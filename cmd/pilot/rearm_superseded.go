package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/logging"
)

// tryRearmSuperseded is GH-5249's re-arm probe: notifyExternalClose's
// supersededClose hand-off tells the reporter the source issue "will not be
// retried automatically under its own number" — but a deliberate operator
// relabel/reopen after that hand-off must still be able to re-admit the
// task_id, the same GH-5139 established for `pilot task cancel`. Before this
// existed, counting 'superseded' as terminal in HasTerminalCompletion (see
// store.go) would have made that promise permanent even for a source an
// operator explicitly wants back in the retry ladder.
//
// backoffKey is threaded through so a successful re-arm can clear the
// repick-backoff window immediately (recordSuccess) rather than waiting out
// whatever cooldown the last "not yet re-armed" probe set.
//
// Returns rearmed=false, err=nil for the ordinary "nothing to do" cases: no
// superseded row (the terminal evidence was a genuine completed/no_op/
// canceled row instead — callers must not reach here in that case, but this
// stays a safe no-op if they do), a non-GitHub task_id, or an open+labeled
// issue with no labeled/reopened event timestamped after the supersede.
// Returns err != nil only on an actual GitHub API failure, so the caller can
// distinguish "confirmed not re-armed" from "couldn't tell" (see
// HasCompletedExecution's error-handling comment for why that distinction
// matters for backoff).
func (c terminalCompletionChecker) tryRearmSuperseded(taskID, projectPath, backoffKey string) (bool, error) {
	exec, found, err := c.store.LatestSupersededExecution(taskID, projectPath)
	if err != nil {
		return false, fmt.Errorf("looking up superseded execution: %w", err)
	}
	if !found || exec.CompletedAt == nil {
		// No superseded row (terminal evidence was completed/no_op/canceled
		// instead) or a superseded row somehow missing its supersede
		// timestamp — either way, nothing GH-5249 can evaluate re-arm
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
	if issue.State != "open" || !github.HasLabel(issue, c.triggerLabel) {
		// Re-arm requires BOTH open and labeled right now — an operator who
		// relabels a still-closed issue, or reopens without the trigger
		// label, hasn't finished the deliberate re-arm gesture yet.
		return false, nil
	}

	events, err := c.ghClient.ListIssueEvents(ctx, c.repoOwner, c.repoName, issueNum)
	if err != nil {
		return false, fmt.Errorf("listing issue #%d events: %w", issueNum, err)
	}
	rearmEvent := latestRearmEvent(events, c.triggerLabel, *exec.CompletedAt)
	if rearmEvent == nil {
		// Open + labeled, but nothing in the timeline shows that state was
		// reached AFTER the supersede — e.g. the label was already there
		// before the hand-off and never touched again. Not a deliberate
		// re-arm gesture.
		return false, nil
	}

	reason := fmt.Sprintf("GH-5249: re-armed by issue #%d %s event at %s (superseded at %s)",
		issueNum, rearmEvent.Event, rearmEvent.CreatedAt.Format(time.RFC3339), exec.CompletedAt.Format(time.RFC3339))
	if err := c.store.ReclassifySupersededForRearm(taskID, projectPath, reason); err != nil {
		return false, fmt.Errorf("reclassifying superseded row: %w", err)
	}
	if err := c.ghClient.RemoveLabel(ctx, c.repoOwner, c.repoName, issueNum, github.LabelSuperseded); err != nil {
		// Best-effort: the store-side reclassify above is what actually
		// un-wedges dispatch (HasTerminalCompletion no longer sees this row
		// as done); a surviving pilot-superseded label is cosmetic
		// staleness, not a correctness problem, since (unlike pilot-blocked)
		// it never excluded the issue from poller candidacy in the first
		// place.
		logging.WithComponent("dispatch").Warn("GH-5249: failed to remove pilot-superseded label after re-arm",
			slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Any("error", err))
	}
	repickBackoff.recordSuccess(backoffKey)
	logging.WithComponent("dispatch").Info("GH-5249: superseded task re-armed via GitHub reopen/relabel",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.String("event", rearmEvent.Event))
	return true, nil
}
