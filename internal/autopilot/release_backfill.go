package autopilot

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// maxReleaseBackfillTags is the page size handed to ListTags. It is NOT a
// window: the SDK's ListTags paginates exhaustively (up to 50 pages), so the
// whole tag list comes back regardless. Cost is bounded instead by the
// newest-first probe plus binary search in earliestReleaseTagContaining and by
// maxReleaseBackfillCompares (GH-5658).
const maxReleaseBackfillTags = 100

// maxReleaseBackfillRowsPerPass bounds how many rows one reconcileReleaseBackfill
// pass attempts (GH-5657). Each row costs a PR fetch plus, for a merged PR, a
// tags listing and up to maxReleaseBackfillTags compares; an uncapped walk over
// every StageFailed/StageReleasing row held the epic-parent pass for the whole
// 2026-10-13 stall. Remaining rows are picked up on following passes.
const maxReleaseBackfillRowsPerPass = 5

const (
	// maxReleaseBackfillCompares is the per-row CompareStatus budget for one
	// earliestReleaseTagContaining call. A binary search over the ~900 tags the
	// pilot repo carries needs ~11 compares (1 probe + ceil(log2 n)); 12 leaves
	// headroom for growth to ~2k tags, and exceeding it returns an error so the
	// row backs off (GH-4919) rather than holding the controller tick for a
	// linear walk (2026-10-13: 903 compares, 57 minutes, train never tagged).
	maxReleaseBackfillCompares = 12
	// releaseBackfillDivergedScan caps the linear fallback used when the
	// newest-tag probe reports "diverged" (history not linear at the tip).
	releaseBackfillDivergedScan = 5
)

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
	// softFails counts merged-but-untagged observations (GH-5671). It only
	// drives the back-off exponent of releaseBackfillObserveSoftFailure and
	// never feeds the abandon rule: no API call failed, the row is simply
	// waiting for a tag.
	softFails int
}

const (
	// releaseBackfillMaxBackoff caps the exponential per-row backoff below —
	// a wedged row's API calls can never exceed roughly this cadence,
	// regardless of how long its failure streak runs.
	releaseBackfillMaxBackoff = time.Hour
	// releaseBackfillAbandonThreshold is the consecutive-failure count (see
	// releaseBackfillRowState.consecutiveFails) required, together with
	// releaseBackfillAbandonMinWindow, before a row is classified permanently
	// failed. Abandonment counts API failures only, never the no-tag outcome
	// (a merged PR still waiting for a release tag, see
	// releaseBackfillObserveSoftFailure): such a row backs off but is retried
	// indefinitely, since the sweep exists to heal rows that get tagged later
	// (GH-5671).
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

// releaseBackfillBackoff is the doubling retry delay for the n-th observation
// (n >= 1) in a streak: base, 2*base, 4*base, ... capped at
// releaseBackfillMaxBackoff.
func releaseBackfillBackoff(base time.Duration, n int) time.Duration {
	backoff := base
	for i := 1; i < n && backoff < releaseBackfillMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > releaseBackfillMaxBackoff {
		backoff = releaseBackfillMaxBackoff
	}
	return backoff
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
	st.nextRetryAt = now.Add(releaseBackfillBackoff(base, st.consecutiveFails))

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

// releaseBackfillObserveSoftFailure records a merged-but-untagged observation
// for key (GH-5671): the next retry is scheduled with the same doubling
// schedule and releaseBackfillMaxBackoff cap as a hard failure, but the abandon
// streak (consecutiveFails/firstFailAt) is left untouched, so a row waiting for
// its release tag is never abandoned. A later hard failure keeps today's
// behaviour; releaseBackfillObserveSuccess clears the whole state.
func (c *Controller) releaseBackfillObserveSoftFailure(key string, now time.Time) {
	c.releaseBackfillMu.Lock()
	defer c.releaseBackfillMu.Unlock()
	if c.releaseBackfillRows == nil {
		c.releaseBackfillRows = make(map[string]*releaseBackfillRowState)
	}
	st := c.releaseBackfillRows[key]
	if st == nil {
		st = &releaseBackfillRowState{}
		c.releaseBackfillRows[key] = st
	}
	st.softFails++

	base := c.config.CIPollInterval
	if base <= 0 {
		base = 30 * time.Second
	}
	st.nextRetryAt = now.Add(releaseBackfillBackoff(base, st.softFails))
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
	// GH-5657: a background GitHub consumer like the merged-PR scan — yield to
	// the shared rate-budget floor.
	if !c.backgroundScanAllowed("release_backfill") {
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

	type dueRow struct {
		prState *PRState
		key     string
	}
	var due []dueRow
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
		due = append(due, dueRow{prState: prState, key: key})
	}
	if len(due) == 0 {
		return
	}

	// GH-5657: at most maxReleaseBackfillRowsPerPass rows per pass (each costs a
	// PR fetch plus up to maxReleaseBackfillTags compares). The cursor rotates
	// the window across passes: rows that stay due after a clean fetch (merged
	// but not yet tagged) would otherwise hold the first slots forever.
	c.releaseBackfillMu.Lock()
	start := c.releaseBackfillCursor % len(due)
	c.releaseBackfillMu.Unlock()

	// GH-5658: all rows of this pass share one tag listing per repo, fetched on
	// first use and reused by every later row (no per-row ListTags).
	ctx = withReleaseBackfillTagCache(ctx, now)

	touched := 0
	for i := 0; i < len(due) && touched < maxReleaseBackfillRowsPerPass; i++ {
		if ctx.Err() != nil {
			break
		}
		row := due[(start+i)%len(due)]
		c.healReleaseBackfillRow(ctx, row.prState, row.key, now)
		touched++
	}

	c.releaseBackfillMu.Lock()
	c.releaseBackfillCursor = start + touched
	c.releaseBackfillMu.Unlock()
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
	if tag == "" {
		// Merged, but not yet covered by any tag — genuinely unreleased. Backed
		// off (GH-5665): clearing the state here made the row due again on the
		// next 30s pass, costing a compare per pass for as long as the row
		// exists. A soft failure (GH-5671): it uses the doubling schedule but
		// never advances the abandon streak, since no API call failed. Only a
		// tag hit (below) clears the state.
		c.releaseBackfillObserveSoftFailure(key, now)
		return
	}
	c.releaseBackfillObserveSuccess(key)

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

// releaseBackfillTag is one semver release tag, ready for ancestry probing.
type releaseBackfillTag struct {
	name string
	sha  string
	ver  SemVer
}

// releaseBackfillTagList is one repo's versioned tags, sorted ascending by
// semver, or the error the listing failed with.
type releaseBackfillTagList struct {
	tags []releaseBackfillTag
	err  error
}

// releaseBackfillTagTTL is how long a successful tag listing is reused across
// passes (GH-5665). Without it every 30s pass paginated the whole tag list
// (10 calls over 903 tags on the pilot repo) while any merged-but-untagged row
// existed. A new release tag is picked up within one TTL, which only delays a
// bookkeeping heal.
const releaseBackfillTagTTL = 10 * time.Minute

// releaseBackfillCachedTags is a Controller-held tag listing and when it was
// fetched.
type releaseBackfillCachedTags struct {
	tags      []releaseBackfillTag
	fetchedAt time.Time
}

// releaseBackfillTagCache memoises tag listings for the duration of one
// reconcileReleaseBackfill pass, keyed "owner/repo". It is carried in the
// context so earliestReleaseTagContaining keeps its signature. Errors are
// cached too: a failing listing is not re-hammered once per row.
type releaseBackfillTagCache struct {
	mu    sync.Mutex
	now   time.Time
	lists map[string]*releaseBackfillTagList
}

type releaseBackfillTagCacheKey struct{}

// withReleaseBackfillTagCache attaches the per-pass cache; now is the pass's
// clock reading, used for the cross-pass TTL check.
func withReleaseBackfillTagCache(ctx context.Context, now time.Time) context.Context {
	return context.WithValue(ctx, releaseBackfillTagCacheKey{}, &releaseBackfillTagCache{
		now:   now,
		lists: make(map[string]*releaseBackfillTagList),
	})
}

// releaseBackfillTags returns owner/repo's semver tags sorted ascending. With a
// per-pass cache in ctx the listing is fetched at most once per pass, and a
// successful listing is kept on the Controller for releaseBackfillTagTTL so
// following passes reuse it (GH-5665). Failed listings are cached for the pass
// only: they must be retried on the next pass, not pinned for the TTL.
func (c *Controller) releaseBackfillTags(ctx context.Context, owner, repo string) ([]releaseBackfillTag, error) {
	cache, _ := ctx.Value(releaseBackfillTagCacheKey{}).(*releaseBackfillTagCache)
	if cache == nil {
		return c.fetchReleaseBackfillTags(ctx, owner, repo)
	}
	key := owner + "/" + repo
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if l, ok := cache.lists[key]; ok {
		return l.tags, l.err
	}

	c.releaseBackfillMu.Lock()
	cached, ok := c.releaseBackfillTagLists[key]
	c.releaseBackfillMu.Unlock()
	if ok && cache.now.Sub(cached.fetchedAt) < releaseBackfillTagTTL {
		cache.lists[key] = &releaseBackfillTagList{tags: cached.tags}
		return cached.tags, nil
	}

	tags, err := c.fetchReleaseBackfillTags(ctx, owner, repo)
	cache.lists[key] = &releaseBackfillTagList{tags: tags, err: err}
	if err == nil {
		c.releaseBackfillMu.Lock()
		if c.releaseBackfillTagLists == nil {
			c.releaseBackfillTagLists = make(map[string]releaseBackfillCachedTags)
		}
		c.releaseBackfillTagLists[key] = releaseBackfillCachedTags{tags: tags, fetchedAt: cache.now}
		c.releaseBackfillMu.Unlock()
	}
	return tags, err
}

func (c *Controller) fetchReleaseBackfillTags(ctx context.Context, owner, repo string) ([]releaseBackfillTag, error) {
	tags, err := c.ghClient.ListTags(ctx, owner, repo, maxReleaseBackfillTags)
	if err != nil {
		return nil, err
	}
	versioned := make([]releaseBackfillTag, 0, len(tags))
	for _, tag := range tags {
		ver, err := ParseSemVer(tag.Name)
		if err != nil {
			continue // not a release tag — not a candidate
		}
		versioned = append(versioned, releaseBackfillTag{name: tag.Name, sha: tag.Commit.SHA, ver: ver})
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
	return versioned, nil
}

// earliestReleaseTagContaining returns the earliest (lowest-semver) release
// tag whose history contains sha, or "" if no tag contains it. The earliest tag
// is the one that actually shipped the commit — a later tag also contains it by
// transitivity, but naming that one would misreport when the work released.
//
// GH-5658: the old ascending linear scan cost one CompareStatus per tag for a
// sha in no tag (903 on the pilot repo), which wedged the controller tick.
// On a linear default branch containment is monotonic in tag order, so:
//  1. probe the newest tag once; if sha is not an ancestor of it ("behind"),
//     no older tag can contain it — return "" after a single compare;
//  2. if the probe reports "diverged" the tip is not linear with sha, so fall
//     back to a scan of the releaseBackfillDivergedScan newest tags;
//  3. otherwise binary-search for the earliest containing tag.
//
// Every CompareStatus counts against maxReleaseBackfillCompares; exceeding it
// returns an error so the caller's failure path backs the row off.
func (c *Controller) earliestReleaseTagContaining(ctx context.Context, owner, repo, sha string) (string, error) {
	tags, err := c.releaseBackfillTags(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	if len(tags) == 0 {
		return "", nil
	}

	compares := 0
	// status reports how sha relates to tags[i]: contained, or the raw status
	// when it is not (so the caller can tell "diverged" from "behind").
	probe := func(i int) (contained bool, status string, err error) {
		t := tags[i]
		if t.sha == sha {
			return true, "identical", nil
		}
		if compares >= maxReleaseBackfillCompares {
			return false, "", fmt.Errorf("compare budget of %d exhausted resolving %s against %d tags (stopped at tag %s)",
				maxReleaseBackfillCompares, ShortSHA(sha), len(tags), t.name)
		}
		compares++
		status, err = c.ghClient.CompareStatus(ctx, owner, repo, sha, t.sha)
		if err != nil {
			return false, "", fmt.Errorf("compare %s against tag %s: %w", ShortSHA(sha), t.name, err)
		}
		return status == "ahead" || status == "identical", status, nil
	}

	newest := len(tags) - 1
	contained, status, err := probe(newest)
	if err != nil {
		return "", err
	}
	if !contained {
		if status != "diverged" {
			return "", nil
		}
		// Capped scan of the newest tags, oldest first so the earliest hit wins.
		// The newest was already probed and is known not to contain sha.
		start := len(tags) - releaseBackfillDivergedScan
		if start < 0 {
			start = 0
		}
		for i := start; i < newest; i++ {
			ok, _, err := probe(i)
			if err != nil {
				return "", err
			}
			if ok {
				return tags[i].name, nil
			}
		}
		return "", nil
	}

	// tags[hi] contains sha; find the lowest index that does.
	lo, hi := 0, newest
	for lo < hi {
		mid := lo + (hi-lo)/2
		ok, _, err := probe(mid)
		if err != nil {
			return "", err
		}
		if ok {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return tags[lo].name, nil
}
