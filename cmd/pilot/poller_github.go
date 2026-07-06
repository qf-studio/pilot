package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// sdkPreFlightJudge adapts *executor.IntentJudge to sdkcore.PreFlightJudger.
// Sibling of preFlightJudgeShim (main.go), which returns the in-tree
// github.Verdict; this one returns the SDK's core.Verdict.
type sdkPreFlightJudge struct {
	judge *executor.IntentJudge
}

func (s sdkPreFlightJudge) JudgeIssue(ctx context.Context, title, body, repoContext string) (sdkcore.Verdict, error) {
	v, err := s.judge.JudgeIssue(ctx, title, body, repoContext)
	if err != nil {
		return sdkcore.Verdict{}, err
	}
	return sdkcore.Verdict{
		Accepted:   !v.IsRejection(),
		Decision:   string(v.Decision),
		Reason:     v.Reason,
		Confidence: v.Confidence,
	}, nil
}

// sdkRateLimitScheduler adapts *executor.Scheduler to sdkcore.RateLimitScheduler.
// Classification and task construction stay host-side by design (studio-sdk#71):
// the SDK poller hands over (taskID, title, body, errText) and only needs to know
// whether the error was recognized and queued.
type sdkRateLimitScheduler struct {
	scheduler *executor.Scheduler
}

func (s sdkRateLimitScheduler) QueueRetryIfRateLimited(taskID, title, body, errText string) bool {
	if s.scheduler == nil || !executor.IsRateLimitError(errText) {
		return false
	}
	rlInfo, ok := executor.ParseRateLimitError(errText)
	if !ok {
		return false
	}
	s.scheduler.QueueTask(&executor.Task{
		ID:          taskID,
		Title:       title,
		Description: body,
	}, rlInfo)
	return true
}

// githubPollerRegistration returns the studio-sdk GitHub issue-poller registration.
//
// M7 Phase 4b (studio-sdk v0.27.0): LIVE behind adapters.github.use_sdk_poller.
// When the flag is on, this registration polls the DEFAULT repo
// (adapters.github.repo) via the SDK poller with full host-hook parity —
// board source/sync, rate-limit scheduler, pre-flight judge, execution
// checkers, and issue metrics — and the in-tree poller skips that repo
// (main.go gateway + standalone default-repo blocks). Multi-repo `projects:`
// entries remain on the in-tree poller until Phase 4d.
//
// Known 4b limitation: the SDK adapter runs ExecutionModeAuto only —
// execution.mode=sequential configs should keep the flag off.
func githubPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "github",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.UseSDKPoller &&
				cfg.Adapters.GitHub.Repo != "" &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			ghCfg := deps.Cfg.Adapters.GitHub

			// Determine interval.
			interval := 30 * time.Second
			if ghCfg.Polling.Interval > 0 {
				interval = ghCfg.Polling.Interval
			}

			// Map internal config → SDK config (field names differ: PilotLabel vs TriggerLabel).
			pilotLabel := ghCfg.PilotLabel
			if pilotLabel == "" {
				pilotLabel = "pilot"
			}
			sdkCfg := &githubSDK.Config{
				Enabled:       ghCfg.Enabled,
				Token:         ghCfg.Token,
				WebhookSecret: ghCfg.WebhookSecret,
				Repo:          ghCfg.Repo,
				TriggerLabel:  pilotLabel,
				Polling: &githubSDK.PollingConfig{
					Enabled:  true,
					Interval: interval,
				},
			}

			// Board layer is config-driven in the SDK adapter (v0.27.0): mapping the
			// internal ProjectBoard config is all the wiring board mode needs.
			if pb := ghCfg.ProjectBoard; pb != nil {
				sdkCfg.ProjectBoard = &githubSDK.ProjectBoardConfig{
					Enabled:       pb.Enabled,
					ProjectNumber: pb.ProjectNumber,
					StatusField:   pb.StatusField,
					Statuses: githubSDK.ProjectStatuses{
						InProgress: pb.Statuses.InProgress,
						Review:     pb.Statuses.Review,
						Done:       pb.Statuses.Done,
						Failed:     pb.Statuses.Failed,
					},
					SourceEnabled: pb.SourceEnabled,
					SourceStatus:  pb.SourceStatus,
				}
			}

			pollerDeps := sdkcore.PollerDeps{
				Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
					return handleGithubIssueEventSDK(issueCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
				}),
				ProjectPath: deps.ProjectPath,
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
					// so the autopilot controller's post-merge gates and board-sync-to-Review work.
					issueNumber, _ := strconv.Atoi(prEv.IssueID)
					ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, issueNumber, prEv.HeadSHA, prEv.BranchName, prEv.IssueNodeID)
				}
				pollerDeps.IssueMetricsRecorder = ctrl.Metrics()
			}

			// GH-2201/GH-2242: task-queued gate + completed-execution guard.
			if deps.Store != nil {
				pollerDeps.TaskChecker = storeTaskChecker{store: deps.Store}
				pollerDeps.ExecutionChecker = deps.Store
			}

			// GH-2802: pre-flight judge (CC subprocess, no API key) — mirrors the
			// in-tree gating incl. the claude-binary lookup.
			if deps.Cfg.Executor != nil && deps.Cfg.Executor.PreFlightJudge != nil && deps.Cfg.Executor.PreFlightJudge.Enabled {
				claudeCmd := ""
				if deps.Cfg.Executor.ClaudeCode != nil {
					claudeCmd = deps.Cfg.Executor.ClaudeCode.Command
				}
				if claudeCmd == "" {
					claudeCmd = "claude"
				}
				if _, err := exec.LookPath(claudeCmd); err != nil {
					logging.WithComponent("github").Warn("Pre-flight judge disabled: claude binary not found",
						slog.String("command", claudeCmd))
				} else {
					pollerDeps.PreFlightJudge = sdkPreFlightJudge{judge: executor.NewIntentJudge(claudeCmd)}
					if deps.Store != nil {
						pollerDeps.ExecutionSaver = storeExecutionSaver{store: deps.Store}
					}
				}
			}

			// Rate-limit retry scheduler. The retry callback re-fetches the issue via
			// the SDK client and re-enters the SDK handler path, so the whole retry
			// loop stays on core.IssueEvent. Priority is left "" on retry — it only
			// affects queue ordering and the event is already past candidate selection.
			repoParts := strings.SplitN(ghCfg.Repo, "/", 2)
			if len(repoParts) != 2 {
				logging.WithComponent("github").Error("GitHub SDK poller disabled: invalid repo format",
					slog.String("repo", ghCfg.Repo))
				return
			}
			repoOwner, repoName := repoParts[0], repoParts[1]
			sdkClient := githubSDK.NewClient(ghCfg.Token)

			rateLimitScheduler := executor.NewScheduler(executor.DefaultSchedulerConfig(), nil)
			rateLimitScheduler.SetRetryCallback(func(retryCtx context.Context, pendingTask *executor.PendingTask) error {
				var issueNum int
				if _, err := fmt.Sscanf(pendingTask.Task.ID, "GH-%d", &issueNum); err != nil {
					return fmt.Errorf("invalid task ID format: %s", pendingTask.Task.ID)
				}

				issue, err := sdkClient.GetIssue(retryCtx, repoOwner, repoName, issueNum)
				if err != nil {
					return fmt.Errorf("failed to fetch issue for retry: %w", err)
				}

				logging.WithComponent("scheduler").Info("Retrying rate-limited issue (SDK path)",
					slog.Int("issue", issueNum),
					slog.Int("attempt", pendingTask.Attempts),
				)

				labelNames := make([]string, 0, len(issue.Labels))
				for _, l := range issue.Labels {
					labelNames = append(labelNames, l.Name)
				}
				ev := sdkcore.IssueEvent{
					Action:     "created",
					IssueID:    strconv.Itoa(issue.Number),
					SequenceID: pendingTask.Task.ID,
					Title:      issue.Title,
					Body:       issue.Body,
					Labels:     labelNames,
					ProjectID:  repoName,
				}

				result, err := handleGithubIssueEventSDK(retryCtx, deps.Cfg, ev, deps.ProjectPath, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

				// GH-797: surface retried-issue PRs to autopilot so their merge gates run.
				if result != nil && result.PRNumber > 0 && deps.AutopilotController != nil {
					deps.AutopilotController.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName, issue.NodeID)
				}

				return err
			})
			rateLimitScheduler.SetExpiredCallback(func(expiredCtx context.Context, pendingTask *executor.PendingTask) {
				logging.WithComponent("scheduler").Error("Task exceeded max retry attempts",
					slog.String("task_id", pendingTask.Task.ID),
					slog.Int("attempts", pendingTask.Attempts),
				)
			})
			if schErr := rateLimitScheduler.Start(ctx); schErr != nil {
				logging.WithComponent("start").Warn("Failed to start rate limit scheduler (SDK path)", slog.Any("error", schErr))
			}
			pollerDeps.RateLimitScheduler = sdkRateLimitScheduler{scheduler: rateLimitScheduler}

			// M7 4d.4: PRs for SDK-managed repos go through the SDK client instead
			// of the gh CLI. Startup-time registration keyed by repo — the runner
			// falls back to gh CLI for any github task without a registered creator.
			if deps.Runner != nil {
				deps.Runner.RegisterPRCreator("github:"+ghCfg.Repo, sdkshim.NewGitHubPRCreator(sdkClient, repoOwner, repoName))
			}

			githubPoller := githubSDK.New(sdkCfg).NewPoller(pollerDeps)

			logging.WithComponent("start").Info("GitHub SDK polling enabled (M7 4b)",
				slog.String("repo", ghCfg.Repo),
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
