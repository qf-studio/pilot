package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/asana"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/logging"
)

func asanaPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "asana",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled &&
				cfg.Adapters.Asana.Polling != nil && cfg.Adapters.Asana.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			// Determine interval
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Asana.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Asana.Polling.Interval
			}

			asanaClient := asana.NewClient(
				deps.Cfg.Adapters.Asana.AccessToken,
				deps.Cfg.Adapters.Asana.WorkspaceID,
			)
			// GH-1701: Wire processed store for dedup persistence across restarts
			asanaPollerOpts := []asana.PollerOption{
				asana.WithOnAsanaTask(func(taskCtx context.Context, task *asana.Task) (*asana.TaskResult, error) {
					result, err := handleAsanaTaskWithResult(taskCtx, deps.Cfg, asanaClient, task, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

					// GH-1399: Wire PR to autopilot for CI monitoring + auto-merge
					if result != nil && result.PRNumber > 0 && deps.AutopilotController != nil {
						// issueNumber=0 because Asana tasks don't have GitHub issue numbers
						deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName, "")
					}

					return result, err
				}),
			}
			if deps.AutopilotStateStore != nil {
				asanaPollerOpts = append(asanaPollerOpts, asana.WithProcessedStore(deps.AutopilotStateStore))
			}
			asanaPoller := asana.NewPoller(asanaClient, deps.Cfg.Adapters.Asana, interval, asanaPollerOpts...)

			logging.WithComponent("start").Info("Asana polling enabled",
				slog.String("workspace", deps.Cfg.Adapters.Asana.WorkspaceID),
				slog.String("tag", deps.Cfg.Adapters.Asana.PilotTag),
				slog.Duration("interval", interval),
			)
			go func(p *asana.Poller) {
				if err := p.Start(ctx); err != nil {
					logging.WithComponent("asana").Error("Asana poller failed",
						slog.Any("error", err),
					)
				}
			}(asanaPoller)
		},
	}
}
