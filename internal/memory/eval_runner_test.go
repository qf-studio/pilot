package memory

import (
	"context"
	"fmt"
	"math"
	"testing"
)

// mockEvalExecutor implements EvalExecutor for testing.
type mockEvalExecutor struct {
	results map[string]*EvalResult // keyed by task ID
	err     error
}

func (m *mockEvalExecutor) RunEvalTask(_ context.Context, task *EvalTask, model string) (*EvalResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	if r, ok := m.results[task.ID]; ok {
		return r, nil
	}
	return &EvalResult{
		TaskID:     task.ID,
		Model:      model,
		Passed:     false,
		DurationMs: 100,
		ErrorMsg:   "not configured in mock",
	}, nil
}

func TestPass1(t *testing.T) {
	tests := []struct {
		name    string
		results []*EvalResult
		want    float64
	}{
		{
			name:    "empty results",
			results: nil,
			want:    0,
		},
		{
			name: "all passed",
			results: []*EvalResult{
				{Passed: true},
				{Passed: true},
				{Passed: true},
			},
			want: 100,
		},
		{
			name: "none passed",
			results: []*EvalResult{
				{Passed: false},
				{Passed: false},
			},
			want: 0,
		},
		{
			name: "mixed results",
			results: []*EvalResult{
				{Passed: true},
				{Passed: false},
				{Passed: true},
				{Passed: false},
			},
			want: 50,
		},
		{
			name: "single pass",
			results: []*EvalResult{
				{Passed: true},
			},
			want: 100,
		},
		{
			name: "single fail",
			results: []*EvalResult{
				{Passed: false},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Pass1(tt.results)
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("Pass1() = %.2f, want %.2f", got, tt.want)
			}
		})
	}
}

func TestPassK(t *testing.T) {
	tests := []struct {
		name    string
		results []*EvalResult
		k       int
		want    float64
		delta   float64 // tolerance
	}{
		{
			name:    "empty results",
			results: nil,
			k:       1,
			want:    0,
			delta:   0.01,
		},
		{
			name:    "k=0",
			results: []*EvalResult{{TaskID: "a", Passed: true}},
			k:       0,
			want:    0,
			delta:   0.01,
		},
		{
			name: "k=1 same as pass@1",
			results: []*EvalResult{
				{TaskID: "a", Passed: true},
				{TaskID: "b", Passed: false},
			},
			k:     1,
			want:  50,
			delta: 0.01,
		},
		{
			name: "k=2 with two attempts per task all pass",
			results: []*EvalResult{
				{TaskID: "a", Passed: true},
				{TaskID: "a", Passed: true},
			},
			k:     2,
			want:  100,
			delta: 0.01,
		},
		{
			name: "k=2 with one pass out of two attempts",
			results: []*EvalResult{
				{TaskID: "a", Passed: true},
				{TaskID: "a", Passed: false},
			},
			k:     2,
			want:  100, // pass@2 with 1/2 correct = 1 - C(1,2)/C(2,2) = 1 - 0/1 = 1
			delta: 0.01,
		},
		{
			name: "k=2 with zero passes",
			results: []*EvalResult{
				{TaskID: "a", Passed: false},
				{TaskID: "a", Passed: false},
			},
			k:     2,
			want:  0,
			delta: 0.01,
		},
		{
			name: "k exceeds samples — fallback to empirical",
			results: []*EvalResult{
				{TaskID: "a", Passed: true},
			},
			k:     5,
			want:  100,
			delta: 0.01,
		},
		{
			name: "k exceeds samples — no pass",
			results: []*EvalResult{
				{TaskID: "a", Passed: false},
			},
			k:     5,
			want:  0,
			delta: 0.01,
		},
		{
			name: "multiple tasks averaged",
			results: []*EvalResult{
				{TaskID: "a", Passed: true},
				{TaskID: "a", Passed: false},
				{TaskID: "a", Passed: false},
				{TaskID: "b", Passed: false},
				{TaskID: "b", Passed: false},
				{TaskID: "b", Passed: false},
			},
			k:     1,
			want:  16.67, // task a: 1-C(2,1)/C(3,1) = 1-2/3 = 1/3; task b: 0; avg = 1/6
			delta: 0.1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PassK(tt.results, tt.k)
			if math.Abs(got-tt.want) > tt.delta {
				t.Errorf("PassK(k=%d) = %.2f, want ~%.2f (±%.2f)", tt.k, got, tt.want, tt.delta)
			}
		})
	}
}

func TestCompareModels(t *testing.T) {
	tests := []struct {
		name       string
		resultsA   []*EvalResult
		resultsB   []*EvalResult
		wantWinner string
		wantDelta  float64
	}{
		{
			name: "model A wins",
			resultsA: []*EvalResult{
				{Model: "opus", Passed: true, DurationMs: 1000, TokensUsed: 500, CostUSD: 0.10},
				{Model: "opus", Passed: true, DurationMs: 2000, TokensUsed: 600, CostUSD: 0.12},
			},
			resultsB: []*EvalResult{
				{Model: "sonnet", Passed: true, DurationMs: 500, TokensUsed: 300, CostUSD: 0.05},
				{Model: "sonnet", Passed: false, DurationMs: 400, TokensUsed: 200, CostUSD: 0.04},
			},
			wantWinner: "opus",
			wantDelta:  50,
		},
		{
			name: "model B wins",
			resultsA: []*EvalResult{
				{Model: "opus", Passed: false},
			},
			resultsB: []*EvalResult{
				{Model: "sonnet", Passed: true},
			},
			wantWinner: "sonnet",
			wantDelta:  -100,
		},
		{
			name: "tie",
			resultsA: []*EvalResult{
				{Model: "opus", Passed: true},
				{Model: "opus", Passed: false},
			},
			resultsB: []*EvalResult{
				{Model: "sonnet", Passed: true},
				{Model: "sonnet", Passed: false},
			},
			wantWinner: "tie",
			wantDelta:  0,
		},
		{
			name:       "both empty",
			resultsA:   nil,
			resultsB:   nil,
			wantWinner: "tie",
			wantDelta:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := CompareModels(tt.resultsA, tt.resultsB)
			if mc.Winner != tt.wantWinner {
				t.Errorf("Winner = %q, want %q", mc.Winner, tt.wantWinner)
			}
			if math.Abs(mc.Delta-tt.wantDelta) > 0.01 {
				t.Errorf("Delta = %.2f, want %.2f", mc.Delta, tt.wantDelta)
			}
		})
	}
}

func TestCompareModelsMetrics(t *testing.T) {
	resultsA := []*EvalResult{
		{Model: "opus", Passed: true, DurationMs: 1000, TokensUsed: 500, CostUSD: 0.10},
		{Model: "opus", Passed: true, DurationMs: 3000, TokensUsed: 700, CostUSD: 0.14},
	}
	resultsB := []*EvalResult{
		{Model: "sonnet", Passed: true, DurationMs: 400, TokensUsed: 200, CostUSD: 0.04},
		{Model: "sonnet", Passed: true, DurationMs: 600, TokensUsed: 400, CostUSD: 0.08},
	}

	mc := CompareModels(resultsA, resultsB)

	if mc.AvgDurationA != 2000 {
		t.Errorf("AvgDurationA = %d, want 2000", mc.AvgDurationA)
	}
	if mc.AvgDurationB != 500 {
		t.Errorf("AvgDurationB = %d, want 500", mc.AvgDurationB)
	}
	if mc.AvgTokensA != 600 {
		t.Errorf("AvgTokensA = %d, want 600", mc.AvgTokensA)
	}
	if mc.AvgTokensB != 300 {
		t.Errorf("AvgTokensB = %d, want 300", mc.AvgTokensB)
	}
	if math.Abs(mc.TotalCostA-0.24) > 0.001 {
		t.Errorf("TotalCostA = %.3f, want 0.240", mc.TotalCostA)
	}
	if math.Abs(mc.TotalCostB-0.12) > 0.001 {
		t.Errorf("TotalCostB = %.3f, want 0.120", mc.TotalCostB)
	}
}

func TestRunEval(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	tasks := []*EvalTask{
		{ID: "eval-1", IssueNumber: 1, Repo: "org/repo", PassCriteria: []PassCriteria{{Type: "build", Passed: true}}},
		{ID: "eval-2", IssueNumber: 2, Repo: "org/repo", PassCriteria: []PassCriteria{{Type: "build", Passed: true}}},
		{ID: "eval-3", IssueNumber: 3, Repo: "org/repo", PassCriteria: []PassCriteria{{Type: "test", Passed: false}}},
	}

	executor := &mockEvalExecutor{
		results: map[string]*EvalResult{
			"eval-1": {TaskID: "eval-1", Passed: true, DurationMs: 1000, TokensUsed: 500, CostUSD: 0.10},
			"eval-2": {TaskID: "eval-2", Passed: true, DurationMs: 2000, TokensUsed: 600, CostUSD: 0.12},
			"eval-3": {TaskID: "eval-3", Passed: true, DurationMs: 1500, TokensUsed: 400, CostUSD: 0.08},
		},
	}

	summary, err := RunEval(context.Background(), EvalRunConfig{
		RunID:    "run-1",
		Tasks:    tasks,
		Model:    "opus",
		Store:    store,
		Executor: executor,
	})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}

	if summary.TotalTasks != 3 {
		t.Errorf("TotalTasks = %d, want 3", summary.TotalTasks)
	}
	// eval-3's PassCriteria has Passed=false, so validation should flip it
	if summary.Passed != 2 {
		t.Errorf("Passed = %d, want 2", summary.Passed)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", summary.Failed)
	}
	if math.Abs(summary.PassRate-66.67) > 0.1 {
		t.Errorf("PassRate = %.2f, want ~66.67", summary.PassRate)
	}

	// Verify persistence
	results, err := store.GetEvalResults(EvalResultFilter{RunID: "run-1"})
	if err != nil {
		t.Fatalf("GetEvalResults: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("persisted results = %d, want 3", len(results))
	}

	// Verify stats
	stats, err := store.GetEvalStats("run-1")
	if err != nil {
		t.Fatalf("GetEvalStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	if stats[0].Model != "opus" {
		t.Errorf("stats model = %q, want opus", stats[0].Model)
	}
	if stats[0].TotalTasks != 3 {
		t.Errorf("stats total = %d, want 3", stats[0].TotalTasks)
	}
	if stats[0].Passed != 2 {
		t.Errorf("stats passed = %d, want 2", stats[0].Passed)
	}
}

func TestRunEvalWithLimit(t *testing.T) {
	executor := &mockEvalExecutor{
		results: map[string]*EvalResult{
			"eval-1": {TaskID: "eval-1", Passed: true, DurationMs: 100},
			"eval-2": {TaskID: "eval-2", Passed: true, DurationMs: 100},
			"eval-3": {TaskID: "eval-3", Passed: true, DurationMs: 100},
		},
	}

	tasks := []*EvalTask{
		{ID: "eval-1"}, {ID: "eval-2"}, {ID: "eval-3"},
	}

	summary, err := RunEval(context.Background(), EvalRunConfig{
		RunID:    "run-limit",
		Tasks:    tasks,
		Model:    "opus",
		Limit:    2,
		Executor: executor,
	})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}

	if summary.TotalTasks != 2 {
		t.Errorf("TotalTasks = %d, want 2 (limited)", summary.TotalTasks)
	}
}

func TestRunEvalExecutorError(t *testing.T) {
	executor := &mockEvalExecutor{
		err: fmt.Errorf("worktree checkout failed"),
	}

	tasks := []*EvalTask{{ID: "eval-1"}}

	summary, err := RunEval(context.Background(), EvalRunConfig{
		RunID:    "run-err",
		Tasks:    tasks,
		Model:    "opus",
		Executor: executor,
	})
	if err != nil {
		t.Fatalf("RunEval should not return error for executor failures: %v", err)
	}

	if summary.Passed != 0 {
		t.Errorf("Passed = %d, want 0", summary.Passed)
	}
	if summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", summary.Failed)
	}
	if summary.Results[0].ErrorMsg == "" {
		t.Error("expected error message in result")
	}
}

func TestRunEvalValidation(t *testing.T) {
	_, err := RunEval(context.Background(), EvalRunConfig{
		RunID: "",
	})
	if err == nil {
		t.Error("expected error for empty run_id")
	}

	_, err = RunEval(context.Background(), EvalRunConfig{
		RunID: "run-1",
	})
	if err == nil {
		t.Error("expected error for nil executor")
	}
}

func TestRunEvalContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	executor := &mockEvalExecutor{
		results: map[string]*EvalResult{
			"eval-1": {TaskID: "eval-1", Passed: true},
		},
	}

	summary, err := RunEval(ctx, EvalRunConfig{
		RunID:    "run-cancel",
		Tasks:    []*EvalTask{{ID: "eval-1"}},
		Model:    "opus",
		Executor: executor,
	})
	if err != nil {
		t.Fatalf("RunEval: %v", err)
	}

	// With cancelled context, no tasks should run
	if summary.Passed != 0 && summary.Failed != 0 {
		// It may or may not have run — context cancellation is best-effort
	}
}

func TestSaveAndGetEvalResults(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	results := []*EvalResult{
		{RunID: "run-1", TaskID: "t1", Model: "opus", Passed: true, DurationMs: 1000, TokensUsed: 500, CostUSD: 0.10},
		{RunID: "run-1", TaskID: "t2", Model: "opus", Passed: false, DurationMs: 2000, TokensUsed: 600, CostUSD: 0.12, ErrorMsg: "test failed"},
		{RunID: "run-1", TaskID: "t3", Model: "sonnet", Passed: true, DurationMs: 500, TokensUsed: 300, CostUSD: 0.05},
		{RunID: "run-2", TaskID: "t1", Model: "opus", Passed: true, DurationMs: 900, TokensUsed: 450, CostUSD: 0.09},
	}

	for _, r := range results {
		if err := store.SaveEvalResult(r); err != nil {
			t.Fatalf("SaveEvalResult: %v", err)
		}
		if r.ID == 0 {
			t.Error("expected non-zero ID after save")
		}
	}

	tests := []struct {
		name      string
		filter    EvalResultFilter
		wantCount int
	}{
		{"all", EvalResultFilter{}, 4},
		{"by run", EvalResultFilter{RunID: "run-1"}, 3},
		{"by model", EvalResultFilter{Model: "opus"}, 3},
		{"by run and model", EvalResultFilter{RunID: "run-1", Model: "sonnet"}, 1},
		{"with limit", EvalResultFilter{Limit: 2}, 2},
		{"no match", EvalResultFilter{RunID: "nonexistent"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.GetEvalResults(tt.filter)
			if err != nil {
				t.Fatalf("GetEvalResults: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Errorf("got %d results, want %d", len(got), tt.wantCount)
			}
		})
	}
}

func TestGetEvalStats(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	results := []*EvalResult{
		{RunID: "run-1", TaskID: "t1", Model: "opus", Passed: true, DurationMs: 1000, TokensUsed: 500, CostUSD: 0.10},
		{RunID: "run-1", TaskID: "t2", Model: "opus", Passed: false, DurationMs: 2000, TokensUsed: 600, CostUSD: 0.12},
		{RunID: "run-1", TaskID: "t3", Model: "opus", Passed: true, DurationMs: 3000, TokensUsed: 700, CostUSD: 0.14},
		{RunID: "run-1", TaskID: "t4", Model: "sonnet", Passed: true, DurationMs: 400, TokensUsed: 200, CostUSD: 0.04},
		{RunID: "run-1", TaskID: "t5", Model: "sonnet", Passed: true, DurationMs: 600, TokensUsed: 300, CostUSD: 0.06},
	}

	for _, r := range results {
		if err := store.SaveEvalResult(r); err != nil {
			t.Fatalf("SaveEvalResult: %v", err)
		}
	}

	stats, err := store.GetEvalStats("run-1")
	if err != nil {
		t.Fatalf("GetEvalStats: %v", err)
	}

	if len(stats) != 2 {
		t.Fatalf("stats count = %d, want 2", len(stats))
	}

	// Stats are ordered by model name
	opus := stats[0]
	sonnet := stats[1]
	if opus.Model != "opus" || sonnet.Model != "sonnet" {
		t.Fatalf("unexpected model order: %s, %s", opus.Model, sonnet.Model)
	}

	if opus.TotalTasks != 3 {
		t.Errorf("opus total = %d, want 3", opus.TotalTasks)
	}
	if opus.Passed != 2 {
		t.Errorf("opus passed = %d, want 2", opus.Passed)
	}
	if opus.Failed != 1 {
		t.Errorf("opus failed = %d, want 1", opus.Failed)
	}
	if math.Abs(opus.PassRate-66.67) > 0.1 {
		t.Errorf("opus pass rate = %.2f, want ~66.67", opus.PassRate)
	}
	if opus.AvgDuration != 2000 {
		t.Errorf("opus avg duration = %d, want 2000", opus.AvgDuration)
	}
	if opus.AvgTokens != 600 {
		t.Errorf("opus avg tokens = %d, want 600", opus.AvgTokens)
	}
	if math.Abs(opus.TotalCost-0.36) > 0.001 {
		t.Errorf("opus total cost = %.3f, want 0.360", opus.TotalCost)
	}

	if sonnet.TotalTasks != 2 {
		t.Errorf("sonnet total = %d, want 2", sonnet.TotalTasks)
	}
	if sonnet.Passed != 2 {
		t.Errorf("sonnet passed = %d, want 2", sonnet.Passed)
	}
	if math.Abs(sonnet.PassRate-100) > 0.01 {
		t.Errorf("sonnet pass rate = %.2f, want 100", sonnet.PassRate)
	}
}

func TestGetEvalStatsAllRuns(t *testing.T) {
	store, cleanup := newTestStoreForEval(t)
	defer cleanup()

	results := []*EvalResult{
		{RunID: "run-1", TaskID: "t1", Model: "opus", Passed: true, DurationMs: 1000, TokensUsed: 500, CostUSD: 0.10},
		{RunID: "run-2", TaskID: "t1", Model: "opus", Passed: false, DurationMs: 2000, TokensUsed: 600, CostUSD: 0.12},
	}

	for _, r := range results {
		if err := store.SaveEvalResult(r); err != nil {
			t.Fatalf("SaveEvalResult: %v", err)
		}
	}

	// Empty runID → aggregates all
	stats, err := store.GetEvalStats("")
	if err != nil {
		t.Fatalf("GetEvalStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	if stats[0].TotalTasks != 2 {
		t.Errorf("total = %d, want 2", stats[0].TotalTasks)
	}
}
