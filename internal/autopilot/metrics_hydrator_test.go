package autopilot

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestHydrateFromStore_PerLabelValues seeds a store with executions across two
// models and asserts HydrateFromStore restores per-(model,direction) token
// counters, per-model cost counters, per-(model,result) execution counters,
// and the derived success rate — matching the store's lifetime totals
// (GH-4041 acceptance criteria).
func TestHydrateFromStore_PerLabelValues(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		{
			ID: "h-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
			ModelName:   "claude-sonnet-4-5",
			TokensInput: 1000, TokensOutput: 500, TokensTotal: 1500,
			EstimatedCostUSD: 0.05,
		},
		{
			ID: "h-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "completed",
			ModelName:   "claude-sonnet-4-5",
			TokensInput: 2000, TokensOutput: 1000, TokensTotal: 3000,
			EstimatedCostUSD: 0.10,
		},
		{
			ID: "h-3", TaskID: "TASK-3", ProjectPath: "/p", Status: "failed",
			ModelName:   "claude-opus-4-6",
			TokensInput: 500, TokensOutput: 250, TokensTotal: 750,
			EstimatedCostUSD: 0.02,
		},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}

	snap := metrics.Snapshot()

	// Per-label token totals restored individually.
	wantTokens := map[tokenKey]int64{
		{Model: "claude-sonnet-4-5", Direction: "input"}:  3000,
		{Model: "claude-sonnet-4-5", Direction: "output"}: 1500,
		{Model: "claude-opus-4-6", Direction: "input"}:    500,
		{Model: "claude-opus-4-6", Direction: "output"}:   250,
	}
	for k, want := range wantTokens {
		if got := snap.TokensConsumed[k]; got != want {
			t.Errorf("TokensConsumed[%+v] = %d, want %d", k, got, want)
		}
	}
	var tokenSum int64
	for _, v := range snap.TokensConsumed {
		tokenSum += v
	}
	if want := int64(5250); tokenSum != want {
		t.Errorf("sum(TokensConsumed) = %d, want %d", tokenSum, want)
	}

	// Per-model cost restored.
	const epsilon = 0.0001
	if got, want := snap.ExecutionCostUSD["claude-sonnet-4-5"], 0.15; got < want-epsilon || got > want+epsilon {
		t.Errorf("ExecutionCostUSD[claude-sonnet-4-5] = %.4f, want %.4f", got, want)
	}
	if got, want := snap.ExecutionCostUSD["claude-opus-4-6"], 0.02; got < want-epsilon || got > want+epsilon {
		t.Errorf("ExecutionCostUSD[claude-opus-4-6] = %.4f, want %.4f", got, want)
	}

	// Per-(model,result) execution counts restored; sum matches total executions.
	wantExecs := map[execKey]int64{
		{Model: "claude-sonnet-4-5", Result: "success"}: 2,
		{Model: "claude-opus-4-6", Result: "failed"}:    1,
	}
	for k, want := range wantExecs {
		if got := snap.ExecutionsByResult[k]; got != want {
			t.Errorf("ExecutionsByResult[%+v] = %d, want %d", k, got, want)
		}
	}
	var execSum int64
	for _, v := range snap.ExecutionsByResult {
		execSum += v
	}
	if execSum != int64(len(execs)) {
		t.Errorf("sum(ExecutionsByResult) = %d, want %d", execSum, len(execs))
	}

	// pilot_success_rate inherits the fix for free via IssuesProcessed hydration:
	// 2 of the 3 lifetime executions succeeded.
	wantRate := 2.0 / 3.0
	if snap.SuccessRate < wantRate-epsilon || snap.SuccessRate > wantRate+epsilon {
		t.Errorf("SuccessRate = %.4f, want %.4f", snap.SuccessRate, wantRate)
	}
}

// TestHydrateFromStore_NonFailuresExcludedFromFailed pins TASK-392: declined/
// no_op/stalled/rate_limited/infra/skipped outcomes must not be folded into
// the hydrated "failed" bucket, mirroring the dispatcher's live taxonomy
// (TASK-358) where those statuses are distinct from genuine failures.
func TestHydrateFromStore_NonFailuresExcludedFromFailed(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	statuses := []struct {
		id, status string
	}{
		{"nf-1", "completed"},
		{"nf-2", "failed"},
		{"nf-3", "declined"},
		{"nf-4", "no_op"},
		{"nf-5", "stalled"},
		{"nf-6", "rate_limited"},
		{"nf-7", "infra"},
		{"nf-8", "skipped"},
	}
	for _, s := range statuses {
		if err := store.SaveExecution(&memory.Execution{
			ID: s.id, TaskID: "TASK-" + s.id, ProjectPath: "/p", Status: s.status,
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", s.id, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	// Only the single genuine "failed" row counts as failed — the five
	// non-failure statuses (declined/no_op/stalled/infra/skipped) must not
	// be folded in, and rate_limited hydrates into its own key.
	if got := snap.IssuesProcessed["failed"]; got != 1 {
		t.Errorf("IssuesProcessed[failed] = %d, want 1 (non-failures must not collapse in)", got)
	}
	if got := snap.IssuesProcessed["success"]; got != 1 {
		t.Errorf("IssuesProcessed[success] = %d, want 1", got)
	}
	if got := snap.IssuesProcessed["rate_limited"]; got != 1 {
		t.Errorf("IssuesProcessed[rate_limited] = %d, want 1", got)
	}
}

// TestHydrateFromStore_IssueLevelCounts pins TASK-392: a task retried twice
// before shipping (2 failed rows + 1 completed row, same task_id) hydrates to
// issue-level success 100%, distinct from the per-attempt view.
func TestHydrateFromStore_IssueLevelCounts(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*memory.Execution{
		{ID: "il-1", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed"},
		{ID: "il-2", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "failed"},
		{ID: "il-3", TaskID: "TASK-RETRY", ProjectPath: "/p", Status: "completed"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	if snap.IssuesAttempted != 1 {
		t.Errorf("IssuesAttempted = %d, want 1 (deduped by task_id)", snap.IssuesAttempted)
	}
	if snap.IssuesShipped != 1 {
		t.Errorf("IssuesShipped = %d, want 1", snap.IssuesShipped)
	}
	if snap.IssueLevelSuccessRate != 1.0 {
		t.Errorf("IssueLevelSuccessRate = %f, want 1.0", snap.IssueLevelSuccessRate)
	}

	// Per-attempt semantics are unchanged by the issue-level fix: rate_limited
	// is still excluded from the denominator, and every attempt still counts.
	wantAttemptRate := 1.0 / 3.0
	if snap.SuccessRate != wantAttemptRate {
		t.Errorf("SuccessRate = %f, want %f", snap.SuccessRate, wantAttemptRate)
	}
}

// TestHydrateFromStore_PRFamilyCounters pins GH-4093: pilot_prs_merged_total
// and pilot_prs_failed_total hydrate from the durable execution_events
// ledger (not from executions/autopilot_pr_state, which is deleted on PR
// completion), and a bare executor-level task failure with no PR ever
// created must not inflate PRsFailed.
func TestHydrateFromStore_PRFamilyCounters(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	type seed struct {
		id     string
		stages []memory.Stage
	}
	seeds := []seed{
		{"pr-1", []memory.Stage{memory.StagePRCreated, memory.StageCIPassed, memory.StageMerged}},
		{"pr-2", []memory.Stage{memory.StagePRCreated, memory.StageCIPassed, memory.StageMerged}},
		{"pr-3", []memory.Stage{memory.StagePRCreated, memory.StageCIFailed, memory.StageFailed}},
		// Executor-level failure: no PR was ever created for this attempt.
		{"pr-4", []memory.Stage{memory.StageQueued, memory.StageRunning, memory.StageFailed}},
	}
	for _, s := range seeds {
		if err := store.SaveExecution(&memory.Execution{
			ID: s.id, TaskID: "TASK-" + s.id, ProjectPath: "/p", Status: "completed",
		}); err != nil {
			t.Fatalf("SaveExecution %s: %v", s.id, err)
		}
		for _, stage := range s.stages {
			if err := store.InsertExecutionEvent(s.id, stage, ""); err != nil {
				t.Fatalf("InsertExecutionEvent %s/%s: %v", s.id, stage, err)
			}
		}
	}

	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, metrics); err != nil {
		t.Fatalf("HydrateFromStore: %v", err)
	}
	snap := metrics.Snapshot()

	if snap.PRsMerged != 2 {
		t.Errorf("PRsMerged = %d, want 2", snap.PRsMerged)
	}
	if snap.PRsFailed != 1 {
		t.Errorf("PRsFailed = %d, want 1 (executor-only failure with no PR must be excluded)", snap.PRsFailed)
	}

	// Acceptance: live merges on top of the hydrated baseline must not
	// double count — a fresh live RecordPRMerged() call adds exactly 1.
	metrics.RecordPRMerged()
	if got := metrics.Snapshot().PRsMerged; got != 3 {
		t.Errorf("PRsMerged after live merge = %d, want 3 (hydrated 2 + live 1)", got)
	}
}

// TestHydrateFromStore_NilStoreIsNoop verifies hydration is a no-op (not an
// error) when no store is configured, matching how other optional
// store-backed features degrade in this codebase.
func TestHydrateFromStore_NilStoreIsNoop(t *testing.T) {
	metrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), nil, metrics); err != nil {
		t.Fatalf("HydrateFromStore with nil store: %v", err)
	}
	snap := metrics.Snapshot()
	if len(snap.TokensConsumed) != 0 {
		t.Errorf("expected no hydration with nil store, got %+v", snap.TokensConsumed)
	}
}

// TestHydrateFromStore_RestartContinuity simulates a daemon restart: snapshot
// counter values after the first hydration (pre-restart), then hydrate a
// brand-new Metrics instance from the same store (post-restart) and assert
// the post-hydration values are >= the pre-restart snapshot — the core
// GH-4041 acceptance criterion (no observable regression across restart).
func TestHydrateFromStore_RestartContinuity(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID: "restart-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		ModelName:   "claude-sonnet-4-5",
		TokensInput: 1000, TokensOutput: 500, TokensTotal: 1500,
		EstimatedCostUSD: 0.05,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// Pre-restart: daemon boots, hydrates from store.
	preMetrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, preMetrics); err != nil {
		t.Fatalf("HydrateFromStore (pre-restart): %v", err)
	}
	preSnap := preMetrics.Snapshot()
	preTokens := preSnap.TokensConsumed[tokenKey{Model: "claude-sonnet-4-5", Direction: "input"}]
	preCost := preSnap.ExecutionCostUSD["claude-sonnet-4-5"]
	preExecs := preSnap.ExecutionsByResult[execKey{Model: "claude-sonnet-4-5", Result: "success"}]

	// More work happens against the same store before the simulated restart.
	if err := store.SaveExecution(&memory.Execution{
		ID: "restart-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "completed",
		ModelName:   "claude-sonnet-4-5",
		TokensInput: 500, TokensOutput: 250, TokensTotal: 750,
		EstimatedCostUSD: 0.02,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// Post-restart: a fresh (all-zero) Metrics is constructed, as happens on
	// daemon boot, then hydrated from the same store.
	postMetrics := NewMetrics()
	if err := HydrateFromStore(context.Background(), store, postMetrics); err != nil {
		t.Fatalf("HydrateFromStore (post-restart): %v", err)
	}
	postSnap := postMetrics.Snapshot()
	postTokens := postSnap.TokensConsumed[tokenKey{Model: "claude-sonnet-4-5", Direction: "input"}]
	postCost := postSnap.ExecutionCostUSD["claude-sonnet-4-5"]
	postExecs := postSnap.ExecutionsByResult[execKey{Model: "claude-sonnet-4-5", Result: "success"}]

	if postTokens < preTokens {
		t.Errorf("post-restart tokens = %d, want >= pre-restart %d", postTokens, preTokens)
	}
	if postCost < preCost {
		t.Errorf("post-restart cost = %.4f, want >= pre-restart %.4f", postCost, preCost)
	}
	if postExecs < preExecs {
		t.Errorf("post-restart executions = %d, want >= pre-restart %d", postExecs, preExecs)
	}

	// No 0-then-baseline gap: the freshly constructed postMetrics never
	// observably held zero for these labels before HydrateFromStore returned —
	// nothing reads postMetrics between NewMetrics() and HydrateFromStore().
	if postTokens == 0 {
		t.Error("post-restart tokens baseline is zero, hydration did not run")
	}
}
