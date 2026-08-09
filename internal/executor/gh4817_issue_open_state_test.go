package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH-4817 (TASK-459 Phase 3, Task 5/7): regression suite for the open-state
// guards added to every additive GitHub label/comment write site that fires
// on a stalled/failed/gap execution path. Each site consults the shared
// fetchIssueState seam (issue_state.go, GH-4656) before writing and skips
// the write on positive evidence the issue is already closed — a closed
// issue has already left the poller's candidate set, so labeling/commenting
// on it strands state no human will ever revisit. A lookup error still
// fails open (today's pre-GH-4817 behavior), covered elsewhere by every
// existing test in this suite that leaves fetchIssueState un-stubbed against
// a projectDir with no git remote (real fetch errors, guard proceeds).

// TestIsNoArtifactExplainedOutcome exercises the classification helper (Task
// 3/4's shared dependency) that GH-3053's GitLab demotion (handlers.go) and
// the CLI's no-artifact check (commands.go) both consult before inferring
// failure from an absent commit/PR alone.
func TestIsNoArtifactExplainedOutcome(t *testing.T) {
	tests := []struct {
		outcome string
		want    bool
	}{
		{outcome: string(ExecStatusNoOp), want: true},
		{outcome: string(ExecStatusSuperseded), want: true},
		{outcome: string(ExecStatusCanceled), want: true},
		{outcome: string(ExecStatusCompleted), want: false},
		{outcome: string(ExecStatusFailed), want: false},
		{outcome: "", want: false},
		{outcome: "some-unrelated-outcome", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.outcome, func(t *testing.T) {
			if got := IsNoArtifactExplainedOutcome(tt.outcome); got != tt.want {
				t.Errorf("IsNoArtifactExplainedOutcome(%q) = %v, want %v", tt.outcome, got, tt.want)
			}
		})
	}
}

// setupFakeGhCLI installs a fake `gh` binary on PATH for the duration of the
// test that appends its argv to logFile and exits 0. Mirrors the inline
// pattern already used by TestDispatcher_StallTaskAfterRepickHardCap_SurfacesStalledIssue.
func setupFakeGhCLI(t *testing.T) (logFile string) {
	t.Helper()
	fakeBin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" + `echo "$@" >> "` + logFile + `"` + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)
	return logFile
}

// Task 5a: dispatcher.go surfaceStalledIssue.
func TestSurfaceStalledIssue_SkipsWhenIssueClosed(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: true}, nil
	})

	store, cleanup := setupTestStore(t)
	defer cleanup()
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	task := &Task{ID: "GH-9101", ProjectPath: t.TempDir(), Title: "Closed head issue", SourceAdapter: "github"}
	dispatcher.surfaceStalledIssue(task, "repick hard cap reached")

	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("expected no gh CLI invocation when the issue is already closed")
	}
}

// Task 5b: handlers.go notifyTaskStartedSDK caller — the guard itself lives
// in cmd/pilot (a different module boundary from this package), so it isn't
// directly testable from internal/executor. It's exercised in
// cmd/pilot/gh4817_notify_started_gate_test.go instead.

// Task 5c: title_rejection.go postTitleRejectionEscalation.
func TestPostTitleRejectionEscalation_SkipsWhenIssueClosed(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: true}, nil
	})

	store, cleanup := setupTestStore(t)
	defer cleanup()
	runner := NewRunner()
	runner.SetLogStore(store)

	task := &Task{
		ID:            "GH-9102",
		Title:         "not a conventional title",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
	}

	if err := runner.postTitleRejectionEscalation(context.Background(), task); err != nil {
		t.Fatalf("postTitleRejectionEscalation: %v", err)
	}

	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("expected no gh CLI invocation when the issue is already closed")
	}
}

func TestPostTitleRejectionEscalation_ProceedsWhenIssueOpen(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})

	store, cleanup := setupTestStore(t)
	defer cleanup()
	runner := NewRunner()
	runner.SetLogStore(store)

	task := &Task{
		ID:            "GH-9103",
		Title:         "not a conventional title",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
	}

	if err := runner.postTitleRejectionEscalation(context.Background(), task); err != nil {
		t.Fatalf("postTitleRejectionEscalation: %v", err)
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("expected gh CLI to be invoked for an open issue: %v", err)
	}
	calls := string(logBytes)
	if !strings.Contains(calls, "issue comment 9103") {
		t.Errorf("expected a comment posted to issue 9103, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "--add-label pilot-title-rejected") {
		t.Errorf("expected pilot-title-rejected to be added, got calls:\n%s", calls)
	}
}

// Task 5d: epic.go handleSubIssueCoverageGap.
func TestHandleSubIssueCoverageGap_SkipsLabelingWhenParentClosed(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: true}, nil
	})

	store, cleanup := setupTestStore(t)
	defer cleanup()
	runner := NewRunner()
	runner.SetLogStore(store)

	parent := &Task{ID: "GH-9104", ProjectPath: t.TempDir(), SourceAdapter: "github"}
	plan := &EpicPlan{
		ParentTask: parent,
		Subtasks:   []PlannedSubtask{{Title: "sub 1", Order: 1}, {Title: "sub 2", Order: 2}},
	}

	gap := runner.handleSubIssueCoverageGap(context.Background(), plan, nil, parent.ProjectPath, nil)
	if gap == nil {
		t.Fatal("expected a non-nil coverage gap")
	}

	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("expected no gh CLI invocation when the parent issue is already closed")
	}
}

func TestHandleSubIssueCoverageGap_LabelsWhenParentOpen(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})

	store, cleanup := setupTestStore(t)
	defer cleanup()
	runner := NewRunner()
	runner.SetLogStore(store)

	parent := &Task{ID: "GH-9105", ProjectPath: t.TempDir(), SourceAdapter: "github"}
	plan := &EpicPlan{
		ParentTask: parent,
		Subtasks:   []PlannedSubtask{{Title: "sub 1", Order: 1}, {Title: "sub 2", Order: 2}},
	}

	gap := runner.handleSubIssueCoverageGap(context.Background(), plan, nil, parent.ProjectPath, nil)
	if gap == nil {
		t.Fatal("expected a non-nil coverage gap")
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("expected gh CLI to be invoked for an open parent issue: %v", err)
	}
	calls := string(logBytes)
	if !strings.Contains(calls, "--add-label pilot-needs-clarification") {
		t.Errorf("expected pilot-needs-clarification to be added, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "issue comment 9105") {
		t.Errorf("expected a coverage-gap comment posted to issue 9105, got calls:\n%s", calls)
	}
}
