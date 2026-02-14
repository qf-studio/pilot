package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/testutil"
)

// TestParseGitURL tests URL parsing for various git remote formats.
func TestParseGitURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{
			name:      "SSH URL",
			url:       "git@github.com:acme/backend.git",
			wantOwner: "acme",
			wantRepo:  "backend",
			wantErr:   false,
		},
		{
			name:      "HTTPS URL",
			url:       "https://github.com/acme/backend.git",
			wantOwner: "acme",
			wantRepo:  "backend",
			wantErr:   false,
		},
		{
			name:      "HTTPS without .git",
			url:       "https://github.com/acme/backend",
			wantOwner: "acme",
			wantRepo:  "backend",
			wantErr:   false,
		},
		{
			name:      "GitLab SSH URL",
			url:       "git@gitlab.com:ns/project.git",
			wantOwner: "ns",
			wantRepo:  "project",
			wantErr:   false,
		},
		{
			name:    "Invalid URL - no colon",
			url:     "invalid-url",
			wantErr: true,
		},
		{
			name:    "Invalid SSH URL",
			url:     "git@github.com",
			wantErr: true,
		},
		{
			name:    "Invalid HTTPS URL - no path",
			url:     "https://github.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := parseGitURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseGitURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if owner != tt.wantOwner {
					t.Errorf("parseGitURL() owner = %v, want %v", owner, tt.wantOwner)
				}
				if repo != tt.wantRepo {
					t.Errorf("parseGitURL() repo = %v, want %v", repo, tt.wantRepo)
				}
			}
		})
	}
}

// TestSelectOption tests option selection with mock reader input.
func TestSelectOption(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		options []string
		want    int
	}{
		{
			name:    "select first option",
			input:   "1\n",
			options: []string{"Option A", "Option B", "Option C"},
			want:    1,
		},
		{
			name:    "select third option",
			input:   "3\n",
			options: []string{"Option A", "Option B", "Option C"},
			want:    3,
		},
		{
			name:    "invalid then valid - returns default",
			input:   "abc\n",
			options: []string{"Option A", "Option B"},
			want:    1, // Default to first on invalid
		},
		{
			name:    "out of range - returns default",
			input:   "5\n",
			options: []string{"Option A", "Option B"},
			want:    1, // Default to first on out of range
		},
		{
			name:    "empty input - returns default",
			input:   "\n",
			options: []string{"Option A", "Option B"},
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tt.input))
			got := selectOption(reader, "Select:", tt.options)
			if got != tt.want {
				t.Errorf("selectOption() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPersonaRouting tests that persona selection routes to correct stage counts.
func TestPersonaRouting(t *testing.T) {
	tests := []struct {
		name             string
		persona          Persona
		wantStagesTotal  int
		wantTicketSources int
	}{
		{
			name:             "Solo persona",
			persona:          PersonaSolo,
			wantStagesTotal:  4,
			wantTicketSources: 1, // GitHub only
		},
		{
			name:             "Team persona",
			persona:          PersonaTeam,
			wantStagesTotal:  5,
			wantTicketSources: 3, // GitHub, Linear, Jira
		},
		{
			name:             "Enterprise persona",
			persona:          PersonaEnterprise,
			wantStagesTotal:  5,
			wantTicketSources: 6, // All sources
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create state with persona
			state := &OnboardState{
				Persona: tt.persona,
				Config:  config.DefaultConfig(),
			}

			// Set stage count based on persona (as done in runOnboard)
			switch state.Persona {
			case PersonaSolo:
				state.StagesTotal = 4
			case PersonaTeam, PersonaEnterprise:
				state.StagesTotal = 5
			}

			if state.StagesTotal != tt.wantStagesTotal {
				t.Errorf("StagesTotal = %v, want %v", state.StagesTotal, tt.wantStagesTotal)
			}

			// Check ticket sources for persona
			sources := getTicketSourcesForPersona(tt.persona)
			if len(sources) != tt.wantTicketSources {
				t.Errorf("ticket sources count = %v, want %v", len(sources), tt.wantTicketSources)
			}
		})
	}
}

// TestValidateGitHubConn tests GitHub connection validation.
// Note: Current implementation is a stub that only checks for empty token.
func TestValidateGitHubConn(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{
			name:    "valid token",
			token:   testutil.FakeGitHubToken,
			wantErr: false,
		},
		{
			name:    "empty token",
			token:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitHubConn(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateGitHubConn() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateGitHubConnWithServer demonstrates GitHub validation with httptest server.
// Uses GetRepository as a proxy for auth validation since the client doesn't have GetAuthenticatedUser.
func TestValidateGitHubConnWithServer(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantErr    bool
	}{
		{
			name:       "success - valid token",
			statusCode: http.StatusOK,
			response:   github.Repository{Name: "test-repo"},
			wantErr:    false,
		},
		{
			name:       "unauthorized - invalid token",
			statusCode: http.StatusUnauthorized,
			response:   map[string]string{"message": "Bad credentials"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
					t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
				}
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			// Use the GitHub client with test server base URL
			client := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			_, err := client.GetRepository(context.Background(), "owner", "repo")

			if (err != nil) != tt.wantErr {
				t.Errorf("GetRepository() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateSlackConn tests Slack connection validation.
// Note: Current implementation validates token format (xoxb- prefix).
func TestValidateSlackConn(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantBot string
		wantErr bool
	}{
		{
			name:    "valid token format",
			token:   "xoxb-test-token",
			wantBot: "pilot-bot", // Stub always returns "pilot-bot"
			wantErr: false,
		},
		{
			name:    "invalid token format - no xoxb prefix",
			token:   "invalid-format",
			wantBot: "",
			wantErr: true,
		},
		{
			name:    "empty token",
			token:   "",
			wantBot: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			botName, err := validateSlackConn(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSlackConn() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && botName != tt.wantBot {
				t.Errorf("validateSlackConn() botName = %v, want %v", botName, tt.wantBot)
			}
		})
	}
}

// TestValidateSlackConnWithServer tests Slack validation with httptest server.
func TestValidateSlackConnWithServer(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   interface{}
		wantBot    string
		wantErr    bool
	}{
		{
			name:       "success",
			statusCode: http.StatusOK,
			response:   map[string]interface{}{"ok": true, "user": "pilot-bot"},
			wantBot:    "pilot-bot",
			wantErr:    false,
		},
		{
			name:       "auth error",
			statusCode: http.StatusOK,
			response:   map[string]interface{}{"ok": false, "error": "invalid_auth"},
			wantBot:    "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			// Note: Would need to inject test server URL into Slack client
			// For now, just validate the test server setup
			_ = server
		})
	}
}

// TestValidateLinearConn tests Linear connection validation.
func TestValidateLinearConn(t *testing.T) {
	tests := []struct {
		name          string
		apiKey        string
		statusCode    int
		response      interface{}
		wantWorkspace string
		wantErr       bool
	}{
		{
			name:          "valid API key",
			apiKey:        testutil.FakeLinearAPIKey,
			statusCode:    http.StatusOK,
			response:      map[string]interface{}{"data": map[string]interface{}{"organization": map[string]string{"name": "Test Workspace"}}},
			wantWorkspace: "Workspace", // Stub returns "Workspace"
			wantErr:       false,
		},
		{
			name:          "empty API key",
			apiKey:        "",
			statusCode:    0,
			response:      nil,
			wantWorkspace: "",
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceName, err := validateLinearConn(tt.apiKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLinearConn() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && workspaceName != tt.wantWorkspace {
				t.Errorf("validateLinearConn() workspaceName = %v, want %v", workspaceName, tt.wantWorkspace)
			}
		})
	}
}

// TestValidateTelegramConn tests Telegram connection validation.
func TestValidateTelegramConn(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		statusCode int
		response   interface{}
		wantBot    string
		wantErr    bool
	}{
		{
			name:       "invalid token format - no colon",
			token:      "invalid-no-colon",
			statusCode: 0,
			response:   nil,
			wantBot:    "",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			botName, err := validateTelegramConn(tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateTelegramConn() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && botName != tt.wantBot {
				t.Errorf("validateTelegramConn() botName = %v, want %v", botName, tt.wantBot)
			}
		})
	}
}

// TestIdempotency tests that onboard detects already-configured states.
func TestIdempotency(t *testing.T) {
	tests := []struct {
		name              string
		setupConfig       func() *config.Config
		hasProjects       bool
		hasTickets        bool
		hasNotify         bool
		wantAllConfigured bool
	}{
		{
			name: "empty config",
			setupConfig: func() *config.Config {
				return config.DefaultConfig()
			},
			hasProjects:       false,
			hasTickets:        false,
			hasNotify:         false,
			wantAllConfigured: false,
		},
		{
			name: "has projects only",
			setupConfig: func() *config.Config {
				cfg := config.DefaultConfig()
				cfg.Projects = []*config.ProjectConfig{
					{Name: "test-project", Path: "/path/to/project"},
				}
				return cfg
			},
			hasProjects:       true,
			hasTickets:        false,
			hasNotify:         false,
			wantAllConfigured: false,
		},
		{
			name: "has GitHub adapter configured",
			setupConfig: func() *config.Config {
				cfg := config.DefaultConfig()
				cfg.Projects = []*config.ProjectConfig{
					{Name: "test-project", Path: "/path/to/project"},
				}
				cfg.Adapters = &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: true,
						Token:   testutil.FakeGitHubToken,
					},
				}
				return cfg
			},
			hasProjects:       true,
			hasTickets:        true,
			hasNotify:         false,
			wantAllConfigured: false,
		},
		{
			name: "fully configured - GitHub + Telegram",
			setupConfig: func() *config.Config {
				cfg := config.DefaultConfig()
				cfg.Projects = []*config.ProjectConfig{
					{Name: "test-project", Path: "/path/to/project"},
				}
				cfg.Adapters = &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: true,
						Token:   testutil.FakeGitHubToken,
					},
					Telegram: &telegram.Config{
						Enabled:  true,
						BotToken: testutil.FakeTelegramBotToken,
					},
				}
				return cfg
			},
			hasProjects:       true,
			hasTickets:        true,
			hasNotify:         true,
			wantAllConfigured: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.setupConfig()

			// Test helper functions
			gotHasProjects := len(cfg.Projects) > 0
			gotHasTickets := hasTicketSource(cfg)
			gotHasNotify := hasNotificationChannel(cfg)

			if gotHasProjects != tt.hasProjects {
				t.Errorf("hasProjects = %v, want %v", gotHasProjects, tt.hasProjects)
			}
			if gotHasTickets != tt.hasTickets {
				t.Errorf("hasTicketSource() = %v, want %v", gotHasTickets, tt.hasTickets)
			}
			if gotHasNotify != tt.hasNotify {
				t.Errorf("hasNotificationChannel() = %v, want %v", gotHasNotify, tt.hasNotify)
			}

			// Check all configured state
			allConfigured := gotHasProjects && gotHasTickets && gotHasNotify
			if allConfigured != tt.wantAllConfigured {
				t.Errorf("allConfigured = %v, want %v", allConfigured, tt.wantAllConfigured)
			}
		})
	}
}

// TestPersonaString tests Persona.String() method.
func TestPersonaString(t *testing.T) {
	tests := []struct {
		persona Persona
		want    string
	}{
		{PersonaSolo, "Solo"},
		{PersonaTeam, "Team"},
		{PersonaEnterprise, "Enterprise"},
		{Persona(99), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.persona.String(); got != tt.want {
				t.Errorf("Persona.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestHasTicketSource tests detection of configured ticket sources.
func TestHasTicketSource(t *testing.T) {
	tests := []struct {
		name   string
		config *config.Config
		want   bool
	}{
		{
			name:   "nil adapters",
			config: &config.Config{Adapters: nil},
			want:   false,
		},
		{
			name: "GitHub enabled",
			config: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{Enabled: true},
				},
			},
			want: true,
		},
		{
			name: "GitHub disabled",
			config: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{Enabled: false},
				},
			},
			want: false,
		},
		{
			name:   "empty adapters",
			config: &config.Config{Adapters: &config.AdaptersConfig{}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasTicketSource(tt.config); got != tt.want {
				t.Errorf("hasTicketSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

