package executor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecuteSubIssues_RepickDoesNotBypassBackoff is the GH-4394 subtask 4
// regression test for the "sub-issue paths" entry point named in the parent
// issue's fix list ("Ensure ALL repick entry points (poller, dispatcher
// terminal-claim re-pick, sub-issue paths) consult one shared per-task
// backoff with growth on consecutive failures").
//
// Unlike the poller (cmd/pilot/handler_common.go's repickBackoff.allow gate)
// and the dispatcher's own terminal-claim re-pick (beginWithGenerationRetry,
// dispatcher.go), the epic sub-issue loop (executeSubIssuesTracked) never
// consults the repick_backoff store at all — it claims (taskID, projectPath,
// generation 0) directly via ExecutionLifecycle.Begin (epic.go's sub-issue
// loop) and never computes a generation+1 retry itself. nextRetryGeneration —
// the only generation+1 decider anywhere in this codebase — lives exclusively
// in dispatcher.go and is only ever reached through beginWithGenerationRetry
// (queueSingleTask / queueDecomposedTask, both driven off QueueTask, both
// already backoff-gated as of GH-4394 subtasks 2/3).
//
// This test proves that absence is safe rather than a gap: when an epic is
// repicked (the parent epic's own execution went terminal-but-not-done and
// the dispatcher's already-throttled beginWithGenerationRetry claims a fresh
// generation for the PARENT), CreateSubIssues/recovery re-discovers the
// still-open child sub-issue and re-enters ExecuteSubIssues with it —
// simulated here as two direct ExecuteSubIssues calls against the same
// parent/issues. The second call's Begin() at generation 0 collides with the
// first call's already-claimed, now-terminal row and loses (ErrClaimLost), so
// the backend (executeFunc) is invoked exactly once total — never a second
// time for the same failed child. There is no unthrottled repick here to
// guard with a backoff: the atomic generation-0 claim structurally prevents
// one. Only the PARENT epic's own re-dispatch (already gated by subtasks
// 1-3) grows the shared backoff; if a future change ever threads a
// generation+1 retry into this loop (letting it re-invoke the backend for an
// already-failed child), it must also consult the shared repick_backoff store
// the same way beginWithGenerationRetry does — this test's first assertion
// (exactly one backend call across two ExecuteSubIssues passes) is what would
// break to signal that.
func TestExecuteSubIssues_RepickDoesNotBypassBackoff(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	var mu sync.Mutex
	execCalls := 0
	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		execCalls++
		mu.Unlock()
		return &ExecutionResult{TaskID: task.ID, Success: false, Error: "boom: real failure, not a no-op"}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	// externallyOwned reconciliation (the second ExecuteSubIssues call below)
	// polls on this interval before its first terminal-row lookup — keep it
	// short so the test doesn't pay the 3s default poll tick.
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second

	issues := makeSubIssues(1, 9394)
	parent := &Task{ID: "GH-9300", Title: "[epic] GH-4394 subtask 4 repick test"}

	// First attempt: genuine, un-throttled first dispatch of the sub-issue —
	// claims generation 0, invokes the backend once, and fails.
	err1 := runner.ExecuteSubIssues(context.Background(), parent, issues, "", "")
	if err1 == nil {
		t.Fatal("expected the first ExecuteSubIssues call to fail (child reported failure)")
	}

	mu.Lock()
	firstCallCount := execCalls
	mu.Unlock()
	if firstCallCount != 1 {
		t.Fatalf("expected exactly 1 backend invocation after the first attempt, got %d", firstCallCount)
	}

	// Second attempt: simulates the epic being repicked (parent re-dispatched
	// via the already-throttled dispatcher path) and re-discovering the same
	// still-open child sub-issue.
	err2 := runner.ExecuteSubIssues(context.Background(), parent, issues, "", "")
	if err2 == nil {
		t.Fatal("expected the second ExecuteSubIssues call to also report the child as failed")
	}
	if !strings.Contains(err2.Error(), "boom") {
		t.Errorf("expected the second call's error to surface the original terminal failure detail, got: %v", err2)
	}

	mu.Lock()
	secondCallCount := execCalls
	mu.Unlock()
	if secondCallCount != 1 {
		t.Errorf("epic repick invoked the backend a second time for an already-terminal sub-issue (got %d total calls, want 1) — this would be an unthrottled repick storm needing backoff", secondCallCount)
	}
}
