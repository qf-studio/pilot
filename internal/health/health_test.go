package health

import (
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/transcription"
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

// GH-2361: warn when projects are configured but no issue-source adapter is enabled.
func TestCheckConfig_Adapters_ProjectsButNoAdapter(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "myrepo", Path: os.TempDir()},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "adapters")
	if found == nil {
		t.Fatal("expected 'adapters' check")
	}
	if found.Status != StatusWarning {
		t.Errorf("adapters status = %v, want StatusWarning", found.Status)
	}
	if !strings.Contains(found.Message, "no issue source") {
		t.Errorf("adapters message = %q, want mention of 'no issue source'", found.Message)
	}
}

func TestCheckConfig_Adapters_GitHubEnabled(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "myrepo", Path: os.TempDir()},
		},
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true, Token: "test-gh-token", Repo: "org/repo"},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "adapters")
	if found == nil {
		t.Fatal("expected 'adapters' check")
	}
	if found.Status != StatusOK {
		t.Errorf("adapters status = %v, want StatusOK", found.Status)
	}
}

func TestCheckConfig_Adapters_GitHubPresentButDisabled(t *testing.T) {
	cfg := &config.Config{
		Projects: []*config.ProjectConfig{
			{Name: "myrepo", Path: os.TempDir()},
		},
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: false},
		},
	}
	checks := checkConfig(cfg)

	found := findConfigCheck(checks, "adapters")
	if found == nil {
		t.Fatal("expected 'adapters' check")
	}
	if found.Status != StatusWarning {
		t.Errorf("adapters status = %v, want StatusWarning", found.Status)
	}
}

func TestCheckConfig_Adapters_NoProjectsSkipped(t *testing.T) {
	cfg := &config.Config{}
	checks := checkConfig(cfg)

	if found := findConfigCheck(checks, "adapters"); found != nil {
		t.Errorf("adapters check should not appear when no projects configured, got %+v", found)
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
	report := RunChecks(cfg, "")

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
	report := RunChecks(cfg, "")

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
	report := RunChecks(cfg, "")

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

func TestCheckConfig_GitHubChecks(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *config.Config
		wantCheckName string
		wantStatus    Status
	}{
		{
			name: "enabled no token no gh auth",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: true,
						Token:   "",
					},
				},
			},
			wantCheckName: ghAuthFallbackCheckName(t),
			wantStatus:    ghAuthFallbackStatus(t),
		},
		{
			name: "enabled no repos polling on",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: true,
						Token:   "test-gh-token",
						Polling: &github.PollingConfig{Enabled: true},
					},
				},
			},
			wantCheckName: "github.repos",
			wantStatus:    StatusWarning,
		},
		{
			name: "fully configured",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: true,
						Token:   "test-gh-token",
						Repo:    "org/repo",
					},
				},
			},
			wantCheckName: "github",
			wantStatus:    StatusOK,
		},
		{
			name: "disabled entirely",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: false,
					},
				},
			},
			wantCheckName: "", // no github check expected
			wantStatus:    StatusDisabled,
		},
		{
			name: "token with project-level repos",
			cfg: &config.Config{
				Adapters: &config.AdaptersConfig{
					GitHub: &github.Config{
						Enabled: true,
						Token:   "test-gh-token",
						Polling: &github.PollingConfig{Enabled: true},
					},
				},
				Projects: []*config.ProjectConfig{
					{
						Name: "myproj",
						Path: "/tmp",
						GitHub: &config.ProjectGitHubConfig{
							Owner: "org",
							Repo:  "repo",
						},
					},
				},
			},
			wantCheckName: "github",
			wantStatus:    StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checks := checkConfig(tt.cfg)
			if tt.wantCheckName == "" {
				// No GitHub check should appear
				for _, c := range checks {
					if strings.HasPrefix(c.Name, "github") {
						t.Errorf("found unexpected github check %q (status=%v), want none", c.Name, c.Status)
					}
				}
				return
			}
			found := findConfigCheck(checks, tt.wantCheckName)
			if found == nil {
				t.Fatalf("expected check %q, not found in %v", tt.wantCheckName, checkNames(checks))
			}
			if found.Status != tt.wantStatus {
				t.Errorf("%q status = %v, want %v", tt.wantCheckName, found.Status, tt.wantStatus)
			}
		})
	}
}

// ghAuthFallbackStatus returns the expected status when config token is empty.
// If gh CLI is authenticated on the host, the fallback makes it OK; otherwise error.
func ghAuthFallbackStatus(t *testing.T) Status {
	t.Helper()
	err := exec.Command("gh", "auth", "status").Run()
	if err == nil {
		return StatusOK
	}
	return StatusError
}

// ghAuthFallbackCheckName returns the expected check name when config token is empty.
// If gh CLI is authenticated, the fallback passes and the check is named "github";
// otherwise it fails as "github.token".
func ghAuthFallbackCheckName(t *testing.T) string {
	t.Helper()
	err := exec.Command("gh", "auth", "status").Run()
	if err == nil {
		return "github"
	}
	return "github.token"
}

// checkNames returns all check names for error messages
func checkNames(checks []ConfigCheck) []string {
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.Name
	}
	return names
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

// ---------------------------------------------------------------------------
// checkAgentDocSize
// ---------------------------------------------------------------------------

// makeLines builds a string with n newline-terminated lines of "x".
func makeLines(n int) string {
	return strings.Repeat("x\n", n)
}

func TestCheckAgentDocSize_CleanDir(t *testing.T) {
	tmp := t.TempDir()
	agentDir := filepath.Join(tmp, ".agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a small file that should not trigger any check.
	if err := os.WriteFile(filepath.Join(agentDir, "small.md"), []byte(makeLines(100)), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := checkAgentDocSize(agentDir)
	if len(checks) != 0 {
		t.Errorf("expected 0 checks for clean dir, got %d: %+v", len(checks), checks)
	}
}

func TestCheckAgentDocSize_WarnFile(t *testing.T) {
	tmp := t.TempDir()
	agentDir := filepath.Join(tmp, ".agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 600 lines — above warn threshold (500) but below fail threshold (1000).
	if err := os.WriteFile(filepath.Join(agentDir, "big.md"), []byte(makeLines(600)), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := checkAgentDocSize(agentDir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %v", checks[0].Status)
	}
}

func TestCheckAgentDocSize_FailFile(t *testing.T) {
	tmp := t.TempDir()
	agentDir := filepath.Join(tmp, ".agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 1100 lines — above fail threshold (1000).
	if err := os.WriteFile(filepath.Join(agentDir, "bloated.md"), []byte(makeLines(1100)), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := checkAgentDocSize(agentDir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Status != StatusError {
		t.Errorf("expected StatusError, got %v", checks[0].Status)
	}
}

func TestCheckAgentDocSize_MissingDir(t *testing.T) {
	checks := checkAgentDocSize("/nonexistent/path/.agent")
	if len(checks) != 0 {
		t.Errorf("expected 0 checks for missing dir, got %d", len(checks))
	}
}

func TestCheckAgentDocSize_SubdirFile(t *testing.T) {
	tmp := t.TempDir()
	agentDir := filepath.Join(tmp, ".agent")
	subDir := filepath.Join(agentDir, "sops")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 1100-line file in a subdirectory.
	if err := os.WriteFile(filepath.Join(subDir, "deep.md"), []byte(makeLines(1100)), 0o644); err != nil {
		t.Fatal(err)
	}

	checks := checkAgentDocSize(agentDir)
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Status != StatusError {
		t.Errorf("expected StatusError, got %v", checks[0].Status)
	}
}

// ---------------------------------------------------------------------------
// Approval misconfig doctor check
// ---------------------------------------------------------------------------

func TestCheckConfig_ApprovalMisconfig_Detected(t *testing.T) {
	// env has require_approval=true but approval.pre_merge is disabled → StatusError
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	cfg.Orchestrator.Autopilot.Environments = map[string]*autopilot.EnvironmentConfig{
		"stage": {RequireApproval: true},
	}
	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = false
	cfg.Approval = approvalCfg

	checks := checkConfig(cfg)
	found := false
	for _, c := range checks {
		if c.Name == "approval-misconfig" {
			found = true
			if c.Status != StatusError {
				t.Errorf("approval-misconfig status = %v, want StatusError", c.Status)
			}
			if !strings.Contains(c.Message, "stage") {
				t.Errorf("approval-misconfig message should mention env name, got: %s", c.Message)
			}
		}
	}
	if !found {
		t.Error("expected approval-misconfig check to appear in ConfigChecks")
	}
}

func TestCheckConfig_ApprovalMisconfig_NotReported_WhenPreMergeEnabled(t *testing.T) {
	// env has require_approval=true AND approval.pre_merge is enabled → no misconfig check
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	cfg.Orchestrator.Autopilot.Environments = map[string]*autopilot.EnvironmentConfig{
		"stage": {RequireApproval: true},
	}
	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = true
	approvalCfg.PreMerge = &approval.StageConfig{Enabled: true}
	cfg.Approval = approvalCfg

	checks := checkConfig(cfg)
	for _, c := range checks {
		if c.Name == "approval-misconfig" {
			t.Errorf("unexpected approval-misconfig check when pre_merge is enabled: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// checkBrewTapHealth
// ---------------------------------------------------------------------------

// makeBrewTapGetter returns an httpGetter that serves sequential responses
// from the provided (statusCode, body) pairs.
func makeBrewTapGetter(responses []struct {
	code int
	body string
}) httpGetter {
	idx := 0
	return func(_ string) (*http.Response, error) {
		if idx >= len(responses) {
			return nil, io.EOF
		}
		r := responses[idx]
		idx++
		return &http.Response{
			StatusCode: r.code,
			Body:       io.NopCloser(strings.NewReader(r.body)),
		}, nil
	}
}

func TestCheckBrewTapHealth_NetworkError(t *testing.T) {
	get := func(_ string) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }
	check := checkBrewTapHealth(get)
	if check.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning", check.Status)
	}
	if !strings.Contains(check.Message, "could not reach") {
		t.Errorf("message = %q, want mention of 'could not reach'", check.Message)
	}
}

func TestCheckBrewTapHealth_APIError(t *testing.T) {
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{503, `{}`}})
	check := checkBrewTapHealth(get)
	if check.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning", check.Status)
	}
	if !strings.Contains(check.Message, "503") {
		t.Errorf("message = %q, want HTTP status code", check.Message)
	}
}

func TestCheckBrewTapHealth_LastRunSuccess(t *testing.T) {
	runsBody := `{"workflow_runs":[{"id":1,"conclusion":"success"}]}`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, runsBody}})
	check := checkBrewTapHealth(get)
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK", check.Status)
	}
}

func TestCheckBrewTapHealth_LastRunFailedNoHomebrewStep(t *testing.T) {
	runsBody := `{"workflow_runs":[{"id":42,"conclusion":"failure"}]}`
	jobsBody := `{"jobs":[{"steps":[{"name":"Set up Go","conclusion":"failure"},{"name":"Run GoReleaser","conclusion":"success"}]}]}`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, runsBody}, {200, jobsBody}})
	check := checkBrewTapHealth(get)
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (failure not at brew step)", check.Status)
	}
}

func TestCheckBrewTapHealth_LastRunFailedAtHomebrewStep(t *testing.T) {
	runsBody := `{"workflow_runs":[{"id":42,"conclusion":"failure"}]}`
	jobsBody := `{"jobs":[{"steps":[{"name":"Publish Homebrew Formula","conclusion":"failure"}]}]}`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, runsBody}, {200, jobsBody}})
	check := checkBrewTapHealth(get)
	if check.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning", check.Status)
	}
	if check.Fix == "" {
		t.Error("expected Fix hint for brew token rotation")
	}
	if !strings.Contains(check.Message, "Homebrew") {
		t.Errorf("message = %q, want mention of brew step name", check.Message)
	}
}

func TestCheckBrewTapHealth_NoRuns(t *testing.T) {
	runsBody := `{"workflow_runs":[]}`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, runsBody}})
	check := checkBrewTapHealth(get)
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK for empty run list", check.Status)
	}
}

func TestCheckBrewTapHealth_JobFetchFails(t *testing.T) {
	runsBody := `{"workflow_runs":[{"id":42,"conclusion":"failure"}]}`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, runsBody}, {503, `{}`}})
	check := checkBrewTapHealth(get)
	if check.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning when jobs fetch fails", check.Status)
	}
}

// ---------------------------------------------------------------------------
// checkSelfUpgradeStaleness (GH-3790)
// ---------------------------------------------------------------------------

func TestCheckSelfUpgradeStaleness_ThresholdDisabled(t *testing.T) {
	get := func(_ string) (*http.Response, error) {
		t.Fatal("should not make a network call when threshold <= 0")
		return nil, nil
	}
	check := checkSelfUpgradeStaleness(get, "2.201.2", 0)
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK", check.Status)
	}
}

func TestCheckSelfUpgradeStaleness_NetworkError(t *testing.T) {
	get := func(_ string) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }
	check := checkSelfUpgradeStaleness(get, "2.201.2", 3)
	if check.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning", check.Status)
	}
}

func TestCheckSelfUpgradeStaleness_UpToDate(t *testing.T) {
	body := `[{"tag_name":"v2.201.2","draft":false,"prerelease":false}]`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, body}})
	check := checkSelfUpgradeStaleness(get, "2.201.2", 3)
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK", check.Status)
	}
}

func TestCheckSelfUpgradeStaleness_BehindThreshold(t *testing.T) {
	body := `[
		{"tag_name":"v2.207.1","draft":false,"prerelease":false},
		{"tag_name":"v2.206.1","draft":false,"prerelease":false},
		{"tag_name":"v2.206.0","draft":false,"prerelease":false},
		{"tag_name":"v2.205.0","draft":false,"prerelease":false},
		{"tag_name":"v2.201.2","draft":false,"prerelease":false}
	]`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, body}})
	check := checkSelfUpgradeStaleness(get, "2.201.2", 3)
	if check.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning", check.Status)
	}
	if check.Fix == "" {
		t.Error("expected a Fix hint")
	}
	if !strings.Contains(check.Message, "4 releases behind") {
		t.Errorf("message = %q, want mention of release count", check.Message)
	}
}

func TestCheckSelfUpgradeStaleness_BelowThreshold(t *testing.T) {
	body := `[
		{"tag_name":"v2.202.0","draft":false,"prerelease":false},
		{"tag_name":"v2.201.2","draft":false,"prerelease":false}
	]`
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, body}})
	check := checkSelfUpgradeStaleness(get, "2.201.2", 3)
	if check.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK (1 release behind < threshold 3)", check.Status)
	}
}

// ---------------------------------------------------------------------------
// checkRunningVsDiskVersion (GH-4864)
// ---------------------------------------------------------------------------

func TestCheckRunningVsDiskVersion(t *testing.T) {
	tests := []struct {
		name       string
		get        httpGetter
		port       int
		disk       string
		wantStatus Status
		wantMsg    string
	}{
		{
			name: "running matches disk",
			get: makeBrewTapGetter([]struct {
				code int
				body string
			}{{200, `{"status":"healthy","version":"v2.259.1-29-g35450fea"}`}}),
			port:       9091,
			disk:       "v2.259.1-29-g35450fea",
			wantStatus: StatusOK,
			wantMsg:    "running matches disk",
		},
		{
			name: "running differs from disk — hot restart pending or failed",
			get: makeBrewTapGetter([]struct {
				code int
				body string
			}{{200, `{"status":"healthy","version":"v2.259.0"}`}}),
			port:       9091,
			disk:       "v2.259.1-29-g35450fea",
			wantStatus: StatusWarning,
			wantMsg:    "hot restart pending or failed",
		},
		{
			name:       "daemon unreachable — graceful skip",
			get:        func(_ string) (*http.Response, error) { return nil, io.ErrUnexpectedEOF },
			port:       9091,
			disk:       "v2.259.1-29-g35450fea",
			wantStatus: StatusOK,
			wantMsg:    "skipping running-version check",
		},
		{
			name: "gateway not configured (port 0) — graceful skip",
			get: func(_ string) (*http.Response, error) {
				t.Fatal("should not make a network call when port <= 0")
				return nil, nil
			},
			port:       0,
			disk:       "v2.259.1-29-g35450fea",
			wantStatus: StatusOK,
			wantMsg:    "skipping running-version check",
		},
		{
			name: "non-200 from /health — graceful skip",
			get: makeBrewTapGetter([]struct {
				code int
				body string
			}{{503, `{}`}}),
			port:       9091,
			disk:       "v2.259.1-29-g35450fea",
			wantStatus: StatusOK,
			wantMsg:    "skipping running-version check",
		},
		{
			name: "unparseable body — graceful skip",
			get: makeBrewTapGetter([]struct {
				code int
				body string
			}{{200, `not json`}}),
			port:       9091,
			disk:       "v2.259.1-29-g35450fea",
			wantStatus: StatusOK,
			wantMsg:    "skipping running-version check",
		},
		{
			name: "v-prefix normalized (not a false mismatch)",
			get: makeBrewTapGetter([]struct {
				code int
				body string
			}{{200, `{"status":"healthy","version":"2.259.1"}`}}),
			port:       9091,
			disk:       "v2.259.1",
			wantStatus: StatusOK,
			wantMsg:    "running matches disk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := checkRunningVsDiskVersion(tt.get, tt.port, tt.disk)
			if check.Status != tt.wantStatus {
				t.Errorf("status = %v, want %v (message: %q)", check.Status, tt.wantStatus, check.Message)
			}
			if !strings.Contains(check.Message, tt.wantMsg) {
				t.Errorf("message = %q, want substring %q", check.Message, tt.wantMsg)
			}
		})
	}
}

func TestCheckRunningVsDiskVersion_MismatchHasFix(t *testing.T) {
	get := makeBrewTapGetter([]struct {
		code int
		body string
	}{{200, `{"status":"healthy","version":"v2.259.0"}`}})
	check := checkRunningVsDiskVersion(get, 9091, "v2.259.1-29-g35450fea")
	if check.Fix == "" {
		t.Error("expected a Fix hint pointing at daemon.log for a running/disk mismatch")
	}
	if !strings.Contains(check.Fix, "upgrade verified complete") {
		t.Errorf("Fix = %q, want mention of the boot-reconcile log marker", check.Fix)
	}
}

// ---------------------------------------------------------------------------
// checkGitHubTokenLive (GH-3718)
// ---------------------------------------------------------------------------

func TestCheckGitHubTokenLive_DeadToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true, Token: "dead-token"},
		},
	}
	check := func(string) (int, error) { return http.StatusUnauthorized, nil }

	got := checkGitHubTokenLive(cfg, check)
	if got == nil {
		t.Fatal("expected a check result, got nil")
	}
	if got.Status != StatusError {
		t.Errorf("status = %v, want StatusError", got.Status)
	}
	if !strings.Contains(got.Message, "401") {
		t.Errorf("message = %q, want mention of 401", got.Message)
	}
	if !strings.Contains(got.Message, "config") {
		t.Errorf("message = %q, want token source named", got.Message)
	}
}

func TestCheckGitHubTokenLive_ValidToken(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true, Token: "good-token"},
		},
	}
	check := func(string) (int, error) { return http.StatusOK, nil }

	got := checkGitHubTokenLive(cfg, check)
	if got == nil {
		t.Fatal("expected a check result, got nil")
	}
	if got.Status != StatusOK {
		t.Errorf("status = %v, want StatusOK", got.Status)
	}
}

func TestCheckGitHubTokenLive_NetworkError(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true, Token: "some-token"},
		},
	}
	check := func(string) (int, error) { return 0, io.ErrUnexpectedEOF }

	got := checkGitHubTokenLive(cfg, check)
	if got == nil {
		t.Fatal("expected a check result, got nil")
	}
	if got.Status != StatusWarning {
		t.Errorf("status = %v, want StatusWarning for a network error (not evidence the token is dead)", got.Status)
	}
}

func TestCheckGitHubTokenLive_SkipsWhenDisabled(t *testing.T) {
	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: false, Token: "some-token"},
		},
	}
	called := false
	check := func(string) (int, error) { called = true; return http.StatusOK, nil }

	got := checkGitHubTokenLive(cfg, check)
	if got != nil {
		t.Errorf("expected nil check when GitHub disabled, got %+v", got)
	}
	if called {
		t.Error("expected live check to be skipped when GitHub disabled")
	}
}

func TestCheckGitHubTokenLive_SkipsWhenNoToken(t *testing.T) {
	origEnv, hadEnv := os.LookupEnv("GITHUB_TOKEN")
	_ = os.Unsetenv("GITHUB_TOKEN")
	origExec := ghAuthTokenExec
	ghAuthTokenExec = func() (string, error) { return "", errors.New("not authenticated") }
	t.Cleanup(func() {
		if hadEnv {
			_ = os.Setenv("GITHUB_TOKEN", origEnv)
		}
		ghAuthTokenExec = origExec
	})

	cfg := &config.Config{
		Adapters: &config.AdaptersConfig{
			GitHub: &github.Config{Enabled: true},
		},
	}
	called := false
	check := func(string) (int, error) { called = true; return http.StatusOK, nil }

	got := checkGitHubTokenLive(cfg, check)
	if got != nil {
		t.Errorf("expected nil check when no token resolves, got %+v", got)
	}
	if called {
		t.Error("expected live check to be skipped when no token resolves")
	}
}

func TestCheckConfig_ApprovalMisconfig_NotReported_WhenNoEnvRequiresApproval(t *testing.T) {
	// No env requires approval → no misconfig check even if approval is disabled
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	cfg.Orchestrator.Autopilot.Environments = map[string]*autopilot.EnvironmentConfig{
		"stage": {RequireApproval: false},
	}
	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = false
	cfg.Approval = approvalCfg

	checks := checkConfig(cfg)
	for _, c := range checks {
		if c.Name == "approval-misconfig" {
			t.Errorf("unexpected approval-misconfig check when no env requires approval: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// GH-4774: per-project approval overlay doctor checks
// ---------------------------------------------------------------------------

func TestCheckConfig_ApprovalMisconfig_Detected_ProjectOverride(t *testing.T) {
	// env does NOT require approval, but a project's approval overlay does —
	// with approval.pre_merge disabled, that project's PRs will deadlock.
	requireApproval := true
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	cfg.Orchestrator.Autopilot.Environments = map[string]*autopilot.EnvironmentConfig{
		"stage": {RequireApproval: false},
	}
	cfg.Projects = []*config.ProjectConfig{
		{Name: "work-app", Approval: &autopilot.ProjectApprovalOverride{RequireApproval: &requireApproval}},
	}
	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = false
	cfg.Approval = approvalCfg

	checks := checkConfig(cfg)
	found := false
	for _, c := range checks {
		if c.Name == "approval-misconfig" {
			found = true
			if c.Status != StatusError {
				t.Errorf("approval-misconfig status = %v, want StatusError", c.Status)
			}
			if !strings.Contains(c.Message, "work-app") {
				t.Errorf("approval-misconfig message should mention the project name, got: %s", c.Message)
			}
		}
	}
	if !found {
		t.Error("expected approval-misconfig check to appear in ConfigChecks for a project override")
	}
}

func TestCheckConfig_ApprovalSourceUnregistered_Detected(t *testing.T) {
	// Global approval_source is telegram, but no Telegram adapter/token is
	// configured — the resolved source has no registered handler.
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	cfg.Orchestrator.Autopilot.Enabled = true
	cfg.Orchestrator.Autopilot.ApprovalSource = autopilot.ApprovalSourceTelegram
	cfg.Adapters.Telegram = nil

	checks := checkConfig(cfg)
	found := false
	for _, c := range checks {
		if c.Name == "approval-source-unregistered" {
			found = true
			if c.Status != StatusError {
				t.Errorf("approval-source-unregistered status = %v, want StatusError", c.Status)
			}
			if !strings.Contains(c.Message, "telegram") {
				t.Errorf("approval-source-unregistered message should mention the source, got: %s", c.Message)
			}
		}
	}
	if !found {
		t.Error("expected approval-source-unregistered check when approval_source has no registered handler")
	}
}

func TestCheckConfig_ApprovalSourceUnregistered_NotReported_WhenRegistered(t *testing.T) {
	slackSource := autopilot.ApprovalSourceSlack
	cfg := config.DefaultConfig()
	cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
	cfg.Orchestrator.Autopilot.Enabled = true
	cfg.Orchestrator.Autopilot.ApprovalSource = autopilot.ApprovalSourceTelegram
	cfg.Adapters.Telegram = &telegram.Config{Enabled: true, BotToken: "test-bot-token"}
	cfg.Projects = []*config.ProjectConfig{
		{Name: "work-app", Approval: &autopilot.ProjectApprovalOverride{ApprovalSource: &slackSource}},
	}
	cfg.Adapters.Slack = &slack.Config{
		Enabled:  true,
		BotToken: "test-slack-bot-token",
		Approval: &slack.ApprovalConfig{Enabled: true},
	}

	checks := checkConfig(cfg)
	for _, c := range checks {
		if c.Name == "approval-source-unregistered" {
			t.Errorf("unexpected approval-source-unregistered check when both sources have registered handlers: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// GH-3826: Telegram approval-stranding checks
// ---------------------------------------------------------------------------

func telegramStrandingConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Adapters.Telegram = &telegram.Config{
		Enabled:  true,
		BotToken: "test-bot-token",
		Polling:  false,
	}
	approvalCfg := approval.DefaultConfig()
	approvalCfg.Enabled = true
	approvalCfg.PreMerge = &approval.StageConfig{Enabled: true}
	cfg.Approval = approvalCfg
	return cfg
}

func TestTelegramApprovalStranding_Detected(t *testing.T) {
	cfg := telegramStrandingConfig()

	msg := TelegramApprovalStranding(cfg)
	if msg == "" {
		t.Fatal("expected stranding warning when telegram is enabled, polling is off, and pre_merge approval is enabled")
	}
	if !strings.Contains(msg, "telegram") {
		t.Errorf("expected message to mention telegram, got: %s", msg)
	}
}

func TestTelegramApprovalStranding_NotReported_WhenPollingEnabled(t *testing.T) {
	cfg := telegramStrandingConfig()
	cfg.Adapters.Telegram.Polling = true

	if msg := TelegramApprovalStranding(cfg); msg != "" {
		t.Errorf("expected no stranding warning when polling is enabled, got: %s", msg)
	}
}

func TestTelegramApprovalStranding_NotReported_WhenTelegramDisabled(t *testing.T) {
	cfg := telegramStrandingConfig()
	cfg.Adapters.Telegram.Enabled = false

	if msg := TelegramApprovalStranding(cfg); msg != "" {
		t.Errorf("expected no stranding warning when telegram is disabled, got: %s", msg)
	}
}

func TestTelegramApprovalStranding_NotReported_WhenTelegramApprovalChannelDisabled(t *testing.T) {
	cfg := telegramStrandingConfig()
	cfg.Adapters.Telegram.Approval = &telegram.ApprovalConfig{Enabled: false}

	if msg := TelegramApprovalStranding(cfg); msg != "" {
		t.Errorf("expected no stranding warning when telegram approval channel is disabled, got: %s", msg)
	}
}

func TestTelegramApprovalStranding_NotReported_WhenNoApprovalStageEnabled(t *testing.T) {
	cfg := telegramStrandingConfig()
	cfg.Approval.PreMerge.Enabled = false

	if msg := TelegramApprovalStranding(cfg); msg != "" {
		t.Errorf("expected no stranding warning when no approval stage is enabled, got: %s", msg)
	}
}

func TestTelegramApprovalStranding_NotReported_WhenNoBotToken(t *testing.T) {
	cfg := telegramStrandingConfig()
	cfg.Adapters.Telegram.BotToken = ""

	if msg := TelegramApprovalStranding(cfg); msg != "" {
		t.Errorf("expected no stranding warning when bot token is missing, got: %s", msg)
	}
}

func TestCheckConfig_TelegramApprovalStranding_Detected(t *testing.T) {
	cfg := telegramStrandingConfig()

	checks := checkConfig(cfg)
	found := findConfigCheck(checks, "telegram-approval-stranding")
	if found == nil {
		t.Fatal("expected 'telegram-approval-stranding' check to appear in ConfigChecks")
	}
	if found.Status != StatusError {
		t.Errorf("telegram-approval-stranding status = %v, want StatusError", found.Status)
	}
}

func TestCheckConfig_TelegramApprovalStranding_NotReported_WhenPollingEnabled(t *testing.T) {
	cfg := telegramStrandingConfig()
	cfg.Adapters.Telegram.Polling = true

	checks := checkConfig(cfg)
	if found := findConfigCheck(checks, "telegram-approval-stranding"); found != nil {
		t.Errorf("unexpected telegram-approval-stranding check when polling is enabled: %+v", found)
	}
}
