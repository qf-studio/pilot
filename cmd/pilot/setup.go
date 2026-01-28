package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/transcription"
	"github.com/spf13/cobra"
)

var noSleep bool

func newSetupCmd() *cobra.Command {
	var skipOptional bool
	var setupTunnel bool

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
  - Cloudflare Tunnel (with --tunnel flag)

Examples:
  pilot setup              # Full interactive setup
  pilot setup --skip-optional  # Skip optional features
  pilot setup --tunnel     # Set up Cloudflare Tunnel for webhooks`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// If --tunnel flag, redirect to tunnel setup
			if setupTunnel {
				tunnelCmd := newTunnelSetupCmd()
				return tunnelCmd.RunE(tunnelCmd, args)
			}

			// If --no-sleep flag, disable Mac sleep and exit
			if noSleep {
				return disableMacSleep()
			}

			reader := bufio.NewReader(os.Stdin)

			// Load existing config or create new
			cfg, _ := loadConfig()
			if cfg == nil {
				cfg = config.DefaultConfig()
			}

			fmt.Println()
			fmt.Println("Pilot Setup")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			// Check what's already configured
			hasTelegram := cfg.Adapters != nil && cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.BotToken != ""
			hasProjects := len(cfg.Projects) > 0
			hasVoice := cfg.Adapters != nil && cfg.Adapters.Telegram != nil &&
				cfg.Adapters.Telegram.Transcription != nil &&
				cfg.Adapters.Telegram.Transcription.OpenAIAPIKey != ""
			hasBriefs := cfg.Orchestrator != nil && cfg.Orchestrator.DailyBrief != nil && cfg.Orchestrator.DailyBrief.Enabled
			hasAlerts := cfg.Alerts != nil && cfg.Alerts.Enabled

			// Show current status
			fmt.Println()
			fmt.Println("Current Status:")
			printStatus("Telegram", hasTelegram)
			printStatus("Projects", hasProjects)
			if !skipOptional {
				printStatus("Voice", hasVoice)
				printStatus("Daily Briefs", hasBriefs)
				printStatus("Alerts", hasAlerts)
			}
			fmt.Println()

			// Check if everything is configured
			allConfigured := hasTelegram && hasProjects
			if !skipOptional {
				allConfigured = allConfigured && hasVoice && hasBriefs && hasAlerts
			}

			if allConfigured {
				fmt.Println("✅ Everything is configured!")
				fmt.Println()
				fmt.Print("Reconfigure anyway? [y/N]: ")
				if !readYesNo(reader, false) {
					fmt.Println()
					fmt.Println("Run 'pilot doctor' to verify configuration")
					return nil
				}
				fmt.Println()
			}

			// Only setup unconfigured items (or all if user chose to reconfigure)
			needsSetup := !allConfigured

			// Telegram Bot
			if needsSetup && !hasTelegram {
				fmt.Println("Telegram Bot")
				fmt.Println("─────────────────────────")
				if err := setupTelegram(reader, cfg); err != nil {
					return err
				}
				fmt.Println()
			}

			// Projects
			if needsSetup && !hasProjects {
				fmt.Println("Projects")
				fmt.Println("─────────────────────────")
				if err := setupProjects(reader, cfg); err != nil {
					return err
				}
				fmt.Println()
			}

			if !skipOptional {
				// Voice Transcription
				if needsSetup && !hasVoice {
					fmt.Println("Voice Transcription")
					fmt.Println("─────────────────────────")
					if err := setupVoice(reader, cfg); err != nil {
						return err
					}
					fmt.Println()
				}

				// Daily Briefs
				if needsSetup && !hasBriefs {
					fmt.Println("Daily Briefs")
					fmt.Println("─────────────────────────")
					if err := setupBriefs(reader, cfg); err != nil {
						return err
					}
					fmt.Println()
				}

				// Alerts
				if needsSetup && !hasAlerts {
					fmt.Println("Alerts")
					fmt.Println("─────────────────────────")
					if err := setupAlerts(reader, cfg); err != nil {
						return err
					}
					fmt.Println()
				}
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
	cmd.Flags().BoolVar(&setupTunnel, "tunnel", false, "Set up Cloudflare Tunnel for webhooks (runs pilot tunnel setup)")

	cmd.Flags().BoolVar(&noSleep, "no-sleep", false, "Disable Mac sleep for always-on operation (macOS only, requires sudo)")

	return cmd
}

// disableMacSleep disables system sleep on macOS for always-on operation
func disableMacSleep() error {
	if runtime.GOOS != "darwin" {
		fmt.Println("⚠️  --no-sleep only works on macOS")
		return nil
	}

	fmt.Println("🔋 Disabling Mac sleep...")
	fmt.Println()
	fmt.Println("This requires administrator privileges.")
	fmt.Println("You may be prompted for your password.")
	fmt.Println()

	cmd := exec.Command("sudo", "pmset", "-a", "sleep", "0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to disable sleep: %w", err)
	}

	fmt.Println()
	fmt.Println("✅ Mac sleep disabled")
	fmt.Println()
	fmt.Println("Your Mac will no longer sleep automatically.")
	fmt.Println("To re-enable sleep later:")
	fmt.Println("  sudo pmset -a sleep 1")
	fmt.Println()

	return nil
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

	// Ask for chat ID (required for bot to reply)
	fmt.Println("  Message @userinfobot on Telegram to get your Chat ID")
	fmt.Print("  Paste Chat ID: ")
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
	// Initialize transcription config
	if cfg.Adapters.Telegram == nil {
		cfg.Adapters.Telegram = telegram.DefaultConfig()
	}
	if cfg.Adapters.Telegram.Transcription == nil {
		cfg.Adapters.Telegram.Transcription = &transcription.Config{
			Backend: "whisper-api",
		}
	}

	// Check for existing OpenAI key
	apiKey := cfg.Adapters.Telegram.Transcription.OpenAIAPIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	if apiKey != "" {
		fmt.Println("  ✓ OpenAI API key found")
		cfg.Adapters.Telegram.Transcription.OpenAIAPIKey = apiKey
		cfg.Adapters.Telegram.Transcription.Backend = "whisper-api"
	} else {
		fmt.Println("  Voice transcription requires OpenAI API key (Whisper)")
		fmt.Print("  Enter OpenAI API key (or leave empty to skip): ")
		apiKey = readLine(reader)

		if apiKey != "" {
			cfg.Adapters.Telegram.Transcription.OpenAIAPIKey = apiKey
			cfg.Adapters.Telegram.Transcription.Backend = "whisper-api"
			fmt.Println("  ✓ Whisper API configured")
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

func printStatus(name string, configured bool) {
	if configured {
		fmt.Printf("  ✓ %s\n", name)
	} else {
		fmt.Printf("  ○ %s (not configured)\n", name)
	}
}

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

func validateTelegramToken(token string) error {
	// Simple validation - check format
	if !strings.Contains(token, ":") {
		return fmt.Errorf("invalid token format")
	}
	// Could add actual API call validation here
	return nil
}

