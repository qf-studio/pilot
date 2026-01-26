package jira

import (
	"context"
	"fmt"
	"strings"
)

// Notifier handles status updates to Jira issues
type Notifier struct {
	client      *Client
	transitions struct {
		inProgress string
		done       string
	}
}

// NewNotifier creates a new Jira notifier
func NewNotifier(client *Client, inProgressTransition, doneTransition string) *Notifier {
	return &Notifier{
		client: client,
		transitions: struct {
			inProgress string
			done       string
		}{
			inProgress: inProgressTransition,
			done:       doneTransition,
		},
	}
}

// NotifyTaskStarted posts a comment and transitions to In Progress
func (n *Notifier) NotifyTaskStarted(ctx context.Context, issueKey, taskID string) error {
	// Transition to In Progress if configured
	if n.transitions.inProgress != "" {
		if err := n.client.TransitionIssue(ctx, issueKey, n.transitions.inProgress); err != nil {
			// Log but don't fail - transition might not be available
			fmt.Printf("Warning: failed to transition issue to In Progress: %v\n", err)
		}
	} else {
		// Try to transition by status name
		if err := n.client.TransitionIssueTo(ctx, issueKey, "In Progress"); err != nil {
			fmt.Printf("Warning: failed to transition issue to In Progress: %v\n", err)
		}
	}

	// Post comment
	comment := fmt.Sprintf("🤖 *Pilot started working on this issue*\n\nTask ID: %s\n\nI'll post updates as I make progress.", taskID)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	return nil
}

// NotifyProgress posts a progress update comment
func (n *Notifier) NotifyProgress(ctx context.Context, issueKey, phase, details string) error {
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

	comment := fmt.Sprintf("%s *Phase: %s*\n\n%s", emoji, phase, details)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}

	return nil
}

// NotifyTaskCompleted posts completion comment and transitions to Done
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, issueKey, prURL, summary string) error {
	// Post completion comment
	var comment strings.Builder
	comment.WriteString("✅ *Pilot completed this task!*\n\n")

	if prURL != "" {
		comment.WriteString(fmt.Sprintf("*Pull Request*: %s\n\n", prURL))
	}

	if summary != "" {
		comment.WriteString("*Summary*:\n")
		comment.WriteString(summary)
		comment.WriteString("\n\n")
	}

	comment.WriteString("_This issue will be closed when the PR is merged._")

	if _, err := n.client.AddComment(ctx, issueKey, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	// Transition to Done if configured
	if n.transitions.done != "" {
		if err := n.client.TransitionIssue(ctx, issueKey, n.transitions.done); err != nil {
			fmt.Printf("Warning: failed to transition issue to Done: %v\n", err)
		}
	} else {
		// Try to transition by status name
		if err := n.client.TransitionIssueTo(ctx, issueKey, "Done"); err != nil {
			fmt.Printf("Warning: failed to transition issue to Done: %v\n", err)
		}
	}

	return nil
}

// NotifyTaskFailed posts failure comment
func (n *Notifier) NotifyTaskFailed(ctx context.Context, issueKey, reason string) error {
	comment := fmt.Sprintf("❌ *Pilot could not complete this task*\n\n*Reason*: %s\n\n_Please review the issue and consider manual intervention or reopening with more details._", reason)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	return nil
}

// LinkPR adds a PR link to the issue (as a web link)
func (n *Notifier) LinkPR(ctx context.Context, issueKey string, prNumber int, prURL string) error {
	prTitle := fmt.Sprintf("PR #%d", prNumber)
	if err := n.client.AddPRLink(ctx, issueKey, prURL, prTitle); err != nil {
		return fmt.Errorf("failed to add PR link: %w", err)
	}

	// Also post a comment for visibility
	comment := fmt.Sprintf("🔗 *Pull Request Created*: [PR #%d|%s]\n\n_This PR implements the changes for this issue._", prNumber, prURL)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}

	return nil
}
