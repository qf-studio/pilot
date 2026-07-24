package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// gh4536TwoPackageSubtasks returns two subtasks in distinct directories (so
// isSinglePackageScope doesn't consolidate them into a single in-process
// task) — mirrors GH-4531's real shape: sub-issue 1 (GH-4532) and sub-issue 2
// (GH-4533).
func gh4536TwoPackageSubtasks() []PlannedSubtask {
	return []PlannedSubtask{
		{Order: 1, Title: "feat(gateway): add websocket handler", Description: "Implement upgrade handler in internal/gateway/server.go"},
		{Order: 2, Title: "feat(adapters): add telegram bot", Description: "Wire bot client in internal/adapters/telegram/bot.go"},
	}
}

// TestDispatcher_EpicSelfOwnedQueuedChild_TakesOverAndTerminates is the
// GH-4536 (TASK-419) end-to-end regression test: it drives an epic through
// the REAL Dispatcher -> ProjectWorker -> Runner.Execute path (not
// ExecuteSubIssues directly, which would bypass the very code — the epic
// branch's lack of a bounded context, and Runner.Execute's lack of any
// project-worker identity on ctx — that let the GH-4531 incident happen).
//
// Sub-issue 2's execution claim is pre-seeded as already held by an orphaned
// dispatch channel BEFORE the epic even starts, exactly mirroring GH-4531's
// shape: sub-issue 1 (GH-4532) ships normally, then sub-issue 2 (GH-4533)'s
// Begin() loses its claim to a channel that will never progress. Under the
// old code this polled forever with no ceiling on the queued phase, and
// since the ONLY goroutine that could ever run GH-4533 for this project is
// this very ProjectWorker (TASK-393's one-worker-per-project invariant), the
// parent execution row stayed "running" forever. This test asserts the
// parent instead reaches a terminal status within a bounded wait, and that
// both children actually executed — the second one via takeover.
func TestDispatcher_EpicSelfOwnedQueuedChild_TakesOverAndTerminates(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	projectPath := filepath.Join(os.TempDir(), "gh4536-deadlock-project")

	runner := NewRunner()
	runner.skipPreflightChecks = true
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 200 * time.Millisecond
	runner.childOutcomeQueuedAbsoluteCeiling = 2 * time.Second

	subtasks := gh4536TwoPackageSubtasks()
	runner.planEpicFn = func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
		return &EpicPlan{ParentTask: task, Subtasks: subtasks}, nil
	}
	// Force the recovery path instead of a real `gh issue create` — the
	// sub-issues are treated as already existing, matching GH-4531's shape
	// where the issues themselves were already open.
	runner.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}
	runner.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		return []CreatedIssue{
			{Number: 4532, State: "open", Subtask: subtasks[0]},
			{Number: 4533, State: "open", Subtask: subtasks[1]},
		}, nil
	}

	var executedIDs []string
	runner.executeFunc = func(_ context.Context, task *Task) (*ExecutionResult, error) {
		executedIDs = append(executedIDs, task.ID)
		// Non-zero tokens/files so the zero-delivery no_op guard doesn't
		// reclassify this otherwise-successful epic.
		return &ExecutionResult{TaskID: task.ID, Success: true, TokensOutput: 100, FilesChanged: 1}, nil
	}

	dispatcher := NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// GH-4531 incident setup: sub-issue 2 (GH-4533)'s claim is already held
	// by an orphaned dispatch channel (e.g. a duplicate poller pickup that
	// crashed) before the epic ever reaches it. ClaimExecution only touches
	// execution_claims, matching the real incident: the winning claim holder
	// never got as far as writing its own executions row.
	claimed, err := store.ClaimExecution("GH-4533", projectPath, 0, "exec-orphan-4533")
	if err != nil {
		t.Fatalf("failed to pre-seed orphan claim: %v", err)
	}
	if !claimed {
		t.Fatal("test setup: expected the seeded orphan claim to win")
	}

	epicTask := &Task{
		ID:          "GH-4536E",
		Title:       "[epic] GH-4536 self-owned queued child deadlock regression",
		Description: "epic",
		ProjectPath: projectPath,
		CreatePR:    true,
	}

	execID, err := dispatcher.QueueTask(context.Background(), epicTask)
	if err != nil {
		t.Fatalf("failed to queue epic task: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec, err := dispatcher.WaitForExecution(waitCtx, execID, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("epic parent did not reach a terminal state within the bounded wait — this is the GH-4531/GH-4536 deadlock regressing: %v", err)
	}
	if !isTerminalExecutionStatus(exec.Status) {
		t.Fatalf("expected epic parent execution to reach a terminal status, got %q", exec.Status)
	}
	if exec.Status != "completed" {
		t.Errorf("expected epic parent to complete successfully after takeover, got status %q (error: %s)", exec.Status, exec.Error)
	}

	if len(executedIDs) != 2 {
		t.Fatalf("expected 2 child executions (sub-issue 1 normally + sub-issue 2 via takeover), got %d: %v", len(executedIDs), executedIDs)
	}
	if executedIDs[0] != "GH-4532" || executedIDs[1] != "GH-4533" {
		t.Errorf("expected children executed in order [GH-4532 GH-4533], got %v", executedIDs)
	}
}

// TestDispatcher_WorkerPanic_HasLiveWorkerReflectsExitAndStaleSweepReaps is
// the GH-4536 (TASK-419) regression test for hasLiveWorker's delete-on-exit
// fix. Before this fix, d.workers only ever gained entries — a worker whose
// goroutine panicked mid-task stayed "live" in the map forever (until a
// daemon restart), permanently disabling the stale-sweep's live-worker skip
// guard for that project (recoverStaleQueuedTasks/recoverStaleRunningTasks
// both treat hasLiveWorker==true as "leave it alone, something is still
// making progress"). This drives a real panic through the real
// Dispatcher -> ProjectWorker -> Runner.Execute stack (via the planEpicFn
// test seam, the same injection point production wires through
// NewDispatcher) and asserts: (1) hasLiveWorker eventually reports false
// once the SafeGo-wrapped goroutine's cleanup defer runs during panic
// unwind, and (2) the stale sweep — which used to skip any project with a
// "live" worker regardless of reality — now reaps the row the panicked
// worker left behind.
func TestDispatcher_WorkerPanic_HasLiveWorkerReflectsExitAndStaleSweepReaps(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	projectPath := filepath.Join(os.TempDir(), "gh4536-panic-project")

	runner := NewRunner()
	runner.skipPreflightChecks = true
	runner.planEpicFn = func(context.Context, *Task, string) (*EpicPlan, error) {
		panic("GH-4536 (TASK-419) test: simulated worker panic")
	}

	config := &DispatcherConfig{
		StaleRunningThreshold: 0, // everything is stale immediately once no live worker guards it
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour, // won't tick during this test
	}
	dispatcher := NewDispatcher(store, runner, config)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-4536PANIC",
		Title:       "[epic] GH-4536 worker panic regression",
		Description: "epic",
		ProjectPath: projectPath,
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for dispatcher.hasLiveWorker(projectPath) {
		if time.Now().After(deadline) {
			t.Fatal("worker still considered live 5s after its goroutine panicked — hasLiveWorker is not reflecting real liveness (GH-4536/TASK-419)")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The panicked worker never returned to let processQueue finish the row,
	// so it should still be sitting "running".
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status != "running" {
		t.Fatalf("expected the panicked task's row to still be 'running' before the sweep, got %q", exec.Status)
	}

	dispatcher.recoverStaleTasks()

	reaped, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to get execution after sweep: %v", err)
	}
	if reaped.Status == "running" {
		t.Errorf("expected the stale sweep to reap the row left behind by the panicked worker now that hasLiveWorker correctly reports it dead, got %q", reaped.Status)
	}
}

// TestExecuteSubIssues_ClaimLost_QueuedBehindDifferentProjectWorkerNotFalselyTakenOver
// is the GH-4536 (TASK-419) non-regression test for GH-4413: a child queued
// behind OTHER work on a busy project whose live worker is a DIFFERENT
// project than the one recorded on ctx must NOT be mistaken for a
// self-owned deadlock. It mirrors
// TestExecuteSubIssues_ClaimLost_QueuedBeyondTimeoutStillSucceeds (the
// existing GH-4413 regression test) but additionally stamps ctx with a
// projectWorkerIdentity for a project that is NOT the child's own — proving
// the self-ownership check (workerProject == projectPath) correctly stays
// silent for the "different project, still legitimately busy" case, not
// just the "no identity at all" case the pre-existing test covers (that
// test calls context.Background() directly, which the real
// Runner.Execute/Dispatcher path never does after this fix).
func TestExecuteSubIssues_ClaimLost_QueuedBehindDifferentProjectWorkerNotFalselyTakenOver(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-4413" // must match makeSubIssues(1, 4413) below: taskID = fmt.Sprintf("GH-%d", issue.Number)
	const projectPath = "/busy/project"

	claimed, err := store.ClaimExecution(taskID, projectPath, 0, "exec-owner-4413")
	if err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}
	if !claimed {
		t.Fatal("test setup: expected the seeded claim to win")
	}

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-owner-4413",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "queued",
	}); err != nil {
		t.Fatalf("SaveExecution (queued row): %v", err)
	}

	issues := makeSubIssues(1, 4413)
	parent := &Task{ID: "GH-94", Title: "[epic] queued behind a different project's busy worker", ProjectPath: projectPath}

	execCalled := false
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		execCalled = true
		return &ExecutionResult{TaskID: task.ID, Success: true}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	const timeout = 1500 * time.Millisecond
	const queuedPhase = 2000 * time.Millisecond // > timeout: proves queue-wait isn't charged against it
	runner.childOutcomeReconcileTimeout = timeout

	takeoverCalled := false
	runner.setReclaimSelfOwnedQueuedChildFn(func(subTask *Task) (string, bool, error) {
		takeoverCalled = true
		return "", false, fmt.Errorf("takeover must never be attempted for a child queued on a different project than ctx's identity")
	})

	go func() {
		time.Sleep(queuedPhase)
		if uErr := store.UpdateExecutionStatus("exec-owner-4413", "running"); uErr != nil {
			t.Errorf("UpdateExecutionStatus(running): %v", uErr)
		}
		time.Sleep(100 * time.Millisecond) // well under timeout, running phase
		if mcErr := store.MarkExecutionCompleted("exec-owner-4413", "https://github.com/owner/repo/pull/9413", "sha-4413", 500); mcErr != nil {
			t.Errorf("MarkExecutionCompleted: %v", mcErr)
		}
	}()

	// ctx carries a DIFFERENT project's identity than the child's own — the
	// legitimate GH-2331/GH-4413 case: this Runner happens to be executing
	// on some OTHER project's ProjectWorker while this child sits queued
	// behind unrelated work on its own, still-live, still-progressing
	// project worker.
	ctx := withProjectWorkerIdentity(context.Background(), "/some/other/project")

	start := time.Now()
	err = runner.ExecuteSubIssues(ctx, parent, issues, parent.ProjectPath, "")
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ExecuteSubIssues returned error, want nil (a different project's identity must not trigger takeover or a false timeout): %v", err)
	}
	if takeoverCalled {
		t.Error("self-owned takeover fired for a child queued on a DIFFERENT project than ctx's identity — false positive (GH-4536/TASK-419 regression)")
	}
	if execCalled {
		t.Error("expected the backend NOT to be invoked when the execution claim was already lost")
	}
	if elapsed < queuedPhase {
		t.Errorf("elapsed = %s, want >= %s (queue-wait must still not be charged against the running timeout for a legitimately busy different-project worker)", elapsed, queuedPhase)
	}
}
