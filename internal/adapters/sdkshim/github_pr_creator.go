package sdkshim

import (
	"context"
	"fmt"
	"strings"

	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// GitHubPRCreator adapts the studio-sdk github client to executor.PRCreator
// (M7 4d.4 — retires the gh-CLI PR path for SDK-managed repos). Unlike the
// gitlab/plane SDK clients, which satisfy PRCreator by signature, the github
// client is multi-repo (owner/repo per call) and returns a struct — so this
// shim closes over one repo and extracts the URL.
type GitHubPRCreator struct {
	client *githubSDK.Client
	owner  string
	repo   string
}

// NewGitHubPRCreator returns a PR creator bound to one owner/repo.
func NewGitHubPRCreator(client *githubSDK.Client, owner, repo string) *GitHubPRCreator {
	return &GitHubPRCreator{client: client, owner: owner, repo: repo}
}

// CreatePR opens a pull request and returns its URL. Idempotent against the
// "already exists" 422 the gh CLI used to absorb: when GitHub rejects the
// create because an open PR for the branch exists, the existing PR's URL is
// recovered and returned instead of an error.
func (g *GitHubPRCreator) CreatePR(ctx context.Context, sourceBranch, targetBranch, title, body string) (string, error) {
	pr, err := g.client.CreatePullRequest(ctx, g.owner, g.repo, &githubSDK.PullRequestInput{
		Title: title,
		Body:  body,
		Head:  sourceBranch,
		Base:  targetBranch,
	})
	if err == nil {
		return pr.HTMLURL, nil
	}

	// GitHub 422: "A pull request already exists for <owner>:<branch>."
	if strings.Contains(err.Error(), "already exists") {
		if url, ferr := g.findOpenPRURL(ctx, sourceBranch); ferr == nil && url != "" {
			return url, nil
		}
	}
	return "", fmt.Errorf("create PR %s -> %s: %w", sourceBranch, targetBranch, err)
}

// findOpenPRURL returns the HTML URL of the open PR whose head is branch, or
// "" when none exists.
func (g *GitHubPRCreator) findOpenPRURL(ctx context.Context, branch string) (string, error) {
	prs, err := g.client.ListPullRequests(ctx, g.owner, g.repo, "open")
	if err != nil {
		return "", err
	}
	for _, pr := range prs {
		if pr.Head.Ref == branch {
			return pr.HTMLURL, nil
		}
	}
	return "", nil
}
