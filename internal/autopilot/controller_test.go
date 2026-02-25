package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/approval"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewController(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	approvalMgr := approval.NewManager(nil)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, approvalMgr, "owner", "repo")

	if c == nil {
		t.Fatal("NewController returned nil")
	}
	if c.owner != "owner" {
		t.Errorf("owner = %s, want owner", c.owner)
	}
	if c.repo != "repo" {
		t.Errorf("repo = %s, want repo", c.repo)
	}
	if c.ciMonitor == nil {
		t.Error("ciMonitor should be initialized")
	}
	if c.autoMerger == nil {
		t.Error("autoMerger should be initialized")
	}
	if c.feedbackLoop == nil {
		t.Error("feedbackLoop should be initialized")
	}
}

func TestController_OnPRCreated(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	prs := c.GetActivePRs()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}

	pr := prs[0]
	if pr.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", pr.PRNumber)
	}
	if pr.IssueNumber != 10 {
		t.Errorf("IssueNumber = %d, want 10", pr.IssueNumber)
	}
	if pr.HeadSHA != "abc123" {
		t.Errorf("HeadSHA = %s, want abc123", pr.HeadSHA)
	}
	if pr.Stage != StagePRCreated {
		t.Errorf("Stage = %s, want %s", pr.Stage, StagePRCreated)
	}
	if pr.CIStatus != CIPending {
		t.Errorf("CIStatus = %s, want %s", pr.CIStatus, CIPending)
	}
}

func TestController_GetPRState(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("expected PR to be found")
	}
	if pr.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", pr.PRNumber)
	}

	_, ok = c.GetPRState(99)
	if ok {
		t.Error("PR 99 should not be found")
	}
}

func TestController_ProcessPR_NotTracked(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	err := c.ProcessPR(context.Background(), 99, nil)
	if err == nil {
		t.Error("ProcessPR should fail for untracked PR")
	}
}

func TestController_ProcessPR_DevEnvironment(t *testing.T) {
	// Test dev flow: PR created → waiting CI → CI passed → merging → merged → done
	// Dev now waits for CI like stage/prod, but with shorter timeout
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI (dev now waits for CI)
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	// Stage 3: CI passed → merging
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageMerging)
	}

	// Stage 4: merging → merged
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 4 error: %v", err)
	}
	if !mergeWasCalled {
		t.Error("merge should have been called")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerged {
		t.Errorf("after stage 4: Stage = %s, want %s", pr.Stage, StageMerged)
	}

	// Stage 5: merged → done (removed from tracking in dev)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 5 error: %v", err)
	}
	_, ok := c.GetPRState(42)
	if ok {
		t.Error("PR should be removed from tracking in dev after merge")
	}
}

func TestController_ProcessPR_StageEnvironment_CIPass(t *testing.T) {
	// Test stage flow: PR created → waiting CI → CI passed → merging → merged → post-merge CI
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/branches/main":
			resp := github.Branch{
				Name:   "main",
				Commit: github.BranchCommit{SHA: "abc1234"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	// Stage 3: CI passed → merging (no approval in stage)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageMerging)
	}

	// Stage 4: merging → merged
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 4 error: %v", err)
	}
	if !mergeWasCalled {
		t.Error("merge should have been called")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerged {
		t.Errorf("after stage 4: Stage = %s, want %s", pr.Stage, StageMerged)
	}

	// Stage 5: merged → post-merge CI
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 5 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StagePostMergeCI {
		t.Errorf("after stage 5: Stage = %s, want %s", pr.Stage, StagePostMergeCI)
	}

	// Stage 6: post-merge CI → done (removed from tracking)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 6 error: %v", err)
	}
	_, ok := c.GetPRState(42)
	if ok {
		t.Error("PR should be removed from tracking after post-merge CI")
	}
}

func TestController_ProcessPR_CIFailure(t *testing.T) {
	// Test CI failure creates fix issue and closes the failed PR
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 100}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			// PR close request — verify it's called after CI failure
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → CI failed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageCIFailed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIFailed)
	}

	// Stage 3: CI failed → create fix issue → close PR → failed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	if !issueCreated {
		t.Error("fix issue should have been created")
	}
	if !prClosed {
		t.Error("failed PR should have been closed on GitHub to unblock sequential poller")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageFailed)
	}
}

func TestController_CircuitBreaker(t *testing.T) {
	// Test per-PR circuit breaker trips after max failures for that specific PR
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return error to trigger failures
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start with PR in merging stage (will fail on merge)
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		Stage:    StageMerging,
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Cause failures
	for i := 0; i < 3; i++ {
		_ = c.ProcessPR(ctx, 42, nil)
	}

	// Per-PR circuit breaker should be open for PR 42
	if !c.IsPRCircuitOpen(42) {
		t.Error("per-PR circuit breaker should be open for PR 42 after max failures")
	}

	// Next call for PR 42 should be blocked
	err := c.ProcessPR(ctx, 42, nil)
	if err == nil {
		t.Error("ProcessPR should fail when per-PR circuit breaker is open")
	}
}

func TestController_ResetCircuitBreaker(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set per-PR failures
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 5, LastFailureTime: time.Now()}
	c.prFailures[43] = &prFailureState{FailureCount: 3, LastFailureTime: time.Now()}
	c.mu.Unlock()

	if !c.IsPRCircuitOpen(42) {
		t.Error("circuit should be open for PR 42")
	}
	if !c.IsPRCircuitOpen(43) {
		t.Error("circuit should be open for PR 43")
	}

	c.ResetCircuitBreaker()

	if c.IsPRCircuitOpen(42) {
		t.Error("circuit should be closed for PR 42 after reset")
	}
	if c.IsPRCircuitOpen(43) {
		t.Error("circuit should be closed for PR 43 after reset")
	}
}

func TestController_MultiplePRs(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Add multiple PRs
	c.OnPRCreated(1, "url1", 10, "sha1", "pilot/GH-10")
	c.OnPRCreated(2, "url2", 20, "sha2", "pilot/GH-20")
	c.OnPRCreated(3, "url3", 30, "sha3", "pilot/GH-30")

	prs := c.GetActivePRs()
	if len(prs) != 3 {
		t.Errorf("expected 3 PRs, got %d", len(prs))
	}

	// Verify all are tracked
	for _, prNum := range []int{1, 2, 3} {
		if _, ok := c.GetPRState(prNum); !ok {
			t.Errorf("PR %d should be tracked", prNum)
		}
	}
}

func TestController_ProcessPR_FailedStageNoOp(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set PR to failed state
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		Stage:    StageFailed,
	}
	c.mu.Unlock()

	// Processing failed stage should be a no-op
	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Errorf("ProcessPR on failed stage should not error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage should remain %s, got %s", StageFailed, pr.Stage)
	}
}

func TestController_ProcessPR_ProdRequiresApproval(t *testing.T) {
	// Test that prod goes to awaiting approval after CI passes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	_ = c.ProcessPR(ctx, 42, nil)

	// Stage 2: waiting CI → CI passed
	_ = c.ProcessPR(ctx, 42, nil)

	// Stage 3: CI passed → awaiting approval (prod)
	_ = c.ProcessPR(ctx, 42, nil)

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("Stage = %s, want %s for prod environment", pr.Stage, StageAwaitApproval)
	}
}

func TestController_RemovePR(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "url", 10, "sha", "pilot/GH-10")

	// Verify exists
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should exist")
	}

	// Remove
	c.removePR(42)

	// Verify removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed")
	}
}

func TestController_SuccessResetsFailureCount(t *testing.T) {
	// Successful processing should reset per-PR failures
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage // Use stage to have predictable behavior
	cfg.MaxFailures = 5

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "url", 10, "abc1234", "pilot/GH-10")

	// Set some failures for this specific PR
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 2, LastFailureTime: time.Now()}
	c.mu.Unlock()

	// Successful processing (pr_created → waiting_ci)
	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	failures := c.GetPRFailures(42)
	if failures != 0 {
		t.Errorf("PR failures = %d, want 0 after successful processing", failures)
	}
}

func TestController_MergeAttemptIncrement(t *testing.T) {
	mergeCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			// Return successful CI checks for pre-merge verification
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			mergeCallCount++
			// Fail first ProcessPR call (use 422 which is non-retryable), succeed second
			if mergeCallCount == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
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
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start at merging stage
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
		Stage:    StageMerging,
	}
	c.mu.Unlock()

	ctx := context.Background()

	// First attempt fails (merge fails, not CI verification)
	err := c.ProcessPR(ctx, 42, nil)
	if err == nil {
		t.Error("first merge attempt should fail")
	}

	pr, _ := c.GetPRState(42)
	if pr.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1", pr.MergeAttempts)
	}

	// Second attempt succeeds
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Errorf("second merge attempt should succeed: %v", err)
	}

	pr, _ = c.GetPRState(42)
	if pr.MergeAttempts != 2 {
		t.Errorf("MergeAttempts = %d, want 2", pr.MergeAttempts)
	}
}

func TestController_ScanExistingPRs(t *testing.T) {
	tests := []struct {
		name          string
		prs           []github.PullRequest
		wantRestored  int
		wantIssueNums []int
	}{
		{
			name: "restores pilot PRs only",
			prs: []github.PullRequest{
				{Number: 1, Head: github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"}, HTMLURL: "url1"},
				{Number: 2, Head: github.PRRef{Ref: "feature/other", SHA: "sha2"}, HTMLURL: "url2"},
				{Number: 3, Head: github.PRRef{Ref: "pilot/GH-200", SHA: "sha3"}, HTMLURL: "url3"},
			},
			wantRestored:  2,
			wantIssueNums: []int{100, 200},
		},
		{
			name: "no pilot PRs",
			prs: []github.PullRequest{
				{Number: 1, Head: github.PRRef{Ref: "feature/one", SHA: "sha1"}, HTMLURL: "url1"},
				{Number: 2, Head: github.PRRef{Ref: "fix/two", SHA: "sha2"}, HTMLURL: "url2"},
			},
			wantRestored:  0,
			wantIssueNums: []int{},
		},
		{
			name:          "empty PR list",
			prs:           []github.PullRequest{},
			wantRestored:  0,
			wantIssueNums: []int{},
		},
		{
			name: "various pilot branch patterns",
			prs: []github.PullRequest{
				{Number: 1, Head: github.PRRef{Ref: "pilot/GH-1", SHA: "sha1"}, HTMLURL: "url1"},
				{Number: 2, Head: github.PRRef{Ref: "pilot/GH-999", SHA: "sha2"}, HTMLURL: "url2"},
				{Number: 3, Head: github.PRRef{Ref: "pilot-GH-123", SHA: "sha3"}, HTMLURL: "url3"}, // wrong pattern
				{Number: 4, Head: github.PRRef{Ref: "pilot/gh-456", SHA: "sha4"}, HTMLURL: "url4"}, // wrong case
			},
			wantRestored:  2,
			wantIssueNums: []int{1, 999},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls" {
					// Convert to pointer slice for JSON encoding
					prs := make([]*github.PullRequest, len(tt.prs))
					for i := range tt.prs {
						prs[i] = &tt.prs[i]
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(prs)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			err := c.ScanExistingPRs(context.Background())
			if err != nil {
				t.Fatalf("ScanExistingPRs() error = %v", err)
			}

			prs := c.GetActivePRs()
			if len(prs) != tt.wantRestored {
				t.Errorf("restored %d PRs, want %d", len(prs), tt.wantRestored)
			}

			// Verify issue numbers were extracted correctly
			for _, wantIssue := range tt.wantIssueNums {
				found := false
				for _, pr := range prs {
					if pr.IssueNumber == wantIssue {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("issue number %d not found in restored PRs", wantIssue)
				}
			}
		})
	}
}

func TestController_ScanExistingPRs_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	err := c.ScanExistingPRs(context.Background())
	if err == nil {
		t.Error("ScanExistingPRs() should return error on API failure")
	}
}

func TestController_CheckExternalMerge(t *testing.T) {
	// Test that externally merged PRs are detected and removed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as merged
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// Verify PR is tracked
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should be tracked initially")
	}

	// Process PRs - should detect external merge and remove
	c.processAllPRs(context.Background())

	// Verify PR is removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}
}

func TestController_CheckExternalClose(t *testing.T) {
	// Test that externally closed (without merge) PRs are detected and removed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as closed but not merged
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  false,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// Verify PR is tracked
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should be tracked initially")
	}

	// Process PRs - should detect external close and remove
	c.processAllPRs(context.Background())

	// Verify PR is removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external close detection")
	}
}

func TestController_CheckExternalMergeOrClose_OpenPR(t *testing.T) {
	// Test that open PRs are processed normally
	ciCheckCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as still open
			resp := github.PullRequest{
				Number:  42,
				State:   "open",
				Merged:  false,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/abc1234567890/check-runs":
			ciCheckCalled = true
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start at waiting CI stage
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
		Stage:    StageWaitingCI,
	}
	c.mu.Unlock()

	// Process PRs - should check state then continue processing
	c.processAllPRs(context.Background())

	// Verify PR is still tracked
	if _, ok := c.GetPRState(42); !ok {
		t.Error("open PR should still be tracked")
	}

	// Verify normal processing continued (CI check was called)
	if !ciCheckCalled {
		t.Error("CI check should have been called for open PR")
	}
}

func TestController_CheckExternalMerge_APIError(t *testing.T) {
	// Test that API errors don't remove PRs - they're kept for retry on next poll cycle.
	// With the PR caching optimization (GH-1304), we skip processing if GetPR fails
	// to avoid operating on stale data. The PR remains tracked for the next poll.

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return error - simulates transient API failure
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start at waiting CI stage
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
		Stage:    StageWaitingCI,
	}
	c.mu.Unlock()

	// Process PRs - should fail to fetch PR state, skip processing, but keep PR tracked
	c.processAllPRs(context.Background())

	// Verify PR is still tracked (error shouldn't remove it)
	if _, ok := c.GetPRState(42); !ok {
		t.Error("PR should still be tracked after API error")
	}

	// Verify stage hasn't changed (processing was skipped due to API error)
	prState, _ := c.GetPRState(42)
	if prState.Stage != StageWaitingCI {
		t.Errorf("PR stage should remain waiting_ci, got %s", prState.Stage)
	}
}

func TestController_CheckExternalMerge_WithNotifier(t *testing.T) {
	// Test that notifier is called when external merge is detected
	notifyMergedCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as merged
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
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

	// Set up mock notifier
	mockNotifier := &mockNotifier{
		notifyMergedFunc: func(ctx context.Context, prState *PRState) error {
			notifyMergedCalled = true
			if prState.PRNumber != 42 {
				t.Errorf("notified PR number = %d, want 42", prState.PRNumber)
			}
			return nil
		},
	}
	c.SetNotifier(mockNotifier)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// Process PRs - should detect external merge and notify
	c.processAllPRs(context.Background())

	// Verify notifier was called
	if !notifyMergedCalled {
		t.Error("NotifyMerged should have been called for external merge")
	}
}

// GH-1486: Test that external merge closes the associated issue
func TestController_CheckExternalMerge_ClosesIssue(t *testing.T) {
	var (
		addLabelsCalled     bool
		removeLabelInProg   bool
		removeLabelFailed   bool
		issueStateClosed    bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			// AddLabels call - body is {"labels": ["pilot-done"]}
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body["labels"] {
				if l == "pilot-done" {
					addLabelsCalled = true
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: "pilot-done"}})

		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			removeLabelInProg = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-failed" && r.Method == http.MethodDelete:
			removeLabelFailed = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodPatch:
			// UpdateIssueState call
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["state"] == "closed" {
				issueStateClosed = true
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// Process PRs - should detect external merge and close issue
	c.processAllPRs(context.Background())

	// Verify PR is removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}

	// Verify issue operations
	if !addLabelsCalled {
		t.Error("pilot-done label should be added to issue")
	}
	if !removeLabelInProg {
		t.Error("pilot-in-progress label should be removed from issue")
	}
	if !removeLabelFailed {
		t.Error("pilot-failed label should be removed from issue")
	}
	if !issueStateClosed {
		t.Error("issue should be closed after external merge")
	}
}

func TestController_CheckExternalMerge_MultiplePRs(t *testing.T) {
	// Test processing multiple PRs where some are merged externally
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/1":
			// PR 1 is still open
			resp := github.PullRequest{Number: 1, State: "open", Merged: false}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/2":
			// PR 2 was merged externally
			resp := github.PullRequest{Number: 2, State: "closed", Merged: true}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/3":
			// PR 3 was closed externally
			resp := github.PullRequest{Number: 3, State: "closed", Merged: false}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
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

	// Add multiple PRs
	c.OnPRCreated(1, "url1", 10, "sha1", "pilot/GH-10")
	c.OnPRCreated(2, "url2", 20, "sha2", "pilot/GH-20")
	c.OnPRCreated(3, "url3", 30, "sha3", "pilot/GH-30")

	// Process PRs
	c.processAllPRs(context.Background())

	// PR 1 should still be tracked (open)
	if _, ok := c.GetPRState(1); !ok {
		t.Error("PR 1 should still be tracked (open)")
	}

	// PR 2 should be removed (merged externally)
	if _, ok := c.GetPRState(2); ok {
		t.Error("PR 2 should be removed (merged externally)")
	}

	// PR 3 should be removed (closed externally)
	if _, ok := c.GetPRState(3); ok {
		t.Error("PR 3 should be removed (closed externally)")
	}
}

// mockNotifier is a test double for the Notifier interface
type mockNotifier struct {
	notifyMergedFunc           func(ctx context.Context, prState *PRState) error
	notifyCIFailedFunc         func(ctx context.Context, prState *PRState, failedChecks []string) error
	notifyApprovalRequiredFunc func(ctx context.Context, prState *PRState) error
	notifyFixIssueCreatedFunc  func(ctx context.Context, prState *PRState, issueNumber int) error
	notifyReleasedFunc         func(ctx context.Context, prState *PRState, releaseURL string) error
}

func (m *mockNotifier) NotifyMerged(ctx context.Context, prState *PRState) error {
	if m.notifyMergedFunc != nil {
		return m.notifyMergedFunc(ctx, prState)
	}
	return nil
}

func (m *mockNotifier) NotifyCIFailed(ctx context.Context, prState *PRState, failedChecks []string) error {
	if m.notifyCIFailedFunc != nil {
		return m.notifyCIFailedFunc(ctx, prState, failedChecks)
	}
	return nil
}

func (m *mockNotifier) NotifyApprovalRequired(ctx context.Context, prState *PRState) error {
	if m.notifyApprovalRequiredFunc != nil {
		return m.notifyApprovalRequiredFunc(ctx, prState)
	}
	return nil
}

func (m *mockNotifier) NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error {
	if m.notifyFixIssueCreatedFunc != nil {
		return m.notifyFixIssueCreatedFunc(ctx, prState, issueNumber)
	}
	return nil
}

func (m *mockNotifier) NotifyReleased(ctx context.Context, prState *PRState, releaseURL string) error {
	if m.notifyReleasedFunc != nil {
		return m.notifyReleasedFunc(ctx, prState, releaseURL)
	}
	return nil
}

// GH-457: Test that handleWaitingCI refreshes stale HeadSHA from GitHub.
// When self-review pushes new commits, OnPRCreated stores the pre-self-review SHA.
// The controller must fetch the actual HEAD from GitHub before checking CI.
func TestController_ProcessPR_RefreshesStaleHeadSHA(t *testing.T) {
	staleSHA := "stale1234567890"
	actualSHA := "actual1234567890"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR with actual HEAD SHA (different from stale SHA)
			resp := github.PullRequest{
				Number: 42,
				Head:   github.PRRef{SHA: actualSHA},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + actualSHA + "/check-runs":
			// CI passes for actual SHA
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + staleSHA + "/check-runs":
			// No CI for stale SHA (this is the bug scenario)
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Register PR with stale SHA (simulates self-review changing HEAD)
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, staleSHA, "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → should refresh SHA and find CI passed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)

	// Verify SHA was refreshed
	if pr.HeadSHA != actualSHA {
		t.Errorf("HeadSHA = %s, want %s (should have been refreshed from GitHub)", pr.HeadSHA, actualSHA)
	}

	// Verify CI passed with actual SHA
	if pr.CIStatus != CISuccess {
		t.Errorf("CIStatus = %s, want %s", pr.CIStatus, CISuccess)
	}
	if pr.Stage != StageCIPassed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageCIPassed)
	}
}

// GH-457: Test that without the fix, stale SHA would cause CI to stay pending.
// This validates the bug scenario explicitly.
func TestController_ProcessPR_StaleSHAWithoutRefreshWouldStayPending(t *testing.T) {
	staleSHA := "stale1234567890"
	actualSHA := "actual1234567890"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number: 42,
				Head:   github.PRRef{SHA: actualSHA},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + actualSHA + "/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + staleSHA + "/check-runs":
			// Stale SHA has no check runs — this is what caused the bug
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	// Verify what happens when we check CI against stale SHA directly
	ciMonitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// Stale SHA returns no checks → CIPending
	staleStatus, err := ciMonitor.CheckCI(context.Background(), staleSHA)
	if err != nil {
		t.Fatalf("CheckCI for stale SHA failed: %v", err)
	}
	if staleStatus != CIPending {
		t.Errorf("stale SHA status = %s, want %s (no checks = pending)", staleStatus, CIPending)
	}

	// Actual SHA returns passing checks → CISuccess
	actualStatus, err := ciMonitor.CheckCI(context.Background(), actualSHA)
	if err != nil {
		t.Fatalf("CheckCI for actual SHA failed: %v", err)
	}
	if actualStatus != CISuccess {
		t.Errorf("actual SHA status = %s, want %s", actualStatus, CISuccess)
	}
}

// GH-724: Test that merge conflicts are detected during WaitingCI and PR is closed immediately.
func TestController_ProcessPR_MergeConflict_WaitingCI(t *testing.T) {
	prCommented := false
	prClosed := false
	labelRemoved := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			// Return PR with merge conflict
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			// GH-1796: auto-rebase fails (true conflict)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == "POST":
			prCommented = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == "DELETE":
			labelRemoved = true
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
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Inject PR state directly at StageWaitingCI to test the handleWaitingCI path
	// (bypassing handlePRCreated which also checks conflicts now)
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:        42,
		PRURL:           "https://github.com/owner/repo/pull/42",
		IssueNumber:     10,
		BranchName:      "pilot/GH-10",
		HeadSHA:         "abc1234",
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now(),
		CreatedAt:       time.Now(),
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Process PR in WaitingCI stage → should detect conflict and fail immediately
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (conflict should immediately fail)", pr.Stage, StageFailed)
	}
	if pr.Error != "merge conflict with base branch" {
		t.Errorf("Error = %q, want %q", pr.Error, "merge conflict with base branch")
	}
	if !prCommented {
		t.Error("PR should have been commented with conflict explanation")
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}
}

// GH-724: Test that merge conflicts are detected immediately on PR creation.
func TestController_ProcessPR_MergeConflict_PRCreated(t *testing.T) {
	prClosed := false
	labelRemoved := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			// GH-1796: auto-rebase fails (true conflict)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == "DELETE":
			labelRemoved = true
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → should detect conflict immediately and skip CI wait
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (conflict on creation should fail immediately)", pr.Stage, StageFailed)
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}
}

// GH-724: Test that unknown mergeable state (GitHub still computing) proceeds to CI check normally.
func TestController_ProcessPR_MergeableUnknown_ProceedsToCICheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			// Mergeable is nil (GitHub hasn't computed yet)
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				MergeableState: "unknown",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI (unknown mergeable should NOT block)
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s (unknown mergeable should proceed to CI)", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed (should check CI normally despite unknown mergeable)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageCIPassed)
	}
}

// GH-724: Test that mergeable=false (without dirty state) also triggers conflict detection.
func TestController_ProcessPR_MergeableFalse_DetectsConflict(t *testing.T) {
	prClosed := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			// mergeable=false but mergeable_state not set (older API responses)
			resp := github.PullRequest{
				Number:    42,
				Head:      github.PRRef{SHA: "abc1234"},
				Mergeable: &mergeable,
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			// GH-1796: auto-rebase fails (true conflict)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
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
	cfg.DevCITimeout = 1 * time.Second

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → conflict detected via mergeable=false
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
}

// GH-834: Test that per-PR circuit breaker doesn't block other PRs.
func TestController_PerPRCircuitBreaker_DoesNotBlockOtherPRs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/merge":
			// Always fail merge for PR 42
			w.WriteHeader(http.StatusInternalServerError)
		case "/repos/owner/repo/pulls/43/merge":
			// Always succeed for PR 43
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/commits/sha42/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/sha43/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxFailures = 2
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR 42 at merging stage (will fail)
	c.mu.Lock()
	c.activePRs[42] = &PRState{PRNumber: 42, HeadSHA: "sha42", Stage: StageMerging}
	c.activePRs[43] = &PRState{PRNumber: 43, HeadSHA: "sha43", Stage: StageMerging}
	c.mu.Unlock()

	ctx := context.Background()

	// Cause failures on PR 42 until circuit opens
	for i := 0; i < 2; i++ {
		_ = c.ProcessPR(ctx, 42, nil)
	}

	// PR 42's circuit should be open
	if !c.IsPRCircuitOpen(42) {
		t.Error("PR 42's circuit breaker should be open")
	}

	// PR 43's circuit should NOT be open
	if c.IsPRCircuitOpen(43) {
		t.Error("PR 43's circuit breaker should NOT be open (independent of PR 42)")
	}

	// PR 43 should still be processable
	err := c.ProcessPR(ctx, 43, nil)
	if err != nil {
		t.Errorf("PR 43 should be processable despite PR 42's failures: %v", err)
	}

	// PR 42 should be blocked
	err = c.ProcessPR(ctx, 42, nil)
	if err == nil {
		t.Error("PR 42 should be blocked by its per-PR circuit breaker")
	}
}

// GH-834: Test that per-PR circuit breaker resets after timeout.
func TestController_PerPRCircuitBreaker_ResetsAfterTimeout(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 2
	cfg.FailureResetTimeout = 50 * time.Millisecond // Short timeout for testing

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up failure state with old timestamp
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{
		FailureCount:    5,
		LastFailureTime: time.Now().Add(-100 * time.Millisecond), // Older than timeout
	}
	c.activePRs[42] = &PRState{PRNumber: 42, Stage: StagePRCreated}
	c.mu.Unlock()

	// Circuit should be closed because timeout has passed
	if c.IsPRCircuitOpen(42) {
		t.Error("circuit should be closed after timeout passed")
	}
}

// GH-834: Test ResetPRCircuitBreaker for single PR.
func TestController_ResetPRCircuitBreaker(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 2

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up failure state for multiple PRs
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 5, LastFailureTime: time.Now()}
	c.prFailures[43] = &prFailureState{FailureCount: 5, LastFailureTime: time.Now()}
	c.mu.Unlock()

	// Both should be open
	if !c.IsPRCircuitOpen(42) {
		t.Error("PR 42 circuit should be open")
	}
	if !c.IsPRCircuitOpen(43) {
		t.Error("PR 43 circuit should be open")
	}

	// Reset only PR 42
	c.ResetPRCircuitBreaker(42)

	// PR 42 should be closed, PR 43 still open
	if c.IsPRCircuitOpen(42) {
		t.Error("PR 42 circuit should be closed after reset")
	}
	if !c.IsPRCircuitOpen(43) {
		t.Error("PR 43 circuit should still be open")
	}
}

// GH-834: Test GetPRFailures returns correct count.
func TestController_GetPRFailures(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Initially zero
	if c.GetPRFailures(42) != 0 {
		t.Error("initial failures should be 0")
	}

	// Set failures
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 3, LastFailureTime: time.Now()}
	c.mu.Unlock()

	if c.GetPRFailures(42) != 3 {
		t.Errorf("failures = %d, want 3", c.GetPRFailures(42))
	}
}

// GH-834: Test that IsCircuitOpen returns true only when at least one PR is blocked.
func TestController_IsCircuitOpen_AnyPRBlocked(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// No failures — circuit closed
	if c.IsCircuitOpen() {
		t.Error("circuit should be closed with no failures")
	}

	// Add failures below threshold
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 2, LastFailureTime: time.Now()}
	c.mu.Unlock()

	if c.IsCircuitOpen() {
		t.Error("circuit should be closed with failures below threshold")
	}

	// Add failures at threshold
	c.mu.Lock()
	c.prFailures[42].FailureCount = 3
	c.mu.Unlock()

	if !c.IsCircuitOpen() {
		t.Error("circuit should be open with failures at threshold")
	}
}

// TestController_DeadlockDetection tests the deadlock detection mechanism (GH-849).
func TestController_DeadlockDetection(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Initial state: lastProgressAt should be set to now
	initialProgress := c.GetLastProgressAt()
	if initialProgress.IsZero() {
		t.Error("lastProgressAt should be initialized on construction")
	}

	// Alert flag should start as false
	if c.IsDeadlockAlertSent() {
		t.Error("deadlockAlertSent should be false initially")
	}

	// Mark alert as sent
	c.MarkDeadlockAlertSent()
	if !c.IsDeadlockAlertSent() {
		t.Error("deadlockAlertSent should be true after marking")
	}

	// Simulate a PR state transition by adding a PR
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// Get PR and manually trigger a stage transition to update lastProgressAt
	pr, _ := c.GetPRState(42)
	previousStage := pr.Stage
	pr.Stage = StageWaitingCI

	// Simulate what ProcessPR does on stage transition
	c.mu.Lock()
	if pr.Stage != previousStage {
		c.lastProgressAt = time.Now()
		c.deadlockAlertSent = false
	}
	c.mu.Unlock()

	// After transition, lastProgressAt should be updated and alert flag reset
	newProgress := c.GetLastProgressAt()
	if !newProgress.After(initialProgress) && !newProgress.Equal(initialProgress) {
		t.Error("lastProgressAt should be updated after stage transition")
	}
	if c.IsDeadlockAlertSent() {
		t.Error("deadlockAlertSent should be reset after stage transition")
	}
}

// TestController_DeadlockDetection_StaleProgress tests that stale progress is detected.
func TestController_DeadlockDetection_StaleProgress(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Manually set lastProgressAt to 2 hours ago
	c.mu.Lock()
	c.lastProgressAt = time.Now().Add(-2 * time.Hour)
	c.mu.Unlock()

	// Check that GetLastProgressAt returns the stale time
	progress := c.GetLastProgressAt()
	if time.Since(progress) < 1*time.Hour {
		t.Error("lastProgressAt should be more than 1 hour ago")
	}

	// Add a PR to simulate active work
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// The MetricsAlerter would check: noProgressMin >= 60 && len(activePRs) > 0
	noProgressMin := time.Since(c.GetLastProgressAt()).Minutes()
	activePRs := c.GetActivePRs()

	if noProgressMin < 60 {
		t.Errorf("noProgressMin = %.1f, expected >= 60", noProgressMin)
	}
	if len(activePRs) == 0 {
		t.Error("expected active PRs")
	}

	// This is the condition that would trigger a deadlock alert
	deadlockDetected := noProgressMin >= 60 && !c.IsDeadlockAlertSent() && len(activePRs) > 0
	if !deadlockDetected {
		t.Error("deadlock condition should be detected")
	}
}

// TestController_handleMerging_ConflictClearsLabel tests GH-880:
// When merge fails due to conflict, handleMergeConflict should be called
// which removes pilot-in-progress label so the issue can be retried.
func TestController_handleMerging_ConflictClearsLabel(t *testing.T) {
	labelRemoved := false
	prClosed := false
	commentAdded := false
	mergeAttempted := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			mergeAttempted = true
			// Return 405 to simulate conflict error
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Pull Request is not mergeable",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == http.MethodPut:
			// GH-1796: auto-rebase fails (true conflict), fall through to close-and-retry
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "merge conflict between base and head",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			if r.Method == http.MethodPatch {
				// Close PR request
				prClosed = true
				w.WriteHeader(http.StatusOK)
				return
			}
			// GET PR - return with conflict state
			pr := github.PullRequest{
				Number:         42,
				State:          "open",
				Mergeable:      &mergeable,
				MergeableState: "dirty",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			labelRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR in StageMerging state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging

	ctx := context.Background()

	// Process PR - merge should fail and trigger conflict handling
	err := c.ProcessPR(ctx, 42, nil)

	// No error returned because handleMergeConflict handles it gracefully
	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !mergeAttempted {
		t.Error("merge should have been attempted")
	}

	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}

	if !prClosed {
		t.Error("PR should have been closed")
	}

	if !commentAdded {
		t.Error("comment should have been added to PR")
	}

	// PR should be in Failed state
	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error != "merge conflict with base branch" {
		t.Errorf("Error = %q, want 'merge conflict with base branch'", prState.Error)
	}
}

// TestController_handleMerging_Success_RemovesFailedLabel tests GH-1302:
// When a retry succeeds and PR merges, pilot-failed label should be removed.
func TestController_handleMerging_Success_RemovesFailedLabel(t *testing.T) {
	pilotDoneAdded := false
	pilotInProgressRemoved := false
	pilotFailedRemoved := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge" && r.Method == http.MethodPut:
			// Successful merge
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha":     "merged123",
				"merged":  true,
				"message": "Pull Request successfully merged",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
			pr := github.PullRequest{
				Number: 42,
				State:  "open",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			pilotDoneAdded = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: github.LabelDone}})
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			pilotInProgressRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-failed" && r.Method == http.MethodDelete:
			pilotFailedRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR in StageMerging state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging

	ctx := context.Background()

	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !pilotDoneAdded {
		t.Error("pilot-done label should have been added")
	}

	if !pilotInProgressRemoved {
		t.Error("pilot-in-progress label should have been removed")
	}

	if !pilotFailedRemoved {
		t.Error("pilot-failed label should have been removed (GH-1302)")
	}

	// PR should be in Merged state
	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageMerged)
	}
}

// Test consecutive API failure counter logic
func TestController_ConsecutiveAPIFailures(t *testing.T) {
	// Mock HTTP server that always returns error for check runs API
	var apiCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount++
		if strings.Contains(r.URL.Path, "check-runs") {
			// Return error for CI checks
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"API Error"}`))
			return
		}
		// Default PR response for GetPullRequest calls
		if strings.Contains(r.URL.Path, "/pulls/") {
			pr := map[string]interface{}{
				"number":    42,
				"state":     "open",
				"merged":    false,
				"mergeable": true,
				"head": map[string]interface{}{
					"sha": "abc1234",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(pr)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	// Set PR to waiting CI stage
	prState, _ := c.GetPRState(42)
	prState.Stage = StageWaitingCI

	ctx := context.Background()

	// Call ProcessPR 4 times - failures should increment but not transition to StageFailed yet
	for i := 1; i <= 4; i++ {
		err := c.ProcessPR(ctx, 42, nil)
		if err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i, err)
		}

		prState, _ = c.GetPRState(42)
		if prState.ConsecutiveAPIFailures != i {
			t.Errorf("after %d failures: ConsecutiveAPIFailures = %d, want %d", i, prState.ConsecutiveAPIFailures, i)
		}
		if prState.Stage != StageWaitingCI {
			t.Errorf("after %d failures: Stage = %s, want %s", i, prState.Stage, StageWaitingCI)
		}
	}

	// 5th failure should transition to StageFailed
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR 5th iteration error: %v", err)
	}

	prState, _ = c.GetPRState(42)
	if prState.ConsecutiveAPIFailures != 5 {
		t.Errorf("after 5 failures: ConsecutiveAPIFailures = %d, want 5", prState.ConsecutiveAPIFailures)
	}
	if prState.Stage != StageFailed {
		t.Errorf("after 5 failures: Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, "CI check API failed 5 consecutive times") {
		t.Errorf("Error = %q, should contain consecutive API failure message", prState.Error)
	}
}

// Test that consecutive failure counter resets on successful API call
func TestController_ConsecutiveAPIFailures_Reset(t *testing.T) {
	// Mock HTTP server that fails 3 times then succeeds
	var apiCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "check-runs") {
			apiCallCount++
			if apiCallCount <= 3 {
				// Return error for first 3 CI checks
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"API Error"}`))
				return
			}
			// Success on 4th call - return successful CI
			response := map[string]interface{}{
				"total_count": 1,
				"check_runs": []map[string]interface{}{
					{
						"name":       "build",
						"status":     "completed",
						"conclusion": "success",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		// Default PR response for GetPullRequest calls
		if strings.Contains(r.URL.Path, "/pulls/") {
			pr := map[string]interface{}{
				"number":    42,
				"state":     "open",
				"merged":    false,
				"mergeable": true,
				"head": map[string]interface{}{
					"sha": "abc1234",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(pr)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	// Set PR to waiting CI stage
	prState, _ := c.GetPRState(42)
	prState.Stage = StageWaitingCI

	ctx := context.Background()

	// Call ProcessPR 3 times with failures
	for i := 1; i <= 3; i++ {
		err := c.ProcessPR(ctx, 42, nil)
		if err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i, err)
		}
		prState, _ = c.GetPRState(42)
		if prState.ConsecutiveAPIFailures != i {
			t.Errorf("after %d failures: ConsecutiveAPIFailures = %d, want %d", i, prState.ConsecutiveAPIFailures, i)
		}
	}

	// 4th call succeeds - counter should reset and transition to StageCIPassed
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR 4th iteration (success) error: %v", err)
	}

	prState, _ = c.GetPRState(42)
	if prState.ConsecutiveAPIFailures != 0 {
		t.Errorf("after success: ConsecutiveAPIFailures = %d, want 0", prState.ConsecutiveAPIFailures)
	}
	if prState.Stage != StageCIPassed {
		t.Errorf("after success: Stage = %s, want %s", prState.Stage, StageCIPassed)
	}
}

// mockTaskMonitor implements TaskMonitor for testing.
type mockTaskMonitor struct {
	completedTasks map[string]string // taskID -> prURL
}

func newMockTaskMonitor() *mockTaskMonitor {
	return &mockTaskMonitor{
		completedTasks: make(map[string]string),
	}
}

func (m *mockTaskMonitor) Complete(taskID, prURL string) {
	m.completedTasks[taskID] = prURL
}

// TestController_MonitorCompletedOnMerge verifies that when autopilot successfully
// merges a PR, it calls monitor.Complete() to sync dashboard state.
// GH-1336: Dashboard shows stale "failed" status because autopilot didn't update monitor.
func TestController_MonitorCompletedOnMerge(t *testing.T) {
	mergeWasCalled := false
	labelsAdded := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels") && r.Method == "POST":
			// Track labels added
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			labelsAdded = append(labelsAdded, labels...)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	// Create controller with mock monitor
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	mockMonitor := newMockTaskMonitor()
	c.SetMonitor(mockMonitor)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Process through the stages: PR created → waiting CI → CI passed → merging → merged
	for i := 0; i < 5; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}

	// Verify merge was called
	if !mergeWasCalled {
		t.Error("merge should have been called")
	}

	// Verify monitor.Complete was called with correct taskID
	expectedTaskID := "GH-10"
	prURL, ok := mockMonitor.completedTasks[expectedTaskID]
	if !ok {
		t.Errorf("monitor.Complete was not called for taskID %s", expectedTaskID)
	}
	if prURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("monitor.Complete prURL = %s, want https://github.com/owner/repo/pull/42", prURL)
	}
}

// TestController_MonitorNilSafe verifies that nil monitor doesn't cause panic.
func TestController_MonitorNilSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	// Create controller WITHOUT setting monitor (nil monitor)
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// Intentionally NOT calling c.SetMonitor(...)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Process through all stages - should not panic even with nil monitor
	for i := 0; i < 5; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}
	// If we get here without panic, nil safety is verified
}

// GH-1566: Test that CI fix cascade stops after MaxCIFixIterations.
func TestController_CIFixCascadeLimit(t *testing.T) {
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == "GET":
			// Return issue body with iteration:3 (at the limit)
			resp := github.Issue{
				Number: 10,
				Body:   "Fix CI failure\n\n<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:3 -->\n",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → CI failed
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageCIFailed {
		t.Fatalf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIFailed)
	}

	// Stage 3: CI failed → should NOT create fix issue (iteration limit reached)
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue should NOT have been created when iteration limit is reached")
	}
	if !prClosed {
		t.Error("failed PR should still be closed when iteration limit is reached")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !strings.Contains(pr.Error, "CI fix iteration limit reached") {
		t.Errorf("error should mention iteration limit, got: %s", pr.Error)
	}
}

// GH-1566: Test that CI fix proceeds when under the iteration limit.
func TestController_CIFixCascade_UnderLimit(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == "GET":
			// Return issue body with iteration:1 (under the limit of 3)
			resp := github.Issue{
				Number: 10,
				Body:   "Fix CI failure\n\n<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:1 -->\n",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// PR created → waiting CI → CI failed → create fix issue
	for i := 0; i < 3; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}

	if !issueCreated {
		t.Error("fix issue should have been created when under iteration limit")
	}
}

// GH-1566: Test that original PRs (no autopilot-meta) work normally.
func TestController_CIFixCascade_OriginalPR(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == "GET":
			// Return original issue (no autopilot-meta, iteration=0)
			resp := github.Issue{
				Number: 10,
				Body:   "Original task: implement feature X",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")

	ctx := context.Background()

	// PR created → waiting CI → CI failed → create fix issue (iteration 0 < 3)
	for i := 0; i < 3; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}

	if !issueCreated {
		t.Error("fix issue should have been created for original PR (no iteration metadata)")
	}
}

// GH-1566: Test parseAutopilotIteration function.
func TestController_ShouldTriggerRelease(t *testing.T) {
	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		envName       string
		want          bool
	}{
		{
			name:          "global only enabled",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			want:          true,
		},
		{
			name:          "global only disabled",
			globalRelease: &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			want:          false,
		},
		{
			name:       "env only enabled",
			envRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envName:    "prod",
			want:       true,
		},
		{
			name:       "env only disabled",
			envRelease: &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			envName:    "prod",
			want:       false,
		},
		{
			name:          "env overrides global - env enabled",
			globalRelease: &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			envRelease:    &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envName:       "prod",
			want:          true,
		},
		{
			name:          "env overrides global - env disabled",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envRelease:    &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			envName:       "prod",
			want:          false,
		},
		{
			name: "both nil",
			want: false,
		},
		{
			name:          "global enabled but trigger manual",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "manual"},
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghClient := github.NewClient(testutil.FakeGitHubToken)
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease

			if tt.envName != "" && tt.envRelease != nil {
				cfg.Environments[tt.envName] = &EnvironmentConfig{
					Release: tt.envRelease,
				}
				if err := cfg.SetActiveEnvironment(tt.envName); err != nil {
					t.Fatalf("SetActiveEnvironment: %v", err)
				}
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")
			got := c.shouldTriggerRelease()
			if got != tt.want {
				t.Errorf("shouldTriggerRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestController_ResolvedRelease(t *testing.T) {
	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		envName       string
		wantTagPrefix string
		wantNil       bool
	}{
		{
			name:          "env release takes precedence",
			globalRelease: &ReleaseConfig{TagPrefix: "global-v"},
			envRelease:    &ReleaseConfig{TagPrefix: "env-v"},
			envName:       "prod",
			wantTagPrefix: "env-v",
		},
		{
			name:          "falls back to global when env has no release",
			globalRelease: &ReleaseConfig{TagPrefix: "global-v"},
			envName:       "dev",
			wantTagPrefix: "global-v",
		},
		{
			name:    "both nil returns nil",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghClient := github.NewClient(testutil.FakeGitHubToken)
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease

			if tt.envName != "" {
				env := cfg.Environments[tt.envName]
				if env == nil {
					env = &EnvironmentConfig{}
					cfg.Environments[tt.envName] = env
				}
				env.Release = tt.envRelease
				if err := cfg.SetActiveEnvironment(tt.envName); err != nil {
					t.Fatalf("SetActiveEnvironment: %v", err)
				}
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")
			got := c.resolvedRelease()
			if tt.wantNil {
				if got != nil {
					t.Errorf("resolvedRelease() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("resolvedRelease() = nil, want non-nil")
			}
			if got.TagPrefix != tt.wantTagPrefix {
				t.Errorf("resolvedRelease().TagPrefix = %q, want %q", got.TagPrefix, tt.wantTagPrefix)
			}
		})
	}
}

func TestParseAutopilotIteration(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"iteration 1", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:1 -->", 1},
		{"iteration 3", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:3 -->", 3},
		{"iteration 0", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:0 -->", 0},
		{"no iteration", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 -->", 0},
		{"no metadata", "just a normal issue body", 0},
		{"empty body", "", 0},
		{"embedded in body", "# Fix\n\n## Context\nstuff\n\n<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:5 -->\n", 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAutopilotIteration(tt.body)
			if got != tt.want {
				t.Errorf("parseAutopilotIteration() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestController_handleMergeConflict_AutoRebaseSuccess tests GH-1796:
// When merge conflict is detected and GitHub can auto-update the branch,
// the PR stays open and transitions to StageWaitingCI.
func TestController_handleMergeConflict_AutoRebaseSuccess(t *testing.T) {
	updateBranchCalled := false
	prClosed := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			// Return 405 to simulate conflict error on merge attempt
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Pull Request is not mergeable",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == http.MethodPut:
			updateBranchCalled = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			if r.Method == http.MethodPatch {
				prClosed = true
				w.WriteHeader(http.StatusOK)
				return
			}
			// GET PR - return with conflict state
			pr := github.PullRequest{
				Number:         42,
				State:          "open",
				Mergeable:      &mergeable,
				MergeableState: "dirty",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR in StageMerging state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging

	ctx := context.Background()
	err := c.ProcessPR(ctx, 42, nil)

	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !updateBranchCalled {
		t.Error("UpdatePullRequestBranch should have been called")
	}

	if prClosed {
		t.Error("PR should NOT have been closed after successful auto-rebase")
	}

	// PR should transition to WaitingCI
	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageWaitingCI)
	}
	if prState.HeadSHA != "" {
		t.Errorf("HeadSHA should be empty to force refresh, got %q", prState.HeadSHA)
	}
}

// TestController_handleMergeConflict_AutoRebaseFails tests GH-1796:
// When auto-rebase fails (true conflict), falls back to close-and-retry.
func TestController_handleMergeConflict_AutoRebaseFails(t *testing.T) {
	updateBranchCalled := false
	prClosed := false
	labelRemoved := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Pull Request is not mergeable",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == http.MethodPut:
			updateBranchCalled = true
			// Return 422 - true conflict, cannot auto-update
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "merge conflict between base and head",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			if r.Method == http.MethodPatch {
				prClosed = true
				w.WriteHeader(http.StatusOK)
				return
			}
			pr := github.PullRequest{
				Number:         42,
				State:          "open",
				Mergeable:      &mergeable,
				MergeableState: "dirty",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			labelRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging

	ctx := context.Background()
	err := c.ProcessPR(ctx, 42, nil)

	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !updateBranchCalled {
		t.Error("UpdatePullRequestBranch should have been called")
	}

	if !prClosed {
		t.Error("PR should have been closed after failed auto-rebase")
	}

	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}

	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// newTestLearningLoop creates a LearningLoop backed by a temp SQLite store for testing.
// The store is returned so the caller can close and clean it up.
func newTestLearningLoop(t *testing.T) (*memory.LearningLoop, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "controller-learn-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}
	// nil extractor: LearnFromReview will return an error (logged as warning, not propagated)
	loop := memory.NewLearningLoop(store, nil, nil)
	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return loop, cleanup
}

// TestHandleMerged_LearnsFromReviews verifies that handleMerged fetches PR reviews
// when a learning loop is configured.
func TestHandleMerged_LearnsFromReviews(t *testing.T) {
	reviewsFetched := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			reviewsFetched = true
			reviews := []github.PullRequestReview{
				{Body: "LGTM — nice implementation", State: "APPROVED", User: github.User{Login: "reviewer1"}},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, reviews))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		Stage:    StageMerged,
	}

	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}

	if !reviewsFetched {
		t.Error("expected /pulls/42/reviews to be fetched for learning")
	}
}

// TestHandleMerged_NoReviews verifies that handleMerged does not error when
// there are no reviews to learn from.
func TestHandleMerged_NoReviews(t *testing.T) {
	reviewsFetched := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			reviewsFetched = true
			// Return empty array — no reviews
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		Stage:    StageMerged,
	}

	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}

	if !reviewsFetched {
		t.Error("expected /pulls/42/reviews to be fetched even when empty")
	}
}

// TestHandleMerged_NilLearningLoop verifies that handleMerged does not panic
// when no learning loop is configured (nil guard).
func TestHandleMerged_NilLearningLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// learningLoop intentionally not set

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		Stage:    StageMerged,
	}

	// Must not panic
	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}
}

// mustJSON serialises v to JSON and fails the test on error.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return b
}

// TestController_SetBoardSync verifies SetBoardSync stores the board sync and done status.
func TestController_SetBoardSync(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// nil by default
	if c.boardSync != nil {
		t.Error("boardSync should be nil by default")
	}
	if c.doneStatus != "" {
		t.Error("doneStatus should be empty by default")
	}

	// SetBoardSync with nil is safe (board sync disabled)
	c.SetBoardSync(nil, "Done")
	if c.boardSync != nil {
		t.Error("boardSync should remain nil when passed nil")
	}
}

// TestController_SetPRIssueNodeID verifies IssueNodeID is stored on an active PR.
func TestController_SetPRIssueNodeID(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	// SetPRIssueNodeID on tracked PR
	c.SetPRIssueNodeID(42, "MDExOlB1bGxSZXF1ZXN0MTIz")
	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR not found")
	}
	if pr.IssueNodeID != "MDExOlB1bGxSZXF1ZXN0MTIz" {
		t.Errorf("IssueNodeID = %q, want %q", pr.IssueNodeID, "MDExOlB1bGxSZXF1ZXN0MTIz")
	}

	// No-op for unknown PR
	c.SetPRIssueNodeID(999, "some-id") // should not panic

	// No-op for empty string
	c.SetPRIssueNodeID(42, "")
	pr, _ = c.GetPRState(42)
	if pr.IssueNodeID != "MDExOlB1bGxSZXF1ZXN0MTIz" {
		t.Error("IssueNodeID should not be cleared by empty string")
	}
}
