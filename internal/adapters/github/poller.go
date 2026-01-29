package github

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
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
)

// IssueResult is returned by the issue handler with PR information
type IssueResult struct {
	Success  bool
	PRNumber int    // PR number if created
	PRURL    string // PR URL if created
	HeadSHA  string // Head commit SHA of the PR
	Error    error
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
	// Parameters: prNumber, prURL, issueNumber, headSHA
	OnPRCreated func(prNumber int, prURL string, issueNumber int, headSHA string)
	logger      *slog.Logger

	// Sequential mode configuration
	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	prTimeout      time.Duration
	prPollInterval time.Duration

	// Rate limit retry scheduler
	scheduler *executor.Scheduler
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
func WithOnPRCreated(fn func(prNumber int, prURL string, issueNumber int, headSHA string)) PollerOption {
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
		executionMode:  ExecutionModeParallel, // Default for backward compatibility
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

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		p.startParallel(ctx)
	}
}

// startParallel runs the legacy parallel execution mode
func (p *Poller) startParallel(ctx context.Context) {
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
			// Mark as processed to avoid infinite retry loop
			// The issue will have pilot-failed label
			p.markProcessed(issue.Number)
			continue
		}

		// Notify autopilot controller of new PR (if callback registered)
		if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
			p.logger.Info("Notifying autopilot of PR creation",
				slog.Int("pr_number", result.PRNumber),
				slog.Int("issue_number", issue.Number),
			)
			p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA)
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

		// PR was created but we're not waiting for merge, or no PR was created
		p.markProcessed(issue.Number)
	}
}

// findOldestUnprocessedIssue finds the oldest issue with the pilot label
// that hasn't been processed yet
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
		p.mu.RLock()
		processed := p.processed[issue.Number]
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

// checkForNewIssues fetches issues and processes new ones (parallel mode)
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
