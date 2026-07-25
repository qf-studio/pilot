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

// Monitor tracks task execution progress
type Monitor struct {
	tasks map[string]*TaskState
	mu    sync.RWMutex
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
func (m *Monitor) GetRunningTaskIDs() []string {
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
			return fmt.Errorf("drain timeout: %d tasks still active: %v", len(ids), ids)
		case <-ticker.C:
			// continue polling
		}
	}
}
