package memory

import (
	"os"
	"testing"
	"time"
)

// newExecutionEventsTestStore creates a real-file-backed Store (foreign_keys=ON,
// matching production) for execution_events tests.
func newExecutionEventsTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-test-events-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestInsertAndListExecutionEvents covers insert/list, chronological ordering,
// and the unknown-execution-id → empty-result case.
func TestInsertAndListExecutionEvents(t *testing.T) {
	tests := []struct {
		name       string
		seedEvents []struct {
			stage  Stage
			detail string
		}
		queryID     string
		wantStages  []Stage
		wantDetails []string
	}{
		{
			name:    "unknown execution id returns empty result",
			queryID: "exec-does-not-exist",
		},
		{
			name: "single event round-trips",
			seedEvents: []struct {
				stage  Stage
				detail string
			}{
				{StageQueued, "picked up from queue"},
			},
			queryID:     "exec-1",
			wantStages:  []Stage{StageQueued},
			wantDetails: []string{"picked up from queue"},
		},
		{
			name: "multiple events ordered by occurred_at ascending",
			seedEvents: []struct {
				stage  Stage
				detail string
			}{
				{StageQueued, "queued"},
				{StageRunning, "started"},
				{StagePRCreated, "opened PR #1"},
				{StageMerged, "merged"},
			},
			queryID:     "exec-1",
			wantStages:  []Stage{StageQueued, StageRunning, StagePRCreated, StageMerged},
			wantDetails: []string{"queued", "started", "opened PR #1", "merged"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newExecutionEventsTestStore(t)

			// Seed the parent execution row so INSERTs satisfy the FK, unless this
			// case is specifically testing an execution id that was never created.
			if len(tt.seedEvents) > 0 {
				if err := store.SaveExecution(&Execution{
					ID:          "exec-1",
					TaskID:      "GH-1",
					ProjectPath: "/project",
					Status:      "running",
				}); err != nil {
					t.Fatalf("SaveExecution failed: %v", err)
				}
			}

			for _, seed := range tt.seedEvents {
				if err := store.InsertExecutionEvent("exec-1", seed.stage, seed.detail); err != nil {
					t.Fatalf("InsertExecutionEvent(%s) failed: %v", seed.stage, err)
				}
				// Ensure distinct occurred_at values so ASC ordering is meaningful
				// even on filesystems/clocks with coarse resolution.
				time.Sleep(time.Millisecond)
			}

			events, err := store.ListExecutionEvents(tt.queryID)
			if err != nil {
				t.Fatalf("ListExecutionEvents failed: %v", err)
			}

			if len(events) != len(tt.wantStages) {
				t.Fatalf("got %d events, want %d", len(events), len(tt.wantStages))
			}
			var prevOccurredAt time.Time
			for i, e := range events {
				if e.Stage != tt.wantStages[i] {
					t.Errorf("event[%d].Stage = %q, want %q", i, e.Stage, tt.wantStages[i])
				}
				if e.Detail != tt.wantDetails[i] {
					t.Errorf("event[%d].Detail = %q, want %q", i, e.Detail, tt.wantDetails[i])
				}
				if e.ExecutionID != "exec-1" {
					t.Errorf("event[%d].ExecutionID = %q, want exec-1", i, e.ExecutionID)
				}
				if i > 0 && e.OccurredAt.Before(prevOccurredAt) {
					t.Errorf("event[%d].OccurredAt = %v is before previous event's %v; want ascending order", i, e.OccurredAt, prevOccurredAt)
				}
				prevOccurredAt = e.OccurredAt
			}
		})
	}
}

// TestInsertExecutionEventUsesExplicitUTC verifies occurred_at is written with
// an explicit UTC clock value rather than relying on SQLite's local-time default.
func TestInsertExecutionEventUsesExplicitUTC(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{
		ID:          "exec-1",
		TaskID:      "GH-1",
		ProjectPath: "/project",
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	before := time.Now().UTC()
	if err := store.InsertExecutionEvent("exec-1", StageQueued, ""); err != nil {
		t.Fatalf("InsertExecutionEvent failed: %v", err)
	}
	after := time.Now().UTC()

	events, err := store.ListExecutionEvents("exec-1")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}

	got := events[0].OccurredAt
	if got.Before(before) || got.After(after) {
		t.Errorf("OccurredAt = %v, want between %v and %v", got, before, after)
	}
}

// TestListExecutionsForTask covers newest-first ordering and an unknown task id.
func TestListExecutionsForTask(t *testing.T) {
	tests := []struct {
		name      string
		seedIDs   []string // in insertion order
		queryTask string
		wantIDs   []string // expected order returned
	}{
		{
			name:      "unknown task id returns empty result",
			queryTask: "GH-does-not-exist",
		},
		{
			name:      "multiple executions returned newest first",
			seedIDs:   []string{"exec-1", "exec-2", "exec-3"},
			queryTask: "GH-1",
			wantIDs:   []string{"exec-3", "exec-2", "exec-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newExecutionEventsTestStore(t)

			for _, id := range tt.seedIDs {
				if err := store.SaveExecution(&Execution{
					ID:          id,
					TaskID:      "GH-1",
					ProjectPath: "/project",
					Status:      "queued",
				}); err != nil {
					t.Fatalf("SaveExecution(%s) failed: %v", id, err)
				}
				// created_at defaults to CURRENT_TIMESTAMP; sleep so insertion order
				// is distinguishable by time, not just rowid.
				time.Sleep(time.Millisecond)
			}

			got, err := store.ListExecutionsForTask(tt.queryTask)
			if err != nil {
				t.Fatalf("ListExecutionsForTask failed: %v", err)
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d executions, want %d", len(got), len(tt.wantIDs))
			}
			for i, exec := range got {
				if exec.ID != tt.wantIDs[i] {
					t.Errorf("execution[%d].ID = %q, want %q", i, exec.ID, tt.wantIDs[i])
				}
			}
		})
	}
}
