package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/logging"
)

// githubPollerRegistration returns the studio-sdk GitHub issue-poller registration
// (M7 Phase 4a). It is DORMANT by design:
//
//   - Enabled requires the experimental adapters.github.use_sdk_poller flag (default false).
//   - It is NOT included in adapterPollerRegistrations() (see poller_registry.go), so it is
//     never started by the live daemon this phase.
//
// The SDK discovery poller deliberately does NOT carry the in-tree GitHub poller's board
// sync, rate-limit scheduler, pre-flight intent judge, execution checkers, or issue metrics —
// core.PollerDeps cannot express them at studio-sdk v0.24.0. Wiring those (and flipping the
// flag on for single-repo configs) is deferred to Phase 4b, blocked on studio-sdk v0.25.0+.
// Until then this registration exists to mirror the GitLab template (poller_gitlab.go) and to
// exercise SDK adapter/poller construction without changing any runtime behavior.
func githubPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "github",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.UseSDKPoller &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			// Determine interval.
			interval := 30 * time.Second
			if deps.Cfg.Adapters.GitHub.Polling.Interval > 0 {
				interval = deps.Cfg.Adapters.GitHub.Polling.Interval
			}

			// Map internal config → SDK config (field names differ: PilotLabel vs TriggerLabel).
			pilotLabel := deps.Cfg.Adapters.GitHub.PilotLabel
			if pilotLabel == "" {
				pilotLabel = "pilot"
			}
			sdkCfg := &githubSDK.Config{
				Enabled:       deps.Cfg.Adapters.GitHub.Enabled,
				Token:         deps.Cfg.Adapters.GitHub.Token,
				WebhookSecret: deps.Cfg.Adapters.GitHub.WebhookSecret,
				Repo:          deps.Cfg.Adapters.GitHub.Repo,
				TriggerLabel:  pilotLabel,
				Polling: &githubSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					return handleGithubIssueEventSDK(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
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
					// GitHub's IssueID is the numeric issue number; forward it (and the node ID)
					// so the autopilot controller's post-merge gates (controller.go:850/920/1063/
					// 1442/1466) and board-sync-to-Review (controller.go:506) work when the SDK
					// poll path goes live in Phase 4b. Unlike the GitLab template (which passes
					// 0/"" because its IssueID is non-numeric), GitHub populates both.
					issueNumber, _ := strconv.Atoi(prEv.IssueID)
					ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, issueNumber, prEv.HeadSHA, prEv.BranchName, prEv.IssueNodeID)
				}
			}

			githubPoller := githubSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("GitHub SDK polling enabled (experimental)",
				slog.String("repo", deps.Cfg.Adapters.GitHub.Repo),
				slog.String("label", pilotLabel),
				slog.Duration("interval", interval),
			)
			go func() {
				if err := githubPoller.Start(ctx); err != nil {
					logging.WithComponent("github").Error("GitHub SDK poller failed",
						slog.Any("error", err),
					)
				}
			}()
		},
	}
}
