package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// execCall records the arguments of an executeFunc invocation.
type execCall struct {
	TaskID string
	Branch string
	Title  string
	Order  int // 1-indexed execution order
}

// makeSubIssues creates n CreatedIssues numbered starting from startNum.
func makeSubIssues(n, startNum int) []CreatedIssue {
	issues := make([]CreatedIssue, n)
	for i := 0; i < n; i++ {
		num := startNum + i
		issues[i] = CreatedIssue{
			Number: num,
			URL:    fmt.Sprintf("https://github.com/owner/repo/issues/%d", num),
			Subtask: PlannedSubtask{
				Title:       fmt.Sprintf("Sub-issue %d", i+1),
				Description: fmt.Sprintf("Description for sub-issue %d", i+1),
				Order:       i + 1,
			},
		}
	}
	return issues
}

// sequentialRunner builds a Runner with a mock executeFunc and PR-callback
// collector. The executeFunc records each invocation order and delegates to
// the caller-supplied resultFn to decide success/failure per sub-issue.
type sequentialRunner struct {
	Runner    *Runner
	ExecCalls []execCall
	PRCalls   []subIssuePRCall
	mu        sync.Mutex
}

func newSequentialRunner(
	resultFn func(idx int, task *Task) (*ExecutionResult, error),
) *sequentialRunner {
	sr := &sequentialRunner{}

	callIdx := 0
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		sr.mu.Lock()
		idx := callIdx
		callIdx++
		sr.ExecCalls = append(sr.ExecCalls, execCall{
			TaskID: task.ID,
			Branch: task.Branch,
			Title:  task.Title,
			Order:  idx + 1,
		})
		sr.mu.Unlock()
		return resultFn(idx, task)
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName string) {
		sr.mu.Lock()
		defer sr.mu.Unlock()
		sr.PRCalls = append(sr.PRCalls, subIssuePRCall{
			PRNumber:    prNumber,
			PRURL:       prURL,
			IssueNumber: issueNumber,
			CommitSHA:   commitSHA,
			BranchName:  branchName,
		})
	})

	sr.Runner = runner
	return sr
}

// TestSequentialEpicFlow is a table-driven integration test covering
// the full ExecuteSubIssues lifecycle: execution ordering, callback
// invocation, progress tracking, and failure handling.
func TestSequentialEpicFlow(t *testing.T) {
	tests := []struct {
		name string

		// numSubIssues is the number of sub-issues to create.
		numSubIssues int

		// resultFn returns the ExecutionResult for the i-th sub-issue (0-indexed).
		resultFn func(idx int, task *Task) (*ExecutionResult, error)

		// wantErr is true if ExecuteSubIssues should return an error.
		wantErr bool

		// wantErrContains is a substring expected in the error message (if wantErr).
		wantErrContains string

		// wantExecCount is the expected number of executeFunc invocations.
		wantExecCount int

		// wantPRCallbackCount is the expected number of PR callback invocations.
		wantPRCallbackCount int

		// wantPRNumbers lists the expected PR numbers from callbacks, in order.
		wantPRNumbers []int

		// wantBranches lists the expected branch names from exec calls, in order.
		wantBranches []string
	}{
		{
			name:         "happy path - 3 sub-issues all succeed",
			numSubIssues: 3,
			resultFn: func(idx int, task *Task) (*ExecutionResult, error) {
				prNum := 200 + idx
				return &ExecutionResult{
					TaskID:    task.ID,
					Success:   true,
					Output:    fmt.Sprintf("Completed %s", task.Title),
					PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", prNum),
					CommitSHA: fmt.Sprintf("sha-%d", idx),
				}, nil
			},
			wantErr:             false,
			wantExecCount:       3,
			wantPRCallbackCount: 3,
			wantPRNumbers:       []int{200, 201, 202},
			wantBranches: []string{
				"pilot/GH-100",
				"pilot/GH-101",
				"pilot/GH-102",
			},
		},
		{
			name:         "middle sub-issue fails - execution stops at failure",
			numSubIssues: 3,
			resultFn: func(idx int, task *Task) (*ExecutionResult, error) {
				if idx == 1 { // second sub-issue fails
					return &ExecutionResult{
						TaskID:  task.ID,
						Success: false,
						Error:   "CI check failed: lint errors",
					}, nil
				}
				prNum := 300 + idx
				return &ExecutionResult{
					TaskID:    task.ID,
					Success:   true,
					Output:    "done",
					PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", prNum),
					CommitSHA: fmt.Sprintf("sha-%d", idx),
				}, nil
			},
			wantErr:         true,
			wantErrContains: "sub-issue 101 failed",
			// Current behavior: abort on first failure.
			// First sub-issue succeeds (exec+callback), second fails (exec only), third never runs.
			wantExecCount:       2,
			wantPRCallbackCount: 1,
			wantPRNumbers:       []int{300},
			wantBranches: []string{
				"pilot/GH-100",
				"pilot/GH-101",
			},
		},
		{
			name:         "first sub-issue returns exec error - immediate abort",
			numSubIssues: 3,
			resultFn: func(idx int, task *Task) (*ExecutionResult, error) {
				if idx == 0 {
					return nil, fmt.Errorf("backend unavailable: connection refused")
				}
				return &ExecutionResult{
					TaskID:    task.ID,
					Success:   true,
					PRUrl:     "https://github.com/owner/repo/pull/999",
					CommitSHA: "sha-x",
				}, nil
			},
			wantErr:             true,
			wantErrContains:     "sub-issue 100 failed",
			wantExecCount:       1,
			wantPRCallbackCount: 0,
			wantPRNumbers:       nil,
			wantBranches: []string{
				"pilot/GH-100",
			},
		},
		{
			name:         "all sub-issues fail - first failure stops execution",
			numSubIssues: 3,
			resultFn: func(idx int, task *Task) (*ExecutionResult, error) {
				return &ExecutionResult{
					TaskID:  task.ID,
					Success: false,
					Error:   fmt.Sprintf("compilation error in sub-issue %d", idx+1),
				}, nil
			},
			wantErr:             true,
			wantErrContains:     "sub-issue 100 failed",
			wantExecCount:       1, // Stops at first failure
			wantPRCallbackCount: 0,
			wantPRNumbers:       nil,
			wantBranches: []string{
				"pilot/GH-100",
			},
		},
		{
			name:         "context cancellation - stops before next sub-issue",
			numSubIssues: 3,
			resultFn: func(idx int, task *Task) (*ExecutionResult, error) {
				// All would succeed, but context gets cancelled externally
				return &ExecutionResult{
					TaskID:    task.ID,
					Success:   true,
					Output:    "done",
					PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", 400+idx),
					CommitSHA: fmt.Sprintf("sha-%d", idx),
				}, nil
			},
			// Special handling: we cancel context after first execution.
			// See test body below for override.
			wantErr:             true,
			wantErrContains:     "execution cancelled",
			wantExecCount:       1,
			wantPRCallbackCount: 1,
			wantPRNumbers:       []int{400},
			wantBranches: []string{
				"pilot/GH-100",
			},
		},
		{
			name:         "sub-issue succeeds but no PR URL - callback skipped",
			numSubIssues: 2,
			resultFn: func(idx int, task *Task) (*ExecutionResult, error) {
				if idx == 0 {
					// First sub-issue: success but no PR (docs-only change)
					return &ExecutionResult{
						TaskID:    task.ID,
						Success:   true,
						Output:    "docs updated",
						PRUrl:     "",
						CommitSHA: "sha-docs",
					}, nil
				}
				return &ExecutionResult{
					TaskID:    task.ID,
					Success:   true,
					Output:    "code updated",
					PRUrl:     "https://github.com/owner/repo/pull/500",
					CommitSHA: "sha-code",
				}, nil
			},
			wantErr:             false,
			wantExecCount:       2,
			wantPRCallbackCount: 1, // Only second sub-issue triggers callback
			wantPRNumbers:       []int{500},
			wantBranches: []string{
				"pilot/GH-100",
				"pilot/GH-101",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := makeSubIssues(tt.numSubIssues, 100)
			parent := &Task{
				ID:    "GH-50",
				Title: "[epic] Integration test epic",
			}

			// Special case: context cancellation test
			if tt.name == "context cancellation - stops before next sub-issue" {
				ctx, cancel := context.WithCancel(context.Background())

				callCount := 0
				sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
					callCount++
					result, err := tt.resultFn(idx, task)
					// Cancel after first successful execution
					if callCount == 1 {
						cancel()
					}
					return result, err
				})

				err := sr.Runner.ExecuteSubIssues(ctx, parent, issues)

				if err == nil {
					t.Fatal("expected error from cancelled context")
				}
				if !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErrContains)
				}
				if len(sr.ExecCalls) != tt.wantExecCount {
					t.Errorf("exec call count = %d, want %d", len(sr.ExecCalls), tt.wantExecCount)
				}
				if len(sr.PRCalls) != tt.wantPRCallbackCount {
					t.Errorf("PR callback count = %d, want %d", len(sr.PRCalls), tt.wantPRCallbackCount)
				}
				return
			}

			sr := newSequentialRunner(tt.resultFn)
			err := sr.Runner.ExecuteSubIssues(context.Background(), parent, issues)

			// Check error expectation
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErrContains)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			// Verify execution count
			if len(sr.ExecCalls) != tt.wantExecCount {
				t.Errorf("exec call count = %d, want %d", len(sr.ExecCalls), tt.wantExecCount)
			}

			// Verify PR callback count
			if len(sr.PRCalls) != tt.wantPRCallbackCount {
				t.Errorf("PR callback count = %d, want %d", len(sr.PRCalls), tt.wantPRCallbackCount)
			}

			// Verify PR numbers in order
			if tt.wantPRNumbers != nil {
				for i, want := range tt.wantPRNumbers {
					if i >= len(sr.PRCalls) {
						t.Errorf("missing PR callback at index %d", i)
						continue
					}
					if sr.PRCalls[i].PRNumber != want {
						t.Errorf("PR callback[%d].PRNumber = %d, want %d", i, sr.PRCalls[i].PRNumber, want)
					}
				}
			}

			// Verify branches in execution order
			if tt.wantBranches != nil {
				for i, want := range tt.wantBranches {
					if i >= len(sr.ExecCalls) {
						t.Errorf("missing exec call at index %d", i)
						continue
					}
					if sr.ExecCalls[i].Branch != want {
						t.Errorf("exec call[%d].Branch = %q, want %q", i, sr.ExecCalls[i].Branch, want)
					}
				}
			}
		})
	}
}

// TestSequentialEpicFlow_ExecutionOrder verifies that sub-issues execute
// strictly in sequence — each sub-issue's executeFunc must complete before
// the next begins.
func TestSequentialEpicFlow_ExecutionOrder(t *testing.T) {
	var mu sync.Mutex
	var timeline []string

	issues := makeSubIssues(3, 200)
	parent := &Task{
		ID:    "GH-ORDER",
		Title: "[epic] Ordering test",
	}

	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		mu.Lock()
		timeline = append(timeline, fmt.Sprintf("start-%d", idx))
		mu.Unlock()

		// Small sleep to verify sequential (not parallel) execution
		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		timeline = append(timeline, fmt.Sprintf("end-%d", idx))
		mu.Unlock()

		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", 500+idx),
			CommitSHA: fmt.Sprintf("sha-%d", idx),
		}, nil
	})

	err := sr.Runner.ExecuteSubIssues(context.Background(), parent, issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify strict sequential ordering: start-0, end-0, start-1, end-1, start-2, end-2
	expected := []string{
		"start-0", "end-0",
		"start-1", "end-1",
		"start-2", "end-2",
	}

	if len(timeline) != len(expected) {
		t.Fatalf("timeline length = %d, want %d: %v", len(timeline), len(expected), timeline)
	}

	for i, want := range expected {
		if timeline[i] != want {
			t.Errorf("timeline[%d] = %q, want %q (full: %v)", i, timeline[i], want, timeline)
		}
	}
}

// TestSequentialEpicFlow_TaskConstruction verifies that ExecuteSubIssues
// constructs correct Task objects for each sub-issue (ID, Branch, CreatePR, etc.).
func TestSequentialEpicFlow_TaskConstruction(t *testing.T) {
	issues := makeSubIssues(3, 300)
	parent := &Task{
		ID:          "GH-CONSTRUCT",
		Title:       "[epic] Task construction test",
		ProjectPath: "/tmp/test-project",
	}

	var capturedTasks []*Task
	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		// Deep copy the task for verification
		cp := *task
		capturedTasks = append(capturedTasks, &cp)
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", 600+idx),
			CommitSHA: fmt.Sprintf("sha-%d", idx),
		}, nil
	})

	err := sr.Runner.ExecuteSubIssues(context.Background(), parent, issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedTasks) != 3 {
		t.Fatalf("expected 3 captured tasks, got %d", len(capturedTasks))
	}

	for i, task := range capturedTasks {
		expectedID := fmt.Sprintf("GH-%d", 300+i)
		if task.ID != expectedID {
			t.Errorf("task[%d].ID = %q, want %q", i, task.ID, expectedID)
		}

		expectedBranch := fmt.Sprintf("pilot/GH-%d", 300+i)
		if task.Branch != expectedBranch {
			t.Errorf("task[%d].Branch = %q, want %q", i, task.Branch, expectedBranch)
		}

		if !task.CreatePR {
			t.Errorf("task[%d].CreatePR = false, want true", i)
		}

		if task.ProjectPath != parent.ProjectPath {
			t.Errorf("task[%d].ProjectPath = %q, want %q", i, task.ProjectPath, parent.ProjectPath)
		}

		expectedTitle := fmt.Sprintf("Sub-issue %d", i+1)
		if task.Title != expectedTitle {
			t.Errorf("task[%d].Title = %q, want %q", i, task.Title, expectedTitle)
		}
	}
}

// TestSequentialEpicFlow_PRCallbackFields verifies that all fields in the
// PR callback are populated correctly for each sub-issue.
func TestSequentialEpicFlow_PRCallbackFields(t *testing.T) {
	issues := makeSubIssues(3, 400)
	parent := &Task{
		ID:    "GH-FIELDS",
		Title: "[epic] Callback fields test",
	}

	expectedSHAs := []string{"deadbeef", "cafebabe", "f00dcafe"}
	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", 700+idx),
			CommitSHA: expectedSHAs[idx],
		}, nil
	})

	err := sr.Runner.ExecuteSubIssues(context.Background(), parent, issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sr.PRCalls) != 3 {
		t.Fatalf("expected 3 PR callbacks, got %d", len(sr.PRCalls))
	}

	for i, call := range sr.PRCalls {
		// PR number
		wantPR := 700 + i
		if call.PRNumber != wantPR {
			t.Errorf("call[%d].PRNumber = %d, want %d", i, call.PRNumber, wantPR)
		}

		// PR URL
		wantURL := fmt.Sprintf("https://github.com/owner/repo/pull/%d", wantPR)
		if call.PRURL != wantURL {
			t.Errorf("call[%d].PRURL = %q, want %q", i, call.PRURL, wantURL)
		}

		// Issue number
		wantIssue := 400 + i
		if call.IssueNumber != wantIssue {
			t.Errorf("call[%d].IssueNumber = %d, want %d", i, call.IssueNumber, wantIssue)
		}

		// Commit SHA
		if call.CommitSHA != expectedSHAs[i] {
			t.Errorf("call[%d].CommitSHA = %q, want %q", i, call.CommitSHA, expectedSHAs[i])
		}

		// Branch name
		wantBranch := fmt.Sprintf("pilot/GH-%d", 400+i)
		if call.BranchName != wantBranch {
			t.Errorf("call[%d].BranchName = %q, want %q", i, call.BranchName, wantBranch)
		}
	}
}

// TestSequentialEpicFlow_EmptyIssuesList verifies that ExecuteSubIssues
// returns an error when given an empty slice.
func TestSequentialEpicFlow_EmptyIssuesList(t *testing.T) {
	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		t.Fatal("executeFunc should not be called for empty issues")
		return nil, nil
	})

	parent := &Task{ID: "GH-EMPTY", Title: "[epic] Empty test"}
	err := sr.Runner.ExecuteSubIssues(context.Background(), parent, nil)

	if err == nil {
		t.Fatal("expected error for empty issues")
	}
	if !strings.Contains(err.Error(), "no sub-issues") {
		t.Errorf("error = %q, want substring 'no sub-issues'", err.Error())
	}
}

// TestSequentialEpicFlow_PartialSuccessThenFailure verifies that when the
// last sub-issue fails, all previous sub-issues' PR callbacks were still fired.
func TestSequentialEpicFlow_PartialSuccessThenFailure(t *testing.T) {
	issues := makeSubIssues(4, 500)
	parent := &Task{
		ID:    "GH-PARTIAL",
		Title: "[epic] Partial success test",
	}

	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		if idx == 3 { // Last sub-issue fails
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   "test suite timeout",
			}, nil
		}
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    "ok",
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", 800+idx),
			CommitSHA: fmt.Sprintf("sha-%d", idx),
		}, nil
	})

	err := sr.Runner.ExecuteSubIssues(context.Background(), parent, issues)
	if err == nil {
		t.Fatal("expected error from last sub-issue failure")
	}

	// 3 succeeded + 1 failed = 4 exec calls
	if len(sr.ExecCalls) != 4 {
		t.Errorf("exec call count = %d, want 4", len(sr.ExecCalls))
	}

	// Only 3 PR callbacks (the failing one doesn't get a callback)
	if len(sr.PRCalls) != 3 {
		t.Errorf("PR callback count = %d, want 3", len(sr.PRCalls))
	}

	// Error should mention the failing issue number
	if !strings.Contains(err.Error(), "sub-issue 503 failed") {
		t.Errorf("error = %q, want mention of sub-issue 503", err.Error())
	}
}

// TestSequentialEpicFlow_ContextDeadline verifies that a context timeout
// is respected between sub-issue executions.
func TestSequentialEpicFlow_ContextDeadline(t *testing.T) {
	issues := makeSubIssues(5, 600)
	parent := &Task{
		ID:    "GH-TIMEOUT",
		Title: "[epic] Timeout test",
	}

	// Use a deadline that expires after the first sub-issue completes.
	// Use 500ms to account for race detector overhead and slow CI runners.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	execCount := 0
	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		execCount++
		// First call succeeds quickly, subsequent calls will hit deadline
		if idx == 0 {
			return &ExecutionResult{
				TaskID:    task.ID,
				Success:   true,
				PRUrl:     "https://github.com/owner/repo/pull/900",
				CommitSHA: "sha-0",
			}, nil
		}
		// Simulate slow execution that will exceed deadline (600ms > 500ms timeout)
		time.Sleep(600 * time.Millisecond)
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%d", 900+idx),
			CommitSHA: fmt.Sprintf("sha-%d", idx),
		}, nil
	})

	err := sr.Runner.ExecuteSubIssues(ctx, parent, issues)
	if err == nil {
		t.Fatal("expected error from context deadline")
	}

	// Should have executed at least 1 sub-issue before timeout
	if execCount < 1 {
		t.Errorf("expected at least 1 execution before timeout, got %d", execCount)
	}

	// Should not have executed all 5
	if execCount == 5 {
		t.Error("expected timeout before all 5 sub-issues completed")
	}
}

// TestSequentialEpicFlow_SingleSubIssue verifies the edge case of an epic
// with exactly one sub-issue.
func TestSequentialEpicFlow_SingleSubIssue(t *testing.T) {
	issues := makeSubIssues(1, 700)
	parent := &Task{
		ID:    "GH-SINGLE",
		Title: "[epic] Single sub-issue",
	}

	sr := newSequentialRunner(func(idx int, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			Output:    "single done",
			PRUrl:     "https://github.com/owner/repo/pull/1000",
			CommitSHA: "sha-single",
		}, nil
	})

	err := sr.Runner.ExecuteSubIssues(context.Background(), parent, issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sr.ExecCalls) != 1 {
		t.Errorf("exec call count = %d, want 1", len(sr.ExecCalls))
	}
	if len(sr.PRCalls) != 1 {
		t.Errorf("PR callback count = %d, want 1", len(sr.PRCalls))
	}
	if sr.PRCalls[0].CommitSHA != "sha-single" {
		t.Errorf("commit SHA = %q, want %q", sr.PRCalls[0].CommitSHA, "sha-single")
	}
}
