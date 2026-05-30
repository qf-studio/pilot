package alerts

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// =============================================================================
// AlertMetrics unit tests
// =============================================================================

func TestAlertMetrics_RecordFired(t *testing.T) {
	m := NewAlertMetrics()
	m.RecordFired("task_failed", "critical")
	m.RecordFired("task_failed", "critical")
	m.RecordFired("daily_spend", "warning")

	snap := m.Snapshot()

	if got := snap.FiredTotal[alertFiredKey{Rule: "task_failed", Severity: "critical"}]; got != 2 {
		t.Errorf("expected 2 for task_failed/critical, got %d", got)
	}
	if got := snap.FiredTotal[alertFiredKey{Rule: "daily_spend", Severity: "warning"}]; got != 1 {
		t.Errorf("expected 1 for daily_spend/warning, got %d", got)
	}
}

func TestAlertMetrics_RecordDelivery(t *testing.T) {
	m := NewAlertMetrics()
	m.RecordDelivery("slack-ops", "slack", "success")
	m.RecordDelivery("slack-ops", "slack", "success")
	m.RecordDelivery("slack-ops", "slack", "failure")
	m.RecordDelivery("telegram", "telegram", "success")

	snap := m.Snapshot()

	if got := snap.DeliveryTotal[alertDeliveryKey{Channel: "slack-ops", Type: "slack", Result: "success"}]; got != 2 {
		t.Errorf("expected 2 slack-ops/success, got %d", got)
	}
	if got := snap.DeliveryTotal[alertDeliveryKey{Channel: "slack-ops", Type: "slack", Result: "failure"}]; got != 1 {
		t.Errorf("expected 1 slack-ops/failure, got %d", got)
	}
	if got := snap.DeliveryTotal[alertDeliveryKey{Channel: "telegram", Type: "telegram", Result: "success"}]; got != 1 {
		t.Errorf("expected 1 telegram/success, got %d", got)
	}
}

func TestAlertMetrics_RecordDropped(t *testing.T) {
	m := NewAlertMetrics()
	m.RecordDropped()
	m.RecordDropped()
	m.RecordDropped()

	snap := m.Snapshot()
	if snap.DroppedTotal != 3 {
		t.Errorf("expected DroppedTotal=3, got %d", snap.DroppedTotal)
	}
}

func TestAlertMetrics_SnapshotIsCopy(t *testing.T) {
	m := NewAlertMetrics()
	m.RecordFired("r1", "critical")

	snap := m.Snapshot()
	// Mutate original; snapshot should be unaffected.
	m.RecordFired("r1", "critical")

	if snap.FiredTotal[alertFiredKey{Rule: "r1", Severity: "critical"}] != 1 {
		t.Error("snapshot was not a copy — mutation affected it")
	}
}

func TestAlertMetrics_ZeroValues(t *testing.T) {
	m := NewAlertMetrics()
	snap := m.Snapshot()

	if len(snap.FiredTotal) != 0 {
		t.Errorf("expected empty FiredTotal, got %d entries", len(snap.FiredTotal))
	}
	if len(snap.DeliveryTotal) != 0 {
		t.Errorf("expected empty DeliveryTotal, got %d entries", len(snap.DeliveryTotal))
	}
	if snap.DroppedTotal != 0 {
		t.Errorf("expected DroppedTotal=0, got %d", snap.DroppedTotal)
	}
	if snap.QueueDepth != 0 {
		t.Errorf("expected QueueDepth=0, got %d", snap.QueueDepth)
	}
}

func TestAlertMetrics_ConcurrentSafe(t *testing.T) {
	m := NewAlertMetrics()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); m.RecordFired("r", "warning") }()
		go func() { defer wg.Done(); m.RecordDelivery("ch", "slack", "success") }()
		go func() { defer wg.Done(); m.RecordDropped() }()
	}
	wg.Wait()

	snap := m.Snapshot()
	if snap.FiredTotal[alertFiredKey{Rule: "r", Severity: "warning"}] != 100 {
		t.Errorf("expected FiredTotal=100, got %d", snap.FiredTotal[alertFiredKey{Rule: "r", Severity: "warning"}])
	}
	if snap.DroppedTotal != 100 {
		t.Errorf("expected DroppedTotal=100, got %d", snap.DroppedTotal)
	}
}

// =============================================================================
// Integration: shared metrics between Engine + Dispatcher
// =============================================================================

// TestAlertMetrics_SharedBetweenEngineAndDispatcher verifies that when the same
// AlertMetrics instance is injected into both Engine and Dispatcher, a single
// Engine.AlertSnapshot() call returns fired, dropped, AND delivery counters.
func TestAlertMetrics_SharedBetweenEngineAndDispatcher(t *testing.T) {
	cfg := &AlertConfig{
		Enabled: true,
		Rules: []AlertRule{
			{
				Name:        "task-failed-rule",
				Type:        AlertTypeTaskFailed,
				Severity:    SeverityCritical,
				Description: "task failed",
				Enabled:     true,
				Channels:    []string{"mock-ch"},
			},
		},
		Channels: []ChannelConfig{
			{Name: "mock-ch", Type: "mock", Enabled: true},
		},
	}

	m := NewAlertMetrics()
	failCh := newMockChannel("mock-ch", "mock")
	failCh.setError(errors.New("delivery failure"))

	d := NewDispatcher(cfg, WithDispatcherMetrics(m))
	d.RegisterChannel(failCh)

	e := NewEngine(cfg, WithDispatcher(d), WithAlertMetrics(m))

	// Directly exercise the delivery counter path.
	d.metrics.RecordDelivery("mock-ch", "mock", "failure")

	// Exercise the fired counter path.
	m.RecordFired("task-failed-rule", "critical")

	// Exercise the dropped counter path.
	m.RecordDropped()

	snap := e.AlertSnapshot()

	if snap.FiredTotal[alertFiredKey{Rule: "task-failed-rule", Severity: "critical"}] != 1 {
		t.Errorf("expected FiredTotal=1, got %d", snap.FiredTotal[alertFiredKey{Rule: "task-failed-rule", Severity: "critical"}])
	}
	if snap.DeliveryTotal[alertDeliveryKey{Channel: "mock-ch", Type: "mock", Result: "failure"}] != 1 {
		t.Errorf("expected DeliveryTotal failure=1, got %d", snap.DeliveryTotal[alertDeliveryKey{Channel: "mock-ch", Type: "mock", Result: "failure"}])
	}
	if snap.DroppedTotal != 1 {
		t.Errorf("expected DroppedTotal=1, got %d", snap.DroppedTotal)
	}
}

// TestAlertMetrics_DropCounterViaProcessEvent verifies ProcessEvent increments the drop
// counter when the event channel is full.
func TestAlertMetrics_DropCounterViaProcessEvent(t *testing.T) {
	cfg := &AlertConfig{Enabled: true}
	m := NewAlertMetrics()
	e := NewEngine(cfg, WithAlertMetrics(m))
	// Flood the buffered channel (capacity=100) without a consumer.
	for i := 0; i < 110; i++ {
		e.ProcessEvent(Event{Type: EventTypeTaskFailed})
	}

	snap := e.AlertSnapshot()
	if snap.DroppedTotal < 1 {
		t.Errorf("expected DroppedTotal >= 1, got %d", snap.DroppedTotal)
	}
}

// TestAlertMetrics_DeliveryCounterViaDispatcher verifies sendToChannel increments
// the delivery counter on both success and failure paths.
func TestAlertMetrics_DeliveryCounterViaDispatcher(t *testing.T) {
	cfg := &AlertConfig{Enabled: true}
	m := NewAlertMetrics()
	d := NewDispatcher(cfg, WithDispatcherMetrics(m))

	successCh := newMockChannel("ok-ch", "slack")
	failCh := newMockChannel("fail-ch", "telegram")
	failCh.setError(errors.New("boom"))

	d.RegisterChannel(successCh)
	d.RegisterChannel(failCh)

	alert := &Alert{ID: "a1", Severity: SeverityCritical}
	_ = d.Dispatch(context.Background(), alert, []string{"ok-ch", "fail-ch", "missing-ch"})

	snap := m.Snapshot()

	if snap.DeliveryTotal[alertDeliveryKey{Channel: "ok-ch", Type: "slack", Result: "success"}] != 1 {
		t.Errorf("expected ok-ch/success=1, got %d", snap.DeliveryTotal[alertDeliveryKey{Channel: "ok-ch", Type: "slack", Result: "success"}])
	}
	if snap.DeliveryTotal[alertDeliveryKey{Channel: "fail-ch", Type: "telegram", Result: "failure"}] != 1 {
		t.Errorf("expected fail-ch/failure=1, got %d", snap.DeliveryTotal[alertDeliveryKey{Channel: "fail-ch", Type: "telegram", Result: "failure"}])
	}
	// channel-not-found should also be recorded
	if snap.DeliveryTotal[alertDeliveryKey{Channel: "missing-ch", Type: "unknown", Result: "failure"}] != 1 {
		t.Errorf("expected missing-ch/unknown/failure=1, got %d", snap.DeliveryTotal[alertDeliveryKey{Channel: "missing-ch", Type: "unknown", Result: "failure"}])
	}
}
