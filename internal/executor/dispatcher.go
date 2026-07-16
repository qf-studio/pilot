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

	return &Dispatcher{
		config:  config,
		store:   store,
		runner:  runner,
		workers: make(map[string]*ProjectWorker),
		log:     logging.WithComponent("dispatcher"),
		ctx:     ctx,
		cancel:  cancel,
	}
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
		// branch's PR state directly before failing it.
		branch := fmt.Sprintf("pilot/%s", exec.TaskID)
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

		d.log.Warn("Marking stale running task as failed",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.Time("created_at", exec.CreatedAt),
		)
		if err := d.store.UpdateExecutionStatus(exec.ID, "failed", "stale running task recovered (orphaned worker)"); err != nil {
			d.log.Error("Failed to mark stale running task", slog.String("id", exec.ID), slog.Any("error", err))
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
		if err := d.store.UpdateExecutionStatus(exec.ID, "failed", "queued task orphaned by restart; project no longer configured"); err != nil {
			d.log.Error("Failed to mark stale queued task", slog.String("id", exec.ID), slog.Any("error", err))
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
			if err == nil {
				d.recordExecutionEvent(execID, memory.StageDecompositionSkipped, detail)
			}
			return execID, err
		}
	}

	// Queue single task
	return d.queueSingleTask(ctx, task)
}

// queueDecomposedTask handles queuing a decomposed task and its subtasks.
// The parent task is marked as "decomposed" and subtasks are queued in order.
func (d *Dispatcher) queueDecomposedTask(ctx context.Context, parent *Task, result *DecomposeResult) (string, error) {
	// GH-4243: single chokepoint for the row create + ExecutionID threading
	// that used to be hand-rolled here.
	parentExecID, err := NewExecutionLifecycle(d.store).Begin(parent, ExecStatusDecomposed)
	if err != nil {
		return "", fmt.Errorf("failed to save decomposed parent: %w", err)
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
	// that used to be hand-rolled here.
	execID, err := NewExecutionLifecycle(d.store).Begin(task, ExecStatusQueued)
	if err != nil {
		return "", fmt.Errorf("failed to save execution: %w", err)
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
		worker.Run(d.ctx)
	})

	d.log.Info("Started project worker", slog.String("project", projectPath))

	// Signal to process any queued tasks
	worker.Signal()
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
			switch exec.Status {
			case "completed", "failed", "cancelled",
				"declined", "no_op", "rate_limited", "skipped", "stalled", "infra":
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
		result, execErr := w.runner.Execute(ctx, task)
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
