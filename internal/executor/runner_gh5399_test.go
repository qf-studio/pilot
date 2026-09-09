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
// sleeps past the task's deadline, commits real work, and returns success;
// paired with a quality checker that always fails with ShouldRetry:true. The
// backend must be invoked exactly once, and the salvaged commit must be
// pushed and held under pilot-needs-human rather than retried.
func TestSuccessPath_TaskCtxExpired_GatesFail_HoldsBranchNoReinvocation(t *testing.T) {
	capturedTitleFile, capturedLabelEditsFile := setUpFakeGhPRCreateAndLabelPATH(t)

	const branch = "pilot/GH-5399-taskctx-expired-gates-fail"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, _ ExecuteOptions) *BackendResult {
			// Simulate a backend that ignores the ctx it was handed (e.g.
			// already mid an uninterruptible tool call) and only returns
			// once the task's own deadline has already elapsed.
			writeUncommittedFile(t, dir, "salvaged.go")
			runGit(t, dir, "add", "salvaged.go")
			runGit(t, dir, "commit", "-m", "real work landed after the task deadline elapsed")
			time.Sleep(150 * time.Millisecond)
			return &BackendResult{Success: true}
		},
	}
	runner := newGH4964Runner(backend)
	runner.qualityCheckerFactory = func(string, string) QualityChecker {
		return &stubQualityChecker{outcome: &QualityOutcome{Passed: false, ShouldRetry: true}}
	}

	task := newGH4964Task("GH-5399", branch, dir)

	// A short deadline that will have elapsed by the time the ctx-ignoring
	// backend above returns (it sleeps 150ms past its call).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
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
