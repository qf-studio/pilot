package wiring

import (
	"testing"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/dashboard"
)

// TestOnPRCreatedCallbackWired verifies that the OnSubIssuePRCreated callback
// is wired from Runner → autopilot Controller in both harness modes.
func TestOnPRCreatedCallbackWired(t *testing.T) {
	for _, mode := range []string{"polling", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			cfg := MinimalConfig()
			var h *Harness
			if mode == "polling" {
				h = NewPollingHarness(t, cfg)
			} else {
				h = NewGatewayHarness(t, cfg)
			}

			if !h.Runner.HasOnSubIssuePRCreated() {
				t.Fatal("OnSubIssuePRCreated callback not wired")
			}
		})
	}
}

// TestOnPRCreatedFlowsToAutopilot verifies that calling OnPRCreated on the
// controller doesn't panic and accepts a PR (smoke test for the wiring).
func TestOnPRCreatedFlowsToAutopilot(t *testing.T) {
	cfg := WithAutopilot(MinimalConfig())
	h := NewPollingHarness(t, cfg)

	// Calling OnPRCreated should not panic
	h.Controller.OnPRCreated(1, "https://github.com/test/repo/pull/1", 42, "abc123", "test-branch", "")
}

// TestGitHubMockPRCreation verifies that the GitHubMock CreatePR method
// returns a properly structured PR and tracks it internally.
func TestGitHubMockPRCreation(t *testing.T) {
	cfg := MinimalConfig()
	h := NewPollingHarness(t, cfg)

	pr := h.GHMock.CreatePR(2, "New Feature", "feature-branch", "def456")
	if pr == nil {
		t.Fatal("CreatePR returned nil")
	}
	if pr.Number != 2 {
		t.Errorf("expected PR number 2, got %d", pr.Number)
	}
	if pr.Title != "New Feature" {
		t.Errorf("expected title 'New Feature', got %q", pr.Title)
	}
	if pr.Head.Ref != "feature-branch" {
		t.Errorf("expected head ref 'feature-branch', got %q", pr.Head.Ref)
	}
}

// TestBudgetEnforcerGatewayPath exercises the gateway handler path with budget
// disabled (no panic, HasTokenLimitCheck false) and enabled (HasTokenLimitCheck true).
func TestBudgetEnforcerGatewayPath(t *testing.T) {
	t.Run("budget disabled", func(t *testing.T) {
		cfg := MinimalConfig() // budget disabled by default
		h := NewGatewayHarness(t, cfg)

		if h.Runner.HasTokenLimitCheck() {
			t.Error("HasTokenLimitCheck should be false when budget is disabled")
		}
	})

	t.Run("budget enabled", func(t *testing.T) {
		cfg := WithBudget(MinimalConfig())
		h := NewGatewayHarness(t, cfg)

		if !h.Runner.HasTokenLimitCheck() {
			t.Error("HasTokenLimitCheck should be true when budget is enabled")
		}
	})

	t.Run("budget parity polling vs gateway", func(t *testing.T) {
		cfg1 := WithBudget(MinimalConfig())
		polling := NewPollingHarness(t, cfg1)

		cfg2 := WithBudget(MinimalConfig())
		gateway := NewGatewayHarness(t, cfg2)

		if polling.Runner.HasTokenLimitCheck() != gateway.Runner.HasTokenLimitCheck() {
			t.Error("budget token limit check parity mismatch between polling and gateway")
		}
	})
}

// TestMultiRepoConfigCreation verifies that WithMultiRepo produces a valid
// config with multiple projects for dashboard controller visibility testing.
func TestMultiRepoConfigCreation(t *testing.T) {
	cfg := WithMultiRepo(MinimalConfig())

	if len(cfg.Projects) == 0 {
		t.Fatal("expected at least one project after WithMultiRepo")
	}

	proj := cfg.Projects[len(cfg.Projects)-1]
	if proj.Name != "secondary" {
		t.Errorf("expected project name 'secondary', got %q", proj.Name)
	}
	if proj.GitHub == nil {
		t.Fatal("expected GitHub config on secondary project")
	}
	if proj.GitHub.Owner != "test-owner" {
		t.Errorf("expected owner 'test-owner', got %q", proj.GitHub.Owner)
	}
	if proj.GitHub.Repo != "test-repo-2" {
		t.Errorf("expected repo 'test-repo-2', got %q", proj.GitHub.Repo)
	}
}

// TestMultiRepoDashboardControllerVisibility configures 3 repos via WithMultiRepo,
// builds a harness for each, and verifies dashboard model construction.
//
// Limitation: Each harness gets its own autopilot.Controller scoped to one repo.
// The dashboard Model constructor accepts a single Controller, so multi-repo
// visibility requires constructing one Model per controller. There is no
// built-in aggregation across controllers in the current dashboard API.
func TestMultiRepoDashboardControllerVisibility(t *testing.T) {
	// Build a config with 3 repos (primary from MinimalConfig + 2 additional).
	cfg := MinimalConfig()
	WithMultiRepo(cfg) // adds "secondary" (test-repo-2)
	cfg.Projects = append(cfg.Projects, &config.ProjectConfig{
		Name: "tertiary",
		Path: "/tmp/test-repo-3",
		GitHub: &config.ProjectGitHubConfig{
			Owner: "test-owner",
			Repo:  "test-repo-3",
		},
	})

	if len(cfg.Projects) < 2 {
		t.Fatalf("expected at least 2 projects, got %d", len(cfg.Projects))
	}

	// Build a harness for each project config to simulate multi-repo setup.
	// Each harness gets an independent store, controller, and runner.
	type repoHarness struct {
		name    string
		harness *Harness
		model   dashboard.Model
	}
	repos := make([]repoHarness, 0, len(cfg.Projects))

	for _, proj := range cfg.Projects {
		projCfg := MinimalConfig()
		WithAutopilot(projCfg)

		h := NewPollingHarness(t, projCfg)

		// Construct dashboard model per-controller (single-controller limitation).
		m := dashboard.NewModelWithStoreAndAutopilot("test", h.Store, h.Controller)
		repos = append(repos, repoHarness{
			name:    proj.Name,
			harness: h,
			model:   m,
		})
	}

	// Verify each repo got its own independent harness and model.
	if len(repos) < 2 {
		t.Fatalf("expected at least 2 repo harnesses, got %d", len(repos))
	}

	for i, rh := range repos {
		if rh.harness.Controller == nil {
			t.Errorf("repo[%d] %q: Controller is nil", i, rh.name)
		}
		if rh.harness.Store == nil {
			t.Errorf("repo[%d] %q: Store is nil", i, rh.name)
		}
		if rh.harness.Runner == nil {
			t.Errorf("repo[%d] %q: Runner is nil", i, rh.name)
		}
	}

	// Verify controllers are distinct instances (not shared).
	if repos[0].harness.Controller == repos[1].harness.Controller {
		t.Error("controllers should be distinct instances across repos")
	}
}
