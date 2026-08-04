package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	gitlabSDK "github.com/qf-studio/studio-sdk/sdk/integrations/gitlab"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

func gitlabPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "gitlab",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.GitLab != nil && cfg.Adapters.GitLab.Enabled &&
				cfg.Adapters.GitLab.Polling != nil && cfg.Adapters.GitLab.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			// Determine interval
			interval := 30 * time.Second
			if deps.Cfg.Adapters.GitLab.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.GitLab.Polling.Interval
			}

			// Map internal config → SDK config (field names differ: PilotLabel vs TriggerLabel).
			pilotLabel := deps.Cfg.Adapters.GitLab.PilotLabel
			if pilotLabel == "" {
				pilotLabel = "pilot"
			}
			sdkCfg := &gitlabSDK.Config{
				Enabled:       deps.Cfg.Adapters.GitLab.Enabled,
				Token:         deps.Cfg.Adapters.GitLab.Token,
				BaseURL:       deps.Cfg.Adapters.GitLab.BaseURL,
				WebhookSecret: deps.Cfg.Adapters.GitLab.WebhookSecret,
				Project:       deps.Cfg.Adapters.GitLab.Project,
				TriggerLabel:  pilotLabel,
				Polling: &gitlabSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			// Separate client for handler calls (AddIssueNote, SetPRCreator).
			var gitlabClient *gitlabSDK.Client
			if deps.Cfg.Adapters.GitLab.BaseURL != "" {
				gitlabClient = gitlabSDK.NewClientWithBaseURL(
					deps.Cfg.Adapters.GitLab.Token,
					deps.Cfg.Adapters.GitLab.Project,
					deps.Cfg.Adapters.GitLab.BaseURL,
				)
			} else {
				gitlabClient = gitlabSDK.NewClient(
					deps.Cfg.Adapters.GitLab.Token,
					deps.Cfg.Adapters.GitLab.Project,
				)
			}

			// GH-4720: SDK-native notifier for the "Pilot started" note.
			// Additive only — the poller already guarantees LabelInProgress
			// internally (poller.go:520); NotifyTaskStarted's own label add
			// is idempotent (AddIssueLabels merges), so this does not
			// duplicate or replace the SDK-managed label mechanism.
			gitlabNotifier := gitlabSDK.NewNotifier(gitlabClient, pilotLabel)

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					// GH-4720: Notify task started (start note on the issue).
					if iid, err := strconv.Atoi(ev.IssueID); err == nil {
						if err := gitlabNotifier.NotifyTaskStarted(issueCtx, iid, ev.SequenceID); err != nil {
							logging.WithComponent("gitlab").Warn("Failed to notify task started",
								slog.String("issue_id", ev.IssueID),
								slog.Any("error", err),
							)
						}
					} else {
						logging.WithComponent("gitlab").Warn("Failed to notify task started",
							slog.String("issue_id", ev.IssueID),
							slog.Any("error", err),
						)
					}

					return handleGitlabIssueWithResult(issueCtx, deps.Cfg, gitlabClient, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
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

			gitlabPoller := gitlabSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("GitLab polling enabled",
				slog.String("project", deps.Cfg.Adapters.GitLab.Project),
				slog.String("label", pilotLabel),
				slog.Duration("interval", interval),
			)
			deps.SafeAdapterGo(ctx, "gitlab", func() {
				if err := gitlabPoller.Start(ctx); err != nil {
					logging.WithComponent("gitlab").Error("GitLab poller failed",
						slog.Any("error", err),
					)
				}
			})
		},
	}
}
