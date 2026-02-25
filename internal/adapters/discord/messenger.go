package discord

import (
	"context"
	"fmt"

	"github.com/alekspetrov/pilot/internal/comms"
)

// Compile-time check that DiscordMessenger implements comms.Messenger.
var _ comms.Messenger = (*DiscordMessenger)(nil)

// DiscordMessenger implements comms.Messenger by wrapping the Discord Client.
type DiscordMessenger struct {
	client *Client
}

// NewMessenger creates a DiscordMessenger wrapping the given client.
func NewMessenger(client *Client) *DiscordMessenger {
	return &DiscordMessenger{client: client}
}

// SendText sends a plain text message to the given channel.
func (m *DiscordMessenger) SendText(ctx context.Context, contextID, text string) error {
	msg, err := m.client.SendMessage(ctx, contextID, text)
	if err != nil {
		return fmt.Errorf("send text: %w", err)
	}
	if msg == nil {
		return fmt.Errorf("send text: no message ID returned")
	}
	return nil
}

// SendConfirmation sends a task confirmation prompt with execute/cancel buttons.
// Returns the message ID as messageRef.
func (m *DiscordMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	text := FormatTaskConfirmation(taskID, desc, project)
	buttons := BuildConfirmationButtons()

	msg, err := m.client.SendMessageWithComponents(ctx, contextID, text, buttons)
	if err != nil {
		return "", fmt.Errorf("send confirmation: %w", err)
	}

	if msg == nil || msg.ID == "" {
		return "", fmt.Errorf("send confirmation: no message ID returned")
	}

	return msg.ID, nil
}

// SendProgress updates an existing message with progress info.
// Returns the same messageRef (Discord updates in place via message ID).
func (m *DiscordMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	text := FormatProgressUpdate(taskID, phase, progress, detail)

	if err := m.client.EditMessage(ctx, contextID, messageRef, text); err != nil {
		return messageRef, fmt.Errorf("send progress: %w", err)
	}

	return messageRef, nil
}

// SendResult sends the final task result.
func (m *DiscordMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	text := FormatTaskResult(output, success, prURL)

	_, err := m.client.SendMessage(ctx, contextID, text)
	if err != nil {
		return fmt.Errorf("send result: %w", err)
	}

	return nil
}

// SendChunked sends long content split into Discord-appropriate chunks.
func (m *DiscordMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	chunks := ChunkContent(content, m.MaxMessageLength())

	for i, chunk := range chunks {
		text := chunk
		if prefix != "" && i == 0 {
			text = prefix + "\n\n" + chunk
		}

		if _, err := m.client.SendMessage(ctx, contextID, text); err != nil {
			return fmt.Errorf("send chunk %d: %w", i, err)
		}
	}

	return nil
}

// AcknowledgeCallback responds to a button interaction via deferred response.
func (m *DiscordMessenger) AcknowledgeCallback(ctx context.Context, callbackID string) error {
	// For Discord, acknowledgment is handled in handler via CreateInteractionResponse
	// This is a no-op for deferred responses
	return nil
}

// MaxMessageLength returns Discord's maximum message length.
func (m *DiscordMessenger) MaxMessageLength() int {
	return MaxMessageLength
}
