package config

import (
	"fmt"
	"log"
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
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/approval"
	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/gateway"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/quality"
	"github.com/alekspetrov/pilot/internal/tunnel"
	"github.com/alekspetrov/pilot/internal/webhooks"
)

// Config represents the main Pilot configuration loaded from YAML.
// It includes settings for the gateway, adapters, orchestrator, memory, projects, and more.
// Use Load to read from a file or DefaultConfig for sensible defaults.
type Config struct {
	Version        string                  `yaml:"version"`
	Gateway        *gateway.Config         `yaml:"gateway"`
	Auth           *gateway.AuthConfig     `yaml:"auth"`
	Adapters       *AdaptersConfig         `yaml:"adapters"`
	Orchestrator   *OrchestratorConfig     `yaml:"orchestrator"`
	Executor       *executor.BackendConfig `yaml:"executor"`
	Memory         *MemoryConfig           `yaml:"memory"`
	Projects       []*ProjectConfig        `yaml:"projects"`
	DefaultProject string                  `yaml:"default_project"`
	Dashboard      *DashboardConfig        `yaml:"dashboard"`
	Alerts         *AlertsConfig           `yaml:"alerts"`
	Budget         *budget.Config          `yaml:"budget"`
	Logging        *logging.Config         `yaml:"logging"`
	Approval       *approval.Config        `yaml:"approval"`
	Quality        *quality.Config         `yaml:"quality"`
	Tunnel         *tunnel.Config          `yaml:"tunnel"`
	Webhooks       *webhooks.Config        `yaml:"webhooks"`
}

// AdaptersConfig holds configuration for external service adapters.
// Each adapter connects Pilot to a different service (Linear, Slack, GitHub, etc.).
type AdaptersConfig struct {
	Linear   *linear.Config   `yaml:"linear"`
	Slack    *slack.Config    `yaml:"slack"`
	Telegram *telegram.Config `yaml:"telegram"`
	GitHub   *github.Config   `yaml:"github"`
	Jira     *jira.Config     `yaml:"jira"`
}

// OrchestratorConfig holds settings for the task orchestrator including
// the AI model to use, concurrency limits, and daily brief scheduling.
type OrchestratorConfig struct {
	Model         string            `yaml:"model"`
	MaxConcurrent int               `yaml:"max_concurrent"`
	DailyBrief    *DailyBriefConfig `yaml:"daily_brief"`
	Execution     *ExecutionConfig  `yaml:"execution"`
}

// ExecutionConfig holds settings for task execution mode.
// Sequential mode executes one task at a time, waiting for PR merge before the next.
// Parallel mode (legacy) processes multiple tasks concurrently.
type ExecutionConfig struct {
	Mode         string        `yaml:"mode"`           // "sequential" or "parallel"
	WaitForMerge bool          `yaml:"wait_for_merge"` // Wait for PR merge before next task
	PollInterval time.Duration `yaml:"poll_interval"`  // How often to check PR status (default: 30s)
	PRTimeout    time.Duration `yaml:"pr_timeout"`     // Max wait time for PR merge (default: 1h)
}

// DefaultExecutionConfig returns sensible defaults for execution config
func DefaultExecutionConfig() *ExecutionConfig {
	return &ExecutionConfig{
		Mode:         "sequential",
		WaitForMerge: true,
		PollInterval: 30 * time.Second,
		PRTimeout:    1 * time.Hour,
	}
}

// DailyBriefConfig holds settings for automated daily summary reports
// including schedule, delivery channels, and content filters.
type DailyBriefConfig struct {
	Enabled  bool                 `yaml:"enabled"`
	Schedule string               `yaml:"schedule"` // Cron syntax: "0 9 * * 1-5"
	Time     string               `yaml:"time"`     // Deprecated: use schedule
	Timezone string               `yaml:"timezone"`
	Channels []BriefChannelConfig `yaml:"channels"`
	Content  BriefContentConfig   `yaml:"content"`
	Filters  BriefFilterConfig    `yaml:"filters"`
}

// BriefChannelConfig defines a delivery channel for daily briefs (Slack or email).
type BriefChannelConfig struct {
	Type       string   `yaml:"type"`       // "slack", "email"
	Channel    string   `yaml:"channel"`    // For Slack: "#channel-name"
	Recipients []string `yaml:"recipients"` // For email
}

// BriefContentConfig controls what content is included in daily briefs.
type BriefContentConfig struct {
	IncludeMetrics     bool `yaml:"include_metrics"`
	IncludeErrors      bool `yaml:"include_errors"`
	MaxItemsPerSection int  `yaml:"max_items_per_section"`
}

// BriefFilterConfig filters which tasks to include in daily briefs.
type BriefFilterConfig struct {
	Projects []string `yaml:"projects"` // Empty = all projects
}

// MemoryConfig holds settings for the persistent memory/storage system.
type MemoryConfig struct {
	Path         string `yaml:"path"`
	CrossProject bool   `yaml:"cross_project"`
}

// ProjectConfig holds configuration for a registered project.
type ProjectConfig struct {
	Name          string               `yaml:"name"`
	Path          string               `yaml:"path"`
	Navigator     bool                 `yaml:"navigator"`
	DefaultBranch string               `yaml:"default_branch"`
	GitHub        *ProjectGitHubConfig `yaml:"github,omitempty"`
}

// ProjectGitHubConfig holds GitHub-specific project configuration for PR creation and issue tracking.
type ProjectGitHubConfig struct {
	Owner string `yaml:"owner"`
	Repo  string `yaml:"repo"`
}

// DashboardConfig holds settings for the terminal UI dashboard.
type DashboardConfig struct {
	RefreshInterval int  `yaml:"refresh_interval"`
	ShowLogs        bool `yaml:"show_logs"`
}

// AlertsConfig holds configuration for the alerting system including
// channels, rules, and default settings.
type AlertsConfig struct {
	Enabled  bool                 `yaml:"enabled"`
	Channels []AlertChannelConfig `yaml:"channels"`
	Rules    []AlertRuleConfig    `yaml:"rules"`
	Defaults AlertDefaultsConfig  `yaml:"defaults"`
}

// AlertChannelConfig configures a destination channel for alerts.
// Supports Slack, Telegram, email, webhooks, and PagerDuty.
// Channel-specific configs use types from the alerts package (single source of truth).
type AlertChannelConfig struct {
	Name       string   `yaml:"name"` // Unique identifier
	Type       string   `yaml:"type"` // "slack", "telegram", "email", "webhook", "pagerduty"
	Enabled    bool     `yaml:"enabled"`
	Severities []string `yaml:"severities"` // Which severities to receive

	// Channel-specific config (types from alerts package)
	Slack     *alerts.SlackChannelConfig     `yaml:"slack,omitempty"`
	Telegram  *alerts.TelegramChannelConfig  `yaml:"telegram,omitempty"`
	Email     *alerts.EmailChannelConfig     `yaml:"email,omitempty"`
	Webhook   *alerts.WebhookChannelConfig   `yaml:"webhook,omitempty"`
	PagerDuty *alerts.PagerDutyChannelConfig `yaml:"pagerduty,omitempty"`
}

// AlertRuleConfig defines a rule that triggers alerts based on specific conditions.
type AlertRuleConfig struct {
	Name        string               `yaml:"name"`
	Type        string               `yaml:"type"` // "task_stuck", "task_failed", etc.
	Enabled     bool                 `yaml:"enabled"`
	Condition   AlertConditionConfig `yaml:"condition"`
	Severity    string               `yaml:"severity"` // "info", "warning", "critical"
	Channels    []string             `yaml:"channels"` // Channel names to send to
	Cooldown    time.Duration        `yaml:"cooldown"` // Min time between alerts
	Description string               `yaml:"description"`
}

// AlertConditionConfig defines the conditions that trigger an alert rule.
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

// AlertDefaultsConfig contains default settings applied to all alert rules.
type AlertDefaultsConfig struct {
	Cooldown           time.Duration `yaml:"cooldown"`
	DefaultSeverity    string        `yaml:"default_severity"`
	SuppressDuplicates bool          `yaml:"suppress_duplicates"`
}

// DefaultConfig returns a new Config instance with sensible default values.
// The gateway binds to localhost:9090, recording is enabled, and common
// alert rules are pre-configured but disabled.
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
			GitHub:   github.DefaultConfig(),
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
			Execution: DefaultExecutionConfig(),
		},
		Executor: executor.DefaultBackendConfig(),
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
		Budget:   budget.DefaultConfig(),
		Logging:  logging.DefaultConfig(),
		Approval: approval.DefaultConfig(),
		Quality:  quality.DefaultConfig(),
		Tunnel:   tunnel.DefaultConfig(),
		Webhooks: webhooks.DefaultConfig(),
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
			Name:        "task_failed",
			Type:        "task_failed",
			Enabled:     true,
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

// Load reads and parses configuration from a YAML file at the given path.
// Environment variables in the file are expanded using os.ExpandEnv syntax.
// If the file does not exist, default configuration is returned.
// Returns an error if the file cannot be read or parsed.
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

// Save writes the configuration to a YAML file at the given path.
// It creates the parent directory if it does not exist.
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

// DefaultConfigPath returns the default configuration file path (~/.pilot/config.yaml).
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

// Validate checks the configuration for errors and returns an error if invalid.
// It validates required fields, port ranges, and authentication settings.
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

// CheckDeprecations logs warnings for deprecated configuration fields.
// Call this after loading configuration to inform users of deprecated settings.
// Returns a slice of deprecation warnings for testing purposes.
func (c *Config) CheckDeprecations() []string {
	var warnings []string

	// Check DailyBrief.Time (deprecated in favor of Schedule)
	if c.Orchestrator != nil && c.Orchestrator.DailyBrief != nil {
		if c.Orchestrator.DailyBrief.Time != "" {
			msg := "config: orchestrator.daily_brief.time is deprecated, use schedule (cron syntax) instead"
			log.Printf("DEPRECATED: %s", msg)
			warnings = append(warnings, msg)
		}
	}

	return warnings
}

// GetProject returns the project configuration for a given filesystem path.
// Returns nil if no project is configured for that path.
func (c *Config) GetProject(path string) *ProjectConfig {
	for _, project := range c.Projects {
		if project.Path == path {
			return project
		}
	}
	return nil
}

// GetProjectByName returns the project configuration matching the given name.
// The comparison is case-insensitive. Returns nil if no matching project is found.
func (c *Config) GetProjectByName(name string) *ProjectConfig {
	nameLower := strings.ToLower(name)
	for _, project := range c.Projects {
		if strings.ToLower(project.Name) == nameLower {
			return project
		}
	}
	return nil
}

// GetDefaultProject returns the default project configuration.
// It first checks the DefaultProject setting by name, then falls back to the first project.
// Returns nil if no projects are configured.
func (c *Config) GetDefaultProject() *ProjectConfig {
	if c.DefaultProject != "" {
		if proj := c.GetProjectByName(c.DefaultProject); proj != nil {
			return proj
		}
	}
	if len(c.Projects) > 0 {
		return c.Projects[0]
	}
	return nil
}
