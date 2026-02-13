// Dashboard progress test - GH-151
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/pilot"
	"github.com/alekspetrov/pilot/internal/quality"
	"github.com/alekspetrov/pilot/internal/replay"
	"github.com/alekspetrov/pilot/internal/teams"
	"github.com/alekspetrov/pilot/internal/tunnel"
	"github.com/alekspetrov/pilot/internal/upgrade"
)

var (
	version     = "0.3.0"
	buildTime   = "unknown"
	cfgFile     string
	teamAdapter *teams.ServiceAdapter // Global team adapter for RBAC lookups (GH-634)
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

// wireProjectAccessChecker creates and wires a team-based project access checker on the runner (GH-635).
// It opens the teams DB, resolves the configured member, and returns a cleanup function.
// Returns nil cleanup if team config is absent or disabled.
func wireProjectAccessChecker(runner *executor.Runner, cfg *config.Config) func() {
	if cfg.Team == nil || !cfg.Team.Enabled {
		return nil
	}

	if cfg.Team.TeamID == "" || cfg.Team.MemberEmail == "" {
		logging.WithComponent("teams").Warn("team config enabled but team_id or member_email not set, skipping project access check")
		return nil
	}

	if cfg.Memory == nil || cfg.Memory.Path == "" {
		logging.WithComponent("teams").Warn("memory path not configured, skipping project access check")
		return nil
	}

	// Open teams DB (same pilot.db used by memory store)
	dbPath := cfg.Memory.Path + "/pilot.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		logging.WithComponent("teams").Warn("failed to open teams DB", slog.Any("error", err))
		return nil
	}

	store, err := teams.NewStore(db)
	if err != nil {
		_ = db.Close()
		logging.WithComponent("teams").Warn("failed to create teams store", slog.Any("error", err))
		return nil
	}

	service := teams.NewService(store)

	// Resolve team
	team, err := service.GetTeamByName(cfg.Team.TeamID)
	if err != nil || team == nil {
		// Try by ID
		team, err = service.GetTeam(cfg.Team.TeamID)
	}
	if err != nil || team == nil {
		_ = db.Close()
		logging.WithComponent("teams").Warn("team not found, skipping project access check",
			slog.String("team", cfg.Team.TeamID))
		return nil
	}

	// Resolve member
	member, err := service.GetMemberByEmail(team.ID, cfg.Team.MemberEmail)
	if err != nil || member == nil {
		_ = db.Close()
		logging.WithComponent("teams").Warn("member not found in team, skipping project access check",
			slog.String("email", cfg.Team.MemberEmail),
			slog.String("team", team.Name))
		return nil
	}

	// Wire the checker via ServiceAdapter (GH-634 TeamChecker interface)
	adapter := teams.NewServiceAdapter(service)
	runner.SetTeamChecker(adapter)

	logging.WithComponent("teams").Info("project access checker enabled",
		slog.String("team", team.Name),
		slog.String("member", member.Email),
		slog.String("role", string(member.Role)))

	return func() { _ = db.Close() }
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
		newAutopilotCmd(),
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
		autopilotEnv string // Autopilot environment: dev, stage, prod
		autoRelease  bool   // Enable auto-release after PR merge
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
					linearPoller := linear.NewPoller(linearClient, ws, interval,
						linear.WithOnLinearIssue(func(issueCtx context.Context, issue *linear.Issue) (*linear.IssueResult, error) {
							return handleLinearIssueWithResult(issueCtx, cfg, linearClient, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
						}),
					)

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
						return handleJiraIssueWithResult(issueCtx, cfg, jiraClient, issue, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
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
						return handleAsanaTaskWithResult(taskCtx, cfg, asanaClient, task, projectPath, gwDispatcher, gwRunner, gwMonitor, gwProgram, gwAlertsEngine, gwEnforcer)
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
	cmd.Flags().StringVar(&autopilotEnv, "autopilot", "",
		"Enable autopilot mode: dev (auto-merge), stage (CI gate), prod (approval gate)")
	cmd.Flags().BoolVar(&autoRelease, "auto-release", false,
		"Enable automatic release creation after PR merge")

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

// runPollingMode runs lightweight polling-only mode (no HTTP gateway)
func runPollingMode(cfg *config.Config, projectPath string, replace, dashboardMode bool) error {
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
			linearPoller := linear.NewPoller(linearClient, ws, interval,
				linear.WithOnLinearIssue(func(issueCtx context.Context, issue *linear.Issue) (*linear.IssueResult, error) {
					return handleLinearIssueWithResult(issueCtx, cfg, linearClient, issue, projectPath, dispatcher, runner, monitor, program, alertsEngine, enforcer)
				}),
			)

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
				return handleAsanaTaskWithResult(taskCtx, cfg, asanaClient, task, projectPath, dispatcher, runner, monitor, program, alertsEngine, enforcer)
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

// parseAutopilotBranch extracts the target branch from an autopilot-fix issue's metadata comment.
// Returns empty string if no metadata found.
func parseAutopilotBranch(body string) string {
	re := regexp.MustCompile(`<!-- autopilot-meta branch:(\S+) -->`)
	if m := re.FindStringSubmatch(body); len(m) > 1 {
		return m[1]
	}
	return ""
}

// resolveGitHubMemberID maps a GitHub issue author to a team member ID (GH-634).
// Uses the global teamAdapter (set at startup). Returns "" if no adapter is configured
// or no matching member is found — callers treat "" as "skip RBAC".
func resolveGitHubMemberID(issue *github.Issue) string {
	if teamAdapter == nil {
		return ""
	}
	memberID, err := teamAdapter.ResolveGitHubIdentity(issue.User.Login, issue.User.Email)
	if err != nil {
		logging.WithComponent("teams").Warn("failed to resolve GitHub identity",
			slog.String("github_user", issue.User.Login),
			slog.Any("error", err),
		)
		return ""
	}
	if memberID != "" {
		logging.WithComponent("teams").Info("resolved GitHub user to team member",
			slog.String("github_user", issue.User.Login),
			slog.String("member_id", memberID),
		)
	}
	return memberID
}

// extractGitHubLabelNames returns label name strings from a GitHub issue (GH-727).
// Used to flow labels into executor.Task for decomposition/complexity decisions.
func extractGitHubLabelNames(issue *github.Issue) []string {
	if issue == nil || len(issue.Labels) == 0 {
		return nil
	}
	names := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		names[i] = l.Name
	}
	return names
}

// handleGitHubIssueWithResult processes a GitHub issue and returns result with PR info
// Used in sequential mode to enable PR merge waiting
// sourceRepo is the "owner/repo" string that the issue came from (GH-929)
func handleGitHubIssueWithResult(ctx context.Context, cfg *config.Config, client *github.Client, issue *github.Issue, projectPath string, sourceRepo string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*github.IssueResult, error) {
	taskID := fmt.Sprintf("GH-%d", issue.Number)

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

	// GH-539: Pre-execution budget check — block task if daily/monthly limits exceeded
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
				slog.String("action", string(checkResult.Action)),
			)

			// Emit budget exceeded alert
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: issue.Title,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}

			// Comment on the GitHub issue and label as failed
			parts := strings.Split(sourceRepo, "/")
			if len(parts) == 2 {
				comment := fmt.Sprintf("⛔ **Budget Limit Exceeded**\n\n%s\n\nDaily remaining: $%.2f\nMonthly remaining: $%.2f\n\nThis task has been skipped. Resume execution after adjusting budget limits or waiting for the next billing period.",
					checkResult.Reason, checkResult.DailyLeft, checkResult.MonthlyLeft)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
				if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
					logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
				}
			}

			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &github.IssueResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
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

	// GH-489: For autopilot-fix issues, reuse the original branch so the fix
	// lands on the same branch as the failed PR (not a new branch).
	for _, label := range issue.Labels {
		if label.Name == "autopilot-fix" {
			if parsed := parseAutopilotBranch(issue.Body); parsed != "" {
				branchName = parsed
				slog.Info("using original branch from autopilot-fix metadata",
					slog.String("branch", branchName),
					slog.Int("issue", issue.Number),
				)
			}
			break
		}
	}

	// Always create branches and PRs - required for autopilot workflow
	// GH-386: Include SourceRepo for cross-project validation in executor
	// GH-920: Extract acceptance criteria for prompt inclusion
	task := &executor.Task{
		ID:                 taskID,
		Title:              issue.Title,
		Description:        taskDesc,
		ProjectPath:        projectPath,
		Branch:             branchName,
		CreatePR:           true,
		SourceRepo:         sourceRepo,
		MemberID:           resolveGitHubMemberID(issue),           // GH-634: RBAC lookup
		Labels:             extractGitHubLabelNames(issue),         // GH-727: flow labels for complexity classifier
		AcceptanceCriteria: github.ExtractAcceptanceCriteria(issue.Body), // GH-920: acceptance criteria in prompts
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
		program.Send(dashboard.AddCompletedTask(taskID, issue.Title, status, duration, "", false)())
	}

	// Build the issue result
	issueResult := &github.IssueResult{
		Success:    execErr == nil && result != nil && result.Success,
		BranchName: branchName,
		Error:      execErr,
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
				// Close issue so dependent issues can proceed (GH-933)
				// Dependency resolution checks issue.State, not labels
				if err := client.UpdateIssueState(ctx, parts[0], parts[1], issue.Number, "closed"); err != nil {
					logGitHubAPIError("UpdateIssueState", parts[0], parts[1], issue.Number, err)
				}
				comment := buildExecutionComment(result, branchName)
				if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
					logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
				}
			}
		} else if result != nil {
			// result exists but Success is false - mark as failed
			if err := client.AddLabels(ctx, parts[0], parts[1], issue.Number, []string{github.LabelFailed}); err != nil {
				logGitHubAPIError("AddLabels", parts[0], parts[1], issue.Number, err)
			}
			comment := buildFailureComment(result)
			if _, err := client.AddComment(ctx, parts[0], parts[1], issue.Number, comment); err != nil {
				logGitHubAPIError("AddComment", parts[0], parts[1], issue.Number, err)
			}
		}
	}

	return issueResult, execErr
}

// handleLinearIssueWithResult processes a Linear issue picked up by the poller (GH-393)
func handleLinearIssueWithResult(ctx context.Context, cfg *config.Config, client *linear.Client, issue *linear.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*linear.IssueResult, error) {
	taskID := issue.Identifier // e.g., "APP-123"

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		issueURL := fmt.Sprintf("https://linear.app/issue/%s", issue.Identifier)
		monitor.Register(taskID, issue.Title, issueURL)
		monitor.Start(taskID)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📊 Linear Issue %s: %s", issue.Identifier, issue.Title))())
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

	// GH-539: Pre-execution budget check
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: issue.Title,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &linear.IssueResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📊 Linear Issue %s: %s\n", issue.Identifier, issue.Title)

	taskDesc := fmt.Sprintf("Linear Issue %s: %s\n\n%s", issue.Identifier, issue.Title, issue.Description)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	// GH-920: Extract acceptance criteria from Linear issue description
	task := &executor.Task{
		ID:                 taskID,
		Title:              issue.Title,
		Description:        taskDesc,
		ProjectPath:        projectPath,
		Branch:             branchName,
		CreatePR:           true,
		AcceptanceCriteria: github.ExtractAcceptanceCriteria(issue.Description),
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
		} else if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Title, status, duration, "", false)())
	}

	// Build issue result
	issueResult := &linear.IssueResult{
		Success: execErr == nil && result != nil && result.Success,
	}
	if result != nil {
		if result.PRUrl != "" {
			issueResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				issueResult.PRNumber = prNum
			}
		}
	}

	// Add comment to Linear issue
	if execErr != nil {
		comment := fmt.Sprintf("❌ Pilot execution failed:\n\n```\n%s\n```", execErr.Error())
		if err := client.AddComment(ctx, issue.ID, comment); err != nil {
			logging.WithComponent("linear").Warn("Failed to add comment",
				slog.String("issue", issue.Identifier),
				slog.Any("error", err),
			)
		}
	} else if result != nil && result.Success {
		// Validate deliverables before marking as done
		if result.CommitSHA == "" && result.PRUrl == "" {
			comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\n**Duration:** %s\n**Branch:** `%s`\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
				result.Duration, branchName)
			if err := client.AddComment(ctx, issue.ID, comment); err != nil {
				logging.WithComponent("linear").Warn("Failed to add comment",
					slog.String("issue", issue.Identifier),
					slog.Any("error", err),
				)
			}
			issueResult.Success = false
		} else {
			comment := buildExecutionComment(result, branchName)
			if err := client.AddComment(ctx, issue.ID, comment); err != nil {
				logging.WithComponent("linear").Warn("Failed to add comment",
					slog.String("issue", issue.Identifier),
					slog.Any("error", err),
				)
			}
		}
	} else if result != nil {
		comment := buildFailureComment(result)
		if err := client.AddComment(ctx, issue.ID, comment); err != nil {
			logging.WithComponent("linear").Warn("Failed to add comment",
				slog.String("issue", issue.Identifier),
				slog.Any("error", err),
			)
		}
	}

	return issueResult, execErr
}

// handleJiraIssueWithResult processes a Jira issue picked up by the poller (GH-905)
func handleJiraIssueWithResult(ctx context.Context, cfg *config.Config, client *jira.Client, issue *jira.Issue, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*jira.IssueResult, error) {
	taskID := issue.Key // e.g., "PROJ-123"

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		issueURL := fmt.Sprintf("%s/browse/%s", cfg.Adapters.Jira.BaseURL, issue.Key)
		monitor.Register(taskID, issue.Fields.Summary, issueURL)
		monitor.Start(taskID)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📊 Jira Issue %s: %s", issue.Key, issue.Fields.Summary))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: issue.Fields.Summary,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// GH-539: Pre-execution budget check
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: issue.Fields.Summary,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &jira.IssueResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📊 Jira Issue %s: %s\n", issue.Key, issue.Fields.Summary)

	taskDesc := fmt.Sprintf("Jira Issue %s: %s\n\n%s", issue.Key, issue.Fields.Summary, issue.Fields.Description)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	task := &executor.Task{
		ID:          taskID,
		Title:       issue.Fields.Summary,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
		CreatePR:    true,
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
				TaskTitle: issue.Fields.Summary,
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
				TaskTitle: issue.Fields.Summary,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: issue.Fields.Summary,
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
		} else if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, issue.Fields.Summary, status, duration, "", false)())
	}

	// Build issue result
	issueResult := &jira.IssueResult{
		Success: execErr == nil && result != nil && result.Success,
	}
	if result != nil {
		if result.PRUrl != "" {
			issueResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				issueResult.PRNumber = prNum
			}
		}
	}

	// Add comment to Jira issue
	if execErr != nil {
		comment := fmt.Sprintf("❌ Pilot execution failed:\n\n%s", execErr.Error())
		if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
			logging.WithComponent("jira").Warn("Failed to add comment",
				slog.String("issue", issue.Key),
				slog.Any("error", err),
			)
		}
	} else if result != nil && result.Success {
		// Validate deliverables before marking as done
		if result.CommitSHA == "" && result.PRUrl == "" {
			comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\nDuration: %s\nBranch: %s\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
				result.Duration, branchName)
			if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
				logging.WithComponent("jira").Warn("Failed to add comment",
					slog.String("issue", issue.Key),
					slog.Any("error", err),
				)
			}
			issueResult.Success = false
		} else {
			comment := buildJiraExecutionComment(result, branchName)
			if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
				logging.WithComponent("jira").Warn("Failed to add comment",
					slog.String("issue", issue.Key),
					slog.Any("error", err),
				)
			}
		}
	} else if result != nil {
		comment := buildJiraFailureComment(result)
		if _, err := client.AddComment(ctx, issue.Key, comment); err != nil {
			logging.WithComponent("jira").Warn("Failed to add comment",
				slog.String("issue", issue.Key),
				slog.Any("error", err),
			)
		}
	}

	return issueResult, execErr
}

// buildJiraExecutionComment creates a comment for successful Jira execution
func buildJiraExecutionComment(result *executor.ExecutionResult, branchName string) string {
	var parts []string
	parts = append(parts, "✅ Pilot execution completed successfully!")
	parts = append(parts, "")

	if result.PRUrl != "" {
		parts = append(parts, fmt.Sprintf("Pull Request: %s", result.PRUrl))
	}
	if result.CommitSHA != "" {
		parts = append(parts, fmt.Sprintf("Commit: %s", result.CommitSHA[:min(8, len(result.CommitSHA))]))
	}
	parts = append(parts, fmt.Sprintf("Branch: %s", branchName))
	parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))

	return strings.Join(parts, "\n")
}

// buildJiraFailureComment creates a comment for failed Jira execution
func buildJiraFailureComment(result *executor.ExecutionResult) string {
	var parts []string
	parts = append(parts, "❌ Pilot execution failed")
	parts = append(parts, "")
	if result.Error != "" {
		parts = append(parts, fmt.Sprintf("Error: %s", result.Error))
	}
	if result.Duration > 0 {
		parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))
	}
	return strings.Join(parts, "\n")
}

// handleAsanaTaskWithResult processes an Asana task picked up by the poller (GH-906)
func handleAsanaTaskWithResult(ctx context.Context, cfg *config.Config, client *asana.Client, task *asana.Task, projectPath string, dispatcher *executor.Dispatcher, runner *executor.Runner, monitor *executor.Monitor, program *tea.Program, alertsEngine *alerts.Engine, enforcer *budget.Enforcer) (*asana.TaskResult, error) {
	taskID := "ASANA-" + task.GID

	// Get task URL
	taskURL := task.Permalink
	if taskURL == "" {
		taskURL = "https://app.asana.com/0/0/" + task.GID
	}

	// Register task with monitor if in dashboard mode
	if monitor != nil {
		monitor.Register(taskID, task.Name, taskURL)
		monitor.Start(taskID)
	}
	if program != nil {
		program.Send(dashboard.AddLog(fmt.Sprintf("📦 Asana Task %s: %s", task.GID, task.Name))())
	}

	// Emit task started event (GH-337)
	if alertsEngine != nil {
		alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventTypeTaskStarted,
			TaskID:    taskID,
			TaskTitle: task.Name,
			Project:   projectPath,
			Timestamp: time.Now(),
		})
	}

	// GH-539: Pre-execution budget check
	if enforcer != nil {
		checkResult, budgetErr := enforcer.CheckBudget(ctx, "", "")
		if budgetErr != nil {
			logging.WithComponent("budget").Warn("budget check failed, allowing task (fail-open)",
				slog.String("task_id", taskID),
				slog.Any("error", budgetErr),
			)
		} else if !checkResult.Allowed {
			logging.WithComponent("budget").Warn("task blocked by budget enforcement",
				slog.String("task_id", taskID),
				slog.String("reason", checkResult.Reason),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeBudgetExceeded,
					TaskID:    taskID,
					TaskTitle: task.Name,
					Project:   projectPath,
					Error:     checkResult.Reason,
					Metadata: map[string]string{
						"daily_left":   fmt.Sprintf("%.2f", checkResult.DailyLeft),
						"monthly_left": fmt.Sprintf("%.2f", checkResult.MonthlyLeft),
						"action":       string(checkResult.Action),
					},
					Timestamp: time.Now(),
				})
			}
			budgetExceededErr := fmt.Errorf("budget enforcement: %s", checkResult.Reason)
			return &asana.TaskResult{
				Success: false,
				Error:   budgetExceededErr,
			}, budgetExceededErr
		}
	}

	fmt.Printf("\n📦 Asana Task %s: %s\n", task.GID, task.Name)

	taskDesc := fmt.Sprintf("Asana Task %s: %s\n\n%s", task.GID, task.Name, task.Notes)
	branchName := fmt.Sprintf("pilot/%s", taskID)

	execTask := &executor.Task{
		ID:          taskID,
		Title:       task.Name,
		Description: taskDesc,
		ProjectPath: projectPath,
		Branch:      branchName,
		CreatePR:    true,
	}

	var result *executor.ExecutionResult
	var execErr error

	if dispatcher != nil {
		execID, qErr := dispatcher.QueueTask(ctx, execTask)
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
					TaskID:    execTask.ID,
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
		result, execErr = runner.Execute(ctx, execTask)
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
				TaskTitle: task.Name,
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
				TaskTitle: task.Name,
				Project:   projectPath,
				Metadata:  metadata,
				Timestamp: time.Now(),
			})
		} else if result != nil {
			alertsEngine.ProcessEvent(alerts.Event{
				Type:      alerts.EventTypeTaskFailed,
				TaskID:    taskID,
				TaskTitle: task.Name,
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
		} else if result != nil {
			duration = result.Duration.String()
		}
		program.Send(dashboard.AddCompletedTask(taskID, task.Name, status, duration, "", false)())
	}

	// Build task result
	taskResult := &asana.TaskResult{
		Success: execErr == nil && result != nil && result.Success,
	}
	if result != nil {
		if result.PRUrl != "" {
			taskResult.PRURL = result.PRUrl
			if prNum, err := github.ExtractPRNumber(result.PRUrl); err == nil {
				taskResult.PRNumber = prNum
			}
		}
	}

	// Add comment to Asana task
	if execErr != nil {
		comment := fmt.Sprintf("❌ Pilot execution failed:\n\n%s", execErr.Error())
		if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
			logging.WithComponent("asana").Warn("Failed to add comment",
				slog.String("task", task.GID),
				slog.Any("error", err),
			)
		}
	} else if result != nil && result.Success {
		// Validate deliverables before marking as done
		if result.CommitSHA == "" && result.PRUrl == "" {
			comment := fmt.Sprintf("⚠️ Pilot execution completed but no changes were made.\n\nDuration: %s\nBranch: %s\n\nNo commits or PR were created. The task may need clarification or manual intervention.",
				result.Duration, branchName)
			if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
				logging.WithComponent("asana").Warn("Failed to add comment",
					slog.String("task", task.GID),
					slog.Any("error", err),
				)
			}
			taskResult.Success = false
		} else {
			comment := buildAsanaExecutionComment(result, branchName)
			if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
				logging.WithComponent("asana").Warn("Failed to add comment",
					slog.String("task", task.GID),
					slog.Any("error", err),
				)
			}
		}
	} else if result != nil {
		comment := buildAsanaFailureComment(result)
		if _, err := client.AddComment(ctx, task.GID, comment); err != nil {
			logging.WithComponent("asana").Warn("Failed to add comment",
				slog.String("task", task.GID),
				slog.Any("error", err),
			)
		}
	}

	return taskResult, execErr
}

// buildAsanaExecutionComment creates a comment for successful Asana execution
func buildAsanaExecutionComment(result *executor.ExecutionResult, branchName string) string {
	var parts []string
	parts = append(parts, "✅ Pilot execution completed successfully!")
	parts = append(parts, "")

	if result.PRUrl != "" {
		parts = append(parts, fmt.Sprintf("Pull Request: %s", result.PRUrl))
	}
	if result.CommitSHA != "" {
		parts = append(parts, fmt.Sprintf("Commit: %s", result.CommitSHA[:min(8, len(result.CommitSHA))]))
	}
	parts = append(parts, fmt.Sprintf("Branch: %s", branchName))
	parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))

	return strings.Join(parts, "\n")
}

// buildAsanaFailureComment creates a comment for failed Asana execution
func buildAsanaFailureComment(result *executor.ExecutionResult) string {
	var parts []string
	parts = append(parts, "❌ Pilot execution failed")
	parts = append(parts, "")
	if result.Error != "" {
		parts = append(parts, fmt.Sprintf("Error: %s", result.Error))
	}
	if result.Duration > 0 {
		parts = append(parts, fmt.Sprintf("Duration: %s", result.Duration))
	}
	return strings.Join(parts, "\n")
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

			// Success with deliverables - add done label
			if err := client.AddLabels(ctx, owner, repoName, int(issueNum), []string{"pilot-done"}); err != nil {
				logGitHubAPIError("AddLabels", owner, repoName, int(issueNum), err)
			}
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
	var displays []dashboard.TaskDisplay
	for _, state := range states {
		// Skip completed/failed tasks — they appear in HISTORY, not QUEUE
		if state.Status == executor.StatusCompleted || state.Status == executor.StatusFailed {
			continue
		}

		var status string
		switch state.Status {
		case executor.StatusRunning:
			status = "running"
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

func newAutopilotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autopilot",
		Short: "Autopilot commands for PR lifecycle management",
		Long:  `Commands for viewing and managing autopilot PR tracking and automation.`,
	}

	cmd.AddCommand(newAutopilotStatusCmd())
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
						"enabled":   autopilotCfg.Release != nil && autopilotCfg.Release.Enabled,
						"trigger":   func() string { if autopilotCfg.Release != nil { return autopilotCfg.Release.Trigger }; return "" }(),
						"requireCI": func() bool { if autopilotCfg.Release != nil { return autopilotCfg.Release.RequireCI }; return false }(),
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

// buildExecutionComment formats a rich markdown comment from execution results.
func buildExecutionComment(result *executor.ExecutionResult, branchName string) string {
	var sb strings.Builder

	sb.WriteString("✅ Pilot completed!\n\n")
	sb.WriteString("| Metric | Value |\n")
	sb.WriteString("|--------|-------|\n")

	// Duration (always present)
	sb.WriteString(fmt.Sprintf("| Duration | %s |\n", result.Duration.Round(time.Second)))

	// Model
	if result.ModelName != "" {
		sb.WriteString(fmt.Sprintf("| Model | `%s` |\n", result.ModelName))
	}

	// Tokens
	if result.TokensTotal > 0 {
		sb.WriteString(fmt.Sprintf("| Tokens | %s (↑%s ↓%s) |\n",
			formatTokenCountComment(result.TokensTotal),
			formatTokenCountComment(result.TokensInput),
			formatTokenCountComment(result.TokensOutput),
		))
	}

	// Cost
	if result.EstimatedCostUSD > 0 {
		sb.WriteString(fmt.Sprintf("| Cost | ~$%.2f |\n", result.EstimatedCostUSD))
	}

	// Files changed
	if result.FilesChanged > 0 || result.LinesAdded > 0 || result.LinesRemoved > 0 {
		sb.WriteString(fmt.Sprintf("| Files | %d changed (+%d -%d) |\n",
			result.FilesChanged, result.LinesAdded, result.LinesRemoved))
	}

	// Branch
	if branchName != "" {
		sb.WriteString(fmt.Sprintf("| Branch | `%s` |\n", branchName))
	}

	// PR
	if result.PRUrl != "" {
		sb.WriteString(fmt.Sprintf("| PR | %s |\n", result.PRUrl))
	}

	// Intent warning (from intent judge, GH-624)
	if result.IntentWarning != "" {
		sb.WriteString(fmt.Sprintf("\n⚠️ **Intent Warning:** %s\n", result.IntentWarning))
	}

	return sb.String()
}

// buildFailureComment formats a comment for failed executions.
func buildFailureComment(result *executor.ExecutionResult) string {
	var sb strings.Builder
	sb.WriteString("❌ Pilot execution failed\n\n")
	if result != nil && result.Error != "" {
		sb.WriteString("<details>\n<summary>Error details</summary>\n\n")
		sb.WriteString(fmt.Sprintf("```\n%s\n```\n", result.Error))
		sb.WriteString("</details>\n")
	}
	if result != nil {
		if result.Duration > 0 {
			sb.WriteString(fmt.Sprintf("\n**Duration:** %s", result.Duration.Round(time.Second)))
		}
		if result.ModelName != "" {
			sb.WriteString(fmt.Sprintf(" | **Model:** `%s`", result.ModelName))
		}
		if result.EstimatedCostUSD > 0 {
			sb.WriteString(fmt.Sprintf(" | **Cost:** ~$%.2f", result.EstimatedCostUSD))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// formatTokenCountComment formats a token count for display in comments.
func formatTokenCountComment(tokens int64) string {
	if tokens >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1000000)
	}
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}
