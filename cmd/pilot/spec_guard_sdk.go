package main

import (
	"context"
	"log/slog"
	"strings"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/ghissue"
)

// applySpecGuardSDK runs the two-strike spec validation gate for an issue on
// the SDK poll path (M7 4d.3). SDK-typed sibling of applySpecGuard — the two
// share semantics and the comment marker; the in-tree version retires with
// the adapter package. Returns true when dispatch should be skipped.
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
