package budget

import (
	"context"
	"fmt"
	"time"
)

// TaskLimiter provides per-task budget enforcement
type TaskLimiter struct {
	maxTokens   int64
	maxDuration time.Duration
	startTime   time.Time
	tokenCount  int64
	exceeded    bool
	reason      string
}

// NewTaskLimiter creates a limiter for a single task
func NewTaskLimiter(maxTokens int64, maxDuration time.Duration) *TaskLimiter {
	return &TaskLimiter{
		maxTokens:   maxTokens,
		maxDuration: maxDuration,
		startTime:   time.Now(),
	}
}

// AddTokens records token usage and checks if limit exceeded
func (l *TaskLimiter) AddTokens(count int64) bool {
	if l.exceeded || l.maxTokens <= 0 {
		return !l.exceeded
	}

	l.tokenCount += count
	if l.tokenCount > l.maxTokens {
		l.exceeded = true
		l.reason = fmt.Sprintf("token limit exceeded: %d / %d", l.tokenCount, l.maxTokens)
		return false
	}
	return true
}

// CheckDuration checks if duration limit exceeded
func (l *TaskLimiter) CheckDuration() bool {
	if l.exceeded || l.maxDuration <= 0 {
		return !l.exceeded
	}

	elapsed := time.Since(l.startTime)
	if elapsed > l.maxDuration {
		l.exceeded = true
		l.reason = fmt.Sprintf("duration limit exceeded: %v / %v", elapsed.Round(time.Second), l.maxDuration)
		return false
	}
	return true
}

// IsExceeded returns whether any limit was exceeded
func (l *TaskLimiter) IsExceeded() bool {
	return l.exceeded
}

// Reason returns the reason for exceeding
func (l *TaskLimiter) Reason() string {
	return l.reason
}

// GetTokens returns current token count
func (l *TaskLimiter) GetTokens() int64 {
	return l.tokenCount
}

// GetDuration returns elapsed duration
func (l *TaskLimiter) GetDuration() time.Duration {
	return time.Since(l.startTime)
}

// CreateContext creates a context with timeout for per-task duration limit
func (l *TaskLimiter) CreateContext(parent context.Context) (context.Context, context.CancelFunc) {
	if l.maxDuration <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, l.maxDuration)
}

// TaskContext holds budget context for a task execution
type TaskContext struct {
	TeamID     string
	UserID     string
	TaskID     string
	ProjectID  string
	Limiter    *TaskLimiter
	BudgetLeft float64
}

// NewTaskContext creates a new task context with budget info
func NewTaskContext(teamID, userID, taskID, projectID string, limiter *TaskLimiter, budgetLeft float64) *TaskContext {
	return &TaskContext{
		TeamID:     teamID,
		UserID:     userID,
		TaskID:     taskID,
		ProjectID:  projectID,
		Limiter:    limiter,
		BudgetLeft: budgetLeft,
	}
}
