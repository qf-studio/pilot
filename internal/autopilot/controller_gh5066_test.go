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

// TestRedriveFailedPRForBaseRetarget_RevivesOnRetargetToDefault covers
// GH-5066's second leg for a PR that already reached the terminal
// StageFailed with a stale non-default TargetBranch (a pre-fix straggler, or
// one of the base-agnostic StageFailed exits legs 1/2 don't guard — see the
// function doc comment): once GitHub retargets it back to the default
// branch, it must be revived to StageWaitingCI with a fresh CI-wait clock,
// exactly like reAdoptHeldRebasePR (GH-4610) revives a rebase hold on a new
// head SHA.
func TestRedriveFailedPRForBaseRetarget_RevivesOnRetargetToDefault(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:      5055,
		IssueNumber:   5052,
		BranchName:    "pilot/GH-5055",
		HeadSHA:       "gh5066redrive01",
		TargetBranch:  "pilot/GH-5052",
		Stage:         StageFailed,
		Error:         "CI timeout after 30m0s (last confirmed status: pending)",
		TerminalLabel: github.LabelFailed,
	}

	ghPR := &github.PullRequest{Number: 5055, Base: github.PRRef{Ref: "main"}}
	c.redriveFailedPRForBaseRetarget(context.Background(), prState, ghPR)

	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
	}
	if prState.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", prState.TargetBranch, "main")
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty after redrive", prState.Error)
	}
	if prState.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty after redrive — no longer terminal", prState.TerminalLabel)
	}
	if prState.CIWaitStartedAt.IsZero() {
		t.Error("CIWaitStartedAt should be reset for a fresh CI-wait window")
	}
	// AddPRComment posts to the PR's own issues/<PRNumber>/comments endpoint
	// (a PR is an "issue" in GitHub's model) — not the linked ticket's
	// IssueNumber, which can differ (5052 above).
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/5055/comments"); n != 1 {
		t.Errorf("PR comment calls = %d, want 1 (redrive notice)", n)
	}
}

// TestRedriveFailedPRForBaseRetarget_IgnoresDefaultBaseFailures is the
// negative control: a StageFailed PR whose TargetBranch was ALREADY the
// default branch failed for a genuine, unrelated reason (real CI failure,
// merge-attempt cap, config mismatch, etc.) — GitHub's base field can't
// resolve that, so it must stay parked for a human regardless of what the
// fresh ghPR reports.
func TestRedriveFailedPRForBaseRetarget_IgnoresDefaultBaseFailures(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:     6001,
		IssueNumber:  6001,
		TargetBranch: "main",
		Stage:        StageFailed,
		Error:        "CI timeout after 30m0s (last confirmed status: failure)",
	}

	ghPR := &github.PullRequest{Number: 6001, Base: github.PRRef{Ref: "main"}}
	c.redriveFailedPRForBaseRetarget(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (genuine failure, not base-mismatch-shaped)", prState.Stage)
	}
	if prState.Error == "" {
		t.Error("Error should be preserved — this failure was never redriven")
	}
}

// TestRedriveFailedPRForBaseRetarget_NotYetRetargeted covers the "still
// stacked" case: a StageFailed PR whose TargetBranch is non-default AND
// GitHub still reports that same non-default base must stay put — there is
// nothing to redrive until an actual retarget happens.
func TestRedriveFailedPRForBaseRetarget_NotYetRetargeted(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:     5056,
		IssueNumber:  5053,
		TargetBranch: "pilot/GH-5053",
		Stage:        StageFailed,
		Error:        "CI timeout after 30m0s (last confirmed status: pending)",
	}

	ghPR := &github.PullRequest{Number: 5056, Base: github.PRRef{Ref: "pilot/GH-5053"}}
	c.redriveFailedPRForBaseRetarget(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (base unchanged, still stacked)", prState.Stage)
	}
	if prState.TargetBranch != "pilot/GH-5053" {
		t.Errorf("TargetBranch = %q, want unchanged %q", prState.TargetBranch, "pilot/GH-5053")
	}
}

// TestController_ProcessAllPRs_RedrivesFailedPRAndMerges is an end-to-end
// check through processAllPRs (the real poll loop entry point, GH-5066): a
// StageFailed PR retargeted to the default branch must be revived AND
// actually re-enter processing within the same tick (mirrors
// TestController_ProcessAllPRs_ReAdoptsHeldRebasePR's shape for GH-4610),
// then — once CI genuinely resolves against the corrected base — proceed all
// the way through to a completed merge.
func TestController_ProcessAllPRs_RedrivesFailedPRAndMerges(t *testing.T) {
	const sha = "gh5066redrive02"
	const prNumber = 5057

	var ciResolved atomic.Bool
	var mergeCalled, commentPosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5057" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:  prNumber,
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/5057",
				Head:    github.PRRef{SHA: sha, Ref: "pilot/GH-5057"},
				Base:    github.PRRef{Ref: "main"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			status := github.CheckRunInProgress
			conclusion := ""
			if ciResolved.Load() {
				status = github.CheckRunCompleted
				conclusion = github.ConclusionSuccess
			}
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "ci", Status: status, Conclusion: conclusion}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/5057/files":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/pulls/5057/merge" && r.Method == http.MethodPut:
			mergeCalled.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			commentPosts.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:      prNumber,
		IssueNumber:   prNumber,
		PRURL:         "https://github.com/owner/repo/pull/5057",
		BranchName:    "pilot/GH-5057",
		HeadSHA:       sha,
		TargetBranch:  "pilot/GH-9999", // stale: base was non-default when this failed
		Stage:         StageFailed,
		Error:         "CI timeout after 30m0s (last confirmed status: pending)",
		TerminalLabel: github.LabelFailed,
		CreatedAt:     time.Now(),
	}
	c.mu.Lock()
	c.activePRs[prNumber] = prState
	c.mu.Unlock()

	// Tick 1: ghPR now reports base=main — the redrive must fire and this
	// same tick must carry the PR through ProcessPR into StageWaitingCI.
	c.processAllPRs(context.Background())

	got, ok := c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if got.Stage != StageWaitingCI {
		t.Fatalf("tick 1: Stage = %v, want StageWaitingCI (redriven and processed in the same tick)", got.Stage)
	}
	if got.TargetBranch != "main" {
		t.Errorf("tick 1: TargetBranch = %q, want %q", got.TargetBranch, "main")
	}
	if commentPosts.Load() != 1 {
		t.Errorf("tick 1: comment calls = %d, want 1 (redrive notice)", commentPosts.Load())
	}

	// Now let CI actually resolve against the corrected base, exactly as it
	// would in reality. The PR must proceed all the way through to a
	// completed merge — driving ProcessPR repeatedly, same as the real poll
	// loop, since each call advances at most one stage.
	ciResolved.Store(true)
	ghPRRetargeted := &github.PullRequest{Number: prNumber, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	const maxTicks = 6
	for i := 0; i < maxTicks; i++ {
		if err := c.ProcessPR(context.Background(), prNumber, ghPRRetargeted); err != nil {
			t.Fatalf("post-redrive tick %d: ProcessPR: %v", i, err)
		}
		got, ok = c.GetPRState(prNumber)
		if !ok {
			t.Fatal("PR should still be tracked")
		}
		if got.Stage == StageFailed {
			t.Fatalf("post-redrive tick %d: Stage = %s — PR must merge cleanly once retargeted, CI passing", i, got.Stage)
		}
		if got.Stage == StageMerged {
			break
		}
	}
	got, ok = c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if got.Stage != StageMerged {
		t.Fatalf("Stage = %s, want StageMerged — PR should have proceeded through to a completed merge", got.Stage)
	}
	if mergeCalled.Load() != 1 {
		t.Errorf("merge called %d times, want 1", mergeCalled.Load())
	}
}

// TestController_ProcessAllPRs_RedrivenPR_NoChecksAfterRetarget_RefailsCleanly
// pins the GH-5066 acceptance-criterion-3 decision explicitly: retargeting a
// PR does not itself guarantee GitHub runs any check (see the "no checks
// after retarget" decision documented on redriveFailedPRForBaseRetarget) —
// this repo's CI only triggers on opened/synchronize/reopened, not the
// "edited" action a bare retarget fires. If checks genuinely never post
// after redrive, the PR must time out again through the ordinary
// TargetBranch==default CI-timeout branch and land back at StageFailed —
// not loop forever, and not get redriven a second time (its TargetBranch is
// now the default branch, so redriveFailedPRForBaseRetarget's own
// precondition no longer matches).
func TestController_ProcessAllPRs_RedrivenPR_NoChecksAfterRetarget_RefailsCleanly(t *testing.T) {
	const sha = "gh5066redrive03"
	const prNumber = 5058

	var commentPosts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			// Zero checks ever post for this SHA — mirrors the PR#5055
			// incident where the workflow scoped to the default branch never
			// fired for the stacked-base PR, and a bare retarget alone (no new
			// push) doesn't trigger a fresh run either.
			resp := github.CheckRunsResponse{TotalCount: 0}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			commentPosts.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:      prNumber,
		IssueNumber:   prNumber,
		HeadSHA:       sha,
		TargetBranch:  "pilot/GH-9998",
		Stage:         StageFailed,
		Error:         "CI timeout after 30m0s (last confirmed status: pending)",
		TerminalLabel: github.LabelFailed,
	}

	ghPR := &github.PullRequest{Number: prNumber, Head: github.PRRef{SHA: sha}, Base: github.PRRef{Ref: "main"}}
	c.redriveFailedPRForBaseRetarget(context.Background(), prState, ghPR)

	if prState.Stage != StageWaitingCI {
		t.Fatalf("Stage = %v, want StageWaitingCI immediately after redrive", prState.Stage)
	}

	// Fast-forward the CI-wait clock past the deadline, exactly as the
	// existing GH-5066/GH-4851 tests do, then drive one more ProcessPR tick
	// with zero checks ever posted.
	prState.CIWaitStartedAt = time.Now().Add(-45 * time.Minute)
	c.mu.Lock()
	c.activePRs[prNumber] = prState
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), prNumber, ghPR); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}

	got, ok := c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if got.Stage != StageFailed {
		t.Fatalf("Stage = %v, want StageFailed — no checks ever posted, the redriven CI-wait must time out again, not loop forever", got.Stage)
	}
	if !strings.Contains(got.Error, "CI timeout") {
		t.Errorf("Error = %q, want it to mention the CI timeout (a genuine re-fail, not a base-mismatch park)", got.Error)
	}
	if got.Parked {
		t.Error("Parked should be false — TargetBranch is now the default branch, so this is a genuine timeout, not a base-mismatch park")
	}

	// One more processAllPRs tick must NOT redrive it a second time — the
	// precondition (stale non-default TargetBranch) no longer holds since
	// TargetBranch is now "main".
	c.processAllPRs(context.Background())
	got, ok = c.GetPRState(prNumber)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if got.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed — must not be redriven a second time", got.Stage)
	}
}
