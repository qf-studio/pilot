package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// gh4619ForceStallReason is an alias for selfOwnedTakeoverForceStallReason
// (dispatcher.go) — the exact marker text reclaimSelfOwnedQueuedChild stamps
// on a force-stalled dead-end child claim. Referencing the production
// constant directly (rather than duplicating the literal) keeps these tests
// from silently drifting out of sync with the real exact-reason-match
// filtering findTerminalChildExecution/findChildExecutionState now rely on
// (GH-4619).
const gh4619ForceStallReason = selfOwnedTakeoverForceStallReason

// TestFindTerminalChildExecution_TakeoverBookkeepingRowDoesNotShadowRunningChild
// is the GH-4619 regression for defect A: a GH-4536 takeover force-stalls the
// dead-end original child row purely to release its claim generation
// (reclaimSelfOwnedQueuedChild, dispatcher.go), then begins its replacement
// execution directly in ExecStatusRunning. A caller with no execution row of
// its own to exclude (selfExecID="") — e.g. a re-entrant epic attempt whose
// own Begin() also lost the claim — must see "still running" instead of
// misreading the older force-stalled row as the child's terminal outcome.
// This is the exact incident shape: ui#21's epic re-entry read GH-26's
// force-stalled original row and failed the whole epic while GH-26's
// takeover execution was still live in the ledger.
func TestFindTerminalChildExecution_TakeoverBookkeepingRowDoesNotShadowRunningChild(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-26"
	const projectPath = "/repo/pilot-console-ui"

	oldRowCreated := time.Now().Add(-time.Hour)
	newRowCreated := time.Now()

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-original",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "stalled",
		Error:       gh4619ForceStallReason,
		CreatedAt:   oldRowCreated,
	}); err != nil {
		t.Fatalf("SaveExecution (force-stalled original): %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-takeover",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "running",
		CreatedAt:   newRowCreated,
	}); err != nil {
		t.Fatalf("SaveExecution (takeover replacement): %v", err)
	}

	runner := newTestRunnerWithExecFunc(nil)
	runner.logStore = store

	row, err := runner.findTerminalChildExecution(taskID, projectPath, "")
	if err != nil {
		t.Fatalf("findTerminalChildExecution returned error, want nil (still waiting): %v", err)
	}
	if row != nil {
		t.Fatalf("findTerminalChildExecution returned row %+v, want nil — the force-stalled original must not shadow the actively-running takeover", row)
	}

	terminal, running, _, err := runner.findChildExecutionState(taskID, projectPath, "")
	if err != nil {
		t.Fatalf("findChildExecutionState returned error, want nil: %v", err)
	}
	if terminal != nil {
		t.Fatalf("findChildExecutionState returned terminal row %+v, want nil — the force-stalled original must not shadow the actively-running takeover", terminal)
	}
	if !running {
		t.Error("findChildExecutionState running = false, want true (the takeover's replacement execution is actively running)")
	}
}

// TestFindTerminalChildExecution_GenuineTerminalFailureStillReported ensures
// the GH-4619 guard doesn't overcorrect: when the newest (and only) row for
// a child is a genuine terminal failure — not takeover bookkeeping — it must
// still be reported as terminal so the epic actually fails.
func TestFindTerminalChildExecution_GenuineTerminalFailureStillReported(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-27"
	const projectPath = "/repo/pilot-console-ui"

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-failed",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "failed",
		Error:       "panic: nil pointer dereference",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	runner := newTestRunnerWithExecFunc(nil)
	runner.logStore = store

	row, err := runner.findTerminalChildExecution(taskID, projectPath, "")
	if err != nil {
		t.Fatalf("findTerminalChildExecution: %v", err)
	}
	if row == nil {
		t.Fatal("findTerminalChildExecution returned nil, want the genuine terminal-failed row")
	}
	if row.ID != "exec-failed" {
		t.Errorf("returned row ID = %q, want %q", row.ID, "exec-failed")
	}

	terminal, running, _, err := runner.findChildExecutionState(taskID, projectPath, "")
	if err != nil {
		t.Fatalf("findChildExecutionState: %v", err)
	}
	if terminal == nil || terminal.ID != "exec-failed" {
		t.Errorf("findChildExecutionState terminal = %+v, want the genuine terminal-failed row", terminal)
	}
	if running {
		t.Error("findChildExecutionState running = true, want false for a genuinely terminal-failed child")
	}
}

// TestFindTerminalChildExecution_GH4381DuplicateQueuedRowRegression is the
// non-regression guard for GH-4381: a newer "queued" duplicate row (not a
// GH-4536 takeover — merely queued, not running) must NOT suppress an older
// genuine terminal outcome. Only an actively "running" newest row triggers
// the GH-4619 guard.
func TestFindTerminalChildExecution_GH4381DuplicateQueuedRowRegression(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-92"
	const projectPath = ""

	oldRowCreated := time.Now().Add(-time.Hour)
	newRowCreated := time.Now()

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-owner",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "no_op",
		CreatedAt:   oldRowCreated,
	}); err != nil {
		t.Fatalf("SaveExecution (terminal no_op row): %v", err)
	}
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-duplicate",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "queued",
		CreatedAt:   newRowCreated,
	}); err != nil {
		t.Fatalf("SaveExecution (fresh queued duplicate): %v", err)
	}

	runner := newTestRunnerWithExecFunc(nil)
	runner.logStore = store

	row, err := runner.findTerminalChildExecution(taskID, projectPath, "")
	if err != nil {
		t.Fatalf("findTerminalChildExecution: %v", err)
	}
	if row == nil || row.ID != "exec-owner" {
		t.Fatalf("findTerminalChildExecution = %+v, want the older genuine no_op row (GH-4381 semantics preserved)", row)
	}
}

// TestExecuteSubIssues_SelfOwnedTakeover_ChildCompletes_FinalizesTakeoverRow
// is the GH-4619 regression for defect B: reclaimSelfOwnedQueuedChild stamps
// its replacement execution's ID onto subTask.ExecutionID (via
// ExecutionLifecycle.Begin, the same *Task pointer threaded through), but
// executeSubIssuesTracked's finalizeSubIssueExecution calls used the
// loop-local subExecID captured before the claim was lost — "" on the
// ErrClaimLost branch — so the takeover row never finalized and relied on
// the 2h orphan-eviction sweep. This drives a real takeover through
// ExecuteSubIssues and asserts the takeover's OWN execution row (not the
// force-stalled original) reaches "completed" via the normal finalize path.
func TestExecuteSubIssues_SelfOwnedTakeover_ChildCompletes_FinalizesTakeoverRow(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-4619"
	const projectPath = "/repo/gh4619-takeover-completes"

	// Another dispatch channel already won the claim before the epic loop
	// reaches this sub-issue (mirrors the real incident: the poller
	// dispatched the same sub-issue independently).
	claimed, err := store.ClaimExecution(taskID, projectPath, 0, "exec-orphan")
	if err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}
	if !claimed {
		t.Fatal("test setup: expected the seeded orphan claim to win")
	}
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "queued",
	}); err != nil {
		t.Fatalf("SaveExecution (orphan queued row): %v", err)
	}

	issues := makeSubIssues(1, 4619)
	parent := &Task{ID: "GH-9000", Title: "[epic] GH-4619 takeover finalize regression", ProjectPath: projectPath}

	var newExecID string
	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true, PRUrl: "https://github.com/owner/repo/pull/9619", CommitSHA: "sha-4619"}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second
	runner.childOutcomeQueuedAbsoluteCeiling = 2 * time.Second

	// Fake takeover: mirrors Dispatcher.reclaimSelfOwnedQueuedChild's real
	// mechanics (force-stall the dead-end claim, then re-claim at a fresh
	// generation) closely enough to exercise the same subTask.ExecutionID
	// stamping this fix depends on.
	runner.setReclaimSelfOwnedQueuedChildFn(func(subTask *Task) (string, bool, error) {
		existing, lookupErr := store.GetLatestExecutionByTaskID(subTask.ID, subTask.ProjectPath)
		if lookupErr == nil && existing != nil {
			if _, casErr := store.UpdateExecutionStatusIfNotTerminal(existing.ID, "stalled", gh4619ForceStallReason); casErr != nil {
				return "", false, casErr
			}
		}
		execID, beginErr := NewExecutionLifecycle(store).Begin(subTask, ExecStatusRunning, 1)
		if beginErr != nil {
			return "", false, beginErr
		}
		newExecID = execID
		return execID, true, nil
	})

	ctx := withProjectWorkerIdentity(context.Background(), projectPath)

	if err := runner.ExecuteSubIssues(ctx, parent, issues, parent.ProjectPath, ""); err != nil {
		t.Fatalf("ExecuteSubIssues returned error, want nil (takeover child completed successfully): %v", err)
	}

	if newExecID == "" {
		t.Fatal("test setup: takeover never ran (newExecID never set)")
	}

	takeoverRow, err := store.GetExecution(newExecID)
	if err != nil {
		t.Fatalf("GetExecution(takeover row): %v", err)
	}
	if takeoverRow.Status != "completed" {
		t.Errorf("takeover execution row status = %q, want %q — finalizeSubIssueExecution must reach the takeover's own row, not no-op on an empty execID (GH-4619)", takeoverRow.Status, "completed")
	}
	if takeoverRow.PRUrl != "https://github.com/owner/repo/pull/9619" {
		t.Errorf("takeover execution row PRUrl = %q, want the finalized PR URL", takeoverRow.PRUrl)
	}

	origRow, err := store.GetExecution("exec-orphan")
	if err != nil {
		t.Fatalf("GetExecution(original row): %v", err)
	}
	if origRow.Status != "stalled" {
		t.Errorf("original force-stalled row status = %q, want it to remain %q (bookkeeping, untouched by finalize)", origRow.Status, "stalled")
	}
}

// TestExecuteSubIssues_SelfOwnedTakeover_ChildFails_FinalizesTakeoverRowAsFailed
// is TestExecuteSubIssues_SelfOwnedTakeover_ChildCompletes_FinalizesTakeoverRow's
// failure-path counterpart: when the takeover's own child execution fails,
// the takeover row must reach "failed" (not stay stuck "running" for the 2h
// orphan sweep to reap), and the epic must report the real failure.
func TestExecuteSubIssues_SelfOwnedTakeover_ChildFails_FinalizesTakeoverRowAsFailed(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-4620"
	const projectPath = "/repo/gh4619-takeover-fails"

	claimed, err := store.ClaimExecution(taskID, projectPath, 0, "exec-orphan")
	if err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}
	if !claimed {
		t.Fatal("test setup: expected the seeded orphan claim to win")
	}
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "queued",
	}); err != nil {
		t.Fatalf("SaveExecution (orphan queued row): %v", err)
	}

	issues := makeSubIssues(1, 4620)
	parent := &Task{ID: "GH-9001", Title: "[epic] GH-4619 takeover failure regression", ProjectPath: projectPath}

	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: false, Error: "compile error in generated code"}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second
	runner.childOutcomeQueuedAbsoluteCeiling = 2 * time.Second

	var newExecID string
	runner.setReclaimSelfOwnedQueuedChildFn(func(subTask *Task) (string, bool, error) {
		existing, lookupErr := store.GetLatestExecutionByTaskID(subTask.ID, subTask.ProjectPath)
		if lookupErr == nil && existing != nil {
			if _, casErr := store.UpdateExecutionStatusIfNotTerminal(existing.ID, "stalled", gh4619ForceStallReason); casErr != nil {
				return "", false, casErr
			}
		}
		execID, beginErr := NewExecutionLifecycle(store).Begin(subTask, ExecStatusRunning, 1)
		if beginErr != nil {
			return "", false, beginErr
		}
		newExecID = execID
		return execID, true, nil
	})

	ctx := withProjectWorkerIdentity(context.Background(), projectPath)

	err = runner.ExecuteSubIssues(ctx, parent, issues, parent.ProjectPath, "")
	if err == nil {
		t.Fatal("expected ExecuteSubIssues to report the takeover child's real failure, got nil")
	}
	if !strings.Contains(err.Error(), "compile error in generated code") {
		t.Errorf("error = %q, want it to surface the takeover child's real failure message", err.Error())
	}

	if newExecID == "" {
		t.Fatal("test setup: takeover never ran (newExecID never set)")
	}

	takeoverRow, gerr := store.GetExecution(newExecID)
	if gerr != nil {
		t.Fatalf("GetExecution(takeover row): %v", gerr)
	}
	if takeoverRow.Status != "failed" {
		t.Errorf("takeover execution row status = %q, want %q — finalizeSubIssueExecution must reach the takeover's own row (GH-4619)", takeoverRow.Status, "failed")
	}
}

// TestExecuteSubIssues_SelfOwnedTakeover_NeverFinalizes_ReproducesPreFixBug
// documents (and would fail without the GH-4619 fix) the exact pre-fix
// symptom: without re-deriving subExecID from subTask.ExecutionID after the
// claim-lost reconcile, the takeover's execution row is left "running"
// forever — this asserts the fixed behavior explicitly by checking the row
// is NOT left running once ExecuteSubIssues returns.
func TestExecuteSubIssues_SelfOwnedTakeover_NeverFinalizes_ReproducesPreFixBug(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-4621"
	const projectPath = "/repo/gh4619-takeover-not-orphaned"

	claimed, err := store.ClaimExecution(taskID, projectPath, 0, "exec-orphan")
	if err != nil {
		t.Fatalf("ClaimExecution: %v", err)
	}
	if !claimed {
		t.Fatal("test setup: expected the seeded orphan claim to win")
	}
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-orphan",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "queued",
	}); err != nil {
		t.Fatalf("SaveExecution (orphan queued row): %v", err)
	}

	issues := makeSubIssues(1, 4621)
	parent := &Task{ID: "GH-9002", Title: "[epic] GH-4619 orphan-sweep dependency regression", ProjectPath: projectPath}

	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true, PRUrl: "https://github.com/owner/repo/pull/9621", CommitSHA: "sha-4621"}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second
	runner.childOutcomeQueuedAbsoluteCeiling = 2 * time.Second

	var newExecID string
	runner.setReclaimSelfOwnedQueuedChildFn(func(subTask *Task) (string, bool, error) {
		execID, beginErr := NewExecutionLifecycle(store).Begin(subTask, ExecStatusRunning, 1)
		if beginErr != nil {
			return "", false, beginErr
		}
		newExecID = execID
		return execID, true, nil
	})

	ctx := withProjectWorkerIdentity(context.Background(), projectPath)

	if err := runner.ExecuteSubIssues(ctx, parent, issues, parent.ProjectPath, ""); err != nil {
		t.Fatalf("ExecuteSubIssues returned error, want nil: %v", err)
	}

	takeoverRow, err := store.GetExecution(newExecID)
	if err != nil {
		t.Fatalf("GetExecution(takeover row): %v", err)
	}
	if takeoverRow.Status == "running" {
		t.Errorf("takeover row left status=%q after ExecuteSubIssues returned — this is the pre-GH-4619-fix bug (only the 2h orphan sweep would ever finalize it)", takeoverRow.Status)
	}
}
