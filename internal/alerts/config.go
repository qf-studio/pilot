package alerts

import (
	"time"
)

// ConfigAdapter adapts config package types to alerts package types
// This avoids circular imports between config and alerts packages

// FromConfigAlerts converts config.AlertsConfig to alerts.AlertConfig
// cfg is expected to be *config.AlertsConfig
func FromConfigAlerts(enabled bool, channels []ChannelConfigInput, rules []RuleConfigInput, defaults DefaultsConfigInput) *AlertConfig {
	alertCfg := &AlertConfig{
		Enabled:  enabled,
		Channels: make([]ChannelConfig, 0, len(channels)),
		Rules:    make([]AlertRule, 0, len(rules)),
		Defaults: AlertDefaults{
			Cooldown:           defaults.Cooldown,
			DefaultSeverity:    parseSeverity(defaults.DefaultSeverity),
			SuppressDuplicates: defaults.SuppressDuplicates,
		},
	}

	for _, ch := range channels {
		alertCfg.Channels = append(alertCfg.Channels, convertChannel(ch))
	}

	for _, r := range rules {
		alertCfg.Rules = append(alertCfg.Rules, convertRule(r))
	}

	return alertCfg
}

// ChannelConfigInput represents channel config from config package
type ChannelConfigInput struct {
	Name       string
	Type       string
	Enabled    bool
	Severities []string
	Slack      *SlackConfigInput
	Telegram   *TelegramConfigInput
	Email      *EmailConfigInput
	Webhook    *WebhookConfigInput
	PagerDuty  *PagerDutyConfigInput
}

// SlackConfigInput represents Slack config from config package
type SlackConfigInput struct {
	Channel string
}

// TelegramConfigInput represents Telegram config from config package
type TelegramConfigInput struct {
	ChatID int64
}

// EmailConfigInput represents Email config from config package
type EmailConfigInput struct {
	To      []string
	Subject string
}

// WebhookConfigInput represents Webhook config from config package
type WebhookConfigInput struct {
	URL     string
	Method  string
	Headers map[string]string
	Secret  string
}

// PagerDutyConfigInput represents PagerDuty config from config package
type PagerDutyConfigInput struct {
	RoutingKey string
	ServiceID  string
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
}

func convertChannel(in ChannelConfigInput) ChannelConfig {
	ch := ChannelConfig{
		Name:       in.Name,
		Type:       in.Type,
		Enabled:    in.Enabled,
		Severities: make([]Severity, 0, len(in.Severities)),
	}

	for _, s := range in.Severities {
		ch.Severities = append(ch.Severities, parseSeverity(s))
	}

	if in.Slack != nil {
		ch.Slack = &SlackChannelConfig{
			Channel: in.Slack.Channel,
		}
	}

	if in.Telegram != nil {
		ch.Telegram = &TelegramChannelConfig{
			ChatID: in.Telegram.ChatID,
		}
	}

	if in.Email != nil {
		ch.Email = &EmailChannelConfig{
			To:      in.Email.To,
			Subject: in.Email.Subject,
		}
	}

	if in.Webhook != nil {
		ch.Webhook = &WebhookChannelConfig{
			URL:     in.Webhook.URL,
			Method:  in.Webhook.Method,
			Headers: in.Webhook.Headers,
			Secret:  in.Webhook.Secret,
		}
	}

	if in.PagerDuty != nil {
		ch.PagerDuty = &PagerDutyChannelConfig{
			RoutingKey: in.PagerDuty.RoutingKey,
			ServiceID:  in.PagerDuty.ServiceID,
		}
	}

	return ch
}

func convertRule(in RuleConfigInput) AlertRule {
	return AlertRule{
		Name:     in.Name,
		Type:     parseAlertType(in.Type),
		Enabled:  in.Enabled,
		Severity: parseSeverity(in.Severity),
		Channels: in.Channels,
		Cooldown: in.Cooldown,
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
	default:
		return AlertType(t)
	}
}
