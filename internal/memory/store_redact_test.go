package memory

import (
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/secretpatterns"
)

func TestStore_RedactsRegisteredSecretValue(t *testing.T) {
	const value = "zq-opaque-config-value-0042"
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	secretpatterns.RegisterSecretValues(value)

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	t.Run("SaveLogEntry", func(t *testing.T) {
		entry := &LogEntry{ExecutionID: "exec-log", Timestamp: time.Now(), Level: "info", Message: "read api_key: " + value, Component: "test"}
		if err := store.SaveLogEntry(entry); err != nil {
			t.Fatalf("SaveLogEntry: %v", err)
		}
		logs, err := store.GetLogsByExecutionID("exec-log", 10)
		if err != nil || len(logs) != 1 {
			t.Fatalf("GetLogsByExecutionID: %v (%d rows)", err, len(logs))
		}
		if strings.Contains(logs[0].Message, value) || !strings.Contains(logs[0].Message, "<redacted>") {
			t.Errorf("stored message = %q", logs[0].Message)
		}
	})

	t.Run("SaveExecution", func(t *testing.T) {
		exec := &Execution{ID: "exec-save", TaskID: "GH-1", ProjectPath: "/p", Status: "failed", Output: "cat config: " + value, Error: "boom " + value}
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		got, err := store.GetExecution("exec-save")
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if strings.Contains(got.Output, value) || strings.Contains(got.Error, value) {
			t.Errorf("stored execution holds the value: output=%q error=%q", got.Output, got.Error)
		}
	})

	t.Run("UpdateExecutionStatus", func(t *testing.T) {
		exec := &Execution{ID: "exec-upd", TaskID: "GH-2", ProjectPath: "/p", Status: "running"}
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if err := store.UpdateExecutionStatus("exec-upd", "failed", "auth "+value); err != nil {
			t.Fatalf("UpdateExecutionStatus: %v", err)
		}
		got, err := store.GetExecution("exec-upd")
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if strings.Contains(got.Error, value) || !strings.Contains(got.Error, "<redacted>") {
			t.Errorf("stored error = %q", got.Error)
		}
	})
}
