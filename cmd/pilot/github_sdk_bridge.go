package main

import (
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	github "github.com/qf-studio/pilot/internal/adapters/github"
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
