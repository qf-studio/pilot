package main

import (
	"strings"

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
// precedence) and, if enabled, returns the autopilot ControllerOption(s) that wire
// it into the controller. Returns nil when the repo has no board config or board
// sync is disabled — callers append the (possibly empty) result to their option
// slice unconditionally.
//
// When pb.SourceEnabled is also set, a second option wires a ProjectBoardSource
// so the controller's reconcileUnsourcedBoardIssues sweep (GH-4488) can audit the
// same board the studio-sdk poller is already sourcing dispatch candidates from
// — that switch (board replaces label discovery entirely) happens inside the
// vendored studio-sdk poller and isn't observable from here, which is exactly
// why an open pilot-labeled issue the board doesn't cover was previously
// invisible: nothing outside the poller re-derived the comparison. A malformed
// repoFullName (no "/") skips the source wiring rather than panicking; sync still
// applies since it doesn't need a repo.
func projectBoardControllerOpts(apGHClient *githubSDK.Client, cfg *config.Config, repoFullName, owner string, isDefaultRepo bool) []autopilot.ControllerOption {
	pb := cfg.ResolveProjectBoard(repoFullName, isDefaultRepo)
	if pb == nil || !pb.Enabled {
		return nil
	}
	bs := githubSDK.NewProjectBoardSync(apGHClient, toSDKProjectBoardConfig(pb), owner)
	statuses := pb.GetStatuses()
	opts := []autopilot.ControllerOption{autopilot.WithProjectBoardSync(bs, statuses.Done, statuses.Failed, statuses.Review, statuses.InProgress)}

	if pb.SourceEnabled {
		repoParts := strings.SplitN(repoFullName, "/", 2)
		if len(repoParts) == 2 && repoParts[0] != "" && repoParts[1] != "" {
			sourceStatus := pb.SourceStatus
			if sourceStatus == "" {
				sourceStatus = "Todo"
			}
			src := githubSDK.NewProjectBoardSource(apGHClient, toSDKProjectBoardConfig(pb), repoParts[0], repoParts[1])
			opts = append(opts, autopilot.WithProjectBoardSource(src, sourceStatus))
		}
	}

	return opts
}
