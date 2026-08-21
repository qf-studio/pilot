package executor

import (
	"context"
	"os"
	"testing"
	"time"
)

// ctxRespectingQualityChecker is the GH-5060 regression mock: unlike
// sleepThenFailQualityChecker (runner_gh4876_test.go), which unconditionally
// ignores the ctx it's given, this checker actually consumes it on the
// retry-pass re-check (its second+ call) — mirroring how a real checker
// built on exec.CommandContext behaves: if the ctx is already expired when
// the command would start, it returns ctx.Err() instead of running.
//
// The first call always sleeps past the (short) outer attempt ctx deadline
// and fails with ShouldRetry, exactly like sleepThenFailQualityChecker, so
// this checker can stand in for it in the "outer ctx expires before the
// retry" scenario while remaining able to detect a stale ctx on the
// re-check.
type ctxRespectingQualityChecker struct {
	sleep time.Duration
	calls int
}

func (c *ctxRespectingQualityChecker) Check(ctx context.Context) (*QualityOutcome, error) {
	c.calls++
	if c.calls == 1 {
		time.Sleep(c.sleep)
		return &QualityOutcome{
			Passed:        false,
			ShouldRetry:   true,
			RetryFeedback: "synthetic gate failure",
			Attempt:       c.calls,
		}, nil
	}
	// Retry-pass re-check: consume the ctx, exactly as a real
	// exec.CommandContext-backed checker would.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &QualityOutcome{Passed: true, Attempt: c.calls}, nil
}

// TestQualityGateRetry_RecheckUsesFreshContext is the GH-5060 regression
// guard. PR#5058 (GH-4876) gave the pre-retry reset (runner.go ~4523) and
// the retry backend re-invoke (runner.go ~4593) fresh
// context.Background()-derived deadlines, but the retry-pass gate re-check
// itself — checker.Check(ctx) — was left running on the original (possibly
// exhausted) attempt ctx. A real, ctx-respecting checker whose ctx has
// already expired by the time the re-check runs dies immediately with
// "context deadline exceeded", producing a false task_failed right after a
// successful fresh-ctx retry — and the doomed-retry shape is now *more*
// expensive than before #5058, since the re-invoke burns a full backend run
// first.
//
// sleepThenFailQualityChecker (used by
// TestQualityGateRetry_UsesFreshContextForResetAndReinvoke) cannot catch
// this: it ignores its ctx unconditionally, so it structurally cannot tell
// a fresh ctx from a stale one. ctxRespectingQualityChecker fixes that by
// consuming ctx.Err() on its retry-pass call.
//
// The outer ctx is given a short deadline. The checker's first call
// (running on the still-live outer ctx) sleeps past that deadline and
// always fails with ShouldRetry, forcing a retry. By the time the reset +
// backend re-invoke (both on their own fresh contexts, per #5058) complete,
// the outer attempt ctx has expired. Pre-fix, the retry-pass re-check reuses
// that expired ctx and fails with "context deadline exceeded"; post-fix, it
// runs on a fresh ctx and observes the checker's second call pass.
func TestQualityGateRetry_RecheckUsesFreshContext(t *testing.T) {
	localRepo, remoteRepo := setupTestRepoWithRemote(t)
	defer func() { _ = os.RemoveAll(localRepo) }()
	defer func() { _ = os.RemoveAll(remoteRepo) }()

	backend := &stackingAttemptBackend{}
	runner := NewRunnerWithBackend(backend)
	runner.config = &BackendConfig{UseWorktree: false, SkipSelfReview: true}
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	checker := &ctxRespectingQualityChecker{sleep: 1200 * time.Millisecond}
	runner.SetQualityCheckerFactory(func(taskID, projectPath string) QualityChecker {
		return checker
	})

	task := &Task{
		ID:          "GH-5060-RECHECK",
		Title:       "quality gate re-check must not reuse an exhausted attempt ctx",
		Description: "first gate check fails and retries; the attempt ctx expires before the re-check runs",
		ProjectPath: localRepo,
		Branch:      "pilot/GH-5060-recheck",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()

	result, _ := runner.Execute(ctx, task)
	if result == nil {
		t.Fatal("Execute() returned nil result")
	}
	if !result.Success {
		t.Fatalf("expected the retry to complete successfully on a fresh re-check ctx, got failure: %s", result.Error)
	}
	if checker.calls != 2 {
		t.Fatalf("quality checker called %d times, want 2 (fail then pass)", checker.calls)
	}
	if calls := backend.callCount(); calls != 2 {
		t.Fatalf("expected exactly 2 backend calls (initial + one retry), got %d", calls)
	}
}
