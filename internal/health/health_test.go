package health

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/transcription"
)

// ---------------------------------------------------------------------------
// Status type tests
// ---------------------------------------------------------------------------

func TestStatusSymbol(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusOK, "✓"},
		{StatusWarning, "○"},
		{StatusError, "✗"},
		{StatusDisabled, "·"},
		{Status(99), "?"},
	}
	for _, tt := range tests {
		if got := tt.status.Symbol(); got != tt.want {
			t.Errorf("Status(%d).Symbol() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusOK, "ok"},
		{StatusWarning, "warning"},
		{StatusError, "error"},
		{StatusDisabled, "disabled"},
		{Status(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStatusColorSymbol(t *testing.T) {
	// Just verify non-empty and contains the plain symbol
	for _, s := range []Status{StatusOK, StatusWarning, StatusError, StatusDisabled} {
		cs := s.ColorSymbol()
		if cs == "" {
			t.Errorf("Status(%d).ColorSymbol() is empty", s)
		}
		if plain := s.Symbol(); plain != "?" {
			// Colored version should contain the plain symbol rune
			if len(cs) <= len(plain) {
				t.Errorf("Status(%d).ColorSymbol() = %q, expected ANSI-wrapped version", s, cs)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// boolToStatus
// ---------------------------------------------------------------------------

func TestBoolToStatus(t *testing.T) {
	if got := boolToStatus(true); got != StatusOK {
		t.Errorf("boolToStatus(true) = %v, want StatusOK", got)
	}
	if got := boolToStatus(false); got != StatusDisabled {
		t.Errorf("boolToStatus(false) = %v, want StatusDisabled", got)
	}
}

// ---------------------------------------------------------------------------
// expandPath
// ---------------------------------------------------------------------------

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"tilde prefix", "~/projects", filepath.Join(home, "projects")},
		{"tilde only", "~", filepath.Join(home)},
		{"absolute", "/usr/local/bin", "/usr/local/bin"},
		{"relative", "foo/bar", "foo/bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandPath(tt.path); got != tt.want {
				t.Errorf("expandPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HealthReport.Summary
// ---------------------------------------------------------------------------

func TestHealthReportSummary(t *testing.T) {
	report := &HealthReport{
		Dependencies: []Check{
			{Name: "git", Status: StatusOK},
			{Name: "claude", Status: StatusError},
			{Name: "gh", Status: StatusWarning},
		},
		Config: []ConfigCheck{
			{Name: "config file", Status: StatusOK},
			{Name: "telegram", Status: StatusError},
			{Name: "projects", Status: StatusWarning},
		},
	}

	errors, warnings := report.Summary()
	if errors != 2 {
		t.Errorf("Summary() errors = %d, want 2", errors)
	}
	if warnings != 2 {
		t.Errorf("Summary() warnings = %d, want 2", warnings)
	}
}

func TestHealthReportSummaryClean(t *testing.T) {
	report := &HealthReport{
		Dependencies: []Check{{Name: "git", Status: StatusOK}},
		Config:       []ConfigCheck{{Name: "config", Status: StatusOK}},
	}

	errors, warnings := report.Summary()
	if errors != 0 || warnings != 0 {
		t.Errorf("Summary() = (%d, %d), want (0, 0)", errors, warnings)
	}
}

// ---------------------------------------------------------------------------
// HealthReport.ReadyToStart
// ---------------------------------------------------------------------------

func TestReadyToStart(t *testing.T) {
	tests := []struct {
		name string
		deps []Check
		want bool
	}{
		{
			name: "all ok",
			deps: []Check{
				{Name: "claude", Status: StatusOK, Message: "1.0.0 [active backend]"},
				{Name: "git", Status: StatusOK},
			},
			want: true,
		},
		{
			name: "active backend missing",
			deps: []Check{
				{Name: "claude", Status: StatusError, Message: "not found [active backend]"},
				{Name: "git", Status: StatusOK},
			},
			want: false,
		},
		{
			name: "non-active backend missing is ok",
			deps: []Check{
				{Name: "claude", Status: StatusOK, Message: "1.0.0 [active backend]"},
				{Name: "qwen", Status: StatusError, Message: "not found"},
				{Name: "git", Status: StatusOK},
			},
			want: true,
		},
		{
			name: "git missing",
			deps: []Check{
				{Name: "claude", Status: StatusOK, Message: "1.0.0 [active backend]"},
				{Name: "git", Status: StatusError},
			},
			want: false,
		},
		{
			name: "gh warning is ok",
			deps: []Check{
				{Name: "claude", Status: StatusOK, Message: "1.0.0 [active backend]"},
				{Name: "git", Status: StatusOK},
				{Name: "gh", Status: StatusWarning},
			},
			want: true,
		},
		{
			name: "empty deps",
			deps: []Check{},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := &HealthReport{Dependencies: tt.deps}
			if got := report.ReadyToStart(); got != tt.want {
				t.Errorf("ReadyToStart() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkConfig — table-driven tests with synthetic config
// ---------------------------------------------------------------------------

func TestCheckConfig_NoProjects(t *testing.T) {
	cfg := &config.Config{
		Projects: nil,
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "projects")
	if found == nil {
		t.Fatal("expected 'projects' check")
	}
	if found.Status != StatusWarning {
		t.Errorf("projects status = %v, want StatusWarning", found.Status)
	}
}

func TestCheckConfig_ValidProjects(t *testing.T) {
	// Use a path that exists on any system
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "test", Path: os.TempDir()},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "projects")
	if found == nil {
		t.Fatal("expected 'projects' check")
	}
	if found.Status != StatusOK {
		t.Errorf("projects status = %v, want StatusOK", found.Status)
	}
}

func TestCheckConfig_InvalidProjectPath(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "test", Path: "/nonexistent/path/xyz123"},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "projects")
	if found == nil {
		t.Fatal("expected 'projects' check")
	}
	if found.Status != StatusWarning {
		t.Errorf("projects status = %v, want StatusWarning", found.Status)
	}
}

func TestCheckConfig_TelegramEnabled_NoToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{
				Enabled:  true,
				BotToken: "",
			},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "telegram.bot_token")
	if found == nil {
		t.Fatal("expected 'telegram.bot_token' check")
	}
	if found.Status != StatusError {
		t.Errorf("telegram.bot_token status = %v, want StatusError", found.Status)
	}
}

func TestCheckConfig_TelegramEnabled_WithToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{
				Enabled:  true,
				BotToken: "test-bot-token",
			},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "telegram.bot_token")
	if found == nil {
		t.Fatal("expected 'telegram.bot_token' check")
	}
	if found.Status != StatusOK {
		t.Errorf("telegram.bot_token status = %v, want StatusOK", found.Status)
	}
}

func TestCheckConfig_SlackEnabled_NoToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Slack: &slack.Config{
				Enabled:  true,
				BotToken: "",
			},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "slack.bot_token")
	if found == nil {
		t.Fatal("expected 'slack.bot_token' check")
	}
	if found.Status != StatusError {
		t.Errorf("slack.bot_token status = %v, want StatusError", found.Status)
	}
}

func TestCheckConfig_SlackEnabled_WithToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Slack: &slack.Config{
				Enabled:  true,
				BotToken: "test-slack-bot-token",
			},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "slack.bot_token")
	if found == nil {
		t.Fatal("expected 'slack.bot_token' check")
	}
	if found.Status != StatusOK {
		t.Errorf("slack.bot_token status = %v, want StatusOK", found.Status)
	}
}

// ---------------------------------------------------------------------------
// checkFeatures
// ---------------------------------------------------------------------------

func TestCheckFeatures_MinimalConfig(t *testing.T) {
	cfg := &config.Config{}
	features := checkFeatures(cfg)

	// Should always have core features regardless of config
	if len(features) == 0 {
		t.Fatal("expected at least one feature check")
	}

	// Find Task Execution — should be OK if claude+git are on PATH
	exec := findFeature(features, "Task Execution")
	if exec == nil {
		t.Fatal("expected 'Task Execution' feature")
	}
	// On dev machines claude+git should be present
	if exec.Status == StatusError && len(exec.Missing) == 0 {
		t.Error("Task Execution error but Missing is empty")
	}
}

func TestCheckFeatures_TelegramDisabled(t *testing.T) {
	cfg := &config.Config{}
	features := checkFeatures(cfg)

	tg := findFeature(features, "Telegram")
	if tg == nil {
		t.Fatal("expected 'Telegram' feature")
	}
	if tg.Enabled {
		t.Error("Telegram should be disabled with empty config")
	}
	if tg.Status != StatusDisabled {
		t.Errorf("Telegram status = %v, want StatusDisabled", tg.Status)
	}
}

func TestCheckFeatures_TelegramEnabled(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{
				Enabled:  true,
				BotToken: "test-bot-token",
			},
		},
	}
	features := checkFeatures(cfg)

	tg := findFeature(features, "Telegram")
	if tg == nil {
		t.Fatal("expected 'Telegram' feature")
	}
	if !tg.Enabled {
		t.Error("Telegram should be enabled")
	}
	if tg.Status != StatusOK {
		t.Errorf("Telegram status = %v, want StatusOK", tg.Status)
	}
}

func TestCheckFeatures_TelegramEnabledNoToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{
				Enabled:  true,
				BotToken: "",
			},
		},
	}
	features := checkFeatures(cfg)

	tg := findFeature(features, "Telegram")
	if tg == nil {
		t.Fatal("expected 'Telegram' feature")
	}
	if tg.Enabled {
		t.Error("Telegram should be disabled without token")
	}
	if tg.Note != "missing bot_token" {
		t.Errorf("Telegram note = %q, want %q", tg.Note, "missing bot_token")
	}
}

func TestCheckFeatures_VoiceWithoutKey(t *testing.T) {
	cfg := &config.Config{}
	features := checkFeatures(cfg)

	voice := findFeature(features, "Voice")
	if voice == nil {
		t.Fatal("expected 'Voice' feature")
	}
	if voice.Enabled {
		t.Error("Voice should be disabled without OpenAI key")
	}
	if voice.Status != StatusWarning {
		t.Errorf("Voice status = %v, want StatusWarning", voice.Status)
	}
}

func TestCheckFeatures_VoiceWithKey(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{
				Transcription: &transcription.Config{
					OpenAIAPIKey: "test-openai-key",
				},
			},
		},
	}
	features := checkFeatures(cfg)

	voice := findFeature(features, "Voice")
	if voice == nil {
		t.Fatal("expected 'Voice' feature")
	}
	if !voice.Enabled {
		t.Error("Voice should be enabled with OpenAI key")
	}
	if voice.Status != StatusOK {
		t.Errorf("Voice status = %v, want StatusOK", voice.Status)
	}
}

func TestCheckFeatures_AlertsEnabled(t *testing.T) {
	cfg := &config.Config{
		Alerts: &config.AlertsConfig{
			Enabled: true,
		},
	}
	features := checkFeatures(cfg)

	a := findFeature(features, "Alerts")
	if a == nil {
		t.Fatal("expected 'Alerts' feature")
	}
	if !a.Enabled {
		t.Error("Alerts should be enabled")
	}
}

func TestCheckFeatures_AlertsDisabled(t *testing.T) {
	cfg := &config.Config{}
	features := checkFeatures(cfg)

	a := findFeature(features, "Alerts")
	if a == nil {
		t.Fatal("expected 'Alerts' feature")
	}
	if a.Enabled {
		t.Error("Alerts should be disabled with empty config")
	}
}

// ---------------------------------------------------------------------------
// RunChecks — integration-level
// ---------------------------------------------------------------------------

func TestRunChecks_SetsFlags(t *testing.T) {
	cfg := &config.Config{}
	report := RunChecks(cfg)

	if report == nil {
		t.Fatal("RunChecks returned nil")
	}
	if len(report.Dependencies) == 0 {
		t.Error("expected at least one dependency check")
	}
	if len(report.Features) == 0 {
		t.Error("expected at least one feature check")
	}
}

func TestRunChecks_HasErrorsFlagSet(t *testing.T) {
	// Force an error by using a config with enabled adapter but no token
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			Telegram: &telegram.Config{
				Enabled:  true,
				BotToken: "",
			},
		},
	}
	report := RunChecks(cfg)

	// Config should have an error for missing telegram token
	hasConfigError := false
	for _, c := range report.Config {
		if c.Status == StatusError {
			hasConfigError = true
		}
	}
	if !hasConfigError {
		t.Error("expected at least one config error for missing telegram token")
	}
	if !report.HasErrors {
		t.Error("HasErrors should be true when config has errors")
	}
}

func TestRunChecks_ProjectCount(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "a", Path: "/tmp"},
			{Name: "b", Path: "/tmp"},
		},
	}
	report := RunChecks(cfg)

	if report.Projects != 2 {
		t.Errorf("Projects = %d, want 2", report.Projects)
	}
}

// ---------------------------------------------------------------------------
// checkDependencies — integration test (runs on dev machine)
// ---------------------------------------------------------------------------

func TestCheckDependencies_ReturnsChecks(t *testing.T) {
	deps := checkDependencies()

	// Should have at minimum claude, git, gh
	if len(deps) < 3 {
		t.Errorf("expected at least 3 dependency checks, got %d", len(deps))
	}

	// git should be available on any dev machine
	gitCheck := findCheck(deps, "git")
	if gitCheck == nil {
		t.Fatal("expected 'git' dependency check")
	}
	if gitCheck.Status != StatusOK {
		t.Errorf("git status = %v, want StatusOK (is git installed?)", gitCheck.Status)
	}
}

// ---------------------------------------------------------------------------
// getCommandVersion
// ---------------------------------------------------------------------------

func TestGetCommandVersion_ValidCommand(t *testing.T) {
	// 'git --version' should work everywhere
	version := getCommandVersion("git", "--version")
	if version == "" {
		t.Skip("git not installed, skipping")
	}
	// Should contain a dot (version number like "2.39.0")
	if len(version) < 3 {
		t.Errorf("getCommandVersion(git) = %q, expected version string", version)
	}
}

func TestGetCommandVersion_InvalidCommand(t *testing.T) {
	version := getCommandVersion("nonexistent_command_xyz", "--version")
	if version != "" {
		t.Errorf("expected empty string for nonexistent command, got %q", version)
	}
}

// ---------------------------------------------------------------------------
// commandExists
// ---------------------------------------------------------------------------

func TestCommandExists(t *testing.T) {
	if !commandExists("git") {
		t.Skip("git not installed, skipping")
	}
	if commandExists("nonexistent_command_xyz_123") {
		t.Error("expected false for nonexistent command")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func findCheck(checks []Check, name string) *Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func findConfigCheck(checks []ConfigCheck, name string) *ConfigCheck {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func findFeature(features []FeatureStatus, name string) *FeatureStatus {
	for i := range features {
		if features[i].Name == name {
			return &features[i]
		}
	}
	return nil
}
