package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// legalPRStages is the set of every defined PRStage. The concurrent-driver test
// asserts the final Stage is one of these (i.e. never a torn/garbage value).
func legalPRStages() map[PRStage]bool {
	out := make(map[PRStage]bool)
	for _, s := range AllPRStages() {
		out[s] = true
	}
	return out
}

// TestController_PRStateRace_Concurrent is a -race regression test for TASK-324.
//
// It drives, concurrently and on the SAME tracked PR:
//   - a ProcessPR loop (the main state-machine leg: 11 handlers + inline writes +
//     persist, all under prState.mu),
//   - OnReviewRequested (webhook leg: writes prState.Stage),
//   - SetApprovalDecision (approval-callback leg: writes prState.ApprovalDecision),
//   - GetActivePRs (read-only consumers: snapshot under prState.mu).
//
// Before the fix these legs mutate/read *PRState fields from different goroutines
// with only c.mu guarding the map (never the struct), which `go test -race` flags
// as a data race. With the per-PR mutex in place the run is clean and the final
// Stage is always a legal value.
//
// NEGATIVE CHECK: commenting out the `prState.mu.Lock()` in ProcessPR makes this
// test report a DATA RACE under -race (verified during implementation).
func TestController_PRStateRace_Concurrent(t *testing.T) {
	// Permissive mock GitHub: GetPullRequest returns an open, clean (non-conflicting)
	// PR so the state machine keeps churning; reviews are empty; everything else 200.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls/42":
			mergeable := true
			resp := github.PullRequest{
				Number:         42,
				Title:          "feat: race target",
				State:          "open",
				Merged:         false,
				Mergeable:      &mergeable,
				MergeableState: "clean",
				HTMLURL:        "https://github.com/owner/repo/pull/42",
				Head:           github.PRRef{SHA: "abc1234", Ref: "pilot/GH-10"},
				Base:           github.PRRef{Ref: "main"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			// CI still in progress → keeps the PR in StageWaitingCI, mutating fields
			// (HeadSHA refresh, CIStatus, LastChecked) on each ProcessPR tick.
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "in_progress", Conclusion: ""},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
			// No reviews — hasChangesRequested / handleReviewRequested see an empty list.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			// AddLabels, RemoveLabel, UpdateIssueState, AddComment, ClosePullRequest,
			// DeleteBranch, merge, issue creation, etc. all succeed harmlessly.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	cfg := DefaultConfig()
	cfg.Environment = EnvProd // prod → approval path is reachable
	cfg.AutoReview = false
	cfg.CIPollInterval = time.Millisecond
	cfg.CIWaitTimeout = time.Hour // don't time out CI mid-test
	cfg.MaxFailures = 1 << 30     // keep the per-PR circuit breaker from tripping
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 1 << 30}

	mgr := approval.NewManager(&approval.Config{
		Enabled:        true,
		DefaultTimeout: time.Hour,
		DefaultAction:  approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       time.Hour,
			DefaultAction: approval.DecisionRejected,
		},
	})

	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	// Track exactly one PR; seed an approval request id so SetApprovalDecision has a
	// live target without depending on ordering with the submit path.
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	c.mu.RLock()
	seed := c.activePRs[42]
	c.mu.RUnlock()
	seed.mu.Lock()
	seed.ApprovalRequestID = "req-race-42"
	seed.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())

	const iterations = 400
	var wg sync.WaitGroup

	// Leg 1: ProcessPR loop (main state-machine writer).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// Ignore errors — the goal is concurrent field access, not a clean run.
			_ = c.ProcessPR(ctx, 42, nil)
		}
	}()

	// Leg 2: OnReviewRequested (webhook writer of prState.Stage).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c.OnReviewRequested(42, "submitted", "changes_requested", "human-reviewer")
		}
	}()

	// Leg 3: SetApprovalDecision (approval-callback writer of prState.ApprovalDecision).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = c.SetApprovalDecision(ctx, "req-race-42", string(approval.DecisionApproved), "human-approver")
		}
	}()

	// Leg 4: GetActivePRs (read-only snapshot consumer).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			for _, snap := range c.GetActivePRs() {
				_ = snap.Stage // read a field off the detached snapshot
			}
		}
	}()

	wg.Wait()
	cancel()

	// Final assertion: read via a detached snapshot (race-free) and verify the Stage
	// is a legal value (never torn). The PR may or may not still be tracked depending
	// on which terminal transition the ProcessPR loop landed on last.
	legal := legalPRStages()
	for _, snap := range c.GetActivePRs() {
		if snap.PRNumber != 42 {
			continue
		}
		if !legal[snap.Stage] {
			t.Fatalf("PR 42 ended in illegal/torn stage %q", snap.Stage)
		}
	}
}
