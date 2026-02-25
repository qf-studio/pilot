package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/plane"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/logging"
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

			planePollerOpts := []plane.PollerOption{
				plane.WithOnIssue(func(issueCtx context.Context, issue *plane.WorkItem) (*plane.IssueResult, error) {
					result, err := handlePlaneIssueWithResult(issueCtx, deps.Cfg, planeClient, issue, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

					// Wire PR to autopilot for CI monitoring + auto-merge
					if result != nil && result.PRNumber > 0 && deps.AutopilotController != nil {
						deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName)
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
