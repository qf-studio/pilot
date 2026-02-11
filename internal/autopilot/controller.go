package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/approval"
)

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

// Controller orchestrates the autopilot loop for PR processing.
// It manages the state machine: PR created → CI check → merge → post-merge CI → feedback loop.
type Controller struct {
	config       *Config
	ghClient     *github.Client
	approvalMgr  *approval.Manager
	ciMonitor    *CIMonitor
	autoMerger   *AutoMerger
	feedbackLoop *FeedbackLoop
	releaser     *Releaser
	notifier     Notifier
	log          *slog.Logger

	// State tracking
	activePRs map[int]*PRState
	mu        sync.RWMutex

	// Persistent state store (optional, nil = in-memory only)
	stateStore *StateStore

	// Per-PR circuit breaker: each PR has independent failure tracking.
	// A failure on one PR does not block other PRs.
	prFailures map[int]*prFailureState

	// Metrics
	metrics *Metrics

	// Owner and repo for GitHub operations
	owner string
	repo  string
}

// NewController creates an autopilot controller with all required components.
func NewController(cfg *Config, ghClient *github.Client, approvalMgr *approval.Manager, owner, repo string) *Controller {
	c := &Controller{
		config:      cfg,
		ghClient:    ghClient,
		approvalMgr: approvalMgr,
		owner:       owner,
		repo:        repo,
		activePRs:   make(map[int]*PRState),
		prFailures:  make(map[int]*prFailureState),
		metrics:     NewMetrics(),
		log:         slog.Default().With("component", "autopilot"),
	}

	c.ciMonitor = NewCIMonitor(ghClient, owner, repo, cfg)
	c.autoMerger = NewAutoMerger(ghClient, approvalMgr, c.ciMonitor, owner, repo, cfg)
	c.feedbackLoop = NewFeedbackLoop(ghClient, owner, repo, cfg)

	// Initialize releaser if release config exists
	if cfg.Release != nil && cfg.Release.Enabled {
		c.releaser = NewReleaser(ghClient, owner, repo, cfg.Release)
	}

	return c
}

// SetNotifier sets the notifier for autopilot events.
// This is optional; if not set, no notifications will be sent.
func (c *Controller) SetNotifier(n Notifier) {
	c.notifier = n
}

// SetStateStore sets the persistent state store for crash recovery.
// If set, all state transitions are persisted to SQLite.
func (c *Controller) SetStateStore(store *StateStore) {
	c.stateStore = store
}

// persistPRState saves a PR state to the store if available.
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

// OnPRCreated registers a new PR for autopilot processing.
func (c *Controller) OnPRCreated(prNumber int, prURL string, issueNumber int, headSHA string, branchName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prState := &PRState{
		PRNumber:    prNumber,
		PRURL:       prURL,
		IssueNumber: issueNumber,
		BranchName:  branchName,
		HeadSHA:     headSHA,
		Stage:       StagePRCreated,
		CIStatus:    CIPending,
		CreatedAt:   time.Now(),
	}
	c.activePRs[prNumber] = prState

	// Persist to SQLite (outside lock is fine, persist is idempotent)
	c.persistPRState(prState)

	c.log.Info("PR registered for autopilot",
		"pr", prNumber,
		"url", prURL,
		"issue", issueNumber,
		"branch", branchName,
		"sha", ShortSHA(headSHA),
		"stage", StagePRCreated,
		"env", c.config.Environment,
	)
}

// ProcessPR processes a single PR through the state machine.
// Returns error if processing fails; caller should retry based on error type.
func (c *Controller) ProcessPR(ctx context.Context, prNumber int) error {
	c.mu.RLock()
	prState, ok := c.activePRs[prNumber]
	c.mu.RUnlock()

	if !ok {
		return fmt.Errorf("PR %d not tracked", prNumber)
	}

	// Per-PR circuit breaker check
	if c.isPRCircuitOpen(prNumber) {
		c.log.Warn("per-PR circuit breaker open", "pr", prNumber)
		c.metrics.RecordCircuitBreakerTrip()
		return fmt.Errorf("circuit breaker: PR %d has too many consecutive failures", prNumber)
	}

	previousStage := prState.Stage
	var err error

	switch prState.Stage {
	case StagePRCreated:
		err = c.handlePRCreated(ctx, prState)
	case StageWaitingCI:
		err = c.handleWaitingCI(ctx, prState)
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
	case StageReleasing:
		err = c.handleReleasing(ctx, prState)
	case StageFailed:
		// Terminal state - no processing
		return nil
	}

	// Log stage transitions
	if prState.Stage != previousStage {
		c.log.Info("PR stage transition",
			"pr", prNumber,
			"from", previousStage,
			"to", prState.Stage,
			"env", c.config.Environment,
		)
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
func (c *Controller) handlePRCreated(ctx context.Context, prState *PRState) error {
	c.log.Debug("handlePRCreated: starting CI monitoring",
		"pr", prState.PRNumber,
		"sha", ShortSHA(prState.HeadSHA),
	)

	// GH-724: Check for merge conflicts immediately after PR creation.
	// Concurrent merges can make a PR conflicting before CI even starts.
	ghPR, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("failed to check PR mergeable state on creation", "pr", prState.PRNumber, "error", err)
		// Non-fatal: proceed to CI wait, conflict will be caught there
	} else if c.isMergeConflict(ghPR) {
		return c.handleMergeConflict(ctx, prState)
	}

	// All environments wait for CI - no skipping
	prState.Stage = StageWaitingCI
	prState.CIWaitStartedAt = time.Now()
	return nil
}

// handleWaitingCI checks CI status once (non-blocking) and updates state.
// Uses CheckCI instead of WaitForCI to prevent blocking the processing loop.
func (c *Controller) handleWaitingCI(ctx context.Context, prState *PRState) error {
	// Initialize CIWaitStartedAt if not set (backwards compatibility)
	if prState.CIWaitStartedAt.IsZero() {
		prState.CIWaitStartedAt = time.Now()
	}

	// Check for CI timeout
	ciTimeout := c.config.CIWaitTimeout
	if c.config.Environment == EnvDev && c.config.DevCITimeout > 0 {
		ciTimeout = c.config.DevCITimeout
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
	ghPR, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("failed to fetch PR head SHA", "pr", prState.PRNumber, "error", err)
		if sha == "" {
			return nil // Can't check CI without SHA, retry next cycle
		}
		// Fall through with existing SHA if we have one
	} else if ghPR.Head.SHA != "" {
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
		c.log.Warn("CI status check failed", "pr", prState.PRNumber, "sha", ShortSHA(sha), "error", err)
		// Don't fail the PR on transient errors, will retry next poll cycle
		return nil
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

// handleCIPassed proceeds to merge (with approval for prod).
func (c *Controller) handleCIPassed(ctx context.Context, prState *PRState) error {
	c.log.Info("handleCIPassed: CI passed, determining next stage",
		"pr", prState.PRNumber,
		"env", c.config.Environment,
		"auto_merge", c.config.AutoMerge,
	)

	if c.config.Environment == EnvProd {
		c.log.Info("prod mode: awaiting approval", "pr", prState.PRNumber)
		prState.Stage = StageAwaitApproval

		// Notify approval required
		if c.notifier != nil {
			if err := c.notifier.NotifyApprovalRequired(ctx, prState); err != nil {
				c.log.Warn("failed to send approval notification", "error", err)
			}
		}
	} else {
		c.log.Info("dev/stage mode: proceeding to merge",
			"pr", prState.PRNumber,
			"env", c.config.Environment,
		)
		prState.Stage = StageMerging
	}
	return nil
}

// handleCIFailed creates fix issue via feedback loop.
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

	issueNum, err := c.feedbackLoop.CreateFailureIssue(ctx, prState, FailureCIPreMerge, failedChecks, "")
	if err != nil {
		return fmt.Errorf("failed to create fix issue: %w", err)
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

	prState.Stage = StageFailed
	c.metrics.RecordPRFailed()
	return nil
}

// handleAwaitApproval waits for human approval (prod only).
func (c *Controller) handleAwaitApproval(ctx context.Context, prState *PRState) error {
	// This will block until approval received or timeout
	err := c.autoMerger.MergePR(ctx, prState)
	if err != nil {
		if err.Error() == "merge rejected: approval denied" {
			c.log.Info("merge approval denied", "pr", prState.PRNumber)
			prState.Stage = StageFailed
			return nil
		}
		return err
	}
	prState.Stage = StageMerged

	// Notify merge success after approval
	if c.notifier != nil {
		if err := c.notifier.NotifyMerged(ctx, prState); err != nil {
			c.log.Warn("failed to send merge notification", "error", err)
		}
	}

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
		return fmt.Errorf("merge attempt %d failed: %w", prState.MergeAttempts, err)
	}

	c.log.Info("PR merged successfully", "pr", prState.PRNumber)
	prState.Stage = StageMerged
	c.metrics.RecordPRMerged()
	c.metrics.RecordPRTimeToMerge(time.Since(prState.CreatedAt))

	// Notify merge success
	if c.notifier != nil {
		if err := c.notifier.NotifyMerged(ctx, prState); err != nil {
			c.log.Warn("failed to send merge notification", "error", err)
		}
	}

	return nil
}

// handleMerged checks post-merge CI (stage/prod).
func (c *Controller) handleMerged(ctx context.Context, prState *PRState) error {
	c.log.Info("handleMerged: PR merged, checking next steps",
		"pr", prState.PRNumber,
		"env", c.config.Environment,
		"should_release", c.shouldTriggerRelease(),
	)

	if c.config.Environment == EnvDev {
		// Dev: check if we should release without waiting for post-merge CI
		if c.shouldTriggerRelease() && !c.config.Release.RequireCI {
			c.log.Info("dev mode: proceeding to release (no post-merge CI required)",
				"pr", prState.PRNumber,
			)
			prState.Stage = StageReleasing
			return nil
		}
		c.log.Info("dev mode: PR complete", "pr", prState.PRNumber)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Stage/Prod: wait for post-merge CI
	c.log.Info("stage/prod mode: waiting for post-merge CI",
		"pr", prState.PRNumber,
		"env", c.config.Environment,
	)
	prState.Stage = StagePostMergeCI
	return nil
}

// handlePostMergeCI monitors deployment/post-merge checks.
func (c *Controller) handlePostMergeCI(ctx context.Context, prState *PRState) error {
	// Get merge commit SHA from main branch
	// For now, use head SHA - in production, should get actual merge commit
	mainSHA, err := c.getMainBranchSHA(ctx)
	if err != nil {
		c.log.Warn("failed to get main branch SHA, using head SHA", "error", err)
		mainSHA = prState.HeadSHA
	}

	status, err := c.ciMonitor.WaitForCI(ctx, mainSHA)
	if err != nil {
		return err
	}

	if status == CIFailure {
		c.log.Warn("post-merge CI failed", "pr", prState.PRNumber)
		failedChecks, _ := c.ciMonitor.GetFailedChecks(ctx, mainSHA)
		issueNum, err := c.feedbackLoop.CreateFailureIssue(ctx, prState, FailureCIPostMerge, failedChecks, "")
		if err != nil {
			c.log.Error("failed to create post-merge fix issue", "error", err)
		} else {
			c.log.Info("created fix issue for post-merge CI failure", "pr", prState.PRNumber, "issue", issueNum)
		}
		c.removePR(prState.PRNumber)
		return nil
	}

	// CI passed - check if we should release
	if c.shouldTriggerRelease() {
		prState.Stage = StageReleasing
		return nil
	}

	c.log.Info("post-merge CI passed", "pr", prState.PRNumber)
	c.removePR(prState.PRNumber)
	return nil
}

// getMainBranchSHA returns the current SHA of the main branch.
func (c *Controller) getMainBranchSHA(ctx context.Context) (string, error) {
	branch, err := c.ghClient.GetBranch(ctx, c.owner, c.repo, "main")
	if err != nil {
		return "", err
	}
	return branch.SHA(), nil
}

// shouldTriggerRelease returns true if auto-release is configured.
func (c *Controller) shouldTriggerRelease() bool {
	return c.config.Release != nil &&
		c.config.Release.Enabled &&
		c.config.Release.Trigger == "on_merge"
}

// handleReleasing creates a release after successful merge and CI.
func (c *Controller) handleReleasing(ctx context.Context, prState *PRState) error {
	if c.releaser == nil {
		c.log.Debug("releaser not configured, skipping release", "pr", prState.PRNumber)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Race condition guard: Check if this commit already has a tag.
	// When multiple PRs merge rapidly, each triggers handleReleasing but only
	// the first should create a tag. Subsequent PRs will see their merge commit
	// is already tagged (by an earlier release) and skip.
	existingTag, err := c.ghClient.GetTagForSHA(ctx, c.owner, c.repo, prState.HeadSHA)
	if err != nil {
		c.log.Warn("failed to check existing tags", "error", err)
		// Continue anyway - worst case we get a duplicate tag error
	} else if existingTag != "" {
		c.log.Info("commit already tagged, skipping release",
			"pr", prState.PRNumber,
			"sha", ShortSHA(prState.HeadSHA),
			"tag", existingTag,
		)
		c.removePR(prState.PRNumber)
		return nil
	}

	// Get current version
	currentVersion, err := c.releaser.GetCurrentVersion(ctx)
	if err != nil {
		c.log.Warn("failed to get current version, defaulting to 0.0.0", "error", err)
		currentVersion = SemVer{}
	}

	// Get PR commits for bump detection
	commits, err := c.ghClient.GetPRCommits(ctx, c.owner, c.repo, prState.PRNumber)
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
	prState.ReleaseVersion = newVersion.String(c.config.Release.TagPrefix)

	c.log.Info("creating release",
		"pr", prState.PRNumber,
		"current", currentVersion.String(c.config.Release.TagPrefix),
		"new", prState.ReleaseVersion,
		"bump", bumpType,
	)

	// Create git tag only — GoReleaser CI handles the full release with binaries
	tagName, err := c.releaser.CreateTag(ctx, prState, newVersion)
	if err != nil {
		return fmt.Errorf("failed to create tag: %w", err)
	}

	releaseURL := fmt.Sprintf("https://github.com/%s/%s/releases/tag/%s", c.owner, c.repo, tagName)
	c.log.Info("tag created (GoReleaser will create release)",
		"pr", prState.PRNumber,
		"version", prState.ReleaseVersion,
		"tag", tagName,
	)

	// Send notification
	if c.config.Release.NotifyOnRelease && c.notifier != nil {
		if n, ok := c.notifier.(ReleaseNotifier); ok {
			if err := n.NotifyReleased(ctx, prState, releaseURL); err != nil {
				c.log.Warn("failed to send release notification", "error", err)
			}
		}
	}

	c.removePR(prState.PRNumber)
	return nil
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

// handleMergeConflict closes a conflicting PR, comments, and returns the issue to the queue.
// The issue will be re-picked by the poller and re-executed from updated main.
func (c *Controller) handleMergeConflict(ctx context.Context, prState *PRState) error {
	c.log.Warn("merge conflict detected",
		"pr", prState.PRNumber,
		"issue", prState.IssueNumber,
		"branch", prState.BranchName,
	)

	// Add comment explaining the closure
	comment := "Merge conflict detected. Closing PR so the issue can be re-executed from updated main."
	if _, err := c.ghClient.AddPRComment(ctx, c.owner, c.repo, prState.PRNumber, comment); err != nil {
		c.log.Warn("failed to comment on conflicting PR", "pr", prState.PRNumber, "error", err)
	}

	// Close the PR
	if err := c.ghClient.ClosePullRequest(ctx, c.owner, c.repo, prState.PRNumber); err != nil {
		c.log.Warn("failed to close conflicting PR", "pr", prState.PRNumber, "error", err)
	}

	// Remove pilot-in-progress label from the issue so poller can re-pick it
	if prState.IssueNumber > 0 {
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, prState.IssueNumber, github.LabelInProgress); err != nil {
			c.log.Warn("failed to remove in-progress label", "issue", prState.IssueNumber, "error", err)
		}
	}

	prState.Stage = StageFailed
	prState.Error = "merge conflict with base branch"
	return nil
}

// removePR removes PR from tracking.
func (c *Controller) removePR(prNumber int) {
	c.mu.Lock()
	delete(c.activePRs, prNumber)
	delete(c.prFailures, prNumber)
	c.mu.Unlock()

	c.persistRemovePR(prNumber)
	c.removePRFailures(prNumber)
	c.log.Info("PR removed from tracking", "pr", prNumber)
}

// GetActivePRs returns all tracked PRs.
func (c *Controller) GetActivePRs() []*PRState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	prs := make([]*PRState, 0, len(c.activePRs))
	for _, pr := range c.activePRs {
		prs = append(prs, pr)
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

		c.log.Info("restoring Pilot PR for tracking",
			"pr", pr.Number,
			"branch", pr.Head.Ref,
			"sha", ShortSHA(pr.Head.SHA),
			"issue", issueNum,
		)

		// Register PR via existing mechanism
		c.OnPRCreated(pr.Number, pr.HTMLURL, issueNum, pr.Head.SHA, pr.Head.Ref)
		restored++
	}

	c.log.Info("completed PR scan", "restored", restored, "env", c.config.Environment)
	return nil
}

// ScanRecentlyMergedPRs scans for Pilot PRs that were merged while Pilot was offline.
// This catches PRs that need release triggering but were merged externally.
// Called on startup after ScanExistingPRs.
func (c *Controller) ScanRecentlyMergedPRs(ctx context.Context) error {
	// Skip if auto-release is not enabled
	if !c.shouldTriggerRelease() {
		c.log.Debug("skipping merged PR scan: auto-release not enabled")
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

	// Get recent releases to check for existing releases
	releases, err := c.ghClient.ListReleases(ctx, c.owner, c.repo, 20)
	if err != nil {
		c.log.Warn("failed to list releases, continuing without release check", "error", err)
		releases = nil
	}

	// Build set of release target commits for quick lookup
	releasedCommits := make(map[string]bool)
	for _, rel := range releases {
		if rel.TargetCommitish != "" {
			releasedCommits[rel.TargetCommitish] = true
		}
	}

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

		// Skip if release already exists for this merge commit
		if pr.MergeCommitSHA != "" && releasedCommits[pr.MergeCommitSHA] {
			c.log.Debug("skipping PR: release already exists",
				"pr", pr.Number,
				"merge_sha", ShortSHA(pr.MergeCommitSHA),
			)
			continue
		}

		// Extract issue number from branch name (optional)
		var issueNum int
		if strings.HasPrefix(pr.Head.Ref, "pilot/GH-") {
			_, _ = fmt.Sscanf(pr.Head.Ref, "pilot/GH-%d", &issueNum)
		}

		c.log.Info("found merged Pilot PR needing release",
			"pr", pr.Number,
			"branch", pr.Head.Ref,
			"merged_at", mergedAt,
			"merge_sha", ShortSHA(pr.MergeCommitSHA),
		)

		// Create PR state and trigger release
		prState := &PRState{
			PRNumber:    pr.Number,
			PRURL:       pr.HTMLURL,
			IssueNumber: issueNum,
			BranchName:  pr.Head.Ref,
			HeadSHA:     pr.MergeCommitSHA,
			Stage:       StageReleasing,
			CIStatus:    CISuccess, // Assume CI passed if merged
			CreatedAt:   time.Now(),
		}

		// Register and trigger release
		c.mu.Lock()
		c.activePRs[pr.Number] = prState
		c.mu.Unlock()
		c.persistPRState(prState)

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
		"env", c.config.Environment,
		"poll_interval", c.config.CIPollInterval,
		"ci_timeout", c.config.CIWaitTimeout,
		"auto_merge", c.config.AutoMerge,
		"release_enabled", c.config.Release != nil && c.config.Release.Enabled,
	)

	ticker := time.NewTicker(c.config.CIPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("autopilot controller stopping")
			return ctx.Err()
		case <-ticker.C:
			c.processAllPRs(ctx)
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

	for _, pr := range prs {
		select {
		case <-ctx.Done():
			return
		default:
			c.log.Debug("checking PR",
				"pr", pr.PRNumber,
				"stage", pr.Stage,
				"ci_status", pr.CIStatus,
			)

			// Check if PR was merged/closed externally before processing
			if c.checkExternalMergeOrClose(ctx, pr) {
				continue
			}

			if err := c.ProcessPR(ctx, pr.PRNumber); err != nil {
				// Error already logged in ProcessPR
				continue
			}
		}
	}
}

// checkExternalMergeOrClose checks if a PR was merged or closed externally (by human).
// Returns true if the PR was removed from tracking, false otherwise.
func (c *Controller) checkExternalMergeOrClose(ctx context.Context, prState *PRState) bool {
	ghPR, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("failed to check PR state", "pr", prState.PRNumber, "error", err)
		return false
	}

	// Check if PR was merged externally
	if ghPR.Merged {
		c.log.Info("PR merged externally", "pr", prState.PRNumber)
		c.notifyExternalMerge(ctx, prState)

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

// notifyExternalClose sends notification when a PR is closed externally.
// Currently logs only; can be extended to use a specific notifier method.
func (c *Controller) notifyExternalClose(ctx context.Context, prState *PRState) {
	// No specific notification for closed PRs yet
	// This is a hook for future extension
	c.log.Info("PR closed externally without merge", "pr", prState.PRNumber, "issue", prState.IssueNumber)
}
