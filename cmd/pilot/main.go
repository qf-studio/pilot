package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/banner"
	"github.com/alekspetrov/pilot/internal/briefs"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/dashboard"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/pilot"
	"github.com/alekspetrov/pilot/internal/replay"
	"github.com/alekspetrov/pilot/internal/upgrade"
)

var (
	version   = "0.2.0"
	buildTime = "unknown"
	cfgFile   string
)

var quietMode bool

func main() {
	rootCmd := &cobra.Command{
		Use:   "pilot",
		Short: "AI that ships your tickets",
		Long:  `Pilot is an autonomous AI development pipeline that receives tickets, implements features, and creates PRs.`,
		Run: func(cmd *cobra.Command, args []string) {
			// If no subcommand provided, enter interactive mode
			if err := runInteractiveMode(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		},
	}

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ~/.pilot/config.yaml)")
	rootCmd.PersistentFlags().BoolVarP(&quietMode, "quiet", "q", false, "Suppress non-essential output")

	rootCmd.AddCommand(
		newStartCmd(),
		newStopCmd(),
		newStatusCmd(),
		newInitCmd(),
		newVersionCmd(),
		newTaskCmd(),
		newTelegramCmd(),
		newGitHubCmd(),
		newBriefCmd(),
		newPatternsCmd(),
		newMetricsCmd(),
		newUsageCmd(),
		newTeamCmd(),
		newBudgetCmd(),
		newDoctorCmd(),
		newSetupCmd(),
		newReplayCmd(),
		newTunnelCmd(),
		newCompletionCmd(),
		newConfigCmd(),
		newLogsCmd(),
		newWebhooksCmd(),
		newUpgradeCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newStartCmd() *cobra.Command {
	var daemon bool
	var dashboardMode bool

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

			// Check for updates in background (non-blocking)
			go checkForUpdates()

			if dashboardMode {
				// Run TUI dashboard mode
				return runDashboardMode(p, cfg)
			}

			// Show startup banner (headless mode)
			gatewayURL := fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
			banner.StartupBanner(version, gatewayURL)

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			<-sigCh
			fmt.Println("\n🛑 Shutting down...")

			return p.Stop()
		},
	}

	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run in background (daemon mode)")
	cmd.Flags().BoolVar(&dashboardMode, "dashboard", false, "Show TUI dashboard for real-time task monitoring")

	return cmd
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Pilot daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Daemon process management is out of scope - users should use
			// standard OS signals (Ctrl+C) or process managers (systemd, launchd)
			fmt.Println("🛑 Stopping Pilot daemon...")
			fmt.Println("   Use Ctrl+C or send SIGTERM to stop the daemon")
			return nil
		},
	}
}

func newStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
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

			if jsonOutput {
				status := map[string]interface{}{
					"gateway": fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port),
					"adapters": map[string]bool{
						"linear":   cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled,
						"slack":    cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled,
						"telegram": cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled,
						"github":   cfg.Adapters.Github != nil && cfg.Adapters.Github.Enabled,
						"jira":     cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled,
					},
					"projects": cfg.Projects,
				}

				data, err := json.MarshalIndent(status, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal status: %w", err)
				}
				fmt.Println(string(data))
				return nil
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
			if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
				fmt.Println("  ✓ Telegram (enabled)")
			} else {
				fmt.Println("  ○ Telegram (disabled)")
			}
			if cfg.Adapters.Github != nil && cfg.Adapters.Github.Enabled {
				fmt.Println("  ✓ GitHub (enabled)")
			} else {
				fmt.Println("  ○ GitHub (disabled)")
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

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
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
	var enableAlerts bool

	cmd := &cobra.Command{
		Use:   "task [description]",
		Short: "Execute a task using Claude Code",
		Long: `Execute a task using Claude Code with Navigator integration.

Examples:
  pilot task "Add user authentication with JWT"
  pilot task "Fix the login bug in auth.go" --project /path/to/project
  pilot task "Refactor the API handlers" --dry-run
  pilot task "Add index.py with hello world" --verbose
  pilot task "Add new feature" --create-pr
  pilot task "Fix bug" --alerts`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskDesc := args[0]

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

			// Initialize alerts engine if --alerts flag is set
			var alertsEngine *alerts.Engine
			if enableAlerts {
				// Load config for alerts
				configPath := cfgFile
				if configPath == "" {
					configPath = config.DefaultConfigPath()
				}

				cfg, err := config.Load(configPath)
				if err != nil {
					return fmt.Errorf("failed to load config for alerts: %w", err)
				}

				// Get alerts config
				alertsCfg := getAlertsConfig(cfg)
				if alertsCfg == nil {
					// Use default config with alerts enabled
					alertsCfg = alerts.DefaultConfig()
					alertsCfg.Enabled = true
				} else {
					alertsCfg.Enabled = true
				}

				// Create dispatcher and register channels
				dispatcher := alerts.NewDispatcher(alertsCfg)

				// Register Slack channel if configured
				if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
					slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
					for _, ch := range alertsCfg.Channels {
						if ch.Type == "slack" && ch.Slack != nil {
							slackChannel := alerts.NewSlackChannel(ch.Name, slackClient, ch.Slack.Channel)
							dispatcher.RegisterChannel(slackChannel)
						}
					}
				}

				// Register Telegram channel if configured
				if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
					telegramClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
					for _, ch := range alertsCfg.Channels {
						if ch.Type == "telegram" && ch.Telegram != nil {
							telegramChannel := alerts.NewTelegramChannel(ch.Name, telegramClient, ch.Telegram.ChatID)
							dispatcher.RegisterChannel(telegramChannel)
						}
					}
				}

				alertsEngine = alerts.NewEngine(alertsCfg, alerts.WithDispatcher(dispatcher))
				if err := alertsEngine.Start(ctx); err != nil {
					return fmt.Errorf("failed to start alerts engine: %w", err)
				}
				defer alertsEngine.Stop()

				fmt.Printf("   Alerts:    ✓ enabled (%d channels)\n", len(dispatcher.ListChannels()))

				// Send task started event
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeTaskStarted,
					TaskID:    taskID,
					TaskTitle: taskDesc,
					Project:   projectPath,
					Timestamp: time.Now(),
				})
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

				// Send progress event to alerts engine
				if alertsEngine != nil {
					alertsEngine.ProcessEvent(alerts.Event{
						Type:      alerts.EventTypeTaskProgress,
						TaskID:    taskID,
						TaskTitle: taskDesc,
						Project:   projectPath,
						Phase:     phase,
						Progress:  pct,
						Timestamp: time.Now(),
					})
				}
			})

			fmt.Println("⏳ Executing task with Claude Code...")
			if verbose {
				fmt.Println("   (streaming raw JSON)")
			}
			fmt.Println()

			// Start progress display
			progress.Start()

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

				// Send task completed event to alerts engine
				if alertsEngine != nil {
					alertsEngine.ProcessEvent(alerts.Event{
						Type:      alerts.EventTypeTaskCompleted,
						TaskID:    taskID,
						TaskTitle: taskDesc,
						Project:   projectPath,
						Timestamp: time.Now(),
						Metadata: map[string]string{
							"duration":   result.Duration.String(),
							"pr_url":     result.PRUrl,
							"commit_sha": result.CommitSHA,
						},
					})
				}
			} else {
				fmt.Println("❌ Task failed")
				fmt.Printf("   Duration: %s\n", result.Duration.Round(time.Second))
				if result.Error != "" {
					fmt.Printf("   Error: %s\n", result.Error)
				}

				// Send task failed event to alerts engine
				if alertsEngine != nil {
					alertsEngine.ProcessEvent(alerts.Event{
						Type:      alerts.EventTypeTaskFailed,
						TaskID:    taskID,
						TaskTitle: taskDesc,
						Project:   projectPath,
						Error:     result.Error,
						Timestamp: time.Now(),
						Metadata: map[string]string{
							"duration": result.Duration.String(),
						},
					})
					// Give time for alert to be sent before exiting
					time.Sleep(500 * time.Millisecond)
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
	cmd.Flags().BoolVar(&enableAlerts, "alerts", false, "Enable alerts for task execution")

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

			// Resolve project path priority: flag > positional arg > config default > cwd
			if projectPath == "" && len(args) > 0 {
				projectPath = args[0]
			}
			if projectPath == "" {
				// Try to get default from config
				if defaultProj := cfg.GetDefaultProject(); defaultProj != nil {
					projectPath = defaultProj.Path
				}
			}
			if projectPath == "" {
				// Fall back to current working directory
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
				Projects:      config.NewProjectSource(cfg),
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

						// Retry singleton check with exponential backoff
						// Telegram API needs time to release the connection
						fmt.Print("   Waiting for Telegram to release connection")
						maxRetries := 10
						var lastErr error
						for i := 0; i < maxRetries; i++ {
							// Exponential backoff: 500ms, 1s, 1.5s, 2s...
							delay := time.Duration(500+i*500) * time.Millisecond
							time.Sleep(delay)
							fmt.Print(".")

							if err := handler.CheckSingleton(ctx); err == nil {
								fmt.Println(" ✓")
								fmt.Println("   ✓ Existing instance stopped")
								fmt.Println()
								lastErr = nil
								break
							} else {
								lastErr = err
							}
						}
						if lastErr != nil {
							fmt.Println(" ✗")
							return fmt.Errorf("timeout waiting for Telegram to release connection after %d retries", maxRetries)
						}
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

			// Start GitHub polling if enabled
			var ghPoller *github.Poller
			if cfg.Adapters.Github != nil && cfg.Adapters.Github.Enabled &&
				cfg.Adapters.Github.Polling != nil && cfg.Adapters.Github.Polling.Enabled {

				token := cfg.Adapters.Github.Token
				if token == "" {
					token = os.Getenv("GITHUB_TOKEN")
				}

				if token != "" && cfg.Adapters.Github.Repo != "" {
					client := github.NewClient(token)
					label := cfg.Adapters.Github.Polling.Label
					if label == "" {
						label = cfg.Adapters.Github.PilotLabel
					}
					interval := cfg.Adapters.Github.Polling.Interval
					if interval == 0 {
						interval = 30 * time.Second
					}

					var err error
					ghPoller, err = github.NewPoller(client, cfg.Adapters.Github.Repo, label, interval,
						github.WithOnIssue(func(issueCtx context.Context, issue *github.Issue) error {
							// Execute issue as task via handler
							fmt.Printf("\n📥 GitHub Issue #%d: %s\n", issue.Number, issue.Title)

							// Add in-progress label
							parts := strings.Split(cfg.Adapters.Github.Repo, "/")
							if len(parts) == 2 {
								_ = client.AddLabels(issueCtx, parts[0], parts[1], issue.Number, []string{github.LabelInProgress})
							}

							// Build task description
							taskDesc := fmt.Sprintf("GitHub Issue #%d: %s\n\n%s", issue.Number, issue.Title, issue.Body)
							taskID := fmt.Sprintf("GH-%d", issue.Number)
							branchName := fmt.Sprintf("pilot/%s", taskID)

							task := &executor.Task{
								ID:          taskID,
								Title:       issue.Title,
								Description: taskDesc,
								ProjectPath: projectPath,
								Branch:      branchName,
								CreatePR:    true, // Auto-create PR for GitHub issues
							}

							result, execErr := runner.Execute(issueCtx, task)

							// Update issue with result
							if len(parts) == 2 {
								_ = client.RemoveLabel(issueCtx, parts[0], parts[1], issue.Number, github.LabelInProgress)

								if execErr != nil {
									_ = client.AddLabels(issueCtx, parts[0], parts[1], issue.Number, []string{github.LabelFailed})
									comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
									_, _ = client.AddComment(issueCtx, parts[0], parts[1], issue.Number, comment)
								} else {
									_ = client.AddLabels(issueCtx, parts[0], parts[1], issue.Number, []string{github.LabelDone})
									comment := fmt.Sprintf("✅ Pilot completed!\n\n**Duration:** %s\n**Branch:** `%s`",
										result.Duration, branchName)
									if result.PRUrl != "" {
										comment += fmt.Sprintf("\n**PR:** %s", result.PRUrl)
									}
									_, _ = client.AddComment(issueCtx, parts[0], parts[1], issue.Number, comment)
								}
							}

							return execErr
						}),
					)
					if err != nil {
						fmt.Printf("⚠️  GitHub polling disabled: %v\n", err)
					} else {
						fmt.Printf("🐙 GitHub polling enabled: %s (every %s)\n", cfg.Adapters.Github.Repo, interval)
						go ghPoller.Start(ctx)
					}
				}
			}

			// Start Telegram polling
			handler.StartPolling(ctx)

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			<-sigCh
			fmt.Println("\n🛑 Stopping Telegram bot...")
			handler.Stop()
			if ghPoller != nil {
				fmt.Println("🐙 Stopping GitHub poller...")
			}

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

func newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub integration commands",
		Long:  `Commands for working with GitHub issues and pull requests.`,
	}

	cmd.AddCommand(newGitHubRunCmd())
	return cmd
}

func newGitHubRunCmd() *cobra.Command {
	var projectPath string
	var dryRun bool
	var verbose bool
	var createPR bool
	var repo string

	cmd := &cobra.Command{
		Use:   "run <issue-number>",
		Short: "Run a GitHub issue as a Pilot task",
		Long: `Fetch a GitHub issue and execute it as a Pilot task.

Examples:
  pilot github run 8
  pilot github run 8 --repo owner/repo
  pilot github run 8 --create-pr
  pilot github run 8 --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNum, err := parseInt64(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			// Load config
			// Resolve config path
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}

			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Check GitHub is configured
			if cfg.Adapters == nil || cfg.Adapters.Github == nil || !cfg.Adapters.Github.Enabled {
				return fmt.Errorf("GitHub adapter not enabled. Run 'pilot setup' or edit ~/.pilot/config.yaml")
			}

			token := cfg.Adapters.Github.Token
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("GitHub token not configured. Set GITHUB_TOKEN env or add to config")
			}

			// Determine repo
			if repo == "" {
				repo = cfg.Adapters.Github.Repo
			}
			if repo == "" {
				return fmt.Errorf("no repository specified. Use --repo owner/repo or set in config")
			}

			parts := strings.Split(repo, "/")
			if len(parts) != 2 {
				return fmt.Errorf("invalid repo format. Use owner/repo")
			}
			owner, repoName := parts[0], parts[1]

			// Resolve project path
			if projectPath == "" {
				// Try to find project by repo
				for _, p := range cfg.Projects {
					if p.GitHub != nil && p.GitHub.Owner == owner && p.GitHub.Repo == repoName {
						projectPath = p.Path
						break
					}
				}
				if projectPath == "" {
					cwd, _ := os.Getwd()
					projectPath = cwd
				}
			}

			// Fetch issue from GitHub
			client := github.NewClient(token)
			ctx := context.Background()

			fmt.Printf("📥 Fetching issue #%d from %s...\n", issueNum, repo)
			issue, err := client.GetIssue(ctx, owner, repoName, int(issueNum))
			if err != nil {
				return fmt.Errorf("failed to fetch issue: %w", err)
			}

			banner.Print()

			taskID := fmt.Sprintf("GH-%d", issueNum)
			branchName := fmt.Sprintf("pilot/%s", taskID)

			// Check for Navigator
			hasNavigator := false
			if _, err := os.Stat(projectPath + "/.agent"); err == nil {
				hasNavigator = true
			}

			fmt.Println("🚀 Pilot GitHub Task Execution")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("   Issue:     #%d\n", issue.Number)
			fmt.Printf("   Title:     %s\n", issue.Title)
			fmt.Printf("   Task ID:   %s\n", taskID)
			fmt.Printf("   Project:   %s\n", projectPath)
			fmt.Printf("   Branch:    %s\n", branchName)
			if createPR {
				fmt.Printf("   Create PR: ✓ enabled\n")
			}
			if hasNavigator {
				fmt.Printf("   Navigator: ✓ enabled\n")
			}
			fmt.Println()
			fmt.Println("📋 Issue Body:")
			fmt.Println("───────────────────────────────────────")
			if issue.Body != "" {
				fmt.Println(issue.Body)
			} else {
				fmt.Println("(no body)")
			}
			fmt.Println("───────────────────────────────────────")
			fmt.Println()

			// Build task description
			taskDesc := fmt.Sprintf("GitHub Issue #%d: %s\n\n%s", issue.Number, issue.Title, issue.Body)

			task := &executor.Task{
				ID:          taskID,
				Title:       issue.Title,
				Description: taskDesc,
				ProjectPath: projectPath,
				Branch:      branchName,
				Verbose:     verbose,
				CreatePR:    createPR,
			}

			// Dry run mode
			if dryRun {
				fmt.Println("🧪 DRY RUN - showing what would execute:")
				fmt.Println()
				runner := executor.NewRunner()
				prompt := runner.BuildPrompt(task)
				fmt.Println("Prompt:")
				fmt.Println("─────────────────────────────────────")
				fmt.Println(prompt)
				fmt.Println("─────────────────────────────────────")
				return nil
			}

			// Add in-progress label
			fmt.Println("🏷️  Adding in-progress label...")
			_ = client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-in-progress"})

			// Execute the task
			runner := executor.NewRunner()
			fmt.Println()
			fmt.Println("⏳ Executing task with Claude Code...")
			fmt.Println()

			result, err := runner.Execute(ctx, task)
			if err != nil {
				// Add failed label
				_ = client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-failed"})
				_ = client.RemoveLabel(ctx, owner, repoName, int(issueNum), "pilot-in-progress")

				comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", err.Error())
				_, _ = client.AddComment(ctx, owner, repoName, int(issueNum), comment)

				return fmt.Errorf("task execution failed: %w", err)
			}

			// Success - update labels and add comment
			_ = client.RemoveLabel(ctx, owner, repoName, int(issueNum), "pilot-in-progress")
			_ = client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-done"})

			comment := fmt.Sprintf("✅ Pilot completed successfully!\n\n**Duration:** %s\n**Branch:** `%s`",
				result.Duration, branchName)
			if result.PRUrl != "" {
				comment += fmt.Sprintf("\n**PR:** %s", result.PRUrl)
			}
			_, _ = client.AddComment(ctx, owner, repoName, int(issueNum), comment)

			fmt.Println()
			fmt.Println("───────────────────────────────────────")
			fmt.Println("✅ Task completed successfully!")
			fmt.Printf("   Duration: %s\n", result.Duration)
			if result.PRUrl != "" {
				fmt.Printf("   PR: %s\n", result.PRUrl)
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository (owner/repo)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would execute without running")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	cmd.Flags().BoolVar(&createPR, "create-pr", false, "Create a PR after task completion")

	return cmd
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
		limit       int
		minConf     float64
		patternType string
		showAnti    bool
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

// Replay commands (TASK-21)

func newReplayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay",
		Short: "Replay and debug execution recordings",
		Long:  `View, replay, and analyze execution recordings for debugging and improvement.`,
	}

	cmd.AddCommand(
		newReplayListCmd(),
		newReplayShowCmd(),
		newReplayPlayCmd(),
		newReplayAnalyzeCmd(),
		newReplayExportCmd(),
		newReplayDeleteCmd(),
	)

	return cmd
}

func newReplayListCmd() *cobra.Command {
	var (
		limit   int
		project string
		status  string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List execution recordings",
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingsPath := replay.DefaultRecordingsPath()

			filter := &replay.RecordingFilter{
				Limit:       limit,
				ProjectPath: project,
				Status:      status,
			}

			recordings, err := replay.ListRecordings(recordingsPath, filter)
			if err != nil {
				return fmt.Errorf("failed to list recordings: %w", err)
			}

			if len(recordings) == 0 {
				fmt.Println("No recordings found.")
				fmt.Println()
				fmt.Println("💡 Recordings are created automatically when you run tasks.")
				return nil
			}

			fmt.Println("📹 Execution Recordings")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			for _, rec := range recordings {
				statusIcon := "✅"
				switch rec.Status {
				case "failed":
					statusIcon = "❌"
				case "cancelled":
					statusIcon = "⚠️"
				}

				fmt.Printf("%s %s\n", statusIcon, rec.ID)
				fmt.Printf("   Task:     %s\n", rec.TaskID)
				fmt.Printf("   Duration: %s | Events: %d\n", rec.Duration.Round(time.Second), rec.EventCount)
				fmt.Printf("   Started:  %s\n", rec.StartTime.Format("2006-01-02 15:04:05"))
				fmt.Println()
			}

			fmt.Printf("Showing %d recording(s)\n", len(recordings))
			fmt.Println()
			fmt.Println("💡 Use 'pilot replay show <id>' for details")
			fmt.Println("   Use 'pilot replay play <id>' to replay")

			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum recordings to show")
	cmd.Flags().StringVar(&project, "project", "", "Filter by project path")
	cmd.Flags().StringVar(&status, "status", "", "Filter by status (completed, failed, cancelled)")

	return cmd
}

func newReplayShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <recording-id>",
		Short: "Show recording details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingID := args[0]
			recordingsPath := replay.DefaultRecordingsPath()

			recording, err := replay.LoadRecording(recordingsPath, recordingID)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("📹 RECORDING: %s\n", recording.ID)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println()

			statusIcon := "✅"
			switch recording.Status {
			case "failed":
				statusIcon = "❌"
			case "cancelled":
				statusIcon = "⚠️"
			}

			fmt.Printf("Status:   %s %s\n", statusIcon, recording.Status)
			fmt.Printf("Task:     %s\n", recording.TaskID)
			fmt.Printf("Project:  %s\n", recording.ProjectPath)
			fmt.Printf("Duration: %s\n", recording.Duration.Round(time.Second))
			fmt.Printf("Events:   %d\n", recording.EventCount)
			fmt.Printf("Started:  %s\n", recording.StartTime.Format("2006-01-02 15:04:05"))
			fmt.Printf("Ended:    %s\n", recording.EndTime.Format("2006-01-02 15:04:05"))
			fmt.Println()

			if recording.Metadata != nil {
				fmt.Println("METADATA")
				fmt.Println("───────────────────────────────────────")
				if recording.Metadata.Branch != "" {
					fmt.Printf("  Branch:    %s\n", recording.Metadata.Branch)
				}
				if recording.Metadata.CommitSHA != "" {
					fmt.Printf("  Commit:    %s\n", recording.Metadata.CommitSHA)
				}
				if recording.Metadata.PRUrl != "" {
					fmt.Printf("  PR:        %s\n", recording.Metadata.PRUrl)
				}
				if recording.Metadata.ModelName != "" {
					fmt.Printf("  Model:     %s\n", recording.Metadata.ModelName)
				}
				fmt.Printf("  Navigator: %v\n", recording.Metadata.HasNavigator)
				fmt.Println()
			}

			if recording.TokenUsage != nil {
				fmt.Println("TOKEN USAGE")
				fmt.Println("───────────────────────────────────────")
				fmt.Printf("  Input:    %d tokens\n", recording.TokenUsage.InputTokens)
				fmt.Printf("  Output:   %d tokens\n", recording.TokenUsage.OutputTokens)
				fmt.Printf("  Total:    %d tokens\n", recording.TokenUsage.TotalTokens)
				fmt.Printf("  Cost:     $%.4f\n", recording.TokenUsage.EstimatedCostUSD)
				fmt.Println()
			}

			if len(recording.PhaseTimings) > 0 {
				fmt.Println("PHASE TIMINGS")
				fmt.Println("───────────────────────────────────────")
				for _, pt := range recording.PhaseTimings {
					pct := float64(pt.Duration) / float64(recording.Duration) * 100
					fmt.Printf("  %-12s %8s (%5.1f%%)\n", pt.Phase+":", pt.Duration.Round(time.Second), pct)
				}
				fmt.Println()
			}

			fmt.Println("FILES")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("  Stream:   %s\n", recording.StreamPath)
			fmt.Printf("  Summary:  %s\n", recording.SummaryPath)
			fmt.Println()

			fmt.Println("💡 Use 'pilot replay play " + recording.ID + "' to replay")
			fmt.Println("   Use 'pilot replay analyze " + recording.ID + "' for detailed analysis")

			return nil
		},
	}

	return cmd
}

func newReplayPlayCmd() *cobra.Command {
	var (
		startAt     int
		stopAt      int
		verbose     bool
		interactive bool
		speed       float64
		filterTools bool
		filterText  bool
		filterAll   bool
	)

	cmd := &cobra.Command{
		Use:   "play <recording-id>",
		Short: "Replay an execution recording",
		Long: `Replay an execution recording with an interactive TUI viewer.

The interactive viewer supports:
  - Play/pause with spacebar
  - Speed control (1-4 keys for 0.5x, 1x, 2x, 4x)
  - Event filtering (t=tools, x=text, r=results, s=system, e=errors)
  - Navigation with arrow keys or j/k
  - Jump to start (g) or end (G)

Examples:
  pilot replay play TG-1234567890              # Interactive viewer
  pilot replay play TG-1234567890 --no-tui     # Simple output mode
  pilot replay play TG-1234567890 --start 50   # Start from event 50
  pilot replay play TG-1234567890 --verbose    # Show all details`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingID := args[0]
			recordingsPath := replay.DefaultRecordingsPath()

			recording, err := replay.LoadRecording(recordingsPath, recordingID)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			// Use interactive viewer by default if terminal supports it
			if interactive && replay.CheckTerminalSupport() {
				filter := replay.DefaultEventFilter()
				if filterTools && !filterAll {
					filter = replay.EventFilter{ShowTools: true}
				}
				if filterText && !filterAll {
					filter.ShowText = true
				}

				return replay.RunViewerWithOptions(recording, startAt, filter)
			}

			// Fallback to simple output mode
			options := &replay.ReplayOptions{
				StartAt:     startAt,
				StopAt:      stopAt,
				Speed:       speed,
				ShowTools:   true,
				ShowText:    true,
				ShowResults: verbose,
				Verbose:     verbose,
			}

			player, err := replay.NewPlayer(recording, options)
			if err != nil {
				return fmt.Errorf("failed to create player: %w", err)
			}

			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("▶️  REPLAYING: %s\n", recording.ID)
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Printf("Task: %s | Events: %d | Duration: %s\n",
				recording.TaskID, recording.EventCount, recording.Duration.Round(time.Second))
			if speed > 0 {
				fmt.Printf("Speed: %.1fx\n", speed)
			}
			fmt.Println()

			// Play with callback
			player.OnEvent(func(event *replay.StreamEvent, index, total int) error {
				formatted := replay.FormatEvent(event, verbose)
				fmt.Printf("[%d/%d] %s\n", index+1, total, formatted)
				return nil
			})

			ctx := context.Background()
			if err := player.Play(ctx); err != nil {
				return fmt.Errorf("replay failed: %w", err)
			}

			fmt.Println()
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
			fmt.Println("⏹️  REPLAY COMPLETE")
			fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

			return nil
		},
	}

	cmd.Flags().IntVar(&startAt, "start", 0, "Start from event sequence number")
	cmd.Flags().IntVar(&stopAt, "stop", 0, "Stop at event sequence number (0 = end)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show all event details")
	cmd.Flags().BoolVar(&interactive, "tui", true, "Use interactive TUI viewer")
	cmd.Flags().Float64Var(&speed, "speed", 0, "Playback speed (0 = instant, 1 = real-time, 2 = 2x, etc)")
	cmd.Flags().BoolVar(&filterTools, "tools-only", false, "Show only tool calls")
	cmd.Flags().BoolVar(&filterText, "text-only", false, "Show only text events")
	cmd.Flags().BoolVar(&filterAll, "all", true, "Show all event types")

	return cmd
}

func newReplayAnalyzeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "analyze <recording-id>",
		Short: "Analyze an execution recording",
		Long:  `Generate detailed analysis of token usage, phase timing, tool usage, and errors.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingID := args[0]
			recordingsPath := replay.DefaultRecordingsPath()

			recording, err := replay.LoadRecording(recordingsPath, recordingID)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			analyzer, err := replay.NewAnalyzer(recording)
			if err != nil {
				return fmt.Errorf("failed to create analyzer: %w", err)
			}

			report, err := analyzer.Analyze()
			if err != nil {
				return fmt.Errorf("analysis failed: %w", err)
			}

			fmt.Print(replay.FormatReport(report))

			return nil
		},
	}

	return cmd
}

func newReplayExportCmd() *cobra.Command {
	var (
		format      string
		output      string
		withAnalysis bool
	)

	cmd := &cobra.Command{
		Use:   "export <recording-id>",
		Short: "Export a recording for sharing",
		Long: `Export a recording to HTML, JSON, or Markdown format.

HTML reports include visual charts for phase timing, token breakdown,
and tool usage when --with-analysis is enabled.

Examples:
  pilot replay export TG-1234567890                    # Basic HTML
  pilot replay export TG-1234567890 --with-analysis    # Full report with charts
  pilot replay export TG-1234567890 --format json
  pilot replay export TG-1234567890 --format markdown
  pilot replay export TG-1234567890 --output report.html`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingID := args[0]
			recordingsPath := replay.DefaultRecordingsPath()

			recording, err := replay.LoadRecording(recordingsPath, recordingID)
			if err != nil {
				return fmt.Errorf("failed to load recording: %w", err)
			}

			events, err := replay.LoadStreamEvents(recording)
			if err != nil {
				return fmt.Errorf("failed to load events: %w", err)
			}

			// Generate analysis if requested
			var report *replay.AnalysisReport
			if withAnalysis || format == "markdown" {
				analyzer, err := replay.NewAnalyzer(recording)
				if err != nil {
					return fmt.Errorf("failed to create analyzer: %w", err)
				}
				report, err = analyzer.Analyze()
				if err != nil {
					return fmt.Errorf("analysis failed: %w", err)
				}
			}

			var content []byte
			var ext string

			switch format {
			case "html":
				ext = "html"
				if withAnalysis && report != nil {
					html, err := replay.ExportHTMLReport(recording, events, report)
					if err != nil {
						return fmt.Errorf("failed to export HTML report: %w", err)
					}
					content = []byte(html)
				} else {
					html, err := replay.ExportToHTML(recording, events)
					if err != nil {
						return fmt.Errorf("failed to export HTML: %w", err)
					}
					content = []byte(html)
				}
			case "json":
				ext = "json"
				content, err = replay.ExportToJSON(recording, events)
				if err != nil {
					return fmt.Errorf("failed to export JSON: %w", err)
				}
			case "markdown", "md":
				ext = "md"
				md, err := replay.ExportToMarkdown(recording, events, report)
				if err != nil {
					return fmt.Errorf("failed to export Markdown: %w", err)
				}
				content = []byte(md)
			default:
				return fmt.Errorf("unsupported format: %s (use html, json, or markdown)", format)
			}

			// Determine output path
			if output == "" {
				output = fmt.Sprintf("%s.%s", recordingID, ext)
			}

			if err := os.WriteFile(output, content, 0644); err != nil {
				return fmt.Errorf("failed to write file: %w", err)
			}

			fmt.Printf("✅ Exported to: %s\n", output)
			fmt.Printf("   Format: %s | Size: %d bytes\n", format, len(content))
			if withAnalysis {
				fmt.Println("   Analysis: ✓ included")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&format, "format", "html", "Export format (html, json, markdown)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path")
	cmd.Flags().BoolVar(&withAnalysis, "with-analysis", false, "Include detailed analysis in export")

	return cmd
}

func newReplayDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <recording-id>",
		Short: "Delete a recording",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			recordingID := args[0]
			recordingsPath := replay.DefaultRecordingsPath()

			// Verify recording exists
			_, err := replay.LoadRecording(recordingsPath, recordingID)
			if err != nil {
				return fmt.Errorf("recording not found: %w", err)
			}

			if !force {
				fmt.Printf("Delete recording %s? [y/N]: ", recordingID)
				var input string
				_, _ = fmt.Scanln(&input)
				if strings.ToLower(input) != "y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			if err := replay.DeleteRecording(recordingsPath, recordingID); err != nil {
				return fmt.Errorf("failed to delete: %w", err)
			}

			fmt.Printf("✅ Deleted recording: %s\n", recordingID)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Delete without confirmation")

	return cmd
}

// checkForUpdates checks for new versions in the background
func checkForUpdates() {
	if quietMode {
		return
	}

	upgrader, err := upgrade.NewUpgrader(version)
	if err != nil {
		return // Silently fail
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := upgrader.CheckVersion(ctx)
	if err != nil {
		return // Silently fail
	}

	if info.UpdateAvail {
		fmt.Println()
		fmt.Printf("✨ Update available: %s → %s\n", info.Current, info.Latest)
		fmt.Println("   Run 'pilot upgrade' to install")
		fmt.Println()
	}
}
// runDashboardMode runs the TUI dashboard with live task updates
func runDashboardMode(p *pilot.Pilot, cfg *config.Config) error {
	// Create TUI program
	model := dashboard.NewModel()
	program := tea.NewProgram(model, tea.WithAltScreen())

	// Set up event bridge: poll task states and send to dashboard
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register progress callback on Pilot's orchestrator
	p.OnProgress(func(taskID, phase string, progress int, message string) {
		// Convert current task states to dashboard display format
		tasks := convertTaskStatesToDisplay(p.GetTaskStates())
		program.Send(dashboard.UpdateTasks(tasks))

		// Also add progress message as log
		logMsg := fmt.Sprintf("[%s] %s: %s (%d%%)", taskID, phase, message, progress)
		program.Send(dashboard.AddLog(logMsg))
	})

	// Periodic refresh to catch any missed updates
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tasks := convertTaskStatesToDisplay(p.GetTaskStates())
				program.Send(dashboard.UpdateTasks(tasks))
			}
		}
	}()

	// Handle signals for graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		cancel()
		program.Send(tea.Quit())
	}()

	// Add startup log
	gatewayURL := fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	program.Send(dashboard.AddLog(fmt.Sprintf("🚀 Pilot v%s started - Gateway: %s", version, gatewayURL)))

	// Run TUI (blocks until quit)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("dashboard error: %w", err)
	}

	// Clean shutdown
	return p.Stop()
}

// convertTaskStatesToDisplay converts executor TaskStates to dashboard TaskDisplay format
func convertTaskStatesToDisplay(states []*executor.TaskState) []dashboard.TaskDisplay {
	displays := make([]dashboard.TaskDisplay, len(states))
	for i, state := range states {
		var status string
		switch state.Status {
		case executor.StatusRunning:
			status = "running"
		case executor.StatusCompleted:
			status = "completed"
		case executor.StatusFailed:
			status = "failed"
		default:
			status = "pending"
		}

		var duration string
		if state.StartedAt != nil {
			elapsed := time.Since(*state.StartedAt)
			if state.CompletedAt != nil {
				elapsed = state.CompletedAt.Sub(*state.StartedAt)
			}
			duration = elapsed.Round(time.Second).String()
		}

		displays[i] = dashboard.TaskDisplay{
			ID:       state.ID,
			Title:    state.Title,
			Status:   status,
			Phase:    state.Phase,
			Progress: state.Progress,
			Duration: duration,
		}
	}
	return displays
}

// getAlertsConfig extracts alerts configuration from the main config
func getAlertsConfig(cfg *config.Config) *alerts.AlertConfig {
	if cfg.Alerts == nil {
		return nil
	}

	alertsCfg := cfg.Alerts

	// Convert to alerts package types
	channels := make([]alerts.ChannelConfigInput, 0, len(alertsCfg.Channels))
	for _, ch := range alertsCfg.Channels {
		input := alerts.ChannelConfigInput{
			Name:       ch.Name,
			Type:       ch.Type,
			Enabled:    ch.Enabled,
			Severities: ch.Severities,
		}
		if ch.Slack != nil {
			input.Slack = &alerts.SlackConfigInput{Channel: ch.Slack.Channel}
		}
		if ch.Telegram != nil {
			input.Telegram = &alerts.TelegramConfigInput{ChatID: ch.Telegram.ChatID}
		}
		if ch.Email != nil {
			input.Email = &alerts.EmailConfigInput{To: ch.Email.To, Subject: ch.Email.Subject}
		}
		if ch.Webhook != nil {
			input.Webhook = &alerts.WebhookConfigInput{
				URL:     ch.Webhook.URL,
				Method:  ch.Webhook.Method,
				Headers: ch.Webhook.Headers,
				Secret:  ch.Webhook.Secret,
			}
		}
		if ch.PagerDuty != nil {
			input.PagerDuty = &alerts.PagerDutyConfigInput{
				RoutingKey: ch.PagerDuty.RoutingKey,
				ServiceID:  ch.PagerDuty.ServiceID,
			}
		}
		channels = append(channels, input)
	}

	rules := make([]alerts.RuleConfigInput, 0, len(alertsCfg.Rules))
	for _, r := range alertsCfg.Rules {
		rules = append(rules, alerts.RuleConfigInput{
			Name:        r.Name,
			Type:        r.Type,
			Enabled:     r.Enabled,
			Severity:    r.Severity,
			Channels:    r.Channels,
			Cooldown:    r.Cooldown,
			Description: r.Description,
			Condition: alerts.ConditionConfigInput{
				ProgressUnchangedFor: r.Condition.ProgressUnchangedFor,
				ConsecutiveFailures:  r.Condition.ConsecutiveFailures,
				DailySpendThreshold:  r.Condition.DailySpendThreshold,
				BudgetLimit:          r.Condition.BudgetLimit,
				UsageSpikePercent:    r.Condition.UsageSpikePercent,
				Pattern:              r.Condition.Pattern,
				FilePattern:          r.Condition.FilePattern,
				Paths:                r.Condition.Paths,
			},
		})
	}

	defaults := alerts.DefaultsConfigInput{
		Cooldown:           alertsCfg.Defaults.Cooldown,
		DefaultSeverity:    alertsCfg.Defaults.DefaultSeverity,
		SuppressDuplicates: alertsCfg.Defaults.SuppressDuplicates,
	}

	return alerts.FromConfigAlerts(alertsCfg.Enabled, channels, rules, defaults)
}
