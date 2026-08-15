package alerts

import (
	"time"
)

// ConfigAdapter adapts config package types to alerts package types.
// Channel-specific configs (Slack, Telegram, etc.) are shared directly
// to eliminate duplicate type definitions.

// FromConfigAlerts converts config.AlertsConfig to alerts.AlertConfig.
func FromConfigAlerts(enabled bool, channels []ChannelConfigInput, rules []RuleConfigInput, defaults DefaultsConfigInput) *AlertConfig {
	alertCfg := &AlertConfig{
		Enabled:  enabled,
		Channels: make([]ChannelConfig, 0, len(channels)),
		Rules:    make([]AlertRule, 0, len(rules)),
		Defaults: AlertDefaults{
			Cooldown:           defaults.Cooldown,
			DefaultSeverity:    parseSeverity(defaults.DefaultSeverity),
			SuppressDuplicates: defaults.SuppressDuplicates,
			NotifyOnResolve:    defaults.NotifyOnResolve,
		},
	}

	for _, ch := range channels {
		alertCfg.Channels = append(alertCfg.Channels, convertChannel(ch))
	}

	for _, r := range rules {
		alertCfg.Rules = append(alertCfg.Rules, convertRule(r))
	}

	// GH-4866 (TASK-441 kill-drill A): union in any default rule Type absent
	// from the caller's list. The daemon path's config-sourced rules
	// (internal/config/config.go's defaultAlertRules, or a persisted
	// alerts.rules: list that predates a Type added since) can omit Types
	// added after that list was authored — dead-man streaks
	// (label_lifecycle/self_review/finish_tripwire), the intent-judge
	// streak, and the GitHub side-effect audit all exist only in
	// defaultRules(), whose sole caller used to be the one-shot `--alerts`
	// CLI (DefaultConfig()). There is no migration path for a persisted
	// config, so config.Load's "replace defaults wholesale with any
	// persisted list" behavior would otherwise silently drop these Types
	// forever. Every default AlertType is therefore load-bearing here: this
	// is the single point both production callers (cmd/pilot/adapters.go's
	// getAlertsConfig and internal/pilot/pilot.go's initAlerts) go through,
	// so a rule the caller's list already covers (by Type) is left
	// untouched — only a genuinely missing Type gets the default appended.
	seenTypes := make(map[AlertType]bool, len(alertCfg.Rules))
	for _, r := range alertCfg.Rules {
		seenTypes[r.Type] = true
	}
	for _, defaultRule := range defaultRules() {
		if !seenTypes[defaultRule.Type] {
			alertCfg.Rules = append(alertCfg.Rules, defaultRule)
		}
	}

	return alertCfg
}

// ChannelConfigInput represents channel config from config package.
// Channel-specific configs use the same types as alerts package directly.
type ChannelConfigInput struct {
	Name       string
	Type       string
	Enabled    bool
	Severities []string
	// Channel-specific configs - same types used in both packages
	Slack     *SlackChannelConfig
	Telegram  *TelegramChannelConfig
	Email     *EmailChannelConfig
	Webhook   *WebhookChannelConfig
	PagerDuty *PagerDutyChannelConfig
}

// RuleConfigInput represents rule config from config package
type RuleConfigInput struct {
	Name        string
	Type        string
	Enabled     bool
	Condition   ConditionConfigInput
	Severity    string
	Channels    []string
	Cooldown    time.Duration
	Description string
}

// ConditionConfigInput represents condition config from config package
type ConditionConfigInput struct {
	ProgressUnchangedFor time.Duration
	ConsecutiveFailures  int
	DailySpendThreshold  float64
	BudgetLimit          float64
	UsageSpikePercent    float64
	Pattern              string
	FilePattern          string
	Paths                []string
}

// DefaultsConfigInput represents defaults config from config package
type DefaultsConfigInput struct {
	Cooldown           time.Duration
	DefaultSeverity    string
	SuppressDuplicates bool
	NotifyOnResolve    *bool
}

func convertChannel(in ChannelConfigInput) ChannelConfig {
	ch := ChannelConfig{
		Name:       in.Name,
		Type:       in.Type,
		Enabled:    in.Enabled,
		Severities: make([]Severity, 0, len(in.Severities)),
		// Direct assignment - no conversion needed (same types)
		Slack:     in.Slack,
		Telegram:  in.Telegram,
		Email:     in.Email,
		Webhook:   in.Webhook,
		PagerDuty: in.PagerDuty,
	}

	for _, s := range in.Severities {
		ch.Severities = append(ch.Severities, parseSeverity(s))
	}

	return ch
}

func convertRule(in RuleConfigInput) AlertRule {
	return AlertRule{
		Name:        in.Name,
		Type:        parseAlertType(in.Type),
		Enabled:     in.Enabled,
		Severity:    parseSeverity(in.Severity),
		Channels:    in.Channels,
		Cooldown:    in.Cooldown,
		Description: in.Description,
		Condition: RuleCondition{
			ProgressUnchangedFor: in.Condition.ProgressUnchangedFor,
			ConsecutiveFailures:  in.Condition.ConsecutiveFailures,
			DailySpendThreshold:  in.Condition.DailySpendThreshold,
			BudgetLimit:          in.Condition.BudgetLimit,
			UsageSpikePercent:    in.Condition.UsageSpikePercent,
			Pattern:              in.Condition.Pattern,
			FilePattern:          in.Condition.FilePattern,
			Paths:                in.Condition.Paths,
		},
	}
}

func parseSeverity(s string) Severity {
	switch s {
	case "critical":
		return SeverityCritical
	case "warning":
		return SeverityWarning
	case "info":
		return SeverityInfo
	default:
		return SeverityWarning
	}
}

func parseAlertType(t string) AlertType {
	switch t {
	case "task_stuck":
		return AlertTypeTaskStuck
	case "task_failed":
		return AlertTypeTaskFailed
	case "consecutive_failures":
		return AlertTypeConsecutiveFails
	case "service_unhealthy":
		return AlertTypeServiceUnhealthy
	case "daily_spend_exceeded":
		return AlertTypeDailySpend
	case "budget_depleted":
		return AlertTypeBudgetDepleted
	case "usage_spike":
		return AlertTypeUsageSpike
	case "unauthorized_access":
		return AlertTypeUnauthorizedAccess
	case "sensitive_file_modified":
		return AlertTypeSensitiveFile
	case "unusual_pattern":
		return AlertTypeUnusualPattern
	case "eval_regression":
		return AlertTypeEvalRegression
	default:
		return AlertType(t)
	}
}
