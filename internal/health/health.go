// Package health provides system health checks for Pilot.
//
// It verifies required dependencies (Claude Code CLI, git) are installed
// and checks feature availability based on configuration. The RunChecks function
// generates a HealthReport used by the CLI status command to display system
// readiness and configuration state.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/health/verify"
	"github.com/qf-studio/pilot/internal/upgrade"
)

// Status represents feature or dependency status
type Status int

const (
	StatusOK Status = iota
	StatusWarning
	StatusError
	StatusDisabled
)

// Check represents a health check result
type Check struct {
	Name    string
	Status  Status
	Message string
	Fix     string
}

// ConfigCheck represents a configuration check result
type ConfigCheck struct {
	Name    string
	Status  Status
	Message string
	Fix     string
}

// FeatureStatus represents a feature with its availability
type FeatureStatus struct {
	Name     string
	Enabled  bool
	Status   Status
	Note     string
	Missing  []string // What's missing to enable this feature
	Degraded bool     // Feature works but with reduced functionality
}

// HealthReport contains all health check results
type HealthReport struct {
	Dependencies []Check
	Config       []ConfigCheck
	Features     []FeatureStatus
	Subsystems   []SubsystemCheck
	Projects     int
	HasErrors    bool
	HasWarnings  bool
}

// agentDocWarnLines is the line count at which a .agent/*.md file triggers a warning.
const agentDocWarnLines = 500

// agentDocFailLines is the line count at which a .agent/*.md file triggers an error.
const agentDocFailLines = 1000

// checkAgentDocSize walks agentDir for .md files and returns ConfigChecks for
// files that exceed the warn or fail thresholds.
func checkAgentDocSize(agentDir string) []ConfigCheck {
	var checks []ConfigCheck

	_ = filepath.WalkDir(agentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lineCount := strings.Count(string(data), "\n")
		if len(data) > 0 && data[len(data)-1] != '\n' {
			lineCount++
		}
		if lineCount <= agentDocWarnLines {
			return nil
		}

		rel, _ := filepath.Rel(filepath.Dir(agentDir), path)
		if rel == "" {
			rel = path
		}

		if lineCount > agentDocFailLines {
			checks = append(checks, ConfigCheck{
				Name:    rel,
				Status:  StatusError,
				Message: fmt.Sprintf("%d lines (limit: %d)", lineCount, agentDocFailLines),
				Fix:     fmt.Sprintf("Archive or trim %s to under %d lines", rel, agentDocFailLines),
			})
		} else {
			checks = append(checks, ConfigCheck{
				Name:    rel,
				Status:  StatusWarning,
				Message: fmt.Sprintf("%d lines (warn at: %d)", lineCount, agentDocWarnLines),
				Fix:     fmt.Sprintf("Consider archiving sections of %s", rel),
			})
		}
		return nil
	})

	return checks
}

// httpGetter is an injectable HTTP GET function for testability.
type httpGetter func(url string) (*http.Response, error)

// brewTapHTTPGet is the default HTTP getter. Override in tests.
var brewTapHTTPGet httpGetter = func(url string) (*http.Response, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pilot-doctor/1.0")
	return client.Do(req)
}

// githubAuthChecker performs an authenticated GitHub API call to validate a
// token, returning its HTTP status code (or an error for network failures).
// Injectable for testability; overridden in tests. GH-3718.
type githubAuthChecker func(token string) (int, error)

// githubAuthCheck is the default githubAuthChecker: a real GET /user call.
var githubAuthCheck githubAuthChecker = func(token string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode, nil
}

// ghAuthTokenExec runs `gh auth token` and returns the trimmed token.
// Injectable so tests aren't at the mercy of the host's real gh CLI auth state.
var ghAuthTokenExec = func() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveGitHubTokenForDoctor mirrors cmd/pilot's resolveGitHubToken precedence
// (config -> GITHUB_TOKEN env -> `gh auth token` CLI) without importing cmd/pilot,
// which would create an import cycle. Returns ("", "") when nothing resolves.
func resolveGitHubTokenForDoctor(cfg *config.Config) (token, source string) {
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Token != "" {
		return cfg.Adapters.GitHub.Token, "config"
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, "env GITHUB_TOKEN"
	}
	if tok, err := ghAuthTokenExec(); err == nil && tok != "" {
		return tok, "gh CLI"
	}
	return "", ""
}

// checkGitHubTokenLive makes one authenticated API call to confirm the
// resolved GitHub token actually works, producing a clear pass/fail line
// distinct from the presence-only "github"/"github.token" checks above.
// Returns nil when GitHub isn't enabled or no token could be resolved — those
// cases are already covered by the presence check. GH-3718.
func checkGitHubTokenLive(cfg *config.Config, check githubAuthChecker) *ConfigCheck {
	if cfg.Adapters == nil || cfg.Adapters.GitHub == nil || !cfg.Adapters.GitHub.Enabled {
		return nil
	}
	token, source := resolveGitHubTokenForDoctor(cfg)
	if token == "" {
		return nil
	}

	status, err := check(token)
	if err != nil {
		return &ConfigCheck{
			Name:    "github.token.live",
			Status:  StatusWarning,
			Message: fmt.Sprintf("could not verify token (%s source): %v", source, err),
			Fix:     "Check network connectivity to api.github.com",
		}
	}
	if status == http.StatusUnauthorized {
		return &ConfigCheck{
			Name:    "github.token.live",
			Status:  StatusError,
			Message: fmt.Sprintf("token invalid or expired (401, source: %s)", source),
			Fix:     "Rotate adapters.github.token / GITHUB_TOKEN, or run: gh auth login",
		}
	}
	if status < 200 || status >= 300 {
		return &ConfigCheck{
			Name:    "github.token.live",
			Status:  StatusWarning,
			Message: fmt.Sprintf("unexpected API response (%d, source: %s)", status, source),
			Fix:     "Check GitHub API status",
		}
	}
	return &ConfigCheck{
		Name:    "github.token.live",
		Status:  StatusOK,
		Message: fmt.Sprintf("valid (source: %s)", source),
	}
}

// verifyProbeTimeout bounds each adapter's live Verify(ctx) call during
// doctor checks so an unreachable API can't hang `pilot doctor`.
const verifyProbeTimeout = 5 * time.Second

// checkAdapterVerify runs a live verify.Verifiable probe and turns the
// result into a single ConfigCheck, tagging failures with tokenSource so a
// dead credential can be diagnosed without re-deriving where it was
// resolved from. This generalizes the GitHub-only mechanism in
// checkGitHubTokenLive (GH-3718) to any adapter that implements Verifiable
// (GH-3769) — callers only call it once the fast, network-free presence
// pre-check (token/config non-empty) has already passed.
func checkAdapterVerify(checkName, tokenSource string, v verify.Verifiable) ConfigCheck {
	ctx, cancel := context.WithTimeout(context.Background(), verifyProbeTimeout)
	defer cancel()

	err := v.Verify(ctx)
	switch {
	case err == nil:
		msg := "valid"
		if tokenSource != "" {
			msg = fmt.Sprintf("valid (source: %s)", tokenSource)
		}
		return ConfigCheck{Name: checkName, Status: StatusOK, Message: msg}
	case errors.Is(err, verify.ErrProbeNotImplemented):
		// No live probe wired up for this adapter yet — presence already
		// passed, so report configured-but-unchecked rather than red.
		return ConfigCheck{Name: checkName, Status: StatusOK, Message: "configured (no live probe yet)"}
	default:
		msg := err.Error()
		if tokenSource != "" {
			msg = fmt.Sprintf("%s (source: %s)", msg, tokenSource)
		}
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusError,
			Message: msg,
			Fix:     fmt.Sprintf("Check %s credentials are valid and not expired", checkName),
		}
	}
}

// checkTelegramTokenLive makes one live getUpdates call to confirm the
// configured Telegram bot token actually works, mirroring
// checkGitHubTokenLive for the Telegram adapter (GH-3769). Returns nil when
// Telegram isn't enabled or has no token — those cases are already covered
// by the telegram.bot_token presence check.
func checkTelegramTokenLive(cfg *config.Config, newClient func(token string) verify.Verifiable) *ConfigCheck {
	if cfg.Adapters == nil || cfg.Adapters.Telegram == nil || !cfg.Adapters.Telegram.Enabled {
		return nil
	}
	token := cfg.Adapters.Telegram.BotToken
	if token == "" {
		return nil
	}
	check := checkAdapterVerify("telegram.token.live", "", newClient(token))
	return &check
}

// checkSlackTokenLive makes one live auth.test call to confirm the
// configured Slack bot token actually works, mirroring
// checkGitHubTokenLive for the Slack adapter (GH-3769). Returns nil when
// Slack isn't enabled or has no token — those cases are already covered by
// the slack.bot_token presence check.
func checkSlackTokenLive(cfg *config.Config, newClient func(token string) verify.Verifiable) *ConfigCheck {
	if cfg.Adapters == nil || cfg.Adapters.Slack == nil || !cfg.Adapters.Slack.Enabled {
		return nil
	}
	token := cfg.Adapters.Slack.BotToken
	if token == "" {
		return nil
	}
	check := checkAdapterVerify("slack.token.live", "", newClient(token))
	return &check
}

// newTelegramVerifier and newSlackVerifier build the real live-probe client
// for checkTelegramTokenLive/checkSlackTokenLive. Injectable so tests can
// substitute fake verify.Verifiable implementations instead of hitting the
// real Telegram/Slack APIs.
var newTelegramVerifier = func(token string) verify.Verifiable { return telegram.NewClient(token) }
var newSlackVerifier = func(token string) verify.Verifiable { return slack.NewClient(token) }

// checkBrewTapHealth checks whether the last release.yml run failed at a
// homebrew step, which indicates that HOMEBREW_TAP_GITHUB_TOKEN has expired.
// Uses unauthenticated GitHub API calls (public repo, 60 req/hour limit).
func checkBrewTapHealth(get httpGetter) ConfigCheck {
	const checkName = "brew-tap-token"
	const runsURL = "https://api.github.com/repos/qf-studio/pilot/actions/workflows/release.yml/runs?per_page=1"

	resp, err := get(runsURL)
	if err != nil {
		return ConfigCheck{Name: checkName, Status: StatusWarning, Message: "could not reach GitHub API"}
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusWarning,
			Message: fmt.Sprintf("GitHub API returned %d", resp.StatusCode),
		}
	}

	var runsPayload struct {
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&runsPayload); err != nil || len(runsPayload.WorkflowRuns) == 0 {
		return ConfigCheck{Name: checkName, Status: StatusOK, Message: "no recent release runs"}
	}

	lastRun := runsPayload.WorkflowRuns[0]
	if lastRun.Conclusion != "failure" {
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusOK,
			Message: fmt.Sprintf("last release: %s", lastRun.Conclusion),
		}
	}

	// Run failed — check if the failed step name contains "homebrew".
	jobsURL := fmt.Sprintf("https://api.github.com/repos/qf-studio/pilot/actions/runs/%d/jobs", lastRun.ID)
	jobsResp, err := get(jobsURL)
	if err != nil || jobsResp.StatusCode != http.StatusOK {
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusWarning,
			Message: "last release failed (could not fetch job steps)",
		}
	}
	defer jobsResp.Body.Close() //nolint:errcheck

	var jobsPayload struct {
		Jobs []struct {
			Steps []struct {
				Name       string `json:"name"`
				Conclusion string `json:"conclusion"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.NewDecoder(jobsResp.Body).Decode(&jobsPayload); err != nil {
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusWarning,
			Message: "last release failed (could not parse step data)",
		}
	}

	for _, job := range jobsPayload.Jobs {
		for _, step := range job.Steps {
			if step.Conclusion == "failure" && strings.Contains(strings.ToLower(step.Name), "homebrew") {
				return ConfigCheck{
					Name:    checkName,
					Status:  StatusWarning,
					Message: fmt.Sprintf("last release failed at %q — HOMEBREW_TAP_GITHUB_TOKEN may be expired", step.Name),
					Fix:     "Rotate HOMEBREW_TAP_GITHUB_TOKEN in GitHub repo secrets",
				}
			}
		}
	}

	return ConfigCheck{Name: checkName, Status: StatusOK, Message: "last release failed (not at brew step)"}
}

// defaultStaleReleaseThreshold mirrors config.UpgradeConfig's default so
// doctor still warns when cfg.Upgrade is unset (e.g. loaded via
// config.DefaultConfig() paths that predate the field).
const defaultStaleReleaseThreshold = 3

// checkSelfUpgradeStaleness compares the running version against GitHub
// releases and warns when the daemon has fallen threshold-or-more releases
// behind (GH-3790: self-upgrade previously had no automatic trigger, so a
// daemon could silently run 8+ releases stale with nothing surfacing it).
// This is the doctor/one-shot counterpart to the periodic runtime check in
// upgrade.VersionChecker — same threshold and comparison, different cadence.
func checkSelfUpgradeStaleness(get httpGetter, currentVersion string, threshold int) ConfigCheck {
	const checkName = "self-upgrade.staleness"

	if threshold <= 0 {
		return ConfigCheck{Name: checkName, Status: StatusOK, Message: "check disabled"}
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", upgrade.GitHubRepo, releasesCheckLimit)
	resp, err := get(url)
	if err != nil {
		return ConfigCheck{Name: checkName, Status: StatusWarning, Message: "could not reach GitHub API"}
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusWarning,
			Message: fmt.Sprintf("GitHub API returned %d", resp.StatusCode),
		}
	}

	var releases []upgrade.Release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return ConfigCheck{Name: checkName, Status: StatusWarning, Message: "could not parse releases"}
	}

	behind := upgrade.ReleasesBehind(releases, currentVersion)
	if behind >= threshold {
		return ConfigCheck{
			Name:    checkName,
			Status:  StatusWarning,
			Message: fmt.Sprintf("%d releases behind (running %s)", behind, currentVersion),
			Fix:     "Check ~/.pilot/logs/daemon.log for hot-upgrade errors, or restart the daemon manually",
		}
	}

	return ConfigCheck{Name: checkName, Status: StatusOK, Message: fmt.Sprintf("up to date (%d behind)", behind)}
}

// releasesCheckLimit mirrors upgrade.releasesFetchLimit — kept as a separate
// constant since it belongs to a different package's public API surface.
const releasesCheckLimit = 30

// RunChecks performs all health checks based on config. currentVersion is
// compared against GitHub releases for the self-upgrade staleness check
// (GH-3790); pass "" to skip it.
func RunChecks(cfg *config.Config, currentVersion string) *HealthReport {
	// Determine active backend type from config
	backendType := "claude-code" // default
	if cfg.Executor != nil && cfg.Executor.Type != "" {
		backendType = cfg.Executor.Type
	}

	configChecks := checkConfig(cfg)
	if cwd, err := os.Getwd(); err == nil {
		configChecks = append(configChecks, checkAgentDocSize(filepath.Join(cwd, ".agent"))...)
	}
	configChecks = append(configChecks, checkBrewTapHealth(brewTapHTTPGet))
	if liveCheck := checkGitHubTokenLive(cfg, githubAuthCheck); liveCheck != nil {
		configChecks = append(configChecks, *liveCheck)
	}
	if liveCheck := checkTelegramTokenLive(cfg, newTelegramVerifier); liveCheck != nil {
		configChecks = append(configChecks, *liveCheck)
	}
	if liveCheck := checkSlackTokenLive(cfg, newSlackVerifier); liveCheck != nil {
		configChecks = append(configChecks, *liveCheck)
	}
	if currentVersion != "" {
		threshold := defaultStaleReleaseThreshold
		if cfg.Upgrade != nil {
			threshold = cfg.Upgrade.StaleReleaseThreshold
		}
		configChecks = append(configChecks, checkSelfUpgradeStaleness(brewTapHTTPGet, currentVersion, threshold))
	}

	report := &HealthReport{
		Dependencies: checkDependenciesWithBackend(backendType),
		Config:       configChecks,
		Features:     checkFeatures(cfg),
		Subsystems:   CheckDisabledSubsystems(cfg),
		Projects:     len(cfg.Projects),
	}

	// Check for errors/warnings
	for _, d := range report.Dependencies {
		if d.Status == StatusError {
			report.HasErrors = true
		}
		if d.Status == StatusWarning {
			report.HasWarnings = true
		}
	}
	for _, c := range report.Config {
		if c.Status == StatusError {
			report.HasErrors = true
		}
		if c.Status == StatusWarning {
			report.HasWarnings = true
		}
	}
	for _, f := range report.Features {
		if f.Status == StatusError {
			report.HasErrors = true
		}
		if f.Status == StatusWarning || f.Degraded {
			report.HasWarnings = true
		}
	}

	return report
}

// backendInfo holds metadata about a backend for health checks
type backendInfo struct {
	name        string   // display name (e.g., "claude")
	backendType string   // executor.BackendType constant (e.g., "claude-code")
	command     string   // CLI command to check
	versionArgs []string // args to get version (e.g., ["--version"])
	installCmd  string   // install instruction
}

var backends = []backendInfo{
	{
		name:        "claude",
		backendType: "claude-code",
		command:     "claude",
		versionArgs: []string{"--version"},
		installCmd:  "npm install -g @anthropic-ai/claude-code",
	},
	{
		name:        "qwen",
		backendType: "qwen-code",
		command:     "qwen",
		versionArgs: []string{"--version"},
		installCmd:  "See https://github.com/anthropics/qwen-code",
	},
	{
		name:        "opencode",
		backendType: "opencode",
		command:     "opencode",
		versionArgs: []string{"version"},
		installCmd:  "go install github.com/opencode-ai/opencode@latest",
	},
}

// checkDependencies checks required system dependencies
func checkDependencies() []Check {
	// Use default backend type for backwards compatibility
	return checkDependenciesWithBackend("claude-code")
}

// checkDependenciesWithBackend checks dependencies including backend-aware checks
func checkDependenciesWithBackend(activeBackendType string) []Check {
	checks := []Check{}

	// Check Git first (always required)
	if version := getCommandVersion("git", "--version"); version != "" {
		checks = append(checks, Check{
			Name:    "git",
			Status:  StatusOK,
			Message: version,
		})
	} else {
		checks = append(checks, Check{
			Name:    "git",
			Status:  StatusError,
			Message: "not found",
			Fix:     "brew install git",
		})
	}

	// Check gh CLI (optional, for PRs)
	if version := getCommandVersion("gh", "--version"); version != "" {
		checks = append(checks, Check{
			Name:    "gh",
			Status:  StatusOK,
			Message: version,
		})
	} else {
		checks = append(checks, Check{
			Name:    "gh",
			Status:  StatusWarning,
			Message: "not found (PR creation unavailable)",
			Fix:     "brew install gh && gh auth login",
		})
	}

	// Check all backends (active backend is required, others are optional)
	for _, backend := range backends {
		isActive := backend.backendType == activeBackendType
		version := getCommandVersion(backend.command, backend.versionArgs...)

		if version != "" {
			message := version
			if isActive {
				message = version + " [active backend]"
			}
			checks = append(checks, Check{
				Name:    backend.name,
				Status:  StatusOK,
				Message: message,
			})
		} else {
			if isActive {
				// Active backend missing is an error
				checks = append(checks, Check{
					Name:    backend.name,
					Status:  StatusError,
					Message: "not found [active backend]",
					Fix:     backend.installCmd,
				})
			} else {
				// Other backends missing is informational (skip)
				checks = append(checks, Check{
					Name:    backend.name,
					Status:  StatusDisabled,
					Message: "not installed (optional)",
				})
			}
		}
	}

	// Check Mac sleep status (macOS only)
	if runtime.GOOS == "darwin" {
		checks = append(checks, checkMacSleep())
	}

	return checks
}

// checkMacSleep checks if Mac sleep is disabled for always-on operation
func checkMacSleep() Check {
	out, err := exec.Command("pmset", "-g", "custom").Output()
	if err != nil {
		return Check{
			Name:    "sleep",
			Status:  StatusWarning,
			Message: "could not check",
		}
	}

	// Look for "sleep" setting - format is "sleep		0" or "sleep		1"
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "sleep") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[1] == "0" {
				return Check{
					Name:    "sleep",
					Status:  StatusOK,
					Message: "disabled (always-on)",
				}
			}
		}
	}

	return Check{
		Name:    "sleep",
		Status:  StatusWarning,
		Message: "enabled (Pilot may pause when idle)",
		Fix:     "pilot setup --no-sleep",
	}
}

// issueSourceAdapters lists config paths to adapters that can ingest issues
// for Pilot to execute. At least one must be enabled for the daemon to
// actually pick up work — otherwise `pilot start` launches a no-op poller.
func enabledIssueSources(cfg *config.Config) []string {
	if cfg.Adapters == nil {
		return nil
	}
	var enabled []string
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		enabled = append(enabled, "github")
	}
	if cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled {
		enabled = append(enabled, "gitlab")
	}
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		enabled = append(enabled, "linear")
	}
	if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
		enabled = append(enabled, "jira")
	}
	if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
		enabled = append(enabled, "asana")
	}
	if cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled {
		enabled = append(enabled, "azure_devops")
	}
	if cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled {
		enabled = append(enabled, "plane")
	}
	return enabled
}

func checkIssueSourceAdapter(cfg *config.Config) ConfigCheck {
	enabled := enabledIssueSources(cfg)
	if len(enabled) == 0 {
		return ConfigCheck{
			Name:    "adapters",
			Status:  StatusWarning,
			Message: "no issue source enabled",
			Fix:     "Enable at least one of: github, gitlab, linear, jira, asana, azure_devops, plane. Run 'pilot setup'.",
		}
	}
	return ConfigCheck{
		Name:    "adapters",
		Status:  StatusOK,
		Message: strings.Join(enabled, ", "),
	}
}

// TelegramApprovalStranding returns a non-empty warning when Telegram is
// configured as an approval channel (it will send approve/reject requests)
// but has no inbound polling running to receive the button tap that answers
// them (GH-3826). Decisions in this configuration can only resolve via the
// approval stage's timeout/default_action, never via the approver's actual
// choice. Returns "" when there's no stranding risk.
func TelegramApprovalStranding(cfg *config.Config) string {
	if cfg == nil || cfg.Adapters == nil || cfg.Adapters.Telegram == nil {
		return ""
	}
	tg := cfg.Adapters.Telegram
	if !tg.Enabled || tg.BotToken == "" || tg.Polling {
		return ""
	}
	if tg.Approval != nil && !tg.Approval.Enabled {
		return ""
	}

	ac := cfg.Approval
	if ac == nil || !ac.Enabled {
		return ""
	}
	stageEnabled := (ac.PreMerge != nil && ac.PreMerge.Enabled) ||
		(ac.PreExecution != nil && ac.PreExecution.Enabled) ||
		(ac.PostFailure != nil && ac.PostFailure.Enabled)
	if !stageEnabled {
		return ""
	}

	return "approval channel telegram is send-only in this configuration — approvals will strand"
}

// checkConfig validates configuration
func checkConfig(cfg *config.Config) []ConfigCheck {
	checks := []ConfigCheck{}

	// Check config file exists
	configPath := config.DefaultConfigPath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		checks = append(checks, ConfigCheck{
			Name:    "config file",
			Status:  StatusWarning,
			Message: "using defaults",
			Fix:     "pilot init",
		})
	} else {
		checks = append(checks, ConfigCheck{
			Name:    "config file",
			Status:  StatusOK,
			Message: configPath,
		})
	}

	// Check Telegram config
	if cfg.Adapters != nil && cfg.Adapters.Telegram != nil {
		if cfg.Adapters.Telegram.Enabled {
			if cfg.Adapters.Telegram.BotToken != "" {
				checks = append(checks, ConfigCheck{
					Name:    "telegram.bot_token",
					Status:  StatusOK,
					Message: "configured",
				})
			} else {
				checks = append(checks, ConfigCheck{
					Name:    "telegram.bot_token",
					Status:  StatusError,
					Message: "missing",
					Fix:     "Get token from @BotFather and add to config",
				})
			}

			// Check transcription config
			if cfg.Adapters.Telegram.Transcription != nil {
				if cfg.Adapters.Telegram.Transcription.OpenAIAPIKey != "" {
					checks = append(checks, ConfigCheck{
						Name:    "transcription.openai_api_key",
						Status:  StatusOK,
						Message: "configured (voice enabled)",
					})
				} else {
					checks = append(checks, ConfigCheck{
						Name:    "transcription.openai_api_key",
						Status:  StatusWarning,
						Message: "missing (voice disabled)",
						Fix:     "export OPENAI_API_KEY=\"sk-...\" or add to config",
					})
				}
			}
		}
	}

	// Check Slack config
	if cfg.Adapters != nil && cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		if cfg.Adapters.Slack.BotToken != "" {
			checks = append(checks, ConfigCheck{
				Name:    "slack.bot_token",
				Status:  StatusOK,
				Message: "configured",
			})
		} else {
			checks = append(checks, ConfigCheck{
				Name:    "slack.bot_token",
				Status:  StatusError,
				Message: "enabled but token missing",
				Fix:     "Add xoxb-... token to config",
			})
		}
	}

	// Check that at least one issue-source adapter is enabled when projects
	// are configured. Prevents silent "no poller running" when a user has
	// `projects:` set but no `adapters:` block (GH-2361).
	if len(cfg.Projects) > 0 {
		checks = append(checks, checkIssueSourceAdapter(cfg))
	}

	// Check projects
	if len(cfg.Projects) == 0 {
		checks = append(checks, ConfigCheck{
			Name:    "projects",
			Status:  StatusWarning,
			Message: "none configured",
			Fix:     "Add projects to config.yaml",
		})
	} else {
		validProjects := 0
		for _, p := range cfg.Projects {
			path := expandPath(p.Path)
			if _, err := os.Stat(path); err == nil {
				validProjects++
			}
		}
		if validProjects == len(cfg.Projects) {
			checks = append(checks, ConfigCheck{
				Name:    "projects",
				Status:  StatusOK,
				Message: fmt.Sprintf("%d configured", len(cfg.Projects)),
			})
		} else {
			checks = append(checks, ConfigCheck{
				Name:    "projects",
				Status:  StatusWarning,
				Message: fmt.Sprintf("%d/%d valid paths", validProjects, len(cfg.Projects)),
				Fix:     "Check project paths in config.yaml",
			})
		}
	}

	// Check GitHub config
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		hasToken := cfg.Adapters.GitHub.Token != ""
		// Fallback: check if gh CLI is authenticated
		if !hasToken {
			err := exec.Command("gh", "auth", "status").Run()
			hasToken = err == nil
		}

		if !hasToken {
			checks = append(checks, ConfigCheck{
				Name:    "github.token",
				Status:  StatusError,
				Message: "enabled but token missing",
				Fix:     "Add github.token to config or run: gh auth login",
			})
		} else {
			// Token present — check repo configuration for polling mode
			hasDefaultRepo := cfg.Adapters.GitHub.Repo != ""
			hasProjectRepos := false
			for _, p := range cfg.Projects {
				if p.GitHub != nil && p.GitHub.Owner != "" && p.GitHub.Repo != "" {
					hasProjectRepos = true
					break
				}
			}
			pollingEnabled := cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled

			if pollingEnabled && !hasDefaultRepo && !hasProjectRepos {
				checks = append(checks, ConfigCheck{
					Name:    "github.repos",
					Status:  StatusWarning,
					Message: "polling enabled but no repos configured",
					Fix:     "Set adapters.github.repo (\"owner/repo\") or add github.owner/repo to each project",
				})
			} else {
				checks = append(checks, ConfigCheck{
					Name:    "github",
					Status:  StatusOK,
					Message: "configured",
				})
			}
		}
	}

	// Detect approval/env mismatch: an env has require_approval=true but the
	// approval.pre_merge stage is disabled → every PR in that env deadlocks.
	if cfg.Orchestrator != nil && cfg.Orchestrator.Autopilot != nil &&
		cfg.Orchestrator.Autopilot.Environments != nil {
		preMergeEnabled := cfg.Approval != nil &&
			cfg.Approval.Enabled &&
			cfg.Approval.PreMerge != nil &&
			cfg.Approval.PreMerge.Enabled
		for envName, envCfg := range cfg.Orchestrator.Autopilot.Environments {
			if envCfg != nil && envCfg.RequireApproval && !preMergeEnabled {
				checks = append(checks, ConfigCheck{
					Name:    "approval-misconfig",
					Status:  StatusError,
					Message: fmt.Sprintf("env %q has require_approval=true but approval.pre_merge.enabled=false → all PRs will deadlock until enabled or env is changed", envName),
					Fix:     "Set approval.enabled: true + approval.pre_merge.enabled: true + add an approver, or set require_approval: false for the environment",
				})
				break // one diagnostic per run is enough
			}
		}
	}

	// GH-3826: Telegram is registered as an approval channel (sends approve/reject
	// requests) but has no inbound polling running to receive the button tap —
	// approvals can only resolve via the stage timeout/default_action, not the
	// approver's decision.
	if msg := TelegramApprovalStranding(cfg); msg != "" {
		checks = append(checks, ConfigCheck{
			Name:    "telegram-approval-stranding",
			Status:  StatusError,
			Message: msg + " (adapters.telegram.polling=false — no inbound path for approve/reject taps)",
			Fix:     "Set adapters.telegram.polling: true (or start with --telegram) so button taps can be received, or remove Telegram as an approval channel",
		})
	}

	// Check daily brief schedule
	if cfg.Orchestrator != nil && cfg.Orchestrator.DailyBrief != nil {
		if cfg.Orchestrator.DailyBrief.Enabled {
			if cfg.Orchestrator.DailyBrief.Schedule == "" {
				checks = append(checks, ConfigCheck{
					Name:    "daily_brief.schedule",
					Status:  StatusWarning,
					Message: "enabled but no schedule set",
					Fix:     "Add schedule: \"0 9 * * 1-5\" to config",
				})
			} else {
				checks = append(checks, ConfigCheck{
					Name:    "daily_brief",
					Status:  StatusOK,
					Message: cfg.Orchestrator.DailyBrief.Schedule,
				})
			}
		}
	}

	return checks
}

// checkFeatures checks feature availability
func checkFeatures(cfg *config.Config) []FeatureStatus {
	features := []FeatureStatus{}

	// Determine active backend
	backendType := "claude-code"
	if cfg.Executor != nil && cfg.Executor.Type != "" {
		backendType = cfg.Executor.Type
	}

	// Find the command for the active backend
	backendCmd := "claude" // default
	for _, b := range backends {
		if b.backendType == backendType {
			backendCmd = b.command
			break
		}
	}

	// Core execution - check active backend + git
	hasBackend := commandExists(backendCmd)
	hasGit := commandExists("git")
	if hasBackend && hasGit {
		features = append(features, FeatureStatus{
			Name:    "Task Execution",
			Enabled: true,
			Status:  StatusOK,
		})
	} else {
		missing := []string{}
		if !hasBackend {
			missing = append(missing, backendCmd)
		}
		if !hasGit {
			missing = append(missing, "git")
		}
		features = append(features, FeatureStatus{
			Name:    "Task Execution",
			Enabled: false,
			Status:  StatusError,
			Missing: missing,
		})
	}

	// Telegram
	telegramEnabled := cfg.Adapters != nil &&
		cfg.Adapters.Telegram != nil &&
		cfg.Adapters.Telegram.Enabled &&
		cfg.Adapters.Telegram.BotToken != ""
	telegramNote := ""
	if cfg.Adapters != nil && cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken == "" {
		telegramNote = "missing bot_token"
	}
	features = append(features, FeatureStatus{
		Name:    "Telegram",
		Enabled: telegramEnabled,
		Status:  boolToStatus(telegramEnabled),
		Note:    telegramNote,
	})

	// Image analysis (available via multimodal backends)
	features = append(features, FeatureStatus{
		Name:    "Images",
		Enabled: hasBackend,
		Status:  boolToStatus(hasBackend),
	})

	// Voice transcription (only requires OpenAI API key)
	hasOpenAIKey := cfg.Adapters != nil &&
		cfg.Adapters.Telegram != nil &&
		cfg.Adapters.Telegram.Transcription != nil &&
		cfg.Adapters.Telegram.Transcription.OpenAIAPIKey != ""

	var voiceStatus Status
	var voiceNote string
	var voiceMissing []string
	voiceEnabled := false

	if hasOpenAIKey {
		voiceEnabled = true
		voiceStatus = StatusOK
		voiceNote = "Whisper API"
	} else {
		voiceStatus = StatusWarning
		voiceMissing = append(voiceMissing, "OPENAI_API_KEY")
		voiceNote = "missing: OPENAI_API_KEY"
	}

	features = append(features, FeatureStatus{
		Name:    "Voice",
		Enabled: voiceEnabled,
		Status:  voiceStatus,
		Note:    voiceNote,
		Missing: voiceMissing,
	})

	// Daily briefs
	briefsEnabled := cfg.Orchestrator != nil &&
		cfg.Orchestrator.DailyBrief != nil &&
		cfg.Orchestrator.DailyBrief.Enabled
	briefsNote := ""
	if briefsEnabled && cfg.Orchestrator.DailyBrief.Schedule == "" {
		briefsNote = "no schedule"
	}
	features = append(features, FeatureStatus{
		Name:    "Briefs",
		Enabled: briefsEnabled,
		Status:  boolToStatus(briefsEnabled),
		Note:    briefsNote,
	})

	// Alerts
	alertsEnabled := cfg.Alerts != nil && cfg.Alerts.Enabled
	features = append(features, FeatureStatus{
		Name:    "Alerts",
		Enabled: alertsEnabled,
		Status:  boolToStatus(alertsEnabled),
	})

	// Cross-project memory
	memoryEnabled := cfg.Memory != nil && cfg.Memory.CrossProject
	features = append(features, FeatureStatus{
		Name:    "Memory",
		Enabled: memoryEnabled,
		Status:  boolToStatus(memoryEnabled),
	})

	// PR creation
	hasGH := commandExists("gh")
	prNote := ""
	if !hasGH {
		prNote = "gh CLI not installed"
	}
	features = append(features, FeatureStatus{
		Name:    "PRs",
		Enabled: hasGH,
		Status:  boolToStatus(hasGH),
		Note:    prNote,
	})

	return features
}

// getCommandVersion runs a command and returns its version string
func getCommandVersion(cmd string, args ...string) string {
	out, err := exec.Command(cmd, args...).Output()
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(out))
	// Extract just version number if possible
	if strings.Contains(version, " ") {
		parts := strings.Fields(version)
		for _, p := range parts {
			if strings.Contains(p, ".") {
				return p
			}
		}
	}
	return version
}

// commandExists checks if a command exists in PATH
func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

// boolToStatus converts bool to Status
func boolToStatus(enabled bool) Status {
	if enabled {
		return StatusOK
	}
	return StatusDisabled
}

// Symbol returns the symbol for a status
func (s Status) Symbol() string {
	switch s {
	case StatusOK:
		return "✓"
	case StatusWarning:
		return "○"
	case StatusError:
		return "✗"
	case StatusDisabled:
		return "·"
	default:
		return "?"
	}
}

// ColorSymbol returns the colored symbol for a status
func (s Status) ColorSymbol() string {
	switch s {
	case StatusOK:
		return "\033[32m✓\033[0m" // green
	case StatusWarning:
		return "\033[33m○\033[0m" // yellow
	case StatusError:
		return "\033[31m✗\033[0m" // red
	case StatusDisabled:
		return "\033[90m·\033[0m" // gray
	default:
		return "?"
	}
}

// String returns string representation
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarning:
		return "warning"
	case StatusError:
		return "error"
	case StatusDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// Summary returns a summary of issues
func (r *HealthReport) Summary() (errors int, warnings int) {
	for _, d := range r.Dependencies {
		if d.Status == StatusError {
			errors++
		}
		if d.Status == StatusWarning {
			warnings++
		}
	}
	for _, c := range r.Config {
		if c.Status == StatusError {
			errors++
		}
		if c.Status == StatusWarning {
			warnings++
		}
	}
	return
}

// ReadyToStart returns true if there are no critical errors
func (r *HealthReport) ReadyToStart() bool {
	// Check for critical dependency errors
	for _, d := range r.Dependencies {
		// git is always required
		if d.Name == "git" && d.Status == StatusError {
			return false
		}
		// Any backend marked as active (contains "[active backend]") that's missing is critical
		if d.Status == StatusError && strings.Contains(d.Message, "[active backend]") {
			return false
		}
	}
	return true
}
