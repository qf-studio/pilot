package autopilot

import (
	"context"
	"fmt"
	"log/slog"
)

// maxEpicReconcile caps how many open, pilot-labeled parent-with-sub-issues
// candidates a single reconcileEpicParents sweep fetches. Mirrors
// recoverStaleParentIssues' maxRecover — bounded so one pathological repo with
// hundreds of open epics can't turn a poll tick into an unbounded API burst.
const maxEpicReconcile = 50

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
		return
	}

	mergedPRs, veto := c.verifyChildrenShippedForClose(ctx, parentNum, children)
	if veto != nil {
		c.log.Warn("reconcileEpicParents: close vetoed",
			slog.Int("parent", parentNum), slog.Int("child", veto.Child), slog.String("veto_reason", veto.Reason))
		return
	}

	c.closeParentNow(ctx, parentNum, mergedPRs)
}
