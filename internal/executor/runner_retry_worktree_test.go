package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	runner.config = &BackendConfig{UseWorktree: boolPtr(true)} // enable worktree isolation
	runner.SetSkipPreflightChecks(true)                        // mock backend: skip env preflight
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
	runner.config = &BackendConfig{UseWorktree: boolPtr(true)} // enable worktree isolation
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetQualityCheckerFactory(func(taskID, projectPath, _ string) QualityChecker {
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

// headRecordingBackend records the worktree HEAD at the first Execute call, which
// is the base ref the worktree was cut from before any task commit exists.
type headRecordingBackend struct {
	retryPathRecordingBackend
	head string
}

func (b *headRecordingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.projectPaths = append(b.projectPaths, opts.ProjectPath)
	if b.head == "" {
		out, err := exec.CommandContext(ctx, "git", "-C", opts.ProjectPath, "rev-parse", "HEAD").Output()
		if err != nil {
			return nil, err
		}
		b.head = strings.TrimSpace(string(out))
	}
	return &BackendResult{Success: true, Output: "no changes made"}, nil
}

// holdFooterRe reads the hold-meta footer the way the dispatcher does.
var holdFooterRe = regexp.MustCompile(`<!-- ` + PrePRHoldMetaMarker + ` branch:(\S+) sha:(\S+) source:(\d+) -->`)

// TestRetryWorktree_ContinuesHeldBranch pins GH-6233: a retry after a pre-PR hold
// builds its worktree from the branch the hold pushed. The branch and sha come
// out of a real hold comment's footer, so a hold that stops writing the footer
// fails here too.
func TestRetryWorktree_ContinuesHeldBranch(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", localRepo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// The held branch: one commit past main, on origin only.
	mainSHA := git("rev-parse", "HEAD")
	git("checkout", "-b", "held-tmp")
	if err := os.WriteFile(filepath.Join(localRepo, "held.txt"), []byte("held work"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "held work")
	holdSHA := git("rev-parse", "HEAD")
	git("push", "origin", "held-tmp:refs/heads/pilot/GH-6233")
	git("checkout", "main")
	git("branch", "-D", "held-tmp")
	if holdSHA == mainSHA {
		t.Fatal("test setup: the held commit equals main")
	}

	// What the hold wrote on the issue, and what the dispatcher reads back.
	pushed := true
	review := &PrePRReviewOutcome{
		Mode:    PrePRReviewModeEnforce,
		Verdict: PostMergeRequestChanges,
		Review:  &PostMergeReview{Verdict: PostMergeRequestChanges, Defects: holdDefects},
	}
	holdTask := &Task{ID: "GH-6233", Branch: "pilot/GH-6233"}
	comment := renderPrePRHoldComment(holdTask, review, &ExecutionResult{BranchPushed: &pushed}, holdSHA, "")
	m := holdFooterRe.FindStringSubmatch(comment)
	if m == nil {
		t.Fatalf("the hold comment carries no hold-meta footer:\n%s", comment)
	}

	// run queues the task the way the daemon does: the dispatcher saves it on the
	// execution row and the worker runs buildTaskFromExecution's rebuild, not the
	// task the handler built. A field that is not persisted never reaches the runner.
	run := func(t *testing.T, continueSHA string) (head string, runner *Runner, task *Task) {
		t.Helper()
		store, cleanup := setupTestStore(t)
		defer cleanup()
		queued := &Task{
			ID:                "GH-6233",
			Title:             "retry after a pre-PR hold",
			Description:       "continue the held branch",
			ProjectPath:       localRepo,
			Branch:            m[1],
			CreatePR:          true,
			ContinueBranchSHA: continueSHA,
		}
		execID, err := NewExecutionLifecycle(store).Begin(queued, ExecStatusQueued, 0)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		row, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		task = buildTaskFromExecution(row)

		backend := &headRecordingBackend{}
		runner = NewRunnerWithBackend(backend)
		runner.config = &BackendConfig{UseWorktree: boolPtr(true)}
		runner.SetSkipPreflightChecks(true)
		runner.SetRecordingEnabled(false)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = runner.Execute(ctx, task)
		backend.mu.Lock()
		defer backend.mu.Unlock()
		return backend.head, runner, task
	}

	t.Run("continuation starts at the hold sha, not a borrow", func(t *testing.T) {
		head, runner, task := run(t, m[2])
		if task.ContinueBranchSHA != m[2] {
			t.Fatalf("the task rebuilt from the execution row carries ContinueBranchSHA %q, want %q", task.ContinueBranchSHA, m[2])
		}
		if head != holdSHA {
			t.Errorf("worktree HEAD = %q, want the hold sha %q", head, holdSHA)
		}
		if runner.onBorrowedBranch(task) {
			t.Error("a hold continuation must not read as a borrowed branch: the retry has to be able to hold again")
		}
	})

	t.Run("without the continuation the worktree is cut from the default branch", func(t *testing.T) {
		_ = exec.Command("git", "-C", localRepo, "branch", "-D", "pilot/GH-6233").Run()
		head, _, _ := run(t, "")
		if head != mainSHA {
			t.Errorf("worktree HEAD = %q, want main %q", head, mainSHA)
		}
	})
}

// TestRetryWorktree_ContinuationRecordsNoInheritedHead pins GH-6233: a hold
// continuation re-picked at generation > 0 must not stamp the held head as an
// inherited head, since the lease push on it would replace the held work.
func TestRetryWorktree_ContinuationRecordsNoInheritedHead(t *testing.T) {
	tests := []struct {
		name        string
		continueSHA string
		want        string
	}{
		{"fresh re-pick inherits the stale head", "", "deadbeefcafe"},
		{"hold continuation records nothing", "b57d141c0ffee0123456789abcdef0123456789a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			const taskID, projectPath = "GH-6233", "/project-gh-6233"
			priorID, err := NewExecutionLifecycle(store).Begin(&Task{ID: taskID, ProjectPath: projectPath}, ExecStatusRunning, 0)
			if err != nil {
				t.Fatalf("setup: generation 0 Begin failed: %v", err)
			}
			// "failed" rather than needs_human: the latter is a terminal completion
			// the dispatcher refuses to re-admit, and the re-arm that clears it is
			// not what this test exercises — only the generation > 0 re-pick is.
			if err := store.UpdateExecutionStatus(priorID, "failed", "earlier run"); err != nil {
				t.Fatalf("setup: mark failed: %v", err)
			}

			prev := staleBranchHeadProbe
			staleBranchHeadProbe = func(context.Context, string, string) string { return "deadbeefcafe" }
			defer func() { staleBranchHeadProbe = prev }()

			dispatcher := NewDispatcher(store, NewRunner(), nil)
			if err := dispatcher.Start(context.Background()); err != nil {
				t.Fatalf("failed to start dispatcher: %v", err)
			}
			defer dispatcher.Stop()

			task := &Task{ID: taskID, ProjectPath: projectPath, Branch: "pilot/" + taskID, ContinueBranchSHA: tt.continueSHA}
			execID, err := dispatcher.queueSingleTask(context.Background(), task)
			if err != nil || execID == "" {
				t.Fatalf("queueSingleTask = %q, %v, want a queued execution", execID, err)
			}
			exec, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if exec.InheritedHead != tt.want {
				t.Errorf("InheritedHead = %q, want %q", exec.InheritedHead, tt.want)
			}
			// What the worker runs is the rebuild, so neither field may be lost to it.
			rebuilt := buildTaskFromExecution(exec)
			if rebuilt.ContinueBranchSHA != tt.continueSHA || rebuilt.inheritedHeadSHA != tt.want {
				t.Errorf("rebuilt task (ContinueBranchSHA %q, inheritedHeadSHA %q), want (%q, %q)",
					rebuilt.ContinueBranchSHA, rebuilt.inheritedHeadSHA, tt.continueSHA, tt.want)
			}
		})
	}
}
