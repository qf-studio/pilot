package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4851 regression tests.
//
// 2026-08-11 incident: the reconciler adopted open PR#4846 (sha fe0f1d5) at
// 14:35Z; all 7 checks completed green by 14:43Z; the CI wait clock started
// at 14:57Z; a suppressed processing window (rate-limit cooldown/breaker)
// delayed the first-ever handleWaitingCI evaluation until 15:27Z, which
// declared "CI timeout after 30m0s" and set Stage=failed WITHOUT ever
// consulting CI — the deadline check ran before the only CI read in the
// function, so a same-tick CheckCI never happened. The persisted
// ci_status=pending fingerprint is the adoption-time CIPending default,
// never overwritten by a real poll. Closing the stranded PR afterward routed
// through notifyExternalClose with no TerminalLabel recorded, defaulting the
// issue to pilot-retry-ready and re-dispatching already-shipped work.
//
// These tests cover: (1) a deadline-exceeded tick must consult CI before
// declaring timeout, and success wins over an expired clock; (2) a
// same-tick read that resolves failure also wins over the deadline, so it
// is never mislabeled a "CI timeout"; (3) only a same-tick CIPending/
// CIRunning read can confirm a timeout, and that branch records a
// TerminalLabel plus the real polled status (not the blind default); (4)
// external close after a confirmed timeout does not arm pilot-retry-ready;
// (5)/(6) the adoption paths (reconciler, startup scan) seed the wait clock
// from GitHub's own last-activity evidence instead of "now"; (7) an
// end-to-end reproduction of the PR#4846 shape.

func gh4851CheckRunsHandler(sha string, run github.CheckRun) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs" {
			resp := github.CheckRunsResponse{TotalCount: 1, CheckRuns: []github.CheckRun{run}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}
}

// TestHandleWaitingCI_DeadlineExceeded_ConfirmedSuccess_GH4851 is the primary
// acceptance test: a PR whose CI already resolved CISuccess, evaluated for
// the first time only after the wait deadline has already elapsed, must
// reach StageCIPassed — not StageFailed. This fails on pre-fix main, where
// the deadline check ran unconditionally before any CheckCI call.
func TestHandleWaitingCI_DeadlineExceeded_ConfirmedSuccess_GH4851(t *testing.T) {
	const sha = "gh4851pass01"
	server := httptest.NewServer(gh4851CheckRunsHandler(sha, github.CheckRun{
		Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess,
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[4846] = &PRState{
		PRNumber: 4846,
		HeadSHA:  sha,
		Stage:    StageWaitingCI,
		CIStatus: CIPending, // adoption-time blind default, never yet confirmed
		// Deadline already exceeded before this tick's CheckCI ever runs —
		// mirrors the suppressed-window gap between adoption and first poll.
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 4846, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(context.Background(), 4846, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(4846)
	if !ok {
		t.Fatal("PR 4846 no longer tracked")
	}
	if pr.Stage != StageCIPassed {
		t.Fatalf("Stage = %s, want %s — a same-tick CheckCI success must win over an already-expired deadline (error=%q)", pr.Stage, StageCIPassed, pr.Error)
	}
}

// TestHandleWaitingCI_DeadlineExceeded_ConfirmedFailure_GH4851 verifies the
// symmetric case: a same-tick read that resolves CIFailure also takes
// priority over the deadline, so a genuinely-failed PR routes into the
// normal CI-fix cascade (StageCIFailed) rather than being misdiagnosed as a
// bare "CI timeout" with no failing-check evidence attached.
func TestHandleWaitingCI_DeadlineExceeded_ConfirmedFailure_GH4851(t *testing.T) {
	const sha = "gh4851fail01"
	server := httptest.NewServer(gh4851CheckRunsHandler(sha, github.CheckRun{
		Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure,
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[4847] = &PRState{
		PRNumber:        4847,
		HeadSHA:         sha,
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 4847, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(context.Background(), 4847, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(4847)
	if !ok {
		t.Fatal("PR 4847 no longer tracked")
	}
	if pr.Stage != StageCIFailed {
		t.Fatalf("Stage = %s, want %s — a same-tick CheckCI failure must win over an expired deadline instead of being folded into a bare timeout", pr.Stage, StageCIFailed)
	}
	if strings.Contains(pr.Error, "CI timeout") {
		t.Errorf("Error = %q should not mention CI timeout — this PR failed CI, it did not time out", pr.Error)
	}
}

// TestHandleWaitingCI_DeadlineExceeded_ConfirmedPending_DeclaresTimeout_GH4851
// covers the genuine-timeout branch: only when the same-tick read comes back
// CIPending/CIRunning (still unresolved) may the deadline fire. That branch
// must (a) set a TerminalLabel so a later external close can't default to
// pilot-retry-ready, and (b) leave behind a persisted-state fingerprint that
// proves a real poll happened — CIStatus reflects the just-read status (not
// the adoption-time CIPending default) and LastChecked is freshly non-zero.
func TestHandleWaitingCI_DeadlineExceeded_ConfirmedPending_DeclaresTimeout_GH4851(t *testing.T) {
	const sha = "gh4851pend01"
	server := httptest.NewServer(gh4851CheckRunsHandler(sha, github.CheckRun{
		Name: "ci", Status: github.CheckRunInProgress,
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	before := time.Now()
	c.mu.Lock()
	c.activePRs[4848] = &PRState{
		PRNumber:        4848,
		HeadSHA:         sha,
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 4848, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(context.Background(), 4848, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(4848)
	if !ok {
		t.Fatal("PR 4848 no longer tracked")
	}
	if pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s — CI is still genuinely unresolved past the deadline, this must time out", pr.Stage, StageFailed)
	}
	if pr.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q — a confirmed CI timeout is terminal and must not be silently re-queued", pr.TerminalLabel, github.LabelFailed)
	}
	// checkAutoDiscoveredRuns aggregates any still-pending/running check run into
	// CIPending at the aggregate level (ci_monitor.go) — CIRunning is a per-run,
	// not an aggregate, value. What matters here is that this is the *freshly
	// read* same-tick status, not the adoption-time CIPending default carried
	// over unread — which LastChecked (below) is what actually proves.
	if pr.CIStatus != CIPending {
		t.Errorf("CIStatus = %q, want %q — must record the real same-tick poll result", pr.CIStatus, CIPending)
	}
	if pr.LastChecked.Before(before) {
		t.Errorf("LastChecked = %v, want a timestamp at/after %v — proves a real CI read happened this tick, distinguishing 'polled and pending' from 'never successfully polled'", pr.LastChecked, before)
	}
	if !strings.Contains(pr.Error, "CI timeout") {
		t.Errorf("Error = %q, want it to mention CI timeout", pr.Error)
	}
}

// TestNotifyExternalClose_AfterConfirmedTimeout_DoesNotArmRetryReady_GH4851
// reproduces the PR#4846 close asymmetry: once handleWaitingCI has declared
// a confirmed timeout (and recorded TerminalLabel), a human closing the
// stranded PR must route the source issue to pilot-failed, never
// pilot-retry-ready — re-dispatching work that (in the real incident) had
// already shipped.
func TestNotifyExternalClose_AfterConfirmedTimeout_DoesNotArmRetryReady_GH4851(t *testing.T) {
	const sha = "gh4851close01"
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: github.CheckRunInProgress}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/4849" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 4849, State: github.StateOpen})
		case r.URL.Path == "/repos/owner/repo/issues/4849/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:        4846,
		IssueNumber:     4849,
		HeadSHA:         sha,
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Lock()
	c.activePRs[4846] = prState
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 4846, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	if err := c.ProcessPR(context.Background(), 4846, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}
	if prState.Stage != StageFailed || prState.TerminalLabel != github.LabelFailed {
		t.Fatalf("setup failed: Stage=%s TerminalLabel=%q, want StageFailed with TerminalLabel=pilot-failed", prState.Stage, prState.TerminalLabel)
	}

	// Human closes the stranded PR — notifyExternalClose must consult the
	// TerminalLabel recorded above.
	c.notifyExternalClose(context.Background(), prState)

	foundFailed, foundRetryReady := false, false
	for _, l := range labelsAdded {
		if l == github.LabelFailed {
			foundFailed = true
		}
		if l == github.LabelRetryReady {
			foundRetryReady = true
		}
	}
	if !foundFailed {
		t.Errorf("expected issue to be labeled %q, got labels added: %v", github.LabelFailed, labelsAdded)
	}
	if foundRetryReady {
		t.Errorf("issue must NOT be labeled %q after a confirmed CI timeout — this is the PR#4846 stranded-close shape; labels added: %v", github.LabelRetryReady, labelsAdded)
	}
}

// TestReconcileOrphanPRs_SeedsCIWaitClockFromUpdatedAt_GH4851 verifies that
// adopting an orphan PR seeds CIWaitStartedAt from GitHub's own UpdatedAt
// evidence rather than leaving it zero (to later default to time.Now() in
// handlePRCreated) — the reconciler-adoption half of the PR#4846 incident,
// where the clock started at PR-notice time (14:57Z) rather than reflecting
// when the PR had actually last changed.
func TestReconcileOrphanPRs_SeedsCIWaitClockFromUpdatedAt_GH4851(t *testing.T) {
	staleUpdatedAt := time.Now().Add(-22 * time.Minute).Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{
					Number:    4846,
					HTMLURL:   "https://github.com/owner/repo/pull/4846",
					Head:      github.PRRef{Ref: "pilot/GH-4840", SHA: "fe0f1d5"},
					Base:      github.PRRef{Ref: "main"},
					UpdatedAt: staleUpdatedAt.Format(time.RFC3339),
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.reconcileOrphanPRs(context.Background())

	pr, ok := c.GetPRState(4846)
	if !ok {
		t.Fatal("reconciler did not register PR 4846")
	}
	if pr.CIWaitStartedAt.IsZero() {
		t.Fatal("CIWaitStartedAt was not seeded from adoption evidence")
	}
	if diff := pr.CIWaitStartedAt.Sub(staleUpdatedAt); diff < -time.Second || diff > time.Second {
		t.Errorf("CIWaitStartedAt = %v, want ~%v (PR's own UpdatedAt) — must not be seeded to time.Now()", pr.CIWaitStartedAt, staleUpdatedAt)
	}
}

// TestScanExistingPRs_SeedsCIWaitClockFromUpdatedAt_GH4851 is the
// startup-scan counterpart to the reconciler test above.
func TestScanExistingPRs_SeedsCIWaitClockFromUpdatedAt_GH4851(t *testing.T) {
	staleUpdatedAt := time.Now().Add(-40 * time.Minute).Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{
					Number:    4850,
					HTMLURL:   "https://github.com/owner/repo/pull/4850",
					Head:      github.PRRef{Ref: "pilot/GH-4845", SHA: "abc9999"},
					Base:      github.PRRef{Ref: "main"},
					UpdatedAt: staleUpdatedAt.Format(time.RFC3339),
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	if err := c.ScanExistingPRs(context.Background()); err != nil {
		t.Fatalf("ScanExistingPRs returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(4850)
	if !ok {
		t.Fatal("scan did not register PR 4850")
	}
	if diff := pr.CIWaitStartedAt.Sub(staleUpdatedAt); diff < -time.Second || diff > time.Second {
		t.Errorf("CIWaitStartedAt = %v, want ~%v (PR's own UpdatedAt) — must not be seeded to time.Now()", pr.CIWaitStartedAt, staleUpdatedAt)
	}
}

// TestGH4851_EndToEnd_AdoptedGreenPR_PastDeadline_ReachesCIPassed reproduces
// the full PR#4846 shape in one flow: the reconciler adopts an orphan PR
// whose UpdatedAt is already older than the CI wait timeout (the suppressed-
// window gap), and the very first ProcessPR tick — where CheckCI reports the
// checks are already green — must resolve straight to StageCIPassed rather
// than declaring a blind timeout.
func TestGH4851_EndToEnd_AdoptedGreenPR_PastDeadline_ReachesCIPassed(t *testing.T) {
	const sha = "fe0f1d5end2end"
	staleUpdatedAt := time.Now().Add(-45 * time.Minute).Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls":
			prs := []*github.PullRequest{
				{
					Number:    4846,
					HTMLURL:   "https://github.com/owner/repo/pull/4846",
					Head:      github.PRRef{Ref: "pilot/GH-4840", SHA: sha},
					Base:      github.PRRef{Ref: "main"},
					UpdatedAt: staleUpdatedAt.Format(time.RFC3339),
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
		case "/repos/owner/repo/commits/" + sha + "/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Reconciler adopts the orphan PR — CIWaitStartedAt is seeded from its
	// stale UpdatedAt, already past the 30m deadline.
	c.reconcileOrphanPRs(context.Background())

	ghPR := &github.PullRequest{Number: 4846, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}

	// First tick: StagePRCreated -> StageWaitingCI (must preserve the seeded
	// clock, not reset it to time.Now()).
	if err := c.ProcessPR(context.Background(), 4846, ghPR); err != nil {
		t.Fatalf("first ProcessPR (pr_created) error = %v", err)
	}
	pr, ok := c.GetPRState(4846)
	if !ok {
		t.Fatal("PR 4846 no longer tracked after first tick")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("stage after first tick = %s, want %s", pr.Stage, StageWaitingCI)
	}
	if diff := pr.CIWaitStartedAt.Sub(staleUpdatedAt); diff < -time.Second || diff > time.Second {
		t.Fatalf("handlePRCreated clobbered the seeded CIWaitStartedAt: got %v, want ~%v", pr.CIWaitStartedAt, staleUpdatedAt)
	}

	// Second tick: handleWaitingCI evaluates with the deadline already
	// exceeded — must consult CI (green) before declaring timeout.
	if err := c.ProcessPR(context.Background(), 4846, ghPR); err != nil {
		t.Fatalf("second ProcessPR (waiting_ci) error = %v", err)
	}
	pr, ok = c.GetPRState(4846)
	if !ok {
		t.Fatal("PR 4846 no longer tracked after second tick")
	}
	if pr.Stage != StageCIPassed {
		t.Fatalf("final stage = %s, want %s (error=%q) — the PR#4846 shape: adopted PR with green checks must not time out blind", pr.Stage, StageCIPassed, pr.Error)
	}
}
