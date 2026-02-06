package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// SubtaskParser extracts subtasks from planning output using the Anthropic Haiku API.
// Part of the epic planning pipeline: PlanEpic → parseSubtasksWithFallback → SubtaskParser.
// When the API is unavailable or fails, parseSubtasksWithFallback falls back to
// regex-based parseSubtasks() in epic.go.
type SubtaskParser struct {
	apiKey     string
	baseURL    string // Base URL for API (default: https://api.anthropic.com)
	httpClient *http.Client
	model      string
	log        *slog.Logger
}

// NewSubtaskParser creates a SubtaskParser using the ANTHROPIC_API_KEY env var.
// Returns nil if the API key is not set (caller should use regex fallback).
func NewSubtaskParser(log *slog.Logger) *SubtaskParser {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil
	}
	return &SubtaskParser{
		apiKey: apiKey,
		baseURL: "https://api.anthropic.com",
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		model: "claude-haiku-4-5-20251001",
		log:   log,
	}
}

// newSubtaskParserWithURL creates a SubtaskParser with a custom base URL (for testing).
func newSubtaskParserWithURL(apiKey, baseURL string, log *slog.Logger) *SubtaskParser {
	return &SubtaskParser{
		apiKey:  apiKey,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		model: "claude-haiku-4-5-20251001",
		log:   log,
	}
}

// subtaskJSON is the JSON schema for subtask extraction.
type subtaskJSON struct {
	Order       int    `json:"order"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// subtasksResponse wraps the array of subtasks in the API response.
type subtasksResponse struct {
	Subtasks []subtaskJSON `json:"subtasks"`
}

// Parse sends the planning output to Haiku for structured extraction.
// Returns the extracted subtasks or an error if the API call fails.
func (p *SubtaskParser) Parse(ctx context.Context, output string) ([]PlannedSubtask, error) {
	if p == nil {
		return nil, fmt.Errorf("subtask parser is nil")
	}

	systemPrompt := `Extract subtasks from this planning output as JSON. Return ONLY a JSON object with a "subtasks" array. Each subtask must have: "order" (integer), "title" (string), "description" (string).

Example response:
{"subtasks": [{"order": 1, "title": "Set up database", "description": "Create tables and migrations"}, {"order": 2, "title": "Add API endpoints", "description": "REST endpoints for CRUD operations"}]}`

	requestBody := map[string]interface{}{
		"model":      p.model,
		"max_tokens": 1000,
		"system":     systemPrompt,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": output,
			},
		},
		"output_config": map[string]interface{}{
			"effort": "low",
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := p.baseURL + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	// Parse the Anthropic API response envelope
	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from API")
	}

	// Parse the JSON subtasks from the response text
	var parsed subtasksResponse
	if err := json.Unmarshal([]byte(apiResp.Content[0].Text), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse subtasks JSON: %w", err)
	}

	if len(parsed.Subtasks) == 0 {
		return nil, fmt.Errorf("no subtasks in API response")
	}

	// Convert to PlannedSubtask slice
	result := make([]PlannedSubtask, len(parsed.Subtasks))
	for i, s := range parsed.Subtasks {
		result[i] = PlannedSubtask{
			Order:       s.Order,
			Title:       s.Title,
			Description: s.Description,
		}
	}

	return result, nil
}

// parseSubtasksWithFallback is the primary entry point for subtask extraction.
// Tries Haiku structured extraction first (SubtaskParser.Parse), then falls back
// to regex-based parseSubtasks() in epic.go if the API is unavailable or fails.
func parseSubtasksWithFallback(parser *SubtaskParser, output string) []PlannedSubtask {
	if parser != nil {
		subtasks, err := parser.Parse(context.Background(), output)
		if err == nil && len(subtasks) > 0 {
			if parser.log != nil {
				parser.log.Debug("Subtasks extracted via Haiku API", "count", len(subtasks))
			}
			return subtasks
		}
		if parser.log != nil {
			parser.log.Warn("Haiku subtask extraction failed, falling back to regex", "error", err)
		}
	}

	// Fallback to regex parsing
	return parseSubtasks(output)
}
