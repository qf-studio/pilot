package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/testutil"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestPlatformBreakerMonitorTick_RedrivesOnObservePathClose covers GH-4807
// acceptance criterion 2: a breaker close that happens inside
// PlatformBreaker.Observe (a CI-failure observation landing after the quiet
// deadline — part 1's TestPlatformBreaker_ClosesAfterQuietPeriod shape,
// mirrored here with a short real quiet period + sleep instead of a fake
// clock, since PlatformBreaker's clock hook isn't exported outside package
// autopilot) must still get its held PRs re-driven.
//
// Before the fix, startPlatformBreakerMonitor's ticker loop gated on
// `if !breaker.IsOpen() { continue }` BEFORE ever calling EvaluateClose: once
// Observe had already closed the breaker between ticks (and
// alertPlatformBreakerTransition had already resumed admission + fired the
// close alert for it, from inside whichever controller's handleCIFailed
// observed it), the monitor's next tick found the breaker already closed and
// skipped entirely — so ReDriveBreakerHeldPRs, whose only caller is this
// monitor, never ran for that transition. Held PRs stayed parked until the
// re-adopt cap or a later episode that happened to close via the monitor's
// own EvaluateClose instead.
func TestPlatformBreakerMonitorTick_RedrivesOnObservePathClose(t *testing.T) {
	breaker := autopilot.NewPlatformBreaker(3, 15*time.Minute, 5*time.Millisecond, nil)

	breaker.Observe(1, "owner/repo", autopilot.FailureClassInfra)
	breaker.Observe(2, "owner/repo", autopilot.FailureClassInfra)
	opened := breaker.Observe(3, "owner/repo", autopilot.FailureClassInfra)
	if !opened.Open {
		t.Fatal("test setup: breaker should be open after 3 correlated observations")
	}

	time.Sleep(10 * time.Millisecond) // exceed the 5ms quiet period

	// Mirrors handleCIFailed's Observe call for some OTHER, code-classified
	// failure elsewhere: not itself correlation evidence, but it still runs
	// Observe's lazy time-based close check and closes the breaker right
	// here — the Observe path, not the monitor's own EvaluateClose.
	closeResult := breaker.Observe(4, "owner/repo", autopilot.FailureClassCode)
	if !closeResult.JustClosed {
		t.Fatal("test setup: breaker should have closed via the Observe path")
	}
	if breaker.IsOpen() {
		t.Fatal("test setup: breaker should be closed after the Observe-path transition")
	}

	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	ctrl := autopilot.NewController(autopilot.DefaultConfig(), ghClient, nil, "owner", "repo", autopilot.WithPlatformBreaker(breaker))

	// Park a held PR the way handleCIFailed would have while the breaker was
	// open, via the store + RestoreState round trip (GH-4807 criterion 1),
	// rather than reaching into the controller's unexported activePRs map.
	store, err := autopilot.NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	held := &autopilot.PRState{
		PRNumber:          77,
		IssueNumber:       77,
		Stage:             autopilot.StageFailed,
		BreakerHoldActive: true,
	}
	if err := store.SavePRState("owner/repo", held); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	ctrl.SetStateStore(store)
	if _, err := ctrl.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if _, ok := ctrl.GetPRState(77); !ok {
		t.Fatal("test setup: held PR 77 should have been rehydrated by RestoreState")
	}

	controllers := map[string]*autopilot.Controller{"owner/repo": ctrl}

	// wasOpen=true mirrors the monitor's previous tick having last observed
	// the breaker open, before Observe closed it out from under the ticker
	// between ticks. alertsEngine=nil doubles as an assertion that no alert
	// is fired for this transition — alertPlatformBreakerTransition already
	// fired it exactly once at the Observe call above, so a regression that
	// duplicated it here would nil-pointer panic instead of passing quietly.
	nowOpen := platformBreakerMonitorTick(context.Background(), breaker, nil, controllers, nil, false, true, slog.Default())

	if nowOpen {
		t.Error("platformBreakerMonitorTick reported the breaker still open after an Observe-path close")
	}

	revived, ok := ctrl.GetPRState(77)
	if !ok {
		t.Fatal("PR 77 disappeared from activePRs")
	}
	if revived.Stage != autopilot.StageWaitingCI {
		t.Errorf("PR 77 Stage = %v, want StageWaitingCI — Observe-path close did not re-drive it", revived.Stage)
	}
	if revived.BreakerHoldActive {
		t.Error("PR 77 BreakerHoldActive should be cleared after re-drive")
	}
}

// TestPlatformBreakerMonitorTick_MonitorPathCloseStillRedrives covers the
// pre-existing monitor-path close (the original GH-4792 behavior):
// EvaluateClose itself detects the time-based close on this tick (no Observe
// call raced it), and this must still re-drive held PRs — the GH-4807
// Observe-path addition must not regress the path that already worked. (The
// close-alert-exactly-once behavior on this branch is unchanged code, still
// covered by the existing alertPlatformBreakerTransition tests.)
func TestPlatformBreakerMonitorTick_MonitorPathCloseStillRedrives(t *testing.T) {
	breaker := autopilot.NewPlatformBreaker(3, 15*time.Minute, 5*time.Millisecond, nil)

	breaker.Observe(1, "owner/repo", autopilot.FailureClassInfra)
	breaker.Observe(2, "owner/repo", autopilot.FailureClassInfra)
	opened := breaker.Observe(3, "owner/repo", autopilot.FailureClassInfra)
	if !opened.Open {
		t.Fatal("test setup: breaker should be open after 3 correlated observations")
	}

	time.Sleep(10 * time.Millisecond) // exceed the 5ms quiet period, no further Observe call

	if !breaker.IsOpen() {
		t.Fatal("test setup: breaker must still report open — nothing has evaluated the close yet")
	}

	ghClient := githubSDK.NewClient(testutil.FakeGitHubToken)
	ctrl := autopilot.NewController(autopilot.DefaultConfig(), ghClient, nil, "owner", "repo", autopilot.WithPlatformBreaker(breaker))

	store, err := autopilot.NewStateStoreFromPath(":memory:")
	if err != nil {
		t.Fatalf("NewStateStoreFromPath: %v", err)
	}
	held := &autopilot.PRState{
		PRNumber:          78,
		IssueNumber:       78,
		Stage:             autopilot.StageFailed,
		BreakerHoldActive: true,
	}
	if err := store.SavePRState("owner/repo", held); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	ctrl.SetStateStore(store)
	if _, err := ctrl.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}

	controllers := map[string]*autopilot.Controller{"owner/repo": ctrl}

	nowOpen := platformBreakerMonitorTick(context.Background(), breaker, nil, controllers, nil, false, true, slog.Default())
	if nowOpen {
		t.Error("platformBreakerMonitorTick reported the breaker still open after the quiet-period close")
	}

	revived, ok := ctrl.GetPRState(78)
	if !ok {
		t.Fatal("PR 78 disappeared from activePRs")
	}
	if revived.Stage != autopilot.StageWaitingCI {
		t.Errorf("PR 78 Stage = %v, want StageWaitingCI — monitor-path close did not re-drive it", revived.Stage)
	}
}
