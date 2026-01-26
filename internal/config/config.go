package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/jira"
	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/gateway"
	"github.com/alekspetrov/pilot/internal/logging"
)

// Config represents the main configuration
type Config struct {
	Version      string              `yaml:"version"`
	Gateway      *gateway.Config     `yaml:"gateway"`
	Auth         *gateway.AuthConfig `yaml:"auth"`
	Adapters     *AdaptersConfig     `yaml:"adapters"`
	Orchestrator *OrchestratorConfig `yaml:"orchestrator"`
	Memory       *MemoryConfig       `yaml:"memory"`
	Projects     []*ProjectConfig    `yaml:"projects"`
	Dashboard    *DashboardConfig    `yaml:"dashboard"`
	Alerts       *AlertsConfig       `yaml:"alerts"`
	Logging      *logging.Config     `yaml:"logging"`
}

// AdaptersConfig holds adapter configurations
type AdaptersConfig struct {
	Linear   *linear.Config   `yaml:"linear"`
	Slack    *slack.Config    `yaml:"slack"`
	Telegram *telegram.Config `yaml:"telegram"`
	Github   *github.Config   `yaml:"github"`
	Jira     *jira.Config     `yaml:"jira"`
}

// OrchestratorConfig holds orchestrator settings
type OrchestratorConfig struct {
	Model         string            `yaml:"model"`
	MaxConcurrent int               `yaml:"max_concurrent"`
	DailyBrief    *DailyBriefConfig `yaml:"daily_brief"`
}

// DailyBriefConfig holds daily brief settings
type DailyBriefConfig struct {
	Enabled  bool                  `yaml:"enabled"`
	Schedule string                `yaml:"schedule"` // Cron syntax: "0 9 * * 1-5"
	Time     string                `yaml:"time"`     // Deprecated: use schedule
	Timezone string                `yaml:"timezone"`
	Channels []BriefChannelConfig  `yaml:"channels"`
	Content  BriefContentConfig    `yaml:"content"`
	Filters  BriefFilterConfig     `yaml:"filters"`
}

// BriefChannelConfig defines a delivery channel
type BriefChannelConfig struct {
	Type       string   `yaml:"type"`       // "slack", "email"
	Channel    string   `yaml:"channel"`    // For Slack: "#channel-name"
	Recipients []string `yaml:"recipients"` // For email
}

// BriefContentConfig controls what content is included
type BriefContentConfig struct {
	IncludeMetrics     bool `yaml:"include_metrics"`
	IncludeErrors      bool `yaml:"include_errors"`
	MaxItemsPerSection int  `yaml:"max_items_per_section"`
}

// BriefFilterConfig filters which tasks to include
type BriefFilterConfig struct {
	Projects []string `yaml:"projects"` // Empty = all projects
}

// MemoryConfig holds memory settings
type MemoryConfig struct {
	Path         string `yaml:"path"`
	CrossProject bool   `yaml:"cross_project"`
}

// ProjectConfig holds project configuration
type ProjectConfig struct {
	Name          string `yaml:"name"`
	Path          string `yaml:"path"`
	Navigator     bool   `yaml:"navigator"`
	DefaultBranch string `yaml:"default_branch"`
}

// DashboardConfig holds dashboard settings
type DashboardConfig struct {
	RefreshInterval int  `yaml:"refresh_interval"`
	ShowLogs        bool `yaml:"show_logs"`
}

// AlertsConfig holds alerting configuration
type AlertsConfig struct {
	Enabled  bool                  `yaml:"enabled"`
	Channels []AlertChannelConfig  `yaml:"channels"`
	Rules    []AlertRuleConfig     `yaml:"rules"`
	Defaults AlertDefaultsConfig   `yaml:"defaults"`
}

// AlertChannelConfig configures an alert channel
type AlertChannelConfig struct {
	Name       string   `yaml:"name"`       // Unique identifier
	Type       string   `yaml:"type"`       // "slack", "telegram", "email", "webhook", "pagerduty"
	Enabled    bool     `yaml:"enabled"`
	Severities []string `yaml:"severities"` // Which severities to receive

	// Channel-specific config
	Slack     *AlertSlackConfig     `yaml:"slack,omitempty"`
	Telegram  *AlertTelegramConfig  `yaml:"telegram,omitempty"`
	Email     *AlertEmailConfig     `yaml:"email,omitempty"`
	Webhook   *AlertWebhookConfig   `yaml:"webhook,omitempty"`
	PagerDuty *AlertPagerDutyConfig `yaml:"pagerduty,omitempty"`
}

// AlertSlackConfig for Slack alerts
type AlertSlackConfig struct {
	Channel string `yaml:"channel"` // #channel-name
}

// AlertTelegramConfig for Telegram alerts
type AlertTelegramConfig struct {
	ChatID int64 `yaml:"chat_id"`
}

// AlertEmailConfig for email alerts
type AlertEmailConfig struct {
	To      []string `yaml:"to"`
	Subject string   `yaml:"subject"` // Optional custom subject template
}

// AlertWebhookConfig for webhook alerts
type AlertWebhookConfig struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"` // POST, PUT
	Headers map[string]string `yaml:"headers"`
	Secret  string            `yaml:"secret"` // For HMAC signing
}

// AlertPagerDutyConfig for PagerDuty alerts
type AlertPagerDutyConfig struct {
	RoutingKey string `yaml:"routing_key"` // Integration key
	ServiceID  string `yaml:"service_id"`
}

// AlertRuleConfig defines an alert rule
type AlertRuleConfig struct {
	Name        string                 `yaml:"name"`
	Type        string                 `yaml:"type"` // "task_stuck", "task_failed", etc.
	Enabled     bool                   `yaml:"enabled"`
	Condition   AlertConditionConfig   `yaml:"condition"`
	Severity    string                 `yaml:"severity"` // "info", "warning", "critical"
	Channels    []string               `yaml:"channels"` // Channel names to send to
	Cooldown    time.Duration          `yaml:"cooldown"` // Min time between alerts
	Description string                 `yaml:"description"`
}

// AlertConditionConfig defines alert trigger conditions
type AlertConditionConfig struct {
	ProgressUnchangedFor time.Duration `yaml:"progress_unchanged_for"`
	ConsecutiveFailures  int           `yaml:"consecutive_failures"`
	DailySpendThreshold  float64       `yaml:"daily_spend_threshold"`
	BudgetLimit          float64       `yaml:"budget_limit"`
	UsageSpikePercent    float64       `yaml:"usage_spike_percent"`
	Pattern              string        `yaml:"pattern"`
	FilePattern          string        `yaml:"file_pattern"`
	Paths                []string      `yaml:"paths"`
}

// AlertDefaultsConfig contains default alert settings
type AlertDefaultsConfig struct {
	Cooldown           time.Duration `yaml:"cooldown"`
	DefaultSeverity    string        `yaml:"default_severity"`
	SuppressDuplicates bool          `yaml:"suppress_duplicates"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	homeDir, _ := os.UserHomeDir()
	return &Config{
		Version: "1.0",
		Gateway: &gateway.Config{
			Host: "127.0.0.1",
			Port: 9090,
		},
		Auth: &gateway.AuthConfig{
			Type: gateway.AuthTypeClaudeCode,
		},
		Adapters: &AdaptersConfig{
			Linear:   linear.DefaultConfig(),
			Slack:    slack.DefaultConfig(),
			Telegram: telegram.DefaultConfig(),
			Github:   github.DefaultConfig(),
			Jira:     jira.DefaultConfig(),
		},
		Orchestrator: &OrchestratorConfig{
			Model:         "claude-sonnet-4-20250514",
			MaxConcurrent: 2,
			DailyBrief: &DailyBriefConfig{
				Enabled:  false,
				Schedule: "0 9 * * 1-5", // 9 AM weekdays
				Timezone: "America/New_York",
				Channels: []BriefChannelConfig{},
				Content: BriefContentConfig{
					IncludeMetrics:     true,
					IncludeErrors:      true,
					MaxItemsPerSection: 10,
				},
				Filters: BriefFilterConfig{
					Projects: []string{},
				},
			},
		},
		Memory: &MemoryConfig{
			Path:         filepath.Join(homeDir, ".pilot", "data"),
			CrossProject: true,
		},
		Projects: []*ProjectConfig{},
		Dashboard: &DashboardConfig{
			RefreshInterval: 1000,
			ShowLogs:        true,
		},
		Alerts: &AlertsConfig{
			Enabled:  false,
			Channels: []AlertChannelConfig{},
			Rules:    defaultAlertRules(),
			Defaults: AlertDefaultsConfig{
				Cooldown:           5 * time.Minute,
				DefaultSeverity:    "warning",
				SuppressDuplicates: true,
			},
		},
		Logging: logging.DefaultConfig(),
	}
}

// defaultAlertRules returns the default alert rules
func defaultAlertRules() []AlertRuleConfig {
	return []AlertRuleConfig{
		{
			Name:    "task_stuck",
			Type:    "task_stuck",
			Enabled: true,
			Condition: AlertConditionConfig{
				ProgressUnchangedFor: 10 * time.Minute,
			},
			Severity:    "warning",
			Channels:    []string{},
			Cooldown:    15 * time.Minute,
			Description: "Alert when a task has no progress for 10 minutes",
		},
		{
			Name:    "task_failed",
			Type:    "task_failed",
			Enabled: true,
			Condition:   AlertConditionConfig{},
			Severity:    "warning",
			Channels:    []string{},
			Cooldown:    0,
			Description: "Alert when a task fails",
		},
		{
			Name:    "consecutive_failures",
			Type:    "consecutive_failures",
			Enabled: true,
			Condition: AlertConditionConfig{
				ConsecutiveFailures: 3,
			},
			Severity:    "critical",
			Channels:    []string{},
			Cooldown:    30 * time.Minute,
			Description: "Alert when 3 or more consecutive tasks fail",
		},
		{
			Name:    "daily_spend",
			Type:    "daily_spend_exceeded",
			Enabled: false,
			Condition: AlertConditionConfig{
				DailySpendThreshold: 50.0,
			},
			Severity:    "warning",
			Channels:    []string{},
			Cooldown:    1 * time.Hour,
			Description: "Alert when daily spend exceeds threshold",
		},
		{
			Name:    "budget_depleted",
			Type:    "budget_depleted",
			Enabled: false,
			Condition: AlertConditionConfig{
				BudgetLimit: 500.0,
			},
			Severity:    "critical",
			Channels:    []string{},
			Cooldown:    4 * time.Hour,
			Description: "Alert when budget limit is exceeded",
		},
	}
}

// Load loads configuration from a file
func Load(path string) (*Config, error) {
	config := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return config, nil // Return defaults if no config file
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	if err := yaml.Unmarshal([]byte(expanded), config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Expand paths
	if config.Memory != nil {
		config.Memory.Path = expandPath(config.Memory.Path)
	}
	for _, project := range config.Projects {
		project.Path = expandPath(project.Path)
	}

	return config, nil
}

// Save saves configuration to a file
func Save(config *Config, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// DefaultConfigPath returns the default configuration path
func DefaultConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".pilot", "config.yaml")
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, path[1:])
	}
	return path
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Gateway == nil {
		return fmt.Errorf("gateway configuration is required")
	}
	if c.Gateway.Port < 1 || c.Gateway.Port > 65535 {
		return fmt.Errorf("invalid gateway port: %d", c.Gateway.Port)
	}
	if c.Auth != nil && c.Auth.Type == gateway.AuthTypeAPIToken && c.Auth.Token == "" {
		return fmt.Errorf("API token is required when auth type is api-token")
	}
	return nil
}

// GetProject returns project configuration by path
func (c *Config) GetProject(path string) *ProjectConfig {
	for _, project := range c.Projects {
		if project.Path == path {
			return project
		}
	}
	return nil
}
