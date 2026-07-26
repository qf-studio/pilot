package config

import (
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/quality"
)

// baseValidConfig returns a minimal valid config for testing
func baseValidConfig() *Config {
	return &Config{
		Gateway: &gateway.Config{
			Host: "127.0.0.1",
			Port: 9091,
		},
		Projects: []*ProjectConfig{
			{Name: "test", Path: "/tmp/test"},
		},
	}
}

func TestConfig_Validate_EffortRouting(t *testing.T) {
	tests := []struct {
		name      string
		effort    *executor.EffortRoutingConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "nil config is valid",
			effort:  nil,
			wantErr: false,
		},
		{
			name: "disabled routing skips validation",
			effort: &executor.EffortRoutingConfig{
				Enabled: false,
				Complex: "extreme", // Invalid but disabled
			},
			wantErr: false,
		},
		{
			name: "valid effort levels",
			effort: &executor.EffortRoutingConfig{
				Enabled: true,
				Trivial: "low",
				Simple:  "medium",
				Medium:  "high",
				Complex: "high",
			},
			wantErr: false,
		},
		{
			name: "empty values are valid",
			effort: &executor.EffortRoutingConfig{
				Enabled: true,
				Trivial: "",
				Simple:  "",
				Medium:  "",
				Complex: "",
			},
			wantErr: false,
		},
		{
			name: "max is valid",
			effort: &executor.EffortRoutingConfig{
				Enabled: true,
				Trivial: "low",
				Simple:  "medium",
				Medium:  "high",
				Complex: "max",
			},
			wantErr: false,
		},
		{
			name: "invalid trivial",
			effort: &executor.EffortRoutingConfig{
				Enabled: true,
				Trivial: "super",
			},
			wantErr:   true,
			errSubstr: "effort_routing.trivial",
		},
		{
			name: "case insensitive",
			effort: &executor.EffortRoutingConfig{
				Enabled: true,
				Trivial: "LOW",
				Simple:  "Medium",
				Medium:  "HIGH",
				Complex: "high",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Executor = &executor.BackendConfig{
				EffortRouting: tt.effort,
			}

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestConfig_Validate_Projects(t *testing.T) {
	tests := []struct {
		name           string
		projects       []*ProjectConfig
		defaultProject string
		wantErr        bool
		errSubstr      string
	}{
		{
			name: "valid projects",
			projects: []*ProjectConfig{
				{Name: "pilot", Path: "/home/user/pilot"},
			},
			defaultProject: "pilot",
			wantErr:        false,
		},
		{
			name:           "no projects is allowed",
			projects:       nil,
			defaultProject: "",
			wantErr:        false,
		},
		{
			name: "default project not found",
			projects: []*ProjectConfig{
				{Name: "pilot", Path: "/home/user/pilot"},
			},
			defaultProject: "other",
			wantErr:        true,
			errSubstr:      "default_project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Projects = tt.projects
			cfg.DefaultProject = tt.defaultProject

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidEffortLevels(t *testing.T) {
	valid := []string{"low", "medium", "high", "max", ""}
	invalid := []string{"super", "extreme", "none", "default"}

	for _, v := range valid {
		if !validEffortLevels[v] {
			t.Errorf("expected %q to be valid", v)
		}
	}

	for _, v := range invalid {
		if validEffortLevels[v] {
			t.Errorf("expected %q to be invalid", v)
		}
	}
}

// GH-1124: Test bounds and orchestrator validation
func TestConfig_Validate_OrchestratorBounds(t *testing.T) {
	tests := []struct {
		name         string
		orchestrator *OrchestratorConfig
		wantErr      bool
		errSubstr    string
	}{
		{
			name:         "nil orchestrator is valid",
			orchestrator: nil,
			wantErr:      false,
		},
		{
			name: "max_concurrent = 1 is valid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 1,
			},
			wantErr: false,
		},
		{
			name: "max_concurrent > 1 is valid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 5,
			},
			wantErr: false,
		},
		{
			name: "max_concurrent = 0 is invalid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 0,
			},
			wantErr:   true,
			errSubstr: "orchestrator.max_concurrent must be >= 1",
		},
		{
			name: "max_concurrent < 0 is invalid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: -1,
			},
			wantErr:   true,
			errSubstr: "orchestrator.max_concurrent must be >= 1",
		},
		{
			name: "sequential execution mode is valid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 2,
				Execution: &ExecutionConfig{
					Mode: "sequential",
				},
			},
			wantErr: false,
		},
		{
			name: "parallel execution mode is valid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 2,
				Execution: &ExecutionConfig{
					Mode: "parallel",
				},
			},
			wantErr: false,
		},
		{
			name: "auto execution mode is valid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 2,
				Execution: &ExecutionConfig{
					Mode: "auto",
				},
			},
			wantErr: false,
		},
		{
			name: "invalid execution mode",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 2,
				Execution: &ExecutionConfig{
					Mode: "invalid",
				},
			},
			wantErr:   true,
			errSubstr: "orchestrator.execution.mode must be 'sequential', 'parallel', or 'auto'",
		},
		{
			name: "empty execution mode is invalid",
			orchestrator: &OrchestratorConfig{
				MaxConcurrent: 2,
				Execution: &ExecutionConfig{
					Mode: "",
				},
			},
			wantErr:   true,
			errSubstr: "orchestrator.execution.mode must be 'sequential', 'parallel', or 'auto'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Orchestrator = tt.orchestrator

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestConfig_Validate_AutopilotDefaultEnvironment verifies that the
// top-level Config.Validate() wires through to autopilot.Config.Validate()
// (GH-4546): an unknown orchestrator.autopilot.default_environment must be
// a startup error listing the available environment keys, rather than
// silently falling back at runtime.
func TestConfig_Validate_AutopilotDefaultEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		autopilot *autopilot.Config
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "nil autopilot config is valid",
			autopilot: nil,
			wantErr:   false,
		},
		{
			name:      "empty default_environment is valid",
			autopilot: &autopilot.Config{},
			wantErr:   false,
		},
		{
			name: "default_environment matching a built-in is valid",
			autopilot: &autopilot.Config{
				DefaultEnvironment: "prod",
			},
			wantErr: false,
		},
		{
			name: "default_environment matching a custom environments key is valid",
			autopilot: &autopilot.Config{
				DefaultEnvironment: "canary",
				Environments: map[string]*autopilot.EnvironmentConfig{
					"canary": {Branch: "canary"},
				},
			},
			wantErr: false,
		},
		{
			name: "unknown default_environment is a startup error listing available keys",
			autopilot: &autopilot.Config{
				DefaultEnvironment: "typo-env",
				Environments: map[string]*autopilot.EnvironmentConfig{
					"canary": {Branch: "canary"},
				},
			},
			wantErr:   true,
			errSubstr: `default_environment "typo-env" is not a known environment`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Orchestrator = &OrchestratorConfig{MaxConcurrent: 1, Autopilot: tt.autopilot}

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestConfig_Validate_QualityBounds(t *testing.T) {
	tests := []struct {
		name      string
		quality   *quality.Config
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "nil quality config is valid",
			quality: nil,
			wantErr: false,
		},
		{
			name: "max_retries = 0 is valid",
			quality: &quality.Config{
				OnFailure: quality.FailureConfig{
					MaxRetries: 0,
				},
			},
			wantErr: false,
		},
		{
			name: "max_retries = 10 is valid",
			quality: &quality.Config{
				OnFailure: quality.FailureConfig{
					MaxRetries: 10,
				},
			},
			wantErr: false,
		},
		{
			name: "max_retries = 5 is valid",
			quality: &quality.Config{
				OnFailure: quality.FailureConfig{
					MaxRetries: 5,
				},
			},
			wantErr: false,
		},
		{
			name: "max_retries = 11 is invalid",
			quality: &quality.Config{
				OnFailure: quality.FailureConfig{
					MaxRetries: 11,
				},
			},
			wantErr:   true,
			errSubstr: "quality.on_failure.max_retries must be in range [0, 10]",
		},
		{
			name: "max_retries = -1 is invalid",
			quality: &quality.Config{
				OnFailure: quality.FailureConfig{
					MaxRetries: -1,
				},
			},
			wantErr:   true,
			errSubstr: "quality.on_failure.max_retries must be in range [0, 10]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Quality = tt.quality

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestConfig_Validate_BudgetBounds(t *testing.T) {
	tests := []struct {
		name      string
		budget    *budget.Config
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "nil budget config is valid",
			budget:  nil,
			wantErr: false,
		},
		{
			name: "disabled budget with zero daily_limit is valid",
			budget: &budget.Config{
				Enabled:    false,
				DailyLimit: 0,
			},
			wantErr: false,
		},
		{
			name: "disabled budget with negative daily_limit is valid",
			budget: &budget.Config{
				Enabled:    false,
				DailyLimit: -10,
			},
			wantErr: false,
		},
		{
			name: "enabled budget with positive daily_limit is valid",
			budget: &budget.Config{
				Enabled:    true,
				DailyLimit: 50.0,
			},
			wantErr: false,
		},
		{
			name: "enabled budget with zero daily_limit is invalid",
			budget: &budget.Config{
				Enabled:    true,
				DailyLimit: 0,
			},
			wantErr:   true,
			errSubstr: "budget.daily_limit must be > 0 when budget is enabled",
		},
		{
			name: "enabled budget with negative daily_limit is invalid",
			budget: &budget.Config{
				Enabled:    true,
				DailyLimit: -10.5,
			},
			wantErr:   true,
			errSubstr: "budget.daily_limit must be > 0 when budget is enabled",
		},
		{
			name: "enabled budget with very small positive daily_limit is valid",
			budget: &budget.Config{
				Enabled:    true,
				DailyLimit: 0.01,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.Budget = tt.budget

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// GH-3930: Validate release.publish enum at the global, per-environment, and
// per-project overlay levels.
func TestConfig_Validate_ReleasePublish(t *testing.T) {
	tests := []struct {
		name      string
		buildCfg  func() *Config
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid empty publish at all levels",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Publish: ""},
						Environments: map[string]*autopilot.EnvironmentConfig{
							"prod": {Release: &autopilot.ReleaseConfig{Publish: ""}},
						},
					},
				}
				cfg.Projects[0].Release = &autopilot.ProjectReleaseConfig{Publish: ""}
				return cfg
			},
			wantErr: false,
		},
		{
			name: "valid workflow publish at global level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Publish: "workflow"},
					},
				}
				return cfg
			},
			wantErr: false,
		},
		{
			name: "valid api publish at global level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Publish: "api"},
					},
				}
				return cfg
			},
			wantErr: false,
		},
		{
			name: "valid tag_only publish at global level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Publish: "tag_only"},
					},
				}
				return cfg
			},
			wantErr: false,
		},
		{
			name: "invalid publish at global level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Publish: "bogus"},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.release.publish must be "workflow", "api", or "tag_only"`,
		},
		{
			name: "invalid publish at environment level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Environments: map[string]*autopilot.EnvironmentConfig{
							"prod": {Release: &autopilot.ReleaseConfig{Publish: "bogus"}},
						},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.environments[prod].release.publish must be "workflow", "api", or "tag_only"`,
		},
		{
			name: "invalid publish at project overlay level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Projects[0].Release = &autopilot.ProjectReleaseConfig{Publish: "bogus"}
				return cfg
			},
			wantErr:   true,
			errSubstr: `projects[0].release.publish must be "workflow", "api", or "tag_only"`,
		},
		{
			name: "project overlay sets only publish, inherits enabled from global",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Enabled: true, Publish: "workflow"},
					},
				}
				// Only Publish is set on the overlay — Enabled is left nil so
				// it inherits the global Enabled: true above (GH-3930).
				cfg.Projects[0].Release = &autopilot.ProjectReleaseConfig{Publish: "api"}
				return cfg
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.buildCfg()

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}

			if tt.name == "project overlay sets only publish, inherits enabled from global" {
				if cfg.Projects[0].Release.Enabled != nil {
					t.Errorf("expected overlay Enabled to remain nil (inherit), got %v", cfg.Projects[0].Release.Enabled)
				}
				if !cfg.Orchestrator.Autopilot.Release.Enabled {
					t.Errorf("expected global Enabled to remain true")
				}
			}
		})
	}
}

// GH-3989: Validate release.trigger enum + schedule/schedule_timezone
// parseability at the global, per-environment, and per-project overlay
// levels — mirrors TestConfig_Validate_ReleasePublish's structure.
func TestConfig_Validate_ReleaseTrigger(t *testing.T) {
	tests := []struct {
		name      string
		buildCfg  func() *Config
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid triggers at all levels",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "on_scope_close"},
						Environments: map[string]*autopilot.EnvironmentConfig{
							"prod": {Release: &autopilot.ReleaseConfig{Trigger: "manual"}},
						},
					},
				}
				cfg.Projects[0].Release = &autopilot.ProjectReleaseConfig{Trigger: ""}
				return cfg
			},
			wantErr: false,
		},
		{
			name: "invalid trigger at global level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "bogus"},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.release.trigger must be "", "on_merge", "manual", "on_scope_close", or "on_schedule"`,
		},
		{
			name: "invalid trigger at environment level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Environments: map[string]*autopilot.EnvironmentConfig{
							"prod": {Release: &autopilot.ReleaseConfig{Trigger: "bogus"}},
						},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.environments[prod].release.trigger must be`,
		},
		{
			name: "invalid trigger at project overlay level",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Projects[0].Release = &autopilot.ProjectReleaseConfig{Trigger: "bogus"}
				return cfg
			},
			wantErr:   true,
			errSubstr: `projects[0].release.trigger must be`,
		},
		{
			name: "on_schedule without schedule is rejected",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "on_schedule"},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.release.schedule is required when trigger is "on_schedule"`,
		},
		{
			name: "on_schedule with invalid cron expression is rejected",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "on_schedule", Schedule: "not a cron"},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.release.schedule "not a cron" is not a valid cron expression`,
		},
		{
			name: "on_schedule with valid cron expression passes",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "on_schedule", Schedule: "0 21 * * FRI"},
					},
				}
				return cfg
			},
			wantErr: false,
		},
		{
			name: "invalid schedule_timezone is rejected",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "on_schedule", Schedule: "0 21 * * FRI", ScheduleTimezone: "Not/A_Zone"},
					},
				}
				return cfg
			},
			wantErr:   true,
			errSubstr: `orchestrator.autopilot.release.schedule_timezone "Not/A_Zone" is invalid`,
		},
		{
			name: "valid schedule_timezone passes",
			buildCfg: func() *Config {
				cfg := baseValidConfig()
				cfg.Orchestrator = &OrchestratorConfig{
					MaxConcurrent: 1,
					Autopilot: &autopilot.Config{
						Release: &autopilot.ReleaseConfig{Trigger: "on_schedule", Schedule: "0 21 * * FRI", ScheduleTimezone: "Europe/Berlin"},
					},
				}
				return cfg
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.buildCfg()
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
