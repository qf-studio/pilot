package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecute_SinglePackageCollapse_RefusedWhenChildLedgerNonTerminal is the
// GH-4663 regression test: a decomposed parent's retry re-plans as
// single-package scope (isSinglePackageScope would collapse to direct
// execution), but this exact task_id already has a recorded child that is
// non-terminal (decomposedChildLedgerNonTerminal, GH-4659). The collapse
// must be refused — the run must NOT fall through to a fresh direct
// implementation (which would race the still-running child, the
// GH-4648/GH-4649 duplicate-PR incident) and must instead flow into the
// multi-package/CreateSubIssues branch.
func TestExecute_SinglePackageCollapse_RefusedWhenChildLedgerNonTerminal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "should never run directly", Model: "claude"},
	}

	r := NewRunnerWithBackend(backend)
	r.SetRecordingEnabled(false)
	r.skipPreflightChecks = true
	r.dryRun = true // no real gh CLI needed: label/comment side effects are skipped in dry-run
	r.config = &BackendConfig{SkipSelfReview: true}
	r.SetLogStore(store)

	// Force CreateSubIssues down the ErrSubIssuesAlreadyExist recovery path
	// instead of shelling out to `gh issue create`.
	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}
	// The recovered sub-issues are all closed/done, so the recovery branch
	// resolves immediately as "already complete" without running anything.
	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		subs := gh4052SinglePackagePlan(nil).Subtasks
		return []CreatedIssue{
			{Number: 401, State: "closed", Subtask: subs[0]},
			{Number: 402, State: "closed", Subtask: subs[1]},
		}, nil
	}

	const taskID = "GH-4663-PARENT"
	execID := (&Task{ID: taskID}).LogExecutionID()
	if err := store.SaveExecution(&memory.Execution{
		ID: execID, TaskID: taskID, ProjectPath: "", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(parent): %v", err)
	}
	// Record a non-terminal decomposed child for this exact task_id — the
	// signal decomposedChildLedgerNonTerminal reads.
	if err := store.InsertExecutionEvent(execID, memory.StageDecomposed, "decomposed into 1 children: #9001"); err != nil {
		t.Fatalf("InsertExecutionEvent: %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-GH-9001", TaskID: "GH-9001", ProjectPath: "", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(child): %v", err)
	}

	task := &Task{
		ID:          taskID,
		Title:       "[epic] retry of a decomposed parent",
		Description: "Retry that re-plans as single-package scope while a child is still in flight",
		LocalMode:   true,
	}
	// The planner re-derives a single-package-scope plan on this retry —
	// the textbook collapse case isSinglePackageScope exists to handle.
	r.planEpicFn = func(_ context.Context, tsk *Task, _ string) (*EpicPlan, error) {
		return gh4052SinglePackagePlan(tsk), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := r.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s", result.Error)
	}

	// The single-package collapse must have been refused: the backend must
	// never be invoked directly for a fresh implementation.
	backend.mu.Lock()
	execCount := backend.execCount
	backend.mu.Unlock()
	if execCount != 0 {
		t.Errorf("backend.execCount = %d, want 0 — single-package collapse must be refused while a decomposed child is non-terminal, not fall through to direct re-implementation", execCount)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents() error: %v", err)
	}
	var sawCollapseRefused, sawCollapseTaken bool
	for _, e := range events {
		if e.Stage != memory.StageDecompositionSkipped {
			continue
		}
		if strings.Contains(e.Detail, "collapse refused") {
			sawCollapseRefused = true
			if !strings.Contains(e.Detail, "GH-9001") {
				t.Errorf("collapse-refused event detail = %q, want it to mention the non-terminal child", e.Detail)
			}
		}
		if strings.Contains(e.Detail, "reason=single_package_scope") {
			sawCollapseTaken = true
		}
	}
	if !sawCollapseRefused {
		t.Error("expected a decomposition_skipped event recording the single-package collapse was refused")
	}
	if sawCollapseTaken {
		t.Error("must not also record the normal single_package_scope skip event — the collapse itself must not run")
	}
}
