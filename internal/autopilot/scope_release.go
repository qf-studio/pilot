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

// closesIssueRegex extracts the issue number from a "Closes #N" (or
// Fixes/Resolves, singular or plural) reference in a PR body — the
// convention Pilot's own PR bodies use when created from a source issue
// (internal/executor/runner.go). Best-effort: a member PR with no such
// reference (e.g. a human-authored merge) simply gets Issue == 0 in its
// ScopeMember, which BuildScopeReleaseNotes renders without the GH-<N> half
// of the attribution (GH-3992).
var closesIssueRegex = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)`)

// maxScopeReleaseAttempts caps how many times a scope-release carrier may fail
// (post-merge CI red/timeout, or a handleReleasing escalation) before the
// scope is given up on and flagged for human attention via a
// scope_release_failed alert. Each failure short of the cap re-queues the
// scope as 'pending' so the next startPendingScopeReleases sweep registers a
// fresh carrier (GH-3990).
const maxScopeReleaseAttempts = 5

// maxScopeReleaseTimeoutAttempts caps consecutive "post-merge CI timeout"
// carrier failures for one scope before it is parked instead of re-queued
// (GH-4643). This is deliberately separate from maxScopeReleaseAttempts:
// ScopeRelease.TimeoutAttempts is never reset by recoverFailedScopeReleases's
// SHA-advance resurrection the way Attempts is, so it's what actually breaks
// the loop for a structurally-unresolvable timeout (a repo with no post-merge
// CI configured at all, but a required-checks allowlist naming a check that
// will never post) — every ordinary merge to main was otherwise handing a
// fresh 5-attempt budget to a wait that can never succeed.
const maxScopeReleaseTimeoutAttempts = 3

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
		if err := c.stateStore.MarkScopeReleasePending(repo, row.ScopeKey, false, ""); err != nil {
			c.log.Warn("startPendingScopeReleases: failed to re-queue stale releasing row",
				"scope", row.ScopeKey, "error", err)
		}
	}

	c.recoverFailedScopeReleases(ctx)

	pending, err := c.stateStore.ListScopeReleases(repo, "pending")
	if err != nil {
		c.log.Warn("startPendingScopeReleases: failed to list pending scope rows", "error", err)
		return
	}
	for _, row := range pending {
		c.tryStartScopeRelease(row)
	}
}

// recoverFailedScopeReleases resurrects terminal 'failed' scope-release rows
// once main has moved past the SHA they last failed against — turning a
// same-day fix into a same-day release instead of leaving the train dead
// until tomorrow's scope rolls it forward. Attempts reset to 0 so the fresh
// carrier retries against the new commit with the full budget (GH-4331).
// Rows with no recorded LastFailedSHA (pre-migration data) are left alone —
// there is nothing to compare against, so leave the human-alerted terminal
// state as-is rather than guess.
func (c *Controller) recoverFailedScopeReleases(ctx context.Context) {
	repo := c.repoKey()
	failed, err := c.stateStore.ListScopeReleases(repo, "failed")
	if err != nil {
		c.log.Warn("recoverFailedScopeReleases: failed to list failed scope rows", "error", err)
		return
	}
	if len(failed) == 0 {
		return
	}
	mainSHA, err := c.getMainBranchSHA(ctx)
	if err != nil {
		c.log.Warn("recoverFailedScopeReleases: failed to get main branch SHA", "error", err)
		return
	}
	for _, row := range failed {
		if row.LastFailedSHA == "" || row.LastFailedSHA == mainSHA {
			continue
		}
		if err := c.stateStore.ResetScopeReleaseForRetry(repo, row.ScopeKey); err != nil {
			c.log.Warn("recoverFailedScopeReleases: failed to reset scope release", "scope", row.ScopeKey, "error", err)
			continue
		}
		c.log.Info("recovered terminal scope release for retry against new main HEAD",
			"scope", row.ScopeKey, "old_sha", ShortSHA(row.LastFailedSHA), "new_sha", ShortSHA(mainSHA))
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
		if c.shouldLogScopeDefer(row.ScopeKey) {
			c.log.Info("deferring scope release: a member PR is still mid-pipeline", "scope", row.ScopeKey)
		}
		return
	}

	anchorPR := row.MemberPRs[len(row.MemberPRs)-1]
	repo := c.repoKey()

	c.mu.RLock()
	_, tracked := c.activePRs[anchorPR]
	c.mu.RUnlock()
	if tracked {
		if c.shouldLogScopeDefer(row.ScopeKey) {
			c.log.Info("deferring scope release: anchor PR already tracked", "scope", row.ScopeKey, "anchor_pr", anchorPR)
		}
		return
	}
	if age, found, err := c.stateStore.PersistedReleasingAge(repo, anchorPR); err != nil {
		c.log.Warn("tryStartScopeRelease: failed to check persisted releasing state, deferring to be safe",
			"scope", row.ScopeKey, "anchor_pr", anchorPR, "error", err)
		return
	} else if found && age < releasingStaleThreshold {
		if c.shouldLogScopeDefer(row.ScopeKey) {
			c.log.Info("deferring scope release: anchor PR has a fresh persisted releasing row",
				"scope", row.ScopeKey, "anchor_pr", anchorPR)
		}
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

// scopeDeferLogThrottle bounds how often tryStartScopeRelease's "deferring
// scope release" INFO lines repeat for the same scope key (GH-4643). Every
// deferral reason it guards (member PR mid-pipeline, anchor PR tracked, fresh
// persisted releasing row) can legitimately persist across many consecutive
// epicParentTicker ticks, so without a throttle a single stuck scope floods
// the log with an identical line roughly every tick interval.
const scopeDeferLogThrottle = 30 * time.Minute

// shouldLogScopeDefer reports whether tryStartScopeRelease should emit its
// next "deferring scope release" INFO line for scopeKey, and records the
// attempt — at most once per scopeDeferLogThrottle per scope (GH-4643).
// Mirrors the guarded-map-on-Controller pattern used by
// alertedMissingReleases/alertedStaleScopes, just for log throttling instead
// of alert dedup.
func (c *Controller) shouldLogScopeDefer(scopeKey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scopeDeferLogAt == nil {
		c.scopeDeferLogAt = make(map[string]time.Time)
	}
	if last, ok := c.scopeDeferLogAt[scopeKey]; ok && time.Since(last) < scopeDeferLogThrottle {
		return false
	}
	c.scopeDeferLogAt[scopeKey] = time.Now()
	return true
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

// buildScopeMembers resolves each member PR's own commits and, best-effort,
// the GitHub issue it closed (parsed from the PR body via closesIssueRegex)
// so BuildScopeReleaseNotes can render exact per-entry "(#PR, GH-Issue)"
// attribution. A per-member lookup failure logs a warning and leaves that
// member with a zero Issue / empty Commits rather than failing the whole
// scope release — the deterministic notes are best-effort, matching
// scopeReleaseCommits' own per-member tolerance (GH-3992).
func (c *Controller) buildScopeMembers(ctx context.Context, owner, repo string, memberPRs []int) []ScopeMember {
	members := make([]ScopeMember, 0, len(memberPRs))
	for _, pr := range memberPRs {
		member := ScopeMember{PR: pr}

		if commits, err := c.ghClient.GetPRCommits(ctx, owner, repo, pr); err != nil {
			c.log.Warn("buildScopeMembers: failed to fetch member PR commits", "pr", pr, "error", err)
		} else {
			member.Commits = commits
		}

		if pull, err := c.ghClient.GetPullRequest(ctx, owner, repo, pr); err != nil {
			c.log.Warn("buildScopeMembers: failed to fetch member PR for issue attribution", "pr", pr, "error", err)
		} else if m := closesIssueRegex.FindStringSubmatch(pull.Body); m != nil {
			if n, convErr := strconv.Atoi(m[1]); convErr == nil {
				member.Issue = n
			}
		}

		members = append(members, member)
	}
	return members
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
// intervene. Separately (GH-4643), consecutive "post-merge CI timeout"
// failures are tracked via TimeoutAttempts; once that reaches
// maxScopeReleaseTimeoutAttempts the scope is parked instead — a distinct
// terminal state that recoverFailedScopeReleases's SHA-advance resurrection
// never revisits, breaking the loop for a repo with no post-merge CI
// configured at all. No-op when prState is not a scope carrier or no state
// store is wired. Callers remain responsible for draining the carrier
// PRState itself (removePR) so the anchor PR slot frees for the next attempt
// (GH-3990).
//
// isTimeout is a typed fact the caller carries explicitly — TASK-459 Phase 4
// task 4a: this used to be inferred via strings.Contains(reason,
// "post-merge CI timeout") against the human-formatted reason string
// assembled at controller.go's handlePostMergeCI, making that prose the de
// facto routing protocol (any future rewording of the timeout message would
// silently stop parking timed-out scopes). reason still carries the
// human-readable detail for logs/alerts/comments; it just no longer decides
// routing.
func (c *Controller) handleScopeReleaseFailure(ctx context.Context, prState *PRState, reason string, isTimeout bool) {
	_ = ctx
	if prState.ScopeKey == "" || c.stateStore == nil {
		return
	}
	repo := c.repoKey()
	if err := c.stateStore.MarkScopeReleasePending(repo, prState.ScopeKey, true, prState.PostMergeSHA); err != nil {
		c.log.Warn("handleScopeReleaseFailure: failed to re-queue scope release", "scope", prState.ScopeKey, "error", err)
		return
	}

	timeoutAttempts, err := c.stateStore.UpdateScopeReleaseTimeoutAttempts(repo, prState.ScopeKey, isTimeout)
	if err != nil {
		c.log.Warn("handleScopeReleaseFailure: failed to update timeout attempts", "scope", prState.ScopeKey, "error", err)
	}

	row, err := c.stateStore.GetScopeRelease(repo, prState.ScopeKey)
	if err != nil || row == nil {
		c.log.Warn("handleScopeReleaseFailure: failed to read back scope release row", "scope", prState.ScopeKey, "error", err)
		return
	}
	if row.State == "parked" {
		// Already parked by a prior call — MarkScopeReleasePending's terminal
		// guard above already left the row untouched. Without this check a
		// zombie carrier still ticking against an already-parked scope would
		// re-fire scope_release_parked on every failure instead of exactly
		// once (GH-4643).
		return
	}
	c.log.Warn("scope release carrier failed",
		"scope", prState.ScopeKey, "pr", prState.PRNumber, "attempts", row.Attempts,
		"timeout_attempts", timeoutAttempts, "reason", reason)

	if isTimeout && timeoutAttempts >= maxScopeReleaseTimeoutAttempts {
		c.parkScopeReleaseAfterTimeouts(repo, prState.ScopeKey, prState.PostMergeSHA, timeoutAttempts, reason)
		return
	}

	if row.Attempts <= maxScopeReleaseAttempts {
		return
	}

	if err := c.stateStore.MarkScopeReleaseFailed(repo, prState.ScopeKey); err != nil {
		c.log.Warn("handleScopeReleaseFailure: failed to mark scope release failed", "scope", prState.ScopeKey, "error", err)
		return
	}

	msg := fmt.Sprintf("scope release %s failed after %d attempts: %s — manual intervention required",
		prState.ScopeKey, row.Attempts, reason)
	c.fireScopeReleaseAlert("scope_release_failed", repo, prState.ScopeKey, msg)
}

// parkScopeReleaseAfterTimeouts marks a scope-release row terminal-parked
// after maxScopeReleaseTimeoutAttempts consecutive post-merge CI timeouts and
// fires a scope_release_parked alert (GH-4643). Split out from
// handleScopeReleaseFailure so the timeout-specific park path and the
// generic attempts-cap failure path stay readable as two separate branches.
//
// GH-4646: this message used to assert "this repo likely has no post-merge
// CI configured" unconditionally — wrong for auth-service/studio-sdk, where
// CI was very much running, just under check names required_checks didn't
// name. sha lets it consult whatever the CI monitor already discovered for
// this carrier's SHA, so an operator chasing the alert is pointed at the
// actual config mismatch instead of a phantom missing workflow.
func (c *Controller) parkScopeReleaseAfterTimeouts(repo, scopeKey, sha string, timeoutAttempts int, reason string) {
	if err := c.stateStore.MarkScopeReleaseParked(repo, scopeKey); err != nil {
		c.log.Warn("handleScopeReleaseFailure: failed to mark scope release parked", "scope", scopeKey, "error", err)
		return
	}
	c.log.Warn("scope release carrier parked after repeated post-merge CI timeouts",
		"scope", scopeKey, "timeout_attempts", timeoutAttempts)

	detail := "this repo likely has no post-merge CI configured; add a push-to-main workflow, or drop the unreachable required check from this repo's CI config"
	if c.ciMonitor != nil {
		if discovered := c.ciMonitor.GetDiscoveredChecks(sha); len(discovered) > 0 {
			detail = fmt.Sprintf(
				"this repo IS running CI on this SHA (discovered checks: %v; required_checks/ci_checks.required: %v) — a required check name does not match any of them; fix the allowlist rather than assuming there's no workflow",
				discovered, c.ciMonitor.RequiredChecks(),
			)
		}
	}

	msg := fmt.Sprintf(
		"scope release %s parked after %d consecutive post-merge CI timeouts (%s) — %s, then re-enqueue the scope manually",
		scopeKey, timeoutAttempts, reason, detail,
	)
	c.fireScopeReleaseAlert("scope_release_parked", repo, scopeKey, msg)
}

// fireScopeReleaseAlert sends a scope-release lifecycle alert, or logs at
// Error level if no alerts engine is wired — shared by both the terminal-
// failed and terminal-parked paths in handleScopeReleaseFailure (GH-4643).
func (c *Controller) fireScopeReleaseAlert(eventType, repo, scopeKey, msg string) {
	if c.alertsEngine == nil {
		c.log.Error(eventType+" alert not delivered: SetAlertsEngine was never called", "scope", scopeKey)
		return
	}
	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventType(eventType),
		Error:     msg,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":  repo,
			"scope": scopeKey,
		},
	})
}
