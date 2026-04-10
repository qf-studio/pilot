package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// Client is a Discord REST API client with rate limit handling.
type Client struct {
	botToken   string
	baseURL    string
	httpClient *http.Client
	log        *slog.Logger
	maxRetries int
}

// NewClient creates a new Discord client.
func NewClient(botToken string) *Client {
	return &Client{
		botToken: botToken,
		baseURL:  DiscordAPIURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		log:        logging.WithComponent("discord.client"),
		maxRetries: 3,
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
		log:        logging.WithComponent("discord.client"),
		maxRetries: 3,
	}
}

// doRequest sends an HTTP request to the Discord API with rate limit handling.
// On 429 responses, it reads the Retry-After header and retries up to maxRetries times.
func (c *Client) doRequest(ctx context.Context, method, endpoint string, body interface{}) ([]byte, error) {
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		respBody, statusCode, retryAfter, err := c.doRequestOnce(ctx, method, endpoint, body)
		if err != nil {
			return nil, err
		}

		if statusCode == http.StatusTooManyRequests {
			if attempt >= c.maxRetries {
				return nil, fmt.Errorf("discord API rate limited after %d retries: HTTP 429", c.maxRetries)
			}

			wait := retryAfter
			if wait <= 0 {
				wait = time.Duration(attempt+1) * time.Second
			}

			c.log.Warn("Rate limited by Discord API, waiting",
				slog.String("endpoint", endpoint),
				slog.Duration("retry_after", wait),
				slog.Int("attempt", attempt+1))

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if statusCode >= 400 {
			return nil, fmt.Errorf("discord API error: HTTP %d: %s", statusCode, string(respBody))
		}

		return respBody, nil
	}

	return nil, fmt.Errorf("discord API: exhausted retries")
}

// doRequestOnce performs a single HTTP request and returns the body, status code, and retry-after duration.
func (c *Client) doRequestOnce(ctx context.Context, method, endpoint string, body interface{}) ([]byte, int, time.Duration, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, reqBody)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bot "+c.botToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DiscordBot (Pilot, 1.0)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("read response: %w", err)
	}

	// Parse Retry-After header for 429 responses
	var retryAfter time.Duration
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter = parseRetryAfter(resp.Header)
	}

	return respBody, resp.StatusCode, retryAfter, nil
}

// parseRetryAfter extracts the wait duration from the Retry-After header.
// Discord sends this as seconds (possibly fractional).
func parseRetryAfter(headers http.Header) time.Duration {
	val := headers.Get("Retry-After")
	if val == "" {
		return 0
	}

	// Try parsing as float (Discord uses fractional seconds)
	if secs, err := strconv.ParseFloat(val, 64); err == nil {
		return time.Duration(secs * float64(time.Second))
	}

	// Fallback: parse as integer seconds
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}

	return 0
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
		Content    string      `json:"content"`
		Components []Component `json:"components"`
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
