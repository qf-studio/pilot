package executor

import (
	"context"
	"fmt"
	"log/slog"
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

	// Initial recovery pass on startup.
	d.recoverStaleTasks()

	// Launch periodic recovery loop.
	d.wg.Add(1)
	go d.runStaleRecoveryLoop(ctx)

	return nil
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
func (d *Dispatcher) recoverStaleTasks() int {
	var resetCount int

	// Recover stale running tasks (crashed workers).
	staleRunning, err := d.store.GetStaleRunningExecutions(d.config.StaleRunningThreshold)
	if err != nil {
		d.log.Warn("Failed to fetch stale running executions", slog.Any("error", err))
	}
	for _, exec := range staleRunning {
		// If this task already completed successfully, delete the orphan row
		// instead of marking it failed (avoids dashboard showing false failures).
		if completed, _ := d.store.HasCompletedExecution(exec.TaskID, exec.ProjectPath); completed {
			d.log.Info("Deleting orphan running row (task already completed)",
				slog.String("execution_id", exec.ID),
				slog.String("task_id", exec.TaskID),
			)
			if err := d.store.DeleteExecution(exec.ID); err != nil {
				d.log.Error("Failed to delete orphan running row", slog.String("id", exec.ID), slog.Any("error", err))
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
		}
	}

	// Recover stale queued tasks (stuck in queue with no worker).
	// GH-2331: Don't mark queued tasks stale when a live worker exists for the
	// project — they're just waiting their turn. Pilot runs tasks serially per
	// project; when one task takes 8+ minutes (common for epic/Navigator work),
	// its siblings exceed the 5-minute threshold purely by waiting, and get
	// killed mid-queue. Only orphans (no worker alive) should be reaped.
	staleQueued, err := d.store.GetStaleQueuedExecutions(d.config.StaleQueuedThreshold)
	if err != nil {
		d.log.Warn("Failed to fetch stale queued executions", slog.Any("error", err))
	}
	for _, exec := range staleQueued {
		if completed, _ := d.store.HasCompletedExecution(exec.TaskID, exec.ProjectPath); completed {
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

		d.log.Warn("Marking stale queued task as failed",
			slog.String("execution_id", exec.ID),
			slog.String("task_id", exec.TaskID),
			slog.Time("created_at", exec.CreatedAt),
		)
		if err := d.store.UpdateExecutionStatus(exec.ID, "failed", "stale queued task recovered (no worker picked up)"); err != nil {
			d.log.Error("Failed to mark stale queued task", slog.String("id", exec.ID), slog.Any("error", err))
		} else {
			resetCount++
		}
	}

	d.log.Info("stale recovery complete, reset N tasks", slog.Int("count", resetCount))
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

	d.log.Info("Task queued",
		slog.String("execution_id", execID),
		slog.String("task_id", task.ID),
		slog.String("project", task.ProjectPath),
	)

	// Emit progress callback for task queued
	d.runner.EmitProgress(task.ID, "Queued", 0, fmt.Sprintf("Task queued (exec: %s)", execID[:8]))

	// Ensure worker exists and signal it
	d.ensureWorker(task.ProjectPath)

	return execID, nil
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
	go func() {
		defer d.wg.Done()
		worker.Run(d.ctx)
	}()

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

			// Check if terminal state
			switch exec.Status {
			case "completed", "failed", "cancelled":
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

		// Emit progress callback for task started
		w.runner.EmitProgress(exec.TaskID, "Running", 2, fmt.Sprintf("Worker started: %s", truncateForLog(exec.TaskTitle, 40)))

		// Build task from execution record (full details stored when queued)
		// GH-2326: restore Labels so runner-side no-decompose / autopilot-fix
		// gates see the same labels the dispatch-time Decompose() saw.
		task := &Task{
			ID:            exec.TaskID,
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
			// Emit progress callback for task failed
			w.runner.EmitProgress(exec.TaskID, "Failed", 100, fmt.Sprintf("Execution error: %s", truncateForLog(execErr.Error(), 60)))
		} else if !result.Success {
			w.log.Warn("Task completed with failure",
				slog.String("task_id", exec.TaskID),
				slog.String("error", result.Error),
				slog.Duration("duration", duration),
			)
			if err := w.store.UpdateExecutionStatus(exec.ID, "failed", result.Error); err != nil {
				w.log.Error("Failed to update status to failed", slog.Any("error", err))
			}
			// Emit progress callback for task failed
			w.runner.EmitProgress(exec.TaskID, "Failed", 100, fmt.Sprintf("Task failed: %s", truncateForLog(result.Error, 60)))
		} else {
			w.log.Info("Task completed successfully",
				slog.String("task_id", exec.TaskID),
				slog.Duration("duration", duration),
				slog.String("pr_url", result.PRUrl),
			)
			if err := w.store.UpdateExecutionStatus(exec.ID, "completed"); err != nil {
				w.log.Error("Failed to update status to completed", slog.Any("error", err))
			}
			// Update result fields (PR URL, commit SHA, duration)
			if err := w.store.UpdateExecutionResult(exec.ID, result.PRUrl, result.CommitSHA, duration.Milliseconds()); err != nil {
				w.log.Error("Failed to update execution result", slog.Any("error", err))
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
			if err := w.store.SaveExecutionMetrics(&memory.ExecutionMetrics{
				ExecutionID:      exec.ID,
				TokensInput:      result.TokensInput,
				TokensOutput:     result.TokensOutput,
				TokensTotal:      result.TokensTotal,
				EstimatedCostUSD: result.EstimatedCostUSD,
				FilesChanged:     result.FilesChanged,
				LinesAdded:       result.LinesAdded,
				LinesRemoved:     result.LinesRemoved,
				ModelName:        result.ModelName,
			}); err != nil {
				w.log.Error("Failed to save execution metrics", slog.Any("error", err))
			}
		}

		w.currentTaskID.Store("")
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
