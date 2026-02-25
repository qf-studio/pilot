package plane

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

// Status labels for tracking work item progress.
const (
	LabelPilot      = "pilot"
	LabelInProgress = "pilot-in-progress"
	LabelDone       = "pilot-done"
	LabelFailed     = "pilot-failed"
)

// IssueResult is returned by the work item handler.
type IssueResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string // Head commit SHA of the PR (for autopilot wiring)
	BranchName string // Head branch name e.g. "pilot/PLANE-123"
	Error      error
}

// ProcessedStore persists which Plane work items have been processed across restarts.
// GH-1830: Plane uses string UUIDs for work item IDs.
type ProcessedStore interface {
	MarkPlaneIssueProcessed(issueID string, result string) error
	UnmarkPlaneIssueProcessed(issueID string) error
	IsPlaneIssueProcessed(issueID string) (bool, error)
	LoadPlaneProcessedIssues() (map[string]bool, error)
}

// Poller polls Plane.so for work items with the pilot label.
type Poller struct {
	client   *Client
	config   *Config
	interval time.Duration

	processed map[string]bool // Work item UUID → processed
	mu        sync.RWMutex

	onIssue     func(ctx context.Context, issue *WorkItem) (*IssueResult, error)
	onPRCreated func(prNumber int, prURL, issueID, headSHA, branchName string)
	logger      *slog.Logger

	// Label UUID cache (resolved on startup by name)
	pilotLabelID      string
	inProgressLabelID string
	doneLabelID       string
	failedLabelID     string

	// GH-1830: Persistent processed store (optional)
	processedStore ProcessedStore

	// GH-1832: State UUID cache (resolved on startup by group)
	// Maps project ID → state UUID for started/completed groups.
	startedStateIDs   map[string]string
	completedStateIDs map[string]string

	// GH-1830: Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination
}

// PollerOption configures a Poller.
type PollerOption func(*Poller)

// WithOnIssue sets the callback for new work items.
func WithOnIssue(fn func(ctx context.Context, issue *WorkItem) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithPollerLogger sets the logger for the poller.
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithProcessedStore sets the persistent store for processed work item tracking.
// GH-1830: On startup, processed items are loaded from the store to prevent re-processing after hot upgrade.
func WithProcessedStore(store ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
	}
}

// WithMaxConcurrent sets the maximum number of parallel work item executions.
// GH-1830: Ported parallel execution pattern from other adapters.
func WithMaxConcurrent(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.maxConcurrent = n
	}
}

// WithOnPRCreated sets the callback for when a PR is created for a work item.
func WithOnPRCreated(fn func(prNumber int, prURL, issueID, headSHA, branchName string)) PollerOption {
	return func(p *Poller) {
		p.onPRCreated = fn
	}
}

// NewPoller creates a new Plane work item poller.
func NewPoller(client *Client, config *Config, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:    client,
		config:    config,
		interval:  interval,
		processed: make(map[string]bool),
		logger:    logging.WithComponent("plane-poller"),
	}

	for _, opt := range opts {
		opt(p)
	}

	// GH-1830: Load processed items from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.LoadPlaneProcessedIssues()
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

	// GH-1830: Initialize parallel semaphore
	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	return p
}

// Start begins polling for work items.
func (p *Poller) Start(ctx context.Context) error {
	// Cache label UUIDs on startup
	if err := p.cacheLabelIDs(ctx); err != nil {
		return fmt.Errorf("failed to cache label IDs: %w", err)
	}

	// GH-1832: Cache state UUIDs for state transitions
	p.cacheStateIDs(ctx)

	p.logger.Info("Starting Plane poller",
		slog.String("workspace", p.config.WorkspaceSlug),
		slog.Int("projects", len(p.config.ProjectIDs)),
		slog.Duration("interval", p.interval),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	// GH-1830: Recover orphaned in-progress items from previous run
	p.recoverOrphanedIssues(ctx)

	// Initial check
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Plane poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Plane poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// cacheLabelIDs fetches and caches the UUIDs for pilot-related labels across all configured projects.
// Plane labels are per-project, so we resolve from the first project that has matching labels.
func (p *Poller) cacheLabelIDs(ctx context.Context) error {
	pilotLabelName := p.config.PilotLabel
	if pilotLabelName == "" {
		pilotLabelName = LabelPilot
	}

	for _, projectID := range p.config.ProjectIDs {
		labels, err := p.client.ListLabels(ctx, p.config.WorkspaceSlug, projectID)
		if err != nil {
			p.logger.Warn("Failed to list labels for project",
				slog.String("project_id", projectID),
				slog.Any("error", err),
			)
			continue
		}

		for _, label := range labels {
			switch {
			case strings.EqualFold(label.Name, pilotLabelName):
				p.pilotLabelID = label.ID
			case strings.EqualFold(label.Name, LabelInProgress):
				p.inProgressLabelID = label.ID
			case strings.EqualFold(label.Name, LabelDone):
				p.doneLabelID = label.ID
			case strings.EqualFold(label.Name, LabelFailed):
				p.failedLabelID = label.ID
			}
		}

		// If we found the pilot label, stop looking
		if p.pilotLabelID != "" {
			break
		}
	}

	if p.pilotLabelID == "" {
		return fmt.Errorf("pilot label %q not found in any configured project", pilotLabelName)
	}

	p.logger.Debug("Cached label IDs",
		slog.String("pilot", p.pilotLabelID),
		slog.String("in_progress", p.inProgressLabelID),
		slog.String("done", p.doneLabelID),
		slog.String("failed", p.failedLabelID),
	)

	return nil
}

// cacheStateIDs fetches and caches the UUIDs for started/completed state groups per project.
// GH-1832: Plane states are per-project; we cache them to avoid API calls on every transition.
func (p *Poller) cacheStateIDs(ctx context.Context) {
	p.startedStateIDs = make(map[string]string)
	p.completedStateIDs = make(map[string]string)

	for _, projectID := range p.config.ProjectIDs {
		states, err := p.client.ListStates(ctx, p.config.WorkspaceSlug, projectID)
		if err != nil {
			p.logger.Warn("Failed to list states for project",
				slog.String("project_id", projectID),
				slog.Any("error", err),
			)
			continue
		}

		for _, s := range states {
			switch s.Group {
			case StateGroupStarted:
				if p.startedStateIDs[projectID] == "" {
					p.startedStateIDs[projectID] = s.ID
				}
			case StateGroupCompleted:
				if p.completedStateIDs[projectID] == "" {
					p.completedStateIDs[projectID] = s.ID
				}
			}
		}
	}

	p.logger.Debug("Cached state IDs",
		slog.Int("projects_with_started", len(p.startedStateIDs)),
		slog.Int("projects_with_completed", len(p.completedStateIDs)),
	)
}

// recoverOrphanedIssues finds work items with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
// GH-1830: This handles restart/crash scenarios where items were left orphaned.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	if p.inProgressLabelID == "" {
		return
	}

	for _, projectID := range p.config.ProjectIDs {
		items, err := p.client.ListWorkItems(ctx, p.config.WorkspaceSlug, projectID, p.inProgressLabelID)
		if err != nil {
			p.logger.Warn("Failed to check for orphaned issues",
				slog.String("project_id", projectID),
				slog.Any("error", err),
			)
			continue
		}

		if len(items) == 0 {
			continue
		}

		p.logger.Info("Recovering orphaned in-progress issues",
			slog.String("project_id", projectID),
			slog.Int("count", len(items)),
		)

		for _, item := range items {
			if err := p.client.RemoveLabel(ctx, p.config.WorkspaceSlug, projectID, item.ID, p.inProgressLabelID); err != nil {
				p.logger.Warn("Failed to remove in-progress label from orphaned issue",
					slog.String("id", item.ID),
					slog.Any("error", err),
				)
				continue
			}
			p.logger.Info("Recovered orphaned issue",
				slog.String("id", item.ID),
				slog.String("name", item.Name),
			)
		}
	}
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	var allItems []WorkItem

	for _, projectID := range p.config.ProjectIDs {
		items, err := p.client.ListWorkItems(ctx, p.config.WorkspaceSlug, projectID, p.pilotLabelID)
		if err != nil {
			p.logger.Warn("Failed to fetch work items",
				slog.String("project_id", projectID),
				slog.Any("error", err),
			)
			continue
		}
		allItems = append(allItems, items...)
	}

	// Sort by creation date (oldest first)
	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].CreatedAt.Before(allItems[j].CreatedAt)
	})

	for _, item := range allItems {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[item.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if has status label (in-progress, done, or failed)
		if p.hasStatusLabel(&item) {
			// Only mark as processed if it has done label (allow retry of failed)
			if HasLabelID(&item, p.doneLabelID) {
				p.markProcessed(item.ID)
			}
			continue
		}

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(item.ID)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Plane work item for parallel execution",
			slog.String("id", item.ID),
			slog.String("name", item.Name),
			slog.String("project", item.ProjectID),
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

		go p.processIssueAsync(ctx, item)
	}
}

// processIssueAsync handles a single work item in a goroutine.
// GH-1830: Extracted to enable parallel execution.
func (p *Poller) processIssueAsync(ctx context.Context, item WorkItem) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }() // release slot

	if p.onIssue == nil {
		return
	}

	// Add in-progress label
	if p.inProgressLabelID != "" {
		_ = p.client.AddLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, p.inProgressLabelID)
	}

	// GH-1832: Transition to started state on dispatch
	if stateID := p.startedStateIDs[item.ProjectID]; stateID != "" {
		if err := p.client.UpdateIssueState(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, stateID); err != nil {
			p.logger.Warn("Failed to transition work item to started state",
				slog.String("id", item.ID),
				slog.Any("error", err),
			)
		}
	}

	result, err := p.onIssue(ctx, &item)
	if err != nil {
		p.logger.Error("Failed to process work item",
			slog.String("id", item.ID),
			slog.Any("error", err),
		)
		// Remove in-progress label, add failed label
		if p.inProgressLabelID != "" {
			_ = p.client.RemoveLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, p.inProgressLabelID)
		}
		if p.failedLabelID != "" {
			_ = p.client.AddLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, p.failedLabelID)
		}
		// GH-1832: On failure, leave state as-is (user decides)
		return
	}

	// Remove in-progress label
	if p.inProgressLabelID != "" {
		_ = p.client.RemoveLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, p.inProgressLabelID)
	}

	// Add done label on success
	if result != nil && result.Success && p.doneLabelID != "" {
		_ = p.client.AddLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, p.doneLabelID)
	}

	// GH-1832: Transition to completed state on success
	if result != nil && result.Success {
		if stateID := p.completedStateIDs[item.ProjectID]; stateID != "" {
			if err := p.client.UpdateIssueState(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, stateID); err != nil {
				p.logger.Warn("Failed to transition work item to completed state",
					slog.String("id", item.ID),
					slog.Any("error", err),
				)
			}
		}
	}

	// GH-1832: Post PR URL as comment with execution metrics and dedup
	if result != nil && result.Success && result.PRNumber > 0 {
		commentHTML := fmt.Sprintf(
			`<p>✅ PR created: <a href="%s">#%d</a></p>`,
			result.PRURL, result.PRNumber,
		)
		externalID := fmt.Sprintf("pilot-pr-%d-%s", result.PRNumber, item.ID)
		if err := p.client.AddCommentWithTracking(
			ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID,
			commentHTML, "pilot", externalID,
		); err != nil {
			p.logger.Warn("Failed to post PR comment on work item",
				slog.String("id", item.ID),
				slog.Int("pr_number", result.PRNumber),
				slog.Any("error", err),
			)
		}
	}

	// Fire OnPRCreated callback
	if result != nil && result.PRNumber > 0 && p.onPRCreated != nil {
		p.onPRCreated(result.PRNumber, result.PRURL, item.ID, result.HeadSHA, result.BranchName)
	}
}

// hasStatusLabel checks if a work item has any status label UUID.
func (p *Poller) hasStatusLabel(item *WorkItem) bool {
	if p.inProgressLabelID != "" && HasLabelID(item, p.inProgressLabelID) {
		return true
	}
	if p.doneLabelID != "" && HasLabelID(item, p.doneLabelID) {
		return true
	}
	if p.failedLabelID != "" && HasLabelID(item, p.failedLabelID) {
		return true
	}
	return false
}

func (p *Poller) markProcessed(id string) {
	p.mu.Lock()
	p.processed[id] = true
	p.mu.Unlock()

	// GH-1830: Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkPlaneIssueProcessed(id, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.String("id", id), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if a work item has been processed.
func (p *Poller) IsProcessed(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[id]
}

// ProcessedCount returns the number of processed work items.
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed work items map.
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[string]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a specific work item from the processed map (for retry).
// GH-1830: Used when pilot-failed label is removed to allow retry.
func (p *Poller) ClearProcessed(id string) {
	p.mu.Lock()
	delete(p.processed, id)
	p.mu.Unlock()

	// Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.UnmarkPlaneIssueProcessed(id); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.String("id", id),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.String("id", id))
}

// Drain stops accepting new work items and waits for active executions to finish.
// GH-1830: Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new work items will be accepted")
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
	p.logger.Info("Poller drained — all active tasks completed")
}

// WaitForActive waits for all active parallel goroutines to finish.
// GH-1830: Used in tests to synchronize after checkForNewIssues.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}
