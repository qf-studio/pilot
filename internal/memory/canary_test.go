package memory

import (
	"testing"
	"time"
)

// TestSaveExecution_PersistsIsCanary pins GH-4240: the is_canary column must
// round-trip through SaveExecution exactly as written — the ledger write path
// is unaffected by canary status, only metrics/hydrator/dashboard reads are.
func TestSaveExecution_PersistsIsCanary(t *testing.T) {
	tests := []struct {
		name     string
		isCanary bool
	}{
		{name: "canary flag persists true", isCanary: true},
		{name: "canary flag persists false (default)", isCanary: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newExecutionEventsTestStore(t)
			if err := store.SaveExecution(&Execution{
				ID: "exec-canary-1", TaskID: "GH-1", ProjectPath: "/p",
				Status: "completed", IsCanary: tt.isCanary,
			}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			var got bool
			if err := store.db.QueryRow(`SELECT is_canary FROM executions WHERE id = ?`, "exec-canary-1").Scan(&got); err != nil {
				t.Fatalf("query is_canary: %v", err)
			}
			if got != tt.isCanary {
				t.Errorf("is_canary = %v, want %v", got, tt.isCanary)
			}
		})
	}
}

// TestGetIssueLevelCounts_ExcludesCanary pins that a canary project's
// executions never contribute to pilot_issues_shipped_total /
// pilot_issues_attempted_total (TASK-392 gauges), with the same fixture run
// once with the flag off and once with it added, matching non-canary rows
// exactly.
func TestGetIssueLevelCounts_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	// Real work: one shipped, one never shipped.
	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "real-2", TaskID: "GH-2", ProjectPath: "/p", Status: "failed"}); err != nil {
		t.Fatalf("SaveExecution real-2: %v", err)
	}

	baseline, err := store.GetIssueLevelCounts("")
	if err != nil {
		t.Fatalf("GetIssueLevelCounts (baseline): %v", err)
	}
	if baseline.Attempted != 2 || baseline.Shipped != 1 {
		t.Fatalf("baseline = %+v, want Attempted=2 Shipped=1", baseline)
	}

	// Canary sandbox: several shipped issues that must not move the counts.
	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "canary-2", TaskID: "CANARY-2", ProjectPath: "/canary", Status: "completed", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-2: %v", err)
	}

	after, err := store.GetIssueLevelCounts("")
	if err != nil {
		t.Fatalf("GetIssueLevelCounts (with canary rows): %v", err)
	}
	if after.Attempted != baseline.Attempted || after.Shipped != baseline.Shipped {
		t.Errorf("counts changed after adding canary rows: got %+v, want unchanged %+v", after, baseline)
	}
}

// TestGetLifetimeTaskCounts_ExcludesCanary mirrors
// TestGetIssueLevelCounts_ExcludesCanary for the per-attempt lifetime counts
// hydrated into pilot_issues_processed_total.
func TestGetLifetimeTaskCounts_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "real-2", TaskID: "GH-2", ProjectPath: "/p", Status: "failed"}); err != nil {
		t.Fatalf("SaveExecution real-2: %v", err)
	}

	baseline, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts (baseline): %v", err)
	}

	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "canary-2", TaskID: "CANARY-2", ProjectPath: "/canary", Status: "failed", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-2: %v", err)
	}

	after, err := store.GetLifetimeTaskCounts("")
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts (with canary rows): %v", err)
	}
	if *after != *baseline {
		t.Errorf("counts changed after adding canary rows: got %+v, want unchanged %+v", after, baseline)
	}
}

// TestGetLifetimeCounterBaselines_ExcludesCanary pins the GH-4041 Prometheus
// counter hydration source: token/cost/execution-by-model-result baselines
// must not include canary rows.
func TestGetLifetimeCounterBaselines_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{
		ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed",
		ModelName: "claude-sonnet-5", TokensInput: 100, TokensOutput: 50, TokensTotal: 150,
		EstimatedCostUSD: 1.5,
	}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}

	baseline, err := store.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines (baseline): %v", err)
	}

	if err := store.SaveExecution(&Execution{
		ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed",
		ModelName: "claude-sonnet-5", TokensInput: 9999, TokensOutput: 9999, TokensTotal: 19998,
		EstimatedCostUSD: 999.0, IsCanary: true,
	}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}

	after, err := store.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines (with canary row): %v", err)
	}
	key := ModelDirectionKey{Model: "claude-sonnet-5", Direction: "input"}
	if after.TokensByModelDirection[key] != baseline.TokensByModelDirection[key] {
		t.Errorf("input tokens changed after adding canary row: got %d, want %d",
			after.TokensByModelDirection[key], baseline.TokensByModelDirection[key])
	}
	if after.CostByModel["claude-sonnet-5"] != baseline.CostByModel["claude-sonnet-5"] {
		t.Errorf("cost changed after adding canary row: got %v, want %v",
			after.CostByModel["claude-sonnet-5"], baseline.CostByModel["claude-sonnet-5"])
	}
	resultKey := ModelResultKey{Model: "claude-sonnet-5", Result: "success"}
	if after.ExecutionsByModelResult[resultKey] != baseline.ExecutionsByModelResult[resultKey] {
		t.Errorf("execution count changed after adding canary row: got %d, want %d",
			after.ExecutionsByModelResult[resultKey], baseline.ExecutionsByModelResult[resultKey])
	}
}

// TestGetLifetimePRCountersFromExecutions_ExcludesCanary pins the GH-4121
// hydration source for pilot_prs_merged_total/pilot_prs_failed_total.
func TestGetLifetimePRCountersFromExecutions_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed", PRUrl: "https://x/1"}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}

	baseline, err := store.GetLifetimePRCountersFromExecutions("")
	if err != nil {
		t.Fatalf("GetLifetimePRCountersFromExecutions (baseline): %v", err)
	}
	if baseline.Merged != 1 {
		t.Fatalf("baseline.Merged = %d, want 1", baseline.Merged)
	}

	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed", PRUrl: "https://x/2", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}

	after, err := store.GetLifetimePRCountersFromExecutions("")
	if err != nil {
		t.Fatalf("GetLifetimePRCountersFromExecutions (with canary row): %v", err)
	}
	if *after != *baseline {
		t.Errorf("counters changed after adding canary row: got %+v, want unchanged %+v", after, baseline)
	}
}

// TestGetLifetimeTokens_ExcludesCanary pins the dashboard's lifetime
// token/cost display.
func TestGetLifetimeTokens_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed", TokensTotal: 100}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}

	baseline, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens (baseline): %v", err)
	}

	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed", TokensTotal: 99999, IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}

	after, err := store.GetLifetimeTokens("")
	if err != nil {
		t.Fatalf("GetLifetimeTokens (with canary row): %v", err)
	}
	if *after != *baseline {
		t.Errorf("lifetime tokens changed after adding canary row: got %+v, want unchanged %+v", after, baseline)
	}
}

// TestGetRecentExecutions_ExcludesCanary pins the dashboard history feed
// (GetRecentExecutions backs /api/v1/history, /api/v1/queue, the TUI history
// panel, and comms bot commands): a canary row must never appear.
func TestGetRecentExecutions_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}

	execs, err := store.GetRecentExecutions(100, "")
	if err != nil {
		t.Fatalf("GetRecentExecutions: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("got %d executions, want 1 (canary row must be excluded)", len(execs))
	}
	if execs[0].ID != "real-1" {
		t.Errorf("got execution %q, want real-1", execs[0].ID)
	}
}

// TestGetDailyMetrics_ExcludesCanary pins the dashboard's daily sparkline
// aggregates.
func TestGetDailyMetrics_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "completed", TokensTotal: 100}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "completed", TokensTotal: 99999, IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}

	metrics, err := store.GetDailyMetrics(MetricsQuery{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetDailyMetrics: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("got %d daily buckets, want 1", len(metrics))
	}
	if metrics[0].ExecutionCount != 1 {
		t.Errorf("ExecutionCount = %d, want 1 (canary row must be excluded)", metrics[0].ExecutionCount)
	}
	if metrics[0].TotalTokens != 100 {
		t.Errorf("TotalTokens = %d, want 100 (canary row must be excluded)", metrics[0].TotalTokens)
	}
}

// TestGetLifetimeCIRunCounters_ExcludesCanary pins the GH-4134 hydration
// source: a canary execution's CI verdicts must not inflate
// pilot_ci_runs_total.
func TestGetLifetimeCIRunCounters_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	seedExecutionWithEvents(t, store, "real-1", "GH-1", []Stage{StagePRCreated, StageCIPassed, StageMerged})

	baseline, err := store.GetLifetimeCIRunCounters()
	if err != nil {
		t.Fatalf("GetLifetimeCIRunCounters (baseline): %v", err)
	}
	if baseline.Pass != 1 {
		t.Fatalf("baseline.Pass = %d, want 1", baseline.Pass)
	}

	seedCanaryExecutionWithEvents(t, store, "canary-1", "CANARY-1", []Stage{StagePRCreated, StageCIPassed, StageMerged})

	after, err := store.GetLifetimeCIRunCounters()
	if err != nil {
		t.Fatalf("GetLifetimeCIRunCounters (with canary row): %v", err)
	}
	if *after != *baseline {
		t.Errorf("counters changed after adding canary row: got %+v, want unchanged %+v", after, baseline)
	}
}

// TestGetLifetimePRTimeToMerge_ExcludesCanary pins pilot_pr_time_to_merge_seconds
// hydration: a canary execution's pr_created->merged delta must not appear.
func TestGetLifetimePRTimeToMerge_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	seedCanaryExecutionWithEvents(t, store, "canary-1", "CANARY-1", []Stage{StagePRCreated, StageMerged})

	samples, err := store.GetLifetimePRTimeToMerge()
	if err != nil {
		t.Fatalf("GetLifetimePRTimeToMerge: %v", err)
	}
	if len(samples) != 0 {
		t.Errorf("got %d samples from a canary-only fixture, want 0", len(samples))
	}

	seedExecutionWithEvents(t, store, "real-1", "GH-1", []Stage{StagePRCreated, StageMerged})

	samples, err = store.GetLifetimePRTimeToMerge()
	if err != nil {
		t.Fatalf("GetLifetimePRTimeToMerge (with canary row present): %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1 (only the non-canary execution)", len(samples))
	}
}

// TestGetLifetimeTimeToPR_ExcludesCanary pins pilot_time_to_pr_seconds
// hydration (GH-4211).
func TestGetLifetimeTimeToPR_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	seedCanaryExecutionWithEvents(t, store, "canary-1", "CANARY-1", []Stage{StageRunning, StagePRCreated})
	seedExecutionWithEvents(t, store, "real-1", "GH-1", []Stage{StageRunning, StagePRCreated})

	samples, err := store.GetLifetimeTimeToPR()
	if err != nil {
		t.Fatalf("GetLifetimeTimeToPR: %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1 (only the non-canary execution)", len(samples))
	}
}

// TestGetLifetimeQueueWait_ExcludesCanary pins pilot_queue_wait_seconds
// hydration (GH-4211): queue wait is read directly off the executions table,
// not execution_events. created_at/started_at are set via raw SQL with an
// explicit 5s gap — CURRENT_TIMESTAMP has only second resolution, so a
// Go-side sleep between inserts cannot reliably produce a positive delta.
func TestGetLifetimeQueueWait_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	base := time.Now().UTC()
	if err := store.SaveExecution(&Execution{ID: "real-1", TaskID: "GH-1", ProjectPath: "/p", Status: "queued"}); err != nil {
		t.Fatalf("SaveExecution real-1: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE executions SET created_at = ?, started_at = ? WHERE id = ?`,
		base, base.Add(5*time.Second), "real-1"); err != nil {
		t.Fatalf("backdate real-1: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "canary-1", TaskID: "CANARY-1", ProjectPath: "/canary", Status: "queued", IsCanary: true}); err != nil {
		t.Fatalf("SaveExecution canary-1: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE executions SET created_at = ?, started_at = ? WHERE id = ?`,
		base, base.Add(5*time.Second), "canary-1"); err != nil {
		t.Fatalf("backdate canary-1: %v", err)
	}

	samples, err := store.GetLifetimeQueueWait()
	if err != nil {
		t.Fatalf("GetLifetimeQueueWait: %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1 (only the non-canary execution)", len(samples))
	}
}

// TestGetLifetimeApprovalWait_ExcludesCanary pins pilot_approval_wait_seconds
// hydration (GH-4211).
func TestGetLifetimeApprovalWait_ExcludesCanary(t *testing.T) {
	store := newExecutionEventsTestStore(t)

	seedCanaryExecutionWithEvents(t, store, "canary-1", "CANARY-1", []Stage{StageAwaitingApproval, StageMerged})
	seedExecutionWithEvents(t, store, "real-1", "GH-1", []Stage{StageAwaitingApproval, StageMerged})

	samples, err := store.GetLifetimeApprovalWait()
	if err != nil {
		t.Fatalf("GetLifetimeApprovalWait: %v", err)
	}
	if len(samples) != 1 {
		t.Errorf("got %d samples, want 1 (only the non-canary execution)", len(samples))
	}
}

// seedCanaryExecutionWithEvents mirrors seedExecutionWithEvents but marks the
// execution row as canary (GH-4240).
func seedCanaryExecutionWithEvents(t *testing.T, store *Store, executionID, taskID string, stages []Stage) {
	t.Helper()
	if err := store.SaveExecution(&Execution{
		ID: executionID, TaskID: taskID, ProjectPath: "/canary", Status: "completed", IsCanary: true,
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
