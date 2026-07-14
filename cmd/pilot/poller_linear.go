package main

import (
	"context"
	"fmt"
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
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Linear.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Linear.Polling.Interval
			}

			// Map internal workspace configs → SDK workspace configs.
			internalWss := deps.Cfg.Adapters.Linear.GetWorkspaces()
			sdkWorkspaces := make([]*linearSDK.WorkspaceConfig, 0, len(internalWss))
			for _, ws := range internalWss {
				triggerLabel := ws.PilotLabel
				if triggerLabel == "" {
					triggerLabel = "pilot"
				}
				wsInterval := interval
				if ws.Polling != nil && ws.Polling.Interval > 0 {
					wsInterval = ws.Polling.Interval
				}
				sdkWorkspaces = append(sdkWorkspaces, &linearSDK.WorkspaceConfig{
					Name:         ws.Name,
					APIKey:       ws.APIKey,
					TeamID:       ws.TeamID,
					TriggerLabel: triggerLabel,
					Polling: &linearSDK.PollingConfig{
						Enabled:  true,
						Interval: wsInterval,
					},
				})
			}

			sdkCfg := &linearSDK.Config{
				Enabled:    deps.Cfg.Adapters.Linear.Enabled,
				Workspaces: sdkWorkspaces,
				Polling: &linearSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					return handleLinearIssueWithResult(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
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
				slog.String("workspaces", fmt.Sprintf("%d workspace(s)", len(internalWss))),
				slog.Duration("interval", interval),
			)
			deps.SafeAdapterGo(ctx, "linear", func() {
				if err := linearPoller.Start(ctx); err != nil {
					logging.WithComponent("linear").Error("Linear poller failed",
						slog.Any("error", err),
					)
				}
			})
		},
	}
}
