package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/gateway"
)

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	t.Run("Version", func(t *testing.T) {
		if config.Version != "1.0" {
			t.Errorf("Version = %q, want %q", config.Version, "1.0")
		}
	})

	t.Run("Gateway", func(t *testing.T) {
		if config.Gateway == nil {
			t.Fatal("Gateway config is nil")
		}
		if config.Gateway.Host != "127.0.0.1" {
			t.Errorf("Gateway.Host = %q, want %q", config.Gateway.Host, "127.0.0.1")
		}
		if config.Gateway.Port != 9090 {
			t.Errorf("Gateway.Port = %d, want %d", config.Gateway.Port, 9090)
		}
	})

	t.Run("Auth", func(t *testing.T) {
		if config.Auth == nil {
			t.Fatal("Auth config is nil")
		}
		if config.Auth.Type != gateway.AuthTypeClaudeCode {
			t.Errorf("Auth.Type = %q, want %q", config.Auth.Type, gateway.AuthTypeClaudeCode)
		}
	})

	t.Run("Adapters", func(t *testing.T) {
		if config.Adapters == nil {
			t.Fatal("Adapters config is nil")
		}
		if config.Adapters.Linear == nil {
			t.Error("Adapters.Linear is nil")
		}
		if config.Adapters.Slack == nil {
			t.Error("Adapters.Slack is nil")
		}
		if config.Adapters.Telegram == nil {
			t.Error("Adapters.Telegram is nil")
		}
		if config.Adapters.GitHub == nil {
			t.Error("Adapters.GitHub is nil")
		}
		if config.Adapters.Jira == nil {
			t.Error("Adapters.Jira is nil")
		}
	})

	t.Run("Orchestrator", func(t *testing.T) {
		if config.Orchestrator == nil {
			t.Fatal("Orchestrator config is nil")
		}
		if config.Orchestrator.Model != "claude-sonnet-4-6" {
			t.Errorf("Orchestrator.Model = %q, want %q", config.Orchestrator.Model, "claude-sonnet-4-6")
		}
		if config.Orchestrator.MaxConcurrent != 2 {
			t.Errorf("Orchestrator.MaxConcurrent = %d, want %d", config.Orchestrator.MaxConcurrent, 2)
		}
		if config.Orchestrator.DailyBrief == nil {
			t.Fatal("Orchestrator.DailyBrief is nil")
		}
		if config.Orchestrator.DailyBrief.Enabled != false {
			t.Error("DailyBrief.Enabled should be false by default")
		}
		if config.Orchestrator.DailyBrief.Schedule != "0 9 * * 1-5" {
			t.Errorf("DailyBrief.Schedule = %q, want %q", config.Orchestrator.DailyBrief.Schedule, "0 9 * * 1-5")
		}
	})

	t.Run("Execution", func(t *testing.T) {
		if config.Orchestrator.Execution == nil {
			t.Fatal("Orchestrator.Execution is nil")
		}
		exec := config.Orchestrator.Execution
		if exec.Mode != "auto" {
			t.Errorf("Execution.Mode = %q, want %q", exec.Mode, "auto")
		}
		if exec.WaitForMerge != true {
			t.Error("Execution.WaitForMerge should be true by default")
		}
		if exec.PollInterval != 30*time.Second {
			t.Errorf("Execution.PollInterval = %v, want %v", exec.PollInterval, 30*time.Second)
		}
		if exec.PRTimeout != 1*time.Hour {
			t.Errorf("Execution.PRTimeout = %v, want %v", exec.PRTimeout, 1*time.Hour)
		}
	})

	t.Run("Memory", func(t *testing.T) {
		if config.Memory == nil {
			t.Fatal("Memory config is nil")
		}
		homeDir, _ := os.UserHomeDir()
		expectedPath := filepath.Join(homeDir, ".pilot", "data")
		if config.Memory.Path != expectedPath {
			t.Errorf("Memory.Path = %q, want %q", config.Memory.Path, expectedPath)
		}
		if config.Memory.CrossProject != true {
			t.Error("Memory.CrossProject should be true by default")
		}
	})

	t.Run("Dashboard", func(t *testing.T) {
		if config.Dashboard == nil {
			t.Fatal("Dashboard config is nil")
		}
		if config.Dashboard.RefreshInterval != 1000 {
			t.Errorf("Dashboard.RefreshInterval = %d, want %d", config.Dashboard.RefreshInterval, 1000)
		}
		if config.Dashboard.ShowLogs != true {
			t.Error("Dashboard.ShowLogs should be true by default")
		}
	})

	t.Run("Alerts", func(t *testing.T) {
		if config.Alerts == nil {
			t.Fatal("Alerts config is nil")
		}
		if config.Alerts.Enabled != false {
			t.Error("Alerts.Enabled should be false by default")
		}
		if config.Alerts.Defaults.Cooldown != 5*time.Minute {
			t.Errorf("Alerts.Defaults.Cooldown = %v, want %v", config.Alerts.Defaults.Cooldown, 5*time.Minute)
		}
		if config.Alerts.Defaults.DefaultSeverity != "warning" {
			t.Errorf("Alerts.Defaults.DefaultSeverity = %q, want %q", config.Alerts.Defaults.DefaultSeverity, "warning")
		}
		if len(config.Alerts.Rules) == 0 {
			t.Error("Alerts.Rules should have default rules")
		}
	})

	t.Run("Budget", func(t *testing.T) {
		if config.Budget == nil {
			t.Error("Budget config is nil")
		}
	})

	t.Run("Logging", func(t *testing.T) {
		if config.Logging == nil {
			t.Error("Logging config is nil")
		}
	})

	t.Run("Approval", func(t *testing.T) {
		if config.Approval == nil {
			t.Error("Approval config is nil")
		}
	})

	t.Run("Quality", func(t *testing.T) {
		if config.Quality == nil {
			t.Error("Quality config is nil")
		}
	})

	t.Run("Tunnel", func(t *testing.T) {
		if config.Tunnel == nil {
			t.Error("Tunnel config is nil")
		}
	})

	t.Run("Projects", func(t *testing.T) {
		if config.Projects == nil {
			t.Fatal("Projects is nil")
		}
		if len(config.Projects) != 0 {
			t.Errorf("Projects length = %d, want 0", len(config.Projects))
		}
	})
}

func TestBotConfig_YAMLRoundTrip(t *testing.T) {
	yaml := `
version: "1.0"
gateway:
  host: "127.0.0.1"
  port: 9090
bot:
  enabled: true
  model: "claude-haiku-4-5-20251001"
  answer_model: "claude-sonnet-4-6"
  api_key: "test-api-key"
  persona: "You are a Go expert."
`
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Bot == nil {
		t.Fatal("Bot config is nil after parsing")
	}
	if !cfg.Bot.Enabled {
		t.Error("Bot.Enabled should be true")
	}
	if cfg.Bot.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Bot.Model = %q, want claude-haiku-4-5-20251001", cfg.Bot.Model)
	}
	if cfg.Bot.AnswerModel != "claude-sonnet-4-6" {
		t.Errorf("Bot.AnswerModel = %q, want claude-sonnet-4-6", cfg.Bot.AnswerModel)
	}
	if cfg.Bot.APIKey != "test-api-key" {
		t.Errorf("Bot.APIKey = %q, want test-api-key", cfg.Bot.APIKey)
	}
	if cfg.Bot.Persona != "You are a Go expert." {
		t.Errorf("Bot.Persona = %q, want 'You are a Go expert.'", cfg.Bot.Persona)
	}
}

func TestBotConfig_Nil_ByDefault(t *testing.T) {
	// A config file without a bot: block should have nil Bot.
	cfg := DefaultConfig()
	if cfg.Bot != nil {
		t.Errorf("DefaultConfig().Bot = %v, want nil", cfg.Bot)
	}
}

func TestLoad(t *testing.T) {
	t.Run("MissingFile", func(t *testing.T) {
		config, err := Load("/nonexistent/path/config.yaml")
		if err != nil {
			t.Errorf("Load should return defaults for missing file, got error: %v", err)
		}
		if config == nil {
			t.Fatal("Load returned nil config for missing file")
		}
		// Should return default config
		if config.Version != "1.0" {
			t.Errorf("Version = %q, want default %q", config.Version, "1.0")
		}
	})

	t.Run("ValidConfigFile", func(t *testing.T) {
		// Create temp config file
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "2.0"
gateway:
  host: "0.0.0.0"
  port: 8080
orchestrator:
  model: "claude-opus"
  max_concurrent: 4
memory:
  path: "/custom/path"
  cross_project: false
projects:
  - name: "test-project"
    path: "/path/to/project"
    navigator: true
    default_branch: "develop"
default_project: "test-project"
dashboard:
  refresh_interval: 500
  show_logs: false
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		config, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if config.Version != "2.0" {
			t.Errorf("Version = %q, want %q", config.Version, "2.0")
		}
		if config.Gateway.Host != "0.0.0.0" {
			t.Errorf("Gateway.Host = %q, want %q", config.Gateway.Host, "0.0.0.0")
		}
		if config.Gateway.Port != 8080 {
			t.Errorf("Gateway.Port = %d, want %d", config.Gateway.Port, 8080)
		}
		if config.Orchestrator.Model != "claude-opus" {
			t.Errorf("Orchestrator.Model = %q, want %q", config.Orchestrator.Model, "claude-opus")
		}
		if config.Orchestrator.MaxConcurrent != 4 {
			t.Errorf("Orchestrator.MaxConcurrent = %d, want %d", config.Orchestrator.MaxConcurrent, 4)
		}
		if config.Memory.Path != "/custom/path" {
			t.Errorf("Memory.Path = %q, want %q", config.Memory.Path, "/custom/path")
		}
		if config.Memory.CrossProject != false {
			t.Error("Memory.CrossProject should be false")
		}
		if len(config.Projects) != 1 {
			t.Fatalf("Projects length = %d, want 1", len(config.Projects))
		}
		if config.Projects[0].Name != "test-project" {
			t.Errorf("Projects[0].Name = %q, want %q", config.Projects[0].Name, "test-project")
		}
		if config.DefaultProject != "test-project" {
			t.Errorf("DefaultProject = %q, want %q", config.DefaultProject, "test-project")
		}
		if config.Dashboard.RefreshInterval != 500 {
			t.Errorf("Dashboard.RefreshInterval = %d, want %d", config.Dashboard.RefreshInterval, 500)
		}
		if config.Dashboard.ShowLogs != false {
			t.Error("Dashboard.ShowLogs should be false")
		}
	})

	t.Run("EnvironmentVariableExpansion", func(t *testing.T) {
		// Set test environment variable
		testValue := "my-secret-token"
		t.Setenv("TEST_LINEAR_TOKEN", testValue)

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
adapters:
  linear:
    enabled: true
    api_key: "${TEST_LINEAR_TOKEN}"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		config, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if config.Adapters.Linear.APIKey != testValue {
			t.Errorf("Linear.APIKey = %q, want %q (env var expansion failed)", config.Adapters.Linear.APIKey, testValue)
		}
	})

	t.Run("PathExpansionTilde", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
memory:
  path: "~/custom/pilot/data"
projects:
  - name: "home-project"
    path: "~/projects/myapp"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		config, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		homeDir, _ := os.UserHomeDir()

		expectedMemoryPath := filepath.Join(homeDir, "custom/pilot/data")
		if config.Memory.Path != expectedMemoryPath {
			t.Errorf("Memory.Path = %q, want %q", config.Memory.Path, expectedMemoryPath)
		}

		expectedProjectPath := filepath.Join(homeDir, "projects/myapp")
		if config.Projects[0].Path != expectedProjectPath {
			t.Errorf("Projects[0].Path = %q, want %q", config.Projects[0].Path, expectedProjectPath)
		}
	})

	t.Run("InvalidYAML", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
gateway:
  host: [invalid yaml structure
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		_, err := Load(configPath)
		if err == nil {
			t.Error("Load should fail for invalid YAML")
		}
	})

	t.Run("UnreadableFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		if err := os.WriteFile(configPath, []byte("version: 1.0"), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		// Make file unreadable
		if err := os.Chmod(configPath, 0000); err != nil {
			t.Skipf("Cannot change file permissions: %v", err)
		}
		defer func() { _ = os.Chmod(configPath, 0644) }() // Restore permissions for cleanup

		_, err := Load(configPath)
		if err == nil {
			t.Error("Load should fail for unreadable file")
		}
	})
}

// TestLoad_EnvVarPreScan covers GH-3755: unset/empty env var references in
// sensitive config keys (token/key/secret/password) must fail loudly instead
// of silently expanding to "", while non-sensitive keys only warn.
func TestLoad_EnvVarPreScan(t *testing.T) {
	t.Run("SensitiveKeyUnsetEnvVar_ReturnsError", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
adapters:
  github:
    enabled: true
    token: "${UNSET_VAR}"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		_, err := Load(configPath)
		if err == nil {
			t.Fatal("Load should return an error when a sensitive key references an unset env var")
		}
		if !strings.Contains(err.Error(), "UNSET_VAR") {
			t.Errorf("error = %q, want it to mention UNSET_VAR", err.Error())
		}
	})

	t.Run("NonSensitiveKeyUnsetEnvVar_WarnsAndLoads", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
adapters:
  github:
    enabled: true
    pilot_label: "${UNSET_LABEL_VAR}"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		var logBuf bytes.Buffer
		originalOutput := log.Writer()
		log.SetOutput(&logBuf)
		defer log.SetOutput(originalOutput)

		config, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load should succeed for a non-sensitive key with an unset env var, got error: %v", err)
		}
		if config.Adapters.GitHub.PilotLabel != "" {
			t.Errorf("PilotLabel = %q, want empty (unset var expands to empty)", config.Adapters.GitHub.PilotLabel)
		}
		if !strings.Contains(logBuf.String(), "UNSET_LABEL_VAR") {
			t.Errorf("expected a warning log mentioning UNSET_LABEL_VAR, got: %q", logBuf.String())
		}
	})

	t.Run("RealConfigStillLoads", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		t.Setenv("REGRESSION_TEST_LINEAR_TOKEN", "real-token-value")

		configContent := `
version: "2.0"
gateway:
  host: "0.0.0.0"
  port: 8080
adapters:
  linear:
    enabled: true
    api_key: "${REGRESSION_TEST_LINEAR_TOKEN}"
orchestrator:
  model: "claude-opus"
  max_concurrent: 4
projects:
  - name: "test-project"
    path: "/path/to/project"
    navigator: true
    default_branch: "develop"
default_project: "test-project"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		config, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed for regression config: %v", err)
		}
		if config.Adapters.Linear.APIKey != "real-token-value" {
			t.Errorf("Linear.APIKey = %q, want %q", config.Adapters.Linear.APIKey, "real-token-value")
		}
		if config.Version != "2.0" {
			t.Errorf("Version = %q, want %q", config.Version, "2.0")
		}
		if config.Projects[0].Name != "test-project" {
			t.Errorf("Projects[0].Name = %q, want %q", config.Projects[0].Name, "test-project")
		}
	})
}

func TestSave(t *testing.T) {
	t.Run("SaveToNewFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "subdir", "config.yaml")

		config := DefaultConfig()
		config.Version = "test-version"
		config.Gateway.Port = 9999

		err := Save(config, configPath)
		if err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		// Verify file was created
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Error("Config file was not created")
		}

		// Load it back and verify
		loadedConfig, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if loadedConfig.Version != "test-version" {
			t.Errorf("Version = %q, want %q", loadedConfig.Version, "test-version")
		}
		if loadedConfig.Gateway.Port != 9999 {
			t.Errorf("Gateway.Port = %d, want %d", loadedConfig.Gateway.Port, 9999)
		}
	})

	t.Run("SaveToExistingFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		// Create initial config
		initialConfig := DefaultConfig()
		initialConfig.Version = "initial"
		if err := Save(initialConfig, configPath); err != nil {
			t.Fatalf("Initial save failed: %v", err)
		}

		// Save updated config
		updatedConfig := DefaultConfig()
		updatedConfig.Version = "updated"
		if err := Save(updatedConfig, configPath); err != nil {
			t.Fatalf("Updated save failed: %v", err)
		}

		// Verify it was overwritten
		loadedConfig, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		if loadedConfig.Version != "updated" {
			t.Errorf("Version = %q, want %q", loadedConfig.Version, "updated")
		}
	})

	t.Run("SaveToUnwritableDirectory", func(t *testing.T) {
		// Try to save to a path we can't write to
		err := Save(DefaultConfig(), "/root/unwritable/config.yaml")
		if err == nil {
			// On some systems this might work if running as root
			t.Skip("Unable to test unwritable directory (might be running as root)")
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		wantErr     bool
		errContains string
	}{
		{
			name:    "ValidDefaultConfig",
			config:  DefaultConfig(),
			wantErr: false,
		},
		{
			name: "NilGateway",
			config: func() *Config {
				c := DefaultConfig()
				c.Gateway = nil
				return c
			}(),
			wantErr:     true,
			errContains: "gateway configuration is required",
		},
		{
			name: "InvalidPortZero",
			config: func() *Config {
				c := DefaultConfig()
				c.Gateway.Port = 0
				return c
			}(),
			wantErr:     true,
			errContains: "invalid gateway port",
		},
		{
			name: "InvalidPortNegative",
			config: func() *Config {
				c := DefaultConfig()
				c.Gateway.Port = -1
				return c
			}(),
			wantErr:     true,
			errContains: "invalid gateway port",
		},
		{
			name: "InvalidPortTooHigh",
			config: func() *Config {
				c := DefaultConfig()
				c.Gateway.Port = 65536
				return c
			}(),
			wantErr:     true,
			errContains: "invalid gateway port",
		},
		{
			name: "ValidPortMinimum",
			config: func() *Config {
				c := DefaultConfig()
				c.Gateway.Port = 1
				return c
			}(),
			wantErr: false,
		},
		{
			name: "ValidPortMaximum",
			config: func() *Config {
				c := DefaultConfig()
				c.Gateway.Port = 65535
				return c
			}(),
			wantErr: false,
		},
		{
			name: "APITokenAuthWithoutToken",
			config: func() *Config {
				c := DefaultConfig()
				c.Auth = &gateway.AuthConfig{
					Type:  gateway.AuthTypeAPIToken,
					Token: "",
				}
				return c
			}(),
			wantErr:     true,
			errContains: "API token is required",
		},
		{
			name: "APITokenAuthWithToken",
			config: func() *Config {
				c := DefaultConfig()
				c.Auth = &gateway.AuthConfig{
					Type:  gateway.AuthTypeAPIToken,
					Token: "valid-token",
				}
				return c
			}(),
			wantErr: false,
		},
		{
			name: "ClaudeCodeAuthWithoutToken",
			config: func() *Config {
				c := DefaultConfig()
				c.Auth = &gateway.AuthConfig{
					Type: gateway.AuthTypeClaudeCode,
				}
				return c
			}(),
			wantErr: false, // ClaudeCode auth doesn't require a token
		},
		{
			name: "NilAuth",
			config: func() *Config {
				c := DefaultConfig()
				c.Auth = nil
				return c
			}(),
			wantErr: false, // Nil auth is allowed
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Error("Validate() should return error")
				} else if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("Validate() error = %q, want error containing %q", err.Error(), tt.errContains)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestGetProject(t *testing.T) {
	config := DefaultConfig()
	config.Projects = []*ProjectConfig{
		{Name: "project1", Path: "/path/to/project1"},
		{Name: "project2", Path: "/path/to/project2"},
		{Name: "project3", Path: "/path/to/project3"},
	}

	tests := []struct {
		name     string
		path     string
		wantName string
		wantNil  bool
	}{
		{
			name:     "ExistingProject",
			path:     "/path/to/project1",
			wantName: "project1",
			wantNil:  false,
		},
		{
			name:     "SecondProject",
			path:     "/path/to/project2",
			wantName: "project2",
			wantNil:  false,
		},
		{
			name:    "NonexistentProject",
			path:    "/path/to/nonexistent",
			wantNil: true,
		},
		{
			name:    "EmptyPath",
			path:    "",
			wantNil: true,
		},
		{
			name:    "PartialPathMatch",
			path:    "/path/to/project", // Should not match project1
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := config.GetProject(tt.path)
			if tt.wantNil {
				if project != nil {
					t.Errorf("GetProject(%q) = %+v, want nil", tt.path, project)
				}
			} else {
				if project == nil {
					t.Fatalf("GetProject(%q) = nil, want project", tt.path)
				}
				if project.Name != tt.wantName {
					t.Errorf("GetProject(%q).Name = %q, want %q", tt.path, project.Name, tt.wantName)
				}
			}
		})
	}
}

func TestGetProjectByName(t *testing.T) {
	config := DefaultConfig()
	config.Projects = []*ProjectConfig{
		{Name: "MyProject", Path: "/path/to/myproject"},
		{Name: "Another-Project", Path: "/path/to/another"},
		{Name: "UPPERCASE", Path: "/path/to/upper"},
	}

	tests := []struct {
		name     string
		projName string
		wantPath string
		wantNil  bool
	}{
		{
			name:     "ExactMatch",
			projName: "MyProject",
			wantPath: "/path/to/myproject",
			wantNil:  false,
		},
		{
			name:     "LowercaseMatch",
			projName: "myproject",
			wantPath: "/path/to/myproject",
			wantNil:  false,
		},
		{
			name:     "UppercaseMatch",
			projName: "MYPROJECT",
			wantPath: "/path/to/myproject",
			wantNil:  false,
		},
		{
			name:     "MixedCaseMatch",
			projName: "uppercase",
			wantPath: "/path/to/upper",
			wantNil:  false,
		},
		{
			name:     "HyphenatedName",
			projName: "another-project",
			wantPath: "/path/to/another",
			wantNil:  false,
		},
		{
			name:     "NonexistentProject",
			projName: "nonexistent",
			wantNil:  true,
		},
		{
			name:     "EmptyName",
			projName: "",
			wantNil:  true,
		},
		{
			name:     "PartialNameMatch",
			projName: "My", // Should not match MyProject
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := config.GetProjectByName(tt.projName)
			if tt.wantNil {
				if project != nil {
					t.Errorf("GetProjectByName(%q) = %+v, want nil", tt.projName, project)
				}
			} else {
				if project == nil {
					t.Fatalf("GetProjectByName(%q) = nil, want project", tt.projName)
				}
				if project.Path != tt.wantPath {
					t.Errorf("GetProjectByName(%q).Path = %q, want %q", tt.projName, project.Path, tt.wantPath)
				}
			}
		})
	}
}

func TestGetProjectByLinearID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Projects = []*ProjectConfig{
		{Name: "aso-generator", Path: "/path/to/aso", Linear: &ProjectLinearConfig{ProjectID: "proj-abc123"}},
		{Name: "pilot", Path: "/path/to/pilot", Linear: &ProjectLinearConfig{ProjectID: "proj-def456"}},
		{Name: "no-linear", Path: "/path/to/other"},
	}

	tests := []struct {
		name     string
		linearID string
		wantPath string
		wantNil  bool
	}{
		{
			name:     "match first project",
			linearID: "proj-abc123",
			wantPath: "/path/to/aso",
		},
		{
			name:     "match second project",
			linearID: "proj-def456",
			wantPath: "/path/to/pilot",
		},
		{
			name:     "no match",
			linearID: "proj-unknown",
			wantNil:  true,
		},
		{
			name:     "empty ID",
			linearID: "",
			wantNil:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := cfg.GetProjectByLinearID(tt.linearID)
			if tt.wantNil {
				if project != nil {
					t.Errorf("GetProjectByLinearID(%q) = %+v, want nil", tt.linearID, project)
				}
			} else {
				if project == nil {
					t.Fatalf("GetProjectByLinearID(%q) = nil, want project", tt.linearID)
				}
				if project.Path != tt.wantPath {
					t.Errorf("GetProjectByLinearID(%q).Path = %q, want %q", tt.linearID, project.Path, tt.wantPath)
				}
			}
		})
	}
}

func TestGetDefaultProject(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		wantName string
		wantNil  bool
	}{
		{
			name: "DefaultProjectSet",
			config: func() *Config {
				c := DefaultConfig()
				c.Projects = []*ProjectConfig{
					{Name: "first", Path: "/first"},
					{Name: "second", Path: "/second"},
				}
				c.DefaultProject = "second"
				return c
			}(),
			wantName: "second",
			wantNil:  false,
		},
		{
			name: "DefaultProjectCaseInsensitive",
			config: func() *Config {
				c := DefaultConfig()
				c.Projects = []*ProjectConfig{
					{Name: "MyProject", Path: "/myproject"},
				}
				c.DefaultProject = "myproject" // lowercase
				return c
			}(),
			wantName: "MyProject",
			wantNil:  false,
		},
		{
			name: "NoDefaultProjectFallsBackToFirst",
			config: func() *Config {
				c := DefaultConfig()
				c.Projects = []*ProjectConfig{
					{Name: "first", Path: "/first"},
					{Name: "second", Path: "/second"},
				}
				c.DefaultProject = ""
				return c
			}(),
			wantName: "first",
			wantNil:  false,
		},
		{
			name: "DefaultProjectNotFound",
			config: func() *Config {
				c := DefaultConfig()
				c.Projects = []*ProjectConfig{
					{Name: "first", Path: "/first"},
				}
				c.DefaultProject = "nonexistent"
				return c
			}(),
			wantName: "first", // Falls back to first project
			wantNil:  false,
		},
		{
			name: "NoProjects",
			config: func() *Config {
				c := DefaultConfig()
				c.Projects = []*ProjectConfig{}
				c.DefaultProject = ""
				return c
			}(),
			wantNil: true,
		},
		{
			name: "NilProjects",
			config: func() *Config {
				c := DefaultConfig()
				c.Projects = nil
				c.DefaultProject = ""
				return c
			}(),
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := tt.config.GetDefaultProject()
			if tt.wantNil {
				if project != nil {
					t.Errorf("GetDefaultProject() = %+v, want nil", project)
				}
			} else {
				if project == nil {
					t.Fatal("GetDefaultProject() = nil, want project")
				}
				if project.Name != tt.wantName {
					t.Errorf("GetDefaultProject().Name = %q, want %q", project.Name, tt.wantName)
				}
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "TildeOnly",
			input:    "~",
			expected: homeDir,
		},
		{
			name:     "TildeWithPath",
			input:    "~/path/to/file",
			expected: filepath.Join(homeDir, "path/to/file"),
		},
		{
			name:     "TildeWithSlash",
			input:    "~/",
			expected: filepath.Join(homeDir, ""),
		},
		{
			name:     "AbsolutePath",
			input:    "/absolute/path",
			expected: "/absolute/path",
		},
		{
			name:     "RelativePath",
			input:    "relative/path",
			expected: "relative/path",
		},
		{
			name:     "EmptyPath",
			input:    "",
			expected: "",
		},
		{
			name:     "TildeInMiddle",
			input:    "/path/~/with/tilde",
			expected: "/path/~/with/tilde", // Should not expand ~ in middle
		},
		{
			name:     "DoubleSlash",
			input:    "~//double/slash",
			expected: filepath.Join(homeDir, "/double/slash"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandPath(tt.input)
			if result != tt.expected {
				t.Errorf("expandPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestDefaultConfigPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}

	expected := filepath.Join(homeDir, ".pilot", "config.yaml")
	result := DefaultConfigPath()

	if result != expected {
		t.Errorf("DefaultConfigPath() = %q, want %q", result, expected)
	}
}

func TestDefaultAlertRules(t *testing.T) {
	rules := defaultAlertRules()

	if len(rules) == 0 {
		t.Fatal("defaultAlertRules() returned empty slice")
	}

	// Verify expected rules exist
	ruleNames := make(map[string]bool)
	for _, rule := range rules {
		ruleNames[rule.Name] = true
	}

	expectedRules := []string{"task_stuck", "task_failed", "consecutive_failures", "daily_spend", "budget_depleted"}
	for _, name := range expectedRules {
		if !ruleNames[name] {
			t.Errorf("Expected rule %q not found in default rules", name)
		}
	}

	// Verify task_stuck rule configuration
	for _, rule := range rules {
		if rule.Name == "task_stuck" {
			if rule.Condition.ProgressUnchangedFor != 10*time.Minute {
				t.Errorf("task_stuck ProgressUnchangedFor = %v, want %v", rule.Condition.ProgressUnchangedFor, 10*time.Minute)
			}
			if rule.Severity != "warning" {
				t.Errorf("task_stuck Severity = %q, want %q", rule.Severity, "warning")
			}
			if !rule.Enabled {
				t.Error("task_stuck should be enabled by default")
			}
		}
		if rule.Name == "consecutive_failures" {
			if rule.Condition.ConsecutiveFailures != 3 {
				t.Errorf("consecutive_failures ConsecutiveFailures = %d, want %d", rule.Condition.ConsecutiveFailures, 3)
			}
			if rule.Severity != "critical" {
				t.Errorf("consecutive_failures Severity = %q, want %q", rule.Severity, "critical")
			}
		}
	}
}

func TestProjectConfigFields(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
version: "1.0"
projects:
  - name: "full-project"
    path: "/path/to/project"
    navigator: true
    default_branch: "main"
    github:
      owner: "myorg"
      repo: "myrepo"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	config, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(config.Projects) != 1 {
		t.Fatalf("Projects length = %d, want 1", len(config.Projects))
	}

	project := config.Projects[0]
	if project.Name != "full-project" {
		t.Errorf("Project.Name = %q, want %q", project.Name, "full-project")
	}
	if project.Path != "/path/to/project" {
		t.Errorf("Project.Path = %q, want %q", project.Path, "/path/to/project")
	}
	if project.Navigator != true {
		t.Error("Project.Navigator should be true")
	}
	if project.DefaultBranch != "main" {
		t.Errorf("Project.DefaultBranch = %q, want %q", project.DefaultBranch, "main")
	}
	if project.GitHub == nil {
		t.Fatal("Project.GitHub is nil")
	}
	if project.GitHub.Owner != "myorg" {
		t.Errorf("Project.GitHub.Owner = %q, want %q", project.GitHub.Owner, "myorg")
	}
	if project.GitHub.Repo != "myrepo" {
		t.Errorf("Project.GitHub.Repo = %q, want %q", project.GitHub.Repo, "myrepo")
	}
}

// TestProjectConfigBranchFromYAML verifies the branch_from alias deserializes
// alongside default_branch (GH-2290).
func TestProjectConfigBranchFromYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
version: "1.0"
projects:
  - name: "proj"
    path: "/p"
    default_branch: "main"
    branch_from: "dev"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Projects[0]
	if p.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", p.DefaultBranch)
	}
	if p.BranchFrom != "dev" {
		t.Errorf("BranchFrom = %q, want dev", p.BranchFrom)
	}
	if got := p.ResolveBaseBranch(); got != "dev" {
		t.Errorf("ResolveBaseBranch() = %q, want dev", got)
	}
}

// TestProjectConfigCanaryYAML verifies the canary flag deserializes and
// defaults to false when unset (GH-4240).
func TestProjectConfigCanaryYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
version: "1.0"
projects:
  - name: "sandbox"
    path: "/p1"
    canary: true
  - name: "real"
    path: "/p2"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("Projects length = %d, want 2", len(cfg.Projects))
	}
	if !cfg.Projects[0].Canary {
		t.Error("sandbox project Canary = false, want true")
	}
	if cfg.Projects[1].Canary {
		t.Error("real project Canary = true, want false (default)")
	}
}

// TestProjectConfigProjectBoardYAML covers GH-4472: a projects[].github.project_board
// block parses into ProjectGitHubConfig.ProjectBoard; a project with no block gets nil.
func TestProjectConfigProjectBoardYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
version: "1.0"
adapters:
  github:
    repo: "acme/default"
projects:
  - name: "pointer"
    path: "/p1"
    github:
      owner: "acme"
      repo: "pointer"
      project_board:
        enabled: true
        project_number: 2
        status_field: "Status"
        source_enabled: true
        source_status: "Todo"
        statuses:
          in_progress: "In Progress"
          review: "In Review"
          done: "Done"
          failed: "Blocked"
  - name: "no-board"
    path: "/p2"
    github:
      owner: "acme"
      repo: "no-board"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("Projects length = %d, want 2", len(cfg.Projects))
	}

	pointer := cfg.Projects[0]
	if pointer.GitHub == nil || pointer.GitHub.ProjectBoard == nil {
		t.Fatal("pointer project ProjectBoard = nil, want set")
	}
	pb := pointer.GitHub.ProjectBoard
	if !pb.Enabled || pb.ProjectNumber != 2 || pb.StatusField != "Status" {
		t.Errorf("pointer ProjectBoard = %+v, want enabled/project_number=2/status_field=Status", pb)
	}
	if !pb.SourceEnabled || pb.SourceStatus != "Todo" {
		t.Errorf("pointer ProjectBoard source = %+v, want source_enabled/source_status=Todo", pb)
	}
	if pb.Statuses.InProgress != "In Progress" || pb.Statuses.Done != "Done" {
		t.Errorf("pointer ProjectBoard statuses = %+v", pb.Statuses)
	}

	noBoard := cfg.Projects[1]
	if noBoard.GitHub == nil {
		t.Fatal("no-board project GitHub = nil")
	}
	if noBoard.GitHub.ProjectBoard != nil {
		t.Errorf("no-board project ProjectBoard = %+v, want nil", noBoard.GitHub.ProjectBoard)
	}
}

func TestProjectConfigQualityYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
version: "1.0"
quality:
  enabled: true
  gates:
    - name: build
      type: build
      command: "make build"
      required: true
projects:
  - name: "with-quality"
    path: "/with-quality"
    quality:
      enabled: true
      gates:
        - name: build
          type: build
          command: "pnpm run build"
          required: true
  - name: "without-quality"
    path: "/without-quality"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	withQuality := cfg.GetProject("/with-quality")
	if withQuality == nil {
		t.Fatal("project 'with-quality' not found")
	}
	if withQuality.Quality == nil || !withQuality.Quality.Enabled {
		t.Fatal("expected project-level quality config to be set and enabled")
	}
	if len(withQuality.Quality.Gates) != 1 || withQuality.Quality.Gates[0].Command != "pnpm run build" {
		t.Errorf("project quality gates = %+v, want single pnpm build gate", withQuality.Quality.Gates)
	}

	withoutQuality := cfg.GetProject("/without-quality")
	if withoutQuality == nil {
		t.Fatal("project 'without-quality' not found")
	}
	if withoutQuality.Quality != nil {
		t.Errorf("expected project 'without-quality' to have nil Quality, got %+v", withoutQuality.Quality)
	}

	if cfg.Quality == nil || !cfg.Quality.Enabled {
		t.Fatal("expected global quality config to remain set and enabled")
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCheckDeprecations(t *testing.T) {
	tests := []struct {
		name         string
		config       *Config
		wantWarnings int
		wantContains string
	}{
		{
			name:         "NoDeprecatedFields",
			config:       DefaultConfig(),
			wantWarnings: 0,
		},
		{
			name: "DeprecatedTimeField",
			config: func() *Config {
				c := DefaultConfig()
				c.Orchestrator.DailyBrief.Time = "09:00"
				return c
			}(),
			wantWarnings: 1,
			wantContains: "daily_brief.time is deprecated",
		},
		{
			name: "DeprecatedTimeFieldWithSchedule",
			config: func() *Config {
				c := DefaultConfig()
				c.Orchestrator.DailyBrief.Time = "09:00"
				c.Orchestrator.DailyBrief.Schedule = "0 9 * * 1-5"
				return c
			}(),
			wantWarnings: 1,
			wantContains: "use schedule",
		},
		{
			name: "NilOrchestrator",
			config: func() *Config {
				c := DefaultConfig()
				c.Orchestrator = nil
				return c
			}(),
			wantWarnings: 0,
		},
		{
			name: "NilDailyBrief",
			config: func() *Config {
				c := DefaultConfig()
				c.Orchestrator.DailyBrief = nil
				return c
			}(),
			wantWarnings: 0,
		},
		{
			name: "EmptyTimeField",
			config: func() *Config {
				c := DefaultConfig()
				c.Orchestrator.DailyBrief.Time = ""
				return c
			}(),
			wantWarnings: 0,
		},
		{
			name: "GitHubUseSDKPollerFalseButEnabled",
			config: func() *Config {
				c := DefaultConfig()
				c.Adapters.GitHub.Enabled = true
				c.Adapters.GitHub.UseSDKPoller = false
				return c
			}(),
			wantWarnings: 0,
		},
		{
			name: "GitHubUseSDKPollerAbsentButEnabled",
			config: func() *Config {
				c := DefaultConfig()
				c.Adapters.GitHub.Enabled = true
				return c
			}(),
			wantWarnings: 0,
		},
		{
			name: "GitHubUseSDKPollerTrueAndEnabled",
			config: func() *Config {
				c := DefaultConfig()
				c.Adapters.GitHub.Enabled = true
				c.Adapters.GitHub.UseSDKPoller = true
				return c
			}(),
			wantWarnings: 1,
			wantContains: "use_sdk_poller is deprecated and ignored",
		},
		{
			name: "GitHubAdapterDisabled",
			config: func() *Config {
				c := DefaultConfig()
				c.Adapters.GitHub.Enabled = false
				return c
			}(),
			wantWarnings: 0,
		},
		{
			name: "NilGitHubConfig",
			config: func() *Config {
				c := DefaultConfig()
				c.Adapters.GitHub = nil
				return c
			}(),
			wantWarnings: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings := tt.config.CheckDeprecations()
			if len(warnings) != tt.wantWarnings {
				t.Errorf("CheckDeprecations() returned %d warnings, want %d", len(warnings), tt.wantWarnings)
			}
			if tt.wantContains != "" && len(warnings) > 0 {
				found := false
				for _, w := range warnings {
					if contains(w, tt.wantContains) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("CheckDeprecations() warnings %v should contain %q", warnings, tt.wantContains)
				}
			}
		})
	}
}

func TestLoadWithDeprecatedTimeField(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Config using deprecated time field
	configContent := `
version: "1.0"
orchestrator:
  daily_brief:
    enabled: true
    time: "09:00"
    timezone: "America/New_York"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	config, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify the deprecated field was loaded
	if config.Orchestrator.DailyBrief.Time != "09:00" {
		t.Errorf("DailyBrief.Time = %q, want %q", config.Orchestrator.DailyBrief.Time, "09:00")
	}

	// Verify deprecation warning is generated
	warnings := config.CheckDeprecations()
	if len(warnings) != 1 {
		t.Errorf("Expected 1 deprecation warning, got %d", len(warnings))
	}
}

// TestLoadWithDeprecatedUseSDKPoller covers GH-4171/GH-4206: adapters.github.use_sdk_poller
// is still parsed (existing configs must keep loading without error) but now only
// produces a startup deprecation warning when explicitly set to true — it no longer
// changes daemon behavior either way.
func TestLoadWithDeprecatedUseSDKPoller(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
version: "1.0"
adapters:
  github:
    enabled: true
    repo: "owner/repo"
    use_sdk_poller: true
    polling:
      enabled: true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	config, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// The field is still parsed off the YAML.
	if config.Adapters.GitHub.UseSDKPoller != true {
		t.Errorf("Adapters.GitHub.UseSDKPoller = %v, want true", config.Adapters.GitHub.UseSDKPoller)
	}

	warnings := config.CheckDeprecations()
	if len(warnings) != 1 {
		t.Fatalf("Expected 1 deprecation warning, got %d: %v", len(warnings), warnings)
	}
	if !contains(warnings[0], "use_sdk_poller is deprecated and ignored") {
		t.Errorf("warning %q should mention use_sdk_poller is deprecated and ignored", warnings[0])
	}
}

// TestLoadWithAbsentUseSDKPoller covers GH-4206: a config that never set
// use_sdk_poller (the common case for every deployment created after GH-4171)
// must not trigger the deprecation warning just because the GitHub adapter
// is enabled.
func TestLoadWithAbsentUseSDKPoller(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
version: "1.0"
adapters:
  github:
    enabled: true
    repo: "owner/repo"
    polling:
      enabled: true
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	config, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if config.Adapters.GitHub.UseSDKPoller != false {
		t.Errorf("Adapters.GitHub.UseSDKPoller = %v, want false (unset)", config.Adapters.GitHub.UseSDKPoller)
	}

	warnings := config.CheckDeprecations()
	if len(warnings) != 0 {
		t.Fatalf("Expected 0 deprecation warnings for absent use_sdk_poller, got %d: %v", len(warnings), warnings)
	}
}

func TestLoadTeamConfig(t *testing.T) {
	t.Run("team config from YAML", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
team:
  enabled: true
  team_id: "my-team"
  member_email: "dev@example.com"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if cfg.Team == nil {
			t.Fatal("Team config should not be nil")
		}
		if !cfg.Team.Enabled {
			t.Error("Team.Enabled should be true")
		}
		if cfg.Team.TeamID != "my-team" {
			t.Errorf("Team.TeamID = %q, want %q", cfg.Team.TeamID, "my-team")
		}
		if cfg.Team.MemberEmail != "dev@example.com" {
			t.Errorf("Team.MemberEmail = %q, want %q", cfg.Team.MemberEmail, "dev@example.com")
		}
	})

	t.Run("team config absent defaults to nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `version: "1.0"`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if cfg.Team != nil {
			t.Errorf("Team config should be nil when not configured, got %+v", cfg.Team)
		}
	})

	t.Run("team config disabled", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
team:
  enabled: false
  team_id: "my-team"
  member_email: "dev@example.com"
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if cfg.Team == nil {
			t.Fatal("Team config should not be nil")
		}
		if cfg.Team.Enabled {
			t.Error("Team.Enabled should be false")
		}
	})
}

func TestFindProjectByRepo(t *testing.T) {
	cfg := &Config{
		Projects: []*ProjectConfig{
			{
				Name:          "app-one",
				Reviewers:     []string{"alice", "bob"},
				TeamReviewers: []string{"backend-team"},
				GitHub: &ProjectGitHubConfig{
					Owner: "my-org",
					Repo:  "app-one",
				},
			},
			{
				Name: "app-two",
				GitHub: &ProjectGitHubConfig{
					Owner: "my-org",
					Repo:  "app-two",
				},
			},
			{
				Name: "no-github",
			},
		},
	}

	t.Run("found with reviewers", func(t *testing.T) {
		proj := cfg.FindProjectByRepo("my-org/app-one")
		if proj == nil {
			t.Fatal("expected project, got nil")
		}
		if proj.Name != "app-one" {
			t.Errorf("Name = %s, want app-one", proj.Name)
		}
		if len(proj.Reviewers) != 2 {
			t.Errorf("Reviewers count = %d, want 2", len(proj.Reviewers))
		}
		if len(proj.TeamReviewers) != 1 {
			t.Errorf("TeamReviewers count = %d, want 1", len(proj.TeamReviewers))
		}
	})

	t.Run("found without reviewers", func(t *testing.T) {
		proj := cfg.FindProjectByRepo("my-org/app-two")
		if proj == nil {
			t.Fatal("expected project, got nil")
		}
		if len(proj.Reviewers) != 0 {
			t.Errorf("Reviewers count = %d, want 0", len(proj.Reviewers))
		}
	})

	t.Run("not found", func(t *testing.T) {
		proj := cfg.FindProjectByRepo("other-org/other-repo")
		if proj != nil {
			t.Errorf("expected nil, got %v", proj)
		}
	})

	t.Run("empty config", func(t *testing.T) {
		empty := &Config{}
		proj := empty.FindProjectByRepo("my-org/app-one")
		if proj != nil {
			t.Errorf("expected nil, got %v", proj)
		}
	})
}

// TestResolveProjectBoard covers the GH-4472 per-project board resolution
// precedence: project override → default-repo global fallback → none.
func TestResolveProjectBoard(t *testing.T) {
	globalBoard := &github.ProjectBoardConfig{Enabled: true, ProjectNumber: 2}
	projectBoard := &github.ProjectBoardConfig{Enabled: true, ProjectNumber: 1}

	cfg := &Config{
		Adapters: &AdaptersConfig{
			GitHub: &github.Config{
				Repo:         "acme/default",
				ProjectBoard: globalBoard,
			},
		},
		Projects: []*ProjectConfig{
			{
				Name:   "with-board",
				GitHub: &ProjectGitHubConfig{Owner: "acme", Repo: "with-board", ProjectBoard: projectBoard},
			},
			{
				Name:   "no-board",
				GitHub: &ProjectGitHubConfig{Owner: "acme", Repo: "no-board"},
			},
		},
	}

	tests := []struct {
		name          string
		ownerRepo     string
		isDefaultRepo bool
		want          *github.ProjectBoardConfig
	}{
		{
			name:          "project with its own board, non-default repo",
			ownerRepo:     "acme/with-board",
			isDefaultRepo: false,
			want:          projectBoard,
		},
		{
			name:          "project without a board, non-default repo, no fallback",
			ownerRepo:     "acme/no-board",
			isDefaultRepo: false,
			want:          nil,
		},
		{
			name:          "default repo with no project entry falls back to global",
			ownerRepo:     "acme/default",
			isDefaultRepo: true,
			want:          globalBoard,
		},
		{
			name:          "unknown repo, not default, no board",
			ownerRepo:     "acme/unknown",
			isDefaultRepo: false,
			want:          nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ResolveProjectBoard(tt.ownerRepo, tt.isDefaultRepo)
			if got != tt.want {
				t.Errorf("ResolveProjectBoard(%q, %v) = %v, want %v", tt.ownerRepo, tt.isDefaultRepo, got, tt.want)
			}
		})
	}

	t.Run("nil adapters config, default repo, no panic", func(t *testing.T) {
		empty := &Config{}
		if got := empty.ResolveProjectBoard("acme/default", true); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

// TestProjectConfigResolveBaseBranch covers the BranchFrom / DefaultBranch
// precedence introduced in GH-2290.
func TestProjectConfigResolveBaseBranch(t *testing.T) {
	tests := []struct {
		name string
		p    *ProjectConfig
		want string
	}{
		{"nil receiver", nil, ""},
		{"both empty", &ProjectConfig{}, ""},
		{"default_branch only", &ProjectConfig{DefaultBranch: "dev"}, "dev"},
		{"branch_from only", &ProjectConfig{BranchFrom: "dev"}, "dev"},
		{
			"branch_from wins over default_branch",
			&ProjectConfig{DefaultBranch: "main", BranchFrom: "dev"},
			"dev",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.ResolveBaseBranch(); got != tt.want {
				t.Errorf("ResolveBaseBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindProjectByPath(t *testing.T) {
	cfg := &Config{
		Projects: []*ProjectConfig{
			{Name: "a", Path: "/tmp/a", DefaultBranch: "dev"},
			{Name: "b", Path: "/tmp/b"},
		},
	}
	if p := cfg.FindProjectByPath("/tmp/a"); p == nil || p.Name != "a" {
		t.Errorf("FindProjectByPath(/tmp/a) = %v, want project a", p)
	}
	if p := cfg.FindProjectByPath("/tmp/missing"); p != nil {
		t.Errorf("FindProjectByPath(missing) = %v, want nil", p)
	}
	if p := cfg.FindProjectByPath(""); p != nil {
		t.Errorf("FindProjectByPath(\"\") = %v, want nil", p)
	}
}

func TestProjectConfigReviewersYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
version: "1.0"
projects:
  - name: "my-app"
    path: "/tmp/my-app"
    reviewers:
      - alice
      - bob
    team_reviewers:
      - backend-team
    github:
      owner: "my-org"
      repo: "my-app"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Projects) != 1 {
		t.Fatalf("Projects count = %d, want 1", len(cfg.Projects))
	}

	proj := cfg.Projects[0]
	if len(proj.Reviewers) != 2 || proj.Reviewers[0] != "alice" || proj.Reviewers[1] != "bob" {
		t.Errorf("Reviewers = %v, want [alice bob]", proj.Reviewers)
	}
	if len(proj.TeamReviewers) != 1 || proj.TeamReviewers[0] != "backend-team" {
		t.Errorf("TeamReviewers = %v, want [backend-team]", proj.TeamReviewers)
	}
}

// TestSave_PermissionsAre0600 asserts that Save() writes the config file
// with 0600 permissions and its parent directory with 0700 — required
// because the file stores GitHub PAT, Linear API key, Slack bot token,
// and (optionally) Anthropic API key. TASK-290.
//
// Skipped on Windows where Unix file modes don't apply.
func TestSave_PermissionsAre0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	configDir := filepath.Join(tmpDir, ".pilot")
	configPath := filepath.Join(configDir, "config.yaml")

	cfg := &Config{Version: "1.0"}
	if err := Save(cfg, configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat config file failed: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("config file mode = %o, want 0600", got)
	}

	dirInfo, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("Stat config dir failed: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf("config dir mode = %o, want 0700", got)
	}
}

// TestBotConfigRoundTrip verifies that a bot: YAML block parses into the
// expected BotConfig struct, including the model default when omitted (GH-3667).
func TestBotConfigRoundTrip(t *testing.T) {
	t.Run("full bot block", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `
version: "1.0"
bot:
  enabled: true
  model: "claude-haiku-4-5-20251001"
  answer_model: "claude-sonnet-4-6"
  api_key: "test-api-key"
  persona: "You are a helpful assistant."
`
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if cfg.Bot == nil {
			t.Fatal("Bot config should not be nil")
		}
		if !cfg.Bot.Enabled {
			t.Error("Bot.Enabled should be true")
		}
		if cfg.Bot.Model != "claude-haiku-4-5-20251001" {
			t.Errorf("Bot.Model = %q, want claude-haiku-4-5-20251001", cfg.Bot.Model)
		}
		if cfg.Bot.AnswerModel != "claude-sonnet-4-6" {
			t.Errorf("Bot.AnswerModel = %q, want claude-sonnet-4-6", cfg.Bot.AnswerModel)
		}
		if cfg.Bot.APIKey != "test-api-key" {
			t.Errorf("Bot.APIKey = %q, want test-api-key", cfg.Bot.APIKey)
		}
		if cfg.Bot.Persona != "You are a helpful assistant." {
			t.Errorf("Bot.Persona = %q, want \"You are a helpful assistant.\"", cfg.Bot.Persona)
		}
	})

	t.Run("model defaults to haiku when omitted", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `
version: "1.0"
bot:
  enabled: true
  persona: "Minimal bot."
`
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if cfg.Bot == nil {
			t.Fatal("Bot config should not be nil")
		}
		if cfg.Bot.Model != "claude-haiku-4-5-20251001" {
			t.Errorf("Bot.Model = %q, want claude-haiku-4-5-20251001 (default)", cfg.Bot.Model)
		}
	})

	t.Run("bot absent defaults to nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		if err := os.WriteFile(configPath, []byte("version: \"1.0\"\n"), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if cfg.Bot != nil {
			t.Errorf("Bot config should be nil when not configured, got %+v", cfg.Bot)
		}
	})

	t.Run("stub fields parse without error", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		content := `
version: "1.0"
bot:
  enabled: false
  retrieval: {}
  issue_intake: {}
  voice: {}
`
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load with stub fields: %v", err)
		}

		if cfg.Bot == nil {
			t.Fatal("Bot config should not be nil")
		}
		// Stub fields are empty structs — no fields to assert, just confirm no panic/error.
		_ = cfg.Bot.Retrieval
		_ = cfg.Bot.IssueIntake
		_ = cfg.Bot.Voice
	})
}

// TestSave_TightensExistingLoosePerms asserts that Save() rewrites a file
// that previously existed with 0644 down to 0600. Covers the migration path
// for users upgrading past TASK-290 with an existing 0644 config on disk.
func TestSave_TightensExistingLoosePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Pre-seed file at 0644 (the old default).
	if err := os.WriteFile(configPath, []byte("version: \"1.0\"\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cfg := &Config{Version: "1.0"}
	if err := Save(cfg, configPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat after Save failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("config file mode after rewrite = %o, want 0600", got)
	}
}

// TestLoadMemoryInjectionConfig covers TASK-387's config plumbing:
// executor.memory_injection defaults to Enabled=true/MaxMemories=5 when
// omitted, and explicit YAML values override those defaults.
func TestLoadMemoryInjectionConfig(t *testing.T) {
	t.Run("defaults when section omitted", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		if err := os.WriteFile(configPath, []byte(`version: "1.0"`), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if cfg.Executor == nil || cfg.Executor.MemoryInjection == nil {
			t.Fatal("Executor.MemoryInjection should not be nil")
		}
		if !cfg.Executor.MemoryInjection.Enabled {
			t.Error("MemoryInjection.Enabled should default to true")
		}
		if cfg.Executor.MemoryInjection.MaxMemories != 5 {
			t.Errorf("MemoryInjection.MaxMemories = %d, want 5", cfg.Executor.MemoryInjection.MaxMemories)
		}
	})

	t.Run("explicit values override defaults", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
executor:
  memory_injection:
    enabled: false
    max_memories: 3
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if cfg.Executor == nil || cfg.Executor.MemoryInjection == nil {
			t.Fatal("Executor.MemoryInjection should not be nil")
		}
		if cfg.Executor.MemoryInjection.Enabled {
			t.Error("MemoryInjection.Enabled should be false")
		}
		if cfg.Executor.MemoryInjection.MaxMemories != 3 {
			t.Errorf("MemoryInjection.MaxMemories = %d, want 3", cfg.Executor.MemoryInjection.MaxMemories)
		}
	})

	t.Run("partial override keeps other default", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")

		configContent := `
version: "1.0"
executor:
  memory_injection:
    enabled: false
`
		if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		cfg, err := Load(configPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		if cfg.Executor.MemoryInjection.Enabled {
			t.Error("MemoryInjection.Enabled should be false")
		}
		if cfg.Executor.MemoryInjection.MaxMemories != 5 {
			t.Errorf("MemoryInjection.MaxMemories = %d, want default 5 to survive partial override", cfg.Executor.MemoryInjection.MaxMemories)
		}
	})
}
