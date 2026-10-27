package executor

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakePauseClock is a manually advanced pauseClock.
type fakePauseClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakePauseTimer
}

type fakePauseTimer struct {
	clock   *fakePauseClock
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakePauseClock(now time.Time) *fakePauseClock { return &fakePauseClock{now: now} }

func (c *fakePauseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakePauseClock) AfterFunc(d time.Duration, f func()) pauseTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakePauseTimer{clock: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakePauseTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

// Advance moves the clock forward and fires every due, un-stopped timer in
// order, outside the clock lock.
func (c *fakePauseClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakePauseTimer
	for _, t := range c.timers {
		if !t.stopped && !t.fired && !t.at.After(c.now) {
			t.fired = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.f()
	}
}

// usageLimitAlerts records alert events with a lock (the dispatcher emits from
// a worker goroutine).
type usageLimitAlerts struct {
	mu     sync.Mutex
	events []AlertEvent
}

func (a *usageLimitAlerts) ProcessEvent(e AlertEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
}

func (a *usageLimitAlerts) pauseEvents() []AlertEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []AlertEvent
	for _, e := range a.events {
		if e.Type == AlertEventTypeUsageLimitPause {
			out = append(out, e)
		}
	}
	return out
}

// perProjectBackend blocks each Execute on its project's channel and returns
// the received text as a failure error, or success when the text is empty.
type perProjectBackend struct {
	mu   sync.Mutex
	gate map[string]chan string
}

func newPerProjectBackend(projects ...string) *perProjectBackend {
	b := &perProjectBackend{gate: make(map[string]chan string)}
	for _, p := range projects {
		b.gate[p] = make(chan string, 1)
	}
	return b
}

func (b *perProjectBackend) Name() string      { return "mock-per-project" }
func (b *perProjectBackend) IsAvailable() bool { return true }
func (b *perProjectBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	ch := b.gate[opts.ProjectPath]
	b.mu.Unlock()
	select {
	case msg := <-ch:
		if msg == "" {
			return &BackendResult{Success: true, Output: "ok"}, nil
		}
		return &BackendResult{Success: false, Error: msg}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// finish lets the run blocked (or about to block) on project end with msg.
func (b *perProjectBackend) finish(project, msg string) { b.gate[project] <- msg }

func newUsageLimitDispatcher(t *testing.T, backend Backend, pause *UsageLimitPauseConfig, clock pauseClock) (*Dispatcher, *usageLimitAlerts, func()) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true, UsageLimitPause: pause}
	alerts := &usageLimitAlerts{}
	runner.SetAlertProcessor(alerts)

	d := NewDispatcher(store, runner, nil)
	d.clock = clock
	if err := d.Start(context.Background()); err != nil {
		cleanup()
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	return d, alerts, func() {
		d.Stop()
		cleanup()
	}
}

var usageLimitTestNow = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

func TestDispatcher_UsageLimitPausesAdmission(t *testing.T) {
	const projA, projB = "/tmp/pilot-gh5862-a", "/tmp/pilot-gh5862-b"
	backend := newPerProjectBackend(projA, projB)
	clock := newFakePauseClock(usageLimitTestNow)
	d, alertSink, cleanup := newUsageLimitDispatcher(t, backend, nil, clock)
	defer cleanup()
	ctx := context.Background()

	// Two projects so the second task can wait queued behind the pause even
	// though its own worker is idle.
	execA, err := d.QueueTask(ctx, &Task{ID: "GH-5001", Title: "A", ProjectPath: projA})
	if err != nil {
		t.Fatalf("queue A: %v", err)
	}
	waitForExecStatus(t, d.store, execA, "running", 2*time.Second)

	backend.finish(projA, "You've hit your limit · resets 2pm (UTC)")
	waitForExecStatus(t, d.store, execA, "rate_limited", 2*time.Second)

	st := d.AdmissionStatus()
	wantResume := time.Date(2026, 10, 26, 14, 5, 0, 0, time.UTC) // reset + RetryBuffer
	if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerUsageLimit {
		t.Fatalf("status = %+v, want paused by exactly [%s]", st, AdmissionPauseOwnerUsageLimit)
	}
	if !st.UsageLimitResumesAt.Equal(wantResume) {
		t.Fatalf("resumes at %v, want %v", st.UsageLimitResumesAt, wantResume)
	}
	if got := alertSink.pauseEvents(); len(got) != 1 || got[0].TaskID != "GH-5001" {
		t.Fatalf("alerts = %+v, want exactly one usage_limit_pause for GH-5001", got)
	}

	// The next queued task must not start while paused.
	execB, err := d.QueueTask(ctx, &Task{ID: "GH-5002", Title: "B", ProjectPath: projB})
	if err != nil {
		t.Fatalf("queue B: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	waitForExecStatus(t, d.store, execB, "queued", time.Second)

	// One tick before the resume instant: still paused.
	clock.Advance(4*time.Hour + 4*time.Minute + 59*time.Second)
	if !d.IsAdmissionPaused() {
		t.Fatal("admission resumed before reset + RetryBuffer")
	}

	clock.Advance(time.Second)
	if d.IsAdmissionPaused() {
		t.Fatal("admission still paused at reset + RetryBuffer")
	}
	if st := d.AdmissionStatus(); st.Paused || !st.UsageLimitResumesAt.IsZero() {
		t.Fatalf("status after resume = %+v, want cleared", st)
	}
	backend.finish(projB, "")
	waitForExecStatus(t, d.store, execB, "completed", 2*time.Second)
}

func TestDispatcher_UsageLimitPauseExtendsOnSecondRateLimit(t *testing.T) {
	const projA, projC = "/tmp/pilot-gh5862-ext-a", "/tmp/pilot-gh5862-ext-c"
	backend := newPerProjectBackend(projA, projC)
	clock := newFakePauseClock(usageLimitTestNow)
	d, alertSink, cleanup := newUsageLimitDispatcher(t, backend, nil, clock)
	defer cleanup()
	ctx := context.Background()

	execA, _ := d.QueueTask(ctx, &Task{ID: "GH-5011", Title: "A", ProjectPath: projA})
	execC, _ := d.QueueTask(ctx, &Task{ID: "GH-5012", Title: "C", ProjectPath: projC})
	waitForExecStatus(t, d.store, execA, "running", 2*time.Second)
	waitForExecStatus(t, d.store, execC, "running", 2*time.Second)

	backend.finish(projA, "You've hit your limit · resets 2pm (UTC)")
	waitForExecStatus(t, d.store, execA, "rate_limited", 2*time.Second)
	first := d.AdmissionStatus().UsageLimitResumesAt

	// C was already running when the pause began, so it finishes during it.
	backend.finish(projC, "You've hit your limit · resets 3pm (UTC)")
	waitForExecStatus(t, d.store, execC, "rate_limited", 2*time.Second)

	st := d.AdmissionStatus()
	if len(st.Owners) != 1 || st.Owners[0] != AdmissionPauseOwnerUsageLimit {
		t.Fatalf("owners = %v, want a single usage-limit owner", st.Owners)
	}
	wantResume := time.Date(2026, 10, 26, 15, 5, 0, 0, time.UTC)
	if !st.UsageLimitResumesAt.After(first) || !st.UsageLimitResumesAt.Equal(wantResume) {
		t.Fatalf("resumes at %v (first %v), want extended to %v", st.UsageLimitResumesAt, first, wantResume)
	}
	if got := alertSink.pauseEvents(); len(got) != 1 {
		t.Fatalf("got %d pause alerts, want 1 (extension must not re-alert)", len(got))
	}

	// The first (superseded) resume instant passes without releasing.
	clock.Advance(4*time.Hour + 5*time.Minute)
	if !d.IsAdmissionPaused() {
		t.Fatal("admission resumed at the superseded resume time")
	}
	clock.Advance(time.Hour)
	if d.IsAdmissionPaused() {
		t.Fatal("admission still paused at the extended resume time")
	}
}

// A shorter second reset must not pull the resume time earlier.
func TestDispatcher_UsageLimitPauseNeverShortened(t *testing.T) {
	clock := newFakePauseClock(usageLimitTestNow)
	d, _, cleanup := newUsageLimitDispatcher(t, newPerProjectBackend(), nil, clock)
	defer cleanup()

	d.armUsageLimitPause("GH-1", "/p", "You've hit your limit · resets 3pm (UTC)")
	d.armUsageLimitPause("GH-2", "/p", "You've hit your limit · resets 2pm (UTC)")

	want := time.Date(2026, 10, 26, 15, 5, 0, 0, time.UTC)
	if got := d.AdmissionStatus().UsageLimitResumesAt; !got.Equal(want) {
		t.Fatalf("resumes at %v, want %v", got, want)
	}
}

func TestDispatcher_UsageLimitPauseDisabled(t *testing.T) {
	off := false
	clock := newFakePauseClock(usageLimitTestNow)
	d, alertSink, cleanup := newUsageLimitDispatcher(t, newPerProjectBackend(), &UsageLimitPauseConfig{Enabled: &off}, clock)
	defer cleanup()

	d.armUsageLimitPause("GH-1", "/p", "You've hit your limit · resets 2pm (UTC)")

	if d.IsAdmissionPaused() {
		t.Fatal("admission paused with usage_limit_pause disabled")
	}
	if len(alertSink.pauseEvents()) != 0 {
		t.Fatal("alert emitted with usage_limit_pause disabled")
	}
}

func TestDispatcher_UsageLimitPauseFallbackWhenNoResetParses(t *testing.T) {
	clock := newFakePauseClock(usageLimitTestNow)
	d, _, cleanup := newUsageLimitDispatcher(t, newPerProjectBackend(), &UsageLimitPauseConfig{FallbackPause: 10 * time.Minute}, clock)
	defer cleanup()

	d.armUsageLimitPause("GH-1", "/p", "Claude usage limit reached")

	if got, want := d.AdmissionStatus().UsageLimitResumesAt, usageLimitTestNow.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("resumes at %v, want fallback %v", got, want)
	}
	clock.Advance(10*time.Minute - time.Second)
	if !d.IsAdmissionPaused() {
		t.Fatal("resumed before fallback_pause elapsed")
	}
	clock.Advance(time.Second)
	if d.IsAdmissionPaused() {
		t.Fatal("still paused after fallback_pause elapsed")
	}
}

func TestDispatcher_UsageLimitPauseDefaultFallback(t *testing.T) {
	clock := newFakePauseClock(usageLimitTestNow)
	d, _, cleanup := newUsageLimitDispatcher(t, newPerProjectBackend(), nil, clock)
	defer cleanup()

	d.armUsageLimitPause("GH-1", "/p", "Claude usage limit reached")

	if got, want := d.AdmissionStatus().UsageLimitResumesAt, usageLimitTestNow.Add(DefaultUsageLimitFallbackPause); !got.Equal(want) {
		t.Fatalf("resumes at %v, want default fallback %v", got, want)
	}
}

func TestDispatcher_UsageLimitPauseCappedByMaxPause(t *testing.T) {
	clock := newFakePauseClock(usageLimitTestNow)
	d, _, cleanup := newUsageLimitDispatcher(t, newPerProjectBackend(), &UsageLimitPauseConfig{MaxPause: 2 * time.Hour}, clock)
	defer cleanup()

	// 10:00 now, "resets 7am" is 3h past recentResetWindow so it is tomorrow
	// 07:00 — 21h away, far past max_pause.
	d.armUsageLimitPause("GH-1", "/p", "You've hit your limit · resets 7am (UTC)")

	if got, want := d.AdmissionStatus().UsageLimitResumesAt, usageLimitTestNow.Add(2*time.Hour); !got.Equal(want) {
		t.Fatalf("resumes at %v, want capped %v", got, want)
	}
}

func TestDispatcher_UsageLimitPauseCoexistsWithOtherOwners(t *testing.T) {
	clock := newFakePauseClock(usageLimitTestNow)
	d, _, cleanup := newUsageLimitDispatcher(t, newPerProjectBackend(), nil, clock)
	defer cleanup()

	d.PauseAdmissionFor("platform-breaker")
	d.armUsageLimitPause("GH-1", "/p", "Claude usage limit reached")
	clock.Advance(DefaultUsageLimitFallbackPause)

	st := d.AdmissionStatus()
	if !st.Paused || len(st.Owners) != 1 || st.Owners[0] != "platform-breaker" {
		t.Fatalf("status = %+v, want only platform-breaker left holding the pause", st)
	}
	if !st.UsageLimitResumesAt.IsZero() {
		t.Fatalf("resumes_at = %v, want zero once usage-limit owner released", st.UsageLimitResumesAt)
	}
}
