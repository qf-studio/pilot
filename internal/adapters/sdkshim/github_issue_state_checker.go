package sdkshim

import (
	"context"
	"strings"

	"github.com/qf-studio/pilot/internal/executor"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GitHubIssueStateChecker adapts the studio-sdk github client to
// executor.IssueStateChecker (GH-4656). Registered per-repo alongside
// GitHubPRCreator so the pickup-time and PR-creation preflight guards read
// issue state through the same in-process, ghbudget-visible client used for
// PR creation instead of an untracked `gh` CLI subprocess. Unlike
// GitHubPRCreator, this shim is not bound to a fixed owner/repo — the
// executor.IssueStateChecker interface takes owner/repo per call (mirroring
// the gh-CLI fallback, which is genuinely repo-agnostic) — but a fresh
// checker is still registered per repo (RegisterIssueStateChecker key
// "github:owner/repo"), one per registered PR-creator client.
type GitHubIssueStateChecker struct {
	client *githubSDK.Client
}

// NewGitHubIssueStateChecker returns an issue-state checker over client.
func NewGitHubIssueStateChecker(client *githubSDK.Client) *GitHubIssueStateChecker {
	return &GitHubIssueStateChecker{client: client}
}

// GetIssueState fetches the live state of issue number via a single GetIssue
// call.
func (g *GitHubIssueStateChecker) GetIssueState(ctx context.Context, owner, repo string, number int) (executor.IssueState, error) {
	issue, err := g.client.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return executor.IssueState{}, err
	}
	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, l.Name)
	}
	return executor.IssueState{
		Closed: strings.EqualFold(strings.TrimSpace(issue.State), "closed"),
		Labels: labels,
	}, nil
}
