// Dashboard progress test - GH-151
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
	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/approval"
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

var (
	version   = "0.3.0"
	buildTime = "unknown"
	cfgFile   string
)

// telegramBriefAdapter wraps telegram.Client to satisfy briefs.TelegramSender interface
type telegramBriefAdapter struct {
	client *telegram.Client
}

func (a *telegramBriefAdapter) SendBriefMessage(ctx context.Context, chatID, text, parseMode string) (*briefs.TelegramMessageResponse, error) {
	resp, err := a.client.SendMessage(ctx, chatID, text, parseMode)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Result == nil {
		return nil, nil
	}
	return &briefs.TelegramMessageResponse{MessageID: resp.Result.MessageID}, nil
}

// telegramApprovalAdapter wraps telegram.Client to satisfy approval.TelegramClient interface
type telegramApprovalAdapter struct {
	client *telegram.Client
}

func (a *telegramApprovalAdapter) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]approval.InlineKeyboardButton) (*approval.MessageResponse, error) {
	resp, err := a.client.SendMessageWithKeyboard(ctx, chatID, text, parseMode, convertKeyboardToTelegram(keyboard))
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return &approval.MessageResponse{
		Result: &approval.MessageResult{MessageID: resp.Result.MessageID},
	}, nil
}

func (a *telegramApprovalAdapter) EditMessage(ctx context.Context, chatID string, messageID int64, text, parseMode string) error {
	return a.client.EditMessage(ctx, chatID, messageID, text, parseMode)
}

func (a *telegramApprovalAdapter) AnswerCallback(ctx context.Context, callbackID, text string) error {
	return a.client.AnswerCallback(ctx, callbackID, text)
}

func convertKeyboardToTelegram(keyboard [][]approval.InlineKeyboardButton) [][]telegram.InlineKeyboardButton {
	result := make([][]telegram.InlineKeyboardButton, len(keyboard))
	for i, row := range keyboard {
		result[i] = make([]telegram.InlineKeyboardButton, len(row))
		for j, btn := range row {
			result[i][j] = telegram.InlineKeyboardButton{
				Text:         btn.Text,
				CallbackData: btn.CallbackData,
			}
		}
	}
	return result
}

// slackApprovalClientAdapter wraps slack.SlackClientAdapter to satisfy approval.SlackClient interface
type slackApprovalClientAdapter struct {
	adapter *slack.SlackClientAdapter
}

func (a *slackApprovalClientAdapter) PostInteractiveMessage(ctx context.Context, msg *approval.SlackInteractiveMessage) (*approval.SlackPostMessageResponse, error) {
	resp, err := a.adapter.PostInteractiveMessage(ctx, &slack.SlackApprovalMessage{
		Channel: msg.Channel,
		Text:    msg.Text,
		Blocks:  msg.Blocks,
	})
	if err != nil {
		return nil, err
	}
	return &approval.SlackPostMessageResponse{
		OK:      resp.OK,
		TS:      resp.TS,
		Channel: resp.Channel,
		Error:   resp.Error,
	}, nil
}

func (a *slackApprovalClientAdapter) UpdateInteractiveMessage(ctx context.Context, channel, ts string, blocks []interface{}, text string) error {
	return a.adapter.UpdateInteractiveMessage(ctx, channel, ts, blocks, text)
}

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
		newReleaseCmd(),
		newAllowCmd(),
		newProjectCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newStartCmd() *cobra.Command {
	var (
		daemon        bool
		dashboardMode bool
		projectPath   string
		replace       bool
		// Input adapter flags (override config) - use bool with "changed" check
		enableTelegram bool
		enableGithub   bool
		enableLinear   bool
		// Mode flags
		noGateway    bool   // Lightweight mode: polling only, no HTTP gateway
		sequential   bool   // Sequential execution mode (one issue at a time)
		autopilotEnv string // Autopilot environment: dev, stage, prod
		autoRelease  bool   // Enable auto-release after PR merge
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start Pilot with config-driven inputs",
		Long: `Start Pilot with inputs enabled based on config or flags.

By default, reads enabled adapters from ~/.pilot/config.yaml.
Use flags to override config values.

Examples:
  pilot start                          # Config-driven
  pilot start --telegram               # Enable Telegram polling
  pilot start --github                 # Enable GitHub polling
  pilot start --telegram --github      # Enable both
  pilot start --dashboard              # With TUI dashboard
  pilot start --no-gateway             # Polling only (no HTTP server)`,
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

			// Apply flag overrides to config
			applyInputOverrides(cfg, cmd, enableTelegram, enableGithub, enableLinear)

			// Resolve project path: flag > config default > cwd
			if projectPath == "" {
				if defaultProj := cfg.GetDefaultProject(); defaultProj != nil {
					projectPath = defaultProj.Path
				}
			}
			if projectPath == "" {
				cwd, _ := os.Getwd()
				projectPath = cwd
			}
			if strings.HasPrefix(projectPath, "~") {
				home, _ := os.UserHomeDir()
				projectPath = strings.Replace(projectPath, "~", home, 1)
			}

			// Determine mode based on what's enabled
			hasTelegram := cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled
			hasGithubPolling := cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled
			hasLinear := cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled
			hasJira := cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled

			// Apply execution mode override from CLI flags
			if sequential {
				if cfg.Orchestrator.Execution == nil {
					cfg.Orchestrator.Execution = config.DefaultExecutionConfig()
				}
				cfg.Orchestrator.Execution.Mode = "sequential"
			}

			// Override autopilot config if flag provided
			if autopilotEnv != "" {
				if cfg.Orchestrator.Autopilot == nil {
					cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
				}
				cfg.Orchestrator.Autopilot.Enabled = true
				cfg.Orchestrator.Autopilot.Environment = autopilot.Environment(autopilotEnv)

				// Validate environment
				switch cfg.Orchestrator.Autopilot.Environment {
				case autopilot.EnvDev, autopilot.EnvStage, autopilot.EnvProd:
					// valid
				default:
					return fmt.Errorf("invalid autopilot environment: %s (use: dev, stage, prod)", autopilotEnv)
				}
			}

			// Enable auto-release if flag provided
			if autoRelease {
				if cfg.Orchestrator.Autopilot == nil {
					cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
					cfg.Orchestrator.Autopilot.Enabled = true
				}
				if cfg.Orchestrator.Autopilot.Release == nil {
					cfg.Orchestrator.Autopilot.Release = autopilot.DefaultReleaseConfig()
				}
				cfg.Orchestrator.Autopilot.Release.Enabled = true
			}

			// Lightweight mode: polling only, no gateway
			if noGateway || (!hasLinear && !hasJira && (hasTelegram || hasGithubPolling)) {
				return runPollingMode(cfg, projectPath, replace, dashboardMode)
			}

			// Full daemon mode with gateway
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			// Suppress logging in dashboard mode BEFORE initialization (GH-351)
			if dashboardMode {
				logging.Suppress()
			}

			// Build Pilot options for gateway mode (GH-349)
			var pilotOpts []pilot.Option

			// GH-392: Create shared infrastructure for polling adapters in gateway mode
			// This allows GitHub polling to work alongside Linear/Jira webhooks
			telegramFlagSet := cmd.Flags().Changed("telegram")
			githubFlagSet := cmd.Flags().Changed("github")
			needsPollingInfra := (telegramFlagSet && hasTelegram && cfg.Adapters.Telegram.Polling) ||
				(githubFlagSet && hasGithubPolling && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
					cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled)

			// Shared infrastructure for polling adapters
			var gwRunner *executor.Runner
			var gwStore *memory.Store
			var gwDispatcher *executor.Dispatcher
			var gwMonitor *executor.Monitor
			var gwProgram *tea.Program
			var gwAutopilotController *autopilot.Controller
			var gwAlertsEngine *alerts.Engine

			if needsPollingInfra {
				// Create shared runner
				gwRunner = executor.NewRunner()

				// Set up quality gates on runner if configured
				if cfg.Quality != nil && cfg.Quality.Enabled {
					gwRunner.SetQualityCheckerFactory(func(taskID, taskProjectPath string) executor.QualityChecker {
						return &qualityCheckerWrapper{
							executor: quality.NewExecutor(&quality.ExecutorConfig{
								Config:      cfg.Quality,
								ProjectPath: taskProjectPath,
								TaskID:      taskID,
							}),
						}
					})
				}

				// Set up task decomposition if configured
				if cfg.Executor != nil && cfg.Executor.Decompose != nil && cfg.Executor.Decompose.Enabled {
					gwRunner.SetDecomposer(executor.NewTaskDecomposer(cfg.Executor.Decompose))
				}

				// Set up model routing if configured
				if cfg.Executor != nil {
					gwRunner.SetModelRouter(executor.NewModelRouter(cfg.Executor.ModelRouting, cfg.Executor.Timeout))
				}

				// Create memory store for dispatcher
				var storeErr error
				gwStore, storeErr = memory.NewStore(cfg.Memory.Path)
				if storeErr != nil {
					logging.WithComponent("start").Warn("Failed to open memory store for gateway polling", slog.Any("error", storeErr))
				}

				// Create dispatcher if store available
				if gwStore != nil {
					gwDispatcher = executor.NewDispatcher(gwStore, gwRunner, nil)
					if dispErr := gwDispatcher.Start(); dispErr != nil {
						logging.WithComponent("start").Warn("Failed to start dispatcher for gateway polling", slog.Any("error", dispErr))
						gwDispatcher = nil
					}
				}

				// Create approval manager for autopilot
				approvalMgr := approval.NewManager(cfg.Approval)

				// Register Telegram approval handler if enabled
				if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
					tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
					tgApprovalHandler := approval.NewTelegramHandler(&telegramApprovalAdapter{client: tgClient}, cfg.Adapters.Telegram.ChatID)
					approvalMgr.RegisterHandler(tgApprovalHandler)
				}

				// Register Slack approval handler if enabled
				if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
					if cfg.Adapters.Slack.Approval != nil && cfg.Adapters.Slack.Approval.Enabled {
						slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
						slackAdapter := slack.NewSlackClientAdapter(slackClient)
						slackChannel := cfg.Adapters.Slack.Approval.Channel
						if slackChannel == "" {
							slackChannel = cfg.Adapters.Slack.Channel
						}
						slackApprovalHandler := approval.NewSlackHandler(&slackApprovalClientAdapter{adapter: slackAdapter}, slackChannel)
						approvalMgr.RegisterHandler(slackApprovalHandler)
					}
				}

				// Create autopilot controller if enabled
				if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.Enabled {
					ghToken := ""
					if cfg.Adapters.GitHub != nil {
						ghToken = cfg.Adapters.GitHub.Token
						if ghToken == "" {
							ghToken = os.Getenv("GITHUB_TOKEN")
						}
					}
					if ghToken != "" && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
						parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
						if len(parts) == 2 {
							ghClient := github.NewClient(ghToken)
							gwAutopilotController = autopilot.NewController(
								cfg.Orchestrator.Autopilot,
								ghClient,
								approvalMgr,
								parts[0],
								parts[1],
							)
						}
					}
				}

				// Create alerts engine if configured
				alertsCfg := getAlertsConfig(cfg)
				if alertsCfg != nil && alertsCfg.Enabled {
					alertsDispatcher := alerts.NewDispatcher(alertsCfg)

					// Register Slack channel if configured
					if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
						slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
						for _, ch := range alertsCfg.Channels {
							if ch.Type == "slack" && ch.Slack != nil {
								slackChannel := alerts.NewSlackChannel(ch.Name, slackClient, ch.Slack.Channel)
								alertsDispatcher.RegisterChannel(slackChannel)
							}
						}
					}

					// Register Telegram channel if configured
					if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
						telegramClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
						for _, ch := range alertsCfg.Channels {
							if ch.Type == "telegram" && ch.Telegram != nil {
								telegramChannel := alerts.NewTelegramChannel(ch.Name, telegramClient, ch.Telegram.ChatID)
								alertsDispatcher.RegisterChannel(telegramChannel)
							}
						}
					}

					ctx := context.Background()
					gwAlertsEngine = alerts.NewEngine(alertsCfg, alerts.WithDispatcher(alertsDispatcher))
					if alertErr := gwAlertsEngine.Start(ctx); alertErr != nil {
						logging.WithComponent("start").Warn("failed to start alerts engine for gateway polling", slog.Any("error", alertErr))
						gwAlertsEngine = nil
					}
				}

				// Create monitor and TUI program for dashboard mode
				if dashboardMode {
					gwRunner.SuppressProgressLogs(true)
					gwMonitor = executor.NewMonitor()
					model := dashboard.NewModelWithOptions(version, gwStore, gwAutopilotController, nil)
					gwProgram = tea.NewProgram(model,
						tea.WithAltScreen(),
						tea.WithInput(os.Stdin),
						tea.WithOutput(os.Stdout),
					)

					// Wire runner progress updates to dashboard
					gwRunner.AddProgressCallback("dashboard", func(taskID, phase string, progress int, message string) {
						gwMonitor.UpdateProgress(taskID, phase, progress, message)
						tasks := convertTaskStatesToDisplay(gwMonitor.GetAll())
						gwProgram.Send(dashboard.UpdateTasks(tasks)())
						logMsg := fmt.Sprintf("[%s] %s: %s (%d%%)", taskID, phase, message, progress)
						gwProgram.Send(dashboard.AddLog(logMsg)())
					})

					// Wire token usage updates to dashboard
					gwRunner.AddTokenCallback("dashboard", func(taskID string, inputTokens, outputTokens int64) {
						gwProgram.Send(dashboard.UpdateTokens(int(inputTokens), int(outputTokens))())
					})
				}
			}

			// Enable Telegram polling in gateway mode only if --telegram flag was explicitly passed (GH-351)
			if telegramFlagSet && hasTelegram && cfg.Adapters.Telegram.Polling {
				pilotOpts = append(pilotOpts, pilot.WithTelegramHandler(gwRunner, projectPath))
				logging.WithComponent("start").Info("Telegram polling enabled in gateway mode")
			}

			// Enable GitHub polling in gateway mode only if --github flag was explicitly passed (GH-350, GH-351)
			// GH-392: Now actually processes issues instead of no-op
			if githubFlagSet && hasGithubPolling && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {

				token := cfg.Adapters.GitHub.Token
				if token == "" {
					token = os.Getenv("GITHUB_TOKEN")
				}

				if token != "" && cfg.Adapters.GitHub.Repo != "" {
					client := github.NewClient(token)
					label := cfg.Adapters.GitHub.Polling.Label
					if label == "" {
						label = cfg.Adapters.GitHub.PilotLabel
					}
					interval := cfg.Adapters.GitHub.Polling.Interval
					if interval == 0 {
						interval = 30 * time.Second
					}

					// Determine execution mode from config
					execMode := github.ExecutionModeSequential
					waitForMerge := true
					pollInterval := 30 * time.Second
					prTimeout := 1 * time.Hour

					if cfg.Orchestrator != nil && cfg.Orchestrator.Execution != nil {
						execCfg := cfg.Orchestrator.Execution
						if execCfg.Mode == "parallel" {
							execMode = github.ExecutionModeParallel
						}
						waitForMerge = execCfg.WaitForMerge
						if execCfg.PollInterval > 0 {
							pollInterval = execCfg.PollInterval
						}
						if execCfg.PRTimeout > 0 {
							prTimeout = execCfg.PRTimeout
						}
					}

					var pollerOpts []github.PollerOption
					pollerOpts = append(pollerOpts, github.WithExecutionMode(execMode))

					// Wire autopilot OnPRCreated callback if controller initialized
					if gwAutopilotController != nil {
						pollerOpts = append(pollerOpts, github.WithOnPRCreated(gwAutopilotController.OnPRCreated))
					}

					// Create rate limit retry scheduler
					repoParts := strings.Split(cfg.Adapters.GitHub.Repo, "/")
					if len(repoParts) != 2 {
						return fmt.Errorf("invalid repo format: %s", cfg.Adapters.GitHub.Repo)
					}
					repoOwner, repoName := repoParts[0], repoParts[1]

					rateLimitScheduler := executor.NewScheduler(executor.DefaultSchedulerConfig(), nil)
					rateLimitScheduler.SetRetryCallback(func(retryCtx context.Context, pendingTask *executor.PendingTask) error {
						var issueNum int
						if _, err := fmt.Sscanf(pendingTask.Task.ID, "GH-%d", &issueNum); err != nil {
							return fmt.Errorf("invalid task ID format: %s", pendingTask.Task.ID)
						}

						issue, err := client.GetIssue(retryCtx, repoOwner, repoName, issueNum)
						if err != nil {
							return fmt.Errorf("failed to fetch issue for retry: %w", err)
						}

						logging.WithComponent("scheduler").Info("Retrying rate-limited issue",
							slog.Int("issue", issueNum),
							slog.Int("attempt", pendingTask.Attempts),
						)

						if execMode == github.ExecutionModeSequential {
							_, err = handleGitHubIssueWithResult(retryCtx, cfg, client, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine)
						} else {
							err = handleGitHubIssueWithMonitor(retryCtx, cfg, client, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine)
						}
						return err
					})
					rateLimitScheduler.SetExpiredCallback(func(expiredCtx context.Context, pendingTask *executor.PendingTask) {
						logging.WithComponent("scheduler").Error("Task exceeded max retry attempts",
							slog.String("task_id", pendingTask.Task.ID),
							slog.Int("attempts", pendingTask.Attempts),
						)
					})
					ctx := context.Background()
					if schErr := rateLimitScheduler.Start(ctx); schErr != nil {
						logging.WithComponent("start").Warn("Failed to start rate limit scheduler", slog.Any("error", schErr))
					}

					// GH-392: Configure with actual issue processing callbacks (same as polling mode)
					if execMode == github.ExecutionModeSequential {
						pollerOpts = append(pollerOpts,
							github.WithSequentialConfig(waitForMerge, pollInterval, prTimeout),
							github.WithScheduler(rateLimitScheduler),
							github.WithOnIssueWithResult(func(issueCtx context.Context, issue *github.Issue) (*github.IssueResult, error) {
								return handleGitHubIssueWithResult(issueCtx, cfg, client, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine)
							}),
						)
					} else {
						pollerOpts = append(pollerOpts,
							github.WithScheduler(rateLimitScheduler),
							github.WithOnIssue(func(issueCtx context.Context, issue *github.Issue) error {
								return handleGitHubIssueWithMonitor(issueCtx, cfg, client, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine)
							}),
						)
					}

					ghPoller, err := github.NewPoller(client, cfg.Adapters.GitHub.Repo, label, interval, pollerOpts...)
					if err != nil {
						logging.WithComponent("start").Warn("GitHub polling disabled in gateway mode", slog.Any("error", err))
					} else {
						pilotOpts = append(pilotOpts, pilot.WithGitHubPoller(ghPoller))
						logging.WithComponent("start").Info("GitHub polling enabled in gateway mode",
							slog.String("repo", cfg.Adapters.GitHub.Repo),
							slog.Duration("interval", interval),
							slog.String("mode", string(execMode)),
						)

						// Start autopilot processing loop if controller initialized
						if gwAutopilotController != nil {
							ctx := context.Background()
							// Scan for existing PRs created by Pilot
							if scanErr := gwAutopilotController.ScanExistingPRs(ctx); scanErr != nil {
								logging.WithComponent("autopilot").Warn("failed to scan existing PRs",
									slog.Any("error", scanErr),
								)
							}

							logging.WithComponent("start").Info("autopilot enabled in gateway mode",
								slog.String("environment", string(cfg.Orchestrator.Autopilot.Environment)),
							)
							go func() {
								if runErr := gwAutopilotController.Run(ctx); runErr != nil && runErr != context.Canceled {
									logging.WithComponent("autopilot").Error("autopilot controller stopped",
										slog.Any("error", runErr),
									)
								}
							}()
						}
					}
				}
			}

			// Create and start Pilot
			p, err := pilot.New(cfg, pilotOpts...)
			if err != nil {
				return fmt.Errorf("failed to create Pilot: %w", err)
			}

			// Set up quality gates if configured (GH-207) - for orchestrator/webhook mode
			if cfg.Quality != nil && cfg.Quality.Enabled {
				p.SetQualityCheckerFactory(func(taskID, taskProjectPath string) executor.QualityChecker {
					return &qualityCheckerWrapper{
						executor: quality.NewExecutor(&quality.ExecutorConfig{
							Config:      cfg.Quality,
							ProjectPath: taskProjectPath,
							TaskID:      taskID,
						}),
					}
				})
				logging.WithComponent("start").Info("quality gates enabled for webhook mode")
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

			// Show Telegram status in gateway mode (GH-349)
			if hasTelegram && cfg.Adapters.Telegram.Polling {
				fmt.Println("📱 Telegram polling active")
			}

			// Show GitHub status in gateway mode (GH-350)
			if hasGithubPolling && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {
				fmt.Printf("🐙 GitHub polling: %s\n", cfg.Adapters.GitHub.Repo)
			}

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
	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path (default: config default or cwd)")
	cmd.Flags().BoolVar(&replace, "replace", false, "Kill existing bot instance before starting")
	cmd.Flags().BoolVar(&noGateway, "no-gateway", false, "Run polling adapters only (no HTTP gateway)")
	cmd.Flags().BoolVar(&sequential, "sequential", false, "Sequential execution: wait for PR merge before next issue")
	cmd.Flags().StringVar(&autopilotEnv, "autopilot", "",
		"Enable autopilot mode: dev (auto-merge), stage (CI gate), prod (approval gate)")
	cmd.Flags().BoolVar(&autoRelease, "auto-release", false,
		"Enable automatic release creation after PR merge")

	// Input adapter flags - standard bool flags
	cmd.Flags().BoolVar(&enableTelegram, "telegram", false, "Enable Telegram polling (overrides config)")
	cmd.Flags().BoolVar(&enableGithub, "github", false, "Enable GitHub polling (overrides config)")
	cmd.Flags().BoolVar(&enableLinear, "linear", false, "Enable Linear webhooks (overrides config)")

	return cmd
}

// applyInputOverrides applies CLI flag overrides to config
// Uses cmd.Flags().Changed() to only apply flags that were explicitly set
func applyInputOverrides(cfg *config.Config, cmd *cobra.Command, telegramFlag, githubFlag, linearFlag bool) {
	if cmd.Flags().Changed("telegram") {
		if cfg.Adapters.Telegram == nil {
			cfg.Adapters.Telegram = telegram.DefaultConfig()
		}
		cfg.Adapters.Telegram.Enabled = telegramFlag
		cfg.Adapters.Telegram.Polling = telegramFlag
	}
	if cmd.Flags().Changed("github") {
		if cfg.Adapters.GitHub == nil {
			cfg.Adapters.GitHub = github.DefaultConfig()
		}
		cfg.Adapters.GitHub.Enabled = githubFlag
		if cfg.Adapters.GitHub.Polling == nil {
			cfg.Adapters.GitHub.Polling = &github.PollingConfig{}
		}
		cfg.Adapters.GitHub.Polling.Enabled = githubFlag
	}
	if cmd.Flags().Changed("linear") {
		if cfg.Adapters.Linear == nil {
			cfg.Adapters.Linear = linear.DefaultConfig()
		}
		cfg.Adapters.Linear.Enabled = linearFlag
	}
}

// runPollingMode runs lightweight polling-only mode (no HTTP gateway)
func runPollingMode(cfg *config.Config, projectPath string, replace, dashboardMode bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Check Telegram config if enabled
	hasTelegram := cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled
	if hasTelegram && cfg.Adapters.Telegram.BotToken == "" {
		return fmt.Errorf("telegram enabled but bot_token not configured")
	}

	// Suppress logging BEFORE creating runner in dashboard mode (GH-190)
	// Runner caches its logger at creation time, so suppression must happen first
	if dashboardMode {
		logging.Suppress()
	}

	// Create runner
	runner := executor.NewRunner()

	// Set up quality gates if configured (GH-207)
	if cfg.Quality != nil && cfg.Quality.Enabled {
		runner.SetQualityCheckerFactory(func(taskID, taskProjectPath string) executor.QualityChecker {
			return &qualityCheckerWrapper{
				executor: quality.NewExecutor(&quality.ExecutorConfig{
					Config:      cfg.Quality,
					ProjectPath: taskProjectPath,
					TaskID:      taskID,
				}),
			}
		})
		logging.WithComponent("start").Info("quality gates enabled for polling mode")
	}

	// Set up task decomposition if configured (GH-218)
	if cfg.Executor != nil && cfg.Executor.Decompose != nil && cfg.Executor.Decompose.Enabled {
		runner.SetDecomposer(executor.NewTaskDecomposer(cfg.Executor.Decompose))
		logging.WithComponent("start").Info("task decomposition enabled for polling mode")
	}

	// Set up model routing if configured (GH-215)
	if cfg.Executor != nil {
		runner.SetModelRouter(executor.NewModelRouter(cfg.Executor.ModelRouting, cfg.Executor.Timeout))
	}

	// Create approval manager
	approvalMgr := approval.NewManager(cfg.Approval)

	// Register Telegram approval handler if enabled
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
		tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
		tgApprovalHandler := approval.NewTelegramHandler(&telegramApprovalAdapter{client: tgClient}, cfg.Adapters.Telegram.ChatID)
		approvalMgr.RegisterHandler(tgApprovalHandler)
		logging.WithComponent("start").Info("registered Telegram approval handler")
	}

	// Register Slack approval handler if enabled
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
		if cfg.Adapters.Slack.Approval != nil && cfg.Adapters.Slack.Approval.Enabled {
			slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
			slackAdapter := slack.NewSlackClientAdapter(slackClient)
			slackChannel := cfg.Adapters.Slack.Approval.Channel
			if slackChannel == "" {
				slackChannel = cfg.Adapters.Slack.Channel
			}
			slackApprovalHandler := approval.NewSlackHandler(&slackApprovalClientAdapter{adapter: slackAdapter}, slackChannel)
			approvalMgr.RegisterHandler(slackApprovalHandler)
			logging.WithComponent("start").Info("registered Slack approval handler",
				slog.String("channel", slackChannel))
		}
	}

	// Create autopilot controller if enabled
	var autopilotController *autopilot.Controller
	if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.Enabled {
		// Need GitHub client for autopilot
		ghToken := ""
		if cfg.Adapters.GitHub != nil {
			ghToken = cfg.Adapters.GitHub.Token
			if ghToken == "" {
				ghToken = os.Getenv("GITHUB_TOKEN")
			}
		}
		if ghToken != "" && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
			parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
			if len(parts) == 2 {
				ghClient := github.NewClient(ghToken)
				autopilotController = autopilot.NewController(
					cfg.Orchestrator.Autopilot,
					ghClient,
					approvalMgr,
					parts[0],
					parts[1],
				)
			}
		}
	}

	// Initialize memory store early for dashboard persistence (GH-367)
	store, err := memory.NewStore(cfg.Memory.Path)
	if err != nil {
		logging.WithComponent("start").Warn("Failed to open memory store", slog.Any("error", err))
		store = nil
	} else {
		defer func() {
			if store != nil {
				_ = store.Close()
			}
		}()
	}

	// Create monitor and TUI program for dashboard mode
	var monitor *executor.Monitor
	var program *tea.Program
	var upgradeRequestCh chan struct{} // Channel for hot upgrade requests (GH-369)
	if dashboardMode {
		runner.SuppressProgressLogs(true)

		monitor = executor.NewMonitor()
		upgradeRequestCh = make(chan struct{}, 1)
		model := dashboard.NewModelWithOptions(version, store, autopilotController, upgradeRequestCh)
		program = tea.NewProgram(model,
			tea.WithAltScreen(),
			tea.WithInput(os.Stdin),
			tea.WithOutput(os.Stdout),
		)

		// Wire runner progress updates to dashboard using named callback
		// This uses AddProgressCallback instead of OnProgress to prevent Telegram handler
		// from overwriting the dashboard callback (GH-149 fix)
		runner.AddProgressCallback("dashboard", func(taskID, phase string, progress int, message string) {
			monitor.UpdateProgress(taskID, phase, progress, message)
			tasks := convertTaskStatesToDisplay(monitor.GetAll())
			program.Send(dashboard.UpdateTasks(tasks)())

			logMsg := fmt.Sprintf("[%s] %s: %s (%d%%)", taskID, phase, message, progress)
			program.Send(dashboard.AddLog(logMsg)())
		})

		// Wire token usage updates to dashboard (GH-156 fix)
		runner.AddTokenCallback("dashboard", func(taskID string, inputTokens, outputTokens int64) {
			program.Send(dashboard.UpdateTokens(int(inputTokens), int(outputTokens))())
		})
	}

	// Initialize Telegram handler if enabled
	var tgHandler *telegram.Handler
	if hasTelegram {
		var allowedIDs []int64
		// Include explicitly configured allowed IDs
		allowedIDs = append(allowedIDs, cfg.Adapters.Telegram.AllowedIDs...)
		// Also include ChatID so user can message their own bot
		if cfg.Adapters.Telegram.ChatID != "" {
			if id, err := parseInt64(cfg.Adapters.Telegram.ChatID); err == nil {
				allowedIDs = append(allowedIDs, id)
			}
		}

		tgHandler = telegram.NewHandler(&telegram.HandlerConfig{
			BotToken:      cfg.Adapters.Telegram.BotToken,
			ProjectPath:   projectPath,
			Projects:      config.NewProjectSource(cfg),
			AllowedIDs:    allowedIDs,
			Transcription: cfg.Adapters.Telegram.Transcription,
			RateLimit:     cfg.Adapters.Telegram.RateLimit,
			LLMClassifier: cfg.Adapters.Telegram.LLMClassifier,
		}, runner)

		// Security warning if no allowed IDs configured
		if len(allowedIDs) == 0 {
			logging.WithComponent("telegram").Warn("SECURITY: allowed_ids is empty - ALL users can interact with the bot!")
		}

		// Check for existing instance
		if err := tgHandler.CheckSingleton(ctx); err != nil {
			if errors.Is(err, telegram.ErrConflict) {
				if replace {
					fmt.Println("🔄 Stopping existing bot instance...")
					if err := killExistingTelegramBot(); err != nil {
						return fmt.Errorf("failed to stop existing instance: %w", err)
					}
					fmt.Print("   Waiting for Telegram to release connection")
					maxRetries := 10
					var lastErr error
					for i := 0; i < maxRetries; i++ {
						delay := time.Duration(500+i*500) * time.Millisecond
						time.Sleep(delay)
						fmt.Print(".")
						if err := tgHandler.CheckSingleton(ctx); err == nil {
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
						return fmt.Errorf("timeout waiting for Telegram to release connection")
					}
				} else {
					fmt.Println()
					fmt.Println("❌ Another bot instance is already running")
					fmt.Println()
					fmt.Println("   Options:")
					fmt.Println("   • Kill it manually:  pkill -f 'pilot start'")
					fmt.Println("   • Auto-replace:      pilot start --replace")
					fmt.Println()
					return fmt.Errorf("conflict: another bot instance is running")
				}
			} else {
				return fmt.Errorf("singleton check failed: %w", err)
			}
		}
	}

	// Show startup banner (skip in dashboard mode to avoid corrupting TUI)
	if !dashboardMode {
		banner.StartupTelegram(version, projectPath, cfg.Adapters.Telegram.ChatID, cfg)
	}

	// Log autopilot status
	if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.Enabled {
		logging.WithComponent("start").Info("autopilot enabled",
			slog.String("environment", string(cfg.Orchestrator.Autopilot.Environment)),
			slog.Bool("auto_merge", cfg.Orchestrator.Autopilot.AutoMerge),
			slog.Bool("auto_review", cfg.Orchestrator.Autopilot.AutoReview),
		)
	}

	// Initialize alerts engine for outbound notifications (GH-337)
	var alertsEngine *alerts.Engine
	alertsCfg := getAlertsConfig(cfg)
	if alertsCfg != nil && alertsCfg.Enabled {
		// Create dispatcher and register channels
		alertsDispatcher := alerts.NewDispatcher(alertsCfg)

		// Register Slack channel if configured
		if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
			slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
			for _, ch := range alertsCfg.Channels {
				if ch.Type == "slack" && ch.Slack != nil {
					slackChannel := alerts.NewSlackChannel(ch.Name, slackClient, ch.Slack.Channel)
					alertsDispatcher.RegisterChannel(slackChannel)
				}
			}
		}

		// Register Telegram channel if configured
		if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
			telegramClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
			for _, ch := range alertsCfg.Channels {
				if ch.Type == "telegram" && ch.Telegram != nil {
					telegramChannel := alerts.NewTelegramChannel(ch.Name, telegramClient, ch.Telegram.ChatID)
					alertsDispatcher.RegisterChannel(telegramChannel)
				}
			}
		}

		alertsEngine = alerts.NewEngine(alertsCfg, alerts.WithDispatcher(alertsDispatcher))
		if err := alertsEngine.Start(ctx); err != nil {
			logging.WithComponent("start").Warn("failed to start alerts engine", slog.Any("error", err))
			alertsEngine = nil
		} else {
			logging.WithComponent("start").Info("alerts engine started",
				slog.Int("channels", len(alertsDispatcher.ListChannels())),
			)
		}
	}

	// Initialize dispatcher for task queue (uses store created earlier)
	var dispatcher *executor.Dispatcher
	if store != nil {
		dispatcher = executor.NewDispatcher(store, runner, nil)
		if err := dispatcher.Start(); err != nil {
			logging.WithComponent("start").Warn("Failed to start dispatcher", slog.Any("error", err))
			dispatcher = nil
		} else {
			logging.WithComponent("start").Info("Task dispatcher started")
		}
	}

	// Start GitHub polling if enabled
	var ghPoller *github.Poller
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
		cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {

		token := cfg.Adapters.GitHub.Token
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}

		if token != "" && cfg.Adapters.GitHub.Repo != "" {
			// GH-386: Validate repo/project match at startup to prevent cross-project execution
			if err := executor.ValidateRepoProjectMatch(cfg.Adapters.GitHub.Repo, projectPath); err != nil {
				logging.WithComponent("github").Warn("repo/project mismatch detected - issues may execute against wrong project",
					slog.String("repo", cfg.Adapters.GitHub.Repo),
					slog.String("project_path", projectPath),
					slog.String("expected_project", executor.ExtractRepoName(cfg.Adapters.GitHub.Repo)),
				)
				// In strict mode, we could return an error here:
				// return fmt.Errorf("repo/project mismatch: %w", err)
			}

			client := github.NewClient(token)
			label := cfg.Adapters.GitHub.Polling.Label
			if label == "" {
				label = cfg.Adapters.GitHub.PilotLabel
			}
			interval := cfg.Adapters.GitHub.Polling.Interval
			if interval == 0 {
				interval = 30 * time.Second
			}

			// Determine execution mode from config
			execMode := github.ExecutionModeSequential // Default to sequential
			waitForMerge := true
			pollInterval := 30 * time.Second
			prTimeout := 1 * time.Hour

			if cfg.Orchestrator != nil && cfg.Orchestrator.Execution != nil {
				execCfg := cfg.Orchestrator.Execution
				if execCfg.Mode == "parallel" {
					execMode = github.ExecutionModeParallel
				}
				waitForMerge = execCfg.WaitForMerge
				if execCfg.PollInterval > 0 {
					pollInterval = execCfg.PollInterval
				}
				if execCfg.PRTimeout > 0 {
					prTimeout = execCfg.PRTimeout
				}
			}

			var pollerOpts []github.PollerOption

			// Wire autopilot OnPRCreated callback if controller initialized
			// Note: autopilotController is created earlier (before dashboard) to ensure
			// the same instance is used by both dashboard and GitHub poller (GH-263)
			if autopilotController != nil {
				pollerOpts = append(pollerOpts,
					github.WithOnPRCreated(autopilotController.OnPRCreated),
				)
			}

			// Create rate limit retry scheduler
			// Parse owner/repo for GetIssue calls
			repoParts := strings.Split(cfg.Adapters.GitHub.Repo, "/")
			if len(repoParts) != 2 {
				return fmt.Errorf("invalid repo format: %s", cfg.Adapters.GitHub.Repo)
			}
			repoOwner, repoName := repoParts[0], repoParts[1]

			rateLimitScheduler := executor.NewScheduler(executor.DefaultSchedulerConfig(), nil)
			rateLimitScheduler.SetRetryCallback(func(retryCtx context.Context, pendingTask *executor.PendingTask) error {
				// Extract issue number from task ID (format: "GH-123")
				var issueNum int
				if _, err := fmt.Sscanf(pendingTask.Task.ID, "GH-%d", &issueNum); err != nil {
					return fmt.Errorf("invalid task ID format: %s", pendingTask.Task.ID)
				}

				// Fetch the issue again to get current state
				issue, err := client.GetIssue(retryCtx, repoOwner, repoName, issueNum)
				if err != nil {
					return fmt.Errorf("failed to fetch issue for retry: %w", err)
				}

				logging.WithComponent("scheduler").Info("Retrying rate-limited issue",
					slog.Int("issue", issueNum),
					slog.Int("attempt", pendingTask.Attempts),
				)

				// Re-process the issue
				if execMode == github.ExecutionModeSequential {
					_, err = handleGitHubIssueWithResult(retryCtx, cfg, client, issue, projectPath, dispatcher, runner, monitor, program, alertsEngine)
				} else {
					err = handleGitHubIssueWithMonitor(retryCtx, cfg, client, issue, projectPath, dispatcher, runner, monitor, program, alertsEngine)
				}
				return err
			})
			rateLimitScheduler.SetExpiredCallback(func(expiredCtx context.Context, pendingTask *executor.PendingTask) {
				logging.WithComponent("scheduler").Error("Task exceeded max retry attempts",
					slog.String("task_id", pendingTask.Task.ID),
					slog.Int("attempts", pendingTask.Attempts),
				)
			})
			if err := rateLimitScheduler.Start(ctx); err != nil {
				logging.WithComponent("start").Warn("Failed to start rate limit scheduler", slog.Any("error", err))
			} else {
				logging.WithComponent("start").Info("Rate limit retry scheduler started")
			}

			// Configure based on execution mode
			if execMode == github.ExecutionModeSequential {
				pollerOpts = append(pollerOpts,
					github.WithExecutionMode(github.ExecutionModeSequential),
					github.WithSequentialConfig(waitForMerge, pollInterval, prTimeout),
					github.WithScheduler(rateLimitScheduler),
					github.WithOnIssueWithResult(func(issueCtx context.Context, issue *github.Issue) (*github.IssueResult, error) {
						return handleGitHubIssueWithResult(issueCtx, cfg, client, issue, projectPath, dispatcher, runner, monitor, program, alertsEngine)
					}),
				)
			} else {
				pollerOpts = append(pollerOpts,
					github.WithExecutionMode(github.ExecutionModeParallel),
					github.WithScheduler(rateLimitScheduler),
					github.WithOnIssue(func(issueCtx context.Context, issue *github.Issue) error {
						return handleGitHubIssueWithMonitor(issueCtx, cfg, client, issue, projectPath, dispatcher, runner, monitor, program, alertsEngine)
					}),
				)
			}

			var err error
			ghPoller, err = github.NewPoller(client, cfg.Adapters.GitHub.Repo, label, interval, pollerOpts...)
			if err != nil {
				if !dashboardMode {
					fmt.Printf("⚠️  GitHub polling disabled: %v\n", err)
				}
			} else {
				modeStr := "sequential"
				if execMode == github.ExecutionModeParallel {
					modeStr = "parallel"
				}
				if !dashboardMode {
					fmt.Printf("🐙 GitHub polling enabled: %s (every %s, mode: %s)\n", cfg.Adapters.GitHub.Repo, interval, modeStr)
					if execMode == github.ExecutionModeSequential && waitForMerge {
						fmt.Printf("   ⏳ Sequential mode: waiting for PR merge before next issue (timeout: %s)\n", prTimeout)
					}
				}
				go ghPoller.Start(ctx)

				// Start autopilot processing loop if controller initialized
				// Uses autopilotController (created earlier) to ensure dashboard shows scanned PRs (GH-263)
				if autopilotController != nil {
					// Scan for existing PRs created by Pilot before starting the loop
					if err := autopilotController.ScanExistingPRs(ctx); err != nil {
						logging.WithComponent("autopilot").Warn("failed to scan existing PRs",
							slog.Any("error", err),
						)
					}

					if !dashboardMode {
						fmt.Printf("🤖 Autopilot enabled: %s environment\n", cfg.Orchestrator.Autopilot.Environment)
					}
					go func() {
						if err := autopilotController.Run(ctx); err != nil && err != context.Canceled {
							logging.WithComponent("autopilot").Error("autopilot controller stopped",
								slog.Any("error", err),
							)
						}
					}()
				}
			}

			// Start stale label cleanup if enabled
			if cfg.Adapters.GitHub.StaleLabelCleanup != nil && cfg.Adapters.GitHub.StaleLabelCleanup.Enabled {
				if store != nil {
					cleaner, cleanerErr := github.NewCleaner(client, store, cfg.Adapters.GitHub.Repo, cfg.Adapters.GitHub.StaleLabelCleanup)
					if cleanerErr != nil {
						if !dashboardMode {
							fmt.Printf("⚠️  Stale label cleanup disabled: %v\n", cleanerErr)
						}
					} else {
						if !dashboardMode {
							fmt.Printf("🧹 Stale label cleanup enabled (every %s, threshold: %s)\n",
								cfg.Adapters.GitHub.StaleLabelCleanup.Interval,
								cfg.Adapters.GitHub.StaleLabelCleanup.Threshold)
						}
						go cleaner.Start(ctx)
					}
				}
			}
		}
	}

	// Start Telegram polling if enabled
	if tgHandler != nil {
		if !dashboardMode {
			fmt.Println("📱 Telegram polling started")
		}
		tgHandler.StartPolling(ctx)
	}

	// Start brief scheduler if enabled
	var briefScheduler *briefs.Scheduler
	if cfg.Orchestrator.DailyBrief != nil && cfg.Orchestrator.DailyBrief.Enabled {
		briefCfg := cfg.Orchestrator.DailyBrief

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

		// Create generator (requires store)
		if store != nil {
			generator := briefs.NewGenerator(store, briefsConfig)

			// Create delivery service with available clients
			var deliveryOpts []briefs.DeliveryOption
			if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
				slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
				deliveryOpts = append(deliveryOpts, briefs.WithSlackClient(slackClient))
			}
			if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled {
				tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
				deliveryOpts = append(deliveryOpts, briefs.WithTelegramSender(&telegramBriefAdapter{client: tgClient}))
			}
			deliveryOpts = append(deliveryOpts, briefs.WithLogger(slog.Default()))

			delivery := briefs.NewDeliveryService(briefsConfig, deliveryOpts...)

			// Create and start scheduler
			briefScheduler = briefs.NewScheduler(generator, delivery, briefsConfig, slog.Default())
			if err := briefScheduler.Start(ctx); err != nil {
				logging.WithComponent("start").Warn("Failed to start brief scheduler", slog.Any("error", err))
				briefScheduler = nil
			} else {
				logging.WithComponent("start").Info("brief scheduler started",
					slog.String("schedule", briefCfg.Schedule),
					slog.String("timezone", briefCfg.Timezone),
				)
			}
		} else {
			logging.WithComponent("start").Warn("Brief scheduler requires memory store, skipping")
		}
	}

	// Dashboard mode: run TUI and handle shutdown via TUI quit
	if dashboardMode && program != nil {
		fmt.Println("\n🖥️  Starting TUI dashboard...")

		// Start background version checker for hot reload (GH-369)
		versionChecker := upgrade.NewVersionChecker(version, upgrade.DefaultCheckInterval)
		versionChecker.OnUpdate(func(info *upgrade.VersionInfo) {
			program.Send(dashboard.NotifyUpdateAvailable(info.Current, info.Latest, info.ReleaseNotes)())
			program.Send(dashboard.AddLog(fmt.Sprintf("⬆️ Update available: %s → %s", info.Current, info.Latest))())
		})
		versionChecker.Start(ctx)
		defer versionChecker.Stop()

		// Set up hot upgrade goroutine - listens for upgrade requests from 'u' key press
		// The channel is created above and passed to the dashboard model
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-upgradeRequestCh:
					info := versionChecker.GetLatestInfo()
					if info == nil || !info.UpdateAvail || info.LatestRelease == nil {
						program.Send(dashboard.NotifyUpgradeComplete(false, "No update available")())
						continue
					}

					// Perform hot upgrade
					// Pass nil TaskChecker - the upgrade will proceed immediately
					// In future, we could implement TaskChecker on Runner to wait for tasks
					hotUpgrader, err := upgrade.NewHotUpgrader(version, nil)
					if err != nil {
						program.Send(dashboard.NotifyUpgradeComplete(false, err.Error())())
						program.Send(dashboard.AddLog(fmt.Sprintf("❌ Upgrade failed: %v", err))())
						continue
					}

					upgradeCfg := &upgrade.HotUpgradeConfig{
						WaitForTasks: true,
						TaskTimeout:  2 * time.Minute,
						OnProgress: func(pct int, msg string) {
							program.Send(dashboard.NotifyUpgradeProgress(pct, msg)())
						},
						FlushSession: func() error {
							// Future: flush session state to SQLite here
							return nil
						},
					}

					if err := hotUpgrader.PerformHotUpgrade(ctx, info.LatestRelease, upgradeCfg); err != nil {
						program.Send(dashboard.NotifyUpgradeComplete(false, err.Error())())
						program.Send(dashboard.AddLog(fmt.Sprintf("❌ Upgrade failed: %v", err))())
					}
					// If upgrade succeeds, the process is replaced and this line is never reached
				}
			}
		}()

		// Periodic refresh to catch any missed updates
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if monitor != nil {
						tasks := convertTaskStatesToDisplay(monitor.GetAll())
						program.Send(dashboard.UpdateTasks(tasks)())
					}
				}
			}
		}()

		// Add startup logs after TUI starts (Send blocks if Run hasn't been called)
		go func() {
			time.Sleep(100 * time.Millisecond) // Wait for Run() to start
			program.Send(dashboard.AddLog(fmt.Sprintf("🚀 Pilot %s started - Polling mode", version))())
			if hasTelegram {
				program.Send(dashboard.AddLog("📱 Telegram polling active")())
			}
			hasGitHubPolling := cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled
			if hasGitHubPolling {
				program.Send(dashboard.AddLog(fmt.Sprintf("🐙 GitHub polling: %s", cfg.Adapters.GitHub.Repo))())
			}

			// Check for restart marker (set by hot upgrade)
			if os.Getenv("PILOT_RESTARTED") == "1" {
				prevVersion := os.Getenv("PILOT_PREVIOUS_VERSION")
				if prevVersion != "" {
					program.Send(dashboard.AddLog(fmt.Sprintf("✅ Upgraded from %s to %s", prevVersion, version))())
				} else {
					program.Send(dashboard.AddLog("✅ Pilot restarted successfully")())
				}
			}
		}()

		// Run TUI (blocks until quit via 'q' or Ctrl+C)
		// Note: The upgrade callback is handled via upgradeRequestCh above
		if _, err := program.Run(); err != nil {
			cancel() // Stop goroutines
			return fmt.Errorf("dashboard error: %w", err)
		}

		// Clean shutdown - cancel context to stop all goroutines
		cancel()

		if tgHandler != nil {
			tgHandler.Stop()
		}
		// ghPoller stops via context cancellation (no explicit stop needed)
		if dispatcher != nil {
			dispatcher.Stop()
		}
		if briefScheduler != nil {
			briefScheduler.Stop()
		}
		return nil
	}

	// Non-dashboard mode: wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	fmt.Println("\n🛑 Shutting down...")
	if tgHandler != nil {
		tgHandler.Stop()
	}
	if ghPoller != nil {
		fmt.Println("🐙 Stopping GitHub poller...")
	}
	if dispatcher != nil {
		fmt.Println("📋 Stopping task dispatcher...")
		dispatcher.Stop()
	}
	if briefScheduler != nil {
		briefScheduler.Stop()
	}

	return nil
}

// logGitHubAPIError logs GitHub API errors at warn level with context.
// Label operations are non-critical - task execution continues even if labeling fails.
func logGitHubAPIError(operation string, owner, repo string, issueNum int, err error) {
	if err != nil {
		logging.WithComponent("github").Warn("GitHub API call failed",
			slog.String("operation", operation),
			slog.String("repo", owner+"/"+repo),
			slog.Int("issue", issueNum),
			slog.Any("error", err),
		)
	}
}

// handleGitHubIssue processes a GitHub issue picked up by the poller
func handleGitHubIssue(ctx context.Context, cfg *config.Config, client *github.Client, issue *github.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner) error {
	sourceRepo := cfg.Adapters.GitHub.Repo

	// GH-386: Pre-execution validation - fail fast if repo doesn't match project
	if err := executor.ValidateRepoProjectMatch(sourceRepo, projectPath); err != nil {
		logging.WithComponent("github").Error("cross-project execution blocked",
			slog.Any("error", err),
			slog.Int("issue_number", issue.Number),
			slog.String("repo", sourceRepo),
			slog.String("project_path", projectPath),
		)
		return fmt.Errorf("cross-project execution blocked: %w", err)
	}

	fmt.Printf("\n📥 GitHub Issue #%d: %s\n", issue.Number, issue.Title)

	parts := strings.Split(sourceRepo, "/")
	if len(parts) == 2 {
		if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelInProgress}); err != nil {
			logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
		}
	}

	taskDesc := fmt.Sprintf("GitHub Issue #%d: %s\n\n%s", issue.Number, issue.Title, issue.Body)
	taskID := fmt.Sprintf("GH-%d", issue.Number)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// Always create branches and PRs - required for autopilot workflow
	// GH-386: Include SourceRepo for cross-project validation in executor
	task := &executor.Task{
		ID:          taskID,
		Title:       issue.Title,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
		CreatePR:    true,
		SourceRepo:  sourceRepo,
	}

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    task.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = runner.Execute(ctx, task)
	}

	if len(parts) == 2 {
		if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelInProgress); err != nil {
			logGitHubAPIError("RemoveLabel", parts[0], parts[1], issue.Number, err)
		}

		if execErr != nil {
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		} else if result != nil && result.Success {
			// Validate deliverables before marking as done
			if result.CommitSHA == "" && result.PRUrl == "" {
				// No commits and no PR - mark as failed
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
					result.Duration, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			} else {
				// Has deliverables - mark as done
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelDone}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				comment := fmt.Sprintf("✅ Pilot completed!\n\n**Duration:** %s\n**Branch:** `%s`",
					result.Duration, branchName)
				if result.PRUrl != "" {
					comment += fmt.Sprintf("\n**PR:** %s", result.PRUrl)
				}
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			}
		} else if result != nil {
			// result exists but Success is false - mark as failed
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := fmt.Sprintf("❌ Pilot execution completed but failed:\n\n```\n%s\n```", result.Error)
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		}
	}

	return execErr
}

// handleGitHubIssueWithMonitor processes a GitHub issue with optional dashboard monitoring
// Used in parallel mode when dashboard is enabled
func handleGitHubIssueWithMonitor(ctx context.Context, cfg *config.Config, client *github.Client, issue *github.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine) error {
	taskID := fmt.Sprintf("GH-%d", issue.Number)

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		monitor.Register(taskID, issue.Title, issue.HTMLURL)
		monitor.Start(taskID)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📥 GitHub Issue #%d: %s", issue.Number, issue.Title))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: issue.Title,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	err := handleGitHubIssue(ctx, cfg, client, issue, projectPath, dispatcher, runner)

	// Update monitor with completion status
	if monitor != nil {
		if err != nil {
			monitor.Fail(taskID, err.Error())
		} else {
			monitor.Complete(taskID, "")
		}
	}

	// Emit task completed/failed event (GH-337)
	if alertsEngine != nil {
		if err != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     err.Error(),
				Timestamp: time.Now(),
			})
		} else {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Timestamp: time.Now(),
			})
		}
	}

	// Add completed task to dashboard history
	if program != nil {
		status := "success"
		if err != nil {
			status = "failed"
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Title, status, "")())
	}

	return err
}

// handleGitHubIssueWithResult processes a GitHub issue and returns result with PR info
// Used in sequential mode to enable PR merge waiting
func handleGitHubIssueWithResult(ctx context.Context, cfg *config.Config, client *github.Client, issue *github.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine) (*github.IssueResult, error) {
	taskID := fmt.Sprintf("GH-%d", issue.Number)
	sourceRepo := cfg.Adapters.GitHub.Repo

	// GH-386: Pre-execution validation - fail fast if repo doesn't match project
	if err := executor.ValidateRepoProjectMatch(sourceRepo, projectPath); err != nil {
		logging.WithComponent("github").Error("cross-project execution blocked",
			slog.Any("error", err),
			slog.Int("issue_number", issue.Number),
			slog.String("repo", sourceRepo),
			slog.String("project_path", projectPath),
		)
		wrappedErr := fmt.Errorf("cross-project execution blocked: %w", err)
		return &github.IssueResult{
			Success: false,
			Error:   wrappedErr,
		}, wrappedErr
	}

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		monitor.Register(taskID, issue.Title, issue.HTMLURL)
		monitor.Start(taskID)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📥 GitHub Issue #%d: %s", issue.Number, issue.Title))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: issue.Title,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	fmt.Printf("\n📥 GitHub Issue #%d: %s\n", issue.Number, issue.Title)

	parts := strings.Split(sourceRepo, "/")
	if len(parts) == 2 {
		if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelInProgress}); err != nil {
			logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
		}
	}

	taskDesc := fmt.Sprintf("GitHub Issue #%d: %s\n\n%s", issue.Number, issue.Title, issue.Body)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// Always create branches and PRs - required for autopilot workflow
	// GH-386: Include SourceRepo for cross-project validation in executor
	task := &executor.Task{
		ID:          taskID,
		Title:       issue.Title,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
		CreatePR:    true,
		SourceRepo:  sourceRepo,
	}

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, task)
		if qErr != nil {
			execErr = fmt.Errorf("failed to queue task: %w", qErr)
		} else {
			fmt.Printf("   📋 Queued as execution %s\n", execID[:8])
			exec, waitErr := dispatcher.WaitForExecution(ctx, execID, time.Second)
			if waitErr != nil {
				execErr = fmt.Errorf("failed waiting for execution: %w", waitErr)
			} else if exec.Status == "failed" {
				execErr = fmt.Errorf("execution failed: %s", exec.Error)
			} else {
				result = &executor.ExecutionResult{
					TaskID:    task.ID,
					Success:   exec.Status == "completed",
					Output:    exec.Output,
					Error:     exec.Error,
					PRUrl:     exec.PRUrl,
					CommitSHA: exec.CommitSHA,
					Duration:  time.Duration(exec.DurationMs) * time.Millisecond,
				}
			}
		}
	} else {
		result, execErr = runner.Execute(ctx, task)
	}

	// Update monitor with completion status
	prURL := ""
	if result != nil {
		prURL = result.PRUrl
	}
	if monitor != nil {
		if execErr != nil {
			monitor.Fail(taskID, execErr.Error())
		} else {
			monitor.Complete(taskID, prURL)
		}
	}

	// Emit task completed/failed event (GH-337)
	if alertsEngine != nil {
		if execErr != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     execErr.Error(),
				Timestamp: time.Now(),
			})
		} else if result != nil && result.Success {
			metadata := map[string]string{}
			if result.PRUrl != "" {
				metadata["pr_url"] = result.PRUrl
			}
			if result.Duration > 0 {
				metadata["duration"] = result.Duration.String()
			}
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskCompleted,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Title,
				Project:   projectPath,
				Error:     result.Error,
				Timestamp: time.Now(),
			})
		}
	}

	// Add completed task to dashboard history
	if program != nil {
		status := "success"
		duration := ""
		if execErr != nil {
			status = "failed"
		}
		if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Title, status, duration)())
	}

	// Build the issue result
	issueResult := &github.IssueResult{
		Success: execErr == nil && result != nil && result.Success,
		Error:   execErr,
	}

	// Extract PR number and head SHA from result if we have one
	if result != nil {
		if result.PRUrl != "" {
			issueResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				issueResult.PRNumber = prNum
			}
		}
		if result.CommitSHA != "" {
			issueResult.HeadSHA = result.CommitSHA
		}
	}

	// Update issue labels and add comment
	if len(parts) == 2 {
		if err := client.RemoveLabel(ctx, parts[0], parts[1], issue.Number, github.LabelInProgress); err != nil {
			logGitHubAPIError("RemoveLabel", parts[0], parts[1], issue.Number, err)
		}

		if execErr != nil {
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		} else if result != nil && result.Success {
			// Validate deliverables before marking as done
			if result.CommitSHA == "" && result.PRUrl == "" {
				// No commits and no PR - mark as failed
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
					result.Duration, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
				// Update issueResult to reflect failure
				issueResult.Success = false
			} else {
				// Has deliverables - mark as done
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelDone}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
				comment := fmt.Sprintf("✅ Pilot completed!\n\n**Duration:** %s\n**Branch:** `%s`",
					result.Duration, branchName)
				if result.PRUrl != "" {
					comment += fmt.Sprintf("\n**PR:** %s", result.PRUrl)
				}
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			}
		} else if result != nil {
			// result exists but Success is false - mark as failed
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := fmt.Sprintf("❌ Pilot execution completed but failed:\n\n```\n%s\n```", result.Error)
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		}
	}

	return issueResult, execErr
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
				prompt := runner.BuildPrompt(task)
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

			// Create the executor runner
			runner := executor.NewRunner()

			// Set up quality gates and decomposition if configured
			{
				configPath := cfgFile
				if configPath == "" {
					configPath = config.DefaultConfigPath()
				}
				cfg, err := config.Load(configPath)
				if err == nil {
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

					// Task decomposition (GH-218)
					if cfg.Executor != nil && cfg.Executor.Decompose != nil && cfg.Executor.Decompose.Enabled {
						runner.SetDecomposer(executor.NewTaskDecomposer(cfg.Executor.Decompose))
						fmt.Println("   Decompose: ✓ enabled")
					}

					// Model routing (GH-215)
					if cfg.Executor != nil {
						runner.SetModelRouter(executor.NewModelRouter(cfg.Executor.ModelRouting, cfg.Executor.Timeout))
					}
				}
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
			if err := client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-in-progress"}); err != nil {
				logGitHubAPIError("AddLabels", owner, repoName, int(issueNum), err)
			}

			// Execute the task
			runner := executor.NewRunner()
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

			// Success with deliverables - add done label
			if err := client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-done"}); err != nil {
				logGitHubAPIError("AddLabels", owner, repoName, int(issueNum), err)
			}

			comment := fmt.Sprintf("✅ Pilot completed successfully!\n\n**Duration:** %s\n**Branch:** `%s`",
				result.Duration, branchName)
			if result.PRUrl != "" {
				comment += fmt.Sprintf("\n**PR:** %s", result.PRUrl)
			}
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
	p.OnProgress(func(taskID, phase string, progress int, message string) {
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
			IssueURL: state.IssueURL,
			PRURL:    state.PRUrl,
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

	// Convert to alerts package types (channel configs are shared types, passed directly)
	channels := make([]alerts.ChannelConfigInput, 0, len(alertsCfg.Channels))
	for _, ch := range alertsCfg.Channels {
		channels = append(channels, alerts.ChannelConfigInput{
			Name:       ch.Name,
			Type:       ch.Type,
			Enabled:    ch.Enabled,
			Severities: ch.Severities,
			Slack:      ch.Slack,     // Same type, direct pass-through
			Telegram:   ch.Telegram,  // Same type, direct pass-through
			Email:      ch.Email,     // Same type, direct pass-through
			Webhook:    ch.Webhook,   // Same type, direct pass-through
			PagerDuty:  ch.PagerDuty, // Same type, direct pass-through
		})
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

// qualityCheckerWrapper adapts quality.Executor to executor.QualityChecker interface
type qualityCheckerWrapper struct {
	executor *quality.Executor
}

// Check implements executor.QualityChecker by delegating to quality.Executor
// and converting the result type
func (w *qualityCheckerWrapper) Check(ctx context.Context) (*executor.QualityOutcome, error) {
	outcome, err := w.executor.Check(ctx)
	if err != nil {
		return nil, err
	}

	result := &executor.QualityOutcome{
		Passed:        outcome.Passed,
		ShouldRetry:   outcome.ShouldRetry,
		RetryFeedback: outcome.RetryFeedback,
		Attempt:       outcome.Attempt,
	}

	// Populate gate details if results are available (GH-209)
	if outcome.Results != nil {
		result.TotalDuration = outcome.Results.TotalTime
		result.GateDetails = make([]executor.QualityGateDetail, len(outcome.Results.Results))
		for i, r := range outcome.Results.Results {
			result.GateDetails[i] = executor.QualityGateDetail{
				Name:       r.GateName,
				Passed:     r.Status == quality.StatusPassed,
				Duration:   r.Duration,
				RetryCount: r.RetryCount,
				Error:      r.Error,
			}
		}
	}

	return result, nil
}

// resolveOwnerRepo determines the GitHub owner and repo from config or git remote.
func resolveOwnerRepo(cfg *config.Config) (string, string, error) {
	// Try config first
	ghCfg := cfg.Adapters.GitHub
	if ghCfg != nil && ghCfg.Repo != "" {
		parts := strings.SplitN(ghCfg.Repo, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
	}

	// Try git remote
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("could not determine repository - set github.repo in config")
	}

	// Parse remote URL (handles both HTTPS and SSH)
	remote := strings.TrimSpace(string(out))
	// git@github.com:owner/repo.git
	// https://github.com/owner/repo.git
	remote = strings.TrimSuffix(remote, ".git")

	if strings.Contains(remote, "github.com:") {
		parts := strings.Split(remote, "github.com:")
		if len(parts) == 2 {
			ownerRepo := strings.Split(parts[1], "/")
			if len(ownerRepo) == 2 {
				return ownerRepo[0], ownerRepo[1], nil
			}
		}
	}

	if strings.Contains(remote, "github.com/") {
		parts := strings.Split(remote, "github.com/")
		if len(parts) == 2 {
			ownerRepo := strings.Split(parts[1], "/")
			if len(ownerRepo) == 2 {
				return ownerRepo[0], ownerRepo[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("could not parse GitHub remote: %s", remote)
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
