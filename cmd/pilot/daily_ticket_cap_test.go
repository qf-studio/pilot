package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// setDailyTicketCapForTest installs a UTC daily ticket cap and clears it on
// cleanup (the cap is process state, so these tests must not run in parallel).
func setDailyTicketCapForTest(t *testing.T, limit int) {
	t.Helper()
	if err := executor.SetDailyTicketCap(limit, "UTC"); err != nil {
		t.Fatalf("SetDailyTicketCap: %v", err)
	}
	t.Cleanup(func() { _ = executor.SetDailyTicketCap(0, "") })
}

// TestHandleIssueGeneric_DailyTicketCapHold (GH-5808): a task held at the daily
// ticket cap is declined with the gated sentinel before any monitor, alert or
// dashboard side effect, reads as a non-failure to every adapter wrapper, and
// leaves no ledger row.
func TestHandleIssueGeneric_DailyTicketCapHold(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dispatcher := executor.NewDispatcher(store, executor.NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(dispatcher.Stop)

	projectPath := "/tmp/pilot-gh-5808-cap-does-not-exist"
	setDailyTicketCapForTest(t, 1)
	if _, err := executor.NewExecutionLifecycle(store).Begin(&executor.Task{ID: "GH-1", ProjectPath: projectPath}, executor.ExecStatusCompleted); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	monitor := executor.NewMonitor()
	deps := HandlerDeps{Dispatcher: dispatcher, Monitor: monitor, ProjectPath: projectPath}
	info := IssueInfo{TaskID: "GH-2", Title: "second ticket", Adapter: "github", LogMark: "▸"}
	task := &executor.Task{ID: "GH-2", Title: "second ticket", Branch: "pilot/GH-2", ProjectPath: projectPath}

	hr, err := handleIssueGeneric(context.Background(), deps, info, task)
	if err != nil {
		t.Fatalf("held task returned error %v, want nil", err)
	}
	if !errors.Is(hr.Error, executor.ErrDailyTicketCapReached) || !hr.IsDispatchGated() {
		t.Errorf("hr.Error = %v, want gated ErrDailyTicketCapReached", hr.Error)
	}
	if !hr.EffectiveSuccess() {
		t.Error("held task must read as a non-failure to adapter wrappers")
	}
	if _, ok := monitor.Get("GH-2"); ok {
		t.Error("held task was registered with the monitor")
	}
	if rows, lerr := store.ListExecutionsForTask("GH-2", projectPath); lerr != nil || len(rows) != 0 {
		t.Errorf("held task has %d ledger rows (err %v), want 0", len(rows), lerr)
	}
}

func TestClaimLostCLIError_DailyTicketCap(t *testing.T) {
	store := newCLITaskTestStore(t)
	setDailyTicketCapForTest(t, 1)

	if _, err := recordCLITaskStart(store, &executor.Task{ID: "TASK-1", ProjectPath: "/tmp/p"}); err != nil {
		t.Fatalf("first CLI task: %v", err)
	}
	held := &executor.Task{ID: "TASK-2", ProjectPath: "/tmp/p"}
	_, saveErr := recordCLITaskStart(store, held)
	if !errors.Is(saveErr, executor.ErrDailyTicketCapReached) {
		t.Fatalf("second CLI task: err = %v, want daily ticket cap hold", saveErr)
	}
	cliErr := claimLostCLIError(held.ID, saveErr)
	if cliErr == nil {
		t.Fatal("claimLostCLIError returned nil for a cap hold; the CLI would run the task anyway")
	}
	if msg := cliErr.Error(); !strings.Contains(msg, "daily ticket cap reached") || !strings.Contains(msg, "TASK-2") {
		t.Errorf("message %q should name the cap and the task", msg)
	}
}
