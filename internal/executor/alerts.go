package executor

import "time"

// AlertEventProcessor is an interface for processing alert events.
// This interface is satisfied by alerts.Engine and allows the executor
// to emit events without importing the alerts package directly,
// avoiding import cycles.
type AlertEventProcessor interface {
	ProcessEvent(event AlertEvent)
}

// AlertEvent represents an event that might trigger an alert.
// This mirrors alerts.Event to avoid import cycles.
type AlertEvent struct {
	Type      AlertEventType
	TaskID    string
	TaskTitle string
	Project   string
	Phase     string
	Progress  int
	Error     string
	Metadata  map[string]string
	Timestamp time.Time
}

// AlertEventType categorizes incoming events
type AlertEventType string

const (
	AlertEventTypeTaskStarted   AlertEventType = "task_started"
	AlertEventTypeTaskProgress  AlertEventType = "task_progress"
	AlertEventTypeTaskCompleted AlertEventType = "task_completed"
	AlertEventTypeTaskFailed    AlertEventType = "task_failed"
	AlertEventTypeTaskRetry     AlertEventType = "task_retry"
)
