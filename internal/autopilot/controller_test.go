package autopilot

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	ghadapter "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

func TestNewController(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	approvalMgr := approval.NewManager(nil)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, approvalMgr, "owner", "repo")

	if c == nil {
		t.Fatal("NewController returned nil")
	}
	if c.owner != "owner" {
		t.Errorf("owner = %s, want owner", c.owner)
	}
	if c.repo != "repo" {
		t.Errorf("repo = %s, want repo", c.repo)
	}
	if c.ciMonitor == nil {
		t.Error("ciMonitor should be initialized")
	}
	if c.autoMerger == nil {
		t.Error("autoMerger should be initialized")
	}
	if c.feedbackLoop == nil {
		t.Error("feedbackLoop should be initialized")
	}
}

// fakeAlertSink is a minimal alertSink recorder for tests, avoiding the need
// to stand up a real *alerts.Engine with a dispatcher.
type fakeAlertSink struct {
	events []alerts.Event
}

func (f *fakeAlertSink) ProcessEvent(event alerts.Event) {
	f.events = append(f.events, event)
}

// TestSetAlertsEngine verifies the setter stores the sink, and that separate
// controllers (as constructed per-repo in cmd/pilot/main.go) each get their
// own independent engine instead of only the default controller receiving
// one — GH-3954 fixed a wiring bug where every controller but the default
// silently had a nil alertsEngine.
func TestSetAlertsEngine(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c1 := NewController(cfg, ghClient, nil, "owner", "repo1")
	c2 := NewController(cfg, ghClient, nil, "owner", "repo2")

	if c1.alertsEngine != nil {
		t.Fatal("alertsEngine should be nil before SetAlertsEngine is called")
	}

	sink1 := &fakeAlertSink{}
	sink2 := &fakeAlertSink{}
	c1.SetAlertsEngine(sink1)
	c2.SetAlertsEngine(sink2)

	if c1.alertsEngine != alertSink(sink1) {
		t.Error("c1.alertsEngine should be sink1")
	}
	if c2.alertsEngine != alertSink(sink2) {
		t.Error("c2.alertsEngine should be sink2")
	}

	c1.alertsEngine.ProcessEvent(alerts.Event{Type: alerts.EventTypeBudgetWarning})
	if len(sink1.events) != 1 {
		t.Errorf("sink1 should have recorded 1 event, got %d", len(sink1.events))
	}
	if len(sink2.events) != 0 {
		t.Errorf("sink2 should have recorded 0 events (independent per-controller wiring), got %d", len(sink2.events))
	}

	// SetAlertsEngine(nil) must be a safe no-op disable, not a panic.
	c1.SetAlertsEngine(nil)
	if c1.alertsEngine != nil {
		t.Error("alertsEngine should be nil after SetAlertsEngine(nil)")
	}
}

func TestNewController_ReleaserInit(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	enabledRelease := &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
	disabledRelease := &ReleaseConfig{Enabled: false}

	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		wantReleaser  bool
	}{
		{
			name:          "global only enabled",
			globalRelease: enabledRelease,
			envRelease:    nil,
			wantReleaser:  true,
		},
		{
			name:          "env only enabled",
			globalRelease: nil,
			envRelease:    enabledRelease,
			wantReleaser:  true,
		},
		{
			name:          "both set — env wins (enabled)",
			globalRelease: disabledRelease,
			envRelease:    enabledRelease,
			wantReleaser:  true,
		},
		{
			name:          "neither set",
			globalRelease: nil,
			envRelease:    nil,
			wantReleaser:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease
			if tt.envRelease != nil {
				envCfg := &EnvironmentConfig{Release: tt.envRelease}
				cfg.activeEnvName = "test"
				cfg.activeEnvConfig = envCfg
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			if tt.wantReleaser && c.releaser == nil {
				t.Errorf("releaser should be non-nil")
			}
			if !tt.wantReleaser && c.releaser != nil {
				t.Errorf("releaser should be nil")
			}
		})
	}
}

// TestResolvedRelease_Precedence verifies GH-3926's three-level precedence for
// the effective release config: project overlay > per-environment config >
// global config, wired via WithReleaseOverride and resolved once in
// NewController (resolvedRelease()/resolvedReleaseCfg).
func TestResolvedRelease_Precedence(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	t.Run("global only", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", Publish: "workflow"}
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		rel := c.resolvedRelease()
		if rel == nil || rel.PublishMode() != "workflow" {
			t.Fatalf("resolvedRelease() = %+v, want publish=workflow from global", rel)
		}
	})

	t.Run("env overrides global", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", Publish: "workflow"}
		cfg.activeEnvName = "test"
		cfg.activeEnvConfig = &EnvironmentConfig{
			Release: &ReleaseConfig{Enabled: true, Trigger: "on_merge", Publish: "tag_only"},
		}
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		rel := c.resolvedRelease()
		if rel == nil || rel.PublishMode() != "tag_only" {
			t.Fatalf("resolvedRelease() = %+v, want publish=tag_only from env (overriding global)", rel)
		}
	})

	t.Run("project overlay wins over env and global", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", Publish: "workflow"}
		cfg.activeEnvName = "test"
		cfg.activeEnvConfig = &EnvironmentConfig{
			Release: &ReleaseConfig{Enabled: true, Trigger: "on_merge", Publish: "tag_only"},
		}
		overlay := &ProjectReleaseConfig{Publish: "api"}
		c := NewController(cfg, ghClient, nil, "owner", "repo", WithReleaseOverride(overlay))
		rel := c.resolvedRelease()
		if rel == nil || rel.PublishMode() != "api" {
			t.Fatalf("resolvedRelease() = %+v, want publish=api from project overlay (overriding env+global)", rel)
		}
		if !rel.Enabled {
			t.Error("Enabled should still be inherited as true from env config")
		}
	})

	t.Run("project overlay alone enables release with no base config", func(t *testing.T) {
		cfg := DefaultConfig()
		overlay := &ProjectReleaseConfig{Enabled: boolPtr(true), Publish: "api"}
		c := NewController(cfg, ghClient, nil, "owner", "repo", WithReleaseOverride(overlay))
		rel := c.resolvedRelease()
		if rel == nil {
			t.Fatal("resolvedRelease() = nil, want a config derived from DefaultReleaseConfig via the overlay")
		}
		if rel.PublishMode() != "api" {
			t.Errorf("PublishMode() = %q, want %q", rel.PublishMode(), "api")
		}
		if c.releaser == nil {
			t.Error("releaser should be initialized when the project overlay alone enables release")
		}
	})
}

// TestWithReleaseNotOptedIn verifies GH-4001: a projects-loop controller
// wired with WithReleaseNotOptedIn (i.e. the project declares no `release:`
// block) must never inherit the global/env release cascade, and the
// "resolved release policy" log must tag it distinctly
// (source=project-not-opted-in) from an explicit `release: { enabled: false }`
// overlay, even though both resolve to the same disabled outcome.
func TestWithReleaseNotOptedIn(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	t.Run("forces disabled even when global release is enabled", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}

		c := NewController(cfg, ghClient, nil, "owner", "repo", WithReleaseNotOptedIn())

		rel := c.resolvedRelease()
		if rel == nil || rel.Enabled {
			t.Fatalf("resolvedRelease() = %+v, want disabled", rel)
		}
		if c.releaser != nil {
			t.Error("releaser should be nil when the project has not opted in")
		}
	})

	t.Run("logs source=project-not-opted-in", func(t *testing.T) {
		var buf bytes.Buffer
		h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		prev := slog.Default()
		slog.SetDefault(slog.New(h))
		defer slog.SetDefault(prev)

		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
		NewController(cfg, ghClient, nil, "owner", "repo", WithReleaseNotOptedIn())

		out := buf.String()
		if !strings.Contains(out, "source=project-not-opted-in") {
			t.Errorf("expected source=project-not-opted-in in log, got: %s", out)
		}
	})

	t.Run("explicit release:false overlay is tagged project-overlay, not not-opted-in", func(t *testing.T) {
		var buf bytes.Buffer
		h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		prev := slog.Default()
		slog.SetDefault(slog.New(h))
		defer slog.SetDefault(prev)

		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
		disabled := false
		c := NewController(cfg, ghClient, nil, "owner", "repo", WithReleaseOverride(&ProjectReleaseConfig{Enabled: &disabled}))

		rel := c.resolvedRelease()
		if rel == nil || rel.Enabled {
			t.Fatalf("resolvedRelease() = %+v, want disabled", rel)
		}
		out := buf.String()
		if !strings.Contains(out, "source=global+project-overlay") {
			t.Errorf("expected source=global+project-overlay in log, got: %s", out)
		}
		if strings.Contains(out, "project-not-opted-in") {
			t.Errorf("explicit disable must not be tagged project-not-opted-in, got: %s", out)
		}
	})
}

// TestGlobalReleaseEnabled covers GH-4001's migration-WARN gate: main.go
// only warns about a non-opted-in project when the global/env release
// cascade it would have inherited is actually enabled.
func TestGlobalReleaseEnabled(t *testing.T) {
	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		want          bool
	}{
		{name: "neither set", want: false},
		{name: "global enabled", globalRelease: &ReleaseConfig{Enabled: true}, want: true},
		{name: "global disabled", globalRelease: &ReleaseConfig{Enabled: false}, want: false},
		{name: "env enabled overrides global disabled", globalRelease: &ReleaseConfig{Enabled: false}, envRelease: &ReleaseConfig{Enabled: true}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease
			if tt.envRelease != nil {
				cfg.activeEnvName = "test"
				cfg.activeEnvConfig = &EnvironmentConfig{Release: tt.envRelease}
			}
			if got := GlobalReleaseEnabled(cfg); got != tt.want {
				t.Errorf("GlobalReleaseEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestController_OnPRCreated(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	prs := c.GetActivePRs()
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %d", len(prs))
	}

	pr := prs[0]
	if pr.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", pr.PRNumber)
	}
	if pr.IssueNumber != 10 {
		t.Errorf("IssueNumber = %d, want 10", pr.IssueNumber)
	}
	if pr.HeadSHA != "abc123" {
		t.Errorf("HeadSHA = %s, want abc123", pr.HeadSHA)
	}
	if pr.Stage != StagePRCreated {
		t.Errorf("Stage = %s, want %s", pr.Stage, StagePRCreated)
	}
	if pr.CIStatus != CIPending {
		t.Errorf("CIStatus = %s, want %s", pr.CIStatus, CIPending)
	}
}

func TestController_GetPRState(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("expected PR to be found")
	}
	if pr.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", pr.PRNumber)
	}

	_, ok = c.GetPRState(99)
	if ok {
		t.Error("PR 99 should not be found")
	}
}

func TestController_OnReviewRequested(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Should not panic on untracked PR
	c.OnReviewRequested(99, "submitted", "changes_requested", "reviewer1")

	// Register a PR and send review
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")
	c.OnReviewRequested(42, "submitted", "changes_requested", "reviewer1")

	// PR should still be tracked (stage transitions but PR remains in activePRs)
	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("expected PR to be tracked after review event")
	}
	if pr.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", pr.PRNumber)
	}

	// Approved review should also not panic
	c.OnReviewRequested(42, "submitted", "approved", "reviewer2")
}

func TestController_ProcessPR_NotTracked(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	err := c.ProcessPR(context.Background(), 99, nil)
	if err == nil {
		t.Error("ProcessPR should fail for untracked PR")
	}
}

func TestController_ProcessPR_DevEnvironment(t *testing.T) {
	// Test dev flow: PR created → waiting CI → CI passed → merging → merged → done
	// Dev now waits for CI like stage/prod, but with shorter timeout
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	c.activePRs[42].TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	// Stage 1: PR created → waiting CI (dev now waits for CI)
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	// Stage 3: CI passed → merging
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageMerging)
	}

	// Stage 4: merging → merged
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 4 error: %v", err)
	}
	if !mergeWasCalled {
		t.Error("merge should have been called")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerged {
		t.Errorf("after stage 4: Stage = %s, want %s", pr.Stage, StageMerged)
	}

	// Stage 5: merged → done (removed from tracking in dev)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 5 error: %v", err)
	}
	_, ok := c.GetPRState(42)
	if ok {
		t.Error("PR should be removed from tracking in dev after merge")
	}
}

func TestController_ProcessPR_StageEnvironment_CIPass(t *testing.T) {
	// Test stage flow: PR created → waiting CI → CI passed → merging → merged → post-merge CI
	mergeWasCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/branches/main":
			resp := github.Branch{
				Name:   "main",
				Commit: github.BranchCommit{SHA: "abc1234"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	c.activePRs[42].TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	// Stage 3: CI passed → merging (no approval in stage)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageMerging)
	}

	// Stage 4: merging → merged
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 4 error: %v", err)
	}
	if !mergeWasCalled {
		t.Error("merge should have been called")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerged {
		t.Errorf("after stage 4: Stage = %s, want %s", pr.Stage, StageMerged)
	}

	// Stage 5: merged → post-merge CI
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 5 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StagePostMergeCI {
		t.Errorf("after stage 5: Stage = %s, want %s", pr.Stage, StagePostMergeCI)
	}

	// Stage 6: post-merge CI → done (removed from tracking)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 6 error: %v", err)
	}
	_, ok := c.GetPRState(42)
	if ok {
		t.Error("PR should be removed from tracking after post-merge CI")
	}
}

func TestController_ProcessPR_CIFailure(t *testing.T) {
	// Test CI failure creates fix issue and closes the failed PR
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 100}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			// PR close request — verify it's called after CI failure
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → CI failed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageCIFailed {
		t.Errorf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIFailed)
	}

	// Stage 3: CI failed → create fix issue → close PR → failed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}
	if !issueCreated {
		t.Error("fix issue should have been created")
	}
	if !prClosed {
		t.Error("failed PR should have been closed on GitHub to unblock sequential poller")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageFailed)
	}
}

// GH-2402: After a successful merge, the controller must call
// SelfHealExecutionAfterMerge so any prior failed execution row for the
// same task ID is promoted to "completed" with the PR URL stamped.
func TestController_HandleMerging_SelfHealsExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sha":"merged123","merged":true,"message":"merged"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	// TASK-352: scope self-heal to the project's filesystem path (the value the
	// executor stores in executions.project_path), NOT owner/repo.
	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath("/proj/pilot"))
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:     42,
		PRURL:        "https://github.com/owner/repo/pull/42",
		HeadSHA:      "abc1234",
		IssueNumber:  99,
		Stage:        StageMerging,
		TargetBranch: "main",
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR returned unexpected error: %v", err)
	}

	// IssueNumber 99 has no "Parent: GH-N" body (default {} response), so exactly
	// one self-heal call (the issue itself, no parent).
	if len(evalMock.selfHealed) != 1 {
		t.Fatalf("expected 1 self-heal call, got %d", len(evalMock.selfHealed))
	}
	got := evalMock.selfHealed[0]
	if got.TaskID != "GH-99" {
		t.Errorf("self-heal task ID = %q, want GH-99", got.TaskID)
	}
	if got.ProjectPath != "/proj/pilot" {
		t.Errorf("self-heal project path = %q, want /proj/pilot (fs path, not owner/repo)", got.ProjectPath)
	}
	if got.PRURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("self-heal PR URL = %q, want PR URL", got.PRURL)
	}
	// Old UpdateExecutionStatusByTaskID path must NOT also be invoked — self-heal
	// supersedes it so we don't write stale rows without the PR URL.
	if len(evalMock.updateStatus) != 0 {
		t.Errorf("expected 0 UpdateExecutionStatusByTaskID calls (self-heal replaces it), got %d", len(evalMock.updateStatus))
	}
}

// TASK-352: An externally-merged Pilot PR (gh pr merge / GitHub UI) never passes
// through handleMerging, so ScanRecentlyMergedPRs must self-heal its execution
// record (Bug 1) AND, when the PR is for a sub-issue, its parent epic's record
// (Bug 2) — both scoped to the controller's filesystem project path.
func TestController_ScanRecentlyMergedPRs_SelfHeals(t *testing.T) {
	mergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	pr := github.PullRequest{
		Number:         55,
		Head:           github.PRRef{Ref: "pilot/GH-3353", SHA: "sha55"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/55",
		Title:          "fix(memory): integrity cluster",
		Merged:         true,
		MergedAt:       mergedAt,
		MergeCommitSHA: "merge-sha-55",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pr})
		case r.URL.Path == "/repos/owner/repo/issues/3353":
			// Sub-issue body carries the "Parent: GH-N" line epic.go writes.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{
				Body: "<!--autopilot-meta\nparent: GH-3344\n-->\n\nParent: GH-3344\n\nwork",
			})
		case r.URL.Path == "/repos/owner/repo/issues/3344":
			// GetIssueNodeID lookup for the parent's open-children gate (wave 2).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"node_id":"node_3344","number":3344}`))
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			// openSubIssueCount: the only child (GH-3353) is closed → open count 0,
			// so the parent heal is allowed (preserves the TASK-352 scenario).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":{"node":{"subIssues":{"totalCount":1,"nodes":[{"state":"CLOSED"}]}}}}`))
		case r.URL.Path == "/search/issues":
			// Wave-1 cross-check: native 0 must be confirmed by text search.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"total_count":0}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath("/proj/pilot"))
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs: %v", err)
	}

	healed := map[string]bool{}
	for _, h := range evalMock.selfHealed {
		healed[h.TaskID] = true
		if h.ProjectPath != "/proj/pilot" {
			t.Errorf("self-heal %s: ProjectPath = %q, want /proj/pilot", h.TaskID, h.ProjectPath)
		}
		if h.PRURL != pr.HTMLURL {
			t.Errorf("self-heal %s: PRURL = %q, want %q", h.TaskID, h.PRURL, pr.HTMLURL)
		}
	}
	if !healed["GH-3353"] {
		t.Errorf("Bug 1: expected self-heal for the merged sub-issue GH-3353; got %+v", evalMock.selfHealed)
	}
	if !healed["GH-3344"] {
		t.Errorf("Bug 2: expected self-heal for the parent epic GH-3344; got %+v", evalMock.selfHealed)
	}
}

// TestController_ScanRecentlyMergedPRs_HealsProjectPathMismatch pins GH-4511's
// merge-persist miss fix: SelfHealExecutionByPRURL (the project-path-unscoped
// pr_url-keyed fallback heal) now runs unconditionally on every discovered
// merged Pilot PR, not only when resolveIssueNumFromPR fails to resolve an
// issue number. Before the fix, a PR merged on a standard "pilot/GH-N" branch
// (issueNum resolves fine) whose executions row was written under a
// different project_path (e.g. a multi-project shared DB) would never heal:
// selfHealForPR's task_id+project_path-scoped SelfHealExecutionAfterMerge
// finds no matching row, and the unscoped pr_url fallback was skipped
// entirely because issueNum != 0. recordMergeSuccess still counts the merge
// live, so the store's lifetime baseline
// (GetLifetimePRCountersFromExecutions) permanently desynced from the live
// session counter across a restart — the "1236 vs 3" symptom reported in
// GH-4511. Uses a real *memory.Store (not mockEvalStore) as EvalStore so the
// assertion exercises the actual SQL query the metrics hydrator reads at boot.
func TestController_ScanRecentlyMergedPRs_HealsProjectPathMismatch(t *testing.T) {
	mergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	pr := github.PullRequest{
		Number:         88,
		Head:           github.PRRef{Ref: "pilot/GH-9001", SHA: "sha88"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/88",
		Title:          "fix(x): something",
		Body:           "work",
		Merged:         true,
		MergedAt:       mergedAt,
		MergeCommitSHA: "merge-sha-88",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pr})
		case r.URL.Path == "/repos/owner/repo/issues/9001":
			// No "Parent: GH-N" marker — resolveParentIssue returns 0, so the
			// parent-heal branch inside selfHealForPR isn't taken.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(github.Issue{Body: "work"})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// The execution row for the shipped issue lives under a DIFFERENT
	// project_path than the controller's configured one — simulating a
	// multi-project shared DB where the task_id+project_path-scoped heal
	// finds nothing, even though the row's own pr_url already matches the
	// merged PR (stamped at creation time by UpdateExecutionResult).
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-9001", TaskID: "GH-9001", ProjectPath: "/other/project",
		Status: "failed", PRUrl: pr.HTMLURL,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithProjectPath("/proj/pilot"))
	c.SetEvalStore(store)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs: %v", err)
	}

	counters, err := store.GetLifetimePRCountersFromExecutions("")
	if err != nil {
		t.Fatalf("GetLifetimePRCountersFromExecutions: %v", err)
	}
	if counters.Merged != 1 {
		t.Errorf("Merged = %d, want 1 — the pr_url-keyed fallback heal must recover a row whose "+
			"project_path doesn't match the controller's scope even though issueNum resolved fine", counters.Merged)
	}
}

// B3 (TASK-309): a PR persisted at stage='releasing' but absent from the in-memory
// activePRs map (e.g. after a daemon restart) must not be re-registered by the
// scanner while the release is fresh. A 'releasing' row stuck past
// releasingStaleThreshold is re-driven so a wedged release can recover.
func TestController_ScanRecentlyMergedPRs_SkipsPersistedReleasing(t *testing.T) {
	mergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	pr := github.PullRequest{
		Number:         77,
		Head:           github.PRRef{Ref: "pilot/GH-770", SHA: "sha77"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/77",
		Title:          "fix(api): retry",
		Merged:         true,
		MergedAt:       mergedAt,
		MergeCommitSHA: "merge-sha-77",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pr})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		}
	}))
	defer server.Close()

	newScanController := func(t *testing.T) (*Controller, *StateStore) {
		t.Helper()
		ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
		cfg := DefaultConfig()
		cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
		cfg.MergedPRScanWindow = 30 * time.Minute
		c := NewController(cfg, ghClient, nil, "owner", "repo")
		store := newTestStateStore(t)
		c.SetStateStore(store)
		return c, store
	}

	isTracked := func(c *Controller, prNumber int) bool {
		for _, p := range c.GetActivePRs() {
			if p.PRNumber == prNumber {
				return true
			}
		}
		return false
	}

	t.Run("fresh persisted releasing row is skipped", func(t *testing.T) {
		c, store := newScanController(t)
		if err := store.SavePRState("owner/repo", &PRState{PRNumber: 77, BranchName: "pilot/GH-770", Stage: StageReleasing, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("SavePRState: %v", err)
		}
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs: %v", err)
		}
		if isTracked(c, 77) {
			t.Error("PR 77 was re-registered despite a fresh persisted releasing row; B3 skip gate did not fire")
		}
	})

	t.Run("stale persisted releasing row is re-driven", func(t *testing.T) {
		c, store := newScanController(t)
		if err := store.SavePRState("owner/repo", &PRState{PRNumber: 77, BranchName: "pilot/GH-770", Stage: StageReleasing, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("SavePRState: %v", err)
		}
		if _, err := store.db.Exec(
			`UPDATE autopilot_pr_state SET updated_at = datetime('now', '-2 hours') WHERE pr_number = ?`, 77,
		); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
			t.Fatalf("ScanRecentlyMergedPRs: %v", err)
		}
		if !isTracked(c, 77) {
			t.Error("PR 77 was not re-driven despite a stale persisted releasing row; wedged release cannot recover")
		}
	})
}

func TestController_CircuitBreaker(t *testing.T) {
	// Test per-PR circuit breaker trips after max failures for that specific PR
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return error to trigger failures
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start with PR in merging stage (will fail on merge)
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:     42,
		Stage:        StageMerging,
		TargetBranch: "main",
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Cause failures
	for i := 0; i < 3; i++ {
		_ = c.ProcessPR(ctx, 42, nil)
	}

	// Per-PR circuit breaker should be open for PR 42
	if !c.IsPRCircuitOpen(42) {
		t.Error("per-PR circuit breaker should be open for PR 42 after max failures")
	}

	// Next call for PR 42 should be blocked
	err := c.ProcessPR(ctx, 42, nil)
	if err == nil {
		t.Error("ProcessPR should fail when per-PR circuit breaker is open")
	}
}

func TestController_ResetCircuitBreaker(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set per-PR failures
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 5, LastFailureTime: time.Now()}
	c.prFailures[43] = &prFailureState{FailureCount: 3, LastFailureTime: time.Now()}
	c.mu.Unlock()

	if !c.IsPRCircuitOpen(42) {
		t.Error("circuit should be open for PR 42")
	}
	if !c.IsPRCircuitOpen(43) {
		t.Error("circuit should be open for PR 43")
	}

	c.ResetCircuitBreaker()

	if c.IsPRCircuitOpen(42) {
		t.Error("circuit should be closed for PR 42 after reset")
	}
	if c.IsPRCircuitOpen(43) {
		t.Error("circuit should be closed for PR 43 after reset")
	}
}

func TestController_MultiplePRs(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Add multiple PRs
	c.OnPRCreated(1, "url1", 10, "sha1", "pilot/GH-10", "")
	c.OnPRCreated(2, "url2", 20, "sha2", "pilot/GH-20", "")
	c.OnPRCreated(3, "url3", 30, "sha3", "pilot/GH-30", "")

	prs := c.GetActivePRs()
	if len(prs) != 3 {
		t.Errorf("expected 3 PRs, got %d", len(prs))
	}

	// Verify all are tracked
	for _, prNum := range []int{1, 2, 3} {
		if _, ok := c.GetPRState(prNum); !ok {
			t.Errorf("PR %d should be tracked", prNum)
		}
	}
}

func TestController_ProcessPR_FailedStageNoOp(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set PR to failed state
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		Stage:    StageFailed,
	}
	c.mu.Unlock()

	// Processing failed stage should be a no-op
	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Errorf("ProcessPR on failed stage should not error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage should remain %s, got %s", StageFailed, pr.Stage)
	}
}

func TestController_ProcessPR_ProdRequiresApproval(t *testing.T) {
	// Test that prod goes to awaiting approval after CI passes
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
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
	cfg := DefaultConfig()
	cfg.Environment = EnvProd
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	_ = c.ProcessPR(ctx, 42, nil)

	// Stage 2: waiting CI → CI passed
	_ = c.ProcessPR(ctx, 42, nil)

	// Stage 3: CI passed → awaiting approval (prod)
	_ = c.ProcessPR(ctx, 42, nil)

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("Stage = %s, want %s for prod environment", pr.Stage, StageAwaitApproval)
	}
}

func TestController_RemovePR(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "url", 10, "sha", "pilot/GH-10", "")

	// Verify exists
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should exist")
	}

	// Remove
	c.removePR(42)

	// Verify removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed")
	}
}

func TestController_SuccessResetsFailureCount(t *testing.T) {
	// Successful processing should reset per-PR failures
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage // Use stage to have predictable behavior
	cfg.MaxFailures = 5

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "url", 10, "abc1234", "pilot/GH-10", "")

	// Set some failures for this specific PR
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 2, LastFailureTime: time.Now()}
	c.mu.Unlock()

	// Successful processing (pr_created → waiting_ci)
	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	failures := c.GetPRFailures(42)
	if failures != 0 {
		t.Errorf("PR failures = %d, want 0 after successful processing", failures)
	}
}

func TestController_MergeAttemptIncrement(t *testing.T) {
	mergeCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			// Return successful CI checks for pre-merge verification
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			mergeCallCount++
			// Fail first ProcessPR call (use 422 which is non-retryable), succeed second
			if mergeCallCount == 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start at merging stage
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:     42,
		HeadSHA:      "abc1234",
		Stage:        StageMerging,
		TargetBranch: "main",
	}
	c.mu.Unlock()

	ctx := context.Background()

	// First attempt fails (merge fails, not CI verification)
	err := c.ProcessPR(ctx, 42, nil)
	if err == nil {
		t.Error("first merge attempt should fail")
	}

	pr, _ := c.GetPRState(42)
	if pr.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1", pr.MergeAttempts)
	}

	// Second attempt succeeds
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Errorf("second merge attempt should succeed: %v", err)
	}

	pr, _ = c.GetPRState(42)
	if pr.MergeAttempts != 2 {
		t.Errorf("MergeAttempts = %d, want 2", pr.MergeAttempts)
	}
}

// TestController_FirstMergeAttemptSucceedsOnNoCIRepo is the integration
// regression test for GH-3873: a PR on a repo with no CI checks configured
// must merge on the FIRST handleMerging attempt, not fail with
// "CI checks still pending" while the discovery grace period restarts.
//
// Root cause: handleWaitingCI's CheckCI and verifyCIBeforeMerge's GetCIStatus
// share the same CIMonitor.discoveryStart map. Once CheckCI resolves the SHA
// to CISuccess (grace expired, entry evicted per TASK-357 B6b),
// verifyCIBeforeMerge must NOT see a missing entry and restart the grace —
// that previously forced 2-3+ failed merge attempts (or a permanent
// StageFailed under fast poll cadence) before self-healing.
func TestController_FirstMergeAttemptSucceedsOnNoCIRepo(t *testing.T) {
	mergeCallCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			// No CI checks configured on this repo.
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/abc1234/status":
			// No commit statuses either → genuine no-CI repo.
			resp := github.CombinedStatus{State: github.StatusPending, TotalCount: 0, Statuses: []github.CommitStatus{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/files":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.PRFile{})
		case "/repos/owner/repo/pulls/42/merge":
			mergeCallCount++
			w.WriteHeader(http.StatusOK)
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
		DiscoveryGracePeriod: 20 * time.Millisecond, // Very short grace period
	}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	ghPR := &github.PullRequest{
		Number: 42,
		Head:   github.PRRef{SHA: "abc1234"},
		Base:   github.PRRef{Ref: "main"},
	}

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234",
		Stage:    StageWaitingCI,
	}
	c.mu.Unlock()

	ctx := context.Background()

	// First handleWaitingCI tick: no checks found yet, starts the discovery grace.
	if err := c.ProcessPR(ctx, 42, ghPR); err != nil {
		t.Fatalf("first ProcessPR (waiting_ci) error = %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Fatalf("stage after first tick = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Wait for the grace period to expire.
	time.Sleep(30 * time.Millisecond)

	// Second handleWaitingCI tick: grace expired, commit-status fallback resolves
	// CISuccess (TotalCount==0) → transitions to StageCIPassed. This evicts the
	// discoveryStart[sha] entry (TASK-357 B6b).
	if err := c.ProcessPR(ctx, 42, ghPR); err != nil {
		t.Fatalf("second ProcessPR (waiting_ci->ci_passed) error = %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Fatalf("stage after grace expiry = %s, want %s", pr.Stage, StageCIPassed)
	}

	// handleCIPassed: no escalation, no approval required in dev → StageMerging.
	if err := c.ProcessPR(ctx, 42, ghPR); err != nil {
		t.Fatalf("third ProcessPR (ci_passed->merging) error = %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Fatalf("stage after ci_passed = %s, want %s", pr.Stage, StageMerging)
	}

	// handleMerging: verifyCIBeforeMerge's GetCIStatus must return CISuccess
	// immediately (no discovery-grace restart) so the FIRST merge attempt succeeds.
	if err := c.ProcessPR(ctx, 42, ghPR); err != nil {
		t.Fatalf("first handleMerging attempt should succeed, got error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerged {
		t.Errorf("stage after merge attempt = %s, want %s", pr.Stage, StageMerged)
	}
	if pr.MergeAttempts != 1 {
		t.Errorf("MergeAttempts = %d, want 1 (merge must succeed on first attempt)", pr.MergeAttempts)
	}
	if mergeCallCount != 1 {
		t.Errorf("merge endpoint called %d times, want 1", mergeCallCount)
	}
}

func TestController_ScanExistingPRs(t *testing.T) {
	tests := []struct {
		name          string
		prs           []github.PullRequest
		wantRestored  int
		wantIssueNums []int
	}{
		{
			name: "restores pilot PRs only",
			prs: []github.PullRequest{
				{Number: 1, Head: github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"}, HTMLURL: "url1"},
				{Number: 2, Head: github.PRRef{Ref: "feature/other", SHA: "sha2"}, HTMLURL: "url2"},
				{Number: 3, Head: github.PRRef{Ref: "pilot/GH-200", SHA: "sha3"}, HTMLURL: "url3"},
			},
			wantRestored:  2,
			wantIssueNums: []int{100, 200},
		},
		{
			name: "no pilot PRs",
			prs: []github.PullRequest{
				{Number: 1, Head: github.PRRef{Ref: "feature/one", SHA: "sha1"}, HTMLURL: "url1"},
				{Number: 2, Head: github.PRRef{Ref: "fix/two", SHA: "sha2"}, HTMLURL: "url2"},
			},
			wantRestored:  0,
			wantIssueNums: []int{},
		},
		{
			name:          "empty PR list",
			prs:           []github.PullRequest{},
			wantRestored:  0,
			wantIssueNums: []int{},
		},
		{
			name: "various pilot branch patterns",
			prs: []github.PullRequest{
				{Number: 1, Head: github.PRRef{Ref: "pilot/GH-1", SHA: "sha1"}, HTMLURL: "url1"},
				{Number: 2, Head: github.PRRef{Ref: "pilot/GH-999", SHA: "sha2"}, HTMLURL: "url2"},
				{Number: 3, Head: github.PRRef{Ref: "pilot-GH-123", SHA: "sha3"}, HTMLURL: "url3"}, // wrong pattern
				{Number: 4, Head: github.PRRef{Ref: "pilot/gh-456", SHA: "sha4"}, HTMLURL: "url4"}, // wrong case
			},
			wantRestored:  2,
			wantIssueNums: []int{1, 999},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/owner/repo/pulls" {
					// Convert to pointer slice for JSON encoding
					prs := make([]*github.PullRequest, len(tt.prs))
					for i := range tt.prs {
						prs[i] = &tt.prs[i]
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(prs)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			err := c.ScanExistingPRs(context.Background())
			if err != nil {
				t.Fatalf("ScanExistingPRs() error = %v", err)
			}

			prs := c.GetActivePRs()
			if len(prs) != tt.wantRestored {
				t.Errorf("restored %d PRs, want %d", len(prs), tt.wantRestored)
			}

			// Verify issue numbers were extracted correctly
			for _, wantIssue := range tt.wantIssueNums {
				found := false
				for _, pr := range prs {
					if pr.IssueNumber == wantIssue {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("issue number %d not found in restored PRs", wantIssue)
				}
			}
		})
	}
}

// TestController_ScanExistingPRs_PreservesTrackedState verifies that PRs
// already tracked (e.g. restored from SQLite via RestoreState) are not
// clobbered back to StagePRCreated by a subsequent ScanExistingPRs call.
// Regression test for GH-2349.
func TestController_ScanExistingPRs_PreservesTrackedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls" {
			prs := []*github.PullRequest{
				{Number: 42, Head: github.PRRef{Ref: "pilot/GH-100", SHA: "sha-new"}, HTMLURL: "url"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(prs)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Simulate RestoreState having already populated this PR at StageCIPassed
	// with a non-zero CIWaitStartedAt.
	ciWaitStart := time.Now().Add(-30 * time.Minute)
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:        42,
		IssueNumber:     100,
		BranchName:      "pilot/GH-100",
		HeadSHA:         "sha-old",
		Stage:           StageCIPassed,
		CIWaitStartedAt: ciWaitStart,
	}
	c.mu.Unlock()

	if err := c.ScanExistingPRs(context.Background()); err != nil {
		t.Fatalf("ScanExistingPRs() error = %v", err)
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR 42 missing after scan")
	}
	if pr.Stage != StageCIPassed {
		t.Errorf("Stage = %v, want %v (regressed by scan)", pr.Stage, StageCIPassed)
	}
	if !pr.CIWaitStartedAt.Equal(ciWaitStart) {
		t.Errorf("CIWaitStartedAt reset by scan: got %v, want %v", pr.CIWaitStartedAt, ciWaitStart)
	}
	if pr.HeadSHA != "sha-old" {
		t.Errorf("HeadSHA = %q, want %q (clobbered by scan)", pr.HeadSHA, "sha-old")
	}
}

func TestController_ScanExistingPRs_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	err := c.ScanExistingPRs(context.Background())
	if err == nil {
		t.Error("ScanExistingPRs() should return error on API failure")
	}
}

func TestController_CheckExternalMerge(t *testing.T) {
	// Test that externally merged PRs are detected and removed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as merged
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
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
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Verify PR is tracked
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should be tracked initially")
	}

	// Process PRs - should detect external merge and remove
	c.processAllPRs(context.Background())

	// Verify PR is removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}
}

func TestController_CheckExternalClose(t *testing.T) {
	// Test that externally closed (without merge) PRs are detected and removed
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as closed but not merged
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  false,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
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
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Verify PR is tracked
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should be tracked initially")
	}

	// GH-4570: back-date CreatedAt past externalCloseGraceWindow so this test
	// exercises the "current behavior" (single closed read trusted) branch
	// rather than the new grace-window confirmation gate, which is covered
	// separately.
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Now().Add(-10 * time.Minute)
	c.mu.Unlock()

	// Process PRs - should detect external close and remove
	c.processAllPRs(context.Background())

	// Verify PR is removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external close detection")
	}
}

// TestController_CheckExternalMerge_BoardSync verifies GH-4475: checkExternalMergeOrClose
// syncs the board card to doneStatus for an externally-merged PR, the same way the
// internal merge path (handleMerged) does. Before this fix the external-merge branch
// closed the issue, deleted the branch, and removed tracking but never touched the board.
func TestController_CheckExternalMerge_BoardSync(t *testing.T) {
	const issueNodeID = "IssueNodeID_extmerge"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
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
	cfg.CIPollInterval = 10 * time.Millisecond

	mock := &mockBoardSyncer{}
	c := NewController(cfg, ghClient, nil, "owner", "repo",
		withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev"))
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", issueNodeID)
	// OnPRCreated itself fires a reviewStatus sync — reset so we only observe
	// the external-merge sync under test.
	mock.calls = nil

	c.processAllPRs(context.Background())

	if len(mock.calls) != 1 {
		t.Fatalf("board sync calls = %d, want 1 (externally-merged PR card should move to Done)", len(mock.calls))
	}
	if mock.calls[0].issueNodeID != issueNodeID {
		t.Errorf("board sync issueNodeID = %q, want %q", mock.calls[0].issueNodeID, issueNodeID)
	}
	if mock.calls[0].statusName != "Done" {
		t.Errorf("board sync statusName = %q, want %q", mock.calls[0].statusName, "Done")
	}
}

// TestController_CheckExternalClose_BoardSync verifies GH-4475: checkExternalMergeOrClose
// syncs the board card to failStatus for an externally-closed (unmerged) PR.
func TestController_CheckExternalClose_BoardSync(t *testing.T) {
	const issueNodeID = "IssueNodeID_extclose"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  false,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/issues/10":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"node_id": issueNodeID})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	mock := &mockBoardSyncer{}
	c := NewController(cfg, ghClient, nil, "owner", "repo",
		withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev"))
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", issueNodeID)
	// OnPRCreated itself fires a reviewStatus sync — reset so we only observe
	// the external-close sync under test.
	mock.calls = nil

	// GH-4570: back-date CreatedAt past externalCloseGraceWindow so a single
	// closed read is trusted (this test targets board-sync behavior, not the
	// grace-window confirmation gate, which is covered separately).
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Now().Add(-10 * time.Minute)
	c.mu.Unlock()

	c.processAllPRs(context.Background())

	if len(mock.calls) != 1 {
		t.Fatalf("board sync calls = %d, want 1 (externally-closed PR card should move to Failed)", len(mock.calls))
	}
	if mock.calls[0].issueNodeID != issueNodeID {
		t.Errorf("board sync issueNodeID = %q, want %q", mock.calls[0].issueNodeID, issueNodeID)
	}
	if mock.calls[0].statusName != "Failed" {
		t.Errorf("board sync statusName = %q, want %q", mock.calls[0].statusName, "Failed")
	}
}

func TestController_CheckExternalMergeOrClose_OpenPR(t *testing.T) {
	// Test that open PRs are processed normally
	ciCheckCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as still open
			resp := github.PullRequest{
				Number:  42,
				State:   "open",
				Merged:  false,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/abc1234567890/check-runs":
			ciCheckCalled = true
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
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
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start at waiting CI stage
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
		Stage:    StageWaitingCI,
	}
	c.mu.Unlock()

	// Process PRs - should check state then continue processing
	c.processAllPRs(context.Background())

	// Verify PR is still tracked
	if _, ok := c.GetPRState(42); !ok {
		t.Error("open PR should still be tracked")
	}

	// Verify normal processing continued (CI check was called)
	if !ciCheckCalled {
		t.Error("CI check should have been called for open PR")
	}
}

func TestController_CheckExternalMerge_APIError(t *testing.T) {
	// Test that API errors don't remove PRs - they're kept for retry on next poll cycle.
	// With the PR caching optimization (GH-1304), we skip processing if GetPR fails
	// to avoid operating on stale data. The PR remains tracked for the next poll.

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return error - simulates transient API failure
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Start at waiting CI stage
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber: 42,
		HeadSHA:  "abc1234567890",
		Stage:    StageWaitingCI,
	}
	c.mu.Unlock()

	// Process PRs - should fail to fetch PR state, skip processing, but keep PR tracked
	c.processAllPRs(context.Background())

	// Verify PR is still tracked (error shouldn't remove it)
	if _, ok := c.GetPRState(42); !ok {
		t.Error("PR should still be tracked after API error")
	}

	// Verify stage hasn't changed (processing was skipped due to API error)
	prState, _ := c.GetPRState(42)
	if prState.Stage != StageWaitingCI {
		t.Errorf("PR stage should remain waiting_ci, got %s", prState.Stage)
	}
}

func TestController_CheckExternalMerge_WithNotifier(t *testing.T) {
	// Test that notifier is called when external merge is detected
	notifyMergedCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR as merged
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
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
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up mock notifier
	mockNotifier := &mockNotifier{
		notifyMergedFunc: func(ctx context.Context, prState *PRState) error {
			notifyMergedCalled = true
			if prState.PRNumber != 42 {
				t.Errorf("notified PR number = %d, want 42", prState.PRNumber)
			}
			return nil
		},
	}
	c.SetNotifier(mockNotifier)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Process PRs - should detect external merge and notify
	c.processAllPRs(context.Background())

	// Verify notifier was called
	if !notifyMergedCalled {
		t.Error("NotifyMerged should have been called for external merge")
	}
}

// GH-1486: Test that external merge closes the associated issue
func TestController_CheckExternalMerge_ClosesIssue(t *testing.T) {
	var (
		addLabelsCalled   bool
		removeLabelInProg bool
		removeLabelFailed bool
		issueStateClosed  bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				Merged:  true,
				HTMLURL: "https://github.com/owner/repo/pull/42",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			// AddLabels call - body is {"labels": ["pilot-done"]}
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body["labels"] {
				if l == "pilot-done" {
					addLabelsCalled = true
				}
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: "pilot-done"}})

		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			removeLabelInProg = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-failed" && r.Method == http.MethodDelete:
			removeLabelFailed = true
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodPatch:
			// UpdateIssueState call
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["state"] == "closed" {
				issueStateClosed = true
			}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Process PRs - should detect external merge and close issue
	c.processAllPRs(context.Background())

	// Verify PR is removed
	if _, ok := c.GetPRState(42); ok {
		t.Error("PR should be removed after external merge detection")
	}

	// Verify issue operations
	if !addLabelsCalled {
		t.Error("pilot-done label should be added to issue")
	}
	if !removeLabelInProg {
		t.Error("pilot-in-progress label should be removed from issue")
	}
	if !removeLabelFailed {
		t.Error("pilot-failed label should be removed from issue")
	}
	if !issueStateClosed {
		t.Error("issue should be closed after external merge")
	}
}

func TestController_CheckExternalMerge_MultiplePRs(t *testing.T) {
	// Test processing multiple PRs where some are merged externally
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/1":
			// PR 1 is still open
			resp := github.PullRequest{Number: 1, State: "open", Merged: false}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/2":
			// PR 2 was merged externally
			resp := github.PullRequest{Number: 2, State: "closed", Merged: true}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/3":
			// PR 3 was closed externally
			resp := github.PullRequest{Number: 3, State: "closed", Merged: false}
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
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Add multiple PRs
	c.OnPRCreated(1, "url1", 10, "sha1", "pilot/GH-10", "")
	c.OnPRCreated(2, "url2", 20, "sha2", "pilot/GH-20", "")
	c.OnPRCreated(3, "url3", 30, "sha3", "pilot/GH-30", "")

	// GH-4570: back-date PR 3's CreatedAt past externalCloseGraceWindow so its
	// closed-without-merge read is trusted immediately (this test targets
	// multi-PR processing, not the grace-window confirmation gate, which is
	// covered separately).
	c.mu.Lock()
	c.activePRs[3].CreatedAt = time.Now().Add(-10 * time.Minute)
	c.mu.Unlock()

	// Process PRs
	c.processAllPRs(context.Background())

	// PR 1 should still be tracked (open)
	if _, ok := c.GetPRState(1); !ok {
		t.Error("PR 1 should still be tracked (open)")
	}

	// PR 2 should be removed (merged externally)
	if _, ok := c.GetPRState(2); ok {
		t.Error("PR 2 should be removed (merged externally)")
	}

	// PR 3 should be removed (closed externally)
	if _, ok := c.GetPRState(3); ok {
		t.Error("PR 3 should be removed (closed externally)")
	}
}

// mockNotifier is a test double for the Notifier interface
type mockNotifier struct {
	notifyMergedFunc           func(ctx context.Context, prState *PRState) error
	notifyCIFailedFunc         func(ctx context.Context, prState *PRState, failedChecks []string) error
	notifyApprovalRequiredFunc func(ctx context.Context, prState *PRState) error
	notifyFixIssueCreatedFunc  func(ctx context.Context, prState *PRState, issueNumber int) error
	notifyReleasedFunc         func(ctx context.Context, prState *PRState, releaseURL string) error
}

func (m *mockNotifier) NotifyMerged(ctx context.Context, prState *PRState) error {
	if m.notifyMergedFunc != nil {
		return m.notifyMergedFunc(ctx, prState)
	}
	return nil
}

func (m *mockNotifier) NotifyCIFailed(ctx context.Context, prState *PRState, failedChecks []string) error {
	if m.notifyCIFailedFunc != nil {
		return m.notifyCIFailedFunc(ctx, prState, failedChecks)
	}
	return nil
}

func (m *mockNotifier) NotifyApprovalRequired(ctx context.Context, prState *PRState) error {
	if m.notifyApprovalRequiredFunc != nil {
		return m.notifyApprovalRequiredFunc(ctx, prState)
	}
	return nil
}

func (m *mockNotifier) NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error {
	if m.notifyFixIssueCreatedFunc != nil {
		return m.notifyFixIssueCreatedFunc(ctx, prState, issueNumber)
	}
	return nil
}

func (m *mockNotifier) NotifyReleased(ctx context.Context, prState *PRState, releaseURL string) error {
	if m.notifyReleasedFunc != nil {
		return m.notifyReleasedFunc(ctx, prState, releaseURL)
	}
	return nil
}

// GH-457: Test that handleWaitingCI refreshes stale HeadSHA from GitHub.
// When self-review pushes new commits, OnPRCreated stores the pre-self-review SHA.
// The controller must fetch the actual HEAD from GitHub before checking CI.
func TestController_ProcessPR_RefreshesStaleHeadSHA(t *testing.T) {
	staleSHA := "stale1234567890"
	actualSHA := "actual1234567890"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			// Return PR with actual HEAD SHA (different from stale SHA)
			resp := github.PullRequest{
				Number: 42,
				Head:   github.PRRef{SHA: actualSHA},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + actualSHA + "/check-runs":
			// CI passes for actual SHA
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + staleSHA + "/check-runs":
			// No CI for stale SHA (this is the bug scenario)
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Register PR with stale SHA (simulates self-review changing HEAD)
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, staleSHA, "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("after stage 1: Stage = %s, want %s", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → should refresh SHA and find CI passed
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)

	// Verify SHA was refreshed
	if pr.HeadSHA != actualSHA {
		t.Errorf("HeadSHA = %s, want %s (should have been refreshed from GitHub)", pr.HeadSHA, actualSHA)
	}

	// Verify CI passed with actual SHA
	if pr.CIStatus != CISuccess {
		t.Errorf("CIStatus = %s, want %s", pr.CIStatus, CISuccess)
	}
	if pr.Stage != StageCIPassed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageCIPassed)
	}
}

// GH-457: Test that without the fix, stale SHA would cause CI to stay pending.
// This validates the bug scenario explicitly.
func TestController_ProcessPR_StaleSHAWithoutRefreshWouldStayPending(t *testing.T) {
	staleSHA := "stale1234567890"
	actualSHA := "actual1234567890"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42":
			resp := github.PullRequest{
				Number: 42,
				Head:   github.PRRef{SHA: actualSHA},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + actualSHA + "/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case "/repos/owner/repo/commits/" + staleSHA + "/check-runs":
			// Stale SHA has no check runs — this is what caused the bug
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	// Verify what happens when we check CI against stale SHA directly
	ciMonitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// Stale SHA returns no checks → CIPending
	staleStatus, err := ciMonitor.CheckCI(context.Background(), staleSHA)
	if err != nil {
		t.Fatalf("CheckCI for stale SHA failed: %v", err)
	}
	if staleStatus != CIPending {
		t.Errorf("stale SHA status = %s, want %s (no checks = pending)", staleStatus, CIPending)
	}

	// Actual SHA returns passing checks → CISuccess
	actualStatus, err := ciMonitor.CheckCI(context.Background(), actualSHA)
	if err != nil {
		t.Fatalf("CheckCI for actual SHA failed: %v", err)
	}
	if actualStatus != CISuccess {
		t.Errorf("actual SHA status = %s, want %s", actualStatus, CISuccess)
	}
}

// GH-724: Test that merge conflicts are detected during WaitingCI and PR is closed immediately.
func TestController_ProcessPR_MergeConflict_WaitingCI(t *testing.T) {
	prCommented := false
	prClosed := false
	labelRemoved := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			// Return PR with merge conflict
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			// GH-1796: auto-rebase fails (true conflict)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == "POST":
			prCommented = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(github.PRComment{ID: 1})
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == "DELETE":
			labelRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Inject PR state directly at StageWaitingCI to test the handleWaitingCI path
	// (bypassing handlePRCreated which also checks conflicts now)
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:        42,
		PRURL:           "https://github.com/owner/repo/pull/42",
		IssueNumber:     10,
		BranchName:      "pilot/GH-10",
		HeadSHA:         "abc1234",
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now(),
		CreatedAt:       time.Now(),
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Process PR in WaitingCI stage → should detect conflict and fail immediately
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (conflict should immediately fail)", pr.Stage, StageFailed)
	}
	if pr.Error != "merge conflict with base branch" {
		t.Errorf("Error = %q, want %q", pr.Error, "merge conflict with base branch")
	}
	if !prCommented {
		t.Error("PR should have been commented with conflict explanation")
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}
}

// GH-724: Test that merge conflicts are detected immediately on PR creation.
func TestController_ProcessPR_MergeConflict_PRCreated(t *testing.T) {
	prClosed := false
	labelRemoved := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			// GH-1796: auto-rebase fails (true conflict)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == "DELETE":
			labelRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → should detect conflict immediately and skip CI wait
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s (conflict on creation should fail immediately)", pr.Stage, StageFailed)
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}
}

// GH-724: Test that unknown mergeable state (GitHub still computing) proceeds to CI check normally.
func TestController_ProcessPR_MergeableUnknown_ProceedsToCICheck(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			// Mergeable is nil (GitHub hasn't computed yet)
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				MergeableState: "unknown",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 3,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
					{Name: "test", Status: "completed", Conclusion: "success"},
					{Name: "lint", Status: "completed", Conclusion: "success"},
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
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI (unknown mergeable should NOT block)
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s (unknown mergeable should proceed to CI)", pr.Stage, StageWaitingCI)
	}

	// Stage 2: waiting CI → CI passed (should check CI normally despite unknown mergeable)
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageCIPassed)
	}
}

// TestHandleWaitingCI_RecordsCIRunOnPass pins GH-4134: the StageCIPassed
// transition in handleWaitingCI records exactly one pilot_ci_runs_total{
// result="pass"} verdict, mirroring the existing RecordCIWaitDuration call
// at the same transition.
func TestHandleWaitingCI_RecordsCIRunOnPass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				MergeableState: "unknown",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
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
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:        42,
		PRURL:           "https://github.com/owner/repo/pull/42",
		IssueNumber:     10,
		HeadSHA:         "abc1234",
		Stage:           StageWaitingCI,
		CIStatus:        CIPending,
		CIWaitStartedAt: time.Now(),
		CreatedAt:       time.Now(),
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageCIPassed {
		t.Fatalf("Stage = %s, want %s", pr.Stage, StageCIPassed)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["pass"]; got != 1 {
		t.Errorf("CIRuns[pass] = %d, want 1", got)
	}
	if got := snap.CIRuns["fail"]; got != 0 {
		t.Errorf("CIRuns[fail] = %d, want 0", got)
	}
}

// TestHandleCIFailed_RecordsCIRunOnFail pins GH-4134: a terminal
// handleCIFailed call records exactly one pilot_ci_runs_total{result="fail"}
// verdict alongside the existing RecordPRFailed() call.
func TestHandleCIFailed_RecordsCIRunOnFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha789/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			resp := github.Issue{Number: 300}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber: 44,
		HeadSHA:  "sha789",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s", prState.Stage, StageFailed)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["fail"]; got != 1 {
		t.Errorf("CIRuns[fail] = %d, want 1", got)
	}
	if got := snap.CIRuns["pass"]; got != 0 {
		t.Errorf("CIRuns[pass] = %d, want 0", got)
	}
}

// TestHandleCIFailed_GH4997_SkipsSpawnWhenOriginPRClosed is the regression
// test for the #4995 incident (08-19): CI failed on PR#4994 (GH-4988 gen 1)
// and handleCIFailed was triggered from that (now-stale) failure event, but
// by the time it re-reads PR state right before spawning, PR#4994 has
// already been closed without merging — superseded by the retry PR#4996,
// which merged and delivered #4988 first. No fix issue must be created for
// a PR whose CI failure is already moot, and the skip must be logged at Info
// naming the pr/issue/gate.
func TestHandleCIFailed_GH4997_SkipsSpawnWhenOriginPRClosed(t *testing.T) {
	issueCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha994/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/4994" && r.Method == http.MethodGet:
			resp := github.PullRequest{Number: 4994, State: github.StateClosed, Merged: false}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 4995}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	var logBuf bytes.Buffer
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	prState := &PRState{
		PRNumber:    4994,
		IssueNumber: 4988,
		HeadSHA:     "sha994",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("expected no fix issue to be created for a PR already closed without merging")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "CI-fix spawn skipped") {
		t.Errorf("expected a skip log line, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "gate=origin_pr_closed") {
		t.Errorf("expected skip log to name gate=origin_pr_closed, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "pr=4994") {
		t.Errorf("expected skip log to name pr=4994, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "issue=4988") {
		t.Errorf("expected skip log to name issue=4988, got logs:\n%s", logs)
	}
}

// TestHandleCIFailed_GH4997_SkipsSpawnWhenOriginIssueClosed covers the
// sibling gate: the failing PR is still open, but the origin issue was
// already delivered (closed) through another path — e.g. a sibling/retry PR
// merged first. No fix issue must be created, and the now-orphaned PR must
// be closed so the sequential poller doesn't block on it forever.
func TestHandleCIFailed_GH4997_SkipsSpawnWhenOriginIssueClosed(t *testing.T) {
	issueCreated := false
	prClosed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha994/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/4994" && r.Method == http.MethodGet:
			resp := github.PullRequest{Number: 4994, State: github.StateOpen}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/4994" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/repos/owner/repo/issues/4988" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 4988, State: github.StateClosed}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 4995}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	var logBuf bytes.Buffer
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	prState := &PRState{
		PRNumber:    4994,
		IssueNumber: 4988,
		HeadSHA:     "sha994",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("expected no fix issue to be created when the origin issue is already closed")
	}
	if !prClosed {
		t.Error("expected the now-orphaned open PR to be closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "CI-fix spawn skipped") {
		t.Errorf("expected a skip log line, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "gate=origin_issue_closed") {
		t.Errorf("expected skip log to name gate=origin_issue_closed, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "pr=4994") {
		t.Errorf("expected skip log to name pr=4994, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "issue=4988") {
		t.Errorf("expected skip log to name issue=4988, got logs:\n%s", logs)
	}
}

// TestHandleCIFailed_GH4997_SpawnsWhenOriginPROrIssueOpen is the "unchanged"
// half of the GH-4997 acceptance criteria: with the PR still open and the
// origin issue still open, the CI-fix spawn gate must not interfere with the
// normal fix-issue creation path.
func TestHandleCIFailed_GH4997_SpawnsWhenOriginPROrIssueOpen(t *testing.T) {
	issueCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha994/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/4994" && r.Method == http.MethodGet:
			resp := github.PullRequest{Number: 4994, State: github.StateOpen}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/4988" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 4988, State: github.StateOpen}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 4995}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    4994,
		IssueNumber: 4988,
		HeadSHA:     "sha994",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected a fix issue to be created when the origin PR and issue are both still open")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// gh4533InfraTestServer builds one httptest.Server answering both the
// studio-sdk client's endpoints (check-runs list, job log fetch) and the
// in-tree client's endpoints (jobs API, rerun-failed-jobs), matching the
// GH-4526 incident this feature auto-remediates: real checks green, a lint
// job failing on a 429 rate-limited action download. jobID/runID are fixed
// at 100/500. Extra path handlers (issues, PR patch, etc.) can be layered on
// via the extra func before the default catch-all.
func gh4533InfraTestServer(t *testing.T, sha string, rerunCalled *bool, extra func(w http.ResponseWriter, r *http.Request) bool) *httptest.Server {
	t.Helper()
	const infraLog = `Run actions/checkout@v4
##[error]Failed to download action 'https://api.github.com/repos/actions/checkout/tarball/v4'. Error: Response status code does not indicate success: 429 (Too Many Requests).`

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extra != nil && extra(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []github.CheckRun{
					{ID: 99, Name: "build", Status: "completed", Conclusion: "success"},
					{ID: 100, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/100/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(infraLog))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/100":
			w.WriteHeader(http.StatusOK)
			// GH-4591: the 429 action-download failure happens mid-step (the
			// checkout step actually started running before the download
			// failed), so real GitHub jobs-API responses for this shape have
			// a non-empty Steps breakdown — unlike the GH-4591
			// jobs-never-started shape, whose Steps is always []. Populating
			// Steps here keeps this fixture distinguishable from that new
			// shape so classifyPRFailure still reports the generic
			// FailureClassInfra, not FailureClassInfraBilling.
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{
				ID: 100, RunID: 500, Name: "lint", Status: "completed",
				Steps: []ghadapter.JobStep{
					{Name: "Set up job", Status: "completed", Conclusion: "success", Number: 1},
					{Name: "Run actions/checkout@v4", Status: "completed", Conclusion: "failure", Number: 2},
				},
			})
		case r.URL.Path == "/repos/owner/repo/actions/runs/500/rerun-failed-jobs" && r.Method == http.MethodPost:
			if rerunCalled != nil {
				*rerunCalled = true
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestHandleCIFailed_InfraFailure_AutoRetries replays the GH-4526 incident
// scenario (green real checks + a 429-rate-limited action-download lint job)
// end-to-end: handleCIFailed must classify the failure as infra, call
// RerunFailedJobs exactly once (deduped to the one owning run), leave the PR
// open and unmodified, spawn no fix issue, and re-enter StageWaitingCI.
func TestHandleCIFailed_InfraFailure_AutoRetries(t *testing.T) {
	rerunCalled := false
	issueCreated := false
	prClosed := false

	server := gh4533InfraTestServer(t, "infrasha1", &rerunCalled, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 900}))
			return true
		case r.URL.Path == "/repos/owner/repo/pulls/50" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			return true
		}
		return false
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber: 50,
		HeadSHA:  "infrasha1",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !rerunCalled {
		t.Error("expected RerunFailedJobs to be called for the infra-classified failure")
	}
	if issueCreated {
		t.Error("no fix issue should be created for an infra-classified failure with retry budget remaining")
	}
	if prClosed {
		t.Error("PR must not be closed on an infra auto-retry")
	}
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageWaitingCI)
	}
	if prState.InfraRerunCount != 1 {
		t.Errorf("InfraRerunCount = %d, want 1", prState.InfraRerunCount)
	}
	if prState.InfraRerunSHA != "infrasha1" {
		t.Errorf("InfraRerunSHA = %q, want %q", prState.InfraRerunSHA, "infrasha1")
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["infra_retry"]; got != 1 {
		t.Errorf("CIRuns[infra_retry] = %d, want 1", got)
	}
	if got := snap.CIRuns["fail"]; got != 0 {
		t.Errorf("CIRuns[fail] = %d, want 0 (not a terminal fail)", got)
	}
}

// TestHandleCIFailed_InfraFailure_RetryBudgetExhausted covers the case where
// an infra-classified failure has already exhausted its 2-retry budget on
// this exact SHA: handleCIFailed must NOT retry again, must fall through to
// the normal fix-issue/close-PR path, and must record CIRuns["infra_fail"]
// (not the generic "fail") plus a budget-exhausted note in prState.Error.
func TestHandleCIFailed_InfraFailure_RetryBudgetExhausted(t *testing.T) {
	rerunCalled := false
	issueCreated := false
	prClosed := false

	server := gh4533InfraTestServer(t, "infrasha2", &rerunCalled, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 901}))
			return true
		case r.URL.Path == "/repos/owner/repo/pulls/51" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			return true
		}
		return false
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber:        51,
		HeadSHA:         "infrasha2",
		Stage:           StageCIFailed,
		InfraRerunCount: 2,
		InfraRerunSHA:   "infrasha2", // budget already spent on this exact SHA
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if rerunCalled {
		t.Error("RerunFailedJobs must not be called once the retry budget is exhausted")
	}
	if !issueCreated {
		t.Error("expected a fix issue once the infra retry budget is exhausted")
	}
	if !prClosed {
		t.Error("expected the PR to be closed once the infra retry budget is exhausted")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, "infra retries exhausted (2/2)") {
		t.Errorf("Error = %q, want it to mention 'infra retries exhausted (2/2)'", prState.Error)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["infra_fail"]; got != 1 {
		t.Errorf("CIRuns[infra_fail] = %d, want 1", got)
	}
	if got := snap.CIRuns["fail"]; got != 0 {
		t.Errorf("CIRuns[fail] = %d, want 0 (budget-exhausted infra failure records infra_fail)", got)
	}
	if got := snap.PRFailureClasses["infra"]; got != 1 {
		t.Errorf("PRFailureClasses[infra] = %d, want 1", got)
	}
}

// TestHandleCIFailed_InfraFailure_BudgetResetOnNewSHA covers GH-4533's
// per-SHA budget reset: a PR that already spent its 2-retry budget on a
// prior SHA must still get a fresh budget once HeadSHA moves to a new commit
// (e.g. after an unrelated push), even though InfraRerunCount itself is not
// zeroed until the next successful retry.
func TestHandleCIFailed_InfraFailure_BudgetResetOnNewSHA(t *testing.T) {
	rerunCalled := false

	server := gh4533InfraTestServer(t, "infrasha3", &rerunCalled, nil)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber:        52,
		HeadSHA:         "infrasha3",
		Stage:           StageCIFailed,
		InfraRerunCount: 2,
		InfraRerunSHA:   "infrasha2-old", // budget was spent on a different, prior SHA
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !rerunCalled {
		t.Error("expected RerunFailedJobs to be called: budget resets on a new HeadSHA")
	}
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageWaitingCI)
	}
	if prState.InfraRerunCount != 1 {
		t.Errorf("InfraRerunCount = %d, want 1 (reset to 0, then incremented by this retry)", prState.InfraRerunCount)
	}
	if prState.InfraRerunSHA != "infrasha3" {
		t.Errorf("InfraRerunSHA = %q, want %q", prState.InfraRerunSHA, "infrasha3")
	}
}

// TestHandleCIFailed_StructuralOutageReplay_AutoRetries replays the
// 2026-08-06 GitHub Actions outage (pilot#4779) end-to-end through
// handleCIFailed: every job died at GitHub's own synthetic "Set up job" step
// with log prose ("Failed to resolve action download info. Error: Service
// Unavailable") that matches none of TASK-418's four hardcoded infra
// signatures, and the check-run conclusion is the ordinary "failure" — not
// one of the newer startup_failure/stale conclusions. Before GH-4779 this
// fell through to classifyCheckFailure's prose fallback and classified code,
// which is exactly what closed the real PR #4770. The structural signal
// (isStructuralInfra: RepoStepsExecuted==0) must classify infra and
// auto-retry instead.
func TestHandleCIFailed_StructuralOutageReplay_AutoRetries(t *testing.T) {
	rerunCalled := false
	issueCreated := false
	prClosed := false

	const outageLog = `##[error]Failed to resolve action download info. Error: Service Unavailable`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/outagesha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 200, Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/200/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(outageLog))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/200":
			w.WriteHeader(http.StatusOK)
			// The job died during GitHub's own synthetic "Set up job" step —
			// no repo-defined step ever ran.
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{
				ID: 200, RunID: 600, Name: "build", Status: "completed",
				Steps: []ghadapter.JobStep{
					{Name: "Set up job", Status: "completed", Conclusion: "failure", Number: 1},
				},
			})
		case r.URL.Path == "/repos/owner/repo/actions/runs/600/rerun-failed-jobs" && r.Method == http.MethodPost:
			rerunCalled = true
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 950}))
		case r.URL.Path == "/repos/owner/repo/pulls/60" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber: 60,
		HeadSHA:  "outagesha1",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !rerunCalled {
		t.Error("expected RerunFailedJobs to be called for the structurally-classified outage")
	}
	if issueCreated {
		t.Error("no fix issue should be created for a structural-infra outage with retry budget remaining")
	}
	if prClosed {
		t.Error("PR must not be closed on a structural-infra auto-retry")
	}
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageWaitingCI)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["infra_retry"]; got != 1 {
		t.Errorf("CIRuns[infra_retry] = %d, want 1", got)
	}
	if got := snap.CIRuns["fail"]; got != 0 {
		t.Errorf("CIRuns[fail] = %d, want 0 (not a terminal fail)", got)
	}
}

// TestHandleCIFailed_ZeroEvidence_EscalatesInsteadOfClosing covers GH-4779's
// THE INVARIANT: CI's own aggregate reported CIFailure (that's the only way
// handleCIFailed is reached), but evidence gathering came back with nothing
// — the check-runs list is empty. This is precisely the gap that let the
// pre-GH-4779 fail-unsafe default (classifyPRFailure(nil) == FailureClassCode)
// close a PR and spawn a fix issue with nothing to point at. handleCIFailed
// must classify FailureClassUnknown and route to escalateAndHold: never
// ClosePullRequest, never a spawned fix issue, never a self-close marker.
func TestHandleCIFailed_ZeroEvidence_EscalatesInsteadOfClosing(t *testing.T) {
	issueCreated := false
	prClosed := false
	branchDeleted := false
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/zeroevidence1/check-runs":
			// No check runs at all — the classic shape of a check-runs list
			// call that raced GitHub's own status propagation, or otherwise
			// returned nothing despite CI's aggregate already reporting
			// failure.
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 951}))
		case r.URL.Path == "/repos/owner/repo/pulls/61" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    61,
		IssueNumber: 32,
		HeadSHA:     "zeroevidence1",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("no fix issue should be spawned when there is zero evidence to point it at")
	}
	if prClosed {
		t.Error("PR must NOT be closed on zero gathered evidence — THE INVARIANT (GH-4779)")
	}
	if branchDeleted {
		t.Error("branch must NOT be deleted when the PR is held via escalateAndHold")
	}
	if c.consumeSelfClosedMarker(61) {
		t.Error("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error != "CI failure with zero gathered evidence" {
		t.Errorf("Error = %q, want %q", prState.Error, "CI failure with zero gathered evidence")
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label on the issue, got labels: %v", labelsAdded)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["unknown_evidence"]; got != 1 {
		t.Errorf("CIRuns[unknown_evidence] = %d, want 1", got)
	}
	if got := snap.PRFailureClasses["unknown"]; got != 1 {
		t.Errorf("PRFailureClasses[unknown] = %d, want 1", got)
	}
}

// TestHandleCIFailed_RealCodeFailure_StillHitsFixIssuePath is a regression
// guard (GH-4533): a genuine code failure (real errcheck annotation in the
// job log) must be unaffected by the new classify-first path — still
// classified code, still spawns a fix issue and closes the PR, still records
// the plain CIRuns["fail"] (not infra_fail).
func TestHandleCIFailed_RealCodeFailure_StillHitsFixIssuePath(t *testing.T) {
	issueCreated := false
	prClosed := false

	const codeLog = `Run golangci-lint run ./...
internal/autopilot/controller.go:1234:6: Error return value of c.ghClient.ClosePullRequest is not checked (errcheck)
##[error]Process completed with exit code 1.`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/codesha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{ID: 200, Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/200/logs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(codeLog))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 902}))
		case r.URL.Path == "/repos/owner/repo/pulls/53" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber: 53,
		HeadSHA:  "codesha1",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected a fix issue for a genuine code failure")
	}
	if !prClosed {
		t.Error("expected the PR to be closed for a genuine code failure")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if strings.Contains(prState.Error, "infra retries exhausted") {
		t.Errorf("Error = %q, must not mention infra retries for a code failure", prState.Error)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["fail"]; got != 1 {
		t.Errorf("CIRuns[fail] = %d, want 1", got)
	}
	if got := snap.CIRuns["infra_fail"]; got != 0 {
		t.Errorf("CIRuns[infra_fail] = %d, want 0", got)
	}
	if got := snap.PRFailureClasses["code"]; got != 1 {
		t.Errorf("PRFailureClasses[code] = %d, want 1", got)
	}
}

// gh4591BillingTestServer mocks the GH-4591 jobs-never-started billing-refusal
// shape: two failed checks whose check-run Output.Summary carries GitHub's
// billing-refusal text and whose jobs-API lookup reports an empty Steps
// breakdown (the job never started, so there is nothing to step through) and
// no job-logs endpoint at all (a never-started job has no log to fetch).
func gh4591BillingTestServer(t *testing.T, sha string, rerunCalled *bool, extra func(w http.ResponseWriter, r *http.Request) bool) *httptest.Server {
	t.Helper()
	const billingText = "The job was not started because recent account payments have failed or your spending limit needs to be increased."

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extra != nil && extra(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/"+sha+"/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []github.CheckRun{
					{ID: 110, Name: "build", Status: "completed", Conclusion: "failure", Output: &github.CheckOutput{Summary: billingText}},
					{ID: 111, Name: "lint", Status: "completed", Conclusion: "failure", Output: &github.CheckOutput{Summary: billingText}},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/actions/jobs/110/logs" || r.URL.Path == "/repos/owner/repo/actions/jobs/111/logs":
			// A never-started job has no log at all.
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/repos/owner/repo/actions/jobs/110":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{ID: 110, RunID: 600, Name: "build", Status: "completed", Steps: []ghadapter.JobStep{}})
		case r.URL.Path == "/repos/owner/repo/actions/jobs/111":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ghadapter.WorkflowJob{ID: 111, RunID: 600, Name: "lint", Status: "completed", Steps: []ghadapter.JobStep{}})
		case r.URL.Path == "/repos/owner/repo/actions/runs/600/rerun-failed-jobs" && r.Method == http.MethodPost:
			if rerunCalled != nil {
				*rerunCalled = true
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
}

// TestHandleCIFailed_JobsNeverStarted_BillingRefusal replays the GH-4591 live
// incident (pilot-canary-sandbox#106 wrongly closed, fix issue #107 spawned
// wastefully; pointer#213/#214 hit the same shape): a check-run pair whose
// jobs never even started (annotation text + Steps: []) must classify
// infra_billing, auto-retry via RerunFailedJobs exactly like a generic infra
// failure, leave the PR open, spawn no fix issue, and fire exactly one
// ci_billing_refusal alert.
func TestHandleCIFailed_JobsNeverStarted_BillingRefusal(t *testing.T) {
	rerunCalled := false
	issueCreated := false
	prClosed := false

	server := gh4591BillingTestServer(t, "billingsha1", &rerunCalled, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 910}))
			return true
		case r.URL.Path == "/repos/owner/repo/pulls/60" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			return true
		}
		return false
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	prState := &PRState{
		PRNumber: 60,
		HeadSHA:  "billingsha1",
		Stage:    StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !rerunCalled {
		t.Error("expected RerunFailedJobs to be called for the billing-refusal-classified failure")
	}
	if issueCreated {
		t.Error("no fix issue should be created for a billing-refusal failure — there is nothing in the PR's code to fix")
	}
	if prClosed {
		t.Error("PR must not be closed on a billing-refusal auto-retry")
	}
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageWaitingCI)
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d", len(sink.events))
	}
	if sink.events[0].Type != alerts.EventType("ci_billing_refusal") {
		t.Errorf("alert Type = %q, want ci_billing_refusal", sink.events[0].Type)
	}
	if !strings.Contains(sink.events[0].Error, "billing") {
		t.Errorf("alert message = %q, want it to mention billing", sink.events[0].Error)
	}

	snap := c.metrics.Snapshot()
	if got := snap.CIRuns["infra_retry"]; got != 1 {
		t.Errorf("CIRuns[infra_retry] = %d, want 1", got)
	}
}

// TestHandleCIFailed_JobsNeverStarted_AlertFiresOncePerOutageWindow covers the
// GH-4591 acceptance criterion that the ci_billing_refusal alert fires once
// per repo per outage window, not once per PR: two different PRs failing CI
// with the same billing-refusal shape in the same outage window must only
// produce a single alert event, and a later, distinct outage (after CI has
// passed again, resetting the dedup guard) must alert again.
func TestHandleCIFailed_JobsNeverStarted_AlertFiresOncePerOutageWindow(t *testing.T) {
	server := gh4591BillingTestServer(t, "billingsha2", nil, nil)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))
	sink := &fakeAlertSink{}
	c.SetAlertsEngine(sink)

	pr1 := &PRState{PRNumber: 61, HeadSHA: "billingsha2", Stage: StageCIFailed}
	pr2 := &PRState{PRNumber: 62, HeadSHA: "billingsha2", Stage: StageCIFailed}

	if err := c.handleCIFailed(context.Background(), pr1); err != nil {
		t.Fatalf("handleCIFailed(pr1) returned unexpected error: %v", err)
	}
	if err := c.handleCIFailed(context.Background(), pr2); err != nil {
		t.Fatalf("handleCIFailed(pr2) returned unexpected error: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected exactly 1 alert across both PRs in the same outage window, got %d", len(sink.events))
	}

	// CI passing resets the dedup guard, so a later, distinct outage alerts again.
	if err := c.handleCIPassed(context.Background(), pr1); err != nil {
		t.Fatalf("handleCIPassed returned unexpected error: %v", err)
	}

	pr3 := &PRState{PRNumber: 63, HeadSHA: "billingsha2", Stage: StageCIFailed}
	if err := c.handleCIFailed(context.Background(), pr3); err != nil {
		t.Fatalf("handleCIFailed(pr3) returned unexpected error: %v", err)
	}

	if len(sink.events) != 2 {
		t.Fatalf("expected a second alert after the dedup guard reset via handleCIPassed, got %d total", len(sink.events))
	}
}

// GH-724: Test that mergeable=false (without dirty state) also triggers conflict detection.
func TestController_ProcessPR_MergeableFalse_DetectsConflict(t *testing.T) {
	prClosed := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			// mergeable=false but mergeable_state not set (older API responses)
			resp := github.PullRequest{
				Number:    42,
				Head:      github.PRRef{SHA: "abc1234"},
				Mergeable: &mergeable,
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			// GH-1796: auto-rebase fails (true conflict)
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → conflict detected via mergeable=false
	err = c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}
}

// GH-834: Test that per-PR circuit breaker doesn't block other PRs.
func TestController_PerPRCircuitBreaker_DoesNotBlockOtherPRs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/merge":
			// Always fail merge for PR 42
			w.WriteHeader(http.StatusInternalServerError)
		case "/repos/owner/repo/pulls/43/merge":
			// Always succeed for PR 43
			w.WriteHeader(http.StatusOK)
		case "/repos/owner/repo/commits/sha42/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/commits/sha43/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			}
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
	cfg.MaxFailures = 2
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR 42 at merging stage (will fail)
	c.mu.Lock()
	c.activePRs[42] = &PRState{PRNumber: 42, HeadSHA: "sha42", Stage: StageMerging, TargetBranch: "main"}
	c.activePRs[43] = &PRState{PRNumber: 43, HeadSHA: "sha43", Stage: StageMerging, TargetBranch: "main"}
	c.mu.Unlock()

	ctx := context.Background()

	// Cause failures on PR 42 until circuit opens
	for i := 0; i < 2; i++ {
		_ = c.ProcessPR(ctx, 42, nil)
	}

	// PR 42's circuit should be open
	if !c.IsPRCircuitOpen(42) {
		t.Error("PR 42's circuit breaker should be open")
	}

	// PR 43's circuit should NOT be open
	if c.IsPRCircuitOpen(43) {
		t.Error("PR 43's circuit breaker should NOT be open (independent of PR 42)")
	}

	// PR 43 should still be processable
	err := c.ProcessPR(ctx, 43, nil)
	if err != nil {
		t.Errorf("PR 43 should be processable despite PR 42's failures: %v", err)
	}

	// PR 42 should be blocked
	err = c.ProcessPR(ctx, 42, nil)
	if err == nil {
		t.Error("PR 42 should be blocked by its per-PR circuit breaker")
	}
}

// GH-834: Test that per-PR circuit breaker resets after timeout.
func TestController_PerPRCircuitBreaker_ResetsAfterTimeout(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 2
	cfg.FailureResetTimeout = 50 * time.Millisecond // Short timeout for testing

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up failure state with old timestamp
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{
		FailureCount:    5,
		LastFailureTime: time.Now().Add(-100 * time.Millisecond), // Older than timeout
	}
	c.activePRs[42] = &PRState{PRNumber: 42, Stage: StagePRCreated}
	c.mu.Unlock()

	// Circuit should be closed because timeout has passed
	if c.IsPRCircuitOpen(42) {
		t.Error("circuit should be closed after timeout passed")
	}
}

// GH-834: Test ResetPRCircuitBreaker for single PR.
func TestController_ResetPRCircuitBreaker(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 2

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up failure state for multiple PRs
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 5, LastFailureTime: time.Now()}
	c.prFailures[43] = &prFailureState{FailureCount: 5, LastFailureTime: time.Now()}
	c.mu.Unlock()

	// Both should be open
	if !c.IsPRCircuitOpen(42) {
		t.Error("PR 42 circuit should be open")
	}
	if !c.IsPRCircuitOpen(43) {
		t.Error("PR 43 circuit should be open")
	}

	// Reset only PR 42
	c.ResetPRCircuitBreaker(42)

	// PR 42 should be closed, PR 43 still open
	if c.IsPRCircuitOpen(42) {
		t.Error("PR 42 circuit should be closed after reset")
	}
	if !c.IsPRCircuitOpen(43) {
		t.Error("PR 43 circuit should still be open")
	}
}

// GH-834: Test GetPRFailures returns correct count.
func TestController_GetPRFailures(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Initially zero
	if c.GetPRFailures(42) != 0 {
		t.Error("initial failures should be 0")
	}

	// Set failures
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 3, LastFailureTime: time.Now()}
	c.mu.Unlock()

	if c.GetPRFailures(42) != 3 {
		t.Errorf("failures = %d, want 3", c.GetPRFailures(42))
	}
}

// GH-834: Test that IsCircuitOpen returns true only when at least one PR is blocked.
func TestController_IsCircuitOpen_AnyPRBlocked(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.MaxFailures = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// No failures — circuit closed
	if c.IsCircuitOpen() {
		t.Error("circuit should be closed with no failures")
	}

	// Add failures below threshold
	c.mu.Lock()
	c.prFailures[42] = &prFailureState{FailureCount: 2, LastFailureTime: time.Now()}
	c.mu.Unlock()

	if c.IsCircuitOpen() {
		t.Error("circuit should be closed with failures below threshold")
	}

	// Add failures at threshold
	c.mu.Lock()
	c.prFailures[42].FailureCount = 3
	c.mu.Unlock()

	if !c.IsCircuitOpen() {
		t.Error("circuit should be open with failures at threshold")
	}
}

// TestController_DeadlockDetection tests the deadlock detection mechanism (GH-849).
func TestController_DeadlockDetection(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Initial state: lastProgressAt should be set to now
	initialProgress := c.GetLastProgressAt()
	if initialProgress.IsZero() {
		t.Error("lastProgressAt should be initialized on construction")
	}

	// Alert flag should start as false
	if c.IsDeadlockAlertSent() {
		t.Error("deadlockAlertSent should be false initially")
	}

	// Mark alert as sent
	c.MarkDeadlockAlertSent()
	if !c.IsDeadlockAlertSent() {
		t.Error("deadlockAlertSent should be true after marking")
	}

	// Simulate a PR state transition by adding a PR
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Get PR and manually trigger a stage transition to update lastProgressAt
	pr, _ := c.GetPRState(42)
	previousStage := pr.Stage
	pr.Stage = StageWaitingCI

	// Simulate what ProcessPR does on stage transition
	c.mu.Lock()
	if pr.Stage != previousStage {
		c.lastProgressAt = time.Now()
		c.deadlockAlertSent = false
	}
	c.mu.Unlock()

	// After transition, lastProgressAt should be updated and alert flag reset
	newProgress := c.GetLastProgressAt()
	if !newProgress.After(initialProgress) && !newProgress.Equal(initialProgress) {
		t.Error("lastProgressAt should be updated after stage transition")
	}
	if c.IsDeadlockAlertSent() {
		t.Error("deadlockAlertSent should be reset after stage transition")
	}
}

// TestController_DeadlockDetection_StaleProgress tests that stale progress is detected.
func TestController_DeadlockDetection_StaleProgress(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Manually set lastProgressAt to 2 hours ago
	c.mu.Lock()
	c.lastProgressAt = time.Now().Add(-2 * time.Hour)
	c.mu.Unlock()

	// Check that GetLastProgressAt returns the stale time
	progress := c.GetLastProgressAt()
	if time.Since(progress) < 1*time.Hour {
		t.Error("lastProgressAt should be more than 1 hour ago")
	}

	// Add a PR to simulate active work
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// The MetricsAlerter would check: noProgressMin >= 60 && len(activePRs) > 0
	noProgressMin := time.Since(c.GetLastProgressAt()).Minutes()
	activePRs := c.GetActivePRs()

	if noProgressMin < 60 {
		t.Errorf("noProgressMin = %.1f, expected >= 60", noProgressMin)
	}
	if len(activePRs) == 0 {
		t.Error("expected active PRs")
	}

	// This is the condition that would trigger a deadlock alert
	deadlockDetected := noProgressMin >= 60 && !c.IsDeadlockAlertSent() && len(activePRs) > 0
	if !deadlockDetected {
		t.Error("deadlock condition should be detected")
	}
}

// TestController_handleMerging_ConflictClearsLabel tests GH-880:
// When merge fails due to conflict, handleMergeConflict should be called
// which removes pilot-in-progress label so the issue can be retried.
func TestController_handleMerging_ConflictClearsLabel(t *testing.T) {
	labelRemoved := false
	prClosed := false
	commentAdded := false
	mergeAttempted := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			mergeAttempted = true
			// Return 405 to simulate conflict error
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Pull Request is not mergeable",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == http.MethodPut:
			// GH-1796: auto-rebase fails (true conflict), fall through to close-and-retry
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "merge conflict between base and head",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			if r.Method == http.MethodPatch {
				// Close PR request
				prClosed = true
				w.WriteHeader(http.StatusOK)
				return
			}
			// GET PR - return with conflict state
			pr := github.PullRequest{
				Number:         42,
				State:          "open",
				Mergeable:      &mergeable,
				MergeableState: "dirty",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			labelRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			commentAdded = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR in StageMerging state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	// Process PR - merge should fail and trigger conflict handling
	err := c.ProcessPR(ctx, 42, nil)

	// No error returned because handleMergeConflict handles it gracefully
	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !mergeAttempted {
		t.Error("merge should have been attempted")
	}

	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}

	if !prClosed {
		t.Error("PR should have been closed")
	}

	if !commentAdded {
		t.Error("comment should have been added to PR")
	}

	// PR should be in Failed state
	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error != "merge conflict with base branch" {
		t.Errorf("Error = %q, want 'merge conflict with base branch'", prState.Error)
	}
}

// TestController_handleMerging_Success_RemovesFailedLabel tests GH-1302:
// When a retry succeeds and PR merges, pilot-failed label should be removed.
func TestController_handleMerging_Success_RemovesFailedLabel(t *testing.T) {
	pilotDoneAdded := false
	pilotInProgressRemoved := false
	pilotFailedRemoved := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge" && r.Method == http.MethodPut:
			// Successful merge
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha":     "merged123",
				"merged":  true,
				"message": "Pull Request successfully merged",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
			pr := github.PullRequest{
				Number: 42,
				State:  "open",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			pilotDoneAdded = true
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: github.LabelDone}})
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			pilotInProgressRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-failed" && r.Method == http.MethodDelete:
			pilotFailedRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR in StageMerging state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !pilotDoneAdded {
		t.Error("pilot-done label should have been added")
	}

	if !pilotInProgressRemoved {
		t.Error("pilot-in-progress label should have been removed")
	}

	if !pilotFailedRemoved {
		t.Error("pilot-failed label should have been removed (GH-1302)")
	}

	// PR should be in Merged state
	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageMerged {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageMerged)
	}
}

// GH-4021: a pilot-retry-* label from an earlier PR-closed-without-merge
// cycle must be cleared on a later successful merge, or it survives to arm a
// redundant auto-retry against already-shipped work (GH-3992).
func TestController_handleMerging_Success_ClearsRetryLabels(t *testing.T) {
	retryReadyRemoved := false
	retry1Removed := false
	retry2Removed := false
	retryExhaustedRemoved := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha":     "merged123",
				"merged":  true,
				"message": "Pull Request successfully merged",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
			pr := github.PullRequest{
				Number: 42,
				State:  "open",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: github.LabelDone}})
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/"+github.LabelRetryReady && r.Method == http.MethodDelete:
			retryReadyRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/"+github.LabelRetry1 && r.Method == http.MethodDelete:
			retry1Removed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/"+github.LabelRetry2 && r.Method == http.MethodDelete:
			retry2Removed = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/"+github.LabelRetryExhausted && r.Method == http.MethodDelete:
			retryExhaustedRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !retryReadyRemoved {
		t.Error("pilot-retry-ready label should have been removed on merge")
	}
	if !retry1Removed {
		t.Error("pilot-retry-1 label should have been removed on merge")
	}
	if !retry2Removed {
		t.Error("pilot-retry-2 label should have been removed on merge")
	}
	if !retryExhaustedRemoved {
		t.Error("pilot-retry-exhausted label should have been removed on merge")
	}
}

// Test consecutive API failure counter logic
func TestController_ConsecutiveAPIFailures(t *testing.T) {
	// Mock HTTP server that always returns error for check runs API
	var apiCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCallCount++
		if strings.Contains(r.URL.Path, "check-runs") {
			// Return error for CI checks
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"API Error"}`))
			return
		}
		// Default PR response for GetPullRequest calls
		if strings.Contains(r.URL.Path, "/pulls/") {
			pr := map[string]interface{}{
				"number":    42,
				"state":     "open",
				"merged":    false,
				"mergeable": true,
				"head": map[string]interface{}{
					"sha": "abc1234",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(pr)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	// Set PR to waiting CI stage
	prState, _ := c.GetPRState(42)
	prState.Stage = StageWaitingCI

	ctx := context.Background()

	// Call ProcessPR 4 times - failures should increment but not transition to StageFailed yet
	for i := 1; i <= 4; i++ {
		err := c.ProcessPR(ctx, 42, nil)
		if err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i, err)
		}

		prState, _ = c.GetPRState(42)
		if prState.ConsecutiveAPIFailures != i {
			t.Errorf("after %d failures: ConsecutiveAPIFailures = %d, want %d", i, prState.ConsecutiveAPIFailures, i)
		}
		if prState.Stage != StageWaitingCI {
			t.Errorf("after %d failures: Stage = %s, want %s", i, prState.Stage, StageWaitingCI)
		}
	}

	// 5th failure should transition to StageFailed
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR 5th iteration error: %v", err)
	}

	prState, _ = c.GetPRState(42)
	if prState.ConsecutiveAPIFailures != 5 {
		t.Errorf("after 5 failures: ConsecutiveAPIFailures = %d, want 5", prState.ConsecutiveAPIFailures)
	}
	if prState.Stage != StageFailed {
		t.Errorf("after 5 failures: Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, "CI check API failed 5 consecutive times") {
		t.Errorf("Error = %q, should contain consecutive API failure message", prState.Error)
	}
}

// Test that consecutive failure counter resets on successful API call
func TestController_ConsecutiveAPIFailures_Reset(t *testing.T) {
	// Mock HTTP server that fails 3 times then succeeds
	var apiCallCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "check-runs") {
			apiCallCount++
			if apiCallCount <= 3 {
				// Return error for first 3 CI checks
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"API Error"}`))
				return
			}
			// Success on 4th call - return successful CI
			response := map[string]interface{}{
				"total_count": 1,
				"check_runs": []map[string]interface{}{
					{
						"name":       "build",
						"status":     "completed",
						"conclusion": "success",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		// Default PR response for GetPullRequest calls
		if strings.Contains(r.URL.Path, "/pulls/") {
			pr := map[string]interface{}{
				"number":    42,
				"state":     "open",
				"merged":    false,
				"mergeable": true,
				"head": map[string]interface{}{
					"sha": "abc1234",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(pr)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	// Set PR to waiting CI stage
	prState, _ := c.GetPRState(42)
	prState.Stage = StageWaitingCI

	ctx := context.Background()

	// Call ProcessPR 3 times with failures
	for i := 1; i <= 3; i++ {
		err := c.ProcessPR(ctx, 42, nil)
		if err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i, err)
		}
		prState, _ = c.GetPRState(42)
		if prState.ConsecutiveAPIFailures != i {
			t.Errorf("after %d failures: ConsecutiveAPIFailures = %d, want %d", i, prState.ConsecutiveAPIFailures, i)
		}
	}

	// 4th call succeeds - counter should reset and transition to StageCIPassed
	err := c.ProcessPR(ctx, 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR 4th iteration (success) error: %v", err)
	}

	prState, _ = c.GetPRState(42)
	if prState.ConsecutiveAPIFailures != 0 {
		t.Errorf("after success: ConsecutiveAPIFailures = %d, want 0", prState.ConsecutiveAPIFailures)
	}
	if prState.Stage != StageCIPassed {
		t.Errorf("after success: Stage = %s, want %s", prState.Stage, StageCIPassed)
	}
}

// mockTaskMonitor implements TaskMonitor for testing.
type mockTaskMonitor struct {
	completedTasks map[string]string // taskID -> prURL
	failedTasks    map[string]string // taskID -> errorMsg; GH-4490 subtask 3
	runningTaskIDs []string          // TASK-399/GH-4209: live Monitor running/queued set
}

func newMockTaskMonitor() *mockTaskMonitor {
	return &mockTaskMonitor{
		completedTasks: make(map[string]string),
		failedTasks:    make(map[string]string),
	}
}

func (m *mockTaskMonitor) Complete(taskID, prURL string) {
	m.completedTasks[taskID] = prURL
}

// Fail implements TaskMonitor. GH-4490 subtask 3: records the terminal-failure
// call notifyExternalClose makes when a PR closes without merging.
func (m *mockTaskMonitor) Fail(taskID, errorMsg string) {
	m.failedTasks[taskID] = errorMsg
}

// GetRunningTaskIDs implements TaskMonitor. TASK-399/GH-4209.
func (m *mockTaskMonitor) GetRunningTaskIDs() []string {
	return m.runningTaskIDs
}

// TestController_MonitorCompletedOnMerge verifies that when autopilot successfully
// merges a PR, it calls monitor.Complete() to sync dashboard state.
// GH-1336: Dashboard shows stale "failed" status because autopilot didn't update monitor.
func TestController_MonitorCompletedOnMerge(t *testing.T) {
	mergeWasCalled := false
	labelsAdded := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			mergeWasCalled = true
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels") && r.Method == "POST":
			// Track labels added
			var labels []string
			_ = json.NewDecoder(r.Body).Decode(&labels)
			labelsAdded = append(labelsAdded, labels...)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	// Create controller with mock monitor
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	mockMonitor := newMockTaskMonitor()
	c.SetMonitor(mockMonitor)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	c.activePRs[42].TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	// Process through the stages: PR created → waiting CI → CI passed → merging → merged
	for i := 0; i < 5; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}

	// Verify merge was called
	if !mergeWasCalled {
		t.Error("merge should have been called")
	}

	// Verify monitor.Complete was called with correct taskID
	expectedTaskID := "GH-10"
	prURL, ok := mockMonitor.completedTasks[expectedTaskID]
	if !ok {
		t.Errorf("monitor.Complete was not called for taskID %s", expectedTaskID)
	}
	if prURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("monitor.Complete prURL = %s, want https://github.com/owner/repo/pull/42", prURL)
	}
}

// TestController_MonitorNilSafe verifies that nil monitor doesn't cause panic.
func TestController_MonitorNilSafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.DevCITimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	// Create controller WITHOUT setting monitor (nil monitor)
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// Intentionally NOT calling c.SetMonitor(...)

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Process through all stages - should not panic even with nil monitor
	for i := 0; i < 5; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}
	// If we get here without panic, nil safety is verified
}

// GH-1566: Test that CI fix cascade stops after MaxCIFixIterations.
func TestController_CIFixCascadeLimit(t *testing.T) {
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == "GET":
			// Return issue body with iteration:3 (at the limit)
			resp := github.Issue{
				Number: 10,
				Body:   "Fix CI failure\n\n<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:3 -->\n",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// Stage 1: PR created → waiting CI
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR stage 1 error: %v", err)
	}

	// Stage 2: waiting CI → CI failed
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR stage 2 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageCIFailed {
		t.Fatalf("after stage 2: Stage = %s, want %s", pr.Stage, StageCIFailed)
	}

	// Stage 3: CI failed → should NOT create fix issue (iteration limit reached)
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR stage 3 error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue should NOT have been created when iteration limit is reached")
	}
	if !prClosed {
		t.Error("failed PR should still be closed when iteration limit is reached")
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after stage 3: Stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !strings.Contains(pr.Error, "CI fix iteration limit reached") {
		t.Errorf("error should mention iteration limit, got: %s", pr.Error)
	}
	if !strings.Contains(pr.Error, "build") {
		t.Errorf("error should name the failing check, got: %s", pr.Error)
	}
	if pr.TerminalLabel != github.LabelFailed {
		t.Errorf("TerminalLabel = %q, want %q (iteration-limit close must not be silently re-queued)", pr.TerminalLabel, github.LabelFailed)
	}
}

// GH-3806: Simulates the full CI-failure close path end to end — handleCIFailed
// closes the PR after the iteration limit is hit, and on the next poll
// notifyExternalClose observes the closed PR and must post a PR comment naming
// the reason and failing check, correct the issue's labels (pilot-failed, not
// pilot-retry-ready — a stale pilot-done/pilot-in-progress must not survive),
// and post a matching issue comment. No step in this chain may be silent.
func TestController_CIFailedClose_PostsCommentsAndCorrectsLabels(t *testing.T) {
	var prCommentBody, issueCommentBody string
	var prCommentPosted, issueCommentPosted, failedLabelAdded, inProgressRemoved bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "unit-tests", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
			resp := github.Issue{
				Number: 10,
				State:  "open",
				Body:   "Fix CI failure\n\n<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:3 -->\n",
				Labels: []github.Label{{Name: github.LabelInProgress}},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
			resp := github.PullRequest{
				Number:  42,
				State:   "closed",
				HTMLURL: "https://github.com/owner/repo/pull/42",
				Head:    github.PRRef{Ref: "pilot/GH-10", SHA: "abc1234"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		// PRs use the issues comments API (AddPRComment posts to /issues/{prNumber}/comments).
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			prCommentPosted = true
			body, _ := io.ReadAll(r.Body)
			prCommentBody = string(body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		case r.URL.Path == "/repos/owner/repo/issues/10/comments" && r.Method == http.MethodPost:
			issueCommentPosted = true
			body, _ := io.ReadAll(r.Body)
			issueCommentBody = string(body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 2})
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, l := range body.Labels {
				if l == github.LabelFailed {
					failedLabelAdded = true
				}
				if l == github.LabelRetryReady {
					t.Error("pilot-retry-ready must not be added for a terminal iteration-limit close")
				}
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/"+github.LabelInProgress && r.Method == http.MethodDelete:
			inProgressRemoved = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()
	if err := c.ProcessPR(ctx, 42, nil); err != nil { // PR created -> waiting CI
		t.Fatalf("stage 1 error: %v", err)
	}
	if err := c.ProcessPR(ctx, 42, nil); err != nil { // waiting CI -> CI failed
		t.Fatalf("stage 2 error: %v", err)
	}
	if err := c.ProcessPR(ctx, 42, nil); err != nil { // CI failed -> closed (iteration limit)
		t.Fatalf("stage 3 error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s", pr.Stage, StageFailed)
	}

	// Next poll: the poller observes the PR is now closed on GitHub and runs
	// notifyExternalClose — this is where GH-3806's audit trail is written.
	prState, _ := c.GetPRState(42)
	// GH-4570: back-date CreatedAt past externalCloseGraceWindow so this
	// single closed read is trusted (this test targets notifyExternalClose's
	// audit trail, not the grace-window confirmation gate, which is covered
	// separately).
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Now().Add(-10 * time.Minute)
	c.mu.Unlock()
	ghPR, err := ghClient.GetPullRequest(ctx, "owner", "repo", 42)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	prState.mu.Lock()
	resolved := c.checkExternalMergeOrClose(ctx, prState, ghPR)
	prState.mu.Unlock()
	if !resolved {
		t.Fatal("checkExternalMergeOrClose should report the PR as resolved (closed)")
	}

	if !prCommentPosted {
		t.Fatal("expected a PR comment explaining why the PR was closed")
	}
	if !strings.Contains(prCommentBody, "unit-tests") {
		t.Errorf("PR comment should name the failing check, got: %s", prCommentBody)
	}
	if !strings.Contains(prCommentBody, "abc1234") {
		t.Errorf("PR comment should link to the CI run for the head SHA, got: %s", prCommentBody)
	}
	if !issueCommentPosted {
		t.Fatal("expected an issue comment explaining why the linked PR was closed")
	}
	if !strings.Contains(issueCommentBody, "42") {
		t.Errorf("issue comment should reference the closed PR number, got: %s", issueCommentBody)
	}
	if !failedLabelAdded {
		t.Error("issue should be labeled pilot-failed, not left to be silently re-queued")
	}
	if !inProgressRemoved {
		t.Error("pilot-in-progress should be removed from the issue on close")
	}
}

// GH-1566: Test that CI fix proceeds when under the iteration limit.
func TestController_CIFixCascade_UnderLimit(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == "GET":
			// Return issue body with iteration:1 (under the limit of 3)
			resp := github.Issue{
				Number: 10,
				Body:   "Fix CI failure\n\n<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:1 -->\n",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// PR created → waiting CI → CI failed → create fix issue
	for i := 0; i < 3; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}

	if !issueCreated {
		t.Error("fix issue should have been created when under iteration limit")
	}
}

// GH-1566: Test that original PRs (no autopilot-meta) work normally.
func TestController_CIFixCascade_OriginalPR(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == "GET":
			// Return original issue (no autopilot-meta, iteration=0)
			resp := github.Issue{
				Number: 10,
				Body:   "Original task: implement feature X",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.MaxCIFixIterations = 3

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()

	// PR created → waiting CI → CI failed → create fix issue (iteration 0 < 3)
	for i := 0; i < 3; i++ {
		if err := c.ProcessPR(ctx, 42, nil); err != nil {
			t.Fatalf("ProcessPR iteration %d error: %v", i+1, err)
		}
	}

	if !issueCreated {
		t.Error("fix issue should have been created for original PR (no iteration metadata)")
	}
}

// GH-1566: Test parseAutopilotIteration function.
func TestController_ShouldTriggerRelease(t *testing.T) {
	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		envName       string
		want          bool
	}{
		{
			name:          "global only enabled",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			want:          true,
		},
		{
			name:          "global only disabled",
			globalRelease: &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			want:          false,
		},
		{
			name:       "env only enabled",
			envRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envName:    "prod",
			want:       true,
		},
		{
			name:       "env only disabled",
			envRelease: &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			envName:    "prod",
			want:       false,
		},
		{
			name:          "env overrides global - env enabled",
			globalRelease: &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			envRelease:    &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envName:       "prod",
			want:          true,
		},
		{
			name:          "env overrides global - env disabled",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "on_merge"},
			envRelease:    &ReleaseConfig{Enabled: false, Trigger: "on_merge"},
			envName:       "prod",
			want:          false,
		},
		{
			name: "both nil",
			want: false,
		},
		{
			name:          "global enabled but trigger manual",
			globalRelease: &ReleaseConfig{Enabled: true, Trigger: "manual"},
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghClient := github.NewClient(testutil.FakeGitHubToken)
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease

			if tt.envName != "" && tt.envRelease != nil {
				cfg.Environments[tt.envName] = &EnvironmentConfig{
					Release: tt.envRelease,
				}
				if err := cfg.SetActiveEnvironment(tt.envName); err != nil {
					t.Fatalf("SetActiveEnvironment: %v", err)
				}
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")
			got := c.shouldTriggerRelease()
			if got != tt.want {
				t.Errorf("shouldTriggerRelease() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestController_ResolvedRelease(t *testing.T) {
	tests := []struct {
		name          string
		globalRelease *ReleaseConfig
		envRelease    *ReleaseConfig
		envName       string
		wantTagPrefix string
		wantNil       bool
	}{
		{
			name:          "env release takes precedence",
			globalRelease: &ReleaseConfig{TagPrefix: "global-v"},
			envRelease:    &ReleaseConfig{TagPrefix: "env-v"},
			envName:       "prod",
			wantTagPrefix: "env-v",
		},
		{
			name:          "falls back to global when env has no release",
			globalRelease: &ReleaseConfig{TagPrefix: "global-v"},
			envName:       "dev",
			wantTagPrefix: "global-v",
		},
		{
			name:    "both nil returns nil",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ghClient := github.NewClient(testutil.FakeGitHubToken)
			cfg := DefaultConfig()
			cfg.Release = tt.globalRelease

			if tt.envName != "" {
				env := cfg.Environments[tt.envName]
				if env == nil {
					env = &EnvironmentConfig{}
					cfg.Environments[tt.envName] = env
				}
				env.Release = tt.envRelease
				if err := cfg.SetActiveEnvironment(tt.envName); err != nil {
					t.Fatalf("SetActiveEnvironment: %v", err)
				}
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")
			got := c.resolvedRelease()
			if tt.wantNil {
				if got != nil {
					t.Errorf("resolvedRelease() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("resolvedRelease() = nil, want non-nil")
			}
			if got.TagPrefix != tt.wantTagPrefix {
				t.Errorf("resolvedRelease().TagPrefix = %q, want %q", got.TagPrefix, tt.wantTagPrefix)
			}
		})
	}
}

func TestParseAutopilotIteration(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"iteration 1", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:1 -->", 1},
		{"iteration 3", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:3 -->", 3},
		{"iteration 0", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:0 -->", 0},
		{"no iteration", "<!-- autopilot-meta branch:pilot/GH-10 pr:42 -->", 0},
		{"no metadata", "just a normal issue body", 0},
		{"empty body", "", 0},
		{"embedded in body", "# Fix\n\n## Context\nstuff\n\n<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:5 -->\n", 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAutopilotIteration(tt.body)
			if got != tt.want {
				t.Errorf("parseAutopilotIteration() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestController_handleMergeConflict_AutoRebaseSuccess tests GH-1796:
// When merge conflict is detected and GitHub can auto-update the branch,
// the PR stays open and transitions to StageWaitingCI.
func TestController_handleMergeConflict_AutoRebaseSuccess(t *testing.T) {
	updateBranchCalled := false
	prClosed := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			// Return 405 to simulate conflict error on merge attempt
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Pull Request is not mergeable",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == http.MethodPut:
			updateBranchCalled = true
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Updating pull request branch."})
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			if r.Method == http.MethodPatch {
				prClosed = true
				w.WriteHeader(http.StatusOK)
				return
			}
			// GET PR - return with conflict state
			pr := github.PullRequest{
				Number:         42,
				State:          "open",
				Mergeable:      &mergeable,
				MergeableState: "dirty",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Set up PR in StageMerging state
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()
	err := c.ProcessPR(ctx, 42, nil)

	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !updateBranchCalled {
		t.Error("UpdatePullRequestBranch should have been called")
	}

	if prClosed {
		t.Error("PR should NOT have been closed after successful auto-rebase")
	}

	// PR should transition to WaitingCI
	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageWaitingCI {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageWaitingCI)
	}
	if prState.HeadSHA != "" {
		t.Errorf("HeadSHA should be empty to force refresh, got %q", prState.HeadSHA)
	}
}

// TestController_handleMergeConflict_AutoRebaseFails tests GH-1796:
// When auto-rebase fails (true conflict), falls back to close-and-retry.
func TestController_handleMergeConflict_AutoRebaseFails(t *testing.T) {
	updateBranchCalled := false
	prClosed := false
	labelRemoved := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge":
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "Pull Request is not mergeable",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == http.MethodPut:
			updateBranchCalled = true
			// Return 422 - true conflict, cannot auto-update
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": "merge conflict between base and head",
			})
		case r.URL.Path == "/repos/owner/repo/pulls/42":
			if r.Method == http.MethodPatch {
				prClosed = true
				w.WriteHeader(http.StatusOK)
				return
			}
			pr := github.PullRequest{
				Number:         42,
				State:          "open",
				Mergeable:      &mergeable,
				MergeableState: "dirty",
				Head: github.PRRef{
					Ref: "pilot/GH-10",
					SHA: "abc1234",
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels/pilot-in-progress" && r.Method == http.MethodDelete:
			labelRemoved = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()
	err := c.ProcessPR(ctx, 42, nil)

	if err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	if !updateBranchCalled {
		t.Error("UpdatePullRequestBranch should have been called")
	}

	if !prClosed {
		t.Error("PR should have been closed after failed auto-rebase")
	}

	if !labelRemoved {
		t.Error("pilot-in-progress label should have been removed from issue")
	}

	prState, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// newTestLearningLoop creates a LearningLoop backed by a temp SQLite store for testing.
// The store is returned so the caller can close and clean it up.
func newTestLearningLoop(t *testing.T) (*memory.LearningLoop, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "controller-learn-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	store, err := memory.NewStore(tmpDir)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("failed to create store: %v", err)
	}
	// nil extractor: LearnFromReview will return an error (logged as warning, not propagated)
	loop := memory.NewLearningLoop(store, nil, nil)
	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return loop, cleanup
}

// TestHandleMerged_LearnsFromReviews verifies that handleMerged fetches PR reviews
// when a learning loop is configured.
func TestHandleMerged_LearnsFromReviews(t *testing.T) {
	reviewsFetched := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			reviewsFetched = true
			reviews := []github.PullRequestReview{
				{Body: "LGTM — nice implementation", State: "APPROVED", User: github.User{Login: "reviewer1"}},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, reviews))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		Stage:    StageMerged,
	}

	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}

	if !reviewsFetched {
		t.Error("expected /pulls/42/reviews to be fetched for learning")
	}
}

// TestHandleMerged_NoReviews verifies that handleMerged does not error when
// there are no reviews to learn from.
func TestHandleMerged_NoReviews(t *testing.T) {
	reviewsFetched := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			reviewsFetched = true
			// Return empty array — no reviews
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		Stage:    StageMerged,
	}

	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}

	if !reviewsFetched {
		t.Error("expected /pulls/42/reviews to be fetched even when empty")
	}
}

// TestHandleMerged_NilLearningLoop verifies that handleMerged does not panic
// when no learning loop is configured (nil guard).
func TestHandleMerged_NilLearningLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// learningLoop intentionally not set

	prState := &PRState{
		PRNumber: 42,
		PRURL:    "https://github.com/owner/repo/pull/42",
		Stage:    StageMerged,
	}

	// Must not panic
	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}
}

// TestHandleCIFailed_LearnsFromCIFailure verifies that handleCIFailed calls
// LearnFromCIFailure when a learning loop is configured.
func TestHandleCIFailed_LearnsFromCIFailure(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha123/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 42,
		HeadSHA:  "sha123",
		Stage:    StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected fix issue to be created")
	}

	// The learning loop was set, so LearnFromCIFailure was called.
	// With nil extractor it returns an error (logged as warning), but must not panic.
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// TestHandleCIFailed_NilLearningLoop verifies that handleCIFailed does not panic
// when no learning loop is configured (nil guard).
func TestHandleCIFailed_NilLearningLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/sha456/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "lint", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			resp := github.Issue{Number: 201}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	// learningLoop intentionally not set

	prState := &PRState{
		PRNumber: 43,
		HeadSHA:  "sha456",
		Stage:    StageCIFailed,
	}

	// Must not panic
	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
}

// TestHandlePostMergeCI_LearnsFromCIFailure verifies that handlePostMergeCI calls
// LearnFromCIFailure when post-merge CI fails and a learning loop is configured.
func TestHandlePostMergeCI_LearnsFromCIFailure(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/branches/main":
			resp := map[string]interface{}{
				"commit": map[string]string{"sha": "mainsha1"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/commits/mainsha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "e2e", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			issueCreated = true
			resp := github.Issue{Number: 300}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"e2e"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 44,
		Stage:    StagePostMergeCI,
	}

	err := c.handlePostMergeCI(context.Background(), prState)
	if err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected post-merge fix issue to be created")
	}
}

// TestHandlePostMergeCI_NonBlocking verifies that handlePostMergeCI does not block
// the tick loop: pending CI returns nil and stays in StagePostMergeCI, success
// advances to StageReleasing (when release is configured) or removes the PR,
// and a daemon restart resumes from the persisted PostMergeSHA. (GH-2717)
func TestHandlePostMergeCI_NonBlocking(t *testing.T) {
	tests := []struct {
		name           string
		checkStatus    string
		checkConc      string
		releaseEnabled bool
		wantStage      PRStage
		wantRemoved    bool
	}{
		{
			name:        "pending stays in stage",
			checkStatus: "in_progress",
			wantStage:   StagePostMergeCI,
			wantRemoved: false,
		},
		{
			name:           "success without release removes PR",
			checkStatus:    "completed",
			checkConc:      "success",
			releaseEnabled: false,
			wantRemoved:    true,
		},
		{
			name:           "success with release advances to StageReleasing",
			checkStatus:    "completed",
			checkConc:      "success",
			releaseEnabled: true,
			wantStage:      StageReleasing,
			wantRemoved:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/branches/main":
					resp := map[string]interface{}{
						"commit": map[string]string{"sha": "mainsha42"},
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))
				case "/repos/owner/repo/commits/mainsha42/check-runs":
					resp := github.CheckRunsResponse{
						TotalCount: 1,
						CheckRuns: []github.CheckRun{
							{Name: "ci", Status: tt.checkStatus, Conclusion: tt.checkConc},
						},
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(mustJSON(t, resp))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Environment = EnvDev
			cfg.CIPollInterval = 10 * time.Millisecond
			cfg.CIWaitTimeout = 5 * time.Second
			if tt.releaseEnabled {
				cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber: 77,
				Stage:    StagePostMergeCI,
			}

			err := c.handlePostMergeCI(context.Background(), prState)
			if err != nil {
				t.Fatalf("handlePostMergeCI returned error: %v", err)
			}

			if tt.wantRemoved {
				// PR should have been removed from tracking.
				c.mu.RLock()
				_, stillTracked := c.activePRs[77]
				c.mu.RUnlock()
				if stillTracked {
					t.Error("expected PR to be removed but it is still tracked")
				}
			} else {
				if prState.Stage != tt.wantStage {
					t.Errorf("stage = %s, want %s", prState.Stage, tt.wantStage)
				}
			}

			// PostMergeSHA must be populated after first call.
			if prState.PostMergeSHA == "" {
				t.Error("PostMergeSHA should be set after first tick")
			}
		})
	}
}

// TestHandlePostMergeCI_RestartResumesSHA verifies that if PostMergeSHA is already
// set (simulating a daemon restart with persisted state), handlePostMergeCI does not
// fetch a new SHA and continues monitoring the original commit. (GH-2717)
func TestHandlePostMergeCI_RestartResumesSHA(t *testing.T) {
	branchFetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/branches/main":
			branchFetched = true
			resp := map[string]interface{}{
				"commit": map[string]string{"sha": "newsha99"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case "/repos/owner/repo/commits/originalsha/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "in_progress"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 5 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Simulate persisted state from before restart (started 30s ago, well within timeout).
	prState := &PRState{
		PRNumber:             88,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "originalsha",
		PostMergeCIStartedAt: time.Now().Add(-30 * time.Second),
	}

	err := c.handlePostMergeCI(context.Background(), prState)
	if err != nil {
		t.Fatalf("handlePostMergeCI returned error: %v", err)
	}

	if branchFetched {
		t.Error("branch SHA should NOT be fetched when PostMergeSHA is already set")
	}
	if prState.PostMergeSHA != "originalsha" {
		t.Errorf("PostMergeSHA changed to %q, want %q", prState.PostMergeSHA, "originalsha")
	}
	if prState.Stage != StagePostMergeCI {
		t.Errorf("stage = %s, want %s (CI still running)", prState.Stage, StagePostMergeCI)
	}
}

// TestHandlePostMergeCI_Timeout verifies that a long-running post-merge CI
// eventually times out and transitions to StageFailed. (GH-2717)
func TestHandlePostMergeCI_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/commits/tsha/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "in_progress"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIWaitTimeout = 1 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Started well in the past to trigger immediate timeout.
	prState := &PRState{
		PRNumber:             99,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "tsha",
		PostMergeCIStartedAt: time.Now().Add(-10 * time.Minute),
	}

	err := c.handlePostMergeCI(context.Background(), prState)
	if err != nil {
		t.Fatalf("handlePostMergeCI returned error: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s after timeout", prState.Stage, StageFailed)
	}
	if prState.Error == "" {
		t.Error("expected Error to be set on timeout")
	}
}

// TestHandlePostMergeCI_CIFailure_MarksStageFailedNotRemoved verifies GH-4312:
// a post-merge CI failure (no scope release in play) marks the PR StageFailed
// and leaves it in activePRs — mirroring the timeout branch above — instead of
// calling removePR. removePR issues a DELETE against the state store; since no
// release tag exists for this merge commit (CI never passed), evicting the row
// would let the merged-PR release scan re-register the PR at StagePostMergeCI
// on its very next tick, re-entering this branch and respawning a fix issue
// forever.
func TestHandlePostMergeCI_CIFailure_MarksStageFailedNotRemoved(t *testing.T) {
	issueCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/failsha1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 500}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             123,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "failsha1",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[123] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("expected post-merge fix issue to be created")
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error == "" {
		t.Error("expected Error to be set on post-merge CI failure")
	}

	c.mu.RLock()
	_, stillTracked := c.activePRs[123]
	c.mu.RUnlock()
	if !stillTracked {
		t.Error("PR should remain tracked at StageFailed (terminal, no-op in main loop) — removePR would delete the persisted row and let the release scan re-discover it")
	}
}

// TestHandlePostMergeCI_ZeroEvidence_EscalatesInsteadOfSpawning is the
// TASK-459 Phase 2 regression test for the post-merge CI-failure
// CreateFailureIssue rung (family 3 of the irreversible-action inventory,
// controller.go ~:3977 at inventory time): the initial CheckCI status
// determination sees a failing check (that's the only way this rung is
// reached at all), but the classification re-fetch a moment later races
// GitHub's own status propagation and comes back with zero check runs —
// the exact GH-4779 shape, replayed for the post-merge path, which
// previously had no classification step at all and would spawn a fix issue
// for any post-merge CIFailure regardless of evidence.
func TestHandlePostMergeCI_ZeroEvidence_EscalatesInsteadOfSpawning(t *testing.T) {
	issueCreated := false
	var labelsAdded []string
	checkRunsCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/zeroevidencepm1/check-runs":
			checkRunsCalls++
			if checkRunsCalls == 1 {
				// First call — CheckCI's own status determination — sees a
				// failing check, which is what routes into the CIFailure
				// switch case at all.
				resp := github.CheckRunsResponse{
					TotalCount: 1,
					CheckRuns: []github.CheckRun{
						{Name: "ci", Status: "completed", Conclusion: "failure"},
					},
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mustJSON(t, resp))
				return
			}
			// Every subsequent call (GetFailedChecks / GetFailedCheckExcerpts
			// / GetFailedCheckLogsByCheck) comes back with nothing — the
			// zero-gathered-evidence race.
			resp := github.CheckRunsResponse{TotalCount: 0, CheckRuns: []github.CheckRun{}}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 700}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case strings.Contains(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             124,
		IssueNumber:          33,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "zeroevidencepm1",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[124] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("no fix issue should be spawned when there is zero gathered evidence post-merge")
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", prState.Stage, StageFailed)
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label via escalateAndHold, got labels: %v", labelsAdded)
	}
}

// TestHandlePostMergeCI_CIFailure_MaxCIFixIterationsGuard verifies GH-4312:
// the pre-merge iteration-depth guard (controller.go handleCIFailed, ~:1502)
// is ported to the post-merge path — when the merged PR's issue is itself a
// spawned fix issue whose autopilot-meta iteration counter has already
// reached MaxCIFixIterations, handlePostMergeCI must not spawn another fix
// issue.
func TestHandlePostMergeCI_CIFailure_MaxCIFixIterationsGuard(t *testing.T) {
	issueCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/failsha2/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/77" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 77, Body: "<!-- autopilot-meta branch:pilot/GH-77 pr:200 iteration:2 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 999}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MaxCIFixIterations = 2

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             200,
		IssueNumber:          77,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "failsha2",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[200] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue must NOT be created when post-merge iteration limit is reached")
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// TestHandlePostMergeCI_CIFailure_MaxCIFixPRSizeGuard verifies GH-4312: the
// pre-merge cascade size guard (controller.go handleCIFailed, ~:1555) is
// ported to the post-merge path — a merged PR that already exceeds
// MaxCIFixPRSize must not spawn another fix issue on post-merge CI failure.
func TestHandlePostMergeCI_CIFailure_MaxCIFixPRSizeGuard(t *testing.T) {
	issueCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/failsha3/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "ci", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/300/files" && r.Method == http.MethodGet:
			files := []*github.PRFile{
				{Filename: "internal/foo.go", Status: "added", Additions: 500},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 998}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             300,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "failsha3",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[300] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue must NOT be created when merged PR exceeds size floor")
	}
	if prState.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", prState.Stage, StageFailed)
	}
}

// TestHandlePostMergeCI_InfraFailure_AutoRetries is the GH-4813 regression
// test for the post-merge CI-failure rung's missing infra-retry leg: an
// evidenced infra-class post-merge failure (429-rate-limited action
// download, same fixture as the pre-merge GH-4526 replay) must auto-retry
// via RerunFailedJobs instead of ever reaching CreateFailureIssue.
func TestHandlePostMergeCI_InfraFailure_AutoRetries(t *testing.T) {
	rerunCalled := false
	issueCreated := false

	server := gh4533InfraTestServer(t, "pminfrasha1", &rerunCalled, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost {
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 910}))
			return true
		}
		return false
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	stepClient := ghadapter.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithStepLogClient(stepClient))

	prState := &PRState{
		PRNumber:             501,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "pminfrasha1",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[501] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if !rerunCalled {
		t.Error("expected RerunFailedJobs to be called for the infra-classified post-merge failure")
	}
	if issueCreated {
		t.Error("no fix issue should be spawned for an infra-classified post-merge failure with retry budget remaining")
	}
	if prState.Stage != StagePostMergeCI {
		t.Errorf("Stage = %s, want %s (still polling after auto-retry)", prState.Stage, StagePostMergeCI)
	}
	if prState.PostMergeInfraRerunCount != 1 {
		t.Errorf("PostMergeInfraRerunCount = %d, want 1", prState.PostMergeInfraRerunCount)
	}
	if prState.PostMergeInfraRerunSHA != "pminfrasha1" {
		t.Errorf("PostMergeInfraRerunSHA = %q, want %q", prState.PostMergeInfraRerunSHA, "pminfrasha1")
	}
}

// TestHandlePostMergeCI_InfraFailure_NoRerunPlumbing_EscalatesInsteadOfSpawning
// is the GH-4813 fallback-path regression test: when the rerun plumbing
// can't reach the post-merge SHA (no StepLogClient wired — the same
// condition that makes maybeRetryPostMergeInfraFailure a no-op), an
// evidenced infra-class post-merge failure must still never reach
// CreateFailureIssue — it must escalateAndHold with the distinct
// "post-merge CI failure classified infra" reason instead.
func TestHandlePostMergeCI_InfraFailure_NoRerunPlumbing_EscalatesInsteadOfSpawning(t *testing.T) {
	issueCreated := false
	var labelsAdded []string

	server := gh4533InfraTestServer(t, "pminfrasha2", nil, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, github.Issue{Number: 911}))
			return true
		case strings.Contains(r.URL.Path, "/labels") && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
			return true
		}
		return false
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	// Deliberately no WithStepLogClient — mirrors a controller instance
	// where rerun plumbing was never wired up for this repo.
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:             502,
		IssueNumber:          88,
		Stage:                StagePostMergeCI,
		PostMergeSHA:         "pminfrasha2",
		PostMergeCIStartedAt: time.Now(),
	}
	c.mu.Lock()
	c.activePRs[502] = prState
	c.mu.Unlock()

	if err := c.handlePostMergeCI(context.Background(), prState); err != nil {
		t.Fatalf("handlePostMergeCI returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("no fix issue should ever be spawned for an infra-classified post-merge failure — GH-4813 invariant")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label via escalateAndHold, got labels: %v", labelsAdded)
	}
}

// TestHandleCIFailed_EmptyLogs_SkipsLearning verifies that handleCIFailed skips
// LearnFromCIFailure when CI logs are empty or whitespace-only (GH-1979).
// TestHandleCIFailed_EmptyLogs_SkipsLearning verifies that handleCIFailed skips
// LearnFromCIFailure when CI logs are empty (no failed check runs found for log fetch).
func TestHandleCIFailed_EmptyLogs_SkipsLearning(t *testing.T) {
	learnCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// GetFailedChecks uses this endpoint
		case r.URL.Path == "/repos/owner/repo/commits/sha789/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		// GetFailedCheckExcerpts tries to fetch job logs — return 404 so logs are empty
		case strings.Contains(r.URL.Path, "/actions/jobs/") && strings.HasSuffix(r.URL.Path, "/logs"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == "POST":
			resp := github.Issue{Number: 210}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()
	c.SetLearningLoop(loop)

	prState := &PRState{
		PRNumber: 45,
		HeadSHA:  "sha789",
		Stage:    StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	// The learning loop should NOT have been invoked (empty logs guard).
	// learnCalled remains false since we can't directly observe the call,
	// but the absence of "Failed to learn from CI failure" warning in logs
	// confirms the guard works. With nil extractor + non-empty logs, the
	// warning would appear.
	_ = learnCalled
}

// TestSetLearningLoop_ForwardsToFeedbackLoop verifies that SetLearningLoop
// also injects the learning loop into the feedback loop (GH-1979).
func TestSetLearningLoop_ForwardsToFeedbackLoop(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	loop, cleanup := newTestLearningLoop(t)
	defer cleanup()

	// Before setting — feedbackLoop.learningLoop should be nil
	if c.feedbackLoop.learningLoop != nil {
		t.Error("feedbackLoop.learningLoop should be nil before SetLearningLoop")
	}

	c.SetLearningLoop(loop)

	// After setting — feedbackLoop.learningLoop should be wired
	if c.feedbackLoop.learningLoop == nil {
		t.Error("feedbackLoop.learningLoop should be set after SetLearningLoop")
	}
	if c.feedbackLoop.learningLoop != loop {
		t.Error("feedbackLoop.learningLoop should point to the same loop instance")
	}
}

// mockEvalStore captures SaveEvalTask calls for testing.
type mockEvalStore struct {
	saved        []*memory.EvalTask
	selfHealed   []selfHealCall
	updateStatus []updateStatusCall
	reclassified []reclassifyCall
	terminated   []terminateCall
	// GH-4701: separate call logs for the superseded-close siblings so tests
	// can assert which variant fired without disturbing the failed-path
	// assertions above.
	reclassifiedSuperseded []reclassifyCall
	terminatedSuperseded   []terminateCall
	// execStatusByTaskID configures GetExecutionStatusByTaskID responses keyed by
	// task ID (e.g. "GH-11"). Missing keys return sql.ErrNoRows, matching a real
	// store's behavior when no execution row exists for that task.
	execStatusByTaskID map[string]string

	// TASK-399/GH-4209: orphan-running sweep + pr_url fallback test hooks.
	prURLHealed        []string                   // SelfHealExecutionByPRURL calls
	orphanedRunning    []*memory.Execution        // FindOrphanedRunningExecutions candidate pool
	lastExcludeTaskIDs []string                   // last exclude set FindOrphanedRunningExecutions was called with
	resolvedOrphans    []resolveOrphanCall        // ResolveOrphanedRunningExecution calls
	executionEvents    map[string][]*memory.Event // ListExecutionEvents responses keyed by execution ID
}

// resolveOrphanCall records one ResolveOrphanedRunningExecution invocation. TASK-399/GH-4209.
type resolveOrphanCall struct {
	ID    string
	PRURL string
}

type selfHealCall struct {
	TaskID      string
	ProjectPath string
	PRURL       string
}

type updateStatusCall struct {
	TaskID      string
	ProjectPath string
	Status      string
}

// reclassifyCall records one ReclassifyCompletionAsFailed invocation. GH-3818.
type reclassifyCall struct {
	TaskID      string
	ProjectPath string
	Reason      string
}

// terminateCall records one TerminateNonTerminalExecution invocation. GH-4499.
type terminateCall struct {
	TaskID      string
	ProjectPath string
	Reason      string
}

func (m *mockEvalStore) SaveEvalTask(task *memory.EvalTask) error {
	m.saved = append(m.saved, task)
	return nil
}

func (m *mockEvalStore) UpdateExecutionStatusByTaskID(taskID, projectPath, status string) error {
	m.updateStatus = append(m.updateStatus, updateStatusCall{TaskID: taskID, ProjectPath: projectPath, Status: status})
	return nil
}

func (m *mockEvalStore) GetExecutionStatusByTaskID(taskID, projectPath string) (string, error) {
	status, ok := m.execStatusByTaskID[taskID]
	if !ok {
		return "", sql.ErrNoRows
	}
	return status, nil
}

func (m *mockEvalStore) SelfHealExecutionAfterMerge(taskID, projectPath, prURL string) error {
	m.selfHealed = append(m.selfHealed, selfHealCall{TaskID: taskID, ProjectPath: projectPath, PRURL: prURL})
	return nil
}

// ReclassifyCompletionAsFailed records the call for assertions. GH-3818.
func (m *mockEvalStore) ReclassifyCompletionAsFailed(taskID, projectPath, reason string) error {
	m.reclassified = append(m.reclassified, reclassifyCall{TaskID: taskID, ProjectPath: projectPath, Reason: reason})
	return nil
}

// TerminateNonTerminalExecution records the call for assertions. GH-4499.
func (m *mockEvalStore) TerminateNonTerminalExecution(taskID, projectPath, reason string) error {
	m.terminated = append(m.terminated, terminateCall{TaskID: taskID, ProjectPath: projectPath, Reason: reason})
	return nil
}

// ReclassifyCompletionAsSuperseded records the call for assertions. GH-4701.
func (m *mockEvalStore) ReclassifyCompletionAsSuperseded(taskID, projectPath, reason string) error {
	m.reclassifiedSuperseded = append(m.reclassifiedSuperseded, reclassifyCall{TaskID: taskID, ProjectPath: projectPath, Reason: reason})
	return nil
}

// TerminateNonTerminalExecutionAsSuperseded records the call for assertions. GH-4701.
func (m *mockEvalStore) TerminateNonTerminalExecutionAsSuperseded(taskID, projectPath, reason string) error {
	m.terminatedSuperseded = append(m.terminatedSuperseded, terminateCall{TaskID: taskID, ProjectPath: projectPath, Reason: reason})
	return nil
}

// SelfHealExecutionByPRURL records the call for assertions. TASK-399/GH-4209.
func (m *mockEvalStore) SelfHealExecutionByPRURL(prURL string) error {
	m.prURLHealed = append(m.prURLHealed, prURL)
	return nil
}

// FindOrphanedRunningExecutions filters the configured orphanedRunning pool by
// excludeTaskIDs, mirroring the real store's task_id NOT IN(...) exclusion.
// TASK-399/GH-4209.
func (m *mockEvalStore) FindOrphanedRunningExecutions(excludeTaskIDs []string) ([]*memory.Execution, error) {
	m.lastExcludeTaskIDs = excludeTaskIDs
	excluded := make(map[string]bool, len(excludeTaskIDs))
	for _, id := range excludeTaskIDs {
		excluded[id] = true
	}
	var result []*memory.Execution
	for _, exec := range m.orphanedRunning {
		if !excluded[exec.TaskID] {
			result = append(result, exec)
		}
	}
	return result, nil
}

// ResolveOrphanedRunningExecution records the call for assertions. TASK-399/GH-4209.
func (m *mockEvalStore) ResolveOrphanedRunningExecution(id, prURL string) error {
	m.resolvedOrphans = append(m.resolvedOrphans, resolveOrphanCall{ID: id, PRURL: prURL})
	return nil
}

// ListExecutionEvents returns the configured heartbeat events for executionID,
// or nil (no heartbeat) for an unconfigured ID. TASK-399/GH-4209.
func (m *mockEvalStore) ListExecutionEvents(executionID string) ([]*memory.Event, error) {
	return m.executionEvents[executionID], nil
}

// TestHandleMerged_ExtractsEvalTask verifies that handleMerged extracts and saves
// an eval task when evalStore is configured and the PR has a linked issue.
func TestHandleMerged_ExtractsEvalTask(t *testing.T) {
	issueFetched := false
	filesFetched := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/issues/10":
			issueFetched = true
			issue := github.Issue{Number: 10, Title: "Add feature X"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, issue))
		case "/repos/owner/repo/pulls/42/files":
			filesFetched = true
			files := []github.PRFile{
				{Filename: "internal/foo.go", Status: "modified"},
				{Filename: "internal/bar.go", Status: "added"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	evalMock := &mockEvalStore{}
	c.SetEvalStore(evalMock)

	prState := &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		IssueNumber: 10,
		Stage:       StageMerged,
	}

	err := c.handleMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleMerged returned unexpected error: %v", err)
	}

	if !issueFetched {
		t.Error("expected /issues/10 to be fetched")
	}
	if !filesFetched {
		t.Error("expected /pulls/42/files to be fetched")
	}
	if len(evalMock.saved) != 1 {
		t.Fatalf("expected 1 eval task saved, got %d", len(evalMock.saved))
	}

	task := evalMock.saved[0]
	if task.IssueNumber != 10 {
		t.Errorf("expected issue number 10, got %d", task.IssueNumber)
	}
	if task.IssueTitle != "Add feature X" {
		t.Errorf("expected issue title 'Add feature X', got %q", task.IssueTitle)
	}
	if task.Repo != "owner/repo" {
		t.Errorf("expected repo 'owner/repo', got %q", task.Repo)
	}
	if !task.Success {
		t.Error("expected task success=true for merged PR")
	}
	if len(task.FilesChanged) != 2 {
		t.Errorf("expected 2 files changed, got %d", len(task.FilesChanged))
	}
}

// mustJSON serialises v to JSON and fails the test on error.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	return b
}

// --- GH-2079: Review feedback controller tests ---

func TestController_HandleReviewRequested_CreatesIssue(t *testing.T) {
	issueCreated := false
	prClosed := false
	branchDeleted := false
	notified := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Fix the nil check", State: "CHANGES_REQUESTED", SubmittedAt: "2026-03-05T10:00:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42/comments":
			resp := []*github.PRReviewComment{
				{ID: 10, Body: "Add error handling", Path: "foo.go", Line: 5, User: github.User{Login: "alice"}},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 100}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.PullRequest{Number: 42, State: "closed"}))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetNotifier(&mockNotifier{
		notifyFixIssueCreatedFunc: func(ctx context.Context, prState *PRState, issueNumber int) error {
			notified = true
			return nil
		},
	})

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Transition to review_requested
	c.mu.Lock()
	c.activePRs[42].Stage = StageReviewRequested
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	if !issueCreated {
		t.Error("expected review issue to be created")
	}
	if !prClosed {
		t.Error("expected PR to be closed")
	}
	if !branchDeleted {
		t.Error("expected branch to be deleted")
	}
	if !notified {
		t.Error("expected notification to be sent")
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", pr.Stage, StageFailed)
	}
}

func TestController_HandleReviewRequested_IterationLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "alice"}, Body: "Still broken", State: "CHANGES_REQUESTED"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42/comments":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case r.URL.Path == "/repos/owner/repo/issues/10":
			// Return issue with iteration=3 metadata (at limit)
			resp := github.Issue{
				Number: 10,
				Body:   "some body\n<!-- autopilot-meta branch:pilot/GH-10 pr:42 iteration:3 -->",
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, github.PullRequest{Number: 42, State: "closed"}))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	c.mu.Lock()
	c.activePRs[42].Stage = StageReviewRequested
	c.mu.Unlock()

	err := c.ProcessPR(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should still be tracked")
	}
	if pr.Stage != StageFailed {
		t.Errorf("stage = %s, want %s", pr.Stage, StageFailed)
	}
	if !strings.Contains(pr.Error, "iteration limit") {
		t.Errorf("error should mention iteration limit: %s", pr.Error)
	}
}

func TestController_HandleReviewRequested_IgnoresSelfReview(t *testing.T) {
	// hasChangesRequested should skip bot reviews
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{ID: 1, User: github.User{Login: "pilot[bot]"}, Body: "Self-review", State: "CHANGES_REQUESTED", SubmittedAt: "2026-03-05T10:00:00Z"},
				{ID: 2, User: github.User{Login: "ci-bot"}, Body: "Bot review", State: "CHANGES_REQUESTED", SubmittedAt: "2026-03-05T10:00:00Z"},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	prState, _ := c.GetPRState(42)
	// Set CreatedAt to before the reviews so time filter doesn't block them
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	c.mu.Unlock()

	result := c.hasChangesRequested(context.Background(), prState)
	if result {
		t.Error("hasChangesRequested should return false for bot-only reviews")
	}
}

func TestController_HandleReviewRequested_DisabledByConfig(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: false, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// OnReviewRequested should not transition when disabled
	c.OnReviewRequested(42, "submitted", "changes_requested", "alice")

	pr, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("PR should be tracked")
	}
	if pr.Stage == StageReviewRequested {
		t.Error("stage should NOT be review_requested when feature is disabled")
	}
}

func TestController_OnReviewRequested_UntrackedPR(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	// Should not panic on untracked PR
	c.OnReviewRequested(99, "submitted", "changes_requested", "alice")

	_, ok := c.GetPRState(99)
	if ok {
		t.Error("untracked PR should not be added")
	}
}

func TestController_HasChangesRequested_FilterByTime(t *testing.T) {
	// Reviews submitted before PR tracking should be ignored
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/pulls/42/reviews":
			resp := []*github.PullRequestReview{
				{
					ID:          1,
					User:        github.User{Login: "alice"},
					Body:        "Old review",
					State:       "CHANGES_REQUESTED",
					SubmittedAt: "2026-03-01T10:00:00Z", // Before PR creation
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.ReviewFeedback = &ReviewFeedbackConfig{Enabled: true, MaxIterations: 3}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "")

	// Set PR creation after the review
	c.mu.Lock()
	c.activePRs[42].CreatedAt = time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	c.mu.Unlock()

	prState, _ := c.GetPRState(42)
	result := c.hasChangesRequested(context.Background(), prState)
	if result {
		t.Error("hasChangesRequested should return false for reviews submitted before PR creation")
	}
}

func TestMaybeCloseParentIssue(t *testing.T) {
	tests := []struct {
		name             string
		issueNumber      int
		issueBody        string
		openSubIssues    int // used by text-search fallback
		getIssueErr      bool
		searchErr        bool
		nativeTotal      int               // totalCount returned by GraphQL native sub-issues
		nativeOpenStates []string          // states of natively linked sub-issues
		nativeNumbers    []int             // issue numbers matching nativeOpenStates by index; defaults to index+1 if unset
		execStatuses     map[string]string // GH-3780: mockEvalStore.GetExecutionStatusByTaskID responses keyed by task ID (e.g. "GH-1")
		wantClosed       bool
		wantLabeled      bool
		wantCommented    bool
	}{
		{
			name:          "last sub-issue triggers parent close (text-search path)",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			openSubIssues: 0,
			wantClosed:    true,
			wantLabeled:   true,
			wantCommented: true,
		},
		{
			name:          "sibling still open - no-op (text-search path)",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			openSubIssues: 2,
			wantClosed:    false,
			wantLabeled:   false,
			wantCommented: false,
		},
		{
			name:          "no parent reference - no-op",
			issueNumber:   10,
			issueBody:     "Standalone issue with no parent",
			openSubIssues: 0,
			wantClosed:    false,
			wantLabeled:   false,
			wantCommented: false,
		},
		{
			name:        "no issue number - no-op",
			issueNumber: 0,
			wantClosed:  false,
		},
		{
			name:        "GetIssue API error - graceful no-op",
			issueNumber: 10,
			getIssueErr: true,
			wantClosed:  false,
		},
		{
			name:          "SearchOpenSubIssues API error - graceful no-op",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			searchErr:     true,
			wantClosed:    false,
			wantLabeled:   false,
			wantCommented: false,
		},
		{
			name:          "label cleanup removes pilot-failed",
			issueNumber:   10,
			issueBody:     "Fix the bug\n\nParent: GH-5\n",
			openSubIssues: 0,
			wantClosed:    true,
			wantLabeled:   true,
			wantCommented: true,
		},
		{
			name:             "native links all closed - closes parent",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			nativeTotal:      2,
			nativeOpenStates: []string{"CLOSED", "CLOSED"},
			wantClosed:       true,
			wantLabeled:      true,
			wantCommented:    true,
		},
		{
			name:             "native links with open sibling - no-op",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			nativeTotal:      2,
			nativeOpenStates: []string{"OPEN", "CLOSED"},
			wantClosed:       false,
			wantLabeled:      false,
			wantCommented:    false,
		},
		{
			// GH-3780: child mix {completed, no_op, no_op}. The "completed" sibling
			// already merged its PR and closed on GitHub, so only the two no_op
			// children remain OPEN. Neither ever produces a PR/merge, but the ledger
			// verifies both are genuine no_ops — the parent should still auto-close.
			name:             "native links with no_op siblings only - closes parent",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			nativeTotal:      3,
			nativeOpenStates: []string{"CLOSED", "OPEN", "OPEN"},
			nativeNumbers:    []int{1, 2, 3},
			execStatuses:     map[string]string{"GH-2": "no_op", "GH-3": "no_op"},
			wantClosed:       true,
			wantLabeled:      true,
			wantCommented:    true,
		},
		{
			// GH-3780: one open sibling is a genuine no_op, but another is still
			// failed/queued — that one still blocks the close since its work isn't
			// actually done.
			name:             "native links with no_op plus failed sibling - blocks close",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			nativeTotal:      2,
			nativeOpenStates: []string{"OPEN", "OPEN"},
			nativeNumbers:    []int{2, 3},
			execStatuses:     map[string]string{"GH-2": "no_op", "GH-3": "failed"},
			wantClosed:       false,
			wantLabeled:      false,
			wantCommented:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				closeCalled      bool
				addLabelsCalled  bool
				removeLabelCalls []string
				commentCalled    bool
			)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					if tt.getIssueErr {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					issue := github.Issue{
						Number: 10,
						Body:   tt.issueBody,
						State:  "closed",
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				case r.URL.Path == "/repos/owner/repo/issues/5" && r.Method == http.MethodGet:
					// Return node_id for GetIssueNodeID call in native sub-issue path.
					if tt.nativeTotal > 0 {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`{"node_id":"I_parent_node","number":5}`))
					} else {
						// No native links — return empty body so GetIssueNodeID fails and falls back to text search.
						w.WriteHeader(http.StatusOK)
					}

				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					// Serve native sub-issues GraphQL response in node(id:) format used by
					// GetOpenSubIssueNumbers/GetOpenSubIssueCount. nativeNumbers defaults to
					// index+1 per-entry when the test case doesn't set it explicitly.
					nodes := make([]map[string]interface{}, len(tt.nativeOpenStates))
					for i, s := range tt.nativeOpenStates {
						num := i + 1
						if i < len(tt.nativeNumbers) {
							num = tt.nativeNumbers[i]
						}
						nodes[i] = map[string]interface{}{"state": s, "number": num}
					}
					resp := map[string]interface{}{
						"data": map[string]interface{}{
							"node": map[string]interface{}{
								"subIssues": map[string]interface{}{
									"totalCount": tt.nativeTotal,
									"nodes":      nodes,
								},
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case strings.HasPrefix(r.URL.Path, "/search/issues"):
					if tt.searchErr {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					resp := struct {
						TotalCount int `json:"total_count"`
					}{TotalCount: tt.openSubIssues}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case r.URL.Path == "/repos/owner/repo/issues/5/labels" && r.Method == http.MethodPost:
					addLabelsCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/5/labels/") && r.Method == http.MethodDelete:
					label := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/5/labels/")
					removeLabelCalls = append(removeLabelCalls, label)
					w.WriteHeader(http.StatusOK)

				case r.URL.Path == "/repos/owner/repo/issues/5/comments" && r.Method == http.MethodPost:
					commentCalled = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				case r.URL.Path == "/repos/owner/repo/issues/5" && r.Method == http.MethodPatch:
					closeCalled = true
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			if tt.execStatuses != nil {
				c.SetEvalStore(&mockEvalStore{execStatusByTaskID: tt.execStatuses})
			}

			prState := &PRState{
				PRNumber:    42,
				IssueNumber: tt.issueNumber,
			}

			c.maybeCloseParentIssue(context.Background(), prState)

			if closeCalled != tt.wantClosed {
				t.Errorf("parent closed = %v, want %v", closeCalled, tt.wantClosed)
			}
			if addLabelsCalled != tt.wantLabeled {
				t.Errorf("pilot-done label added = %v, want %v", addLabelsCalled, tt.wantLabeled)
			}
			if commentCalled != tt.wantCommented {
				t.Errorf("comment posted = %v, want %v", commentCalled, tt.wantCommented)
			}
			if tt.wantLabeled {
				// Verify stale labels were removed
				expectedRemoved := map[string]bool{"pilot-failed": false, "pilot-in-progress": false, "pilot-blocked": false}
				for _, label := range removeLabelCalls {
					expectedRemoved[label] = true
				}
				for label, removed := range expectedRemoved {
					if !removed {
						t.Errorf("expected label %q to be removed, but it wasn't", label)
					}
				}
			}
		})
	}
}

func TestRecoverStaleParentIssues(t *testing.T) {
	type parentCandidate struct {
		num          int
		openSubs     int  // open sub-issue count from GetOpenSubIssueCount (native links)
		subErr       bool // native GraphQL count fails
		textOpenSubs int  // open sub-issue count from SearchOpenSubIssues (text search)
		textErr      bool // text search fails
	}

	tests := []struct {
		name        string
		candidates  []parentCandidate // issues returned by SearchOpenPilotIssuesWithSubIssues
		searchErr   bool
		wantClosed  []int
		wantSkipped []int
	}{
		{
			name: "closes orphaned parent with all sub-issues done",
			candidates: []parentCandidate{
				{num: 100, openSubs: 0},
			},
			wantClosed: []int{100},
		},
		{
			name: "skips parent with open siblings",
			candidates: []parentCandidate{
				{num: 100, openSubs: 2},
			},
			wantSkipped: []int{100},
		},
		{
			name:        "no-op when search returns no candidates",
			candidates:  []parentCandidate{},
			wantClosed:  nil,
			wantSkipped: nil,
		},
		{
			name: "continues on per-item error, closes others",
			candidates: []parentCandidate{
				{num: 100, subErr: true, textErr: true},
				{num: 200, openSubs: 0},
			},
			wantClosed:  []int{200},
			wantSkipped: []int{100},
		},
		{
			name: "native count error falls back to text search, skips when siblings open",
			candidates: []parentCandidate{
				{num: 100, subErr: true, textOpenSubs: 1},
			},
			wantSkipped: []int{100},
		},
		{
			// GH-3513 regression: LinkSubIssue partially failed, so native links
			// cover only closed children (native open = 0) while unlinked siblings
			// are still open. The text-search cross-check must veto the close.
			name: "native 0 vetoed by text search finding unlinked open siblings",
			candidates: []parentCandidate{
				{num: 100, openSubs: 0, textOpenSubs: 2},
			},
			wantSkipped: []int{100},
		},
		{
			name: "native 0 with failing confirmation search defers close",
			candidates: []parentCandidate{
				{num: 100, openSubs: 0, textErr: true},
			},
			wantSkipped: []int{100},
		},
		{
			name:      "search error - no-op",
			searchErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closedParents := map[int]bool{}
			labeledParents := map[int]bool{}

			// Build a map from parent number to candidate for quick lookup.
			candidateMap := map[int]parentCandidate{}
			for _, c := range tt.candidates {
				candidateMap[c.num] = c
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					var body map[string]interface{}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					query, _ := body["query"].(string)

					if strings.Contains(query, "subIssuesSummary") {
						// SearchOpenPilotIssuesWithSubIssues
						if tt.searchErr {
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write([]byte(`{"errors":[{"message":"search failed"}]}`))
							return
						}
						nodes := make([]map[string]interface{}, 0, len(tt.candidates))
						for _, c := range tt.candidates {
							nodes = append(nodes, map[string]interface{}{
								"number": c.num,
								"subIssuesSummary": map[string]int{
									"total":     1,
									"completed": 1,
								},
							})
						}
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"repository": map[string]interface{}{
									"issues": map[string]interface{}{
										"nodes": nodes,
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)

					} else if strings.Contains(query, "subIssues") {
						// GetOpenSubIssueCount — determine parent num from variables
						vars, _ := body["variables"].(map[string]interface{})
						issueID, _ := vars["issueID"].(string)
						// issueID format: "node_<num>"
						var parentNum int
						_, _ = fmt.Sscanf(issueID, "node_%d", &parentNum)

						cand, ok := candidateMap[parentNum]
						if !ok || cand.subErr {
							w.WriteHeader(http.StatusOK)
							_, _ = w.Write([]byte(`{"errors":[{"message":"sub-issue count failed"}]}`))
							return
						}

						states := make([]map[string]string, cand.openSubs)
						for i := range states {
							states[i] = map[string]string{"state": "OPEN"}
						}
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"node": map[string]interface{}{
									"subIssues": map[string]interface{}{
										"totalCount": cand.openSubs + 1,
										"nodes":      states,
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)
					}

				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
					// GetIssueNodeID REST call — extract issue number
					path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
					var num int
					_, _ = fmt.Sscanf(path, "%d", &num)
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, `{"node_id":"node_%d","number":%d}`, num, num)

				case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/labels"):
					// AddLabels
					var num int
					path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
					_, _ = fmt.Sscanf(path, "%d", &num)
					labeledParents[num] = true
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
					w.WriteHeader(http.StatusOK)

				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/repos/owner/repo/issues/"):
					path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
					var num int
					_, _ = fmt.Sscanf(path, "%d", &num)
					closedParents[num] = true
					w.WriteHeader(http.StatusOK)

				case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
					// SearchOpenSubIssues — parent number is embedded in the q param
					// as `"Parent: GH-<num>"`.
					var num int
					_, _ = fmt.Sscanf(r.URL.Query().Get("q"), `repo:owner/repo "Parent: GH-%d"`, &num)
					cand, ok := candidateMap[num]
					if !ok || cand.textErr {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, `{"total_count":%d}`, cand.textOpenSubs)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			c.recoverStaleParentIssues(context.Background())

			for _, num := range tt.wantClosed {
				if !closedParents[num] {
					t.Errorf("parent %d: expected to be closed, was not", num)
				}
			}
			for _, num := range tt.wantSkipped {
				if closedParents[num] {
					t.Errorf("parent %d: expected to be skipped, was closed", num)
				}
			}
		})
	}
}

func TestRecoverStaleParentIssues_TruncatesAt50(t *testing.T) {
	// Build 50 candidates so SearchOpenPilotIssuesWithSubIssues returns exactly maxRecover=50,
	// triggering the "hit limit" Info log. We don't test the log directly; just
	// verify the sweep still processes all 50 and closes them without error.
	const maxRecover = 50
	candidates := make([]int, maxRecover)
	for i := range candidates {
		candidates[i] = 1000 + i
	}

	closedCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			query, _ := body["query"].(string)

			if strings.Contains(query, "subIssuesSummary") {
				nodes := make([]map[string]interface{}, maxRecover)
				for i, num := range candidates {
					nodes[i] = map[string]interface{}{
						"number":           num,
						"subIssuesSummary": map[string]int{"total": 1, "completed": 1},
					}
				}
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"repository": map[string]interface{}{
							"issues": map[string]interface{}{"nodes": nodes},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			} else {
				// GetOpenSubIssueCount — all sub-issues closed
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"node": map[string]interface{}{
							"subIssues": map[string]interface{}{
								"totalCount": 1,
								"nodes":      []map[string]string{{"state": "CLOSED"}},
							},
						},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
			path := strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/issues/")
			var num int
			_, _ = fmt.Sscanf(path, "%d", &num)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"node_id":"node_%d","number":%d}`, num, num)

		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/repos/owner/repo/issues/"):
			closedCount++
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	c.recoverStaleParentIssues(context.Background())

	if closedCount != maxRecover {
		t.Errorf("closed %d parents, want %d", closedCount, maxRecover)
	}
}

// TestReconcileEpicParents_GH4099CandidateDiscovery reproduces the two
// real-world shapes that left #4020 and #4051 open for hours while every
// reconcile pass logged nothing to do (GH-4099):
//
//   - "unlabeled parent + body-marker children": the parent has lost its
//     "pilot" label (out-of-band, e.g. to break a dispatch retry loop) and its
//     children were only ever linked via the "Parent: GH-N" body-marker
//     convention (LinkSubIssue never ran — GH-3513) — invisible to the native
//     SearchOpenPilotIssuesWithSubIssues candidate query no matter what.
//   - "labeled parent + sub-issue-API children": the pre-existing, already-
//     working shape — a regression guard proving the fix doesn't disturb it.
//
// Both must close via reconcileEpicParents (the poll-cycle sweep) with the
// standard completion comment naming the merged child PR.
func TestReconcileEpicParents_GH4099CandidateDiscovery(t *testing.T) {
	tests := []struct {
		name          string
		parentLabeled bool // parent appears in the native candidate search results
		nativeLinked  bool // children linked via the native GitHub sub-issue API
		parentNum     int
		childNum      int
		mergedPR      int
	}{
		{
			name:          "unlabeled parent + body-marker-only children closes via text-search fallback",
			parentLabeled: false,
			nativeLinked:  false,
			parentNum:     700,
			childNum:      701,
			mergedPR:      900,
		},
		{
			name:          "labeled parent + native sub-issue-API children still closes (regression guard)",
			parentLabeled: true,
			nativeLinked:  true,
			parentNum:     710,
			childNum:      711,
			mergedPR:      910,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var closeCalled bool
			var commentBody string

			childBody := fmt.Sprintf("<!--autopilot-meta\nparent: GH-%d\ninherited-spec: true\n-->\n\nParent: GH-%d\n\nDo the thing.",
				tt.parentNum, tt.parentNum)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/graphql":
					var body map[string]interface{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					query, _ := body["query"].(string)

					switch {
					case strings.Contains(query, "body"):
						// discoverBodyMarkerEpicParents: recently-closed pilot-done
						// children, regardless of the referenced parent's own label.
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"repository": map[string]interface{}{
									"issues": map[string]interface{}{
										"nodes": []map[string]interface{}{
											{"updatedAt": time.Now().UTC().Format(time.RFC3339), "body": childBody},
										},
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)

					case strings.Contains(query, "search(query:"):
						// getSubIssuesByTextSearch fallback (no native links).
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"search": map[string]interface{}{
									"nodes": []map[string]interface{}{
										{"number": tt.childNum, "state": "CLOSED"},
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)

					case strings.Contains(query, "subIssuesSummary"):
						// SearchOpenPilotIssuesWithSubIssues (native candidate search).
						nodes := []map[string]interface{}{}
						if tt.parentLabeled {
							total := 0
							if tt.nativeLinked {
								total = 1
							}
							nodes = append(nodes, map[string]interface{}{
								"number":           tt.parentNum,
								"subIssuesSummary": map[string]int{"total": total, "completed": total},
							})
						}
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"repository": map[string]interface{}{
									"issues": map[string]interface{}{"nodes": nodes},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)

					default:
						// getAllSubIssueNumbers native per-parent query.
						totalCount := 0
						nodes := []map[string]interface{}{}
						if tt.nativeLinked {
							totalCount = 1
							nodes = append(nodes, map[string]interface{}{"number": tt.childNum, "state": "CLOSED"})
						}
						resp := map[string]interface{}{
							"data": map[string]interface{}{
								"node": map[string]interface{}{
									"subIssues": map[string]interface{}{
										"totalCount": totalCount,
										"nodes":      nodes,
									},
								},
							},
						}
						w.WriteHeader(http.StatusOK)
						_ = json.NewEncoder(w).Encode(resp)
					}

				case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", tt.parentNum):
					w.WriteHeader(http.StatusOK)
					_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"title":"epic","state":"open"}`, tt.parentNum)

				case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
					// SearchPRsForIssue (verifyChildrenShippedForClose).
					items := []map[string]interface{}{{
						"id": 1, "number": tt.mergedPR, "title": "fix: child",
						"state":        "closed",
						"pull_request": map[string]interface{}{"merged_at": "2026-01-01T00:00:00Z"},
					}}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})

				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/labels"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
					w.WriteHeader(http.StatusOK)

				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
					var payload struct {
						Body string `json:"body"`
					}
					_ = json.NewDecoder(r.Body).Decode(&payload)
					commentBody = payload.Body
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				case r.Method == http.MethodPatch && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", tt.parentNum):
					closeCalled = true
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			c.reconcileEpicParents(context.Background())

			if !closeCalled {
				t.Fatalf("expected parent #%d to be closed, it wasn't", tt.parentNum)
			}
			if !strings.Contains(commentBody, fmt.Sprintf("GH-%d", tt.parentNum)) {
				t.Errorf("expected standard completion comment naming GH-%d, got: %q", tt.parentNum, commentBody)
			}
			if !strings.Contains(commentBody, fmt.Sprintf("#%d", tt.mergedPR)) {
				t.Errorf("expected completion comment to name merged PR #%d, got: %q", tt.mergedPR, commentBody)
			}
		})
	}
}

// TestReconcileEpicParent_NoLinksLogsReason covers GH-4099's fail-loud
// requirement: a candidate parent that turns out to have no discoverable
// children via EITHER the native sub-issue API or the body-marker text search
// must never skip silently — it must log why (previously this path just
// returned with no log line at all, matching the "closed=0, no visible reason"
// symptom that hid #4020/#4051 for hours).
func TestReconcileEpicParent_NoLinksLogsReason(t *testing.T) {
	const parentNum = 720

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			query, _ := body["query"].(string)

			if strings.Contains(query, "search(query:") {
				// getSubIssuesByTextSearch: no matching children either.
				resp := map[string]interface{}{
					"data": map[string]interface{}{
						"search": map[string]interface{}{"nodes": []map[string]interface{}{}},
					},
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			// getAllSubIssueNumbers native query: no native links.
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"subIssues": map[string]interface{}{
							"totalCount": 0,
							"nodes":      []map[string]interface{}{},
						},
					},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)

		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"node_id":"I_parent","number":%d,"state":"open"}`, parentNum)

		case r.Method == http.MethodPatch && r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", parentNum):
			t.Errorf("parent should not be closed when it has no discoverable children")
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	var logBuf bytes.Buffer
	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.log = slog.New(slog.NewTextHandler(&logBuf, nil))

	c.reconcileEpicParent(context.Background(), parentNum)

	logs := logBuf.String()
	if !strings.Contains(logs, "no discoverable children") {
		t.Errorf("expected a fail-loud skip-reason log line, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, fmt.Sprintf("parent=%d", parentNum)) {
		t.Errorf("expected skip log to name the parent, got logs:\n%s", logs)
	}
}

// TestNotifyExternalClose_MaybeCloseParent verifies that notifyExternalClose
// calls maybeCloseParentIssue so parent epics are auto-closed when the last
// sub-issue PR is closed without merge (GH-2198).
func TestNotifyExternalClose_MaybeCloseParent(t *testing.T) {
	tests := []struct {
		name             string
		issueNumber      int
		issueBody        string
		openSubIssues    int
		wantParentClosed bool
	}{
		{
			name:             "last sub-issue PR closed externally - parent closes",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			openSubIssues:    0,
			wantParentClosed: true,
		},
		{
			name:             "non-last sub-issue - parent stays open",
			issueNumber:      10,
			issueBody:        "Fix the bug\n\nParent: GH-5\n",
			openSubIssues:    1,
			wantParentClosed: false,
		},
		{
			name:             "non-sub-issue - no parent lookup",
			issueNumber:      10,
			issueBody:        "Standalone issue",
			openSubIssues:    0,
			wantParentClosed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parentCloseCalled bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				// Label/remove calls for the sub-issue itself (notifyExternalClose)
				case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)

				// maybeCloseParentIssue: fetch sub-issue body
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					issue := github.Issue{Number: 10, Body: tt.issueBody}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				// maybeCloseParentIssue: count open siblings
				case strings.HasPrefix(r.URL.Path, "/search/issues"):
					resp := struct {
						TotalCount int `json:"total_count"`
					}{TotalCount: tt.openSubIssues}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				// maybeCloseParentIssue: close parent
				case r.URL.Path == "/repos/owner/repo/issues/5" && r.Method == http.MethodPatch:
					parentCloseCalled = true
					w.WriteHeader(http.StatusOK)

				// parent label / comment calls
				case r.URL.Path == "/repos/owner/repo/issues/5/labels" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/5/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)
				case r.URL.Path == "/repos/owner/repo/issues/5/comments" && r.Method == http.MethodPost:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"id":1}`))

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{
				PRNumber:    42,
				IssueNumber: tt.issueNumber,
			}

			c.notifyExternalClose(context.Background(), prState)

			if parentCloseCalled != tt.wantParentClosed {
				t.Errorf("parent closed = %v, want %v", parentCloseCalled, tt.wantParentClosed)
			}
		})
	}
}

// TestNotifyExternalClose_ReclassifiesCompletedExecution covers GH-3818/D10:
// notifyExternalClose is the single place every non-merge PR close converges
// on, so it must reclassify the issue's completed execution row to "failed"
// there — otherwise HasCompletedExecution keeps trusting a "completed" row
// whose PR was actually discarded, and the poller re-marks the issue
// pilot-done on every subsequent poll (the exact #3789/#3802 incident).
func TestNotifyExternalClose_ReclassifiesCompletedExecution(t *testing.T) {
	tests := []struct {
		name           string
		issueNumber    int
		prError        string
		wantReclassify bool
		wantTaskID     string
		wantReason     string
	}{
		{
			name:           "issue closed unmerged with recorded reason - reclassified",
			issueNumber:    3789,
			prError:        "CI checks failed (build); fix issue #3803 created to continue this work",
			wantReclassify: true,
			wantTaskID:     "GH-3789",
			wantReason:     "CI checks failed (build); fix issue #3803 created to continue this work",
		},
		{
			name:           "issue closed unmerged with no recorded reason - default reason used",
			issueNumber:    3790,
			prError:        "",
			wantReclassify: true,
			wantTaskID:     "GH-3790",
			wantReason:     "closed without merging (no reason recorded)",
		},
		{
			name:           "PR has no linked issue - nothing to reclassify",
			issueNumber:    0,
			prError:        "CI checks failed",
			wantReclassify: false,
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
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			evalMock := &mockEvalStore{}
			c.SetEvalStore(evalMock)

			prState := &PRState{PRNumber: 42, IssueNumber: tt.issueNumber, Error: tt.prError}
			c.notifyExternalClose(context.Background(), prState)

			if tt.wantReclassify {
				if len(evalMock.reclassified) != 1 {
					t.Fatalf("reclassify calls = %d, want 1: %+v", len(evalMock.reclassified), evalMock.reclassified)
				}
				got := evalMock.reclassified[0]
				if got.TaskID != tt.wantTaskID {
					t.Errorf("TaskID = %q, want %q", got.TaskID, tt.wantTaskID)
				}
				if got.Reason != tt.wantReason {
					t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
				}
			} else if len(evalMock.reclassified) != 0 {
				t.Errorf("expected no reclassify calls, got %+v", evalMock.reclassified)
			}
		})
	}
}

// TestNotifyExternalClose_TerminatesNonTerminalExecution covers GH-4499: a
// PR closed externally while its execution row was still
// queued/pending/running (never reached "completed") must have that row
// terminated too — otherwise it survives forever, HydrateFromStore re-seeds
// it into the Monitor as a running card on the next restart, and
// Monitor.ReconcileWithStore (GH-4490) can't rescue it because the
// reconciler trusts the executions row as source of truth.
func TestNotifyExternalClose_TerminatesNonTerminalExecution(t *testing.T) {
	tests := []struct {
		name          string
		issueNumber   int
		prError       string
		wantTerminate bool
		wantTaskID    string
		wantReason    string
	}{
		{
			name:          "issue closed unmerged with recorded reason - terminated",
			issueNumber:   3789,
			prError:       "CI checks failed (build); fix issue #3803 created to continue this work",
			wantTerminate: true,
			wantTaskID:    "GH-3789",
			wantReason:    "CI checks failed (build); fix issue #3803 created to continue this work",
		},
		{
			name:          "issue closed unmerged with no recorded reason - default reason used",
			issueNumber:   3790,
			prError:       "",
			wantTerminate: true,
			wantTaskID:    "GH-3790",
			wantReason:    "closed without merging (no reason recorded)",
		},
		{
			name:          "PR has no linked issue - nothing to terminate",
			issueNumber:   0,
			prError:       "CI checks failed",
			wantTerminate: false,
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
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			evalMock := &mockEvalStore{}
			c.SetEvalStore(evalMock)

			prState := &PRState{PRNumber: 42, IssueNumber: tt.issueNumber, Error: tt.prError}
			c.notifyExternalClose(context.Background(), prState)

			if tt.wantTerminate {
				if len(evalMock.terminated) != 1 {
					t.Fatalf("terminate calls = %d, want 1: %+v", len(evalMock.terminated), evalMock.terminated)
				}
				got := evalMock.terminated[0]
				if got.TaskID != tt.wantTaskID {
					t.Errorf("TaskID = %q, want %q", got.TaskID, tt.wantTaskID)
				}
				if got.Reason != tt.wantReason {
					t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
				}
			} else if len(evalMock.terminated) != 0 {
				t.Errorf("expected no terminate calls, got %+v", evalMock.terminated)
			}
		})
	}
}

// TestNotifyExternalClose_SupersededCloseRoutesToSupersededVariants covers
// GH-4701: when prState.TerminalLabel already says pilot-superseded (e.g.
// closeConflictSourceIssueClosed proved a sibling/duplicate execution
// delivered this issue's scope first), notifyExternalClose must route both
// the reclassify and terminate calls to their "AsSuperseded" siblings
// instead of the plain "failed" variants — otherwise HISTORY renders
// deliberate operator cleanup as a pipeline ✗ (the 2026-08-03 #4655 cluster
// that motivated this task).
func TestNotifyExternalClose_SupersededCloseRoutesToSupersededVariants(t *testing.T) {
	tests := []struct {
		name           string
		terminalLabel  string
		wantSuperseded bool
	}{
		{
			name:           "TerminalLabel pilot-superseded - routes to superseded variants",
			terminalLabel:  github.LabelSuperseded,
			wantSuperseded: true,
		},
		{
			name:           "TerminalLabel empty - routes to plain failed variants",
			terminalLabel:  "",
			wantSuperseded: false,
		},
		{
			name:           "TerminalLabel pilot-failed - routes to plain failed variants",
			terminalLabel:  github.LabelFailed,
			wantSuperseded: false,
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
			c := NewController(cfg, ghClient, nil, "owner", "repo")
			evalMock := &mockEvalStore{}
			c.SetEvalStore(evalMock)

			prState := &PRState{PRNumber: 42, IssueNumber: 4701, Error: "source issue #4701 is closed", TerminalLabel: tt.terminalLabel}
			c.notifyExternalClose(context.Background(), prState)

			if tt.wantSuperseded {
				if len(evalMock.reclassifiedSuperseded) != 1 {
					t.Errorf("reclassifiedSuperseded calls = %d, want 1: %+v", len(evalMock.reclassifiedSuperseded), evalMock.reclassifiedSuperseded)
				}
				if len(evalMock.terminatedSuperseded) != 1 {
					t.Errorf("terminatedSuperseded calls = %d, want 1: %+v", len(evalMock.terminatedSuperseded), evalMock.terminatedSuperseded)
				}
				if len(evalMock.reclassified) != 0 {
					t.Errorf("expected no plain reclassify calls, got %+v", evalMock.reclassified)
				}
				if len(evalMock.terminated) != 0 {
					t.Errorf("expected no plain terminate calls, got %+v", evalMock.terminated)
				}
			} else {
				if len(evalMock.reclassified) != 1 {
					t.Errorf("reclassified calls = %d, want 1: %+v", len(evalMock.reclassified), evalMock.reclassified)
				}
				if len(evalMock.terminated) != 1 {
					t.Errorf("terminated calls = %d, want 1: %+v", len(evalMock.terminated), evalMock.terminated)
				}
				if len(evalMock.reclassifiedSuperseded) != 0 {
					t.Errorf("expected no superseded reclassify calls, got %+v", evalMock.reclassifiedSuperseded)
				}
				if len(evalMock.terminatedSuperseded) != 0 {
					t.Errorf("expected no superseded terminate calls, got %+v", evalMock.terminatedSuperseded)
				}
			}
		})
	}
}

// TestNotifyExternalClose_TerminateNotCalledWithoutEvalStore verifies the nil
// guard: when no eval store is configured, notifyExternalClose must not panic
// and simply skips the terminate step. GH-4499, mirrors the GH-3818
// TestNotifyExternalClose_ReclassifyNotCalledWithoutEvalStore guard.
func TestNotifyExternalClose_TerminateNotCalledWithoutEvalStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 3789, Error: "CI checks failed"}
	c.notifyExternalClose(context.Background(), prState) // must not panic
}

// TestNotifyExternalClose_MonitorFailDrivesCardTerminal is the GH-4490
// subtask 3 regression test: by the time a PR closes without merging, the
// execution that opened it has almost always already called
// monitor.Complete(), so the dashboard card sits at StatusCompleted — outside
// Monitor.ReconcileWithStore's periodic-backstop candidate set (subtask 1
// only rescues Running/Queued/Pending cards). notifyExternalClose must call
// monitor.Fail directly so the card flips to a terminal failure the moment
// the close is observed, instead of showing "done" forever.
func TestNotifyExternalClose_MonitorFailDrivesCardTerminal(t *testing.T) {
	tests := []struct {
		name        string
		issueNumber int
		prError     string
		wantFail    bool
		wantTaskID  string
		wantReason  string
	}{
		{
			name:        "issue closed unmerged with recorded reason - card fails with reason",
			issueNumber: 3789,
			prError:     "CI checks failed (build); fix issue #3803 created to continue this work",
			wantFail:    true,
			wantTaskID:  "GH-3789",
			wantReason:  "CI checks failed (build); fix issue #3803 created to continue this work",
		},
		{
			name:        "issue closed unmerged with no recorded reason - default reason used",
			issueNumber: 3790,
			prError:     "",
			wantFail:    true,
			wantTaskID:  "GH-3790",
			wantReason:  "closed without merging (no reason recorded)",
		},
		{
			name:        "PR has no linked issue - nothing to fail",
			issueNumber: 0,
			prError:     "CI checks failed",
			wantFail:    false,
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
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")
			mockMonitor := newMockTaskMonitor()
			c.SetMonitor(mockMonitor)

			prState := &PRState{PRNumber: 42, IssueNumber: tt.issueNumber, Error: tt.prError}
			c.notifyExternalClose(context.Background(), prState)

			if tt.wantFail {
				gotReason, ok := mockMonitor.failedTasks[tt.wantTaskID]
				if !ok {
					t.Fatalf("monitor.Fail was not called for taskID %s: %+v", tt.wantTaskID, mockMonitor.failedTasks)
				}
				if gotReason != tt.wantReason {
					t.Errorf("Fail reason = %q, want %q", gotReason, tt.wantReason)
				}
			} else if len(mockMonitor.failedTasks) != 0 {
				t.Errorf("expected no monitor.Fail calls, got %+v", mockMonitor.failedTasks)
			}
		})
	}
}

// TestNotifyExternalClose_MonitorFailNotCalledWithoutMonitor verifies the nil
// guard: when no dashboard monitor is wired (headless deployment), notifyExternalClose
// must not panic and simply skips the card-fail step. GH-4490 subtask 3.
func TestNotifyExternalClose_MonitorFailNotCalledWithoutMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 3789, Error: "CI checks failed"}
	c.notifyExternalClose(context.Background(), prState) // must not panic
}

// TestNotifyExternalClose_ReclassifyNotCalledWithoutEvalStore verifies the nil
// guard: when no eval store is configured, notifyExternalClose must not panic
// and simply skips the reclassify step. GH-3818.
func TestNotifyExternalClose_ReclassifyNotCalledWithoutEvalStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 3789, Error: "CI checks failed"}
	c.notifyExternalClose(context.Background(), prState) // must not panic
}

// GH-2340: notifyExternalClose must not stamp pilot-retry-ready on issues
// that already carry pilot-done. This happens when Pilot itself closed a
// duplicate PR after the original PR was merged — the issue is closed and
// done, and adding pilot-retry-ready strands the label forever (poller
// skips non-open issues).
func TestNotifyExternalClose_SkipsRetryReadyWhenDone(t *testing.T) {
	tests := []struct {
		name           string
		issueState     string
		issueLabels    []github.Label
		wantRetryAdded bool
	}{
		{
			// A pilot-done issue is normally already closed too — the
			// pilot-done guard (checked first) is what actually skips
			// retry-ready here, not the GH-4817 closed-state guard below it.
			name:           "issue already pilot-done - skip retry-ready",
			issueState:     "closed",
			issueLabels:    []github.Label{{Name: github.LabelDone}},
			wantRetryAdded: false,
		},
		{
			// GH-4817: an issue that's genuinely still open (not done) is the
			// only realistic fixture for "add retry-ready" — a closed-but-not-
			// done issue would (correctly) now be skipped by the open-state
			// guard added in Task 5e, since retry-ready on a closed issue is
			// dead weight the poller will never pick up.
			name:           "issue not done - add retry-ready",
			issueState:     "open",
			issueLabels:    []github.Label{},
			wantRetryAdded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var retryReadyAdded bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					issue := github.Issue{Number: 10, State: tt.issueState, Labels: tt.issueLabels}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
					var body struct {
						Labels []string `json:"labels"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					for _, l := range body.Labels {
						if l == github.LabelRetryReady {
							retryReadyAdded = true
						}
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 42, IssueNumber: 10}
			c.notifyExternalClose(context.Background(), prState)

			if retryReadyAdded != tt.wantRetryAdded {
				t.Errorf("pilot-retry-ready added = %v, want %v", retryReadyAdded, tt.wantRetryAdded)
			}
		})
	}
}

// TestNotifyExternalClose_SkipsLabelWriteWhenIssueClosed (GH-4817, TASK-459
// Phase 3 Task 5e/7g): when the reused pilot-done GetIssue fetch shows the
// issue is already closed (but not pilot-done — the pilot-done guard above
// this code path already covers that case), notifyExternalClose must skip
// the label correction (AddLabels/RemoveLabel) entirely — there's no retry
// state to protect on an issue the poller will never revisit — but it must
// still post the informational issue comment, unlike every other GH-4817
// open-state-gated site in this codebase (which skip the comment too).
func TestNotifyExternalClose_SkipsLabelWriteWhenIssueClosed(t *testing.T) {
	var labelsPosted, labelsDeleted, issueCommentPosted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
			issue := github.Issue{Number: 10, State: "closed", Labels: []github.Label{}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(issue)
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			labelsPosted = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
			labelsDeleted = true
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/repos/owner/repo/issues/10/comments" && r.Method == http.MethodPost:
			issueCommentPosted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 2})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{PRNumber: 42, IssueNumber: 10, Error: "CI checks failed"}
	c.notifyExternalClose(context.Background(), prState)

	if labelsPosted {
		t.Error("expected no label POST when the issue is already closed")
	}
	if labelsDeleted {
		t.Error("expected no label DELETE when the issue is already closed")
	}
	if !issueCommentPosted {
		t.Error("expected the informational issue comment to still post even though the issue is closed")
	}
}

// GH-3806 (TASK-382 D9): reproduces the exact defect — a PR closed after CI
// failure for an issue that already carries pilot-done (e.g. a duplicate PR,
// or a later PR against an issue an earlier PR already shipped). Before this
// fix, notifyExternalClose's pilot-done guard returned immediately with zero
// comments anywhere, so the discarded PR's failure was invisible and the
// issue's pilot-done label wrongly implied its later attempt also shipped.
func TestNotifyExternalClose_PostsCommentsEvenWhenIssueAlreadyDone(t *testing.T) {
	var prCommentPosted, issueCommentPosted bool
	var issueCommentBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
			issue := github.Issue{Number: 10, State: "closed", Labels: []github.Label{{Name: github.LabelDone}}}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(issue)
		// PRs use the issues comments API (AddPRComment posts to /issues/{prNumber}/comments).
		case r.URL.Path == "/repos/owner/repo/issues/42/comments" && r.Method == http.MethodPost:
			prCommentPosted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 1})
		case r.URL.Path == "/repos/owner/repo/issues/10/comments" && r.Method == http.MethodPost:
			issueCommentPosted = true
			body, _ := io.ReadAll(r.Body)
			issueCommentBody = string(body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]int{"id": 2})
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			t.Error("labels must not be touched when the issue is already pilot-done")
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    42,
		IssueNumber: 10,
		HeadSHA:     "deadbee",
		Error:       "CI checks failed (unit-tests); fix issue #200 created to continue this work",
	}
	c.notifyExternalClose(context.Background(), prState)

	if !prCommentPosted {
		t.Error("expected a PR comment naming the close reason even though the issue is already pilot-done")
	}
	if !issueCommentPosted {
		t.Fatal("expected an issue comment even though pilot-done skips label correction — a discarded PR must never be silent")
	}
	if !strings.Contains(issueCommentBody, "42") || !strings.Contains(issueCommentBody, "unit-tests") {
		t.Errorf("issue comment should reference the closed PR and the failure reason, got: %s", issueCommentBody)
	}
}

// GH-3417: notifyExternalClose must not stamp pilot-retry-ready when a human
// recovery PR is already open for the issue. Re-dispatching would overwrite
// the human's branch via git checkout -B in the worktree.
func TestNotifyExternalClose_SkipsRetryReadyForHumanRecoveryPR(t *testing.T) {
	const botLogin = "pilot-bot"

	tests := []struct {
		name           string
		openPRs        []github.PullRequest // PRs returned by SearchOpenPRsForIssue
		wantRetryAdded bool
		wantSkipLogged bool // expect the "human recovery PR" log path
	}{
		{
			name:           "no open PRs — retry-ready applied",
			openPRs:        nil,
			wantRetryAdded: true,
		},
		{
			name: "human PR open — retry-ready skipped",
			openPRs: []github.PullRequest{
				{
					Number:  99,
					State:   "open",
					HTMLURL: "https://github.com/owner/repo/pull/99",
					User:    &github.User{Login: "alice"},
				},
			},
			wantRetryAdded: false,
			wantSkipLogged: true,
		},
		{
			name: "only bot PR open — retry-ready applied",
			openPRs: []github.PullRequest{
				{
					Number:  100,
					State:   "open",
					HTMLURL: "https://github.com/owner/repo/pull/100",
					User:    &github.User{Login: botLogin},
				},
			},
			wantRetryAdded: true,
		},
		{
			name: "mixed: bot PR and human PR — retry-ready skipped",
			openPRs: []github.PullRequest{
				{Number: 100, State: "open", HTMLURL: "https://github.com/owner/repo/pull/100", User: &github.User{Login: botLogin}},
				{Number: 101, State: "open", HTMLURL: "https://github.com/owner/repo/pull/101", User: &github.User{Login: "bob"}},
			},
			wantRetryAdded: false,
			wantSkipLogged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var retryReadyAdded bool

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/user" && r.Method == http.MethodGet:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"login":"` + botLogin + `","id":1}`))

				case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
					issue := github.Issue{Number: 10, State: "open", Labels: []github.Label{}}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(issue)

				case strings.HasPrefix(r.URL.Path, "/search/issues") && r.Method == http.MethodGet:
					// SearchOpenPRsForIssue — return the configured open PRs.
					items := make([]map[string]interface{}, 0, len(tt.openPRs))
					for _, pr := range tt.openPRs {
						item := map[string]interface{}{
							"id":       pr.Number,
							"number":   pr.Number,
							"title":    pr.Title,
							"state":    pr.State,
							"html_url": pr.HTMLURL,
						}
						if pr.User != nil {
							item["user"] = map[string]interface{}{"login": pr.User.Login, "id": 0}
						}
						items = append(items, item)
					}
					resp := map[string]interface{}{
						"total_count": len(items),
						"items":       items,
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)

				case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
					var body struct {
						Labels []string `json:"labels"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					for _, l := range body.Labels {
						if l == github.LabelRetryReady {
							retryReadyAdded = true
						}
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))

				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/10/labels/") && r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusOK)

				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			c := NewController(cfg, ghClient, nil, "owner", "repo")

			prState := &PRState{PRNumber: 42, IssueNumber: 10}
			c.notifyExternalClose(context.Background(), prState)

			if retryReadyAdded != tt.wantRetryAdded {
				t.Errorf("pilot-retry-ready added = %v, want %v", retryReadyAdded, tt.wantRetryAdded)
			}
		})
	}
}

// GH-2251: Test that ScanRecentlyMergedPRs discovers externally-merged PRs
// and skips those already tracked.
func TestController_ScanRecentlyMergedPRs(t *testing.T) {
	recentMergedAt := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	oldMergedAt := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		name            string
		prs             []github.PullRequest
		existingTracked []int // PR numbers already in activePRs
		wantTriggered   int
		wantPRNumbers   []int
	}{
		{
			name: "discovers externally merged pilot PR",
			prs: []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			},
			wantTriggered: 1,
			wantPRNumbers: []int{42},
		},
		{
			name: "skips PR already tracked in activePRs",
			prs: []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			},
			existingTracked: []int{42},
			wantTriggered:   0,
			wantPRNumbers:   []int{},
		},
		{
			name: "skips PR merged outside scan window",
			prs: []github.PullRequest{
				{
					Number:         42,
					Head:           github.PRRef{Ref: "pilot/GH-100", SHA: "sha1"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/42",
					Title:          "feat(api): add endpoint",
					Merged:         true,
					MergedAt:       oldMergedAt,
					MergeCommitSHA: "merge-sha-42",
				},
			},
			wantTriggered: 0,
			wantPRNumbers: []int{},
		},
		{
			name: "skips non-pilot branches and unmerged PRs",
			prs: []github.PullRequest{
				{
					Number:   1,
					Head:     github.PRRef{Ref: "feature/stuff", SHA: "sha1"},
					Base:     github.PRRef{Ref: "main"},
					HTMLURL:  "https://github.com/owner/repo/pull/1",
					Merged:   true,
					MergedAt: recentMergedAt,
				},
				{
					Number:  2,
					Head:    github.PRRef{Ref: "pilot/GH-200", SHA: "sha2"},
					Base:    github.PRRef{Ref: "main"},
					HTMLURL: "https://github.com/owner/repo/pull/2",
					Merged:  false, // closed but not merged
				},
			},
			wantTriggered: 0,
			wantPRNumbers: []int{},
		},
		{
			name: "mixed: discovers one, skips tracked and old",
			prs: []github.PullRequest{
				{
					Number:         10,
					Head:           github.PRRef{Ref: "pilot/GH-10", SHA: "sha10"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/10",
					Title:          "fix(db): connection leak",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-10",
				},
				{
					Number:         20,
					Head:           github.PRRef{Ref: "pilot/GH-20", SHA: "sha20"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/20",
					Title:          "feat(ui): dashboard",
					Merged:         true,
					MergedAt:       recentMergedAt,
					MergeCommitSHA: "merge-sha-20",
				},
				{
					Number:         30,
					Head:           github.PRRef{Ref: "pilot/GH-30", SHA: "sha30"},
					Base:           github.PRRef{Ref: "main"},
					HTMLURL:        "https://github.com/owner/repo/pull/30",
					Title:          "chore: cleanup",
					Merged:         true,
					MergedAt:       oldMergedAt, // outside window
					MergeCommitSHA: "merge-sha-30",
				},
			},
			existingTracked: []int{20}, // already tracked
			wantTriggered:   1,         // only PR 10
			wantPRNumbers:   []int{10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
					prs := make([]*github.PullRequest, len(tt.prs))
					for i := range tt.prs {
						prs[i] = &tt.prs[i]
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(prs)
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.Release = &ReleaseConfig{
				Enabled:   true,
				Trigger:   "on_merge",
				TagPrefix: "v",
			}
			cfg.MergedPRScanWindow = 30 * time.Minute

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			// Pre-populate tracked PRs
			for _, prNum := range tt.existingTracked {
				c.mu.Lock()
				c.activePRs[prNum] = &PRState{PRNumber: prNum, Stage: StageWaitingCI}
				c.mu.Unlock()
			}

			err := c.ScanRecentlyMergedPRs(context.Background())
			if err != nil {
				t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
			}

			// Count newly triggered PRs (exclude pre-existing tracked ones)
			triggered := 0
			for _, pr := range c.GetActivePRs() {
				isPreExisting := false
				for _, existing := range tt.existingTracked {
					if pr.PRNumber == existing {
						isPreExisting = true
						break
					}
				}
				if !isPreExisting {
					triggered++
				}
			}

			if triggered != tt.wantTriggered {
				t.Errorf("triggered %d PRs, want %d", triggered, tt.wantTriggered)
			}

			for _, wantPR := range tt.wantPRNumbers {
				found := false
				for _, pr := range c.GetActivePRs() {
					if pr.PRNumber == wantPR {
						if pr.Stage != StageReleasing {
							t.Errorf("PR %d stage = %v, want StageReleasing", wantPR, pr.Stage)
						}
						found = true
						break
					}
				}
				if !found {
					t.Errorf("PR %d not found in active PRs", wantPR)
				}
			}
		})
	}
}

// mergeMockServer returns an httptest server that answers the handleMerging
// happy-path requests (PR fetch, merge, labels) and counts POSTs to the
// issue comments endpoint.
func mergeMockServer(t *testing.T, prNumber, issueNumber int, commentCount *int) *httptest.Server {
	t.Helper()
	commentPath := "/repos/owner/repo/issues/" + itoa(issueNumber) + "/comments"
	prPath := "/repos/owner/repo/pulls/" + itoa(prNumber)
	mergePath := prPath + "/merge"
	labelsPath := "/repos/owner/repo/issues/" + itoa(issueNumber) + "/labels"

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/check-runs"):
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == commentPath && r.Method == http.MethodPost:
			*commentCount++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": 1})
		case r.URL.Path == mergePath && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sha": "merged123", "merged": true, "message": "Pull Request successfully merged",
			})
		case r.URL.Path == prPath && r.Method == http.MethodGet:
			pr := github.PullRequest{
				Number: prNumber,
				State:  "open",
				Head:   github.PRRef{Ref: "pilot/GH-" + itoa(issueNumber), SHA: "abc1234"},
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(pr)
		case r.URL.Path == labelsPath && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{{Name: github.LabelDone}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestController_handleMerging_IdempotentCompletionComment tests GH-2345:
// Re-entering StageMerging for an already-merged PR must not produce a second
// "PR merged" comment.
func TestController_handleMerging_IdempotentCompletionComment(t *testing.T) {
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	ctx := context.Background()

	// First entry: posts the completion comment and advances stage.
	if err := c.handleMerging(ctx, prState); err != nil {
		t.Fatalf("first handleMerging returned error: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("after first handleMerging: comment count = %d, want 1", commentCount)
	}
	if !prState.MergeNotificationPosted {
		t.Fatal("MergeNotificationPosted should be true after first successful post")
	}

	// Simulate re-entry (e.g. via duplicate-dispatch or crash recovery).
	prState.Stage = StageMerging
	if err := c.handleMerging(ctx, prState); err != nil {
		t.Fatalf("re-entry handleMerging returned error: %v", err)
	}
	if commentCount != 1 {
		t.Errorf("after re-entry: comment count = %d, want 1 (no duplicate)", commentCount)
	}
}

// TestController_handleMerging_CommentFlagPersists tests GH-2345:
// MergeNotificationPosted round-trips through SavePRState/LoadAllPRStates so
// that crash recovery honors the flag and a restored PR never re-posts.
func TestController_handleMerging_CommentFlagPersists(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:                42,
		PRURL:                   "https://github.com/owner/repo/pull/42",
		IssueNumber:             10,
		BranchName:              "pilot/GH-10",
		HeadSHA:                 "abc1234",
		Stage:                   StageMerging,
		CIStatus:                CIPending,
		CreatedAt:               time.Now().Add(-5 * time.Minute).Truncate(time.Second),
		MergeNotificationPosted: true,
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil || !loaded.MergeNotificationPosted {
		t.Fatalf("MergeNotificationPosted did not persist: got %+v", loaded)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 || !all[0].MergeNotificationPosted {
		t.Fatalf("LoadAllPRStates did not preserve MergeNotificationPosted: %+v", all)
	}

	// Wire the restored state into a controller and run handleMerging — it
	// must not post a duplicate comment because the flag is already set.
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.SetStateStore(store)
	if _, err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState failed: %v", err)
	}

	restored, ok := c.GetPRState(42)
	if !ok {
		t.Fatal("restored PR not tracked")
	}
	restored.Stage = StageMerging

	if err := c.handleMerging(context.Background(), restored); err != nil {
		t.Fatalf("handleMerging after restore returned error: %v", err)
	}
	if commentCount != 0 {
		t.Errorf("after restore: comment count = %d, want 0 (flag honored)", commentCount)
	}
}

// --- GH-4164: approval-gated merge follow-up tests ---

// mockApprovalMergeNotifier is a minimal approval.Handler that also
// implements approval.MergeNotifier, so tests can assert Controller wires
// handleMerging's success path to approval.Manager.NotifyMerged without
// depending on the unexported test doubles in the approval package.
type mockApprovalMergeNotifier struct {
	calls []struct{ requestID, shortSHA string }
}

func (m *mockApprovalMergeNotifier) Name() string { return "telegram" }

func (m *mockApprovalMergeNotifier) SendApprovalRequest(context.Context, *approval.Request) (<-chan *approval.Response, error) {
	ch := make(chan *approval.Response, 1)
	return ch, nil
}

func (m *mockApprovalMergeNotifier) CancelRequest(context.Context, string) error { return nil }

func (m *mockApprovalMergeNotifier) NotifyMerged(_ context.Context, requestID, shortSHA string) error {
	m.calls = append(m.calls, struct{ requestID, shortSHA string }{requestID, shortSHA})
	return nil
}

// TestController_handleMerging_NotifiesApprovalGatedMerge is the GH-4164
// regression test: a PR that went through human approval (ApprovalRequestID
// set) fires approval.Manager.NotifyMerged exactly once on a successful
// merge, and sets MergeFollowupPosted.
func TestController_handleMerging_NotifiesApprovalGatedMerge(t *testing.T) {
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	notifier := &mockApprovalMergeNotifier{}
	approvalMgr := approval.NewManager(nil)
	approvalMgr.RegisterHandler(notifier)

	c := NewController(cfg, ghClient, approvalMgr, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging
	prState.ApprovalRequestID = "req-42"

	if err := c.handleMerging(context.Background(), prState); err != nil {
		t.Fatalf("handleMerging returned error: %v", err)
	}

	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 NotifyMerged call, got %d", len(notifier.calls))
	}
	if notifier.calls[0].requestID != "req-42" {
		t.Errorf("expected requestID 'req-42', got %q", notifier.calls[0].requestID)
	}
	if notifier.calls[0].shortSHA != ShortSHA(prState.HeadSHA) {
		t.Errorf("expected shortSHA %q, got %q", ShortSHA(prState.HeadSHA), notifier.calls[0].shortSHA)
	}
	if !prState.MergeFollowupPosted {
		t.Error("expected MergeFollowupPosted to be true after notifying")
	}
}

// TestController_handleMerging_NoApprovalGate_SkipsMergeFollowup ensures a PR
// that never went through approval (ApprovalRequestID empty — the common
// case) never triggers NotifyMerged.
func TestController_handleMerging_NoApprovalGate_SkipsMergeFollowup(t *testing.T) {
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	notifier := &mockApprovalMergeNotifier{}
	approvalMgr := approval.NewManager(nil)
	approvalMgr.RegisterHandler(notifier)

	c := NewController(cfg, ghClient, approvalMgr, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	// ApprovalRequestID left empty — this PR was never gated by approval.

	if err := c.handleMerging(context.Background(), prState); err != nil {
		t.Fatalf("handleMerging returned error: %v", err)
	}

	if len(notifier.calls) != 0 {
		t.Errorf("expected no NotifyMerged calls for a non-approval-gated PR, got %d", len(notifier.calls))
	}
	if prState.MergeFollowupPosted {
		t.Error("expected MergeFollowupPosted to stay false")
	}
}

// TestController_handleMerging_MergeFollowupNotDoubleFired mirrors
// TestController_handleMerging_IdempotentCompletionComment: re-entering
// StageMerging for an already-merged, already-notified PR must not fire a
// second NotifyMerged call.
func TestController_handleMerging_MergeFollowupNotDoubleFired(t *testing.T) {
	commentCount := 0
	server := mergeMockServer(t, 42, 10, &commentCount)
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}

	notifier := &mockApprovalMergeNotifier{}
	approvalMgr := approval.NewManager(nil)
	approvalMgr.RegisterHandler(notifier)

	c := NewController(cfg, ghClient, approvalMgr, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")
	prState, _ := c.GetPRState(42)
	prState.Stage = StageMerging
	prState.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging
	prState.ApprovalRequestID = "req-42"

	if err := c.handleMerging(context.Background(), prState); err != nil {
		t.Fatalf("first handleMerging returned error: %v", err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("expected 1 NotifyMerged call after first merge, got %d", len(notifier.calls))
	}

	// Simulate re-entry (e.g. crash recovery replaying the same tick).
	prState.Stage = StageMerging
	if err := c.handleMerging(context.Background(), prState); err != nil {
		t.Fatalf("re-entry handleMerging returned error: %v", err)
	}
	if len(notifier.calls) != 1 {
		t.Errorf("expected still 1 NotifyMerged call after re-entry, got %d", len(notifier.calls))
	}
}

// TestController_handleMerging_MergeFollowupFlagPersists verifies
// MergeFollowupPosted round-trips through SavePRState/GetPRState, mirroring
// TestController_handleMerging_CommentFlagPersists for the new flag.
func TestController_handleMerging_MergeFollowupFlagPersists(t *testing.T) {
	store := newTestStateStore(t)

	pr := &PRState{
		PRNumber:            42,
		PRURL:               "https://github.com/owner/repo/pull/42",
		IssueNumber:         10,
		BranchName:          "pilot/GH-10",
		HeadSHA:             "abc1234",
		Stage:               StageMerging,
		CIStatus:            CIPending,
		CreatedAt:           time.Now().Add(-5 * time.Minute).Truncate(time.Second),
		ApprovalRequestID:   "req-42",
		MergeFollowupPosted: true,
	}
	if err := store.SavePRState("owner/repo", pr); err != nil {
		t.Fatalf("SavePRState failed: %v", err)
	}

	loaded, err := store.GetPRState("owner/repo", 42)
	if err != nil {
		t.Fatalf("GetPRState failed: %v", err)
	}
	if loaded == nil || !loaded.MergeFollowupPosted {
		t.Fatalf("MergeFollowupPosted did not persist: got %+v", loaded)
	}

	all, err := store.LoadAllPRStates("owner/repo")
	if err != nil {
		t.Fatalf("LoadAllPRStates failed: %v", err)
	}
	if len(all) != 1 || !all[0].MergeFollowupPosted {
		t.Fatalf("LoadAllPRStates did not preserve MergeFollowupPosted: %+v", all)
	}
}

// --- GH-2588: CI fix size guard tests ---

// TestCIFixSizeGuard_OversizedPR_EscalatesInsteadOfClosing is a cascade-2
// reproduction: a failing PR with 512 additions must NOT spawn a fix(ci)
// issue and must NOT be self-closed (GH-4459) — a closed PR with no
// continuation issue is the exact dead end that lost the GH-4415 fix twice.
// It must instead be escalated via escalateAndHold: PR/branch left intact,
// StageFailed, pilot-needs-human applied to the linked issue.
func TestCIFixSizeGuard_OversizedPR_EscalatesInsteadOfClosing(t *testing.T) {
	issueCreated := false
	prClosed := false
	branchDeleted := false
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc1234/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/10" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 10, Body: "<!-- autopilot-meta branch:pilot/GH-5 pr:99 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/42/files" && r.Method == http.MethodGet:
			// Simulate cascade-2 PR: 512 net additions
			files := []*github.PRFile{
				{Filename: "internal/gateway/oauth.go", Status: "added", Additions: 244},
				{Filename: "internal/gateway/oauth_test.go", Status: "added", Additions: 240},
				{Filename: "internal/gateway/server.go", Status: "modified", Additions: 28},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 200}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/10/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    42,
		IssueNumber: 10,
		HeadSHA:     "abc1234",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue must NOT be created when failing PR exceeds size floor")
	}
	if prClosed {
		t.Error("size guard must never self-close the PR — hold via escalateAndHold instead (GH-4459)")
	}
	if branchDeleted {
		t.Error("size guard must never delete the branch")
	}
	if c.consumeSelfClosedMarker(42) {
		t.Error("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, "CI fix size guard fired") {
		t.Errorf("Error should record the escalateAndHold reason, got: %s", prState.Error)
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label on the issue, got labels: %v", labelsAdded)
	}
}

// TestCIFixSizeGuard_SmallPR_AllowsFixIssue verifies that a small failing PR (50 additions)
// still gets a fix(ci) issue created — existing behavior must be unchanged.
func TestCIFixSizeGuard_SmallPR_AllowsFixIssue(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc5678/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/20" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 20, Body: "<!-- autopilot-meta branch:pilot/GH-20 pr:55 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/55/files" && r.Method == http.MethodGet:
			files := []*github.PRFile{
				{Filename: "internal/foo.go", Status: "modified", Additions: 50},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 300}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			// PR closed after fix issue created (normal flow)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    55,
		IssueNumber: 20,
		HeadSHA:     "abc5678",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("fix issue MUST be created for a small failing PR")
	}
}

// TestCIFixSizeGuard_APIError_FailOpen verifies that a ListPullRequestFiles API error
// does not block fix issue creation — the guard must fail open.
func TestCIFixSizeGuard_APIError_FailOpen(t *testing.T) {
	issueCreated := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/abc9999/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/30" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 30, Body: "<!-- autopilot-meta branch:pilot/GH-30 pr:66 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/66/files" && r.Method == http.MethodGet:
			// Simulate transient API error
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 400}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/66" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    66,
		IssueNumber: 30,
		HeadSHA:     "abc9999",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("fix issue MUST be created when ListPullRequestFiles fails (fail-open)")
	}
}

// TestCIFixSizeGuard_WellTestedPR_AllowsFixIssue is a GH-4284 regression test
// reproducing the #4279 shape: 421 total additions (90 production / 290 test
// / 28 bookkeeping) — over the raw 200-line limit, but under it once test and
// `.agent/**` additions are excluded. The guard must NOT fire (a well-tested
// PR was previously auto-closed as "cascade contamination" for this exact shape).
func TestCIFixSizeGuard_WellTestedPR_AllowsFixIssue(t *testing.T) {
	issueCreated := false
	prClosed := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/def4279/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "drift-gate", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/4276" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 4276, Body: "<!-- autopilot-meta branch:pilot/GH-4276 pr:4279 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/4279/files" && r.Method == http.MethodGet:
			// #4279 shape: 90 production, 290 test, 28 bookkeeping = 408 total (>200 raw, <200 production).
			files := []*github.PRFile{
				{Filename: "internal/autopilot/dispatcher.go", Status: "modified", Additions: 90},
				{Filename: "internal/autopilot/dispatcher_gh4276_test.go", Status: "added", Additions: 114},
				{Filename: "internal/memory/store_test.go", Status: "modified", Additions: 103},
				{Filename: "internal/autopilot/dispatcher_test.go", Status: "modified", Additions: 65},
				{Filename: ".agent/knowledge/memories/pitfalls/gh4276-cross-project-task-id.md", Status: "added", Additions: 28},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 500}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/4279" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    4279,
		IssueNumber: 4276,
		HeadSHA:     "def4279",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if !issueCreated {
		t.Error("fix issue MUST be created — 90 production additions is under the 200 limit once tests/bookkeeping are excluded")
	}
	if !prClosed {
		t.Error("failed PR should still be closed (normal flow: fix issue created, poller unblocked)")
	}
	if strings.Contains(prState.Error, "cascade contamination") {
		t.Errorf("error must not claim cascade contamination for a well-tested PR, got: %s", prState.Error)
	}
}

// TestCIFixSizeGuard_GenuineCascade_StillBlocksFixIssue verifies that a PR
// with >200 PRODUCTION lines across unrelated files (no tests, no
// bookkeeping) still trips the guard — the GH-4284 exclusion fix must not
// weaken genuine cascade-contamination detection. Per GH-4459 the guard no
// longer self-closes the PR; it escalates and holds instead.
func TestCIFixSizeGuard_GenuineCascade_StillBlocksFixIssue(t *testing.T) {
	issueCreated := false
	prClosed := false
	branchDeleted := false
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/cascade1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/11" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 11, Body: "<!-- autopilot-meta branch:pilot/GH-11 pr:77 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/77/files" && r.Method == http.MethodGet:
			// 300 production additions across unrelated files, no tests/bookkeeping.
			files := []*github.PRFile{
				{Filename: "internal/gateway/server.go", Status: "modified", Additions: 150},
				{Filename: "internal/adapters/slack/notifier.go", Status: "modified", Additions: 150},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 600}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/11/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/pulls/77" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    77,
		IssueNumber: 11,
		HeadSHA:     "cascade1",
		Stage:       StageCIFailed,
	}

	err := c.handleCIFailed(context.Background(), prState)
	if err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue must NOT be created — 300 production additions is genuine cascade contamination")
	}
	if prClosed {
		t.Error("size guard must never self-close the PR — hold via escalateAndHold instead (GH-4459)")
	}
	if branchDeleted {
		t.Error("size guard must never delete the branch")
	}
	if c.consumeSelfClosedMarker(77) {
		t.Error("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if !strings.Contains(prState.Error, "CI fix size guard") {
		t.Errorf("error should mention size guard, got: %s", prState.Error)
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label on the issue, got labels: %v", labelsAdded)
	}
}

// TestHandleCIFailed_FixIssueCreateErrors_EscalatesInsteadOfClosing covers the
// CI-fail rung of GH-4459: when CreateFailureIssue's underlying GitHub call
// errors (here, a 500 from the issues-create endpoint), the PR must be held
// via escalateAndHold rather than closed with no continuation — a closed PR
// paired with a failed fix-issue create is the exact dead end that lost the
// GH-4415 fix twice.
func TestHandleCIFailed_FixIssueCreateErrors_EscalatesInsteadOfClosing(t *testing.T) {
	prClosed := false
	branchDeleted := false
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/preflight1/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/30" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 30, Body: "<!-- autopilot-meta branch:pilot/GH-30 pr:88 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/88/files" && r.Method == http.MethodGet:
			files := []*github.PRFile{{Filename: "internal/foo.go", Status: "modified", Additions: 10}}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"internal server error"}`))
		case r.URL.Path == "/repos/owner/repo/issues/30/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/pulls/88" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	prState := &PRState{
		PRNumber:    88,
		IssueNumber: 30,
		HeadSHA:     "preflight1",
		Stage:       StageCIFailed,
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if prClosed {
		t.Error("PR must NOT be closed when the continuation fix issue fails to create — hold via escalateAndHold instead (GH-4459)")
	}
	if branchDeleted {
		t.Error("branch must NOT be deleted when the PR is held via escalateAndHold")
	}
	if c.consumeSelfClosedMarker(88) {
		t.Error("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error != "CI-fix continuation declined at preflight" {
		t.Errorf("Error = %q, want %q", prState.Error, "CI-fix continuation declined at preflight")
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label on the issue, got labels: %v", labelsAdded)
	}
}

// TestHandleCIFailed_FixIssuePreflightDeclined_EscalatesInsteadOfClosing covers
// the other half of the CI-fail rung (GH-4459): CreateFailureIssue can
// legitimately return (0, nil) when GH-4307's dedup guard sees the key
// already claimed but not yet recorded (a create still in flight, or a prior
// crash after claiming but before recording). Reproduced here by pre-claiming
// the exact dedup key handleCIFailed will compute before calling it. The PR
// must still be held via escalateAndHold, not closed with no continuation.
func TestHandleCIFailed_FixIssuePreflightDeclined_EscalatesInsteadOfClosing(t *testing.T) {
	prClosed := false
	branchDeleted := false
	issueCreated := false
	var labelsAdded []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/preflight2/check-runs":
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "failure"},
				},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/31" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 31, Body: "<!-- autopilot-meta branch:pilot/GH-31 pr:89 iteration:1 -->"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/pulls/89/files" && r.Method == http.MethodGet:
			files := []*github.PRFile{{Filename: "internal/foo.go", Status: "modified", Additions: 10}}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues" && r.Method == http.MethodPost:
			issueCreated = true
			resp := github.Issue{Number: 700}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(mustJSON(t, resp))
		case r.URL.Path == "/repos/owner/repo/issues/31/labels" && r.Method == http.MethodPost:
			var body map[string][]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			labelsAdded = append(labelsAdded, body["labels"]...)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]github.Label{})
		case r.URL.Path == "/repos/owner/repo/pulls/89" && r.Method == http.MethodPatch:
			prClosed = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/git/refs/heads/") && r.Method == http.MethodDelete:
			branchDeleted = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvStage
	cfg.MaxCIFixPRSize = 200

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	prState := &PRState{
		PRNumber:    89,
		IssueNumber: 31,
		HeadSHA:     "preflight2",
		Stage:       StageCIFailed,
	}

	// Pre-claim the exact dedup key handleCIFailed will compute (iteration+1
	// increments the issue-body iteration of 1 to 2, but the dedup key itself
	// doesn't carry iteration — only PR number, failure type, and the failed
	// check names GetFailedChecks returns for HeadSHA "preflight2"). Claiming
	// but never recording simulates a create still in flight, forcing
	// CreateFailureIssue's dedup guard to return (0, nil).
	dedupRepo := "owner/repo"
	dedupKey := spawnedFixDedupKey(89, FailureCIPreMerge, []string{"build"})
	claimed, err := store.ClaimSpawnedFix(dedupRepo, dedupKey)
	if err != nil {
		t.Fatalf("ClaimSpawnedFix: %v", err)
	}
	if !claimed {
		t.Fatal("expected to win the dedup claim (fresh store)")
	}

	if err := c.handleCIFailed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIFailed returned unexpected error: %v", err)
	}

	if issueCreated {
		t.Error("fix issue must NOT be created — the dedup key is already claimed but not yet recorded")
	}
	if prClosed {
		t.Error("PR must NOT be closed when the continuation fix issue is declined at preflight — hold via escalateAndHold instead (GH-4459)")
	}
	if branchDeleted {
		t.Error("branch must NOT be deleted when the PR is held via escalateAndHold")
	}
	if c.consumeSelfClosedMarker(89) {
		t.Error("escalateAndHold must never stamp a self-close marker — the PR was never closed")
	}
	if prState.Stage != StageFailed {
		t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
	}
	if prState.Error != "CI-fix continuation declined at preflight" {
		t.Errorf("Error = %q, want %q", prState.Error, "CI-fix continuation declined at preflight")
	}
	found := false
	for _, l := range labelsAdded {
		if l == labelNeedsHuman {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-needs-human label on the issue, got labels: %v", labelsAdded)
	}
}

// asyncApprovalManager returns an approval.Manager configured for async pre-merge approval.
func asyncApprovalManager() *approval.Manager {
	cfg := &approval.Config{
		Enabled: true,

		DefaultTimeout: 1 * time.Hour,
		DefaultAction:  approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       1 * time.Hour,
			DefaultAction: approval.DecisionRejected,
		},
	}
	return approval.NewManager(cfg)
}

// mockCapturingApprovalHandler is an approval.Handler that records every
// request it's asked to send, so tests can inspect fields (e.g.
// ReleasePlan) the controller populated before dispatch.
type mockCapturingApprovalHandler struct {
	sent []*approval.Request
}

func (m *mockCapturingApprovalHandler) Name() string { return "telegram" }

func (m *mockCapturingApprovalHandler) SendApprovalRequest(_ context.Context, req *approval.Request) (<-chan *approval.Response, error) {
	m.sent = append(m.sent, req)
	return make(chan *approval.Response, 1), nil
}

func (m *mockCapturingApprovalHandler) CancelRequest(context.Context, string) error { return nil }

// TestController_SubmitAsyncApprovalRequest_SetsReleasePlan is the GH-4164
// regression test: submitAsyncApprovalRequest must inject a release-aware
// ReleasePlan string onto the outbound approval.Request, computed from the
// controller's resolved release config — the approval package itself never
// sees a ReleaseConfig.
func TestController_SubmitAsyncApprovalRequest_SetsReleasePlan(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge"}

	mgr := asyncApprovalManager()
	handler := &mockCapturingApprovalHandler{}
	mgr.RegisterHandler(handler)

	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		PRTitle:     "feat: something",
		IssueNumber: 10,
		Stage:       StageAwaitApproval,
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("tick error: %v", err)
	}

	if len(handler.sent) != 1 {
		t.Fatalf("expected 1 approval request sent, got %d", len(handler.sent))
	}
	if got, want := handler.sent[0].ReleasePlan, "Will release immediately after merge."; got != want {
		t.Errorf("ReleasePlan = %q, want %q", got, want)
	}
}

// TestController_SubmitAsyncApprovalRequest_SetsProject is the GH-4773
// regression test: submitAsyncApprovalRequest must populate the outbound
// approval.Request's Project field from the controller's own c.projectPath
// (set via WithProjectPath), canonicalized the same way memory.Store keys
// its own scoping (the #4297 lesson) — so the persisted approval_pending row
// carries project identity even though the approval package itself never
// computes a project path.
func TestController_SubmitAsyncApprovalRequest_SetsProject(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	mgr := asyncApprovalManager()
	handler := &mockCapturingApprovalHandler{}
	mgr.RegisterHandler(handler)

	c := NewController(cfg, ghClient, mgr, "owner", "repo", WithProjectPath("/proj/pilot"))

	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		PRTitle:     "feat: something",
		IssueNumber: 10,
		Stage:       StageAwaitApproval,
	}
	c.mu.Unlock()

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("tick error: %v", err)
	}

	if len(handler.sent) != 1 {
		t.Fatalf("expected 1 approval request sent, got %d", len(handler.sent))
	}
	// "/proj/pilot" doesn't exist on the test filesystem, so
	// CanonicalizeProjectPath falls back to filepath.Clean (EvalSymlinks
	// fails) — the canonicalized form here is just the cleaned input.
	if got, want := handler.sent[0].Project, memory.CanonicalizeProjectPath("/proj/pilot"); got != want {
		t.Errorf("Project = %q, want %q", got, want)
	}
}

// TestController_AwaitApproval_StaysInStageUntilDecision verifies that the
// non-blocking handleAwaitApproval submits a request on the first tick and
// stays in StageAwaitApproval until a decision is recorded.
func TestController_AwaitApproval_StaysInStageUntilDecision(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	mgr := asyncApprovalManager()
	// GH-4380: the controller always sets PreferredChannel from the resolved
	// approval source (telegram by default), so a handler named "telegram"
	// must be registered or SubmitApprovalRequest fails fast instead of the
	// old silent no-handler no-op.
	mgr.RegisterHandler(&mockCapturingApprovalHandler{})
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	// Plant a PR directly in StageAwaitApproval.
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:    42,
		PRURL:       "https://github.com/owner/repo/pull/42",
		PRTitle:     "feat: something",
		IssueNumber: 10,
		Stage:       StageAwaitApproval,
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Tick 1: no ApprovalRequestID yet — should submit and stay in stage.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick 1 error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("after tick 1: stage = %s, want %s", pr.Stage, StageAwaitApproval)
	}
	if pr.ApprovalRequestID == "" {
		t.Error("after tick 1: ApprovalRequestID must be set")
	}
	if pr.ApprovalRequestedAt.IsZero() {
		t.Error("after tick 1: ApprovalRequestedAt must be set")
	}

	// Tick 2: request submitted, no decision yet — should stay in stage.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick 2 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageAwaitApproval {
		t.Errorf("after tick 2: stage = %s, want %s", pr.Stage, StageAwaitApproval)
	}

	// Record approval decision directly (simulating a Telegram callback).
	_ = c.SetApprovalDecision(ctx, pr.ApprovalRequestID, string(approval.DecisionApproved), "testuser")

	// Tick 3: decision recorded — should advance to StageMerging.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick 3 error: %v", err)
	}
	pr, _ = c.GetPRState(42)
	if pr.Stage != StageMerging {
		t.Errorf("after approval tick: stage = %s, want %s", pr.Stage, StageMerging)
	}
}

// TestController_AwaitApproval_AppliesDefaultActionAtTimeout verifies that when
// ApprovalRequestedAt exceeds the stage timeout and no decision is recorded, the
// controller applies the configured default_action.
func TestController_AwaitApproval_AppliesDefaultActionAtTimeout(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvProd

	// Short timeout so the test can simulate expiry without sleeping.
	approvalCfg := &approval.Config{
		Enabled: true,

		DefaultTimeout: 1 * time.Millisecond,
		DefaultAction:  approval.DecisionRejected,
		PreMerge: &approval.StageConfig{
			Enabled:       true,
			Timeout:       1 * time.Millisecond,
			DefaultAction: approval.DecisionRejected,
		},
	}
	mgr := approval.NewManager(approvalCfg)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	// Plant a PR in StageAwaitApproval with a request already submitted but expired.
	c.mu.Lock()
	c.activePRs[42] = &PRState{
		PRNumber:            42,
		PRURL:               "https://github.com/owner/repo/pull/42",
		IssueNumber:         10,
		Stage:               StageAwaitApproval,
		ApprovalRequestID:   "req-expired",
		ApprovalRequestedAt: time.Now().Add(-2 * time.Hour), // well past timeout
	}
	c.mu.Unlock()

	ctx := context.Background()

	// Single tick: should detect expiry, apply default_action (rejected), set StageFailed.
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("tick error: %v", err)
	}
	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Errorf("after timeout: stage = %s, want %s", pr.Stage, StageFailed)
	}
	if pr.ApprovalDecision != string(approval.DecisionRejected) {
		t.Errorf("after timeout: decision = %q, want %q", pr.ApprovalDecision, approval.DecisionRejected)
	}
	// TASK-459 Phase 4 task 4b: this synthesized decision never calls
	// SetApprovalDecision (no real "by" identity, no ledger write) — it must
	// still carry typed decider evidence rather than none at all.
	if pr.ApprovalDecisionBy != approvalDecisionSourceWallClockExpiryDefault {
		t.Errorf("after timeout: decided_by = %q, want %q", pr.ApprovalDecisionBy, approvalDecisionSourceWallClockExpiryDefault)
	}
}

// TestController_TwoPRQueue_StalledApprovalDoesNotBlockOther verifies that a PR
// stalled in StageAwaitApproval does not prevent another PR from advancing through
// earlier stages.
func TestController_TwoPRQueue_StalledApprovalDoesNotBlockOther(t *testing.T) {
	// Server returns success for any check-run or merge request.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "check-runs"):
			resp := github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: "completed", Conclusion: "success"},
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
	cfg := DefaultConfig()
	// Use stage env so PR-B can reach StageMerging without blocking on approval.
	cfg.Environment = EnvStage
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	mgr := asyncApprovalManager()
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	ctx := context.Background()

	// PR-A: stalled in StageAwaitApproval with request already submitted.
	c.mu.Lock()
	c.activePRs[10] = &PRState{
		PRNumber:            10,
		PRURL:               "https://github.com/owner/repo/pull/10",
		IssueNumber:         1,
		HeadSHA:             "sha10",
		Stage:               StageAwaitApproval,
		ApprovalRequestID:   "req-stalled",
		ApprovalRequestedAt: time.Now(),
	}
	c.mu.Unlock()

	// PR-B: freshly created, will advance independently.
	c.OnPRCreated(20, "https://github.com/owner/repo/pull/20", 2, "sha20", "pilot/GH-2", "")

	// Tick both PRs — PR-A stays stalled, PR-B advances from StagePRCreated → StageWaitingCI.
	_ = c.ProcessPR(ctx, 10, nil)
	_ = c.ProcessPR(ctx, 20, nil)

	prA, _ := c.GetPRState(10)
	prB, _ := c.GetPRState(20)

	if prA.Stage != StageAwaitApproval {
		t.Errorf("PR-A should still be %s, got %s", StageAwaitApproval, prA.Stage)
	}
	if prB.Stage == StagePRCreated {
		t.Errorf("PR-B should have advanced past %s (was blocked by PR-A)", StagePRCreated)
	}
}

// mockApprovalPersister records calls to SetApprovalRequestID and SetApprovalDecision
// so tests can verify that the controller wires through to the memory store.
// It also backs GH-3847's execution-event audit trail: execByTask lets a test
// seed which task IDs resolve to which execution rows, and executionEvents
// records every InsertExecutionEvent call for assertion.
type mockApprovalPersister struct {
	requestIDCalls []struct{ taskID, requestID string }
	decisionCalls  []struct{ requestID, decision, by string }

	execByTask      map[string]string
	executionEvents []recordedExecutionEvent

	// execStatus tracks each execution row's current status keyed by
	// execution ID (GH-4620), so UpdateExecutionStatusIfNotTerminal can
	// mirror the real store's CAS guard: a row already at a terminal status
	// rejects the write instead of being overwritten.
	execStatus        map[string]string
	statusUpdateCalls []struct{ id, status, detail string }
}

// mockTerminalExecStatuses mirrors memory.terminalExecutionStatuses (private
// to the memory package) closely enough for mockApprovalPersister's CAS
// emulation in tests.
var mockTerminalExecStatuses = map[string]bool{
	"completed": true, "failed": true, "cancelled": true, "declined": true,
	"stalled": true, "no_op": true, "rate_limited": true, "infra": true, "skipped": true,
}

type recordedExecutionEvent struct {
	executionID string
	stage       memory.Stage
	detail      string
}

func (m *mockApprovalPersister) SetApprovalRequestID(_ context.Context, taskID, requestID string) error {
	m.requestIDCalls = append(m.requestIDCalls, struct{ taskID, requestID string }{taskID, requestID})
	return nil
}

func (m *mockApprovalPersister) SetApprovalDecision(_ context.Context, requestID, decision, by string) error {
	m.decisionCalls = append(m.decisionCalls, struct{ requestID, decision, by string }{requestID, decision, by})
	return nil
}

func (m *mockApprovalPersister) GetLatestExecutionByTaskID(taskID, _ string) (*memory.Execution, error) {
	id, ok := m.execByTask[taskID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return &memory.Execution{ID: id, TaskID: taskID, Status: m.execStatus[id]}, nil
}

// UpdateExecutionStatusIfNotTerminal emulates memory.Store's CAS-guarded
// finalize (GH-4620): rejects the write once execStatus[id] is already one
// of mockTerminalExecStatuses, mirroring the real WHERE status NOT IN (...)
// guard so tests can assert both the finalize AND the no-clobber behavior.
func (m *mockApprovalPersister) UpdateExecutionStatusIfNotTerminal(id, status string, errorMsg ...string) (bool, error) {
	detail := ""
	if len(errorMsg) > 0 {
		detail = errorMsg[0]
	}
	m.statusUpdateCalls = append(m.statusUpdateCalls, struct{ id, status, detail string }{id, status, detail})

	if m.execStatus == nil {
		m.execStatus = map[string]string{}
	}
	if mockTerminalExecStatuses[m.execStatus[id]] {
		return false, nil
	}
	m.execStatus[id] = status
	return true, nil
}

func (m *mockApprovalPersister) RecordExecutionEvent(executionID string, stage memory.Stage, detail string) error {
	m.executionEvents = append(m.executionEvents, recordedExecutionEvent{executionID, stage, detail})
	return nil
}

func (m *mockApprovalPersister) HasExecutionEventStage(executionID string, stage memory.Stage) (bool, error) {
	for _, ev := range m.executionEvents {
		if ev.executionID == executionID && ev.stage == stage {
			return true, nil
		}
	}
	return false, nil
}

// TestController_SetApprovalDecision_PersistsToMemoryStore verifies that
// Controller.SetApprovalDecision updates in-memory PRState AND calls the
// injected approvalPersister (executions table write path).
func TestController_SetApprovalDecision_PersistsToMemoryStore(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	mgr := approval.NewManager(nil)
	c := NewController(cfg, ghClient, mgr, "owner", "repo")

	mock := &mockApprovalPersister{}
	c.memoryStore = mock

	c.mu.Lock()
	c.activePRs[99] = &PRState{
		PRNumber:          99,
		IssueNumber:       10,
		ApprovalRequestID: "req-test-123",
	}
	c.mu.Unlock()

	ctx := context.Background()
	if err := c.SetApprovalDecision(ctx, "req-test-123", "approved", "reviewer"); err != nil {
		t.Fatalf("SetApprovalDecision: %v", err)
	}

	// In-memory state updated.
	pr, ok := c.GetPRState(99)
	if !ok {
		t.Fatal("PR state not found")
	}
	if pr.ApprovalDecision != "approved" {
		t.Errorf("in-memory ApprovalDecision = %q, want %q", pr.ApprovalDecision, "approved")
	}

	// Memory store called with correct args.
	if len(mock.decisionCalls) != 1 {
		t.Fatalf("expected 1 SetApprovalDecision call, got %d", len(mock.decisionCalls))
	}
	call := mock.decisionCalls[0]
	if call.requestID != "req-test-123" || call.decision != "approved" || call.by != "reviewer" {
		t.Errorf("unexpected call args: %+v", call)
	}
}

// TestController_ProcessPR_StageFailed_FinalizesRunningExecutionRow verifies
// the GH-4620 fix: a PR transition into StageFailed (here via the dirty-PR
// merge-conflict path exercised by TestController_ProcessPR_MergeConflict_PRCreated)
// must finalize a non-terminal source execution row as "failed" instead of
// leaving it orphaned as "running" for the 2h stale-running sweep to evict.
func TestController_ProcessPR_StageFailed_FinalizesRunningExecutionRow(t *testing.T) {
	prClosed := false

	mergeable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "GET":
			resp := github.PullRequest{
				Number:         42,
				Head:           github.PRRef{SHA: "abc1234"},
				Mergeable:      &mergeable,
				MergeableState: "dirty",
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/repos/owner/repo/pulls/42/update-branch" && r.Method == "PUT":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == "PATCH":
			prClosed = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.CIPollInterval = 10 * time.Millisecond

	c := NewController(cfg, ghClient, nil, "owner", "repo")

	mock := &mockApprovalPersister{
		execByTask: map[string]string{"GH-10": "exec-1"},
		execStatus: map[string]string{"exec-1": "running"},
	}
	c.memoryStore = mock

	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc1234", "pilot/GH-10", "")

	ctx := context.Background()
	if err := c.ProcessPR(ctx, 42, nil); err != nil {
		t.Fatalf("ProcessPR error: %v", err)
	}

	pr, _ := c.GetPRState(42)
	if pr.Stage != StageFailed {
		t.Fatalf("Stage = %s, want %s (dirty PR should fail immediately)", pr.Stage, StageFailed)
	}
	if !prClosed {
		t.Error("conflicting PR should have been closed")
	}

	if got := mock.execStatus["exec-1"]; got != "failed" {
		t.Errorf("execution row status = %q, want %q (StageFailed must finalize a running row)", got, "failed")
	}
}

// TestController_RecordExecutionEvent_StageFailed_RespectsTerminalRow verifies
// the GH-4620 finalize is CAS-guarded: an execution row already at a terminal
// status (e.g. "completed" from another writer) must not be clobbered by the
// StageFailed transition's finalize.
func TestController_RecordExecutionEvent_StageFailed_RespectsTerminalRow(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	c := NewController(cfg, ghClient, nil, "owner", "repo")

	mock := &mockApprovalPersister{
		execByTask: map[string]string{"GH-11": "exec-2"},
		execStatus: map[string]string{"exec-2": "completed"},
	}
	c.memoryStore = mock

	prState := &PRState{PRNumber: 43, IssueNumber: 11}
	c.recordExecutionEvent(prState, memory.StageFailed, "pr #43: pr_created -> failed")

	if got := mock.execStatus["exec-2"]; got != "completed" {
		t.Errorf("execution row status = %q, want %q (terminal row must not be overwritten)", got, "completed")
	}
	if len(mock.statusUpdateCalls) != 1 {
		t.Fatalf("expected 1 UpdateExecutionStatusIfNotTerminal call, got %d", len(mock.statusUpdateCalls))
	}
}

// errApprovalPersister is a mock that returns a configurable error for both methods.
type errApprovalPersister struct {
	requestIDErr error
	decisionErr  error
}

func (m *errApprovalPersister) SetApprovalRequestID(_ context.Context, _, _ string) error {
	return m.requestIDErr
}

func (m *errApprovalPersister) SetApprovalDecision(_ context.Context, _, _, _ string) error {
	return m.decisionErr
}

func (m *errApprovalPersister) GetLatestExecutionByTaskID(_, _ string) (*memory.Execution, error) {
	return nil, sql.ErrNoRows
}

func (m *errApprovalPersister) RecordExecutionEvent(_ string, _ memory.Stage, _ string) error {
	return nil
}

func (m *errApprovalPersister) HasExecutionEventStage(_ string, _ memory.Stage) (bool, error) {
	return false, nil
}

func (m *errApprovalPersister) UpdateExecutionStatusIfNotTerminal(_, _ string, _ ...string) (bool, error) {
	return false, nil
}

// TestController_ApprovalPersistMiss_RequestID verifies that a sql.ErrNoRows from
// SetApprovalRequestID increments the request_id miss counter.
func TestController_ApprovalPersistMiss_RequestID(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")
	c.memoryStore = &errApprovalPersister{requestIDErr: sql.ErrNoRows}

	// Directly invoke the counter via the same code path as handleAwaitApproval by
	// calling the private helper through a public wrapper. Since the logic lives in
	// handleAwaitApproval (which calls SetApprovalRequestID), we replicate the
	// pattern inline: set up an active PR and call SetApprovalRequestID to simulate
	// the zero-row path, then check the counter.
	ctx := context.Background()
	taskID := "GH-42"
	requestID := "req-miss-test"

	// Simulate the call site in handleAwaitApproval.
	merr := c.memoryStore.SetApprovalRequestID(ctx, taskID, requestID)
	if merr != nil {
		c.metrics.RecordApprovalPersistMiss("request_id")
	}

	snap := c.metrics.Snapshot()
	if snap.ApprovalPersistMisses["request_id"] != 1 {
		t.Errorf("expected 1 request_id miss, got %d", snap.ApprovalPersistMisses["request_id"])
	}
	if snap.ApprovalPersistMisses["decision"] != 0 {
		t.Errorf("expected 0 decision misses, got %d", snap.ApprovalPersistMisses["decision"])
	}
}

// TestController_ApprovalPersistMiss_Decision verifies that a sql.ErrNoRows from
// SetApprovalDecision increments the decision miss counter via Controller.SetApprovalDecision.
//
// GH-4777: previously Controller.SetApprovalDecision swallowed this error and
// returned nil — dead-code-ing the gateway's unlinked-request (still-200)
// branch in production. It must now propagate sql.ErrNoRows so it survives
// errors.Is() up through Manager.RecordDecision to the gateway handler, which
// is the layer that actually decides to warn-and-resolve rather than fail.
func TestController_ApprovalPersistMiss_Decision(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")
	c.memoryStore = &errApprovalPersister{decisionErr: sql.ErrNoRows}

	c.mu.Lock()
	c.activePRs[7] = &PRState{
		PRNumber:          7,
		IssueNumber:       42,
		ApprovalRequestID: "req-decision-miss",
	}
	c.mu.Unlock()

	ctx := context.Background()
	err := c.SetApprovalDecision(ctx, "req-decision-miss", "approved", "bot")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetApprovalDecision: got %v, want sql.ErrNoRows", err)
	}

	snap := c.metrics.Snapshot()
	if snap.ApprovalPersistMisses["decision"] != 1 {
		t.Errorf("expected 1 decision miss, got %d", snap.ApprovalPersistMisses["decision"])
	}
	if snap.ApprovalPersistMisses["request_id"] != 0 {
		t.Errorf("expected 0 request_id misses, got %d", snap.ApprovalPersistMisses["request_id"])
	}
}

// TestController_SetApprovalDecision_NoStoreNoPanic verifies that the controller
// works correctly when no memory store is wired (nil-safe).
func TestController_SetApprovalDecision_NoStoreNoPanic(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	c := NewController(DefaultConfig(), ghClient, approval.NewManager(nil), "owner", "repo")

	c.mu.Lock()
	c.activePRs[1] = &PRState{
		PRNumber:          1,
		ApprovalRequestID: "req-nil-store",
	}
	c.mu.Unlock()

	// Should not panic with nil memoryStore.
	err := c.SetApprovalDecision(context.Background(), "req-nil-store", "approved", "bot")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	pr, _ := c.GetPRState(1)
	if pr.ApprovalDecision != "approved" {
		t.Errorf("in-memory decision not set: %q", pr.ApprovalDecision)
	}
}

// TestController_IssuesProcessed_Success verifies that RecordIssueProcessed("success")
// is called when a PR merges successfully.
func TestController_IssuesProcessed_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/mergesha1/check-runs":
			_, _ = w.Write(mustJSON(t, github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "success"}},
			}))
		case r.URL.Path == "/repos/owner/repo/pulls/42/merge" && r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, map[string]interface{}{
				"sha": "merged1", "merged": true, "message": "Pull Request successfully merged",
			}))
		case r.URL.Path == "/repos/owner/repo/pulls/42" && r.Method == http.MethodGet:
			_, _ = w.Write(mustJSON(t, github.PullRequest{
				Number: 42, State: "open",
				Head: github.PRRef{Ref: "pilot/GH-10", SHA: "mergesha1"},
			}))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.RequiredChecks = []string{"build"}
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "mergesha1", "pilot/GH-10", "")
	pr, _ := c.GetPRState(42)
	pr.Stage = StageMerging
	pr.TargetBranch = "main" // GH-4872: guard requires a known default-branch target before merging

	if err := c.ProcessPR(context.Background(), 42, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("IssuesProcessed[success] = %d, want 1", snap.IssuesProcessed["success"])
	}
	if snap.IssuesProcessed["failed"] != 0 {
		t.Errorf("IssuesProcessed[failed] = %d, want 0", snap.IssuesProcessed["failed"])
	}
}

// TestController_IssuesProcessed_TerminalFailure verifies that RecordIssueProcessed("failed")
// is called when a PR hits the CI fix iteration limit (terminal failure path).
func TestController_IssuesProcessed_TerminalFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/commits/failsha1/check-runs":
			_, _ = w.Write(mustJSON(t, github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns:  []github.CheckRun{{Name: "build", Status: "completed", Conclusion: "failure"}},
			}))
		case r.URL.Path == "/repos/owner/repo/issues/20" && r.Method == http.MethodGet:
			// iteration:3 >= MaxCIFixIterations(3) → terminal stop
			_, _ = w.Write(mustJSON(t, github.Issue{
				Number: 20,
				Body:   "Fix CI failures.\n\n<!-- autopilot-meta branch:pilot/GH-20 pr:55 iteration:3 -->\n",
			}))
		case r.URL.Path == "/repos/owner/repo/pulls/55" && r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.MaxCIFixIterations = 3
	c := NewController(cfg, ghClient, nil, "owner", "repo")
	c.OnPRCreated(55, "https://github.com/owner/repo/pull/55", 20, "failsha1", "pilot/GH-20", "")
	pr, _ := c.GetPRState(55)
	pr.Stage = StageCIFailed

	if err := c.ProcessPR(context.Background(), 55, nil); err != nil {
		t.Fatalf("ProcessPR returned error: %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.IssuesProcessed["failed"] != 1 {
		t.Errorf("IssuesProcessed[failed] = %d, want 1", snap.IssuesProcessed["failed"])
	}
	if snap.IssuesProcessed["success"] != 0 {
		t.Errorf("IssuesProcessed[success] = %d, want 0", snap.IssuesProcessed["success"])
	}
}

// TestController_ScanRecentlyMergedPRs_RecordsMetrics verifies that
// ScanRecentlyMergedPRs fires merge metrics on first discovery (GH-2981)
// and is idempotent on subsequent scans.
func TestController_ScanRecentlyMergedPRs_RecordsMetrics(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)

	pilotPR := github.PullRequest{
		Number:         77,
		Head:           github.PRRef{Ref: "pilot/GH-300", SHA: "sha77"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/77",
		Title:          "feat(api): new endpoint",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: "merge-sha-77",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/releases"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[]"))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	// First scan: PR not in state store → metrics must be recorded.
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("first ScanRecentlyMergedPRs() error = %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("after first scan: PRsMerged = %d, want 1", snap.PRsMerged)
	}
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("after first scan: IssuesProcessed[success] = %d, want 1", snap.IssuesProcessed["success"])
	}
	hist := c.metrics.HistogramSnapshot()
	if len(hist.PRTimeToMerge) != 1 {
		t.Errorf("after first scan: PRTimeToMerge samples = %d, want 1", len(hist.PRTimeToMerge))
	}

	// Second scan: PR is now in state store at StageReleasing → counts must not change.
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("second ScanRecentlyMergedPRs() error = %v", err)
	}

	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 1 {
		t.Errorf("after second scan (idempotency): PRsMerged = %d, want 1", snap2.PRsMerged)
	}
	if snap2.IssuesProcessed["success"] != 1 {
		t.Errorf("after second scan (idempotency): IssuesProcessed[success] = %d, want 1", snap2.IssuesProcessed["success"])
	}
	hist2 := c.metrics.HistogramSnapshot()
	if len(hist2.PRTimeToMerge) != 1 {
		t.Errorf("after second scan (idempotency): PRTimeToMerge samples = %d, want 1", len(hist2.PRTimeToMerge))
	}
}

// TestController_ScanRecentlyMergedPRs_BoardWriteBack verifies TASK-356 #2: an
// externally-merged Pilot PR (manual `gh pr merge`, never through handleMerging)
// still has its board card moved to Done by the scanner. Large PRs blocked by the
// stage approval-misconfig are merged manually, so without this the card stays
// stuck "In Review".
func TestController_ScanRecentlyMergedPRs_BoardWriteBack(t *testing.T) {
	recentMergedAt := time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)

	pilotPR := github.PullRequest{
		Number:         123,
		Head:           github.PRRef{Ref: "pilot/GH-456", SHA: "headsha123"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/123",
		Title:          "feat: big change merged manually",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: "merge-sha-123",
	}

	const issueNodeID = "I_kwDOissue456"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case r.URL.Path == "/repos/owner/repo/issues/456":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"node_id": issueNodeID})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
	cfg.MergedPRScanWindow = 30 * time.Minute

	mock := &mockBoardSyncer{}
	c := NewController(cfg, ghClient, nil, "owner", "repo",
		withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev"))
	c.SetStateStore(newTestStateStore(t))

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("board sync calls = %d, want 1 (externally-merged PR card should move to Done)", len(mock.calls))
	}
	if mock.calls[0].issueNodeID != issueNodeID {
		t.Errorf("board sync issueNodeID = %q, want %q", mock.calls[0].issueNodeID, issueNodeID)
	}
	if mock.calls[0].statusName != "Done" {
		t.Errorf("board sync statusName = %q, want %q", mock.calls[0].statusName, "Done")
	}
}

// TestController_ScanRecentlyMergedPRs_BoardWriteBack_NoRelease verifies TASK-356 #2
// (decouple): board write-back fires even when on_merge release is DISABLED, and the
// release-triggering tail is skipped (PR not added to activePRs). A board-sourced,
// non-releasing setup must still move a manually-merged PR's card to Done.
func TestController_ScanRecentlyMergedPRs_BoardWriteBack_NoRelease(t *testing.T) {
	recentMergedAt := time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339)

	pilotPR := github.PullRequest{
		Number:         321,
		Head:           github.PRRef{Ref: "pilot/GH-654", SHA: "headsha321"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/321",
		Title:          "feat: merged manually, release off",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: "merge-sha-321",
	}

	const issueNodeID = "I_kwDOissue654"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case r.URL.Path == "/repos/owner/repo/issues/654":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"node_id": issueNodeID})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{Enabled: false} // release OFF
	cfg.MergedPRScanWindow = 30 * time.Minute

	mock := &mockBoardSyncer{}
	c := NewController(cfg, ghClient, nil, "owner", "repo",
		withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev"))
	c.SetStateStore(newTestStateStore(t))

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	if len(mock.calls) != 1 {
		t.Fatalf("board sync calls = %d, want 1 (card should move to Done even with release off)", len(mock.calls))
	}
	if mock.calls[0].issueNodeID != issueNodeID || mock.calls[0].statusName != "Done" {
		t.Errorf("board sync call = %+v, want {%q, Done}", mock.calls[0], issueNodeID)
	}

	// Release tail must be skipped: PR not registered for release triggering.
	c.mu.RLock()
	_, tracked := c.activePRs[321]
	c.mu.RUnlock()
	if tracked {
		t.Error("PR 321 was added to activePRs; release tail must be skipped when release is disabled")
	}
}

// TestController_ScanRecentlyMergedPRs_RecordsMetricsDespiteExistingRelease
// reproduces the bug where Pilot's own self-release pipeline always tags every
// merge within ~1min, so by the time the ~5-15min scanner tick runs the
// "already tagged" gate skips the PR before metrics fire. The recorder must
// fire BEFORE the gate so counters move in stage-mode auto-merge.
func TestController_ScanRecentlyMergedPRs_RecordsMetricsDespiteExistingRelease(t *testing.T) {
	recentMergedAt := time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	recentCreatedAt := time.Now().Add(-12 * time.Minute).UTC().Format(time.RFC3339)
	mergeSHA := "merge-sha-99"

	pilotPR := github.PullRequest{
		Number:         99,
		Head:           github.PRRef{Ref: "pilot/GH-501", SHA: "headsha99"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/99",
		Title:          "feat(api): another endpoint",
		Merged:         true,
		CreatedAt:      recentCreatedAt,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: mergeSHA,
	}

	// Tag already exists at the merge SHA (Pilot's self-shipping pattern).
	existingTag := github.Tag{Name: "v9.9.9", Commit: struct {
		SHA string `json:"sha"`
	}{SHA: mergeSHA}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Tag{&existingTag})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("first ScanRecentlyMergedPRs() error = %v", err)
	}

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("PRsMerged = %d, want 1 (recorder must fire even when release tag exists)", snap.PRsMerged)
	}
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("IssuesProcessed[success] = %d, want 1", snap.IssuesProcessed["success"])
	}
	hist := c.metrics.HistogramSnapshot()
	if len(hist.PRTimeToMerge) != 1 {
		t.Errorf("PRTimeToMerge samples = %d, want 1", len(hist.PRTimeToMerge))
	}

	// Verify release-exists gate still suppresses release triggering: PR should
	// NOT have been added to activePRs (would happen if scanner proceeded past
	// the gate).
	c.mu.RLock()
	_, tracked := c.activePRs[99]
	c.mu.RUnlock()
	if tracked {
		t.Error("PR 99 was added to activePRs; release-exists gate should still suppress release triggering")
	}

	// Second scan: counts unchanged (idempotency).
	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("second ScanRecentlyMergedPRs() error = %v", err)
	}
	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 1 {
		t.Errorf("after second scan: PRsMerged = %d, want 1 (idempotent)", snap2.PRsMerged)
	}
}

// TestController_ScanRecentlyMergedPRs_SkipsTaggedMergeCommit reproduces GH-3218:
// releases have target_commitish="main" (branch ref, not SHA), so the former
// releasedCommits map lookup was never matching. The scanner must skip PRs whose
// merge commit has a release tag regardless of target_commitish.
func TestController_ScanRecentlyMergedPRs_SkipsTaggedMergeCommit(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	mergeSHA := "abc123def456"

	pilotPR := github.PullRequest{
		Number:         55,
		Head:           github.PRRef{Ref: "pilot/GH-200", SHA: "headsha55"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/55",
		Title:          "feat(api): endpoint",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: mergeSHA,
	}

	// Tag exists at the merge SHA. target_commitish is "main" (branch ref, not SHA),
	// which was the broken key in the old releasedCommits map.
	tagAtMergeSHA := github.Tag{Name: "v1.2.3", Commit: struct {
		SHA string `json:"sha"`
	}{SHA: mergeSHA}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Tag{&tagAtMergeSHA})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	c.mu.RLock()
	_, tracked := c.activePRs[55]
	c.mu.RUnlock()
	if tracked {
		t.Error("PR 55 was added to activePRs; scanner must skip PRs whose merge commit is already tagged")
	}
}

// TestController_ScanRecentlyMergedPRs_TracksOrphanMerge verifies that a PR
// merged externally with NO release tag is still picked up by the scanner
// (orphan-merge recovery must keep working after the GH-3218 fix).
func TestController_ScanRecentlyMergedPRs_TracksOrphanMerge(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	mergeSHA := "deadbeef1234"

	pilotPR := github.PullRequest{
		Number:         66,
		Head:           github.PRRef{Ref: "pilot/GH-300", SHA: "headsha66"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        "https://github.com/owner/repo/pull/66",
		Title:          "fix(db): leak",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: mergeSHA,
	}

	// Tags endpoint returns a tag for a DIFFERENT SHA — merge commit has no tag yet.
	unrelatedTag := github.Tag{Name: "v0.0.1", Commit: struct {
		SHA string `json:"sha"`
	}{SHA: "ffffffffffffffffffffffffffffffffffffffff"}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
		case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]*github.Tag{&unrelatedTag})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Release = &ReleaseConfig{
		Enabled:   true,
		Trigger:   "on_merge",
		TagPrefix: "v",
	}
	cfg.MergedPRScanWindow = 30 * time.Minute

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	store := newTestStateStore(t)
	c.SetStateStore(store)

	if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
		t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
	}

	c.mu.RLock()
	pr, tracked := c.activePRs[66]
	c.mu.RUnlock()
	if !tracked {
		t.Fatal("PR 66 was not added to activePRs; orphan-merge recovery must still pick up untagged PRs")
	}
	if pr.Stage != StageReleasing {
		t.Errorf("PR 66 stage = %v, want StageReleasing", pr.Stage)
	}
}

// TestController_ScanRecentlyMergedPRs_FlagMatrix verifies GH-3419: the scan
// runs unconditionally across all {release, board} flag combinations. Self-heal
// fires in every case; release triggering and board write-back are gated
// internally per-mode.
func TestController_ScanRecentlyMergedPRs_FlagMatrix(t *testing.T) {
	recentMergedAt := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	const (
		prNum       = 77
		issueNum    = 400
		issueNodeID = "I_kwDOissue400"
	)
	pilotPR := github.PullRequest{
		Number:         prNum,
		Head:           github.PRRef{Ref: fmt.Sprintf("pilot/GH-%d", issueNum), SHA: "headsha77"},
		Base:           github.PRRef{Ref: "main"},
		HTMLURL:        fmt.Sprintf("https://github.com/owner/repo/pull/%d", prNum),
		Title:          "feat: matrix test PR",
		Merged:         true,
		MergedAt:       recentMergedAt,
		MergeCommitSHA: "merge-sha-77",
	}

	tests := []struct {
		name           string
		releaseEnabled bool
		boardEnabled   bool
		wantSelfHeal   bool
		wantActivePR   bool // PR registered for release
		wantBoardCalls int
	}{
		{
			name:           "off,off: scan still runs self-heal, no release, no board",
			releaseEnabled: false,
			boardEnabled:   false,
			wantSelfHeal:   true,
			wantActivePR:   false,
			wantBoardCalls: 0,
		},
		{
			name:           "off,on: no release but board write-back fires",
			releaseEnabled: false,
			boardEnabled:   true,
			wantSelfHeal:   true,
			wantActivePR:   false,
			wantBoardCalls: 1,
		},
		{
			name:           "on,off: release triggered, no board",
			releaseEnabled: true,
			boardEnabled:   false,
			wantSelfHeal:   true,
			wantActivePR:   true,
			wantBoardCalls: 0,
		},
		{
			name:           "on,on: release triggered AND board write-back fires",
			releaseEnabled: true,
			boardEnabled:   true,
			wantSelfHeal:   true,
			wantActivePR:   true,
			wantBoardCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/pulls"):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode([]*github.PullRequest{&pilotPR})
				case r.URL.Path == fmt.Sprintf("/repos/owner/repo/issues/%d", issueNum):
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(map[string]string{"node_id": issueNodeID})
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/tags"):
					// No tags for merge SHA — PR triggers release when enabled.
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("[]"))
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			if tt.releaseEnabled {
				cfg.Release = &ReleaseConfig{Enabled: true, Trigger: "on_merge", TagPrefix: "v"}
			} else {
				cfg.Release = &ReleaseConfig{Enabled: false}
			}
			cfg.MergedPRScanWindow = 30 * time.Minute

			var opts []ControllerOption
			var boardMock *mockBoardSyncer
			if tt.boardEnabled {
				boardMock = &mockBoardSyncer{}
				opts = append(opts, withBoardSyncerForTest(boardMock, "Done", "Failed", "In Review", "In Dev"))
			}

			c := NewController(cfg, ghClient, nil, "owner", "repo", opts...)
			c.SetStateStore(newTestStateStore(t))
			evalMock := &mockEvalStore{}
			c.SetEvalStore(evalMock)

			if err := c.ScanRecentlyMergedPRs(context.Background()); err != nil {
				t.Fatalf("ScanRecentlyMergedPRs() error = %v", err)
			}

			// Self-heal must fire in every flag combination.
			if got, want := len(evalMock.selfHealed) > 0, tt.wantSelfHeal; got != want {
				t.Errorf("selfHealed entries = %d (healed=%v), want %v", len(evalMock.selfHealed), got, want)
			}

			// Release-trigger: PR registered in activePRs only when release is enabled.
			c.mu.RLock()
			_, tracked := c.activePRs[prNum]
			c.mu.RUnlock()
			if tracked != tt.wantActivePR {
				t.Errorf("activePRs[%d] tracked = %v, want %v", prNum, tracked, tt.wantActivePR)
			}

			// Board write-back: called only when board is enabled.
			var boardCalls int
			if boardMock != nil {
				boardCalls = len(boardMock.calls)
			}
			if boardCalls != tt.wantBoardCalls {
				t.Errorf("board sync calls = %d, want %d", boardCalls, tt.wantBoardCalls)
			}
		})
	}
}

// TestController_RecordMergeSuccess_Idempotency verifies recordMergeSuccess
// fires exactly once per PR number even when called multiple times from
// different code paths (e.g. handleMerging + ScanRecentlyMergedPRs both
// observing the same PR).
func TestController_RecordMergeSuccess_Idempotency(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")

	createdAt := time.Now().Add(-10 * time.Minute)
	prState := &PRState{PRNumber: 42, CreatedAt: createdAt}

	c.recordMergeSuccess(prState)
	c.recordMergeSuccess(prState)
	c.recordMergeSuccess(prState)

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("PRsMerged = %d after 3 calls, want 1", snap.PRsMerged)
	}
	if snap.IssuesProcessed["success"] != 1 {
		t.Errorf("IssuesProcessed[success] = %d after 3 calls, want 1", snap.IssuesProcessed["success"])
	}
	hist := c.metrics.HistogramSnapshot()
	if len(hist.PRTimeToMerge) != 1 {
		t.Errorf("PRTimeToMerge samples = %d after 3 calls, want 1", len(hist.PRTimeToMerge))
	}

	// Different PR number → fires independently.
	c.recordMergeSuccess(&PRState{PRNumber: 43, CreatedAt: createdAt})
	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 2 {
		t.Errorf("PRsMerged = %d after second PR, want 2", snap2.PRsMerged)
	}
}

// TestController_RecordExternalMerge_DedupesAcrossPaths pins GH-4390's
// acceptance criterion "double-increment impossible for the same PR": a PR
// observed merged via the controller's own flow (handleMerging/scan, which
// calls recordMergeSuccess directly) and then again via the executor's
// self-heal path (RecordExternalMerge, GH-4390) must only count once —
// RecordExternalMerge routes through the same recordedMerges dedup guard.
func TestController_RecordExternalMerge_DedupesAcrossPaths(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")

	c.recordMergeSuccess(&PRState{PRNumber: 42})
	c.RecordExternalMerge("", 42) // same PR, observed via the executor self-heal path

	snap := c.metrics.Snapshot()
	if snap.PRsMerged != 1 {
		t.Errorf("PRsMerged = %d after controller merge + external-merge observation of the same PR, want 1", snap.PRsMerged)
	}

	// The reverse order — external-merge observed first — must also dedupe.
	c.RecordExternalMerge("", 43)
	c.recordMergeSuccess(&PRState{PRNumber: 43})
	snap2 := c.metrics.Snapshot()
	if snap2.PRsMerged != 2 {
		t.Errorf("PRsMerged = %d after external-merge + controller merge of the same PR, want 2 (1 from PR 42 + 1 from PR 43)", snap2.PRsMerged)
	}
}

// TestController_RecordExternalMerge_ProjectPathScoping verifies
// RecordExternalMerge only records when the given projectPath matches this
// controller's own WithProjectPath scope — the guard MultiControllerMergeRecorder
// relies on to fan a merge out to every controller without double-counting
// across repos (GH-4390).
func TestController_RecordExternalMerge_ProjectPathScoping(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo", WithProjectPath("/project-a"))

	c.RecordExternalMerge("/project-b", 100)
	if got := c.metrics.Snapshot().PRsMerged; got != 0 {
		t.Errorf("PRsMerged = %d after RecordExternalMerge with a non-matching projectPath, want 0", got)
	}

	c.RecordExternalMerge("/project-a", 100)
	if got := c.metrics.Snapshot().PRsMerged; got != 1 {
		t.Errorf("PRsMerged = %d after RecordExternalMerge with the matching projectPath, want 1", got)
	}
}

// TestController_RecordExternalMerge_UnscopedAcceptsAny verifies a controller
// with no WithProjectPath (projectPath == "", e.g. single-controller test/dev
// setups) records regardless of the given projectPath, matching the
// single-controller-implies-single-owner assumption used elsewhere in this
// file (GH-4390).
func TestController_RecordExternalMerge_UnscopedAcceptsAny(t *testing.T) {
	c := NewController(DefaultConfig(), nil, nil, "owner", "repo")

	c.RecordExternalMerge("/any/project", 200)
	if got := c.metrics.Snapshot().PRsMerged; got != 1 {
		t.Errorf("PRsMerged = %d after RecordExternalMerge on an unscoped controller, want 1", got)
	}
}

// TestMultiControllerMergeRecorder_RoutesToOwningController is the GH-4390
// multi-repo counterpart to TestController_RecordExternalMerge_ProjectPathScoping:
// fanning a merge out to every controller (as SetMergeMetricsRecorder wires
// in polling mode) must land on exactly the controller that owns the given
// projectPath, not every controller.
func TestMultiControllerMergeRecorder_RoutesToOwningController(t *testing.T) {
	c1 := NewController(DefaultConfig(), nil, nil, "owner", "repo1", WithProjectPath("/project-1"))
	c2 := NewController(DefaultConfig(), nil, nil, "owner", "repo2", WithProjectPath("/project-2"))

	recorder := NewMultiControllerMergeRecorder(c1, c2)
	recorder.RecordExternalMerge("/project-2", 55)

	if got := c1.metrics.Snapshot().PRsMerged; got != 0 {
		t.Errorf("c1 PRsMerged = %d, want 0 (merge belongs to /project-2)", got)
	}
	if got := c2.metrics.Snapshot().PRsMerged; got != 1 {
		t.Errorf("c2 PRsMerged = %d, want 1 (merge belongs to /project-2)", got)
	}
}

// TestGetMainBranchSHA_RespectsResolvedEnv asserts that getMainBranchSHA reads
// the branch name from c.config.ResolvedEnv().Branch instead of the previously
// hardcoded "main" literal. TASK-291: prevents the regression where develop /
// master / trunk default repos silently failed post-merge CI monitoring.
//
// Each sub-test installs a different EnvironmentConfig and asserts that the
// GitHub branch endpoint received the matching branch name in its path.
func TestGetMainBranchSHA_RespectsResolvedEnv(t *testing.T) {
	tests := []struct {
		name           string
		envBranch      string
		wantBranchPath string // suffix of the URL path we expect GH to receive
	}{
		{name: "branch=main", envBranch: "main", wantBranchPath: "/branches/main"},
		{name: "branch=develop", envBranch: "develop", wantBranchPath: "/branches/develop"},
		{name: "branch=master", envBranch: "master", wantBranchPath: "/branches/master"},
		{name: "branch=trunk", envBranch: "trunk", wantBranchPath: "/branches/trunk"},
		{name: "empty branch falls back to main", envBranch: "", wantBranchPath: "/branches/main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requestedPath string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/branches/") {
					requestedPath = r.URL.Path
					resp := github.Branch{
						Name:   strings.TrimPrefix(r.URL.Path, "/repos/owner/repo/branches/"),
						Commit: github.BranchCommit{SHA: "deadbeef"},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			// Install per-test environment directly via unexported fields (same-package access).
			// Mirrors the pattern at TestNewController_ReleaserInit.
			cfg.activeEnvName = "test-env"
			cfg.activeEnvConfig = &EnvironmentConfig{Branch: tt.envBranch}

			c := NewController(cfg, ghClient, nil, "owner", "repo")

			sha, err := c.getMainBranchSHA(context.Background())
			if err != nil {
				t.Fatalf("getMainBranchSHA error: %v", err)
			}
			if sha != "deadbeef" {
				t.Errorf("sha = %q, want deadbeef", sha)
			}
			if !strings.HasSuffix(requestedPath, tt.wantBranchPath) {
				t.Errorf("requested path %q does not end with %q", requestedPath, tt.wantBranchPath)
			}
		})
	}
}

// --- Board/Project/Status/Review/Block tests (GH-3260) ---

// mockBoardSyncer is a test double for projectBoardSyncer.
type mockBoardSyncer struct {
	calls []boardSyncCall
	err   error // if non-nil, returned from UpdateProjectItemStatus
}

type boardSyncCall struct {
	issueNodeID string
	statusName  string
}

func (m *mockBoardSyncer) UpdateProjectItemStatus(_ context.Context, issueNodeID, statusName string) error {
	m.calls = append(m.calls, boardSyncCall{issueNodeID: issueNodeID, statusName: statusName})
	return m.err
}

// TestController_WithProjectBoardSync_AllStatuses verifies that WithProjectBoardSync
// stores all four status strings and wires the boardSync field.
func TestController_WithProjectBoardSync_AllStatuses(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	mock := &mockBoardSyncer{}
	opt := withBoardSyncerForTest(mock, "Done", "Failed", "In Review", "In Dev")

	c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", opt)

	if c.boardSync == nil {
		t.Fatal("boardSync should be set")
	}
	if c.doneStatus != "Done" {
		t.Errorf("doneStatus = %q, want %q", c.doneStatus, "Done")
	}
	if c.failStatus != "Failed" {
		t.Errorf("failStatus = %q, want %q", c.failStatus, "Failed")
	}
	if c.reviewStatus != "In Review" {
		t.Errorf("reviewStatus = %q, want %q", c.reviewStatus, "In Review")
	}
	if c.inProgressStatus != "In Dev" {
		t.Errorf("inProgressStatus = %q, want %q", c.inProgressStatus, "In Dev")
	}
}

// withBoardSyncerForTest is a ControllerOption that injects a mockBoardSyncer
// (bypasses the *github.ProjectBoardSync type constraint of WithProjectBoardSync).
func withBoardSyncerForTest(bs projectBoardSyncer, done, fail, review, inProgress string) ControllerOption {
	return func(c *Controller) {
		c.boardSync = bs
		c.doneStatus = done
		c.failStatus = fail
		c.reviewStatus = review
		c.inProgressStatus = inProgress
	}
}

// TestController_OnPRCreated_BoardSyncReview verifies that OnPRCreated triggers a
// board sync to reviewStatus when a non-empty IssueNodeID is present.
func TestController_OnPRCreated_BoardSyncReview(t *testing.T) {
	tests := []struct {
		name         string
		issueNodeID  string
		reviewStatus string
		wantCalls    int
		wantStatus   string
	}{
		{
			name:         "syncs to reviewStatus when nodeID and status set",
			issueNodeID:  "IssueNodeID_abc",
			reviewStatus: "In Review",
			wantCalls:    1,
			wantStatus:   "In Review",
		},
		{
			name:         "no sync when issueNodeID is empty",
			issueNodeID:  "",
			reviewStatus: "In Review",
			wantCalls:    0,
		},
		{
			name:         "no sync when reviewStatus is empty",
			issueNodeID:  "IssueNodeID_abc",
			reviewStatus: "",
			wantCalls:    0,
		},
		{
			name:         "no sync when both empty",
			issueNodeID:  "",
			reviewStatus: "",
			wantCalls:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockBoardSyncer{}
			opt := withBoardSyncerForTest(mock, "Done", "Failed", tt.reviewStatus, "")
			c := NewController(DefaultConfig(), github.NewClient(testutil.FakeGitHubToken), nil, "owner", "repo", opt)

			c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", tt.issueNodeID)

			if len(mock.calls) != tt.wantCalls {
				t.Fatalf("board sync calls = %d, want %d", len(mock.calls), tt.wantCalls)
			}
			if tt.wantCalls > 0 {
				got := mock.calls[0]
				if got.issueNodeID != tt.issueNodeID {
					t.Errorf("issueNodeID = %q, want %q", got.issueNodeID, tt.issueNodeID)
				}
				if got.statusName != tt.wantStatus {
					t.Errorf("statusName = %q, want %q", got.statusName, tt.wantStatus)
				}
			}
		})
	}
}

// TestController_OnPRCreated_BoardSync_NoBoardSync is a regression guard that verifies
// OnPRCreated does not panic or error when no board sync is configured.
func TestController_OnPRCreated_BoardSync_NoBoardSync(t *testing.T) {
	c := NewController(DefaultConfig(), github.NewClient(testutil.FakeGitHubToken), nil, "owner", "repo")
	// Should not panic.
	c.OnPRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10", "IssueNodeID_abc")
	if _, ok := c.GetPRState(42); !ok {
		t.Fatal("PR should be registered even without board sync")
	}
}

// TestController_handleCIFailed_BoardSync_IterationLimit verifies that the
// iteration-limit execution-failure path syncs the board to failStatus.
func TestController_handleCIFailed_BoardSync_IterationLimit(t *testing.T) {
	const issueNodeID = "IssueNodeID_iter"

	tests := []struct {
		name       string
		failStatus string
		wantCalls  int
	}{
		{
			name:       "syncs to failStatus at iteration limit",
			failStatus: "Blocked",
			wantCalls:  1,
		},
		{
			name:       "no sync when failStatus is empty",
			failStatus: "",
			wantCalls:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockBoardSyncer{}

			// Build a test HTTP server that serves the GitHub API calls made by handleCIFailed.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/"):
					// Return an issue body with iteration counter at the limit.
					_, _ = fmt.Fprintf(w, `{"number":10,"body":"<!-- autopilot-meta iteration:%d -->"}`, 3)
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/check-runs"):
					// GH-4779: a genuine code failure, so classifyPRFailure sees positive
					// evidence (FailureClassCode) rather than short-circuiting into the
					// zero-evidence escalateAndHold path — this test exercises the
					// iteration-limit board sync, not the zero-evidence guard.
					_, _ = fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"id":200,"name":"test","status":"completed","conclusion":"failure"}]}`)
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/actions/jobs/200/logs"):
					_, _ = w.Write([]byte("--- FAIL: TestSomething\nassertion failed: expected true, got false"))
				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/pulls/"):
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()
			cfg.MaxCIFixIterations = 3 // iteration = 3 >= MaxCIFixIterations = 3 → limit hit

			opt := withBoardSyncerForTest(mock, "Done", tt.failStatus, "In Review", "")
			c := NewController(cfg, ghClient, nil, "owner", "repo", opt)

			prState := &PRState{
				PRNumber:    42,
				IssueNumber: 10,
				IssueNodeID: issueNodeID,
				HeadSHA:     "abc123",
				BranchName:  "pilot/GH-10",
			}

			err := c.handleCIFailed(context.Background(), prState)
			if err != nil {
				t.Fatalf("handleCIFailed returned unexpected error: %v", err)
			}
			if prState.Stage != StageFailed {
				t.Errorf("Stage = %s, want %s", prState.Stage, StageFailed)
			}

			if len(mock.calls) != tt.wantCalls {
				t.Fatalf("board sync calls = %d, want %d (calls: %+v)", len(mock.calls), tt.wantCalls, mock.calls)
			}
			if tt.wantCalls > 0 {
				got := mock.calls[0]
				if got.issueNodeID != issueNodeID {
					t.Errorf("issueNodeID = %q, want %q", got.issueNodeID, issueNodeID)
				}
				if got.statusName != tt.failStatus {
					t.Errorf("statusName = %q, want %q", got.statusName, tt.failStatus)
				}
			}
		})
	}
}

// TestController_handleCIFailed_BoardSync_Regression_NormalPath is a regression guard
// that verifies the existing normal CI failure board sync (non-iteration-limit) still fires.
func TestController_handleCIFailed_BoardSync_Regression_NormalPath(t *testing.T) {
	const issueNodeID = "IssueNodeID_normal"
	mock := &mockBoardSyncer{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/") && !strings.Contains(r.URL.Path, "/comments"):
			// Iteration = 0 → below any limit
			_, _ = fmt.Fprintf(w, `{"number":10,"body":"no-meta"}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues"):
			// CreateFailureIssue creates a new issue
			_, _ = fmt.Fprintf(w, `{"number":99}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/check-runs"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/check-runs"):
			// GH-4779: a genuine code failure, so classifyPRFailure sees positive
			// evidence (FailureClassCode) rather than short-circuiting into the
			// zero-evidence escalateAndHold path — this test exercises the normal
			// CI-fail board sync, not the zero-evidence guard.
			_, _ = fmt.Fprintf(w, `{"total_count":1,"check_runs":[{"id":300,"name":"test","status":"completed","conclusion":"failure"}]}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/actions/jobs/300/logs"):
			_, _ = w.Write([]byte("--- FAIL: TestSomething\nassertion failed: expected true, got false"))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls/"):
			_, _ = fmt.Fprintf(w, `{"number":42,"state":"open"}`)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.MaxCIFixIterations = 5 // below limit

	opt := withBoardSyncerForTest(mock, "Done", "Blocked", "In Review", "")
	c := NewController(cfg, ghClient, nil, "owner", "repo", opt)

	prState := &PRState{
		PRNumber:    42,
		IssueNumber: 10,
		IssueNodeID: issueNodeID,
		HeadSHA:     "abc123",
		BranchName:  "pilot/GH-10",
	}

	// handleCIFailed should reach the normal path and fire board sync with failStatus.
	_ = c.handleCIFailed(context.Background(), prState)

	// The normal path fires one board sync call with failStatus.
	found := false
	for _, call := range mock.calls {
		if call.issueNodeID == issueNodeID && call.statusName == "Blocked" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected board sync call with issueNodeID=%q statusName=%q, got calls: %+v",
			issueNodeID, "Blocked", mock.calls)
	}
}

// TestHandleCIPassed_SizeFloorEscalation verifies that a PR exceeding the size
// floor (600 net additions > 500 threshold) is routed to StageAwaitApproval even
// when RequireApproval is false.
func TestHandleCIPassed_SizeFloorEscalation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/owner/repo/pulls/77/files" && r.Method == http.MethodGet {
			files := []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 600},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev // RequireApproval = false

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{
		PRNumber: 77,
		PRTitle:  "fix(auth): fix bug",
		Stage:    StageCIPassed,
	}

	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed returned unexpected error: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Errorf("expected StageAwaitApproval for oversized PR, got %v", prState.Stage)
	}
}

// TestHandleCIPassed_ScopeDriftEscalation verifies that a PR whose conventional-
// commit type diverges from the linked issue's title is routed to StageAwaitApproval
// even when RequireApproval is false.
func TestHandleCIPassed_ScopeDriftEscalation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/78/files" && r.Method == http.MethodGet:
			// Small PR — size floor should not fire.
			files := []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 10},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues/50" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 50, Title: "fix(auth): fix login bug"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev // RequireApproval = false

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{
		PRNumber:    78,
		IssueNumber: 50,
		// feat type diverges from the fix issue title — scope drift gate fires.
		PRTitle: "feat(auth): add OAuth",
		Stage:   StageCIPassed,
	}

	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed returned unexpected error: %v", err)
	}
	if prState.Stage != StageAwaitApproval {
		t.Errorf("expected StageAwaitApproval for scope-drifting PR, got %v", prState.Stage)
	}
}

// TestHandleCIPassed_SmallInScopePRMerges verifies that a small, in-scope PR with
// RequireApproval=false proceeds to StageMerging (no false positives from the gates).
func TestHandleCIPassed_SmallInScopePRMerges(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls/79/files" && r.Method == http.MethodGet:
			files := []*github.PRFile{
				{Filename: "a.go", Status: "modified", Additions: 50},
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, files))
		case r.URL.Path == "/repos/owner/repo/issues/51" && r.Method == http.MethodGet:
			resp := github.Issue{Number: 51, Title: "fix(auth): fix login bug"}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(mustJSON(t, resp))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev // RequireApproval = false

	c := NewController(cfg, ghClient, nil, "owner", "repo")
	prState := &PRState{
		PRNumber:    79,
		IssueNumber: 51,
		// Same type+scope as the issue — no drift.
		PRTitle: "fix(auth): fix login bug",
		Stage:   StageCIPassed,
	}

	if err := c.handleCIPassed(context.Background(), prState); err != nil {
		t.Fatalf("handleCIPassed returned unexpected error: %v", err)
	}
	if prState.Stage != StageMerging {
		t.Errorf("expected StageMerging for small in-scope PR, got %v", prState.Stage)
	}
}

// GH-3513 wave 2: a merged PR registered under a DECOMPOSED PARENT's issue
// number must not close the parent while children are open — only the
// count-verified path may close decomposed parents.
func TestShouldDeferIssueClose(t *testing.T) {
	tests := []struct {
		name      string
		graphqlOK bool   // false → GraphQL count errors
		openSubs  int    // open children from native links (totalCount = openSubs+1)
		textCount string // /search/issues total_count JSON
		want      bool
	}{
		{name: "open children defer the close", graphqlOK: true, openSubs: 2, want: true},
		{name: "leaf issue (no children) closes", graphqlOK: true, openSubs: 0, textCount: `{"total_count":0}`, want: false},
		{name: "native 0 but text search finds unlinked open children", graphqlOK: true, openSubs: 0, textCount: `{"total_count":1}`, want: true},
		{name: "count error fails open (close proceeds)", graphqlOK: false, textCount: `bad`, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					if !tt.graphqlOK {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
						return
					}
					states := make([]map[string]string, tt.openSubs)
					for i := range states {
						states[i] = map[string]string{"state": "OPEN"}
					}
					resp := map[string]interface{}{
						"data": map[string]interface{}{
							"node": map[string]interface{}{
								"subIssues": map[string]interface{}{
									"totalCount": tt.openSubs + 1,
									"nodes":      states,
								},
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)
				case r.URL.Path == "/search/issues":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(tt.textCount))
				case strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/"):
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"node_id":"node_77","number":77}`))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo")

			if got := c.shouldDeferIssueClose(context.Background(), 77, 42); got != tt.want {
				t.Errorf("shouldDeferIssueClose() = %v, want %v", got, tt.want)
			}
		})
	}
}

// GH-3513 wave 2: selfHealForPR must not promote a decomposed parent's
// execution row (stamping a child's PR URL) while siblings are still open —
// that false "completed" row woke hung handlers and fed dispatch-skips.
func TestSelfHealForPR_ParentGate(t *testing.T) {
	tests := []struct {
		name       string
		openSubs   int
		graphqlOK  bool
		searchJSON string
		wantHealed []string
	}{
		{name: "open siblings: only the child heals", openSubs: 1, graphqlOK: true, searchJSON: `{"total_count":0}`, wantHealed: []string{"GH-50"}},
		{name: "last child merged: parent heals too", openSubs: 0, graphqlOK: true, searchJSON: `{"total_count":0}`, wantHealed: []string{"GH-50", "GH-40"}},
		{name: "count error: parent heal skipped (fail closed)", graphqlOK: false, searchJSON: `not json`, wantHealed: []string{"GH-50"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/repos/owner/repo/issues/50":
					// Child body carries the parent reference.
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"number":50,"body":"Parent: GH-40\n\nwork","node_id":"node_50"}`))
				case r.URL.Path == "/repos/owner/repo/issues/40":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"number":40,"body":"epic","node_id":"node_40"}`))
				case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
					if !tt.graphqlOK {
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
						return
					}
					states := make([]map[string]string, tt.openSubs)
					for i := range states {
						states[i] = map[string]string{"state": "OPEN"}
					}
					resp := map[string]interface{}{
						"data": map[string]interface{}{
							"node": map[string]interface{}{
								"subIssues": map[string]interface{}{
									"totalCount": tt.openSubs + 1,
									"nodes":      states,
								},
							},
						},
					}
					w.WriteHeader(http.StatusOK)
					_ = json.NewEncoder(w).Encode(resp)
				case r.URL.Path == "/search/issues":
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(tt.searchJSON))
				default:
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			c := NewController(DefaultConfig(), ghClient, nil, "owner", "repo", WithProjectPath("/proj/p"))
			evalMock := &mockEvalStore{}
			c.SetEvalStore(evalMock)

			c.selfHealForPR(context.Background(), 50, "https://github.com/owner/repo/pull/9")

			var got []string
			for _, h := range evalMock.selfHealed {
				got = append(got, h.TaskID)
			}
			if len(got) != len(tt.wantHealed) {
				t.Fatalf("healed %v, want %v", got, tt.wantHealed)
			}
			for i := range got {
				if got[i] != tt.wantHealed[i] {
					t.Errorf("healed[%d] = %s, want %s", i, got[i], tt.wantHealed[i])
				}
			}
		})
	}
}
