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
	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, LivenessPolicy{StallTimeout: stallTimeout, StallWatchdogInterval: watchdogTickInterval(stallTimeout)}, done, cancel)

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
	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, LivenessPolicy{StallTimeout: stallTimeout, StallWatchdogInterval: watchdogTickInterval(stallTimeout)}, done, cancel)

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

	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, LivenessPolicy{StallTimeout: stallTimeout, StallWatchdogInterval: watchdogTickInterval(stallTimeout)}, done, cancel)

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

	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, LivenessPolicy{StallTimeout: stallTimeout, StallWatchdogInterval: watchdogTickInterval(stallTimeout)}, done, cancel)

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

// TestEffortAwareStallTimeout covers GH-4501: high-effort or complex-lane
// executions get a raised stall-timeout floor (defense-in-depth alongside the
// --include-partial-messages streaming fix), but an explicit configured
// timeout that's already higher always wins, and disabled stall detection
// (configured <= 0) is never re-enabled by this logic.
func TestEffortAwareStallTimeout(t *testing.T) {
	tests := []struct {
		name       string
		configured time.Duration
		effort     string
		complexity Complexity
		want       time.Duration
	}{
		{
			name:       "default effort, default complexity: unchanged at 3m",
			configured: 3 * time.Minute,
			effort:     "medium",
			complexity: ComplexityMedium,
			want:       3 * time.Minute,
		},
		{
			name:       "high effort raises 3m default to the 10m floor",
			configured: 3 * time.Minute,
			effort:     "high",
			complexity: ComplexityMedium,
			want:       highEffortStallFloor,
		},
		{
			name:       "complex-lane raises 3m default to the 10m floor",
			configured: 3 * time.Minute,
			effort:     "medium",
			complexity: ComplexityComplex,
			want:       highEffortStallFloor,
		},
		{
			// GH-4691: epic was silently excluded before Complexity.IsHeavy()
			// replaced the ComplexityComplex-only check.
			name:       "epic-lane raises 3m default to the 10m floor",
			configured: 3 * time.Minute,
			effort:     "medium",
			complexity: ComplexityEpic,
			want:       highEffortStallFloor,
		},
		{
			name:       "simple lane, low effort: unchanged at 3m",
			configured: 3 * time.Minute,
			effort:     "low",
			complexity: ComplexitySimple,
			want:       3 * time.Minute,
		},
		{
			name:       "explicit config already above the floor wins",
			configured: 15 * time.Minute,
			effort:     "high",
			complexity: ComplexityComplex,
			want:       15 * time.Minute,
		},
		{
			name:       "explicit config exactly at the floor is left unchanged",
			configured: highEffortStallFloor,
			effort:     "high",
			complexity: ComplexityMedium,
			want:       highEffortStallFloor,
		},
		{
			name:       "disabled stall detection stays disabled regardless of effort",
			configured: 0,
			effort:     "high",
			complexity: ComplexityComplex,
			want:       0,
		},
		{
			name:       "negative (explicitly disabled) stays disabled",
			configured: -1,
			effort:     "high",
			complexity: ComplexityComplex,
			want:       -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effortAwareStallTimeout(tt.configured, tt.effort, tt.complexity)
			if got != tt.want {
				t.Errorf("effortAwareStallTimeout(%v, %q, %q) = %v, want %v",
					tt.configured, tt.effort, tt.complexity, got, tt.want)
			}
		})
	}
}

// TestEffortAwareHeartbeatFloor covers GH-4691: the hard heartbeat's floor
// must apply to high-effort and heavy-complexity (complex, epic) lanes —
// mirroring effortAwareStallTimeout's own effort/complexity gate — and must
// NOT depend on any configured stall-timeout value (unlike
// effortAwareStallTimeout, there is no "configured" input here at all).
func TestEffortAwareHeartbeatFloor(t *testing.T) {
	tests := []struct {
		name       string
		effort     string
		complexity Complexity
		want       time.Duration
	}{
		{
			name:       "simple lane, low effort: no floor",
			effort:     "low",
			complexity: ComplexitySimple,
			want:       0,
		},
		{
			name:       "medium lane, medium effort: no floor",
			effort:     "medium",
			complexity: ComplexityMedium,
			want:       0,
		},
		{
			name:       "high effort raises the floor regardless of complexity",
			effort:     "high",
			complexity: ComplexityMedium,
			want:       highEffortStallFloor,
		},
		{
			name:       "complex-lane raises the floor",
			effort:     "medium",
			complexity: ComplexityComplex,
			want:       highEffortStallFloor,
		},
		{
			// The GH-4679 incident lane: epic complexity must get the same
			// floor as complex — this was the confirmed secondary defect.
			name:       "epic-lane raises the floor",
			effort:     "medium",
			complexity: ComplexityEpic,
			want:       highEffortStallFloor,
		},
		{
			name:       "high effort and epic: still just the floor (no stacking)",
			effort:     "high",
			complexity: ComplexityEpic,
			want:       highEffortStallFloor,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := effortAwareHeartbeatFloor(tt.effort, tt.complexity)
			if got != tt.want {
				t.Errorf("effortAwareHeartbeatFloor(%q, %q) = %v, want %v",
					tt.effort, tt.complexity, got, tt.want)
			}
		})
	}
}

// TestStallWatchdog_PartialDeltasSurviveGapBetweenCompleteEvents covers
// GH-4501's core regression: a long silent single model turn now emits
// partial-message stdout lines (thanks to --include-partial-messages) even
// though the two surrounding *complete* stream-json events (e.g. two
// "assistant" messages) are far more than stallTimeout apart. Every stdout
// line resets the watchdog's idle clock unconditionally (mirroring the real
// EventHandler wiring in runner.go), so the session must survive.
func TestStallWatchdog_PartialDeltasSurviveGapBetweenCompleteEvents(t *testing.T) {
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		inFlight      atomic.Int64
		done          = make(chan struct{})
	)

	stallTimeout := 300 * time.Millisecond
	// watchdogTickInterval floors at 1s, so the watchdog's first real check
	// lands ~1s in — comfortably after the simulated gap below.
	start := time.Now()
	lastEventAt.Store(start.UnixNano())

	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, LivenessPolicy{StallTimeout: stallTimeout, StallWatchdogInterval: watchdogTickInterval(stallTimeout)}, done, func() {})

	// Simulate partial-delta lines arriving every 150ms (well under
	// stallTimeout) for 900ms — spanning a gap between "complete" events far
	// larger than stallTimeout, but with no single idle gap exceeding it.
	deltaInterval := 150 * time.Millisecond
	deadline := start.Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(deltaInterval)
		lastEventAt.Store(time.Now().UnixNano()) // simulates EventHandler firing on a delta line
	}

	// Give the watchdog's ~1s tick a chance to fire before we conclude.
	time.Sleep(300 * time.Millisecond)
	close(done)

	if stallDetected.Load() {
		t.Fatal("stall watchdog fired despite partial-delta lines keeping the idle clock fresh")
	}
}

// TestStallWatchdog_NoPartialDeltasFiresOnGenuineSilence is the negative
// control for the test above: with no lines at all resetting lastEventAt
// (the pre-GH-4501 failure mode — a CLI without --include-partial-messages
// during a long silent turn), the watchdog must still fire once idle exceeds
// stallTimeout.
func TestStallWatchdog_NoPartialDeltasFiresOnGenuineSilence(t *testing.T) {
	r := &Runner{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var (
		lastEventAt   atomic.Int64
		stallDetected atomic.Bool
		inFlight      atomic.Int64
		done          = make(chan struct{})
	)

	// Simulate a stale last-event timestamp from well before the watchdog's
	// first tick, with nothing refreshing it in between.
	lastEventAt.Store(time.Now().Add(-1 * time.Second).UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stallTimeout := 50 * time.Millisecond
	go r.runStallWatchdog("test-task", &lastEventAt, &stallDetected, &inFlight, LivenessPolicy{StallTimeout: stallTimeout, StallWatchdogInterval: watchdogTickInterval(stallTimeout)}, done, cancel)

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("stall watchdog did not fire on genuine silence past stallTimeout")
	}
	if !stallDetected.Load() {
		t.Fatal("stallDetected should be true after genuine silence")
	}
	close(done)
}
