package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// retryPathRecordingBackend records the ProjectPath of every Execute call so a
// test can assert which working tree a retry ran in. It always reports success
// but never creates a commit, which forces the runner's no-commit retry path.
type retryPathRecordingBackend struct {
	mu           sync.Mutex
	projectPaths []string
}

func (b *retryPathRecordingBackend) Name() string      { return "retry-path-recording" }
func (b *retryPathRecordingBackend) IsAvailable() bool { return true }

func (b *retryPathRecordingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.projectPaths = append(b.projectPaths, opts.ProjectPath)
	b.mu.Unlock()
	return &BackendResult{Success: true, Output: "no changes made"}, nil
}

func (b *retryPathRecordingBackend) paths() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.projectPaths))
	copy(out, b.projectPaths)
	return out
}

// TestRunner_NoCommitRetry_RunsInWorktree is the TASK-323 regression guard.
//
// Before the fix both retry paths passed ProjectPath: task.ProjectPath, so a
// retry executed against the user's real repository instead of the isolated
// worktree — defeating worktree isolation AND guaranteeing the post-retry
// CountNewCommits (which inspects the worktree) always saw zero commits, so the
// retry could never clear a no_changes failure. With the fix the retry runs in
// the worktree (executionPath).
func TestRunner_NoCommitRetry_RunsInWorktree(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &retryPathRecordingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: true} // enable worktree isolation
	runner.SetSkipPreflightChecks(true)               // mock backend: skip env preflight
	runner.SetRecordingEnabled(false)

	task := &Task{
		ID:          "GH-323",
		Title:       "force a no-commit retry in worktree mode",
		Description: "the backend never commits, so the no-commit retry fires",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-323", // Branch + CreatePR + !DirectCommit → worktree + no-commit retry
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Result is a no_changes failure (no commits ever produced) — we only care
	// about which path the retry executed in.
	_, _ = runner.Execute(ctx, task)

	paths := backend.paths()
	if len(paths) < 2 {
		t.Fatalf("expected >=2 backend calls (initial + no-commit retry), got %d: %v", len(paths), paths)
	}

	// The initial execution must run in the worktree (existing GH-936 guarantee).
	if !strings.Contains(paths[0], "pilot-worktree-") {
		t.Errorf("initial execution ProjectPath %q should be the isolated worktree", paths[0])
	}

	// TASK-323: the retry must ALSO run in the worktree, not task.ProjectPath.
	retryPath := paths[len(paths)-1]
	if retryPath == task.ProjectPath {
		t.Errorf("retry ran in task.ProjectPath %q; it must run in the worktree", task.ProjectPath)
	}
	if !strings.Contains(retryPath, "pilot-worktree-") {
		t.Errorf("retry ProjectPath %q should be the isolated worktree (contain 'pilot-worktree-')", retryPath)
	}
}

// commitAndRecordBackend records the ProjectPath of every Execute call and
// creates a real commit in that path, so the runner proceeds past the
// no-commit checks into the quality-gate loop.
type commitAndRecordBackend struct {
	retryPathRecordingBackend
	commits int
}

func (b *commitAndRecordBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.projectPaths = append(b.projectPaths, opts.ProjectPath)
	b.commits++
	n := b.commits
	b.mu.Unlock()

	fname := filepath.Join(opts.ProjectPath, fmt.Sprintf("change_%d.txt", n))
	if err := os.WriteFile(fname, []byte("x"), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", fmt.Sprintf("change %d", n)}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = opts.ProjectPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %v (%s)", args, err, out)
		}
	}
	return &BackendResult{Success: true, Output: "done"}, nil
}

// failingQualityChecker always fails with ShouldRetry so the quality-gate
// retry path fires.
type failingQualityChecker struct{}

func (c *failingQualityChecker) Check(_ context.Context) (*QualityOutcome, error) {
	return &QualityOutcome{
		Passed:        false,
		ShouldRetry:   true,
		RetryFeedback: "synthetic gate failure",
		Attempt:       1,
	}, nil
}

// TestRunner_QualityRetry_RunsInWorktree is the GH-3577 regression guard.
//
// Before the fix the quality-gate retry (and intent retry) passed
// ProjectPath: task.ProjectPath — the daemon's repo root — so retry workers
// executed outside the isolated worktree. Their fixes were invisible to the
// gate loop re-testing the worktree, making quality retries structurally
// unable to succeed, and the retry sessions ran loose in the user's real repo
// (the GH-3573 incident: a retry reimplemented the whole task in a foreign
// .claude/worktrees branch while gates kept failing against the worktree).
func TestRunner_QualityRetry_RunsInWorktree(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &commitAndRecordBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: true} // enable worktree isolation
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &failingQualityChecker{}
	})

	task := &Task{
		ID:          "GH-3577",
		Title:       "force a quality-gate retry in worktree mode",
		Description: "gates always fail, so the quality retry fires",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-3577",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// The task fails (gates never pass) — we only care which path every
	// backend invocation, including the quality retries, executed in.
	_, _ = runner.Execute(ctx, task)

	paths := backend.paths()
	if len(paths) < 2 {
		t.Fatalf("expected >=2 backend calls (initial + quality retry), got %d: %v", len(paths), paths)
	}
	for i, p := range paths {
		if p == task.ProjectPath {
			t.Errorf("backend call %d ran in task.ProjectPath %q; it must run in the worktree", i, p)
		}
		if !strings.Contains(p, "pilot-worktree-") {
			t.Errorf("backend call %d ProjectPath %q should be the isolated worktree (contain 'pilot-worktree-')", i, p)
		}
	}
}
