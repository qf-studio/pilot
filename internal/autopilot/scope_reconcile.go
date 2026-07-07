package autopilot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/alerts"
)

// maxLabelScopeReconcile caps how many scope:<name>-prefixed label candidates
// a single reconcileLabelScopes sweep inspects per tick. Mirrors
// maxEpicReconcile — bounded so a repo with many scope labels can't turn one
// poll cycle into an unbounded API burst (GH-3991).
const maxLabelScopeReconcile = 50

// maxLabelScopeMembers caps how many issues a single label's membership query
// fetches. A label with more members than this is WARN-logged and only the
// first page is considered — no SDK change, no pagination (GH-3991).
const maxLabelScopeMembers = 100

// scopeMemberIssue is one issue carrying a release-scope label, with just
// enough signal to run the completion gate and the abandoned-scope check.
type scopeMemberIssue struct {
	Number    int
	Closed    bool
	ClosedAt  time.Time
	UpdatedAt time.Time
}

// reconcileLabelScopes is the poll-cycle label-scope completion sweep
// (GH-3991) — the second scope kind alongside epic parents: sibling issues
// grouped by a shared "scope:<name>" label with no epic parent (e.g.
// TASK-388's dependency-chained issues). Mirrors reconcileEpicParents:
// discover candidates cheaply, then hand each to the per-scope reconciler.
func (c *Controller) reconcileLabelScopes(ctx context.Context) {
	rel := c.resolvedRelease()
	if !rel.ScopeReleaseEnabled() || c.stateStore == nil {
		return
	}

	prefix := rel.effectiveScopeLabelPrefix()
	names, err := c.searchScopeLabelCandidates(ctx, prefix, maxLabelScopeReconcile)
	if err != nil {
		c.log.Warn("reconcileLabelScopes: label search failed", slog.Any("error", err))
		return
	}
	if len(names) == maxLabelScopeReconcile {
		c.log.Info("reconcileLabelScopes: hit limit, some candidates may be skipped", slog.Int("limit", maxLabelScopeReconcile))
	}

	repo := c.repoKey()
	terminal, err := c.stateStore.ListScopeReleases(repo, "done", "failed")
	if err != nil {
		c.log.Warn("reconcileLabelScopes: failed to list terminal scope rows", slog.Any("error", err))
		terminal = nil
	}
	terminalKeys := make(map[string]bool, len(terminal))
	for _, row := range terminal {
		terminalKeys[row.ScopeKey] = true
	}

	lowerPrefix := strings.ToLower(prefix)
	for _, name := range names {
		// GitHub's labels(query:) match is fuzzy (substring), not anchored —
		// re-check the exact prefix here, matching heldByScope's convention.
		if !strings.HasPrefix(strings.ToLower(name), lowerPrefix) {
			continue
		}
		scopeKey := "label:" + name[len(prefix):]
		if terminalKeys[scopeKey] {
			// Already done/failed — zero further API calls for this label.
			continue
		}
		c.reconcileLabelScope(ctx, prefix, name)
	}
}

// reconcileLabelScope runs the completion-or-veto decision for a single
// scope:<name> label. Split out from reconcileLabelScopes so tests can drive
// one label directly, mirroring reconcileEpicParent.
func (c *Controller) reconcileLabelScope(ctx context.Context, prefix, labelName string) {
	rel := c.resolvedRelease()
	if rel == nil {
		return
	}
	scopeKey := "label:" + labelName[len(prefix):]

	members, err := c.fetchLabelScopeMembers(ctx, labelName, maxLabelScopeMembers)
	if err != nil {
		c.log.Warn("reconcileLabelScopes: failed to fetch label members", slog.String("label", labelName), slog.Any("error", err))
		return
	}
	if len(members) == 0 {
		return
	}

	children := make([]subIssueState, 0, len(members))
	var newestClosed time.Time
	openCount := 0
	for _, m := range members {
		children = append(children, subIssueState{Number: m.Number, Closed: m.Closed})
		if m.Closed {
			if m.ClosedAt.After(newestClosed) {
				newestClosed = m.ClosedAt
			}
		} else {
			openCount++
		}
	}

	if openCount > 0 {
		// Routine — a scope mid-flight looks like this on every tick, so this
		// stays at Debug (mirrors reconcileEpicParent's openBlocking log).
		// mergedPRs here is only the "at least one shipped member" signal for
		// the abandoned-scope alert, not a completion decision.
		c.log.Debug("reconcileLabelScopes: label has open members, skipping completion",
			slog.String("label", labelName), slog.Int("open", openCount))
		if mergedPRs, _ := c.verifyChildrenShippedForClose(ctx, 0, children); len(mergedPRs) > 0 {
			c.maybeAlertStaleScope(scopeKey, labelName, members, rel)
		}
		return
	}

	lookback := rel.ScopeLookback
	if lookback <= 0 {
		lookback = 24 * time.Hour
	}
	if newestClosed.Before(time.Now().Add(-lookback)) {
		// Every member closed, but the newest close predates the lookback
		// window — a pre-feature completed scope. Never enqueue, and skip the
		// PR-search verification entirely since there's nothing to complete
		// (GH-3991).
		c.log.Debug("reconcileLabelScopes: label completed before lookback window, skipping",
			slog.String("label", labelName))
		return
	}

	mergedPRs, veto := c.verifyChildrenShippedForClose(ctx, 0, children)
	if veto != nil {
		c.log.Warn("reconcileLabelScopes: scope vetoed",
			slog.String("label", labelName), slog.Int("child", veto.Child), slog.String("veto_reason", veto.Reason))
		return
	}
	if len(mergedPRs) == 0 {
		return
	}

	c.enqueueScopeRelease(ctx, scopeKey, labelName, mergedPRs)
}

// searchScopeLabelCandidates returns every label name matching prefix, up to
// limit. GitHub's `labels(query:)` match is fuzzy, so callers must re-check
// the exact prefix. In-package GraphQL, no SDK method added — mirrors
// getAllSubIssueNumbers's precedent (GH-3991).
func (c *Controller) searchScopeLabelCandidates(ctx context.Context, prefix string, limit int) ([]string, error) {
	const query = `query($owner: String!, $repo: String!, $prefix: String!, $first: Int!) {
		repository(owner: $owner, name: $repo) {
			labels(query: $prefix, first: $first) {
				nodes {
					name
				}
			}
		}
	}`

	var result struct {
		Repository struct {
			Labels struct {
				Nodes []struct {
					Name string `json:"name"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"repository"`
	}

	variables := map[string]interface{}{
		"owner":  c.owner,
		"repo":   c.repo,
		"prefix": prefix,
		"first":  limit,
	}
	if err := c.ghClient.ExecuteGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("search scope labels for %s/%s: %w", c.owner, c.repo, err)
	}

	names := make([]string, 0, len(result.Repository.Labels.Nodes))
	for _, n := range result.Repository.Labels.Nodes {
		names = append(names, n.Name)
	}
	return names, nil
}

// fetchLabelScopeMembers returns every issue (open and closed) carrying
// labelName, up to limit. In-package GraphQL (labels -> issues), no SDK
// method added — mirrors searchClosedPilotIssuesWithSubIssues's precedent.
// Caps at limit with a WARN so an unexpectedly large scope label degrades to
// "fewer members considered" rather than paging indefinitely (GH-3991).
func (c *Controller) fetchLabelScopeMembers(ctx context.Context, labelName string, limit int) ([]scopeMemberIssue, error) {
	const query = `query($owner: String!, $repo: String!, $label: String!, $first: Int!) {
		repository(owner: $owner, name: $repo) {
			labels(query: $label, first: 1) {
				nodes {
					name
					issues(states: [OPEN, CLOSED], first: $first) {
						totalCount
						nodes {
							number
							state
							closedAt
							updatedAt
						}
					}
				}
			}
		}
	}`

	var result struct {
		Repository struct {
			Labels struct {
				Nodes []struct {
					Name   string `json:"name"`
					Issues struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							Number    int       `json:"number"`
							State     string    `json:"state"`
							ClosedAt  time.Time `json:"closedAt"`
							UpdatedAt time.Time `json:"updatedAt"`
						} `json:"nodes"`
					} `json:"issues"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"repository"`
	}

	variables := map[string]interface{}{
		"owner": c.owner,
		"repo":  c.repo,
		"label": labelName,
		"first": limit,
	}
	if err := c.ghClient.ExecuteGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("fetch members for label %q in %s/%s: %w", labelName, c.owner, c.repo, err)
	}

	for _, node := range result.Repository.Labels.Nodes {
		if node.Name != labelName {
			// labels(query:) is fuzzy — only the exact label's issues count.
			continue
		}
		if node.Issues.TotalCount > limit {
			c.log.Warn("fetchLabelScopeMembers: label has more members than the fetch cap, results truncated",
				slog.String("label", labelName), slog.Int("total", node.Issues.TotalCount), slog.Int("limit", limit))
		}
		members := make([]scopeMemberIssue, 0, len(node.Issues.Nodes))
		for _, issue := range node.Issues.Nodes {
			members = append(members, scopeMemberIssue{
				Number:    issue.Number,
				Closed:    issue.State == "CLOSED",
				ClosedAt:  issue.ClosedAt,
				UpdatedAt: issue.UpdatedAt,
			})
		}
		return members, nil
	}
	return nil, nil
}

// maybeAlertStaleScope fires a scope_stale alert when a label scope has at
// least one shipped member (mergedPRs non-empty in the caller) and at least
// one open member that has sat untouched past ScopeStaleAfter — an abandoned
// scope: the label was likely left on stragglers that will never close,
// silently holding their PRs forever (GH-3991).
func (c *Controller) maybeAlertStaleScope(scopeKey, labelName string, members []scopeMemberIssue, rel *ReleaseConfig) {
	staleAfter := rel.effectiveScopeStaleAfter()
	cutoff := time.Now().Add(-staleAfter)

	stale := false
	for _, m := range members {
		if !m.Closed && m.UpdatedAt.Before(cutoff) {
			stale = true
			break
		}
	}
	if !stale {
		return
	}
	c.fireScopeStaleAlert(scopeKey, labelName, staleAfter)
}

// fireScopeStaleAlert fires a scope_stale alert, deduplicated per scopeKey via
// alertedStaleScopes — mirrors fireReleaseMissingAlert's alertedMissingReleases
// dedup pattern, since the alerts engine's own cooldown is keyed by rule name,
// not by source (GH-3991).
func (c *Controller) fireScopeStaleAlert(scopeKey, labelName string, staleAfter time.Duration) {
	c.mu.Lock()
	if c.alertedStaleScopes == nil {
		c.alertedStaleScopes = make(map[string]bool)
	}
	if c.alertedStaleScopes[scopeKey] {
		c.mu.Unlock()
		return
	}
	c.alertedStaleScopes[scopeKey] = true
	c.mu.Unlock()

	msg := fmt.Sprintf(
		"scope %s (label %q) has shipped members but at least one open member has sat untouched past %s — possible abandoned scope",
		scopeKey, labelName, staleAfter,
	)
	if c.alertsEngine == nil {
		c.log.Error("scope_stale alert not delivered: SetAlertsEngine was never called", slog.String("scope", scopeKey))
		return
	}
	c.alertsEngine.ProcessEvent(alerts.Event{
		Type:      alerts.EventType("scope_stale"),
		Error:     msg,
		Timestamp: time.Now(),
		Metadata: map[string]string{
			"repo":  c.repoKey(),
			"scope": scopeKey,
		},
	})
}
