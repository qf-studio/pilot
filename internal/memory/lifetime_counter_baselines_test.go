package memory

import "testing"

// TestGetLifetimeCounterBaselines_Empty verifies an empty executions table
// yields empty (not nil) maps, so callers can range over them unconditionally.
func TestGetLifetimeCounterBaselines_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	b, err := store.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines (empty): %v", err)
	}
	if len(b.TokensByModelDirection) != 0 {
		t.Errorf("TokensByModelDirection = %v, want empty", b.TokensByModelDirection)
	}
	if len(b.CostByModel) != 0 {
		t.Errorf("CostByModel = %v, want empty", b.CostByModel)
	}
	if len(b.ExecutionsByModelResult) != 0 {
		t.Errorf("ExecutionsByModelResult = %v, want empty", b.ExecutionsByModelResult)
	}
}

// TestGetLifetimeCounterBaselines_PerLabel verifies per-(model,direction) token
// totals, per-model cost totals, and per-(model,result) execution totals are
// aggregated correctly, and that summing the per-label values reproduces the
// same totals GetLifetimeTokens/GetLifetimeTaskCounts report (GH-4041).
func TestGetLifetimeCounterBaselines_PerLabel(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*Execution{
		{
			ID: "base-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
			ModelName:   "claude-sonnet-4-5",
			TokensInput: 1000, TokensOutput: 500, TokensTotal: 1500,
			TokensCacheRead: 200, TokensCacheWrite: 100, EstimatedCostUSD: 0.05,
		},
		{
			ID: "base-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "completed",
			ModelName:   "claude-sonnet-4-5",
			TokensInput: 2000, TokensOutput: 1000, TokensTotal: 3000,
			EstimatedCostUSD: 0.10,
		},
		{
			ID: "base-3", TaskID: "TASK-3", ProjectPath: "/p", Status: "failed",
			ModelName:   "claude-opus-4-6",
			TokensInput: 500, TokensOutput: 250, TokensTotal: 750,
			EstimatedCostUSD: 0.02,
		},
		{
			ID: "base-4", TaskID: "TASK-4", ProjectPath: "/p", Status: "stalled",
			ModelName: "claude-opus-4-6",
			// Zero tokens — excluded from the token/cost baseline (like
			// GetLifetimeTokens), but still counted as an execution.
		},
		{
			// Non-terminal row (still queued) — must not be counted as an execution.
			ID: "base-5", TaskID: "TASK-5", ProjectPath: "/p", Status: "queued",
			ModelName: "claude-opus-4-6",
		},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	b, err := store.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines: %v", err)
	}

	wantTokens := map[ModelDirectionKey]int64{
		{Model: "claude-sonnet-4-5", Direction: "input"}:          3000,
		{Model: "claude-sonnet-4-5", Direction: "output"}:         1500,
		{Model: "claude-sonnet-4-5", Direction: "cache_read"}:     200,
		{Model: "claude-sonnet-4-5", Direction: "cache_creation"}: 100,
		{Model: "claude-opus-4-6", Direction: "input"}:            500,
		{Model: "claude-opus-4-6", Direction: "output"}:           250,
	}
	for k, want := range wantTokens {
		if got := b.TokensByModelDirection[k]; got != want {
			t.Errorf("TokensByModelDirection[%+v] = %d, want %d", k, got, want)
		}
	}
	for k := range b.TokensByModelDirection {
		if _, ok := wantTokens[k]; !ok {
			t.Errorf("unexpected TokensByModelDirection key %+v = %d", k, b.TokensByModelDirection[k])
		}
	}

	wantCost := map[string]float64{
		"claude-sonnet-4-5": 0.15,
		"claude-opus-4-6":   0.02,
	}
	const epsilon = 0.0001
	for model, want := range wantCost {
		if got := b.CostByModel[model]; got < want-epsilon || got > want+epsilon {
			t.Errorf("CostByModel[%s] = %.4f, want %.4f", model, got, want)
		}
	}

	wantExecs := map[ModelResultKey]int64{
		{Model: "claude-sonnet-4-5", Result: "success"}: 2,
		{Model: "claude-opus-4-6", Result: "failed"}:    1,
		{Model: "claude-opus-4-6", Result: "stalled"}:   1,
	}
	for k, want := range wantExecs {
		if got := b.ExecutionsByModelResult[k]; got != want {
			t.Errorf("ExecutionsByModelResult[%+v] = %d, want %d", k, got, want)
		}
	}
	var totalExecs int64
	for _, v := range b.ExecutionsByModelResult {
		totalExecs += v
	}
	if totalExecs != 4 {
		t.Errorf("total executions = %d, want 4 (base-5 queued row excluded)", totalExecs)
	}
}

// TestGetLifetimeCounterBaselines_FullTaxonomy pins GH-4483: the per-model
// execution baseline must preserve the executions table's full status
// taxonomy (declined/no_op/rate_limited/infra/skipped each get their own
// result label) instead of collapsing every non-completed/non-stalled status
// into "failed" — the same defect TASK-392/#4070 fixed for the headline
// pilot_success_rate. Also pins that rows with an empty model_name are
// excluded entirely rather than bucketed under "unknown".
func TestGetLifetimeCounterBaselines_FullTaxonomy(t *testing.T) {
	tmpDir := t.TempDir()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	execs := []*Execution{
		{ID: "tax-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "declined", ModelName: "claude-sonnet-5"},
		{ID: "tax-2", TaskID: "TASK-2", ProjectPath: "/p", Status: "no_op", ModelName: "claude-sonnet-5"},
		{ID: "tax-3", TaskID: "TASK-3", ProjectPath: "/p", Status: "rate_limited", ModelName: "claude-sonnet-5"},
		{ID: "tax-4", TaskID: "TASK-4", ProjectPath: "/p", Status: "infra", ModelName: "claude-sonnet-5"},
		{ID: "tax-5", TaskID: "TASK-5", ProjectPath: "/p", Status: "skipped", ModelName: "claude-sonnet-5"},
		{ID: "tax-6", TaskID: "TASK-6", ProjectPath: "/p", Status: "failed", ModelName: "claude-sonnet-5"},
		// Empty model_name (pre-GH-4041 / died-before-invoking-Claude): must
		// be excluded entirely, not folded into a "model=unknown" bucket.
		{ID: "tax-7", TaskID: "TASK-7", ProjectPath: "/p", Status: "failed", ModelName: ""},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	b, err := store.GetLifetimeCounterBaselines()
	if err != nil {
		t.Fatalf("GetLifetimeCounterBaselines: %v", err)
	}

	wantExecs := map[ModelResultKey]int64{
		{Model: "claude-sonnet-5", Result: "declined"}:     1,
		{Model: "claude-sonnet-5", Result: "no_op"}:        1,
		{Model: "claude-sonnet-5", Result: "rate_limited"}: 1,
		{Model: "claude-sonnet-5", Result: "infra"}:        1,
		{Model: "claude-sonnet-5", Result: "skipped"}:      1,
		{Model: "claude-sonnet-5", Result: "failed"}:       1,
	}
	for k, want := range wantExecs {
		if got := b.ExecutionsByModelResult[k]; got != want {
			t.Errorf("ExecutionsByModelResult[%+v] = %d, want %d", k, got, want)
		}
	}
	for k := range b.ExecutionsByModelResult {
		if _, ok := wantExecs[k]; !ok {
			t.Errorf("unexpected ExecutionsByModelResult key %+v = %d", k, b.ExecutionsByModelResult[k])
		}
	}

	// No key for the empty-model row's status, under any model label
	// (including "unknown").
	for k := range b.ExecutionsByModelResult {
		if k.Model == "" || k.Model == "unknown" {
			t.Errorf("empty/unknown model row leaked into ExecutionsByModelResult: %+v", k)
		}
	}
}
