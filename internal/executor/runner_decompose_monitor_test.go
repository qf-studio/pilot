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
