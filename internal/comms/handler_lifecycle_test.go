package comms

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

type noopChatBackend struct{}

func (b *noopChatBackend) Name() string      { return "noop-chat" }
func (b *noopChatBackend) IsAvailable() bool { return true }

func (b *noopChatBackend) Execute(_ context.Context, _ executor.ExecuteOptions) (*executor.BackendResult, error) {
	return &executor.BackendResult{Success: true, Output: "done"}, nil
}

func TestExecuteTask_CreatesAndFinalizesExecutionRow(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	runner := executor.NewRunnerWithBackend(&noopChatBackend{})
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)
	runner.SetLogStore(store)

	projectPath := t.TempDir()
	h := NewHandler(&HandlerConfig{
		Messenger:    &handlerMock{},
		Runner:       runner,
		Store:        store,
		ProjectPath:  projectPath,
		TaskIDPrefix: "TG",
	})

	h.executeTask(context.Background(), "chat1", "", "TG-1786832353", "check the upstream issue")

	execs, err := store.ListExecutionsForTask("TG-1786832353", projectPath)
	if err != nil {
		t.Fatalf("ListExecutionsForTask: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("executions rows = %d, want 1 — chat tasks must get a ledger row", len(execs))
	}
	exec := execs[0]
	if exec.Status == string(executor.ExecStatusRunning) || exec.Status == "queued" {
		t.Errorf("execution left non-terminal (%q); Finish must run after Execute", exec.Status)
	}

	if err := store.RecordExecutionEvent(exec.ID, memory.StageCommit, "event writes must work now"); err != nil {
		t.Errorf("RecordExecutionEvent against the chat task's row failed: %v", err)
	}
}
