package memory

import (
	"os"
	"testing"
	"time"
)

func TestLatestAutopilotMetrics(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("empty table returns nil", func(t *testing.T) {
		row, err := store.LatestAutopilotMetrics()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if row != nil {
			t.Fatalf("expected nil, got %+v", row)
		}
	})

	t.Run("returns newer snapshot after two inserts", func(t *testing.T) {
		older := &AutopilotMetricsRow{
			SnapshotAt:    time.Now().Add(-10 * time.Minute),
			IssuesSuccess: 5,
			PRsMerged:     3,
		}
		newer := &AutopilotMetricsRow{
			SnapshotAt:    time.Now(),
			IssuesSuccess: 12,
			PRsMerged:     7,
		}

		if err := store.SaveAutopilotMetrics(older); err != nil {
			t.Fatalf("SaveAutopilotMetrics (older): %v", err)
		}
		if err := store.SaveAutopilotMetrics(newer); err != nil {
			t.Fatalf("SaveAutopilotMetrics (newer): %v", err)
		}

		got, err := store.LatestAutopilotMetrics()
		if err != nil {
			t.Fatalf("LatestAutopilotMetrics: %v", err)
		}
		if got == nil {
			t.Fatal("expected a row, got nil")
		}
		if got.IssuesSuccess != newer.IssuesSuccess {
			t.Errorf("IssuesSuccess: got %d, want %d", got.IssuesSuccess, newer.IssuesSuccess)
		}
		if got.PRsMerged != newer.PRsMerged {
			t.Errorf("PRsMerged: got %d, want %d", got.PRsMerged, newer.PRsMerged)
		}
	})
}
