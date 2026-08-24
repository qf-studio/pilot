package pilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/asana"
	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/adapters/gitlab"
	"github.com/qf-studio/pilot/internal/adapters/jira"
	"github.com/qf-studio/pilot/internal/adapters/linear"
	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/adapters/web"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/orchestrator"
	"github.com/qf-studio/pilot/internal/teams"
	"github.com/qf-studio/pilot/internal/webhooks"
)

// Pilot is the main application
type Pilot struct {
	config                 *config.Config
	gateway                *gateway.Server
	orchestrator           *orchestrator.Orchestrator
	linearMultiWH          *linear.MultiWorkspaceHandler // Multi-workspace handler (GH-391)
	linearClient           *linear.Client                // Legacy single-workspace client
	linearWH               *linear.WebhookHandler        // Legacy single-workspace handler
	linearNotify           *linear.Notifier              // Legacy single-workspace notifier
	githubClient           *github.Client
	githubWH               *github.WebhookHandler
	githubNotify           *github.Notifier
	gitlabClient           *gitlab.Client
	gitlabWH               *gitlab.WebhookHandler
	gitlabNotify           *gitlab.Notifier
	jiraClient             *jira.Client
	jiraWH                 *jira.WebhookHandler
	azureDevOpsClient      *azuredevops.Client
	azureDevOpsWH          *azuredevops.WebhookHandler
	asanaClient            *asana.Client
	asanaWH                *asana.WebhookHandler
	planeWH                *plane.WebhookHandler
	slackNotify            *slack.Notifier
	slackClient            *slack.Client
	slackInteractionWH     *slack.InteractionHandler
	slackApprovalHdlr      *approval.SlackHandler
	telegramClient         *telegram.Client
	telegramHandler        *telegram.Handler                // Telegram polling handler (GH-349)
	telegramRunner         *executor.Runner                 // Runner for Telegram tasks (GH-349)
	telegramMemberResolver telegram.MemberResolver          // Team member resolver for Telegram RBAC (GH-634)
	telegramApprovalHdlr   telegram.ApprovalCallbackHandler // Routes approve:/reject: callbacks (GH-2651)
	slackHandler           *slack.Handler                   // Slack Socket Mode handler (GH-652)
	slackRunner            *executor.Runner                 // Runner for Slack tasks (GH-652)
	slackMemberResolver    slack.MemberResolver             // Team member resolver for Slack RBAC (GH-786)
	chatRunner             *executor.Runner                 // Runner for the web chat API's tasks (GH-4835)
	alertEngine            *alerts.Engine
	teamsService           *teams.Service // Teams RBAC service (GH-633)
	store                  *memory.Store
	graph                  *memory.KnowledgeGraph
	webhookManager         *webhooks.Manager
	approvalMgr            *approval.Manager
	dashboardFS            fs.FS // Embedded React frontend (GH-1612)

	// linearTasks maps task IDs to Linear issue IDs for completion callbacks
	linearTasks   map[string]linearTaskInfo
	linearTasksMu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
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

func (a *slackApprovalClientAdapter) PostEphemeral(ctx context.Context, responseURL, text string) error {
	return a.adapter.PostEphemeral(ctx, responseURL, text)
}

func (a *slackApprovalClientAdapter) PostEphemeralToUser(ctx context.Context, channel, user, text string) error {
	return a.adapter.PostEphemeralToUser(ctx, channel, user, text)
}

// linearTaskInfo tracks Linear issue info for completion callbacks (GH-391)
type linearTaskInfo struct {
	IssueID       string
	WorkspaceName string // Empty for legacy single-workspace mode
}

// Option is a functional option for configuring Pilot
type Option func(*Pilot)

// WithTelegramHandler enables Telegram polling in gateway mode (GH-349)
// The runner is required to execute tasks from Telegram messages.
func WithTelegramHandler(runner *executor.Runner, projectPath string) Option {
	return func(p *Pilot) {
		p.telegramRunner = runner
		// Store projectPath in config for handler initialization
		// This is used in the Telegram handler setup
		if projectPath != "" && len(p.config.Projects) > 0 {
			// Use provided projectPath as override
			p.config.Projects[0].Path = projectPath
		}
	}
}

// WithTelegramMemberResolver sets the team member resolver for Telegram RBAC (GH-634).
func WithTelegramMemberResolver(resolver telegram.MemberResolver) Option {
	return func(p *Pilot) {
		p.telegramMemberResolver = resolver
	}
}

// WithTelegramApprovalHandler wires an approval callback handler into the Telegram message handler (GH-2651).
// When set, approve:/reject: button taps are dispatched to this handler instead of being silently dropped.
func WithTelegramApprovalHandler(h telegram.ApprovalCallbackHandler) Option {
	return func(p *Pilot) {
		p.telegramApprovalHdlr = h
	}
}

// WithTeamsService enables team-scoped execution (GH-633)
// When set, Pilot uses team RBAC for permission checks and audit logging.
func WithTeamsService(svc *teams.Service) Option {
	return func(p *Pilot) {
		p.teamsService = svc
	}
}

// WithSlackHandler enables Slack Socket Mode in gateway mode (GH-652)
// The runner is required to execute tasks from Slack messages.
func WithSlackHandler(runner *executor.Runner, projectPath string) Option {
	return func(p *Pilot) {
		p.slackRunner = runner
		// Store projectPath in config for handler initialization
		// This is used in the Slack handler setup
		if projectPath != "" && len(p.config.Projects) > 0 {
			// Use provided projectPath as override
			p.config.Projects[0].Path = projectPath
		}
	}
}

// WithSlackMemberResolver sets the team member resolver for Slack RBAC (GH-786).
func WithSlackMemberResolver(resolver slack.MemberResolver) Option {
	return func(p *Pilot) {
		p.slackMemberResolver = resolver
	}
}

// WithChatHandler enables the web chat API (console Operator chat panel,
// GH-4835) in gateway mode. The runner is required to execute tasks
// dispatched through POST /api/v1/chat/messages; mirrors WithTelegramHandler
// / WithSlackHandler. Only takes effect when cfg.Adapters.Chat.Enabled is
// also true — see the wiring block in New().
func WithChatHandler(runner *executor.Runner, projectPath string) Option {
	return func(p *Pilot) {
		p.chatRunner = runner
		if projectPath != "" && len(p.config.Projects) > 0 {
			p.config.Projects[0].Path = projectPath
		}
	}
}

// WithDashboardFS sets the embedded React frontend filesystem (GH-1612).
// When set, the gateway serves the dashboard at /dashboard/.
func WithDashboardFS(fsys fs.FS) Option {
	return func(p *Pilot) {
		p.dashboardFS = fsys
	}
}

// WithGitHubClient overrides the GitHub client New would otherwise build
// internally from a static cfg.Adapters.GitHub.Token (GH-4755). Webhook-mode
// Pilot held that static client for the daemon's lifetime, bypassing the
// per-request token-resolution chain (GitHub App auth / env / gh-CLI
// fallback, plus 401-triggered re-mint) that cmd/pilot's newGitHubClient
// already gives every polling-mode client (GH-4747/GH-4754). Passing a
// client built the same way in here means webhook mode's githubWH/
// githubNotify resolve tokens identically instead of freezing the boot-time
// value. Must be applied before New's GitHub adapter init block runs.
func WithGitHubClient(client *github.Client) Option {
	return func(p *Pilot) {
		p.githubClient = client
	}
}

// New creates a new Pilot instance
func New(cfg *config.Config, opts ...Option) (*Pilot, error) {
	ctx, cancel := context.WithCancel(context.Background())

	p := &Pilot{
		config:      cfg,
		ctx:         ctx,
		cancel:      cancel,
		linearTasks: make(map[string]linearTaskInfo),
	}

	// GH-4755: apply functional options before any adapter init block runs
	// (not just before return, as previously) so WithGitHubClient can supply
	// a per-request-resolving client before the GitHub adapter block below
	// decides whether it needs to build its own static fallback. The other
	// options (WithDashboardFS, WithTeamsService, WithTelegramHandler, ...)
	// only set fields consumed later in New or by callers after it returns,
	// so applying them this early changes nothing for them.
	for _, opt := range opts {
		opt(p)
	}

	// Initialize memory store
	store, err := memory.NewStore(cfg.Memory.Path)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create memory store: %w", err)
	}
	p.store = store

	// Initialize knowledge graph
	graph, err := memory.NewKnowledgeGraph(cfg.Memory.Path)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create knowledge graph: %w", err)
	}
	p.graph = graph

	// Initialize approval manager
	p.approvalMgr = approval.NewManager(cfg.Approval)

	// Initialize Slack notifier if enabled
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		p.slackNotify = slack.NewNotifier(cfg.Adapters.Slack)

		// Initialize Slack approval handler if enabled
		if cfg.Adapters.Slack.Approval != nil && cfg.Adapters.Slack.Approval.Enabled {
			p.slackClient = slack.NewClient(cfg.Adapters.Slack.BotToken)
			slackAdapter := slack.NewSlackClientAdapter(p.slackClient)
			approvalChannel := cfg.Adapters.Slack.Approval.Channel
			if approvalChannel == "" {
				approvalChannel = cfg.Adapters.Slack.Channel
			}
			p.slackApprovalHdlr = approval.NewSlackHandler(
				&slackApprovalClientAdapter{adapter: slackAdapter},
				approvalChannel,
			)
			// GH-5159: fallback allowlist consulted by isAuthorizedApprover
			// when a request's own Approvers is empty (mirrors the Telegram
			// wiring from cfg.Adapters.Telegram.AllowedIDs, GH-5158).
			p.slackApprovalHdlr.WithAllowedIDs(cfg.Adapters.Slack.AllowedUsers)
			// GH-4411: persist decisions directly to PRState via the manager so a
			// button click on a Rehydrate-restored request isn't lost when no
			// waiter goroutine survived the restart (mirrors GH-3825's Telegram fix).
			p.slackApprovalHdlr.WithDecisionRecorder(p.approvalMgr)
			if p.store != nil {
				p.slackApprovalHdlr.WithStore(p.store)
				if rErr := p.slackApprovalHdlr.Rehydrate(ctx); rErr != nil {
					logging.WithComponent("pilot").Warn("slack approval rehydrate failed", slog.Any("error", rErr))
				}
			}
			p.approvalMgr.RegisterHandler(p.slackApprovalHdlr)
			logging.WithComponent("pilot").Info("registered Slack approval handler",
				slog.String("channel", approvalChannel))

			// GH-4411: prune requests that expired while the daemon was down (or
			// with no in-process waiter) instead of leaving them pending forever.
			slackApprovalHdlr := p.slackApprovalHdlr
			logging.SafeGo("approval-expiry-sweep-slack", func() {
				ticker := time.NewTicker(1 * time.Minute)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if _, err := slackApprovalHdlr.PruneExpired(ctx); err != nil {
							logging.WithComponent("approval").Warn("expired Slack approval sweep failed", slog.Any("error", err))
						}
					}
				}
			})

			// Set up Slack interaction webhook handler
			signingSecret := cfg.Adapters.Slack.Approval.SigningSecret
			if signingSecret == "" {
				signingSecret = cfg.Adapters.Slack.SigningSecret
			}
			p.slackInteractionWH = slack.NewInteractionHandler(signingSecret)
			p.slackInteractionWH.OnAction(func(action *slack.InteractionAction) bool {
				return p.slackApprovalHdlr.HandleInteraction(
					context.Background(),
					action.ActionID,
					action.Value,
					action.UserID,
					action.Username,
					action.ResponseURL,
				)
			})
		}
	}

	// Initialize webhook manager
	p.webhookManager = webhooks.NewManager(cfg.Webhooks, logging.WithComponent("webhooks"))

	// Initialize orchestrator
	orchConfig := &orchestrator.Config{
		Model:         cfg.Orchestrator.Model,
		MaxConcurrent: cfg.Orchestrator.MaxConcurrent,
		BackendConfig: cfg.Executor, // GH-2286: pass executor config to runner
	}
	orch, err := orchestrator.NewOrchestrator(orchConfig, p.slackNotify)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create orchestrator: %w", err)
	}
	p.orchestrator = orch

	// Register completion callback for platform notifications + outbound webhooks
	p.orchestrator.OnCompletion(p.handleTaskCompletion)

	// Register progress callback for outbound webhooks
	p.orchestrator.OnProgress(func(taskID, phase string, progress int, message string) {
		if p.webhookManager.IsEnabled() {
			p.webhookManager.Dispatch(ctx, webhooks.NewEvent(webhooks.EventTaskProgress, &webhooks.TaskProgressData{
				TaskID:   taskID,
				Phase:    phase,
				Progress: float64(progress),
				Message:  message,
			}))
		}
	})

	// Initialize Linear adapter if enabled (GH-391: multi-workspace support)
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		workspaces := cfg.Adapters.Linear.GetWorkspaces()
		if len(workspaces) > 1 || (len(workspaces) == 1 && len(cfg.Adapters.Linear.Workspaces) > 0) {
			// Multi-workspace mode
			multiWH, err := linear.NewMultiWorkspaceHandler(cfg.Adapters.Linear)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("failed to create Linear multi-workspace handler: %w", err)
			}
			p.linearMultiWH = multiWH
			p.linearMultiWH.OnIssue(p.handleLinearIssueMultiWorkspace)
			logging.WithComponent("pilot").Info("Linear multi-workspace mode enabled",
				slog.Int("workspaces", p.linearMultiWH.WorkspaceCount()))
		} else {
			// Legacy single-workspace mode
			p.linearClient = linear.NewClient(cfg.Adapters.Linear.APIKey)
			pilotLabel := cfg.Adapters.Linear.PilotLabel
			if pilotLabel == "" {
				pilotLabel = "pilot"
			}
			p.linearWH = linear.NewWebhookHandler(p.linearClient, pilotLabel, cfg.Adapters.Linear.ProjectIDs)
			p.linearWH.OnIssue(p.handleLinearIssue)
			p.linearNotify = linear.NewNotifier(p.linearClient)
		}
	}

	// Initialize GitHub adapter if enabled
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled {
		// GH-4755: WithGitHubClient (applied above, before this block) already
		// supplies a client whose token resolves per-request through the same
		// chain as polling mode. Only fall back to a static
		// github.NewClient(token) — frozen at construction time — when no
		// caller passed one in (e.g. direct pilot.New callers in tests).
		if p.githubClient == nil {
			p.githubClient = github.NewClient(cfg.Adapters.GitHub.Token)
		}
		p.githubWH = github.NewWebhookHandler(
			p.githubClient,
			cfg.Adapters.GitHub.WebhookSecret,
			cfg.Adapters.GitHub.PilotLabel,
		)
		p.githubWH.OnIssue(p.handleGithubIssue)
		p.githubNotify = github.NewNotifier(p.githubClient, cfg.Adapters.GitHub.PilotLabel)
	}

	// Initialize GitLab adapter if enabled
	if cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled {
		p.gitlabClient = gitlab.NewClient(cfg.Adapters.GitLab.Token, cfg.Adapters.GitLab.Project)
		p.gitlabWH = gitlab.NewWebhookHandler(
			p.gitlabClient,
			cfg.Adapters.GitLab.WebhookSecret,
			cfg.Adapters.GitLab.PilotLabel,
		)
		p.gitlabWH.OnIssue(p.handleGitlabIssue)
		p.gitlabNotify = gitlab.NewNotifier(p.gitlabClient, cfg.Adapters.GitLab.PilotLabel)
	}

	// Initialize Jira adapter if enabled
	if cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled {
		p.jiraClient = jira.NewClient(
			cfg.Adapters.Jira.BaseURL,
			cfg.Adapters.Jira.Username,
			cfg.Adapters.Jira.APIToken,
			cfg.Adapters.Jira.Platform,
		)
		pilotLabel := cfg.Adapters.Jira.PilotLabel
		if pilotLabel == "" {
			pilotLabel = "pilot"
		}
		p.jiraWH = jira.NewWebhookHandler(p.jiraClient, cfg.Adapters.Jira.WebhookSecret, pilotLabel)
		p.jiraWH.OnIssue(p.handleJiraIssue)
	}

	// GH-1699: Initialize Azure DevOps webhook handler if configured
	if cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled {
		p.azureDevOpsClient = azuredevops.NewClientWithConfig(cfg.Adapters.AzureDevOps)
		pilotTag := cfg.Adapters.AzureDevOps.PilotTag
		if pilotTag == "" {
			pilotTag = "pilot"
		}
		p.azureDevOpsWH = azuredevops.NewWebhookHandler(p.azureDevOpsClient, cfg.Adapters.AzureDevOps.WebhookSecret, pilotTag)
		if len(cfg.Adapters.AzureDevOps.WorkItemTypes) > 0 {
			p.azureDevOpsWH.SetWorkItemTypes(cfg.Adapters.AzureDevOps.WorkItemTypes)
		}
	}

	// GH-2044: Initialize Asana adapter if enabled
	if cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled {
		p.asanaClient = asana.NewClient(cfg.Adapters.Asana.AccessToken, cfg.Adapters.Asana.WorkspaceID)
		pilotTag := cfg.Adapters.Asana.PilotTag
		if pilotTag == "" {
			pilotTag = "pilot"
		}
		p.asanaWH = asana.NewWebhookHandler(p.asanaClient, cfg.Adapters.Asana.WebhookSecret, pilotTag)
		p.asanaWH.OnTask(p.handleAsanaTask)
	}

	// GH-2044: Initialize Plane adapter if enabled
	if cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled {
		pilotLabel := cfg.Adapters.Plane.PilotLabel
		if pilotLabel == "" {
			pilotLabel = "pilot"
		}
		p.planeWH = plane.NewWebhookHandler(cfg.Adapters.Plane.WebhookSecret, pilotLabel, cfg.Adapters.Plane.ProjectIDs)
		p.planeWH.OnWorkItem(p.handlePlaneWorkItem)
	}

	// Initialize alerts engine if enabled
	if cfg.Alerts != nil && cfg.Alerts.Enabled {
		p.initAlerts(cfg)
	}

	// Initialize gateway with webhook secrets
	gatewayCfg := cfg.Gateway
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.WebhookSecret != "" {
		gatewayCfg.GithubWebhookSecret = cfg.Adapters.GitHub.WebhookSecret
	}
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.WebhookPublicKey != "" {
		key, err := parseLinearWebhookKey(cfg.Adapters.Linear.WebhookPublicKey)
		if err != nil {
			logging.WithComponent("pilot").Error("Linear webhook public key invalid — signature verification disabled", slog.Any("error", err))
		} else {
			gatewayCfg.LinearWebhookPublicKey = key
		}
	} else {
		logging.WithComponent("pilot").Info("Linear webhook signature verification disabled — set adapters.linear.webhook_public_key to enable")
	}
	// Loud startup warning when the fail-closed default is bypassed.
	// PILOT_ALLOW_UNSIGNED_WEBHOOKS=1 disables signature verification on every adapter.
	if os.Getenv("PILOT_ALLOW_UNSIGNED_WEBHOOKS") == "1" {
		logging.WithComponent("pilot").Warn("PILOT_ALLOW_UNSIGNED_WEBHOOKS=1 — webhook signature verification DISABLED across all adapters; never use this in production")
	}
	// GH-4784: enforce bearer auth on /api/v1 when an api-token is
	// configured (cfg.Auth); empty/default token keeps today's open
	// behavior (loopback bind is the mitigant) via a nil authConfig.
	p.gateway = gateway.NewServerWithAuth(gatewayCfg, cfg.GatewayAuthConfig())

	// Register webhook handlers
	if p.linearMultiWH != nil {
		// Multi-workspace mode (GH-391)
		p.gateway.Router().RegisterWebhookHandler("linear", func(payload map[string]interface{}) {
			if err := p.linearMultiWH.Handle(ctx, payload); err != nil {
				logging.WithComponent("pilot").Error("Linear webhook error", slog.Any("error", err))
			}
		})
	} else if p.linearWH != nil {
		// Legacy single-workspace mode
		p.gateway.Router().RegisterWebhookHandler("linear", func(payload map[string]interface{}) {
			if err := p.linearWH.Handle(ctx, payload); err != nil {
				logging.WithComponent("pilot").Error("Linear webhook error", slog.Any("error", err))
			}
		})
	}

	if p.githubWH != nil {
		p.gateway.Router().RegisterWebhookHandler("github", func(payload map[string]interface{}) {
			eventType, _ := payload["_event_type"].(string)
			if err := p.githubWH.Handle(ctx, eventType, payload); err != nil {
				logging.WithComponent("pilot").Error("GitHub webhook error", slog.Any("error", err))
			}
		})
	}

	if p.gitlabWH != nil {
		p.gateway.Router().RegisterWebhookHandler("gitlab", func(payload map[string]interface{}) {
			eventType, _ := payload["_event_type"].(string)
			token, _ := payload["_token"].(string)

			// Verify webhook token
			if !p.gitlabWH.VerifyToken(token) {
				logging.WithComponent("pilot").Warn("GitLab webhook token verification failed")
				return
			}

			// Parse the webhook payload
			webhookPayload := p.parseGitlabWebhookPayload(payload)
			if webhookPayload == nil {
				logging.WithComponent("pilot").Warn("Failed to parse GitLab webhook payload")
				return
			}

			if err := p.gitlabWH.Handle(ctx, eventType, webhookPayload); err != nil {
				logging.WithComponent("pilot").Error("GitLab webhook error", slog.Any("error", err))
			}
		})
	}

	if p.jiraWH != nil {
		p.gateway.Router().RegisterWebhookHandler("jira", func(payload map[string]interface{}) {
			signature, _ := payload["_signature"].(string)
			// TASK-333: verify the HMAC over the exact raw request body the gateway
			// buffered, not a re-marshaled map (which would not reproduce the bytes
			// Jira signed).
			rawBody, _ := payload["_raw_body"].(string)
			if !p.jiraWH.VerifySignature([]byte(rawBody), signature) {
				logging.WithComponent("pilot").Warn("Jira webhook signature verification failed")
				return
			}
			if err := p.jiraWH.Handle(ctx, payload); err != nil {
				logging.WithComponent("pilot").Error("Jira webhook error", slog.Any("error", err))
			}
		})
	}

	// GH-1699: Register Azure DevOps webhook handler
	if p.azureDevOpsWH != nil {
		p.gateway.Router().RegisterWebhookHandler("azuredevops", func(payload map[string]interface{}) {
			// Verify webhook secret
			secret, _ := payload["_secret"].(string)
			if !p.azureDevOpsWH.VerifySecret(secret) {
				logging.WithComponent("pilot").Warn("Azure DevOps webhook secret verification failed")
				return
			}

			// Parse the raw payload into WebhookPayload
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				logging.WithComponent("pilot").Error("Failed to marshal Azure DevOps payload", slog.Any("error", err))
				return
			}

			var webhookPayload azuredevops.WebhookPayload
			if err := json.Unmarshal(payloadBytes, &webhookPayload); err != nil {
				logging.WithComponent("pilot").Error("Failed to parse Azure DevOps webhook payload", slog.Any("error", err))
				return
			}

			if err := p.azureDevOpsWH.Handle(ctx, &webhookPayload); err != nil {
				logging.WithComponent("pilot").Error("Azure DevOps webhook error", slog.Any("error", err))
			}
		})
	}

	// GH-2044: Register Asana webhook handler
	if p.asanaWH != nil {
		p.gateway.Router().RegisterWebhookHandler("asana", func(payload map[string]interface{}) {
			signature, _ := payload["_signature"].(string)
			// TASK-333: verify the HMAC over the exact raw request body the gateway
			// buffered, not a re-marshaled map (which would not reproduce the bytes
			// Asana signed).
			rawBody, _ := payload["_raw_body"].(string)
			if !p.asanaWH.VerifySignature([]byte(rawBody), signature) {
				logging.WithComponent("pilot").Warn("Asana webhook signature verification failed")
				return
			}

			// Parse the map payload into WebhookPayload
			payloadBytes, err := json.Marshal(payload)
			if err != nil {
				logging.WithComponent("pilot").Error("Failed to marshal Asana payload", slog.Any("error", err))
				return
			}

			var webhookPayload asana.WebhookPayload
			if err := json.Unmarshal(payloadBytes, &webhookPayload); err != nil {
				logging.WithComponent("pilot").Error("Failed to parse Asana webhook payload", slog.Any("error", err))
				return
			}

			if err := p.asanaWH.Handle(ctx, &webhookPayload); err != nil {
				logging.WithComponent("pilot").Error("Asana webhook error", slog.Any("error", err))
			}
		})
	}

	// GH-2044: Register Plane webhook handler
	if p.planeWH != nil {
		p.gateway.Router().RegisterWebhookHandler("plane", func(payload map[string]interface{}) {
			// Plane handler needs raw bytes + signature for HMAC verification
			rawBody, _ := payload["_raw_body"].(string)
			signature, _ := payload["_signature"].(string)

			if err := p.planeWH.Handle(ctx, []byte(rawBody), signature); err != nil {
				logging.WithComponent("pilot").Error("Plane webhook error", slog.Any("error", err))
			}
		})
	}

	// Register Slack interaction webhook handler for approval buttons
	if p.slackInteractionWH != nil {
		p.gateway.RegisterHandler("/webhooks/slack/interactions", p.slackInteractionWH)
		logging.WithComponent("pilot").Info("registered Slack interaction webhook handler",
			slog.String("path", "/webhooks/slack/interactions"))
	}

	// Functional options were already applied at the top of New (GH-4755),
	// before the adapter init blocks above ran.

	// Set embedded dashboard frontend on gateway if available (GH-1612)
	if p.dashboardFS != nil {
		p.gateway.SetDashboardFS(p.dashboardFS)
	}

	// Initialize Telegram handler if runner was provided via options (GH-349)
	// This enables Telegram polling in gateway mode alongside Linear/Jira webhooks
	if p.telegramRunner != nil && cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.Polling {
		var allowedIDs []int64
		allowedIDs = append(allowedIDs, cfg.Adapters.Telegram.AllowedIDs...)
		if cfg.Adapters.Telegram.ChatID != "" {
			if id, err := parseInt64(cfg.Adapters.Telegram.ChatID); err == nil {
				allowedIDs = append(allowedIDs, id)
			}
		}

		// Get project path - use first project if available
		projectPath := ""
		if len(cfg.Projects) > 0 {
			projectPath = cfg.Projects[0].Path
		}

		tgClient := telegram.NewClient(cfg.Adapters.Telegram.BotToken)
		tgMessenger := telegram.NewMessenger(tgClient, true) // Default to plain text mode

		// Build comms.MemberResolver wrapper (GH-634)
		var tgMemberResolver comms.MemberResolver
		if p.telegramMemberResolver != nil {
			tgMemberResolver = &telegram.MemberResolverAdapter{Inner: p.telegramMemberResolver}
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
			}
		}

		tgCommsHandler := comms.BuildHandler(comms.HandlerDeps{
			Messenger:       tgMessenger,
			Runner:          p.telegramRunner,
			Projects:        config.NewProjectSource(cfg),
			ProjectPath:     projectPath,
			RateLimit:       cfg.Adapters.Telegram.RateLimit,
			Classifier:      tgClassifierCfg,
			Bot:             tgBotCfg,
			MemberResolver:  tgMemberResolver,
			Store:           p.store,
			TaskIDPrefix:    "TG",
			ExecutorBackend: cfg.Executor,
		})

		p.telegramHandler = telegram.NewHandler(&telegram.HandlerConfig{
			Client:          tgClient,
			CommsHandler:    tgCommsHandler,
			ProjectPath:     projectPath,
			Projects:        config.NewProjectSource(cfg),
			AllowedIDs:      allowedIDs,
			ChatID:          cfg.Adapters.Telegram.ChatID,
			Transcription:   cfg.Adapters.Telegram.Transcription,
			Store:           p.store,
			ApprovalHandler: p.telegramApprovalHdlr,
		}, p.telegramRunner)

		if len(allowedIDs) == 0 {
			logging.WithComponent("pilot").Warn("SECURITY: telegram allowed_ids is empty - ALL users can interact with the bot!")
		}

		logging.WithComponent("pilot").Info("Telegram handler initialized for gateway mode")
	}

	// Initialize Slack handler if runner was provided via options (GH-652)
	// This enables Slack Socket Mode in gateway mode alongside other adapters
	if p.slackRunner != nil && cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.SocketMode {
		// Get project path - use first project if available
		projectPath := ""
		if len(cfg.Projects) > 0 {
			projectPath = cfg.Projects[0].Path
		}

		slackClient := slack.NewClient(cfg.Adapters.Slack.BotToken)
		slackMessenger := slack.NewMessenger(slackClient)

		var slackMemberResolver comms.MemberResolver
		if p.slackMemberResolver != nil {
			slackMemberResolver = &slack.MemberResolverAdapter{Inner: p.slackMemberResolver}
		}

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
			}
		}

		slackCommsHandler := comms.BuildHandler(comms.HandlerDeps{
			Messenger:       slackMessenger,
			Runner:          p.slackRunner,
			Projects:        config.NewSlackProjectSource(cfg),
			ProjectPath:     projectPath,
			Classifier:      slackClassifierCfg,
			Bot:             slackBotCfg,
			MemberResolver:  slackMemberResolver,
			Store:           p.store,
			TaskIDPrefix:    "SLACK",
			ExecutorBackend: cfg.Executor,
		})

		// GH-4431: route Socket Mode approve/reject clicks to the same
		// SlackHandler that the (HTTP-only) interaction webhook uses, so
		// approval buttons are also routable on socket-mode deployments that
		// have no public Interactivity Request URL. Guard against wrapping a
		// nil *approval.SlackHandler in a non-nil interface (p.slackApprovalHdlr
		// is nil when Slack approval isn't enabled).
		var slackApprovalHandler slack.ApprovalCallbackHandler
		if p.slackApprovalHdlr != nil {
			slackApprovalHandler = p.slackApprovalHdlr
		}

		p.slackHandler = slack.NewHandler(&slack.HandlerConfig{
			AppToken:        cfg.Adapters.Slack.AppToken,
			Client:          slackClient,
			CommsHandler:    slackCommsHandler,
			ApprovalHandler: slackApprovalHandler,
			AllowedChannels: cfg.Adapters.Slack.AllowedChannels,
			AllowedUsers:    cfg.Adapters.Slack.AllowedUsers,
		})

		if len(cfg.Adapters.Slack.AllowedChannels) == 0 && len(cfg.Adapters.Slack.AllowedUsers) == 0 {
			logging.WithComponent("pilot").Warn("SECURITY: slack allowed_channels and allowed_users are empty - ALL users can interact with the bot!")
		}

		logging.WithComponent("pilot").Info("Slack handler initialized for gateway mode")
	}

	// Initialize the web chat API if a runner was provided via options
	// (GH-4835). Backs the console Operator chat panel: POST
	// /api/v1/chat/messages and GET
	// /api/v1/chat/conversations/{id}/events. Must be wired here (gateway
	// mode) AND in cmd/pilot/main.go (polling mode) — see SetChatAPI's doc
	// comment in internal/gateway/chat.go; missing either means the routes
	// silently don't exist in that mode.
	if p.chatRunner != nil && cfg.Adapters.Chat != nil && cfg.Adapters.Chat.Enabled {
		projectPath := ""
		if len(cfg.Projects) > 0 {
			projectPath = cfg.Projects[0].Path
		}

		webMessenger := web.NewMessenger()
		webCommsHandler := comms.BuildHandler(comms.HandlerDeps{
			Messenger:       webMessenger,
			Runner:          p.chatRunner,
			Projects:        config.NewProjectSource(cfg),
			ProjectPath:     projectPath,
			RateLimit:       cfg.Adapters.Chat.RateLimit,
			Store:           p.store,
			TaskIDPrefix:    "WEB",
			ExecutorBackend: cfg.Executor,
		})

		p.gateway.SetChatAPI(web.NewAPI(webCommsHandler, webMessenger))

		logging.WithComponent("pilot").Info("Chat API initialized for gateway mode")
	}

	return p, nil
}

// parseInt64 parses a string to int64
func parseInt64(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	return id, err
}

// Start starts Pilot
func (p *Pilot) Start() error {
	logging.WithComponent("pilot").Info("Starting Pilot")

	// Start alerts engine if initialized
	if p.alertEngine != nil {
		if err := p.alertEngine.Start(p.ctx); err != nil {
			logging.WithComponent("pilot").Warn("Failed to start alerts engine", slog.Any("error", err))
		}
	}

	// Start orchestrator
	p.orchestrator.Start()

	// Start gateway
	p.wg.Add(1)
	logging.SafeGo("pilot-core", func() {
		defer p.wg.Done()
		if err := p.gateway.Start(p.ctx); err != nil {
			logging.WithComponent("pilot").Error("Gateway error", slog.Any("error", err))
		}
	})

	// Start Telegram polling if handler is initialized (GH-349)
	if p.telegramHandler != nil {
		p.telegramHandler.StartPolling(p.ctx)
		logging.WithComponent("pilot").Info("Telegram polling started in gateway mode")
	}

	// Start Slack Socket Mode if handler is initialized (GH-652)
	if p.slackHandler != nil {
		p.wg.Add(1)
		logging.SafeGo("pilot-core", func() {
			defer p.wg.Done()
			if err := p.slackHandler.StartListening(p.ctx); err != nil {
				logging.WithComponent("pilot").Error("Slack Socket Mode error", slog.Any("error", err))
			}
		})
		logging.WithComponent("pilot").Info("Slack Socket Mode started in gateway mode")
	}

	logging.WithComponent("pilot").Info("Pilot started",
		slog.String("host", p.config.Gateway.Host),
		slog.Int("port", p.config.Gateway.Port))
	return nil
}

// Stop stops Pilot
func (p *Pilot) Stop() error {
	logging.WithComponent("pilot").Info("Stopping Pilot")

	p.cancel()

	// Stop Telegram polling if enabled (GH-349)
	if p.telegramHandler != nil {
		p.telegramHandler.Stop()
		logging.WithComponent("pilot").Info("Telegram polling stopped")
	}

	// Stop Slack Socket Mode if enabled (GH-652)
	if p.slackHandler != nil {
		p.slackHandler.Stop()
		logging.WithComponent("pilot").Info("Slack Socket Mode stopped")
	}

	// Stop alerts engine
	if p.alertEngine != nil {
		p.alertEngine.Stop()
	}

	p.orchestrator.Stop()
	_ = p.gateway.Shutdown()
	_ = p.store.Close()
	p.wg.Wait()

	logging.WithComponent("pilot").Info("Pilot stopped")
	return nil
}

// Wait waits for Pilot to stop
func (p *Pilot) Wait() {
	p.wg.Wait()
}

// handleLinearIssue handles a new Linear issue (legacy single-workspace mode)
func (p *Pilot) handleLinearIssue(ctx context.Context, issue *linear.Issue) error {
	logging.WithComponent("pilot").Info("Received Linear issue",
		slog.String("identifier", issue.Identifier),
		slog.String("title", issue.Title))

	// Find project for this issue
	projectPath := p.findProjectForIssue(issue)
	if projectPath == "" {
		return fmt.Errorf("no project configured for issue %s", issue.Identifier)
	}

	// Track task ID -> Linear issue ID mapping for completion callback
	// Task ID format matches bridge.go: "TASK-{identifier}"
	taskID := fmt.Sprintf("TASK-%s", issue.Identifier)
	p.linearTasksMu.Lock()
	p.linearTasks[taskID] = linearTaskInfo{IssueID: issue.ID, WorkspaceName: ""}
	p.linearTasksMu.Unlock()

	// Dispatch outbound webhook
	if p.webhookManager.IsEnabled() {
		p.webhookManager.Dispatch(ctx, webhooks.NewEvent(webhooks.EventTaskStarted, &webhooks.TaskStartedData{
			TaskID: taskID, Title: issue.Title, Project: projectPath, Source: "linear", SourceID: issue.Identifier,
		}))
	}

	// Notify that task has started
	if p.linearNotify != nil {
		if err := p.linearNotify.NotifyTaskStarted(ctx, issue.ID, taskID); err != nil {
			logging.WithComponent("pilot").Warn("Failed to notify task started", slog.Any("error", err))
		}
	}

	// Process ticket through orchestrator
	err := p.orchestrator.ProcessTicket(ctx, issue, projectPath)

	// Immediate errors are handled here; async completion is handled by handleTaskCompletion
	if err != nil && p.linearNotify != nil {
		if notifyErr := p.linearNotify.NotifyTaskFailed(ctx, issue.ID, err.Error()); notifyErr != nil {
			logging.WithComponent("pilot").Warn("Failed to notify task failed", slog.Any("error", notifyErr))
		}
		// Clean up tracking on immediate error
		p.linearTasksMu.Lock()
		delete(p.linearTasks, taskID)
		p.linearTasksMu.Unlock()
	}

	return err
}

// handleLinearIssueMultiWorkspace handles a new Linear issue in multi-workspace mode (GH-391)
func (p *Pilot) handleLinearIssueMultiWorkspace(ctx context.Context, issue *linear.Issue, workspaceName string) error {
	logging.WithComponent("pilot").Info("Received Linear issue",
		slog.String("identifier", issue.Identifier),
		slog.String("title", issue.Title),
		slog.String("workspace", workspaceName))

	// Get workspace handler for project resolution and notifications
	ws := p.linearMultiWH.GetWorkspace(workspaceName)
	if ws == nil {
		return fmt.Errorf("workspace %s not found", workspaceName)
	}

	// Find project for this issue
	var projectPath string

	// GH-1684: Check project-level linear.project_id mapping first
	if issue.Project != nil {
		if proj := p.config.GetProjectByLinearID(issue.Project.ID); proj != nil {
			projectPath = proj.Path
		}
	}

	// Fall back to workspace-specific mapping
	if projectPath == "" {
		pilotProject := ws.ResolvePilotProject(issue)
		if pilotProject != "" {
			if proj := p.config.GetProjectByName(pilotProject); proj != nil {
				projectPath = proj.Path
			}
		}
	}

	// Fall back to generic matching
	if projectPath == "" {
		projectPath = p.findProjectForIssue(issue)
	}

	if projectPath == "" {
		return fmt.Errorf("no project configured for issue %s in workspace %s", issue.Identifier, workspaceName)
	}

	// Track task ID -> Linear issue ID + workspace mapping for completion callback
	taskID := fmt.Sprintf("TASK-%s", issue.Identifier)
	p.linearTasksMu.Lock()
	p.linearTasks[taskID] = linearTaskInfo{IssueID: issue.ID, WorkspaceName: workspaceName}
	p.linearTasksMu.Unlock()

	// Notify that task has started
	notifier := ws.Notifier()
	if notifier != nil {
		if err := notifier.NotifyTaskStarted(ctx, issue.ID, taskID); err != nil {
			logging.WithComponent("pilot").Warn("Failed to notify task started", slog.Any("error", err))
		}
	}

	// Process ticket through orchestrator
	err := p.orchestrator.ProcessTicket(ctx, issue, projectPath)

	// Immediate errors are handled here; async completion is handled by handleTaskCompletion
	if err != nil && notifier != nil {
		if notifyErr := notifier.NotifyTaskFailed(ctx, issue.ID, err.Error()); notifyErr != nil {
			logging.WithComponent("pilot").Warn("Failed to notify task failed", slog.Any("error", notifyErr))
		}
		// Clean up tracking on immediate error
		p.linearTasksMu.Lock()
		delete(p.linearTasks, taskID)
		p.linearTasksMu.Unlock()
	}

	return err
}

// handleTaskCompletion handles task completion events from the orchestrator
func (p *Pilot) handleTaskCompletion(taskID, prURL string, success bool, errMsg string) {
	// Dispatch outbound webhook
	if p.webhookManager.IsEnabled() {
		ctx := context.Background()
		if success {
			p.webhookManager.Dispatch(ctx, webhooks.NewEvent(webhooks.EventTaskCompleted, &webhooks.TaskCompletedData{
				TaskID:    taskID,
				PRCreated: prURL != "",
				PRURL:     prURL,
			}))
		} else {
			p.webhookManager.Dispatch(ctx, webhooks.NewEvent(webhooks.EventTaskFailed, &webhooks.TaskFailedData{
				TaskID: taskID,
				Error:  errMsg,
			}))
		}
	}

	// Check if this is a Linear task
	p.linearTasksMu.Lock()
	taskInfo, isLinear := p.linearTasks[taskID]
	if isLinear {
		delete(p.linearTasks, taskID)
	}
	p.linearTasksMu.Unlock()

	if !isLinear {
		return
	}

	ctx := context.Background()

	// Get the appropriate notifier (GH-391: multi-workspace support)
	var notifier *linear.Notifier
	if taskInfo.WorkspaceName != "" && p.linearMultiWH != nil {
		notifier = p.linearMultiWH.GetNotifier(taskInfo.WorkspaceName)
	} else if p.linearNotify != nil {
		notifier = p.linearNotify
	}

	if notifier == nil {
		logging.WithComponent("pilot").Warn("No notifier found for Linear task",
			slog.String("task_id", taskID),
			slog.String("workspace", taskInfo.WorkspaceName))
		return
	}

	if success {
		if err := notifier.NotifyTaskCompleted(ctx, taskInfo.IssueID, prURL, ""); err != nil {
			logging.WithComponent("pilot").Warn("Failed to notify Linear task completed",
				slog.String("task_id", taskID),
				slog.Any("error", err))
		}
	} else {
		if err := notifier.NotifyTaskFailed(ctx, taskInfo.IssueID, errMsg); err != nil {
			logging.WithComponent("pilot").Warn("Failed to notify Linear task failed",
				slog.String("task_id", taskID),
				slog.Any("error", err))
		}
	}
}

// findProjectForIssue finds the project path for an issue
func (p *Pilot) findProjectForIssue(issue *linear.Issue) string {
	// Try to match by project name or team
	for _, proj := range p.config.Projects {
		// Match by name
		if issue.Project != nil && issue.Project.Name == proj.Name {
			return proj.Path
		}
		// Match by team key
		if issue.Team.Key != "" && proj.Name == issue.Team.Key {
			return proj.Path
		}
	}

	// Return first project as fallback
	if len(p.config.Projects) > 0 {
		return p.config.Projects[0].Path
	}

	return ""
}

// GetStatus returns current Pilot status
func (p *Pilot) GetStatus() map[string]interface{} {
	webhookDeliveries, webhookFailures, webhookRetries, lastDelivery := p.webhookManager.Stats()

	// Build Linear status (GH-391: multi-workspace support)
	linearStatus := p.config.Adapters.Linear != nil && p.config.Adapters.Linear.Enabled
	var linearWorkspaces []string
	if p.linearMultiWH != nil {
		linearWorkspaces = p.linearMultiWH.ListWorkspaces()
	}

	return map[string]interface{}{
		"running": true,
		"tasks":   p.orchestrator.GetTaskStates(),
		"config": map[string]interface{}{
			"gateway":           fmt.Sprintf("%s:%d", p.config.Gateway.Host, p.config.Gateway.Port),
			"linear":            linearStatus,
			"linear_workspaces": linearWorkspaces,
			"github":            p.config.Adapters.GitHub != nil && p.config.Adapters.GitHub.Enabled,
			"gitlab":            p.config.Adapters.GitLab != nil && p.config.Adapters.GitLab.Enabled,
			"slack":             p.config.Adapters.Slack != nil && p.config.Adapters.Slack.Enabled,
			"webhooks":          p.webhookManager.IsEnabled(),
		},
		"webhooks": map[string]interface{}{
			"enabled":       p.webhookManager.IsEnabled(),
			"endpoints":     len(p.webhookManager.ListEndpoints()),
			"deliveries":    webhookDeliveries,
			"failures":      webhookFailures,
			"retries":       webhookRetries,
			"last_delivery": lastDelivery,
		},
	}
}

// WebhookManager returns the webhook manager for external access
func (p *Pilot) WebhookManager() *webhooks.Manager {
	return p.webhookManager
}

// DispatchWebhookEvent dispatches an event to all subscribed webhook endpoints
func (p *Pilot) DispatchWebhookEvent(ctx context.Context, event *webhooks.Event) []webhooks.DeliveryResult {
	return p.webhookManager.Dispatch(ctx, event)
}

// Router returns the gateway router for registering handlers
func (p *Pilot) Router() *gateway.Router {
	return p.gateway.Router()
}

// Gateway returns the gateway server for registering HTTP handlers
func (p *Pilot) Gateway() *gateway.Server {
	return p.gateway
}

// TeamsService returns the teams service for RBAC (GH-633)
// Returns nil if --team was not provided.
func (p *Pilot) TeamsService() *teams.Service {
	return p.teamsService
}

// OnProgress registers a callback for task progress updates
func (p *Pilot) OnProgress(callback func(taskID, phase string, progress int, message string)) {
	p.orchestrator.OnProgress(callback)
}

// OnToken registers a callback for token usage updates
func (p *Pilot) OnToken(name string, callback func(taskID string, inputTokens, outputTokens int64, modelName string)) {
	p.orchestrator.OnToken(name, callback)
}

// GetTaskStates returns current task states from the orchestrator
func (p *Pilot) GetTaskStates() []*executor.TaskState {
	return p.orchestrator.GetTaskStates()
}

// Store returns the shared memory store, or nil if persistence is disabled.
func (p *Pilot) Store() *memory.Store {
	return p.store
}

// ReconcileTaskStatesWithStore corrects orchestrator-tracked task states
// against the executions table (source of truth) — a card whose
// Complete/Fail callback never fired (no-commit failure, externally closed
// PR) would otherwise be stuck displaying "running" forever. See
// executor.Monitor.ReconcileWithStore (GH-4490).
func (p *Pilot) ReconcileTaskStatesWithStore() error {
	if p.store == nil {
		return nil
	}
	return p.orchestrator.ReconcileWithStore(p.store)
}

// SuppressProgressLogs disables slog output for progress updates.
// Use this when a visual progress display is active to prevent log spam.
func (p *Pilot) SuppressProgressLogs(suppress bool) {
	p.orchestrator.SuppressProgressLogs(suppress)
}

// SetQualityCheckerFactory sets the factory for creating quality checkers.
// Quality gates run after task execution to validate code quality before PR creation.
// This allows main.go to wire the factory without creating import cycles.
func (p *Pilot) SetQualityCheckerFactory(factory executor.QualityCheckerFactory) {
	p.orchestrator.SetQualityCheckerFactory(factory)
}

// SetContractDependencyLookup sets the lookup used by the executor's
// Contract Evidence gate (GH-5009/GH-5013) to resolve a task's project's
// configured contract dependencies. This allows main.go to wire the lookup
// without creating import cycles.
func (p *Pilot) SetContractDependencyLookup(lookup executor.ContractDependencyLookup) {
	p.orchestrator.SetContractDependencyLookup(lookup)
}

// SetContractContentFetcher sets the fetcher used by the executor's
// Contract Evidence gate (GH-5009/GH-5011/GH-5022) to independently verify
// a citation's producer source. This allows main.go to wire the fetcher
// without creating import cycles.
func (p *Pilot) SetContractContentFetcher(fetcher executor.ContractContentFetcher) {
	p.orchestrator.SetContractContentFetcher(fetcher)
}

// SetOnPRReview wires a PR review callback on the GitHub webhook handler.
// This allows cmd/pilot/main.go to route review events to the autopilot controller
// without creating import cycles.
func (p *Pilot) SetOnPRReview(callback github.PRReviewCallback) {
	if p.githubWH != nil {
		p.githubWH.OnPRReview(callback)
	}
}

// handleGithubIssue handles a new GitHub issue
func (p *Pilot) handleGithubIssue(ctx context.Context, issue *github.Issue, repo *github.Repository) error {
	logging.WithComponent("pilot").Info("Received GitHub issue",
		slog.String("repo", repo.FullName),
		slog.Int("number", issue.Number),
		slog.String("title", issue.Title))

	// Convert to task
	task := github.ConvertIssueToTask(issue, repo)

	// Find project for this repo
	projectPath := p.findProjectForGithubRepo(repo)
	if projectPath == "" {
		return fmt.Errorf("no project configured for repo %s", repo.FullName)
	}

	// Notify that task has started
	if p.githubNotify != nil {
		if err := p.githubNotify.NotifyTaskStarted(ctx, repo.Owner.Login, repo.Name, issue.Number, task.ID); err != nil {
			logging.WithComponent("pilot").Warn("Failed to notify task started", slog.Any("error", err))
		}
	}

	// Process ticket through orchestrator
	err := p.orchestrator.ProcessGithubTicket(ctx, task, projectPath)

	// Update GitHub with result
	if p.githubNotify != nil {
		if err != nil {
			if notifyErr := p.githubNotify.NotifyTaskFailed(ctx, repo.Owner.Login, repo.Name, issue.Number, err.Error()); notifyErr != nil {
				logging.WithComponent("pilot").Warn("Failed to notify task failed", slog.Any("error", notifyErr))
			}
		}
		// Success notification handled by orchestrator when PR is created
	}

	return err
}

// findProjectForGithubRepo finds the project path for a GitHub repo
func (p *Pilot) findProjectForGithubRepo(repo *github.Repository) string {
	// Try to match by repo name or full name
	for _, proj := range p.config.Projects {
		// Match by name
		if repo.Name == proj.Name {
			return proj.Path
		}
		// Match by full name (org/repo)
		if repo.FullName == proj.Name {
			return proj.Path
		}
	}

	// Return first project as fallback
	if len(p.config.Projects) > 0 {
		return p.config.Projects[0].Path
	}

	return ""
}

// handleJiraIssue handles a new Jira issue
func (p *Pilot) handleJiraIssue(ctx context.Context, issue *jira.Issue) error {
	logging.WithComponent("pilot").Info("Received Jira issue",
		slog.String("key", issue.Key),
		slog.String("summary", issue.Fields.Summary))

	// Convert to task
	task := jira.ConvertIssueToTask(issue, p.config.Adapters.Jira.BaseURL)

	// Find project for this Jira project
	projectPath := p.findProjectForJiraProject(issue.Fields.Project.Key)
	if projectPath == "" {
		return fmt.Errorf("no project configured for Jira project %s", issue.Fields.Project.Key)
	}

	// Process ticket through orchestrator
	return p.orchestrator.ProcessJiraTicket(ctx, task, projectPath)
}

// findProjectForJiraProject finds the project path for a Jira project key
func (p *Pilot) findProjectForJiraProject(projectKey string) string {
	for _, proj := range p.config.Projects {
		if proj.Name == projectKey {
			return proj.Path
		}
	}

	// Return first project as fallback
	if len(p.config.Projects) > 0 {
		return p.config.Projects[0].Path
	}

	return ""
}

// handleAsanaTask handles a new Asana task (GH-2044)
func (p *Pilot) handleAsanaTask(ctx context.Context, task *asana.Task) error {
	logging.WithComponent("pilot").Info("Received Asana task",
		slog.String("gid", task.GID),
		slog.String("name", task.Name))

	taskInfo := asana.ConvertToTaskInfo(task)

	// Find project (use first project as fallback)
	projectPath := p.defaultProjectPath()

	return p.orchestrator.ProcessAsanaTicket(ctx, taskInfo, projectPath)
}

// handlePlaneWorkItem handles a new Plane work item (GH-2044)
func (p *Pilot) handlePlaneWorkItem(ctx context.Context, item *plane.WebhookWorkItemData) error {
	logging.WithComponent("pilot").Info("Received Plane work item",
		slog.String("id", item.ID),
		slog.String("name", item.Name))

	projectPath := p.defaultProjectPath()

	return p.orchestrator.ProcessPlaneTicket(ctx, item, projectPath)
}

// defaultProjectPath returns the first configured project path
func (p *Pilot) defaultProjectPath() string {
	if len(p.config.Projects) > 0 {
		return p.config.Projects[0].Path
	}
	return ""
}

// handleGitlabIssue handles a new GitLab issue
func (p *Pilot) handleGitlabIssue(ctx context.Context, issue *gitlab.Issue, project *gitlab.Project) error {
	logging.WithComponent("pilot").Info("Received GitLab issue",
		slog.String("project", project.PathWithNamespace),
		slog.Int("iid", issue.IID),
		slog.String("title", issue.Title))

	// Convert to task
	task := gitlab.ConvertIssueToTask(issue, project)

	// Find project for this GitLab project
	projectPath := p.findProjectForGitlabProject(project)
	if projectPath == "" {
		return fmt.Errorf("no project configured for GitLab project %s", project.PathWithNamespace)
	}

	// Notify that task has started
	if p.gitlabNotify != nil {
		if err := p.gitlabNotify.NotifyTaskStarted(ctx, issue.IID, task.ID); err != nil {
			logging.WithComponent("pilot").Warn("Failed to notify task started", slog.Any("error", err))
		}
	}

	// Process ticket through orchestrator
	err := p.orchestrator.ProcessGitlabTicket(ctx, task, projectPath)

	// Update GitLab with result
	if p.gitlabNotify != nil {
		if err != nil {
			if notifyErr := p.gitlabNotify.NotifyTaskFailed(ctx, issue.IID, err.Error()); notifyErr != nil {
				logging.WithComponent("pilot").Warn("Failed to notify task failed", slog.Any("error", notifyErr))
			}
		}
		// Success notification handled by orchestrator when MR is created
	}

	return err
}

// findProjectForGitlabProject finds the project path for a GitLab project
func (p *Pilot) findProjectForGitlabProject(project *gitlab.Project) string {
	// Try to match by project name or path
	for _, proj := range p.config.Projects {
		// Match by name
		if project.Name == proj.Name {
			return proj.Path
		}
		// Match by path with namespace (namespace/project)
		if project.PathWithNamespace == proj.Name {
			return proj.Path
		}
	}

	// Return first project as fallback
	if len(p.config.Projects) > 0 {
		return p.config.Projects[0].Path
	}

	return ""
}

// parseGitlabWebhookPayload parses a GitLab webhook payload from a generic map
func (p *Pilot) parseGitlabWebhookPayload(payload map[string]interface{}) *gitlab.IssueWebhookPayload {
	result := &gitlab.IssueWebhookPayload{}

	// Parse object_kind and event_type
	if v, ok := payload["object_kind"].(string); ok {
		result.ObjectKind = v
	}
	if v, ok := payload["event_type"].(string); ok {
		result.EventType = v
	}

	// Parse project
	if projectData, ok := payload["project"].(map[string]interface{}); ok {
		result.Project = &gitlab.WebhookProject{}
		if v, ok := projectData["id"].(float64); ok {
			result.Project.ID = int(v)
		}
		if v, ok := projectData["name"].(string); ok {
			result.Project.Name = v
		}
		if v, ok := projectData["path_with_namespace"].(string); ok {
			result.Project.PathWithNamespace = v
		}
		if v, ok := projectData["web_url"].(string); ok {
			result.Project.WebURL = v
		}
		if v, ok := projectData["default_branch"].(string); ok {
			result.Project.DefaultBranch = v
		}
	}

	// Parse object_attributes (issue details)
	if attrs, ok := payload["object_attributes"].(map[string]interface{}); ok {
		result.ObjectAttributes = &gitlab.IssueAttributes{}
		if v, ok := attrs["id"].(float64); ok {
			result.ObjectAttributes.ID = int(v)
		}
		if v, ok := attrs["iid"].(float64); ok {
			result.ObjectAttributes.IID = int(v)
		}
		if v, ok := attrs["title"].(string); ok {
			result.ObjectAttributes.Title = v
		}
		if v, ok := attrs["description"].(string); ok {
			result.ObjectAttributes.Description = v
		}
		if v, ok := attrs["state"].(string); ok {
			result.ObjectAttributes.State = v
		}
		if v, ok := attrs["action"].(string); ok {
			result.ObjectAttributes.Action = v
		}
		if v, ok := attrs["url"].(string); ok {
			result.ObjectAttributes.URL = v
		}
	}

	// Parse labels
	if labelsData, ok := payload["labels"].([]interface{}); ok {
		for _, l := range labelsData {
			if labelMap, ok := l.(map[string]interface{}); ok {
				label := &gitlab.WebhookLabel{}
				if v, ok := labelMap["id"].(float64); ok {
					label.ID = int(v)
				}
				if v, ok := labelMap["title"].(string); ok {
					label.Title = v
				}
				result.Labels = append(result.Labels, label)
			}
		}
	}

	// Parse changes (for update events)
	if changesData, ok := payload["changes"].(map[string]interface{}); ok {
		result.Changes = &gitlab.IssueChanges{}
		if labelsChange, ok := changesData["labels"].(map[string]interface{}); ok {
			result.Changes.Labels = &gitlab.LabelChange{}

			if prev, ok := labelsChange["previous"].([]interface{}); ok {
				for _, l := range prev {
					if labelMap, ok := l.(map[string]interface{}); ok {
						label := &gitlab.WebhookLabel{}
						if v, ok := labelMap["id"].(float64); ok {
							label.ID = int(v)
						}
						if v, ok := labelMap["title"].(string); ok {
							label.Title = v
						}
						result.Changes.Labels.Previous = append(result.Changes.Labels.Previous, label)
					}
				}
			}

			if curr, ok := labelsChange["current"].([]interface{}); ok {
				for _, l := range curr {
					if labelMap, ok := l.(map[string]interface{}); ok {
						label := &gitlab.WebhookLabel{}
						if v, ok := labelMap["id"].(float64); ok {
							label.ID = int(v)
						}
						if v, ok := labelMap["title"].(string); ok {
							label.Title = v
						}
						result.Changes.Labels.Current = append(result.Changes.Labels.Current, label)
					}
				}
			}
		}
	}

	return result
}

// initAlerts initializes the alerts engine with configured channels
func (p *Pilot) initAlerts(cfg *config.Config) {
	log := logging.WithComponent("alerts")

	// Convert config.AlertsConfig to alerts.AlertConfig
	alertCfg := p.convertAlertsConfig(cfg.Alerts)

	// Create dispatcher with configured channels
	dispatcher := alerts.NewDispatcher(alertCfg, alerts.WithDispatcherLogger(log))

	// Register Slack channel if configured
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled && cfg.Adapters.Slack.BotToken != "" {
		p.slackClient = slack.NewClient(cfg.Adapters.Slack.BotToken)
		for _, ch := range cfg.Alerts.Channels {
			if ch.Type == "slack" && ch.Enabled && ch.Slack != nil {
				slackChannel := alerts.NewSlackChannel(ch.Name, p.slackClient, ch.Slack.Channel)
				dispatcher.RegisterChannel(slackChannel)
				log.Info("Registered Slack alert channel",
					slog.String("name", ch.Name),
					slog.String("channel", ch.Slack.Channel))
			}
		}
	}

	// Register Telegram channel if configured
	if cfg.Adapters.Telegram != nil && cfg.Adapters.Telegram.Enabled && cfg.Adapters.Telegram.BotToken != "" {
		p.telegramClient = telegram.NewClient(cfg.Adapters.Telegram.BotToken)
		for _, ch := range cfg.Alerts.Channels {
			if ch.Type == "telegram" && ch.Enabled && ch.Telegram != nil {
				telegramChannel := alerts.NewTelegramChannel(ch.Name, p.telegramClient, ch.Telegram.ChatID, ch.Telegram.MessageThreadID)
				dispatcher.RegisterChannel(telegramChannel)
				log.Info("Registered Telegram alert channel",
					slog.String("name", ch.Name),
					slog.Int64("chat_id", ch.Telegram.ChatID))
			}
		}
	}

	// Register webhook channels
	for _, ch := range cfg.Alerts.Channels {
		if ch.Type == "webhook" && ch.Enabled && ch.Webhook != nil {
			webhookChannel := alerts.NewWebhookChannel(ch.Name, &alerts.WebhookChannelConfig{
				URL:     ch.Webhook.URL,
				Method:  ch.Webhook.Method,
				Headers: ch.Webhook.Headers,
				Secret:  ch.Webhook.Secret,
			})
			dispatcher.RegisterChannel(webhookChannel)
			log.Info("Registered webhook alert channel",
				slog.String("name", ch.Name),
				slog.String("url", ch.Webhook.URL))
		}
	}

	// Register email channels
	for _, ch := range cfg.Alerts.Channels {
		if ch.Type == "email" && ch.Enabled && ch.Email != nil && ch.Email.SMTPHost != "" {
			sender := alerts.NewSMTPSender(
				ch.Email.SMTPHost,
				ch.Email.SMTPPort,
				ch.Email.From,
				ch.Email.Username,
				ch.Email.Password,
			)
			emailChannel := alerts.NewEmailChannel(ch.Name, sender, ch.Email)
			dispatcher.RegisterChannel(emailChannel)
			log.Info("Registered email alert channel",
				slog.String("name", ch.Name),
				slog.Int("recipients", len(ch.Email.To)))
		}
	}

	// Register PagerDuty channels
	for _, ch := range cfg.Alerts.Channels {
		if ch.Type == "pagerduty" && ch.Enabled && ch.PagerDuty != nil {
			pdChannel := alerts.NewPagerDutyChannel(ch.Name, ch.PagerDuty)
			dispatcher.RegisterChannel(pdChannel)
			log.Info("Registered PagerDuty alert channel",
				slog.String("name", ch.Name))
		}
	}

	// Create engine with dispatcher
	p.alertEngine = alerts.NewEngine(alertCfg,
		alerts.WithLogger(log),
		alerts.WithDispatcher(dispatcher),
		// GH-4562: lets the stuck-task evictor stall an orphan-evicted
		// task's still-alive execution row instead of silently dropping the
		// tracker entry and leaving a live-looking claim behind.
		alerts.WithExecutionLifecycle(executor.NewExecutionLifecycle(p.store)),
		// GH-5095: wire active-alert persistence (GH-4890/PR#5090) — the
		// legacy orchestrator/webhook-mode path's alert engine construction
		// site, same dead-plumbing gap as the two cmd/pilot sites (GH-4716).
		alerts.WithActiveAlertStore(p.store),
		// GH-5209: wire counter-checkpoint persistence so level-triggered
		// stats-event rules (circuit_breaker_trip) survive a restart without
		// replaying their pre-restart standing counter as a fresh alert.
		alerts.WithAlertCounterStore(p.store),
	)

	// Wire alerts engine to executor via adapter
	adapter := alerts.NewEngineAdapter(p.alertEngine)
	p.orchestrator.SetAlertProcessor(adapter)
	// GH-4716: also cover the stuck-task evictor's own terminal writes (the
	// ExecutionLifecycle passed via WithExecutionLifecycle above) — without
	// this, its Finish call has an alertProcessor of nil and the TASK-441 L5
	// finish-tripwire sweep it triggers has nowhere to relay dead-man
	// signals, even though every other terminal path in this mode does.
	p.alertEngine.WireLifecycleAlertProcessor(adapter)

	// TASK-441 L5 (GH-4716): register the four finish-tripwire dead-man
	// trackers once at startup, mirroring the self-review/label-lifecycle
	// registrations (L2, GH-4709) elsewhere.
	for _, name := range executor.FinishTripwireTrackerNames {
		p.alertEngine.RegisterDeadManTracker(name, alerts.AlertTypeFinishTripwireFailureStreak, alerts.DefaultDeadManFailureThreshold, alerts.DefaultDeadManWindow)
	}

	log.Info("Alerts engine initialized",
		slog.Int("rules", len(alertCfg.Rules)),
		slog.Int("channels", len(dispatcher.ListChannels())))
}

// convertAlertsConfig converts config.AlertsConfig to alerts.AlertConfig
func (p *Pilot) convertAlertsConfig(cfg *config.AlertsConfig) *alerts.AlertConfig {
	// Build channel configs (channel-specific configs are shared types, passed directly)
	channels := make([]alerts.ChannelConfigInput, len(cfg.Channels))
	for i, ch := range cfg.Channels {
		channels[i] = alerts.ChannelConfigInput{
			Name:       ch.Name,
			Type:       ch.Type,
			Enabled:    ch.Enabled,
			Severities: ch.Severities,
			Slack:      ch.Slack,     // Same type, direct pass-through
			Telegram:   ch.Telegram,  // Same type, direct pass-through
			Email:      ch.Email,     // Same type, direct pass-through
			Webhook:    ch.Webhook,   // Same type, direct pass-through
			PagerDuty:  ch.PagerDuty, // Same type, direct pass-through
		}
	}

	// Build rule configs
	rules := make([]alerts.RuleConfigInput, len(cfg.Rules))
	for i, r := range cfg.Rules {
		rules[i] = alerts.RuleConfigInput{
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
		}
	}

	// Build defaults
	defaults := alerts.DefaultsConfigInput{
		Cooldown:           cfg.Defaults.Cooldown,
		DefaultSeverity:    cfg.Defaults.DefaultSeverity,
		SuppressDuplicates: cfg.Defaults.SuppressDuplicates,
	}

	return alerts.FromConfigAlerts(cfg.Enabled, channels, rules, defaults)
}
