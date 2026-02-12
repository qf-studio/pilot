package github

import (
	"context"
	"fmt"
	"strings"
)

// Notifier handles status updates to GitHub issues
type Notifier struct {
	client     *Client
	pilotLabel string
}

// NewNotifier creates a new GitHub notifier
func NewNotifier(client *Client, pilotLabel string) *Notifier {
	return &Notifier{
		client:     client,
		pilotLabel: pilotLabel,
	}
}

// NotifyTaskStarted posts a comment and adds in-progress label
func (n *Notifier) NotifyTaskStarted(ctx context.Context, owner, repo string, issueNum int, taskID string) error {
	// Add in-progress label
	if err := n.client.AddLabels(ctx, owner, repo, issueNum, []string{LabelInProgress}); err != nil {
		return fmt.Errorf("failed to add in-progress label: %w", err)
	}

	// Post comment
	comment := fmt.Sprintf("🤖 **Pilot started working on this issue**\n\nTask ID: `%s`\n\nI'll post updates as I make progress.", taskID)
	if _, err := n.client.AddComment(ctx, owner, repo, issueNum, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	return nil
}

// NotifyProgress posts a progress update comment
func (n *Notifier) NotifyProgress(ctx context.Context, owner, repo string, issueNum int, phase string, details string) error {
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
	if _, err := n.client.AddComment(ctx, owner, repo, issueNum, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}

	return nil
}

// NotifyTaskCompleted posts completion comment and updates labels
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, owner, repo string, issueNum int, prURL string, summary string) error {
	// Remove in-progress label (best-effort, non-critical)
	// Label may not exist if task started before labeling was added
	if err := n.client.RemoveLabel(ctx, owner, repo, issueNum, LabelInProgress); err != nil {
		// Log but don't fail - label removal is non-critical
		_ = err // intentionally ignored: label may not exist
	}

	// Remove pilot trigger label (best-effort, non-critical)
	if err := n.client.RemoveLabel(ctx, owner, repo, issueNum, n.pilotLabel); err != nil {
		_ = err // intentionally ignored: label may not exist
	}

	// Add done label
	if err := n.client.AddLabels(ctx, owner, repo, issueNum, []string{LabelDone}); err != nil {
		return fmt.Errorf("failed to add done label: %w", err)
	}

	// Close the issue so dependent issues can proceed
	// (dependency resolution checks issue.State, not labels)
	if err := n.client.UpdateIssueState(ctx, owner, repo, issueNum, "closed"); err != nil {
		return fmt.Errorf("failed to close issue: %w", err)
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

	comment.WriteString("_Issue closed. PR awaiting review._")

	if _, err := n.client.AddComment(ctx, owner, repo, issueNum, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	return nil
}

// NotifyTaskFailed posts failure comment and updates labels
func (n *Notifier) NotifyTaskFailed(ctx context.Context, owner, repo string, issueNum int, reason string) error {
	// Remove in-progress label (best-effort, non-critical)
	if err := n.client.RemoveLabel(ctx, owner, repo, issueNum, LabelInProgress); err != nil {
		_ = err // intentionally ignored: label may not exist
	}

	// Add failed label
	if err := n.client.AddLabels(ctx, owner, repo, issueNum, []string{LabelFailed}); err != nil {
		return fmt.Errorf("failed to add failed label: %w", err)
	}

	// Post failure comment
	comment := fmt.Sprintf("❌ **Pilot could not complete this task**\n\n**Reason**: %s\n\n_Please review the issue and consider manual intervention or reopening with more details._", reason)
	if _, err := n.client.AddComment(ctx, owner, repo, issueNum, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	return nil
}

// LinkPR adds a comment linking the created PR
func (n *Notifier) LinkPR(ctx context.Context, owner, repo string, issueNum int, prNumber int, prURL string) error {
	comment := fmt.Sprintf("🔗 **Pull Request Created**: #%d\n\n%s\n\n_This PR implements the changes for this issue._", prNumber, prURL)
	if _, err := n.client.AddComment(ctx, owner, repo, issueNum, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}

	return nil
}
