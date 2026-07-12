package executor

import (
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func newLifecycleTestStore(t *testing.T) *memory.Store {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestExecutionLifecycle_Begin_ThreadsExecutionID is the GH-4243 regression
// test for the defect class this issue exists to close (TASK-394 epic path,
// GH-4205 CLI path): Begin must create the row AND stamp task.ExecutionID so
// Task.LogExecutionID() resolves to a real executions.id, letting an
// execution_events insert satisfy the foreign key instead of failing with
// FOREIGN KEY constraint failed (787).
func TestExecutionLifecycle_Begin_ThreadsExecutionID(t *testing.T) {
	store := newLifecycleTestStore(t)
	lifecycle := NewExecutionLifecycle(store)

	task := &Task{
		ID:          "GH-4243",
		Title:       "refactor lifecycle",
		Description: "single chokepoint",
		ProjectPath: "/tmp/project",
		Branch:      "pilot/GH-4243",
		CreatePR:    true,
		Labels:      []string{"pilot"},
	}

	execID, err := lifecycle.Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID")
	}
	if task.ExecutionID != execID {
		t.Fatalf("expected task.ExecutionID to be stamped with %q, got %q", execID, task.ExecutionID)
	}
	if got := task.LogExecutionID(); got != execID {
		t.Fatalf("expected LogExecutionID() to resolve to %q, got %q", execID, got)
	}
	if err := store.InsertExecutionEvent(task.LogExecutionID(), memory.StageQueued, "queued"); err != nil {
		t.Fatalf("execution_events insert should satisfy FK against the Begin-created row, got: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusQueued) {
		t.Errorf("expected status %q, got %q", ExecStatusQueued, exec.Status)
	}
	if exec.TaskID != task.ID || exec.ProjectPath != task.ProjectPath {
		t.Errorf("expected task_id/project_path to match task, got task_id=%q project_path=%q", exec.TaskID, exec.ProjectPath)
	}
}

// TestExecutionLifecycle_Transition_UpdatesStatus covers the non-terminal
// queued->running move used by the dispatcher's ProjectWorker.
func TestExecutionLifecycle_Transition_UpdatesStatus(t *testing.T) {
	store := newLifecycleTestStore(t)
	lifecycle := NewExecutionLifecycle(store)

	task := &Task{ID: "GH-1", ProjectPath: "/tmp/project"}
	execID, err := lifecycle.Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if err := lifecycle.Transition(execID, ExecStatusRunning); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusRunning) {
		t.Errorf("expected status %q, got %q", ExecStatusRunning, exec.Status)
	}
}

func TestExecutionLifecycle_Finish_Success(t *testing.T) {
	store := newLifecycleTestStore(t)
	lifecycle := NewExecutionLifecycle(store)

	task := &Task{ID: "GH-2", ProjectPath: "/tmp/project"}
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		Success:      true,
		PRUrl:        "https://github.com/qf-studio/pilot/pull/1",
		CommitSHA:    "deadbeef",
		TokensInput:  10,
		TokensOutput: 20,
	}
	status, errMsg := Classify(nil, result)
	if status != ExecStatusCompleted {
		t.Fatalf("expected Classify to return ExecStatusCompleted, got %q", status)
	}
	if errMsg != "" {
		t.Fatalf("expected empty errMsg on success, got %q", errMsg)
	}
	if err := lifecycle.Finish(execID, status, errMsg, result, 42*time.Second); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusCompleted) {
		t.Errorf("expected status %q, got %q", ExecStatusCompleted, exec.Status)
	}
	if exec.PRUrl != result.PRUrl {
		t.Errorf("expected pr_url %q, got %q", result.PRUrl, exec.PRUrl)
	}
	if exec.CommitSHA != result.CommitSHA {
		t.Errorf("expected commit_sha %q, got %q", result.CommitSHA, exec.CommitSHA)
	}
	if exec.TokensInput != result.TokensInput || exec.TokensOutput != result.TokensOutput {
		t.Errorf("expected metrics to be persisted, got tokens_input=%d tokens_output=%d", exec.TokensInput, exec.TokensOutput)
	}
}

func TestExecutionLifecycle_Finish_ExecutionError(t *testing.T) {
	store := newLifecycleTestStore(t)
	lifecycle := NewExecutionLifecycle(store)

	task := &Task{ID: "GH-3", ProjectPath: "/tmp/project"}
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	execErr := errors.New("backend crashed")
	status, errMsg := Classify(execErr, nil)
	if status != ExecStatusFailed {
		t.Fatalf("expected ExecStatusFailed, got %q", status)
	}
	if errMsg != execErr.Error() {
		t.Fatalf("expected errMsg %q, got %q", execErr.Error(), errMsg)
	}
	if err := lifecycle.Finish(execID, status, errMsg, nil, time.Second); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusFailed) {
		t.Errorf("expected status %q, got %q", ExecStatusFailed, exec.Status)
	}
	if exec.Error != execErr.Error() {
		t.Errorf("expected error %q, got %q", execErr.Error(), exec.Error)
	}
}

// TestExecutionLifecycle_Finish_ClassifiesNonSuccess verifies Classify wraps
// TerminalStatus (TASK-358) instead of collapsing every !Success result into
// "failed".
func TestExecutionLifecycle_Finish_ClassifiesNonSuccess(t *testing.T) {
	store := newLifecycleTestStore(t)
	lifecycle := NewExecutionLifecycle(store)

	task := &Task{ID: "GH-4", ProjectPath: "/tmp/project"}
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{Success: false, Outcome: "no_op", Error: "no changes needed"}
	status, errMsg := Classify(nil, result)
	if status != ExecStatusNoOp {
		t.Fatalf("expected ExecStatusNoOp, got %q", status)
	}
	if err := lifecycle.Finish(execID, status, errMsg, result, time.Second); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusNoOp) {
		t.Errorf("expected status %q, got %q", ExecStatusNoOp, exec.Status)
	}
	if exec.Error != result.Error {
		t.Errorf("expected error %q, got %q", result.Error, exec.Error)
	}
}

// TestExecutionLifecycle_Finish_ExplicitOverride covers epic.go's work-loss
// guard: a result can report Success=true yet the caller must still force a
// "failed" terminal status (commits with no PR). Finish must honor an
// explicit status rather than re-deriving it from result.
func TestExecutionLifecycle_Finish_ExplicitOverride(t *testing.T) {
	store := newLifecycleTestStore(t)
	lifecycle := NewExecutionLifecycle(store)

	task := &Task{ID: "GH-5", ProjectPath: "/tmp/project"}
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{Success: true, CommitSHA: "abc123"} // no PRUrl
	if err := lifecycle.Finish(execID, ExecStatusFailed, "committed work but no PR", result, time.Second); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(ExecStatusFailed) {
		t.Errorf("expected status %q (override), got %q", ExecStatusFailed, exec.Status)
	}
}
