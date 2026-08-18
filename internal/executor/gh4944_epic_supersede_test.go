package executor

import (
	"context"
	"fmt"
	"testing"
)

// GH-4944 regression suite: a plain external close of a queued epic child
// (no `pilot-superseded` label) previously fell through to full execution —
// the sequential sub-issue loop dispatched the CLOSED child anyway, the
// executor ran to completion, and only aborted at PR-creation time
// (checkIssueSupersededBeforePR). That single abort failed the WHOLE epic
// run, triggering a parent retry and a full nondeterministic
// re-decomposition (live specimen: #4929 run 1 / child #4932, 2026-08-18;
// pitfall memory pilot-issue-missing-no-decompose-fragments-single-fix).
//
// These tests cover both interception points the fix adds:
//   - the pre-dispatch check in executeSubIssuesTracked, before Begin()/
//     executeFunc ever runs for the child (no executor spawn at all)
//   - the mid-execution PR-create backstop (checkIssueSupersededBeforePR),
//     surfaced back through result.Outcome == "superseded" and now handled
//     as a supersede-and-continue instead of a hard failure

// TestExecuteSubIssuesTracked_ChildClosedBeforeDispatch is table-driven over
// which child (if any) is closed at dispatch time, with and without the
// `pilot-superseded` label, verifying: no executor spawn for the closed
// child, its terminal state is "superseded", and the run does not fail.
func TestExecuteSubIssuesTracked_ChildClosedBeforeDispatch(t *testing.T) {
	tests := []struct {
		name          string
		closedTaskIDs map[string]bool
		labels        map[string][]string
		wantStates    []string
		wantExecuted  []string // task IDs expected to reach executeFunc, in order
	}{
		{
			name:         "no children closed — all execute normally",
			wantStates:   []string{"completed", "completed", "completed"},
			wantExecuted: []string{"GH-100", "GH-101", "GH-102"},
		},
		{
			name:          "middle child closed externally, no label — superseded, run continues",
			closedTaskIDs: map[string]bool{"GH-101": true},
			wantStates:    []string{"completed", "superseded", "completed"},
			wantExecuted:  []string{"GH-100", "GH-102"},
		},
		{
			name:          "middle child closed with pilot-superseded label — behavior unchanged",
			closedTaskIDs: map[string]bool{"GH-101": true},
			labels:        map[string][]string{"GH-101": {"pilot-superseded", "pilot"}},
			wantStates:    []string{"completed", "superseded", "completed"},
			wantExecuted:  []string{"GH-100", "GH-102"},
		},
		{
			name:          "first child closed externally — subsequent children still dispatch",
			closedTaskIDs: map[string]bool{"GH-100": true},
			wantStates:    []string{"superseded", "completed", "completed"},
			wantExecuted:  []string{"GH-101", "GH-102"},
		},
		{
			name:          "last child closed externally — earlier children unaffected",
			closedTaskIDs: map[string]bool{"GH-102": true},
			wantStates:    []string{"completed", "completed", "superseded"},
			wantExecuted:  []string{"GH-100", "GH-101"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := makeSubIssues(3, 100)

			stubFetchIssueState(t, func(_ context.Context, _ *Runner, task *Task, _ string) (IssueState, error) {
				closed := tt.closedTaskIDs[task.ID]
				return IssueState{Closed: closed, Labels: tt.labels[task.ID]}, nil
			})

			var executed []string
			execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
				executed = append(executed, task.ID)
				return &ExecutionResult{
					TaskID:    task.ID,
					Success:   true,
					PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%s", task.ID),
					CommitSHA: "sha-" + task.ID,
				}, nil
			}
			runner := newTestRunnerWithExecFunc(execFn)

			parent := &Task{ID: "GH-50", Title: "[epic] GH-4944 pre-dispatch supersede test"}
			childStates, _, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, parent.ProjectPath, "")
			if err != nil {
				t.Fatalf("expected the run to continue past a superseded child, got error: %v", err)
			}

			if len(childStates) != len(tt.wantStates) {
				t.Fatalf("childStates = %v, want %v", childStates, tt.wantStates)
			}
			for i, want := range tt.wantStates {
				if childStates[i] != want {
					t.Errorf("childStates[%d] = %q, want %q (full: %v)", i, childStates[i], want, childStates)
				}
			}

			if len(executed) != len(tt.wantExecuted) {
				t.Fatalf("executed = %v, want %v (no executor should spawn for a closed child)", executed, tt.wantExecuted)
			}
			for i, want := range tt.wantExecuted {
				if executed[i] != want {
					t.Errorf("executed[%d] = %q, want %q (full: %v)", i, executed[i], want, executed)
				}
			}
		})
	}
}

// TestExecuteSubIssuesTracked_ChildClosedMidExecution verifies the
// PR-creation backstop: a child that passes the pre-dispatch check (issue
// still open) but is closed externally WHILE it executes surfaces
// result.Outcome == "superseded" from checkIssueSupersededBeforePR. The
// epic loop must treat that the same as the pre-dispatch case — supersede
// and continue to the next child — instead of failing the whole run.
func TestExecuteSubIssuesTracked_ChildClosedMidExecution(t *testing.T) {
	issues := makeSubIssues(3, 200)

	// No child is closed at pre-dispatch time — the mid-execution close is
	// simulated purely through the executeFunc result below.
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})

	var executed []string
	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		executed = append(executed, task.ID)
		if task.ID == "GH-201" {
			// Mirrors exactly what checkIssueSupersededBeforePR (runner.go)
			// produces when the issue closed during this child's run: a
			// refused PR, Success=false, Outcome="superseded".
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Outcome: "superseded",
				Error:   "issue closed before PR creation (superseded_label=false, labels=[pilot])",
			}, nil
		}
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%s", task.ID),
			CommitSHA: "sha-" + task.ID,
		}, nil
	}
	runner := newTestRunnerWithExecFunc(execFn)

	parent := &Task{ID: "GH-60", Title: "[epic] GH-4944 mid-execution supersede test"}
	childStates, _, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, parent.ProjectPath, "")
	if err != nil {
		t.Fatalf("expected the run to continue past a mid-execution supersede, got error: %v", err)
	}

	wantExecuted := []string{"GH-200", "GH-201", "GH-202"}
	if len(executed) != len(wantExecuted) {
		t.Fatalf("executed = %v, want %v", executed, wantExecuted)
	}
	for i, want := range wantExecuted {
		if executed[i] != want {
			t.Errorf("executed[%d] = %q, want %q (full: %v)", i, executed[i], want, executed)
		}
	}

	wantStates := []string{"completed", "superseded", "completed"}
	if len(childStates) != len(wantStates) {
		t.Fatalf("childStates = %v, want %v", childStates, wantStates)
	}
	for i, want := range wantStates {
		if childStates[i] != want {
			t.Errorf("childStates[%d] = %q, want %q (full: %v)", i, childStates[i], want, childStates)
		}
	}
}

// TestExecuteSubIssuesTracked_GenuineFailureStillAbortsEpic guards against
// over-broadening the GH-4944 fix: a child that fails for a real reason
// (Outcome == "" / anything other than "no_op" or "superseded") must still
// abort the whole epic run exactly as before.
func TestExecuteSubIssuesTracked_GenuineFailureStillAbortsEpic(t *testing.T) {
	issues := makeSubIssues(3, 300)

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})

	var executed []string
	execFn := func(_ context.Context, task *Task) (*ExecutionResult, error) {
		executed = append(executed, task.ID)
		if task.ID == "GH-301" {
			return &ExecutionResult{
				TaskID:  task.ID,
				Success: false,
				Error:   "CI check failed: lint errors",
			}, nil
		}
		return &ExecutionResult{
			TaskID:    task.ID,
			Success:   true,
			PRUrl:     fmt.Sprintf("https://github.com/owner/repo/pull/%s", task.ID),
			CommitSHA: "sha-" + task.ID,
		}, nil
	}
	runner := newTestRunnerWithExecFunc(execFn)

	parent := &Task{ID: "GH-70", Title: "[epic] GH-4944 genuine failure guard test"}
	_, _, err := runner.executeSubIssuesTracked(context.Background(), parent, issues, parent.ProjectPath, "")
	if err == nil {
		t.Fatal("expected a genuine child failure to still abort the epic run")
	}

	wantExecuted := []string{"GH-300", "GH-301"}
	if len(executed) != len(wantExecuted) {
		t.Fatalf("executed = %v, want %v (third child must not run after the abort)", executed, wantExecuted)
	}
}
