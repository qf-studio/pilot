package executor

import (
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestEscalateStalledTask_IdempotenceKeyStability_GH4823 is TASK-459 Phase 4
// task 4c's regression coverage for escalateStalledTask's idempotence key
// (dispatcher.go's escalateStalledTask: `claimedExec.Error == reason`).
//
// Investigation: dropCount/cap values baked into the reason string by
// stallTaskAfterRepickHardCap/stallTaskAfterStallCap/stallTaskAfterInfraCap
// are stable-by-invariant across repeat calls (once a task is escalated, no
// further repicks happen, so dropCount never changes on a later poll tick
// that re-observes the same stalled execution). That leaves exactly one way
// this exact-string-equality guard can break: a future edit to one of the
// fmt.Sprintf prose templates in dispatcher.go. This test exists so that
// class of change fails loudly here instead of silently degrading into
// duplicate stall writes/alerts in production.
//
// Only TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent (the
// repick-hard-cap path, exercised through the full beginWithGenerationRetry
// stack) covered this before GH-4823. This file exercises escalateStalledTask
// directly — cheaper to set up and covers all five reason-string shapes
// (hard cap, stall cap, infra cap, deterministic failure, identical failure
// streak), not just the hard-cap one.
func TestEscalateStalledTask_IdempotenceKeyStability_GH4823(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(d *Dispatcher, task *Task)
	}{
		{
			name: "repick hard cap",
			invoke: func(d *Dispatcher, task *Task) {
				d.stallTaskAfterRepickHardCap(task, 0, dispatcherRepickHardCap)
			},
		},
		{
			name: "stall repick cap",
			invoke: func(d *Dispatcher, task *Task) {
				d.stallTaskAfterStallCap(task, 0, dispatcherStallRepickCap)
			},
		},
		{
			name: "infra repick cap",
			invoke: func(d *Dispatcher, task *Task) {
				d.stallTaskAfterInfraCap(task, 0, dispatcherInfraRepickCap)
			},
		},
		{
			name: "deterministic failure",
			invoke: func(d *Dispatcher, task *Task) {
				d.escalateDeterministicFailure(task, 0, "exit status 1: permission denied")
			},
		},
		{
			name: "identical failure streak",
			invoke: func(d *Dispatcher, task *Task) {
				d.escalateIdenticalFailureStreak(task, 0, "exit status 1: permission denied")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+": repeat call with unchanged inputs is idempotent (no duplicate stall/alert)", func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			task := &Task{ID: "GH-4823-" + tt.name, ProjectPath: "/project-gh4823-" + tt.name}
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

			// Same call twice, identical inputs both times — mirrors a task
			// sitting past its cap being re-observed on a later poll tick.
			tt.invoke(dispatcher, task)
			tt.invoke(dispatcher, task)

			if len(processor.events) != 1 {
				t.Errorf("expected exactly 1 alert across both calls, got %d: %+v", len(processor.events), processor.events)
			}

			events, err := store.ListExecutionEvents(execID)
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			stalledEvents := 0
			for _, e := range events {
				if e.Stage == memory.StageStalled {
					stalledEvents++
				}
			}
			if stalledEvents != 1 {
				t.Errorf("expected exactly 1 stalled execution event across both calls, got %d: %+v", stalledEvents, events)
			}
		})
	}
}

// TestEscalateStalledTask_ProseOnlyChangeBreaksIdempotence_GH4823 documents,
// deliberately, the exact fragility named by TASK-459 Phase 4 task 4c:
// escalateStalledTask's idempotence guard is exact string equality against
// the human-facing reason, so a repeat call carrying the identical dropCount
// but different prose (e.g. a future wording tweak to one of dispatcher.go's
// stallTaskAfter* templates) is NOT recognized as the same escalation and
// produces a second stall write and a second alert.
//
// This is a known, accepted limitation rather than a fix: hardening it to a
// prose-independent typed key would require a new persisted column on
// executions (Error is a single free-text field) purely to carry a machine
// tag no consumer other than this idempotence check needs — out of
// proportion for the actual risk, since the dynamic parts of every reason
// string (dropCount/cap) are stable by construction and only a deliberate,
// reviewed prose edit can trigger this. If that ever happens, this test
// fails and makes the blast radius (one duplicate alert, not a double
// destructive action) explicit at review time instead of silently in
// production.
func TestEscalateStalledTask_ProseOnlyChangeBreaksIdempotence_GH4823(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4823-prose-drift", ProjectPath: "/project-gh4823-prose-drift"}
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

	dispatcher.escalateStalledTask(task, 0, dispatcherRepickHardCap,
		"repick backoff hard cap reached: 5 consecutive failed re-picks (cap=5) — stopping automatic retries, manual re-arm required",
		map[string]string{"reason": "repick_hard_cap_stalled"})

	// Same dropCount/cap baked in, prose reworded — simulates an unrelated
	// future edit to the message template with no code-level protocol
	// change intended.
	dispatcher.escalateStalledTask(task, 0, dispatcherRepickHardCap,
		"repick backoff hard cap reached: 5 consecutive failed re-picks (cap=5). Stopping automatic retries; a human must manually re-arm this task.",
		map[string]string{"reason": "repick_hard_cap_stalled"})

	if len(processor.events) != 2 {
		t.Fatalf("expected 2 alerts (reworded reason not recognized as idempotent — known limitation), got %d: %+v", len(processor.events), processor.events)
	}
}
