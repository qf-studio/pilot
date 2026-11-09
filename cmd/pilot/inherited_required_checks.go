package main

import (
	"log/slog"

	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/logging"
)

// warnInheritedRequiredChecks logs one Warn line for a project that has no
// ci_checks block while the global autopilot config lists required checks
// (GH-6239). Such a project inherits that list (GH-4478), and when its CI
// posts other check names every PR ends in config_mismatch (GH-4646). Shared
// by both controller-setup sites. Returns whether it warned.
func warnInheritedRequiredChecks(global *autopilot.Config, override *autopilot.ProjectCIChecksOverride, project, repo string) bool {
	inherited := autopilot.InheritedRequiredChecks(global, override)
	if len(inherited) == 0 {
		return false
	}
	logging.WithComponent("autopilot").Warn("project inherits the global required checks",
		slog.String("project", project),
		slog.String("repo", repo),
		slog.Any("required_checks", inherited),
		slog.String("hint", autopilot.CIChecksInheritHint),
	)
	return true
}
