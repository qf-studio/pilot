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

// TestRedriveSizeGuardHeldPR_RevivesOnNewHead covers the GH-5378 fix itself:
// a PR held via escalateAndHold's CI-fix size guard, once its branch
// receives a new commit (e.g. a small lint fix that doesn't grow the PR
// further), must be revived to StageWaitingCI for fresh CI — not left
// stranded even after CI turns green, as PR #5356 was for 85 minutes until
// merged by hand.
func TestRedriveSizeGuardHeldPR_RevivesOnNewHead(t *testing.T) {
	rec, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             5356,
		IssueNumber:          5351,
		BranchName:           "pilot/GH-5351",
		HeadSHA:              "old-sha",
		Stage:                StageFailed,
		Error:                "CI fix size guard fired",
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "old-sha",
		TerminalLabel:        github.LabelFailed,
		RebaseAttempts:       1,
		MergeAttempts:        2,
	}

	ghPR := &github.PullRequest{Number: 5356, Head: github.PRRef{SHA: "new-sha"}}
	c.redriveSizeGuardHeldPR(context.Background(), prState, ghPR)

	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %v, want StageWaitingCI", prState.Stage)
	}
	if prState.HeadSHA != "new-sha" {
		t.Errorf("HeadSHA = %q, want %q", prState.HeadSHA, "new-sha")
	}
	if prState.SizeGuardHoldActive {
		t.Error("SizeGuardHoldActive should be cleared after re-adoption")
	}
	if prState.SizeGuardHoldHeadSHA != "" {
		t.Errorf("SizeGuardHoldHeadSHA = %q, want empty after re-adoption", prState.SizeGuardHoldHeadSHA)
	}
	if prState.Error != "" {
		t.Errorf("Error = %q, want empty after re-adoption", prState.Error)
	}
	if prState.TerminalLabel != "" {
		t.Errorf("TerminalLabel = %q, want empty after re-adoption", prState.TerminalLabel)
	}
	// Preserved attempt counters, matching reAdoptHeldRebasePR (GH-4610).
	if prState.RebaseAttempts != 1 {
		t.Errorf("RebaseAttempts = %d, want 1 (preserved)", prState.RebaseAttempts)
	}
	if prState.MergeAttempts != 2 {
		t.Errorf("MergeAttempts = %d, want 2 (preserved)", prState.MergeAttempts)
	}
	if prState.CIWaitStartedAt.IsZero() {
		t.Error("CIWaitStartedAt should be reset for fresh CI monitoring")
	}
	if n := rec.count(http.MethodPost, "/repos/owner/repo/issues/5356/comments"); n != 1 {
		t.Errorf("PR comment calls = %d, want 1 (re-adoption notice)", n)
	}
	if n := rec.count(http.MethodDelete, "/repos/owner/repo/issues/5351/labels/"+labelNeedsHuman); n != 1 {
		t.Errorf("pilot-needs-human removal calls on the issue = %d, want 1", n)
	}
}

// TestRedriveSizeGuardHeldPR_IgnoresNonSizeGuardHolds covers the negative
// case: a StageFailed PR held for any reason OTHER than the size guard
// (SizeGuardHoldActive=false — e.g. the iteration cap) must stay parked even
// if its branch moved, per the acceptance criterion that terminal-cap
// failures are never revived.
func TestRedriveSizeGuardHeldPR_IgnoresNonSizeGuardHolds(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:            60,
		IssueNumber:         60,
		HeadSHA:             "old-sha",
		Stage:               StageFailed,
		Error:               "CI fix iteration limit reached (3/3)",
		SizeGuardHoldActive: false,
	}

	ghPR := &github.PullRequest{Number: 60, Head: github.PRRef{SHA: "new-sha"}}
	c.redriveSizeGuardHeldPR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (unrelated hold must not be revived)", prState.Stage)
	}
	if prState.HeadSHA != "old-sha" {
		t.Errorf("HeadSHA = %q, want unchanged %q", prState.HeadSHA, "old-sha")
	}
}

// TestRedriveSizeGuardHeldPR_SameHeadSHA_NoOp covers the no-branch-update
// case: a held PR whose head SHA is unchanged (no push happened) must stay
// parked — this is what makes detection safe to run on every poll tick.
func TestRedriveSizeGuardHeldPR_SameHeadSHA_NoOp(t *testing.T) {
	_, srv := newRecordingGHServer()
	defer srv.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             61,
		HeadSHA:              "same-sha",
		Stage:                StageFailed,
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "same-sha",
	}

	ghPR := &github.PullRequest{Number: 61, Head: github.PRRef{SHA: "same-sha"}}
	c.redriveSizeGuardHeldPR(context.Background(), prState, ghPR)

	if prState.Stage != StageFailed {
		t.Errorf("Stage = %v, want StageFailed (no branch update, no revival)", prState.Stage)
	}
	if !prState.SizeGuardHoldActive {
		t.Error("SizeGuardHoldActive should remain set when no branch update occurred")
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
		PRNumber:             5356,
		IssueNumber:          5351,
		PRURL:                "https://github.com/owner/repo/pull/5356",
		BranchName:           "pilot/GH-5351",
		HeadSHA:              "old-sha",
		Stage:                StageFailed,
		Error:                "CI fix size guard fired",
		SizeGuardHoldActive:  true,
		SizeGuardHoldHeadSHA: "old-sha",
		CreatedAt:            time.Now(),
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
	if got.SizeGuardHoldActive {
		t.Error("SizeGuardHoldActive should be cleared after redrive")
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
// (SizeGuardHoldActive unset) must stay parked through processAllPRs even
// when its branch has moved, since that hold is not one
// redriveSizeGuardHeldPR is meant to resolve.
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
		PRNumber:            62,
		IssueNumber:         62,
		PRURL:               "https://github.com/owner/repo/pull/62",
		BranchName:          "pilot/GH-62",
		HeadSHA:             "old-sha",
		Stage:               StageFailed,
		Error:               "CI fix iteration limit reached (3/3)",
		SizeGuardHoldActive: false,
		CreatedAt:           time.Now(),
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
