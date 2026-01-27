// Package health provides system health checks for Pilot.
//
// It verifies required dependencies (Claude Code CLI, git, ffmpeg) are installed
// and checks feature availability based on configuration. The RunChecks function
// generates a HealthReport used by the CLI status command to display system
// readiness and configuration state.
package health

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alekspetrov/pilot/internal/config"
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
	Projects     int
	HasErrors    bool
	HasWarnings  bool
}

// RunChecks performs all health checks based on config
func RunChecks(cfg *config.Config) *HealthReport {
	report := &HealthReport{
		Dependencies: checkDependencies(),
		Config:       checkConfig(cfg),
		Features:     checkFeatures(cfg),
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

// checkDependencies checks required system dependencies
func checkDependencies() []Check {
	checks := []Check{}

	// Check Claude Code
	if version := getCommandVersion("claude", "--version"); version != "" {
		checks = append(checks, Check{
			Name:    "claude",
			Status:  StatusOK,
			Message: version,
		})
	} else {
		checks = append(checks, Check{
			Name:    "claude",
			Status:  StatusError,
			Message: "not found",
			Fix:     "npm install -g @anthropic-ai/claude-code",
		})
	}

	// Check Git
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

	// Check ffmpeg (optional, for voice)
	if version := getCommandVersion("ffmpeg", "-version"); version != "" {
		checks = append(checks, Check{
			Name:    "ffmpeg",
			Status:  StatusOK,
			Message: "installed",
		})
	} else {
		checks = append(checks, Check{
			Name:    "ffmpeg",
			Status:  StatusWarning,
			Message: "not found (voice transcription unavailable)",
			Fix:     "brew install ffmpeg",
		})
	}

	// Check Python (optional, for SenseVoice)
	if version := getCommandVersion("python3", "--version"); version != "" {
		// Check if funasr is installed
		hasFunasr := checkPythonModule("funasr")
		if hasFunasr {
			checks = append(checks, Check{
				Name:    "python3",
				Status:  StatusOK,
				Message: version + " + funasr",
			})
		} else {
			checks = append(checks, Check{
				Name:    "python3",
				Status:  StatusWarning,
				Message: version + " (funasr not installed)",
				Fix:     pythonInstallHint("funasr torch torchaudio"),
			})
		}
	} else {
		checks = append(checks, Check{
			Name:    "python3",
			Status:  StatusWarning,
			Message: "not found (SenseVoice unavailable)",
			Fix:     "brew install python@3",
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

	return checks
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
						Message: "configured (voice fallback available)",
					})
				} else if !commandExists("ffmpeg") {
					checks = append(checks, ConfigCheck{
						Name:    "transcription.openai_api_key",
						Status:  StatusWarning,
						Message: "missing (no voice fallback)",
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

	// Core execution
	hasClaude := commandExists("claude")
	hasGit := commandExists("git")
	if hasClaude && hasGit {
		features = append(features, FeatureStatus{
			Name:    "Task Execution",
			Enabled: true,
			Status:  StatusOK,
		})
	} else {
		missing := []string{}
		if !hasClaude {
			missing = append(missing, "claude")
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

	// Image analysis (always available via Claude)
	features = append(features, FeatureStatus{
		Name:    "Images",
		Enabled: hasClaude,
		Status:  boolToStatus(hasClaude),
	})

	// Voice transcription
	hasFFmpeg := commandExists("ffmpeg")
	hasFunasr := checkPythonModule("funasr")
	hasOpenAIKey := cfg.Adapters != nil &&
		cfg.Adapters.Telegram != nil &&
		cfg.Adapters.Telegram.Transcription != nil &&
		cfg.Adapters.Telegram.Transcription.OpenAIAPIKey != ""

	var voiceStatus Status
	var voiceNote string
	var voiceMissing []string
	voiceEnabled := false
	voiceDegraded := false

	if hasFFmpeg {
		voiceEnabled = true
		if hasFunasr {
			voiceStatus = StatusOK
			voiceNote = "SenseVoice"
		} else if hasOpenAIKey {
			voiceStatus = StatusOK
			voiceNote = "Whisper API"
		} else {
			voiceStatus = StatusWarning
			voiceNote = "no backend configured"
			voiceDegraded = true
			voiceMissing = append(voiceMissing, "funasr or OPENAI_API_KEY")
		}
	} else {
		voiceStatus = StatusWarning
		voiceNote = "no ffmpeg"
		voiceMissing = append(voiceMissing, "ffmpeg")
	}

	features = append(features, FeatureStatus{
		Name:     "Voice",
		Enabled:  voiceEnabled,
		Status:   voiceStatus,
		Note:     voiceNote,
		Missing:  voiceMissing,
		Degraded: voiceDegraded,
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

// checkPythonModule checks if a Python module is installed
func checkPythonModule(module string) bool {
	pythonPath := getPythonPath()
	cmd := exec.Command(pythonPath, "-c", fmt.Sprintf("import %s", module))
	return cmd.Run() == nil
}

// getPythonPath returns the path to Python, preferring ~/.pilot/venv if it exists
func getPythonPath() string {
	if home, err := os.UserHomeDir(); err == nil {
		venvPython := filepath.Join(home, ".pilot", "venv", "bin", "python3")
		if _, err := os.Stat(venvPython); err == nil {
			return venvPython
		}
	}
	return "python3"
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
		if d.Name == "claude" && d.Status == StatusError {
			return false
		}
		if d.Name == "git" && d.Status == StatusError {
			return false
		}
	}
	return true
}

// pythonInstallHint returns the best pip/uv install command
func pythonInstallHint(packages string) string {
	if commandExists("uv") {
		return "uv pip install " + packages
	}
	if commandExists("pip3") {
		return "pip3 install " + packages
	}
	return "pip install " + packages
}

// commandExists checks if a command is available
