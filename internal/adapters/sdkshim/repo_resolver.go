package sdkshim

import (
	"errors"
	"fmt"
	"strings"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

// ErrRepoNotResolved is returned when ResolveRepoForEvent cannot map a
// (source, projectID) pair to a clone URL using the current config.
// Callers should treat this as a fatal dispatch error — without a clone URL
// the executor's git layer cannot operate.
var ErrRepoNotResolved = errors.New("sdkshim: repo not resolved for event")

// ResolveRepoForEvent maps (SourceAdapter, ev.ProjectID) → CloneURL/RepoOwner/RepoName
// using cfg.Adapters and cfg.Projects.
//
// core.IssueEvent intentionally does NOT carry git-host metadata (see SDK
// sdk/core/registry.go IssueEvent). The daemon, which has cfg in hand,
// resolves it at Task{} construction time before dispatch.
//
// Per-source resolution (Phase 1+ fills these in):
//   - "github":      cfg.Projects[i].GitHub.{Owner,Repo} (per-project routing) or cfg.Adapters.GitHub
//   - "gitlab":      cfg.Adapters.GitLab.Project ("namespace/project") + base URL
//   - "azuredevops": cfg.Adapters.AzureDevOps organization/project/repo
//   - "plane":       cfg.Adapters.Plane.WorkspaceSlug + repo lookup by project UUID
//   - "linear":      cfg.Adapters.Linear maps team → repo
//   - "jira":        cfg.Adapters.Jira project key → repo
//   - "asana":       cfg.Adapters.Asana project ID → repo
//
// Phase 0: stub. Phase 4 (GitHub) populates the github branch and proves the pattern.
func ResolveRepoForEvent(cfg *config.Config, source string, ev core.IssueEvent) (cloneURL, repoOwner, repoName string, err error) {
	if cfg == nil {
		return "", "", "", errors.New("sdkshim.ResolveRepoForEvent: nil config")
	}

	// github (M7 Phase 4): the SDK github adapter sets ev.ProjectID to the repo NAME
	// (the part after "owner/" in "owner/repo" — see studio-sdk github adapter.go toIssueEvent).
	// Route per-project by matching that repo name against cfg.Projects[].GitHub.Repo,
	// falling back to the default cfg.Adapters.GitHub repo ("owner/repo").
	if source == "github" {
		if owner, repo, ok := resolveGithubRepo(cfg, ev.ProjectID); ok {
			return githubCloneURL(owner, repo), owner, repo, nil
		}
		return "", "", "", ErrRepoNotResolved
	}

	// TODO(phase-1): implement plane branch; cross-check ev.ProjectID against cfg.Adapters.Plane.
	// TODO(phase-2/3): gitlab, azuredevops.
	// TODO(phase-5): linear, jira, asana.
	_ = ev // referenced for the signature; unused for not-yet-implemented sources.
	_ = source
	return "", "", "", ErrRepoNotResolved
}

// resolveGithubRepo resolves owner/repo for a github event. It prefers a per-project match
// on the repo name (ev.ProjectID matched against cfg.Projects[].GitHub.Repo), then falls back
// to the default cfg.Adapters.GitHub.Repo ("owner/repo"). Returns ok=false when neither yields
// a non-empty owner and repo.
//
// LIMITATION: the SDK IssueEvent carries only the repo NAME (not "owner/repo"), so matching is
// by name alone. If two configured projects share a repo name under different owners, the first
// configured match wins. This is harmless in Phase 4a (every caller discards the resolved
// owner/repo), but Phase 4b — which wires the result into clone/dispatch — must disambiguate
// (e.g. grow IssueEvent to carry owner, or constrain config to unique repo names).
func resolveGithubRepo(cfg *config.Config, repoName string) (owner, repo string, ok bool) {
	if repoName != "" {
		for _, p := range cfg.Projects {
			if p.GitHub != nil && p.GitHub.Repo == repoName && p.GitHub.Owner != "" {
				return p.GitHub.Owner, p.GitHub.Repo, true
			}
		}
	}
	if cfg.Adapters != nil && cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Repo != "" {
		parts := strings.SplitN(cfg.Adapters.GitHub.Repo, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

// githubCloneURL builds the HTTPS clone URL for an owner/repo pair.
func githubCloneURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}
