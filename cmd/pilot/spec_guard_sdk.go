package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/ghissue"
)

// applySpecGuardSDK runs the two-strike spec validation gate for an issue on
// the SDK poll path (M7 4d.3). Returns true when dispatch should be skipped.
//
// First call with a failing issue adds pilot-spec-incomplete and posts a
// structured comment. Subsequent call (comment marker already present)
// escalates to pilot-blocked. Board-failed-column sync is handled at the SDK
// poller level, not here.
func applySpecGuardSDK(ctx context.Context, client *githubSDK.Client, owner, repo string, issue *githubSDK.Issue, reasons []string) bool {
	comments, err := client.ListIssueComments(ctx, owner, repo, issue.Number)
	if err != nil {
		slog.Warn("spec-guard(sdk): failed to list comments, skipping guard",
			slog.Int("issue", issue.Number), slog.Any("error", err))
		return false
	}

	markerFound := false
	for _, c := range comments {
		if strings.Contains(c.Body, ghissue.SpecCommentMarker) {
			markerFound = true
			break
		}
	}

	if !markerFound {
		// First strike: warn and ask the author to improve the body.
		if err := client.AddLabels(ctx, owner, repo, issue.Number, []string{githubSDK.LabelSpecIncomplete}); err != nil {
			logGitHubAPIError("AddLabels[spec-incomplete,sdk]", owner, repo, issue.Number, err)
		}
		comment := buildSpecIncompleteComment(reasons)
		if _, err := client.AddComment(ctx, owner, repo, issue.Number, comment); err != nil {
			logGitHubAPIError("AddComment[spec-incomplete,sdk]", owner, repo, issue.Number, err)
		}
		slog.Warn("spec-guard(sdk): first strike — issue body too thin, dispatch skipped",
			slog.Int("issue", issue.Number), slog.Any("reasons", reasons))
	} else {
		// Second strike: block the issue.
		if err := client.AddLabels(ctx, owner, repo, issue.Number, []string{githubSDK.LabelBlocked}); err != nil {
			logGitHubAPIError("AddLabels[blocked,sdk]", owner, repo, issue.Number, err)
		}
		slog.Warn("spec-guard(sdk): second strike — escalating to pilot-blocked",
			slog.Int("issue", issue.Number))
	}

	return true
}

// buildSpecIncompleteComment renders the structured first-strike comment.
func buildSpecIncompleteComment(reasons []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ghissue.SpecCommentMarker)
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
	fmt.Fprintf(&b, "To retry: edit the issue body, then remove the `%s` label.\n", githubSDK.LabelSpecIncomplete)
	fmt.Fprintf(&b, "If `%s` was also added, remove that too.\n", githubSDK.LabelBlocked)
	return b.String()
}
