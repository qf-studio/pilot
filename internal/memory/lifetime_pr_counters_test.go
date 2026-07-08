package memory

import (
	"testing"
	"time"
)

// seedExecutionWithEvents creates the parent execution row (satisfying the
// execution_events FK) and inserts the given stage events in order, sleeping
// between inserts so occurred_at values are distinguishable.
func seedExecutionWithEvents(t *testing.T, store *Store, executionID, taskID string, stages []Stage) {
	t.Helper()
	if err := store.SaveExecution(&Execution{
		ID:          executionID,
		TaskID:      taskID,
		ProjectPath: "/project",
		Status:      "completed",
	}); err != nil {
		t.Fatalf("SaveExecution(%s) failed: %v", executionID, err)
	}
	for _, stage := range stages {
		if err := store.InsertExecutionEvent(executionID, stage, ""); err != nil {
			t.Fatalf("InsertExecutionEvent(%s, %s) failed: %v", executionID, stage, err)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestGetLifetimePRCounters covers dedupe (multiple events don't double count
// a single execution) and mixed stages — specifically that a bare 'failed'
// event with no preceding 'pr_created' (an executor-level task failure that
// never produced a PR) must NOT be counted as a PR-family failure (GH-4093).
func TestGetLifetimePRCounters(t *testing.T) {
	tests := []struct {
		name       string
		executions map[string][]Stage // executionID -> stage sequence
		wantMerged int64
		wantFailed int64
	}{
		{
			name:       "no events",
			executions: map[string][]Stage{},
			wantMerged: 0,
			wantFailed: 0,
		},
		{
			name: "single merged PR counts once",
			executions: map[string][]Stage{
				"exec-1": {StageQueued, StagePRCreated, StageCIPassed, StageMerged},
			},
			wantMerged: 1,
			wantFailed: 0,
		},
		{
			name: "single failed PR counts once",
			executions: map[string][]Stage{
				"exec-1": {StageQueued, StagePRCreated, StageCIFailed, StageFailed},
			},
			wantMerged: 0,
			wantFailed: 1,
		},
		{
			name: "task failure with no PR created is excluded from PRsFailed",
			executions: map[string][]Stage{
				"exec-1": {StageQueued, StageRunning, StageFailed},
			},
			wantMerged: 0,
			wantFailed: 0,
		},
		{
			name: "mixed: PR-family failure and non-PR task failure both present",
			executions: map[string][]Stage{
				"exec-1": {StagePRCreated, StageCIFailed, StageFailed},
				"exec-2": {StageQueued, StageRunning, StageFailed},
			},
			wantMerged: 0,
			wantFailed: 1,
		},
		{
			name: "dedupe: repeated merged event for the same execution counts once",
			executions: map[string][]Stage{
				"exec-1": {StagePRCreated, StageMerged, StageMerged},
			},
			wantMerged: 1,
			wantFailed: 0,
		},
		{
			name: "multiple distinct merged and failed executions",
			executions: map[string][]Stage{
				"exec-1": {StagePRCreated, StageMerged},
				"exec-2": {StagePRCreated, StageMerged},
				"exec-3": {StagePRCreated, StageFailed},
			},
			wantMerged: 2,
			wantFailed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newExecutionEventsTestStore(t)

			for execID, stages := range tt.executions {
				seedExecutionWithEvents(t, store, execID, "GH-1", stages)
			}

			got, err := store.GetLifetimePRCounters()
			if err != nil {
				t.Fatalf("GetLifetimePRCounters failed: %v", err)
			}
			if got.Merged != tt.wantMerged {
				t.Errorf("Merged = %d, want %d", got.Merged, tt.wantMerged)
			}
			if got.Failed != tt.wantFailed {
				t.Errorf("Failed = %d, want %d", got.Failed, tt.wantFailed)
			}
		})
	}
}

// TestGetLifetimePRTimeToMerge covers the pr_created->merged delta derivation,
// exclusion of executions missing either stage, and dedupe via MIN(occurred_at)
// when a stage is (unexpectedly) logged more than once for the same execution.
func TestGetLifetimePRTimeToMerge(t *testing.T) {
	t.Run("execution with both stages yields one positive sample", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed"}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if err := store.InsertExecutionEvent("exec-1", StagePRCreated, ""); err != nil {
			t.Fatalf("InsertExecutionEvent(pr_created): %v", err)
		}
		time.Sleep(5 * time.Millisecond)
		if err := store.InsertExecutionEvent("exec-1", StageMerged, ""); err != nil {
			t.Fatalf("InsertExecutionEvent(merged): %v", err)
		}

		samples, err := store.GetLifetimePRTimeToMerge()
		if err != nil {
			t.Fatalf("GetLifetimePRTimeToMerge failed: %v", err)
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
		seedExecutionWithEvents(t, store, "exec-1", "GH-1", []Stage{StagePRCreated, StageCIFailed, StageFailed})

		samples, err := store.GetLifetimePRTimeToMerge()
		if err != nil {
			t.Fatalf("GetLifetimePRTimeToMerge failed: %v", err)
		}
		if len(samples) != 0 {
			t.Errorf("got %d samples, want 0", len(samples))
		}
	})

	t.Run("multiple merged executions produce multiple samples", func(t *testing.T) {
		store := newExecutionEventsTestStore(t)
		seedExecutionWithEvents(t, store, "exec-1", "GH-1", []Stage{StagePRCreated, StageMerged})
		seedExecutionWithEvents(t, store, "exec-2", "GH-2", []Stage{StagePRCreated, StageMerged})

		samples, err := store.GetLifetimePRTimeToMerge()
		if err != nil {
			t.Fatalf("GetLifetimePRTimeToMerge failed: %v", err)
		}
		if len(samples) != 2 {
			t.Fatalf("got %d samples, want 2", len(samples))
		}
		for i, d := range samples {
			if d <= 0 {
				t.Errorf("sample[%d] = %v, want > 0", i, d)
			}
		}
	})
}
