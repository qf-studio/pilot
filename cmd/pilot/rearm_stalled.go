package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/logging"
)

// stalledRearmSweepTimeout bounds one sweepStalledRearm pass: the ListIssues
// call plus however many tryRearmStalled probes (each individually bounded
// by rearmProbeTimeout) it triggers within that pass.
const stalledRearmSweepTimeout = 60 * time.Second

// retryReadyRearmLabels (GH-5272) are the labels — beyond the base trigger
// label — whose "labeled" event counts as re-arm evidence for a stalled
// task_id: the retry-ready label an operator adds per surfaceStalledIssue's
// own instructions, plus the two rungs the vendored SDK poller's own
// retry-budget ladder advances it through (shouldRetryRetryReadyIssue in
// studio-sdk/sdk/integrations/github/poller.go) before this probe's next
// sweep pass gets a chance to run. pilot-retry-exhausted is deliberately
// excluded — that label means the SDK itself decided the retry budget is
// spent, and this probe must not fight that call.
var retryReadyRearmLabels = []string{github.LabelRetryReady, github.LabelRetry1, github.LabelRetry2}

// isRetryReadyLabeled reports whether issue currently carries any label in
// retryReadyRearmLabels.
func isRetryReadyLabeled(issue *github.Issue) bool {
	for _, l := range retryReadyRearmLabels {
		if github.HasLabel(issue, l) {
			return true
		}
	}
	return false
}

// tryRearmStalled is GH-5212's re-arm probe for stalled rows — GH-5139's
// operator-cancel re-arm pattern extended to the escalate-and-hold path
// (repick hard cap / identical-failure streak, escalateStalledTask in
// internal/executor/dispatcher.go). A trigger-label re-add or issue reopen
// timestamped after the stall re-admits the task_id through the ordinary
// retry path — before this, nothing ever checked for that, so a hosted
// tenant whose task stalled on a since-fixed cause had no product lever.
//
// GH-5272: surfaceStalledIssue's own posted comment tells the operator to
// run `--remove-label pilot-blocked --add-label pilot-retry-ready` — a
// mutation that never touches the base trigger label at all. Before GH-5272,
// latestRearmEvent only recognized a labeled/reopened event for the trigger
// label, so the exact recipe the bot itself hands out could never satisfy
// this probe's evidence check even once sweepStalledRearm found the issue.
// retryReadyRearmLabels below is passed alongside the trigger label so a
// labeled event for pilot-retry-ready/-1/-2 (the SDK's own retry-budget
// ladder, which advances pilot-retry-ready forward within a poll tick or two
// — see studio-sdk's shouldRetryRetryReadyIssue) counts as evidence too, not
// just the current label snapshot (which may already have moved past
// pilot-retry-ready by the time this probe runs).
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
		// Re-arm requires BOTH open and (still/again) carrying the base
		// trigger label right now — mirrors tryRearmCanceled's same
		// requirement. The base trigger label ("pilot") is never touched by
		// the documented stalled re-arm recipe (remove pilot-blocked, add
		// pilot-retry-ready), so this check is unaffected by GH-5272.
		return false, nil
	}

	events, err := c.ghClient.ListIssueEvents(ctx, c.repoOwner, c.repoName, issueNum)
	if err != nil {
		return false, fmt.Errorf("listing issue #%d events: %w", issueNum, err)
	}
	kind, evidenceAt, ok := stalledRearmEvidence(issue, events, *exec.CompletedAt, append([]string{c.triggerLabel}, retryReadyRearmLabels...)...)
	if !ok {
		// Open + labeled, but nothing observed shows that state was reached
		// AFTER the stall (no base-label relabel/reopen, no
		// pilot-retry-ready/-1/-2 label add, no body/metadata edit) — not a
		// deliberate re-arm gesture.
		return false, nil
	}

	reason := fmt.Sprintf("GH-5212: re-armed by issue #%d %s evidence at %s (stalled at %s)",
		issueNum, kind, evidenceAt.Format(time.RFC3339), exec.CompletedAt.Format(time.RFC3339))
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
	stalledRearmNoEvidenceStreaks.reset(backoffKey)
	logging.WithComponent("dispatch").Info("GH-5212: stalled task re-armed via GitHub reopen/relabel/edit",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.String("evidence", kind))
	return true, nil
}

// stalledRearmEvidence is GH-5376's broadened evidence check for
// tryRearmStalled: on top of latestRearmEvent's labeled/reopened events, a
// body edit also counts as a deliberate operator re-arm gesture — the
// GH-5346 incident re-armed pilot-retry-1 by editing the issue body ("new
// branch from main") alone, which latestRearmEvent can never see (neither a
// label nor a reopen), so tryRearmStalled kept reporting rearmed=false
// forever and the sweep never stopped calling recordClaimLostDrop.
//
// Two signals are checked, in order:
//  1. An "edited" timeline event (as returned by ListIssueEvents) newer than
//     since — present if/when GitHub's classic Events API ever surfaces body
//     edits for a given repo.
//  2. issue.UpdatedAt newer than since — the reliable fallback, since GH's
//     classic /issues/{n}/events endpoint (unlike the newer Timeline API)
//     does not emit an "edited" event for a body-only edit in practice, but
//     GetIssue's own UpdatedAt field always moves forward on any edit
//     (body, title, or otherwise). issue is the same GetIssue response
//     tryRearmStalled already fetched for the open/labeled check, so this
//     costs no extra API call.
//
// Returns the evidence kind (for logging) and its timestamp alongside ok.
func stalledRearmEvidence(issue *github.Issue, events []*github.IssueEvent, since time.Time, labels ...string) (kind string, at time.Time, ok bool) {
	if ev := latestRearmEvent(events, since, labels...); ev != nil {
		return ev.Event, ev.CreatedAt, true
	}
	for _, ev := range events {
		if ev != nil && ev.Event == "edited" && ev.CreatedAt.After(since) {
			return "edited", ev.CreatedAt, true
		}
	}
	if issue != nil && issue.UpdatedAt.After(since) {
		return "body/metadata edit (updated_at)", issue.UpdatedAt, true
	}
	return "", time.Time{}, false
}

// sweepStalledRearm scans repoOwner/repoName for open issues currently
// carrying pilot-blocked OR any pilot-retry-ready/-1/-2 label and, for each
// one backed by a stalled execution row under projectPath, probes for
// post-stall re-arm evidence via tryRearmStalled. See tryRearmStalled's doc
// comment for why this scan exists instead of a hook inside the SDK's
// admission chokepoint.
//
// GH-5272: originally scanned pilot-blocked only, on the assumption that a
// stalled issue stays pilot-blocked until this sweep clears it. But
// surfaceStalledIssue's own posted comment instructs the operator to REMOVE
// pilot-blocked in the very same edit that adds pilot-retry-ready — so by
// the time any sweep pass ran, the issue had already dropped out of a
// pilot-blocked-only candidate list, and the recipe the bot itself hands out
// could never reach tryRearmStalled at all. Listing all open issues (no
// server-side label filter — ListIssues already fetches every open issue
// and filters in Go, so this costs nothing extra) and filtering here for
// EITHER label family closes that gap without widening what tryRearmStalled
// itself accepts as evidence.
//
// Per-issue throttling reuses the exact repickBackoff window/key
// (repickBackoffKey(projectPath, taskID)) GH-4469's HasCompletedExecution
// gate and GH-5139's tryRearmCanceled already use — a candidate issue with
// no re-arm evidence yet must not pay for a GetIssue+ListIssueEvents call on
// every sweep pass. gateStatus can't be consulted before ListIssues runs
// (ListIssues has to run first to discover which issues are candidates at
// all), but every individual probe past that point is gated, so a quiet
// backlog of blocked/retry-ready issues costs one list call per sweep
// interval and nothing more.
func (c terminalCompletionChecker) sweepStalledRearm(ctx context.Context, projectPath string) {
	if c.ghClient == nil {
		return
	}

	issues, err := c.ghClient.ListIssues(ctx, c.repoOwner, c.repoName, &github.ListIssuesOptions{
		State: github.StateOpen,
	})
	if err != nil {
		logging.WithComponent("dispatch").Warn("GH-5212: stalled re-arm sweep failed to list open issues",
			slog.String("repo", c.repoOwner+"/"+c.repoName), slog.Any("error", err))
		return
	}

	for _, issue := range issues {
		if !github.HasLabel(issue, github.LabelBlocked) && !isRetryReadyLabeled(issue) {
			// Neither escalated-and-held nor mid-re-arm — not a candidate
			// this sweep needs to spend a probe on.
			continue
		}
		if github.HasLabel(issue, labelPilotNeedsHumanSDK) {
			// GH-5376: already escalated by escalateStalledRearmNoEvidence
			// below — an operator must clear pilot-needs-human (and satisfy
			// tryRearmStalled's evidence check) before this sweep resumes
			// probing. Without this, the sweep would keep re-probing (and
			// re-posting the escalation comment) on every pass forever.
			continue
		}

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
			// title-guard escalation), a retry-ready label on a task that was
			// never stalled, or belongs to another project sharing this repo —
			// nothing for this probe to evaluate.
			continue
		}

		rearmed, err := c.tryRearmStalled(taskID, projectPath, backoffKey)
		if err != nil {
			logging.WithComponent("dispatch").Warn("GH-5212 stalled re-arm probe failed",
				slog.String("task_id", taskID), slog.Any("error", err))
			repickBackoff.recordClaimLostDrop(backoffKey)
			continue
		}
		if rearmed {
			continue
		}

		// GH-5376: recordClaimLostDrop alone has no backstop — nothing ever
		// stopped this branch from firing again next sweep pass, forever
		// (GH-5346: 76 drops over 33h with claim_lost_drops explicitly
		// excluded from dispatcherRepickHardCap's gating counter by design).
		// Once the SAME key has found no evidence
		// terminalDropPilotStripThreshold (GH-5297's constant, reused here)
		// times in a row, stop growing the backoff via recordClaimLostDrop
		// and escalate to an operator instead.
		streak := stalledRearmNoEvidenceStreaks.increment(backoffKey)
		if streak >= terminalDropPilotStripThreshold {
			c.escalateStalledRearmNoEvidence(ctx, taskID, issue.Number, streak)
			stalledRearmNoEvidenceStreaks.reset(backoffKey)
			continue
		}
		repickBackoff.recordClaimLostDrop(backoffKey)
	}
}

// stalledRearmNoEvidenceStreaks counts, per repickBackoffKey, how many
// consecutive sweepStalledRearm passes found no re-arm evidence for that key
// (GH-5376). Reset on any successful re-arm (tryRearmStalled returning true)
// or once escalateStalledRearmNoEvidence fires — the pilot-needs-human label
// applied there is what actually stops the sweep from revisiting the issue
// (see the candidate-filter check above), so resetting the streak here just
// keeps this counter from drifting stale if the label is ever cleared
// without a genuine re-arm.
//
// A package-level var (mirroring repickBackoff) since terminalCompletionChecker
// is constructed fresh per call by runStalledRearmSweepLoop's closure — the
// streak has to outlive any single sweepStalledRearm call to count "in a
// row" across sweep passes.
type stalledRearmNoEvidenceTracker struct {
	mu      sync.Mutex
	streaks map[string]int
}

func newStalledRearmNoEvidenceTracker() *stalledRearmNoEvidenceTracker {
	return &stalledRearmNoEvidenceTracker{streaks: make(map[string]int)}
}

// increment bumps key's streak and returns the new count.
func (t *stalledRearmNoEvidenceTracker) increment(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.streaks[key]++
	return t.streaks[key]
}

// reset clears key's streak, e.g. after a successful re-arm or an escalation.
func (t *stalledRearmNoEvidenceTracker) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.streaks, key)
}

var stalledRearmNoEvidenceStreaks = newStalledRearmNoEvidenceTracker()

// escalateStalledRearmNoEvidence posts the explanatory comment and applies
// pilot-needs-human once sweepStalledRearm has failed to find re-arm
// evidence terminalDropPilotStripThreshold times in a row for the same
// task_id (GH-5376) — the operator-signal backstop the GH-5346 incident
// (76 claim-lost drops, 33h, no signal) never had. Mirrors GH-5300's
// stripPilotLabelAndCommentSDK shape but applies pilot-needs-human instead
// of stripping the trigger label: the sweep's own candidate filter (in
// sweepStalledRearm above) already excludes pilot-needs-human issues, so
// applying it here is what makes this a one-shot escalation rather than a
// per-sweep-pass repeat, without touching pilot-blocked/pilot-retry-ready
// (which the documented re-arm recipe still expects to manipulate).
func (c terminalCompletionChecker) escalateStalledRearmNoEvidence(ctx context.Context, taskID string, issueNum int, streak int) {
	comment := fmt.Sprintf(
		"⚠️ **Pilot could not confirm this stalled task was re-armed**\n\n"+
			"This issue was checked %d times with no evidence of a deliberate re-arm gesture since it stalled. "+
			"Re-arm evidence is any of: re-adding the `%s` label, adding one of `%s`, reopening the issue, "+
			"or editing the issue body/title — all timestamped after the stall.\n\n"+
			"Applying `%s` so this stops being silently re-checked. Remove it and repeat one of the above to re-arm.",
		streak, c.triggerLabel, strings.Join(retryReadyRearmLabels, "`, `"), labelPilotNeedsHumanSDK,
	)
	if _, err := c.ghClient.AddComment(ctx, c.repoOwner, c.repoName, issueNum, comment); err != nil {
		logging.WithComponent("dispatch").Warn("GH-5376: failed to post stalled-no-evidence escalation comment",
			slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Any("error", err))
	}
	if err := c.ghClient.AddLabels(ctx, c.repoOwner, c.repoName, issueNum, []string{labelPilotNeedsHumanSDK}); err != nil {
		logging.WithComponent("dispatch").Warn("GH-5376: failed to apply pilot-needs-human after repeated no-evidence sweeps",
			slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Any("error", err))
		return
	}
	logging.WithComponent("dispatch").Warn("GH-5376: stalled re-arm sweep escalated to pilot-needs-human after repeated no-evidence checks",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Int("streak", streak))
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
