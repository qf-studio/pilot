package azuredevops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ExecutionMode determines how work items are processed
type ExecutionMode string

const (
	// ExecutionModeSequential processes one work item at a time, waiting for PR merge
	ExecutionModeSequential ExecutionMode = "sequential"
	// ExecutionModeParallel processes work items concurrently (legacy behavior)
	ExecutionModeParallel ExecutionMode = "parallel"
)

// WorkItemResult is returned by the work item handler with PR information
type WorkItemResult struct {
	Success  bool
	PRNumber int    // PR ID if created
	PRURL    string // PR URL if created
	HeadSHA  string // Head commit SHA of the PR
	Error    error
}

// Poller polls Azure DevOps for work items with a specific tag
type Poller struct {
	client     *Client
	tag        string
	interval   time.Duration
	processed  map[int]bool
	mu         sync.RWMutex
	onWorkItem func(ctx context.Context, wi *WorkItem) error
	// onWorkItemWithResult is called for sequential mode, returns PR info
	onWorkItemWithResult func(ctx context.Context, wi *WorkItem) (*WorkItemResult, error)
	// OnPRCreated is called when a PR is created after work item processing
	// Parameters: prID, prURL, workItemID, headSHA
	OnPRCreated func(prID int, prURL string, workItemID int, headSHA string)
	logger      *slog.Logger

	// Work item filtering
	workItemTypes []string

	// Sequential mode configuration
	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	prTimeout      time.Duration
	prPollInterval time.Duration
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithPollerLogger sets the logger for the poller
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithOnWorkItem sets the callback for new work items (parallel mode)
func WithOnWorkItem(fn func(ctx context.Context, wi *WorkItem) error) PollerOption {
	return func(p *Poller) {
		p.onWorkItem = fn
	}
}

// WithOnWorkItemWithResult sets the callback for new work items that returns PR info (sequential mode)
func WithOnWorkItemWithResult(fn func(ctx context.Context, wi *WorkItem) (*WorkItemResult, error)) PollerOption {
	return func(p *Poller) {
		p.onWorkItemWithResult = fn
	}
}

// WithExecutionMode sets the execution mode (sequential or parallel)
func WithExecutionMode(mode ExecutionMode) PollerOption {
	return func(p *Poller) {
		p.executionMode = mode
	}
}

// WithSequentialConfig configures sequential execution settings
func WithSequentialConfig(waitForMerge bool, pollInterval, timeout time.Duration) PollerOption {
	return func(p *Poller) {
		p.waitForMerge = waitForMerge
		p.prPollInterval = pollInterval
		p.prTimeout = timeout
	}
}

// WithOnPRCreated sets the callback for PR creation events
func WithOnPRCreated(fn func(prID int, prURL string, workItemID int, headSHA string)) PollerOption {
	return func(p *Poller) {
		p.OnPRCreated = fn
	}
}

// WithWorkItemTypes sets the work item types to filter
func WithWorkItemTypes(types []string) PollerOption {
	return func(p *Poller) {
		p.workItemTypes = types
	}
}

// NewPoller creates a new Azure DevOps work item poller
func NewPoller(client *Client, tag string, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:         client,
		tag:            tag,
		interval:       interval,
		processed:      make(map[int]bool),
		logger:         logging.WithComponent("azuredevops-poller"),
		executionMode:  ExecutionModeParallel, // Default for backward compatibility
		waitForMerge:   true,
		prPollInterval: 30 * time.Second,
		prTimeout:      1 * time.Hour,
		workItemTypes:  []string{"Bug", "Task", "User Story"},
	}

	for _, opt := range opts {
		opt(p)
	}

	// Create merge waiter if in sequential mode
	if p.executionMode == ExecutionModeSequential && p.waitForMerge {
		p.mergeWaiter = NewMergeWaiter(client, &MergeWaiterConfig{
			PollInterval: p.prPollInterval,
			Timeout:      p.prTimeout,
		})
	}

	return p
}

// Start begins polling for work items
func (p *Poller) Start(ctx context.Context) {
	p.logger.Info("Starting Azure DevOps poller",
		slog.String("tag", p.tag),
		slog.Duration("interval", p.interval),
		slog.String("mode", string(p.executionMode)),
	)

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		p.startParallel(ctx)
	}
}

// startParallel runs the legacy parallel execution mode
func (p *Poller) startParallel(ctx context.Context) {
	// Do an initial check immediately
	p.checkForNewWorkItems(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Azure DevOps poller stopped")
			return
		case <-ticker.C:
			p.checkForNewWorkItems(ctx)
		}
	}
}

// startSequential runs the sequential execution mode
// Processes one work item at a time, waits for PR merge before next
func (p *Poller) startSequential(ctx context.Context) {
	p.logger.Info("Running in sequential mode",
		slog.Bool("wait_for_merge", p.waitForMerge),
		slog.Duration("pr_timeout", p.prTimeout),
	)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Sequential poller stopped")
			return
		default:
		}

		// Find oldest unprocessed work item
		wi, err := p.findOldestUnprocessedWorkItem(ctx)
		if err != nil {
			p.logger.Warn("Failed to find work items", slog.Any("error", err))
			time.Sleep(p.interval)
			continue
		}

		if wi == nil {
			// No work items to process, wait before checking again
			p.logger.Debug("No unprocessed work items found, waiting...")
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.interval):
				continue
			}
		}

		// Process the work item
		p.logger.Info("Processing work item in sequential mode",
			slog.Int("id", wi.ID),
			slog.String("title", wi.GetTitle()),
		)

		result, err := p.processWorkItemSequential(ctx, wi)
		if err != nil {
			p.logger.Error("Failed to process work item",
				slog.Int("id", wi.ID),
				slog.Any("error", err),
			)
			// Mark as processed to avoid infinite retry loop
			// The work item will have pilot-failed tag
			p.markProcessed(wi.ID)
			continue
		}

		// Notify autopilot controller of new PR (if callback registered)
		if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
			p.logger.Info("Notifying autopilot of PR creation",
				slog.Int("pr_id", result.PRNumber),
				slog.Int("work_item_id", wi.ID),
			)
			p.OnPRCreated(result.PRNumber, result.PRURL, wi.ID, result.HeadSHA)
		}

		// If we created a PR and should wait for merge
		if result != nil && result.PRNumber > 0 && p.waitForMerge && p.mergeWaiter != nil {
			p.logger.Info("Waiting for PR merge before next work item",
				slog.Int("pr_id", result.PRNumber),
				slog.String("pr_url", result.PRURL),
			)

			mergeResult, err := p.mergeWaiter.WaitWithCallback(ctx, result.PRNumber, func(r *MergeWaitResult) {
				p.logger.Debug("PR status check",
					slog.Int("pr_id", r.PRNumber),
					slog.String("status", r.Message),
				)
			})

			if err != nil {
				p.logger.Warn("Error waiting for PR merge, pausing sequential processing",
					slog.Int("pr_id", result.PRNumber),
					slog.Any("error", err),
				)
				// DON'T mark as processed - leave for retry after fix
				time.Sleep(5 * time.Minute)
				continue
			}

			p.logger.Info("PR merge wait completed",
				slog.Int("pr_id", result.PRNumber),
				slog.Bool("merged", mergeResult.Merged),
				slog.Bool("abandoned", mergeResult.Abandoned),
				slog.Bool("has_conflicts", mergeResult.HasConflicts),
				slog.Bool("timed_out", mergeResult.TimedOut),
			)

			// Check if PR has conflicts - stop processing
			if mergeResult.HasConflicts {
				p.logger.Warn("PR has conflicts, pausing sequential processing",
					slog.Int("pr_id", result.PRNumber),
					slog.String("pr_url", result.PRURL),
				)
				// DON'T mark as processed - needs manual resolution or rebase
				time.Sleep(5 * time.Minute)
				continue
			}

			// Check if PR timed out
			if mergeResult.TimedOut {
				p.logger.Warn("PR merge timed out, pausing sequential processing",
					slog.Int("pr_id", result.PRNumber),
					slog.String("pr_url", result.PRURL),
				)
				// DON'T mark as processed - needs investigation
				time.Sleep(5 * time.Minute)
				continue
			}

			// Only mark as processed if actually merged
			if mergeResult.Merged {
				p.markProcessed(wi.ID)
				continue
			}

			// PR was abandoned without merge
			if mergeResult.Abandoned {
				p.logger.Info("PR was abandoned without merge",
					slog.Int("pr_id", result.PRNumber),
				)
				// DON'T mark as processed - work item may need re-execution
				continue
			}
		}

		// Direct commit case: no PR to wait for, proceed to next work item
		if result != nil && result.Success && result.PRNumber == 0 {
			p.logger.Info("Direct commit completed, proceeding to next work item",
				slog.Int("work_item_id", wi.ID),
				slog.String("commit_sha", result.HeadSHA),
			)
			p.markProcessed(wi.ID)
			continue
		}

		// PR was created but we're not waiting for merge, or no PR was created
		p.markProcessed(wi.ID)
	}
}

// findOldestUnprocessedWorkItem finds the oldest work item with the pilot tag
// that hasn't been processed yet
func (p *Poller) findOldestUnprocessedWorkItem(ctx context.Context) (*WorkItem, error) {
	workItems, err := p.client.ListWorkItems(ctx, &ListWorkItemsOptions{
		Tags:          []string{p.tag},
		States:        []string{StateNew, StateActive},
		WorkItemTypes: p.workItemTypes,
	})
	if err != nil {
		return nil, err
	}

	// Filter out already processed and in-progress work items
	var candidates []*WorkItem
	for _, wi := range workItems {
		p.mu.RLock()
		processed := p.processed[wi.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if HasTag(wi, TagInProgress) || HasTag(wi, TagDone) {
			continue
		}

		candidates = append(candidates, wi)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by creation date (oldest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].GetCreatedDate().Before(candidates[j].GetCreatedDate())
	})

	return candidates[0], nil
}

// processWorkItemSequential processes a single work item and returns PR info
func (p *Poller) processWorkItemSequential(ctx context.Context, wi *WorkItem) (*WorkItemResult, error) {
	// Use the new callback if available
	if p.onWorkItemWithResult != nil {
		return p.onWorkItemWithResult(ctx, wi)
	}

	// Fall back to legacy callback
	if p.onWorkItem != nil {
		err := p.onWorkItem(ctx, wi)
		if err != nil {
			return &WorkItemResult{Success: false, Error: err}, err
		}
		return &WorkItemResult{Success: true}, nil
	}

	return nil, fmt.Errorf("no work item handler configured")
}

// checkForNewWorkItems fetches work items and processes new ones (parallel mode)
func (p *Poller) checkForNewWorkItems(ctx context.Context) {
	workItems, err := p.client.ListWorkItems(ctx, &ListWorkItemsOptions{
		Tags:          []string{p.tag},
		States:        []string{StateNew, StateActive},
		WorkItemTypes: p.workItemTypes,
	})
	if err != nil {
		p.logger.Warn("Failed to fetch work items", slog.Any("error", err))
		return
	}

	for _, wi := range workItems {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[wi.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if already in progress or done
		if HasTag(wi, TagInProgress) || HasTag(wi, TagDone) {
			p.markProcessed(wi.ID)
			continue
		}

		// Process the work item
		p.logger.Info("Found new work item",
			slog.Int("id", wi.ID),
			slog.String("title", wi.GetTitle()),
		)

		if p.onWorkItem != nil {
			if err := p.onWorkItem(ctx, wi); err != nil {
				p.logger.Error("Failed to process work item",
					slog.Int("id", wi.ID),
					slog.Any("error", err),
				)
				// Don't mark as processed so we can retry
				continue
			}
		}

		p.markProcessed(wi.ID)
	}
}

// markProcessed marks a work item as processed
func (p *Poller) markProcessed(id int) {
	p.mu.Lock()
	p.processed[id] = true
	p.mu.Unlock()
}

// IsProcessed checks if a work item has been processed
func (p *Poller) IsProcessed(id int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[id]
}

// ProcessedCount returns the number of processed work items
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed work items map
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[int]bool)
	p.mu.Unlock()
}

// ExtractPRNumber extracts PR ID from an Azure DevOps PR URL
// e.g., "https://dev.azure.com/org/project/_git/repo/pullrequest/123" -> 123
func ExtractPRNumber(prURL string) (int, error) {
	if prURL == "" {
		return 0, fmt.Errorf("empty PR URL")
	}

	// Match pattern: /pullrequest/123
	re := regexp.MustCompile(`/pullrequest/(\d+)`)
	matches := re.FindStringSubmatch(prURL)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not extract PR number from URL: %s", prURL)
	}

	var num int
	if _, err := fmt.Sscanf(matches[1], "%d", &num); err != nil {
		return 0, fmt.Errorf("invalid PR number in URL: %s", prURL)
	}

	return num, nil
}
