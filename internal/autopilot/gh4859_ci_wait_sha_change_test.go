package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-4859 regression tests.
//
// Post-merge review of PR#4857 (GH-4855) found a fourth CI-wait re-entry
// vector the fix's three Stage=StageWaitingCI assignment sites could not
// see: handleWaitingCI (controller.go) computes deadlineExceeded from the
// PR's existing CIWaitStartedAt BEFORE the same-tick GH-419/GH-457 HeadSHA
// refresh runs. That refresh does not change Stage — the PR was already in
// StageWaitingCI and stays there — so it is not a re-entry site at all, and
// PR#4857 never reset the clock there. Scenario: a PR sits in
// StageWaitingCI past its deadline; a post-creation commit (self-review
// push, human push) lands on the branch and kicks off a fresh CI run; the
// next tick refreshes HeadSHA to the new commit and CheckCI correctly
// reads the new run as pending — but deadlineExceeded, measured from the
// ORIGINAL wait entry, is already true, producing an instant CONFIRMED
// timeout (StageFailed, TerminalLabel=pilot-failed) against a CI run that
// had not had any chance to complete. These tests cover: (1) a changed
// HeadSHA whose new run is still pending must NOT time out, and the clock
// must be reset to measure the new run; (2) the GH-4851 same-tick-success-
// wins ordering must still hold when the SHA also changed this tick.

func gh4859CheckRunsHandler(sha string, run github.CheckRun) http.HandlerFunc {
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

// TestHandleWaitingCI_HeadSHAChangeMidWait_ResetsDeadline_GH4859 is the
// primary acceptance test: a PR whose wait clock is already 45m stale (past
// the 30m default deadline) picks up a new HeadSHA this tick whose CI run
// is still in_progress. It must stay in StageWaitingCI (not be declared a
// confirmed timeout), and CIWaitStartedAt must be reset to at/after the SHA
// change so the new run gets a full timeout window.
func TestHandleWaitingCI_HeadSHAChangeMidWait_ResetsDeadline_GH4859(t *testing.T) {
	const oldSHA = "gh4859old0001"
	const newSHA = "gh4859new0001"
	server := httptest.NewServer(gh4859CheckRunsHandler(newSHA, github.CheckRun{
		Name: "ci", Status: github.CheckRunInProgress,
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	before := time.Now()
	c.mu.Lock()
	c.activePRs[4859] = &PRState{
		PRNumber: 4859,
		HeadSHA:  oldSHA,
		Stage:    StageWaitingCI,
		CIStatus: CIPending,
		// Deadline already exceeded against the ORIGINAL sha's wait entry —
		// mirrors a PR that sat in StageWaitingCI for 45m before a
		// post-creation commit landed.
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	// GitHub now reports a new HeadSHA — the branch moved mid-wait.
	ghPR := &github.PullRequest{Number: 4859, Head: github.PRRef{SHA: newSHA}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(context.Background(), 4859, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(4859)
	if !ok {
		t.Fatal("PR 4859 no longer tracked")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s — a changed HeadSHA whose new CI run is still pending must not be declared a timeout (error=%q)", pr.Stage, StageWaitingCI, pr.Error)
	}
	if pr.TerminalLabel == github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want unset — this PR did not time out", pr.TerminalLabel)
	}
	if pr.HeadSHA != newSHA {
		t.Errorf("HeadSHA = %q, want %q — must refresh to the new commit", pr.HeadSHA, newSHA)
	}
	if pr.CIWaitStartedAt.Before(before) {
		t.Fatalf("CIWaitStartedAt = %v, want reset to >= SHA-change time %v — a changed head means a new CI run and must get a full timeout window, not carry over the stale original clock", pr.CIWaitStartedAt, before)
	}
}

// TestHandleWaitingCI_HeadSHAChangeMidWait_ConfirmedSuccessSameTick_GH4859
// guards the GH-4851 ordering: even when the SHA also changed this tick,
// a same-tick CheckCI read that resolves CISuccess on the new SHA must
// still win and land the PR in StageCIPassed, not fall through to a
// timeout evaluation.
func TestHandleWaitingCI_HeadSHAChangeMidWait_ConfirmedSuccessSameTick_GH4859(t *testing.T) {
	const oldSHA = "gh4859old0002"
	const newSHA = "gh4859new0002"
	server := httptest.NewServer(gh4859CheckRunsHandler(newSHA, github.CheckRun{
		Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess,
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[4860] = &PRState{
		PRNumber:        4860,
		HeadSHA:         oldSHA,
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{Number: 4860, Head: github.PRRef{SHA: newSHA}, Base: github.PRRef{Ref: "main"}}

	if err := c.ProcessPR(context.Background(), 4860, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(4860)
	if !ok {
		t.Fatal("PR 4860 no longer tracked")
	}
	if pr.Stage != StageCIPassed {
		t.Fatalf("Stage = %s, want %s — a same-tick CheckCI success on the new SHA must win in the same tick the SHA changed", pr.Stage, StageCIPassed)
	}
	if pr.HeadSHA != newSHA {
		t.Errorf("HeadSHA = %q, want %q", pr.HeadSHA, newSHA)
	}
}
