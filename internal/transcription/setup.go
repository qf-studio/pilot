package transcription

import (
	"context"
	"fmt"
	"os/exec"
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
	FFmpegInstalled    bool
	FunASRInstalled    bool
	OpenAIKeySet       bool
	Platform           string
	Missing            []Dependency
	CanAutoInstall     bool
	RecommendedBackend string
}

// CheckSetup checks what's needed for voice transcription
func CheckSetup(config *Config) *SetupStatus {
	status := &SetupStatus{
		Platform: runtime.GOOS,
	}

	// Check ffmpeg
	status.FFmpegInstalled = commandExists("ffmpeg")
	if !status.FFmpegInstalled {
		status.Missing = append(status.Missing, Dependency{
			Name:        "ffmpeg",
			Description: "Audio conversion (required)",
			InstallCmd:  ffmpegInstallCmd(),
			Required:    true,
		})
	}

	// Check funasr (SenseVoice)
	status.FunASRInstalled = checkPythonModule("funasr")
	if !status.FunASRInstalled {
		status.Missing = append(status.Missing, Dependency{
			Name:        "funasr",
			Description: "Local transcription (free, private)",
			InstallCmd:  "pip3 install funasr torch torchaudio",
			Required:    false,
		})
	}

	// Check OpenAI key
	if config != nil && config.OpenAIAPIKey != "" {
		status.OpenAIKeySet = true
	}

	// Determine if we can auto-install
	status.CanAutoInstall = canAutoInstall()

	// Recommend backend
	if status.FunASRInstalled {
		status.RecommendedBackend = "sensevoice"
	} else if status.OpenAIKeySet {
		status.RecommendedBackend = "whisper-api"
	} else {
		status.RecommendedBackend = "none"
	}

	return status
}

// InstallFFmpeg attempts to install ffmpeg
func InstallFFmpeg(ctx context.Context) error {
	cmd := ffmpegInstallCmd()
	if cmd == "" {
		return fmt.Errorf("no auto-install available for %s", runtime.GOOS)
	}

	return runInstallCmd(ctx, cmd)
}

// InstallFunASR attempts to install funasr and dependencies
func InstallFunASR(ctx context.Context) error {
	// pip works cross-platform
	return runInstallCmd(ctx, "pip3 install funasr torch torchaudio")
}

// ffmpegInstallCmd returns the platform-specific ffmpeg install command
func ffmpegInstallCmd() string {
	switch runtime.GOOS {
	case "darwin":
		// macOS - prefer Homebrew
		if commandExists("brew") {
			return "brew install ffmpeg"
		}
		return ""
	case "linux":
		// Linux - try common package managers
		if commandExists("apt-get") {
			return "sudo apt-get install -y ffmpeg"
		}
		if commandExists("apt") {
			return "sudo apt install -y ffmpeg"
		}
		if commandExists("dnf") {
			return "sudo dnf install -y ffmpeg"
		}
		if commandExists("yum") {
			return "sudo yum install -y ffmpeg"
		}
		if commandExists("pacman") {
			return "sudo pacman -S --noconfirm ffmpeg"
		}
		return ""
	case "windows":
		// Windows - prefer winget, fallback to choco
		if commandExists("winget") {
			return "winget install -e --id Gyan.FFmpeg"
		}
		if commandExists("choco") {
			return "choco install ffmpeg -y"
		}
		return ""
	default:
		return ""
	}
}

// canAutoInstall checks if we have a package manager available
func canAutoInstall() bool {
	switch runtime.GOOS {
	case "darwin":
		return commandExists("brew")
	case "linux":
		return commandExists("apt-get") || commandExists("apt") ||
			commandExists("dnf") || commandExists("yum") || commandExists("pacman")
	case "windows":
		return commandExists("winget") || commandExists("choco")
	default:
		return false
	}
}

// runInstallCmd executes an install command
func runInstallCmd(ctx context.Context, cmd string) error {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}

	// Handle sudo specially on Unix
	if parts[0] == "sudo" && len(parts) > 1 {
		// Run with sudo
		c := exec.CommandContext(ctx, parts[0], parts[1:]...)
		output, err := c.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s failed: %w\nOutput: %s", cmd, err, string(output))
		}
		return nil
	}

	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	output, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w\nOutput: %s", cmd, err, string(output))
	}
	return nil
}

// commandExists checks if a command is available in PATH
func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// checkPythonModule checks if a Python module is installed
func checkPythonModule(module string) bool {
	cmd := exec.Command("python3", "-c", fmt.Sprintf("import %s", module))
	return cmd.Run() == nil
}

// GetInstallInstructions returns human-readable install instructions
func GetInstallInstructions() string {
	var sb strings.Builder
	sb.WriteString("Voice Setup Instructions\n")
	sb.WriteString("========================\n\n")

	sb.WriteString("1. Install ffmpeg (required):\n")
	switch runtime.GOOS {
	case "darwin":
		sb.WriteString("   brew install ffmpeg\n")
	case "linux":
		sb.WriteString("   Ubuntu/Debian: sudo apt install ffmpeg\n")
		sb.WriteString("   Fedora: sudo dnf install ffmpeg\n")
		sb.WriteString("   Arch: sudo pacman -S ffmpeg\n")
	case "windows":
		sb.WriteString("   winget install -e --id Gyan.FFmpeg\n")
		sb.WriteString("   or: choco install ffmpeg\n")
	}

	sb.WriteString("\n2. Choose transcription backend:\n\n")
	sb.WriteString("   Option A - Local (free, private):\n")
	sb.WriteString("   pip3 install funasr torch torchaudio\n")
	sb.WriteString("   (downloads ~2GB model on first use)\n\n")
	sb.WriteString("   Option B - Cloud (faster, paid):\n")
	sb.WriteString("   Set OPENAI_API_KEY in your config\n")

	sb.WriteString("\n3. Restart the bot\n")

	return sb.String()
}

// FormatStatusMessage formats status for Telegram display
func FormatStatusMessage(status *SetupStatus) string {
	var sb strings.Builder

	if len(status.Missing) == 0 && (status.FunASRInstalled || status.OpenAIKeySet) {
		sb.WriteString("Voice transcription is ready!\n")
		sb.WriteString("Backend: " + status.RecommendedBackend)
		return sb.String()
	}

	sb.WriteString("Voice Setup Status\n")
	sb.WriteString("------------------\n")

	// Show what's installed
	if status.FFmpegInstalled {
		sb.WriteString("ffmpeg: installed\n")
	} else {
		sb.WriteString("ffmpeg: missing (required)\n")
	}

	if status.FunASRInstalled {
		sb.WriteString("SenseVoice: installed\n")
	} else if status.OpenAIKeySet {
		sb.WriteString("Whisper API: configured\n")
	} else {
		sb.WriteString("Backend: none configured\n")
	}

	// Show what's needed
	if len(status.Missing) > 0 {
		sb.WriteString("\nTo enable voice:\n")
		for _, dep := range status.Missing {
			if dep.InstallCmd != "" {
				sb.WriteString(fmt.Sprintf("• %s\n", dep.InstallCmd))
			}
		}
	}

	return sb.String()
}
