package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/replay"
)

// GH-6138: on_unfixed hold. In enforce mode a REQUEST-CHANGES whose blocking
// defects survive the fix round ends the run needs_human through
// holdPushedBranch instead of opening a PR that autopilot can only park
// awaiting approval (a hold with no automated exit).

// prePRHoldReasonText is the error text of a pre-PR hold. Decision: it is fixed
// and never carries the fix status — briefs.ClassifyFailure and outcomeClassifiers
// match substrings ("timeout", "conflict", "rate limit", …) and a status word
// leaking in would reclassify a needs_human hold as an infra failure and re-queue
// it. The fix status goes into the issue comment.
const prePRHoldReasonText = "pre-PR review: blocking defects not fixed"

// PrePRHoldMetaMarker names the machine-readable footer of a pre-PR hold
// comment: `<!-- pilot-hold-meta branch:<branch> sha:<sha> source:<issue> -->`.
// The dispatcher reads it on the re-arm so the retry continues the held branch
// (GH-6233). It is not rearmCommentMarker's footer: that parser requires pr > 0
// and a hold has no PR.
const PrePRHoldMetaMarker = "pilot-hold-meta"

// prePRHoldCommentMax keeps the issue comment under GitHub's 65536 character limit.
const prePRHoldCommentMax = 60000

// prePRHoldReason reports whether a finished review must hold the run: enforce
// mode, on_unfixed resolving to hold, a REQUEST-CHANGES verdict with at least one
// [blocking] defect, and a fix round that did not leave a fixed tree (nil when it
// did not apply). The reason is constant text, see prePRHoldReasonText.
func prePRHoldReason(review *PrePRReviewOutcome, fix *ReviewFixOutcome, cfg PrePRReviewConfig) (string, bool) {
	if review == nil || review.Mode != PrePRReviewModeEnforce || cfg.EffectiveOnUnfixed() != PrePRReviewOnUnfixedHold {
		return "", false
	}
	if review.Verdict != PostMergeRequestChanges || review.Review == nil || len(review.Review.BlockingDefects()) == 0 {
		return "", false
	}
	if fix != nil && fix.Status == ReviewFixFixed {
		return "", false
	}
	return prePRHoldReasonText, true
}

// onBorrowedBranch reports whether task continues an open PR's branch (an
// autopilot-fix or revision issue): a push there lands on that PR's head, and
// the PR keeps its original body, so holding would ship the unfixed defects
// without a trace.
func (r *Runner) onBorrowedBranch(task *Task) bool {
	if task.FromPR > 0 {
		return true
	}
	// An entry on the task's own pilot/<ID> branch is a sweep-protection record
	// (owner-death re-arm, GH-6198), not a borrow of another PR's head.
	branch, _, ok := r.BorrowedBranch(task.ID)
	return ok && branch != "pilot/"+task.ID
}

// prePRHoldConfig returns the effective pre_pr_review config (the zero value when unset).
func (r *Runner) prePRHoldConfig() PrePRReviewConfig {
	if r.config != nil && r.config.PrePRReview != nil {
		return *r.config.PrePRReview
	}
	return PrePRReviewConfig{}
}

// holdPrePRReview parks the run after the review: it pushes the branch and
// applies pilot-needs-human through holdPushedBranch, posts the defects, the
// evidence and the re-arm instruction on the issue, drops pilot-in-progress and
// finishes the recording. The caller returns result straight away: no intent
// judge, PR, review label, ledger row, webhook or completion alert follows.
func (r *Runner) holdPrePRReview(ctx context.Context, task *Task, git *GitOperations, result *ExecutionResult, recorder *replay.Recorder, log *slog.Logger, reason, executionPath string, evidence *AcceptanceEvidenceCollection, preserveWorktree *bool) {
	review := result.PrePRReview

	sha, shaErr := git.GetCurrentCommitSHA(ctx)
	if shaErr != nil || sha == "" {
		sha = result.CommitSHA
	} else {
		result.CommitSHA = sha
	}
	evidenceBlock := strings.TrimSpace(r.appendAcceptanceEvidence(ctx, task, git.ProjectPath(), "", result.QualityGates, evidence))

	r.holdPushedBranch(ctx, task, git, result, log, reason, executionPath, preserveWorktree)

	blocking, _ := review.defectCounts()
	fixStatus, regateStatus := PrePRFixNone, "none"
	if review.Fix != nil {
		fixStatus = string(review.Fix.Status)
		if review.Fix.RegateBudget > 0 {
			regateStatus = fmt.Sprintf("%s after %s of %s", fixStatus, review.Fix.RegateElapsed.Round(time.Second), review.Fix.RegateBudget.Round(time.Second))
		}
	}

	// The comment and label calls run on a fresh window like holdPushedBranch's:
	// ctx is the finalize window and may be nearly spent after the push.
	holdCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if task.SourceAdapter == "" || task.SourceAdapter == "github" {
		if issueNum := task.GHIssueRef(); issueNum != "" {
			body := renderPrePRHoldComment(task, review, result, sha, evidenceBlock)
			if err := ghIssueComment(holdCtx, task.ProjectPath, issueNum, body); err != nil {
				log.Warn("Pre-PR review hold: failed to post the defects comment",
					slog.String("task_id", task.ID), slog.Any("error", err))
			}
			if err := ghEditLabels(holdCtx, task.ProjectPath, issueNum, nil, []string{"pilot-in-progress"}); err != nil {
				log.Warn("Pre-PR review hold: failed to remove pilot-in-progress",
					slog.String("task_id", task.ID), slog.Any("error", err))
			}
		}
	}

	RecordPrePRHold(task.ProjectPath)
	log.Info("Pre-PR review hold",
		slog.String("task_id", task.ID),
		slog.String("branch", task.Branch),
		slog.String("sha", sha),
		slog.Int("blocking", blocking),
		slog.String("fix_status", fixStatus),
		slog.String("regate_status", regateStatus),
	)

	if recorder != nil {
		recorder.SetCommitSHA(result.CommitSHA)
		if finErr := recorder.Finish("needs_human"); finErr != nil {
			log.Warn("Failed to finish recording", slog.Any("error", finErr))
		}
	}
}

// renderPrePRHoldComment builds the issue comment of a pre-PR hold: what was
// found, what happened to the fix, the evidence the PR body would have carried,
// where the work is and how to re-arm.
func renderPrePRHoldComment(task *Task, review *PrePRReviewOutcome, result *ExecutionResult, sha, evidenceBlock string) string {
	var sb strings.Builder
	sb.WriteString("Pilot held this task before opening a PR: the pre-PR review found blocking defects that the fix round did not resolve, so no PR was opened.\n")
	sb.WriteString(RenderPrePRReviewSection(review))
	sb.WriteString("\n")
	if note := prePRHoldFixNote(review.Fix); note != "" {
		sb.WriteString("\n" + note + "\n")
	}
	if evidenceBlock != "" {
		sb.WriteString("\n" + evidenceBlock + "\n")
	}

	sb.WriteString("\n### Where the work is\n\n")
	switch {
	case result.BranchPushed != nil && !*result.BranchPushed:
		fmt.Fprintf(&sb, "Branch `%s` could not be pushed; see the note above.", task.Branch)
	default:
		fmt.Fprintf(&sb, "Branch `%s` is pushed", task.Branch)
	}
	if sha != "" {
		fmt.Fprintf(&sb, " (HEAD `%s`)", sha)
	}
	sb.WriteString(".\n\nTo re-run: edit the issue or the branch as needed, then remove the `pilot-needs-human` label; Pilot picks the task up again.\n")
	footer := prePRHoldMetaFooter(task, result, sha)
	// The footer is the last line, so it is appended after the cut: truncating
	// the body must never drop it.
	return cutAtBoundary(sb.String(), prePRHoldCommentMax-len(footer)-1) + footer
}

// prePRHoldMetaFooter renders the hold-meta footer line (with its leading
// newline), or "" when there is no pushed branch for a retry to continue.
func prePRHoldMetaFooter(task *Task, result *ExecutionResult, sha string) string {
	issue := task.GHIssueRef()
	if sha == "" || task.Branch == "" || issue == "" || result.BranchPushed == nil || !*result.BranchPushed {
		return ""
	}
	return PrePRHoldMetaFooter(task.Branch, sha, issue)
}

// PrePRHoldMetaFooter renders the footer line, with its leading and trailing
// newline. Exported so the dispatcher's tests can round-trip the producer's
// format (GH-6233).
func PrePRHoldMetaFooter(branch, sha, issue string) string {
	return fmt.Sprintf("\n<!-- %s branch:%s sha:%s source:%s -->\n", PrePRHoldMetaMarker, branch, sha, issue)
}

// prePRHoldFixNote says in plain words what became of the fix round.
func prePRHoldFixNote(fix *ReviewFixOutcome) string {
	if fix == nil {
		return "No fix round ran for these defects."
	}
	regate := ""
	if fix.RegateBudget > 0 {
		regate = fmt.Sprintf(" (re-gate ran %s of a %s budget)", fix.RegateElapsed.Round(time.Second), fix.RegateBudget.Round(time.Second))
	}
	switch fix.Status {
	case ReviewFixRegateTimeout:
		return "The fix was reverted because its re-gate ran out of time" + regate + "; it is not verified, so it may or may not be correct."
	case ReviewFixReverted:
		return "The fix failed the quality gates on re-check" + regate + " and was reverted."
	case ReviewFixRegateError:
		return "The fix could not be re-checked" + regate + " and was reverted."
	case ReviewFixFailed:
		return "The fix session failed; its edits were discarded."
	case ReviewFixNoChange:
		return "The fix session made no change."
	case ReviewFixSkippedCtx, ReviewFixSkippedBudget:
		return "The fix round was skipped: not enough task time was left."
	}
	return "Fix round: " + string(fix.Status) + "."
}

// prePRFixOf returns the fix round recorded on o, nil-safe.
func prePRFixOf(o *PrePRReviewOutcome) *ReviewFixOutcome {
	if o == nil {
		return nil
	}
	return o.Fix
}
