package main

import (
	"context"
	"log/slog"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	jiraSDK "github.com/qf-studio/studio-sdk/sdk/integrations/jira"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func jiraPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "jira",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.Jira != nil && cfg.Adapters.Jira.Enabled &&
				cfg.Adapters.Jira.Polling != nil && cfg.Adapters.Jira.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			interval := 30 * time.Second
			if deps.Cfg.Adapters.Jira.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.Jira.Polling.Interval
			}

			// Map internal config → SDK config (field names differ: PilotLabel vs TriggerLabel).
			pilotLabel := deps.Cfg.Adapters.Jira.PilotLabel
			if pilotLabel == "" {
				pilotLabel = "pilot"
			}
			sdkCfg := &jiraSDK.Config{
				Enabled:       deps.Cfg.Adapters.Jira.Enabled,
				Platform:      deps.Cfg.Adapters.Jira.Platform,
				BaseURL:       deps.Cfg.Adapters.Jira.BaseURL,
				Username:      deps.Cfg.Adapters.Jira.Username,
				APIToken:      deps.Cfg.Adapters.Jira.APIToken,
				WebhookSecret: deps.Cfg.Adapters.Jira.WebhookSecret,
				TriggerLabel:  pilotLabel,
				ProjectKey:    deps.Cfg.Adapters.Jira.ProjectKey,
				Polling: &jiraSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}
			sdkCfg.Transitions.InProgress = deps.Cfg.Adapters.Jira.Transitions.InProgress
			sdkCfg.Transitions.Done = deps.Cfg.Adapters.Jira.Transitions.Done

			// Separate client + notifier for the start comment / native
			// workflow transition (GH-4718). sdkCfg.Transitions is plumbed
			// above but never read by the SDK poller package itself — the
			// only consumer is jiraSDK.NewNotifier, so without constructing
			// it here the config field was dead wiring (notify-started audit).
			jiraClient := jiraSDK.NewClient(
				deps.Cfg.Adapters.Jira.BaseURL,
				deps.Cfg.Adapters.Jira.Username,
				deps.Cfg.Adapters.Jira.APIToken,
				deps.Cfg.Adapters.Jira.Platform,
			)
			notifier := jiraSDK.NewNotifier(
				jiraClient,
				deps.Cfg.Adapters.Jira.Transitions.InProgress,
				deps.Cfg.Adapters.Jira.Transitions.Done,
			)

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					// GH-4718: notify Jira the task has started — posts a
					// "Pilot started" comment and, when transitions.in_progress
					// is configured, transitions the issue's native workflow
					// status (the board column, distinct from the SDK-managed
					// pilot-in-progress label). Mirrors GH-4717's Linear
					// wiring. Failure is WARN-logged only — a notify failure
					// must never abort dispatch.
					if err := notifier.NotifyTaskStarted(issueCtx, ev.IssueID, ev.SequenceID); err != nil {
						logging.WithComponent("jira").Warn("Failed to notify task started",
							slog.String("issue_id", ev.IssueID),
							slog.Any("error", err),
						)
					}

					return handleJiraSDKIssueWithResult(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
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

				// GH-4987: wire the same start-leg notifier (constructed above)
				// as the merge-side done leg — completion comment + done
				// transition when a JIRA-* task's PR merges. WARN-only on
				// failure inside Controller.notifyJiraDone; never blocks merge.
				ctrl.SetJiraDoneNotifier(notifier)
			}

			jiraPoller := jiraSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("Jira polling enabled",
				slog.String("base_url", deps.Cfg.Adapters.Jira.BaseURL),
				slog.String("project", deps.Cfg.Adapters.Jira.ProjectKey),
				slog.String("label", pilotLabel),
				slog.Duration("interval", interval),
			)
			deps.SafeAdapterGo(ctx, "jira", func() {
				if err := jiraPoller.Start(ctx); err != nil {
					logging.WithComponent("jira").Error("Jira poller failed",
						slog.Any("error", err),
					)
				}
			})
		},
	}
}
