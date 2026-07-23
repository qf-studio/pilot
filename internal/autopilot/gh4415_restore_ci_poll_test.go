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

// TestController_RestoreState_WaitingCIPolling_GH4415 is the regression test
// for GH-4415: a waiting_ci PR that enters c.activePRs via RestoreState (a
// daemon restart) must be polled by ProcessPR exactly like a PR that entered
// via OnPRCreated in the same process — resolving from its already-completed
// check-runs on the very next tick, instead of sitting stuck until the 30m
// CIWaitTimeout elapses. Both cases below seed a fresh (non-expired)
// CIWaitStartedAt and leave the default 30m CIWaitTimeout in place: since the
// test completes in a handful of near-instant ProcessPR calls, reaching a
// terminal outcome at all — rather than looping in StageWaitingCI until this
// test's context/deadline gives up — is itself the proof that restore-time
// polling works and no timeout wait occurred.
func TestController_RestoreState_WaitingCIPolling_GH4415(t *testing.T) {
	t.Run("pre-completed passing check-runs transitions to released without a CI timeout", func(t *testing.T) {
		const owner, repo = "owner", "repo"
		const prNumber = 501
		const sha = "restoredpass01"

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/commits/restoredpass01/check-runs":
				resp := github.CheckRunsResponse{
					TotalCount: 1,
					CheckRuns: []github.CheckRun{
						{Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/501/files":
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]*github.PRFile{})
			case r.Method == http.MethodPut && r.URL.Path == "/repos/owner/repo/pulls/501/merge":
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"merged": true})
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/501/commits":
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]*github.Commit{makeCommit("feat: exercise restore-time release path")})
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/tags":
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]*github.Tag{})
			case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/git/refs":
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{})
			default:
				// releases/latest (no prior release), branches/main and compare
				// (reachability guard) are all fail-open on error — 404 exercises
				// those fallback paths rather than requiring explicit stubs.
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		store := newTestStateStore(t)

		seeded := &PRState{
			PRNumber:        prNumber,
			PRURL:           "https://github.com/owner/repo/pull/501",
			HeadSHA:         sha,
			Stage:           StageWaitingCI,
			CIStatus:        CIRunning,
			CIWaitStartedAt: time.Now(),
			CreatedAt:       time.Now(),
		}
		if err := store.SavePRState("owner/repo", seeded); err != nil {
			t.Fatalf("SavePRState failed: %v", err)
		}

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := &Config{
			Environment:    EnvDev,
			AutoMerge:      true,
			MergeMethod:    github.MergeMethodSquash,
			CIWaitTimeout:  30 * time.Minute,
			RequiredChecks: []string{"ci"},
			Release: &ReleaseConfig{
				Enabled:   true,
				Trigger:   "on_merge",
				TagPrefix: "v",
				Publish:   "tag_only",
				RequireCI: false,
			},
		}
		c := NewController(cfg, ghClient, nil, owner, repo)
		c.SetStateStore(store)

		restored, err := c.RestoreState()
		if err != nil {
			t.Fatalf("RestoreState failed: %v", err)
		}
		if restored != 1 {
			t.Fatalf("restored = %d, want 1", restored)
		}
		if _, ok := c.GetPRState(prNumber); !ok {
			t.Fatalf("PR %d not active after RestoreState", prNumber)
		}

		ghPR := &github.PullRequest{
			Number: prNumber,
			Title:  "feat: exercise restore-time release path",
			Head:   github.PRRef{SHA: sha},
			Base:   github.PRRef{Ref: "main"},
		}

		ctx := context.Background()
		const maxTicks = 10
		tick := 0
		for ; tick < maxTicks; tick++ {
			pr, ok := c.GetPRState(prNumber)
			if !ok {
				break // drained: released
			}
			if pr.Stage == StageFailed {
				t.Fatalf("PR reached StageFailed on tick %d (error=%q) — want it to release instead", tick, pr.Error)
			}
			if err := c.ProcessPR(ctx, prNumber, ghPR); err != nil {
				t.Fatalf("ProcessPR tick %d error = %v", tick, err)
			}
		}

		if _, stillTracked := c.GetPRState(prNumber); stillTracked {
			t.Fatalf("PR %d still tracked after %d ticks — expected it to drain (release) promptly, not stall waiting on CI", prNumber, maxTicks)
		}
	})

	t.Run("pre-failed check-runs transitions to failed immediately, not at CI timeout", func(t *testing.T) {
		const owner, repo = "owner", "repo"
		const prNumber = 502
		const sha = "restoredfail02"

		var issueCreated bool
		var prClosed bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/commits/restoredfail02/check-runs":
				resp := github.CheckRunsResponse{
					TotalCount: 1,
					CheckRuns: []github.CheckRun{
						{Name: "ci", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
				// findExistingFixIssue: no prior fix issue open.
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode([]*github.Issue{})
			case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues":
				issueCreated = true
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(github.Issue{Number: 9001, Title: "fix(ci): resolve CI failure from PR #502"})
			case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/pulls/502":
				prClosed = true
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		store := newTestStateStore(t)

		seeded := &PRState{
			PRNumber:        prNumber,
			PRURL:           "https://github.com/owner/repo/pull/502",
			HeadSHA:         sha,
			Stage:           StageWaitingCI,
			CIStatus:        CIRunning,
			CIWaitStartedAt: time.Now(),
			CreatedAt:       time.Now(),
		}
		if err := store.SavePRState("owner/repo", seeded); err != nil {
			t.Fatalf("SavePRState failed: %v", err)
		}

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := &Config{
			Environment:    EnvDev,
			AutoMerge:      true,
			MergeMethod:    github.MergeMethodSquash,
			CIWaitTimeout:  30 * time.Minute,
			RequiredChecks: []string{"ci"},
		}
		c := NewController(cfg, ghClient, nil, owner, repo)
		c.SetStateStore(store)

		restored, err := c.RestoreState()
		if err != nil {
			t.Fatalf("RestoreState failed: %v", err)
		}
		if restored != 1 {
			t.Fatalf("restored = %d, want 1", restored)
		}

		ghPR := &github.PullRequest{
			Number: prNumber,
			Title:  "fix: exercise restore-time CI-failure path",
			Head:   github.PRRef{SHA: sha},
			Base:   github.PRRef{Ref: "main"},
		}

		ctx := context.Background()
		const maxTicks = 5
		tick := 0
		for ; tick < maxTicks; tick++ {
			pr, ok := c.GetPRState(prNumber)
			if !ok {
				t.Fatalf("PR %d drained from tracking before reaching StageFailed", prNumber)
			}
			if pr.Stage == StageFailed {
				break
			}
			if err := c.ProcessPR(ctx, prNumber, ghPR); err != nil {
				t.Fatalf("ProcessPR tick %d error = %v", tick, err)
			}
		}

		pr, ok := c.GetPRState(prNumber)
		if !ok {
			t.Fatalf("PR %d not found in active PRs after processing", prNumber)
		}
		if pr.Stage != StageFailed {
			t.Fatalf("PR stage after %d ticks = %s, want %s (must fail immediately, not at the 30m CI timeout)", tick, pr.Stage, StageFailed)
		}
		if tick >= maxTicks {
			t.Fatalf("PR took %d ticks to reach StageFailed, want it within %d — resolution must come from the pre-failed check-runs, not a timeout loop", tick, maxTicks)
		}
		if !issueCreated {
			t.Error("expected a fix(ci) issue to be created for the CI failure")
		}
		if !prClosed {
			t.Error("expected the failed PR to be closed")
		}
	})
}
