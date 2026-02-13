package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// subIssuePRCall records a single invocation of the SubIssuePRCallback.
type subIssuePRCall struct {
	PRNumber    int
	PRURL       string
	IssueNumber int
	CommitSHA   string
	BranchName  string
}

// newTestRunnerWithExecFunc creates a Runner that uses the given function
// instead of r.Execute for sub-issue execution. This avoids the full
// backend/git/webhook stack, making ExecuteSubIssues unit-testable.
func newTestRunnerWithExecFunc(execFn func(ctx context.Context, task *Task) (*ExecutionResult, error)) *Runner {
	return &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				Command: "echo", // unused, but prevents nil panics
			},
		},
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		modelRouter:       NewModelRouter(nil, nil),
		executeFunc:       execFn,
	}
}

func TestExecuteSubIssues_CallbackFiresForEachPR(t *testing.T) {
	// Table of sub-issues with expected PR results
	subIssues := []CreatedIssue{
		{
			Number:  10,
			URL:     "https://github.com/owner/repo/issues/10",
			Subtask: PlannedSubtask{Title: "Create schema", Description: "Migration", Order: 1},
		},
		{
			Number:  11,
			URL:     "https://github.com/owner/repo/issues/11",
			Subtask: PlannedSubtask{Title: "Add endpoints", Description: "REST API", Order: 2},
		},
		{
			Number:  12,
			URL:     "https://github.com/owner/repo/issues/12",
			Subtask: PlannedSubtask{Title: "Write tests", Description: "Unit tests", Order: 3},
		},
	}

	// Expected PR data for each sub-issue
	expectedPRs := []struct {
		prNumber  int
		commitSHA string
		prURL     string
	}{
		{prNumber: 100, commitSHA: "abc1234", prURL: "https://github.com/owner/repo/pull/100"},
		{prNumber: 101, commitSHA: "def5678", prURL: "https://github.com/owner/repo/pull/101"},
		{prNumber: 102, commitSHA: "ghi9012", prURL: "https://github.com/owner/repo/pull/102"},
	}

	// Mock execute function returns success with PR URLs
	callIdx := 0
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		idx := callIdx
		callIdx++
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    fmt.Sprintf("Completed %s", task.Title),
			PRUrl:     expectedPRs[idx].prURL,
			CommitSHA: expectedPRs[idx].commitSHA,
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)

	// Register callback and collect invocations
	var mu sync.Mutex
	var calls []subIssuePRCall
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, subIssuePRCall{
			PRNumber:    prNumber,
			PRURL:       prURL,
			IssueNumber: issueNumber,
			CommitSHA:   commitSHA,
			BranchName:  branchName,
		})
	})

	parent := &Task{
		ID:    "GH-50",
		Title: "[epic] Build auth system",
	}

	err := runner.ExecuteSubIssues(context.Background(), parent, subIssues, parent.ProjectPath)
	if err != nil {
		t.Fatalf("ExecuteSubIssues returned error: %v", err)
	}

	// Assert callback fired exactly 3 times
	if len(calls) != 3 {
		t.Fatalf("expected 3 callback calls, got %d", len(calls))
	}

	// Verify each callback invocation
	for i, call := range calls {
		if call.PRNumber != expectedPRs[i].prNumber {
			t.Errorf("call[%d].PRNumber = %d, want %d", i, call.PRNumber, expectedPRs[i].prNumber)
		}
		if call.PRURL != expectedPRs[i].prURL {
			t.Errorf("call[%d].PRURL = %q, want %q", i, call.PRURL, expectedPRs[i].prURL)
		}
		if call.IssueNumber != subIssues[i].Number {
			t.Errorf("call[%d].IssueNumber = %d, want %d", i, call.IssueNumber, subIssues[i].Number)
		}
		if call.CommitSHA != expectedPRs[i].commitSHA {
			t.Errorf("call[%d].CommitSHA = %q, want %q", i, call.CommitSHA, expectedPRs[i].commitSHA)
		}
		expectedBranch := fmt.Sprintf("pilot/GH-%d", subIssues[i].Number)
		if call.BranchName != expectedBranch {
			t.Errorf("call[%d].BranchName = %q, want %q", i, call.BranchName, expectedBranch)
		}
	}
}

func TestExecuteSubIssues_NilCallbackNoPanic(t *testing.T) {
	// Mock execute that returns a successful result with a PR URL
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    "done",
			PRUrl:     "https://github.com/owner/repo/pull/200",
			CommitSHA: "deadbeef",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	// Intentionally NOT setting onSubIssuePRCreated — must not panic

	parent := &Task{
		ID:    "GH-60",
		Title: "[epic] Safe nil callback",
	}

	issues := []CreatedIssue{
		{
			Number:  20,
			URL:     "https://github.com/owner/repo/issues/20",
			Subtask: PlannedSubtask{Title: "Only task", Description: "desc", Order: 1},
		},
	}

	err := runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath)
	if err != nil {
		t.Fatalf("ExecuteSubIssues should not error with nil callback: %v", err)
	}
}

func TestExecuteSubIssues_CallbackNotFiredOnNoPRUrl(t *testing.T) {
	// Execution succeeds but no PR URL (e.g., CreatePR was false or PR creation failed)
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    "done",
			PRUrl:     "", // No PR
			CommitSHA: "abc123",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)

	callbackFired := false
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName string) {
		callbackFired = true
	})

	parent := &Task{
		ID:    "GH-70",
		Title: "[epic] No PR test",
	}

	issues := []CreatedIssue{
		{
			Number:  30,
			URL:     "https://github.com/owner/repo/issues/30",
			Subtask: PlannedSubtask{Title: "No PR task", Description: "desc", Order: 1},
		},
	}

	err := runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath)
	if err != nil {
		t.Fatalf("ExecuteSubIssues returned error: %v", err)
	}

	if callbackFired {
		t.Error("callback should not fire when PRUrl is empty")
	}
}

func TestExecuteSubIssues_CallbackNotFiredOnFailure(t *testing.T) {
	// First sub-issue succeeds with PR, second fails — callback should fire once
	callCount := 0
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		callCount++
		if callCount == 1 {
			return &ExecutionResult{
				TaskID:    task.ID,
				Success:   true,
				PRUrl:     "https://github.com/owner/repo/pull/300",
				CommitSHA: "sha1",
			}, nil
		}
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: false,
			Error:   "compilation error",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)

	var calls []subIssuePRCall
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName string) {
		calls = append(calls, subIssuePRCall{
			PRNumber:    prNumber,
			PRURL:       prURL,
			IssueNumber: issueNumber,
			CommitSHA:   commitSHA,
			BranchName:  branchName,
		})
	})

	parent := &Task{
		ID:    "GH-80",
		Title: "[epic] Partial failure",
	}

	issues := []CreatedIssue{
		{Number: 40, Subtask: PlannedSubtask{Title: "Good task", Order: 1}},
		{Number: 41, Subtask: PlannedSubtask{Title: "Bad task", Order: 2}},
	}

	err := runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath)
	if err == nil {
		t.Fatal("ExecuteSubIssues should return error when sub-issue fails")
	}

	// Callback should have fired exactly once (for the successful sub-issue)
	if len(calls) != 1 {
		t.Fatalf("expected 1 callback call, got %d", len(calls))
	}
	if calls[0].IssueNumber != 40 {
		t.Errorf("callback issue number = %d, want 40", calls[0].IssueNumber)
	}
	if calls[0].PRNumber != 300 {
		t.Errorf("callback PR number = %d, want 300", calls[0].PRNumber)
	}
}

func TestExecuteSubIssues_CallbackNotFiredOnExecError(t *testing.T) {
	// Execute returns an error (not just unsuccessful result)
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return nil, fmt.Errorf("backend unavailable")
	}

	runner := newTestRunnerWithExecFunc(execFn)

	callbackFired := false
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName string) {
		callbackFired = true
	})

	parent := &Task{ID: "GH-90", Title: "[epic] Exec error"}
	issues := []CreatedIssue{
		{Number: 50, Subtask: PlannedSubtask{Title: "Task", Order: 1}},
	}

	err := runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath)
	if err == nil {
		t.Fatal("expected error from ExecuteSubIssues")
	}

	if callbackFired {
		t.Error("callback should not fire when Execute returns error")
	}
}

func TestParsePRNumberFromURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected int
	}{
		{
			name:     "standard github PR url",
			url:      "https://github.com/owner/repo/pull/123",
			expected: 123,
		},
		{
			name:     "github enterprise PR url",
			url:      "https://github.example.com/org/repo/pull/456",
			expected: 456,
		},
		{
			name:     "url with trailing newline",
			url:      "https://github.com/owner/repo/pull/789\n",
			expected: 789, // TrimSpace handles trailing whitespace
		},
		{
			name:     "large PR number",
			url:      "https://github.com/owner/repo/pull/99999",
			expected: 99999,
		},
		{
			name:     "empty string",
			url:      "",
			expected: 0,
		},
		{
			name:     "issue url not PR",
			url:      "https://github.com/owner/repo/issues/123",
			expected: 0,
		},
		{
			name:     "no number after pull",
			url:      "https://github.com/owner/repo/pull/",
			expected: 0,
		},
		{
			name:     "PR url with trailing path",
			url:      "https://github.com/owner/repo/pull/42/files",
			expected: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parsePRNumberFromURL(tt.url)
			if result != tt.expected {
				t.Errorf("parsePRNumberFromURL(%q) = %d, want %d", tt.url, result, tt.expected)
			}
		})
	}
}
