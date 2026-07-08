package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
	github "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// maxEpicReconcile caps how many open, pilot-labeled parent-with-sub-issues
// candidates a single reconcileEpicParents sweep fetches. Mirrors
// recoverStaleParentIssues' maxRecover — bounded so one pathological repo with
// hundreds of open epics can't turn a poll tick into an unbounded API burst.
const maxEpicReconcile = 50

// epicCloseVetoBreakerThreshold caps how many consecutive reconcile passes a
// parent may fail the SAME close-veto (identical blocking child + reason)
// before the loop breaker escalates. Without this, a permanently-vetoed
// parent gets re-dispatched by the poller forever: #3927 ran 6 completed
// executions in ~2h before a human closed it manually (GH-4006) because its
// ghost-closed child #3952 could never produce a merged PR under its own
// issue number — the actual fix shipped via a different issue's PR (#3980).
const epicCloseVetoBreakerThreshold = 3

// epicCloseVetoTracking is the in-memory streak for one epic parent's
// close-veto: which child is blocking, why, how many consecutive reconcile
// passes have seen that exact pair, and whether the breaker has already
// escalated it (so escalateEpicCloseVeto fires its label/comment/alert once).
type epicCloseVetoTracking struct {
	child     int
	reason    string
	count     int
	escalated bool
}

// subIssueState is one native GitHub sub-issue's number and open/closed state.
// Unlike GetOpenSubIssueNumbers (which discards closed children), reconcileEpicParent
// needs the CLOSED ones too, to verify each actually shipped a merged PR.
type subIssueState struct {
	Number int
	Closed bool
}

// childCloseVeto explains why a specific closed child blocks its parent epic's
// auto-close. Reason is a stable, human-readable string (not per-call-unique)
// so repeated poll-cycle sightings of the same stalled child produce the same
// log line — the dedupe path downstream can key off (parent, child, reason)
// to recognize "still the same stall" instead of re-triggering on every tick.
type childCloseVeto struct {
	Child  int
	Reason string
}

// getAllSubIssueNumbers queries native GitHub sub-issues for parentNum and
// returns every child regardless of state (open AND closed), plus whether the
// parent has any linked children at all (native OR text-based). This mirrors
// the SDK's GetOpenSubIssueNumbers query shape but keeps closed children too —
// GH-3939's merged-PR verification must inspect exactly the children that
// open-only listings discard.
//
// GH-4099: when the parent has no native sub-issue links (LinkSubIssue is
// non-fatal at child-creation time, GH-3513, and some epics are decomposed
// using only the body-marker "Parent: GH-N" convention), falls back to a text
// search for children referencing this parent — otherwise such epics are
// invisible to the merged-PR-verified close path even once discovered as a
// reconcile candidate.
func (c *Controller) getAllSubIssueNumbers(ctx context.Context, parentNum int) ([]subIssueState, bool, error) {
	parentID, err := c.ghClient.GetIssueNodeID(ctx, c.owner, c.repo, parentNum)
	if err != nil {
		return nil, false, fmt.Errorf("resolve parent node ID: %w", err)
	}

	const query = `query($issueID: ID!) {
		node(id: $issueID) {
			... on Issue {
				subIssues(first: 100) {
					totalCount
					nodes {
						number
						state
					}
				}
			}
		}
	}`

	var result struct {
		Node struct {
			SubIssues struct {
				TotalCount int `json:"totalCount"`
				Nodes      []struct {
					Number int    `json:"number"`
					State  string `json:"state"`
				} `json:"nodes"`
			} `json:"subIssues"`
		} `json:"node"`
	}

	if err := c.ghClient.ExecuteGraphQL(ctx, query, map[string]interface{}{"issueID": parentID}, &result); err != nil {
		return nil, false, fmt.Errorf("query sub-issues for %s/%s#%d: %w", c.owner, c.repo, parentNum, err)
	}

	if result.Node.SubIssues.TotalCount == 0 {
		return c.getSubIssuesByTextSearch(ctx, parentNum)
	}

	children := make([]subIssueState, 0, len(result.Node.SubIssues.Nodes))
	for _, n := range result.Node.SubIssues.Nodes {
		children = append(children, subIssueState{Number: n.Number, Closed: n.State == "CLOSED"})
	}
	return children, true, nil
}

// getSubIssuesByTextSearch finds every issue (open AND closed) referencing
// "Parent: GH-{parentNum}" in its body via GitHub's GraphQL search (GH-4099).
// Returns the same subIssueState + hasLinks shape as the native-link query so
// callers don't need to special-case the linkage source. Used as the fallback
// for parents whose children were never LinkSubIssue-linked (GH-3513) or were
// decomposed using only the body-marker convention.
func (c *Controller) getSubIssuesByTextSearch(ctx context.Context, parentNum int) ([]subIssueState, bool, error) {
	const query = `query($q: String!, $first: Int!) {
		search(query: $q, type: ISSUE, first: $first) {
			nodes {
				... on Issue {
					number
					state
				}
			}
		}
	}`

	q := fmt.Sprintf(`repo:%s/%s "Parent: GH-%d" is:issue`, c.owner, c.repo, parentNum)
	var result struct {
		Search struct {
			Nodes []struct {
				Number int    `json:"number"`
				State  string `json:"state"`
			} `json:"nodes"`
		} `json:"search"`
	}

	if err := c.ghClient.ExecuteGraphQL(ctx, query, map[string]interface{}{"q": q, "first": 100}, &result); err != nil {
		return nil, false, fmt.Errorf("text-search sub-issues for %s/%s#%d: %w", c.owner, c.repo, parentNum, err)
	}

	if len(result.Search.Nodes) == 0 {
		return nil, false, nil
	}

	children := make([]subIssueState, 0, len(result.Search.Nodes))
	for _, n := range result.Search.Nodes {
		children = append(children, subIssueState{Number: n.Number, Closed: n.State == "CLOSED"})
	}
	return children, true, nil
}

// verifyChildrenShippedForClose re-checks every CLOSED child in children has
// either a merged PR or a verified no_op ledger row (GH-3780: a decomposed
// child that never produced a PR can still be a genuine, verified completion).
// A closed child with neither is a real guard veto — closing the parent would
// silently drop that slice's work.
//
// Returns every merged PR number found (for the closing summary comment) and a
// non-nil veto on the FIRST closed child that fails verification. Fails open
// (skips, does not veto) on a PR-search error for an individual child, since a
// transient API failure must not indefinitely block a legitimate close.
func (c *Controller) verifyChildrenShippedForClose(ctx context.Context, parentNum int, children []subIssueState) (mergedPRs []int, veto *childCloseVeto) {
	for _, child := range children {
		if !child.Closed {
			continue
		}
		if c.isChildNoOp(child.Number) {
			continue
		}

		prs, err := c.ghClient.SearchPRsForIssue(ctx, c.owner, c.repo, child.Number)
		if err != nil {
			c.log.Warn("verifyChildrenShippedForClose: PR search failed, fail-open on this child",
				slog.Int("parent", parentNum), slog.Int("child", child.Number), slog.Any("error", err))
			continue
		}

		merged := false
		for _, pr := range prs {
			if pr.Merged {
				mergedPRs = append(mergedPRs, pr.Number)
				merged = true
			}
		}
		if !merged {
			return mergedPRs, &childCloseVeto{Child: child.Number, Reason: "issue is closed but has no merged PR"}
		}
	}
	return mergedPRs, nil
}

// reconcileEpicParents is the poll-cycle epic-parent auto-close check (GH-3939).
// On each tick it inspects every candidate epic parent (see epicParentCandidates)
// and closes the ones whose children are ALL closed AND shipped a merged PR (or
// verified no_op).
//
// This complements the two existing close paths: maybeCloseParentIssue fires
// reactively only when a sibling's own PR merges, and recoverStaleParentIssues
// runs once at startup. Neither revisits a parent left behind by some other
// event (a child closed out-of-band, a webhook missed) — this sweep does, so
// such a parent converges within one poll cycle instead of staying stuck until
// the next restart.
func (c *Controller) reconcileEpicParents(ctx context.Context) {
	for _, parentNum := range c.epicParentCandidates(ctx) {
		c.reconcileEpicParent(ctx, parentNum)
	}
}

// epicParentCandidates merges the two independent epic-parent discovery
// sources and returns their union, de-duplicated (GH-4099). Shared by
// recoverStaleParentIssues (startup) and reconcileEpicParents (poll-cycle) so
// neither sweep depends on only one of:
//
//   - Native GitHub sub-issue links via SearchOpenPilotIssuesWithSubIssues,
//     gated on the PARENT's own "pilot" label and on subIssuesSummary.total>0.
//   - The body-marker "Parent: GH-N" text convention via
//     discoverBodyMarkerEpicParents, derived from the CHILD side and therefore
//     immune to the parent losing its label or never getting a native link.
//
// #4020 and #4051 both sat open for hours because they were invisible to the
// first source (both had subIssuesSummary.total==0 — LinkSubIssue never ran —
// and #4051 had additionally lost its "pilot" label) and there was no second
// source to catch them. Each source's own failure is logged and non-fatal to
// the other, so a transient outage in one query never blocks the sweep.
func (c *Controller) epicParentCandidates(ctx context.Context) []int {
	native, err := c.ghClient.SearchOpenPilotIssuesWithSubIssues(ctx, c.owner, c.repo, maxEpicReconcile)
	if err != nil {
		c.log.Warn("epicParentCandidates: native-link search failed", slog.Any("error", err))
	}
	// GH-3939: this cap-hit log used to live only in recoverStaleParentIssues;
	// reconcileEpicParents repeats every poll cycle (not just once at startup),
	// so a repo that consistently sits at the cap would otherwise silently and
	// indefinitely skip the overflow candidates with no operator-visible signal.
	if len(native) == maxEpicReconcile {
		c.log.Info("epicParentCandidates: hit native-link limit, some candidates may be skipped", slog.Int("limit", maxEpicReconcile))
	}

	bodyMarker, err := c.discoverBodyMarkerEpicParents(ctx)
	if err != nil {
		c.log.Warn("epicParentCandidates: body-marker discovery failed", slog.Any("error", err))
	}

	seen := make(map[int]bool, len(native)+len(bodyMarker))
	merged := make([]int, 0, len(native)+len(bodyMarker))
	for _, group := range [][]int{native, bodyMarker} {
		for _, n := range group {
			if !seen[n] {
				seen[n] = true
				merged = append(merged, n)
			}
		}
	}
	return merged
}

// epicParentDiscoveryLookback bounds how far back discoverBodyMarkerEpicParents
// scans recently-closed pilot subtasks for a "Parent: GH-N" body reference
// (GH-4099). Wide enough that a parent whose children shipped days apart still
// surfaces as a candidate, but bounded so the query stays a single cheap page.
const epicParentDiscoveryLookback = 7 * 24 * time.Hour

// discoverBodyMarkerEpicParents finds candidate epic-parent numbers referenced
// by recently-closed, pilot-managed child issues' "Parent: GH-N" body marker
// (GH-4099). See epicParentCandidates for why this candidate source exists
// alongside the native sub-issue-link search: it is deliberately NOT gated on
// the parent's own label or native links — it derives candidates purely from
// the CHILD side, which is reliably labeled "pilot"/"pilot-done" regardless of
// what happens to the parent's own label or linkage.
func (c *Controller) discoverBodyMarkerEpicParents(ctx context.Context) ([]int, error) {
	const query = `query($owner: String!, $repo: String!, $first: Int!) {
		repository(owner: $owner, name: $repo) {
			issues(first: $first, states: [CLOSED], labels: ["pilot", "pilot-done"], orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes {
					updatedAt
					body
				}
			}
		}
	}`

	var result struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					UpdatedAt time.Time `json:"updatedAt"`
					Body      string    `json:"body"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}

	variables := map[string]interface{}{
		"owner": c.owner,
		"repo":  c.repo,
		"first": maxEpicReconcile,
	}
	if err := c.ghClient.ExecuteGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("discover body-marker epic parents for %s/%s: %w", c.owner, c.repo, err)
	}

	cutoff := time.Now().Add(-epicParentDiscoveryLookback)
	seen := make(map[int]bool)
	var parents []int
	for _, node := range result.Repository.Issues.Nodes {
		if node.UpdatedAt.Before(cutoff) {
			// Results are ordered newest-first — everything after this is older still.
			break
		}
		parentNum := github.ParseParentIssueNumber(node.Body)
		if parentNum == 0 || seen[parentNum] {
			continue
		}
		seen[parentNum] = true
		parents = append(parents, parentNum)
	}
	return parents, nil
}

// reconcileEpicParent runs the full close-or-veto decision for a single epic
// parent. Split out from reconcileEpicParents so tests can drive one parent
// directly, mirroring how maybeCloseParentIssue/recoverStaleParentIssues are tested.
func (c *Controller) reconcileEpicParent(ctx context.Context, parentNum int) {
	children, hasLinks, err := c.getAllSubIssueNumbers(ctx, parentNum)
	if err != nil {
		c.log.Warn("reconcileEpicParents: failed to list children", slog.Int("parent", parentNum), slog.Any("error", err))
		return
	}
	if !hasLinks {
		// GH-4099: getAllSubIssueNumbers already tried both the native sub-issue
		// API and the body-marker text-search fallback — neither found a single
		// child. This parent was a candidate (native link or a child's body
		// marker pointed at it), so a silent no-op here would hide exactly the
		// kind of gap that left #4020/#4051 open for hours; log why instead.
		c.log.Warn("reconcileEpicParents: candidate parent has no discoverable children via native links or text search, skipping",
			slog.Int("parent", parentNum))
		return
	}

	openBlocking := 0
	for _, child := range children {
		if child.Closed {
			continue
		}
		if c.isChildNoOp(child.Number) {
			continue
		}
		openBlocking++
	}
	if openBlocking > 0 {
		// Routine, not a veto — an epic mid-flight looks like this on every tick,
		// so this stays at Debug to avoid spamming logs while children finish.
		c.log.Debug("reconcileEpicParents: siblings still open", slog.Int("parent", parentNum), slog.Int("open", openBlocking))
		// The epic re-entered a "still building" phase (a new/reopened sibling) —
		// forget any prior close-veto streak so it doesn't carry over (GH-4006).
		c.clearEpicCloseVeto(parentNum)
		return
	}

	mergedPRs, veto := c.verifyChildrenShippedForClose(ctx, parentNum, children)
	if veto != nil {
		c.log.Warn("reconcileEpicParents: close vetoed",
			slog.Int("parent", parentNum), slog.Int("child", veto.Child), slog.String("veto_reason", veto.Reason))
		if c.recordEpicCloseVeto(parentNum, veto) {
			c.escalateEpicCloseVeto(ctx, parentNum, veto)
		}
		return
	}
	c.clearEpicCloseVeto(parentNum)

	closed, title := c.closeParentNow(ctx, parentNum, mergedPRs)
	// GH-3990: an epic just completed — enqueue its scope release. Gated on
	// closed=true so a no-op/failed close never enqueues a release for work
	// that didn't actually finish closing.
	if closed && c.resolvedRelease().ScopeReleaseEnabled() {
		c.enqueueScopeRelease(ctx, fmt.Sprintf("epic:%d", parentNum), title, mergedPRs)
	}
}

// reconcileClosedEpicScopes sweeps closed, pilot-labeled epic parents updated
// within the configured scope lookback window that have native sub-issues but
// no scope-release row yet, and enqueues one. This covers the window
// reconcileEpicParent's reactive enqueue can miss entirely: the epic closed
// while the daemon was down, or the daemon crashed between closeParentNow
// succeeding and enqueueScopeRelease running (GH-3990).
func (c *Controller) reconcileClosedEpicScopes(ctx context.Context) {
	rel := c.resolvedRelease()
	if !rel.ScopeReleaseEnabled() || c.stateStore == nil {
		return
	}

	lookback := rel.ScopeLookback
	if lookback <= 0 {
		lookback = 24 * time.Hour
	}

	candidates, err := c.searchClosedPilotIssuesWithSubIssues(ctx, maxEpicReconcile, lookback)
	if err != nil {
		c.log.Warn("reconcileClosedEpicScopes: search failed", slog.Any("error", err))
		return
	}

	repo := c.repoKey()
	for _, parentNum := range candidates {
		scopeKey := fmt.Sprintf("epic:%d", parentNum)

		existing, err := c.stateStore.GetScopeRelease(repo, scopeKey)
		if err != nil {
			c.log.Warn("reconcileClosedEpicScopes: failed to check existing scope row",
				slog.Int("parent", parentNum), slog.Any("error", err))
			continue
		}
		if existing != nil {
			// Already enqueued (any state) — idempotent, no further API calls.
			continue
		}

		children, hasLinks, err := c.getAllSubIssueNumbers(ctx, parentNum)
		if err != nil || !hasLinks {
			continue
		}
		mergedPRs, veto := c.verifyChildrenShippedForClose(ctx, parentNum, children)
		if veto != nil {
			c.log.Warn("reconcileClosedEpicScopes: closed parent has an unverified child, skipping enqueue",
				slog.Int("parent", parentNum), slog.Int("child", veto.Child), slog.String("veto_reason", veto.Reason))
			continue
		}

		title := ""
		if issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, parentNum); err == nil {
			title = issue.Title
		}
		c.enqueueScopeRelease(ctx, scopeKey, title, mergedPRs)
	}
}

// searchClosedPilotIssuesWithSubIssues returns issue numbers for CLOSED issues
// labeled both "pilot" and "pilot-done" that have at least one sub-issue and
// were updated within lookback. Mirrors SearchOpenPilotIssuesWithSubIssues's
// query shape (in-package GraphQL, no SDK change — GH-3990); results are
// ordered newest-updated-first so the scan can stop as soon as it crosses the
// lookback cutoff instead of paging through the whole closed-issue history.
func (c *Controller) searchClosedPilotIssuesWithSubIssues(ctx context.Context, limit int, lookback time.Duration) ([]int, error) {
	const query = `query($owner: String!, $repo: String!, $first: Int!) {
		repository(owner: $owner, name: $repo) {
			issues(first: $first, states: [CLOSED], labels: ["pilot", "pilot-done"], orderBy: {field: UPDATED_AT, direction: DESC}) {
				nodes {
					number
					updatedAt
					subIssuesSummary {
						total
					}
				}
			}
		}
	}`

	var result struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					Number           int       `json:"number"`
					UpdatedAt        time.Time `json:"updatedAt"`
					SubIssuesSummary struct {
						Total int `json:"total"`
					} `json:"subIssuesSummary"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}

	variables := map[string]interface{}{
		"owner": c.owner,
		"repo":  c.repo,
		"first": limit,
	}
	if err := c.ghClient.ExecuteGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("search closed pilot issues with sub-issues for %s/%s: %w", c.owner, c.repo, err)
	}

	cutoff := time.Now().Add(-lookback)
	var numbers []int
	for _, node := range result.Repository.Issues.Nodes {
		if node.UpdatedAt.Before(cutoff) {
			// Results are ordered newest-first — everything after this is older still.
			break
		}
		if node.SubIssuesSummary.Total > 0 {
			numbers = append(numbers, node.Number)
		}
	}
	return numbers, nil
}

// recordEpicCloseVeto tracks parentNum's veto against its previous sighting
// (GH-4006). A different blocking child or a different reason resets the
// streak — the epic's blocking condition changed, so the escalation clock
// starts over; the identical signature bumps the count. Returns true exactly
// once, on the pass that first reaches epicCloseVetoBreakerThreshold, so
// escalateEpicCloseVeto fires its label/comment/alert a single time instead of
// repeating on every subsequent poll while the same veto persists.
func (c *Controller) recordEpicCloseVeto(parentNum int, veto *childCloseVeto) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.epicVeto == nil {
		c.epicVeto = make(map[int]*epicCloseVetoTracking)
	}
	st, ok := c.epicVeto[parentNum]
	if !ok || st.child != veto.Child || st.reason != veto.Reason {
		st = &epicCloseVetoTracking{child: veto.Child, reason: veto.Reason}
		c.epicVeto[parentNum] = st
	}
	st.count++
	if st.count >= epicCloseVetoBreakerThreshold && !st.escalated {
		st.escalated = true
		return true
	}
	return false
}

// clearEpicCloseVeto forgets parentNum's close-veto streak (GH-4006), called
// once its children verify shipped or it re-enters a "still building" phase —
// so a resolved stall doesn't leave a stale escalated flag behind that would
// suppress a genuinely new veto on this parent later.
func (c *Controller) clearEpicCloseVeto(parentNum int) {
	c.mu.Lock()
	delete(c.epicVeto, parentNum)
	c.mu.Unlock()
}

// escalateEpicCloseVeto breaks the parent re-dispatch loop once its close-veto
// has persisted for epicCloseVetoBreakerThreshold consecutive reconcile passes
// (GH-4006): adds LabelNeedsClarification — already excluded from dispatch by
// the poller — posts one explanatory comment naming the blocking child and
// why, and fires a single epic_close_vetoed alert. Best-effort throughout:
// errors are logged, never propagated, matching closeParentNow's style.
func (c *Controller) escalateEpicCloseVeto(ctx context.Context, parentNum int, veto *childCloseVeto) {
	c.log.Warn("reconcileEpicParents: close veto persisted, breaking re-dispatch loop",
		slog.Int("parent", parentNum), slog.Int("child", veto.Child), slog.String("veto_reason", veto.Reason),
		slog.Int("attempts", epicCloseVetoBreakerThreshold))

	if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, parentNum, []string{github.LabelNeedsClarification}); err != nil {
		c.log.Warn("escalateEpicCloseVeto: failed to add needs-clarification label",
			slog.Int("parent", parentNum), slog.Any("error", err))
	}

	comment := fmt.Sprintf(
		"⚠️ **Epic auto-close is permanently blocked**\n\n"+
			"GH-%d has failed the shipped-check %d reconcile passes in a row for the same reason: "+
			"child #%d %s.\n\n"+
			"Pilot will not re-dispatch this issue while `%s` is present.\n\n"+
			"**To resume:**\n"+
			"- Close this parent manually if the work is verified complete, or\n"+
			"- Give child #%d a merged PR (or a verified no-op ledger row) referencing it so the next "+
			"reconciliation pass confirms it shipped, then remove the `%s` label.",
		parentNum, epicCloseVetoBreakerThreshold, veto.Child, veto.Reason,
		github.LabelNeedsClarification, veto.Child, github.LabelNeedsClarification,
	)
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, parentNum, comment); err != nil {
		c.log.Warn("escalateEpicCloseVeto: failed to post comment", slog.Int("parent", parentNum), slog.Any("error", err))
	}

	if c.alertsEngine == nil {
		c.log.Error("epic_close_vetoed alert not delivered: SetAlertsEngine was never called", slog.Int("parent", parentNum))
		return
	}
	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventType("epic_close_vetoed"),
		Error:     fmt.Sprintf("epic parent #%d permanently blocked: child #%d %s", parentNum, veto.Child, veto.Reason),
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":   c.repoKey(),
			"parent": strconv.Itoa(parentNum),
			"child":  strconv.Itoa(veto.Child),
			"reason": veto.Reason,
		},
	})
}
