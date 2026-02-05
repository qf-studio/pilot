package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// AnthropicClient provides direct access to Claude API for fast classification
type AnthropicClient struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

// NewAnthropicClient creates a new Anthropic API client
func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second, // Fast timeout for classification
		},
		model: "claude-haiku-4-5-20251001",
	}
}

// ConversationMessage represents a message in the conversation
type ConversationMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// ClassifyResponse is the response from the classification API
type ClassifyResponse struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
}

// Classify determines the intent of a message using Claude Haiku
func (c *AnthropicClient) Classify(ctx context.Context, messages []ConversationMessage, currentMessage string) (Intent, error) {
	// Build the classification prompt
	systemPrompt := `You are an intent classifier for a coding assistant bot. Classify the user's message into exactly one of these intents:

- command: Message starts with /
- greeting: Simple greeting like "hi", "hello", "hey"
- research: Requests for analysis/research (e.g., "research how X works", "analyze the auth flow")
- planning: Requests for implementation plans (e.g., "plan how to add X", "design a solution for Y")
- question: Questions about code/project (e.g., "what files handle auth?", "how does X work?")
- chat: Conversational/opinion-seeking (e.g., "what do you think about...", "should I...")
- task: Requests to make changes (e.g., "add a button", "fix the bug", "implement feature X")

IMPORTANT:
- "What do you think about adding X?" is CHAT (asking opinion), not TASK
- "Add X to the project" is TASK (direct instruction)
- Questions that don't require code changes are QUESTION
- Be conservative: when in doubt between task and chat, prefer chat

Respond with JSON only: {"intent": "...", "confidence": 0.0-1.0}`

	// Build messages array for API
	apiMessages := []map[string]string{}

	// Add conversation history (last 5 messages for context)
	historyStart := 0
	if len(messages) > 5 {
		historyStart = len(messages) - 5
	}
	for _, msg := range messages[historyStart:] {
		apiMessages = append(apiMessages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Add the current message to classify
	apiMessages = append(apiMessages, map[string]string{
		"role":    "user",
		"content": fmt.Sprintf("Classify this message: %s", currentMessage),
	})

	// Build request body
	requestBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": 100,
		"system":     systemPrompt,
		"messages":   apiMessages,
		"output_config": map[string]interface{}{
			"effort": "low", // Fast classification, minimize token spend
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make API request
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	// Parse response
	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response from API")
	}

	// Parse the JSON response
	var classifyResp ClassifyResponse
	if err := json.Unmarshal([]byte(apiResp.Content[0].Text), &classifyResp); err != nil {
		return "", fmt.Errorf("failed to parse classification: %w", err)
	}

	// Map to Intent type
	switch classifyResp.Intent {
	case "command":
		return IntentCommand, nil
	case "greeting":
		return IntentGreeting, nil
	case "research":
		return IntentResearch, nil
	case "planning":
		return IntentPlanning, nil
	case "question":
		return IntentQuestion, nil
	case "chat":
		return IntentChat, nil
	case "task":
		return IntentTask, nil
	default:
		return IntentTask, nil // Default fallback
	}
}
