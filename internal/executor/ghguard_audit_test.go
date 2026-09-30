package executor

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5542: a denial whose origin is a test binary must be neither journaled
// nor alerted (only counted); a denial from any other process still is.
func TestIngestGhGuardDenials_TestOriginSuppressed(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-gh5542", TaskID: "GH-5542", ProjectPath: t.TempDir(), Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	processor := &fakeAlertProcessor{}
	r := &Runner{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		alertProcessor: processor,
	}
	r.SetLogStore(store)

	task := &Task{ID: "GH-5542", ExecutionID: exec.ID, SourceIssueID: "5542"}
	now := time.Now()
	result := &BackendResult{GhGuardDenials: []ghguard.JournalEntry{
		{Time: now, Verdict: ghguard.VerdictDeny, Reason: "fixture", Args: []string{"issue", "comment", "EPIC-100"}, TestOrigin: true},
		{Time: now, Verdict: ghguard.VerdictDeny, Reason: "fixture", Args: []string{"issue", "close", "NAV"}, TestOrigin: true},
		{Time: now, Verdict: ghguard.VerdictDeny, Reason: "model", Args: []string{"issue", "close", "9999"}},
	}}

	r.ingestGhGuardDenials(task, result)

	events, err := store.ListExecutionEvents(exec.ID)
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	var denied, suppressed []*memory.Event
	for _, e := range events {
		switch e.Stage {
		case memory.StageGhGuardDenied:
			denied = append(denied, e)
		case memory.StageGhGuardTestDenialsSuppressed:
			suppressed = append(suppressed, e)
		}
	}
	if len(denied) != 1 || !strings.Contains(denied[0].Detail, "9999") {
		t.Fatalf("journaled denials = %+v, want exactly the non-test denial", denied)
	}
	if len(suppressed) != 1 || suppressed[0].Detail != `{"count":2}` {
		t.Fatalf("suppressed events = %+v, want one with count 2", suppressed)
	}
	if len(processor.events) != 1 || !strings.Contains(processor.events[0].Error, "9999") {
		t.Fatalf("alerts = %+v, want exactly one for the non-test denial", processor.events)
	}
}
