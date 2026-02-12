package autopilot

import (
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/alerts"
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
