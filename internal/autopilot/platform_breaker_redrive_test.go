package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestReDriveBreakerHeldPRs_RevivesHeldPR covers the GH-4792 acceptance
// criterion "held PRs re-driven exactly once on close": a PR parked by the
// platform-outage breaker (StageFailed + BreakerHoldActive, see
// handleCIFailed) must re-enter StageWaitingCI, have BreakerHoldActive
// cleared, BreakerReadoptCount incremented exactly once, and get a
// re-adoption PR comment — the first time ReDriveBreakerHeldPRs runs after
// the breaker closes.
func TestReDriveBreakerHeldPRs_RevivesHeldPR(t *testing.T) {
	var mu sync.Mutex
	var commentPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/issues/80/comments" && r.Method == http.MethodPost {
			mu.Lock()
			commentPosts++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:          80,
		IssueNumber:       80,
		BranchName:        "pilot/GH-80",
		HeadSHA:           "sha-held",
		Stage:             StageFailed,
		Error:             "platform-outage breaker open — holding PR",
		BreakerHoldActive: true,
	}
	c.mu.Lock()
	c.activePRs[80] = prState
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
	}
	if prState.BreakerHoldActive {
		t.Error("BreakerHoldActive should be cleared after re-drive")
	}
	if prState.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount = %d, want 1", prState.BreakerReadoptCount)
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty after re-drive", prState.Error)
	}
	if prState.CIWaitStartedAt.IsZero() {
		t.Error("CIWaitStartedAt should be reset for fresh CI monitoring")
	}
	mu.Lock()
	got := commentPosts
	mu.Unlock()
	if got != 1 {
		t.Errorf("PR comment calls = %d, want 1 (re-adoption notice)", got)
	}

	// GH-4792: calling ReDriveBreakerHeldPRs again (e.g. a second monitor
	// tick, or the breaker opening and closing again shortly after) must be
	// a no-op for this PR now — it is no longer StageFailed/BreakerHoldActive,
	// so "re-driven exactly once" holds even under a redundant call.
	c.ReDriveBreakerHeldPRs(context.Background())
	if prState.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount after second ReDriveBreakerHeldPRs call = %d, want unchanged 1", prState.BreakerReadoptCount)
	}
	mu.Lock()
	got = commentPosts
	mu.Unlock()
	if got != 1 {
		t.Errorf("PR comment calls after second call = %d, want unchanged 1", got)
	}
}

// TestReDriveBreakerHeldPRs_IgnoresPRsNotBreakerHeld covers the negative
// case: a StageFailed PR held for any OTHER reason (e.g. a rebase hold, or a
// plain terminal failure with no BreakerHoldActive) must not be touched by
// ReDriveBreakerHeldPRs — only the platform-breaker's own hold flag is in
// scope here, not every StageFailed PR in the fleet.
func TestReDriveBreakerHeldPRs_IgnoresPRsNotBreakerHeld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	rebaseHeld := &PRState{
		PRNumber:         81,
		Stage:            StageFailed,
		RebaseHoldActive: true,
		HeadSHA:          "sha-rebase",
	}
	plainFailed := &PRState{
		PRNumber: 82,
		Stage:    StageFailed,
		HeadSHA:  "sha-plain",
	}
	c.mu.Lock()
	c.activePRs[81] = rebaseHeld
	c.activePRs[82] = plainFailed
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	if rebaseHeld.Stage != StageFailed || !rebaseHeld.RebaseHoldActive {
		t.Errorf("rebase-held PR was touched: Stage=%v RebaseHoldActive=%v", rebaseHeld.Stage, rebaseHeld.RebaseHoldActive)
	}
	if plainFailed.Stage != StageFailed {
		t.Errorf("plain-failed PR was touched: Stage=%v", plainFailed.Stage)
	}
}

// TestReDriveBreakerHeldPRs_CapReached covers the re-adoption cap: once
// BreakerReadoptCount reaches maxBreakerReadoptAttempts, a further breaker
// close must NOT trigger another revival — the PR stays parked for a human
// even though the breaker itself has closed again, so a PR whose own
// perpetual failure keeps re-opening the breaker can't ping-pong forever.
func TestReDriveBreakerHeldPRs_CapReached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:            83,
		IssueNumber:         83,
		Stage:               StageFailed,
		BreakerHoldActive:   true,
		BreakerReadoptCount: maxBreakerReadoptAttempts,
		HeadSHA:             "sha-at-cap",
	}
	c.mu.Lock()
	c.activePRs[83] = prState
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (cap reached, must stay parked)", prState.Stage)
	}
	if !prState.BreakerHoldActive {
		t.Error("BreakerHoldActive should remain true — cap reached, PR stays parked")
	}
	if prState.BreakerReadoptCount != maxBreakerReadoptAttempts {
		t.Errorf("BreakerReadoptCount = %d, want unchanged %d", prState.BreakerReadoptCount, maxBreakerReadoptAttempts)
	}
}

// TestReDriveBreakerHeldPRs_MultiplePRs verifies the fan-out across every
// currently-held PR in one controller's activePRs map, mirroring the
// periodic monitor calling this once per controller.
func TestReDriveBreakerHeldPRs_MultiplePRs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	held := []*PRState{
		{PRNumber: 90, Stage: StageFailed, BreakerHoldActive: true, HeadSHA: "sha-90"},
		{PRNumber: 91, Stage: StageFailed, BreakerHoldActive: true, HeadSHA: "sha-91"},
		{PRNumber: 92, Stage: StageFailed, BreakerHoldActive: true, HeadSHA: "sha-92"},
	}
	c.mu.Lock()
	for _, pr := range held {
		c.activePRs[pr.PRNumber] = pr
	}
	c.mu.Unlock()

	c.ReDriveBreakerHeldPRs(context.Background())

	for _, pr := range held {
		if pr.Stage != StageWaitingCI {
			t.Errorf("pr %d Stage = %v, want StageWaitingCI", pr.PRNumber, pr.Stage)
		}
		if pr.BreakerHoldActive {
			t.Errorf("pr %d BreakerHoldActive should be cleared", pr.PRNumber)
		}
		if pr.BreakerReadoptCount != 1 {
			t.Errorf("pr %d BreakerReadoptCount = %d, want 1", pr.PRNumber, pr.BreakerReadoptCount)
		}
	}
}

// TestController_RestoreState_RehydratesBreakerHeldPR covers GH-4807
// acceptance criterion 1: a PR parked via a platform-outage breaker hold
// (StageFailed + BreakerHoldActive) must survive a daemon restart. Before
// the fix, RestoreState's unconditional `pr.Stage == StageFailed` skip
// dropped every held PR — breaker_hold_active/breaker_readopt_count
// persisted in SQLite, but the row never re-entered a fresh controller's
// activePRs, so ReDriveBreakerHeldPRs (which only scans activePRs) could
// never revive it. This test saves a held PR directly to the store,
// constructs a FRESH controller over that same store (simulating the
// restart), calls RestoreState, then closes the breaker via the monitor
// path (EvaluateClose + ReDriveBreakerHeldPRs, mirroring
// startPlatformBreakerMonitor) and asserts the PR re-enters StageWaitingCI.
func TestController_RestoreState_RehydratesBreakerHeldPR(t *testing.T) {
	store := newTestStateStore(t)

	held := &PRState{
		PRNumber:          120,
		PRURL:             "https://github.com/owner/repo/pull/120",
		IssueNumber:       120,
		BranchName:        "pilot/GH-120",
		HeadSHA:           "sha-restart-held",
		Stage:             StageFailed,
		Error:             "platform-outage breaker open — holding PR",
		BreakerHoldActive: true,
	}
	if err := store.SavePRState("owner/repo", held); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	// A PR held at StageFailed for an UNRELATED reason (no BreakerHoldActive)
	// must still be skipped by RestoreState — only the breaker-hold exception
	// widens the rehydration filter.
	plainFailed := &PRState{
		PRNumber:    121,
		PRURL:       "https://github.com/owner/repo/pull/121",
		IssueNumber: 121,
		BranchName:  "pilot/GH-121",
		HeadSHA:     "sha-plain-failed",
		Stage:       StageFailed,
		Error:       "fix-issue cascade exhausted",
	}
	if err := store.SavePRState("owner/repo", plainFailed); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	// Fresh controller over the SAME store — simulates the daemon restart.
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	c.SetStateStore(store)

	if _, err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState failed: %v", err)
	}

	restoredHeld, ok := c.GetPRState(120)
	if !ok {
		t.Fatal("breaker-held PR 120 was not rehydrated into activePRs — RestoreState still strands it across a restart")
	}
	if restoredHeld.Stage != StageFailed || !restoredHeld.BreakerHoldActive {
		t.Errorf("rehydrated PR 120: Stage=%v BreakerHoldActive=%v, want StageFailed/true", restoredHeld.Stage, restoredHeld.BreakerHoldActive)
	}

	if _, ok := c.GetPRState(121); ok {
		t.Error("PR 121 (plain StageFailed, no BreakerHoldActive) should NOT be rehydrated — the skip still applies for ordinary terminal failures")
	}

	// Open the breaker for real (3 distinct correlated PRs), then close it
	// via the monitor path: EvaluateClose (time-based, mirrors
	// startPlatformBreakerMonitor's own call) followed by
	// ReDriveBreakerHeldPRs.
	breaker.Observe(1, "owner/repo", FailureClassInfra)
	breaker.Observe(2, "owner/repo", FailureClassInfra)
	opened := breaker.Observe(3, "owner/repo", FailureClassInfra)
	if !opened.Open {
		t.Fatal("test setup: breaker should be open after 3 correlated observations")
	}

	breaker.mu.Lock()
	breaker.lastInfraAt = breaker.lastInfraAt.Add(-30 * time.Minute) // force past the quiet period
	breaker.mu.Unlock()

	closeResult := breaker.EvaluateClose()
	if !closeResult.JustClosed {
		t.Fatal("test setup: breaker should have closed via the time-based quiet-period check")
	}

	c.ReDriveBreakerHeldPRs(context.Background())

	revived, ok := c.GetPRState(120)
	if !ok {
		t.Fatal("PR 120 disappeared from activePRs after re-drive")
	}
	if revived.Stage != StageWaitingCI {
		t.Errorf("PR 120 Stage after re-drive = %v, want StageWaitingCI", revived.Stage)
	}
	if revived.BreakerHoldActive {
		t.Error("PR 120 BreakerHoldActive should be cleared after re-drive")
	}
}
