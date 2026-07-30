package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// ErrTaskAlreadyActive wraps QueueTask's duplicate-task rejection so callers
// can distinguish expected dedup (task already queued or running) from a
// genuine queueing failure via errors.Is, instead of string-matching the
// error text (GH-4008).
var ErrTaskAlreadyActive = errors.New("task already queued or running")

// ErrDispatchGated marks a HandlerResult produced by one of handleIssueGeneric's
// pre-dispatch admission gates (already-active dedup, repick backoff window,
// terminal-completion re-check) rather than a genuine execution failure
// (GH-4469). Every adapter wrapper forwards HandlerResult.Error into its
// result struct unchanged, so anything that CAN inspect the error (in-tree
// pollers, tests, future adapters) can use errors.Is(err, ErrDispatchGated) to
// tell "the dispatcher intentionally declined this tick" apart from "the
// execution actually failed" instead of inferring it from Success/PRNumber
// alone. Note the vendored github SDK poller's own unmark decision does not
// consult this field at all (it only looks at Success/PRNumber) — for that
// path GH-4469's fix is gating earlier, at terminalCompletionChecker (see
// cmd/pilot/main.go), so the gated tick never reaches handleIssueGeneric.
var ErrDispatchGated = errors.New("dispatch gated: pre-dispatch admission check declined this tick")

// terminalExecutionStatuses is every execution status WaitForExecution and
// the GH-4372 retry decider both treat as "finished" — the execution is no
// longer in flight, regardless of whether it succeeded. Kept as the single
// definition both consult so they can't drift apart.
var terminalExecutionStatuses = map[string]bool{
	"completed": true, "failed": true, "cancelled": true, "declined": true,
	"no_op": true, "rate_limited": true, "skipped": true, "stalled": true, "infra": true,
}

// isTerminalExecutionStatus reports whether status is one of
// terminalExecutionStatuses.
func isTerminalExecutionStatus(status string) bool {
	return terminalExecutionStatuses[status]
}

// DispatcherConfig configures the task dispatcher behavior.
type DispatcherConfig struct {
	// StaleTaskDuration is a backwards-compat alias for StaleRunningThreshold.
	// Deprecated: use StaleRunningThreshold instead.
	StaleTaskDuration time.Duration

	// StaleRunningThreshold is how long a "running" task can remain before
	// it is considered orphaned and marked failed. Default: 30 minutes.
	StaleRunningThreshold time.Duration

	// StaleQueuedThreshold is how long a "queued" task can remain without
	// being picked up before it is considered stuck and marked failed.
	// Default: 5 minutes.
	StaleQueuedThreshold time.Duration

	// StaleRecoveryInterval is how often the periodic stale-recovery loop
	// runs. Default: 5 minutes.
	StaleRecoveryInterval time.Duration
}

// DefaultDispatcherConfig returns default dispatcher settings.
func DefaultDispatcherConfig() *DispatcherConfig {
	return &DispatcherConfig{
		StaleRunningThreshold: 30 * time.Minute,
		StaleQueuedThreshold:  5 * time.Minute,
		StaleRecoveryInterval: 5 * time.Minute,
	}
}

// resolveDefaults fills zero-valued fields with sensible defaults and
// applies the StaleTaskDuration backwards-compat alias.
func (c *DispatcherConfig) resolveDefaults() {
	// Backwards compat: if only the deprecated field is set, use it.
	if c.StaleRunningThreshold == 0 && c.StaleTaskDuration > 0 {
		c.StaleRunningThreshold = c.StaleTaskDuration
	}
	if c.StaleRecoveryInterval == 0 {
		c.StaleRecoveryInterval = 5 * time.Minute
	}
}

// Dispatcher manages task queuing and per-project workers.
// It ensures that tasks for the same project are executed serially
// while allowing parallel execution across different projects.
// Progress updates are emitted via runner.EmitProgress() so they
// flow through the same callback path as execution progress.
type Dispatcher struct {
	config     *DispatcherConfig
	store      *memory.Store
	runner     *Runner
	decomposer *TaskDecomposer           // Optional task decomposer
	workers    map[string]*ProjectWorker // key: project path
	mu         sync.RWMutex
	log        *slog.Logger
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	// dispatchMu serializes QueueTask's duplicate-task check + executions-row
	// insert (GH-4347). The two steps were separate, unlocked store calls
	// (IsTaskQueued SELECT, then queueSingleTask/queueDecomposedTask's INSERT
	// via ExecutionLifecycle.Begin) — two goroutines racing QueueTask for the
	// same task_id/project_path (e.g. the SDK poller's concurrent per-issue
	// goroutines, or a poll tick overlapping epic sub-issue creation) could
	// both observe "not queued" before either row landed, producing two
	// executions rows and two live dispatches for one task. Held for the
	// whole check-then-act region below; cheap (a couple of SQLite queries),
	// so serializing it dispatcher-wide does not bottleneck actual task
	// execution, which is unaffected by this lock.
	dispatchMu sync.Mutex
}

// NewDispatcher creates a new task dispatcher.
func NewDispatcher(store *memory.Store, runner *Runner, config *DispatcherConfig) *Dispatcher {
	if config == nil {
		config = DefaultDispatcherConfig()
	}
	config.resolveDefaults()

	ctx, cancel := context.WithCancel(context.Background())

	d := &Dispatcher{
		config:  config,
		store:   store,
		runner:  runner,
		workers: make(map[string]*ProjectWorker),
		log:     logging.WithComponent("dispatcher"),
		ctx:     ctx,
		cancel:  cancel,
	}

	// GH-4536 (TASK-419): wire the self-owned-queued-child takeover mechanism
	// into the Runner so reconcileChildOutcome (epic.go) can break the
	// structural self-deadlock instead of polling forever. nil runner is only
	// used by tests exercising Dispatcher in isolation.
	if runner != nil {
		runner.setReclaimSelfOwnedQueuedChildFn(d.reclaimSelfOwnedQueuedChild)
	}

	return d
}

// SetDecomposer sets the task decomposer for auto-splitting complex tasks.
// If set, complex tasks meeting the decomposition criteria will be split
// into subtasks before queuing.
func (d *Dispatcher) SetDecomposer(decomposer *TaskDecomposer) {
	d.decomposer = decomposer
}

// Start initializes the dispatcher, recovers stale tasks, and launches the
// periodic stale-recovery loop. The provided context controls the loop lifetime.
func (d *Dispatcher) Start(ctx context.Context) error {
	d.log.Info("Starting dispatcher")

	// GH-4392: reconcile dead-owner claimed executions before any other
	// recovery/adoption step and before any worker exists for this process.
	// See reconcileOrphanedExecutions's doc comment for why this runs first
	// and why it is scoped to claimed rows only (leaves GH-3732 restart
	// adoption below untouched for genuinely-continuing queued work).
	d.reconcileOrphanedExecutions()

	// Recover stale RUNNING tasks first, before queue adoption below creates
	// any workers — hasLiveWorker must reflect "nothing alive yet" for this
	// pass, exactly as before GH-3732 (crashed-worker recovery is unchanged
	// and out of scope for this fix).
	d.recoverStaleRunningTasks()

	// GH-3732: re-adopt projects that still have queued rows in SQLite. Only
	// the in-memory workers map was lost on restart — recreating a worker per
	// project lets Signal() drain the existing FIFO queue instead of the
	// stale-queued reap below wrongly failing tasks that are simply waiting
	// their turn (GH-3714/3715/3716 incident).
	//
	// GH-3788: adoptQueuedProjects reports whether it could actually read the
	// queued project paths. adoptQueuedProjects calls ensureWorker()
	// synchronously per project, and ensureWorker inserts into the workers
	// map under the same lock it holds for the rest of the call — so on
	// success, every project with a queued row already has a live worker by
	// the time recoverStaleQueuedTasks runs, regardless of goroutine
	// scheduling. But if the SQLite query itself fails (e.g. the store isn't
	// ready yet at boot), adoption silently adopts zero projects and the
	// stale-queued reap below would then treat every queued row as an orphan
	// — reproducing the exact "no worker picked up" mass-reap this issue
	// tracks. Skip this boot's reap pass in that case; the periodic loop
	// still catches genuine orphans on the next tick.
	adopted := d.adoptQueuedProjects()

	// Recover queued tasks that still have no worker after adoption — genuine
	// orphans only (e.g. a duplicate of an already-completed task, or a
	// project removed from config). See
	// TestDispatcher_BootWithQueuedRows_FIFODrainNoStaleReap for regression
	// coverage of N queued rows across multiple projects at boot, and
	// TestDispatcher_AdoptQueuedProjects_ReportsFailureWithoutAdopting for the
	// failed-adoption guard above.
	if adopted {
		d.recoverStaleQueuedTasks()
	} else {
		d.log.Warn("Skipping boot-time stale-queued reap — queue adoption failed, cannot tell adopted projects from orphans; genuine orphans will still be caught by the periodic stale-recovery loop")
	}

	// GH-2428: warn when the last batch of completed runs has no token
	// telemetry. A persistent gap means the backend's usage events aren't
	// being parsed — cost reporting and per-task budgets silently degrade.
	d.checkTelemetryGap()

	// Launch periodic recovery loop.
	d.wg.Add(1)
	go d.runStaleRecoveryLoop(ctx)

	return nil
}

// reconcileOrphanedExecutions transitions every claimed, non-terminal
// ("queued" or "running") execution row found at boot to "stalled", freeing
// its execution_claims generation lock so nextRetryGeneration can hand out a
// generation+1 retry on the next dispatch attempt (GH-4392). Every row it
// stalls also has its repick_backoff state cleared (GH-4454) — a daemon
// restart is not evidence the task can't succeed, so the retry this stall
// enables must not inherit a consecutive-drop count inflated by restart
// churn rather than genuine failures.
//
// Incident context: nextRetryGeneration (GH-4372) only advances the
// generation when the claimed execution it finds is in a TERMINAL status —
// "queued" and "running" both read as a live owner forever. A daemon
// restart that leaves rows claimed-but-queued (e.g. TASK-409's AWS cutover,
// which killed the local process mid-flight) therefore wedges those tasks:
// every future dispatch attempt sees the same non-terminal claim and backs
// off, and the periodic stale sweep only ever looked at "running" rows, so
// it silently reset 0 tasks.
//
// Why this is safe: Dispatcher.Start calls this before adoptQueuedProjects
// creates any worker and before the periodic recovery loop starts — no
// worker exists yet, so no row returned by GetClaimedNonTerminalExecutions
// can have been created by THIS process. Under the single-daemon invariant
// (H7/#4311) every claimed non-terminal row at this point in Start was
// therefore left behind by a prior, now-dead daemon process.
//
// Scoped to CLAIMED rows only (GetClaimedNonTerminalExecutions inner-joins
// execution_claims) — a non-terminal row with no claims entry was never
// inserted through ExecutionLifecycle.Begin, so it cannot be blocking a
// future dispatch attempt via the claim mechanism; GH-3732's queued-project
// restart adoption (adoptQueuedProjects, below) still owns getting such a
// row a worker, unchanged. This is what keeps
// TestDispatcher_AdoptQueuedProjectsOnRestart and
// TestDispatcher_BootWithQueuedRows_FIFODrainNoStaleReap passing: their
// fixtures use bare store.SaveExecution, which never writes a claims row.
//
// Mirrors the decomposed-parent and HasCompletedExecution guards
// recoverStaleRunningTasks/recoverStaleQueuedTasks use below, so a row whose
// real work already shipped (via decomposed children, or a duplicate row for
// an already-completed task) is deleted instead of marked stalled. Also
// mirrors the GH-4092 merged-PR heal check for "running" rows, so a dead
// daemon's in-flight run that had, in fact, already shipped a merged PR
// heals to "completed" instead of being marked "stalled".
func (d *Dispatcher) reconcileOrphanedExecutions() int {
	orphans, err := d.store.GetClaimedNonTerminalExecutions()
	if err != nil {
		d.log.Warn("Failed to fetch claimed non-terminal executions for boot-time orphan reconciliation", slog.Any("error", err))
		return 0
	}

	var reconciled int
	for _, exec := range orphans {
		if allComplete, childIDs, evidence, gErr := decomposedChildrenAllComplete(d.store, exec.TaskID, exec.ProjectPath, d.log); gErr != nil {
			d.log.Warn("Failed to check decomposed-parent guard during boot orphan reconciliation",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID), slog.Any("error", gErr))
		} else if allComplete {
			d.log.Warn("decomposed-parent guard fired during boot orphan reconciliation",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID),
				slog.Any("children", childIDs), slog.Any("evidence", evidence))
			d.deleteOrphanRunningRow(exec)
			continue
		}

		completed, hceErr := d.store.HasCompletedExecution(exec.TaskID, exec.ProjectPath)
		if hceErr != nil {
			d.log.Warn("HasCompletedExecution error during boot orphan reconciliation; treating as not completed",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID), slog.Any("error", hceErr))
		}
		if completed {
			d.log.Info("Deleting orphan row found at boot (task already completed)",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID))
			d.deleteOrphanRunningRow(exec)
			continue
		}

		if exec.Status == string(ExecStatusRunning) {
			branch := mergedPRCheckBranch(exec)
			if mergedURL, mergedErr := staleRunningMergedPRCheck(d.ctx, exec.ProjectPath, branch); mergedErr == nil && mergedURL != "" {
				durationMs := time.Since(exec.CreatedAt).Milliseconds()
				if err := d.store.MarkExecutionCompleted(exec.ID, mergedURL, "", durationMs); err != nil {
					d.log.Error("Failed to heal boot orphan to completed", slog.String("id", exec.ID), slog.Any("error", err))
				} else {
					d.log.Info("Boot orphan's branch already merged; healed to completed instead of stalled",
						slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID), slog.String("pr_url", mergedURL))
					d.recordExecutionEvent(exec.ID, memory.StageCompleted,
						fmt.Sprintf("boot orphan healed to completed after restart (merged PR: %s, GH-4392)", mergedURL))
				}
				continue
			}
		}

		d.log.Warn("Reconciling dead-owner execution found at boot — transitioning to stalled to free its claim for retry",
			slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID),
			slog.String("project", exec.ProjectPath), slog.String("prior_status", exec.Status),
			slog.Time("created_at", exec.CreatedAt))
		if err := d.store.UpdateExecutionStatus(exec.ID, string(ExecStatusStalled), "orphaned by daemon restart: non-terminal claimed row at boot before this process claimed any work (GH-4392)"); err != nil {
			d.log.Error("Failed to reconcile orphaned execution at boot", slog.String("id", exec.ID), slog.Any("error", err))
			continue
		}
		reconciled++
		d.recordExecutionEvent(exec.ID, memory.StageStalled, "orphaned queued/running execution reconciled at daemon boot (dead pre-restart owner, GH-4392)")

		// GH-4454: a daemon restart is not evidence the task can't succeed —
		// but the "stalled" status this loop just wrote is exactly the
		// terminal-but-not-done claim nextRetryGeneration looks for, so the
		// very next dispatch attempt repicks it and beginWithGenerationRetry
		// treats that repick as one more consecutive drop toward
		// dispatcherRepickHardCap. Left alone, repeated restarts (or one
		// restart on top of pre-existing real drops) push a perfectly healthy
		// task over the hard cap on restart churn alone, permanently stalling
		// it via stallTaskAfterRepickHardCap for a reason that has nothing to
		// do with the task itself. Clear any accumulated backoff state here so
		// the retry this stall enables starts a fresh consecutive-drop count
		// instead of inheriting one inflated by the restart.
		backoffKey := repickBackoffKey(exec.ProjectPath, exec.TaskID)
		if err := d.ClearRepickBackoffState(backoffKey); err != nil {
			d.log.Warn("failed to clear repick backoff state for boot-stalled execution",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID),
				slog.String("project", exec.ProjectPath), slog.Any("error", err))
		}
	}

	if reconciled > 0 {
		d.log.Info("boot-time orphan reconciliation complete", slog.Int("reconciled", reconciled), slog.Int("found", len(orphans)))
	}
	return reconciled
}

// adoptQueuedProjects recreates a worker for every project that still has
// queued (or pending) executions in SQLite. Called once at Start, before the
// stale-queued reap runs, so tasks left behind by a daemon restart resume
// FIFO processing instead of being misclassified as orphans. GH-3732.
//
// Returns false if the queued-project-paths query itself failed, meaning the
// caller cannot trust that every queued project got a worker — the caller
// must not run the stale-queued reap in that case (GH-3788).
func (d *Dispatcher) adoptQueuedProjects() bool {
	projectPaths, err := d.store.GetQueuedProjectPaths()
	if err != nil {
		d.log.Warn("Failed to fetch queued project paths for restart adoption", slog.Any("error", err))
		return false
	}
	for _, path := range projectPaths {
		d.log.Info("Re-adopting queued tasks after restart", slog.String("project", path))
		d.ensureWorker(path)
	}
	return true
}

// checkTelemetryGap inspects recent completed executions and logs a warning
// when token telemetry is mostly missing. Threshold: ≥50% of the last 50
// completed runs (with a real commit) reporting tokens_total=0. GH-2428.
func (d *Dispatcher) checkTelemetryGap() {
	const sampleSize = 50
	const threshold = 0.5
	stats, err := d.store.RecentCompletedTelemetryStats(sampleSize)
	if err != nil {
		d.log.Debug("Skipping telemetry gap check", slog.Any("error", err))
		return
	}
	if stats.CompletedRuns < 10 {
		return // Not enough data
	}
	ratio := float64(stats.ZeroTokenRuns) / float64(stats.CompletedRuns)
	if ratio >= threshold {
		backend := "claude-code"
		if d.runner != nil {
			backend = d.runner.backendType()
		}
		d.log.Warn("Token telemetry gap detected — recent completed runs report 0 tokens",
			slog.String("backend", backend),
			slog.Int("completed_runs", stats.CompletedRuns),
			slog.Int("zero_token_runs", stats.ZeroTokenRuns),
			slog.Float64("zero_token_ratio", ratio),
			slog.String("hint", "verify backend usage events are being parsed (GH-2428)"),
		)
	}
}

// runStaleRecoveryLoop ticks every StaleRecoveryInterval and calls
// recoverStaleTasks. It stops when ctx is cancelled or the dispatcher stops.
func (d *Dispatcher) runStaleRecoveryLoop(ctx context.Context) {
	defer d.wg.Done()

	interval := d.config.StaleRecoveryInterval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	d.log.Info("Stale recovery loop started", slog.Duration("interval", interval))

	for {
		select {
		case <-ctx.Done():
			d.log.Debug("Stale recovery loop stopped (context cancelled)")
			return
		case <-d.ctx.Done():
			d.log.Debug("Stale recovery loop stopped (dispatcher stopped)")
			return
		case <-ticker.C:
			d.recoverStaleTasks()
		}
	}
}

// Stop gracefully stops all workers and the dispatcher.
func (d *Dispatcher) Stop() {
	d.log.Info("Stopping dispatcher")
	d.cancel()

	// Stop all workers
	d.mu.Lock()
	for _, worker := range d.workers {
		worker.Stop()
	}
	d.mu.Unlock()

	// Wait for all workers to finish
	d.wg.Wait()
	d.log.Info("Dispatcher stopped")
}

// recoverStaleTasks marks orphaned running and queued tasks as failed.
// Re-queuing without a worker just recreates the orphan, so we fail them.
// Used by the periodic recovery loop, where both halves can safely run
// back-to-back since queue adoption already happened at Start.
func (d *Dispatcher) recoverStaleTasks() int {
	resetCount := d.recoverStaleRunningTasks()
	resetCount += d.recoverStaleQueuedTasks()
	d.log.Info("stale recovery complete, reset N tasks", slog.Int("count", resetCount))
	return resetCount
}

// recoverStaleRunningTasks marks orphaned running tasks (crashed workers) as
// failed. Split out from recoverStaleTasks so Dispatcher.Start can run it
// before queue adoption creates any workers. GH-3732.
func (d *Dispatcher) recoverStaleRunningTasks() int {
	var resetCount int

	staleRunning, err := d.store.GetStaleRunningExecutions(d.config.StaleRunningThreshold)
	if err != nil {
		d.log.Warn("Failed to fetch stale running executions", slog.Any("error", err))
	}
	for _, exec := range staleRunning {
		// GH-4227: decomposed-parent guard runs BEFORE the own-row
		// HasCompletedExecution check below — an epic parent whose task_id
		// decomposed into children that ALL already shipped never gets its own
		// completed row (TASK-296, epic parents carry no direct deliverable),
		// so the check below would otherwise mark it stale_running->failed
		// even though the real work is done. Ledger-only, defense-in-depth
		// alongside the pickup-time guard in processQueue (GH-4216 fix 3).
		if allComplete, childIDs, evidence, gErr := decomposedChildrenAllComplete(d.store, exec.TaskID, exec.ProjectPath, d.log); gErr != nil {
			d.log.Warn("Failed to check decomposed-parent guard during stale-running reap",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("error", gErr))
		} else if allComplete {
			d.log.Warn("decomposed-parent guard fired",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("children", childIDs),
				slog.Any("evidence", evidence),
			)
			d.deleteOrphanRunningRow(exec)
			continue
		}

		// If this task already completed successfully, delete the orphan row
		// instead of marking it failed (avoids dashboard showing false failures).
		completed, hceErr := d.store.HasCompletedExecution(exec.TaskID, exec.ProjectPath)
		if hceErr != nil {
			d.log.Warn("HasCompletedExecution error during stale-running reap; treating as not completed",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("error", hceErr))
		}
		if completed {
			d.log.Info("Deleting orphan running row (task already completed)",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
			)
			d.deleteOrphanRunningRow(exec)
			continue
		}
		if d.hasLiveWorker(exec.ProjectPath) {
			d.log.Debug("Skipping stale running reap — live worker for project exists",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.String("project", exec.ProjectPath),
			)
			continue
		}

		// GH-4092: a stale "running" row does not mean the work was lost — the
		// worker may have shipped a PR (autopilot merged it) and only the row's
		// own status update raced the reap. HasCompletedExecution above only
		// catches a *separate* completed row for the same task; this row IS the
		// one being reaped, so it never satisfies that check. Consult the task
		// branch's PR state directly before failing it. GH-4409: prefer the
		// branch actually recorded on the row (mergedPRCheckBranch) over
		// reconstructing "pilot/{TaskID}" — a decomposed subtask's work lands
		// on its parent's branch, not a branch named after the subtask.
		branch := mergedPRCheckBranch(exec)
		if mergedURL, mergedErr := staleRunningMergedPRCheck(d.ctx, exec.ProjectPath, branch); mergedErr == nil && mergedURL != "" {
			d.log.Info("Stale running task's branch already merged; healing to completed instead of failed",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.String("pr_url", mergedURL),
			)
			durationMs := time.Since(exec.CreatedAt).Milliseconds()
			if err := d.store.MarkExecutionCompleted(exec.ID, mergedURL, "", durationMs); err != nil {
				d.log.Error("Failed to heal stale running task to completed", slog.String("id", exec.ID), slog.Any("error", err))
			} else {
				d.recordExecutionEvent(exec.ID, memory.StageCompleted,
					fmt.Sprintf("stale_running healed to completed after restart (merged PR: %s)", mergedURL))
			}
			continue
		}

		// GH-4423: a merged PR isn't the only liveness evidence a branch can
		// carry — an OPEN PR (the normal state for minutes-to-hours while CI
		// runs or a reviewer is pending) means the worker already shipped its
		// deliverable and is simply waiting, not orphaned. Without this check,
		// any task whose PR review outlasts StaleRunningThreshold got marked
		// failed on every 5-minute reap tick past that point.
		if openURL, openErr := staleRunningOpenPRCheck(d.ctx, exec.ProjectPath, branch); openErr == nil && openURL != "" {
			d.log.Info("Stale running task's branch has an open PR; treating as live, not orphaned",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.String("pr_url", openURL),
			)
			continue
		}

		d.log.Warn("Marking stale running task as failed",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.Time("created_at", exec.CreatedAt),
		)
		// GH-4423: CAS-guarded — if this row completed in the gap between the
		// evidence gathered above and this write (the reaper's own TOCTOU
		// window), the write is rejected instead of clobbering the completed
		// row. The store already logs the rejection at ERROR with both states.
		applied, err := d.store.UpdateExecutionStatusIfNotTerminal(exec.ID, "failed", "stale running task recovered (orphaned worker)")
		if err != nil {
			d.log.Error("Failed to mark stale running task", slog.String("id", exec.ID), slog.Any("error", err))
		} else if !applied {
			d.log.Warn("Skipped marking stale running task failed — row reached a terminal status during reap (GH-4423 CAS guard)",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID))
		} else {
			resetCount++
			// GH-4101: without this, the terminal transition a restart forces on an
			// orphaned row is invisible in execution_events — the audit trail simply
			// stops, indistinguishable from a row still legitimately mid-flight.
			d.recordExecutionEvent(exec.ID, memory.StageFailed, "stale_running recovered after restart")
		}
	}

	return resetCount
}

// deleteOrphanRunningRow deletes a stale "running" row and prunes its
// worktree, once the reap loop has established the real work already
// shipped — either via HasCompletedExecution on the row's own task_id or via
// the decomposed-parent guard finding every decomposed child complete.
func (d *Dispatcher) deleteOrphanRunningRow(exec *memory.Execution) {
	if err := d.store.DeleteExecution(exec.ID); err != nil {
		d.log.Error("Failed to delete orphan running row", slog.String("id", exec.ID), slog.Any("error", err))
	}
	// GH-4021: the orphaned run's worktree outlives the DB row it was
	// tracked under — prune it now instead of leaving it for the next
	// restart's full CleanupOrphanedWorktrees sweep.
	if pruned, pruneErr := PruneOrphanedWorktreeForTask(d.ctx, exec.ProjectPath, exec.TaskID); pruneErr != nil {
		d.log.Warn("Failed to prune orphaned task worktree",
			slog.String("task_id", exec.TaskID), slog.Any("error", pruneErr))
	} else if pruned > 0 {
		d.log.Info("Pruned orphaned task worktree", slog.String("task_id", exec.TaskID), slog.Int("count", pruned))
	}
}

// mergedPRCheckBranch reports the branch staleRunningMergedPRCheck should
// probe for exec: exec.TaskBranch when the row recorded one, falling back to
// reconstructing "pilot/{TaskID}" only for legacy rows saved before
// TaskBranch was persisted (or a row where it was never set).
//
// GH-4409: reconstructing "pilot/{TaskID}" unconditionally was wrong for a
// decomposed subtask — decompose.go stamps subtask.Branch = parent.Branch,
// so a child's real work lands on its PARENT's branch (e.g. GH-4393-5 ships
// on pilot/GH-4393, not pilot/GH-4393-5). Probing the reconstructed
// child-only branch found nothing to merge, so a claimed running child whose
// work already shipped via the parent missed this heal and was re-run at
// stalled->generation+1.
func mergedPRCheckBranch(exec *memory.Execution) string {
	if exec.TaskBranch != "" {
		return exec.TaskBranch
	}
	return fmt.Sprintf("pilot/%s", exec.TaskID)
}

// staleRunningMergedPRCheck reports the URL of a merged PR for branch in
// projectPath, or "" if none exists. Used by recoverStaleRunningTasks to
// distinguish a genuinely orphaned worker from one whose work already shipped
// (GH-4092). Production shells out via GitOperations.FindMergedPRByBranch
// (the same gh-CLI dependency CreatePR already relies on); tests override
// this var directly, mirroring isParentDoneLiveFallback in epic.go — real
// subprocess calls never run during `go test`.
var staleRunningMergedPRCheck = func(ctx context.Context, projectPath, branch string) (string, error) {
	if testing.Testing() {
		return "", nil
	}
	return NewGitOperations(projectPath).FindMergedPRByBranch(ctx, branch)
}

// staleRunningOpenPRCheck reports the URL of an OPEN PR for branch in
// projectPath, or "" if none exists. GH-4423: an open PR is liveness evidence
// just like a merged one — it's the normal state for minutes-to-hours while
// CI runs or a reviewer is pending, not evidence of an orphaned worker.
// staleRunningMergedPRCheck alone only recognized a MERGED PR, so a task
// legitimately waiting on an open PR past StaleRunningThreshold got no credit
// here and was marked failed on the very next reap tick. Mirrors
// staleRunningMergedPRCheck's test-mode short-circuit; tests override this
// var directly.
var staleRunningOpenPRCheck = func(ctx context.Context, projectPath, branch string) (string, error) {
	if testing.Testing() {
		return "", nil
	}
	return NewGitOperations(projectPath).FindOpenPRByBranch(ctx, branch)
}

// mergedPRPreflightCheck reports the URL of a merged PR for a queued task's
// branch, or "" if none exists. GH-4141 Phase 3 defense-in-depth: a queued
// duplicate (e.g. the sub-issue poller-retry duplicate that motivated
// TASK-394's ledger row) must not burn a full backend invocation just to
// rediscover "no new commit" as a no_op — the worker marks it completed with
// the existing PR URL instead and skips the backend call entirely. Mirrors
// staleRunningMergedPRCheck's test-mode short-circuit; tests override this
// var directly.
var mergedPRPreflightCheck = func(ctx context.Context, projectPath, branch string) (string, error) {
	if testing.Testing() {
		return "", nil
	}
	return NewGitOperations(projectPath).FindMergedPRByBranch(ctx, branch)
}

// recoverStaleQueuedTasks marks orphaned queued tasks as failed: either a
// duplicate row for a task that already completed, or a queued task whose
// project has no live worker even after Dispatcher.Start's adoption pass
// (e.g. the project was removed from config). GH-2331: a live worker means
// the task is simply waiting its turn — Pilot runs tasks serially per
// project, and a sibling taking 8+ minutes (common for epic/Navigator work)
// would otherwise exceed the 5-minute threshold and get killed mid-queue.
func (d *Dispatcher) recoverStaleQueuedTasks() int {
	var resetCount int

	staleQueued, err := d.store.GetStaleQueuedExecutions(d.config.StaleQueuedThreshold)
	if err != nil {
		d.log.Warn("Failed to fetch stale queued executions", slog.Any("error", err))
	}
	for _, exec := range staleQueued {
		// GH-4227: decomposed-parent guard runs BEFORE the own-row
		// HasCompletedExecution check below, mirroring recoverStaleRunningTasks
		// — a re-queued decomposed epic parent whose children all shipped must
		// not be reaped as an orphan just because its own row carries no
		// deliverable (TASK-296).
		if allComplete, childIDs, evidence, gErr := decomposedChildrenAllComplete(d.store, exec.TaskID, exec.ProjectPath, d.log); gErr != nil {
			d.log.Warn("Failed to check decomposed-parent guard during stale-queued reap",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("error", gErr))
		} else if allComplete {
			d.log.Warn("decomposed-parent guard fired",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("children", childIDs),
				slog.Any("evidence", evidence),
			)
			if err := d.store.DeleteExecution(exec.ID); err != nil {
				d.log.Error("Failed to delete decomposed-parent-guarded orphan queued row", slog.String("id", exec.ID), slog.Any("error", err))
			}
			continue
		}

		completed, hceErr := d.store.HasCompletedExecution(exec.TaskID, exec.ProjectPath)
		if hceErr != nil {
			d.log.Warn("HasCompletedExecution error during stale-queued reap; treating as not completed",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("error", hceErr))
		}
		if completed {
			d.log.Info("Deleting orphan queued row (task already completed)",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
			)
			if err := d.store.DeleteExecution(exec.ID); err != nil {
				d.log.Error("Failed to delete orphan queued row", slog.String("id", exec.ID), slog.Any("error", err))
			}
			continue
		}

		if d.hasLiveWorker(exec.ProjectPath) {
			d.log.Debug("Skipping stale queued reap — live worker for project exists",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.String("project", exec.ProjectPath),
			)
			continue
		}

		d.log.Warn("Marking orphaned queued task as failed",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.Time("created_at", exec.CreatedAt),
		)
		// GH-3732: reworded from "recovered" — restart adoption already gives
		// every project with queued rows a worker, so reaching here means the
		// project genuinely has none (e.g. removed from config), not that a
		// normal restart failed to reconnect it.
		//
		// GH-4423: CAS-guarded — same TOCTOU window as the stale-running reap
		// above; if this row went terminal between the guards above and this
		// write, the write is rejected instead of clobbering it (the store
		// already logs both states at ERROR).
		applied, err := d.store.UpdateExecutionStatusIfNotTerminal(exec.ID, "failed", "queued task orphaned by restart; project no longer configured")
		if err != nil {
			d.log.Error("Failed to mark stale queued task", slog.String("id", exec.ID), slog.Any("error", err))
		} else if !applied {
			d.log.Warn("Skipped marking stale queued task failed — row reached a terminal status during reap (GH-4423 CAS guard)",
				slog.String("execution_id", exec.ID), slog.String("task_id", exec.TaskID))
		} else {
			resetCount++
			// GH-4101: mirrors the stale-running event above — the audit trail must
			// show why this row went terminal, not just that it did.
			d.recordExecutionEvent(exec.ID, memory.StageFailed, "stale_queued recovered after restart: project no longer configured")
		}
	}

	return resetCount
}

// recordExecutionEvent writes a best-effort stage-transition record to the
// execution_events audit trail for dispatcher-driven status changes (stale
// recovery, GH-4101). Mirrors ProjectWorker.recordExecutionEvent: a nil store,
// missing parent execution row (GH-4244 validate-first via
// memory.Store.RecordExecutionEvent), or insert failure is logged and
// swallowed, never blocks recovery — the audit trail is a diagnostic aid, not
// load-bearing.
func (d *Dispatcher) recordExecutionEvent(executionID string, stage memory.Stage, detail string) {
	if d.store == nil {
		return
	}
	if err := d.store.RecordExecutionEvent(executionID, stage, detail); err != nil {
		d.log.Warn("Failed to record execution event",
			slog.String("execution_id", executionID),
			slog.String("stage", string(stage)),
			slog.Any("error", err))
	}
}

// hasLiveWorker reports whether a worker goroutine exists for the given
// project path. Used by stale recovery to avoid killing queued tasks that
// are simply waiting their turn behind a long-running sibling. GH-2331.
func (d *Dispatcher) hasLiveWorker(projectPath string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.workers[projectPath]
	return ok
}

// projectWorkerIdentityKey is the context.Value key used to mark a context as
// being executed synchronously by a ProjectWorker (TASK-393's sole
// per-project serialization point). GH-4536 (TASK-419).
type projectWorkerIdentityKey struct{}

// withProjectWorkerIdentity marks ctx as executing synchronously on
// projectPath's ProjectWorker. Set once, at processQueue's call into
// Runner.Execute — the only place that call happens — so anything deep in
// the call stack (epic.go's reconcileChildOutcome) can explicitly detect
// "I am the project's own worker" instead of inferring it from timing or a
// package-level global. GH-4536 (TASK-419): this is what lets a queued child
// of an epic be recognized as unrunnable-by-anyone-else, rather than waited
// on forever as if it were just queued behind other work (GH-2331/GH-4413's
// legitimate case, which by construction never carries this identity for a
// DIFFERENT project).
func withProjectWorkerIdentity(ctx context.Context, projectPath string) context.Context {
	return context.WithValue(ctx, projectWorkerIdentityKey{}, projectPath)
}

// projectWorkerIdentity returns the project path of the ProjectWorker
// executing ctx synchronously, and whether that identity was set at all.
// false for contexts built outside the real Dispatcher->ProjectWorker path
// (e.g. tests calling epic/runner code directly with a bare context) —
// callers must treat "unknown" as "not self-owned", preserving existing
// behavior for those paths.
func projectWorkerIdentity(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(projectWorkerIdentityKey{}).(string)
	return v, ok
}

// selfOwnedTakeoverForceStallReason is the exact Error text
// reclaimSelfOwnedQueuedChild stamps on a queued child's dead-end claim when
// force-stalling it purely to release the claim generation for a GH-4536
// takeover (below) — not a genuine execution failure. Exported as a package
// constant (rather than an inline literal) so epic.go's child-outcome
// lookups (GH-4619) can recognize this exact administrative marker and
// exclude it from being treated as a genuine terminal outcome, the same
// exact-reason-match idiom escalateStalledTask already uses to distinguish
// "already escalated" from "fresh stall" (GH-4502).
const selfOwnedTakeoverForceStallReason = "GH-4536 (TASK-419): force-stalled for takeover — this Runner's own ProjectWorker is the only goroutine that could ever run this queued child"

// reclaimSelfOwnedQueuedChild takes over a queued sub-issue execution that
// only the caller's own ProjectWorker could ever run (GH-4536/TASK-419): the
// blocked worker IS the sole goroutine serializing subTask.ProjectPath
// (TASK-393), so waiting for some other channel to progress a queued row it
// owns is a guaranteed, structural deadlock, not a legitimate queue-wait.
//
// Mechanics mirror reconcileOrphanedExecutions' boot-time precedent (dead
// claim -> "stalled" -> clear repick backoff -> eligible for a fresh
// generation) but scoped to one row and triggered in-flight rather than only
// at boot, with a CAS guard since other writers may be live:
//  1. Force the existing non-terminal claim to "stalled" via
//     UpdateExecutionStatusIfNotTerminal, freeing nextRetryGeneration to grant
//     a new generation for it (a plain "queued"/"running" claim reads as a
//     live owner forever, GH-4372).
//  2. Clear its repick_backoff state — this deadlock is not a genuine
//     execution failure and must not inherit or grow the orphan channel's
//     drop count toward dispatcherRepickHardCap (mirrors GH-4454).
//  3. Route the actual re-claim through beginWithGenerationRetry — the one
//     production caller of nextRetryGeneration+the shared repick_backoff
//     store — instead of re-implementing a second, driftable claim-retry path
//     (the exact warning at epic.go's sub-issue Begin() call site).
//
// Returns ok=false (no error) when beginWithGenerationRetry itself declines
// the retry (hard cap already tripped, still inside the backoff window, or a
// genuine race lost to another channel) — the caller must fail the sub-issue
// rather than hang, per GH-4536's acceptance criteria, not treat this as
// grounds to keep polling.
func (d *Dispatcher) reclaimSelfOwnedQueuedChild(subTask *Task) (execID string, ok bool, err error) {
	taskID, projectPath := subTask.ID, subTask.ProjectPath

	existing, lookupErr := d.store.GetLatestExecutionByTaskID(taskID, projectPath)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return "", false, fmt.Errorf("reclaimSelfOwnedQueuedChild: execution lookup failed: %w", lookupErr)
	}
	if existing != nil && !isTerminalExecutionStatus(existing.Status) {
		reason := selfOwnedTakeoverForceStallReason
		applied, casErr := d.store.UpdateExecutionStatusIfNotTerminal(existing.ID, string(ExecStatusStalled), reason)
		if casErr != nil {
			return "", false, fmt.Errorf("reclaimSelfOwnedQueuedChild: force-stall failed: %w", casErr)
		}
		if applied {
			d.log.Warn("reclaimSelfOwnedQueuedChild: force-stalled self-owned queued child for takeover",
				slog.String("task_id", taskID),
				slog.String("project", projectPath),
				slog.String("execution_id", existing.ID),
			)
			d.recordExecutionEvent(existing.ID, memory.StageStalled, reason)
		} else {
			// Lost a race with another writer (e.g. the sweep reaping the same
			// row concurrently) — it already reached a terminal status, which
			// is exactly what we needed; fall through to the claim attempt.
			d.log.Info("reclaimSelfOwnedQueuedChild: existing claim already reached a terminal status before force-stall landed",
				slog.String("task_id", taskID), slog.String("project", projectPath))
		}

		backoffKey := repickBackoffKey(projectPath, taskID)
		if clearErr := d.ClearRepickBackoffState(backoffKey); clearErr != nil {
			d.log.Warn("reclaimSelfOwnedQueuedChild: failed to clear repick backoff state",
				slog.String("task_id", taskID), slog.String("project", projectPath), slog.Any("error", clearErr))
		}
	}

	newExecID, beginErr := d.beginWithGenerationRetry(subTask, ExecStatusRunning)
	if beginErr != nil {
		return "", false, fmt.Errorf("reclaimSelfOwnedQueuedChild: beginWithGenerationRetry failed: %w", beginErr)
	}
	if newExecID == "" {
		return "", false, nil
	}
	return newExecID, true, nil
}

// IsActive reports whether taskID is already queued or running in
// projectPath, using the same source of truth QueueTask's duplicate-task
// check uses. Callers that dispatch on a poll loop can pre-check this
// before announcing/attempting a dispatch, so a task legitimately waiting
// behind other work doesn't generate a repeated dispatch-attempt +
// rejection on every tick (GH-4008).
// A store error fails open (returns false) — QueueTask's own check remains
// the authoritative guard.
//
// GH-4276: scoped by projectPath — task_id alone is not unique across
// projects (fresh repos start numbering at #1), so an unscoped check could
// see a same-numbered task active in a different project and wrongly skip
// dispatch here.
func (d *Dispatcher) IsActive(taskID, projectPath string) bool {
	active, err := d.store.IsTaskQueued(taskID, projectPath)
	if err != nil {
		d.log.Warn("Failed to check task active state", slog.String("task_id", taskID), slog.Any("error", err))
		return false
	}
	return active
}

// HasTerminalCompletion reports whether taskID already has terminal ledger
// evidence in projectPath (delegates to the package-level HasTerminalCompletion
// helper this package's own processQueue pickup guard uses).
//
// GH-4376: exported so admission gates outside this package — e.g. the shared
// dispatch chokepoint in cmd/pilot/handler_common.go — can consult the same
// "done" definition the SDK poller's ExecutionChecker uses, as a second,
// in-tree check independent of the poller's own per-tick admission logic
// (external studio-sdk dependency, not something this repo can patch).
func (d *Dispatcher) HasTerminalCompletion(taskID, projectPath string) (bool, error) {
	return HasTerminalCompletion(d.store, taskID, projectPath)
}

// HasLiveExecutionOwner reports whether taskID currently has a live
// (non-terminal) execution owner in projectPath (delegates to the
// package-level HasLiveExecutionOwner helper), exported for callers outside
// this package — mirrors the HasTerminalCompletion export pattern above.
func (d *Dispatcher) HasLiveExecutionOwner(taskID, projectPath string) (bool, error) {
	return HasLiveExecutionOwner(d.store, taskID, projectPath)
}

// HasLiveExecutionOwner reports whether taskID currently has a live
// (non-terminal) execution owner in projectPath — mirroring the
// nextRetryGeneration liveness check this package's own claim-loss retry
// path already uses. Exported at package level (not just via *Dispatcher) so
// callers that only hold a *memory.Store — e.g. internal/adapters/gitlab's
// legacy poller, which has no Dispatcher of its own — can consult it too,
// the same way cmd/pilot's terminalCompletionChecker calls the package-level
// HasTerminalCompletion directly.
//
// GH-4587: a poller/handler that just received a failed-looking IssueResult
// (Success=false, no PR/MR) must not treat that as a genuine failure and
// "unmark for retry" when the task is actually still being executed by
// someone else — the current execution_claims generation is held by a
// still-running execution, or the latest executions row for the task is
// itself non-terminal (queued/pending/running/decomposed, the same set
// IsTaskQueued and Dispatcher.IsActive use). Either condition means another
// dispatch is legitimately in flight; unmarking now would let a second
// channel re-offer the same task while the first is still working it.
func HasLiveExecutionOwner(store *memory.Store, taskID, projectPath string) (bool, error) {
	active, err := store.IsTaskQueued(taskID, projectPath)
	if err != nil {
		return false, err
	}
	if active {
		return true, nil
	}

	_, execID, found, err := store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	claimedExec, err := store.GetExecution(execID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Dangling claim, no executions row behind it — not a live
			// owner, same treatment nextRetryGeneration gives this case.
			return false, nil
		}
		return false, err
	}
	return !isTerminalExecutionStatus(claimedExec.Status), nil
}

// RepickBackoffState, SetRepickBackoffState, and ClearRepickBackoffState
// expose the store's repick_backoff table to callers outside this package —
// e.g. cmd/pilot's repickBackoffTracker (GH-4394). The tracker owns the
// exponential-growth policy and its own in-process cache for the hot path;
// these are thin store proxies so that cache is backed by the same durable
// ledger every other dispatch-admission state (execution_claims, etc.) uses,
// rather than resetting to empty on daemon restart or diverging across a
// shadow-DB split-brain (#4393).
func (d *Dispatcher) RepickBackoffState(key string) (consecutiveDrops int, nextAllowedAt time.Time, found bool, err error) {
	return d.store.GetRepickBackoff(key)
}

func (d *Dispatcher) SetRepickBackoffState(key string, consecutiveDrops int, nextAllowedAt time.Time) error {
	return d.store.SetRepickBackoff(key, consecutiveDrops, nextAllowedAt)
}

func (d *Dispatcher) ClearRepickBackoffState(key string) error {
	return d.store.ClearRepickBackoff(key)
}

// StallDropCount and SetStallDropCount expose the store's per-key stall-kill
// counter (GH-4502) — the carve-out for drops caused by the stall watchdog
// killing a healthy-but-slow session, tracked separately from the
// consecutive_drops genuine-failure counter above so 4 stall-kills can't
// wedge a task at dispatcherRepickHardCap the way pilot-console GH-24 did.
func (d *Dispatcher) StallDropCount(key string) (count int, found bool, err error) {
	return d.store.GetStallDropCount(key)
}

func (d *Dispatcher) SetStallDropCount(key string, count int) error {
	return d.store.SetStallDropCount(key, count)
}

// InfraDropCount and SetInfraDropCount expose the store's per-key
// infra-classified-repick counter (GH-4540/TASK-421) — the carve-out for
// drops caused by an environment/infra failure (e.g. a hosted git_clean
// preflight deadlock or a CI outage) rather than the task's own code,
// tracked separately from consecutive_drops the same way StallDropCount is.
func (d *Dispatcher) InfraDropCount(key string) (count int, found bool, err error) {
	return d.store.GetInfraDropCount(key)
}

func (d *Dispatcher) SetInfraDropCount(key string, count int) error {
	return d.store.SetInfraDropCount(key, count)
}

// ClaimLostDropCount and SetClaimLostBackoff expose the store's per-key
// claim-lost/already-done drop counter (GH-4540/TASK-421) to cmd/pilot's
// repickBackoffTracker — the sibling counter that grows the shared backoff
// cooldown window for a dispatch attempt refused because the task was
// already active or already terminally done, WITHOUT touching
// consecutive_drops (the counter beginWithGenerationRetry gates the hard cap
// on). See repickBackoffPersister in cmd/pilot/repick_backoff.go.
func (d *Dispatcher) ClaimLostDropCount(key string) (count int, found bool, err error) {
	return d.store.GetClaimLostDropCount(key)
}

func (d *Dispatcher) SetClaimLostBackoff(key string, claimLostDrops int, nextAllowedAt time.Time) error {
	return d.store.SetClaimLostBackoff(key, claimLostDrops, nextAllowedAt)
}

// ExecutionGeneration returns the execution_claims generation most recently
// claimed for (taskID, projectPath): 0 for an ordinary first attempt, >0 when
// beginWithGenerationRetry claimed a retry generation because the prior
// claim was terminal but the task was not yet done (GH-4394). Callers use
// this to distinguish a genuine fresh dispatch (safe to clear repick
// backoff) from a repick whose backoff was just extended directly against
// the store by beginWithGenerationRetry — clearing it unconditionally on any
// successful QueueTask return is what let GH-85 re-pick 5x in ~15 minutes
// with no backoff growth: every repick looked identical to a fresh dispatch
// from the poller-chokepoint's point of view.
func (d *Dispatcher) ExecutionGeneration(taskID, projectPath string) (int, error) {
	gen, _, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	return gen, nil
}

// QueueTask adds a task to the execution queue and returns the execution ID.
// The task will be executed by the project's worker in FIFO order.
// If a decomposer is configured and the task is complex, it will be split
// into subtasks that are queued instead of the parent task.
func (d *Dispatcher) QueueTask(ctx context.Context, task *Task) (string, error) {
	// GH-4347: serialize the duplicate check below with its own insert so two
	// concurrent QueueTask calls for the same task_id/project_path can't both
	// pass IsTaskQueued before either row lands. See dispatchMu's doc comment.
	d.dispatchMu.Lock()
	defer d.dispatchMu.Unlock()

	// Check for duplicate tasks (GH-4276: scoped to this task's project)
	exists, err := d.store.IsTaskQueued(task.ID, task.ProjectPath)
	if err != nil {
		d.log.Warn("Failed to check for duplicate task", slog.Any("error", err))
	} else if exists {
		return "", fmt.Errorf("task %s: %w", task.ID, ErrTaskAlreadyActive)
	}

	// Try decomposition if decomposer is configured
	if d.decomposer != nil {
		result := d.decomposer.Decompose(task)
		if result.Decomposed && len(result.Subtasks) > 1 {
			return d.queueDecomposedTask(ctx, task, result)
		}
		// GH-4271: an epic-classified (or otherwise at/above min_complexity)
		// task that does NOT enter decomposition previously left zero trace
		// at this queue-time decision point — see the matching runner.go
		// Execute() site for the direct-execution-time equivalent. execID
		// only exists once queueSingleTask creates the executions row below,
		// so the event write follows it rather than preceding it.
		if d.decomposer.ReportableSkip(result) {
			detail := d.decomposer.SkipLogDetail(result)
			d.log.Info(detail,
				slog.String("task_id", task.ID),
				slog.String("skip_reason", string(result.SkipReason)),
				slog.String("complexity", result.Complexity.String()),
			)
			execID, err := d.queueSingleTask(ctx, task)
			if err == nil && execID != "" {
				// execID == "" with a nil error means queueSingleTask dropped
				// an ErrClaimLost pickup silently — no executions row exists
				// here to attach this stage event to (GH-4359).
				d.recordExecutionEvent(execID, memory.StageDecompositionSkipped, detail)
			}
			return execID, err
		}
	}

	// Queue single task
	return d.queueSingleTask(ctx, task)
}

// dispatcherRepickBackoffBaseInterval and dispatcherRepickBackoffMaxShift
// mirror the growth policy in cmd/pilot/repick_backoff.go (#4385) exactly.
// Duplicated rather than imported — cmd/pilot depends on this package, not
// the reverse, so importing it here would create a cycle — but both sides
// read/write the SAME repick_backoff store row (repickBackoffKey below
// matches cmd/pilot's), so as long as the formulas agree a repick from
// EITHER entry point grows the SAME cooldown (GH-4394 subtask 2).
const (
	dispatcherRepickBackoffBaseInterval = 30 * time.Second
	dispatcherRepickBackoffMaxShift     = 5
)

// dispatcherRepickHardCap is the hard ceiling on consecutive repicks for one
// task before beginWithGenerationRetry stops retrying altogether and marks
// the task's claimed execution "stalled" instead (GH-4394 subtask 5).
// Exponential backoff (capped at dispatcherRepickBackoffMaxShift, i.e.
// dispatcherRepickBackoffBaseInterval*32 ≈ 16 minutes) only slows repeats
// down — left unbounded, a task that can never succeed keeps burning a real
// backend execution every ~16 minutes forever. GH-85 hit 5 consecutive
// repicks in ~15 minutes with NO backoff growth at all before anyone
// noticed; matches cmd/pilot's repickBackoffWarnThreshold (the point at
// which that package's own log line escalates to WARN) so the WARN and the
// hard stop land on the same incident, not two different thresholds someone
// has to reconcile later.
const dispatcherRepickHardCap = 5

// dispatcherStallRepickCap is the hard ceiling on consecutive stall-watchdog
// kills for one task before beginWithGenerationRetry stops granting
// stall-carved-out retries and stalls the task instead (GH-4502). Stall-kills
// are counted separately from dispatcherRepickHardCap's consecutiveDrops
// (see priorClaimWasStalled) because a stall-kill is not evidence the task's
// code is broken — it's a healthy session sitting in a long silent model
// turn that the watchdog killed defensively (incident: pilot-console GH-24,
// 4 identical stall-kills wedged a healthy task at the shared hard cap and
// required a manual re-arm). But this must not be an unlimited bypass
// either: until the silent-turn stall root cause ships (tracked separately),
// a complex-lane task can stall deterministically on every generation, and a
// pure bypass would retry forever, burning tokens on every attempt. Set
// higher than dispatcherRepickHardCap (8 vs 5) since stalls are a weaker
// signal of a doomed task than genuine failures are.
const dispatcherStallRepickCap = 8

// dispatcherInfraRepickCap is the hard ceiling on consecutive
// infra-classified repicks for one task before beginWithGenerationRetry
// stops granting infra-carved-out retries and stalls the task instead
// (GH-4540/TASK-421). Infra-classified failures (status "infra" — a hosted
// preflight deadlock, a CI outage) are counted separately from
// dispatcherRepickHardCap's consecutiveDrops (see priorClaimWasInfra)
// because they are not evidence the task's own code is broken (incident:
// GH-4526 accrued drops from environment failures alone). Set to the same
// value as dispatcherStallRepickCap: both are weaker signals of a doomed
// task than a genuine code failure, and reusing the established number
// avoids inventing a third arbitrary threshold.
const dispatcherInfraRepickCap = 8

// repickBackoffKey namespaces backoff state by project path + task ID,
// matching cmd/pilot's repickBackoffKey — task_id alone is not unique across
// projects (GH-4276).
func repickBackoffKey(projectPath, taskID string) string {
	return projectPath + "|" + taskID
}

// priorClaimWasOperatorCancelled reports whether (taskID, projectPath)'s
// currently claimed execution — the one nextRetryGeneration just examined to
// grant this retry — has status "cancelled". There is no in-repo call site
// that writes "cancelled" (see store.go's UpdateExecutionStatus comment); it
// is a value an operator sets by hand (direct DB write) to unblock a wedged
// head-of-queue task. GH-4454 subtask 2: that manual intervention must not
// be treated as a failure by beginWithGenerationRetry's hard-cap accounting.
// Errors are treated as "not cancelled" — the caller falls through to the
// ordinary backoff/hard-cap path, which is the safe default.
func (d *Dispatcher) priorClaimWasOperatorCancelled(taskID, projectPath string) bool {
	_, execID, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil || !found {
		return false
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil {
		return false
	}
	return exec.Status == "cancelled"
}

// priorClaimWasStalled reports whether (taskID, projectPath)'s currently
// claimed execution — the one nextRetryGeneration just examined to grant
// this retry — was killed by the stall watchdog (status "stalled"). A
// stall-kill is not evidence the task's code is broken: it's a healthy
// session sitting in a long silent model turn that got killed defensively
// (the #4484 outcome taxonomy classifies stall as its own class in
// runner.go, but that classification was telemetry-only until GH-4502).
// Left uncarved-out, each stall-kill grew the SAME consecutiveDrops counter
// a real failure would, so 4 consecutive stall-kills wedged a healthy task
// at dispatcherRepickHardCap (incident: pilot-console GH-24). Errors are
// treated as "not stalled" — the caller falls through to the ordinary
// backoff/hard-cap path, which is the safe default.
func (d *Dispatcher) priorClaimWasStalled(taskID, projectPath string) bool {
	_, execID, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil || !found {
		return false
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil {
		return false
	}
	return exec.Status == string(ExecStatusStalled)
}

// priorClaimWasInfra reports whether (taskID, projectPath)'s currently
// claimed execution — the one nextRetryGeneration just examined to grant
// this retry — was classified as an infrastructure/environment failure
// (status "infra") rather than a genuine code failure. GH-4526 accrued
// consecutive_drops purely from environment failures (a hosted git_clean
// preflight deadlock and a CI infra outage) — the task's code was never at
// fault, but each drop counted identically to a real failure toward
// dispatcherRepickHardCap. Mirrors priorClaimWasStalled exactly, for the
// "infra" status instead of "stalled". Errors are treated as "not infra" —
// the caller falls through to the ordinary backoff/hard-cap path, which is
// the safe default.
func (d *Dispatcher) priorClaimWasInfra(taskID, projectPath string) bool {
	_, execID, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil || !found {
		return false
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil {
		return false
	}
	return exec.Status == string(ExecStatusInfra)
}

// consecutiveIdenticalFailureThreshold is N in "the last N consecutive
// generations for the same (task_id, project_path) failed with the exact
// same error string" (GH-4586). A single failure could be any kind of
// one-off transient blip, but the SAME error string twice in a row means the
// retry changed nothing about the outcome — a third identical attempt is not
// going to behave differently either.
const consecutiveIdenticalFailureThreshold = 2

// deterministicFailureReasonPrefix / identicalFailureStreakReasonPrefix tag
// the reason string escalateStalledTask persists to the claimed execution's
// Error column for the two GH-4586 operator-attention triggers below. They
// exist so priorClaimWasEscalatedForOperatorAttention (consulted on every
// later beginWithGenerationRetry call for the same claim) can recognize "this
// stalled status was OUR escalation" and stay sticky, instead of falling
// through to priorClaimWasStalled and minting free stall-carve-out retries
// forever — the exact bypass GH-4502's ordering comment (see the hard-cap
// check above priorClaimWasStalled) already had to guard against for the
// repick-hard-cap escalation.
const (
	deterministicFailureReasonPrefix   = "deterministic failure (will not retry): "
	identicalFailureStreakReasonPrefix = "consecutive identical failures (will not retry): "
)

// priorClaimWasDeterministicFailure reports whether (taskID, projectPath)'s
// currently claimed execution — the one nextRetryGeneration just examined to
// grant this retry — failed (status "failed") with a deterministic error
// class per IsDeterministicFailure (a "blocked:" hard-guard veto, or any
// pattern IsPermanentFailure already flags). Such a failure reproduces
// identically on a bare retry — the task's own spec or a hard guard rejected
// it, not a transient environment blip — so beginWithGenerationRetry must
// not spend a fresh generation reproducing it. GH-4586. Returns the prior
// error string alongside the bool so the caller can surface it verbatim in
// the operator-attention reason. Errors are treated as "not deterministic" —
// the caller falls through to the ordinary backoff/hard-cap path, which is
// the safe default.
func (d *Dispatcher) priorClaimWasDeterministicFailure(taskID, projectPath string) (bool, string) {
	_, execID, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil || !found {
		return false, ""
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil || exec == nil {
		return false, ""
	}
	if exec.Status != string(ExecStatusFailed) || !IsDeterministicFailure(exec.Error) {
		return false, ""
	}
	return true, exec.Error
}

// priorClaimsHadIdenticalFailureStreak reports whether the most recent
// consecutiveIdenticalFailureThreshold executions recorded for (taskID,
// projectPath) all have status "failed" and share the exact same, non-empty
// Error string — independent of whether that error matches a known
// deterministic class (GH-4586). Executions are read newest-first via
// ListExecutionsForTask, which mirrors how beginWithGenerationRetry itself
// creates one fresh execution row per granted generation, so the newest rows
// line up with the newest generations. Two identical failures in a row means
// the retry changed nothing about the outcome, so granting a third
// generation is unlikely to behave differently either. Errors (or fewer than
// threshold rows) are treated as "no streak" — the caller falls through to
// the ordinary backoff/hard-cap path, which is the safe default.
func (d *Dispatcher) priorClaimsHadIdenticalFailureStreak(taskID, projectPath string) (bool, string) {
	execs, err := d.store.ListExecutionsForTask(taskID, projectPath)
	if err != nil || len(execs) < consecutiveIdenticalFailureThreshold {
		return false, ""
	}
	recent := execs[:consecutiveIdenticalFailureThreshold]
	firstErr := recent[0].Error
	if firstErr == "" {
		return false, ""
	}
	for _, exec := range recent {
		if exec.Status != string(ExecStatusFailed) || exec.Error != firstErr {
			return false, ""
		}
	}
	return true, firstErr
}

// priorClaimWasEscalatedForOperatorAttention reports whether (taskID,
// projectPath)'s currently claimed execution was already routed to the
// GH-4586 operator-attention path (status "stalled" with a reason string
// carrying deterministicFailureReasonPrefix or
// identicalFailureStreakReasonPrefix) by a previous call to
// escalateDeterministicFailure / escalateIdenticalFailureStreak. Consulted
// BEFORE priorClaimWasStalled so a later poll tick — which would otherwise
// see status "stalled" on the same claimed execution and (wrongly) treat it
// as a genuine watchdog stall-kill eligible for the stall carve-out — stays
// pinned to the operator-attention path instead of minting free retries
// forever. Mirrors the ordering discipline the GH-4502 hard-cap check above
// priorClaimWasStalled already established for exactly this class of bypass.
func (d *Dispatcher) priorClaimWasEscalatedForOperatorAttention(taskID, projectPath string) (bool, string) {
	_, execID, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil || !found {
		return false, ""
	}
	exec, err := d.store.GetExecution(execID)
	if err != nil || exec == nil || exec.Status != string(ExecStatusStalled) {
		return false, ""
	}
	if strings.HasPrefix(exec.Error, deterministicFailureReasonPrefix) ||
		strings.HasPrefix(exec.Error, identicalFailureStreakReasonPrefix) {
		return true, exec.Error
	}
	return false, ""
}

// nextRetryGeneration inspects the highest execution_claims generation
// currently held for (taskID, projectPath) and reports whether a fresh
// Begin(..., generation+1) is warranted (GH-4372). Three outcomes:
//   - no claim row at all, or the claimed execution is still LIVE
//     (queued/running): retry=false — this is the ordinary duplicate-pickup
//     race ErrClaimLost exists to catch; the caller must keep dropping it
//     silently.
//   - the claimed execution is terminal (dead — the owning run finished) AND
//     the task is already "done" per HasTerminalCompletion (a genuine
//     deliverable, or a no_op with no error): retry=false — preserves
//     GH-4350's invariant that a no_op'd task must never be re-armed.
//   - the claimed execution is terminal but the task is NOT yet done (e.g.
//     failed, stalled): retry=true, generation = claimed generation + 1.
func (d *Dispatcher) nextRetryGeneration(taskID, projectPath string) (generation int, retry bool, err error) {
	gen, execID, found, err := d.store.LatestClaimGeneration(taskID, projectPath)
	if err != nil || !found {
		return 0, false, err
	}

	claimedExec, err := d.store.GetExecution(execID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	// GH-4409: sql.ErrNoRows means the claim row survives but the executions
	// row it names does not — either Begin's save-failure case (claim landed,
	// the executions INSERT never did) or a dangling claim left behind by
	// deleteOrphanRunningRow / boot reconciliation deleting an execution row
	// without pruning its claims (both current call sites only delete rows
	// they've already confirmed are done, but a future caller deleting a
	// claimed row for a NOT-done task must not wedge that task forever).
	// Neither case is a live owner, so fall through to the done-check below
	// instead of the old conservative "treat as still owned" — that
	// short-circuited before HasTerminalCompletion could ever run, so a
	// not-done task behind a dangling claim retried nothing, silently,
	// forever.
	if err == nil && !isTerminalExecutionStatus(claimedExec.Status) {
		return 0, false, nil // live owner — today's ErrClaimLost drop path
	}

	done, err := HasTerminalCompletion(d.store, taskID, projectPath)
	if err != nil {
		return 0, false, err
	}
	if done {
		return 0, false, nil // GH-4350: already "done" — must not re-arm
	}

	return gen + 1, true, nil
}

// beginWithGenerationRetry wraps ExecutionLifecycle.Begin(task, initial) so a
// claim loss caused by a DEAD prior claim (the owning execution finished, but
// the task isn't done yet — e.g. it failed) automatically retries at
// generation+1 instead of retrying generation 0 forever. GH-4372: a task
// that failed once was permanently unable to re-pick, since its claim on
// generation 0 was already permanently occupied by the terminal failure and
// every re-pick attempt kept retrying that same occupied generation.
//
// Returns ("", nil) for the two "drop this pickup silently" outcomes callers
// already handle identically: a live owner (today's ErrClaimLost duplicate-
// pickup case) or a dead owner whose task is already done and must not be
// re-armed (GH-4350's no_op invariant). A genuine store error still
// surfaces.
func (d *Dispatcher) beginWithGenerationRetry(task *Task, initial Status) (string, error) {
	lifecycle := NewExecutionLifecycle(d.store)
	execID, err := lifecycle.Begin(task, initial)
	if err == nil {
		return execID, nil
	}
	if !errors.Is(err, ErrClaimLost) {
		return "", err
	}

	gen, retry, decErr := d.nextRetryGeneration(task.ID, task.ProjectPath)
	if decErr != nil {
		d.log.Warn("failed to evaluate retry generation after claim loss — dropping pickup",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Any("error", decErr))
		return "", nil
	}
	if !retry {
		return "", nil
	}

	backoffKey := repickBackoffKey(task.ProjectPath, task.ID)

	// GH-4454 subtask 2: an operator manually cancelling a wedged
	// head-of-queue execution to unblock the lane is not evidence the task
	// can't succeed — unlike a genuine failure, it's deliberate human
	// intervention. Left uncounted-for, each cancel-and-repick cycle still
	// grew the SAME consecutiveDrops counter a real failure would, so an
	// operator trying to rescue a wedged task tripped
	// dispatcherRepickHardCap on their own unblock attempts and permanently
	// stalled the very task they were trying to save (GH-4454: 7h silent
	// idle after a wedged head issue starved its lane). Clear any
	// accumulated backoff state and grant the retry immediately, without
	// consulting or growing the hard-cap counter.
	if d.priorClaimWasOperatorCancelled(task.ID, task.ProjectPath) {
		if err := d.ClearRepickBackoffState(backoffKey); err != nil {
			d.log.Warn("failed to clear repick backoff state for operator-cancelled claim",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Any("error", err))
		}
		retryExecID, retryErr := lifecycle.Begin(task, initial, gen)
		if retryErr == nil {
			d.log.Info("dispatch re-pick: prior claim was operator-cancelled — claiming next generation without counting toward repick hard cap",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Int("generation", gen),
			)
			return retryExecID, nil
		}
		if errors.Is(retryErr, ErrClaimLost) {
			// Race: another channel claimed generation gen between our
			// decision and this Begin call — drop it, same as any other
			// duplicate pickup.
			return "", nil
		}
		return "", fmt.Errorf("failed to save retry execution: %w", retryErr)
	}

	// GH-4586: a prior poll tick already routed this claim to the
	// operator-attention path below (deterministic failure or an identical
	// failure streak) and marked it "stalled" as the hold marker — the exact
	// same status a genuine watchdog stall-kill uses. Recognizing "this is OUR
	// escalation" here, before priorClaimWasStalled ever runs, keeps the task
	// pinned to the operator-attention path instead of falling through to the
	// stall carve-out and minting a fresh, free generation on every later poll
	// tick (the same bypass class the GH-4502 hard-cap check above
	// priorClaimWasStalled already exists to prevent for that escalation).
	if wasEscalated, reason := d.priorClaimWasEscalatedForOperatorAttention(task.ID, task.ProjectPath); wasEscalated {
		d.escalateStalledTask(task, gen, 0, reason, nil)
		return "", nil
	}

	// GH-4586: a deterministic failure (a "blocked:" hard-guard veto, or any
	// class IsPermanentFailure already flags — e.g. the GH-4496 memory-doc
	// deletion veto) reproduces identically on a bare retry: the task's own
	// spec or a hard guard rejected the diff, not a transient environment
	// blip. Spending a fresh generation reproducing it only delays the
	// eventual repick-hard-cap stall while still burning consecutiveDrops
	// toward it — route straight to the operator-attention path instead, on
	// the very first occurrence, rather than waiting for
	// dispatcherRepickHardCap.
	if isDeterministic, errStr := d.priorClaimWasDeterministicFailure(task.ID, task.ProjectPath); isDeterministic {
		d.escalateDeterministicFailure(task, gen, errStr)
		return "", nil
	}

	// GH-4586: independent of error class, the last consecutiveIdenticalFailureThreshold
	// consecutive generations failing with the exact same error string means
	// the retry changed nothing about the outcome — the task is durably
	// stuck, not transiently unlucky. Route to the same operator-attention
	// path rather than burning another generation reproducing it a third
	// time.
	if hadStreak, errStr := d.priorClaimsHadIdenticalFailureStreak(task.ID, task.ProjectPath); hadStreak {
		d.escalateIdenticalFailureStreak(task, gen, errStr)
		return "", nil
	}

	// GH-4394 subtask 2: cmd/pilot's per-issue backoff (#4385) only gates
	// whether handleIssueGeneric calls QueueTask at all — it has no
	// visibility into a repick decided *inside* this call, and QueueTask's
	// only production caller IS handleIssueGeneric. That let this path
	// bypass the throttle entirely: every poll tick that got past the outer
	// gate found a fresh terminal-but-not-done claim here and re-armed
	// unconditionally (GH-85: 5 repicks in ~15 min, no backoff growth).
	// Consult the same persisted repick_backoff row before claiming a fresh
	// generation, so consecutive repicks back off exponentially too. Read
	// here (rather than after the GH-4502 stall check below) so the hard-cap
	// check that follows immediately can run BEFORE priorClaimWasStalled is
	// ever consulted — see that check's comment for why the order matters.
	//
	// GH-4394 subtask 3: GH-85 happened to be dispatched against the
	// registered pilot-canary-sandbox project (GH-4240/TASK-379), raising the
	// hypothesis that task.IsCanary/ProjectConfig.Canary might short-circuit
	// this gate the same way it deliberately short-circuits metrics recording
	// elsewhere (runner.go's `!task.IsCanary` guards). Investigated and ruled
	// out: this gate is keyed only on ProjectPath+TaskID (both stable,
	// config-registered values, identical for a canary or a real project) and
	// never inspects IsCanary. See
	// TestDispatcher_BeginWithGenerationRetry_ThrottlesCanaryProjectSameAsRegular.
	consecutiveDrops, nextAllowedAt, found, boErr := d.RepickBackoffState(backoffKey)
	if boErr != nil {
		d.log.Warn("failed to read repick backoff state — dropping retry to be safe",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Any("error", boErr))
		return "", nil
	}
	// GH-4394 subtask 5: exponential backoff alone never stops retrying — it
	// only slows the interval down, capping at ~16 min forever. A task stuck
	// past dispatcherRepickHardCap consecutive repicks is treated as a
	// permanent failure: stop granting new generations and mark it stalled
	// instead, so it stops burning a real backend execution on every window
	// expiry and instead waits for a human to investigate/re-arm it. Checked
	// before the GH-4502 stall check below (not just the backoff-window gate)
	// because escalating marks the claimed execution's status "stalled" as
	// its hold marker (escalateStalledTask) — indistinguishable, by status
	// alone, from a genuine stall-watchdog kill. Without this ordering, the
	// NEXT poll tick after a hard-cap escalation would see status "stalled"
	// on the same claimed execution, priorClaimWasStalled would (wrongly)
	// return true, and the stall carve-out below would keep minting fresh
	// generations forever — completely defeating the hard cap it just
	// tripped. Consulting consecutiveDrops (which only a genuine failure
	// grows, and which persists untouched across escalation) first keeps a
	// hard-capped task sticky regardless of what its execution's status flips
	// to afterward.
	if found && consecutiveDrops >= dispatcherRepickHardCap {
		d.stallTaskAfterRepickHardCap(task, gen, consecutiveDrops)
		return "", nil
	}

	// GH-4502: a stall-watchdog kill is not evidence the task's code is
	// broken — it's a healthy session sitting in a long silent model turn
	// that got killed defensively — so it must not grow the same
	// consecutiveDrops counter a genuine failure does (incident:
	// pilot-console GH-24, 4 stall-kills wedged a healthy task at
	// dispatcherRepickHardCap). Tracked in its own persisted counter with its
	// own, higher cap instead of an unlimited bypass: until the silent-turn
	// stall root cause ships, a complex-lane task could stall on every
	// generation, and an uncapped carve-out would retry forever. Only
	// consulted once the hard-cap check above has cleared (see its comment).
	if d.priorClaimWasStalled(task.ID, task.ProjectPath) {
		stallDrops, _, stallErr := d.StallDropCount(backoffKey)
		if stallErr != nil {
			d.log.Warn("failed to read stall drop count — dropping retry to be safe",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Any("error", stallErr))
			return "", nil
		}
		if stallDrops >= dispatcherStallRepickCap {
			d.stallTaskAfterStallCap(task, gen, stallDrops)
			return "", nil
		}

		retryExecID, retryErr := lifecycle.Begin(task, initial, gen)
		if retryErr == nil {
			newStallDrops := stallDrops + 1
			if setErr := d.SetStallDropCount(backoffKey, newStallDrops); setErr != nil {
				d.log.Warn("failed to persist stall drop count after re-pick",
					slog.String("task_id", task.ID),
					slog.String("project", task.ProjectPath),
					slog.Any("error", setErr))
			}
			// GH-4609 subtask 2: a fresh generation was just granted for this
			// stalled claim — finalize the PRIOR attempt's active-registry
			// (Monitor) entry right here instead of leaving it to age into
			// ReconcileDeadOwners' drain-time backstop. Normally runner.go's
			// own Stall() call already did this when the watchdog fired; this
			// is a no-op in that case (ReleasePriorAttempt only touches a
			// still-non-terminal entry) and only matters when that call
			// hasn't landed (or never will — e.g. the worker process died
			// before reaching it), so the retried task is counted exactly
			// once, under the new generation, not still under the superseded
			// one.
			if d.runner != nil && d.runner.monitor != nil {
				d.runner.monitor.ReleasePriorAttempt(task.ID, fmt.Sprintf("stall-retry: superseded by generation %d", gen))
			}
			d.log.Info("dispatch re-pick: prior claim was stall-killed — claiming next generation without counting toward repick hard cap",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Int("generation", gen),
				slog.Int("consecutive_stall_drops", newStallDrops),
			)
			return retryExecID, nil
		}
		if errors.Is(retryErr, ErrClaimLost) {
			// Race: another channel claimed generation gen between our
			// decision and this Begin call — drop it, same as any other
			// duplicate pickup.
			return "", nil
		}
		return "", fmt.Errorf("failed to save retry execution: %w", retryErr)
	}

	// GH-4540/TASK-421: an infra-classified failure (status "infra" — e.g. a
	// hosted git_clean preflight deadlock or a CI outage) is not evidence the
	// task's own code is broken, exactly mirroring the stall carve-out above
	// (incident: GH-4526 wedged a healthy task at dispatcherRepickHardCap on
	// environment failures alone). Tracked in its own persisted counter with
	// its own cap rather than an unlimited bypass, for the same reason the
	// stall carve-out is capped: a task whose environment is durably broken
	// (not just transiently flaky) must still stop retrying eventually.
	if d.priorClaimWasInfra(task.ID, task.ProjectPath) {
		infraDrops, _, infraErr := d.InfraDropCount(backoffKey)
		if infraErr != nil {
			d.log.Warn("failed to read infra drop count — dropping retry to be safe",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Any("error", infraErr))
			return "", nil
		}
		if infraDrops >= dispatcherInfraRepickCap {
			d.stallTaskAfterInfraCap(task, gen, infraDrops)
			return "", nil
		}

		retryExecID, retryErr := lifecycle.Begin(task, initial, gen)
		if retryErr == nil {
			newInfraDrops := infraDrops + 1
			if setErr := d.SetInfraDropCount(backoffKey, newInfraDrops); setErr != nil {
				d.log.Warn("failed to persist infra drop count after re-pick",
					slog.String("task_id", task.ID),
					slog.String("project", task.ProjectPath),
					slog.Any("error", setErr))
			}
			d.log.Info("dispatch re-pick: prior claim was infra-classified — claiming next generation without counting toward repick hard cap",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Int("generation", gen),
				slog.Int("consecutive_infra_drops", newInfraDrops),
			)
			return retryExecID, nil
		}
		if errors.Is(retryErr, ErrClaimLost) {
			// Race: another channel claimed generation gen between our
			// decision and this Begin call — drop it, same as any other
			// duplicate pickup.
			return "", nil
		}
		return "", fmt.Errorf("failed to save retry execution: %w", retryErr)
	}

	if found && time.Now().Before(nextAllowedAt) {
		d.log.Info("dispatch re-pick throttled — task still within repick backoff window, dropping duplicate retry",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Int("consecutive_drops", consecutiveDrops),
			slog.Time("next_allowed_at", nextAllowedAt),
		)
		return "", nil
	}

	retryExecID, retryErr := lifecycle.Begin(task, initial, gen)
	if retryErr == nil {
		newConsecutive := consecutiveDrops + 1
		shift := newConsecutive - 1
		if shift > dispatcherRepickBackoffMaxShift {
			shift = dispatcherRepickBackoffMaxShift
		}
		newNextAllowedAt := time.Now().Add(dispatcherRepickBackoffBaseInterval * time.Duration(uint64(1)<<uint(shift)))
		if setErr := d.SetRepickBackoffState(backoffKey, newConsecutive, newNextAllowedAt); setErr != nil {
			d.log.Warn("failed to persist repick backoff growth after re-pick",
				slog.String("task_id", task.ID),
				slog.String("project", task.ProjectPath),
				slog.Any("error", setErr))
		}
		d.log.Info("dispatch re-pick: prior claim was terminal but task is not done — claiming next generation for retry",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Int("generation", gen),
			slog.Int("consecutive_repicks", newConsecutive),
		)
		return retryExecID, nil
	}
	if errors.Is(retryErr, ErrClaimLost) {
		// Race: another channel claimed generation gen between our decision
		// and this Begin call — drop it, same as any other duplicate pickup.
		return "", nil
	}
	return "", fmt.Errorf("failed to save retry execution: %w", retryErr)
}

// stallTaskAfterRepickHardCap marks (taskID, projectPath)'s currently claimed
// execution "stalled" and raises an alert once dispatcherRepickHardCap
// consecutive repicks have been exhausted (GH-4394 subtask 5) — the hard
// stop that replaces "retry forever, just slower" once exponential backoff
// alone has proven the task can't succeed on its own.
//
// Idempotent by design: if the claimed execution is already "stalled" (a
// prior poll tick already tripped this same cap), the ledger write and alert
// are skipped so a task sitting past the cap doesn't re-alert on every
// backoff-window expiry — it stays quiet until a human re-arms it (e.g. via
// SetRepickBackoffState/ClearRepickBackoffState) or the underlying issue is
// closed.
func (d *Dispatcher) stallTaskAfterRepickHardCap(task *Task, gen, consecutiveDrops int) {
	reason := fmt.Sprintf(
		"repick backoff hard cap reached: %d consecutive failed re-picks (cap=%d) — stopping automatic retries, manual re-arm required",
		consecutiveDrops, dispatcherRepickHardCap,
	)
	d.escalateStalledTask(task, gen, consecutiveDrops, reason, map[string]string{
		"reason":            "repick_hard_cap_stalled",
		"consecutive_drops": fmt.Sprintf("%d", consecutiveDrops),
		"hard_cap":          fmt.Sprintf("%d", dispatcherRepickHardCap),
	})
}

// stallTaskAfterStallCap marks the task's claimed execution "stalled" and
// raises an alert once dispatcherStallRepickCap consecutive stall-watchdog
// kills have been exhausted (GH-4502) — the same escalate-and-hold path
// stallTaskAfterRepickHardCap takes for genuine failures, but with a
// distinct, truthful reason string: a run of stall-kills means the session
// keeps getting killed mid-turn, not that the code itself is failing, and an
// operator investigating must be able to tell those two classes apart at a
// glance instead of reading the generic hard-cap message and assuming the
// task's code is broken.
func (d *Dispatcher) stallTaskAfterStallCap(task *Task, gen, stallDrops int) {
	reason := fmt.Sprintf(
		"stall repick cap reached: %d consecutive stall-watchdog kills (cap=%d) — the session keeps stalling mid-turn, not failing; stopping automatic retries, manual re-arm required",
		stallDrops, dispatcherStallRepickCap,
	)
	d.escalateStalledTask(task, gen, stallDrops, reason, map[string]string{
		"reason":      "stall_repick_cap_stalled",
		"stall_drops": fmt.Sprintf("%d", stallDrops),
		"stall_cap":   fmt.Sprintf("%d", dispatcherStallRepickCap),
	})
}

// stallTaskAfterInfraCap marks the task's claimed execution "stalled" and
// raises an alert once dispatcherInfraRepickCap consecutive
// infra-classified repicks have been exhausted (GH-4540/TASK-421) — the same
// escalate-and-hold path stallTaskAfterRepickHardCap/stallTaskAfterStallCap
// take, but with a distinct, truthful reason string: a run of
// infra-classified drops means the environment kept failing the task, not
// that the task's own code is broken.
func (d *Dispatcher) stallTaskAfterInfraCap(task *Task, gen, infraDrops int) {
	reason := fmt.Sprintf(
		"infra repick cap reached: %d consecutive infra-classified failures (cap=%d) — the environment keeps failing this task, not the task's code; stopping automatic retries, manual re-arm required",
		infraDrops, dispatcherInfraRepickCap,
	)
	d.escalateStalledTask(task, gen, infraDrops, reason, map[string]string{
		"reason":      "infra_repick_cap_stalled",
		"infra_drops": fmt.Sprintf("%d", infraDrops),
		"infra_cap":   fmt.Sprintf("%d", dispatcherInfraRepickCap),
	})
}

// escalateDeterministicFailure routes a task whose prior claim failed with a
// deterministic error class (priorClaimWasDeterministicFailure) straight to
// the operator-attention path via escalateStalledTask, without granting a
// fresh generation — GH-4586. Fires on the first occurrence rather than
// waiting for dispatcherRepickHardCap, since a deterministic failure is
// already known to reproduce identically on retry. The reason string is
// tagged with deterministicFailureReasonPrefix so
// priorClaimWasEscalatedForOperatorAttention can recognize this escalation
// on later poll ticks and stay pinned to this path instead of falling
// through to the stall carve-out.
func (d *Dispatcher) escalateDeterministicFailure(task *Task, gen int, errStr string) {
	reason := deterministicFailureReasonPrefix + errStr
	d.escalateStalledTask(task, gen, 0, reason, map[string]string{
		"reason":      "deterministic_failure_stalled",
		"prior_error": errStr,
	})
}

// escalateIdenticalFailureStreak routes a task to the operator-attention
// path via escalateStalledTask when the last consecutiveIdenticalFailureThreshold
// generations failed with the exact same error string, independent of
// whether that error matches a known deterministic class — GH-4586. The
// reason string is tagged with identicalFailureStreakReasonPrefix so
// priorClaimWasEscalatedForOperatorAttention can recognize this escalation
// on later poll ticks and stay pinned to this path instead of falling
// through to the stall carve-out.
func (d *Dispatcher) escalateIdenticalFailureStreak(task *Task, gen int, errStr string) {
	reason := identicalFailureStreakReasonPrefix + errStr
	d.escalateStalledTask(task, gen, 0, reason, map[string]string{
		"reason":            "identical_failure_streak_stalled",
		"consecutive_count": fmt.Sprintf("%d", consecutiveIdenticalFailureThreshold),
		"prior_error":       errStr,
	})
}

// escalateStalledTask marks (taskID, projectPath)'s currently claimed
// execution "stalled" and raises an alert with reason/alertMetadata, shared
// by stallTaskAfterRepickHardCap (genuine-failure hard cap) and
// stallTaskAfterStallCap (stall-kill cap, GH-4502) so both drop classes take
// the identical escalate-and-hold path and differ only in the reason string
// and alert metadata surfaced to the operator.
//
// Idempotent by design: if the claimed execution is already "stalled" (a
// prior poll tick already tripped this same cap), the ledger write and alert
// are skipped so a task sitting past the cap doesn't re-alert on every
// backoff-window expiry — it stays quiet until a human re-arms it (e.g. via
// SetRepickBackoffState/ClearRepickBackoffState) or the underlying issue is
// closed.
func (d *Dispatcher) escalateStalledTask(task *Task, gen, dropCount int, reason string, alertMetadata map[string]string) {
	_, execID, found, err := d.store.LatestClaimGeneration(task.ID, task.ProjectPath)
	if err != nil || !found || execID == "" {
		d.log.Warn("repick cap reached but no claimed execution found to stall",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
			slog.Int("drop_count", dropCount),
			slog.Any("error", err))
		return
	}

	// GH-4502: matching on the exact prior reason text (not just
	// Status=="stalled") matters because the stall-cap class is ITSELF
	// triggered by a claimed execution whose status is already "stalled" (a
	// genuine, fresh watchdog kill) — Status alone can't tell that apart from
	// "already escalated by a prior call to this same function". A prior
	// call always leaves the exact reason string (dropCount/cap baked in) as
	// Error, so an unchanged dropCount on a repeat tick reproduces the
	// identical string and is correctly recognized as a repeat; a genuine
	// fresh stall-kill carries the runner's own message instead and never
	// matches.
	claimedExec, getErr := d.store.GetExecution(execID)
	alreadyStalled := getErr == nil && claimedExec != nil &&
		claimedExec.Status == string(ExecStatusStalled) && claimedExec.Error == reason

	if uerr := d.store.UpdateExecutionStatus(execID, string(ExecStatusStalled), reason); uerr != nil {
		d.log.Warn("failed to mark execution stalled after repick cap",
			slog.String("task_id", task.ID), slog.String("execution_id", execID), slog.Any("error", uerr))
	}

	if alreadyStalled {
		return
	}

	d.recordExecutionEvent(execID, memory.StageStalled, reason)
	d.log.Warn("repick cap reached — task marked stalled, no further automatic retries",
		slog.String("task_id", task.ID),
		slog.String("project", task.ProjectPath),
		slog.Int("drop_count", dropCount),
		slog.Int("generation", gen),
		slog.String("reason", reason),
	)
	d.surfaceStalledIssue(task, reason)
	if d.runner != nil {
		d.runner.EmitAlertEvent(AlertEvent{
			Type:      AlertEventTypeTaskFailed,
			TaskID:    task.ID,
			TaskTitle: task.Title,
			Project:   task.ProjectPath,
			Error:     reason,
			Metadata:  alertMetadata,
			Timestamp: time.Now(),
		})
	}
}

// surfaceStalledIssue labels a repick-hard-cap-stalled task's GitHub issue
// pilot-blocked (removing pilot-failed/pilot-in-progress) and posts an
// explanatory comment — GH-4454 subtask 3.
//
// Why this matters: studio-sdk's GitHub poller groups open "pilot"-labeled
// issues by shared directory reference (scope overlap) and, within each
// group, dispatches only the OLDEST issue every poll tick, deferring every
// other issue in that group. That grouping/ordering has no visibility into
// this dispatcher's independent repick_backoff hard cap — it re-admits a
// pilot-failed issue as a candidate via its own separate retry counter,
// unaware the store-side execution behind it was already stalled above.
// Being the oldest issue in its scope cluster, the stalled head keeps
// winning the dispatch slot on every tick, silently starving every issue
// that shares its scope forever (GH-4454: 7h idle after exactly this). The
// poller DOES unconditionally exclude any issue carrying pilot-blocked from
// its candidate list before scope grouping ever runs, so applying that label
// here removes the stalled issue from contention entirely and lets the
// next-oldest overlapping issue through instead of silently starving.
//
// Best-effort and GitHub-only: a labeling/comment failure is logged, not
// fatal — the store-side "stalled" status the caller already wrote is the
// durable source of truth regardless of whether this side channel succeeds.
func (d *Dispatcher) surfaceStalledIssue(task *Task, reason string) {
	if task.SourceAdapter != "" && task.SourceAdapter != "github" {
		return
	}
	issueNum := strings.TrimPrefix(task.ID, "GH-")
	if task.SourceIssueID != "" {
		issueNum = task.SourceIssueID
	}
	if issueNum == "" {
		return
	}
	var parsed int
	if _, err := fmt.Sscanf(issueNum, "%d", &parsed); err != nil || parsed <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	comment := fmt.Sprintf(
		"Pilot stopped retrying (repick hard cap): %s\n\n"+
			"Labeled `pilot-blocked` so this issue stops winning scope-overlap "+
			"dispatch priority over sibling issues that touch the same files. "+
			"To re-arm after fixing the underlying blocker:\n```\ngh issue edit %d --remove-label pilot-blocked --remove-label pilot-failed --add-label pilot-retry-ready\n```",
		reason, parsed,
	)
	if err := ghIssueComment(ctx, task.ProjectPath, issueNum, comment); err != nil {
		d.log.Warn("stalled-issue surfacing: failed to post comment",
			slog.String("task_id", task.ID), slog.Any("error", err))
	}
	if err := ghEditLabels(ctx, task.ProjectPath, issueNum, []string{"pilot-blocked"}, []string{"pilot-failed", "pilot-in-progress"}); err != nil {
		d.log.Warn("stalled-issue surfacing: failed to update labels",
			slog.String("task_id", task.ID), slog.Any("error", err))
	}
}

// queueDecomposedTask handles queuing a decomposed task and its subtasks.
// The parent task is marked as "decomposed" and subtasks are queued in order.
func (d *Dispatcher) queueDecomposedTask(ctx context.Context, parent *Task, result *DecomposeResult) (string, error) {
	// GH-4243: single chokepoint for the row create + ExecutionID threading
	// that used to be hand-rolled here. GH-4372: beginWithGenerationRetry
	// additionally claims generation+1 when the prior claim belongs to a
	// dead (terminal, not-yet-done) execution instead of retrying generation
	// 0 forever.
	parentExecID, err := d.beginWithGenerationRetry(parent, ExecStatusDecomposed)
	if err != nil {
		return "", fmt.Errorf("failed to save decomposed parent: %w", err)
	}
	if parentExecID == "" {
		// TASK-407/GH-4359, GH-4372: dropped duplicate pickup — either
		// another dispatch channel (e.g. the epic sub-issue loop or a
		// racing poller pass) already owns a LIVE execution for
		// (parent.ID, parent.ProjectPath), or the parent task is already
		// terminal-done (GH-4350: must not re-arm a no_op). Unlike epic.go's
		// sub-issue loop, this call site has no wrapping execution row of
		// its own to poll or attach a ledger event to — the parent task IS
		// the top-level dispatch. Drop the duplicate pickup silently: the
		// execution is already owned elsewhere, so re-queuing here would
		// either FK-787 (no executions row to reference) or start a
		// genuine duplicate run.
		d.log.Info("dispatch claim lost — decomposed parent already owned by another dispatch channel or already terminal, dropping duplicate pickup",
			slog.String("task_id", parent.ID),
			slog.String("project", parent.ProjectPath),
		)
		return "", nil
	}

	d.log.Info("Task decomposed",
		slog.String("parent_id", parent.ID),
		slog.Int("subtask_count", len(result.Subtasks)),
		slog.String("reason", result.Reason),
	)

	// Emit progress for parent
	d.runner.EmitProgress(parent.ID, "Decomposed", 0,
		fmt.Sprintf("Split into %d subtasks", len(result.Subtasks)))

	// Queue each subtask
	var lastExecID string
	for i, subtask := range result.Subtasks {
		execID, err := d.queueSingleTask(ctx, subtask)
		if err != nil {
			d.log.Error("Failed to queue subtask",
				slog.String("subtask_id", subtask.ID),
				slog.Int("index", i),
				slog.Any("error", err),
			)
			continue
		}
		lastExecID = execID
	}

	// Return parent execution ID
	if lastExecID == "" {
		return parentExecID, nil
	}
	return parentExecID, nil
}

// queueSingleTask queues a single task (no decomposition).
func (d *Dispatcher) queueSingleTask(ctx context.Context, task *Task) (string, error) {
	// GH-4243: single chokepoint for the row create + ExecutionID threading
	// that used to be hand-rolled here. GH-4372: beginWithGenerationRetry
	// additionally claims generation+1 when the prior claim belongs to a
	// dead (terminal, not-yet-done) execution instead of retrying generation
	// 0 forever — this is the poller's primary re-pick path.
	execID, err := d.beginWithGenerationRetry(task, ExecStatusQueued)
	if err != nil {
		return "", fmt.Errorf("failed to save execution: %w", err)
	}
	if execID == "" {
		// TASK-407/GH-4359, GH-4372: dropped duplicate pickup — either
		// another dispatch channel already claimed a LIVE execution for
		// (task.ID, task.ProjectPath) — most likely a concurrently-racing
		// epic sub-issue loop or CLI stub picking up the same task_id
		// outside this dispatcher's dispatchMu-serialized path — or the
		// task is already terminal-done (GH-4350: must not re-arm a
		// no_op). Drop this pickup silently (idempotent pickup): the
		// execution is already owned elsewhere, and proceeding here would
		// either FK-787 (no executions row for this call to reference) or
		// start a genuine duplicate run. Returning a nil error — rather
		// than wrapping ErrClaimLost like ErrTaskAlreadyActive — means
		// callers (including the decomposed-subtask loop below) treat this
		// exactly like an already-handled task, not a failure to log or
		// retry.
		d.log.Info("dispatch claim lost — task already owned by another dispatch channel or already terminal, dropping duplicate pickup",
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
		)
		return "", nil
	}

	// GH-3732: surface per-project serialization instead of leaving a queued
	// task invisible until its turn comes up — log what it's waiting behind.
	if blockedBy, position, busy := d.queueBlockInfo(task.ProjectPath); busy {
		d.log.Info(fmt.Sprintf("task queued behind %s (position %d in %s queue)",
			blockedBy, position, filepath.Base(task.ProjectPath)),
			slog.String("execution_id", execID),
			slog.String("task_id", task.ID),
			slog.String("blocked_by", blockedBy),
			slog.Int("position", position),
			slog.String("project", task.ProjectPath),
		)
	} else {
		d.log.Info("Task queued",
			slog.String("execution_id", execID),
			slog.String("task_id", task.ID),
			slog.String("project", task.ProjectPath),
		)
	}

	// Emit progress callback for task queued
	d.runner.EmitProgress(task.ID, "Queued", 0, fmt.Sprintf("Task queued (exec: %s)", execID[:8]))

	// Ensure worker exists and signal it
	d.ensureWorker(task.ProjectPath)

	return execID, nil
}

// queueBlockInfo reports whether the project's worker is currently busy
// processing another task and, if so, which task is blocking and what
// position (1-indexed, tail of the FIFO queue) the newly-saved row holds.
// GH-3732.
func (d *Dispatcher) queueBlockInfo(projectPath string) (blockedBy string, position int, busy bool) {
	d.mu.RLock()
	worker, exists := d.workers[projectPath]
	d.mu.RUnlock()
	if !exists {
		return "", 0, false
	}

	status := worker.Status()
	if !status.IsProcessing {
		return "", 0, false
	}
	return status.CurrentTaskID, status.QueuedCount, true
}

// ensureWorker creates a worker for the project if it doesn't exist and starts it.
func (d *Dispatcher) ensureWorker(projectPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.workers[projectPath]; exists {
		// Worker exists, signal it to check queue
		d.workers[projectPath].Signal()
		return
	}

	// Create new worker
	worker := NewProjectWorker(projectPath, d.store, d.runner, d.log)
	d.workers[projectPath] = worker

	// Start worker in background
	d.wg.Add(1)
	logging.SafeGo("executor-dispatcher", func() {
		defer d.wg.Done()
		// GH-4536 (TASK-419): remove this worker from d.workers once its
		// goroutine actually exits. This defer runs during panic unwind
		// before SafeGo's own recover (defers inside the wrapped fn fire
		// first), so it covers normal return (ctx cancellation, Stop()) AND
		// the SafeGo panic-recover path (dispatcher.go's own comment on that
		// mechanism) alike. Before this, hasLiveWorker was pure map presence
		// and never reflected reality: a worker that panicked mid-task left
		// its project permanently "live"-but-dead until a daemon restart, and
		// recoverStaleQueuedTasks/recoverStaleRunningTasks — which both skip
		// any project where hasLiveWorker is true — could never reap behind
		// it.
		defer d.removeWorker(projectPath, worker)
		worker.Run(d.ctx)
	})

	d.log.Info("Started project worker", slog.String("project", projectPath))

	// Signal to process any queued tasks
	worker.Signal()
}

// removeWorker deletes worker from d.workers, but only if it is still the
// CURRENT entry registered for projectPath (GH-4536/TASK-419). The identity
// check guards a narrow race: if a brand-new worker was already registered
// for the same project (e.g. ensureWorker ran again right as this one's
// Run() was returning) before this cleanup runs, a bare
// delete(d.workers, projectPath) would remove the NEW live worker's entry
// instead of this exiting one's — comparing pointer identity first ensures
// only the actual exiting worker's own map entry is ever removed.
func (d *Dispatcher) removeWorker(projectPath string, worker *ProjectWorker) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.workers[projectPath] == worker {
		delete(d.workers, projectPath)
	}
}

// GetWorkerStatus returns the status of all active workers.
func (d *Dispatcher) GetWorkerStatus() map[string]WorkerStatus {
	d.mu.RLock()
	defer d.mu.RUnlock()

	status := make(map[string]WorkerStatus)
	for path, worker := range d.workers {
		status[path] = worker.Status()
	}
	return status
}

// GetRunningTaskIDs returns the task IDs every ProjectWorker is currently
// processing. Unlike executor.Monitor.GetRunningTaskIDs (only populated in
// --dashboard mode, see cmd/pilot/main.go's SetMonitor wiring), the
// Dispatcher and its workers are always constructed, so this is the
// authoritative "is a live worker actually holding this task right now"
// signal regardless of dashboard/headless mode. GH-4412: the autopilot
// orphan-running sweep previously relied solely on the optional Monitor for
// this exclusion set, which is empty in headless (--telegram/--github only)
// deployments — silently disabling the "live worker" guard and leaving only
// the 10-minute execution_events heartbeat window to protect a genuinely
// running task, which a single long-running tool call can easily exceed.
func (d *Dispatcher) GetRunningTaskIDs() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var ids []string
	for _, worker := range d.workers {
		// Read currentTaskID/processing directly instead of worker.Status(),
		// which also queries the store for QueuedCount — unneeded here and
		// this can be called on every sweep tick.
		if !worker.processing.Load() {
			continue
		}
		if v := worker.currentTaskID.Load(); v != nil {
			if taskID, ok := v.(string); ok && taskID != "" {
				ids = append(ids, taskID)
			}
		}
	}
	return ids
}

// QueuedOrRunningCount returns the number of tasks currently queued or being
// processed for projectPath — the same live/store-backed signals
// GetWorkerStatus exposes, collapsed to one int for a single project. A
// project with no live worker at all (nothing ever dispatched, or the worker
// drained its queue and exited) returns 0, which is itself the "nothing in
// flight" signal the lane-starvation detector needs (GH-4454): a project
// lane can have open pilot-labeled issues on GitHub while never having had a
// worker created for it here, e.g. every candidate issue is stuck behind a
// scope-overlap defer or a repick-hard-cap-stalled head issue.
func (d *Dispatcher) QueuedOrRunningCount(projectPath string) int {
	d.mu.RLock()
	worker, ok := d.workers[projectPath]
	d.mu.RUnlock()
	if !ok {
		return 0
	}

	status := worker.Status()
	count := status.QueuedCount
	if status.IsProcessing {
		count++
	}
	return count
}

// GetExecutionStatus returns the current status of an execution.
func (d *Dispatcher) GetExecutionStatus(execID string) (*memory.Execution, error) {
	return d.store.GetExecution(execID)
}

// WaitForExecution waits for an execution to complete and returns the result.
// Returns error if context is cancelled or execution not found.
func (d *Dispatcher) WaitForExecution(ctx context.Context, execID string, pollInterval time.Duration) (*memory.Execution, error) {
	if pollInterval == 0 {
		pollInterval = 500 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// GH-4021: remembers the row's identity from the last successful poll so
	// a later sql.ErrNoRows (the row vanished between ticks) can be resolved
	// against HasCompletedExecution instead of surfacing as a waiter error.
	var lastTaskID, lastProjectPath string

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			exec, err := d.store.GetExecution(execID)
			if err != nil {
				// GH-4021: recoverStaleRunningTasks deletes this exact "running"
				// row out from under us once it observes a genuine completed
				// execution for the same task (orphan-row cleanup after a
				// redundant re-dispatch) — the row disappearing is that success,
				// not a failure. Resolve it as such instead of returning
				// "sql: no rows", which the caller reports as a false
				// task_failed alert for work that actually shipped.
				if errors.Is(err, sql.ErrNoRows) && lastTaskID != "" {
					// GH-4227: decomposed-parent guard runs BEFORE the
					// HasCompletedExecution check below — a decomposed epic
					// parent whose children all shipped never gets its own
					// completed row (TASK-296), so a row-vanished orphan
					// cleanup (e.g. the decomposed-parent guard branch in
					// recoverStaleRunningTasks above) would otherwise surface
					// here as a false "failed to get execution" waiter error.
					if allComplete, childIDs, evidence, gErr := decomposedChildrenAllComplete(d.store, lastTaskID, lastProjectPath, d.log); gErr != nil {
						d.log.Warn("Failed to check decomposed-parent guard while resolving vanished execution row",
							slog.String("execution_id", execID),
							slog.String("task_id", lastTaskID),
							slog.Any("error", gErr))
					} else if allComplete {
						prURL := ""
						if len(childIDs) > 0 {
							if latest, lErr := d.store.GetLatestExecutionByTaskID(childIDs[len(childIDs)-1], lastProjectPath); lErr == nil && latest != nil {
								prURL = latest.PRUrl
							}
						}
						d.log.Warn("decomposed-parent guard fired",
							slog.String("execution_id", execID),
							slog.String("task_id", lastTaskID),
							slog.Any("children", childIDs),
							slog.Any("evidence", evidence),
						)
						return &memory.Execution{
							ID:          execID,
							TaskID:      lastTaskID,
							ProjectPath: lastProjectPath,
							Status:      "completed",
							PRUrl:       prURL,
						}, nil
					}

					if completed, hcErr := d.store.HasCompletedExecution(lastTaskID, lastProjectPath); hcErr == nil && completed {
						if completedExec, gErr := d.store.GetLatestExecutionByTaskID(lastTaskID, lastProjectPath); gErr == nil {
							d.log.Info("Execution row vanished after orphan recovery — task already completed, resolving wait as success",
								slog.String("execution_id", execID),
								slog.String("task_id", lastTaskID),
							)
							return completedExec, nil
						}
					}
				}
				return nil, fmt.Errorf("failed to get execution: %w", err)
			}

			lastTaskID = exec.TaskID
			lastProjectPath = exec.ProjectPath

			// Check if terminal state. The TASK-358 classified worker outcomes
			// (no_op, declined, skipped, stalled, rate_limited, infra) are terminal
			// too: treating them as in-flight left this loop hanging until something
			// else mutated the row — in the GH-3513/GH-3530 incidents a child PR
			// merge self-healed the PARENT's row to completed with the child's PR
			// URL, and the woken handler reported a false "✅ Pilot completed!".
			if isTerminalExecutionStatus(exec.Status) {
				return exec, nil
			}
		}
	}
}

// WorkerStatus represents the current state of a project worker.
type WorkerStatus struct {
	ProjectPath   string
	IsProcessing  bool
	CurrentTaskID string
	QueuedCount   int
}

// ProjectWorker processes tasks for a single project serially.
// Only one task runs at a time per project to prevent git conflicts.
type ProjectWorker struct {
	projectPath   string
	store         *memory.Store
	lifecycle     *ExecutionLifecycle
	runner        *Runner
	log           *slog.Logger
	signal        chan struct{}
	processing    atomic.Bool
	currentTaskID atomic.Value // stores string
	stopCh        chan struct{}
	mu            sync.Mutex
}

// NewProjectWorker creates a new project worker.
func NewProjectWorker(projectPath string, store *memory.Store, runner *Runner, log *slog.Logger) *ProjectWorker {
	return &ProjectWorker{
		projectPath: projectPath,
		store:       store,
		lifecycle:   NewExecutionLifecycle(store),
		runner:      runner,
		log:         log.With(slog.String("project", projectPath)),
		signal:      make(chan struct{}, 1), // Buffered to avoid blocking
		stopCh:      make(chan struct{}),
	}
}

// Run starts the worker loop. Blocks until context is cancelled.
func (w *ProjectWorker) Run(ctx context.Context) {
	w.log.Debug("Worker started")

	for {
		select {
		case <-ctx.Done():
			w.log.Debug("Worker stopped (context cancelled)")
			return
		case <-w.stopCh:
			w.log.Debug("Worker stopped (stop signal)")
			return
		case <-w.signal:
			w.processQueue(ctx)
		}
	}
}

// Stop signals the worker to stop.
func (w *ProjectWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	select {
	case <-w.stopCh:
		// Already stopped
	default:
		close(w.stopCh)
	}
}

// Signal notifies the worker to check the queue.
func (w *ProjectWorker) Signal() {
	select {
	case w.signal <- struct{}{}:
	default:
		// Signal already pending
	}
}

// Status returns the current worker status.
func (w *ProjectWorker) Status() WorkerStatus {
	taskID := ""
	if v := w.currentTaskID.Load(); v != nil {
		taskID = v.(string)
	}

	// Get queue count
	queuedCount := 0
	if tasks, err := w.store.GetQueuedTasksForProject(w.projectPath, 100); err == nil {
		queuedCount = len(tasks)
	}

	return WorkerStatus{
		ProjectPath:   w.projectPath,
		IsProcessing:  w.processing.Load(),
		CurrentTaskID: taskID,
		QueuedCount:   queuedCount,
	}
}

// processQueue processes all queued tasks for this project.
func (w *ProjectWorker) processQueue(ctx context.Context) {
	// Only one goroutine can process at a time
	if !w.processing.CompareAndSwap(false, true) {
		return // Already processing
	}
	defer w.processing.Store(false)

	for {
		// Check if we should stop
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		default:
		}

		// Get next queued task for THIS project
		tasks, err := w.store.GetQueuedTasksForProject(w.projectPath, 1)
		if err != nil {
			w.log.Error("Failed to get queued tasks", slog.Any("error", err))
			return
		}

		if len(tasks) == 0 {
			return // Queue empty
		}

		exec := tasks[0]
		w.currentTaskID.Store(exec.TaskID)

		// GH-4184: consult the TASK-394 execution ledger at pickup time, not
		// just at poll time. The 17:48->18:12 incident: the poller's re-arm
		// guard (ExecutionChecker.HasCompletedExecution) saw no completion and
		// queued a retry; the genuine completion landed in the ledger before
		// the dispatcher got to this row, with no GitHub-side signal (labels,
		// merged PR) yet visible to catch it. Re-checking the same ledger here
		// closes that window regardless of what the poller observed earlier.
		if done, err := w.hasTerminalSuccessLedger(exec.TaskID); err != nil {
			w.log.Warn("Failed to check terminal-success ledger before pickup",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("error", err))
		} else if done {
			prURL := ""
			if latest, gErr := w.store.GetLatestExecutionByTaskIDExcluding(exec.TaskID, w.projectPath, exec.ID); gErr == nil && latest != nil {
				prURL = latest.PRUrl
			}
			w.log.Info("Terminal-success ledger already has a completed row for task; refusing duplicate dispatch",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.String("pr_url", prURL),
			)
			if err := w.store.MarkExecutionCompleted(exec.ID, prURL, "", 0); err != nil {
				w.log.Error("Failed to mark ledger-guarded duplicate completed", slog.Any("error", err))
			}
			w.recordExecutionEvent(exec.ID, memory.StageCompleted, "terminal-success ledger guard: task already completed")
			w.runner.EmitProgress(exec.TaskID, "Completed", 100, "already completed per execution ledger")
			w.currentTaskID.Store("")
			continue
		} else if allShipped, childIDs, evidence, cErr := decomposedChildrenAllComplete(w.store, exec.TaskID, w.projectPath, w.log); cErr != nil {
			w.log.Warn("Failed to check cross-task-id dispatch guard before pickup",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("error", cErr))
		} else if allShipped {
			// GH-4216 (Defect A, fix 3) / GH-4227: the ledger shows this task_id
			// decomposed into children that ALL already shipped completed
			// executions — the GH-4211 repro re-dispatched the parent as a fresh
			// top-level task and re-implemented the same fix its child (#4212)
			// had already landed in PR #4213. hasTerminalSuccessLedger above
			// never catches this because an epic parent's own row typically
			// carries no deliverable (TASK-296); this check follows the
			// decomposed-children trail instead. Fail-loud: this is a
			// defense-in-depth skip, not a normal completion, so it always logs
			// at Warn with the full child list and per-child evidence.
			prURL := ""
			if latest, gErr := w.store.GetLatestExecutionByTaskID(childIDs[len(childIDs)-1], w.projectPath); gErr == nil && latest != nil {
				prURL = latest.PRUrl
			}
			w.log.Warn("decomposed-parent guard fired",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
				slog.Any("children", childIDs),
				slog.Any("evidence", evidence),
				slog.String("evidence_pr_url", prURL),
			)
			if err := w.store.MarkExecutionCompleted(exec.ID, prURL, "", 0); err != nil {
				w.log.Error("Failed to mark cross-task-id-guarded duplicate completed", slog.Any("error", err))
			}
			w.recordExecutionEvent(exec.ID, memory.StageCompleted, fmt.Sprintf("cross-task-id dispatch guard: children already completed (%s)", strings.Join(childIDs, ", ")))
			w.runner.EmitProgress(exec.TaskID, "Completed", 100, "already completed via decomposed children (cross-task-id guard)")
			w.currentTaskID.Store("")
			continue
		}

		w.log.Info("Processing task",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.String("title", exec.TaskTitle),
		)

		// Update status to running
		if err := w.lifecycle.Transition(exec.ID, ExecStatusRunning); err != nil {
			w.log.Error("Failed to update status to running", slog.Any("error", err))
			continue
		}

		// GH-3846: record queued->running transition to the execution-events audit trail.
		w.recordExecutionEvent(exec.ID, memory.StageRunning, fmt.Sprintf("worker started task %s", exec.TaskID))

		// Emit progress callback for task started
		w.runner.EmitProgress(exec.TaskID, "Running", 2, fmt.Sprintf("Worker started: %s", truncateForLog(exec.TaskTitle, 40)))

		// Build task from execution record (full details stored when queued)
		// GH-2326: restore Labels so runner-side no-decompose / autopilot-fix
		// gates see the same labels the dispatch-time Decompose() saw.
		task := buildTaskFromExecution(exec)

		// GH-4141 Phase 3: pre-execute merged-PR short-circuit. A queued task
		// whose branch already has a merged PR (e.g. a poller-retry duplicate of
		// a sub-issue the epic already shipped) must not re-invoke the backend
		// just to rediscover "no new commit" as a no_op — mark it completed with
		// the existing PR URL and skip execution entirely.
		if task.Branch != "" {
			if mergedURL, mergedErr := mergedPRPreflightCheck(ctx, exec.ProjectPath, task.Branch); mergedErr == nil && mergedURL != "" {
				w.log.Info("Queued task's branch already merged; skipping backend invocation",
					slog.String("execution_id", exec.ID),
					slog.String("task_id", exec.TaskID),
					slog.String("pr_url", mergedURL),
				)
				if err := w.store.MarkExecutionCompleted(exec.ID, mergedURL, "", 0); err != nil {
					w.log.Error("Failed to mark pre-execute merged-PR short-circuit completed", slog.Any("error", err))
				}
				w.recordExecutionEvent(exec.ID, memory.StageCompleted, "pre-execute merged-PR short-circuit: "+mergedURL)
				w.runner.EmitProgress(exec.TaskID, "Completed", 100, "work already merged: "+mergedURL)
				w.currentTaskID.Store("")
				continue
			}
		}

		// Execute (blocking)
		start := time.Now()
		// GH-4536 (TASK-419): mark ctx as executing on THIS project's
		// ProjectWorker — the only place Runner.Execute is invoked
		// synchronously by the dispatcher — so a queued epic sub-issue owned
		// by this same project (reconcileChildOutcome, epic.go) can be
		// recognized as self-owned rather than waited on forever.
		result, execErr := w.runner.Execute(withProjectWorkerIdentity(ctx, w.projectPath), task)
		duration := time.Since(start)

		// GH-4243: single chokepoint classifies the outcome (TerminalStatus).
		// The branches below drive event-recording/progress side effects off
		// the classification, then GH-4259 requires persisting the terminal
		// status (below, after the switch) only once those events are
		// written — otherwise a poller can observe the terminal status via
		// GetExecution and read the execution_events ledger before the
		// matching event row exists, intermittently losing the race (this is
		// exactly what made the synthetic dispatch event-sequence tests flake
		// once RecordExecutionEvent's GH-4244 validate-first GetExecution
		// round trip made the event write slow enough to lose more often).
		outcome := w.lifecycle.Classify(result, execErr)

		switch {
		case execErr != nil:
			w.log.Error("Task execution failed",
				slog.String("task_id", exec.TaskID),
				slog.Any("error", execErr),
				slog.Duration("duration", duration),
			)
			// GH-3846: record terminal-failure transition to the execution-events audit trail.
			w.recordExecutionEvent(exec.ID, memory.StageFailed, truncateForLog(execErr.Error(), 200))
			// Emit progress callback for task failed
			w.runner.EmitProgress(exec.TaskID, "Failed", 100, fmt.Sprintf("Execution error: %s", truncateForLog(execErr.Error(), 60)))
		case outcome.Status != ExecStatusCompleted:
			// TASK-358: classify the terminal outcome instead of collapsing every
			// non-success into "failed". declined / no-op / stalled get their own
			// status so the dashboard's "failed" count reflects genuine failures.
			w.log.Warn("Task ended without success",
				slog.String("task_id", exec.TaskID),
				slog.String("status", string(outcome.Status)),
				slog.String("error", outcome.Error),
				slog.Duration("duration", duration),
			)
			// GH-3846: record no_op/skipped transitions to the execution-events audit
			// trail. Stalled is instrumented at its detection site in runner.go instead;
			// declined/rate_limited/infra have no execution_events Stage equivalent yet.
			if stage, ok := dispatchTerminalStage(string(outcome.Status)); ok {
				w.recordExecutionEvent(exec.ID, stage, fmt.Sprintf("%s: %s", outcome.Status, truncateForLog(outcome.Error, 200)))
			}
			// Emit progress callback with a phase that matches the classified outcome.
			w.runner.EmitProgress(exec.TaskID, terminalPhaseLabel(string(outcome.Status)), 100,
				fmt.Sprintf("%s: %s", outcome.Status, truncateForLog(outcome.Error, 60)))
			// GH-4490 subtask 2: EmitProgress alone only drives phase/progress —
			// it never moves the in-memory card's Status off "running" (see
			// Monitor.UpdateProgress), so a no-commit run (outcome.Status ==
			// no_op) previously left the dashboard card stuck at running/100%
			// until the periodic reconciler's next tick (subtask 1's backstop).
			// Drive the terminal transition here instead, on the same
			// event-driven path that already knows the classified outcome, so
			// the card matches the executions-table status immediately: no_op
			// is a non-failure terminal outcome and must not be reported as
			// Failed. Other non-completed subtypes (stalled, rate_limited,
			// infra, skipped, declined) are left to their own detection sites
			// (e.g. runner.go's Stall) or the periodic reconciler.
			if w.runner.monitor != nil {
				switch outcome.Status {
				case ExecStatusNoOp:
					w.runner.monitor.NoOp(exec.TaskID, outcome.Error)
				case ExecStatusFailed:
					w.runner.monitor.Fail(exec.TaskID, outcome.Error)
				}
			}
		default:
			w.log.Info("Task completed successfully",
				slog.String("task_id", exec.TaskID),
				slog.Duration("duration", duration),
				slog.String("pr_url", result.PRUrl),
			)
			// GH-3846: record terminal-success transition to the execution-events audit trail.
			if stage, ok := dispatchSuccessStage(result.PRUrl); ok {
				w.recordExecutionEvent(exec.ID, stage, fmt.Sprintf("completed with PR: %s", result.PRUrl))
			}
			// Emit progress callback for task completed
			msg := fmt.Sprintf("Completed in %s", duration.Round(time.Second))
			if result.PRUrl != "" {
				msg = fmt.Sprintf("Completed with PR: %s", result.PRUrl)
			}
			w.runner.EmitProgress(exec.TaskID, "Completed", 100, msg)
		}

		// GH-4259: persist the terminal status/metrics after the events above
		// are written, not before — see the Classify comment.
		if finErr := w.lifecycle.Persist(exec.ID, outcome, result, duration); finErr != nil {
			w.log.Error("Failed to persist execution outcome", slog.String("execution_id", exec.ID), slog.Any("error", finErr))
		}

		// Effort tier and usage-event telemetry stay dispatcher-owned: they're
		// observability writes, not part of the row-lifecycle contract Finish
		// covers (status + metrics). Needed for GetLifetimeTokens() (GH-533).
		if result != nil {
			// GH-2807: persist effort/complexity tier for cost-by-tier observability.
			if result.EffortLevel != "" || result.ComplexityLevel != "" {
				if err := w.store.UpdateExecutionEffort(exec.ID, result.EffortLevel, result.ComplexityLevel); err != nil {
					w.log.Error("Failed to update execution effort", slog.Any("error", err))
				}
			}

			// GH-2429: emit per-execution usage events (task + token + compute) so the
			// `usage_events` table reflects real activity. UserID is single-tenant for
			// now (empty); when multi-user lands, plumb the real ID through Execution.
			if err := w.store.RecordTaskUsage(
				exec.ID,
				exec.UserID,
				exec.ProjectPath,
				duration.Milliseconds(),
				result.TokensInput,
				result.TokensOutput,
			); err != nil {
				w.log.Error("Failed to record usage event", slog.Any("error", err))
			}
		}

		w.currentTaskID.Store("")
	}
}

// recordExecutionEvent writes a best-effort stage-transition record to the
// execution_events audit trail (GH-3846). Mirrors the worker's other store
// writes here: a nil store, missing parent execution row (GH-4244
// validate-first via memory.Store.RecordExecutionEvent), or insert failure is
// logged and swallowed, never fails the worker loop — the audit trail is a
// diagnostic aid, not load-bearing.
func (w *ProjectWorker) recordExecutionEvent(executionID string, stage memory.Stage, detail string) {
	if w.store == nil {
		return
	}
	if err := w.store.RecordExecutionEvent(executionID, stage, detail); err != nil {
		w.log.Warn("Failed to record execution event",
			slog.String("execution_id", executionID),
			slog.String("stage", string(stage)),
			slog.Any("error", err))
	}
}

// hasTerminalSuccessLedger reports whether the TASK-394 execution ledger
// already holds terminal-completion evidence for taskID in this worker's
// project. It is the single guard shared by both re-arm points: the poller's
// re-arm path consults the identical ledger via ExecutionChecker (M7 4d.6:
// the SDK poller's ExecutionChecker.HasCompletedExecution, wired in
// cmd/pilot/poller_github.go) at poll time, and processQueue calls this
// method again immediately before pickup (GH-4184) — closing the window
// where a completion lands between the poller's decision and the dispatcher
// actually starting the backend.
//
// GH-4347: delegates to HasTerminalCompletion rather than the stricter
// Store.HasCompletedExecution directly — a no_op outcome ("nothing to
// change") never satisfies HasCompletedExecution (no commit/PR), so a task
// whose correct terminal state is no_op was invisible to both re-arm points
// forever, and got re-dispatched on every poll tick.
func (w *ProjectWorker) hasTerminalSuccessLedger(taskID string) (bool, error) {
	return HasTerminalCompletion(w.store, taskID, w.projectPath)
}

// HasTerminalCompletion reports whether taskID has ledger evidence in
// projectPath that no further dispatch is warranted: either a genuine
// Store.HasCompletedExecution row (completed with a commit/PR deliverable),
// or ANY row that terminated no_op with no error (Store.HasTerminalCompletion's
// "nothing to change is itself a legitimate completion" definition, matching
// childCompletionEvidence's no_op reason).
//
// GH-4347: exported so every re-arm/admission gate shares one definition of
// "done" instead of drifting. Before this, Store.HasCompletedExecution's
// stricter deliverable-only definition was consulted directly in two places
// that both needed the broader one — the SDK poller's pre-dispatch
// ExecutionChecker check (poll time) and this package's processQueue pickup
// guard (hasTerminalSuccessLedger) — so a no_op task_id (a legitimate,
// common epic sub-issue outcome: "already covered by a sibling", "nothing
// to change") was re-dispatched on every poll tick indefinitely. Confirmed
// via ledger (GH-82 on pilot-canary-sandbox: six no_op rows, ~minutes
// apart, matching the poll interval plus per-run subprocess time — not a
// tight race).
//
// Delegates to Store.HasTerminalCompletion rather than childCompletionEvidence:
// the latter's no_op fallback only inspects GetLatestExecutionByTaskID's most
// recent row, which is correct for its own call site (a decomposed child's
// one prior attempt) but wrong here, where the caller is re-checking
// admission for a task_id that may already have a fresh "queued" duplicate
// row racing alongside the earlier no_op row — the fresh row would sort as
// "latest" and hide the terminal no_op. Store.HasTerminalCompletion scans
// every row for the task_id instead.
//
// Store.HasCompletedExecution itself is intentionally left untouched: TASK-359
// established its strict "has a deliverable" contract is load-bearing
// elsewhere (TestTaskCompletionInvariant); this wraps it rather than
// broadening it.
func HasTerminalCompletion(store *memory.Store, taskID, projectPath string) (bool, error) {
	return store.HasTerminalCompletion(taskID, projectPath)
}

// decomposedChildrenAllComplete reports whether taskID has a recorded
// StageDecomposed ledger event AND every child task_id parsed from it has
// ledger evidence of completion (see childCompletionEvidence). It returns the
// child task IDs found (possibly empty) and a matching per-child evidence tag
// ("<childID>:<reason>"), regardless of outcome, for logging.
//
// GH-4216 (Defect A, fix 3) / GH-4227: the decomposed-parent / cross-task-id
// dispatch guard, defense-in-depth alongside hasTerminalSuccessLedger.
// hasTerminalSuccessLedger only ever checks taskID's own rows, which never
// catches an epic parent whose finalize keeps failing (or whose task_id got
// re-queued, orphan-reaped, or polled for status for any other reason) once
// every child it decomposed into already shipped — an epic parent's own row
// typically carries no deliverable (TASK-296), so
// HasCompletedExecution(taskID, ...) stays false forever even though the real
// work is done. Shared by every dispatcher.go call site that consults
// HasCompletedExecution(taskID) for a task_id that might itself be a
// decomposed epic parent: processQueue's pickup guard, stale-running/queued
// recovery, and WaitForExecution's row-vanished resolution.
//
// Returns false with no children if taskID never decomposed, or if any child
// is still incomplete (existing epic-resume behavior is left unchanged in
// that case). A StageDecomposed event whose detail string didn't parse into
// any child refs (malformed/legacy format) is logged at Warn and treated the
// same as "never decomposed" — fail safe, falling through rather than
// guessing.
func decomposedChildrenAllComplete(store *memory.Store, taskID, projectPath string, log *slog.Logger) (allComplete bool, childIDs []string, evidence []string, err error) {
	childIDs, found, err := store.GetDecomposedChildTaskIDs(taskID, projectPath)
	if err != nil {
		return false, nil, nil, err
	}
	if !found {
		return false, nil, nil, nil
	}
	if len(childIDs) == 0 {
		log.Warn("decomposed-parent guard: StageDecomposed event found but no child refs parsed from detail; treating as not decomposed",
			slog.String("task_id", taskID))
		return false, nil, nil, nil
	}

	evidence = make([]string, 0, len(childIDs))
	for _, childID := range childIDs {
		reason, complete, cErr := childCompletionEvidence(store, childID, projectPath)
		if cErr != nil {
			return false, childIDs, evidence, cErr
		}
		if !complete {
			return false, childIDs, evidence, nil
		}
		evidence = append(evidence, childID+":"+reason)
	}
	return true, childIDs, evidence, nil
}

// childCompletionEvidence reports whether childID has ledger evidence of
// completion in projectPath, and a short tag describing which signal
// matched: "completed" (Store.HasCompletedExecution — a genuine completed
// row with a deliverable), "no_op" (latest row terminated no_op with no
// error — nothing to change is itself a legitimate completion), or
// "merged_pr" (latest row carries a non-empty pr_url even though its own
// status/error fields wouldn't satisfy HasCompletedExecution, e.g. a row
// healed after the fact). Ledger-only: reads local store data alone, no live
// GitHub calls — matching the "ledger-only guard" framing of every other
// dispatch guard in this file (contrast staleRunningMergedPRCheck /
// mergedPRPreflightCheck, which do shell out).
func childCompletionEvidence(store *memory.Store, childID, projectPath string) (reason string, complete bool, err error) {
	completed, err := store.HasCompletedExecution(childID, projectPath)
	if err != nil {
		return "", false, err
	}
	if completed {
		return "completed", true, nil
	}

	latest, err := store.GetLatestExecutionByTaskID(childID, projectPath)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if latest == nil || latest.ProjectPath != projectPath {
		return "", false, nil
	}
	if latest.Status == "no_op" && latest.Error == "" {
		return "no_op", true, nil
	}
	if latest.PRUrl != "" {
		return "merged_pr", true, nil
	}
	return "", false, nil
}

// dispatchSuccessStage reports the execution_events Stage (and whether to
// write one) for the dispatcher's terminal-success site. The Stage enum
// (GH-3840) has no generic "completed" value, so a PR is the only durable
// milestone this site can map to today — runner.go already emits pr_created
// at creation time; this dispatcher-level entry marks the run as a whole
// finishing. Direct-commit tasks with no PR have no matching Stage yet, so
// they're intentionally left uninstrumented here (GH-3846).
func dispatchSuccessStage(prURL string) (memory.Stage, bool) {
	if prURL == "" {
		return "", false
	}
	return memory.StagePRCreated, true
}

// dispatchTerminalStage maps a dispatcher-classified terminal status (see
// TerminalStatus) to its execution_events Stage, for the subset that mark a
// durable milestone. Stalled is instrumented at its detection site in
// runner.go instead (GH-3846). infra reuses StageFailed (GH-4101) — it has no
// dedicated Stage enum value, but StageFailed already carries no ladder rung
// of its own (stageLadderPosition returns 0), and the dashboard's
// mutedOutcomes set already overrides the rendered label/color for "infra"
// regardless of the underlying stage (internal/dashboard/stage_strip.go), so
// reusing it produces no behavior change there. declined/rate_limited still
// have no Stage enum equivalent, so they're skipped rather than mismapped.
func dispatchTerminalStage(status string) (memory.Stage, bool) {
	switch status {
	case "no_op":
		return memory.StageNoOp, true
	case "skipped":
		return memory.StageSkipped, true
	case "infra":
		return memory.StageFailed, true
	default:
		return "", false
	}
}

// buildTaskFromExecution reconstructs a Task from its persisted memory.Execution
// row before handing it to the runner. GH-3764: ExecutionID carries the exec's
// UUID (exec.ID) through Execute() so log/diagnostic/learning writes can join
// against executions.id — task.ID (the human-readable "GH-123" label) is kept
// as a separate field rather than replaced, since WS live-tail filters key on it.
func buildTaskFromExecution(exec *memory.Execution) *Task {
	return &Task{
		ID:            exec.TaskID,
		ExecutionID:   exec.ID,
		Title:         exec.TaskTitle,
		Description:   exec.TaskDescription,
		ProjectPath:   exec.ProjectPath,
		Branch:        exec.TaskBranch,
		BaseBranch:    exec.TaskBaseBranch,
		CreatePR:      exec.TaskCreatePR,
		Verbose:       exec.TaskVerbose,
		SourceAdapter: exec.TaskSourceAdapter,
		SourceIssueID: exec.TaskSourceIssueID,
		Labels:        exec.TaskLabels,
		IsCanary:      exec.IsCanary,
	}
}

// truncateForLog truncates a string for log messages, removing newlines and adding ellipsis
func truncateForLog(s string, maxLen int) string {
	// Replace newlines with spaces
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// terminalPhaseLabel maps a classified execution status to the human-readable
// progress phase shown in the dashboard. TASK-358.
func terminalPhaseLabel(status string) string {
	switch status {
	case "no_op":
		return "No-op"
	case "stalled":
		return "Stalled"
	case "declined":
		return "Declined"
	default:
		return "Failed"
	}
}
