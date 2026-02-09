package pilot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/gitlab"
	"github.com/alekspetrov/pilot/internal/adapters/jira"
	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/approval"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/gateway"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/orchestrator"
	"github.com/alekspetrov/pilot/internal/webhooks"
)

// Pilot is the main application
type Pilot struct {
	config             *config.Config
	gateway            *gateway.Server
	orchestrator       *orchestrator.Orchestrator
	linearMultiWH      *linear.MultiWorkspaceHandler // Multi-workspace handler (GH-391)
	linearClient       *linear.Client                // Legacy single-workspace client
	linearWH           *linear.WebhookHandler        // Legacy single-workspace handler
	linearNotify       *linear.Notifier              // Legacy single-workspace notifier
	githubClient       *github.Client
	githubWH           *github.WebhookHandler
	githubNotify       *github.Notifier
	gitlabClient       *gitlab.Client
	gitlabWH           *gitlab.WebhookHandler
	gitlabNotify       *gitlab.Notifier
	jiraClient         *jira.Client
	jiraWH             *jira.WebhookHandler
	slackNotify        *slack.Notifier
	slackClient        *slack.Client
	slackInteractionWH *slack.InteractionHandler
	slackApprovalHdlr  *approval.SlackHandler
	telegramClient     *telegram.Client
	telegramHandler    *telegram.Handler // Telegram polling handler (GH-349)
	telegramRunner     *executor.Runner  // Runner for Telegram tasks (GH-349)
	githubPoller       *github.Poller    // GitHub polling handler (GH-350)
	alertEngine        *alerts.Engine
	store              *memory.Store
	graph              *memory.KnowledgeGraph
	webhookManager     *webhooks.Manager
	approvalMgr        *approval.Manager

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

// WithGitHubPoller enables GitHub polling in gateway mode (GH-350)
// The poller is created externally with all necessary options and passed in.
func WithGitHubPoller(poller *github.Poller) Option {
	return func(p *Pilot) {
		p.githubPoller = poller
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
			p.approvalMgr.RegisterHandler(p.slackApprovalHdlr)
			logging.WithComponent("pilot").Info("registered Slack approval handler",
				slog.String("channel", approvalChannel))

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
		p.githubClient = github.NewClient(cfg.Adapters.GitHub.Token)
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

	// Initialize alerts engine if enabled
	if cfg.Alerts != nil && cfg.Alerts.Enabled {
		p.initAlerts(cfg)
	}

	// Initialize gateway with webhook secrets
	gatewayCfg := cfg.Gateway
	if cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.WebhookSecret != "" {
		gatewayCfg.GithubWebhookSecret = cfg.Adapters.GitHub.WebhookSecret
	}
	p.gateway = gateway.NewServer(gatewayCfg)

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
			if err := p.jiraWH.Handle(ctx, payload); err != nil {
				logging.WithComponent("pilot").Error("Jira webhook error", slog.Any("error", err))
			}
		})
	}

	// Register Slack interaction webhook handler for approval buttons
	if p.slackInteractionWH != nil {
		p.gateway.RegisterHandler("/webhooks/slack/interactions", p.slackInteractionWH)
		logging.WithComponent("pilot").Info("registered Slack interaction webhook handler",
			slog.String("path", "/webhooks/slack/interactions"))
	}

	// Apply functional options (GH-349)
	for _, opt := range opts {
		opt(p)
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

		p.telegramHandler = telegram.NewHandler(&telegram.HandlerConfig{
			BotToken:      cfg.Adapters.Telegram.BotToken,
			ProjectPath:   projectPath,
			Projects:      config.NewProjectSource(cfg),
			AllowedIDs:    allowedIDs,
			Transcription: cfg.Adapters.Telegram.Transcription,
			RateLimit:     cfg.Adapters.Telegram.RateLimit,
			PlainTextMode: true, // Default to plain text mode
		}, p.telegramRunner)

		if len(allowedIDs) == 0 {
			logging.WithComponent("pilot").Warn("SECURITY: telegram allowed_ids is empty - ALL users can interact with the bot!")
		}

		logging.WithComponent("pilot").Info("Telegram handler initialized for gateway mode")
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
	go func() {
		defer p.wg.Done()
		if err := p.gateway.Start(p.ctx); err != nil {
			logging.WithComponent("pilot").Error("Gateway error", slog.Any("error", err))
		}
	}()

	// Start Telegram polling if handler is initialized (GH-349)
	if p.telegramHandler != nil {
		p.telegramHandler.StartPolling(p.ctx)
		logging.WithComponent("pilot").Info("Telegram polling started in gateway mode")
	}

	// Start GitHub polling if poller is initialized (GH-350)
	if p.githubPoller != nil {
		go p.githubPoller.Start(p.ctx)
		logging.WithComponent("pilot").Info("GitHub polling started in gateway mode")
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

	// Find project for this issue - first try workspace-specific mapping
	var projectPath string
	pilotProject := ws.ResolvePilotProject(issue)
	if pilotProject != "" {
		// Look up the Pilot project config by name
		if proj := p.config.GetProjectByName(pilotProject); proj != nil {
			projectPath = proj.Path
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

// OnProgress registers a callback for task progress updates
func (p *Pilot) OnProgress(callback func(taskID, phase string, progress int, message string)) {
	p.orchestrator.OnProgress(callback)
}

// OnToken registers a callback for token usage updates
func (p *Pilot) OnToken(name string, callback func(taskID string, inputTokens, outputTokens int64)) {
	p.orchestrator.OnToken(name, callback)
}

// GetTaskStates returns current task states from the orchestrator
func (p *Pilot) GetTaskStates() []*executor.TaskState {
	return p.orchestrator.GetTaskStates()
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
				telegramChannel := alerts.NewTelegramChannel(ch.Name, p.telegramClient, ch.Telegram.ChatID)
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
	)

	// Wire alerts engine to executor via adapter
	adapter := alerts.NewEngineAdapter(p.alertEngine)
	p.orchestrator.SetAlertProcessor(adapter)

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
