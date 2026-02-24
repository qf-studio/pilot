package telegram

import (
	"context"
	"fmt"
	"strconv"
)

// TelegramMessenger implements comms.Messenger interface for Telegram
type TelegramMessenger struct {
	client        *Client
	plainTextMode bool
}

// NewTelegramMessenger creates a new Telegram messenger
func NewTelegramMessenger(client *Client, plainTextMode bool) *TelegramMessenger {
	return &TelegramMessenger{
		client:        client,
		plainTextMode: plainTextMode,
	}
}

// getParseMode returns the appropriate parse mode based on plainTextMode flag
func (tm *TelegramMessenger) getParseMode() string {
	if tm.plainTextMode {
		return ""
	}
	return "MarkdownV2"
}

// SendText sends a plain text message to a chat
func (tm *TelegramMessenger) SendText(ctx context.Context, contextID, text string) error {
	_, err := tm.client.SendMessage(ctx, contextID, text, tm.getParseMode())
	if err != nil {
		return fmt.Errorf("failed to send text message: %w", err)
	}
	return nil
}

// SendConfirmation sends a confirmation message with execute/cancel buttons
func (tm *TelegramMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	text := FormatTaskConfirmation(taskID, desc, project)

	keyboard := [][]InlineKeyboardButton{
		{
			InlineKeyboardButton{
				Text:         "✅ Execute",
				CallbackData: "execute:" + taskID,
			},
			InlineKeyboardButton{
				Text:         "❌ Cancel",
				CallbackData: "cancel:" + taskID,
			},
		},
	}

	resp, err := tm.client.SendMessageWithKeyboard(ctx, contextID, text, tm.getParseMode(), keyboard)
	if err != nil {
		return "", fmt.Errorf("failed to send confirmation message: %w", err)
	}

	if resp == nil || resp.Result == nil {
		return "", fmt.Errorf("empty response from send message with keyboard")
	}

	return strconv.FormatInt(resp.Result.MessageID, 10), nil
}

// SendProgress updates an existing message or sends a new one with progress info
func (tm *TelegramMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	text := FormatProgressUpdate(taskID, phase, progress, detail)

	// If messageRef exists, try to edit the message
	if messageRef != "" {
		messageID, err := strconv.ParseInt(messageRef, 10, 64)
		if err == nil {
			err = tm.client.EditMessage(ctx, contextID, messageID, text, tm.getParseMode())
			if err == nil {
				return messageRef, nil
			}
		}
		// Fall through to send new message if parse or edit failed
	}

	// Send a new message
	resp, err := tm.client.SendMessage(ctx, contextID, text, tm.getParseMode())
	if err != nil {
		return "", fmt.Errorf("failed to send progress message: %w", err)
	}

	if resp == nil || resp.Result == nil {
		return "", fmt.Errorf("empty response from send message")
	}

	return strconv.FormatInt(resp.Result.MessageID, 10), nil
}

// SendResult sends a task result message
func (tm *TelegramMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	text := fmt.Sprintf("Task %s: ", taskID)
	if success {
		text += "✅ Success"
	} else {
		text += "❌ Failed"
	}
	if output != "" {
		text += "\n" + output
	}
	if prURL != "" {
		text += "\nPR: " + prURL
	}

	_, err := tm.client.SendMessage(ctx, contextID, text, tm.getParseMode())
	if err != nil {
		return fmt.Errorf("failed to send result message: %w", err)
	}
	return nil
}

// SendChunked splits large messages and sends them sequentially
func (tm *TelegramMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	maxLen := tm.MaxMessageLength()
	if prefix != "" {
		content = prefix + "\n" + content
	}
	chunks := chunkContent(content, maxLen)

	for _, chunk := range chunks {
		_, err := tm.client.SendMessage(ctx, contextID, chunk, tm.getParseMode())
		if err != nil {
			return fmt.Errorf("failed to send chunked message: %w", err)
		}
	}
	return nil
}

// AcknowledgeCallback acknowledges a callback query (e.g., button press)
func (tm *TelegramMessenger) AcknowledgeCallback(ctx context.Context, callbackID string) error {
	err := tm.client.AnswerCallback(ctx, callbackID, "")
	if err != nil {
		return fmt.Errorf("failed to acknowledge callback: %w", err)
	}
	return nil
}

// MaxMessageLength returns the maximum message length for Telegram
func (tm *TelegramMessenger) MaxMessageLength() int {
	return 4000
}
