package main

import (
	"log/slog"

	forgejoSDK "github.com/qf-studio/studio-sdk/sdk/integrations/forgejo"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// registerProjectForgejoPRCreators registers a project-keyed PR creator on the
// runner for every projects[] entry with a `forgejo:` block, mirroring GH-5583
// (registerProjectGitLabPRCreators), so tasks from any source adapter (e.g.
// Plane) on a Forgejo-hosted project open a Forgejo PR instead of falling
// through to `gh pr create`.
func registerProjectForgejoPRCreators(cfg *config.Config, runner *executor.Runner) []string {
	if cfg == nil || runner == nil {
		return nil
	}
	var keys []string
	for _, proj := range cfg.Projects {
		if proj == nil || proj.Forgejo == nil {
			continue
		}
		key := executor.ProjectPRCreatorKey(proj.Path)
		if key == "" {
			logging.WithComponent("start").Error("Project forgejo: block has no path; PR creator not registered",
				slog.String("project", proj.Name),
				slog.String("forgejo_repo", proj.Forgejo.Repo),
			)
			continue
		}
		token := ""
		if cfg.Adapters != nil && cfg.Adapters.Forgejo != nil {
			token = cfg.Adapters.Forgejo.Token
		}
		if token == "" {
			logging.WithComponent("start").Error("Project forgejo: block requires adapters.forgejo.token; PR creator not registered",
				slog.String("project", proj.Name),
				slog.String("forgejo_repo", proj.Forgejo.Repo),
			)
			continue
		}
		baseURL := cfg.ResolveForgejoBaseURL(proj.Forgejo)
		client, err := forgejoSDK.NewClient(token, proj.Forgejo.Repo, baseURL)
		if err != nil {
			logging.WithComponent("start").Error("Project forgejo: invalid config; PR creator not registered",
				slog.String("project", proj.Name),
				slog.String("forgejo_repo", proj.Forgejo.Repo),
				slog.String("base_url", baseURL),
				slog.Any("error", err),
			)
			continue
		}
		runner.RegisterPRCreator(key, client)
		keys = append(keys, key)
		logging.WithComponent("start").Info("Registered project Forgejo PR creator",
			slog.String("project", proj.Name),
			slog.String("forgejo_repo", proj.Forgejo.Repo),
			slog.String("base_url", baseURL),
			slog.String("key", key),
		)
	}
	return keys
}
