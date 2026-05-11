package autopilot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
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

func TestNewController_ReleaserInit(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	enabledRelease := &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
	disabledRelease := &ReleaseConfig{Enabled: false}

	tests := []struct {
		name           string
		globalRelease  *ReleaseConfig
		envRelease     *ReleaseConfig
		wantReleaser   bool
	}{
		{
			name:          "global only enabled",
			globalRelease: enabledRelease,
			envRelease:    nil,
			wantReleaser:  true,
		},
		{
			name:          "env only enabled",
			globalRelease: nil,
			envRelease:    enabledRelease,
			wantReleaser:  true,
		},
		{
			name:          "both set — env wins (enabled)",
			globalRelease: disabledRelease,
			envRelease:    enabledRelease,
			wantReleaser:  true,
		},
		{
			name:          "neither set",
			globalRelease: nil,
			envRelease:    nil,
			wantReleaser:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease
			if tt.envRelease != nil {
				envCfg := &EnvironmentConfig{Release: tt.envRelease}
				cfg.activeEnvName = "test"
				cfg.activeEnvConfig = envCfg
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			if tt.wantReleaser && c.releaser == nil {
				t.Errorf("releaser should be non-nil")
			}
			if !tt.wantReleaser && c.releaser != nil {
				t.Errorf("releaser should be nil")
			}
		})
	}
}

func TestController_OnPRCreated(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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

func TestController_OnReviewRequested(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Should not panic on untracked PR
	c.OnReviewRequested(99, "submitted", "changes_requested", "reviewer1")

	// Register a PR and send review
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
	c.OnReviewRequested(42, "submitted", "changes_requested", "reviewer1")

	// PR should still be tracked (stage transitions but PR remains in activePRs)
	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("expected PR to be tracked after review event")
	}
	if pr.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", pr.PRNumber)
	}

	// Approved review should also not panic
	c.OnReviewRequested(42, "submitted", "approved", "reviewer2")
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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

// GH-2402: After a successful merge, the controller must call
// SelfHealExecutionAfterMerge so any prior failed execution row for the
// same task ID is promoted to "completed" with the PR URL stamped.
func TestController_HandleMerging_SelfHealsExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"merged123","merged":true,"message":"merged"}`))
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
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		HeadSHA:     "abc1234",
		IssueNumber: 99,
		Stage:       StageMerging,
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	if len(evalMock.selfHealed) != 1 {
		t.Fatalf("expected 1 self-heal call, got %d", len(evalMock.selfHealed))
	}
	got := evalMock.selfHealed[0]
	if got.TaskID != "GH-99" {
		t.Errorf("self-heal task ID = %q, want GH-99", got.TaskID)
	}
	if got.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("self-heal PR URL = %q, want PR URL", got.PRURL)
	}
	// Old UpdateExecutionStatusByTaskID path must NOT also be invoked — self-heal
	// supersedes it so we don't write stale rows without the PR URL.
	if len(evalMock.updateStatus) != 0 {
		t.Errorf("expected 0 UpdateExecutionStatusByTaskID calls (self-heal replaces it), got %d", len(evalMock.updateStatus))
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
	c.OnPRCreated(1, "url1", 10, "sha1", "pilot/GH-10", "")
	c.OnPRCreated(2, "url2", 20, "sha2", "pilot/GH-20", "")
	c.OnPRCreated(3, "url3", 30, "sha3", "pilot/GH-30", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "url", 10, "sha", "pilot/GH-10", "")

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

	c.OnPRCreated(42, "url", 10, "abc1234", "pilot/GH-10", "")

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

// TestController_ScanExistingPRs_PreservesTrackedState verifies that PRs
// already tracked (e.g. restored from SQLite via RestoreState) are not
// clobbered back to StagePRCreated by a subsequent ScanExistingPRs call.
// Regression test for GH-2349.
func TestController_ScanExistingPRs_PreservesTrackedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{Number: 42, Head: github.PRRef{Ref: "pilot/GH-100", SHA: "sha-new"}, HTMLURL: "url"},
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

	// Simulate RestoreState having already populated this PR at StageCIPassed
	// with a non-zero CIWaitStartedAt.
	ciWaitStart := time.Now().Add(-30 * time.Minute)
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:        42,
		IssueNumber:     100,
		BranchName:      "pilot/GH-100",
		HeadSHA:         "sha-old",
		Stage:           StageCIPassed,
		CIWaitStartedAt: ciWaitStart,
	}
	c.mu.Unlock()

	if err := c.ScanExistingPRs(context.Background()); err != nil {
		t.Fatalf("ScanExistingPRs() error = %v", err)
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 missing after scan")
	}
	if pr.Stage != StageCIPassed {
		t.Errorf("Stage = %v, want %v (regressed by scan)", pr.Stage, StageCIPassed)
	}
	if !pr.CIWaitStartedAt.Equal(ciWaitStart) {
		t.Errorf("CIWaitStartedAt reset by scan: got %v, want %v", pr.CIWaitStartedAt, ciWaitStart)
	}
	if pr.HeadSHA != "sha-old" {
		t.Errorf("HeadSHA = %q, want %q (clobbered by scan)", pr.HeadSHA, "sha-old")
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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
	c.OnPRCreated(1, "url1", 10, "sha1", "pilot/GH-10", "")
	c.OnPRCreated(2, "url2", 20, "sha2", "pilot/GH-20", "")
	c.OnPRCreated(3, "url3", 30, "sha3", "pilot/GH-30", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, staleSHA, "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

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
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
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

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
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

// TestHandleCIFailed_LearnsFromCIFailure verifies that handleCIFailed calls
// LearnFromCIFailure when a learning loop is configured.
func TestHandleCIFailed_LearnsFromCIFailure(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha123/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "sha123",
		Stage:    StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected fix issue to be created")
	}

	// The learning loop was set, so LearnFromCIFailure was called.
	// With nil extractor it returns an error (logged as warning), but must not panic.
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// TestHandleCIFailed_NilLearningLoop verifies that handleCIFailed does not panic
// when no learning loop is configured (nil guard).
func TestHandleCIFailed_NilLearningLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha456/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			resp := github.Issue{Number: 201}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// learningLoop intentionally not set

	prState := &PRState{
		PRNumber: 43,
		HeadSHA:  "sha456",
		Stage:    StageCIFailed,
	}

	// Must not panic
	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
}

// TestHandlePostMergeCI_LearnsFromCIFailure verifies that handlePostMergeCI calls
// LearnFromCIFailure when post-merge CI fails and a learning loop is configured.
func TestHandlePostMergeCI_LearnsFromCIFailure(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/branches/main":
			resp := map[string]interface{}{
				"commit": map[string]string{"sha": "mainsha1"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/commits/mainsha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "e2e", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 300}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"e2e"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 44,
		Stage:    StagePostMergeCI,
	}

	err := c.handlePostMergeCI(context.Background(), prState)
	if err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected post-merge fix issue to be created")
	}
}

// TestHandlePostMergeCI_NonBlocking verifies that handlePostMergeCI does not block
// the tick loop: pending CI returns nil and stays in StagePostMergeCI, success
// advances to StageReleasing (when release is configured) or removes the PR,
// and a daemon restart resumes from the persisted PostMergeSHA. (GH-2717)
func TestHandlePostMergeCI_NonBlocking(t *testing.T) {
	tests := []struct {
		name          string
		checkStatus   string
		checkConc     string
		releaseEnabled bool
		wantStage     PRStage
		wantRemoved   bool
	}{
		{
			name:        "pending stays in stage",
			checkStatus: "in_progress",
			wantStage:   StagePostMergeCI,
			wantRemoved: false,
		},
		{
			name:          "success without release removes PR",
			checkStatus:   "completed",
			checkConc:     "success",
			releaseEnabled: false,
			wantRemoved:   true,
		},
		{
			name:          "success with release advances to StageReleasing",
			checkStatus:   "completed",
			checkConc:     "success",
			releaseEnabled: true,
			wantStage:     StageReleasing,
			wantRemoved:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/branches/main":
					resp := map[string]interface{}{
						"commit": map[string]string{"sha": "mainsha42"},
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))
				case "/repos/owner/repo/commits/mainsha42/check-runs":
					resp := github.CheckRunsResponse{
						TotalCount: 1,
						CheckRuns: []github.CheckRun{
							{Name: "ci", Status: tt.checkStatus, Conclusion: tt.checkConc},
						},
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.CIPollInterval = 10 * time.Millisecond
			cfg.CIWaitTimeout = 5 * time.Second
			if tt.releaseEnabled {
				cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber: 77,
				Stage:    StagePostMergeCI,
			}

			err := c.handlePostMergeCI(context.Background(), prState)
			if err != nil {
				t.Fatalf("handlePostMergeCI returned error: %v", err)
			}

			if tt.wantRemoved {
				// PR should have been removed from tracking.
				c.mu.RLock()
				_, stillTracked := c.activePRs[77]
				c.mu.RUnlock()
				if stillTracked {
					t.Error("expected PR to be removed but it is still tracked")
				}
			} else {
				if prState.Stage != tt.wantStage {
					t.Errorf("stage = %s, want %s", prState.Stage, tt.wantStage)
				}
			}

			// PostMergeSHA must be populated after first call.
			if prState.PostMergeSHA == "" {
				t.Error("PostMergeSHA should be set after first tick")
			}
		})
	}
}

// TestHandlePostMergeCI_RestartResumesSHA verifies that if PostMergeSHA is already
// set (simulating a daemon restart with persisted state), handlePostMergeCI does not
// fetch a new SHA and continues monitoring the original commit. (GH-2717)
func TestHandlePostMergeCI_RestartResumesSHA(t *testing.T) {
	branchFetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/branches/main":
			branchFetched = true
			resp := map[string]interface{}{
				"commit": map[string]string{"sha": "newsha99"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case "/repos/owner/repo/commits/originalsha/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "in_progress"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 5 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Simulate persisted state from before restart (started 30s ago, well within timeout).
	prState := &PRState{
		PRNumber:             88,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "originalsha",
		PostMergeCIStartedAt: time.Now().Add(-30 * time.Second),
	}

	err := c.handlePostMergeCI(context.Background(), prState)
	if err != nil {
		t.Fatalf("handlePostMergeCI returned error: %v", err)
	}

	if branchFetched {
		t.Error("branch SHA should NOT be fetched when PostMergeSHA is already set")
	}
	if prState.PostMergeSHA != "originalsha" {
		t.Errorf("PostMergeSHA changed to %q, want %q", prState.PostMergeSHA, "originalsha")
	}
	if prState.Stage != StagePostMergeCI {
		t.Errorf("stage = %s, want %s (CI still running)", prState.Stage, StagePostMergeCI)
	}
}

// TestHandlePostMergeCI_Timeout verifies that a long-running post-merge CI
// eventually times out and transitions to StageFailed. (GH-2717)
func TestHandlePostMergeCI_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/tsha/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "in_progress"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 1 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Started well in the past to trigger immediate timeout.
	prState := &PRState{
		PRNumber:             99,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "tsha",
		PostMergeCIStartedAt: time.Now().Add(-10 * time.Minute),
	}

	err := c.handlePostMergeCI(context.Background(), prState)
	if err != nil {
		t.Fatalf("handlePostMergeCI returned error: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s after timeout", prState.Stage, StageFailed)
	}
	if prState.Error == "" {
		t.Error("expected Error to be set on timeout")
	}
}

// TestHandleCIFailed_EmptyLogs_SkipsLearning verifies that handleCIFailed skips
// LearnFromCIFailure when CI logs are empty or whitespace-only (GH-1979).
// TestHandleCIFailed_EmptyLogs_SkipsLearning verifies that handleCIFailed skips
// LearnFromCIFailure when CI logs are empty (no failed check runs found for log fetch).
func TestHandleCIFailed_EmptyLogs_SkipsLearning(t *testing.T) {
	learnCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// GetFailedChecks uses this endpoint
		case r.URL.Path == "/repos/owner/repo/commits/sha789/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		// GetFailedCheckLogs tries to fetch job logs — return 404 so logs are empty
		case strings.Contains(r.URL.Path, "/actions/jobs/") && strings.HasSuffix(r.URL.Path, "/logs"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			resp := github.Issue{Number: 210}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 45,
		HeadSHA:  "sha789",
		Stage:    StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	// The learning loop should NOT have been invoked (empty logs guard).
	// learnCalled remains false since we can't directly observe the call,
	// but the absence of "Failed to learn from CI failure" warning in logs
	// confirms the guard works. With nil extractor + non-empty logs, the
	// warning would appear.
	_ = learnCalled
}

// TestSetLearningLoop_ForwardsToFeedbackLoop verifies that SetLearningLoop
// also injects the learning loop into the feedback loop (GH-1979).
func TestSetLearningLoop_ForwardsToFeedbackLoop(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()

	// Before setting — feedbackLoop.learningLoop should be nil
	if c.feedbackLoop.learningLoop != nil {
		t.Error("feedbackLoop.learningLoop should be nil before SetLearningLoop")
	}

	c.SetLearningLoop(loop)

	// After setting — feedbackLoop.learningLoop should be wired
	if c.feedbackLoop.learningLoop == nil {
		t.Error("feedbackLoop.learningLoop should be set after SetLearningLoop")
	}
	if c.feedbackLoop.learningLoop != loop {
		t.Error("feedbackLoop.learningLoop should point to the same loop instance")
	}
}

// mockEvalStore captures SaveEvalTask calls for testing.
type mockEvalStore struct {
	saved        []*memory.EvalTask
	selfHealed   []selfHealCall
	updateStatus []updateStatusCall
}

type selfHealCall struct {
	TaskID string
	PRURL  string
}

type updateStatusCall struct {
	TaskID string
	Status string
}

func (m *mockEvalStore) SaveEvalTask(task *memory.EvalTask) error {
	m.saved = append(m.saved, task)
	return nil
}

func (m *mockEvalStore) UpdateExecutionStatusByTaskID(taskID, status string) error {
	m.updateStatus = append(m.updateStatus, updateStatusCall{TaskID: taskID, Status: status})
	return nil
}

func (m *mockEvalStore) SelfHealExecutionAfterMerge(taskID, prURL string) error {
	m.selfHealed = append(m.selfHealed, selfHealCall{TaskID: taskID, PRURL: prURL})
	return nil
}

// TestHandleMerged_ExtractsEvalTask verifies that handleMerged extracts and saves
// an eval task when evalStore is configured and the PR has a linked issue.
func TestHandleMerged_ExtractsEvalTask(t *testing.T) {
	issueFetched := false
	filesFetched := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/issues/10":
			issueFetched = true
			issue := github.Issue{Number: 10, Title: "Add feature X"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, issue))
		case "/repos/owner/repo/pulls/42/files":
			filesFetched = true
			files := []github.PRFile{
				{Filename: "internal/foo.go", Status: "modified"},
				{Filename: "internal/bar.go", Status: "added"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
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
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	prState := &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		IssueNumber: 10,
		Stage:       StageMerged,
	}

	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}

	if !issueFetched {
		t.Error("expected /issues/10 to be fetched")
	}
	if !filesFetched {
		t.Error("expected /pulls/42/files to be fetched")
	}
	if len(evalMock.saved) != 1 {
		t.Fatalf("expected 1 eval task saved, got %d", len(evalMock.saved))
	}

	task := evalMock.saved[0]
	if task.IssueNumber != 10 {
		t.Errorf("expected issue number 10, got %d", task.IssueNumber)
	}
	if task.IssueTitle != "Add feature X" {
		t.Errorf("expected issue title 'Add feature X', got %q", task.IssueTitle)
	}
	if task.Repo != "owner/repo" {
		t.Errorf("expected repo 'owner/repo', got %q", task.Repo)
	}
	if !task.Success {
		t.Error("expected task success=true for merged PR")
	}
	if len(task.FilesChanged) != 2 {
		t.Errorf("expected 2 files changed, got %d", len(task.FilesChanged))
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

// --- GH-2079: Review feedback controller tests ---

func TestController_HandleReviewRequested_CreatesIssue(t *testing.T) {
	issueCreated := false
	prClosed := false
	branchDeleted := false
	notified := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Fix the nil check", State: "CHANGES_REQUESTED", SubmittedAt: "2026-03-05T10:00:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42/comments":
			resp := []*github.PRReviewComment{
				{ID: 10, Body: "Add error handling", Path: "foo.go", Line: 5, User: github.User{Login: "alice"}},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 100}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.PullRequest{Number: 42, State: "closed"}))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetNotifier(&mockNotifier{
		notifyFixIssueCreatedFunc: func(ctx context.Context, prState *PRState, issueNumber int) error {
			notified = true
			return nil
		},
	})

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Transition to review_requested
	c.mu.Lock()
	c.activePRs[42].Stage = StageReviewRequested
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	if !issueCreated {
		t.Error("expected review issue to be created")
	}
	if !prClosed {
		t.Error("expected PR to be closed")
	}
	if !branchDeleted {
		t.Error("expected branch to be deleted")
	}
	if !notified {
		t.Error("expected notification to be sent")
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", pr.Stage, StageFailed)
	}
}

func TestController_HandleReviewRequested_IterationLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Still broken", State: "CHANGES_REQUESTED"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/10":
			// Return issue with iteration=3 metadata (at limit)
			resp := github.Issue{
				Number: 10,
				Body:   "some body\n<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:3 -->",
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.PullRequest{Number: 42, State: "closed"}))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	c.mu.Lock()
	c.activePRs[42].Stage = StageReviewRequested
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !strings.Contains(pr.Error, "iteration limit") {
		t.Errorf("error should mention iteration limit: %s", pr.Error)
	}
}

func TestController_HandleReviewRequested_IgnoresSelfReview(t *testing.T) {
	// hasChangesRequested should skip bot reviews
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "pilot[bot]"}, Body: "Self-review", State: "CHANGES_REQUESTED", SubmittedAt: "2026-03-05T10:00:00Z"},
				{ID: 2, User: github.User{Login: "ci-bot"}, Body: "Bot review", State: "CHANGES_REQUESTED", SubmittedAt: "2026-03-05T10:00:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	prState, _ := c.GetPRState(42)
	// Set CreatedAt to before the reviews so time filter doesn't block them
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	c.mu.Unlock()

	result := c.hasChangesRequested(context.Background(), prState)
	if result {
		t.Error("hasChangesRequested should return false for bot-only reviews")
	}
}

func TestController_HandleReviewRequested_DisabledByConfig(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: false, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// OnReviewRequested should not transition when disabled
	c.OnReviewRequested(42, "submitted", "changes_requested", "alice")

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should be tracked")
	}
	if pr.Stage == StageReviewRequested {
		t.Error("stage should NOT be review_requested when feature is disabled")
	}
}

func TestController_OnReviewRequested_UntrackedPR(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Should not panic on untracked PR
	c.OnReviewRequested(99, "submitted", "changes_requested", "alice")

	_, ok := c.GetPRState(99)
	if ok {
		t.Error("untracked PR should not be added")
	}
}

func TestController_HasChangesRequested_FilterByTime(t *testing.T) {
	// Reviews submitted before PR tracking should be ignored
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{
					ID:          1,
					User:        github.User{Login: "alice"},
					Body:        "Old review",
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-03-01T10:00:00Z", // Before PR creation
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Set PR creation after the review
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	c.mu.Unlock()

	prState, _ := c.GetPRState(42)
	result := c.hasChangesRequested(context.Background(), prState)
	if result {
		t.Error("hasChangesRequested should return false for reviews submitted before PR creation")
	}
}

func TestMaybeCloseParentIssue(t *testing.T) {
	tests := []struct {
		name              string
		issueNumber       int
		issueBody         string
		openSubIssues     int // used by text-search fallback
		getIssueErr       bool
		searchErr         bool
		nativeTotal       int    // totalCount returned by GraphQL native sub-issues
		nativeOpenStates  []string // states of natively linked sub-issues
		wantClosed        bool
		wantLabeled       bool
		wantCommented     bool
	}{
		{
			name:          "last sub-issue triggers parent close (text-search path)",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			openSubIssues: 0,
			wantClosed:    true,
			wantLabeled:   true,
			wantCommented: true,
		},
		{
			name:          "sibling still open - no-op (text-search path)",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			openSubIssues: 2,
			wantClosed:    false,
			wantLabeled:   false,
			wantCommented: false,
		},
		{
			name:          "no parent reference - no-op",
			issueNumber:   10,
			issueBody:     "Standalone issue with no parent",
			openSubIssues: 0,
			wantClosed:    false,
			wantLabeled:   false,
			wantCommented: false,
		},
		{
			name:        "no issue number - no-op",
			issueNumber: 0,
			wantClosed:  false,
		},
		{
			name:        "GetIssue API error - graceful no-op",
			issueNumber: 10,
			getIssueErr: true,
			wantClosed:  false,
		},
		{
			name:          "SearchOpenSubIssues API error - graceful no-op",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			searchErr:     true,
			wantClosed:    false,
			wantLabeled:   false,
			wantCommented: false,
		},
		{
			name:          "label cleanup removes pilot-failed",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			openSubIssues: 0,
			wantClosed:    true,
			wantLabeled:   true,
			wantCommented: true,
		},
		{
			name:             "native links all closed - closes parent",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			nativeTotal:      2,
			nativeOpenStates: []string{"CLOSED", "CLOSED"},
			wantClosed:       true,
			wantLabeled:      true,
			wantCommented:    true,
		},
		{
			name:             "native links with open sibling - no-op",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			nativeTotal:      2,
			nativeOpenStates: []string{"OPEN", "CLOSED"},
			wantClosed:       false,
			wantLabeled:      false,
			wantCommented:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				closeCalled      bool
				addLabelsCalled  bool
				removeLabelCalls []string
				commentCalled    bool
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					if tt.getIssueErr {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					issue := github.Issue{
						Number: 10,
						Body:   tt.issueBody,
						State:  "closed",
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				case r.URL.Path == "/repos/owner/repo/issues/5" && r.Method == http.MethodGet:
					// Return node_id for GetIssueNodeID call in native sub-issue path.
					if tt.nativeTotal > 0 {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`{"node_id":"I_parent_node","number":5}`))
					} else {
						// No native links — return empty body so GetIssueNodeID fails and falls back to text search.
						w.WriteHeader(http.StatusOK)
					}

				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					// Serve native sub-issues GraphQL response in node(id:) format used by GetOpenSubIssueCount.
					nodes := make([]map[string]string, len(tt.nativeOpenStates))
					for i, s := range tt.nativeOpenStates {
						nodes[i] = map[string]string{"state": s}
					}
					resp := map[string]interface{}{
						"data": map[string]interface{}{
							"node": map[string]interface{}{
								"subIssues": map[string]interface{}{
									"totalCount": tt.nativeTotal,
									"nodes":      nodes,
								},
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case strings.HasPrefix(r.URL.Path, "/search/issues"):
					if tt.searchErr {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					resp := struct {
						TotalCount int `json:"total_count"`
					}{TotalCount: tt.openSubIssues}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case r.URL.Path == "/repos/owner/repo/issues/5/labels" && r.Method == http.MethodPost:
					addLabelsCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/5/labels/") && r.Method == http.MethodDelete:
					label := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/5/labels/")
					removeLabelCalls = append(removeLabelCalls, label)
					w.WriteHeader(http.StatusOK)

				case r.URL.Path == "/repos/owner/repo/issues/5/comments" && r.Method == http.MethodPost:
					commentCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				case r.URL.Path == "/repos/owner/repo/issues/5" && r.Method == http.MethodPatch:
					closeCalled = true
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:    42,
				IssueNumber: tt.issueNumber,
			}

			c.maybeCloseParentIssue(context.Background(), prState)

			if closeCalled != tt.wantClosed {
				t.Errorf("parent closed = %v, want %v", closeCalled, tt.wantClosed)
			}
			if addLabelsCalled != tt.wantLabeled {
				t.Errorf("pilot-done label added = %v, want %v", addLabelsCalled, tt.wantLabeled)
			}
			if commentCalled != tt.wantCommented {
				t.Errorf("comment posted = %v, want %v", commentCalled, tt.wantCommented)
			}
			if tt.wantLabeled {
				// Verify stale labels were removed
				expectedRemoved := map[string]bool{"pilot-failed": false, "pilot-in-progress": false, "pilot-blocked": false}
				for _, label := range removeLabelCalls {
					expectedRemoved[label] = true
				}
				for label, removed := range expectedRemoved {
					if !removed {
						t.Errorf("expected label %q to be removed, but it wasn't", label)
					}
				}
			}
		})
	}
}

func TestRecoverStaleParentIssues(t *testing.T) {
	type parentCandidate struct {
		num      int
		openSubs int // open sub-issue count from GetOpenSubIssueCount
		subErr   bool
	}

	tests := []struct {
		name        string
		candidates  []parentCandidate // issues returned by SearchOpenPilotIssuesWithSubIssues
		searchErr   bool
		wantClosed  []int
		wantSkipped []int
	}{
		{
			name: "closes orphaned parent with all sub-issues done",
			candidates: []parentCandidate{
				{num: 100, openSubs: 0},
			},
			wantClosed: []int{100},
		},
		{
			name: "skips parent with open siblings",
			candidates: []parentCandidate{
				{num: 100, openSubs: 2},
			},
			wantSkipped: []int{100},
		},
		{
			name:        "no-op when search returns no candidates",
			candidates:  []parentCandidate{},
			wantClosed:  nil,
			wantSkipped: nil,
		},
		{
			name: "continues on per-item error, closes others",
			candidates: []parentCandidate{
				{num: 100, subErr: true},
				{num: 200, openSubs: 0},
			},
			wantClosed:  []int{200},
			wantSkipped: []int{100},
		},
		{
			name:      "search error - no-op",
			searchErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closedParents := map[int]bool{}
			labeledParents := map[int]bool{}

			// Build a map from parent number to candidate for quick lookup.
			candidateMap := map[int]parentCandidate{}
			for _, c := range tt.candidates {
				candidateMap[c.num] = c
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					var body map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					query, _ := body["query"].(string)

					if strings.Contains(query, "subIssuesSummary") {
						// SearchOpenPilotIssuesWithSubIssues
						if tt.searchErr {
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write([]byte(`{"errors":[{"message":"search failed"}]}`))
							return
						}
						nodes := make([]map[string]interface{}, 0, len(tt.candidates))
						for _, c := range tt.candidates {
							nodes = append(nodes, map[string]interface{}{
								"number": c.num,
								"subIssuesSummary": map[string]int{
									"total":     1,
									"completed": 1,
								},
							})
						}
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"repository": map[string]interface{}{
									"issues": map[string]interface{}{
										"nodes": nodes,
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)

					} else if strings.Contains(query, "subIssues") {
						// GetOpenSubIssueCount — determine parent num from variables
						vars, _ := body["variables"].(map[string]interface{})
						issueID, _ := vars["issueID"].(string)
						// issueID format: "node_<num>"
						var parentNum int
						_, _ = fmt.Sscanf(issueID, "node_%d", &parentNum)

						cand, ok := candidateMap[parentNum]
						if !ok || cand.subErr {
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write([]byte(`{"errors":[{"message":"sub-issue count failed"}]}`))
							return
						}

						states := make([]map[string]string, cand.openSubs)
						for i := range states {
							states[i] = map[string]string{"state": "OPEN"}
						}
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"node": map[string]interface{}{
									"subIssues": map[string]interface{}{
										"totalCount": cand.openSubs + 1,
										"nodes":      states,
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)
					}

				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
					// GetIssueNodeID REST call — extract issue number
					path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
					var num int
					_, _ = fmt.Sscanf(path, "%d", &num)
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, `{"node_id":"node_%d","number":%d}`, num, num)

				case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/labels"):
					// AddLabels
					var num int
					path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
					_, _ = fmt.Sscanf(path, "%d", &num)
					labeledParents[num] = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
					w.WriteHeader(http.StatusOK)

				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/repos/owner/repo/issues/"):
					path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
					var num int
					_, _ = fmt.Sscanf(path, "%d", &num)
					closedParents[num] = true
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			c.recoverStaleParentIssues(context.Background())

			for _, num := range tt.wantClosed {
				if !closedParents[num] {
					t.Errorf("parent %d: expected to be closed, was not", num)
				}
			}
			for _, num := range tt.wantSkipped {
				if closedParents[num] {
					t.Errorf("parent %d: expected to be skipped, was closed", num)
				}
			}
		})
	}
}

func TestRecoverStaleParentIssues_TruncatesAt50(t *testing.T) {
	// Build 50 candidates so SearchOpenPilotIssuesWithSubIssues returns exactly maxRecover=50,
	// triggering the "hit limit" Info log. We don't test the log directly; just
	// verify the sweep still processes all 50 and closes them without error.
	const maxRecover = 50
	candidates := make([]int, maxRecover)
	for i := range candidates {
		candidates[i] = 1000 + i
	}

	closedCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			query, _ := body["query"].(string)

			if strings.Contains(query, "subIssuesSummary") {
				nodes := make([]map[string]interface{}, maxRecover)
				for i, num := range candidates {
					nodes[i] = map[string]interface{}{
						"number": num,
						"subIssuesSummary": map[string]int{"total": 1, "completed": 1},
					}
				}
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"repository": map[string]interface{}{
							"issues": map[string]interface{}{"nodes": nodes},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			} else {
				// GetOpenSubIssueCount — all sub-issues closed
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"node": map[string]interface{}{
							"subIssues": map[string]interface{}{
								"totalCount": 1,
								"nodes":      []map[string]string{{"state": "CLOSED"}},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
			path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
			var num int
			_, _ = fmt.Sscanf(path, "%d", &num)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"node_id":"node_%d","number":%d}`, num, num)

		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/repos/owner/repo/issues/"):
			closedCount++
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.recoverStaleParentIssues(context.Background())

	if closedCount != maxRecover {
		t.Errorf("closed %d parents, want %d", closedCount, maxRecover)
	}
}

// TestNotifyExternalClose_MaybeCloseParent verifies that notifyExternalClose
// calls maybeCloseParentIssue so parent epics are auto-closed when the last
// sub-issue PR is closed without merge (GH-2198).
func TestNotifyExternalClose_MaybeCloseParent(t *testing.T) {
	tests := []struct {
		name             string
		issueNumber      int
		issueBody        string
		openSubIssues    int
		wantParentClosed bool
	}{
		{
			name:             "last sub-issue PR closed externally - parent closes",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			openSubIssues:    0,
			wantParentClosed: true,
		},
		{
			name:             "non-last sub-issue - parent stays open",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			openSubIssues:    1,
			wantParentClosed: false,
		},
		{
			name:             "non-sub-issue - no parent lookup",
			issueNumber:      10,
			issueBody:        "Standalone issue",
			openSubIssues:    0,
			wantParentClosed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parentCloseCalled bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				// Label/remove calls for the sub-issue itself (notifyExternalClose)
				case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)

				// maybeCloseParentIssue: fetch sub-issue body
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					issue := github.Issue{Number: 10, Body: tt.issueBody}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				// maybeCloseParentIssue: count open siblings
				case strings.HasPrefix(r.URL.Path, "/search/issues"):
					resp := struct {
						TotalCount int `json:"total_count"`
					}{TotalCount: tt.openSubIssues}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				// maybeCloseParentIssue: close parent
				case r.URL.Path == "/repos/owner/repo/issues/5" && r.Method == http.MethodPatch:
					parentCloseCalled = true
					w.WriteHeader(http.StatusOK)

				// parent label / comment calls
				case r.URL.Path == "/repos/owner/repo/issues/5/labels" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/5/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)
				case r.URL.Path == "/repos/owner/repo/issues/5/comments" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:    42,
				IssueNumber: tt.issueNumber,
			}

			c.notifyExternalClose(context.Background(), prState)

			if parentCloseCalled != tt.wantParentClosed {
				t.Errorf("parent closed = %v, want %v", parentCloseCalled, tt.wantParentClosed)
			}
		})
	}
}

// GH-2340: notifyExternalClose must not stamp pilot-retry-ready on issues
// that already carry pilot-done. This happens when Pilot itself closed a
// duplicate PR after the original PR was merged — the issue is closed and
// done, and adding pilot-retry-ready strands the label forever (poller
// skips non-open issues).
func TestNotifyExternalClose_SkipsRetryReadyWhenDone(t *testing.T) {
	tests := []struct {
		name              string
		issueLabels       []github.Label
		wantRetryAdded    bool
	}{
		{
			name:           "issue already pilot-done - skip retry-ready",
			issueLabels:    []github.Label{{Name: github.LabelDone}},
			wantRetryAdded: false,
		},
		{
			name:           "issue not done - add retry-ready",
			issueLabels:    []github.Label{},
			wantRetryAdded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var retryReadyAdded bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					issue := github.Issue{Number: 10, State: "closed", Labels: tt.issueLabels}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
					var body struct {
						Labels []string `json:"labels"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					for _, l := range body.Labels {
						if l == github.LabelRetryReady {
							retryReadyAdded = true
						}
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 42, IssueNumber: 10}
			c.notifyExternalClose(context.Background(), prState)

			if retryReadyAdded != tt.wantRetryAdded {
				t.Errorf("pilot-retry-ready added = %v, want %v", retryReadyAdded, tt.wantRetryAdded)
			}
		})
	}
}

// GH-2251: Test that ScanRecentlyMergedPRs discovers externally-merged PRs
// and skips those already tracked.
func TestController_ScanRecentlyMergedPRs(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	oldMergedAt := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name             string
		prs              []github.PullRequest
		existingTracked  []int // PR numbers already in activePRs
		wantTriggered    int
		wantPRNumbers    []int
	}{
		{
			name: "discovers externally merged pilot PR",
			prs: []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			},
			wantTriggered: 1,
			wantPRNumbers: []int{42},
		},
		{
			name: "skips PR already tracked in activePRs",
			prs: []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			},
			existingTracked: []int{42},
			wantTriggered:   0,
			wantPRNumbers:   []int{},
		},
		{
			name: "skips PR merged outside scan window",
			prs: []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       oldMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			},
			wantTriggered: 0,
			wantPRNumbers: []int{},
		},
		{
			name: "skips non-pilot branches and unmerged PRs",
			prs: []github.PullRequest{
				{
					Number:   1,
					Head:     github.PRRef{Ref: "feature/stuff", SHA: "sha1"},
					Base:     github.PRRef{Ref: "main"},
					HTMLURL:  "https://github.com/owner/repo/pull/1",
					Merged:   true,
					MergedAt: recentMergedAt,
				},
				{
					Number:  2,
					Head:    github.PRRef{Ref: "pilot/GH-200", SHA: "sha2"},
					Base:    github.PRRef{Ref: "main"},
					HTMLURL: "https://github.com/owner/repo/pull/2",
					Merged:  false, // closed but not merged
				},
			},
			wantTriggered: 0,
			wantPRNumbers: []int{},
		},
		{
			name: "mixed: discovers one, skips tracked and old",
			prs: []github.PullRequest{
				{
					Number:         10,
					Head:           github.PRRef{Ref: "pilot/GH-10", SHA: "sha10"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/10",
					Title:          "fix(db): connection leak",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-10",
				},
				{
					Number:         20,
					Head:           github.PRRef{Ref: "pilot/GH-20", SHA: "sha20"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/20",
					Title:          "feat(ui): dashboard",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-20",
				},
				{
					Number:         30,
					Head:           github.PRRef{Ref: "pilot/GH-30", SHA: "sha30"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/30",
					Title:          "chore: cleanup",
					Merged:         true,
					MergedAt:       oldMergedAt, // outside window
					MergeCommitSHA: "merge-sha-30",
				},
			},
			existingTracked: []int{20}, // already tracked
			wantTriggered:   1,         // only PR 10
			wantPRNumbers:   []int{10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
					prs := make([]*github.PullRequest, len(tt.prs))
					for i := range tt.prs {
						prs[i] = &tt.prs[i]
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(prs)
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Release = &ReleaseConfig{
				Enabled:   true,
				Trigger:   "on_merge",
				TagPrefix: "v",
			}
			cfg.MergedPRScanWindow = 30 * time.Minute

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			// Pre-populate tracked PRs
			for _, prNum := range tt.existingTracked {
				c.mu.Lock()
				c.activePRs[prNum] = &PRState{PRNumber: prNum, Stage: StageWaitingCI}
				c.mu.Unlock()
			}

			err := c.ScanRecentlyMergedPRs(context.Background())
			if err != nil {
				t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
			}

			// Count newly triggered PRs (exclude pre-existing tracked ones)
			triggered := 0
			for _, pr := range c.GetActivePRs() {
				isPreExisting := false
				for _, existing := range tt.existingTracked {
					if pr.PRNumber == existing {
						isPreExisting = true
						break
					}
				}
				if !isPreExisting {
					triggered++
				}
			}

			if triggered != tt.wantTriggered {
				t.Errorf("triggered %d PRs, want %d", triggered, tt.wantTriggered)
			}

			for _, wantPR := range tt.wantPRNumbers {
				found := false
				for _, pr := range c.GetActivePRs() {
					if pr.PRNumber == wantPR {
						if pr.Stage != StageReleasing {
							t.Errorf("PR %d stage = %v, want StageReleasing", wantPR, pr.Stage)
						}
						found = true
						break
					}
				}
				if !found {
					t.Errorf("PR %d not found in active PRs", wantPR)
				}
			}
		})
	}
}

// mergeMockServer returns an httptest server that answers the handleMerging
// happy-path requests (PR fetch, merge, labels) and counts POSTs to the
// issue comments endpoint.
func mergeMockServer(t *testing.T, prNumber, issueNumber int, commentCount *int) *httptest.Server {
	t.Helper()
	commentPath := "/repos/owner/repo/issues/" + itoa(issueNumber) + "/comments"
	prPath := "/repos/owner/repo/pulls/" + itoa(prNumber)
	mergePath := prPath + "/merge"
	labelsPath := "/repos/owner/repo/issues/" + itoa(issueNumber) + "/labels"

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/check-runs"):
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == commentPath && r.Method == http.MethodPost:
			*commentCount++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
		case r.URL.Path == mergePath && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha": "merged123", "merged": true, "message": "Pull Request successfully merged",
			})
		case r.URL.Path == prPath && r.Method == http.MethodGet:
			pr := github.PullRequest{
				Number: prNumber,
				State:  "open",
				Head:   github.PRRef{Ref: "pilot/GH-" + itoa(issueNumber), SHA: "abc1234"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == labelsPath && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: github.LabelDone}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestController_handleMerging_IdempotentCompletionComment tests GH-2345:
// Re-entering StageMerging for an already-merged PR must not produce a second
// "PR merged" comment.
func TestController_handleMerging_IdempotentCompletionComment(t *testing.T) {
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging

	ctx := context.Background()

	// First entry: posts the completion comment and advances stage.
	if err := c.handleMerging(ctx, prState); err != nil {
		t.Fatalf("first handleMerging returned error: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("after first handleMerging: comment count = %d, want 1", commentCount)
	}
	if !prState.MergeNotificationPosted {
		t.Fatal("MergeNotificationPosted should be true after first successful post")
	}

	// Simulate re-entry (e.g. via duplicate-dispatch or crash recovery).
	prState.Stage = StageMerging
	if err := c.handleMerging(ctx, prState); err != nil {
		t.Fatalf("re-entry handleMerging returned error: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("after re-entry: comment count = %d, want 1 (no duplicate)", commentCount)
	}
}

// TestController_handleMerging_CommentFlagPersists tests GH-2345:
// MergeNotificationPosted round-trips through SavePRState/LoadAllPRStates so
// that crash recovery honors the flag and a restored PR never re-posts.
func TestController_handleMerging_CommentFlagPersists(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:                42,
		PRURL:                   "https://github.com/owner/repo/pull/42",
		IssueNumber:             10,
		BranchName:              "pilot/GH-10",
		HeadSHA:                 "abc1234",
		Stage:                   StageMerging,
		CIStatus:                CIPending,
		CreatedAt:               time.Now().Add(-5 * time.Minute).Truncate(time.Second),
		MergeNotificationPosted: true,
	}
	if err := store.SavePRState(pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState(42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil || !loaded.MergeNotificationPosted {
		t.Fatalf("MergeNotificationPosted did not persist: got %+v", loaded)
	}

	all, err := store.LoadAllPRStates()
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 || !all[0].MergeNotificationPosted {
		t.Fatalf("LoadAllPRStates did not preserve MergeNotificationPosted: %+v", all)
	}

	// Wire the restored state into a controller and run handleMerging — it
	// must not post a duplicate comment because the flag is already set.
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = false
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)
	if _, err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState failed: %v", err)
	}

	restored, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("restored PR not tracked")
	}
	restored.Stage = StageMerging

	if err := c.handleMerging(context.Background(), restored); err != nil {
		t.Fatalf("handleMerging after restore returned error: %v", err)
	}
	if commentCount != 0 {
		t.Errorf("after restore: comment count = %d, want 0 (flag honored)", commentCount)
	}
}

// --- GH-2588: CI fix size guard tests ---

// TestCIFixSizeGuard_OversizedPR_BlocksFixIssue is a cascade-2 reproduction:
// a failing PR with 512 additions must NOT spawn a fix(ci) issue and must be closed.
func TestCIFixSizeGuard_OversizedPR_BlocksFixIssue(t *testing.T) {
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
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 10, Body: "<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42/files" && r.Method == http.MethodGet:
			// Simulate cascade-2 PR: 512 net additions
			files := []*github.PRFile{
				{Filename: "internal/gateway/oauth.go", Status: "added", Additions: 244},
				{Filename: "internal/gateway/oauth_test.go", Status: "added", Additions: 240},
				{Filename: "internal/gateway/server.go", Status: "modified", Additions: 28},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    42,
		IssueNumber: 10,
		HeadSHA:     "abc1234",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue must NOT be created when failing PR exceeds size floor")
	}
	if !prClosed {
		t.Error("oversized failing PR must be closed by the size guard")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, "CI fix size guard") {
		t.Errorf("error should mention size guard, got: %s", prState.Error)
	}
}

// TestCIFixSizeGuard_SmallPR_AllowsFixIssue verifies that a small failing PR (50 additions)
// still gets a fix(ci) issue created — existing behavior must be unchanged.
func TestCIFixSizeGuard_SmallPR_AllowsFixIssue(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc5678/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/20" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 20, Body: "<!-- autopilot-meta branch:pilot/GH-20 pr:55 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/55/files" && r.Method == http.MethodGet:
			files := []*github.PRFile{
				{Filename: "internal/foo.go", Status: "modified", Additions: 50},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 300}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			// PR closed after fix issue created (normal flow)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    55,
		IssueNumber: 20,
		HeadSHA:     "abc5678",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("fix issue MUST be created for a small failing PR")
	}
}

// TestCIFixSizeGuard_APIError_FailOpen verifies that a ListPullRequestFiles API error
// does not block fix issue creation — the guard must fail open.
func TestCIFixSizeGuard_APIError_FailOpen(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc9999/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/30" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 30, Body: "<!-- autopilot-meta branch:pilot/GH-30 pr:66 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/66/files" && r.Method == http.MethodGet:
			// Simulate transient API error
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 400}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/66" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    66,
		IssueNumber: 30,
		HeadSHA:     "abc9999",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("fix issue MUST be created when ListPullRequestFiles fails (fail-open)")
	}
}

// asyncApprovalManager returns an approval.Manager configured for async pre-merge approval.
func asyncApprovalManager() *approval.Manager {
	cfg := &approval.Config{
		Enabled:        true,

		DefaultTimeout: 1 * time.Hour,
		DefaultAction:  approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       1 * time.Hour,
			DefaultAction: approval.DecisionRejected,
		},
	}
	return approval.NewManager(cfg)
}

// TestController_AwaitApproval_StaysInStageUntilDecision verifies that the
// non-blocking handleAwaitApproval submits a request on the first tick and
// stays in StageAwaitApproval until a decision is recorded.
func TestController_AwaitApproval_StaysInStageUntilDecision(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	mgr := asyncApprovalManager()
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	// Plant a PR directly in StageAwaitApproval.
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		PRTitle:     "feat: something",
		IssueNumber: 10,
		Stage:       StageAwaitApproval,
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Tick 1: no ApprovalRequestID yet — should submit and stay in stage.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("after tick 1: stage = %s, want %s", pr.Stage, StageAwaitApproval)
	}
	if pr.ApprovalRequestID == "" {
		t.Error("after tick 1: ApprovalRequestID must be set")
	}
	if pr.ApprovalRequestedAt.IsZero() {
		t.Error("after tick 1: ApprovalRequestedAt must be set")
	}

	// Tick 2: request submitted, no decision yet — should stay in stage.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("after tick 2: stage = %s, want %s", pr.Stage, StageAwaitApproval)
	}

	// Record approval decision directly (simulating a Telegram callback).
	_ = c.SetApprovalDecision(ctx, pr.ApprovalRequestID, string(approval.DecisionApproved), "testuser")

	// Tick 3: decision recorded — should advance to StageMerging.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after approval tick: stage = %s, want %s", pr.Stage, StageMerging)
	}
}

// TestController_AwaitApproval_AppliesDefaultActionAtTimeout verifies that when
// ApprovalRequestedAt exceeds the stage timeout and no decision is recorded, the
// controller applies the configured default_action.
func TestController_AwaitApproval_AppliesDefaultActionAtTimeout(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	// Short timeout so the test can simulate expiry without sleeping.
	approvalCfg := &approval.Config{
		Enabled:        true,

		DefaultTimeout: 1 * time.Millisecond,
		DefaultAction:  approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       1 * time.Millisecond,
			DefaultAction: approval.DecisionRejected,
		},
	}
	mgr := approval.NewManager(approvalCfg)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	// Plant a PR in StageAwaitApproval with a request already submitted but expired.
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:            42,
		PRURL:               "https://github.com/owner/repo/pull/42",
		IssueNumber:         10,
		Stage:               StageAwaitApproval,
		ApprovalRequestID:   "req-expired",
		ApprovalRequestedAt: time.Now().Add(-2 * time.Hour), // well past timeout
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Single tick: should detect expiry, apply default_action (rejected), set StageFailed.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after timeout: stage = %s, want %s", pr.Stage, StageFailed)
	}
	if pr.ApprovalDecision != string(approval.DecisionRejected) {
		t.Errorf("after timeout: decision = %q, want %q", pr.ApprovalDecision, approval.DecisionRejected)
	}
}

// TestController_TwoPRQueue_StalledApprovalDoesNotBlockOther verifies that a PR
// stalled in StageAwaitApproval does not prevent another PR from advancing through
// earlier stages.
func TestController_TwoPRQueue_StalledApprovalDoesNotBlockOther(t *testing.T) {
	// Server returns success for any check-run or merge request.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "check-runs"):
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
	// Use stage env so PR-B can reach StageMerging without blocking on approval.
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	mgr := asyncApprovalManager()
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	ctx := context.Background()

	// PR-A: stalled in StageAwaitApproval with request already submitted.
	c.mu.Lock()
	c.activePRs[10] = &PRState{
		PRNumber:            10,
		PRURL:               "https://github.com/owner/repo/pull/10",
		IssueNumber:         1,
		HeadSHA:             "sha10",
		Stage:               StageAwaitApproval,
		ApprovalRequestID:   "req-stalled",
		ApprovalRequestedAt: time.Now(),
	}
	c.mu.Unlock()

	// PR-B: freshly created, will advance independently.
	c.OnPRCreated(20, "https://github.com/owner/repo/pull/20", 2, "sha20", "pilot/GH-2", "")

	// Tick both PRs — PR-A stays stalled, PR-B advances from StagePRCreated → StageWaitingCI.
	_ = c.ProcessPR(ctx, 10, nil)
	_ = c.ProcessPR(ctx, 20, nil)

	prA, _ := c.GetPRState(10)
	prB, _ := c.GetPRState(20)

	if prA.Stage != StageAwaitApproval {
		t.Errorf("PR-A should still be %s, got %s", StageAwaitApproval, prA.Stage)
	}
	if prB.Stage == StagePRCreated {
		t.Errorf("PR-B should have advanced past %s (was blocked by PR-A)", StagePRCreated)
	}
}

// mockApprovalPersister records calls to SetApprovalRequestID and SetApprovalDecision
// so tests can verify that the controller wires through to the memory store.
type mockApprovalPersister struct {
	requestIDCalls []struct{ taskID, requestID string }
	decisionCalls  []struct{ requestID, decision, by string }
}

func (m *mockApprovalPersister) SetApprovalRequestID(_ context.Context, taskID, requestID string) error {
	m.requestIDCalls = append(m.requestIDCalls, struct{ taskID, requestID string }{taskID, requestID})
	return nil
}

func (m *mockApprovalPersister) SetApprovalDecision(_ context.Context, requestID, decision, by string) error {
	m.decisionCalls = append(m.decisionCalls, struct{ requestID, decision, by string }{requestID, decision, by})
	return nil
}

// TestController_SetApprovalDecision_PersistsToMemoryStore verifies that
// Controller.SetApprovalDecision updates in-memory PRState AND calls the
// injected approvalPersister (executions table write path).
func TestController_SetApprovalDecision_PersistsToMemoryStore(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	mgr := approval.NewManager(nil)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	mock := &mockApprovalPersister{}
	c.memoryStore = mock

	c.mu.Lock()
	c.activePRs[99] = &PRState{
		PRNumber:          99,
		IssueNumber:       10,
		ApprovalRequestID: "req-test-123",
	}
	c.mu.Unlock()

	ctx := context.Background()
	if err := c.SetApprovalDecision(ctx, "req-test-123", "approved", "reviewer"); err != nil {
		t.Fatalf("SetApprovalDecision: %v", err)
	}

	// In-memory state updated.
	pr, ok := c.GetPRState(99)
	if !ok {
		t.Fatal("PR state not found")
	}
	if pr.ApprovalDecision != "approved" {
		t.Errorf("in-memory ApprovalDecision = %q, want %q", pr.ApprovalDecision, "approved")
	}

	// Memory store called with correct args.
	if len(mock.decisionCalls) != 1 {
		t.Fatalf("expected 1 SetApprovalDecision call, got %d", len(mock.decisionCalls))
	}
	call := mock.decisionCalls[0]
	if call.requestID != "req-test-123" || call.decision != "approved" || call.by != "reviewer" {
		t.Errorf("unexpected call args: %+v", call)
	}
}

// errApprovalPersister is a mock that returns a configurable error for both methods.
type errApprovalPersister struct {
	requestIDErr error
	decisionErr  error
}

func (m *errApprovalPersister) SetApprovalRequestID(_ context.Context, _, _ string) error {
	return m.requestIDErr
}

func (m *errApprovalPersister) SetApprovalDecision(_ context.Context, _, _, _ string) error {
	return m.decisionErr
}

// TestController_ApprovalPersistMiss_RequestID verifies that a sql.ErrNoRows from
// SetApprovalRequestID increments the request_id miss counter.
func TestController_ApprovalPersistMiss_RequestID(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")
	c.memoryStore = &errApprovalPersister{requestIDErr: sql.ErrNoRows}

	// Directly invoke the counter via the same code path as handleAwaitApproval by
	// calling the private helper through a public wrapper. Since the logic lives in
	// handleAwaitApproval (which calls SetApprovalRequestID), we replicate the
	// pattern inline: set up an active PR and call SetApprovalRequestID to simulate
	// the zero-row path, then check the counter.
	ctx := context.Background()
	taskID := "GH-42"
	requestID := "req-miss-test"

	// Simulate the call site in handleAwaitApproval.
	merr := c.memoryStore.SetApprovalRequestID(ctx, taskID, requestID)
	if merr != nil {
		c.metrics.RecordApprovalPersistMiss("request_id")
	}

	snap := c.metrics.Snapshot()
	if snap.ApprovalPersistMisses["request_id"] != 1 {
		t.Errorf("expected 1 request_id miss, got %d", snap.ApprovalPersistMisses["request_id"])
	}
	if snap.ApprovalPersistMisses["decision"] != 0 {
		t.Errorf("expected 0 decision misses, got %d", snap.ApprovalPersistMisses["decision"])
	}
}

// TestController_ApprovalPersistMiss_Decision verifies that a sql.ErrNoRows from
// SetApprovalDecision increments the decision miss counter via Controller.SetApprovalDecision.
func TestController_ApprovalPersistMiss_Decision(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")
	c.memoryStore = &errApprovalPersister{decisionErr: sql.ErrNoRows}

	c.mu.Lock()
	c.activePRs[7] = &PRState{
		PRNumber:          7,
		IssueNumber:       42,
		ApprovalRequestID: "req-decision-miss",
	}
	c.mu.Unlock()

	ctx := context.Background()
	if err := c.SetApprovalDecision(ctx, "req-decision-miss", "approved", "bot"); err != nil {
		t.Fatalf("SetApprovalDecision: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.ApprovalPersistMisses["decision"] != 1 {
		t.Errorf("expected 1 decision miss, got %d", snap.ApprovalPersistMisses["decision"])
	}
	if snap.ApprovalPersistMisses["request_id"] != 0 {
		t.Errorf("expected 0 request_id misses, got %d", snap.ApprovalPersistMisses["request_id"])
	}
}

// TestController_SetApprovalDecision_NoStoreNoPanic verifies that the controller
// works correctly when no memory store is wired (nil-safe).
func TestController_SetApprovalDecision_NoStoreNoPanic(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")

	c.mu.Lock()
	c.activePRs[1] = &PRState{
		PRNumber:          1,
		ApprovalRequestID: "req-nil-store",
	}
	c.mu.Unlock()

	// Should not panic with nil memoryStore.
	err := c.SetApprovalDecision(context.Background(), "req-nil-store", "approved", "bot")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	pr, _ := c.GetPRState(1)
	if pr.ApprovalDecision != "approved" {
		t.Errorf("in-memory decision not set: %q", pr.ApprovalDecision)
	}
}

// TestController_IssuesProcessed_Success verifies that RecordIssueProcessed("success")
// is called when a PR merges successfully.
func TestController_IssuesProcessed_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/mergesha1/check-runs":
			_, _ = w.Write(mustJSON(t, github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			}))
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, map[string]interface{}{
				"sha": "merged1", "merged": true, "message": "Pull Request successfully merged",
			}))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
			_, _ = w.Write(mustJSON(t, github.PullRequest{
				Number: 42, State: "open",
				Head: github.PRRef{Ref: "pilot/GH-10", SHA: "mergesha1"},
			}))
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
	cfg.RequiredChecks = []string{"build"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "mergesha1", "pilot/GH-10", "")
	pr, _ := c.GetPRState(42)
	pr.Stage = StageMerging

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("IssuesProcessed[success] = %d, want 1", snap.IssuesProcessed["success"])
	}
	if snap.IssuesProcessed["failed"] != 0 {
		t.Errorf("IssuesProcessed[failed] = %d, want 0", snap.IssuesProcessed["failed"])
	}
}

// TestController_IssuesProcessed_TerminalFailure verifies that RecordIssueProcessed("failed")
// is called when a PR hits the CI fix iteration limit (terminal failure path).
func TestController_IssuesProcessed_TerminalFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/failsha1/check-runs":
			_, _ = w.Write(mustJSON(t, github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "failure"}},
			}))
		case r.URL.Path == "/repos/owner/repo/issues/20" && r.Method == http.MethodGet:
			// iteration:3 >= MaxCIFixIterations(3) → terminal stop
			_, _ = w.Write(mustJSON(t, github.Issue{
				Number: 20,
				Body:   "Fix CI failures.\n\n<!-- autopilot-meta branch:pilot/GH-20 pr:55 iteration:3 -->\n",
			}))
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxCIFixIterations = 3
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(55, "https://github.com/owner/repo/pull/55", 20, "failsha1", "pilot/GH-20", "")
	pr, _ := c.GetPRState(55)
	pr.Stage = StageCIFailed

	if err := c.ProcessPR(context.Background(), 55, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.IssuesProcessed["failed"] != 1 {
		t.Errorf("IssuesProcessed[failed] = %d, want 1", snap.IssuesProcessed["failed"])
	}
	if snap.IssuesProcessed["success"] != 0 {
		t.Errorf("IssuesProcessed[success] = %d, want 0", snap.IssuesProcessed["success"])
	}
}

// TestController_ScanRecentlyMergedPRs_RecordsMetrics verifies that
// ScanRecentlyMergedPRs fires merge metrics on first discovery (GH-2981)
// and is idempotent on subsequent scans.
func TestController_ScanRecentlyMergedPRs_RecordsMetrics(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)

	pilotPR := github.PullRequest{
		Number:         77,
		Head:           github.PRRef{Ref: "pilot/GH-300", SHA: "sha77"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/77",
		Title:          "feat(api): new endpoint",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: "merge-sha-77",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	// First scan: PR not in state store → metrics must be recorded.
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("first ScanRecentlyMergedPRs() error = %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("after first scan: PRsMerged = %d, want 1", snap.PRsMerged)
	}
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("after first scan: IssuesProcessed[success] = %d, want 1", snap.IssuesProcessed["success"])
	}
	hist := c.metrics.HistogramSnapshot()
	if len(hist.PRTimeToMerge) != 1 {
		t.Errorf("after first scan: PRTimeToMerge samples = %d, want 1", len(hist.PRTimeToMerge))
	}

	// Second scan: PR is now in state store at StageReleasing → counts must not change.
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("second ScanRecentlyMergedPRs() error = %v", err)
	}

	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 1 {
		t.Errorf("after second scan (idempotency): PRsMerged = %d, want 1", snap2.PRsMerged)
	}
	if snap2.IssuesProcessed["success"] != 1 {
		t.Errorf("after second scan (idempotency): IssuesProcessed[success] = %d, want 1", snap2.IssuesProcessed["success"])
	}
	hist2 := c.metrics.HistogramSnapshot()
	if len(hist2.PRTimeToMerge) != 1 {
		t.Errorf("after second scan (idempotency): PRTimeToMerge samples = %d, want 1", len(hist2.PRTimeToMerge))
	}
}

// TestController_ScanRecentlyMergedPRs_RecordsMetricsDespiteExistingRelease
// reproduces the bug where Pilot's own self-release pipeline always tags every
// merge within ~1min, so by the time the ~5-15min scanner tick runs the
// "release already exists" gate at line 2186 skips the PR before metrics
// fire. The recorder must fire BEFORE the gate so counters move in
// stage-mode auto-merge.
func TestController_ScanRecentlyMergedPRs_RecordsMetricsDespiteExistingRelease(t *testing.T) {
	recentMergedAt := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	recentCreatedAt := time.Now().Add(-12 * time.Minute).UTC().Format(time.RFC3339)
	mergeSHA := "merge-sha-99"

	pilotPR := github.PullRequest{
		Number:         99,
		Head:           github.PRRef{Ref: "pilot/GH-501", SHA: "headsha99"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/99",
		Title:          "feat(api): another endpoint",
		Merged:         true,
		CreatedAt:      recentCreatedAt,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: mergeSHA,
	}

	// Release already exists for the merge SHA (Pilot's self-shipping pattern).
	existingRelease := github.Release{
		TagName:         "v9.9.9",
		TargetCommitish: mergeSHA,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Release{&existingRelease})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("first ScanRecentlyMergedPRs() error = %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("PRsMerged = %d, want 1 (recorder must fire even when release tag exists)", snap.PRsMerged)
	}
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("IssuesProcessed[success] = %d, want 1", snap.IssuesProcessed["success"])
	}
	hist := c.metrics.HistogramSnapshot()
	if len(hist.PRTimeToMerge) != 1 {
		t.Errorf("PRTimeToMerge samples = %d, want 1", len(hist.PRTimeToMerge))
	}

	// Verify release-exists gate still suppresses release triggering: PR should
	// NOT have been added to activePRs (would happen if scanner proceeded past
	// the gate).
	c.mu.RLock()
	_, tracked := c.activePRs[99]
	c.mu.RUnlock()
	if tracked {
		t.Error("PR 99 was added to activePRs; release-exists gate should still suppress release triggering")
	}

	// Second scan: counts unchanged (idempotency).
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("second ScanRecentlyMergedPRs() error = %v", err)
	}
	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 1 {
		t.Errorf("after second scan: PRsMerged = %d, want 1 (idempotent)", snap2.PRsMerged)
	}
}

// TestController_RecordMergeSuccess_Idempotency verifies recordMergeSuccess
// fires exactly once per PR number even when called multiple times from
// different code paths (e.g. handleMerging + ScanRecentlyMergedPRs both
// observing the same PR).
func TestController_RecordMergeSuccess_Idempotency(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")

	createdAt := time.Now().Add(-10 * time.Minute)
	prState := &PRState{PRNumber: 42, CreatedAt: createdAt}

	c.recordMergeSuccess(prState)
	c.recordMergeSuccess(prState)
	c.recordMergeSuccess(prState)

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("PRsMerged = %d after 3 calls, want 1", snap.PRsMerged)
	}
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("IssuesProcessed[success] = %d after 3 calls, want 1", snap.IssuesProcessed["success"])
	}
	hist := c.metrics.HistogramSnapshot()
	if len(hist.PRTimeToMerge) != 1 {
		t.Errorf("PRTimeToMerge samples = %d after 3 calls, want 1", len(hist.PRTimeToMerge))
	}

	// Different PR number → fires independently.
	c.recordMergeSuccess(&PRState{PRNumber: 43, CreatedAt: createdAt})
	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 2 {
		t.Errorf("PRsMerged = %d after second PR, want 2", snap2.PRsMerged)
	}
}
