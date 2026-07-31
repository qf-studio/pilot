package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// TestCIMonitor_MismatchedGlobalRequiredChecks_GH4478 is the CIMonitor-level
// regression test for GH-4478: qf-studio/pointer#108 stayed at
// stage=waiting_ci, ci_status=pending for 4-6 minutes / ~10 poll cycles after
// all three of its check-runs ("integration", "go", "web") completed SUCCESS.
// Root cause: the production config's global required_checks: [test, lint]
// (tuned for the qf-studio/pilot repo's own CI job names) applied to every
// controller, including pointer's. checkRequiredChecks seeds a
// requiredStatus map from cfg.RequiredChecks and only flips an entry to
// success when a live check-run's Name matches one of those allowlisted
// names — a total name mismatch means aggregateStatus never observes
// anything but the CIPending zero-value, no matter how green the actual
// check-runs are.
//
// GH-4646: GH-4478 gave operators a way to fix this (WithCIChecksOverride)
// but did nothing for a project that hasn't been overridden yet — that
// project stayed silently stuck exactly as described above, which is what
// later produced the auth-service/studio-sdk incident (18 release-train
// scopes stuck or parked). checkRequiredChecks now detects this shape (every
// discovered check-run terminal, a required name never seen) and fails loudly
// with CIConfigMismatch instead of returning CIPending forever, so the first
// assertion below now documents the fix, not the bug.
func TestCIMonitor_MismatchedGlobalRequiredChecks_GH4478(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/qf-studio/pointer/commits/f671620/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "integration", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Name: "go", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Name: "web", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Global config, as deployed in production: required_checks tuned for a
	// different repo (qf-studio/pilot's own "test"/"lint" jobs).
	cfg := DefaultConfig()
	cfg.RequiredChecks = []string{"test", "lint"}

	monitor := NewCIMonitor(ghClient, "qf-studio", "pointer", cfg)

	status, err := monitor.CheckCI(context.Background(), "f671620")
	if err != nil {
		t.Fatalf("CheckCI() error = %v", err)
	}
	if status != CIConfigMismatch {
		t.Fatalf("CheckCI() = %s, want %s (GH-4646) — a global required_checks list with no matching check-run names, on a SHA whose actual check-runs are all green, is a config mismatch and must fail loudly rather than aggregate to pending forever", status, CIConfigMismatch)
	}

	// Now prove the fix: a per-project override with the correct check names
	// (or an empty list, falling through to auto-discovery) resolves the same
	// SHA to success.
	overrideCfg := *cfg
	overrideCfg.RequiredChecks = []string{"integration", "go", "web"}
	overriddenMonitor := NewCIMonitor(ghClient, "qf-studio", "pointer", &overrideCfg)

	status, err = overriddenMonitor.CheckCI(context.Background(), "f671620")
	if err != nil {
		t.Fatalf("overridden CheckCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("overridden CheckCI() = %s, want %s — a required_checks list matching this repo's actual check-run names must resolve to success", status, CISuccess)
	}
}

// TestController_ProjectCIChecksOverride_GH4478 is the controller-level
// acceptance test for GH-4478, in the style of
// TestController_ActionsOnlyRepo_AdvancesWaitingCIToCIPassed_GH4384. It
// builds two controllers sharing the SAME global *Config (mirroring
// cmd/pilot/main.go passing cfg.Orchestrator.Autopilot by pointer to every
// controller): one for the default repo the global required_checks list is
// tuned for, and one for a "pointer"-shaped repo whose check-run names never
// match that list. Without WithCIChecksOverride, the pointer controller's PR
// used to be stuck at waiting_ci forever despite green check-runs —
// reproducing the live pointer#108 stall; GH-4646 changed that outcome to a
// loud StageFailed instead (see the first sub-test below). With
// WithCIChecksOverride wired (the GH-4478 fix, mirroring the
// WithReleaseOverride/ProjectReleaseConfig pattern), it transitions to
// ci_passed within a single poll — unaffected by GH-4646 since there's no
// mismatch left to detect.
func TestController_ProjectCIChecksOverride_GH4478(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/qf-studio/pointer/commits/f671620/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "integration", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Name: "go", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Name: "web", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Single shared global Config, exactly as main.go threads
	// cfg.Orchestrator.Autopilot by pointer into every controller.
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"test", "lint"}

	ghPR := &github.PullRequest{
		Number: 108,
		Head:   github.PRRef{SHA: "f671620"},
		Base:   github.PRRef{Ref: "main"},
	}

	newPointerControllerState := func(c *Controller) {
		c.mu.Lock()
		c.activePRs[108] = &PRState{
			PRNumber: 108,
			HeadSHA:  "f671620",
			Stage:    StageWaitingCI,
		}
		c.mu.Unlock()
	}

	t.Run("without override: fails loudly instead of hanging at waiting_ci (GH-4646)", func(t *testing.T) {
		c := NewController(cfg, ghClient, nil, "qf-studio", "pointer")
		newPointerControllerState(c)

		ctx := context.Background()
		if err := c.ProcessPR(ctx, 108, ghPR); err != nil {
			t.Fatalf("ProcessPR error = %v", err)
		}
		pr, _ := c.GetPRState(108)
		if pr.Stage != StageFailed {
			t.Fatalf("stage = %s, want %s (GH-4646: a required_checks mismatch on an otherwise-green SHA must fail loudly, not hang at waiting_ci forever)", pr.Stage, StageFailed)
		}
	})

	t.Run("with WithCIChecksOverride: advances to ci_passed within one poll", func(t *testing.T) {
		override := &ProjectCIChecksOverride{RequiredChecks: []string{"integration", "go", "web"}}
		c := NewController(cfg, ghClient, nil, "qf-studio", "pointer", WithCIChecksOverride(override))
		newPointerControllerState(c)

		ctx := context.Background()
		if err := c.ProcessPR(ctx, 108, ghPR); err != nil {
			t.Fatalf("ProcessPR error = %v", err)
		}
		pr, _ := c.GetPRState(108)
		if pr.Stage != StageCIPassed {
			t.Fatalf("stage = %s, want %s — the per-project override must let this repo's own check-run names resolve CI status instead of the mismatched global required_checks list", pr.Stage, StageCIPassed)
		}
	})
}
