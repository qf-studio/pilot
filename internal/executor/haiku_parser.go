package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HaikuParser uses the Anthropic API (Haiku model) to parse planning output
// into structured subtasks. This provides a faster, cheaper alternative to
// shelling out to the Claude CLI for parsing structured data.
type HaikuParser struct {
	apiKey     string
	apiURL     string
	model      string
	httpClient *http.Client
}

// NewHaikuParser creates a new HaikuParser that calls the Anthropic API directly.
func NewHaikuParser(apiKey string) *HaikuParser {
	return &HaikuParser{
		apiKey: apiKey,
		apiURL: "https://api.anthropic.com/v1/messages",
		model:  "claude-haiku-4-5-20251001",
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// haikuRequest is the Anthropic Messages API request body.
type haikuRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    string            `json:"system"`
	Messages  []haikuMessage    `json:"messages"`
}

// haikuMessage is a single message in the API request.
type haikuMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// haikuResponse is the Anthropic Messages API response body.
type haikuResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// haikuSubtaskJSON is the JSON shape returned by Haiku for each subtask.
type haikuSubtaskJSON struct {
	Order       int    `json:"order"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ParsePlanning sends raw planning output to Haiku and returns parsed subtasks.
// The model extracts numbered subtasks from freeform text and returns structured JSON.
func (h *HaikuParser) ParsePlanning(ctx context.Context, planningOutput string) ([]PlannedSubtask, error) {
	if planningOutput == "" {
		return nil, fmt.Errorf("empty planning output")
	}

	systemPrompt := `You are a structured data extractor. Given planning output that contains numbered subtasks, extract them into a JSON array.

Each subtask should have:
- "order": the sequence number (integer, 1-indexed)
- "title": short title of the subtask
- "description": detailed description of what needs to be done

Return ONLY a valid JSON array. No markdown, no explanation. Example:
[{"order":1,"title":"Set up schema","description":"Create database tables"}]`

	reqBody := haikuRequest{
		Model:     h.model,
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages: []haikuMessage{
			{
				Role:    "user",
				Content: fmt.Sprintf("Extract subtasks from this planning output:\n\n%s", planningOutput),
			},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", h.apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", h.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var apiResp haikuResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from API")
	}

	text := apiResp.Content[0].Text

	// Parse the JSON array from the response text
	var rawSubtasks []haikuSubtaskJSON
	if err := json.Unmarshal([]byte(text), &rawSubtasks); err != nil {
		return nil, fmt.Errorf("failed to parse subtasks JSON: %w", err)
	}

	if len(rawSubtasks) == 0 {
		return nil, fmt.Errorf("no subtasks in response")
	}

	// Convert to PlannedSubtask
	subtasks := make([]PlannedSubtask, len(rawSubtasks))
	for i, raw := range rawSubtasks {
		subtasks[i] = PlannedSubtask{
			Order:       raw.Order,
			Title:       raw.Title,
			Description: raw.Description,
		}
	}

	return subtasks, nil
}
