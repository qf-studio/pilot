package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
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
