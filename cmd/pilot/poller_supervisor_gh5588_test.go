package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapterhealth"
)

// fakeSupervisorClock records Sleep durations and returns immediately;
// AfterFunc callbacks fire only when the test calls fireSettle.
type fakeSupervisorClock struct {
	mu     sync.Mutex
	sleeps []time.Duration
	timers []func()
}

func (c *fakeSupervisorClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *fakeSupervisorClock) AfterFunc(_ time.Duration, f func()) func() bool {
	c.mu.Lock()
	c.timers = append(c.timers, f)
	c.mu.Unlock()
	return func() bool { return true }
}

func (c *fakeSupervisorClock) fireSettle() {
	c.mu.Lock()
	f := c.timers[len(c.timers)-1]
	c.mu.Unlock()
	f()
}

func (c *fakeSupervisorClock) recordedSleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

type supervisorHarness struct {
	sup      *pollerSupervisor
	clock    *fakeSupervisorClock
	health   *adapterhealth.Registry
	mu       sync.Mutex
	calls    int
	alerts   []int
	clears   int
	upStates []bool
}

// newSupervisorHarness builds a supervisor around a Start that fails with
// err for the first failFor calls, then "runs" (settles up, blocks on ctx).
func newSupervisorHarness(failFor, alertAfter int, err error) *supervisorHarness {
	h := &supervisorHarness{clock: &fakeSupervisorClock{}, health: adapterhealth.NewRegistry()}
	h.sup = &pollerSupervisor{
		adapter:      "linear",
		instance:     "ws",
		clock:        h.clock,
		initialDelay: pollerRetryInitialDelay,
		maxDelay:     pollerRetryMaxDelay,
		settle:       pollerStartSettleTimeout,
		alertAfter:   alertAfter,
		setUp: func(up bool) {
			h.health.SetPollerUp("linear", "ws", up)
			h.mu.Lock()
			h.upStates = append(h.upStates, up)
			h.mu.Unlock()
		},
		onAlert: func(n int, _ error) {
			h.mu.Lock()
			h.alerts = append(h.alerts, n)
			h.mu.Unlock()
		},
		onClear: func() {
			h.mu.Lock()
			h.clears++
			h.mu.Unlock()
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h.sup.start = func(ctx context.Context) error {
		h.mu.Lock()
		h.calls++
		n := h.calls
		h.mu.Unlock()
		if n <= failFor {
			return err
		}
		h.clock.fireSettle()
		<-ctx.Done()
		return nil
	}
	return h
}

func (h *supervisorHarness) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPollerSupervisor_RetriesUntilRunning(t *testing.T) {
	h := newSupervisorHarness(2, 3, errors.New("cacheLabelIDs: rate limited"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sup.run(ctx); close(done) }()

	waitFor(t, func() bool { return h.health.PollerUpSnapshot()["linear"] })
	if got := h.callCount(); got != 3 {
		t.Fatalf("Start calls = %d, want 3 (fail, fail, run)", got)
	}
	want := []time.Duration{30 * time.Second, 60 * time.Second}
	if got := h.clock.recordedSleeps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff sleeps = %v, want %v", got, want)
	}

	cancel()
	<-done
	if h.health.PollerUpSnapshot()["linear"] {
		t.Fatal("poller must report down after supervisor exits")
	}
}

func TestPollerSupervisor_BackoffCapsAtMax(t *testing.T) {
	h := newSupervisorHarness(8, 100, errors.New("boom"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sup.run(ctx); close(done) }()
	waitFor(t, func() bool { return h.health.PollerUpSnapshot()["linear"] })
	cancel()
	<-done

	want := []time.Duration{
		30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute,
		8 * time.Minute, 10 * time.Minute, 10 * time.Minute, 10 * time.Minute,
	}
	if got := h.clock.recordedSleeps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("backoff sleeps = %v, want %v", got, want)
	}
}

func TestPollerSupervisor_AlertThreshold(t *testing.T) {
	tests := []struct {
		name       string
		failFor    int
		alertAfter int
		wantAlerts []int
		wantClears int
	}{
		{"below threshold: no alert", 2, 3, nil, 0},
		{"at threshold: alert once", 3, 3, []int{3}, 1},
		{"past threshold: still one alert", 5, 3, []int{3}, 1},
		{"custom threshold of 1", 1, 1, []int{1}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSupervisorHarness(tt.failFor, tt.alertAfter, errors.New("boom"))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { h.sup.run(ctx); close(done) }()
			waitFor(t, func() bool { return h.health.PollerUpSnapshot()["linear"] })
			cancel()
			<-done

			h.mu.Lock()
			defer h.mu.Unlock()
			if !reflect.DeepEqual(h.alerts, tt.wantAlerts) {
				t.Errorf("alerts = %v, want %v", h.alerts, tt.wantAlerts)
			}
			if h.clears != tt.wantClears {
				t.Errorf("clears = %d, want %d (recovery resolves a fired alert only)", h.clears, tt.wantClears)
			}
		})
	}
}

func TestPollerSupervisor_ExitsOnCleanReturnAndCancel(t *testing.T) {
	h := newSupervisorHarness(0, 3, nil)
	h.sup.start = func(context.Context) error { return nil }
	h.sup.run(context.Background()) // must return, not loop
	if h.health.PollerUpSnapshot()["linear"] {
		t.Fatal("clean exit must report down")
	}

	h2 := newSupervisorHarness(1000, 3, errors.New("boom"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h2.sup.run(ctx) // cancelled before the first attempt
	if got := h2.callCount(); got != 0 {
		t.Fatalf("Start called %d times after cancel, want 0", got)
	}
}

func TestPollerUpSnapshot_AllInstancesMustBeUp(t *testing.T) {
	r := adapterhealth.NewRegistry()
	r.SetPollerUp("linear", "a", true)
	r.SetPollerUp("linear", "b", false)
	r.SetPollerUp("jira", "jira", true)
	got := r.PollerUpSnapshot()
	if got["linear"] || !got["jira"] {
		t.Fatalf("snapshot = %v, want linear=false (one workspace down), jira=true", got)
	}
	r.SetPollerUp("linear", "b", true)
	if !r.PollerUpSnapshot()["linear"] {
		t.Fatal("linear should be up once every workspace is")
	}
}

func TestSuperviseStart_DefaultsAndNilSafe(t *testing.T) {
	d := &PollerDeps{} // no AdapterHealth, no AlertsEngine: must not panic
	sup := d.newPollerSupervisor("jira", "jira", func(context.Context) error { return nil })
	if sup.alertAfter != defaultPollerAlertAfter || sup.initialDelay != 30*time.Second || sup.maxDelay != 10*time.Minute {
		t.Fatalf("unexpected defaults: alertAfter=%d initial=%v max=%v", sup.alertAfter, sup.initialDelay, sup.maxDelay)
	}
	sup.setUp(true)
	sup.onAlert(3, errors.New("x"))
	sup.onClear()

	d.PollerAlertAfter = 7
	if got := d.newPollerSupervisor("jira", "jira", nil).alertAfter; got != 7 {
		t.Fatalf("alertAfter = %d, want 7", got)
	}
}
