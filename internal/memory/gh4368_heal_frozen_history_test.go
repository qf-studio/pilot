package memory

import "testing"

// TestHealFrozenHistoryLadders is the GH-4368 regression: rows healed by the
// pre-GH-4292 code path (or any other path that flips status to 'completed'
// without recording a terminal execution_events row) sit outside autopilot's
// bounded merged_pr_scan_window forever once they're old enough, so
// SelfHealExecutionAfterMerge/SelfHealExecutionByPRURL — which only run
// per-task_id/pr_url off a live merge signal — never reach them again.
// HealFrozenHistoryLadders is the one-shot, whole-table sweep that does.
func TestHealFrozenHistoryLadders(t *testing.T) {
	tests := []struct {
		name          string
		initialStatus string
		prURL         string
		seedEvent     *Stage // nil means seed no events at all
		wantHealed    bool
		wantStage     Stage
	}{
		{
			name:          "frozen ladder with pr_url heals to merged",
			initialStatus: "completed",
			prURL:         "https://github.com/qf-studio/pilot/pull/4278",
			seedEvent:     stagePtr(StageRunning),
			wantHealed:    true,
			wantStage:     StageMerged,
		},
		{
			name:          "frozen ladder without pr_url heals to completed",
			initialStatus: "completed",
			prURL:         "",
			seedEvent:     stagePtr(StageCIPassed),
			wantHealed:    true,
			wantStage:     StageCompleted,
		},
		{
			name:          "completed row with zero events also heals",
			initialStatus: "completed",
			prURL:         "https://github.com/qf-studio/pilot/pull/4284",
			seedEvent:     nil,
			wantHealed:    true,
			wantStage:     StageMerged,
		},
		{
			name:          "already-terminal ledger is left alone",
			initialStatus: "completed",
			prURL:         "https://github.com/qf-studio/pilot/pull/4290",
			seedEvent:     stagePtr(StageMerged),
			wantHealed:    false,
		},
		{
			name:          "non-completed status (still running) is never touched",
			initialStatus: "running",
			prURL:         "",
			seedEvent:     stagePtr(StageRunning),
			wantHealed:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			exec := &Execution{ID: "exec-1", TaskID: "GH-4368", ProjectPath: "/proj", Status: tt.initialStatus, PRUrl: tt.prURL}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}
			if tt.seedEvent != nil {
				if err := store.InsertExecutionEvent("exec-1", *tt.seedEvent, "seed"); err != nil {
					t.Fatalf("seed InsertExecutionEvent: %v", err)
				}
			}

			healed, err := store.HealFrozenHistoryLadders()
			if err != nil {
				t.Fatalf("HealFrozenHistoryLadders: %v", err)
			}
			if tt.wantHealed && healed != 1 {
				t.Errorf("healed = %d, want 1", healed)
			}
			if !tt.wantHealed && healed != 0 {
				t.Errorf("healed = %d, want 0", healed)
			}

			events, err := store.ListExecutionEvents("exec-1")
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}

			if tt.wantHealed {
				var matches int
				for _, e := range events {
					if e.Stage == tt.wantStage {
						matches++
					}
				}
				if matches != 1 {
					t.Fatalf("expected exactly one %q backfill event, got %d of %d total: %+v", tt.wantStage, matches, len(events), events)
				}
			}

			// completed_at/status/pr_url must never move — same guarantee as GH-4277.
			got, err := store.GetExecution("exec-1")
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tt.initialStatus {
				t.Errorf("status = %q, want unchanged %q", got.Status, tt.initialStatus)
			}
			if got.PRUrl != tt.prURL {
				t.Errorf("pr_url = %q, want unchanged %q", got.PRUrl, tt.prURL)
			}

			// Idempotency: a second call must not append a duplicate event.
			if _, err := store.HealFrozenHistoryLadders(); err != nil {
				t.Fatalf("second HealFrozenHistoryLadders call failed: %v", err)
			}
			eventsAfter, err := store.ListExecutionEvents("exec-1")
			if err != nil {
				t.Fatalf("ListExecutionEvents (second call): %v", err)
			}
			if len(eventsAfter) != len(events) {
				t.Errorf("second call changed event count: got %d, want %d (not idempotent)", len(eventsAfter), len(events))
			}
		})
	}
}

func stagePtr(s Stage) *Stage { return &s }
