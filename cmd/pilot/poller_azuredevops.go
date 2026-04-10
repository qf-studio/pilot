package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/azuredevops"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
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

			// GH-2132: Create notifier for task lifecycle notifications
			pilotTag := deps.Cfg.Adapters.AzureDevOps.PilotTag
			if pilotTag == "" {
				pilotTag = "pilot"
			}
			adoNotifier := azuredevops.NewNotifier(adoClient, pilotTag)

			adoPollerOpts := []azuredevops.PollerOption{
				azuredevops.WithOnWorkItemWithResult(func(wiCtx context.Context, wi *azuredevops.WorkItem) (*azuredevops.WorkItemResult, error) {
					result, err := handleAzureDevOpsWorkItemWithResult(wiCtx, deps.Cfg, adoClient, adoNotifier, wi, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

					// GH-2132: Wire PR to autopilot for CI monitoring + auto-merge
					if result != nil && result.PRNumber > 0 && deps.AutopilotController != nil {
						deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName, "")
					}

					return result, err
				}),
			}

			// Wire autopilot OnPRCreated callback
			if deps.AutopilotController != nil {
				adoPollerOpts = append(adoPollerOpts, azuredevops.WithOnPRCreated(func(prID int, prURL string, workItemID int, headSHA string, branchName string) {
					deps.AutopilotController.OnPRCreated(prID, prURL, 0, headSHA, branchName, "")
				}))
			}

			// Wire processed store for persistence
			if deps.AutopilotStateStore != nil {
				adoPollerOpts = append(adoPollerOpts, azuredevops.WithProcessedStore(deps.AutopilotStateStore))
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
