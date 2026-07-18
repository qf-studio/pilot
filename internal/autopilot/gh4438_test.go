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

// TestController_Run_PollsRestoredWaitingCIImmediately is the regression test
// for GH-4438: on daemon restart, a PR restored (via RestoreState, simulated
// here by seeding activePRs the same way RestoreState does) into
// StageWaitingCI whose CI already went terminal while the daemon was down
// must be rechecked immediately when Run's loop starts, not left to sit until
// the ticker's first tick.
//
// CIPollInterval is set to an hour so the test would time out waiting on the
// real fixture (a few hundred milliseconds) if Run() only relied on
// ticker.C — proving the fix calls processAllPRs eagerly before entering the
// select loop.
func TestController_Run_PollsRestoredWaitingCIImmediately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number: 42,
				State:  "open",
				Head:   github.PRRef{SHA: "deadbeef"},
				Base:   github.PRRef{Ref: "main"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/deadbeef/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/deadbeef/status":
			resp := github.CombinedStatus{State: github.StatusSuccess, TotalCount: 0, Statuses: []github.CommitStatus{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{Mode: "auto", DiscoveryGracePeriod: time.Millisecond}
	// Deliberately long: if Run() relied on the ticker's first tick, the PR
	// below would still be StageWaitingCI when the test's short deadline
	// below is reached.
	cfg.CIPollInterval = time.Hour
	cfg.MergedPRScanWindow = 24 * time.Hour

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Simulate what RestoreState leaves in activePRs after a daemon restart:
	// a PR already in StageWaitingCI whose CI resolved while the process was
	// down.
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:        42,
		HeadSHA:         "deadbeef",
		Stage:           StageWaitingCI,
		CIWaitStartedAt: time.Now().Add(-5 * time.Minute),
	}
	c.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pr, ok := c.GetPRState(42); ok && pr.Stage == StageCIPassed {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done
	pr, _ := c.GetPRState(42)
	t.Fatalf("PR #42 stage = %s after 2s, want %s — Run() did not poll restored waiting_ci PR immediately", pr.Stage, StageCIPassed)
}
