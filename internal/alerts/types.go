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
)

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
	ChatID int64 `yaml:"chat_id"`
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
	}
}
