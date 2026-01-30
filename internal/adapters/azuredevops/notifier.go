package azuredevops

import (
	"context"
	"fmt"
	"strings"
)

// Notifier handles status updates to Azure DevOps work items
type Notifier struct {
	client   *Client
	pilotTag string
}

// NewNotifier creates a new Azure DevOps notifier
func NewNotifier(client *Client, pilotTag string) *Notifier {
	return &Notifier{
		client:   client,
		pilotTag: pilotTag,
	}
}

// NotifyTaskStarted posts a comment and adds in-progress tag
func (n *Notifier) NotifyTaskStarted(ctx context.Context, workItemID int, taskID string) error {
	// Add in-progress tag
	if err := n.client.AddWorkItemTag(ctx, workItemID, TagInProgress); err != nil {
		return fmt.Errorf("failed to add in-progress tag: %w", err)
	}

	// Post comment
	comment := fmt.Sprintf("🤖 **Pilot started working on this work item**\n\nTask ID: `%s`\n\nI'll post updates as I make progress.", taskID)
	if _, err := n.client.AddWorkItemComment(ctx, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	return nil
}

// NotifyProgress posts a progress update comment
func (n *Notifier) NotifyProgress(ctx context.Context, workItemID int, phase string, details string) error {
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
	if _, err := n.client.AddWorkItemComment(ctx, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}

	return nil
}

// NotifyTaskCompleted posts completion comment and updates tags
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, workItemID int, prURL string, summary string) error {
	// Remove in-progress tag (best-effort, non-critical)
	// Tag may not exist if task started before tagging was added
	_ = n.client.RemoveWorkItemTag(ctx, workItemID, TagInProgress)

	// Remove pilot trigger tag (best-effort, non-critical)
	_ = n.client.RemoveWorkItemTag(ctx, workItemID, n.pilotTag)

	// Add done tag
	if err := n.client.AddWorkItemTag(ctx, workItemID, TagDone); err != nil {
		return fmt.Errorf("failed to add done tag: %w", err)
	}

	// Post completion comment
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

	comment.WriteString("_This work item will be resolved when the PR is merged._")

	if _, err := n.client.AddWorkItemComment(ctx, workItemID, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	return nil
}

// NotifyTaskFailed posts failure comment and updates tags
func (n *Notifier) NotifyTaskFailed(ctx context.Context, workItemID int, reason string) error {
	// Remove in-progress tag (best-effort, non-critical)
	_ = n.client.RemoveWorkItemTag(ctx, workItemID, TagInProgress)

	// Add failed tag
	if err := n.client.AddWorkItemTag(ctx, workItemID, TagFailed); err != nil {
		return fmt.Errorf("failed to add failed tag: %w", err)
	}

	// Post failure comment
	comment := fmt.Sprintf("❌ **Pilot could not complete this task**\n\n**Reason**: %s\n\n_Please review the work item and consider manual intervention or reopening with more details._", reason)
	if _, err := n.client.AddWorkItemComment(ctx, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	return nil
}

// LinkPR adds a comment linking the created PR
func (n *Notifier) LinkPR(ctx context.Context, workItemID int, prID int, prURL string) error {
	comment := fmt.Sprintf("🔗 **Pull Request Created**: #%d\n\n%s\n\n_This PR implements the changes for this work item._", prID, prURL)
	if _, err := n.client.AddWorkItemComment(ctx, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}

	return nil
}
