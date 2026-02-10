package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// ComplexityClassifier uses an LLM (Haiku) to classify task complexity
// instead of word-count heuristics. Falls back to heuristic on API failure.
// Caches results per task ID to avoid re-classification on retries.
type ComplexityClassifier struct {
	apiKey     string
	apiURL     string
	model      string
	httpClient *http.Client
	log        *slog.Logger

	mu    sync.Mutex
	cache map[string]Complexity // task ID → cached classification
}

// classificationResponse is the JSON structure returned by the LLM.
type classificationResponse struct {
	Complexity string `json:"complexity"`
	Reason     string `json:"reason"`
}

const complexityClassifierSystemPrompt = `You are a task complexity classifier for a software development pipeline. Classify the given issue into exactly one complexity level.

Levels:
- TRIVIAL: Minimal changes like typos, log additions, renames, comment updates
- SIMPLE: Small focused changes: add a field, small fix, single function change
- MEDIUM: Standard feature work: new endpoint, component, integration. Also includes well-scoped issues with clear step-by-step instructions — these are NOT complex even if detailed
- COMPLEX: Requires multiple independent components or architectural changes: refactors, migrations, system redesigns
- EPIC: Too large for single execution: multi-phase projects with 5+ distinct phases

IMPORTANT: A detailed issue with clear step-by-step instructions is NOT complex — it's well-scoped MEDIUM work. COMPLEX means the task requires changes across multiple independent systems or architectural decisions.

Respond with ONLY a JSON object (no markdown, no explanation):
{"complexity": "TRIVIAL|SIMPLE|MEDIUM|COMPLEX|EPIC", "reason": "brief one-sentence explanation"}`

// NewComplexityClassifier creates a classifier that calls the Anthropic API.
// Returns nil if apiKey is empty (caller should fall back to heuristic).
func NewComplexityClassifier(apiKey string) *ComplexityClassifier {
	if apiKey == "" {
		return nil
	}
	return &ComplexityClassifier{
		apiKey: apiKey,
		apiURL: "https://api.anthropic.com/v1/messages",
		model:  "claude-haiku-4-5-20251001",
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		log:   logging.WithComponent("complexity-classifier"),
		cache: make(map[string]Complexity),
	}
}

// newComplexityClassifierWithURL creates a classifier with a custom API URL for testing.
func newComplexityClassifierWithURL(apiKey, url string) *ComplexityClassifier {
	c := NewComplexityClassifier(apiKey)
	if c == nil {
		return nil
	}
	c.apiURL = url
	return c
}

// Classify determines task complexity using the LLM.
// Returns cached result if available for the given task ID.
// Falls back to heuristic-based DetectComplexity on any API error.
func (c *ComplexityClassifier) Classify(ctx context.Context, task *Task) Complexity {
	if task == nil {
		return ComplexityMedium
	}

	// Check cache first (prevents re-classification on retry)
	if task.ID != "" {
		c.mu.Lock()
		if cached, ok := c.cache[task.ID]; ok {
			c.mu.Unlock()
			c.log.Debug("using cached complexity", slog.String("task_id", task.ID), slog.String("complexity", string(cached)))
			return cached
		}
		c.mu.Unlock()
	}

	// Call LLM
	result, err := c.callAPI(ctx, task)
	if err != nil {
		c.log.Warn("LLM classification failed, falling back to heuristic",
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
		return DetectComplexity(task)
	}

	// Cache result
	if task.ID != "" {
		c.mu.Lock()
		c.cache[task.ID] = result
		c.mu.Unlock()
	}

	c.log.Info("LLM classified task complexity",
		slog.String("task_id", task.ID),
		slog.String("complexity", string(result)),
	)

	return result
}

// callAPI makes the Haiku API call and parses the response.
func (c *ComplexityClassifier) callAPI(ctx context.Context, task *Task) (Complexity, error) {
	userContent := fmt.Sprintf("## Issue Title\n%s\n\n## Issue Description\n%s", task.Title, task.Description)

	// Truncate to avoid token overflow (description can be very long)
	const maxChars = 4000
	if len(userContent) > maxChars {
		userContent = userContent[:maxChars] + "\n...[truncated]"
	}

	reqBody := haikuRequest{
		Model:     c.model,
		MaxTokens: 256,
		System:    complexityClassifierSystemPrompt,
		Messages: []haikuMessage{
			{Role: "user", Content: userContent},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
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

	var apiResp haikuResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(apiResp.Content) == 0 || apiResp.Content[0].Text == "" {
		return "", fmt.Errorf("empty response from API")
	}

	return parseClassificationResponse(apiResp.Content[0].Text)
}

// parseClassificationResponse extracts complexity from the LLM's JSON response.
func parseClassificationResponse(text string) (Complexity, error) {
	// Strip any markdown code fence wrapper
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var resp classificationResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return "", fmt.Errorf("parse classification JSON: %w (raw: %s)", err, text)
	}

	switch strings.ToLower(resp.Complexity) {
	case "trivial":
		return ComplexityTrivial, nil
	case "simple":
		return ComplexitySimple, nil
	case "medium":
		return ComplexityMedium, nil
	case "complex":
		return ComplexityComplex, nil
	case "epic":
		return ComplexityEpic, nil
	default:
		return "", fmt.Errorf("unknown complexity level: %q", resp.Complexity)
	}
}

// HasLabel checks if the task has a specific label (case-insensitive).
func HasLabel(task *Task, label string) bool {
	if task == nil {
		return false
	}
	label = strings.ToLower(label)
	for _, l := range task.Labels {
		if strings.ToLower(l) == label {
			return true
		}
	}
	return false
}
