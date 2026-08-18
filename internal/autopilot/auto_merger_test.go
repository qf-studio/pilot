package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestNewAutoMerger(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	approvalMgr := approval.NewManager(nil)
	cfg := DefaultConfig()

	merger := NewAutoMerger(ghClient, approvalMgr, nil, "owner", "repo", cfg)

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

func TestAutoMerger_ShouldWaitForCI(t *testing.T) {
	// All environments now wait for CI to prevent broken code from merging
	tests := []struct {
		name     string
		env      Environment
		wantWait bool
	}{
		{"dev - wait for CI", EnvDev, true},
		{"stage - wait for CI", EnvStage, true},
		{"prod - wait for CI", EnvProd, true},
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)
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
		{
			name: "cannot merge - non-default base (GH-4872)",
			pr: github.PullRequest{
				Number:    42,
				State:     "open",
				Merged:    false,
				Mergeable: boolPtr(true),
				Base:      github.PRRef{Ref: "pilot/GH-70"},
			},
			statusCode: http.StatusOK,
			canMerge:   false,
			reason:     `base branch mismatch: targets "pilot/GH-70", not default branch "main"`,
			wantErr:    false,
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
			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

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
		case "/repos/owner/repo/pulls/42/files":
			// GH-2585: size-floor gate enumerates files. Return empty list.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
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
	cfg.MergeMethod = github.MergeMethodSquash

	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

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

func TestAutoMerger_MergePR_NonProdWithApprovalDisabled(t *testing.T) {
	// Scenario: Non-prod environment (dev/stage) with approval disabled should still auto-approve
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

	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = true
	approvalCfg.PreMerge.Enabled = false
	approvalMgr := approval.NewManager(approvalCfg)

	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	merger := NewAutoMerger(ghClient, approvalMgr, nil, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
	}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !mergeWasCalled {
		t.Error("merge should have been called in dev when approval stage is disabled")
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
	cfg.MergeMethod = "" // Empty - should default to squash

	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

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

	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}

	err := merger.MergePR(context.Background(), prState)
	if err == nil {
		t.Error("MergePR() should return error when merge fails")
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

	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

	prState := &PRState{PRNumber: 42}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !mergeWasCalled {
		t.Error("merge should be called for stage environment")
	}
}

func TestEnvironmentBehaviorMatrix(t *testing.T) {
	// Verify the environment behavior matrix
	// All environments now wait for CI to prevent broken code from merging
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	tests := []struct {
		env       Environment
		waitForCI bool // all environments now wait for CI
	}{
		{EnvDev, true},
		{EnvStage, true},
		{EnvProd, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.env), func(t *testing.T) {
			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

			shouldWait := merger.ShouldWaitForCI(tt.env)
			if shouldWait != tt.waitForCI {
				t.Errorf("ShouldWaitForCI(%s) = %v, want %v", tt.env, shouldWait, tt.waitForCI)
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
			cfg.MergeMethod = method

			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

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

func TestAutoMerger_MergePR_SquashUsePRTitle(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		prTitle   string
		wantTitle string
	}{
		{
			name:      "squash with PR title uses PR title",
			method:    github.MergeMethodSquash,
			prTitle:   "feat(api): add rate limiting",
			wantTitle: "feat(api): add rate limiting (#42)",
		},
		{
			name:      "squash without PR title falls back to default",
			method:    github.MergeMethodSquash,
			prTitle:   "",
			wantTitle: "Merge PR #42",
		},
		{
			name:      "squash with PR title already suffixed does not double-append",
			method:    github.MergeMethodSquash,
			prTitle:   "feat(api): add rate limiting (#42)",
			wantTitle: "feat(api): add rate limiting (#42)",
		},
		{
			name:      "regular merge ignores PR title",
			method:    github.MergeMethodMerge,
			prTitle:   "feat(api): add rate limiting",
			wantTitle: "Merge PR #42",
		},
		{
			name:      "rebase ignores PR title",
			method:    github.MergeMethodRebase,
			prTitle:   "fix(auth): token refresh",
			wantTitle: "Merge PR #42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitle := ""

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls/42/merge" {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					capturedTitle = body["commit_title"]
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.MergeMethod = tt.method

			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

			err := merger.MergePR(context.Background(), &PRState{
				PRNumber: 42,
				PRTitle:  tt.prTitle,
			})
			if err != nil {
				t.Errorf("MergePR() error = %v", err)
			}

			if capturedTitle != tt.wantTitle {
				t.Errorf("commit_title = %q, want %q", capturedTitle, tt.wantTitle)
			}
		})
	}
}

func TestAutoMerger_MergePR_SquashStripsIssuePrefix(t *testing.T) {
	tests := []struct {
		name        string
		prTitle     string
		issueNumber int
		wantTitle   string
	}{
		{
			name:        "strips GH-XXXX: prefix from squash commit",
			prTitle:     "GH-2312: fix(autopilot): strip issue prefix from squash title",
			issueNumber: 2312,
			wantTitle:   "fix(autopilot): strip issue prefix from squash title (#42)",
		},
		{
			name:        "strips regardless of issue number mismatch (regex-driven, not IssueNumber-gated)",
			prTitle:     "GH-9999: feat(scope): something",
			issueNumber: 2312,
			wantTitle:   "feat(scope): something (#42)",
		},
		{
			name:        "strips even when no issue number set (GH-4966)",
			prTitle:     "GH-2312: fix(autopilot): something",
			issueNumber: 0,
			wantTitle:   "fix(autopilot): something (#42)",
		},
		{
			name:        "plain conventional commit unchanged",
			prTitle:     "feat(api): add endpoint",
			issueNumber: 2312,
			wantTitle:   "feat(api): add endpoint (#42)",
		},
		{
			name:        "strips jira-style non-GH prefix (GH-4966)",
			prTitle:     "APP-123: feat(auth): oauth flow",
			issueNumber: 123,
			wantTitle:   "feat(auth): oauth flow (#42)",
		},
		{
			name:        "strips compound linear prefix with no issue number (GH-4966)",
			prTitle:     "LIN-ROU-586: fix(core): widen window",
			issueNumber: 0,
			wantTitle:   "fix(core): widen window (#42)",
		},
		{
			name:        "strips three-segment linear prefix (GH-4966)",
			prTitle:     "LIN-MY1-TEAM-42: fix(core): resolve",
			issueNumber: 0,
			wantTitle:   "fix(core): resolve (#42)",
		},
		{
			name:        "prefix-only title with empty subject does not panic on suffix append (GH-4966)",
			prTitle:     "GH-123: ",
			issueNumber: 123,
			wantTitle:   " (#42)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capturedTitle := ""

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls/42/merge" {
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					capturedTitle = body["commit_title"]
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.MergeMethod = github.MergeMethodSquash

			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

			err := merger.MergePR(context.Background(), &PRState{
				PRNumber:    42,
				PRTitle:     tt.prTitle,
				IssueNumber: tt.issueNumber,
			})
			if err != nil {
				t.Errorf("MergePR() error = %v", err)
			}

			if capturedTitle != tt.wantTitle {
				t.Errorf("commit_title = %q, want %q", capturedTitle, tt.wantTitle)
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
			merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

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
	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

	if merger.config.ApprovalTimeout != 2*time.Hour {
		t.Errorf("ApprovalTimeout = %v, want 2h", merger.config.ApprovalTimeout)
	}
}

func TestAutoMerger_VerifyCIBeforeMerge(t *testing.T) {
	tests := []struct {
		name        string
		ciStatus    CIStatus
		wantErr     bool
		errContains string
	}{
		{
			name:     "CI success - verification passes",
			ciStatus: CISuccess,
			wantErr:  false,
		},
		{
			name:        "CI failure - verification fails",
			ciStatus:    CIFailure,
			wantErr:     true,
			errContains: "CI checks failing",
		},
		{
			name:        "CI pending - verification fails",
			ciStatus:    CIPending,
			wantErr:     true,
			errContains: "CI checks still pending",
		},
		{
			name:        "CI running - verification fails",
			ciStatus:    CIRunning,
			wantErr:     true,
			errContains: "CI checks still pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock server that returns check runs with the desired status
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/commits/abc123def/check-runs" {
					checkStatus := github.CheckRunCompleted
					conclusion := github.ConclusionSuccess

					switch tt.ciStatus {
					case CISuccess:
						checkStatus = github.CheckRunCompleted
						conclusion = github.ConclusionSuccess
					case CIFailure:
						checkStatus = github.CheckRunCompleted
						conclusion = github.ConclusionFailure
					case CIPending:
						checkStatus = github.CheckRunQueued
						conclusion = ""
					case CIRunning:
						checkStatus = github.CheckRunInProgress
						conclusion = ""
					}

					response := github.CheckRunsResponse{
						TotalCount: 1,
						CheckRuns: []github.CheckRun{
							{
								Name:       "build",
								Status:     checkStatus,
								Conclusion: conclusion,
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(response)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.RequiredChecks = []string{} // Check all runs

			ciMonitor := NewCIMonitor(ghClient, "owner", "repo", cfg)
			merger := NewAutoMerger(ghClient, nil, ciMonitor, "owner", "repo", cfg)

			prState := &PRState{
				PRNumber: 42,
				HeadSHA:  "abc123def",
			}

			err := merger.verifyCIBeforeMerge(context.Background(), prState)

			if (err != nil) != tt.wantErr {
				t.Errorf("verifyCIBeforeMerge() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" {
				if err == nil || !containsStr(err.Error(), tt.errContains) {
					t.Errorf("verifyCIBeforeMerge() error = %v, want error containing %q", err, tt.errContains)
				}
			}
		})
	}
}

func TestAutoMerger_VerifyCIBeforeMerge_NoCIMonitor(t *testing.T) {
	// When CI monitor is nil, verification should be skipped (no error)
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc123",
	}

	err := merger.verifyCIBeforeMerge(context.Background(), prState)
	if err != nil {
		t.Errorf("verifyCIBeforeMerge() with nil CIMonitor should not error, got %v", err)
	}
}

func TestAutoMerger_MergePR_StageWithCIVerification(t *testing.T) {
	// Stage environment should verify CI before merge
	mergeWasCalled := false
	ciCheckCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc123def/check-runs":
			ciCheckCalled = true
			response := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{
						Name:       "build",
						Status:     github.CheckRunCompleted,
						Conclusion: github.ConclusionSuccess,
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(response)
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
	cfg.RequiredChecks = []string{}

	ciMonitor := NewCIMonitor(ghClient, "owner", "repo", cfg)
	merger := NewAutoMerger(ghClient, nil, ciMonitor, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc123def",
	}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !ciCheckCalled {
		t.Error("CI check should have been called for stage environment")
	}
	if !mergeWasCalled {
		t.Error("merge should have been called after CI verification passed")
	}
}

func TestAutoMerger_MergePR_StageWithCIFailure(t *testing.T) {
	// Stage environment should block merge when CI fails
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc123def/check-runs":
			response := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{
						Name:       "build",
						Status:     github.CheckRunCompleted,
						Conclusion: github.ConclusionFailure,
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(response)
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
	cfg.RequiredChecks = []string{}

	ciMonitor := NewCIMonitor(ghClient, "owner", "repo", cfg)
	merger := NewAutoMerger(ghClient, nil, ciMonitor, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc123def",
	}

	err := merger.MergePR(context.Background(), prState)
	if err == nil {
		t.Error("MergePR() should fail when CI is failing")
	}

	if mergeWasCalled {
		t.Error("merge should NOT have been called when CI is failing")
	}
}

func TestAutoMerger_MergePR_DevWithCIVerification(t *testing.T) {
	// Dev environment now calls CI verification like all other environments
	ciCheckCalled := false
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc123def/check-runs":
			ciCheckCalled = true
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
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
	cfg.RequiredChecks = []string{"build"}

	ciMonitor := NewCIMonitor(ghClient, "owner", "repo", cfg)
	merger := NewAutoMerger(ghClient, nil, ciMonitor, "owner", "repo", cfg)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "abc123def",
	}

	err := merger.MergePR(context.Background(), prState)
	if err != nil {
		t.Errorf("MergePR() error = %v", err)
	}

	if !ciCheckCalled {
		t.Error("CI check should have been called for dev environment")
	}
	if !mergeWasCalled {
		t.Error("merge should have been called for dev environment")
	}
}

func TestAutoMerger_PostMisconfigComment_Idempotent(t *testing.T) {
	// First call: no existing comments → comment is posted.
	// Second call: marker present in comment list → posting is skipped.
	postCount := 0
	listCallCount := 0
	labelCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/repos/owner/repo/issues/42/comments":
			listCallCount++
			if postCount == 0 {
				// No comments yet.
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("[]"))
			} else {
				// Return the previously posted comment with the marker.
				resp := []map[string]interface{}{
					{"id": 1, "body": misconfigCommentMarker + "\nsome text"},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}
		case r.Method == http.MethodPost && path == "/repos/owner/repo/issues/42/comments":
			postCount++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"body":"posted"}`))
		case r.Method == http.MethodPost && path == "/repos/owner/repo/issues/42/labels":
			labelCount++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", cfg)
	prState := &PRState{PRNumber: 42}

	// First call — should post the comment. The parked label is applied to
	// the linked issue by the controller's park path, never from here.
	merger.postMisconfigComment(context.Background(), prState, "approval.enabled")
	if postCount != 1 {
		t.Errorf("first call: postCount = %d, want 1", postCount)
	}
	if labelCount != 0 {
		t.Errorf("first call: labelCount = %d, want 0 (label is controller-side)", labelCount)
	}

	// Second call — marker in list, should NOT post again.
	merger.postMisconfigComment(context.Background(), prState, "approval.enabled")
	if postCount != 1 {
		t.Errorf("second call: postCount = %d, want 1 (idempotent)", postCount)
	}
	if labelCount != 0 {
		t.Errorf("second call: labelCount = %d, want 0 (label is controller-side)", labelCount)
	}
	if listCallCount != 2 {
		t.Errorf("listCallCount = %d, want 2", listCallCount)
	}
}

// containsStr is a helper to check if a string contains a substring
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
