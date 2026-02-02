package pilot

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/gitlab"
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
	config         *config.Config
	gateway        *gateway.Server
	orchestrator   *orchestrator.Orchestrator
	linearClient   *linear.Client
	linearWH       *linear.WebhookHandler
	githubClient   *github.Client
	githubWH       *github.WebhookHandler
	githubNotify   *github.Notifier
	gitlabClient   *gitlab.Client
	gitlabWH       *gitlab.WebhookHandler
	gitlabNotify   *gitlab.Notifier
	slackNotify        *slack.Notifier
	slackClient        *slack.Client
	slackInteractionWH *slack.InteractionHandler
	slackApprovalHdlr  *approval.SlackHandler
	telegramClient     *telegram.Client
	alertEngine        *alerts.Engine
	store              *memory.Store
	graph              *memory.KnowledgeGraph
	webhookManager     *webhooks.Manager
	approvalMgr        *approval.Manager

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

// New creates a new Pilot instance
func New(cfg *config.Config) (*Pilot, error) {
	ctx, cancel := context.WithCancel(context.Background())

	p := &Pilot{
		config: cfg,
		ctx:    ctx,
		cancel: cancel,
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

	// Initialize Linear adapter if enabled
	if cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled {
		p.linearClient = linear.NewClient(cfg.Adapters.Linear.APIKey)
		p.linearWH = linear.NewWebhookHandler(p.linearClient, cfg.Adapters.Linear.PilotLabel)
		p.linearWH.OnIssue(p.handleLinearIssue)
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
	if p.linearWH != nil {
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

	// Register Slack interaction webhook handler for approval buttons
	if p.slackInteractionWH != nil {
		p.gateway.RegisterHandler("/webhooks/slack/interactions", p.slackInteractionWH)
		logging.WithComponent("pilot").Info("registered Slack interaction webhook handler",
			slog.String("path", "/webhooks/slack/interactions"))
	}

	return p, nil
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

	logging.WithComponent("pilot").Info("Pilot started",
		slog.String("host", p.config.Gateway.Host),
		slog.Int("port", p.config.Gateway.Port))
	return nil
}

// Stop stops Pilot
func (p *Pilot) Stop() error {
	logging.WithComponent("pilot").Info("Stopping Pilot")

	p.cancel()

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

// handleLinearIssue handles a new Linear issue
func (p *Pilot) handleLinearIssue(ctx context.Context, issue *linear.Issue) error {
	logging.WithComponent("pilot").Info("Received Linear issue",
		slog.String("identifier", issue.Identifier),
		slog.String("title", issue.Title))

	// Find project for this issue
	projectPath := p.findProjectForIssue(issue)
	if projectPath == "" {
		return fmt.Errorf("no project configured for issue %s", issue.Identifier)
	}

	// Process ticket through orchestrator
	return p.orchestrator.ProcessTicket(ctx, issue, projectPath)
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
	return map[string]interface{}{
		"running": true,
		"tasks":   p.orchestrator.GetTaskStates(),
		"config": map[string]interface{}{
			"gateway":  fmt.Sprintf("%s:%d", p.config.Gateway.Host, p.config.Gateway.Port),
			"linear":   p.config.Adapters.Linear != nil && p.config.Adapters.Linear.Enabled,
			"github":   p.config.Adapters.GitHub != nil && p.config.Adapters.GitHub.Enabled,
			"gitlab":   p.config.Adapters.GitLab != nil && p.config.Adapters.GitLab.Enabled,
			"slack":    p.config.Adapters.Slack != nil && p.config.Adapters.Slack.Enabled,
			"webhooks": p.webhookManager.IsEnabled(),
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
