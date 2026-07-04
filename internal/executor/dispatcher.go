package executor

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

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
			if err := d.store.DeleteExecution(exec.ID); err != nil {
				d.log.Error("Failed to delete orphan running row", slog.String("id", exec.ID), slog.Any("error", err))
			}
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
		d.log.Warn("Marking stale running task as failed",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.Time("created_at", exec.CreatedAt),
		)
		if err := d.store.UpdateExecutionStatus(exec.ID, "failed", "stale running task recovered (orphaned worker)"); err != nil {
			d.log.Error("Failed to mark stale running task", slog.String("id", exec.ID), slog.Any("error", err))
		} else {
			resetCount++
		}
	}

	return resetCount
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
		}
	}

	return resetCount
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

// QueueTask adds a task to the execution queue and returns the execution ID.
// The task will be executed by the project's worker in FIFO order.
// If a decomposer is configured and the task is complex, it will be split
// into subtasks that are queued instead of the parent task.
func (d *Dispatcher) QueueTask(ctx context.Context, task *Task) (string, error) {
	// Check for duplicate tasks
	exists, err := d.store.IsTaskQueued(task.ID)
	if err != nil {
		d.log.Warn("Failed to check for duplicate task", slog.Any("error", err))
	} else if exists {
		return "", fmt.Errorf("task %s is already queued or running", task.ID)
	}

	// Try decomposition if decomposer is configured
	if d.decomposer != nil {
		result := d.decomposer.Decompose(task)
		if result.Decomposed && len(result.Subtasks) > 1 {
			return d.queueDecomposedTask(ctx, task, result)
		}
	}

	// Queue single task
	return d.queueSingleTask(ctx, task)
}

// queueDecomposedTask handles queuing a decomposed task and its subtasks.
// The parent task is marked as "decomposed" and subtasks are queued in order.
func (d *Dispatcher) queueDecomposedTask(ctx context.Context, parent *Task, result *DecomposeResult) (string, error) {
	// Generate parent execution ID
	parentExecID := uuid.New().String()

	// Save parent as "decomposed" status
	parentExec := &memory.Execution{
		ID:                parentExecID,
		TaskID:            parent.ID,
		ProjectPath:       parent.ProjectPath,
		Status:            "decomposed",
		TaskTitle:         parent.Title,
		TaskDescription:   parent.Description,
		TaskBranch:        parent.Branch,
		TaskBaseBranch:    parent.BaseBranch,
		TaskCreatePR:      parent.CreatePR,
		TaskVerbose:       parent.Verbose,
		TaskSourceAdapter: parent.SourceAdapter,
		TaskSourceIssueID: parent.SourceIssueID,
		TaskLabels:        parent.Labels, // GH-2326: persist labels for no-decompose/autopilot-fix gates
	}

	if err := d.store.SaveExecution(parentExec); err != nil {
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
	// Generate execution ID
	execID := uuid.New().String()

	// Save to SQLite with status='queued' and full task details
	exec := &memory.Execution{
		ID:                execID,
		TaskID:            task.ID,
		ProjectPath:       task.ProjectPath,
		Status:            "queued",
		TaskTitle:         task.Title,
		TaskDescription:   task.Description,
		TaskBranch:        task.Branch,
		TaskBaseBranch:    task.BaseBranch,
		TaskCreatePR:      task.CreatePR,
		TaskVerbose:       task.Verbose,
		TaskSourceAdapter: task.SourceAdapter,
		TaskSourceIssueID: task.SourceIssueID,
		TaskLabels:        task.Labels, // GH-2326: persist labels for no-decompose/autopilot-fix gates
	}

	if err := d.store.SaveExecution(exec); err != nil {
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

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			exec, err := d.store.GetExecution(execID)
			if err != nil {
				return nil, fmt.Errorf("failed to get execution: %w", err)
			}

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

		w.log.Info("Processing task",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.String("title", exec.TaskTitle),
		)

		// Update status to running
		if err := w.store.UpdateExecutionStatus(exec.ID, "running"); err != nil {
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

		// Execute (blocking)
		start := time.Now()
		result, execErr := w.runner.Execute(ctx, task)
		duration := time.Since(start)

		// Update execution record with result
		if execErr != nil {
			w.log.Error("Task execution failed",
				slog.String("task_id", exec.TaskID),
				slog.Any("error", execErr),
				slog.Duration("duration", duration),
			)
			if err := w.store.UpdateExecutionStatus(exec.ID, "failed", execErr.Error()); err != nil {
				w.log.Error("Failed to update status to failed", slog.Any("error", err))
			}
			// GH-3846: record terminal-failure transition to the execution-events audit trail.
			w.recordExecutionEvent(exec.ID, memory.StageFailed, truncateForLog(execErr.Error(), 200))
			// Emit progress callback for task failed
			w.runner.EmitProgress(exec.TaskID, "Failed", 100, fmt.Sprintf("Execution error: %s", truncateForLog(execErr.Error(), 60)))
		} else if !result.Success {
			// TASK-358: classify the terminal outcome instead of collapsing every
			// non-success into "failed". declined / no-op / stalled get their own
			// status so the dashboard's "failed" count reflects genuine failures.
			status := TerminalStatus(result)
			w.log.Warn("Task ended without success",
				slog.String("task_id", exec.TaskID),
				slog.String("status", status),
				slog.String("error", result.Error),
				slog.Duration("duration", duration),
			)
			if err := w.store.UpdateExecutionStatus(exec.ID, status, result.Error); err != nil {
				w.log.Error("Failed to update execution status",
					slog.String("status", status), slog.Any("error", err))
			}
			// GH-3846: record no_op/skipped transitions to the execution-events audit
			// trail. Stalled is instrumented at its detection site in runner.go instead;
			// declined/rate_limited/infra have no execution_events Stage equivalent yet.
			if stage, ok := dispatchTerminalStage(status); ok {
				w.recordExecutionEvent(exec.ID, stage, fmt.Sprintf("%s: %s", status, truncateForLog(result.Error, 200)))
			}
			// Emit progress callback with a phase that matches the classified outcome.
			w.runner.EmitProgress(exec.TaskID, terminalPhaseLabel(status), 100,
				fmt.Sprintf("%s: %s", status, truncateForLog(result.Error, 60)))
		} else {
			w.log.Info("Task completed successfully",
				slog.String("task_id", exec.TaskID),
				slog.Duration("duration", duration),
				slog.String("pr_url", result.PRUrl),
			)
			// TASK-359 Layer 1: one atomic write (status + result fields) instead of
			// UpdateExecutionStatus("completed") then UpdateExecutionResult — the gap
			// between those two could leave a 'completed' row with an empty pr_url.
			if err := w.store.MarkExecutionCompleted(exec.ID, result.PRUrl, result.CommitSHA, duration.Milliseconds()); err != nil {
				w.log.Error("Failed to mark execution completed", slog.Any("error", err))
			}
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

		// Persist execution metrics (tokens, cost, code changes) so they survive restarts.
		// This is needed for GetLifetimeTokens() to return real data (GH-533).
		if result != nil {
			// GH-2807: persist effort/complexity tier for cost-by-tier observability.
			if result.EffortLevel != "" || result.ComplexityLevel != "" {
				if err := w.store.UpdateExecutionEffort(exec.ID, result.EffortLevel, result.ComplexityLevel); err != nil {
					w.log.Error("Failed to update execution effort", slog.Any("error", err))
				}
			}
			if err := w.store.SaveExecutionMetrics(&memory.ExecutionMetrics{
				ExecutionID:      exec.ID,
				TokensInput:      result.TokensInput,
				TokensOutput:     result.TokensOutput,
				TokensTotal:      result.TokensTotal,
				TokensCacheRead:  result.CacheReadInputTokens,
				TokensCacheWrite: result.CacheCreationInputTokens,
				EstimatedCostUSD: result.EstimatedCostUSD,
				FilesChanged:     result.FilesChanged,
				LinesAdded:       result.LinesAdded,
				LinesRemoved:     result.LinesRemoved,
				ModelName:        result.ModelName,
				PeakRSSMB:        result.PeakRSSMB,
				FinalRSSMB:       result.FinalRSSMB,
			}); err != nil {
				w.log.Error("Failed to save execution metrics", slog.Any("error", err))
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
// writes here: a nil store or insert failure is logged and swallowed, never
// fails the worker loop — the audit trail is a diagnostic aid, not load-bearing.
func (w *ProjectWorker) recordExecutionEvent(executionID string, stage memory.Stage, detail string) {
	if w.store == nil {
		return
	}
	if err := w.store.InsertExecutionEvent(executionID, stage, detail); err != nil {
		w.log.Warn("Failed to record execution event",
			slog.String("execution_id", executionID),
			slog.String("stage", string(stage)),
			slog.Any("error", err))
	}
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
// runner.go instead (GH-3846); declined/rate_limited/infra have no Stage
// enum equivalent yet, so they're skipped rather than mismapped.
func dispatchTerminalStage(status string) (memory.Stage, bool) {
	switch status {
	case "no_op":
		return memory.StageNoOp, true
	case "skipped":
		return memory.StageSkipped, true
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
