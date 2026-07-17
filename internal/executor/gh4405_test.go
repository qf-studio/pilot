package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestHandleSubIssueCoverageGap_UsesBareIssueNumberForGHCLI is the regression
// test for GH-4405: 2026-07-17 production logs showed
//
//	failed to label parent with pilot-needs-clarification after coverage gap ... error="... invalid issue format: \"GH-8\""
//	failed to post coverage-gap comment on parent ... error="... invalid issue format: \"GH-8\""
//
// because handleSubIssueCoverageGap passed the human-readable, prefixed task
// id ("GH-8") as gh CLI's positional issue argument. gh only accepts the bare
// issue number. This asserts the `gh issue edit`/`gh issue comment` calls
// triggered by a coverage gap use the bare number instead.
func TestHandleSubIssueCoverageGap_UsesBareIssueNumberForGHCLI(t *testing.T) {
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	// Every `gh issue create` fails so both planned subtasks are missing and
	// a coverage gap fires; every other subcommand (issue edit/comment) is
	// logged and succeeds, mirroring the real gh CLI's exit-0 behavior once
	// the issue argument is well-formed.
	content := "#!/bin/sh\n" +
		`echo "$@" >> "` + logFile + `"
if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  echo "gh: validation failed" >&2
  exit 1
fi
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)
	runner.subIssueCreateRetryAttempts = 1
	runner.subIssueCreateRetryDelay = time.Millisecond

	// No SourceIssueID set — mirrors the dispatcher-restored Task shape that
	// hit this bug in production (executions row has no source-issue-id
	// column; only the prefixed human-readable ID survives a restart).
	parent := &Task{ID: "GH-8"}
	if err := store.SaveExecution(&memory.Execution{ID: parent.LogExecutionID(), TaskID: parent.ID, Status: "running"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	plan := &EpicPlan{ParentTask: parent, Subtasks: gh4300TwoSubtasks()}
	if _, err := runner.CreateSubIssues(context.Background(), plan, t.TempDir()); err == nil {
		t.Fatal("CreateSubIssues returned nil error, want a coverage-gap error")
	}

	logData, _ := os.ReadFile(logFile)
	logStr := strings.TrimSpace(string(logData))
	if logStr == "" {
		t.Fatal("expected gh CLI invocations to be logged, got none")
	}

	var sawEdit, sawComment bool
	for _, line := range strings.Split(logStr, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "issue" {
			continue
		}
		switch fields[1] {
		case "edit":
			sawEdit = true
		case "comment":
			sawComment = true
		default:
			continue
		}
		issueArg := fields[2]
		if issueArg == parent.ID {
			t.Errorf("gh issue %s called with prefixed task id %q — gh CLI requires the bare issue number, line: %q",
				fields[1], issueArg, line)
		}
		if issueArg != "8" {
			t.Errorf("gh issue %s issue arg = %q, want bare issue number %q, line: %q",
				fields[1], issueArg, "8", line)
		}
	}
	if !sawEdit {
		t.Error("expected a `gh issue edit` call labeling the parent pilot-needs-clarification")
	}
	if !sawComment {
		t.Error("expected a `gh issue comment` call posting the coverage-gap comment")
	}
}

// TestExecuteSubIssuesTracked_ClosesParentWithBareIssueNumber is the
// regression test for GH-4405's third failure signature:
//
//	Failed to close parent issue ... error="failed to close issue GH-95: ... invalid issue format: \"GH-95\""
//
// executeSubIssuesTracked closed the parent epic issue by shelling out to
// `gh issue close <parent.ID>` with the prefixed task id. This asserts the
// close call (and the preceding progress comment) use the bare issue number.
func TestExecuteSubIssuesTracked_ClosesParentWithBareIssueNumber(t *testing.T) {
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" +
		`echo "$@" >> "` + logFile + `"
if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  echo "OPEN"
fi
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	r := NewRunner()
	r.skipPreflightChecks = true
	r.executeFunc = func(_ context.Context, task *Task) (*ExecutionResult, error) {
		return &ExecutionResult{TaskID: task.ID, Success: true}, nil
	}

	projectPath := t.TempDir()
	parent := &Task{ID: "GH-95", ProjectPath: projectPath}
	issues := []CreatedIssue{
		{Number: 96, Identifier: "96", URL: "https://github.com/o/r/issues/96",
			Subtask: PlannedSubtask{Title: "part 1", Description: "do part 1"}},
	}

	if _, _, err := r.executeSubIssuesTracked(context.Background(), parent, issues, projectPath, projectPath); err != nil {
		t.Fatalf("executeSubIssuesTracked returned unexpected error: %v", err)
	}

	logData, _ := os.ReadFile(logFile)
	logStr := strings.TrimSpace(string(logData))

	var sawParentClose bool
	for _, line := range strings.Split(logStr, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "issue" {
			continue
		}
		// Only inspect calls addressed at the parent (issue #95); sub-issue
		// #96 calls use its own bare number and are out of scope here.
		if fields[2] != "95" && fields[2] != "GH-95" {
			continue
		}
		if fields[1] == "close" {
			sawParentClose = true
		}
		if fields[2] == "GH-95" {
			t.Errorf("gh issue %s called with prefixed task id %q — gh CLI requires the bare issue number, line: %q",
				fields[1], fields[2], line)
		}
	}
	if !sawParentClose {
		t.Errorf("expected a `gh issue close 95 ...` call for the parent, log:\n%s", logStr)
	}
}
