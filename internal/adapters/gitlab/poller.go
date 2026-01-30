package gitlab

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
	Success  bool
	MRNumber int    // MR IID if created
	MRURL    string // MR URL if created
	HeadSHA  string // Head commit SHA of the MR
	Error    error
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
	// Parameters: mrIID, mrURL, issueIID, headSHA
	OnMRCreated func(mrIID int, mrURL string, issueIID int, headSHA string)
	logger      *slog.Logger

	// Sequential mode configuration
	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	mrTimeout      time.Duration
	mrPollInterval time.Duration
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
func WithOnMRCreated(fn func(mrIID int, mrURL string, issueIID int, headSHA string)) PollerOption {
	return func(p *Poller) {
		p.OnMRCreated = fn
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
			)
			p.OnMRCreated(result.MRNumber, result.MRURL, issue.IID, result.HeadSHA)
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

// checkForNewIssues fetches issues and processes new ones (parallel mode)
func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		Labels:  []string{p.label},
		State:   StateOpened,
		Sort:    "desc",
		OrderBy: "updated_at",
	})
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	for _, issue := range issues {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[issue.IID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if already in progress or done
		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) {
			p.markProcessed(issue.IID)
			continue
		}

		// Process the issue
		p.logger.Info("Found new issue",
			slog.Int("iid", issue.IID),
			slog.String("title", issue.Title),
		)

		if p.onIssue != nil {
			if err := p.onIssue(ctx, issue); err != nil {
				p.logger.Error("Failed to process issue",
					slog.Int("iid", issue.IID),
					slog.Any("error", err),
				)
				// Don't mark as processed so we can retry
				continue
			}
		}

		p.markProcessed(issue.IID)
	}
}

// markProcessed marks an issue as processed
func (p *Poller) markProcessed(iid int) {
	p.mu.Lock()
	p.processed[iid] = true
	p.mu.Unlock()
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
