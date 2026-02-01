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

// NotifyMerged sends notification when a PR is successfully merged.
func (n *TelegramNotifier) NotifyMerged(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("✅ *PR #%d merged*\n\n"+
		"Environment: `%s`\n"+
		"Method: squash",
		prState.PRNumber, prState.Stage)

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

	msg := fmt.Sprintf("❌ *CI Failed* for PR #%d\n\n%s",
		prState.PRNumber, checks)

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyApprovalRequired sends notification when a PR requires human approval.
func (n *TelegramNotifier) NotifyApprovalRequired(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("⏳ *Approval Required*\n\n"+
		"PR #%d is ready for production merge.\n"+
		"Reply `/approve %d` or `/reject %d`",
		prState.PRNumber, prState.PRNumber, prState.PRNumber)

	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown")
	return err
}

// NotifyFixIssueCreated sends notification when a fix issue is auto-created.
func (n *TelegramNotifier) NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error {
	msg := fmt.Sprintf("🔄 *Fix Issue Created*\n\n"+
		"Issue #%d created to fix failures from PR #%d.\n"+
		"Pilot will pick this up automatically.",
		issueNumber, prState.PRNumber)

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

	msg := fmt.Sprintf("✨ *Release %s Published*\n\n"+
		"Version: `%s`\n"+
		"Type: %s\n"+
		"From PR: #%d\n\n"+
		"[View Release](%s)",
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
