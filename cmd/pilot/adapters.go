package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/qf-studio/pilot/internal/adapterhealth"
	"github.com/qf-studio/pilot/internal/adapters/slack"
	"github.com/qf-studio/pilot/internal/adapters/telegram"
	"github.com/qf-studio/pilot/internal/alerts"
	"github.com/qf-studio/pilot/internal/approval"
	"github.com/qf-studio/pilot/internal/autopilot"
	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/config"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/gateway"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/quality"
	"github.com/qf-studio/pilot/internal/teams"
)

// telegramBriefAdapter wraps telegram.Client to satisfy briefs.TelegramSender interface
type telegramBriefAdapter struct {
	client *telegram.Client
}

func (a *telegramBriefAdapter) SendBriefMessage(ctx context.Context, chatID, text, parseMode string) (*briefs.TelegramMessageResponse, error) {
	resp, err := a.client.SendMessage(ctx, chatID, text, parseMode, 0)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Result == nil {
		return nil, nil
	}
	return &briefs.TelegramMessageResponse{MessageID: resp.Result.MessageID}, nil
}

// telegramApprovalAdapter wraps telegram.Client to satisfy approval.TelegramClient interface
type telegramApprovalAdapter struct {
	client *telegram.Client
}

func (a *telegramApprovalAdapter) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]approval.InlineKeyboardButton, messageThreadID int64) (*approval.MessageResponse, error) {
	resp, err := a.client.SendMessageWithKeyboard(ctx, chatID, text, parseMode, convertKeyboardToTelegram(keyboard), messageThreadID)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return &approval.MessageResponse{
		Result: &approval.MessageResult{MessageID: resp.Result.MessageID},
	}, nil
}

func (a *telegramApprovalAdapter) EditMessage(ctx context.Context, chatID string, messageID int64, text, parseMode string) error {
	return a.client.EditMessage(ctx, chatID, messageID, text, parseMode)
}

func (a *telegramApprovalAdapter) AnswerCallback(ctx context.Context, callbackID, text string) error {
	return a.client.AnswerCallback(ctx, callbackID, text)
}

func convertKeyboardToTelegram(keyboard [][]approval.InlineKeyboardButton) [][]telegram.InlineKeyboardButton {
	result := make([][]telegram.InlineKeyboardButton, len(keyboard))
	for i, row := range keyboard {
		result[i] = make([]telegram.InlineKeyboardButton, len(row))
		for j, btn := range row {
			result[i][j] = telegram.InlineKeyboardButton{
				Text:         btn.Text,
				CallbackData: btn.CallbackData,
			}
		}
	}
	return result
}

// slackApprovalClientAdapter wraps slack.SlackClientAdapter to satisfy approval.SlackClient interface
type slackApprovalClientAdapter struct {
	adapter *slack.SlackClientAdapter
}

func (a *slackApprovalClientAdapter) PostInteractiveMessage(ctx context.Context, msg *approval.SlackInteractiveMessage) (*approval.SlackPostMessageResponse, error) {
	resp, err := a.adapter.PostInteractiveMessage(ctx, &slack.SlackApprovalMessage{
		Channel: msg.Channel,
		Text:    msg.Text,
		Blocks:  msg.Blocks,
	})
	if err != nil {
		return nil, err
	}
	return &approval.SlackPostMessageResponse{
		OK:      resp.OK,
		TS:      resp.TS,
		Channel: resp.Channel,
		Error:   resp.Error,
	}, nil
}

func (a *slackApprovalClientAdapter) UpdateInteractiveMessage(ctx context.Context, channel, ts string, blocks []interface{}, text string) error {
	return a.adapter.UpdateInteractiveMessage(ctx, channel, ts, blocks, text)
}

// wireProjectAccessChecker creates and wires a team-based project access checker on the runner (GH-635).
// It opens the teams DB, resolves the configured member, and returns a cleanup function.
// Returns nil cleanup if team config is absent or disabled.
func wireProjectAccessChecker(runner *executor.Runner, cfg *config.Config) func() {
	if cfg.Team == nil || !cfg.Team.Enabled {
		return nil
	}

	if cfg.Team.TeamID == "" || cfg.Team.MemberEmail == "" {
		logging.WithComponent("teams").Warn("team config enabled but team_id or member_email not set, skipping project access check")
		return nil
	}

	if cfg.Memory == nil || cfg.Memory.Path == "" {
		logging.WithComponent("teams").Warn("memory path not configured, skipping project access check")
		return nil
	}

	// Open teams DB (same pilot.db used by memory store)
	dbPath := cfg.Memory.Path + "/pilot.db"
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		logging.WithComponent("teams").Warn("failed to open teams DB", slog.Any("error", err))
		return nil
	}

	store, err := teams.NewStore(db)
	if err != nil {
		_ = db.Close()
		logging.WithComponent("teams").Warn("failed to create teams store", slog.Any("error", err))
		return nil
	}

	service := teams.NewService(store)

	// Resolve team
	team, err := service.GetTeamByName(cfg.Team.TeamID)
	if err != nil || team == nil {
		// Try by ID
		team, err = service.GetTeam(cfg.Team.TeamID)
	}
	if err != nil || team == nil {
		_ = db.Close()
		logging.WithComponent("teams").Warn("team not found, skipping project access check",
			slog.String("team", cfg.Team.TeamID))
		return nil
	}

	// Resolve member
	member, err := service.GetMemberByEmail(team.ID, cfg.Team.MemberEmail)
	if err != nil || member == nil {
		_ = db.Close()
		logging.WithComponent("teams").Warn("member not found in team, skipping project access check",
			slog.String("email", cfg.Team.MemberEmail),
			slog.String("team", team.Name))
		return nil
	}

	// Wire the checker via ServiceAdapter (GH-634 TeamChecker interface)
	adapter := teams.NewServiceAdapter(service)
	runner.SetTeamChecker(adapter)

	logging.WithComponent("teams").Info("project access checker enabled",
		slog.String("team", team.Name),
		slog.String("member", member.Email),
		slog.String("role", string(member.Role)))

	return func() { _ = db.Close() }
}

// getAlertsConfig extracts alerts configuration from the main config.
// GH-4866: delegates to config.AlertsConfig.ToAlertConfig so this
// conversion has exactly one implementation, shared with internal/health's
// doctor rule-coverage check (which can't reach this function directly —
// cmd/pilot imports internal/health, not the reverse).
func getAlertsConfig(cfg *config.Config) *alerts.AlertConfig {
	return cfg.Alerts.ToAlertConfig()
}

// qualityCheckerWrapper adapts quality.Executor to executor.QualityChecker interface
type qualityCheckerWrapper struct {
	executor *quality.Executor
}

// newProjectQualityCheckerFactory builds a per-task quality checker factory
// that resolves the effective quality gate config per project (GH-3716):
// the registered project's own `quality:` block takes precedence over the
// global `Config.Quality`, which in turn takes precedence over an
// auto-detected minimal build/test gate. This lets one Pilot deployment mix
// stacks (e.g. Go/Makefile + pnpm/Node) without a global config tuned for
// one stack forcing the wrong commands onto another.
func newProjectQualityCheckerFactory(cfg *config.Config) func(taskID, taskProjectPath string) executor.QualityChecker {
	return func(taskID, taskProjectPath string) executor.QualityChecker {
		var projectQuality *quality.Config
		if proj := cfg.FindProjectByPath(taskProjectPath); proj != nil {
			projectQuality = proj.Quality
		}
		resolved := quality.ResolveConfig(projectQuality, cfg.Quality, taskProjectPath)
		return &qualityCheckerWrapper{
			executor: quality.NewExecutor(&quality.ExecutorConfig{
				Config:      resolved,
				ProjectPath: taskProjectPath,
				TaskID:      taskID,
			}),
		}
	}
}

// newProjectContractDependencyLookup builds an executor.ContractDependencyLookup
// that resolves a project's configured `contract_dependencies` (GH-5010) per
// task project path, mirroring newProjectQualityCheckerFactory above.
//
// internal/executor cannot import internal/config (import cycle risk via
// internal/comms — see internal/executor/contract_evidence.go's package doc),
// so ContractDependency is duplicated there as an executor-local mirror.
// This function is the one place that bridges the two shapes (GH-5013): it
// resolves the task's project via cfg.FindProjectByPath and translates each
// config.ContractDependency into its executor.ContractDependency twin. A
// project with no `contract_dependencies` configured (or no matching
// project) yields an empty slice — the Contract Evidence gate (GH-5009)
// treats that as a complete no-op, per its first acceptance criterion.
func newProjectContractDependencyLookup(cfg *config.Config) executor.ContractDependencyLookup {
	return func(taskProjectPath string) []executor.ContractDependency {
		proj := cfg.FindProjectByPath(taskProjectPath)
		if proj == nil || len(proj.ContractDependencies) == 0 {
			return nil
		}
		deps := make([]executor.ContractDependency, len(proj.ContractDependencies))
		for i, d := range proj.ContractDependencies {
			deps[i] = executor.ContractDependency{
				Owner:         d.Owner,
				Repo:          d.Repo,
				ContractFiles: d.ContractFiles,
				Ref:           d.Ref,
			}
		}
		return deps
	}
}

// newProjectContractContentFetcher builds an executor.ContractContentFetcher
// backed by the shared *github.Client (GH-5022, the activation leg of
// GH-5009/GH-5011). *github.Client's GetFileContent method (PR#5015)
// already matches ContractContentFetcher's signature exactly, so
// newGitHubClient(cfg) satisfies the interface directly — no adapter type
// needed. Named/wrapped as its own function (rather than passing
// newGitHubClient(cfg) inline at each call site) to mirror
// newProjectContractDependencyLookup's shape and give
// TestContractContentFetcherWiredAtEveryQualityCheckerFactorySite a stable
// grep target.
func newProjectContractContentFetcher(cfg *config.Config) executor.ContractContentFetcher {
	return newGitHubClient(cfg)
}

// Check implements executor.QualityChecker by delegating to quality.Executor
// and converting the result type
func (w *qualityCheckerWrapper) Check(ctx context.Context) (*executor.QualityOutcome, error) {
	outcome, err := w.executor.Check(ctx)
	if err != nil {
		return nil, err
	}

	result := &executor.QualityOutcome{
		Passed:        outcome.Passed,
		ShouldRetry:   outcome.ShouldRetry,
		RetryFeedback: outcome.RetryFeedback,
		Attempt:       outcome.Attempt,
	}

	// Populate gate details if results are available (GH-209)
	if outcome.Results != nil {
		result.TotalDuration = outcome.Results.TotalTime
		result.GateDetails = make([]executor.QualityGateDetail, len(outcome.Results.Results))
		for i, r := range outcome.Results.Results {
			result.GateDetails[i] = executor.QualityGateDetail{
				Name:       r.GateName,
				Passed:     r.Status == quality.StatusPassed,
				Duration:   r.Duration,
				RetryCount: r.RetryCount,
				Error:      r.Error,
			}
		}
	}

	return result, nil
}

// autopilotProviderAdapter wraps autopilot.Controller to satisfy gateway.AutopilotProvider.
// GH-1585: Bridges autopilot controller to gateway API for /api/v1/autopilot endpoint.
type autopilotProviderAdapter struct {
	controller *autopilot.Controller
}

func (a *autopilotProviderAdapter) GetEnvironment() string {
	return a.controller.Config().EnvironmentName()
}

func (a *autopilotProviderAdapter) GetActivePRs() []*gateway.AutopilotPRState {
	prs := a.controller.GetActivePRs()
	result := make([]*gateway.AutopilotPRState, 0, len(prs))
	for _, pr := range prs {
		result = append(result, &gateway.AutopilotPRState{
			PRNumber:   pr.PRNumber,
			PRURL:      pr.PRURL,
			Stage:      string(pr.Stage),
			CIStatus:   string(pr.CIStatus),
			Error:      pr.Error,
			BranchName: pr.BranchName,
		})
	}
	return result
}

func (a *autopilotProviderAdapter) GetFailureCount() int {
	return a.controller.TotalFailures()
}

func (a *autopilotProviderAdapter) IsAutoReleaseEnabled() bool {
	cfg := a.controller.Config()
	return cfg.Release != nil && cfg.Release.Enabled
}

// adapterHealthProviderAdapter wraps adapterhealth.Registry to satisfy
// gateway.AdapterHealthSource (GH-4314), so /api/v1/status can surface which
// adapters have panicked/restarted/been disabled.
type adapterHealthProviderAdapter struct {
	registry *adapterhealth.Registry
}

func (a *adapterHealthProviderAdapter) AdapterHealthSnapshot() []gateway.AdapterHealthStatus {
	statuses := a.registry.Snapshot()
	result := make([]gateway.AdapterHealthStatus, len(statuses))
	for i, st := range statuses {
		result[i] = gateway.AdapterHealthStatus{
			Name:         st.Name,
			Healthy:      st.Healthy,
			Disabled:     st.Disabled,
			LastError:    st.LastError,
			RestartCount: st.RestartCount,
		}
	}
	return result
}

// resolveOwnerRepo determines the GitHub owner and repo from config or git remote.
func resolveOwnerRepo(cfg *config.Config) (string, string, error) {
	// Try config first
	ghCfg := cfg.Adapters.GitHub
	if ghCfg != nil && ghCfg.Repo != "" {
		parts := strings.SplitN(ghCfg.Repo, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
	}

	// Try git remote
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("could not determine repository - set github.repo in config")
	}

	// Parse remote URL (handles both HTTPS and SSH)
	remote := strings.TrimSpace(string(out))
	// git@github.com:owner/repo.git
	// https://github.com/owner/repo.git
	remote = strings.TrimSuffix(remote, ".git")

	if strings.Contains(remote, "github.com:") {
		parts := strings.Split(remote, "github.com:")
		if len(parts) == 2 {
			ownerRepo := strings.Split(parts[1], "/")
			if len(ownerRepo) == 2 {
				return ownerRepo[0], ownerRepo[1], nil
			}
		}
	}

	if strings.Contains(remote, "github.com/") {
		parts := strings.Split(remote, "github.com/")
		if len(parts) == 2 {
			ownerRepo := strings.Split(parts[1], "/")
			if len(ownerRepo) == 2 {
				return ownerRepo[0], ownerRepo[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("could not parse GitHub remote: %s", remote)
}
