package main

import (
	"context"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// githubBasePresenceProbe adapts internal/adapters/github.Client to
// executor.BasePresenceProbe (GH-5053). *github.Client already implements
// FileExistsOnDefaultBranch and IssueOrPRState with signatures matching
// executor.BasePresenceProbe byte-for-byte (repo_state.go, GH-5046) — this
// wrapper only needs to add LinkedPRNumbers, via the existing
// SearchPRsForIssue GitHub Search API call (client.go).
type githubBasePresenceProbe struct {
	*github.Client
}

// LinkedPRNumbers implements executor.BasePresenceProbe by mapping
// SearchPRsForIssue's []*github.PullRequest results down to bare PR
// numbers — basePresenceChecker only needs the number to re-probe each
// candidate via IssueOrPRState.
func (p githubBasePresenceProbe) LinkedPRNumbers(ctx context.Context, owner, repo string, issueNumber int) ([]int, error) {
	prs, err := p.SearchPRsForIssue(ctx, owner, repo, issueNumber)
	if err != nil {
		return nil, err
	}
	numbers := make([]int, 0, len(prs))
	for _, pr := range prs {
		numbers = append(numbers, pr.Number)
	}
	return numbers, nil
}

// newGitHubBasePresenceProbe builds a base-presence probe (GH-5053) over an
// in-tree GitHub client whose token is re-resolved per request
// (newGitHubClient, main.go — GH-4747), for registration at composition
// time in place of the gh-CLI shellout fallback (ghCLIBasePresenceProbe,
// internal/executor/base_presence.go) — that fallback remains the
// unregistered default for any repo this probe is never registered for.
// Returned as the executor.BasePresenceProbe interface so callers
// (poller_github.go) never need to import internal/adapters/github
// directly — that file is SDK-poller-only by convention
// (TestGithubPollerNoLegacyImport, poller_github_test.go).
func newGitHubBasePresenceProbe(cfg *config.Config) executor.BasePresenceProbe {
	return githubBasePresenceProbe{Client: newGitHubClient(cfg)}
}
