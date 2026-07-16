package executor

import (
	"context"
	"fmt"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecuteSubIssues_LedgerRowPerChild is the GH-4141 regression test: each
// in-process sub-issue run must get its own executions row (running →
// completed), threaded through as the sub-task's ExecutionID, instead of
// falling back to the human-readable task ID as the execution_events join
// key. Before this fix, every execution_events write for a sub-issue tripped
// a FOREIGN KEY constraint failed (787) warning because no executions row
// existed with id = "GH-N".
func TestExecuteSubIssues_LedgerRowPerChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	issues := makeSubIssues(2, 4141)
	parent := &Task{ID: "GH-4140", Title: "[epic] ledger row per child", ProjectPath: "/project-4141"}

	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		if task.ExecutionID == "" {
			t.Errorf("task %s: ExecutionID not set before backend invocation", task.ID)
		}
		// Mirrors every real recordExecutionEvent call site inside
		// executeWithOptions: keyed on task.LogExecutionID(), which must now
		// resolve to a real executions.id, not the "GH-N" task ID.
		if err := store.InsertExecutionEvent(task.LogExecutionID(), memory.StageClaudeStarted, "test invocation"); err != nil {
			t.Errorf("task %s: InsertExecutionEvent failed (FK error?): %v", task.ID, err)
		}
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			CommitSHA: "deadbeef" + task.ID,
			PRUrl:     "https://github.com/owner/repo/pull/" + task.ID,
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store

	childStates, _, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, parent.ProjectPath, "")
	if err != nil {
		t.Fatalf("executeSubIssuesTracked returned error: %v", err)
	}
	if len(childStates) != 2 {
		t.Fatalf("expected 2 child states, got %d: %v", len(childStates), childStates)
	}
	for _, state := range childStates {
		if state != "completed" {
			t.Errorf("expected all children completed, got %q", state)
		}
	}

	for _, issue := range issues {
		taskID := fmt.Sprintf("GH-%d", issue.Number)
		row, err := store.GetLatestExecutionByTaskID(taskID, parent.ProjectPath)
		if err != nil {
			t.Fatalf("GetLatestExecutionByTaskID(%s): %v", taskID, err)
		}
		if row.Status != "completed" {
			t.Errorf("%s: status = %q, want completed", taskID, row.Status)
		}
		if row.ProjectPath != parent.ProjectPath {
			t.Errorf("%s: project_path = %q, want parent's absolute path %q", taskID, row.ProjectPath, parent.ProjectPath)
		}
		if row.PRUrl == "" {
			t.Errorf("%s: pr_url not recorded", taskID)
		}

		events, err := store.ListExecutionEvents(row.ID)
		if err != nil {
			t.Fatalf("ListExecutionEvents(%s): %v", row.ID, err)
		}
		if len(events) == 0 {
			t.Errorf("%s: expected execution_events recorded against the ledger row UUID %s, got none", taskID, row.ID)
		}
	}
}
