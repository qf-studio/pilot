// Secondary CLI command constructors extracted from main.go (GH-1215)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/autopilot"
	"github.com/alekspetrov/pilot/internal/banner"
	"github.com/alekspetrov/pilot/internal/briefs"
	"github.com/alekspetrov/pilot/internal/budget"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/dashboard"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/pilot"
	"github.com/alekspetrov/pilot/internal/quality"
	"github.com/alekspetrov/pilot/internal/replay"
	"github.com/alekspetrov/pilot/internal/upgrade"
)

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
						"github":   cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled,
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
			if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
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
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize Pilot configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := config.DefaultConfigPath()

			// Check if config already exists
			if _, err := os.Stat(configPath); err == nil {
				if force {
					// Backup existing config
					backupPath := configPath + ".bak"
					if err := os.Rename(configPath, backupPath); err != nil {
						return fmt.Errorf("failed to backup config: %w", err)
					}
					fmt.Printf("   📦 Backed up existing config to %s\n\n", backupPath)
				} else {
					// Load and display existing config summary
					return showExistingConfigInfo(configPath)
				}
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

	cmd.Flags().BoolVar(&force, "force", false, "Reinitialize config (backs up existing to .bak)")

	return cmd
}

// showExistingConfigInfo displays a summary of the existing config and helpful options
func showExistingConfigInfo(configPath string) error {
	// Load existing config
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Use ~ for home directory in display
	displayPath := configPath
	if home, err := os.UserHomeDir(); err == nil {
		displayPath = strings.Replace(configPath, home, "~", 1)
	}

	fmt.Printf("⚠️  Config already exists: %s\n\n", displayPath)
	fmt.Println("   Current settings:")

	// Projects count
	switch projectCount := len(cfg.Projects); projectCount {
	case 0:
		fmt.Println("   • Projects: none configured")
	case 1:
		fmt.Println("   • Projects: 1 configured")
	default:
		fmt.Printf("   • Projects: %d configured\n", projectCount)
	}

	// Check enabled adapters
	if cfg.Adapters != nil {
		if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
			fmt.Println("   • Telegram: enabled")
		} else {
			fmt.Println("   • Telegram: disabled")
		}

		if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
			fmt.Println("   • GitHub: enabled")
		} else {
			fmt.Println("   • GitHub: disabled")
		}

		if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
			fmt.Println("   • Linear: enabled")
		}

		if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
			fmt.Println("   • Slack: enabled")
		}

		if cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled {
			fmt.Println("   • GitLab: enabled")
		}

		if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
			fmt.Println("   • Jira: enabled")
		}

		if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
			fmt.Println("   • Asana: enabled")
		}

		if cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled {
			fmt.Println("   • Azure DevOps: enabled")
		}
	}

	fmt.Println()
	fmt.Println("   Options:")
	fmt.Printf("   • Edit:   $EDITOR %s\n", displayPath)
	fmt.Println("   • Reset:  pilot init --force")
	fmt.Println("   • Start:  pilot start --help")

	return nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show Pilot version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Pilot %s\n", version)
			if buildTime != "unknown" {
				fmt.Printf("Built: %s\n", buildTime)
			}
		},
	}
}

func newTaskCmd() *cobra.Command {
	var projectPath string
	var dryRun bool
	var verbose bool
	var enableAlerts bool
	var enableBudget bool
	var teamID string     // GH-635: team project access scoping
	var teamMember string // GH-635: member email for access scoping

	cmd := &cobra.Command{
		Use:   "task [description]",
		Short: "Execute a task using Claude Code",
		Long: `Execute a task using Claude Code with Navigator integration.

PRs are always created to enable autopilot workflow.

Examples:
  pilot task "Add user authentication with JWT"
  pilot task "Fix the login bug in auth.go" --project /path/to/project
  pilot task "Refactor the API handlers" --dry-run
  pilot task "Add index.py with hello world" --verbose
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
			fmt.Printf("   Branch:    %s\n", branchName)
			fmt.Printf("   Create PR: ✓ always enabled\n")
			if hasNavigator {
				fmt.Printf("   Navigator: ✓ enabled\n")
			}
			fmt.Println()
			fmt.Println("📋 Task:")
			fmt.Printf("   %s\n", taskDesc)
			fmt.Println("───────────────────────────────────────")
			fmt.Println()

			// Build the task early so we can show prompt in dry-run
			// Always create branches and PRs - required for autopilot workflow
			task := &executor.Task{
				ID:          taskID,
				Title:       taskDesc,
				Description: taskDesc,
				ProjectPath: projectPath,
				Branch:      branchName,
				Verbose:     verbose,
				CreatePR:    true,
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
				prompt := runner.BuildPrompt(task, task.ProjectPath)
				fmt.Println(prompt)
				fmt.Println("─────────────────────────────────────")
				return nil
			}

			// Check budget before task execution if --budget flag is set
			if enableBudget {
				// Load config for budget
				configPath := cfgFile
				if configPath == "" {
					configPath = config.DefaultConfigPath()
				}

				budgetCfg, err := config.Load(configPath)
				if err != nil {
					return fmt.Errorf("failed to load config for budget: %w", err)
				}

				// Get budget config or use defaults
				budgetConfig := budgetCfg.Budget
				if budgetConfig == nil {
					budgetConfig = budget.DefaultConfig()
				}

				// Enable budget check even if not enabled in config (flag overrides)
				budgetConfig.Enabled = true

				// Open memory store for usage data
				store, err := memory.NewStore(budgetCfg.Memory.Path)
				if err != nil {
					return fmt.Errorf("failed to open memory store for budget: %w", err)
				}
				defer func() { _ = store.Close() }()

				// Create budget enforcer and check
				enforcer := budget.NewEnforcer(budgetConfig, store)
				result, err := enforcer.CheckBudget(ctx, "", "")
				if err != nil {
					return fmt.Errorf("budget check failed: %w", err)
				}

				if !result.Allowed {
					fmt.Println()
					fmt.Println("🚫 Task Blocked by Budget")
					fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
					fmt.Printf("   Reason: %s\n", result.Reason)
					fmt.Println()
					fmt.Println("   Run 'pilot budget status' for details")
					fmt.Println("   Run 'pilot budget reset' to reset daily counters")
					fmt.Println()
					return fmt.Errorf("task blocked by budget: %s", result.Reason)
				}

				// Show budget status
				fmt.Printf("   Budget:    ✓ $%.2f daily / $%.2f monthly remaining\n", result.DailyLeft, result.MonthlyLeft)
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

			// Load config for runner setup
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}
			cfg, cfgErr := config.Load(configPath)
			if cfgErr != nil {
				return fmt.Errorf("failed to load config: %w", cfgErr)
			}

			// Apply team flag overrides (GH-635)
			applyTeamOverrides(cfg, cmd, teamID, teamMember)

			// Create the executor runner with config (GH-956: enables worktree isolation, decomposer, model routing)
			runner, runnerErr := executor.NewRunnerWithConfig(cfg.Executor)
			if runnerErr != nil {
				return fmt.Errorf("failed to create executor runner: %w", runnerErr)
			}

			// GH-962: Clean up orphaned worktree directories from previous crashed executions
			if cfg.Executor != nil && cfg.Executor.UseWorktree {
				if err := executor.CleanupOrphanedWorktrees(ctx, projectPath); err != nil {
					// Log the cleanup but don't fail startup - this is best-effort cleanup
					fmt.Printf("   Worktree:  ✓ cleanup completed (%s)\n", err.Error())
				}
			}

			// Quality gates (GH-207)
			if cfg.Quality != nil && cfg.Quality.Enabled {
				runner.SetQualityCheckerFactory(func(taskID, projectPath string) executor.QualityChecker {
					return &qualityCheckerWrapper{
						executor: quality.NewExecutor(&quality.ExecutorConfig{
							Config:      cfg.Quality,
							ProjectPath: projectPath,
							TaskID:      taskID,
						}),
					}
				})
				fmt.Println("   Quality:   ✓ gates enabled")
			}

			// Decomposer status (GH-218) - wired via NewRunnerWithConfig
			if cfg.Executor != nil && cfg.Executor.Decompose != nil && cfg.Executor.Decompose.Enabled {
				fmt.Println("   Decompose: ✓ enabled")
			}

			// GH-539: Wire per-task budget limits if configured
			// GH-1019: Debug logging for budget state visibility
			if cfg.Budget != nil && cfg.Budget.Enabled {
				maxTokens := cfg.Budget.PerTask.MaxTokens
				maxDuration := cfg.Budget.PerTask.MaxDuration
				if maxTokens > 0 || maxDuration > 0 {
					limiter := budget.NewTaskLimiter(maxTokens, maxDuration)
					runner.SetTokenLimitCheck(func(_ string, deltaInput, deltaOutput int64) bool {
						totalDelta := deltaInput + deltaOutput
						if totalDelta > 0 {
							if !limiter.AddTokens(totalDelta) {
								return false
							}
						}
						if !limiter.CheckDuration() {
							return false
						}
						return true
					})
					fmt.Printf("   Per-task:  ✓ max %d tokens, %v duration\n", maxTokens, maxDuration)
				}
				logging.WithComponent("execute").Debug("budget enforcement enabled",
					slog.Int64("max_tokens", cfg.Budget.PerTask.MaxTokens),
					slog.Duration("max_duration", cfg.Budget.PerTask.MaxDuration),
				)
			} else {
				// GH-1019: Log why budget is disabled for debugging
				logging.WithComponent("execute").Debug("budget enforcement disabled",
					slog.Bool("config_nil", cfg.Budget == nil),
					slog.Bool("enabled", cfg.Budget != nil && cfg.Budget.Enabled),
				)
			}

			// Team project access checker (GH-635)
			if runTeamCleanup := wireProjectAccessChecker(runner, cfg); runTeamCleanup != nil {
				defer runTeamCleanup()
				fmt.Println("   Team:      ✓ project access scoping enabled")
			}

			// Create progress display (disabled in verbose mode - show raw JSON instead)
			progress := executor.NewProgressDisplay(task.ID, taskDesc, !verbose)

			// Suppress slog progress output when visual display is active
			runner.SuppressProgressLogs(!verbose)

			// Track Navigator mode detection
			var detectedNavMode string

			// Set up progress callback
			runner.OnProgress(func(taskID, phase string, pct int, message string) {
				// Detect Navigator mode from phase names
				switch phase {
				case "Navigator", "Loop Mode", "Task Mode":
					progress.SetNavigator(true, phase)
					detectedNavMode = phase
				case "Research", "Implement", "Verify":
					if detectedNavMode == "" {
						detectedNavMode = "nav-task"
					}
					progress.SetNavigator(true, detectedNavMode)
				}

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

			// Start progress display with Navigator check
			progress.StartWithNavigatorCheck(projectPath)

			// Execute the task
			result, err := runner.Execute(ctx, task)
			if err != nil {
				return fmt.Errorf("execution failed: %w", err)
			}

			// Build execution report
			report := &executor.ExecutionReport{
				TaskID:           result.TaskID,
				TaskTitle:        taskDesc,
				Success:          result.Success,
				Duration:         result.Duration,
				Branch:           task.Branch,
				CommitSHA:        result.CommitSHA,
				PRUrl:            result.PRUrl,
				HasNavigator:     detectedNavMode != "",
				NavMode:          detectedNavMode,
				TokensInput:      result.TokensInput,
				TokensOutput:     result.TokensOutput,
				EstimatedCostUSD: result.EstimatedCostUSD,
				ModelName:        result.ModelName,
				ErrorMessage:     result.Error,
			}

			// Finish progress display with comprehensive report
			progress.FinishWithReport(report)

			// Send alerts based on result
			if result.Success {
				if result.PRUrl == "" {
					fmt.Println("   ⚠️  PR not created (check gh auth status)")
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
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Stream Claude Code output")
	cmd.Flags().BoolVar(&enableAlerts, "alerts", false, "Enable alerts for task execution")
	cmd.Flags().BoolVar(&enableBudget, "budget", false, "Enable budget enforcement for this task")
	cmd.Flags().StringVar(&teamID, "team", "", "Team ID or name for project access scoping (overrides config)")
	cmd.Flags().StringVar(&teamMember, "team-member", "", "Member email for team access scoping (overrides config)")

	return cmd
}

// killExistingTelegramBot finds and kills any running pilot process with Telegram enabled
func killExistingTelegramBot() error {
	currentPID := os.Getpid()

	// Find processes matching "pilot start" or "pilot telegram" (for backward compatibility)
	patterns := []string{"pilot start", "pilot telegram"}
	for _, pattern := range patterns {
		out, err := exec.Command("pgrep", "-f", pattern).Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				continue // No process found
			}
			// pgrep not available, try ps-based approach
			return killExistingBotPS(currentPID, pattern)
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
	}

	return nil
}

// killExistingBotPS uses ps + grep as fallback
func killExistingBotPS(currentPID int, pattern string) error {
	out, err := exec.Command("sh", "-c", fmt.Sprintf("ps aux | grep '%s' | grep -v grep | awk '{print $2}'", pattern)).Output()
	if err != nil {
		return nil
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
	var repo string
	var teamID string     // GH-635: team project access scoping
	var teamMember string // GH-635: member email for access scoping

	cmd := &cobra.Command{
		Use:   "run <issue-number>",
		Short: "Run a GitHub issue as a Pilot task",
		Long: `Fetch a GitHub issue and execute it as a Pilot task.

PRs are always created to enable autopilot workflow.

Examples:
  pilot github run 8
  pilot github run 8 --repo owner/repo
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

			// Apply team flag overrides (GH-635)
			applyTeamOverrides(cfg, cmd, teamID, teamMember)

			// Check GitHub is configured
			if cfg.Adapters == nil || cfg.Adapters.GitHub == nil || !cfg.Adapters.GitHub.Enabled {
				return fmt.Errorf("GitHub adapter not enabled. Run 'pilot setup' or edit ~/.pilot/config.yaml")
			}

			token := cfg.Adapters.GitHub.Token
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("GitHub token not configured. Set GITHUB_TOKEN env or add to config")
			}

			// Determine repo
			if repo == "" {
				repo = cfg.Adapters.GitHub.Repo
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
			fmt.Printf("   Create PR: ✓ always enabled\n")
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

			// Always create branches and PRs - required for autopilot workflow
			task := &executor.Task{
				ID:          taskID,
				Title:       issue.Title,
				Description: taskDesc,
				ProjectPath: projectPath,
				Branch:      branchName,
				Verbose:     verbose,
				CreatePR:    true,
				Labels:      extractGitHubLabelNames(issue), // GH-727: flow labels for complexity classifier
			}

			// Dry run mode
			if dryRun {
				fmt.Println("🧪 DRY RUN - showing what would execute:")
				fmt.Println()
				runner := executor.NewRunner()
				prompt := runner.BuildPrompt(task, task.ProjectPath)
				fmt.Println("Prompt:")
				fmt.Println("─────────────────────────────────────")
				fmt.Println(prompt)
				fmt.Println("─────────────────────────────────────")
				return nil
			}

			// Add in-progress label
			fmt.Println("🏷️  Adding in-progress label...")
			if err := client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-in-progress"}); err != nil {
				logGitHubAPIError("AddLabels", owner, repoName, int(issueNum), err)
			}

			// Execute the task with config (GH-956: enables worktree isolation, decomposer, model routing)
			runner, runnerErr := executor.NewRunnerWithConfig(cfg.Executor)
			if runnerErr != nil {
				return fmt.Errorf("failed to create executor runner: %w", runnerErr)
			}

			// GH-962: Clean up orphaned worktree directories from previous crashed executions
			if cfg.Executor != nil && cfg.Executor.UseWorktree {
				if err := executor.CleanupOrphanedWorktrees(ctx, projectPath); err != nil {
					fmt.Printf("🧹 Worktree cleanup completed (%s)\n", err.Error())
				}
			}

			// Team project access checker (GH-635)
			if ghTeamCleanup := wireProjectAccessChecker(runner, cfg); ghTeamCleanup != nil {
				defer ghTeamCleanup()
				fmt.Printf("   Team:      ✓ project access scoping enabled\n")
			}

			fmt.Println()
			fmt.Println("⏳ Executing task with Claude Code...")
			fmt.Println()

			result, err := runner.Execute(ctx, task)
			if err != nil {
				// Add failed label
				if labelErr := client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-failed"}); labelErr != nil {
					logGitHubAPIError("AddLabels", owner, repoName, int(issueNum), labelErr)
				}
				if labelErr := client.RemoveLabel(ctx, owner, repoName, int(issueNum), "pilot-in-progress"); labelErr != nil {
					logGitHubAPIError("RemoveLabel", owner, repoName, int(issueNum), labelErr)
				}

				comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", err.Error())
				if _, commentErr := client.AddComment(ctx, owner, repoName, int(issueNum), comment); commentErr != nil {
					logGitHubAPIError("AddComment", owner, repoName, int(issueNum), commentErr)
				}

				return fmt.Errorf("task execution failed: %w", err)
			}

			// Remove in-progress label
			if err := client.RemoveLabel(ctx, owner, repoName, int(issueNum), "pilot-in-progress"); err != nil {
				logGitHubAPIError("RemoveLabel", owner, repoName, int(issueNum), err)
			}

			// Validate deliverables - execution succeeded but did it produce anything?
			if result.CommitSHA == "" && result.PRUrl == "" {
				// No commits and no PR - mark as failed
				if err := client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-failed"}); err != nil {
					logGitHubAPIError("AddLabels", owner, repoName, int(issueNum), err)
				}

				comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
					result.Duration, branchName)
				if _, err := client.AddComment(ctx, owner, repoName, int(issueNum), comment); err != nil {
					logGitHubAPIError("AddComment", owner, repoName, int(issueNum), err)
				}

				fmt.Println()
				fmt.Println("───────────────────────────────────────")
				fmt.Println("⚠️  Task completed but no changes made")
				fmt.Printf("   Duration: %s\n", result.Duration)
				return fmt.Errorf("execution completed but no commits or PR created")
			}

			// Success with deliverables - keep pilot-in-progress until PR merges
			// GH-1015: pilot-done is now added by autopilot controller after successful merge
			// This prevents false positives where PRs are closed without merging
			// Remove pilot-failed if present (may exist from previous failed attempt)
			_ = client.RemoveLabel(ctx, owner, repoName, int(issueNum), "pilot-failed")

			comment := buildExecutionComment(result, branchName)
			if _, err := client.AddComment(ctx, owner, repoName, int(issueNum), comment); err != nil {
				logGitHubAPIError("AddComment", owner, repoName, int(issueNum), err)
			}

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
	cmd.Flags().StringVar(&teamID, "team", "", "Team ID or name for project access scoping (overrides config)")
	cmd.Flags().StringVar(&teamMember, "team-member", "", "Member email for team access scoping (overrides config)")

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

						// Add Telegram sender if configured
						if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
							tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
							deliveryOpts = append(deliveryOpts, briefs.WithTelegramSender(&telegramBriefAdapter{client: tgClient}))
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
		newPatternsApplyCmd(),
		newPatternsIgnoreCmd(),
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

func newPatternsApplyCmd() *cobra.Command {
	var projectPath string

	cmd := &cobra.Command{
		Use:   "apply <pattern-id>",
		Short: "Apply a pattern to a project",
		Long:  `Link a pattern to a project so it will be considered during task execution.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patternID := args[0]

			// Resolve project path
			if projectPath == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
				projectPath = cwd
			}

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

			// Verify pattern exists
			pattern, err := store.GetCrossPattern(patternID)
			if err != nil {
				return fmt.Errorf("pattern not found: %w", err)
			}

			// Link pattern to project
			if err := store.LinkPatternToProject(patternID, projectPath); err != nil {
				return fmt.Errorf("failed to apply pattern: %w", err)
			}

			fmt.Printf("✅ Applied pattern to project:\n")
			fmt.Printf("   Pattern: %s\n", pattern.Title)
			fmt.Printf("   Type:    %s\n", pattern.Type)
			fmt.Printf("   Project: %s\n", shortenPath(projectPath))

			return nil
		},
	}

	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path (default: current directory)")

	return cmd
}

func newPatternsIgnoreCmd() *cobra.Command {
	var (
		projectPath string
		global      bool
	)

	cmd := &cobra.Command{
		Use:   "ignore <pattern-id>",
		Short: "Ignore a pattern",
		Long: `Mark a pattern as ignored. By default, ignores for the current project only.
Use --global to ignore across all projects (deletes the pattern).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patternID := args[0]

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

			// Verify pattern exists
			pattern, err := store.GetCrossPattern(patternID)
			if err != nil {
				return fmt.Errorf("pattern not found: %w", err)
			}

			if global {
				// Delete the pattern entirely
				if err := store.DeleteCrossPattern(patternID); err != nil {
					return fmt.Errorf("failed to delete pattern: %w", err)
				}
				fmt.Printf("✅ Deleted pattern globally:\n")
				fmt.Printf("   Pattern: %s\n", pattern.Title)
				fmt.Printf("   Type:    %s\n", pattern.Type)
			} else {
				// Record negative feedback for this project
				if projectPath == "" {
					cwd, err := os.Getwd()
					if err != nil {
						return fmt.Errorf("failed to get current directory: %w", err)
					}
					projectPath = cwd
				}

				feedback := &memory.PatternFeedback{
					PatternID:       patternID,
					ProjectPath:     projectPath,
					Outcome:         "ignored",
					ConfidenceDelta: -0.1, // Reduce confidence for ignored patterns
				}
				if err := store.RecordPatternFeedback(feedback); err != nil {
					return fmt.Errorf("failed to record ignore: %w", err)
				}
				fmt.Printf("✅ Ignored pattern for project:\n")
				fmt.Printf("   Pattern: %s\n", pattern.Title)
				fmt.Printf("   Project: %s\n", shortenPath(projectPath))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path (default: current directory)")
	cmd.Flags().BoolVar(&global, "global", false, "Ignore globally (deletes the pattern)")

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
		format       string
		output       string
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
	// Suppress slog output to prevent corrupting TUI display (GH-164)
	logging.Suppress()
	p.SuppressProgressLogs(true)

	// Create TUI program
	model := dashboard.NewModel(version)
	program := tea.NewProgram(model, tea.WithAltScreen())

	// Set up event bridge: poll task states and send to dashboard
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register progress callback on Pilot's orchestrator
	// GH-1220: Throttle progress callbacks to 200ms to prevent message flooding
	var lastDashboardUpdate time.Time
	var dashboardMu sync.Mutex
	p.OnProgress(func(taskID, phase string, progress int, message string) {
		dashboardMu.Lock()
		if time.Since(lastDashboardUpdate) < 200*time.Millisecond {
			dashboardMu.Unlock()
			return // Skip — periodic ticker will catch it
		}
		lastDashboardUpdate = time.Now()
		dashboardMu.Unlock()

		// Convert current task states to dashboard display format
		tasks := convertTaskStatesToDisplay(p.GetTaskStates())
		program.Send(dashboard.UpdateTasks(tasks)())

		// Also add progress message as log
		logMsg := fmt.Sprintf("[%s] %s: %s (%d%%)", taskID, phase, message, progress)
		program.Send(dashboard.AddLog(logMsg)())
	})

	// Register token usage callback for dashboard updates (GH-156 fix)
	p.OnToken("dashboard", func(taskID string, inputTokens, outputTokens int64) {
		program.Send(dashboard.UpdateTokens(int(inputTokens), int(outputTokens))())
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
				program.Send(dashboard.UpdateTasks(tasks)())
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

	// Add startup log AFTER program starts (GH-351: Send blocks if called before Run)
	gatewayURL := fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	go func() {
		time.Sleep(100 * time.Millisecond) // Wait for program.Run() to start
		program.Send(dashboard.AddLog(fmt.Sprintf("🚀 Pilot %s started - Gateway: %s", version, gatewayURL))())
	}()

	// Run TUI (blocks until quit)
	_, err := program.Run()
	if err != nil {
		return fmt.Errorf("dashboard error: %w", err)
	}

	// Clean shutdown
	return p.Stop()
}

// convertTaskStatesToDisplay converts executor TaskStates to dashboard TaskDisplay format.
// Maps all 5 states: done, running, queued, pending, failed for state-aware dashboard rendering.
// GH-1220: Added deduplication safety net to prevent duplicate tasks in rendering.
func convertTaskStatesToDisplay(states []*executor.TaskState) []dashboard.TaskDisplay {
	seen := make(map[string]bool)
	var displays []dashboard.TaskDisplay
	for _, state := range states {
		// GH-1220: Skip duplicate task IDs to prevent duplicate panels
		if seen[state.ID] {
			continue
		}
		seen[state.ID] = true

		var status string
		switch state.Status {
		case executor.StatusRunning:
			status = "running"
		case executor.StatusQueued:
			status = "queued"
		case executor.StatusCompleted:
			status = "done"
		case executor.StatusFailed:
			status = "failed"
		default:
			status = "pending"
		}

		var duration string
		if state.StartedAt != nil {
			elapsed := time.Since(*state.StartedAt)
			duration = elapsed.Round(time.Second).String()
		}

		displays = append(displays, dashboard.TaskDisplay{
			ID:       state.ID,
			Title:    state.Title,
			Status:   status,
			Phase:    state.Phase,
			Progress: state.Progress,
			Duration: duration,
			IssueURL: state.IssueURL,
			PRURL:    state.PRUrl,
		})
	}
	return displays
}

func newReleaseCmd() *cobra.Command {
	var (
		bump   string // force bump type: patch, minor, major
		draft  bool   // create as draft
		dryRun bool   // show what would be released
	)

	cmd := &cobra.Command{
		Use:   "release [version]",
		Short: "Create a release manually",
		Long: `Create a new release for the current repository.

If no version is specified, detects version bump from commits since last release.

Examples:
  pilot release                  # Auto-detect version from commits
  pilot release --bump=minor     # Force minor bump
  pilot release v1.2.3           # Specific version
  pilot release --draft          # Create as draft
  pilot release --dry-run        # Show what would be released`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			// Load config
			configPath := cfgFile
			if configPath == "" {
				configPath = config.DefaultConfigPath()
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			// Get GitHub token
			ghToken := ""
			if cfg.Adapters.GitHub != nil {
				ghToken = cfg.Adapters.GitHub.Token
			}
			if ghToken == "" {
				ghToken = os.Getenv("GITHUB_TOKEN")
			}
			if ghToken == "" {
				return fmt.Errorf("GitHub not configured - set github.token in config or GITHUB_TOKEN env var")
			}

			// Resolve owner/repo
			owner, repo, err := resolveOwnerRepo(cfg)
			if err != nil {
				return err
			}

			ghClient := github.NewClient(ghToken)

			// Create releaser with default config
			releaseCfg := autopilot.DefaultReleaseConfig()
			releaseCfg.Enabled = true
			releaser := autopilot.NewReleaser(ghClient, owner, repo, releaseCfg)

			// Get current version
			currentVersion, err := releaser.GetCurrentVersion(ctx)
			if err != nil {
				return fmt.Errorf("failed to get current version: %w", err)
			}

			var newVersion autopilot.SemVer
			var bumpType autopilot.BumpType

			// Determine version
			if len(args) > 0 {
				// Explicit version provided
				newVersion, err = autopilot.ParseSemVer(args[0])
				if err != nil {
					return fmt.Errorf("invalid version: %w", err)
				}
				bumpType = autopilot.BumpNone // Not applicable for explicit version
			} else if bump != "" {
				// Force bump type
				switch bump {
				case "patch":
					bumpType = autopilot.BumpPatch
				case "minor":
					bumpType = autopilot.BumpMinor
				case "major":
					bumpType = autopilot.BumpMajor
				default:
					return fmt.Errorf("invalid bump type: %s (use: patch, minor, major)", bump)
				}
				newVersion = currentVersion.Bump(bumpType)
			} else {
				// Auto-detect from commits
				latestRelease, _ := ghClient.GetLatestRelease(ctx, owner, repo)
				var baseRef string
				if latestRelease != nil {
					baseRef = latestRelease.TagName
				}

				var commits []*github.Commit
				if baseRef != "" {
					commits, err = ghClient.CompareCommits(ctx, owner, repo, baseRef, "HEAD")
					if err != nil {
						return fmt.Errorf("failed to get commits: %w", err)
					}
				}

				bumpType = autopilot.DetectBumpType(commits)
				if bumpType == autopilot.BumpNone {
					fmt.Println("No releasable commits found (no feat/fix commits)")
					return nil
				}
				newVersion = currentVersion.Bump(bumpType)
			}

			versionStr := newVersion.String(releaseCfg.TagPrefix)

			if dryRun {
				fmt.Printf("Would create release:\n")
				fmt.Printf("  Current version: %s\n", currentVersion.String(releaseCfg.TagPrefix))
				fmt.Printf("  New version: %s\n", versionStr)
				fmt.Printf("  Bump type: %s\n", bumpType)
				fmt.Printf("  Draft: %v\n", draft)
				return nil
			}

			fmt.Printf("Creating release %s...\n", versionStr)

			input := &github.ReleaseInput{
				TagName:         versionStr,
				TargetCommitish: "main",
				Name:            versionStr,
				Body:            fmt.Sprintf("Release %s", versionStr),
				Draft:           draft,
				GenerateNotes:   true, // Let GitHub generate release notes
			}

			release, err := ghClient.CreateRelease(ctx, owner, repo, input)
			if err != nil {
				return fmt.Errorf("failed to create release: %w", err)
			}

			fmt.Printf("✨ Release %s created!\n", versionStr)
			fmt.Printf("   URL: %s\n", release.HTMLURL)

			return nil
		},
	}

	cmd.Flags().StringVar(&bump, "bump", "", "Force bump type: patch, minor, major")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create release as draft")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be released without creating")

	return cmd
}

func newAutopilotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autopilot",
		Short: "Autopilot commands for PR lifecycle management",
		Long:  `Commands for viewing and managing autopilot PR tracking and automation.`,
	}

	cmd.AddCommand(
		newAutopilotStatusCmd(),
		newAutopilotEnableCmd(),
		newAutopilotDisableCmd(),
	)
	return cmd
}

func newAutopilotStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show tracked PRs and their current stage",
		Long: `Display autopilot status including:
- Tracked PRs and their lifecycle stage
- Time in current stage
- CI status for each PR
- Release configuration status

This command queries the running Pilot instance for autopilot state.
Note: Pilot must be running with --autopilot flag for this to work.`,
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

			// Check if autopilot is configured
			if cfg.Orchestrator == nil || cfg.Orchestrator.Autopilot == nil || !cfg.Orchestrator.Autopilot.Enabled {
				if jsonOutput {
					data := map[string]interface{}{
						"enabled": false,
						"error":   "autopilot not enabled in config",
					}
					out, _ := json.MarshalIndent(data, "", "  ")
					fmt.Println(string(out))
					return nil
				}
				fmt.Println("⚠️  Autopilot is not enabled in configuration")
				fmt.Println("   Start Pilot with --autopilot=<env> to enable autopilot mode")
				return nil
			}

			autopilotCfg := cfg.Orchestrator.Autopilot

			if jsonOutput {
				data := map[string]interface{}{
					"enabled":     true,
					"environment": autopilotCfg.Environment,
					"auto_merge":  autopilotCfg.AutoMerge,
					"auto_review": autopilotCfg.AutoReview,
					"release": map[string]interface{}{
						"enabled": autopilotCfg.Release != nil && autopilotCfg.Release.Enabled,
						"trigger": func() string {
							if autopilotCfg.Release != nil {
								return autopilotCfg.Release.Trigger
							}
							return ""
						}(),
						"requireCI": func() bool {
							if autopilotCfg.Release != nil {
								return autopilotCfg.Release.RequireCI
							}
							return false
						}(),
					},
					"ci_wait_timeout": autopilotCfg.CIWaitTimeout.String(),
					"max_failures":    autopilotCfg.MaxFailures,
					"note":            "For live PR tracking, check the dashboard or logs. This shows config only.",
				}
				out, _ := json.MarshalIndent(data, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			fmt.Println("🤖 Autopilot Status")
			fmt.Println("───────────────────────────────────────")
			fmt.Printf("Environment: %s\n", autopilotCfg.Environment)
			fmt.Println()

			fmt.Println("Configuration:")
			fmt.Printf("  Auto Merge:     %v\n", autopilotCfg.AutoMerge)
			fmt.Printf("  Auto Review:    %v\n", autopilotCfg.AutoReview)
			fmt.Printf("  Merge Method:   %s\n", autopilotCfg.MergeMethod)
			fmt.Printf("  CI Timeout:     %s\n", autopilotCfg.CIWaitTimeout)
			fmt.Printf("  Max Failures:   %d\n", autopilotCfg.MaxFailures)
			fmt.Println()

			fmt.Println("Release:")
			if autopilotCfg.Release != nil && autopilotCfg.Release.Enabled {
				fmt.Printf("  Enabled:        true\n")
				fmt.Printf("  Trigger:        %s\n", autopilotCfg.Release.Trigger)
				fmt.Printf("  Require CI:     %v\n", autopilotCfg.Release.RequireCI)
				fmt.Printf("  Tag Prefix:     %s\n", autopilotCfg.Release.TagPrefix)
			} else {
				fmt.Printf("  Enabled:        false\n")
			}
			fmt.Println()

			fmt.Println("ℹ️  For live PR tracking, check:")
			fmt.Println("   • Dashboard: pilot start --dashboard --autopilot=<env>")
			fmt.Println("   • Logs: pilot logs --follow")

			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newAutopilotEnableCmd() *cobra.Command {
	var (
		env        string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable autopilot in configuration",
		Long: `Enable autopilot mode in Pilot configuration.

This updates the config file to enable autopilot. You must restart Pilot
for changes to take effect.

Examples:
  pilot autopilot enable                 # Enable with default (dev) environment
  pilot autopilot enable --env=stage     # Enable with staging environment
  pilot autopilot enable --env=prod      # Enable with production environment`,
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

			// Validate environment
			switch autopilot.Environment(env) {
			case autopilot.EnvDev, autopilot.EnvStage, autopilot.EnvProd:
				// valid
			default:
				return fmt.Errorf("invalid environment: %s (use: dev, stage, prod)", env)
			}

			// Initialize orchestrator config if nil
			if cfg.Orchestrator == nil {
				cfg.Orchestrator = &config.OrchestratorConfig{}
			}

			// Initialize autopilot config if nil
			if cfg.Orchestrator.Autopilot == nil {
				cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
			}

			// Enable autopilot
			cfg.Orchestrator.Autopilot.Enabled = true
			cfg.Orchestrator.Autopilot.Environment = autopilot.Environment(env)

			// Save config
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			if jsonOutput {
				data := map[string]interface{}{
					"enabled":     true,
					"environment": env,
					"message":     "autopilot enabled",
				}
				out, _ := json.MarshalIndent(data, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			fmt.Printf("✓ Autopilot enabled (environment: %s)\n", env)
			fmt.Println("  Restart Pilot to apply: pilot start --autopilot=" + env)
			return nil
		},
	}

	cmd.Flags().StringVar(&env, "env", "dev", "Environment: dev, stage, prod")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newAutopilotDisableCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Disable autopilot in configuration",
		Long: `Disable autopilot mode in Pilot configuration.

This updates the config file to disable autopilot. You must restart Pilot
for changes to take effect.

Examples:
  pilot autopilot disable            # Disable autopilot
  pilot autopilot disable --json     # Output as JSON`,
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

			// Check if already disabled
			if cfg.Orchestrator == nil || cfg.Orchestrator.Autopilot == nil || !cfg.Orchestrator.Autopilot.Enabled {
				if jsonOutput {
					data := map[string]interface{}{
						"enabled": false,
						"message": "autopilot already disabled",
					}
					out, _ := json.MarshalIndent(data, "", "  ")
					fmt.Println(string(out))
					return nil
				}
				fmt.Println("Autopilot is already disabled")
				return nil
			}

			// Disable autopilot
			cfg.Orchestrator.Autopilot.Enabled = false

			// Save config
			if err := config.Save(cfg, configPath); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			if jsonOutput {
				data := map[string]interface{}{
					"enabled": false,
					"message": "autopilot disabled",
				}
				out, _ := json.MarshalIndent(data, "", "  ")
				fmt.Println(string(out))
				return nil
			}

			fmt.Println("✓ Autopilot disabled")
			fmt.Println("  Restart Pilot to apply changes")
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}
