package main

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/executor"
)

// TestHandleIssueGeneric_BudgetExceeded verifies that handleIssueGeneric returns early
// when the budget enforcer is paused, without reaching the execution step.
func TestHandleIssueGeneric_BudgetExceeded(t *testing.T) {
	cfg := &budget.Config{Enabled: true}
	enforcer := budget.NewEnforcer(cfg, nil)
	enforcer.Pause("daily limit exceeded")

	monitor := executor.NewMonitor()

	deps := HandlerDeps{
		Monitor:  monitor,
		Enforcer: enforcer,
		// Runner and Dispatcher intentionally nil — must not be reached due to budget block
	}
	info := IssueInfo{
		TaskID:   "GH-999",
		Title:    "Test Issue",
		URL:      "https://github.com/test/repo/issues/999",
		Adapter:  "github",
		LogEmoji: "📥",
	}
	task := &executor.Task{
		ID:    "GH-999",
		Title: "Test Issue",
		Branch: "pilot/GH-999",
	}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)

	if err == nil {
		t.Fatal("expected error from budget enforcement, got nil")
	}
	if !strings.HasPrefix(err.Error(), "budget enforcement:") {
		t.Errorf("expected budget enforcement error, got: %v", err)
	}
	if hr.Success {
		t.Error("expected Success=false on budget exceeded")
	}
	if hr.BranchName != "pilot/GH-999" {
		t.Errorf("expected BranchName=pilot/GH-999, got %q", hr.BranchName)
	}
}

// TestHandleIssueGeneric_MonitorRegistration verifies that the monitor is populated
// with task state when handleIssueGeneric is called (budget exceeded path ensures
// monitor.Register is reached before the early return).
func TestHandleIssueGeneric_MonitorRegistration(t *testing.T) {
	cfg := &budget.Config{Enabled: true}
	enforcer := budget.NewEnforcer(cfg, nil)
	enforcer.Pause("test limit")

	monitor := executor.NewMonitor()

	deps := HandlerDeps{
		Monitor:  monitor,
		Enforcer: enforcer,
	}
	info := IssueInfo{
		TaskID:   "APP-123",
		Title:    "Linear task title",
		URL:      "https://linear.app/issue/APP-123",
		Adapter:  "linear",
		LogEmoji: "📊",
	}
	task := &executor.Task{
		ID:     "APP-123",
		Title:  "Linear task title",
		Branch: "pilot/APP-123",
	}

	_, _ = handleIssueGeneric(context.Background(), deps, info, task)

	// Verify monitor.Register was called: the monitor should have the task state
	state, ok := monitor.Get("APP-123")
	if !ok || state == nil {
		t.Fatal("expected monitor to have task APP-123 registered, got nil")
	}
	if state.Title != "Linear task title" {
		t.Errorf("expected task title %q, got %q", "Linear task title", state.Title)
	}
}

// TestAdapterSpecificPRNumberExtraction verifies that PR/MR number extraction
// uses the correct adapter-specific regex for each forge (GH-2293).
func TestAdapterSpecificPRNumberExtraction(t *testing.T) {
	tests := []struct {
		name     string
		adapter  string
		prURL    string
		wantNum  int
		wantFail bool
	}{
		{
			name:    "github PR URL",
			adapter: "github",
			prURL:   "https://github.com/org/repo/pull/42",
			wantNum: 42,
		},
		{
			name:    "gitlab MR URL",
			adapter: "gitlab",
			prURL:   "https://gitlab.com/namespace/project/-/merge_requests/17",
			wantNum: 17,
		},
		{
			name:    "gitlab MR URL without dash prefix",
			adapter: "gitlab",
			prURL:   "https://gitlab.example.com/group/repo/merge_requests/99",
			wantNum: 99,
		},
		{
			name:    "azuredevops PR URL",
			adapter: "azuredevops",
			prURL:   "https://dev.azure.com/org/project/_git/repo/pullrequest/55",
			wantNum: 55,
		},
		{
			name:     "github extractor does not match gitlab URL",
			adapter:  "github",
			prURL:    "https://gitlab.com/ns/proj/-/merge_requests/10",
			wantNum:  0,
			wantFail: true,
		},
		{
			name:     "gitlab extractor does not match github URL",
			adapter:  "gitlab",
			prURL:    "https://github.com/org/repo/pull/10",
			wantNum:  0,
			wantFail: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			var err error
			switch tc.adapter {
			case "gitlab":
				got, err = gitlab.ExtractMRNumber(tc.prURL)
			case "azuredevops":
				got, err = azuredevops.ExtractPRNumber(tc.prURL)
			default:
				got, err = github.ExtractPRNumber(tc.prURL)
			}

			if tc.wantFail {
				if err == nil {
					t.Errorf("expected extraction to fail for adapter=%s url=%s, got %d", tc.adapter, tc.prURL, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("extraction failed for adapter=%s url=%s: %v", tc.adapter, tc.prURL, err)
			}
			if got != tc.wantNum {
				t.Errorf("expected PR number %d, got %d (adapter=%s url=%s)", tc.wantNum, got, tc.adapter, tc.prURL)
			}
		})
	}
}

// TestHandleIssueGeneric_NilEnforcer verifies that nil enforcer skips budget check
// and proceeds. Because runner is also nil, it should fail at execution.
func TestHandleIssueGeneric_NilEnforcer(t *testing.T) {
	deps := HandlerDeps{
		Enforcer: nil,
		// Runner nil and Dispatcher nil — will panic at execution step
	}
	info := IssueInfo{
		TaskID:   "GH-1",
		Title:    "No enforcer",
		URL:      "https://github.com/org/repo/issues/1",
		Adapter:  "github",
		LogEmoji: "📥",
	}
	task := &executor.Task{
		ID:     "GH-1",
		Title:  "No enforcer",
		Branch: "pilot/GH-1",
	}

	// With nil runner and nil dispatcher the function will panic at the execution step.
	// We recover to confirm execution was actually attempted (budget check was skipped).
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from nil runner.Execute call, indicating budget check was skipped")
		}
	}()

	_, _ = handleIssueGeneric(context.Background(), deps, info, task)
}
