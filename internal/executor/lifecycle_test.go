package executor

import (
	"errors"
	"testing"
	"time"
)

// TestExecutionLifecycle_Begin_CreatesRowAndThreadsID verifies Begin creates
// the executions row at the given initial status and stamps task.ExecutionID
// with the same ID it persisted (GH-4243).
func TestExecutionLifecycle_Begin_CreatesRowAndThreadsID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{
		ID:          "GH-1",
		Title:       "fix the thing",
		Description: "fix the thing",
		ProjectPath: "/tmp/project",
		Branch:      "pilot/GH-1",
		CreatePR:    true,
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID")
	}
	if task.ExecutionID != execID {
		t.Fatalf("expected task.ExecutionID %q, got %q", execID, task.ExecutionID)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load saved execution: %v", err)
	}
	if exec.Status != string(ExecStatusQueued) {
		t.Errorf("expected status %q, got %q", ExecStatusQueued, exec.Status)
	}
	if exec.TaskID != task.ID || exec.ProjectPath != task.ProjectPath || exec.TaskTitle != task.Title {
		t.Errorf("execution row fields don't match source task: %+v", exec)
	}
}

// TestExecutionLifecycle_Begin_NilStore_StillThreadsID mirrors the epic
// sub-issue path's mem-026 tolerance: a nil store (or a failed save) must
// never stop task.ExecutionID from being stamped, so downstream event
// recording always has a stable UUID rather than falling back to the
// human-readable task ID.
func TestExecutionLifecycle_Begin_NilStore_StillThreadsID(t *testing.T) {
	task := &Task{ID: "GH-2", ProjectPath: "/tmp/project"}

	execID, err := NewExecutionLifecycle(nil).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("expected nil-store Begin to succeed, got: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID even with a nil store")
	}
	if task.ExecutionID != execID {
		t.Fatalf("expected task.ExecutionID %q, got %q", execID, task.ExecutionID)
	}
}

// TestExecutionLifecycle_Transition_QueuedToRunning verifies the non-terminal
// status move used by the dispatcher's pickup path.
func TestExecutionLifecycle_Transition_QueuedToRunning(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-3", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if err := lifecycle.Transition(execID, ExecStatusRunning); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusRunning) {
		t.Errorf("expected status %q, got %q", ExecStatusRunning, exec.Status)
	}
}

// TestExecutionLifecycle_Finish_Success verifies the success path atomically
// persists status/pr_url/commit_sha and saves metrics.
func TestExecutionLifecycle_Finish_Success(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		TaskID:       task.ID,
		Success:      true,
		PRUrl:        "https://github.com/qf-studio/pilot/pull/1",
		CommitSHA:    "deadbeef",
		TokensInput:  100,
		TokensOutput: 200,
		TokensTotal:  300,
	}
	outcome, err := lifecycle.Finish(execID, result, nil, 42*time.Second)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusCompleted {
		t.Errorf("expected outcome status %q, got %q", ExecStatusCompleted, outcome.Status)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusCompleted) {
		t.Errorf("expected status %q, got %q", ExecStatusCompleted, exec.Status)
	}
	if exec.PRUrl != result.PRUrl || exec.CommitSHA != result.CommitSHA {
		t.Errorf("expected pr_url/commit_sha to match result, got %+v", exec)
	}
	if exec.TokensTotal != result.TokensTotal {
		t.Errorf("expected metrics to be persisted: tokens_total = %d, want %d", exec.TokensTotal, result.TokensTotal)
	}
}

// TestExecutionLifecycle_Finish_ExecErrShortCircuits verifies an execErr
// forces StatusFailed regardless of what result would otherwise classify to.
func TestExecutionLifecycle_Finish_ExecErrShortCircuits(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-5", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	execErr := errors.New("backend crashed")
	outcome, err := lifecycle.Finish(execID, nil, execErr, time.Second)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusFailed {
		t.Errorf("expected outcome status %q, got %q", ExecStatusFailed, outcome.Status)
	}
	if outcome.Error != execErr.Error() {
		t.Errorf("expected outcome error %q, got %q", execErr.Error(), outcome.Error)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusFailed) {
		t.Errorf("expected status %q, got %q", ExecStatusFailed, exec.Status)
	}
	if exec.Error != execErr.Error() {
		t.Errorf("expected error %q, got %q", execErr.Error(), exec.Error)
	}
}

// TestExecutionLifecycle_Finish_Override reproduces epic.go's work-loss guard:
// a sub-issue that reports Success with real commits but no PR must persist
// as failed even though TerminalStatus alone would call it completed.
func TestExecutionLifecycle_Finish_Override(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-6", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		TaskID:    task.ID,
		Success:   true,
		CommitSHA: "deadbeef",
		// PRUrl intentionally empty: commits with no PR is the stranded-work case.
	}

	if classified := TerminalStatus(result); classified != "completed" {
		t.Fatalf("test setup invalid: expected TerminalStatus to classify as completed without an override, got %q", classified)
	}

	outcome, err := lifecycle.Finish(execID, result, nil, time.Second, ExecStatusFailed)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusFailed {
		t.Errorf("expected override to force status %q, got %q", ExecStatusFailed, outcome.Status)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusFailed) {
		t.Errorf("expected status %q, got %q", ExecStatusFailed, exec.Status)
	}
}

// TestExecutionLifecycle_NilStore_MethodsAreNoOps verifies every method
// degrades gracefully with a nil store, mirroring the "logStore == nil"
// guards this type consolidates.
func TestExecutionLifecycle_NilStore_MethodsAreNoOps(t *testing.T) {
	lifecycle := NewExecutionLifecycle(nil)

	if err := lifecycle.Transition("some-id", ExecStatusRunning); err != nil {
		t.Errorf("expected nil-store Transition to be a no-op, got: %v", err)
	}

	outcome, err := lifecycle.Finish("some-id", &ExecutionResult{Success: true}, nil, time.Second)
	if err != nil {
		t.Errorf("expected nil-store Finish to be a no-op, got: %v", err)
	}
	if outcome.Status != "" {
		t.Errorf("expected zero-value outcome from nil-store Finish, got: %+v", outcome)
	}
}
