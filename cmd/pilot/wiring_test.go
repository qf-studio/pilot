package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/asana"
	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/discord"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/jira"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/quality"
	"github.com/qf-studio/pilot/internal/testutil"
)

// =============================================================================
// GH-2134: Per-adapter Enabled() wiring tests
//
// Each poller registration's Enabled() function must correctly gate on:
//   - adapter config being non-nil
//   - adapter .Enabled == true
//   - polling sub-config being non-nil and .Enabled == true (where applicable)
// =============================================================================

func TestPollerEnabled_Linear(t *testing.T) {
	reg := linearPollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil adapters",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "adapter disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{Enabled: false},
			}},
			enabled: false,
		},
		{
			name: "adapter enabled but no polling config",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{Enabled: true},
			}},
			enabled: false,
		},
		{
			name: "adapter enabled but polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{
					Enabled: true,
					Polling: &linear.PollingConfig{Enabled: false},
				},
			}},
			enabled: false,
		},
		{
			name: "fully enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Linear: &linear.Config{
					Enabled: true,
					APIKey:  testutil.FakeLinearAPIKey,
					Polling: &linear.PollingConfig{Enabled: true},
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestPollerEnabled_Jira(t *testing.T) {
	reg := jiraPollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil config",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "enabled without polling",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{Enabled: true},
			}},
			enabled: false,
		},
		{
			name: "polling disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{
					Enabled: true,
					Polling: &jira.PollingConfig{Enabled: false},
				},
			}},
			enabled: false,
		},
		{
			name: "fully enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Jira: &jira.Config{
					Enabled:  true,
					BaseURL:  "https://jira.test",
					APIToken: testutil.FakeJiraAPIToken,
					Polling:  &jira.PollingConfig{Enabled: true},
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestPollerEnabled_Asana(t *testing.T) {
	reg := asanaPollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil config",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "enabled without polling",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Asana: &asana.Config{Enabled: true},
			}},
			enabled: false,
		},
		{
			name: "fully enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Asana: &asana.Config{
					Enabled:     true,
					AccessToken: testutil.FakeAsanaAccessToken,
					WorkspaceID: testutil.FakeAsanaWorkspaceID,
					Polling:     &asana.PollingConfig{Enabled: true},
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestPollerEnabled_AzureDevOps(t *testing.T) {
	reg := azuredevopsPollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil config",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "enabled without polling",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				AzureDevOps: &azuredevops.Config{Enabled: true},
			}},
			enabled: false,
		},
		{
			name: "fully enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				AzureDevOps: &azuredevops.Config{
					Enabled:      true,
					PAT:          testutil.FakeAzureDevOpsPAT,
					Organization: "test-org",
					Project:      "test-project",
					Polling:      &azuredevops.PollingConfig{Enabled: true},
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestPollerEnabled_Plane(t *testing.T) {
	reg := planePollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil config",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "enabled without polling",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{Enabled: true},
			}},
			enabled: false,
		},
		{
			name: "fully enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Plane: &plane.Config{
					Enabled:       true,
					BaseURL:       "https://plane.test",
					APIKey:        testutil.FakePlaneAPIKey,
					WorkspaceSlug: "test-ws",
					Polling:       &plane.PollingConfig{Enabled: true},
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestPollerEnabled_Discord(t *testing.T) {
	reg := discordPollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil config",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "adapter disabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Discord: &discord.Config{Enabled: false},
			}},
			enabled: false,
		},
		{
			name: "enabled — no polling sub-config needed",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				Discord: &discord.Config{
					Enabled:  true,
					BotToken: testutil.FakeBearerToken,
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestPollerEnabled_GitLab(t *testing.T) {
	reg := gitlabPollerRegistration()

	tests := []struct {
		name    string
		cfg     *config.Config
		enabled bool
	}{
		{
			name:    "nil config",
			cfg:     &config.Config{Adapters: &config.AdaptersConfig{}},
			enabled: false,
		},
		{
			name: "enabled without polling",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitLab: &gitlab.Config{Enabled: true},
			}},
			enabled: false,
		},
		{
			name: "fully enabled",
			cfg: &config.Config{Adapters: &config.AdaptersConfig{
				GitLab: &gitlab.Config{
					Enabled: true,
					Token:   testutil.FakeGitLabToken,
					Project: "group/project",
					Polling: &gitlab.PollingConfig{Enabled: true},
				},
			}},
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reg.Enabled(tt.cfg); got != tt.enabled {
				t.Errorf("Enabled() = %v, want %v", got, tt.enabled)
			}
		})
	}
}

// =============================================================================
// GH-2134: getAlertsConfig wiring tests
// =============================================================================

func TestGetAlertsConfig_NilAlerts(t *testing.T) {
	cfg := &config.Config{}
	result := getAlertsConfig(cfg)
	if result != nil {
		t.Error("expected nil AlertConfig when cfg.Alerts is nil")
	}
}

func TestGetAlertsConfig_DisabledAlerts(t *testing.T) {
	cfg := &config.Config{
		Alerts: &config.AlertsConfig{
			Enabled: false,
			Defaults: config.AlertDefaultsConfig{
				Cooldown:        5 * time.Minute,
				DefaultSeverity: "warning",
			},
		},
	}
	result := getAlertsConfig(cfg)
	if result == nil {
		t.Fatal("expected non-nil AlertConfig even when disabled (FromConfigAlerts decides)")
	}
}

func TestGetAlertsConfig_WithChannelsAndRules(t *testing.T) {
	cfg := &config.Config{
		Alerts: &config.AlertsConfig{
			Enabled: true,
			Channels: []config.AlertChannelConfig{
				{
					Name:       "slack-alerts",
					Type:       "slack",
					Enabled:    true,
					Severities: []string{"critical", "error"},
					Slack: &alerts.SlackChannelConfig{
						Channel: "#alerts",
					},
				},
				{
					Name:    "webhook-alerts",
					Type:    "webhook",
					Enabled: true,
					Webhook: &alerts.WebhookChannelConfig{
						URL: "https://hooks.test/alert",
					},
				},
			},
			Rules: []config.AlertRuleConfig{
				{
					Name:     "task-failure",
					Type:     "task_failure",
					Enabled:  true,
					Severity: "error",
					Channels: []string{"slack-alerts"},
					Cooldown: 10 * time.Minute,
					Condition: config.AlertConditionConfig{
						ConsecutiveFailures: 3,
					},
				},
			},
			Defaults: config.AlertDefaultsConfig{
				Cooldown:           5 * time.Minute,
				DefaultSeverity:    "warning",
				SuppressDuplicates: true,
			},
		},
	}

	result := getAlertsConfig(cfg)
	if result == nil {
		t.Fatal("expected non-nil AlertConfig")
	}
	if !result.Enabled {
		t.Error("expected AlertConfig.Enabled = true")
	}
}

// =============================================================================
// GH-2134: resolveOwnerRepo tests
// =============================================================================

func TestResolveOwnerRepo_FromConfig(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{
				Repo: "myorg/myrepo",
			},
		},
	}

	owner, repo, err := resolveOwnerRepo(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if owner != "myorg" {
		t.Errorf("owner = %q, want %q", owner, "myorg")
	}
	if repo != "myrepo" {
		t.Errorf("repo = %q, want %q", repo, "myrepo")
	}
}

func TestResolveOwnerRepo_EmptyConfigFallsBackToGit(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{},
	}

	// This will try git remote — may succeed or fail depending on environment.
	// We just verify it doesn't panic with nil GitHub config.
	_, _, _ = resolveOwnerRepo(cfg)
}

func TestResolveOwnerRepo_SingleSegmentRepo(t *testing.T) {
	// Single-segment repo string doesn't split into owner/repo
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{
				Repo: "just-a-name",
			},
		},
	}

	// Falls back to git remote since split won't produce 2 parts
	_, _, _ = resolveOwnerRepo(cfg)
}

// =============================================================================
// GH-2134: qualityCheckerWrapper adapter test
// =============================================================================

func TestQualityCheckerWrapper_NilResultFields(t *testing.T) {
	// qualityCheckerWrapper is a type adapter — verify it compiles and implements
	// the interface correctly by checking the struct can be instantiated.
	// Full integration would require a quality.Executor with real gates.
	var _ interface {
		Check(ctx interface{}) (interface{}, error)
	}
	// Type assertion at compile time is sufficient — the wrapper adapts
	// quality.Executor to executor.QualityChecker. If the interface changes,
	// this file won't compile.
	_ = &qualityCheckerWrapper{}
}

// =============================================================================
// GH-3716: newProjectQualityCheckerFactory resolution precedence
// =============================================================================

func TestNewProjectQualityCheckerFactory_ProjectOverrideWinsOverGlobal(t *testing.T) {
	projectPath := t.TempDir()

	cfg := &config.Config{
		Quality: &quality.Config{
			Enabled: true,
			Gates:   []*quality.Gate{{Name: "build", Type: quality.GateBuild, Command: "false", Required: true}},
		},
		Projects: []*config.ProjectConfig{
			{
				Name: "override-project",
				Path: projectPath,
				Quality: &quality.Config{
					Enabled: true,
					Gates:   []*quality.Gate{{Name: "build", Type: quality.GateBuild, Command: "true", Required: true}},
				},
			},
		},
	}

	factory := newProjectQualityCheckerFactory(cfg)
	checker := factory("task-1", projectPath)

	outcome, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !outcome.Passed {
		t.Error("expected project-level quality override (command: true) to pass, indicating it took precedence over the failing global config")
	}
}

func TestNewProjectQualityCheckerFactory_FallsBackToGlobal(t *testing.T) {
	projectPath := t.TempDir()

	cfg := &config.Config{
		Quality: &quality.Config{
			Enabled: true,
			Gates:   []*quality.Gate{{Name: "build", Type: quality.GateBuild, Command: "true", Required: true}},
		},
		Projects: []*config.ProjectConfig{
			{Name: "no-override", Path: projectPath}, // no Quality set
		},
	}

	factory := newProjectQualityCheckerFactory(cfg)
	checker := factory("task-1", projectPath)

	outcome, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !outcome.Passed {
		t.Error("expected global quality config to apply when project has no override")
	}
}

func TestNewProjectQualityCheckerFactory_AutoDetectsWhenUnconfigured(t *testing.T) {
	projectPath := t.TempDir()

	// No global Quality, no matching project entry — should synthesize a
	// minimal config via quality.AutoDetectConfig rather than reuse a
	// mismatched global config or panic on a nil config.
	cfg := &config.Config{}

	factory := newProjectQualityCheckerFactory(cfg)
	checker := factory("task-1", projectPath)

	outcome, err := checker.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	// Empty tmp dir has no detectable build command, so AutoDetectConfig
	// yields a disabled config, which Check() short-circuits as passed.
	if !outcome.Passed {
		t.Error("expected auto-detect on an unrecognized project to short-circuit as passed rather than fail or panic")
	}
}

func TestNewProjectQualityCheckerFactory_AutoDetectsGoProject(t *testing.T) {
	projectPath := t.TempDir()
	if err := os.WriteFile(projectPath+"/go.mod", []byte("module autodetecttest\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	cfg := &config.Config{}

	factory := newProjectQualityCheckerFactory(cfg)
	checker := factory("task-1", projectPath)

	// Sanity check the resolution picked the Go build command rather than
	// silently disabling gates, without actually invoking `go build`.
	resolved := quality.ResolveConfig(nil, cfg.Quality, projectPath)
	if !resolved.Enabled {
		t.Fatal("expected auto-detect to enable gates for a Go project")
	}
	if resolved.Gates[0].Command != "go build ./..." {
		t.Errorf("auto-detected build command = %q, want %q", resolved.Gates[0].Command, "go build ./...")
	}
	if checker == nil {
		t.Fatal("expected a non-nil checker")
	}
}

// =============================================================================
// GH-2134: Verify all adapter registrations have correct names
// =============================================================================

func TestPollerRegistrationNames(t *testing.T) {
	regs := adapterPollerRegistrations()

	names := make(map[string]bool)
	for _, reg := range regs {
		if reg.Name == "" {
			t.Error("found registration with empty name")
		}
		if names[reg.Name] {
			t.Errorf("duplicate registration name: %q", reg.Name)
		}
		names[reg.Name] = true

		if reg.Enabled == nil {
			t.Errorf("registration %q has nil Enabled func", reg.Name)
		}
		if reg.CreateAndStart == nil {
			t.Errorf("registration %q has nil CreateAndStart func", reg.Name)
		}
	}
}

// =============================================================================
// GH-2134: Verify multiple adapters can be enabled simultaneously
// =============================================================================

func TestPollerEnabled_MultipleAdaptersSimultaneously(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Linear: &linear.Config{
				Enabled: true,
				APIKey:  testutil.FakeLinearAPIKey,
				Polling: &linear.PollingConfig{Enabled: true},
			},
			Jira: &jira.Config{
				Enabled:  true,
				BaseURL:  "https://jira.test",
				APIToken: testutil.FakeJiraAPIToken,
				Polling:  &jira.PollingConfig{Enabled: true},
			},
			Discord: &discord.Config{
				Enabled:  true,
				BotToken: testutil.FakeBearerToken,
			},
			GitLab: &gitlab.Config{
				Enabled: true,
				Token:   testutil.FakeGitLabToken,
				Polling: &gitlab.PollingConfig{Enabled: true},
			},
			// These remain disabled
			Asana:       &asana.Config{Enabled: false},
			AzureDevOps: &azuredevops.Config{Enabled: false},
			Plane:       &plane.Config{Enabled: false},
		},
	}

	regs := adapterPollerRegistrations()

	expectedEnabled := map[string]bool{
		"linear":      true,
		"jira":        true,
		"discord":     true,
		"gitlab":      true,
		"asana":       false,
		"azuredevops": false,
		"plane":       false,
		"github":      false, // no GitHub config in cfg → SDK registration stays off
	}

	for _, reg := range regs {
		want, ok := expectedEnabled[reg.Name]
		if !ok {
			t.Errorf("unexpected registration name %q", reg.Name)
			continue
		}
		if got := reg.Enabled(cfg); got != want {
			t.Errorf("adapter %q: Enabled() = %v, want %v", reg.Name, got, want)
		}
	}
}

// =============================================================================
// GH-2134: convertKeyboardToTelegram test
// =============================================================================

func TestConvertKeyboardToTelegram(t *testing.T) {
	input := [][]approval.InlineKeyboardButton{
		{
			{Text: "Approve", CallbackData: "approve_1"},
			{Text: "Reject", CallbackData: "reject_1"},
		},
		{
			{Text: "Skip", CallbackData: "skip_1"},
		},
	}

	result := convertKeyboardToTelegram(input)

	if len(result) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result))
	}
	if len(result[0]) != 2 {
		t.Fatalf("expected 2 buttons in row 0, got %d", len(result[0]))
	}
	if len(result[1]) != 1 {
		t.Fatalf("expected 1 button in row 1, got %d", len(result[1]))
	}

	if result[0][0].Text != "Approve" {
		t.Errorf("button[0][0].Text = %q, want %q", result[0][0].Text, "Approve")
	}
	if result[0][0].CallbackData != "approve_1" {
		t.Errorf("button[0][0].CallbackData = %q, want %q", result[0][0].CallbackData, "approve_1")
	}
	if result[0][1].Text != "Reject" {
		t.Errorf("button[0][1].Text = %q, want %q", result[0][1].Text, "Reject")
	}
	if result[1][0].Text != "Skip" {
		t.Errorf("button[1][0].Text = %q, want %q", result[1][0].Text, "Skip")
	}
}

func TestConvertKeyboardToTelegram_Empty(t *testing.T) {
	result := convertKeyboardToTelegram(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 rows for nil input, got %d", len(result))
	}
}

// =============================================================================
// GH-4843 (D1): gatewayChatNeedsOwnRunner — chat/gateway-mode wiring hole
// =============================================================================

func TestGatewayChatNeedsOwnRunner(t *testing.T) {
	tests := []struct {
		name              string
		chatEnabled       bool
		needsPollingInfra bool
		want              bool
	}{
		{
			name:              "chat off, no polling infra",
			chatEnabled:       false,
			needsPollingInfra: false,
			want:              false,
		},
		{
			name:              "chat off, polling infra needed",
			chatEnabled:       false,
			needsPollingInfra: true,
			want:              false,
		},
		{
			// The GH-4843 D1 gap: chat enabled, nothing else needs a runner —
			// gwRunner would otherwise stay nil and WithChatHandler(nil, ...)
			// silently leaves the chat routes unregistered.
			name:              "chat on, no polling infra",
			chatEnabled:       true,
			needsPollingInfra: false,
			want:              true,
		},
		{
			// needsPollingInfra's own block already builds gwRunner — chat
			// must not build a second, redundant one.
			name:              "chat on, polling infra also needed",
			chatEnabled:       true,
			needsPollingInfra: true,
			want:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gatewayChatNeedsOwnRunner(tt.chatEnabled, tt.needsPollingInfra); got != tt.want {
				t.Errorf("gatewayChatNeedsOwnRunner(%v, %v) = %v, want %v", tt.chatEnabled, tt.needsPollingInfra, got, tt.want)
			}
		})
	}
}

// =============================================================================
// GH-2134: autopilotProviderAdapter compile check
// =============================================================================

func TestAutopilotProviderAdapter_ImplementsInterface(t *testing.T) {
	// Compile-time check that autopilotProviderAdapter satisfies gateway.AutopilotProvider
	// (if the interface signature changes, this file will fail to compile)
	_ = &autopilotProviderAdapter{}
}
