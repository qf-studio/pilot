package executor

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// seedNoProgressRow writes one terminal execution row at generation gen with
// the given status/error, the shape a finished run leaves behind.
func seedNoProgressRow(t *testing.T, store *memory.Store, task *Task, gen int, status Status, errStr string) string {
	t.Helper()
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning, gen)
	if err != nil {
		t.Fatalf("setup: generation %d Begin failed: %v", gen, err)
	}
	if err := store.UpdateExecutionStatus(execID, string(status), errStr); err != nil {
		t.Fatalf("setup: failed to mark generation %d as %s: %v", gen, status, err)
	}
	return execID
}

// latestClaimStatus returns the generation and execution row the claim ledger
// currently points at.
func latestClaimStatus(t *testing.T, store *memory.Store, task *Task) (int, *memory.Execution) {
	t.Helper()
	gen, execID, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath)
	if err != nil || !found {
		t.Fatalf("LatestClaimGeneration: found=%v err=%v", found, err)
	}
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	return gen, exec
}

const noCommitErr = "no new commit produced — post-push SHA matches base branch"

// TestDispatcher_NoProgressRepickCap_StallsOnThirdPick covers GH-5800: two
// consecutive declined / no-commit no_op runs (in any mix) stall the third
// pick with a comment naming the outcome and asking for a rescope, instead of
// riding the generic backoff to dispatcherRepickHardCap.
func TestDispatcher_NoProgressRepickCap_StallsOnThirdPick(t *testing.T) {
	tests := []struct {
		name        string
		first       Status
		firstErr    string
		second      Status
		secondErr   string
		wantOutcome string
	}{
		{"two declined", ExecStatusDeclined, "DECLINED: body does not match page", ExecStatusDeclined, "DECLINED: body does not match page", "declined"},
		{"declined then no-commit no_op", ExecStatusDeclined, "DECLINED: nothing to change", ExecStatusNoOp, noCommitErr, "no_op (no new commit produced)"},
		{"no-commit no_op then declined", ExecStatusNoOp, noCommitErr, ExecStatusDeclined, "DECLINED: nothing to change", "declined"},
		{"two no-commit no_op", ExecStatusNoOp, noCommitErr, ExecStatusNoOp, noCommitErr, "no_op (no new commit produced)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			// SourceAdapter left empty so surfaceStalledIssue skips the gh CLI.
			task := &Task{ID: "GH-5800", ProjectPath: "/project-gh-5800"}
			seedNoProgressRow(t, store, task, 0, tt.first, tt.firstErr)
			dispatcher := NewDispatcher(store, NewRunner(), nil)

			// Second pick: only one no-progress run so far — still granted.
			execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
			if err != nil || execID == "" {
				t.Fatalf("second pick should be granted after one no-progress run, got execID=%q err=%v", execID, err)
			}
			if err := store.UpdateExecutionStatus(execID, string(tt.second), tt.secondErr); err != nil {
				t.Fatalf("setup: finishing second run: %v", err)
			}

			// Third pick: two consecutive no-progress runs — stalls.
			execID, err = dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
			if err != nil {
				t.Fatalf("third pick: unexpected error: %v", err)
			}
			if execID != "" {
				t.Fatalf("third pick must be dropped, got new execution %q", execID)
			}
			gen, claimed := latestClaimStatus(t, store, task)
			if gen != 1 {
				t.Errorf("no third generation may be claimed, latest generation = %d", gen)
			}
			if claimed.Status != string(ExecStatusStalled) {
				t.Fatalf("claimed execution status = %q, want stalled", claimed.Status)
			}
			for _, want := range []string{noProgressRepickReasonPrefix, tt.wantOutcome, "rescope"} {
				if !strings.Contains(claimed.Error, want) {
					t.Errorf("stall reason %q missing %q", claimed.Error, want)
				}
			}
			if comment := stalledIssueComment(claimed.Error, 5800); !strings.Contains(comment, "rescope") || !strings.Contains(comment, tt.wantOutcome) {
				t.Errorf("issue comment does not name the outcome and the rescope: %q", comment)
			}

			// Sticky: later poll ticks stay dropped (no free stall carve-out
			// generation) and do not rewrite the hold.
			for i := 0; i < 2; i++ {
				execID, err = dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
				if err != nil || execID != "" {
					t.Fatalf("poll tick %d after stall: want dropped, got execID=%q err=%v", i, execID, err)
				}
			}
			if gen, again := latestClaimStatus(t, store, task); gen != 1 || again.Error != claimed.Error {
				t.Errorf("hold changed on later ticks: gen=%d error=%q", gen, again.Error)
			}
		})
	}
}

// TestDispatcher_NoProgressRepickCap_LabelCycleRearms: the cap is not
// terminal-done. A single declined run is re-picked normally, and a stalled
// task re-armed by a label cycle (stalled row demoted to failed, backoff
// cleared, as cmd/pilot's tryRearmStalled does) is picked again.
func TestDispatcher_NoProgressRepickCap_LabelCycleRearms(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-5800", ProjectPath: "/project-gh-5800-rearm"}
	key := repickBackoffKey(task.ProjectPath, task.ID)
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	// One declined run, then a label-cycle: not stalled, picked.
	seedNoProgressRow(t, store, task, 0, ExecStatusDeclined, "DECLINED: body does not match page")
	execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("one declined run must be re-picked, got execID=%q err=%v", execID, err)
	}
	if err := store.UpdateExecutionStatus(execID, string(ExecStatusDeclined), "DECLINED: body does not match page"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Two declined runs stall the next pick.
	if execID, err = dispatcher.beginWithGenerationRetry(task, ExecStatusQueued); err != nil || execID != "" {
		t.Fatalf("third pick must stall, got execID=%q err=%v", execID, err)
	}
	if _, claimed := latestClaimStatus(t, store, task); claimed.Status != string(ExecStatusStalled) {
		t.Fatalf("want stalled, got %q", claimed.Status)
	}

	// Label cycle after the body is fixed: re-arm demotes stalled -> failed.
	if err := store.ReclassifyStalledForRearm(task.ID, task.ProjectPath, "GH-5212: re-armed by label cycle"); err != nil {
		t.Fatalf("ReclassifyStalledForRearm: %v", err)
	}
	if err := dispatcher.ClearRepickBackoffState(key); err != nil {
		t.Fatalf("ClearRepickBackoffState: %v", err)
	}
	execID, err = dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil || execID == "" {
		t.Fatalf("re-armed task must be picked, got execID=%q err=%v", execID, err)
	}
	if gen, _ := latestClaimStatus(t, store, task); gen != 2 {
		t.Errorf("re-armed pick should claim generation 2, got %d", gen)
	}
}

// TestDispatcher_NoProgressRepickCap_UnchangedOutcomes: an empty-error no_op
// stays terminal-done (dropped, never stalled), and infra runs are not
// counted against the no-progress cap.
func TestDispatcher_NoProgressRepickCap_UnchangedOutcomes(t *testing.T) {
	t.Run("empty-error no_op stays terminal-done", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-5800", ProjectPath: "/project-gh-5800-noop"}
		seedNoProgressRow(t, store, task, 0, ExecStatusNoOp, "")
		dispatcher := NewDispatcher(store, NewRunner(), nil)

		execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
		if err != nil || execID != "" {
			t.Fatalf("done no_op must be dropped, got execID=%q err=%v", execID, err)
		}
		if _, claimed := latestClaimStatus(t, store, task); claimed.Status != string(ExecStatusNoOp) || claimed.Error != "" {
			t.Errorf("no_op row must be untouched, got status=%q error=%q", claimed.Status, claimed.Error)
		}
	})

	t.Run("no_op with other error is not counted", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-5800", ProjectPath: "/project-gh-5800-noop-other"}
		seedNoProgressRow(t, store, task, 0, ExecStatusNoOp, "no_changes: already on base")
		seedNoProgressRow(t, store, task, 1, ExecStatusNoOp, "no_changes: already on base")
		dispatcher := NewDispatcher(store, NewRunner(), nil)

		if n, _ := dispatcher.consecutiveNoProgressOutcomes(task.ID, task.ProjectPath); n != 0 {
			t.Errorf("no_op without the no-commit marker must not count, got %d", n)
		}
	})

	t.Run("infra outcome keeps its own cap", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-5800", ProjectPath: "/project-gh-5800-infra"}
		seedNoProgressRow(t, store, task, 0, ExecStatusInfra, "preflight deadlock")
		seedNoProgressRow(t, store, task, 1, ExecStatusInfra, "preflight deadlock")
		seedNoProgressRow(t, store, task, 2, ExecStatusInfra, "preflight deadlock")
		dispatcher := NewDispatcher(store, NewRunner(), nil)

		execID, err := dispatcher.beginWithGenerationRetry(task, ExecStatusQueued)
		if err != nil || execID == "" {
			t.Fatalf("infra retries must still be granted, got execID=%q err=%v", execID, err)
		}
		if gen, _ := latestClaimStatus(t, store, task); gen != 3 {
			t.Errorf("want generation 3, got %d", gen)
		}
	})

	t.Run("failed then declined counts one", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-5800", ProjectPath: "/project-gh-5800-mixed"}
		seedNoProgressRow(t, store, task, 0, ExecStatusFailed, "quality gate failed")
		seedNoProgressRow(t, store, task, 1, ExecStatusDeclined, "DECLINED: nothing to change")
		dispatcher := NewDispatcher(store, NewRunner(), nil)

		if n, _ := dispatcher.consecutiveNoProgressOutcomes(task.ID, task.ProjectPath); n != 1 {
			t.Errorf("only the trailing declined run counts, got %d", n)
		}
	})
}
