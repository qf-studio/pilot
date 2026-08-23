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

// rearmProbeTimeout bounds the two GitHub API calls tryRearmCanceled makes
// (GetIssue + ListIssueEvents). This is a rare, individually-cancelled-task
// code path gated behind repickBackoff (never a hot per-tick loop — see
// HasCompletedExecution's caller), so a generous-but-bounded timeout is safe.
const rearmProbeTimeout = 15 * time.Second

// tryRearmCanceled is GH-5139's re-arm probe: `pilot task cancel` tells the
// operator that reopening or relabeling the issue re-admits the task_id.
// Before this existed nothing ever checked for that, so the hint was false
// (verified live during the GH-5127/5129 recovery). This is what makes it
// true, for GitHub-backed tasks only.
//
// backoffKey is threaded through so a successful re-arm can clear the
// repick-backoff window immediately (recordSuccess) rather than waiting out
// whatever cooldown the last "not yet re-armed" probe set.
//
// Returns rearmed=false, err=nil for the ordinary "nothing to do" cases: no
// canceled row (the terminal evidence was a genuine completed/no_op row —
// callers must not reach here in that case, but this stays a safe no-op if
// they do), a non-GitHub task_id, or an open+labeled issue with no
// labeled/reopened event timestamped after the cancel. Returns err != nil
// only on an actual GitHub API failure, so the caller can distinguish
// "confirmed not re-armed" from "couldn't tell" (see HasCompletedExecution's
// error-handling comment for why that distinction matters for backoff).
func (c terminalCompletionChecker) tryRearmCanceled(taskID, projectPath, backoffKey string) (bool, error) {
	exec, found, err := c.store.LatestCanceledExecution(taskID, projectPath)
	if err != nil {
		return false, fmt.Errorf("looking up canceled execution: %w", err)
	}
	if !found || exec.CompletedAt == nil {
		// No canceled row (terminal evidence was completed/no_op instead) or a
		// canceled row somehow missing its cancel timestamp — either way,
		// nothing GH-5139 can evaluate re-arm evidence against.
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
		// reached AFTER the cancel — e.g. the label was already there before
		// cancel and never touched again. Not a deliberate re-arm gesture.
		return false, nil
	}

	reason := fmt.Sprintf("GH-5139: re-armed by issue #%d %s event at %s (canceled at %s)",
		issueNum, rearmEvent.Event, rearmEvent.CreatedAt.Format(time.RFC3339), exec.CompletedAt.Format(time.RFC3339))
	if err := c.store.ReclassifyCanceledForRearm(taskID, projectPath, reason); err != nil {
		return false, fmt.Errorf("reclassifying canceled row: %w", err)
	}
	repickBackoff.recordSuccess(backoffKey)
	logging.WithComponent("dispatch").Info("GH-5139: canceled task re-armed via GitHub reopen/relabel",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.String("event", rearmEvent.Event))
	return true, nil
}

// latestRearmEvent scans events (as returned by ListIssueEvents, oldest
// first) for the most recent "reopened" event, or "labeled" event naming
// label, whose CreatedAt is strictly after since (the cancel timestamp).
// Either event type alone is sufficient evidence of a deliberate operator
// gesture post-cancel — the caller separately confirms the issue is
// currently open AND labeled, so this only needs to establish that one of
// those two state changes happened after the cancel, not both.
func latestRearmEvent(events []*github.IssueEvent, label string, since time.Time) *github.IssueEvent {
	var latest *github.IssueEvent
	for _, ev := range events {
		if ev == nil || !ev.CreatedAt.After(since) {
			continue
		}
		switch ev.Event {
		case "reopened":
		case "labeled":
			if ev.Label == nil || !strings.EqualFold(ev.Label.Name, label) {
				continue
			}
		default:
			continue
		}
		if latest == nil || ev.CreatedAt.After(latest.CreatedAt) {
			latest = ev
		}
	}
	return latest
}
