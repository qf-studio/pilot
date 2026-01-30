package executor

import (
	"sort"
	"sync"
	"time"
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
func (m *Monitor) Register(taskID, title string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.tasks[taskID] = &TaskState{
		ID:       taskID,
		Title:    title,
		Status:   StatusPending,
		Phase:    "Pending",
		Progress: 0,
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

// UpdateProgress updates task progress
func (m *Monitor) UpdateProgress(taskID, phase string, progress int, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if state, ok := m.tasks[taskID]; ok {
		state.Phase = phase
		state.Progress = progress
		if message != "" {
			state.Message = message
		}
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
