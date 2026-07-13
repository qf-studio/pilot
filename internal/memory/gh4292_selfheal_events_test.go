package memory

import (
	"fmt"
	"testing"
	"time"
)

// TestHealFunctions_WriteOneTerminalEventMatchingPRURLBranch covers GH-4292:
// ResolveOrphanedRunningExecution, SelfHealExecutionAfterMerge, and
// SelfHealExecutionByPRURL must each write exactly one terminal
// execution_events row per healed row, of type StageMerged when a PR URL is
// known and StageFailed otherwise.
func TestHealFunctions_WriteOneTerminalEventMatchingPRURLBranch(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus string
		prURL         string
		wantStage     Stage
		heal          func(store *Store, execID, taskID, projectPath, prURL string) error
	}{
		{
			name:          "ResolveOrphanedRunningExecution/merged",
			initialStatus: "running",
			prURL:         "https://github.com/o/r/pull/4199",
			wantStage:     StageMerged,
			heal: func(store *Store, execID, taskID, projectPath, prURL string) error {
				return store.ResolveOrphanedRunningExecution(execID, prURL)
			},
		},
		{
			name:          "ResolveOrphanedRunningExecution/no-evidence",
			initialStatus: "running",
			prURL:         "",
			wantStage:     StageFailed,
			heal: func(store *Store, execID, taskID, projectPath, prURL string) error {
				return store.ResolveOrphanedRunningExecution(execID, prURL)
			},
		},
		{
			name:          "SelfHealExecutionAfterMerge/merged",
			initialStatus: "failed",
			prURL:         "https://github.com/o/r/pull/4217",
			wantStage:     StageMerged,
			heal: func(store *Store, execID, taskID, projectPath, prURL string) error {
				return store.SelfHealExecutionAfterMerge(taskID, projectPath, prURL)
			},
		},
		{
			name:          "SelfHealExecutionAfterMerge/no-url",
			initialStatus: "failed",
			prURL:         "",
			wantStage:     StageFailed,
			heal: func(store *Store, execID, taskID, projectPath, prURL string) error {
				return store.SelfHealExecutionAfterMerge(taskID, projectPath, prURL)
			},
		},
		{
			name:          "SelfHealExecutionByPRURL/merged",
			initialStatus: "failed",
			prURL:         "https://github.com/o/r/pull/4190",
			wantStage:     StageMerged,
			heal: func(store *Store, execID, taskID, projectPath, prURL string) error {
				return store.SelfHealExecutionByPRURL(prURL)
			},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			execID := fmt.Sprintf("exec-%d", i)
			taskID := fmt.Sprintf("GH-%d", 4300+i)
			projectPath := "/proj"

			exec := &Execution{ID: execID, TaskID: taskID, ProjectPath: projectPath, Status: tt.initialStatus}
			if tt.name == "SelfHealExecutionByPRURL/merged" {
				// SelfHealExecutionByPRURL matches on the row's own already-stamped
				// pr_url column, unlike the task_id-keyed heals above.
				exec.PRUrl = tt.prURL
			}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := tt.heal(store, execID, taskID, projectPath, tt.prURL); err != nil {
				t.Fatalf("heal call failed: %v", err)
			}

			events, err := store.ListExecutionEvents(execID)
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("expected exactly one terminal event, got %d: %+v", len(events), events)
			}
			if events[0].Stage != tt.wantStage {
				t.Errorf("event stage = %q, want %q", events[0].Stage, tt.wantStage)
			}
		})
	}
}

// TestHealFunctions_SecondCallOnHealedRowDoesNotDoubleWriteEvent verifies
// idempotency: once a row has healed to a terminal status, it drops out of
// each heal function's WHERE clause, so calling the same heal function again
// selects zero candidates and writes zero additional execution_events rows.
func TestHealFunctions_SecondCallOnHealedRowDoesNotDoubleWriteEvent(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus string
		heal          func(store *Store) error
	}{
		{
			name:          "ResolveOrphanedRunningExecution",
			initialStatus: "running",
			heal: func(store *Store) error {
				return store.ResolveOrphanedRunningExecution("exec-1", "https://github.com/o/r/pull/4182")
			},
		},
		{
			name:          "SelfHealExecutionAfterMerge",
			initialStatus: "failed",
			heal: func(store *Store) error {
				return store.SelfHealExecutionAfterMerge("GH-1", "/proj", "https://github.com/o/r/pull/4203")
			},
		},
		{
			name:          "SelfHealExecutionByPRURL",
			initialStatus: "failed",
			heal: func(store *Store) error {
				return store.SelfHealExecutionByPRURL("https://github.com/o/r/pull/4227")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			exec := &Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/proj", Status: tt.initialStatus}
			if tt.name == "SelfHealExecutionByPRURL" {
				exec.PRUrl = "https://github.com/o/r/pull/4227"
			}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if err := tt.heal(store); err != nil {
				t.Fatalf("first heal call failed: %v", err)
			}
			if err := tt.heal(store); err != nil {
				t.Fatalf("second heal call failed: %v", err)
			}

			events, err := store.ListExecutionEvents("exec-1")
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("expected exactly one terminal event after two calls, got %d: %+v", len(events), events)
			}
		})
	}
}

// TestHealFunctions_RollbackOnEventWriteFailureLeavesStatusUntouched verifies
// GH-4292's transactional requirement: the status UPDATE and the terminal
// execution_events INSERT commit together. Dropping the execution_events
// table forces the INSERT to fail after the UPDATE has already run inside the
// same transaction — if the write were not transactional, the status would
// still flip to its terminal value even though no event was recorded.
func TestHealFunctions_RollbackOnEventWriteFailureLeavesStatusUntouched(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus string
		heal          func(store *Store) error
	}{
		{
			name:          "ResolveOrphanedRunningExecution",
			initialStatus: "running",
			heal: func(store *Store) error {
				return store.ResolveOrphanedRunningExecution("exec-1", "https://github.com/o/r/pull/4234")
			},
		},
		{
			name:          "SelfHealExecutionAfterMerge",
			initialStatus: "failed",
			heal: func(store *Store) error {
				return store.SelfHealExecutionAfterMerge("GH-1", "/proj", "https://github.com/o/r/pull/4235")
			},
		},
		{
			name:          "SelfHealExecutionByPRURL",
			initialStatus: "failed",
			heal: func(store *Store) error {
				return store.SelfHealExecutionByPRURL("https://github.com/o/r/pull/4236")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			exec := &Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/proj", Status: tt.initialStatus}
			if tt.name == "SelfHealExecutionByPRURL" {
				exec.PRUrl = "https://github.com/o/r/pull/4236"
			}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			if _, err := store.DB().Exec(`DROP TABLE execution_events`); err != nil {
				t.Fatalf("drop execution_events table: %v", err)
			}

			if err := tt.heal(store); err == nil {
				t.Fatal("expected an error once execution_events is unwritable")
			}

			got, err := store.GetExecution("exec-1")
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tt.initialStatus {
				t.Errorf("status = %q, want unchanged %q (expected rollback)", got.Status, tt.initialStatus)
			}
		})
	}
}

// TestHealFunctions_BackfillsEventOnAlreadyCompletedRow is the GH-4277
// regression: SelfHealExecutionAfterMerge / SelfHealExecutionByPRURL ran
// against ~/.pilot/data/pilot.db pre-GH-4292 already flipped several rows
// (GH-4199/4217/4190/4182/4203/4227/4234/4235) to status='completed' without
// ever writing a terminal execution_events row, so their dashboard HISTORY
// label stayed frozen at whatever stage (e.g. "running", "ci_passed",
// "awaiting_approval") happened to be last logged. A row already sitting at
// "completed" with no terminal ledger entry must still get the missing event
// backfilled the next time autopilot's catch-up sweep calls these heal
// functions for it — without re-touching status/pr_url/completed_at, which
// are already correct.
func TestHealFunctions_BackfillsEventOnAlreadyCompletedRow(t *testing.T) {
	tests := []struct {
		name string
		heal func(store *Store) error
	}{
		{
			name: "SelfHealExecutionAfterMerge",
			heal: func(store *Store) error {
				return store.SelfHealExecutionAfterMerge("GH-4199", "/proj", "https://github.com/qf-studio/pilot/pull/4201")
			},
		},
		{
			name: "SelfHealExecutionByPRURL",
			heal: func(store *Store) error {
				return store.SelfHealExecutionByPRURL("https://github.com/qf-studio/pilot/pull/4201")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			completedAt := "2026-07-01T00:00:00Z"
			exec := &Execution{
				ID: "exec-1", TaskID: "GH-4199", ProjectPath: "/proj",
				Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/4201",
			}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}
			// Ledger frozen mid-flight (no terminal stage), matching the live-DB
			// evidence — the row completed but the last logged stage never advanced.
			if err := store.InsertExecutionEvent("exec-1", StageImplementationStarted, "direct path handing off to claude for implementation"); err != nil {
				t.Fatalf("seed InsertExecutionEvent: %v", err)
			}
			// Pin completed_at to a known past value so the assertion below can
			// prove the backfill doesn't re-stamp it to "now".
			if _, err := store.DB().Exec(`UPDATE executions SET completed_at = ? WHERE id = ?`, completedAt, "exec-1"); err != nil {
				t.Fatalf("pin completed_at: %v", err)
			}

			if err := tt.heal(store); err != nil {
				t.Fatalf("heal call failed: %v", err)
			}

			events, err := store.ListExecutionEvents("exec-1")
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			var merged int
			for _, e := range events {
				if e.Stage == StageMerged {
					merged++
				}
			}
			if merged != 1 {
				t.Fatalf("expected exactly one StageMerged backfill event, got %d of %d total events: %+v", merged, len(events), events)
			}

			got, err := store.GetExecution("exec-1")
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != "completed" {
				t.Errorf("status = %q, want unchanged %q", got.Status, "completed")
			}
			if got.CompletedAt == nil || !got.CompletedAt.Equal(mustParseRFC3339(t, completedAt)) {
				t.Errorf("completed_at was re-stamped by the backfill, want it left untouched at %s", completedAt)
			}

			// Re-running the same heal call must not append a second event now
			// that the ledger has a terminal entry.
			if err := tt.heal(store); err != nil {
				t.Fatalf("second heal call failed: %v", err)
			}
			events, err = store.ListExecutionEvents("exec-1")
			if err != nil {
				t.Fatalf("ListExecutionEvents (second call): %v", err)
			}
			merged = 0
			for _, e := range events {
				if e.Stage == StageMerged {
					merged++
				}
			}
			if merged != 1 {
				t.Fatalf("expected still exactly one StageMerged event after a repeat call, got %d of %d: %+v", merged, len(events), events)
			}
		})
	}
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}
