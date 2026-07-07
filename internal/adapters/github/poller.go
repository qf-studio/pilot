package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
)

// defaultAuthFailureThreshold is the number of consecutive auth-classified
// fetch errors (401, or non-rate-limited 403) required before the poller
// escalates to an ERROR log + alert (GH-3839).
const defaultAuthFailureThreshold = 3

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
	Mark(source, repo, issueID string) error
	Unmark(source, repo, issueID string) error
	IsProcessed(source, repo, issueID string) (bool, error)
	Load(source, repo string) (map[string]time.Time, error)
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
	// InvalidateCompletion deletes stale completed execution records so a
	// retry-ready re-dispatch is not silently no-op'd (GH-3418).
	InvalidateCompletion(taskID, projectPath string) error
}

// Verdict is the poller-side result of a pre-flight judgment.
// Mirrors executor.PreFlightVerdict but keeps the poller decoupled from the executor
// package to avoid import cycles.
type Verdict struct {
	Accepted   bool
	Decision   string
	Reason     string
	Confidence float64
}

// PreFlightJudger evaluates issues before dispatch to avoid burning worker slots on
// vague, ambiguous, or otherwise unactionable issues (GH-2802).
// Implemented by a shim in cmd/pilot/main.go that wraps *executor.IntentJudge.
type PreFlightJudger interface {
	JudgeIssue(ctx context.Context, title, body, repoContext string) (Verdict, error)
}

// ExecutionSaver persists pre-flight rejection records for observability.
type ExecutionSaver interface {
	SaveDeclinedExecution(taskID, projectPath, status, reason string) error
}

// IssueMetricsRecorder records issue processing outcomes.
// Implemented by *autopilot.Metrics; kept as an interface here to avoid circular imports.
type IssueMetricsRecorder interface {
	RecordIssueProcessed(result string)
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
	// inFlight tracks issues whose dispatch goroutine is currently executing
	// (set for the lifetime of the goroutine, including label finalization).
	// TASK-354: the periodic stranded-issue sweep skips in-flight issues so a
	// live execution is never disturbed.
	inFlight map[int]struct{}
	mu       sync.RWMutex
	onIssue  func(ctx context.Context, issue *Issue) error
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

	// TASK-354: interval for the periodic stranded-issue sweep that clears
	// pilot-in-progress from issues whose execution goroutine has ended without
	// a terminal transition (mid-session orphan recovery). Default: 10 minutes.
	strandSweepInterval time.Duration

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

	// GH-2802: Pre-flight judge evaluates issues before dispatch.
	// nil means disabled (config flag executor.pre_flight_judge.enabled=false).
	preFlightJudge PreFlightJudger
	execSaver      ExecutionSaver

	// metricsRecorder records issue processing outcomes (optional).
	metricsRecorder IssueMetricsRecorder

	// pollerMetrics records per-repo dispatch/skip counters (TASK-293, GH-3064).
	pollerMetrics skipreason.PollerMetricsRecorder

	// projectBoardSource sources candidates from a Projects V2 board column (GH-3228).
	// When non-nil, replaces label-based ListIssues in findOldestUnprocessedIssue.
	projectBoardSource *ProjectBoardSource

	// boardSync moves the issue card to inProgressStatus on confirmed dispatch (GH-3252).
	// nil or empty inProgressStatus disables the write-back, keeping label-mode identical.
	boardSync        *ProjectBoardSync
	inProgressStatus string

	// GH-3839: consecutive auth-failure escalation. Auth errors (401, or
	// non-rate-limited 403) on candidate fetches increment this counter; a
	// successful fetch resets it to zero. At authFailureThreshold consecutive
	// failures, an ERROR log names tokenSource and an alert fires through
	// alertProcessor (if configured). Rate-limited 403s stay on the existing
	// backoff path (#3798) and never touch this counter.
	consecutiveAuthFailures atomic.Int32
	authFailureThreshold    int
	tokenSource             string
	alertProcessor          executor.AlertEventProcessor
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

// WithPreFlightJudge sets the pre-flight issue quality judge (GH-2802).
// Pass nil to disable (same as not calling this option).
func WithPreFlightJudge(judge PreFlightJudger) PollerOption {
	return func(p *Poller) {
		p.preFlightJudge = judge
	}
}

// WithExecutionSaver sets the store used to persist pre-flight rejection records.
func WithExecutionSaver(saver ExecutionSaver) PollerOption {
	return func(p *Poller) {
		p.execSaver = saver
	}
}

// WithIssueMetricsRecorder sets the recorder for issue processing outcomes.
// Pass nil to disable (same as not calling this option).
func WithIssueMetricsRecorder(rec IssueMetricsRecorder) PollerOption {
	return func(p *Poller) {
		p.metricsRecorder = rec
	}
}

// WithPollerMetrics sets the recorder for per-repo dispatch/skip counters (TASK-293).
func WithPollerMetrics(rec skipreason.PollerMetricsRecorder) PollerOption {
	return func(p *Poller) {
		p.pollerMetrics = rec
	}
}

// WithProjectBoardSource configures the poller to source candidates from a Projects V2
// board column instead of by label. When set, FindIssuesFromProject replaces ListIssues
// as the candidate fetch in findOldestUnprocessedIssue; all downstream filters are unchanged.
func WithProjectBoardSource(src *ProjectBoardSource) PollerOption {
	return func(p *Poller) {
		p.projectBoardSource = src
	}
}

// WithBoardSync configures the poller to move the issue card to inProgressStatus on the
// Projects V2 board after confirmed dispatch. No-op when bs is nil or inProgressStatus is "".
func WithBoardSync(bs *ProjectBoardSync, inProgressStatus string) PollerOption {
	return func(p *Poller) {
		p.boardSync = bs
		p.inProgressStatus = inProgressStatus
	}
}

// WithAlertProcessor sets the alert processor used to escalate consecutive
// GitHub auth failures (GH-3839). The interface is the same narrow,
// import-cycle-free shape executor.Runner already uses (ProcessEvent(AlertEvent));
// callers typically pass alerts.NewEngineAdapter(engine). Pass nil (default)
// to disable alerting — escalation still logs at ERROR either way.
func WithAlertProcessor(ap executor.AlertEventProcessor) PollerOption {
	return func(p *Poller) {
		p.alertProcessor = ap
	}
}

// WithTokenSource names where the poller's GitHub token was resolved from
// (e.g. "config", "env GITHUB_TOKEN", "gh CLI"), included in auth-failure
// escalation logs/alerts so a dead token can be diagnosed without
// re-deriving the resolution chain (GH-3839, mirrors Client.Verify's
// tokenSource parameter from GH-3718).
func WithTokenSource(source string) PollerOption {
	return func(p *Poller) {
		p.tokenSource = source
	}
}

// WithAuthFailureThreshold sets the number of consecutive auth failures
// required before escalating to an ERROR log + alert (GH-3839). Default: 3.
func WithAuthFailureThreshold(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.authFailureThreshold = n
	}
}

// NewPoller creates a new GitHub issue poller
func NewPoller(client *Client, repo string, label string, interval time.Duration, opts ...PollerOption) (*Poller, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format, expected owner/repo: %s", repo)
	}

	p := &Poller{
		client:               client,
		owner:                parts[0],
		repo:                 parts[1],
		label:                label,
		interval:             interval,
		processed:            make(map[int]time.Time),
		inFlight:             make(map[int]struct{}),
		logger:               logging.WithComponent("github-poller"),
		executionMode:        ExecutionModeAuto, // Default matches config.DefaultExecutionConfig()
		waitForMerge:         true,
		prPollInterval:       30 * time.Second,
		prTimeout:            1 * time.Hour,
		retryGracePeriod:     5 * time.Minute,  // GH-2201: default grace period
		strandSweepInterval:  10 * time.Minute, // TASK-354: default stranded-issue sweep cadence
		failedRetryCount:     make(map[int]int),
		maxFailedRetries:     3, // GH-2176: default max retries for pilot-failed issues
		retryReadyCount:      make(map[int]int),
		maxRetryReadyRetries: 3, // GH-2276: default max retries for pilot-retry-ready issues
		authFailureThreshold: defaultAuthFailureThreshold,
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
		loaded, err := p.processedStore.Load("github", p.repoKey())
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for idStr, t := range loaded {
				if num, parseErr := strconv.Atoi(idStr); parseErr == nil {
					p.processed[num] = t
				}
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
		if isAuthFetchError(err) {
			p.recordAuthFailure(err)
		} else {
			p.logger.Warn("Failed to check for orphaned issues", slog.Any("error", err))
		}
		return
	}
	p.resetAuthFailures()

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

// markInFlight records that issue's dispatch goroutine is executing. Held for the
// goroutine's lifetime (including label finalization) so sweepStrandedIssues never
// disturbs a live execution. TASK-354.
func (p *Poller) markInFlight(number int) {
	p.mu.Lock()
	p.inFlight[number] = struct{}{}
	p.mu.Unlock()
}

// unmarkInFlight clears the in-flight marker once the dispatch goroutine returns.
func (p *Poller) unmarkInFlight(number int) {
	p.mu.Lock()
	delete(p.inFlight, number)
	p.mu.Unlock()
}

// isInFlight reports whether issue's dispatch goroutine is currently executing.
func (p *Poller) isInFlight(number int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.inFlight[number]
	return ok
}

// sweepStrandedIssues clears pilot-in-progress from issues whose execution
// goroutine has ended without performing a terminal transition. Unlike
// recoverOrphanedIssues (which runs once at startup), this runs periodically so a
// mid-session strand — e.g. a no_op/terminal path that failed to remove the label
// (TASK-354) — self-heals without a daemon restart.
//
// Safety:
//   - Issues currently in-flight are skipped, so a live execution is never touched
//     (the in-progress label is added inside the dispatch goroutine, which is
//     marked in-flight, so the label can only exist for a live or already-finished
//     run).
//   - When a terminal label (blocked/done/failed) is already present alongside
//     pilot-in-progress, the strand is a contradictory leftover: the label is
//     cleaned up but the issue is NOT re-armed (no unmarkProcessed), so a
//     deterministically-failing issue does not re-dispatch on a loop.
//   - Otherwise (in-progress only, goroutine gone — e.g. a crash mid-run) the issue
//     is re-armed for pickup, mirroring recoverOrphanedIssues.
func (p *Poller) sweepStrandedIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label, LabelInProgress},
		State:  StateOpen,
	})
	if err != nil {
		p.logger.Warn("Stranded-issue sweep: list failed", slog.Any("error", err))
		return
	}

	for _, issue := range issues {
		if p.isInFlight(issue.Number) {
			continue // live execution — leave it alone
		}
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelInProgress); err != nil {
			p.logger.Warn("Stranded-issue sweep: failed to remove in-progress label",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
			continue
		}

		terminal := HasLabel(issue, LabelBlocked) || HasLabel(issue, LabelDone) || HasLabel(issue, LabelFailed)
		if !terminal {
			// Pure orphan (crash mid-run): re-arm for pickup.
			p.unmarkProcessed(issue.Number)
		}
		p.logger.Info("Swept stranded in-progress issue",
			slog.Int("number", issue.Number),
			slog.String("title", issue.Title),
			slog.Bool("rearmed", !terminal),
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

	// TASK-354: periodic stranded-issue sweep (mid-session orphan recovery).
	sweepInterval := p.strandSweepInterval
	if sweepInterval <= 0 {
		sweepInterval = 10 * time.Minute
	}
	sweepTicker := time.NewTicker(sweepInterval)
	defer sweepTicker.Stop()

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
		case <-sweepTicker.C:
			p.sweepStrandedIssues(ctx)
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

		// GH-2802: Pre-flight judge — evaluate issue quality before burning a worker slot.
		if p.preFlightJudge != nil {
			verdict, pfErr := p.preFlightJudge.JudgeIssue(ctx, issue.Title, issue.Body, "")
			if pfErr != nil {
				p.logger.Warn("pre-flight judge error (fail-open)",
					slog.Int("issue", issue.Number),
					slog.Any("error", pfErr))
			} else if !verdict.Accepted {
				p.logger.Info("pre-flight rejected issue",
					slog.Int("issue", issue.Number),
					slog.String("decision", verdict.Decision),
					slog.String("reason", verdict.Reason))
				p.handlePreFlightReject(ctx, issue, verdict)
				continue // skip dispatch; label removal re-triggers on next poll
			}
		}

		// Board sync: move card to in-progress on confirmed dispatch (GH-3252).
		p.syncBoardStatusInProgress(ctx, issue)

		result, err := p.processIssueSequential(ctx, issue)
		if err != nil {
			// Check if this is a rate limit error that can be retried
			if executor.IsRateLimitError(err.Error()) {
				rlInfo, ok := executor.ParseRateLimitError(err.Error())
				if ok && p.scheduler != nil {
					// Second converter path (rate-limit retry) — bypasses
					// ConvertIssueToTask, so sanitize explicitly here.
					cleanTitle, titleStripped := text.SanitizeUntrusted(issue.Title)
					cleanBody, bodyStripped := text.SanitizeUntrusted(issue.Body)
					if titleStripped+bodyStripped > 0 {
						p.logger.Warn("invisible_unicode_stripped",
							slog.String("path", "rate_limit_retry"),
							slog.Int("issue", issue.Number),
							slog.Int("title_stripped", titleStripped),
							slog.Int("body_stripped", bodyStripped),
						)
					}
					task := &executor.Task{
						ID:          fmt.Sprintf("GH-%d", issue.Number),
						Title:       cleanTitle,
						Description: cleanBody,
						ProjectPath: "", // Will be set by retry callback
					}
					p.scheduler.QueueTask(task, rlInfo)
					p.logger.Info("Task queued for retry after rate limit",
						slog.Int("issue", issue.Number),
						slog.Time("retry_at", rlInfo.ResetTime.Add(5*time.Minute)),
						slog.String("reset_time", rlInfo.ResetTimeFormatted()),
					)
					if p.metricsRecorder != nil {
						p.metricsRecorder.RecordIssueProcessed("rate_limited")
					}
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

		// Diagnostic: surface why OnPRCreated may not fire (GH-2999 Phase 1)
		if result == nil {
			p.logger.Info("OnPRCreated skipped: result is nil",
				slog.Int("issue_number", issue.Number),
			)
		} else if result.PRNumber == 0 {
			p.logger.Info("OnPRCreated skipped: PRNumber=0",
				slog.Int("issue_number", issue.Number),
				slog.String("pr_url", result.PRURL),
				slog.String("branch", result.BranchName),
				slog.String("head_sha", result.HeadSHA),
			)
		} else if p.OnPRCreated == nil {
			// GH-3784: a real PR exists but no autopilot controller is wired to
			// track it — it will never auto-merge unless the periodic orphan-PR
			// reconciler (or a human) picks it up. Fail loud, not Info.
			p.logger.Warn("OnPRCreated skipped: PR created but no autopilot callback wired",
				slog.Int("pr_number", result.PRNumber),
				slog.Int("issue_number", issue.Number),
				slog.String("branch", result.BranchName),
			)
		}
		// Gate: PRNumber > 0 implies executor surfaced a valid PR URL via runner.go:3151. Empty PRUrl (no-commits guard, push-fail, title-rejection) leaves PRNumber=0 and we silently skip — see TASK-60 for the upstream chain.
		// Notify autopilot controller of new PR (if callback registered)
		// Gate: PRNumber > 0 implies executor surfaced a valid PR URL via runner.go:3151. Empty PRUrl (no-commits guard, push-fail, title-rejection) leaves PRNumber=0 and we silently skip — see TASK-60 for the upstream chain.
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
// fetchCandidates returns the raw candidate issues for this poll cycle. When a
// projectBoardSource is configured it sources from the board column (GH-3228);
// otherwise it lists open issues by label. This is the single source-selection
// point used by BOTH dispatch paths — findOldestUnprocessedIssue (sequential)
// and checkForNewIssues (parallel/auto) — so the board source is honored
// regardless of execution mode (TASK-338). Previously only the sequential path
// consulted the board source, so source_enabled + mode:parallel silently
// reverted to label polling.
func (p *Poller) fetchCandidates(ctx context.Context) ([]*Issue, error) {
	if p.projectBoardSource != nil {
		sourceStatus := p.projectBoardSource.config.SourceStatus
		if sourceStatus == "" {
			sourceStatus = "Todo"
		}
		return p.projectBoardSource.FindIssuesFromProject(ctx, sourceStatus)
	}
	return p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label},
		State:  StateOpen,
		Sort:   "created", // oldest first
	})
}

// When projectBoardSource is set, candidates are fetched from the board column
// instead of by label; all downstream filters remain identical.
func (p *Poller) findOldestUnprocessedIssue(ctx context.Context) (*Issue, error) {
	issues, err := p.fetchCandidates(ctx)
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

		// GH-2402: Skip permanently-blocked issues. The user must remove
		// the pilot-blocked label to retry (e.g. after fixing a non-conventional title).
		if HasLabel(issue, LabelBlocked) {
			continue
		}

		// GH-2768: Skip issues declined as unactionable. Remove the label to re-enable dispatch.
		if HasLabel(issue, LabelNeedsClarification) {
			continue
		}

		// GH-2402: Auto-close sub-issues whose parent epic already shipped.
		if p.skipSupersededByParent(ctx, issue) {
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
				if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(issue.Number)); err != nil {
					p.logger.Warn("Failed to unmark issue in store",
						slog.Int("number", issue.Number),
						slog.Any("error", err))
				}
			}

			// GH-1983: Before retrying, check if merged PRs already exist
			if p.hasMergedWork(ctx, issue) {
				continue
			}

			// TASK-341: a re-dispatch whose pilot/GH-N PR is still OPEN (created but
			// not yet merged — pilot-done/close are deferred to merge time per
			// GH-3139/TASK-301) would only produce a "no new commit produced" no-op
			// that the handler used to mislabel pilot-blocked. Re-mark so the grace
			// window throttles re-checks (mirrors hasMergedWork); do NOT label — the
			// autopilot merge flow owns this issue until the PR merges or closes.
			if p.hasOpenPRAwaitingMerge(ctx, issue) {
				p.markProcessed(issue.Number)
				continue
			}
		}

		// GH-3269: Fresh candidates (never processed / post-unmark) bypass the
		// retry block above, so apply the merged-work guard unconditionally for them.
		if !processed && p.hasMergedWork(ctx, issue) {
			continue
		}

		// GH-3269: Mirror the parallel-mode HasCompletedExecution guard — prevents
		// re-dispatch when the pilot-done label failed to apply after execution.
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

	if len(candidates) == 0 {
		return nil, nil
	}

	// Sort by creation date (oldest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	// Find the oldest issue without pending dependencies
	for _, candidate := range candidates {
		if !p.hasPendingDependencies(candidate, issues) {
			return candidate, nil
		}
		p.logger.Info("Skipping issue with pending dependencies",
			slog.Int("number", candidate.Number),
			slog.String("title", candidate.Title),
		)
		p.recordSkip(skipreason.ReasonPendingDependency)
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
// at least one common directory (transitive closure), including a shared
// root-level config/scaffold file (package.json, go.mod, Makefile, ...) since
// those have no real parent directory to compare. Scaffold-flavored issues
// (2+ distinct root config files mentioned) are treated as globally
// overlapping and grouped with every candidate. Within each group only the
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

	// Pre-extract directories once per candidate, then pairwise set intersection.
	// Comment bodies are attacker-controllable text; sanitize before parsing so
	// smuggled paths cannot influence grouping decisions.
	dirs := make([]map[string]bool, n)
	scaffold := make([]bool, n)
	for i, c := range candidates {
		body := text.SanitizeUntrustedString(c.Body)
		dirs[i] = executor.ExtractDirectoriesFromText(body)
		// A body naming 2+ distinct root config/scaffold files (package.json,
		// tsconfig.json, Makefile, lockfiles, ...) is bootstrapping project
		// tooling from scratch — it collides with every other in-flight issue,
		// not just ones sharing a directory (GH-3714).
		scaffold[i] = len(executor.RootConfigFileMentions(body)) >= 2
	}
	for i := 0; i < n; i++ {
		if scaffold[i] {
			continue
		}
		if len(dirs[i]) == 0 {
			continue
		}
		for j := i + 1; j < n; j++ {
			if scaffold[j] {
				continue
			}
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

	// Scaffold-flavored issues serialize against every other candidate.
	for i := 0; i < n; i++ {
		if !scaffold[i] {
			continue
		}
		for j := 0; j < n; j++ {
			if j != i {
				union(i, j)
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

// isAuthFetchError classifies a candidate-fetch error as an authentication
// failure — 401 (AuthError), or a 403 that doRequest did NOT classify as
// rate-limited (RateLimitError) — as opposed to a transient/network error.
// Rate-limited 403s are excluded so they stay on the existing backoff path
// (#3798) rather than tripping the auth-failure counter (GH-3839).
func isAuthFetchError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return true
	}
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return false
	}
	// A non-rate-limited 403 reaches doRequest's generic fallback path
	// (client.go) as a plain "API error (status 403): ..." string rather
	// than a typed error, since only the rate-limited case gets RateLimitError.
	return strings.Contains(err.Error(), "status 403")
}

// recordAuthFailure increments the consecutive-auth-failure counter for a
// classified auth error and escalates once authFailureThreshold is reached:
// an ERROR log naming the token source, plus an alert via alertProcessor (if
// configured). Below threshold it logs at Warn so an isolated failure
// doesn't page anyone (GH-3839).
func (p *Poller) recordAuthFailure(err error) {
	n := p.consecutiveAuthFailures.Add(1)
	threshold := p.authFailureThreshold
	if threshold <= 0 {
		threshold = defaultAuthFailureThreshold
	}

	if int(n) < threshold {
		p.logger.Warn("github fetch auth error",
			slog.Int("consecutive_auth_failures", int(n)),
			slog.Any("error", err))
		return
	}

	p.logger.Error("github token appears invalid — consecutive auth failures reached threshold",
		slog.Int("consecutive_auth_failures", int(n)),
		slog.String("token_source", p.tokenSource),
		slog.Any("error", err))

	if p.alertProcessor != nil {
		p.alertProcessor.ProcessEvent(executor.AlertEvent{
			Type: executor.AlertEventTypeConfigError,
			Error: fmt.Sprintf("GitHub token invalid (source: %s) — %d consecutive auth failures",
				p.tokenSource, n),
			Metadata: map[string]string{
				"reason":               "github_auth_failure",
				"token_source":         p.tokenSource,
				"consecutive_failures": strconv.Itoa(int(n)),
			},
			Timestamp: time.Now(),
		})
	}
}

// resetAuthFailures clears the consecutive-auth-failure streak after a
// successful candidate fetch (GH-3839).
func (p *Poller) resetAuthFailures() {
	p.consecutiveAuthFailures.Store(0)
}

// repoKey returns "owner/repo" for use as the Prometheus `repo` label.
func (p *Poller) repoKey() string { return p.owner + "/" + p.repo }

// recordSkip increments the skip counter when pollerMetrics is configured.
func (p *Poller) recordSkip(reason string) {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerSkipped(p.repoKey(), reason)
	}
}

// recordDispatched increments the dispatch counter when pollerMetrics is configured.
func (p *Poller) recordDispatched() {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerDispatched(p.repoKey())
	}
}

// recordDeferredScopeOverlap increments the scope-overlap deferral counter when pollerMetrics is configured.
func (p *Poller) recordDeferredScopeOverlap() {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerDeferredScopeOverlap(p.repoKey())
	}
}

// syncBoardStatusInProgress moves the issue card to the configured in-progress status
// on the Projects V2 board. Called once after confirmed dispatch, before execution starts.
// Logs errors but does not fail the dispatch — board sync is best-effort.
// No-op when boardSync is nil or inProgressStatus is empty.
func (p *Poller) syncBoardStatusInProgress(ctx context.Context, issue *Issue) {
	if p.boardSync == nil || p.inProgressStatus == "" {
		return
	}

	nodeID := issue.NodeID
	if nodeID == "" {
		var err error
		nodeID, err = p.client.GetIssueNodeID(ctx, p.owner, p.repo, issue.Number)
		if err != nil {
			p.logger.Warn("board sync: failed to resolve issue node ID",
				slog.Int("issue", issue.Number),
				slog.Any("error", err))
			return
		}
	}

	if err := p.boardSync.UpdateProjectItemStatus(ctx, nodeID, p.inProgressStatus); err != nil {
		p.logger.Warn("board sync: failed to update project item status",
			slog.Int("issue", issue.Number),
			slog.String("status", p.inProgressStatus),
			slog.Any("error", err))
	}
}

// checkForNewIssues fetches issues and dispatches new ones concurrently (parallel mode)
func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.fetchCandidates(ctx)
	if err != nil {
		if isAuthFetchError(err) {
			p.recordAuthFailure(err)
		} else {
			p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		}
		return
	}
	p.resetAuthFailures()

	// Phase 1: Collect candidates eligible for dispatch
	var candidates []*Issue
	for _, issue := range issues {
		// Skip pull requests (GitHub Issues API returns both issues and PRs)
		if issue.PullRequest != nil {
			continue
		}

		// Skip if already in progress
		if HasLabel(issue, LabelInProgress) {
			p.recordSkip(skipreason.ReasonInProgress)
			continue
		}

		// GH-2402: Skip permanently-blocked issues. The user must remove
		// the pilot-blocked label to retry (e.g. after fixing a non-conventional title).
		if HasLabel(issue, LabelBlocked) {
			p.recordSkip(skipreason.ReasonBlocked)
			continue
		}

		// GH-2768: Skip issues declined as unactionable. Remove the label to re-enable dispatch.
		if HasLabel(issue, LabelNeedsClarification) {
			p.recordSkip(skipreason.ReasonNeedsClarification)
			continue
		}

		// GH-2402: Auto-close sub-issues whose parent epic already shipped.
		if p.skipSupersededByParent(ctx, issue) {
			p.recordSkip(skipreason.ReasonSuperseded)
			continue
		}

		// GH-2176: Auto-retry issues stuck with pilot-failed (no pilot-done)
		if HasLabel(issue, LabelFailed) {
			if !p.shouldRetryFailedIssue(ctx, issue) {
				p.recordSkip(skipreason.ReasonFailedSkip)
				continue
			}
			// Label removed, fall through to candidate selection
		}

		// GH-2276: Auto-retry issues with pilot-retry-ready (PR closed without merge)
		if HasLabel(issue, LabelRetryReady) {
			if !p.shouldRetryRetryReadyIssue(ctx, issue) {
				p.recordSkip(skipreason.ReasonRetryReadySkip)
				continue
			}
			// Label removed, fall through to candidate selection
		}

		// Skip and mark done issues as permanently processed
		if HasLabel(issue, LabelDone) {
			p.markProcessed(issue.Number)
			p.recordSkip(skipreason.ReasonDone)
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
				p.recordSkip(skipreason.ReasonProcessedGrace)
				continue
			}

			// GH-2201: Check if task is still queued/in-progress
			if p.taskChecker != nil {
				taskID := fmt.Sprintf("GH-%d", issue.Number)
				if p.taskChecker.IsTaskQueued(taskID) {
					p.logger.Debug("Issue still queued/in-progress, skipping retry",
						slog.Int("number", issue.Number),
						slog.String("task_id", taskID))
					p.recordSkip(skipreason.ReasonTaskQueued)
					continue
				}
			}

			p.logger.Info("Issue was processed but status labels removed, allowing retry",
				slog.Int("number", issue.Number))
			p.mu.Lock()
			delete(p.processed, issue.Number)
			p.mu.Unlock()
			if p.processedStore != nil {
				if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(issue.Number)); err != nil {
					p.logger.Warn("Failed to unmark issue in store",
						slog.Int("number", issue.Number),
						slog.Any("error", err))
				}
			}

			// GH-1983: Before retrying, check if merged PRs already exist
			if p.hasMergedWork(ctx, issue) {
				p.recordSkip(skipreason.ReasonHasMergedWork)
				continue
			}

			// TASK-341: skip a re-dispatch whose pilot/GH-N PR is still OPEN (parallel
			// mode). Mirrors the sequential guard — the open PR is awaiting merge
			// (pilot-done/close deferred per GH-3139/TASK-301), so re-dispatch would
			// only no-op ("no new commit produced") and used to be mislabeled
			// pilot-blocked. Re-mark so the grace window throttles re-checks; do NOT
			// label — leave the open PR for the autopilot merge flow.
			if p.hasOpenPRAwaitingMerge(ctx, issue) {
				p.markProcessed(issue.Number)
				p.recordSkip(skipreason.ReasonHasOpenPR)
				continue
			}
		}

		// GH-3269 / TASK-321 PR-4: Fresh candidates (never processed / post-unmark)
		// bypass the retry block above, so apply the merged-work guard
		// unconditionally for them — mirrors the sequential
		// findOldestUnprocessedIssue guard and prevents phantom
		// "no new commit produced" redispatch in parallel mode.
		if !processed && p.hasMergedWork(ctx, issue) {
			p.recordSkip(skipreason.ReasonHasMergedWork)
			continue
		}

		// Skip issues with pending dependencies
		if p.hasPendingDependencies(issue, issues) {
			p.logger.Debug("Skipping issue with pending dependencies in parallel mode",
				slog.Int("number", issue.Number),
			)
			p.recordSkip(skipreason.ReasonPendingDependency)
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
				p.recordSkip(skipreason.ReasonCompletedExecution)
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
				p.recordDeferredScopeOverlap()
			}
		}
	}

	// Phase 3: Dispatch selected issues
	for _, issue := range toDispatch {
		// GH-2341: Refresh labels via single-issue GET to bypass stale ListIssues snapshot.
		// If pilot-done or pilot-in-progress was added after the list was fetched,
		// the snapshot's Labels will not reflect it — fetch fresh state.
		if fresh, ferr := p.client.GetIssue(ctx, p.owner, p.repo, issue.Number); ferr == nil && fresh != nil {
			if HasLabel(fresh, LabelDone) || HasLabel(fresh, LabelInProgress) {
				p.logger.Info("Skipping dispatch — fresh labels show issue already handled",
					slog.Int("number", issue.Number),
					slog.Bool("done", HasLabel(fresh, LabelDone)),
					slog.Bool("in_progress", HasLabel(fresh, LabelInProgress)),
				)
				p.markProcessed(issue.Number)
				p.recordSkip(skipreason.ReasonFreshLabelCheck)
				continue
			}
		} else if ferr != nil {
			p.logger.Debug("Failed to refresh issue labels before dispatch — proceeding with snapshot",
				slog.Int("number", issue.Number),
				slog.Any("error", ferr),
			)
		}

		// GH-2802: Pre-flight judge — evaluate issue quality before burning a worker slot.
		if p.preFlightJudge != nil {
			verdict, pfErr := p.preFlightJudge.JudgeIssue(ctx, issue.Title, issue.Body, "")
			if pfErr != nil {
				p.logger.Warn("pre-flight judge error (fail-open)",
					slog.Int("issue", issue.Number),
					slog.Any("error", pfErr))
			} else if !verdict.Accepted {
				p.logger.Info("pre-flight rejected issue",
					slog.Int("issue", issue.Number),
					slog.String("decision", verdict.Decision),
					slog.String("reason", verdict.Reason))
				p.handlePreFlightReject(ctx, issue, verdict)
				p.recordSkip(skipreason.ReasonPreFlightReject)
				continue // skip markProcessed so label removal re-triggers dispatch
			}
		}

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(issue.Number)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.recordDispatched()
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
		logging.SafeGo("github-poller", func() {
			defer p.activeWg.Done()
			defer func() { <-p.semaphore }() // release slot

			// TASK-354: mark in-flight for the whole execution (incl. label
			// finalization) so the stranded-issue sweep never touches a live run.
			p.markInFlight(issue.Number)
			defer p.unmarkInFlight(issue.Number)

			// Board sync: move card to in-progress on confirmed dispatch (GH-3252).
			p.syncBoardStatusInProgress(ctx, issue)

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

				// GH-2176: Unmark if execution failed without creating a PR (unless permanent).
				// GH-3270: Permanent/no-op failures already carry pilot-blocked; retaining the
				// durable row is defense-in-depth so a daemon restart cannot re-dispatch until
				// the human removes pilot-blocked (which clears the mark via the retry path).
				if result != nil && !result.Success && result.PRNumber == 0 {
					if result.Error != nil && executor.IsPermanentFailure(result.Error.Error()) {
						p.logger.Info("Permanent failure — retaining adapter_processed marker",
							slog.Int("number", issue.Number),
							slog.String("error", result.Error.Error()),
						)
					} else {
						p.logger.Info("Execution failed without PR, unmarking for retry",
							slog.Int("number", issue.Number),
						)
						p.unmarkProcessed(issue.Number)
					}
				}

				// Diagnostic: surface why OnPRCreated may not fire (GH-2999 Phase 1)
				if result == nil {
					p.logger.Info("OnPRCreated skipped: result is nil",
						slog.Int("issue_number", issue.Number),
					)
				} else if result.PRNumber == 0 {
					p.logger.Info("OnPRCreated skipped: PRNumber=0",
						slog.Int("issue_number", issue.Number),
						slog.String("pr_url", result.PRURL),
						slog.String("branch", result.BranchName),
						slog.String("head_sha", result.HeadSHA),
					)
				} else if p.OnPRCreated == nil {
					// GH-3784: a real PR exists but no autopilot controller is wired to
					// track it — it will never auto-merge unless the periodic orphan-PR
					// reconciler (or a human) picks it up. Fail loud, not Info.
					p.logger.Warn("OnPRCreated skipped: PR created but no autopilot callback wired",
						slog.Int("pr_number", result.PRNumber),
						slog.Int("issue_number", issue.Number),
						slog.String("branch", result.BranchName),
					)
				}
				// Gate: PRNumber > 0 implies executor surfaced a valid PR URL via runner.go:3151. Empty PRUrl (no-commits guard, push-fail, title-rejection) leaves PRNumber=0 and we silently skip — see TASK-60 for the upstream chain.
				// Notify autopilot controller of new PR
				// Gate: PRNumber > 0 implies executor surfaced a valid PR URL via runner.go:3151. Empty PRUrl (no-commits guard, push-fail, title-rejection) leaves PRNumber=0 and we silently skip — see TASK-60 for the upstream chain.
				if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
					p.logger.Info("Notifying autopilot of PR creation (parallel path)",
						slog.Int("pr_number", result.PRNumber),
						slog.Int("issue_number", issue.Number),
						slog.String("branch", result.BranchName),
					)
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
		})
	}
}

// markProcessed marks an issue as processed with the current timestamp
func (p *Poller) markProcessed(number int) {
	p.mu.Lock()
	p.processed[number] = time.Now()
	p.mu.Unlock()

	// Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.Mark("github", p.repoKey(), strconv.Itoa(number)); err != nil {
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
		if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(number)); err != nil {
			p.logger.Warn("Failed to unmark processed issue", slog.Int("issue", number), slog.Any("error", err))
		}
	}
}

// handlePreFlightReject handles an issue rejected by the pre-flight judge (GH-2802):
//   - adds pilot-needs-clarification label so the issue is filtered on subsequent polls
//   - posts a comment explaining the decision and how to re-trigger
//   - saves a declined-preflight execution record if an ExecutionSaver is wired
//
// The caller must NOT call markProcessed after this so that label removal re-triggers dispatch.
func (p *Poller) handlePreFlightReject(ctx context.Context, issue *Issue, verdict Verdict) {
	taskID := fmt.Sprintf("GH-%d", issue.Number)

	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelNeedsClarification}); err != nil {
		p.logger.Warn("pre-flight: failed to add needs-clarification label",
			slog.Int("issue", issue.Number),
			slog.Any("error", err))
	}

	comment := fmt.Sprintf(
		"**Pre-flight check declined this issue.**\n\n"+
			"**Decision:** `%s`\n"+
			"**Reason:** %s\n"+
			"**Confidence:** %.0f%%\n\n"+
			"To re-trigger: edit the issue to address the above, then remove the `%s` label.",
		verdict.Decision, verdict.Reason, verdict.Confidence*100, LabelNeedsClarification,
	)
	if _, err := p.client.AddComment(ctx, p.owner, p.repo, issue.Number, comment); err != nil {
		p.logger.Warn("pre-flight: failed to post rejection comment",
			slog.Int("issue", issue.Number),
			slog.Any("error", err))
	}

	if p.execSaver != nil {
		if err := p.execSaver.SaveDeclinedExecution(taskID, p.projectPath, "declined-preflight", verdict.Reason); err != nil {
			p.logger.Warn("pre-flight: failed to save execution record",
				slog.Int("issue", issue.Number),
				slog.Any("error", err))
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

// MarkProcessed marks an issue as processed so findOldestUnprocessedIssue skips it.
// Called by the executor after epic sub-issues are created to prevent re-dispatch (GH-3240).
func (p *Poller) MarkProcessed(number int) {
	p.markProcessed(number)
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
		if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(number)); err != nil {
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
		// Fall through to branch lookup below — don't block on Search API errors alone.
	}

	// GH-2341: Search API has up to ~30s indexing lag. Supplement with a direct REST
	// lookup by branch (strongly consistent) to catch just-merged PRs on pilot/GH-N.
	if !found {
		branch := fmt.Sprintf("pilot/GH-%d", issue.Number)
		branchFound, berr := p.client.FindMergedPRByBranch(ctx, p.owner, p.repo, branch)
		if berr != nil {
			p.logger.Warn("Failed to check merged PRs by branch",
				slog.Int("issue", issue.Number),
				slog.String("branch", branch),
				slog.Any("error", berr),
			)
			return false
		}
		if !branchFound {
			// GH-3420 / TASK-359 Layer 3b: DB fallback — no Search API indexing lag.
			// A completed execution row with a deliverable (commit_sha or pr_url) is
			// strong evidence work shipped, even when the ~30s Search window is open.
			// Skip for pilot-retry-ready: that label means the prior execution's PR was
			// closed without merge, so the completed row is stale. shouldRetryRetryReadyIssue
			// invalidates it explicitly before re-dispatch.
			if p.execChecker != nil && !HasLabel(issue, LabelRetryReady) {
				taskID := fmt.Sprintf("GH-%d", issue.Number)
				if completed, cerr := p.execChecker.HasCompletedExecution(taskID, p.projectPath); cerr == nil && completed {
					p.logger.Debug("hasMergedWork: DB fallback hit — execution previously completed",
						slog.String("task_id", taskID))
					// fall through to mark-as-done below
				} else {
					return false
				}
			} else {
				return false
			}
		}
		p.logger.Info("Merged PR found via branch lookup (Search API lag)",
			slog.Int("issue", issue.Number),
			slog.String("branch", branch),
		)
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
	// GH-4021: A pilot-retry-* label from an earlier PR-closed-without-merge
	// cycle must not survive a later poller-detected merge, or it arms a
	// redundant auto-retry against already-shipped work.
	for _, label := range []string{LabelRetryReady, LabelRetry1, LabelRetry2, LabelRetryExhausted} {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, label); err != nil {
			p.logger.Debug("retry label cleanup", slog.Int("issue", issue.Number), slog.String("label", label), slog.Any("error", err))
		}
	}
	p.markProcessed(issue.Number)
	return true
}

// hasOpenPRAwaitingMerge reports whether an OPEN pilot PR already exists for the
// issue. Unlike hasMergedWork it is read-only — it mutates no labels and marks
// nothing processed, because the awaiting-merge state is transient (the PR will
// merge → hasMergedWork closes it, or close-without-merge → the retry-ready path
// re-picks it). TASK-321/TASK-341: skip the redundant re-dispatch that would
// otherwise produce a "no new commit produced" no-op during the
// PR-created-but-not-yet-merged window. Branch lookup is strongly consistent
// (no Search API lag), which matters here because the PR may have been created
// only one poll cycle earlier.
func (p *Poller) hasOpenPRAwaitingMerge(ctx context.Context, issue *Issue) bool {
	branch := fmt.Sprintf("pilot/GH-%d", issue.Number)
	found, err := p.client.FindOpenPRByBranch(ctx, p.owner, p.repo, branch)
	if err != nil {
		p.logger.Warn("Failed to check for open PRs by branch",
			slog.Int("issue", issue.Number),
			slog.String("branch", branch),
			slog.Any("error", err),
		)
		return false
	}
	if found {
		p.logger.Info("Issue has an open PR awaiting merge",
			slog.Int("issue", issue.Number),
			slog.String("branch", branch),
		)
	}
	return found
}

// shouldRetryFailedIssue checks if a pilot-failed issue should be auto-retried.
// Returns true if the issue should be retried (label removed), false if it should be skipped.
// GH-2176: Issues stuck with pilot-failed get retried up to maxFailedRetries times.
//
// GH-3715: Retry counter is persisted via GitHub labels (pilot-failed-retry-1,
// -2, -exhausted) so the count survives `pilot start` restarts. The previous
// in-memory failedRetryCount map silently reset on restart, allowing a
// persistently failing issue to retry indefinitely across restarts.
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

	// GH-2363: Title-guard escalation explicitly halted retries. A human must
	// edit the title and remove pilot-title-rejected before we try again.
	if HasLabel(issue, LabelTitleRejected) {
		p.logger.Info("Skipping retry — pilot-title-rejected set (GH-2363)",
			slog.Int("number", issue.Number),
		)
		return false
	}

	// GH-3715: terminal state — exhausted retries never retry again, even
	// across daemon restarts.
	if HasLabel(issue, LabelFailedRetryExhausted) {
		p.logger.Warn("Issue is pilot-failed-retry-exhausted, skipping",
			slog.Int("number", issue.Number),
		)
		return false
	}

	// GH-3787: check shipped-ness BEFORE any retry-counter accounting,
	// including the escalate-to-exhausted path below. A restart-noise streak
	// (stale-task reap + preflight blip) can burn through the retry budget
	// on an issue whose deliverable already merged — hasMergedWork closes it
	// as done instead of letting it get parked as pilot-failed-retry-exhausted.
	if p.hasMergedWork(ctx, issue) {
		return false
	}

	// Determine the next retry-counter label based on current state.
	var currentRetryLabel, nextRetryLabel string
	switch {
	case HasLabel(issue, LabelFailedRetry2):
		currentRetryLabel = LabelFailedRetry2
		nextRetryLabel = LabelFailedRetryExhausted
	case HasLabel(issue, LabelFailedRetry1):
		currentRetryLabel = LabelFailedRetry1
		nextRetryLabel = LabelFailedRetry2
	default:
		nextRetryLabel = LabelFailedRetry1
	}

	// If escalating to exhausted, mark the label and skip dispatch.
	if nextRetryLabel == LabelFailedRetryExhausted {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, currentRetryLabel); err != nil {
			p.logger.Warn("Failed to remove prior failed-retry label",
				slog.Int("number", issue.Number),
				slog.String("label", currentRetryLabel),
				slog.Any("error", err),
			)
		}
		if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelFailedRetryExhausted}); err != nil {
			p.logger.Warn("Failed to add pilot-failed-retry-exhausted label",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
		}
		p.logger.Warn("Issue exhausted failed-retry budget — escalated to pilot-failed-retry-exhausted",
			slog.Int("number", issue.Number),
		)
		return false
	}

	// Remove pilot-failed label so the poller doesn't loop the same issue.
	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelFailed); err != nil {
		p.logger.Warn("Failed to remove pilot-failed label for retry",
			slog.Int("number", issue.Number),
			slog.Any("error", err),
		)
		return false
	}

	// Swap retry-N label: remove current (if any), add next.
	if currentRetryLabel != "" {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, currentRetryLabel); err != nil {
			p.logger.Warn("Failed to remove prior failed-retry label",
				slog.Int("number", issue.Number),
				slog.String("label", currentRetryLabel),
				slog.Any("error", err),
			)
		}
	}
	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{nextRetryLabel}); err != nil {
		p.logger.Warn("Failed to add failed-retry label",
			slog.Int("number", issue.Number),
			slog.String("label", nextRetryLabel),
			slog.Any("error", err),
		)
		// Continue anyway; we'd rather retry than block on a label flake.
	}

	// Keep the legacy in-memory counter in sync so existing tests/state observers
	// remain consistent. GH-3715: this is now a mirror, not the source of truth.
	p.mu.Lock()
	p.failedRetryCount[issue.Number]++
	p.mu.Unlock()

	// Clear from processed map so the issue can be re-picked
	p.ClearProcessed(issue.Number)

	p.logger.Info("Auto-retrying pilot-failed issue",
		slog.Int("number", issue.Number),
		slog.String("retry_label", nextRetryLabel),
	)

	return true
}

// shouldRetryRetryReadyIssue checks if a pilot-retry-ready issue should be auto-retried.
// Returns true if the issue should be retried (label removed), false if it should be skipped.
// GH-2276: Issues with pilot-retry-ready (PR closed without merge) get retried up to maxRetryReadyRetries times.
//
// GH-2432: Retry counter is persisted via GitHub labels (pilot-retry-1, -2,
// -exhausted) so the count survives `pilot start` restarts. The previous
// in-memory map silently reset on restart, allowing pathological issues to
// consume Opus indefinitely.
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

	// GH-2432: terminal state — exhausted retries never retry again.
	if HasLabel(issue, LabelRetryExhausted) {
		p.logger.Warn("Issue is pilot-retry-exhausted, skipping",
			slog.Int("number", issue.Number),
		)
		return false
	}

	// Determine the next retry-counter label based on current state.
	var currentRetryLabel, nextRetryLabel string
	switch {
	case HasLabel(issue, LabelRetry2):
		currentRetryLabel = LabelRetry2
		nextRetryLabel = LabelRetryExhausted
	case HasLabel(issue, LabelRetry1):
		currentRetryLabel = LabelRetry1
		nextRetryLabel = LabelRetry2
	default:
		nextRetryLabel = LabelRetry1
	}

	// If escalating to exhausted, mark the label and skip dispatch.
	if nextRetryLabel == LabelRetryExhausted {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, currentRetryLabel); err != nil {
			p.logger.Warn("Failed to remove prior retry label",
				slog.Int("number", issue.Number),
				slog.String("label", currentRetryLabel),
				slog.Any("error", err),
			)
		}
		if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelRetryExhausted}); err != nil {
			p.logger.Warn("Failed to add pilot-retry-exhausted label",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
		}
		// Also clear pilot-retry-ready so the poller doesn't keep finding it.
		_ = p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelRetryReady)
		p.logger.Warn("Issue exhausted retry budget — escalated to pilot-retry-exhausted",
			slog.Int("number", issue.Number),
		)
		return false
	}

	// Check if merged work already exists before retrying
	if p.hasMergedWork(ctx, issue) {
		return false
	}

	// GH-4021: Mirror the normal-dispatch hasOpenPRAwaitingMerge guard here
	// too. hasMergedWork's DB fallback above is skipped whenever
	// LabelRetryReady is set, on the assumption that any completed row
	// surviving under that label is stale from the PR-closed-without-merge
	// event that set it — true once notifyExternalClose (GH-3818/D10)
	// reclassifies that row to "failed", but there's a window between a
	// later retry's execution completing (PR created, row completed) and
	// its PR actually merging where hasMergedWork sees neither a merged PR
	// nor (thanks to the label-based skip) the completed row. GH-3992: the
	// auto-retry fired in exactly that window — PR #4018 was open, not yet
	// merged — and dispatched a redundant third run. An open PR is
	// unambiguous evidence work is already in flight regardless of label
	// staleness, so check it before invalidating anything.
	if p.hasOpenPRAwaitingMerge(ctx, issue) {
		// Mirror the normal path (markProcessed, don't remove the label):
		// the PR will either merge (hasMergedWork closes the issue and
		// clears retry labels) or close without merging (re-arms this same
		// retry-ready path next poll). Throttles re-checks in the meantime.
		p.markProcessed(issue.Number)
		return false
	}

	// Swap retry-N label: remove current (if any), add next.
	if currentRetryLabel != "" {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, currentRetryLabel); err != nil {
			p.logger.Warn("Failed to remove prior retry label",
				slog.Int("number", issue.Number),
				slog.String("label", currentRetryLabel),
				slog.Any("error", err),
			)
		}
	}
	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{nextRetryLabel}); err != nil {
		p.logger.Warn("Failed to add retry label",
			slog.Int("number", issue.Number),
			slog.String("label", nextRetryLabel),
			slog.Any("error", err),
		)
		// Continue anyway; we'd rather retry than block on a label flake.
	}

	// Remove pilot-retry-ready label so the poller doesn't loop the same issue.
	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelRetryReady); err != nil {
		p.logger.Warn("Failed to remove pilot-retry-ready label for retry",
			slog.Int("number", issue.Number),
			slog.Any("error", err),
		)
		return false
	}

	// Keep the legacy in-memory counter in sync so existing tests/state observers
	// remain consistent. GH-2432: this is now a mirror, not the source of truth.
	p.mu.Lock()
	p.retryReadyCount[issue.Number]++
	p.mu.Unlock()

	// GH-3418: Delete any stale completed execution row so HasCompletedExecution
	// doesn't cause the re-dispatch to silently no-op. Must happen before
	// ClearProcessed so the issue can actually reach the dispatch gate.
	if p.execChecker != nil {
		taskID := fmt.Sprintf("GH-%d", issue.Number)
		if err := p.execChecker.InvalidateCompletion(taskID, p.projectPath); err != nil {
			p.logger.Warn("InvalidateCompletion failed on retry-ready re-dispatch — proceeding",
				slog.String("task_id", taskID),
				slog.Any("error", err),
			)
		}
	}

	// Clear from processed map so the issue can be re-picked
	p.ClearProcessed(issue.Number)

	p.logger.Info("Auto-retrying pilot-retry-ready issue",
		slog.Int("number", issue.Number),
		slog.String("retry_label", nextRetryLabel),
	)

	return true
}

// skipSupersededByParent auto-closes a sub-issue whose parent epic has already
// shipped (closed AND has pilot-done). Returns true if the issue was closed
// and should be skipped from dispatch. GH-2402.
//
// On transient API errors (e.g. failed parent lookup) the function returns
// false so the issue falls through to normal dispatch — we never want to lose
// work because of a flaky GET.
func (p *Poller) skipSupersededByParent(ctx context.Context, issue *Issue) bool {
	parentNum := ParseParentIssueNumber(issue.Body)
	if parentNum <= 0 {
		return false
	}

	parent, err := p.client.GetIssue(ctx, p.owner, p.repo, parentNum)
	if err != nil {
		p.logger.Warn("Failed to fetch parent issue, falling through to normal dispatch",
			slog.Int("issue", issue.Number),
			slog.Int("parent", parentNum),
			slog.Any("error", err),
		)
		return false
	}

	// Parent must be both closed AND marked done. A closed-without-done parent
	// (e.g. user closed the epic manually before Pilot finished) shouldn't
	// strand sub-issues.
	if parent.State != StateClosed || !HasLabel(parent, LabelDone) {
		return false
	}

	// GH-3513 incident: pilot-done on the parent proves only that the parent
	// ISSUE was closed, not that this child's work merged — premature parent
	// closes (partial native sub-issue links) otherwise cascade into discarding
	// live work. An open pilot/GH-N PR for this child is direct evidence its
	// implementation is still in flight; never supersede over it. Fail open on
	// lookup errors for the same reason as the parent GET above.
	branch := fmt.Sprintf("pilot/GH-%d", issue.Number)
	if openPR, perr := p.client.FindOpenPRByBranch(ctx, p.owner, p.repo, branch); perr != nil {
		p.logger.Warn("Failed to check for open PR before superseding, falling through to normal dispatch",
			slog.Int("issue", issue.Number),
			slog.String("branch", branch),
			slog.Any("error", perr),
		)
		return false
	} else if openPR {
		p.logger.Info("Not superseding sub-issue: open PR exists for its branch",
			slog.Int("issue", issue.Number),
			slog.Int("parent", parentNum),
			slog.String("branch", branch),
		)
		return false
	}

	// GH-3513 wave 2 (#3537): the open-PR veto above cannot protect a child
	// that has not created its PR yet. Require POSITIVE evidence this child's
	// own slice shipped — a merged PR on its branch or a completed execution
	// row — before discarding it. A prematurely-closed parent must never
	// cascade into closing children whose work never landed. Fail open on
	// lookup errors (dispatch rather than lose work).
	merged, merr := p.client.FindMergedPRByBranch(ctx, p.owner, p.repo, branch)
	if merr != nil {
		p.logger.Warn("Failed to check for merged PR before superseding, falling through to normal dispatch",
			slog.Int("issue", issue.Number),
			slog.String("branch", branch),
			slog.Any("error", merr),
		)
		return false
	}
	completed := false
	if !merged && p.execChecker != nil {
		taskID := fmt.Sprintf("GH-%d", issue.Number)
		if c, cerr := p.execChecker.HasCompletedExecution(taskID, p.projectPath); cerr == nil {
			completed = c
		}
	}
	if !merged && !completed {
		p.logger.Info("Not superseding sub-issue: parent is done but child has no evidence of shipped work — dispatching",
			slog.Int("issue", issue.Number),
			slog.Int("parent", parentNum),
			slog.String("branch", branch),
		)
		return false
	}

	p.logger.Info("Auto-closing sub-issue: parent epic already shipped",
		slog.Int("issue", issue.Number),
		slog.Int("parent", parentNum),
	)

	comment := fmt.Sprintf(
		"🔁 Auto-closed by Pilot: parent epic #%d already shipped this work. "+
			"This sub-issue is redundant.",
		parentNum,
	)
	if _, cerr := p.client.AddComment(ctx, p.owner, p.repo, issue.Number, comment); cerr != nil {
		p.logger.Warn("Failed to post superseded comment",
			slog.Int("issue", issue.Number),
			slog.Any("error", cerr),
		)
	}
	if lerr := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelSuperseded}); lerr != nil {
		p.logger.Warn("Failed to add pilot-superseded label",
			slog.Int("issue", issue.Number),
			slog.Any("error", lerr),
		)
	}
	if uerr := p.client.UpdateIssueState(ctx, p.owner, p.repo, issue.Number, StateClosed); uerr != nil {
		p.logger.Warn("Failed to close superseded sub-issue",
			slog.Int("issue", issue.Number),
			slog.Any("error", uerr),
		)
	}

	p.markProcessed(issue.Number)
	return true
}

// fetchedIssueNumbers builds a set of issue numbers from the candidate list
// returned by fetchCandidates for the current poll cycle.
func fetchedIssueNumbers(fetched []*Issue) map[int]struct{} {
	set := make(map[int]struct{}, len(fetched))
	for _, iss := range fetched {
		set[iss.Number] = struct{}{}
	}
	return set
}

// hasPendingDependencies reports whether any of issue's Blocked-by/Depends-on
// references resolve to an open blocker, checked against fetched — the same
// candidate slice fetchCandidates already retrieved this poll cycle (the full
// open, pilot-labeled issue set) — instead of a per-blocker p.client.GetIssue
// call (GH-3789). The old per-blocker lookup deadlocked the stress/ suite
// across four attempts (PRs #3802/#3822/#3824/#3835): its fake GitHub servers
// only serve the issues-list route, so any per-blocker GET burned the full
// retry/backoff timeout. Resolving in-memory against the existing fetch adds
// zero new API calls and is stress-safe by construction.
//
// A blocker present in fetched is open and blocking. A blocker absent
// (closed, unlabeled, or nonexistent) is treated as not-blocking — a
// deliberate fail-open change from the old fail-closed-on-API-error
// behavior: fetched is the full open+pilot-labeled set, so absence
// overwhelmingly means the blocker already shipped or was never applicable,
// and fail-closed would permanently wedge issues behind a blocker that will
// never reappear in that fetch.
func (p *Poller) hasPendingDependencies(issue *Issue, fetched []*Issue) bool {
	// Sanitize before parsing so an attacker cannot smuggle fake dependency
	// references (e.g. an invisible "#1337" that would block execution).
	deps := ParseDependencies(text.SanitizeUntrustedString(issue.Body))
	if len(deps) == 0 {
		return false
	}

	open := fetchedIssueNumbers(fetched)
	for _, depNum := range deps {
		if _, present := open[depNum]; present {
			p.logger.Debug("Issue has open dependency in fetched candidates, skipping",
				slog.Int("issue", issue.Number),
				slog.Int("dependency", depNum),
			)
			return true
		}
	}

	return false
}
