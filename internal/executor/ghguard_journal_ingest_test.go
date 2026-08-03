package executor

import (
	"testing"

	"github.com/qf-studio/pilot/internal/executor/ghguard"
	"github.com/qf-studio/pilot/internal/memory"
)

// TestIngestGHGuardJournal covers GH-4671's ingestion of gh-guard deny
// events into the same executor.github_sideeffect event stage and
// AlertEventTypeGithubSideEffect alert channel GH-4670 uses — mirroring
// sideeffect_audit_test.go's pattern for the audit half of the pair.
func TestIngestGHGuardJournal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)

	task := &Task{
		ID:            "GH-4671",
		Title:         "gh-guard shim",
		ProjectPath:   t.TempDir(),
		SourceRepo:    "qf-studio/pilot",
		SourceIssueID: "4671",
	}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	path := ghGuardJournalPath(task.ID)
	t.Cleanup(func() { _ = ghguard.RemoveJournal(path) })

	ctx := ghguard.TaskContext{Issue: "4671", Repo: "qf-studio/pilot", Branch: "pilot/GH-4671"}
	if err := ghguard.AppendDenyToJournal(path, []string{"issue", "close", "4649"}, ctx, ghguard.Verdict{Decision: ghguard.Deny, Reason: "closes issue lifecycle state"}); err != nil {
		t.Fatalf("AppendDenyToJournal: %v", err)
	}
	if err := ghguard.AppendDenyToJournal(path, []string{"issue", "comment", "4649", "--body", "closing"}, ctx, ghguard.Verdict{Decision: ghguard.Deny, Reason: "targets issue #4649, task is scoped to issue #4671"}); err != nil {
		t.Fatalf("AppendDenyToJournal: %v", err)
	}

	runner.ingestGHGuardJournal(task)

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	var sideEffectEvents int
	for _, e := range events {
		if e.Stage == memory.StageGithubSideEffect {
			sideEffectEvents++
		}
	}
	if sideEffectEvents != 2 {
		t.Errorf("got %d executor.github_sideeffect events, want 2", sideEffectEvents)
	}

	var sideEffectAlerts int
	for _, e := range processor.events {
		if e.Type == AlertEventTypeGithubSideEffect {
			sideEffectAlerts++
		}
	}
	if sideEffectAlerts != 2 {
		t.Errorf("got %d github_sideeffect alerts, want 2", sideEffectAlerts)
	}

	// The journal must be removed after ingestion so a retried task_id
	// doesn't re-report the same denies.
	remaining, err := ghguard.ReadJournal(path)
	if err != nil {
		t.Fatalf("ReadJournal after ingest: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected journal to be removed after ingestion, got %d leftover entries", len(remaining))
	}
}

// TestIngestGHGuardJournal_NoJournalIsNoOp covers the common case: a run
// with no gh-guard denies must not emit any event or alert.
func TestIngestGHGuardJournal_NoJournalIsNoOp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)

	task := &Task{
		ID:            "GH-4671-NOOP",
		ProjectPath:   t.TempDir(),
		SourceRepo:    "qf-studio/pilot",
		SourceIssueID: "4671",
	}
	if err := store.SaveExecution(&memory.Execution{ID: task.LogExecutionID(), TaskID: task.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	runner.ingestGHGuardJournal(task)

	events, err := store.ListExecutionEvents(task.LogExecutionID())
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
	if len(processor.events) != 0 {
		t.Errorf("expected no alerts, got %d", len(processor.events))
	}
}
