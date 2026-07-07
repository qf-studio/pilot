package autopilot

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// maxScopeReleaseAttempts caps how many times a scope-release carrier may fail
// (post-merge CI red/timeout, or a handleReleasing escalation) before the
// scope is given up on and flagged for human attention via a
// scope_release_failed alert. Each failure short of the cap re-queues the
// scope as 'pending' so the next startPendingScopeReleases sweep registers a
// fresh carrier (GH-3990).
const maxScopeReleaseAttempts = 5

// enqueueScopeRelease durably records that scopeKey's members (mergedPRs) are
// ready to release as one carrier once startPendingScopeReleases claims the
// row. Idempotent: a second call for the same scopeKey (e.g. the reactive
// epic-close path racing the closed-epic lookback sweep) is a no-op via
// INSERT OR IGNORE. No-op with a WARN when no state store is wired — a scope
// release must survive a restart between "epic closed" and "carrier claimed",
// so it requires persistence (GH-3990).
func (c *Controller) enqueueScopeRelease(ctx context.Context, scopeKey, title string, mergedPRs []int) {
	_ = ctx
	if c.stateStore == nil {
		c.log.Warn("enqueueScopeRelease: no state store wired, scope release requires persistence — skipping",
			"scope", scopeKey)
		return
	}
	members := dedupeSortInts(mergedPRs)
	if len(members) == 0 {
		c.log.Warn("enqueueScopeRelease: no merged member PRs, skipping", "scope", scopeKey)
		return
	}
	if err := c.stateStore.EnqueueScopeRelease(c.repoKey(), scopeKey, title, members); err != nil {
		c.log.Warn("enqueueScopeRelease: failed to persist scope release", "scope", scopeKey, "error", err)
		return
	}
	c.log.Info("enqueued scope release", "scope", scopeKey, "title", title, "members", members)
}

// dedupeSortInts returns nums deduplicated and sorted ascending.
func dedupeSortInts(nums []int) []int {
	seen := make(map[int]bool, len(nums))
	out := make([]int, 0, len(nums))
	for _, n := range nums {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// startPendingScopeReleases claims durable scope-release rows ready to carry
// and registers a carrier PRState for each, and re-drives any 'releasing' row
// left behind by a crash with no live carrier. Called once at startup
// (Controller.Start) and on every epicParentTicker tick (GH-3990).
func (c *Controller) startPendingScopeReleases(ctx context.Context) {
	if c.stateStore == nil {
		return
	}
	repo := c.repoKey()

	releasing, err := c.stateStore.ListScopeReleases(repo, "releasing")
	if err != nil {
		c.log.Warn("startPendingScopeReleases: failed to list releasing scope rows", "error", err)
	}
	for _, row := range releasing {
		if c.scopeKeyHasLiveCarrier(row.ScopeKey) {
			continue
		}
		c.log.Info("re-driving scope release with no live carrier", "scope", row.ScopeKey)
		if err := c.stateStore.MarkScopeReleasePending(repo, row.ScopeKey, false); err != nil {
			c.log.Warn("startPendingScopeReleases: failed to re-queue stale releasing row",
				"scope", row.ScopeKey, "error", err)
		}
	}

	pending, err := c.stateStore.ListScopeReleases(repo, "pending")
	if err != nil {
		c.log.Warn("startPendingScopeReleases: failed to list pending scope rows", "error", err)
		return
	}
	for _, row := range pending {
		c.tryStartScopeRelease(row)
	}
}

// tryStartScopeRelease claims one pending scope-release row and registers a
// carrier PRState for it. Defers (without claiming) when any member PR is
// still tracked in activePRs — members may sit mid-pipeline (issues close at
// merge while post-merge CI still runs) — or when the chosen anchor PR is
// already tracked or has a fresh persisted 'releasing' row, guarding against
// double-registration across a restart-timing gap (GH-3990).
func (c *Controller) tryStartScopeRelease(row *ScopeRelease) {
	if len(row.MemberPRs) == 0 {
		c.log.Warn("tryStartScopeRelease: scope release has no members, skipping", "scope", row.ScopeKey)
		return
	}
	if c.memberPRsStillActive(row.MemberPRs) {
		c.log.Debug("deferring scope release: a member PR is still mid-pipeline", "scope", row.ScopeKey)
		return
	}

	anchorPR := row.MemberPRs[len(row.MemberPRs)-1]
	repo := c.repoKey()

	c.mu.RLock()
	_, tracked := c.activePRs[anchorPR]
	c.mu.RUnlock()
	if tracked {
		c.log.Debug("deferring scope release: anchor PR already tracked", "scope", row.ScopeKey, "anchor_pr", anchorPR)
		return
	}
	if age, found, err := c.stateStore.PersistedReleasingAge(repo, anchorPR); err != nil {
		c.log.Warn("tryStartScopeRelease: failed to check persisted releasing state, deferring to be safe",
			"scope", row.ScopeKey, "anchor_pr", anchorPR, "error", err)
		return
	} else if found && age < releasingStaleThreshold {
		c.log.Debug("deferring scope release: anchor PR has a fresh persisted releasing row",
			"scope", row.ScopeKey, "anchor_pr", anchorPR)
		return
	}

	claimed, err := c.stateStore.ClaimScopeRelease(repo, row.ScopeKey)
	if err != nil {
		c.log.Warn("tryStartScopeRelease: failed to claim scope release", "scope", row.ScopeKey, "error", err)
		return
	}
	if !claimed {
		return
	}

	prState := &PRState{
		PRNumber:        anchorPR,
		PRURL:           fmt.Sprintf("https://github.com/%s/%s/pull/%d", c.owner, c.repo, anchorPR),
		IssueNumber:     epicParentFromScopeKey(row.ScopeKey),
		Stage:           StagePostMergeCI,
		CreatedAt:       time.Now(),
		EnvironmentName: c.config.EnvironmentName(),
		ScopeKey:        row.ScopeKey,
		ScopeTitle:      row.ScopeTitle,
		ScopeMemberPRs:  row.MemberPRs,
	}
	c.mu.Lock()
	c.activePRs[anchorPR] = prState
	c.mu.Unlock()
	// prState is now published in activePRs — persist under prState.mu per the
	// caller-holds-the-lock contract (mirrors OnPRCreated/ScanRecentlyMergedPRs).
	prState.mu.Lock()
	c.persistPRState(prState)
	prState.mu.Unlock()

	c.log.Info("registered scope release carrier", "scope", row.ScopeKey, "anchor_pr", anchorPR, "members", row.MemberPRs)
}

// scopeKeyHasLiveCarrier reports whether any tracked PRState currently carries
// scopeKey.
func (c *Controller) scopeKeyHasLiveCarrier(scopeKey string) bool {
	c.mu.RLock()
	live := make([]*PRState, 0, len(c.activePRs))
	for _, pr := range c.activePRs {
		live = append(live, pr)
	}
	c.mu.RUnlock()

	for _, pr := range live {
		pr.mu.Lock()
		k := pr.ScopeKey
		pr.mu.Unlock()
		if k == scopeKey {
			return true
		}
	}
	return false
}

// memberPRsStillActive reports whether any of the given PR numbers is still
// tracked in activePRs.
func (c *Controller) memberPRsStillActive(memberPRs []int) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, n := range memberPRs {
		if _, ok := c.activePRs[n]; ok {
			return true
		}
	}
	return false
}

// epicScopeKeyRe extracts the epic parent issue number from a scope key of the
// form "epic:<N>".
var epicScopeKeyRe = regexp.MustCompile(`^epic:(\d+)$`)

// epicParentFromScopeKey returns the epic parent issue number encoded in an
// "epic:<N>" scope key, or 0 for label:/train: keys — which have no natural
// issue to attach failure comments/notifications to.
func epicParentFromScopeKey(scopeKey string) int {
	m := epicScopeKeyRe.FindStringSubmatch(scopeKey)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// hydrateScopeMembers re-populates a scope carrier's in-memory ScopeTitle/
// ScopeMemberPRs from the persisted scope_release row when they're empty — the
// case after a daemon restart restores the carrier's PRState from
// autopilot_pr_state, which persists ScopeKey but not these fields (GH-3990).
func (c *Controller) hydrateScopeMembers(prState *PRState) {
	if c.stateStore == nil {
		return
	}
	row, err := c.stateStore.GetScopeRelease(c.repoKey(), prState.ScopeKey)
	if err != nil || row == nil {
		c.log.Warn("hydrateScopeMembers: failed to load scope release row", "scope", prState.ScopeKey, "error", err)
		return
	}
	prState.ScopeTitle = row.ScopeTitle
	prState.ScopeMemberPRs = row.MemberPRs
}

// scopeReleaseCommits builds the commit set for a scope carrier's release: the
// union of every member PR's commits, deduped by SHA. If that union comes back
// empty (e.g. every member GetPRCommits call failed), it falls back to
// comparing against the tag for the current version (GH-3990).
func (c *Controller) scopeReleaseCommits(ctx context.Context, owner, repo string, prState *PRState, currentVersion SemVer, rel *ReleaseConfig) ([]*github.Commit, error) {
	seen := make(map[string]bool)
	var commits []*github.Commit
	for _, member := range prState.ScopeMemberPRs {
		memberCommits, err := c.ghClient.GetPRCommits(ctx, owner, repo, member)
		if err != nil {
			c.log.Warn("scope release: failed to fetch member PR commits",
				"scope", prState.ScopeKey, "member_pr", member, "error", err)
			continue
		}
		for _, mc := range memberCommits {
			if seen[mc.SHA] {
				continue
			}
			seen[mc.SHA] = true
			commits = append(commits, mc)
		}
	}
	if len(commits) > 0 {
		return commits, nil
	}

	lastTag := currentVersion.String(rel.TagPrefix)
	c.log.Warn("scope release: member commit union empty, falling back to compare against last tag",
		"scope", prState.ScopeKey, "last_tag", lastTag, "head_sha", ShortSHA(prState.HeadSHA))
	return c.ghClient.CompareCommits(ctx, owner, repo, lastTag, prState.HeadSHA)
}

// markScopeReleaseDone records a scope carrier's successful (or no-op)
// completion in the state store. tag is "" for a no-op release (BumpNone).
// No-op when prState is not a scope carrier or no state store is wired.
func (c *Controller) markScopeReleaseDone(prState *PRState, tag string) {
	if prState.ScopeKey == "" || c.stateStore == nil {
		return
	}
	if err := c.stateStore.MarkScopeReleaseDone(c.repoKey(), prState.ScopeKey, tag, prState.HeadSHA); err != nil {
		c.log.Warn("markScopeReleaseDone: failed to mark scope release done",
			"scope", prState.ScopeKey, "tag", tag, "error", err)
	}
}

// handleScopeReleaseFailure records a carrier failure against its scope
// release row: increments attempts and re-queues it as 'pending' for a fresh
// carrier, or — once attempts exceeds maxScopeReleaseAttempts — marks the
// scope 'failed' and fires a scope_release_failed alert so a human can
// intervene. No-op when prState is not a scope carrier or no state store is
// wired. Callers remain responsible for draining the carrier PRState itself
// (removePR) so the anchor PR slot frees for the next attempt (GH-3990).
func (c *Controller) handleScopeReleaseFailure(ctx context.Context, prState *PRState, reason string) {
	_ = ctx
	if prState.ScopeKey == "" || c.stateStore == nil {
		return
	}
	repo := c.repoKey()
	if err := c.stateStore.MarkScopeReleasePending(repo, prState.ScopeKey, true); err != nil {
		c.log.Warn("handleScopeReleaseFailure: failed to re-queue scope release", "scope", prState.ScopeKey, "error", err)
		return
	}
	row, err := c.stateStore.GetScopeRelease(repo, prState.ScopeKey)
	if err != nil || row == nil {
		c.log.Warn("handleScopeReleaseFailure: failed to read back scope release row", "scope", prState.ScopeKey, "error", err)
		return
	}
	c.log.Warn("scope release carrier failed",
		"scope", prState.ScopeKey, "pr", prState.PRNumber, "attempts", row.Attempts, "reason", reason)

	if row.Attempts <= maxScopeReleaseAttempts {
		return
	}

	if err := c.stateStore.MarkScopeReleaseFailed(repo, prState.ScopeKey); err != nil {
		c.log.Warn("handleScopeReleaseFailure: failed to mark scope release failed", "scope", prState.ScopeKey, "error", err)
		return
	}

	msg := fmt.Sprintf("scope release %s failed after %d attempts: %s — manual intervention required",
		prState.ScopeKey, row.Attempts, reason)
	if c.alertsEngine == nil {
		c.log.Error("scope_release_failed alert not delivered: SetAlertsEngine was never called", "scope", prState.ScopeKey)
	} else {
		c.alertsEngine.ProcessEvent(alerts.Event{
			Type:      alerts.EventType("scope_release_failed"),
			Error:     msg,
			Timestamp: time.Now(),
			Metadata: map[string]string{
				"repo":  repo,
				"scope": prState.ScopeKey,
			},
		})
	}
}
