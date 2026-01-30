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
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Telegram client
func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  telegramAPIURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second, // Must be > long polling timeout (30s)
		},
	}
}

// NewClientWithBaseURL creates a new Telegram client with a custom base URL (for testing)
func NewClientWithBaseURL(botToken, baseURL string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  baseURL + "/bot",
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// SendMessageRequest represents a Telegram sendMessage request
type SendMessageRequest struct {
	ChatID      string                `json:"chat_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
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

// Update represents a Telegram update from getUpdates
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// CallbackQuery represents a callback query from inline keyboard
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// InlineKeyboardMarkup represents an inline keyboard
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton represents a button in an inline keyboard
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// Message represents a Telegram message
type Message struct {
	MessageID int64        `json:"message_id"`
	From      *User        `json:"from,omitempty"`
	Chat      *Chat        `json:"chat"`
	Date      int64        `json:"date"`
	Text      string       `json:"text,omitempty"`
	Photo     []*PhotoSize `json:"photo,omitempty"`
	Voice     *Voice       `json:"voice,omitempty"`
	Caption   string       `json:"caption,omitempty"`
}

// Voice represents a voice message
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// PhotoSize represents one size of a photo or file thumbnail
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
}

// File represents a file ready to be downloaded
type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path,omitempty"`
	FileSize int    `json:"file_size,omitempty"`
}

// GetFileResponse represents the response from getFile API
type GetFileResponse struct {
	OK          bool   `json:"ok"`
	Result      *File  `json:"result,omitempty"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

// User represents a Telegram user
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat represents a Telegram chat
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// GetUpdatesResponse represents the response from getUpdates
type GetUpdatesResponse struct {
	OK          bool      `json:"ok"`
	Result      []*Update `json:"result,omitempty"`
	Description string    `json:"description,omitempty"`
	ErrorCode   int       `json:"error_code,omitempty"`
}

// ErrConflict is returned when another bot instance is already running
var ErrConflict = fmt.Errorf("another bot instance is running")

// CheckSingleton verifies no other bot instance is running by making a quick API call.
// Returns ErrConflict if another instance is detected (409 error).
func (c *Client) CheckSingleton(ctx context.Context) error {
	// Use timeout=0 for immediate response (no long polling)
	url := fmt.Sprintf("%s%s/getUpdates?timeout=0&limit=1", c.baseURL, c.botToken)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to check singleton: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result GetUpdatesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		// 409 = Conflict: another getUpdates is running
		if result.ErrorCode == 409 {
			return ErrConflict
		}
		return fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return nil
}

// GetUpdates retrieves updates using long polling
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]*Update, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=%d", c.baseURL, c.botToken, offset, timeout)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get updates: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result GetUpdatesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return result.Result, nil
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

	url := c.baseURL + c.botToken + "/sendMessage"
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

// SendMessageWithKeyboard sends a message with an inline keyboard
func (c *Client) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]InlineKeyboardButton) (*SendMessageResponse, error) {
	req := SendMessageRequest{
		ChatID:    chatID,
		Text:      text,
		ParseMode: parseMode,
		ReplyMarkup: &InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	url := c.baseURL + c.botToken + "/sendMessage"
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

// EditMessage edits an existing message's text
func (c *Client) EditMessage(ctx context.Context, chatID string, messageID int64, text, parseMode string) error {
	type editRequest struct {
		ChatID    string `json:"chat_id"`
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}

	req := editRequest{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
		ParseMode: parseMode,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + c.botToken + "/editMessageText"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to edit message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result SendMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return nil
}

// AnswerCallback answers a callback query
func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	type answerRequest struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}

	req := answerRequest{
		CallbackQueryID: callbackID,
		Text:            text,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + c.botToken + "/answerCallbackQuery"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to answer callback: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// GetFile retrieves file info from Telegram servers
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	url := fmt.Sprintf("%s%s/getFile?file_id=%s", c.baseURL, c.botToken, fileID)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to get file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result GetFileResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return result.Result, nil
}

// DownloadFile downloads a file from Telegram servers
func (c *Client) DownloadFile(ctx context.Context, filePath string) ([]byte, error) {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.botToken, filePath)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read file data: %w", err)
	}

	return data, nil
}

// BriefMessageResponse is a simplified response for brief delivery
type BriefMessageResponse struct {
	MessageID int64
}

// SendBriefMessage sends a message and returns a simplified response for brief delivery.
// This method satisfies the briefs.TelegramSender interface.
func (c *Client) SendBriefMessage(ctx context.Context, chatID, text, parseMode string) (*BriefMessageResponse, error) {
	resp, err := c.SendMessage(ctx, chatID, text, parseMode)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Result == nil {
		return nil, nil
	}
	return &BriefMessageResponse{MessageID: resp.Result.MessageID}, nil
}
