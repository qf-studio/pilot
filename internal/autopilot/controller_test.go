package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/approval"
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

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123")

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

	err := c.ProcessPR(context.Background(), 99)
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI (dev now waits for CI)
	err := c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	// Stage 3: CI passed → merging
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageMerging)
	}

	// Stage 4: merging → merged
	err = c.ProcessPR(ctx, 42)
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
	err = c.ProcessPR(ctx, 42)
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	// Stage 3: CI passed → merging (no approval in stage)
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageMerging)
	}

	// Stage 4: merging → merged
	err = c.ProcessPR(ctx, 42)
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
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 5 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StagePostMergeCI {
		t.Errorf("after stage 5: Stage = %s, want %s", pr.Stage, StagePostMergeCI)
	}

	// Stage 6: post-merge CI → done (removed from tracking)
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 6 error: %v", err)
	}
	_, ok := c.GetPRState(42)
	if ok {
		t.Error("PR should be removed from tracking after post-merge CI")
	}
}

func TestController_ProcessPR_CIFailure(t *testing.T) {
	// Test CI failure creates fix issue
	issueCreated := false

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → CI failed
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageCIFailed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIFailed)
	}

	// Stage 3: CI failed → create fix issue → failed
	err = c.ProcessPR(ctx, 42)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	if !issueCreated {
		t.Error("fix issue should have been created")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageFailed)
	}
}

func TestController_CircuitBreaker(t *testing.T) {
	// Test circuit breaker trips after max failures
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
		_ = c.ProcessPR(ctx, 42)
	}

	if !c.IsCircuitOpen() {
		t.Error("circuit breaker should be open after max failures")
	}

	// Next call should be blocked
	err := c.ProcessPR(ctx, 42)
	if err == nil {
		t.Error("ProcessPR should fail when circuit breaker is open")
	}
}

func TestController_ResetCircuitBreaker(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set failures
	c.mu.Lock()
	c.consecutiveFailures = 5
	c.mu.Unlock()

	if !c.IsCircuitOpen() {
		t.Error("circuit should be open")
	}

	c.ResetCircuitBreaker()

	if c.IsCircuitOpen() {
		t.Error("circuit should be closed after reset")
	}
}

func TestController_MultiplePRs(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Add multiple PRs
	c.OnPRCreated(1, "url1", 10, "sha1")
	c.OnPRCreated(2, "url2", 20, "sha2")
	c.OnPRCreated(3, "url3", 30, "sha3")

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
	err := c.ProcessPR(context.Background(), 42)
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	_ = c.ProcessPR(ctx, 42)

	// Stage 2: waiting CI → CI passed
	_ = c.ProcessPR(ctx, 42)

	// Stage 3: CI passed → awaiting approval (prod)
	_ = c.ProcessPR(ctx, 42)

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("Stage = %s, want %s for prod environment", pr.Stage, StageAwaitApproval)
	}
}

func TestController_RemovePR(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "url", 10, "sha")

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
	// Successful processing should reset consecutive failures
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage // Use stage to have predictable behavior
	cfg.MaxFailures = 5

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set some failures
	c.mu.Lock()
	c.consecutiveFailures = 2
	c.mu.Unlock()

	c.OnPRCreated(42, "url", 10, "abc1234")

	// Successful processing (pr_created → waiting_ci)
	err := c.ProcessPR(context.Background(), 42)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	c.mu.RLock()
	failures := c.consecutiveFailures
	c.mu.RUnlock()

	if failures != 0 {
		t.Errorf("consecutiveFailures = %d, want 0 after successful processing", failures)
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
			// Fail first attempt, succeed second
			if mergeCallCount == 1 {
				w.WriteHeader(http.StatusInternalServerError)
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
	err := c.ProcessPR(ctx, 42)
	if err == nil {
		t.Error("first merge attempt should fail")
	}

	pr, _ := c.GetPRState(42)
	if pr.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1", pr.MergeAttempts)
	}

	// Second attempt succeeds
	err = c.ProcessPR(ctx, 42)
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123")

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
	// Test that API errors don't remove PRs but allow processing to continue
	ciCheckCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return error
			w.WriteHeader(http.StatusInternalServerError)
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

	// Process PRs - should fail to check state but continue processing
	c.processAllPRs(context.Background())

	// Verify PR is still tracked (error shouldn't remove it)
	if _, ok := c.GetPRState(42); !ok {
		t.Error("PR should still be tracked after API error")
	}

	// Verify normal processing continued despite check failure
	if !ciCheckCalled {
		t.Error("CI check should have been called even after state check failed")
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

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123")

	// Process PRs - should detect external merge and notify
	c.processAllPRs(context.Background())

	// Verify notifier was called
	if !notifyMergedCalled {
		t.Error("NotifyMerged should have been called for external merge")
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
	c.OnPRCreated(1, "url1", 10, "sha1")
	c.OnPRCreated(2, "url2", 20, "sha2")
	c.OnPRCreated(3, "url3", 30, "sha3")

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
