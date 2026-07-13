package executor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecute_DescriptionTooShort_LogsAndRecordsSkipEvent is a GH-4271
// regression test for the runner.go Execute() decomposer call site: a
// complex-classified task whose description is too short to clear
// decompose.min_description_words must produce exactly one log line and one
// decomposition_skipped execution event carrying the concrete
// threshold/observed values, and must still execute as a single direct run
// (the decomposition decision itself is unchanged — see
// TestExecute_SinglePackageEpic_NumberedListDescription_SkipsDecomposition in
// gh4052_test.go for the epic-tier sibling of this call site).
func TestExecute_DescriptionTooShort_LogsAndRecordsSkipEvent(t *testing.T) {
	dir := setupPRGuardRepo(t, "pilot/GH-4271-decompose-skip", true)
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "done", Model: "claude"},
	}

	var buf bytes.Buffer
	runner := NewRunnerWithBackend(backend)
	runner.log = slog.New(slog.NewTextHandler(&buf, nil))
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	runner.SetLogStore(store)
	runner.EnableDecomposition(&DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 50,
	})

	const execID = "GH-4271-DESC-SHORT"
	if err := store.SaveExecution(&memory.Execution{
		ID:          execID,
		TaskID:      "GH-4271-DESC-SHORT",
		ProjectPath: dir,
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	task := &Task{
		ID:          "GH-4271-DESC-SHORT",
		ExecutionID: execID,
		Title:       "refactor the executor pipeline",
		Description: "small change, nothing structural here",
		ProjectPath: dir,
		Branch:      "pilot/GH-4271-decompose-skip",
		CreatePR:    false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s", result.Error)
	}

	// Decomposition decision itself must be unchanged: the task ran as a
	// single direct execution, exactly like it did before this fix.
	backend.mu.Lock()
	execCount := backend.execCount
	backend.mu.Unlock()
	if execCount != 1 {
		t.Errorf("backend.execCount = %d, want 1 (task must run as a single unit, not be decomposed)", execCount)
	}
	if result.IsEpic {
		t.Error("expected IsEpic=false for a directly-executed complex task")
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "reason=description_too_short") {
		t.Errorf("expected skip log line with reason=description_too_short, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "min_description_words=50") {
		t.Errorf("expected skip log line with concrete min_description_words value, got: %s", logOutput)
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
	if !strings.Contains(skipEvents[0].Detail, "reason=description_too_short") {
		t.Errorf("event detail = %q, want it to contain reason=description_too_short", skipEvents[0].Detail)
	}
}

// TestExecute_SinglePackageEpic_RecordsUnifiedSkipEvent extends GH-4052's
// single-package-scope regression (gh4052_test.go) to verify the GH-4271
// unification: that branch already logged "Single-package scope detected"
// but never wrote an execution_events row, leaving `pilot trace` silent for
// exactly the epic-classified-but-collapsed-to-single-task scenario this
// issue is about.
func TestExecute_SinglePackageEpic_RecordsUnifiedSkipEvent(t *testing.T) {
	dir := setupPRGuardRepo(t, "pilot/GH-4271-single-package", true)
	store, cleanup := setupTestStore(t)
	defer cleanup()

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "done", Model: "claude"},
	}

	runner := NewRunnerWithBackend(backend)
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	runner.SetLogStore(store)
	runner.EnableDecomposition(nil)

	const execID = "GH-4271-SINGLE-PKG"
	if err := store.SaveExecution(&memory.Execution{
		ID:          execID,
		TaskID:      "GH-4271-SINGLE-PKG",
		ProjectPath: dir,
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	task := &Task{
		ID:          "GH-4271-SINGLE-PKG",
		ExecutionID: execID,
		Title:       "[epic] fix(executor): honor require_ci on polled merge path",
		Description: "## Suggested investigation scope\n\n1. Check the guard\n2. Check the matcher\n",
		ProjectPath: dir,
		Branch:      "pilot/GH-4271-single-package",
		CreatePR:    false,
	}
	runner.planEpicFn = func(_ context.Context, tsk *Task, _ string) (*EpicPlan, error) {
		return gh4052SinglePackagePlan(tsk), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, task)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() not successful: %s", result.Error)
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
	if !strings.Contains(skipEvents[0].Detail, "single_package_scope") {
		t.Errorf("event detail = %q, want it to contain reason=single_package_scope", skipEvents[0].Detail)
	}
}
