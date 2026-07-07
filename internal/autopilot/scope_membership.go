package autopilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/qf-studio/pilot/internal/adapters/github"
)

// heldByScope determines whether a merged PR's linked issue belongs to a
// release scope that Trigger "on_scope_close" is configured to hold: either
// an open epic (the issue's parent, per github.ParseParentIssueNumber — the
// same resolution controller.go's maybeCloseParentIssue uses) or a shared
// "scope:"-prefixed label. The epic check wins when both are present, so
// scope membership is always deterministic for a given issue.
//
// Any GitHub API error along the way fails OPEN (not held, scopeKey/title
// empty) rather than wedging a release forever on a transient lookup
// failure — held work is fully reconstructable from GitHub, so a false
// "not held" merely releases a little early, while a false "held" could hang
// indefinitely (GH-3989).
func (c *Controller) heldByScope(ctx context.Context, issueNum int) (scopeKey, scopeTitle string, held bool) {
	rel := c.resolvedRelease()
	if !rel.ScopeReleaseEnabled() {
		return "", "", false
	}
	if issueNum == 0 {
		return "", "", false
	}

	issue, err := c.ghClient.GetIssue(ctx, c.owner, c.repo, issueNum)
	if err != nil {
		c.log.Warn("heldByScope: failed to fetch issue, failing open (not held)",
			"issue", issueNum, "error", err)
		return "", "", false
	}

	if parentNum := github.ParseParentIssueNumber(issue.Body); parentNum != 0 {
		parent, parentErr := c.ghClient.GetIssue(ctx, c.owner, c.repo, parentNum)
		switch {
		case parentErr != nil:
			c.log.Debug("heldByScope: failed to fetch epic parent, falling through to label check",
				"issue", issueNum, "parent", parentNum, "error", parentErr)
		case parent.State == "open":
			return fmt.Sprintf("epic:%d", parentNum), parent.Title, true
		default:
			// Parent already closed: a late straggler merge — release per-merge
			// rather than holding on a scope that has already shipped.
		}
	}

	prefix := strings.ToLower(rel.effectiveScopeLabelPrefix())
	for _, label := range issue.Labels {
		if strings.HasPrefix(strings.ToLower(label.Name), prefix) {
			return "label:" + label.Name[len(prefix):], label.Name, true
		}
	}

	return "", "", false
}
