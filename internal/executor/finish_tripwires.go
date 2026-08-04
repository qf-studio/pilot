package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// Finish-tripwire dead-man tracker names (TASK-441 L5, GH-4716). Each guards
// one of the four post-task invariants the sweep checks; registering them
// separately (rather than one shared tracker) means a sustained run of, say,
// worktree-pruning violations pages independently of a root-clean violation
// streak, instead of one check's failures masking another's successes in a
// shared counter. Registered at daemon startup (cmd/pilot/main.go's
// runPollingMode, internal/pilot/pilot.go's initAlerts) against
// alerts.AlertTypeFinishTripwireFailureStreak.
const (
	// FinishTripwireRootCleanTrackerName guards the runselfreview-runs-in-
	// repo-root-phantom-reimplementation pitfall class: task.ProjectPath
	// left with a staged/unstaged diff after a terminal write.
	FinishTripwireRootCleanTrackerName = "finish_tripwire_root_clean"
	// FinishTripwireLabelLifecycleTrackerName guards the
	// poller-labels-removed-log-means-never-applied / GH-4687 class: an
	// adapter-dispatched execution reaching a terminal status with no
	// execution_events ledger at all — the "wired to nothing" shape.
	FinishTripwireLabelLifecycleTrackerName = "finish_tripwire_label_lifecycle"
	// FinishTripwireChildrenTerminalTrackerName guards the
	// epic-decompose-discards-child-work pitfall class: a decomposed
	// parent's own recorded children not all reaching a terminal status.
	FinishTripwireChildrenTerminalTrackerName = "finish_tripwire_children_terminal"
	// FinishTripwireWorktreeTrackerName guards two shapes of the same
	// stranded-work class: an orphaned worktree directory still on disk
	// after a terminal write, or real commits with no PR for a task that
	// requested one.
	FinishTripwireWorktreeTrackerName = "finish_tripwire_worktree"
)

// FinishTripwireTrackerNames lists every tracker name the finish-tripwire
// sweep emits against, so daemon-startup wiring can register all four in one
// loop (see cmd/pilot/main.go's runPollingMode, internal/pilot/pilot.go's
// initAlerts) instead of hand-listing them a second time and risking drift
// if a check is ever added or removed.
var FinishTripwireTrackerNames = []string{
	FinishTripwireRootCleanTrackerName,
	FinishTripwireLabelLifecycleTrackerName,
	FinishTripwireChildrenTerminalTrackerName,
	FinishTripwireWorktreeTrackerName,
}

// finishTripwireGitTimeout bounds the git invocations checkRootClean makes.
// The sweep's own constraint is "no network calls in the hot path" — `git
// status --porcelain` is local-only, but a short timeout still guards
// against an unexpected hang (e.g. a stale index.lock) turning a cheap
// diagnostic into a stuck Persist call.
const finishTripwireGitTimeout = 5 * time.Second

// tripwireCheckResult is one check's verdict: violated and, when true, a
// human-readable reason logged and forwarded as the DeadManTracker failure's
// Error field (mirrors runSelfReview's AlertEventTypeDeadManFailure usage).
type tripwireCheckResult struct {
	violated bool
	reason   string
}

// runFinishTripwireSweep runs the four TASK-441 L5 post-terminal invariant
// checks for execID and reports each through processor as a
// alerts.DeadManTracker attempt/success/failure (relayed via the
// AlertEventProcessor mirror — see alerts.go's doc comment for why executor
// can't import alerts directly). It never blocks or fails the caller: a
// panic anywhere in the sweep (or the checks it calls) is recovered and
// logged, never propagated. store == nil or execID == "" is a no-op,
// mirroring every other nil-store guard in this package.
//
// Called from ExecutionLifecycle.Persist, which is always a terminal write
// (see Persist's own doc comment) — so every call here is, by construction,
// a "terminal paths only" invocation; Transition (the non-terminal path)
// never reaches this function.
func runFinishTripwireSweep(store *memory.Store, processor AlertEventProcessor, execID string) {
	if store == nil || execID == "" {
		return
	}

	log := logging.WithComponent("executor.finish_tripwires")
	defer func() {
		if r := recover(); r != nil {
			log.Error("finish tripwire sweep panicked — recovered, terminal write is unaffected",
				"execution_id", execID, "panic", r)
		}
	}()

	row, err := store.GetExecution(execID)
	if err != nil || row == nil {
		// Nothing to sweep against — this is unexpected (Persist just wrote
		// this row) but not itself a finding; the write this sweep follows
		// already happened and already returned its own error, if any.
		log.Debug("finish tripwire sweep: execution row not found, skipping",
			"execution_id", execID, "error", err)
		return
	}

	emitTripwireResult(log, processor, row, FinishTripwireRootCleanTrackerName, checkRootClean(row))
	emitTripwireResult(log, processor, row, FinishTripwireLabelLifecycleTrackerName, checkLabelLifecycle(store, row))
	emitTripwireResult(log, processor, row, FinishTripwireChildrenTerminalTrackerName, checkChildrenTerminal(store, row))
	emitTripwireResult(log, processor, row, FinishTripwireWorktreeTrackerName, checkWorktreePruned(row))
}

// emitTripwireResult records one check's attempt, then its success or
// failure, both as a log line and — when processor is non-nil — as the
// matching AlertEventTypeDeadMan{Attempt,Success,Failure} relay so the
// tracker's own consecutive-failure streak (and eventual alert) reflects it.
// Mirrors runSelfReview's exact attempt/success/failure emission shape
// (runner.go) so this sweep's trackers behave identically to the L2
// (self-review/label-lifecycle) trackers already in production.
func emitTripwireResult(log *slog.Logger, processor AlertEventProcessor, row *memory.Execution, tracker string, result tripwireCheckResult) {
	now := time.Now()
	emitTripwireAlertEvent(processor, AlertEvent{
		Type:      AlertEventTypeDeadManAttempt,
		TaskID:    row.TaskID,
		Metadata:  map[string]string{"tracker": tracker, "execution_id": row.ID},
		Timestamp: now,
	})

	if !result.violated {
		emitTripwireAlertEvent(processor, AlertEvent{
			Type:      AlertEventTypeDeadManSuccess,
			TaskID:    row.TaskID,
			Metadata:  map[string]string{"tracker": tracker, "execution_id": row.ID},
			Timestamp: now,
		})
		return
	}

	log.Warn("finish tripwire violation",
		"tracker", tracker, "execution_id", row.ID, "task_id", row.TaskID, "reason", result.reason)
	emitTripwireAlertEvent(processor, AlertEvent{
		Type:      AlertEventTypeDeadManFailure,
		TaskID:    row.TaskID,
		Error:     result.reason,
		Metadata:  map[string]string{"tracker": tracker, "execution_id": row.ID},
		Timestamp: now,
	})
}

// emitTripwireAlertEvent is runFinishTripwireSweep's nil-safe send: unlike
// Runner.emitAlertEvent, this has no *Runner (and no warn-once state) to
// hang a log line off — an unwired processor here has already been logged
// once at startup when the daemon chose not to configure alerts, so a
// second, per-sweep warning would only add noise.
func emitTripwireAlertEvent(processor AlertEventProcessor, event AlertEvent) {
	if processor == nil {
		return
	}
	processor.ProcessEvent(event)
}

// checkRootClean guards the runselfreview-runs-in-repo-root-phantom-
// reimplementation pitfall class: any staged or unstaged diff left in
// row.ProjectPath after a terminal write is the observable symptom of a
// backend session that ran against the shared repo root instead of its own
// worktree (or any other path that leaves the root dirty). A git invocation
// error (missing directory, not a repo, git unavailable) is not itself a
// finding — there's nothing observable from here to report.
func checkRootClean(row *memory.Execution) tripwireCheckResult {
	if row.ProjectPath == "" {
		return tripwireCheckResult{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), finishTripwireGitTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "git", "-C", row.ProjectPath, "status", "--porcelain").Output()
	if err != nil {
		return tripwireCheckResult{}
	}
	if strings.TrimSpace(string(out)) == "" {
		return tripwireCheckResult{}
	}

	return tripwireCheckResult{
		violated: true,
		reason: fmt.Sprintf("project root %s has a staged or unstaged diff after execution %s finished",
			row.ProjectPath, row.ID),
	}
}

// checkLabelLifecycle guards the poller-labels-removed-log-means-never-
// applied / GH-4687 incident class: an adapter-dispatched task (one that
// went through a GitHub/GitLab/Linear/... poller, not a bare CLI run) whose
// execution reaches a terminal status with ZERO recorded execution_events is
// the observable shape of "nothing downstream of dispatch ever recorded a
// stage transition for it" — the same silent-death signature GH-4687 traced
// to the in-progress label never actually being applied. This reads what the
// execution already recorded (Store.ListExecutionEvents) rather than
// querying the adapter's live label state, per the sweep's own "no network
// calls" constraint. Scoped to TaskSourceAdapter != "" — a CLI-driven
// execution has no external label lifecycle to verify.
func checkLabelLifecycle(store *memory.Store, row *memory.Execution) tripwireCheckResult {
	if row.TaskSourceAdapter == "" {
		return tripwireCheckResult{}
	}

	events, err := store.ListExecutionEvents(row.ID)
	if err != nil {
		return tripwireCheckResult{}
	}
	if len(events) > 0 {
		return tripwireCheckResult{}
	}

	return tripwireCheckResult{
		violated: true,
		reason: fmt.Sprintf("adapter-dispatched execution %s (adapter=%s) reached a terminal status with zero recorded execution_events",
			row.ID, row.TaskSourceAdapter),
	}
}

// checkChildrenTerminal guards the epic-decompose-discards-child-work
// pitfall class: a task that recorded decomposed children (StageDecomposed
// ledger event) reaching its own terminal status while at least one child is
// still non-terminal or failed — exactly the shape that let a parent record
// a false "completed" no-op while a child's real work sat stranded.
// Delegates to decomposedChildLedgerNonTerminal (runner.go), the existing
// GH-4655/GH-4659 chokepoint for this same classification, rather than
// re-deriving child-terminal status with a second copy of the vocabulary. A
// task with no recorded children (the common case) is a no-op pass.
func checkChildrenTerminal(store *memory.Store, row *memory.Execution) tripwireCheckResult {
	hasNonTerminal, childIDs, err := decomposedChildLedgerNonTerminal(store, row.TaskID, row.ProjectPath)
	if err != nil || !hasNonTerminal {
		return tripwireCheckResult{}
	}

	return tripwireCheckResult{
		violated: true,
		reason: fmt.Sprintf("task %s finished with %d decomposed child(ren) recorded but not all reached a terminal status: %s",
			row.TaskID, len(childIDs), strings.Join(childIDs, ", ")),
	}
}

// checkWorktreePruned guards two shapes of the same stranded-work class the
// epic-discard pitfall documents:
//
//  1. An orphaned worktree directory still on disk for row.TaskID.
//     Runner.executeWithOptions's deferred cleanup runs before the
//     dispatcher ever calls Persist (worktree.go), so a match here means
//     that cleanup didn't happen or didn't finish.
//  2. Real commits landed (CommitSHA set) but no PR exists for a task that
//     requested one (TaskCreatePR) — the exact "committed work, no
//     delivery" shape PR#3383 made loud instead of silently discarding.
//     Skipped for ExecStatusDecomposed rows: a decomposed parent
//     legitimately has no commit of its own to ship.
func checkWorktreePruned(row *memory.Execution) tripwireCheckResult {
	if dirs, err := findOrphanedWorktreeDirs(row.TaskID); err == nil && len(dirs) > 0 {
		return tripwireCheckResult{
			violated: true,
			reason: fmt.Sprintf("worktree director%s still present after execution %s finished: %s",
				pluralY(len(dirs)), row.ID, strings.Join(dirs, ", ")),
		}
	}

	if row.TaskCreatePR && row.CommitSHA != "" && row.PRUrl == "" && row.Status != string(ExecStatusDecomposed) {
		return tripwireCheckResult{
			violated: true,
			reason: fmt.Sprintf("execution %s committed %s but produced no PR (task requested one)",
				row.ID, row.CommitSHA),
		}
	}

	return tripwireCheckResult{}
}

// pluralY returns "y" for n == 1 ("directory") and "ies" for anything else
// ("directories") — a tiny formatting helper kept local to this file since
// it has exactly one caller.
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// findOrphanedWorktreeDirs is a read-only sibling of
// PruneOrphanedWorktreeForTask (worktree.go): it scans os.TempDir() for
// directories matching taskID's "pilot-worktree-<sanitized-taskID>-"
// naming convention (CreateWorktree/CreateWorktreeWithBranch) without
// removing anything — this sweep only reports, it never mutates disk state,
// consistent with "alert-never-block".
func findOrphanedWorktreeDirs(taskID string) ([]string, error) {
	if taskID == "" {
		return nil, nil
	}

	tmpDir := os.TempDir()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, err
	}

	prefix := "pilot-worktree-" + sanitizeBranchName(taskID) + "-"
	var found []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			found = append(found, filepath.Join(tmpDir, entry.Name()))
		}
	}
	return found, nil
}
