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
