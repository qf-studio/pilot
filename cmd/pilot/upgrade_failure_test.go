package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/executor"
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

// wrappedDrainTimeoutErr mirrors the real error chain a drain-timeout
// produces in production: executor.Monitor.WaitForTasks wraps
// executor.ErrDrainTimeout, and upgrade.HotUpgrader.PerformHotUpgrade wraps
// that again ("timeout waiting for tasks: %w").
func wrappedDrainTimeoutErr() error {
	inner := fmt.Errorf("%w: 1 tasks still active: [GH-72]", executor.ErrDrainTimeout)
	return fmt.Errorf("timeout waiting for tasks: %w", inner)
}

// TestDrainTimeoutAlertGate_SuppressesFirstDrainTimeout is the GH-4609
// acceptance contract: a single drain-timeout failure — which can be a
// legitimately long-running task crossing the wait window and often clears
// on the next retry — must not page an operator.
func TestDrainTimeoutAlertGate_SuppressesFirstDrainTimeout(t *testing.T) {
	gate := &drainTimeoutAlertGate{}

	if gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("first consecutive drain-timeout should not alert")
	}
}

// TestDrainTimeoutAlertGate_AlertsOnSecondConsecutiveDrainTimeout is the
// GH-4609 acceptance contract: the second consecutive drain-timeout failure
// must fire an alert — this closes the GH-72 incident where the drain
// looped "drain timeout" every 5 minutes for ~55 minutes with no alert ever
// firing.
func TestDrainTimeoutAlertGate_AlertsOnSecondConsecutiveDrainTimeout(t *testing.T) {
	gate := &drainTimeoutAlertGate{}

	if gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("first consecutive drain-timeout should not alert")
	}
	if !gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("second consecutive drain-timeout should alert")
	}
	// A third (and any further) consecutive occurrence should keep alerting.
	if !gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("third consecutive drain-timeout should also alert")
	}
}

// TestDrainTimeoutAlertGate_NonDrainTimeoutAlertsImmediately preserves the
// GH-4468 contract for every other upgrade failure (bad download, unwritable
// binary dir, restart failure, ...): those are not naturally transient the
// way a drain timeout is, so they must keep alerting on the very first
// occurrence, unchanged.
func TestDrainTimeoutAlertGate_NonDrainTimeoutAlertsImmediately(t *testing.T) {
	gate := &drainTimeoutAlertGate{}

	if !gate.observe(errors.New("binary directory /usr/local/bin is not writable")) {
		t.Fatal("a non-drain-timeout failure should alert immediately")
	}
}

// TestDrainTimeoutAlertGate_StreakResetsAfterNonDrainTimeoutFailure verifies
// an unrelated failure breaks the drain-timeout streak, so a subsequent
// drain-timeout is again treated as a fresh "first occurrence".
func TestDrainTimeoutAlertGate_StreakResetsAfterNonDrainTimeoutFailure(t *testing.T) {
	gate := &drainTimeoutAlertGate{}

	if gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("first consecutive drain-timeout should not alert")
	}
	if !gate.observe(errors.New("checksum mismatch")) {
		t.Fatal("unrelated failure should alert immediately and reset the streak")
	}
	if gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("drain-timeout streak should have reset after the unrelated failure")
	}
}

// TestDrainTimeoutAlertGate_SuccessResetsStreak verifies a successful
// upgrade attempt (nil err) clears any accumulated drain-timeout streak.
func TestDrainTimeoutAlertGate_SuccessResetsStreak(t *testing.T) {
	gate := &drainTimeoutAlertGate{}

	if gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("first consecutive drain-timeout should not alert")
	}
	if gate.observe(nil) {
		t.Fatal("a successful attempt should not itself alert")
	}
	if gate.observe(wrappedDrainTimeoutErr()) {
		t.Fatal("drain-timeout streak should have reset after a successful attempt")
	}
}

// TestDrainTimeoutAlertGate_EndToEndAlertDelivery wires the gate together
// with reportUpgradeFailure and a real alerts.Engine/Dispatcher, mirroring
// how cmd/pilot's hot-upgrade goroutine uses both: exactly one alert should
// reach the channel, on the second consecutive drain-timeout, not the first.
func TestDrainTimeoutAlertGate_EndToEndAlertDelivery(t *testing.T) {
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

	gate := &drainTimeoutAlertGate{}

	// 1st consecutive drain-timeout: must not alert.
	if gate.observe(wrappedDrainTimeoutErr()) {
		reportUpgradeFailure(engine, "v2.248.0", "v2.249.0", wrappedDrainTimeoutErr())
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(mockCh.getAlerts()); got != 0 {
		t.Fatalf("expected no alert after 1st consecutive drain-timeout, got %d", got)
	}

	// 2nd consecutive drain-timeout: must alert.
	if gate.observe(wrappedDrainTimeoutErr()) {
		reportUpgradeFailure(engine, "v2.248.0", "v2.249.0", wrappedDrainTimeoutErr())
	}
	waitForMockAlerts(t, mockCh, 1, 2*time.Second)
	got := mockCh.getAlerts()
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 alert after 2nd consecutive drain-timeout, got %d", len(got))
	}
	if got[0].Type != alerts.AlertTypeServiceUnhealthy {
		t.Errorf("alert type = %s, want %s", got[0].Type, alerts.AlertTypeServiceUnhealthy)
	}
}
