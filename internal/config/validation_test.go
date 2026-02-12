package config

import (
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/gateway"
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
				Complex: "max", // Invalid but disabled
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
			name: "max is invalid",
			effort: &executor.EffortRoutingConfig{
				Enabled: true,
				Trivial: "low",
				Simple:  "medium",
				Medium:  "high",
				Complex: "max",
			},
			wantErr:   true,
			errSubstr: "effort_routing.complex",
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
	valid := []string{"low", "medium", "high", ""}
	invalid := []string{"max", "super", "extreme", "none", "default"}

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
