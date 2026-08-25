package alerts

import "time"

// Severity levels for alerts
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// AlertType categorizes alerts
type AlertType string

const (
	// Operational alerts
	AlertTypeTaskStuck        AlertType = "task_stuck"
	AlertTypeTaskFailed       AlertType = "task_failed"
	AlertTypeConsecutiveFails AlertType = "consecutive_failures"
	AlertTypeServiceUnhealthy AlertType = "service_unhealthy"

	// Cost/Usage alerts
	AlertTypeDailySpend     AlertType = "daily_spend_exceeded"
	AlertTypeBudgetDepleted AlertType = "budget_depleted"
	AlertTypeUsageSpike     AlertType = "usage_spike"

	// Security alerts
	AlertTypeUnauthorizedAccess AlertType = "unauthorized_access"
	AlertTypeSensitiveFile      AlertType = "sensitive_file_modified"
	AlertTypeUnusualPattern     AlertType = "unusual_pattern"

	// Autopilot health alerts (GH-728)
	AlertTypeFailedQueueHigh    AlertType = "failed_queue_high"
	AlertTypeCircuitBreakerTrip AlertType = "circuit_breaker_trip"
	AlertTypeAPIErrorRateHigh   AlertType = "api_error_rate_high"
	AlertTypePRStuckWaitingCI   AlertType = "pr_stuck_waiting_ci"

	// Deadlock detection (GH-849)
	AlertTypeDeadlock AlertType = "deadlock"

	// Escalation alerts (GH-848)
	AlertTypeEscalation AlertType = "escalation"

	// Heartbeat timeout (GH-884)
	AlertTypeHeartbeatTimeout AlertType = "heartbeat_timeout"

	// Eval regression detection (GH-2065)
	AlertTypeEvalRegression AlertType = "eval_regression"

	// Release monitoring (GH-3952): a merged pilot/GH-* PR that never
	// produced its expected release tag.
	AlertTypeReleaseMissing AlertType = "release_missing"

	// Lane-starvation detection (GH-4454): a project lane has open
	// pilot-labeled issues but nothing queued/running for too many
	// consecutive poll cycles.
	AlertTypeLaneStarvation AlertType = "lane_starvation"

	// Dispatch loop breaker (GH-4469): the same task has been
	// dispatched-and-rejected (gated or genuinely failed) 10+ consecutive
	// times without ever completing — see repickLoopBreakerThreshold in
	// cmd/pilot/repick_backoff.go, GH-4391's 4,233-cycle incident.
	AlertTypeDispatchLoopBreaker AlertType = "dispatch_loop_breaker"

	// GitHub side-effect (GH-4670): the executor's post-run audit found a
	// GitHub issue in the task's own repo closed or reopened during the run
	// window OTHER than the issue the session was dispatched to fix — the
	// GH-4649 incident class (an executor session improvised `gh issue
	// close` plus a label on a sibling issue mid-run).
	AlertTypeGithubSideEffect AlertType = "github_sideeffect"

	// Intent judge failure streak (GH-4669): the pre-flight/post-hoc intent
	// judge subprocess has failed (fail-open) N consecutive times in a row —
	// the class of incident that hid a 17-day, 100%, 4,321-invocation
	// failure streak (all context_deadline exceeded) behind a silent
	// fail-open, discovered only while diagnosing GH-4648.
	AlertTypeIntentJudgeFailureStreak AlertType = "intent_judge_failure_streak"

	// Dead-man tracker generic streak alerts (TASK-441 L2, GH-4709):
	// registrations that route through the generic DeadManTracker path
	// (handleDeadManStreak) rather than a pre-existing dedicated handler.
	// Label-lifecycle: the post-GH-4692 notifyTaskStartedSDK path has gone
	// silent for 19 days (GH-4687) with zero errors surfaced. Self-review:
	// runSelfReview has gone silent for months (GH-4702) the same way. GH-4866
	// added a second tracker under this same AlertType — executor.
	// LabelStripDeadManTrackerName, the removal-side counterpart wired into
	// stripInProgressLabelOnTerminalFailure (lifecycle.go) — mirroring how
	// AlertTypeFinishTripwireFailureStreak below already covers four
	// independently-counted trackers under one rule.
	AlertTypeLabelLifecycleFailureStreak AlertType = "label_lifecycle_failure_streak"
	AlertTypeSelfReviewFailureStreak     AlertType = "self_review_failure_streak"

	// AlertTypeFinishTripwireFailureStreak (TASK-441 L5, GH-4716) is the
	// shared alert type behind all four finish-tripwire dead-man trackers
	// (executor.FinishTripwireTrackerNames) registered on
	// ExecutionLifecycle.Persist's post-terminal invariant sweep —
	// root-clean, label lifecycle, decomposed-children-terminal, and
	// worktree-pruned/no-commits-without-PR. One AlertType covers all four
	// since each tracker still counts and streaks independently
	// (DeadManTracker instances are per-name); this only selects which rule
	// fires when any one of them reaches its threshold.
	AlertTypeFinishTripwireFailureStreak AlertType = "finish_tripwire_failure_streak"

	// AlertTypePushRetryExhaustedFailureStreak (GH-4866): the git-push retry
	// loop (Runner.executeWithOptions, runner.go) exhausted every retry
	// attempt N+ consecutive times without a success — committed work left
	// stranded with no PR. Previously observable only as isolated "Push
	// failed, retrying" WARN lines with no dead-man coverage at all (the
	// GH-4866 kill-drill's unwired-seam finding).
	AlertTypePushRetryExhaustedFailureStreak AlertType = "push_retry_exhausted_failure_streak"

	// AlertTypeEnvClassFailureStreak (GH-5217): a task's consecutive
	// env-class (credential/environment) failure count — see
	// executor.IsEnvClassFailure and the GH-5211 carve-out in
	// Dispatcher.beginWithGenerationRetry — has reached the dispatcher's
	// alert threshold (envClassFailureStreakThreshold). GH-5211 made
	// env-class failures exempt from the identical-failure streak
	// escalation so they retry forever via ordinary backoff; this type is
	// the only thing that surfaces a persistent credential break to an
	// operator instead of it retrying silently forever (PR#5214 review
	// note 1).
	AlertTypeEnvClassFailureStreak AlertType = "env_class_failure_streak"
)

// HandlerEmittedAlertTypes lists every AlertType whose handler
// (handleDispatchLoopBreaker, handleIntentJudgeFailureStreak,
// handleDeadManStreak — engine.go/deadman.go) fires without any
// Condition-based counting of its own: the caller already computed and
// gated on the exact threshold before emitting the event, so if no enabled
// rule exists for one of these types by the time the event arrives, the
// alert is dropped silently no matter what — each of those handlers WARNs
// on that no-match case (GH-4866), but the WARN only fires after the fact.
// CheckAlertRuleCoverage (internal/health/subsystems.go) and
// TestHandlerEmittedAlertTypesHaveCoverage (types_test.go) both key off
// this list so the doctor coverage check catches the gap before the
// runtime WARN ever needs to. Adding a new caller-gated handler means
// adding its AlertType here too.
var HandlerEmittedAlertTypes = []AlertType{
	AlertTypeDispatchLoopBreaker,
	AlertTypeIntentJudgeFailureStreak,
	AlertTypeLabelLifecycleFailureStreak,
	AlertTypeSelfReviewFailureStreak,
	AlertTypeFinishTripwireFailureStreak,
	AlertTypePushRetryExhaustedFailureStreak,
	AlertTypeEnvClassFailureStreak,
}

// CoverageGaps returns the subset of HandlerEmittedAlertTypes that cfg has
// no enabled rule for. An empty, non-nil cfg (Rules == nil/empty) reports
// every handler-emitted type as a gap, matching the "engine never delivers
// any of these alerts" reality of that config. A nil cfg is treated the
// same way (alerts subsystem entirely absent).
func CoverageGaps(cfg *AlertConfig) []AlertType {
	covered := make(map[AlertType]bool)
	if cfg != nil {
		for _, r := range cfg.Rules {
			if r.Enabled {
				covered[r.Type] = true
			}
		}
	}

	var gaps []AlertType
	for _, t := range HandlerEmittedAlertTypes {
		if !covered[t] {
			gaps = append(gaps, t)
		}
	}
	return gaps
}

// Alert represents an alert event
type Alert struct {
	ID          string            `json:"id"`
	Type        AlertType         `json:"type"`
	Severity    Severity          `json:"severity"`
	Title       string            `json:"title"`
	Message     string            `json:"message"`
	Source      string            `json:"source"`       // e.g., "task:TASK-123", "service:executor"
	ProjectPath string            `json:"project_path"` // Optional project context
	Metadata    map[string]string `json:"metadata"`     // Additional context
	CreatedAt   time.Time         `json:"created_at"`
	AckedAt     *time.Time        `json:"acked_at,omitempty"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
}

// AlertRule defines when to trigger an alert
type AlertRule struct {
	Name        string            `yaml:"name"`
	Type        AlertType         `yaml:"type"`
	Enabled     bool              `yaml:"enabled"`
	Condition   RuleCondition     `yaml:"condition"`
	Severity    Severity          `yaml:"severity"`
	Channels    []string          `yaml:"channels"`    // Channel names to send to
	Cooldown    time.Duration     `yaml:"cooldown"`    // Min time between alerts
	Labels      map[string]string `yaml:"labels"`      // Additional labels for filtering
	Description string            `yaml:"description"` // Human-readable description
}

// RuleCondition defines the alert trigger condition
type RuleCondition struct {
	// Task-related conditions
	ProgressUnchangedFor time.Duration `yaml:"progress_unchanged_for"` // For stuck tasks
	ConsecutiveFailures  int           `yaml:"consecutive_failures"`   // Number of failures

	// Cost-related conditions
	DailySpendThreshold float64 `yaml:"daily_spend_threshold"` // USD
	BudgetLimit         float64 `yaml:"budget_limit"`          // USD
	UsageSpikePercent   float64 `yaml:"usage_spike_percent"`   // e.g., 200 = 200% spike

	// Pattern-related conditions
	Pattern     string   `yaml:"pattern"`      // Regex pattern
	FilePattern string   `yaml:"file_pattern"` // Glob pattern for files
	Paths       []string `yaml:"paths"`        // Specific paths to watch

	// Autopilot health conditions (GH-728)
	FailedQueueThreshold int           `yaml:"failed_queue_threshold"` // Max failed issues
	APIErrorRatePerMin   float64       `yaml:"api_error_rate_per_min"` // Errors/min threshold
	PRStuckTimeout       time.Duration `yaml:"pr_stuck_timeout"`       // Max time in waiting_ci

	// Deadlock detection (GH-849)
	DeadlockTimeout time.Duration `yaml:"deadlock_timeout"` // Max time with no state transitions

	// Escalation conditions (GH-848)
	EscalationRetries int `yaml:"escalation_retries"` // Failures before escalation (default 3)

	// Lane-starvation detection (GH-4454)
	LaneStarvationPollCycles int `yaml:"lane_starvation_poll_cycles"` // Consecutive idle poll cycles before firing (default 3)
}

// AlertConfig holds the main alerting configuration
type AlertConfig struct {
	Enabled  bool            `yaml:"enabled"`
	Channels []ChannelConfig `yaml:"channels"`
	Rules    []AlertRule     `yaml:"rules"`
	Defaults AlertDefaults   `yaml:"defaults"`
}

// AlertDefaults contains default settings
type AlertDefaults struct {
	Cooldown           time.Duration `yaml:"cooldown"`
	DefaultSeverity    Severity      `yaml:"default_severity"`
	SuppressDuplicates bool          `yaml:"suppress_duplicates"`
	NotifyOnResolve    *bool         `yaml:"notify_on_resolve"`
}

func (d AlertDefaults) ResolveNotificationsEnabled() bool {
	return d.NotifyOnResolve == nil || *d.NotifyOnResolve
}

func (a *Alert) IsResolution() bool {
	return a != nil && a.ResolvedAt != nil
}

func (a *Alert) Duration() time.Duration {
	if !a.IsResolution() {
		return 0
	}
	return a.ResolvedAt.Sub(a.CreatedAt)
}

// ChannelConfig configures an alert channel
type ChannelConfig struct {
	Name       string     `yaml:"name"` // Unique identifier
	Type       string     `yaml:"type"` // "slack", "telegram", "email", "webhook", "pagerduty"
	Enabled    bool       `yaml:"enabled"`
	Severities []Severity `yaml:"severities"` // Which severities to receive

	// Channel-specific config
	Slack     *SlackChannelConfig     `yaml:"slack,omitempty"`
	Telegram  *TelegramChannelConfig  `yaml:"telegram,omitempty"`
	Email     *EmailChannelConfig     `yaml:"email,omitempty"`
	Webhook   *WebhookChannelConfig   `yaml:"webhook,omitempty"`
	PagerDuty *PagerDutyChannelConfig `yaml:"pagerduty,omitempty"`
}

// SlackChannelConfig for Slack alerts
type SlackChannelConfig struct {
	Channel string `yaml:"channel"` // #channel-name
}

// TelegramChannelConfig for Telegram alerts
type TelegramChannelConfig struct {
	ChatID          int64 `yaml:"chat_id"`
	MessageThreadID int64 `yaml:"message_thread_id"`
}

// EmailChannelConfig for email alerts
type EmailChannelConfig struct {
	To       []string `yaml:"to"`
	Subject  string   `yaml:"subject"` // Optional custom subject template
	SMTPHost string   `yaml:"smtp_host"`
	SMTPPort int      `yaml:"smtp_port"`
	From     string   `yaml:"from"`
	Username string   `yaml:"username"`
	Password string   `yaml:"password"`
}

// WebhookChannelConfig for webhook alerts
type WebhookChannelConfig struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"` // POST, PUT
	Headers map[string]string `yaml:"headers"`
	Secret  string            `yaml:"secret"` // For HMAC signing
}

// PagerDutyChannelConfig for PagerDuty alerts
type PagerDutyChannelConfig struct {
	RoutingKey string `yaml:"routing_key"` // Integration key
	ServiceID  string `yaml:"service_id"`
}

// DeliveryResult represents the result of sending an alert
type DeliveryResult struct {
	ChannelName string    `json:"channel_name"`
	Success     bool      `json:"success"`
	Error       error     `json:"error,omitempty"`
	SentAt      time.Time `json:"sent_at"`
	MessageID   string    `json:"message_id,omitempty"`
}

// AlertHistory stores alert history for tracking
type AlertHistory struct {
	AlertID     string    `json:"alert_id"`
	RuleName    string    `json:"rule_name"`
	Source      string    `json:"source"`
	FiredAt     time.Time `json:"fired_at"`
	DeliveredTo []string  `json:"delivered_to"`
}

// DefaultConfig returns sensible default alerting configuration
func DefaultConfig() *AlertConfig {
	return &AlertConfig{
		Enabled:  false,
		Channels: []ChannelConfig{},
		Rules:    defaultRules(),
		Defaults: AlertDefaults{
			Cooldown:           5 * time.Minute,
			DefaultSeverity:    SeverityWarning,
			SuppressDuplicates: true,
		},
	}
}

// defaultRules returns the default alert rules
func defaultRules() []AlertRule {
	return []AlertRule{
		{
			Name:    "task_stuck",
			Type:    AlertTypeTaskStuck,
			Enabled: true,
			Condition: RuleCondition{
				ProgressUnchangedFor: 10 * time.Minute,
			},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    15 * time.Minute,
			Description: "Alert when a task has no progress for 10 minutes",
		},
		{
			Name:        "task_failed",
			Type:        AlertTypeTaskFailed,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    0, // No cooldown for failures
			Description: "Alert when a task fails",
		},
		{
			Name:    "consecutive_failures",
			Type:    AlertTypeConsecutiveFails,
			Enabled: true,
			Condition: RuleCondition{
				ConsecutiveFailures: 3,
			},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when 3 or more consecutive tasks fail",
		},
		{
			Name:    "daily_spend",
			Type:    AlertTypeDailySpend,
			Enabled: false,
			Condition: RuleCondition{
				DailySpendThreshold: 50.0, // $50 default
			},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    1 * time.Hour,
			Description: "Alert when daily spend exceeds threshold",
		},
		{
			Name:    "budget_depleted",
			Type:    AlertTypeBudgetDepleted,
			Enabled: false,
			Condition: RuleCondition{
				BudgetLimit: 500.0, // $500 default monthly budget
			},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    4 * time.Hour,
			Description: "Alert when budget limit is exceeded",
		},
		// Autopilot health rules (GH-728)
		{
			Name:    "failed_queue_high",
			Type:    AlertTypeFailedQueueHigh,
			Enabled: true,
			Condition: RuleCondition{
				FailedQueueThreshold: 5,
			},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when failed issue queue exceeds threshold",
		},
		{
			Name:    "circuit_breaker_trip",
			Type:    AlertTypeCircuitBreakerTrip,
			Enabled: true,
			Condition: RuleCondition{
				ConsecutiveFailures: 1, // Any trip
			},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when autopilot circuit breaker trips",
		},
		{
			Name:    "api_error_rate_high",
			Type:    AlertTypeAPIErrorRateHigh,
			Enabled: true,
			Condition: RuleCondition{
				APIErrorRatePerMin: 10.0,
			},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    15 * time.Minute,
			Description: "Alert when API error rate exceeds 10/min",
		},
		{
			Name:    "pr_stuck_waiting_ci",
			Type:    AlertTypePRStuckWaitingCI,
			Enabled: true,
			Condition: RuleCondition{
				PRStuckTimeout: 15 * time.Minute,
			},
			Severity:    SeverityInfo,
			Channels:    []string{},
			Cooldown:    15 * time.Minute,
			Description: "Alert when a PR is stuck in waiting_ci for too long",
		},
		// Deadlock detection (GH-849)
		{
			Name:    "autopilot_deadlock",
			Type:    AlertTypeDeadlock,
			Enabled: true,
			Condition: RuleCondition{
				DeadlockTimeout: 1 * time.Hour,
			},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    1 * time.Hour,
			Description: "Alert when autopilot has no state transitions for 1 hour",
		},
		// Eval regression detection (GH-2065)
		{
			Name:    "eval_regression",
			Type:    AlertTypeEvalRegression,
			Enabled: true,
			Condition: RuleCondition{
				UsageSpikePercent: 10.0, // delta threshold; >2× this → critical
			},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when eval pass@1 scores regress compared to baseline",
		},
		// Escalation rule (GH-848)
		{
			Name:    "escalation",
			Type:    AlertTypeEscalation,
			Enabled: true,
			Condition: RuleCondition{
				EscalationRetries: 3,
			},
			Severity:    SeverityCritical,
			Channels:    []string{}, // Will route to PagerDuty channels by severity
			Cooldown:    1 * time.Hour,
			Description: "Escalate to PagerDuty after repeated failures for the same source",
		},
		// Release monitoring (GH-3952)
		{
			Name:        "release_missing",
			Type:        AlertTypeReleaseMissing,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when a merged PR did not produce its expected release tag",
		},
		// Lane-starvation detection (GH-4454)
		{
			Name:    "lane_starvation",
			Type:    AlertTypeLaneStarvation,
			Enabled: true,
			Condition: RuleCondition{
				LaneStarvationPollCycles: 3,
			},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when a project lane has open pilot-labeled issues but nothing queued/running for too many poll cycles",
		},
		// Dispatch loop breaker (GH-4469): caller (handleIssueGeneric) does its
		// own threshold counting via repickLoopBreakerThreshold and fires the
		// event exactly once per storm (at consecutive drop == 10), so no
		// Condition field is needed here — mirrors release_missing.
		{
			Name:        "dispatch_loop_breaker",
			Type:        AlertTypeDispatchLoopBreaker,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when a task is dispatched-and-rejected 10+ consecutive times without ever completing",
		},
		// GitHub side-effect audit (GH-4670): the executor's post-run audit
		// already did all the filtering (own-issue exclusion, window bounds)
		// before emitting, so no Condition field is needed — mirrors
		// release_missing and dispatch_loop_breaker.
		{
			Name:        "github_sideeffect",
			Type:        AlertTypeGithubSideEffect,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when a session mutates a GitHub issue other than the one it was dispatched to fix",
		},
		// Intent judge failure streak (GH-4669): caller (sdkPreFlightJudge.
		// JudgeIssue) does its own threshold counting and fires the event
		// exactly once per streak (at consecutive failures ==
		// judgeFailureStreakAlertThreshold), so no Condition field is needed
		// here — mirrors dispatch_loop_breaker and github_sideeffect. Severity
		// is Critical because this is the exact incident class (17-day, 100%
		// fail-open, silently hidden) the rule exists to page on.
		{
			Name:        "intent_judge_failure_streak",
			Type:        AlertTypeIntentJudgeFailureStreak,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when the pre-flight intent judge fails open N+ consecutive times without producing a verdict",
		},
		// Label-lifecycle dead-man tracker (TASK-441 L2, GH-4709): the
		// post-GH-4692 notifyTaskStartedSDK path records its own
		// threshold count via DeadManTracker.RecordFailure before
		// emitting, so no Condition field is needed here — mirrors
		// intent_judge_failure_streak. Severity is Critical: this is
		// the GH-4687 incident class (19-day silent death).
		{
			Name:        "label_lifecycle_failure_streak",
			Type:        AlertTypeLabelLifecycleFailureStreak,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when the label-lifecycle notifier fails N+ consecutive times without a success",
		},
		// Self-review dead-man tracker (TASK-441 L2, GH-4709): same shape
		// as label_lifecycle_failure_streak, guarding the GH-4702
		// incident class (months-long silent death).
		{
			Name:        "self_review_failure_streak",
			Type:        AlertTypeSelfReviewFailureStreak,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when post-run self-review fails N+ consecutive times without a success",
		},
		// Finish-tripwire dead-man trackers (TASK-441 L5, GH-4716): each of
		// the four post-terminal invariant checks (root-clean, label
		// lifecycle, decomposed-children-terminal, worktree-pruned) does its
		// own threshold counting via DeadManTracker.RecordFailure before
		// emitting, so no Condition field is needed here — mirrors
		// label_lifecycle_failure_streak/self_review_failure_streak. Severity
		// is Critical: these guard the exact three pitfall classes (phantom
		// root reimplementation, silently-dead label wiring, epic-discard)
		// that motivated the whole TASK-441 contract-hardening effort.
		{
			Name:        "finish_tripwire_failure_streak",
			Type:        AlertTypeFinishTripwireFailureStreak,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when a post-task invariant sweep check fails N+ consecutive times without a success",
		},
		// Push-retry-exhausted dead-man tracker (GH-4866): same shape as
		// label_lifecycle_failure_streak/self_review_failure_streak, guarding
		// a sustained run of exhausted git-push retries (stranded commits,
		// no PR) — the GH-4866 kill-drill's unwired-seam finding.
		{
			Name:        "push_retry_exhausted_failure_streak",
			Type:        AlertTypePushRetryExhaustedFailureStreak,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityCritical,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when the git-push retry loop exhausts all attempts N+ consecutive times without a success",
		},
		// Env-class failure streak (GH-5217): caller
		// (Dispatcher.beginWithGenerationRetry, the GH-5211 carve-out
		// branch) does its own threshold counting
		// (consecutiveEnvClassFailures) and fires the event once the
		// streak reaches envClassFailureStreakThreshold, so no Condition
		// field is needed here — mirrors dispatch_loop_breaker/
		// intent_judge_failure_streak. Severity is Warning (not Critical):
		// GH-5211 deliberately exempted env-class failures from stall
		// escalation by founder decision — this rule closes the resulting
		// silence gap (PR#5214 review) without treating a broken
		// credential as page-critical the way a silently dead subsystem
		// is.
		{
			Name:        "env_class_failure_streak",
			Type:        AlertTypeEnvClassFailureStreak,
			Enabled:     true,
			Condition:   RuleCondition{},
			Severity:    SeverityWarning,
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when a task accumulates N+ consecutive env-class (credential/environment) failures without any attempt reaching the model backend",
		},
	}
}
