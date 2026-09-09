package executor

import (
	"testing"
)

// TestRunner_ExecCancelStack_NestedSameTaskID is the GH-5409 regression test
// for the nested-executeWithOptions bug (see the execCancel field doc and
// registerExecCancel/unregisterExecCancel/Cancel/IsRunning): a decomposed
// subtask can invoke executeWithOptions again with the SAME task.ID as its
// still-running outer call. Before this fix, execCancel was a plain
// map[string]context.CancelFunc, so the inner call's defer'd
// unregisterExecCancel deleted the map entry outright when it returned —
// making IsRunning report false (and Cancel a no-op) for the remainder of
// the outer call, even though real work was still in flight. The fix makes
// execCancel a per-task LIFO stack: only the innermost entry is popped on
// unregister, so the outer call's entry survives until it, in turn, returns.
func TestRunner_ExecCancelStack_NestedSameTaskID(t *testing.T) {
	r := newSilentRunnerTask359()
	const taskID = "GH-100"

	var outerCanceled, innerCanceled bool
	outerCancel := func() { outerCanceled = true }
	innerCancel := func() { innerCanceled = true }

	if r.IsRunning(taskID) {
		t.Fatal("expected IsRunning false before any registration")
	}

	// Outer executeWithOptions call registers its cancel func.
	r.registerExecCancel(taskID, outerCancel)
	if !r.IsRunning(taskID) {
		t.Fatal("expected IsRunning true after outer register")
	}

	// Nested inner executeWithOptions call, same task ID, registers its own
	// cancel func on top of the outer's.
	r.registerExecCancel(taskID, innerCancel)
	if !r.IsRunning(taskID) {
		t.Fatal("expected IsRunning true while both outer and inner are registered")
	}

	// Cancel while both are registered must hit the innermost (still-active)
	// entry, not the outer one.
	if err := r.Cancel(taskID); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if !innerCanceled {
		t.Error("expected Cancel to invoke the innermost registered cancel func")
	}
	if outerCanceled {
		t.Error("expected Cancel NOT to invoke the outer cancel func while an inner call is still registered")
	}

	// Inner call returns; its deferred unregister pops only its own entry.
	r.unregisterExecCancel(taskID)
	if !r.IsRunning(taskID) {
		t.Fatal("expected IsRunning still true after the inner call unregisters — the outer entry must survive")
	}

	// Outer call returns; its deferred unregister pops the last entry.
	r.unregisterExecCancel(taskID)
	if r.IsRunning(taskID) {
		t.Error("expected IsRunning false after the outer call unregisters")
	}
}

// TestRunner_CancelDeclined_RecordsReasonForExecuteWithOptions verifies the
// GH-5409 CancelDeclined/takeDeclinedCancelReason contract that
// executeWithOptions's error-handling path relies on: CancelDeclined must (1)
// record the reason before invoking the underlying context cancel so the
// error path can find it once ctx.Err() fires, (2) actually cancel the
// registered context, and (3) takeDeclinedCancelReason must return it exactly
// once (consumed), so a later unrelated run of the same task ID never
// inherits a stale reason.
func TestRunner_CancelDeclined_RecordsReasonForExecuteWithOptions(t *testing.T) {
	r := newSilentRunnerTask359()
	const taskID = "GH-101"
	const reason = "spec-guard: scope changed after dispatch"

	var canceled bool
	r.registerExecCancel(taskID, func() { canceled = true })

	if err := r.CancelDeclined(taskID, reason); err != nil {
		t.Fatalf("CancelDeclined returned error: %v", err)
	}
	if !canceled {
		t.Fatal("expected CancelDeclined to invoke the registered cancel func")
	}

	got, ok := r.takeDeclinedCancelReason(taskID)
	if !ok {
		t.Fatal("expected takeDeclinedCancelReason to report a recorded reason")
	}
	if got != reason {
		t.Errorf("takeDeclinedCancelReason reason = %q, want %q", got, reason)
	}

	// Second read must find nothing — the reason is consumed, not sticky.
	if _, ok := r.takeDeclinedCancelReason(taskID); ok {
		t.Error("expected takeDeclinedCancelReason to be empty after being consumed once")
	}

	r.unregisterExecCancel(taskID)
}

// TestRunner_CancelDeclined_NotRunningLeavesNoStaleReason covers the
// not-running branch of CancelDeclined: if the task has already finished (or
// never started), CancelDeclined must return an error AND must not leave a
// dangling declinedCancel entry that a later, unrelated dispatch of the same
// task ID could pick up.
func TestRunner_CancelDeclined_NotRunningLeavesNoStaleReason(t *testing.T) {
	r := newSilentRunnerTask359()
	const taskID = "GH-102"

	if err := r.CancelDeclined(taskID, "some reason"); err == nil {
		t.Fatal("expected CancelDeclined to error when the task is not running")
	}
	if _, ok := r.takeDeclinedCancelReason(taskID); ok {
		t.Error("expected no declinedCancel entry to survive a failed CancelDeclined call")
	}
}

// TestRunner_FinishDeclined_TerminalStatusIsDeclinedNotFailed exercises the
// finishDeclined path (shared by the new GH-5409 CancelDeclined branch in
// executeWithOptions) end-to-end against TerminalStatus: a mid-execution
// decline must classify as "declined", never "failed" — so pilot-failed never
// stacks on top of pilot-needs-clarification (issue GH-5409 gap #3).
func TestRunner_FinishDeclined_TerminalStatusIsDeclinedNotFailed(t *testing.T) {
	r := newSilentRunnerTask359()
	task := &Task{ID: "GH-103"}
	result := &ExecutionResult{TaskID: task.ID, Success: true}

	r.finishDeclined(task, result, nil, nil, &progressState{}, r.log, "spec-guard: scope changed after dispatch")

	if !result.Declined {
		t.Error("expected result.Declined = true")
	}
	if result.Success {
		t.Error("expected result.Success = false")
	}
	if got := TerminalStatus(result); got != "declined" {
		t.Errorf("TerminalStatus() = %q, want %q", got, "declined")
	}
}

// TestRunner_UnregisterExecCancel_ClearsStaleDeclinedReason is the GH-5415
// regression test: CancelDeclined stashes a decline reason for the failure
// path to consume via takeDeclinedCancelReason, but that consumption only
// happens on the err != nil branch. If the backend races the cancel and
// returns success (subprocess finished cleanly just before the cancel
// landed, or the backend swallows the ctx cancellation), the reason is never
// taken by that execution. Before this fix, it survived in the runner's
// declinedCancel map and the NEXT execution of the same task ID — even one
// that fails for a completely ordinary reason — would be misclassified as
// "declined" instead of "failed". unregisterExecCancel must clear the entry
// unconditionally so no reason outlives the execution it was raised against.
func TestRunner_UnregisterExecCancel_ClearsStaleDeclinedReason(t *testing.T) {
	r := newSilentRunnerTask359()
	const taskID = "GH-104"

	// First execution: CancelDeclined is invoked (races a backend that is
	// about to finish cleanly), but the mock backend "returns success" —
	// i.e. the error path (and its takeDeclinedCancelReason call) never
	// runs, so the reason is left sitting in the map.
	r.registerExecCancel(taskID, func() {})
	if err := r.CancelDeclined(taskID, "spec-guard: scope changed after dispatch"); err != nil {
		t.Fatalf("CancelDeclined returned error: %v", err)
	}
	// Simulate the backend returning err == nil: the success path does not
	// call takeDeclinedCancelReason. executeWithOptions's defer still runs.
	r.unregisterExecCancel(taskID)

	// Second, unrelated execution of the same task ID fails for an ordinary
	// reason. Its error path calls takeDeclinedCancelReason to decide
	// declined vs. failed — it must find nothing.
	r.registerExecCancel(taskID, func() {})
	if reason, declined := r.takeDeclinedCancelReason(taskID); declined {
		t.Errorf("expected no stale declinedCancel reason to survive into the next execution, got %q", reason)
	}
	r.unregisterExecCancel(taskID)
}
