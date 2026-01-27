package transcription

import (
	"runtime"
	"strings"
)

// Dependency represents a missing dependency
type Dependency struct {
	Name        string
	Description string
	InstallCmd  string // Platform-specific install command
	Required    bool   // If true, voice won't work without it
}

// SetupStatus represents the voice transcription setup status
type SetupStatus struct {
	OpenAIKeySet       bool
	Platform           string
	Missing            []Dependency
	RecommendedBackend string
}

// CheckSetup checks what's needed for voice transcription
func CheckSetup(config *Config) *SetupStatus {
	status := &SetupStatus{
		Platform: runtime.GOOS,
	}

	// Check OpenAI key (only requirement for voice transcription)
	if config != nil && config.OpenAIAPIKey != "" {
		status.OpenAIKeySet = true
	} else {
		status.Missing = append(status.Missing, Dependency{
			Name:        "OPENAI_API_KEY",
			Description: "Whisper API transcription",
			InstallCmd:  "Set openai_api_key in ~/.pilot/config.yaml",
			Required:    true,
		})
	}

	// Recommend backend
	if status.OpenAIKeySet {
		status.RecommendedBackend = "whisper-api"
	} else {
		status.RecommendedBackend = "none"
	}

	return status
}

// GetInstallInstructions returns human-readable install instructions
func GetInstallInstructions() string {
	var sb strings.Builder
	sb.WriteString("Voice Setup Instructions\n")
	sb.WriteString("========================\n\n")

	sb.WriteString("1. Set OpenAI API key:\n")
	sb.WriteString("   Add to ~/.pilot/config.yaml:\n")
	sb.WriteString("   adapters:\n")
	sb.WriteString("     telegram:\n")
	sb.WriteString("       transcription:\n")
	sb.WriteString("         openai_api_key: \"sk-...\"\n")

	sb.WriteString("\n2. Restart the bot\n")

	return sb.String()
}

// FormatStatusMessage formats status for Telegram display
func FormatStatusMessage(status *SetupStatus) string {
	var sb strings.Builder

	if status.OpenAIKeySet {
		sb.WriteString("✅ Voice transcription is ready!\n")
		sb.WriteString("Backend: Whisper API")
		return sb.String()
	}

	sb.WriteString("Voice Setup Status\n")
	sb.WriteString("------------------\n")

	sb.WriteString("✗ OpenAI API key: not set\n")

	// Show what's needed
	sb.WriteString("\nTo enable voice:\n")
	sb.WriteString("• Set openai_api_key in ~/.pilot/config.yaml\n")

	return sb.String()
}
