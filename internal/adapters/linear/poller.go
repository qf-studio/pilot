package linear

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// IssueResult is returned by the issue handler
type IssueResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string // Head commit SHA of the PR (GH-1361: for autopilot wiring)
	BranchName string // Head branch name e.g. "pilot/APP-123" (GH-1361: for autopilot wiring)
	Error      error
}

// ProcessedStore persists which Linear issues have been processed across restarts.
// GH-1351: Linear uses string IDs unlike GitHub's integer IDs.
type ProcessedStore interface {
	MarkLinearIssueProcessed(issueID string, result string) error
	UnmarkLinearIssueProcessed(issueID string) error
	IsLinearIssueProcessed(issueID string) (bool, error)
	LoadLinearProcessedIssues() (map[string]bool, error)
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

	// GH-1351: Persistent processed store (optional)
	processedStore ProcessedStore

	// GH-1700: OnPRCreated is called when a PR is created after issue processing
	OnPRCreated func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)

	// GH-1357: Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithOnLinearIssue sets the callback for new issues
func WithOnLinearIssue(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithOnPRCreated sets the callback for PR creation events.
// GH-1700: Mirrors the GitHub poller pattern for autopilot wiring.
func WithOnPRCreated(fn func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)) PollerOption {
	return func(p *Poller) {
		p.OnPRCreated = fn
	}
}

// WithPollerLogger sets the logger for the poller
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithProcessedStore sets the persistent store for processed issue tracking.
// GH-1351: On startup, processed issues are loaded from the store to prevent re-processing after hot upgrade.
func WithProcessedStore(store ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
	}
}

// WithMaxConcurrent sets the maximum number of parallel issue executions.
// GH-1357: Ported from GitHub poller parallel execution pattern.
func WithMaxConcurrent(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.maxConcurrent = n
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

	// GH-1351: Load processed issues from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.LoadLinearProcessedIssues()
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for id := range loaded {
				p.processed[id] = true
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed issues from store", slog.Int("count", len(loaded)))
		}
	}

	// GH-1357: Initialize parallel semaphore
	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

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
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	// GH-1355: Recover orphaned in-progress issues from previous run before starting poll loop
	p.recoverOrphanedIssues(ctx)

	// Initial check
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Linear poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
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

	// GH-1351: Auto-create status labels if they don't exist.
	// These labels are required for deduplication after hot upgrade.
	// Colors chosen to match typical status semantics: blue=in-progress, green=done, red=failed
	p.inProgressLabelID, err = p.client.GetOrCreateLabel(ctx, p.config.TeamID, "pilot-in-progress", "#0066FF")
	if err != nil {
		p.logger.Warn("Failed to get/create pilot-in-progress label", slog.Any("error", err))
	}

	p.doneLabelID, err = p.client.GetOrCreateLabel(ctx, p.config.TeamID, "pilot-done", "#00AA55")
	if err != nil {
		p.logger.Warn("Failed to get/create pilot-done label", slog.Any("error", err))
	}

	p.failedLabelID, err = p.client.GetOrCreateLabel(ctx, p.config.TeamID, "pilot-failed", "#DD0000")
	if err != nil {
		p.logger.Warn("Failed to get/create pilot-failed label", slog.Any("error", err))
	}

	return nil
}

// recoverOrphanedIssues finds issues with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
// GH-1355: This handles restart/crash scenarios where issues were left orphaned.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	if p.inProgressLabelID == "" {
		return
	}

	// List issues that have both the pilot label and in-progress label
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		TeamID:     p.config.TeamID,
		Label:      "pilot-in-progress",
		ProjectIDs: p.config.ProjectIDs,
	})
	if err != nil {
		p.logger.Warn("Failed to check for orphaned issues", slog.Any("error", err))
		return
	}

	if len(issues) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress issues",
		slog.Int("count", len(issues)),
	)

	for _, issue := range issues {
		if err := p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.String("identifier", issue.Identifier),
				slog.Any("error", err),
			)
			continue
		}
		p.logger.Info("Recovered orphaned issue",
			slog.String("identifier", issue.Identifier),
			slog.String("title", issue.Title),
		)
	}
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

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(issue.ID)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Linear issue for parallel execution",
			slog.String("identifier", issue.Identifier),
			slog.String("title", issue.Title),
		)

		// Use mutex to coordinate stopping flag check with WaitGroup Add
		p.wgMu.Lock()
		if p.stopping.Load() {
			p.wgMu.Unlock()
			<-p.semaphore // release slot we acquired
			return
		}
		p.activeWg.Add(1)
		p.wgMu.Unlock()

		go p.processIssueAsync(ctx, issue)
	}
}

// processIssueAsync handles a single issue in a goroutine.
// GH-1357: Extracted to enable parallel execution.
func (p *Poller) processIssueAsync(ctx context.Context, issue *Issue) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }() // release slot

	if p.onIssue == nil {
		return
	}

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
		return
	}

	// Remove in-progress label
	if p.inProgressLabelID != "" {
		_ = p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID)
	}

	// Add done label on success
	if result != nil && result.Success && p.doneLabelID != "" {
		_ = p.client.AddLabel(ctx, issue.ID, p.doneLabelID)
	}

	// GH-1700: Notify autopilot controller about new PR
	if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
		p.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName, "")
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

	// GH-1351: Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkLinearIssueProcessed(id, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.String("issue", id), slog.Any("error", err))
		}
	}
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

// ClearProcessed removes a single issue from the processed map.
// GH-1351: Used when pilot-failed label is removed to allow the issue to be retried.
func (p *Poller) ClearProcessed(id string) {
	p.mu.Lock()
	delete(p.processed, id)
	p.mu.Unlock()

	// Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.UnmarkLinearIssueProcessed(id); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.String("id", id),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.String("id", id))
}

// Drain stops accepting new issues and waits for active executions to finish.
// GH-1357: Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new issues will be accepted")
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
	p.logger.Info("Poller drained — all active tasks completed")
}

// WaitForActive waits for all active parallel goroutines to finish.
// GH-1357: Used in tests to synchronize after checkForNewIssues.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}
