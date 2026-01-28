package alerts

import (
	"testing"
	"time"
)

func TestParseSeverity(t *testing.T) {
	tests := []struct {
		input    string
		expected Severity
	}{
		{"critical", SeverityCritical},
		{"warning", SeverityWarning},
		{"info", SeverityInfo},
		{"unknown", SeverityWarning}, // Default
		{"", SeverityWarning},        // Default
		{"CRITICAL", SeverityWarning}, // Case sensitive, defaults
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseSeverity(tt.input)
			if result != tt.expected {
				t.Errorf("parseSeverity(%q) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseAlertType(t *testing.T) {
	tests := []struct {
		input    string
		expected AlertType
	}{
		{"task_stuck", AlertTypeTaskStuck},
		{"task_failed", AlertTypeTaskFailed},
		{"consecutive_failures", AlertTypeConsecutiveFails},
		{"service_unhealthy", AlertTypeServiceUnhealthy},
		{"daily_spend_exceeded", AlertTypeDailySpend},
		{"budget_depleted", AlertTypeBudgetDepleted},
		{"usage_spike", AlertTypeUsageSpike},
		{"unauthorized_access", AlertTypeUnauthorizedAccess},
		{"sensitive_file_modified", AlertTypeSensitiveFile},
		{"unusual_pattern", AlertTypeUnusualPattern},
		{"custom_type", AlertType("custom_type")}, // Passthrough for unknown
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseAlertType(tt.input)
			if result != tt.expected {
				t.Errorf("parseAlertType(%q) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestConvertChannel(t *testing.T) {
	tests := []struct {
		name   string
		input  ChannelConfigInput
		verify func(t *testing.T, ch ChannelConfig)
	}{
		{
			name: "basic channel",
			input: ChannelConfigInput{
				Name:       "test-channel",
				Type:       "webhook",
				Enabled:    true,
				Severities: []string{"critical", "warning"},
			},
			verify: func(t *testing.T, ch ChannelConfig) {
				if ch.Name != "test-channel" {
					t.Errorf("expected name 'test-channel', got '%s'", ch.Name)
				}
				if ch.Type != "webhook" {
					t.Errorf("expected type 'webhook', got '%s'", ch.Type)
				}
				if !ch.Enabled {
					t.Error("expected enabled to be true")
				}
				if len(ch.Severities) != 2 {
					t.Errorf("expected 2 severities, got %d", len(ch.Severities))
				}
				if ch.Severities[0] != SeverityCritical {
					t.Errorf("expected first severity 'critical', got '%s'", ch.Severities[0])
				}
			},
		},
		{
			name: "slack channel",
			input: ChannelConfigInput{
				Name:    "slack-alerts",
				Type:    "slack",
				Enabled: true,
				Slack: &SlackConfigInput{
					Channel: "#ops-alerts",
				},
			},
			verify: func(t *testing.T, ch ChannelConfig) {
				if ch.Slack == nil {
					t.Fatal("expected Slack config to be set")
				}
				if ch.Slack.Channel != "#ops-alerts" {
					t.Errorf("expected channel '#ops-alerts', got '%s'", ch.Slack.Channel)
				}
			},
		},
		{
			name: "telegram channel",
			input: ChannelConfigInput{
				Name:    "telegram-alerts",
				Type:    "telegram",
				Enabled: true,
				Telegram: &TelegramConfigInput{
					ChatID: 123456789,
				},
			},
			verify: func(t *testing.T, ch ChannelConfig) {
				if ch.Telegram == nil {
					t.Fatal("expected Telegram config to be set")
				}
				if ch.Telegram.ChatID != 123456789 {
					t.Errorf("expected ChatID 123456789, got %d", ch.Telegram.ChatID)
				}
			},
		},
		{
			name: "email channel",
			input: ChannelConfigInput{
				Name:    "email-alerts",
				Type:    "email",
				Enabled: true,
				Email: &EmailConfigInput{
					To:      []string{"admin@example.com", "ops@example.com"},
					Subject: "[ALERT] {{title}}",
				},
			},
			verify: func(t *testing.T, ch ChannelConfig) {
				if ch.Email == nil {
					t.Fatal("expected Email config to be set")
				}
				if len(ch.Email.To) != 2 {
					t.Errorf("expected 2 recipients, got %d", len(ch.Email.To))
				}
				if ch.Email.Subject != "[ALERT] {{title}}" {
					t.Errorf("expected subject '[ALERT] {{title}}', got '%s'", ch.Email.Subject)
				}
			},
		},
		{
			name: "webhook channel",
			input: ChannelConfigInput{
				Name:    "webhook-alerts",
				Type:    "webhook",
				Enabled: true,
				Webhook: &WebhookConfigInput{
					URL:    "https://hooks.example.com/alert",
					Method: "POST",
					Headers: map[string]string{
						"Authorization": "Bearer token123",
					},
					Secret: "webhook-secret",
				},
			},
			verify: func(t *testing.T, ch ChannelConfig) {
				if ch.Webhook == nil {
					t.Fatal("expected Webhook config to be set")
				}
				if ch.Webhook.URL != "https://hooks.example.com/alert" {
					t.Errorf("expected URL 'https://hooks.example.com/alert', got '%s'", ch.Webhook.URL)
				}
				if ch.Webhook.Method != "POST" {
					t.Errorf("expected method 'POST', got '%s'", ch.Webhook.Method)
				}
				if ch.Webhook.Headers["Authorization"] != "Bearer token123" {
					t.Error("expected Authorization header")
				}
				if ch.Webhook.Secret != "webhook-secret" {
					t.Errorf("expected secret 'webhook-secret', got '%s'", ch.Webhook.Secret)
				}
			},
		},
		{
			name: "pagerduty channel",
			input: ChannelConfigInput{
				Name:    "pagerduty-alerts",
				Type:    "pagerduty",
				Enabled: true,
				PagerDuty: &PagerDutyConfigInput{
					RoutingKey: "routing-key-abc",
					ServiceID:  "service-xyz",
				},
			},
			verify: func(t *testing.T, ch ChannelConfig) {
				if ch.PagerDuty == nil {
					t.Fatal("expected PagerDuty config to be set")
				}
				if ch.PagerDuty.RoutingKey != "routing-key-abc" {
					t.Errorf("expected routing key 'routing-key-abc', got '%s'", ch.PagerDuty.RoutingKey)
				}
				if ch.PagerDuty.ServiceID != "service-xyz" {
					t.Errorf("expected service ID 'service-xyz', got '%s'", ch.PagerDuty.ServiceID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertChannel(tt.input)
			tt.verify(t, result)
		})
	}
}

func TestConvertRule(t *testing.T) {
	tests := []struct {
		name   string
		input  RuleConfigInput
		verify func(t *testing.T, rule AlertRule)
	}{
		{
			name: "basic rule",
			input: RuleConfigInput{
				Name:        "my-rule",
				Type:        "task_failed",
				Enabled:     true,
				Severity:    "critical",
				Channels:    []string{"slack", "telegram"},
				Cooldown:    10 * time.Minute,
				Description: "Test rule",
			},
			verify: func(t *testing.T, rule AlertRule) {
				if rule.Name != "my-rule" {
					t.Errorf("expected name 'my-rule', got '%s'", rule.Name)
				}
				if rule.Type != AlertTypeTaskFailed {
					t.Errorf("expected type TaskFailed, got %s", rule.Type)
				}
				if !rule.Enabled {
					t.Error("expected enabled to be true")
				}
				if rule.Severity != SeverityCritical {
					t.Errorf("expected severity Critical, got %s", rule.Severity)
				}
				if len(rule.Channels) != 2 {
					t.Errorf("expected 2 channels, got %d", len(rule.Channels))
				}
				if rule.Cooldown != 10*time.Minute {
					t.Errorf("expected cooldown 10m, got %v", rule.Cooldown)
				}
			},
		},
		{
			name: "rule with condition",
			input: RuleConfigInput{
				Name:    "stuck-task-rule",
				Type:    "task_stuck",
				Enabled: true,
				Condition: ConditionConfigInput{
					ProgressUnchangedFor: 15 * time.Minute,
				},
				Severity: "warning",
			},
			verify: func(t *testing.T, rule AlertRule) {
				if rule.Condition.ProgressUnchangedFor != 15*time.Minute {
					t.Errorf("expected ProgressUnchangedFor 15m, got %v", rule.Condition.ProgressUnchangedFor)
				}
			},
		},
		{
			name: "rule with all condition fields",
			input: RuleConfigInput{
				Name:    "complex-rule",
				Type:    "task_failed",
				Enabled: true,
				Condition: ConditionConfigInput{
					ProgressUnchangedFor: 20 * time.Minute,
					ConsecutiveFailures:  5,
					DailySpendThreshold:  100.0,
					BudgetLimit:          500.0,
					UsageSpikePercent:    300.0,
					Pattern:              "error.*",
					FilePattern:          "*.secret",
					Paths:                []string{"/etc/passwd"},
				},
				Severity: "critical",
			},
			verify: func(t *testing.T, rule AlertRule) {
				if rule.Condition.ConsecutiveFailures != 5 {
					t.Errorf("expected ConsecutiveFailures 5, got %d", rule.Condition.ConsecutiveFailures)
				}
				if rule.Condition.DailySpendThreshold != 100.0 {
					t.Errorf("expected DailySpendThreshold 100.0, got %f", rule.Condition.DailySpendThreshold)
				}
				if rule.Condition.BudgetLimit != 500.0 {
					t.Errorf("expected BudgetLimit 500.0, got %f", rule.Condition.BudgetLimit)
				}
				if rule.Condition.UsageSpikePercent != 300.0 {
					t.Errorf("expected UsageSpikePercent 300.0, got %f", rule.Condition.UsageSpikePercent)
				}
				if rule.Condition.Pattern != "error.*" {
					t.Errorf("expected Pattern 'error.*', got '%s'", rule.Condition.Pattern)
				}
				if rule.Condition.FilePattern != "*.secret" {
					t.Errorf("expected FilePattern '*.secret', got '%s'", rule.Condition.FilePattern)
				}
				if len(rule.Condition.Paths) != 1 {
					t.Errorf("expected 1 path, got %d", len(rule.Condition.Paths))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertRule(tt.input)
			tt.verify(t, result)
		})
	}
}

func TestFromConfigAlerts(t *testing.T) {
	channels := []ChannelConfigInput{
		{
			Name:       "slack-channel",
			Type:       "slack",
			Enabled:    true,
			Severities: []string{"critical"},
			Slack:      &SlackConfigInput{Channel: "#alerts"},
		},
		{
			Name:    "webhook-channel",
			Type:    "webhook",
			Enabled: true,
			Webhook: &WebhookConfigInput{URL: "https://example.com"},
		},
	}

	rules := []RuleConfigInput{
		{
			Name:     "task-failed",
			Type:     "task_failed",
			Enabled:  true,
			Severity: "warning",
			Channels: []string{"slack-channel"},
			Cooldown: 5 * time.Minute,
		},
		{
			Name:    "consecutive",
			Type:    "consecutive_failures",
			Enabled: true,
			Condition: ConditionConfigInput{
				ConsecutiveFailures: 3,
			},
			Severity: "critical",
		},
	}

	defaults := DefaultsConfigInput{
		Cooldown:           10 * time.Minute,
		DefaultSeverity:    "warning",
		SuppressDuplicates: true,
	}

	config := FromConfigAlerts(true, channels, rules, defaults)

	// Verify enabled
	if !config.Enabled {
		t.Error("expected config to be enabled")
	}

	// Verify channels
	if len(config.Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(config.Channels))
	}

	// Find and verify slack channel
	var slackCh *ChannelConfig
	for i := range config.Channels {
		if config.Channels[i].Name == "slack-channel" {
			slackCh = &config.Channels[i]
			break
		}
	}
	if slackCh == nil {
		t.Fatal("slack-channel not found")
	}
	if slackCh.Slack == nil || slackCh.Slack.Channel != "#alerts" {
		t.Error("slack channel config incorrect")
	}

	// Verify rules
	if len(config.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(config.Rules))
	}

	// Find and verify task-failed rule
	var taskFailedRule *AlertRule
	for i := range config.Rules {
		if config.Rules[i].Name == "task-failed" {
			taskFailedRule = &config.Rules[i]
			break
		}
	}
	if taskFailedRule == nil {
		t.Fatal("task-failed rule not found")
	}
	if taskFailedRule.Type != AlertTypeTaskFailed {
		t.Errorf("expected type TaskFailed, got %s", taskFailedRule.Type)
	}

	// Verify defaults
	if config.Defaults.Cooldown != 10*time.Minute {
		t.Errorf("expected default cooldown 10m, got %v", config.Defaults.Cooldown)
	}
	if config.Defaults.DefaultSeverity != SeverityWarning {
		t.Errorf("expected default severity Warning, got %s", config.Defaults.DefaultSeverity)
	}
	if !config.Defaults.SuppressDuplicates {
		t.Error("expected SuppressDuplicates to be true")
	}
}

func TestFromConfigAlerts_Empty(t *testing.T) {
	config := FromConfigAlerts(false, nil, nil, DefaultsConfigInput{})

	if config.Enabled {
		t.Error("expected config to be disabled")
	}
	if len(config.Channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(config.Channels))
	}
	if len(config.Rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(config.Rules))
	}
}

func TestChannelConfigInput_NilSubConfigs(t *testing.T) {
	// Test that nil sub-configs don't cause issues
	input := ChannelConfigInput{
		Name:      "test",
		Type:      "webhook",
		Enabled:   true,
		Slack:     nil,
		Telegram:  nil,
		Email:     nil,
		Webhook:   nil,
		PagerDuty: nil,
	}

	result := convertChannel(input)

	if result.Slack != nil {
		t.Error("expected Slack to be nil")
	}
	if result.Telegram != nil {
		t.Error("expected Telegram to be nil")
	}
	if result.Email != nil {
		t.Error("expected Email to be nil")
	}
	if result.Webhook != nil {
		t.Error("expected Webhook to be nil")
	}
	if result.PagerDuty != nil {
		t.Error("expected PagerDuty to be nil")
	}
}

func TestConditionConfigInput_ZeroValues(t *testing.T) {
	input := RuleConfigInput{
		Name:      "zero-condition",
		Type:      "task_failed",
		Enabled:   true,
		Condition: ConditionConfigInput{}, // All zero values
		Severity:  "warning",
	}

	result := convertRule(input)

	if result.Condition.ProgressUnchangedFor != 0 {
		t.Errorf("expected ProgressUnchangedFor 0, got %v", result.Condition.ProgressUnchangedFor)
	}
	if result.Condition.ConsecutiveFailures != 0 {
		t.Errorf("expected ConsecutiveFailures 0, got %d", result.Condition.ConsecutiveFailures)
	}
	if result.Condition.DailySpendThreshold != 0 {
		t.Errorf("expected DailySpendThreshold 0, got %f", result.Condition.DailySpendThreshold)
	}
}
