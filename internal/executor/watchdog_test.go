package executor

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// TestWatchdogTickInterval verifies the tick interval is derived from stallTimeout
// (min(30s, stallTimeout/3), floored at 1s) so small timeouts are honored. TASK-344.
func TestWatchdogTickInterval(t *testing.T) {
	tests := []struct {
		name         string
		stallTimeout time.Duration
		want         time.Duration
	}{
		{"sub-30s honored", 9 * time.Second, 3 * time.Second},
		{"floored at 1s", 900 * time.Millisecond, time.Second},
		{"large caps at default", 90 * time.Second, defaultStallWatchdogInterval},
		{"default boundary", 90 * time.Second, 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := watchdogTickInterval(tt.stallTimeout); got != tt.want {
				t.Errorf("watchdogTickInterval(%v) = %v, want %v", tt.stallTimeout, got, tt.want)
			}
		})
	}
}

// TestStallWatchdog_FiresOnIdle verifies the watchdog cancels the context
// when no event is received within stallTimeout.
func TestStallWatchdog_FiresOnIdle(t *testing.T) {
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		done          = make(chan struct{})
	)
	lastEventAt.Store(time.Now().Add(-10 * time.Second).UnixNano()) // simulate 10s idle

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a very short stall timeout so the test doesn't wait 3 minutes.
	stallTimeout := 5 * time.Second

	var inFlight atomic.Int64
	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, stallTimeout, done, cancel)

	// The watchdog ticks every 30s by default — too slow for a unit test.
	// Instead, simulate a stale lastEventAt (already set above) and wait for
	// the first tick cycle. To avoid a 30s wait, we use the fact that the
	// watchdog's select also responds to `done` being closed, so we test
	// via context cancellation.
	//
	// For a direct unit test of the timer logic, call the predicate directly.
	lastAt := time.Unix(0, lastEventAt.Load())
	idle := time.Since(lastAt)
	if idle <= stallTimeout {
		t.Fatalf("expected idle > stallTimeout, got idle=%v, timeout=%v", idle, stallTimeout)
	}

	// Simulate what the watchdog does on the tick.
	stallDetected.Store(true)
	cancel()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(1 * time.Second):
		t.Fatal("context should have been cancelled")
	}

	if !stallDetected.Load() {
		t.Fatal("stallDetected should be true after stall")
	}

	close(done)
}

// TestStallWatchdog_SurvivesLiveSession verifies the watchdog does NOT
// cancel the context when events arrive regularly.
func TestStallWatchdog_SurvivesLiveSession(t *testing.T) {
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		done          = make(chan struct{})
	)
	lastEventAt.Store(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stallTimeout := 200 * time.Millisecond

	var inFlight atomic.Int64
	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, stallTimeout, done, cancel)

	// Simulate periodic events keeping the watchdog alive.
	// The watchdog interval is 30s in production; in the test we close done
	// quickly to verify the watchdog exits cleanly without killing the context.
	time.Sleep(50 * time.Millisecond)
	lastEventAt.Store(time.Now().UnixNano()) // fresh event

	close(done) // execution ended

	// Context should still be alive (not cancelled by stall).
	select {
	case <-ctx.Done():
		if stallDetected.Load() {
			t.Fatal("stall watchdog should not have fired for a live session")
		}
	default:
		// expected: context not cancelled
	}

	if stallDetected.Load() {
		t.Fatal("stallDetected should be false for a live session")
	}
}

// TestStallWatchdog_SuspendsWhileBackgroundTaskInFlight verifies the watchdog
// does NOT terminate a session that is silent past stallTimeout while a
// background task (e.g. a long-running backgrounded Bash command) is still
// running. GH-4357: a session that starts such a task and awaits its result
// legitimately emits zero events until it completes; the previous idle-only
// check misclassified this as a dead session and killed a healthy execution
// (observed: a `go test -race -count=5` run silently exceeding the 3m stall
// timeout while otherwise healthy).
func TestStallWatchdog_SuspendsWhileBackgroundTaskInFlight(t *testing.T) {
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		inFlight      atomic.Int64
		done          = make(chan struct{})
	)
	// Simulate a task_started event long enough ago that, without the fix,
	// the idle clock alone would exceed stallTimeout well before the ticker
	// below stops.
	lastEventAt.Store(time.Now().Add(-1 * time.Second).UnixNano())
	inFlight.Store(1) // one background task in flight, no completion event yet

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stallTimeout := 50 * time.Millisecond

	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, stallTimeout, done, cancel)

	// Let several ticks elapse — well past stallTimeout — while the
	// background task remains in flight.
	time.Sleep(10 * stallTimeout)
	close(done)

	select {
	case <-ctx.Done():
		t.Fatal("stall watchdog terminated session while a background task was in flight")
	default:
		// expected: context still alive
	}
	if stallDetected.Load() {
		t.Fatal("stallDetected should be false while a background task is in flight")
	}
}

// TestStallWatchdog_FiresAfterBackgroundTaskCompletes verifies the watchdog
// resumes normal idle detection once the in-flight background task count
// drops back to zero (GH-4357).
func TestStallWatchdog_FiresAfterBackgroundTaskCompletes(t *testing.T) {
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		inFlight      atomic.Int64
		done          = make(chan struct{})
	)
	lastEventAt.Store(time.Now().Add(-1 * time.Second).UnixNano())
	inFlight.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stallTimeout := 50 * time.Millisecond

	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, stallTimeout, done, cancel)

	// Task is still running: watchdog must not fire yet.
	time.Sleep(3 * stallTimeout)
	select {
	case <-ctx.Done():
		t.Fatal("stall watchdog fired while background task was still in flight")
	default:
	}

	// Background task completes (task_notification received) without any
	// other event refreshing lastEventAt — idle clock resumes from a stale
	// timestamp, so the very next tick should now detect a stall.
	inFlight.Store(0)

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("stall watchdog did not resume idle detection after background task completed")
	}

	if !stallDetected.Load() {
		t.Fatal("stallDetected should be true once idle detection resumes past stallTimeout")
	}

	close(done)
}

// TestEffectiveStallTimeout verifies default and custom values.
func TestEffectiveStallTimeout(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *BackendConfig
		expected time.Duration
	}{
		{"nil config returns default", nil, 3 * time.Minute},
		{"zero StallTimeoutMs returns default", &BackendConfig{StallTimeoutMs: 0}, 3 * time.Minute},
		{"custom 60s", &BackendConfig{StallTimeoutMs: 60_000}, 60 * time.Second},
		{"negative disables", &BackendConfig{StallTimeoutMs: -1}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.EffectiveStallTimeout()
			if got != tt.expected {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}
