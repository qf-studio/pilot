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

// holdMetaFooterLineRe matches the pre-PR hold footer only when it is the whole
// of a line, like autopilotMetaFooterLineRe.
var holdMetaFooterLineRe = regexp.MustCompile(`^<!-- ` + regexp.QuoteMeta(executor.PrePRHoldMetaMarker) + `(.*)-->$`)

// extractHoldMetaFooter returns the hold-meta footer of body: the last non-empty
// line outside code spans, the placement renderPrePRHoldComment writes. "" otherwise.
func extractHoldMetaFooter(body string) string {
	stripped := fencedCodeBlockRe.ReplaceAllString(body, "")
	stripped = inlineCodeSpanRe.ReplaceAllString(stripped, "")
	lines := strings.Split(stripped, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if holdMetaFooterLineRe.MatchString(line) {
			return line
		}
		return ""
	}
	return ""
}

// parseHoldMetaFromComments returns the branch and sha of the latest pre-PR
// hold comment for issueNum, oldest-to-newest bodies in (GH-6233). A comment
// counts when its footer parses, its source: field names issueNum, the branch is
// that issue's own pilot/GH-<n> branch and the sha is plausible; a later
// task-started comment cancels it (rearmSuperseded), since that run has already
// built on the branch or another path re-armed the issue.
func parseHoldMetaFromComments(bodies []string, issueNum int) (branch, sha string, ok bool) {
	for i := len(bodies) - 1; i >= 0; i-- {
		footer := extractHoldMetaFooter(bodies[i])
		if footer == "" {
			continue
		}
		if m := autopilotSourceFieldRe.FindStringSubmatch(footer); len(m) < 2 || m[1] != strconv.Itoa(issueNum) {
			continue
		}
		b := autopilotBranchFieldRe.FindStringSubmatch(footer)
		s := autopilotSHAFieldRe.FindStringSubmatch(footer)
		if len(b) < 2 || len(s) < 2 || b[1] != "pilot/GH-"+strconv.Itoa(issueNum) || !isValidAutopilotBranch(b[1]) || !isPlausibleGitSHA(s[1]) {
			continue
		}
		if rearmSuperseded(bodies[i+1:]) {
			return "", "", false
		}
		return b[1], s[1], true
	}
	return "", "", false
}

// continueHeldBranch is the dispatch-time wiring for a retry after a pre-PR hold
// (GH-6233): when no footer already chose a branch (branchName is still the
// task's own pilot/<id>, fromPR == 0), the latest hold-meta comment hands the
// retry the pushed branch and its sha, provided the branch is still on origin.
// It never sets a PR number, so the run is not treated as borrowing a PR's
// branch and can hold again. Any miss — no hold comment, comment-list error,
// superseded, branch gone — returns the inputs and "" with one Info line naming
// the reason, and the task starts fresh from the default branch.
func continueHeldBranch(ctx context.Context, lister issueCommentLister, owner, repo string, issueNum int, projectPath, taskID, branchName string, fromPR int) (string, string) {
	if fromPR != 0 || owner == "" || repo == "" || branchName != "pilot/"+taskID {
		return branchName, ""
	}
	log := logging.WithComponent("github")
	comments, err := lister.ListIssueComments(ctx, owner, repo, issueNum)
	if err != nil {
		log.Info("pre-PR hold continuation: comment list failed, starting from the default branch",
			slog.String("task_id", taskID), slog.Any("error", err))
		return branchName, ""
	}
	bodies := make([]string, 0, len(comments))
	for _, c := range comments {
		if c != nil {
			bodies = append(bodies, c.Body)
		}
	}
	branch, sha, ok := parseHoldMetaFromComments(bodies, issueNum)
	if !ok {
		reason := "no hold comment on the issue"
		if hasHoldMetaMarker(bodies) {
			reason = "hold comment superseded or invalid"
		}
		log.Info("pre-PR hold continuation: "+reason+", starting from the default branch",
			slog.String("task_id", taskID))
		return branchName, ""
	}
	if !executor.NewGitOperations(projectPath).RearmBranchAvailable(ctx, branch) {
		log.Info("pre-PR hold continuation: held branch gone from origin, starting from the default branch",
			slog.String("task_id", taskID), slog.String("branch", branch))
		return branchName, ""
	}
	log.Info("Continuing the branch a pre-PR hold pushed",
		slog.String("task_id", taskID), slog.String("branch", branch), slog.String("sha", sha))
	return branch, sha
}

// hasHoldMetaMarker reports whether any body carries a hold-meta footer line.
func hasHoldMetaMarker(bodies []string) bool {
	for _, b := range bodies {
		if extractHoldMetaFooter(b) != "" {
			return true
		}
	}
	return false
}
