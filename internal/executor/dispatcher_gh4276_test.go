package executor

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestQueueTask_CrossProjectTaskIDCollision_StillDispatchesAndDecomposes is
// the GH-4276 regression test: task_id is only unique within a single
// repo/project (a fresh SaaS-tenant repo's issue #10 collides with any other
// configured project's pre-existing "GH-10"). A stale/in-flight execution row
// for the same task_id in an unrelated project must never suppress dispatch
// or decomposition of a genuinely fresh task in a different project.
func TestQueueTask_CrossProjectTaskIDCollision_StillDispatchesAndDecomposes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const (
		otherProject   = "/other-project"
		sandboxProject = "/sandbox-project"
		taskID         = "GH-10"
	)

	// An unrelated project has a genuinely completed GH-10 (old, real work)...
	if err := store.SaveExecution(&memory.Execution{
		ID: "other-completed", TaskID: taskID, ProjectPath: otherProject,
		Status: "completed", PRUrl: "https://github.com/org/other/pull/1",
	}); err != nil {
		t.Fatalf("SaveExecution(other-completed): %v", err)
	}
	// ...and a concurrently in-flight GH-10 of its own.
	if err := store.SaveExecution(&memory.Execution{
		ID: "other-running", TaskID: taskID, ProjectPath: otherProject, Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(other-running): %v", err)
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

	// The sandbox project's own, never-before-seen GH-10 — same task_id as
	// the unrelated project's completed+running rows above, structured so
	// the decomposer splits it into multiple subtasks.
	task := &Task{
		ID:    taskID,
		Title: "[epic] roll out multi-service rollout",
		Description: `This epic requires several coordinated changes across services:

1. Update the user model to include new fields for MFA
2. Refactor the login endpoint to support MFA flow
3. Add new middleware for session validation
4. Update the frontend components for MFA input
5. Add comprehensive tests for all changes

This is a complex architectural change spanning multiple files.`,
		ProjectPath: sandboxProject,
		Branch:      "feature/mfa-rollout",
		CreatePR:    true,
	}

	if dispatcher.IsActive(taskID, sandboxProject) {
		t.Fatal("expected IsActive=false for the sandbox project's fresh GH-10 before dispatch")
	}

	parentExecID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("QueueTask() should not be blocked by an unrelated project's identical task_id, got error: %v", err)
	}

	parentExec, err := store.GetExecution(parentExecID)
	if err != nil {
		t.Fatalf("GetExecution(parent): %v", err)
	}
	if parentExec.ProjectPath != sandboxProject {
		t.Fatalf("parentExec.ProjectPath = %q, want %q", parentExec.ProjectPath, sandboxProject)
	}
	if parentExec.Status != string(ExecStatusDecomposed) {
		t.Fatalf("parentExec.Status = %q, want %q — epic-classified task with structural split points must decompose despite the cross-project task_id collision", parentExec.Status, ExecStatusDecomposed)
	}

	// The decomposer must have split the task into multiple subtask execution
	// rows, all queued in the sandbox project — not swallowed by the
	// cross-project task_id collision.
	subtaskID := taskID + "-1"
	subExec, err := store.GetLatestExecutionByTaskIDForProject(subtaskID, sandboxProject)
	if err != nil {
		t.Fatalf("expected subtask %s queued in %s, got: %v", subtaskID, sandboxProject, err)
	}
	if subExec.ProjectPath != sandboxProject {
		t.Errorf("subtask ProjectPath = %q, want %q", subExec.ProjectPath, sandboxProject)
	}

	// The unrelated project's rows must be untouched by any of this.
	otherExec, err := store.GetExecution("other-completed")
	if err != nil {
		t.Fatalf("GetExecution(other-completed): %v", err)
	}
	if otherExec.Status != "completed" {
		t.Errorf("other-completed status mutated to %q", otherExec.Status)
	}
}
