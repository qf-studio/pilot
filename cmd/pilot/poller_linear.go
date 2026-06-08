package main

import (
	"context"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	linearSDK "github.com/qf-studio/studio-sdk/sdk/integrations/linear"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func linearPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "linear",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Linear != nil && cfg.Adapters.Linear.Enabled &&
				cfg.Adapters.Linear.Polling != nil && cfg.Adapters.Linear.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			workspaces := deps.Cfg.Adapters.Linear.GetWorkspaces()
			for _, ws := range workspaces {
				// Determine interval: workspace override > global > default
				interval := 30 * time.Second
				if ws.Polling != nil && ws.Polling.Interval > 0 {
					interval = ws.Polling.Interval
				} else if deps.Cfg.Adapters.Linear.Polling.Interval > 0 {
					interval = deps.Cfg.Adapters.Linear.Polling.Interval
				}

				// Check if workspace polling is explicitly disabled
				if ws.Polling != nil && !ws.Polling.Enabled {
					continue
				}

				// Resolve trigger label: workspace override > default
				triggerLabel := ws.PilotLabel
				if triggerLabel == "" {
					triggerLabel = "pilot"
				}

				// Build SDK config scoped to this workspace
				sdkCfg := &linearSDK.Config{
					Enabled: true,
					Workspaces: []*linearSDK.WorkspaceConfig{{
						Name:         ws.Name,
						APIKey:       ws.APIKey,
						TeamID:       ws.TeamID,
						TriggerLabel: triggerLabel,
						ProjectIDs:   ws.ProjectIDs,
						Projects:     ws.Projects,
						AutoAssign:   ws.AutoAssign,
						Polling: &linearSDK.PollingConfig{
							Enabled:  true,
							Interval: interval,
						},
					}},
					Polling: &linearSDK.PollingConfig{
						Enabled:  true,
						Interval: interval,
					},
				}

				// Capture workspace fields for the handler closure — avoid loop-var aliasing.
				wsAPIKey := ws.APIKey
				wsTeamKey := ws.TeamID // team key (e.g., "APP") used for GetTeamDoneStateID
				wsName := ws.Name

				pollerDeps := sdkcore.PollerDeps{
					Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
						return handleLinearIssueWithResult(issueCtx, deps.Cfg, wsAPIKey, wsTeamKey, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
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

				linearPoller := linearSDK.New(sdkCfg).NewPoller(pollerDeps)

				logging.WithComponent("start").Info("Linear polling enabled",
					slog.String("workspace", wsName),
					slog.String("team", wsTeamKey),
					slog.Duration("interval", interval),
				)
				go func() {
					if err := linearPoller.Start(ctx); err != nil {
						logging.WithComponent("linear").Error("Linear poller failed",
							slog.String("workspace", wsName),
							slog.Any("error", err),
						)
					}
				}()
			}
		},
	}
}
