package autopilot

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
)

func TestTripTracker_RecordTrip(t *testing.T) {
	tracker := newTripTracker()

	// Record first trip
	count := tracker.recordTrip()
	if count != 1 {
		t.Errorf("expected 1 trip, got %d", count)
	}

	// Record second trip
	count = tracker.recordTrip()
	if count != 2 {
		t.Errorf("expected 2 trips, got %d", count)
	}

	// Record third trip
	count = tracker.recordTrip()
	if count != 3 {
		t.Errorf("expected 3 trips, got %d", count)
	}
}

func TestTripTracker_OldTripsExpire(t *testing.T) {
	tracker := newTripTracker()
	// Use a shorter window for testing
	tracker.escalationWindow = 100 * time.Millisecond

	// Record a trip
	tracker.recordTrip()

	// Wait for it to expire
	time.Sleep(150 * time.Millisecond)

	// Record another trip - old one should be filtered out
	count := tracker.recordTrip()
	if count != 1 {
		t.Errorf("expected 1 trip (old expired), got %d", count)
	}
}

func TestTripTracker_ShouldEscalate(t *testing.T) {
	tracker := newTripTracker()

	// Not enough trips
	tracker.recordTrip()
	tracker.recordTrip()
	if tracker.shouldEscalate() {
		t.Error("should not escalate with only 2 trips")
	}

	// Third trip hits threshold
	tracker.recordTrip()
	if !tracker.shouldEscalate() {
		t.Error("should escalate with 3 trips")
	}
}

func TestTripTracker_EscalationCooldown(t *testing.T) {
	tracker := newTripTracker()
	// Use a shorter cooldown for testing
	tracker.escalationCooldown = 100 * time.Millisecond

	// Trigger escalation
	tracker.recordTrip()
	tracker.recordTrip()
	tracker.recordTrip()

	if !tracker.shouldEscalate() {
		t.Error("should escalate initially")
	}

	// Mark sent
	tracker.markEscalationSent()

	// Should not escalate during cooldown
	if tracker.shouldEscalate() {
		t.Error("should not escalate during cooldown")
	}

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	// Add another trip to stay above threshold
	tracker.recordTrip()

	// Should escalate again after cooldown
	if !tracker.shouldEscalate() {
		t.Error("should escalate after cooldown")
	}
}

func TestTripTracker_RecentTripCount(t *testing.T) {
	tracker := newTripTracker()

	tracker.recordTrip()
	tracker.recordTrip()

	if count := tracker.recentTripCount(); count != 2 {
		t.Errorf("expected 2 trips, got %d", count)
	}
}

func TestMetricsAlerter_RecordCircuitBreakerTrip_NoEngine(t *testing.T) {
	// With nil engine, should not panic
	ma := &MetricsAlerter{
		engine:      nil,
		tripTracker: newTripTracker(),
	}

	// Should not panic
	ma.RecordCircuitBreakerTrip(123, "test failure")
}

func TestMetricsAlerter_RecordCircuitBreakerTrip_NoEscalation(t *testing.T) {
	config := alerts.DefaultConfig()
	config.Enabled = true
	engine := alerts.NewEngine(config)

	controller := &Controller{
		owner: "test",
		repo:  "repo",
	}

	ma := NewMetricsAlerter(controller, engine)

	// Record 2 trips - should not trigger escalation
	ma.RecordCircuitBreakerTrip(1, "failure 1")
	ma.RecordCircuitBreakerTrip(2, "failure 2")

	// No escalation should have been sent (checked via tripTracker state)
	if ma.tripTracker.lastEscalationSentAt.IsZero() == false {
		// If not zero, escalation was sent prematurely
		if ma.tripTracker.recentTripCount() < 3 {
			t.Error("escalation sent before threshold reached")
		}
	}
}

func TestMetricsAlerter_RecordCircuitBreakerTrip_Escalation(t *testing.T) {
	config := alerts.DefaultConfig()
	config.Enabled = true
	engine := alerts.NewEngine(config)

	controller := &Controller{
		owner: "test",
		repo:  "repo",
	}

	ma := NewMetricsAlerter(controller, engine)

	// Record 3 trips - should trigger escalation
	ma.RecordCircuitBreakerTrip(1, "failure 1")
	ma.RecordCircuitBreakerTrip(2, "failure 2")
	ma.RecordCircuitBreakerTrip(3, "failure 3")

	// Check that escalation was marked as sent
	if ma.tripTracker.lastEscalationSentAt.IsZero() {
		t.Error("escalation should have been sent after 3 trips")
	}
}

func TestTripTracker_ThresholdCustomizable(t *testing.T) {
	tracker := newTripTracker()
	tracker.escalationThreshold = 5

	// Record 4 trips
	for i := 0; i < 4; i++ {
		tracker.recordTrip()
	}

	if tracker.shouldEscalate() {
		t.Error("should not escalate with threshold of 5 and only 4 trips")
	}

	// Fifth trip should trigger
	tracker.recordTrip()
	if !tracker.shouldEscalate() {
		t.Error("should escalate after 5 trips with threshold of 5")
	}
}

// fleetSnapshot stands in for the fleet-wide metrics source (GH-4068), whose
// total_active_prs can be nonzero while this controller has nothing in flight.
type fleetSnapshot struct{ activePRs int }

func (f fleetSnapshot) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{TotalActivePRs: f.activePRs}
}

// alertCapture records dispatched alerts for assertions.
type alertCapture struct{ alerts chan *alerts.Alert }

func (c alertCapture) Name() string { return "capture" }
func (c alertCapture) Type() string { return "webhook" }
func (c alertCapture) Send(_ context.Context, a *alerts.Alert) error {
	c.alerts <- a
	return nil
}

// TestMetricsAlerter_Evaluate_IdleIsNotDeadlock is the GH-5448 regression pin:
// an idle controller reports no stall (even when a fleet-wide metrics source
// counts PRs in other repos), and the first PR registered after a long idle
// stretch does not inherit the idle time as its stall. A real stall still
// fires, exactly once.
func TestMetricsAlerter_Evaluate_IdleIsNotDeadlock(t *testing.T) {
	config := &alerts.AlertConfig{
		Enabled:  true,
		Channels: []alerts.ChannelConfig{{Name: "capture", Type: "webhook", Enabled: true}},
		Rules: []alerts.AlertRule{
			{Name: "autopilot_deadlock", Type: alerts.AlertTypeDeadlock, Enabled: true, Severity: alerts.SeverityCritical},
		},
	}
	capture := alertCapture{alerts: make(chan *alerts.Alert, 4)}
	dispatcher := alerts.NewDispatcher(config)
	dispatcher.RegisterChannel(capture)
	engine := alerts.NewEngine(config, alerts.WithDispatcher(dispatcher))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}

	controller := &Controller{
		owner:          "test",
		repo:           "repo",
		config:         &Config{},
		log:            slog.Default(),
		activePRs:      map[int]*PRState{},
		lastProgressAt: time.Now().Add(-2 * time.Hour),
		// A stale sent flag from a previous stall must be cleared by the idle
		// tick (ResetProgressClock), otherwise the next real stall never alerts.
		deadlockAlertSent: true,
	}
	ma := NewMetricsAlerter(controller, engine)
	ma.SetMetricsSource(fleetSnapshot{activePRs: 3})

	ma.evaluate() // idle for two hours: must not fire
	if controller.IsDeadlockAlertSent() {
		t.Fatal("idle evaluate() did not clear deadlockAlertSent")
	}

	controller.registerPR(7, "", 0, "", "", "", false)
	ma.evaluate() // PR 7 just registered: must not fire

	// Now PR 7 genuinely stalls. Events and dispatches are both handled in
	// order, so if either earlier tick had fired, its alert would arrive first.
	controller.mu.Lock()
	controller.lastProgressAt = time.Now().Add(-3 * time.Hour)
	controller.mu.Unlock()
	ma.evaluate()

	select {
	case a := <-capture.alerts:
		if !strings.Contains(a.Message, "PR #7") || !strings.Contains(a.Message, "180 minutes") {
			t.Fatalf("first deadlock alert was not the PR #7 stall: %q", a.Message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled active PR did not fire a deadlock alert")
	}
	if !controller.IsDeadlockAlertSent() {
		t.Error("deadlock alert not marked sent after a real stall")
	}
}
