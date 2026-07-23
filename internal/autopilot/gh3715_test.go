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

// GH-3715: a successful auto-rebase (handleMergeConflict's UpdatePullRequestBranch
// path) returns the PR to StageWaitingCI without consuming MergeAttempts or any
// other retry budget, so a PR could previously cycle
// conflict -> rebase-success -> CI -> conflict indefinitely. RebaseAttempts caps
// that oscillation.

// TestController_HandleMergeConflict_RebaseAttemptCapEscalates verifies that the
// 3rd successful auto-rebase for the same PR escalates to StageFailed with a
// comment instead of rebasing again.
func TestController_HandleMergeConflict_RebaseAttemptCapEscalates(t *testing.T) {
	var (
		updateBranchCalled      bool
		escalationCommentBody   string
		escalationCommentPosted bool
	)

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/77/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))

		case r.URL.Path == "/repos/owner/repo/pulls/77" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:         77,
				Head:           github.PRRef{SHA: "sha77", Ref: "pilot/GH-30"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/77/update-branch" && r.Method == http.MethodPut:
			updateBranchCalled = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})

		case r.URL.Path == "/repos/owner/repo/issues/77/comments" && r.Method == http.MethodPost:
			escalationCommentPosted = true
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			escalationCommentBody = body["body"]
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxRebaseAttempts = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	// RebaseAttempts is 2; a successful rebase increments to 3 == MaxRebaseAttempts → cap fires.
	c.activePRs[77] = &PRState{
		PRNumber:       77,
		PRURL:          "https://github.com/owner/repo/pull/77",
		IssueNumber:    30,
		BranchName:     "pilot/GH-30",
		HeadSHA:        "sha77",
		Stage:          StageMerging,
		RebaseAttempts: 2,
		CreatedAt:      time.Now(),
	}
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 77, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if !updateBranchCalled {
		t.Fatal("UpdatePullRequestBranch should have been called")
	}

	pr, ok := c.GetPRState(77)
	if !ok {
		t.Fatal("PR 77 not found in activePRs")
	}
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (rebase oscillation cap should escalate)", pr.Stage, StageFailed)
	}
	if pr.RebaseAttempts != 3 {
		t.Errorf("RebaseAttempts = %d, want 3", pr.RebaseAttempts)
	}
	if !escalationCommentPosted {
		t.Error("escalation comment should have been posted on the PR")
	}
	if escalationCommentBody == "" {
		t.Error("escalation comment body should not be empty")
	}
}

// TestController_HandleMergeConflict_BelowRebaseCapStaysWaitingCI verifies that a
// successful auto-rebase below MaxRebaseAttempts still returns the PR to
// StageWaitingCI (existing GH-1796 behavior) while incrementing the counter.
func TestController_HandleMergeConflict_BelowRebaseCapStaysWaitingCI(t *testing.T) {
	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/78/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))

		case r.URL.Path == "/repos/owner/repo/pulls/78" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:         78,
				Head:           github.PRRef{SHA: "sha78", Ref: "pilot/GH-31"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/78/update-branch" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxRebaseAttempts = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	// RebaseAttempts is 1; a successful rebase increments to 2 < MaxRebaseAttempts(3).
	c.activePRs[78] = &PRState{
		PRNumber:       78,
		PRURL:          "https://github.com/owner/repo/pull/78",
		IssueNumber:    31,
		BranchName:     "pilot/GH-31",
		HeadSHA:        "sha78",
		Stage:          StageMerging,
		RebaseAttempts: 1,
		CreatedAt:      time.Now(),
	}
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 78, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(78)
	if !ok {
		t.Fatal("PR 78 not found in activePRs")
	}
	if pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s (below-cap rebase must stay retryable)", pr.Stage, StageWaitingCI)
	}
	if pr.RebaseAttempts != 2 {
		t.Errorf("RebaseAttempts = %d, want 2", pr.RebaseAttempts)
	}
}

// TestController_HandleMerging_ResetsRebaseAttemptsOnSuccess verifies that a
// clean merge resets RebaseAttempts to 0 so a future regression on the same PR
// starts the oscillation budget from zero again.
func TestController_HandleMerging_ResetsRebaseAttemptsOnSuccess(t *testing.T) {
	mergeable := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha79/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/79/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "merged79", "merged": "true"})

		case r.URL.Path == "/repos/owner/repo/pulls/79" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:         79,
				Head:           github.PRRef{SHA: "sha79"},
				Mergeable:      &mergeable,
				MergeableState: "clean",
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
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	c.activePRs[79] = &PRState{
		PRNumber:       79,
		PRURL:          "https://github.com/owner/repo/pull/79",
		IssueNumber:    0,
		BranchName:     "pilot/GH-32",
		HeadSHA:        "sha79",
		Stage:          StageMerging,
		RebaseAttempts: 2,
		CreatedAt:      time.Now(),
	}
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 79, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(79)
	if !ok {
		t.Fatal("PR 79 not found in activePRs")
	}
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
	}
	if pr.RebaseAttempts != 0 {
		t.Errorf("RebaseAttempts = %d, want 0 (must reset on clean merge)", pr.RebaseAttempts)
	}
}

// TestStateStore_RebaseAttempts_SurvivesRestart verifies that RebaseAttempts is
// persisted to SQLite and reloaded correctly via both GetPRState and
// LoadAllPRStates — the path a daemon restart actually uses to restore
// activePRs. Without this, a daemon restart would silently reset the
// oscillation counter and re-open the conflict -> rebase -> CI -> conflict loop.
func TestStateStore_RebaseAttempts_SurvivesRestart(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:       55,
		PRURL:          "https://github.com/owner/repo/pull/55",
		IssueNumber:    20,
		BranchName:     "pilot/GH-20",
		HeadSHA:        "sha55",
		Stage:          StageMerging,
		RebaseAttempts: 2,
		CreatedAt:      time.Now().Truncate(time.Second),
	}

	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 55)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("GetPRState returned nil")
	}
	if loaded.RebaseAttempts != 2 {
		t.Errorf("GetPRState: RebaseAttempts = %d, want 2", loaded.RebaseAttempts)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("LoadAllPRStates returned %d states, want 1", len(all))
	}
	if all[0].RebaseAttempts != 2 {
		t.Errorf("LoadAllPRStates: RebaseAttempts = %d, want 2", all[0].RebaseAttempts)
	}
}
