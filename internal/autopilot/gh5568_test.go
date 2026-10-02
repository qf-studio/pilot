package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// liveSmokeServer serves a PR 5561 whose body is body, green CI, no files, and
// records whether the merge API was called and whether any comment was posted.
func liveSmokeServer(t *testing.T, body string, mergeAttempted, commented *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5561" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(github.PullRequest{
				Number: 5561,
				Head:   github.PRRef{SHA: "sha5561"},
				Base:   github.PRRef{Ref: "main"},
				Body:   body,
			})
		case r.URL.Path == "/repos/owner/repo/pulls/5561/files":
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/pulls/5561/reviews":
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/commits/sha5561/check-runs":
			_ = json.NewEncoder(w).Encode(github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			})
		case r.URL.Path == "/repos/owner/repo/pulls/5561/merge" && r.Method == http.MethodPut:
			mergeAttempted.Store(true)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))
		case r.URL.Path == "/repos/owner/repo/issues/5561/comments" && r.Method == http.MethodPost:
			commented.Store(true)
			_, _ = w.Write([]byte("{}"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

func newLiveSmokeController(server *httptest.Server) *Controller {
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.AutoMerge = true
	cfg.RequiredChecks = []string{"build"}
	return NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
}

const liveSmokeBody = "## Summary\n\nx\n\n## Not verified\n\n- Live smoke on four labelled excerpts\n"

// TestHandleCIPassed_LiveSmokeNotVerified covers GH-5568/GH-5597: a PR whose
// "## Not verified" section names a key-gated live smoke is routed to
// StageAwaitApproval (never merged on green); unrelated bullets are not held.
func TestHandleCIPassed_LiveSmokeNotVerified(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantHold  bool
		wantStage PRStage
	}{
		{"live smoke bullet with no reason holds", liveSmokeBody, true, StageAwaitApproval},
		{
			"reason-only match holds",
			"## Not verified\n\n- **corpus run**\n  Reason: needs a real API key\n",
			true, StageAwaitApproval,
		},
		{
			"unrelated not-verified bullet proceeds",
			"## Not verified\n\n- **freeform mutation**\n  Reason: freeform mutation, run manually\n",
			false, StageMerging,
		},
		{"no not-verified section proceeds", "## Summary\n\nlive smoke passed locally\n", false, StageMerging},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mergeAttempted, commented atomic.Bool
			server := liveSmokeServer(t, tt.body, &mergeAttempted, &commented)
			defer server.Close()
			c := newLiveSmokeController(server)

			prState := &PRState{PRNumber: 5561, HeadSHA: "sha5561", Stage: StageCIPassed, TargetBranch: "main"}
			if err := c.handleCIPassed(context.Background(), prState); err != nil {
				t.Fatalf("handleCIPassed: %v", err)
			}
			if prState.Stage != tt.wantStage {
				t.Errorf("Stage = %s, want %s", prState.Stage, tt.wantStage)
			}
			if tt.wantHold {
				if !strings.Contains(prState.EscalationReason, "key-gated / live-service") {
					t.Errorf("EscalationReason = %q, want live-smoke reason", prState.EscalationReason)
				}
				if !commented.Load() {
					t.Error("hold comment should be posted")
				}
			} else if prState.EscalationReason != "" {
				t.Errorf("EscalationReason = %q, want empty", prState.EscalationReason)
			}
			if mergeAttempted.Load() {
				t.Error("handleCIPassed must not merge")
			}
		})
	}
}

// TestLiveSmokeHold_ApprovalReleases covers GH-5597: after the live-smoke hold
// parks the PR, a DecisionApproved advances it to StageMerging and
// handleMerging merges it rather than escalateAndHold-ing it to StageFailed.
func TestLiveSmokeHold_ApprovalReleases(t *testing.T) {
	var mergeAttempted, commented atomic.Bool
	server := liveSmokeServer(t, liveSmokeBody, &mergeAttempted, &commented)
	defer server.Close()
	c := newLiveSmokeController(server)

	prState := &PRState{
		PRNumber: 5561, IssueNumber: 55, BranchName: "pilot/GH-55", HeadSHA: "sha5561",
		Stage: StageCIPassed, TargetBranch: "main",
		CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed: %v", err)
	}
	if prState.Stage != StageAwaitApproval || prState.EscalationReason == "" {
		t.Fatalf("Stage = %s, EscalationReason = %q; want awaiting approval with a reason", prState.Stage, prState.EscalationReason)
	}

	prState.ApprovalRequestID = "req-1"
	prState.ApprovalDecision = string(approval.DecisionApproved)
	if err := c.handleAwaitApproval(context.Background(), prState); err != nil {
		t.Fatalf("handleAwaitApproval: %v", err)
	}
	if prState.Stage != StageMerging {
		t.Fatalf("Stage = %s, want %s after approval", prState.Stage, StageMerging)
	}

	c.mu.Lock()
	c.activePRs[5561] = prState
	c.mu.Unlock()
	if err := c.ProcessPR(context.Background(), 5561, nil); err != nil {
		t.Fatalf("ProcessPR: %v", err)
	}
	if prState.Stage == StageFailed {
		t.Fatalf("approved PR must not be failed by handleMerging, Error = %q", prState.Error)
	}
	if !mergeAttempted.Load() || prState.Stage != StageMerged {
		t.Errorf("approved PR should merge: attempted=%v stage=%s", mergeAttempted.Load(), prState.Stage)
	}
}

// TestLiveSmokeHoldComment_NoBodyEdit pins the hold comment to the real
// release path (approve), not the nonexistent "edit the PR body" one.
func TestLiveSmokeHoldComment_NoBodyEdit(t *testing.T) {
	var commentBody atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			var in struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			commentBody.Store(in.Body)
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()
	c := newLiveSmokeController(server)

	c.postLiveSmokeHoldComment(context.Background(), &PRState{PRNumber: 5561}, []string{"Live smoke on excerpts"})
	got, _ := commentBody.Load().(string)
	if got == "" {
		t.Fatal("no comment posted")
	}
	if strings.Contains(got, "edit the PR body") {
		t.Errorf("comment must not promise a body-edit release path: %q", got)
	}
	if !strings.Contains(got, "approve") {
		t.Errorf("comment should describe the approval release path: %q", got)
	}
}
