package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapterhealth"
	"github.com/qf-studio/pilot/internal/config"
)

func TestStartAdapterPollers_OnlyStartsEnabled(t *testing.T) {
	var started []string

	registrations := []PollerRegistration{
		{
			Name:    "enabled-adapter",
			Enabled: func(_ *config.Config) bool { return true },
			CreateAndStart: func(_ context.Context, _ *PollerDeps) {
				started = append(started, "enabled-adapter")
			},
		},
		{
			Name:    "disabled-adapter",
			Enabled: func(_ *config.Config) bool { return false },
			CreateAndStart: func(_ context.Context, _ *PollerDeps) {
				started = append(started, "disabled-adapter")
			},
		},
		{
			Name:    "another-enabled",
			Enabled: func(_ *config.Config) bool { return true },
			CreateAndStart: func(_ context.Context, _ *PollerDeps) {
				started = append(started, "another-enabled")
			},
		},
	}

	deps := &PollerDeps{Cfg: &config.Config{}}
	StartAdapterPollers(context.Background(), deps, registrations)

	if len(started) != 2 {
		t.Fatalf("expected 2 pollers started, got %d", len(started))
	}
	if started[0] != "enabled-adapter" {
		t.Errorf("expected first started = enabled-adapter, got %s", started[0])
	}
	if started[1] != "another-enabled" {
		t.Errorf("expected second started = another-enabled, got %s", started[1])
	}
}

func TestAdapterPollerRegistrations_ReturnsAllFive(t *testing.T) {
	regs := adapterPollerRegistrations()
	if len(regs) != 8 {
		t.Fatalf("expected 8 registrations, got %d", len(regs))
	}

	expected := []string{"linear", "jira", "asana", "azuredevops", "plane", "discord", "gitlab", "github"}
	for i, name := range expected {
		if regs[i].Name != name {
			t.Errorf("registration[%d]: expected name %q, got %q", i, name, regs[i].Name)
		}
	}
}

// TestSafeAdapterGo_PanicIsRecoveredAndOthersKeepRunning is the GH-4314
// acceptance test: a panicking fake adapter must not crash the daemon, and
// every other adapter goroutine started via SafeAdapterGo must keep running
// unaffected — mirroring the 2026-07-14 incident where a nil-conn deref in
// the Discord gateway's listen loop took down github + telegram execution
// mid-flight.
func TestSafeAdapterGo_PanicIsRecoveredAndOthersKeepRunning(t *testing.T) {
	deps := &PollerDeps{
		Cfg:           &config.Config{},
		AdapterHealth: adapterhealth.NewRegistry(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var healthyTicks atomic.Int32
	deps.SafeAdapterGo(ctx, "healthy-adapter", func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				healthyTicks.Add(1)
				time.Sleep(time.Millisecond)
			}
		}
	})

	deps.SafeAdapterGo(ctx, "panicking-adapter", func() {
		panic("simulated nil-conn deref")
	})

	// If the panic weren't recovered, the whole test process would crash
	// here rather than reaching this assertion.
	deadline := time.After(2 * time.Second)
	for {
		snap := deps.AdapterHealth.Snapshot()
		var panickingSeen bool
		for _, st := range snap {
			if st.Name == "panicking-adapter" && st.LastError != "" {
				panickingSeen = true
			}
		}
		if panickingSeen {
			break
		}
		select {
		case <-deadline:
			t.Fatal("expected panicking-adapter's panic to be recorded")
		case <-time.After(time.Millisecond):
		}
	}

	if healthyTicks.Load() == 0 {
		t.Fatal("expected healthy-adapter to keep running after the other adapter panicked")
	}
}

// TestSafeAdapterGo_FiresAlertOnPanic confirms a recovered adapter panic
// raises a WARN service_unhealthy alert (GH-4314) — an adapter going down
// is an ops signal, not a silent degrade.
func TestSafeAdapterGo_FiresAlertOnPanic(t *testing.T) {
	engine, ch := newTestAlertsEngine(t)
	deps := &PollerDeps{
		Cfg:           &config.Config{},
		AlertsEngine:  engine,
		AdapterHealth: adapterhealth.NewRegistry(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.SafeAdapterGo(ctx, "flaky", func() {
		panic("boom")
	})

	deadline := time.After(2 * time.Second)
	for ch.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected an alert to be fired when an adapter panics")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestSafeAdapterGo_NilRegistryFallsBackSafely confirms SafeAdapterGo still
// recovers panics (via logging.SafeGo) when AdapterHealth is nil, so callers
// that don't wire it up (e.g. older tests) don't crash the process either.
func TestSafeAdapterGo_NilRegistryFallsBackSafely(t *testing.T) {
	deps := &PollerDeps{Cfg: &config.Config{}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	deps.SafeAdapterGo(ctx, "no-registry", func() {
		defer close(done)
		panic("boom")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected fn to run (and its panic to be recovered) without crashing the test")
	}
}

func TestPollerRegistrations_EnabledChecksConfig(t *testing.T) {
	regs := adapterPollerRegistrations()
	// Config with initialized but empty Adapters
	emptyCfg := &config.Config{
		Adapters: &config.AdaptersConfig{},
	}

	for _, reg := range regs {
		if reg.Enabled(emptyCfg) {
			t.Errorf("registration %q should be disabled with empty config", reg.Name)
		}
	}
}
