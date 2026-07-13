package executor

import (
	"context"
	"fmt"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestDispatcher_QueueTask_CrossProjectTaskIDCollision is a GH-4276
// regression test: task_id is not unique across projects (a fresh repo's
// issue numbering restarts at #1 and collides with every other configured
// project's history), so a completed/queued row for task_id under project A
// must never suppress dispatch or decomposition for the identical task_id
// under project B.
//
// Root cause (GH-4276): Store.IsTaskQueued and Dispatcher.IsActive were
// unscoped by project_path, so QueueTask's pre-decompose duplicate check
// (dispatcher.go) could see a same-numbered task queued/running in a
// different project and reject dispatch with ErrTaskAlreadyActive before the
// decomposer ever ran.
func TestDispatcher_QueueTask_CrossProjectTaskIDCollision(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const taskID = "GH-10"
	const projectA = "/tmp/gh-4276-project-a"
	const projectB = "/tmp/gh-4276-project-b"

	// Seed project A with both a genuine completed execution and a currently
	// queued/running row for the identical task_id — the two distinct
	// unscoped-lookup classes (HasCompletedExecution-style and
	// IsTaskQueued-style) this defect covers.
	if err := store.SaveExecution(&memory.Execution{
		ID:          "gh4276-a-completed",
		TaskID:      taskID,
		ProjectPath: projectA,
		Status:      "completed",
		CommitSHA:   "deadbeef",
	}); err != nil {
		t.Fatalf("failed to seed completed execution for project A: %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID:          "gh4276-a-running",
		TaskID:      taskID,
		ProjectPath: projectA,
		Status:      "running",
	}); err != nil {
		t.Fatalf("failed to seed running execution for project A: %v", err)
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)
	dispatcher.SetDecomposer(NewTaskDecomposer(&DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}))

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// (a) IsActive must not be short-circuited by project A's rows.
	if dispatcher.IsActive(taskID, projectB) {
		t.Fatal("IsActive() = true for project B, want false — cross-project task_id collision suppressed dispatch")
	}

	task := &Task{
		ID:    taskID,
		Title: "[epic] roll out multi-service rollout",
		Description: `This task requires refactoring the entire authentication system with multiple changes:

1. Update the user model to include new fields for MFA
2. Refactor the login endpoint to support MFA flow
3. Add new middleware for session validation
4. Update the frontend components for MFA input
5. Add comprehensive tests for all changes

This is a complex architectural change that spans multiple files.`,
		ProjectPath: projectB,
		Branch:      "feature/auth-refactor",
		CreatePR:    true,
	}

	// (b) dispatch must proceed — not rejected as already-active.
	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("QueueTask() error = %v, want nil — cross-project task_id collision must not block dispatch", err)
	}

	// (c) decomposition must run: the parent row is recorded "decomposed",
	// not silently skipped/no-op'd as a duplicate of project A's task_id.
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution() error: %v", err)
	}
	if exec.Status != string(ExecStatusDecomposed) {
		t.Errorf("parent exec.Status = %q, want %q — decomposition was short-circuited", exec.Status, ExecStatusDecomposed)
	}
	if exec.ProjectPath != projectB {
		t.Errorf("parent exec.ProjectPath = %q, want %q", exec.ProjectPath, projectB)
	}

	// Subtasks race the worker goroutine (ensureWorker starts it as soon as
	// the first subtask is queued), so checking queued-only status is racy —
	// look up each expected subtask row directly instead, regardless of
	// whether it has already moved past "queued".
	for i := 1; i <= 5; i++ {
		subTaskID := fmt.Sprintf("%s-%d", taskID, i)
		rows, err := store.ListExecutionsForTask(subTaskID)
		if err != nil {
			t.Fatalf("ListExecutionsForTask(%s) error: %v", subTaskID, err)
		}
		if len(rows) != 1 {
			t.Errorf("got %d execution rows for subtask %s, want 1", len(rows), subTaskID)
			continue
		}
		if rows[0].ProjectPath != projectB {
			t.Errorf("subtask %s ProjectPath = %q, want %q", subTaskID, rows[0].ProjectPath, projectB)
		}
	}
}

// TestStore_IsTaskQueued_CrossProjectCollision is a focused GH-4276
// regression test directly on the store method: a task_id queued/running
// under one project must not read as queued under a different project with
// the identical task_id.
func TestStore_IsTaskQueued_CrossProjectCollision(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID:          "gh4276-store-running",
		TaskID:      "GH-1",
		ProjectPath: "/project-a",
		Status:      "running",
	}); err != nil {
		t.Fatalf("failed to seed execution: %v", err)
	}

	queued, err := store.IsTaskQueued("GH-1", "/project-a")
	if err != nil {
		t.Fatalf("IsTaskQueued() error: %v", err)
	}
	if !queued {
		t.Error("expected GH-1 to be queued under /project-a")
	}

	queued, err = store.IsTaskQueued("GH-1", "/project-b")
	if err != nil {
		t.Fatalf("IsTaskQueued() error: %v", err)
	}
	if queued {
		t.Error("expected GH-1 to NOT be queued under /project-b (cross-project task_id collision)")
	}
}
