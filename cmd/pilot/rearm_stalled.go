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

	var kind string
	var evidenceAt time.Time
	if rearmEvent := latestRearmEvent(events, *exec.CompletedAt, append([]string{c.triggerLabel}, retryReadyRearmLabels...)...); rearmEvent != nil {
		kind, evidenceAt = rearmEvent.Event, rearmEvent.CreatedAt
	} else {
		// GH-5381: no label/reopen evidence found via the classic Events API
		// — fall back to the Timeline API, the only endpoint that surfaces a
		// body/title "edited" event at all (confirmed during the GH-5376
		// incident; the classic Events API never emits one). This is
		// deliberately NOT issue.UpdatedAt (PR #5380's approach, reverted in
		// f8532806): UpdatedAt moves on ANY mutation, including the
		// dispatcher's own stall comment + pilot-blocked label
		// (surfaceStalledIssue, internal/executor/dispatcher.go), so it
		// re-armed every stalled task on the very next sweep. An "edited"
		// timeline event authored by anyone other than the bot itself is a
		// much narrower, deliberate-operator-gesture signal.
		timeline, timelineErr := c.ghClient.ListIssueTimeline(ctx, c.repoOwner, c.repoName, issueNum)
		if timelineErr != nil {
			return false, fmt.Errorf("listing issue #%d timeline: %w", issueNum, timelineErr)
		}
		// Only spend a GetAuthenticatedUser call resolving the bot login
		// (resolveBotLogin) when there's at least one "edited" event that
		// could possibly qualify — the common case (no edit at all since the
		// stall) has nothing to filter by actor, so this skips an API call
		// on every ordinary no-evidence sweep pass.
		if hasEditedEventAfter(timeline, *exec.CompletedAt) {
			botLogin := resolveBotLogin(ctx, c.ghClient)
			if at, ok := stalledTimelineRearmEvidence(timeline, *exec.CompletedAt, botLogin); ok {
				kind, evidenceAt = "edited", at
			}
		}
	}
	if kind == "" {
		// Open + labeled, but nothing observed shows that state was reached
		// AFTER the stall (no base-label relabel/reopen, no
		// pilot-retry-ready/-1/-2 label add, no non-bot body/title edit) —
		// not a deliberate re-arm gesture.
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

// hasEditedEventAfter reports whether events contains any "edited" entry
// timestamped after since, with no actor filtering — a cheap pre-check so
// tryRearmStalled only pays for resolveBotLogin's GetAuthenticatedUser call
// when there's actually something that could qualify as evidence.
func hasEditedEventAfter(events []*github.TimelineEvent, since time.Time) bool {
	for _, ev := range events {
		if ev != nil && ev.Event == "edited" && ev.CreatedAt.After(since) {
			return true
		}
	}
	return false
}

// stalledTimelineRearmEvidence is GH-5381's real body-edit evidence check.
// GitHub's classic Events API never emits an "edited" event for a body/title
// edit (the GH-5376 incident and the now-reverted PR #5380 confirmed this —
// #5380 substituted issue.UpdatedAt instead, which broke worse: the
// dispatcher's OWN stall comment + pilot-blocked label bump UpdatedAt
// seconds after CompletedAt, so every stalled task re-armed itself on the
// very next sweep — see commit f8532806's revert). The Timeline API DOES
// surface "edited" events, each with an actor, so a genuine operator body
// edit can be told apart from the bot's own activity by actor login alone —
// no timestamp heuristic needed.
//
// botLogin is the authenticated token identity (resolveBotLogin, cached); an
// edited event whose actor matches it (case-insensitive) is excluded — it's
// Pilot's own doing (e.g. an autopilot-meta footer rewrite), never a
// deliberate operator re-arm gesture. An empty botLogin (resolution failed)
// accepts NO edited-event evidence this pass — fail closed, since accepting
// one without an actor filter risks reintroducing exactly the self-re-arm
// bug this function exists to avoid. Label/reopen evidence (checked first,
// unconditionally, in tryRearmStalled) is unaffected by this fallback.
func stalledTimelineRearmEvidence(events []*github.TimelineEvent, since time.Time, botLogin string) (at time.Time, ok bool) {
	if botLogin == "" {
		return time.Time{}, false
	}
	var latest *github.TimelineEvent
	for _, ev := range events {
		if ev == nil || ev.Event != "edited" || !ev.CreatedAt.After(since) {
			continue
		}
		if ev.Actor != nil && strings.EqualFold(ev.Actor.Login, botLogin) {
			continue
		}
		if latest == nil || ev.CreatedAt.After(latest.CreatedAt) {
			latest = ev
		}
	}
	if latest == nil {
		return time.Time{}, false
	}
	return latest.CreatedAt, true
}

// botLoginMu/cachedBotLogin/botLoginResolved cache the authenticated GitHub
// login for the Pilot token, mirroring internal/autopilot.Controller's
// getBotLogin — package-level here because terminalCompletionChecker is a
// value type constructed fresh per call (see sweepStalledRearm's doc
// comment), so an instance-level cache would never survive across sweep
// passes.
var (
	botLoginMu       sync.Mutex
	cachedBotLogin   string
	botLoginResolved bool
)

// resolveBotLogin returns the authenticated GitHub login for client's token,
// resolved lazily on first call and cached for the process lifetime. Returns
// "" when it can't be determined (e.g. a transient GetAuthenticatedUser
// failure); callers must treat that as "exclude nothing can be confirmed
// safe" — see stalledTimelineRearmEvidence's fail-closed handling.
func resolveBotLogin(ctx context.Context, client *github.Client) string {
	botLoginMu.Lock()
	if botLoginResolved {
		login := cachedBotLogin
		botLoginMu.Unlock()
		return login
	}
	botLoginMu.Unlock()

	user, err := client.GetAuthenticatedUser(ctx)
	if err != nil {
		logging.WithComponent("dispatch").Warn("GH-5381: could not resolve bot login for edited-event actor exclusion — Timeline edited-event evidence disabled until this succeeds",
			slog.Any("error", err))
		return ""
	}
	botLoginMu.Lock()
	cachedBotLogin = user.Login
	botLoginResolved = true
	botLoginMu.Unlock()
	return user.Login
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
			// GH-5381: already escalated by escalateStalledRearmNoEvidence
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
			// GH-5381: a probe error (e.g. a persistent GitHub API outage)
			// must count toward the no-evidence streak exactly like an
			// ordinary "no evidence found" result — before this, only the
			// !rearmed branch below fed the streak, so a GitHub API error
			// repeating every sweep pass could recordClaimLostDrop forever
			// without ever reaching the escalation backstop.
			logging.WithComponent("dispatch").Warn("GH-5212 stalled re-arm probe failed",
				slog.String("task_id", taskID), slog.Any("error", err))
			c.recordStalledRearmMiss(ctx, taskID, issue.Number, backoffKey)
			continue
		}
		if rearmed {
			continue
		}
		c.recordStalledRearmMiss(ctx, taskID, issue.Number, backoffKey)
	}
}

// stalledRearmNoEvidenceTracker counts, per repickBackoffKey, how many
// consecutive sweepStalledRearm passes found no re-arm evidence for that key
// (GH-5381, carried forward from the reverted #5380) — a probe error and an
// ordinary "no evidence" result both count via recordStalledRearmMiss.
// Reset on any successful re-arm (tryRearmStalled returning true) or once
// escalateStalledRearmNoEvidence successfully applies pilot-needs-human —
// that label is what actually stops the sweep from revisiting the issue (see
// the candidate-filter check above); resetting the streak here just keeps
// this counter from drifting stale if the label is ever cleared without a
// genuine re-arm.
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

// recordStalledRearmMiss is sweepStalledRearm's shared "no evidence this
// pass" handler — reached both when tryRearmStalled returns a hard error
// (a GitHub API failure) and when it returns rearmed=false cleanly. Below
// terminalDropPilotStripThreshold (GH-5297's constant, reused here)
// consecutive misses, it behaves exactly like the pre-GH-5381 code:
// recordClaimLostDrop arms the ordinary repick-backoff cooldown. At or past
// the threshold, it escalates to an operator instead of growing the backoff
// window forever (GH-5346: 76 claim-lost drops over 33h with no signal,
// since claim_lost_drops is deliberately excluded from the dispatcher's
// hard-cap counter).
func (c terminalCompletionChecker) recordStalledRearmMiss(ctx context.Context, taskID string, issueNum int, backoffKey string) {
	streak := stalledRearmNoEvidenceStreaks.increment(backoffKey)
	if streak < terminalDropPilotStripThreshold {
		repickBackoff.recordClaimLostDrop(backoffKey)
		return
	}
	if c.escalateStalledRearmNoEvidence(ctx, taskID, issueNum, streak) {
		stalledRearmNoEvidenceStreaks.reset(backoffKey)
		return
	}
	// GH-5381: AddLabels failed — pilot-needs-human never landed, so the
	// sweep's candidate filter won't exclude this issue and would otherwise
	// silently resume ordinary claim-lost-drop backoff next pass. Leave the
	// streak at/above threshold so the very next sweep retries the
	// escalation (comment + label) instead of quietly falling back.
}

// escalateStalledRearmNoEvidence posts the explanatory comment and applies
// pilot-needs-human once sweepStalledRearm has found no re-arm evidence
// (probe error or clean no-evidence result alike) terminalDropPilotStripThreshold
// times in a row for the same task_id — the operator-signal backstop the
// GH-5346 incident (76 claim-lost drops, 33h, no signal) never had. Mirrors
// GH-5300's stripPilotLabelAndCommentSDK shape but applies pilot-needs-human
// instead of stripping the trigger label: the sweep's own candidate filter
// (in sweepStalledRearm above) already excludes pilot-needs-human issues, so
// applying it here is what makes this a one-shot escalation rather than a
// per-sweep-pass repeat, without touching pilot-blocked/pilot-retry-ready
// (which the documented re-arm recipe still expects to manipulate).
//
// Returns whether pilot-needs-human was successfully applied — callers must
// only reset the no-evidence streak on success (see recordStalledRearmMiss):
// on an AddLabels failure the escalation comment may already have posted,
// so a retried pass can end up posting a second explanatory comment, an
// accepted, visible cost against the alternative of a silently stuck
// escalation that the sweep never retries.
func (c terminalCompletionChecker) escalateStalledRearmNoEvidence(ctx context.Context, taskID string, issueNum int, streak int) bool {
	comment := fmt.Sprintf(
		"⚠️ **Pilot could not confirm this stalled task was re-armed**\n\n"+
			"This issue was checked %d times with no evidence of a deliberate re-arm gesture since it stalled. "+
			"Re-arm evidence is any of: re-adding the `%s` label, adding one of `%s`, reopening the issue, "+
			"or editing the issue body/title as a human (not Pilot itself) — all timestamped after the stall.\n\n"+
			"Applying `%s` so this stops being silently re-checked. Remove it and repeat one of the above to re-arm.",
		streak, c.triggerLabel, strings.Join(retryReadyRearmLabels, "`, `"), labelPilotNeedsHumanSDK,
	)
	if _, err := c.ghClient.AddComment(ctx, c.repoOwner, c.repoName, issueNum, comment); err != nil {
		logging.WithComponent("dispatch").Warn("GH-5381: failed to post stalled-no-evidence escalation comment",
			slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Any("error", err))
	}
	if err := c.ghClient.AddLabels(ctx, c.repoOwner, c.repoName, issueNum, []string{labelPilotNeedsHumanSDK}); err != nil {
		logging.WithComponent("dispatch").Warn("GH-5381: failed to apply pilot-needs-human after repeated no-evidence sweeps — will retry on the next sweep pass",
			slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Any("error", err))
		return false
	}
	logging.WithComponent("dispatch").Warn("GH-5381: stalled re-arm sweep escalated to pilot-needs-human after repeated no-evidence checks",
		slog.String("task_id", taskID), slog.Int("issue", issueNum), slog.Int("streak", streak))
	return true
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
