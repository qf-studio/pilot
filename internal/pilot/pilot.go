package pilot

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/adapters/linear"
	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/gateway"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/orchestrator"
)

// Pilot is the main application
type Pilot struct {
	config       *config.Config
	gateway      *gateway.Server
	orchestrator *orchestrator.Orchestrator
	linearClient *linear.Client
	linearWH     *linear.WebhookHandler
	githubClient *github.Client
	githubWH     *github.WebhookHandler
	githubNotify *github.Notifier
	slackNotify  *slack.Notifier
	store        *memory.Store
	graph        *memory.KnowledgeGraph

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
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

	// Initialize Slack notifier if enabled
	if cfg.Adapters.Slack != nil && cfg.Adapters.Slack.Enabled {
		p.slackNotify = slack.NewNotifier(cfg.Adapters.Slack)
	}

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
	if cfg.Adapters.Github != nil && cfg.Adapters.Github.Enabled {
		p.githubClient = github.NewClient(cfg.Adapters.Github.Token)
		p.githubWH = github.NewWebhookHandler(
			p.githubClient,
			cfg.Adapters.Github.WebhookSecret,
			cfg.Adapters.Github.PilotLabel,
		)
		p.githubWH.OnIssue(p.handleGithubIssue)
		p.githubNotify = github.NewNotifier(p.githubClient, cfg.Adapters.Github.PilotLabel)
	}

	// Initialize gateway
	p.gateway = gateway.NewServer(cfg.Gateway)

	// Register webhook handlers
	if p.linearWH != nil {
		p.gateway.Router().RegisterWebhookHandler("linear", func(payload map[string]interface{}) {
			if err := p.linearWH.Handle(ctx, payload); err != nil {
				log.Printf("Linear webhook error: %v", err)
			}
		})
	}

	if p.githubWH != nil {
		p.gateway.Router().RegisterWebhookHandler("github", func(payload map[string]interface{}) {
			eventType, _ := payload["_event_type"].(string)
			if err := p.githubWH.Handle(ctx, eventType, payload); err != nil {
				log.Printf("GitHub webhook error: %v", err)
			}
		})
	}

	return p, nil
}

// Start starts Pilot
func (p *Pilot) Start() error {
	log.Println("Starting Pilot...")

	// Start orchestrator
	p.orchestrator.Start()

	// Start gateway
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := p.gateway.Start(p.ctx); err != nil {
			log.Printf("Gateway error: %v", err)
		}
	}()

	log.Printf("Pilot started on %s:%d", p.config.Gateway.Host, p.config.Gateway.Port)
	return nil
}

// Stop stops Pilot
func (p *Pilot) Stop() error {
	log.Println("Stopping Pilot...")

	p.cancel()
	p.orchestrator.Stop()
	_ = p.gateway.Shutdown()
	_ = p.store.Close()
	p.wg.Wait()

	log.Println("Pilot stopped")
	return nil
}

// Wait waits for Pilot to stop
func (p *Pilot) Wait() {
	p.wg.Wait()
}

// handleLinearIssue handles a new Linear issue
func (p *Pilot) handleLinearIssue(ctx context.Context, issue *linear.Issue) error {
	log.Printf("Received Linear issue: %s - %s", issue.Identifier, issue.Title)

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
	return map[string]interface{}{
		"running": true,
		"tasks":   p.orchestrator.GetTaskStates(),
		"config": map[string]interface{}{
			"gateway": fmt.Sprintf("%s:%d", p.config.Gateway.Host, p.config.Gateway.Port),
			"linear":  p.config.Adapters.Linear != nil && p.config.Adapters.Linear.Enabled,
			"github":  p.config.Adapters.Github != nil && p.config.Adapters.Github.Enabled,
			"slack":   p.config.Adapters.Slack != nil && p.config.Adapters.Slack.Enabled,
		},
	}
}

// Router returns the gateway router for registering handlers
func (p *Pilot) Router() *gateway.Router {
	return p.gateway.Router()
}

// handleGithubIssue handles a new GitHub issue
func (p *Pilot) handleGithubIssue(ctx context.Context, issue *github.Issue, repo *github.Repository) error {
	log.Printf("Received GitHub issue: %s/%s#%d - %s", repo.Owner.Login, repo.Name, issue.Number, issue.Title)

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
			log.Printf("Failed to notify task started: %v", err)
		}
	}

	// Process ticket through orchestrator
	err := p.orchestrator.ProcessGithubTicket(ctx, task, projectPath)

	// Update GitHub with result
	if p.githubNotify != nil {
		if err != nil {
			if notifyErr := p.githubNotify.NotifyTaskFailed(ctx, repo.Owner.Login, repo.Name, issue.Number, err.Error()); notifyErr != nil {
				log.Printf("Failed to notify task failed: %v", notifyErr)
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
