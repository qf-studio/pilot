package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gh4406ThreeSubtasks returns three subtasks across distinct directories
// (so isSinglePackageScope doesn't consolidate them into a single task),
// mirroring the pointer epic GH-8 shape from the bug report: a plan wants
// N sub-issues, but only some of them survived a prior, interrupted run.
func gh4406ThreeSubtasks() []PlannedSubtask {
	return []PlannedSubtask{
		{Order: 1, Title: "feat(gateway): add websocket handler", Description: "Implement upgrade handler in internal/gateway/server.go"},
		{Order: 2, Title: "feat(adapters): add telegram bot", Description: "Wire bot client in internal/adapters/telegram/bot.go"},
		{Order: 3, Title: "feat(vectors): add duckdb store", Description: "Add vector store in internal/knowledge/vectors/store.go"},
	}
}

// TestReconcileRecoveredSubIssues_AdoptsMatchesAndReportsMissing is the core
// GH-4406 unit test: given a plan of 3 subtasks and only 2 recovered issues
// whose titles match subtasks 1 and 3, the reconciler must adopt those two
// and report subtask 2 as the only one missing — not decline the whole
// batch because the raw counts (2 recovered vs 3 planned) don't line up.
func TestReconcileRecoveredSubIssues_AdoptsMatchesAndReportsMissing(t *testing.T) {
	planned := gh4406ThreeSubtasks()
	recovered := []CreatedIssue{
		{Number: 301, State: "open", Subtask: PlannedSubtask{Title: "feat(gateway): add websocket handler"}},
		{Number: 303, State: "open", Subtask: PlannedSubtask{Title: "feat(vectors): add duckdb store"}},
	}

	adopted, missing := reconcileRecoveredSubIssues(planned, recovered)

	if len(adopted) != 2 {
		t.Fatalf("adopted = %d, want 2", len(adopted))
	}
	if len(missing) != 1 {
		t.Fatalf("missing = %d, want 1", len(missing))
	}
	if missing[0].Title != "feat(adapters): add telegram bot" {
		t.Errorf("missing[0].Title = %q, want the un-recovered second subtask", missing[0].Title)
	}

	// Adopted issues must keep their real tracker identity...
	gotNumbers := map[int]bool{}
	for _, iss := range adopted {
		gotNumbers[iss.Number] = true
	}
	if !gotNumbers[301] || !gotNumbers[303] {
		t.Errorf("adopted issue numbers = %v, want 301 and 303 preserved", gotNumbers)
	}
	// ...but be re-attached to the current plan's PlannedSubtask (Order set).
	for _, iss := range adopted {
		if iss.Subtask.Order == 0 {
			t.Errorf("adopted issue %d has zero Order — expected it re-attached to the current plan's subtask", iss.Number)
		}
	}
}

// TestReconcileRecoveredSubIssues_NoMatchIsConflict covers the "refusing
// entirely is only right if the existing children conflict with the plan"
// half of the GH-4406 spec: when none of the recovered issues' titles match
// any planned subtask, nothing should be adopted — every planned subtask
// comes back as missing, signaling the caller should not blindly create over
// unrelated existing children.
func TestReconcileRecoveredSubIssues_NoMatchIsConflict(t *testing.T) {
	planned := gh4406ThreeSubtasks()
	recovered := []CreatedIssue{
		{Number: 501, State: "open", Subtask: PlannedSubtask{Title: "chore(unrelated): totally different work"}},
	}

	adopted, missing := reconcileRecoveredSubIssues(planned, recovered)

	if len(adopted) != 0 {
		t.Fatalf("adopted = %d, want 0 (no titles match — this is a conflict, not partial progress)", len(adopted))
	}
	if len(missing) != len(planned) {
		t.Fatalf("missing = %d, want %d (every planned subtask, since nothing recovered matches)", len(missing), len(planned))
	}
}

// TestReconcilePartialSubIssueRecovery_CreatesOnlyMissingSubtasks drives the
// Runner method end-to-end against a faked `gh` CLI: 2 of 3 planned
// subtasks already exist (recovered), 1 is missing. The reconciler must
// create an issue for ONLY the missing subtask — never re-invoking `gh issue
// create` for the two that already exist — and return all 3 as the merged
// set.
func TestReconcilePartialSubIssueRecovery_CreatesOnlyMissingSubtasks(t *testing.T) {
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		`echo "$@" >> "` + logFile + `"
if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  echo "https://github.com/owner/repo/issues/999"
fi
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	subtasks := gh4406ThreeSubtasks()
	plan := &EpicPlan{
		ParentTask: &Task{ID: "GH-4406P"},
		Subtasks:   subtasks,
	}
	recovered := []CreatedIssue{
		{Number: 301, State: "open", Subtask: subtasks[0]},
		{Number: 303, State: "open", Subtask: subtasks[2]},
	}

	runner := NewRunner()

	merged, err := runner.reconcilePartialSubIssueRecovery(context.Background(), plan, subtasks, recovered, t.TempDir())
	if err != nil {
		t.Fatalf("reconcilePartialSubIssueRecovery returned error: %v", err)
	}
	if len(merged) != 3 {
		t.Fatalf("merged = %d, want 3 (2 adopted + 1 newly created)", len(merged))
	}

	logData, _ := os.ReadFile(logFile)
	createCount := strings.Count(string(logData), "issue create")
	if createCount != 1 {
		t.Errorf("gh issue create invocation count = %d, want 1 (only the missing subtask), log:\n%s", createCount, logData)
	}
	if !strings.Contains(string(logData), "feat(adapters): add telegram bot") {
		t.Errorf("expected the missing subtask's title in the gh issue create call, log:\n%s", logData)
	}
}

// TestRunner_Execute_EpicReconcilesPartialRecoveryByCreatingMissingSubtasks
// is the GH-4406 regression test for the reported livelock: pointer epic
// GH-8 wanted N sub-issues, found fewer already-open children from a prior
// attempt, and declined forever because recovery refused to create the
// missing ones. This drives the real r.Execute entry point end-to-end with
// a faked `gh` CLI and confirms the epic proceeds (adopts the 2 existing
// children, creates the 1 missing one, executes all 3) instead of
// declining.
func TestRunner_Execute_EpicReconcilesPartialRecoveryByCreatingMissingSubtasks(t *testing.T) {
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		`echo "$@" >> "` + logFile + `"
if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  echo "https://github.com/owner/repo/issues/999"
fi
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	subtasks := gh4406ThreeSubtasks()

	r := NewRunner()
	r.skipPreflightChecks = true

	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil // force ErrSubIssuesAlreadyExist -> recovery path
	}
	// Only subtasks 1 and 3 survived a prior partial run; subtask 2 was
	// never created — the exact GH-4406 livelock shape (planner wants N,
	// fewer than N children exist).
	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		return []CreatedIssue{
			{Number: 301, State: "open", Subtask: subtasks[0]},
			{Number: 303, State: "open", Subtask: subtasks[2]},
		}, nil
	}
	r.planEpicFn = func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
		return &EpicPlan{ParentTask: task, Subtasks: subtasks}, nil
	}

	var executedIDs []string
	r.executeFunc = func(_ context.Context, task *Task) (*ExecutionResult, error) {
		executedIDs = append(executedIDs, task.ID)
		// Non-zero tokens/files so the zero-delivery no_op guard
		// (classifyZeroDeliveryEpicCompletion) doesn't reclassify this
		// otherwise-successful epic — unrelated to the GH-4406 fix under test.
		return &ExecutionResult{TaskID: task.ID, Success: true, TokensOutput: 100, FilesChanged: 1}, nil
	}

	task := &Task{ID: "GH-4406R", Title: "[epic] partial recovery reconciliation test"}

	result, err := r.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected successful epic result, got: %+v", result)
	}
	if result.Declined {
		t.Error("expected Declined=false — the gap should have been reconciled instead of declined forever")
	}
	if len(executedIDs) != 3 {
		t.Fatalf("expected 3 child executions (2 adopted + 1 newly created), got %d: %v", len(executedIDs), executedIDs)
	}

	logData, _ := os.ReadFile(logFile)
	createCount := strings.Count(string(logData), "issue create")
	if createCount != 1 {
		t.Errorf("expected exactly 1 `gh issue create` call (only the missing subtask), got %d, log:\n%s", createCount, logData)
	}
}

// TestRunner_Execute_EpicRecoveryConflictStillDeclines guards the other half
// of the GH-4406 spec: when the recovered children's titles don't match the
// current plan at all (a genuine conflict — e.g. stale children from an
// unrelated prior decomposition), the epic must still decline rather than
// blindly creating a full new batch on top of the unrelated existing
// issues.
func TestRunner_Execute_EpicRecoveryConflictStillDeclines(t *testing.T) {
	subtasks := gh4406ThreeSubtasks()

	r := NewRunner()
	r.skipPreflightChecks = true
	r.dryRun = true

	r.openSubIssueCheck = func(_ context.Context, _, _ string) (bool, error) {
		return true, nil
	}
	r.recoverSubIssuesFn = func(_ context.Context, _, _ string) ([]CreatedIssue, error) {
		return []CreatedIssue{
			{Number: 601, State: "open", Subtask: PlannedSubtask{Title: "chore(unrelated): totally different work"}},
		}, nil
	}
	r.planEpicFn = func(_ context.Context, task *Task, _ string) (*EpicPlan, error) {
		return &EpicPlan{ParentTask: task, Subtasks: subtasks}, nil
	}

	task := &Task{ID: "GH-4406C", Title: "[epic] recovery conflict test"}

	result, err := r.Execute(context.Background(), task)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Declined {
		t.Error("expected Declined=true — unrelated existing children must not be treated as partial progress")
	}
}
