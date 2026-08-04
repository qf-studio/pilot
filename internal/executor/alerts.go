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
	AlertEventTypeTaskStarted      AlertEventType = "task_started"
	AlertEventTypeTaskProgress     AlertEventType = "task_progress"
	AlertEventTypeTaskCompleted    AlertEventType = "task_completed"
	AlertEventTypeTaskFailed       AlertEventType = "task_failed"
	AlertEventTypeTaskRetry        AlertEventType = "task_retry"
	AlertEventTypeTaskTimeout      AlertEventType = "task_timeout"
	AlertEventTypeHeartbeatTimeout AlertEventType = "heartbeat_timeout"
	AlertEventTypeWatchdogKill     AlertEventType = "watchdog_kill"

	// GH-917: Specific error types for better classification
	AlertEventTypeRateLimit   AlertEventType = "rate_limit"
	AlertEventTypeConfigError AlertEventType = "config_error"
	AlertEventTypeAPIError    AlertEventType = "api_error"

	// GH-2332: OOM/SIGKILL events are worth separating from the generic
	// task_failed bucket so operators can spot memory-pressure patterns
	// and wire dedicated remediation (shrink context, lower concurrency).
	AlertEventTypeOOMKilled AlertEventType = "oom_killed"

	// GH-925: Stagnation detection alerts
	AlertEventTypeStagnationWarn  AlertEventType = "stagnation_warn"
	AlertEventTypeStagnationPause AlertEventType = "stagnation_pause"
	AlertEventTypeStagnationAbort AlertEventType = "stagnation_abort"

	// AlertEventTypeGithubSideEffect (GH-4670): the post-run audit
	// (sideeffect_audit.go) found a GitHub issue in the task's own repo
	// closed or reopened during the run window OTHER than the issue the
	// session was dispatched to fix — the GH-4649 incident class.
	AlertEventTypeGithubSideEffect AlertEventType = "github_sideeffect"

	// AlertEventTypeGhGuardDenied (GH-4671): the gh-guard shim refused a
	// `gh` invocation during the run — the preventive counterpart to
	// AlertEventTypeGithubSideEffect: a call that never reached GitHub
	// because it was blocked at the Bash tool boundary, rather than one
	// detected after the fact.
	AlertEventTypeGhGuardDenied AlertEventType = "gh_guard_denied"
)
