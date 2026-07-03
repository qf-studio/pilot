package intent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxErrorBodyBytes caps how much of a non-200 response body we read into an
// error message, so a misbehaving API doesn't force us to buffer an unbounded
// payload.
const maxErrorBodyBytes = 4096

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

// Classifier classifies user messages into intents
type Classifier interface {
	Classify(ctx context.Context, messages []ConversationMessage, currentMessage string) (Intent, error)
}

// AnthropicClient provides direct access to Claude API for fast classification
type AnthropicClient struct {
	apiKey     string
	httpClient *http.Client
	model      string
	apiURL     string
}

// NewAnthropicClient creates a new Anthropic API client
func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second, // Fast timeout for classification
		},
		model:  "claude-haiku-4-5-20251001",
		apiURL: "https://api.anthropic.com/v1/messages",
	}
}

// SetModel overrides the model used for classification.
func (c *AnthropicClient) SetModel(model string) {
	c.model = model
}

// SetAPIURL overrides the API endpoint URL.
func (c *AnthropicClient) SetAPIURL(url string) {
	c.apiURL = url
}

// Classify determines the intent of a message using Claude Haiku
func (c *AnthropicClient) Classify(ctx context.Context, messages []ConversationMessage, currentMessage string) (Intent, error) {
	// Build the classification prompt
	systemPrompt := `You are an intent classifier for a coding assistant bot. Classify the user's message into exactly one of these intents:

- command: Message starts with /
- greeting: Simple greeting like "hi", "hello", "hey"
- research: Deep multi-file analysis/research (e.g., "research how X works across the codebase", "analyze the auth flow")
- planning: Requests for an implementation PLAN before changing code (e.g., "plan how to add X", "design a solution for Y")
- operational: Queries about live daemon or queue state (e.g., "what's in the queue?", "anything running?", "how many tasks", "status of the queue")
- question: Asking the bot to EXPLAIN, DESCRIBE, SHOW, DIAGRAM, DRAW, OUTLINE, SKETCH, VISUALIZE, or SUMMARIZE existing code/architecture (e.g., "what files handle auth?", "how does X work?", "draw the schema in ASCII", "diagram the intent flow", "show me how auth works"). The deliverable is a text/diagram ANSWER — NOT a code change.
- chat: Conversational/opinion-seeking (e.g., "what do you think about...", "should I...")
- task: Requests to CHANGE the codebase — add/fix/implement/refactor code that results in a commit or PR (e.g., "add a button", "fix the bug", "implement feature X")
- issue_intake: Requests to file a new GitHub issue (e.g., "create an issue to...", "file a ticket for...", "open an issue about...", "raise a ticket")

DELIVERABLE TEST (apply this first):
- Does the user want the bot to MODIFY files / produce a PR? -> TASK (or ISSUE_INTAKE if they explicitly ask to file/open/raise a ticket or issue).
- Does the user want an ANSWER, EXPLANATION, DIAGRAM, or OPINION? -> QUESTION (about the code) or CHAT (opinion). Verbs like draw/diagram/show/outline/sketch/visualize/explain/summarize produce an answer, NOT a code change -> QUESTION.

IMPORTANT:
- "draw the schema in ASCII" / "outline the intent flow" / "show me how auth works" are QUESTION (they produce a diagram/answer), NOT TASK.
- "What do you think about adding X?" is CHAT (asking opinion), not TASK.
- "Add X to the project" / "fix the login bug" is TASK (modify code).
- "Create an issue to add rate limiting" / "file a ticket for the login bug" is ISSUE_INTAKE, not TASK.
- "What's in the queue?" or "anything running?" are OPERATIONAL (live state), not QUESTION.
- Be conservative: when in doubt between task and question/chat, prefer the non-mutating one (question/chat).

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
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make API request
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(jsonBody))
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
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
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
	case "operational":
		return IntentOperational, nil
	case "question":
		return IntentQuestion, nil
	case "chat":
		return IntentChat, nil
	case "task":
		return IntentTask, nil
	case "issue_intake":
		return IntentIssueIntake, nil
	default:
		return IntentTask, nil // Default fallback
	}
}
