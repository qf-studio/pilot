package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestDispatcher_QueueTask_RecordsDecompositionSkipEvent is a GH-4271
// regression test for the queue-time decision point (dispatcher.go
// QueueTask): an epic-classified task whose description is too short to
// clear decompose.min_description_words must record exactly one
// decomposition_skipped execution event, and the decomposition decision
// itself (the task still gets queued as a single unit) must be unchanged.
func TestDispatcher_QueueTask_RecordsDecompositionSkipEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)
	dispatcher.SetDecomposer(NewTaskDecomposer(&DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 300,
	}))

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-4271-EPIC",
		Title:       "[epic] roll out multi-service rollout",
		Description: "short description well under the word minimum",
		ProjectPath: "/tmp/gh-4271-test-project",
		Branch:      "test-branch",
		CreatePR:    true,
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("QueueTask() error: %v", err)
	}

	// Decomposition decision itself must be unchanged: the task is still
	// queued as a single execution row, not split into subtasks.
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution() error: %v", err)
	}
	if exec.Status != "queued" && exec.Status != "running" {
		t.Errorf("exec.Status = %q, want queued or running", exec.Status)
	}
	if exec.TaskID != task.ID {
		t.Errorf("exec.TaskID = %q, want %q", exec.TaskID, task.ID)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents() error: %v", err)
	}

	var skipEvents []*memory.Event
	for _, e := range events {
		if e.Stage == memory.StageDecompositionSkipped {
			skipEvents = append(skipEvents, e)
		}
	}
	if len(skipEvents) != 1 {
		t.Fatalf("got %d decomposition_skipped events, want exactly 1 (all events: %+v)", len(skipEvents), events)
	}

	detail := skipEvents[0].Detail
	for _, want := range []string{"reason=description_too_short", "complexity=epic", "min_description_words=300"} {
		if !strings.Contains(detail, want) {
			t.Errorf("event detail = %q, want it to contain %q", detail, want)
		}
	}
}

// TestDispatcher_QueueTask_BelowMinComplexity_NoSkipEvent verifies the
// negative case: a task that never met decompose.min_complexity in the first
// place is not reportable (SkipReasonBelowMinComplexity is excluded by
// design — see TaskDecomposer.ReportableSkip), so no decomposition_skipped
// event is written and the task queues normally (GH-4271).
func TestDispatcher_QueueTask_BelowMinComplexity_NoSkipEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)
	dispatcher.SetDecomposer(NewTaskDecomposer(&DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 50,
	}))

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-4271-MEDIUM",
		Title:       "Add a new dashboard widget",
		Description: "Add a new widget to the dashboard that shows the current queue depth and updates live via websocket polling.",
		ProjectPath: "/tmp/gh-4271-test-project-2",
		Branch:      "test-branch",
		CreatePR:    true,
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("QueueTask() error: %v", err)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents() error: %v", err)
	}
	for _, e := range events {
		if e.Stage == memory.StageDecompositionSkipped {
			t.Errorf("unexpected decomposition_skipped event for a below-threshold task: %+v", e)
		}
	}
}
