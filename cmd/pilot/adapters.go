package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/alekspetrov/pilot/internal/adapters/slack"
	"github.com/alekspetrov/pilot/internal/adapters/telegram"
	"github.com/alekspetrov/pilot/internal/alerts"
	"github.com/alekspetrov/pilot/internal/approval"
	"github.com/alekspetrov/pilot/internal/autopilot"
	"github.com/alekspetrov/pilot/internal/briefs"
	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/gateway"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/quality"
	"github.com/alekspetrov/pilot/internal/teams"
)

// telegramBriefAdapter wraps telegram.Client to satisfy briefs.TelegramSender interface
type telegramBriefAdapter struct {
	client *telegram.Client
}

func (a *telegramBriefAdapter) SendBriefMessage(ctx context.Context, chatID, text, parseMode string) (*briefs.TelegramMessageResponse, error) {
	resp, err := a.client.SendMessage(ctx, chatID, text, parseMode)
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

func (a *telegramApprovalAdapter) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]approval.InlineKeyboardButton) (*approval.MessageResponse, error) {
	resp, err := a.client.SendMessageWithKeyboard(ctx, chatID, text, parseMode, convertKeyboardToTelegram(keyboard))
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

// getAlertsConfig extracts alerts configuration from the main config
func getAlertsConfig(cfg *config.Config) *alerts.AlertConfig {
	if cfg.Alerts == nil {
		return nil
	}

	alertsCfg := cfg.Alerts

	// Convert to alerts package types (channel configs are shared types, passed directly)
	channels := make([]alerts.ChannelConfigInput, 0, len(alertsCfg.Channels))
	for _, ch := range alertsCfg.Channels {
		channels = append(channels, alerts.ChannelConfigInput{
			Name:       ch.Name,
			Type:       ch.Type,
			Enabled:    ch.Enabled,
			Severities: ch.Severities,
			Slack:      ch.Slack,     // Same type, direct pass-through
			Telegram:   ch.Telegram,  // Same type, direct pass-through
			Email:      ch.Email,     // Same type, direct pass-through
			Webhook:    ch.Webhook,   // Same type, direct pass-through
			PagerDuty:  ch.PagerDuty, // Same type, direct pass-through
		})
	}

	rules := make([]alerts.RuleConfigInput, 0, len(alertsCfg.Rules))
	for _, r := range alertsCfg.Rules {
		rules = append(rules, alerts.RuleConfigInput{
			Name:        r.Name,
			Type:        r.Type,
			Enabled:     r.Enabled,
			Severity:    r.Severity,
			Channels:    r.Channels,
			Cooldown:    r.Cooldown,
			Description: r.Description,
			Condition: alerts.ConditionConfigInput{
				ProgressUnchangedFor: r.Condition.ProgressUnchangedFor,
				ConsecutiveFailures:  r.Condition.ConsecutiveFailures,
				DailySpendThreshold:  r.Condition.DailySpendThreshold,
				BudgetLimit:          r.Condition.BudgetLimit,
				UsageSpikePercent:    r.Condition.UsageSpikePercent,
				Pattern:              r.Condition.Pattern,
				FilePattern:          r.Condition.FilePattern,
				Paths:                r.Condition.Paths,
			},
		})
	}

	defaults := alerts.DefaultsConfigInput{
		Cooldown:           alertsCfg.Defaults.Cooldown,
		DefaultSeverity:    alertsCfg.Defaults.DefaultSeverity,
		SuppressDuplicates: alertsCfg.Defaults.SuppressDuplicates,
	}

	return alerts.FromConfigAlerts(alertsCfg.Enabled, channels, rules, defaults)
}

// qualityCheckerWrapper adapts quality.Executor to executor.QualityChecker interface
type qualityCheckerWrapper struct {
	executor *quality.Executor
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
