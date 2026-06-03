package sdkshim

import (
	"errors"

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
// Phase 0: stub. Phase 1 (Plane) populates the plane branch and proves the pattern.
func ResolveRepoForEvent(cfg *config.Config, source string, ev core.IssueEvent) (cloneURL, repoOwner, repoName string, err error) {
	if cfg == nil {
		return "", "", "", errors.New("sdkshim.ResolveRepoForEvent: nil config")
	}
	// TODO(phase-1): implement plane branch; cross-check ev.ProjectID against cfg.Adapters.Plane.
	// TODO(phase-2/3): gitlab, azuredevops.
	// TODO(phase-4): github with per-project routing fallback.
	// TODO(phase-5): linear, jira, asana.
	_ = ev // referenced for the signature; unused until Phase 1.
	_ = source
	return "", "", "", ErrRepoNotResolved
}
