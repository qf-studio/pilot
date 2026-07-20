package main

import (
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	github "github.com/qf-studio/pilot/internal/adapters/github"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
)

// toSDKProjectBoardConfig maps the internal GitHub ProjectBoard config to the
// studio-sdk shape. Field-by-field copy — the two structs are intentionally
// identical, but the types live in different packages until the in-tree
// adapter is fully retired (M7 4d).
func toSDKProjectBoardConfig(pb *github.ProjectBoardConfig) *githubSDK.ProjectBoardConfig {
	if pb == nil {
		return nil
	}
	return &githubSDK.ProjectBoardConfig{
		Enabled:       pb.Enabled,
		ProjectNumber: pb.ProjectNumber,
		StatusField:   pb.StatusField,
		Statuses: githubSDK.ProjectStatuses{
			InProgress: pb.Statuses.InProgress,
			Review:     pb.Statuses.Review,
			Done:       pb.Statuses.Done,
			Failed:     pb.Statuses.Failed,
		},
		SourceEnabled: pb.SourceEnabled,
		SourceStatus:  pb.SourceStatus,
	}
}

// projectBoardControllerOpts resolves the effective board config for repoFullName
// (GH-4472: Config.ResolveProjectBoard's project-override → default-repo-fallback
// precedence) and, if enabled, returns the autopilot ControllerOption that wires
// ProjectBoardSync for it. Returns nil when the repo has no board config or board
// sync is disabled — callers append the (possibly empty) result to their option
// slice unconditionally.
func projectBoardControllerOpts(apGHClient *githubSDK.Client, cfg *config.Config, repoFullName, owner string, isDefaultRepo bool) []autopilot.ControllerOption {
	pb := cfg.ResolveProjectBoard(repoFullName, isDefaultRepo)
	if pb == nil || !pb.Enabled {
		return nil
	}
	bs := githubSDK.NewProjectBoardSync(apGHClient, toSDKProjectBoardConfig(pb), owner)
	statuses := pb.GetStatuses()
	return []autopilot.ControllerOption{autopilot.WithProjectBoardSync(bs, statuses.Done, statuses.Failed, statuses.Review, statuses.InProgress)}
}
