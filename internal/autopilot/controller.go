package autopilot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/memory"
)

// approvalPersister is the subset of memory.Store used for approval persistence
// in the executions table.
type approvalPersister interface {
	SetApprovalRequestID(ctx context.Context, taskID, requestID string) error
	SetApprovalDecision(ctx context.Context, requestID, decision, by string) error
}

// projectBoardSyncer abstracts GitHub Projects V2 board status updates.
// *github.ProjectBoardSync implements this interface; tests substitute a mock.
type projectBoardSyncer interface {
	UpdateProjectItemStatus(ctx context.Context, issueNodeID string, statusName string) error
}

// iterationRe matches the iteration field in autopilot-meta comments.
var iterationRe = regexp.MustCompile(`<!-- autopilot-meta.*?iteration:(\d+).*?-->`)

// buildMergeCompletionComment creates a success comment to post on an issue
// after its associated PR is merged. This ensures the last comment on the issue
// is a success message rather than a stale failure comment from a prior attempt.
func buildMergeCompletionComment(prState *PRState) string {
	var sb strings.Builder
	sb.WriteString("✅ PR merged successfully!\n\n")
	sb.WriteString("| Metric | Value |\n")
	sb.WriteString("|--------|-------|\n")
	sb.WriteString(fmt.Sprintf("| PR | #%d |\n", prState.PRNumber))
	sb.WriteString(fmt.Sprintf("| Branch | `%s` |\n", prState.BranchName))
	if !prState.CreatedAt.IsZero() {
		duration := time.Since(prState.CreatedAt).Round(time.Second)
		sb.WriteString(fmt.Sprintf("| Time to merge | %s |\n", duration))
	}
	return sb.String()
}

// parseAutopilotIteration extracts the CI fix iteration counter from an issue body.
// Returns 0 if no iteration metadata is found (i.e., the issue is not a fix issue).
func parseAutopilotIteration(body string) int {
	if m := iterationRe.FindStringSubmatch(body); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// prFailureState tracks per-PR circuit breaker state.
// Each PR has independent failure tracking so one bad PR doesn't block others.
type prFailureState struct {
	FailureCount    int       // Number of consecutive failures for this PR
	LastFailureTime time.Time // When the last failure occurred (for timeout reset)
}

// Notifier sends autopilot notifications for PR lifecycle events.
type Notifier interface {
	// NotifyMerged sends notification when a PR is successfully merged.
	NotifyMerged(ctx context.Context, prState *PRState) error
	// NotifyCIFailed sends notification when CI checks fail.
	NotifyCIFailed(ctx context.Context, prState *PRState, failedChecks []string) error
	// NotifyApprovalRequired sends notification when a PR requires human approval.
	NotifyApprovalRequired(ctx context.Context, prState *PRState) error
	// NotifyFixIssueCreated sends notification when a fix issue is auto-created.
	NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error
	// NotifyReleased sends notification when a release is created.
	NotifyReleased(ctx context.Context, prState *PRState, releaseURL string) error
}

// ReleaseNotifier extends Notifier with release notifications.
type ReleaseNotifier interface {
	Notifier
	// NotifyReleased sends notification when a release is created.
	NotifyReleased(ctx context.Context, prState *PRState, releaseURL string) error
}

// TaskMonitor allows autopilot to update task display state.
// GH-1336: Sync monitor state when autopilot merges PR so dashboard shows correct status.
type TaskMonitor interface {
	Complete(taskID, prURL string)
}

// EvalStore persists eval tasks extracted from merged PRs.
type EvalStore interface {
	SaveEvalTask(task *memory.EvalTask) error
	// UpdateExecutionStatusByTaskID updates execution status by task ID and project path.
	// Used to mark failed executions as completed when the PR is merged.
	UpdateExecutionStatusByTaskID(taskID, projectPath, status string) error
	// SelfHealExecutionAfterMerge promotes failed rows to completed and
	// stamps the PR URL after a successful merge. projectPath scopes the update
	// to prevent cross-repo clobbering. GH-2402.
	SelfHealExecutionAfterMerge(taskID, projectPath, prURL string) error
}

// ControllerOption is a functional option for Controller configuration.
type ControllerOption func(*Controller)

// WithProjectBoardSync wires a GitHub Projects V2 board sync into the controller.
// doneStatus: merged PRs; failStatus: CI/exec failures; reviewStatus: PR created (In Progress → Review);
// inProgressStatus: reserved for future use (wired for symmetry, not yet emitted).
func WithProjectBoardSync(bs *github.ProjectBoardSync, doneStatus, failStatus, reviewStatus, inProgressStatus string) ControllerOption {
	return func(c *Controller) {
		c.boardSync = bs
		c.doneStatus = doneStatus
		c.failStatus = failStatus
		c.reviewStatus = reviewStatus
		c.inProgressStatus = inProgressStatus
	}
}

// WithMemoryStore wires an execution-level approval persister so that
// approval_request_id and approval_decision are written to the executions table.
func WithMemoryStore(s *memory.Store) ControllerOption {
	return func(c *Controller) {
		c.memoryStore = s
	}
}

// WithProjectPath sets the filesystem project path used to scope execution
// self-heal (SelfHealExecutionAfterMerge) to this project's rows. It MUST match
// the value the executor stored in executions.project_path — an absolute fs path
// (e.g. /Users/me/proj), NOT owner/repo. Empty falls back to task_id-only match.
// TASK-352.
func WithProjectPath(path string) ControllerOption {
	return func(c *Controller) {
		c.projectPath = path
	}
}

// Controller orchestrates the autopilot loop for PR processing.
// It manages the state machine: PR created → CI check → merge → post-merge CI → feedback loop.
type Controller struct {
	config           *Config
	ghClient         *github.Client
	approvalMgr      *approval.Manager
	ciMonitor        *CIMonitor
	autoMerger       *AutoMerger
	feedbackLoop     *FeedbackLoop
	releaser         *Releaser
	deployer         *Deployer
	notifier         Notifier
	monitor          TaskMonitor // GH-1336: sync dashboard state on merge
	boardSync        projectBoardSyncer
	doneStatus       string
	failStatus       string
	reviewStatus     string // GH-3260: board column for PR-created (In Progress → Review)
	inProgressStatus string // GH-3260: reserved for symmetry; not yet emitted
	log              *slog.Logger

	// State tracking
	activePRs map[int]*PRState
	mu        sync.RWMutex

	// Merge-metric idempotency: tracks PR numbers we've already recorded
	// merge-success metrics for, so handleMerging + ScanRecentlyMergedPRs
	// can both call recordMergeSuccess without double-counting.
	recordedMerges map[int]bool

	// Persistent state store (optional, nil = in-memory only)
	stateStore *StateStore

	// Learning loop for capturing review feedback (optional, nil = learning disabled)
	learningLoop *memory.LearningLoop

	// Eval store for capturing eval tasks from merged PRs (optional, nil = eval disabled)
	evalStore EvalStore

	// Execution-level approval persistence (optional, nil = audit trail disabled)
	memoryStore approvalPersister

	// Per-PR circuit breaker: each PR has independent failure tracking.
	// A failure on one PR does not block other PRs.
	prFailures map[int]*prFailureState

	// Deadlock detection (GH-849): track last time any PR made progress.
	// If no state transitions occur for 1h, fire a deadlock alert.
	lastProgressAt    time.Time
	deadlockAlertSent bool

	// Release summary generator (optional, nil = no LLM enrichment)
	releaseSummary *ReleaseSummaryGenerator

	// Metrics
	metrics *Metrics

	// Owner and repo for GitHub operations
	owner string
	repo  string

	// projectPath is the absolute filesystem path the executor stored in
	// executions.project_path. Used to scope self-heal to this project's rows.
	// Empty = match by task_id only (single-repo / tests). TASK-352.
	projectPath string

	// GH-3271: called after a PR merges and pilot-done is applied so pollers
	// can immediately re-mark the issue as processed, closing the merge→done
	// race window before label propagation catches up.
	onIssueDone func(issueNumber int)
}

// NewController creates an autopilot controller with all required components.
func NewController(cfg *Config, ghClient *github.Client, approvalMgr *approval.Manager, owner, repo string, opts ...ControllerOption) *Controller {
	c := &Controller{
		config:         cfg,
		ghClient:       ghClient,
		approvalMgr:    approvalMgr,
		owner:          owner,
		repo:           repo,
		activePRs:      make(map[int]*PRState),
		recordedMerges: make(map[int]bool),
		prFailures:     make(map[int]*prFailureState),
		lastProgressAt: time.Now(), // Initialize to now to avoid false alarm on startup
		metrics:        NewMetrics(),
		log:            slog.Default().With("component", "autopilot"),
	}

	c.ciMonitor = NewCIMonitor(ghClient, owner, repo, cfg)
	c.autoMerger = NewAutoMerger(ghClient, approvalMgr, c.ciMonitor, owner, repo, cfg)
	c.feedbackLoop = NewFeedbackLoop(ghClient, owner, repo, cfg)

	// Initialize releaser from resolved release config (env-scoped wins over global).
	// Mirrors resolvedRelease() so construction matches runtime decision path.
	if relCfg := resolveRelease(cfg); relCfg != nil && relCfg.Enabled {
		c.releaser = NewReleaser(ghClient, owner, repo, relCfg)
	}

	// Initialize deployer if post-merge config exists
	if env := cfg.ResolvedEnv(); env.PostMerge != nil && env.PostMerge.Action != "" && env.PostMerge.Action != "none" {
		c.deployer = NewDeployer(ghClient, owner, repo, env.PostMerge)
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// parentIssueRe extracts a parent issue number from a sub-issue body line like
// "Parent: GH-3344" — the convention epic.go writes when decomposing an issue
// into sub-issues. TASK-352.
var parentIssueRe = regexp.MustCompile(`(?i)Parent:\s*GH-(\d+)`)

// selfHealForPR promotes any prior "failed" execution rows for the merged PR's
// issue — and its parent epic, if it is a sub-issue — to "completed", stamping the
// PR URL so the dashboard reflects the merged outcome. Safe to call from any merge
// path (controller-driven handleMerging or the externally-merged scan). No-op when
// the eval store is unset or issueNum is zero. TASK-352.
func (c *Controller) selfHealForPR(ctx context.Context, issueNum int, prURL string) {
	if c.evalStore == nil || issueNum == 0 {
		return
	}
	c.selfHealTask(fmt.Sprintf("GH-%d", issueNum), prURL)
	// Pilot decomposes a parent issue into sub-issues; only the sub-issue's PR
	// merges, so the parent's no-op "failed" row would never heal otherwise.
	if parent := c.resolveParentIssue(ctx, issueNum); parent != 0 && parent != issueNum {
		c.selfHealTask(fmt.Sprintf("GH-%d", parent), prURL)
	}
}

// selfHealTask runs SelfHealExecutionAfterMerge for one task ID, scoped to this
// controller's project path (empty = task_id-only match). TASK-352.
func (c *Controller) selfHealTask(taskID, prURL string) {
	if err := c.evalStore.SelfHealExecutionAfterMerge(taskID, c.projectPath, prURL); err != nil {
		c.log.Warn("failed to self-heal execution on merge", "task_id", taskID, "error", err)
	}
}

// resolveParentIssue returns the parent issue number for a sub-issue by parsing
// the "Parent: GH-N" line epic.go writes into sub-issue bodies, or 0 if the issue
// has no parent or cannot be fetched (best-effort, fail-open). TASK-352.
func (c *Controller) resolveParentIssue(ctx context.Context, issueNum int) int {
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, issueNum)
	if err != nil || issue == nil {
		return 0
	}
	if m := parentIssueRe.FindStringSubmatch(issue.Body); len(m) == 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// SetNotifier sets the notifier for autopilot events.
// This is optional; if not set, no notifications will be sent.
func (c *Controller) SetNotifier(n Notifier) {
	c.notifier = n
}

// SetMonitor sets the task monitor for dashboard state sync.
// GH-1336: When autopilot merges a PR, it updates monitor state so dashboard
// shows correct "done" status instead of stale "failed" from earlier execution attempts.
func (c *Controller) SetMonitor(m TaskMonitor) {
	c.monitor = m
}

// SetStateStore sets the persistent state store for crash recovery.
// If set, all state transitions are persisted to SQLite.
func (c *Controller) SetStateStore(store *StateStore) {
	c.stateStore = store
}

// SetLearningLoop sets the learning loop for capturing PR review feedback.
// When set, handleMerged will fetch reviews after merge and extract patterns.
func (c *Controller) SetLearningLoop(loop *memory.LearningLoop) {
	c.learningLoop = loop
	// GH-1979: Forward to feedback loop so fix issues can be annotated with known patterns.
	if c.feedbackLoop != nil {
		c.feedbackLoop.SetLearningLoop(loop)
	}
}

// SetEvalStore sets the eval store for capturing eval tasks from merged PRs.
func (c *Controller) SetEvalStore(store EvalStore) {
	c.evalStore = store
}

// SetMemoryStore wires an execution-level approval persister so that
// approval_request_id and approval_decision are written to the executions table.
func (c *Controller) SetMemoryStore(s *memory.Store) {
	c.memoryStore = s
}

// SetReleaseSummaryGenerator sets the LLM release summary generator.
// When set, handleReleasing will enrich GitHub releases with a human-friendly summary.
func (c *Controller) SetReleaseSummaryGenerator(gen *ReleaseSummaryGenerator) {
	c.releaseSummary = gen
}

// persistPRState saves a PR state to the store if available.
//
// TASK-324 concurrency contract: this method is LOCK-FREE with respect to the
// per-PR mutex. The CALLER MUST hold prState.mu (so the fields read by
// stateStore.SavePRState are stable) — OR the prState must not yet be published in
// c.activePRs (e.g. freshly constructed). It must NOT take prState.mu itself: every
// caller that holds the live pointer already owns prState.mu, and re-locking would
// deadlock (Go's sync.Mutex is non-reentrant).
func (c *Controller) persistPRState(prState *PRState) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.SavePRState(prState); err != nil {
		c.log.Warn("failed to persist PR state", "pr", prState.PRNumber, "error", err)
	}
}

// persistRemovePR removes a PR state from the store if available.
func (c *Controller) persistRemovePR(prNumber int) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.RemovePRState(prNumber); err != nil {
		c.log.Warn("failed to remove persisted PR state", "pr", prNumber, "error", err)
	}
}

// persistPRFailures saves per-PR failure state to the store if available.
func (c *Controller) persistPRFailures(prNumber int, state *prFailureState) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.SavePRFailures(prNumber, state.FailureCount, state.LastFailureTime); err != nil {
		c.log.Warn("failed to persist PR failure state", "pr", prNumber, "error", err)
	}
}

// removePRFailures removes per-PR failure state from the store if available.
func (c *Controller) removePRFailures(prNumber int) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.RemovePRFailures(prNumber); err != nil {
		c.log.Warn("failed to remove PR failure state", "pr", prNumber, "error", err)
	}
}

// RestoreState loads PR states and per-PR failures from the persistent store.
// If state is found in the store, ScanExistingPRs is unnecessary.
// Returns the number of restored PRs.
func (c *Controller) RestoreState() (int, error) {
	if c.stateStore == nil {
		return 0, nil
	}

	// Restore PR states
	states, err := c.stateStore.LoadAllPRStates()
	if err != nil {
		return 0, fmt.Errorf("failed to load PR states: %w", err)
	}

	c.mu.Lock()
	for _, pr := range states {
		// Skip terminal states — they shouldn't be active
		if pr.Stage == StageFailed {
			continue
		}
		c.activePRs[pr.PRNumber] = pr
	}
	c.mu.Unlock()

	// Restore per-PR failures
	prFailures, err := c.stateStore.LoadAllPRFailures()
	if err != nil {
		c.log.Warn("failed to load per-PR failure states", "error", err)
	} else {
		c.mu.Lock()
		for prNum, state := range prFailures {
			c.prFailures[prNum] = state
		}
		c.mu.Unlock()
	}

	restored := len(states)
	if restored > 0 {
		c.log.Info("restored autopilot state from SQLite",
			"pr_states", restored,
			"pr_failures", len(prFailures),
		)
	}

	return restored, nil
}

// SetOnIssueDone registers a callback invoked after a PR merges and pilot-done
// is applied. The callback receives the issue number and should mark it as
// processed in every active poller so the merge→done window cannot trigger
// phantom re-dispatch. GH-3271.
func (c *Controller) SetOnIssueDone(fn func(issueNumber int)) {
	c.onIssueDone = fn
}

// OnPRCreated registers a new PR for autopilot processing.
func (c *Controller) OnPRCreated(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string) {
	c.mu.Lock()
	prState := &PRState{
		PRNumber:        prNumber,
		PRURL:           prURL,
		IssueNumber:     issueNumber,
		BranchName:      branchName,
		HeadSHA:         headSHA,
		Stage:           StagePRCreated,
		CIStatus:        CIPending,
		CreatedAt:       time.Now(),
		EnvironmentName: c.config.EnvironmentName(),
		IssueNodeID:     issueNodeID,
	}
	c.activePRs[prNumber] = prState
	c.mu.Unlock()

	// Persist to SQLite (idempotent, safe outside lock).
	// TASK-324: prState is now published in activePRs, so a concurrent ProcessPR or
	// webhook could already hold a reference. Take prState.mu for the persist to honor
	// the persistPRState contract (caller holds prState.mu). c.mu is already released,
	// so the prState.mu→c.mu ordering invariant holds.
	prState.mu.Lock()
	c.persistPRState(prState)
	prState.mu.Unlock()

	c.log.Info("PR registered for autopilot",
		"pr", prNumber,
		"url", prURL,
		"issue", issueNumber,
		"branch", branchName,
		"sha", ShortSHA(headSHA),
		"stage", StagePRCreated,
		"env", c.config.EnvironmentName(),
	)

	// GH-3260: Sync board card to "In Review" column when PR is created (In Progress → Review).
	// Board sync is a non-critical side-effect; failure is logged but does not block registration.
	if c.boardSync != nil && issueNodeID != "" && c.reviewStatus != "" {
		if err := c.boardSync.UpdateProjectItemStatus(context.Background(), issueNodeID, c.reviewStatus); err != nil {
			c.log.Warn("board sync on PR created failed", "pr", prNumber, "error", err)
		}
	}
}

// OnReviewRequested handles PR review events from GitHub webhooks.
// For changes_requested reviews on tracked PRs, it transitions the PR to StageReviewRequested
// so the next processAllPRs tick will create a revision issue.
func (c *Controller) OnReviewRequested(prNumber int, action, state, reviewer string) {
	c.mu.RLock()
	prState, tracked := c.activePRs[prNumber]
	c.mu.RUnlock()

	c.log.Info("PR review received",
		"pr", prNumber,
		"action", action,
		"state", state,
		"reviewer", reviewer,
		"tracked", tracked,
	)

	if !tracked {
		return
	}

	// Only act on changes_requested reviews
	if state != "changes_requested" {
		return
	}

	// Check if review feedback handling is enabled
	if c.config.ReviewFeedback == nil || !c.config.ReviewFeedback.Enabled {
		c.log.Info("review feedback handling disabled, ignoring changes_requested",
			"pr", prNumber,
			"reviewer", reviewer,
		)
		return
	}

	// TASK-324: guard the read of prState.Stage (for the log), the Stage write, and
	// the persist under the per-PR mutex. The pointer was fetched under c.mu above and
	// c.mu has since been released, so taking prState.mu here keeps the no-deadlock
	// invariant (prState.mu before c.mu, never the reverse).
	prState.mu.Lock()
	c.log.Warn("Changes requested on PR, transitioning to review_requested stage",
		"pr", prNumber,
		"reviewer", reviewer,
		"current_stage", prState.Stage,
	)
	prState.Stage = StageReviewRequested
	c.persistPRState(prState)
	prState.mu.Unlock()
}

// ProcessPR processes a single PR through the state machine.
// Returns error if processing fails; caller should retry based on error type.
// Accepts optional cached ghPR to avoid redundant API calls.
func (c *Controller) ProcessPR(ctx context.Context, prNumber int, ghPR *github.PullRequest) error {
	c.mu.RLock()
	prState, ok := c.activePRs[prNumber]
	c.mu.RUnlock()

	if !ok {
		return fmt.Errorf("PR %d not tracked", prNumber)
	}

	// TASK-324: hold the per-PR mutex for the entire processing body. This single
	// lock covers all 11 handleX(prState) handlers, the inline PRTitle/TargetBranch/
	// Error writes, and the persistPRState call, serialising the main loop against
	// webhook writers (OnReviewRequested, SetApprovalDecision) on the same PR.
	// Lock ordering: we hold prState.mu and may take c.mu below (isPRCircuitOpen,
	// recordPRFailure/resetPRFailures, the lastProgressAt update, removePR via
	// handlers). Never the reverse — see the no-deadlock invariant on PRState.
	prState.mu.Lock()
	defer prState.mu.Unlock()

	// Per-PR circuit breaker check
	if c.isPRCircuitOpen(prNumber) {
		c.log.Warn("per-PR circuit breaker open", "pr", prNumber)
		c.metrics.RecordCircuitBreakerTrip()
		return fmt.Errorf("circuit breaker: PR %d has too many consecutive failures", prNumber)
	}

	// Populate PR metadata from GitHub response when available
	if ghPR != nil {
		if prState.PRTitle == "" && ghPR.Title != "" {
			prState.PRTitle = ghPR.Title
		}
		if prState.TargetBranch == "" && ghPR.Base.Ref != "" {
			prState.TargetBranch = ghPR.Base.Ref
		}
	}

	previousStage := prState.Stage
	var err error

	switch prState.Stage {
	case StagePRCreated:
		err = c.handlePRCreated(ctx, prState, ghPR)
	case StageWaitingCI:
		err = c.handleWaitingCI(ctx, prState, ghPR)
	case StageCIPassed:
		err = c.handleCIPassed(ctx, prState)
	case StageCIFailed:
		err = c.handleCIFailed(ctx, prState)
	case StageAwaitApproval:
		err = c.handleAwaitApproval(ctx, prState)
	case StageMerging:
		err = c.handleMerging(ctx, prState)
	case StageMerged:
		err = c.handleMerged(ctx, prState)
	case StagePostMergeCI:
		err = c.handlePostMergeCI(ctx, prState)
	case StageReviewRequested:
		err = c.handleReviewRequested(ctx, prState)
	case StageReleasing:
		err = c.handleReleasing(ctx, prState)
	case StageFailed:
		// Terminal state - no processing
		return nil
	}

	// Log stage transitions and update progress timestamp for deadlock detection
	if prState.Stage != previousStage {
		c.log.Info("PR stage transition",
			"pr", prNumber,
			"from", previousStage,
			"to", prState.Stage,
			"env", c.config.EnvironmentName(),
		)

		// GH-849: Update lastProgressAt and reset deadlock alert flag
		c.mu.Lock()
		c.lastProgressAt = time.Now()
		c.deadlockAlertSent = false
		c.mu.Unlock()
	}

	if err != nil {
		c.recordPRFailure(prNumber)
		prState.Error = err.Error()
		c.log.Error("autopilot stage failed", "pr", prNumber, "stage", prState.Stage, "error", err)
	} else {
		c.resetPRFailures(prNumber)
	}

	// Persist state after every processing cycle (covers transitions and updated fields)
	c.persistPRState(prState)

	return err
}

// handlePRCreated starts CI monitoring for all environments.
// Also checks for merge conflicts immediately (race condition with concurrent merges).
// Accepts optional cached ghPR to avoid redundant API calls.
func (c *Controller) handlePRCreated(ctx context.Context, prState *PRState, ghPR *github.PullRequest) error {
	c.log.Debug("handlePRCreated: starting CI monitoring",
		"pr", prState.PRNumber,
		"sha", ShortSHA(prState.HeadSHA),
	)

	// GH-724: Check for merge conflicts immediately after PR creation.
	// Concurrent merges can make a PR conflicting before CI even starts.
	// Use cached ghPR if provided to avoid redundant API call.
	if ghPR != nil {
		if c.isMergeConflict(ghPR) {
			return c.handleMergeConflict(ctx, prState)
		}
	} else {
		// Fallback: fetch PR if not provided (for backward compatibility)
		fetchedPR, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prState.PRNumber)
		if err != nil {
			c.log.Warn("failed to check PR mergeable state on creation", "pr", prState.PRNumber, "error", err)
			// Non-fatal: proceed to CI wait, conflict will be caught there
		} else if c.isMergeConflict(fetchedPR) {
			return c.handleMergeConflict(ctx, prState)
		}
	}

	// All environments wait for CI - no skipping
	prState.Stage = StageWaitingCI
	prState.CIWaitStartedAt = time.Now()
	return nil
}

// handleWaitingCI checks CI status once (non-blocking) and updates state.
// Uses CheckCI instead of WaitForCI to prevent blocking the processing loop.
// Accepts optional cached ghPR to avoid redundant API calls.
func (c *Controller) handleWaitingCI(ctx context.Context, prState *PRState, ghPR *github.PullRequest) error {
	// Initialize CIWaitStartedAt if not set (backwards compatibility)
	if prState.CIWaitStartedAt.IsZero() {
		prState.CIWaitStartedAt = time.Now()
	}

	// Check for CI timeout: use the minimum of CIWaitTimeout and the environment's CITimeout.
	// This respects explicit user overrides (e.g. short timeouts in tests) while defaulting
	// to the environment-specific timeout when no override is set.
	ciTimeout := c.config.CIWaitTimeout
	envCITimeout := c.config.ResolvedEnv().CITimeout
	if envCITimeout > 0 && (ciTimeout == 0 || envCITimeout < ciTimeout) {
		ciTimeout = envCITimeout
	}

	if time.Since(prState.CIWaitStartedAt) > ciTimeout {
		c.log.Warn("CI timeout", "pr", prState.PRNumber, "waited", time.Since(prState.CIWaitStartedAt))
		prState.Stage = StageFailed
		prState.Error = fmt.Sprintf("CI timeout after %v", ciTimeout)
		return nil
	}

	// GH-419, GH-457: Always refresh HeadSHA from GitHub before checking CI.
	// Self-review or other post-creation commits can change the HEAD,
	// and OnPRCreated may have been called with an empty or stale CommitSHA.
	// The previous fix (GH-419) only handled empty SHA; stale non-empty SHAs
	// caused autopilot to query CI for the wrong commit indefinitely.
	sha := prState.HeadSHA

	// Use cached ghPR if provided, otherwise fetch it
	if ghPR == nil {
		var err error
		ghPR, err = c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prState.PRNumber)
		if err != nil {
			c.log.Warn("failed to fetch PR head SHA", "pr", prState.PRNumber, "error", err)
			if sha == "" {
				return nil // Can't check CI without SHA, retry next cycle
			}
			// Fall through with existing SHA if we have one
		}
	}

	if ghPR != nil && ghPR.Head.SHA != "" {
		if sha != "" && sha != ghPR.Head.SHA {
			c.log.Info("refreshed stale HeadSHA from GitHub",
				"pr", prState.PRNumber,
				"old", ShortSHA(sha),
				"new", ShortSHA(ghPR.Head.SHA),
			)
		} else if sha == "" {
			c.log.Info("refreshed empty HeadSHA from GitHub",
				"pr", prState.PRNumber,
				"sha", ShortSHA(ghPR.Head.SHA),
			)
		}
		prState.HeadSHA = ghPR.Head.SHA
		sha = ghPR.Head.SHA
	} else if sha == "" {
		c.log.Warn("GitHub returned empty SHA for PR", "pr", prState.PRNumber)
		return nil // Retry next cycle
	}

	// GH-724: Check for merge conflicts before waiting for CI.
	// Conflicting PRs will never have CI run, so waiting is pointless.
	if ghPR != nil && c.isMergeConflict(ghPR) {
		return c.handleMergeConflict(ctx, prState)
	}

	// Non-blocking CI status check
	status, err := c.ciMonitor.CheckCI(ctx, sha)
	if err != nil {
		prState.ConsecutiveAPIFailures++
		c.log.Warn("CI status check failed",
			"pr", prState.PRNumber,
			"sha", ShortSHA(sha),
			"consecutive_failures", prState.ConsecutiveAPIFailures,
			"error", err)

		// If we've had 5 consecutive failures, transition to failed stage
		if prState.ConsecutiveAPIFailures >= 5 {
			prState.Stage = StageFailed
			prState.Error = fmt.Sprintf("CI check API failed %d consecutive times: %v",
				prState.ConsecutiveAPIFailures, err)
			c.log.Error("PR transitioned to failed due to consecutive API failures",
				"pr", prState.PRNumber,
				"consecutive_failures", prState.ConsecutiveAPIFailures)
			return nil
		}

		// Don't fail the PR on transient errors, will retry next poll cycle
		return nil
	}

	// Reset failure counter on successful API call
	prState.ConsecutiveAPIFailures = 0

	// GH-862: Capture discovered checks for PR state (only once, when first seen)
	if discovered := c.ciMonitor.GetDiscoveredChecks(sha); len(discovered) > 0 && len(prState.DiscoveredChecks) == 0 {
		prState.DiscoveredChecks = discovered
		c.log.Info("CI checks discovered", "pr", prState.PRNumber, "checks", discovered)
	}

	prState.CIStatus = status
	prState.LastChecked = time.Now()

	c.log.Debug("CI status check result",
		"pr", prState.PRNumber,
		"sha", ShortSHA(sha),
		"status", status,
	)

	switch status {
	case CISuccess:
		c.log.Info("CI passed", "pr", prState.PRNumber, "sha", ShortSHA(sha))
		prState.Stage = StageCIPassed
		if !prState.CIWaitStartedAt.IsZero() {
			c.metrics.RecordCIWaitDuration(time.Since(prState.CIWaitStartedAt))
		}
	case CIFailure:
		c.log.Warn("CI failed", "pr", prState.PRNumber, "sha", ShortSHA(sha))
		prState.Stage = StageCIFailed
		if !prState.CIWaitStartedAt.IsZero() {
			c.metrics.RecordCIWaitDuration(time.Since(prState.CIWaitStartedAt))
		}
	case CIPending, CIRunning:
		// Stay in StageWaitingCI, will be checked next poll cycle
		c.log.Debug("CI still running", "pr", prState.PRNumber, "status", status)
	}

	return nil
}

// handleCIPassed proceeds to merge (with approval if required by environment config
// or by the scope-drift / size-floor defense-in-depth rails).
func (c *Controller) handleCIPassed(ctx context.Context, prState *PRState) error {
	c.log.Info("handleCIPassed: CI passed, determining next stage",
		"pr", prState.PRNumber,
		"env", c.config.EnvironmentName(),
		"auto_merge", c.config.AutoMerge,
	)

	// Defense-in-depth: scope-drift and size-floor gates escalate to human approval
	// regardless of env RequireApproval config. Born from OAuth cascade #2
	// (#2572/#2584/#2585): a runaway executor must not land oversized or scope-drifting
	// code unsupervised even when env config drops require_approval.
	var escalateReason string
	files, listErr := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, prState.PRNumber)
	if listErr != nil {
		c.log.Warn("handleCIPassed: ListPullRequestFiles failed, skipping size-floor gate (fail-open)",
			"pr", prState.PRNumber, "error", listErr)
	} else if reason := SizeFloorReason(files); reason != "" {
		escalateReason = reason
	}

	if escalateReason == "" && prState.IssueNumber > 0 {
		issue, issueErr := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
		if issueErr != nil {
			c.log.Warn("handleCIPassed: GetIssue failed, skipping scope-drift gate (fail-open)",
				"pr", prState.PRNumber, "issue", prState.IssueNumber, "error", issueErr)
		} else if reason := ScopeDriftReason(prState.PRTitle, issue.Title); reason != "" {
			escalateReason = reason
		}
	}

	if escalateReason != "" {
		c.log.Warn("merge gate escalated: requiring human approval",
			"pr", prState.PRNumber, "reason", escalateReason)
		prState.Stage = StageAwaitApproval
		if c.notifier != nil {
			if err := c.notifier.NotifyApprovalRequired(ctx, prState); err != nil {
				c.log.Warn("failed to send approval notification", "error", err)
			}
		}
		return nil
	}

	if c.config.ResolvedEnv().RequireApproval {
		c.log.Info("awaiting approval before merge", "pr", prState.PRNumber)
		prState.Stage = StageAwaitApproval

		// Notify approval required
		if c.notifier != nil {
			if err := c.notifier.NotifyApprovalRequired(ctx, prState); err != nil {
				c.log.Warn("failed to send approval notification", "error", err)
			}
		}
	} else {
		c.log.Info("proceeding to merge",
			"pr", prState.PRNumber,
			"env", c.config.EnvironmentName(),
		)
		prState.Stage = StageMerging
	}
	return nil
}

// handleCIFailed creates fix issue via feedback loop.
// GH-1566: Tracks CI fix iteration depth to prevent infinite cascade.
// Each fix issue embeds an iteration counter in autopilot-meta; when the
// counter reaches MaxCIFixIterations the PR transitions to StageFailed
// instead of spawning another fix issue.
func (c *Controller) handleCIFailed(ctx context.Context, prState *PRState) error {
	failedChecks, err := c.ciMonitor.GetFailedChecks(ctx, prState.HeadSHA)
	if err != nil {
		c.log.Warn("failed to get failed checks", "error", err)
		// Continue with empty list
	}

	// Notify CI failure
	if c.notifier != nil {
		if err := c.notifier.NotifyCIFailed(ctx, prState, failedChecks); err != nil {
			c.log.Warn("failed to send CI failure notification", "error", err)
		}
	}

	// GH-1566: Check CI fix iteration depth from the originating issue.
	// If this PR was created from an autopilot-fix issue, that issue's body
	// contains an iteration counter. Stop the cascade when limit is reached.
	iteration := 0
	if prState.IssueNumber > 0 && c.config.MaxCIFixIterations > 0 {
		issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
		if err != nil {
			c.log.Warn("failed to fetch issue for iteration check", "issue", prState.IssueNumber, "error", err)
			// Continue with iteration=0 (safe: won't block on transient error)
		} else {
			iteration = parseAutopilotIteration(issue.Body)
		}

		if iteration >= c.config.MaxCIFixIterations {
			c.log.Warn("CI fix iteration limit reached, stopping cascade",
				"pr", prState.PRNumber,
				"issue", prState.IssueNumber,
				"iteration", iteration,
				"max", c.config.MaxCIFixIterations,
			)

			// Close the failed PR so the sequential poller can unblock
			if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
				c.log.Warn("failed to close failed PR", "pr", prState.PRNumber, "error", err)
			}

			// GH-3260: Sync board card to "Blocked/Failed" column on execution failure (iteration limit).
			if c.boardSync != nil && prState.IssueNodeID != "" && c.failStatus != "" {
				if err := c.boardSync.UpdateProjectItemStatus(ctx, prState.IssueNodeID, c.failStatus); err != nil {
					c.log.Warn("board sync on exec failure (iteration limit) failed", "pr", prState.PRNumber, "error", err)
				}
			}

			prState.Stage = StageFailed
			prState.Error = fmt.Sprintf("CI fix iteration limit reached (%d/%d): stopping cascade to prevent infinite loop", iteration, c.config.MaxCIFixIterations)
			c.metrics.RecordPRFailed()
			c.metrics.RecordIssueProcessed("failed")
			return nil
		}
	}

	// GH-2588: Cascade-2 size-guard — if the failing PR already exceeds the size floor,
	// it is a likely contamination cascade. Refuse to spawn another fix(ci) issue.
	if c.config.MaxCIFixPRSize > 0 {
		files, err := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, prState.PRNumber)
		if err != nil {
			c.log.Warn("CI fix size guard: ListPullRequestFiles failed, skipping guard (fail-open)",
				"pr", prState.PRNumber, "error", err)
			// Fall through — belt-and-suspenders: merge-time SizeFloor gate in handleCIPassed catches it.
		} else {
			netAdditions := 0
			for _, f := range files {
				netAdditions += f.Additions
			}
			if netAdditions > c.config.MaxCIFixPRSize {
				c.log.Warn("CI fix size guard fired — failing PR exceeds size floor, refusing to spawn fix issue",
					"pr", prState.PRNumber, "additions", netAdditions, "limit", c.config.MaxCIFixPRSize)
				if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
					c.log.Warn("CI fix size guard: failed to close oversized failed PR",
						"pr", prState.PRNumber, "error", err)
				}
				// GH-3260: Sync board card to "Blocked/Failed" column on execution failure (size guard).
				if c.boardSync != nil && prState.IssueNodeID != "" && c.failStatus != "" {
					if err := c.boardSync.UpdateProjectItemStatus(ctx, prState.IssueNodeID, c.failStatus); err != nil {
						c.log.Warn("board sync on exec failure (size guard) failed", "pr", prState.PRNumber, "error", err)
					}
				}
				prState.Stage = StageFailed
				prState.Error = fmt.Sprintf("CI fix size guard: PR has %d additions, over limit %d (likely cascade contamination — escalate to human)", netAdditions, c.config.MaxCIFixPRSize)
				c.metrics.RecordPRFailed()
				c.metrics.RecordIssueProcessed("failed")
				return nil
			}
		}
	}

	// GH-1567: Fetch actual CI error logs to include in fix issues.
	// This prevents Pilot from having to rediscover errors by running linter/tests itself.
	ciLogs := c.ciMonitor.GetFailedCheckLogs(ctx, prState.HeadSHA, 2000)

	issueNum, err := c.feedbackLoop.CreateFailureIssue(ctx, prState, FailureCIPreMerge, failedChecks, ciLogs, iteration+1)
	if err != nil {
		return fmt.Errorf("failed to create fix issue: %w", err)
	}

	// GH-1964/GH-1979: Learn from CI failure patterns (self-improvement).
	// Guard: skip learning when CI logs are empty/whitespace (nothing to extract).
	if c.learningLoop != nil && strings.TrimSpace(ciLogs) != "" {
		projectPath := c.owner + "/" + c.repo
		if learnErr := c.learningLoop.LearnFromCIFailure(ctx, projectPath, ciLogs, failedChecks); learnErr != nil {
			c.log.Warn("Failed to learn from CI failure", slog.Any("error", learnErr))
		}
	}

	// Notify fix issue created
	if c.notifier != nil {
		if err := c.notifier.NotifyFixIssueCreated(ctx, prState, issueNum); err != nil {
			c.log.Warn("failed to send fix issue notification", "error", err)
		}
	}

	c.log.Info("created fix issue for CI failure", "pr", prState.PRNumber, "issue", issueNum)

	// Close the failed PR on GitHub so the sequential poller's merge waiter
	// can unblock and pick up the fix issue. Without this, the poller stays
	// blocked in WaitWithCallback() waiting for a PR that will never merge.
	if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
		c.log.Warn("failed to close failed PR", "pr", prState.PRNumber, "error", err)
		// Non-fatal: merge waiter will eventually timeout
	} else {
		c.log.Info("closed failed PR", "pr", prState.PRNumber, "fix_issue", issueNum)
	}

	// GH-1870: Sync board card to "Failed" column on CI failure
	if c.boardSync != nil && prState.IssueNodeID != "" && c.failStatus != "" {
		if err := c.boardSync.UpdateProjectItemStatus(ctx, prState.IssueNodeID, c.failStatus); err != nil {
			c.log.Warn("board sync on CI fail failed", "pr", prState.PRNumber, "error", err)
		}
	}

	prState.Stage = StageFailed
	c.metrics.RecordPRFailed()
	return nil
}

// handleReviewRequested processes a PR that received "changes requested" review feedback.
// It fetches reviews and comments, checks iteration limits, creates a revision issue,
// learns from the review, then closes the PR and deletes the branch.
func (c *Controller) handleReviewRequested(ctx context.Context, prState *PRState) error {
	c.log.Info("handleReviewRequested: processing review feedback",
		"pr", prState.PRNumber,
	)

	// Fetch reviews and comments
	reviews, err := c.ghClient.ListPullRequestReviews(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		return fmt.Errorf("failed to fetch reviews: %w", err)
	}

	comments, err := c.ghClient.GetPullRequestComments(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("failed to fetch review comments", "pr", prState.PRNumber, "error", err)
		// Non-fatal: proceed with reviews only
	}

	// Check iteration limit
	iteration := 0
	if prState.IssueNumber > 0 && c.config.ReviewFeedback != nil && c.config.ReviewFeedback.MaxIterations > 0 {
		issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
		if err != nil {
			c.log.Warn("failed to fetch issue for iteration check", "issue", prState.IssueNumber, "error", err)
		} else {
			iteration = parseAutopilotIteration(issue.Body)
		}

		if iteration >= c.config.ReviewFeedback.MaxIterations {
			c.log.Warn("review feedback iteration limit reached",
				"pr", prState.PRNumber,
				"iteration", iteration,
				"max", c.config.ReviewFeedback.MaxIterations,
			)

			if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
				c.log.Warn("failed to close PR", "pr", prState.PRNumber, "error", err)
			}

			prState.Stage = StageFailed
			prState.Error = fmt.Sprintf("review feedback iteration limit reached (%d/%d)", iteration, c.config.ReviewFeedback.MaxIterations)
			c.metrics.RecordPRFailed()
			c.metrics.RecordIssueProcessed("failed")
			return nil
		}
	}

	// Create revision issue with review feedback
	issueNum, err := c.feedbackLoop.CreateReviewIssue(ctx, prState, reviews, comments, iteration+1)
	if err != nil {
		return fmt.Errorf("failed to create review issue: %w", err)
	}

	// Learn from review (self-improvement)
	if c.learningLoop != nil && len(reviews) > 0 {
		var reviewData []*memory.ReviewData
		for _, r := range reviews {
			if r.Body == "" {
				continue
			}
			reviewData = append(reviewData, &memory.ReviewData{
				Body:     r.Body,
				State:    r.State,
				Reviewer: r.User.Login,
			})
		}
		for _, comment := range comments {
			reviewData = append(reviewData, &memory.ReviewData{
				Body:     comment.Body,
				State:    "COMMENTED",
				Reviewer: comment.User.Login,
			})
		}
		if len(reviewData) > 0 {
			projectPath := c.owner + "/" + c.repo
			if learnErr := c.learningLoop.LearnFromReview(ctx, projectPath, reviewData, prState.PRURL); learnErr != nil {
				c.log.Warn("Failed to learn from review feedback", slog.Any("error", learnErr))
			}
		}
	}

	// Notify fix issue created
	if c.notifier != nil {
		if err := c.notifier.NotifyFixIssueCreated(ctx, prState, issueNum); err != nil {
			c.log.Warn("failed to send review issue notification", "error", err)
		}
	}

	c.log.Info("created revision issue for review feedback", "pr", prState.PRNumber, "issue", issueNum)

	// Close the PR and delete the branch
	if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
		c.log.Warn("failed to close PR after review", "pr", prState.PRNumber, "error", err)
	}

	if prState.BranchName != "" {
		if err := c.ghClient.DeleteBranch(ctx, c.owner, c.repo, prState.BranchName); err != nil {
			c.log.Debug("branch cleanup after review", "branch", prState.BranchName, "error", err)
		}
	}

	prState.Stage = StageFailed
	c.metrics.RecordPRFailed()
	return nil
}

// hasChangesRequested checks if a PR has unresolved "changes requested" reviews.
// It filters out bot reviews and only considers reviews submitted after the PR was created.
func (c *Controller) hasChangesRequested(ctx context.Context, prState *PRState) bool {
	reviews, err := c.ghClient.ListPullRequestReviews(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("failed to fetch reviews for changes_requested check", "pr", prState.PRNumber, "error", err)
		return false
	}

	// Track latest review state per user (only non-bot users)
	latestState := make(map[string]string)
	for _, r := range reviews {
		// Skip bot reviews (self-review)
		if strings.Contains(r.User.Login, "[bot]") || strings.HasSuffix(r.User.Login, "-bot") {
			continue
		}

		// Only consider reviews submitted after the PR entered tracking
		if r.SubmittedAt != "" && !prState.CreatedAt.IsZero() {
			submittedAt, err := time.Parse(time.RFC3339, r.SubmittedAt)
			if err == nil && submittedAt.Before(prState.CreatedAt) {
				continue
			}
		}

		latestState[r.User.Login] = r.State
	}

	for _, state := range latestState {
		if state == "CHANGES_REQUESTED" {
			return true
		}
	}

	return false
}

// handleAwaitApproval is a non-blocking tick handler for StageAwaitApproval.
//
// Tick 1 (no ApprovalRequestID): submits the request via SubmitApprovalRequest,
// persists the returned ID + ApprovalRequestedAt, stays in StageAwaitApproval.
//
// Tick N with decision recorded: advances to StageMerging (approved) or
// StageFailed (rejected/timeout).
//
// Tick N with no decision: checks wall-clock expiry against the stage timeout and
// applies default_action when expired (belt-and-suspenders for post-restart cases).
func (c *Controller) handleAwaitApproval(ctx context.Context, prState *PRState) error {
	// Path 1: submit request on first tick.
	if prState.ApprovalRequestID == "" {
		return c.submitAsyncApprovalRequest(ctx, prState)
	}

	// Path 2: decision already recorded — advance the state machine.
	if prState.ApprovalDecision != "" {
		return c.applyApprovalDecision(prState)
	}

	// Path 3: still waiting — check wall-clock expiry as a guard for post-restart
	// cases where the background goroutine in SubmitApprovalRequest is gone.
	timeout := c.approvalMgr.PreMergeTimeout()
	if !prState.ApprovalRequestedAt.IsZero() && time.Since(prState.ApprovalRequestedAt) > timeout {
		defaultAction := c.approvalMgr.PreMergeDefaultAction()
		c.log.Warn("approval request expired in controller (post-restart guard)",
			"pr", prState.PRNumber,
			"request_id", prState.ApprovalRequestID,
			"elapsed", time.Since(prState.ApprovalRequestedAt).Round(time.Second),
			"default_action", defaultAction)
		prState.ApprovalDecision = string(defaultAction)
		return c.applyApprovalDecision(prState)
	}

	// Still waiting for user input — stay in StageAwaitApproval.
	return nil
}

// submitAsyncApprovalRequest submits the first async approval request for a PR.
func (c *Controller) submitAsyncApprovalRequest(ctx context.Context, prState *PRState) error {
	// Fail closed: if approval stage is not enabled, do NOT auto-approve when the env requires approval.
	if c.approvalMgr == nil || !c.approvalMgr.IsStageEnabled(approval.StagePreMerge) {
		c.log.Error("approval misconfig: env requires approval but pre_merge.enabled=false",
			"pr", prState.PRNumber, "env", c.config.EnvironmentName())
		prState.Stage = StageFailed
		prState.Error = fmt.Sprintf(
			"approval-misconfig: env %q has require_approval=true but approval.pre_merge.enabled=false → deadlock until config fixed",
			c.config.EnvironmentName(),
		)
		c.autoMerger.postMisconfigComment(ctx, prState)
		c.metrics.RecordPRFailed()
		c.metrics.RecordIssueProcessed("failed")
		return nil
	}

	taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
	if prState.IssueNumber == 0 {
		taskID = fmt.Sprintf("PR-%d", prState.PRNumber)
	}
	req := &approval.Request{
		ID:     fmt.Sprintf("pr-%d-%d", prState.PRNumber, time.Now().UnixNano()),
		TaskID: taskID,
		Stage:  approval.StagePreMerge,
		Title:  fmt.Sprintf("Merge approval for PR #%d", prState.PRNumber),
		Metadata: map[string]interface{}{
			"pr_url":    prState.PRURL,
			"pr_title":  prState.PRTitle,
			"pr_number": prState.PRNumber,
		},
	}

	requestID, err := c.approvalMgr.SubmitApprovalRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("submit approval request for PR %d: %w", prState.PRNumber, err)
	}

	prState.ApprovalRequestID = requestID
	prState.ApprovalRequestedAt = time.Now()
	// Stage intentionally stays at StageAwaitApproval.

	if c.stateStore != nil {
		if serr := c.stateStore.SavePRState(prState); serr != nil {
			c.log.Warn("failed to persist approval request state", "pr", prState.PRNumber, "error", serr)
		}
	}

	if c.memoryStore != nil {
		if merr := c.memoryStore.SetApprovalRequestID(ctx, taskID, requestID); merr != nil {
			c.log.Warn("failed to persist approval_request_id to executions",
				"pr", prState.PRNumber, "task_id", taskID, "request_id", requestID,
				"op", "SetApprovalRequestID", "error", merr)
			if errors.Is(merr, sql.ErrNoRows) {
				c.metrics.RecordApprovalPersistMiss("request_id")
			}
		}
	}

	c.log.Info("async approval request submitted",
		"pr", prState.PRNumber, "request_id", requestID)
	return nil
}

// applyApprovalDecision advances the state machine based on the recorded decision.
func (c *Controller) applyApprovalDecision(prState *PRState) error {
	switch approval.Decision(prState.ApprovalDecision) {
	case approval.DecisionApproved:
		c.log.Info("approval granted — advancing to merging stage", "pr", prState.PRNumber)
		prState.Stage = StageMerging
	case approval.DecisionRejected, approval.DecisionTimeout:
		c.log.Info("approval not granted — failing PR",
			"pr", prState.PRNumber, "decision", prState.ApprovalDecision)
		prState.Stage = StageFailed
		prState.Error = fmt.Sprintf("merge rejected: approval %s", prState.ApprovalDecision)
		c.metrics.RecordPRFailed()
		c.metrics.RecordIssueProcessed("failed")
	default:
		c.log.Warn("unknown approval decision — failing PR",
			"pr", prState.PRNumber, "decision", prState.ApprovalDecision)
		prState.Stage = StageFailed
		prState.Error = fmt.Sprintf("unknown approval decision: %q", prState.ApprovalDecision)
		c.metrics.RecordPRFailed()
		c.metrics.RecordIssueProcessed("failed")
	}
	return nil
}

// SetApprovalDecision implements approval.PRStateWriter. It finds the in-memory
// PRState whose ApprovalRequestID matches and records the decision, then persists
// via stateStore. Called by the approval.Manager's background goroutine when a
// handler fires (e.g. Telegram button tap).
func (c *Controller) SetApprovalDecision(ctx context.Context, requestID string, decision string, by string) error {
	if requestID == "" {
		return nil
	}

	// TASK-324: collect the live pointers under c.mu, then RELEASE c.mu before taking
	// any prState.mu (no-deadlock invariant: prState.mu before c.mu, never reverse).
	// ApprovalRequestID is written under prState.mu (submitAsyncApprovalRequest), so we
	// also read it under prState.mu to find the match.
	c.mu.RLock()
	live := make([]*PRState, 0, len(c.activePRs))
	for _, pr := range c.activePRs {
		live = append(live, pr)
	}
	c.mu.RUnlock()

	for _, pr := range live {
		pr.mu.Lock()
		if pr.ApprovalRequestID != requestID {
			pr.mu.Unlock()
			continue
		}
		pr.ApprovalDecision = decision
		if c.stateStore != nil {
			_ = c.stateStore.SavePRState(pr)
		}
		prNumber := pr.PRNumber
		issueNumber := pr.IssueNumber
		pr.mu.Unlock()

		// memoryStore persistence is keyed by requestID, not by the live PRState
		// fields, so it is safe (and preferable) to run it outside prState.mu.
		if c.memoryStore != nil {
			if merr := c.memoryStore.SetApprovalDecision(ctx, requestID, decision, by); merr != nil {
				taskIDStr := fmt.Sprintf("GH-%d", issueNumber)
				if errors.Is(merr, sql.ErrNoRows) {
					c.log.Warn("failed to persist approval decision to executions (no matching row)",
						"pr", prNumber, "task_id", taskIDStr, "request_id", requestID,
						"op", "SetApprovalDecision", "decision", decision, "error", merr)
					c.metrics.RecordApprovalPersistMiss("decision")
				} else {
					c.log.Warn("failed to persist approval decision to executions",
						"pr", prNumber, "task_id", taskIDStr, "request_id", requestID,
						"op", "SetApprovalDecision", "decision", decision, "error", merr)
				}
			}
		}
		c.log.Info("approval decision applied to PR state",
			"pr", prNumber, "request_id", requestID,
			"decision", decision, "by", by)
		return nil
	}
	// requestID not found in this controller — normal in multi-repo deployments.
	return nil
}

// handleMerging merges the PR.
func (c *Controller) handleMerging(ctx context.Context, prState *PRState) error {
	prState.MergeAttempts++

	c.log.Info("handleMerging: attempting merge",
		"pr", prState.PRNumber,
		"attempt", prState.MergeAttempts,
		"method", c.config.MergeMethod,
	)

	err := c.autoMerger.MergePR(ctx, prState)
	if err != nil {
		c.log.Error("handleMerging: merge failed",
			"pr", prState.PRNumber,
			"attempt", prState.MergeAttempts,
			"error", err,
		)

		// GH-880: Check if merge failed due to conflict.
		// If so, close PR and clear pilot-in-progress so issue can be retried.
		ghPR, ghErr := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prState.PRNumber)
		if ghErr == nil && c.isMergeConflict(ghPR) {
			return c.handleMergeConflict(ctx, prState)
		}

		// B5 (TASK-336): Hard cap on non-conflict merge retries. The circuit breaker
		// (MaxFailures) auto-resets after FailureResetTimeout, so without this cap a
		// PR blocked by branch-protection or a stuck status check retries indefinitely.
		// Once MergeAttempts reaches MaxMergeAttempts the failure is terminal and a
		// human must intervene.
		if prState.MergeAttempts >= c.config.MaxMergeAttempts {
			errMsg := fmt.Sprintf("merge failed after %d/%d attempts: %v — manual intervention required",
				prState.MergeAttempts, c.config.MaxMergeAttempts, err)
			c.log.Error("handleMerging: merge attempt cap reached — escalating to StageFailed",
				"pr", prState.PRNumber,
				"attempts", prState.MergeAttempts,
				"max", c.config.MaxMergeAttempts,
				"error", err,
			)
			if prState.IssueNumber > 0 {
				comment := fmt.Sprintf(
					"⚠️ **Merge escalation**: PR #%d failed to merge after %d attempts.\n\nLast error: `%v`\n\nManual intervention is required — no further automatic retries will be made.",
					prState.PRNumber, prState.MergeAttempts, err)
				if _, cerr := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, comment); cerr != nil {
					c.log.Warn("failed to post merge escalation comment", "issue", prState.IssueNumber, "error", cerr)
				}
			}
			prState.Stage = StageFailed
			prState.Error = errMsg
			c.metrics.RecordPRFailed()
			c.metrics.RecordIssueProcessed("failed")
			return nil
		}

		return fmt.Errorf("merge attempt %d failed: %w", prState.MergeAttempts, err)
	}

	c.log.Info("PR merged successfully", "pr", prState.PRNumber)
	prState.Stage = StageMerged
	c.recordMergeSuccess(prState)

	// GH-1015: Add pilot-done label after successful merge (not at PR creation)
	// This prevents false positives where PRs are closed without merging
	if prState.IssueNumber > 0 {
		// GH-3271: mark issue processed in all pollers before any label updates so
		// a poll tick that fires during the merge→pilot-done propagation window
		// cannot re-dispatch the issue (phantom pilot-blocked).
		if c.onIssueDone != nil {
			c.onIssueDone(prState.IssueNumber)
		}
		if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, prState.IssueNumber, []string{github.LabelDone}); err != nil {
			c.log.Warn("failed to add pilot-done label after merge", "issue", prState.IssueNumber, "error", err)
		}
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelInProgress); err != nil {
			c.log.Warn("failed to remove pilot-in-progress label after merge", "issue", prState.IssueNumber, "error", err)
		}
		// GH-1302: Clean up stale pilot-failed label from prior failed attempt
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelFailed); err != nil {
			// 404 is expected if label doesn't exist - silently ignore
			c.log.Debug("pilot-failed label cleanup", "issue", prState.IssueNumber, "error", err)
		}
		// Close the issue after successful merge
		if err := c.ghClient.UpdateIssueState(ctx, c.owner, c.repo, prState.IssueNumber, "closed"); err != nil {
			c.log.Warn("failed to close issue after merge", "issue", prState.IssueNumber, "error", err)
		}
		c.log.Info("closed issue after merge", "issue", prState.IssueNumber, "pr", prState.PRNumber)

		// GH-2297: Post success comment so last comment isn't stale failure.
		// GH-2345: Guard against re-entry producing duplicate comments.
		if !prState.MergeNotificationPosted {
			comment := buildMergeCompletionComment(prState)
			if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, comment); err != nil {
				c.log.Warn("failed to post merge completion comment", "issue", prState.IssueNumber, "error", err)
			} else {
				prState.MergeNotificationPosted = true
			}
		}

		// GH-1336: Sync monitor state so dashboard shows "done" instead of stale "failed"
		if c.monitor != nil {
			taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
			c.monitor.Complete(taskID, prState.PRURL)
			c.log.Debug("updated monitor state to completed", "task", taskID, "pr", prState.PRNumber)
		}

		// GH-2279/GH-2402 + TASK-352: Self-heal execution records on merge.
		// Promotes prior "failed" rows (for the issue AND its parent epic) to
		// "completed" and stamps the PR URL so the dashboard reflects the merged
		// outcome (handles user-pushed commits, sub-issues merged via parent, etc.).
		c.selfHealForPR(ctx, prState.IssueNumber, prState.PRURL)

		// GH-1870: Sync board card to "Done" column on merge
		if c.boardSync != nil && prState.IssueNodeID != "" {
			if err := c.boardSync.UpdateProjectItemStatus(ctx, prState.IssueNodeID, c.doneStatus); err != nil {
				c.log.Warn("board sync on merge failed", "pr", prState.PRNumber, "error", err)
			}
		}
	}

	// GH-1383: Delete remote branch after successful merge
	// Branch is safe to delete — it's fully merged. If GitHub already deleted it
	// (delete_branch_on_merge setting), the API returns 404/422 which we ignore.
	if prState.BranchName != "" {
		if err := c.ghClient.DeleteBranch(ctx, c.owner, c.repo, prState.BranchName); err != nil {
			c.log.Warn("failed to delete branch after merge", "branch", prState.BranchName, "pr", prState.PRNumber, "error", err)
		} else {
			c.log.Info("deleted branch after merge", "branch", prState.BranchName, "pr", prState.PRNumber)
		}
	}

	// Notify merge success
	if c.notifier != nil {
		if err := c.notifier.NotifyMerged(ctx, prState); err != nil {
			c.log.Warn("failed to send merge notification", "error", err)
		}
	}

	return nil
}

// handleMerged runs post-merge deployer and checks post-merge CI based on environment config.
func (c *Controller) handleMerged(ctx context.Context, prState *PRState) error {
	c.log.Info("handleMerged: PR merged, checking next steps",
		"pr", prState.PRNumber,
		"env", c.config.EnvironmentName(),
		"should_release", c.shouldTriggerRelease(),
	)

	// Run deployer if configured (webhook, branch-push).
	// Tag action is a no-op here — handled by the releaser stage.
	if c.deployer != nil {
		if err := c.deployer.Deploy(ctx, prState); err != nil {
			c.log.Error("post-merge deploy failed", "pr", prState.PRNumber, "error", err)
			return fmt.Errorf("deploy failed: %w", err)
		}
	}

	// GH-1823: Learn from PR reviews (self-improvement).
	// Fetch reviews and line-level comments after merge, when the review cycle is complete.
	if c.learningLoop != nil {
		reviews, err := c.ghClient.ListPullRequestReviews(ctx, c.owner, c.repo, prState.PRNumber)
		if err != nil {
			c.log.Warn("Failed to fetch reviews for learning", slog.Any("error", err))
		} else if len(reviews) > 0 {
			var reviewData []*memory.ReviewData
			for _, r := range reviews {
				if r.Body == "" {
					continue // Skip click-only approvals
				}
				reviewData = append(reviewData, &memory.ReviewData{
					Body:     r.Body,
					State:    r.State,
					Reviewer: r.User.Login,
				})
			}

			// Also fetch line-level comments for richer signal
			comments, err := c.ghClient.GetPullRequestComments(ctx, c.owner, c.repo, prState.PRNumber)
			if err == nil {
				for _, comment := range comments {
					reviewData = append(reviewData, &memory.ReviewData{
						Body:     comment.Body,
						State:    "COMMENTED",
						Reviewer: comment.User.Login,
					})
				}
			}

			if len(reviewData) > 0 {
				projectPath := "" // resolved from prState if project path is available
				if learnErr := c.learningLoop.LearnFromReview(ctx, projectPath, reviewData, prState.PRURL); learnErr != nil {
					c.log.Warn("Failed to learn from reviews", slog.Any("error", learnErr))
				} else {
					c.log.Info("Learned from PR reviews",
						slog.Int("pr", prState.PRNumber),
						slog.Int("reviews", len(reviewData)),
					)
				}
			}
		}
	}

	// GH-2059: Extract eval task from merged PR for benchmarking.
	if c.evalStore != nil && prState.IssueNumber > 0 {
		issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
		if err != nil {
			c.log.Warn("Failed to fetch issue for eval task", slog.Any("error", err))
		} else {
			prFiles, err := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, prState.PRNumber)
			if err != nil {
				c.log.Warn("Failed to fetch PR files for eval task", slog.Any("error", err))
			} else {
				var filenames []string
				for _, f := range prFiles {
					filenames = append(filenames, f.Filename)
				}
				evalTask := memory.ExtractEvalTask(memory.EvalInput{
					TaskID:       fmt.Sprintf("pr-%d", prState.PRNumber),
					Success:      true, // merged = successful
					IssueNumber:  prState.IssueNumber,
					IssueTitle:   issue.Title,
					Repo:         fmt.Sprintf("%s/%s", c.owner, c.repo),
					FilesChanged: filenames,
				})
				if saveErr := c.evalStore.SaveEvalTask(evalTask); saveErr != nil {
					c.log.Warn("Failed to save eval task", slog.Any("error", saveErr))
				} else {
					c.log.Info("Saved eval task from merged PR",
						slog.Int("pr", prState.PRNumber),
						slog.Int("issue", prState.IssueNumber),
					)
				}
			}
		}
	}

	// GH-2086: Close parent issue when all sub-issues are done.
	c.maybeCloseParentIssue(ctx, prState)

	if c.config.ResolvedEnv().SkipPostMergeCI {
		// Fast path: skip post-merge CI, check if we should release immediately
		if c.shouldTriggerRelease() && !c.resolvedRelease().RequireCI {
			c.log.Info("skipping post-merge CI: proceeding to release",
				"pr", prState.PRNumber,
			)
			prState.Stage = StageReleasing
			return nil
		}
		c.log.Info("skipping post-merge CI: PR complete", "pr", prState.PRNumber)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Wait for post-merge CI
	c.log.Info("waiting for post-merge CI",
		"pr", prState.PRNumber,
		"env", c.config.EnvironmentName(),
	)
	prState.Stage = StagePostMergeCI
	return nil
}

// maybeCloseParentIssue checks whether the merged PR's issue is a sub-issue
// and, if all sibling sub-issues are also closed, closes the parent issue.
// All errors are logged as warnings without blocking the merge flow.
func (c *Controller) maybeCloseParentIssue(ctx context.Context, prState *PRState) {
	if prState.IssueNumber == 0 {
		return
	}

	// Fetch the sub-issue body to find parent reference.
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
	if err != nil {
		c.log.Warn("maybeCloseParentIssue: failed to fetch issue", slog.Int("issue", prState.IssueNumber), slog.Any("error", err))
		return
	}

	parentNum := github.ParseParentIssueNumber(issue.Body)
	if parentNum == 0 {
		return
	}

	// Check how many sibling sub-issues are still open.
	// Tier 1: try native GitHub sub-issues GraphQL API (more reliable, works even without text patterns).
	// Tier 2: fall back to text search when native links are absent (legacy repos use body "Parent: GH-N" only).
	openCount, hasNativeLinks, err := c.ghClient.GetOpenSubIssueCount(ctx, c.owner, c.repo, parentNum)
	if err != nil || !hasNativeLinks {
		if err != nil {
			c.log.Warn("maybeCloseParentIssue: native sub-issue count failed, falling back to search", slog.Int("parent", parentNum), slog.Any("error", err))
		} else {
			c.log.Debug("maybeCloseParentIssue: no native sub-issue links, falling back to search", slog.Int("parent", parentNum))
		}
		openCount, err = c.ghClient.SearchOpenSubIssues(ctx, c.owner, c.repo, parentNum)
		if err != nil {
			c.log.Warn("maybeCloseParentIssue: failed to search open sub-issues", slog.Int("parent", parentNum), slog.Any("error", err))
			return
		}
	}

	if openCount > 0 {
		c.log.Info("maybeCloseParentIssue: siblings still open", slog.Int("parent", parentNum), slog.Int("open", openCount))
		return
	}

	c.closeParentNow(ctx, parentNum)
}

// closeParentNow adds pilot-done, removes stale labels, posts a summary comment,
// and closes the parent issue. All errors are logged as warnings without propagating.
func (c *Controller) closeParentNow(ctx context.Context, parentNum int) {
	c.log.Info("closeParentNow: all sub-issues done, closing parent", slog.Int("parent", parentNum))

	// Label cleanup: add pilot-done, remove stale labels.
	if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, parentNum, []string{"pilot-done"}); err != nil {
		c.log.Warn("closeParentNow: failed to add pilot-done label", slog.Int("parent", parentNum), slog.Any("error", err))
	}
	for _, stale := range []string{"pilot-failed", "pilot-in-progress", "pilot-blocked"} {
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, parentNum, stale); err != nil {
			c.log.Warn("closeParentNow: failed to remove label", slog.String("label", stale), slog.Int("parent", parentNum), slog.Any("error", err))
		}
	}

	// Post summary comment.
	comment := fmt.Sprintf("All sub-issues for GH-%d are complete. Closing parent issue automatically.", parentNum)
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, parentNum, comment); err != nil {
		c.log.Warn("closeParentNow: failed to post comment", slog.Int("parent", parentNum), slog.Any("error", err))
	}

	// Close the parent issue.
	if err := c.ghClient.UpdateIssueState(ctx, c.owner, c.repo, parentNum, "closed"); err != nil {
		c.log.Warn("closeParentNow: failed to close parent issue", slog.Int("parent", parentNum), slog.Any("error", err))
	}
}

// recoverStaleParentIssues scans open pilot parent issues at startup and closes any
// whose sub-issues are all done. Catches parents orphaned when the daemon was down.
func (c *Controller) recoverStaleParentIssues(ctx context.Context) {
	const maxRecover = 50

	candidates, err := c.ghClient.SearchOpenPilotIssuesWithSubIssues(ctx, c.owner, c.repo, maxRecover)
	if err != nil {
		c.log.Warn("recoverStaleParentIssues: search failed", slog.Any("error", err))
		return
	}

	if len(candidates) == maxRecover {
		c.log.Info("recoverStaleParentIssues: hit limit, some candidates may be skipped", slog.Int("limit", maxRecover))
	}

	closed := 0
	for _, parentNum := range candidates {
		openCount, _, err := c.ghClient.GetOpenSubIssueCount(ctx, c.owner, c.repo, parentNum)
		if err != nil {
			c.log.Warn("recoverStaleParentIssues: failed to count sub-issues", slog.Int("parent", parentNum), slog.Any("error", err))
			continue
		}
		if openCount > 0 {
			continue
		}
		c.closeParentNow(ctx, parentNum)
		closed++
	}

	c.log.Info("recoverStaleParentIssues: done", slog.Int("closed", closed))
}

// Start runs one-time startup recovery sweeps. Call before the main Run loop.
func (c *Controller) Start(ctx context.Context) {
	c.recoverStaleParentIssues(ctx)
}

// handlePostMergeCI monitors deployment/post-merge checks (non-blocking).
// Each tick calls CheckCI once and either advances the stage or returns to wait
// for the next tick, mirroring the pattern used by handleWaitingCI.
func (c *Controller) handlePostMergeCI(ctx context.Context, prState *PRState) error {
	// Capture main branch SHA on first entry; persisted so daemon restarts resume
	// monitoring the same commit rather than picking up a newer one.
	if prState.PostMergeSHA == "" {
		sha, err := c.getMainBranchSHA(ctx)
		if err != nil {
			c.log.Warn("failed to get main branch SHA, using head SHA", "error", err)
			sha = prState.HeadSHA
		}
		prState.PostMergeSHA = sha
	}

	// Start the CI timer on first tick.
	if prState.PostMergeCIStartedAt.IsZero() {
		prState.PostMergeCIStartedAt = time.Now()
	}

	// Enforce timeout using same logic as handleWaitingCI.
	ciTimeout := c.config.CIWaitTimeout
	envCITimeout := c.config.ResolvedEnv().CITimeout
	if envCITimeout > 0 && (ciTimeout == 0 || envCITimeout < ciTimeout) {
		ciTimeout = envCITimeout
	}
	if time.Since(prState.PostMergeCIStartedAt) > ciTimeout {
		c.log.Warn("post-merge CI timeout", "pr", prState.PRNumber, "waited", time.Since(prState.PostMergeCIStartedAt))
		prState.Stage = StageFailed
		prState.Error = fmt.Sprintf("post-merge CI timeout after %v", ciTimeout)
		return nil
	}

	mainSHA := prState.PostMergeSHA
	status, err := c.ciMonitor.CheckCI(ctx, mainSHA)
	if err != nil {
		// Transient API error — log and retry next tick without failing the PR.
		c.log.Warn("post-merge CI status check failed", "pr", prState.PRNumber, "sha", ShortSHA(mainSHA), "error", err)
		return nil
	}

	prState.CIStatus = status
	prState.LastChecked = time.Now()

	switch status {
	case CISuccess:
		c.log.Info("post-merge CI passed", "pr", prState.PRNumber, "sha", ShortSHA(mainSHA))
		if c.shouldTriggerRelease() {
			prState.Stage = StageReleasing
			return nil
		}
		c.removePR(prState.PRNumber)

	case CIFailure:
		c.log.Warn("post-merge CI failed", "pr", prState.PRNumber, "sha", ShortSHA(mainSHA))
		failedChecks, _ := c.ciMonitor.GetFailedChecks(ctx, mainSHA)
		// GH-1567: Fetch CI error logs for post-merge failures too.
		ciLogs := c.ciMonitor.GetFailedCheckLogs(ctx, mainSHA, 2000)
		// Post-merge failures start a new lineage (iteration 1), not part of pre-merge cascade.
		issueNum, issueErr := c.feedbackLoop.CreateFailureIssue(ctx, prState, FailureCIPostMerge, failedChecks, ciLogs, 1)
		if issueErr != nil {
			c.log.Error("failed to create post-merge fix issue", "error", issueErr)
		} else {
			c.log.Info("created fix issue for post-merge CI failure", "pr", prState.PRNumber, "issue", issueNum)
		}
		// GH-1964/GH-1979: Learn from post-merge CI failure patterns (self-improvement).
		// Guard: skip learning when CI logs are empty/whitespace (nothing to extract).
		if c.learningLoop != nil && strings.TrimSpace(ciLogs) != "" {
			projectPath := c.owner + "/" + c.repo
			if learnErr := c.learningLoop.LearnFromCIFailure(ctx, projectPath, ciLogs, failedChecks); learnErr != nil {
				c.log.Warn("Failed to learn from post-merge CI failure", slog.Any("error", learnErr))
			}
		}
		c.removePR(prState.PRNumber)

	default:
		// CIPending or CIRunning — stay in StagePostMergeCI and wait for next tick.
		c.log.Debug("post-merge CI still running", "pr", prState.PRNumber, "sha", ShortSHA(mainSHA), "status", status)
	}

	return nil
}

// getMainBranchSHA returns the current SHA of the main branch.
//
// TASK-291: previously hardcoded "main" — this silently broke post-merge CI
// monitoring on repos defaulting to develop/master/trunk (releases could fire
// before main-branch CI completed). Now reads ResolvedEnv().Branch and falls
// back to literal "main" with a WARN log when no environment branch is set.
func (c *Controller) getMainBranchSHA(ctx context.Context) (string, error) {
	branchName := c.resolveMainBranchName()
	branch, err := c.ghClient.GetBranch(ctx, c.owner, c.repo, branchName)
	if err != nil {
		return "", err
	}
	return branch.SHA(), nil
}

// resolveMainBranchName returns the branch name post-merge CI should track.
// Preference order:
//  1. c.config.ResolvedEnv().Branch — the per-environment branch (prod=main, stage=develop, etc.)
//  2. Literal "main" with a WARN log — last-resort fallback so we never block a release on an empty branch name.
//
// A broader fallback through ProjectConfig (BranchFrom/DefaultBranch) would
// require wiring the pilot global Config into autopilot.Controller — deferred
// to a follow-up; not needed for the workshop-scope incident this fix targets.
func (c *Controller) resolveMainBranchName() string {
	if env := c.config.ResolvedEnv(); env != nil && env.Branch != "" {
		return env.Branch
	}
	c.log.Warn("resolveMainBranchName: no environment branch configured, falling back to literal \"main\" — set environments.<env>.branch to silence this warning",
		"owner", c.owner,
		"repo", c.repo,
	)
	return "main"
}

// resolveRelease is a package-level helper used during construction (before Controller
// exists) and by the resolvedRelease method below. Env-scoped config wins over global.
func resolveRelease(cfg *Config) *ReleaseConfig {
	if env := cfg.ResolvedEnv(); env != nil && env.Release != nil {
		return env.Release
	}
	return cfg.Release
}

// resolvedRelease returns the effective release config, preferring per-environment
// config over global. Returns nil if neither is set.
func (c *Controller) resolvedRelease() *ReleaseConfig {
	return resolveRelease(c.config)
}

// shouldTriggerRelease returns true if auto-release is configured.
func (c *Controller) shouldTriggerRelease() bool {
	rel := c.resolvedRelease()
	return rel != nil && rel.Enabled && rel.Trigger == "on_merge"
}

// handleReleasing creates a release after successful merge and CI.
func (c *Controller) handleReleasing(ctx context.Context, prState *PRState) error {
	if c.releaser == nil {
		c.log.Debug("releaser not configured, skipping release", "pr", prState.PRNumber)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Resolve the actual repo owner/name from the PR URL.
	// Cross-repo PRs (e.g. auth-service) have a PRURL pointing to a different repo
	// than c.owner/c.repo (the pilot repo). All release API calls must target the
	// correct repo to avoid stuck-forever releasing state.
	owner, repo := prState.RepoOwnerAndName(c.owner, c.repo)

	// Race condition guard: Check if this commit already has a tag.
	// When multiple PRs merge rapidly, each triggers handleReleasing but only
	// the first should create a tag. Subsequent PRs will see their merge commit
	// is already tagged (by an earlier release) and skip.
	existingTag, err := c.ghClient.GetTagForSHA(ctx, owner, repo, prState.HeadSHA)
	if err != nil {
		// Transient lookup failure: do NOT fall through to CreateTagForRepo. If a
		// tag already exists but we couldn't see it, the create call fails with
		// "Reference already exists", returns an error, and the PR stays in
		// StageReleasing forever (re-tried every poll). Return the error so this
		// PR is retried cleanly on the next poll once the lookup recovers. (TASK-316)
		return fmt.Errorf("failed to check existing tags for PR #%d: %w", prState.PRNumber, err)
	}
	if existingTag != "" {
		c.log.Info("commit already tagged, skipping release",
			"pr", prState.PRNumber,
			"sha", ShortSHA(prState.HeadSHA),
			"tag", existingTag,
		)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Get current version from the target repo
	currentVersion, err := c.releaser.GetCurrentVersionForRepo(ctx, owner, repo)
	if err != nil {
		c.log.Warn("failed to get current version, defaulting to 0.0.0", "error", err)
		currentVersion = SemVer{}
	}

	// Get PR commits for bump detection
	commits, err := c.ghClient.GetPRCommits(ctx, owner, repo, prState.PRNumber)
	if err != nil {
		return fmt.Errorf("failed to get PR commits: %w", err)
	}

	// Detect bump type from commits
	bumpType := DetectBumpType(commits)
	prState.ReleaseBumpType = bumpType

	if !c.releaser.ShouldRelease(bumpType) {
		c.log.Info("no release needed", "pr", prState.PRNumber, "bump", bumpType)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Calculate new version
	newVersion := currentVersion.Bump(bumpType)
	rel := c.resolvedRelease()
	prState.ReleaseVersion = newVersion.String(rel.TagPrefix)

	c.log.Info("creating release",
		"pr", prState.PRNumber,
		"current", currentVersion.String(rel.TagPrefix),
		"new", prState.ReleaseVersion,
		"bump", bumpType,
	)

	// Create git tag in the correct repo
	tagName, err := c.releaser.CreateTagForRepo(ctx, owner, repo, prState, newVersion)
	if err != nil {
		// A duplicate-tag error means the commit is already released (e.g. a
		// racing PR tagged it, or our GetTagForSHA check raced the create).
		// Treat it as success so the PR drains from activePRs instead of
		// looping forever on a tag it can never re-create. (TASK-316)
		if isDuplicateTagError(err) {
			c.log.Info("tag already exists at HEAD SHA — treating as released",
				"pr", prState.PRNumber,
				"sha", ShortSHA(prState.HeadSHA),
			)
			c.removePR(prState.PRNumber)
			return nil
		}
		return fmt.Errorf("failed to create tag: %w", err)
	}

	releaseURL := fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", owner, repo, tagName)
	c.log.Info("tag created (GoReleaser will create release)",
		"pr", prState.PRNumber,
		"version", prState.ReleaseVersion,
		"tag", tagName,
	)

	// Enrich release with LLM-generated summary (best-effort, non-blocking).
	// Runs in a goroutine because it polls for GoReleaser to publish the release
	// (up to 5 min) and we don't want to block the notification or PR cleanup.
	if c.releaseSummary != nil && rel.GenerateSummary {
		go func() {
			enrichCtx, cancel := context.WithTimeout(context.Background(), releasePollTimeout+releaseSummaryTimeout)
			defer cancel()
			if err := c.releaseSummary.EnrichRelease(enrichCtx, owner, repo, tagName, commits); err != nil {
				c.log.Warn("failed to enrich release notes", "tag", tagName, "error", err)
			}
		}()
	}

	// Send notification
	if rel.NotifyOnRelease && c.notifier != nil {
		if n, ok := c.notifier.(ReleaseNotifier); ok {
			if err := n.NotifyReleased(ctx, prState, releaseURL); err != nil {
				c.log.Warn("failed to send release notification", "error", err)
			}
		}
	}

	c.removePR(prState.PRNumber)
	return nil
}

// isDuplicateTagError reports whether err indicates the git tag already exists.
// GitHub returns HTTP 422 with body {"message":"Reference already exists"} when
// POSTing /git/refs for a ref that is already present. The predicate is kept
// deliberately narrow — it matches the "already exists" signal, not generic 422s
// (e.g. validation failures), so we never swallow a real release failure. (TASK-316)
func isDuplicateTagError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// isMergeConflict returns true if the PR has merge conflicts.
// GitHub's mergeable field is computed asynchronously, so:
//   - nil means GitHub hasn't computed it yet (not a conflict)
//   - false means conflicts exist
//   - true means no conflicts
//
// We also check mergeable_state for "dirty" which explicitly means conflicts.
func (c *Controller) isMergeConflict(pr *github.PullRequest) bool {
	// Check mergeable_state first (more specific)
	if pr.MergeableState == "dirty" {
		return true
	}
	// Fallback to mergeable bool
	if pr.Mergeable != nil && !*pr.Mergeable {
		return true
	}
	return false
}

// handleMergeConflict tries to auto-rebase the PR branch first. If that fails,
// falls back to closing the PR and returning the issue to the queue.
// GH-1796: Saves ~$8-15 per run by avoiding full re-execution for trivial conflicts.
func (c *Controller) handleMergeConflict(ctx context.Context, prState *PRState) error {
	c.log.Warn("merge conflict detected",
		"pr", prState.PRNumber,
		"issue", prState.IssueNumber,
		"branch", prState.BranchName,
	)

	// Try GitHub auto-update first (merge-from-base, not true rebase)
	err := c.ghClient.UpdatePullRequestBranch(ctx, c.owner, c.repo, prState.PRNumber)
	if err == nil {
		c.log.Info("auto-rebased conflicting PR", "pr", prState.PRNumber)
		prState.Stage = StageWaitingCI // rebase triggers new CI
		prState.HeadSHA = ""           // force refresh on next tick
		return nil
	}
	c.log.Warn("auto-rebase failed, closing PR for retry", "pr", prState.PRNumber, "error", err)

	// Add comment explaining the closure
	comment := "Merge conflict detected. Auto-rebase failed — closing PR so the issue can be re-executed from updated main."
	if _, err := c.ghClient.AddPRComment(ctx, c.owner, c.repo, prState.PRNumber, comment); err != nil {
		c.log.Warn("failed to comment on conflicting PR", "pr", prState.PRNumber, "error", err)
	}

	// Close the PR
	if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
		c.log.Warn("failed to close conflicting PR", "pr", prState.PRNumber, "error", err)
	}

	// Restore issue to dispatch-ready state after conflict.
	// GH-3139/TASK-301: issue must remain OPEN with pilot label so the poller
	// can re-dispatch. Do NOT close the issue or add pilot-done here.
	if prState.IssueNumber > 0 {
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelInProgress); err != nil {
			c.log.Warn("failed to remove in-progress label", "issue", prState.IssueNumber, "error", err)
		}
		// Re-add pilot label so poller can pick up the issue on the next cycle.
		if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, prState.IssueNumber, []string{github.LabelPilot}); err != nil {
			c.log.Warn("failed to re-add pilot label on conflict", "issue", prState.IssueNumber, "error", err)
		}
		// Guard: remove pilot-done if somehow present — prevents ghost-close.
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelDone); err != nil {
			c.log.Debug("pilot-done cleanup on conflict (may not exist)", "issue", prState.IssueNumber, "error", err)
		}
	}

	prState.Stage = StageFailed
	prState.Error = "merge conflict with base branch"
	return nil
}

// removePR removes PR from tracking and cleans up the remote branch.
func (c *Controller) removePR(prNumber int) {
	c.mu.Lock()
	prState, ok := c.activePRs[prNumber]
	var branchName string
	if ok {
		branchName = prState.BranchName
		// GH-862: Clean up discovery state for this PR's SHA
		if prState.HeadSHA != "" {
			c.ciMonitor.ClearDiscovery(prState.HeadSHA)
		}
		delete(c.activePRs, prNumber)
	}
	delete(c.prFailures, prNumber)
	c.mu.Unlock()

	// Clean up remote branch for closed/failed PRs (merged PRs already handled in handleMerging)
	if branchName != "" && c.ghClient != nil {
		if err := c.ghClient.DeleteBranch(context.Background(), c.owner, c.repo, branchName); err != nil {
			c.log.Debug("branch cleanup on PR removal", "branch", branchName, "pr", prNumber, "error", err)
		} else {
			c.log.Info("deleted branch on PR removal", "branch", branchName, "pr", prNumber)
		}
	}

	c.persistRemovePR(prNumber)
	c.removePRFailures(prNumber)
	c.log.Info("PR removed from tracking", "pr", prNumber)
}

// GetActivePRs returns detached snapshots of all tracked PRs.
//
// TASK-324: each returned *PRState is a field-by-field copy taken under that PR's
// own mu (via snapshot()), so every read-only consumer (metrics.UpdateActivePRs,
// metrics_alerter, dashboard/tui, gateway/server, cmd/pilot/adapters) is race-free
// for free and can never observe a torn write. The returned pointers are NOT the
// live map entries; callers that must mutate state (e.g. processAllPRs) re-fetch the
// live pointer by PRNumber under c.mu and take that pr.mu themselves.
//
// Lock ordering: we collect the live pointers under c.mu.RLock, RELEASE c.mu, then
// take each pr.mu to snapshot. This preserves the no-deadlock invariant (never hold
// c.mu while acquiring a prState.mu).
func (c *Controller) GetActivePRs() []*PRState {
	c.mu.RLock()
	live := make([]*PRState, 0, len(c.activePRs))
	for _, pr := range c.activePRs {
		live = append(live, pr)
	}
	c.mu.RUnlock()

	prs := make([]*PRState, 0, len(live))
	for _, pr := range live {
		pr.mu.Lock()
		prs = append(prs, pr.snapshot())
		pr.mu.Unlock()
	}
	return prs
}

// GetPRState returns the state of a specific PR.
func (c *Controller) GetPRState(prNumber int) (*PRState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	pr, ok := c.activePRs[prNumber]
	return pr, ok
}

// isPRCircuitOpen checks if the per-PR circuit breaker is open.
// A PR's circuit breaker opens when it has >= MaxFailures consecutive failures.
// The counter auto-resets after FailureResetTimeout since the last failure.
func (c *Controller) isPRCircuitOpen(prNumber int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state, ok := c.prFailures[prNumber]
	if !ok {
		return false
	}

	// Auto-reset after timeout
	resetTimeout := c.config.FailureResetTimeout
	if resetTimeout == 0 {
		resetTimeout = 30 * time.Minute // Default fallback
	}
	if time.Since(state.LastFailureTime) > resetTimeout {
		return false
	}

	return state.FailureCount >= c.config.MaxFailures
}

// recordPRFailure increments the failure counter for a specific PR.
func (c *Controller) recordPRFailure(prNumber int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.prFailures[prNumber]
	if !ok {
		state = &prFailureState{}
		c.prFailures[prNumber] = state
	}

	// Check if we should reset due to timeout before incrementing
	resetTimeout := c.config.FailureResetTimeout
	if resetTimeout == 0 {
		resetTimeout = 30 * time.Minute
	}
	if !state.LastFailureTime.IsZero() && time.Since(state.LastFailureTime) > resetTimeout {
		state.FailureCount = 0
	}

	state.FailureCount++
	state.LastFailureTime = time.Now()

	c.log.Debug("recorded PR failure",
		"pr", prNumber,
		"failures", state.FailureCount,
		"max", c.config.MaxFailures,
	)

	// Persist outside lock
	go c.persistPRFailures(prNumber, state)
}

// resetPRFailures clears the failure counter for a specific PR after success.
func (c *Controller) resetPRFailures(prNumber int) {
	c.mu.Lock()
	state, hadFailures := c.prFailures[prNumber]
	if hadFailures && state.FailureCount > 0 {
		delete(c.prFailures, prNumber)
	}
	c.mu.Unlock()

	if hadFailures && state.FailureCount > 0 {
		c.log.Debug("reset PR failure counter after success", "pr", prNumber)
		c.removePRFailures(prNumber)
	}
}

// ResetCircuitBreaker resets the failure counter for all PRs.
// Call this after manual intervention or system recovery.
func (c *Controller) ResetCircuitBreaker() {
	c.mu.Lock()
	prNumbers := make([]int, 0, len(c.prFailures))
	for prNum := range c.prFailures {
		prNumbers = append(prNumbers, prNum)
	}
	c.prFailures = make(map[int]*prFailureState)
	c.mu.Unlock()

	// Persist removal of all failures
	for _, prNum := range prNumbers {
		c.removePRFailures(prNum)
	}
	c.log.Info("circuit breaker reset for all PRs", "count", len(prNumbers))
}

// ResetPRCircuitBreaker resets the failure counter for a specific PR.
// Use this when manually recovering a single PR.
func (c *Controller) ResetPRCircuitBreaker(prNumber int) {
	c.mu.Lock()
	_, hadFailures := c.prFailures[prNumber]
	delete(c.prFailures, prNumber)
	c.mu.Unlock()

	if hadFailures {
		c.removePRFailures(prNumber)
		c.log.Info("circuit breaker reset for PR", "pr", prNumber)
	}
}

// IsCircuitOpen returns true if any PR has an open circuit breaker.
// For per-PR tracking, this checks if any PR is blocked.
func (c *Controller) IsCircuitOpen() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	resetTimeout := c.config.FailureResetTimeout
	if resetTimeout == 0 {
		resetTimeout = 30 * time.Minute
	}

	for _, state := range c.prFailures {
		// Skip if timeout has passed
		if time.Since(state.LastFailureTime) > resetTimeout {
			continue
		}
		if state.FailureCount >= c.config.MaxFailures {
			return true
		}
	}
	return false
}

// IsPRCircuitOpen returns true if a specific PR's circuit breaker is open.
func (c *Controller) IsPRCircuitOpen(prNumber int) bool {
	return c.isPRCircuitOpen(prNumber)
}

// Config returns the autopilot configuration.
func (c *Controller) Config() *Config {
	return c.config
}

// GetPRFailures returns the current failure count for a specific PR.
func (c *Controller) GetPRFailures(prNumber int) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	state, ok := c.prFailures[prNumber]
	if !ok {
		return 0
	}
	return state.FailureCount
}

// TotalFailures returns the sum of all active per-PR failure counts.
// Used for dashboard display. Only counts failures within the reset timeout.
func (c *Controller) TotalFailures() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	resetTimeout := c.config.FailureResetTimeout
	if resetTimeout == 0 {
		resetTimeout = 30 * time.Minute
	}

	total := 0
	for _, state := range c.prFailures {
		// Skip expired failures
		if time.Since(state.LastFailureTime) > resetTimeout {
			continue
		}
		total += state.FailureCount
	}
	return total
}

// Metrics returns the autopilot metrics collector.
func (c *Controller) Metrics() *Metrics {
	return c.metrics
}

// recordMergeSuccess fires the three merge-success metrics counters exactly
// once per PR number per daemon lifetime. Safe to call from any path that
// observes a Pilot PR transitioning to merged (handleMerging for
// autopilot-driven merges, ScanRecentlyMergedPRs for externally-merged PRs).
// Skips the time-to-merge histogram if prState.CreatedAt is zero (defensive).
func (c *Controller) recordMergeSuccess(prState *PRState) {
	c.mu.Lock()
	if c.recordedMerges[prState.PRNumber] {
		c.mu.Unlock()
		return
	}
	c.recordedMerges[prState.PRNumber] = true
	c.mu.Unlock()

	c.metrics.RecordPRMerged()
	c.metrics.RecordIssueProcessed("success")
	if !prState.CreatedAt.IsZero() {
		c.metrics.RecordPRTimeToMerge(time.Since(prState.CreatedAt))
	}
}

// GetLastProgressAt returns the timestamp of the last PR state transition.
// Used by MetricsAlerter for deadlock detection (GH-849).
func (c *Controller) GetLastProgressAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastProgressAt
}

// IsDeadlockAlertSent returns whether a deadlock alert has been sent since the last progress.
// Used by MetricsAlerter to avoid alert spam (GH-849).
func (c *Controller) IsDeadlockAlertSent() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deadlockAlertSent
}

// MarkDeadlockAlertSent marks that a deadlock alert has been sent.
// Called by MetricsAlerter after firing a deadlock alert (GH-849).
func (c *Controller) MarkDeadlockAlertSent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadlockAlertSent = true
}

// ScanExistingPRs scans for open PRs created by Pilot and restores their state.
// This should be called on startup to track PRs that were created before the current session.
func (c *Controller) ScanExistingPRs(ctx context.Context) error {
	c.log.Info("scanning for existing Pilot PRs",
		"owner", c.owner,
		"repo", c.repo,
	)

	prs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "open")
	if err != nil {
		return fmt.Errorf("failed to list PRs: %w", err)
	}

	c.log.Debug("found open PRs", "total", len(prs))

	restored := 0
	for _, pr := range prs {
		// Filter for Pilot branches (pilot/GH-*)
		if !strings.HasPrefix(pr.Head.Ref, "pilot/GH-") {
			c.log.Debug("skipping non-Pilot PR",
				"pr", pr.Number,
				"branch", pr.Head.Ref,
			)
			continue
		}

		// Extract issue number from branch name
		var issueNum int
		if _, err := fmt.Sscanf(pr.Head.Ref, "pilot/GH-%d", &issueNum); err != nil {
			c.log.Warn("failed to parse branch name", "branch", pr.Head.Ref, "error", err)
			continue
		}

		// Skip PRs already tracked via RestoreState — OnPRCreated would clobber
		// their persisted stage (e.g. StageWaitingCI) back to StagePRCreated and
		// reset CIWaitStartedAt, making CI timers restart from zero after every
		// Pilot restart. RestoreState is authoritative for PRs in SQLite; this
		// scan only registers genuine orphans (PRs created while Pilot was down).
		c.mu.RLock()
		_, alreadyTracked := c.activePRs[pr.Number]
		c.mu.RUnlock()
		if alreadyTracked {
			c.log.Debug("skipping already-tracked PR in scan", "pr", pr.Number, "branch", pr.Head.Ref)
			continue
		}

		c.log.Info("restoring Pilot PR for tracking",
			"pr", pr.Number,
			"branch", pr.Head.Ref,
			"sha", ShortSHA(pr.Head.SHA),
			"issue", issueNum,
		)

		// Register PR via existing mechanism
		c.OnPRCreated(pr.Number, pr.HTMLURL, issueNum, pr.Head.SHA, pr.Head.Ref, "")
		c.metrics.RecordOrphanPRRegistered("startup_scan")
		restored++
	}

	c.log.Info("completed PR scan", "restored", restored, "env", c.config.EnvironmentName())
	return nil
}

// startReconciler runs a periodic loop that calls reconcileOrphanPRs once per
// minute. It is launched as a goroutine by Run and exits when ctx is cancelled.
func (c *Controller) startReconciler(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.reconcileOrphanPRs(ctx)
		}
	}
}

// reconcileOrphanPRs lists all open pilot/ PRs and registers any that are not
// currently tracked in activePRs. A PR is orphaned when OnPRCreated was never
// fired — e.g. the executor returned pr_url="" or the poller gate filtered it.
// The function is idempotent and safe to call concurrently with processAllPRs.
func (c *Controller) reconcileOrphanPRs(ctx context.Context) {
	prs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "open")
	if err != nil {
		c.log.Warn("reconciler: failed to list open PRs", "error", err)
		return
	}

	for _, pr := range prs {
		if !strings.HasPrefix(pr.Head.Ref, "pilot/GH-") {
			continue
		}

		c.mu.RLock()
		_, tracked := c.activePRs[pr.Number]
		c.mu.RUnlock()
		if tracked {
			continue
		}

		var issueNum int
		if _, err := fmt.Sscanf(pr.Head.Ref, "pilot/GH-%d", &issueNum); err != nil {
			c.log.Warn("reconciler: failed to parse branch name", "branch", pr.Head.Ref, "error", err)
			continue
		}

		c.log.Warn("reconciler: registering orphan PR",
			"pr", pr.Number,
			"branch", pr.Head.Ref,
			"issue", issueNum,
		)
		c.OnPRCreated(pr.Number, pr.HTMLURL, issueNum, pr.Head.SHA, pr.Head.Ref, "")
		c.metrics.RecordOrphanPRRegistered("reconciler")
	}
}

// ScanRecentlyMergedPRs scans for Pilot PRs that were merged externally.
// This catches PRs that need release triggering but were merged outside of
// autopilot (e.g. via `gh pr merge` or the GitHub UI).
// Called on startup and periodically from the Run loop.
func (c *Controller) ScanRecentlyMergedPRs(ctx context.Context) error {
	// TASK-356 #2 (decouple): the scan reconciles externally-merged Pilot PRs —
	// release triggering, merge metrics, execution-row self-heal, AND board
	// write-back. Run it whenever EITHER auto-release OR board sync is enabled, so
	// a board-sourced (non-releasing) setup still gets its cards moved to Done on a
	// manual merge. The release-triggering tail below is separately gated on
	// releaseEnabled so nothing tries to tag a release when release is off.
	releaseEnabled := c.shouldTriggerRelease()
	boardEnabled := c.boardSync != nil && c.doneStatus != ""
	if !releaseEnabled && !boardEnabled {
		c.log.Debug("skipping merged PR scan: neither auto-release nor board sync enabled")
		return nil
	}

	scanWindow := c.config.MergedPRScanWindow
	if scanWindow == 0 {
		scanWindow = 30 * time.Minute // Default fallback
	}

	c.log.Info("scanning for recently merged Pilot PRs",
		"owner", c.owner,
		"repo", c.repo,
		"window", scanWindow,
	)

	// List closed PRs
	prs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "closed")
	if err != nil {
		return fmt.Errorf("failed to list closed PRs: %w", err)
	}

	c.log.Debug("found closed PRs", "total", len(prs))

	cutoff := time.Now().Add(-scanWindow)
	triggered := 0

	for _, pr := range prs {
		// Filter for Pilot branches (pilot/GH-* or pilot/*)
		if !strings.HasPrefix(pr.Head.Ref, "pilot/") {
			continue
		}

		// Must be merged (not just closed)
		if !pr.Merged {
			continue
		}

		// Check if merged within scan window
		// MergedAt is RFC3339 format string
		if pr.MergedAt == "" {
			continue
		}
		mergedAt, err := time.Parse(time.RFC3339, pr.MergedAt)
		if err != nil {
			c.log.Warn("failed to parse MergedAt", "pr", pr.Number, "merged_at", pr.MergedAt, "error", err)
			continue
		}
		if mergedAt.Before(cutoff) {
			continue
		}

		// Extract issue number from branch name (optional)
		var issueNum int
		if strings.HasPrefix(pr.Head.Ref, "pilot/GH-") {
			_, _ = fmt.Sscanf(pr.Head.Ref, "pilot/GH-%d", &issueNum)
		}

		// Record merge metrics BEFORE the activePRs/release-exists skip gates
		// below — those gates exist to avoid duplicate release triggering, but
		// the metric must fire on every discovered merged Pilot PR regardless
		// of whether a release tag already exists or whether autopilot tracked
		// the PR through creation. recordMergeSuccess is idempotent via
		// recordedMerges so handleMerging + scanner can both call it.
		// Use pr.CreatedAt for a meaningful time-to-merge sample; fall back to
		// mergedAt so the histogram still records on PRs missing CreatedAt.
		createdAt, _ := time.Parse(time.RFC3339, pr.CreatedAt)
		if createdAt.IsZero() {
			createdAt = mergedAt
		}
		c.recordMergeSuccess(&PRState{PRNumber: pr.Number, CreatedAt: createdAt})

		// TASK-352: Self-heal execution records for externally-merged PRs (gh pr
		// merge / GitHub UI). These never pass through handleMerging, so their
		// "failed" rows would otherwise never flip to "completed". Like
		// recordMergeSuccess above, this fires before the release-tag/activePRs skip
		// gates because the heal must happen on every discovered merged Pilot PR.
		c.selfHealForPR(ctx, issueNum, pr.HTMLURL)

		// TASK-356 #2: board write-back for externally-merged PRs. Large PRs that
		// hit the stage approval-misconfig (require_approval=true + approval disabled)
		// are merged manually (`gh pr merge` / GitHub UI) and never pass through
		// handleMerging, so their board card stays stuck "In Review". Move it to Done
		// here, mirroring the on-merge write-back in handleMerging. Like
		// recordMergeSuccess/selfHealForPR above, this fires on every discovered merged
		// Pilot PR (before the release-tag/activePRs skip gates) and is independent of
		// whether release is enabled. UpdateProjectItemStatus is idempotent and silently
		// skips issues that aren't on the board.
		if boardEnabled && issueNum > 0 {
			if nodeID, nodeErr := c.ghClient.GetIssueNodeID(ctx, c.owner, c.repo, issueNum); nodeErr != nil {
				c.log.Warn("board sync on external merge: failed to resolve issue node id",
					"pr", pr.Number, "issue", issueNum, "error", nodeErr)
			} else if err := c.boardSync.UpdateProjectItemStatus(ctx, nodeID, c.doneStatus); err != nil {
				c.log.Warn("board sync on external merge failed",
					"pr", pr.Number, "issue", issueNum, "error", err)
			}
		}

		// Everything below is release-triggering machinery — skip it entirely when
		// release is disabled (the scan may be running for board sync alone).
		if !releaseEnabled {
			continue
		}

		// Skip if already tracked in activePRs (avoid duplicate processing)
		c.mu.RLock()
		_, alreadyTracked := c.activePRs[pr.Number]
		c.mu.RUnlock()
		if alreadyTracked {
			continue
		}

		// B3 (TASK-309): activePRs is in-memory only. After a daemon restart a PR
		// can be persisted at stage='releasing' yet be absent from activePRs, so the
		// in-memory gate above would re-register and re-trigger the release on every
		// scan. Consult the persistent state: if a recent 'releasing' row exists, the
		// release is already in flight — skip it. Stale rows (age past
		// releasingStaleThreshold) are intentionally NOT skipped so a genuinely
		// wedged release can be re-driven.
		if c.stateStore != nil {
			if age, found, err := c.stateStore.PersistedReleasingAge(pr.Number); err != nil {
				c.log.Warn("failed to check persisted releasing state, will track to be safe",
					"pr", pr.Number,
					"error", err,
				)
			} else if found && age < releasingStaleThreshold {
				c.log.Debug("skipping PR: release already in flight (persisted at releasing)",
					"pr", pr.Number,
					"age", age,
				)
				continue
			}
		}

		// Skip if this merge commit already has a release tag.
		// GitHub releases set target_commitish to the branch ref ("main"), not the merge
		// SHA, so the former map-based check was unreliable. GetTagForSHA (same primitive
		// handleReleasing uses) is the reliable check.
		if pr.MergeCommitSHA != "" {
			existingTag, tagErr := c.ghClient.GetTagForSHA(ctx, c.owner, c.repo, pr.MergeCommitSHA)
			if tagErr != nil {
				c.log.Warn("failed to check existing tag for PR, will track to be safe",
					"pr", pr.Number,
					"merge_sha", ShortSHA(pr.MergeCommitSHA),
					"error", tagErr,
				)
			} else if existingTag != "" {
				c.log.Debug("skipping PR: merge commit already tagged",
					"pr", pr.Number,
					"merge_sha", ShortSHA(pr.MergeCommitSHA),
					"tag", existingTag,
				)
				continue
			}
		}

		c.log.Info("found merged Pilot PR needing release",
			"pr", pr.Number,
			"branch", pr.Head.Ref,
			"merged_at", mergedAt,
			"merge_sha", ShortSHA(pr.MergeCommitSHA),
		)

		// Create PR state and trigger release
		prState := &PRState{
			PRNumber:        pr.Number,
			PRURL:           pr.HTMLURL,
			IssueNumber:     issueNum,
			BranchName:      pr.Head.Ref,
			HeadSHA:         pr.MergeCommitSHA,
			Stage:           StageReleasing,
			CIStatus:        CISuccess, // Assume CI passed if merged
			CreatedAt:       time.Now(),
			EnvironmentName: c.config.EnvironmentName(),
			PRTitle:         pr.Title,
			TargetBranch:    pr.Base.Ref,
		}

		// Register and trigger release
		c.mu.Lock()
		c.activePRs[pr.Number] = prState
		c.mu.Unlock()
		// prState is now published in activePRs, so a concurrent ProcessPR or
		// webhook could already hold the pointer — persist under prState.mu per
		// the caller-holds-the-lock contract (mirrors OnPRCreated).
		prState.mu.Lock()
		c.persistPRState(prState)
		prState.mu.Unlock()

		triggered++
	}

	c.log.Info("completed merged PR scan",
		"triggered", triggered,
		"window", scanWindow,
	)

	return nil
}

// Run starts the autopilot processing loop.
// It continuously processes all active PRs until context is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	c.log.Info("autopilot controller started",
		"env", c.config.EnvironmentName(),
		"poll_interval", c.config.CIPollInterval,
		"ci_timeout", c.config.CIWaitTimeout,
		"auto_merge", c.config.AutoMerge,
		"release_enabled", c.resolvedRelease() != nil && c.resolvedRelease().Enabled,
	)

	// Dynamic poll interval settings
	basePollInterval := c.config.CIPollInterval
	fastPollInterval := 10 * time.Second
	idlePollInterval := 60 * time.Second
	currentInterval := basePollInterval

	// GH-3113: Periodic reconciliation loop — registers orphan PRs that OnPRCreated missed.
	go c.startReconciler(ctx)

	// GH-2251: Periodic scan for externally-merged PRs.
	// Use half the scan window as the interval so merges are detected well within the window.
	mergedScanInterval := c.config.MergedPRScanWindow / 2
	if mergedScanInterval < 5*time.Minute {
		mergedScanInterval = 5 * time.Minute
	}
	mergedScanTicker := time.NewTicker(mergedScanInterval)
	defer mergedScanTicker.Stop()

	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("autopilot controller stopping")
			return ctx.Err()
		case <-mergedScanTicker.C:
			// GH-2251: Periodically scan for externally-merged PRs that
			// were never tracked by autopilot (e.g. merged via gh pr merge).
			if err := c.ScanRecentlyMergedPRs(ctx); err != nil {
				c.log.Warn("periodic merged PR scan failed", "error", err)
			}
		case <-ticker.C:
			c.processAllPRs(ctx)

			// Adjust interval based on active PR states
			newInterval := idlePollInterval
			activePRs := c.GetActivePRs()
			for _, pr := range activePRs {
				if pr.Stage == StageWaitingCI || pr.Stage == StagePRCreated {
					newInterval = fastPollInterval
					break
				}
			}

			// Update ticker interval if it changed
			if newInterval != currentInterval {
				c.log.Debug("adjusting poll interval",
					"old_interval", currentInterval,
					"new_interval", newInterval,
					"active_prs", len(activePRs),
				)
				ticker.Reset(newInterval)
				currentInterval = newInterval
			}
		}
	}
}

// processAllPRs processes all active PRs in one iteration.
func (c *Controller) processAllPRs(ctx context.Context) {
	prs := c.GetActivePRs()

	// Update active PR gauges every tick
	c.metrics.UpdateActivePRs(prs)

	if len(prs) == 0 {
		return
	}

	c.log.Info("processing active PRs", "count", len(prs))

	for _, snap := range prs {
		select {
		case <-ctx.Done():
			return
		default:
			c.log.Debug("checking PR",
				"pr", snap.PRNumber,
				"stage", snap.Stage,
				"ci_status", snap.CIStatus,
			)

			// TASK-324: `snap` is a detached snapshot from GetActivePRs. Re-fetch the
			// LIVE pointer by number so the pre-ProcessPR mutations below (and
			// checkExternalMergeOrClose) operate on the shared state under its mutex.
			c.mu.RLock()
			pr, ok := c.activePRs[snap.PRNumber]
			c.mu.RUnlock()
			if !ok {
				// PR was removed between snapshot and now — skip.
				continue
			}

			// Fetch PR once, use twice - cache to avoid redundant API calls
			ghPR, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, pr.PRNumber)
			if err != nil {
				c.log.Warn("failed to fetch PR", "pr", pr.PRNumber, "error", err)
				continue
			}

			// TASK-324: hold pr.mu around the external-merge/close check and the
			// polling-mode changes-requested read-modify-write + persist. Release it
			// BEFORE calling ProcessPR, which re-acquires pr.mu for its whole body
			// (Go's sync.Mutex is non-reentrant). Lock ordering preserved: pr.mu is
			// taken before any c.mu that checkExternalMergeOrClose→removePR acquires.
			pr.mu.Lock()
			externallyResolved := c.checkExternalMergeOrClose(ctx, pr, ghPR)
			if externallyResolved {
				pr.mu.Unlock()
				continue
			}

			// Detect changes_requested reviews in polling mode (webhook mode uses OnReviewRequested).
			// Only check PRs that haven't already been transitioned to review_requested.
			if pr.Stage != StageReviewRequested && pr.Stage != StageFailed &&
				c.config.ReviewFeedback != nil && c.config.ReviewFeedback.Enabled {
				if c.hasChangesRequested(ctx, pr) {
					c.log.Info("detected changes_requested review in polling mode",
						"pr", pr.PRNumber,
						"stage", pr.Stage,
					)
					pr.Stage = StageReviewRequested
					c.persistPRState(pr)
				}
			}
			pr.mu.Unlock()

			if err := c.ProcessPR(ctx, pr.PRNumber, ghPR); err != nil {
				// Error already logged in ProcessPR
				continue
			}
		}
	}
}

// checkExternalMergeOrClose checks if a PR was merged or closed externally (by human).
// Returns true if the PR was removed from tracking, false otherwise.
// Accepts cached ghPR to avoid redundant API calls.
func (c *Controller) checkExternalMergeOrClose(ctx context.Context, prState *PRState, ghPR *github.PullRequest) bool {

	// Check if PR was merged externally
	if ghPR.Merged {
		c.log.Info("PR merged externally", "pr", prState.PRNumber)
		c.notifyExternalMerge(ctx, prState)

		// GH-1486: Close associated issue and add pilot-done label on external merge
		if prState.IssueNumber > 0 {
			// Add pilot-done label
			if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, prState.IssueNumber, []string{github.LabelDone}); err != nil {
				c.log.Warn("failed to add pilot-done label after external merge", "issue", prState.IssueNumber, "error", err)
			}
			// Remove pilot-in-progress label
			if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelInProgress); err != nil {
				c.log.Debug("pilot-in-progress label cleanup on external merge", "issue", prState.IssueNumber, "error", err)
			}
			// Remove pilot-failed label (cleanup from prior failed attempt)
			if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelFailed); err != nil {
				c.log.Debug("pilot-failed label cleanup on external merge", "issue", prState.IssueNumber, "error", err)
			}
			// Close the issue
			if err := c.ghClient.UpdateIssueState(ctx, c.owner, c.repo, prState.IssueNumber, "closed"); err != nil {
				c.log.Warn("failed to close issue after external merge", "issue", prState.IssueNumber, "error", err)
			} else {
				c.log.Info("closed issue after external merge", "issue", prState.IssueNumber, "pr", prState.PRNumber)

				// GH-2297: Post success comment so last comment isn't stale failure
				comment := buildMergeCompletionComment(prState)
				if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, comment); err != nil {
					c.log.Warn("failed to post merge completion comment on external merge", "issue", prState.IssueNumber, "error", err)
				}
			}
		}

		// GH-411: Trigger release for externally merged PRs if auto-release is enabled
		if c.shouldTriggerRelease() && prState.Stage != StageReleasing {
			c.log.Info("triggering release for externally merged PR", "pr", prState.PRNumber)
			// Update SHA to merge commit if available
			if ghPR.MergeCommitSHA != "" {
				prState.HeadSHA = ghPR.MergeCommitSHA
			}
			prState.Stage = StageReleasing
			c.persistPRState(prState)
			return false // Continue processing to handle release
		}

		c.removePR(prState.PRNumber)
		return true
	}

	// Check if PR was closed (without merge) externally
	if ghPR.State == "closed" {
		c.log.Info("PR closed externally, removing from tracking", "pr", prState.PRNumber)
		c.notifyExternalClose(ctx, prState)
		c.removePR(prState.PRNumber)
		return true
	}

	return false
}

// notifyExternalMerge sends notification when a PR is merged externally.
func (c *Controller) notifyExternalMerge(ctx context.Context, prState *PRState) {
	if c.notifier == nil {
		return
	}

	// Reuse the existing NotifyMerged notification
	if err := c.notifier.NotifyMerged(ctx, prState); err != nil {
		c.log.Warn("failed to send external merge notification", "pr", prState.PRNumber, "error", err)
	}
}

// notifyExternalClose sends notification when a PR is closed externally without merge.
// GH-1015: Marks the issue as pilot-retry-ready so it can be re-picked by the poller.
func (c *Controller) notifyExternalClose(ctx context.Context, prState *PRState) {
	c.log.Info("PR closed externally without merge", "pr", prState.PRNumber, "issue", prState.IssueNumber)

	// GH-1015: Add pilot-retry-ready label so the issue can be retried
	// Remove pilot-in-progress to allow the poller to re-pick it
	if prState.IssueNumber > 0 {
		// GH-2340: Skip pilot-retry-ready when the issue already carries
		// pilot-done. This happens when Pilot itself closed a duplicate PR
		// (e.g. via handleMergeConflict) after the original PR was already
		// merged. Adding pilot-retry-ready in that case strands the label
		// on a closed/done issue forever (poller skips non-open issues).
		issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
		if err != nil {
			c.log.Warn("failed to fetch issue for label check", "issue", prState.IssueNumber, "error", err)
		} else if github.HasLabel(issue, github.LabelDone) {
			c.log.Info("skipping pilot-retry-ready: issue already pilot-done", "issue", prState.IssueNumber, "pr", prState.PRNumber)
			c.maybeCloseParentIssue(ctx, prState)
			return
		}

		if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, prState.IssueNumber, []string{github.LabelRetryReady}); err != nil {
			c.log.Warn("failed to add pilot-retry-ready label", "issue", prState.IssueNumber, "error", err)
		}
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelInProgress); err != nil {
			c.log.Warn("failed to remove pilot-in-progress label", "issue", prState.IssueNumber, "error", err)
		}
		// Remove stale pilot-failed label (GH-1302 gap)
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelFailed); err != nil {
			c.log.Debug("failed to remove pilot-failed (may not exist)", "issue", prState.IssueNumber, "error", err)
		}
		c.log.Info("marked issue as pilot-retry-ready (PR closed without merge)", "issue", prState.IssueNumber, "pr", prState.PRNumber)
	}

	// GH-2198: Close parent epic when all sub-issues are done (even if this one
	// was closed without merge). maybeCloseParentIssue no-ops for non-sub-issues.
	c.maybeCloseParentIssue(ctx, prState)
}

// MultiControllerStateWriter routes approval decisions to whichever controller
// owns the matching ApprovalRequestID. Use this when multiple controllers share
// a single approval.Manager (multi-repo deployments).
type MultiControllerStateWriter struct {
	controllers []*Controller
}

// NewMultiControllerStateWriter creates a writer that delegates SetApprovalDecision
// to each controller in order, stopping at the first match.
func NewMultiControllerStateWriter(controllers ...*Controller) *MultiControllerStateWriter {
	return &MultiControllerStateWriter{controllers: controllers}
}

// SetApprovalDecision implements approval.PRStateWriter by trying each controller.
func (w *MultiControllerStateWriter) SetApprovalDecision(ctx context.Context, requestID string, decision string, by string) error {
	for _, c := range w.controllers {
		if err := c.SetApprovalDecision(ctx, requestID, decision, by); err != nil {
			return err
		}
	}
	return nil
}
