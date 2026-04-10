package main

import (
	"context"
	"log/slog"

	"github.com/qf-studio/pilot/internal/adapters"
	"github.com/qf-studio/pilot/internal/adapters/jira"
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
			// GH-1838: Jira adapter via common adapter interface + registry
			jiraAdapter := jira.NewAdapter(deps.Cfg.Adapters.Jira)
			adapters.Register(jiraAdapter)

			// GH-2132: Create notifier for task lifecycle notifications
			jiraNotifier := jira.NewNotifier(
				jiraAdapter.Client(),
				deps.Cfg.Adapters.Jira.Transitions.InProgress,
				deps.Cfg.Adapters.Jira.Transitions.Done,
			)

			pollerDeps := adapters.PollerDeps{
				MaxConcurrent: deps.Cfg.Orchestrator.MaxConcurrent,
			}
			if deps.AutopilotStateStore != nil {
				pollerDeps.ProcessedStore = deps.AutopilotStateStore
			}

			jiraPoller := jiraAdapter.CreatePoller(pollerDeps, func(issueCtx context.Context, issue *jira.Issue) (*jira.IssueResult, error) {
				// GH-2132: Notify task started (transitions to In Progress + posts comment)
				if err := jiraNotifier.NotifyTaskStarted(issueCtx, issue.Key, issue.Key); err != nil {
					logging.WithComponent("jira").Warn("Failed to notify task started",
						slog.String("issue", issue.Key),
						slog.Any("error", err),
					)
				}

				result, err := handleJiraIssueWithResult(issueCtx, deps.Cfg, jiraAdapter.Client(), issue, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

				// GH-2132: Link PR via notifier
				if result != nil && result.PRNumber > 0 {
					if linkErr := jiraNotifier.LinkPR(issueCtx, issue.Key, result.PRNumber, result.PRURL); linkErr != nil {
						logging.WithComponent("jira").Warn("Failed to link PR",
							slog.String("issue", issue.Key),
							slog.Any("error", linkErr),
						)
					}
				}

				// GH-1399: Wire PR to autopilot for CI monitoring + auto-merge
				if result != nil && result.PRNumber > 0 && deps.AutopilotController != nil {
					deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName, "")
				}

				return result, err
			})

			logging.WithComponent("start").Info("Jira polling enabled",
				slog.String("base_url", deps.Cfg.Adapters.Jira.BaseURL),
				slog.String("project", deps.Cfg.Adapters.Jira.ProjectKey),
				slog.String("adapter", jiraAdapter.Name()),
			)
			go func(p *jira.Poller) {
				if err := p.Start(ctx); err != nil {
					logging.WithComponent("jira").Error("Jira poller failed",
						slog.Any("error", err),
					)
				}
			}(jiraPoller)
		},
	}
}
