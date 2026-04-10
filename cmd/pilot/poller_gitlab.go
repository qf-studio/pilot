package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/gitlab"
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

			gitlabClient := gitlab.NewClientWithBaseURL(
				deps.Cfg.Adapters.GitLab.Token,
				deps.Cfg.Adapters.GitLab.Project,
				deps.Cfg.Adapters.GitLab.BaseURL,
			)

			label := deps.Cfg.Adapters.GitLab.PilotLabel
			if label == "" {
				label = "pilot"
			}

			gitlabPollerOpts := []gitlab.PollerOption{
				gitlab.WithOnIssueWithResult(func(issueCtx context.Context, issue *gitlab.Issue) (*gitlab.IssueResult, error) {
					result, err := handleGitLabIssueWithResult(issueCtx, deps.Cfg, gitlabClient, issue, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

					// Wire MR to autopilot for CI monitoring + auto-merge
					if result != nil && result.MRNumber > 0 && deps.AutopilotController != nil {
						deps.AutopilotController.OnPRCreated(result.MRNumber, result.MRURL, 0, result.HeadSHA, result.BranchName, "")
					}

					return result, err
				}),
			}

			if deps.AutopilotStateStore != nil {
				gitlabPollerOpts = append(gitlabPollerOpts, gitlab.WithProcessedStore(deps.AutopilotStateStore))
			}

			// Wire OnMRCreated for autopilot controller
			if deps.AutopilotController != nil {
				ctrl := deps.AutopilotController
				gitlabPollerOpts = append(gitlabPollerOpts, gitlab.WithOnMRCreated(func(mrIID int, mrURL string, issueIID int, headSHA string, branchName string) {
					ctrl.OnPRCreated(mrIID, mrURL, issueIID, headSHA, branchName, "")
				}))
			}

			if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
				gitlabPollerOpts = append(gitlabPollerOpts, gitlab.WithMaxConcurrent(deps.Cfg.Orchestrator.MaxConcurrent))
			}

			gitlabPoller := gitlab.NewPoller(gitlabClient, label, interval, gitlabPollerOpts...)

			logging.WithComponent("start").Info("GitLab polling enabled",
				slog.String("project", deps.Cfg.Adapters.GitLab.Project),
				slog.String("label", label),
				slog.Duration("interval", interval),
			)
			go func(p *gitlab.Poller) {
				p.Start(ctx)
			}(gitlabPoller)
		},
	}
}
