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

// TestCIMonitor_AutoDiscoveryCompletion_GH4384 is the regression test for
// GH-4384: on qf-studio/pointer (zero required_checks, ci_checks.mode: auto,
// Actions-only CI), PRs #5/#6/#7 all timed out at waiting_ci after 30m with
// green checks. Root cause: GitHub Actions only ever writes check-runs, never
// legacy commit statuses, so the combined-status endpoint permanently reports
// state=pending/total_count=0 for such a repo — any code path that lets a
// completion check fall through to combined-status once check-runs already
// exist for the SHA will see phantom "pending" forever.
//
// Table-driven per the two required scenarios plus the hardening case this
// fix adds:
//  1. discovered check-runs are green + combined-status is pending/total=0 →
//     must conclude success from the check-runs conclusions, never look at
//     combined-status.
//  2. once check-runs have been discovered for a SHA, a later transient empty
//     check-runs response must not be mistaken for "no CI configured" and
//     fall through to combined-status.
//  3. inverse: a repo that genuinely has no check-runs, ever, and reports
//     exclusively via the legacy commit-status API, must still resolve via
//     combined-status (unaffected by this fix).
func TestCIMonitor_AutoDiscoveryCompletion_GH4384(t *testing.T) {
	t.Run("discovered check-runs green + combined-status pending/total=0 -> success", func(t *testing.T) {
		combinedStatusHits := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/qf-studio/pointer/commits/35e110af/check-runs":
				resp := github.CheckRunsResponse{
					TotalCount: 1,
					CheckRuns: []github.CheckRun{
						{Name: "go", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			case "/repos/qf-studio/pointer/commits/35e110af/status":
				combinedStatusHits++
				resp := github.CombinedStatus{State: github.StatusPending, TotalCount: 0, Statuses: []github.CommitStatus{}}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.RequiredChecks = nil
		cfg.CIChecks = &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 60 * time.Second,
		}
		monitor := NewCIMonitor(ghClient, "qf-studio", "pointer", cfg)

		status, err := monitor.CheckCI(context.Background(), "35e110af")
		if err != nil {
			t.Fatalf("CheckCI() error = %v", err)
		}
		if status != CISuccess {
			t.Errorf("CheckCI() = %s, want %s (discovered check-runs are green; must not time out)", status, CISuccess)
		}
		if combinedStatusHits != 0 {
			t.Errorf("combined-status endpoint was hit %d times, want 0 — completion must come from discovered check-runs only", combinedStatusHits)
		}
	})

	t.Run("previously discovered checks + later transient empty check-runs response stays pending", func(t *testing.T) {
		tick := 0
		combinedStatusHits := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/owner/repo/commits/abc1234/check-runs":
				tick++
				var resp github.CheckRunsResponse
				if tick == 1 {
					// First poll: check-run exists but is still running.
					resp = github.CheckRunsResponse{
						TotalCount: 1,
						CheckRuns:  []github.CheckRun{{Name: "go", Status: github.CheckRunInProgress}},
					}
				} else {
					// Second poll: a transient/eventually-consistent empty read from
					// GitHub even though the check-run was already discovered.
					resp = github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			case "/repos/owner/repo/commits/abc1234/status":
				combinedStatusHits++
				resp := github.CombinedStatus{State: github.StatusPending, TotalCount: 0, Statuses: []github.CommitStatus{}}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.RequiredChecks = nil
		cfg.CIChecks = &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 20 * time.Millisecond,
		}
		monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

		firstStatus, err := monitor.CheckCI(context.Background(), "abc1234")
		if err != nil {
			t.Fatalf("first CheckCI() error = %v", err)
		}
		if firstStatus != CIPending {
			t.Fatalf("first CheckCI() = %s, want %s (check-run still in_progress)", firstStatus, CIPending)
		}

		secondStatus, err := monitor.CheckCI(context.Background(), "abc1234")
		if err != nil {
			t.Fatalf("second CheckCI() error = %v", err)
		}
		if secondStatus != CIPending {
			t.Errorf("second CheckCI() (transient empty check-runs) = %s, want %s — must not fall through to combined-status", secondStatus, CIPending)
		}
		if combinedStatusHits != 0 {
			t.Errorf("combined-status endpoint was hit %d times, want 0 — a SHA with prior discovery must never consult it", combinedStatusHits)
		}
	})

	t.Run("inverse: repo with no check-runs ever, legacy statuses only, still resolves via combined-status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/repos/owner/repo/commits/def5678/check-runs":
				resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			case "/repos/owner/repo/commits/def5678/status":
				resp := github.CombinedStatus{
					State:      github.StatusSuccess,
					TotalCount: 1,
					Statuses:   []github.CommitStatus{{Context: "ci/circleci", State: github.StatusSuccess}},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer server.Close()

		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.RequiredChecks = nil
		cfg.CIChecks = &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 20 * time.Millisecond,
		}
		monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

		firstStatus, _ := monitor.CheckCI(context.Background(), "def5678")
		if firstStatus != CIPending {
			t.Fatalf("first CheckCI() = %s, want %s (grace period starting)", firstStatus, CIPending)
		}

		time.Sleep(30 * time.Millisecond)

		status, err := monitor.CheckCI(context.Background(), "def5678")
		if err != nil {
			t.Fatalf("CheckCI() error = %v", err)
		}
		if status != CISuccess {
			t.Errorf("CheckCI() = %s, want %s (legacy commit-status API must still gate CI for status-only providers)", status, CISuccess)
		}
	})
}

// TestController_ActionsOnlyRepo_AdvancesWaitingCIToCIPassed_GH4384 is the
// controller-level acceptance test for GH-4384: a repo with zero
// required_checks, ci_checks.mode: auto, and Actions-only CI (both the
// check-runs and legacy combined-status APIs faked) must advance
// waiting_ci -> ci_passed once its discovered check-runs complete, instead of
// timing out after 30m while `gh pr checks` shows green.
func TestController_ActionsOnlyRepo_AdvancesWaitingCIToCIPassed_GH4384(t *testing.T) {
	checkRunConclusion := "" // empty = still running
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/qf-studio/pointer/commits/35e110af/check-runs":
			run := github.CheckRun{Name: "go", Status: github.CheckRunInProgress}
			if checkRunConclusion != "" {
				run.Status = github.CheckRunCompleted
				run.Conclusion = checkRunConclusion
			}
			resp := github.CheckRunsResponse{TotalCount: 1, CheckRuns: []github.CheckRun{run}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/qf-studio/pointer/commits/35e110af/status":
			// GitHub Actions never writes legacy commit statuses: this repo has
			// zero statuses, so GitHub reports state=pending with total_count=0.
			resp := github.CombinedStatus{State: github.StatusPending, TotalCount: 0, Statuses: []github.CommitStatus{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{
		Mode:                 "auto",
		DiscoveryGracePeriod: 60 * time.Second,
	}

	c := NewController(cfg, ghClient, nil, "qf-studio", "pointer")

	ghPR := &github.PullRequest{
		Number: 7,
		Head:   github.PRRef{SHA: "35e110af"},
		Base:   github.PRRef{Ref: "main"},
	}

	c.mu.Lock()
	c.activePRs[7] = &PRState{
		PRNumber: 7,
		HeadSHA:  "35e110af",
		Stage:    StageWaitingCI,
	}
	c.mu.Unlock()

	ctx := context.Background()

	// First tick: check-run discovered but still running -> stays waiting_ci.
	if err := c.ProcessPR(ctx, 7, ghPR); err != nil {
		t.Fatalf("first ProcessPR (waiting_ci) error = %v", err)
	}
	pr, _ := c.GetPRState(7)
	if pr.Stage != StageWaitingCI {
		t.Fatalf("stage after first tick = %s, want %s", pr.Stage, StageWaitingCI)
	}
	if len(pr.DiscoveredChecks) == 0 {
		t.Fatalf("expected DiscoveredChecks to be populated after first tick, got none")
	}

	// The check-run completes green (mirrors PR #7 completing ~1 minute in).
	checkRunConclusion = github.ConclusionSuccess

	// Second tick: must resolve from the discovered check-run's conclusion,
	// not the legacy combined-status endpoint (which is permanently
	// pending/total_count=0 for this repo).
	if err := c.ProcessPR(ctx, 7, ghPR); err != nil {
		t.Fatalf("second ProcessPR (waiting_ci->ci_passed) error = %v", err)
	}
	pr, _ = c.GetPRState(7)
	if pr.Stage != StageCIPassed {
		t.Fatalf("stage after check-run completed = %s, want %s (must not be stuck behind pending combined-status)", pr.Stage, StageCIPassed)
	}
}
