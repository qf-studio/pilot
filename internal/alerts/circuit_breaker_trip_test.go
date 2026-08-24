package alerts

import (
	"context"
	"fmt"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// circuitBreakerTripEvent builds a synthetic autopilot-metrics stats event
// carrying the given windowed/cumulative circuit_breaker_trips count, the
// same metadata shape metrics_alerter.go emits every poll tick.
func circuitBreakerTripEvent(trips int) Event {
	return Event{
		Type: EventTypeAutopilotMetrics,
		Metadata: map[string]string{
			"circuit_breaker_trips": fmt.Sprintf("%d", trips),
		},
	}
}

// newCircuitBreakerTestEngine builds a minimal engine with only the
// circuit_breaker_trip rule enabled and no cooldown, so tests can isolate
// the edge-trigger logic (counterDelta) from cooldown suppression.
// counterStore may be nil (in-memory-only checkpoint state).
func newCircuitBreakerTestEngine(t *testing.T, counterStore AlertCounterStore) (*Engine, *mockChannel) {
	t.Helper()
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:     "circuit_breaker_trip",
				Type:     AlertTypeCircuitBreakerTrip,
				Enabled:  true,
				Severity: SeverityCritical,
			},
		},
	}
	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	opts := []EngineOption{WithDispatcher(dispatcher)}
	if counterStore != nil {
		opts = append(opts, WithAlertCounterStore(counterStore))
	}
	engine := NewEngine(config, opts...)
	return engine, mockCh
}

// TestCircuitBreakerTrip_StandingNonzeroCounterNoAlert is the GH-5209
// regression pin: handleAutopilotMetrics must not fire on a counter that is
// merely nonzero. metrics_hydrator.go seeds circuit_breaker_trips
// periodically regardless of whether a trip just happened, so a
// level-triggered "cbTrips > 0" condition re-fires every cooldown period
// forever once the counter goes nonzero, with no live trip and no state to
// clear (the 2026-08-24 incident this issue reports).
func TestCircuitBreakerTrip_StandingNonzeroCounterNoAlert(t *testing.T) {
	engine, mockCh := newCircuitBreakerTestEngine(t, nil)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		engine.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(5))
	}

	if got := len(mockCh.getAlerts()); got != 0 {
		t.Fatalf("dispatched %d alerts for a standing nonzero counter, want 0", got)
	}
}

// TestCircuitBreakerTrip_IncreaseFiresExactlyOnce is the GH-5209 edge-trigger
// acceptance test: an increase over the last-observed value fires exactly
// one alert, the baseline-establishing first observation fires none even
// though nonzero, and repeating the new (now-unchanged) value again does not
// fire a second alert.
func TestCircuitBreakerTrip_IncreaseFiresExactlyOnce(t *testing.T) {
	engine, mockCh := newCircuitBreakerTestEngine(t, nil)
	ctx := context.Background()

	// Baseline observation: establishes last-seen=5, must not fire even
	// though the value is nonzero (GH-5209).
	engine.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(5))
	if got := len(mockCh.getAlerts()); got != 0 {
		t.Fatalf("baseline observation dispatched %d alerts, want 0", got)
	}

	// Fresh trip: counter increases 5 -> 6.
	engine.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(6))
	if got := len(mockCh.getAlerts()); got != 1 {
		t.Fatalf("dispatched %d alerts after counter increase, want exactly 1", got)
	}

	// Repeating the same (now-unchanged) value must not fire again.
	engine.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(6))
	if got := len(mockCh.getAlerts()); got != 1 {
		t.Fatalf("dispatched %d alerts after repeating unchanged value, want still 1", got)
	}
}

// TestCircuitBreakerTrip_RestartDoesNotRefire is the GH-5209 end-to-end
// acceptance test against real SQLite: a restart must rehydrate the
// pre-restart counter checkpoint, so the first post-restart stats event —
// which still carries the same value the daemon already alerted on — does
// not replay as a fresh alert, while a genuine increase after restart still
// fires normally.
func TestCircuitBreakerTrip_RestartDoesNotRefire(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	ctx := context.Background()

	// --- pre-restart process: baseline + one genuine trip ---
	engine1, mockCh1 := newCircuitBreakerTestEngine(t, store)
	engine1.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(5))
	engine1.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(6))
	if got := len(mockCh1.getAlerts()); got != 1 {
		t.Fatalf("pre-restart: dispatched %d alerts, want 1", got)
	}

	// --- restart: fresh Engine, fresh dispatcher/channel, same store ---
	engine2, mockCh2 := newCircuitBreakerTestEngine(t, store)

	// First post-restart event still carries the pre-restart value: must not
	// re-fire the backlog.
	engine2.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(6))
	if got := len(mockCh2.getAlerts()); got != 0 {
		t.Fatalf("first post-restart event (unchanged value) dispatched %d alerts, want 0", got)
	}

	// A genuine new trip after restart still fires normally.
	engine2.handleAutopilotMetrics(ctx, circuitBreakerTripEvent(7))
	if got := len(mockCh2.getAlerts()); got != 1 {
		t.Fatalf("post-restart increase dispatched %d alerts, want 1", got)
	}
}
