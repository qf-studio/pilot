package gitlab

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ExecutionMode determines how issues are processed
type ExecutionMode string

const (
	// ExecutionModeSequential processes one issue at a time, waiting for MR merge
	ExecutionModeSequential ExecutionMode = "sequential"
	// ExecutionModeParallel processes issues concurrently (legacy behavior)
	ExecutionModeParallel ExecutionMode = "parallel"
)

// IssueResult is returned by the issue handler with MR information
type IssueResult struct {
	Success    bool
	MRNumber   int    // MR IID if created
	MRURL      string // MR URL if created
	HeadSHA    string // Head commit SHA of the MR
	BranchName string // Head branch name (e.g. "pilot/GH-123")
	Error      error
}

// ProcessedStore persists which GitLab issues have been processed across restarts.
// GH-1358: GitLab uses integer IDs like GitHub.
type ProcessedStore interface {
	MarkGitLabIssueProcessed(issueNumber int, result string) error
	UnmarkGitLabIssueProcessed(issueNumber int) error
	IsGitLabIssueProcessed(issueNumber int) (bool, error)
	LoadGitLabProcessedIssues() (map[int]bool, error)
}

// Poller polls GitLab for issues with a specific label
type Poller struct {
	client    *Client
	label     string
	interval  time.Duration
	processed map[int]bool
	mu        sync.RWMutex
	onIssue   func(ctx context.Context, issue *Issue) error
	// onIssueWithResult is called for sequential mode, returns MR info
	onIssueWithResult func(ctx context.Context, issue *Issue) (*IssueResult, error)
	// OnMRCreated is called when an MR is created after issue processing
	// Parameters: mrIID, mrURL, issueIID, headSHA, branchName
	OnMRCreated func(mrIID int, mrURL string, issueIID int, headSHA string, branchName string)
	logger      *slog.Logger

	// Sequential mode configuration
	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	mrTimeout      time.Duration
	mrPollInterval time.Duration

	// GH-1358: Persistent processed store (optional)
	processedStore ProcessedStore

	// GH-1358: Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithPollerLogger sets the logger for the poller
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithOnIssue sets the callback for new issues (parallel mode)
func WithOnIssue(fn func(ctx context.Context, issue *Issue) error) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithOnIssueWithResult sets the callback for new issues that returns MR info (sequential mode)
func WithOnIssueWithResult(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssueWithResult = fn
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
		p.mrPollInterval = pollInterval
		p.mrTimeout = timeout
	}
}

// WithOnMRCreated sets the callback for MR creation events
func WithOnMRCreated(fn func(mrIID int, mrURL string, issueIID int, headSHA string, branchName string)) PollerOption {
	return func(p *Poller) {
		p.OnMRCreated = fn
	}
}

// WithProcessedStore sets the persistent store for processed issue tracking.
// GH-1358: On startup, processed issues are loaded from the store to prevent re-processing after hot upgrade.
func WithProcessedStore(store ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
	}
}

// WithMaxConcurrent sets the maximum number of parallel issue executions.
// GH-1358: Ported from GitHub poller parallel execution pattern.
func WithMaxConcurrent(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.maxConcurrent = n
	}
}

// NewPoller creates a new GitLab issue poller
func NewPoller(client *Client, label string, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:         client,
		label:          label,
		interval:       interval,
		processed:      make(map[int]bool),
		logger:         logging.WithComponent("gitlab-poller"),
		executionMode:  ExecutionModeParallel, // Default for backward compatibility
		waitForMerge:   true,
		mrPollInterval: 30 * time.Second,
		mrTimeout:      1 * time.Hour,
	}

	for _, opt := range opts {
		opt(p)
	}

	// GH-1358: Load processed issues from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.LoadGitLabProcessedIssues()
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

	// GH-1358: Initialize parallel semaphore
	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	// Create merge waiter if in sequential mode
	if p.executionMode == ExecutionModeSequential && p.waitForMerge {
		p.mergeWaiter = NewMergeWaiter(client, &MergeWaiterConfig{
			PollInterval: p.mrPollInterval,
			Timeout:      p.mrTimeout,
		})
	}

	return p
}

// Start begins polling for issues
func (p *Poller) Start(ctx context.Context) {
	p.logger.Info("Starting GitLab poller",
		slog.String("label", p.label),
		slog.Duration("interval", p.interval),
		slog.String("mode", string(p.executionMode)),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		p.startParallel(ctx)
	}
}

// startParallel runs the parallel execution mode with goroutine dispatch
func (p *Poller) startParallel(ctx context.Context) {
	// Do an initial check immediately
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("GitLab poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("GitLab poller stopped")
			return
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// startSequential runs the sequential execution mode
// Processes one issue at a time, waits for MR merge before next
func (p *Poller) startSequential(ctx context.Context) {
	p.logger.Info("Running in sequential mode",
		slog.Bool("wait_for_merge", p.waitForMerge),
		slog.Duration("mr_timeout", p.mrTimeout),
	)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Sequential poller stopped")
			return
		default:
		}

		// Find oldest unprocessed issue
		issue, err := p.findOldestUnprocessedIssue(ctx)
		if err != nil {
			p.logger.Warn("Failed to find issues", slog.Any("error", err))
			time.Sleep(p.interval)
			continue
		}

		if issue == nil {
			// No issues to process, wait before checking again
			p.logger.Debug("No unprocessed issues found, waiting...")
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.interval):
				continue
			}
		}

		// Process the issue
		p.logger.Info("Processing issue in sequential mode",
			slog.Int("iid", issue.IID),
			slog.String("title", issue.Title),
		)

		result, err := p.processIssueSequential(ctx, issue)
		if err != nil {
			p.logger.Error("Failed to process issue",
				slog.Int("iid", issue.IID),
				slog.Any("error", err),
			)
			// Mark as processed to avoid infinite retry loop
			// The issue will have pilot-failed label
			p.markProcessed(issue.IID)
			continue
		}

		// Notify autopilot controller of new MR (if callback registered)
		if result != nil && result.MRNumber > 0 && p.OnMRCreated != nil {
			p.logger.Info("Notifying autopilot of MR creation",
				slog.Int("mr_iid", result.MRNumber),
				slog.Int("issue_iid", issue.IID),
				slog.String("branch", result.BranchName),
			)
			p.OnMRCreated(result.MRNumber, result.MRURL, issue.IID, result.HeadSHA, result.BranchName)
		}

		// If we created an MR and should wait for merge
		if result != nil && result.MRNumber > 0 && p.waitForMerge && p.mergeWaiter != nil {
			p.logger.Info("Waiting for MR merge before next issue",
				slog.Int("mr_iid", result.MRNumber),
				slog.String("mr_url", result.MRURL),
			)

			mergeResult, err := p.mergeWaiter.WaitWithCallback(ctx, result.MRNumber, func(r *MergeWaitResult) {
				p.logger.Debug("MR status check",
					slog.Int("mr_iid", r.MRNumber),
					slog.String("status", r.Message),
				)
			})

			if err != nil {
				p.logger.Warn("Error waiting for MR merge, pausing sequential processing",
					slog.Int("mr_iid", result.MRNumber),
					slog.Any("error", err),
				)
				// DON'T mark as processed - leave for retry after fix
				time.Sleep(5 * time.Minute)
				continue
			}

			p.logger.Info("MR merge wait completed",
				slog.Int("mr_iid", result.MRNumber),
				slog.Bool("merged", mergeResult.Merged),
				slog.Bool("closed", mergeResult.Closed),
				slog.Bool("has_conflicts", mergeResult.HasConflicts),
				slog.Bool("timed_out", mergeResult.TimedOut),
			)

			// Check if MR has conflicts - stop processing
			if mergeResult.HasConflicts {
				p.logger.Warn("MR has conflicts, pausing sequential processing",
					slog.Int("mr_iid", result.MRNumber),
					slog.String("mr_url", result.MRURL),
				)
				// DON'T mark as processed - needs manual resolution or rebase
				time.Sleep(5 * time.Minute)
				continue
			}

			// Check if MR timed out
			if mergeResult.TimedOut {
				p.logger.Warn("MR merge timed out, pausing sequential processing",
					slog.Int("mr_iid", result.MRNumber),
					slog.String("mr_url", result.MRURL),
				)
				// DON'T mark as processed - needs investigation
				time.Sleep(5 * time.Minute)
				continue
			}

			// Only mark as processed if actually merged
			if mergeResult.Merged {
				p.markProcessed(issue.IID)
				continue
			}

			// MR was closed without merge
			if mergeResult.Closed {
				p.logger.Info("MR was closed without merge",
					slog.Int("mr_iid", result.MRNumber),
				)
				// DON'T mark as processed - issue may need re-execution
				continue
			}
		}

		// Direct commit case: no MR to wait for, proceed to next issue
		if result != nil && result.Success && result.MRNumber == 0 {
			p.logger.Info("Direct commit completed, proceeding to next issue",
				slog.Int("issue_iid", issue.IID),
				slog.String("commit_sha", result.HeadSHA),
			)
			p.markProcessed(issue.IID)
			continue
		}

		// MR was created but we're not waiting for merge, or no MR was created
		p.markProcessed(issue.IID)
	}
}

// findOldestUnprocessedIssue finds the oldest issue with the pilot label
// that hasn't been processed yet
func (p *Poller) findOldestUnprocessedIssue(ctx context.Context) (*Issue, error) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		Labels:  []string{p.label},
		State:   StateOpened,
		Sort:    "asc", // Oldest first
		OrderBy: "created_at",
	})
	if err != nil {
		return nil, err
	}

	// Filter out already processed and in-progress issues
	var candidates []*Issue
	for _, issue := range issues {
		p.mu.RLock()
		processed := p.processed[issue.IID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) {
			continue
		}

		candidates = append(candidates, issue)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by creation date (oldest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	return candidates[0], nil
}

// processIssueSequential processes a single issue and returns MR info
func (p *Poller) processIssueSequential(ctx context.Context, issue *Issue) (*IssueResult, error) {
	// Use the new callback if available
	if p.onIssueWithResult != nil {
		return p.onIssueWithResult(ctx, issue)
	}

	// Fall back to legacy callback
	if p.onIssue != nil {
		err := p.onIssue(ctx, issue)
		if err != nil {
			return &IssueResult{Success: false, Error: err}, err
		}
		return &IssueResult{Success: true}, nil
	}

	return nil, fmt.Errorf("no issue handler configured")
}

// checkForNewIssues fetches issues and dispatches them for parallel execution
func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		Labels:  []string{p.label},
		State:   StateOpened,
		Sort:    "asc", // Oldest first
		OrderBy: "created_at",
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
		processed := p.processed[issue.IID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if has in-progress, done, or failed label
		if p.hasStatusLabel(issue) {
			p.markProcessed(issue.IID)
			continue
		}

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(issue.IID)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching GitLab issue for parallel execution",
			slog.Int("iid", issue.IID),
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
// GH-1358: Extracted to enable parallel execution.
func (p *Poller) processIssueAsync(ctx context.Context, issue *Issue) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }() // release slot

	if p.onIssue == nil {
		return
	}

	// Add in-progress label
	if err := p.client.AddIssueLabels(ctx, issue.IID, []string{LabelInProgress}); err != nil {
		p.logger.Warn("Failed to add in-progress label",
			slog.Int("iid", issue.IID),
			slog.Any("error", err),
		)
	}

	err := p.onIssue(ctx, issue)
	if err != nil {
		p.logger.Error("Failed to process issue",
			slog.Int("iid", issue.IID),
			slog.Any("error", err),
		)
		// Remove in-progress label, add failed label
		_ = p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress)
		_ = p.client.AddIssueLabels(ctx, issue.IID, []string{LabelFailed})
		return
	}

	// Remove in-progress label
	_ = p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress)

	// Add done label on success
	_ = p.client.AddIssueLabels(ctx, issue.IID, []string{LabelDone})
}

func (p *Poller) hasStatusLabel(issue *Issue) bool {
	return HasLabel(issue, LabelInProgress) ||
		HasLabel(issue, LabelDone) ||
		HasLabel(issue, LabelFailed)
}

// markProcessed marks an issue as processed
func (p *Poller) markProcessed(iid int) {
	p.mu.Lock()
	p.processed[iid] = true
	p.mu.Unlock()

	// GH-1358: Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkGitLabIssueProcessed(iid, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.Int("iid", iid), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if an issue has been processed
func (p *Poller) IsProcessed(iid int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[iid]
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

// ClearProcessed removes a single issue from the processed map.
// GH-1358: Used when pilot-failed label is removed to allow the issue to be retried.
func (p *Poller) ClearProcessed(iid int) {
	p.mu.Lock()
	delete(p.processed, iid)
	p.mu.Unlock()

	// Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.UnmarkGitLabIssueProcessed(iid); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.Int("iid", iid),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.Int("iid", iid))
}

// Drain stops accepting new issues and waits for active executions to finish.
// GH-1358: Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new issues will be accepted")
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
	p.logger.Info("Poller drained — all active tasks completed")
}

// WaitForActive waits for all active parallel goroutines to finish.
// GH-1358: Used in tests to synchronize after checkForNewIssues.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}

// ExtractMRNumber extracts MR IID from a GitLab MR URL
// e.g., "https://gitlab.com/namespace/project/-/merge_requests/123" -> 123
func ExtractMRNumber(mrURL string) (int, error) {
	if mrURL == "" {
		return 0, fmt.Errorf("empty MR URL")
	}

	// Match pattern: /-/merge_requests/123 or /merge_requests/123
	re := regexp.MustCompile(`/(?:-/)?merge_requests/(\d+)`)
	matches := re.FindStringSubmatch(mrURL)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not extract MR number from URL: %s", mrURL)
	}

	var num int
	if _, err := fmt.Sscanf(matches[1], "%d", &num); err != nil {
		return 0, fmt.Errorf("invalid MR number in URL: %s", mrURL)
	}

	return num, nil
}
