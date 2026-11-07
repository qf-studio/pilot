package main

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// rearmCommentMarker identifies the comment rearmDeadOwnerSource
// (internal/autopilot/owner_death.go) posts; only such comments are trusted as
// a source of a continuation branch, so a stray autopilot-meta quote in a human
// comment can never steer a retry onto another branch (GH-6183).
const rearmCommentMarker = "Owner-death recovery"

var autopilotSourceFieldRe = regexp.MustCompile(`\bsource:(\d+)`)

// issueCommentLister is the slice of the SDK client resolveRearmBranch needs.
type issueCommentLister interface {
	ListIssueComments(ctx context.Context, owner, repo string, number int) ([]*githubSDK.Comment, error)
}

// hasRetryFamilyLabel reports whether labels carry any pilot-retry-ready/-1/-2
// label. The SDK retry ladder advances pilot-retry-ready forward, so a re-armed
// source may already carry -1 or -2 by the time it is dispatched.
func hasRetryFamilyLabel(labels []string) bool {
	for _, l := range labels {
		for _, r := range retryReadyRearmLabels {
			if l == r {
				return true
			}
		}
	}
	return false
}

// parseRearmFooterFromComments returns the branch/PR/sha of the latest owner-
// death re-arm comment for issueNum, oldest-to-newest bodies in. A comment only
// counts when it is a re-arm comment, its footer parses (GH-5390 validation via
// parseAutopilotBranch), and its source: field names issueNum.
func parseRearmFooterFromComments(bodies []string, issueNum int) (branch string, pr int, sha string, ok bool) {
	for i := len(bodies) - 1; i >= 0; i-- {
		body := bodies[i]
		if !strings.Contains(body, rearmCommentMarker) {
			continue
		}
		b := parseAutopilotBranch(body)
		if b == "" {
			continue
		}
		footer := extractAutopilotMetaFooter(body)
		if m := autopilotSourceFieldRe.FindStringSubmatch(footer); len(m) < 2 || m[1] != strconv.Itoa(issueNum) {
			continue
		}
		return b, parseAutopilotPR(body), parseAutopilotSHA(body), true
	}
	return "", 0, "", false
}

// resolveRearmBranch returns the branch a re-armed source issue's retry should
// continue (GH-6183): read from the latest re-arm comment, and only when that
// branch still exists on origin. Any miss — no comment, comment-list error, no
// PR number, branch gone — returns ok=false so the caller keeps the from-scratch
// path; a gone branch is logged as "re-arm branch missing, starting from main".
func resolveRearmBranch(ctx context.Context, lister issueCommentLister, owner, repo string, issueNum int, projectPath, taskID string) (branch string, pr int, sha string, ok bool) {
	comments, err := lister.ListIssueComments(ctx, owner, repo, issueNum)
	if err != nil {
		logging.WithComponent("github").Warn("re-arm branch: failed to list issue comments, starting from main",
			slog.String("task_id", taskID), slog.Any("error", err))
		return "", 0, "", false
	}
	bodies := make([]string, 0, len(comments))
	for _, c := range comments {
		if c != nil {
			bodies = append(bodies, c.Body)
		}
	}
	branch, pr, sha, ok = parseRearmFooterFromComments(bodies, issueNum)
	if !ok || pr <= 0 {
		return "", 0, "", false
	}
	if !executor.NewGitOperations(projectPath).RearmBranchAvailable(ctx, branch) {
		return "", 0, "", false
	}
	return branch, pr, sha, true
}
