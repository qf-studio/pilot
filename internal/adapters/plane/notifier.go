package plane

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/qf-studio/pilot/internal/logging"
)

// Notifier handles status updates to Plane.so work items.
type Notifier struct {
	client        *Client
	workspaceSlug string
}

// NewNotifier creates a new Plane notifier.
func NewNotifier(client *Client, workspaceSlug string) *Notifier {
	return &Notifier{
		client:        client,
		workspaceSlug: workspaceSlug,
	}
}

// NotifyTaskStarted posts a comment when Pilot starts working on a work item.
func (n *Notifier) NotifyTaskStarted(ctx context.Context, projectID, workItemID, taskID string) error {
	comment := fmt.Sprintf("<p>🤖 <strong>Pilot started working on this issue</strong></p><p>Task ID: <code>%s</code></p><p>I'll post updates as I make progress.</p>", taskID)
	if err := n.client.AddComment(ctx, n.workspaceSlug, projectID, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	logging.WithComponent("plane").Info("Notified task started",
		slog.String("work_item_id", workItemID),
		slog.String("task_id", taskID))

	return nil
}

// NotifyTaskCompleted posts a completion comment.
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, projectID, workItemID, prURL, summary string) error {
	comment := "<p>✅ <strong>Pilot completed this task!</strong></p>"
	if prURL != "" {
		comment += fmt.Sprintf("<p><strong>Pull Request</strong>: <a href=\"%s\">%s</a></p>", prURL, prURL)
	}
	if summary != "" {
		comment += fmt.Sprintf("<p><strong>Summary</strong>: %s</p>", summary)
	}

	if err := n.client.AddComment(ctx, n.workspaceSlug, projectID, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	logging.WithComponent("plane").Info("Notified task completed",
		slog.String("work_item_id", workItemID),
		slog.String("pr_url", prURL))

	return nil
}

// NotifyTaskFailed posts a failure comment.
func (n *Notifier) NotifyTaskFailed(ctx context.Context, projectID, workItemID, reason string) error {
	comment := fmt.Sprintf("<p>❌ <strong>Pilot could not complete this task</strong></p><p><strong>Reason</strong>: %s</p><p><em>Please review the issue and consider manual intervention or reopening with more details.</em></p>", reason)
	if err := n.client.AddComment(ctx, n.workspaceSlug, projectID, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	logging.WithComponent("plane").Warn("Notified task failed",
		slog.String("work_item_id", workItemID),
		slog.String("reason", reason))

	return nil
}

// LinkPR posts a comment linking the created PR.
func (n *Notifier) LinkPR(ctx context.Context, projectID, workItemID string, prNumber int, prURL string) error {
	comment := fmt.Sprintf("<p>🔗 <strong>Pull Request Created</strong>: <a href=\"%s\">PR #%d</a></p><p><em>This PR implements the changes for this issue.</em></p>", prURL, prNumber)
	if err := n.client.AddComment(ctx, n.workspaceSlug, projectID, workItemID, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}

	logging.WithComponent("plane").Info("Linked PR to work item",
		slog.String("work_item_id", workItemID),
		slog.Int("pr_number", prNumber))

	return nil
}
