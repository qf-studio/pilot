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

	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// alertSink is the minimal interface the controller needs to fire alerts.
// Satisfied by *alerts.Engine; kept as an interface so tests can inject a
// fake sink instead of standing up a real engine. GH-3927.
type alertSink interface {
	ProcessEvent(alerts.Event)
}

// approvalPersister is the subset of memory.Store used for approval persistence
// and execution-event audit-trail writes in the executions / execution_events
// tables. GH-3847: PR stage transitions are recorded here so the audit trail
// survives autopilot's own PR-state-row cleanup after merge (state_store.go
// deletes the row; execution_events is keyed off executions.id, not the PR
// state row, so it is unaffected).
type approvalPersister interface {
	SetApprovalRequestID(ctx context.Context, taskID, requestID string) error
	SetApprovalDecision(ctx context.Context, requestID, decision, by string) error
	GetLatestExecutionByTaskID(taskID string) (*memory.Execution, error)
	InsertExecutionEvent(executionID string, stage memory.Stage, detail string) error
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
	// GetExecutionStatusByTaskID returns the status of the most recent execution
	// row exactly matching taskID and projectPath (no substring fallback) — used
	// to verify a child sub-issue's ledger status before treating it as complete
	// for parent-close purposes. GH-3780.
	GetExecutionStatusByTaskID(taskID, projectPath string) (string, error)
	// ReclassifyCompletionAsFailed demotes a genuine completed execution row to
	// "failed" with reason, so a PR closed without merging can never leave a
	// "completed" row behind that HasCompletedExecution keeps trusting. GH-3818.
	ReclassifyCompletionAsFailed(taskID, projectPath, reason string) error
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

// WithReleaseOverride wires a per-project release config overlay (GH-3930)
// for this controller's repo. Nil is a no-op. NewController applies the
// overlay (ProjectReleaseConfig.Apply) against the resolved global/env
// ReleaseConfig before constructing the releaser, so options must be applied
// before that point — see the options loop at the top of NewController. GH-3926.
func WithReleaseOverride(o *ProjectReleaseConfig) ControllerOption {
	return func(c *Controller) {
		c.projectRelease = o
	}
}

// WithReleaseNotOptedIn marks this controller's repo as NOT opted into
// release automation via a project-level `release:` block (GH-4001).
// Release automation for projects-loop controllers is per-project opt-in: a
// repo with no `release:` block must never inherit the global/env cascade
// (a forgotten repo silently tagging releases has caused two incidents —
// studio-sdk 2026-07-06, Navigator 2026-07-07 near-miss). This forces the
// resolved release config to disabled regardless of global/env settings,
// and tags the "resolved release policy" startup log with
// source=project-not-opted-in so the posture is loud and greppable rather
// than silently inherited. Do not combine with WithReleaseOverride on the
// same controller — whichever option runs last wins.
func WithReleaseNotOptedIn() ControllerOption {
	return func(c *Controller) {
		disabled := false
		c.projectRelease = &ProjectReleaseConfig{Enabled: &disabled}
		c.releaseNotOptedIn = true
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

	// projectRelease is the per-project release config overlay (GH-3930),
	// wired via WithReleaseOverride. Nil = no project-level override. Applied
	// once during NewController — see resolvedReleaseCfg. GH-3926.
	projectRelease *ProjectReleaseConfig

	// releaseNotOptedIn is true when projectRelease was synthesized by
	// WithReleaseNotOptedIn rather than a real per-project `release:` block
	// (GH-4001). Only affects the "resolved release policy" log source tag —
	// resolvedReleaseCfg is already forced disabled either way.
	releaseNotOptedIn bool

	// resolvedReleaseCfg is the effective release config computed once in
	// NewController: env-scoped config wins over global, then projectRelease
	// (if any) is overlaid on top. resolvedRelease() returns this directly —
	// it is NOT recomputed per call, so it reflects exactly what c.releaser
	// was constructed with. Nil = releasing is not configured at any level. GH-3926.
	resolvedReleaseCfg *ReleaseConfig

	// GH-3271: called after a PR merges and pilot-done is applied so pollers
	// can immediately re-mark the issue as processed, closing the merge→done
	// race window before label propagation catches up.
	onIssueDone func(issueNumber int)

	// alertsEngine is the alert sink wired via SetAlertsEngine (optional, nil =
	// alerting disabled for this controller). Consumed by post-tag release
	// verification (GH-3927). GH-3954: every controller must receive this via
	// main.go, not just the default one.
	alertsEngine alertSink

	// alertedMissingReleases deduplicates release_missing alerts per
	// "owner/repo@tag", guarded by mu. Needed because the alerts engine's
	// cooldown is keyed by rule name (shouldFire -> lastAlertTimes[rule.Name]),
	// not by source — without this map a second repo/tag breaking inside the
	// same cooldown window would be silently swallowed. Both afterTagCreated
	// (goroutine path) and the ScanRecentlyMergedPRs backstop share it, since a
	// hot-upgrade restart can kill the former mid-flight and let the scanner
	// catch the same tag. GH-3927.
	alertedMissingReleases map[string]bool

	// alertedStaleScopes deduplicates scope_stale alerts per scope key
	// ("epic:<N>" / "label:<name>"), guarded by mu — same rationale as
	// alertedMissingReleases (GH-3991).
	alertedStaleScopes map[string]bool

	// alertedPersistFailures deduplicates pr_persist_failed alerts per PR
	// number, guarded by mu — same rationale as alertedMissingReleases: a
	// wedged PR retries every tick, and without this map the alerts engine's
	// per-rule cooldown would still let through a repeat every cooldown
	// window instead of firing exactly once per PR (GH-4053).
	alertedPersistFailures map[int]bool

	// persistFailedPRs records, per PR number, when persistPRState evicted the
	// PR after persistFailureEvictThreshold consecutive SavePRState failures.
	// Guarded by mu. reconcileOrphanPRs and restorePilotPRs consult this to
	// skip re-adopting a PR whose state store row cannot be saved, within
	// persistFailureReadoptCooldown — otherwise the 60s reconciler sweep would
	// immediately re-adopt the still-open PR and repeat the identical
	// adopt-fail-evict cycle forever (GH-4053).
	persistFailedPRs map[int]time.Time

	// epicVeto tracks, per epic parent issue number, how many consecutive
	// reconcile passes have failed the SAME close-veto (same blocking child +
	// same reason), guarded by mu. Lets reconcileEpicParent tell "still
	// converging" (veto changes or clears) apart from "permanently stuck"
	// (identical veto, epicCloseVetoBreakerThreshold times running) and break
	// the re-dispatch loop instead of burning tokens forever (GH-4006).
	// In-memory only: a daemon restart resets the streak, which only delays
	// (never skips) the eventual escalation.
	epicVeto map[int]*epicCloseVetoTracking

	// cachedBotLogin holds the authenticated GitHub login for the Pilot token.
	// Populated lazily by getBotLogin; protected by mu. GH-3417.
	cachedBotLogin string

	// rateLimitedUntil holds off processAllPRs/reconcileOrphanPRs/ScanRecentlyMergedPRs
	// after a GitHub primary-rate-limit response, instead of re-hitting the API on every
	// PR on every tick until quota resets. Protected by mu. GH-3784: a sustained rate-limit
	// window with no backoff left green, approved PRs unmerged for over an hour because
	// every tick burned through the exhausted quota re-fetching every tracked PR.
	rateLimitedUntil time.Time
}

// NewController creates an autopilot controller with all required components.
func NewController(cfg *Config, ghClient *github.Client, approvalMgr *approval.Manager, owner, repo string, opts ...ControllerOption) *Controller {
	c := &Controller{
		config:                 cfg,
		ghClient:               ghClient,
		approvalMgr:            approvalMgr,
		owner:                  owner,
		repo:                   repo,
		activePRs:              make(map[int]*PRState),
		recordedMerges:         make(map[int]bool),
		prFailures:             make(map[int]*prFailureState),
		lastProgressAt:         time.Now(), // Initialize to now to avoid false alarm on startup
		metrics:                NewMetrics(),
		log:                    slog.Default().With("component", "autopilot"),
		alertedMissingReleases: make(map[string]bool),
		alertedStaleScopes:     make(map[string]bool),
		alertedPersistFailures: make(map[int]bool),
		persistFailedPRs:       make(map[int]time.Time),
		epicVeto:               make(map[int]*epicCloseVetoTracking),
	}

	// Options must apply before the releaser is constructed below: the
	// per-project release overlay (WithReleaseOverride) needs c.projectRelease
	// set so the resolved+overlaid config below reflects it, rather than the
	// options loop overwriting c.projectRelease after the releaser already
	// picked up the un-overlaid config (GH-3926). Every other option (board
	// sync, project path, memory store, ...) only sets plain fields, so
	// running them first is safe.
	for _, opt := range opts {
		opt(c)
	}

	c.ciMonitor = NewCIMonitor(ghClient, owner, repo, cfg)
	c.autoMerger = NewAutoMerger(ghClient, approvalMgr, c.ciMonitor, owner, repo, cfg)
	c.feedbackLoop = NewFeedbackLoop(ghClient, owner, repo, cfg)

	// Resolve the effective release config: env-scoped wins over global, then
	// the per-project overlay (if any) is applied on top (GH-3926/GH-3930).
	// Stored once on the controller — resolvedRelease() returns this value
	// directly rather than recomputing it, so it always matches what the
	// releaser below was constructed with.
	baseRelCfg := resolveRelease(cfg)
	relSource := "none"
	if env := cfg.ResolvedEnv(); env != nil && env.Release != nil {
		relSource = "env:" + cfg.EnvironmentName()
	} else if cfg.Release != nil {
		relSource = "global"
	}
	relCfg := baseRelCfg
	if c.projectRelease != nil {
		relCfg = c.projectRelease.Apply(baseRelCfg)
		switch {
		case c.releaseNotOptedIn:
			// GH-4001: no per-project `release:` block — never inherit the
			// global/env cascade, regardless of relSource above.
			relSource = "project-not-opted-in"
		case relSource == "none":
			relSource = "project-only"
		default:
			relSource += "+project-overlay"
		}
	}
	c.resolvedReleaseCfg = relCfg

	publishMode := ""
	if relCfg != nil {
		publishMode = relCfg.PublishMode()
	}
	c.log.Info("resolved release policy",
		"enabled", relCfg != nil && relCfg.Enabled,
		"source", relSource,
		"publish", publishMode,
	)
	if relCfg != nil && relCfg.Enabled {
		c.releaser = NewReleaser(ghClient, owner, repo, relCfg)
	}

	// Initialize deployer if post-merge config exists
	if env := cfg.ResolvedEnv(); env.PostMerge != nil && env.PostMerge.Action != "" && env.PostMerge.Action != "none" {
		c.deployer = NewDeployer(ghClient, owner, repo, env.PostMerge)
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
	// GH-3513/GH-3530: heal the parent ONLY once all its children are closed —
	// healing on the first child's merge stamped that child's PR URL on the
	// parent's row and marked it "completed", which woke a hung WaitForExecution
	// with a false success and fed HasCompletedExecution dispatch-skips while
	// sibling slices were still unshipped.
	if parent := c.resolveParentIssue(ctx, issueNum); parent != 0 && parent != issueNum {
		open, err := c.openSubIssueCount(ctx, parent)
		if err != nil {
			c.log.Warn("selfHealForPR: sub-issue count failed — not healing parent row",
				"parent", parent, "child", issueNum, "error", err)
			return
		}
		if open > 0 {
			c.log.Info("selfHealForPR: parent has open children — not healing parent row",
				"parent", parent, "child", issueNum, "open", open)
			return
		}
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

// repoKey returns this controller's "owner/repo" identity, used to scope
// every StateStore read/write so a pr_number collision with another repo
// sharing the same SQLite DB can never be restored or acted on by this
// controller (GH-3903).
func (c *Controller) repoKey() string {
	return c.owner + "/" + c.repo
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

// persistFailureEvictThreshold bounds how many consecutive SavePRState
// failures are tolerated for one PR before persistPRState evicts it from
// tracking. Mirrors notFoundEvictionThreshold's rationale: a row this
// controller cannot persist (e.g. a schema/ON CONFLICT mismatch surfaced by a
// reconciler-adopted or otherwise irregular row) can never advance stage —
// every handler ends in a persist call — so without an eviction it retries
// silently forever instead of escalating (GH-4053).
const persistFailureEvictThreshold = 5

// persistFailureReadoptCooldown bounds how long reconcileOrphanPRs and
// restorePilotPRs skip re-adopting a PR evicted by evictPersistFailedPR. A
// short cooldown (rather than a permanent skip) lets the same PR be retried
// once whatever caused the persist failure might have cleared (e.g. a state
// store hot-swap or migration on daemon restart, which also resets this
// in-memory map) — but stops the 60s reconciler sweep from re-adopting an
// unpersistable PR every single tick (GH-4053).
const persistFailureReadoptCooldown = 1 * time.Hour

// persistPRState saves a PR state to the store if available.
//
// TASK-324 concurrency contract: this method is LOCK-FREE with respect to the
// per-PR mutex. The CALLER MUST hold prState.mu (so the fields read by
// stateStore.SavePRState are stable) — OR the prState must not yet be published in
// c.activePRs (e.g. freshly constructed). It must NOT take prState.mu itself: every
// caller that holds the live pointer already owns prState.mu, and re-locking would
// deadlock (Go's sync.Mutex is non-reentrant). It MAY take c.mu (via
// evictPersistFailedPR below): every call site releases c.mu before taking
// prState.mu, so prState.mu -> c.mu is the only order ever exercised here.
func (c *Controller) persistPRState(prState *PRState) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.SavePRState(c.repoKey(), prState); err != nil {
		prState.PersistFailureCount++
		failures := prState.PersistFailureCount
		c.log.Warn("failed to persist PR state", "pr", prState.PRNumber, "error", err, "consecutive_failures", failures)
		c.alertPersistFailureOnce(prState.PRNumber, err)
		if failures >= persistFailureEvictThreshold {
			c.evictPersistFailedPR(prState.PRNumber)
		}
		return
	}
	prState.PersistFailureCount = 0
}

// alertPersistFailureOnce fires a pr_persist_failed alert the first time a PR
// fails to persist, deduplicated per PR number via alertedPersistFailures — a
// wedged PR retries every processAllPRs tick, so without this dedup the
// same underlying error would otherwise only surface via repeated WARN log
// lines (GH-4053: reconciler-adopted PR #4047 looped silently on an
// "ON CONFLICT clause does not match" persist error for 22+ ticks with no
// alert ever firing).
func (c *Controller) alertPersistFailureOnce(prNumber int, persistErr error) {
	c.mu.Lock()
	if c.alertedPersistFailures == nil {
		c.alertedPersistFailures = make(map[int]bool)
	}
	if c.alertedPersistFailures[prNumber] {
		c.mu.Unlock()
		return
	}
	c.alertedPersistFailures[prNumber] = true
	c.mu.Unlock()

	msg := fmt.Sprintf(
		"PR #%d (%s) cannot be persisted to the state store: %s — it will be evicted from tracking after %d consecutive failures and cannot advance stage until then",
		prNumber, c.repoKey(), persistErr, persistFailureEvictThreshold,
	)
	if c.alertsEngine == nil {
		c.log.Error("pr_persist_failed alert not delivered: SetAlertsEngine was never called", "pr", prNumber, "error", persistErr)
		return
	}
	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventType("pr_persist_failed"),
		Error:     msg,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo": c.repoKey(),
			"pr":   strconv.Itoa(prNumber),
		},
	})
}

// evictPersistFailedPR drops a PR that has failed to persist
// persistFailureEvictThreshold times in a row from in-memory tracking, and
// records it in persistFailedPRs so reconcileOrphanPRs/restorePilotPRs won't
// immediately re-adopt it (GH-4053). Unlike a normal removePR, it does not
// attempt any GitHub-side cleanup — the row is unpersistable, not resolved,
// and a stuck PR should escalate for human attention (the alert already
// fired), not have its branch/labels touched based on incomplete state.
//
// persistRemovePR issues a plain DELETE (no ON CONFLICT clause), so it is
// expected to succeed even when the upsert path that got the PR into this
// state cannot — clearing the stuck row is the same one-time reconciliation
// a human would otherwise run by hand.
func (c *Controller) evictPersistFailedPR(prNumber int) {
	c.mu.Lock()
	delete(c.activePRs, prNumber)
	delete(c.prFailures, prNumber)
	delete(c.recordedMerges, prNumber)
	if c.persistFailedPRs == nil {
		c.persistFailedPRs = make(map[int]time.Time)
	}
	c.persistFailedPRs[prNumber] = time.Now()
	c.mu.Unlock()

	c.persistRemovePR(prNumber)
	c.removePRFailures(prNumber)
	c.log.Error("evicted PR after repeated persist failures — state store cannot save this PR's row",
		"pr", prNumber, "repo", c.repoKey(), "threshold", persistFailureEvictThreshold)
}

// recentlyEvictedForPersistFailure reports whether prNumber was evicted by
// evictPersistFailedPR within persistFailureReadoptCooldown, so orphan-PR
// adoption (reconciler sweep and startup scan) can skip it instead of
// re-registering a PR whose row the state store just proved it cannot save
// (GH-4053).
func (c *Controller) recentlyEvictedForPersistFailure(prNumber int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	evictedAt, ok := c.persistFailedPRs[prNumber]
	if !ok {
		return false
	}
	return time.Since(evictedAt) < persistFailureReadoptCooldown
}

// persistRemovePR removes a PR state from the store if available.
func (c *Controller) persistRemovePR(prNumber int) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.RemovePRState(c.repoKey(), prNumber); err != nil {
		c.log.Warn("failed to remove persisted PR state", "pr", prNumber, "error", err)
	}
}

// executionEventStageFor maps a PRStage to the memory.Stage recorded in the
// execution-events audit trail. ok is false for stages with no audit-trail
// equivalent, so callers skip the write instead of logging noise for those
// transitions.
func executionEventStageFor(prStage PRStage) (memory.Stage, bool) {
	switch prStage {
	case StageWaitingCI:
		// GH-4130: persist CI-wait entry so it survives restarts — previously
		// only the in-memory PRState.CIWaitStartedAt (types.go:728) tracked this,
		// which reset to zero on every daemon restart.
		return memory.StageWaitingCI, true
	case StageCIPassed:
		return memory.StageCIPassed, true
	case StageCIFailed:
		return memory.StageCIFailed, true
	case StageAwaitApproval:
		return memory.StageAwaitingApproval, true
	case StageMerged:
		return memory.StageMerged, true
	case StageFailed:
		return memory.StageFailed, true
	default:
		return "", false
	}
}

// recordExecutionEvent writes a best-effort audit-trail entry for prState's
// current stage to the execution_events table (GH-3847). It resolves the
// execution row via the same "GH-<issue>" task ID used for approval
// persistence, so it survives autopilot's own PR-state-row cleanup — the
// event is keyed off executions.id, not the PR state row that gets deleted
// after a successful merge.
//
// Failures (no memory store wired, no matching execution row, insert error)
// are logged and swallowed: the audit trail is a diagnostic aid, not load-
// bearing for the state machine, so a lookup miss must never fail the PR's
// processing cycle.
func (c *Controller) recordExecutionEvent(prState *PRState, stage memory.Stage, detail string) {
	if c.memoryStore == nil {
		return
	}

	taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
	if prState.IssueNumber == 0 {
		taskID = fmt.Sprintf("PR-%d", prState.PRNumber)
	}

	exec, err := c.memoryStore.GetLatestExecutionByTaskID(taskID)
	if err != nil {
		c.log.Warn("execution audit trail: no execution row for task, skipping event",
			"pr", prState.PRNumber, "task_id", taskID, "stage", stage, "error", err)
		return
	}

	if err := c.memoryStore.InsertExecutionEvent(exec.ID, stage, detail); err != nil {
		c.log.Warn("execution audit trail: failed to insert execution event",
			"pr", prState.PRNumber, "execution_id", exec.ID, "stage", stage, "error", err)
	}
}

// persistPRFailures saves per-PR failure state to the store if available.
func (c *Controller) persistPRFailures(prNumber int, state *prFailureState) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.SavePRFailures(c.repoKey(), prNumber, state.FailureCount, state.LastFailureTime); err != nil {
		c.log.Warn("failed to persist PR failure state", "pr", prNumber, "error", err)
	}
}

// removePRFailures removes per-PR failure state from the store if available.
func (c *Controller) removePRFailures(prNumber int) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.RemovePRFailures(c.repoKey(), prNumber); err != nil {
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
	states, err := c.stateStore.LoadAllPRStates(c.repoKey())
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
	prFailures, err := c.stateStore.LoadAllPRFailures(c.repoKey())
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

// SetAlertsEngine wires an alert sink into the controller so future work
// (post-tag release verification, GH-3927) can call c.alertsEngine.ProcessEvent
// instead of silently dropping alert-worthy events. Nil disables alerting for
// this controller. Must be called once at startup, before Run(), same as
// SetOnIssueDone above. GH-3954.
func (c *Controller) SetAlertsEngine(engine alertSink) {
	c.alertsEngine = engine
}

// OnPRCreated registers a new PR for autopilot processing.
//
// GH-3828: the orphan-reconciler (60s sweep) and the normal poller callback
// path both race to register the same PR — the reconciler's tracked-check and
// its OnPRCreated call are two separate lock acquisitions, so a callback can
// land in between and register the PR first. Without a check here, the
// second caller would silently overwrite the first's *PRState (discarding any
// progress already made — CI wait state, escalation reason, a submitted
// approval request) and restart the whole pipeline, producing duplicate
// approval requests and duplicate "PR merged" comments. Registration must
// therefore be idempotent at the source of truth (the activePRs map, under
// c.mu), not just at each caller's pre-check.
func (c *Controller) OnPRCreated(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string) {
	c.mu.Lock()
	if _, exists := c.activePRs[prNumber]; exists {
		c.mu.Unlock()
		c.log.Debug("PR already registered, skipping duplicate registration",
			"pr", prNumber,
			"issue", issueNumber,
		)
		c.metrics.RecordDuplicateRegistrationSkipped()
		return
	}
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

	// GH-4130: observe pilot_time_to_pr_seconds / pilot_queue_wait_seconds now
	// that the PR exists — this is the first point autopilot sees the issue's
	// execution, so it's the natural place to read started_at/created_at off
	// the execution row (taskID resolution mirrors recordExecutionEvent).
	if c.memoryStore != nil {
		taskID := fmt.Sprintf("GH-%d", issueNumber)
		if issueNumber == 0 {
			taskID = fmt.Sprintf("PR-%d", prNumber)
		}
		if exec, err := c.memoryStore.GetLatestExecutionByTaskID(taskID); err == nil && exec.StartedAt != nil {
			c.metrics.RecordTimeToPR(time.Since(*exec.StartedAt))
			c.metrics.RecordQueueWaitDuration(exec.StartedAt.Sub(exec.CreatedAt))
		}
	}

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

		// GH-3847: record durable-milestone transitions to the execution-events
		// audit trail. Best-effort — see recordExecutionEvent.
		if eventStage, ok := executionEventStageFor(prState.Stage); ok {
			detail := fmt.Sprintf("pr #%d: %s -> %s", prNumber, previousStage, prState.Stage)
			c.recordExecutionEvent(prState, eventStage, detail)
		}
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
		c.metrics.RecordCIRun("pass")
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
		} else if reason := ScopeDriftReason(c.log, prState.PRTitle, issue.Title); reason != "" {
			escalateReason = reason
		}
	}

	if escalateReason != "" {
		c.log.Warn("merge gate escalated: requiring human approval",
			"pr", prState.PRNumber, "reason", escalateReason)
		prState.Stage = StageAwaitApproval
		// GH-3569: record WHY this PR awaits approval so downstream reporting
		// (misconfig error, PR comment) names the actual trigger instead of
		// blaming env require_approval.
		prState.EscalationReason = escalateReason
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
		prState.EscalationReason = fmt.Sprintf("environments.%s.require_approval=true", c.config.EnvironmentName())

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

			// GH-3806: name the reason and terminal outcome so notifyExternalClose
			// (which fires on the next poll once it sees this PR closed) can post a
			// PR/issue comment and correct the issue's labels instead of silently
			// leaving a stale pilot-in-progress/pilot-done on discarded work.
			reason := fmt.Sprintf("CI fix iteration limit reached (%d/%d): stopping cascade to prevent infinite loop", iteration, c.config.MaxCIFixIterations)
			if len(failedChecks) > 0 {
				reason = fmt.Sprintf("%s (failing checks: %s)", reason, strings.Join(failedChecks, ", "))
			}
			prState.Stage = StageFailed
			prState.Error = reason
			prState.TerminalLabel = github.LabelFailed
			c.metrics.RecordPRFailed()
			c.metrics.RecordCIRun("fail")
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
				// GH-3806: see the matching comment on the iteration-limit branch above.
				prState.Stage = StageFailed
				prState.Error = fmt.Sprintf("CI fix size guard: PR has %d additions, over limit %d (likely cascade contamination — escalate to human)", netAdditions, c.config.MaxCIFixPRSize)
				prState.TerminalLabel = github.LabelFailed
				c.metrics.RecordPRFailed()
				c.metrics.RecordCIRun("fail")
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
		projectPath := c.repoKey()
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

	// GH-3806: name the reason (and the follow-up issue that now owns this work)
	// so notifyExternalClose can post the audit-trail comments and mark this
	// issue pilot-failed instead of leaving it stranded on a stale label — the
	// fix issue created above carries the retry forward, so this issue must not
	// also be re-queued (that would double-dispatch the same failure).
	reason := "CI checks failed"
	if len(failedChecks) > 0 {
		reason = fmt.Sprintf("CI checks failed (%s)", strings.Join(failedChecks, ", "))
	}
	prState.Stage = StageFailed
	prState.Error = fmt.Sprintf("%s; fix issue #%d created to continue this work", reason, issueNum)
	prState.TerminalLabel = github.LabelFailed
	c.metrics.RecordPRFailed()
	c.metrics.RecordCIRun("fail")
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

			// GH-3806: see the matching comment on handleCIFailed's iteration-limit branch.
			prState.Stage = StageFailed
			prState.Error = fmt.Sprintf("review feedback iteration limit reached (%d/%d)", iteration, c.config.ReviewFeedback.MaxIterations)
			prState.TerminalLabel = github.LabelFailed
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
			projectPath := c.repoKey()
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

	// GH-3806: the revision issue created above now owns the retry, so this
	// issue must be marked pilot-failed rather than re-queued (see the matching
	// comment on handleCIFailed's main CI-fail branch).
	prState.Stage = StageFailed
	prState.Error = fmt.Sprintf("changes requested by reviewer; revision issue #%d created to continue this work", issueNum)
	prState.TerminalLabel = github.LabelFailed
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
	// Fail closed: if approval stage is not enabled, do NOT auto-approve when approval is required.
	if c.approvalMgr == nil || !c.approvalMgr.IsStageEnabled(approval.StagePreMerge) {
		// GH-3569: PRs reach StageAwaitApproval via three paths (size-floor gate,
		// scope-drift gate, env require_approval). The old hardcoded message
		// blamed require_approval=true even when the env had it false and a
		// defense-in-depth gate did the escalating — observed on PR #3559.
		reason := prState.EscalationReason
		if reason == "" {
			reason = fmt.Sprintf("environments.%s.require_approval=true", c.config.EnvironmentName())
		}
		c.log.Error("approval misconfig: approval required but pre_merge.enabled=false",
			"pr", prState.PRNumber, "env", c.config.EnvironmentName(), "escalation_reason", reason)
		prState.Stage = StageFailed
		prState.Error = fmt.Sprintf(
			"approval-misconfig: PR requires approval (%s) but approval.pre_merge.enabled=false → deadlock until config fixed",
			reason,
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
		if serr := c.stateStore.SavePRState(c.repoKey(), prState); serr != nil {
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
	// GH-4130: observe pilot_approval_wait_seconds from when the async approval
	// request was submitted (functionally the awaiting_approval entry point) to
	// this merge decision being applied — covers both the SetApprovalDecision
	// webhook path and the wall-clock-expiry default-action path (Path 3 above).
	if !prState.ApprovalRequestedAt.IsZero() {
		c.metrics.RecordApprovalWaitDuration(time.Since(prState.ApprovalRequestedAt))
	}

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
			_ = c.stateStore.SavePRState(c.repoKey(), pr)
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
// shouldDeferIssueClose reports whether the merged PR's issue is a decomposed
// parent that still has open children. GH-3513/GH-3530 incidents: a child's PR
// mis-registered under the parent's issue number made handleMerging close the
// parent + pilot-done while siblings were open and unshipped. Decomposed
// parents must only be closed by the count-verified path (maybeCloseParentIssue
// / recoverStaleParentIssues). Fail-open on count errors so leaf issues keep
// closing on transient API failures.
func (c *Controller) shouldDeferIssueClose(ctx context.Context, issueNum, prNum int) bool {
	open, err := c.openSubIssueCount(ctx, issueNum)
	if err != nil {
		c.log.Warn("shouldDeferIssueClose: sub-issue count failed — proceeding with close",
			"issue", issueNum, "pr", prNum, "error", err)
		return false
	}
	if open > 0 {
		c.log.Info("handleMerging: issue is a decomposed parent with open children — deferring close to count-verified path",
			"issue", issueNum, "open", open, "pr", prNum)
		return true
	}
	return false
}

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
	prState.RebaseAttempts = 0 // GH-3715: reset rebase-oscillation counter on a clean merge
	c.recordMergeSuccess(prState)

	// GH-1015: Add pilot-done label after successful merge (not at PR creation)
	// This prevents false positives where PRs are closed without merging
	if prState.IssueNumber > 0 && !c.shouldDeferIssueClose(ctx, prState.IssueNumber, prState.PRNumber) {
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
		// GH-4021: A pilot-retry-* label from an earlier PR-closed-without-merge
		// cycle must not survive a later successful merge — left in place it
		// arms a redundant auto-retry against already-shipped work.
		c.clearRetryLabels(ctx, prState.IssueNumber)
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
					ProjectPath:  c.projectPath,
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
		if c.releaseConfigured() && !c.resolvedRelease().RequireCI {
			action, scopeKey, scopeTitle := c.releaseActionFor(ctx, prState.IssueNumber)
			if action == releaseActionRelease {
				c.log.Info("skipping post-merge CI: proceeding to release",
					"pr", prState.PRNumber,
				)
				prState.Stage = StageReleasing
				return nil
			}
			c.log.Info("skipping post-merge CI: holding PR for scope release",
				"pr", prState.PRNumber, "scope", scopeKey, "scope_title", scopeTitle,
			)
			c.removePR(prState.PRNumber)
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

	openCount, err := c.openSubIssueCount(ctx, parentNum)
	if err != nil {
		c.log.Warn("maybeCloseParentIssue: failed to count open sub-issues", slog.Int("parent", parentNum), slog.Any("error", err))
		return
	}

	if openCount > 0 {
		c.log.Info("maybeCloseParentIssue: siblings still open", slog.Int("parent", parentNum), slog.Int("open", openCount))
		return
	}

	c.closeParentNow(ctx, parentNum, nil)
}

// openSubIssueCount returns the number of open sub-issues for a parent,
// combining both lookup tiers:
//   - Tier 1: native GitHub sub-issues GraphQL API (works even without text patterns).
//   - Tier 2: text search for body "Parent: GH-N" references.
//
// GH-3513 incident: LinkSubIssue is non-fatal at creation time, so the native
// link set can cover only a SUBSET of children. A native count of 0 then looks
// like "all done" while unlinked siblings are still open — the parent gets
// closed prematurely and the poller later supersedes the live children.
// Therefore a native count of 0 is never trusted alone: it must be confirmed
// by the text search before the caller may close the parent. The max of both
// tiers is returned.
func (c *Controller) openSubIssueCount(ctx context.Context, parentNum int) (int, error) {
	numbers, hasNativeLinks, err := c.ghClient.GetOpenSubIssueNumbers(ctx, c.owner, c.repo, parentNum)
	if err != nil || !hasNativeLinks {
		if err != nil {
			c.log.Warn("openSubIssueCount: native sub-issue count failed, falling back to search", slog.Int("parent", parentNum), slog.Any("error", err))
		} else {
			c.log.Debug("openSubIssueCount: no native sub-issue links, falling back to search", slog.Int("parent", parentNum))
		}
		return c.ghClient.SearchOpenSubIssues(ctx, c.owner, c.repo, parentNum)
	}

	nativeCount := c.blockingChildCount(numbers)
	if nativeCount > 0 {
		return nativeCount, nil
	}

	// Native says 0 blocking — cross-check text search to catch unlinked open siblings.
	textCount, err := c.ghClient.SearchOpenSubIssues(ctx, c.owner, c.repo, parentNum)
	if err != nil {
		return 0, fmt.Errorf("native count is 0 but confirmation search failed: %w", err)
	}
	if textCount > 0 {
		c.log.Info("openSubIssueCount: native links report 0 open but text search found unlinked open siblings, deferring close",
			slog.Int("parent", parentNum), slog.Int("text_open", textCount))
	}
	return textCount, nil
}

// blockingChildCount returns how many of the given open native sub-issue numbers
// still block the parent from closing. GH-3780: an open GitHub sub-issue normally
// blocks, but a decomposed child whose execution ledger classifies it "no_op" (no
// commits, no PR — so it never produced a merge to close its own issue) has
// genuinely finished its work. Any other status (queued, running, failed, or no
// ledger row at all) still blocks, matching the pre-GH-3780 behavior.
func (c *Controller) blockingChildCount(numbers []int) int {
	blocking := 0
	for _, num := range numbers {
		if !c.isChildNoOp(num) {
			blocking++
		}
	}
	return blocking
}

// isChildNoOp reports whether the sub-issue's most recent ledger execution is a
// verified no_op, via the exact task_id+project_path join (GetExecutionStatusByTaskID)
// rather than the fuzzy substring match GetLatestExecutionByTaskID uses — a wrong-repo
// or wrong-issue match could otherwise let an unrelated no_op row wrongly close this
// parent. Fails closed (false) when the eval store isn't wired or the lookup errors.
func (c *Controller) isChildNoOp(issueNum int) bool {
	if c.evalStore == nil {
		return false
	}
	status, err := c.evalStore.GetExecutionStatusByTaskID(fmt.Sprintf("GH-%d", issueNum), c.projectPath)
	if err != nil {
		return false
	}
	return status == "no_op"
}

// closeParentNow adds pilot-done, removes stale labels, posts a summary comment,
// and closes the parent issue. All errors are logged as warnings without propagating.
//
// mergedPRs optionally names the child PRs that shipped this epic's work (GH-3939,
// populated by reconcileEpicParent's merged-PR verification); nil/empty falls back
// to the plain "sub-issues are complete" comment used by the older count-only
// callers (maybeCloseParentIssue, recoverStaleParentIssues).
// closeParentNow returns closed=true only when this call actually transitioned
// the parent to closed — false for an already-closed no-op or a failed
// UpdateIssueState call. GH-3990: reconcileEpicParent gates enqueueScopeRelease
// on this so a scope release is never enqueued for a close that didn't happen.
// parentTitle is best-effort (empty if the pre-close GetIssue failed).
func (c *Controller) closeParentNow(ctx context.Context, parentNum int, mergedPRs []int) (closed bool, parentTitle string) {
	// GH-3939: guard against re-closing an already-closed parent — two close
	// paths (reactive maybeCloseParentIssue and the periodic reconcileEpicParents
	// sweep) can both observe "no open children" for the same parent before either
	// has closed it, which would otherwise double-post the summary comment and
	// re-run label churn. Fail-open on lookup error so a transient API failure
	// never blocks a legitimate close.
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, parentNum)
	if err == nil {
		parentTitle = issue.Title
		if strings.EqualFold(issue.State, "closed") {
			c.log.Debug("closeParentNow: parent already closed, no-op", slog.Int("parent", parentNum))
			return false, parentTitle
		}
	}

	c.log.Info("closeParentNow: all sub-issues done, closing parent", slog.Int("parent", parentNum))

	// Label cleanup: add pilot-done, remove stale labels.
	if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, parentNum, []string{"pilot-done"}); err != nil {
		c.log.Warn("closeParentNow: failed to add pilot-done label", slog.Int("parent", parentNum), slog.Any("error", err))
	}
	// GH-4006: also clear a needs-clarification label left by an earlier
	// escalateEpicCloseVeto pass whose veto later resolved — harmless if it
	// was never applied (RemoveLabel on an absent label is a no-op).
	for _, stale := range []string{"pilot-failed", "pilot-in-progress", "pilot-blocked", github.LabelNeedsClarification} {
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, parentNum, stale); err != nil {
			c.log.Warn("closeParentNow: failed to remove label", slog.String("label", stale), slog.Int("parent", parentNum), slog.Any("error", err))
		}
	}

	// Post summary comment, naming the merged child PRs when known.
	comment := fmt.Sprintf("All sub-issues for GH-%d are complete. Closing parent issue automatically.", parentNum)
	if len(mergedPRs) > 0 {
		comment += fmt.Sprintf("\n\nMerged PRs: %s", formatMergedPRRefs(mergedPRs))
	}
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, parentNum, comment); err != nil {
		c.log.Warn("closeParentNow: failed to post comment", slog.Int("parent", parentNum), slog.Any("error", err))
	}

	// Close the parent issue.
	if err := c.ghClient.UpdateIssueState(ctx, c.owner, c.repo, parentNum, "closed"); err != nil {
		c.log.Warn("closeParentNow: failed to close parent issue", slog.Int("parent", parentNum), slog.Any("error", err))
		return false, parentTitle
	}
	return true, parentTitle
}

// formatMergedPRRefs renders merged PR numbers as a comma-separated "#N" list
// for the parent-close summary comment (GH-3939).
func formatMergedPRRefs(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}

// recoverStaleParentIssues scans open pilot parent issues at startup and closes any
// whose sub-issues are all done. Catches parents orphaned when the daemon was down.
//
// GH-4099: candidates come from epicParentCandidates, not a bare native-link search
// — a parent whose "pilot" label was stripped out-of-band, or whose children were
// only ever linked via the "Parent: GH-N" body-marker convention (LinkSubIssue is
// non-fatal at creation time, GH-3513), used to be silently invisible here forever.
func (c *Controller) recoverStaleParentIssues(ctx context.Context) {
	candidates := c.epicParentCandidates(ctx)

	closed := 0
	for _, parentNum := range candidates {
		openCount, err := c.openSubIssueCount(ctx, parentNum)
		if err != nil {
			c.log.Warn("recoverStaleParentIssues: failed to count sub-issues", slog.Int("parent", parentNum), slog.Any("error", err))
			continue
		}
		if openCount > 0 {
			c.log.Debug("recoverStaleParentIssues: siblings still open, skipping", slog.Int("parent", parentNum), slog.Int("open", openCount))
			continue
		}
		c.closeParentNow(ctx, parentNum, nil)
		closed++
	}

	c.log.Info("recoverStaleParentIssues: done", slog.Int("closed", closed), slog.Int("candidates", len(candidates)))
}

// Start runs one-time startup recovery sweeps. Call before the main Run loop.
func (c *Controller) Start(ctx context.Context) {
	c.recoverStaleParentIssues(ctx)
	// GH-3990: claim any scope releases left pending (or re-drive any left
	// 'releasing' with no live carrier) by a daemon restart.
	c.startPendingScopeReleases(ctx)
	// GH-3993: start the on_schedule release-train cron (no-op unless
	// resolvedRelease().ScheduleReleaseEnabled()).
	c.startScheduleRelease(ctx)
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
		if prState.ScopeKey != "" {
			// GH-3990: re-queue the scope for a fresh carrier attempt instead of
			// leaving this one wedged at StageFailed forever — drain it now so the
			// anchor PR slot frees for the retry.
			c.handleScopeReleaseFailure(ctx, prState, fmt.Sprintf("post-merge CI timeout after %v", ciTimeout))
			c.removePR(prState.PRNumber)
			return nil
		}
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
		if prState.ScopeKey != "" {
			// GH-3990: this is the scope carrier itself — the hold decision
			// already happened when the scope was enqueued; proceed straight to
			// releasing rather than re-consulting releaseActionFor/heldByScope.
			prState.Stage = StageReleasing
			return nil
		}
		if c.releaseConfigured() {
			action, scopeKey, scopeTitle := c.releaseActionFor(ctx, prState.IssueNumber)
			if action == releaseActionRelease {
				prState.Stage = StageReleasing
				return nil
			}
			c.log.Info("holding merged PR for scope release",
				"pr", prState.PRNumber, "scope", scopeKey, "scope_title", scopeTitle,
			)
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
			projectPath := c.repoKey()
			if learnErr := c.learningLoop.LearnFromCIFailure(ctx, projectPath, ciLogs, failedChecks); learnErr != nil {
				c.log.Warn("Failed to learn from post-merge CI failure", slog.Any("error", learnErr))
			}
		}
		// GH-3990: the fix-issue flow above is unchanged — additionally re-queue
		// the scope release for a fresh carrier attempt.
		if prState.ScopeKey != "" {
			c.handleScopeReleaseFailure(ctx, prState, fmt.Sprintf("post-merge CI failed at %s", ShortSHA(mainSHA)))
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

// GlobalReleaseEnabled reports whether the resolved env/global release
// config is enabled, ignoring any per-project overlay. main.go's
// projects-loop wiring (GH-4001) uses this to decide whether a project with
// no `release:` block needs a migration WARN — that project used to inherit
// this cascade and, as of GH-4001, no longer does.
func GlobalReleaseEnabled(cfg *Config) bool {
	rel := resolveRelease(cfg)
	return rel != nil && rel.Enabled
}

// resolvedRelease returns the effective release config: per-environment config
// wins over global, then the per-project overlay (if any) is applied on top.
// Computed once in NewController and cached — see resolvedReleaseCfg. Returns
// nil if releasing is not configured at any level.
func (c *Controller) resolvedRelease() *ReleaseConfig {
	return c.resolvedReleaseCfg
}

// shouldTriggerRelease returns true if auto-release is configured for the
// per-merge cadence specifically (Trigger "on_merge"). Use releaseConfigured
// for gates that must also cover the on_scope_close/on_schedule cadences
// (which release too, just not on every merge — see releaseActionFor).
func (c *Controller) shouldTriggerRelease() bool {
	rel := c.resolvedRelease()
	return rel != nil && rel.Enabled && rel.Trigger == "on_merge"
}

// releaseConfigured returns true if auto-release is enabled at ANY trigger
// cadence (on_merge, on_scope_close, on_schedule). This gates the four
// release-decision sites (handleMerged fast path, handlePostMergeCI,
// checkExternalMergeOrClose, ScanRecentlyMergedPRs) so on_scope_close/
// on_schedule merges are still routed through releaseActionFor — and
// possibly held — instead of being silently drained like Trigger "manual"
// (GH-3989).
func (c *Controller) releaseConfigured() bool {
	rel := c.resolvedRelease()
	return rel != nil && rel.Enabled
}

// releaseAction is the outcome of releaseActionFor: whether a merged PR
// should proceed to StageReleasing now or be held for a later scope/schedule
// release.
type releaseAction int

const (
	// releaseActionRelease proceeds to StageReleasing immediately.
	releaseActionRelease releaseAction = iota
	// releaseActionHold drains the PR without releasing; the merge is fully
	// reconstructable from GitHub once the scope/schedule fires (GH-3989 Issue B/F).
	releaseActionHold
)

// releaseActionFor decides, for a merged PR linked to issueNumber (0 if
// none/standalone), whether to release now or hold — based on the effective
// release Trigger. Callers must have already confirmed releaseConfigured().
//
//   - Trigger "on_schedule" holds every merge unconditionally (no scheduler
//     ships in this issue — the hold is inert-but-safe until Issue F lands).
//   - Trigger "on_scope_close" holds only merges whose issue is a scope
//     member per heldByScope; standalone merges release per-merge as today.
//   - Any other trigger ("on_merge") releases immediately.
func (c *Controller) releaseActionFor(ctx context.Context, issueNumber int) (action releaseAction, scopeKey, scopeTitle string) {
	rel := c.resolvedRelease()
	switch {
	case rel.ScheduleReleaseEnabled():
		return releaseActionHold, "schedule", ""
	case rel.ScopeReleaseEnabled():
		if key, title, held := c.heldByScope(ctx, issueNumber); held {
			return releaseActionHold, key, title
		}
		return releaseActionRelease, "", ""
	default:
		return releaseActionRelease, "", ""
	}
}

// tagCoveringCommit returns the name of an existing release tag that already
// covers sha, or "" if none does. A tag "covers" sha when sha is the tag's
// commit (exact match) OR sha is an ancestor of the tag's commit — i.e. the
// commit was already shipped inside a later release. This is a superset of
// GetTagForSHA's exact-match dedup and guards handleReleasing against cutting a
// redundant, lower-content release for already-released work.
//
// The ancestor probe uses the compare API (base=sha, head=tag): "ahead" means
// the tag contains sha plus more commits, "identical" means same commit —
// either way sha is covered. Any lookup error is propagated so the caller
// retries on the next poll rather than tagging on uncertain state (TASK-316).
func (c *Controller) tagCoveringCommit(ctx context.Context, owner, repo, sha string) (string, error) {
	// 10 most recent tags: a redundant release only happens when sha is an
	// ancestor of a recent tag (the current release line), so a bounded list
	// keeps the compare-call fan-out small.
	tags, err := c.ghClient.ListTags(ctx, owner, repo, 10)
	if err != nil {
		return "", err
	}
	for _, tag := range tags {
		if tag.Commit.SHA == sha {
			return tag.Name, nil
		}
		status, err := c.ghClient.CompareStatus(ctx, owner, repo, sha, tag.Commit.SHA)
		if err != nil {
			return "", err
		}
		if status == "ahead" || status == "identical" {
			return tag.Name, nil
		}
	}
	return "", nil
}

// handleReleasing creates a release after successful merge and CI.
func (c *Controller) handleReleasing(ctx context.Context, prState *PRState) error {
	if c.releaser == nil {
		c.log.Warn("releaser not configured, skipping release", "pr", prState.PRNumber)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Track attempt count for retry cap. Drain paths (tag already exists) never
	// reach the cap — only persistent error loops do.
	prState.ReleasingAttempts++
	if prState.ReleasingFirstAt.IsZero() {
		prState.ReleasingFirstAt = time.Now()
	}

	rel := c.resolvedRelease()

	// GH-3990: a scope-release carrier's HeadSHA is the post-merge-CI-validated
	// main SHA captured on first entry to StagePostMergeCI, not the anchor
	// member PR's own (unrelated) head commit. Set it explicitly before any of
	// the logic below reads prState.HeadSHA.
	isScope := prState.ScopeKey != ""
	if isScope {
		prState.HeadSHA = prState.PostMergeSHA
		if len(prState.ScopeMemberPRs) == 0 {
			// Restart path: autopilot_pr_state persists ScopeKey but not the
			// in-memory-only ScopeMemberPRs/ScopeTitle fields.
			c.hydrateScopeMembers(prState)
		}
	}

	// Resolve the actual repo owner/name from the PR URL.
	// Cross-repo PRs (e.g. auth-service) have a PRURL pointing to a different repo
	// than c.owner/c.repo (the pilot repo). All release API calls must target the
	// correct repo to avoid stuck-forever releasing state.
	owner, repo := prState.RepoOwnerAndName(c.owner, c.repo)

	if !isScope {
		// Race condition guard: Check if this commit is already covered by a tag.
		// When multiple PRs merge rapidly, each triggers handleReleasing but only
		// the first should create a tag. Subsequent PRs see their commit is already
		// covered (by an earlier release) and skip. "Covered" means either the
		// commit is tagged exactly, OR it is an ANCESTOR of an existing release
		// tag's commit — e.g. it was already shipped inside a later squash-merge or
		// a manual tag on a descendant commit. Without the ancestor check we cut a
		// redundant, lower-content release for already-shipped work (the spurious
		// v2.178.0 incident: #3494's commit was an ancestor of v2.177.0).
		//
		// GH-3990: skipped for a scope carrier — an interleaved standalone release
		// landing on the same commit range would otherwise falsely drain the scope
		// before its own tag is cut. The autopilot_scope_release row is the dedup
		// for scope releases (ClaimScopeRelease's single-winner claim).
		existingTag, err := c.tagCoveringCommit(ctx, owner, repo, prState.HeadSHA)
		if err != nil {
			// Transient lookup failure: do NOT fall through to CreateTagForRepo. If a
			// tag already exists but we couldn't see it, the create call fails with
			// "Reference already exists", returns an error, and the PR stays in
			// StageReleasing forever (re-tried every poll). Return the error so this
			// PR is retried cleanly on the next poll once the lookup recovers. (TASK-316)
			return c.checkReleasingRetryOrEscalate(ctx, prState,
				fmt.Errorf("failed to check existing tags for PR #%d: %w", prState.PRNumber, err))
		}
		if existingTag != "" {
			// GH-3926: publish mode "api" idempotence — if a prior pass created
			// this tag (or the tag it's an ancestor of) but a transient failure
			// meant the GitHub Release was never published, publish it now instead
			// of silently draining the PR with no release ever created.
			c.ensureReleasePublished(ctx, rel, owner, repo, existingTag, prState)
			c.log.Info("commit already covered by existing tag, skipping release",
				"pr", prState.PRNumber,
				"sha", ShortSHA(prState.HeadSHA),
				"tag", existingTag,
			)
			c.removePR(prState.PRNumber)
			return nil
		}

		// Published-release guard: tagCoveringCommit uses a bounded window of 10 tags.
		// This exhaustive lookup (paginates all tags) catches the case where the SHA
		// was tagged more than 10 releases ago and treats it as already released.
		exactTag, err := c.ghClient.GetTagForSHA(ctx, owner, repo, prState.HeadSHA)
		if err != nil {
			return c.checkReleasingRetryOrEscalate(ctx, prState,
				fmt.Errorf("failed to check published release for PR #%d: %w", prState.PRNumber, err))
		}
		if exactTag != "" {
			// GH-3926: same idempotence as the existingTag drain above.
			c.ensureReleasePublished(ctx, rel, owner, repo, exactTag, prState)
			c.log.Info("commit already tagged (exact match in full tag history) — treating as released",
				"pr", prState.PRNumber,
				"sha", ShortSHA(prState.HeadSHA),
				"tag", exactTag,
			)
			c.removePR(prState.PRNumber)
			return nil
		}
	}

	// Reachability guard: refuse to tag a commit that is not reachable from the
	// default branch. A diverged SHA (e.g. from a force-push or a PR merged to a
	// non-main branch) can never be released from main; failing immediately avoids
	// unbounded retries on a permanently unreleasable commit. Kept for scope
	// carriers too.
	if reachErr := c.guardReleaseSHAReachable(ctx, owner, repo, prState); reachErr != nil {
		c.escalateReleasingFailed(ctx, prState, reachErr.Error())
		return nil
	}

	// Get current version from the target repo
	currentVersion, err := c.releaser.GetCurrentVersionForRepo(ctx, owner, repo)
	if err != nil {
		c.log.Warn("failed to get current version, defaulting to 0.0.0", "error", err)
		currentVersion = SemVer{}
	}

	// Get commits for bump detection: a "train:" scope carrier is defined as
	// "everything since the last tag" (CompareCommits, not a member-PR union
	// — GH-3993); any other scope carrier (epic:/label:) unions every member
	// PR's own commits; a regular release reads just its own PR's commits.
	var commits []*github.Commit
	switch {
	case isScope && strings.HasPrefix(prState.ScopeKey, "train:"):
		commits, err = c.trainReleaseCommits(ctx, owner, repo, prState, currentVersion, rel)
		if err != nil {
			return c.checkReleasingRetryOrEscalate(ctx, prState,
				fmt.Errorf("failed to get train release commits for %s: %w", prState.ScopeKey, err))
		}
	case isScope:
		commits, err = c.scopeReleaseCommits(ctx, owner, repo, prState, currentVersion, rel)
		if err != nil {
			return c.checkReleasingRetryOrEscalate(ctx, prState,
				fmt.Errorf("failed to get scope release commits for %s: %w", prState.ScopeKey, err))
		}
	default:
		commits, err = c.ghClient.GetPRCommits(ctx, owner, repo, prState.PRNumber)
		if err != nil {
			return c.checkReleasingRetryOrEscalate(ctx, prState,
				fmt.Errorf("failed to get PR commits: %w", err))
		}
	}

	// Detect bump type from commits
	bumpType := DetectBumpType(commits)
	prState.ReleaseBumpType = bumpType

	if !c.releaser.ShouldRelease(bumpType) {
		c.log.Info("no release needed", "pr", prState.PRNumber, "bump", bumpType)
		if isScope {
			// GH-3990: a no-op scope (no releasable commits across any member)
			// must never retry — record it done with an empty tag.
			c.markScopeReleaseDone(prState, "")
		}
		c.removePR(prState.PRNumber)
		return nil
	}

	// Calculate new version
	newVersion := currentVersion.Bump(bumpType)
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
			// GH-3926: the tag we attempted to create is prState.ReleaseVersion —
			// same idempotence as the existingTag/exactTag drains above, so a
			// prior CreateReleaseForRepo failure on this exact tag still gets
			// the release published before draining.
			c.ensureReleasePublished(ctx, rel, owner, repo, prState.ReleaseVersion, prState)
			c.log.Info("tag already exists at HEAD SHA — treating as released",
				"pr", prState.PRNumber,
				"sha", ShortSHA(prState.HeadSHA),
			)
			if isScope {
				c.markScopeReleaseDone(prState, prState.ReleaseVersion)
			}
			c.removePR(prState.PRNumber)
			return nil
		}
		return c.checkReleasingRetryOrEscalate(ctx, prState,
			fmt.Errorf("failed to create tag: %w", err))
	}

	releaseURL := fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", owner, repo, tagName)

	// GH-3992: a scope carrier's deterministic notes (headline, grouped
	// Features/Fixes/Other with exact per-member "(#PR, GH-Issue)"
	// attribution, breaking changes, compare footer) are computed once here
	// and reused both as the "api"-mode release body below and as the
	// "workflow"-mode enrichment addition in afterTagCreated — one GitHub
	// round trip per member PR regardless of publish mode.
	var scopeNotesBody string
	if isScope {
		scopeNotesBody = BuildScopeReleaseNotes(ScopeNotesInput{
			Owner:      owner,
			Repo:       repo,
			ScopeKey:   prState.ScopeKey,
			ScopeTitle: prState.ScopeTitle,
			Members:    c.buildScopeMembers(ctx, owner, repo, prState.ScopeMemberPRs),
			LastTag:    currentVersion.String(rel.TagPrefix),
			NewTag:     tagName,
		})
	}

	// GH-3926: branch on the resolved publish mode. "workflow" (default)
	// preserves the original behavior byte-for-byte — Pilot only tags, the
	// repo's own tag-triggered CI (e.g. GoReleaser) publishes the release.
	switch rel.PublishMode() {
	case ReleasePublishAPI:
		body := ""
		switch {
		case isScope:
			// GH-3992: the scope notes ARE the body — no separate
			// GenerateChangelog call, since it only ever attributes every
			// entry to the single anchor PR (prState.PRNumber), not each
			// member. The async enrichReleaseNotes below still prepends the
			// LLM "What's New" on top when generate_summary is on.
			body = scopeNotesBody
		case rel.GenerateChangelog:
			body = GenerateChangelog(commits, prState.PRNumber)
		}
		release, relErr := c.releaser.CreateReleaseForRepo(ctx, owner, repo, tagName, body)
		switch {
		case relErr == nil:
			releaseURL = release.HTMLURL
			c.log.Info("tag created — published GitHub Release via API",
				"pr", prState.PRNumber,
				"version", prState.ReleaseVersion,
				"tag", tagName,
				"release_url", releaseURL,
			)
		case isDuplicateReleaseError(relErr):
			c.log.Info("tag created — GitHub Release already exists for tag, treating as published",
				"pr", prState.PRNumber,
				"tag", tagName,
			)
		default:
			// The tag already landed — only the release publish failed. Retry
			// (or escalate at the cap) WITHOUT re-creating the tag: the next
			// pass hits the existingTag/exactTag drain above, which retries
			// CreateReleaseForRepo via ensureReleasePublished.
			return c.checkReleasingRetryOrEscalate(ctx, prState,
				fmt.Errorf("failed to publish GitHub Release for tag %s: %w", tagName, relErr))
		}
	case ReleasePublishTagOnly:
		c.log.Info("tag created (tag_only mode — no GitHub Release will be published)",
			"pr", prState.PRNumber,
			"version", prState.ReleaseVersion,
			"tag", tagName,
		)
	default:
		c.log.Info("tag created — waiting for release workflow to publish GitHub Release",
			"pr", prState.PRNumber,
			"version", prState.ReleaseVersion,
			"tag", tagName,
		)
	}

	// Enrichment + post-tag release verification (GH-3927), unified into a
	// single goroutine so "workflow" mode polls for the release exactly once
	// (afterTagCreated does not launch anything for "tag_only"). scopeNotesBody
	// is "" for a non-scope release — afterTagCreated's "api" branch ignores
	// it (already folded into the body above); its "workflow" branch uses it
	// to compose the scope-aware enrichment (GH-3992).
	c.afterTagCreated(owner, repo, tagName, prState.PRNumber, prState.IssueNumber, commits, rel, scopeNotesBody)

	// Send notification
	if rel.NotifyOnRelease && c.notifier != nil {
		if n, ok := c.notifier.(ReleaseNotifier); ok {
			if err := n.NotifyReleased(ctx, prState, releaseURL); err != nil {
				c.log.Warn("failed to send release notification", "error", err)
			}
		}
	}

	// GH-3847: unlike ci_passed/ci_failed/awaiting_approval/merged/failed, a
	// successful release never changes prState.Stage (it stays StageReleasing
	// until removePR below), so it can't be caught by ProcessPR's stage-diff
	// hook — record it explicitly here instead.
	c.recordExecutionEvent(prState, memory.StageReleased,
		fmt.Sprintf("pr #%d: released %s (tag %s)", prState.PRNumber, prState.ReleaseVersion, tagName))

	if isScope {
		c.markScopeReleaseDone(prState, tagName)
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

// ensureReleasePublished makes handleReleasing's "commit already tagged"
// drain paths idempotent under publish mode "api": if a prior pass tagged this
// commit but then failed to publish the GitHub Release (transient API error,
// process restart, ...), the retry poll finds the tag via
// tagCoveringCommit/GetTagForSHA/isDuplicateTagError and would otherwise drain
// the PR having never published a release. No-op for "workflow"/"tag_only" —
// neither mode expects Pilot to have created a release. Best-effort: logs and
// returns on failure rather than blocking the drain, since the tag is the
// source of truth for "released" and a human can always publish manually.
// GH-3926.
func (c *Controller) ensureReleasePublished(ctx context.Context, rel *ReleaseConfig, owner, repo, tagName string, prState *PRState) {
	if rel == nil || rel.PublishMode() != ReleasePublishAPI {
		return
	}
	existing, err := c.ghClient.GetReleaseByTag(ctx, owner, repo, tagName)
	if err != nil {
		c.log.Warn("ensureReleasePublished: failed to check for existing release, skipping",
			"tag", tagName, "error", err)
		return
	}
	if existing != nil {
		return
	}

	body := ""
	if rel.GenerateChangelog {
		if commits, cErr := c.ghClient.GetPRCommits(ctx, owner, repo, prState.PRNumber); cErr == nil {
			body = GenerateChangelog(commits, prState.PRNumber)
		}
	}
	release, err := c.releaser.CreateReleaseForRepo(ctx, owner, repo, tagName, body)
	if err != nil {
		if isDuplicateReleaseError(err) {
			c.log.Info("ensureReleasePublished: release already exists for tag (idempotent retry)", "tag", tagName)
			return
		}
		c.log.Warn("ensureReleasePublished: failed to publish release for already-tagged commit",
			"tag", tagName, "error", err)
		return
	}
	c.log.Info("ensureReleasePublished: published GitHub Release for already-tagged commit",
		"tag", tagName, "release_url", release.HTMLURL)
}

// checkReleasingRetryOrEscalate returns nil (transitioning prState to StageFailed) when
// ReleasingAttempts has reached MaxReleasingAttempts; otherwise returns err so the caller
// retries on the next poll.
func (c *Controller) checkReleasingRetryOrEscalate(ctx context.Context, prState *PRState, err error) error {
	if prState.ReleasingAttempts >= c.config.MaxReleasingAttempts {
		msg := fmt.Sprintf("release failed after %d/%d attempts: %v — manual intervention required",
			prState.ReleasingAttempts, c.config.MaxReleasingAttempts, err)
		c.escalateReleasingFailed(ctx, prState, msg)
		return nil
	}
	return err
}

// escalateReleasingFailed transitions a PR to StageFailed, posts a GitHub comment on the
// linked issue, and records metrics. Used for both the retry cap and the reachability guard.
func (c *Controller) escalateReleasingFailed(ctx context.Context, prState *PRState, reason string) {
	c.log.Error("handleReleasing: escalating to StageFailed",
		"pr", prState.PRNumber,
		"sha", ShortSHA(prState.HeadSHA),
		"attempts", prState.ReleasingAttempts,
		"reason", reason,
	)
	if prState.IssueNumber > 0 {
		comment := fmt.Sprintf(
			"⚠️ **Release escalation**: PR #%d failed to release.\n\nReason: `%v`\n\nManual intervention is required — no further automatic retries will be made.",
			prState.PRNumber, reason)
		if _, cerr := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, comment); cerr != nil {
			c.log.Warn("failed to post release escalation comment", "issue", prState.IssueNumber, "error", cerr)
		}
	}
	prState.Stage = StageFailed
	prState.Error = reason
	c.metrics.RecordPRFailed()
	c.metrics.RecordIssueProcessed("failed")

	// GH-3990: a scope-release carrier must not stay wedged at StageFailed —
	// flip the scope back to pending (or terminal-failed past the retry cap)
	// and drain the carrier so the anchor PR slot frees for the next attempt.
	if prState.ScopeKey != "" {
		c.handleScopeReleaseFailure(ctx, prState, reason)
		c.removePR(prState.PRNumber)
	}
}

// afterTagCreated runs once a release tag has been created, unifying release-note
// enrichment with post-tag release verification (GH-3927) into a single per-mode
// goroutine so "workflow" mode polls for the release exactly once:
//   - "tag_only": no release will ever appear — nothing is launched.
//   - "api": the GitHub Release already exists (Pilot just created it above) —
//     enrichment-only, no polling needed.
//   - "workflow": polls waitForReleaseByTag for up to rel.VerifyTimeout. On
//     success, enriches like "api". On timeout, fires a release_missing alert
//     (unless VerifyRelease was explicitly disabled) via fireReleaseMissingAlert.
//
// Uses context.Background()+timeout inside the goroutine (not the ctx
// handleReleasing was called with) so a poll-tick cancellation cannot kill
// verification or enrichment mid-flight — mirrors the pre-existing enrichment
// goroutine this replaces.
func (c *Controller) afterTagCreated(owner, repo, tagName string, prNumber, issueNumber int, commits []*github.Commit, rel *ReleaseConfig, scopeNotes string) {
	if rel.PublishMode() == ReleasePublishTagOnly {
		return
	}

	if rel.PublishMode() == ReleasePublishAPI {
		// GH-3992: for a scope carrier, scopeNotes is already the release
		// body handleReleasing created above — enrichReleaseNotes's plain
		// prepend-only EnrichRelease is unchanged and correct here too.
		logging.SafeGo("autopilot-controller", func() {
			c.enrichReleaseNotes(owner, repo, tagName, commits, rel)
		})
		return
	}

	// "workflow": verify the release appears within VerifyTimeout before enriching.
	timeout := rel.VerifyTimeout
	if timeout <= 0 {
		timeout = releasePollTimeout
	}
	logging.SafeGo("autopilot-controller", func() {
		c.verifyReleaseAfterTag(context.Background(), owner, repo, tagName, prNumber, issueNumber, commits, rel, releasePollInterval, timeout, scopeNotes)
	})
}

// enrichReleaseNotes generates and attaches an LLM release summary. Best-effort:
// logs and returns on failure rather than propagating an error, since a missing
// summary should never affect release state.
func (c *Controller) enrichReleaseNotes(owner, repo, tagName string, commits []*github.Commit, rel *ReleaseConfig) {
	if c.releaseSummary == nil || !rel.GenerateSummary {
		return
	}
	enrichCtx, cancel := context.WithTimeout(context.Background(), releasePollTimeout+releaseSummaryTimeout)
	defer cancel()
	if err := c.releaseSummary.EnrichRelease(enrichCtx, owner, repo, tagName, commits); err != nil {
		c.log.Warn("failed to enrich release notes", "tag", tagName, "error", err)
	}
}

// enrichScopeReleaseNotes is enrichReleaseNotes' scope-carrier counterpart for
// "workflow" publish mode: GoReleaser owns the release GitHub just published,
// so the final body must compose LLM "## What's New" + the deterministic
// scopeNotes + GoReleaser's original body, instead of enrichReleaseNotes'
// prepend-only composition. Unlike enrichReleaseNotes, this does NOT
// early-return when GenerateSummary is off or c.releaseSummary is nil — the
// deterministic scope notes must ship regardless of whether the LLM step ran
// (GH-3992 edge cases: `generate_summary: false` and no ANTHROPIC_API_KEY
// both still get scope notes, just without the "What's New" section).
// Best-effort like enrichReleaseNotes: logs and returns on failure.
func (c *Controller) enrichScopeReleaseNotes(owner, repo, tagName string, commits []*github.Commit, rel *ReleaseConfig, scopeNotes string) {
	enrichCtx, cancel := context.WithTimeout(context.Background(), releasePollTimeout+releaseSummaryTimeout)
	defer cancel()

	release, err := waitForReleaseByTag(enrichCtx, c.ghClient, c.log, owner, repo, tagName, releasePollInterval, releasePollTimeout)
	if err != nil {
		c.log.Warn("failed to enrich scope release notes: release never appeared", "tag", tagName, "error", err)
		return
	}

	var summary string
	if rel.GenerateSummary && c.releaseSummary != nil {
		if s, sErr := c.releaseSummary.generateSummary(enrichCtx, tagName, commits); sErr != nil {
			c.log.Warn("scope release: LLM summary generation failed, shipping without What's New", "tag", tagName, "error", sErr)
		} else {
			summary = s
		}
	}

	body := scopeNotes
	if summary != "" {
		body = summary + "\n\n" + scopeNotes
	}
	body += "\n\n" + release.Body

	if _, err := c.ghClient.UpdateRelease(enrichCtx, owner, repo, release.ID, &github.ReleaseInput{Body: body}); err != nil {
		c.log.Warn("failed to update scope release body", "tag", tagName, "error", err)
		return
	}
	c.log.Info("scope release enriched with notes", "tag", tagName)
}

// verifyReleaseAfterTag is the synchronous body of afterTagCreated's "workflow"
// verification goroutine, factored out (interval/timeout as params) so tests can
// call it directly with short values instead of racing a background goroutine
// against the real releasePollInterval/VerifyTimeout. On success it enriches like
// "api" mode; on timeout it fires a release_missing alert unless VerifyRelease
// was explicitly disabled. GH-3927.
func (c *Controller) verifyReleaseAfterTag(ctx context.Context, owner, repo, tagName string, prNumber, issueNumber int, commits []*github.Commit, rel *ReleaseConfig, interval, timeout time.Duration, scopeNotes string) {
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := waitForReleaseByTag(verifyCtx, c.ghClient, c.log, owner, repo, tagName, interval, timeout); err != nil {
		if rel.VerifyReleaseEnabled() {
			c.fireReleaseMissingAlert(owner, repo, tagName, prNumber, issueNumber, timeout)
		}
		return
	}
	if scopeNotes != "" {
		c.enrichScopeReleaseNotes(owner, repo, tagName, commits, rel, scopeNotes)
		return
	}
	c.enrichReleaseNotes(owner, repo, tagName, commits, rel)
}

// fireReleaseMissingAlert fires a release_missing alert (GH-3927) for a tag whose
// GitHub Release never appeared within the verification window, and comments on
// the source issue (in c.owner/c.repo, mirroring escalateReleasingFailed) when
// known. Deduplicated per "owner/repo@tag" via alertedMissingReleases: the alerts
// engine's cooldown is keyed by rule name, not by source, so without
// controller-side dedup a second break inside the same cooldown window would be
// silently swallowed. Shared by afterTagCreated and the ScanRecentlyMergedPRs
// backstop, since a hot-upgrade restart can kill the former mid-flight.
func (c *Controller) fireReleaseMissingAlert(owner, repo, tag string, prNumber, issueNumber int, timeout time.Duration) {
	key := owner + "/" + repo + "@" + tag
	c.mu.Lock()
	if c.alertedMissingReleases == nil {
		c.alertedMissingReleases = make(map[string]bool)
	}
	if c.alertedMissingReleases[key] {
		c.mu.Unlock()
		return
	}
	c.alertedMissingReleases[key] = true
	c.mu.Unlock()

	msg := fmt.Sprintf(
		"tag %s exists in %s/%s but no GitHub Release was published within %s — check the repo's release workflow (or the release is a draft)",
		tag, owner, repo, timeout,
	)
	c.log.Warn("release verification: no GitHub Release published within window",
		"owner", owner, "repo", repo, "tag", tag, "pr", prNumber, "timeout", timeout,
	)

	if c.alertsEngine == nil {
		c.log.Error("release_missing alert not delivered: SetAlertsEngine was never called", "tag", tag)
	} else {
		c.alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventType("release_missing"),
			Error:     msg,
			Timestamp: time.Now(),
			Metadata: map[string]string{
				"repo": owner + "/" + repo,
				"tag":  tag,
				"pr":   strconv.Itoa(prNumber),
			},
		})
	}

	if issueNumber > 0 {
		comment := fmt.Sprintf("⚠️ **Release verification failed**: %s", msg)
		if _, cerr := c.ghClient.AddComment(context.Background(), c.owner, c.repo, issueNumber, comment); cerr != nil {
			c.log.Warn("failed to post release-missing comment", "issue", issueNumber, "error", cerr)
		}
	}
}

// guardReleaseSHAReachable verifies that prState.HeadSHA is reachable from the default
// branch of the target repo before creating a release tag. A diverged SHA (from a
// force-push or a PR merged to a non-main branch) cannot be released from main and would
// loop forever without this guard. Fails open (returns nil) on transient API errors so
// a temporary outage doesn't block a valid release.
func (c *Controller) guardReleaseSHAReachable(ctx context.Context, owner, repo string, prState *PRState) error {
	branchName := c.resolveMainBranchName()
	branch, err := c.ghClient.GetBranch(ctx, owner, repo, branchName)
	if err != nil {
		// Transient — skip the guard rather than blocking a valid release.
		c.log.Warn("reachability guard: could not fetch default branch, skipping check",
			"pr", prState.PRNumber,
			"branch", branchName,
			"error", err,
		)
		return nil
	}
	mainSHA := branch.SHA()

	// Compare base=HeadSHA, head=mainSHA:
	//   "ahead"     → mainSHA contains HeadSHA as ancestor → reachable ✓
	//   "identical" → same commit → reachable ✓
	//   "behind"    → HeadSHA has commits main doesn't → not reachable from main ✗
	//   "diverged"  → both have exclusive commits → not reachable ✗
	status, err := c.ghClient.CompareStatus(ctx, owner, repo, prState.HeadSHA, mainSHA)
	if err != nil {
		// Transient — skip the guard.
		c.log.Warn("reachability guard: CompareStatus failed, skipping check",
			"pr", prState.PRNumber,
			"sha", ShortSHA(prState.HeadSHA),
			"error", err,
		)
		return nil
	}
	if status == "ahead" || status == "identical" {
		return nil
	}
	return fmt.Errorf("SHA %s is not reachable from %s (compare status: %q) — SHA may be from a diverged or force-pushed branch",
		ShortSHA(prState.HeadSHA), branchName, status)
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

	// GH-4069: record exactly once per PR-conflict event. handleMergeConflict
	// is re-entered on every poll tick while the conflict persists (and from
	// multiple call sites), so guard on ConflictRecorded rather than
	// incrementing unconditionally.
	if !prState.ConflictRecorded {
		c.metrics.RecordPRConflicting()
		prState.ConflictRecorded = true
	}

	// Try GitHub auto-update first (merge-from-base, not true rebase)
	err := c.ghClient.UpdatePullRequestBranch(ctx, c.owner, c.repo, prState.PRNumber)
	if err == nil {
		prState.RebaseAttempts++

		// GH-3715: A successful rebase returns the PR to StageWaitingCI without
		// consuming MergeAttempts or any other retry budget, so a PR can cycle
		// conflict -> rebase-success -> CI -> conflict indefinitely. Cap the
		// number of successful auto-rebases per PR and escalate instead of
		// rebasing again once the cap is reached.
		if prState.RebaseAttempts >= c.config.MaxRebaseAttempts {
			errMsg := fmt.Sprintf("auto-rebase oscillation: %d successful rebases without a clean merge — manual intervention required",
				prState.RebaseAttempts)
			c.log.Error("handleMergeConflict: rebase attempt cap reached — escalating to StageFailed",
				"pr", prState.PRNumber,
				"attempts", prState.RebaseAttempts,
				"max", c.config.MaxRebaseAttempts,
			)
			if prState.IssueNumber > 0 {
				comment := fmt.Sprintf(
					"⚠️ **Rebase escalation**: PR #%d has been auto-rebased %d times but keeps hitting merge conflicts.\n\nManual intervention is required — no further automatic rebases will be made.",
					prState.PRNumber, prState.RebaseAttempts)
				if _, cerr := c.ghClient.AddPRComment(ctx, c.owner, c.repo, prState.PRNumber, comment); cerr != nil {
					c.log.Warn("failed to post rebase escalation comment", "pr", prState.PRNumber, "error", cerr)
				}
			}
			prState.Stage = StageFailed
			prState.Error = errMsg
			c.metrics.RecordPRFailed()
			c.metrics.RecordIssueProcessed("failed")
			return nil
		}

		c.log.Info("auto-rebased conflicting PR", "pr", prState.PRNumber, "attempt", prState.RebaseAttempts, "max", c.config.MaxRebaseAttempts)
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
	// TASK-357 (B6a): evict the merge-idempotency record alongside activePRs/prFailures.
	// recordMergeSuccess sets recordedMerges[pr]=true and nothing else ever deleted it,
	// so over a long-lived daemon it grew without bound. Idempotency is only needed
	// while the PR is in flight; once removed it cannot be re-recorded by the live loop.
	delete(c.recordedMerges, prNumber)
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
		if c.recentlyEvictedForPersistFailure(pr.Number) {
			c.log.Debug("skipping PR recently evicted for persist failure", "pr", pr.Number, "branch", pr.Head.Ref)
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
	if c.rateLimitCooldownActive() {
		return
	}

	prs, err := c.ghClient.ListPullRequests(ctx, c.owner, c.repo, "open")
	if err != nil {
		var rlErr *github.RateLimitError
		if errors.As(err, &rlErr) {
			wait := c.enterRateLimitCooldown(rlErr.RetryAfter)
			c.log.Warn("reconciler: GitHub rate limit hit, pausing orphan-PR sweep until cooldown elapses",
				"cooldown", wait, "error", err)
			return
		}
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
		if c.recentlyEvictedForPersistFailure(pr.Number) {
			c.log.Debug("reconciler: skipping PR recently evicted for persist failure", "pr", pr.Number, "branch", pr.Head.Ref)
			continue
		}

		var issueNum int
		if _, err := fmt.Sscanf(pr.Head.Ref, "pilot/GH-%d", &issueNum); err != nil {
			c.log.Warn("reconciler: failed to parse branch name", "branch", pr.Head.Ref, "error", err)
			continue
		}

		// GH-3784: adopting an orphan PR within the 60s sweep interval is the
		// self-heal working as designed, not an anomaly — log at Info.
		c.log.Info("reconciler: adopting untracked open PR",
			"pr", pr.Number,
			"branch", pr.Head.Ref,
			"issue", issueNum,
		)
		c.OnPRCreated(pr.Number, pr.HTMLURL, issueNum, pr.Head.SHA, pr.Head.Ref, "")
		c.metrics.RecordOrphanPRRegistered("reconciler")
	}
}

// backstopCheckReleaseMissing is the scanner-side counterpart to afterTagCreated's
// verification (GH-3927). A hot-upgrade restart kills afterTagCreated's in-flight
// goroutine, and autopilot_pr_state rows are deleted on completion by design, so
// there is no persistence to resume verification from — this backstop re-derives
// it from GitHub state alone during ScanRecentlyMergedPRs's already-tagged branch.
// Fires (through the shared alertedMissingReleases dedup) when: publish mode is
// "workflow" or "api" (both expect a GitHub Release to exist — "tag_only" never
// does), verification is enabled, the merge is older than VerifyTimeout, and no
// GitHub Release exists for tag. Known limitation: only covers gaps up to
// MergedPRScanWindow (default 30m) after the goroutine would have fired, since
// that is how far back this scan looks.
func (c *Controller) backstopCheckReleaseMissing(ctx context.Context, rel *ReleaseConfig, prNumber, issueNumber int, tag string, mergedAt time.Time) {
	if rel == nil || !rel.VerifyReleaseEnabled() {
		return
	}
	mode := rel.PublishMode()
	if mode != ReleasePublishWorkflow && mode != ReleasePublishAPI {
		return
	}
	timeout := rel.VerifyTimeout
	if timeout <= 0 {
		timeout = releasePollTimeout
	}
	if time.Since(mergedAt) <= timeout {
		return
	}
	release, err := c.ghClient.GetReleaseByTag(ctx, c.owner, c.repo, tag)
	if err != nil {
		c.log.Warn("backstop: failed to check release for already-tagged PR, skipping",
			"pr", prNumber, "tag", tag, "error", err)
		return
	}
	if release != nil {
		return
	}
	c.fireReleaseMissingAlert(c.owner, c.repo, tag, prNumber, issueNumber, timeout)
}

// ScanRecentlyMergedPRs scans for Pilot PRs that were merged externally.
// This catches PRs that need release triggering but were merged outside of
// autopilot (e.g. via `gh pr merge` or the GitHub UI).
// Called on startup and periodically from the Run loop.
func (c *Controller) ScanRecentlyMergedPRs(ctx context.Context) error {
	// Run the scan unconditionally — it covers self-heal + merge metrics even when
	// neither auto-release nor board sync is enabled (e.g. a plain GH-issue-source
	// deployment). Internal gates below handle release-trigger and board-writeback
	// per-mode; both are idempotent so duplicate calls are safe.
	// releaseEnabled means "release configured at any trigger cadence" — a
	// PR under on_scope_close/on_schedule still needs the self-heal/board
	// bookkeeping above and the hold check below, not just on_merge (GH-3989).
	releaseEnabled := c.releaseConfigured()
	boardEnabled := c.boardSync != nil && c.doneStatus != ""
	rel := c.resolvedRelease()

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
		// Filter for Pilot branches (pilot/GH-* or pilot/*), or human-authored
		// PRs when release.tag_human_merges is enabled (GH-3928). rel.TagHumanMerges
		// is only read when releaseEnabled is true, which guarantees rel != nil
		// (releaseConfigured short-circuits on a nil rel).
		isPilotPR := strings.HasPrefix(pr.Head.Ref, "pilot/")
		tagHuman := releaseEnabled && rel.TagHumanMerges
		if !isPilotPR && !tagHuman {
			continue
		}

		// Human PRs only count toward releases when merged into the default
		// branch — merges into feature/integration branches are silently
		// skipped here rather than escalated later by guardReleaseSHAReachable.
		if !isPilotPR && pr.Base.Ref != c.resolveMainBranchName() {
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
		// GH-3928: gated on isPilotPR — human merges must not pollute merge
		// metrics, execution self-heal, or the board.
		if isPilotPR {
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
		}

		// Everything below is release-triggering machinery — skip it entirely when
		// release is disabled (the scan may be running for board sync alone).
		if !releaseEnabled {
			continue
		}

		// GH-3990: skip a merged PR that is a pending/in-flight scope-release
		// member — the carrier will tag it. This closes the window where the
		// scope has already completed (issue+parent closed, so heldByScope
		// would fail open) and the scanner would otherwise cut a redundant
		// per-merge tag for it ahead of the carrier.
		if c.stateStore != nil {
			if pending, err := c.stateStore.ScopeMemberPending(c.repoKey(), pr.Number); err != nil {
				c.log.Warn("failed to check scope-member pending state, will track to be safe",
					"pr", pr.Number, "error", err)
			} else if pending {
				c.log.Debug("skipping PR: member of a pending/in-flight scope release", "pr", pr.Number)
				continue
			}
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
			if age, found, err := c.stateStore.PersistedReleasingAge(c.repoKey(), pr.Number); err != nil {
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
				c.backstopCheckReleaseMissing(ctx, rel, pr.Number, issueNum, existingTag, mergedAt)
				continue
			}
		}

		if isPilotPR {
			c.log.Info("found merged Pilot PR needing release",
				"pr", pr.Number,
				"branch", pr.Head.Ref,
				"merged_at", mergedAt,
				"merge_sha", ShortSHA(pr.MergeCommitSHA),
			)
		} else {
			c.log.Info("found merged human PR needing release",
				"pr", pr.Number,
				"branch", pr.Head.Ref,
				"title", pr.Title,
			)
		}

		// GH-3989: on_scope_close/on_schedule may hold this PR instead of
		// registering it at StageReleasing. Held PRs are simply skipped here —
		// no StageReleasing registration — since they're fully reconstructable
		// from GitHub once the scope/schedule fires.
		if action, scopeKey, scopeTitle := c.releaseActionFor(ctx, issueNum); action == releaseActionHold {
			c.log.Info("holding merged PR for scope release (scan)",
				"pr", pr.Number, "scope", scopeKey, "scope_title", scopeTitle,
			)
			continue
		}

		// Create PR state and trigger release
		prState := &PRState{
			PRNumber:        pr.Number,
			PRURL:           pr.HTMLURL,
			IssueNumber:     issueNum,
			BranchName:      pr.Head.Ref,
			HeadSHA:         pr.MergeCommitSHA,
			CreatedAt:       time.Now(),
			EnvironmentName: c.config.EnvironmentName(),
			PRTitle:         pr.Title,
			TargetBranch:    pr.Base.Ref,
		}
		// GH-3994: require_ci must gate scan-recovery the same way it now gates
		// checkExternalMergeOrClose — route through StagePostMergeCI instead of
		// registering directly at StageReleasing with an assumed CISuccess.
		if rel.RequireCI {
			prState.Stage = StagePostMergeCI
			prState.PostMergeSHA = pr.MergeCommitSHA
			prState.PostMergeCIStartedAt = time.Now()
		} else {
			prState.Stage = StageReleasing
			prState.CIStatus = CISuccess // Assume CI passed if merged
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

	// GH-3939: Periodic epic-parent reconciliation. maybeCloseParentIssue only
	// fires reactively (when a sibling's own PR merges) and recoverStaleParentIssues
	// only runs once at startup, so a parent left behind by any other close path
	// (e.g. a child closed out-of-band) would otherwise never be revisited. This
	// ticker sweeps every open decomposed parent each poll cycle.
	epicParentTicker := time.NewTicker(basePollInterval)
	defer epicParentTicker.Stop()

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
		case <-epicParentTicker.C:
			// GH-3939: poll-cycle epic-parent auto-close sweep.
			c.reconcileEpicParents(ctx)
			// GH-3990: sweep closed epics that completed without an enqueued
			// scope release (daemon down at close time, or crashed between
			// close and enqueue).
			c.reconcileClosedEpicScopes(ctx)
			// GH-3991: poll-cycle label-scope completion sweep — the second
			// scope kind (sibling issues sharing a "scope:<name>" label, no
			// epic parent).
			c.reconcileLabelScopes(ctx)
			// GH-3990: claim any scope releases now unblocked, and re-drive
			// stale 'releasing' rows with no live carrier.
			c.startPendingScopeReleases(ctx)
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

// rateLimitCooldownActive reports whether a prior GitHub primary-rate-limit
// response is still within its backoff window. GH-3784.
func (c *Controller) rateLimitCooldownActive() bool {
	c.mu.RLock()
	until := c.rateLimitedUntil
	c.mu.RUnlock()
	return time.Now().Before(until)
}

// enterRateLimitCooldown records a backoff window so processAllPRs and
// reconcileOrphanPRs stop re-hitting the GitHub API on every tracked PR every
// tick and instead wait out the reported quota reset. Returns the (bounded)
// cooldown actually applied, for logging.
//
// GH-3784: PRs #3778/#3781 sat approved-and-green for 40-80 minutes because a
// sustained "API rate limit exceeded" 403 window had no backoff — every 10-60s
// tick re-fetched every tracked PR, burning the little quota headroom that
// existed and extending the outage instead of waiting it out.
func (c *Controller) enterRateLimitCooldown(retryAfter time.Duration) time.Duration {
	const minCooldown = 30 * time.Second
	const maxCooldown = 20 * time.Minute
	if retryAfter < minCooldown {
		retryAfter = minCooldown
	}
	if retryAfter > maxCooldown {
		retryAfter = maxCooldown
	}
	c.mu.Lock()
	c.rateLimitedUntil = time.Now().Add(retryAfter)
	c.mu.Unlock()
	return retryAfter
}

// processAllPRs processes all active PRs in one iteration.
func (c *Controller) processAllPRs(ctx context.Context) {
	if c.rateLimitCooldownActive() {
		c.log.Debug("processAllPRs: skipping tick, GitHub rate-limit cooldown active")
		return
	}

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
				var rlErr *github.RateLimitError
				if errors.As(err, &rlErr) {
					wait := c.enterRateLimitCooldown(rlErr.RetryAfter)
					c.log.Warn("processAllPRs: GitHub rate limit hit, pausing PR processing until cooldown elapses",
						"pr", pr.PRNumber, "cooldown", wait, "error", err)
					return
				}
				if isNotFoundError(err) {
					pr.mu.Lock()
					pr.NotFoundCount++
					notFoundCount := pr.NotFoundCount
					pr.mu.Unlock()
					if notFoundCount >= notFoundEvictionThreshold {
						c.evictNotFoundPR(pr.PRNumber)
						continue
					}
					c.log.Warn("failed to fetch PR", "pr", pr.PRNumber, "error", err, "not_found_count", notFoundCount)
					continue
				}
				c.log.Warn("failed to fetch PR", "pr", pr.PRNumber, "error", err)
				continue
			}
			pr.mu.Lock()
			pr.NotFoundCount = 0
			pr.mu.Unlock()

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

// isNotFoundError reports whether err represents a GitHub API 404 response.
// studio-sdk's github client wraps non-2xx responses as a plain
// fmt.Errorf("API error (status %d): ...", code, msg) with no typed error to
// check via errors.As, so the status-code substring is the only signal available.
func isNotFoundError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 404")
}

// notFoundEvictionThreshold bounds how many consecutive 404s fetching a
// tracked PR are tolerated before the row is evicted. Guards against a stale
// or foreign PR-state row (e.g. GH-3903: rows written before repo scoping
// existed, or any future collision) driving an infinite "failed to fetch PR"
// retry loop.
const notFoundEvictionThreshold = 5

// evictNotFoundPR drops a PR that has 404'd repeatedly from in-memory
// tracking and the persisted state store. Unlike removePR, it deliberately
// does NOT attempt remote branch cleanup: a repeated 404 means this
// controller has no verified relationship to the PR, so touching c.owner/
// c.repo's remote state based on nothing but a matching number would repeat
// the exact wrong-repo mutation this eviction exists to prevent (GH-3903).
func (c *Controller) evictNotFoundPR(prNumber int) {
	c.mu.Lock()
	prState, ok := c.activePRs[prNumber]
	var headSHA string
	if ok {
		headSHA = prState.HeadSHA
		delete(c.activePRs, prNumber)
	}
	delete(c.prFailures, prNumber)
	delete(c.recordedMerges, prNumber)
	c.mu.Unlock()

	// GH-862: mirror removePR's discovery-state cleanup so an evicted PR
	// doesn't leak an entry in ciMonitor's discovery map forever.
	if headSHA != "" {
		c.ciMonitor.ClearDiscovery(headSHA)
	}

	c.persistRemovePR(prNumber)
	c.removePRFailures(prNumber)
	c.log.Warn("evicted PR after repeated 404s fetching it — stale or foreign state-store row",
		"pr", prNumber, "repo", c.repoKey(), "threshold", notFoundEvictionThreshold)
}

// checkExternalMergeOrClose checks if a PR was merged or closed externally (by human).
// Returns true if the PR was removed from tracking, false otherwise.
// Accepts cached ghPR to avoid redundant API calls.
func (c *Controller) checkExternalMergeOrClose(ctx context.Context, prState *PRState, ghPR *github.PullRequest) bool {
	// GH-3990: this PRState is a scope-release carrier, not the real PR entry
	// for prState.PRNumber (its anchor is an already-merged member PR reused
	// as the release vehicle). The external-merge hijack must never touch it —
	// the carrier's own StagePostMergeCI/StageReleasing flow owns its lifecycle.
	if prState.ScopeKey != "" {
		return false
	}

	// GH-3994: once a PR has entered StagePostMergeCI — via handleMerged's
	// webhook path or via the RequireCI branch below — it stays Merged=true
	// on GitHub for the rest of its life. Without this guard the polled tick
	// loop calls this function again on every subsequent tick and re-runs the
	// whole external-merge flow (re-notify, re-close-issue, re-post the merge
	// comment) and — worse — the GH-411 block below would re-evaluate release
	// and force the PR straight to StageReleasing on the very next tick,
	// skipping the CI wait outright. handlePostMergeCI's own tick now owns it.
	if prState.Stage == StagePostMergeCI {
		return false
	}

	// GH-4124: a PR already in the release pipeline is owned by handleReleasing's
	// own tick — the external-merge drain below must not remove it before the tag
	// is cut (same reasoning as the StagePostMergeCI guard above; GH-3994). Without
	// this guard, a require_ci merged PR routed post_merge_ci -> releasing gets
	// drained here on the very next tick because the GH-411 block below only fires
	// when Stage != StageReleasing, and execution falls through straight to
	// removePR — so handleReleasing never runs and no tag is ever cut.
	if prState.Stage == StageReleasing {
		return false
	}

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
			// GH-4021: same stale-label cleanup as the polled-merge path.
			c.clearRetryLabels(ctx, prState.IssueNumber)
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
		if c.releaseConfigured() && prState.Stage != StageReleasing {
			action, scopeKey, _ := c.releaseActionFor(ctx, prState.IssueNumber)
			if action == releaseActionRelease {
				// GH-3994: require_ci must gate the polled/external-merge path the
				// same way it gates handleMerged — route through StagePostMergeCI
				// instead of hijacking straight to StageReleasing.
				if c.resolvedRelease().RequireCI {
					c.log.Info("externally merged PR requires post-merge CI before releasing", "pr", prState.PRNumber)
					if ghPR.MergeCommitSHA != "" {
						prState.PostMergeSHA = ghPR.MergeCommitSHA
					}
					prState.PostMergeCIStartedAt = time.Now()
					prState.Stage = StagePostMergeCI
					c.persistPRState(prState)
					return false // Continue processing; handlePostMergeCI takes over next tick
				}
				c.log.Info("triggering release for externally merged PR", "pr", prState.PRNumber)
				// Update SHA to merge commit if available
				if ghPR.MergeCommitSHA != "" {
					prState.HeadSHA = ghPR.MergeCommitSHA
				}
				prState.Stage = StageReleasing
				c.persistPRState(prState)
				return false // Continue processing to handle release
			}

			// GH-3989: held for scope/schedule release — drain the PR like any
			// other externally-merged, non-releasing PR, but leave a one-time
			// breadcrumb on the issue so held-vs-forgotten is visible from GitHub.
			c.log.Info("holding externally merged PR for scope release", "pr", prState.PRNumber, "scope", scopeKey)
			if prState.IssueNumber > 0 {
				comment := fmt.Sprintf("held for scope release %s", scopeKey)
				if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, comment); err != nil {
					c.log.Warn("failed to post scope-hold comment after external merge", "issue", prState.IssueNumber, "error", err)
				}
			}
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

// getBotLogin returns the authenticated GitHub login of the Pilot token.
// The value is resolved lazily on first call and then cached. Returns "" when the
// login cannot be determined; callers must skip the human-recovery-PR guard in that case.
func (c *Controller) getBotLogin(ctx context.Context) string {
	c.mu.RLock()
	login := c.cachedBotLogin
	c.mu.RUnlock()
	if login != "" {
		return login
	}

	user, err := c.ghClient.GetAuthenticatedUser(ctx)
	if err != nil {
		c.log.Warn("could not fetch authenticated user login, GH-3417 recovery-PR human-guard disabled", "error", err)
		return ""
	}
	c.mu.Lock()
	c.cachedBotLogin = user.Login
	c.mu.Unlock()
	return user.Login
}

// clearRetryLabels removes any pilot-retry-* bookkeeping labels once an issue's
// work has genuinely shipped (merged, internally or externally). Left in place,
// a stale pilot-retry-ready/pilot-retry-N label survives to the next poll and
// arms a redundant auto-retry dispatch against already-shipped work — GH-4021:
// pilot-retry-ready outlived a successful merge by five minutes and fired a
// third, redundant dispatch that raced the orphan-row cleanup into a false
// task_failed alert.
func (c *Controller) clearRetryLabels(ctx context.Context, issueNumber int) {
	for _, label := range []string{github.LabelRetryReady, github.LabelRetry1, github.LabelRetry2, github.LabelRetryExhausted} {
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, issueNumber, label); err != nil {
			// 404 is expected when the label was never set - silently ignore.
			c.log.Debug("retry label cleanup", "issue", issueNumber, "label", label, "error", err)
		}
	}
}

// notifyExternalClose runs once autopilot observes a PR closed without a merge —
// whether a human closed it, or autopilot closed it itself a poll cycle earlier
// (handleCIFailed/handleReviewRequested/handleMergeConflict set prState.Error and
// return; this is the next place execution reaches once the close is visible on
// GitHub). Every non-merge close converges here, which makes it the single place
// to guarantee GH-3806's audit trail: a PR comment naming the reason (plus a CI
// run link when a SHA is known) and a matching issue comment, even along the
// branches that intentionally skip label changes below.
//
// GH-1015: Marks the issue as pilot-retry-ready so it can be re-picked by the
// poller — unless prState.TerminalLabel says the failure is terminal or already
// continues under a different issue number, in which case that label is used
// instead so the issue is never silently re-queued.
func (c *Controller) notifyExternalClose(ctx context.Context, prState *PRState) {
	c.log.Info("PR closed externally without merge", "pr", prState.PRNumber, "issue", prState.IssueNumber)

	reason := prState.Error
	if reason == "" {
		reason = "closed without merging (no reason recorded)"
	}

	// GH-3818/D10: reclassify any "completed" execution row for this issue to
	// "failed" now that we know its PR was discarded — otherwise HasCompletedExecution
	// keeps trusting the stale row and the poller re-marks the issue pilot-done on
	// every subsequent poll even though nothing shipped. A later merge heals this
	// back to "completed" via SelfHealExecutionAfterMerge.
	if c.evalStore != nil && prState.IssueNumber > 0 {
		taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
		if err := c.evalStore.ReclassifyCompletionAsFailed(taskID, c.projectPath, reason); err != nil {
			c.log.Warn("failed to reclassify completed execution after PR close",
				"task_id", taskID, "pr", prState.PRNumber, "error", err)
		}
	}

	prComment := fmt.Sprintf("This PR was closed without merging: %s", reason)
	if prState.HeadSHA != "" {
		prComment += fmt.Sprintf("\n\nCI run: https://github.com/%s/%s/commit/%s/checks", c.owner, c.repo, prState.HeadSHA)
	}
	if _, err := c.ghClient.AddPRComment(ctx, c.owner, c.repo, prState.PRNumber, prComment); err != nil {
		c.log.Warn("failed to comment on closed PR", "pr", prState.PRNumber, "error", err)
	}

	// GH-1015: Add pilot-retry-ready label so the issue can be retried
	// Remove pilot-in-progress to allow the poller to re-pick it
	if prState.IssueNumber > 0 {
		issueComment := fmt.Sprintf("PR #%d was closed without merging: %s", prState.PRNumber, reason)

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
			// GH-3806: pilot-done here means an earlier PR for this issue already
			// shipped — the label is intentionally left untouched, but this PR's
			// discarded work must not vanish silently just because of that.
			issueComment += "\n\nThe issue is already marked pilot-done from an earlier PR, so its labels were left unchanged. This closed PR represents separate, discarded work."
			if _, cerr := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, issueComment); cerr != nil {
				c.log.Warn("failed to comment on issue after PR close", "issue", prState.IssueNumber, "error", cerr)
			}
			c.maybeCloseParentIssue(ctx, prState)
			return
		}

		// GH-3417: Skip pilot-retry-ready when a human recovery PR is already open
		// for this issue. Re-dispatching via retry-ready would overwrite the human's
		// branch (git checkout -B in worktree.go). Guard only fires when we can
		// resolve the bot's own login; if the lookup fails, fall through to the
		// existing retry-ready behaviour (safe default).
		if botLogin := c.getBotLogin(ctx); botLogin != "" {
			prs, searchErr := c.ghClient.SearchOpenPRsForIssue(ctx, c.owner, c.repo, prState.IssueNumber)
			if searchErr == nil {
				for _, pr := range prs {
					if pr.User != nil && pr.User.Login != botLogin {
						c.log.Info("skipping pilot-retry-ready: human recovery PR already open",
							"issue", prState.IssueNumber,
							"recovery_pr", pr.HTMLURL,
							"author", pr.User.Login)
						issueComment += fmt.Sprintf("\n\nA human recovery PR (%s) is already open for this issue, so it was left as-is instead of being re-queued.", pr.HTMLURL)
						if _, cerr := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, issueComment); cerr != nil {
							c.log.Warn("failed to comment on issue after PR close", "issue", prState.IssueNumber, "error", cerr)
						}
						c.maybeCloseParentIssue(ctx, prState)
						return
					}
				}
			}
		}

		// GH-3806: a close path that already knows the failure is terminal, or
		// that a dependent follow-up issue now owns the retry, sets TerminalLabel
		// so this issue is marked pilot-failed instead of silently re-queued
		// (which would either retry a cascade that already hit its cap, or
		// double-dispatch work a follow-up issue is already doing).
		issueLabel := github.LabelRetryReady
		nextSteps := "The issue has been marked pilot-retry-ready and will be retried automatically."
		if prState.TerminalLabel != "" {
			issueLabel = prState.TerminalLabel
			nextSteps = "This issue will not be retried automatically under its own number — see the reason above for what happens next."
		}

		if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, prState.IssueNumber, []string{issueLabel}); err != nil {
			c.log.Warn("failed to set issue label on PR close", "issue", prState.IssueNumber, "label", issueLabel, "error", err)
		}
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelInProgress); err != nil {
			c.log.Warn("failed to remove pilot-in-progress label", "issue", prState.IssueNumber, "error", err)
		}
		if issueLabel != github.LabelFailed {
			// Remove stale pilot-failed label (GH-1302 gap) — only when we're not
			// the ones setting it above.
			if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelFailed); err != nil {
				c.log.Debug("failed to remove pilot-failed (may not exist)", "issue", prState.IssueNumber, "error", err)
			}
		}
		c.log.Info("corrected issue label on PR close", "issue", prState.IssueNumber, "pr", prState.PRNumber, "label", issueLabel)

		issueComment += "\n\n" + nextSteps
		if _, cerr := c.ghClient.AddComment(ctx, c.owner, c.repo, prState.IssueNumber, issueComment); cerr != nil {
			c.log.Warn("failed to comment on issue after PR close", "issue", prState.IssueNumber, "error", cerr)
		}
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
