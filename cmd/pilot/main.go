// Dashboard progress test - GH-151
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/alekspetrov/pilot/internal/adapters/asana"
	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/jira"
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
	"github.com/alekspetrov/pilot/internal/gateway"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/pilot"
	"github.com/alekspetrov/pilot/internal/quality"
	"github.com/alekspetrov/pilot/internal/teams"
	"github.com/alekspetrov/pilot/internal/tunnel"
	"github.com/alekspetrov/pilot/internal/upgrade"
)

var (
	version     = "1.0.0"
	buildTime   = "unknown"
	cfgFile     string
	teamAdapter *teams.ServiceAdapter // Global team adapter for RBAC lookups (GH-634)
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
		newAutopilotCmd(),
		newOnboardCmd(),
		newBackendCmd(),
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
		enableSlack    bool
		// Mode flags
		noGateway    bool   // Lightweight mode: polling only, no HTTP gateway
		sequential   bool   // Sequential execution mode (one issue at a time)
		envFlag      string // Environment name: dev, stage, prod, or custom configured name
		enableTunnel bool   // Enable public tunnel (Cloudflare/ngrok)
		teamID       string // Optional team ID for scoping execution
		teamMember   string // Member email for project access scoping
		logFormat    string // Log output format: text or json (GH-847)
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
  pilot start --slack                  # Enable Slack Socket Mode
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
			applyInputOverrides(cfg, cmd, enableTelegram, enableGithub, enableLinear, enableSlack, enableTunnel)

			// Apply team ID override if flag provided
			if teamID != "" {
				cfg.TeamID = teamID
			}

			// Apply team flag overrides (GH-635)
			applyTeamOverrides(cfg, cmd, teamID, teamMember)

			// Initialize logging with config (GH-847)
			// Apply log-format flag override if set
			if cmd.Flags().Changed("log-format") {
				if cfg.Logging == nil {
					cfg.Logging = logging.DefaultConfig()
				}
				cfg.Logging.Format = logFormat
			}
			if cfg.Logging != nil {
				if err := logging.Init(cfg.Logging); err != nil {
					return fmt.Errorf("failed to initialize logging: %w", err)
				}
			}

			// GH-879: Log config reload on hot upgrade
			// After syscall.Exec, the new binary starts fresh and re-reads config from disk
			if os.Getenv("PILOT_RESTARTED") == "1" {
				logging.WithComponent("config").Info("config reloaded from disk after hot upgrade",
					"path", configPath)
			}

			// GH-710: Validate Slack Socket Mode config — degrade gracefully if app_token missing
			if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.SocketMode && cfg.Adapters.Slack.AppToken == "" {
				logging.WithComponent("slack").Warn("socket_mode enabled but app_token not configured, skipping Slack Socket Mode")
				cfg.Adapters.Slack.SocketMode = false
			}

			// Stamp build version into executor config for feature matrix updates (GH-1388)
			if cfg.Executor == nil {
				cfg.Executor = executor.DefaultBackendConfig()
			}
			cfg.Executor.Version = version

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
			hasSlack := cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.SocketMode

			// Apply execution mode override from CLI flags
			if sequential {
				if cfg.Orchestrator.Execution == nil {
					cfg.Orchestrator.Execution = config.DefaultExecutionConfig()
				}
				cfg.Orchestrator.Execution.Mode = "sequential"
			}

			// Override autopilot config if flag provided
			if envFlag != "" {
				if cfg.Orchestrator.Autopilot == nil {
					cfg.Orchestrator.Autopilot = autopilot.DefaultConfig()
				}
				cfg.Orchestrator.Autopilot.Enabled = true
			
				// Use SetActiveEnvironment to validate and resolve environment
				if err := cfg.Orchestrator.Autopilot.SetActiveEnvironment(envFlag); err != nil {
					// Show helpful error with available environments
					availableEnvs := []string{"dev", "stage", "prod"}
					if cfg.Orchestrator.Autopilot.Environments != nil {
						for name := range cfg.Orchestrator.Autopilot.Environments {
							availableEnvs = append(availableEnvs, name)
						}
					}
					fmt.Fprintf(os.Stderr, "Error: %v\n", err)
					fmt.Fprintf(os.Stderr, "Available environments: %v\n", availableEnvs)
					fmt.Fprintf(os.Stderr, "\nTo add a custom environment, add to autopilot.environments in config.yaml:\n")
					fmt.Fprintf(os.Stderr, "autopilot:\n  environments:\n    my-env:\n      branch: main\n      require_approval: true\n")
					return err
				}
			}
			
			// GH-394: Polling mode is the default when any polling adapter is enabled.
			// Previously, having linear.enabled=true would force gateway mode even when
			// only using GitHub/Telegram polling. Now polling adapters work independently.
			//
			// Mode selection:
			// - noGateway flag: always use polling mode (user override)
			// - Polling adapters enabled: use polling mode (Telegram, GitHub)
			// - Only webhook adapters (Linear, Jira): use gateway mode
			//
			// Note: Linear/Jira webhooks require gateway but don't block polling adapters.
			// When both are needed, gateway starts in background within polling mode.
			hasPollingAdapter := hasTelegram || hasGithubPolling
			if noGateway || hasPollingAdapter {
				return runPollingMode(cfg, projectPath, replace, dashboardMode, noGateway)
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

			// Serve embedded React dashboard at /dashboard/ if available (GH-1612)
			if dashboardEmbedded {
				pilotOpts = append(pilotOpts, pilot.WithDashboardFS(dashboardFS))
			}

			// GH-392: Create shared infrastructure for polling adapters in gateway mode
			// This allows GitHub polling to work alongside Linear/Jira webhooks
			telegramFlagSet := cmd.Flags().Changed("telegram")
			githubFlagSet := cmd.Flags().Changed("github")
			slackFlagSet := cmd.Flags().Changed("slack")
			needsPollingInfra := (telegramFlagSet && hasTelegram && cfg.Adapters.Telegram.Polling) ||
				(githubFlagSet && hasGithubPolling && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
					cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled) ||
				(slackFlagSet && hasSlack)

			// Shared infrastructure for polling adapters
			var gwRunner *executor.Runner
			var gwStore *memory.Store
			var gwDispatcher *executor.Dispatcher
			var gwMonitor *executor.Monitor
			var gwProgram *tea.Program
			var gwAutopilotController *autopilot.Controller
			var gwAutopilotStateStore *autopilot.StateStore
			var gwAlertsEngine *alerts.Engine

			if needsPollingInfra {
				// Create shared runner with config (GH-956: enables worktree isolation)
				var runnerErr error
				gwRunner, runnerErr = executor.NewRunnerWithConfig(cfg.Executor)
				if runnerErr != nil {
					return fmt.Errorf("failed to create executor runner: %w", runnerErr)
				}

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

				// Set up team project access checker if configured (GH-635)
				if gwTeamCleanup := wireProjectAccessChecker(gwRunner, cfg); gwTeamCleanup != nil {
					defer gwTeamCleanup()
				}

				// GH-962: Clean up orphaned worktree directories from previous crashed executions
				if cfg.Executor != nil && cfg.Executor.UseWorktree {
					if err := executor.CleanupOrphanedWorktrees(context.Background(), projectPath); err != nil {
						// Log the cleanup but don't fail startup - this is best-effort cleanup
						logging.WithComponent("start").Info("worktree cleanup completed", slog.String("result", err.Error()))
					} else {
						logging.WithComponent("start").Debug("worktree cleanup scan completed, no orphans found")
					}
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

				// GH-634: Initialize teams service for RBAC enforcement in gateway mode
				if gwStore != nil {
					teamStore, teamErr := teams.NewStore(gwStore.DB())
					if teamErr != nil {
						logging.WithComponent("teams").Warn("Failed to initialize team store for gateway", slog.Any("error", teamErr))
					} else {
						teamSvc := teams.NewService(teamStore)
						teamAdapter = teams.NewServiceAdapter(teamSvc)
						gwRunner.SetTeamChecker(teamAdapter)
						logging.WithComponent("teams").Info("team RBAC enforcement enabled for gateway mode")
					}
				}

				// GH-1027: Initialize knowledge store for experiential memories (gateway mode)
				if gwStore != nil {
					knowledgeStore := memory.NewKnowledgeStore(gwStore.DB())
					if err := knowledgeStore.InitSchema(); err != nil {
						logging.WithComponent("knowledge").Warn("Failed to initialize knowledge store schema (gateway)", slog.Any("error", err))
					} else {
						gwRunner.SetKnowledgeStore(knowledgeStore)
						logging.WithComponent("knowledge").Debug("Knowledge store initialized for gateway mode")
					}
				}

				// GH-1599: Wire log store for execution milestone entries (gateway mode)
				if gwStore != nil {
					gwRunner.SetLogStore(gwStore)
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

				// GH-726: Initialize autopilot state store for gateway mode
				if gwStore != nil && gwAutopilotController != nil {
					var gwStoreErr error
					gwAutopilotStateStore, gwStoreErr = autopilot.NewStateStore(gwStore.DB())
					if gwStoreErr != nil {
						logging.WithComponent("autopilot").Warn("Failed to initialize state store (gateway)", slog.Any("error", gwStoreErr))
					} else {
						gwAutopilotController.SetStateStore(gwAutopilotStateStore)
						restored, restoreErr := gwAutopilotController.RestoreState()
						if restoreErr != nil {
							logging.WithComponent("autopilot").Warn("Failed to restore state from SQLite (gateway)", slog.Any("error", restoreErr))
						} else if restored > 0 {
							logging.WithComponent("autopilot").Info("Restored autopilot PR states from SQLite (gateway)", slog.Int("count", restored))
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

					// Register webhook channels
					for _, ch := range alertsCfg.Channels {
						if ch.Type == "webhook" && ch.Enabled && ch.Webhook != nil {
							webhookChannel := alerts.NewWebhookChannel(ch.Name, &alerts.WebhookChannelConfig{
								URL:     ch.Webhook.URL,
								Method:  ch.Webhook.Method,
								Headers: ch.Webhook.Headers,
								Secret:  ch.Webhook.Secret,
							})
							alertsDispatcher.RegisterChannel(webhookChannel)
						}
					}

					// Register email channels
					for _, ch := range alertsCfg.Channels {
						if ch.Type == "email" && ch.Enabled && ch.Email != nil && ch.Email.SMTPHost != "" {
							sender := alerts.NewSMTPSender(ch.Email.SMTPHost, ch.Email.SMTPPort, ch.Email.From, ch.Email.Username, ch.Email.Password)
							emailChannel := alerts.NewEmailChannel(ch.Name, sender, ch.Email)
							alertsDispatcher.RegisterChannel(emailChannel)
						}
					}

					// Register PagerDuty channels
					for _, ch := range alertsCfg.Channels {
						if ch.Type == "pagerduty" && ch.Enabled && ch.PagerDuty != nil {
							pdChannel := alerts.NewPagerDutyChannel(ch.Name, ch.PagerDuty)
							alertsDispatcher.RegisterChannel(pdChannel)
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
					gwRunner.SetMonitor(gwMonitor)
					// GH-1336: Wire monitor to autopilot controller so dashboard shows "done" after merge
					if gwAutopilotController != nil {
						gwAutopilotController.SetMonitor(gwMonitor)
					}
					model := dashboard.NewModelWithOptions(version, gwStore, gwAutopilotController, nil)
					gwProgram = tea.NewProgram(model,
						tea.WithAltScreen(),
						tea.WithInput(os.Stdin),
						tea.WithOutput(os.Stdout),
					)

					// Wire runner progress updates to dashboard
					// GH-1220: Throttle progress callbacks to 200ms to prevent message flooding
					var gwLastDashboardUpdate time.Time
					var gwDashboardMu sync.Mutex
					gwRunner.AddProgressCallback("dashboard", func(taskID, phase string, progress int, message string) {
						gwMonitor.UpdateProgress(taskID, phase, progress, message)

						gwDashboardMu.Lock()
						if time.Since(gwLastDashboardUpdate) < 200*time.Millisecond {
							gwDashboardMu.Unlock()
							return // Skip — periodic ticker will catch it
						}
						gwLastDashboardUpdate = time.Now()
						gwDashboardMu.Unlock()

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
				// GH-634: Wire team member resolver for Telegram RBAC in gateway mode
				if teamAdapter != nil {
					pilotOpts = append(pilotOpts, pilot.WithTelegramMemberResolver(teamAdapter))
				}
				logging.WithComponent("start").Info("Telegram polling enabled in gateway mode")
			}

			// Enable Slack Socket Mode in gateway mode only if --slack flag was explicitly passed (GH-652)
			if slackFlagSet && hasSlack {
				pilotOpts = append(pilotOpts, pilot.WithSlackHandler(gwRunner, projectPath))
				// GH-786: Wire team member resolver for Slack RBAC in gateway mode
				if teamAdapter != nil {
					pilotOpts = append(pilotOpts, pilot.WithSlackMemberResolver(teamAdapter))
				}
				logging.WithComponent("start").Info("Slack Socket Mode enabled in gateway mode")
			}

			// GH-539: Create budget enforcer for gateway mode
			// GH-1019: Debug logging for budget state visibility
			var gwEnforcer *budget.Enforcer
			if cfg.Budget != nil && cfg.Budget.Enabled && gwStore != nil {
				gwEnforcer = budget.NewEnforcer(cfg.Budget, gwStore)
				if gwAlertsEngine != nil {
					gwEnforcer.OnAlert(func(alertType, message, severity string) {
						gwAlertsEngine.ProcessEvent(alerts.Event{
							Type:      alerts.EventTypeBudgetWarning,
							Error:     message,
							Metadata:  map[string]string{"alert_type": alertType, "severity": severity},
							Timestamp: time.Now(),
						})
					})
				}
				logging.WithComponent("start").Info("budget enforcement enabled (gateway mode)",
					slog.Float64("daily_limit", cfg.Budget.DailyLimit),
					slog.Float64("monthly_limit", cfg.Budget.MonthlyLimit),
				)
			} else {
				// GH-1019: Log why budget is disabled for debugging
				logging.WithComponent("start").Debug("budget enforcement disabled (gateway mode)",
					slog.Bool("config_nil", cfg.Budget == nil),
					slog.Bool("enabled", cfg.Budget != nil && cfg.Budget.Enabled),
					slog.Bool("store_nil", gwStore == nil),
				)

				// GH-539: Wire per-task token/duration limits into executor stream (gateway mode)
				maxTokens, maxDuration := gwEnforcer.GetPerTaskLimits()
				if gwRunner != nil && (maxTokens > 0 || maxDuration > 0) {
					var gwTaskLimiters sync.Map
					gwRunner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
						val, _ := gwTaskLimiters.LoadOrStore(taskID, budget.NewTaskLimiter(maxTokens, maxDuration))
						limiter := val.(*budget.TaskLimiter)
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
					logging.WithComponent("start").Info("per-task budget limits enabled (gateway mode)",
						slog.Int64("max_tokens", maxTokens),
						slog.Duration("max_duration", maxDuration),
					)
				}
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
						// Wire sub-issue PR callback so epic sub-PRs are tracked by autopilot (GH-594)
						gwRunner.SetOnSubIssuePRCreated(gwAutopilotController.OnPRCreated)
					}

					// GH-726: Wire processed issue persistence for gateway poller
					if gwAutopilotStateStore != nil {
						pollerOpts = append(pollerOpts, github.WithProcessedStore(gwAutopilotStateStore))
					}

					// Create rate limit retry scheduler
					repoParts := strings.Split(cfg.Adapters.GitHub.Repo, "/")
					if len(repoParts) != 2 {
						return fmt.Errorf("invalid repo format: %s", cfg.Adapters.GitHub.Repo)
					}
					repoOwner, repoName := repoParts[0], repoParts[1]
					gwSourceRepo := cfg.Adapters.GitHub.Repo // GH-929: Capture for closure

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

						var result *github.IssueResult
						if execMode == github.ExecutionModeSequential {
							result, err = handleGitHubIssueWithResult(retryCtx, cfg, client, issue, projectPath, gwSourceRepo, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
						} else {
							result, err = handleGitHubIssueWithResult(retryCtx, cfg, client, issue, projectPath, gwSourceRepo, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
						}

						// GH-797: Call OnPRCreated for retried issues so autopilot tracks their PRs
						if result != nil && result.PRNumber > 0 && gwAutopilotController != nil {
							gwAutopilotController.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName)
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
								return handleGitHubIssueWithResult(issueCtx, cfg, client, issue, projectPath, gwSourceRepo, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
							}),
						)
					} else {
						pollerOpts = append(pollerOpts,
							github.WithScheduler(rateLimitScheduler),
							github.WithMaxConcurrent(cfg.Orchestrator.MaxConcurrent),
							github.WithOnIssueWithResult(func(issueCtx context.Context, issue *github.Issue) (*github.IssueResult, error) {
								return handleGitHubIssueWithResult(issueCtx, cfg, client, issue, projectPath, gwSourceRepo, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
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

							// Scan for recently merged PRs that may need release (GH-416)
							if scanErr := gwAutopilotController.ScanRecentlyMergedPRs(ctx); scanErr != nil {
								logging.WithComponent("autopilot").Warn("failed to scan merged PRs",
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

			// Enable Linear polling in gateway mode if configured (GH-393)
			if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled {

				workspaces := cfg.Adapters.Linear.GetWorkspaces()
				for _, ws := range workspaces {
					// Determine interval: workspace override > global > default
					interval := 30 * time.Second
					if ws.Polling != nil && ws.Polling.Interval > 0 {
						interval = ws.Polling.Interval
					} else if cfg.Adapters.Linear.Polling.Interval > 0 {
						interval = cfg.Adapters.Linear.Polling.Interval
					}

					// Check if workspace polling is explicitly disabled
					if ws.Polling != nil && !ws.Polling.Enabled {
						continue
					}

					linearClient := linear.NewClient(ws.APIKey)

					// Build poller options
					gwLinearPollerOpts := []linear.PollerOption{
						linear.WithOnLinearIssue(func(issueCtx context.Context, issue *linear.Issue) (*linear.IssueResult, error) {
							result, err := handleLinearIssueWithResult(issueCtx, cfg, linearClient, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)

							// GH-1361: Wire PR to autopilot for CI monitoring + auto-merge
							if result != nil && result.PRNumber > 0 && gwAutopilotController != nil {
								// issueNumber=0 because Linear issues don't have GitHub issue numbers
								gwAutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
							}

							return result, err
						}),
					}

					// GH-1351: Wire processed issue persistence to prevent re-dispatch after hot upgrade
					if gwAutopilotStateStore != nil {
						gwLinearPollerOpts = append(gwLinearPollerOpts, linear.WithProcessedStore(gwAutopilotStateStore))
					}

					linearPoller := linear.NewPoller(linearClient, ws, interval, gwLinearPollerOpts...)

					logging.WithComponent("start").Info("Linear polling enabled in gateway mode",
						slog.String("workspace", ws.Name),
						slog.String("team", ws.TeamID),
						slog.Duration("interval", interval),
					)
					go func(p *linear.Poller, name string) {
						if err := p.Start(context.Background()); err != nil {
							logging.WithComponent("linear").Error("Linear poller failed",
								slog.String("workspace", name),
								slog.Any("error", err),
							)
						}
					}(linearPoller, ws.Name)
				}
			}

			// Enable Jira polling in gateway mode if configured (GH-905)
			if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled &&
				cfg.Adapters.Jira.Polling != nil && cfg.Adapters.Jira.Polling.Enabled {

				// Determine interval
				interval := 30 * time.Second
				if cfg.Adapters.Jira.Polling.Interval > 0 {
					interval = cfg.Adapters.Jira.Polling.Interval
				}

				jiraClient := jira.NewClient(
					cfg.Adapters.Jira.BaseURL,
					cfg.Adapters.Jira.Username,
					cfg.Adapters.Jira.APIToken,
					cfg.Adapters.Jira.Platform,
				)
				jiraPoller := jira.NewPoller(jiraClient, cfg.Adapters.Jira, interval,
					jira.WithOnJiraIssue(func(issueCtx context.Context, issue *jira.Issue) (*jira.IssueResult, error) {
						result, err := handleJiraIssueWithResult(issueCtx, cfg, jiraClient, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)

						// GH-1399: Wire PR to autopilot for CI monitoring + auto-merge
						if result != nil && result.PRNumber > 0 && gwAutopilotController != nil {
							// issueNumber=0 because Jira issues don't have GitHub issue numbers
							gwAutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
						}

						return result, err
					}),
				)

				logging.WithComponent("start").Info("Jira polling enabled in gateway mode",
					slog.String("base_url", cfg.Adapters.Jira.BaseURL),
					slog.String("project", cfg.Adapters.Jira.ProjectKey),
					slog.Duration("interval", interval),
				)
				go func(p *jira.Poller) {
					if err := p.Start(context.Background()); err != nil {
						logging.WithComponent("jira").Error("Jira poller failed",
							slog.Any("error", err),
						)
					}
				}(jiraPoller)
			}

			// Enable Asana polling in gateway mode if configured (GH-906)
			if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled &&
				cfg.Adapters.Asana.Polling != nil && cfg.Adapters.Asana.Polling.Enabled {

				// Determine interval
				interval := 30 * time.Second
				if cfg.Adapters.Asana.Polling.Interval > 0 {
					interval = cfg.Adapters.Asana.Polling.Interval
				}

				asanaClient := asana.NewClient(
					cfg.Adapters.Asana.AccessToken,
					cfg.Adapters.Asana.WorkspaceID,
				)
				asanaPoller := asana.NewPoller(asanaClient, cfg.Adapters.Asana, interval,
					asana.WithOnAsanaTask(func(taskCtx context.Context, task *asana.Task) (*asana.TaskResult, error) {
						result, err := handleAsanaTaskWithResult(taskCtx, cfg, asanaClient, task, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)

						// GH-1399: Wire PR to autopilot for CI monitoring + auto-merge
						if result != nil && result.PRNumber > 0 && gwAutopilotController != nil {
							// issueNumber=0 because Asana tasks don't have GitHub issue numbers
							gwAutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
						}

						return result, err
					}),
				)

				logging.WithComponent("start").Info("Asana polling enabled in gateway mode",
					slog.String("workspace", cfg.Adapters.Asana.WorkspaceID),
					slog.String("tag", cfg.Adapters.Asana.PilotTag),
					slog.Duration("interval", interval),
				)
				go func(p *asana.Poller) {
					if err := p.Start(context.Background()); err != nil {
						logging.WithComponent("asana").Error("Asana poller failed",
							slog.Any("error", err),
						)
					}
				}(asanaPoller)
			}

			// Wire teams service if --team flag provided (GH-633)
			var teamsDB *sql.DB
			if cfg.TeamID != "" {
				dbPath := filepath.Join(cfg.Memory.Path, "pilot.db")
				teamsDB, err = sql.Open("sqlite", dbPath)
				if err != nil {
					return fmt.Errorf("failed to open teams database: %w", err)
				}
				teamsStore, storeErr := teams.NewStore(teamsDB)
				if storeErr != nil {
					_ = teamsDB.Close()
					return fmt.Errorf("failed to create teams store: %w", storeErr)
				}
				teamsSvc := teams.NewService(teamsStore)

				// Verify team exists
				team, teamErr := teamsSvc.GetTeam(cfg.TeamID)
				if teamErr != nil || team == nil {
					// Try by name
					team, teamErr = teamsSvc.GetTeamByName(cfg.TeamID)
					if teamErr != nil || team == nil {
						_ = teamsDB.Close()
						return fmt.Errorf("team %q not found — create it with: pilot team create <name> --owner <email>", cfg.TeamID)
					}
					// Resolve name to ID
					cfg.TeamID = team.ID
				}

				pilotOpts = append(pilotOpts, pilot.WithTeamsService(teamsSvc))
				logging.WithComponent("start").Info("teams service initialized",
					slog.String("team_id", team.ID),
					slog.String("team_name", team.Name))
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

			// GH-1585: Wire autopilot provider to gateway so /api/v1/autopilot returns live PR data
			if gwAutopilotController != nil {
				p.Gateway().SetAutopilotProvider(&autopilotProviderAdapter{controller: gwAutopilotController})
			}

			// GH-1609: Wire dashboard store to gateway so /api/v1/{metrics,queue,history,logs} return 200
			if gwStore != nil {
				p.Gateway().SetDashboardStore(gwStore)
				p.Gateway().SetLogStreamStore(gwStore)
			}

			// GH-1633: Wire git graph fetcher to gateway so /api/v1/gitgraph returns live git data
			p.Gateway().SetGitGraphFetcher(func(path string, limit int) interface{} {
				return dashboard.FetchGitGraph(path, limit)
			})
			p.Gateway().SetGitGraphPath(projectPath)

			if err := p.Start(); err != nil {
				return fmt.Errorf("failed to start Pilot: %w", err)
			}

			// Start tunnel if enabled
			if cfg.Tunnel != nil && cfg.Tunnel.Enabled {
				if cfg.Tunnel.Port == 0 {
					cfg.Tunnel.Port = cfg.Gateway.Port
				}
				tunnelMgr, tunnelErr := tunnel.NewManager(cfg.Tunnel, logging.WithComponent("tunnel"))
				if tunnelErr != nil {
					logging.WithComponent("start").Warn("failed to create tunnel", slog.Any("error", tunnelErr))
				} else if setupErr := tunnelMgr.Setup(context.Background()); setupErr != nil {
					logging.WithComponent("start").Warn("tunnel setup failed", slog.Any("error", setupErr))
				} else if publicURL, startErr := tunnelMgr.Start(context.Background()); startErr != nil {
					logging.WithComponent("start").Warn("failed to start tunnel", slog.Any("error", startErr))
				} else {
					fmt.Printf("🌐 Public tunnel: %s\n", publicURL)
					fmt.Printf("   Webhooks: %s/webhooks/{linear,github,gitlab,jira}\n", publicURL)
					defer tunnelMgr.Stop() //nolint:errcheck
				}
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

			// Show Slack status in gateway mode (GH-652)
			if hasSlack {
				fmt.Println("💬 Slack Socket Mode active")
			}

			// Show Linear status in gateway mode (GH-393)
			if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled {
				workspaces := cfg.Adapters.Linear.GetWorkspaces()
				for _, ws := range workspaces {
					fmt.Printf("📊 Linear polling: %s/%s\n", ws.Name, ws.TeamID)
				}
			}

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			<-sigCh
			fmt.Println("\n🛑 Shutting down...")

			// Close teams DB if opened (GH-633)
			if teamsDB != nil {
				_ = teamsDB.Close()
			}

			return p.Stop()
		},
	}

	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run in background (daemon mode)")
	cmd.Flags().BoolVar(&dashboardMode, "dashboard", false, "Show TUI dashboard for real-time task monitoring")
	cmd.Flags().StringVarP(&projectPath, "project", "p", "", "Project path (default: config default or cwd)")
	cmd.Flags().BoolVar(&replace, "replace", false, "Kill existing bot instance before starting")
	cmd.Flags().BoolVar(&noGateway, "no-gateway", false, "Run polling adapters only (no HTTP gateway)")
	cmd.Flags().BoolVar(&sequential, "sequential", false, "Sequential execution: wait for PR merge before next issue")
	cmd.Flags().StringVar(&envFlag, "env", "",
		"Environment name: dev, stage, prod, or custom configured environment")
	// Keep --autopilot as hidden deprecated alias
	cmd.Flags().StringVar(&envFlag, "autopilot", "",
		"DEPRECATED: Use --env instead")
	_ = cmd.Flags().MarkHidden("autopilot")

	// Input adapter flags - standard bool flags
	cmd.Flags().BoolVar(&enableTelegram, "telegram", false, "Enable Telegram polling (overrides config)")
	cmd.Flags().BoolVar(&enableGithub, "github", false, "Enable GitHub polling (overrides config)")
	cmd.Flags().BoolVar(&enableLinear, "linear", false, "Enable Linear webhooks (overrides config)")
	cmd.Flags().BoolVar(&enableSlack, "slack", false, "Enable Slack Socket Mode (overrides config)")
	cmd.Flags().BoolVar(&enableTunnel, "tunnel", false, "Enable public tunnel for webhook ingress (Cloudflare/ngrok)")
	cmd.Flags().StringVar(&teamID, "team", "", "Team ID or name for project access scoping (overrides config)")
	cmd.Flags().StringVar(&teamMember, "team-member", "", "Member email for team access scoping (overrides config)")
	cmd.Flags().StringVar(&logFormat, "log-format", "text", "Log output format: text or json (for log aggregation systems)")

	return cmd
}

// applyInputOverrides applies CLI flag overrides to config
// Uses cmd.Flags().Changed() to only apply flags that were explicitly set
func applyInputOverrides(cfg *config.Config, cmd *cobra.Command, telegramFlag, githubFlag, linearFlag, slackFlag, tunnelFlag bool) {
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
	if cmd.Flags().Changed("slack") {
		if cfg.Adapters.Slack == nil {
			cfg.Adapters.Slack = slack.DefaultConfig()
		}
		cfg.Adapters.Slack.Enabled = slackFlag
		cfg.Adapters.Slack.SocketMode = slackFlag
	}
	if cmd.Flags().Changed("tunnel") {
		if cfg.Tunnel == nil {
			cfg.Tunnel = tunnel.DefaultConfig()
		}
		cfg.Tunnel.Enabled = tunnelFlag
	}
}

// applyTeamOverrides applies --team and --team-member CLI flag overrides to config (GH-635).
// When --team is set, enables team-based project access scoping.
func applyTeamOverrides(cfg *config.Config, cmd *cobra.Command, teamID, teamMember string) {
	if !cmd.Flags().Changed("team") {
		return
	}
	if cfg.Team == nil {
		cfg.Team = &config.TeamConfig{}
	}
	cfg.Team.Enabled = true
	cfg.Team.TeamID = teamID
	if cmd.Flags().Changed("team-member") {
		cfg.Team.MemberEmail = teamMember
	}
}

// runPollingMode runs lightweight polling-only mode.
// When noGateway is false, the HTTP gateway starts in the background so the
// desktop app (and any other client hitting /health) can reach the daemon.
func runPollingMode(cfg *config.Config, projectPath string, replace, dashboardMode, noGateway bool) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Check Telegram config if enabled
	hasTelegram := cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled
	if hasTelegram && cfg.Adapters.Telegram.BotToken == "" {
		return fmt.Errorf("telegram enabled but bot_token not configured")
	}

	// GH-710: Validate Slack Socket Mode config — degrade gracefully if app_token missing
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.SocketMode && cfg.Adapters.Slack.AppToken == "" {
		logging.WithComponent("slack").Warn("socket_mode enabled but app_token not configured, skipping Slack Socket Mode")
		cfg.Adapters.Slack.SocketMode = false
	}

	// Suppress logging BEFORE creating runner in dashboard mode (GH-190)
	// Runner caches its logger at creation time, so suppression must happen first
	if dashboardMode {
		logging.Suppress()
	}

	// Create runner with config (GH-956: enables worktree isolation, decomposer, model routing)
	runner, err := executor.NewRunnerWithConfig(cfg.Executor)
	if err != nil {
		return fmt.Errorf("failed to create executor runner: %w", err)
	}

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

	// Set up team project access checker if configured (GH-635)
	if teamCleanup := wireProjectAccessChecker(runner, cfg); teamCleanup != nil {
		defer teamCleanup()
	}

	// GH-962: Clean up orphaned worktree directories from previous crashed executions
	if cfg.Executor != nil && cfg.Executor.UseWorktree {
		if err := executor.CleanupOrphanedWorktrees(ctx, projectPath); err != nil {
			// Log the cleanup but don't fail startup - this is best-effort cleanup
			logging.WithComponent("start").Info("worktree cleanup completed", slog.String("result", err.Error()))
		} else {
			logging.WithComponent("start").Debug("worktree cleanup scan completed, no orphans found")
		}
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

	// GH-929: Create autopilot controllers map (one per repo) if enabled
	autopilotControllers := make(map[string]*autopilot.Controller)
	var autopilotController *autopilot.Controller // Default controller for backwards compat
	if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.Enabled {
		// Need GitHub client for autopilot
		ghToken := ""
		if cfg.Adapters.GitHub != nil {
			ghToken = cfg.Adapters.GitHub.Token
			if ghToken == "" {
				ghToken = os.Getenv("GITHUB_TOKEN")
			}
		}
		if ghToken != "" {
			ghClient := github.NewClient(ghToken)

			// Create controller for default repo (adapters.github.repo)
			if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
				parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
				if len(parts) == 2 {
					controller := autopilot.NewController(
						cfg.Orchestrator.Autopilot,
						ghClient,
						approvalMgr,
						parts[0],
						parts[1],
					)
					autopilotControllers[cfg.Adapters.GitHub.Repo] = controller
					autopilotController = controller // Default for backwards compat
				}
			}

			// GH-929: Create controllers for each project with GitHub config
			for _, proj := range cfg.Projects {
				if proj.GitHub == nil || proj.GitHub.Owner == "" || proj.GitHub.Repo == "" {
					continue
				}
				repoFullName := fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
				if _, exists := autopilotControllers[repoFullName]; exists {
					continue // Skip duplicates
				}
				controller := autopilot.NewController(
					cfg.Orchestrator.Autopilot,
					ghClient,
					approvalMgr,
					proj.GitHub.Owner,
					proj.GitHub.Repo,
				)
				autopilotControllers[repoFullName] = controller
				logging.WithComponent("autopilot").Info("created controller for project",
					slog.String("project", proj.Name),
					slog.String("repo", repoFullName),
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

	// GH-726: Initialize autopilot state store for crash recovery
	var autopilotStateStore *autopilot.StateStore
	if store != nil && len(autopilotControllers) > 0 {
		var storeErr error
		autopilotStateStore, storeErr = autopilot.NewStateStore(store.DB())
		if storeErr != nil {
			logging.WithComponent("autopilot").Warn("Failed to initialize state store", slog.Any("error", storeErr))
		} else {
			// GH-929: Wire state store to all controllers
			for repoName, controller := range autopilotControllers {
				controller.SetStateStore(autopilotStateStore)
				restored, restoreErr := controller.RestoreState()
				if restoreErr != nil {
					logging.WithComponent("autopilot").Warn("Failed to restore state from SQLite",
						slog.String("repo", repoName),
						slog.Any("error", restoreErr))
				} else if restored > 0 {
					logging.WithComponent("autopilot").Info("Restored autopilot PR states from SQLite",
						slog.String("repo", repoName),
						slog.Int("count", restored))
				}
			}
		}
	}

	// GH-634: Initialize teams service for RBAC enforcement
	if store != nil {
		teamStore, teamErr := teams.NewStore(store.DB())
		if teamErr != nil {
			logging.WithComponent("teams").Warn("Failed to initialize team store", slog.Any("error", teamErr))
		} else {
			teamSvc := teams.NewService(teamStore)
			teamAdapter = teams.NewServiceAdapter(teamSvc)
			runner.SetTeamChecker(teamAdapter)
			logging.WithComponent("teams").Info("team RBAC enforcement enabled for polling mode")
		}
	}

	// GH-1027: Initialize knowledge store for experiential memories
	if store != nil {
		knowledgeStore := memory.NewKnowledgeStore(store.DB())
		if err := knowledgeStore.InitSchema(); err != nil {
			logging.WithComponent("knowledge").Warn("Failed to initialize knowledge store schema", slog.Any("error", err))
		} else {
			runner.SetKnowledgeStore(knowledgeStore)
			logging.WithComponent("knowledge").Debug("Knowledge store initialized for polling mode")
		}
	}

	// GH-1599: Wire log store for execution milestone entries
	if store != nil {
		runner.SetLogStore(store)
	}

	// GH-1662: Start gateway in background so desktop app can reach /health
	if !noGateway && cfg.Gateway != nil {
		gwServer := gateway.NewServer(cfg.Gateway)
		if autopilotController != nil {
			gwServer.SetAutopilotProvider(&autopilotProviderAdapter{controller: autopilotController})
		}
		if store != nil {
			gwServer.SetDashboardStore(store)
			gwServer.SetLogStreamStore(store)
		}
		gwServer.SetGitGraphFetcher(func(path string, limit int) interface{} {
			return dashboard.FetchGitGraph(path, limit)
		})
		gwServer.SetGitGraphPath(projectPath)
		go func() {
			addr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
			logging.WithComponent("gateway").Info("gateway started in background", "addr", addr)
			if err := gwServer.Start(ctx); err != nil && ctx.Err() == nil {
				logging.WithComponent("gateway").Error("gateway background error", "error", err)
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
		runner.SetMonitor(monitor)
		// GH-1336: Wire monitor to autopilot controllers so dashboard shows "done" after merge
		for _, ctrl := range autopilotControllers {
			ctrl.SetMonitor(monitor)
		}
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
		// GH-1220: Throttle progress callbacks to 200ms to prevent message flooding
		var lastDashboardUpdate time.Time
		var dashboardMu sync.Mutex
		runner.AddProgressCallback("dashboard", func(taskID, phase string, progress int, message string) {
			monitor.UpdateProgress(taskID, phase, progress, message)

			dashboardMu.Lock()
			if time.Since(lastDashboardUpdate) < 200*time.Millisecond {
				dashboardMu.Unlock()
				return // Skip — periodic ticker will catch it
			}
			lastDashboardUpdate = time.Now()
			dashboardMu.Unlock()

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

		tgConfig := &telegram.HandlerConfig{
			BotToken:      cfg.Adapters.Telegram.BotToken,
			ProjectPath:   projectPath,
			Projects:      config.NewProjectSource(cfg),
			AllowedIDs:    allowedIDs,
			Transcription: cfg.Adapters.Telegram.Transcription,
			RateLimit:     cfg.Adapters.Telegram.RateLimit,
			LLMClassifier: cfg.Adapters.Telegram.LLMClassifier,
		}
		// GH-634: Wire team member resolver if available (avoid nil interface trap)
		if teamAdapter != nil {
			tgConfig.MemberResolver = teamAdapter
		}
		tgHandler = telegram.NewHandler(tgConfig, runner)

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

		// Register webhook channels
		for _, ch := range alertsCfg.Channels {
			if ch.Type == "webhook" && ch.Enabled && ch.Webhook != nil {
				webhookChannel := alerts.NewWebhookChannel(ch.Name, &alerts.WebhookChannelConfig{
					URL:     ch.Webhook.URL,
					Method:  ch.Webhook.Method,
					Headers: ch.Webhook.Headers,
					Secret:  ch.Webhook.Secret,
				})
				alertsDispatcher.RegisterChannel(webhookChannel)
			}
		}

		// Register email channels
		for _, ch := range alertsCfg.Channels {
			if ch.Type == "email" && ch.Enabled && ch.Email != nil && ch.Email.SMTPHost != "" {
				sender := alerts.NewSMTPSender(ch.Email.SMTPHost, ch.Email.SMTPPort, ch.Email.From, ch.Email.Username, ch.Email.Password)
				emailChannel := alerts.NewEmailChannel(ch.Name, sender, ch.Email)
				alertsDispatcher.RegisterChannel(emailChannel)
			}
		}

		// Register PagerDuty channels
		for _, ch := range alertsCfg.Channels {
			if ch.Type == "pagerduty" && ch.Enabled && ch.PagerDuty != nil {
				pdChannel := alerts.NewPagerDutyChannel(ch.Name, ch.PagerDuty)
				alertsDispatcher.RegisterChannel(pdChannel)
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

	// GH-539: Create budget enforcer if configured
	var enforcer *budget.Enforcer
	if cfg.Budget != nil && cfg.Budget.Enabled && store != nil {
		enforcer = budget.NewEnforcer(cfg.Budget, store)
		// Wire alert callback to alerts engine
		if alertsEngine != nil {
			enforcer.OnAlert(func(alertType, message, severity string) {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetWarning,
					Error:     message,
					Metadata:  map[string]string{"alert_type": alertType, "severity": severity},
					Timestamp: time.Now(),
				})
			})
		}
		logging.WithComponent("start").Info("budget enforcement enabled",
			slog.Float64("daily_limit", cfg.Budget.DailyLimit),
			slog.Float64("monthly_limit", cfg.Budget.MonthlyLimit),
		)

		// GH-539: Wire per-task token/duration limits into executor stream
		maxTokens, maxDuration := enforcer.GetPerTaskLimits()
		if maxTokens > 0 || maxDuration > 0 {
			var taskLimiters sync.Map // map[taskID]*budget.TaskLimiter
			runner.SetTokenLimitCheck(func(taskID string, deltaInput, deltaOutput int64) bool {
				// Get or create limiter for this task
				val, _ := taskLimiters.LoadOrStore(taskID, budget.NewTaskLimiter(maxTokens, maxDuration))
				limiter := val.(*budget.TaskLimiter)

				// Feed token deltas into the limiter
				totalDelta := deltaInput + deltaOutput
				if totalDelta > 0 {
					if !limiter.AddTokens(totalDelta) {
						return false
					}
				}

				// Also check duration on every event
				if !limiter.CheckDuration() {
					return false
				}

				return true
			})
			logging.WithComponent("start").Info("per-task budget limits enabled",
				slog.Int64("max_tokens", maxTokens),
				slog.Duration("max_duration", maxDuration),
			)
		}

		if !dashboardMode {
			fmt.Printf("💰 Budget enforcement enabled: $%.2f/day, $%.2f/month\n",
				cfg.Budget.DailyLimit, cfg.Budget.MonthlyLimit)
		}
	} else {
		// GH-1019: Log why budget is disabled for debugging
		logging.WithComponent("start").Debug("budget enforcement disabled",
			slog.Bool("config_nil", cfg.Budget == nil),
			slog.Bool("enabled", cfg.Budget != nil && cfg.Budget.Enabled),
			slog.Bool("store_nil", store == nil),
		)
	}

	// GH-929: Start GitHub polling for multiple repos if enabled
	var ghPollers []*github.Poller
	polledRepos := make(map[string]bool) // Track repos already polled to avoid duplicates

	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
		cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {

		token := cfg.Adapters.GitHub.Token
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}

		if token != "" {
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

			modeStr := "sequential"
			if execMode == github.ExecutionModeParallel {
				modeStr = "parallel"
			}

			// Helper to create poller for a repo with its project path
			createPollerForRepo := func(repoFullName, projPath string) (*github.Poller, error) {
				repoParts := strings.Split(repoFullName, "/")
				if len(repoParts) != 2 {
					return nil, fmt.Errorf("invalid repo format: %s", repoFullName)
				}
				repoOwner, repoName := repoParts[0], repoParts[1]

				// GH-386: Validate repo/project match at startup
				if err := executor.ValidateRepoProjectMatch(repoFullName, projPath); err != nil {
					logging.WithComponent("github").Warn("repo/project mismatch detected",
						slog.String("repo", repoFullName),
						slog.String("project_path", projPath),
						slog.String("expected_project", executor.ExtractRepoName(repoFullName)),
					)
				}

				var pollerOpts []github.PollerOption

				// Wire autopilot callback to the correct controller for this repo
				controller := autopilotControllers[repoFullName]
				if controller != nil {
					pollerOpts = append(pollerOpts,
						github.WithOnPRCreated(controller.OnPRCreated),
					)
				}

				// GH-726: Wire processed issue persistence
				if autopilotStateStore != nil {
					pollerOpts = append(pollerOpts, github.WithProcessedStore(autopilotStateStore))
				}

				// Capture variables for closures
				sourceRepo := repoFullName
				projPathCapture := projPath
				controllerCapture := controller

				// Create rate limit retry scheduler for this repo
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
						slog.String("repo", sourceRepo),
						slog.Int("issue", issueNum),
						slog.Int("attempt", pendingTask.Attempts),
					)

					result, err := handleGitHubIssueWithResult(retryCtx, cfg, client, issue, projPathCapture, sourceRepo, dispatcher, runner, monitor, program, alertsEngine, enforcer)

					if result != nil && result.PRNumber > 0 && controllerCapture != nil {
						controllerCapture.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName)
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
					logging.WithComponent("start").Warn("Failed to start rate limit scheduler",
						slog.String("repo", repoFullName),
						slog.Any("error", err))
				}

				// Configure based on execution mode
				if execMode == github.ExecutionModeSequential {
					pollerOpts = append(pollerOpts,
						github.WithExecutionMode(github.ExecutionModeSequential),
						github.WithSequentialConfig(waitForMerge, pollInterval, prTimeout),
						github.WithScheduler(rateLimitScheduler),
						github.WithOnIssueWithResult(func(issueCtx context.Context, issue *github.Issue) (*github.IssueResult, error) {
							return handleGitHubIssueWithResult(issueCtx, cfg, client, issue, projPathCapture, sourceRepo, dispatcher, runner, monitor, program, alertsEngine, enforcer)
						}),
					)
				} else {
					pollerOpts = append(pollerOpts,
						github.WithExecutionMode(github.ExecutionModeParallel),
						github.WithScheduler(rateLimitScheduler),
						github.WithMaxConcurrent(cfg.Orchestrator.MaxConcurrent),
						github.WithOnIssueWithResult(func(issueCtx context.Context, issue *github.Issue) (*github.IssueResult, error) {
							return handleGitHubIssueWithResult(issueCtx, cfg, client, issue, projPathCapture, sourceRepo, dispatcher, runner, monitor, program, alertsEngine, enforcer)
						}),
					)
				}

				return github.NewPoller(client, repoFullName, label, interval, pollerOpts...)
			}

			// Create poller for default repo (adapters.github.repo)
			if cfg.Adapters.GitHub.Repo != "" {
				polledRepos[cfg.Adapters.GitHub.Repo] = true
				poller, err := createPollerForRepo(cfg.Adapters.GitHub.Repo, projectPath)
				if err != nil {
					if !dashboardMode {
						fmt.Printf("⚠️  GitHub polling disabled for %s: %v\n", cfg.Adapters.GitHub.Repo, err)
					}
				} else {
					ghPollers = append(ghPollers, poller)
					if !dashboardMode {
						fmt.Printf("🐙 GitHub polling enabled: %s (every %s, mode: %s)\n", cfg.Adapters.GitHub.Repo, interval, modeStr)
					}
				}
			}

			// GH-929: Create pollers for each project with GitHub config
			for _, proj := range cfg.Projects {
				if proj.GitHub == nil || proj.GitHub.Owner == "" || proj.GitHub.Repo == "" {
					continue
				}
				repoFullName := fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
				if polledRepos[repoFullName] {
					continue // Skip duplicates
				}
				polledRepos[repoFullName] = true

				projPath := proj.Path
				if projPath == "" {
					projPath = projectPath // Fall back to default project path
				}

				poller, err := createPollerForRepo(repoFullName, projPath)
				if err != nil {
					logging.WithComponent("github").Warn("Failed to create poller for project",
						slog.String("project", proj.Name),
						slog.String("repo", repoFullName),
						slog.Any("error", err))
					continue
				}
				ghPollers = append(ghPollers, poller)
				if !dashboardMode {
					fmt.Printf("🐙 GitHub polling enabled: %s (project: %s, every %s, mode: %s)\n", repoFullName, proj.Name, interval, modeStr)
				}
			}

			// Start all pollers
			for _, poller := range ghPollers {
				go poller.Start(ctx)
			}

			if len(ghPollers) > 0 {
				if !dashboardMode && execMode == github.ExecutionModeSequential && waitForMerge {
					fmt.Printf("   ⏳ Sequential mode: waiting for PR merge before next issue (timeout: %s)\n", prTimeout)
				}

				// Start autopilot processing loops for all controllers
				for repoName, controller := range autopilotControllers {
					// Scan for existing PRs
					if err := controller.ScanExistingPRs(ctx); err != nil {
						logging.WithComponent("autopilot").Warn("failed to scan existing PRs",
							slog.String("repo", repoName),
							slog.Any("error", err),
						)
					}

					// Scan for recently merged PRs (GH-416)
					if err := controller.ScanRecentlyMergedPRs(ctx); err != nil {
						logging.WithComponent("autopilot").Warn("failed to scan merged PRs",
							slog.String("repo", repoName),
							slog.Any("error", err),
						)
					}

					// Start controller run loop
					go func(c *autopilot.Controller, repo string) {
						if err := c.Run(ctx); err != nil && err != context.Canceled {
							logging.WithComponent("autopilot").Error("autopilot controller stopped",
								slog.String("repo", repo),
								slog.Any("error", err),
							)
						}
					}(controller, repoName)
				}

				if len(autopilotControllers) > 0 && !dashboardMode {
					fmt.Printf("🤖 Autopilot enabled: %s environment (%d repos)\n", cfg.Orchestrator.Autopilot.Environment, len(autopilotControllers))
				}

				// Start metrics alerter for default controller (GH-728)
				if alertsEngine != nil && autopilotController != nil {
					metricsAlerter := autopilot.NewMetricsAlerter(autopilotController, alertsEngine)
					go metricsAlerter.Run(ctx)
				}

				// Start metrics persister for default controller (GH-728)
				if store != nil && autopilotController != nil {
					metricsPersister := autopilot.NewMetricsPersister(autopilotController, store)
					go metricsPersister.Run(ctx)
				}

				// Wire sub-issue PR callback for default controller (GH-594)
				if autopilotController != nil {
					runner.SetOnSubIssuePRCreated(autopilotController.OnPRCreated)
				}
			}

			// Start stale label cleanup for default repo if enabled
			if cfg.Adapters.GitHub.Repo != "" && cfg.Adapters.GitHub.StaleLabelCleanup != nil && cfg.Adapters.GitHub.StaleLabelCleanup.Enabled {
				if store != nil {
					cleanerOpts := []github.CleanerOption{}
					// Wire callback to clear processed map when pilot-failed labels are removed
					if len(ghPollers) > 0 {
						cleanerOpts = append(cleanerOpts, github.WithOnFailedCleaned(func(issueNumber int) {
							for _, p := range ghPollers {
								p.ClearProcessed(issueNumber)
							}
						}))
					}
					cleaner, cleanerErr := github.NewCleaner(client, store, cfg.Adapters.GitHub.Repo, cfg.Adapters.GitHub.StaleLabelCleanup, cleanerOpts...)
					if cleanerErr != nil {
						if !dashboardMode {
							fmt.Printf("⚠️  Stale label cleanup disabled: %v\n", cleanerErr)
						}
					} else {
						if !dashboardMode {
							fmt.Printf("🧹 Stale label cleanup enabled (every %s, in-progress: %s, failed: %s)\n",
								cfg.Adapters.GitHub.StaleLabelCleanup.Interval,
								cfg.Adapters.GitHub.StaleLabelCleanup.Threshold,
								cfg.Adapters.GitHub.StaleLabelCleanup.FailedThreshold)
						}
						go cleaner.Start(ctx)
					}
				}
			}
		}
	}

	// Start Linear polling if enabled (GH-393)
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
		cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled {

		workspaces := cfg.Adapters.Linear.GetWorkspaces()
		for _, ws := range workspaces {
			// Determine interval: workspace override > global > default
			interval := 30 * time.Second
			if ws.Polling != nil && ws.Polling.Interval > 0 {
				interval = ws.Polling.Interval
			} else if cfg.Adapters.Linear.Polling.Interval > 0 {
				interval = cfg.Adapters.Linear.Polling.Interval
			}

			// Check if workspace polling is explicitly disabled
			if ws.Polling != nil && !ws.Polling.Enabled {
				continue
			}

			linearClient := linear.NewClient(ws.APIKey)
			// Capture workspace config for per-issue project resolution (GH-1348)
			wsConfig := ws

			// Build poller options
			linearPollerOpts := []linear.PollerOption{
				linear.WithOnLinearIssue(func(issueCtx context.Context, issue *linear.Issue) (*linear.IssueResult, error) {
					// GH-1348: Resolve project path per-issue using workspace→project mapping
					issueProjectPath := projectPath // fallback to default
					pilotProject := wsConfig.ResolvePilotProject(issue)
					if pilotProject != "" {
						if proj := cfg.GetProjectByName(pilotProject); proj != nil {
							issueProjectPath = proj.Path
						}
					}
					result, err := handleLinearIssueWithResult(issueCtx, cfg, linearClient, issue, issueProjectPath, dispatcher, runner, monitor, program, alertsEngine, enforcer)

					// GH-1361: Wire PR to autopilot for CI monitoring + auto-merge
					if result != nil && result.PRNumber > 0 {
						// Resolve which autopilot controller to use based on project
						if pilotProject != "" {
							if proj := cfg.GetProjectByName(pilotProject); proj != nil && proj.GitHub != nil {
								repoFullName := fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
								if controller, ok := autopilotControllers[repoFullName]; ok && controller != nil {
									// issueNumber=0 because Linear issues don't have GitHub issue numbers
									controller.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
								}
							}
						}
					}

					return result, err
				}),
			}

			// GH-1351: Wire processed issue persistence to prevent re-dispatch after hot upgrade
			if autopilotStateStore != nil {
				linearPollerOpts = append(linearPollerOpts, linear.WithProcessedStore(autopilotStateStore))
			}

			linearPoller := linear.NewPoller(linearClient, ws, interval, linearPollerOpts...)

			if !dashboardMode {
				fmt.Printf("📊 Linear polling enabled: %s/%s (every %s)\n", ws.Name, ws.TeamID, interval)
			}
			go func(p *linear.Poller, name string) {
				if err := p.Start(ctx); err != nil {
					logging.WithComponent("linear").Error("Linear poller failed",
						slog.String("workspace", name),
						slog.Any("error", err),
					)
				}
			}(linearPoller, ws.Name)
		}
	}

	// Start Asana polling if enabled (GH-906)
	if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled &&
		cfg.Adapters.Asana.Polling != nil && cfg.Adapters.Asana.Polling.Enabled {

		// Determine interval
		interval := 30 * time.Second
		if cfg.Adapters.Asana.Polling.Interval > 0 {
			interval = cfg.Adapters.Asana.Polling.Interval
		}

		asanaClient := asana.NewClient(
			cfg.Adapters.Asana.AccessToken,
			cfg.Adapters.Asana.WorkspaceID,
		)
		asanaPoller := asana.NewPoller(asanaClient, cfg.Adapters.Asana, interval,
			asana.WithOnAsanaTask(func(taskCtx context.Context, task *asana.Task) (*asana.TaskResult, error) {
				result, err := handleAsanaTaskWithResult(taskCtx, cfg, asanaClient, task, projectPath, dispatcher, runner, monitor, program, alertsEngine, enforcer)

				// GH-1399: Wire PR to autopilot for CI monitoring + auto-merge
				if result != nil && result.PRNumber > 0 && autopilotController != nil {
					// issueNumber=0 because Asana tasks don't have GitHub issue numbers
					autopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
				}

				return result, err
			}),
		)

		if !dashboardMode {
			fmt.Printf("📦 Asana polling enabled: workspace %s (every %s)\n", cfg.Adapters.Asana.WorkspaceID, interval)
		}
		go func(p *asana.Poller) {
			if err := p.Start(ctx); err != nil {
				logging.WithComponent("asana").Error("Asana poller failed",
					slog.Any("error", err),
				)
			}
		}(asanaPoller)
	}

	// Start Telegram polling if enabled
	if tgHandler != nil {
		if !dashboardMode {
			fmt.Println("📱 Telegram polling started")
		}
		tgHandler.StartPolling(ctx)
	}

	// Start Slack Socket Mode if enabled (GH-652: wire into polling mode)
	var slackHandler *slack.Handler
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.SocketMode &&
		cfg.Adapters.Slack.AppToken != "" && cfg.Adapters.Slack.BotToken != "" {
		slackHandler = slack.NewHandler(&slack.HandlerConfig{
			AppToken:        cfg.Adapters.Slack.AppToken,
			BotToken:        cfg.Adapters.Slack.BotToken,
			ProjectPath:     projectPath,
			Projects:        config.NewSlackProjectSource(cfg),
			AllowedChannels: cfg.Adapters.Slack.AllowedChannels,
			AllowedUsers:    cfg.Adapters.Slack.AllowedUsers,
			MemberResolver:  teamAdapter,
		}, runner)

		go func() {
			if err := slackHandler.StartListening(ctx); err != nil {
				logging.WithComponent("slack").Error("Slack Socket Mode error", slog.Any("error", err))
			}
		}()

		if !dashboardMode {
			fmt.Println("💬 Slack Socket Mode started")
		}
		logging.WithComponent("start").Info("Slack Socket Mode started in polling mode")
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
			briefScheduler = briefs.NewScheduler(generator, delivery, briefsConfig, slog.Default(), store)
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

					// Drain pollers — stop accepting new issues before upgrade
					program.Send(dashboard.AddLog("⏳ Draining pollers — no new issues will be accepted...")())
					for _, p := range ghPollers {
						go p.Drain()
					}

					// Perform hot upgrade with monitor as TaskChecker
					// Monitor tracks running/queued tasks; upgrade waits for them to finish
					hotUpgrader, err := upgrade.NewHotUpgrader(version, monitor)
					if err != nil {
						program.Send(dashboard.NotifyUpgradeComplete(false, err.Error())())
						program.Send(dashboard.AddLog(fmt.Sprintf("❌ Upgrade failed: %v", err))())
						continue
					}

					upgradeCfg := &upgrade.HotUpgradeConfig{
						WaitForTasks: true,
						TaskTimeout:  30 * time.Minute,
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
					} else {
						// On Unix, process is replaced and this line is never reached.
						// On Windows, hot restart is not supported — binary is installed
						// but process continues. Notify user to restart manually.
						program.Send(dashboard.NotifyUpgradeComplete(true, "")())
						program.Send(dashboard.AddLog("✅ Upgrade installed! Please restart Pilot to use the new version.")())
					}
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
			hasLinearPolling := cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled
			if hasLinearPolling {
				workspaces := cfg.Adapters.Linear.GetWorkspaces()
				for _, ws := range workspaces {
					program.Send(dashboard.AddLog(fmt.Sprintf("📊 Linear polling: %s/%s", ws.Name, ws.TeamID))())
				}
			}

			// Check for restart marker (set by hot upgrade)
			// GH-879: Config is automatically reloaded because syscall.Exec starts a fresh process
			if os.Getenv("PILOT_RESTARTED") == "1" {
				prevVersion := os.Getenv("PILOT_PREVIOUS_VERSION")
				if prevVersion != "" {
					program.Send(dashboard.AddLog(fmt.Sprintf("✅ Upgraded from %s to %s (config reloaded)", prevVersion, version))())
				} else {
					program.Send(dashboard.AddLog("✅ Pilot restarted (config reloaded)")())
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

		// Terminate all running subprocesses (GH-883)
		runner.CancelAll()

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

	// Terminate all running subprocesses (GH-883)
	runner.CancelAll()

	if tgHandler != nil {
		tgHandler.Stop()
	}
	if len(ghPollers) > 0 {
		fmt.Printf("🐙 Stopping GitHub pollers (%d)...\n", len(ghPollers))
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


