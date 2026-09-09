package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/logging"
)

// reasonNeedsHumanAwaitingRearm is HasCompletedExecutionReason's skip reason
// for a needs_human row that has not (yet) seen re-arm evidence — GH-5414
// names the parked state explicitly instead of falling through to the
// generic "stalled: awaiting re-arm evidence" text canceled/superseded share,
// so the poller's skip log line tells the operator what to do: clear
// pilot-needs-human.
const reasonNeedsHumanAwaitingRearm = "needs_human: awaiting operator to clear pilot-needs-human"

// tryRearmNeedsHuman is GH-5414's re-arm probe for holdPushedBranch's
// needs_human hand-off (GH-5399/PR#5402). Counting 'needs_human' as terminal
// in Store.HasTerminalCompletion fixed a false-retry bug, but it also removed
// the only re-admission path a parked task had: unlike 'canceled' (GH-5139)
// and 'superseded' (GH-5249), needs_human had no re-arm probe at all, so the
// hold comment (internal/executor/runner.go, "clear pilot-needs-human to
// resume") and the pre-PR#5402 operator recipe that worked live on #5375
// until 09-08 both promised something the poller could no longer deliver —
// clearing the label just left the task falling through to
// HasCompletedExecutionReason's generic "stalled: awaiting re-arm evidence"
// branch forever. This is what makes the promise true again, for
// GitHub-backed tasks only.
//
// Mirrors tryRearmCanceled (cmd/pilot/rearm_canceled.go) almost exactly —
// same store-pair shape, same rearmProbeTimeout, same backoff wiring — with
// two differences: the issue must ALSO have actually dropped
// pilot-needs-human (a needs_human hold is only resumable once the label
// that caused it is gone, not just because the trigger label is still
// present), and the re-arm evidence additionally accepts an "unlabeled"
// event for pilot-needs-human itself, via latestNeedsHumanRearmEvent below —
// removing that label IS the documented gesture.
//
// backoffKey is threaded through so a successful re-arm can clear the
// repick-backoff window immediately (recordSuccess) rather than waiting out
// whatever cooldown the last "not yet re-armed" probe set.
//
// Returns rearmed=false, err=nil for the ordinary "nothing to do" cases: no
// needs_human row (the terminal evidence was a genuine completed/no_op/
// canceled/superseded row instead), a non-GitHub task_id, the issue still
// carrying pilot-needs-human, a closed issue, or an open+labeled+cleared
// issue with no qualifying event timestamped after the hold. Returns
// err != nil only on an actual GitHub API failure, so the caller can
// distinguish "confirmed not re-armed" from "couldn't tell" (see
// HasCompletedExecutionReason's error-handling comment for why that
// distinction matters for backoff).
func (c terminalCompletionChecker) tryRearmNeedsHuman(taskID, projectPath, backoffKey string) (bool, error) {
	exec, found, err := c.store.LatestNeedsHumanExecution(taskID, projectPath)
	if err != nil {
		return false, fmt.Errorf("looking up needs_human execution: %w", err)
	}
	if !found || exec.CompletedAt == nil {
		// No needs_human row (terminal evidence was completed/no_op/canceled/
		// superseded instead) or a needs_human row somehow missing its hold
		// timestamp — either way, nothing GH-5414 can evaluate re-arm
		// evidence against.
		return false, nil
	}

	var issueNum int
	if _, scanErr := fmt.Sscanf(taskID, "GH-%d", &issueNum); scanErr != nil || issueNum <= 0 {
		// Not a GitHub-issue-shaped task_id — this checker is only wired for
		// the GitHub SDK poller, but stay defensive rather than assume.
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), rearmProbeTimeout)
	defer cancel()

	issue, err := c.ghClient.GetIssue(ctx, c.repoOwner, c.repoName, issueNum)
	if err != nil {
		return false, fmt.Errorf("fetching issue #%d: %w", issueNum, err)
	}
	if issue.State != "open" || !github.HasLabel(issue, c.triggerLabel) || github.HasLabel(issue, labelPilotNeedsHumanSDK) {
		// Re-arm requires open + trigger-labeled + pilot-needs-human actually
		// gone right now — an operator who relabels a still-closed issue,
		// reopens without the trigger label, or hasn't removed
		// pilot-needs-human yet hasn't finished the deliberate re-arm
		// gesture.
		return false, nil
	}

	events, err := c.ghClient.ListIssueEvents(ctx, c.repoOwner, c.repoName, issueNum)
	if err != nil {
		return false, fmt.Errorf("listing issue #%d events: %w", issueNum, err)
	}
	rearmEvent := latestNeedsHumanRearmEvent(events, *exec.CompletedAt, c.triggerLabel, labelPilotNeedsHumanSDK)
	if rearmEvent == nil {
		// Open + labeled + needs-human cleared right now, but nothing in the
		// timeline shows that state was reached AFTER the hold — e.g. the
		// label was removed before the hold and somehow never re-applied.
		// Not a deliberate re-arm gesture.
		return false, nil
	}

	reason := fmt.Sprintf("GH-5414: re-armed by issue #%d %s event at %s (needs_human at %s)",
		issueNum, rearmEvent.Event, rearmEvent.CreatedAt.Format(time.RFC3339), exec.CompletedAt.Format(time.RFC3339))
	if err := c.store.ReclassifyNeedsHumanForRearm(taskID, projectPath, reason); err != nil {
		return false, fmt.Errorf("reclassifying needs_human row: %w", err)
	}
	repickBackoff.recordSuccess(backoffKey)
	logging.WithComponent("dispatch").Info("GH-5414: needs_human task re-armed via GitHub label-clear/relabel/reopen",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.String("event", rearmEvent.Event))
	return true, nil
}

// latestNeedsHumanRearmEvent extends latestRearmEvent (cmd/pilot/rearm_canceled.go)
// for GH-5414: on top of the shared reopened/renamed/labeled(triggerLabel)
// evidence every other re-arm probe accepts, an "unlabeled" event for
// needsHumanLabel is itself sufficient re-arm evidence — removing
// pilot-needs-human IS the documented gesture (the hold comment posted by
// internal/executor/runner.go and the operator recipe that worked live on
// #5375 until 09-08 both say "clear pilot-needs-human to resume"), unlike
// labeled/unlabeled events on any other label family that other probes
// accept.
func latestNeedsHumanRearmEvent(events []*github.IssueEvent, since time.Time, triggerLabel, needsHumanLabel string) *github.IssueEvent {
	latest := latestRearmEvent(events, since, triggerLabel)
	for _, ev := range events {
		if ev == nil || !ev.CreatedAt.After(since) {
			continue
		}
		if ev.Event != "unlabeled" || ev.Label == nil || !strings.EqualFold(ev.Label.Name, needsHumanLabel) {
			continue
		}
		if latest == nil || ev.CreatedAt.After(latest.CreatedAt) {
			latest = ev
		}
	}
	return latest
}
