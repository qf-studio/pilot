package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/gateway"
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
		if config.Orchestrator.Model != "claude-sonnet-4-5-20250929" {
			t.Errorf("Orchestrator.Model = %q, want %q", config.Orchestrator.Model, "claude-sonnet-4-5-20250929")
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
		if exec.Mode != "sequential" {
			t.Errorf("Execution.Mode = %q, want %q", exec.Mode, "sequential")
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
