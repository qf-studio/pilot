package autopilot

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

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
		c.scheduleReleaseTick(ctx, scheduledAt)
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

// recoverMissedTrainTick runs the release-train tick once, immediately, if
// the daemon was offline across a scheduled fire and the missed slot is
// still within rel.ScopeLookback. A miss older than the lookback is left for
// a human to manually re-trigger (documented in configs/pilot.example.yaml)
// rather than silently tagging a long-stale train — mirrors the
// lookback-gated backstop precedent in reconcileLabelScope
// (internal/autopilot/scope_reconcile.go) (GH-3993).
func (c *Controller) recoverMissedTrainTick(ctx context.Context, rel *ReleaseConfig, schedule cron.Schedule, loc *time.Location) {
	prevScheduled := previousScheduledTime(schedule, time.Now().In(loc))
	if prevScheduled.IsZero() {
		return
	}

	lookback := rel.ScopeLookback
	if lookback <= 0 {
		lookback = 24 * time.Hour
	}
	if time.Since(prevScheduled) > lookback {
		c.log.Debug("recoverMissedTrainTick: previous scheduled tick predates the lookback window, skipping",
			"scheduled_at", prevScheduled.Format(time.RFC3339), "lookback", lookback)
		return
	}

	scopeKey := trainScopeKey(prevScheduled)
	if c.stateStore != nil {
		row, err := c.stateStore.GetScopeRelease(c.repoKey(), scopeKey)
		if err != nil {
			c.log.Warn("recoverMissedTrainTick: failed to check for an existing train row, skipping recovery",
				"scope", scopeKey, "error", err)
			return
		}
		if row != nil {
			// Already enqueued — either the tick fired on schedule, or a prior
			// recovery pass already ran for this slot.
			return
		}
	}

	c.log.Warn("recovering missed train", "scheduled_at", prevScheduled.Format(time.RFC3339))
	c.scheduleReleaseTick(ctx, prevScheduled)
}

// scheduleReleaseTick batches everything merged since the last tag into one
// scope-release carrier for Trigger "on_schedule" (GH-3993). scheduledAt is
// the cron-scheduled fire time (not the actual wall-clock time this call
// runs) — used verbatim as the "train:<RFC3339>" scope key so a live tick
// and a restart-recovered tick for the same slot always resolve to the same
// key; enqueueScopeRelease's INSERT OR IGNORE then makes a double-fire for
// the same slot exactly-once.
func (c *Controller) scheduleReleaseTick(ctx context.Context, scheduledAt time.Time) {
	rel := c.resolvedRelease()
	branch := c.resolveMainBranchName()

	currentVersion, err := c.releaser.GetCurrentVersionForRepo(ctx, c.owner, c.repo)
	if err != nil {
		c.log.Warn("scheduleReleaseTick: failed to get current version, defaulting to 0.0.0", "error", err)
		currentVersion = SemVer{}
	}
	lastTag := currentVersion.String(rel.TagPrefix)

	commits, err := c.ghClient.CompareCommits(ctx, c.owner, c.repo, lastTag, branch)
	if err != nil {
		c.log.Warn("scheduleReleaseTick: failed to compare commits, skipping this tick",
			"last_tag", lastTag, "branch", branch, "error", err)
		return
	}
	if len(commits) == 0 {
		c.log.Debug("scheduleReleaseTick: empty train, skipping",
			"last_tag", lastTag, "scheduled_at", scheduledAt.Format(time.RFC3339))
		return
	}

	memberPRs := c.resolveTrainMemberPRs(ctx, commits)
	if len(memberPRs) == 0 {
		c.log.Warn("scheduleReleaseTick: no resolvable member PRs (direct-commit-only train), skipping — "+
			"v1 limitation, the scope-release carrier requires a real merged PR",
			"last_tag", lastTag, "commits", len(commits), "scheduled_at", scheduledAt.Format(time.RFC3339))
		return
	}

	scopeKey := trainScopeKey(scheduledAt)
	title := fmt.Sprintf("Release train %s", scheduledAt.Format("2006-01-02 15:04"))
	c.enqueueScopeRelease(ctx, scopeKey, title, memberPRs)
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
// so it always reads directly off CompareCommits rather than the member
// list, which exists only for release-notes attribution (GH-3993).
func (c *Controller) trainReleaseCommits(ctx context.Context, owner, repo string, prState *PRState, currentVersion SemVer, rel *ReleaseConfig) ([]*github.Commit, error) {
	lastTag := currentVersion.String(rel.TagPrefix)
	return c.ghClient.CompareCommits(ctx, owner, repo, lastTag, prState.HeadSHA)
}
