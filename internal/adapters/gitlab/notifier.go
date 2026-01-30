package gitlab

import (
	"context"
	"fmt"
	"strings"
)

// Notifier handles status updates to GitLab issues
type Notifier struct {
	client     *Client
	pilotLabel string
}

// NewNotifier creates a new GitLab notifier
func NewNotifier(client *Client, pilotLabel string) *Notifier {
	return &Notifier{
		client:     client,
		pilotLabel: pilotLabel,
	}
}

// NotifyTaskStarted posts a comment and adds in-progress label
func (n *Notifier) NotifyTaskStarted(ctx context.Context, issueIID int, taskID string) error {
	// Add in-progress label
	if err := n.client.AddIssueLabels(ctx, issueIID, []string{LabelInProgress}); err != nil {
		return fmt.Errorf("failed to add in-progress label: %w", err)
	}

	// Post comment
	comment := fmt.Sprintf("🤖 **Pilot started working on this issue**\n\nTask ID: `%s`\n\nI'll post updates as I make progress.", taskID)
	if _, err := n.client.AddIssueNote(ctx, issueIID, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	return nil
}

// NotifyProgress posts a progress update comment
func (n *Notifier) NotifyProgress(ctx context.Context, issueIID int, phase string, details string) error {
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
	if _, err := n.client.AddIssueNote(ctx, issueIID, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}

	return nil
}

// NotifyTaskCompleted posts completion comment and updates labels
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, issueIID int, mrURL string, summary string) error {
	// Remove in-progress label (best-effort, non-critical)
	// Label may not exist if task started before labeling was added
	_ = n.client.RemoveIssueLabel(ctx, issueIID, LabelInProgress)

	// Remove pilot trigger label (best-effort, non-critical)
	_ = n.client.RemoveIssueLabel(ctx, issueIID, n.pilotLabel)

	// Add done label
	if err := n.client.AddIssueLabels(ctx, issueIID, []string{LabelDone}); err != nil {
		return fmt.Errorf("failed to add done label: %w", err)
	}

	// Post completion comment
	var comment strings.Builder
	comment.WriteString("✅ **Pilot completed this task!**\n\n")

	if mrURL != "" {
		comment.WriteString(fmt.Sprintf("**Merge Request**: %s\n\n", mrURL))
	}

	if summary != "" {
		comment.WriteString("**Summary**:\n")
		comment.WriteString(summary)
		comment.WriteString("\n\n")
	}

	comment.WriteString("_This issue will be closed when the MR is merged._")

	if _, err := n.client.AddIssueNote(ctx, issueIID, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	return nil
}

// NotifyTaskFailed posts failure comment and updates labels
func (n *Notifier) NotifyTaskFailed(ctx context.Context, issueIID int, reason string) error {
	// Remove in-progress label (best-effort, non-critical)
	_ = n.client.RemoveIssueLabel(ctx, issueIID, LabelInProgress)

	// Add failed label
	if err := n.client.AddIssueLabels(ctx, issueIID, []string{LabelFailed}); err != nil {
		return fmt.Errorf("failed to add failed label: %w", err)
	}

	// Post failure comment
	comment := fmt.Sprintf("❌ **Pilot could not complete this task**\n\n**Reason**: %s\n\n_Please review the issue and consider manual intervention or reopening with more details._", reason)
	if _, err := n.client.AddIssueNote(ctx, issueIID, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	return nil
}

// LinkMR adds a comment linking the created MR
func (n *Notifier) LinkMR(ctx context.Context, issueIID int, mrIID int, mrURL string) error {
	comment := fmt.Sprintf("🔗 **Merge Request Created**: !%d\n\n%s\n\n_This MR implements the changes for this issue._", mrIID, mrURL)
	if _, err := n.client.AddIssueNote(ctx, issueIID, comment); err != nil {
		return fmt.Errorf("failed to add MR link comment: %w", err)
	}

	return nil
}
