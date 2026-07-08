package executor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// gh4052SinglePackagePlan returns two subtasks whose descriptions both cite
// files under internal/executor/, so isSinglePackageScope consolidates them
// into a single task instead of creating separate GitHub issues.
func gh4052SinglePackagePlan(task *Task) *EpicPlan {
	return &EpicPlan{
		ParentTask: task,
		Subtasks: []PlannedSubtask{
			{Order: 1, Title: "fix(executor): update epic.go", Description: "Edit internal/executor/epic.go"},
			{Order: 2, Title: "fix(executor): update runner.go", Description: "Edit internal/executor/runner.go"},
		},
	}
}

// TestExecute_SinglePackageEpic_NumberedListDescription_SkipsDecomposition is a
// regression test for GH-4052: an epic task the planner classifies as
// single-package scope must run as ONE unit of work, even though its original
// description (mirroring the GH-3994 require_ci issue body) contains numbered
// lists that the regex TaskDecomposer would otherwise explode into up to
// MaxSubtasks subtasks.
func TestExecute_SinglePackageEpic_NumberedListDescription_SkipsDecomposition(t *testing.T) {
	dir := setupPRGuardRepo(t, "pilot/GH-4052-decompose-guard", true)

	backend := &mockFixedBackend{
		result: &BackendResult{Success: true, Output: "done", Model: "claude"},
	}

	var buf bytes.Buffer
	runner := NewRunnerWithBackend(backend)
	runner.log = slog.New(slog.NewTextHandler(&buf, nil))
	runner.SetRecordingEnabled(false)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	// Decomposition enabled with real regex-based decomposer — if the GH-4052
	// gate is missing, this decomposer WILL match the numbered lists below.
	runner.EnableDecomposition(nil)

	task := &Task{
		ID:    "GH-4052-TEST",
		Title: "[epic] fix(executor): honor require_ci on polled merge path",
		Description: "## Suggested investigation scope\n\n" +
			"1. Check the epic planner's single-package guard\n" +
			"2. Check the regex decomposer's numbered-list matcher\n" +
			"3. Check the Decompose() call site in runner.go\n\n" +
			"## Implementation spec\n\n" +
			"1. Fix epic.go consolidateEpicPlan\n" +
			"2. Fix runner.go call site\n" +
			"3. Add regression tests\n" +
			"4. Run go build\n" +
			"5. Run go test\n" +
			"6. Commit changes",
		ProjectPath: dir,
		Branch:      "pilot/GH-4052-decompose-guard",
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
	if result.IsEpic {
		t.Error("single-package epic should fall through to direct execution, not report IsEpic=true")
	}

	backend.mu.Lock()
	execCount := backend.execCount
	backend.mu.Unlock()
	if execCount != 1 {
		t.Errorf("backend.execCount = %d, want 1 (task must run as a single unit, not be decomposed)", execCount)
	}

	logOutput := buf.String()
	if strings.Contains(logOutput, "Task decomposed") {
		t.Errorf("expected no 'Task decomposed' log line for single-package epic, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "Single-package scope detected") {
		t.Errorf("expected 'Single-package scope detected' log line, got: %s", logOutput)
	}
}
