package memory

import (
	"os"
	"testing"
	"time"
)

// TestAutopilotMetricsMapRoundTrip verifies that TokensConsumed, ExecutionCostUSD,
// and ExecutionsByResult survive a full save → retrieve cycle (GH-2856).
func TestAutopilotMetricsMapRoundTrip(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-metrics-maps-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	row := &AutopilotMetricsRow{
		SnapshotAt:    time.Now(),
		IssuesSuccess: 3,
		PRsMerged:     1,
		TokensConsumed: map[string]int64{
			"claude-sonnet-4-6|input":      12000,
			"claude-sonnet-4-6|output":     3000,
			"claude-sonnet-4-6|cache_read": 500,
		},
		ExecutionCostUSD: map[string]float64{
			"claude-sonnet-4-6": 0.042,
		},
		ExecutionsByResult: map[string]int64{
			"claude-sonnet-4-6|success": 2,
			"claude-sonnet-4-6|failed":  1,
		},
	}

	if err := store.SaveAutopilotMetrics(row); err != nil {
		t.Fatalf("SaveAutopilotMetrics: %v", err)
	}

	t.Run("LatestAutopilotMetrics round-trips maps", func(t *testing.T) {
		got, err := store.LatestAutopilotMetrics()
		if err != nil {
			t.Fatalf("LatestAutopilotMetrics: %v", err)
		}
		if got == nil {
			t.Fatal("expected a row, got nil")
		}

		assertStringIntMap(t, "TokensConsumed", got.TokensConsumed, row.TokensConsumed)
		assertStringFloatMap(t, "ExecutionCostUSD", got.ExecutionCostUSD, row.ExecutionCostUSD)
		assertStringIntMap(t, "ExecutionsByResult", got.ExecutionsByResult, row.ExecutionsByResult)
	})

	t.Run("GetRecentAutopilotMetrics round-trips maps", func(t *testing.T) {
		rows, err := store.GetRecentAutopilotMetrics(1)
		if err != nil {
			t.Fatalf("GetRecentAutopilotMetrics: %v", err)
		}
		if len(rows) == 0 {
			t.Fatal("expected one row, got none")
		}
		got := rows[0]

		assertStringIntMap(t, "TokensConsumed", got.TokensConsumed, row.TokensConsumed)
		assertStringFloatMap(t, "ExecutionCostUSD", got.ExecutionCostUSD, row.ExecutionCostUSD)
		assertStringIntMap(t, "ExecutionsByResult", got.ExecutionsByResult, row.ExecutionsByResult)
	})

	t.Run("empty maps round-trip as non-nil empty maps", func(t *testing.T) {
		emptyRow := &AutopilotMetricsRow{
			SnapshotAt: time.Now().Add(time.Second),
		}
		if err := store.SaveAutopilotMetrics(emptyRow); err != nil {
			t.Fatalf("SaveAutopilotMetrics (empty): %v", err)
		}
		got, err := store.LatestAutopilotMetrics()
		if err != nil {
			t.Fatalf("LatestAutopilotMetrics (empty): %v", err)
		}
		if got == nil {
			t.Fatal("expected row, got nil")
		}
		if got.TokensConsumed == nil {
			t.Error("TokensConsumed should be non-nil empty map, got nil")
		}
		if got.ExecutionCostUSD == nil {
			t.Error("ExecutionCostUSD should be non-nil empty map, got nil")
		}
		if got.ExecutionsByResult == nil {
			t.Error("ExecutionsByResult should be non-nil empty map, got nil")
		}
	})
}

func assertStringIntMap(t *testing.T, name string, got, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: len mismatch: got %d, want %d", name, len(got), len(want))
	}
	for k, wantV := range want {
		if gotV, ok := got[k]; !ok {
			t.Errorf("%s: missing key %q", name, k)
		} else if gotV != wantV {
			t.Errorf("%s[%q]: got %d, want %d", name, k, gotV, wantV)
		}
	}
}

func assertStringFloatMap(t *testing.T, name string, got, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: len mismatch: got %d, want %d", name, len(got), len(want))
	}
	for k, wantV := range want {
		if gotV, ok := got[k]; !ok {
			t.Errorf("%s: missing key %q", name, k)
		} else if gotV != wantV {
			t.Errorf("%s[%q]: got %f, want %f", name, k, gotV, wantV)
		}
	}
}
