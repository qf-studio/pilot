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

	// Circuit breaker
	consecutiveFailures int

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

// OnPRCreated registers a new PR for autopilot processing.
func (c *Controller) OnPRCreated(prNumber int, prURL string, issueNumber int, headSHA string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.activePRs[prNumber] = &PRState{
		PRNumber:    prNumber,
		PRURL:       prURL,
		IssueNumber: issueNumber,
		HeadSHA:     headSHA,
		Stage:       StagePRCreated,
		CIStatus:    CIPending,
		CreatedAt:   time.Now(),
	}

	c.log.Info("PR registered for autopilot",
		"pr", prNumber,
		"url", prURL,
		"issue", issueNumber,
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

	// Circuit breaker check
	if c.consecutiveFailures >= c.config.MaxFailures {
		c.log.Warn("circuit breaker open", "failures", c.consecutiveFailures)
		return fmt.Errorf("circuit breaker: too many consecutive failures (%d)", c.consecutiveFailures)
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
		c.consecutiveFailures++
		prState.Error = err.Error()
		c.log.Error("autopilot stage failed", "pr", prNumber, "stage", prState.Stage, "error", err)
	} else {
		c.consecutiveFailures = 0
	}

	return err
}

// handlePRCreated starts CI monitoring for all environments.
func (c *Controller) handlePRCreated(ctx context.Context, prState *PRState) error {
	c.log.Debug("handlePRCreated: starting CI monitoring",
		"pr", prState.PRNumber,
		"sha", ShortSHA(prState.HeadSHA),
	)
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

	// Non-blocking CI status check
	status, err := c.ciMonitor.CheckCI(ctx, prState.HeadSHA)
	if err != nil {
		c.log.Warn("CI status check failed", "pr", prState.PRNumber, "error", err)
		// Don't fail the PR on transient errors, will retry next poll cycle
		return nil
	}

	prState.CIStatus = status
	prState.LastChecked = time.Now()

	switch status {
	case CISuccess:
		c.log.Info("CI passed", "pr", prState.PRNumber)
		prState.Stage = StageCIPassed
	case CIFailure:
		c.log.Warn("CI failed", "pr", prState.PRNumber)
		prState.Stage = StageCIFailed
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
	prState.Stage = StageFailed
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

	// Generate changelog
	var changelog string
	if c.config.Release.GenerateChangelog {
		changelog = GenerateChangelog(commits, prState.PRNumber)
	} else {
		changelog = fmt.Sprintf("Release from PR #%d", prState.PRNumber)
	}

	// Create release
	release, err := c.releaser.CreateRelease(ctx, prState, newVersion, changelog)
	if err != nil {
		return fmt.Errorf("failed to create release: %w", err)
	}

	c.log.Info("release created",
		"pr", prState.PRNumber,
		"version", prState.ReleaseVersion,
		"url", release.HTMLURL,
	)

	// Send notification
	if c.config.Release.NotifyOnRelease && c.notifier != nil {
		if n, ok := c.notifier.(ReleaseNotifier); ok {
			if err := n.NotifyReleased(ctx, prState, release.HTMLURL); err != nil {
				c.log.Warn("failed to send release notification", "error", err)
			}
		}
	}

	c.removePR(prState.PRNumber)
	return nil
}

// removePR removes PR from tracking.
func (c *Controller) removePR(prNumber int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.activePRs, prNumber)
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

// ResetCircuitBreaker resets the consecutive failure counter.
// Call this after manual intervention or system recovery.
func (c *Controller) ResetCircuitBreaker() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveFailures = 0
	c.log.Info("circuit breaker reset")
}

// IsCircuitOpen returns true if the circuit breaker has tripped.
func (c *Controller) IsCircuitOpen() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.consecutiveFailures >= c.config.MaxFailures
}

// Config returns the autopilot configuration.
func (c *Controller) Config() *Config {
	return c.config
}

// ConsecutiveFailures returns the current consecutive failure count.
func (c *Controller) ConsecutiveFailures() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.consecutiveFailures
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
		c.OnPRCreated(pr.Number, pr.HTMLURL, issueNum, pr.Head.SHA)
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
			HeadSHA:     pr.MergeCommitSHA,
			Stage:       StageReleasing,
			CIStatus:    CISuccess, // Assume CI passed if merged
			CreatedAt:   time.Now(),
		}

		// Register and trigger release
		c.mu.Lock()
		c.activePRs[pr.Number] = prState
		c.mu.Unlock()

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
