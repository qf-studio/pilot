package memory

import (
	"os"
	"testing"
	"time"
)

func TestGetMetricsSummary_CacheTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-metrics-cache-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	execs := []*Execution{
		{
			ID:               "e1",
			TaskID:           "T1",
			ProjectPath:      "/proj",
			Status:           "completed",
			DurationMs:       1000,
			TokensInput:      100,
			TokensOutput:     200,
			TokensTotal:      300,
			TokensCacheRead:  400,
			TokensCacheWrite: 50,
			CreatedAt:        now,
		},
		{
			ID:               "e2",
			TaskID:           "T2",
			ProjectPath:      "/proj",
			Status:           "completed",
			DurationMs:       2000,
			TokensInput:      500,
			TokensOutput:     600,
			TokensTotal:      1100,
			TokensCacheRead:  200,
			TokensCacheWrite: 75,
			CreatedAt:        now,
		},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution %s: %v", e.ID, err)
		}
	}

	q := MetricsQuery{
		Start: now.Add(-time.Hour),
		End:   now.Add(time.Hour),
	}
	summary, err := store.GetMetricsSummary(q)
	if err != nil {
		t.Fatalf("GetMetricsSummary: %v", err)
	}

	if summary.TotalTokensCacheRead != 600 {
		t.Errorf("TotalTokensCacheRead = %d, want 600", summary.TotalTokensCacheRead)
	}
	if summary.TotalTokensCacheWrite != 125 {
		t.Errorf("TotalTokensCacheWrite = %d, want 125", summary.TotalTokensCacheWrite)
	}
}

func TestSaveExecutionMetrics_CacheTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-save-metrics-cache-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	exec := &Execution{
		ID:          "ex-cache-1",
		TaskID:      "T-cache-1",
		ProjectPath: "/proj",
		Status:      "completed",
		DurationMs:  1000,
		TokensTotal: 100,
		CreatedAt:   now,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	metrics := &ExecutionMetrics{
		ExecutionID:      exec.ID,
		TokensInput:      400,
		TokensOutput:     600,
		TokensTotal:      1000,
		TokensCacheRead:  850,
		TokensCacheWrite: 120,
		EstimatedCostUSD: 0.01,
		ModelName:        "claude-sonnet-4-6",
	}
	if err := store.SaveExecutionMetrics(metrics); err != nil {
		t.Fatalf("SaveExecutionMetrics: %v", err)
	}

	q := MetricsQuery{
		Start: now.Add(-time.Hour),
		End:   now.Add(time.Hour),
	}
	summary, err := store.GetMetricsSummary(q)
	if err != nil {
		t.Fatalf("GetMetricsSummary: %v", err)
	}
	if summary.TotalTokensCacheRead == 0 {
		t.Errorf("TotalTokensCacheRead = 0, want non-zero")
	}
	if summary.TotalTokensCacheRead != 850 {
		t.Errorf("TotalTokensCacheRead = %d, want 850", summary.TotalTokensCacheRead)
	}
	if summary.TotalTokensCacheWrite == 0 {
		t.Errorf("TotalTokensCacheWrite = 0, want non-zero")
	}
	if summary.TotalTokensCacheWrite != 120 {
		t.Errorf("TotalTokensCacheWrite = %d, want 120", summary.TotalTokensCacheWrite)
	}
}

func TestExportMetrics_CacheTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-export-cache-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	e := &Execution{
		ID:               "e1",
		TaskID:           "T1",
		ProjectPath:      "/proj",
		Status:           "completed",
		DurationMs:       1000,
		TokensInput:      100,
		TokensOutput:     200,
		TokensTotal:      300,
		TokensCacheRead:  777,
		TokensCacheWrite: 333,
		CreatedAt:        now,
	}
	if err := store.SaveExecution(e); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	q := MetricsQuery{
		Start: now.Add(-time.Hour),
		End:   now.Add(time.Hour),
	}
	exports, err := store.ExportMetrics(q)
	if err != nil {
		t.Fatalf("ExportMetrics: %v", err)
	}
	if len(exports) != 1 {
		t.Fatalf("len(exports) = %d, want 1", len(exports))
	}

	got := exports[0]
	if got.TokensCacheRead != 777 {
		t.Errorf("TokensCacheRead = %d, want 777", got.TokensCacheRead)
	}
	if got.TokensCacheWrite != 333 {
		t.Errorf("TokensCacheWrite = %d, want 333", got.TokensCacheWrite)
	}
}
