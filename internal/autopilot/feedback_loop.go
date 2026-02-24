package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

// FeedbackLoop creates issues when CI fails or bugs are detected.
// It closes the autonomous loop by automatically creating fix issues
// that Pilot can pick up and execute.
type FeedbackLoop struct {
	ghClient    *github.Client
	owner       string
	repo        string
	issueLabels []string
	log         *slog.Logger
}

// NewFeedbackLoop creates a feedback loop for automatic issue creation.
func NewFeedbackLoop(ghClient *github.Client, owner, repo string, cfg *Config) *FeedbackLoop {
	return &FeedbackLoop{
		ghClient:    ghClient,
		owner:       owner,
		repo:        repo,
		issueLabels: cfg.IssueLabels,
		log:         slog.Default().With("component", "feedback-loop"),
	}
}

// FailureType categorizes the type of failure that occurred.
type FailureType string

const (
	// FailureCIPreMerge indicates CI failed before the PR was merged.
	FailureCIPreMerge FailureType = "ci_pre_merge"
	// FailureCIPostMerge indicates CI failed after the PR was merged to main.
	FailureCIPostMerge FailureType = "ci_post_merge"
	// FailureMerge indicates the PR could not be merged due to conflicts.
	FailureMerge FailureType = "merge_conflict"
	// FailureDeployment indicates deployment failed after merge.
	FailureDeployment FailureType = "deployment"
)

// CreateFailureIssue creates a GitHub issue for a CI/deployment failure.
// The iteration parameter tracks how many CI fix attempts have been chained
// (0 = original PR, 1 = first fix, etc.). It is embedded in autopilot-meta
// so downstream fix issues can inherit and increment the counter.
// Returns the issue number on success.
func (f *FeedbackLoop) CreateFailureIssue(ctx context.Context, prState *PRState, failureType FailureType, failedChecks []string, logs string, iteration int) (int, error) {
	title := f.generateTitle(prState, failureType)
	body := f.generateBody(prState, failureType, failedChecks, logs, iteration)

	input := &github.IssueInput{
		Title:  title,
		Body:   body,
		Labels: f.issueLabels,
	}

	issue, err := f.ghClient.CreateIssue(ctx, f.owner, f.repo, input)
	if err != nil {
		return 0, fmt.Errorf("failed to create issue: %w", err)
	}

	f.log.Info("created fix issue",
		"issue", issue.Number,
		"pr", prState.PRNumber,
		"failure", failureType,
	)

	return issue.Number, nil
}

// generateTitle creates an issue title based on the failure type.
func (f *FeedbackLoop) generateTitle(prState *PRState, failureType FailureType) string {
	switch failureType {
	case FailureCIPreMerge:
		return fmt.Sprintf("Fix CI failure from PR #%d", prState.PRNumber)
	case FailureCIPostMerge:
		return fmt.Sprintf("Fix post-merge CI failure (PR #%d)", prState.PRNumber)
	case FailureMerge:
		return fmt.Sprintf("Resolve merge conflict for PR #%d", prState.PRNumber)
	case FailureDeployment:
		return fmt.Sprintf("Fix deployment failure (PR #%d)", prState.PRNumber)
	default:
		return fmt.Sprintf("Fix issue from PR #%d", prState.PRNumber)
	}
}

// generateBody creates a detailed issue body with context for Pilot.
func (f *FeedbackLoop) generateBody(prState *PRState, failureType FailureType, failedChecks []string, logs string, iteration int) string {
	var sb strings.Builder

	sb.WriteString("# Autopilot: Auto-Generated Fix Request\n\n")

	// Context section
	sb.WriteString("## Context\n\n")
	sb.WriteString(fmt.Sprintf("- **Original PR**: #%d\n", prState.PRNumber))
	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("- **Original Issue**: #%d\n", prState.IssueNumber))
	}
	sb.WriteString(fmt.Sprintf("- **Failure Type**: %s\n", failureType))
	if len(prState.HeadSHA) >= 7 {
		sb.WriteString(fmt.Sprintf("- **SHA**: %s\n", prState.HeadSHA[:7]))
	}
	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", prState.BranchName))
	}
	sb.WriteString("\n")

	// Failed checks section
	if len(failedChecks) > 0 {
		sb.WriteString("## Failed Checks\n\n")
		for _, check := range failedChecks {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", check))
		}
		sb.WriteString("\n")
	}

	// Error logs section in collapsible details block (GH-1567)
	if logs != "" {
		sb.WriteString("<details><summary>CI Error Logs</summary>\n\n")
		sb.WriteString("```\n")
		if len(logs) > 2000 {
			sb.WriteString(logs[:2000])
			sb.WriteString("\n... (truncated)")
		} else {
			sb.WriteString(logs)
		}
		sb.WriteString("\n```\n\n")
		sb.WriteString("</details>\n\n")
	}

	// Task instructions for Pilot
	sb.WriteString("## Task\n\n")
	switch failureType {
	case FailureCIPreMerge:
		sb.WriteString("Fix the CI failures listed above. Run tests locally before committing.\n")
	case FailureCIPostMerge:
		sb.WriteString("The PR was merged but CI failed afterward. Investigate and fix.\n")
	case FailureMerge:
		sb.WriteString("Resolve the merge conflicts and ensure the changes integrate properly.\n")
	case FailureDeployment:
		sb.WriteString("The deployment failed. Check logs and fix the deployment issue.\n")
	default:
		sb.WriteString("Investigate and fix the issue described above.\n")
	}

	// Wire dependency so fix issue waits for parent to close
	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("\nDepends on: #%d\n", prState.IssueNumber))
	}

	sb.WriteString("\n---\n*This issue was auto-generated by Pilot autopilot.*\n")

	// Machine-readable metadata for poller to parse original branch and PR number.
	// GH-1267: Include pr:N so fix sessions can use --from-pr for context resumption.
	// GH-1566: Include iteration:N to track CI fix cascade depth and enforce limits.
	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("\n<!-- autopilot-meta branch:%s pr:%d iteration:%d -->\n", prState.BranchName, prState.PRNumber, iteration))
	}

	return sb.String()
}
