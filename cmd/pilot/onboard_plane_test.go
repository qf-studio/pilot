package main

import (
	"bufio"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/testutil"
)

// TestOnboardPlaneTickets drives onboardPlaneTickets with scripted input and a
// fake connection test (GH-6755).
func TestOnboardPlaneTickets(t *testing.T) {
	key := testutil.FakePlaneAPIKey

	tests := []struct {
		name        string
		input       []string
		validateErr error
		wantEnabled bool
		wantCalls   int
		check       func(t *testing.T, cfg *plane.Config, out string)
	}{
		{
			name: "full run",
			input: []string{
				"  https://plane.example.test  ", // base URL
				"  " + key + "  ",                // API key
				"  my-team ",                     // workspace slug
				" proj-1 , ,proj-2 ,",            // project IDs
				"",                               // label: default
			},
			wantEnabled: true,
			wantCalls:   1,
			check: func(t *testing.T, cfg *plane.Config, out string) {
				if cfg.BaseURL != "https://plane.example.test" {
					t.Errorf("BaseURL = %q", cfg.BaseURL)
				}
				if cfg.APIKey != key {
					t.Errorf("APIKey = %q, want %q", cfg.APIKey, key)
				}
				if cfg.WorkspaceSlug != "my-team" {
					t.Errorf("WorkspaceSlug = %q", cfg.WorkspaceSlug)
				}
				if len(cfg.ProjectIDs) != 2 || cfg.ProjectIDs[0] != "proj-1" || cfg.ProjectIDs[1] != "proj-2" {
					t.Errorf("ProjectIDs = %q, want [proj-1 proj-2]", cfg.ProjectIDs)
				}
				if cfg.PilotLabel != "pilot" {
					t.Errorf("PilotLabel = %q, want pilot", cfg.PilotLabel)
				}
				if cfg.Polling == nil || !cfg.Polling.Enabled || cfg.Polling.Interval != 30*time.Second {
					t.Errorf("Polling = %+v, want enabled with 30s", cfg.Polling)
				}
				if !strings.Contains(out, "polling 2 project(s)") {
					t.Errorf("output lacks the project count:\n%s", out)
				}
			},
		},
		{
			name:        "default base URL kept on empty answer",
			input:       []string{"", key, "my-team", "proj-1", "triage"},
			wantEnabled: true,
			wantCalls:   1,
			check: func(t *testing.T, cfg *plane.Config, _ string) {
				if cfg.BaseURL != plane.DefaultConfig().BaseURL {
					t.Errorf("BaseURL = %q, want default", cfg.BaseURL)
				}
				if cfg.PilotLabel != "triage" {
					t.Errorf("PilotLabel = %q, want triage", cfg.PilotLabel)
				}
			},
		},
		{
			name:        "empty key skips",
			input:       []string{"", ""},
			wantEnabled: false,
			wantCalls:   0,
			check: func(t *testing.T, _ *plane.Config, out string) {
				if !strings.Contains(out, "Skipped - no API key provided") {
					t.Errorf("output lacks the skip line:\n%s", out)
				}
			},
		},
		{
			name:        "empty slug twice skips",
			input:       []string{"", key, "", ""},
			wantEnabled: false,
			wantCalls:   0,
		},
		{
			name:        "slug given on second ask",
			input:       []string{"", key, "", "my-team", "proj-1", ""},
			wantEnabled: true,
			wantCalls:   1,
			check: func(t *testing.T, cfg *plane.Config, _ string) {
				if cfg.WorkspaceSlug != "my-team" {
					t.Errorf("WorkspaceSlug = %q", cfg.WorkspaceSlug)
				}
			},
		},
		{
			name:        "no project ID skips",
			input:       []string{"", key, "my-team", " , "},
			wantEnabled: false,
			wantCalls:   0,
		},
		{
			name:        "failed connection test then skip",
			input:       []string{"", key, "my-team", "proj-1", "", "3"},
			validateErr: errors.New("plane check failed: 401 unauthorized"),
			wantEnabled: false,
			wantCalls:   1,
			check: func(t *testing.T, _ *plane.Config, out string) {
				if !strings.Contains(out, "401 unauthorized") {
					t.Errorf("output lacks the error text:\n%s", out)
				}
				if !strings.Contains(out, "Skipped Plane") {
					t.Errorf("output lacks the skip line:\n%s", out)
				}
			},
		},
		{
			name:        "failed run never prints the key",
			input:       []string{"", key, "my-team", "proj-1", "", "3"},
			validateErr: errors.New("plane check failed: bad key " + key),
			wantEnabled: false,
			wantCalls:   1,
			check: func(t *testing.T, _ *plane.Config, out string) {
				if strings.Contains(out, key) {
					t.Errorf("output holds the API key:\n%s", out)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			orig := planeConnValidator
			planeConnValidator = func(*plane.Config) error {
				calls++
				return tt.validateErr
			}
			t.Cleanup(func() { planeConnValidator = orig })

			state := &OnboardState{
				Persona: PersonaEnterprise,
				Config:  config.DefaultConfig(),
				Reader:  bufio.NewReader(strings.NewReader(strings.Join(tt.input, "\n") + "\n")),
			}

			var runErr error
			out := captureStdout(func() {
				runErr = onboardPlaneTickets(state)
			})
			if runErr != nil {
				t.Fatalf("onboardPlaneTickets() error = %v", runErr)
			}

			cfg := state.Config.Adapters.Plane
			if cfg == nil {
				t.Fatal("Plane config is nil")
			}
			if cfg.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", cfg.Enabled, tt.wantEnabled)
			}
			if calls != tt.wantCalls {
				t.Errorf("connection test calls = %d, want %d", calls, tt.wantCalls)
			}
			if tt.check != nil {
				tt.check(t, cfg, out)
			}
		})
	}
}
