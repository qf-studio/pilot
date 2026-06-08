package main

import (
	"context"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	azuredevopsSDK "github.com/qf-studio/studio-sdk/sdk/integrations/azuredevops"

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
			interval := 30 * time.Second
			if deps.Cfg.Adapters.AzureDevOps.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.AzureDevOps.Polling.Interval
			}

			// Map internal config → SDK config (field names differ: PilotTag vs TriggerLabel).
			pilotTag := deps.Cfg.Adapters.AzureDevOps.PilotTag
			if pilotTag == "" {
				pilotTag = "pilot"
			}
			sdkCfg := &azuredevopsSDK.Config{
				Enabled:       deps.Cfg.Adapters.AzureDevOps.Enabled,
				PAT:           deps.Cfg.Adapters.AzureDevOps.PAT,
				Organization:  deps.Cfg.Adapters.AzureDevOps.Organization,
				Project:       deps.Cfg.Adapters.AzureDevOps.Project,
				Repository:    deps.Cfg.Adapters.AzureDevOps.Repository,
				BaseURL:       deps.Cfg.Adapters.AzureDevOps.BaseURL,
				WebhookSecret: deps.Cfg.Adapters.AzureDevOps.WebhookSecret,
				TriggerLabel:  pilotTag,
				WorkItemTypes: deps.Cfg.Adapters.AzureDevOps.WorkItemTypes,
				Polling: &azuredevopsSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					return handleAzureDevOpsIssueWithResult(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
				}),
			}

			if deps.AutopilotStateStore != nil {
				pollerDeps.ProcessedStore = deps.AutopilotStateStore
			}
			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				pollerDeps.MaxConcurrent = deps.Cfg.Orchestrator.MaxConcurrent
			}
			if deps.AutopilotController != nil {
				ctrl := deps.AutopilotController
				pollerDeps.OnPRCreated = func(prEv sdkcore.PRCreatedEvent) {
					ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, 0, prEv.HeadSHA, prEv.BranchName, "")
				}
			}

			adoPoller := azuredevopsSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("Azure DevOps polling enabled",
				slog.String("organization", deps.Cfg.Adapters.AzureDevOps.Organization),
				slog.String("project", deps.Cfg.Adapters.AzureDevOps.Project),
				slog.Duration("interval", interval),
			)
			go func() {
				if err := adoPoller.Start(ctx); err != nil {
					logging.WithComponent("azuredevops").Error("Azure DevOps poller failed",
						slog.Any("error", err),
					)
				}
			}()
		},
	}
}
