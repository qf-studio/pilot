package memory

import (
	"testing"
	"time"
)

// setExecutionTimes seeds an execution row's created_at/started_at via
// SetExecutionTimesForTest, bypassing UpdateExecutionStatus's CURRENT_TIMESTAMP
// (SQLite's CURRENT_TIMESTAMP is second-resolution, too coarse to produce
// reliably distinguishable/positive deltas in a fast test — same rationale as
// TestGetStaleRunningExecutions_UsesStartedAtNotCreatedAt in store_test.go).
func setExecutionTimes(t *testing.T, store *Store, id string, createdAt, startedAt time.Time) {
	t.Helper()
	if err := store.SetExecutionTimesForTest(id, createdAt, startedAt); err != nil {
		t.Fatalf("SetExecutionTimesForTest(%s): %v", id, err)
	}
}

// TestGetLifetimeQueueWaitDurations covers the started_at-minus-created_at
// derivation straight off the executions table, exclusion of rows that never
// reached "running" (started_at still NULL), and chronological ordering
// (GH-4212).
func TestGetLifetimeQueueWaitDurations(t *testing.T) {
	t.Run("execution with started_at yields one positive sample", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/p", Status: "running"}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		now := time.Now()
		setExecutionTimes(t, store, "exec-1", now.Add(-6*time.Minute), now)

		samples, err := store.GetLifetimeQueueWaitDurations()
		if err != nil {
			t.Fatalf("GetLifetimeQueueWaitDurations failed: %v", err)
		}
		if len(samples) != 1 {
			t.Fatalf("got %d samples, want 1", len(samples))
		}
		if got := samples[0]; got < 5*time.Minute || got > 7*time.Minute {
			t.Errorf("sample duration = %v, want ~6m", got)
		}
	})

	t.Run("execution never started (started_at NULL) is excluded", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/p", Status: "queued"}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}

		samples, err := store.GetLifetimeQueueWaitDurations()
		if err != nil {
			t.Fatalf("GetLifetimeQueueWaitDurations failed: %v", err)
		}
		if len(samples) != 0 {
			t.Errorf("got %d samples, want 0", len(samples))
		}
	})

	t.Run("multiple started executions are ordered ascending by started_at", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		now := time.Now()
		starts := map[string]time.Time{
			"exec-1": now.Add(-30 * time.Minute),
			"exec-2": now.Add(-20 * time.Minute),
			"exec-3": now.Add(-10 * time.Minute),
		}
		for _, id := range []string{"exec-1", "exec-2", "exec-3"} {
			if err := store.SaveExecution(&Execution{ID: id, TaskID: "GH-" + id, ProjectPath: "/p", Status: "running"}); err != nil {
				t.Fatalf("SaveExecution(%s): %v", id, err)
			}
			setExecutionTimes(t, store, id, starts[id].Add(-1*time.Minute), starts[id])
		}

		samples, err := store.GetLifetimeQueueWaitDurations()
		if err != nil {
			t.Fatalf("GetLifetimeQueueWaitDurations failed: %v", err)
		}
		if len(samples) != 3 {
			t.Fatalf("got %d samples, want 3", len(samples))
		}
		for i, d := range samples {
			if d <= 0 {
				t.Errorf("sample[%d] = %v, want > 0", i, d)
			}
		}
	})
}

// TestGetLifetimeTimeToPRDurations covers the started_at->pr_created delta
// derivation (joining executions.started_at against the execution_events
// ledger) and exclusion of executions with no pr_created event (GH-4212).
func TestGetLifetimeTimeToPRDurations(t *testing.T) {
	t.Run("execution with started_at and pr_created yields one positive sample", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/p", Status: "running"}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if err := store.UpdateExecutionStatus("exec-1", "running"); err != nil {
			t.Fatalf("UpdateExecutionStatus(running): %v", err)
		}
		time.Sleep(5 * time.Millisecond)
		if err := store.InsertExecutionEvent("exec-1", StagePRCreated, ""); err != nil {
			t.Fatalf("InsertExecutionEvent(pr_created): %v", err)
		}

		samples, err := store.GetLifetimeTimeToPRDurations()
		if err != nil {
			t.Fatalf("GetLifetimeTimeToPRDurations failed: %v", err)
		}
		if len(samples) != 1 {
			t.Fatalf("got %d samples, want 1", len(samples))
		}
		if samples[0] <= 0 {
			t.Errorf("sample duration = %v, want > 0", samples[0])
		}
	})

	t.Run("execution never started is excluded even with a pr_created event", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		seedExecutionWithEvents(t, store, "exec-1", "GH-1", []Stage{StagePRCreated})

		samples, err := store.GetLifetimeTimeToPRDurations()
		if err != nil {
			t.Fatalf("GetLifetimeTimeToPRDurations failed: %v", err)
		}
		if len(samples) != 0 {
			t.Errorf("got %d samples, want 0", len(samples))
		}
	})

	t.Run("started execution with no pr_created event is excluded", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/p", Status: "running"}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if err := store.UpdateExecutionStatus("exec-1", "running"); err != nil {
			t.Fatalf("UpdateExecutionStatus(running): %v", err)
		}

		samples, err := store.GetLifetimeTimeToPRDurations()
		if err != nil {
			t.Fatalf("GetLifetimeTimeToPRDurations failed: %v", err)
		}
		if len(samples) != 0 {
			t.Errorf("got %d samples, want 0", len(samples))
		}
	})
}

// TestGetLifetimeApprovalWaitDurations covers the awaiting_approval->merged
// delta derivation and exclusion of executions missing either stage
// (GH-4212).
func TestGetLifetimeApprovalWaitDurations(t *testing.T) {
	t.Run("execution with both stages yields one positive sample", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		seedExecutionWithEvents(t, store, "exec-1", "GH-1", []Stage{StagePRCreated, StageAwaitingApproval, StageMerged})

		samples, err := store.GetLifetimeApprovalWaitDurations()
		if err != nil {
			t.Fatalf("GetLifetimeApprovalWaitDurations failed: %v", err)
		}
		if len(samples) != 1 {
			t.Fatalf("got %d samples, want 1", len(samples))
		}
		if samples[0] <= 0 {
			t.Errorf("sample duration = %v, want > 0", samples[0])
		}
	})

	t.Run("execution missing merged stage is excluded", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		seedExecutionWithEvents(t, store, "exec-1", "GH-1", []Stage{StagePRCreated, StageAwaitingApproval})

		samples, err := store.GetLifetimeApprovalWaitDurations()
		if err != nil {
			t.Fatalf("GetLifetimeApprovalWaitDurations failed: %v", err)
		}
		if len(samples) != 0 {
			t.Errorf("got %d samples, want 0", len(samples))
		}
	})

	t.Run("execution merged without ever awaiting approval is excluded", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		seedExecutionWithEvents(t, store, "exec-1", "GH-1", []Stage{StagePRCreated, StageMerged})

		samples, err := store.GetLifetimeApprovalWaitDurations()
		if err != nil {
			t.Fatalf("GetLifetimeApprovalWaitDurations failed: %v", err)
		}
		if len(samples) != 0 {
			t.Errorf("got %d samples, want 0", len(samples))
		}
	})
}
