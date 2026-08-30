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

// TestHasChangesRequested_ReAdoptionCutoff_TableDriven is GH-5266 (N1 from the
// PR#5264 review): hasChangesRequested used to filter reviews against
// prState.CreatedAt alone. Both PR registration paths — OnPRCreated (fresh
// registration) and the reconciler's orphan-PR sweep (re-adoption after a
// daemon restart or a missed executor callback) — set CreatedAt to
// time.Now(), so a standing CHANGES_REQUESTED review submitted before the
// restart looked "older than tracking" and the hold silently vanished the
// moment the PR was re-adopted. The fix anchors the cutoff on the PR's own
// GitHub creation time (ghPR.CreatedAt) instead, which survives restarts.
func TestHasChangesRequested_ReAdoptionCutoff_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		prCreatedAt    string // the PR's real, durable creation time on GitHub
		reviewAt       string // when the CHANGES_REQUESTED review was submitted
		freshPRState   bool   // simulate re-adoption: prState.CreatedAt = time.Now()
		ghPRFetchFails bool   // caller's GetPullRequest failed, so ghPR is nil
		wantHeld       bool
	}{
		{
			name:         "re-adoption: standing review predates fresh prState.CreatedAt but postdates real PR creation — still held",
			prCreatedAt:  "2026-08-01T00:00:00Z",
			reviewAt:     "2026-08-15T00:00:00Z",
			freshPRState: true,
			wantHeld:     true,
		},
		{
			name:         "no re-adoption: fresh registration behaves as before — review after creation holds",
			prCreatedAt:  "2026-08-29T00:00:00Z",
			reviewAt:     "2026-08-29T12:00:00Z",
			freshPRState: false,
			wantHeld:     true,
		},
		{
			name:         "no-permanent-park preserved: review predates the PR's own creation (cross-linked/moved review edge) — not held",
			prCreatedAt:  "2026-08-20T00:00:00Z",
			reviewAt:     "2026-08-01T00:00:00Z",
			freshPRState: true,
			wantHeld:     false,
		},
		{
			name:           "fail-open fallback: ghPR fetch failed (nil) — falls back to prState.CreatedAt, review after it holds",
			prCreatedAt:    "2026-08-01T00:00:00Z",
			reviewAt:       "2026-08-29T12:00:00Z",
			freshPRState:   false,
			ghPRFetchFails: true,
			wantHeld:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/pulls/42/reviews":
					resp := []*github.PullRequestReview{
						{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: tt.reviewAt},
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
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:  42,
				CreatedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), // stale value; only freshPRState below matters
			}
			if tt.freshPRState {
				// Simulate what the reconciler's re-adoption (or a fresh
				// OnPRCreated registration) actually does: stamp CreatedAt to
				// "now", long after the real GitHub PR creation date.
				prState.CreatedAt = time.Now()
			} else {
				parsed, err := time.Parse(time.RFC3339, tt.prCreatedAt)
				if err != nil {
					t.Fatalf("failed to parse prCreatedAt fixture: %v", err)
				}
				prState.CreatedAt = parsed
			}

			var ghPR *github.PullRequest
			if !tt.ghPRFetchFails {
				ghPR = &github.PullRequest{Number: 42, CreatedAt: tt.prCreatedAt}
			}

			got := c.hasChangesRequested(context.Background(), prState, ghPR)
			if got != tt.wantHeld {
				t.Errorf("hasChangesRequested() = %v, want %v", got, tt.wantHeld)
			}
		})
	}
}

// TestHasChangesRequested_CommentedDoesNotSupersede is GH-5266 (N2 from the
// PR#5264 review): the per-user latest-review map used to overwrite with
// whatever review came last chronologically, so a COMMENTED review from the
// same reviewer after their CHANGES_REQUESTED silently cleared the hold.
// GitHub's own review model does not treat COMMENTED as superseding a change
// request — only a fresh APPROVED or an explicit DISMISSED does.
func TestHasChangesRequested_CommentedDoesNotSupersede(t *testing.T) {
	baseTime := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	prCreatedAt := baseTime.Add(-24 * time.Hour).Format(time.RFC3339)

	tests := []struct {
		name     string
		reviews  []*github.PullRequestReview
		wantHeld bool
	}{
		{
			name: "CHANGES_REQUESTED then COMMENTED (same user) — still held",
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: baseTime.Add(1 * time.Hour).Format(time.RFC3339)},
				{ID: 2, User: github.User{Login: "reviewer"}, State: github.ReviewStateCommented, SubmittedAt: baseTime.Add(2 * time.Hour).Format(time.RFC3339)},
			},
			wantHeld: true,
		},
		{
			name: "CHANGES_REQUESTED, COMMENTED, then APPROVE (same user) — unparked",
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: baseTime.Add(1 * time.Hour).Format(time.RFC3339)},
				{ID: 2, User: github.User{Login: "reviewer"}, State: github.ReviewStateCommented, SubmittedAt: baseTime.Add(2 * time.Hour).Format(time.RFC3339)},
				{ID: 3, User: github.User{Login: "reviewer"}, State: github.ReviewStateApproved, SubmittedAt: baseTime.Add(3 * time.Hour).Format(time.RFC3339)},
			},
			wantHeld: false,
		},
		{
			name: "CHANGES_REQUESTED, COMMENTED, then DISMISSED (same user) — unparked",
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: baseTime.Add(1 * time.Hour).Format(time.RFC3339)},
				{ID: 2, User: github.User{Login: "reviewer"}, State: github.ReviewStateCommented, SubmittedAt: baseTime.Add(2 * time.Hour).Format(time.RFC3339)},
				{ID: 3, User: github.User{Login: "reviewer"}, State: github.ReviewStateDismissed, SubmittedAt: baseTime.Add(3 * time.Hour).Format(time.RFC3339)},
			},
			wantHeld: false,
		},
		{
			name: "COMMENTED only, no prior CHANGES_REQUESTED — never held",
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateCommented, SubmittedAt: baseTime.Add(1 * time.Hour).Format(time.RFC3339)},
			},
			wantHeld: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/pulls/42/reviews":
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(tt.reviews)
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 42, CreatedAt: baseTime.Add(-48 * time.Hour)}
			ghPR := &github.PullRequest{Number: 42, CreatedAt: prCreatedAt}

			got := c.hasChangesRequested(context.Background(), prState, ghPR)
			if got != tt.wantHeld {
				t.Errorf("hasChangesRequested() = %v, want %v", got, tt.wantHeld)
			}
		})
	}
}

// TestHandleMerging_ReAdoptedPR_StandingChangesRequestedHoldsMerge is the
// end-to-end counterpart to TestHasChangesRequested_ReAdoptionCutoff_TableDriven:
// it reproduces the exact GH-5266 scenario through ProcessPR/handleMerging —
// a PR that already has an outstanding CHANGES_REQUESTED review gets
// re-adopted with a fresh PRState (as the reconciler's orphan-PR sweep does
// after a daemon restart, or a fresh OnPRCreated registration would), and the
// merge must stay held rather than landing over the reviewer's objection.
func TestHandleMerging_ReAdoptedPR_StandingChangesRequestedHoldsMerge(t *testing.T) {
	var mergeAttempted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5266" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:    5266,
				Head:      github.PRRef{SHA: "sha5266"},
				Base:      github.PRRef{Ref: "main"},
				Draft:     false,
				CreatedAt: "2026-08-01T00:00:00Z", // real, durable PR creation time
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5266/reviews":
			// Standing review, submitted well before the (simulated) restart
			// but after the PR's real creation time.
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: "2026-08-15T00:00:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/commits/sha5266/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5266/merge" && r.Method == http.MethodPut:
			mergeAttempted = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"mergedSHA","merged":true,"message":"merged"}`))

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
	c.activePRs[5266] = &PRState{
		PRNumber:     5266,
		PRURL:        "https://github.com/owner/repo/pull/5266",
		IssueNumber:  70,
		BranchName:   "pilot/GH-70",
		HeadSHA:      "sha5266",
		Stage:        StageMerging,
		TargetBranch: "main",
		// Re-adoption/restart shape: CreatedAt stamped to "now", long after
		// both the PR's real creation and the standing review above.
		CreatedAt: time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 5266, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if mergeAttempted {
		t.Error("merge API must not be called — a standing CHANGES_REQUESTED review predates the re-adoption but postdates the PR's real creation")
	}
	pr, ok := c.GetPRState(5266)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageMerging || pr.MergeAttempts != 0 {
		t.Errorf("stage=%s attempts=%d, want stage=%s attempts=0 (held, not failed)", pr.Stage, pr.MergeAttempts, StageMerging)
	}
	if c.GetPRFailures(5266) != 0 {
		t.Errorf("PR failure count = %d, want 0 (a hold must not feed the per-PR circuit breaker)", c.GetPRFailures(5266))
	}
}
