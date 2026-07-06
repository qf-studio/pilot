package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GH-3569: the misconfig error must name the ACTUAL escalation trigger
// (size-floor gate, scope-drift gate, or env require_approval) — the old
// hardcoded message blamed require_approval=true even when the env had it
// false and a defense-in-depth gate did the escalating (observed on PR #3559).
func TestSubmitAsyncApprovalRequest_MisconfigErrorText(t *testing.T) {
	tests := []struct {
		name             string
		escalationReason string
		wantInError      string
	}{
		{
			name:             "size-floor gate reason is reported verbatim",
			escalationReason: "PR adds 656 net lines (> 500 threshold)",
			wantInError:      "PR adds 656 net lines (> 500 threshold)",
		},
		{
			name:             "scope-drift gate reason is reported verbatim",
			escalationReason: `PR title type "feat" diverges from issue title type "fix"`,
			wantInError:      `PR title type "feat" diverges from issue title type "fix"`,
		},
		{
			name:             "env require_approval reason is reported verbatim",
			escalationReason: "environments.prod.require_approval=true",
			wantInError:      "environments.prod.require_approval=true",
		},
		{
			name:             "zero-value falls back to env-based wording",
			escalationReason: "",
			wantInError:      "require_approval=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("[]"))
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvStage
			// approvalMgr nil → IsStageEnabled false → misconfig branch.
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:         90,
				Stage:            StageAwaitApproval,
				EscalationReason: tt.escalationReason,
			}

			if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
				t.Fatalf("submitAsyncApprovalRequest returned error: %v", err)
			}
			if prState.Stage != StageFailed {
				t.Errorf("Stage = %v, want StageFailed", prState.Stage)
			}
			if !strings.Contains(prState.Error, tt.wantInError) {
				t.Errorf("Error %q does not contain %q", prState.Error, tt.wantInError)
			}
			if tt.escalationReason != "" && !strings.Contains(prState.Error, tt.escalationReason) {
				t.Errorf("Error %q must carry the escalation reason %q", prState.Error, tt.escalationReason)
			}
		})
	}
}

// GH-3569: handleCIPassed must record why the PR entered StageAwaitApproval.
func TestHandleCIPassed_SetsEscalationReason(t *testing.T) {
	tests := []struct {
		name         string
		additions    int
		requireAppr  bool
		wantInReason string
	}{
		{name: "size-floor gate stamps its reason", additions: SizeFloorThreshold + 1, wantInReason: "net lines"},
		{name: "env require_approval stamps env wording", additions: 1, requireAppr: true, wantInReason: "require_approval=true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/files") && r.Method == http.MethodGet {
					files := []*github.PRFile{{Filename: "a.go", Status: "modified", Additions: tt.additions}}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(files)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("{}"))
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			if tt.requireAppr {
				cfg.activeEnvName = "dev"
				cfg.activeEnvConfig = &EnvironmentConfig{Branch: "main", RequireApproval: true}
			}
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 91, PRTitle: "fix(auth): fix bug", Stage: StageCIPassed}
			if err := c.handleCIPassed(context.Background(), prState); err != nil {
				t.Fatalf("handleCIPassed: %v", err)
			}
			if prState.Stage != StageAwaitApproval {
				t.Fatalf("Stage = %v, want StageAwaitApproval", prState.Stage)
			}
			if !strings.Contains(prState.EscalationReason, tt.wantInReason) {
				t.Errorf("EscalationReason %q does not contain %q", prState.EscalationReason, tt.wantInReason)
			}
		})
	}
}

// GH-3569: the misconfig PR comment names the actual escalation reason.
func TestPostMisconfigComment_NamesEscalationReason(t *testing.T) {
	var postedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			postedBody = payload.Body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	merger := NewAutoMerger(ghClient, nil, nil, "owner", "repo", DefaultConfig())
	prState := &PRState{
		PRNumber:         92,
		EscalationReason: "PR adds 656 net lines (> 500 threshold)",
	}

	merger.postMisconfigComment(context.Background(), prState)

	if !strings.Contains(postedBody, "PR adds 656 net lines") {
		t.Errorf("comment body does not name the escalation reason: %q", postedBody)
	}
	if strings.Contains(postedBody, "require_approval: true") {
		t.Errorf("comment body still blames require_approval despite a gate reason: %q", postedBody)
	}
}
