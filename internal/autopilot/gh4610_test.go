package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestEscalateAndHold_SetsRebaseHoldActive covers the GH-4610 wiring:
// escalateAndHold must flag RebaseHoldActive only when the caller-supplied
// labels include "needs-manual-rebase" — every other escalation reason
// (CI-fix size guard, rebase-oscillation cap, etc.) must leave the PR
// ineligible for re-adoption.
func TestEscalateAndHold_SetsRebaseHoldActive(t *testing.T) {
	tests := []struct {
		name       string
		labels     []string
		wantActive bool
	}{
		{"needs-manual-rebase label present", []string{"needs-manual-rebase"}, true},
		{"needs-manual-rebase among other labels", []string{"foo", "needs-manual-rebase"}, true},
		{"unrelated label only", []string{"pilot-blocked"}, false},
		{"no labels", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, srv := newRecordingGHServer()
			defer srv.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 50, IssueNumber: 5, BranchName: "pilot/GH-5"}
			c.escalateAndHold(context.Background(), prState, "some reason", tt.labels, "comment")

			if prState.RebaseHoldActive != tt.wantActive {
				t.Errorf("RebaseHoldActive = %v, want %v", prState.RebaseHoldActive, tt.wantActive)
			}
		})
	}
}

// TestReAdoptHeldRebasePR_RevivesOnNewHead covers the GH-4610 fix itself: a
// PR held via escalateAndHold's needs-manual-rebase hold, once its branch
// receives a new commit (operator resolved the conflict and pushed), must
// be revived to StageWaitingCI for fresh CI — not left stranded requiring a
// fully manual `gh pr merge` as it was before this fix (5x recurrence in one
// wave, pilot-console PRs #67/#68/#70/#74/#75).
func TestReAdoptHeldRebasePR_RevivesOnNewHead(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:         67,
		IssueNumber:      67,
		BranchName:       "pilot/GH-67",
		HeadSHA:          "old-sha",
		Stage:            StageFailed,
		Error:            "auto-rebase failed",
		RebaseHoldActive: true,
		RebaseAttempts:   1,
		MergeAttempts:    2,
	}

	ghPR := &github.PullRequest{Number: 67, Head: github.PRRef{SHA: "new-sha"}}
	c.reAdoptHeldRebasePR(context.Background(), prState, ghPR)

	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
	}
	if prState.HeadSHA != "new-sha" {
		t.Errorf("HeadSHA = %q, want %q", prState.HeadSHA, "new-sha")
	}
	if prState.RebaseHoldActive {
		t.Error("RebaseHoldActive should be cleared after re-adoption")
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty after re-adoption", prState.Error)
	}
	if prState.ReadoptCount != 1 {
		t.Errorf("ReadoptCount = %d, want 1", prState.ReadoptCount)
	}
	// Preserved attempt counters — GH-4610 requires these survive re-adoption
	// so their own caps (MaxRebaseAttempts, merge attempt cap) still apply.
	if prState.RebaseAttempts != 1 {
		t.Errorf("RebaseAttempts = %d, want 1 (preserved)", prState.RebaseAttempts)
	}
	if prState.MergeAttempts != 2 {
		t.Errorf("MergeAttempts = %d, want 2 (preserved)", prState.MergeAttempts)
	}
	if prState.CIWaitStartedAt.IsZero() {
		t.Error("CIWaitStartedAt should be reset for fresh CI monitoring")
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/67/comments"); n != 1 {
		t.Errorf("PR comment calls = %d, want 1 (re-adoption notice)", n)
	}
}

// TestReAdoptHeldRebasePR_IgnoresNonRebaseHolds covers the negative case: a
// StageFailed PR held for any reason OTHER than needs-manual-rebase
// (RebaseHoldActive=false) must stay parked even if its branch moved — e.g.
// a CI-timeout or rebase-oscillation-cap hold is not something a bare branch
// push resolves.
func TestReAdoptHeldRebasePR_IgnoresNonRebaseHolds(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:         68,
		HeadSHA:          "old-sha",
		Stage:            StageFailed,
		Error:            "CI timeout after 1h0m0s",
		RebaseHoldActive: false,
	}

	ghPR := &github.PullRequest{Number: 68, Head: github.PRRef{SHA: "new-sha"}}
	c.reAdoptHeldRebasePR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (unrelated hold must not be revived)", prState.Stage)
	}
	if prState.HeadSHA != "old-sha" {
		t.Errorf("HeadSHA = %q, want unchanged %q", prState.HeadSHA, "old-sha")
	}
	if prState.ReadoptCount != 0 {
		t.Errorf("ReadoptCount = %d, want 0", prState.ReadoptCount)
	}
}

// TestReAdoptHeldRebasePR_SameHeadSHA_NoOp covers the no-branch-update case:
// a held PR whose head SHA is unchanged (no push happened) must stay parked
// — this is what makes detection safe to run on every poll tick.
func TestReAdoptHeldRebasePR_SameHeadSHA_NoOp(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:         70,
		HeadSHA:          "same-sha",
		Stage:            StageFailed,
		RebaseHoldActive: true,
	}

	ghPR := &github.PullRequest{Number: 70, Head: github.PRRef{SHA: "same-sha"}}
	c.reAdoptHeldRebasePR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (no branch update, no revival)", prState.Stage)
	}
	if prState.ReadoptCount != 0 {
		t.Errorf("ReadoptCount = %d, want 0", prState.ReadoptCount)
	}
}

// TestReAdoptHeldRebasePR_CapReached covers the ping-pong guard: once
// ReadoptCount reaches maxReadoptAttempts, a further branch update must NOT
// trigger another revival — the PR stays parked for a human even though its
// head SHA changed again, so a repeatedly-conflicting branch can't cycle
// autopilot between held and waiting_ci forever.
func TestReAdoptHeldRebasePR_CapReached(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:         74,
		HeadSHA:          "sha-at-cap",
		Stage:            StageFailed,
		RebaseHoldActive: true,
		ReadoptCount:     maxReadoptAttempts,
	}

	ghPR := &github.PullRequest{Number: 74, Head: github.PRRef{SHA: "yet-another-sha"}}
	c.reAdoptHeldRebasePR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (cap reached, must stay parked)", prState.Stage)
	}
	if prState.HeadSHA != "sha-at-cap" {
		t.Errorf("HeadSHA = %q, want unchanged %q", prState.HeadSHA, "sha-at-cap")
	}
	if prState.ReadoptCount != maxReadoptAttempts {
		t.Errorf("ReadoptCount = %d, want unchanged %d", prState.ReadoptCount, maxReadoptAttempts)
	}
}

// TestController_ProcessAllPRs_ReAdoptsHeldRebasePR is an end-to-end check
// through processAllPRs (the real poll loop entry point, GH-4610): a held
// PR whose branch was pushed must be revived AND actually re-enter
// StageWaitingCI processing within the same tick, not just have its stage
// flipped with no follow-through.
func TestController_ProcessAllPRs_ReAdoptsHeldRebasePR(t *testing.T) {
	var mu sync.Mutex
	var commentPosts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/75" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:  75,
				State:   "open",
				HTMLURL: "https://github.com/owner/repo/pull/75",
				Head:    github.PRRef{SHA: "new-sha", Ref: "pilot/GH-75"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/75/comments" && r.Method == http.MethodPost:
			mu.Lock()
			commentPosts++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
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
		PRNumber:         75,
		IssueNumber:      75,
		PRURL:            "https://github.com/owner/repo/pull/75",
		BranchName:       "pilot/GH-75",
		HeadSHA:          "old-sha",
		Stage:            StageFailed,
		Error:            "auto-rebase failed",
		RebaseHoldActive: true,
		CreatedAt:        time.Now(),
	}
	c.mu.Lock()
	c.activePRs[75] = prState
	c.mu.Unlock()

	c.processAllPRs(context.Background())

	got, ok := c.GetPRState(75)
	if !ok {
		t.Fatal("PR 75 should still be tracked")
	}
	if got.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI (re-adopted and processed in the same tick)", got.Stage)
	}
	if got.HeadSHA != "new-sha" {
		t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, "new-sha")
	}
	if got.ReadoptCount != 1 {
		t.Errorf("ReadoptCount = %d, want 1", got.ReadoptCount)
	}
	mu.Lock()
	gotComments := commentPosts
	mu.Unlock()
	if gotComments != 1 {
		t.Errorf("PR comment calls = %d, want 1 (re-adoption notice)", gotComments)
	}
}

// TestStateStore_RebaseHoldActiveAndReadoptCount_SurviveRestart is the
// GH-4610 persistence regression test: without this, a daemon restart while
// a PR sat held for needs-manual-rebase would reload it with
// RebaseHoldActive=false, permanently disqualifying it from re-adoption even
// though the branch push it's waiting on hasn't happened yet, and would
// reset ReadoptCount to 0, silently granting a fresh re-adoption budget on
// every restart. Both fields must round-trip through SavePRState via both
// read paths a restart uses: GetPRState and LoadAllPRStates.
func TestStateStore_RebaseHoldActiveAndReadoptCount_SurviveRestart(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:         75,
		PRURL:            "https://github.com/owner/repo/pull/75",
		IssueNumber:      75,
		BranchName:       "pilot/GH-75",
		HeadSHA:          "sha75",
		Stage:            StageFailed,
		Error:            "auto-rebase failed",
		RebaseHoldActive: true,
		ReadoptCount:     1,
		CreatedAt:        time.Now().Truncate(time.Second),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 75)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetPRState returned nil")
	}
	if !loaded.RebaseHoldActive {
		t.Error("GetPRState: RebaseHoldActive = false, want true")
	}
	if loaded.ReadoptCount != 1 {
		t.Errorf("GetPRState: ReadoptCount = %d, want 1", loaded.ReadoptCount)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAllPRStates returned %d states, want 1", len(all))
	}
	if !all[0].RebaseHoldActive {
		t.Error("LoadAllPRStates: RebaseHoldActive = false, want true")
	}
	if all[0].ReadoptCount != 1 {
		t.Errorf("LoadAllPRStates: ReadoptCount = %d, want 1", all[0].ReadoptCount)
	}
}
