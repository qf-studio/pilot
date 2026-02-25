package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a Discord REST API client.
type Client struct {
	botToken   string
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a new Discord client.
func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  DiscordAPIURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new Discord client with a custom base URL (for testing).
func NewClientWithBaseURL(botToken, baseURL string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doRequest sends an HTTP request to the Discord API.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bot "+c.botToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DiscordBot (Pilot, 1.0)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("discord API error: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

// SendMessage sends a message to a channel.
func (c *Client) SendMessage(ctx context.Context, channelID, content string) (*Message, error) {
	payload := struct {
		Content string `json:"content"`
	}{Content: content}

	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/channels/%s/messages", channelID), payload)
	if err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(resp, &msg); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &msg, nil
}

// EditMessage edits an existing message.
func (c *Client) EditMessage(ctx context.Context, channelID, messageID, content string) error {
	payload := struct {
		Content string `json:"content"`
	}{Content: content}

	_, err := c.doRequest(ctx, http.MethodPatch, fmt.Sprintf("/channels/%s/messages/%s", channelID, messageID), payload)
	if err != nil {
		return fmt.Errorf("edit message: %w", err)
	}

	return nil
}

// SendMessageWithComponents sends a message with interactive components (buttons).
func (c *Client) SendMessageWithComponents(ctx context.Context, channelID, content string, components []Component) (*Message, error) {
	payload := struct {
		Content    string       `json:"content"`
		Components []Component  `json:"components"`
	}{
		Content:    content,
		Components: components,
	}

	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/channels/%s/messages", channelID), payload)
	if err != nil {
		return nil, fmt.Errorf("send message with components: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(resp, &msg); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &msg, nil
}

// CreateInteractionResponse acknowledges an interaction (button click).
func (c *Client) CreateInteractionResponse(ctx context.Context, interactionID, interactionToken string, responseType int, content string) error {
	payload := struct {
		Type int `json:"type"`
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}{
		Type: responseType,
	}
	payload.Data.Content = content

	_, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/interactions/%s/%s/callback", interactionID, interactionToken), payload)
	if err != nil {
		return fmt.Errorf("create interaction response: %w", err)
	}

	return nil
}

// GetGatewayURL returns the WebSocket gateway URL.
func (c *Client) GetGatewayURL(ctx context.Context) (string, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/gateway", nil)
	if err != nil {
		return "", fmt.Errorf("get gateway: %w", err)
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	return result.URL, nil
}
