package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5236 (2026-08-26 GitHub Actions outage) regression tests.
//
// During the outage, PR#5231 sat with all 8 jobs `queued` and never
// started, exhausted the 30-minute CI-wait deadline with last_status
// pending, and hit StageFailed directly via handleWaitingCI's
// deadlineExceeded branch — a path that never called
// PlatformBreaker.Observe. A sibling repo PR got zero check-runs for over
// an hour (the "missing checks" shape, which GH-5233 already folds into
// the same CIPending-forever/deadline-exceeded branch since it holds a SHA
// at CIPending forever once the repo has previously produced check-runs).
// Neither shape ever reached the breaker's cross-PR correlation, so the
// breaker never opened during a real platform-wide outage.
//
// These tests cover the four acceptance-criteria table rows: correlated
// timeouts open the breaker, correlated missing-checks open the breaker,
// an isolated timeout does not open it, and suppression + re-drive of both
// shapes while the breaker is open — exercised through handleWaitingCI via
// ProcessPR, mirroring gh4851_ci_wait_confirm_poll_test.go's pattern.

// gh5236PendingCheckRunHandler reports a single in-progress (never
// resolving) check run for sha — the "timeout" shape: checks exist and
// were discovered, but never complete.
func gh5236PendingCheckRunHandler(sha string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs" {
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: github.CheckRunInProgress}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}
}

// gh5236MultiPendingCheckRunHandler is the multi-SHA counterpart, reporting
// a single in-progress check run for any of the given SHAs.
func gh5236MultiPendingCheckRunHandler(shas []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, sha := range shas {
			if r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs" {
				resp := github.CheckRunsResponse{
					TotalCount: 1,
					CheckRuns:  []github.CheckRun{{Name: "ci", Status: github.CheckRunInProgress}},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}
}

// gh5236NoCheckRunsHandler reports zero check-runs for every SHA — the
// "missing checks" shape: a SHA never produces a single check-run.
func gh5236NoCheckRunsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/check-runs") {
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}
}

func gh5236SeedWaitingPR(c *Controller, prNum int, sha string) *PRState {
	prState := &PRState{
		PRNumber:        prNum,
		IssueNumber:     prNum,
		HeadSHA:         sha,
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		TargetBranch:    "main",
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Lock()
	c.activePRs[prNum] = prState
	c.mu.Unlock()
	return prState
}

// TestHandleWaitingCI_CorrelatedTimeouts_OpenBreaker covers acceptance row
// 1: a burst of CI-wait timeouts (checks discovered but never resolving)
// across distinct PRs must open the shared platform breaker, exactly like
// a burst of infra-classified CI failures does via handleCIFailed.
func TestHandleWaitingCI_CorrelatedTimeouts_OpenBreaker(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "")
	})

	shas := []string{"gh5236to1", "gh5236to2", "gh5236to3"}
	server := httptest.NewServer(gh5236MultiPendingCheckRunHandler(shas))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prNums := []int{5301, 5302, 5303}
	for i, prNum := range prNums {
		prState := gh5236SeedWaitingPR(c, prNum, shas[i])
		ghPR := &github.PullRequest{Number: prNum, Head: github.PRRef{SHA: shas[i]}, Base: github.PRRef{Ref: "main"}}
		if err := c.ProcessPR(context.Background(), prNum, ghPR); err != nil {
			t.Fatalf("ProcessPR(%d) returned unexpected error: %v", prNum, err)
		}
		if i < 2 {
			if breaker.IsOpen() {
				t.Fatalf("breaker opened too early, after only %d distinct-PR timeouts", i+1)
			}
			if prState.Stage != StageFailed {
				t.Errorf("PR %d Stage = %s, want %s (confirmed timeout, breaker not yet open)", prNum, prState.Stage, StageFailed)
			}
			if prState.BreakerHoldActive {
				t.Errorf("PR %d should not be breaker-held before the breaker opens", prNum)
			}
		}
	}

	if !breaker.IsOpen() {
		t.Fatal("breaker should be open after 3 distinct-PR CI-wait timeouts")
	}

	// The 3rd PR's own tick is the one that crossed the threshold — it must
	// be held, not confirmed as a terminal timeout.
	thirdPR, ok := c.GetPRState(5303)
	if !ok {
		t.Fatal("PR 5303 no longer tracked")
	}
	if thirdPR.Stage != StageFailed || !thirdPR.BreakerHoldActive {
		t.Errorf("PR 5303 Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true (held, not confirmed-timeout)", thirdPR.Stage, thirdPR.BreakerHoldActive)
	}

	if len(sink.events) != 1 || sink.events[0].Type != alerts.EventType("platform_breaker_open") {
		t.Fatalf("expected exactly 1 platform_breaker_open alert, got %+v", sink.events)
	}
}

// TestHandleWaitingCI_CorrelatedMissingChecks_OpenBreaker covers acceptance
// row 2: a burst of SHAs that never produce a single check-run across
// distinct PRs must open the breaker via the same deadline-exceeded path.
func TestHandleWaitingCI_CorrelatedMissingChecks_OpenBreaker(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "")
	})

	server := httptest.NewServer(gh5236NoCheckRunsHandler())
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prNums := []int{5311, 5312, 5313}
	for i, prNum := range prNums {
		sha := fmt.Sprintf("gh5236mc%d", i)
		gh5236SeedWaitingPR(c, prNum, sha)
		ghPR := &github.PullRequest{Number: prNum, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
		if err := c.ProcessPR(context.Background(), prNum, ghPR); err != nil {
			t.Fatalf("ProcessPR(%d) returned unexpected error: %v", prNum, err)
		}
		if i < 2 && breaker.IsOpen() {
			t.Fatalf("breaker opened too early, after only %d distinct-PR missing-checks observations", i+1)
		}
	}

	if !breaker.IsOpen() {
		t.Fatal("breaker should be open after 3 distinct-PR missing-checks timeouts")
	}

	thirdPR, ok := c.GetPRState(5313)
	if !ok {
		t.Fatal("PR 5313 no longer tracked")
	}
	if thirdPR.Stage != StageFailed || !thirdPR.BreakerHoldActive {
		t.Errorf("PR 5313 Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true", thirdPR.Stage, thirdPR.BreakerHoldActive)
	}
	if len(thirdPR.DiscoveredChecks) != 0 {
		t.Errorf("PR 5313 DiscoveredChecks = %v, want empty (missing-checks shape)", thirdPR.DiscoveredChecks)
	}
}

// TestHandleWaitingCI_IsolatedTimeout_DoesNotOpenBreaker covers acceptance
// row 3: a single, uncorrelated CI-wait timeout — with the breaker wired up
// but no other distinct-PR observations feeding it — must behave exactly
// as an isolated timeout does today: a normal confirmed-timeout terminal
// failure, no breaker hold, no suppression.
func TestHandleWaitingCI_IsolatedTimeout_DoesNotOpenBreaker(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"operational"}],"incidents":[]}`)
	})

	const sha = "gh5236iso1"
	server := httptest.NewServer(gh5236PendingCheckRunHandler(sha))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := gh5236SeedWaitingPR(c, 5321, sha)
	ghPR := &github.PullRequest{Number: 5321, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 5321, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if breaker.IsOpen() {
		t.Error("breaker should not open on a single isolated timeout with a green githubstatus.com probe")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (isolated confirmed timeout)", prState.Stage, StageFailed)
	}
	if prState.BreakerHoldActive {
		t.Error("BreakerHoldActive should be false — this is a normal terminal timeout, not a breaker hold")
	}
	if prState.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q", prState.TerminalLabel, github.LabelFailed)
	}
	if !strings.Contains(prState.Error, "CI timeout") {
		t.Errorf("Error = %q, want it to mention CI timeout", prState.Error)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no platform-breaker alert for an isolated timeout, got %+v", sink.events)
	}
}

// TestHandleWaitingCI_IsolatedTimeout_NilBreaker_UnchangedBehavior is the
// disabled-by-config control: with no WithPlatformBreaker option (nil, the
// default — the pre-GH-5236 configuration), an isolated timeout must behave
// byte-identically to before, including never making an outbound
// githubstatus.com call (the probe is gated on the breaker being wired up
// and not yet open).
func TestHandleWaitingCI_IsolatedTimeout_NilBreaker_UnchangedBehavior(t *testing.T) {
	probeCalled := false
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		probeCalled = true
		return jsonResponse(http.StatusOK, `{"components":[],"incidents":[]}`)
	})

	const sha = "gh5236nilbreaker1"
	server := httptest.NewServer(gh5236PendingCheckRunHandler(sha))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := gh5236SeedWaitingPR(c, 5325, sha)
	ghPR := &github.PullRequest{Number: 5325, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 5325, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.BreakerHoldActive {
		t.Error("BreakerHoldActive should be false with no breaker wired")
	}
	if probeCalled {
		t.Error("githubstatus.com probe should not be called when no platform breaker is wired up")
	}
}

// TestHandleWaitingCI_CorroboratedProbe_OpensOnFirstTimeout covers the
// accelerant: when githubstatus.com corroborates an Actions incident, a
// single CI-wait timeout is enough to open the breaker — never required to
// wait for minDistinctPRs distinct timeouts to each expire in turn.
func TestHandleWaitingCI_CorroboratedProbe_OpensOnFirstTimeout(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"major_outage"}],"incidents":[]}`)
	})

	const sha = "gh5236corrob1"
	server := httptest.NewServer(gh5236PendingCheckRunHandler(sha))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := gh5236SeedWaitingPR(c, 5331, sha)
	ghPR := &github.PullRequest{Number: 5331, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 5331, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if !breaker.IsOpen() {
		t.Error("breaker should open on the very first timeout when githubstatus.com corroborates an Actions outage")
	}
	if prState.Stage != StageFailed || !prState.BreakerHoldActive {
		t.Errorf("Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true (held, breaker open)", prState.Stage, prState.BreakerHoldActive)
	}
}

// TestHandleWaitingCI_BreakerOpen_SuppressesAndReDrives covers acceptance
// row 4 for the timeout shape: while the breaker is already open (opened by
// unrelated PRs), a PR whose own CI wait expires must be held
// (BreakerHoldActive), not confirmed as a terminal timeout — and once the
// breaker closes, ReDriveBreakerHeldPRs must revive it back into
// StageWaitingCI with a fresh wait clock.
func TestHandleWaitingCI_BreakerOpen_SuppressesAndReDrives(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "")
	})

	const sha = "gh5236held1"
	server := httptest.NewServer(gh5236PendingCheckRunHandler(sha))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	// Pre-correlate three OTHER, unrelated PRs directly so the breaker is
	// already open going into this PR's own timeout, independent of this
	// PR's own shape.
	breaker.Observe(9001, "owner/repo", FailureClassInfra)
	breaker.Observe(9002, "owner/repo", FailureClassInfra)
	breaker.Observe(9003, "owner/repo", FailureClassInfra)
	if !breaker.IsOpen() {
		t.Fatal("test setup: breaker should already be open")
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := gh5236SeedWaitingPR(c, 5341, sha)
	ghPR := &github.PullRequest{Number: 5341, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 5341, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if prState.Stage != StageFailed || !prState.BreakerHoldActive {
		t.Fatalf("Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true while breaker is open", prState.Stage, prState.BreakerHoldActive)
	}
	if strings.Contains(prState.Error, "CI timeout") {
		t.Errorf("Error = %q should not be set on a breaker-held PR (not a confirmed timeout)", prState.Error)
	}
	// No new alert: the open transition happened via the pre-seeded direct
	// Observe calls, not through this PR's own ObserveTimeout call.
	if len(sink.events) != 0 {
		t.Errorf("expected no new alert (breaker was already open), got %+v", sink.events)
	}

	// The breaker itself need not be closed for ReDriveBreakerHeldPRs to run
	// — it acts purely on each PR's own Stage/BreakerHoldActive flags (see
	// redriveBreakerHeldPRLocked), mirroring the periodic monitor calling it
	// once the breaker's own JustClosed transition fires.
	c.ReDriveBreakerHeldPRs(context.Background())

	revived, ok := c.GetPRState(5341)
	if !ok {
		t.Fatal("PR 5341 no longer tracked")
	}
	if revived.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s after re-drive", revived.Stage, StageWaitingCI)
	}
	if revived.BreakerHoldActive {
		t.Error("BreakerHoldActive should be cleared after re-drive")
	}
	if revived.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount = %d, want 1", revived.BreakerReadoptCount)
	}
	if revived.CIWaitStartedAt.Before(time.Now().Add(-time.Minute)) {
		t.Error("CIWaitStartedAt should be reset to a fresh clock on re-drive")
	}
}

// TestHandleWaitingCI_MissingChecks_BreakerOpen_SuppressesAndReDrives is the
// missing-checks-shape counterpart of the above: same suppression and
// re-drive guarantee, but for a SHA that never produced a single
// check-run rather than one that produced checks which never resolved.
func TestHandleWaitingCI_MissingChecks_BreakerOpen_SuppressesAndReDrives(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusNotFound, "")
	})

	server := httptest.NewServer(gh5236NoCheckRunsHandler())
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	breaker.Observe(9011, "owner/repo", FailureClassInfra)
	breaker.Observe(9012, "owner/repo", FailureClassInfra)
	breaker.Observe(9013, "owner/repo", FailureClassInfra)
	if !breaker.IsOpen() {
		t.Fatal("test setup: breaker should already be open")
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	const sha = "gh5236heldmc1"
	prState := gh5236SeedWaitingPR(c, 5351, sha)
	ghPR := &github.PullRequest{Number: 5351, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 5351, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if prState.Stage != StageFailed || !prState.BreakerHoldActive {
		t.Fatalf("Stage=%s BreakerHoldActive=%v, want StageFailed+BreakerHoldActive=true while breaker is open", prState.Stage, prState.BreakerHoldActive)
	}
	if len(sink.events) != 0 {
		t.Errorf("expected no new alert (breaker was already open), got %+v", sink.events)
	}

	c.ReDriveBreakerHeldPRs(context.Background())

	revived, ok := c.GetPRState(5351)
	if !ok {
		t.Fatal("PR 5351 no longer tracked")
	}
	if revived.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s after re-drive", revived.Stage, StageWaitingCI)
	}
	if revived.BreakerHoldActive {
		t.Error("BreakerHoldActive should be cleared after re-drive")
	}
	if revived.BreakerReadoptCount != 1 {
		t.Errorf("BreakerReadoptCount = %d, want 1", revived.BreakerReadoptCount)
	}
}
