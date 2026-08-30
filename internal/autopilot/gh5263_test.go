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

// TestHandleMerging_DraftReview_TableDriven reproduces the PR#5258 incident
// (2026-08-30): the merging stage never checked draft status or outstanding
// review state, so a draft PR (or one with an unresolved CHANGES_REQUESTED
// review) was merged blind — guaranteed to fail with GitHub 405 ("still a
// draft") in the draft case, or to actually land over a reviewer's objection
// in the non-draft changes-requested case. Both must now be held with zero
// merge attempts, zero attempted merge calls, and zero circuit-breaker feed.
func TestHandleMerging_DraftReview_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		draft    bool
		reviews  []*github.PullRequestReview
		wantHold bool
	}{
		{
			name:     "draft PR holds regardless of CI",
			draft:    true,
			reviews:  nil,
			wantHold: true,
		},
		{
			name:  "non-draft PR with outstanding changes-requested review holds",
			draft: false,
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: "2026-08-29T17:20:00Z"},
			},
			wantHold: true,
		},
		{
			name:  "PR#5258 shape: draft AND changes-requested AND green CI still holds, no attempt",
			draft: true,
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateChangesRequested, SubmittedAt: "2026-08-29T17:20:00Z"},
			},
			wantHold: true,
		},
		{
			name:  "non-draft PR with only an approval merges normally",
			draft: false,
			reviews: []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: github.ReviewStateApproved, SubmittedAt: "2026-08-29T17:20:00Z"},
			},
			wantHold: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mergeAttempted bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/pulls/5258" && r.Method == http.MethodGet:
					resp := github.PullRequest{
						Number: 5258,
						Head:   github.PRRef{SHA: "sha5258"},
						Base:   github.PRRef{Ref: "main"},
						Draft:  tt.draft,
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case r.URL.Path == "/repos/owner/repo/pulls/5258/reviews":
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(tt.reviews)

				case r.URL.Path == "/repos/owner/repo/commits/sha5258/check-runs":
					resp := github.CheckRunsResponse{
						TotalCount: 1,
						CheckRuns: []github.CheckRun{
							{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case r.URL.Path == "/repos/owner/repo/pulls/5258/merge" && r.Method == http.MethodPut:
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
			c.activePRs[5258] = &PRState{
				PRNumber:     5258,
				PRURL:        "https://github.com/owner/repo/pull/5258",
				IssueNumber:  61,
				BranchName:   "pilot/GH-61",
				HeadSHA:      "sha5258",
				Stage:        StageMerging,
				TargetBranch: "main",
				CreatedAt:    time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
			}
			c.mu.Unlock()

			if err := c.ProcessPR(context.Background(), 5258, nil); err != nil {
				t.Fatalf("ProcessPR returned error: %v", err)
			}

			pr, ok := c.GetPRState(5258)
			if !ok {
				t.Fatal("PR should still be tracked")
			}

			if tt.wantHold {
				if mergeAttempted {
					t.Error("merge API must not be called while held")
				}
				if pr.MergeAttempts != 0 {
					t.Errorf("MergeAttempts = %d, want 0 (held ticks must not burn the attempt budget)", pr.MergeAttempts)
				}
				if pr.Stage != StageMerging {
					t.Errorf("Stage = %s, want %s (held PR stays in merging, no error/backoff)", pr.Stage, StageMerging)
				}
				if c.GetPRFailures(5258) != 0 {
					t.Errorf("PR failure count = %d, want 0 (a hold must not feed the per-PR circuit breaker)", c.GetPRFailures(5258))
				}
				if c.IsPRCircuitOpen(5258) {
					t.Error("per-PR circuit breaker must not open on a draft/changes-requested hold")
				}
			} else {
				if !mergeAttempted {
					t.Error("merge API should have been called for a mergeable, approved PR")
				}
				if pr.Stage != StageMerged {
					t.Errorf("Stage = %s, want %s", pr.Stage, StageMerged)
				}
			}
		})
	}
}

// TestHandleMerging_DraftPR_ReadyResumesMergeOnNextTick verifies that a draft
// PR held at StageMerging (GH-5263) resumes and merges automatically on the
// very next tick once GitHub reports it as no longer a draft — no extra event
// plumbing, since draft state is a plain field re-read from GetPullRequest
// every tick.
func TestHandleMerging_DraftPR_ReadyResumesMergeOnNextTick(t *testing.T) {
	draft := true
	var mergeAttempted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5259" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number: 5259,
				Head:   github.PRRef{SHA: "sha5259"},
				Base:   github.PRRef{Ref: "main"},
				Draft:  draft,
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5259/reviews":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequestReview{})

		case r.URL.Path == "/repos/owner/repo/commits/sha5259/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5259/merge" && r.Method == http.MethodPut:
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
	c.activePRs[5259] = &PRState{
		PRNumber:     5259,
		PRURL:        "https://github.com/owner/repo/pull/5259",
		IssueNumber:  62,
		BranchName:   "pilot/GH-62",
		HeadSHA:      "sha5259",
		Stage:        StageMerging,
		TargetBranch: "main",
		CreatedAt:    time.Now(),
	}
	c.mu.Unlock()

	// Tick 1: still a draft — held, no attempt.
	if err := c.ProcessPR(context.Background(), 5259, nil); err != nil {
		t.Fatalf("ProcessPR (tick 1) returned error: %v", err)
	}
	if mergeAttempted {
		t.Fatal("merge must not be attempted while the PR is a draft")
	}
	pr, _ := c.GetPRState(5259)
	if pr.Stage != StageMerging || pr.MergeAttempts != 0 {
		t.Fatalf("after tick 1: stage=%s attempts=%d, want stage=%s attempts=0", pr.Stage, pr.MergeAttempts, StageMerging)
	}

	// Marked ready for review.
	draft = false

	// Tick 2: no longer a draft — merges.
	if err := c.ProcessPR(context.Background(), 5259, nil); err != nil {
		t.Fatalf("ProcessPR (tick 2) returned error: %v", err)
	}
	if !mergeAttempted {
		t.Error("merge should be attempted once the PR is marked ready")
	}
	pr, _ = c.GetPRState(5259)
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s after draft->ready transition", pr.Stage, StageMerged)
	}
}

// TestHandleMerging_ChangesRequested_DismissalResumesMerge verifies that a
// non-draft PR held for an outstanding CHANGES_REQUESTED review (GH-5263)
// resumes once the review is superseded — either a later APPROVE from the
// same reviewer or an explicit dismissal (GitHub flips the review's own
// State to DISMISSED in place; hasChangesRequested already treats that as
// "no longer the latest CHANGES_REQUESTED state" for that user).
func TestHandleMerging_ChangesRequested_DismissalResumesMerge(t *testing.T) {
	reviewState := github.ReviewStateChangesRequested
	var mergeAttempted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5260" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number: 5260,
				Head:   github.PRRef{SHA: "sha5260"},
				Base:   github.PRRef{Ref: "main"},
				Draft:  false,
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5260/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "reviewer"}, State: reviewState, SubmittedAt: "2026-08-29T17:20:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/commits/sha5260/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5260/merge" && r.Method == http.MethodPut:
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
	c.activePRs[5260] = &PRState{
		PRNumber:     5260,
		PRURL:        "https://github.com/owner/repo/pull/5260",
		IssueNumber:  63,
		BranchName:   "pilot/GH-63",
		HeadSHA:      "sha5260",
		Stage:        StageMerging,
		TargetBranch: "main",
		CreatedAt:    time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
	}
	c.mu.Unlock()

	// Tick 1: outstanding changes-requested review — held, no attempt.
	if err := c.ProcessPR(context.Background(), 5260, nil); err != nil {
		t.Fatalf("ProcessPR (tick 1) returned error: %v", err)
	}
	if mergeAttempted {
		t.Fatal("merge must not be attempted while a changes-requested review is outstanding")
	}
	pr, _ := c.GetPRState(5260)
	if pr.Stage != StageMerging || pr.MergeAttempts != 0 {
		t.Fatalf("after tick 1: stage=%s attempts=%d, want stage=%s attempts=0", pr.Stage, pr.MergeAttempts, StageMerging)
	}
	if c.GetPRFailures(5260) != 0 {
		t.Errorf("PR failure count = %d, want 0", c.GetPRFailures(5260))
	}

	// Reviewer dismisses their own changes-requested review.
	reviewState = github.ReviewStateDismissed

	// Tick 2: review no longer blocking — merges.
	if err := c.ProcessPR(context.Background(), 5260, nil); err != nil {
		t.Fatalf("ProcessPR (tick 2) returned error: %v", err)
	}
	if !mergeAttempted {
		t.Error("merge should be attempted once the changes-requested review is dismissed")
	}
	pr, _ = c.GetPRState(5260)
	if pr.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s after dismissal", pr.Stage, StageMerged)
	}
}

// TestHandleMerging_GenuineMergeFailure_StillFeedsBreaker verifies the
// GH-5263 fix does not weaken the existing failure path: a real merge
// failure (not draft, not changes-requested) still increments MergeAttempts
// and feeds the per-PR circuit breaker exactly as before.
func TestHandleMerging_GenuineMergeFailure_StillFeedsBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/5261" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:         5261,
				Head:           github.PRRef{SHA: "sha5261"},
				Base:           github.PRRef{Ref: "main"},
				Draft:          false,
				Mergeable:      boolPtr(true),
				MergeableState: "clean",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5261/reviews":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequestReview{})

		case r.URL.Path == "/repos/owner/repo/commits/sha5261/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/pulls/5261/merge" && r.Method == http.MethodPut:
			// Simulate a genuine, non-draft/non-review merge failure (e.g. a
			// transient API error) — not a conflict, so handleMergeConflict's
			// close path is not taken.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"internal error"}`))

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
	cfg.MaxMergeAttempts = 5

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.mu.Lock()
	c.activePRs[5261] = &PRState{
		PRNumber:     5261,
		PRURL:        "https://github.com/owner/repo/pull/5261",
		IssueNumber:  64,
		BranchName:   "pilot/GH-64",
		HeadSHA:      "sha5261",
		Stage:        StageMerging,
		TargetBranch: "main",
		CreatedAt:    time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 5261, nil); err == nil {
		t.Fatal("expected a genuine merge failure to return an error")
	}

	pr, _ := c.GetPRState(5261)
	if pr.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1 (a genuine failure must still count)", pr.MergeAttempts)
	}
	if c.GetPRFailures(5261) != 1 {
		t.Errorf("PR failure count = %d, want 1 (a genuine failure must still feed the breaker)", c.GetPRFailures(5261))
	}
}
