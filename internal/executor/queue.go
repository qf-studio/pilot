package executor

import (
	"sync"
	"time"
)

// MaxRetryAttempts is the maximum number of retry attempts for rate-limited tasks
const MaxRetryAttempts = 3

// PendingTask represents a task waiting for retry after rate limit
type PendingTask struct {
	Task       *Task
	RetryAfter time.Time
	Attempts   int
	QueuedAt   time.Time
	Reason     string
}

// TaskQueue manages pending tasks waiting for rate limit reset
type TaskQueue struct {
	pending []PendingTask
	mu      sync.RWMutex
}

// NewTaskQueue creates a new task queue
func NewTaskQueue() *TaskQueue {
	return &TaskQueue{
		pending: make([]PendingTask, 0),
	}
}

// Add adds a task to the pending queue
func (q *TaskQueue) Add(task *Task, retryAfter time.Time, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Check if task already exists in queue
	for i, p := range q.pending {
		if p.Task.ID == task.ID {
			// Update existing entry
			q.pending[i].RetryAfter = retryAfter
			q.pending[i].Attempts++
			q.pending[i].Reason = reason
			return
		}
	}

	// Add new entry
	q.pending = append(q.pending, PendingTask{
		Task:       task,
		RetryAfter: retryAfter,
		QueuedAt:   time.Now(),
		Attempts:   1,
		Reason:     reason,
	})
}

// GetReady returns tasks that are ready for retry (RetryAfter has passed)
// and removes them from the queue
func (q *TaskQueue) GetReady() []*PendingTask {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	var ready []*PendingTask
	var remaining []PendingTask

	for i := range q.pending {
		p := q.pending[i]
		if now.After(p.RetryAfter) && p.Attempts <= MaxRetryAttempts {
			ready = append(ready, &PendingTask{
				Task:       p.Task,
				RetryAfter: p.RetryAfter,
				Attempts:   p.Attempts,
				QueuedAt:   p.QueuedAt,
				Reason:     p.Reason,
			})
		} else {
			remaining = append(remaining, p)
		}
	}

	q.pending = remaining
	return ready
}

// GetExpired returns tasks that have exceeded max retry attempts
// and removes them from the queue
func (q *TaskQueue) GetExpired() []*PendingTask {
	q.mu.Lock()
	defer q.mu.Unlock()

	var expired []*PendingTask
	var remaining []PendingTask

	for i := range q.pending {
		p := q.pending[i]
		if p.Attempts > MaxRetryAttempts {
			expired = append(expired, &PendingTask{
				Task:       p.Task,
				RetryAfter: p.RetryAfter,
				Attempts:   p.Attempts,
				QueuedAt:   p.QueuedAt,
				Reason:     p.Reason,
			})
		} else {
			remaining = append(remaining, p)
		}
	}

	q.pending = remaining
	return expired
}

// Remove removes a specific task from the queue
func (q *TaskQueue) Remove(taskID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, p := range q.pending {
		if p.Task.ID == taskID {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return true
		}
	}
	return false
}

// Len returns the number of pending tasks
func (q *TaskQueue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.pending)
}

// List returns a copy of all pending tasks (for status display)
func (q *TaskQueue) List() []PendingTask {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]PendingTask, len(q.pending))
	copy(result, q.pending)
	return result
}

// NextRetryTime returns the earliest retry time in the queue, or zero time if empty
func (q *TaskQueue) NextRetryTime() time.Time {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if len(q.pending) == 0 {
		return time.Time{}
	}

	earliest := q.pending[0].RetryAfter
	for _, p := range q.pending[1:] {
		if p.RetryAfter.Before(earliest) {
			earliest = p.RetryAfter
		}
	}
	return earliest
}
