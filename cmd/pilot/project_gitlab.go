package main

import (
	"log/slog"

	gitlabSDK "github.com/qf-studio/studio-sdk/sdk/integrations/gitlab"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// registerProjectGitLabPRCreators registers a project-keyed MR creator on the
// runner for every projects[] entry with a `gitlab:` block (GH-5583), so a task
// from any source adapter (e.g. Linear) on a GitLab-hosted project opens a
// GitLab MR instead of falling through to `gh pr create`.
//
// Registration is independent of adapters.gitlab.enabled / polling.enabled —
// those govern issue intake, not MR creation. The token is the global
// adapters.gitlab.token (Config.Validate already rejects a gitlab: project
// without one; a missing token here is skipped with an ERROR as defence in
// depth). The client is built with the same constructor poller_gitlab.go uses.
// The key is executor.ProjectPRCreatorKey(project.path) — the EvalSymlinks'd
// configured path, matched against task.ProjectPath (never the worktree path,
// GH-5582) by Runner.resolvePRCreator.
//
// Returns the registered keys.
func registerProjectGitLabPRCreators(cfg *config.Config, runner *executor.Runner) []string {
	if cfg == nil || runner == nil {
		return nil
	}
	var keys []string
	for _, proj := range cfg.Projects {
		if proj == nil || proj.GitLab == nil {
			continue
		}
		key := executor.ProjectPRCreatorKey(proj.Path)
		if key == "" {
			logging.WithComponent("start").Error("Project gitlab: block has no path; MR creator not registered",
				slog.String("project", proj.Name),
				slog.String("gitlab_project", proj.GitLab.Project),
			)
			continue
		}
		token := ""
		if cfg.Adapters != nil && cfg.Adapters.GitLab != nil {
			token = cfg.Adapters.GitLab.Token
		}
		if token == "" {
			logging.WithComponent("start").Error("Project gitlab: block requires adapters.gitlab.token; MR creator not registered",
				slog.String("project", proj.Name),
				slog.String("gitlab_project", proj.GitLab.Project),
			)
			continue
		}
		baseURL := cfg.ResolveGitLabBaseURL(proj.GitLab)
		runner.RegisterPRCreator(key, gitlabSDK.NewClientWithBaseURL(token, proj.GitLab.Project, baseURL))
		keys = append(keys, key)
		logging.WithComponent("start").Info("Registered project MR creator",
			slog.String("project", proj.Name),
			slog.String("gitlab_project", proj.GitLab.Project),
			slog.String("base_url", baseURL),
			slog.String("key", key),
		)
	}
	return keys
}
