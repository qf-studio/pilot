package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/logging"
)

// stalledRearmSweepTimeout bounds one sweepStalledRearm pass: the ListIssues
// call plus however many tryRearmStalled probes (each individually bounded
// by rearmProbeTimeout) it triggers within that pass.
const stalledRearmSweepTimeout = 60 * time.Second

// tryRearmStalled is GH-5212's re-arm probe for stalled rows — GH-5139's
// operator-cancel re-arm pattern extended to the escalate-and-hold path
// (repick hard cap / identical-failure streak, escalateStalledTask in
// internal/executor/dispatcher.go). A trigger-label re-add or issue reopen
// timestamped after the stall re-admits the task_id through the ordinary
// retry path — before this, nothing ever checked for that, so a hosted
// tenant whose task stalled on a since-fixed cause had no product lever.
//
// Deliberately NOT reachable via terminalCompletionChecker.HasCompletedExecution
// (GH-5139's own call site, next to tryRearmCanceled) for two independent
// reasons:
//  1. executor.HasTerminalCompletion never counts a 'stalled' row as done (only
//     'canceled' and error-free 'no_op' are), so HasCompletedExecution's own
//     `if err != nil || !done { return done, err }` guard returns before a
//     stalled-row branch could ever run.
//  2. surfaceStalledIssue (internal/executor/dispatcher.go) unconditionally
//     labels the issue pilot-blocked, and the vendored SDK poller's candidate
//     loop (studio-sdk/sdk/integrations/github/poller.go: checkForNewIssues /
//     findOldestUnprocessedIssue) excludes any pilot-blocked issue via
//     HasLabel(issue, LabelBlocked) BEFORE ever calling the ExecutionChecker —
//     there is no host hook point between the two to intercept.
//
// So this is driven instead by sweepStalledRearm below, a host-side scan
// run outside the SDK's per-tick admission chokepoint. tryRearmStalled itself
// still mirrors tryRearmCanceled (cmd/pilot/rearm_canceled.go) almost exactly,
// down to reusing latestRearmEvent unchanged — only the store pair and the
// extra pilot-blocked label removal differ.
func (c terminalCompletionChecker) tryRearmStalled(taskID, projectPath, backoffKey string) (bool, error) {
	exec, found, err := c.store.LatestStalledExecution(taskID, projectPath)
	if err != nil {
		return false, fmt.Errorf("looking up stalled execution: %w", err)
	}
	if !found || exec.CompletedAt == nil {
		// No stalled row (this task_id's terminal-ish state came from
		// something else entirely) or a stalled row somehow missing its
		// stall timestamp — either way, nothing to evaluate re-arm evidence
		// against.
		return false, nil
	}

	var issueNum int
	if _, scanErr := fmt.Sscanf(taskID, "GH-%d", &issueNum); scanErr != nil || issueNum <= 0 {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), rearmProbeTimeout)
	defer cancel()

	issue, err := c.ghClient.GetIssue(ctx, c.repoOwner, c.repoName, issueNum)
	if err != nil {
		return false, fmt.Errorf("fetching issue #%d: %w", issueNum, err)
	}
	if issue.State != "open" || !github.HasLabel(issue, c.triggerLabel) {
		// Re-arm requires BOTH open and (still/again) carrying the trigger
		// label right now — mirrors tryRearmCanceled's same requirement.
		return false, nil
	}

	events, err := c.ghClient.ListIssueEvents(ctx, c.repoOwner, c.repoName, issueNum)
	if err != nil {
		return false, fmt.Errorf("listing issue #%d events: %w", issueNum, err)
	}
	rearmEvent := latestRearmEvent(events, c.triggerLabel, *exec.CompletedAt)
	if rearmEvent == nil {
		// Open + labeled, but nothing in the timeline shows that state was
		// reached AFTER the stall — not a deliberate re-arm gesture.
		return false, nil
	}

	reason := fmt.Sprintf("GH-5212: re-armed by issue #%d %s event at %s (stalled at %s)",
		issueNum, rearmEvent.Event, rearmEvent.CreatedAt.Format(time.RFC3339), exec.CompletedAt.Format(time.RFC3339))
	if err := c.store.ReclassifyStalledForRearm(taskID, projectPath, reason); err != nil {
		return false, fmt.Errorf("reclassifying stalled row: %w", err)
	}

	// Both must happen (task spec): a surviving pilot-blocked label keeps the
	// SDK poller's candidate loop excluding the issue forever regardless of
	// the store-side row status. Best-effort: the store demotion above is
	// already durable, so a label-removal failure is logged, not fatal — the
	// next sweep pass will simply retry the label removal (LatestStalledExecution
	// now finds nothing, since the row is 'failed' not 'stalled', so this
	// specific retry path only applies while the row is still stalled; a
	// failure here after a successful reclassify is the one case where the
	// label can persist stale, same class of best-effort gap surfaceStalledIssue's
	// own labeling already accepts).
	if err := c.ghClient.RemoveLabel(ctx, c.repoOwner, c.repoName, issueNum, github.LabelBlocked); err != nil {
		logging.WithComponent("dispatch").Warn("GH-5212: stalled task reclassified but pilot-blocked label removal failed — issue may stay excluded from poller candidacy until manually cleared",
			slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Any("error", err))
	}

	repickBackoff.recordSuccess(backoffKey)
	logging.WithComponent("dispatch").Info("GH-5212: stalled task re-armed via GitHub reopen/relabel",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.String("event", rearmEvent.Event))
	return true, nil
}

// sweepStalledRearm scans repoOwner/repoName for open issues currently
// carrying pilot-blocked and, for each one backed by a stalled execution row
// under projectPath, probes for post-stall re-arm evidence via
// tryRearmStalled. See tryRearmStalled's doc comment for why this scan
// exists instead of a hook inside the SDK's admission chokepoint.
//
// Per-issue throttling reuses the exact repickBackoff window/key
// (repickBackoffKey(projectPath, taskID)) GH-4469's HasCompletedExecution
// gate and GH-5139's tryRearmCanceled already use — a pilot-blocked issue
// with no re-arm evidence yet must not pay for a GetIssue+ListIssueEvents
// call on every sweep pass. gateStatus is consulted before ListIssues even
// runs is not possible (ListIssues has to run first to discover which
// issues are pilot-blocked at all), but every individual probe past that
// point is gated, so a quiet backlog of blocked issues costs one list call
// per sweep interval and nothing more.
func (c terminalCompletionChecker) sweepStalledRearm(ctx context.Context, projectPath string) {
	if c.ghClient == nil {
		return
	}

	issues, err := c.ghClient.ListIssues(ctx, c.repoOwner, c.repoName, &github.ListIssuesOptions{
		Labels: []string{github.LabelBlocked},
		State:  github.StateOpen,
	})
	if err != nil {
		logging.WithComponent("dispatch").Warn("GH-5212: stalled re-arm sweep failed to list pilot-blocked issues",
			slog.String("repo", c.repoOwner+"/"+c.repoName), slog.Any("error", err))
		return
	}

	for _, issue := range issues {
		taskID := fmt.Sprintf("GH-%d", issue.Number)
		backoffKey := repickBackoffKey(projectPath, taskID)

		if gated, _ := repickBackoff.gateStatus(backoffKey); gated {
			continue
		}

		_, found, err := c.store.LatestStalledExecution(taskID, projectPath)
		if err != nil {
			logging.WithComponent("dispatch").Warn("GH-5212: stalled re-arm sweep: store lookup failed",
				slog.String("task_id", taskID), slog.Any("error", err))
			continue
		}
		if !found {
			// pilot-blocked from a different cause (e.g. GH-2402 deterministic
			// title-guard escalation) or belongs to another project sharing
			// this repo — nothing for this probe to evaluate.
			continue
		}

		rearmed, err := c.tryRearmStalled(taskID, projectPath, backoffKey)
		if err != nil {
			logging.WithComponent("dispatch").Warn("GH-5212 stalled re-arm probe failed",
				slog.String("task_id", taskID), slog.Any("error", err))
			repickBackoff.recordClaimLostDrop(backoffKey)
			continue
		}
		if !rearmed {
			repickBackoff.recordClaimLostDrop(backoffKey)
		}
	}
}

// runStalledRearmSweepLoop drives sweepStalledRearm on the same cadence as
// the SDK poller's own candidate polling (interval) — frequent enough that a
// relabel/reopen is noticed promptly, but never tighter, so this never
// becomes a hot loop independent of the poller it rides alongside. Runs one
// immediate pass before entering the ticker, matching github.Cleaner.Start's
// "run initial pass, then tick" shape.
func runStalledRearmSweepLoop(ctx context.Context, checker terminalCompletionChecker, projectPath string, interval time.Duration, log *slog.Logger) {
	sweep := func() {
		sweepCtx, cancel := context.WithTimeout(ctx, stalledRearmSweepTimeout)
		defer cancel()
		checker.sweepStalledRearm(sweepCtx, projectPath)
	}

	sweep()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Debug("GH-5212: stalled re-arm sweep loop stopped (context canceled)")
			return
		case <-ticker.C:
			sweep()
		}
	}
}
