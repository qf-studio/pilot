package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/alerts"
)

// MetricsAlerter periodically evaluates autopilot metrics and sends events
// to the alerts engine for rule evaluation.
type MetricsAlerter struct {
	controller *Controller
	engine     *alerts.Engine
	interval   time.Duration
	log        *slog.Logger
}

// NewMetricsAlerter creates a new MetricsAlerter.
func NewMetricsAlerter(controller *Controller, engine *alerts.Engine) *MetricsAlerter {
	return &MetricsAlerter{
		controller: controller,
		engine:     engine,
		interval:   30 * time.Second,
		log:        slog.Default().With("component", "metrics-alerter"),
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
