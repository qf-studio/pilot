package executor

import (
	"context"
	"testing"
)

// GH-4649 (sub-issue of GH-4648): regression coverage for the AC #2 (a/b/c)
// invariant that Task.IsCanary — resolved upstream from ProjectConfig.Canary
// at whichever fresh-intake site owns the task (outside internal/executor;
// see this test file's package-level doc note below) — actually survives the
// executor's own write chokepoints once it reaches them.
//
// Audit note (recorded here per GH-4649's PR-body requirement): every
// Task{...} literal inside internal/executor is a PROPAGATION site, not a
// fresh-intake one — there is no place in this package that originates a
// brand-new Task from a GitHub/Linear/etc issue, so there is nothing to wire
// here. The three literals and their classification:
//   - epic.go:2849 (executeSubIssuesTracked's subTask) — propagation,
//     inherits parent.IsCanary. Already correct; covered by (b) below.
//   - dispatcher.go:2913 (buildTaskFromExecution) — propagation, restores
//     exec.IsCanary after a queue round-trip. Already correct; covered by
//     the pre-existing TestBuildTaskFromExecution_ThreadsExecutionUUID.
//   - decompose.go:426 (createSubtasks) — propagation site named explicitly
//     out-of-scope by GH-4649 ("leave decompose.go:~430 alone"); left
//     untouched here to avoid conflicting with GH-4648's own PR.
// The two "known fresh-build" line refs from the parent issue
// (dispatcher.go:~2426, ~2301) do not correspond to Task{} construction at
// all on this branch — 2426 is WorkerStatus{} (ProjectWorker.Status) and
// 2301 is a synthesized memory.Execution{} for the decomposed-parent guard.
// Neither builds a Task. No code change follows from this audit.

// TestExecutionLifecycle_Begin_CanaryTask_PersistsIsCanaryTrue covers AC #2(a):
// a task built for a canary-configured project must produce an execution row
// with is_canary=1 through the ExecutionLifecycle.Begin chokepoint.
func TestExecutionLifecycle_Begin_CanaryTask_PersistsIsCanaryTrue(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{
		ID:          "GH-4649-a",
		Title:       "canary task",
		ProjectPath: "/tmp/canary-project",
		IsCanary:    true,
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load saved execution: %v", err)
	}
	if !exec.IsCanary {
		t.Error("expected exec.IsCanary = true for a canary-configured task, got false")
	}
}

// TestExecutionLifecycle_Begin_NonCanaryTask_PersistsIsCanaryFalse covers AC
// #2(c): a task built for a non-canary (ordinary) project must land
// is_canary=0 — no false positives leaking onto regular executions.
func TestExecutionLifecycle_Begin_NonCanaryTask_PersistsIsCanaryFalse(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{
		ID:          "GH-4649-c",
		Title:       "ordinary task",
		ProjectPath: "/tmp/regular-project",
		IsCanary:    false,
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load saved execution: %v", err)
	}
	if exec.IsCanary {
		t.Error("expected exec.IsCanary = false for a non-canary task, got true")
	}
}

// TestExecuteSubIssues_CanaryParent_ChildInheritsIsCanary covers AC #2(b): a
// decomposed (epic sub-issue) child of a canary parent must inherit
// parent.IsCanary through executeSubIssuesTracked's subTask construction
// (epic.go:2849) and carry it all the way to the child's own executions row.
func TestExecuteSubIssues_CanaryParent_ChildInheritsIsCanary(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	issues := makeSubIssues(1, 4649)
	parent := &Task{
		ID:          "GH-4648",
		Title:       "[epic] canary parent",
		ProjectPath: "/project-4649",
		IsCanary:    true,
	}

	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
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
	if len(childStates) != 1 || childStates[0] != "completed" {
		t.Fatalf("expected 1 completed child state, got %v", childStates)
	}

	row, err := store.GetLatestExecutionByTaskID("GH-4649", parent.ProjectPath)
	if err != nil {
		t.Fatalf("GetLatestExecutionByTaskID: %v", err)
	}
	if !row.IsCanary {
		t.Error("expected decomposed child's execution row to inherit parent.IsCanary=true, got false")
	}
}
