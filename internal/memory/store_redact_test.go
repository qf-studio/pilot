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

func TestStore_RedactsExecutionEventsRearmAndChatTurns(t *testing.T) {
	const value = "zq-opaque-config-value-0042"
	secretpatterns.ResetSecretValuesForTest()
	t.Cleanup(secretpatterns.ResetSecretValuesForTest)
	secretpatterns.RegisterSecretValues(value)

	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	assertClean := func(t *testing.T, field, got string) {
		t.Helper()
		if strings.Contains(got, value) || !strings.Contains(got, "<redacted>") {
			t.Errorf("%s = %q", field, got)
		}
	}
	save := func(t *testing.T, id, taskID, status string) {
		t.Helper()
		if err := store.SaveExecution(&Execution{ID: id, TaskID: taskID, ProjectPath: "/p", Status: status}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
	}

	t.Run("execution_events.detail", func(t *testing.T) {
		save(t, "ev-1", "GH-10", "running")
		if err := store.InsertExecutionEvent("ev-1", StageFailed, "boom "+value); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordExecutionEvent("ev-1", StageMerged, "merged "+value); err != nil {
			t.Fatal(err)
		}
		events, err := store.ListExecutionEvents("ev-1")
		if err != nil || len(events) != 2 {
			t.Fatalf("ListExecutionEvents: %v (%d)", err, len(events))
		}
		for _, e := range events {
			assertClean(t, "detail", e.Detail)
		}
	})

	t.Run("ResolveOrphanedRunningExecution event", func(t *testing.T) {
		save(t, "orph-1", "GH-11", "running")
		if err := store.ResolveOrphanedRunningExecution("orph-1", "https://example.test/pr?x="+value); err != nil {
			t.Fatal(err)
		}
		events, err := store.ListExecutionEvents("orph-1")
		if err != nil || len(events) == 0 {
			t.Fatalf("ListExecutionEvents: %v (%d)", err, len(events))
		}
		assertClean(t, "detail", events[0].Detail)
	})

	rearm := []struct {
		name, status string
		fn           func(taskID, project, reason string) error
	}{
		{"canceled", "canceled", store.ReclassifyCanceledForRearm},
		{"stalled", "stalled", store.ReclassifyStalledForRearm},
		{"superseded", "superseded", store.ReclassifySupersededForRearm},
		{"needs_human", "needs_human", store.ReclassifyNeedsHumanForRearm},
	}
	for _, tt := range rearm {
		t.Run("rearm "+tt.name, func(t *testing.T) {
			id := "rearm-" + tt.name
			save(t, id, "GH-"+tt.name, tt.status)
			if err := tt.fn("GH-"+tt.name, "/p", "re-armed "+value); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			assertClean(t, "error", got.Error)
		})
	}

	t.Run("chat_turns.error", func(t *testing.T) {
		now := time.Now().UTC()
		turn := &ChatTurn{ID: "turn-1", ConversationID: "web:c1", ProjectPath: "/p", Status: ChatTurnFailed,
			Error: "auth failed " + value, StartedAt: now.Add(-time.Second), CompletedAt: now}
		if err := store.SaveChatTurn(turn); err != nil {
			t.Fatal(err)
		}
		turns, err := store.ListChatTurnsSince(now.Add(-time.Hour), "", 10)
		if err != nil || len(turns) != 1 {
			t.Fatalf("ListChatTurnsSince: %v (%d)", err, len(turns))
		}
		assertClean(t, "error", turns[0].Error)
	})
}
