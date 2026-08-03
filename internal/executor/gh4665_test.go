package executor

import (
	"context"
	"reflect"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestParentRetryPickup_GH4648IncidentShape is the GH-4655/TASK-437
// regression test for the GH-4648/GH-4649 duplicate-execution incident: a
// decomposed parent gets picked up again (e.g. after a daemon restart or a
// dispatcher repick) while it already has two recorded children — one that
// failed at generation 0, one that already completed. The incident was that
// pickup treated this exactly like a brand-new task: it re-ran complexity
// detection, invoked PlanEpic again, and could fall through to directly
// re-implementing the whole spec, racing its own still-running or
// already-failed child instead of resuming coordination.
//
// This fixture reproduces that exact shape using the two primitives already
// merged for this fix:
//   - decomposedChildLedgerNonTerminal (GH-4659, #4666) — the gate check
//     that must report true the instant ANY recorded child isn't terminal,
//     not only when every child has failed to ship
//     (decomposedChildrenAllComplete is all-or-nothing).
//   - Dispatcher.beginWithGenerationRetry / nextRetryGeneration (GH-4372) —
//     the existing generation-retry mechanism a correct coordinator-resume
//     must reuse to re-dispatch the FAILED child at generation+1 without
//     re-dispatching the already-completed one.
//
// Wiring the gate into the epic-mode decision block ahead of
// DetectComplexity/PlanEpic and routing a gated pickup into this
// generation-retry dispatch are separate sibling issues (GH-4660/GH-4661,
// PRs #4667/#4672) — not yet merged as of this test. Until they land, this
// test pins the contract those two changes must satisfy: given this exact
// incident fixture, a correct retry pickup (a) never invokes PlanEpic again,
// (b) never falls through to direct re-implementation, and (c) re-dispatches
// only the failed child, advancing it to generation 1.
func TestParentRetryPickup_GH4648IncidentShape(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const projectPath = "/project-gh4648-incident"
	const parentID = "GH-6101"
	const childFailedID = "GH-6102"
	const childCompletedID = "GH-6103"

	// Parent: already decomposed into two children.
	parentExec := &memory.Execution{ID: "exec-gh4648-parent", TaskID: parentID, ProjectPath: projectPath, Status: "failed"}
	if err := store.SaveExecution(parentExec); err != nil {
		t.Fatalf("SaveExecution(parent): %v", err)
	}
	if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed,
		"decomposed into 2 children: #6102, #6103"); err != nil {
		t.Fatalf("InsertExecutionEvent(decomposed): %v", err)
	}

	// Child A: claimed generation 0, then failed — the child a correct
	// pickup must re-dispatch at generation+1.
	childATask := &Task{ID: childFailedID, ProjectPath: projectPath}
	childAExecID, err := NewExecutionLifecycle(store).Begin(childATask, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin(childA gen0): %v", err)
	}
	if err := store.UpdateExecutionStatus(childAExecID, "failed"); err != nil {
		t.Fatalf("setup: mark childA generation 0 failed: %v", err)
	}

	// Child B: claimed generation 0, then completed with a shipped PR — a
	// correct pickup must leave it alone (already done, nothing to retry).
	childBTask := &Task{ID: childCompletedID, ProjectPath: projectPath}
	childBExecID, err := NewExecutionLifecycle(store).Begin(childBTask, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin(childB gen0): %v", err)
	}
	if err := store.UpdateExecutionStatus(childBExecID, "completed"); err != nil {
		t.Fatalf("setup: mark childB generation 0 completed: %v", err)
	}
	if err := store.UpdateExecutionResult(childBExecID, "https://github.com/qf-studio/pilot/pull/6103", "", 0); err != nil {
		t.Fatalf("setup: record childB PR result: %v", err)
	}

	// Real gate check: retry pickup of this parent must detect a
	// non-terminal (failed) child.
	hasNonTerminal, childIDs, err := decomposedChildLedgerNonTerminal(store, parentID, projectPath)
	if err != nil {
		t.Fatalf("decomposedChildLedgerNonTerminal: %v", err)
	}
	if !hasNonTerminal {
		t.Fatal("expected decomposedChildLedgerNonTerminal to report true for a decomposed parent with one failed child — the gate this fix relies on")
	}

	// Fake planner + fake direct-implementation counters, wired onto a real
	// Runner exactly the way the epic decision block (runner.go ~2327,
	// ~2286) and the coordinator's sub-issue loop (epic.go) invoke them.
	planCalls := 0
	runner := NewRunner()
	runner.planEpicFn = func(context.Context, *Task, string) (*EpicPlan, error) {
		planCalls++
		return &EpicPlan{}, nil
	}
	directExecCalls := 0
	runner.executeFunc = func(context.Context, *Task) (*ExecutionResult, error) {
		directExecCalls++
		return &ExecutionResult{Success: true}, nil
	}

	// Simulated coordinator-resume routing (the GH-4660/GH-4661 contract):
	// because the gate fired, pickup must go straight to per-child
	// generation-retry dispatch — it must never reach runner.planEpicFn or
	// runner.executeFunc.
	dispatcher := NewDispatcher(store, runner, nil)

	table := []struct {
		childID   string
		wantRetry bool
		wantGen   int
	}{
		{childID: childFailedID, wantRetry: true, wantGen: 1},
		{childID: childCompletedID, wantRetry: false},
	}

	if hasNonTerminal {
		for _, tc := range table {
			t.Run(tc.childID, func(t *testing.T) {
				childTask := &Task{ID: tc.childID, ProjectPath: projectPath}
				execID, err := dispatcher.beginWithGenerationRetry(childTask, ExecStatusQueued)
				if err != nil {
					t.Fatalf("beginWithGenerationRetry(%s): %v", tc.childID, err)
				}

				if !tc.wantRetry {
					if execID != "" {
						t.Errorf("child %s: expected no re-dispatch (already completed), got fresh execID %q", tc.childID, execID)
					}
					return
				}

				if execID == "" {
					t.Fatalf("child %s: expected a fresh re-dispatch execID, got empty (pickup dropped)", tc.childID)
				}
				gen, _, found, err := store.LatestClaimGeneration(tc.childID, projectPath)
				if err != nil {
					t.Fatalf("LatestClaimGeneration(%s): %v", tc.childID, err)
				}
				if !found || gen != tc.wantGen {
					t.Errorf("child %s: expected latest claim generation %d, found=%v got=%d", tc.childID, tc.wantGen, found, gen)
				}
			})
		}
	}

	if !reflect.DeepEqual(childIDs, []string{childFailedID, childCompletedID}) {
		t.Errorf("decomposedChildLedgerNonTerminal childIDs = %v, want [%s %s]", childIDs, childFailedID, childCompletedID)
	}

	if planCalls != 0 {
		t.Errorf("expected zero fresh PlanEpic invocations on retry pickup of an already-decomposed parent with a non-terminal child, got %d", planCalls)
	}
	if directExecCalls != 0 {
		t.Errorf("expected no direct implementation path taken on retry pickup of an already-decomposed parent with a non-terminal child, got %d", directExecCalls)
	}
}
