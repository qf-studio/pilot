package autopilot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/approval"
)

// ErrApprovalNotConfigured is returned when an environment requires pre-merge
// approval but the approval.pre_merge stage is not enabled in config.
var ErrApprovalNotConfigured = errors.New("approval not configured for required environment")

// misconfigCommentMarker is embedded in the PR comment so the idempotency
// check can detect it on repeated calls without scanning comment text.
const misconfigCommentMarker = "<!-- pilot-approval-misconfig -->"

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
// Approval is obtained upstream by the controller's handleAwaitApproval before MergePR is called.
func (m *AutoMerger) MergePR(ctx context.Context, prState *PRState) error {
	m.log.Info("MergePR: starting merge process",
		"pr", prState.PRNumber,
		"env", m.config.EnvironmentName(),
		"method", m.config.MergeMethod,
		"auto_review", m.config.AutoReview,
		"sha", ShortSHA(prState.HeadSHA),
	)

	// Auto-review if enabled (creates approval review on the PR)
	if m.config.AutoReview {
		if err := m.approvePR(ctx, prState.PRNumber); err != nil {
			m.log.Warn("auto-review failed", "pr", prState.PRNumber, "error", err)
			// Continue anyway - might not need review or already reviewed
		}
	}

	// Final CI verification immediately before merge to prevent race conditions.
	// CI status can change between initial check and merge, so we verify again.
	if m.ShouldWaitForCI(m.config.Environment) {
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
	// For squash merges, use PR title as commit message to preserve conventional commit prefixes
	// (e.g. "feat(scope): ...") so parseBumpFromMessage() can detect release bumps.
	commitTitle := fmt.Sprintf("Merge PR #%d", prState.PRNumber)
	if mergeMethod == github.MergeMethodSquash && prState.PRTitle != "" {
		commitTitle = prState.PRTitle
		// Strip "GH-XXXX: " prefix from Pilot-generated PR titles so
		// parseBumpFromMessage() sees the conventional commit prefix (e.g. "fix(scope): ...").
		if prState.IssueNumber > 0 {
			prefix := fmt.Sprintf("GH-%d: ", prState.IssueNumber)
			commitTitle = strings.TrimPrefix(commitTitle, prefix)
		}
	}
	if err := m.ghClient.MergePullRequest(ctx, m.owner, m.repo, prState.PRNumber, mergeMethod, commitTitle); err != nil {
		return fmt.Errorf("merge failed: %w", err)
	}

	m.log.Info("PR merged", "pr", prState.PRNumber, "method", mergeMethod)

	// GH-2432: Strip retry-counter labels off the linked issue so a future
	// regression on the same issue starts the retry budget from zero again.
	if prState.IssueNumber > 0 {
		for _, label := range github.RetryStateLabels {
			if err := m.ghClient.RemoveLabel(ctx, m.owner, m.repo, prState.IssueNumber, label); err != nil {
				m.log.Debug("retry label cleanup: remove failed (likely not present)",
					"issue", prState.IssueNumber, "label", label, "error", err)
			}
		}
	}

	return nil
}

// postMisconfigComment posts a single explanatory comment on the PR when
// approval is required (by an escalation gate or the environment) but not
// enabled in config. Idempotent: skips posting if the marker comment exists.
func (m *AutoMerger) postMisconfigComment(ctx context.Context, prState *PRState) {
	existing, err := m.ghClient.ListIssueComments(ctx, m.owner, m.repo, prState.PRNumber)
	if err != nil {
		m.log.Warn("postMisconfigComment: failed to list PR comments, will post anyway",
			"pr", prState.PRNumber, "error", err)
	} else {
		for _, c := range existing {
			if strings.Contains(c.Body, misconfigCommentMarker) {
				m.log.Debug("postMisconfigComment: already posted, skipping", "pr", prState.PRNumber)
				return
			}
		}
	}

	// GH-3569: name the actual escalation trigger (size-floor / scope-drift /
	// env require_approval) instead of always blaming require_approval.
	reason := prState.EscalationReason
	if reason == "" {
		reason = fmt.Sprintf("environments.%s.require_approval=true", m.config.EnvironmentName())
	}
	body := fmt.Sprintf(`%s
🚧 **Merge blocked: approval not wired**

This PR requires pre-merge approval (**%s**) but the approval system is disabled (`+"`approval.enabled: false`"+` and/or `+"`approval.pre_merge.enabled: false`"+`).

To unblock: either enable approval in `+"`~/.pilot/config.yaml`"+` (set `+"`approval.enabled: true`"+` + `+"`approval.pre_merge.enabled: true`"+` + add an approver), or merge manually with `+"`gh pr merge %d`"+`.

Tracked in #2598.`,
		misconfigCommentMarker, reason, prState.PRNumber)

	if _, postErr := m.ghClient.AddPRComment(ctx, m.owner, m.repo, prState.PRNumber, body); postErr != nil {
		m.log.Warn("postMisconfigComment: failed to post PR comment",
			"pr", prState.PRNumber, "error", postErr)
	}
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
