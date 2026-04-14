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

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
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

// TaskChecker checks whether a task is currently queued or in-progress.
// Used during retry grace-period evaluation to avoid re-dispatching issues
// that are still being executed.
type TaskChecker interface {
	IsTaskQueued(taskID string) bool
}

// ExecutionChecker verifies whether a completed execution exists for a task.
// GH-2242: Prevents re-dispatch of completed tasks when pilot-done label is missing.
type ExecutionChecker interface {
	HasCompletedExecution(taskID, projectPath string) (bool, error)
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
	processed map[int]time.Time
	mu        sync.RWMutex
	onIssue   func(ctx context.Context, issue *Issue) error
	// onIssueWithResult is called for sequential mode, returns PR info
	onIssueWithResult func(ctx context.Context, issue *Issue) (*IssueResult, error)
	// OnPRCreated is called when a PR is created after issue processing
	// Parameters: prNumber, prURL, issueNumber, headSHA, branchName, issueNodeID
	OnPRCreated func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)
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

	// GH-2201: Retry grace period prevents rapid re-dispatch of recently-processed issues.
	// When a processed issue's status labels are removed, the poller waits this duration
	// before allowing retry. Default: 5 minutes.
	retryGracePeriod time.Duration

	// GH-2201: TaskChecker verifies whether an issue is still queued/in-progress
	// before allowing retry after the grace period expires.
	taskChecker TaskChecker

	// GH-2176: Auto-retry issues stuck with pilot-failed from execution failures.
	// Tracks how many times each issue has been retried after pilot-failed.
	failedRetryCount map[int]int
	maxFailedRetries int // default: 3

	// GH-2276: Auto-retry issues with pilot-retry-ready (PR closed without merge).
	// Tracks how many times each issue has been retried after pilot-retry-ready.
	retryReadyCount      map[int]int
	maxRetryReadyRetries int // default: 3

	// GH-2242: ExecutionChecker prevents re-dispatch of completed tasks
	// when pilot-done label failed to apply.
	execChecker ExecutionChecker
	projectPath string
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

// WithOnPRCreated sets the callback for PR creation events.
// The callback receives prNumber, prURL, issueNumber, headSHA, branchName, issueNodeID.
// The callback is invoked after a PR is successfully created for an issue
func WithOnPRCreated(fn func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)) PollerOption {
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

// WithRetryGracePeriod sets the minimum time that must elapse after an issue is
// marked processed before the retry path will allow re-dispatch. Default: 5 minutes.
func WithRetryGracePeriod(d time.Duration) PollerOption {
	return func(p *Poller) {
		p.retryGracePeriod = d
	}
}

// WithTaskChecker sets the task checker used to verify whether an issue is still
// queued or in-progress before allowing retry after the grace period expires.
func WithTaskChecker(tc TaskChecker) PollerOption {
	return func(p *Poller) {
		p.taskChecker = tc
	}
}

// WithExecutionChecker sets the execution checker used to prevent re-dispatch
// of tasks that already have a completed execution in the database (GH-2242).
func WithExecutionChecker(ec ExecutionChecker, projectPath string) PollerOption {
	return func(p *Poller) {
		p.execChecker = ec
		p.projectPath = projectPath
	}
}

// WithMaxFailedRetries sets the maximum number of auto-retries for issues
// that are stuck with pilot-failed label from execution failures. Default: 3.
func WithMaxFailedRetries(n int) PollerOption {
	return func(p *Poller) {
		if n < 0 {
			n = 0
		}
		p.maxFailedRetries = n
	}
}

// WithMaxRetryReadyRetries sets the maximum number of auto-retries for issues
// with pilot-retry-ready label (PR closed without merge). Default: 3.
func WithMaxRetryReadyRetries(n int) PollerOption {
	return func(p *Poller) {
		if n < 0 {
			n = 0
		}
		p.maxRetryReadyRetries = n
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
		client:           client,
		owner:            parts[0],
		repo:             parts[1],
		label:            label,
		interval:         interval,
		processed:        make(map[int]time.Time),
		logger:           logging.WithComponent("github-poller"),
		executionMode:    ExecutionModeAuto, // Default matches config.DefaultExecutionConfig()
		waitForMerge:     true,
		prPollInterval:   30 * time.Second,
		prTimeout:        1 * time.Hour,
		retryGracePeriod: 5 * time.Minute, // GH-2201: default grace period
		failedRetryCount:     make(map[int]int),
		maxFailedRetries:     3, // GH-2176: default max retries for pilot-failed issues
		retryReadyCount:      make(map[int]int),
		maxRetryReadyRetries: 3, // GH-2276: default max retries for pilot-retry-ready issues
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
				p.processed[num] = time.Now()
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
		// GH-2301: Also clear from processed map/store so the first poll cycle picks it up.
		p.unmarkProcessed(issue.Number)
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
			p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName, issue.NodeID)
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

		// GH-2176: Don't mark as processed if execution failed (no PR created, not successful)
		// This allows the retry path in findOldestUnprocessedIssue to re-pick the issue
		// after pilot-failed label is removed (manually or by stale label cleanup)
		if result != nil && !result.Success && result.PRNumber == 0 {
			p.logger.Info("Execution failed without PR, not marking as processed (retryable)",
				slog.Int("issue_number", issue.Number),
			)
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
		// Skip pull requests (GitHub Issues API returns both issues and PRs)
		if issue.PullRequest != nil {
			continue
		}

		// Skip if in-progress or done
		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) {
			continue
		}

		// GH-2176: Auto-retry issues stuck with pilot-failed (no pilot-done)
		if HasLabel(issue, LabelFailed) {
			if !p.shouldRetryFailedIssue(ctx, issue) {
				continue
			}
			// Label removed, fall through to candidate selection
		}

		// GH-2276: Auto-retry issues with pilot-retry-ready (PR closed without merge)
		if HasLabel(issue, LabelRetryReady) {
			if !p.shouldRetryRetryReadyIssue(ctx, issue) {
				continue
			}
			// Label removed, fall through to candidate selection
		}

		// Check if previously processed
		p.mu.RLock()
		processedAt, processed := p.processed[issue.Number]
		p.mu.RUnlock()

		// If processed but no status labels, allow retry (pilot-failed was removed)
		if processed {
			// GH-2201: Check grace period before allowing retry
			if p.retryGracePeriod > 0 && time.Since(processedAt) < p.retryGracePeriod {
				p.logger.Debug("Issue within retry grace period, skipping",
					slog.Int("number", issue.Number),
					slog.Duration("elapsed", time.Since(processedAt)),
					slog.Duration("grace_period", p.retryGracePeriod))
				continue
			}

			// GH-2201: Check if task is still queued/in-progress
			if p.taskChecker != nil {
				taskID := fmt.Sprintf("GH-%d", issue.Number)
				if p.taskChecker.IsTaskQueued(taskID) {
					p.logger.Debug("Issue still queued/in-progress, skipping retry",
						slog.Int("number", issue.Number),
						slog.String("task_id", taskID))
					continue
				}
			}

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

			// GH-1983: Before retrying, check if merged PRs already exist
			if p.hasMergedWork(ctx, issue) {
				continue
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
		// Skip pull requests (GitHub Issues API returns both issues and PRs)
		if issue.PullRequest != nil {
			continue
		}

		// Skip if already in progress
		if HasLabel(issue, LabelInProgress) {
			continue
		}

		// GH-2176: Auto-retry issues stuck with pilot-failed (no pilot-done)
		if HasLabel(issue, LabelFailed) {
			if !p.shouldRetryFailedIssue(ctx, issue) {
				continue
			}
			// Label removed, fall through to candidate selection
		}

		// GH-2276: Auto-retry issues with pilot-retry-ready (PR closed without merge)
		if HasLabel(issue, LabelRetryReady) {
			if !p.shouldRetryRetryReadyIssue(ctx, issue) {
				continue
			}
			// Label removed, fall through to candidate selection
		}

		// Skip and mark done issues as permanently processed
		if HasLabel(issue, LabelDone) {
			p.markProcessed(issue.Number)
			continue
		}

		// Check if already processed
		p.mu.RLock()
		processedAt, processed := p.processed[issue.Number]
		p.mu.RUnlock()

		// If processed but no status labels, allow retry (pilot-failed was removed)
		if processed {
			// GH-2201: Check grace period before allowing retry
			if p.retryGracePeriod > 0 && time.Since(processedAt) < p.retryGracePeriod {
				p.logger.Debug("Issue within retry grace period, skipping",
					slog.Int("number", issue.Number),
					slog.Duration("elapsed", time.Since(processedAt)),
					slog.Duration("grace_period", p.retryGracePeriod))
				continue
			}

			// GH-2201: Check if task is still queued/in-progress
			if p.taskChecker != nil {
				taskID := fmt.Sprintf("GH-%d", issue.Number)
				if p.taskChecker.IsTaskQueued(taskID) {
					p.logger.Debug("Issue still queued/in-progress, skipping retry",
						slog.Int("number", issue.Number),
						slog.String("task_id", taskID))
					continue
				}
			}

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

			// GH-1983: Before retrying, check if merged PRs already exist
			if p.hasMergedWork(ctx, issue) {
				continue
			}
		}

		// Skip issues with pending dependencies
		if p.hasPendingDependencies(ctx, issue) {
			p.logger.Debug("Skipping issue with pending dependencies in parallel mode",
				slog.Int("number", issue.Number),
			)
			continue
		}

		// GH-2242: Before dispatching, check if we already have a completed execution.
		// This prevents re-dispatch when pilot-done label failed to apply.
		if p.execChecker != nil {
			taskID := fmt.Sprintf("GH-%d", issue.Number)
			completed, err := p.execChecker.HasCompletedExecution(taskID, p.projectPath)
			if err != nil {
				p.logger.Warn("Failed to check execution status",
					slog.Int("number", issue.Number),
					slog.Any("error", err))
			} else if completed {
				p.logger.Info("Skipping re-dispatch — completed execution exists",
					slog.Int("number", issue.Number),
					slog.String("task_id", taskID))
				p.markProcessed(issue.Number)
				continue
			}
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
					// GH-2176: Unmark so retry path can re-pick after pilot-failed is removed
					p.unmarkProcessed(issue.Number)
					return
				}

				// GH-2176: Unmark if execution failed without creating a PR
				if result != nil && !result.Success && result.PRNumber == 0 {
					p.logger.Info("Execution failed without PR, unmarking for retry",
						slog.Int("number", issue.Number),
					)
					p.unmarkProcessed(issue.Number)
				}

				// Notify autopilot controller of new PR
				if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
					p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName, issue.NodeID)
				}
			} else if p.onIssue != nil {
				if err := p.onIssue(ctx, issue); err != nil {
					p.logger.Error("Failed to process issue",
						slog.Int("number", issue.Number),
						slog.Any("error", err),
					)
					// GH-2176: Unmark so retry path can re-pick
					p.unmarkProcessed(issue.Number)
				}
			}
		}(issue)
	}
}

// markProcessed marks an issue as processed with the current timestamp
func (p *Poller) markProcessed(number int) {
	p.mu.Lock()
	p.processed[number] = time.Now()
	p.mu.Unlock()

	// Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.MarkIssueProcessed(number, "processed"); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.Int("issue", number), slog.Any("error", err))
		}
	}
}

// unmarkProcessed removes an issue from the processed set, allowing retry.
// GH-2176: Used when execution fails without creating a PR.
func (p *Poller) unmarkProcessed(number int) {
	p.mu.Lock()
	delete(p.processed, number)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.UnmarkIssueProcessed(number); err != nil {
			p.logger.Warn("Failed to unmark processed issue", slog.Int("issue", number), slog.Any("error", err))
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
	_, ok := p.processed[number]
	return ok
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
	p.processed = make(map[int]time.Time)
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

// hasMergedWork checks if the issue already has merged PRs (e.g. "GH-123" in title).
// If merged work exists, the issue is marked as done and should be skipped.
func (p *Poller) hasMergedWork(ctx context.Context, issue *Issue) bool {
	found, err := p.client.SearchMergedPRsForIssue(ctx, p.owner, p.repo, issue.Number)
	if err != nil {
		p.logger.Warn("Failed to check for merged PRs",
			slog.Int("issue", issue.Number),
			slog.Any("error", err),
		)
		return false // Don't block on API errors
	}
	if !found {
		return false
	}

	p.logger.Info("Issue already has merged PRs, marking as done",
		slog.Int("issue", issue.Number),
		slog.String("title", issue.Title),
	)
	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelDone}); err != nil {
		p.logger.Warn("Failed to add pilot-done label",
			slog.Int("issue", issue.Number),
			slog.Any("error", err),
		)
	}
	// Remove stale pilot-failed label (GH-1302 gap)
	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelFailed); err != nil {
		p.logger.Debug("Failed to remove pilot-failed (may not exist)",
			slog.Int("issue", issue.Number),
			slog.Any("error", err),
		)
	}
	p.markProcessed(issue.Number)
	return true
}

// shouldRetryFailedIssue checks if a pilot-failed issue should be auto-retried.
// Returns true if the issue should be retried (label removed), false if it should be skipped.
// GH-2176: Issues stuck with pilot-failed get retried up to maxFailedRetries times.
func (p *Poller) shouldRetryFailedIssue(ctx context.Context, issue *Issue) bool {
	// Don't retry closed issues — they may have stale pilot-failed labels (GH-2252)
	if issue.State != "open" {
		p.logger.Info("Skipping retry — issue is closed",
			slog.Int("number", issue.Number),
			slog.String("state", issue.State),
		)
		return false
	}

	// Never retry if also marked done
	if HasLabel(issue, LabelDone) {
		return false
	}

	p.mu.RLock()
	retries := p.failedRetryCount[issue.Number]
	p.mu.RUnlock()

	if retries >= p.maxFailedRetries {
		p.logger.Warn("Issue has reached max failed retries, skipping",
			slog.Int("number", issue.Number),
			slog.Int("retries", retries),
			slog.Int("max", p.maxFailedRetries),
		)
		return false
	}

	// Check if merged work already exists before retrying
	if p.hasMergedWork(ctx, issue) {
		return false
	}

	// Remove pilot-failed label and increment retry count
	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelFailed); err != nil {
		p.logger.Warn("Failed to remove pilot-failed label for retry",
			slog.Int("number", issue.Number),
			slog.Any("error", err),
		)
		return false
	}

	p.mu.Lock()
	p.failedRetryCount[issue.Number] = retries + 1
	p.mu.Unlock()

	// Clear from processed map so the issue can be re-picked
	p.ClearProcessed(issue.Number)

	p.logger.Info("Auto-retrying pilot-failed issue",
		slog.Int("number", issue.Number),
		slog.Int("retry", retries+1),
		slog.Int("max", p.maxFailedRetries),
	)

	return true
}

// shouldRetryRetryReadyIssue checks if a pilot-retry-ready issue should be auto-retried.
// Returns true if the issue should be retried (label removed), false if it should be skipped.
// GH-2276: Issues with pilot-retry-ready (PR closed without merge) get retried up to maxRetryReadyRetries times.
func (p *Poller) shouldRetryRetryReadyIssue(ctx context.Context, issue *Issue) bool {
	// Don't retry closed issues
	if issue.State != "open" {
		p.logger.Info("Skipping retry — issue is closed",
			slog.Int("number", issue.Number),
			slog.String("state", issue.State),
		)
		return false
	}

	// Never retry if also marked done
	if HasLabel(issue, LabelDone) {
		return false
	}

	p.mu.RLock()
	retries := p.retryReadyCount[issue.Number]
	p.mu.RUnlock()

	if retries >= p.maxRetryReadyRetries {
		p.logger.Warn("Issue has reached max retry-ready retries, skipping",
			slog.Int("number", issue.Number),
			slog.Int("retries", retries),
			slog.Int("max", p.maxRetryReadyRetries),
		)
		return false
	}

	// Check if merged work already exists before retrying
	if p.hasMergedWork(ctx, issue) {
		return false
	}

	// Remove pilot-retry-ready label and increment retry count
	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelRetryReady); err != nil {
		p.logger.Warn("Failed to remove pilot-retry-ready label for retry",
			slog.Int("number", issue.Number),
			slog.Any("error", err),
		)
		return false
	}

	p.mu.Lock()
	p.retryReadyCount[issue.Number] = retries + 1
	p.mu.Unlock()

	// Clear from processed map so the issue can be re-picked
	p.ClearProcessed(issue.Number)

	p.logger.Info("Auto-retrying pilot-retry-ready issue",
		slog.Int("number", issue.Number),
		slog.Int("retry", retries+1),
		slog.Int("max", p.maxRetryReadyRetries),
	)

	return true
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
