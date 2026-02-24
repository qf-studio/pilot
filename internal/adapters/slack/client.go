package slack

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
	slackAPIURL = "https://slack.com/api"
)

// Client is a Slack API client
type Client struct {
	botToken   string
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Slack client
func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  slackAPIURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new Slack client with a custom base URL (for testing).
func NewClientWithBaseURL(botToken, baseURL string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Message represents a Slack message
type Message struct {
	Channel     string       `json:"channel"`
	Text        string       `json:"text,omitempty"`
	Blocks      []Block      `json:"blocks,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
}

// Block represents a Slack block
type Block struct {
	Type     string       `json:"type"`
	Text     *TextObject  `json:"text,omitempty"`
	Elements []TextObject `json:"elements,omitempty"`
}

// TextObject represents text in a block
type TextObject struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// ButtonElement represents an interactive button in Slack
type ButtonElement struct {
	Type     string      `json:"type"`
	Text     *TextObject `json:"text"`
	ActionID string      `json:"action_id"`
	Value    string      `json:"value,omitempty"`
	Style    string      `json:"style,omitempty"` // "primary" or "danger"
}

// ActionsBlock represents an actions block containing interactive elements
type ActionsBlock struct {
	Type     string          `json:"type"`
	BlockID  string          `json:"block_id,omitempty"`
	Elements []ButtonElement `json:"elements"`
}

// Attachment represents a Slack attachment
type Attachment struct {
	Color  string `json:"color,omitempty"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text,omitempty"`
	Footer string `json:"footer,omitempty"`
}

// PostMessageResponse represents the response from posting a message
type PostMessageResponse struct {
	OK       bool   `json:"ok"`
	TS       string `json:"ts"`
	Channel  string `json:"channel"`
	Error    string `json:"error,omitempty"`
	ErrorMsg string `json:"error_msg,omitempty"`
}

// PostMessage posts a message to a channel
func (c *Client) PostMessage(ctx context.Context, msg *Message) (*PostMessageResponse, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to post message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result PostMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("slack API error: %s", result.Error)
	}

	return &result, nil
}

// UpdateMessage updates an existing message
func (c *Client) UpdateMessage(ctx context.Context, channel, ts string, msg *Message) error {
	payload := struct {
		Channel string  `json:"channel"`
		TS      string  `json:"ts"`
		Text    string  `json:"text,omitempty"`
		Blocks  []Block `json:"blocks,omitempty"`
	}{
		Channel: channel,
		TS:      ts,
		Text:    msg.Text,
		Blocks:  msg.Blocks,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat.update", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("slack API error: %s", result.Error)
	}

	return nil
}

// InteractiveMessage represents a message with interactive buttons
type InteractiveMessage struct {
	Channel string        `json:"channel"`
	Text    string        `json:"text,omitempty"`
	Blocks  []interface{} `json:"blocks,omitempty"`
}

// PostInteractiveMessage posts a message with interactive buttons to a channel
func (c *Client) PostInteractiveMessage(ctx context.Context, msg *InteractiveMessage) (*PostMessageResponse, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to post message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result PostMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("slack API error: %s", result.Error)
	}

	return &result, nil
}

// UpdateInteractiveMessage updates an existing message (removes buttons, updates text)
func (c *Client) UpdateInteractiveMessage(ctx context.Context, channel, ts string, blocks []interface{}, text string) error {
	payload := struct {
		Channel string        `json:"channel"`
		TS      string        `json:"ts"`
		Text    string        `json:"text,omitempty"`
		Blocks  []interface{} `json:"blocks,omitempty"`
	}{
		Channel: channel,
		TS:      ts,
		Text:    text,
		Blocks:  blocks,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat.update", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.botToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("slack API error: %s", result.Error)
	}

	return nil
}
