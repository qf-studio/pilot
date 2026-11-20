package config

import (
	"fmt"
	"strings"
)

// ProjectForgejoConfig holds Forgejo/Gitea-specific project configuration for
// PR creation (mirroring ProjectGitLabConfig / GH-5583). The access token stays
// global (adapters.forgejo.token).
type ProjectForgejoConfig struct {
	// Repo is the Forgejo repository in "owner/repo" form (required).
	Repo string `yaml:"repo"`
	// BaseURL is the Forgejo instance URL (e.g. "http://rome.tail3ce82d.ts.net:30030").
	// Optional: defaults to adapters.forgejo.base_url.
	BaseURL string `yaml:"base_url,omitempty"`
}

// ResolveForgejoBaseURL returns the effective Forgejo base URL for a project's
// forgejo block: its own base_url, else adapters.forgejo.base_url.
func (c *Config) ResolveForgejoBaseURL(p *ProjectForgejoConfig) string {
	if p != nil && strings.TrimSpace(p.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	}
	if c != nil && c.Adapters != nil && c.Adapters.Forgejo != nil && strings.TrimSpace(c.Adapters.Forgejo.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(c.Adapters.Forgejo.BaseURL), "/")
	}
	return ""
}

// validForgejoRepoPath reports whether s is a strict "owner/repo" path:
// exactly two non-empty, whitespace-free segments with no leading/trailing slash.
func validForgejoRepoPath(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, " \t\r\n") {
			return false
		}
	}
	return true
}

// validateProjectForgejo checks every projects[].forgejo block:
// repo must be owner/repo, a project may not combine forgejo with github or
// gitlab, a base_url must resolve (either on the project or adapters.forgejo),
// and adapters.forgejo.token must be set.
func (c *Config) validateProjectForgejo() error {
	for i, p := range c.Projects {
		if p == nil || p.Forgejo == nil {
			continue
		}
		name := p.Name
		if name == "" {
			name = fmt.Sprintf("#%d", i)
		}
		if !validForgejoRepoPath(p.Forgejo.Repo) {
			return fmt.Errorf("projects[%d] (%s): forgejo.repo must be owner/repo, got %q", i, name, p.Forgejo.Repo)
		}
		if p.GitHub != nil {
			return fmt.Errorf("projects[%d] (%s): a project may not declare both a github and a forgejo block", i, name)
		}
		if p.GitLab != nil {
			return fmt.Errorf("projects[%d] (%s): a project may not declare both a gitlab and a forgejo block", i, name)
		}
		if c.ResolveForgejoBaseURL(p.Forgejo) == "" {
			return fmt.Errorf("projects[%d] (%s): forgejo: block requires forgejo.base_url or adapters.forgejo.base_url", i, name)
		}
		if c.Adapters == nil || c.Adapters.Forgejo == nil || c.Adapters.Forgejo.Token == "" {
			return fmt.Errorf("projects[%d] (%s): forgejo: block requires adapters.forgejo.token to be set", i, name)
		}
	}
	return nil
}
