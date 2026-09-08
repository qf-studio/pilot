package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	task := newGH4964Task("GH-5342", branch, dir)
	task.SkipQualityGates = true

	// GH-5355: everything before the intent judge runs — branch switch,
	// the mock backend's commit, and GetDiffAgainstOrigin's git
	// fetch/merge-base/diff spawns — is real git subprocess work, not
	// mocked. A 200ms budget here previously raced that work: under
	// package-level contention (`go test -short ./...` running many
	// packages concurrently) those spawns occasionally took long enough to
	// blow the deadline *before* the intent judge goroutine was even
	// reached, so GetDiffAgainstOrigin failed on an already-done ctx and
	// runIntentJudge flipped to false — skipping the intent judge
	// entirely (not just the retry) and leaving IntentWarning empty. A
	// generous ctx budget keeps that setup work outside the race window
	// entirely, regardless of load.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The judge subprocess is mocked to block until the task ctx it was
	// handed is actually done, rather than guessing a fixed sleep duration
	// long enough to outlast a fixed ctx timeout — that guess is exactly
	// what raced the setup work above. Waiting on the real ctx.Done()
	// ties "returns after ctx is done" directly to ctx's actual state, so
	// this reproduces a judge call that legitimately took real wall-clock
	// time (GH-4669's measured judge subprocess latency) and returns only
	// after ctx is already done, deterministically and without a sleep.
	runner.intentJudge = newIntentJudgeWithRunner(func(judgeCtx context.Context, _ ...string) ([]byte, error) {
		<-judgeCtx.Done()
		return []byte("VERDICT: FAIL\nThe diff adds unrelated scope.\nCONFIDENCE: 0.9"), nil
	})

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

// TestNoCommitRetry_PostRetryExpiredCtx_NoDeadlineExceededLogNoise is the
// GH-5342 (subtask 4) regression guard: the daemon log for the finalization
// phase must show no "context deadline exceeded" from git/gate calls after
// a backend timeout.
//
// This drives the identical scenario as
// TestNoCommitRetry_PostRetryCommitCountFailure_NotRecordedAsNoOp (subtask
// 2: the GH-916 retry lands a real commit but returns only after the outer
// task ctx's deadline has blown, so the post-retry commit-count re-check at
// runner.go ~4477 fails on the dead ctx) but asserts on the *logged output*
// instead of the result: subtask 2 already made sure this dead-ctx count
// failure is never misread as a confirmed no_op, but it still logged the
// failure at Warn with the raw "context deadline exceeded" error text —
// exactly the kind of daemon-log noise that looks like a real problem for a
// designed, expected outcome. logGitCtxErr (runner.go) downgrades this to
// Debug once ctx is already Done(), so at the default (Info) log level the
// message must not appear at all.
func TestNoCommitRetry_PostRetryExpiredCtx_NoDeadlineExceededLogNoise(t *testing.T) {
	setUpFakeGhPRCreatePATH(t)

	const branch = "pilot/GH-5342-logquiet"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &mockGH4964Backend{
		perCall: func(call int, _ ExecuteOptions) *BackendResult {
			if call == 1 {
				return &BackendResult{Success: true, Output: "looked at it, nothing yet"}
			}
			writeUncommittedFile(t, dir, "real-work.go")
			runGit(t, dir, "add", "real-work.go")
			runGit(t, dir, "commit", "-m", "real work from retry")
			time.Sleep(1200 * time.Millisecond)
			return &BackendResult{Success: true, Output: "done"}
		},
	}
	runner := newGH4964Runner(backend)

	var logBuf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&logBuf, nil)) // default level: Info — Debug entries must not appear

	task := newGH4964Task("GH-5342", branch, dir)
	task.SkipQualityGates = true

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	if _, err := runner.Execute(ctx, task); err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if logged := logBuf.String(); strings.Contains(logged, "context deadline exceeded") {
		t.Errorf("daemon log must not surface \"context deadline exceeded\" from the finalization-phase git/gate calls, got:\n%s", logged)
	}
}

// ctxRespectingBackend is a scriptable Backend for exercising the GH-5346
// timeout-salvage path. Unlike mockGH4964Backend (which ignores the ctx it's
// handed entirely and always returns a nil error), a real backend — a
// subprocess wrapper watching the ctx it was given — returns a non-nil
// error once that ctx is done. run is invoked once per Execute() call
// (1-indexed) and is handed the real ctx so it can block on it exactly like
// a live subprocess wrapper would.
type ctxRespectingBackend struct {
	mu    sync.Mutex
	count int
	run   func(ctx context.Context, call int, opts ExecuteOptions) (*BackendResult, error)
}

func (b *ctxRespectingBackend) Name() string      { return "mock-ctx-respecting" }
func (b *ctxRespectingBackend) IsAvailable() bool { return true }

func (b *ctxRespectingBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.count++
	call := b.count
	b.mu.Unlock()
	return b.run(ctx, call, opts)
}

func (b *ctxRespectingBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

// stubQualityChecker is a scriptable QualityChecker so the timeout-salvage
// tests below can control the single (non-retrying) gate-check outcome
// directly, instead of depending on a real build/test command being
// detectable in the throwaway test repo.
type stubQualityChecker struct {
	outcome *QualityOutcome
}

func (s *stubQualityChecker) Check(context.Context) (*QualityOutcome, error) {
	return s.outcome, nil
}

// writeFakeGhPRCreateAndLabel extends writeFakeGhPRCreate (runner_gh4220_test.go)
// with a `gh issue edit ...` capture: the full argument line of any `issue
// edit` invocation is appended to capturedLabelEditsFile, one line per call,
// so a test can assert the pilot-needs-human hold path actually reached the
// label/comment escalation call instead of only checking ExecutionResult.
func writeFakeGhPRCreateAndLabel(t *testing.T, fakeBin, capturedTitleFile, capturedLabelEditsFile string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"pr list"*) echo "[]" ;;
  *"pr create"*)
    prev=""
    for arg in "$@"; do
      if [ "$prev" = "--title" ]; then
        printf '%%s' "$arg" > %q
      fi
      prev="$arg"
    done
    echo "https://github.com/o/r/pull/777"
    ;;
  *"issue edit"*) echo "$*" >> %q ;;
  *) echo "[]" ;;
esac
`, capturedTitleFile, capturedLabelEditsFile)
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

// setUpFakeGhPRCreateAndLabelPATH mirrors setUpFakeGhPRCreatePATH but also
// returns the file `gh issue edit` invocations get appended to.
func setUpFakeGhPRCreateAndLabelPATH(t *testing.T) (capturedTitleFile, capturedLabelEditsFile string) {
	t.Helper()
	fakeBin := t.TempDir()
	capturedTitleFile = filepath.Join(fakeBin, "captured-title.txt")
	capturedLabelEditsFile = filepath.Join(fakeBin, "captured-label-edits.txt")
	writeFakeGhPRCreateAndLabel(t, fakeBin, capturedTitleFile, capturedLabelEditsFile)
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return capturedTitleFile, capturedLabelEditsFile
}

// TestBackendTimeoutSalvage_CommitsPresent_GatesPass_CreatesPR is the
// GH-5346 regression guard for the "timeout + commits + gates pass" case:
// the backend call returns only because its ctx hit the deadline (not a
// clean success), but it left a real commit on the branch first — exactly
// what a watchdog killing Claude Code mid-task looks like. Before GH-5346,
// executeWithOptions's timedOut branch always fell through to a bare "task
// failed" return, discarding that commit and leaving the branch unpushed —
// no PR, no held branch, silent data loss. attemptBackendTimeoutSalvage must
// instead find the commit, run quality gates exactly once (never the
// re-invoking gate-retry loop — the backend has already burned its full
// budget getting here), and — since they pass — push the branch and open a
// PR.
func TestBackendTimeoutSalvage_CommitsPresent_GatesPass_CreatesPR(t *testing.T) {
	capturedTitleFile := setUpFakeGhPRCreatePATH(t)

	const branch = "pilot/GH-5346-timeout-salvage-pass"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &ctxRespectingBackend{
		run: func(ctx context.Context, _ int, _ ExecuteOptions) (*BackendResult, error) {
			// Simulate Claude Code landing a real commit before the
			// watchdog/task-deadline kills it: commit first, then block
			// until ctx is actually done and return its error — exactly
			// like a ctx-respecting subprocess wrapper would.
			writeUncommittedFile(t, dir, "salvaged.go")
			runGit(t, dir, "add", "salvaged.go")
			runGit(t, dir, "commit", "-m", "real work before the deadline hit")
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	runner := newGH4964Runner(backend)
	runner.qualityCheckerFactory = func(string, string) QualityChecker {
		return &stubQualityChecker{outcome: &QualityOutcome{Passed: true}}
	}

	task := newGH4964Task("GH-5346", branch, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once (timeout salvage must never re-invoke Claude Code), got %d", backend.callCount())
	}
	if !result.Success {
		t.Fatalf("expected Success=true, got false (error=%q)", result.Error)
	}
	if result.Outcome == "no_op" {
		t.Errorf("committed work must never be recorded as no_op after a backend timeout, got Outcome=%q error=%q", result.Outcome, result.Error)
	}
	if result.PRUrl == "" {
		t.Error("expected a PR URL, got none")
	}
	if _, statErr := os.Stat(capturedTitleFile); statErr != nil {
		t.Errorf("gh pr create was never invoked: %v", statErr)
	}

	remoteBranches := gitOutput(t, dir, "ls-remote", "origin", branch)
	if strings.TrimSpace(remoteBranches) == "" {
		t.Errorf("expected branch %q to be pushed to origin, ls-remote returned nothing", branch)
	}
}

// TestBackendTimeoutSalvage_CommitsPresent_GatesFail_HoldsBranchNoReinvocation
// is the GH-5346 regression guard for the "timeout + commits + gates fail"
// case: same ctx-timeout-with-a-real-commit shape as the gates-pass test
// above, but this time the single post-timeout gate check fails. GH-5346
// step 2 forbids re-invoking Claude Code once the backend has already
// consumed its full budget on this timeout — there is no more time to burn
// on a fix-it retry — so the gate failure must route straight to
// holdPushedBranch: push the branch (so the work is not stranded only in
// the about-to-be-cleaned-up worktree) and park the source issue under
// pilot-needs-human, never silently drop back to a bare "task failed" with
// an unpushed branch.
func TestBackendTimeoutSalvage_CommitsPresent_GatesFail_HoldsBranchNoReinvocation(t *testing.T) {
	capturedTitleFile, capturedLabelEditsFile := setUpFakeGhPRCreateAndLabelPATH(t)

	const branch = "pilot/GH-5346-timeout-salvage-fail"
	dir, _ := setupFreshnessRepo(t)
	runGit(t, dir, "checkout", "-b", branch)

	backend := &ctxRespectingBackend{
		run: func(ctx context.Context, _ int, _ ExecuteOptions) (*BackendResult, error) {
			writeUncommittedFile(t, dir, "salvaged.go")
			runGit(t, dir, "add", "salvaged.go")
			runGit(t, dir, "commit", "-m", "real work before the deadline hit")
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	runner := newGH4964Runner(backend)
	runner.qualityCheckerFactory = func(string, string) QualityChecker {
		return &stubQualityChecker{outcome: &QualityOutcome{Passed: false}}
	}

	task := newGH4964Task("GH-5346", branch, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if backend.callCount() != 1 {
		t.Errorf("expected backend called exactly once (a timeout-with-failing-gates salvage must never re-invoke Claude Code), got %d", backend.callCount())
	}
	if result.Success {
		t.Errorf("expected Success=false when post-timeout quality gates fail, got true")
	}
	if result.Outcome != "needs_human" {
		t.Errorf("expected Outcome=%q, got %q (error=%q)", "needs_human", result.Outcome, result.Error)
	}
	if result.PRUrl != "" {
		t.Errorf("expected no PR URL (gates failed), got %q", result.PRUrl)
	}
	if _, statErr := os.Stat(capturedTitleFile); statErr == nil {
		t.Error("gh pr create must not be invoked when post-timeout quality gates fail")
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
