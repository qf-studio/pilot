package autopilot

import (
	"context"
	"fmt"
	"log/slog"
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
	c.autoMerger = NewAutoMerger(ghClient, approvalMgr, owner, repo, cfg)
	c.feedbackLoop = NewFeedbackLoop(ghClient, owner, repo, cfg)

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

	c.log.Info("PR registered for autopilot", "pr", prNumber)
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
	case StageFailed:
		// Terminal state - no processing
		return nil
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

// handlePRCreated starts CI monitoring or skips for dev.
func (c *Controller) handlePRCreated(ctx context.Context, prState *PRState) error {
	if c.config.Environment == EnvDev {
		// Dev: skip CI, go straight to merge
		c.log.Info("dev mode: skipping CI", "pr", prState.PRNumber)
		prState.Stage = StageCIPassed
	} else {
		// Stage/Prod: wait for CI
		prState.Stage = StageWaitingCI
	}
	return nil
}

// handleWaitingCI polls CI status until complete.
func (c *Controller) handleWaitingCI(ctx context.Context, prState *PRState) error {
	status, err := c.ciMonitor.WaitForCI(ctx, prState.HeadSHA)
	if err != nil {
		return err
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
	}

	return nil
}

// handleCIPassed proceeds to merge (with approval for prod).
func (c *Controller) handleCIPassed(ctx context.Context, prState *PRState) error {
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

	err := c.autoMerger.MergePR(ctx, prState)
	if err != nil {
		return fmt.Errorf("merge attempt %d failed: %w", prState.MergeAttempts, err)
	}

	c.log.Info("PR merged", "pr", prState.PRNumber)
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
	if c.config.Environment == EnvDev {
		// Dev: done
		c.log.Info("dev mode: PR complete", "pr", prState.PRNumber)
		c.removePR(prState.PRNumber)
		return nil
	}
	c.log.Info("waiting for post-merge CI", "pr", prState.PRNumber)
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
	} else {
		c.log.Info("post-merge CI passed", "pr", prState.PRNumber)
	}

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

// Run starts the autopilot processing loop.
// It continuously processes all active PRs until context is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	c.log.Info("autopilot controller started", "env", c.config.Environment)

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

	c.log.Debug("processing PRs", "count", len(prs))

	for _, pr := range prs {
		select {
		case <-ctx.Done():
			return
		default:
			if err := c.ProcessPR(ctx, pr.PRNumber); err != nil {
				// Error already logged in ProcessPR
				continue
			}
		}
	}
}
