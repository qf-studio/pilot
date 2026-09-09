package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSuccessPath_TaskCtxExpired_GatesFail_HoldsBranchNoReinvocation is the
// GH-5399 regression guard for Fix 1: GH-5346 forbade re-invoking Claude Code
// once the backend has already burned the task's full budget — but that
// guarantee only covered the *error* path (attemptBackendTimeoutSalvage,
// reached when the backend itself returns a ctx-deadline error). It did not
// cover the *success* path: a backend that ignores the ctx it's handed (as
// some backend implementations effectively do once they've already started
// an uninterruptible tool call) can return Success:true well after the
// task's own deadline has passed. Before this fix, a failing post-hoc
// quality-gate check on that stale success would still fall into the normal
// gate-retry loop and re-invoke the backend via a fresh context.Background()
// timeout — exactly the unbounded second invocation GH-5346 closed for the
// error path. This test drives that exact shape: a backend that ignores ctx,
// commits real work, and returns success only after the task ctx has
// already been canceled; paired with a quality checker that always fails
// with ShouldRetry:true. The backend must be invoked exactly once, and the
// salvaged commit must be pushed and held under pilot-needs-human rather
// than retried.
//
// GH-5408: the deadline is an explicitly-canceled context.WithCancel,
// canceled from inside the backend's own call, rather than a fixed-duration
// context.WithTimeout the backend had to out-sleep. The prior version used a
// 50ms timeout and had the backend sleep 150ms past it, racing that fixed
// budget against however long setupFreshnessRepo/checkout takes on the test
// host — on macOS the branch switch alone routinely exceeded 50ms, so the
// ctx (derived from the same 50ms parent inside executeWithOptions, which
// keeps a parent's already-elapsed deadline) was sometimes already Done()
// before the backend was even invoked, failing 6/6 locally while passing on
// (faster) Linux CI. Canceling from inside the mock backend — which ignores
// ctx entirely (mockGH4964Backend.Execute takes `_ context.Context`), so
// cancellation here has no side effect beyond flipping ctx.Err() — makes
// "the deadline elapses after the backend was called but before it
// returned" a deterministic ordering instead of a wall-clock race.
func TestSuccessPath_TaskCtxExpired_GatesFail_HoldsBranchNoReinvocation(t *testing.T) {
	capturedTitleFile, capturedLabelEditsFile := setUpFakeGhPRCreateAndLabelPATH(t)

	const branch = "pilot/GH-5399-taskctx-expired-gates-fail"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	var cancel context.CancelFunc
	backend := &mockGH4964Backend{
		perCall: func(_ int, _ ExecuteOptions) *BackendResult {
			// Simulate a backend that ignores the ctx it was handed (e.g.
			// already mid an uninterruptible tool call) and only returns
			// once the task's own deadline has already elapsed — deterministically,
			// by canceling that deadline from here rather than out-sleeping a timer.
			writeUncommittedFile(t, dir, "salvaged.go")
			runGit(t, dir, "add", "salvaged.go")
			runGit(t, dir, "commit", "-m", "real work landed after the task deadline elapsed")
			cancel()
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	runner.qualityCheckerFactory = func(string, string) QualityChecker {
		return &stubQualityChecker{outcome: &QualityOutcome{Passed: false, ShouldRetry: true}}
	}

	task := newGH4964Task("GH-5399", branch, dir)

	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once (GH-5399: a failing gate check after the task's deadline already passed must never re-invoke Claude Code), got %d", backend.callCount())
	}
	if result.Success {
		t.Errorf("expected Success=false when post-deadline quality gates fail, got true")
	}
	if result.Outcome != "needs_human" {
		t.Errorf("expected Outcome=%q, got %q (error=%q)", "needs_human", result.Outcome, result.Error)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL (gates failed), got %q", result.PRUrl)
	}
	if _, statErr := os.Stat(capturedTitleFile); statErr == nil {
		t.Error("gh pr create must not be invoked when post-deadline quality gates fail")
	}

	remoteBranches := gitOutput(t, dir, "ls-remote", "origin", branch)
	if strings.TrimSpace(remoteBranches) == "" {
		t.Errorf("expected branch %q to still be pushed to origin even though gates failed, ls-remote returned nothing", branch)
	}

	labelEdits, readErr := os.ReadFile(capturedLabelEditsFile)
	if readErr != nil {
		t.Fatalf("expected a `gh issue edit` call applying pilot-needs-human, got no captured calls: %v", readErr)
	}
	if !strings.Contains(string(labelEdits), labelPilotNeedsHuman) {
		t.Errorf("expected the captured `gh issue edit` call to add %q, got:\n%s", labelPilotNeedsHuman, labelEdits)
	}
}

// TestSuccessPath_TaskCtxExpired_GatesFail_NoPR_FailsNormally is the GH-5408
// regression guard for Fix 3: the success-path hold above (Fix 1 of GH-5399)
// called holdPushedBranch whenever taskCtxWasDone was true, without the
// error path's precondition (attemptBackendTimeoutSalvage, ~2776: git == nil
// || task.Branch == "" || task.DirectCommit || !task.CreatePR). A task with
// CreatePR=false (LocalMode and other non-PR code tasks) has no PR-driven
// issue hand-off for holdPushedBranch's pilot-needs-human comment/label to
// mean anything, but was parked there anyway — hiding it from the ordinary
// failure ladder (TaskFailed alert, webhook, recorder.Finish("failed")) that
// every other exhausted-retries task goes through. This drives the same
// ctx-expired-mid-backend-call, gates-keep-failing shape as
// TestSuccessPath_TaskCtxExpired_GatesFail_HoldsBranchNoReinvocation above,
// but with CreatePR=false: the backend must still be invoked exactly once
// (taskCtxWasDone forbids the retry regardless of whether the hold
// precondition holds), but the task must fail normally instead of being
// parked needs_human.
func TestSuccessPath_TaskCtxExpired_GatesFail_NoPR_FailsNormally(t *testing.T) {
	capturedTitleFile, capturedLabelEditsFile := setUpFakeGhPRCreateAndLabelPATH(t)

	const branch = "pilot/GH-5408-taskctx-expired-nopr"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	var cancel context.CancelFunc
	backend := &mockGH4964Backend{
		perCall: func(_ int, _ ExecuteOptions) *BackendResult {
			writeUncommittedFile(t, dir, "salvaged.go")
			runGit(t, dir, "add", "salvaged.go")
			runGit(t, dir, "commit", "-m", "real work landed after the task deadline elapsed")
			cancel()
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	runner.qualityCheckerFactory = func(string, string) QualityChecker {
		return &stubQualityChecker{outcome: &QualityOutcome{Passed: false, ShouldRetry: true}}
	}

	task := newGH4964Task("GH-5408", branch, dir)
	task.CreatePR = false // no-PR task: nothing for holdPushedBranch to hand off

	var ctx context.Context
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once (taskCtxWasDone must still forbid re-invocation even when the hold precondition fails), got %d", backend.callCount())
	}
	if result.Success {
		t.Errorf("expected Success=false when post-deadline quality gates fail, got true")
	}
	if result.Outcome == "needs_human" {
		t.Errorf("expected a no-PR task never to be parked needs_human, got Outcome=%q (error=%q)", result.Outcome, result.Error)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL (gates failed), got %q", result.PRUrl)
	}
	if _, statErr := os.Stat(capturedTitleFile); statErr == nil {
		t.Error("gh pr create must not be invoked when post-deadline quality gates fail")
	}
	if _, statErr := os.Stat(capturedLabelEditsFile); statErr == nil {
		labelEdits, _ := os.ReadFile(capturedLabelEditsFile)
		t.Errorf("expected no pilot-needs-human label edit for a no-PR task, got:\n%s", labelEdits)
	}
}

// TestBackendTimeoutSalvage_PushFailure_PreservesWorktreeAndBranch is the
// GH-5399 regression guard for Fix 3: previously holdPushedBranch shared one
// 30s context.WithoutCancel(ctx) window across the branch push AND the
// comment/label calls, and — regardless of budget — a push that still ended
// up failing simply fell through to executeWithOptions's ordinary worktree
// cleanup defer, which force-deletes the local branch (`git branch -D`) and
// removes the worktree directory. That destroys the ONLY remaining copy of
// the salvaged commits: they were never reachable from the remote (the push
// failed) and now they're gone locally too. This test drives a real push
// failure (a broken origin remote) through the full Execute() pipeline with
// worktree isolation enabled, and asserts the worktree directory and its
// local branch both survive on disk, and the needs-human comment names them.
func TestBackendTimeoutSalvage_PushFailure_PreservesWorktreeAndBranch(t *testing.T) {
	ghLogPath := filepath.Join(t.TempDir(), "gh-calls.log")
	ghAppendLogScript(t, ghLogPath)

	const branch = "pilot/GH-5399-push-failure-preserve"
	dir, _ := setupFreshnessRepo(t)

	var mu sync.Mutex
	var capturedWorktreePath string

	backend := &ctxRespectingBackend{
		run: func(ctx context.Context, _ int, opts ExecuteOptions) (*BackendResult, error) {
			mu.Lock()
			capturedWorktreePath = opts.ProjectPath
			mu.Unlock()

			// Break the shared origin remote (worktrees share the main
			// repo's .git config) from inside the callback, once the
			// worktree — and its own fetch of origin/main — already
			// exist, so the later salvage push genuinely fails.
			runGit(t, opts.ProjectPath, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "unreachable-remote"))

			writeUncommittedFile(t, opts.ProjectPath, "salvaged.go")
			runGit(t, opts.ProjectPath, "add", "salvaged.go")
			runGit(t, opts.ProjectPath, "commit", "-m", "real work before the deadline hit")
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	runner := newGH4964Runner(backend)
	runner.config.UseWorktree = true
	runner.qualityCheckerFactory = func(string, string) QualityChecker {
		return &stubQualityChecker{outcome: &QualityOutcome{Passed: true}}
	}

	task := newGH4964Task("GH-5399", branch, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	mu.Lock()
	worktreePath := capturedWorktreePath
	mu.Unlock()
	if worktreePath == "" {
		t.Fatal("backend was never invoked with a worktree ProjectPath — test setup is broken")
	}

	if result.Success {
		t.Errorf("expected Success=false when the salvage push fails, got true")
	}
	if result.Outcome != "needs_human" {
		t.Errorf("expected Outcome=%q, got %q (error=%q)", "needs_human", result.Outcome, result.Error)
	}

	if _, statErr := os.Stat(worktreePath); statErr != nil {
		t.Errorf("expected the worktree directory %q to survive a failed salvage push, but it's gone: %v", worktreePath, statErr)
	}

	branchList := gitOutput(t, dir, "branch", "--list", branch)
	if strings.TrimSpace(branchList) == "" {
		t.Errorf("expected local branch %q to survive a failed salvage push, but `git branch --list` found nothing", branch)
	}

	calls := parseGhInvocations(t, ghLogPath)
	var commentBody string
	for _, args := range calls {
		if len(args) >= 3 && args[0] == "issue" && args[1] == "comment" {
			for i, a := range args {
				if a == "--body" && i+1 < len(args) {
					commentBody = args[i+1]
				}
			}
		}
	}
	if commentBody == "" {
		t.Fatalf("expected a `gh issue comment` call recording the needs-human hold, got calls: %v", calls)
	}
	if !strings.Contains(commentBody, worktreePath) {
		t.Errorf("expected the needs-human comment to name the preserved worktree path %q, got:\n%s", worktreePath, commentBody)
	}
	if !strings.Contains(commentBody, branch) {
		t.Errorf("expected the needs-human comment to name the branch %q, got:\n%s", branch, commentBody)
	}

	var labelCall string
	for _, args := range calls {
		if len(args) >= 3 && args[0] == "issue" && args[1] == "edit" {
			labelCall = strings.Join(args, " ")
		}
	}
	if !strings.Contains(labelCall, labelPilotNeedsHuman) {
		t.Errorf("expected a `gh issue edit` call adding %q, got:\n%s", labelPilotNeedsHuman, labelCall)
	}
}
