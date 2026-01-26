package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	telegramAPIURL = "https://api.telegram.org/bot"
)

// Client is a Telegram Bot API client
type Client struct {
	botToken   string
	httpClient *http.Client
}

// NewClient creates a new Telegram client
func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SendMessageRequest represents a Telegram sendMessage request
type SendMessageRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// SendMessageResponse represents the response from sending a message
type SendMessageResponse struct {
	OK          bool    `json:"ok"`
	Result      *Result `json:"result,omitempty"`
	Description string  `json:"description,omitempty"`
	ErrorCode   int     `json:"error_code,omitempty"`
}

// Result represents the message result
type Result struct {
	MessageID int64 `json:"message_id"`
	ChatID    int64 `json:"chat_id,omitempty"`
}

// SendMessage sends a message to a chat
func (c *Client) SendMessage(ctx context.Context, chatID, text, parseMode string) (*SendMessageResponse, error) {
	req := SendMessageRequest{
		ChatID:    chatID,
		Text:      text,
		ParseMode: parseMode,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	url := telegramAPIURL + c.botToken + "/sendMessage"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result SendMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return &result, nil
}
