package main

import (
	"context"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	planeSDK "github.com/qf-studio/studio-sdk/sdk/integrations/plane"

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

			// Map internal config → SDK config (field names differ: PilotLabel vs TriggerLabel).
			pilotLabel := deps.Cfg.Adapters.Plane.PilotLabel
			if pilotLabel == "" {
				pilotLabel = "pilot"
			}
			sdkCfg := &planeSDK.Config{
				Enabled:       deps.Cfg.Adapters.Plane.Enabled,
				BaseURL:       deps.Cfg.Adapters.Plane.BaseURL,
				APIKey:        deps.Cfg.Adapters.Plane.APIKey,
				WebhookSecret: deps.Cfg.Adapters.Plane.WebhookSecret,
				WorkspaceSlug: deps.Cfg.Adapters.Plane.WorkspaceSlug,
				ProjectIDs:    deps.Cfg.Adapters.Plane.ProjectIDs,
				TriggerLabel:  pilotLabel,
				Polling: &planeSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			// Separate client for notifier calls (adapter creates its own internally).
			planeClient := planeSDK.NewClient(
				deps.Cfg.Adapters.Plane.BaseURL,
				deps.Cfg.Adapters.Plane.APIKey,
			)
			planeNotifier := planeSDK.NewNotifier(planeClient, deps.Cfg.Adapters.Plane.WorkspaceSlug)

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					// GH-2132: Notify task started
					if err := planeNotifier.NotifyTaskStarted(issueCtx, ev.ProjectID, ev.IssueID, ev.SequenceID); err != nil {
						logging.WithComponent("plane").Warn("Failed to notify task started",
							slog.String("work_item_id", ev.IssueID),
							slog.Any("error", err),
						)
					}

					result, err := handlePlaneIssueWithResult(issueCtx, deps.Cfg, planeClient, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

					// GH-2132: Link PR via notifier
					if result != nil && result.PRNumber > 0 {
						if linkErr := planeNotifier.LinkPR(issueCtx, ev.ProjectID, ev.IssueID, result.PRNumber, result.PRURL); linkErr != nil {
							logging.WithComponent("plane").Warn("Failed to link PR",
								slog.String("work_item_id", ev.IssueID),
								slog.Any("error", linkErr),
							)
						}
					}

					return result, err
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

			planePoller := planeSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("Plane.so polling enabled",
				slog.String("workspace", deps.Cfg.Adapters.Plane.WorkspaceSlug),
				slog.Int("projects", len(deps.Cfg.Adapters.Plane.ProjectIDs)),
				slog.Duration("interval", interval),
			)
			go func() {
				if err := planePoller.Start(ctx); err != nil {
					logging.WithComponent("plane").Error("Plane poller failed",
						slog.Any("error", err),
					)
				}
			}()
		},
	}
}
