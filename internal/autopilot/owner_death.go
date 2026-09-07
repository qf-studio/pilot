package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
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
// and the original issue stays open (pilot-superseded, never closed) after
// hand-off, so that line permanently blocked the fix issue itself from ever
// dispatching. The source number now lives inside autopilot-meta instead,
// which the poller does not parse as a dependency marker.
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
// verifyFixPRDeliversSourceScope uses this to decide which of its two label
// disciplines applies to a given source issue — spawnFailureIssue still
// applies pilot-superseded eagerly at spawn time (unchanged, out of scope
// for GH-5362), so the CI-fix path there still means "already labeled,
// react only to a mismatch"; spawnReviewIssue no longer does, so the review
// path means "never labeled yet, gate whether it ever is."
func isReviewRevisionFixIssue(body string) bool {
	m := fixIssueFailureTypeRe.FindStringSubmatch(body)
	return len(m) >= 2 && m[1] == string(FailureReviewRequested)
}

// verifyFixPRDeliversSourceScope guards GH-5348 subtask 3 for the CI-fix
// path, and — as of GH-5362 — gates whether pilot-superseded is ever applied
// at all for the review-revision path. spawnFailureIssue (controller.go)
// still marks a CI-fix source issue pilot-superseded the moment its fix
// ISSUE is created — before that issue's own PR exists, let alone before its
// diff is known. GH-5348 subtasks 1/2 fixed the one confirmed way that PR's
// eventual diff could end up dropping the origin's real changes (a deleted
// branch silently rebuilt from main); this is the belt-and-braces check for
// every other way a fix PR could land unrelated content — a stale worktree,
// a human pushing to the wrong branch, a future regression in the recovery
// path. It runs once the fix PR actually merges, the one point its diff is
// fully known, and compares its changed files against the origin PR it was
// spawned to replace (source:N / pr:N, recorded in the fix issue body by
// FeedbackLoop.generateBody/CreateReviewIssue).
//
// GH-5362: spawnReviewIssue no longer applies pilot-superseded eagerly at
// spawn time (the same pilot-console #275 false-success shape GH-5348/
// GH-5351 fixed on the CI-fix path) — a review-revision's source issue
// reaches here carrying no terminal label at all, and isReviewRevisionFixIssue
// tells the two paths apart (CreateFailureIssue vs CreateReviewIssue leave a
// different Failure Type marker in the fix issue body) so this function can
// give each its own discipline without disturbing the other:
//
//   - CI-fix (isReviewRevisionFixIssue == false): unchanged GH-5348
//     subtask-3 behavior — the label is already applied, so a source that
//     isn't (still) carrying pilot-superseded has already been resolved some
//     other way (re-armed, previously escalated) and is left alone; only a
//     confirmed-superseded source with zero file overlap gets corrected
//     (label stripped, escalated to pilot-needs-human).
//   - Review-revision (isReviewRevisionFixIssue == true): GH-5362 gate — no
//     label was ever applied, so this is the one place that decides whether
//     one ever is. Confirmed overlap applies pilot-superseded for the first
//     time; zero overlap escalates to pilot-needs-human without anything to
//     strip.
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
		// Not a Pilot-spawned CI-fix/revision issue — nothing to verify.
		return
	}
	originPR, ok := parseFixIssuePR(fixIssue.Body)
	if !ok {
		c.log.Warn("verifyFixPRDeliversSourceScope: fix issue names a source but no origin PR, skipping",
			"issue", prState.IssueNumber, "source", sourceNum)
		return
	}
	isReviewRevision := isReviewRevisionFixIssue(fixIssue.Body)

	fixFiles, err := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, prState.PRNumber)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to list fix PR files, skipping (fail-open)",
			"pr", prState.PRNumber, "error", err)
		return
	}
	originFiles, err := c.ghClient.ListPullRequestFiles(ctx, c.owner, c.repo, originPR)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to list origin PR files, skipping (fail-open)",
			"origin_pr", originPR, "error", err)
		return
	}
	overlap := filesOverlap(fixFiles, originFiles)
	// GH-4284-style caution: an origin PR with no recorded files (deleted
	// commit history unreadable, or a genuinely empty diff) can't prove
	// overlap or non-overlap either way — skip rather than act on a false
	// signal, whichever direction that action would take.
	noEvidence := len(originFiles) == 0
	if noEvidence {
		c.log.Warn("verifyFixPRDeliversSourceScope: origin PR has no recorded files, skipping (fail-open)",
			"source", sourceNum, "origin_pr", originPR)
		return
	}

	source, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, sourceNum)
	if err != nil {
		c.log.Warn("verifyFixPRDeliversSourceScope: failed to fetch source issue, skipping",
			"source", sourceNum, "error", err)
		return
	}

	if !isReviewRevision {
		// Unchanged GH-5348 subtask-3 behavior for the CI-fix path.
		if source.State == github.StateClosed || !github.HasLabel(source, github.LabelSuperseded) {
			// Already resolved some other way (closed, re-armed, previously
			// escalated) — nothing left to correct.
			return
		}
		if overlap {
			return
		}
		reasonMsg := fmt.Sprintf(
			"its designated fix PR #%d merged but shares no changed files with the original PR #%d it was spawned to continue",
			prState.PRNumber, originPR,
		)
		if err := c.labeler.AddLabels(ctx, c.owner, c.repo, sourceNum, []string{labelNeedsHuman}); err != nil {
			c.log.Warn("verifyFixPRDeliversSourceScope: failed to add needs-human label", "issue", sourceNum, "error", err)
		}
		if err := c.labeler.RemoveLabel(ctx, c.owner, c.repo, sourceNum, github.LabelSuperseded); err != nil {
			c.log.Debug("verifyFixPRDeliversSourceScope: failed to remove pilot-superseded label (may not exist)", "issue", sourceNum, "error", err)
		}
		comment := fmt.Sprintf(
			"\U0001F6A8 **Delivery mismatch detected (GH-5348)**: %s — the original changes may have been silently dropped. Marked `%s` for manual review instead of staying parked under `%s`.",
			reasonMsg, labelNeedsHuman, github.LabelSuperseded,
		)
		if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, sourceNum, comment); err != nil {
			c.log.Warn("verifyFixPRDeliversSourceScope: failed to post comment", "issue", sourceNum, "error", err)
		}
		c.fireOwnerDeathAlert(sourceNum, reasonMsg, "escalated")
		c.log.Warn("verifyFixPRDeliversSourceScope: fix PR shares no files with origin PR — escalated source issue",
			"source", sourceNum, "fix_issue", prState.IssueNumber, "fix_pr", prState.PRNumber, "origin_pr", originPR)
		return
	}

	// GH-5362 gate for the review-revision path: no label was ever applied
	// at spawn time, so decide here, once, whether one ever is.
	if source.State == github.StateClosed || github.HasLabel(source, github.LabelSuperseded) {
		// Already resolved (closed) or already gated by an earlier run of
		// this function (e.g. a daemon restart mid-gate) — idempotent,
		// nothing more to do.
		return
	}
	if overlap {
		if err := c.labeler.AddLabels(ctx, c.owner, c.repo, sourceNum, []string{github.LabelSuperseded}); err != nil {
			c.log.Warn("verifyFixPRDeliversSourceScope: failed to add superseded label", "issue", sourceNum, "error", err)
		}
		c.log.Info("verifyFixPRDeliversSourceScope: revision PR confirmed to deliver origin scope — source issue marked superseded",
			"source", sourceNum, "fix_issue", prState.IssueNumber, "fix_pr", prState.PRNumber, "origin_pr", originPR)
		return
	}

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
	c.rearmDeadOwnerSource(ctx, source, reasonMsg)
}

// rearmDeadOwnerSource restores pilot-retry-ready on the source issue so it
// re-enters the normal retry ladder, mirroring the relabeling notifyExternalClose
// already performs for the reactive-close path (controller.go).
func (c *Controller) rearmDeadOwnerSource(ctx context.Context, source *github.Issue, reasonMsg string) {
	if err := c.labeler.AddLabels(ctx, c.owner, c.repo, source.Number, []string{github.LabelRetryReady}); err != nil {
		c.log.Warn("owner-death: failed to add retry-ready label", "issue", source.Number, "error", err)
	}
	if err := c.labeler.RemoveLabel(ctx, c.owner, c.repo, source.Number, github.LabelFailed); err != nil {
		c.log.Debug("owner-death: failed to remove pilot-failed label (may not exist)", "issue", source.Number, "error", err)
	}
	// GH-5249: also strip pilot-superseded — the durable-claim fallback above
	// means `designated` can now be reached via a source that hand off (not
	// pilot-failed) designated to the dead fix issue, which carries
	// pilot-superseded rather than pilot-failed. Leaving it on would land the
	// re-armed issue in a contradictory {pilot-retry-ready, pilot-superseded}
	// state — the SDK poller has no skip rung for pilot-superseded (that's
	// the GH-5249 root defect), but a human reading the labels would still
	// see "superseded" and "retry-ready" fighting each other.
	if err := c.labeler.RemoveLabel(ctx, c.owner, c.repo, source.Number, github.LabelSuperseded); err != nil {
		c.log.Debug("owner-death: failed to remove pilot-superseded label (may not exist)", "issue", source.Number, "error", err)
	}
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
		"\U0001F501 **Owner-death recovery**: %s. Re-armed for automatic retry (`%s` restored).",
		reasonMsg, github.LabelRetryReady,
	)
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, source.Number, comment); err != nil {
		c.log.Warn("owner-death: failed to post re-arm comment", "issue", source.Number, "error", err)
	}
	c.fireOwnerDeathAlert(source.Number, reasonMsg, "rearmed")
	c.log.Warn("owner-death: source re-armed after designated fix issue died", "source", source.Number, "reason", reasonMsg)
}

// escalateDeadOwnerSource holds the source issue for manual review instead
// of re-arming, because its retry budget is already exhausted — re-arming
// here would silently bypass the retry-exhausted ceiling.
func (c *Controller) escalateDeadOwnerSource(ctx context.Context, source *github.Issue, reasonMsg string) {
	if err := c.labeler.AddLabels(ctx, c.owner, c.repo, source.Number, []string{labelNeedsHuman}); err != nil {
		c.log.Warn("owner-death: failed to add needs-human label", "issue", source.Number, "error", err)
	}
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
