package autopilot

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// newTestStore creates a temp-dir Store for integration tests.
func newTestStore(t *testing.T) (*memory.Store, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "metrics-persister-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}
	return store, func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
}

// newTestPersister returns a MetricsPersister backed by a stub controller that
// holds the given *Metrics so tests can inspect the result of RestoreFromRow.
func newTestPersister(m *Metrics, store *memory.Store) *MetricsPersister {
	ctrl := &Controller{metrics: m}
	return &MetricsPersister{
		controller: ctrl,
		store:      store,
		interval:   time.Hour, // large — we don't want ticks to fire in tests
		retention:  7 * 24 * time.Hour,
		log:        slog.Default(),
	}
}

// TestMetricsPersister_RestoreFromStore_OneRow seeds a store with one snapshot row
// and verifies that Run() populates Metrics before any tick fires.
func TestMetricsPersister_RestoreFromStore_OneRow(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	seed := &memory.AutopilotMetricsRow{
		SnapshotAt:          time.Now().Add(-10 * time.Minute),
		IssuesSuccess:       20,
		IssuesFailed:        4,
		IssuesRateLimited:   2,
		PRsMerged:           8,
		PRsFailed:           1,
		PRsConflicting:      3,
		CircuitBreakerTrips: 6,
	}
	if err := store.SaveAutopilotMetrics(seed); err != nil {
		t.Fatalf("SaveAutopilotMetrics: %v", err)
	}

	m := NewMetrics()
	mp := newTestPersister(m, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so the ticker loop exits right away

	mp.Run(ctx)

	snap := m.Snapshot()
	if snap.IssuesProcessed["success"] != 20 {
		t.Errorf("IssuesProcessed[success]: want 20, got %d", snap.IssuesProcessed["success"])
	}
	if snap.IssuesProcessed["failed"] != 4 {
		t.Errorf("IssuesProcessed[failed]: want 4, got %d", snap.IssuesProcessed["failed"])
	}
	if snap.PRsMerged != 8 {
		t.Errorf("PRsMerged: want 8, got %d", snap.PRsMerged)
	}
	if snap.CircuitBreakerTrips != 6 {
		t.Errorf("CircuitBreakerTrips: want 6, got %d", snap.CircuitBreakerTrips)
	}
}

// TestMetricsPersister_RestoreFromStore_Empty verifies that an empty store leaves
// all counters at zero and does not return an error.
func TestMetricsPersister_RestoreFromStore_Empty(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	m := NewMetrics()
	mp := newTestPersister(m, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Must not panic or log a fatal error; counters stay zero.
	mp.Run(ctx)

	snap := m.Snapshot()
	if snap.TotalIssuesProcessed() != 0 {
		t.Errorf("expected zero counters, got total %d", snap.TotalIssuesProcessed())
	}
	if snap.PRsMerged != 0 {
		t.Errorf("expected PRsMerged 0, got %d", snap.PRsMerged)
	}
}

// TestMetricsPersister_RestoreFromStore_LatestRow seeds two rows and confirms
// that the newer snapshot is the one restored.
func TestMetricsPersister_RestoreFromStore_LatestRow(t *testing.T) {
	store, cleanup := newTestStore(t)
	defer cleanup()

	older := &memory.AutopilotMetricsRow{
		SnapshotAt:    time.Now().Add(-20 * time.Minute),
		IssuesSuccess: 5,
	}
	newer := &memory.AutopilotMetricsRow{
		SnapshotAt:    time.Now().Add(-5 * time.Minute),
		IssuesSuccess: 99,
	}
	if err := store.SaveAutopilotMetrics(older); err != nil {
		t.Fatalf("SaveAutopilotMetrics(older): %v", err)
	}
	if err := store.SaveAutopilotMetrics(newer); err != nil {
		t.Fatalf("SaveAutopilotMetrics(newer): %v", err)
	}

	m := NewMetrics()
	mp := newTestPersister(m, store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mp.Run(ctx)

	snap := m.Snapshot()
	if snap.IssuesProcessed["success"] != 99 {
		t.Errorf("expected newest snapshot (99), got %d", snap.IssuesProcessed["success"])
	}
}
