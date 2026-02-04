package linear

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// IssueResult is returned by the issue handler
type IssueResult struct {
	Success  bool
	PRNumber int
	PRURL    string
	Error    error
}

// Poller polls Linear for issues with a specific label
type Poller struct {
	client    *Client
	config    *WorkspaceConfig
	interval  time.Duration
	processed map[string]bool // Linear uses string IDs
	mu        sync.RWMutex
	onIssue   func(ctx context.Context, issue *Issue) (*IssueResult, error)
	logger    *slog.Logger

	// Labels cache
	pilotLabelID      string
	inProgressLabelID string
	doneLabelID       string
	failedLabelID     string
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithOnLinearIssue sets the callback for new issues
func WithOnLinearIssue(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithPollerLogger sets the logger for the poller
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// NewPoller creates a new Linear issue poller
func NewPoller(client *Client, config *WorkspaceConfig, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:    client,
		config:    config,
		interval:  interval,
		processed: make(map[string]bool),
		logger:    logging.WithComponent("linear-poller"),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Start begins polling for issues
func (p *Poller) Start(ctx context.Context) error {
	// Cache label IDs on startup
	if err := p.cacheLabelIDs(ctx); err != nil {
		return fmt.Errorf("failed to cache label IDs: %w", err)
	}

	p.logger.Info("Starting Linear poller",
		slog.String("team", p.config.TeamID),
		slog.String("label", p.config.PilotLabel),
		slog.Duration("interval", p.interval),
	)

	// Initial check
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Linear poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

func (p *Poller) cacheLabelIDs(ctx context.Context) error {
	var err error

	p.pilotLabelID, err = p.client.GetLabelByName(ctx, p.config.TeamID, p.config.PilotLabel)
	if err != nil {
		return fmt.Errorf("pilot label: %w", err)
	}

	// These labels are optional - create if needed or skip
	p.inProgressLabelID, _ = p.client.GetLabelByName(ctx, p.config.TeamID, "pilot-in-progress")
	p.doneLabelID, _ = p.client.GetLabelByName(ctx, p.config.TeamID, "pilot-done")
	p.failedLabelID, _ = p.client.GetLabelByName(ctx, p.config.TeamID, "pilot-failed")

	return nil
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		TeamID:     p.config.TeamID,
		Label:      p.config.PilotLabel,
		ProjectIDs: p.config.ProjectIDs,
	})
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	// Sort by creation date (oldest first)
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].CreatedAt.Before(issues[j].CreatedAt)
	})

	for _, issue := range issues {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[issue.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if has in-progress, done, or failed label
		if p.hasStatusLabel(issue) {
			p.markProcessed(issue.ID)
			continue
		}

		// Process the issue
		p.logger.Info("Found new Linear issue",
			slog.String("identifier", issue.Identifier),
			slog.String("title", issue.Title),
		)

		if p.onIssue != nil {
			// Add in-progress label
			if p.inProgressLabelID != "" {
				_ = p.client.AddLabel(ctx, issue.ID, p.inProgressLabelID)
			}

			result, err := p.onIssue(ctx, issue)
			if err != nil {
				p.logger.Error("Failed to process issue",
					slog.String("identifier", issue.Identifier),
					slog.Any("error", err),
				)
				// Remove in-progress label, add failed label
				if p.inProgressLabelID != "" {
					_ = p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID)
				}
				if p.failedLabelID != "" {
					_ = p.client.AddLabel(ctx, issue.ID, p.failedLabelID)
				}
				p.markProcessed(issue.ID)
				continue
			}

			// Remove in-progress label
			if p.inProgressLabelID != "" {
				_ = p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID)
			}

			// Add done label on success
			if result != nil && result.Success && p.doneLabelID != "" {
				_ = p.client.AddLabel(ctx, issue.ID, p.doneLabelID)
			}
		}

		p.markProcessed(issue.ID)
	}
}

func (p *Poller) hasStatusLabel(issue *Issue) bool {
	for _, label := range issue.Labels {
		switch label.Name {
		case "pilot-in-progress", "pilot-done", "pilot-failed":
			return true
		}
	}
	return false
}

func (p *Poller) markProcessed(id string) {
	p.mu.Lock()
	p.processed[id] = true
	p.mu.Unlock()
}

// IsProcessed checks if an issue has been processed
func (p *Poller) IsProcessed(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[id]
}

// ProcessedCount returns the number of processed issues
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed issues map
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[string]bool)
	p.mu.Unlock()
}
