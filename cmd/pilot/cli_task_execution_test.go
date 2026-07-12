package main

// GH-4205: `pilot task` (the standalone CLI path) executed correctly and
// wrote to execution_logs via runner.SetLogStore(), but never called
// memory.Store.SaveExecution() to create the parent row in the executions
// table. That left pilot status/logs/metrics empty for CLI-run tasks and
// caused execution_events inserts to fail with FOREIGN KEY constraint
// failed (787), since Task.LogExecutionID() fell back to the human-readable
// task ID with no matching executions.id row.
//
// recordCLITaskStart/recordCLITaskFinish (commands.go) mirror the
// dispatcher's queueSingleTask/post-execute persistence
// (internal/executor/dispatcher.go) for the CLI path. These tests exercise
// them directly against a real (temp-file) SQLite store.

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

func newCLITaskTestStore(t *testing.T) *memory.Store {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "pilot-cli-task-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })

	store, err := memory.NewStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

func TestRecordCLITaskStart_CreatesRunningRow(t *testing.T) {
	store := newCLITaskTestStore(t)

	task := &executor.Task{
		ID:          "TASK-12345",
		Title:       "fix the thing",
		Description: "fix the thing",
		ProjectPath: "/tmp/project",
		Branch:      "pilot/TASK-12345",
		CreatePR:    true,
	}

	execID, err := recordCLITaskStart(store, task)
	if err != nil {
		t.Fatalf("recordCLITaskStart failed: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID")
	}
	if task.ExecutionID != execID {
		t.Fatalf("expected task.ExecutionID to be stamped with %q, got %q", execID, task.ExecutionID)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load saved execution: %v", err)
	}
	if exec.Status != "running" {
		t.Errorf("expected status 'running', got %q", exec.Status)
	}
	if exec.TaskID != task.ID {
		t.Errorf("expected task_id %q, got %q", task.ID, exec.TaskID)
	}
	if exec.ProjectPath != task.ProjectPath {
		t.Errorf("expected project_path %q, got %q", task.ProjectPath, exec.ProjectPath)
	}

	// GH-4205: the whole point of stamping ExecutionID is that execution_events
	// rows referencing task.LogExecutionID() now satisfy the FK against a real
	// executions.id instead of the bare human-readable task ID.
	if got := task.LogExecutionID(); got != execID {
		t.Fatalf("expected LogExecutionID() to resolve to the saved execution row %q, got %q", execID, got)
	}
	if err := store.InsertExecutionEvent(task.LogExecutionID(), memory.StageRunning, "worker started"); err != nil {
		t.Fatalf("execution_events insert should satisfy FK against the saved executions row, got: %v", err)
	}
}

func TestRecordCLITaskFinish_Success(t *testing.T) {
	store := newCLITaskTestStore(t)

	task := &executor.Task{ID: "TASK-1", ProjectPath: "/tmp/project"}
	execID, err := recordCLITaskStart(store, task)
	if err != nil {
		t.Fatalf("recordCLITaskStart failed: %v", err)
	}

	result := &executor.ExecutionResult{
		TaskID:    task.ID,
		Success:   true,
		PRUrl:     "https://github.com/qf-studio/pilot/pull/1",
		CommitSHA: "deadbeef",
	}
	if err := recordCLITaskFinish(store, execID, nil, result, 42*time.Second); err != nil {
		t.Fatalf("recordCLITaskFinish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", exec.Status)
	}
	if exec.PRUrl != result.PRUrl {
		t.Errorf("expected pr_url %q, got %q", result.PRUrl, exec.PRUrl)
	}
	if exec.CommitSHA != result.CommitSHA {
		t.Errorf("expected commit_sha %q, got %q", result.CommitSHA, exec.CommitSHA)
	}
}

func TestRecordCLITaskFinish_ExecutionError(t *testing.T) {
	store := newCLITaskTestStore(t)

	task := &executor.Task{ID: "TASK-2", ProjectPath: "/tmp/project"}
	execID, err := recordCLITaskStart(store, task)
	if err != nil {
		t.Fatalf("recordCLITaskStart failed: %v", err)
	}

	execErr := errors.New("backend crashed")
	if err := recordCLITaskFinish(store, execID, execErr, nil, time.Second); err != nil {
		t.Fatalf("recordCLITaskFinish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", exec.Status)
	}
	if exec.Error != execErr.Error() {
		t.Errorf("expected error %q, got %q", execErr.Error(), exec.Error)
	}
}

func TestRecordCLITaskFinish_UnsuccessfulResult(t *testing.T) {
	store := newCLITaskTestStore(t)

	task := &executor.Task{ID: "TASK-3", ProjectPath: "/tmp/project"}
	execID, err := recordCLITaskStart(store, task)
	if err != nil {
		t.Fatalf("recordCLITaskStart failed: %v", err)
	}

	result := &executor.ExecutionResult{
		TaskID:  task.ID,
		Success: false,
		Error:   "declined: no changes needed",
	}
	if err := recordCLITaskFinish(store, execID, nil, result, time.Second); err != nil {
		t.Fatalf("recordCLITaskFinish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	// Classified via executor.TerminalStatus rather than collapsed to "failed"
	// (see internal/executor/runner.go TerminalStatus / TASK-358).
	expected := executor.TerminalStatus(result)
	if exec.Status != expected {
		t.Errorf("expected status %q, got %q", expected, exec.Status)
	}
	if exec.Error != result.Error {
		t.Errorf("expected error %q, got %q", result.Error, exec.Error)
	}
}
