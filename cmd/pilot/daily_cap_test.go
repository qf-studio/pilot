package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// capTestRunner returns a runner whose daily ticket cap is 1 (UTC) and a store
// that already holds one ticket begun just now, so the cap is reached for any
// other task id. GH-5808.
func capTestRunner(t *testing.T, projectPath string) (*executor.Runner, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-cap-seed", TaskID: "GH-5808-SEED", ProjectPath: projectPath,
		Status: "completed", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	runner, err := executor.NewRunnerWithConfig(&executor.BackendConfig{DailyTicketCap: 1, DailyTicketCapTimezone: "UTC"})
	if err != nil {
		t.Fatalf("NewRunnerWithConfig: %v", err)
	}
	return runner, store
}

// TestHandleIssueGeneric_DailyCapHoldIsGatedNotFailed: a task the daily cap
// holds comes back as a gated decline — no failure result, no repick-backoff
// growth, no execution row — so no adapter moves, labels or comments on it.
func TestHandleIssueGeneric_DailyCapHoldIsGatedNotFailed(t *testing.T) {
	taskID := "GH-5808-HELD"
	projectPath := "/tmp/pilot-gh-5808-held-does-not-exist"
	backoffKey := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(backoffKey) })

	runner, store := capTestRunner(t, projectPath)
	dispatcher := executor.NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: executor.NewMonitor(), ProjectPath: projectPath}
	info := IssueInfo{TaskID: taskID, Title: "t", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: taskID, Title: "t", Branch: "pilot/" + taskID, ProjectPath: projectPath}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("handleIssueGeneric err = %v, want nil (a hold is not a failure)", err)
	}
	if !errors.Is(hr.Error, executor.ErrDailyTicketCapReached) || !hr.IsDispatchGated() {
		t.Errorf("hr.Error = %v, want a gated daily-cap hold", hr.Error)
	}
	if hr.Success || !hr.EffectiveSuccess() {
		t.Errorf("Success=%v EffectiveSuccess=%v, want false/true (declined, not failed)", hr.Success, hr.EffectiveSuccess())
	}
	if gated, _ := repickBackoff.gateStatus(backoffKey); gated {
		t.Error("a held task grew the repick backoff window")
	}
	if exec, _ := store.GetLatestExecutionByTaskID(taskID, projectPath); exec != nil {
		t.Errorf("a held task wrote an execution row: %+v", exec)
	}
}

// TestTerminalCompletionChecker_DailyCapHold: the poller's candidacy gate skips
// a task the cap would refuse (so no label or comment is ever applied) and
// lets an already-counted retry through.
func TestTerminalCompletionChecker_DailyCapHold(t *testing.T) {
	projectPath := "/tmp/pilot-gh-5808-checker-does-not-exist"
	runner, store := capTestRunner(t, projectPath)

	held := terminalCompletionChecker{store: store, capGate: runner.DailyCapGate()}
	skip, reason, err := held.HasCompletedExecutionReason("GH-5808-NEW", projectPath)
	if err != nil || !skip || !strings.Contains(reason, "daily ticket cap") {
		t.Errorf("new ticket at cap = skip %v reason %q err %v, want skip with a daily-cap reason", skip, reason, err)
	}

	// The seed ticket already has a row today: a retry of it is not new.
	skip, reason, err = held.HasCompletedExecutionReason("GH-5808-SEED", projectPath)
	if strings.Contains(reason, "daily ticket cap") {
		t.Errorf("counted ticket reported as cap-held: skip %v reason %q err %v", skip, reason, err)
	}

	uncapped := terminalCompletionChecker{store: store}
	if skip, reason, err := uncapped.HasCompletedExecutionReason("GH-5808-NEW", projectPath); err != nil || skip {
		t.Errorf("checker without a cap gate = skip %v reason %q err %v, want no skip", skip, reason, err)
	}
}
