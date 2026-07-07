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
// parent has any native sub-issue links at all. This mirrors the SDK's
// GetOpenSubIssueNumbers query shape but keeps closed children too — GH-3939's
// merged-PR verification must inspect exactly the children that open-only
// listings discard.
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
		return nil, false, nil
	}

	children := make([]subIssueState, 0, len(result.Node.SubIssues.Nodes))
	for _, n := range result.Node.SubIssues.Nodes {
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
// On each tick it inspects every open, pilot-labeled issue that has native
// GitHub sub-issues (a decomposed epic parent) and closes the ones whose
// children are ALL closed AND shipped a merged PR (or verified no_op).
//
// This complements the two existing close paths: maybeCloseParentIssue fires
// reactively only when a sibling's own PR merges, and recoverStaleParentIssues
// runs once at startup. Neither revisits a parent left behind by some other
// event (a child closed out-of-band, a webhook missed) — this sweep does, so
// such a parent converges within one poll cycle instead of staying stuck until
// the next restart.
func (c *Controller) reconcileEpicParents(ctx context.Context) {
	candidates, err := c.ghClient.SearchOpenPilotIssuesWithSubIssues(ctx, c.owner, c.repo, maxEpicReconcile)
	if err != nil {
		c.log.Warn("reconcileEpicParents: search failed", slog.Any("error", err))
		return
	}

	// GH-3939: mirror recoverStaleParentIssues' cap-hit log — this sweep repeats
	// every poll cycle (not just once at startup), so a repo that consistently
	// sits at the cap would otherwise silently and indefinitely skip the overflow
	// candidates with no operator-visible signal.
	if len(candidates) == maxEpicReconcile {
		c.log.Info("reconcileEpicParents: hit limit, some candidates may be skipped", slog.Int("limit", maxEpicReconcile))
	}

	for _, parentNum := range candidates {
		c.reconcileEpicParent(ctx, parentNum)
	}
}

// reconcileEpicParent runs the full close-or-veto decision for a single epic
// parent. Split out from reconcileEpicParents so tests can drive one parent
// directly, mirroring how maybeCloseParentIssue/recoverStaleParentIssues are tested.
func (c *Controller) reconcileEpicParent(ctx context.Context, parentNum int) {
	children, hasNativeLinks, err := c.getAllSubIssueNumbers(ctx, parentNum)
	if err != nil {
		c.log.Warn("reconcileEpicParents: failed to list children", slog.Int("parent", parentNum), slog.Any("error", err))
		return
	}
	if !hasNativeLinks {
		// No native sub-issue links to verify against — leave this parent to the
		// text-search-based paths (maybeCloseParentIssue / recoverStaleParentIssues),
		// which is the only signal available for pre-native-linking epics.
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

		children, hasNativeLinks, err := c.getAllSubIssueNumbers(ctx, parentNum)
		if err != nil || !hasNativeLinks {
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
