package main

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/memory"
)

// TestStartQueueDepthRefresh verifies GH-4512: pilot_queue_depth must track
// the store's queued-task count from a ticker wired into the daemon
// lifecycle itself, independent of whether the interactive TUI dashboard
// (whose own 2s refresh loop was previously the gauge's only caller) is
// running. This exercises the non-dashboard ("headless") path (AC1).
func TestStartQueueDepthRefresh(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	metrics := autopilot.NewMetrics()

	// Seed two queued/pending executions before the loop's initial refresh
	// runs, so we can assert the gauge is correct immediately at boot
	// without waiting for a tick.
	execs := []*memory.Execution{
		{ID: "q-1", TaskID: "T1", ProjectPath: "/p", Status: "queued"},
		{ID: "q-2", TaskID: "T2", ProjectPath: "/p", Status: "pending"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	const tick = 20 * time.Millisecond
	startQueueDepthRefresh(ctx, store, metrics, tick)

	// The initial synchronous refresh (before the ticker loop starts) should
	// already reflect the seeded count, with no need to wait for a tick.
	if snap := metrics.Snapshot(); snap.QueueDepth != 2 {
		t.Fatalf("expected queue depth 2 immediately after start, got %d", snap.QueueDepth)
	}

	// Drain the queue and confirm a subsequent tick picks up the change —
	// proving the ticker (not just the initial call) drives the gauge.
	if err := store.UpdateExecutionStatus("q-1", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}
	if err := store.UpdateExecutionStatus("q-2", "completed"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		if snap := metrics.Snapshot(); snap.QueueDepth == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for ticker to refresh queue depth to 0, last value %d", metrics.Snapshot().QueueDepth)
		case <-time.After(tick):
		}
	}

	// AC3: cancelling the context must stop the ticker goroutine. Requeue a
	// task, cancel, then confirm the gauge stops tracking further changes —
	// if the goroutine leaked, it would keep refreshing and the gauge would
	// go back to 1.
	cancel()
	time.Sleep(5 * tick) // let any in-flight tick land before we mutate state

	if err := store.UpdateExecutionStatus("q-1", "queued"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}

	time.Sleep(10 * tick)
	if snap := metrics.Snapshot(); snap.QueueDepth != 0 {
		t.Errorf("expected queue depth to stay 0 after context cancellation (goroutine leak?), got %d", snap.QueueDepth)
	}
}

// TestStartQueueDepthRefreshNilArgs verifies the nil-safety no-op behavior
// mirrors autopilot.RefreshQueueDepth's own guard, so callers don't need to
// special-case a nil store/metrics before wiring the ticker.
func TestStartQueueDepthRefreshNilArgs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Neither call should panic or block.
	startQueueDepthRefresh(ctx, nil, autopilot.NewMetrics(), time.Millisecond)
	startQueueDepthRefresh(ctx, store, nil, time.Millisecond)
}
