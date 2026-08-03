package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// gh4677CountingBackend is a Backend stub whose Execute increments a counter.
// Used to prove a decomposed parent's retry never reaches direct
// implementation of its own scope (GH-4648/GH-4649, TASK-437 item A) — if
// the pre-planning ledger gate in executeWithOptions were bypassed, this
// backend would be invoked for the parent's own task.
type gh4677CountingBackend struct {
	mu        sync.Mutex
	execCount int
}

func (b *gh4677CountingBackend) Name() string      { return "gh4677-counting" }
func (b *gh4677CountingBackend) IsAvailable() bool { return true }
func (b *gh4677CountingBackend) Execute(_ context.Context, _ ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.execCount++
	return &BackendResult{Success: true}, nil
}

func (b *gh4677CountingBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.execCount
}

// gh4677SeedDecomposedParent writes the ledger state a decomposed parent
// leaves behind after decomposing into the given children: a parent
// executions row carrying a StageDecomposed event naming every child
// (Store.GetDecomposedChildTaskIDs' source), plus one executions row per
// child reflecting its terminal/non-terminal state. Mirrors the seeding
// pattern in TestFinalizeEpicBranchPR_AbortSweepsRunningChildren
// (epic_abort_sweep_test.go).
type gh4677ChildSpec struct {
	number int    // GitHub issue number -> childID "GH-<number>"
	status string // "completed", "failed", or "open" (still-running/no evidence)
	prURL  string // set for a genuinely completed child
}

func gh4677SeedDecomposedParent(t *testing.T, store *memory.Store, parentTask *Task, children []gh4677ChildSpec) {
	t.Helper()
	lifecycle := NewExecutionLifecycle(store)
	parentExecID, err := lifecycle.Begin(parentTask, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin(parent): %v", err)
	}
	parentTask.ExecutionID = parentExecID

	refs := make([]string, len(children))
	for i, c := range children {
		refs[i] = fmt.Sprintf("#%d", c.number)
	}
	detail := fmt.Sprintf("decomposed into %d children: %s", len(children), strings.Join(refs, ", "))
	if err := store.RecordExecutionEvent(parentExecID, memory.StageDecomposed, detail); err != nil {
		t.Fatalf("RecordExecutionEvent(StageDecomposed): %v", err)
	}

	for _, c := range children {
		childID := fmt.Sprintf("GH-%d", c.number)
		exec := &memory.Execution{
			ID:          fmt.Sprintf("exec-%d", c.number),
			TaskID:      childID,
			ProjectPath: parentTask.ProjectPath,
			Status:      c.status,
			PRUrl:       c.prURL,
		}
		if c.status == "failed" {
			exec.Error = "heartbeat timeout: exit 137"
		}
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution(%s): %v", childID, err)
		}
	}
}

// TestRunner_Execute_DecomposedParentRetryResumesCoordinator is the GH-4677
// regression test for the GH-4648/GH-4649 incident (TASK-437 prevention item
// A): a decomposed parent retried after one child failed must resume
// coordination — re-dispatching the failed child and waiting on the rest —
// never re-run classification/planning and re-implement the full spec
// itself. Table-driven per acceptance: the main incident shape plus one case
// per bypass branch the fix must pre-empt (planning-failure fallback,
// isSinglePackageScope collapse) — both must remain unreachable once
// children are on record, regardless of what planFn or complexity would
// have produced.
func TestRunner_Execute_DecomposedParentRetryResumesCoordinator(t *testing.T) {
	tests := []struct {
		name    string
		planFn  func(callCount *int) func(ctx context.Context, task *Task, dir string) (*EpicPlan, error)
		wantErr bool
	}{
		{
			// The incident shape: without this fix, GH-4648 gen-2's planFn ran
			// successfully and produced a fresh plan the parent then executed
			// directly, racing its own still-alive child (GH-4649). planFn
			// must never even be invoked once children are on record.
			name: "planning would succeed — bypass N/A, gate must still pre-empt",
			planFn: func(callCount *int) func(context.Context, *Task, string) (*EpicPlan, error) {
				return func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
					*callCount++
					return &EpicPlan{ParentTask: &Task{ID: task.ID}, Subtasks: []PlannedSubtask{
						{Order: 1, Title: "re-implement", Description: "would re-implement the whole spec"},
					}}, nil
				}
			},
		},
		{
			// Bypass branch 1 (runner.go's planning-failure fallback,
			// GH-1687): a planEpicFn error used to fall straight through to
			// direct execution, unconditionally. Must be unreachable here.
			name: "planning-failure fallback bypass must be unreachable",
			planFn: func(callCount *int) func(context.Context, *Task, string) (*EpicPlan, error) {
				return func(_ context.Context, _ *Task, _ string) (*EpicPlan, error) {
					*callCount++
					return nil, fmt.Errorf("epic planning backend unavailable")
				}
			},
		},
		{
			// Bypass branch 2 (runner.go's isSinglePackageScope collapse,
			// GH-1265/epic.go): a plan whose subtasks all target the same
			// package collapses to hasNoDecompose=true and executes directly.
			// Must be unreachable here.
			name: "isSinglePackageScope collapse bypass must be unreachable",
			planFn: func(callCount *int) func(context.Context, *Task, string) (*EpicPlan, error) {
				return func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
					*callCount++
					return &EpicPlan{ParentTask: &Task{ID: task.ID}, Subtasks: []PlannedSubtask{
						{Order: 1, Title: "impl", Description: "internal/executor/runner.go change"},
						{Order: 2, Title: "test", Description: "internal/executor/runner_test.go change"},
					}}, nil
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("memory.NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			projectPath := t.TempDir()
			task := &Task{
				ID:          "GH-4648",
				Title:       "[epic] duplicate-execution race repro",
				Description: "reproduces the GH-4648/GH-4649 decomposed-parent retry race",
				ProjectPath: projectPath,
			}

			// Child #4649 failed (heartbeat SIGKILL, the GH-4649 shape);
			// child #4650 completed cleanly.
			gh4677SeedDecomposedParent(t, store, task, []gh4677ChildSpec{
				{number: 4649, status: "failed"},
				{number: 4650, status: "completed", prURL: "https://github.com/owner/repo/pull/9650"},
			})

			backend := &gh4677CountingBackend{}
			r := NewRunnerWithBackend(backend)
			r.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
			r.skipPreflightChecks = true
			r.dryRun = true
			r.logStore = store

			planCallCount := 0
			r.planEpicFn = tt.planFn(&planCallCount)

			// recoverSubIssuesFn hydrates the ledger's child refs into
			// CreatedIssues: #4649 still open on GitHub (the failed child
			// never merged), #4650 closed (its PR merged).
			r.recoverSubIssuesFn = func(_ context.Context, _, parentID string) ([]CreatedIssue, error) {
				if parentID != task.ID {
					t.Errorf("recoverSubIssuesFn parentID = %q, want %q", parentID, task.ID)
				}
				return []CreatedIssue{
					{Number: 4649, Identifier: "4649", URL: "https://github.com/owner/repo/issues/4649", State: "open",
						Subtask: PlannedSubtask{Title: "impl", Description: "the failed child's original task description"}},
					{Number: 4650, Identifier: "4650", URL: "https://github.com/owner/repo/issues/4650", State: "closed",
						Subtask: PlannedSubtask{Title: "test", Description: "the completed child's original task description"}},
				}, nil
			}

			// executeFunc stands in for the sub-issue dispatch/re-dispatch
			// point (executeSubIssuesTracked) — the ONLY place that may be
			// invoked with a child task in this scenario.
			var executedChildIDs []string
			var mu sync.Mutex
			r.executeFunc = func(_ context.Context, subTask *Task) (*ExecutionResult, error) {
				mu.Lock()
				executedChildIDs = append(executedChildIDs, subTask.ID)
				mu.Unlock()
				return &ExecutionResult{TaskID: subTask.ID, Success: true, PRUrl: "https://github.com/owner/repo/pull/9649"}, nil
			}

			result, err := r.Execute(context.Background(), task)
			if err != nil {
				t.Fatalf("Execute returned unexpected error: %v", err)
			}
			if result == nil {
				t.Fatal("expected non-nil result")
			}
			if !result.IsEpic {
				t.Error("expected IsEpic=true — the parent must resolve via the epic coordinator path, not direct execution")
			}

			// (a) zero fresh planning invocations.
			if planCallCount != 0 {
				t.Errorf("planEpicFn call count = %d, want 0 (classification/planning must never run once children are on record)", planCallCount)
			}

			// (b) no direct-implementation path taken for the PARENT itself.
			if got := backend.count(); got != 0 {
				t.Errorf("backend.Execute call count = %d, want 0 (parent must never directly implement its own scope)", got)
			}

			// (c) the failed child was re-dispatched; the already-completed
			// child was not re-executed.
			mu.Lock()
			gotExecuted := append([]string(nil), executedChildIDs...)
			mu.Unlock()
			if len(gotExecuted) != 1 || gotExecuted[0] != "GH-4649" {
				t.Errorf("executed child IDs = %v, want [GH-4649] (only the failed child re-dispatched, the completed one skipped)", gotExecuted)
			}
		})
	}
}

// TestRunner_ResumeDecomposedParent_AllChildrenTerminalFinalizes covers
// acceptance criterion 3 at the unit level: resumeDecomposedParent itself
// (the coordinator-resume path decomposedChildLedgerNonTerminal routes
// into) still finalizes as already-complete when every recovered child
// evidences completion, reusing the same allChildrenDone check the
// existing ErrSubIssuesAlreadyExist recovery branch relies on. In
// production this exact scenario is intercepted earlier by the
// dispatcher-level decomposedChildrenAllComplete guard (dispatcher.go,
// covered by TestDecomposedChildrenAllComplete* in dispatcher_test.go)
// before Runner.Execute is ever invoked — decomposedChildLedgerNonTerminal
// deliberately only reports true when at least one child is NOT yet
// terminal, so resumeDecomposedParent is reached from executeWithOptions
// only in that non-terminal case. This test exercises
// resumeDecomposedParent directly to confirm its own all-terminal branch
// (unreachable from executeWithOptions today, but shared logic with the
// ErrSubIssuesAlreadyExist recovery branch) still behaves correctly rather
// than leaving it untested.
func TestRunner_ResumeDecomposedParent_AllChildrenTerminalFinalizes(t *testing.T) {
	projectPath := t.TempDir()
	task := &Task{
		ID:          "GH-4700",
		Title:       "[epic] all children already shipped",
		Description: "both children already completed",
		ProjectPath: projectPath,
	}

	backend := &gh4677CountingBackend{}
	r := NewRunnerWithBackend(backend)
	r.log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	r.dryRun = true

	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		return []CreatedIssue{
			{Number: 4701, Identifier: "4701", URL: "https://github.com/owner/repo/issues/4701", State: "closed"},
			{Number: 4702, Identifier: "4702", URL: "https://github.com/owner/repo/issues/4702", State: "closed"},
		}, nil
	}
	execCalled := false
	r.executeFunc = func(_ context.Context, subTask *Task) (*ExecutionResult, error) {
		execCalled = true
		return &ExecutionResult{TaskID: subTask.ID, Success: true}, nil
	}

	result, err := r.resumeDecomposedParent(context.Background(), task, projectPath, []string{"GH-4701", "GH-4702"}, time.Now())
	if err != nil {
		t.Fatalf("resumeDecomposedParent returned unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful result, got: %+v", result)
	}
	if !result.IsEpic {
		t.Error("expected IsEpic=true")
	}
	if execCalled {
		t.Error("executeFunc should NOT have been called — every child already terminal")
	}
	if backend.count() != 0 {
		t.Errorf("backend.Execute call count = %d, want 0", backend.count())
	}
}
