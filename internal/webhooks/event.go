package webhooks

import (
	"time"
)

// Event represents a webhook event payload.
type Event struct {
	// ID is a unique identifier for this event
	ID string `json:"id"`

	// Type is the event type
	Type EventType `json:"type"`

	// Timestamp is when the event occurred
	Timestamp time.Time `json:"timestamp"`

	// Data contains the event-specific payload
	Data any `json:"data"`
}

// TaskStartedData is the payload for task.started events.
type TaskStartedData struct {
	TaskID      string `json:"task_id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Project     string `json:"project"`
	Source      string `json:"source"` // "linear", "github", "jira", etc.
	SourceID    string `json:"source_id,omitempty"`
}

// TaskProgressData is the payload for task.progress events.
type TaskProgressData struct {
	TaskID   string  `json:"task_id"`
	Phase    string  `json:"phase"`
	Progress float64 `json:"progress"` // 0-100
	Message  string  `json:"message,omitempty"`
}

// TaskCompletedData is the payload for task.completed events.
type TaskCompletedData struct {
	TaskID    string        `json:"task_id"`
	Title     string        `json:"title"`
	Project   string        `json:"project"`
	Duration  time.Duration `json:"duration_ms"`
	PRCreated bool          `json:"pr_created"`
	PRURL     string        `json:"pr_url,omitempty"`
	Summary   string        `json:"summary,omitempty"`
}

// TaskFailedData is the payload for task.failed events.
type TaskFailedData struct {
	TaskID   string        `json:"task_id"`
	Title    string        `json:"title"`
	Project  string        `json:"project"`
	Duration time.Duration `json:"duration_ms"`
	Error    string        `json:"error"`
	Phase    string        `json:"phase,omitempty"`
}

// TaskTimeoutData is the payload for task.timeout events.
// This is a specific failure case where the task exceeded its configured timeout.
type TaskTimeoutData struct {
	TaskID     string        `json:"task_id"`
	Title      string        `json:"title"`
	Project    string        `json:"project"`
	Duration   time.Duration `json:"duration_ms"`
	Timeout    time.Duration `json:"timeout_ms"`
	Complexity string        `json:"complexity"` // trivial, simple, medium, complex
	Phase      string        `json:"phase,omitempty"`
}

// PRCreatedData is the payload for pr.created events.
type PRCreatedData struct {
	TaskID   string `json:"task_id"`
	Project  string `json:"project"`
	PRURL    string `json:"pr_url"`
	PRNumber int    `json:"pr_number"`
	Title    string `json:"title"`
	Branch   string `json:"branch"`
}

// BudgetWarningData is the payload for budget.warning events.
type BudgetWarningData struct {
	Project      string  `json:"project,omitempty"` // Empty for global budget
	CurrentSpend float64 `json:"current_spend"`
	BudgetLimit  float64 `json:"budget_limit"`
	Percentage   float64 `json:"percentage"` // 0-100
	Message      string  `json:"message"`
}

// NewEvent creates a new Event with generated ID and current timestamp.
func NewEvent(eventType EventType, data any) *Event {
	return &Event{
		ID:        generateEventID(),
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}
}

// generateEventID generates a unique event ID.
func generateEventID() string {
	return "evt_" + randomString(16)
}

// randomString generates a random alphanumeric string.
func randomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		// Use time-based seed for simplicity (in production, use crypto/rand)
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		time.Sleep(time.Nanosecond)
	}
	return string(b)
}
