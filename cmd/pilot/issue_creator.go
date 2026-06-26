package main

import (
	"os"
	"strings"

	"github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/config"
)

// buildIssueCreator constructs a comms.IssueCreator backed by the GitHub API.
// Returns nil when GitHub is not configured (no token or no default repo).
// The caller may pass the result directly to comms.HandlerDeps.IssueCreator —
// comms.Handler handles nil gracefully (responds with "not configured").
func buildIssueCreator(cfg *config.Config) comms.IssueCreator {
	if cfg.Adapters == nil || cfg.Adapters.GitHub == nil {
		return nil
	}

	token := cfg.Adapters.GitHub.Token
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return nil
	}

	var defaultOwner, defaultRepo string
	if cfg.Adapters.GitHub.Repo != "" {
		parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
		if len(parts) == 2 {
			defaultOwner = parts[0]
			defaultRepo = parts[1]
		}
	}

	// Build per-project path→owner/repo lookup.
	var projects []github.ProjectEntry
	for _, p := range cfg.Projects {
		if p == nil || p.GitHub == nil || p.GitHub.Owner == "" || p.GitHub.Repo == "" || p.Path == "" {
			continue
		}
		projects = append(projects, github.ProjectEntry{
			Path:  p.Path,
			Owner: p.GitHub.Owner,
			Repo:  p.GitHub.Repo,
		})
	}

	client := github.NewClient(token)
	allow := newConfigIssueAllowlist(cfg)

	return github.NewIssueCreatorAdapter(client, allow, projects, defaultOwner, defaultRepo)
}

// configIssueAllowlist adapts *config.Config to github.IssueAllowlist.
// Mirrors configRepoAllowlist in repo_allowlist.go but for the issue-creation path.
type configIssueAllowlist struct {
	cfg *config.Config
}

func newConfigIssueAllowlist(cfg *config.Config) github.IssueAllowlist {
	if cfg == nil {
		return nil
	}
	return &configIssueAllowlist{cfg: cfg}
}

func (a *configIssueAllowlist) RepoIsAllowed(owner, repo, _ string) bool {
	if a == nil || a.cfg == nil {
		return false
	}
	match := a.cfg.FindProjectByRepo(owner + "/" + repo)
	if match != nil {
		return true
	}
	// Also allow the default repo (adapters.github.repo) even if not in projects list.
	if a.cfg.Adapters != nil && a.cfg.Adapters.GitHub != nil {
		if a.cfg.Adapters.GitHub.Repo == owner+"/"+repo {
			return true
		}
	}
	return false
}

func (a *configIssueAllowlist) ConfiguredRepos() []string {
	if a == nil || a.cfg == nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	if a.cfg.Adapters != nil && a.cfg.Adapters.GitHub != nil && a.cfg.Adapters.GitHub.Repo != "" {
		seen[a.cfg.Adapters.GitHub.Repo] = true
		out = append(out, a.cfg.Adapters.GitHub.Repo)
	}
	for _, p := range a.cfg.Projects {
		if p == nil || p.GitHub == nil || p.GitHub.Owner == "" || p.GitHub.Repo == "" {
			continue
		}
		key := p.GitHub.Owner + "/" + p.GitHub.Repo
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}
