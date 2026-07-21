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
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/qf-studio/pilot/internal/adapterhealth"
	"github.com/qf-studio/pilot/internal/adapters/discord"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/banner"
	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/budget"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/dashboard"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/health"
	"github.com/qf-studio/pilot/internal/health/verify"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/pilot"
	"github.com/qf-studio/pilot/internal/singleton"
	"github.com/qf-studio/pilot/internal/teams"
	"github.com/qf-studio/pilot/internal/tunnel"
	"github.com/qf-studio/pilot/internal/upgrade"
	sdkCore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
	sdkSlack "github.com/qf-studio/studio-sdk/sdk/integrations/slack"
	sdkTelegram "github.com/qf-studio/studio-sdk/sdk/integrations/telegram"
)

var (
	version     = "1.0.0"
	buildTime   = "unknown"
	cfgFile     string
	teamAdapter *teams.ServiceAdapter // Global team adapter for RBAC lookups (GH-634)
)

var quietMode bool

// executionMode mirrors the execution-mode enum the (now-deleted, GH-4191)
// in-tree github.Poller used to expose. Kept locally since it now only drives
// the startup "sequential mode" display decision below — GitHub polling is
// SDK-only and the SDK adapter runs ExecutionModeAuto unconditionally.
type executionMode string

const (
	executionModeSequential executionMode = "sequential"
	executionModeParallel   executionMode = "parallel"
	executionModeAuto       executionMode = "auto"
)

// resolveExecutionMode maps the orchestrator.execution.mode config string to
// an executionMode. Empty and "auto" both resolve to executionModeAuto
// (parallel dispatch with scope-overlap guard), matching
// config.DefaultExecutionConfig(). Any other unrecognized value falls back to
// executionModeSequential.
func resolveExecutionMode(mode string) executionMode {
	switch mode {
	case "sequential":
		return executionModeSequential
	case "parallel":
		return executionModeParallel
	case "auto", "":
		return executionModeAuto
	default:
		return executionModeSequential
	}
}

// githubTokenSource names where a resolved GitHub token came from, so a dead
// token can be diagnosed without re-deriving the resolution chain (GH-3718).
type githubTokenSource string

const (
	githubTokenSourceConfig githubTokenSource = "config (adapters.github.token)"
	githubTokenSourceEnv    githubTokenSource = "env (GITHUB_TOKEN)"
	githubTokenSourceGhCLI  githubTokenSource = "gh CLI (gh auth token)"
	githubTokenSourceNone   githubTokenSource = "none"
)

// ghCLITokenCache memoizes the `gh auth token` fallback lookup for the process
// lifetime — it forks a subprocess, and the credential can't change mid-run.
// A pointer so tests can reset it by swapping in a fresh instance instead of
// copying a sync.Once by value.
type ghCLITokenCache struct {
	once  sync.Once
	token string
	ok    bool
}

func (c *ghCLITokenCache) resolve() (string, bool) {
	c.once.Do(func() {
		tok, err := ghAuthToken()
		if err == nil && tok != "" {
			c.token = tok
			c.ok = true
		}
	})
	return c.token, c.ok
}

var ghTokenCache = &ghCLITokenCache{}

// resolveGitHubToken resolves the GitHub token with precedence:
// adapters.github.token config → GITHUB_TOKEN env → `gh auth token` CLI
// fallback (GH-3718). It consolidates the pattern previously duplicated
// across five call-sites in this file. The returned source lets callers log
// which credential a startup 401 came from.
func resolveGitHubToken(cfg *config.Config) (string, githubTokenSource) {
	if cfg != nil && cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Token != "" {
		return cfg.Adapters.GitHub.Token, githubTokenSourceConfig
	}
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, githubTokenSourceEnv
	}
	if tok, ok := ghTokenCache.resolve(); ok {
		return tok, githubTokenSourceGhCLI
	}
	return "", githubTokenSourceNone
}

// validateGitHubToken makes one authenticated API call to confirm the
// resolved token actually works. A dead/expired token otherwise fails
// silently on every subsequent poll (live incident 2026-06-30) — this makes
// the failure loud at startup instead. Never returns an error: validation
// failure is logged (and alerted, if alertsEngine is configured) but must not
// block daemon startup, since other adapters may still work fine.
func validateGitHubToken(ctx context.Context, client *github.Client, source githubTokenSource, alertsEngine *alerts.Engine) {
	log := logging.WithComponent("github")
	if _, err := client.GetAuthenticatedUser(ctx); err != nil {
		var authErr *github.AuthError
		if errors.As(err, &authErr) {
			log.Error("GitHub token rejected by API (401) — polling and PR operations will silently fail until this is fixed",
				slog.String("token_source", string(source)),
				slog.String("fix", "rotate the token at its source and restart pilot"),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeConfigError,
					Error:     fmt.Sprintf("GitHub token (source: %s) is invalid or expired — 401 from GitHub API", source),
					Timestamp: time.Now(),
				})
			}
			return
		}
		// Network error, rate limit, etc. — not evidence the token itself is dead.
		log.Warn("could not verify GitHub token validity at startup",
			slog.String("token_source", string(source)),
			slog.String("error", err.Error()),
		)
		return
	}
	log.Info("GitHub token validated", slog.String("token_source", string(source)))
}

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
		newRestartCmd(),
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
		newTraceCmd(),
		newWebhooksCmd(),
		newUpgradeCmd(),
		newReleaseCmd(),
		newAllowCmd(),
		newProjectCmd(),
		newAutopilotCmd(),
		newOnboardCmd(),
		newBackendCmd(),
		newEvalCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newStartCmd() *cobra.Command {
	var (
		dashboardMode bool
		projectPath   string
		replace       bool
		// Input adapter flags (override config) - use bool with "changed" check
		enableTelegram bool
		enableGithub   bool
		enableLinear   bool
		enableSlack    bool
		enablePlane    bool
		enableDiscord  bool
		// Mode flags
		noGateway      bool   // Lightweight mode: polling only, no HTTP gateway
		sequential     bool   // Sequential execution mode (one issue at a time)
		envFlag        string // Environment name: dev, stage, prod, or custom configured name
		enableTunnel   bool   // Enable public tunnel (Cloudflare/ngrok)
		teamID         string // Optional team ID for scoping execution
		teamMember     string // Member email for project access scoping
		logFormat      string // Log output format: text or json (GH-847)
		dashboardScope string // Dashboard metrics scope: "project" (default) or "all" (GH-3534)
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

			// GH-2361: Fail loudly when an adapter flag is used but the
			// corresponding adapter block is missing from config. Previously,
			// `pilot start --github` would silently auto-enable a defaulted
			// adapter with no token/repo and poll nothing.
			if err := validateAdapterFlags(cfg, cmd); err != nil {
				return err
			}

			// Apply flag overrides to config
			applyInputOverrides(cfg, cmd, enableTelegram, enableGithub, enableLinear, enableSlack, enableTunnel, enablePlane, enableDiscord)

			// GH-3826: warn loudly when Telegram will send approval requests
			// but has no inbound polling to receive the approve/reject tap —
			// otherwise decisions silently strand until the approval stage
			// times out.
			if msg := health.TelegramApprovalStranding(cfg); msg != "" {
				logging.WithComponent("start").Error(msg,
					slog.Bool("telegram_polling", cfg.Adapters.Telegram.Polling))
				fmt.Fprintf(os.Stderr, "!  %s — run 'pilot doctor' for details, or set adapters.telegram.polling: true / start with --telegram\n", msg)
			}

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

			// GH-3600: in dashboard mode daemon logs must not hit the terminal,
			// but discarding them hid a failed hot restart entirely — redirect to
			// a rotating file instead (logging.dashboard_log; "off" = old discard
			// behavior). Must run BEFORE runner/gateway creation (GH-190/GH-351:
			// components cache their logger) and before the reconciliation below
			// so its outcome is durably logged.
			if dashboardMode {
				setupDashboardLogging(cfg)
			}

			// GH-3600: verify whether a pending upgrade actually took effect —
			// the running version vs the state file is the ground truth; the
			// PILOT_RESTARTED marker only tells how the restart happened.
			bootReconcile, _ := upgrade.ReconcileBootState(version, "")
			switch bootReconcile.Outcome {
			case upgrade.BootUpgradeVerified:
				via := "manual restart"
				if bootReconcile.HotExec {
					via = "hot restart"
				}
				logging.WithComponent("upgrade").Info("upgrade verified complete",
					"from", bootReconcile.PreviousVersion,
					"to", bootReconcile.NewVersion,
					"via", via)
			case upgrade.BootRestartFailed:
				logging.WithComponent("upgrade").Error("previous upgrade did NOT take effect — still running old version",
					"running", version,
					"expected", bootReconcile.NewVersion,
					"error", bootReconcile.RestartError)
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

			// Validate --dashboard-scope (GH-3534)
			if dashboardScope != "project" && dashboardScope != "all" {
				return fmt.Errorf("invalid --dashboard-scope %q: must be one of [project, all]", dashboardScope)
			}

			// Clean stale pilot hooks on startup (GH-1883)
			cleanStartupHooks(cfg, projectPath)

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
			// Splash screen removed — caused alt-screen flicker between
			// splash exit and dashboard start (GH-2459 follow-up).

			hasPollingAdapter := hasTelegram || hasGithubPolling
			if noGateway || hasPollingAdapter {
				return runPollingMode(cmd, cfg, projectPath, replace, dashboardMode, noGateway, bootReconcile)
			}

			// Full daemon mode with gateway
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			// Dashboard-mode log redirect already happened above (GH-3600),
			// before initialization (GH-351 ordering preserved).

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
			// GH-2232: Check if any adapter-registry poller is enabled (GitLab, Linear, Jira, etc.)
			adapterPollerEnabled := false
			for _, reg := range adapterPollerRegistrations() {
				if reg.Enabled(cfg) {
					adapterPollerEnabled = true
					break
				}
			}
			needsPollingInfra := (telegramFlagSet && hasTelegram && cfg.Adapters.Telegram.Polling) ||
				(githubFlagSet && hasGithubPolling && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
					cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled) ||
				(slackFlagSet && hasSlack) ||
				adapterPollerEnabled

			// Shared infrastructure for polling adapters
			var gwRunner *executor.Runner
			var gwStore *memory.Store
			var gwDispatcher *executor.Dispatcher
			var gwMonitor *executor.Monitor
			var gwProgram *tea.Program
			var gwAutopilotController *autopilot.Controller
			var gwAutopilotStateStore *autopilot.StateStore
			var gwAlertsEngine *alerts.Engine
			var gwTgApprovalHandler *approval.TelegramHandler
			var gwSlackApprovalHandler *approval.SlackHandler

			if needsPollingInfra {
				// Create shared runner with config (GH-956: enables worktree isolation)
				var runnerErr error
				gwRunner, runnerErr = executor.NewRunnerWithConfig(cfg.Executor)
				if runnerErr != nil {
					return fmt.Errorf("failed to create executor runner: %w", runnerErr)
				}
				// TASK-286 / GH-3027: refuse sub-issue creation on unmanaged repos.
				gwRunner.SetRepoAllowlist(newConfigRepoAllowlist(cfg))

				// Set up quality gates on runner (GH-3716: resolved per-project,
				// falling back to the global config, then auto-detection).
				gwRunner.SetQualityCheckerFactory(newProjectQualityCheckerFactory(cfg))

				// Set up team project access checker if configured (GH-635)
				if gwTeamCleanup := wireProjectAccessChecker(gwRunner, cfg); gwTeamCleanup != nil {
					defer gwTeamCleanup()
				}

				// GH-962: Clean up orphaned worktree directories from previous crashed executions
				if cfg.Executor != nil && cfg.Executor.UseWorktree {
					removed, freedBytes, err := executor.CleanupOrphanedWorktrees(context.Background(), projectPath)
					if err != nil {
						// Real failure — don't fail startup, this is best-effort cleanup.
						logging.WithComponent("start").Warn("worktree cleanup error", slog.String("error", err.Error()))
					} else if removed > 0 {
						logging.WithComponent("start").Info("worktree cleanup completed",
							slog.Int("removed", removed),
							slog.String("freed_mb", fmt.Sprintf("%.1f", float64(freedBytes)/(1024*1024))))
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
					if dispErr := gwDispatcher.Start(context.Background()); dispErr != nil {
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
				if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" &&
					(cfg.Adapters.Telegram.Approval == nil || cfg.Adapters.Telegram.Approval.Enabled) {
					tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
					gwTgApprovalHandler = approval.NewTelegramHandler(&telegramApprovalAdapter{client: tgClient}, cfg.Adapters.Telegram.ChatID)
					// GH-3825: persist decisions directly to PRState via the manager so a
					// button tap on a Rehydrate-restored request isn't lost when no
					// waiter goroutine survived the restart.
					gwTgApprovalHandler.WithDecisionRecorder(approvalMgr)
					if gwStore != nil {
						gwTgApprovalHandler.WithStore(gwStore)
						if rErr := gwTgApprovalHandler.Rehydrate(context.Background()); rErr != nil {
							logging.WithComponent("approval").Warn("telegram approval rehydrate failed", slog.Any("error", rErr))
						}
					}
					approvalMgr.RegisterHandler(gwTgApprovalHandler)
					// GH-3825: prune requests that expired while the daemon was
					// down (or with no in-process waiter) instead of leaving them
					// pending forever.
					startApprovalExpirySweep(context.Background(), gwTgApprovalHandler)
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
						gwSlackApprovalHandler = approval.NewSlackHandler(&slackApprovalClientAdapter{adapter: slackAdapter}, slackChannel)
						// GH-4411: persist decisions directly to PRState via the manager so a
						// button click on a Rehydrate-restored request isn't lost when no
						// waiter goroutine survived the restart (mirrors GH-3825's Telegram fix).
						gwSlackApprovalHandler.WithDecisionRecorder(approvalMgr)
						if gwStore != nil {
							gwSlackApprovalHandler.WithStore(gwStore)
							if rErr := gwSlackApprovalHandler.Rehydrate(context.Background()); rErr != nil {
								logging.WithComponent("approval").Warn("slack approval rehydrate failed", slog.Any("error", rErr))
							}
						}
						approvalMgr.RegisterHandler(gwSlackApprovalHandler)
						// GH-4411: prune requests that expired while the daemon was
						// down (or with no in-process waiter) instead of leaving them
						// pending forever.
						startApprovalExpirySweep(context.Background(), gwSlackApprovalHandler)
					}
				}

				// Create autopilot controller if enabled
				if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.Enabled {
					ghToken, _ := resolveGitHubToken(cfg)
					if ghToken != "" && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
						parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
						if len(parts) == 2 {
							ghClient := github.NewClient(ghToken)

							// Register GitHub approval handler if enabled
							if cfg.Adapters.GitHub.Approval != nil && cfg.Adapters.GitHub.Approval.Enabled {
								pollInterval := cfg.Adapters.GitHub.Approval.PollInterval
								if pollInterval == 0 {
									pollInterval = 30 * time.Second
								}
								ghApprovalHandler := approval.NewGitHubHandler(ghClient, &approval.GitHubHandlerConfig{
									Owner: parts[0], Repo: parts[1], PollInterval: pollInterval,
								})
								approvalMgr.RegisterHandler(ghApprovalHandler)
							}

							// M7 4d.1: autopilot consumes the studio-sdk client; the in-tree
							// ghClient stays for the legacy poller/webhook until later phases.
							apGHClient := githubSDK.NewClient(ghToken)

							// GH-1870: Board sync option for gateway autopilot controller.
							var gwBoardOpts []autopilot.ControllerOption
							// GH-4460: the in-tree client exposes the jobs/annotations APIs the
							// studio-sdk client doesn't yet — wire it so CI-failure excerpts
							// resolve to the actual failing step instead of a whole-job tail.
							gwBoardOpts = append(gwBoardOpts, autopilot.WithStepLogClient(ghClient))
							// GH-4454: match the polling-path pilot-label wiring so the
							// lane-starvation reconciler searches the same trigger label
							// the webhook/legacy poller watches for.
							if cfg.Adapters.GitHub.PilotLabel != "" {
								gwBoardOpts = append(gwBoardOpts, autopilot.WithPilotLabel(cfg.Adapters.GitHub.PilotLabel))
							}
							// GH-4472: resolve via project override → default-repo fallback
							// instead of reading the global block directly, so a projects[]
							// entry for this same repo with its own project_board wins.
							gwBoardOpts = append(gwBoardOpts, projectBoardControllerOpts(apGHClient, cfg, cfg.Adapters.GitHub.Repo, parts[0], true)...)
							// TASK-352: scope self-heal to the project's fs path (matches
							// executions.project_path) so merged work flips failed→completed.
							gwBoardOpts = append(gwBoardOpts, autopilot.WithProjectPath(projectPath))
							// GH-3931: apply the per-project release overlay (GH-3930) when configured.
							if proj := cfg.FindProjectByRepo(cfg.Adapters.GitHub.Repo); proj != nil && proj.Release != nil {
								gwBoardOpts = append(gwBoardOpts, autopilot.WithReleaseOverride(proj.Release))
							}
							gwAutopilotController = autopilot.NewController(
								cfg.Orchestrator.Autopilot,
								apGHClient,
								approvalMgr,
								parts[0],
								parts[1],
								gwBoardOpts...,
							)
							// GH-2685: wire the controller as the approval state writer so
							// async approval decisions update the in-memory PRState.
							approvalMgr.WithStateWriter(gwAutopilotController)
							// GH-3992: wire the LLM release summary generator — nil (graceful
							// no-op) when ANTHROPIC_API_KEY is unset, matching
							// NewReleaseSummaryGenerator's documented degradation.
							gwAutopilotController.SetReleaseSummaryGenerator(
								autopilot.NewReleaseSummaryGenerator(apGHClient, os.Getenv("ANTHROPIC_API_KEY"), logging.WithComponent("autopilot")),
							)
							// GH-4412: wire the always-on Dispatcher liveness signal so the
							// orphan-running sweep's live-worker exclusion set isn't silently
							// empty outside --dashboard mode (see SetMonitor's dashboard-only
							// wiring further down).
							if gwDispatcher != nil {
								gwAutopilotController.SetDispatcherLiveness(gwDispatcher)
								// GH-4454: project-scoped queued/running count for the
								// lane-starvation reconciler.
								gwAutopilotController.SetLaneQueueStatus(gwDispatcher)
							}
						}
					}
				}

				// GH-726: Initialize autopilot state store for gateway mode
				if gwStore != nil && gwAutopilotController != nil {
					gwAutopilotController.SetMemoryStore(gwStore)

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
					alertsMetrics := alerts.NewAlertMetrics()
					alertsDispatcher := alerts.NewDispatcher(alertsCfg, alerts.WithDispatcherMetrics(alertsMetrics))

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
					gwAlertsEngine = alerts.NewEngine(alertsCfg, alerts.WithDispatcher(alertsDispatcher), alerts.WithAlertMetrics(alertsMetrics))
					if alertErr := gwAlertsEngine.Start(ctx); alertErr != nil {
						logging.WithComponent("start").Error("alert engine failed to start — downstream alerters will be silently disabled; check alerts config", slog.Any("error", alertErr))
						gwAlertsEngine = nil
					}
				}

				// GH-3954: wire the alerts engine into the gateway autopilot controller
				// so it can fire alert-worthy events (e.g. post-tag release verification,
				// GH-3927) instead of only the default polling-path controller receiving it.
				if gwAutopilotController != nil && gwAlertsEngine != nil {
					gwAutopilotController.SetAlertsEngine(gwAlertsEngine)
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
					model.SetProjectPath(projectPath)
					applyDashboardBannerMeta(&model, cfg, cmd)
					model.EnableSplash(resolvedConfigPath())
					gwProgram = tea.NewProgram(model,
						tea.WithAltScreen(),
						tea.WithInput(os.Stdin),
						tea.WithOutput(os.Stdout),
					)
					// GH-2291: Progress/token callbacks are registered by runDashboardMode
					// which merges task states from both adapter pollers and gateway webhooks.
				}
			}

			// Enable Telegram polling in gateway mode only if --telegram flag was explicitly passed (GH-351)
			if telegramFlagSet && hasTelegram && cfg.Adapters.Telegram.Polling {
				pilotOpts = append(pilotOpts, pilot.WithTelegramHandler(gwRunner, projectPath))
				// GH-634: Wire team member resolver for Telegram RBAC in gateway mode
				if teamAdapter != nil {
					pilotOpts = append(pilotOpts, pilot.WithTelegramMemberResolver(teamAdapter))
				}
				// GH-2651: Wire approval handler so approve:/reject: button taps are dispatched
				if gwTgApprovalHandler != nil {
					pilotOpts = append(pilotOpts, pilot.WithTelegramApprovalHandler(gwTgApprovalHandler))
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
			} else {
				// GH-1019: Log why budget is disabled for debugging
				logging.WithComponent("start").Debug("budget enforcement disabled (gateway mode)",
					slog.Bool("config_nil", cfg.Budget == nil),
					slog.Bool("enabled", cfg.Budget != nil && cfg.Budget.Enabled),
					slog.Bool("store_nil", gwStore == nil),
				)
			}

			// GitHub polling in gateway mode is SDK-only (M7 4b/4d.2b) — the
			// in-tree fallback poller has been removed; StartAdapterPollers below
			// (via githubPollerRegistration) owns default-repo GitHub polling.

			// GH-1847: Start adapter pollers via registry pattern (gateway mode)
			// GH-4314: adapterHealthRegistry tracks per-adapter panic/restart/disable
			// state so one adapter's panic can't crash the daemon; wired into the
			// gateway status endpoint once p.Gateway() exists below.
			adapterHealthRegistry := adapterhealth.NewRegistry()
			gwPollerDeps := &PollerDeps{
				Cfg:                 cfg,
				ProjectPath:         projectPath,
				Dispatcher:          gwDispatcher,
				Runner:              gwRunner,
				Monitor:             gwMonitor,
				Program:             gwProgram,
				AlertsEngine:        gwAlertsEngine,
				Enforcer:            gwEnforcer,
				AutopilotController: gwAutopilotController,
				AutopilotStateStore: gwAutopilotStateStore,
				AdapterHealth:       adapterHealthRegistry,
			}
			StartAdapterPollers(context.Background(), gwPollerDeps, adapterPollerRegistrations())

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

			// Set up quality gates (GH-207) - for orchestrator/webhook mode.
			// GH-3716: resolved per-project, falling back to the global
			// config, then auto-detection.
			p.SetQualityCheckerFactory(newProjectQualityCheckerFactory(cfg))
			logging.WithComponent("start").Info("quality gates enabled for webhook mode")

			// GH-4314: surface adapter goroutine health on /api/v1/status.
			p.Gateway().SetAdapterHealthSource(&adapterHealthProviderAdapter{registry: adapterHealthRegistry})

			// GH-1585: Wire autopilot provider to gateway so /api/v1/autopilot returns live PR data
			if gwAutopilotController != nil {
				p.Gateway().SetAutopilotProvider(&autopilotProviderAdapter{controller: gwAutopilotController})
				p.Gateway().SetMetricsSource(gwAutopilotController.Metrics())
				// GH-2855: wire token/cost/execution counters into executor
				if gwRunner != nil {
					gwRunner.SetMetricsRecorder(gwAutopilotController.Metrics())
				}
				// GH-4041: restore Prometheus counter baselines from the store's
				// lifetime execution history before p.Start() below brings up the
				// /metrics handler, so external dashboards don't observe a
				// reset-to-zero on restart. Fail loud rather than silently start
				// with zero baselines.
				if gwStore != nil {
					if hydrateErr := autopilot.HydrateFromStore(context.Background(), gwStore, gwAutopilotController.Metrics()); hydrateErr != nil {
						return fmt.Errorf("failed to hydrate metrics from store: %w", hydrateErr)
					}
				}
			}
			// TASK-332: Wire alert metrics into the Prometheus exporter
			if gwAlertsEngine != nil {
				p.Gateway().SetAlertsMetricsSource(gwAlertsEngine)
			}
			if gwAutopilotController != nil {

				// GH-2080: Wire PR review events to autopilot controller
				p.SetOnPRReview(func(ctx context.Context, prNumber int, action, state, reviewer string, repo *github.Repository) error {
					if action == "submitted" {
						gwAutopilotController.OnReviewRequested(prNumber, action, state, reviewer)
					}
					return nil
				})
			}

			// GH-1609: Wire dashboard store to gateway so /api/v1/{metrics,queue,history,logs} return 200
			if gwStore != nil {
				p.Gateway().SetDashboardStore(gwStore)
				p.Gateway().SetLogStreamStore(gwStore)
			}
			p.Gateway().SetDashboardProjectPath(scopedProjectPath(dashboardScope, projectPath))

			// GH-1633: Wire git graph fetcher to gateway so /api/v1/gitgraph returns live git data
			p.Gateway().SetGitGraphFetcher(func(path string, limit int) interface{} {
				return dashboard.FetchGitGraph(path, limit)
			})
			p.Gateway().SetGitGraphPath(projectPath)

			// GH-1935: Wire learning system into gateway mode (mirrors polling-mode wiring)
			if gwStore != nil && (cfg.Memory.Learning == nil || cfg.Memory.Learning.Enabled) {
				gwPatternStore, gwPatternErr := memory.NewGlobalPatternStore(cfg.Memory.Path)
				if gwPatternErr != nil {
					logging.WithComponent("learning").Warn("Failed to create pattern store, learning disabled (gateway mode)", slog.Any("error", gwPatternErr))
				} else {
					gwExtractor := memory.NewPatternExtractor(gwPatternStore, gwStore)
					gwLearningLoop := memory.NewLearningLoop(gwStore, gwExtractor, nil)
					gwPatternContext := executor.NewPatternContext(gwStore)

					gwRunner.SetLearningLoop(gwLearningLoop)
					gwRunner.SetPatternContext(gwPatternContext)
					gwRunner.SetSelfReviewExtractor(gwExtractor)

					if gwAutopilotController != nil {
						gwAutopilotController.SetLearningLoop(gwLearningLoop)
						gwAutopilotController.SetEvalStore(gwStore)
					}

					// GH-1991: Wire outcome tracker for model escalation (gateway mode)
					gwOutcomeTracker := memory.NewModelOutcomeTracker(gwStore)
					gwRunner.SetOutcomeTracker(gwOutcomeTracker)
					if gwRunner.HasModelRouter() {
						gwRunner.ModelRouter().SetOutcomeTracker(gwOutcomeTracker)
					}

					// GH-2016: Wire knowledge graph into gateway runner
					gwKG, gwKGErr := memory.NewKnowledgeGraph(cfg.Memory.Path)
					if gwKGErr != nil {
						logging.WithComponent("learning").Warn("Failed to create knowledge graph (gateway mode)", slog.Any("error", gwKGErr))
					} else {
						gwRunner.SetKnowledgeGraph(gwKG)
						logging.WithComponent("learning").Info("Knowledge graph initialized (gateway mode)")
					}

					logging.WithComponent("learning").Info("Learning system initialized (gateway mode)")
				}
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
					fmt.Printf("● public tunnel · %s\n", publicURL)
					fmt.Printf("   Webhooks: %s/webhooks/{linear,github,gitlab,jira}\n", publicURL)
					defer tunnelMgr.Stop() //nolint:errcheck
				}
			}

			// Check for updates in background (non-blocking)
			go checkForUpdates()

			if dashboardMode {
				// GH-2291: Pass adapter poller infrastructure so the dashboard
				// merges task states from both adapter pollers and gateway webhooks.
				// GH-4490: also pass gwStore so collectTasks() can reconcile gwMonitor
				// against the executions table before every merge.
				return runDashboardMode(p, cfg, gwProgram, gwMonitor, gwRunner, gwStore, scopedProjectPath(dashboardScope, projectPath))
			}

			// Show startup banner (headless mode)
			gatewayURL := fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
			banner.StartupBanner(version, gatewayURL)

			// Show Telegram status in gateway mode (GH-349)
			if hasTelegram && cfg.Adapters.Telegram.Polling {
				fmt.Println("● telegram polling active")
			}

			// Show GitHub status in gateway mode (GH-350)
			if hasGithubPolling && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {
				fmt.Printf("● github polling · %s\n", cfg.Adapters.GitHub.Repo)
			}

			// Show Slack status in gateway mode (GH-652)
			if hasSlack {
				fmt.Println("● slack socket mode active")
			}

			// Show Linear status in gateway mode (GH-393)
			if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled {
				workspaces := cfg.Adapters.Linear.GetWorkspaces()
				for _, ws := range workspaces {
					fmt.Printf("● linear polling · %s/%s\n", ws.Name, ws.TeamID)
				}
			}

			// Show GitLab status (GH-2045)
			if cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled {
				if cfg.Adapters.GitLab.Polling != nil && cfg.Adapters.GitLab.Polling.Enabled {
					fmt.Println("● gitlab polling active")
				} else {
					fmt.Println("● gitlab webhooks enabled")
				}
			}

			// Show Jira status (GH-2045)
			if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
				if cfg.Adapters.Jira.Polling != nil && cfg.Adapters.Jira.Polling.Enabled {
					fmt.Println("● jira polling active")
				} else {
					fmt.Println("● jira webhooks enabled")
				}
			}

			// Show Asana status (GH-2045)
			if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
				if cfg.Adapters.Asana.Polling != nil && cfg.Adapters.Asana.Polling.Enabled {
					fmt.Println("● asana polling active")
				} else {
					fmt.Println("● asana webhooks enabled")
				}
			}

			// Show Azure DevOps status (GH-2045)
			if cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled {
				if cfg.Adapters.AzureDevOps.Polling != nil && cfg.Adapters.AzureDevOps.Polling.Enabled {
					fmt.Println("● azure devops polling active")
				} else {
					fmt.Println("● azure devops webhooks enabled")
				}
			}

			// Show Plane status (GH-2045)
			if cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled {
				if cfg.Adapters.Plane.Polling != nil && cfg.Adapters.Plane.Polling.Enabled {
					fmt.Println("● plane polling active")
				} else {
					fmt.Println("● plane webhooks enabled")
				}
			}

			// Show Discord status (GH-2045)
			if cfg.Adapters.Discord != nil && cfg.Adapters.Discord.Enabled {
				fmt.Println("● discord gateway enabled")
			}

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

			<-sigCh
			fmt.Println("\n○ shutting down...")

			// Close teams DB if opened (GH-633)
			if teamsDB != nil {
				_ = teamsDB.Close()
			}

			return p.Stop()
		},
	}

	cmd.Flags().BoolVar(&dashboardMode, "dashboard", false, "Show TUI dashboard for real-time task monitoring")
	cmd.Flags().StringVar(&dashboardScope, "dashboard-scope", "project", "Scope dashboard metrics: project (current project only) or all (all projects)")
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
	cmd.Flags().BoolVar(&enablePlane, "plane", false, "Enable Plane.so polling (overrides config)")
	cmd.Flags().BoolVar(&enableDiscord, "discord", false, "Enable Discord bot (overrides config)")
	cmd.Flags().BoolVar(&enableTunnel, "tunnel", false, "Enable public tunnel for webhook ingress (Cloudflare/ngrok)")
	cmd.Flags().StringVar(&teamID, "team", "", "Team ID or name for project access scoping (overrides config)")
	cmd.Flags().StringVar(&teamMember, "team-member", "", "Member email for team access scoping (overrides config)")
	cmd.Flags().StringVar(&logFormat, "log-format", "text", "Log output format: text or json (for log aggregation systems)")

	return cmd
}

// validateAdapterFlags returns an error when an adapter flag is set but the
// corresponding adapter block is missing or disabled in config. This prevents
// `pilot start --github` from silently auto-enabling a blank adapter and
// launching a no-op poller (GH-2361).
func validateAdapterFlags(cfg *config.Config, cmd *cobra.Command) error {
	type adapterCheck struct {
		flag    string
		enabled bool
		exists  bool
	}
	adapters := []adapterCheck{
		{"github", cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled, cfg.Adapters != nil && cfg.Adapters.GitHub != nil},
		{"linear", cfg.Adapters != nil && cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled, cfg.Adapters != nil && cfg.Adapters.Linear != nil},
		{"plane", cfg.Adapters != nil && cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled, cfg.Adapters != nil && cfg.Adapters.Plane != nil},
		{"discord", cfg.Adapters != nil && cfg.Adapters.Discord != nil && cfg.Adapters.Discord.Enabled, cfg.Adapters != nil && cfg.Adapters.Discord != nil},
	}
	for _, a := range adapters {
		if !cmd.Flags().Changed(a.flag) {
			continue
		}
		if a.enabled {
			continue
		}
		if a.exists {
			return fmt.Errorf("--%s flag set but adapters.%s.enabled is false in config.\nFix: set adapters.%s.enabled: true, or run 'pilot setup'",
				a.flag, a.flag, a.flag)
		}
		return fmt.Errorf("--%s flag set but adapters.%s block is missing in config.\nFix: add adapters.%s block, or run 'pilot setup'",
			a.flag, a.flag, a.flag)
	}
	return nil
}

// applyInputOverrides applies CLI flag overrides to config
// Uses cmd.Flags().Changed() to only apply flags that were explicitly set
func applyInputOverrides(cfg *config.Config, cmd *cobra.Command, telegramFlag, githubFlag, linearFlag, slackFlag, tunnelFlag, planeFlag, discordFlag bool) {
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
	if cmd.Flags().Changed("plane") {
		if cfg.Adapters.Plane == nil {
			cfg.Adapters.Plane = plane.DefaultConfig()
		}
		cfg.Adapters.Plane.Enabled = planeFlag
		if cfg.Adapters.Plane.Polling == nil {
			cfg.Adapters.Plane.Polling = &plane.PollingConfig{}
		}
		cfg.Adapters.Plane.Polling.Enabled = planeFlag
	}
	if cmd.Flags().Changed("discord") {
		if cfg.Adapters.Discord == nil {
			cfg.Adapters.Discord = discord.DefaultConfig()
		}
		cfg.Adapters.Discord.Enabled = discordFlag
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

// setupDashboardLogging redirects daemon logs to a rotating file in TUI
// dashboard mode (GH-3600) so upgrade/restart failures stay diagnosable.
// logging.dashboard_log config: "" = default ~/.pilot/logs/daemon.log,
// "off" = discard (pre-GH-3600 behavior), anything else = custom path.
// Falls back to Suppress on error — an unwritable log path must not corrupt
// the TUI.
func setupDashboardLogging(cfg *config.Config) {
	path := logging.DefaultDaemonLogPath()
	var rotation *logging.RotationConfig
	if cfg.Logging != nil {
		if cfg.Logging.DashboardLog == "off" {
			logging.Suppress()
			return
		}
		if cfg.Logging.DashboardLog != "" {
			path = cfg.Logging.DashboardLog
		}
		rotation = cfg.Logging.Rotation
	}
	if err := logging.RedirectToFile(path, rotation); err != nil {
		logging.Suppress()
	}
}

// approvalExpirySweepInterval is how often startApprovalExpirySweep checks
// for pending approvals past their expires_at.
const approvalExpirySweepInterval = 1 * time.Minute

// expirablePendingHandler is satisfied by any approval handler that persists
// pending requests and needs its own timeout sweep post-restart (currently
// *approval.TelegramHandler and *approval.SlackHandler; GH-3825, GH-4411).
type expirablePendingHandler interface {
	PruneExpired(ctx context.Context) (int, error)
}

// startApprovalExpirySweep runs a background loop that prunes pending
// approvals whose expires_at has passed, editing their message to show they
// expired. A request rehydrated after a restart (see TelegramHandler/
// SlackHandler.Rehydrate) has no waiter goroutine enforcing its own timeout,
// so without this sweep it would sit in the pending set forever instead of
// resolving (GH-3825, GH-4411).
func startApprovalExpirySweep(ctx context.Context, handler expirablePendingHandler) {
	if handler == nil || reflect.ValueOf(handler).IsNil() {
		return
	}
	logging.SafeGo("approval-expiry-sweep", func() {
		ticker := time.NewTicker(approvalExpirySweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := handler.PruneExpired(ctx); err != nil {
					logging.WithComponent("approval").Warn("expired approval sweep failed", slog.Any("error", err))
				}
			}
		}
	})
}

// daemonLockDir resolves the directory that holds the single-instance lock
// file, falling back to the same default memory.Path uses when config
// somehow leaves Memory unset.
func daemonLockDir(cfg *config.Config) string {
	if cfg.Memory != nil && cfg.Memory.Path != "" {
		return cfg.Memory.Path
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pilot", "data")
}

// resolveMemoryDBPath symlink-resolves configuredPath and returns the
// absolute path to the pilot.db file it points at (GH-4393). If
// configuredPath doesn't exist yet — a genuine first run, or about to be
// auto-created by the memory store or lock acquisition — EvalSymlinks can't
// resolve it, so this falls back to the unresolved path.
func resolveMemoryDBPath(configuredPath string) string {
	resolved, err := filepath.EvalSymlinks(configuredPath)
	if err != nil {
		resolved = configuredPath
	}
	return filepath.Join(resolved, "pilot.db")
}

// logMemoryStartupBanner logs the configured memory/state directory and its
// symlink-resolved absolute path (GH-4393). A configured path that silently
// diverges from where it actually resolves on disk — e.g. an absolute path
// left over from a host migration that a cutover shim didn't cover — is
// otherwise invisible until writes vanish from the canonical ledger. Emitted
// as early as possible (right after the single-instance lock is acquired)
// so it lands in the first lines of daemon.log.
func logMemoryStartupBanner(cfg *config.Config) {
	configuredPath := daemonLockDir(cfg)
	logging.WithComponent("start").Info("memory store path resolved",
		slog.String("configured_path", configuredPath),
		slog.String("resolved_db_path", resolveMemoryDBPath(configuredPath)),
	)
}

// acquireDaemonLock takes the adapter-agnostic single-instance guard
// (GH-4311): an OS-level flock on <Memory.Path>/pilot.lock, held for the
// process lifetime and released automatically on exit or crash (flock
// semantics — no cleanup code required for the crash case).
//
// With --replace, an existing holder is SIGTERM'd and we wait (bounded) for
// it to release the lock before acquiring it ourselves. This is now the
// primary --replace mechanism — it supersedes the old behavior where
// --replace only fired on a Telegram 409 and pkilled every "pilot start"
// match with no confirmation the target actually exited.
func acquireDaemonLock(cfg *config.Config, replace bool) (*singleton.Lock, error) {
	dir := daemonLockDir(cfg)

	lock, err := singleton.Acquire(dir)
	if err == nil {
		return lock, nil
	}

	var held *singleton.ErrHeld
	if !errors.As(err, &held) {
		return nil, fmt.Errorf("failed to acquire single-instance lock: %w", err)
	}

	if !replace {
		fmt.Println()
		fmt.Printf("✗ Another pilot daemon is already running (pid %d)\n", held.PID)
		fmt.Println()
		fmt.Println("   Options:")
		fmt.Println("   • Stop it:            pilot stop")
		fmt.Println("   • Auto-replace:       pilot start --replace")
		fmt.Println()
		return nil, fmt.Errorf("conflict: pilot daemon already running (pid %d)", held.PID)
	}

	fmt.Printf("⟲ stopping existing pilot daemon (pid %d)...\n", held.PID)
	if held.PID > 0 {
		if proc, ferr := os.FindProcess(held.PID); ferr == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	fmt.Print("   Waiting for existing daemon to release its lock")
	const maxRetries = 20
	for i := 0; i < maxRetries; i++ {
		time.Sleep(time.Duration(500+i*250) * time.Millisecond)
		fmt.Print(".")
		lock, err = singleton.Acquire(dir)
		if err == nil {
			fmt.Println(" ✓")
			fmt.Println("   ✓ existing daemon stopped, lock acquired")
			fmt.Println()
			return lock, nil
		}
		if !errors.As(err, &held) {
			fmt.Println(" ✗")
			return nil, fmt.Errorf("failed to acquire single-instance lock: %w", err)
		}
	}
	fmt.Println(" ✗")
	return nil, fmt.Errorf("timeout waiting for existing pilot daemon (pid %d) to release lock", held.PID)
}

// runPollingMode runs lightweight polling-only mode.
// When noGateway is false, the HTTP gateway starts in the background so the
// desktop app (and any other client hitting /health) can reach the daemon.
// bootReconcile carries the GH-3600 upgrade verification outcome for the
// dashboard to surface; may be nil.
func runPollingMode(cmd *cobra.Command, cfg *config.Config, projectPath string, replace, dashboardMode, noGateway bool, bootReconcile *upgrade.BootReconcileResult) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// GH-4311: adapter-agnostic single-instance guard. Must run before any
	// adapter wiring below — two daemons concurrently wiring the same
	// adapters (e.g. two autopilot controllers polling the same repo) is
	// exactly the duplicate-work failure mode this closes. This supersedes
	// the Telegram-only 409 check further down, which is blind to
	// github-only/headless runs.
	daemonLock, err := acquireDaemonLock(cfg, replace)
	if err != nil {
		return err
	}
	defer func() { _ = daemonLock.Release() }()

	// GH-4393: log the resolved, symlink-evaluated absolute DB path in the
	// first lines of daemon.log. The 2026-07-16 cutover incident produced a
	// shadow ledger — an absolute Memory.Path left over from a host
	// migration that bypassed the cutover shim — that was indistinguishable
	// from a healthy first run until executions silently diverged from the
	// canonical tree for three hours. Logging the resolved path up front
	// makes that divergence visible immediately instead of only in hindsight.
	logMemoryStartupBanner(cfg)

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

	// Dashboard-mode log redirect already happened in the start command before
	// this call (GH-3600), which preserves the GH-190 ordering: the runner
	// below caches its logger at creation time, so the redirect must precede it.

	// Create runner with config (GH-956: enables worktree isolation, decomposer, model routing)
	runner, err := executor.NewRunnerWithConfig(cfg.Executor)
	if err != nil {
		return fmt.Errorf("failed to create executor runner: %w", err)
	}
	// TASK-286 / GH-3027: refuse sub-issue creation on unmanaged repos.
	runner.SetRepoAllowlist(newConfigRepoAllowlist(cfg))

	// Set up quality gates (GH-207). GH-3716: resolved per-project, falling
	// back to the global config, then auto-detection.
	runner.SetQualityCheckerFactory(newProjectQualityCheckerFactory(cfg))
	logging.WithComponent("start").Info("quality gates enabled for polling mode")

	// Set up team project access checker if configured (GH-635)
	if teamCleanup := wireProjectAccessChecker(runner, cfg); teamCleanup != nil {
		defer teamCleanup()
	}

	// GH-962: Clean up orphaned worktree directories from previous crashed executions
	if cfg.Executor != nil && cfg.Executor.UseWorktree {
		removed, freedBytes, err := executor.CleanupOrphanedWorktrees(ctx, projectPath)
		if err != nil {
			// Real failure — don't fail startup, this is best-effort cleanup.
			logging.WithComponent("start").Warn("worktree cleanup error", slog.String("error", err.Error()))
		} else if removed > 0 {
			logging.WithComponent("start").Info("worktree cleanup completed",
				slog.Int("removed", removed),
				slog.String("freed_mb", fmt.Sprintf("%.1f", float64(freedBytes)/(1024*1024))))
		} else {
			logging.WithComponent("start").Debug("worktree cleanup scan completed, no orphans found")
		}
	}

	// Create approval manager
	approvalMgr := approval.NewManager(cfg.Approval)

	// Register Telegram approval handler if enabled
	var tgApprovalHandler telegram.ApprovalCallbackHandler
	var tgApprovalHandlerImpl *approval.TelegramHandler
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" &&
		(cfg.Adapters.Telegram.Approval == nil || cfg.Adapters.Telegram.Approval.Enabled) {
		tgApprovalClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
		tgApprovalHandlerImpl = approval.NewTelegramHandler(&telegramApprovalAdapter{client: tgApprovalClient}, cfg.Adapters.Telegram.ChatID)
		// GH-3825: persist decisions directly to PRState via the manager so a
		// button tap on a Rehydrate-restored request isn't lost when no waiter
		// goroutine survived the restart.
		tgApprovalHandlerImpl.WithDecisionRecorder(approvalMgr)
		approvalMgr.RegisterHandler(tgApprovalHandlerImpl)
		tgApprovalHandler = tgApprovalHandlerImpl
		logging.WithComponent("start").Info("registered Telegram approval handler")
	}

	// Register Slack approval handler if enabled
	var slackApprovalHandler slack.ApprovalCallbackHandler
	var slackApprovalHandlerImpl *approval.SlackHandler
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
		if cfg.Adapters.Slack.Approval != nil && cfg.Adapters.Slack.Approval.Enabled {
			slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
			slackAdapter := slack.NewSlackClientAdapter(slackClient)
			slackChannel := cfg.Adapters.Slack.Approval.Channel
			if slackChannel == "" {
				slackChannel = cfg.Adapters.Slack.Channel
			}
			slackApprovalHandlerImpl = approval.NewSlackHandler(&slackApprovalClientAdapter{adapter: slackAdapter}, slackChannel)
			// GH-4411: persist decisions directly to PRState via the manager so a
			// button click on a Rehydrate-restored request isn't lost when no
			// waiter goroutine survived the restart (mirrors GH-3825's Telegram fix).
			slackApprovalHandlerImpl.WithDecisionRecorder(approvalMgr)
			approvalMgr.RegisterHandler(slackApprovalHandlerImpl)
			// GH-4431: route Socket Mode approve/reject clicks to this handler
			// (see slack.HandlerConfig.ApprovalHandler below) — without this,
			// approval buttons are unroutable on socket-mode deployments since
			// they have no public HTTP Interactivity endpoint to receive them.
			slackApprovalHandler = slackApprovalHandlerImpl
			logging.WithComponent("start").Info("registered Slack approval handler",
				slog.String("channel", slackChannel))
		}
	}

	// GH-929: Create autopilot controllers map (one per repo) if enabled
	autopilotControllers := make(map[string]*autopilot.Controller)
	var autopilotController *autopilot.Controller // Default controller for backwards compat
	if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.Enabled {
		// Need GitHub client for autopilot
		ghToken, _ := resolveGitHubToken(cfg)
		if ghToken == "" {
			// GH-3050: surface silent autopilot disable when token is missing.
			// Without this warning, --env=<...> appears accepted but autopilot
			// never starts because controller creation is skipped here.
			logging.WithComponent("autopilot").Warn(
				"autopilot enabled but no GitHub token resolved — autopilot will not start (set adapters.github.token or GITHUB_TOKEN)",
				slog.String("env", string(cfg.Orchestrator.Autopilot.Environment)),
			)
		}
		if ghToken != "" {
			ghClient := github.NewClient(ghToken)
			// M7 4d.1: autopilot consumes the studio-sdk client; ghClient (in-tree)
			// stays for the approval handler and legacy paths until later phases.
			apGHClient := githubSDK.NewClient(ghToken)

			// GH-3992: one shared LLM release summary generator for every
			// controller constructed below (default + per-project) — nil
			// (graceful no-op) when ANTHROPIC_API_KEY is unset.
			releaseSummaryGen := autopilot.NewReleaseSummaryGenerator(apGHClient, os.Getenv("ANTHROPIC_API_KEY"), logging.WithComponent("autopilot"))

			// GH-1870: Build shared (non-board) options for every autopilot
			// controller. GH-4472: board sync is resolved per-repo below via
			// projectBoardControllerOpts instead of being folded into this
			// shared slice — a single global ProjectBoard here would leak
			// onto every project controller regardless of its own repo.
			var autopilotSharedOpts []autopilot.ControllerOption
			// GH-4460: the in-tree client exposes the jobs/annotations APIs the
			// studio-sdk client doesn't yet — wire it so CI-failure excerpts
			// resolve to the actual failing step instead of a whole-job tail.
			autopilotSharedOpts = append(autopilotSharedOpts, autopilot.WithStepLogClient(ghClient))
			// GH-4454: every controller's lane-starvation reconciler needs the
			// same trigger label the GitHub SDK poller watches for
			// (poller_github.go resolves this identically: ghCfg.PilotLabel,
			// defaulting to "pilot" — WithPilotLabel leaves it unset when
			// PilotLabel is empty, so NewController's own default applies).
			if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.PilotLabel != "" {
				autopilotSharedOpts = append(autopilotSharedOpts, autopilot.WithPilotLabel(cfg.Adapters.GitHub.PilotLabel))
			}

			// Create controller for default repo (adapters.github.repo)
			if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
				parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
				if len(parts) == 2 {
					// Register GitHub approval handler if enabled
					if cfg.Adapters.GitHub.Approval != nil && cfg.Adapters.GitHub.Approval.Enabled {
						pollInterval := cfg.Adapters.GitHub.Approval.PollInterval
						if pollInterval == 0 {
							pollInterval = 30 * time.Second
						}
						ghApprovalHandler := approval.NewGitHubHandler(ghClient, &approval.GitHubHandlerConfig{
							Owner: parts[0], Repo: parts[1], PollInterval: pollInterval,
						})
						approvalMgr.RegisterHandler(ghApprovalHandler)
						logging.WithComponent("start").Info("registered GitHub approval handler",
							slog.String("repo", cfg.Adapters.GitHub.Repo))
					}

					// TASK-352: scope self-heal to the project's fs path. Fresh slice so
					// the per-project loop below does not alias this controller's option.
					ctrlOpts := append(append([]autopilot.ControllerOption{}, autopilotSharedOpts...), autopilot.WithProjectPath(projectPath))
					// GH-4472: default repo resolves project override → global fallback.
					ctrlOpts = append(ctrlOpts, projectBoardControllerOpts(apGHClient, cfg, cfg.Adapters.GitHub.Repo, parts[0], true)...)
					// GH-3931: apply the per-project release overlay (GH-3930) when configured.
					if proj := cfg.FindProjectByRepo(cfg.Adapters.GitHub.Repo); proj != nil {
						if proj.Release != nil {
							ctrlOpts = append(ctrlOpts, autopilot.WithReleaseOverride(proj.Release))
						}
						// GH-4478: apply the per-project CI-checks overlay when configured;
						// nil is a no-op (inherits the global required-checks/CI-checks).
						if proj.CIChecks != nil {
							ctrlOpts = append(ctrlOpts, autopilot.WithCIChecksOverride(proj.CIChecks))
						}
					}
					controller := autopilot.NewController(
						cfg.Orchestrator.Autopilot,
						apGHClient,
						approvalMgr,
						parts[0],
						parts[1],
						ctrlOpts...,
					)
					controller.SetReleaseSummaryGenerator(releaseSummaryGen)
					autopilotControllers[cfg.Adapters.GitHub.Repo] = controller
					autopilotController = controller // Default for backwards compat
				}
			}

			// GH-4001: release automation is per-project opt-in for projects-loop
			// controllers — resolved once here so the WARN below fires only when
			// global release would otherwise have cascaded to a non-opted-in repo.
			globalReleaseEnabled := autopilot.GlobalReleaseEnabled(cfg.Orchestrator.Autopilot)

			// GH-929: Create controllers for each project with GitHub config
			for _, proj := range cfg.Projects {
				if proj.GitHub == nil || proj.GitHub.Owner == "" || proj.GitHub.Repo == "" {
					continue
				}
				repoFullName := fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
				if _, exists := autopilotControllers[repoFullName]; exists {
					continue // Skip duplicates
				}
				// TASK-352: scope self-heal to this project's fs path (matches
				// executions.project_path). Fresh slice to avoid aliasing the shared opts.
				ctrlOpts := append(append([]autopilot.ControllerOption{}, autopilotSharedOpts...), autopilot.WithProjectPath(proj.Path))
				// GH-4472: this project's own github.project_board (if set) wins;
				// no fallback here — only the default adapter repo inherits the
				// global block.
				ctrlOpts = append(ctrlOpts, projectBoardControllerOpts(apGHClient, cfg, repoFullName, proj.GitHub.Owner, false)...)
				// GH-4001: a project's own `release:` block keeps today's overlay
				// semantics (GH-3931/GH-3930); no block means this repo never
				// opted into release automation and must not inherit the
				// global/env cascade — two incidents (studio-sdk 2026-07-06,
				// Navigator 2026-07-07 near-miss) came from a forgotten repo
				// silently releasing.
				if proj.Release != nil {
					ctrlOpts = append(ctrlOpts, autopilot.WithReleaseOverride(proj.Release))
				} else {
					ctrlOpts = append(ctrlOpts, autopilot.WithReleaseNotOptedIn())
					if globalReleaseEnabled {
						logging.WithComponent("autopilot").Warn(
							"project has no release: block — it will NOT auto-release even though global release is enabled (GH-4001); add a release block to opt in",
							slog.String("project", proj.Name),
							slog.String("repo", repoFullName),
						)
					}
				}
				// GH-4478: apply the per-project CI-checks overlay when configured;
				// nil is a no-op (inherits the global required-checks/CI-checks) —
				// unlike Release, there's no "opt-in" warning here since inheriting
				// the global CI-checks config was always the pre-existing behavior.
				if proj.CIChecks != nil {
					ctrlOpts = append(ctrlOpts, autopilot.WithCIChecksOverride(proj.CIChecks))
				}
				controller := autopilot.NewController(
					cfg.Orchestrator.Autopilot,
					apGHClient,
					approvalMgr,
					proj.GitHub.Owner,
					proj.GitHub.Repo,
					ctrlOpts...,
				)
				controller.SetReleaseSummaryGenerator(releaseSummaryGen)
				autopilotControllers[repoFullName] = controller
				logging.WithComponent("autopilot").Info("created controller for project",
					slog.String("project", proj.Name),
					slog.String("repo", repoFullName),
				)
			}
		}
	}

	// GH-2685: wire all controllers as the approval state writer so async approval
	// decisions update the correct in-memory PRState across multi-repo deployments.
	if len(autopilotControllers) > 0 {
		var allControllers []*autopilot.Controller
		for _, c := range autopilotControllers {
			allControllers = append(allControllers, c)
		}
		approvalMgr.WithStateWriter(autopilot.NewMultiControllerStateWriter(allControllers...))
	}

	// Initialize memory store early for dashboard persistence (GH-367).
	// NewStoreGuarded (GH-4393) refuses to hand back a store that looks like
	// a shadow ledger: a brand-new/empty state directory opened despite this
	// daemon having run before with real history recorded elsewhere. That is
	// a different failure mode than "couldn't open the DB" — it looks
	// healthy, so unlike an ordinary store error it must abort startup
	// rather than degrade gracefully with store=nil.
	store, err := memory.NewStoreGuarded(cfg.Memory.Path)
	if err != nil {
		var splitBrain *memory.ErrSplitBrainLedger
		if errors.As(err, &splitBrain) {
			logging.WithComponent("start").Error("refusing to start: possible shadow ledger detected", slog.Any("error", err))
			return err
		}
		logging.WithComponent("start").Warn("Failed to open memory store", slog.Any("error", err))
		store = nil
	} else {
		defer func() {
			if store != nil {
				_ = store.Close()
			}
		}()
	}

	// Attach persistence store and rehydrate pending approvals after restart.
	if store != nil && tgApprovalHandlerImpl != nil {
		tgApprovalHandlerImpl.WithStore(store)
		if rErr := tgApprovalHandlerImpl.Rehydrate(ctx); rErr != nil {
			logging.WithComponent("approval").Warn("telegram approval rehydrate failed", slog.Any("error", rErr))
		}
	}
	// GH-3825: prune requests that expired while the daemon was down (or with
	// no in-process waiter) instead of leaving them pending forever.
	startApprovalExpirySweep(ctx, tgApprovalHandlerImpl)

	// GH-4411: same restart-survival treatment for Slack approvals.
	if store != nil && slackApprovalHandlerImpl != nil {
		slackApprovalHandlerImpl.WithStore(store)
		if rErr := slackApprovalHandlerImpl.Rehydrate(ctx); rErr != nil {
			logging.WithComponent("approval").Warn("slack approval rehydrate failed", slog.Any("error", rErr))
		}
	}
	startApprovalExpirySweep(ctx, slackApprovalHandlerImpl)

	// GH-726: Initialize autopilot state store for crash recovery
	var autopilotStateStore *autopilot.StateStore
	if store != nil && len(autopilotControllers) > 0 {
		// GH-2712: Wire memory store for approval_request_id / approval_decision persistence.
		for _, controller := range autopilotControllers {
			controller.SetMemoryStore(store)
		}

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

	// GH-1814: Initialize learning system
	if store != nil && (cfg.Memory.Learning == nil || cfg.Memory.Learning.Enabled) {
		patternStore, patternErr := memory.NewGlobalPatternStore(cfg.Memory.Path)
		if patternErr != nil {
			logging.WithComponent("learning").Warn("Failed to create pattern store, learning disabled", slog.Any("error", patternErr))
		} else {
			extractor := memory.NewPatternExtractor(patternStore, store)
			learningLoop := memory.NewLearningLoop(store, extractor, nil)
			patternContext := executor.NewPatternContext(store)

			runner.SetLearningLoop(learningLoop)
			runner.SetPatternContext(patternContext)
			runner.SetSelfReviewExtractor(extractor)

			// GH-1823: Wire review learning into autopilot controllers
			for _, ctrl := range autopilotControllers {
				ctrl.SetLearningLoop(learningLoop)
				ctrl.SetEvalStore(store)
			}

			logging.WithComponent("learning").Info("Learning system initialized")

			// GH-1991: Wire outcome tracker for model escalation
			outcomeTracker := memory.NewModelOutcomeTracker(store)
			runner.SetOutcomeTracker(outcomeTracker)
			if runner.HasModelRouter() {
				runner.ModelRouter().SetOutcomeTracker(outcomeTracker)
			}
			logging.WithComponent("learning").Info("Model outcome tracker initialized")

			// GH-2016: Wire knowledge graph into runner
			kg, kgErr := memory.NewKnowledgeGraph(cfg.Memory.Path)
			if kgErr != nil {
				logging.WithComponent("learning").Warn("Failed to create knowledge graph", slog.Any("error", kgErr))
			} else {
				runner.SetKnowledgeGraph(kg)
				logging.WithComponent("learning").Info("Knowledge graph initialized")
			}

			// Pattern maintenance — decay and cleanup every 24h
			go func() {
				ticker := time.NewTicker(24 * time.Hour)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if n, decayErr := learningLoop.ApplyDecay(ctx); decayErr != nil {
							logging.WithComponent("learning").Warn("Pattern decay failed", slog.Any("error", decayErr))
						} else if n > 0 {
							logging.WithComponent("learning").Info("Applied pattern decay", slog.Int("patterns_decayed", n))
						}
						minConfidence := 0.1
						if cfg.Memory.Learning != nil && cfg.Memory.Learning.MinConfidence > 0 {
							minConfidence = cfg.Memory.Learning.MinConfidence
						}
						if n, depErr := learningLoop.DeprecateLowConfidencePatterns(ctx, minConfidence); depErr != nil {
							logging.WithComponent("learning").Warn("Pattern deprecation failed", slog.Any("error", depErr))
						} else if n > 0 {
							logging.WithComponent("learning").Info("Deprecated low-confidence patterns", slog.Int("deprecated", n))
						}
					}
				}
			}()
		}
	}

	// GH-4314: adapterHealthRegistry tracks per-adapter panic/restart/disable
	// state so one adapter's panic can't crash the daemon; wired into
	// pollingDeps below and, when the gateway runs, into /api/v1/status.
	adapterHealthRegistry := adapterhealth.NewRegistry()

	// GH-1662: Start gateway in background so desktop app can reach /health
	var gwServer *gateway.Server // hoisted so TASK-332 alert-metrics wiring can run after alerts engine is created
	// GH-4068: aggregate every controller's Metrics (default + one per
	// project, not just the backward-compat default) so /metrics, the
	// metrics alerter, and the metrics persister all reflect fleet-wide PR
	// activity. Hoisted so the alerter/persister wiring further down (after
	// pollers start) can reuse it. autopilotControllers always contains the
	// default controller too (see assignment above), so ranging over it
	// alone covers both.
	var fleetMetrics []*autopilot.Metrics
	for _, c := range autopilotControllers {
		fleetMetrics = append(fleetMetrics, c.Metrics())
	}
	autopilotMetricsAggregate := autopilot.NewAggregateMetrics(fleetMetrics...)
	if !noGateway && cfg.Gateway != nil {
		gwServer = gateway.NewServer(cfg.Gateway)
		gwServer.SetAdapterHealthSource(&adapterHealthProviderAdapter{registry: adapterHealthRegistry})

		if autopilotController != nil {
			gwServer.SetAutopilotProvider(&autopilotProviderAdapter{controller: autopilotController})
			// GH-2855: wire token/cost/execution counters into executor
			runner.SetMetricsRecorder(autopilotController.Metrics())
			// GH-4041: restore Prometheus counter baselines from the store's
			// lifetime execution history before the /metrics handler starts
			// serving scrapes below, so external dashboards don't observe a
			// reset-to-zero on restart. Fail loud rather than silently start
			// with zero baselines. The default controller is the sole
			// hydration owner (GH-4068) — hydrating any other controller's
			// Metrics would double-count once autopilotMetricsAggregate sums
			// them below.
			if store != nil {
				if hydrateErr := autopilot.HydrateFromStore(ctx, store, autopilotController.Metrics()); hydrateErr != nil {
					return fmt.Errorf("failed to hydrate metrics from store: %w", hydrateErr)
				}
			}
		}
		if len(fleetMetrics) > 0 {
			gwServer.SetMetricsSource(autopilotMetricsAggregate)
		}

		// GH-2080: Wire PR review webhook events to autopilot controller in polling mode
		if autopilotController != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
			capturedController := autopilotController
			token, _ := resolveGitHubToken(cfg)
			if token != "" {
				ghClient := githubSDK.NewClient(token)
				ghWH := githubSDK.NewWebhookHandler(ghClient, cfg.Adapters.GitHub.WebhookSecret, cfg.Adapters.GitHub.PilotLabel)
				ghWH.OnPRReview(func(ctx context.Context, prNumber int, action, state, reviewer string, repo *githubSDK.Repository) error {
					if action == "submitted" {
						capturedController.OnReviewRequested(prNumber, action, state, reviewer)
					}
					return nil
				})
				gwServer.Router().RegisterWebhookHandler("github", func(payload map[string]interface{}) {
					eventType, _ := payload["_event_type"].(string)
					if err := ghWH.Handle(context.Background(), eventType, payload); err != nil {
						logging.WithComponent("pilot").Error("GitHub webhook error (polling mode)", slog.Any("error", err))
					}
				})
			}
		}
		if store != nil {
			gwServer.SetDashboardStore(store)
			gwServer.SetLogStreamStore(store)
		}
		gwServer.SetDashboardProjectPath(projectPath)
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
		// GH-4246: rebuild the monitor from queued/running DB rows before the
		// dashboard's first refresh tick — otherwise a restart with active
		// work in the DB leaves the queue panel blind until each task's own
		// lifecycle happens to re-touch the monitor (queued tasks never do).
		if store != nil {
			if hydrateErr := monitor.HydrateFromStore(store); hydrateErr != nil {
				logging.WithComponent("start").Warn("failed to hydrate monitor from store", slog.Any("error", hydrateErr))
			}
		}
		upgradeRequestCh = make(chan struct{}, 1)
		model := dashboard.NewModelWithOptions(version, store, autopilotController, upgradeRequestCh)
		model.SetProjectPath(projectPath)
		applyDashboardBannerMeta(&model, cfg, cmd)
		model.EnableSplash(resolvedConfigPath())
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
		runner.AddTokenCallback("dashboard", func(taskID string, inputTokens, outputTokens int64, modelName string) {
			program.Send(dashboard.UpdateTokens(int(inputTokens), int(outputTokens), modelName)())
		})
	}

	// Build a shared IssueCreator for comms.Handler (bot /draft-issue + NL intake).
	// Nil when GitHub is not configured — Handler degrades gracefully.
	var commsIssueCreator comms.IssueCreator
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled && cfg.Adapters.GitHub.Repo != "" {
		ghToken, _ := resolveGitHubToken(cfg)
		if ghToken != "" {
			repoParts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
			if len(repoParts) == 2 {
				ghIssueClient := github.NewClient(ghToken)
				commsIssueCreator = github.NewIssueCreator(
					ghIssueClient,
					github.AllowAllIssueRepos(),
					github.IssueCreatorEntry{
						ProjectPath: cfg.Adapters.GitHub.ProjectPath,
						Owner:       repoParts[0],
						Repo:        repoParts[1],
					},
				)
			}
		}
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

		tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
		tgMessenger := telegram.NewMessenger(tgClient, cfg.Adapters.Telegram.PlainTextMode)

		// Build comms.MemberResolver wrapper (GH-634)
		var tgMemberResolver comms.MemberResolver
		if teamAdapter != nil {
			tgMemberResolver = &telegram.MemberResolverAdapter{Inner: teamAdapter}
		}

		var tgClassifierCfg *comms.ClassifierConfig
		if cfg.Adapters.Telegram.LLMClassifier != nil {
			tgClassifierCfg = &comms.ClassifierConfig{
				Enabled:     cfg.Adapters.Telegram.LLMClassifier.Enabled,
				APIKey:      cfg.Adapters.Telegram.LLMClassifier.APIKey,
				HistorySize: cfg.Adapters.Telegram.LLMClassifier.HistorySize,
				HistoryTTL:  cfg.Adapters.Telegram.LLMClassifier.HistoryTTL,
			}
		}

		var tgBotCfg *comms.BotConfig
		if cfg.Bot != nil {
			tgBotCfg = &comms.BotConfig{
				Enabled:     cfg.Bot.Enabled,
				Model:       cfg.Bot.Model,
				AnswerModel: cfg.Bot.AnswerModel,
				APIKey:      cfg.Bot.APIKey,
				Persona:     cfg.Bot.Persona,
				Retrieval: comms.RetrievalConfig{
					Enabled:  cfg.Bot.Retrieval.Enabled,
					MaxFiles: cfg.Bot.Retrieval.MaxFiles,
					MaxBytes: cfg.Bot.Retrieval.MaxBytes,
				},
			}
		}

		tgCommsHandler := comms.BuildHandler(comms.HandlerDeps{
			Messenger:       tgMessenger,
			Runner:          runner,
			Projects:        config.NewProjectSource(cfg),
			ProjectPath:     projectPath,
			RateLimit:       cfg.Adapters.Telegram.RateLimit,
			Classifier:      tgClassifierCfg,
			Bot:             tgBotCfg,
			MemberResolver:  tgMemberResolver,
			Store:           store,
			IssueCreator:    commsIssueCreator,
			TaskIDPrefix:    "TG",
			ExecutorBackend: cfg.Executor,
		})

		tgConfig := &telegram.HandlerConfig{
			Client:          tgClient,
			CommsHandler:    tgCommsHandler,
			ProjectPath:     projectPath,
			Projects:        config.NewProjectSource(cfg),
			AllowedIDs:      allowedIDs,
			Transcription:   cfg.Adapters.Telegram.Transcription,
			Store:           store,
			ApprovalHandler: tgApprovalHandler,
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
					fmt.Println("⟲ stopping existing bot instance...")
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
					fmt.Println("✗ Another bot instance is already running")
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
		alertsMetrics := alerts.NewAlertMetrics()
		alertsDispatcher := alerts.NewDispatcher(alertsCfg, alerts.WithDispatcherMetrics(alertsMetrics))

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

		alertsEngine = alerts.NewEngine(alertsCfg, alerts.WithDispatcher(alertsDispatcher), alerts.WithAlertMetrics(alertsMetrics))
		if err := alertsEngine.Start(ctx); err != nil {
			logging.WithComponent("start").Error("alert engine failed to start — downstream alerters will be silently disabled; check alerts config", slog.Any("error", err))
			alertsEngine = nil
		} else {
			logging.WithComponent("start").Info("alerts engine started",
				slog.Int("channels", len(alertsDispatcher.ListChannels())),
			)
		}
	}

	// TASK-332: Wire alert metrics into the Prometheus exporter (polling mode)
	if gwServer != nil && alertsEngine != nil {
		gwServer.SetAlertsMetricsSource(alertsEngine)
	}

	// Initialize dispatcher for task queue (uses store created earlier)
	var dispatcher *executor.Dispatcher
	if store != nil {
		dispatcher = executor.NewDispatcher(store, runner, nil)
		if err := dispatcher.Start(ctx); err != nil {
			logging.WithComponent("start").Warn("Failed to start dispatcher", slog.Any("error", err))
			dispatcher = nil
		} else {
			logging.WithComponent("start").Info("Task dispatcher started")
		}
	}

	// GH-4412: wire the always-on Dispatcher liveness signal into every
	// autopilot controller, unconditionally (unlike SetMonitor above, which
	// only runs in --dashboard mode). Without this, the orphan-running sweep's
	// live-worker exclusion set is silently empty in the common headless
	// (--telegram/--github, no --dashboard) deployment.
	if dispatcher != nil {
		for _, ctrl := range autopilotControllers {
			ctrl.SetDispatcherLiveness(dispatcher)
			// GH-4454: wire the project-scoped queued/running count the
			// lane-starvation reconciler needs, unconditionally alongside the
			// liveness signal above.
			ctrl.SetLaneQueueStatus(dispatcher)
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
			fmt.Printf("● budget enforcement enabled · $%.2f/day, $%.2f/month\n",
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

	// GH-3769: Verify every enabled adapter's credentials concurrently
	// before pollers start, so a dead token surfaces as a loud startup
	// error/alert instead of silently failing on the first poll. Each
	// verifier is also registered with the gateway so /ready reports real
	// per-adapter status.
	adapterVerifiers := buildAdapterVerifiers(cfg)
	runAdapterPreflight(ctx, adapterVerifiers, alertsEngine)
	registerAdapterReadiness(gwServer, adapterVerifiers, verify.DefaultTimeout)

	// GH-929: Start GitHub polling for multiple repos if enabled
	// GH-4110: repo-keyed registry of every GitHub poller (populated by the SDK
	// poller's CreateAndStart). The sub-issue-skip / done-remark / stale-label
	// callbacks route through this so they reach the SDK poller and stay scoped
	// to the correct repo.
	ghPollerRegistry := newGithubPollerRegistry()
	polledRepos := make(map[string]bool) // Track repos already polled to avoid duplicates

	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
		cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled {

		token, tokenSource := resolveGitHubToken(cfg)

		if token != "" {
			client := github.NewClient(token)
			validateGitHubToken(context.Background(), client, tokenSource, alertsEngine)
			interval := cfg.Adapters.GitHub.Polling.Interval
			if interval == 0 {
				interval = 30 * time.Second
			}

			// Determine execution mode from config
			execMode := executionModeSequential // Default to sequential
			waitForMerge := true
			pollInterval := 30 * time.Second
			prTimeout := 1 * time.Hour

			if cfg.Orchestrator != nil && cfg.Orchestrator.Execution != nil {
				execCfg := cfg.Orchestrator.Execution
				execMode = resolveExecutionMode(execCfg.Mode)
				waitForMerge = execCfg.WaitForMerge
				if execCfg.PollInterval > 0 {
					pollInterval = execCfg.PollInterval
				}
				if execCfg.PRTimeout > 0 {
					prTimeout = execCfg.PRTimeout
				}
			}

			// M7 4d.2b: when use_sdk_poller is on, the SDK registration fans out a
			// poller for the default repo AND every projects[] github repo. This is
			// the single source of truth for "is a github poller going to exist".
			sdkGithubPollerEnabled := githubPollerRegistration().Enabled(cfg)

			// SDK registration (poller_github.go) owns the default repo — the
			// in-tree fallback poller has been removed; GitHub polling is SDK-only.
			if cfg.Adapters.GitHub.Repo != "" {
				polledRepos[cfg.Adapters.GitHub.Repo] = true
				if !dashboardMode {
					fmt.Printf("● github polling (sdk, m7 4b) · %s (every %s)\n", cfg.Adapters.GitHub.Repo, interval)
				}
			}

			// GH-929: mark projects with GitHub config as SDK-owned (M7 4d.2b fan-out).
			for _, proj := range cfg.Projects {
				if proj.GitHub == nil || proj.GitHub.Owner == "" || proj.GitHub.Repo == "" {
					continue
				}
				repoFullName := fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)
				if polledRepos[repoFullName] {
					continue // Skip duplicates
				}
				polledRepos[repoFullName] = true

				if !dashboardMode {
					fmt.Printf("● github polling (sdk, m7 4d.2b) · %s (project: %s)\n", repoFullName, proj.Name)
				}
			}

			// M7 4d.2b: SDK pollers are created later (StartAdapterPollers), so gate
			// on "will any github poller exist" — otherwise a flag-on config with
			// all repos SDK-owned would look like "no pollers" and silently skip
			// autopilot startup.
			hasGithubPollers := sdkGithubPollerEnabled

			if !hasGithubPollers {
				logging.WithComponent("github").Warn("GitHub polling enabled but no repos configured — set adapters.github.repo or add project-level github.owner/github.repo",
					slog.Int("pollers", 0))
				// GH-3050: surface second silent autopilot gate. Controllers
				// were created but will not Start because there are no pollers.
				if len(autopilotControllers) > 0 {
					logging.WithComponent("autopilot").Warn(
						"autopilot controllers created but no GitHub pollers configured — autopilot will not start",
						slog.Int("controllers", len(autopilotControllers)),
					)
				}
			}

			if hasGithubPollers {
				if !dashboardMode && execMode == executionModeSequential && waitForMerge {
					fmt.Printf("   ◌ sequential mode · waiting for PR merge before next issue (timeout: %s)\n", prTimeout)
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

					// Scan for recently merged PRs (GH-416). TASK-399/GH-4209: startup
					// uses the wide-lookback catch-up sweep — not the periodic loop's
					// 30-min scanWindow — so a merge that landed while the daemon was
					// down still self-heals its execution row (and any orphaned
					// 'running' rows resolve) instead of staying red in HISTORY forever.
					if err := controller.ScanRecentlyMergedPRsWithWindow(ctx, autopilot.StartupMergedPRLookback); err != nil {
						logging.WithComponent("autopilot").Warn("failed to scan merged PRs",
							slog.String("repo", repoName),
							slog.Any("error", err),
						)
					}

					// GH-2970: startup recovery sweep for stale parent issues
					controller.Start(ctx)

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
					fmt.Printf("● autopilot enabled · %s environment (%d repos)\n", cfg.Orchestrator.Autopilot.Environment, len(autopilotControllers))
				}

				// Start metrics alerter for default controller (GH-728). The
				// alerter's stuck-PR/deadlock detection stays scoped to the
				// default controller (inherently per-repo), but its
				// success_rate/total_active_prs/queue_depth alert metadata is
				// widened to the fleet-wide aggregate (GH-4068).
				if alertsEngine != nil && autopilotController != nil {
					metricsAlerter := autopilot.NewMetricsAlerter(autopilotController, alertsEngine)
					metricsAlerter.SetMetricsSource(autopilotMetricsAggregate)
					go metricsAlerter.Run(ctx)
				}

				// Start metrics persister for default controller (GH-728).
				// Persisted snapshots reflect the fleet-wide aggregate, not
				// just the default controller (GH-4068).
				if store != nil && autopilotController != nil {
					metricsPersister := autopilot.NewMetricsPersister(autopilotController, store)
					metricsPersister.SetMetricsSource(autopilotMetricsAggregate)
					go metricsPersister.Run(ctx)
				}

				// Wire sub-issue PR callback for default controller (GH-594)
				if autopilotController != nil {
					runner.SetOnSubIssuePRCreated(autopilotController.OnPRCreated)
				}

				// GH-3240: mark epic-created sub-issues as processed so
				// findOldestUnprocessedIssue does not re-dispatch them. GH-4110: route
				// through the repo-keyed registry (reaches the SDK poller, whose handle
				// never leaves githubPollerRegistration()) and scope the mark to the
				// sub-issue's repo so it cannot suppress a same-numbered issue elsewhere.
				runner.SetSubIssuePollerSkip(func(n int, repo string) {
					ghPollerRegistry.markProcessed(repo, n)
				})

				// GH-3271: when autopilot marks an issue done after PR-merge, immediately
				// re-mark it processed so a poll tick during the merge→pilot-done label
				// propagation window cannot re-dispatch it. GH-4110: scope to the
				// controller's own repo and route via the registry so the SDK poller is
				// covered too.
				for repoKey, ctrl := range autopilotControllers {
					repoKey := repoKey
					ctrl.SetOnIssueDone(func(n int) {
						ghPollerRegistry.markProcessed(repoKey, n)
					})
				}

				// GH-3954: wire the alerts engine into every controller, not just the
				// default one — fixes the prior pattern where only autopilotController
				// (single-repo backwards-compat default) received alerting, leaving every
				// other project-configured repo's controller unable to fire alerts (e.g.
				// post-tag release verification, GH-3927).
				if alertsEngine != nil {
					for _, ctrl := range autopilotControllers {
						ctrl.SetAlertsEngine(alertsEngine)
					}
				}

				// Wire sub-issue merge-wait so epic sub-issues block until their PR merges
				// (GH-2179). GH-4234: wired unconditionally regardless of waitForMerge —
				// it's cheap when unused, and the per-child decision now lives in the
				// executor's dependency detector (executeSubIssuesTracked), not this flag.
				// wait_for_merge:false stays the effective global default for independent
				// siblings; this callback only fires when a child is actually detected as
				// dependent on a prior sibling (TASK-402).
				if cfg.Adapters.GitHub.Repo != "" {
					parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
					if len(parts) == 2 {
						mergeWaiter := github.NewMergeWaiter(client, parts[0], parts[1], &github.MergeWaiterConfig{
							PollInterval: pollInterval,
							Timeout:      prTimeout,
						})
						runner.SetSubIssueMergeWait(func(ctx context.Context, prNumber int) error {
							_, err := mergeWaiter.WaitForMerge(ctx, prNumber)
							return err
						})
					}
				}
			}

			// Start stale label cleanup for default repo if enabled
			if cfg.Adapters.GitHub.Repo != "" && cfg.Adapters.GitHub.StaleLabelCleanup != nil && cfg.Adapters.GitHub.StaleLabelCleanup.Enabled {
				if store != nil {
					cleanerOpts := []github.CleanerOption{}
					// Clear the poller's processed map when a stale label is removed so
					// the issue can be re-dispatched. GH-4110: this cleaner is scoped to
					// the default repo, so route the clear through the registry keyed by
					// that repo — it reaches the SDK poller (which is the default repo's
					// poller when use_sdk_poller is on) and is a no-op if no poller for
					// that repo is registered.
					defaultRepo := cfg.Adapters.GitHub.Repo
					cleanerOpts = append(cleanerOpts, github.WithOnFailedCleaned(func(issueNumber int) {
						ghPollerRegistry.clearProcessed(defaultRepo, issueNumber)
					}))
					// GH-2402: Same wiring for pilot-blocked so removal allows re-dispatch.
					cleanerOpts = append(cleanerOpts, github.WithOnBlockedCleaned(func(issueNumber int) {
						ghPollerRegistry.clearProcessed(defaultRepo, issueNumber)
					}))
					// GH-2589: On startup recovery, clear the processed map so the issue
					// is re-dispatched on the next poll cycle.
					cleanerOpts = append(cleanerOpts, github.WithOnStartupRecovered(func(issueNumber int) {
						ghPollerRegistry.clearProcessed(defaultRepo, issueNumber)
					}))
					// GH-2354: when pilot-in-progress is stripped from a closed
					// issue, remove its task from the dashboard monitor so it
					// stops showing in the queue view.
					if monitor != nil {
						cleanerOpts = append(cleanerOpts, github.WithOnInProgressCleaned(func(issueNumber int) {
							monitor.Remove(fmt.Sprintf("GH-%d", issueNumber))
						}))
					}
					cleaner, cleanerErr := github.NewCleaner(client, store, cfg.Adapters.GitHub.Repo, cfg.Adapters.GitHub.StaleLabelCleanup, cleanerOpts...)
					if cleanerErr != nil {
						if !dashboardMode {
							fmt.Printf("!  stale label cleanup disabled: %v\n", cleanerErr)
						}
					} else {
						if !dashboardMode {
							fmt.Printf("● stale label cleanup enabled (every %s, in-progress: %s, failed: %s)\n",
								cfg.Adapters.GitHub.StaleLabelCleanup.Interval,
								cfg.Adapters.GitHub.StaleLabelCleanup.Threshold,
								cfg.Adapters.GitHub.StaleLabelCleanup.FailedThreshold)
						}
						// GH-2589: On daemon startup, strip pilot-in-progress labels
						// that have no live execution row. Daemon restart leaves these
						// stuck on issues whose executor was killed mid-flight.
						if n, err := cleaner.StartupRecover(ctx); err != nil {
							logging.WithComponent("github-cleanup").Warn("startup recovery failed",
								slog.Any("error", err))
						} else if !dashboardMode && n > 0 {
							fmt.Printf("⟲ startup recovery · cleared %d stuck pilot-in-progress label(s)\n", n)
						}
						go cleaner.Start(ctx)
					}
				}
			}
		}
	}

	// GH-1847: Start adapter pollers via registry pattern (polling mode)
	pollingDeps := &PollerDeps{
		Cfg:                  cfg,
		ProjectPath:          projectPath,
		Dispatcher:           dispatcher,
		Runner:               runner,
		Monitor:              monitor,
		Program:              program,
		AlertsEngine:         alertsEngine,
		Enforcer:             enforcer,
		Store:                store,
		AutopilotController:  autopilotController,
		AutopilotStateStore:  autopilotStateStore,
		AutopilotControllers: autopilotControllers,
		GitHubPollers:        ghPollerRegistry, // GH-4110: SDK poller registers itself here
		AdapterHealth:        adapterHealthRegistry,
	}
	StartAdapterPollers(ctx, pollingDeps, adapterPollerRegistrations())

	// Start Telegram inbound if enabled.
	if tgHandler != nil {
		if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.SDKBridge {
			// M7 Phase 6 (GH-3470), opt-in: drive inbound through the studio-sdk
			// chat bridge instead of the local long-poll loop. tgHandler implements
			// core.MessageHandler (telegram/sdk_chat.go) and routes through the same
			// comms.Handler, so command + intent handling is unchanged. Outbound
			// stays on the existing messenger, and commands.go + the host-side
			// photo/voice paths are untouched — the full cutover (delete commands.go,
			// rewire the notifier) is a soak-gated follow-up. Default off: when
			// sdk_bridge is unset the original StartPolling path runs verbatim.
			tgBridge := sdkTelegram.New(sdkTelegram.Config{
				BotToken:   cfg.Adapters.Telegram.BotToken,
				AllowedIDs: cfg.Adapters.Telegram.AllowedIDs,
			}, nil).NewChatBridge(sdkCore.ChatDeps{Handler: tgHandler})
			if !dashboardMode {
				fmt.Println("● telegram sdk chat bridge started")
			}
			logging.WithComponent("start").Info("Telegram studio-sdk chat bridge started (sdk_bridge=true)")
			go func() {
				if err := tgBridge.Start(ctx); err != nil {
					logging.WithComponent("telegram").Error("Telegram SDK bridge error", slog.Any("error", err))
				}
			}()
		} else {
			if !dashboardMode {
				fmt.Println("● telegram polling started")
			}
			tgHandler.StartPolling(ctx)
		}
	}

	// Start Slack Socket Mode if enabled (GH-652: wire into polling mode)
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.SocketMode &&
		cfg.Adapters.Slack.AppToken != "" && cfg.Adapters.Slack.BotToken != "" {

		var slackMemberResolver comms.MemberResolver
		if teamAdapter != nil {
			slackMemberResolver = &slack.MemberResolverAdapter{Inner: teamAdapter}
		}

		// slackChatHandler is the pilotChatHandler: it shims SDK events into
		// comms.IncomingMessage and forwards them to slackCommsHandler.
		// SetCommsHandler is called after the bridge messenger is created to
		// break the bridge ↔ Messenger circular dependency.
		slackChatHandler := slack.NewHandler(&slack.HandlerConfig{
			AllowedChannels: cfg.Adapters.Slack.AllowedChannels,
			AllowedUsers:    cfg.Adapters.Slack.AllowedUsers,
			ApprovalHandler: slackApprovalHandler,
		})

		slackBridge := sdkSlack.New(sdkSlack.Config{
			AppToken:        cfg.Adapters.Slack.AppToken,
			BotToken:        cfg.Adapters.Slack.BotToken,
			AllowedChannels: cfg.Adapters.Slack.AllowedChannels,
			AllowedUsers:    cfg.Adapters.Slack.AllowedUsers,
		}, nil).NewChatBridge(sdkCore.ChatDeps{Handler: slackChatHandler})

		var slackClassifierCfg *comms.ClassifierConfig
		if cfg.Adapters.Slack.LLMClassifier != nil {
			slackClassifierCfg = &comms.ClassifierConfig{
				Enabled:     cfg.Adapters.Slack.LLMClassifier.Enabled,
				APIKey:      cfg.Adapters.Slack.LLMClassifier.APIKey,
				HistorySize: cfg.Adapters.Slack.LLMClassifier.HistorySize,
				HistoryTTL:  cfg.Adapters.Slack.LLMClassifier.HistoryTTL,
			}
		}

		var slackBotCfg *comms.BotConfig
		if cfg.Bot != nil {
			slackBotCfg = &comms.BotConfig{
				Enabled:     cfg.Bot.Enabled,
				Model:       cfg.Bot.Model,
				AnswerModel: cfg.Bot.AnswerModel,
				APIKey:      cfg.Bot.APIKey,
				Persona:     cfg.Bot.Persona,
				Retrieval: comms.RetrievalConfig{
					Enabled:  cfg.Bot.Retrieval.Enabled,
					MaxFiles: cfg.Bot.Retrieval.MaxFiles,
					MaxBytes: cfg.Bot.Retrieval.MaxBytes,
				},
			}
		}

		slackCommsHandler := comms.BuildHandler(comms.HandlerDeps{
			Messenger:       sdkshim.MessengerToBridge(slackBridge),
			Runner:          runner,
			Projects:        config.NewSlackProjectSource(cfg),
			ProjectPath:     projectPath,
			Classifier:      slackClassifierCfg,
			Bot:             slackBotCfg,
			MemberResolver:  slackMemberResolver,
			Store:           store,
			IssueCreator:    commsIssueCreator,
			TaskIDPrefix:    "SLACK",
			ExecutorBackend: cfg.Executor,
		})
		slackChatHandler.SetCommsHandler(slackCommsHandler)

		go func() {
			if err := slackBridge.Start(ctx); err != nil {
				logging.WithComponent("slack").Error("Slack Socket Mode error", slog.Any("error", err))
			}
		}()

		if !dashboardMode {
			fmt.Println("● slack socket mode started")
		}
		logging.WithComponent("start").Info("Slack Socket Mode started in polling mode")
	}

	// Discord bot started via poller registry (poller_discord.go)

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
		fmt.Println("\n● starting tui dashboard...")

		// Start background version checker for hot reload (GH-369)
		upgradeCfg := cfg.Upgrade
		if upgradeCfg == nil {
			upgradeCfg = &config.UpgradeConfig{AutoHotUpgrade: true, StaleReleaseThreshold: 3}
		}
		versionChecker := upgrade.NewVersionChecker(version, upgrade.DefaultCheckInterval)
		versionChecker.OnUpdate(func(info *upgrade.VersionInfo) {
			program.Send(dashboard.NotifyUpdateAvailable(info.Current, info.Latest, info.ReleaseNotes)())
			program.Send(dashboard.AddLog(fmt.Sprintf("↑ update available: %s → %s", info.Current, info.Latest))())

			// GH-3790 root cause: this callback used to only log/notify —
			// PerformHotUpgrade never ran unless a human pressed 'u' in the
			// TUI, so the daemon silently sat on stale releases whenever
			// nobody was watching. Auto-enqueue the same request the
			// keypress sends, unless disabled via config.
			if upgradeCfg.AutoHotUpgrade {
				select {
				case upgradeRequestCh <- struct{}{}:
				default:
					// an upgrade is already queued/running
				}
			}
		})
		if upgradeCfg.StaleReleaseThreshold > 0 {
			versionChecker.SetStaleThreshold(upgradeCfg.StaleReleaseThreshold)
			versionChecker.OnStale(func(info *upgrade.VersionInfo) {
				program.Send(dashboard.AddLog(fmt.Sprintf(
					"! %d releases behind (running %s, latest %s) — check ~/.pilot/logs/daemon.log",
					info.ReleasesBehind, info.Current, info.Latest))())
				if alertsEngine != nil {
					alertsEngine.ProcessEvent(alerts.Event{
						Type:      alerts.EventTypeConfigError,
						TaskID:    "self-upgrade",
						TaskTitle: "Self-upgrade staleness check",
						Error: fmt.Sprintf("daemon is %d releases behind (running %s, latest %s)",
							info.ReleasesBehind, info.Current, info.Latest),
						Metadata: map[string]string{
							"check":           "self_upgrade_stale",
							"current_version": info.Current,
							"latest_version":  info.Latest,
							"releases_behind": fmt.Sprintf("%d", info.ReleasesBehind),
						},
						Timestamp: time.Now(),
					})
				}
			})
		}
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
					program.Send(dashboard.AddLog("◌ draining pollers — no new issues will be accepted")())

					// Perform hot upgrade with monitor as TaskChecker
					// Monitor tracks running/queued tasks; upgrade waits for them to finish
					hotUpgrader, err := upgrade.NewHotUpgrader(version, monitor)
					if err != nil {
						program.Send(dashboard.NotifyUpgradeComplete(false, err.Error())())
						program.Send(dashboard.AddLog(fmt.Sprintf("✗ upgrade failed: %v", err))())
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
						program.Send(dashboard.AddLog(fmt.Sprintf("✗ upgrade failed: %v", err))())
						reportUpgradeFailure(alertsEngine, version, info.Latest, err)
					} else {
						// On Unix, process is replaced and this line is never reached.
						// On Windows, hot restart is not supported — binary is installed
						// but process continues. Notify user to restart manually.
						program.Send(dashboard.NotifyUpgradeComplete(true, "")())
						program.Send(dashboard.AddLog("✓ upgrade installed — restart pilot to apply")())
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
						// GH-4490: reconcile against the executions table (source of
						// truth) before rendering — event-driven transitions alone can
						// leave a card stuck at running/100% after a no-commit failure
						// or an externally closed PR that never calls back into monitor.
						if store != nil {
							if reconcileErr := monitor.ReconcileWithStore(store); reconcileErr != nil {
								logging.WithComponent("dashboard").Warn("failed to reconcile monitor with store", slog.Any("error", reconcileErr))
							}
						}
						tasks := convertTaskStatesToDisplay(monitor.GetAll())
						program.Send(dashboard.UpdateTasks(tasks)())
					}
					// GH-4246: pilot_queue_depth had zero production callers and
					// always read 0. Refresh it here from the DB queued/pending
					// count on the same cadence as the task-list refresh.
					// pilot_failed_queue_depth is intentionally left unwired —
					// its documented semantics ("issues with pilot-failed label")
					// are GitHub-issue-label state, not an executions-row status;
					// the DB has no equivalent count to source it from correctly.
					if store != nil && autopilotController != nil {
						if depthErr := autopilot.RefreshQueueDepth(store, autopilotController.Metrics()); depthErr != nil {
							logging.WithComponent("dashboard").Warn("failed to refresh queue depth gauge", slog.Any("error", depthErr))
						}
					}
				}
			}
		}()

		// Add startup logs after TUI starts (Send blocks if Run hasn't been called)
		go func() {
			time.Sleep(100 * time.Millisecond) // Wait for Run() to start
			program.Send(dashboard.AddLog(fmt.Sprintf("● pilot %s started · polling mode", version))())
			if hasTelegram {
				program.Send(dashboard.AddLog("● telegram polling active")())
			}
			hasGitHubPolling := cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled
			if hasGitHubPolling {
				repoCount := countGitHubRepos(cfg)
				if repoCount == 0 {
					program.Send(dashboard.AddLog("○ github polling · no repos configured")())
				} else {
					program.Send(dashboard.AddLog(fmt.Sprintf("● github polling · %d repo(s)", repoCount))())
				}
			}
			hasLinearPolling := cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled
			if hasLinearPolling {
				workspaces := cfg.Adapters.Linear.GetWorkspaces()
				for _, ws := range workspaces {
					program.Send(dashboard.AddLog(fmt.Sprintf("● linear polling · %s/%s", ws.Name, ws.TeamID))())
				}
			}

			// Show GitLab status (GH-2045)
			if cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled {
				if cfg.Adapters.GitLab.Polling != nil && cfg.Adapters.GitLab.Polling.Enabled {
					program.Send(dashboard.AddLog("● gitlab polling active")())
				} else {
					program.Send(dashboard.AddLog("● gitlab webhooks enabled")())
				}
			}
			// Show Jira status (GH-2045)
			if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
				if cfg.Adapters.Jira.Polling != nil && cfg.Adapters.Jira.Polling.Enabled {
					program.Send(dashboard.AddLog("● jira polling active")())
				} else {
					program.Send(dashboard.AddLog("● jira webhooks enabled")())
				}
			}
			// Show Asana status (GH-2045)
			if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
				if cfg.Adapters.Asana.Polling != nil && cfg.Adapters.Asana.Polling.Enabled {
					program.Send(dashboard.AddLog("● asana polling active")())
				} else {
					program.Send(dashboard.AddLog("● asana webhooks enabled")())
				}
			}
			// Show Azure DevOps status (GH-2045)
			if cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled {
				if cfg.Adapters.AzureDevOps.Polling != nil && cfg.Adapters.AzureDevOps.Polling.Enabled {
					program.Send(dashboard.AddLog("● azure devops polling active")())
				} else {
					program.Send(dashboard.AddLog("● azure devops webhooks enabled")())
				}
			}
			// Show Plane status (GH-2045)
			if cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled {
				if cfg.Adapters.Plane.Polling != nil && cfg.Adapters.Plane.Polling.Enabled {
					program.Send(dashboard.AddLog("● plane polling active")())
				} else {
					program.Send(dashboard.AddLog("● plane webhooks enabled")())
				}
			}
			// Show Discord status (GH-2045)
			if cfg.Adapters.Discord != nil && cfg.Adapters.Discord.Enabled {
				program.Send(dashboard.AddLog("● discord gateway enabled")())
			}

			// GH-3600: surface upgrade verification — running version vs the
			// state file is the ground truth, not the env marker alone.
			// GH-879: config is reloaded automatically because exec starts a
			// fresh process.
			switch {
			case bootReconcile != nil && bootReconcile.Outcome == upgrade.BootUpgradeVerified:
				via := "manual restart"
				if bootReconcile.HotExec {
					via = "hot restart, config reloaded"
				}
				program.Send(dashboard.AddLog(fmt.Sprintf("✓ upgrade complete: %s → %s (%s)",
					bootReconcile.PreviousVersion, bootReconcile.NewVersion, via))())
			case bootReconcile != nil && bootReconcile.Outcome == upgrade.BootRestartFailed:
				// Drives the sticky "! UPGRADE FAILED" panel.
				program.Send(dashboard.NotifyUpgradeComplete(false, fmt.Sprintf(
					"Upgrade to %s did not take effect — still running %s. See ~/.pilot/logs/daemon.log",
					bootReconcile.NewVersion, version))())
			case os.Getenv("PILOT_RESTARTED") == "1":
				// Legacy: restart marker without a reconcilable state file
				prevVersion := os.Getenv("PILOT_PREVIOUS_VERSION")
				if prevVersion != "" {
					program.Send(dashboard.AddLog(fmt.Sprintf("✓ upgraded from %s to %s (config reloaded)", prevVersion, version))())
				} else {
					program.Send(dashboard.AddLog("✓ pilot restarted (config reloaded)")())
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
	fmt.Println("\n○ shutting down...")

	// Terminate all running subprocesses (GH-883)
	runner.CancelAll()

	if tgHandler != nil {
		tgHandler.Stop()
	}
	if dispatcher != nil {
		fmt.Println("○ stopping task dispatcher...")
		dispatcher.Stop()
	}
	if briefScheduler != nil {
		briefScheduler.Stop()
	}

	return nil
}

// cleanStartupHooks removes stale pilot hooks from .claude/settings.json
// for the active project and all explicitly configured projects.
func cleanStartupHooks(cfg *config.Config, projectPath string) {
	seen := make(map[string]bool)

	// Clean the resolved projectPath
	if projectPath != "" {
		seen[projectPath] = true
		settingsPath := filepath.Join(projectPath, ".claude", "settings.json")
		if err := executor.CleanStalePilotHooks(settingsPath); err != nil {
			slog.Warn("failed to clean stale hooks", "path", projectPath, "error", err)
		}
	}

	// Clean all explicitly configured projects
	for _, p := range cfg.Projects {
		if p.Path == "" || seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		settingsPath := filepath.Join(p.Path, ".claude", "settings.json")
		if err := executor.CleanStalePilotHooks(settingsPath); err != nil {
			slog.Warn("failed to clean stale hooks", "path", p.Path, "error", err)
		}
	}
}

// storeTaskChecker adapts memory.Store to the github.TaskChecker interface.
// GH-2201: Used by the poller to check if a task is still queued/in-progress
// before allowing retry after the grace period expires.
//
// GH-4276: the SDK's TaskChecker.IsTaskQueued(taskID) interface has no
// projectPath parameter, but task_id is not unique across projects — every
// freshly onboarded repo starts issue numbering at #1. Each poller
// registration is already per-project, so projectPath is captured at
// construction and threaded into the underlying scoped store query rather
// than relying on the interface signature.
type storeTaskChecker struct {
	store       *memory.Store
	projectPath string
}

func (s storeTaskChecker) IsTaskQueued(taskID string) bool {
	queued, err := s.store.IsTaskQueued(taskID, s.projectPath)
	if err != nil {
		return false // Don't block retry on DB errors
	}
	return queued
}

// terminalCompletionChecker adapts *memory.Store to the SDK's
// sdkcore.ExecutionChecker interface (HasCompletedExecution(taskID,
// projectPath) (bool, error)) via executor.HasTerminalCompletion instead of
// Store.HasCompletedExecution directly.
//
// GH-4347: Store.HasCompletedExecution only recognizes a "completed" row with
// a commit/PR deliverable — a no_op outcome ("nothing to change", a common
// legitimate epic sub-issue result) never satisfies it, so the poller's
// pre-dispatch admission check kept treating an already-no_op'd issue as a
// fresh candidate on every poll tick, re-dispatching it indefinitely
// (confirmed via ledger: GH-82 on pilot-canary-sandbox, six no_op rows).
// executor.HasTerminalCompletion is the same broadened "done" definition
// dispatcher.go's own pickup guard (hasTerminalSuccessLedger) uses, so both
// re-arm points agree.
//
// GH-4469: this is also the earliest host-controllable checkpoint in the
// vendored github SDK poller's per-issue candidate loop
// (studio-sdk/sdk/integrations/github/poller.go: hasCompletedExecution runs
// before scope-grouping, the fresh-label GH API refresh, the pre-flight judge
// subprocess, markProcessed, board-sync, and the dispatch/claim-insert
// itself). GH-4391 looped 4,233 times over two days because the ONLY existing
// gate was inside handleIssueGeneric (cmd/pilot/handler_common.go), which the
// poller only reaches AFTER already paying for the judge run and board-sync
// write — a rejection there still cost the full cycle every ~30s. Consulting
// the repick backoff here, before any of that, makes a backoff-gated task
// look identical to an already-completed one to the poller: it's skipped via
// recordSkip(ReasonCompletedExecution) with zero further API calls, judge
// runs, or claim rows until next_allowed_at passes.
type terminalCompletionChecker struct {
	store *memory.Store
}

func (c terminalCompletionChecker) HasCompletedExecution(taskID, projectPath string) (bool, error) {
	if gated, shouldLog := repickBackoff.gateStatus(repickBackoffKey(projectPath, taskID)); gated {
		if shouldLog {
			logging.WithComponent("dispatch").Debug("task in repick backoff window, skipping poller candidacy entirely",
				slog.String("task_id", taskID))
		}
		return true, nil
	}
	return executor.HasTerminalCompletion(c.store, taskID, projectPath)
}

// InvalidateCompletion delegates to the store unchanged — GH-4347 only
// broadens what counts as "done" for the pre-dispatch check above; deleting a
// stale completed record for an explicit retry keeps its existing, stricter
// "genuine completed row" semantics (a no_op row is legitimately terminal and
// disappearing labels/relisting the issue should not delete it).
func (c terminalCompletionChecker) InvalidateCompletion(taskID, projectPath string) error {
	return c.store.InvalidateCompletion(taskID, projectPath)
}

// storeExecutionSaver adapts *memory.Store to the github.ExecutionSaver interface.
// GH-2802: Persists pre-flight rejection records for observability.
type storeExecutionSaver struct {
	store *memory.Store
}

func (s storeExecutionSaver) SaveDeclinedExecution(taskID, projectPath, status, reason string) error {
	now := time.Now()
	return s.store.SaveExecution(&memory.Execution{
		ID:          fmt.Sprintf("%s-preflight-%d", taskID, now.UnixNano()),
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      status,
		Error:       reason,
		CreatedAt:   now,
		CompletedAt: &now,
	})
}

// countGitHubRepos counts unique GitHub repos from the default config and project-level entries.
// applyDashboardBannerMeta populates the dashboard banner with env name,
// model stack (plan/exec), session code, and a per-adapter active/configured
// list so the banner reflects what's actually running this session.
// (GH-2459 — rework of the wiring shipped in GH-2455.)
//
// An adapter contributes a chip to the banner when it is configured (non-nil
// + Enabled) in cfg. Active=true when the corresponding CLI flag was passed
// on this invocation; Active=false renders an empty circle.
func applyDashboardBannerMeta(model *dashboard.Model, cfg *config.Config, cmd *cobra.Command) {
	envName := ""
	if cfg.Orchestrator != nil && cfg.Orchestrator.Autopilot != nil {
		envName = string(cfg.Orchestrator.Autopilot.Environment)
	}

	modelStack := ""
	if cfg.Executor != nil {
		def := shortenModelID(cfg.Executor.DefaultModel)
		var complex string
		if cfg.Executor.ModelRouting != nil {
			complex = shortenModelID(cfg.Executor.ModelRouting.Complex)
		}
		switch {
		case complex != "" && def != "" && complex != def:
			modelStack = complex + " / " + def
		case def != "":
			modelStack = def
		case complex != "":
			modelStack = complex
		}
	}

	flagPassed := func(name string) bool {
		if cmd == nil {
			return true // no cobra context — assume runtime active for back-compat
		}
		f := cmd.Flags().Lookup(name)
		if f == nil {
			return false
		}
		return f.Changed
	}

	var adapters []dashboard.AdapterStatus
	if cfg.Adapters != nil {
		if cfg.Adapters.GitHub != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "GH",
				Active: cfg.Adapters.GitHub.Enabled && flagPassed("github"),
			})
		}
		if cfg.Adapters.Telegram != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "TG",
				Active: cfg.Adapters.Telegram.Enabled && flagPassed("telegram"),
			})
		}
		if cfg.Adapters.Slack != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "SLACK",
				Active: cfg.Adapters.Slack.Enabled && flagPassed("slack"),
			})
		}
		if cfg.Adapters.Discord != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "DISCORD",
				Active: cfg.Adapters.Discord.Enabled && flagPassed("discord"),
			})
		}
		if cfg.Adapters.Linear != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "LINEAR",
				Active: cfg.Adapters.Linear.Enabled && flagPassed("linear"),
			})
		}
		if cfg.Adapters.Jira != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "JIRA",
				Active: cfg.Adapters.Jira.Enabled,
			})
		}
		if cfg.Adapters.GitLab != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "GL",
				Active: cfg.Adapters.GitLab.Enabled,
			})
		}
		if cfg.Adapters.Plane != nil {
			adapters = append(adapters, dashboard.AdapterStatus{
				Name:   "PLANE",
				Active: cfg.Adapters.Plane.Enabled && flagPassed("plane"),
			})
		}
	}

	model.SetBannerMeta(envName, modelStack, nil, time.Now())
	model.SetBannerAdapters(adapters)
}

// resolvedConfigPath returns the user-facing path to ~/.pilot/config.yaml
// (with $HOME contracted to ~) for display in the splash boot block.
func resolvedConfigPath() string {
	home, _ := os.UserHomeDir()
	full := filepath.Join(home, ".pilot", "config.yaml")
	if home != "" && strings.HasPrefix(full, home) {
		return "~" + strings.TrimPrefix(full, home)
	}
	return full
}

// shortenModelID compacts a model identifier for the banner: strips the
// vendor prefix ("claude-", "gpt-", etc.) and uppercases the rest so
// "claude-opus-4-7" → "OPUS-4-7". Returns empty string for empty input.
func shortenModelID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, prefix := range []string{"claude-", "gpt-", "anthropic-", "openai-"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}
	return strings.ToUpper(s)
}

func countGitHubRepos(cfg *config.Config) int {
	seen := make(map[string]bool)
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
		seen[cfg.Adapters.GitHub.Repo] = true
	}
	for _, proj := range cfg.Projects {
		if proj.GitHub != nil && proj.GitHub.Owner != "" && proj.GitHub.Repo != "" {
			seen[fmt.Sprintf("%s/%s", proj.GitHub.Owner, proj.GitHub.Repo)] = true
		}
	}
	return len(seen)
}
