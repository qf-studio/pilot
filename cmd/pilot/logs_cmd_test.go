package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// newLogsTestStore creates a real, file-backed memory.Store for logs command tests.
func newLogsTestStore(t *testing.T) *memory.Store {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-test-logs-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return store
}

// TestRunTaskLogs_UUIDKeyedExecution pins the GH-4083 regression: since GH-3764-2,
// execution_logs.execution_id is written via Task.LogExecutionID(), which prefers the
// dispatcher-assigned executions.id UUID over the human-readable task ID. Before the
// fix, runTaskLogs (formerly showTaskLogs) queried GetLogsByExecutionID(exec.TaskID, ...)
// and always got zero rows for dispatcher-executed tasks whose logs are keyed by exec.ID.
func TestRunTaskLogs_UUIDKeyedExecution(t *testing.T) {
	store := newLogsTestStore(t)

	const (
		execUUID = "exec-uuid-1234"
		taskID   = "GH-42"
	)

	if err := store.SaveExecution(&memory.Execution{
		ID:          execUUID,
		TaskID:      taskID,
		ProjectPath: "/project",
		Status:      "completed",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	base := time.Now()
	messages := []string{"starting", "implementing", "done"}
	for i, msg := range messages {
		if err := store.SaveLogEntry(&memory.LogEntry{
			ExecutionID: execUUID,
			Timestamp:   base.Add(time.Duration(i) * time.Second),
			Level:       "info",
			Message:     msg,
			Component:   "executor",
		}); err != nil {
			t.Fatalf("SaveLogEntry failed: %v", err)
		}
	}

	var buf bytes.Buffer
	if err := runTaskLogs(&buf, store, taskID, false, false); err != nil {
		t.Fatalf("runTaskLogs failed: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "No log entries recorded for this task.") {
		t.Fatalf("runTaskLogs reported no log entries; UUID-keyed logs were not found:\n%s", out)
	}
	for _, msg := range messages {
		if !strings.Contains(out, msg) {
			t.Errorf("expected output to contain log message %q, got:\n%s", msg, out)
		}
	}
}

// TestRunTaskLogs_TaskIDFallback covers executions that never got a dispatcher-assigned
// UUID (e.g. the direct-commit path or pre-GH-3764-2 rows), where execution_id falls back
// to the human-readable task ID and exec.ID == exec.TaskID.
func TestRunTaskLogs_TaskIDFallback(t *testing.T) {
	store := newLogsTestStore(t)

	const taskID = "GH-99"

	if err := store.SaveExecution(&memory.Execution{
		ID:          taskID,
		TaskID:      taskID,
		ProjectPath: "/project",
		Status:      "completed",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	if err := store.SaveLogEntry(&memory.LogEntry{
		ExecutionID: taskID,
		Timestamp:   time.Now(),
		Level:       "info",
		Message:     "task started",
		Component:   "executor",
	}); err != nil {
		t.Fatalf("SaveLogEntry failed: %v", err)
	}

	var buf bytes.Buffer
	if err := runTaskLogs(&buf, store, taskID, false, false); err != nil {
		t.Fatalf("runTaskLogs failed: %v", err)
	}

	if !strings.Contains(buf.String(), "task started") {
		t.Errorf("expected fallback lookup by task ID to find the log entry, got:\n%s", buf.String())
	}
}
