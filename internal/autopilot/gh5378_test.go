package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestRedriveHeldPR_RevivesOnNewHead covers the GH-5378 fix itself,
// generalized by GH-5643 to all four redrivable CI-side holds: a PR held via
// escalateAndHold(redrivable=true), once its branch receives a new commit,
// must be revived to StageWaitingCI for fresh CI — not left stranded even
// after CI turns green, as PR #5356 was for 85 minutes until merged by hand
// and pilot-console PR #388 was until merged by hand on 2026-10-12.
func TestRedriveHeldPR_RevivesOnNewHead(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		holdLabels string
		wantDelete []string
	}{
		{"zero gathered evidence", "CI failure with zero gathered evidence", labelNeedsHuman, []string{labelNeedsHuman}},
		{"CI fix size guard", "CI fix size guard fired", labelNeedsHuman, []string{labelNeedsHuman}},
		{"CI-fix continuation declined", "CI-fix continuation declined at preflight", labelNeedsHuman + ",extra-hold-label", []string{labelNeedsHuman, "extra-hold-label"}},
		{"review-fix continuation declined", "review-fix continuation declined at preflight", labelNeedsHuman, []string{labelNeedsHuman}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, srv := newRecordingGHServer()
			defer srv.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:         5356,
				IssueNumber:      5351,
				BranchName:       "pilot/GH-5351",
				HeadSHA:          "old-sha",
				Stage:            StageFailed,
				Error:            tt.reason,
				HoldActive:       true,
				HoldHeadSHA:      "old-sha",
				HoldLabels:       tt.holdLabels,
				TerminalLabel:    github.LabelFailed,
				RebaseAttempts:   1,
				MergeAttempts:    2,
				ReadoptCount:     1,
				HoldRedriveCount: 0,
			}

			ghPR := &github.PullRequest{Number: 5356, State: "open", Head: github.PRRef{SHA: "new-sha"}}
			c.redriveHeldPR(context.Background(), prState, ghPR)

			if prState.Stage != StageWaitingCI {
				t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
			}
			if prState.HeadSHA != "new-sha" {
				t.Errorf("HeadSHA = %q, want %q", prState.HeadSHA, "new-sha")
			}
			if prState.HoldActive {
				t.Error("HoldActive should be cleared after re-adoption")
			}
			if prState.HoldHeadSHA != "" {
				t.Errorf("HoldHeadSHA = %q, want empty after re-adoption", prState.HoldHeadSHA)
			}
			if prState.HoldRedriveCount != 1 {
				t.Errorf("HoldRedriveCount = %d, want 1", prState.HoldRedriveCount)
			}
			if prState.Error != "" {
				t.Errorf("Error = %q, want empty after re-adoption", prState.Error)
			}
			if prState.TerminalLabel != "" {
				t.Errorf("TerminalLabel = %q, want empty after re-adoption", prState.TerminalLabel)
			}
			// Preserved counters, matching reAdoptHeldRebasePR (GH-4610).
			if prState.RebaseAttempts != 1 || prState.MergeAttempts != 2 || prState.ReadoptCount != 1 {
				t.Errorf("counters not preserved: rebase=%d merge=%d readopt=%d",
					prState.RebaseAttempts, prState.MergeAttempts, prState.ReadoptCount)
			}
			if prState.CIWaitStartedAt.IsZero() {
				t.Error("CIWaitStartedAt should be reset for fresh CI monitoring")
			}
			if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/5356/comments"); n != 1 {
				t.Errorf("PR comment calls = %d, want 1 (re-adoption notice)", n)
			}
			for _, l := range tt.wantDelete {
				if n := rec.count(http.MethodDelete, "/repos/owner/repo/issues/5351/labels/"+l); n != 1 {
					t.Errorf("%s removal calls on the issue = %d, want 1", l, n)
				}
			}
		})
	}
}

// TestRedriveHeldPR_EmptyHoldHeadSHA_AdoptsBaselineWithoutRedrive: a hold row
// with no recorded baseline adopts the live head as its baseline and does not
// redrive on that tick; a later head change then redrives.
func TestRedriveHeldPR_EmptyHoldHeadSHA_AdoptsBaselineWithoutRedrive(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()
	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{PRNumber: 70, IssueNumber: 70, HeadSHA: "old-sha", Stage: StageFailed, HoldActive: true}
	c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 70, State: "open", Head: github.PRRef{SHA: "live-sha"}})

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (baseline adoption must not redrive)", prState.Stage)
	}
	if prState.HoldHeadSHA != "live-sha" {
		t.Errorf("HoldHeadSHA = %q, want baseline %q", prState.HoldHeadSHA, "live-sha")
	}
	if prState.HoldRedriveCount != 0 {
		t.Errorf("HoldRedriveCount = %d, want 0", prState.HoldRedriveCount)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/issues/70/labels/"+labelNeedsHuman); n != 0 {
		t.Errorf("label removals = %d, want 0", n)
	}

	c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 70, State: "open", Head: github.PRRef{SHA: "newer-sha"}})
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI after a head change past the baseline", prState.Stage)
	}
}

// TestRedriveHeldPR_IgnoresMergedOrClosedPR: a head change on a PR that is
// already merged or closed must never re-enter the pipeline.
func TestRedriveHeldPR_IgnoresMergedOrClosedPR(t *testing.T) {
	tests := []struct {
		name string
		ghPR *github.PullRequest
	}{
		{"closed", &github.PullRequest{Number: 71, State: "closed", Head: github.PRRef{SHA: "new-sha"}}},
		{"merged", &github.PullRequest{Number: 71, State: "closed", Merged: true, Head: github.PRRef{SHA: "new-sha"}}},
		{"merged flag only", &github.PullRequest{Number: 71, State: "open", Merged: true, Head: github.PRRef{SHA: "new-sha"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, srv := newRecordingGHServer()
			defer srv.Close()
			c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

			prState := &PRState{PRNumber: 71, IssueNumber: 71, HeadSHA: "old-sha", Stage: StageFailed, HoldActive: true, HoldHeadSHA: "old-sha"}
			c.redriveHeldPR(context.Background(), prState, tt.ghPR)

			if prState.Stage != StageFailed || !prState.HoldActive || prState.HeadSHA != "old-sha" {
				t.Errorf("merged/closed PR must stay untouched: stage=%v holdActive=%v head=%q", prState.Stage, prState.HoldActive, prState.HeadSHA)
			}
		})
	}
}

// TestRedriveHeldPR_IgnoresPostMergeHolds: a post-merge hold never sets
// HoldActive (escalateAndHold redrivable=false) and a PostMergeSHA row never
// redrives even when the head differs.
func TestRedriveHeldPR_IgnoresPostMergeHolds(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()
	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{PRNumber: 72, IssueNumber: 72, HeadSHA: "old-sha", PostMergeSHA: "main-sha", Stage: StagePostMergeCI}
	c.escalateAndHold(context.Background(), prState, "post-merge CI failure with zero gathered evidence", []string{labelNeedsHuman}, "", false)
	if prState.HoldActive {
		t.Fatal("post-merge hold must not set HoldActive")
	}

	c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 72, State: "open", Head: github.PRRef{SHA: "new-sha"}})
	if prState.Stage != StageFailed || prState.HeadSHA != "old-sha" {
		t.Errorf("post-merge hold must stay parked: stage=%v head=%q", prState.Stage, prState.HeadSHA)
	}

	// Even a (hypothetically) active hold with a PostMergeSHA is not revived.
	prState.HoldActive = true
	prState.HoldHeadSHA = "old-sha"
	c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 72, State: "open", Head: github.PRRef{SHA: "new-sha"}})
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (PostMergeSHA set)", prState.Stage)
	}
}

// TestRedriveHeldPR_CapReached_StaysParked: HoldRedriveCount at the cap blocks
// a further redrive, and two successive redrives reach exactly that cap.
func TestRedriveHeldPR_CapReached_StaysParked(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()
	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{PRNumber: 73, IssueNumber: 73, HeadSHA: "sha-0", Stage: StageFailed, HoldActive: true, HoldHeadSHA: "sha-0"}
	for i, head := range []string{"sha-1", "sha-2"} {
		c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 73, State: "open", Head: github.PRRef{SHA: head}})
		if prState.Stage != StageWaitingCI || prState.HoldRedriveCount != i+1 {
			t.Fatalf("redrive %d: stage=%v count=%d", i+1, prState.Stage, prState.HoldRedriveCount)
		}
		// The PR fails again at the new head and is held again.
		c.escalateAndHold(context.Background(), prState, "CI failure with zero gathered evidence", []string{labelNeedsHuman}, "", true)
	}

	c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 73, State: "open", Head: github.PRRef{SHA: "sha-3"}})
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (cap reached)", prState.Stage)
	}
	if prState.HoldRedriveCount != 2 {
		t.Errorf("HoldRedriveCount = %d, want 2", prState.HoldRedriveCount)
	}
}

// TestRedriveHeldPR_ExhaustedRetryLadder_LeavesLabels: an issue already at
// pilot-failed-retry-exhausted keeps its hold labels, but the PR still
// re-enters CI.
func TestRedriveHeldPR_ExhaustedRetryLadder_LeavesLabels(t *testing.T) {
	var mu sync.Mutex
	var deletes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/74":
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 74, State: "open", Labels: []github.Label{{Name: github.LabelFailedRetryExhausted}, {Name: labelNeedsHuman}}})
		case r.Method == http.MethodDelete:
			mu.Lock()
			deletes++
			mu.Unlock()
			_, _ = w.Write([]byte("{}"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()
	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{PRNumber: 74, IssueNumber: 74, HeadSHA: "old-sha", Stage: StageFailed, HoldActive: true, HoldHeadSHA: "old-sha", HoldLabels: labelNeedsHuman + ",extra-hold-label"}
	c.redriveHeldPR(context.Background(), prState, &github.PullRequest{Number: 74, State: "open", Head: github.PRRef{SHA: "new-sha"}})

	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
	}
	mu.Lock()
	defer mu.Unlock()
	if deletes != 0 {
		t.Errorf("label removals = %d, want 0 (retry ladder exhausted)", deletes)
	}
}

// TestController_OnPRCreatedForFixIssue_ExistingFailedPR_NoStageChange pins
// the PR #388 observation: registering a fix issue's PR against an already
// tracked held PR changes neither Stage nor HeadSHA.
func TestController_OnPRCreatedForFixIssue_ExistingFailedPR_NoStageChange(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()
	c := NewController(DefaultConfig(), github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{PRNumber: 388, IssueNumber: 380, HeadSHA: "held-sha", Stage: StageFailed, Error: "CI failure with zero gathered evidence", HoldActive: true, HoldHeadSHA: "held-sha"}
	c.mu.Lock()
	c.activePRs[388] = prState
	c.mu.Unlock()

	c.OnPRCreatedForFixIssue(388, "https://github.com/owner/repo/pull/388", 381, 380, "fixed-sha", "pilot/GH-380", "")

	got, ok := c.GetPRState(388)
	if !ok {
		t.Fatal("PR 388 should still be tracked")
	}
	if got.Stage != StageFailed || got.HeadSHA != "held-sha" || !got.HoldActive || got.HoldHeadSHA != "held-sha" {
		t.Errorf("registration must not touch the held row: stage=%v head=%q holdActive=%v holdHead=%q", got.Stage, got.HeadSHA, got.HoldActive, got.HoldHeadSHA)
	}
}

// TestController_ProcessAllPRs_RedrivesZeroEvidenceHeldPR reproduces
// pilot-console PR #388 end to end: handleCIFailed holds the PR for zero
// gathered evidence, a continuation then pushes a new head, and the next tick
// must re-enter the pipeline instead of leaving the PR for a human.
func TestController_ProcessAllPRs_RedrivesZeroEvidenceHeldPR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/zero-evidence-sha/check-runs":
			_, _ = w.Write([]byte(`{"total_count":0,"check_runs":[]}`))
		case r.URL.Path == "/repos/owner/repo/pulls/388" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number:  388,
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/388",
				Head:    github.PRRef{SHA: "fixed-sha", Ref: "pilot/GH-380"},
			})
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIWaitTimeout = time.Hour
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    388,
		IssueNumber: 380,
		PRURL:       "https://github.com/owner/repo/pull/388",
		BranchName:  "pilot/GH-380",
		HeadSHA:     "zero-evidence-sha",
		Stage:       StageCIFailed,
		CreatedAt:   time.Now(),
	}
	c.mu.Lock()
	c.activePRs[388] = prState
	c.mu.Unlock()

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed: %v", err)
	}
	if prState.Stage != StageFailed || !prState.HoldActive || prState.HoldHeadSHA != "zero-evidence-sha" {
		t.Fatalf("expected a redrivable zero-evidence hold, got stage=%v holdActive=%v holdHead=%q", prState.Stage, prState.HoldActive, prState.HoldHeadSHA)
	}

	c.processAllPRs(context.Background())

	got, ok := c.GetPRState(388)
	if !ok {
		t.Fatal("PR 388 should still be tracked")
	}
	if got.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI (redriven on the new head)", got.Stage)
	}
	if got.HeadSHA != "fixed-sha" {
		t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, "fixed-sha")
	}
	if got.HoldActive {
		t.Error("HoldActive should be cleared after redrive")
	}
}

// TestRedriveHeldPR_IgnoresIterationCapHolds covers the negative
// case: a StageFailed PR held for any reason OTHER than the size guard
// (HoldActive=false — e.g. the iteration cap) must stay parked even
// if its branch moved, per the acceptance criterion that terminal-cap
// failures are never revived.
func TestRedriveHeldPR_IgnoresIterationCapHolds(t *testing.T) {
	// Drive the real iteration-cap hold through handleCIFailed (non-sequential
	// mode holds instead of closing) so the escalateAndHold call site's
	// redrivable flag is what this test pins (GH-5643).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/60":
			_, _ = w.Write([]byte(`{"number":60,"body":"<!-- autopilot-meta iteration:3 -->"}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(`{"total_count":1,"check_runs":[{"id":200,"name":"test","status":"completed","conclusion":"failure"}]}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/actions/jobs/200/logs"):
			_, _ = w.Write([]byte("--- FAIL: TestSomething\nassertion failed: expected true, got false"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.MaxCIFixIterations = 3
	cfg.ExecutionMode = "auto"
	c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL), nil, "owner", "repo")

	prState := &PRState{PRNumber: 60, IssueNumber: 60, HeadSHA: "old-sha", BranchName: "pilot/GH-60"}
	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed: %v", err)
	}
	if prState.Stage != StageFailed || !strings.Contains(prState.Error, "CI fix iteration limit reached") {
		t.Fatalf("expected an iteration-cap hold, got stage=%v error=%q", prState.Stage, prState.Error)
	}

	ghPR := &github.PullRequest{Number: 60, State: "open", Head: github.PRRef{SHA: "new-sha"}}
	c.redriveHeldPR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (unrelated hold must not be revived)", prState.Stage)
	}
	if prState.HeadSHA != "old-sha" {
		t.Errorf("HeadSHA = %q, want unchanged %q", prState.HeadSHA, "old-sha")
	}
}

// TestRedriveHeldPR_SameHeadSHA_NoOp covers the no-branch-update
// case: a held PR whose head SHA is unchanged (no push happened) must stay
// parked — this is what makes detection safe to run on every poll tick.
func TestRedriveHeldPR_SameHeadSHA_NoOp(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    61,
		HeadSHA:     "same-sha",
		Stage:       StageFailed,
		HoldActive:  true,
		HoldHeadSHA: "same-sha",
	}

	ghPR := &github.PullRequest{Number: 61, Head: github.PRRef{SHA: "same-sha"}}
	c.redriveHeldPR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (no branch update, no revival)", prState.Stage)
	}
	if !prState.HoldActive {
		t.Error("HoldActive should remain set when no branch update occurred")
	}
}

// TestController_ProcessAllPRs_RedrivesSizeGuardHeldPR is an end-to-end
// check through processAllPRs (the real poll loop entry point, GH-5378): a
// size-guard-held PR whose branch was pushed must be revived AND actually
// re-enter StageWaitingCI processing within the same tick, mirroring
// TestController_ProcessAllPRs_ReAdoptsHeldRebasePR (GH-4610).
func TestController_ProcessAllPRs_RedrivesSizeGuardHeldPR(t *testing.T) {
	var mu sync.Mutex
	var commentPosts int
	var labelRemovals []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5356" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:  5356,
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/5356",
				Head:    github.PRRef{SHA: "new-sha", Ref: "pilot/GH-5351"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/5351/comments" && r.Method == http.MethodPost:
			mu.Lock()
			commentPosts++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/repos/owner/repo/issues/5356/comments" && r.Method == http.MethodPost:
			mu.Lock()
			commentPosts++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/5351/labels/"+labelNeedsHuman:
			mu.Lock()
			labelRemovals = append(labelRemovals, labelNeedsHuman)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = time.Hour
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    5356,
		IssueNumber: 5351,
		PRURL:       "https://github.com/owner/repo/pull/5356",
		BranchName:  "pilot/GH-5351",
		HeadSHA:     "old-sha",
		Stage:       StageFailed,
		Error:       "CI fix size guard fired",
		HoldActive:  true,
		HoldHeadSHA: "old-sha",
		CreatedAt:   time.Now(),
	}
	c.mu.Lock()
	c.activePRs[5356] = prState
	c.mu.Unlock()

	c.processAllPRs(context.Background())

	got, ok := c.GetPRState(5356)
	if !ok {
		t.Fatal("PR 5356 should still be tracked")
	}
	if got.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI (redriven and processed in the same tick)", got.Stage)
	}
	if got.HeadSHA != "new-sha" {
		t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, "new-sha")
	}
	if got.HoldActive {
		t.Error("HoldActive should be cleared after redrive")
	}
	mu.Lock()
	gotRemovals := len(labelRemovals)
	mu.Unlock()
	if gotRemovals != 1 {
		t.Errorf("pilot-needs-human removal calls = %d, want 1", gotRemovals)
	}
}

// TestController_ProcessAllPRs_DoesNotRedriveIterationCapHold covers the
// negative end-to-end case: a PR held at StageFailed by the iteration cap
// (HoldActive unset) must stay parked through processAllPRs even
// when its branch has moved, since that hold is not one
// redriveHeldPR is meant to resolve.
func TestController_ProcessAllPRs_DoesNotRedriveIterationCapHold(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/62" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:  62,
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/62",
				Head:    github.PRRef{SHA: "new-sha", Ref: "pilot/GH-62"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = time.Hour
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    62,
		IssueNumber: 62,
		PRURL:       "https://github.com/owner/repo/pull/62",
		BranchName:  "pilot/GH-62",
		HeadSHA:     "old-sha",
		Stage:       StageFailed,
		Error:       "CI fix iteration limit reached (3/3)",
		HoldActive:  false,
		CreatedAt:   time.Now(),
	}
	c.mu.Lock()
	c.activePRs[62] = prState
	c.mu.Unlock()

	c.processAllPRs(context.Background())

	got, ok := c.GetPRState(62)
	if !ok {
		t.Fatal("PR 62 should still be tracked")
	}
	if got.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (iteration-cap hold must not be revived)", got.Stage)
	}
	if got.HeadSHA != "old-sha" {
		t.Errorf("HeadSHA = %q, want unchanged %q (revival must not touch an unrelated hold)", got.HeadSHA, "old-sha")
	}
}

// TestStateStore_SizeGuardHoldFieldsSurviveRestart is the GH-5378
// persistence regression test: without this, a daemon restart while a PR sat
// held for the CI-fix size guard would reload it with SizeGuardHoldActive
// false, permanently disqualifying it from redriveSizeGuardHeldPR even
// though the branch push it's waiting on hasn't happened yet. Both fields
// must round-trip through SavePRState via both read paths a restart uses:
// GetPRState and LoadAllPRStates.
func TestStateStore_SizeGuardHoldFieldsSurviveRestart(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:             5356,
		PRURL:                "https://github.com/owner/repo/pull/5356",
		IssueNumber:          5351,
		BranchName:           "pilot/GH-5351",
		HeadSHA:              "sha5356",
		Stage:                StageFailed,
		Error:                "CI fix size guard fired",
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "sha5356",
		CreatedAt:            time.Now().Truncate(time.Second),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 5356)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetPRState returned nil")
	}
	if !loaded.SizeGuardHoldActive {
		t.Error("GetPRState: SizeGuardHoldActive = false, want true")
	}
	if loaded.SizeGuardHoldHeadSHA != "sha5356" {
		t.Errorf("GetPRState: SizeGuardHoldHeadSHA = %q, want %q", loaded.SizeGuardHoldHeadSHA, "sha5356")
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAllPRStates returned %d states, want 1", len(all))
	}
	if !all[0].SizeGuardHoldActive {
		t.Error("LoadAllPRStates: SizeGuardHoldActive = false, want true")
	}
	if all[0].SizeGuardHoldHeadSHA != "sha5356" {
		t.Errorf("LoadAllPRStates: SizeGuardHoldHeadSHA = %q, want %q", all[0].SizeGuardHoldHeadSHA, "sha5356")
	}
}

// TestStateStore_HoldFieldsSurviveRoundTrip is the GH-5643 persistence pin:
// the generalized hold triple and its redrive counter must survive a save and
// reload through both GetPRState and LoadAllPRStates.
func TestStateStore_HoldFieldsSurviveRoundTrip(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:         388,
		PRURL:            "https://github.com/owner/repo/pull/388",
		IssueNumber:      380,
		BranchName:       "pilot/GH-380",
		HeadSHA:          "sha388",
		Stage:            StageFailed,
		Error:            "CI failure with zero gathered evidence",
		HoldActive:       true,
		HoldHeadSHA:      "sha388",
		HoldLabels:       labelNeedsHuman + ",extra-hold-label",
		HoldRedriveCount: 1,
		CreatedAt:        time.Now().Truncate(time.Second),
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	check := func(src string, got *PRState) {
		t.Helper()
		if !got.HoldActive || got.HoldHeadSHA != "sha388" || got.HoldLabels != labelNeedsHuman+",extra-hold-label" || got.HoldRedriveCount != 1 {
			t.Errorf("%s: hold fields = active=%v head=%q labels=%q redrive=%d", src, got.HoldActive, got.HoldHeadSHA, got.HoldLabels, got.HoldRedriveCount)
		}
	}
	loaded, err := store.GetPRState("owner/repo", 388)
	if err != nil || loaded == nil {
		t.Fatalf("GetPRState: %v / %v", loaded, err)
	}
	check("GetPRState", loaded)

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil || len(all) != 1 {
		t.Fatalf("LoadAllPRStates: %v / %d rows", err, len(all))
	}
	check("LoadAllPRStates", all[0])
}

// restoreHeldFixture persists pr into a fresh in-memory store and returns a
// new controller (simulating a daemon restart) after RestoreState.
func restoreHeldFixture(t *testing.T, pr *PRState, client *github.Client) *Controller {
	t.Helper()
	store := newTestStateStore(t)
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState: %v", err)
	}
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIWaitTimeout = time.Hour
	c := NewController(cfg, client, nil, "owner", "repo")
	c.SetStateStore(store)
	if _, err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	return c
}

// TestRestoreState_RehydratesHeldFailedPR (GH-5644): a human-held row must
// survive a restart so the tick can fetch its live head and redrive it.
func TestRestoreState_RehydratesHeldFailedPR(t *testing.T) {
	c := restoreHeldFixture(t, &PRState{PRNumber: 388, IssueNumber: 380, HeadSHA: "held-sha", Stage: StageFailed, HoldActive: true, HoldHeadSHA: "held-sha", CreatedAt: time.Now()}, github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:1"))
	got, ok := c.GetPRState(388)
	if !ok {
		t.Fatal("held StageFailed PR should be tracked after RestoreState")
	}
	if !got.HoldActive || got.HoldHeadSHA != "held-sha" {
		t.Errorf("hold triple lost: active=%v head=%q", got.HoldActive, got.HoldHeadSHA)
	}
}

func TestRestoreState_RehydratesRebaseHeldFailedPR(t *testing.T) {
	c := restoreHeldFixture(t, &PRState{PRNumber: 389, IssueNumber: 381, HeadSHA: "sha", Stage: StageFailed, RebaseHoldActive: true, CreatedAt: time.Now()}, github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:1"))
	if _, ok := c.GetPRState(389); !ok {
		t.Fatal("rebase-held StageFailed PR should be tracked after RestoreState")
	}
}

func TestRestoreState_StillSkipsPlainFailedPR(t *testing.T) {
	c := restoreHeldFixture(t, &PRState{PRNumber: 390, IssueNumber: 382, HeadSHA: "sha", Stage: StageFailed, CreatedAt: time.Now()}, github.NewClientWithBaseURL(testutil.FakeGitHubToken, "http://127.0.0.1:1"))
	if _, ok := c.GetPRState(390); ok {
		t.Fatal("plain StageFailed PR must stay untracked after RestoreState")
	}
}

// TestController_ProcessAllPRs_RedrivesHeldPRAfterRestart (GH-5644): the
// PR #388 scenario across a daemon restart — the redrive must still fire.
func TestController_ProcessAllPRs_RedrivesHeldPRAfterRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/388" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number:  388,
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/388",
				Head:    github.PRRef{SHA: "fixed-sha", Ref: "pilot/GH-380"},
			})
		case r.URL.Path == "/repos/owner/repo/pulls" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()

	pr := &PRState{PRNumber: 388, IssueNumber: 380, PRURL: "https://github.com/owner/repo/pull/388", BranchName: "pilot/GH-380", HeadSHA: "held-sha", Stage: StageFailed, Error: "CI failure with zero gathered evidence", HoldActive: true, HoldHeadSHA: "held-sha", CreatedAt: time.Now()}
	c := restoreHeldFixture(t, pr, github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL))

	c.processAllPRs(context.Background())

	got, ok := c.GetPRState(388)
	if !ok {
		t.Fatal("PR 388 should be tracked after restart")
	}
	if got.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", got.Stage)
	}
	if got.HeadSHA != "fixed-sha" || got.HoldActive {
		t.Errorf("head=%q holdActive=%v, want fixed-sha / false", got.HeadSHA, got.HoldActive)
	}
}
