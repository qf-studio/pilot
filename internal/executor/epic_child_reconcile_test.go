package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecuteSubIssues_ChildTransientFailureEventualSuccess is the GH-3786
// (TASK-382 D3) regression test: GH-3760 failed with "sub-issue 3769 failed:
// unknown: exit status 1" while GH-3769's own execution row was still
// "running" — it went on to reach "completed" and ship its PR. The parent
// must not conclude the child failed while the child's execution row is
// non-terminal; it must reconcile against that row before failing the epic.
func TestExecuteSubIssues_ChildTransientFailureEventualSuccess(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-101"
	if err := store.SaveExecution(&memory.Execution{
		ID:     "exec-101",
		TaskID: taskID,
		Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	issues := makeSubIssues(1, 101)
	parent := &Task{ID: "GH-50", Title: "[epic] transient child failure"}

	// Simulates the inline sub-issue execution observing the early
	// inner-subtask exit-1 signal while a separately-tracked run of the same
	// issue is still in flight.
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: false,
			Error:   "unknown: exit status 1",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second

	var prCalls []subIssuePRCall
	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, commitSHA, branchName, issueNodeID string) {
		prCalls = append(prCalls, subIssuePRCall{
			PRNumber: prNumber, PRURL: prURL, IssueNumber: issueNumber, CommitSHA: commitSHA, BranchName: branchName,
		})
	})

	// The child's real, separately-tracked execution finishes shortly after
	// the inline call observed its transient failure signal.
	go func() {
		time.Sleep(60 * time.Millisecond)
		if mcErr := store.MarkExecutionCompleted("exec-101", "https://github.com/owner/repo/pull/9101", "sha-final", 1000); mcErr != nil {
			t.Errorf("MarkExecutionCompleted: %v", mcErr)
		}
	}()

	err = runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath, "")
	if err != nil {
		t.Fatalf("ExecuteSubIssues returned error, want nil (child eventually succeeded): %v", err)
	}

	if len(prCalls) != 1 {
		t.Fatalf("PR callback count = %d, want 1", len(prCalls))
	}
	if prCalls[0].PRNumber != 9101 {
		t.Errorf("PR callback PRNumber = %d, want 9101", prCalls[0].PRNumber)
	}
	if prCalls[0].CommitSHA != "sha-final" {
		t.Errorf("PR callback CommitSHA = %q, want %q", prCalls[0].CommitSHA, "sha-final")
	}
}

// TestExecuteSubIssues_ReconciledSuccessRetiresMonitorCard is the GH-4185
// regression test: executeWithOptions calls monitor.Start(taskID) for the
// child's own synchronous attempt (GH-3786 race) before that attempt fails
// and reconcileChildOutcome takes over. Because the reconciled-to-success
// path (resolveChildTerminalOutcome's "completed" case) previously returned
// straight to the epic loop without ever touching the Monitor, the child's
// dashboard card was left stuck at StatusRunning forever — the phantom
// "● running 100%" card — even though the child had actually finished. The
// fix must retire that Monitor entry itself, the same way a normal
// completion does (cmd/pilot/handler_common.go step 7: nil error → Complete).
func TestExecuteSubIssues_ReconciledSuccessRetiresMonitorCard(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-101"
	if err := store.SaveExecution(&memory.Execution{
		ID:     "exec-101",
		TaskID: taskID,
		Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	issues := makeSubIssues(1, 101)
	parent := &Task{ID: "GH-50", Title: "[epic] transient child failure"}

	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: false,
			Error:   "unknown: exit status 1",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second

	// Wire a real Monitor and simulate what executeWithOptions itself already
	// does for the in-flight synchronous attempt: register + start the
	// child's own card. This is the state left behind right before the
	// synchronous call fails and reconcileChildOutcome kicks in.
	monitor := NewMonitor()
	monitor.Register(taskID, "Sub-issue 1", "https://github.com/owner/repo/issues/101")
	monitor.Start(taskID)
	runner.monitor = monitor

	go func() {
		time.Sleep(60 * time.Millisecond)
		if mcErr := store.MarkExecutionCompleted("exec-101", "https://github.com/owner/repo/pull/9101", "sha-final", 1000); mcErr != nil {
			t.Errorf("MarkExecutionCompleted: %v", mcErr)
		}
	}()

	if err := runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath, ""); err != nil {
		t.Fatalf("ExecuteSubIssues returned error, want nil (child eventually succeeded): %v", err)
	}

	state, ok := monitor.Get(taskID)
	if !ok {
		t.Fatalf("monitor has no entry for %s after reconciled success", taskID)
	}
	if state.Status != StatusCompleted {
		t.Errorf("monitor status for %s = %q, want %q (phantom running card never retired)", taskID, state.Status, StatusCompleted)
	}
	if state.PRUrl != "https://github.com/owner/repo/pull/9101" {
		t.Errorf("monitor PRUrl for %s = %q, want the reconciled child's PR URL", taskID, state.PRUrl)
	}
}

// TestExecuteSubIssues_ChildFailureMessageSurfacesRealError verifies that
// when a child's execution row reaches a genuine terminal failure, the
// error the epic reports includes the row's real error message instead of
// the uninformative "unknown: exit status 1" backend classification.
func TestExecuteSubIssues_ChildFailureMessageSurfacesRealError(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const taskID = "GH-102"
	if err := store.SaveExecution(&memory.Execution{
		ID:     "exec-102",
		TaskID: taskID,
		Status: "failed",
		Error:  "panic: nil pointer dereference in health.go:42",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	issues := makeSubIssues(1, 102)
	parent := &Task{ID: "GH-51", Title: "[epic] real child failure"}

	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: false,
			Error:   "unknown: exit status 1",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	runner.logStore = store
	runner.childOutcomeReconcilePollInterval = 20 * time.Millisecond
	runner.childOutcomeReconcileTimeout = 2 * time.Second

	err = runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath, "")
	if err == nil {
		t.Fatal("expected error from genuinely failed child, got nil")
	}
	if !strings.Contains(err.Error(), "panic: nil pointer dereference in health.go:42") {
		t.Errorf("error = %q, want it to contain the child's real error message", err.Error())
	}
	if strings.Contains(err.Error(), "unknown: exit status 1") {
		t.Errorf("error = %q, should not surface the uninformative classification once the real error is known", err.Error())
	}
}

// TestExecuteSubIssues_NoLogStoreFailsImmediately verifies that without a
// log store wired, a synchronous child failure fails the epic immediately
// (unchanged pre-GH-3786 behavior) rather than blocking on a poll it has no
// way to resolve.
func TestExecuteSubIssues_NoLogStoreFailsImmediately(t *testing.T) {
	issues := makeSubIssues(1, 103)
	parent := &Task{ID: "GH-52", Title: "[epic] no log store"}

	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: false,
			Error:   "compile error",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)
	// logStore intentionally left nil.

	start := time.Now()
	err := runner.ExecuteSubIssues(context.Background(), parent, issues, parent.ProjectPath, "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "compile error") {
		t.Errorf("error = %q, want substring %q", err.Error(), "compile error")
	}
	if elapsed > time.Second {
		t.Errorf("ExecuteSubIssues took %s, want near-immediate failure with no log store wired", elapsed)
	}
}
