package jira

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// ProcessedStore persists which Jira issues have been processed across restarts.
// GH-1357: Jira uses string IDs (issue keys like PROJ-123).
type ProcessedStore interface {
	MarkJiraIssueProcessed(issueKey string, result string) error
	UnmarkJiraIssueProcessed(issueKey string) error
	IsJiraIssueProcessed(issueKey string) (bool, error)
	LoadJiraProcessedIssues() (map[string]bool, error)
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

	// GH-1357: Persistent processed store (optional)
	processedStore ProcessedStore

	// GH-1357: Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination
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

// WithProcessedStore sets the persistent store for processed issue tracking.
// GH-1357: On startup, processed issues are loaded from the store to prevent re-processing after hot upgrade.
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

	// GH-1357: Load processed issues from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.LoadJiraProcessedIssues()
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for key := range loaded {
				p.processed[key] = true
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
	p.logger.Info("Starting Jira poller",
		slog.String("label", p.pilotLabel),
		slog.String("project", p.config.ProjectKey),
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
			p.logger.Info("Jira poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
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

// recoverOrphanedIssues finds issues with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
// GH-1355: This handles restart/crash scenarios where issues were left orphaned.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	// Build JQL to find in-progress issues
	var parts []string
	parts = append(parts, fmt.Sprintf("labels = \"%s\"", LabelInProgress))
	if p.config.ProjectKey != "" {
		parts = append(parts, fmt.Sprintf("project = \"%s\"", p.config.ProjectKey))
	}
	parts = append(parts, "statusCategory != Done")
	jql := strings.Join(parts, " AND ")

	issues, err := p.client.SearchIssues(ctx, jql, 50)
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
		if err := p.client.RemoveLabel(ctx, issue.Key, LabelInProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.String("key", issue.Key),
				slog.Any("error", err),
			)
			continue
		}
		p.logger.Info("Recovered orphaned issue",
			slog.String("key", issue.Key),
			slog.String("summary", issue.Fields.Summary),
		)
	}
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

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(issue.Key)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Jira issue for parallel execution",
			slog.String("key", issue.Key),
			slog.String("summary", issue.Fields.Summary),
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
		return
	}

	// Remove in-progress label
	_ = p.client.RemoveLabel(ctx, issue.Key, LabelInProgress)

	// Add done label on success
	if result != nil && result.Success {
		_ = p.client.AddLabel(ctx, issue.Key, LabelDone)
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

	// GH-1357: Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkJiraIssueProcessed(key, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.String("issue", key), slog.Any("error", err))
		}
	}
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

	// GH-1357: Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.UnmarkJiraIssueProcessed(key); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.String("key", key),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.String("key", key))
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
