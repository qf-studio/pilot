package executor

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// TestExecuteDecomposedTask_RegistersPlannedSubtaskTitle is the GH-4339
// regression test for the decomposition create site: when
// executeDecomposedTask materializes a sub-issue execution (e.g.
// "GH-4328-1"), the monitor entry for that task ID must carry the planned
// subtask's title, not fall back to the bare sub-issue ID.
//
// Before the fix, subtask.ID had no monitor entry when its first progress
// callback arrived, so Monitor.UpdateProgress's unknown-taskID fallback
// (monitor.go) created one with Title == ID.
func TestExecuteDecomposedTask_RegistersPlannedSubtaskTitle(t *testing.T) {
	r := &Runner{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor: NewMonitor(),
		executeFunc: func(ctx context.Context, task *Task) (*ExecutionResult, error) {
			return &ExecutionResult{
				TaskID:    task.ID,
				Success:   true,
				CommitSHA: "deadbeef",
			}, nil
		},
	}

	parent := &Task{ID: "GH-4328", Title: "Epic: ship the thing", ProjectPath: "/tmp/does-not-matter"}
	subtasks := []*Task{
		{ID: "GH-4328-1", Title: "feat(api): add rate limiting middleware", ProjectPath: parent.ProjectPath},
		{ID: "GH-4328-2", Title: "test(api): cover rate limiting edge cases", ProjectPath: parent.ProjectPath},
	}

	if _, err := r.executeDecomposedTask(context.Background(), parent, subtasks, parent.ProjectPath); err != nil {
		t.Fatalf("executeDecomposedTask() error = %v", err)
	}

	for _, st := range subtasks {
		state, ok := r.monitor.Get(st.ID)
		if !ok {
			t.Fatalf("monitor has no entry for subtask %s", st.ID)
		}
		if state.Title != st.Title {
			t.Errorf("subtask %s: monitor Title = %q, want planned subtask title %q", st.ID, state.Title, st.Title)
		}
		if state.Title == st.ID {
			t.Errorf("subtask %s: monitor Title fell back to the bare sub-issue ID", st.ID)
		}
	}
}

// TestExecuteDecomposedTask_SubtaskStatusTakenFromOwnResult is the GH-5350
// item-3 regression test: before the fix, executeDecomposedTask started each
// subtask's Monitor entry (StatusRunning, via executeWithOptions'
// Monitor.Start) but never finalized it individually. Dispatcher.
// GetRunningTaskIDs() only tracks top-level dispatched task IDs — never
// in-process subtask IDs — so every subtask's Monitor entry looked
// abandoned to ReconcileDeadOwners' dead-owner sweep and was eventually
// flipped to StatusFailed, even though the daemon log recorded "Subtask
// completed" for it and the parent finished done with a PR (GH-5348/GH-5342
// evidence). The dashboard's convertTaskStatesToDisplay
// (cmd/pilot/commands.go) then rendered every such row as "✗ failed" (red).
//
// This proves each subtask's own Monitor status is finalized from its own
// ExecutionResult as soon as it finishes — done for a delivered commit,
// no_op for a deliberate no-commit run, failed only for a genuine subtask
// failure — and that a successfully-completed subtask's PR link is
// backfilled with the parent's real PR once the parent aggregates it.
func TestExecuteDecomposedTask_SubtaskStatusTakenFromOwnResult(t *testing.T) {
	const finalPRUrl = "https://github.com/qf-studio/pilot/pull/9999"

	r := &Runner{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor: NewMonitor(),
		executeFunc: func(ctx context.Context, task *Task) (*ExecutionResult, error) {
			switch task.ID {
			case "GH-5350-1":
				return &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "aaa111"}, nil
			case "GH-5350-2":
				// TASK-320 B2 no-op shape: !Success with Outcome "no_op".
				return &ExecutionResult{TaskID: task.ID, Success: false, Outcome: "no_op", Error: "no new commit produced"}, nil
			case "GH-5350-3":
				return &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "bbb222", PRUrl: finalPRUrl}, nil
			}
			t.Fatalf("unexpected subtask ID %s", task.ID)
			return nil, nil
		},
	}

	parent := &Task{ID: "GH-5350", Title: "Add rate limiting to API endpoints", ProjectPath: "/tmp/does-not-matter"}
	subtasks := []*Task{
		{ID: "GH-5350-1", Title: "feat(api): add token-bucket middleware", ProjectPath: parent.ProjectPath},
		{ID: "GH-5350-2", Title: "chore(api): verify existing tests still pass", ProjectPath: parent.ProjectPath},
		{ID: "GH-5350-3", Title: "feat(api): wire per-client limits from config", ProjectPath: parent.ProjectPath, CreatePR: true},
	}

	result, err := r.executeDecomposedTask(context.Background(), parent, subtasks, parent.ProjectPath)
	if err != nil {
		t.Fatalf("executeDecomposedTask() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("executeDecomposedTask() Success = false, want true (Error: %s)", result.Error)
	}
	if result.PRUrl != finalPRUrl {
		t.Fatalf("aggregate PRUrl = %q, want %q", result.PRUrl, finalPRUrl)
	}

	wantStatus := map[string]TaskStatus{
		"GH-5350-1": StatusCompleted,
		"GH-5350-2": StatusNoOp,
		"GH-5350-3": StatusCompleted,
	}
	for id, want := range wantStatus {
		state, ok := r.monitor.Get(id)
		if !ok {
			t.Fatalf("monitor has no entry for subtask %s", id)
		}
		if state.Status != want {
			t.Errorf("subtask %s: monitor Status = %q, want %q", id, state.Status, want)
		}
	}

	// Every subtask whose own result succeeded must inherit the parent's real
	// PR link once known — not stay linkless (GH-5350 item 3).
	for _, id := range []string{"GH-5350-1", "GH-5350-3"} {
		state, _ := r.monitor.Get(id)
		if state.PRUrl != finalPRUrl {
			t.Errorf("subtask %s: monitor PRUrl = %q, want backfilled parent PR %q", id, state.PRUrl, finalPRUrl)
		}
	}

	// The no-op subtask never delivered a commit — it must not be backfilled
	// with the parent's PR link, and must stay the non-failure StatusNoOp
	// terminal state (not silently promoted to done).
	noOpState, _ := r.monitor.Get("GH-5350-2")
	if noOpState.PRUrl != "" {
		t.Errorf("no-op subtask GH-5350-2: monitor PRUrl = %q, want empty", noOpState.PRUrl)
	}
}

// TestExecuteDecomposedTask_FailedSubtaskStatusIsFailed proves a subtask
// that genuinely fails (not the TASK-320 B2 no-op shape) is finalized as
// StatusFailed with its own error — the counterpart to the done/no_op cases
// above, so "only a genuine subtask failure shows failed" (GH-5350
// acceptance criteria) is covered on both sides.
func TestExecuteDecomposedTask_FailedSubtaskStatusIsFailed(t *testing.T) {
	r := &Runner{
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor: NewMonitor(),
		executeFunc: func(ctx context.Context, task *Task) (*ExecutionResult, error) {
			return &ExecutionResult{TaskID: task.ID, Success: false, Error: "quality gate failed: lint"}, nil
		},
	}

	parent := &Task{ID: "GH-5350-F", Title: "Add rate limiting to API endpoints", ProjectPath: "/tmp/does-not-matter"}
	subtasks := []*Task{
		{ID: "GH-5350-F-1", Title: "feat(api): add token-bucket middleware", ProjectPath: parent.ProjectPath},
	}

	result, err := r.executeDecomposedTask(context.Background(), parent, subtasks, parent.ProjectPath)
	if err != nil {
		t.Fatalf("executeDecomposedTask() error = %v", err)
	}
	if result.Success {
		t.Fatal("executeDecomposedTask() Success = true, want false for a genuinely failed subtask")
	}

	state, ok := r.monitor.Get("GH-5350-F-1")
	if !ok {
		t.Fatal("monitor has no entry for subtask GH-5350-F-1")
	}
	if state.Status != StatusFailed {
		t.Errorf("subtask GH-5350-F-1: monitor Status = %q, want %q", state.Status, StatusFailed)
	}
	if state.Error != "quality gate failed: lint" {
		t.Errorf("subtask GH-5350-F-1: monitor Error = %q, want the subtask's own error", state.Error)
	}
}
