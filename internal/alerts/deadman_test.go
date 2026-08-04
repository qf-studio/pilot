package alerts

import (
	"context"
	"testing"
	"time"
)

// =============================================================================
// DeadManTracker Tests (TASK-441 L2, GH-4709)
//
// These exercise the reusable primitive directly (not through a specific
// registration like intent-judge/label-lifecycle/self-review) — the seam-
// specific tests (TestSdkPreFlightJudge_*, cmd/pilot/poller_github_test.go)
// cover that the migration preserved GH-4669's exact externally-observable
// alert contract; these cover the primitive's own guarantees.
// =============================================================================

// TestDeadManTracker_StreakFiresOnceAtThreshold drives exactly `threshold`
// consecutive failures through a tracker and confirms exactly one alert
// fires — at the threshold-th failure, not before — and driving further
// failures past it does not fire again (no retry storm), mirroring
// TestSdkPreFlightJudge_FiresStreakAlertExactlyOnceAtThreshold's assertion
// for the pre-generalization bespoke counter.
func TestDeadManTracker_StreakFiresOnceAtThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		failures  int // total RecordFailure calls to drive
		wantFires int
	}{
		{name: "threshold-1 failures fires nothing", threshold: 5, failures: 4, wantFires: 0},
		{name: "exactly threshold failures fires once", threshold: 5, failures: 5, wantFires: 1},
		{name: "threshold+3 failures still fires only once", threshold: 5, failures: 8, wantFires: 1},
		{name: "threshold of 1 fires on first failure", threshold: 1, failures: 1, wantFires: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &AlertConfig{
				Enabled: true,
				Channels: []ChannelConfig{
					{Name: "test-channel", Type: "webhook", Enabled: true},
				},
				Rules: []AlertRule{
					{
						Name:     "self_review_failure_streak",
						Type:     AlertTypeSelfReviewFailureStreak,
						Enabled:  true,
						Severity: SeverityCritical,
						Channels: []string{"test-channel"},
						Cooldown: 0,
					},
				},
			}
			mockCh := newMockChannel("test-channel", "webhook")
			dispatcher := NewDispatcher(config)
			dispatcher.RegisterChannel(mockCh)
			engine := NewEngine(config, WithDispatcher(dispatcher))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_ = engine.Start(ctx)

			tracker := engine.RegisterDeadManTracker(
				"test-tracker-"+tt.name,
				AlertTypeSelfReviewFailureStreak,
				tt.threshold,
				DefaultDeadManWindow,
			)

			for i := 0; i < tt.failures; i++ {
				tracker.RecordFailure(nil)
			}

			if tt.wantFires > 0 {
				waitForAlerts(t, mockCh, tt.wantFires, 2*time.Second)
			}
			// Give any unexpected extra alert a moment to land before asserting count.
			time.Sleep(20 * time.Millisecond)
			got := mockCh.getAlerts()
			if len(got) != tt.wantFires {
				t.Fatalf("expected %d alert(s), got %d", tt.wantFires, len(got))
			}
			if tt.wantFires > 0 && got[0].Type != AlertTypeSelfReviewFailureStreak {
				t.Errorf("expected alert type %s, got %s", AlertTypeSelfReviewFailureStreak, got[0].Type)
			}
		})
	}
}

// TestDeadManTracker_SuccessResetsStreak verifies RecordSuccess zeroes the
// consecutive-failure streak, so failures before and after an intervening
// success don't compound toward the alert threshold.
func TestDeadManTracker_SuccessResetsStreak(t *testing.T) {
	tracker := NewDeadManTracker(nil, "test", AlertTypeSelfReviewFailureStreak, 5, DefaultDeadManWindow)

	for i := 0; i < 4; i++ {
		tracker.RecordFailure(nil)
	}
	if got := tracker.ConsecutiveFailures(); got != 4 {
		t.Fatalf("expected streak of 4, got %d", got)
	}

	tracker.RecordSuccess()
	if got := tracker.ConsecutiveFailures(); got != 0 {
		t.Fatalf("expected streak reset to 0 after success, got %d", got)
	}

	// A further run of failures short of threshold must not fire — the
	// pre-reset failures must not carry over.
	for i := 0; i < 4; i++ {
		tracker.RecordFailure(nil)
	}
	if got := tracker.ConsecutiveFailures(); got != 4 {
		t.Fatalf("expected post-reset streak of 4, got %d", got)
	}
}

// TestDeadManTracker_ZeroAttemptsDetection covers the half of the
// silent-death class a pure failure counter can never observe (GH-4687,
// GH-4702): a tracker nothing calls produces zero failures too, so Stale
// must report true from a fresh tracker (before any RecordAttempt), false
// once an attempt has landed within the window, and true again once the
// last attempt falls outside the window.
func TestDeadManTracker_ZeroAttemptsDetection(t *testing.T) {
	t.Run("fresh tracker with no attempts is stale", func(t *testing.T) {
		tracker := NewDeadManTracker(nil, "test", AlertTypeSelfReviewFailureStreak, 5, time.Hour)
		if !tracker.Stale(time.Now()) {
			t.Error("expected a tracker with zero attempts to be stale")
		}
	})

	t.Run("recent attempt within window is not stale", func(t *testing.T) {
		tracker := NewDeadManTracker(nil, "test", AlertTypeSelfReviewFailureStreak, 5, time.Hour)
		tracker.RecordAttempt()
		if tracker.Stale(time.Now()) {
			t.Error("expected a tracker with a recent attempt to not be stale")
		}
	})

	t.Run("attempt older than window is stale", func(t *testing.T) {
		tracker := NewDeadManTracker(nil, "test", AlertTypeSelfReviewFailureStreak, 5, time.Hour)
		tracker.RecordAttempt()
		if !tracker.Stale(time.Now().Add(2 * time.Hour)) {
			t.Error("expected a tracker whose last attempt is outside the window to be stale")
		}
	})

	t.Run("non-positive window disables elapsed-time staleness", func(t *testing.T) {
		tracker := NewDeadManTracker(nil, "test", AlertTypeSelfReviewFailureStreak, 5, 0)
		tracker.RecordAttempt()
		if tracker.Stale(time.Now().Add(365 * 24 * time.Hour)) {
			t.Error("expected a non-positive window to disable the elapsed-time check once an attempt exists")
		}
	})

	t.Run("attempts and successes count separately from failures", func(t *testing.T) {
		tracker := NewDeadManTracker(nil, "test", AlertTypeSelfReviewFailureStreak, 5, time.Hour)
		tracker.RecordAttempt()
		tracker.RecordAttempt()
		tracker.RecordSuccess()
		if got := tracker.Attempts(); got != 2 {
			t.Errorf("expected 2 attempts, got %d", got)
		}
		if got := tracker.Successes(); got != 1 {
			t.Errorf("expected 1 success, got %d", got)
		}
	})
}

// TestDeadManTracker_NilSafe verifies every counting method no-ops (rather
// than panicking) on a nil *DeadManTracker — the shape a lookup miss in
// namedDeadManTracker (an unregistered tracker name) returns to the relay
// handlers, and the same defensive shape used throughout this package for
// nil stores/engines.
func TestDeadManTracker_NilSafe(t *testing.T) {
	var tracker *DeadManTracker

	tracker.RecordAttempt()
	tracker.RecordSuccess()
	tracker.RecordFailure(map[string]string{"repo": "x"})

	if got := tracker.Attempts(); got != 0 {
		t.Errorf("expected 0 attempts from nil tracker, got %d", got)
	}
	if got := tracker.Successes(); got != 0 {
		t.Errorf("expected 0 successes from nil tracker, got %d", got)
	}
	if got := tracker.ConsecutiveFailures(); got != 0 {
		t.Errorf("expected 0 consecutive failures from nil tracker, got %d", got)
	}
	if !tracker.Stale(time.Now()) {
		t.Error("expected a nil tracker to report stale")
	}
	if got := tracker.Name(); got != "" {
		t.Errorf("expected empty name from nil tracker, got %q", got)
	}
}

// TestEngine_RegisterDeadManTracker_Memoizes verifies a second registration
// under the same name returns the original tracker (and its accumulated
// counters) rather than resetting it — the guarantee that lets a call site
// constructed fresh per request/repo/reconfigure (e.g.
// startGithubSDKPollerForRepo, called once per repo poller) share one set
// of counters.
func TestEngine_RegisterDeadManTracker_Memoizes(t *testing.T) {
	engine := NewEngine(DefaultConfig())

	first := engine.RegisterDeadManTracker("shared", AlertTypeSelfReviewFailureStreak, 5, DefaultDeadManWindow)
	first.RecordAttempt()
	first.RecordFailure(nil)

	second := engine.RegisterDeadManTracker("shared", AlertTypeSelfReviewFailureStreak, 5, DefaultDeadManWindow)

	if second != first {
		t.Fatal("expected the second registration under the same name to return the original tracker")
	}
	if got := second.Attempts(); got != 1 {
		t.Errorf("expected the memoized tracker to retain its attempt count, got %d", got)
	}
	if got := second.ConsecutiveFailures(); got != 1 {
		t.Errorf("expected the memoized tracker to retain its failure streak, got %d", got)
	}
}

// TestEngine_RegisterDeadManTracker_NilEngineSafe verifies a nil *Engine
// (no alerts engine wired — e.g. a repo with alerting disabled) returns a
// standalone tracker that still counts correctly; only alert delivery
// no-ops.
func TestEngine_RegisterDeadManTracker_NilEngineSafe(t *testing.T) {
	var engine *Engine

	tracker := engine.RegisterDeadManTracker("standalone", AlertTypeSelfReviewFailureStreak, 2, DefaultDeadManWindow)
	tracker.RecordAttempt()
	tracker.RecordFailure(nil)
	tracker.RecordFailure(nil) // reaches threshold; must not panic with a nil engine

	if got := tracker.ConsecutiveFailures(); got != 2 {
		t.Errorf("expected streak of 2, got %d", got)
	}
}

// TestEngine_DeadManEventRelay verifies the executor-relay path
// (EventTypeDeadManAttempt/Success/Failure, mirroring
// internal/executor/alerts.go's AlertEventTypeDeadMan* constants) routes to
// the correctly-named registered tracker, and that an event naming an
// unregistered tracker is a silent no-op rather than a panic.
func TestEngine_DeadManEventRelay(t *testing.T) {
	// Enabled: true — ProcessEvent/flushForTest are no-ops (and flushForTest
	// deadlocks: processEvents never starts) against a disabled engine like
	// DefaultConfig()'s.
	engine := NewEngine(&AlertConfig{Enabled: true})
	tracker := engine.RegisterDeadManTracker("relayed", AlertTypeSelfReviewFailureStreak, 3, DefaultDeadManWindow)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = engine.Start(ctx)

	engine.ProcessEvent(Event{
		Type:      EventTypeDeadManAttempt,
		Metadata:  map[string]string{"tracker": "relayed"},
		Timestamp: time.Now(),
	})
	engine.ProcessEvent(Event{
		Type:      EventTypeDeadManFailure,
		Metadata:  map[string]string{"tracker": "relayed"},
		Timestamp: time.Now(),
	})
	engine.flushForTest()

	if got := tracker.Attempts(); got != 1 {
		t.Errorf("expected 1 relayed attempt, got %d", got)
	}
	if got := tracker.ConsecutiveFailures(); got != 1 {
		t.Errorf("expected 1 relayed failure, got %d", got)
	}

	// An event naming an unregistered tracker must not panic.
	engine.ProcessEvent(Event{
		Type:      EventTypeDeadManSuccess,
		Metadata:  map[string]string{"tracker": "does-not-exist"},
		Timestamp: time.Now(),
	})
	engine.flushForTest()
}
