package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/plane"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func planePollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "plane",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Plane != nil && cfg.Adapters.Plane.Enabled &&
				cfg.Adapters.Plane.Polling != nil && cfg.Adapters.Plane.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			// Determine interval
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Plane.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Plane.Polling.Interval
			}

			planeClient := plane.NewClient(
				deps.Cfg.Adapters.Plane.BaseURL,
				deps.Cfg.Adapters.Plane.APIKey,
			)

			// GH-2132: Create notifier for task lifecycle notifications
			planeNotifier := plane.NewNotifier(planeClient, deps.Cfg.Adapters.Plane.WorkspaceSlug)

			planePollerOpts := []plane.PollerOption{
				plane.WithOnIssue(func(issueCtx context.Context, issue *plane.WorkItem) (*plane.IssueResult, error) {
					taskID := "PLANE-" + issue.ID[:8]

					// GH-2132: Notify task started
					if err := planeNotifier.NotifyTaskStarted(issueCtx, issue.ProjectID, issue.ID, taskID); err != nil {
						logging.WithComponent("plane").Warn("Failed to notify task started",
							slog.String("work_item_id", issue.ID),
							slog.Any("error", err),
						)
					}

					result, err := handlePlaneIssueWithResult(issueCtx, deps.Cfg, planeClient, issue, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

					// GH-2132: Link PR via notifier
					if result != nil && result.PRNumber > 0 {
						if linkErr := planeNotifier.LinkPR(issueCtx, issue.ProjectID, issue.ID, result.PRNumber, result.PRURL); linkErr != nil {
							logging.WithComponent("plane").Warn("Failed to link PR",
								slog.String("work_item_id", issue.ID),
								slog.Any("error", linkErr),
							)
						}
					}

					// Wire PR to autopilot for CI monitoring + auto-merge
					if result != nil && result.PRNumber > 0 && deps.AutopilotController != nil {
						deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName, "")
					}

					return result, err
				}),
			}
			if deps.AutopilotStateStore != nil {
				planePollerOpts = append(planePollerOpts, plane.WithProcessedStore(deps.AutopilotStateStore))
			}
			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				planePollerOpts = append(planePollerOpts, plane.WithMaxConcurrent(deps.Cfg.Orchestrator.MaxConcurrent))
			}
			planePoller := plane.NewPoller(planeClient, deps.Cfg.Adapters.Plane, interval, planePollerOpts...)

			logging.WithComponent("start").Info("Plane.so polling enabled",
				slog.String("workspace", deps.Cfg.Adapters.Plane.WorkspaceSlug),
				slog.Int("projects", len(deps.Cfg.Adapters.Plane.ProjectIDs)),
				slog.Duration("interval", interval),
			)
			go func(p *plane.Poller) {
				if err := p.Start(ctx); err != nil {
					logging.WithComponent("plane").Error("Plane poller failed",
						slog.Any("error", err),
					)
				}
			}(planePoller)
		},
	}
}
