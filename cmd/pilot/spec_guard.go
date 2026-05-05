package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/pilot/internal/adapters/github"
)

// applySpecGuard runs the two-strike spec validation gate for a GitHub issue.
// Returns true when dispatch should be skipped (guard fired), false to proceed.
//
// First call with a failing issue adds pilot-spec-incomplete and posts a structured comment.
// Subsequent call (comment marker already present) escalates to pilot-blocked.
func applySpecGuard(ctx context.Context, client *github.Client, owner, repo string, issue *github.Issue, reasons []string) bool {
	comments, err := client.ListIssueComments(ctx, owner, repo, issue.Number)
	if err != nil {
		slog.Warn("spec-guard: failed to list comments, skipping guard",
			slog.Int("issue", issue.Number), slog.Any("error", err))
		return false
	}

	markerFound := false
	for _, c := range comments {
		if strings.Contains(c.Body, github.SpecCommentMarker) {
			markerFound = true
			break
		}
	}

	if !markerFound {
		// First strike: warn and ask the author to improve the body.
		if err := client.AddLabels(ctx, owner, repo, issue.Number, []string{github.LabelSpecIncomplete}); err != nil {
			logGitHubAPIError("AddLabels[spec-incomplete]", owner, repo, issue.Number, err)
		}
		comment := buildSpecIncompleteComment(reasons)
		if _, err := client.AddComment(ctx, owner, repo, issue.Number, comment); err != nil {
			logGitHubAPIError("AddComment[spec-incomplete]", owner, repo, issue.Number, err)
		}
		slog.Warn("spec-guard: first strike — issue body too thin, dispatch skipped",
			slog.Int("issue", issue.Number), slog.Any("reasons", reasons))
	} else {
		// Second strike: block the issue.
		if err := client.AddLabels(ctx, owner, repo, issue.Number, []string{github.LabelBlocked}); err != nil {
			logGitHubAPIError("AddLabels[blocked]", owner, repo, issue.Number, err)
		}
		slog.Warn("spec-guard: second strike — escalating to pilot-blocked",
			slog.Int("issue", issue.Number))
	}
	return true
}

// buildSpecIncompleteComment renders the structured first-strike comment.
func buildSpecIncompleteComment(reasons []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", github.SpecCommentMarker)
	fmt.Fprintf(&b, "⚠️ Pilot can't dispatch this issue: the spec body is too thin to execute against.\n\n")
	fmt.Fprintf(&b, "**What failed:**\n")
	for _, r := range reasons {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	fmt.Fprintf(&b, "\n**Suggested template:**\n\n")
	fmt.Fprintf(&b, "```markdown\n")
	fmt.Fprintf(&b, "## Context\n\n<!-- Describe what this changes and why -->\n\n")
	fmt.Fprintf(&b, "## Acceptance\n\n- [ ] ...\n\n")
	fmt.Fprintf(&b, "## Implementation\n\n<!-- Optional: describe the approach -->\n")
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "To retry: edit the issue body, then remove the `%s` label.\n", github.LabelSpecIncomplete)
	fmt.Fprintf(&b, "If `%s` was also added, remove that too.\n", github.LabelBlocked)
	return b.String()
}
