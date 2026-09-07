package executor

import (
	"testing"
	"time"
)

// TestEscalateStalledTask_AlreadyStalledRepeatPickup_DoesNotRestampCompletedAt
// is GH-5353's regression coverage for the race identified in post-merge
// review of PR #5343 (GH-5272): escalateStalledTask used to call
// UpdateExecutionStatus unconditionally, including on the alreadyStalled
// repeat path, and UpdateExecutionStatus (internal/memory/store.go) stamps
// completed_at = CURRENT_TIMESTAMP on every terminal write. The GH-5212/
// GH-5272 re-arm sweep (cmd/pilot/rearm_stalled.go's tryRearmStalled)
// compares GitHub label events against exactly that completed_at to decide
// whether a re-arm gesture postdates the stall.
//
// Interleaving scenario this reproduces:
//  1. escalateStalledTask first stalls the claim at T0 (completed_at = T0).
//  2. An operator applies the re-arm recipe, producing a labeled event at
//     T1 > T0.
//  3. The SDK poller (unsynchronized, same ~30s cadence as the sweep) picks
//     the issue up again first and re-enters escalateStalledTask on the same
//     already-stalled claim at T2 > T1 — status/reason unchanged.
//
// Before the fix, step 3's unconditional write re-stamped completed_at to
// (approximately) T2, which is >= T1, so the sweep's "event.CreatedAt >
// completed_at" check permanently failed and the re-arm evidence could never
// win the race. After the fix, step 3 performs no write at all on the
// alreadyStalled path, so completed_at stays pinned at T0 and T1 remains
// newer than it indefinitely — the sweep can still re-arm no matter how many
// times the poller re-observes the stalled claim in between.
func TestEscalateStalledTask_AlreadyStalledRepeatPickup_DoesNotRestampCompletedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-5353-race", ProjectPath: "/project-gh5353-race"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	// T0: the genuine, fresh escalation — the row stalls and completed_at is
	// stamped for the first time.
	dispatcher.stallTaskAfterRepickHardCap(task, 0, dispatcherRepickHardCap)

	exec0, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution after first escalation: %v", err)
	}
	if exec0.Status != string(ExecStatusStalled) {
		t.Fatalf("expected status=stalled after first escalation, got %q", exec0.Status)
	}
	if exec0.CompletedAt == nil {
		t.Fatal("expected completed_at to be set after first escalation")
	}

	// Backdate completed_at well into the past (mirrors seedStalledRow in
	// cmd/pilot) so a repeat escalation that wrongly re-stamps it to
	// CURRENT_TIMESTAMP is observably later, rather than comparing equal
	// within the same wall-clock second a same-second test run would
	// otherwise produce — CURRENT_TIMESTAMP has only second precision, so two
	// calls made back-to-back in-process can land on the identical second and
	// mask a re-stamp entirely.
	t0 := exec0.CompletedAt.Add(-2 * time.Hour).UTC()
	if _, err := store.DB().Exec(`UPDATE executions SET completed_at = ? WHERE id = ?`, t0, execID); err != nil {
		t.Fatalf("failed to backdate completed_at: %v", err)
	}

	// T1: the operator's re-arm label event lands strictly after T0 — modeled
	// here as a timestamp value (no GitHub call from this package), since
	// what this test proves is that T1 remains newer than completed_at no
	// matter what happens at T2 below.
	t1 := t0.Add(30 * time.Minute)

	// T2 > T1: the SDK poller re-observes the same already-stalled claim
	// (identical dropCount/cap => identical reason string => alreadyStalled)
	// and re-enters escalateStalledTask via the same call site a real
	// dispatch retry would use.
	dispatcher.stallTaskAfterRepickHardCap(task, 0, dispatcherRepickHardCap)

	exec1, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution after repeat (alreadyStalled) escalation: %v", err)
	}
	if exec1.CompletedAt == nil {
		t.Fatal("expected completed_at to remain set after the repeat escalation")
	}
	if !exec1.CompletedAt.Equal(t0) {
		t.Fatalf("expected completed_at to stay pinned at the first stall (%s), got %s — the alreadyStalled path re-stamped it, reproducing the GH-5353 race",
			t0.Format(time.RFC3339Nano), exec1.CompletedAt.Format(time.RFC3339Nano))
	}

	// The core invariant the sweep depends on: the re-arm evidence at T1 must
	// remain newer than the stall stamp, indefinitely, regardless of how many
	// times the poller re-observes the already-stalled claim in between.
	if !t1.After(*exec1.CompletedAt) {
		t.Fatalf("expected re-arm evidence at %s to remain after completed_at %s",
			t1.Format(time.RFC3339Nano), exec1.CompletedAt.Format(time.RFC3339Nano))
	}
}
