package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// TestDetectChildDependency_ExplicitRef covers the explicit "Depends on: #N" /
// "Blocked by: #N" ref path, including the sibling-set scoping requirement
// (GH-4234): a ref to an issue outside the current epic's child set must not
// be honored.
func TestDetectChildDependency_ExplicitRef(t *testing.T) {
	siblings := map[int]bool{100: true, 101: true}

	tests := []struct {
		name        string
		title       string
		description string
		wantDepends bool
		wantReason  DependencyReason
	}{
		{
			name:        "Depends on sibling",
			description: "Finish the rollout.\n\nDepends on: #100",
			wantDepends: true,
			wantReason:  DependencyExplicitRef,
		},
		{
			name:        "Blocked by sibling",
			description: "Finish the rollout.\n\nBlocked by: #101",
			wantDepends: true,
			wantReason:  DependencyExplicitRef,
		},
		{
			name:        "ref to issue outside sibling set is not honored",
			description: "Depends on: #999",
			wantDepends: false,
			wantReason:  DependencyNone,
		},
		{
			name:        "no ref at all",
			description: "Just a plain implementation task.",
			wantDepends: false,
			wantReason:  DependencyNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			depends, reason := detectChildDependency(tt.title, tt.description, siblings)
			if depends != tt.wantDepends {
				t.Errorf("depends = %v, want %v", depends, tt.wantDepends)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
		})
	}
}

// TestDetectChildDependency_VerificationShape is the table test required by
// GH-4234's acceptance criteria: positive phrases ("verify…", "confirm zero
// hits…", "run the acceptance…", "regression-test…") must be treated as
// dependent on the prior sibling; negative phrases ("add…", "fix…",
// "implement…") must not.
func TestDetectChildDependency_VerificationShape(t *testing.T) {
	noSiblings := map[int]bool{}

	tests := []struct {
		name        string
		title       string
		wantDepends bool
	}{
		{name: "verify the migration completed", title: "verify the migration completed", wantDepends: true},
		{name: "confirm zero hits for the removed helper", title: "confirm zero hits for the removed helper", wantDepends: true},
		{name: "run the acceptance suite", title: "run the acceptance suite", wantDepends: true},
		{name: "regression-test the previous change", title: "regression-test the previous change", wantDepends: true},
		{name: "regression test without hyphen", title: "regression test the previous change", wantDepends: true},

		{name: "add rate limiting to the API", title: "add rate limiting to the API", wantDepends: false},
		{name: "fix the flaky test", title: "fix the flaky test", wantDepends: false},
		{name: "implement the new endpoint", title: "implement the new endpoint", wantDepends: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			depends, reason := detectChildDependency(tt.title, "", noSiblings)
			if depends != tt.wantDepends {
				t.Errorf("detectChildDependency(%q) depends = %v, want %v (reason=%q)", tt.title, depends, tt.wantDepends, reason)
			}
			if tt.wantDepends && reason != DependencyVerificationShape {
				t.Errorf("reason = %q, want %q", reason, DependencyVerificationShape)
			}
			if !tt.wantDepends && reason != DependencyNone {
				t.Errorf("reason = %q, want %q", reason, DependencyNone)
			}
		})
	}
}

// TestDetectChildDependency_NegativeOverridesPositive verifies that when a
// child's text contains both a verification word and genuine implementation
// language, the negative (implementation) signal wins — a child that verifies
// AND implements is still new work, not a pure verification-only child.
func TestDetectChildDependency_NegativeOverridesPositive(t *testing.T) {
	depends, reason := detectChildDependency("verify the fix, then add the missing validation", "", map[int]bool{})
	if depends {
		t.Errorf("expected mixed verify+add text to NOT be flagged dependent, got depends=%v reason=%q", depends, reason)
	}
}

// TestExecuteSubIssuesTracked_IndependentSiblings_NoMergeWait is the GH-4234
// acceptance criterion: two independent children (no explicit ref, no
// verification-shape language) must yield ZERO merge-wait calls even though a
// merge-waiter is wired — wait_for_merge:false stays the effective default.
func TestExecuteSubIssuesTracked_IndependentSiblings_NoMergeWait(t *testing.T) {
	issues := makeSubIssues(2, 5000)
	// Both children have plain implementation-style descriptions — no
	// dependency markers at all.
	issues[0].Subtask.Description = "Add the new config field."
	issues[1].Subtask.Description = "Implement the new handler."

	callIdx := 0
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		idx := callIdx
		callIdx++
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: true,
			PRUrl:   fmt.Sprintf("https://github.com/owner/repo/pull/%d", 6000+idx),
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)

	var buf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	waitCalls := 0
	runner.SetSubIssueMergeWait(func(ctx context.Context, prNumber int) error {
		waitCalls++
		return nil
	})

	parent := &Task{ID: "GH-4234-IND", Title: "[epic] independent siblings"}

	err := runner.ExecuteSubIssues(context.Background(), parent, issues, "", "")
	if err != nil {
		t.Fatalf("ExecuteSubIssues returned error: %v", err)
	}

	if waitCalls != 0 {
		t.Fatalf("expected 0 merge-wait calls for independent siblings, got %d", waitCalls)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "merge-wait decision: not waiting before next sub-issue") {
		t.Errorf("expected a fail-loud no-wait decision log line, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "dependency_reason=none") {
		t.Errorf("expected no-wait decision log to record dependency_reason=none, got: %s", logOutput)
	}
}

// TestExecuteSubIssuesTracked_ExplicitDependency_WaitsAndSyncs is the GH-4234
// acceptance criterion: a child explicitly declaring "Depends on: #<sibling>"
// waits for that sibling's PR to merge (+ syncMainBranch) before it starts.
func TestExecuteSubIssuesTracked_ExplicitDependency_WaitsAndSyncs(t *testing.T) {
	issues := makeSubIssues(2, 7000)
	issues[1].Subtask.Description += fmt.Sprintf("\n\nDepends on: #%d", issues[0].Number)

	var execOrder []string
	execFn := func(ctx context.Context, task *Task) (*ExecutionResult, error) {
		execOrder = append(execOrder, "exec:"+task.ID)
		return &ExecutionResult{
			TaskID:  task.ID,
			Success: true,
			PRUrl:   "https://github.com/owner/repo/pull/8000",
		}, nil
	}

	runner := newTestRunnerWithExecFunc(execFn)

	var buf bytes.Buffer
	runner.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	waitedPRs := []int{}
	runner.SetSubIssueMergeWait(func(ctx context.Context, prNumber int) error {
		execOrder = append(execOrder, "wait")
		waitedPRs = append(waitedPRs, prNumber)
		return nil
	})

	parent := &Task{ID: "GH-4234-DEP", Title: "[epic] explicit dependency"}

	err := runner.ExecuteSubIssues(context.Background(), parent, issues, "", "")
	if err != nil {
		t.Fatalf("ExecuteSubIssues returned error: %v", err)
	}

	if len(waitedPRs) != 1 || waitedPRs[0] != 8000 {
		t.Fatalf("expected exactly one merge-wait call for PR #8000, got %v", waitedPRs)
	}

	// The wait must happen between the two execs, i.e. before the dependent
	// sibling starts.
	wantOrder := fmt.Sprintf("exec:GH-%d,wait,exec:GH-%d", issues[0].Number, issues[1].Number)
	gotOrder := strings.Join(execOrder, ",")
	if gotOrder != wantOrder {
		t.Errorf("execution order = %q, want %q", gotOrder, wantOrder)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "merge-wait decision: waiting for prior sub-issue PR to merge") {
		t.Errorf("expected a fail-loud wait decision log line, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "dependency_reason=explicit_ref") {
		t.Errorf("expected wait decision log to record dependency_reason=explicit_ref, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "target_pr=8000") {
		t.Errorf("expected wait decision log to record target_pr=8000, got: %s", logOutput)
	}
}
