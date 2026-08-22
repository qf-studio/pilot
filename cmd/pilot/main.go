// Dashboard progress test - GH-151
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/adapters/web"
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
	"github.com/qf-studio/pilot/internal/ghbudget"
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
	githubTokenSourceApp    githubTokenSource = "github app (adapters.github.app)"
	githubTokenSourceConfig githubTokenSource = "config (adapters.github.token)"
	githubTokenSourceEnv    githubTokenSource = "env (GITHUB_TOKEN)"
	githubTokenSourceGhCLI  githubTokenSource = "gh CLI (gh auth token)"
	githubTokenSourceNone   githubTokenSource = "none"
)

// ghAppTokenSourceCache memoizes the *github.TokenSource built from
// adapters.github.app — constructing it re-reads and re-parses the PEM
// private key, and TokenSource already owns its own mint/refresh cache, so
// building it once per process (like ghCLITokenCache below) avoids both the
// wasted work and defeats-the-cache bug of rebuilding on every call.
type ghAppTokenSourceCache struct {
	once   sync.Once
	source *github.TokenSource
	err    error
}

func (c *ghAppTokenSourceCache) get(appCfg *github.AppConfig) (*github.TokenSource, error) {
	c.once.Do(func() {
		c.source, c.err = github.NewTokenSource(appCfg)
	})
	return c.source, c.err
}

var ghAppTokenCache = &ghAppTokenSourceCache{}

// mintGitHubAppToken mints (or returns the cached, proactively-refreshed)
// GitHub App installation token for appCfg. Shared by resolveGitHubToken
// (API client construction) and the git credential provider installed on
// the executor for pilot worktree push/fetch (GH-4743) — both paths mint
// through this exact same TokenSource cache, so there is one chokepoint for
// "get me a currently-valid installation token", not two independent ones
// that could drift or double-mint.
func mintGitHubAppToken(ctx context.Context, appCfg *github.AppConfig) (string, error) {
	source, err := ghAppTokenCache.get(appCfg)
	if err != nil {
		return "", err
	}
	return source.Token(ctx)
}

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
// adapters.github.app (GitHub App installation token, GH-4743) →
// adapters.github.token config → GITHUB_TOKEN env → `gh auth token` CLI
// fallback (GH-3718). It consolidates the pattern previously duplicated
// across five call-sites in this file. The returned source lets callers log
// which credential a startup 401 came from.
//
// GH-4743: App auth is preferred when configured — it kills the ec2-user
// gh-CLI OAuth single point of failure and moves off the shared per-user
// 5000/hr rate pool onto a per-installation one. On mint failure it logs
// loudly and falls through to the legacy chain (config token → GITHUB_TOKEN
// env → gh CLI) rather than failing the caller outright, so a transient
// GitHub outage or an expired App key doesn't take the whole daemon down
// when a GITHUB_TOKEN fallback is available.
func resolveGitHubToken(cfg *config.Config) (string, githubTokenSource) {
	if cfg != nil && cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.App != nil {
		appCfg := cfg.Adapters.GitHub.App
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		tok, err := mintGitHubAppToken(ctx, appCfg)
		cancel()
		if err == nil && tok != "" {
			return tok, githubTokenSourceApp
		}
		logging.WithComponent("github-auth").Error(
			"github app installation token mint failed — falling back to GITHUB_TOKEN",
			slog.Int64("app_id", appCfg.AppID),
			slog.Int64("installation_id", appCfg.InstallationID),
			slog.Any("error", err),
		)
	}
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

// errNoGitHubTokenResolved is returned by the TokenFunc built from
// githubTokenFunc when resolveGitHubToken comes up empty (e.g. an App
// installation token mint failed and no fallback credential is configured).
// Surfacing it as a request error keeps a would-be-401 from silently
// carrying an empty Authorization header.
var errNoGitHubTokenResolved = errors.New("no github token resolved (adapters.github.app / adapters.github.token / GITHUB_TOKEN / gh auth token all empty)")

// githubTokenFunc adapts resolveGitHubToken into a github.TokenFunc so a
// long-lived in-tree GitHub client (autopilot's step-log client, a poll-loop
// approval handler, the /ready readiness verifier, ...) resolves the current
// token on every request instead of freezing the boot-time value (GH-4747).
// resolveGitHubToken's App-auth branch already mints through
// ghAppTokenCache's proactively-refreshing TokenSource, so calling it again
// per request is cheap — a cache hit, not a fresh mint — and this is the one
// place that bridges cmd/pilot's token-resolution chokepoint to the client
// package's construction-time source, so callers don't each reimplement it.
func githubTokenFunc(cfg *config.Config) github.TokenFunc {
	return func(ctx context.Context) (string, error) {
		token, _ := resolveGitHubToken(cfg)
		if token == "" {
			return "", errNoGitHubTokenResolved
		}
		return token, nil
	}
}

// invalidateGitHubAppToken returns a callback that discards the cached
// GitHub App installation token, if App auth is configured, so the next
// resolveGitHubToken call re-mints instead of replaying a token GitHub has
// already revoked (GH-4754: a 401 on an App-minted token means the App
// private key rotated or the installation was suspended/reinstalled — the
// old token is dead regardless of what TokenSource's cached expiresAt
// says). Returns nil when App auth isn't configured for cfg — there is
// nothing App-sourced to invalidate, and a 401 in that case came from a
// different credential entirely (config/env/gh-CLI), which this ticket
// doesn't change the handling of.
func invalidateGitHubAppToken(cfg *config.Config) func() {
	if cfg == nil || cfg.Adapters == nil || cfg.Adapters.GitHub == nil || cfg.Adapters.GitHub.App == nil {
		return nil
	}
	appCfg := cfg.Adapters.GitHub.App
	return func() {
		if source, err := ghAppTokenCache.get(appCfg); err == nil {
			source.Invalidate()
		}
	}
}

// newGitHubClient builds an in-tree GitHub client whose token is re-resolved
// on every request via githubTokenFunc (GH-4747) and whose cached App token
// is invalidated on a 401 so the automatic single retry re-mints instead of
// replaying a revoked token (GH-4754). Use this instead of
// github.NewClient(token) for anything held past the call that constructs
// it — a client built once from a static string keeps that string forever,
// which is exactly the bug this ticket fixes for the daemon's long-lived
// clients.
func newGitHubClient(cfg *config.Config) *github.Client {
	return github.NewClientWithTokenFuncAndInvalidate(githubTokenFunc(cfg), invalidateGitHubAppToken(cfg))
}

// newGitHubSDKClient builds a studio-sdk GitHub client whose token is
// re-resolved on every request via githubTokenFunc (TASK-461 Leg 2 — the
// studio-sdk client family's counterpart to newGitHubClient's GH-4747 fix).
// Use this instead of githubSDK.NewClient(token) for anything held past the
// call that constructs it: a client built once from a static string keeps
// that string for the daemon's lifetime, which is exactly the bug that
// leaves App-auth installation tokens (hourly expiry) frozen at boot.
// invalidateGitHubAppToken returns nil when App auth isn't configured — the
// SDK's withAuthRetry treats a nil hook as no-retry (safe) — but the option
// is only passed when there's something to invalidate, to keep intent
// explicit at the call site. extraOpts is exposed so tests can pin the
// client's base URL (githubSDK.WithClientBaseURL) at a local test server
// without duplicating this constructor's wiring; production call sites all
// pass none.
func newGitHubSDKClient(cfg *config.Config, extraOpts ...githubSDK.ClientOption) *githubSDK.Client {
	var opts []githubSDK.ClientOption
	if invalidate := invalidateGitHubAppToken(cfg); invalidate != nil {
		opts = append(opts, githubSDK.WithTokenInvalidate(invalidate))
	}
	opts = append(opts, extraOpts...)
	return githubSDK.NewClientWithTokenFunc(githubSDK.TokenFunc(githubTokenFunc(cfg)), opts...)
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
		newGhGuardCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// gatewayChatNeedsOwnRunner reports whether gateway mode must build a
// dedicated executor.Runner for the chat API on its own, separate from the
// needsPollingInfra block. GH-4843 (D1): chat is an HTTP endpoint, not a
// polling adapter — before this fix, chatEnabled didn't factor into
// needsPollingInfra at all, so a webhooks-only gateway daemon (chat enabled,
// zero polling adapters) left gwRunner nil, silently wiring
// WithChatHandler(nil, ...) and leaving the chat routes unregistered (a 404)
// despite the startup log claiming "Chat API enabled in gateway mode". When
// needsPollingInfra is already true, that block builds gwRunner itself, so
// this only needs to cover the gap case.
//
// Decision: fixed by building a second, chat-scoped runner rather than
// folding chatEnabled into needsPollingInfra's own condition. The latter
// would also pull in that block's polling-only side effects (memory store +
// dispatcher, teams RBAC, approval handlers, autopilot controller, alerts
// engine) for a deployment that only asked for the chat HTTP endpoint —
// scope creep with real side effects (e.g. autopilot starting to act on PRs)
// for config that never opted into it.
func gatewayChatNeedsOwnRunner(chatEnabled, needsPollingInfra bool) bool {
	return chatEnabled && !needsPollingInfra
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
		enableGitlab   bool
		// Mode flags
		noGateway      bool   // Lightweight mode: polling only, no HTTP gateway
		sequential     bool   // Sequential execution mode (one issue at a time)
		envFlag        string // Environment name: dev, stage, prod, or custom configured name
		enableTunnel   bool   // Enable public tunnel (Cloudflare/ngrok)
		teamID         string // Optional team ID for scoping execution
		teamMember     string // Member email for project access scoping
		logFormat      string // Log output format: text or json (GH-847)
		dashboardScope string // Dashboard metrics scope: "project" (default) or "all" (GH-3534)
		allowArchived  bool   // GH-4569: override the archive-sentinel start refusal
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
			applyInputOverrides(cfg, cmd, enableTelegram, enableGithub, enableLinear, enableSlack, enableTunnel, enablePlane, enableDiscord, enableGitlab)

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
				return runPollingMode(cmd, cfg, projectPath, replace, dashboardMode, noGateway, allowArchived, bootReconcile)
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

			// GH-4755: webhook-mode Pilot's GitHub client was built once from
			// github.NewClient(cfg.Adapters.GitHub.Token) inside pilot.New and
			// held for the daemon's lifetime — bypassing the per-request
			// token-resolution chain (App auth / env / gh-CLI fallback,
			// GH-4747) that every polling-mode client already goes through via
			// newGitHubClient. Pass the same kind of client in via an option so
			// webhook mode re-resolves (and invalidates on 401) identically.
			if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
				gwGithubClient := newGitHubClient(cfg)
				pilotOpts = append(pilotOpts, pilot.WithGitHubClient(gwGithubClient))

				// GH-4778: runPollingMode has always validated its GitHub token at
				// startup (validateGitHubToken, GH-3769 preflight below) so a dead
				// App key surfaces as a loud 401 log line before the first poll.
				// Gateway/webhook mode built its client the same way but never ran
				// this check, so a dead key here failed silently on the first
				// inbound webhook instead. gwAlertsEngine isn't constructed yet at
				// this point in gateway-mode setup (it's built further down, only
				// inside the needsPollingInfra branch), so alerting isn't wired
				// here — the loud startup log is what this closes.
				_, gwTokenSource := resolveGitHubToken(cfg)
				validateGitHubToken(context.Background(), gwGithubClient, gwTokenSource, nil)
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
			// GH-4843 (D1): chat is an HTTP endpoint, not a polling adapter, so
			// it must NOT gate on needsPollingInfra the way Telegram/Slack/GitHub
			// polling do — a webhooks-only gateway daemon with chat enabled and
			// zero polling adapters still needs a runner to dispatch chat tasks.
			// Checked separately from needsPollingInfra below so a chat-only
			// deployment doesn't also spin up autopilot/approvals/alerts infra
			// it never asked for.
			chatEnabled := cfg.Adapters.Chat != nil && cfg.Adapters.Chat.Enabled

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
			// GH-4748: hoisted so it's still in scope after the needsPollingInfra
			// block closes, where it's wired into p.Gateway().SetDecisionRecorder
			// (nil when needsPollingInfra is false — mirrors gwAutopilotController
			// et al. above, all hoisted for the same reason).
			var approvalMgr *approval.Manager

			if needsPollingInfra {
				// Create shared runner with config (GH-956: enables worktree isolation)
				var runnerErr error
				gwRunner, runnerErr = executor.NewRunnerWithConfig(cfg.Executor)
				if runnerErr != nil {
					return fmt.Errorf("failed to create executor runner: %w", runnerErr)
				}
				// TASK-286 / GH-3027: refuse sub-issue creation on unmanaged repos.
				gwRunner.SetRepoAllowlist(newConfigRepoAllowlist(cfg))

				// GH-4670: post-run GitHub side-effect audit — detective backstop for
				// the GH-4649 incident class (a session mutating a sibling issue
				// mid-run). Safe to wire unconditionally: no-ops for non-GitHub tasks,
				// fails open on search errors.
				gwRunner.SetGithubSideEffectSearcher(executor.NewGithubSideEffectSearcher())

				// Set up quality gates on runner (GH-3716: resolved per-project,
				// falling back to the global config, then auto-detection).
				gwRunner.SetQualityCheckerFactory(newProjectQualityCheckerFactory(cfg))

				// Set up the Contract Evidence gate's dependency lookup
				// (GH-5009/GH-5013): resolves each task's project's
				// configured contract_dependencies so the gate can enforce
				// producer-source citations for wire-contract changes.
				gwRunner.SetContractDependencyLookup(newProjectContractDependencyLookup(cfg))

				// GH-5022: wire the Contract Evidence gate's content
				// fetcher so citations are independently verified against
				// real producer source rather than trusted outright.
				gwRunner.SetContractContentFetcher(newProjectContractContentFetcher(cfg))

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
				approvalMgr = approval.NewManager(cfg.Approval)

				// Register Telegram approval handler if enabled
				if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" &&
					(cfg.Adapters.Telegram.Approval == nil || cfg.Adapters.Telegram.Approval.Enabled) {
					tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
					gwTgApprovalHandler = approval.NewTelegramHandler(&telegramApprovalAdapter{client: tgClient}, cfg.Adapters.Telegram.ChatID, cfg.Adapters.Telegram.MessageThreadID)
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
							// GH-4747: ghClient is held by the approval handler's poll
							// loop and by autopilot's step-log client for the daemon's
							// lifetime — built from the token source, not the one-off
							// ghToken string, so an App-auth refresh propagates.
							ghClient := newGitHubClient(cfg)

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
							// ghClient stays for the legacy poller/webhook. TASK-461 Leg 2:
							// built via newGitHubSDKClient so the daemon-lifetime client
							// re-resolves its token per request instead of freezing ghToken.
							apGHClient := newGitHubSDKClient(cfg)

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
							// GH-4774: apply the per-project approval overlay when configured.
							if proj := cfg.FindProjectByRepo(cfg.Adapters.GitHub.Repo); proj != nil {
								if proj.Release != nil {
									gwBoardOpts = append(gwBoardOpts, autopilot.WithReleaseOverride(proj.Release))
								}
								if proj.Approval != nil {
									gwBoardOpts = append(gwBoardOpts, autopilot.WithApprovalOverride(proj.Approval))
								}
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
								telegramChannel := alerts.NewTelegramChannel(ch.Name, telegramClient, ch.Telegram.ChatID, ch.Telegram.MessageThreadID)
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
					gwEngineOpts := []alerts.EngineOption{alerts.WithDispatcher(alertsDispatcher), alerts.WithAlertMetrics(alertsMetrics)}
					if gwStore != nil {
						// GH-4562: lets the stuck-task evictor stall an orphan-evicted
						// task's still-alive execution row instead of silently dropping
						// the tracker entry and leaving a live-looking claim behind.
						gwEngineOpts = append(gwEngineOpts, alerts.WithExecutionLifecycle(executor.NewExecutionLifecycle(gwStore)))
						// GH-5095: wire active-alert persistence (GH-4890/PR#5090) —
						// gateway mode's alert engine construction site, same
						// dead-plumbing gap as the polling-mode site above (GH-4716).
						gwEngineOpts = append(gwEngineOpts, alerts.WithActiveAlertStore(gwStore))
					}
					gwAlertsEngine = alerts.NewEngine(alertsCfg, gwEngineOpts...)
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
					scope := ""
					if cfg.Dashboard != nil {
						model.SetStatsWindowDays(cfg.Dashboard.StatsWindowDays)
						scope = cfg.Dashboard.MetricsScopePath
					}
					model.SetMetricsScopePath(scope)
					warnIfMetricsScopeEmpty(gwStore, scope)
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
			} else if gatewayChatNeedsOwnRunner(chatEnabled, needsPollingInfra) {
				// GH-4843 (D1): chat needs a runner to dispatch tasks even when
				// no polling adapter is enabled — build just enough of it
				// (mirrors the runner-setup prefix of the needsPollingInfra
				// block above) without the rest of that block's polling-only
				// infra (memory store/dispatcher, teams, approvals, autopilot,
				// alerts), which a chat-only deployment never asked for.
				var runnerErr error
				gwRunner, runnerErr = executor.NewRunnerWithConfig(cfg.Executor)
				if runnerErr != nil {
					return fmt.Errorf("failed to create executor runner for chat API: %w", runnerErr)
				}
				gwRunner.SetRepoAllowlist(newConfigRepoAllowlist(cfg))
				gwRunner.SetGithubSideEffectSearcher(executor.NewGithubSideEffectSearcher())
				gwRunner.SetQualityCheckerFactory(newProjectQualityCheckerFactory(cfg))
				gwRunner.SetContractDependencyLookup(newProjectContractDependencyLookup(cfg))
				gwRunner.SetContractContentFetcher(newProjectContractContentFetcher(cfg))
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

			// GH-4835: enable the web chat API (console Operator chat panel)
			// in gateway mode whenever adapters.chat.enabled is true. No CLI
			// flag gate like Telegram/Slack polling — this is an HTTP
			// endpoint the console calls, not a polling loop. gwRunner is
			// guaranteed non-nil here when chatEnabled (built above by either
			// the needsPollingInfra block or the chatEnabled else-if), but the
			// nil check guards against silently logging "enabled" on a path
			// that left the handler unwired (GH-4843 D1).
			if chatEnabled {
				pilotOpts = append(pilotOpts, pilot.WithChatHandler(gwRunner, projectPath))
				if gwRunner != nil {
					logging.WithComponent("start").Info("Chat API enabled in gateway mode")
				} else {
					logging.WithComponent("start").Warn("Chat API enabled in config but no runner was constructed — chat routes will not be wired")
				}
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

			// GH-5013: wire the Contract Evidence gate's dependency lookup
			// for webhook/orchestrator mode, mirroring the quality gate wiring above.
			p.SetContractDependencyLookup(newProjectContractDependencyLookup(cfg))

			// GH-5022: wire the Contract Evidence gate's content fetcher for
			// webhook/orchestrator mode, mirroring the dependency lookup wiring above.
			p.SetContractContentFetcher(newProjectContractContentFetcher(cfg))

			// GH-4864: surface the running process's compiled-in version on
			// /health, /api/v1/status, and the pilot_build_info metric — the
			// only status surface a hot restart (syscall.Exec, no PID
			// change) actually shows up on.
			p.Gateway().SetVersion(version)

			// GH-4314: surface adapter goroutine health on /api/v1/status.
			p.Gateway().SetAdapterHealthSource(&adapterHealthProviderAdapter{registry: adapterHealthRegistry})

			// GH-1585: Wire autopilot provider to gateway so /api/v1/autopilot returns live PR data
			if gwAutopilotController != nil {
				p.Gateway().SetAutopilotProvider(&autopilotProviderAdapter{controller: gwAutopilotController})
				p.Gateway().SetMetricsSource(gwAutopilotController.Metrics())
				// GH-2855: wire token/cost/execution counters into executor
				if gwRunner != nil {
					gwRunner.SetMetricsRecorder(gwAutopilotController.Metrics())
					// GH-4390: wire self-heal/pre-execute short-circuit merges into
					// pilot_prs_merged_total via the same controller the scan/
					// handleMerging paths use, so all merge-marking paths share one
					// counted-exactly-once chokepoint.
					gwRunner.SetMergeMetricsRecorder(gwAutopilotController)
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
					// GH-4735: seed pilot_window_* immediately (non-fatal —
					// unlike the lifetime baselines above, a failed window
					// query just means those 4 gauges start at zero until
					// the refresher's first tick, not a stale/reset-looking
					// dashboard), then keep them fresh on a ticker.
					windowDays := config.DefaultDashboardStatsWindowDays
					if cfg.Dashboard != nil {
						windowDays = cfg.Dashboard.StatsWindowDays
					}
					if hydrateErr := autopilot.HydrateWindowStats(gwStore, gwAutopilotController.Metrics(), windowDays); hydrateErr != nil {
						logging.WithComponent("start").Warn("failed to seed window stats", slog.Any("error", hydrateErr))
					}
					autopilot.StartWindowStatsRefresher(context.Background(), gwStore, gwAutopilotController.Metrics(), windowDays, 5*time.Minute)
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
				// GH-4922: eval pass@1 now lives on /metrics instead of the
				// TUI eval panel — same store, same wiring site.
				p.Gateway().SetEvalMetricsSource(gwStore)
			}
			// GH-4748: wire the same DecisionRecorder (approvalMgr) that
			// Telegram/Slack use via WithDecisionRecorder above, so
			// POST /api/v1/approvals/{requestId}/decision persists through the
			// identical Manager.RecordDecision seam in gateway mode. Guard nil:
			// approvalMgr is only non-nil when needsPollingInfra was true: a bare
			// SetDecisionRecorder(approvalMgr) call would store a non-nil
			// approval.DecisionRecorder interface wrapping a nil *approval.Manager
			// (Go's typed-nil-in-interface trap), which the handler's own
			// `recorder == nil` check can't catch and RecordDecision would then
			// panic on a nil receiver.
			if approvalMgr != nil {
				p.Gateway().SetDecisionRecorder(approvalMgr)
			}
			p.Gateway().SetDashboardProjectPath(scopedProjectPath(dashboardScope, projectPath))
			if cfg.Dashboard != nil {
				p.Gateway().SetDashboardStatsWindowDays(cfg.Dashboard.StatsWindowDays)
			}

			// GH-1633: Wire git graph fetcher to gateway so /api/v1/gitgraph returns live git data
			p.Gateway().SetGitGraphFetcher(func(path string, limit int) interface{} {
				return dashboard.FetchGitGraph(path, limit)
			})
			p.Gateway().SetGitGraphPath(projectPath)

			// GH-5003 / TASK-466 read leg: wire the project path backing
			// GET /api/v1/docs/tree and /docs/file (gateway mode). Must
			// mirror the polling-mode wiring below or the routes 404 in
			// this mode (the GH-4784 class; pilot#4835 §wiring precedent).
			p.Gateway().SetDocsProjectPath(projectPath)

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
	cmd.Flags().BoolVar(&allowArchived, "i-know-this-is-an-archive", false, "Override the refusal to start against a ledger marked archived (LEDGER-ARCHIVED sentinel) — forensics only")
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
	cmd.Flags().BoolVar(&enableGitlab, "gitlab", false, "Enable GitLab polling (overrides config)")
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
		{"gitlab", cfg.Adapters != nil && cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled, cfg.Adapters != nil && cfg.Adapters.GitLab != nil},
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
func applyInputOverrides(cfg *config.Config, cmd *cobra.Command, telegramFlag, githubFlag, linearFlag, slackFlag, tunnelFlag, planeFlag, discordFlag, gitlabFlag bool) {
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
	if cmd.Flags().Changed("gitlab") {
		if cfg.Adapters.GitLab == nil {
			cfg.Adapters.GitLab = gitlab.DefaultConfig()
		}
		cfg.Adapters.GitLab.Enabled = gitlabFlag
		if cfg.Adapters.GitLab.Polling == nil {
			cfg.Adapters.GitLab.Polling = &gitlab.PollingConfig{}
		}
		cfg.Adapters.GitLab.Polling.Enabled = gitlabFlag
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

// queueDepthRefreshInterval is how often startQueueDepthRefresh polls the
// store for the queued-task count. 30s is cheap (one COUNT query) and keeps
// the gauge close enough to real-time for alerting/dashboards (GH-4512).
const queueDepthRefreshInterval = 30 * time.Second

// selfUpgradeAdmissionPauseOwner is this daemon's Dispatcher.PauseAdmissionFor
// / ResumeAdmissionFor owner key for the GH-4683 self-upgrade drain. The
// platform-outage breaker (GH-4792) uses its own distinct owner key,
// autopilot.PlatformBreakerAdmissionPauseOwner — defined in the autopilot
// package (not here) since Controller.SetAdmissionPauser also needs it — so
// one owner's resume never undoes the other's still-active pause. See
// Dispatcher.admissionPauseOwners in internal/executor/dispatcher.go.
const selfUpgradeAdmissionPauseOwner = "self-upgrade"

// startPlatformBreakerMonitor runs a periodic tick (GH-4792, TASK-458 part 2)
// that catches the platform-outage breaker's CLOSE transition during a quiet
// spell with no CI activity anywhere to drive it via Observe — Observe's own
// time-based close check (see PlatformBreaker.closeIfQuietLocked) only ever
// runs as a side effect of a CI-failure observation, so a held episode with
// nothing failing anywhere would otherwise never close and its parked PRs
// would never re-drive. EvaluateClose applies the identical check standalone.
//
// GH-4807: the OPEN transition still needs no monitor tick — it's always
// detected synchronously inside whichever controller's handleCIFailed call
// correlates it (alertPlatformBreakerTransition pauses admission and fires
// the alert immediately). But a CLOSE can *also* happen synchronously that
// same way: any CI-failure observation landing after the quiet deadline
// closes the breaker inside Observe itself, and alertPlatformBreakerTransition
// resumes admission and fires the close alert for it right there. That path
// has no access to the fleet-wide `controllers` map (Controller only knows
// its own activePRs), so it never re-drives held PRs — they stayed parked
// until this monitor's own EvaluateClose happened to fire, or the re-adopt
// cap, or a later episode that closed via this monitor's path instead. This
// loop now tracks the breaker's open/closed state across ticks (dropping the
// old `if !breaker.IsOpen() { continue }` pre-check) so it notices an
// Observe-path close within one probe_interval and re-drives for it too —
// without re-alerting, since alertPlatformBreakerTransition already fired
// the close alert exactly once for that transition.
func startPlatformBreakerMonitor(ctx context.Context, breaker *autopilot.PlatformBreaker, dispatcher *executor.Dispatcher, controllers map[string]*autopilot.Controller, alertsEngine *alerts.Engine, pauseAdmissionEnabled bool, interval time.Duration, log *slog.Logger) {
	if breaker == nil {
		return
	}
	if interval <= 0 {
		interval = autopilot.DefaultPlatformBreakerProbeInterval
	}
	if log == nil {
		log = slog.Default()
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		wasOpen := breaker.IsOpen()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				wasOpen = platformBreakerMonitorTick(ctx, breaker, dispatcher, controllers, alertsEngine, pauseAdmissionEnabled, wasOpen, log)
			}
		}
	}()
}

// platformBreakerMonitorTick runs one evaluation of the platform-outage
// breaker's close condition, extracted out of startPlatformBreakerMonitor's
// ticker loop so it can be driven directly (and deterministically) by tests
// instead of waiting on a real time.Ticker. wasOpen is the breaker's open
// state as observed on the PREVIOUS tick (or at monitor start); the return
// value is the state observed at the end of THIS tick, meant to be threaded
// back in as wasOpen on the next call.
func platformBreakerMonitorTick(ctx context.Context, breaker *autopilot.PlatformBreaker, dispatcher *executor.Dispatcher, controllers map[string]*autopilot.Controller, alertsEngine *alerts.Engine, pauseAdmissionEnabled bool, wasOpen bool, log *slog.Logger) bool {
	var result autopilot.PlatformBreakerResult
	if wasOpen {
		// Only worth the time-based check while we last saw the breaker
		// open — a no-op otherwise (mirrors the old IsOpen() pre-check for
		// the never-opened-yet case).
		result = breaker.EvaluateClose()
	}
	nowOpen := breaker.IsOpen()
	// GH-4807: wasOpen but no longer, and THIS tick's EvaluateClose didn't
	// cause it (JustClosed false) — the only other way that happens is an
	// Observe-path close between ticks.
	observePathClose := wasOpen && !nowOpen && !result.JustClosed

	if !result.JustClosed && !observePathClose {
		return nowOpen
	}

	if observePathClose {
		log.Info("platform-outage breaker closed via an Observe-path CI-failure evaluation (caught on next monitor tick) — re-driving held PRs; close alert already fired at the transition")
	} else {
		log.Info("platform-outage breaker closed during a quiet spell (no CI activity to trigger Observe) — re-driving held PRs",
			"correlated_prs", result.CorrelatedPRs)
	}

	if dispatcher != nil && pauseAdmissionEnabled {
		// Idempotent no-op if alertPlatformBreakerTransition already resumed
		// admission for the Observe-path case (ResumeAdmissionFor deletes a
		// possibly-absent owner key) — see Dispatcher.ResumeAdmissionFor.
		dispatcher.ResumeAdmissionFor(autopilot.PlatformBreakerAdmissionPauseOwner)
	}

	for _, ctrl := range controllers {
		ctrl.ReDriveBreakerHeldPRs(ctx)
	}

	if !result.JustClosed {
		// Observe-path close: alertPlatformBreakerTransition already fired
		// the close alert exactly once for this transition — do not
		// duplicate it here.
		return nowOpen
	}

	if alertsEngine == nil {
		log.Error("platform breaker close alert not delivered: alertsEngine is nil")
		return nowOpen
	}
	alertsEngine.ProcessEvent(alerts.Event{
		Type: alerts.EventType("platform_breaker_close"),
		Error: fmt.Sprintf(
			"Platform-outage breaker CLOSED: quiet period elapsed with no new infra/unknown-class CI failure (detected by the periodic monitor during a quiet spell — nothing was failing anywhere to trigger this via a live CI check). Normal CI-failure handling has resumed. PRs held during the outage: %s",
			strings.Join(result.CorrelatedPRs, ", "),
		),
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"prs": strings.Join(result.CorrelatedPRs, ","),
		},
	})
	return nowOpen
}

// startQueueDepthRefresh launches a background loop that periodically calls
// autopilot.RefreshQueueDepth so pilot_queue_depth stays live on any daemon
// serving metrics — not just when the interactive TUI dashboard's own 2s
// refresh loop (cmd/pilot/main.go, dashboard-mode branch) happens to be
// running.
//
// GH-4512: a headless daemon (`pilot start --telegram --github`, no
// --dashboard) previously exported a frozen pilot_queue_depth gauge forever,
// because RefreshQueueDepth's sole call site lived inside the dashboard-only
// ticker. Fleet observability (hosted S2 tenants, S4 C15 Prometheus alarms)
// runs headless by design, so the gauge must be refreshed independently of
// the TUI.
//
// An initial synchronous refresh runs before the loop starts so the gauge is
// correct immediately at boot rather than only after the first tick. The
// loop exits on ctx cancellation, stopping the ticker via defer so the
// goroutine does not leak past daemon shutdown.
func startQueueDepthRefresh(ctx context.Context, store *memory.Store, metrics *autopilot.Metrics, interval time.Duration) {
	if store == nil || metrics == nil {
		return
	}
	if err := autopilot.RefreshQueueDepth(store, metrics); err != nil {
		logging.WithComponent("start").Warn("failed to refresh queue depth gauge", slog.Any("error", err))
	}
	logging.SafeGo("queue-depth-refresh", func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := autopilot.RefreshQueueDepth(store, metrics); err != nil {
					logging.WithComponent("start").Warn("failed to refresh queue depth gauge", slog.Any("error", err))
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
func runPollingMode(cmd *cobra.Command, cfg *config.Config, projectPath string, replace, dashboardMode, noGateway, allowArchived bool, bootReconcile *upgrade.BootReconcileResult) error {
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

	// GH-4391: install the shared GitHub rate-limit budget tracker over
	// http.DefaultTransport before any GitHub client is constructed below.
	// Neither of Pilot's two GitHub HTTP clients (internal/adapters/github,
	// the vendored studio-sdk client) exposes a way to inject a custom
	// transport, and both leave http.Client.Transport nil, so they resolve
	// http.DefaultTransport per-request — installing here observes every
	// outbound GitHub API call from both clients (pollers, autopilot scans,
	// CI watches) without touching either client's source. The 2026-07-16
	// incident this closes: startup rescans across 11 repos burned the
	// entire shared per-user rate budget in under an hour and 403'd every
	// issue poller for 67+ minutes with no visibility into why. See
	// internal/ghbudget for the full writeup.
	rateBudgetFloorPct := ghbudget.DefaultFloorPct
	if cfg.Orchestrator.Autopilot != nil && cfg.Orchestrator.Autopilot.RateLimitFloorPct > 0 {
		rateBudgetFloorPct = cfg.Orchestrator.Autopilot.RateLimitFloorPct
	}
	rateBudgetTracker := ghbudget.NewTracker(rateBudgetFloorPct, logging.WithComponent("ghbudget"))
	http.DefaultTransport = &ghbudget.RoundTripper{Next: http.DefaultTransport, Tracker: rateBudgetTracker}

	// GH-4743: when GitHub App auth is configured, wire pilot worktree git
	// push/fetch to authenticate with the same minted installation token as
	// the API clients above, instead of the ambient GITHUB_TOKEN env / gh
	// CLI credential helper — the SPOF this ticket kills. No-op (nil
	// provider) when App auth isn't configured, so git commands keep using
	// the ambient environment exactly as before the cutover.
	// GH-4746: same cutover, closing the matching gap for `gh` CLI
	// subprocesses (PR creation, issue comments, label ops) — the daemon's
	// highest-volume writes, previously left riding the ambient
	// GITHUB_TOKEN/gh-CLI login even with App auth configured. Both
	// providers mint through the same mintGitHubAppToken/TokenSource cache,
	// so git and gh always agree on the current token. No-op (nil provider)
	// when App auth isn't configured, matching the git provider above.
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.App != nil {
		appCfg := cfg.Adapters.GitHub.App
		executor.SetGitCredentialProvider(func(ctx context.Context) (string, error) {
			return mintGitHubAppToken(ctx, appCfg)
		})
		executor.SetGhCredentialProvider(func(ctx context.Context) (string, error) {
			return mintGitHubAppToken(ctx, appCfg)
		})
	}

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

	// GH-4670: post-run GitHub side-effect audit — detective backstop for the
	// GH-4649 incident class (a session mutating a sibling issue mid-run).
	// auditGithubSideEffects no-ops for non-GitHub tasks and fails open on
	// search errors, so this is safe to wire unconditionally.
	runner.SetGithubSideEffectSearcher(executor.NewGithubSideEffectSearcher())

	// Set up quality gates (GH-207). GH-3716: resolved per-project, falling
	// back to the global config, then auto-detection.
	runner.SetQualityCheckerFactory(newProjectQualityCheckerFactory(cfg))
	logging.WithComponent("start").Info("quality gates enabled for polling mode")

	// GH-5013: wire the Contract Evidence gate's dependency lookup for
	// polling mode, mirroring the quality gate wiring above.
	runner.SetContractDependencyLookup(newProjectContractDependencyLookup(cfg))

	// GH-5022: wire the Contract Evidence gate's content fetcher for
	// polling mode, mirroring the dependency lookup wiring above.
	runner.SetContractContentFetcher(newProjectContractContentFetcher(cfg))

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
		tgApprovalHandlerImpl = approval.NewTelegramHandler(&telegramApprovalAdapter{client: tgApprovalClient}, cfg.Adapters.Telegram.ChatID, cfg.Adapters.Telegram.MessageThreadID)
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
	// GH-4792: hoisted so the part-2 wiring below (admission-pause +
	// periodic probe/close monitor, constructed after dispatcher/alerts
	// exist) can reach the same breaker instance every controller shares.
	var platformBreaker *autopilot.PlatformBreaker
	// platformBreakerPauseAdmissionEnabled and platformBreakerProbeInterval
	// mirror pbCfg's resolved values (below) out to the part-2 wiring point,
	// since pbCfg itself is scoped to the if-block that constructs
	// platformBreaker. platformBreakerPauseAdmissionEnabled defaults false
	// here (matches "breaker disabled" — never asserted) and is only ever
	// set true inside that block, which only runs when pbCfg.Enabled.
	var platformBreakerPauseAdmissionEnabled bool
	var platformBreakerProbeInterval time.Duration
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
			// GH-4747: built from the token source (not the one-off ghToken
			// string) — ghClient is held by the approval handler's poll loop
			// and autopilot's step-log client for the daemon's lifetime.
			ghClient := newGitHubClient(cfg)
			// M7 4d.1: autopilot consumes the studio-sdk client; ghClient (in-tree)
			// stays for the approval handler and legacy paths. TASK-461 Leg 2:
			// built via newGitHubSDKClient so the daemon-lifetime client
			// re-resolves its token per request instead of freezing ghToken.
			apGHClient := newGitHubSDKClient(cfg)

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
			// GH-4391: every controller shares the one process-wide rate-limit
			// budget tracker installed above — the GitHub primary rate limit is
			// pooled per authenticated user across every client/controller, so
			// a single Tracker (not one per repo) is the correct scope.
			autopilotSharedOpts = append(autopilotSharedOpts, autopilot.WithRateBudget(rateBudgetTracker))
			// GH-4791: cross-PR platform-outage correlation breaker, shared
			// across every controller like the rate-budget tracker above — an
			// outage correlated across unrelated PRs is not scoped to a
			// single repo. Disabled by default (Config.PlatformBreaker nil
			// or Enabled false), in which case no option is added and
			// Controller.platformBreaker stays nil — a byte-identical no-op.
			if pbCfg := cfg.Orchestrator.Autopilot.PlatformBreaker; pbCfg != nil && pbCfg.Enabled {
				platformBreaker = autopilot.NewPlatformBreaker(
					pbCfg.MinCorrelatedPRs,
					pbCfg.CorrelationWindow,
					pbCfg.QuietPeriod,
					logging.WithComponent("platform-breaker"),
				)
				autopilotSharedOpts = append(autopilotSharedOpts, autopilot.WithPlatformBreaker(platformBreaker))
				// GH-4792 part 2: admission-pause opt-out and periodic
				// probe/close-monitor interval, resolved here (config is in
				// scope) and carried out to the wiring point below via the
				// hoisted vars above.
				platformBreakerPauseAdmissionEnabled = pbCfg.PauseAdmissionEnabled()
				platformBreakerProbeInterval = pbCfg.ProbeInterval
				if platformBreakerProbeInterval <= 0 {
					platformBreakerProbeInterval = autopilot.DefaultPlatformBreakerProbeInterval
				}
				// GH-4814: enabling the breaker was otherwise silent at
				// startup — NewPlatformBreaker logs nothing at construction
				// and the component=platform-breaker logger only speaks on
				// the OPEN transition, so confirming today's enablement
				// required tracing this config decode path by hand. One
				// INFO line with every resolved setting (including the two,
				// probe_interval/pause_admission, NewPlatformBreaker itself
				// never sees) fixes that.
				logging.WithComponent("platform-breaker").Info("platform-outage breaker enabled",
					slog.Int("min_correlated_prs", platformBreaker.MinCorrelatedPRs()),
					slog.Duration("correlation_window", platformBreaker.CorrelationWindow()),
					slog.Duration("quiet_period", platformBreaker.QuietPeriod()),
					slog.Duration("probe_interval", platformBreakerProbeInterval),
					slog.Bool("pause_admission", platformBreakerPauseAdmissionEnabled),
				)
			}
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
						// GH-4774: apply the per-project approval overlay when configured;
						// nil is a no-op (inherits the resolved env/global RequireApproval
						// gate and ApprovalSource channel).
						if proj.Approval != nil {
							ctrlOpts = append(ctrlOpts, autopilot.WithApprovalOverride(proj.Approval))
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
				// GH-4774: apply the per-project approval overlay when configured;
				// nil is a no-op (inherits the resolved env/global RequireApproval
				// gate and ApprovalSource channel).
				if proj.Approval != nil {
					ctrlOpts = append(ctrlOpts, autopilot.WithApprovalOverride(proj.Approval))
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
	store, err := memory.NewStoreGuarded(cfg.Memory.Path, allowArchived)
	if err != nil {
		var splitBrain *memory.ErrSplitBrainLedger
		if errors.As(err, &splitBrain) {
			logging.WithComponent("start").Error("refusing to start: possible shadow ledger detected", slog.Any("error", err))
			return err
		}
		var archived *memory.ErrLedgerArchived
		if errors.As(err, &archived) {
			logging.WithComponent("start").Error("refusing to start: ledger marked archived", slog.Any("error", err))
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
		// GH-4784: enforce bearer auth on /api/v1 when an api-token is
		// configured (cfg.Auth); empty/default token keeps today's open
		// behavior (loopback bind is the mitigant) via a nil authConfig.
		gwServer = gateway.NewServerWithAuth(cfg.Gateway, cfg.GatewayAuthConfig())
		// GH-4864: see the p.Gateway().SetVersion call above for rationale.
		gwServer.SetVersion(version)
		gwServer.SetAdapterHealthSource(&adapterHealthProviderAdapter{registry: adapterHealthRegistry})

		if autopilotController != nil {
			gwServer.SetAutopilotProvider(&autopilotProviderAdapter{controller: autopilotController})
			// GH-2855: wire token/cost/execution counters into executor
			runner.SetMetricsRecorder(autopilotController.Metrics())
			// GH-4390: wire self-heal/pre-execute short-circuit merges into
			// pilot_prs_merged_total. Fans out to every controller (not just
			// the default) since self-heal can fire for any project's tasks
			// — each controller's own projectPath scoping
			// (Controller.RecordExternalMerge) ensures exactly one actually
			// records, matching the approval-routing shape used for
			// MultiControllerStateWriter above.
			var mergeRecorderControllers []*autopilot.Controller
			for _, c := range autopilotControllers {
				mergeRecorderControllers = append(mergeRecorderControllers, c)
			}
			runner.SetMergeMetricsRecorder(autopilot.NewMultiControllerMergeRecorder(mergeRecorderControllers...))
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
				// GH-4735: seed pilot_window_* immediately (non-fatal), then
				// keep it fresh on a ticker — same reasoning as the gateway-
				// mode call site above.
				windowDays := config.DefaultDashboardStatsWindowDays
				if cfg.Dashboard != nil {
					windowDays = cfg.Dashboard.StatsWindowDays
				}
				if hydrateErr := autopilot.HydrateWindowStats(store, autopilotController.Metrics(), windowDays); hydrateErr != nil {
					logging.WithComponent("start").Warn("failed to seed window stats", slog.Any("error", hydrateErr))
				}
				autopilot.StartWindowStatsRefresher(ctx, store, autopilotController.Metrics(), windowDays, 5*time.Minute)
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
				// TASK-461 Leg 2: built via newGitHubSDKClient so this
				// daemon-lifetime client re-resolves its token per request
				// instead of freezing the one-off token string above (which
				// is retained only to gate whether a client is built at all).
				ghClient := newGitHubSDKClient(cfg)
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
			// GH-4922: eval pass@1 now lives on /metrics instead of the TUI
			// eval panel — same store, same wiring site.
			gwServer.SetEvalMetricsSource(store)
		}
		// GH-4748: wire the same DecisionRecorder (approvalMgr) that
		// Telegram/Slack use via WithDecisionRecorder above, so
		// POST /api/v1/approvals/{requestId}/decision persists through the
		// identical Manager.RecordDecision seam in polling mode too (the
		// GH-4738 lesson: wire both gateway-mode and polling-mode paths).
		gwServer.SetDecisionRecorder(approvalMgr)
		// GH-4835: wire the web chat API (console Operator chat panel) in
		// polling mode too — the gateway-mode leg is in internal/pilot/pilot.go
		// (WithChatHandler). Missing either means the chat routes silently
		// don't exist in that daemon mode; see SetChatAPI's doc comment.
		if cfg.Adapters.Chat != nil && cfg.Adapters.Chat.Enabled {
			webMessenger := web.NewMessenger()
			webCommsHandler := comms.BuildHandler(comms.HandlerDeps{
				Messenger:       webMessenger,
				Runner:          runner,
				Projects:        config.NewProjectSource(cfg),
				ProjectPath:     projectPath,
				RateLimit:       cfg.Adapters.Chat.RateLimit,
				Store:           store,
				TaskIDPrefix:    "WEB",
				ExecutorBackend: cfg.Executor,
			})
			gwServer.SetChatAPI(web.NewAPI(webCommsHandler, webMessenger))
			logging.WithComponent("start").Info("Chat API enabled in polling mode")
		}
		gwServer.SetDashboardProjectPath(projectPath)
		if cfg.Dashboard != nil {
			gwServer.SetDashboardStatsWindowDays(cfg.Dashboard.StatsWindowDays)
		}
		gwServer.SetGitGraphFetcher(func(path string, limit int) interface{} {
			return dashboard.FetchGitGraph(path, limit)
		})
		gwServer.SetGitGraphPath(projectPath)

		// GH-5003 / TASK-466 read leg: wire the project path backing
		// GET /api/v1/docs/tree and /docs/file (polling mode). Must mirror
		// the gateway-mode wiring above or the routes 404 in this mode.
		gwServer.SetDocsProjectPath(projectPath)
		go func() {
			addr := fmt.Sprintf("%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
			logging.WithComponent("gateway").Info("gateway started in background", "addr", addr)
			if err := gwServer.Start(ctx); err != nil && ctx.Err() == nil {
				logging.WithComponent("gateway").Error("gateway background error", "error", err)
			}
		}()

		// GH-4512: refresh pilot_queue_depth from the daemon lifecycle itself
		// (gated on the same !noGateway && cfg.Gateway != nil condition that
		// starts /metrics above), so headless runs keep the gauge live. The
		// dashboard-mode branch further down still runs its own 2s refresh
		// for snappier TUI updates — both writers are idempotent sets.
		if autopilotController != nil {
			startQueueDepthRefresh(ctx, store, autopilotController.Metrics(), queueDepthRefreshInterval)
		}
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
		scope := ""
		if cfg.Dashboard != nil {
			model.SetStatsWindowDays(cfg.Dashboard.StatsWindowDays)
			scope = cfg.Dashboard.MetricsScopePath
		}
		model.SetMetricsScopePath(scope)
		warnIfMetricsScopeEmpty(store, scope)
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

			// TASK-420/GH-4537: this was the one UpdateTasks producer of the
			// five that skipped the GH-4490 reconcile step before rendering —
			// the periodic ticker below (~2s cadence) self-heals eventually,
			// but this path could still push a queue snapshot with a stale
			// Monitor status (e.g. a false-complete card) straight to the
			// dashboard on every progress tick in between. Reconcile against
			// the executions table (ledger authoritative) the same way the
			// ticker does before converting.
			if store != nil {
				if reconcileErr := monitor.ReconcileWithStore(store); reconcileErr != nil {
					logging.WithComponent("dashboard").Warn("failed to reconcile monitor with store", slog.Any("error", reconcileErr))
				}
			}
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
				// GH-4747: commsIssueCreator is held by comms.Handler for the
				// daemon's lifetime, so build from the token source rather
				// than the one-off ghToken string.
				ghIssueClient := newGitHubClient(cfg)
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
			ChatID:          cfg.Adapters.Telegram.ChatID,
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
		// GH-4550: use EnvironmentName() (which honors DefaultEnvironment)
		// rather than the raw legacy Environment field, so this line reports
		// the environment actually resolved by ResolvedEnv() even when
		// default_environment is set in config with no --env flag.
		logging.WithComponent("start").Info("autopilot enabled",
			slog.String("environment", cfg.Orchestrator.Autopilot.EnvironmentName()),
			slog.Bool("auto_merge", cfg.Orchestrator.Autopilot.AutoMerge),
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
					telegramChannel := alerts.NewTelegramChannel(ch.Name, telegramClient, ch.Telegram.ChatID, ch.Telegram.MessageThreadID)
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

		engineOpts := []alerts.EngineOption{alerts.WithDispatcher(alertsDispatcher), alerts.WithAlertMetrics(alertsMetrics)}
		if store != nil {
			// GH-4562: lets the stuck-task evictor stall an orphan-evicted
			// task's still-alive execution row instead of silently dropping
			// the tracker entry and leaving a live-looking claim behind.
			engineOpts = append(engineOpts, alerts.WithExecutionLifecycle(executor.NewExecutionLifecycle(store)))
			// GH-5095: wire active-alert persistence (GH-4890/PR#5090) — without
			// this, WithActiveAlertStore is never called on the polling-mode
			// daemon's only alert engine, so a real restart loses all
			// currently-firing alert state despite the persistence layer
			// existing and being fully tested (GH-4716 dead-plumbing class).
			engineOpts = append(engineOpts, alerts.WithActiveAlertStore(store))
		}
		alertsEngine = alerts.NewEngine(alertsCfg, engineOpts...)
		if err := alertsEngine.Start(ctx); err != nil {
			logging.WithComponent("start").Error("alert engine failed to start — downstream alerters will be silently disabled; check alerts config", slog.Any("error", err))
			alertsEngine = nil
		} else {
			logging.WithComponent("start").Info("alerts engine started",
				slog.Int("channels", len(alertsDispatcher.ListChannels())),
			)

			// GH-4716: this is the polling-mode daemon's only alert engine —
			// wire runner's AlertEventProcessor here, or every executor-side
			// emitAlertEvent call (self-review/decompose/ghguard/sideeffect
			// dead-man trackers, and now the TASK-441 L5 finish-tripwire
			// sweep below) silently drops its events for the entire life of
			// this process. Previously nothing called
			// runner.SetAlertProcessor on this code path at all — the
			// dead-man trackers registered against deps.AlertsEngine in
			// startGithubSDKPollerForRepo (poller_github.go) were counting
			// correctly but had no wired processor to receive attempts from,
			// so they could never actually fire. internal/pilot/pilot.go's
			// initAlerts wires the equivalent orchestrator/webhook-mode path
			// (pilot.New) the same way; this is polling mode's counterpart.
			alertProcessor := alerts.NewEngineAdapter(alertsEngine)
			runner.SetAlertProcessor(alertProcessor)
			alertsEngine.WireLifecycleAlertProcessor(alertProcessor)

			// TASK-441 L5 (GH-4716): register the four finish-tripwire
			// dead-man trackers once at startup, mirroring the self-review/
			// label-lifecycle registrations (L2, GH-4709) in
			// poller_github.go.
			for _, name := range executor.FinishTripwireTrackerNames {
				alertsEngine.RegisterDeadManTracker(name, alerts.AlertTypeFinishTripwireFailureStreak, alerts.DefaultDeadManFailureThreshold, alerts.DefaultDeadManWindow)
			}
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

	// GH-4609: wire the Dispatcher's live-worker liveness signal (and the
	// store, for its execution_event heartbeat fallback) into the monitor so
	// ReconcileDeadOwners can finalize a dead-owner active-registry entry —
	// no live worker holding it, execution row not progressing — before it
	// blocks self-upgrade drain forever (see monitor.GetRunningTaskIDs,
	// consumed as upgrade.TaskChecker below via NewHotUpgrader). monitor is
	// only non-nil in --dashboard mode (see above).
	if monitor != nil && dispatcher != nil {
		monitor.SetLiveWorkerChecker(dispatcher)
		if store != nil {
			monitor.SetExecutionStore(store)
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
	if cfg.Alerts != nil {
		startAdapterHealthLoop(ctx, adapterVerifiers, alertsEngine, cfg.Alerts.ResolvedHealthCheckInterval())
	}
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
			// GH-4778: use newGitHubClient(cfg) instead of github.NewClient(token) —
			// this client is held past construction by the merge-waiter (daemon-lifetime
			// merge-wait callback) and the stale-label cleaner (periodic loop), so a
			// static token here would freeze and 401 after App-token rotation (GH-4755).
			client := newGitHubClient(cfg)
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

				// GH-4391: startup scan window + inter-repo stagger interval come
				// from autopilot config, falling back to the pre-GH-4391 wide
				// 30-day lookback (and no stagger) when autopilot config is unset
				// — DefaultConfig() supplies the new defaults (72h window, 3s
				// stagger) in the common case.
				startupScanWindow := autopilot.StartupMergedPRLookback
				var scanStagger time.Duration
				if cfg.Orchestrator.Autopilot != nil {
					if cfg.Orchestrator.Autopilot.StartupMergedPRScanWindow > 0 {
						startupScanWindow = cfg.Orchestrator.Autopilot.StartupMergedPRScanWindow
					}
					scanStagger = cfg.Orchestrator.Autopilot.ScanStaggerInterval
				}

				// GH-4391: stagger per-repo startup scans (jittered, serialized)
				// instead of bursting every repo's ScanExistingPRs +
				// ScanRecentlyMergedPRsAtStartup back-to-back — that burst is what
				// exhausted the entire shared GitHub rate budget within an hour in
				// the 2026-07-16 incident and 403'd every issue poller for 67+
				// minutes. Start/Run still fire immediately after each repo's own
				// scan completes, so a staggered repo isn't left un-polled any
				// longer than its own scan takes.
				autopilot.StaggerRepoScans(ctx, autopilotControllers, scanStagger, func(ctx context.Context, repoName string, controller *autopilot.Controller) {
					// Scan for existing PRs
					if err := controller.ScanExistingPRs(ctx); err != nil {
						logging.WithComponent("autopilot").Warn("failed to scan existing PRs",
							slog.String("repo", repoName),
							slog.Any("error", err),
						)
					}

					// Scan for recently merged PRs (GH-416). TASK-399/GH-4209: startup
					// uses a wide-lookback catch-up sweep — not the periodic loop's
					// 30-min scanWindow — so a merge that landed while the daemon was
					// down still self-heals its execution row (and any orphaned
					// 'running' rows resolve) instead of staying red in HISTORY
					// forever. GH-4391: the window is now configurable (default 72h,
					// down from the previous hardcoded 720h) and shrinks further on
					// restart via a per-repo cursor persisted across process
					// lifetimes (ScanRecentlyMergedPRsAtStartup).
					if err := controller.ScanRecentlyMergedPRsAtStartup(ctx, startupScanWindow); err != nil {
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
				})

				if len(autopilotControllers) > 0 && !dashboardMode {
					// GH-4611: use EnvironmentName() (same resolution path as the
					// "autopilot enabled" structured log above) rather than the
					// raw legacy Environment field, so the banner never shows a
					// stale/unresolved value when default_environment is set in
					// config with no --env flag.
					fmt.Printf("● autopilot enabled · %s environment (%d repos)\n", cfg.Orchestrator.Autopilot.EnvironmentName(), len(autopilotControllers))
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

				// GH-4792 (TASK-458 part 2): wire the shared Dispatcher into
				// every controller as an AdmissionPauser so the OPEN
				// transition (detected synchronously inside whichever
				// controller's handleCIFailed call correlates the breaker
				// open, see alertPlatformBreakerTransition) pauses new-work
				// admission immediately — not on the periodic monitor's next
				// tick, which only exists to catch the CLOSE transition
				// during a quiet spell with no CI activity anywhere to
				// trigger Observe. Admission pause itself is config-gated
				// (platformBreakerPauseAdmissionEnabled, resolved from
				// PlatformBreakerConfig.PauseAdmissionEnabled() above,
				// default on) via one owner key
				// (autopilot.PlatformBreakerAdmissionPauseOwner) distinct
				// from the GH-4683 self-upgrade drain's, so neither resume
				// undoes the other's still-active pause — but the monitor
				// itself always runs when the breaker is enabled, since
				// held-PR re-drive and the close alert are NOT gated by the
				// admission-pause opt-out.
				if platformBreaker != nil {
					if dispatcher != nil && platformBreakerPauseAdmissionEnabled {
						for _, ctrl := range autopilotControllers {
							ctrl.SetAdmissionPauser(dispatcher)
						}
					}
					startPlatformBreakerMonitor(ctx, platformBreaker, dispatcher, autopilotControllers, alertsEngine, platformBreakerPauseAdmissionEnabled, platformBreakerProbeInterval, logging.WithComponent("platform-breaker"))
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
		//
		// GH-4609: drainAlertGate tracks consecutive drain-timeout failures
		// across retries of this loop (versionChecker re-fires OnUpdate every
		// DefaultCheckInterval while an update stays available) so the alert
		// below only pages an operator starting on the second consecutive
		// drain timeout instead of every 5-minute retry.
		drainAlertGate := &drainTimeoutAlertGate{}
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

					// GH-4683: pause admission before draining — the dispatcher
					// stops starting any NEW execution (queued rows stay
					// queued; pollers may keep enqueueing), so the drain below
					// only has to wait out tasks already running. Resumed on
					// every path out of this attempt except a successful Unix
					// exec-restart, where the whole process is replaced and
					// nothing after PerformHotUpgrade runs anyway.
					if dispatcher != nil {
						dispatcher.PauseAdmissionFor(selfUpgradeAdmissionPauseOwner)
					}
					program.Send(dashboard.AddLog("◌ pausing task admission — queued work waits, running tasks finish normally")())

					// Perform hot upgrade with a running-only TaskChecker
					// (GH-4683): the drain waits solely for tasks the Monitor
					// currently shows as RUNNING, never queued ones — see
					// runningOnlyTaskChecker.
					hotUpgrader, err := upgrade.NewHotUpgrader(version, &runningOnlyTaskChecker{monitor: monitor})
					if err != nil {
						if dispatcher != nil {
							dispatcher.ResumeAdmissionFor(selfUpgradeAdmissionPauseOwner)
						}
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
						if dispatcher != nil {
							dispatcher.ResumeAdmissionFor(selfUpgradeAdmissionPauseOwner)
						}
						program.Send(dashboard.NotifyUpgradeComplete(false, err.Error())())
						program.Send(dashboard.AddLog(fmt.Sprintf("✗ upgrade failed: %v", err))())
						if drainAlertGate.observe(err) {
							reportUpgradeFailure(alertsEngine, version, info.Latest, err)
						} else {
							slog.Warn("self-upgrade drain timeout — retrying next check, not alerting yet (1st consecutive occurrence)",
								slog.String("current_version", version),
								slog.String("target_version", info.Latest),
								slog.Any("error", err),
							)
						}
					} else {
						// On Unix, process is replaced via exec and this line is
						// never reached, so ResumeAdmission below is moot — a
						// brand new process starts unpaused. On Windows, hot
						// restart is not supported: the OLD process keeps
						// running (the new binary is only installed on disk),
						// so admission must resume here or the daemon would stay
						// paused indefinitely until a manual restart. GH-4683.
						if dispatcher != nil {
							dispatcher.ResumeAdmissionFor(selfUpgradeAdmissionPauseOwner)
						}
						drainAlertGate.observe(nil)
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

// storeExecutionSaver adapts *memory.Store to the sdk core.ExecutionSaver /
// core.ExecutionSaverV2 interfaces. GH-2802: Persists pre-flight rejection
// records for observability. Must stay a VALUE receiver — the poller wires a
// value (poller_github.go), and a pointer receiver would silently satisfy
// only core.ExecutionSaver, dropping back to the legacy repo-blind path
// (see var _ core.ExecutionSaverV2 assertion below).
type storeExecutionSaver struct {
	store *memory.Store
	// cfg resolves the declined issue's owning project (GH-4845), same
	// precedence idiom as handleIssueGeneric's canary stamp fix (GH-4833).
	// Nil is tolerated — IsCanary then stays false, matching pre-GH-4845
	// behavior.
	cfg *config.Config
	// controller is optional (nil for a repo with no autopilot controller
	// wired) — when set, a preflight decline of a Pilot-spawned fix issue
	// reacts via the owner-death path (GH-4842) instead of only being
	// recorded for observability.
	controller *autopilot.Controller
}

// ownerDeathReactTimeout bounds the synchronous owner-death reaction
// (GitHub issue fetch + label/comment writes) fired from inside
// SaveDeclinedExecutionRecord, which the SDK poller calls inline on every
// pre-flight decline.
const ownerDeathReactTimeout = 15 * time.Second

// SaveDeclinedExecutionRecord implements sdkCore.ExecutionSaverV2
// (studio-sdk v0.35.0+, sdk PR#112). rec carries the declined issue's repo
// identity (RepoOwner/RepoName) — the SDK poller passes it on every
// pre-flight decline — which lets us resolve the owning project via
// FindProjectByRepo and stamp IsCanary correctly (GH-4845). Before this, the
// legacy SaveDeclinedExecution only had ProjectPath, which collides across
// projects sharing a local checkout (the same GH-4833 collision that
// corrupted canary attribution for dispatched tasks, fixed for those in
// handleIssueGeneric by PR#4837). An empty RepoOwner/RepoName (e.g. the
// legacy SaveDeclinedExecution delegation below) falls back to the
// path-only lookup, preserving prior behavior byte-for-byte.
func (s storeExecutionSaver) SaveDeclinedExecutionRecord(rec sdkCore.DeclinedExecutionRecord) error {
	now := time.Now()

	isCanary := false
	if s.cfg != nil {
		var proj *config.ProjectConfig
		if rec.RepoOwner != "" && rec.RepoName != "" {
			proj = s.cfg.FindProjectByRepo(fmt.Sprintf("%s/%s", rec.RepoOwner, rec.RepoName))
		}
		if proj == nil {
			proj = s.cfg.GetProject(rec.ProjectPath)
		}
		if proj != nil {
			isCanary = proj.Canary
		}
	}

	err := s.store.SaveExecution(&memory.Execution{
		ID:          fmt.Sprintf("%s-preflight-%d", rec.TaskID, now.UnixNano()),
		TaskID:      rec.TaskID,
		ProjectPath: rec.ProjectPath,
		Status:      rec.Status,
		Error:       rec.Reason,
		CreatedAt:   now,
		CompletedAt: &now,
		IsCanary:    isCanary,
	})

	// GH-4842: a preflight decline is owner death for a Pilot-spawned fix
	// issue — react (re-arm/escalate its source) using the same signal the
	// SDK already produces here, instead of adding a new poller.
	if rec.Status == "declined-preflight" && s.controller != nil {
		var issueNum int
		if _, scanErr := fmt.Sscanf(rec.TaskID, "GH-%d", &issueNum); scanErr == nil && issueNum > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), ownerDeathReactTimeout)
			s.controller.ReactToDeclinedFixIssue(ctx, issueNum, rec.Reason)
			cancel()
		}
	}

	return err
}

// SaveDeclinedExecution implements the legacy sdkCore.ExecutionSaver
// interface, kept for interface compatibility with any caller that only
// knows the narrower type. Delegates to SaveDeclinedExecutionRecord with an
// empty repo identity, which falls back to the path-only project lookup —
// identical to this method's pre-GH-4845 behavior.
func (s storeExecutionSaver) SaveDeclinedExecution(taskID, projectPath, status, reason string) error {
	return s.SaveDeclinedExecutionRecord(sdkCore.DeclinedExecutionRecord{
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      status,
		Reason:      reason,
	})
}

// var _ sdkCore.ExecutionSaverV2 assertion: fail the build if
// storeExecutionSaver ever stops satisfying ExecutionSaverV2, so the SDK
// poller's type-assert (poller.go handlePreFlightReject) can never silently
// regress to the repo-blind legacy path (GH-4845 fail-when-unwired guard).
var _ sdkCore.ExecutionSaverV2 = storeExecutionSaver{}

// warnIfMetricsScopeEmpty logs a startup warning when a configured
// dashboard.metrics_scope_path matches zero executions in the store. This is
// otherwise silent: metrics_scope_path is matched with an exact string
// comparison against the uncanonicalized executions.project_path column
// (store.go's GetRecentExecutions/GetLifetimeTokens/GetWindowedStats), so a
// trailing-slash or symlink variant (e.g. /var vs /private/var) of the real
// project path yields all-zero scoped metrics with no other indication
// anything is wrong (GH-4832).
func warnIfMetricsScopeEmpty(store *memory.Store, scope string) {
	if store == nil || scope == "" {
		return
	}
	counts, err := store.GetLifetimeTaskCounts(scope)
	if err != nil {
		return
	}
	if counts.Total == 0 {
		logging.WithComponent("start").Warn(
			"dashboard.metrics_scope_path matches zero executions — check for a trailing slash or symlink variant (e.g. /var vs /private/var) of the project_path recorded in the store",
			slog.String("metrics_scope_path", scope),
		)
	}
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
		// GH-4611: use EnvironmentName() (same resolution path as the
		// "autopilot enabled" structured log and startup banner) rather than
		// the raw legacy Environment field, so the dashboard banner never
		// shows a stale value when default_environment is set in config
		// with no --env flag.
		envName = cfg.Orchestrator.Autopilot.EnvironmentName()
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
