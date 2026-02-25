package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/azuredevops"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/logging"
)

func azuredevopsPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "azuredevops",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.AzureDevOps != nil && cfg.Adapters.AzureDevOps.Enabled &&
				cfg.Adapters.AzureDevOps.Polling != nil && cfg.Adapters.AzureDevOps.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			// Determine interval
			interval := 30 * time.Second
			if deps.Cfg.Adapters.AzureDevOps.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.AzureDevOps.Polling.Interval
			}

			adoClient := azuredevops.NewClientWithConfig(deps.Cfg.Adapters.AzureDevOps)

			adoPollerOpts := []azuredevops.PollerOption{
				azuredevops.WithOnWorkItem(func(wiCtx context.Context, wi *azuredevops.WorkItem) error {
					logging.WithComponent("azuredevops").Info("Work item picked up",
						slog.Int("id", wi.ID),
						slog.String("title", wi.GetTitle()),
					)
					return nil
				}),
			}

			// Wire autopilot OnPRCreated callback
			if deps.AutopilotController != nil {
				adoPollerOpts = append(adoPollerOpts, azuredevops.WithOnPRCreated(func(prID int, prURL string, workItemID int, headSHA string, branchName string) {
					deps.AutopilotController.OnPRCreated(prID, prURL, 0, headSHA, branchName)
				}))
			}

			// Wire processed store for persistence
			if deps.AutopilotStateStore != nil {
				adoPollerOpts = append(adoPollerOpts, azuredevops.WithProcessedStore(deps.AutopilotStateStore))
			}

			pilotTag := deps.Cfg.Adapters.AzureDevOps.PilotTag
			if pilotTag == "" {
				pilotTag = "pilot"
			}

			adoPoller := azuredevops.NewPoller(adoClient, pilotTag, interval, adoPollerOpts...)

			logging.WithComponent("start").Info("Azure DevOps polling enabled",
				slog.String("organization", deps.Cfg.Adapters.AzureDevOps.Organization),
				slog.String("project", deps.Cfg.Adapters.AzureDevOps.Project),
				slog.Duration("interval", interval),
			)
			go adoPoller.Start(ctx)
		},
	}
}
