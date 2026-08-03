package executor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TaskStatus represents the status of a task
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusQueued    TaskStatus = "queued"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCancelled TaskStatus = "cancelled"
	StatusStalled   TaskStatus = "stalled"
	// StatusNoOp marks a run that ended deterministically with no deliverable
	// (e.g. "no new commit produced") — a non-failure terminal outcome,
	// distinct from StatusFailed so the card doesn't misreport a no-op as a
	// genuine failure (GH-4490 subtask 2).
	StatusNoOp TaskStatus = "no_op"
)

// TaskState holds the current state of a task
type TaskState struct {
	ID          string
	Title       string
	Status      TaskStatus
	Phase       string
	Progress    int
	Message     string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       string
	PRUrl       string
	IssueURL    string
	ProjectPath string // Resolved project directory for this task (GH-2167)
	ProjectName string // Short project name for display (GH-2167)
}

// LiveWorkerChecker reports which task IDs a live executor worker is
// currently processing right now — the same "is a live worker actually
// holding this task" signal *Dispatcher.GetRunningTaskIDs already exposes to
// the autopilot orphan-running sweep (GH-4412). ReconcileDeadOwners (GH-4609)
// uses it to tell a genuinely in-flight task apart from a Monitor entry whose
// owning executor process is gone.
type LiveWorkerChecker interface {
	GetRunningTaskIDs() []string
}

// Monitor tracks task execution progress
type Monitor struct {
	tasks map[string]*TaskState
	mu    sync.RWMutex

	// liveWorkers and execStore back ReconcileDeadOwners (GH-4609): the
	// live-worker liveness signal and the execution-row heartbeat fallback
	// used to detect an active-registry entry whose owning executor process
	// is gone. Both nil by default — ReconcileDeadOwners is then a no-op,
	// preserving today's behavior for any caller that never wires one (e.g.
	// tests) — until SetLiveWorkerChecker/SetExecutionStore are called.
	liveWorkers LiveWorkerChecker
	execStore   *memory.Store
}

// NewMonitor creates a new task monitor
func NewMonitor() *Monitor {
	return &Monitor{
		tasks: make(map[string]*TaskState),
	}
}

// Register registers a new task
func (m *Monitor) Register(taskID, title, issueURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.tasks[taskID] = &TaskState{
		ID:       taskID,
		Title:    title,
		Status:   StatusPending,
		Phase:    "Pending",
		Progress: 0,
		IssueURL: issueURL,
	}
}

// Hydrate seeds a task's state directly from persisted data, bypassing the
// normal Register→Queue/Start transition sequence. Used at startup to
// reconstruct monitor state from DB rows after a restart (GH-4246) — plain
// Register would leave every hydrated task stuck at "pending" and drop
// StartedAt for already-running tasks.
func (m *Monitor) Hydrate(taskID, title, issueURL string, status TaskStatus, startedAt *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := &TaskState{
		ID:       taskID,
		Title:    title,
		IssueURL: issueURL,
		Status:   status,
	}
	switch status {
	case StatusRunning:
		state.Phase = "Running"
		state.StartedAt = startedAt
	default:
		state.Phase = "Queued"
	}
	m.tasks[taskID] = state
}

// HydrateFromStore seeds the monitor from the store's non-terminal execution
// rows (see Hydrate and memory.Store.GetTasksForMonitorHydration). Call once
// at startup, after both the monitor and store are constructed and before
// the dashboard's first refresh tick, so a restart with queued or running
// work in the DB doesn't leave the queue/progress view blind (GH-4246).
func (m *Monitor) HydrateFromStore(store *memory.Store) error {
	if store == nil {
		return nil
	}
	tasks, err := store.GetTasksForMonitorHydration()
	if err != nil {
		return fmt.Errorf("hydrate monitor from store: %w", err)
	}
	for _, t := range tasks {
		status := TaskStatus(t.Status)
		if status == StatusPending {
			status = StatusQueued
		}
		title := t.Title
		if title == "" {
			title = t.TaskID
		}
		m.Hydrate(t.TaskID, title, t.IssueURL, status, t.StartedAt)
	}
	return nil
}

// ReconcileWithStore corrects any in-memory task whose Status disagrees with
// its executions row in either direction (GH-4490; extended bidirectionally
// by TASK-420/GH-4537):
//
//  1. non-terminal Monitor state, terminal store row — the original GH-4490
//     case. Event-driven transitions (Start/Complete/Fail/...) are skipped or
//     raced in some failure paths — e.g. a no-commit failure or an externally
//     closed PR that updates the executions row without ever calling back
//     into this Monitor — which otherwise leaves a card stuck at
//     "running"/100% forever.
//  2. terminal Monitor state, store row still "running" — the false-complete
//     case captured 2026-07-24 22:17:07Z (queue showed "✓ done GH-4536" while
//     its executions row was running with 3 live processes). Whatever raced
//     Monitor into a terminal state early (duplicate dispatch, a stale
//     completion callback, ...), the executions row is still the ground
//     truth for "is this task's work actually finished" — a terminal Monitor
//     card must not survive a store row that says otherwise.
//
// The executions table is the source of truth, so call this periodically
// (e.g. alongside the dashboard's refresh tick, before GetAll()) as a
// self-correcting backstop on top of the normal event path, not a
// replacement for it.
func (m *Monitor) ReconcileWithStore(store *memory.Store) error {
	if store == nil {
		return nil
	}

	m.mu.RLock()
	type candidate struct {
		id          string
		projectPath string
		status      TaskStatus
	}
	candidates := make([]candidate, 0, len(m.tasks))
	for id, state := range m.tasks {
		candidates = append(candidates, candidate{id: id, projectPath: state.ProjectPath, status: state.Status})
	}
	m.mu.RUnlock()

	for _, c := range candidates {
		dbStatus, err := store.GetExecutionStatusByTaskID(c.id, c.projectPath)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // no execution row yet (e.g. registered but not dispatched) — nothing to reconcile
			}
			return fmt.Errorf("reconcile monitor with store: %w", err)
		}

		if dbStatus == "running" {
			// Case 2: ledger says running but Monitor already shows a
			// terminal outcome — revert the false-complete/false-terminal
			// card back to running so a live task never renders done.
			if c.status == StatusRunning || c.status == StatusQueued || c.status == StatusPending {
				continue
			}
			m.mu.Lock()
			if state, ok := m.tasks[c.id]; ok && state.Status != StatusRunning {
				state.Status = StatusRunning
				state.CompletedAt = nil
				state.Error = ""
				state.Phase = "Running (reconciled)"
			}
			m.mu.Unlock()
			continue
		}

		// Case 1: existing non-terminal -> terminal correction.
		if c.status != StatusRunning && c.status != StatusQueued && c.status != StatusPending {
			continue
		}

		newStatus, terminal := terminalMonitorStatus(dbStatus)
		if !terminal {
			continue
		}

		m.mu.Lock()
		if state, ok := m.tasks[c.id]; ok {
			switch state.Status {
			case StatusRunning, StatusQueued, StatusPending:
				now := time.Now()
				state.Status = newStatus
				state.CompletedAt = &now
				state.Phase = string(newStatus)
				if newStatus == StatusCompleted {
					state.Progress = 100
				} else if state.Error == "" {
					state.Error = fmt.Sprintf("reconciled from executions table: status=%q", dbStatus)
				}
			}
		}
		m.mu.Unlock()
	}

	return nil
}

// terminalMonitorStatus maps a raw executions.status value to the Monitor's
// terminal TaskStatus for card display. Mirrors the non-terminal set used by
// GetTasksForMonitorHydration (queued/pending/running); everything else is
// terminal. no_op gets its own TaskStatus (GH-4490 subtask 2) so a no-commit
// run reads distinctly from a genuine failure; the remaining subtypes with no
// direct TaskStatus equivalent (declined, declined-preflight, rate_limited,
// infra, skipped, failed) still fold into StatusFailed — the point there is
// only to stop the card from displaying "running" once the DB row is
// terminal, not to preserve every outcome subtype (GH-4490).
func terminalMonitorStatus(dbStatus string) (TaskStatus, bool) {
	switch dbStatus {
	case "", "queued", "pending", "running":
		return "", false
	case "completed":
		return StatusCompleted, true
	case "cancelled":
		return StatusCancelled, true
	case "stalled":
		return StatusStalled, true
	case "no_op":
		return StatusNoOp, true
	default:
		return StatusFailed, true
	}
}

// SetLiveWorkerChecker wires the live-worker liveness signal ReconcileDeadOwners
// (GH-4609) uses to detect a Monitor entry whose owning executor process is
// gone. In production this is *executor.Dispatcher, wired once the Dispatcher
// exists (see cmd/pilot/main.go). Optional — nil keeps ReconcileDeadOwners a
// no-op, matching today's behavior for any caller that never wires one.
func (m *Monitor) SetLiveWorkerChecker(c LiveWorkerChecker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.liveWorkers = c
}

// SetExecutionStore wires the store ReconcileDeadOwners (GH-4609) queries for
// a dead-owner candidate's execution_event heartbeat before finalizing it —
// the "or its execution row has progressed within N minutes" fallback that
// protects a task caught in the narrow race between Monitor.Start and the
// live-worker checker registering it. Optional — nil skips the fallback
// check (a dead-owner candidate is finalized on the liveness signal alone).
func (m *Monitor) SetExecutionStore(store *memory.Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execStore = store
}

// SetProjectInfo sets the project path and name for a task (GH-2167).
func (m *Monitor) SetProjectInfo(taskID, projectPath, projectName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		state.ProjectPath = projectPath
		state.ProjectName = projectName
	}
}

// Queue marks a task as queued in the dispatcher (waiting for execution slot).
func (m *Monitor) Queue(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		state.Status = StatusQueued
		state.Phase = "Queued"
	}
}

// Start marks a task as started
func (m *Monitor) Start(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		now := time.Now()
		state.Status = StatusRunning
		state.StartedAt = &now
		state.Phase = "Starting"
		state.Progress = 0
	}
}

// UpdateProgress updates task progress.
// Progress is monotonic — never decreases (except reset to 0 on task start).
// An unknown taskID creates a minimal running entry instead of dropping the
// event (GH-4246): a progress event is proof the task is executing, and
// silently discarding it is what made post-restart running tasks invisible
// even though they were live — visible only in logs, never in the monitor.
func (m *Monitor) UpdateProgress(taskID, phase string, progress int, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.tasks[taskID]
	if !ok {
		state = &TaskState{ID: taskID, Title: taskID, Status: StatusRunning}
		m.tasks[taskID] = state
	}

	state.Phase = phase
	// Enforce monotonic progress (never go backwards)
	if progress >= state.Progress {
		state.Progress = progress
	}
	if message != "" {
		state.Message = message
	}
}

// Complete marks a task as completed
func (m *Monitor) Complete(taskID, prURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		now := time.Now()
		state.Status = StatusCompleted
		state.CompletedAt = &now
		state.Phase = "Completed"
		state.Progress = 100
		state.PRUrl = prURL
	}
}

// Fail marks a task as failed
func (m *Monitor) Fail(taskID, errorMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		now := time.Now()
		state.Status = StatusFailed
		state.CompletedAt = &now
		state.Phase = "Failed"
		state.Error = errorMsg
	}
}

// NoOp marks a task as terminated with no deliverable (e.g. "no new commit
// produced") — a non-failure terminal outcome, so callers must use this
// instead of Fail when the classified execution outcome is no_op (GH-4490
// subtask 2).
func (m *Monitor) NoOp(taskID, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		now := time.Now()
		state.Status = StatusNoOp
		state.CompletedAt = &now
		state.Phase = "No-op"
		state.Error = message
	}
}

// Stall marks a task as stalled (no event activity for stall_timeout). TASK-308.
func (m *Monitor) Stall(taskID, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		now := time.Now()
		state.Status = StatusStalled
		state.CompletedAt = &now
		state.Phase = "Stalled"
		state.Error = reason
	}
}

// ReleasePriorAttempt finalizes taskID's active-registry entry as stalled if
// it is still showing a non-terminal status (Running/Queued/Pending) — the
// stalled->retry seam GH-4609 subtask 2 requires: the moment a fresh
// generation is granted for a task whose prior claim stalled
// (Dispatcher.beginWithGenerationRetry's priorClaimWasStalled branch), the
// prior attempt's entry must be released/finished right there, rather than
// leaving it to eventually age past deadOwnerGracePeriod/
// deadOwnerHeartbeatWindow and get swept up by the next drain-time
// ReconcileDeadOwners call (subtask 1's backstop, which exists for the
// general dead-owner case but can take up to deadOwnerHeartbeatWindow to
// fire). Without this, a retry granted while the prior attempt's own Stall()
// call hasn't landed yet (or never lands, e.g. an externally killed worker
// process) leaves the SAME map entry straddling two generations — the
// dashboard/drain count would keep reading the superseded attempt's stale
// Running/Queued status until the backstop eventually catches up, instead of
// the retried task being counted exactly once, under its fresh generation.
//
// A no-op when the entry is missing or already terminal (including the
// common case where the prior attempt's own Stall() already ran) — this only
// forces the transition when it hasn't happened yet.
func (m *Monitor) ReleasePriorAttempt(taskID, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state, ok := m.tasks[taskID]
	if !ok {
		return
	}
	switch state.Status {
	case StatusRunning, StatusQueued, StatusPending:
	default:
		return
	}

	now := time.Now()
	state.Status = StatusStalled
	state.CompletedAt = &now
	state.Phase = "Stalled"
	state.Error = reason
}

// Cancel marks a task as cancelled
func (m *Monitor) Cancel(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		now := time.Now()
		state.Status = StatusCancelled
		state.CompletedAt = &now
		state.Phase = "Cancelled"
	}
}

// Get returns the state of a task
func (m *Monitor) Get(taskID string) (*TaskState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state, ok := m.tasks[taskID]
	if !ok {
		return nil, false
	}

	// Return a copy
	copy := *state
	return &copy, true
}

// GetAll returns all task states sorted by TaskID for stable ordering
func (m *Monitor) GetAll() []*TaskState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make([]*TaskState, 0, len(m.tasks))
	for _, state := range m.tasks {
		copy := *state
		states = append(states, &copy)
	}

	// Sort by ID for stable ordering (prevents dashboard jumping)
	sort.Slice(states, func(i, j int) bool {
		return states[i].ID < states[j].ID
	})

	return states
}

// GetRunning returns all running tasks sorted by TaskID for stable ordering
func (m *Monitor) GetRunning() []*TaskState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var running []*TaskState
	for _, state := range m.tasks {
		if state.Status == StatusRunning {
			copy := *state
			running = append(running, &copy)
		}
	}

	// Sort by ID for stable ordering
	sort.Slice(running, func(i, j int) bool {
		return running[i].ID < running[j].ID
	})

	return running
}

// Remove removes a task from monitoring
func (m *Monitor) Remove(taskID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, taskID)
}

// Count returns the number of tasks
func (m *Monitor) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}

// GetRunningTaskIDs returns IDs of currently running or queued tasks.
// Implements upgrade.TaskChecker interface for graceful drain during hot upgrade.
//
// GH-4609: reconciles dead-owner entries first, so a StatusRunning entry
// whose owning executor process is gone is finalized as failed and excluded
// from the result instead of blocking a caller (e.g. self-upgrade drain)
// forever. See ReconcileDeadOwners.
func (m *Monitor) GetRunningTaskIDs() []string {
	m.ReconcileDeadOwners()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var ids []string
	for _, state := range m.tasks {
		if state.Status == StatusRunning || state.Status == StatusQueued {
			ids = append(ids, state.ID)
		}
	}
	return ids
}

// GetActiveRunningTaskIDs returns IDs of tasks that are currently RUNNING —
// unlike GetRunningTaskIDs, queued tasks are excluded. GH-4683: a self-upgrade
// drain that waits on GetRunningTaskIDs also waits on the queue, and nothing
// stops the dispatcher from admitting new queued work while the drain is in
// progress — on a busy box (pollers/retries keep enqueueing) the queue never
// empties and the drain times out no matter how long the window is (the
// v2.252.0 rollout incident: 2 x 30-minute timeouts against a queue that
// never dropped below ~5). Pair this with Dispatcher.PauseAdmission, which
// stops new admission for the duration of the wait, so "currently running"
// is actually bounded by the existing timeout instead of chasing a moving
// target.
//
// GetRunningTaskIDs deliberately keeps its broader running-or-queued
// semantics for its other caller (the orphan-running sweep's exclusion set,
// TASK-399/GH-4209) — that usage is unaffected by this addition.
func (m *Monitor) GetActiveRunningTaskIDs() []string {
	m.ReconcileDeadOwners()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var ids []string
	for _, state := range m.tasks {
		if state.Status == StatusRunning {
			ids = append(ids, state.ID)
		}
	}
	return ids
}

// deadOwnerGracePeriod protects a task Monitor.Start just marked running from
// being reconciled as a dead owner before its worker has had a chance to
// register with the wired LiveWorkerChecker — Start() and the Dispatcher's
// own "processing" flag are not set atomically with each other. GH-4609.
const deadOwnerGracePeriod = 30 * time.Second

// deadOwnerHeartbeatWindow bounds how recently a dead-owner candidate's
// execution row must have logged an execution_event to still count as
// "progressing" despite having no live worker. Mirrors the orphan-running
// sweep's heartbeat check (autopilot/controller.go's
// orphanRunningHeartbeatWindow), but deliberately skips that sweep's 120m
// floor (minOrphanRunningThreshold) — a stuck drain blocks every queued task
// in the daemon, not just one execution row, so this reconciliation needs to
// resolve much sooner. GH-4609.
const deadOwnerHeartbeatWindow = 10 * time.Minute

// ReconcileDeadOwners finalizes any Monitor entry still showing StatusRunning
// whose owning executor process is gone, so it stops blocking self-upgrade
// drain forever — the GH-72 stalled-retry incident (GH-4609): the retry
// produced a PR and left no worker process alive, but the in-memory active
// registry kept counting the original entry, so drain looped
// "1 tasks still active" every 5 minutes indefinitely.
//
// A "dead owner" is a StatusRunning entry that is (a) absent from the wired
// LiveWorkerChecker's current set — no live worker holds it right now — and
// (b) has logged no execution_event heartbeat within deadOwnerHeartbeatWindow.
// Both conditions must hold before an entry is finalized: (a) alone can
// misfire in the race between Monitor.Start and the live-worker checker
// registering the task (deadOwnerGracePeriod also guards this), and (b) alone
// can't distinguish a dead task from one legitimately mid-tool-call with no
// heartbeat yet (GH-4412).
//
// No-op (fail-open) when no LiveWorkerChecker is wired via
// SetLiveWorkerChecker — call sites that never wire one (tests, and any
// Monitor not driven by the polling-mode daemon) keep today's behavior
// exactly.
func (m *Monitor) ReconcileDeadOwners() {
	m.mu.RLock()
	liveWorkers := m.liveWorkers
	execStore := m.execStore
	m.mu.RUnlock()

	if liveWorkers == nil {
		return
	}

	live := make(map[string]bool)
	for _, id := range liveWorkers.GetRunningTaskIDs() {
		live[id] = true
	}

	type candidate struct {
		id          string
		projectPath string
	}
	var candidates []candidate
	now := time.Now()

	m.mu.RLock()
	for id, state := range m.tasks {
		if state.Status != StatusRunning || live[id] {
			continue
		}
		if state.StartedAt != nil && now.Sub(*state.StartedAt) < deadOwnerGracePeriod {
			continue
		}
		candidates = append(candidates, candidate{id: id, projectPath: state.ProjectPath})
	}
	m.mu.RUnlock()

	for _, c := range candidates {
		if execStore != nil && executionRecentlyProgressed(execStore, c.id, c.projectPath) {
			continue
		}

		m.mu.Lock()
		if state, ok := m.tasks[c.id]; ok && state.Status == StatusRunning {
			completedAt := time.Now()
			state.Status = StatusFailed
			state.CompletedAt = &completedAt
			state.Phase = "Failed"
			state.Error = "reconciled: dead-owner active-registry entry (no live executor process, execution row not progressing) — GH-4609"
		}
		m.mu.Unlock()
	}
}

// executionRecentlyProgressed reports whether taskID's most recent execution
// row has logged an execution_event within deadOwnerHeartbeatWindow — the
// corroborating "still making progress" signal ReconcileDeadOwners checks
// before finalizing a candidate with no live worker. Fails open (treats as
// progressing) on any store error so a store hiccup can never wrongly
// finalize a possibly-live task; a clean "no execution row at all" is treated
// as not progressing.
func executionRecentlyProgressed(store *memory.Store, taskID, projectPath string) bool {
	exec, err := store.GetLatestExecutionByTaskID(taskID, projectPath)
	if err != nil {
		return !errors.Is(err, sql.ErrNoRows)
	}

	events, err := store.ListExecutionEvents(exec.ID)
	if err != nil {
		return true
	}
	if len(events) == 0 {
		return false
	}
	return time.Since(events[len(events)-1].OccurredAt) < deadOwnerHeartbeatWindow
}

// ErrDrainTimeout is the sentinel wrapped into WaitForTasks' timeout error so
// callers (e.g. cmd/pilot's self-upgrade loop) can distinguish "still-active
// tasks blocked the drain" from other TaskChecker failures via errors.Is,
// without parsing the message. GH-4609: the self-upgrade alert path needs
// this to gate on consecutive drain-timeout occurrences specifically,
// leaving other upgrade failures (bad download, unwritable binary dir, ...)
// alerting immediately as before.
var ErrDrainTimeout = errors.New("drain timeout")

// WaitForTasks polls until all running/queued tasks complete or context expires.
// Implements upgrade.TaskChecker interface for graceful drain during hot upgrade.
func (m *Monitor) WaitForTasks(ctx context.Context, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		ids := m.GetRunningTaskIDs()
		if len(ids) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("%w: %d tasks still active: %v", ErrDrainTimeout, len(ids), ids)
		case <-ticker.C:
			// continue polling
		}
	}
}

// WaitForRunningTasks polls until all currently RUNNING tasks complete or
// context/timeout expires — unlike WaitForTasks, queued tasks never count
// against this wait, so a saturated (or continuously replenished) queue can
// no longer block it. GH-4683: pair with Dispatcher.PauseAdmission so no new
// task starts running during the wait; the timeout then genuinely bounds
// "how long can whatever is already running take", not queue depth.
func (m *Monitor) WaitForRunningTasks(ctx context.Context, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		ids := m.GetActiveRunningTaskIDs()
		if len(ids) == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("%w: %d running task(s) still in flight (queue depth no longer blocks the drain): %v", ErrDrainTimeout, len(ids), ids)
		case <-ticker.C:
			// continue polling
		}
	}
}
