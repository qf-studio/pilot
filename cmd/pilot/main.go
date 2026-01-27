package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/banner"
	"github.com/alekspetrov/pilot/internal/briefs"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/pilot"
)

var (
	version   = "0.2.0"
	buildTime = "unknown"
	cfgFile   string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "pilot",
		Short: "AI that ships your tickets",
		Long:  `Pilot is an autonomous AI development pipeline that receives tickets, implements features, and creates PRs.`,
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ~/.pilot/config.yaml)")

	rootCmd.AddCommand(
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newInitCmd(),
		newVersionCmd(),
		newTaskCmd(),
		newTelegramCmd(),
		newBriefCmd(),
		newPatternsCmd(),
		newMetricsCmd(),
		newUsageCmd(),
		newTeamCmd(),
		newBudgetCmd(),
		newDoctorCmd(),
		newSetupCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newStartCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Pilot daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			// Create and start Pilot
			p, err := pilot.New(cfg)
			if err != nil {
				return fmt.Errorf("failed to create Pilot: %w", err)
			}

			if err := p.Start(); err != nil {
				return fmt.Errorf("failed to start Pilot: %w", err)
			}

			// Show startup banner
			gateway := fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
			banner.StartupBanner(version, gateway)

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			<-sigCh
			fmt.Println("\n🛑 Shutting down...")

			return p.Stop()
		},
	}

	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run in background (daemon mode)")

	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Pilot daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Send shutdown signal to running daemon
			fmt.Println("🛑 Stopping Pilot daemon...")
			fmt.Println("   (Not implemented - use Ctrl+C to stop)")
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Pilot status and running tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config to get gateway address
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			fmt.Println("📊 Pilot Status")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("Gateway: http://%s:%d\n", cfg.Gateway.Host, cfg.Gateway.Port)
			fmt.Println()

			// Check adapters
			fmt.Println("Adapters:")
			if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
				fmt.Println("  ✓ Linear (enabled)")
			} else {
				fmt.Println("  ○ Linear (disabled)")
			}
			if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
				fmt.Println("  ✓ Slack (enabled)")
			} else {
				fmt.Println("  ○ Slack (disabled)")
			}
			fmt.Println()

			// List projects
			fmt.Println("Projects:")
			if len(cfg.Projects) == 0 {
				fmt.Println("  (none configured)")
			} else {
				for _, proj := range cfg.Projects {
					nav := ""
					if proj.Navigator {
						nav = " [Navigator]"
					}
					fmt.Printf("  • %s: %s%s\n", proj.Name, proj.Path, nav)
				}
			}

			return nil
		},
	}
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize Pilot configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := config.DefaultConfigPath()

			// Check if config already exists
			if _, err := os.Stat(configPath); err == nil {
				fmt.Printf("Config already exists at %s\n", configPath)
				fmt.Println("Edit it manually or delete to reinitialize.")
				return nil
			}

			// Create default config
			cfg := config.DefaultConfig()

			// Save config
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			// Show banner
			banner.PrintWithVersion(version)

			fmt.Println("   ✅ Initialized!")
			fmt.Printf("   Config: %s\n", configPath)
			fmt.Println()
			fmt.Println("   Next steps:")
			fmt.Println("   1. Edit config with your API keys")
			fmt.Println("   2. Add your projects")
			fmt.Println("   3. Run 'pilot start'")

			return nil
		},
	}
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show Pilot version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Pilot v%s\n", version)
			if buildTime != "unknown" {
				fmt.Printf("Built: %s\n", buildTime)
			}
		},
	}
}

func newTaskCmd() *cobra.Command {
	var projectPath string
	var dryRun bool
	var noBranch bool
	var verbose bool
	var createPR bool

	cmd := &cobra.Command{
		Use:   "task [description]",
		Short: "Execute a task using Claude Code",
		Long: `Execute a task using Claude Code with Navigator integration.

Examples:
  pilot task "Add user authentication with JWT"
  pilot task "Fix the login bug in auth.go" --project /path/to/project
  pilot task "Refactor the API handlers" --dry-run
  pilot task "Add index.py with hello world" --verbose
  pilot task "Add new feature" --create-pr`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskDesc := args[0]

			banner.Print()

			// Resolve project path
			if projectPath == "" {
				cwd, _ := os.Getwd()
				projectPath = cwd
			}

			// Generate task ID based on timestamp
			taskID := fmt.Sprintf("TASK-%d", time.Now().Unix()%100000)
			branchName := fmt.Sprintf("pilot/%s", taskID)

			// Check for Navigator
			hasNavigator := false
			if _, err := os.Stat(projectPath + "/.agent"); err == nil {
				hasNavigator = true
			}

			fmt.Println("🚀 Pilot Task Execution")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("   Task ID:   %s\n", taskID)
			fmt.Printf("   Project:   %s\n", projectPath)
			if noBranch {
				fmt.Printf("   Branch:    (current)\n")
			} else {
				fmt.Printf("   Branch:    %s\n", branchName)
			}
			if createPR {
				fmt.Printf("   Create PR: ✓ enabled\n")
			}
			if hasNavigator {
				fmt.Printf("   Navigator: ✓ enabled\n")
			}
			fmt.Println()
			fmt.Println("📋 Task:")
			fmt.Printf("   %s\n", taskDesc)
			fmt.Println("───────────────────────────────────────")
			fmt.Println()

			// Build the task early so we can show prompt in dry-run
			task := &executor.Task{
				ID:          taskID,
				Title:       taskDesc,
				Description: taskDesc,
				ProjectPath: projectPath,
				Branch:      branchName,
				Verbose:     verbose,
				CreatePR:    createPR,
			}

			if noBranch {
				task.Branch = ""
				if createPR {
					fmt.Println("⚠️  Warning: --create-pr requires a branch. Use without --no-branch.")
					return nil
				}
			}

			// Dry run mode - just show what would happen
			if dryRun {
				fmt.Println("🧪 DRY RUN - showing what would execute:")
				fmt.Println()
				fmt.Println("Command: claude -p \"<prompt>\" --verbose --output-format stream-json")
				fmt.Println("Working directory:", projectPath)
				fmt.Println()
				fmt.Println("Prompt:")
				fmt.Println("─────────────────────────────────────")
				// Build actual prompt using a temporary runner
				runner := executor.NewRunner()
				prompt := runner.BuildPrompt(task)
				fmt.Println(prompt)
				fmt.Println("─────────────────────────────────────")
				return nil
			}

			// Create the executor runner
			runner := executor.NewRunner()

			// Create progress display (disabled in verbose mode - show raw JSON instead)
			progress := executor.NewProgressDisplay(task.ID, taskDesc, !verbose)

			// Set up progress callback
			runner.OnProgress(func(taskID, phase string, pct int, message string) {
				if verbose {
					// Verbose mode: simple line output
					timestamp := time.Now().Format("15:04:05")
					if message != "" {
						fmt.Printf("   [%s] %s (%d%%): %s\n", timestamp, phase, pct, message)
					}
				} else {
					// Normal mode: visual progress display
					progress.Update(phase, pct, message)
				}
			})

			fmt.Println("⏳ Executing task with Claude Code...")
			if verbose {
				fmt.Println("   (streaming raw JSON)")
			}
			fmt.Println()

			// Start progress display
			progress.Start()

			// Create context with cancellation on SIGINT
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Handle Ctrl+C
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Println("\n\n⚠️  Cancelling task...")
				cancel()
			}()

			// Execute the task
			result, err := runner.Execute(ctx, task)
			if err != nil {
				return fmt.Errorf("execution failed: %w", err)
			}

			// Finish progress display with result
			progress.Finish(result.Success, result.Output)

			fmt.Println()
			fmt.Println("───────────────────────────────────────")

			if result.Success {
				fmt.Println("✅ Task completed successfully!")
				fmt.Printf("   Duration: %s\n", result.Duration.Round(time.Second))
				if result.PRUrl != "" {
					fmt.Printf("   PR: %s\n", result.PRUrl)
				} else if createPR {
					fmt.Println("   ⚠️  PR not created (check gh auth status)")
				}
				if result.CommitSHA != "" {
					fmt.Printf("   Commit: %s\n", result.CommitSHA[:8])
				}
			} else {
				fmt.Println("❌ Task failed")
				fmt.Printf("   Duration: %s\n", result.Duration.Round(time.Second))
				if result.Error != "" {
					fmt.Printf("   Error: %s\n", result.Error)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path (default: current directory)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be executed without running")
	cmd.Flags().BoolVar(&noBranch, "no-branch", false, "Don't create a new git branch")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Stream Claude Code output")
	cmd.Flags().BoolVar(&createPR, "create-pr", false, "Create GitHub PR after successful execution")

	return cmd
}

func newTelegramCmd() *cobra.Command {
	var projectPath string
	var replace bool

	cmd := &cobra.Command{
		Use:   "telegram",
		Short: "Start Telegram bot for receiving tasks",
		Long: `Start the Telegram bot that listens for messages and executes them as tasks.

Send any message to the bot and it will be executed as a Pilot task.
The bot will reply with the task result.

Commands:
  /help   - Show help message
  /status - Check bot status

Example:
  pilot telegram --project /path/to/project
  pilot telegram --replace  # Kill existing instance first`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Check Telegram config
			if cfg.Adapters.Telegram == nil || !cfg.Adapters.Telegram.Enabled {
				return fmt.Errorf("telegram adapter not enabled in config")
			}

			if cfg.Adapters.Telegram.BotToken == "" {
				return fmt.Errorf("telegram bot_token not configured")
			}

			// Resolve project path (flag > positional arg > cwd)
			if projectPath == "" && len(args) > 0 {
				projectPath = args[0]
			}
			if projectPath == "" {
				cwd, _ := os.Getwd()
				projectPath = cwd
			}
			// Expand ~ to home directory
			if strings.HasPrefix(projectPath, "~") {
				home, _ := os.UserHomeDir()
				projectPath = strings.Replace(projectPath, "~", home, 1)
			}

			// Create runner and handler
			runner := executor.NewRunner()

			// Parse chat ID for allowed IDs
			var allowedIDs []int64
			if cfg.Adapters.Telegram.ChatID != "" {
				if id, err := parseIntID(cfg.Adapters.Telegram.ChatID); err == nil {
					allowedIDs = append(allowedIDs, id)
				}
			}

			handler := telegram.NewHandler(&telegram.HandlerConfig{
				BotToken:      cfg.Adapters.Telegram.BotToken,
				ProjectPath:   projectPath,
				AllowedIDs:    allowedIDs,
				Transcription: cfg.Adapters.Telegram.Transcription,
			}, runner)

			// Check for existing instance
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if err := handler.CheckSingleton(ctx); err != nil {
				if errors.Is(err, telegram.ErrConflict) {
					if replace {
						// Kill existing instance
						fmt.Println("🔄 Stopping existing bot instance...")
						if err := killExistingTelegramBot(); err != nil {
							return fmt.Errorf("failed to stop existing instance: %w", err)
						}
						// Wait for the process to fully terminate
						time.Sleep(500 * time.Millisecond)
						fmt.Println("   ✓ Existing instance stopped")
						fmt.Println()
					} else {
						fmt.Println()
						fmt.Println("❌ Another bot instance is already running")
						fmt.Println()
						fmt.Println("   Options:")
						fmt.Println("   • Kill it manually:  pkill -f 'pilot telegram'")
						fmt.Println("   • Auto-replace:      pilot telegram --replace")
						fmt.Println()
						return fmt.Errorf("conflict: another bot instance is running")
					}
				} else {
					return fmt.Errorf("singleton check failed: %w", err)
				}
			}

			banner.StartupTelegram(version, projectPath, cfg.Adapters.Telegram.ChatID, cfg)

			// Start polling
			handler.StartPolling(ctx)

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			<-sigCh
			fmt.Println("\n🛑 Stopping Telegram bot...")
			handler.Stop()

			return nil
		},
	}

	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path (default: current directory)")
	cmd.Flags().BoolVar(&replace, "replace", false, "Kill existing bot instance before starting")

	return cmd
}

// killExistingTelegramBot finds and kills any running "pilot telegram" process
func killExistingTelegramBot() error {
	// Get current process ID to avoid killing ourselves
	currentPID := os.Getpid()

	// Find processes matching "pilot telegram"
	// Using pgrep for cross-platform compatibility
	out, err := exec.Command("pgrep", "-f", "pilot telegram").Output()
	if err != nil {
		// No process found is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		// pgrep not available, try ps-based approach
		return killExistingTelegramBotPS(currentPID)
	}

	// Parse PIDs and kill each one (except current)
	pids := strings.Fields(strings.TrimSpace(string(out)))
	for _, pidStr := range pids {
		var pid int
		if _, err := fmt.Sscanf(pidStr, "%d", &pid); err != nil {
			continue
		}
		if pid == currentPID {
			continue
		}
		// Send SIGTERM for graceful shutdown
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Signal(syscall.SIGTERM)
	}

	return nil
}

// killExistingTelegramBotPS uses ps + grep as fallback
func killExistingTelegramBotPS(currentPID int) error {
	// ps aux | grep "pilot telegram" | grep -v grep
	out, err := exec.Command("sh", "-c", "ps aux | grep 'pilot telegram' | grep -v grep | awk '{print $2}'").Output()
	if err != nil {
		return nil // Ignore errors - process may not exist
	}

	pids := strings.Fields(strings.TrimSpace(string(out)))
	for _, pidStr := range pids {
		var pid int
		if _, err := fmt.Sscanf(pidStr, "%d", &pid); err != nil {
			continue
		}
		if pid == currentPID {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		_ = proc.Signal(syscall.SIGTERM)
	}

	return nil
}

// parseIntID parses a string ID to int64
func parseIntID(s string) (int64, error) {
	return parseInt64(s)
}

// parseInt64 parses a string to int64
func parseInt64(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	return id, err
}

func newBriefCmd() *cobra.Command {
	var now bool
	var weekly bool

	cmd := &cobra.Command{
		Use:   "brief",
		Short: "Generate and send daily briefs",
		Long: `Generate and optionally send daily/weekly briefs summarizing Pilot activity.

Examples:
  pilot brief           # Show scheduler status
  pilot brief --now     # Generate and send brief immediately
  pilot brief --weekly  # Generate a weekly summary`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Check if brief config exists
			briefCfg := cfg.Orchestrator.DailyBrief
			if briefCfg == nil {
				fmt.Println("❌ Brief not configured in config.yaml")
				fmt.Println()
				fmt.Println("   Add the following to your config:")
				fmt.Println()
				fmt.Println("   orchestrator:")
				fmt.Println("     daily_brief:")
				fmt.Println("       enabled: true")
				fmt.Println("       schedule: \"0 9 * * 1-5\"")
				fmt.Println("       timezone: \"America/New_York\"")
				fmt.Println("       channels:")
				fmt.Println("         - type: slack")
				fmt.Println("           channel: \"#dev-briefs\"")
				return nil
			}

			// Create memory store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Convert config to briefs.BriefConfig
			briefsConfig := &briefs.BriefConfig{
				Enabled:  briefCfg.Enabled,
				Schedule: briefCfg.Schedule,
				Timezone: briefCfg.Timezone,
				Content: briefs.ContentConfig{
					IncludeMetrics:     briefCfg.Content.IncludeMetrics,
					IncludeErrors:      briefCfg.Content.IncludeErrors,
					MaxItemsPerSection: briefCfg.Content.MaxItemsPerSection,
				},
				Filters: briefs.FilterConfig{
					Projects: briefCfg.Filters.Projects,
				},
			}

			// Convert channels
			for _, ch := range briefCfg.Channels {
				briefsConfig.Channels = append(briefsConfig.Channels, briefs.ChannelConfig{
					Type:       ch.Type,
					Channel:    ch.Channel,
					Recipients: ch.Recipients,
				})
			}

			// Create generator
			generator := briefs.NewGenerator(store, briefsConfig)

			// If --now flag, generate and optionally deliver
			if now || weekly {
				fmt.Println("📊 Generating Brief")
				fmt.Println("───────────────────────────────────────")

				var brief *briefs.Brief
				if weekly {
					brief, err = generator.GenerateWeekly()
				} else {
					brief, err = generator.GenerateDaily()
				}
				if err != nil {
					return fmt.Errorf("failed to generate brief: %w", err)
				}

				// Format as plain text for display
				formatter := briefs.NewPlainTextFormatter()
				text, err := formatter.Format(brief)
				if err != nil {
					return fmt.Errorf("failed to format brief: %w", err)
				}

				fmt.Println()
				fmt.Println(text)

				// If channels configured, ask to deliver
				if len(briefsConfig.Channels) > 0 {
					fmt.Println("───────────────────────────────────────")
					fmt.Printf("📤 Deliver to %d configured channel(s)? [y/N]: ", len(briefsConfig.Channels))

					var input string
					_, _ = fmt.Scanln(&input)

					if strings.ToLower(input) == "y" {
						// Create delivery service
						var deliveryOpts []briefs.DeliveryOption

						// Add Slack client if configured
						if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
							slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
							deliveryOpts = append(deliveryOpts, briefs.WithSlackClient(slackClient))
						}

						deliveryOpts = append(deliveryOpts, briefs.WithLogger(slog.Default()))

						delivery := briefs.NewDeliveryService(briefsConfig, deliveryOpts...)
						results := delivery.DeliverAll(context.Background(), brief)

						fmt.Println()
						for _, result := range results {
							if result.Success {
								fmt.Printf("   ✅ %s delivered\n", result.Channel)
							} else {
								fmt.Printf("   ❌ %s failed: %v\n", result.Channel, result.Error)
							}
						}
					}
				}

				return nil
			}

			// Default: show status
			fmt.Println("📊 Brief Scheduler Status")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("   Enabled:  %v\n", briefCfg.Enabled)
			fmt.Printf("   Schedule: %s\n", briefCfg.Schedule)
			fmt.Printf("   Timezone: %s\n", briefCfg.Timezone)
			fmt.Println()

			fmt.Println("Channels:")
			if len(briefCfg.Channels) == 0 {
				fmt.Println("   (none configured)")
			} else {
				for _, ch := range briefCfg.Channels {
					fmt.Printf("   • %s: %s\n", ch.Type, ch.Channel)
				}
			}
			fmt.Println()

			if !briefCfg.Enabled {
				fmt.Println("💡 Briefs are disabled. Enable in config:")
				fmt.Println("   orchestrator.daily_brief.enabled: true")
			} else {
				fmt.Println("💡 Run 'pilot brief --now' to generate immediately")
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&now, "now", false, "Generate and send brief immediately")
	cmd.Flags().BoolVar(&weekly, "weekly", false, "Generate weekly summary instead of daily")

	return cmd
}

func newPatternsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "patterns",
		Short: "Manage cross-project patterns",
		Long:  `View, search, and manage learned patterns across projects.`,
	}

	cmd.AddCommand(
		newPatternsListCmd(),
		newPatternsSearchCmd(),
		newPatternsStatsCmd(),
	)

	return cmd
}

func newPatternsListCmd() *cobra.Command {
	var (
		limit      int
		minConf    float64
		patternType string
		showAnti   bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List learned patterns",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Query patterns
			ctx := context.Background()
			queryService := memory.NewPatternQueryService(store)

			query := &memory.PatternQuery{
				MaxResults:    limit,
				MinConfidence: minConf,
				IncludeAnti:   showAnti,
			}

			if patternType != "" {
				query.Types = []string{patternType}
			}

			result, err := queryService.Query(ctx, query)
			if err != nil {
				return fmt.Errorf("query failed: %w", err)
			}

			if len(result.Patterns) == 0 {
				fmt.Println("No patterns found.")
				return nil
			}

			fmt.Printf("Found %d patterns (showing %d):\n\n", result.TotalMatches, len(result.Patterns))

			for _, p := range result.Patterns {
				icon := "📘"
				if p.IsAntiPattern {
					icon = "⚠️"
				}
				fmt.Printf("%s %s (%.0f%% confidence)\n", icon, p.Title, p.Confidence*100)
				fmt.Printf("   Type: %s | Uses: %d | Scope: %s\n", p.Type, p.Occurrences, p.Scope)
				if p.Description != "" {
					desc := p.Description
					if len(desc) > 80 {
						desc = desc[:77] + "..."
					}
					fmt.Printf("   %s\n", desc)
				}
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum patterns to show")
	cmd.Flags().Float64Var(&minConf, "min-confidence", 0.5, "Minimum confidence threshold")
	cmd.Flags().StringVar(&patternType, "type", "", "Filter by type (code, structure, workflow, error, naming)")
	cmd.Flags().BoolVar(&showAnti, "anti", false, "Include anti-patterns")

	return cmd
}

func newPatternsSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search patterns by keyword",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]

			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Search patterns
			patterns, err := store.SearchCrossPatterns(query, 20)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}

			if len(patterns) == 0 {
				fmt.Printf("No patterns found matching '%s'\n", query)
				return nil
			}

			fmt.Printf("Found %d patterns matching '%s':\n\n", len(patterns), query)

			for _, p := range patterns {
				icon := "📘"
				if p.IsAntiPattern {
					icon = "⚠️"
				}
				fmt.Printf("%s %s (%.0f%%)\n", icon, p.Title, p.Confidence*100)
				if p.Context != "" {
					fmt.Printf("   Context: %s\n", p.Context)
				}
				fmt.Printf("   %s\n\n", p.Description)
			}

			return nil
		},
	}

	return cmd
}

func newPatternsStatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Show pattern statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Open store
			store, err := memory.NewStore(cfg.Memory.Path)
			if err != nil {
				return fmt.Errorf("failed to open memory store: %w", err)
			}
			defer func() { _ = store.Close() }()

			// Get stats
			stats, err := store.GetCrossPatternStats()
			if err != nil {
				return fmt.Errorf("failed to get stats: %w", err)
			}

			fmt.Println("📊 Cross-Project Pattern Statistics")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("Total Patterns:     %d\n", stats.TotalPatterns)
			fmt.Printf("  ├─ Patterns:      %d\n", stats.Patterns)
			fmt.Printf("  └─ Anti-Patterns: %d\n", stats.AntiPatterns)
			fmt.Printf("Avg Confidence:     %.1f%%\n", stats.AvgConfidence*100)
			fmt.Printf("Total Occurrences:  %d\n", stats.TotalOccurrences)
			fmt.Printf("Projects Using:     %d\n", stats.ProjectCount)
			fmt.Println()

			if len(stats.ByType) > 0 {
				fmt.Println("By Type:")
				for pType, count := range stats.ByType {
					fmt.Printf("  %s: %d\n", pType, count)
				}
			}

			return nil
		},
	}

	return cmd
}
