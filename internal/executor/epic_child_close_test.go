package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// GH-4697: the epic child-completion close call (executeSubIssuesTracked,
// epic.go ~line 3159) previously closed a sub-issue unconditionally the
// moment its child run reported Success + a PR URL — with no check on
// whether that PR had actually merged. That is the exact shape of the
// TASK-437 incident: #4660 was closed at 10:28:44Z, three seconds after its
// PR #4667 opened at 10:28:41Z, and 97 minutes before PR #4667 itself closed
// unmerged at 12:05:06Z (same for #4661/#4672). This suite exercises the
// guard added to getSubIssuePRState/executeSubIssuesTracked: an OPEN,
// unmerged PR blocks the close and emits a WARN naming the PR; a MERGED PR
// (or a state-query failure, fail-safe) is handled without regressing the
// existing close-on-completion behavior.

func TestExecuteSubIssuesTracked_OpenUnmergedPR_DefersClose(t *testing.T) {
	var buf bytes.Buffer
	var queriedPRNumber int

	r := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true, PRUrl: "https://github.com/owner/repo/pull/501"}, nil
	})
	r.log = slog.New(slog.NewTextHandler(&buf, nil))
	r.subIssuePRStateCheck = func(_ context.Context, _ string, prNumber int) (*SubIssuePRState, error) {
		queriedPRNumber = prNumber
		return &SubIssuePRState{State: "OPEN", Merged: false}, nil
	}

	parent := &Task{ID: "GH-999"}
	issues := []CreatedIssue{
		{Number: 500, Identifier: "500", URL: "https://github.com/owner/repo/issues/500",
			Subtask: PlannedSubtask{Title: "part 1", Description: "do part 1"}},
	}

	childStates, _, err := r.executeSubIssuesTracked(context.Background(), parent, issues, "", "")
	if err != nil {
		t.Fatalf("executeSubIssuesTracked returned unexpected error: %v", err)
	}

	if queriedPRNumber != 501 {
		t.Errorf("expected PR state check to query PR #501, got #%d", queriedPRNumber)
	}

	// The child's internal terminal state must still be "completed" — the
	// guard defers the GitHub-visible close, it does not change the epic's
	// own bookkeeping of whether the child delivered.
	if len(childStates) != 1 || childStates[0] != "completed" {
		t.Errorf("expected childStates = [completed], got %v", childStates)
	}

	logs := buf.String()
	if !strings.Contains(logs, "sub-issue PR still open and unmerged; deferring child-issue close") {
		t.Errorf("expected WARN log deferring close for an open PR, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "pr_number=501") {
		t.Errorf("expected WARN log to name the PR number (501), got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "https://github.com/owner/repo/pull/501") {
		t.Errorf("expected WARN log to name the PR URL, got logs:\n%s", logs)
	}
	// CloseIssueWithComment must never have been invoked for the *sub-issue*
	// specifically (issue=500) — if it had been (even as a dry-run no-op), it
	// logs its own "dry-run: skipping CloseIssueWithComment" line naming it.
	// The parent (issue=999) is still closed as usual once the loop ends, so
	// this must be scoped to the child issue, not a blanket substring check.
	if strings.Contains(logs, `skipping CloseIssueWithComment" issue=500`) {
		t.Errorf("expected the child issue's close to be skipped entirely (guard blocks the call), got logs:\n%s", logs)
	}
}

func TestExecuteSubIssuesTracked_MergedPR_ClosesNormally(t *testing.T) {
	var buf bytes.Buffer

	r := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true, PRUrl: "https://github.com/owner/repo/pull/502"}, nil
	})
	r.log = slog.New(slog.NewTextHandler(&buf, nil))
	r.subIssuePRStateCheck = func(_ context.Context, _ string, _ int) (*SubIssuePRState, error) {
		return &SubIssuePRState{State: "MERGED", Merged: true}, nil
	}

	parent := &Task{ID: "GH-999"}
	issues := []CreatedIssue{
		{Number: 500, Identifier: "500", URL: "https://github.com/owner/repo/issues/500",
			Subtask: PlannedSubtask{Title: "part 1", Description: "do part 1"}},
	}

	if _, _, err := r.executeSubIssuesTracked(context.Background(), parent, issues, "", ""); err != nil {
		t.Fatalf("executeSubIssuesTracked returned unexpected error: %v", err)
	}

	logs := buf.String()
	if strings.Contains(logs, "deferring child-issue close") {
		t.Errorf("did not expect the close to be deferred for a merged PR, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, `skipping CloseIssueWithComment" issue=500`) {
		t.Errorf("expected the normal close path to run (dry-run no-op) for the child issue on a merged PR, got logs:\n%s", logs)
	}
}

func TestExecuteSubIssuesTracked_PRStateQueryError_FailsSafeAndWarns(t *testing.T) {
	var buf bytes.Buffer

	r := newTestRunnerWithExecFunc(func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true, PRUrl: "https://github.com/owner/repo/pull/503"}, nil
	})
	r.log = slog.New(slog.NewTextHandler(&buf, nil))
	r.subIssuePRStateCheck = func(_ context.Context, _ string, _ int) (*SubIssuePRState, error) {
		return nil, errors.New("network blip talking to gh")
	}

	parent := &Task{ID: "GH-999"}
	issues := []CreatedIssue{
		{Number: 500, Identifier: "500", URL: "https://github.com/owner/repo/issues/500",
			Subtask: PlannedSubtask{Title: "part 1", Description: "do part 1"}},
	}

	if _, _, err := r.executeSubIssuesTracked(context.Background(), parent, issues, "", ""); err != nil {
		t.Fatalf("executeSubIssuesTracked returned unexpected error: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "could not determine sub-issue PR state before close; leaving issue open") {
		t.Errorf("expected fail-safe WARN log when the PR state query errors, got logs:\n%s", logs)
	}
	if !strings.Contains(logs, "network blip talking to gh") {
		t.Errorf("expected the WARN log to carry the underlying error, got logs:\n%s", logs)
	}
	if strings.Contains(logs, `skipping CloseIssueWithComment" issue=500`) {
		t.Errorf("expected the child issue's close to be skipped when PR state can't be determined, got logs:\n%s", logs)
	}
}

// TestGetSubIssuePRState_DryRunDefaultIsMerged verifies the fallback default
// (no override wired) returns a terminal MERGED state under dry-run instead
// of shelling out to a real `gh pr view` — dry-run never makes real gh calls
// and CloseIssueWithComment already no-ops the close itself, so this check
// must not be what changes dry-run behavior.
func TestGetSubIssuePRState_DryRunDefaultIsMerged(t *testing.T) {
	r := &Runner{
		log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		dryRun: true,
	}

	state, err := r.getSubIssuePRState(context.Background(), "", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == nil || !state.Merged || state.State != "MERGED" {
		t.Errorf("expected dry-run default state {MERGED, true}, got %+v", state)
	}
}
