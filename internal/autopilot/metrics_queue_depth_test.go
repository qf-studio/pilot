package autopilot

import (
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestRefreshQueueDepth verifies the pilot_queue_depth gauge wiring (GH-4246):
// SetQueueDepth had zero production callers, so the gauge always read 0
// regardless of actual DB queue depth. RefreshQueueDepth must track the DB
// queued/pending count and fall back to 0 once the queue drains.
func TestRefreshQueueDepth(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	metrics := NewMetrics()

	execs := []*memory.Execution{
		{ID: "q-1", TaskID: "T1", ProjectPath: "/p", Status: "queued"},
		{ID: "q-2", TaskID: "T2", ProjectPath: "/p", Status: "pending"},
		{ID: "q-3", TaskID: "T3", ProjectPath: "/p", Status: "running"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}

	if err := RefreshQueueDepth(store, metrics); err != nil {
		t.Fatalf("RefreshQueueDepth failed: %v", err)
	}
	if snap := metrics.Snapshot(); snap.QueueDepth != 2 {
		t.Errorf("Expected queue depth 2, got %d", snap.QueueDepth)
	}

	// Drain the queue and confirm the gauge returns to 0.
	if err := store.UpdateExecutionStatus("q-1", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	if err := store.UpdateExecutionStatus("q-2", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	if err := RefreshQueueDepth(store, metrics); err != nil {
		t.Fatalf("RefreshQueueDepth failed: %v", err)
	}
	if snap := metrics.Snapshot(); snap.QueueDepth != 0 {
		t.Errorf("Expected queue depth 0 after drain, got %d", snap.QueueDepth)
	}
}

func TestRefreshQueueDepthNilArgs(t *testing.T) {
	if err := RefreshQueueDepth(nil, NewMetrics()); err != nil {
		t.Errorf("RefreshQueueDepth(nil store) should be a no-op, got error: %v", err)
	}
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	if err := RefreshQueueDepth(store, nil); err != nil {
		t.Errorf("RefreshQueueDepth(nil metrics) should be a no-op, got error: %v", err)
	}
}
