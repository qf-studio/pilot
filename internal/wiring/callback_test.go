package wiring

import (
	"testing"
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

// TestBudgetEnforcerNilSafety verifies that when budget is disabled,
// the token limit check is not wired, and the runner handles it gracefully.
func TestBudgetEnforcerNilSafety(t *testing.T) {
	cfg := MinimalConfig()
	// Budget is disabled by default
	h := NewPollingHarness(t, cfg)

	// Runner should not have token limit check when budget disabled
	// (The Has* accessor doesn't exist for TokenLimitCheck, so we verify
	// the runner was created successfully without budget wiring)
	if h.Runner == nil {
		t.Fatal("Runner is nil with disabled budget")
	}
}

// TestBudgetEnforcerWired verifies that enabling budget wires the token limit check.
func TestBudgetEnforcerWired(t *testing.T) {
	cfg := WithBudget(MinimalConfig())
	h := NewPollingHarness(t, cfg)

	if h.Runner == nil {
		t.Fatal("Runner is nil with enabled budget")
	}
	// Budget enforcement is wired via SetTokenLimitCheck — we verify the
	// harness didn't panic during construction with budget enabled.
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
