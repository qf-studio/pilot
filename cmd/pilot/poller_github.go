package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	sdkcore "github.com/qf-studio/studio-sdk/sdk/core"
	githubSDK "github.com/qf-studio/studio-sdk/sdk/integrations/github"

	"github.com/qf-studio/pilot/internal/adapters/sdkshim"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
)

// githubOnPRCreatedHandler builds the callback wired into pollerDeps.OnPRCreated:
// it forwards the SDK's PRCreatedEvent into Controller.OnPRCreated, the sole live
// entry point for the GH-4130 throughput-histogram observation. Extracted to its
// own function (GH-4211) so a regression test can drive the real handler that
// production wires up, instead of calling ctrl.OnPRCreated directly — the gap
// that let GH-4130's observation ship false-green in the first place.
func githubOnPRCreatedHandler(ctrl *autopilot.Controller) func(sdkcore.PRCreatedEvent) {
	return func(prEv sdkcore.PRCreatedEvent) {
		issueNumber, _ := strconv.Atoi(prEv.IssueID)
		ctrl.OnPRCreated(prEv.PRNumber, prEv.PRURL, issueNumber, prEv.HeadSHA, prEv.BranchName, prEv.IssueNodeID)
	}
}

// sdkPollerVerifyTimeout bounds the one-off authenticated call made before
// the SDK poller starts (GH-3917), matching preflightVerifyTimeout's budget
// for the equivalent in-tree adapter checks (adapter_preflight.go).
const sdkPollerVerifyTimeout = 8 * time.Second

// verifySDKGithubToken makes one authenticated call to confirm the SDK
// poller's resolved token actually works, fail-loud (GH-3917): live incident
// 2026-07-06 had the SDK poller sending empty credentials on every 30s poll
// for ~16 minutes while startup still logged "polling enabled". Mirrors
// validateGitHubToken's in-tree pattern (main.go) — a confirmed 401
// (AuthError) disables the poller (returns false); network errors, rate
// limits, etc. are not evidence the token itself is dead, so the poller
// still starts.
func verifySDKGithubToken(ctx context.Context, client *githubSDK.Client, tokenSource githubTokenSource, alertsEngine *alerts.Engine) bool {
	log := logging.WithComponent("github-sdk-poller")

	vctx, cancel := context.WithTimeout(ctx, sdkPollerVerifyTimeout)
	defer cancel()

	if _, err := client.GetAuthenticatedUser(vctx); err != nil {
		var authErr *githubSDK.AuthError
		if errors.As(err, &authErr) {
			log.Error("GitHub SDK poller disabled: token rejected by API (401)",
				slog.String("token_source", string(tokenSource)),
				slog.String("fix", "rotate the token at its source and restart pilot"),
			)
			if alertsEngine != nil {
				alertsEngine.ProcessEvent(alerts.Event{
					Type:      alerts.EventTypeConfigError,
					Error:     fmt.Sprintf("GitHub SDK poller token (source: %s) is invalid or expired — 401 from GitHub API", tokenSource),
					Timestamp: time.Now(),
				})
			}
			return false
		}
		log.Warn("could not verify GitHub SDK poller token validity at startup — proceeding anyway",
			slog.String("token_source", string(tokenSource)),
			slog.String("error", err.Error()),
		)
		return true
	}

	log.Info("GitHub SDK poller token validated", slog.String("token_source", string(tokenSource)))
	return true
}

// sdkPreFlightJudge adapts *executor.IntentJudge to sdkcore.PreFlightJudger,
// returning the SDK's core.Verdict.
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
// M7 Phase 4d.2b (studio-sdk v0.30.0): fans out one SDK poller per repo — the
// DEFAULT repo (adapters.github.repo) plus every projects[] GitHub entry — each
// with full host-hook parity (rate-limit scheduler, pre-flight judge, execution
// checkers, issue metrics, per-repo autopilot controller and PR creator). The
// default repo additionally gets board source/sync wiring; project repos do not
// (parity with the in-tree path).
// M7 Phase 4d.6 (GH-4171): the in-tree fallback poller is gone (GH-4170), so this
// registration is now unconditional for every GitHub repo — UseSDKPoller is still
// parsed off adapters.github.use_sdk_poller for backward-compat config loading
// but no longer gates anything here (see config.CheckDeprecations for the
// startup warning).
//
// Known 4b limitation: the SDK adapter runs ExecutionModeAuto only —
// execution.mode=sequential configs are not supported.
func githubPollerRegistration() PollerRegistration {
	return PollerRegistration{
		Name: "github",
		Enabled: func(cfg *config.Config) bool {
			return cfg.Adapters.GitHub != nil && cfg.Adapters.GitHub.Enabled &&
				cfg.Adapters.GitHub.Repo != "" &&
				cfg.Adapters.GitHub.Polling != nil && cfg.Adapters.GitHub.Polling.Enabled
		},
		CreateAndStart: func(ctx context.Context, deps *PollerDeps) {
			log := logging.WithComponent("github-sdk-poller")

			// GH-3917: resolve the token via the same chain every in-tree GitHub
			// path uses (config -> GITHUB_TOKEN env -> `gh auth token` CLI) instead
			// of trusting ghCfg.Token verbatim. With token: "" in config (the common
			// setup), the SDK poller previously sent empty credentials on every poll
			// while startup still logged "polling enabled". One token is shared across
			// every per-repo poller (M7 4d.2b fan-out).
			token, tokenSource := resolveGitHubToken(deps.Cfg)
			if token == "" {
				log.Error("GitHub SDK poller disabled: no token resolved",
					slog.String("resolution_chain", "adapters.github.token config -> GITHUB_TOKEN env -> gh auth token CLI"),
				)
				return
			}

			// GH-3917: fail loud on a dead/invalid token once, up front, instead of
			// letting every repo's poll silently 401.
			if !verifySDKGithubToken(ctx, githubSDK.NewClient(token), tokenSource, deps.AlertsEngine) {
				return
			}

			// M7 4d.2b: one SDK poller per repo — default adapter repo + projects[].
			targets := githubSDKPollerTargets(deps.Cfg, deps.ProjectPath)
			started := 0
			for _, target := range targets {
				if startGithubSDKPollerForRepo(ctx, deps, log, token, tokenSource, target) {
					started++
				}
			}
			log.Info("GitHub SDK polling enabled (M7 4b/4d.2b)",
				slog.Int("repos_configured", len(targets)),
				slog.Int("repos_started", started),
				slog.String("token_source", string(tokenSource)),
			)
		},
	}
}

// githubSDKPollerTarget is one repo the SDK poller fan-out drives.
type githubSDKPollerTarget struct {
	repoFullName string // "owner/repo"
	projectPath  string
	isDefault    bool // the default adapter repo — the only target that gets board wiring
}

// githubSDKPollerTargets derives the repo set the SDK poller drives when
// use_sdk_poller is on (M7 4d.2b): the default adapter repo (adapters.github.repo)
// first, then every projects[] entry with a GitHub owner/repo, in config order,
// de-duplicated. defaultProjectPath is used for the default repo and as the fallback
// for a project entry with no explicit Path.
func githubSDKPollerTargets(cfg *config.Config, defaultProjectPath string) []githubSDKPollerTarget {
	var targets []githubSDKPollerTarget
	seen := make(map[string]bool)
	if gh := cfg.Adapters.GitHub; gh != nil && gh.Repo != "" {
		targets = append(targets, githubSDKPollerTarget{repoFullName: gh.Repo, projectPath: defaultProjectPath, isDefault: true})
		seen[gh.Repo] = true
	}
	for _, proj := range cfg.Projects {
		if proj.GitHub == nil || proj.GitHub.Owner == "" || proj.GitHub.Repo == "" {
			continue
		}
		full := proj.GitHub.Owner + "/" + proj.GitHub.Repo
		if seen[full] {
			continue
		}
		seen[full] = true
		projPath := proj.Path
		if projPath == "" {
			projPath = defaultProjectPath
		}
		targets = append(targets, githubSDKPollerTarget{repoFullName: full, projectPath: projPath, isDefault: false})
	}
	return targets
}

// startGithubSDKPollerForRepo constructs and starts one SDK poller for target.
// The shared token is already resolved and verified by the caller. Returns false
// (and logs) without starting when the repo cannot be driven safely (invalid repo
// format). Each poller carries its own repo identity, project path, autopilot
// controller, rate-limit scheduler and PR creator; they share the token and the
// repo-scoped processed store.
func startGithubSDKPollerForRepo(ctx context.Context, deps *PollerDeps, log *slog.Logger, token string, tokenSource githubTokenSource, target githubSDKPollerTarget) bool {
	ghCfg := deps.Cfg.Adapters.GitHub

	repoParts := strings.SplitN(target.repoFullName, "/", 2)
	if len(repoParts) != 2 || repoParts[0] == "" || repoParts[1] == "" {
		log.Error("GitHub SDK poller skipped: invalid repo format",
			slog.String("repo", target.repoFullName))
		return false
	}
	repoOwner, repoName := repoParts[0], repoParts[1]
	repoLog := log.With(slog.String("repo", target.repoFullName))

	// Determine interval (shared adapter polling config; projects have no per-repo interval).
	interval := 30 * time.Second
	if ghCfg.Polling != nil && ghCfg.Polling.Interval > 0 {
		interval = ghCfg.Polling.Interval
	}

	// Map internal config → SDK config (field names differ: PilotLabel vs TriggerLabel).
	pilotLabel := ghCfg.PilotLabel
	if pilotLabel == "" {
		pilotLabel = "pilot"
	}
	sdkCfg := &githubSDK.Config{
		Enabled:       ghCfg.Enabled,
		Token:         token,
		WebhookSecret: ghCfg.WebhookSecret,
		Repo:          target.repoFullName,
		TriggerLabel:  pilotLabel,
		Polling: &githubSDK.PollingConfig{
			Enabled:  true,
			Interval: interval,
		},
	}

	// Board layer is wired for the default repo only — parity with the in-tree path,
	// which gates board source/sync to adapters.github.repo (main.go). Per-project
	// board sync is a 4d.6+ question; project repos get no board wiring.
	if target.isDefault {
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
	}

	pollerDeps := sdkcore.PollerDeps{
		Handler: sdkcore.IssueHandlerFunc(func(issueCtx context.Context, ev sdkcore.IssueEvent) (*sdkcore.IssueResult, error) {
			// M7 4d.2c: pass the poller's own repo explicitly so the handler does not
			// have to resolve it from the event by name (ambiguous across same-named repos).
			return handleGithubIssueEventSDK(issueCtx, deps.Cfg, ev, target.projectPath, target.repoFullName, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)
		}),
		ProjectPath: target.projectPath,
		// GH-3921: tag every poller-originated log line with the component +
		// repo, so incidents no longer reach the daemon log untagged.
		Logger: repoLog,
	}

	if deps.AutopilotStateStore != nil {
		pollerDeps.ProcessedStore = deps.AutopilotStateStore
	}
	if deps.Cfg.Orchestrator.MaxConcurrent > 0 {
		pollerDeps.MaxConcurrent = deps.Cfg.Orchestrator.MaxConcurrent
	}

	// M7 4d.2b: per-repo autopilot controller (keyed owner/repo). The default repo
	// falls back to the backwards-compat singular controller. When autopilot is
	// enabled but this repo has no controller, fail loud — its PRs would strand
	// unmerged — but still start the poller so issues are not silently dropped.
	controller := deps.AutopilotControllers[target.repoFullName]
	if controller == nil && target.isDefault {
		controller = deps.AutopilotController
	}
	if controller != nil {
		ctrl := controller
		// GitHub's IssueID is the numeric issue number; forwarded (with the node ID)
		// so the autopilot controller's post-merge gates and board-sync-to-Review work.
		pollerDeps.OnPRCreated = githubOnPRCreatedHandler(ctrl)
		pollerDeps.IssueMetricsRecorder = ctrl.Metrics()
	} else if len(deps.AutopilotControllers) > 0 {
		repoLog.Error("GitHub SDK poller: no autopilot controller for repo — PRs from this repo will not be auto-merged; check projects[] vs autopilot config")
	}

	// GH-2201/GH-2242: task-queued gate + completed-execution guard.
	// GH-4276: storeTaskChecker is scoped to this poller's own project so a
	// same-numbered task_id active in a different project never counts here.
	if deps.Store != nil {
		pollerDeps.TaskChecker = storeTaskChecker{store: deps.Store, projectPath: target.projectPath}
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
			repoLog.Warn("Pre-flight judge disabled: claude binary not found",
				slog.String("command", claudeCmd))
		} else {
			pollerDeps.PreFlightJudge = sdkPreFlightJudge{judge: executor.NewIntentJudge(claudeCmd)}
			if deps.Store != nil {
				pollerDeps.ExecutionSaver = storeExecutionSaver{store: deps.Store}
			}
		}
	}

	// Rate-limit retry scheduler. The retry callback re-fetches the issue via the
	// SDK client and re-enters the SDK handler path, so the whole retry loop stays
	// on core.IssueEvent. Priority is left "" on retry — it only affects queue
	// ordering and the event is already past candidate selection.
	sdkClient := githubSDK.NewClient(token)
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
			slog.String("repo", target.repoFullName),
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

		result, err := handleGithubIssueEventSDK(retryCtx, deps.Cfg, ev, target.projectPath, target.repoFullName, deps.Dispatcher, deps.Runner, deps.Monitor, deps.Program, deps.AlertsEngine, deps.Enforcer)

		// GH-797: surface retried-issue PRs to autopilot so their merge gates run.
		if result != nil && result.PRNumber > 0 && controller != nil {
			controller.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName, issue.NodeID)
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

	// M7 4d.4: PRs for SDK-managed repos go through the SDK client instead of the gh
	// CLI. Startup-time registration keyed by "github:owner/repo" — the runner falls
	// back to gh CLI for any github task without a registered creator.
	if deps.Runner != nil {
		deps.Runner.RegisterPRCreator("github:"+target.repoFullName, sdkshim.NewGitHubPRCreator(sdkClient, repoOwner, repoName))
	}

	githubPoller := githubSDK.New(sdkCfg).NewPoller(pollerDeps)

	// GH-4110: publish the SDK poller handle to the shared repo-keyed registry so
	// the main.go sub-issue-skip / done-remark / stale-label loops can mark/clear it.
	// NewPoller returns the core.Poller interface (Start only), so assert to the
	// mark/clear surface and fail loud if a future SDK drops it.
	if deps.GitHubPollers != nil {
		if marker, ok := githubPoller.(githubProcessedMarker); ok {
			deps.GitHubPollers.add(target.repoFullName, marker)
		} else {
			repoLog.Error("GitHub SDK poller does not expose MarkProcessed/ClearProcessed; cross-poller sub-issue skip and stale-label recovery cannot reach it (GH-4110)")
		}
	}

	repoLog.Info("GitHub SDK poller started",
		slog.String("label", pilotLabel),
		slog.String("token_source", string(tokenSource)),
		slog.Duration("interval", interval),
		slog.Bool("default_repo", target.isDefault),
		slog.Bool("board_wired", sdkCfg.ProjectBoard != nil),
	)
	go func() {
		if err := githubPoller.Start(ctx); err != nil {
			repoLog.Error("GitHub SDK poller failed",
				slog.Any("error", err),
			)
		}
	}()
	return true
}
