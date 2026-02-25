package discord

import (
	"context"
	"fmt"
)

// Notifier sends outbound task lifecycle messages to Discord.
type Notifier struct {
	client *Client
}

// NewNotifier creates a new Discord notifier.
func NewNotifier(client *Client) *Notifier {
	return &Notifier{client: client}
}

// NotifyTaskStarted sends a message when a task starts.
func (n *Notifier) NotifyTaskStarted(ctx context.Context, channelID, taskID, description string) error {
	text := fmt.Sprintf("🚀 Starting task %s\n\n%s", taskID, TruncateText(description, 100))
	_, err := n.client.SendMessage(ctx, channelID, text)
	return err
}

// NotifyTaskCompleted sends a message when a task completes successfully.
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, channelID, taskID, output, prURL string) error {
	text := FormatTaskResult(output, true, prURL)
	_, err := n.client.SendMessage(ctx, channelID, text)
	return err
}

// NotifyTaskFailed sends a message when a task fails.
func (n *Notifier) NotifyTaskFailed(ctx context.Context, channelID, taskID, errorMsg string) error {
	text := fmt.Sprintf("❌ Task %s failed\n\n%s", taskID, TruncateText(errorMsg, 500))
	_, err := n.client.SendMessage(ctx, channelID, text)
	return err
}

// NotifyProgress sends a progress update message.
func (n *Notifier) NotifyProgress(ctx context.Context, channelID, messageID, taskID, phase string, progress int, detail string) error {
	text := FormatProgressUpdate(taskID, phase, progress, detail)
	return n.client.EditMessage(ctx, channelID, messageID, text)
}
