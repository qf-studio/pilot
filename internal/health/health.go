package health

import (
	"os/exec"
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

// FeatureStatus represents a feature with its availability
type FeatureStatus struct {
	Name    string
	Enabled bool
	Status  Status
	Note    string
}

// HealthReport contains all health check results
type HealthReport struct {
	Dependencies []Check
	Features     []FeatureStatus
	Projects     int
}

// RunChecks performs all health checks based on config
func RunChecks(cfg *config.Config) *HealthReport {
	report := &HealthReport{
		Dependencies: checkDependencies(),
		Features:     checkFeatures(cfg),
		Projects:     len(cfg.Projects),
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
	if commandExists("ffmpeg") {
		checks = append(checks, Check{
			Name:    "ffmpeg",
			Status:  StatusOK,
			Message: "installed",
		})
	} else {
		checks = append(checks, Check{
			Name:    "ffmpeg",
			Status:  StatusWarning,
			Message: "not found (voice disabled)",
			Fix:     "brew install ffmpeg",
		})
	}

	return checks
}

// checkFeatures checks feature availability
func checkFeatures(cfg *config.Config) []FeatureStatus {
	features := []FeatureStatus{}

	// Telegram
	telegramEnabled := cfg.Adapters != nil &&
		cfg.Adapters.Telegram != nil &&
		cfg.Adapters.Telegram.Enabled
	features = append(features, FeatureStatus{
		Name:    "Telegram",
		Enabled: telegramEnabled,
		Status:  boolToStatus(telegramEnabled),
	})

	// Daily briefs
	briefsEnabled := cfg.Orchestrator != nil &&
		cfg.Orchestrator.DailyBrief != nil &&
		cfg.Orchestrator.DailyBrief.Enabled
	features = append(features, FeatureStatus{
		Name:    "Briefs",
		Enabled: briefsEnabled,
		Status:  boolToStatus(briefsEnabled),
	})

	// Alerts
	alertsEnabled := cfg.Alerts != nil && cfg.Alerts.Enabled
	features = append(features, FeatureStatus{
		Name:    "Alerts",
		Enabled: alertsEnabled,
		Status:  boolToStatus(alertsEnabled),
	})

	// Voice transcription
	hasFFmpeg := commandExists("ffmpeg")
	voiceStatus := StatusDisabled
	voiceNote := ""
	if hasFFmpeg {
		voiceStatus = StatusOK
	} else {
		voiceStatus = StatusWarning
		voiceNote = "no ffmpeg"
	}
	features = append(features, FeatureStatus{
		Name:    "Voice",
		Enabled: hasFFmpeg,
		Status:  voiceStatus,
		Note:    voiceNote,
	})

	// Cross-project memory
	memoryEnabled := cfg.Memory != nil && cfg.Memory.CrossProject
	features = append(features, FeatureStatus{
		Name:    "Memory",
		Enabled: memoryEnabled,
		Status:  boolToStatus(memoryEnabled),
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

// boolToStatus converts bool to Status
func boolToStatus(enabled bool) Status {
	if enabled {
		return StatusOK
	}
	return StatusDisabled
}

// StatusSymbol returns the symbol for a status
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
