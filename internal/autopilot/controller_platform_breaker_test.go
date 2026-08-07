package autopilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandleCIFailed_PlatformBreakerOpen_SuppressesDestructiveActions covers
// GH-4791 acceptance criterion 3: while the shared platform-outage breaker is
// open, handleCIFailed must not close the PR, must not create a fix issue,
// and must leave prState.Stage untouched (still StageCIFailed) so the PR is
// re-examined on a later tick — even for a failure that would otherwise be
// classified "code" and hit the fix-issue/close path (see the sibling
// TestHandleCIFailed_RealCodeFailure_StillHitsFixIssuePath, which exercises
// the same mock shape with the breaker absent). Suppression applies
// regardless of THIS PR's own classification once the breaker is already
// open from unrelated PRs — the failure signal is untrustworthy for
// everyone during a correlated outage, not just the PRs that fed it.
func TestHandleCIFailed_PlatformBreakerOpen_SuppressesDestructiveActions(t *testing.T) {
	issueCreated := false
	prClosed := false

	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/codesha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 200, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/200/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 902}))
		case r.URL.Path == "/repos/owner/repo/pulls/53" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	// Pre-correlate three OTHER, unrelated PRs directly against the shared
	// breaker to simulate a platform outage already confirmed BEFORE this
	// PR's own CI failure is even observed — the breaker is open going into
	// this call, regardless of what handleCIFailed classifies PR #53 as.
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	breaker.Observe(1, "owner/repo", FailureClassInfra)
	breaker.Observe(2, "owner/repo", FailureClassInfra)
	breaker.Observe(3, "owner/repo", FailureClassInfra)
	if !breaker.IsOpen() {
		t.Fatal("test setup: breaker should already be open from 3 pre-seeded distinct-PR observations")
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient), WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber: 53,
		HeadSHA:  "codesha1",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("no fix issue should be created while the platform-outage breaker is open")
	}
	if prClosed {
		t.Error("PR must not be closed while the platform-outage breaker is open")
	}
	if prState.Stage != StageCIFailed {
		t.Errorf("Stage = %s, want unchanged %s (PR should be re-examined on a later tick)", prState.Stage, StageCIFailed)
	}

	snap := c.metrics.Snapshot()
	if !snap.PlatformBreakerOpen {
		t.Error("metrics snapshot PlatformBreakerOpen = false, want true")
	}
	if snap.PlatformBreakerTrips != 1 {
		t.Errorf("PlatformBreakerTrips = %d, want 1", snap.PlatformBreakerTrips)
	}

	// The open transition happened via the pre-seeded direct Observe calls,
	// not through this controller's handleCIFailed call (which only
	// observed a code-classified, hence irrelevant-to-correlation, failure
	// on an already-open breaker) — so no NEW transition alert fires here.
	if len(sink.events) != 0 {
		t.Errorf("expected no alert from this call (breaker was already open), got %d: %+v", len(sink.events), sink.events)
	}
}

// TestHandleCIFailed_PlatformBreakerTransition_AlertsOnceOnOpenAndClose
// covers GH-4791 acceptance criterion 4: exactly one alert on open and one
// on close, never one per affected PR, and that closing the breaker resumes
// normal (destructive) CI-failure handling.
func TestHandleCIFailed_PlatformBreakerTransition_AlertsOnceOnOpenAndClose(t *testing.T) {
	issueCreated := false
	prClosed := false

	// A runner-infra log signature (see classifyCheckFailure) — classifies
	// FailureClassInfra, the third distinct-PR observation needed to open
	// the breaker.
	const infraLog = `##[error]The runner has received a shutdown signal.`
	// A real compiler/lint annotation — always classifies FailureClassCode,
	// used on an unrelated PR/SHA to verify normal destructive handling
	// resumes once the breaker closes.
	const codeLog = `internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/infrasha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 210, Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/210/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(infraLog))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/210":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, ghadapter.WorkflowJob{
				ID: 210, RunID: 610, Name: "build", Status: "completed",
				Steps: []ghadapter.JobStep{{Name: "Build", Status: "completed", Conclusion: "failure", Number: 1}},
			}))
		case r.URL.Path == "/repos/owner/repo/actions/runs/610/rerun-failed-jobs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/repos/owner/repo/commits/codesha2/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 211, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/211/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 903}))
		case r.URL.Path == "/repos/owner/repo/pulls/999" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	clock := &fixedClock{t: time.Now()}
	breaker.now = clock.now
	// Two other unrelated PRs pre-correlated directly.
	breaker.Observe(1, "owner/repo", FailureClassInfra)
	breaker.Observe(2, "owner/repo", FailureClassInfra)

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient), WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	// PR #54 is the 3rd distinct PR: this call's own Observe pushes the
	// breaker open, firing the open alert.
	infraPRState := &PRState{
		PRNumber: 54,
		HeadSHA:  "infrasha1",
		Stage:    StageCIFailed,
	}
	if err := c.handleCIFailed(context.Background(), infraPRState); err != nil {
		t.Fatalf("handleCIFailed (open) returned unexpected error: %v", err)
	}
	if !breaker.IsOpen() {
		t.Fatal("breaker should be open after the 3rd distinct-PR infra observation")
	}
	if len(sink.events) != 1 || sink.events[0].Type != alerts.EventType("platform_breaker_open") {
		t.Fatalf("expected exactly 1 platform_breaker_open alert after opening, got %+v", sink.events)
	}

	// Advance past the quiet period with no further infra/unknown-class
	// failure, then drive an UNRELATED PR's own CI failure (classified
	// code, not infra) through handleCIFailed: the close check inside
	// Observe runs unconditionally regardless of this call's own class, so
	// this call both detects the close transition (firing the close alert)
	// and — since the breaker reports Open:false for this call — proceeds
	// through the normal destructive fix-issue/close path for PR #999.
	clock.advance(21 * time.Minute)

	codePRState := &PRState{
		PRNumber: 999,
		HeadSHA:  "codesha2",
		Stage:    StageCIFailed,
	}
	if err := c.handleCIFailed(context.Background(), codePRState); err != nil {
		t.Fatalf("handleCIFailed (close) returned unexpected error: %v", err)
	}

	if breaker.IsOpen() {
		t.Error("breaker should be closed after the quiet period elapsed")
	}
	if !issueCreated {
		t.Error("expected a fix issue once the breaker closed and normal handling resumed")
	}
	if !prClosed {
		t.Error("expected the PR to be closed once the breaker closed and normal handling resumed")
	}
	if codePRState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s once normal handling resumed", codePRState.Stage, StageFailed)
	}

	if len(sink.events) != 2 {
		t.Fatalf("expected exactly 2 alert events total (1 open, 1 close), got %d: %+v", len(sink.events), sink.events)
	}
	if sink.events[1].Type != alerts.EventType("platform_breaker_close") {
		t.Errorf("second alert Type = %q, want platform_breaker_close", sink.events[1].Type)
	}
}

// TestHandleCIFailed_PlatformBreakerDisabled_IsNoOp covers the
// disabled-by-config acceptance criterion: a Controller with no
// WithPlatformBreaker option (nil *PlatformBreaker, the default) behaves
// byte-identically to pre-GH-4791 code — normal destructive handling
// proceeds on the very first code-classified failure.
func TestHandleCIFailed_PlatformBreakerDisabled_IsNoOp(t *testing.T) {
	issueCreated := false
	prClosed := false

	const codeLog = `internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/codesha3/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 202, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/202/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 904}))
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	// No WithPlatformBreaker option: c.platformBreaker stays nil.
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber: 55,
		HeadSHA:  "codesha3",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected a fix issue with the platform breaker disabled (nil)")
	}
	if !prClosed {
		t.Error("expected the PR to be closed with the platform breaker disabled (nil)")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}

	snap := c.metrics.Snapshot()
	if snap.PlatformBreakerOpen {
		t.Error("PlatformBreakerOpen = true with the breaker disabled, want false")
	}
	if snap.PlatformBreakerTrips != 0 {
		t.Errorf("PlatformBreakerTrips = %d, want 0 with the breaker disabled", snap.PlatformBreakerTrips)
	}
}
