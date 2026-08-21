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

// TestQualityGatesRespectSkipQualityGates is the GH-4876 regression guard
// for the primary defect: quality gates used to run unconditionally,
// including for read-only question tasks (no branch, no files written by
// design). Running gates against those failed deterministically and
// triggered a doomed retry cycle against a working tree never set up for
// code work.
//
// The gate is task.SkipQualityGates, NOT task.CreatePR: CreatePR only
// tracks whether a PR gets opened, and plenty of legitimate code tasks
// (direct-commit, etc.) have CreatePR=false while still writing files that
// need linting/testing. Only task construction sites that are certain no
// code will be written (comms question/research/planning/chat handlers)
// set SkipQualityGates.
func TestQualityGatesRespectSkipQualityGates(t *testing.T) {
	tests := []struct {
		name             string
		skipQualityGates bool
		wantGateRun      bool
	}{
		{name: "read-only task (SkipQualityGates=true) skips quality gates", skipQualityGates: true, wantGateRun: false},
		{name: "code task (SkipQualityGates=false) still runs quality gates", skipQualityGates: false, wantGateRun: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projectDir := t.TempDir()

			backend := &mockSelfReviewBackend{output: "done"}
			runner := NewRunnerWithBackend(backend)
			runner.config = &BackendConfig{}
			runner.SetRecordingEnabled(false)
			runner.skipPreflightChecks = true

			gateCalled := false
			runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
				gateCalled = true
				return &mockQualityChecker{outcome: &QualityOutcome{Passed: true}}
			})

			task := &Task{
				ID:               "GH-4876-SKIPGATES",
				Title:            "quality gate SkipQualityGates guard test",
				Description:      "test",
				ProjectPath:      projectDir,
				LocalMode:        true,
				CreatePR:         false, // deliberately false for both cases: CreatePR must not drive gating
				SkipQualityGates: tt.skipQualityGates,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := runner.Execute(ctx, task)
			if err != nil {
				t.Fatalf("Execute() error: %v", err)
			}
			if !result.Success {
				t.Fatalf("Execute() not successful: %s", result.Error)
			}
			if gateCalled != tt.wantGateRun {
				t.Errorf("quality checker factory called = %v, want %v (SkipQualityGates=%v)", gateCalled, tt.wantGateRun, tt.skipQualityGates)
			}
		})
	}
}

// TestQuestionTaskDoesNotAutoEnableQualityGates covers the GH-363
// auto-enable path specifically (a distinct code path from an explicitly
// configured QualityCheckerFactory): a question task's project directory
// may contain detectable build/test commands (here, a go.mod with no .go
// files, so a real `go build ./...` would fail with "no Go files in .").
// For a SkipQualityGates=true task, gates must never be auto-enabled or
// run, so the answer succeeds and the factory stays nil.
func TestQuestionTaskDoesNotAutoEnableQualityGates(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module gh4876question\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("WriteFile go.mod: %v", err)
	}

	backend := &mockSelfReviewBackend{output: "the answer is 42"}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{}
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	// Deliberately no SetQualityCheckerFactory call: this exercises the
	// GH-363 auto-enable path, which would otherwise detect "go build ./..."
	// from the go.mod in projectDir and wire up a build gate on its own.

	task := &Task{
		ID:               "Q-GH-4876-AUTOGATE",
		Title:            "Question: what does this module do?",
		Description:      "Answer this question about the codebase. DO NOT make any changes, only read and analyze.",
		ProjectPath:      projectDir,
		LocalMode:        true,
		CreatePR:         false,
		SkipQualityGates: true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s — a question task must succeed on its answer alone; quality gates must not auto-enable and run `go build ./...` against a project with no .go files", result.Error)
	}
	if runner.qualityCheckerFactory != nil {
		t.Errorf("GH-363 auto-enable wired a quality checker factory for a CreatePR=false question task; it must stay nil")
	}
}

// sleepThenFailQualityChecker always fails with ShouldRetry, and sleeps on
// its first call so tests can force a parent context to expire before the
// quality-gate retry loop reaches the reset/re-invoke step.
type sleepThenFailQualityChecker struct {
	sleep time.Duration
	calls int
}

func (c *sleepThenFailQualityChecker) Check(_ context.Context) (*QualityOutcome, error) {
	c.calls++
	if c.calls == 1 {
		time.Sleep(c.sleep)
	}
	return &QualityOutcome{
		Passed:        false,
		ShouldRetry:   true,
		RetryFeedback: "synthetic gate failure",
		Attempt:       c.calls,
	}, nil
}

// TestQualityGateRetry_UsesFreshContextForResetAndReinvoke is the GH-4876
// regression guard for secondary fix #1: the pre-retry reset and the retry
// backend re-invoke must run on a fresh context.Background()-derived
// deadline, not the (possibly already-exhausted) attempt ctx. We force the
// outer ctx to expire before the gate check returns by sleeping past its
// timeout in the quality checker; if the reset/retry still depended on the
// exhausted ctx, both would fail immediately with "context deadline
// exceeded" and the backend would never be re-invoked.
func TestQualityGateRetry_UsesFreshContextForResetAndReinvoke(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &stackingAttemptBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	checker := &sleepThenFailQualityChecker{sleep: 1200 * time.Millisecond}
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return checker
	})

	task := &Task{
		ID:          "GH-4876-CTX",
		Title:       "quality retry must not reuse an exhausted parent context",
		Description: "gates always fail; the outer ctx expires before the first retry",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4876-ctx",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	result, _ := runner.Execute(ctx, task)
	if result == nil {
		t.Fatal("Execute() returned nil result")
	}
	if strings.Contains(result.Error, "context deadline exceeded") {
		t.Fatalf("quality-gate retry failed against the exhausted parent context's deadline instead of a fresh one: %s", result.Error)
	}
	if calls := backend.callCount(); calls < 2 {
		t.Fatalf("expected the retry to actually re-invoke the backend on a fresh context (>=2 calls), got %d", calls)
	}
}

// gitRepoDeletingBackend commits a new file on each call, and after its
// first commit deletes .git entirely — simulating a working tree that is
// left in a genuinely broken state (not merely a context timeout) so that
// the subsequent pre-retry reset must fail for real.
type gitRepoDeletingBackend struct {
	mu    sync.Mutex
	calls int
}

func (b *gitRepoDeletingBackend) Name() string      { return "git-repo-deleting" }
func (b *gitRepoDeletingBackend) IsAvailable() bool { return true }

func (b *gitRepoDeletingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.calls++
	n := b.calls
	b.mu.Unlock()

	fname := filepath.Join(opts.ProjectPath, fmt.Sprintf("attempt_%d.txt", n))
	if err := os.WriteFile(fname, []byte("x"), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", fmt.Sprintf("attempt %d", n)}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = opts.ProjectPath
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %v: %v (%s)", args, err, out)
		}
	}

	if n == 1 {
		if err := os.RemoveAll(filepath.Join(opts.ProjectPath, ".git")); err != nil {
			return nil, fmt.Errorf("remove .git: %v", err)
		}
	}

	return &BackendResult{Success: true, Output: "done"}, nil
}

func (b *gitRepoDeletingBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// TestQualityGateRetry_AbortsOnFailedReset is the GH-4876 regression guard
// for secondary fix #2: a failed pre-retry reset must abort the retry
// (task fails with a clear error) instead of logging a warning and
// proceeding to re-invoke the backend on top of an unknown working-tree
// state. The backend deletes .git after its first commit, guaranteeing the
// reset genuinely fails; if the retry proceeded anyway, the backend would
// be invoked a second time.
func TestQualityGateRetry_AbortsOnFailedReset(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &gitRepoDeletingBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return &failingQualityChecker{}
	})

	task := &Task{
		ID:          "GH-4876-ABORT",
		Title:       "failed pre-retry reset must abort the retry",
		Description: "gates always fail; the repo is corrupted after the first attempt",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-4876-abort",
		CreatePR:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, _ := runner.Execute(ctx, task)
	if result == nil || result.Success {
		t.Fatalf("expected task failure (reset failed, retry aborted), got %+v", result)
	}
	if !strings.Contains(result.Error, "failed to reset to pre-attempt state") {
		t.Errorf("result.Error = %q, want it to mention the failed reset", result.Error)
	}
	if calls := backend.callCount(); calls != 1 {
		t.Errorf("backend called %d times, want exactly 1 (retry must be aborted, not re-invoked, after a failed reset)", calls)
	}
}
