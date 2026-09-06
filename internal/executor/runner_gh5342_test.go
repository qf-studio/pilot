package executor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFinalizeCtx is the GH-5342 unit-level guard for finalizeCtx: a blown
// deadline must get a fresh, bounded window (so finalization can still push
// already-committed work), while an explicit cancellation or a still-live
// deadline must be left untouched.
func TestFinalizeCtx(t *testing.T) {
	t.Run("expired deadline gets a fresh usable ctx", func(t *testing.T) {
		parent, parentCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
		defer parentCancel()
		if parent.Err() != context.DeadlineExceeded {
			t.Fatalf("test setup: parent ctx should already be expired, got %v", parent.Err())
		}

		fresh, cancel := finalizeCtx(parent, time.Minute)
		defer cancel()

		if fresh.Err() != nil {
			t.Errorf("expected the fresh ctx to still be usable, got Err()=%v", fresh.Err())
		}
		if deadline, ok := fresh.Deadline(); !ok || time.Until(deadline) < 30*time.Second {
			t.Errorf("expected a fresh ~1m deadline, got ok=%v deadline=%v", ok, deadline)
		}
	})

	t.Run("explicit cancellation is preserved, not overridden", func(t *testing.T) {
		parent, parentCancel := context.WithCancel(context.Background())
		parentCancel()
		if parent.Err() != context.Canceled {
			t.Fatalf("test setup: parent ctx should be canceled, got %v", parent.Err())
		}

		derived, cancel := finalizeCtx(parent, time.Minute)
		defer cancel()

		if derived.Err() != context.Canceled {
			t.Errorf("a genuine cancellation must stay canceled, got Err()=%v", derived.Err())
		}
	})

	t.Run("live ctx is passed through unaffected", func(t *testing.T) {
		parent, parentCancel := context.WithTimeout(context.Background(), time.Hour)
		defer parentCancel()

		derived, cancel := finalizeCtx(parent, time.Minute)
		defer cancel()

		if derived.Err() != nil {
			t.Errorf("expected a live ctx to remain usable, got Err()=%v", derived.Err())
		}
		deadline, ok := derived.Deadline()
		if !ok {
			t.Fatal("expected the derived ctx to inherit the parent's deadline")
		}
		if time.Until(deadline) < 30*time.Minute {
			t.Errorf("expected the derived ctx to keep the parent's long deadline, got %v remaining", time.Until(deadline))
		}
	})
}

// TestFinalizeEpicBranchPR_ExpiredTaskCtxWithCommits_StillPushesAndCreatesPR
// is the GH-5342 regression guard on the epic finalize path: a branch that
// carries real commits must still get pushed and turned into a PR even when
// the incoming ctx's deadline has already been blown by a backend run that
// legitimately used the full task timeout — not silently misread as "zero
// commits" (CountNewCommitsAgainstOrigin failing instantly on a dead ctx)
// and recorded as no_op, discarding already-committed work (GH-263 x3).
func TestFinalizeEpicBranchPR_ExpiredTaskCtxWithCommits_StillPushesAndCreatesPR(t *testing.T) {
	capturedTitleFile := setUpFakeGhPRCreatePATH(t)

	branch := "pilot/GH-5342-epic"
	dir := initRepoWithRemoteAndFeatureBranch(t, branch)

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-5342", Success: true, IsEpic: true}
	task := &Task{
		ID:          "GH-5342",
		Title:       "fix: post-timeout finalization must not discard committed work",
		Description: "d",
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}

	// Simulate a task ctx whose deadline was already blown by the time
	// finalization runs — the exact shape a full-length backend run that
	// still landed real commits produces.
	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	if expiredCtx.Err() != context.DeadlineExceeded {
		t.Fatalf("test setup: ctx should already be expired, got %v", expiredCtx.Err())
	}

	r.finalizeEpicBranchPR(expiredCtx, task, NewGitOperations(dir), result, nil)

	if !result.Success {
		t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
	}
	if result.Outcome == "no_op" {
		t.Errorf("committed work must never be recorded as no_op on an expired ctx, got Outcome=%q error=%q", result.Outcome, result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL, got none")
	}
	if _, err := os.Stat(capturedTitleFile); err != nil {
		t.Errorf("gh pr create was never invoked: %v", err)
	}

	// The branch must have actually landed on the remote, not just locally —
	// a fresh ctx that only ever timed out locally without reaching git would
	// pass a weaker assertion but still leave the work stranded.
	remoteBranches := gitOutput(t, dir, "ls-remote", "origin", branch)
	if strings.TrimSpace(remoteBranches) == "" {
		t.Errorf("expected branch %q to be pushed to origin, ls-remote returned nothing", branch)
	}
}

// TestFinalizeEpicBranchPR_CanceledTaskCtx_FailsInsteadOfSilentlyProceeding
// verifies the other half of finalizeCtx's contract: an explicit
// cancellation (context.Canceled — a real stop request, not an exhausted
// budget) must NOT be papered over with a fresh context. The guard's git
// call fails on the canceled ctx, and that failure must surface as a hard
// failure (never no_op), not a silently-granted extra 5 minutes to keep
// working after being told to stop.
func TestFinalizeEpicBranchPR_CanceledTaskCtx_FailsInsteadOfSilentlyProceeding(t *testing.T) {
	setUpFakeGhPRCreatePATH(t)

	branch := "pilot/GH-5342-epic-canceled"
	dir := initRepoWithRemoteAndFeatureBranch(t, branch)

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-5342", Success: true, IsEpic: true}
	task := &Task{
		ID:          "GH-5342",
		Title:       "fix: post-timeout finalization must not discard committed work",
		Description: "d",
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	r.finalizeEpicBranchPR(canceledCtx, task, NewGitOperations(dir), result, nil)

	if result.Success {
		t.Error("expected Success=false when the task ctx was explicitly canceled")
	}
	if result.Outcome == "no_op" {
		t.Errorf("a canceled ctx's git failure must not be misread as no_op, got Outcome=%q error=%q", result.Outcome, result.Error)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL, got %q", result.PRUrl)
	}
}

// TestFinalizeDecomposedParentPR_ExpiredTaskCtxWithCommits_StillPushesAndCreatesPR
// mirrors the epic-path regression guard above for the decomposed-parent
// finalize path (runner_decompose.go), which follows the identical
// count-guard-then-push-then-CreatePR shape and had the identical bug.
func TestFinalizeDecomposedParentPR_ExpiredTaskCtxWithCommits_StillPushesAndCreatesPR(t *testing.T) {
	capturedTitleFile := setUpFakeGhPRCreatePATH(t)

	branch := "pilot/GH-5342-decomposed"
	dir := initRepoWithRemoteAndFeatureBranch(t, branch)

	r := newSilentRunnerTask359()
	result := &ExecutionResult{TaskID: "GH-5342", Success: true}
	task := &Task{
		ID:          "GH-5342",
		Title:       "fix: post-timeout finalization must not discard committed work",
		Description: "d",
		Branch:      branch,
		BaseBranch:  "main",
		CreatePR:    true,
	}

	expiredCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	if expiredCtx.Err() != context.DeadlineExceeded {
		t.Fatalf("test setup: ctx should already be expired, got %v", expiredCtx.Err())
	}

	r.finalizeDecomposedParentPR(expiredCtx, task, NewGitOperations(dir), result)

	if !result.Success {
		t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL, got none")
	}
	if _, err := os.Stat(capturedTitleFile); err != nil {
		t.Errorf("gh pr create was never invoked: %v", err)
	}

	remoteBranches := gitOutput(t, dir, "ls-remote", "origin", branch)
	if strings.TrimSpace(remoteBranches) == "" {
		t.Errorf("expected branch %q to be pushed to origin, ls-remote returned nothing", branch)
	}
}

// TestNoCommitRetry_PostRetryCommitCountFailure_NotRecordedAsNoOp is the
// GH-5342 (subtask 2) regression guard for the no-commit-retry insertion
// point (runner.go ~4477, the "Check again after retry" guard): a
// commit-count check that FAILS — e.g. because the task ctx's deadline blew
// while the GH-916 retry backend call was still running — must never be
// read as "confirmed zero commits". Before this fix the count error was
// silently discarded (guardCount always read as if it were 0 on error), so
// a retry that legitimately committed real work — but only returned after
// the outer ctx's deadline had already passed — was recorded as no_op and
// its branch was never pushed (GH-263 x2).
//
// Mirrors TestFinalizeEpicBranchPR_ExpiredTaskCtxWithCommits above, but
// exercises the earlier no-commit-retry insertion point (via the real
// Runner.Execute() path) instead of the finalize-block call sites: only a
// *confirmed* zero count may set no_op; a count failure must fall through
// so the finalize block's own fresh ctx (finalizeCtx) gets a chance to
// detect the real commit and push it.
func TestNoCommitRetry_PostRetryCommitCountFailure_NotRecordedAsNoOp(t *testing.T) {
	capturedTitleFile := setUpFakeGhPRCreatePATH(t)

	const branch = "pilot/GH-5342-postretry"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(call int, _ ExecuteOptions) *BackendResult {
			if call == 1 {
				// Initial attempt: clean tree, no commit — triggers the GH-916 retry.
				return &BackendResult{Success: true, Output: "looked at it, nothing yet"}
			}
			// Retry: lands a real commit, then keeps "running" long enough
			// for the outer (short) task ctx to blow its deadline before
			// returning — reproducing a backend call that legitimately used
			// the remaining budget to land real work.
			writeUncommittedFile(t, dir, "real-work.go")
			runGit(t, dir, "add", "real-work.go")
			runGit(t, dir, "commit", "-m", "real work from retry")
			time.Sleep(1200 * time.Millisecond)
			return &BackendResult{Success: true, Output: "done"}
		},
	}
	runner := newGH4964Runner(backend)
	task := newGH4964Task("GH-5342", branch, dir)
	task.SkipQualityGates = true

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}
	if result.Outcome == "no_op" {
		t.Errorf("committed work must never be recorded as no_op because the post-retry commit-count check failed on an expired ctx, got Outcome=%q error=%q", result.Outcome, result.Error)
	}
	if !result.Success {
		t.Fatalf("expected the branch's real commit to be detected via the finalize block's fresh ctx and result in success, got failure: %s", result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL")
	}
	if _, statErr := os.Stat(capturedTitleFile); statErr != nil {
		t.Errorf("gh pr create was never invoked: %v", statErr)
	}
	if backend.callCount() != 2 {
		t.Errorf("expected backend called twice (initial + GH-916 retry), got %d", backend.callCount())
	}
}

// TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation is the GH-5342
// (subtask 3) regression guard on the intent-judge retry (runner.go
// ~5174, "Handle intent judge result"): unlike the GH-916 no-commit
// retry above — which is naturally gated by a git commit-count call that
// shares ctx and fails fast on a dead one — nothing upstream of the
// intent-judge retry decision shares ctx to fail first. A quality-gate
// retry loop earlier in the same run always gets a fresh, ctx-independent
// timeout (GH-4876) and can burn real wall-clock, so by the time the
// intent judge flags a mismatch, this task's own ctx may already be
// done. Before this fix, the retry unconditionally called
// backendExecute(ctx, ...) — spawning a Claude Code process on an
// already-done ctx that can't produce anything usable (exec.CommandContext
// refuses to even start it), burning a real retry attempt for nothing.
func TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation(t *testing.T) {
	capturedTitleFile := setUpFakeGhPRCreatePATH(t)

	const branch = "pilot/GH-5342-intentretry"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(_ int, _ ExecuteOptions) *BackendResult {
			// Every call (initial or retry) lands a real commit so a
			// non-empty diff always reaches the intent judge; the retry
			// must never actually be invoked, but if the guard regresses
			// this keeps the test failure about the retry itself, not an
			// unrelated no-commit path.
			writeUncommittedFile(t, dir, fmt.Sprintf("work-%d.go", time.Now().UnixNano()))
			runGit(t, dir, "add", ".")
			runGit(t, dir, "commit", "-m", "real work")
			return &BackendResult{Success: true, Output: "done"}
		},
	}
	runner := newGH4964Runner(backend)

	// The judge subprocess is mocked to sleep past the task ctx's short
	// deadline before returning FAIL — reproducing a judge call that
	// legitimately took real wall-clock time (GH-4669's measured judge
	// subprocess latency) and returns only after ctx is already done.
	runner.intentJudge = newIntentJudgeWithRunner(func(context.Context, ...string) ([]byte, error) {
		time.Sleep(400 * time.Millisecond)
		return []byte("VERDICT: FAIL\nThe diff adds unrelated scope.\nCONFIDENCE: 0.9"), nil
	})

	task := newGH4964Task("GH-5342", branch, dir)
	task.SkipQualityGates = true

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if backend.callCount() != 1 {
		t.Errorf("expected backend called once (initial only, intent-judge retry must be skipped on an already-done ctx), got %d", backend.callCount())
	}
	if result.IntentWarning == "" {
		t.Error("expected IntentWarning to carry the judge's FAIL reason even though the retry was skipped")
	}
	if !result.Success {
		t.Fatalf("expected the initial commit to still result in success (intent warning is advisory, not blocking), got failure: %s", result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL")
	}
	if _, statErr := os.Stat(capturedTitleFile); statErr != nil {
		t.Errorf("gh pr create was never invoked: %v", statErr)
	}
}
