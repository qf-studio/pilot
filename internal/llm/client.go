package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/intent"
	"github.com/qf-studio/pilot/internal/llm/anthropic"
)

const defaultAPIURL = "https://api.anthropic.com/v1/messages"

// maxErrorBodyBytes caps how much of a non-200 response body we read into an
// error message, so a misbehaving API doesn't force us to buffer an unbounded
// payload.
const maxErrorBodyBytes = 4096

// maxTokens caps the response length for bot replies (chat, grounded Q&A, issue
// drafts). The Anthropic Messages API REQUIRES max_tokens — omitting it returns
// HTTP 400 "max_tokens: Field required".
const maxTokens = 2048

// Client sends requests to the Anthropic Messages API.
type Client struct {
	apiKey     string
	httpClient *http.Client
	apiURL     string
}

// NewClient creates a Client with the given API key.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		apiURL: defaultAPIURL,
	}
}

// Answer calls the Anthropic API with the provided model, system prompt,
// conversation history, and user message, then returns the concatenated text
// of all content blocks in the response.
func (c *Client) Answer(ctx context.Context, model, system string, history []intent.ConversationMessage, user string) (string, error) {
	// Build messages array from history + new user message.
	messages := make([]anthropic.Message, 0, len(history)+1)
	for _, msg := range history {
		messages = append(messages, anthropic.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	messages = append(messages, anthropic.Message{
		Role:    "user",
		Content: user,
	})

	apiReq, err := anthropic.NewRequest(model, maxTokens, messages)
	if err != nil {
		return "", fmt.Errorf("llm: build request: %w", err)
	}
	apiReq.WithSystem(system)

	jsonBody, err := json.Marshal(apiReq)
	if err != nil {
		return "", fmt.Errorf("llm: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("llm: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return "", fmt.Errorf("llm: API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("llm: decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("llm: empty response from API")
	}

	var sb strings.Builder
	for _, block := range apiResp.Content {
		sb.WriteString(block.Text)
	}
	return sb.String(), nil
}
