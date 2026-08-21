package alerts

import (
	"context"
	"strings"
	"testing"
	"time"
)

// GH-5065: handleEscalation (engine.go) is the single handler for every
// EventTypeEscalation source, but it used to build its message exclusively
// from the circuit-breaker trip metadata (trips_in_hour/escalation_threshold/
// last_pr/last_reason) — fields only metrics_alerter.go's emitEscalationAlert
// populates. The autopilot emitters (alertStackedSupersetOnce,
// alertBaseMismatchOnce, alertBranchDeleteHeldOnce, alertUnresolvableBaseOnce)
// put their diagnostic text in event.Error instead, which handleEscalation
// never read — every one of their escalations rendered a blank template
// (incident a695c90e, 2026-08-21).

func mkGH5065EscalationEngine(t *testing.T) (*Engine, *mockChannel) {
	t.Helper()
	config := &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "pagerduty", Type: "pagerduty", Enabled: true, Severities: []Severity{SeverityCritical}},
		},
		Rules: []AlertRule{
			{
				Name:        "escalation",
				Type:        AlertTypeEscalation,
				Enabled:     true,
				Condition:   RuleCondition{EscalationRetries: 3},
				Severity:    SeverityCritical,
				Channels:    []string{"pagerduty"},
				Cooldown:    0,
				Description: "Escalate to PagerDuty after repeated failures for the same source",
			},
		},
	}

	mockCh := newMockChannel("pagerduty", "pagerduty")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher))
	return engine, mockCh
}

// TestHandleEscalation_AutopilotEmitterRendersEventError covers the
// alertStackedSupersetOnce/alertBaseMismatchOnce/alertBranchDeleteHeldOnce/
// alertUnresolvableBaseOnce shape: no circuit-breaker metadata, diagnostic
// text in event.Error. Before the GH-5065 fix, this rendered
// "Circuit breaker escalation:  trips in 1 hour (threshold: ). Last: PR # -".
func TestHandleEscalation_AutopilotEmitterRendersEventError(t *testing.T) {
	engine, mockCh := mkGH5065EscalationEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)
	defer engine.Stop()

	wantMsg := `refused to delete branch "pilot/GH-5052" (from PR #5054 cleanup) because it is the base of open PR #5055 in qf-studio/pilot — deleting it would orphan that PR's content the same way GH-4872 did`

	engine.ProcessEvent(Event{
		Type:      EventTypeEscalation,
		TaskID:    "branch-pilot/GH-5052-delete-held",
		TaskTitle: "Branch delete held: pilot/GH-5052 is the base of an open PR",
		Project:   "qf-studio/pilot",
		Error:     wantMsg,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":        "qf-studio/pilot",
			"branch":      "pilot/GH-5052",
			"pr":          "5054",
			"blocking_pr": "5055",
		},
	})

	waitForAlerts(t, mockCh, 1, 2*time.Second)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Message != wantMsg {
		t.Errorf("alert message = %q, want %q", alerts[0].Message, wantMsg)
	}
	if strings.Contains(alerts[0].Message, "Circuit breaker escalation") {
		t.Errorf("alert message must not render the circuit-breaker template for an event with no circuit-breaker metadata: %q", alerts[0].Message)
	}
	if alerts[0].Severity != SeverityCritical {
		t.Errorf("expected severity %s, got %s", SeverityCritical, alerts[0].Severity)
	}
}

// TestHandleEscalation_CircuitBreakerRendersExistingTemplate is the
// byte-identical-when-present guard from GH-5065: an escalation event
// carrying the full circuit-breaker metadata (metrics_alerter.go
// emitEscalationAlert's shape) must render exactly the pre-GH-5065 template,
// unaffected by the new event.Error fallback path.
func TestHandleEscalation_CircuitBreakerRendersExistingTemplate(t *testing.T) {
	engine, mockCh := mkGH5065EscalationEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)
	defer engine.Stop()

	engine.ProcessEvent(Event{
		Type:      EventTypeEscalation,
		TaskID:    "autopilot-circuit-breaker",
		TaskTitle: "Autopilot Circuit Breaker Escalation",
		Project:   "qf-studio/pilot",
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"trips_in_hour":        "5",
			"escalation_threshold": "3",
			"last_pr":              "5054",
			"last_reason":          "ci_timeout",
			"severity":             string(SeverityCritical),
		},
	})

	waitForAlerts(t, mockCh, 1, 2*time.Second)

	alerts := mockCh.getAlerts()
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	want := "Circuit breaker escalation: 5 trips in 1 hour (threshold: 3). Last: PR #5054 - ci_timeout"
	if alerts[0].Message != want {
		t.Errorf("alert message = %q, want %q", alerts[0].Message, want)
	}
}
