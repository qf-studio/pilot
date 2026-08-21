package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-5066 regression test.
//
// Incident 2026-08-21: PR#5055 was stacked on sibling branch pilot/GH-5052
// (base != main). This repo's own CI workflow is scoped
// `pull_request: branches: [main]` (ci.yml:6-7), so zero checks ever ran for
// #5055 — CheckCI stayed CIPending forever. handleWaitingCI's confirmed-
// timeout branch (controller.go, deadlineExceeded) sent it straight to the
// terminal StageFailed (`case StageFailed: return nil` in ProcessPR), a dead
// end the stacked-PR protections (parkForBaseMismatch, parkForStackedSuperset)
// never got a chance to run — those are wired only inside handleMerging.
// When the founder merged the base PR, GitHub retargeted #5055 to main, but a
// StageFailed PR never re-enters ProcessPR's body, so recovery was fully
// manual.
//
// This test pins the fix's first leg: a tracked PR sitting in StageWaitingCI
// whose TargetBranch is not the repo's default branch must, at the CI-wait
// deadline, park via parkForBaseMismatch (Parked=true, EscalationReason
// naming the mismatch, labelParkedAwaitingApproval applied, exactly one
// alert/comment) instead of transitioning to StageFailed.

// gh5066PendingCheckHandler serves a repo whose CI never resolves for the
// given sha (always CheckRunInProgress) plus the issue comment/label
// endpoints parkForBaseMismatch needs.
func gh5066PendingCheckHandler(sha string, commentPosted, labelApplied *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: github.CheckRunInProgress}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			commentPosted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		case strings.HasSuffix(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			labelApplied.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}
}

func TestHandleWaitingCI_NonDefaultTargetBranch_ParksInsteadOfFailing_GH5066(t *testing.T) {
	const sha = "gh5066stacked01"
	const prNumber = 5055
	const issueNumber = 5052

	var commentPosted, labelApplied atomic.Int32
	server := httptest.NewServer(gh5066PendingCheckHandler(sha, &commentPosted, &labelApplied))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	c.mu.Lock()
	c.activePRs[prNumber] = &PRState{
		PRNumber:    prNumber,
		IssueNumber: issueNumber,
		Stage:       StageWaitingCI,
		CIStatus:    CIPending,
		// Deadline already exceeded, mirroring the PR#5055 incident: CI never
		// ran because this repo's workflow is scoped to the default branch.
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	// Stacked on a sibling branch, not the repo default ("main").
	ghPR := &github.PullRequest{
		Number: prNumber,
		Head:   github.PRRef{SHA: sha},
		Base:   github.PRRef{Ref: "pilot/GH-5052"},
	}

	if err := c.ProcessPR(context.Background(), prNumber, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageWaitingCI {
		t.Fatalf("Stage = %s, want %s — a non-default-base PR must park, not transition to a terminal stage at the CI-wait deadline", pr.Stage, StageWaitingCI)
	}
	if !pr.Parked {
		t.Error("Parked should be true — the PR must be held for a human, not silently dropped")
	}
	if !strings.HasPrefix(pr.EscalationReason, baseMismatchReasonPrefix) {
		t.Errorf("EscalationReason = %q, want prefix %q", pr.EscalationReason, baseMismatchReasonPrefix)
	}
	if !strings.Contains(pr.EscalationReason, "pilot/GH-5052") {
		t.Errorf("EscalationReason = %q, want it to name the actual target branch %q", pr.EscalationReason, "pilot/GH-5052")
	}
	if pr.TerminalLabel == github.LabelFailed {
		t.Error("TerminalLabel should not be set to failed — parking is not a terminal outcome")
	}
	if strings.Contains(pr.Error, "CI timeout") {
		t.Errorf("Error = %q should not mention CI timeout — this PR was parked for a base mismatch, not a genuine CI timeout", pr.Error)
	}
	if labelApplied.Load() != 1 {
		t.Errorf("parked-awaiting-approval label applied %d times, want 1", labelApplied.Load())
	}
	if commentPosted.Load() != 1 {
		t.Errorf("PR comment posted %d times, want 1", commentPosted.Load())
	}
	if len(sink.events) != 1 {
		t.Fatalf("alerts fired %d times, want 1", len(sink.events))
	}
}

// TestHandleWaitingCI_DefaultTargetBranch_StillFailsAtDeadline_GH5066 is the
// negative control: a PR whose base IS the repo default branch must still
// hit the pre-existing confirmed-timeout path (StageFailed) — the GH-5066
// park behavior is scoped strictly to non-default bases, it must not swallow
// genuine CI timeouts on normal PRs.
func TestHandleWaitingCI_DefaultTargetBranch_StillFailsAtDeadline_GH5066(t *testing.T) {
	const sha = "gh5066default01"
	const prNumber = 6001

	var commentPosted, labelApplied atomic.Int32
	server := httptest.NewServer(gh5066PendingCheckHandler(sha, &commentPosted, &labelApplied))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[prNumber] = &PRState{
		PRNumber:        prNumber,
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now().Add(-45 * time.Minute),
	}
	c.mu.Unlock()

	ghPR := &github.PullRequest{
		Number: prNumber,
		Head:   github.PRRef{SHA: sha},
		Base:   github.PRRef{Ref: "main"},
	}

	if err := c.ProcessPR(context.Background(), prNumber, ghPR); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s — a default-base PR past the CI-wait deadline must still fail terminally", pr.Stage, StageFailed)
	}
	if pr.Parked {
		t.Error("Parked should be false — a default-base PR has no base mismatch to park for")
	}
	if labelApplied.Load() != 0 {
		t.Errorf("parked-awaiting-approval label applied %d times, want 0", labelApplied.Load())
	}
}
