package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/gateway"
)

// Config represents the main configuration
type Config struct {
	Version      string             `yaml:"version"`
	Gateway      *gateway.Config    `yaml:"gateway"`
	Auth         *gateway.AuthConfig `yaml:"auth"`
	Adapters     *AdaptersConfig    `yaml:"adapters"`
	Orchestrator *OrchestratorConfig `yaml:"orchestrator"`
	Memory       *MemoryConfig      `yaml:"memory"`
	Projects     []*ProjectConfig   `yaml:"projects"`
	Dashboard    *DashboardConfig   `yaml:"dashboard"`
}

// AdaptersConfig holds adapter configurations
type AdaptersConfig struct {
	Linear   *linear.Config   `yaml:"linear"`
	Slack    *slack.Config    `yaml:"slack"`
	Telegram *telegram.Config `yaml:"telegram"`
}

// OrchestratorConfig holds orchestrator settings
type OrchestratorConfig struct {
	Model         string            `yaml:"model"`
	MaxConcurrent int               `yaml:"max_concurrent"`
	DailyBrief    *DailyBriefConfig `yaml:"daily_brief"`
}

// DailyBriefConfig holds daily brief settings
type DailyBriefConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Time     string `yaml:"time"`
	Timezone string `yaml:"timezone"`
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
		},
		Orchestrator: &OrchestratorConfig{
			Model:         "claude-sonnet-4-20250514",
			MaxConcurrent: 2,
			DailyBrief: &DailyBriefConfig{
				Enabled:  false,
				Time:     "09:00",
				Timezone: "America/New_York",
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
