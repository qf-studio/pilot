package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/transcription"
	"github.com/spf13/cobra"
)

func newSetupCmd() *cobra.Command {
	var skipOptional bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Interactive setup wizard",
		Long: `Interactive wizard to configure Pilot step by step.

Sets up:
  - Telegram bot connection
  - Project paths
  - Voice transcription
  - Daily briefs
  - Alerts

Examples:
  pilot setup              # Full interactive setup
  pilot setup --skip-optional  # Skip optional features`,
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(os.Stdin)

			// Load existing config or create new
			cfg, _ := loadConfig()
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			fmt.Println()
			fmt.Println("Welcome to Pilot Setup!")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			totalSteps := 5
			if skipOptional {
				totalSteps = 2
			}

			// Step 1: Telegram Bot
			fmt.Printf("Step 1/%d: Telegram Bot\n", totalSteps)
			fmt.Println("─────────────────────────")
			if err := setupTelegram(reader, cfg); err != nil {
				return err
			}
			fmt.Println()

			// Step 2: Projects
			fmt.Printf("Step 2/%d: Projects\n", totalSteps)
			fmt.Println("─────────────────────────")
			if err := setupProjects(reader, cfg); err != nil {
				return err
			}
			fmt.Println()

			if !skipOptional {
				// Step 3: Voice Transcription
				fmt.Printf("Step 3/%d: Voice Transcription\n", totalSteps)
				fmt.Println("─────────────────────────")
				if err := setupVoice(reader, cfg); err != nil {
					return err
				}
				fmt.Println()

				// Step 4: Daily Briefs
				fmt.Printf("Step 4/%d: Daily Briefs\n", totalSteps)
				fmt.Println("─────────────────────────")
				if err := setupBriefs(reader, cfg); err != nil {
					return err
				}
				fmt.Println()

				// Step 5: Alerts
				fmt.Printf("Step 5/%d: Alerts\n", totalSteps)
				fmt.Println("─────────────────────────")
				if err := setupAlerts(reader, cfg); err != nil {
					return err
				}
				fmt.Println()
			}

			// Save config
			configPath := config.DefaultConfigPath()
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println("Setup complete!")
			fmt.Println()
			fmt.Printf("Config saved to: %s\n", configPath)
			fmt.Println()
			fmt.Println("Next steps:")
			if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
				fmt.Println("  pilot telegram    # Start Telegram bot")
			}
			fmt.Println("  pilot doctor      # Verify configuration")
			fmt.Println("  pilot task \"...\"  # Execute a task")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().BoolVar(&skipOptional, "skip-optional", false, "Skip optional feature setup")

	return cmd
}

func setupTelegram(reader *bufio.Reader, cfg *config.Config) error {
	fmt.Print("  Set up Telegram bot? [Y/n]: ")
	if !readYesNo(reader, true) {
		return nil
	}

	// Initialize telegram config if needed
	if cfg.Adapters == nil {
		cfg.Adapters = &config.AdaptersConfig{}
	}
	if cfg.Adapters.Telegram == nil {
		cfg.Adapters.Telegram = telegram.DefaultConfig()
	}

	// Check for existing token
	if cfg.Adapters.Telegram.BotToken != "" {
		fmt.Printf("  Existing token found. Replace? [y/N]: ")
		if !readYesNo(reader, false) {
			cfg.Adapters.Telegram.Enabled = true
			fmt.Println("  ✓ Keeping existing token")
			return nil
		}
	}

	fmt.Print("  Enter bot token (from @BotFather): ")
	token := readLine(reader)
	if token == "" {
		fmt.Println("  ○ Skipped - no token provided")
		return nil
	}

	cfg.Adapters.Telegram.BotToken = token
	cfg.Adapters.Telegram.Enabled = true
	cfg.Adapters.Telegram.Polling = true

	// Validate token by getting bot info
	fmt.Print("  Validating... ")
	if err := validateTelegramToken(token); err != nil {
		fmt.Println("✗")
		fmt.Printf("  ⚠️  Token validation failed: %v\n", err)
		fmt.Print("  Continue anyway? [y/N]: ")
		if !readYesNo(reader, false) {
			cfg.Adapters.Telegram.BotToken = ""
			cfg.Adapters.Telegram.Enabled = false
			return nil
		}
	} else {
		fmt.Println("✓")
	}

	// Ask for chat ID
	fmt.Print("  Enter your Telegram chat ID (optional, for security): ")
	chatID := readLine(reader)
	if chatID != "" {
		cfg.Adapters.Telegram.ChatID = chatID
	}

	fmt.Println("  ✓ Telegram configured")
	return nil
}

func setupProjects(reader *bufio.Reader, cfg *config.Config) error {
	// Show existing projects
	if len(cfg.Projects) > 0 {
		fmt.Println("  Existing projects:")
		for _, p := range cfg.Projects {
			fmt.Printf("    • %s: %s\n", p.Name, p.Path)
		}
		fmt.Print("  Add more projects? [y/N]: ")
		if !readYesNo(reader, false) {
			return nil
		}
	}

	for {
		fmt.Print("  Project path (or Enter to finish): ")
		path := readLine(reader)
		if path == "" {
			break
		}

		// Expand ~ and validate
		path = expandPath(path)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  ⚠️  Path not found: %s\n", path)
			fmt.Print("  Add anyway? [y/N]: ")
			if !readYesNo(reader, false) {
				continue
			}
		}

		// Get project name
		defaultName := filepath.Base(path)
		fmt.Printf("  Project name [%s]: ", defaultName)
		name := readLine(reader)
		if name == "" {
			name = defaultName
		}

		// Check for Navigator
		hasNavigator := false
		agentPath := filepath.Join(path, ".agent")
		if _, err := os.Stat(agentPath); err == nil {
			hasNavigator = true
			fmt.Println("  ✓ Navigator detected")
		}

		// Add project
		cfg.Projects = append(cfg.Projects, &config.ProjectConfig{
			Name:      name,
			Path:      path,
			Navigator: hasNavigator,
		})

		fmt.Printf("  ✓ Added: %s\n", name)
	}

	if len(cfg.Projects) == 0 {
		fmt.Println("  ○ No projects configured")
	} else {
		fmt.Printf("  ✓ %d project(s) configured\n", len(cfg.Projects))
	}

	return nil
}

func setupVoice(reader *bufio.Reader, cfg *config.Config) error {
	// Check ffmpeg
	hasFFmpeg := commandExists("ffmpeg")
	if !hasFFmpeg {
		fmt.Println("  ffmpeg not found (required for voice)")
		fmt.Print("  Install ffmpeg now? [Y/n]: ")
		if readYesNo(reader, true) {
			fmt.Print("  Installing... ")
			if err := installFFmpeg(); err != nil {
				fmt.Println("✗")
				fmt.Printf("  ⚠️  Install failed: %v\n", err)
				fmt.Println("  → Install manually: brew install ffmpeg")
			} else {
				fmt.Println("✓")
				hasFFmpeg = true
			}
		}
	} else {
		fmt.Println("  ✓ ffmpeg found")
	}

	if !hasFFmpeg {
		fmt.Println("  ○ Voice transcription not available")
		return nil
	}

	// Initialize transcription config
	if cfg.Adapters.Telegram == nil {
		cfg.Adapters.Telegram = telegram.DefaultConfig()
	}
	if cfg.Adapters.Telegram.Transcription == nil {
		cfg.Adapters.Telegram.Transcription = &transcription.Config{
			Backend: "auto",
		}
	}

	// Check SenseVoice
	hasFunasr := checkPythonModule("funasr")
	if hasFunasr {
		fmt.Println("  ✓ SenseVoice (funasr) found")
		cfg.Adapters.Telegram.Transcription.Backend = "sensevoice"
	} else {
		fmt.Println("  SenseVoice not installed (local, fast, free)")
		fmt.Print("  Install now? (requires ~2GB) [y/N]: ")
		if readYesNo(reader, false) {
			fmt.Println("  Installing SenseVoice dependencies...")
			if err := installSenseVoice(); err != nil {
				fmt.Printf("  ⚠️  Install failed: %v\n", err)
			} else {
				fmt.Println("  ✓ SenseVoice installed")
				cfg.Adapters.Telegram.Transcription.Backend = "sensevoice"
				hasFunasr = true
			}
		}
	}

	// Whisper API fallback
	if !hasFunasr {
		fmt.Print("  Set up Whisper API (OpenAI) as fallback? [Y/n]: ")
		if readYesNo(reader, true) {
			// Check environment first
			apiKey := os.Getenv("OPENAI_API_KEY")
			if apiKey != "" {
				fmt.Println("  ✓ Using OPENAI_API_KEY from environment")
			} else {
				fmt.Print("  Enter OpenAI API key: ")
				apiKey = readLine(reader)
			}

			if apiKey != "" {
				cfg.Adapters.Telegram.Transcription.OpenAIAPIKey = apiKey
				cfg.Adapters.Telegram.Transcription.Backend = "whisper-api"
				fmt.Println("  ✓ Whisper API configured")
			} else {
				fmt.Println("  ○ No API key provided")
			}
		}
	}

	// Summary
	switch cfg.Adapters.Telegram.Transcription.Backend {
	case "sensevoice":
		fmt.Println("  ✓ Voice transcription: SenseVoice (local)")
	case "whisper-api":
		fmt.Println("  ✓ Voice transcription: Whisper API")
	case "auto":
		if cfg.Adapters.Telegram.Transcription.OpenAIAPIKey != "" {
			fmt.Println("  ✓ Voice transcription: auto (Whisper fallback)")
		} else {
			fmt.Println("  ○ Voice transcription not configured")
		}
	}

	return nil
}

func setupBriefs(reader *bufio.Reader, cfg *config.Config) error {
	fmt.Print("  Enable daily briefs? [y/N]: ")
	if !readYesNo(reader, false) {
		return nil
	}

	// Initialize config
	if cfg.Orchestrator == nil {
		cfg.Orchestrator = &config.OrchestratorConfig{
			Model:         "claude-sonnet-4-20250514",
			MaxConcurrent: 2,
		}
	}
	if cfg.Orchestrator.DailyBrief == nil {
		cfg.Orchestrator.DailyBrief = &config.DailyBriefConfig{
			Channels: []config.BriefChannelConfig{},
			Content: config.BriefContentConfig{
				IncludeMetrics:     true,
				IncludeErrors:      true,
				MaxItemsPerSection: 10,
			},
			Filters: config.BriefFilterConfig{
				Projects: []string{},
			},
		}
	}

	cfg.Orchestrator.DailyBrief.Enabled = true

	// Schedule
	fmt.Print("  What time? (24h format) [9:00]: ")
	timeStr := readLine(reader)
	if timeStr == "" {
		timeStr = "9:00"
	}

	// Parse time into cron
	hour, minute := "9", "0"
	if _, err := fmt.Sscanf(timeStr, "%[0-9]:%[0-9]", &hour, &minute); err == nil {
		cfg.Orchestrator.DailyBrief.Schedule = fmt.Sprintf("%s %s * * 1-5", minute, hour)
	} else {
		cfg.Orchestrator.DailyBrief.Schedule = "0 9 * * 1-5" // default
	}

	// Timezone
	fmt.Print("  Timezone [Europe/Berlin]: ")
	tz := readLine(reader)
	if tz == "" {
		tz = "Europe/Berlin"
	}
	cfg.Orchestrator.DailyBrief.Timezone = tz

	fmt.Printf("  ✓ Daily briefs at %s (%s)\n", timeStr, tz)

	return nil
}

func setupAlerts(reader *bufio.Reader, cfg *config.Config) error {
	fmt.Print("  Enable failure alerts? [Y/n]: ")
	if !readYesNo(reader, true) {
		return nil
	}

	// Initialize config
	if cfg.Alerts == nil {
		cfg.Alerts = &config.AlertsConfig{
			Enabled: true,
		}
	}
	cfg.Alerts.Enabled = true

	// Default to Telegram if configured
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
		fmt.Println("  ✓ Alerts will be sent to Telegram")
	}

	return nil
}

// Helper functions

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func readYesNo(reader *bufio.Reader, defaultYes bool) bool {
	line := readLine(reader)
	if line == "" {
		return defaultYes
	}
	line = strings.ToLower(line)
	return line == "y" || line == "yes"
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[1:])
	}
	return path
}

func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func checkPythonModule(module string) bool {
	cmd := exec.Command("python3", "-c", fmt.Sprintf("import %s", module))
	return cmd.Run() == nil
}

func validateTelegramToken(token string) error {
	// Simple validation - check format
	if !strings.Contains(token, ":") {
		return fmt.Errorf("invalid token format")
	}
	// Could add actual API call validation here
	return nil
}

func installFFmpeg() error {
	// Try homebrew on macOS
	cmd := exec.Command("brew", "install", "ffmpeg")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func installSenseVoice() error {
	cmd := exec.Command("pip3", "install", "funasr", "torch", "torchaudio")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
