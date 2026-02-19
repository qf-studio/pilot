package autopilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/alekspetrov/pilot/internal/adapters/telegram"
)

// TelegramNotifier sends autopilot notifications to Telegram.
type TelegramNotifier struct {
	client *telegram.Client
	chatID string
}

// NewTelegramNotifier creates a Telegram notifier for autopilot events.
func NewTelegramNotifier(client *telegram.Client, chatID string) *TelegramNotifier {
	return &TelegramNotifier{
		client: client,
		chatID: chatID,
	}
}

// envPrefix returns the "[env] " prefix for notification messages.
// Returns empty string when EnvironmentName is not set.
func envPrefix(prState *PRState) string {
	if prState.EnvironmentName == "" {
		return ""
	}
	return fmt.Sprintf("[%s] ", prState.EnvironmentName)
}

// prDetail returns PR title and target branch details when available.
func prDetail(prState *PRState) string {
	var parts []string
	if prState.PRTitle != "" {
		parts = append(parts, fmt.Sprintf("Title: %s", prState.PRTitle))
	}
	if prState.TargetBranch != "" {
		parts = append(parts, fmt.Sprintf("Branch: `%s`", prState.TargetBranch))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, "\n")
}

// NotifyMerged sends notification when a PR is successfully merged.
func (n *TelegramNotifier) NotifyMerged(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("%s✅ *PR #%d merged*%s\n\n"+
		"Method: squash",
		envPrefix(prState), prState.PRNumber, prDetail(prState))

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyCIFailed sends notification when CI checks fail.
func (n *TelegramNotifier) NotifyCIFailed(ctx context.Context, prState *PRState, failedChecks []string) error {
	var checks string
	if len(failedChecks) > 0 {
		checks = "Failed checks:\n"
		for _, c := range failedChecks {
			checks += fmt.Sprintf("  • `%s`\n", c)
		}
	} else {
		checks = "Failed checks: _unknown_"
	}

	msg := fmt.Sprintf("%s❌ *CI Failed* for PR #%d%s\n\n%s",
		envPrefix(prState), prState.PRNumber, prDetail(prState), checks)

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyApprovalRequired sends notification when a PR requires human approval.
func (n *TelegramNotifier) NotifyApprovalRequired(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("%s⏳ *Approval Required*\n\n"+
		"PR #%d is ready for production merge.%s\n"+
		"Reply `/approve %d` or `/reject %d`",
		envPrefix(prState), prState.PRNumber, prDetail(prState),
		prState.PRNumber, prState.PRNumber)

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyFixIssueCreated sends notification when a fix issue is auto-created.
func (n *TelegramNotifier) NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error {
	msg := fmt.Sprintf("%s🔄 *Fix Issue Created*\n\n"+
		"Issue #%d created to fix failures from PR #%d.\n"+
		"Pilot will pick this up automatically.",
		envPrefix(prState), issueNumber, prState.PRNumber)

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyPipelineComplete sends notification when the full pipeline completes for a PR.
func (n *TelegramNotifier) NotifyPipelineComplete(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("%s🏁 *Pipeline complete* for GH-%d — PR #%d merged%s",
		envPrefix(prState), prState.IssueNumber, prState.PRNumber, prDetail(prState))

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyReleased sends notification when a release is created.
func (n *TelegramNotifier) NotifyReleased(ctx context.Context, prState *PRState, releaseURL string) error {
	bumpLabel := "release"
	switch prState.ReleaseBumpType {
	case BumpMajor:
		bumpLabel = "major release"
	case BumpMinor:
		bumpLabel = "minor release"
	case BumpPatch:
		bumpLabel = "patch release"
	}

	msg := fmt.Sprintf("%s✨ *Release %s Published*\n\n"+
		"Version: `%s`\n"+
		"Type: %s\n"+
		"From PR: #%d\n\n"+
		"[View Release](%s)",
		envPrefix(prState),
		escapeMarkdown(prState.ReleaseVersion),
		prState.ReleaseVersion,
		bumpLabel,
		prState.PRNumber,
		releaseURL,
	)

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// escapeMarkdown escapes special characters for Telegram Markdown.
func escapeMarkdown(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(s)
}
