package github

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/logging"
)

// ExecutionMode determines how issues are processed
type ExecutionMode string

const (
	// ExecutionModeSequential processes one issue at a time, waiting for PR merge
	ExecutionModeSequential ExecutionMode = "sequential"
	// ExecutionModeParallel processes issues concurrently (legacy behavior)
	ExecutionModeParallel ExecutionMode = "parallel"
	// ExecutionModeAuto uses parallel dispatch with scope-overlap guard:
	// non-overlapping issues run concurrently; overlapping groups run oldest-first.
	ExecutionModeAuto ExecutionMode = "auto"
)

// ProcessedStore persists which issues have been processed across restarts.
// Implemented by autopilot.StateStore to avoid circular imports.
type ProcessedStore interface {
	MarkIssueProcessed(issueNumber int, result string) error
	UnmarkIssueProcessed(issueNumber int) error
	IsIssueProcessed(issueNumber int) (bool, error)
	LoadProcessedIssues() (map[int]bool, error)
}

// IssueResult is returned by the issue handler with PR information
type IssueResult struct {
	Success    bool
	PRNumber   int    // PR number if created
	PRURL      string // PR URL if created
	HeadSHA    string // Head commit SHA of the PR
	BranchName string // Head branch name (e.g. "pilot/GH-123")
	Error      error
}

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
	// onIssueWithResult is called for sequential mode, returns PR info
	onIssueWithResult func(ctx context.Context, issue *Issue) (*IssueResult, error)
	// OnPRCreated is called when a PR is created after issue processing
	// Parameters: prNumber, prURL, issueNumber, headSHA, branchName
	OnPRCreated func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string)
	logger      *slog.Logger

	// Sequential mode configuration
	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	prTimeout      time.Duration
	prPollInterval time.Duration

	// Rate limit retry scheduler
	scheduler *executor.Scheduler

	// Parallel mode configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination

	// Persistent processed store (optional)
	processedStore ProcessedStore
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

// WithOnIssueWithResult sets the callback for new issues that returns PR info (sequential mode)
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
		p.prPollInterval = pollInterval
		p.prTimeout = timeout
	}
}

// WithOnPRCreated sets the callback for PR creation events
// The callback is invoked after a PR is successfully created for an issue
func WithOnPRCreated(fn func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string)) PollerOption {
	return func(p *Poller) {
		p.OnPRCreated = fn
	}
}

// WithScheduler sets the rate limit retry scheduler
func WithScheduler(s *executor.Scheduler) PollerOption {
	return func(p *Poller) {
		p.scheduler = s
	}
}

// WithProcessedStore sets the persistent store for processed issue tracking.
// On startup, processed issues are loaded from the store to prevent re-processing.
func WithProcessedStore(store ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
	}
}

// WithMaxConcurrent sets the maximum number of parallel issue executions
func WithMaxConcurrent(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.maxConcurrent = n
	}
}

// NewPoller creates a new GitHub issue poller
func NewPoller(client *Client, repo string, label string, interval time.Duration, opts ...PollerOption) (*Poller, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format, expected owner/repo: %s", repo)
	}

	p := &Poller{
		client:         client,
		owner:          parts[0],
		repo:           parts[1],
		label:          label,
		interval:       interval,
		processed:      make(map[int]bool),
		logger:         logging.WithComponent("github-poller"),
		executionMode:  ExecutionModeAuto, // Default matches config.DefaultExecutionConfig()
		waitForMerge:   true,
		prPollInterval: 30 * time.Second,
		prTimeout:      1 * time.Hour,
	}

	for _, opt := range opts {
		opt(p)
	}

	// Create merge waiter if in sequential mode
	if p.executionMode == ExecutionModeSequential && p.waitForMerge {
		p.mergeWaiter = NewMergeWaiter(client, p.owner, p.repo, &MergeWaiterConfig{
			PollInterval: p.prPollInterval,
			Timeout:      p.prTimeout,
		})
	}

	// Load processed issues from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.LoadProcessedIssues()
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for num := range loaded {
				p.processed[num] = true
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed issues from store", slog.Int("count", len(loaded)))
		}
	}

	// Initialize parallel semaphore
	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	return p, nil
}

// Start begins polling for issues
func (p *Poller) Start(ctx context.Context) {
	p.logger.Info("Starting GitHub poller",
		slog.String("repo", p.owner+"/"+p.repo),
		slog.String("label", p.label),
		slog.Duration("interval", p.interval),
		slog.String("mode", string(p.executionMode)),
	)

	// GH-1355: Recover orphaned in-progress issues from previous run before starting poll loop
	p.recoverOrphanedIssues(ctx)

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		// Both parallel and auto modes use startParallel; auto additionally
		// applies the scope-overlap guard (groupByOverlappingScope) which is
		// already built into checkForNewIssues.
		p.startParallel(ctx)
	}
}

// recoverOrphanedIssues finds issues with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
// GH-1355: This handles restart/crash scenarios where issues were left orphaned.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label, LabelInProgress},
		State:  StateOpen,
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
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelInProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
			continue
		}
		p.logger.Info("Recovered orphaned issue",
			slog.Int("number", issue.Number),
			slog.String("title", issue.Title),
		)
	}
}

// startParallel runs concurrent issue execution with a semaphore limiter.
// Used by both "parallel" and "auto" modes. In "auto" mode, checkForNewIssues
// applies the scope-overlap guard so that overlapping issues are held back.
func (p *Poller) startParallel(ctx context.Context) {
	p.logger.Info("Running in parallel mode",
		slog.String("mode", string(p.executionMode)),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	// Do an initial check immediately
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Parallel poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Parallel poller stopped")
			return
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// startSequential runs the sequential execution mode
// Processes one issue at a time, waits for PR merge before next
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
			slog.Int("number", issue.Number),
			slog.String("title", issue.Title),
		)

		result, err := p.processIssueSequential(ctx, issue)
		if err != nil {
			// Check if this is a rate limit error that can be retried
			if executor.IsRateLimitError(err.Error()) {
				rlInfo, ok := executor.ParseRateLimitError(err.Error())
				if ok && p.scheduler != nil {
					task := &executor.Task{
						ID:          fmt.Sprintf("GH-%d", issue.Number),
						Title:       issue.Title,
						Description: issue.Body,
						ProjectPath: "", // Will be set by retry callback
					}
					p.scheduler.QueueTask(task, rlInfo)
					p.logger.Info("Task queued for retry after rate limit",
						slog.Int("issue", issue.Number),
						slog.Time("retry_at", rlInfo.ResetTime.Add(5*time.Minute)),
						slog.String("reset_time", rlInfo.ResetTimeFormatted()),
					)
					// Don't mark as processed - will retry via scheduler
					continue
				}
			}

			p.logger.Error("Failed to process issue",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
			// Don't mark as processed - the pilot-failed label is the source of truth
			// Removing the label will make the issue retryable without restart
			continue
		}

		// Notify autopilot controller of new PR (if callback registered)
		if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
			p.logger.Info("Notifying autopilot of PR creation",
				slog.Int("pr_number", result.PRNumber),
				slog.Int("issue_number", issue.Number),
				slog.String("branch", result.BranchName),
			)
			p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName)
		}

		// If we created a PR and should wait for merge
		if result != nil && result.PRNumber > 0 && p.waitForMerge && p.mergeWaiter != nil {
			p.logger.Info("Waiting for PR merge before next issue",
				slog.Int("pr_number", result.PRNumber),
				slog.String("pr_url", result.PRURL),
			)

			mergeResult, err := p.mergeWaiter.WaitWithCallback(ctx, result.PRNumber, func(r *MergeWaitResult) {
				p.logger.Debug("PR status check",
					slog.Int("pr_number", r.PRNumber),
					slog.String("status", r.Message),
				)
			})

			if err != nil {
				p.logger.Warn("Error waiting for PR merge, pausing sequential processing",
					slog.Int("pr_number", result.PRNumber),
					slog.Any("error", err),
				)
				// DON'T mark as processed - leave for retry after fix
				time.Sleep(5 * time.Minute)
				continue
			}

			p.logger.Info("PR merge wait completed",
				slog.Int("pr_number", result.PRNumber),
				slog.Bool("merged", mergeResult.Merged),
				slog.Bool("closed", mergeResult.Closed),
				slog.Bool("conflicting", mergeResult.Conflicting),
				slog.Bool("timed_out", mergeResult.TimedOut),
			)

			// Check if PR has conflicts - stop processing
			if mergeResult.Conflicting {
				p.logger.Warn("PR has conflicts, pausing sequential processing",
					slog.Int("pr_number", result.PRNumber),
					slog.String("pr_url", result.PRURL),
				)
				// DON'T mark as processed - needs manual resolution or rebase
				time.Sleep(5 * time.Minute)
				continue
			}

			// Check if PR timed out
			if mergeResult.TimedOut {
				p.logger.Warn("PR merge timed out, pausing sequential processing",
					slog.Int("pr_number", result.PRNumber),
					slog.String("pr_url", result.PRURL),
				)
				// DON'T mark as processed - needs investigation
				time.Sleep(5 * time.Minute)
				continue
			}

			// Only mark as processed if actually merged
			if mergeResult.Merged {
				p.markProcessed(issue.Number)
				continue
			}

			// PR was closed without merge
			if mergeResult.Closed {
				p.logger.Info("PR was closed without merge",
					slog.Int("pr_number", result.PRNumber),
				)
				// DON'T mark as processed - issue may need re-execution
				continue
			}
		}

		// Direct commit case: no PR to wait for, proceed to next issue
		if result != nil && result.Success && result.PRNumber == 0 {
			p.logger.Info("Direct commit completed, proceeding to next issue",
				slog.Int("issue_number", issue.Number),
				slog.String("commit_sha", result.HeadSHA),
			)
			p.markProcessed(issue.Number)
			continue
		}

		// PR was created but we're not waiting for merge, or no PR was created
		p.markProcessed(issue.Number)
	}
}

// findOldestUnprocessedIssue finds the oldest issue with the pilot label
// that hasn't been processed yet and has no pending dependencies.
func (p *Poller) findOldestUnprocessedIssue(ctx context.Context) (*Issue, error) {
	issues, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label},
		State:  StateOpen,
		Sort:   "created", // Sort by creation date to get oldest first
	})
	if err != nil {
		return nil, err
	}

	// Filter out already processed and in-progress issues
	var candidates []*Issue
	for _, issue := range issues {
		// Skip if has status labels
		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) || HasLabel(issue, LabelFailed) {
			continue
		}

		// Check if previously processed
		p.mu.RLock()
		processed := p.processed[issue.Number]
		p.mu.RUnlock()

		// If processed but no status labels, allow retry (pilot-failed was removed)
		if processed {
			p.logger.Info("Issue was processed but status labels removed, allowing retry",
				slog.Int("number", issue.Number))
			p.mu.Lock()
			delete(p.processed, issue.Number)
			p.mu.Unlock()
			// Also clear from persistent store
			if p.processedStore != nil {
				if err := p.processedStore.UnmarkIssueProcessed(issue.Number); err != nil {
					p.logger.Warn("Failed to unmark issue in store",
						slog.Int("number", issue.Number),
						slog.Any("error", err))
				}
			}
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

	// Find the oldest issue without pending dependencies
	for _, candidate := range candidates {
		if !p.hasPendingDependencies(ctx, candidate) {
			return candidate, nil
		}
		p.logger.Info("Skipping issue with pending dependencies",
			slog.Int("number", candidate.Number),
			slog.String("title", candidate.Title),
		)
	}

	// All candidates have pending dependencies
	return nil, nil
}

// processIssueSequential processes a single issue and returns PR info
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

// groupByOverlappingScope partitions issues into groups where members reference
// at least one common directory (transitive closure). Within each group only the
// oldest issue should be dispatched to avoid merge conflicts.
func groupByOverlappingScope(candidates []*Issue) [][]*Issue {
	n := len(candidates)
	if n == 0 {
		return nil
	}

	// Union-Find
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	// Pre-extract directories once per candidate, then pairwise set intersection
	dirs := make([]map[string]bool, n)
	for i, c := range candidates {
		dirs[i] = executor.ExtractDirectoriesFromText(c.Body)
	}
	for i := 0; i < n; i++ {
		if len(dirs[i]) == 0 {
			continue
		}
		for j := i + 1; j < n; j++ {
			if len(dirs[j]) == 0 {
				continue
			}
			for d := range dirs[i] {
				if dirs[j][d] {
					union(i, j)
					break
				}
			}
		}
	}

	// Collect groups
	groups := make(map[int][]*Issue)
	for i, c := range candidates {
		root := find(i)
		groups[root] = append(groups[root], c)
	}

	result := make([][]*Issue, 0, len(groups))
	for _, g := range groups {
		result = append(result, g)
	}
	return result
}

// checkForNewIssues fetches issues and dispatches new ones concurrently (parallel mode)
func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label},
		State:  StateOpen,
		Sort:   "created",
	})
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	// Phase 1: Collect candidates eligible for dispatch
	var candidates []*Issue
	for _, issue := range issues {
		// Skip if already in progress or failed (check before processed to allow retry)
		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelFailed) {
			continue
		}

		// Skip and mark done issues as permanently processed
		if HasLabel(issue, LabelDone) {
			p.markProcessed(issue.Number)
			continue
		}

		// Check if already processed
		p.mu.RLock()
		processed := p.processed[issue.Number]
		p.mu.RUnlock()

		// If processed but no status labels, allow retry (pilot-failed was removed)
		if processed {
			p.logger.Info("Issue was processed but status labels removed, allowing retry",
				slog.Int("number", issue.Number))
			p.mu.Lock()
			delete(p.processed, issue.Number)
			p.mu.Unlock()
			if p.processedStore != nil {
				if err := p.processedStore.UnmarkIssueProcessed(issue.Number); err != nil {
					p.logger.Warn("Failed to unmark issue in store",
						slog.Int("number", issue.Number),
						slog.Any("error", err))
				}
			}
		}

		// Skip issues with pending dependencies
		if p.hasPendingDependencies(ctx, issue) {
			p.logger.Debug("Skipping issue with pending dependencies in parallel mode",
				slog.Int("number", issue.Number),
			)
			continue
		}

		candidates = append(candidates, issue)
	}

	// Phase 2: Group candidates by overlapping scope, dispatch only oldest per group
	groups := groupByOverlappingScope(candidates)
	var toDispatch []*Issue
	for _, group := range groups {
		if len(group) == 1 {
			toDispatch = append(toDispatch, group[0])
		} else {
			// Sort by CreatedAt ascending; dispatch only the oldest
			sort.Slice(group, func(i, j int) bool {
				return group[i].CreatedAt.Before(group[j].CreatedAt)
			})
			toDispatch = append(toDispatch, group[0])
			for _, deferred := range group[1:] {
				p.logger.Info("Deferring issue due to overlapping scope with older issue",
					slog.Int("number", deferred.Number),
					slog.Int("dispatched", group[0].Number),
				)
			}
		}
	}

	// Phase 3: Dispatch selected issues
	for _, issue := range toDispatch {
		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(issue.Number)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching issue for parallel execution",
			slog.Int("number", issue.Number),
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
		go func(issue *Issue) {
			defer p.activeWg.Done()
			defer func() { <-p.semaphore }() // release slot

			if p.onIssueWithResult != nil {
				result, err := p.onIssueWithResult(ctx, issue)
				if err != nil {
					p.logger.Error("Failed to process issue",
						slog.Int("number", issue.Number),
						slog.Any("error", err),
					)
					return
				}

				// Notify autopilot controller of new PR
				if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
					p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName)
				}
			} else if p.onIssue != nil {
				if err := p.onIssue(ctx, issue); err != nil {
					p.logger.Error("Failed to process issue",
						slog.Int("number", issue.Number),
						slog.Any("error", err),
					)
				}
			}
		}(issue)
	}
}

// markProcessed marks an issue as processed
func (p *Poller) markProcessed(number int) {
	p.mu.Lock()
	p.processed[number] = true
	p.mu.Unlock()

	// Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkIssueProcessed(number, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.Int("issue", number), slog.Any("error", err))
		}
	}
}

// Drain stops accepting new issues and waits for active executions to finish.
// Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new issues will be accepted")
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
	p.logger.Info("Poller drained — all active tasks completed")
}

// WaitForActive waits for all active parallel goroutines to finish.
// Used in tests to synchronize after checkForNewIssues.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
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

// ClearProcessed removes a single issue from the processed map.
// Used by the stale label cleaner when removing pilot-failed labels
// to allow the issue to be retried without restarting Pilot.
func (p *Poller) ClearProcessed(number int) {
	p.mu.Lock()
	delete(p.processed, number)
	p.mu.Unlock()

	// Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.UnmarkIssueProcessed(number); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.Int("number", number),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.Int("number", number))
}

// ExtractPRNumber extracts PR number from a GitHub PR URL
// e.g., "https://github.com/owner/repo/pull/123" -> 123
func ExtractPRNumber(prURL string) (int, error) {
	if prURL == "" {
		return 0, fmt.Errorf("empty PR URL")
	}

	// Match pattern: /pull/123 or /pulls/123
	re := regexp.MustCompile(`/pulls?/(\d+)`)
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

// dependencyRegex matches common dependency patterns in issue bodies:
// - "Depends on: #123"
// - "Depends on #123"
// - "## Depends on: #123"
// - "Blocked by: #123"
// - "Blocked by #123"
// - "Requires: #123"
// - "Requires #123"
var dependencyRegex = regexp.MustCompile(`(?i)(?:depends\s+on|blocked\s+by|requires):?\s*#(\d+)`)

// ParseDependencies extracts issue numbers that this issue depends on from the body.
// It looks for patterns like "Depends on: #123", "Blocked by: #456", etc.
func ParseDependencies(body string) []int {
	if body == "" {
		return nil
	}

	matches := dependencyRegex.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	// Use a map to deduplicate
	seen := make(map[int]bool)
	var deps []int

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		var num int
		if _, err := fmt.Sscanf(match[1], "%d", &num); err != nil {
			continue
		}
		if num > 0 && !seen[num] {
			seen[num] = true
			deps = append(deps, num)
		}
	}

	return deps
}

// hasPendingDependencies checks if any of the issue's dependencies are still open.
// Returns true if the issue has open dependencies and should be skipped.
func (p *Poller) hasPendingDependencies(ctx context.Context, issue *Issue) bool {
	deps := ParseDependencies(issue.Body)
	if len(deps) == 0 {
		return false
	}

	for _, depNum := range deps {
		depIssue, err := p.client.GetIssue(ctx, p.owner, p.repo, depNum)
		if err != nil {
			// If we can't fetch the dependency, log and assume it's still pending
			// to be safe (don't execute if we can't verify)
			p.logger.Warn("Failed to fetch dependency issue",
				slog.Int("issue", issue.Number),
				slog.Int("dependency", depNum),
				slog.Any("error", err),
			)
			return true
		}

		// Check if dependency is still open
		if depIssue.State == "open" {
			p.logger.Debug("Issue has open dependency, skipping",
				slog.Int("issue", issue.Number),
				slog.Int("dependency", depNum),
			)
			return true
		}
	}

	return false
}
