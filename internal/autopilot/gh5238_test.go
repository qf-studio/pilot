package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5238 regression tests.
//
// Two fail-open holes were found in the post-merge stage, the same class
// GH-5233 (pre-merge discovery) and GH-5236 (platform-breaker feed) already
// fixed pre-merge:
//
//  1. handlePostMergeCI's no-workflow probe (HasAnyCIConfigured) was a raw
//     single-SHA look with no cross-SHA history guard, so a repo with prior
//     observed check runs whose merged SHA legitimately shows zero checks
//     during a platform outage resolved straight to CISuccess and proceeded
//     to StageReleasing — shipping a release on no evidence, with no revert
//     path anywhere in this package.
//  2. The post-merge timeout branch never called PlatformBreaker.Observe/
//     ObserveTimeout at all, so a burst of post-merge outage symptoms never
//     opened the breaker and was never suppressed by it.
//
// These tests cover the four acceptance-criteria rows: no-CI repo unchanged,
// CI-bearing repo with missing checks held, correlated post-merge timeouts
// opening the breaker, and suppression + re-drive while open.

// TestHandlePostMergeCI_GH5238_NoCIRepo_Unchanged is acceptance row 1: a
// repository that has never produced a check run for any SHA must continue
// to treat a checkless merge SHA as satisfied CI and proceed to releasing,
// exactly as it did before this fix (GH-4643's original behavior).
func TestHandlePostMergeCI_GH5238_NoCIRepo_Unchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/gh5238nocisha/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}})
		case "/repos/owner/repo/commits/gh5238nocisha/status":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CombinedStatus{TotalCount: 0})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Second

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:     92380,
		ScopeKey:     "epic:5238-a",
		IssueNumber:  92380,
		Stage:        StagePostMergeCI,
		PostMergeSHA: "gh5238nocisha",
		// Past the 90s no-workflow grace period but not the 5s CIWaitTimeout's
		// relative order — proves the probe path resolves this, not a timeout.
		PostMergeCIStartedAt: time.Now().Add(-2 * time.Minute),
	}
	c.mu.Lock()
	c.activePRs[prState.PRNumber] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if prState.Stage != StageReleasing {
		t.Errorf("Stage = %v, want StageReleasing (no-CI repo behavior must be unaffected)", prState.Stage)
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty", prState.Error)
	}
}

// TestHandlePostMergeCI_GH5238_CIBearingRepo_MissingChecksHeld is acceptance
// row 2: once this repo's CI monitor has observed check runs before (on a
// different SHA), a merged SHA that reports zero check runs within the grace
// window must be held — never resolved to CISuccess, never StageReleasing.
func TestHandlePostMergeCI_GH5238_CIBearingRepo_MissingChecksHeld(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/gh5238priorsha/check-runs":
			resp := github.CheckRunsResponse{TotalCount: 1, CheckRuns: []github.CheckRun{
				{ID: 1, Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/gh5238mainsha/check-runs":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}})
		case "/repos/owner/repo/commits/gh5238mainsha/status":
			// Actions-only repo: combined-status legitimately reports empty
			// too — this must not be consulted once history is present.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.CombinedStatus{TotalCount: 0})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 5 * time.Minute
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{Mode: "auto", DiscoveryGracePeriod: 10 * time.Millisecond}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Seed this monitor's cross-SHA history: the repo HAS produced a
	// check-run before, just on a different SHA.
	if _, err := c.ciMonitor.CheckCI(context.Background(), "gh5238priorsha"); err != nil {
		t.Fatalf("seed CheckCI(gh5238priorsha) error = %v", err)
	}

	prState := &PRState{
		PRNumber:             92381,
		IssueNumber:          92381,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "gh5238mainsha",
		PostMergeCIStartedAt: time.Now().Add(-2 * time.Minute), // past the 90s no-workflow grace
	}
	c.mu.Lock()
	c.activePRs[prState.PRNumber] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() (1st) error = %v", err)
	}
	// Let the per-SHA discovery grace period elapse so the second call
	// exercises checkAutoDiscoveredRuns' post-grace anomaly-hold branch,
	// not just "grace not yet expired".
	time.Sleep(30 * time.Millisecond)
	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() (2nd) error = %v", err)
	}

	if prState.Stage == StageReleasing {
		t.Error("Stage = StageReleasing, want held — a CI-bearing repo's missing checks must not resolve to success")
	}
	if prState.CIStatus == CISuccess {
		t.Errorf("CIStatus = %v, want anything but CISuccess", prState.CIStatus)
	}
	if prState.Stage != StagePostMergeCI {
		t.Errorf("Stage = %v, want StagePostMergeCI (still holding, not failed/timed out yet)", prState.Stage)
	}
}

// TestHandlePostMergeCI_GH5238_CorrelatedTimeouts_OpenBreaker is acceptance
// row 3: a burst of post-merge CI timeouts across distinct PRs must open the
// shared platform breaker via the post-merge rung's own ObserveTimeout call
// — before this fix, post-merge handling never called Observe/ObserveTimeout
// at all, so this burst would have gone completely unnoticed by the breaker.
func TestHandlePostMergeCI_GH5238_CorrelatedTimeouts_OpenBreaker(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "")
	})

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 50 * time.Millisecond
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prNums := []int{92391, 92392, 92393}
	var lastState *PRState
	for i, prNum := range prNums {
		prState := &PRState{
			PRNumber:    prNum,
			IssueNumber: prNum,
			Stage:       StagePostMergeCI,
			// Distinct SHA per PR; irrelevant here since the timeout check
			// fires before any CheckCI call (started-at already exceeds the
			// timeout, but not yet the 90s no-workflow grace period).
			PostMergeSHA:         fmt.Sprintf("gh5238to%d", i),
			PostMergeCIStartedAt: time.Now().Add(-200 * time.Millisecond),
		}
		c.mu.Lock()
		c.activePRs[prNum] = prState
		c.mu.Unlock()

		if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
			t.Fatalf("handlePostMergeCI(%d) error = %v", prNum, err)
		}

		if i < 2 {
			if breaker.IsOpen() {
				t.Fatalf("breaker opened too early, after only %d distinct-PR post-merge timeouts", i+1)
			}
			if prState.Stage != StageFailed || prState.BreakerHoldActive {
				t.Errorf("PR %d Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=false (confirmed timeout, breaker not yet open)",
					prNum, prState.Stage, prState.BreakerHoldActive)
			}
		}
		lastState = prState
	}

	if !breaker.IsOpen() {
		t.Fatal("breaker should be open after 3 distinct-PR post-merge CI timeouts")
	}
	if lastState.Stage != StageFailed || !lastState.BreakerHoldActive {
		t.Errorf("3rd PR Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true (held, not confirmed-timeout)",
			lastState.Stage, lastState.BreakerHoldActive)
	}
}

// TestHandlePostMergeCI_GH5238_BreakerOpen_SuppressesAndReDrives is
// acceptance row 4: while the breaker is already open (opened by unrelated
// PRs), a post-merge timeout must not re-queue/fail the scope release or
// spawn a fix issue — it must be held (BreakerHoldActive), and once the
// breaker closes, ReDriveBreakerHeldPRs must revive it back into
// StagePostMergeCI (not StageWaitingCI — this PR is already merged) with a
// fresh timer.
func TestHandlePostMergeCI_GH5238_BreakerOpen_SuppressesAndReDrives(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "")
	})

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:0")
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 50 * time.Millisecond
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_scope_close"}

	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	// Pre-correlate three OTHER, unrelated PRs directly so the breaker is
	// already open going into this PR's own post-merge timeout.
	breaker.Observe(9001, "owner/repo", FailureClassInfra)
	breaker.Observe(9002, "owner/repo", FailureClassInfra)
	breaker.Observe(9003, "owner/repo", FailureClassInfra)
	if !breaker.IsOpen() {
		t.Fatal("test setup: breaker should already be open")
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	stateStore := newTestStateStore(t)
	if err := stateStore.EnqueueScopeRelease("owner/repo", "epic:5238-held", "epic", []int{92401}); err != nil {
		t.Fatalf("EnqueueScopeRelease failed: %v", err)
	}
	c.SetStateStore(stateStore)

	prState := &PRState{
		PRNumber:             92401,
		ScopeKey:             "epic:5238-held",
		IssueNumber:          92401,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "gh5238held",
		PostMergeCIStartedAt: time.Now().Add(-200 * time.Millisecond),
	}
	c.mu.Lock()
	c.activePRs[prState.PRNumber] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI() error = %v", err)
	}

	if prState.Stage != StageFailed || !prState.BreakerHoldActive {
		t.Fatalf("Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true while breaker is open",
			prState.Stage, prState.BreakerHoldActive)
	}

	// The scope-release row must be untouched — handleScopeReleaseFailure
	// must never have been called while the breaker holds this PR.
	row, err := stateStore.GetScopeRelease("owner/repo", "epic:5238-held")
	if err != nil || row == nil {
		t.Fatalf("GetScopeRelease failed: %v", err)
	}
	if row.State != "pending" || row.Attempts != 0 {
		t.Errorf("scope release row = state=%q attempts=%d, want state=pending attempts=0 (untouched — no scope failure recorded while held)",
			row.State, row.Attempts)
	}
	// No new alert: the open transition happened via the pre-seeded direct
	// Observe calls, not through this PR's own ObserveTimeout call.
	if len(sink.events) != 0 {
		t.Errorf("expected no new alert (breaker was already open), got %+v", sink.events)
	}

	// The PR must still be tracked (not drained via removePR) so it can be
	// re-driven once the breaker closes.
	if _, ok := c.GetPRState(92401); !ok {
		t.Fatal("PR 92401 should still be tracked while breaker-held")
	}

	c.ReDriveBreakerHeldPRs(context.Background())

	revived, ok := c.GetPRState(92401)
	if !ok {
		t.Fatal("PR 92401 no longer tracked")
	}
	if revived.Stage != StagePostMergeCI {
		t.Errorf("Stage = %s, want %s after re-drive (already merged — must not re-enter pre-merge CI wait)", revived.Stage, StagePostMergeCI)
	}
	if revived.BreakerHoldActive {
		t.Error("BreakerHoldActive should be cleared after re-drive")
	}
	if revived.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount = %d, want 1", revived.BreakerReadoptCount)
	}
	if revived.PostMergeCIStartedAt.Before(time.Now().Add(-time.Minute)) {
		t.Error("PostMergeCIStartedAt should be reset to a fresh clock on re-drive")
	}
}
