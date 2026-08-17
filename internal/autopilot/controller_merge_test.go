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

// TestController_MergeConflict_IssueNotGhostClosed verifies that when autopilot
// detects a merge conflict and closes the PR, the associated issue is NOT closed
// and pilot-done is NOT added. The issue must stay open for re-dispatch.
// GH-3139 / TASK-301.
func TestController_MergeConflict_IssueNotGhostClosed(t *testing.T) {
	var (
		pilotDoneAdded    bool
		pilotLabelAdded   bool
		pilotDoneRemoved  bool
		issueClosed       bool
		prClosed          bool
		inProgressRemoved bool
	)

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:         55,
				Head:           github.PRRef{SHA: "sha55"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/55/update-branch" && r.Method == http.MethodPut:
			// Auto-rebase fails → true conflict path
			w.WriteHeader(http.StatusUnprocessableEntity)

		case r.URL.Path == "/repos/owner/repo/issues/55/comments" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})

		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/20/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body["labels"] {
				if l == github.LabelDone {
					pilotDoneAdded = true
				}
				if l == github.LabelPilot {
					pilotLabelAdded = true
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})

		case r.URL.Path == "/repos/owner/repo/issues/20/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			inProgressRemoved = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/20/labels/pilot-done" && r.Method == http.MethodDelete:
			pilotDoneRemoved = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/20" && r.Method == http.MethodPatch:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["state"] == "closed" {
				issueClosed = true
			}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	c.activePRs[55] = &PRState{
		PRNumber:        55,
		PRURL:           "https://github.com/owner/repo/pull/55",
		IssueNumber:     20,
		BranchName:      "pilot/GH-20",
		HeadSHA:         "sha55",
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now(),
		CreatedAt:       time.Now(),
	}
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 55, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	pr, _ := c.GetPRState(55)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
	if !inProgressRemoved {
		t.Error("pilot-in-progress should be removed from issue")
	}
	if pilotDoneAdded {
		t.Error("pilot-done must NOT be added on conflict — ghost-close guard (GH-3139)")
	}
	if issueClosed {
		t.Error("issue must NOT be closed on conflict — ghost-close guard (GH-3139)")
	}
	if !pilotLabelAdded {
		t.Error("pilot label should be re-added to issue for re-dispatch")
	}
	if !pilotDoneRemoved {
		t.Error("pilot-done cleanup should be attempted on conflict (may be 404 — that is OK)")
	}
}

// TestController_HandleMerging_ClosesIssueWithPilotDone verifies that when a PR
// is successfully merged, the associated issue is closed and pilot-done is added.
// GH-3139 / TASK-301: this is the only path that should close the issue.
func TestController_HandleMerging_ClosesIssueWithPilotDone(t *testing.T) {
	var (
		pilotDoneAdded bool
		issueClosed    bool
		inProgRemoved  bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha99/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/99/merge" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))

		case r.URL.Path == "/repos/owner/repo/issues/30/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body["labels"] {
				if l == github.LabelDone {
					pilotDoneAdded = true
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})

		case r.URL.Path == "/repos/owner/repo/issues/30/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			inProgRemoved = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/30" && r.Method == http.MethodPatch:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["state"] == "closed" {
				issueClosed = true
			}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoMerge = true
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	c.activePRs[99] = &PRState{
		PRNumber:     99,
		PRURL:        "https://github.com/owner/repo/pull/99",
		IssueNumber:  30,
		BranchName:   "pilot/GH-30",
		HeadSHA:      "sha99",
		Stage:        StageMerging,
		TargetBranch: "main",
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 99, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	pr, _ := c.GetPRState(99)
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
	}
	if !pilotDoneAdded {
		t.Error("pilot-done must be added after successful merge")
	}
	if !issueClosed {
		t.Error("issue must be closed after successful merge")
	}
	if !inProgRemoved {
		t.Error("pilot-in-progress should be removed after merge")
	}
}

// TestController_HandleMerging_CallsOnIssueDone verifies that the SetOnIssueDone
// callback fires with the correct issue number after a successful PR merge. GH-3271.
func TestController_HandleMerging_CallsOnIssueDone(t *testing.T) {
	var doneCalled []int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha77/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/77/merge" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))

		case r.URL.Path == "/repos/owner/repo/issues/55/labels" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})

		case r.URL.Path == "/repos/owner/repo/issues/55" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoMerge = true
	cfg.RequiredChecks = []string{"ci"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetOnIssueDone(func(issueNumber int) {
		doneCalled = append(doneCalled, issueNumber)
	})
	c.mu.Lock()
	c.activePRs[77] = &PRState{
		PRNumber:     77,
		PRURL:        "https://github.com/owner/repo/pull/77",
		IssueNumber:  55,
		BranchName:   "pilot/GH-55",
		HeadSHA:      "sha77",
		Stage:        StageMerging,
		TargetBranch: "main",
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 77, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	pr, _ := c.GetPRState(77)
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
	}
	if len(doneCalled) != 1 || doneCalled[0] != 55 {
		t.Errorf("onIssueDone called with %v, want [55]", doneCalled)
	}
}

// TestController_HandleMerging_MergeAttemptCapEscalates verifies that when a PR
// reaches MaxMergeAttempts on a non-conflict merge failure it transitions to
// StageFailed (never retried) and posts an escalation comment on the issue.
// TASK-336 / B5.
func TestController_HandleMerging_MergeAttemptCapEscalates(t *testing.T) {
	var (
		escalationCommentPosted bool
		prFetched               bool
	)

	mergeable := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/88/merge" && r.Method == http.MethodPut:
			// Simulate a persistent non-conflict merge failure (e.g. branch-protection).
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"405 Method Not Allowed"}`))

		case r.URL.Path == "/repos/owner/repo/pulls/88" && r.Method == http.MethodGet:
			prFetched = true
			resp := github.PullRequest{
				Number:         88,
				Head:           github.PRRef{SHA: "sha88"},
				Mergeable:      &mergeable,
				MergeableState: "clean",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/issues/40/comments" && r.Method == http.MethodPost:
			escalationCommentPosted = true
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
	cfg.MaxMergeAttempts = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	// MergeAttempts is 2; handleMerging will increment to 3 == MaxMergeAttempts → cap fires.
	c.activePRs[88] = &PRState{
		PRNumber:      88,
		PRURL:         "https://github.com/owner/repo/pull/88",
		IssueNumber:   40,
		BranchName:    "pilot/GH-40",
		HeadSHA:       "sha88",
		Stage:         StageMerging,
		TargetBranch:  "main",
		MergeAttempts: 2,
		CreatedAt:     time.Now(),
	}
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 88, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	pr, ok := c.GetPRState(88)
	if !ok {
		t.Fatal("PR 88 not found in activePRs")
	}
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageFailed)
	}
	if pr.MergeAttempts != 3 {
		t.Errorf("MergeAttempts = %d, want 3", pr.MergeAttempts)
	}
	if !prFetched {
		t.Error("GetPullRequest should have been called to check for conflict")
	}
	if !escalationCommentPosted {
		t.Error("escalation comment should have been posted on the issue")
	}
}

// TestController_HandleMerging_BelowCapReturnsRetryableError verifies that a merge
// failure when MergeAttempts is still below MaxMergeAttempts returns a retryable
// error (Stage stays StageMerging) rather than transitioning to StageFailed.
// TASK-336 / B5.
func TestController_HandleMerging_BelowCapReturnsRetryableError(t *testing.T) {
	mergeable := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/66/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message":"405 Method Not Allowed"}`))

		case r.URL.Path == "/repos/owner/repo/pulls/66" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:         66,
				Head:           github.PRRef{SHA: "sha66"},
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
	cfg.MaxMergeAttempts = 5
	cfg.MaxFailures = 100 // disable circuit breaker for this test

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	// MergeAttempts is 1; handleMerging will increment to 2 < MaxMergeAttempts(5) → retryable.
	c.activePRs[66] = &PRState{
		PRNumber:      66,
		PRURL:         "https://github.com/owner/repo/pull/66",
		IssueNumber:   0,
		BranchName:    "pilot/GH-66",
		HeadSHA:       "sha66",
		Stage:         StageMerging,
		TargetBranch:  "main",
		MergeAttempts: 1,
		CreatedAt:     time.Now(),
	}
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 66, nil)
	if err == nil {
		t.Fatal("ProcessPR should have returned an error for a below-cap merge failure")
	}

	pr, ok := c.GetPRState(66)
	if !ok {
		t.Fatal("PR 66 not found in activePRs")
	}
	if pr.Stage != StageMerging {
		t.Errorf("Stage = %s, want %s (below-cap failure must stay retryable)", pr.Stage, StageMerging)
	}
	if pr.MergeAttempts != 2 {
		t.Errorf("MergeAttempts = %d, want 2", pr.MergeAttempts)
	}
}
