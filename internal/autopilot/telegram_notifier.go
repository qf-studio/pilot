package autopilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/qf-studio/pilot/internal/adapters/telegram"
)

// TelegramNotifier sends autopilot notifications to Telegram.
type TelegramNotifier struct {
	client          *telegram.Client
	chatID          string
	messageThreadID int64
}

// NewTelegramNotifier creates a Telegram notifier for autopilot events.
func NewTelegramNotifier(client *telegram.Client, chatID string, messageThreadID int64) *TelegramNotifier {
	return &TelegramNotifier{
		client:          client,
		chatID:          chatID,
		messageThreadID: messageThreadID,
	}
}

// envPrefix returns the "[env] " prefix for notification messages.
// Returns empty string when EnvironmentName is not set.
func envPrefix(prState *PRState) string {
	if prState.EnvironmentName == "" {
		return ""
	}
	return fmt.Sprintf("[%s] ", escapeLegacyMarkdown(prState.EnvironmentName))
}

// prDetail returns PR title and target branch details when available.
func prDetail(prState *PRState) string {
	var parts []string
	if prState.PRTitle != "" {
		parts = append(parts, fmt.Sprintf("Title: %s", escapeLegacyMarkdown(prState.PRTitle)))
	}
	if prState.TargetBranch != "" {
		parts = append(parts, fmt.Sprintf("Branch: `%s`", escapeCodeSpan(prState.TargetBranch)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, "\n")
}

// escapeLegacyMarkdown escapes only the characters that are special in
// Telegram's legacy "Markdown" parse mode (the mode NotifyApprovalRequired
// sends with); the V2-style escapeMarkdown would leave literal backslashes.
func escapeLegacyMarkdown(s string) string {
	return strings.NewReplacer("_", "\\_", "*", "\\*", "`", "\\`", "[", "\\[").Replace(s)
}

// escapeCodeSpan makes s safe inside a legacy-Markdown `code` span, where
// backslash escapes are not honored and only a backtick ends the entity.
func escapeCodeSpan(s string) string {
	return strings.ReplaceAll(s, "`", "'")
}

// send delivers msg in legacy Markdown. GH-6276: when Telegram rejects the
// entities (400 "can't parse entities", e.g. an unescaped delimiter in free
// text that slipped through), the same text is sent once more with no parse
// mode so the notice still arrives instead of being lost to a Warn line.
func (n *TelegramNotifier) send(ctx context.Context, msg string) error {
	_, err := n.client.SendMessage(ctx, n.chatID, msg, "Markdown", n.messageThreadID)
	if err != nil && telegram.IsParseEntitiesError(err) {
		_, err = n.client.SendMessage(ctx, n.chatID, msg, "", n.messageThreadID)
	}
	return err
}

// approvalReasonDetail renders why the PR is gated (EscalationReason) and, for
// a live-smoke Not-verified hold, the bullets plus what approving means, so the
// chat message does not read as the routine require_approval gate (GH-5599).
// Returns "" when there is no reason, otherwise a block ending in a newline.
func approvalReasonDetail(prState *PRState) string {
	if prState.EscalationReason == "" {
		return ""
	}
	// The full combined reason is always shown: size-floor, scope-drift and
	// test-evidence segments are gates the approver also releases (GH-5602).
	var b strings.Builder
	fmt.Fprintf(&b, "Reason: %s\n", escapeLegacyMarkdown(prState.EscalationReason))
	if !isLiveSmokeHold(prState) {
		return b.String()
	}
	b.WriteString("⚠️ *Held: key-gated / live-service check not run*\n")
	for _, bullet := range liveSmokeBulletsFromReason(prState.EscalationReason) {
		fmt.Fprintf(&b, "  • %s\n", escapeLegacyMarkdown(bullet))
	}
	b.WriteString(escapeLegacyMarkdown(liveSmokeApprovalNote))
	b.WriteString("\n")
	return b.String()
}

// NotifyMerged sends notification when a PR is successfully merged.
func (n *TelegramNotifier) NotifyMerged(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("%s✅ *PR #%d merged*%s\n\n"+
		"Method: squash",
		envPrefix(prState), prState.PRNumber, prDetail(prState))

	return n.send(ctx, msg)
}

// NotifyCIFailed sends notification when CI checks fail.
func (n *TelegramNotifier) NotifyCIFailed(ctx context.Context, prState *PRState, failedChecks []string) error {
	var checks string
	if len(failedChecks) > 0 {
		checks = "Failed checks:\n"
		for _, c := range failedChecks {
			checks += fmt.Sprintf("  • `%s`\n", escapeCodeSpan(c))
		}
	} else {
		checks = "Failed checks: _unknown_"
	}

	msg := fmt.Sprintf("%s❌ *CI Failed* for PR #%d%s\n\n%s",
		envPrefix(prState), prState.PRNumber, prDetail(prState), checks)

	return n.send(ctx, msg)
}

// NotifyApprovalRequired sends notification when a PR requires human approval.
func (n *TelegramNotifier) NotifyApprovalRequired(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("%s⏳ *Approval Required*\n\n"+
		"PR #%d is ready for production merge.%s\n%s"+
		"Reply `/approve %d` or `/reject %d`",
		envPrefix(prState), prState.PRNumber, prDetail(prState), approvalReasonDetail(prState),
		prState.PRNumber, prState.PRNumber)

	return n.send(ctx, msg)
}

// NotifyFixIssueCreated sends notification when a fix issue is auto-created.
func (n *TelegramNotifier) NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error {
	msg := fmt.Sprintf("%s🔄 *Fix Issue Created*\n\n"+
		"Issue #%d created to fix failures from PR #%d.\n"+
		"Pilot will pick this up automatically.",
		envPrefix(prState), issueNumber, prState.PRNumber)

	return n.send(ctx, msg)
}

// NotifyPipelineComplete sends notification when the full pipeline completes for a PR.
func (n *TelegramNotifier) NotifyPipelineComplete(ctx context.Context, prState *PRState) error {
	msg := fmt.Sprintf("%s🏁 *Pipeline complete* for GH-%d — PR #%d merged%s",
		envPrefix(prState), prState.IssueNumber, prState.PRNumber, prDetail(prState))

	return n.send(ctx, msg)
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

	// GH-3992: a scope carrier (ScopeTitle set) represents many member PRs
	// batched into one release, not a single source PR — render the scope
	// headline and member count instead of a misleading "From PR: #<anchor>".
	sourceLine := fmt.Sprintf("From PR: #%d", prState.PRNumber)
	if prState.ScopeTitle != "" {
		sourceLine = fmt.Sprintf("Scope: %s (%d PRs)", escapeLegacyMarkdown(prState.ScopeTitle), len(prState.ScopeMemberPRs))
	}

	msg := fmt.Sprintf("%s✨ *Release %s Published*\n\n"+
		"Version: `%s`\n"+
		"Type: %s\n"+
		"%s\n\n"+
		"[View Release](%s)",
		envPrefix(prState),
		escapeLegacyMarkdown(prState.ReleaseVersion),
		escapeCodeSpan(prState.ReleaseVersion),
		bumpLabel,
		sourceLine,
		releaseURL,
	)

	return n.send(ctx, msg)
}
