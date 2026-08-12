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

// fixIssueSourceRe extracts the source issue number embedded by
// FeedbackLoop.generateBody/CreateReviewIssue ("Depends on: #123"). Anchored
// to line start so it can't match unrelated inline "Depends on" prose.
var fixIssueSourceRe = regexp.MustCompile(`(?m)^Depends on: #(\d+)\s*$`)

// autopilotMetaMarkerRe requires the autopilot-meta marker to be present
// before trusting a "Depends on" line as a Pilot-authored source reference —
// distinguishes autopilot-spawned fix issues from unrelated uses of the same
// phrase (e.g. epic decomposition in internal/executor/epic.go).
var autopilotMetaMarkerRe = regexp.MustCompile(`<!--\s*autopilot-meta\b`)

// parseFixIssueSource recovers the source issue number from a spawned fix
// issue's body, without any new persistence (GH-4842 explicitly scopes
// designation persistence out — this piggybacks on the existing body
// convention instead).
func parseFixIssueSource(body string) (int, bool) {
	if !autopilotMetaMarkerRe.MatchString(body) {
		return 0, false
	}
	m := fixIssueSourceRe.FindStringSubmatch(body)
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
// via "Depends on: #N", the PR via "pr:N" in the same autopilot-meta comment.
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
