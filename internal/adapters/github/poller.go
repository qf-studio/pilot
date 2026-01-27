package github

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Poller polls GitHub for issues with a specific label
type Poller struct {
	client    *Client
	owner     string
	repo      string
	label     string
	interval  time.Duration
	processed map[int]bool
	mu        sync.RWMutex
	onIssue   func(ctx context.Context, issue *Issue) error
	logger    *slog.Logger
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithPollerLogger sets the logger for the poller
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithOnIssue sets the callback for new issues
func WithOnIssue(fn func(ctx context.Context, issue *Issue) error) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// NewPoller creates a new GitHub issue poller
func NewPoller(client *Client, repo string, label string, interval time.Duration, opts ...PollerOption) (*Poller, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format, expected owner/repo: %s", repo)
	}

	p := &Poller{
		client:    client,
		owner:     parts[0],
		repo:      parts[1],
		label:     label,
		interval:  interval,
		processed: make(map[int]bool),
		logger:    logging.WithComponent("github-poller"),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p, nil
}

// Start begins polling for issues
func (p *Poller) Start(ctx context.Context) {
	p.logger.Info("Starting GitHub poller",
		slog.String("repo", p.owner+"/"+p.repo),
		slog.String("label", p.label),
		slog.Duration("interval", p.interval),
	)

	// Do an initial check immediately
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("GitHub poller stopped")
			return
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// checkForNewIssues fetches issues and processes new ones
func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label},
		State:  StateOpen,
		Sort:   "updated",
	})
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	for _, issue := range issues {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[issue.Number]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if already in progress or done
		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) {
			p.markProcessed(issue.Number)
			continue
		}

		// Process the issue
		p.logger.Info("Found new issue",
			slog.Int("number", issue.Number),
			slog.String("title", issue.Title),
		)

		if p.onIssue != nil {
			if err := p.onIssue(ctx, issue); err != nil {
				p.logger.Error("Failed to process issue",
					slog.Int("number", issue.Number),
					slog.Any("error", err),
				)
				// Don't mark as processed so we can retry
				continue
			}
		}

		p.markProcessed(issue.Number)
	}
}

// markProcessed marks an issue as processed
func (p *Poller) markProcessed(number int) {
	p.mu.Lock()
	p.processed[number] = true
	p.mu.Unlock()
}

// IsProcessed checks if an issue has been processed
func (p *Poller) IsProcessed(number int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[number]
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
	p.processed = make(map[int]bool)
	p.mu.Unlock()
}
