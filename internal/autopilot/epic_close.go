package autopilot

import (
	"context"
	"fmt"
	"strings"
)

// epicChild is one child issue of a decomposed epic-parent, as returned by
// allSubIssueStates: its number and whether it is still open. GH-3939.
type epicChild struct {
	Number int
	Open   bool
}

// allSubIssueStates queries the native GitHub sub-issues GraphQL API for every
// child of parentNum, regardless of state. Unlike GetOpenSubIssueNumbers
// (which filters to OPEN only, since the merge-triggered maybeCloseParentIssue
// and startup-only recoverStaleParentIssues paths only need a count of what's
// still blocking), pollCloseEpicParents needs the CLOSED children too, so it
// can verify each one individually shipped via a merged PR rather than trust
// "closed" alone. hasNativeLinks is false when the parent has no native
// sub-issue links at all (totalCount==0) — the poll-cycle check then has no
// concrete child list to verify and skips the parent, leaving it to the
// coarser count-based recovery/merge paths. GH-3939.
func (c *Controller) allSubIssueStates(ctx context.Context, parentNum int) (children []epicChild, hasNativeLinks bool, err error) {
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

	children = make([]epicChild, 0, len(result.Node.SubIssues.Nodes))
	for _, n := range result.Node.SubIssues.Nodes {
		children = append(children, epicChild{Number: n.Number, Open: n.State == "OPEN"})
	}
	return children, true, nil
}

// pollCloseEpicParents runs on autopilot's periodic poll cycle (wired into
// startReconciler) and closes decomposed epic-parent issues once every child
// is BOTH closed and shipped via a merged PR, posting a summary comment that
// names each merged PR. GH-3939.
//
// This is a stricter gate than openSubIssueCount (used by the merge-triggered
// maybeCloseParentIssue and the startup-only recoverStaleParentIssues): those
// two only check that no child issue is still OPEN, so a child that was
// closed without shipping (wontfix, duplicate, or a PR closed instead of
// merged) would let the parent close having delivered less than the epic
// promised. pollCloseEpicParents additionally requires a merged PR per child.
func (c *Controller) pollCloseEpicParents(ctx context.Context) {
	const maxCandidates = 50
	candidates, err := c.ghClient.SearchOpenPilotIssuesWithSubIssues(ctx, c.owner, c.repo, maxCandidates)
	if err != nil {
		c.log.Warn("pollCloseEpicParents: search for epic-parent candidates failed", "error", err)
		return
	}

	for _, parentNum := range candidates {
		c.closeEpicParentIfChildrenShipped(ctx, parentNum)
	}
}

// closeEpicParentIfChildrenShipped is the per-parent worker for
// pollCloseEpicParents. Every close-guard veto (an open child, a closed child
// with no merged PR, an open PR still referencing the parent, or a lookup
// failure) is logged with an explicit reason and the function returns without
// touching the parent — no retry, no repeated GitHub writes. Seeing the same
// veto reason on consecutive ticks is expected while an epic is in progress;
// it is not evidence of a stuck loop, so callers scraping these logs for a
// dedupe/suppression signal can key off the reason string instead of
// interpreting "checked again" as "something is wrong". GH-3939.
func (c *Controller) closeEpicParentIfChildrenShipped(ctx context.Context, parentNum int) {
	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, parentNum)
	if err != nil {
		c.log.Warn("pollCloseEpicParents: veto — failed to fetch parent issue",
			"parent", parentNum, "reason", err.Error())
		return
	}
	if issue == nil || issue.State == "closed" {
		// Already closed: a no-op, not a veto — nothing pending for a dedupe
		// path to suppress.
		return
	}

	// Close-guard: an open PR that still references the parent issue number
	// itself (as opposed to one of its children) means work against this
	// epic hasn't landed yet, even if every decomposed child is done.
	openParentPRs, err := c.ghClient.SearchOpenPRsForIssue(ctx, c.owner, c.repo, parentNum)
	if err != nil {
		c.log.Warn("pollCloseEpicParents: veto — failed to check for open PRs referencing parent",
			"parent", parentNum, "reason", err.Error())
		return
	}
	if len(openParentPRs) > 0 {
		c.log.Warn("pollCloseEpicParents: veto — an open PR still references the parent issue",
			"parent", parentNum, "reason", "open PR references parent", "open_pr_count", len(openParentPRs))
		return
	}

	children, hasNativeLinks, err := c.allSubIssueStates(ctx, parentNum)
	if err != nil {
		c.log.Warn("pollCloseEpicParents: veto — failed to list child issues",
			"parent", parentNum, "reason", err.Error())
		return
	}
	if !hasNativeLinks {
		c.log.Debug("pollCloseEpicParents: no native sub-issue links, deferring to count-based recovery path",
			"parent", parentNum)
		return
	}

	var mergedPRs []string
	for _, child := range children {
		if child.Open {
			// Expected, high-frequency state while an epic is still in progress —
			// log at Debug so it never spams Info/Warn on every poll cycle.
			c.log.Debug("pollCloseEpicParents: child issue still open, deferring parent close",
				"parent", parentNum, "child", child.Number)
			return
		}

		prs, err := c.ghClient.SearchPRsForIssue(ctx, c.owner, c.repo, child.Number)
		if err != nil {
			c.log.Warn("pollCloseEpicParents: veto — merged-PR lookup failed for closed child",
				"parent", parentNum, "child", child.Number, "reason", err.Error())
			return
		}

		mergedURL := ""
		for _, pr := range prs {
			if pr.Merged {
				mergedURL = pr.HTMLURL
				break
			}
		}
		if mergedURL == "" {
			c.log.Warn("pollCloseEpicParents: veto — child closed but no merged PR references it",
				"parent", parentNum, "child", child.Number, "reason", "child closed without a merged PR")
			return
		}
		mergedPRs = append(mergedPRs, mergedURL)
	}

	c.closeParentWithMergedPRs(ctx, parentNum, mergedPRs)
}

// closeParentWithMergedPRs closes an epic-parent issue whose children are all
// closed and individually verified merged, posting a summary comment that
// names every merged PR. Mirrors closeParentNow's label cleanup but with a
// PR-specific comment instead of the generic "sub-issues complete" message.
// All errors are logged as warnings without blocking the rest of the poll
// cycle. GH-3939.
func (c *Controller) closeParentWithMergedPRs(ctx context.Context, parentNum int, mergedPRs []string) {
	c.log.Info("pollCloseEpicParents: all children closed and merged, closing epic parent",
		"parent", parentNum, "merged_prs", mergedPRs)

	if err := c.ghClient.AddLabels(ctx, c.owner, c.repo, parentNum, []string{"pilot-done"}); err != nil {
		c.log.Warn("pollCloseEpicParents: failed to add pilot-done label", "parent", parentNum, "error", err)
	}
	for _, stale := range []string{"pilot-failed", "pilot-in-progress", "pilot-blocked"} {
		if err := c.ghClient.RemoveLabel(ctx, c.owner, c.repo, parentNum, stale); err != nil {
			c.log.Warn("pollCloseEpicParents: failed to remove label", "label", stale, "parent", parentNum, "error", err)
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("All child issues for GH-%d are closed and shipped. Closing parent issue automatically.\n\n", parentNum))
	sb.WriteString("Merged PRs:\n")
	for _, prURL := range mergedPRs {
		sb.WriteString(fmt.Sprintf("- %s\n", prURL))
	}
	if _, err := c.ghClient.AddComment(ctx, c.owner, c.repo, parentNum, sb.String()); err != nil {
		c.log.Warn("pollCloseEpicParents: failed to post summary comment", "parent", parentNum, "error", err)
	}

	if err := c.ghClient.UpdateIssueState(ctx, c.owner, c.repo, parentNum, "closed"); err != nil {
		c.log.Warn("pollCloseEpicParents: failed to close parent issue", "parent", parentNum, "error", err)
	}
}
