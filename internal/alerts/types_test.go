package alerts

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	// Check defaults
	if cfg.Enabled {
		t.Error("expected Enabled to be false by default")
	}

	if len(cfg.Channels) != 0 {
		t.Errorf("expected empty channels, got %d", len(cfg.Channels))
	}

	if len(cfg.Rules) == 0 {
		t.Error("expected default rules to be set")
	}

	// Check default rules
	if cfg.Defaults.Cooldown != 5*time.Minute {
		t.Errorf("expected default cooldown 5m, got %v", cfg.Defaults.Cooldown)
	}

	if cfg.Defaults.DefaultSeverity != SeverityWarning {
		t.Errorf("expected default severity Warning, got %v", cfg.Defaults.DefaultSeverity)
	}

	if !cfg.Defaults.SuppressDuplicates {
		t.Error("expected SuppressDuplicates to be true by default")
	}
}

func TestDefaultRules(t *testing.T) {
	rules := defaultRules()

	expectedRules := map[AlertType]struct {
		name    string
		enabled bool
	}{
		AlertTypeTaskStuck:        {"task_stuck", true},
		AlertTypeTaskFailed:       {"task_failed", true},
		AlertTypeConsecutiveFails: {"consecutive_failures", true},
		AlertTypeDailySpend:       {"daily_spend", false},
		AlertTypeBudgetDepleted:   {"budget_depleted", false},
		// Autopilot health rules (GH-728)
		AlertTypeFailedQueueHigh:    {"failed_queue_high", true},
		AlertTypeCircuitBreakerTrip: {"circuit_breaker_trip", true},
		AlertTypeAPIErrorRateHigh:   {"api_error_rate_high", true},
		AlertTypePRStuckWaitingCI:   {"pr_stuck_waiting_ci", true},
	}

	if len(rules) != len(expectedRules) {
		t.Errorf("expected %d default rules, got %d", len(expectedRules), len(rules))
	}

	for _, rule := range rules {
		expected, ok := expectedRules[rule.Type]
		if !ok {
			t.Errorf("unexpected rule type: %s", rule.Type)
			continue
		}

		if rule.Name != expected.name {
			t.Errorf("rule %s: expected name '%s', got '%s'", rule.Type, expected.name, rule.Name)
		}

		if rule.Enabled != expected.enabled {
			t.Errorf("rule %s: expected enabled=%v, got %v", rule.Type, expected.enabled, rule.Enabled)
		}
	}
}

func TestDefaultRules_TaskStuck(t *testing.T) {
	rules := defaultRules()

	var stuckRule *AlertRule
	for i := range rules {
		if rules[i].Type == AlertTypeTaskStuck {
			stuckRule = &rules[i]
			break
		}
	}

	if stuckRule == nil {
		t.Fatal("task_stuck rule not found")
	}

	if stuckRule.Condition.ProgressUnchangedFor != 10*time.Minute {
		t.Errorf("expected ProgressUnchangedFor 10m, got %v", stuckRule.Condition.ProgressUnchangedFor)
	}

	if stuckRule.Severity != SeverityWarning {
		t.Errorf("expected severity Warning, got %s", stuckRule.Severity)
	}

	if stuckRule.Cooldown != 15*time.Minute {
		t.Errorf("expected cooldown 15m, got %v", stuckRule.Cooldown)
	}
}

func TestDefaultRules_ConsecutiveFailures(t *testing.T) {
	rules := defaultRules()

	var failRule *AlertRule
	for i := range rules {
		if rules[i].Type == AlertTypeConsecutiveFails {
			failRule = &rules[i]
			break
		}
	}

	if failRule == nil {
		t.Fatal("consecutive_failures rule not found")
	}

	if failRule.Condition.ConsecutiveFailures != 3 {
		t.Errorf("expected ConsecutiveFailures 3, got %d", failRule.Condition.ConsecutiveFailures)
	}

	if failRule.Severity != SeverityCritical {
		t.Errorf("expected severity Critical, got %s", failRule.Severity)
	}

	if failRule.Cooldown != 30*time.Minute {
		t.Errorf("expected cooldown 30m, got %v", failRule.Cooldown)
	}
}

func TestDefaultRules_DailySpend(t *testing.T) {
	rules := defaultRules()

	var spendRule *AlertRule
	for i := range rules {
		if rules[i].Type == AlertTypeDailySpend {
			spendRule = &rules[i]
			break
		}
	}

	if spendRule == nil {
		t.Fatal("daily_spend rule not found")
	}

	if spendRule.Condition.DailySpendThreshold != 50.0 {
		t.Errorf("expected DailySpendThreshold 50.0, got %f", spendRule.Condition.DailySpendThreshold)
	}

	if spendRule.Enabled {
		t.Error("expected daily_spend rule to be disabled by default")
	}
}

func TestDefaultRules_BudgetDepleted(t *testing.T) {
	rules := defaultRules()

	var budgetRule *AlertRule
	for i := range rules {
		if rules[i].Type == AlertTypeBudgetDepleted {
			budgetRule = &rules[i]
			break
		}
	}

	if budgetRule == nil {
		t.Fatal("budget_depleted rule not found")
	}

	if budgetRule.Condition.BudgetLimit != 500.0 {
		t.Errorf("expected BudgetLimit 500.0, got %f", budgetRule.Condition.BudgetLimit)
	}

	if budgetRule.Enabled {
		t.Error("expected budget_depleted rule to be disabled by default")
	}
}

func TestSeverityConstants(t *testing.T) {
	tests := []struct {
		severity Severity
		expected string
	}{
		{SeverityInfo, "info"},
		{SeverityWarning, "warning"},
		{SeverityCritical, "critical"},
	}

	for _, tt := range tests {
		t.Run(string(tt.severity), func(t *testing.T) {
			if string(tt.severity) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, tt.severity)
			}
		})
	}
}

func TestAlertTypeConstants(t *testing.T) {
	tests := []struct {
		alertType AlertType
		expected  string
	}{
		{AlertTypeTaskStuck, "task_stuck"},
		{AlertTypeTaskFailed, "task_failed"},
		{AlertTypeConsecutiveFails, "consecutive_failures"},
		{AlertTypeServiceUnhealthy, "service_unhealthy"},
		{AlertTypeDailySpend, "daily_spend_exceeded"},
		{AlertTypeBudgetDepleted, "budget_depleted"},
		{AlertTypeUsageSpike, "usage_spike"},
		{AlertTypeUnauthorizedAccess, "unauthorized_access"},
		{AlertTypeSensitiveFile, "sensitive_file_modified"},
		{AlertTypeUnusualPattern, "unusual_pattern"},
	}

	for _, tt := range tests {
		t.Run(string(tt.alertType), func(t *testing.T) {
			if string(tt.alertType) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, tt.alertType)
			}
		})
	}
}

func TestAlert_Struct(t *testing.T) {
	now := time.Now()
	acked := now.Add(1 * time.Minute)
	resolved := now.Add(5 * time.Minute)

	alert := Alert{
		ID:          "test-id",
		Type:        AlertTypeTaskFailed,
		Severity:    SeverityCritical,
		Title:       "Test Title",
		Message:     "Test Message",
		Source:      "task:TASK-123",
		ProjectPath: "/my/project",
		Metadata: map[string]string{
			"key": "value",
		},
		CreatedAt:  now,
		AckedAt:    &acked,
		ResolvedAt: &resolved,
	}

	if alert.ID != "test-id" {
		t.Errorf("expected ID 'test-id', got '%s'", alert.ID)
	}
	if alert.Type != AlertTypeTaskFailed {
		t.Errorf("expected Type TaskFailed, got %s", alert.Type)
	}
	if alert.Severity != SeverityCritical {
		t.Errorf("expected Severity Critical, got %s", alert.Severity)
	}
	if alert.Metadata["key"] != "value" {
		t.Error("expected metadata to contain key-value pair")
	}
	if alert.AckedAt == nil || !alert.AckedAt.Equal(acked) {
		t.Error("expected AckedAt to be set correctly")
	}
	if alert.ResolvedAt == nil || !alert.ResolvedAt.Equal(resolved) {
		t.Error("expected ResolvedAt to be set correctly")
	}
}

func TestDeliveryResult_Struct(t *testing.T) {
	now := time.Now()
	result := DeliveryResult{
		ChannelName: "slack-channel",
		Success:     true,
		Error:       nil,
		SentAt:      now,
		MessageID:   "msg-123",
	}

	if result.ChannelName != "slack-channel" {
		t.Errorf("expected ChannelName 'slack-channel', got '%s'", result.ChannelName)
	}
	if !result.Success {
		t.Error("expected Success to be true")
	}
	if result.Error != nil {
		t.Error("expected Error to be nil")
	}
	if result.MessageID != "msg-123" {
		t.Errorf("expected MessageID 'msg-123', got '%s'", result.MessageID)
	}
}

func TestAlertHistory_Struct(t *testing.T) {
	now := time.Now()
	history := AlertHistory{
		AlertID:     "alert-123",
		RuleName:    "task_failed",
		Source:      "task:TASK-456",
		FiredAt:     now,
		DeliveredTo: []string{"slack", "telegram"},
	}

	if history.AlertID != "alert-123" {
		t.Errorf("expected AlertID 'alert-123', got '%s'", history.AlertID)
	}
	if history.RuleName != "task_failed" {
		t.Errorf("expected RuleName 'task_failed', got '%s'", history.RuleName)
	}
	if len(history.DeliveredTo) != 2 {
		t.Errorf("expected 2 delivery targets, got %d", len(history.DeliveredTo))
	}
}

func TestChannelConfig_AllTypes(t *testing.T) {
	tests := []struct {
		name       string
		config     ChannelConfig
		expectType string
	}{
		{
			name: "slack channel",
			config: ChannelConfig{
				Name:    "my-slack",
				Type:    "slack",
				Enabled: true,
				Slack: &SlackChannelConfig{
					Channel: "#alerts",
				},
			},
			expectType: "slack",
		},
		{
			name: "telegram channel",
			config: ChannelConfig{
				Name:    "my-telegram",
				Type:    "telegram",
				Enabled: true,
				Telegram: &TelegramChannelConfig{
					ChatID: 123456789,
				},
			},
			expectType: "telegram",
		},
		{
			name: "email channel",
			config: ChannelConfig{
				Name:    "my-email",
				Type:    "email",
				Enabled: true,
				Email: &EmailChannelConfig{
					To:      []string{"test@example.com"},
					Subject: "Alert: {{title}}",
				},
			},
			expectType: "email",
		},
		{
			name: "webhook channel",
			config: ChannelConfig{
				Name:    "my-webhook",
				Type:    "webhook",
				Enabled: true,
				Webhook: &WebhookChannelConfig{
					URL:    "https://example.com/webhook",
					Method: "POST",
					Headers: map[string]string{
						"Authorization": "Bearer token",
					},
					Secret: "my-secret",
				},
			},
			expectType: "webhook",
		},
		{
			name: "pagerduty channel",
			config: ChannelConfig{
				Name:    "my-pagerduty",
				Type:    "pagerduty",
				Enabled: true,
				PagerDuty: &PagerDutyChannelConfig{
					RoutingKey: "routing-key-123",
					ServiceID:  "service-456",
				},
			},
			expectType: "pagerduty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Type != tt.expectType {
				t.Errorf("expected type %s, got %s", tt.expectType, tt.config.Type)
			}
			if !tt.config.Enabled {
				t.Error("expected channel to be enabled")
			}
		})
	}
}

func TestRuleCondition_AllFields(t *testing.T) {
	condition := RuleCondition{
		ProgressUnchangedFor: 15 * time.Minute,
		ConsecutiveFailures:  5,
		DailySpendThreshold:  100.0,
		BudgetLimit:          1000.0,
		UsageSpikePercent:    200.0,
		Pattern:              "error.*fatal",
		FilePattern:          "*.secret",
		Paths:                []string{"/etc/passwd", "/etc/shadow"},
	}

	if condition.ProgressUnchangedFor != 15*time.Minute {
		t.Errorf("expected ProgressUnchangedFor 15m, got %v", condition.ProgressUnchangedFor)
	}
	if condition.ConsecutiveFailures != 5 {
		t.Errorf("expected ConsecutiveFailures 5, got %d", condition.ConsecutiveFailures)
	}
	if condition.DailySpendThreshold != 100.0 {
		t.Errorf("expected DailySpendThreshold 100.0, got %f", condition.DailySpendThreshold)
	}
	if condition.BudgetLimit != 1000.0 {
		t.Errorf("expected BudgetLimit 1000.0, got %f", condition.BudgetLimit)
	}
	if condition.UsageSpikePercent != 200.0 {
		t.Errorf("expected UsageSpikePercent 200.0, got %f", condition.UsageSpikePercent)
	}
	if condition.Pattern != "error.*fatal" {
		t.Errorf("expected Pattern 'error.*fatal', got '%s'", condition.Pattern)
	}
	if len(condition.Paths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(condition.Paths))
	}
}

func TestAlertRule_AllFields(t *testing.T) {
	rule := AlertRule{
		Name:        "my-rule",
		Type:        AlertTypeTaskFailed,
		Enabled:     true,
		Condition:   RuleCondition{ConsecutiveFailures: 3},
		Severity:    SeverityCritical,
		Channels:    []string{"slack", "telegram"},
		Cooldown:    10 * time.Minute,
		Labels:      map[string]string{"env": "prod"},
		Description: "Test rule description",
	}

	if rule.Name != "my-rule" {
		t.Errorf("expected Name 'my-rule', got '%s'", rule.Name)
	}
	if !rule.Enabled {
		t.Error("expected rule to be enabled")
	}
	if len(rule.Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(rule.Channels))
	}
	if rule.Labels["env"] != "prod" {
		t.Error("expected label env=prod")
	}
}
