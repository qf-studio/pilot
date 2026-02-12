package jira

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Status labels for tracking issue progress
const (
	LabelInProgress = "pilot-in-progress"
	LabelDone       = "pilot-done"
	LabelFailed     = "pilot-failed"
)

// IssueResult is returned by the issue handler
type IssueResult struct {
	Success  bool
	PRNumber int
	PRURL    string
	Error    error
}

// Poller polls Jira for issues with the pilot label
type Poller struct {
	client     *Client
	config     *Config
	interval   time.Duration
	processed  map[string]bool // Jira uses string IDs (issue keys like PROJ-123)
	mu         sync.RWMutex
	onIssue    func(ctx context.Context, issue *Issue) (*IssueResult, error)
	logger     *slog.Logger
	pilotLabel string
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithOnJiraIssue sets the callback for new issues
func WithOnJiraIssue(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithJiraPollerLogger sets the logger for the poller
func WithJiraPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// NewPoller creates a new Jira issue poller
func NewPoller(client *Client, config *Config, interval time.Duration, opts ...PollerOption) *Poller {
	pilotLabel := config.PilotLabel
	if pilotLabel == "" {
		pilotLabel = "pilot"
	}

	p := &Poller{
		client:     client,
		config:     config,
		interval:   interval,
		processed:  make(map[string]bool),
		logger:     logging.WithComponent("jira-poller"),
		pilotLabel: pilotLabel,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Start begins polling for issues
func (p *Poller) Start(ctx context.Context) error {
	p.logger.Info("Starting Jira poller",
		slog.String("label", p.pilotLabel),
		slog.String("project", p.config.ProjectKey),
		slog.Duration("interval", p.interval),
	)

	// Initial check
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Jira poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// buildJQL constructs the JQL query for finding pilot issues
func (p *Poller) buildJQL() string {
	var parts []string

	// Filter by label
	parts = append(parts, fmt.Sprintf("labels = \"%s\"", p.pilotLabel))

	// Filter by project if configured
	if p.config.ProjectKey != "" {
		parts = append(parts, fmt.Sprintf("project = \"%s\"", p.config.ProjectKey))
	}

	// Exclude done/closed statuses (using status category for broader coverage)
	parts = append(parts, "statusCategory != Done")

	// Order by created date (oldest first)
	jql := strings.Join(parts, " AND ") + " ORDER BY created ASC"

	return jql
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	jql := p.buildJQL()
	issues, err := p.client.SearchIssues(ctx, jql, 50)
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	// Sort by creation date (oldest first) - API should return sorted, but ensure it
	sort.Slice(issues, func(i, j int) bool {
		// Parse Jira's datetime format
		ti, _ := time.Parse("2006-01-02T15:04:05.000-0700", issues[i].Fields.Created)
		tj, _ := time.Parse("2006-01-02T15:04:05.000-0700", issues[j].Fields.Created)
		return ti.Before(tj)
	})

	for _, issue := range issues {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[issue.Key]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if has in-progress, done, or failed label
		if p.hasStatusLabel(issue) {
			// Only mark as processed if it has done label (allow retry of failed)
			if p.client.HasLabel(issue, LabelDone) {
				p.markProcessed(issue.Key)
			}
			continue
		}

		// Process the issue
		p.logger.Info("Found new Jira issue",
			slog.String("key", issue.Key),
			slog.String("summary", issue.Fields.Summary),
		)

		if p.onIssue != nil {
			// Add in-progress label
			if err := p.client.AddLabel(ctx, issue.Key, LabelInProgress); err != nil {
				p.logger.Warn("Failed to add in-progress label",
					slog.String("key", issue.Key),
					slog.Any("error", err),
				)
			}

			result, err := p.onIssue(ctx, issue)
			if err != nil {
				p.logger.Error("Failed to process issue",
					slog.String("key", issue.Key),
					slog.Any("error", err),
				)
				// Remove in-progress label, add failed label
				_ = p.client.RemoveLabel(ctx, issue.Key, LabelInProgress)
				_ = p.client.AddLabel(ctx, issue.Key, LabelFailed)
				// Don't mark as processed so it can be retried after fixing
				continue
			}

			// Remove in-progress label
			_ = p.client.RemoveLabel(ctx, issue.Key, LabelInProgress)

			// Add done label on success
			if result != nil && result.Success {
				_ = p.client.AddLabel(ctx, issue.Key, LabelDone)
			}
		}

		p.markProcessed(issue.Key)
	}
}

func (p *Poller) hasStatusLabel(issue *Issue) bool {
	return p.client.HasLabel(issue, LabelInProgress) ||
		p.client.HasLabel(issue, LabelDone) ||
		p.client.HasLabel(issue, LabelFailed)
}

func (p *Poller) markProcessed(key string) {
	p.mu.Lock()
	p.processed[key] = true
	p.mu.Unlock()
}

// IsProcessed checks if an issue has been processed
func (p *Poller) IsProcessed(key string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[key]
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

// ClearProcessed removes a specific issue from the processed map (for retry)
func (p *Poller) ClearProcessed(key string) {
	p.mu.Lock()
	delete(p.processed, key)
	p.mu.Unlock()
}
