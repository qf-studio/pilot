package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/memory"
)

// FeedbackLoop creates issues when CI fails or bugs are detected.
// It closes the autonomous loop by automatically creating fix issues
// that Pilot can pick up and execute.
type FeedbackLoop struct {
	ghClient     *github.Client
	owner        string
	repo         string
	issueLabels  []string
	learningLoop *memory.LearningLoop // GH-1979: optional, annotates issues with known patterns
	log          *slog.Logger
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

// SetLearningLoop injects a learning loop for pattern annotation in fix issues.
func (f *FeedbackLoop) SetLearningLoop(ll *memory.LearningLoop) {
	f.learningLoop = ll
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
	// FailureReviewRequested indicates a human reviewer requested changes on the PR.
	FailureReviewRequested FailureType = "review_requested"
)

// CreateFailureIssue creates a GitHub issue for a CI/deployment failure.
// The iteration parameter tracks how many CI fix attempts have been chained
// (0 = original PR, 1 = first fix, etc.). It is embedded in autopilot-meta
// so downstream fix issues can inherit and increment the counter.
// Returns the issue number on success.
func (f *FeedbackLoop) CreateFailureIssue(ctx context.Context, prState *PRState, failureType FailureType, failedChecks []string, logs string, iteration int) (int, error) {
	title := f.generateTitle(prState, failureType)

	// GH-1979: Surface known patterns to annotate the fix issue body.
	var knownPatterns []*memory.CrossPattern
	if f.learningLoop != nil {
		projectPath := f.owner + "/" + f.repo
		patterns, err := f.learningLoop.SurfaceHighValuePatterns(ctx, projectPath)
		if err != nil {
			f.log.Warn("failed to surface patterns for fix issue", "error", err)
		} else {
			knownPatterns = patterns
		}
	}

	body := f.generateBody(prState, failureType, failedChecks, logs, iteration, knownPatterns)

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
	case FailureReviewRequested:
		return fmt.Sprintf("Address review feedback on PR #%d", prState.PRNumber)
	default:
		return fmt.Sprintf("Fix issue from PR #%d", prState.PRNumber)
	}
}

// generateBody creates a detailed issue body with context for Pilot.
func (f *FeedbackLoop) generateBody(prState *PRState, failureType FailureType, failedChecks []string, logs string, iteration int, knownPatterns []*memory.CrossPattern) string {
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

	// GH-1979: Known patterns section — helps Pilot avoid past mistakes.
	if len(knownPatterns) > 0 {
		sb.WriteString("## Known Patterns\n\n")
		sb.WriteString("These patterns have been learned from previous failures in this project:\n\n")
		for _, p := range knownPatterns {
			sb.WriteString(fmt.Sprintf("- **%s** (confidence: %.0f%%): %s\n", p.Title, p.Confidence*100, p.Description))
		}
		sb.WriteString("\n")
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
	case FailureReviewRequested:
		sb.WriteString("A reviewer requested changes on this PR. Address the feedback in the review comments above.\n")
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

// CreateReviewIssue creates a GitHub issue for review feedback (changes requested).
// reviews are top-level review bodies, comments are line-level code annotations.
// iteration tracks how many review-fix attempts have been chained.
// Returns the issue number on success.
func (f *FeedbackLoop) CreateReviewIssue(ctx context.Context, prState *PRState, reviews []*github.PullRequestReview, comments []*github.PRReviewComment, iteration int) (int, error) {
	title := f.generateTitle(prState, FailureReviewRequested)

	var sb strings.Builder
	sb.WriteString("# Autopilot: Review Feedback\n\n")

	// Context section
	sb.WriteString("## Context\n\n")
	sb.WriteString(fmt.Sprintf("- **Original PR**: #%d\n", prState.PRNumber))
	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("- **Original Issue**: #%d\n", prState.IssueNumber))
	}
	sb.WriteString(fmt.Sprintf("- **Failure Type**: %s\n", FailureReviewRequested))
	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", prState.BranchName))
	}
	sb.WriteString("\n")

	// Review feedback section
	feedback := formatReviewFeedback(reviews, comments)
	if feedback != "" {
		sb.WriteString("## Review Comments\n\n")
		sb.WriteString(feedback)
		sb.WriteString("\n")
	}

	// Task instructions
	sb.WriteString("## Task\n\n")
	sb.WriteString("A reviewer requested changes on this PR. Address the feedback in the review comments above.\n")

	if prState.IssueNumber > 0 {
		sb.WriteString(fmt.Sprintf("\nDepends on: #%d\n", prState.IssueNumber))
	}

	sb.WriteString("\n---\n*This issue was auto-generated by Pilot autopilot.*\n")

	if prState.BranchName != "" {
		sb.WriteString(fmt.Sprintf("\n<!-- autopilot-meta branch:%s pr:%d iteration:%d -->\n", prState.BranchName, prState.PRNumber, iteration))
	}

	body := sb.String()

	input := &github.IssueInput{
		Title:  title,
		Body:   body,
		Labels: f.issueLabels,
	}

	issue, err := f.ghClient.CreateIssue(ctx, f.owner, f.repo, input)
	if err != nil {
		return 0, fmt.Errorf("failed to create review issue: %w", err)
	}

	f.log.Info("created review issue",
		"issue", issue.Number,
		"pr", prState.PRNumber,
	)

	return issue.Number, nil
}

// formatReviewFeedback formats review comments into collapsible <details> blocks per file.
// Top-level review bodies are grouped under "General", line-level comments are grouped by file path.
// Output is truncated to 4000 characters to avoid GitHub issue body limits.
func formatReviewFeedback(reviews []*github.PullRequestReview, comments []*github.PRReviewComment) string {
	var sb strings.Builder

	// Group top-level review bodies
	for _, r := range reviews {
		if r.Body == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("<details><summary>Review by %s (%s)</summary>\n\n", r.User.Login, r.State))
		sb.WriteString(r.Body)
		sb.WriteString("\n\n</details>\n\n")
	}

	// Group line-level comments by file
	fileComments := make(map[string][]*github.PRReviewComment)
	var fileOrder []string
	for _, c := range comments {
		if _, seen := fileComments[c.Path]; !seen {
			fileOrder = append(fileOrder, c.Path)
		}
		fileComments[c.Path] = append(fileComments[c.Path], c)
	}

	for _, path := range fileOrder {
		sb.WriteString(fmt.Sprintf("<details><summary>%s</summary>\n\n", path))
		for _, c := range fileComments[path] {
			if c.Line > 0 {
				sb.WriteString(fmt.Sprintf("**Line %d** (%s):\n", c.Line, c.User.Login))
			} else {
				sb.WriteString(fmt.Sprintf("**Comment** (%s):\n", c.User.Login))
			}
			sb.WriteString(c.Body)
			sb.WriteString("\n\n")
		}
		sb.WriteString("</details>\n\n")
	}

	result := sb.String()
	if len(result) > 4000 {
		result = result[:4000] + "\n... (truncated)\n"
	}
	return result
}
