package autopilot

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// maxReleaseBackfillTags bounds how many of the most recent tags
// earliestReleaseTagContaining inspects. GitHub's tags endpoint returns
// newest-first and this reconciliation only ever targets recent release-train
// residue (GH-4370), so 100 (GitHub's per-page max, and ListTags's only
// supported page) comfortably covers the window without unbounded pagination.
const maxReleaseBackfillTags = 100

// releaseBackfillRowState is reconcileReleaseBackfill's (GH-4919) per-row
// backoff and consecutive-failure bookkeeping. Held in
// Controller.releaseBackfillRows, keyed by "owner/repo#pr".
type releaseBackfillRowState struct {
	// nextRetryAt is when this row becomes eligible for another sweep
	// attempt; releaseBackfillDue skips it (with zero API calls) until then.
	nextRetryAt time.Time
	// firstFailAt is when the current unbroken failure streak began — reset
	// whenever the streak is cleared by a success. The abandon threshold
	// requires the streak to also span releaseBackfillAbandonMinWindow since
	// this timestamp.
	firstFailAt time.Time
	// consecutiveFails counts API errors since the last success.
	consecutiveFails int
}

const (
	// releaseBackfillMaxBackoff caps the exponential per-row backoff below —
	// a wedged row's API calls can never exceed roughly this cadence,
	// regardless of how long its failure streak runs.
	releaseBackfillMaxBackoff = time.Hour
	// releaseBackfillAbandonThreshold is the consecutive-failure count (see
	// releaseBackfillRowState.consecutiveFails) required, together with
	// releaseBackfillAbandonMinWindow, before a row is classified permanently
	// failed.
	releaseBackfillAbandonThreshold = 10
	// releaseBackfillAbandonMinWindow is the minimum wall-clock span a
	// failure streak must cover before a row is classified permanently
	// failed — deliberately independent of the failure count so a same-day
	// platform incident (this fix's own motivating case, the 2026-08-17
	// GitHub compare-API degradation) cannot itself terminalize a row that
	// would have healed once the incident cleared.
	releaseBackfillAbandonMinWindow = 6 * time.Hour
)

// releaseBackfillRowKey builds the distinct-row identity used for backoff
// bookkeeping. Includes owner/repo (not just the PR number) since one
// controller's persisted rows can reference a foreign repo (RepoOwnerAndName)
// when the row was adopted cross-repo.
func releaseBackfillRowKey(owner, repo string, pr int) string {
	return fmt.Sprintf("%s/%s#%d", owner, repo, pr)
}

// releaseBackfillDue reports whether key is eligible for a sweep attempt
// right now. A row with no bookkeeping (never failed, or its streak was
// cleared by a prior success) is always due.
func (c *Controller) releaseBackfillDue(key string, now time.Time) bool {
	c.releaseBackfillMu.Lock()
	defer c.releaseBackfillMu.Unlock()
	st := c.releaseBackfillRows[key]
	if st == nil {
		return true
	}
	return !now.Before(st.nextRetryAt)
}

// releaseBackfillObserveSuccess clears key's failure streak and backoff. Any
// error-free API round for the row — healed or not (a genuinely unmerged PR
// is a successful, informative fetch, not a failure) — proves the row is
// currently reachable, so the streak that feeds both backoff and the abandon
// threshold resets (GH-4919).
func (c *Controller) releaseBackfillObserveSuccess(key string) {
	c.releaseBackfillMu.Lock()
	defer c.releaseBackfillMu.Unlock()
	delete(c.releaseBackfillRows, key)
}

// releaseBackfillObserveFailure records one API-error observation for key:
// advances its consecutive-failure streak and sets its next eligible retry
// time via exponential backoff from the poll interval, capped at
// releaseBackfillMaxBackoff. Reports whether the row has now crossed the
// permanent-failure threshold (releaseBackfillAbandonThreshold consecutive
// failures AND releaseBackfillAbandonMinWindow elapsed since the streak's
// first failure).
func (c *Controller) releaseBackfillObserveFailure(key string, now time.Time) (abandon bool, streak int) {
	c.releaseBackfillMu.Lock()
	defer c.releaseBackfillMu.Unlock()
	if c.releaseBackfillRows == nil {
		c.releaseBackfillRows = make(map[string]*releaseBackfillRowState)
	}
	st := c.releaseBackfillRows[key]
	if st == nil {
		st = &releaseBackfillRowState{firstFailAt: now}
		c.releaseBackfillRows[key] = st
	}
	st.consecutiveFails++

	base := c.config.CIPollInterval
	if base <= 0 {
		base = 30 * time.Second
	}
	backoff := base
	for i := 1; i < st.consecutiveFails && backoff < releaseBackfillMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > releaseBackfillMaxBackoff {
		backoff = releaseBackfillMaxBackoff
	}
	st.nextRetryAt = now.Add(backoff)

	abandon = st.consecutiveFails >= releaseBackfillAbandonThreshold &&
		now.Sub(st.firstFailAt) >= releaseBackfillAbandonMinWindow
	if abandon {
		// The persisted ReleaseBackfillAbandoned flag is now authoritative
		// for skipping this row — no need to keep the in-memory streak
		// around too.
		delete(c.releaseBackfillRows, key)
	}
	return abandon, st.consecutiveFails
}

// releaseBackfillFail is healReleaseBackfillRow's single call site for any
// API error: records the failure/backoff and, if it just crossed the
// permanent-failure threshold, persists prState as abandoned so every future
// sweep skips it with zero API calls — logged once, on the transition only
// (GH-4919).
func (c *Controller) releaseBackfillFail(key string, now time.Time, prState *PRState, cause error) {
	abandon, streak := c.releaseBackfillObserveFailure(key, now)
	if !abandon {
		return
	}

	prState.ReleaseBackfillAbandoned = true
	prState.Error = fmt.Sprintf(
		"release-backfill: abandoned after %d consecutive API failures spanning >= %s — last error: %v",
		streak, releaseBackfillAbandonMinWindow, cause)

	if c.stateStore != nil {
		if serr := c.stateStore.SavePRState(c.repoKey(), prState); serr != nil {
			c.log.Warn("reconcileReleaseBackfill: failed to persist abandoned row",
				"pr", prState.PRNumber, "error", serr)
			return
		}
	}
	c.log.Warn("reconcileReleaseBackfill: row abandoned — API failures exceeded threshold, will not be retried",
		"pr", prState.PRNumber, "consecutive_failures", streak, "error", cause)
}

// reconcileReleaseBackfill is GH-4370's periodic release-ledger reconciliation.
// A manual tag push (bypassing the automated release train entirely) leaves
// every PR physically contained in that release wedged in autopilot_pr_state
// at StageFailed or StageReleasing forever: RestoreState refuses to rehydrate
// StageFailed rows ("shouldn't be active"), and a scope carrier whose
// autopilot_scope_release row already resolved terminal is skipped too
// (GH-4331) — both classes are permanently invisible to the normal poll loop
// once orphaned. This sweep reads every persisted row directly from the state
// store (not c.activePRs, which the orphans were never rehydrated into) and
// heals any row whose PR did in fact merge and ship. Ground truth is git
// ancestry: a PR is released iff its merge commit is an ancestor of an
// existing release tag; the earliest such tag names the version.
//
// GH-4919: every tick unconditionally re-attempted every candidate row, even
// one whose lookups were erroring — during the 2026-08-17 GitHub platform
// incident this burned ~2.9k rate-limit hits in a day against the shared
// 5000/hr pool. Three guards now bound that cost: the platform-outage breaker
// (TASK-458) skips the whole sweep while a platform incident is suspected
// open; a per-row exponential backoff skips an individual erroring row
// without any API call until its next scheduled retry
// (releaseBackfillObserveFailure/releaseBackfillDue); and a row whose errors
// outlive the abandon threshold is persisted as permanently failed
// (PRState.ReleaseBackfillAbandoned) and skipped by every future sweep.
func (c *Controller) reconcileReleaseBackfill(ctx context.Context) {
	if c.stateStore == nil || c.ghClient == nil {
		return
	}
	if c.platformBreaker.IsOpen() {
		c.log.Debug("reconcileReleaseBackfill: skipping sweep — platform-outage breaker open")
		return
	}
	states, err := c.stateStore.LoadAllPRStates(c.repoKey())
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: failed to load PR states", "error", err)
		return
	}
	now := time.Now()
	if c.releaseBackfillClock != nil {
		now = c.releaseBackfillClock()
	}
	for _, prState := range states {
		if prState.Stage != StageFailed && prState.Stage != StageReleasing {
			continue
		}
		if prState.ReleaseBackfillAbandoned {
			continue
		}
		owner, repo := prState.RepoOwnerAndName(c.owner, c.repo)
		key := releaseBackfillRowKey(owner, repo, prState.PRNumber)
		if !c.releaseBackfillDue(key, now) {
			continue
		}
		c.healReleaseBackfillRow(ctx, prState, key, now)
	}
}

// healReleaseBackfillRow resolves prState's live merge status and tag
// ancestry and, on a confirmed release match, backfills the execution ladder
// and drains the residue row — mirroring exactly what a successful
// handleReleasing does on the normal path, without re-running any of the
// tag-creation logic (the tag already exists; this is bookkeeping, not a new
// release). key/now are reconcileReleaseBackfill's backoff-bookkeeping
// identity and clock reading for this row (GH-4919).
func (c *Controller) healReleaseBackfillRow(ctx context.Context, prState *PRState, key string, now time.Time) {
	owner, repo := prState.RepoOwnerAndName(c.owner, c.repo)

	ghPR, err := c.ghClient.GetPullRequest(ctx, owner, repo, prState.PRNumber)
	if err != nil {
		c.log.Debug("reconcileReleaseBackfill: failed to fetch PR, skipping",
			"pr", prState.PRNumber, "error", err)
		c.releaseBackfillFail(key, now, prState, err)
		return
	}
	if !ghPR.Merged || ghPR.MergeCommitSHA == "" {
		// Genuinely unreleased (or never merged) — leave the row exactly as
		// is. The fetch itself succeeded, so this clears any prior streak.
		c.releaseBackfillObserveSuccess(key)
		return
	}

	tag, err := c.earliestReleaseTagContaining(ctx, owner, repo, ghPR.MergeCommitSHA)
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: tag ancestry lookup failed",
			"pr", prState.PRNumber, "sha", ShortSHA(ghPR.MergeCommitSHA), "error", err)
		c.releaseBackfillFail(key, now, prState, err)
		return
	}
	c.releaseBackfillObserveSuccess(key)
	if tag == "" {
		// Merged, but not yet covered by any tag — genuinely unreleased.
		return
	}

	previousStage := prState.Stage
	c.recordReleaseBackfillEvent(prState, ghPR.MergeCommitSHA, tag)

	prState.ReleaseVersion = tag
	prState.HeadSHA = ghPR.MergeCommitSHA
	if prState.ScopeKey != "" {
		c.markScopeReleaseDone(prState, tag)
	}
	c.removePR(prState.PRNumber)

	c.log.Info("reconcileReleaseBackfill: healed merged-but-unreleased residue row",
		"pr", prState.PRNumber, "stage_was", previousStage, "version", tag,
		"sha", ShortSHA(ghPR.MergeCommitSHA))
}

// recordReleaseBackfillEvent writes the released execution event for
// prState, unless one is already recorded. Idempotent so a repeat sweep — or
// a crash between this write and the row-drain in healReleaseBackfillRow —
// never double-stamps the ladder (GH-4370, mirrors GH-4277's heal semantics:
// this only appends the missing terminal event, it never re-stamps the
// execution row's own timestamps to "now").
func (c *Controller) recordReleaseBackfillEvent(prState *PRState, mergeSHA, tag string) {
	if c.memoryStore == nil {
		return
	}
	taskID := fmt.Sprintf("GH-%d", prState.IssueNumber)
	if prState.IssueNumber == 0 {
		taskID = fmt.Sprintf("PR-%d", prState.PRNumber)
	}
	exec, err := c.memoryStore.GetLatestExecutionByTaskID(taskID, c.projectPath)
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: no execution row for task, skipping event",
			"pr", prState.PRNumber, "task_id", taskID, "error", err)
		return
	}
	already, err := c.memoryStore.HasExecutionEventStage(exec.ID, memory.StageReleased)
	if err != nil {
		c.log.Warn("reconcileReleaseBackfill: failed to check existing released event",
			"pr", prState.PRNumber, "execution_id", exec.ID, "error", err)
		return
	}
	if already {
		return
	}
	detail := fmt.Sprintf("release-backfill (GH-4370): pr #%d merge commit %s found in tag %s (manual tag push bypassed the release train)",
		prState.PRNumber, ShortSHA(mergeSHA), tag)
	if err := c.memoryStore.RecordExecutionEvent(exec.ID, memory.StageReleased, detail); err != nil {
		c.log.Warn("reconcileReleaseBackfill: failed to record released event",
			"pr", prState.PRNumber, "execution_id", exec.ID, "error", err)
	}
}

// earliestReleaseTagContaining returns the earliest (lowest-semver) release
// tag whose history contains sha, or "" if no tag among the most recent
// maxReleaseBackfillTags contains it. The earliest tag is the one that
// actually shipped the commit — a later tag also contains it by
// transitivity, but naming that one would misreport when the work released.
func (c *Controller) earliestReleaseTagContaining(ctx context.Context, owner, repo, sha string) (string, error) {
	tags, err := c.ghClient.ListTags(ctx, owner, repo, maxReleaseBackfillTags)
	if err != nil {
		return "", err
	}

	type versionedTag struct {
		name string
		sha  string
		ver  SemVer
	}
	versioned := make([]versionedTag, 0, len(tags))
	for _, tag := range tags {
		ver, err := ParseSemVer(tag.Name)
		if err != nil {
			continue // not a release tag — not a candidate
		}
		versioned = append(versioned, versionedTag{name: tag.Name, sha: tag.Commit.SHA, ver: ver})
	}
	sort.Slice(versioned, func(i, j int) bool {
		a, b := versioned[i].ver, versioned[j].ver
		if a.Major != b.Major {
			return a.Major < b.Major
		}
		if a.Minor != b.Minor {
			return a.Minor < b.Minor
		}
		return a.Patch < b.Patch
	})

	for _, t := range versioned {
		if t.sha == sha {
			return t.name, nil
		}
		status, err := c.ghClient.CompareStatus(ctx, owner, repo, sha, t.sha)
		if err != nil {
			return "", fmt.Errorf("compare %s against tag %s: %w", ShortSHA(sha), t.name, err)
		}
		if status == "ahead" || status == "identical" {
			return t.name, nil
		}
	}
	return "", nil
}
