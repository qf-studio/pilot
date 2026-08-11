package main

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/config"
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

// TestHandleIssueGeneric_CanaryStampedViaProjectRepo is the GH-4833
// regression test for pilot-canary-sandbox rows landing is_canary=0: a
// projects[] entry with no explicit `path` (a perfectly normal config for a
// repo-only registration, e.g. a synthetic canary sandbox that doesn't need
// its own local checkout override) makes githubSDKPollerTargets fall back to
// the default adapter repo's project path — so the canary project and the
// default project resolve to the SAME deps.ProjectPath. Before this fix,
// handleIssueGeneric's deps.Cfg.GetProject(projectPath) then silently
// resolved to whichever project happened to match that shared path first,
// discarding the actual matched project's Canary flag. deps.ProjectRepo lets
// the caller pass its already-resolved "owner/repo" match through, sidestepping
// the ambiguous path entirely (mirroring config.FindProjectByRepo's use in
// ResolveProjectBoard).
func TestHandleIssueGeneric_CanaryStampedViaProjectRepo(t *testing.T) {
	sharedPath := "/tmp/pilot-gh-4833-shared-path-does-not-exist"
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{
				Name:   "pilot",
				Path:   sharedPath,
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot"},
				Canary: false,
			},
			{
				Name: "pilot-canary-sandbox",
				// No explicit Path — githubSDKPollerTargets falls back to the
				// default project's path for entries like this in production.
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot-canary-sandbox"},
				Canary: true,
			},
		},
	}

	tests := []struct {
		name        string
		projectRepo string
		wantCanary  bool
	}{
		{
			name:        "ProjectRepo resolves the canary project despite the shared path",
			projectRepo: "qf-studio/pilot-canary-sandbox",
			wantCanary:  true,
		},
		{
			name:        "ProjectRepo resolves the default (non-canary) project — no false positive",
			projectRepo: "qf-studio/pilot",
			wantCanary:  false,
		},
		{
			name:        "no ProjectRepo — falls back to the path lookup (existing behavior preserved)",
			projectRepo: "",
			wantCanary:  false, // GetProject(sharedPath) matches the first (non-canary) entry
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budgetCfg := &budget.Config{Enabled: true}
			enforcer := budget.NewEnforcer(budgetCfg, nil)
			enforcer.Pause("test: short-circuit before dispatch — canary stamping runs before this gate")

			deps := HandlerDeps{
				Cfg:         cfg,
				Monitor:     executor.NewMonitor(),
				Enforcer:    enforcer,
				ProjectPath: sharedPath,
				ProjectRepo: tt.projectRepo,
			}
			info := IssueInfo{TaskID: "GH-4833", Title: "canary probe", Adapter: "github", LogMark: "▸"}
			task := &executor.Task{ID: "GH-4833", Title: "canary probe", Branch: "pilot/GH-4833", ProjectPath: sharedPath}

			if _, err := handleIssueGeneric(context.Background(), deps, info, task); err == nil {
				t.Fatal("expected budget-exceeded error from the early return")
			}

			if task.IsCanary != tt.wantCanary {
				t.Errorf("task.IsCanary = %v, want %v", task.IsCanary, tt.wantCanary)
			}
		})
	}
}

// TestHandleIssueGeneric_CanaryStampedViaProjectRepo_PersistsThroughRealStore
// is the end-to-end companion to
// TestHandleIssueGeneric_CanaryStampedViaProjectRepo: drives the REAL
// dispatcher/store (not a mock) through handleIssueGeneric's QueueTask call,
// then asserts the persisted execution row's is_canary column via
// store.GetLatestExecutionByTaskID — closing the loop from GH-4833's
// production evidence (480 pilot-canary-sandbox rows with is_canary=0 in the
// 30d window) all the way to the actual write path.
func TestHandleIssueGeneric_CanaryStampedViaProjectRepo_PersistsThroughRealStore(t *testing.T) {
	sharedPath := "/tmp/pilot-gh-4833-e2e-shared-path-does-not-exist"
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{
				Name:   "pilot",
				Path:   sharedPath,
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot"},
				Canary: false,
			},
			{
				Name:   "pilot-canary-sandbox",
				GitHub: &config.ProjectGitHubConfig{Owner: "qf-studio", Repo: "pilot-canary-sandbox"},
				Canary: true,
			},
		},
	}

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

	taskID := "GH-4833-E2E"
	deps := HandlerDeps{
		Cfg:         cfg,
		Dispatcher:  dispatcher,
		Monitor:     executor.NewMonitor(),
		ProjectPath: sharedPath,
		ProjectRepo: "qf-studio/pilot-canary-sandbox",
	}
	info := IssueInfo{TaskID: taskID, Title: "canary probe e2e", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "canary probe e2e", Branch: "pilot/" + taskID, ProjectPath: sharedPath}

	// A short-lived context lets WaitForExecution bail out quickly via
	// ctx.Done() instead of blocking on a real backend execution against a
	// project path that doesn't exist — QueueTask (and the is_canary write)
	// already completed synchronously before WaitForExecution starts polling.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, _ = handleIssueGeneric(ctx, deps, info, task)

	exec, err := store.GetLatestExecutionByTaskID(taskID, sharedPath)
	if err != nil {
		t.Fatalf("GetLatestExecutionByTaskID failed: %v", err)
	}
	if !exec.IsCanary {
		t.Error("expected persisted execution row to have is_canary=1 for the canary-designated project, got false")
	}
}

// --- GH-4794: superseded/canceled executions must not be reported as
// failures — the poller's post-execution classification must treat the
// status vocabulary (executor.IsTerminalByDesignStatus) as the source of
// truth rather than inferring failure from "no PR produced" ---

// TestClassifyWaitedExecution_StatusVocabulary is the acceptance-criterion-1
// regression test: classifyWaitedExecution (the step-6 classification
// handleIssueGeneric applies to a terminal execution row) must distinguish
// superseded/canceled (terminal-by-design, not a failure) from a genuine
// "failed" status, and must leave "completed" behavior unchanged.
func TestClassifyWaitedExecution_StatusVocabulary(t *testing.T) {
	tests := []struct {
		name             string
		exec             *memory.Execution
		wantErr          bool
		wantTermByDesign bool
		wantSuccess      bool
	}{
		{
			name:             "superseded is terminal-by-design, not a failure",
			exec:             &memory.Execution{TaskID: "GH-1", Status: "superseded"},
			wantErr:          false,
			wantTermByDesign: true,
			wantSuccess:      false,
		},
		{
			name:             "canceled is terminal-by-design, not a failure",
			exec:             &memory.Execution{TaskID: "GH-1", Status: "canceled"},
			wantErr:          false,
			wantTermByDesign: true,
			wantSuccess:      false,
		},
		{
			name:             "failed is a genuine failure (regression guard)",
			exec:             &memory.Execution{TaskID: "GH-1", Status: "failed", Error: "boom"},
			wantErr:          true,
			wantTermByDesign: false,
		},
		{
			name:             "completed is unaffected",
			exec:             &memory.Execution{TaskID: "GH-1", Status: "completed", PRUrl: "https://github.com/org/repo/pull/1"},
			wantErr:          false,
			wantTermByDesign: false,
			wantSuccess:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err, terminalByDesign := classifyWaitedExecution(tt.exec.TaskID, tt.exec)

			if (err != nil) != tt.wantErr {
				t.Fatalf("classifyWaitedExecution() err = %v, wantErr %v", err, tt.wantErr)
			}
			if terminalByDesign != tt.wantTermByDesign {
				t.Errorf("terminalByDesign = %v, want %v", terminalByDesign, tt.wantTermByDesign)
			}
			if tt.wantErr {
				if result != nil {
					t.Errorf("expected nil result for a genuine failure, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatal("expected non-nil result for a non-failed status")
			}
			if result.Success != tt.wantSuccess {
				t.Errorf("result.Success = %v, want %v", result.Success, tt.wantSuccess)
			}
		})
	}
}

// TestClassifyResultAlert_TerminalByDesign_SuppressesFailureAlert is the
// acceptance-criterion-1/3 regression test for the alert side: a
// terminal-by-design result (superseded/canceled) must produce no alert at
// all — neither TaskFailed nor TaskCompleted — while a genuine failure
// (terminalByDesign=false) still produces TaskFailed unchanged, and a hard
// execErr always produces TaskFailed regardless of terminalByDesign.
func TestClassifyResultAlert_TerminalByDesign_SuppressesFailureAlert(t *testing.T) {
	t.Run("superseded result: no alert", func(t *testing.T) {
		result := &executor.ExecutionResult{TaskID: "GH-1", Success: false}
		ev := classifyResultAlert("GH-1", "t", "/proj", nil, result, true)
		if ev != nil {
			t.Errorf("expected no alert for a terminal-by-design (superseded) result, got %+v", ev)
		}
	})

	t.Run("canceled result: no alert", func(t *testing.T) {
		result := &executor.ExecutionResult{TaskID: "GH-1", Success: false}
		ev := classifyResultAlert("GH-1", "t", "/proj", nil, result, true)
		if ev != nil {
			t.Errorf("expected no alert for a terminal-by-design (canceled) result, got %+v", ev)
		}
	})

	t.Run("genuine failure result: TaskFailed alert unchanged", func(t *testing.T) {
		result := &executor.ExecutionResult{TaskID: "GH-1", Success: false, Error: "boom"}
		ev := classifyResultAlert("GH-1", "t", "/proj", nil, result, false)
		if ev == nil {
			t.Fatal("expected a TaskFailed alert for a genuine (non-terminal-by-design) failure")
		}
		if ev.Type != alerts.EventTypeTaskFailed {
			t.Errorf("expected EventTypeTaskFailed, got %v", ev.Type)
		}
		if ev.Error != "boom" {
			t.Errorf("expected alert error %q, got %q", "boom", ev.Error)
		}
	})

	t.Run("hard execErr: TaskFailed alert fires regardless of terminalByDesign", func(t *testing.T) {
		ev := classifyResultAlert("GH-1", "t", "/proj", errors.New("queue failure"), nil, true)
		if ev == nil {
			t.Fatal("expected a TaskFailed alert for a hard dispatch/wait error")
		}
		if ev.Type != alerts.EventTypeTaskFailed {
			t.Errorf("expected EventTypeTaskFailed, got %v", ev.Type)
		}
	})

	t.Run("completed result: TaskCompleted alert unchanged", func(t *testing.T) {
		result := &executor.ExecutionResult{TaskID: "GH-1", Success: true, PRUrl: "https://github.com/org/repo/pull/1"}
		ev := classifyResultAlert("GH-1", "t", "/proj", nil, result, false)
		if ev == nil {
			t.Fatal("expected a TaskCompleted alert for a genuine success")
		}
		if ev.Type != alerts.EventTypeTaskCompleted {
			t.Errorf("expected EventTypeTaskCompleted, got %v", ev.Type)
		}
	})
}

// TestHandlerResult_IsTerminalByDesign mirrors TestHandlerResult_IsDispatchGated
// (poller_github_test.go) for the new GH-4794 field/method pair.
func TestHandlerResult_IsTerminalByDesign(t *testing.T) {
	terminal := &HandlerResult{TerminalByDesign: true}
	if !terminal.IsTerminalByDesign() {
		t.Error("expected IsTerminalByDesign() = true when TerminalByDesign is set")
	}

	notTerminal := &HandlerResult{TerminalByDesign: false}
	if notTerminal.IsTerminalByDesign() {
		t.Error("expected IsTerminalByDesign() = false when TerminalByDesign is unset")
	}

	var nilHR *HandlerResult
	if nilHR.IsTerminalByDesign() {
		t.Error("expected IsTerminalByDesign() = false for a nil HandlerResult")
	}
}

// TestSDKTranslation_TerminalByDesign_DoesNotMislabelAsFailed drives
// HandlerResult.EffectiveSuccess() — the single shared formula all seven
// adapter handlers (github, gitlab, azuredevops, linear, jira, asana, plane)
// now use to build sdkcore.IssueResult.Success (GH-4801; previously
// linear/jira/asana/plane built it bare from hr.Success, and azuredevops
// omitted the GH-4587 IsDispatchGated() term) — against a manufactured
// terminal-by-design HandlerResult, mirroring
// TestGithubSDKTranslation_LiveClaimStillRunning_DoesNotMislabelAsFailed's
// approach of testing the translation formula directly since the real
// handlers can't be driven end-to-end without live network/token access.
// Success=false here would trip the vendored poller's "failed without PR,
// unmarking for retry" branch on a closed/canceled issue (GH-4794's actual
// incident).
func TestSDKTranslation_TerminalByDesign_DoesNotMislabelAsFailed(t *testing.T) {
	hr := &HandlerResult{Success: false, TerminalByDesign: true}
	if !hr.EffectiveSuccess() {
		t.Error("expected EffectiveSuccess() = true for a terminal-by-design result")
	}
}

// TestHandlerResult_EffectiveSuccess_Table covers the four cases the shared
// GH-4801 helper must classify: a plain successful execution, a GH-4587
// dispatch-gated admission decline, and a GH-4794 terminal-by-design
// (superseded/canceled) execution must all report EffectiveSuccess() ==
// true, while a genuine failure (Success=false, no gating/terminal reason)
// must report false — the case that legitimately should trip the vendored
// poller's "failed without PR, unmarking for retry" branch.
func TestHandlerResult_EffectiveSuccess_Table(t *testing.T) {
	tests := []struct {
		name string
		hr   *HandlerResult
		want bool
	}{
		{
			name: "plain success",
			hr:   &HandlerResult{Success: true},
			want: true,
		},
		{
			name: "dispatch-gated admission decline (GH-4587)",
			hr:   &HandlerResult{Success: false, Error: executor.ErrDispatchGated},
			want: true,
		},
		{
			name: "terminal-by-design: superseded/canceled (GH-4794)",
			hr:   &HandlerResult{Success: false, TerminalByDesign: true},
			want: true,
		},
		{
			name: "genuine failure",
			hr:   &HandlerResult{Success: false, Error: errors.New("execution failed: boom")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.hr.EffectiveSuccess(); got != tt.want {
				t.Errorf("EffectiveSuccess() = %v, want %v", got, tt.want)
			}
		})
	}
}

// fakeClosedIssueChecker reports every issue as closed with the
// pilot-superseded label — used to drive the REAL dispatcher pickup-time
// revalidation guard (GH-4656, dispatcher.go) into its supersede branch
// without a live GitHub call.
type fakeClosedIssueChecker struct{}

func (fakeClosedIssueChecker) GetIssueState(_ context.Context, _, _ string, _ int) (executor.IssueState, error) {
	return executor.IssueState{Closed: true, Labels: []string{"pilot-superseded"}}, nil
}

// TestHandleIssueGeneric_SupersededExecution_EndToEnd is the
// acceptance-criterion-3 end-to-end regression test for GH-4794: drives the
// REAL dispatcher (its actual GH-4656 pickup-time revalidation guard, not a
// stub) for an issue that's closed before pickup, through handleIssueGeneric,
// with a real AlertsEngine wired up. Confirms the whole pipeline —
// dispatcher -> classifyWaitedExecution -> classifyResultAlert ->
// HandlerResult.TerminalByDesign — agrees end to end: no TaskFailed alert
// fires, and the sdkcore.IssueResult.Success translation formula (verbatim
// from handlers.go) reports success, so the vendored poller's "failed
// without PR, unmarking for retry" branch never fires for this closed issue.
func TestHandleIssueGeneric_SupersededExecution_EndToEnd(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir, err := os.MkdirTemp("", "pilot-gh4794-superseded-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	runGit := func(args ...string) {
		t.Helper()
		cmd := osexec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@pilot.local")
	runGit("config", "user.name", "Pilot Test")
	runGit("remote", "add", "origin", "https://github.com/gh4794-org/gh4794-repo.git")
	if err := os.WriteFile(dir+"/README.md", []byte("# t\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "initial")

	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	runner := executor.NewRunner()
	runner.RegisterIssueStateChecker("github:gh4794-org/gh4794-repo", fakeClosedIssueChecker{})

	d := executor.NewDispatcher(store, runner, nil)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(d.Stop)

	config := &alerts.AlertConfig{
		Enabled: true,
		Channels: []alerts.ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []alerts.AlertRule{
			{
				Name:     "task_failed",
				Type:     alerts.AlertTypeTaskFailed,
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
		t.Fatalf("engine.Start: %v", err)
	}

	taskID := "GH-84001"
	deps := HandlerDeps{Dispatcher: d, Monitor: executor.NewMonitor(), ProjectPath: dir, AlertsEngine: engine}
	info := IssueInfo{TaskID: taskID, Title: "superseded e2e", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "superseded e2e", Branch: "pilot/" + taskID, ProjectPath: dir, CreatePR: true}

	hr, hErr := handleIssueGeneric(ctx, deps, info, task)
	if hErr != nil {
		t.Fatalf("expected nil error for a superseded execution, got: %v", hErr)
	}
	if !hr.IsTerminalByDesign() {
		t.Error("expected hr.IsTerminalByDesign() = true for an issue closed before pickup")
	}
	if hr.Error != nil {
		t.Errorf("expected nil hr.Error, got: %v", hr.Error)
	}

	// Give the async AlertsEngine.ProcessEvent a moment to settle before
	// asserting nothing was dispatched.
	time.Sleep(150 * time.Millisecond)
	if got := testCh.count(); got != 0 {
		t.Errorf("expected 0 alerts for a superseded execution (false operator alert, GH-4794), got %d", got)
	}

	// This is the exact formula every adapter handler in cmd/pilot/handlers.go
	// (github, gitlab, azuredevops, linear, jira, asana, plane) uses to build
	// the sdkcore.IssueResult handed back to the poller (GH-4801).
	issueResult := &sdkcore.IssueResult{
		Success: hr.EffectiveSuccess(),
	}
	if !issueResult.Success {
		t.Error("expected translated Success=true for a superseded execution — false would trip " +
			"the vendored poller's 'failed without PR, unmarking for retry' branch on a closed issue")
	}
}
