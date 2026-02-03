package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// LLMClassifierConfig holds configuration for the LLM-based intent classifier
type LLMClassifierConfig struct {
	Enabled    bool          `yaml:"enabled"`     // Enable LLM classification (default: false, uses regex fallback)
	Model      string        `yaml:"model"`       // Model to use (default: claude-3-haiku-20240307)
	Timeout    time.Duration `yaml:"timeout"`     // API timeout (default: 2s)
	MaxHistory int           `yaml:"max_history"` // Max conversation history for context (default: 10)
	HistoryTTL time.Duration `yaml:"history_ttl"` // TTL for conversation history (default: 30m)
}

// DefaultLLMClassifierConfig returns sensible defaults
func DefaultLLMClassifierConfig() *LLMClassifierConfig {
	return &LLMClassifierConfig{
		Enabled:    false,
		Model:      "claude-3-haiku-20240307",
		Timeout:    2 * time.Second,
		MaxHistory: 10,
		HistoryTTL: 30 * time.Minute,
	}
}

// ClassificationResult contains the LLM classification output
type ClassificationResult struct {
	Intent      Intent  // Detected intent
	Confidence  float64 // Confidence score (0.0 - 1.0)
	Reasoning   string  // Why this intent was chosen
	TaskSummary string  // Context summary for task execution (if intent is task)
}

// LLMClassifier uses Claude Haiku for intent classification
type LLMClassifier struct {
	apiKey     string
	model      string
	timeout    time.Duration
	httpClient *http.Client
	enabled    bool
	log        *slog.Logger
}

// NewLLMClassifier creates a new LLM classifier
func NewLLMClassifier(cfg *LLMClassifierConfig) *LLMClassifier {
	if cfg == nil {
		cfg = DefaultLLMClassifierConfig()
	}

	// Read API key from environment (same as Claude Code uses)
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	classifier := &LLMClassifier{
		apiKey:  apiKey,
		model:   cfg.Model,
		timeout: cfg.Timeout,
		enabled: cfg.Enabled && apiKey != "",
		log:     logging.WithComponent("classifier"),
	}

	if cfg.Model == "" {
		classifier.model = "claude-3-haiku-20240307"
	}
	if cfg.Timeout <= 0 {
		classifier.timeout = 2 * time.Second
	}

	classifier.httpClient = &http.Client{
		Timeout: classifier.timeout,
	}

	if cfg.Enabled && apiKey == "" {
		classifier.log.Warn("LLM classifier enabled but ANTHROPIC_API_KEY not set, falling back to regex")
	}

	return classifier
}

// IsEnabled returns whether the classifier is enabled and functional
func (c *LLMClassifier) IsEnabled() bool {
	return c.enabled
}

// Classify classifies a message using the LLM with conversation context
func (c *LLMClassifier) Classify(ctx context.Context, message string, history []ConversationMessage) (*ClassificationResult, error) {
	if !c.enabled {
		return nil, fmt.Errorf("classifier not enabled")
	}

	// Build prompt with conversation context
	prompt := c.buildClassificationPrompt(message, history)

	// Make API call
	response, err := c.callAPI(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("API call failed: %w", err)
	}

	// Parse response
	result, err := c.parseResponse(response)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log.Debug("Classification result",
		slog.String("message", truncateForLog(message, 50)),
		slog.String("intent", string(result.Intent)),
		slog.Float64("confidence", result.Confidence),
	)

	return result, nil
}

// buildClassificationPrompt creates the prompt for intent classification
func (c *LLMClassifier) buildClassificationPrompt(message string, history []ConversationMessage) string {
	var sb strings.Builder

	sb.WriteString(`You are an intent classifier for a Telegram bot that manages code tasks.

## Intent Types
- command: Bot commands starting with /
- greeting: Casual greetings (hi, hello, hey)
- question: Questions about the codebase (what, how, where + ?)
- research: Requests to analyze, review, summarize code
- planning: Requests to plan, design, architect solutions
- chat: Conversational/opinion seeking, no action intent
- task: Requests to create, modify, fix, implement code

## Key Classification Rules
1. Casual mentions of action words in reaction context = chat, NOT task
   - "Wow, let's commit changes first" (reacting to something) = chat
   - "Let's commit the authentication changes" (clear directive) = task
2. "Check if X happened" or "Check the status" = question, NOT task
3. Ambiguous without clear action intent → prefer chat over task
4. "yes", "ok", "confirm" after a task suggestion = use context to determine
5. Questions ending with ? are usually questions unless they contain clear task directives

## Conversation Context
`)

	// Add conversation history if available
	if len(history) > 0 {
		sb.WriteString("Recent conversation:\n")
		// Show last few messages for context
		start := 0
		if len(history) > 5 {
			start = len(history) - 5
		}
		for _, msg := range history[start:] {
			role := "User"
			if msg.Role == "assistant" {
				role = "Bot"
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", role, truncateForContext(msg.Content, 150)))
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("(No prior conversation)\n\n")
	}

	sb.WriteString(fmt.Sprintf("## Current Message to Classify\n%s\n\n", message))

	sb.WriteString(`## Response Format
Respond with ONLY a JSON object (no markdown, no explanation):
{"intent": "...", "confidence": 0.0-1.0, "reasoning": "brief explanation", "task_summary": "context for task execution if applicable"}

If intent is "task", include a task_summary that captures the full context from the conversation for execution.
`)

	return sb.String()
}

// anthropicRequest represents the API request structure
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

// anthropicMessage represents a message in the API request
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse represents the API response structure
type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// callAPI makes the API call to Claude
func (c *LLMClassifier) callAPI(ctx context.Context, prompt string) (string, error) {
	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 256,
		Messages: []anthropicMessage{
			{Role: "user", Content: prompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s", apiResp.Error.Message)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response content")
	}

	return apiResp.Content[0].Text, nil
}

// classificationJSON represents the expected JSON response from the LLM
type classificationJSON struct {
	Intent      string  `json:"intent"`
	Confidence  float64 `json:"confidence"`
	Reasoning   string  `json:"reasoning"`
	TaskSummary string  `json:"task_summary"`
}

// parseResponse parses the LLM response into a ClassificationResult
func (c *LLMClassifier) parseResponse(response string) (*ClassificationResult, error) {
	// Clean up response - remove markdown code blocks if present
	response = strings.TrimSpace(response)
	response = strings.TrimPrefix(response, "```json")
	response = strings.TrimPrefix(response, "```")
	response = strings.TrimSuffix(response, "```")
	response = strings.TrimSpace(response)

	var parsed classificationJSON
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w (response: %s)", err, truncateForLog(response, 200))
	}

	// Map string intent to Intent type
	intent := mapStringToIntent(parsed.Intent)

	return &ClassificationResult{
		Intent:      intent,
		Confidence:  parsed.Confidence,
		Reasoning:   parsed.Reasoning,
		TaskSummary: parsed.TaskSummary,
	}, nil
}

// mapStringToIntent converts a string intent to the Intent type
func mapStringToIntent(s string) Intent {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "command":
		return IntentCommand
	case "greeting":
		return IntentGreeting
	case "question":
		return IntentQuestion
	case "research":
		return IntentResearch
	case "planning":
		return IntentPlanning
	case "chat":
		return IntentChat
	case "task":
		return IntentTask
	default:
		// Default to chat for safety (won't accidentally trigger task execution)
		return IntentChat
	}
}

// truncateForLog truncates a string for logging
func truncateForLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
