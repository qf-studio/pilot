package main

import (
	"context"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	asanaSDK "github.com/qf-studio/studio-sdk/sdk/integrations/asana"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func asanaPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "asana",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Asana != nil && cfg.Adapters.Asana.Enabled &&
				cfg.Adapters.Asana.Polling != nil && cfg.Adapters.Asana.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Asana.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Asana.Polling.Interval
			}

			// Map internal config → SDK config (field names differ: PilotTag vs TriggerLabel).
			pilotTag := deps.Cfg.Adapters.Asana.PilotTag
			if pilotTag == "" {
				pilotTag = "pilot"
			}
			sdkCfg := &asanaSDK.Config{
				Enabled:       deps.Cfg.Adapters.Asana.Enabled,
				AccessToken:   deps.Cfg.Adapters.Asana.AccessToken,
				WorkspaceID:   deps.Cfg.Adapters.Asana.WorkspaceID,
				WebhookSecret: deps.Cfg.Adapters.Asana.WebhookSecret,
				TriggerLabel:  pilotTag,
				Polling: &asanaSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			// GH-4719: SDK-native client + notifier for the "Pilot started"
			// comment (mirrors poller_jira.go's single-purpose client
			// construction — handleAsanaIssueWithResult takes no client
			// param, so this client exists solely to back the notifier).
			// Additive only — the poller already guarantees the pilotTag
			// tag internally (poller.go:297-298); this does not touch that
			// mechanism.
			asanaClient := asanaSDK.NewClient(
				deps.Cfg.Adapters.Asana.AccessToken,
				deps.Cfg.Adapters.Asana.WorkspaceID,
			)
			notifier := asanaSDK.NewNotifier(asanaClient, pilotTag)

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					// GH-4719: notify Asana the task has started — posts a
					// "Pilot started" comment on the task. Mirrors GH-4718's
					// Jira wiring / GH-2132's Plane wiring. Failure is
					// WARN-logged only — a notify failure must never abort
					// dispatch.
					if err := notifier.NotifyTaskStarted(issueCtx, ev.IssueID, ev.SequenceID); err != nil {
						logging.WithComponent("asana").Warn("Failed to notify task started",
							slog.String("issue_id", ev.IssueID),
							slog.Any("error", err),
						)
					}

					return handleAsanaIssueWithResult(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
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

			asanaPoller := asanaSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("Asana polling enabled",
				slog.String("workspace", deps.Cfg.Adapters.Asana.WorkspaceID),
				slog.String("tag", pilotTag),
				slog.Duration("interval", interval),
			)
			deps.SafeAdapterGo(ctx, "asana", func() {
				if err := asanaPoller.Start(ctx); err != nil {
					logging.WithComponent("asana").Error("Asana poller failed",
						slog.Any("error", err),
					)
				}
			})
		},
	}
}
