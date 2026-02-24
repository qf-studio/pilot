package telegram

import (
	"context"
	"fmt"
	"strconv"

	"github.com/alekspetrov/pilot/internal/comms"
)

// Compile-time check that TelegramMessenger implements comms.Messenger.
var _ comms.Messenger = (*TelegramMessenger)(nil)

// TelegramMessenger implements comms.Messenger by wrapping the Telegram Client.
type TelegramMessenger struct {
	client        *Client
	plainTextMode bool
}

// NewMessenger creates a TelegramMessenger wrapping the given client.
func NewMessenger(client *Client, plainTextMode bool) *TelegramMessenger {
	return &TelegramMessenger{
		client:        client,
		plainTextMode: plainTextMode,
	}
}

// parseMode returns the Telegram parse mode string.
func (m *TelegramMessenger) parseMode() string {
	if m.plainTextMode {
		return ""
	}
	return "Markdown"
}

// SendText sends a plain text message to the given chat.
func (m *TelegramMessenger) SendText(ctx context.Context, contextID, text string) error {
	_, err := m.client.SendMessage(ctx, contextID, text, m.parseMode())
	return err
}

// SendConfirmation sends a task confirmation prompt with approve/reject buttons.
// Returns a string messageRef (the Telegram message ID).
func (m *TelegramMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	text := FormatTaskConfirmation(taskID, desc, project)

	keyboard := [][]InlineKeyboardButton{
		{
			{Text: "✅ Execute", CallbackData: "execute_task:" + taskID},
			{Text: "❌ Cancel", CallbackData: "cancel_task:" + taskID},
		},
	}

	resp, err := m.client.SendMessageWithKeyboard(ctx, contextID, text, m.parseMode(), keyboard)
	if err != nil {
		return "", fmt.Errorf("send confirmation: %w", err)
	}
	if resp == nil || resp.Result == nil {
		return "", fmt.Errorf("send confirmation: nil response")
	}
	return strconv.FormatInt(resp.Result.MessageID, 10), nil
}

// SendProgress updates the existing confirmation message with progress info.
// Returns the same messageRef (Telegram edits in place).
func (m *TelegramMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	text := FormatProgressUpdate(taskID, phase, progress, detail)

	msgID, err := strconv.ParseInt(messageRef, 10, 64)
	if err != nil {
		return messageRef, fmt.Errorf("parse message ref: %w", err)
	}

	if err := m.client.EditMessage(ctx, contextID, msgID, text, m.parseMode()); err != nil {
		return messageRef, fmt.Errorf("send progress: %w", err)
	}
	return messageRef, nil
}

// SendResult sends the final task result.
func (m *TelegramMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	var icon, status string
	if success {
		icon = "✅"
		status = "completed"
	} else {
		icon = "❌"
		status = "failed"
	}

	text := fmt.Sprintf("%s Task %s: %s", icon, status, taskID)
	if output != "" {
		clean := cleanInternalSignals(output)
		if len(clean) > 3000 {
			clean = clean[:3000] + "..."
		}
		text += "\n\n" + clean
	}
	if prURL != "" {
		text += fmt.Sprintf("\n\n🔗 PR: %s", prURL)
	}

	_, err := m.client.SendMessage(ctx, contextID, text, m.parseMode())
	return err
}

// SendChunked sends long content split into platform-appropriate chunks.
func (m *TelegramMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	chunks := chunkContent(content, m.MaxMessageLength())
	for i, chunk := range chunks {
		text := chunk
		if prefix != "" && i == 0 {
			text = prefix + "\n\n" + chunk
		}
		if _, err := m.client.SendMessage(ctx, contextID, text, m.parseMode()); err != nil {
			return fmt.Errorf("send chunk %d: %w", i, err)
		}
	}
	return nil
}

// AcknowledgeCallback responds to a button callback interaction.
func (m *TelegramMessenger) AcknowledgeCallback(ctx context.Context, callbackID string) error {
	return m.client.AnswerCallback(ctx, callbackID, "")
}

// MaxMessageLength returns Telegram's practical max message length.
func (m *TelegramMessenger) MaxMessageLength() int {
	return 4000
}
