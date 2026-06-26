package github

import (
	"context"
	"fmt"

	"github.com/qf-studio/pilot/internal/comms"
)

// issueCreator implements comms.IssueCreator using the GitHub REST API.
// It is scoped to a single owner/repo pair; projectPath is accepted for
// interface compatibility and used as a tie-breaker when multiple repos are
// registered (falls back to the first entry when no match is found).
type issueCreator struct {
	client    *Client
	allowlist IssueAllowlist
	repos     []issueCreatorRepo
}

type issueCreatorRepo struct {
	projectPath string // local disk path (may be empty = match-all)
	owner       string
	repo        string
}

// NewIssueCreator returns a comms.IssueCreator backed by the GitHub API.
// repos maps local project paths to owner/repo pairs; the first entry whose
// projectPath matches (or is empty) wins.  Pass at least one entry.
func NewIssueCreator(client *Client, allowlist IssueAllowlist, entries ...IssueCreatorEntry) comms.IssueCreator {
	repos := make([]issueCreatorRepo, len(entries))
	for i, e := range entries {
		repos[i] = issueCreatorRepo{projectPath: e.ProjectPath, owner: e.Owner, repo: e.Repo}
	}
	return &issueCreator{client: client, allowlist: allowlist, repos: repos}
}

// IssueCreatorEntry maps a local project path to a GitHub owner/repo.
type IssueCreatorEntry struct {
	ProjectPath string // local disk path; empty means "match any"
	Owner       string
	Repo        string
}

// CreateIssue implements comms.IssueCreator.
func (c *issueCreator) CreateIssue(ctx context.Context, projectPath string, d comms.IssueDraft) (string, error) {
	owner, repo, err := c.resolveRepo(projectPath)
	if err != nil {
		return "", err
	}
	issue, err := CreatePilotIssue(ctx, c.client, c.allowlist, owner, repo, d.Title, d.Body, d.Labels)
	if err != nil {
		return "", fmt.Errorf("github issue creation failed: %w", err)
	}
	return issue.HTMLURL, nil
}

func (c *issueCreator) resolveRepo(projectPath string) (owner, repo string, err error) {
	// Exact path match first
	for _, r := range c.repos {
		if r.projectPath == projectPath {
			return r.owner, r.repo, nil
		}
	}
	// Empty projectPath or no match → first entry
	if len(c.repos) > 0 {
		return c.repos[0].owner, c.repos[0].repo, nil
	}
	return "", "", fmt.Errorf("no GitHub repo configured for project path %q", projectPath)
}
