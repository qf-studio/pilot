// repo_allowlist.go — TASK-286 / GH-3027
//
// Concrete adapter implementing executor.RepoAllowlist over *config.Config.
// Lives in cmd/pilot/ (not internal/executor/) so the executor package
// remains decoupled from the top-level config types — the same reason
// FindProjectByRepo is only invoked from cmd/pilot in the existing codebase.

package main

import (
	"fmt"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
)

// configRepoAllowlist adapts *config.Config to the
// executor.RepoAllowlist interface used by the sub-issue creation
// guardrail.
type configRepoAllowlist struct {
	cfg *config.Config
}

// newConfigRepoAllowlist constructs an allowlist backed by cfg. cfg must
// not be nil; pass nil to Runner.SetRepoAllowlist if you genuinely want to
// disable the allowlist (the guardrail will then refuse without
// PILOT_ALLOW_UNMANAGED_REPO=1).
func newConfigRepoAllowlist(cfg *config.Config) executor.RepoAllowlist {
	if cfg == nil {
		return nil
	}
	return &configRepoAllowlist{cfg: cfg}
}

// RepoIsAllowed implements executor.RepoAllowlist.
//
// A repo is allowed if some configured project matches both:
//   - GitHub owner+repo (case-sensitive match against ProjectGitHubConfig)
//   - When projectPath is non-empty, the matched project's filesystem Path
//     equals projectPath. This blocks the "right repo, wrong working tree"
//     misconfiguration class.
func (a *configRepoAllowlist) RepoIsAllowed(owner, repo, projectPath string) bool {
	if a == nil || a.cfg == nil {
		return false
	}
	match := a.cfg.FindProjectByRepo(fmt.Sprintf("%s/%s", owner, repo))
	if match == nil {
		return false
	}
	if projectPath == "" {
		return true
	}
	return match.Path == projectPath
}

// ConfiguredRepos returns all configured "owner/repo" pairs. Used only for
// error and log messages.
func (a *configRepoAllowlist) ConfiguredRepos() []string {
	if a == nil || a.cfg == nil {
		return nil
	}
	out := make([]string, 0, len(a.cfg.Projects))
	for _, p := range a.cfg.Projects {
		if p == nil || p.GitHub == nil {
			continue
		}
		if p.GitHub.Owner == "" || p.GitHub.Repo == "" {
			continue
		}
		out = append(out, fmt.Sprintf("%s/%s", p.GitHub.Owner, p.GitHub.Repo))
	}
	return out
}
