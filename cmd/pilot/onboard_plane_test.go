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

// TestOnboardPlaneProjectPairing drives onboardPlaneProjectPairing with
// scripted input (GH-6764).
func TestOnboardPlaneProjectPairing(t *testing.T) {
	tests := []struct {
		name     string
		projects []string          // project entry names, config order
		planeIDs []string          // polled Plane project IDs
		before   map[string]string // pairings of before: entry -> Plane ID
		input    []string
		want     map[string]string // pairings after: entry -> Plane ID
		wantAsk  bool
		wantOut  string
	}{
		{
			name:     "one project entry",
			projects: []string{"api"},
			planeIDs: []string{"plane-a", "plane-b"},
			input:    []string{"1"},
			want:     map[string]string{},
		},
		{
			name:     "two entries and one Plane project",
			projects: []string{"api", "web"},
			planeIDs: []string{"plane-a"},
			input:    []string{"1", "1"},
			want:     map[string]string{},
		},
		{
			name:     "two valid choices",
			projects: []string{"api", "web"},
			planeIDs: []string{"plane-a", "plane-b"},
			input:    []string{"2", "1"},
			want:     map[string]string{"api": "plane-b", "web": "plane-a"},
			wantAsk:  true,
		},
		{
			name:     "taken project is not offered",
			projects: []string{"api", "web"},
			planeIDs: []string{"plane-a", "plane-b"},
			input:    []string{"1", "1", "2"},
			want:     map[string]string{"api": "plane-a", "web": "plane-b"},
			wantAsk:  true,
		},
		{
			name:     "first takes a project and second answers empty",
			projects: []string{"api", "web"},
			planeIDs: []string{"plane-a", "plane-b"},
			input:    []string{"1", ""},
			want:     map[string]string{"api": "plane-a"},
			wantAsk:  true,
		},
		{
			name:     "wrong answer two times",
			projects: []string{"api", "web"},
			planeIDs: []string{"plane-a", "plane-b"},
			input:    []string{"x", "7", ""},
			want:     map[string]string{},
			wantAsk:  true,
		},
		{
			name:     "pairing of before stays on empty answer",
			projects: []string{"api", "web"},
			planeIDs: []string{"plane-a", "plane-b"},
			before:   map[string]string{"web": "plane-b"},
			input:    []string{"", ""},
			want:     map[string]string{"web": "plane-b"},
			wantAsk:  true,
			wantOut:  "web — Plane project (1, 2; empty keeps 2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Adapters.Plane = plane.DefaultConfig()
			cfg.Adapters.Plane.ProjectIDs = tt.planeIDs
			for _, name := range tt.projects {
				p := &config.ProjectConfig{Name: name, Path: "/tmp/" + name}
				if id, ok := tt.before[name]; ok {
					p.Plane = &config.ProjectPlaneConfig{ProjectID: id}
				}
				cfg.Projects = append(cfg.Projects, p)
			}
			state := &OnboardState{
				Persona: PersonaEnterprise,
				Config:  cfg,
				Reader:  bufio.NewReader(strings.NewReader(strings.Join(tt.input, "\n") + "\n")),
			}

			out := captureStdout(func() {
				onboardPlaneProjectPairing(state)
			})

			asked := strings.Contains(out, "Plane project (")
			if asked != tt.wantAsk {
				t.Errorf("asked = %v, want %v; output:\n%s", asked, tt.wantAsk, out)
			}
			if !tt.wantAsk && out != "" {
				t.Errorf("output = %q, want none", out)
			}

			got := map[string]string{}
			for _, p := range cfg.Projects {
				if p.Plane != nil {
					got[p.Name] = p.Plane.ProjectID
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("pairings = %v, want %v", got, tt.want)
			}
			for name, id := range tt.want {
				if got[name] != id {
					t.Errorf("pairing of %s = %q, want %q (all: %v)", name, got[name], id, got)
				}
			}
			for name, id := range got {
				polled := false
				for _, pid := range tt.planeIDs {
					polled = polled || pid == id
				}
				if !polled {
					t.Errorf("pairing of %s = %q, not a polled ID", name, id)
				}
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() error = %v", err)
			}
			if tt.wantOut != "" && !strings.Contains(out, tt.wantOut) {
				t.Errorf("output lacks %q:\n%s", tt.wantOut, out)
			}
			if tt.wantAsk && !strings.Contains(out, "go to the default project (api)") {
				t.Errorf("output lacks the default project line:\n%s", out)
			}
		})
	}
}
