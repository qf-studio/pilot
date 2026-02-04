package autopilot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/approval"
)

// AutoMerger handles PR merging with environment-aware safety.
// Environment behavior:
//   - dev: immediate merge, no approval required
//   - stage: merge after CI passes, no approval required
//   - prod: merge after CI passes, requires human approval
type AutoMerger struct {
	ghClient    *github.Client
	approvalMgr *approval.Manager
	ciMonitor   *CIMonitor
	owner       string
	repo        string
	config      *Config
	log         *slog.Logger
}

// NewAutoMerger creates an auto-merger with the given configuration.
func NewAutoMerger(ghClient *github.Client, approvalMgr *approval.Manager, ciMonitor *CIMonitor, owner, repo string, cfg *Config) *AutoMerger {
	return &AutoMerger{
		ghClient:    ghClient,
		approvalMgr: approvalMgr,
		ciMonitor:   ciMonitor,
		owner:       owner,
		repo:        repo,
		config:      cfg,
		log:         slog.Default().With("component", "auto-merger"),
	}
}

// MergePR merges a PR with environment-appropriate safety checks.
// For prod environment, requests human approval before merge.
func (m *AutoMerger) MergePR(ctx context.Context, prState *PRState) error {
	env := m.config.Environment

	m.log.Info("MergePR: starting merge process",
		"pr", prState.PRNumber,
		"env", env,
		"method", m.config.MergeMethod,
		"auto_review", m.config.AutoReview,
		"sha", ShortSHA(prState.HeadSHA),
	)

	// Check if approval required (prod only)
	if m.requiresApproval(env) {
		approved, err := m.requestApproval(ctx, prState)
		if err != nil {
			return fmt.Errorf("approval request failed: %w", err)
		}
		if !approved {
			return fmt.Errorf("merge rejected: approval denied")
		}
	}

	// Auto-review if enabled (creates approval review on the PR)
	if m.config.AutoReview {
		if err := m.approvePR(ctx, prState.PRNumber); err != nil {
			m.log.Warn("auto-review failed", "pr", prState.PRNumber, "error", err)
			// Continue anyway - might not need review or already reviewed
		}
	}

	// Final CI verification immediately before merge to prevent race conditions.
	// CI status can change between initial check and merge, so we verify again.
	if m.ShouldWaitForCI(env) {
		if err := m.verifyCIBeforeMerge(ctx, prState); err != nil {
			return fmt.Errorf("pre-merge CI verification failed: %w", err)
		}
	}

	// Determine merge method, defaulting to squash
	mergeMethod := m.config.MergeMethod
	if mergeMethod == "" {
		mergeMethod = github.MergeMethodSquash
	}

	// Merge the PR
	commitTitle := fmt.Sprintf("Merge PR #%d", prState.PRNumber)
	if err := m.ghClient.MergePullRequest(ctx, m.owner, m.repo, prState.PRNumber, mergeMethod, commitTitle); err != nil {
		return fmt.Errorf("merge failed: %w", err)
	}

	m.log.Info("PR merged", "pr", prState.PRNumber, "method", mergeMethod)
	return nil
}

// requiresApproval checks if environment needs human approval for merge.
func (m *AutoMerger) requiresApproval(env Environment) bool {
	return env == EnvProd
}

// requestApproval requests human approval via the approval manager.
func (m *AutoMerger) requestApproval(ctx context.Context, prState *PRState) (bool, error) {
	if m.approvalMgr == nil {
		return false, fmt.Errorf("approval manager not configured")
	}

	// Check if approval stage is enabled
	if !m.approvalMgr.IsStageEnabled(approval.StagePreMerge) {
		m.log.Warn("pre-merge approval stage not enabled, auto-approving",
			"pr", prState.PRNumber)
		return true, nil
	}

	req := &approval.Request{
		TaskID:      fmt.Sprintf("merge-pr-%d", prState.PRNumber),
		Stage:       approval.StagePreMerge,
		Title:       fmt.Sprintf("PR #%d Merge Approval", prState.PRNumber),
		Description: fmt.Sprintf("Approve merge of PR #%d to production?", prState.PRNumber),
		Metadata: map[string]interface{}{
			"pr_url":    prState.PRURL,
			"pr_number": prState.PRNumber,
			"head_sha":  prState.HeadSHA,
		},
	}

	m.log.Info("requesting merge approval",
		"pr", prState.PRNumber,
		"url", prState.PRURL)

	result, err := m.approvalMgr.RequestApproval(ctx, req)
	if err != nil {
		return false, err
	}

	approved := result.Decision == approval.DecisionApproved
	m.log.Info("approval response",
		"pr", prState.PRNumber,
		"decision", result.Decision,
		"approved_by", result.ApprovedBy)

	return approved, nil
}

// approvePR creates an approval review on the PR.
func (m *AutoMerger) approvePR(ctx context.Context, prNumber int) error {
	body := "Auto-approved by Pilot autopilot"
	return m.ghClient.ApprovePullRequest(ctx, m.owner, m.repo, prNumber, body)
}

// CanMerge checks if a PR is in a mergeable state.
// Returns (canMerge, reason, error).
func (m *AutoMerger) CanMerge(ctx context.Context, prNumber int) (bool, string, error) {
	pr, err := m.ghClient.GetPullRequest(ctx, m.owner, m.repo, prNumber)
	if err != nil {
		return false, "", fmt.Errorf("failed to get PR: %w", err)
	}

	if pr.Merged {
		return false, "already merged", nil
	}
	if pr.State == "closed" {
		return false, "PR is closed", nil
	}
	if pr.Mergeable != nil && !*pr.Mergeable {
		return false, "merge conflicts", nil
	}

	return true, "", nil
}

// ShouldWaitForCI returns true if the environment requires CI to pass before merge.
// All environments now wait for CI to prevent broken code from merging.
func (m *AutoMerger) ShouldWaitForCI(env Environment) bool {
	return true
}

// verifyCIBeforeMerge performs a final CI status check immediately before merge.
// This prevents race conditions where CI status changes between initial check and merge.
func (m *AutoMerger) verifyCIBeforeMerge(ctx context.Context, prState *PRState) error {
	if m.ciMonitor == nil {
		m.log.Warn("CI monitor not configured, skipping pre-merge CI verification",
			"pr", prState.PRNumber)
		return nil
	}

	m.log.Debug("verifyCIBeforeMerge: checking CI status",
		"pr", prState.PRNumber,
		"sha", ShortSHA(prState.HeadSHA))

	status, err := m.ciMonitor.GetCIStatus(ctx, prState.HeadSHA)
	if err != nil {
		m.log.Error("verifyCIBeforeMerge: failed to get CI status",
			"pr", prState.PRNumber,
			"error", err)
		return fmt.Errorf("failed to get CI status: %w", err)
	}

	m.log.Debug("verifyCIBeforeMerge: CI status retrieved",
		"pr", prState.PRNumber,
		"status", status)

	switch status {
	case CISuccess:
		m.log.Info("verifyCIBeforeMerge: CI passed",
			"pr", prState.PRNumber,
			"status", status)
		return nil
	case CIFailure:
		m.log.Warn("verifyCIBeforeMerge: CI failed",
			"pr", prState.PRNumber,
			"sha", ShortSHA(prState.HeadSHA))
		return fmt.Errorf("CI checks failing for SHA %s", prState.HeadSHA)
	case CIPending, CIRunning:
		m.log.Debug("verifyCIBeforeMerge: CI still running",
			"pr", prState.PRNumber,
			"status", status)
		return fmt.Errorf("CI checks still pending for SHA %s", prState.HeadSHA)
	default:
		return fmt.Errorf("unexpected CI status %s for SHA %s", status, prState.HeadSHA)
	}
}
