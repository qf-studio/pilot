package slack

import (
	"context"
	"fmt"

	"github.com/alekspetrov/pilot/internal/comms"
)

// Compile-time check that SlackMessenger implements comms.Messenger.
var _ comms.Messenger = (*SlackMessenger)(nil)

// SlackMessenger implements comms.Messenger by wrapping the Slack Client.
type SlackMessenger struct {
	client *Client
}

// NewMessenger creates a SlackMessenger wrapping the given client.
func NewMessenger(client *Client) *SlackMessenger {
	return &SlackMessenger{client: client}
}

// SendText sends a plain text message to the given channel.
func (m *SlackMessenger) SendText(ctx context.Context, contextID, text string) error {
	msg := &Message{
		Channel: contextID,
		Text:    text,
	}
	_, err := m.client.PostMessage(ctx, msg)
	return err
}

// SendConfirmation sends a task confirmation prompt with approve/reject buttons.
// Returns the Slack message timestamp as messageRef.
func (m *SlackMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	blocks := BuildConfirmationBlocks(taskID, desc)

	msg := &InteractiveMessage{
		Channel: contextID,
		Text:    FormatTaskConfirmation(taskID, desc, project),
		Blocks:  blocks,
	}

	resp, err := m.client.PostInteractiveMessage(ctx, msg)
	if err != nil {
		return "", fmt.Errorf("send confirmation: %w", err)
	}
	return resp.TS, nil
}

// SendProgress updates the existing message with progress info.
// Returns the same messageRef (Slack updates in place via timestamp).
func (m *SlackMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	blocks := BuildProgressBlocks(taskID, phase, progress, detail)

	err := m.client.UpdateInteractiveMessage(ctx, contextID, messageRef, blocks, FormatProgressUpdate(taskID, phase, progress, detail))
	if err != nil {
		return messageRef, fmt.Errorf("send progress: %w", err)
	}
	return messageRef, nil
}

// SendResult sends the final task result.
func (m *SlackMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	blocks := BuildResultBlocks(taskID, success, output, prURL)

	msg := &InteractiveMessage{
		Channel: contextID,
		Text:    FormatTaskResult(output, success, prURL),
		Blocks:  blocks,
	}

	_, err := m.client.PostInteractiveMessage(ctx, msg)
	return err
}

// SendChunked sends long content split into platform-appropriate chunks.
func (m *SlackMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	chunks := ChunkContent(content, m.MaxMessageLength())
	for i, chunk := range chunks {
		text := chunk
		if prefix != "" && i == 0 {
			text = prefix + "\n\n" + chunk
		}
		msg := &Message{
			Channel:  contextID,
			Text:     text,
			ThreadTS: threadID,
		}
		if _, err := m.client.PostMessage(ctx, msg); err != nil {
			return fmt.Errorf("send chunk %d: %w", i, err)
		}
	}
	return nil
}

// AcknowledgeCallback is a no-op for Slack (callbacks are acknowledged via HTTP response).
func (m *SlackMessenger) AcknowledgeCallback(_ context.Context, _ string) error {
	return nil
}

// MaxMessageLength returns Slack's practical max message length.
func (m *SlackMessenger) MaxMessageLength() int {
	return 3800
}
