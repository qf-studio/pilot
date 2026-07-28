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

// GH-4596: when a gate demands approval but no approval channel is wired
// (approvalMgr nil, or approval.pre_merge.enabled=false), the PR must stay
// parked in StageAwaitApproval — not transition to StageFailed — with
// EscalationReason recording the ACTUAL gate that fired (size-floor gate,
// scope-drift gate, or env require_approval). Before GH-4596 this branch
// blamed require_approval=true even when the env had it false and a
// defense-in-depth gate did the escalating (observed on PR #3559), AND
// terminated the PR into StageFailed, leaving no live PR for auto-merge/board
// write-back to resume once the config was fixed.
func TestSubmitAsyncApprovalRequest_MisconfigParksInsteadOfFailing(t *testing.T) {
	tests := []struct {
		name             string
		escalationReason string
		wantInReason     string
	}{
		{
			name:             "size-floor gate reason is reported verbatim",
			escalationReason: "PR adds 656 net lines (> 500 threshold)",
			wantInReason:     "PR adds 656 net lines (> 500 threshold)",
		},
		{
			name:             "scope-drift gate reason is reported verbatim",
			escalationReason: `PR title type "feat" diverges from issue title type "fix"`,
			wantInReason:     `PR title type "feat" diverges from issue title type "fix"`,
		},
		{
			name:             "env require_approval reason is reported verbatim",
			escalationReason: "environments.prod.require_approval=true",
			wantInReason:     "environments.prod.require_approval=true",
		},
		{
			name:             "zero-value falls back to env-based wording",
			escalationReason: "",
			wantInReason:     "require_approval=true",
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
			if prState.Stage != StageAwaitApproval {
				t.Errorf("Stage = %v, want StageAwaitApproval (parked, not failed)", prState.Stage)
			}
			if !prState.Parked {
				t.Errorf("Parked = false, want true")
			}
			if prState.Error != "" {
				t.Errorf("Error = %q, want empty — a parked PR is not a failed PR", prState.Error)
			}
			if !strings.Contains(prState.EscalationReason, tt.wantInReason) {
				t.Errorf("EscalationReason %q does not contain %q", prState.EscalationReason, tt.wantInReason)
			}
		})
	}
}

// GH-4596: a second tick of the same parked PR must not re-post the misconfig
// comment or otherwise re-run the one-time side effects — Parked dedupes it.
func TestSubmitAsyncApprovalRequest_MisconfigParkIsIdempotent(t *testing.T) {
	commentPosts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			commentPosts++
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:         93,
		Stage:            StageAwaitApproval,
		EscalationReason: "PR adds 656 net lines (> 500 threshold)",
	}

	for i := 0; i < 3; i++ {
		if err := c.submitAsyncApprovalRequest(context.Background(), prState); err != nil {
			t.Fatalf("tick %d: submitAsyncApprovalRequest returned error: %v", i, err)
		}
		if prState.Stage != StageAwaitApproval {
			t.Fatalf("tick %d: Stage = %v, want StageAwaitApproval", i, prState.Stage)
		}
	}
	if commentPosts != 1 {
		t.Errorf("comment POSTs = %d, want exactly 1 (deduped by Parked across ticks)", commentPosts)
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
