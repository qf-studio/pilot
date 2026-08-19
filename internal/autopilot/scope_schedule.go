package autopilot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
	"github.com/robfig/cron/v3"
)

// trainPRSuffixRe matches a GitHub squash-merge PR reference suffix on a
// commit's first message line, e.g. "fix(x): do the thing (#123)" (GH-3993).
var trainPRSuffixRe = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// previousScheduledSearchWindow bounds how far back previousScheduledTime
// walks looking for a schedule's most recent fire time before a reference
// instant. Wide enough to cover any reasonable release-train cadence
// (weekly, monthly) — a narrower window (e.g. the on_scope_close
// ScopeLookback default of 24h) would miss the slot entirely for a weekly
// schedule before ever comparing it against the lookback eligibility gate
// in recoverMissedTrainTick (GH-3993).
const previousScheduledSearchWindow = 400 * 24 * time.Hour

// previousScheduledTime returns the most recent time at or before `before`
// that `schedule` would have fired, or the zero time if none falls within
// previousScheduledSearchWindow. robfig/cron/v3 only exposes Schedule.Next
// (forward), so this walks forward from the search-window floor and keeps
// the last hit at-or-before `before` — mirrors
// internal/briefs.Scheduler.maybeCatchUp's backward search
// (internal/briefs/scheduler.go ~:236-247), generalized to a wider fixed
// window instead of a hardcoded 48h (GH-3993).
func previousScheduledTime(schedule cron.Schedule, before time.Time) time.Time {
	checkTime := before.Add(-previousScheduledSearchWindow)
	var prev time.Time
	for {
		next := schedule.Next(checkTime)
		if next.After(before) {
			break
		}
		prev = next
		checkTime = next
	}
	return prev
}

// nextScheduledRunString computes the human-readable, timezone-aware next
// fire time for rel's cron Schedule relative to now. Used to render the
// on_schedule branch of releasePlanMessage's approval ack-card text
// (GH-4164). Mirrors startScheduleRelease's parser/timezone resolution so
// the ack card and the actual scheduler always agree on the next slot.
func nextScheduledRunString(rel *ReleaseConfig, now time.Time) (string, error) {
	loc := time.Local
	if rel.ScheduleTimezone != "" {
		l, err := time.LoadLocation(rel.ScheduleTimezone)
		if err != nil {
			return "", fmt.Errorf("invalid schedule_timezone %q: %w", rel.ScheduleTimezone, err)
		}
		loc = l
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(rel.Schedule)
	if err != nil {
		return "", fmt.Errorf("invalid schedule %q: %w", rel.Schedule, err)
	}

	next := schedule.Next(now.In(loc))
	return next.Format("2006-01-02 15:04 MST"), nil
}

// trainScopeKey renders the "train:<RFC3339>" scope key for a scheduled
// release-train tick at t. Normalized to UTC so the live-fire tick and a
// restart-recovered tick for the same slot always agree on the same key
// regardless of which time.Location representation computed t — the state
// store's INSERT OR IGNORE on this key is what makes a tick-then-recovery
// race for the same slot exactly-once (GH-3993).
func trainScopeKey(t time.Time) string {
	return "train:" + t.UTC().Format(time.RFC3339)
}

// startScheduleRelease starts the cron release-train scheduler for Trigger
// "on_schedule" (GH-3993). No-op unless resolvedRelease().ScheduleReleaseEnabled().
// Mirrors internal/briefs.Scheduler's cron.New/AddFunc/Start pattern
// (robfig/cron/v3), adapted to Controller's shutdown model: Controller has no
// separate Stop() call site — main.go's Run(ctx) exits on ctx.Done() instead
// — so the cron instance ties its own Stop to the same ctx via a watcher
// goroutine rather than a paired Stop method.
func (c *Controller) startScheduleRelease(ctx context.Context) {
	rel := c.resolvedRelease()
	if !rel.ScheduleReleaseEnabled() {
		return
	}

	loc := time.Local
	if rel.ScheduleTimezone != "" {
		l, err := time.LoadLocation(rel.ScheduleTimezone)
		if err != nil {
			c.log.Warn("startScheduleRelease: invalid schedule_timezone, using local timezone",
				"timezone", rel.ScheduleTimezone, "error", err)
		} else {
			loc = l
		}
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(rel.Schedule)
	if err != nil {
		c.log.Error("startScheduleRelease: invalid schedule, release train disabled",
			"schedule", rel.Schedule, "error", err)
		return
	}

	cronInst := cron.New(cron.WithLocation(loc))
	entryID, err := cronInst.AddFunc(rel.Schedule, func() {
		scheduledAt := previousScheduledTime(schedule, time.Now().In(loc))
		c.scheduleReleaseTickWithRetry(ctx, scheduledAt)
	})
	if err != nil {
		c.log.Error("startScheduleRelease: failed to register cron job",
			"schedule", rel.Schedule, "error", err)
		return
	}
	cronInst.Start()

	c.log.Info("release train scheduler started",
		"schedule", rel.Schedule,
		"timezone", loc.String(),
		"next_run", cronInst.Entry(entryID).Next,
	)

	go func() {
		<-ctx.Done()
		stopCtx := cronInst.Stop()
		<-stopCtx.Done()
	}()

	c.recoverMissedTrainTick(ctx, rel, schedule, loc)
}

// trainRecoveryDecision is recoverMissedTrainTick's fire/skip verdict plus
// the reasoning that produced it (GH-4982 acceptance criterion: one log
// line must be enough to diagnose this class of bug).
type trainRecoveryDecision struct {
	Fire   bool
	Reason string
}

// decideTrainRecovery judges whether a boot-time recovery pass should fire
// the release train tick scheduled for scheduledAt, given lastReleaseAt (the
// timestamp of the most recent release actually cut for the repo, or the
// zero time if none). Pure function of its three inputs so the boot/tick/
// release orderings are unit-testable without a state store or GitHub
// client (GH-4982).
//
// GH-4982: two cooperating live defects motivated this gate. (1) A boot
// restart ran recovery for a tick whose scheduled time hadn't arrived yet
// and cut a release 15 minutes before the tick it claimed to recover — a
// recovery pass must never consider a tick still in the future relative to
// now. (2) A later restart, after the tick had genuinely passed with
// nothing released for it, apparently treated a nearby-but-earlier release
// as satisfying the tick and never fired recovery — a release cut before
// the scheduled time does not satisfy that tick, however close the two
// timestamps are.
//
// The rule: fire iff the tick is actually in the past (scheduledAt <= now)
// AND the last release for the repo predates it (lastReleaseAt is zero, or
// strictly before scheduledAt).
func decideTrainRecovery(now, scheduledAt, lastReleaseAt time.Time) trainRecoveryDecision {
	if scheduledAt.After(now) {
		return trainRecoveryDecision{
			Fire:   false,
			Reason: "scheduled tick is in the future relative to now",
		}
	}
	if lastReleaseAt.IsZero() {
		return trainRecoveryDecision{
			Fire:   true,
			Reason: "no prior release found for this repo",
		}
	}
	if !lastReleaseAt.Before(scheduledAt) {
		return trainRecoveryDecision{
			Fire:   false,
			Reason: "last release already covers this tick",
		}
	}
	return trainRecoveryDecision{
		Fire:   true,
		Reason: "last release predates the scheduled tick",
	}
}

// formatOptionalTime renders t as RFC3339, or "none" for the zero time —
// used to keep recoverMissedTrainTick's decision log line readable when no
// prior release exists to compare against.
func formatOptionalTime(t time.Time) string {
	if t.IsZero() {
		return "none"
	}
	return t.Format(time.RFC3339)
}

// recoverMissedTrainTick runs the release-train tick once, immediately, if
// the daemon was offline across a scheduled fire and the missed slot is
// still within rel.ScopeLookback. A miss older than the lookback is left for
// a human to manually re-trigger (documented in configs/pilot.example.yaml)
// rather than silently tagging a long-stale train — mirrors the
// lookback-gated backstop precedent in reconcileLabelScope
// (internal/autopilot/scope_reconcile.go) (GH-3993).
//
// Beyond the lookback and existing-scope-row checks, the fire/skip verdict
// itself is decided by decideTrainRecovery against the repo's actual last
// release time (GH-4982) rather than trusting the scope row alone — see
// decideTrainRecovery's doc comment for the two live defects this closes.
func (c *Controller) recoverMissedTrainTick(ctx context.Context, rel *ReleaseConfig, schedule cron.Schedule, loc *time.Location) {
	now := time.Now()
	prevScheduled := previousScheduledTime(schedule, now.In(loc))
	if prevScheduled.IsZero() {
		return
	}

	lookback := rel.ScopeLookback
	if lookback <= 0 {
		lookback = 24 * time.Hour
	}
	if time.Since(prevScheduled) > lookback {
		c.log.Info("recoverMissedTrainTick: skipping recovery — previous scheduled tick predates the lookback window",
			"repo", c.repoKey(), "scheduled_at", prevScheduled.Format(time.RFC3339), "lookback", lookback, "gate", "lookback")
		return
	}

	scopeKey := trainScopeKey(prevScheduled)
	if c.stateStore != nil {
		row, err := c.stateStore.GetScopeRelease(c.repoKey(), scopeKey)
		if err != nil {
			c.log.Warn("recoverMissedTrainTick: failed to check for an existing train row, skipping recovery",
				"repo", c.repoKey(), "scope", scopeKey, "error", err, "gate", "row-lookup-error")
			return
		}
		if row != nil {
			// Already enqueued — either the tick fired on schedule, or a prior
			// recovery pass already ran for this slot. GH-4989: this was the
			// one skip that mattered in the #4982 misdiagnosis (08-18 15:11Z)
			// and it logged nothing, so the incident was misread as "recovery
			// never ran" — always log the row state that suppressed recovery.
			c.log.Info("recoverMissedTrainTick: skipping recovery — a row already exists for this scheduled slot",
				"repo", c.repoKey(), "scope", scopeKey, "row_state", row.State, "row_tag", row.Tag, "gate", "row-exists")
			return
		}
	}

	lastReleaseAt, err := c.releaser.GetLastReleaseTime(ctx, c.owner, c.repo)
	if err != nil {
		c.log.Warn("recoverMissedTrainTick: failed to determine last release time, skipping recovery",
			"repo", c.repoKey(), "scheduled_at", prevScheduled.Format(time.RFC3339), "error", err, "gate", "last-release-lookup-error")
		return
	}

	decision := decideTrainRecovery(now, prevScheduled, lastReleaseAt)
	logArgs := []any{
		"repo", c.repoKey(),
		"scheduled_at", prevScheduled.Format(time.RFC3339),
		"last_release_at", formatOptionalTime(lastReleaseAt),
		"verdict", decision.Reason,
	}
	if !decision.Fire {
		c.log.Info("recoverMissedTrainTick: skipping recovery — last-release gate", append(logArgs, "gate", "last-release")...)
		return
	}

	c.log.Warn("recovering missed train", logArgs...)
	c.scheduleReleaseTickWithRetry(ctx, prevScheduled)
}

// releaseTickOutcome classifies a scheduleReleaseTick result so
// scheduleReleaseTickWithRetry knows whether to retry (GH-4476): a transient
// GitHub API failure (releaseTickFailed) is retried, while success and a
// legitimate no-op (nothing merged, empty train, no resolvable member PRs)
// are both terminal and must not be retried.
type releaseTickOutcome int

const (
	releaseTickSucceeded releaseTickOutcome = iota
	releaseTickSkipped
	releaseTickFailed
)

// scheduleReleaseTick batches everything merged since the last tag into one
// scope-release carrier for Trigger "on_schedule" (GH-3993). scheduledAt is
// the cron-scheduled fire time (not the actual wall-clock time this call
// runs) — used verbatim as the "train:<RFC3339>" scope key so a live tick
// and a restart-recovered tick for the same slot always resolve to the same
// key; enqueueScopeRelease's INSERT OR IGNORE then makes a double-fire for
// the same slot exactly-once.
//
// Returns releaseTickFailed (with the triggering error) for a transient
// GitHub API failure so scheduleReleaseTickWithRetry can retry the tick
// instead of forfeiting the day's release train (GH-4476).
func (c *Controller) scheduleReleaseTick(ctx context.Context, scheduledAt time.Time) (releaseTickOutcome, error) {
	rel := c.resolvedRelease()
	branch := c.resolveMainBranchName()

	hasTag, err := c.repoHasAnyTag(ctx, c.owner, c.repo)
	if err != nil {
		c.log.Warn("scheduleReleaseTick: failed to check for an existing tag, skipping this tick", "error", err)
		return releaseTickFailed, err
	}

	var memberPRs []int
	if !hasTag {
		// First release: no tag exists yet, so CompareCommits against a
		// synthesized "v0.0.0" ref 404s every tick forever — that ref was
		// never created (GH-4174). The merged-PR list is already the
		// definitive member set for this case, so source it directly instead
		// of parsing "(#N)" squash-title suffixes off a CompareCommits range
		// that doesn't exist yet.
		memberPRs, err = c.firstReleaseTrainMembers(ctx, c.owner, c.repo)
		if err != nil {
			c.log.Warn("scheduleReleaseTick: failed to list merged PRs for first release, skipping this tick", "error", err)
			return releaseTickFailed, err
		}
		if len(memberPRs) == 0 {
			c.log.Debug("scheduleReleaseTick: no merged PRs yet, skipping first release",
				"scheduled_at", scheduledAt.Format(time.RFC3339))
			return releaseTickSkipped, nil
		}
		c.log.Info("scheduleReleaseTick: first release train — no prior tag, releasing entire merged history",
			"repo", fmt.Sprintf("%s/%s", c.owner, c.repo),
			"members", memberPRs,
			"scheduled_at", scheduledAt.Format(time.RFC3339),
		)
	} else {
		currentVersion, verErr := c.releaser.GetCurrentVersionForRepo(ctx, c.owner, c.repo)
		if verErr != nil {
			c.log.Warn("scheduleReleaseTick: failed to get current version, skipping this tick", "error", verErr)
			return releaseTickFailed, verErr
		}
		lastTag := currentVersion.String(rel.TagPrefix)

		commits, cmpErr := c.ghClient.CompareCommits(ctx, c.owner, c.repo, lastTag, branch)
		if cmpErr != nil {
			c.log.Warn("scheduleReleaseTick: failed to compare commits, skipping this tick",
				"last_tag", lastTag, "branch", branch, "error", cmpErr)
			return releaseTickFailed, cmpErr
		}
		if len(commits) == 0 {
			c.log.Debug("scheduleReleaseTick: empty train, skipping",
				"last_tag", lastTag, "scheduled_at", scheduledAt.Format(time.RFC3339))
			return releaseTickSkipped, nil
		}

		memberPRs = c.resolveTrainMemberPRs(ctx, commits)
		if len(memberPRs) == 0 {
			c.log.Warn("scheduleReleaseTick: no resolvable member PRs (direct-commit-only train), skipping — "+
				"v1 limitation, the scope-release carrier requires a real merged PR",
				"last_tag", lastTag, "commits", len(commits), "scheduled_at", scheduledAt.Format(time.RFC3339))
			return releaseTickSkipped, nil
		}
	}

	scopeKey := trainScopeKey(scheduledAt)
	title := fmt.Sprintf("Release train %s", scheduledAt.Format("2006-01-02 15:04"))
	c.enqueueScopeRelease(ctx, scopeKey, title, memberPRs)
	return releaseTickSucceeded, nil
}

// releaseTickRetryMinInterval, releaseTickRetryMaxInterval, and
// releaseTickRetryWindow bound scheduleReleaseTickWithRetry's backoff loop
// (GH-4476): short enough between attempts that a transient outage doesn't
// cost the release train its whole day, long enough not to hammer an
// already-rate-limited API, and bounded overall so a permanently-broken tick
// still gives up and alerts instead of retrying forever. Package-level vars
// (not consts) so tests can shrink them instead of a real test run waiting
// out real minutes/hours — see scope_schedule_retry_test.go.
var (
	// releaseTickRetryMinInterval is the default wait between retry attempts
	// absent a rate-limit error's own Retry-After/X-RateLimit-Reset delay.
	releaseTickRetryMinInterval = 15 * time.Minute
	// releaseTickRetryMaxInterval caps any single retry wait, including one
	// driven by a rate-limit header, so a runaway header value can't stall
	// the loop for hours between attempts.
	releaseTickRetryMaxInterval = 30 * time.Minute
	// releaseTickRetryWindow bounds how long past the scheduled fire time
	// the loop keeps retrying before giving up and firing a
	// release_tick_failed alert. GH-4476: the 2026-07-18 16:00 Europe/Berlin
	// tick hit a GitHub 403 and the train simply skipped the day with no
	// retry at all — this window gives a same-day transient failure (rate
	// limit, 5xx, network blip) room to clear before conceding the day.
	releaseTickRetryWindow = 6 * time.Hour
)

// scheduleReleaseTickWithRetry runs scheduleReleaseTick and, on a transient
// GitHub failure, retries with backoff for up to releaseTickRetryWindow past
// scheduledAt instead of forfeiting the train until the next scheduled day
// (GH-4476). The retry loop runs in its own goroutine so the cron callback
// and recoverMissedTrainTick's synchronous call both return immediately;
// ctx cancellation (daemon shutdown) stops the loop without firing the
// exhausted-retries alert, since a restart already re-attempts the tick via
// recoverMissedTrainTick.
func (c *Controller) scheduleReleaseTickWithRetry(ctx context.Context, scheduledAt time.Time) {
	outcome, err := c.scheduleReleaseTick(ctx, scheduledAt)
	if outcome != releaseTickFailed {
		return
	}
	go c.retryReleaseTick(ctx, scheduledAt, err)
}

// retryReleaseTick is scheduleReleaseTickWithRetry's backoff loop. Every
// attempt's wait is releaseTickRetryMinInterval..releaseTickRetryMaxInterval,
// preferring a rate-limit error's own Retry-After/X-RateLimit-Reset delay
// (clamped to the same bounds) over the default interval so the retry
// actually lands after the reported quota reset instead of guessing.
func (c *Controller) retryReleaseTick(ctx context.Context, scheduledAt time.Time, firstErr error) {
	deadline := scheduledAt.Add(releaseTickRetryWindow)
	lastErr := firstErr
	attempts := 1

	for time.Now().Before(deadline) {
		wait := releaseTickRetryMinInterval
		var rlErr *github.RateLimitError
		if errors.As(lastErr, &rlErr) && rlErr.RetryAfter > releaseTickRetryMinInterval {
			wait = rlErr.RetryAfter
		}
		if wait > releaseTickRetryMaxInterval {
			wait = releaseTickRetryMaxInterval
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		attempts++
		outcome, err := c.scheduleReleaseTick(ctx, scheduledAt)
		if outcome != releaseTickFailed {
			if outcome == releaseTickSucceeded {
				c.log.Info("scheduleReleaseTick: retry succeeded after a transient failure",
					"scheduled_at", scheduledAt.Format(time.RFC3339), "attempts", attempts)
			}
			return
		}
		lastErr = err
		c.log.Warn("scheduleReleaseTick: retry attempt failed, will retry",
			"scheduled_at", scheduledAt.Format(time.RFC3339), "attempts", attempts, "error", err)
	}

	c.fireReleaseTickFailedAlert(scheduledAt, lastErr, attempts)
}

// fireReleaseTickFailedAlert fires a loud release_tick_failed alert (GH-4476)
// once scheduleReleaseTickWithRetry has exhausted releaseTickRetryWindow of
// retries without a successful (or legitimately-skipped) result. Mirrors
// fireReleaseMissingAlert's alerts-engine-or-log-ERROR pattern so an
// exhausted retry surfaces loudly instead of silently skipping the day's
// release train the way the pre-GH-4476 bug did.
func (c *Controller) fireReleaseTickFailedAlert(scheduledAt time.Time, lastErr error, attempts int) {
	msg := fmt.Sprintf(
		"release train tick scheduled for %s in %s/%s failed after %d attempt(s) over %s: %v — the release train did not run this cycle",
		scheduledAt.Format(time.RFC3339), c.owner, c.repo, attempts, releaseTickRetryWindow, lastErr,
	)
	c.log.Error("scheduleReleaseTick: exhausted retries, giving up on this tick",
		"scheduled_at", scheduledAt.Format(time.RFC3339), "attempts", attempts, "error", lastErr,
	)

	if c.alertsEngine == nil {
		c.log.Error("release_tick_failed alert not delivered: SetAlertsEngine was never called",
			"scheduled_at", scheduledAt.Format(time.RFC3339))
		return
	}
	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventType("release_tick_failed"),
		Error:     msg,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":         c.owner + "/" + c.repo,
			"scheduled_at": scheduledAt.Format(time.RFC3339),
			"attempts":     strconv.Itoa(attempts),
		},
	})
}

// repoHasAnyTag reports whether owner/repo has at least one published
// release or git tag. A repo synthesizes lastTag = tagPrefix + "0.0.0" when
// GetCurrentVersionForRepo finds nothing to parse, but that string was never
// created as an actual ref — CompareCommits against it 404s. This checks ref
// existence directly rather than inferring it from a zero SemVer, since a
// lookup failure also collapses to the same zero value (GH-4174).
func (c *Controller) repoHasAnyTag(ctx context.Context, owner, repo string) (bool, error) {
	release, err := c.ghClient.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	if release != nil {
		return true, nil
	}
	tags, err := c.ghClient.ListTags(ctx, owner, repo, 1)
	if err != nil {
		return false, err
	}
	return len(tags) > 0, nil
}

// firstReleaseTrainMembers lists every merged PR in owner/repo as the member
// set for a repo's first scheduled release train (GH-4174). Unlike
// resolveTrainMemberPRs, this doesn't parse "(#N)" squash-title suffixes off
// commit messages — those suffixes only ever land on the squashed commit
// CompareCommits would return, and with no prior tag there is no
// CompareCommits range to source that list from yet. The GitHub list-PRs
// endpoint doesn't populate the `merged` boolean (only `merged_at`, unlike
// the single-PR GET resolveTrainMemberPRs uses), so MergedAt is the signal
// for "actually merged" vs. "closed without merging".
func (c *Controller) firstReleaseTrainMembers(ctx context.Context, owner, repo string) ([]int, error) {
	prs, err := c.ghClient.ListPullRequests(ctx, owner, repo, "closed")
	if err != nil {
		return nil, err
	}
	var members []int
	for _, pr := range prs {
		if pr == nil || pr.MergedAt == "" {
			continue
		}
		members = append(members, pr.Number)
	}
	return dedupeSortInts(members), nil
}

// resolveTrainMemberPRs extracts "(#N)" squash-merge PR references from each
// commit's first message line, dedupes, and keeps only numbers that resolve
// to an actual merged PR. An unverifiable number (deleted PR, a direct
// commit with no PR, a non-squash merge commit) is dropped from the member
// list — its commit still counts toward the train via the CompareCommits
// result in scheduleReleaseTick, but it can't anchor a carrier or contribute
// release-notes attribution (GH-3993).
func (c *Controller) resolveTrainMemberPRs(ctx context.Context, commits []*github.Commit) []int {
	seen := make(map[int]bool)
	var candidates []int
	for _, commit := range commits {
		if commit == nil {
			continue
		}
		firstLine := strings.SplitN(commit.Commit.Message, "\n", 2)[0]
		m := trainPRSuffixRe.FindStringSubmatch(firstLine)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		candidates = append(candidates, n)
	}

	var members []int
	for _, n := range candidates {
		pr, err := c.ghClient.GetPullRequest(ctx, c.owner, c.repo, n)
		if err != nil {
			c.log.Debug("resolveTrainMemberPRs: failed to verify candidate PR, dropping from members",
				"pr", n, "error", err)
			continue
		}
		if !pr.Merged {
			c.log.Debug("resolveTrainMemberPRs: candidate PR is not merged, dropping from members", "pr", n)
			continue
		}
		members = append(members, n)
	}
	return dedupeSortInts(members)
}

// trainReleaseCommits returns the commit set for a "train:" scope carrier's
// release: everything reachable from HeadSHA since the last tag. Unlike
// scopeReleaseCommits (epic:/label: scopes, which union each member PR's own
// commits), a release train is defined as "everything since the last tag" —
// so it normally reads directly off CompareCommits rather than the member
// list, which exists only for release-notes attribution (GH-3993).
//
// First release (no tag exists yet): lastTag has no corresponding ref to
// compare against, so CompareCommits would 404 (GH-4174) — fall back to
// scopeReleaseCommits' member-PR commit union instead, using the member list
// scheduleReleaseTick already resolved via firstReleaseTrainMembers.
func (c *Controller) trainReleaseCommits(ctx context.Context, owner, repo string, prState *PRState, currentVersion SemVer, rel *ReleaseConfig) ([]*github.Commit, error) {
	hasTag, err := c.repoHasAnyTag(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if !hasTag {
		return c.scopeReleaseCommits(ctx, owner, repo, prState, currentVersion, rel)
	}
	lastTag := currentVersion.String(rel.TagPrefix)
	return c.ghClient.CompareCommits(ctx, owner, repo, lastTag, prState.HeadSHA)
}
