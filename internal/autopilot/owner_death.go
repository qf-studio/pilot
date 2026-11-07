package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// ownerHealth classifies whether a designated recovery owner (a spawned fix
// issue holding TerminalLabel for its source) is still able to do its job.
// GH-4842: a designated owner that dies — declined at preflight, or closed
// without ever shipping a merged PR — leaves its source issue permanently
// stranded (pilot-failed, zero live owners) unless something re-arms or
// escalates it.
type ownerHealth int

const (
	// ownerAlive: still open, may yet ship.
	ownerAlive ownerHealth = iota
	// ownerShipped: closed with pilot-done — did its job, not a death.
	ownerShipped
	// ownerDead: closed without shipping — never merged a fixing PR.
	ownerDead
)

// classifyOwnerHealth inspects a (possibly closed) fix issue and reports
// whether its closure represents a completed hand-off (ownerShipped), no
// closure at all (ownerAlive), or an owner-death event (ownerDead) that
// must trigger source re-arm/escalation.
//
// GH-4856: a fix issue declined at preflight stays OPEN carrying
// pilot-needs-clarification (GH-2768) and never dispatches — it reads as
// ownerAlive by the closed-only check below even though it will never ship.
// ReactToDeclinedFixIssue already reacts to this exact event and re-arms the
// source, but any caller that re-checks health afterward (the dedup path
// here, or notifyExternalClose's durable-claim fallback) must agree it's
// dead too, or it re-designates the already-declined zombie as the recovery
// owner — the TASK-468 D1 shape where the reaction's re-arm is immediately
// clobbered by a fallback that still thinks the zombie is alive.
func classifyOwnerHealth(issue *github.Issue) ownerHealth {
	if issue == nil {
		return ownerAlive
	}
	if issue.State != github.StateClosed {
		if github.HasLabel(issue, github.LabelNeedsClarification) {
			return ownerDead
		}
		return ownerAlive
	}
	if github.HasLabel(issue, github.LabelDone) {
		return ownerShipped
	}
	return ownerDead
}

// fixIssueSourceRe extracts the source issue number embedded in the
// autopilot-meta comment ("source:123") by FeedbackLoop.generateBody/
// CreateReviewIssue. GH-5336: the source number used to be carried via a
// standalone "Depends on: #123" line, but the SDK poller's
// hasPendingDependencies treats any such line as an unmet-dependency gate —
// and the original issue stayed open (pilot-superseded) after hand-off, so
// that line permanently blocked the fix issue itself from ever
// dispatching. The source number now lives inside autopilot-meta instead,
// which the poller does not parse as a dependency marker. Since GH-5741 the
// pre-merge fix issue body carries "Closes #<source>", so the fix PR's merge
// closes the original on GitHub (GH-5747: it is labelled pilot-superseded
// after the fact by verifyFixPRDeliversSourceScope).
var fixIssueSourceRe = regexp.MustCompile(`<!-- autopilot-meta.*?source:(\d+).*?-->`)

// fixIssueSourceLegacyRe is the pre-GH-5336 encoding: a standalone
// "Depends on: #123" line, anchored to line start so it can't match
// unrelated inline "Depends on" prose, with the autopilot-meta marker
// required elsewhere in the body (checked by autopilotMetaMarkerRe). Fix
// issues created before this fix shipped still use this shape — kept as a
// fallback so in-flight owner-death tracking for those issues doesn't
// regress.
var fixIssueSourceLegacyRe = regexp.MustCompile(`(?m)^Depends on: #(\d+)\s*$`)

// autopilotMetaMarkerRe requires the autopilot-meta marker to be present
// before trusting a legacy "Depends on" line as a Pilot-authored source
// reference — distinguishes autopilot-spawned fix issues from unrelated uses
// of the same phrase (e.g. epic decomposition in internal/executor/epic.go).
var autopilotMetaMarkerRe = regexp.MustCompile(`<!--\s*autopilot-meta\b`)

// parseFixIssueSource recovers the source issue number from a spawned fix
// issue's body, without any new persistence (GH-4842 explicitly scopes
// designation persistence out — this piggybacks on the existing body
// convention instead). Tries the current source:N-in-meta encoding first,
// then falls back to the legacy "Depends on: #N" line (GH-5336) so fix
// issues spawned before that fix shipped keep working.
func parseFixIssueSource(body string) (int, bool) {
	if m := fixIssueSourceRe.FindStringSubmatch(body); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return n, true
		}
	}
	if !autopilotMetaMarkerRe.MatchString(body) {
		return 0, false
	}
	m := fixIssueSourceLegacyRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// fixIssuePRRe extracts the originating PR number embedded by
// FeedbackLoop.generateBody/CreateReviewIssue's autopilot-meta comment
// ("pr:123"), mirroring controller.go's iterationRe for the same comment.
var fixIssuePRRe = regexp.MustCompile(`<!-- autopilot-meta.*?pr:(\d+).*?-->`)

// parseFixIssuePR recovers the originating PR number from a spawned fix
// issue's body. Companion to parseFixIssueSource: the source issue is named
// via "source:N" (or, pre-GH-5336, a standalone "Depends on: #N" line), the
// PR via "pr:N" — both in the same autopilot-meta comment.
// Used by reactToDeadFixIssue (GH-4852) to re-check the durable spawned-fix
// claim as an alternate designation source when the pilot-failed label
// hasn't landed yet.
func parseFixIssuePR(body string) (int, bool) {
	m := fixIssuePRRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// fixIssueFailureTypeRe extracts the "**Failure Type**: <type>" line both
// FeedbackLoop.generateBody (CreateFailureIssue) and CreateReviewIssue write
// into the "## Context" section, near the top of the body, before any
// attacker-controllable content (CI logs, review comments) — so the first
// match is always the genuine one. CreateReviewIssue always writes
// FailureReviewRequested; CreateFailureIssue writes one of the ci_*/
// merge_conflict/deployment FailureType values.
var fixIssueFailureTypeRe = regexp.MustCompile(`(?m)^-\s+\*\*Failure Type\*\*:\s+(\S+)\s*$`)

// isReviewRevisionFixIssue reports whether a spawned fix issue's body
// identifies it as a review-revision issue (CreateReviewIssue), as opposed
// to a CI-fix continuation (CreateFailureIssue). GH-5362:
// verifyFixPRDeliversSourceScope uses this to decide which of its two
// zero-overlap disciplines applies to a given source issue — see the
// function's doc comment.
func isReviewRevisionFixIssue(body string) bool {
	m := fixIssueFailureTypeRe.FindStringSubmatch(body)
	return len(m) >= 2 && m[1] == string(FailureReviewRequested)
}

// isCIPostMergeFixIssue reports whether a spawned fix issue's body identifies
// it as a post-merge CI fix (FailureCIPostMerge): the original PR already
// merged, so the fix PR touches CI config or the workflow rather than the
// original's files. GH-6123: the GH-5351 delivery-mismatch guard skips these.
func isCIPostMergeFixIssue(body string) bool {
	m := fixIssueFailureTypeRe.FindStringSubmatch(body)
	return len(m) >= 2 && m[1] == string(FailureCIPostMerge)
}

// verifyFixPRDeliversSourceScope gates whether a merged fix/revision PR is
// allowed to mark its source issue pilot-superseded at all — for BOTH the
// CI-fix continuation path (GH-5351, spawnFailureIssue) and the
// review-revision path (GH-5362, spawnReviewIssue). Neither spawner applies
// pilot-superseded eagerly at spawn time anymore: spawnFailureIssue used to
// apply it the instant the continuation fix ISSUE was created, and
// spawnReviewIssue used to apply it the instant the revision issue was
// created — both before that issue's own PR existed, let alone before its
// diff was known (the pilot-console #275 false-success shape). This runs
// once the fix/revision PR actually merges, the one point its diff is fully
// known, and compares its changed files against the origin PR it was
// spawned to replace (source:N / pr:N, recorded in the fix issue body by
// FeedbackLoop.generateBody/CreateReviewIssue). isReviewRevisionFixIssue
// tells the two paths apart (CreateFailureIssue vs CreateReviewIssue leave a
// different Failure Type marker in the fix issue body) because they differ
// in what a confirmed zero-overlap mismatch does:
//
//   - CI-fix (isReviewRevisionFixIssue == false): GH-5351 gate — leave the
//     source issue open with a delivery-mismatch comment. No needs-human
//     escalation; the source was never superseded, so there is nothing to
//     revert or protect it from beyond leaving it alone to retry normally.
//   - Review-revision (isReviewRevisionFixIssue == true): GH-5362 gate —
//     escalate the source issue to pilot-needs-human in addition to the
//     comment, mirroring the pre-GH-5351 CI-fix behavior this replaced.
//
// Confirmed overlap applies pilot-superseded for the first time on either
// path. Both paths are idempotent against a source that already carries
// pilot-superseded (e.g. a daemon restart mid-gate). A source already closed
// by the fix PR's "Closes #<source>" (GH-5741) but not yet labelled falls
// through to the same overlap check, so a confirmed overlap still strips its
// trigger labels and adds pilot-superseded (GH-5747).
//
// A no-op (fails open, logs a warning) whenever the merged PR isn't a
// Pilot-spawned CI-fix/revision continuation, or any GitHub read fails —
// this is a detection backstop, not a gate that should ever block or delay
// a merge that already landed.
func (c *Controller) verifyFixPRDeliversSourceScope(ctx context.Context, prState *PRState) {
	if prState.IssueNumber <= 0 {
		return
	}
	fixIssue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, prState.IssueNumber)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to fetch fix issue, skipping (fail-open)",
			"issue", prState.IssueNumber, "error", err)
		return
	}
	sourceNum, ok := parseFixIssueSource(fixIssue.Body)
	if !ok {
		// Not a Pilot-spawned CI-fix/revision issue — nothing to gate.
		return
	}
	isReviewRevision := isReviewRevisionFixIssue(fixIssue.Body)

	originPR, originFiles, found, err := c.originScope(ctx, prState.IssueNumber, fixIssue.Body)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to resolve origin scope, skipping (fail-open)",
			"issue", prState.IssueNumber, "source", sourceNum, "error", err)
		return
	}
	if !found {
		c.log.Warn("verifyFixPRDeliversSourceScope: fix issue names a source but no origin PR could be resolved, skipping",
			"issue", prState.IssueNumber, "source", sourceNum)
		return
	}
	// GH-4284-style caution: an origin PR with no recorded/fetchable files
	// (deleted commit history unreadable, or a genuinely empty diff) can't
	// prove overlap or non-overlap either way — skip rather than gate on a
	// false signal.
	if len(originFiles) == 0 {
		c.log.Warn("verifyFixPRDeliversSourceScope: origin PR has no recorded/fetchable files, skipping (fail-open)",
			"source", sourceNum, "origin_pr", originPR)
		return
	}

	fixFiles, err := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to list fix PR files, skipping (fail-open)",
			"pr", prState.PRNumber, "error", err)
		return
	}
	overlap := filesOverlap(fixFiles, originFiles)

	source, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, sourceNum)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to fetch source issue, skipping",
			"source", sourceNum, "error", err)
		return
	}
	if github.HasLabel(source, github.LabelSuperseded) {
		// Already gated by an earlier run of this function (e.g. a daemon
		// restart mid-gate) — idempotent, nothing more to do.
		return
	}
	// GH-5747: a closed source without pilot-superseded is the normal
	// post-GH-5741 shape — the fix PR's "Closes #<source>" closed it on merge
	// — so it must fall through to the label mutation below rather than
	// return, or it ends closed with pilot/pilot-in-progress standing.

	if overlap {
		// Confirmed overlap: the fix/revision PR's diff genuinely continues
		// the origin PR's work — safe to mark the source issue superseded
		// now that its scope is confirmed delivered.
		// GH-5375: both kinds must mirror the label set notifyExternalClose's
		// supersededClose branch applies (controller.go ~9860-9868) — pilot
		// and pilot-in-progress stripped in the same mutation that adds
		// pilot-superseded. Before this fix the review-revision kind only
		// added pilot-superseded, leaving pilot/pilot-in-progress standing on
		// an OPEN source issue: the poller skips it (in-progress) so there's
		// no double-dispatch, but the stale-label cleanup sweep and any
		// operator reading labels see a live task that will never move
		// (GH-5298 invariant: pilot-superseded and pilot must never coexist).
		c.mutateIssueLabels(ctx, sourceNum,
			[]string{github.LabelSuperseded},
			[]string{github.LabelPilot, github.LabelInProgress, labelNeedsManualRebase},
		)
		c.log.Info("verifyFixPRDeliversSourceScope: fix/revision PR confirmed to deliver origin scope — source issue marked superseded",
			"source", sourceNum, "fix_issue", prState.IssueNumber, "fix_pr", prState.PRNumber, "origin_pr", originPR, "review_revision", isReviewRevision)
		c.cleanupOriginScope(prState.IssueNumber)
		return
	}

	if isReviewRevision {
		reasonMsg := fmt.Sprintf(
			"its designated revision PR #%d merged but shares no changed files with the original PR #%d it was spawned to continue",
			prState.PRNumber, originPR,
		)
		if err := c.labeler.AddLabels(ctx, c.owner, c.repo, sourceNum, []string{labelNeedsHuman}); err != nil {
			c.log.Warn("verifyFixPRDeliversSourceScope: failed to add needs-human label", "issue", sourceNum, "error", err)
		}
		comment := fmt.Sprintf(
			"\U0001F6A8 **Delivery mismatch detected (GH-5362)**: %s — the original changes may have been silently dropped. Leaving this issue open under `%s` instead of marking it `%s`.",
			reasonMsg, labelNeedsHuman, github.LabelSuperseded,
		)
		if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, sourceNum, comment); err != nil {
			c.log.Warn("verifyFixPRDeliversSourceScope: failed to post comment", "issue", sourceNum, "error", err)
		}
		c.fireOwnerDeathAlert(sourceNum, reasonMsg, "escalated")
		c.log.Warn("verifyFixPRDeliversSourceScope: revision PR shares no files with origin PR — escalated source issue",
			"source", sourceNum, "fix_issue", prState.IssueNumber, "fix_pr", prState.PRNumber, "origin_pr", originPR)
		c.cleanupOriginScope(prState.IssueNumber)
		return
	}

	// GH-6123: a disjoint fix PR cannot mean dropped changes when the original
	// already merged (its delivery is proven) or when the fix issue is a
	// post-merge CI fix (it touches CI config, never the original's files).
	// The source is not marked superseded: overlap was not proven.
	skipReason := ""
	if isCIPostMergeFixIssue(fixIssue.Body) {
		skipReason = "ci_post_merge"
	} else if originPRObj, perr := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, originPR); perr != nil {
		// Fail toward the guard: never block the merge path on a read error.
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to fetch origin PR, keeping delivery-mismatch guard",
			"origin_pr", originPR, "error", perr)
	} else if originPRObj != nil && originPRObj.Merged {
		skipReason = "origin_merged"
	}
	if skipReason != "" {
		c.log.Info("verifyFixPRDeliversSourceScope: fix PR shares no files with origin PR but delivery-mismatch guard does not apply — skipping",
			"reason", skipReason, "source", sourceNum, "fix_issue", prState.IssueNumber, "fix_pr", prState.PRNumber, "origin_pr", originPR)
		c.cleanupOriginScope(prState.IssueNumber)
		return
	}

	reasonMsg := fmt.Sprintf(
		"its designated fix PR #%d merged but shares no changed files with the original PR #%d it was spawned to continue",
		prState.PRNumber, originPR,
	)
	comment := fmt.Sprintf(
		"\U0001F6A8 **Delivery mismatch detected (GH-5351)**: %s — the original changes may have been silently dropped. Leaving this issue open instead of marking it `%s`.",
		reasonMsg, github.LabelSuperseded,
	)
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, sourceNum, comment); err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to post delivery-mismatch comment", "issue", sourceNum, "error", err)
	}
	c.fireOwnerDeathAlert(sourceNum, reasonMsg, "mismatch_no_supersede")
	c.log.Warn("verifyFixPRDeliversSourceScope: fix PR shares no files with origin PR — not superseding source issue",
		"source", sourceNum, "fix_issue", prState.IssueNumber, "fix_pr", prState.PRNumber, "origin_pr", originPR)
	c.cleanupOriginScope(prState.IssueNumber)
}

// cleanupOriginScope deletes the recorded origin-scope row for fixIssueNumber
// once verifyFixPRDeliversSourceScope has reached a final gate decision
// (mismatch or confirmed match) — best-effort, since leaving a stale row
// behind only costs a few bytes and a harmless re-read, never correctness.
func (c *Controller) cleanupOriginScope(fixIssueNumber int) {
	if c.stateStore == nil {
		return
	}
	if err := c.stateStore.DeleteOriginScope(c.repoKey(), fixIssueNumber); err != nil {
		c.log.Warn("cleanupOriginScope: failed to delete origin-scope record", "fix_issue", fixIssueNumber, "error", err)
	}
}

// originScope resolves the origin PR number and changed-file set for a
// spawned fix issue (GH-5351), preferring the record spawnFailureIssue wrote
// at spawn time (StateStore.RecordOriginScope) over a live re-fetch — the
// origin PR is normally closed and untracked within one poll tick of the
// spawn (removePRTracking), well before a fix PR eventually merges and this
// runs, so a live re-fetch of the origin PR's files is not always possible.
// Falls back to a live ListPullRequestFiles call, keyed off the fix issue
// body's "pr:N" marker, for fix issues spawned before this migration or a
// transient failure during the spawn-time recording. found=false means
// neither source could resolve an origin PR number at all.
func (c *Controller) originScope(ctx context.Context, fixIssueNumber int, fixIssueBody string) (originPR int, files []*github.PRFile, found bool, err error) {
	if c.stateStore != nil {
		recordedPR, recordedFiles, ok, serr := c.stateStore.GetOriginScope(c.repoKey(), fixIssueNumber)
		if serr != nil {
			c.log.Warn("originScope: GetOriginScope failed, falling back to live fetch", "fix_issue", fixIssueNumber, "error", serr)
		} else if ok {
			return recordedPR, stringsToPRFiles(recordedFiles), true, nil
		}
	}

	originPR, ok := parseFixIssuePR(fixIssueBody)
	if !ok {
		return 0, nil, false, nil
	}
	liveFiles, ferr := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, originPR)
	if ferr != nil {
		return 0, nil, false, ferr
	}
	return originPR, liveFiles, true, nil
}

// stringsToPRFiles wraps recorded filenames as PRFile stand-ins so
// filesOverlap can be reused unchanged against a persisted origin-scope
// record instead of a live GitHub PRFile list.
func stringsToPRFiles(names []string) []*github.PRFile {
	files := make([]*github.PRFile, len(names))
	for i, n := range names {
		files[i] = &github.PRFile{Filename: n}
	}
	return files
}

// filesOverlap reports whether any filename appears in both PR file lists.
func filesOverlap(a, b []*github.PRFile) bool {
	seen := make(map[string]struct{}, len(a))
	for _, f := range a {
		if f != nil {
			seen[f.Filename] = struct{}{}
		}
	}
	for _, f := range b {
		if f == nil {
			continue
		}
		if _, ok := seen[f.Filename]; ok {
			return true
		}
	}
	return false
}

// emitOwnerDeathAlert fires an alerts.Event for an owner-death reaction
// (rearm/escalate/replace). Shared by Controller and FeedbackLoop, which
// each hold their own optional alertSink. Logs (rather than drops silently)
// when no sink is wired, since owner-death is exactly the kind of event that
// must not go unnoticed per the issue's acceptance criteria.
func emitOwnerDeathAlert(engine alertSink, log *slog.Logger, repoKey string, issueNum int, reasonMsg, outcome string) {
	if engine == nil {
		log.Error("owner-death: alert not delivered, alerts engine not wired", "issue", issueNum, "reason", reasonMsg, "outcome", outcome)
		return
	}
	engine.ProcessEvent(alerts.Event{
		Type:      alerts.EventTypeTaskFailed,
		TaskID:    fmt.Sprintf("owner-death-%d", issueNum),
		TaskTitle: fmt.Sprintf("Issue #%d: designated fix issue died", issueNum),
		Project:   repoKey,
		Error:     reasonMsg,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":    repoKey,
			"issue":   strconv.Itoa(issueNum),
			"outcome": outcome,
		},
	})
}

// fireOwnerDeathAlert emits an owner-death alert for the dedup-path reaction
// in CreateFailureIssue (a dead designated fix issue discovered and replaced
// while re-checking the spawned-fix claim).
func (f *FeedbackLoop) fireOwnerDeathAlert(issueNum int, reasonMsg, outcome string) {
	emitOwnerDeathAlert(f.alertsEngine, f.log, f.owner+"/"+f.repo, issueNum, reasonMsg, outcome)
}

// ReactToDeclinedFixIssue handles the preflight-decline owner-death path
// (GH-4842 implementation step 1a). It is invoked synchronously from
// storeExecutionSaver.SaveDeclinedExecution whenever the SDK poller's
// pre-flight judge rejects a task before dispatch — the SDK already calls
// this exactly once per decline, so no new poller/goroutine is needed to
// observe it.
func (c *Controller) ReactToDeclinedFixIssue(ctx context.Context, issueNumber int, reason string) {
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, issueNumber)
	if err != nil {
		c.log.Warn("owner-death: failed to fetch declined issue", "issue", issueNumber, "error", err)
		return
	}
	detail := fmt.Sprintf("declined at preflight: %s", reason)
	c.reactToDeadFixIssue(ctx, issue, detail)
}

// reactToDeadFixIssue is the shared reaction for both owner-death paths
// (preflight-declined and closed-unmerged): trace the dead fix issue back
// to its source via the body convention, and either re-arm the source for
// retry or escalate to needs-human if retries are already exhausted.
func (c *Controller) reactToDeadFixIssue(ctx context.Context, deadIssue *github.Issue, detail string) {
	// GH-5937: the issue is dead whether or not it traces back to a source.
	c.forgetBorrowedBranchForIssue(deadIssue.Number)
	// GH-6183: read the dead issue's branch/pr/sha before it is lost, so a
	// re-armed source continues that branch.
	carry, hasCarry := parseRearmCarry(deadIssue.Body)
	sourceNum, ok := parseFixIssueSource(deadIssue.Body)
	if !ok {
		// Not a Pilot-spawned fix issue (or body doesn't carry the
		// autopilot-meta marker) — nothing to react to.
		return
	}
	source, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, sourceNum)
	if err != nil {
		c.log.Warn("owner-death: failed to fetch source issue", "fix_issue", deadIssue.Number, "source", sourceNum, "error", err)
		return
	}
	if source.State == github.StateClosed {
		c.log.Info("owner-death: source issue already closed, nothing to re-arm", "fix_issue", deadIssue.Number, "source", sourceNum)
		return
	}
	designated := github.HasLabel(source, github.LabelFailed)
	if !designated && c.stateStore != nil {
		// GH-4852: the pilot-failed label is written by notifyExternalClose,
		// which only runs once the external-close poll tick observes the PR
		// closed. The SDK poller's preflight-decline hook (ReactToDeclinedFixIssue)
		// can fire on a freshly-spawned fix issue BEFORE that tick lands the
		// label (TASK-468 D1 ordering race: restart in the close→persist
		// window). Without this fallback the label-only check below would
		// skip — consuming this one-shot reaction — while the durable
		// spawned-fix claim already designates deadIssue as this source's
		// recovery owner; PR#4846's controller.go fallback then re-designates
		// the already-declined owner from that still-live claim, permanently
		// stranding the source. Re-check the claim (recorded synchronously
		// before either handler closes the PR) as an alternate designation
		// source before giving up.
		if prNum, ok := parseFixIssuePR(deadIssue.Body); ok {
			if claimedIssue, cerr := c.stateStore.HasSpawnedFixForPR(c.repoKey(), prNum); cerr != nil {
				c.log.Warn("owner-death: durable claim lookup failed while checking designation",
					"fix_issue", deadIssue.Number, "source", sourceNum, "error", cerr)
			} else {
				designated = claimedIssue == deadIssue.Number
			}
		}
	}
	if !designated {
		// Source isn't currently designated to this fix issue (already
		// re-armed by something else, or was never in the TerminalLabel
		// state to begin with) — avoid double-processing.
		c.log.Info("owner-death: source issue not currently designated to this fix issue, skipping", "fix_issue", deadIssue.Number, "source", sourceNum)
		return
	}
	reasonMsg := fmt.Sprintf("its designated fix issue #%d died (%s)", deadIssue.Number, detail)
	if github.HasLabel(source, github.LabelRetryExhausted) {
		c.escalateDeadOwnerSource(ctx, source, reasonMsg)
		return
	}
	var carryPtr *rearmCarry
	if hasCarry {
		carryPtr = &carry
	}
	c.rearmDeadOwnerSource(ctx, source, reasonMsg, carryPtr)
}

// rearmCarry is the dead fix issue's branch/PR/sha, read from its
// autopilot-meta footer, that a re-armed source's retry continues instead of
// restarting from the default branch (GH-6183).
type rearmCarry struct {
	branch    string
	pr        int
	sha       string
	iteration int // the dead fix issue's own iteration; the re-arm footer carries +1
}

var (
	// fixMetaFooterLineRe matches the autopilot-meta comment only when it is the
	// entire content of a (trimmed) line; mirrors cmd/pilot's
	// autopilotMetaFooterLineRe.
	fixMetaFooterLineRe = regexp.MustCompile(`^<!--\s*autopilot-meta\b(.*)-->$`)
	fixMetaFenceRe      = regexp.MustCompile("(?s)```.*?```")
	fixMetaCodeSpanRe   = regexp.MustCompile("`[^`\n]*`")
	fixMetaPRRe         = regexp.MustCompile(`\bpr:(\d+)`)
	fixMetaBranchRe     = regexp.MustCompile(`\bbranch:(\S+)`)
	fixMetaSHARe        = regexp.MustCompile(`\bsha:([0-9a-fA-F]{7,40})\b`)
	fixMetaIterationRe  = regexp.MustCompile(`\biteration:(\d+)`)
	// fixMetaBranchOKRe mirrors cmd/pilot's isValidAutopilotBranch: only a
	// ref-safe pilot/ branch is ever handed on to a dispatcher.
	fixMetaBranchOKRe = regexp.MustCompile(`^pilot/[A-Za-z0-9._/-]+$`)
)

// fixMetaFooter returns the field text of the autopilot-meta footer, but only
// when it is the last non-empty line of body outside any fenced block or inline
// code span — the placement the dispatcher trusts (GH-5390). A fix issue's body
// embeds attacker-controllable content (CI logs, review comments); a footer
// quoted there must never be carried onto the source's retry.
func fixMetaFooter(body string) (string, bool) {
	stripped := fixMetaFenceRe.ReplaceAllString(body, "")
	stripped = fixMetaCodeSpanRe.ReplaceAllString(stripped, "")
	lines := strings.Split(stripped, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		m := fixMetaFooterLineRe.FindStringSubmatch(line)
		if len(m) < 2 {
			return "", false
		}
		return m[1], true
	}
	return "", false
}

// parseRearmCarry reads branch, pr, sha and iteration from a dead fix issue's
// autopilot-meta footer. ok is false when the footer carries no usable branch,
// in which case the re-arm degrades to the pre-GH-6183 reason-only comment.
func parseRearmCarry(body string) (rearmCarry, bool) {
	meta, found := fixMetaFooter(body)
	if !found {
		return rearmCarry{}, false
	}
	bm := fixMetaBranchRe.FindStringSubmatch(meta)
	if len(bm) < 2 || !fixMetaBranchOKRe.MatchString(bm[1]) ||
		strings.Contains(bm[1], "..") || strings.HasSuffix(bm[1], "/") || strings.HasSuffix(bm[1], ".lock") {
		return rearmCarry{}, false
	}
	carry := rearmCarry{branch: bm[1]}
	if pm := fixMetaPRRe.FindStringSubmatch(meta); len(pm) >= 2 {
		carry.pr, _ = strconv.Atoi(pm[1])
	}
	if sm := fixMetaSHARe.FindStringSubmatch(meta); len(sm) >= 2 {
		carry.sha = sm[1]
	}
	if im := fixMetaIterationRe.FindStringSubmatch(meta); len(im) >= 2 {
		carry.iteration, _ = strconv.Atoi(im[1])
	}
	return carry, true
}

// RearmCommentMarker is the bold heading the owner-death re-arm comment carries.
// cmd/pilot trusts only comments containing it as a source of a continuation
// branch (GH-6183), so producer and consumer share this one constant.
const RearmCommentMarker = "Owner-death recovery"

// RenderRearmCommentFooter is rearmCommentFooter for the dispatcher-side
// consumer's tests, so they exercise the producer's real format instead of a
// hand-written copy (GH-6191). iteration is the dead fix issue's own.
func RenderRearmCommentFooter(branch string, pr int, sha string, iteration, source int) string {
	return rearmCommentFooter(rearmCarry{branch: branch, pr: pr, sha: sha, iteration: iteration}, source)
}

// rearmCommentFooter renders the human line plus the machine-readable footer
// appended to the re-arm comment, in the autopilot-meta shape the dispatcher
// already parses. The footer is the last line, as the dispatcher's parser
// requires.
func rearmCommentFooter(carry rearmCarry, source int) string {
	var human strings.Builder
	fmt.Fprintf(&human, "Retry continues branch `%s`", carry.branch)
	if carry.sha != "" {
		sha7 := carry.sha
		if len(sha7) > 7 {
			sha7 = sha7[:7]
		}
		fmt.Fprintf(&human, " at `%s`", sha7)
	}
	if carry.pr > 0 {
		fmt.Fprintf(&human, "; the closed PR #%d holds the review history", carry.pr)
	}
	human.WriteString(".")

	meta := "branch:" + carry.branch
	if carry.pr > 0 {
		meta += fmt.Sprintf(" pr:%d", carry.pr)
	}
	meta += fmt.Sprintf(" iteration:%d source:%d", carry.iteration+1, source)
	if carry.sha != "" {
		meta += " sha:" + carry.sha
	}
	return fmt.Sprintf("\n\n%s\n\n<!-- autopilot-meta %s -->", human.String(), meta)
}

// applyRearmCarry hands a dead fix issue's branch to its source and returns the
// footer to append to the re-arm comment. The borrowed-branch registry entry is
// always recorded: the stale-branch sweep protects only registry branches and
// open fix-issue footers (branch_sweeper.go), and the dead fix issue is closed,
// so without the entry the branch is unprotected between the re-arm and the
// retry being queued. When the branch is the source's own pilot/GH-N (the usual
// case) the entry carries fromPR=0: the dispatcher deliberately skips such
// self-entries (handlers.go: branchName != pilot/taskID) and every routing gate
// that sends a PR through the fix-issue path requires fromPR > 0 (and the
// pre-PR hold bypass ignores an entry on the task's own branch), so the entry
// protects the branch without re-homing the retry's PR.
func (c *Controller) applyRearmCarry(sourceNum int, carry rearmCarry) string {
	if c.borrowedBranchLookup != nil && sourceNum > 0 {
		fromPR := carry.pr
		if carry.branch == fmt.Sprintf("pilot/GH-%d", sourceNum) {
			fromPR = 0
		}
		c.borrowedBranchLookup.RecordBorrowedBranch(fmt.Sprintf("GH-%d", sourceNum), carry.branch, fromPR)
	}
	return rearmCommentFooter(carry, sourceNum)
}

// rearmDeadOwnerSource restores pilot-retry-ready on the source issue so it
// re-enters the normal retry ladder, mirroring the relabeling notifyExternalClose
// already performs for the reactive-close path (controller.go).
//
// GH-6183: a non-nil carry hands the dead fix issue's branch to the source —
// recorded in the borrowed-branch registry under the source's task ID and
// rendered as an autopilot-meta footer in the re-arm comment — so the retry
// continues that branch instead of building a fresh worktree from main.
func (c *Controller) rearmDeadOwnerSource(ctx context.Context, source *github.Issue, reasonMsg string, carry *rearmCarry) {
	// GH-5249: strip pilot-superseded too — the durable-claim fallback above
	// means `designated` can now be reached via a source that hand off (not
	// pilot-failed) designated to the dead fix issue, which carries
	// pilot-superseded rather than pilot-failed. Leaving it on would land the
	// re-armed issue in a contradictory {pilot-retry-ready, pilot-superseded}
	// state — a human reading the labels would see "superseded" and
	// "retry-ready" fighting each other.
	// GH-6163: strip pilot-in-progress as well. The SDK poller skips every open
	// issue carrying it before it reaches the retry-ready rung, so a re-armed
	// source with the stale label is never re-picked (same poller skip as the
	// GH-5375 path in verifyFixPRDeliversSourceScope). All removals and the add
	// land in one mutation.
	labelsRemoved := []string{github.LabelFailed, github.LabelSuperseded, github.LabelInProgress}
	c.mutateIssueLabels(ctx, source.Number, []string{github.LabelRetryReady}, labelsRemoved)
	// GH-5252: the label strip above is cosmetic only — HasTerminalCompletion
	// reads the ledger, not labels. If this source's latest terminal evidence
	// is a 'superseded' execution row (the durable-claim fallback above can
	// reach a hand-off-designated source, not just a pilot-failed one), that
	// row survives the SDK poller's retry-ready rung (InvalidateCompletion
	// only deletes 'completed' rows) and keeps HasTerminalCompletion=true
	// forever — the re-arm comment posts, the retry-budget label is
	// consumed, and dispatch never fires. Demote it here exactly like the
	// GH-5249 poller probe does (tryRearmSuperseded), instead of requiring a
	// second, separate re-arm gesture the owner-death path never emits. The
	// underlying UPDATE only touches status='superseded' rows, so this is a
	// no-op when the terminal evidence was pilot-failed instead.
	if c.evalStore != nil {
		taskID := fmt.Sprintf("GH-%d", source.Number)
		reclassifyReason := fmt.Sprintf("GH-5252: owner-death re-arm (%s)", reasonMsg)
		if err := c.evalStore.ReclassifySupersededForRearm(taskID, c.projectPath, reclassifyReason); err != nil {
			c.log.Warn("owner-death: failed to demote superseded ledger row for re-arm", "issue", source.Number, "error", err)
		}
	}
	comment := fmt.Sprintf(
		"\U0001F501 **%s**: %s. Re-armed for automatic retry (`%s` restored). `%s` removed so the poller's retry rung sees the issue.",
		RearmCommentMarker, reasonMsg, github.LabelRetryReady, github.LabelInProgress,
	)
	if carry != nil {
		comment += c.applyRearmCarry(source.Number, *carry)
	}
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, source.Number, comment); err != nil {
		c.log.Warn("owner-death: failed to post re-arm comment", "issue", source.Number, "error", err)
	}
	c.fireOwnerDeathAlert(source.Number, reasonMsg, "rearmed")
	c.log.Warn("owner-death: source re-armed after designated fix issue died", "source", source.Number, "reason", reasonMsg, "labels_removed", labelsRemoved)
}

// escalateDeadOwnerSource holds the source issue for manual review instead
// of re-arming, because its retry budget is already exhausted — re-arming
// here would silently bypass the retry-exhausted ceiling.
func (c *Controller) escalateDeadOwnerSource(ctx context.Context, source *github.Issue, reasonMsg string) {
	// GH-6163: an escalated issue must not look retry-able or in-flight, so
	// the same single mutation strips pilot-in-progress and the retry ladder.
	c.mutateIssueLabels(ctx, source.Number,
		[]string{labelNeedsHuman},
		[]string{github.LabelInProgress, github.LabelRetryReady, github.LabelRetry1, github.LabelRetry2},
	)
	comment := fmt.Sprintf(
		"\U0001F6A8 **Owner-death recovery**: %s, and its retries are already exhausted. Holding for manual review (`%s`).",
		reasonMsg, labelNeedsHuman,
	)
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, source.Number, comment); err != nil {
		c.log.Warn("owner-death: failed to post escalation comment", "issue", source.Number, "error", err)
	}
	c.fireOwnerDeathAlert(source.Number, reasonMsg, "escalated")
	c.log.Warn("owner-death: source escalated to needs-human, retries exhausted", "source", source.Number, "reason", reasonMsg)
}

// fireOwnerDeathAlert emits an owner-death alert on behalf of the controller
// (rearm/escalate reactions).
func (c *Controller) fireOwnerDeathAlert(sourceIssueNum int, reasonMsg, outcome string) {
	emitOwnerDeathAlert(c.alertsEngine, c.log, c.repoKey(), sourceIssueNum, reasonMsg, outcome)
}
