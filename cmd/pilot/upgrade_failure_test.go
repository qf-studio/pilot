package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
)

// mockAlertChannel is a minimal alerts.Channel implementation for asserting
// that reportUpgradeFailure's alerts.Event actually reaches a channel via
// the real Engine/Dispatcher, not just that ProcessEvent was called.
type mockAlertChannel struct {
	mu     sync.Mutex
	name   string
	alerts []*alerts.Alert
}

func newMockAlertChannel(name string) *mockAlertChannel {
	return &mockAlertChannel{name: name}
}

func (m *mockAlertChannel) Name() string { return m.name }
func (m *mockAlertChannel) Type() string { return "webhook" }

func (m *mockAlertChannel) Send(_ context.Context, alert *alerts.Alert) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = append(m.alerts, alert)
	return nil
}

func (m *mockAlertChannel) getAlerts() []*alerts.Alert {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*alerts.Alert, len(m.alerts))
	copy(out, m.alerts)
	return out
}

func waitForMockAlerts(t *testing.T, ch *mockAlertChannel, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(ch.getAlerts()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d alert(s), got %d", n, len(ch.getAlerts()))
}

// TestReportUpgradeFailure_EmitsAlert is the GH-4468 "loud failure" contract:
// a failed self-upgrade must reach a real alert channel (severity WARNING)
// through the same service_unhealthy rule path the self-upgrade-staleness
// check (GH-3790) already uses, not just log at ERROR.
func TestReportUpgradeFailure_EmitsAlert(t *testing.T) {
	config := &alerts.AlertConfig{
		Enabled: true,
		Channels: []alerts.ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []alerts.AlertRule{
			{
				Name:     "service-unhealthy",
				Type:     alerts.AlertTypeServiceUnhealthy,
				Enabled:  true,
				Severity: alerts.SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}

	mockCh := newMockAlertChannel("test-channel")
	dispatcher := alerts.NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := alerts.NewEngine(config, alerts.WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}

	upgradeErr := errors.New("binary directory /usr/local/bin is not writable by uid 1000: permission denied")
	reportUpgradeFailure(engine, "v2.242.0", "v2.243.0", upgradeErr)

	waitForMockAlerts(t, mockCh, 1, 2*time.Second)
	got := mockCh.getAlerts()
	if len(got) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(got))
	}
	if got[0].Type != alerts.AlertTypeServiceUnhealthy {
		t.Errorf("alert type = %s, want %s", got[0].Type, alerts.AlertTypeServiceUnhealthy)
	}
	if got[0].Severity != alerts.SeverityWarning {
		t.Errorf("alert severity = %s, want %s", got[0].Severity, alerts.SeverityWarning)
	}
	if !strings.Contains(got[0].Message, "not writable") {
		t.Errorf("alert message = %q, want it to include the upgrade error detail", got[0].Message)
	}
}

// TestReportUpgradeFailure_NoMatchingRule confirms the call is a no-op (no
// panic, no delivery attempt) when alerting is configured but no
// service_unhealthy rule exists — mirrors the engine's existing
// "unmatched rule drops silently" behavior.
func TestReportUpgradeFailure_NoMatchingRule(t *testing.T) {
	config := &alerts.AlertConfig{
		Enabled:  true,
		Channels: []alerts.ChannelConfig{{Name: "test-channel", Type: "webhook", Enabled: true}},
		Rules:    []alerts.AlertRule{},
	}

	mockCh := newMockAlertChannel("test-channel")
	dispatcher := alerts.NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := alerts.NewEngine(config, alerts.WithDispatcher(dispatcher))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start() error = %v", err)
	}

	reportUpgradeFailure(engine, "v2.242.0", "v2.243.0", errors.New("boom"))

	time.Sleep(100 * time.Millisecond)
	if got := len(mockCh.getAlerts()); got != 0 {
		t.Errorf("expected no alerts without a matching rule, got %d", got)
	}
}

// TestReportUpgradeFailure_NilEngineDoesNotPanic covers the daemon startup
// path where alertsEngine can be nil (alerting disabled/misconfigured) —
// reportUpgradeFailure must still log and simply skip alerting.
func TestReportUpgradeFailure_NilEngineDoesNotPanic(t *testing.T) {
	reportUpgradeFailure(nil, "v2.242.0", "v2.243.0", errors.New("boom"))
}
