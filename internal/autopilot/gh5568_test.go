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

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestHandleMerging_LiveSmokeNotVerified_HoldsForHuman covers GH-5568: a PR
// whose "## Not verified" section names a key-gated live smoke must be held
// for a human (StageFailed via escalateAndHold), never merged on green.
func TestHandleMerging_LiveSmokeNotVerified_HoldsForHuman(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantHold bool
	}{
		{
			name:     "live smoke bullet with no reason holds",
			body:     "## Summary\n\nx\n\n## Not verified\n\n- Live smoke on four labelled excerpts\n",
			wantHold: true,
		},
		{
			name:     "unrelated not-verified bullet merges",
			body:     "## Not verified\n\n- **freeform mutation**\n  Reason: freeform mutation, run manually\n",
			wantHold: false,
		},
		{
			name:     "no not-verified section merges",
			body:     "## Summary\n\nlive smoke passed locally\n",
			wantHold: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mergeAttempted atomic.Bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/pulls/5561" && r.Method == http.MethodGet:
					_ = json.NewEncoder(w).Encode(github.PullRequest{
						Number: 5561,
						Head:   github.PRRef{SHA: "sha5561"},
						Base:   github.PRRef{Ref: "main"},
						Body:   tt.body,
					})
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
				default:
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.AutoMerge = true
			cfg.RequiredChecks = []string{"build"}

			c := NewController(cfg, github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), nil, "owner", "repo")
			c.mu.Lock()
			c.activePRs[5561] = &PRState{
				PRNumber:     5561,
				IssueNumber:  55,
				BranchName:   "pilot/GH-55",
				HeadSHA:      "sha5561",
				Stage:        StageMerging,
				TargetBranch: "main",
				CreatedAt:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			}
			c.mu.Unlock()

			if err := c.ProcessPR(context.Background(), 5561, nil); err != nil {
				t.Fatalf("ProcessPR returned error: %v", err)
			}
			pr, ok := c.GetPRState(5561)
			if !ok {
				t.Fatal("PR should still be tracked")
			}

			if tt.wantHold {
				if mergeAttempted.Load() {
					t.Error("merge API must not be called for a PR with a live-smoke Not-verified bullet")
				}
				if pr.Stage != StageFailed {
					t.Errorf("Stage = %s, want %s (needs-human hold)", pr.Stage, StageFailed)
				}
				if !strings.Contains(pr.Error, "Live smoke") {
					t.Errorf("hold reason should name the bullet, got %q", pr.Error)
				}
			} else {
				if !mergeAttempted.Load() {
					t.Error("merge API should have been called")
				}
				if pr.Stage != StageMerged {
					t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
				}
			}
		})
	}
}
