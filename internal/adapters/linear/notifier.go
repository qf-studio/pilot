package linear

import (
	"context"
	"fmt"
	"strings"
)

// Notifier handles status updates to Linear issues
type Notifier struct {
	client *Client
}

// NewNotifier creates a new Linear notifier
func NewNotifier(client *Client) *Notifier {
	return &Notifier{
		client: client,
	}
}

// NotifyTaskStarted posts a comment when Pilot starts working on an issue
func (n *Notifier) NotifyTaskStarted(ctx context.Context, issueID, taskID string) error {
	comment := fmt.Sprintf("🤖 **Pilot started working on this issue**\n\nTask ID: `%s`\n\nI'll post updates as I make progress.", taskID)
	if err := n.client.AddComment(ctx, issueID, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}
	return nil
}

// NotifyProgress posts a progress update comment
func (n *Notifier) NotifyProgress(ctx context.Context, issueID, phase, details string) error {
	var emoji string
	switch strings.ToLower(phase) {
	case "exploring", "research":
		emoji = "🔍"
	case "implementing", "impl":
		emoji = "🔨"
	case "testing", "verify":
		emoji = "🧪"
	case "committing":
		emoji = "📝"
	default:
		emoji = "⏳"
	}

	comment := fmt.Sprintf("%s **Phase: %s**\n\n%s", emoji, phase, details)
	if err := n.client.AddComment(ctx, issueID, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}
	return nil
}

// NotifyTaskCompleted posts completion comment with PR URL
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, issueID, prURL, summary string) error {
	var comment strings.Builder
	comment.WriteString("✅ **Pilot completed this task!**\n\n")

	if prURL != "" {
		comment.WriteString(fmt.Sprintf("**Pull Request**: %s\n\n", prURL))
	}

	if summary != "" {
		comment.WriteString("**Summary**:\n")
		comment.WriteString(summary)
		comment.WriteString("\n\n")
	}

	comment.WriteString("_This issue can be closed when the PR is merged._")

	if err := n.client.AddComment(ctx, issueID, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}
	return nil
}

// NotifyTaskFailed posts failure comment
func (n *Notifier) NotifyTaskFailed(ctx context.Context, issueID, reason string) error {
	comment := fmt.Sprintf("❌ **Pilot could not complete this task**\n\n**Reason**: %s\n\n_Please review the issue and consider manual intervention or reopening with more details._", reason)
	if err := n.client.AddComment(ctx, issueID, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}
	return nil
}

// LinkPR adds a comment linking the created PR
func (n *Notifier) LinkPR(ctx context.Context, issueID, prURL string) error {
	comment := fmt.Sprintf("🔗 **Pull Request Created**: %s\n\n_This PR implements the changes for this issue._", prURL)
	if err := n.client.AddComment(ctx, issueID, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}
	return nil
}
