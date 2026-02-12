package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/alerts"
)

// tripTracker tracks circuit breaker trips over time for escalation detection.
// Escalates to PagerDuty after 3+ trips within 1 hour.
type tripTracker struct {
	trips                []time.Time
	mu                   sync.Mutex
	escalationThreshold  int
	escalationWindow     time.Duration
	lastEscalationSentAt time.Time
	escalationCooldown   time.Duration
}

// newTripTracker creates a trip tracker with default settings (3 trips in 1 hour).
func newTripTracker() *tripTracker {
	return &tripTracker{
		trips:               make([]time.Time, 0),
		escalationThreshold: 3,
		escalationWindow:    1 * time.Hour,
		escalationCooldown:  1 * time.Hour,
	}
}

// recordTrip records a circuit breaker trip and returns the count of trips
// within the escalation window. Also cleans up old trips.
func (t *tripTracker) recordTrip() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.trips = append(t.trips, now)

	// Remove trips older than the escalation window
	cutoff := now.Add(-t.escalationWindow)
	filtered := make([]time.Time, 0, len(t.trips))
	for _, trip := range t.trips {
		if trip.After(cutoff) {
			filtered = append(filtered, trip)
		}
	}
	t.trips = filtered

	return len(t.trips)
}

// shouldEscalate returns true if escalation should be triggered based on
// the current trip count and cooldown period.
func (t *tripTracker) shouldEscalate() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.trips) < t.escalationThreshold {
		return false
	}

	// Check if we're still in cooldown from last escalation
	if time.Since(t.lastEscalationSentAt) < t.escalationCooldown {
		return false
	}

	return true
}

// markEscalationSent records that an escalation was sent.
func (t *tripTracker) markEscalationSent() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastEscalationSentAt = time.Now()
}

// recentTripCount returns the count of trips within the window (thread-safe read).
func (t *tripTracker) recentTripCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.trips)
}

// MetricsAlerter periodically evaluates autopilot metrics and sends events
// to the alerts engine for rule evaluation.
type MetricsAlerter struct {
	controller  *Controller
	engine      *alerts.Engine
	interval    time.Duration
	log         *slog.Logger
	tripTracker *tripTracker
}

// NewMetricsAlerter creates a new MetricsAlerter.
func NewMetricsAlerter(controller *Controller, engine *alerts.Engine) *MetricsAlerter {
	return &MetricsAlerter{
		controller:  controller,
		engine:      engine,
		interval:    30 * time.Second,
		log:         slog.Default().With("component", "metrics-alerter"),
		tripTracker: newTripTracker(),
	}
}

// Run starts the metrics alerter loop.
func (ma *MetricsAlerter) Run(ctx context.Context) {
	if ma.engine == nil {
		ma.log.Debug("alerts engine not configured, metrics alerter disabled")
		return
	}

	ticker := time.NewTicker(ma.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ma.evaluate()
		}
	}
}

// evaluate takes a metrics snapshot and emits an autopilot_metrics event.
func (ma *MetricsAlerter) evaluate() {
	snap := ma.controller.Metrics().Snapshot()

	// Calculate stuck PRs (in waiting_ci)
	var prStuckCount int
	var prMaxWaitMin float64
	var lastStuckPR *PRState

	activePRs := ma.controller.GetActivePRs()
	for _, pr := range activePRs {
		if pr.Stage == StageWaitingCI && !pr.CIWaitStartedAt.IsZero() {
			waitMin := time.Since(pr.CIWaitStartedAt).Minutes()
			prStuckCount++
			if waitMin > prMaxWaitMin {
				prMaxWaitMin = waitMin
				lastStuckPR = pr
			}
		}
	}

	// GH-849: Deadlock detection - time since last progress
	lastProgressAt := ma.controller.GetLastProgressAt()
	noProgressMin := time.Since(lastProgressAt).Minutes()
	deadlockAlertSent := ma.controller.IsDeadlockAlertSent()

	// Find the last known state for deadlock context
	lastKnownState := ""
	lastKnownPR := 0
	if lastStuckPR != nil {
		lastKnownState = string(lastStuckPR.Stage)
		lastKnownPR = lastStuckPR.PRNumber
	} else if len(activePRs) > 0 {
		// Pick any active PR for context
		for _, pr := range activePRs {
			lastKnownState = string(pr.Stage)
			lastKnownPR = pr.PRNumber
			break
		}
	}

	event := alerts.Event{
		Type:      alerts.EventTypeAutopilotMetrics,
		TaskID:    "autopilot",
		TaskTitle: "Autopilot Health Check",
		Project:   fmt.Sprintf("%s/%s", ma.controller.owner, ma.controller.repo),
		Metadata: map[string]string{
			"failed_queue_depth":    fmt.Sprintf("%d", snap.FailedQueueDepth),
			"circuit_breaker_trips": fmt.Sprintf("%d", snap.CircuitBreakerTrips),
			"api_error_rate":        fmt.Sprintf("%.2f", snap.APIErrorRate),
			"pr_stuck_count":        fmt.Sprintf("%d", prStuckCount),
			"pr_max_wait_minutes":   fmt.Sprintf("%.1f", prMaxWaitMin),
			"success_rate":          fmt.Sprintf("%.2f", snap.SuccessRate),
			"total_active_prs":      fmt.Sprintf("%d", snap.TotalActivePRs),
			"queue_depth":           fmt.Sprintf("%d", snap.QueueDepth),
			// GH-849: Deadlock detection metadata
			"no_progress_minutes":   fmt.Sprintf("%.1f", noProgressMin),
			"deadlock_alert_sent":   fmt.Sprintf("%t", deadlockAlertSent),
			"last_known_state":      lastKnownState,
			"last_known_pr":         fmt.Sprintf("%d", lastKnownPR),
		},
		Timestamp: time.Now(),
	}

	ma.engine.ProcessEvent(event)

	// GH-849: Mark deadlock alert as sent if we're in deadlock state.
	// This prevents repeated alerts until progress resumes.
	// Default threshold is 1 hour (60 minutes).
	if noProgressMin >= 60 && !deadlockAlertSent && len(activePRs) > 0 {
		ma.controller.MarkDeadlockAlertSent()
	}
}

// RecordCircuitBreakerTrip records a circuit breaker trip and checks if escalation is needed.
// If 3+ trips occur within 1 hour, emits an escalation alert for PagerDuty.
func (ma *MetricsAlerter) RecordCircuitBreakerTrip(prNumber int, reason string) {
	if ma.engine == nil {
		return
	}

	// Record the trip and get the count within the window
	tripCount := ma.tripTracker.recordTrip()

	ma.log.Info("circuit breaker trip recorded",
		"pr", prNumber,
		"reason", reason,
		"trips_in_window", tripCount,
		"threshold", ma.tripTracker.escalationThreshold,
	)

	// Check if we should escalate to PagerDuty
	if ma.tripTracker.shouldEscalate() {
		ma.emitEscalationAlert(tripCount, prNumber, reason)
		ma.tripTracker.markEscalationSent()
	}
}

// emitEscalationAlert sends a critical escalation event for PagerDuty routing.
func (ma *MetricsAlerter) emitEscalationAlert(tripCount int, lastPR int, lastReason string) {
	ma.log.Warn("escalating to PagerDuty",
		"trips_in_window", tripCount,
		"last_pr", lastPR,
		"last_reason", lastReason,
	)

	event := alerts.Event{
		Type:      alerts.EventTypeEscalation,
		TaskID:    "autopilot-circuit-breaker",
		TaskTitle: "Autopilot Circuit Breaker Escalation",
		Project:   fmt.Sprintf("%s/%s", ma.controller.owner, ma.controller.repo),
		Metadata: map[string]string{
			"trips_in_hour":        fmt.Sprintf("%d", tripCount),
			"escalation_threshold": fmt.Sprintf("%d", ma.tripTracker.escalationThreshold),
			"last_pr":              fmt.Sprintf("%d", lastPR),
			"last_reason":          lastReason,
			"severity":             string(alerts.SeverityCritical),
		},
		Timestamp: time.Now(),
	}

	ma.engine.ProcessEvent(event)
}
