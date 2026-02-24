package slack

import (
	"context"
	"fmt"

	"github.com/alekspetrov/pilot/internal/comms"
)

// SlackMessenger implements comms.Messenger for Slack.
type SlackMessenger struct {
	client *Client
}

// NewSlackMessenger creates a new SlackMessenger.
func NewSlackMessenger(client *Client) *SlackMessenger {
	return &SlackMessenger{
		client: client,
	}
}

// SendText sends a plain text message to the given context (channel).
func (s *SlackMessenger) SendText(ctx context.Context, contextID, text string) error {
	msg := &Message{
		Channel: contextID,
		Text:    text,
	}
	_, err := s.client.PostMessage(ctx, msg)
	return err
}

// SendConfirmation sends a task confirmation prompt with approve/reject buttons.
// Returns a messageRef (Slack timestamp) that can be used to update the message later.
func (s *SlackMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (messageRef string, err error) {
	blocks := BuildConfirmationBlocks(taskID, desc)
	msg := &InteractiveMessage{
		Channel: contextID,
		Text:    fmt.Sprintf("📋 Task: %s", taskID),
		Blocks:  blocks,
	}

	if threadID != "" {
		// For thread support, we need to add threadTS to blocks if possible
		// But InteractiveMessage doesn't have ThreadTS field directly
		// We'll handle threading in SendProgress if needed
	}

	resp, err := s.client.PostInteractiveMessage(ctx, msg)
	if err != nil {
		return "", err
	}

	return resp.TS, nil
}

// SendProgress updates an existing message (identified by messageRef) with progress info.
// Returns a new messageRef if the platform creates a new message.
func (s *SlackMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (newRef string, err error) {
	blocks := BuildProgressBlocks(taskID, phase, progress, detail)
	err = s.client.UpdateInteractiveMessage(ctx, contextID, messageRef, blocks, fmt.Sprintf("⚙️ %s", taskID))
	if err != nil {
		return "", err
	}

	// Slack updates return the same message ref
	return messageRef, nil
}

// SendResult sends the final task result (success or failure).
func (s *SlackMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	blocks := BuildResultBlocks(taskID, success, output, prURL)
	msg := &InteractiveMessage{
		Channel: contextID,
		Text:    fmt.Sprintf("Task %s %s", taskID, map[bool]string{true: "completed", false: "failed"}[success]),
		Blocks:  blocks,
	}

	_, err := s.client.PostInteractiveMessage(ctx, msg)
	return err
}

// SendChunked sends long content split into platform-appropriate chunks.
func (s *SlackMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	chunks := ChunkContent(content, s.MaxMessageLength())

	for i, chunk := range chunks {
		var text string
		if prefix != "" {
			if i == 0 {
				text = fmt.Sprintf("%s\n%s", prefix, chunk)
			} else {
				text = fmt.Sprintf("%s (part %d)\n%s", prefix, i+1, chunk)
			}
		} else {
			if i > 0 {
				text = fmt.Sprintf("(continued part %d)\n%s", i+1, chunk)
			} else {
				text = chunk
			}
		}

		msg := &Message{
			Channel:  contextID,
			Text:     text,
			ThreadTS: threadID,
		}

		_, err := s.client.PostMessage(ctx, msg)
		if err != nil {
			return err
		}
	}

	return nil
}

// AcknowledgeCallback responds to a button/callback interaction.
// Slack handles acknowledgment via HTTP response, so this is a no-op.
func (s *SlackMessenger) AcknowledgeCallback(ctx context.Context, callbackID string) error {
	return nil
}

// MaxMessageLength returns the platform's maximum single-message length.
func (s *SlackMessenger) MaxMessageLength() int {
	return 3800
}

// Verify SlackMessenger implements comms.Messenger
var _ comms.Messenger = (*SlackMessenger)(nil)
