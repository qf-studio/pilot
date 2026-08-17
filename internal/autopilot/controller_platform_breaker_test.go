package autopilot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
// and must park it (StageFailed, BreakerHoldActive) instead of leaving it at
// StageCIFailed for per-tick reprocessing (GH-4792: reprocessing on every
// tick would indefinitely refresh the breaker's own quiet-period close
// clock) — even for a failure that would otherwise be classified "code" and
// hit the fix-issue/close path (see the sibling
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
	// GH-4792: suppression now parks the PR at StageFailed with
	// BreakerHoldActive set, instead of leaving it at StageCIFailed for
	// per-tick reprocessing — this stops each tick's Observe call from
	// indefinitely refreshing the breaker's own quiet-period close clock.
	// The held PR is revived by ReDriveBreakerHeldPRs once the breaker
	// closes (see platform_breaker_redrive_test.go).
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (PR should be parked, not left CI-failed)", prState.Stage, StageFailed)
	}
	if !prState.BreakerHoldActive {
		t.Error("BreakerHoldActive = false, want true so ReDriveBreakerHeldPRs can find this PR later")
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

	// GH-4792: the open transition below synchronously fires
	// ProbeGitHubStatus inside alertPlatformBreakerTransition. Stub the
	// injectable HTTP getter so this test never makes a real outbound call
	// to githubstatus.com — a canned "not found" response drives both
	// component and incident probes to PlatformProbeUnknown, which is fine
	// here since this test only cares about alert count/type, not verdict.
	origGetter := platformStatusHTTPGet
	platformStatusHTTPGet = func(url string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}
	t.Cleanup(func() { platformStatusHTTPGet = origGetter })

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

// newPlatformBreakerOpenGitHubServer builds the minimal GitHub API mock
// needed to drive a single infra-classified CI failure through
// handleCIFailed on head SHA sha / job id/run id jobID/runID — shared by the
// two probe-interaction tests below, which only care about the breaker's own
// open decision and the resulting alert, not the auto-retry/fix-issue
// machinery downstream of it.
func newPlatformBreakerOpenGitHubServer(t *testing.T, sha string, jobID, runID int64) *httptest.Server {
	t.Helper()
	const infraLog = `##[error]The runner has received a shutdown signal.`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == fmt.Sprintf("/repos/owner/repo/commits/%s/check-runs", sha):
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: jobID, Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == fmt.Sprintf("/repos/owner/repo/actions/jobs/%d/logs", jobID):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(infraLog))
		case r.URL.Path == fmt.Sprintf("/repos/owner/repo/actions/jobs/%d", jobID):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, ghadapter.WorkflowJob{
				ID: jobID, RunID: runID, Name: "build", Status: "completed",
				Steps: []ghadapter.JobStep{{Name: "Build", Status: "completed", Conclusion: "failure", Number: 1}},
			}))
		case r.URL.Path == fmt.Sprintf("/repos/owner/repo/actions/runs/%d/rerun-failed-jobs", runID) && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestHandleCIFailed_PlatformBreakerOpen_ProbeFailureDoesNotBlockOpening
// covers the GH-4792 acceptance criterion "probe failure doesn't block
// opening": the advisory githubstatus.com probe is fired synchronously
// inside alertPlatformBreakerTransition once the breaker's own correlation
// logic has ALREADY decided to open (PlatformBreaker.Observe runs first and
// is the sole authority — see controller.go's handleCIFailed). A probe that
// fails outright (network error) must never prevent, delay, or undo that
// decision.
func TestHandleCIFailed_PlatformBreakerOpen_ProbeFailureDoesNotBlockOpening(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return nil, io.ErrClosedPipe
	})

	server := newPlatformBreakerOpenGitHubServer(t, "infrasha9", 300, 700)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	breaker.Observe(1, "owner/repo", FailureClassInfra)
	breaker.Observe(2, "owner/repo", FailureClassInfra)

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient), WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 60, HeadSHA: "infrasha9", Stage: StageCIFailed}
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !breaker.IsOpen() {
		t.Error("breaker should still open on the 3rd distinct-PR infra observation despite the probe failing")
	}
	if len(sink.events) != 1 || sink.events[0].Type != alerts.EventType("platform_breaker_open") {
		t.Fatalf("expected exactly 1 platform_breaker_open alert, got %+v", sink.events)
	}
	if got := sink.events[0].Metadata["probe_verdict"]; got != string(PlatformProbeUnknown) {
		t.Errorf("probe_verdict metadata = %q, want %q (probe failed)", got, PlatformProbeUnknown)
	}
}

// TestHandleCIFailed_PlatformBreakerOpen_GreenProbeDoesNotVetoCorrelation
// covers the GH-4792 acceptance criterion "green probe doesn't veto
// correlation": a githubstatus.com probe reporting fully healthy (Actions
// operational, no unresolved incident) must never override or suppress an
// already-correlated internal CI-failure signal — status pages lag reality,
// per the 2026-08-06 incident this whole feature exists to catch.
func TestHandleCIFailed_PlatformBreakerOpen_GreenProbeDoesNotVetoCorrelation(t *testing.T) {
	stubPlatformStatusHTTPGet(t, func(url string) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"components":[{"name":"Actions","status":"operational"}],"incidents":[]}`)
	})

	server := newPlatformBreakerOpenGitHubServer(t, "infrasha10", 301, 701)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	breaker.Observe(1, "owner/repo", FailureClassInfra)
	breaker.Observe(2, "owner/repo", FailureClassInfra)

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient), WithPlatformBreaker(breaker))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{PRNumber: 61, HeadSHA: "infrasha10", Stage: StageCIFailed}
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !breaker.IsOpen() {
		t.Error("breaker should still open on the 3rd distinct-PR infra observation even though the external probe reports green")
	}
	if len(sink.events) != 1 || sink.events[0].Type != alerts.EventType("platform_breaker_open") {
		t.Fatalf("expected exactly 1 platform_breaker_open alert, got %+v", sink.events)
	}
	if got := sink.events[0].Metadata["probe_verdict"]; got != string(PlatformProbeGreen) {
		t.Errorf("probe_verdict metadata = %q, want %q", got, PlatformProbeGreen)
	}
}

// TestHandleMerging_PlatformBreakerOpen_SuppressesMerge covers the GH-4792
// acceptance criterion "merge suppression while open": an already-green PR
// sitting at StageMerging must NOT be merged while the platform-outage
// breaker is open — CI's "success" signal is untrustworthy during a
// correlated platform incident. The PR must stay parked at StageMerging
// (not StageFailed — unlike handleCIFailed's hold, this isn't terminal and
// needs no BreakerHoldActive/re-drive step, see handleMerging's doc
// comment) so it is simply retried automatically once the breaker closes,
// and the suppression must not count against MergeAttempts.
func TestHandleMerging_PlatformBreakerOpen_SuppressesMerge(t *testing.T) {
	var mergeCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha99/check-runs" && r.Method == http.MethodGet:
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "test", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/99/merge" && r.Method == http.MethodPut:
			mergeCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"merged":true}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	// Pre-correlate three OTHER, unrelated PRs directly against the shared
	// breaker so it is open going into handleMerging, independent of this
	// PR's own (green) CI status.
	breaker := NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)
	breaker.Observe(1, "owner/repo", FailureClassInfra)
	breaker.Observe(2, "owner/repo", FailureClassInfra)
	breaker.Observe(3, "owner/repo", FailureClassInfra)
	if !breaker.IsOpen() {
		t.Fatal("test setup: breaker should already be open from 3 pre-seeded distinct-PR observations")
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithPlatformBreaker(breaker))

	prState := &PRState{
		PRNumber:    99,
		IssueNumber: 50,
		HeadSHA:     "sha99",
		Stage:       StageMerging,
		CIStatus:    CISuccess,
		CreatedAt:   time.Now(),
	}
	c.mu.Lock()
	c.activePRs[99] = prState
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 99, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeCalled {
		t.Error("merge API was called — merges must be suppressed while the platform breaker is open")
	}

	pr, ok := c.GetPRState(99)
	if !ok {
		t.Fatal("PR state missing after ProcessPR")
	}
	if pr.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (PR stays parked at StageMerging, not escalated, for automatic retry once the breaker closes)", pr.Stage, StageMerging)
	}
	if pr.MergeAttempts != 0 {
		t.Errorf("MergeAttempts = %d, want 0 (suppression must not count against MaxMergeAttempts)", pr.MergeAttempts)
	}
}

// TestHandleMerging_PlatformBreakerNilOrClosed_IsNoOp is the control case
// confirming the suppression check in handleMerging is a true no-op both
// when no breaker is wired (nil, the default) and when a wired breaker is
// simply closed — an already-green PR must proceed to merge exactly as
// before GH-4792 in both cases (disabled-by-config / not-yet-triggered must
// not regress ordinary merges).
func TestHandleMerging_PlatformBreakerNilOrClosed_IsNoOp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		breaker *PlatformBreaker
	}{
		{name: "nil breaker (disabled by config)", breaker: nil},
		{name: "wired but closed breaker", breaker: NewPlatformBreaker(3, 15*time.Minute, 20*time.Minute, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mergeCalled bool
			prNum := 100

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == fmt.Sprintf("/repos/owner/repo/commits/sha%d/check-runs", prNum) && r.Method == http.MethodGet:
					resp := github.CheckRunsResponse{
						TotalCount: 1,
						CheckRuns: []github.CheckRun{
							{Name: "test", Status: "completed", Conclusion: "success"},
						},
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))
				case r.URL.Path == fmt.Sprintf("/repos/owner/repo/pulls/%d/merge", prNum) && r.Method == http.MethodPut:
					mergeCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"merged":true}`))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev

			var opts []ControllerOption
			if tc.breaker != nil {
				opts = append(opts, WithPlatformBreaker(tc.breaker))
			}
			c := NewController(cfg, ghClient, nil, "owner", "repo", opts...)

			prState := &PRState{
				PRNumber:     prNum,
				HeadSHA:      fmt.Sprintf("sha%d", prNum),
				Stage:        StageMerging,
				CIStatus:     CISuccess,
				CreatedAt:    time.Now(),
				TargetBranch: "main",
			}
			c.mu.Lock()
			c.activePRs[prNum] = prState
			c.mu.Unlock()

			if err := c.ProcessPR(context.Background(), prNum, nil); err != nil {
				t.Fatalf("ProcessPR returned error: %v", err)
			}

			if !mergeCalled {
				t.Error("merge API was not called — a green PR must merge normally when the breaker is nil or closed")
			}

			pr, ok := c.GetPRState(prNum)
			if !ok {
				t.Fatal("PR state missing after ProcessPR")
			}
			if pr.Stage != StageMerged {
				t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
			}
		})
	}
}
