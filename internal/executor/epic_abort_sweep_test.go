package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// writeFakeGhPRCreateFailing writes a fake "gh" binary that answers `gh pr
// list ...` with an empty array (no merged/open PR short-circuit) and fails
// `gh pr create ...` outright (exit 1, no "already exists" in the output) —
// simulating a genuine PR-creation failure (rate limit, transient GitHub API
// error, ...) distinct from a push failure or a title-normalization failure.
func writeFakeGhPRCreateFailing(t *testing.T, fakeBin string) {
	t.Helper()
	script := `#!/bin/sh
case "$*" in
  *"pr list"*) echo "[]" ;;
  *"pr create"*) echo "gh: unexpected server error" >&2; exit 1 ;;
  *) echo "[]" ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

// setUpFakeGhPRCreateFailingPATH prepends writeFakeGhPRCreateFailing's fake
// "gh" binary to PATH for the duration of the test.
func setUpFakeGhPRCreateFailingPATH(t *testing.T) {
	t.Helper()
	fakeBin := t.TempDir()
	writeFakeGhPRCreateFailing(t, fakeBin)
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

// TestFinalizeEpicBranchPR_AbortSweepsRunningChildren is the GH-4561
// acceptance test: whichever way finalizeEpicBranchPR terminally aborts the
// epic parent (push failure, title rejection, or PR-creation failure), any
// child sub-issue execution still non-terminal ("running" here, simulating a
// child independently picked up by the poller while the parent's own
// sequential loop was finalizing) must be swept to "stalled" carrying the
// parent's failure text — instead of being left claimed forever with no
// owner left to reconcile it.
//
// Each case verifies three things the task spec calls out explicitly:
//  1. the child's execution transitions to "stalled" with an error string
//     naming the parent epic and its failure reason (reusing
//     ExecutionLifecycle.Finish — no raw status UPDATEs, TASK-404/FK-787);
//  2. the child's execution_events ladder is preserved (the earlier
//     queued/running events are still there, appended-to, not replaced); and
//  3. re-dispatch is admitted — Dispatcher.nextRetryGeneration grants a
//     fresh generation for the child once its stalled claim is examined,
//     because "stalled" is itself a terminal status (dispatcher.go's
//     terminalExecutionStatuses) — the sweep's transition IS the claim
//     release, no separate release call is needed.
func TestFinalizeEpicBranchPR_AbortSweepsRunningChildren(t *testing.T) {
	tests := []struct {
		name          string
		setUp         func(t *testing.T) (dir string, task *Task)
		wantErrSubstr string // substring finalizeEpicBranchPR's result.Error must contain
	}{
		{
			name: "push failure",
			setUp: func(t *testing.T) (string, *Task) {
				dir := initRepoWithCommitTask359(t)
				runGit(t, dir, "checkout", "-b", "feature")
				if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("work\n"), 0644); err != nil {
					t.Fatalf("write: %v", err)
				}
				runGit(t, dir, "add", "f.txt")
				runGit(t, dir, "commit", "-m", "feature work")
				task := &Task{ID: "GH-431", Title: "feat: epic work", Description: "d", Branch: "feature", BaseBranch: "main", CreatePR: true}
				return dir, task
			},
			wantErrSubstr: "epic branch push failed",
		},
		{
			name: "title rejection",
			setUp: func(t *testing.T) (string, *Task) {
				dir := initRepoWithRemoteAndFeatureBranch(t, "pilot/GH-431-epic")
				task := &Task{ID: "GH-431", Title: "   ", Description: "d", Branch: "pilot/GH-431-epic", BaseBranch: "main", CreatePR: true}
				return dir, task
			},
			wantErrSubstr: "epic PR creation failed",
		},
		{
			name: "PR creation failure",
			setUp: func(t *testing.T) (string, *Task) {
				setUpFakeGhPRCreateFailingPATH(t)
				dir := initRepoWithRemoteAndFeatureBranch(t, "pilot/GH-431-epic2")
				task := &Task{ID: "GH-431", Title: "feat: epic work", Description: "d", Branch: "pilot/GH-431-epic2", BaseBranch: "main", CreatePR: true}
				return dir, task
			},
			wantErrSubstr: "epic PR creation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, task := tt.setUp(t)

			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("memory.NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			task.ProjectPath = dir
			lifecycle := NewExecutionLifecycle(store)

			// Seed the parent's own execution row with a StageDecomposed event
			// naming the child — this is the record executeSubIssuesTracked
			// leaves behind, and what sweepEpicChildrenOnAbort's
			// GetDecomposedChildTaskIDs lookup parses to find the child.
			parentExecID, err := lifecycle.Begin(task, ExecStatusRunning)
			if err != nil {
				t.Fatalf("Begin(parent): %v", err)
			}
			if err := store.RecordExecutionEvent(parentExecID, memory.StageDecomposed, "decomposed into 1 children: #101"); err != nil {
				t.Fatalf("RecordExecutionEvent(StageDecomposed): %v", err)
			}

			// Seed the child's execution row as "running" — simulating the
			// poller having independently picked up and started GH-101 while
			// the parent's own finalize step is running concurrently.
			childTask := &Task{ID: "GH-101", ProjectPath: dir}
			childExecID, err := lifecycle.Begin(childTask, ExecStatusQueued)
			if err != nil {
				t.Fatalf("Begin(child): %v", err)
			}
			if err := store.RecordExecutionEvent(childExecID, memory.StageQueued, "queued"); err != nil {
				t.Fatalf("RecordExecutionEvent(StageQueued): %v", err)
			}
			if err := lifecycle.Transition(childExecID, ExecStatusRunning); err != nil {
				t.Fatalf("Transition(child running): %v", err)
			}
			if err := store.RecordExecutionEvent(childExecID, memory.StageRunning, "running"); err != nil {
				t.Fatalf("RecordExecutionEvent(StageRunning): %v", err)
			}

			r := newSilentRunnerTask359()
			r.logStore = store

			result := &ExecutionResult{TaskID: task.ID, Success: true, IsEpic: true}
			r.finalizeEpicBranchPR(context.Background(), task, NewGitOperations(dir), result, nil)

			if result.Success {
				t.Fatalf("expected epic parent finalize to fail, got Success=true")
			}
			if !strings.Contains(result.Error, tt.wantErrSubstr) {
				t.Fatalf("result.Error = %q, want substring %q", result.Error, tt.wantErrSubstr)
			}

			// (1) Child transitioned to "stalled" with the parent-failure text.
			childExec, err := store.GetExecution(childExecID)
			if err != nil {
				t.Fatalf("GetExecution(child): %v", err)
			}
			if childExec.Status != string(ExecStatusStalled) {
				t.Fatalf("child status = %q, want %q", childExec.Status, ExecStatusStalled)
			}
			wantPrefix := fmt.Sprintf("parent epic %s aborted:", task.ID)
			if !strings.HasPrefix(childExec.Error, wantPrefix) {
				t.Errorf("child error = %q, want prefix %q", childExec.Error, wantPrefix)
			}
			if !strings.Contains(childExec.Error, tt.wantErrSubstr) {
				t.Errorf("child error = %q, want it to carry the parent's failure text %q", childExec.Error, tt.wantErrSubstr)
			}

			// (2) Ladder events preserved: the earlier queued/running events
			// are still there, with a stalled event appended — not replaced.
			events, err := store.ListExecutionEvents(childExecID)
			if err != nil {
				t.Fatalf("ListExecutionEvents: %v", err)
			}
			var stages []string
			for _, e := range events {
				stages = append(stages, string(e.Stage))
			}
			wantStages := []string{string(memory.StageQueued), string(memory.StageRunning), string(memory.StageStalled)}
			if len(stages) != len(wantStages) {
				t.Fatalf("child event ladder = %v, want %v", stages, wantStages)
			}
			for i, want := range wantStages {
				if stages[i] != want {
					t.Errorf("child event ladder[%d] = %q, want %q (full ladder: %v)", i, stages[i], want, stages)
				}
			}

			// (3) Re-dispatch admitted: nextRetryGeneration must grant a fresh
			// generation for the child now that its claimed execution is
			// terminal (stalled) and the task was never marked done.
			dispatcher := NewDispatcher(store, NewRunner(), nil)
			gen, retry, err := dispatcher.nextRetryGeneration(childTask.ID, childTask.ProjectPath)
			if err != nil {
				t.Fatalf("nextRetryGeneration: %v", err)
			}
			if !retry {
				t.Fatalf("expected retry=true (child re-dispatchable) after stalling, got retry=false")
			}
			if gen != 1 {
				t.Errorf("expected next generation 1, got %d", gen)
			}
		})
	}
}

// TestSweepStalledEpicChildren_SkipsAlreadyTerminalChildren verifies that a
// child execution which already reached a terminal status (e.g. "completed")
// before the parent's abort is left untouched — the sweep must not clobber a
// child that already shipped its own outcome.
func TestSweepStalledEpicChildren_SkipsAlreadyTerminalChildren(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const projectPath = "/proj"
	lifecycle := NewExecutionLifecycle(store)

	childTask := &Task{ID: "GH-102", ProjectPath: projectPath}
	childExecID, err := lifecycle.Begin(childTask, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin(child): %v", err)
	}
	if err := store.MarkExecutionCompleted(childExecID, "https://github.com/o/r/pull/1", "sha123", 500); err != nil {
		t.Fatalf("MarkExecutionCompleted: %v", err)
	}

	r := newSilentRunnerTask359()
	r.logStore = store

	r.sweepStalledEpicChildren("GH-431", projectPath, []string{childTask.ID}, "epic PR creation failed: boom")

	childExec, err := store.GetExecution(childExecID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if childExec.Status != string(ExecStatusCompleted) {
		t.Errorf("child status = %q, want unchanged %q", childExec.Status, ExecStatusCompleted)
	}
	if childExec.PRUrl != "https://github.com/o/r/pull/1" {
		t.Errorf("child PRUrl clobbered: got %q", childExec.PRUrl)
	}
}
