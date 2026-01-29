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

func TestNewAutoMerger(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	approvalMgr := approval.NewManager(nil)
	cfg := DefaultConfig()

	merger := NewAutoMerger(ghClient, approvalMgr, "owner", "repo", cfg)

	if merger == nil {
		t.Fatal("NewAutoMerger returned nil")
	}
	if merger.owner != "owner" {
		t.Errorf("owner = %s, want owner", merger.owner)
	}
	if merger.repo != "repo" {
		t.Errorf("repo = %s, want repo", merger.repo)
	}
}

func TestAutoMerger_RequiresApproval(t *testing.T) {
	tests := []struct {
		name     string
		env      Environment
		wantAppr bool
	}{
		{"dev - no approval", EnvDev, false},
		{"stage - no approval", EnvStage, false},
		{"prod - requires approval", EnvProd, true},
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)
			got := merger.requiresApproval(tt.env)
			if got != tt.wantAppr {
				t.Errorf("requiresApproval(%s) = %v, want %v", tt.env, got, tt.wantAppr)
			}
		})
	}
}

func TestAutoMerger_ShouldWaitForCI(t *testing.T) {
	tests := []struct {
		name     string
		env      Environment
		wantWait bool
	}{
		{"dev - no wait", EnvDev, false},
		{"stage - wait for CI", EnvStage, true},
		{"prod - wait for CI", EnvProd, true},
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)
			got := merger.ShouldWaitForCI(tt.env)
			if got != tt.wantWait {
				t.Errorf("ShouldWaitForCI(%s) = %v, want %v", tt.env, got, tt.wantWait)
			}
		})
	}
}

func TestAutoMerger_CanMerge(t *testing.T) {
	tests := []struct {
		name       string
		pr         github.PullRequest
		statusCode int
		canMerge   bool
		reason     string
		wantErr    bool
	}{
		{
			name: "can merge - open and mergeable",
			pr: github.PullRequest{
				Number:    42,
				State:     "open",
				Merged:    false,
				Mergeable: boolPtr(true),
			},
			statusCode: http.StatusOK,
			canMerge:   true,
			reason:     "",
			wantErr:    false,
		},
		{
			name: "cannot merge - already merged",
			pr: github.PullRequest{
				Number:    42,
				State:     "closed",
				Merged:    true,
				Mergeable: boolPtr(false),
			},
			statusCode: http.StatusOK,
			canMerge:   false,
			reason:     "already merged",
			wantErr:    false,
		},
		{
			name: "cannot merge - closed",
			pr: github.PullRequest{
				Number:    42,
				State:     "closed",
				Merged:    false,
				Mergeable: boolPtr(true),
			},
			statusCode: http.StatusOK,
			canMerge:   false,
			reason:     "PR is closed",
			wantErr:    false,
		},
		{
			name: "cannot merge - conflicts",
			pr: github.PullRequest{
				Number:    42,
				State:     "open",
				Merged:    false,
				Mergeable: boolPtr(false),
			},
			statusCode: http.StatusOK,
			canMerge:   false,
			reason:     "merge conflicts",
			wantErr:    false,
		},
		{
			name: "can merge - mergeable nil (unknown)",
			pr: github.PullRequest{
				Number:    42,
				State:     "open",
				Merged:    false,
				Mergeable: nil,
			},
			statusCode: http.StatusOK,
			canMerge:   true,
			reason:     "",
			wantErr:    false,
		},
		{
			name:       "error - not found",
			statusCode: http.StatusNotFound,
			canMerge:   false,
			reason:     "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/owner/repo/pulls/42" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == http.StatusOK {
					_ = json.NewEncoder(w).Encode(tt.pr)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

			canMerge, reason, err := merger.CanMerge(context.Background(), 42)

			if (err != nil) != tt.wantErr {
				t.Errorf("CanMerge() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if canMerge != tt.canMerge {
				t.Errorf("CanMerge() canMerge = %v, want %v", canMerge, tt.canMerge)
			}
			if reason != tt.reason {
				t.Errorf("CanMerge() reason = %q, want %q", reason, tt.reason)
			}
		})
	}
}

func TestAutoMerger_MergePR_DevEnvironment(t *testing.T) {
	mergeCalledWith := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			// Auto-review call
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/pulls/42/merge":
			// Merge call
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mergeCalledWith = body["merge_method"]
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = true
	cfg.MergeMethod = github.MergeMethodSquash

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		HeadSHA:  "abc123",
	}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if mergeCalledWith != github.MergeMethodSquash {
		t.Errorf("merge called with method = %s, want squash", mergeCalledWith)
	}
}

func TestAutoMerger_MergePR_ProdRequiresApproval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s - should have failed before calling GitHub", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	// No approval manager configured
	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
	}

	err := merger.MergePR(context.Background(), prState)
	if err == nil {
		t.Error("MergePR() should fail when prod requires approval but no manager configured")
	}
}

func TestAutoMerger_MergePR_ProdWithApprovalDisabled(t *testing.T) {
	// Scenario: Prod environment but pre-merge approval stage is disabled
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Approval manager with pre-merge disabled
	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = true
	approvalCfg.PreMerge.Enabled = false
	approvalMgr := approval.NewManager(approvalCfg)

	cfg := DefaultConfig()
	cfg.Environment = EnvProd
	cfg.AutoReview = true

	merger := NewAutoMerger(ghClient, approvalMgr, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
	}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !mergeWasCalled {
		t.Error("merge should have been called when approval stage is disabled")
	}
}

func TestAutoMerger_MergePR_DefaultMergeMethod(t *testing.T) {
	mergeCalledWith := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/merge":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mergeCalledWith = body["merge_method"]
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
	cfg.MergeMethod = "" // Empty - should default to squash

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if mergeCalledWith != github.MergeMethodSquash {
		t.Errorf("merge method = %s, want squash (default)", mergeCalledWith)
	}
}

func TestAutoMerger_MergePR_MergeFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"message": "Pull request is not mergeable"}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoReview = true

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}

	err := merger.MergePR(context.Background(), prState)
	if err == nil {
		t.Error("MergePR() should return error when merge fails")
	}
}

func TestAutoMerger_MergePR_AutoReviewFailureContinues(t *testing.T) {
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			// Auto-review fails (e.g., already reviewed)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "/repos/owner/repo/pulls/42/merge":
			// Merge should still be attempted
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
	cfg.AutoReview = true

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !mergeWasCalled {
		t.Error("merge should still be called when auto-review fails")
	}
}

func TestAutoMerger_MergePR_StageEnvironment(t *testing.T) {
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			w.WriteHeader(http.StatusOK)
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
	cfg.Environment = EnvStage
	cfg.AutoReview = true

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !mergeWasCalled {
		t.Error("merge should be called for stage environment")
	}
}

func TestAutoMerger_ApprovePR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/pulls/42/reviews" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)

		if body["event"] != github.ReviewEventApprove {
			t.Errorf("expected APPROVE event, got %s", body["event"])
		}
		if body["body"] != "Auto-approved by Pilot autopilot" {
			t.Errorf("unexpected review body: %s", body["body"])
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	err := merger.approvePR(context.Background(), 42)
	if err != nil {
		t.Errorf("approvePR() error = %v", err)
	}
}

func TestAutoMerger_RequestApproval_NoManager(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
	}

	approved, err := merger.requestApproval(context.Background(), prState)
	if err == nil {
		t.Error("requestApproval() should fail when no manager configured")
	}
	if approved {
		t.Error("should not be approved when no manager configured")
	}
}

func TestEnvironmentBehaviorMatrix(t *testing.T) {
	// Verify the environment behavior matrix from the issue spec
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	tests := []struct {
		env              Environment
		autoReview       bool
		immediateCI      bool // true = no CI wait, false = wait for CI
		requiresApproval bool
	}{
		{EnvDev, true, true, false},    // dev: Auto-Review Yes, Immediate merge, No approval
		{EnvStage, true, false, false}, // stage: Auto-Review Yes, Wait for CI, No approval
		{EnvProd, false, false, true},  // prod: No auto-review (per matrix), Wait for CI, Requires approval
	}

	for _, tt := range tests {
		t.Run(string(tt.env), func(t *testing.T) {
			merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

			shouldWait := merger.ShouldWaitForCI(tt.env)
			wantWait := !tt.immediateCI

			if shouldWait != wantWait {
				t.Errorf("ShouldWaitForCI(%s) = %v, want %v", tt.env, shouldWait, wantWait)
			}

			requiresApproval := merger.requiresApproval(tt.env)
			if requiresApproval != tt.requiresApproval {
				t.Errorf("requiresApproval(%s) = %v, want %v", tt.env, requiresApproval, tt.requiresApproval)
			}
		})
	}
}

// Helper function for creating *bool
func boolPtr(b bool) *bool {
	return &b
}

func TestAutoMerger_MergePR_AllMergeMethods(t *testing.T) {
	methods := []string{
		github.MergeMethodMerge,
		github.MergeMethodSquash,
		github.MergeMethodRebase,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			capturedMethod := ""

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls/42/merge" {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					capturedMethod = body["merge_method"]
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.AutoReview = false
			cfg.MergeMethod = method

			merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

			err := merger.MergePR(context.Background(), &PRState{PRNumber: 42})
			if err != nil {
				t.Errorf("MergePR() error = %v", err)
			}

			if capturedMethod != method {
				t.Errorf("merge method = %s, want %s", capturedMethod, method)
			}
		})
	}
}

func TestAutoMerger_CanMerge_IntegrationScenarios(t *testing.T) {
	// Test real-world PR state combinations
	tests := []struct {
		name     string
		state    string
		merged   bool
		canMerge bool
	}{
		{"open PR", "open", false, true},
		{"merged PR", "closed", true, false},
		{"closed without merge", "closed", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pr := github.PullRequest{
					Number:    42,
					State:     tt.state,
					Merged:    tt.merged,
					Mergeable: boolPtr(true),
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(pr)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

			canMerge, _, err := merger.CanMerge(context.Background(), 42)
			if err != nil {
				t.Errorf("CanMerge() error = %v", err)
			}
			if canMerge != tt.canMerge {
				t.Errorf("CanMerge() = %v, want %v", canMerge, tt.canMerge)
			}
		})
	}
}

func TestAutoMerger_WithApprovalTimeout(t *testing.T) {
	// Verify that approval timeout from config is accessible
	cfg := DefaultConfig()
	cfg.ApprovalTimeout = 2 * time.Hour

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	merger := NewAutoMerger(ghClient, nil, "owner", "repo", cfg)

	if merger.config.ApprovalTimeout != 2*time.Hour {
		t.Errorf("ApprovalTimeout = %v, want 2h", merger.config.ApprovalTimeout)
	}
}
