package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/qf-studio/pilot/internal/comms"
)

// ProjectEntry maps a local filesystem path to a GitHub owner/repo.
type ProjectEntry struct {
	Path  string
	Owner string
	Repo  string
}

// IssueCreatorAdapter implements comms.IssueCreator using the GitHub REST API.
// It resolves the active project path to an owner/repo pair, then delegates to
// CreatePilotIssue for conventional-commit title validation and allowlist enforcement.
type IssueCreatorAdapter struct {
	client       *Client
	allow        IssueAllowlist
	projects     []ProjectEntry
	defaultOwner string
	defaultRepo  string
}

// NewIssueCreatorAdapter constructs an IssueCreatorAdapter.
//
// projects lists all configured projects for path→owner/repo resolution.
// defaultOwner/defaultRepo are used when projectPath does not match any entry.
// allow enforces the repo allowlist (pass AllowAllIssueRepos() when the caller
// has already constrained the target to trusted config).
func NewIssueCreatorAdapter(client *Client, allow IssueAllowlist, projects []ProjectEntry, defaultOwner, defaultRepo string) *IssueCreatorAdapter {
	return &IssueCreatorAdapter{
		client:       client,
		allow:        allow,
		projects:     projects,
		defaultOwner: defaultOwner,
		defaultRepo:  defaultRepo,
	}
}

// CreateIssue implements comms.IssueCreator.
// It resolves projectPath → owner/repo, then calls CreatePilotIssue which
// enforces the conventional-commit title format and the repo allowlist.
func (a *IssueCreatorAdapter) CreateIssue(ctx context.Context, projectPath string, d comms.IssueDraft) (string, error) {
	owner, repo := a.resolveRepo(projectPath)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("cannot resolve GitHub owner/repo for project path %q", projectPath)
	}

	issue, err := CreatePilotIssue(ctx, a.client, a.allow, owner, repo, d.Title, d.Body, d.Labels)
	if err != nil {
		return "", err
	}
	return issue.HTMLURL, nil
}

// resolveRepo maps a filesystem path to (owner, repo).
// Returns (defaultOwner, defaultRepo) when no project matches.
func (a *IssueCreatorAdapter) resolveRepo(projectPath string) (string, string) {
	for _, p := range a.projects {
		if p.Path != "" && strings.EqualFold(p.Path, projectPath) {
			return p.Owner, p.Repo
		}
	}
	return a.defaultOwner, a.defaultRepo
}
