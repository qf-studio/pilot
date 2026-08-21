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

	// AlertEventTypeDeadManAttempt/Success/Failure (TASK-441 L2, GH-4709)
	// relay a alerts.DeadManTracker's Record{Attempt,Success,Failure} calls
	// across the executor/alerts package boundary: internal/executor cannot
	// import internal/alerts directly (that package already imports this
	// one, for ExecutionLifecycle), so an executor-side seam (e.g.
	// runSelfReview) cannot hold a live *alerts.DeadManTracker reference.
	// These string values match alerts.EventTypeDeadMan{Attempt,Success,
	// Failure} exactly — EngineAdapter.ProcessEvent forwards them unchanged
	// via a string-typed cast, and Metadata["tracker"] selects which
	// registered tracker (by name) the call routes to.
	AlertEventTypeDeadManAttempt AlertEventType = "dead_man_attempt"
	AlertEventTypeDeadManSuccess AlertEventType = "dead_man_success"
	AlertEventTypeDeadManFailure AlertEventType = "dead_man_failure"

	// AlertEventTypeEscalation (GH-5079) mirrors alerts.EventTypeEscalation
	// ("escalation") byte-for-byte — EngineAdapter.ProcessEvent forwards
	// AlertEventType values across the executor/alerts package boundary via a
	// direct string cast (adapter.go), so this value must match exactly.
	// Routes through alerts.Engine.handleEscalation, which (post-PR#5069)
	// falls back to rendering event.Error when the circuit-breaker-only
	// metadata (trips_in_hour/escalation_threshold/last_pr/last_reason) is
	// absent — every non-circuit-breaker escalation emitter, including the
	// pilot-failed-retry-exhausted hook (title_rejection.go), populates
	// Error rather than that metadata.
	AlertEventTypeEscalation AlertEventType = "escalation"
)

// SelfReviewDeadManTrackerName is the alerts.DeadManTracker registration
// name (TASK-441 L2, GH-4709) shared between runSelfReview (runner.go,
// which records attempts/successes/failures via the AlertEventTypeDeadMan*
// relay above) and startGithubSDKPollerForRepo (cmd/pilot/poller_github.go,
// which registers the tracker against AlertTypeSelfReviewFailureStreak at
// startup) — guards the GH-4702 incident class (self-review silently dead
// for months).
const SelfReviewDeadManTrackerName = "self_review"

// LabelStripDeadManTrackerName is the alerts.DeadManTracker registration
// name (GH-4866) for stripInProgressLabelOnTerminalFailure (lifecycle.go) —
// the removal-side counterpart of the apply-side label_lifecycle tracker
// (labelLifecycleDeadManTrackerName, cmd/pilot/handlers.go). It routes
// through the same alerts.AlertTypeLabelLifecycleFailureStreak rule as the
// apply-side tracker (mirrors how the four finish-tripwire trackers all
// share one AlertType): both are the same "label lifecycle silently broken"
// incident class, just counted separately by name. Global rather than
// per-repo — unlike the apply-side call site, ExecutionLifecycle has no
// live repo-owner context to key on here (only ProjectPath/TaskID), and the
// kill-drill gap this closes (GH-4866) was "wired to nothing at all," not
// dilution across repos.
const LabelStripDeadManTrackerName = "label_strip"

// PushRetryExhaustedDeadManTrackerName is the alerts.DeadManTracker
// registration name (GH-4866) for the git-push retry loop in
// Runner.executeWithOptions (runner.go): a sustained run of pushes that
// exhaust every gitPushRetryAttempts attempt leaves committed work stranded
// with no PR, and the kill-drill found this producing zero dead-man signal
// despite repeated occurrences during the drill window.
const PushRetryExhaustedDeadManTrackerName = "push_retry_exhausted"
