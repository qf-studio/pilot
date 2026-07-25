package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// newHandlerTestDispatcher creates a real dispatcher backed by a temporary
// on-disk store, matching production schema/migrations, for tests that need
// to exercise QueueTask/IsActive dedup behavior end-to-end (GH-4008).
func newHandlerTestDispatcher(t *testing.T) *executor.Dispatcher {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-test-handler-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	return dispatcher
}

// TestHandleIssueGeneric_BudgetExceeded verifies that handleIssueGeneric returns early
// when the budget enforcer is paused, without reaching the execution step.
func TestHandleIssueGeneric_BudgetExceeded(t *testing.T) {
	cfg := &budget.Config{Enabled: true}
	enforcer := budget.NewEnforcer(cfg, nil)
	enforcer.Pause("daily limit exceeded")

	monitor := executor.NewMonitor()

	deps := HandlerDeps{
		Monitor:  monitor,
		Enforcer: enforcer,
		// Runner and Dispatcher intentionally nil — must not be reached due to budget block
	}
	info := IssueInfo{
		TaskID:  "GH-999",
		Title:   "Test Issue",
		URL:     "https://github.com/test/repo/issues/999",
		Adapter: "github",
		LogMark: "▸",
	}
	task := &executor.Task{
		ID:     "GH-999",
		Title:  "Test Issue",
		Branch: "pilot/GH-999",
	}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)

	if err == nil {
		t.Fatal("expected error from budget enforcement, got nil")
	}
	if !strings.HasPrefix(err.Error(), "budget enforcement:") {
		t.Errorf("expected budget enforcement error, got: %v", err)
	}
	if hr.Success {
		t.Error("expected Success=false on budget exceeded")
	}
	if hr.BranchName != "pilot/GH-999" {
		t.Errorf("expected BranchName=pilot/GH-999, got %q", hr.BranchName)
	}
}

// TestHandleIssueGeneric_MonitorRegistration verifies that the monitor is populated
// with task state when handleIssueGeneric is called (budget exceeded path ensures
// monitor.Register is reached before the early return).
func TestHandleIssueGeneric_MonitorRegistration(t *testing.T) {
	cfg := &budget.Config{Enabled: true}
	enforcer := budget.NewEnforcer(cfg, nil)
	enforcer.Pause("test limit")

	monitor := executor.NewMonitor()

	deps := HandlerDeps{
		Monitor:  monitor,
		Enforcer: enforcer,
	}
	info := IssueInfo{
		TaskID:  "APP-123",
		Title:   "Linear task title",
		URL:     "https://linear.app/issue/APP-123",
		Adapter: "linear",
		LogMark: "▸",
	}
	task := &executor.Task{
		ID:     "APP-123",
		Title:  "Linear task title",
		Branch: "pilot/APP-123",
	}

	_, _ = handleIssueGeneric(context.Background(), deps, info, task)

	// Verify monitor.Register was called: the monitor should have the task state
	state, ok := monitor.Get("APP-123")
	if !ok || state == nil {
		t.Fatal("expected monitor to have task APP-123 registered, got nil")
	}
	if state.Title != "Linear task title" {
		t.Errorf("expected task title %q, got %q", "Linear task title", state.Title)
	}
}

// TestHandleIssueGeneric_AlreadyActive_SkipsDispatch verifies that when the
// dispatcher already has taskID queued/running, handleIssueGeneric returns
// early — nil error, Success=false, no monitor registration, no QueueTask
// attempt — instead of announcing a dispatch and then failing with
// "already queued or running" (GH-4008).
func TestHandleIssueGeneric_AlreadyActive_SkipsDispatch(t *testing.T) {
	dispatcher := newHandlerTestDispatcher(t)

	taskID := "GH-4008-ACTIVE"
	projectPath := "/tmp/pilot-gh-4008-does-not-exist"
	seedTask := &executor.Task{ID: taskID, Title: "seed", ProjectPath: projectPath}
	if _, err := dispatcher.QueueTask(context.Background(), seedTask); err != nil {
		t.Fatalf("failed to seed queued task: %v", err)
	}

	monitor := executor.NewMonitor()
	// GH-4276: IsActive is now project-scoped, so deps.ProjectPath must match
	// the seeded task's project for the pre-check to see it as active —
	// mirroring production, where deps.ProjectPath and task.ProjectPath are
	// always the same resolved project.
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor, ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "seed", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "seed", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("expected nil error for already-active task, got: %v", err)
	}
	if hr.Success {
		t.Error("expected Success=false for already-active task")
	}
	if _, ok := monitor.Get(taskID); ok {
		t.Error("expected monitor registration to be skipped for an already-active task")
	}
}

// TestHandleIssueGeneric_DecomposedTask_SkipsDispatchViaPrecheck is the
// GH-4540/TASK-421 regression test for the actual GH-4537 mechanism: a
// decomposed epic-parent's claim was invisible to IsActive() (IsTaskQueued's
// SQL allowlist was missing 'decomposed'), so handleIssueGeneric's early
// IsActive precheck (GH-4008) never caught it and every poll tick fell
// through to the claim-lost drop path further down. Seeding a
// decomposed-status execution and asserting the precheck alone gates the
// call — no monitor registration, no QueueTask reached — mirrors
// TestHandleIssueGeneric_AlreadyActive_SkipsDispatch for the decomposed case.
func TestHandleIssueGeneric_DecomposedTask_SkipsDispatchViaPrecheck(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	taskID := "GH-4537-DECOMPOSED-PRECHECK"
	projectPath := "/tmp/pilot-gh-4537-decomposed-does-not-exist"
	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	seedExecID, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(seedExecID, "decomposed"); err != nil {
		t.Fatalf("setup: failed to mark seed execution decomposed: %v", err)
	}

	monitor := executor.NewMonitor()
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor, ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "decomposed epic", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "decomposed epic", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("expected nil error for a decomposed task, got: %v", err)
	}
	if hr.Success {
		t.Error("expected Success=false for a decomposed task")
	}
	if !errors.Is(hr.Error, executor.ErrDispatchGated) {
		t.Errorf("expected hr.Error to wrap executor.ErrDispatchGated, got: %v", hr.Error)
	}
	if _, ok := monitor.Get(taskID); ok {
		t.Error("expected monitor registration to be skipped for a decomposed task — the IsActive precheck should have gated it")
	}
}

// TestHandleIssueGeneric_QueueTaskRace_DowngradesToDebug verifies that when
// QueueTask itself rejects a task as already-active — the TOCTOU race
// between the pre-check and the enqueue attempt — handleIssueGeneric still
// returns a nil error instead of propagating the rejection as a failure.
// info.TaskID and task.ID are deliberately different: the pre-check (keyed
// on info.TaskID) passes because that ID was never queued, but the actual
// QueueTask call (keyed on task.ID) hits the already-active task seeded
// below, deterministically reproducing the race window (GH-4008).
func TestHandleIssueGeneric_QueueTaskRace_DowngradesToDebug(t *testing.T) {
	dispatcher := newHandlerTestDispatcher(t)

	activeTaskID := "GH-4008-RACE-ACTUAL"
	projectPath := "/tmp/pilot-gh-4008-does-not-exist"
	seedTask := &executor.Task{ID: activeTaskID, Title: "seed", ProjectPath: projectPath}
	if _, err := dispatcher.QueueTask(context.Background(), seedTask); err != nil {
		t.Fatalf("failed to seed queued task: %v", err)
	}

	monitor := executor.NewMonitor()
	// GH-4276: QueueTask's dedup check is now project-scoped, so task.ProjectPath
	// must match the seeded task's project to reproduce the race.
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor, ProjectPath: projectPath}
	info := IssueInfo{TaskID: "GH-4008-RACE-PRECHECK", Title: "race", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: activeTaskID, Title: "race", Branch: "pilot/" + activeTaskID, ProjectPath: projectPath}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("expected nil error for race-path already-active rejection, got: %v", err)
	}
	if hr.Success {
		t.Error("expected Success=false for race-path already-active rejection")
	}
}

// TestHandleIssueGeneric_GenuineQueueFailure_StillErrors verifies that a
// non-dedup QueueTask failure still propagates as an error — GH-4008 only
// downgrades the specific "already active" dedup rejection, not real
// queueing failures.
func TestHandleIssueGeneric_GenuineQueueFailure_StillErrors(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-handler-fail-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Force a genuine (non-dedup) queueing failure by closing the store's
	// underlying DB out from under the dispatcher.
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	monitor := executor.NewMonitor()
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor}
	taskID := "GH-4008-FAIL"
	info := IssueInfo{TaskID: taskID, Title: "fail", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "fail", Branch: "pilot/" + taskID}

	_, err = handleIssueGeneric(context.Background(), deps, info, task)
	if err == nil {
		t.Fatal("expected error for genuine queue failure, got nil")
	}
	if errors.Is(err, executor.ErrTaskAlreadyActive) {
		t.Errorf("expected a genuine failure, not the already-active dedup rejection: %v", err)
	}
}

// TestHandleIssueGeneric_DroppedTerminalPickup_NoPhantomWaitError is the
// GH-4372 regression test for the poller-visible half of the bug: before the
// fix, QueueTask's silent-drop contract (nil error, empty execID) fell
// through to the WaitForExecution(ctx, "", ...) branch, which hit
// sql.ErrNoRows on its very first poll (an empty execID never matches a
// row) and surfaced as "failed to get execution: sql: no rows in result
// set" — an ERROR the SDK poller logged on every tick ("Failed to process
// issue ...") for a task that was never actually a failure.
//
// A no_op'd task at generation 0 reproduces the drop deterministically
// without needing a live owner (which the IsActive pre-check would catch
// before QueueTask is even reached) or a real backend execution (which a
// generation+1 retry would need to run to completion).
func TestHandleIssueGeneric_DroppedTerminalPickup_NoPhantomWaitError(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-handler-noop-drop-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	taskID := "GH-4372-NOOP-DROP"
	projectPath := "/tmp/pilot-gh-4372-noop-does-not-exist"

	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	seedExecID, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("setup: generation 0 Begin failed: %v", err)
	}
	if err := store.UpdateExecutionStatus(seedExecID, "no_op"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as no_op: %v", err)
	}

	monitor := executor.NewMonitor()
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor, ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "already no_op'd", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "already no_op'd", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	done := make(chan struct{})
	var hr *HandlerResult
	var hErr error
	go func() {
		hr, hErr = handleIssueGeneric(context.Background(), deps, info, task)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleIssueGeneric hung — likely stuck polling a phantom empty execID (GH-4372)")
	}

	if hErr != nil {
		t.Fatalf("expected nil error for a dropped duplicate/terminal pickup, got: %v (this reproduces GH-4372's poller ERROR log)", hErr)
	}
	if hr.Success {
		t.Error("expected Success=false for a dropped duplicate/terminal pickup")
	}
}

// TestHandleIssueGeneric_TerminalCompletion_SkipsDispatch is the GH-4376
// regression test for the storm evidenced on GH-91: a completed-but-open
// issue with terminal ledger evidence must be skipped at the shared handler
// chokepoint — no QueueTask attempt — independent of whatever the poller's
// own admission check decided.
func TestHandleIssueGeneric_TerminalCompletion_SkipsDispatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-handler-terminal-completion-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	taskID := "GH-4376-COMPLETED"
	projectPath := "/tmp/pilot-gh-4376-completed-does-not-exist"

	// Seed a genuine completed execution row (commit/PR deliverable) — the
	// same "done" signal GH-91 had (COMPLETED terminal execution, issue still
	// open, no status labels) when it was re-dispatched every ~30s poll cycle.
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-gh-4376-completed",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "completed",
		PRUrl:       "https://github.com/qf-studio/pilot-canary-sandbox/pull/91",
	}); err != nil {
		t.Fatalf("failed to seed completed execution: %v", err)
	}

	monitor := executor.NewMonitor()
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor, ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "already completed", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "already completed", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	hr, hErr := handleIssueGeneric(context.Background(), deps, info, task)
	if hErr != nil {
		t.Fatalf("expected nil error for a completed-but-open issue, got: %v", hErr)
	}
	if hr.Success {
		t.Error("expected Success=false — dispatch must be skipped for a task with terminal completion")
	}
	if _, ok := monitor.Get(taskID); ok {
		t.Error("expected monitor registration to be skipped — the terminal-completion gate runs before any side effects")
	}
}

// TestHandleIssueGeneric_RepickBackoff_ThrottlesRepeatedDrops is the GH-4376
// regression test for the storm's throughput symptom: repeatedly calling
// handleIssueGeneric for the same completed-but-open task_id/project_path —
// simulating the poller re-offering it on every ~30s tick — must dispatch at
// most once (the seeded completed row is caught every time), and the second
// call must be short-circuited by the backoff window rather than repeating
// the full HasTerminalCompletion check and its side effects.
func TestHandleIssueGeneric_RepickBackoff_ThrottlesRepeatedDrops(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-handler-repick-backoff-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	taskID := "GH-4376-STORM"
	projectPath := "/tmp/pilot-gh-4376-storm-does-not-exist"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-gh-4376-storm", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/qf-studio/pilot-canary-sandbox/pull/92",
	}); err != nil {
		t.Fatalf("failed to seed completed execution: %v", err)
	}

	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "storm", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "storm", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	// First call: caught by the HasTerminalCompletion gate, which arms the backoff.
	if _, err := handleIssueGeneric(context.Background(), deps, info, task); err != nil {
		t.Fatalf("first call: expected nil error, got: %v", err)
	}
	if repickBackoff.allow(backoffKey) {
		t.Fatal("expected the backoff window to be armed after the first drop")
	}

	// Second call (simulating the next ~30s poll tick): must be thrown out by
	// the backoff pre-check before it even re-evaluates HasTerminalCompletion.
	monitor2 := executor.NewMonitor()
	deps.Monitor = monitor2
	hr, hErr := handleIssueGeneric(context.Background(), deps, info, task)
	if hErr != nil {
		t.Fatalf("second call: expected nil error, got: %v", hErr)
	}
	if hr.Success {
		t.Error("second call: expected Success=false")
	}
	if _, ok := monitor2.Get(taskID); ok {
		t.Error("second call: expected monitor registration to be skipped by the backoff pre-check")
	}
}

// TestHandleIssueGeneric_RepickDoesNotClearBackoff is the GH-4394 subtask 2
// regression test for the actual GH-85 mechanism: before this fix, a
// terminal-claim re-pick (Dispatcher.beginWithGenerationRetry claiming
// generation > 0 because the prior claim failed but the task wasn't done)
// returned a valid, non-empty execID indistinguishable from a genuine fresh
// dispatch — so handleIssueGeneric's blanket repickBackoff.recordSuccess
// call wiped the backoff the re-pick had just armed, and the very next poll
// tick re-picked again with zero growth. This seeds a generation-0 FAILED
// (terminal, not done) execution so the single handleIssueGeneric call below
// exercises the re-pick path end-to-end, then asserts the backoff survives.
func TestHandleIssueGeneric_RepickDoesNotClearBackoff(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-handler-repick-arms-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	taskID := "GH-4394-REPICK-ARM"
	projectPath := "/tmp/pilot-gh-4394-repick-arm-does-not-exist"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	// Generation 0: a failed (terminal, not done) execution — exactly the
	// "prior claim was terminal but task is not done" precondition.
	seed := &executor.Task{ID: taskID, ProjectPath: projectPath}
	seedExecID, err := executor.NewExecutionLifecycle(store).Begin(seed, executor.ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("setup: generation 0 Begin failed: %v", err)
	}
	if err := store.UpdateExecutionStatus(seedExecID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "repick", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "repick", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	// The re-pick's own backoff bookkeeping (the assertion this test cares
	// about) happens synchronously right after QueueTask returns, before
	// WaitForExecution starts polling — a short-lived context lets
	// WaitForExecution bail out quickly via ctx.Done() instead of blocking on
	// a real backend execution against a project path that doesn't exist.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = handleIssueGeneric(ctx, deps, info, task)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleIssueGeneric hung waiting for the re-picked execution")
	}

	if repickBackoff.allow(backoffKey) {
		t.Fatal("expected the re-pick's backoff to survive handleIssueGeneric's success handling, but it was cleared")
	}

	consecutive, _, found, err := dispatcher.RepickBackoffState(backoffKey)
	if err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	}
	if !found || consecutive != 1 {
		t.Errorf("expected persisted repick backoff consecutive_drops=1 after the re-pick, got found=%v consecutive=%d", found, consecutive)
	}
}

// TestHandleIssueGeneric_GatedReturns_CarryErrDispatchGated is the GH-4469
// deliverable-2 regression test: every one of handleIssueGeneric's
// pre-dispatch admission gates must set HandlerResult.Error to
// executor.ErrDispatchGated (checkable via errors.Is), so anything that
// inspects the result can distinguish "the dispatcher intentionally declined
// this tick" from a genuine execution failure — even though the vendored
// github SDK poller itself doesn't consult this field (GH-4469's fix for that
// path is gating earlier, at terminalCompletionChecker).
func TestHandleIssueGeneric_GatedReturns_CarryErrDispatchGated(t *testing.T) {
	t.Run("IsActive dedup gate", func(t *testing.T) {
		dispatcher := newHandlerTestDispatcher(t)
		taskID := "GH-4469-ACTIVE"
		projectPath := "/tmp/pilot-gh-4469-active-does-not-exist"
		task := &executor.Task{ID: taskID, Title: "t", Branch: "pilot/" + taskID, ProjectPath: projectPath}

		// Queue the task once so IsActive reports true on the next check.
		if _, err := dispatcher.QueueTask(context.Background(), task); err != nil {
			t.Fatalf("setup QueueTask failed: %v", err)
		}

		deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
		info := IssueInfo{TaskID: taskID, Title: "t", Adapter: "github", LogMark: "▸"}

		hr, err := handleIssueGeneric(context.Background(), deps, info, task)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if !errors.Is(hr.Error, executor.ErrDispatchGated) {
			t.Errorf("expected hr.Error to wrap executor.ErrDispatchGated, got: %v", hr.Error)
		}
	})

	t.Run("repick backoff window gate", func(t *testing.T) {
		dispatcher := newHandlerTestDispatcher(t)
		taskID := "GH-4469-BACKOFF"
		projectPath := "/tmp/pilot-gh-4469-backoff-does-not-exist"
		backoffKey := repickBackoffKey(projectPath, taskID)
		t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })
		repickBackoff.setPersister(dispatcher)
		repickBackoff.recordDrop(backoffKey)

		deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
		info := IssueInfo{TaskID: taskID, Title: "t", Adapter: "github", LogMark: "▸"}
		task := &executor.Task{ID: taskID, Title: "t", Branch: "pilot/" + taskID, ProjectPath: projectPath}

		hr, err := handleIssueGeneric(context.Background(), deps, info, task)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if !errors.Is(hr.Error, executor.ErrDispatchGated) {
			t.Errorf("expected hr.Error to wrap executor.ErrDispatchGated, got: %v", hr.Error)
		}
	})

	t.Run("terminal completion re-check gate", func(t *testing.T) {
		taskID := "GH-4469-TERMINAL"
		projectPath := "/tmp/pilot-gh-4469-terminal-does-not-exist"
		backoffKey := repickBackoffKey(projectPath, taskID)
		t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

		store, err := memory.NewStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewStore failed: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		dispatcher2 := executor.NewDispatcher(store, executor.NewRunner(), nil)
		if err := dispatcher2.Start(context.Background()); err != nil {
			t.Fatalf("failed to start dispatcher: %v", err)
		}
		t.Cleanup(dispatcher2.Stop)
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-gh-4469-terminal", TaskID: taskID, ProjectPath: projectPath,
			Status: "completed", PRUrl: "https://github.com/qf-studio/pilot-canary-sandbox/pull/1",
		}); err != nil {
			t.Fatalf("failed to seed completed execution: %v", err)
		}

		deps := HandlerDeps{Dispatcher: dispatcher2, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
		info := IssueInfo{TaskID: taskID, Title: "t", Adapter: "github", LogMark: "▸"}
		task := &executor.Task{ID: taskID, Title: "t", Branch: "pilot/" + taskID, ProjectPath: projectPath}

		hr, hErr := handleIssueGeneric(context.Background(), deps, info, task)
		if hErr != nil {
			t.Fatalf("expected nil error, got: %v", hErr)
		}
		if !errors.Is(hr.Error, executor.ErrDispatchGated) {
			t.Errorf("expected hr.Error to wrap executor.ErrDispatchGated, got: %v", hr.Error)
		}
	})
}

// testAlertChannel is a minimal alerts.Channel implementation that records
// every alert it receives, for tests that need to observe what
// handleIssueGeneric's AlertsEngine actually dispatched.
type testAlertChannel struct {
	mu     sync.Mutex
	alerts []alerts.Alert
}

func (c *testAlertChannel) Name() string { return "test-channel" }
func (c *testAlertChannel) Type() string { return "webhook" }
func (c *testAlertChannel) Send(_ context.Context, alert *alerts.Alert) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerts = append(c.alerts, *alert)
	return nil
}
func (c *testAlertChannel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.alerts)
}

// waitForAlertCount polls until ch has recorded at least n alerts or the
// timeout elapses (alerts.Engine.ProcessEvent is asynchronous).
func waitForAlertCount(t *testing.T, ch *testAlertChannel, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ch.count() >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d alert(s), got %d", n, ch.count())
}

// TestHandleIssueGeneric_LoopBreakerAlert_FiresOnceAtThreshold is the GH-4469
// deliverable-4 regression test: repeatedly hitting the same gate (here, the
// terminal-completion re-check) must fire exactly one
// AlertTypeDispatchLoopBreaker WARNING when the consecutive-drop count first
// reaches repickLoopBreakerThreshold (10) — not before, and not again on
// every subsequent tick past it.
func TestHandleIssueGeneric_LoopBreakerAlert_FiresOnceAtThreshold(t *testing.T) {
	taskID := "GH-4469-LOOP-BREAKER"
	projectPath := "/tmp/pilot-gh-4469-loop-breaker-does-not-exist"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	d2 := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := d2.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(d2.Stop)
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-gh-4469-loop-breaker", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/qf-studio/pilot-canary-sandbox/pull/1",
	}); err != nil {
		t.Fatalf("failed to seed completed execution: %v", err)
	}

	config := &alerts.AlertConfig{
		Enabled: true,
		Channels: []alerts.ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []alerts.AlertRule{
			{
				Name:     "dispatch_loop_breaker",
				Type:     alerts.AlertTypeDispatchLoopBreaker,
				Enabled:  true,
				Severity: alerts.SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}
	testCh := &testAlertChannel{}
	alertDispatcher := alerts.NewDispatcher(config)
	alertDispatcher.RegisterChannel(testCh)
	engine := alerts.NewEngine(config, alerts.WithDispatcher(alertDispatcher))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatalf("failed to start alerts engine: %v", err)
	}

	deps := HandlerDeps{Dispatcher: d2, Monitor: executor.NewMonitor(), ProjectPath: projectPath, AlertsEngine: engine}
	info := IssueInfo{TaskID: taskID, Title: "loop breaker", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "loop breaker", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	forceExpire := func() {
		repickBackoff.mu.Lock()
		if e, ok := repickBackoff.entries[backoffKey]; ok {
			e.nextAllowedAt = time.Now().Add(-time.Second)
		}
		repickBackoff.mu.Unlock()
	}

	// Drive 9 drops (simulating 9 prior poll ticks each ~30s+ apart) — no
	// alert expected yet.
	for i := 0; i < 9; i++ {
		if i > 0 {
			forceExpire()
		}
		if _, err := handleIssueGeneric(context.Background(), deps, info, task); err != nil {
			t.Fatalf("drop %d: unexpected error: %v", i+1, err)
		}
	}
	if got := testCh.count(); got != 0 {
		t.Fatalf("expected 0 alerts before reaching the threshold, got %d", got)
	}

	// 10th consecutive drop: must fire exactly one alert.
	forceExpire()
	if _, err := handleIssueGeneric(context.Background(), deps, info, task); err != nil {
		t.Fatalf("10th drop: unexpected error: %v", err)
	}
	waitForAlertCount(t, testCh, 1, 2*time.Second)
	if got := testCh.count(); got != 1 {
		t.Fatalf("expected exactly 1 alert at the threshold, got %d", got)
	}

	// An 11th drop must not fire a second alert.
	forceExpire()
	if _, err := handleIssueGeneric(context.Background(), deps, info, task); err != nil {
		t.Fatalf("11th drop: unexpected error: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := testCh.count(); got != 1 {
		t.Fatalf("expected still exactly 1 alert past the threshold, got %d", got)
	}
}

// TestHandleIssueGeneric_TerminalCompletionStorm_NeverCountsTowardHardCap is
// the GH-4540/TASK-421 primary regression test for the main handler_common.go
// fix: before this fix, a completed-but-open issue re-admitted repeatedly by
// the poller (GH-91's mechanism) grew consecutive_drops via
// repickBackoff.recordDrop on every tick — the SAME persisted counter
// beginWithGenerationRetry gates dispatcherRepickHardCap (5) on — so a task
// that had already succeeded could still end up wedged/stalled purely from
// being redundantly re-offered. Driving the HasTerminalCompletion gate well
// past the hard cap (8 ticks) must leave consecutive_drops at 0/not-found
// (never touched) while claim_lost_drops grows to match every tick, proving
// the two counters are now fully decoupled.
func TestHandleIssueGeneric_TerminalCompletionStorm_NeverCountsTowardHardCap(t *testing.T) {
	taskID := "GH-4540-STORM-HARDCAP"
	projectPath := "/tmp/pilot-gh-4540-storm-hardcap-does-not-exist"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-gh-4540-storm-hardcap", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/qf-studio/pilot-canary-sandbox/pull/1",
	}); err != nil {
		t.Fatalf("failed to seed completed execution: %v", err)
	}

	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "storm", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "storm", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	forceExpire := func() {
		repickBackoff.mu.Lock()
		if e, ok := repickBackoff.entries[backoffKey]; ok {
			e.nextAllowedAt = time.Now().Add(-time.Second)
		}
		repickBackoff.mu.Unlock()
	}

	// Drive well past dispatcherRepickHardCap's ticks worth of re-admissions —
	// each one refused for the exact same "already terminal" reason.
	const ticks = 8
	for i := 0; i < ticks; i++ {
		if i > 0 {
			forceExpire()
		}
		hr, err := handleIssueGeneric(context.Background(), deps, info, task)
		if err != nil {
			t.Fatalf("tick %d: unexpected error: %v", i+1, err)
		}
		if hr.Success {
			t.Fatalf("tick %d: expected Success=false", i+1)
		}
		if !errors.Is(hr.Error, executor.ErrDispatchGated) {
			t.Fatalf("tick %d: expected hr.Error to wrap executor.ErrDispatchGated, got: %v", i+1, hr.Error)
		}
	}

	if consecutive, _, found, err := dispatcher.RepickBackoffState(backoffKey); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found && consecutive != 0 {
		t.Errorf("expected consecutive_drops to never grow from terminal-completion re-admissions, got found=%v consecutive=%d", found, consecutive)
	}

	claimLostDrops, found, err := dispatcher.ClaimLostDropCount(backoffKey)
	if err != nil {
		t.Fatalf("ClaimLostDropCount: %v", err)
	}
	if !found || claimLostDrops != ticks {
		t.Errorf("expected claim_lost_drops=%d after %d re-admissions, got found=%v count=%d", ticks, ticks, found, claimLostDrops)
	}
}

// TestAdapterSpecificPRNumberExtraction verifies that PR/MR number extraction
// uses the correct adapter-specific regex for each forge (GH-2293).
func TestAdapterSpecificPRNumberExtraction(t *testing.T) {
	tests := []struct {
		name     string
		adapter  string
		prURL    string
		wantNum  int
		wantFail bool
	}{
		{
			name:    "github PR URL",
			adapter: "github",
			prURL:   "https://github.com/org/repo/pull/42",
			wantNum: 42,
		},
		{
			name:    "gitlab MR URL",
			adapter: "gitlab",
			prURL:   "https://gitlab.com/namespace/project/-/merge_requests/17",
			wantNum: 17,
		},
		{
			name:    "gitlab MR URL without dash prefix",
			adapter: "gitlab",
			prURL:   "https://gitlab.example.com/group/repo/merge_requests/99",
			wantNum: 99,
		},
		{
			name:    "azuredevops PR URL",
			adapter: "azuredevops",
			prURL:   "https://dev.azure.com/org/project/_git/repo/pullrequest/55",
			wantNum: 55,
		},
		{
			name:     "github extractor does not match gitlab URL",
			adapter:  "github",
			prURL:    "https://gitlab.com/ns/proj/-/merge_requests/10",
			wantNum:  0,
			wantFail: true,
		},
		{
			name:     "gitlab extractor does not match github URL",
			adapter:  "gitlab",
			prURL:    "https://github.com/org/repo/pull/10",
			wantNum:  0,
			wantFail: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			var err error
			switch tc.adapter {
			case "gitlab":
				got, err = gitlab.ExtractMRNumber(tc.prURL)
			case "azuredevops":
				got, err = azuredevops.ExtractPRNumber(tc.prURL)
			default:
				got, err = github.ExtractPRNumber(tc.prURL)
			}

			if tc.wantFail {
				if err == nil {
					t.Errorf("expected extraction to fail for adapter=%s url=%s, got %d", tc.adapter, tc.prURL, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("extraction failed for adapter=%s url=%s: %v", tc.adapter, tc.prURL, err)
			}
			if got != tc.wantNum {
				t.Errorf("expected PR number %d, got %d (adapter=%s url=%s)", tc.wantNum, got, tc.adapter, tc.prURL)
			}
		})
	}
}

// TestExecFailureMsg_EmptyBody asserts that an empty exec.Error is replaced with a
// descriptive default, so no bare "execution failed:" comment body is produced.
func TestExecFailureMsg_EmptyBody(t *testing.T) {
	got := execFailureMsg("")
	if got == "" {
		t.Fatal("expected non-empty default message for empty exec error")
	}
	// Verify the full comment body would not be bare.
	full := "execution failed: " + got
	if strings.HasSuffix(full, ": ") {
		t.Errorf("bare failure comment produced for empty exec error: %q", full)
	}
}

// TestExecFailureMsg_NonEmptyPassthrough verifies that a non-empty error string is passed through unchanged.
func TestExecFailureMsg_NonEmptyPassthrough(t *testing.T) {
	in := "build failed: undefined reference to foo"
	if got := execFailureMsg(in); got != in {
		t.Errorf("expected passthrough %q, got %q", in, got)
	}
}

// TestHandleIssueGeneric_NilEnforcer verifies that nil enforcer skips budget check
// and proceeds. Because runner is also nil, it should fail at execution.
func TestHandleIssueGeneric_NilEnforcer(t *testing.T) {
	deps := HandlerDeps{
		Enforcer: nil,
		// Runner nil and Dispatcher nil — will panic at execution step
	}
	info := IssueInfo{
		TaskID:  "GH-1",
		Title:   "No enforcer",
		URL:     "https://github.com/org/repo/issues/1",
		Adapter: "github",
		LogMark: "▸",
	}
	task := &executor.Task{
		ID:     "GH-1",
		Title:  "No enforcer",
		Branch: "pilot/GH-1",
	}

	// With nil runner and nil dispatcher the function will panic at the execution step.
	// We recover to confirm execution was actually attempted (budget check was skipped).
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic from nil runner.Execute call, indicating budget check was skipped")
		}
	}()

	_, _ = handleIssueGeneric(context.Background(), deps, info, task)
}
