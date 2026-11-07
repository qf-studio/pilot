package main

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// rearmCommentMarker identifies the comment rearmDeadOwnerSource
// (internal/autopilot/owner_death.go) posts; only such comments are trusted as
// a source of a continuation branch, so a stray autopilot-meta quote in a human
// comment can never steer a retry onto another branch (GH-6183).
const rearmCommentMarker = autopilot.RearmCommentMarker

// Comments that begin or re-queue a later execution. A re-arm comment is
// honoured only by the retry that directly follows it (GH-6191): once either
// appears after it, that retry has run or another path has re-armed the source
// on its own terms, and the old branch must not be revived.
const (
	taskStartedCommentMarker = "Pilot started working on this issue"
	plainRetryCommentMarker  = "was closed without merging"
)

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

// rearmSuperseded reports whether any body after a re-arm comment starts or
// re-queues a later execution (see taskStartedCommentMarker).
func rearmSuperseded(later []string) bool {
	for _, b := range later {
		if strings.Contains(b, taskStartedCommentMarker) || strings.Contains(b, plainRetryCommentMarker) {
			return true
		}
	}
	return false
}

// parseRearmFooterFromComments returns the branch/PR/sha of the latest owner-
// death re-arm comment for issueNum, oldest-to-newest bodies in. A comment only
// counts when it is a re-arm comment, its footer parses (GH-5390 validation via
// parseAutopilotBranch), its source: field names issueNum, and no later comment
// started or re-queued an execution (GH-6191).
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
		if rearmSuperseded(bodies[i+1:]) {
			return "", 0, "", false
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
	if !ok {
		return "", 0, "", false
	}
	if pr <= 0 {
		logging.WithComponent("github").Warn("re-arm footer carries no PR number, starting from main",
			slog.String("task_id", taskID), slog.String("branch", branch))
		return "", 0, "", false
	}
	if !executor.NewGitOperations(projectPath).RearmBranchAvailable(ctx, branch) {
		return "", 0, "", false
	}
	return branch, pr, sha, true
}

// continueRearmBranch is the dispatch-time wiring (GH-6183): when the body
// footer did not already resolve a branch (fromPR == 0) and the issue carries a
// retry-family label, an owner-death re-arm comment may hand the retry the dead
// fix issue's branch. Returns the inputs unchanged on any miss.
func continueRearmBranch(ctx context.Context, lister issueCommentLister, owner, repo string, issueNum int, projectPath, taskID string, liveLabels []string, branchName string, fromPR int, fixFromSHA string) (string, int, string) {
	if fromPR != 0 || owner == "" || repo == "" || !hasRetryFamilyLabel(liveLabels) {
		return branchName, fromPR, fixFromSHA
	}
	rb, rp, rs, ok := resolveRearmBranch(ctx, lister, owner, repo, issueNum, projectPath, taskID)
	if !ok {
		return branchName, fromPR, fixFromSHA
	}
	logging.WithComponent("github").Info("Reusing branch from owner-death re-arm comment",
		slog.String("task_id", taskID),
		slog.String("branch", rb),
		slog.Int("from_pr", rp),
	)
	return rb, rp, rs
}
