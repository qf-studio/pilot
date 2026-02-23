package asana

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alekspetrov/pilot/internal/logging"
)

// Notifier handles status updates to Asana tasks
type Notifier struct {
	client      *Client
	pilotTag    string
	pilotTagGID string // Cached GID for the pilot tag
}

// NewNotifier creates a new Asana notifier
func NewNotifier(client *Client, pilotTag string) *Notifier {
	return &Notifier{
		client:   client,
		pilotTag: pilotTag,
	}
}

// NotifyTaskStarted posts a comment indicating Pilot started work
func (n *Notifier) NotifyTaskStarted(ctx context.Context, taskGID, pilotTaskID string) error {
	comment := fmt.Sprintf("🤖 **Pilot started working on this task**\n\nTask ID: `%s`\n\nI'll post updates as I make progress.", pilotTaskID)

	if _, err := n.client.AddComment(ctx, taskGID, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	logging.WithComponent("asana").Info("Notified task started",
		slog.String("task_gid", taskGID),
		slog.String("pilot_task_id", pilotTaskID))

	return nil
}

// NotifyProgress posts a progress update comment
func (n *Notifier) NotifyProgress(ctx context.Context, taskGID, phase, details string) error {
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
	case "reviewing":
		emoji = "👀"
	default:
		emoji = "⏳"
	}

	comment := fmt.Sprintf("%s **Phase: %s**\n\n%s", emoji, phase, details)

	if _, err := n.client.AddComment(ctx, taskGID, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}

	logging.WithComponent("asana").Debug("Posted progress update",
		slog.String("task_gid", taskGID),
		slog.String("phase", phase))

	return nil
}

// NotifyTaskCompleted posts completion comment and marks task as complete
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, taskGID, prURL, summary string) error {
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

	if _, err := n.client.AddComment(ctx, taskGID, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	// Mark the Asana task as complete; log warning on failure but don't block the flow
	if _, err := n.client.CompleteTask(ctx, taskGID); err != nil {
		logging.WithComponent("asana").Warn("Failed to mark task complete in Asana",
			slog.String("task_gid", taskGID),
			slog.Any("error", err))
	} else {
		logging.WithComponent("asana").Info("Marked task as complete",
			slog.String("task_gid", taskGID))
	}

	logging.WithComponent("asana").Info("Notified task completed",
		slog.String("task_gid", taskGID),
		slog.String("pr_url", prURL))

	return nil
}

// CompleteTask marks the task as completed in Asana
func (n *Notifier) CompleteTask(ctx context.Context, taskGID string) error {
	if _, err := n.client.CompleteTask(ctx, taskGID); err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}

	logging.WithComponent("asana").Info("Marked task as complete",
		slog.String("task_gid", taskGID))

	return nil
}

// NotifyTaskFailed posts failure comment
func (n *Notifier) NotifyTaskFailed(ctx context.Context, taskGID, reason string) error {
	comment := fmt.Sprintf("❌ **Pilot could not complete this task**\n\n**Reason**: %s\n\n_Please review the task and consider manual intervention or reopening with more details._", reason)

	if _, err := n.client.AddComment(ctx, taskGID, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	logging.WithComponent("asana").Warn("Notified task failed",
		slog.String("task_gid", taskGID),
		slog.String("reason", reason))

	return nil
}

// LinkPR adds a PR link as an attachment to the task
func (n *Notifier) LinkPR(ctx context.Context, taskGID string, prNumber int, prURL string) error {
	prName := fmt.Sprintf("Pull Request #%d", prNumber)

	if _, err := n.client.AddAttachment(ctx, taskGID, prURL, prName); err != nil {
		logging.WithComponent("asana").Warn("Failed to add PR attachment, posting comment instead",
			slog.String("task_gid", taskGID),
			slog.Any("error", err))
		// Fall back to comment
	}

	// Also post a comment for visibility
	comment := fmt.Sprintf("🔗 **Pull Request Created**: [PR #%d](%s)\n\n_This PR implements the changes for this task._", prNumber, prURL)

	if _, err := n.client.AddComment(ctx, taskGID, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}

	logging.WithComponent("asana").Info("Linked PR to task",
		slog.String("task_gid", taskGID),
		slog.Int("pr_number", prNumber))

	return nil
}

// RemovePilotTag removes the pilot tag from a task (after completion)
func (n *Notifier) RemovePilotTag(ctx context.Context, taskGID string) error {
	// Find pilot tag GID if not cached
	if n.pilotTagGID == "" {
		tag, err := n.client.FindTagByName(ctx, n.pilotTag)
		if err != nil {
			return fmt.Errorf("failed to find pilot tag: %w", err)
		}
		if tag == nil {
			// Tag doesn't exist, nothing to remove
			return nil
		}
		n.pilotTagGID = tag.GID
	}

	if err := n.client.RemoveTag(ctx, taskGID, n.pilotTagGID); err != nil {
		// Log but don't fail - tag might already be removed
		logging.WithComponent("asana").Warn("Failed to remove pilot tag",
			slog.String("task_gid", taskGID),
			slog.Any("error", err))
	}

	return nil
}

// AddPilotTag adds the pilot tag to a task
func (n *Notifier) AddPilotTag(ctx context.Context, taskGID string) error {
	// Find or create pilot tag
	if n.pilotTagGID == "" {
		tag, err := n.client.FindTagByName(ctx, n.pilotTag)
		if err != nil {
			return fmt.Errorf("failed to find pilot tag: %w", err)
		}
		if tag == nil {
			// Create the tag
			newTag, err := n.client.CreateTag(ctx, n.pilotTag)
			if err != nil {
				return fmt.Errorf("failed to create pilot tag: %w", err)
			}
			n.pilotTagGID = newTag.GID
		} else {
			n.pilotTagGID = tag.GID
		}
	}

	if err := n.client.AddTag(ctx, taskGID, n.pilotTagGID); err != nil {
		return fmt.Errorf("failed to add pilot tag: %w", err)
	}

	return nil
}
