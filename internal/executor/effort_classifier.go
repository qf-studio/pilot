package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// EffortClassifier uses an LLM (Haiku) to classify task effort level.
// Supports two modes:
//   - "api": Direct HTTP call to Anthropic Messages API (~1MB memory)
//   - "subprocess": Spawns `claude --print` (~300MB memory, legacy)
//
// Falls back to static complexity→effort mapping on failure.
// Caches results per task ID to avoid re-classification on retries.
//
// GH-727: LLM-based effort selection for smarter resource allocation.
// Cost: ~$0.0002 per classification (negligible vs execution savings).
type EffortClassifier struct {
	model               string
	timeout             time.Duration
	log                 *slog.Logger
	useStructuredOutput bool
	apiKey              string // Anthropic API key or OAuth token for direct API mode
	apiURL              string // API endpoint URL (default: https://api.anthropic.com/v1/messages)

	// cmdRunner is the function that executes the claude command (subprocess mode).
	// Can be overridden for testing.
	cmdRunner func(ctx context.Context, args ...string) ([]byte, error)

	mu    sync.Mutex
	cache map[string]string // task ID → cached effort level
}

// effortClassificationResponse is the JSON structure returned by the LLM.
type effortClassificationResponse struct {
	Effort string `json:"effort"`
	Reason string `json:"reason"`
}

const effortClassifierSystemPrompt = `You are an effort classifier for a software development pipeline. Classify the given issue into exactly one effort level.

Effort levels control how many tokens Claude uses when responding — trading off between thoroughness and efficiency.

Levels:
- LOW: Straightforward, mechanical tasks. No ambiguity. Examples: typos, log additions, renames, config tweaks.
- MEDIUM: Standard work with clear requirements. Examples: add a field, implement a well-defined endpoint, write tests for existing code.
- HIGH: Tasks requiring careful analysis or multiple considerations. Examples: refactors, debugging subtle issues, implementing features with security implications.

Decision factors (ranked by importance):
1. Ambiguity → higher effort (unclear requirements need more reasoning)
2. Risk (security/data integrity) → higher effort (mistakes are costly)
3. Scope (multi-file, cross-system) → higher effort (coordination needed)
4. Clear step-by-step instructions → lower effort (even if detailed)

IMPORTANT: A detailed issue with clear instructions is NOT automatically high effort. If the path is clear, use MEDIUM or LOW regardless of description length.

BIAS: When uncertain between MEDIUM and HIGH, prefer MEDIUM. Most tasks perform better with MEDIUM effort in memory-constrained environments. Only use HIGH for tasks with genuine security risks or multi-system coordination.

Respond with ONLY a JSON object (no markdown, no explanation):
{"effort": "low|medium|high", "reason": "brief one-sentence explanation"}`

// NewEffortClassifier creates a classifier that uses `claude --print` subprocess.
// Uses the user's existing Claude Code subscription - no separate API key needed.
func NewEffortClassifier() *EffortClassifier {
	c := &EffortClassifier{
		model:   "claude-haiku-4-5-20251001",
		apiURL:  "https://api.anthropic.com/v1/messages",
		timeout: 30 * time.Second,
		log:     logging.WithComponent("effort-classifier"),
		cache:   make(map[string]string),
	}
	c.cmdRunner = c.defaultCmdRunner

	// Auto-detect API key from environment for direct API mode
	// PILOT_CLASSIFIER_API_KEY is checked first — dedicated key for classifier only.
	// Falls back to ANTHROPIC_API_KEY, then OAuth token.
	// IMPORTANT: Don't pass ANTHROPIC_API_KEY to container if CC should use OAuth
	// subscription — CC auto-detects ANTHROPIC_API_KEY and bills to it instead.
	for _, key := range []string{"PILOT_CLASSIFIER_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if v := os.Getenv(key); v != "" {
			c.apiKey = v
			c.log.Info("Effort classifier using direct API mode", slog.String("auth_source", key))
			break
		}
	}

	return c
}

// defaultCmdRunner executes the claude command.
func (c *EffortClassifier) defaultCmdRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	return cmd.Output()
}

// newEffortClassifierWithRunner creates a classifier with a custom command runner for testing.
func newEffortClassifierWithRunner(runner func(ctx context.Context, args ...string) ([]byte, error)) *EffortClassifier {
	c := NewEffortClassifier()
	c.cmdRunner = runner
	c.apiKey = "" // force subprocess path for tests regardless of ambient env
	return c
}

// SetUseStructuredOutput configures whether to use Claude Code's --json-schema structured output.
func (c *EffortClassifier) SetUseStructuredOutput(enabled bool) {
	c.useStructuredOutput = enabled
}

// Classify determines task effort level using LLM.
// Tries direct API call first (lightweight), falls back to subprocess.
// Returns cached result if available for the given task ID.
// Returns empty string on failure (allows fallback to static mapping).
func (c *EffortClassifier) Classify(ctx context.Context, task *Task) string {
	if task == nil {
		return ""
	}

	// Check cache first (prevents re-classification on retry)
	if task.ID != "" {
		c.mu.Lock()
		if cached, ok := c.cache[task.ID]; ok {
			c.mu.Unlock()
			c.log.Debug("using cached effort", slog.String("task_id", task.ID), slog.String("effort", cached))
			return cached
		}
		c.mu.Unlock()
	}

	// Try direct API call first (lightweight, ~1MB vs ~300MB subprocess)
	var result string
	var err error
	if c.apiKey != "" {
		result, err = c.classifyViaAPI(ctx, task)
		if err != nil {
			c.log.Warn("Direct API classification failed, trying subprocess",
				slog.String("task_id", task.ID),
				slog.Any("error", err),
			)
			// Fall through to subprocess
			result, err = c.classifyViaSubprocess(ctx, task)
		}
	} else {
		result, err = c.classifyViaSubprocess(ctx, task)
	}

	if err != nil {
		c.log.Warn("LLM effort classification failed, falling back to static mapping",
			slog.String("task_id", task.ID),
			slog.Any("error", err),
		)
		return "" // Empty string signals fallback
	}

	// Cache result
	if task.ID != "" {
		c.mu.Lock()
		c.cache[task.ID] = result
		c.mu.Unlock()
	}

	c.log.Info("LLM classified task effort",
		slog.String("task_id", task.ID),
		slog.String("effort", result),
	)

	return result
}

// anthropicMessage is the request/response format for the Anthropic Messages API.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// classifyViaAPI calls the Anthropic Messages API directly.
// Memory: ~1MB (HTTP connection + JSON) vs ~300MB (Node.js subprocess).
func (c *EffortClassifier) classifyViaAPI(ctx context.Context, task *Task) (string, error) {
	userContent := fmt.Sprintf("%s\n\n---\n\n## Issue Title\n%s\n\n## Issue Description\n%s",
		effortClassifierSystemPrompt, task.Title, task.Description)

	// Truncate
	const maxChars = 4000
	if len(userContent) > maxChars {
		userContent = userContent[:maxChars] + "\n...[truncated]"
	}

	reqBody := anthropicRequest{
		Model:     c.model,
		MaxTokens: 100,
		Messages: []anthropicMessage{
			{Role: "user", Content: userContent},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	// Try OAuth token as API key
	if strings.HasPrefix(c.apiKey, "sk-ant-") {
		req.Header.Set("x-api-key", c.apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return "", fmt.Errorf("parse API response: %w", err)
	}

	if apiResp.Error != nil {
		return "", fmt.Errorf("API error: %s: %s", apiResp.Error.Type, apiResp.Error.Message)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty API response content")
	}

	// Extract text from first content block
	text := apiResp.Content[0].Text
	return parseEffortResponse(text)
}

// classifyViaSubprocess calls Claude Code subprocess with Haiku model.
// Uses --bare flag (CC 2.1.81+) to skip hooks/LSP/plugins for lighter memory.
func (c *EffortClassifier) classifyViaSubprocess(ctx context.Context, task *Task) (string, error) {
	userContent := fmt.Sprintf("## Issue Title\n%s\n\n## Issue Description\n%s", task.Title, task.Description)

	// Truncate to avoid token overflow (description can be very long)
	const maxChars = 4000
	if len(userContent) > maxChars {
		userContent = userContent[:maxChars] + "\n...[truncated]"
	}

	// Build prompt with system instructions embedded
	prompt := fmt.Sprintf("%s\n\n---\n\n%s", effortClassifierSystemPrompt, userContent)

	// Add timeout to context
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Call claude --print --bare with Haiku model
	// --bare (CC 2.1.81+): skips hooks, LSP, plugin sync = lighter memory
	var args []string
	if c.useStructuredOutput {
		args = []string{
			"--print",
			// "--bare", // requires CC 2.1.81+
			"-p", prompt,
			"--model", c.model,
			"--output-format", "json",
			"--json-schema", EffortSchema,
		}
	} else {
		args = []string{
			"--print",
			// "--bare", // requires CC 2.1.81+
			"-p", prompt,
			"--model", c.model,
			"--output-format", "text",
		}
	}

	output, err := c.cmdRunner(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("claude command failed: %w", err)
	}

	if len(output) == 0 {
		return "", fmt.Errorf("empty response from claude")
	}

	if c.useStructuredOutput {
		return parseStructuredEffortResponse(output)
	} else {
		return parseEffortResponse(string(output))
	}
}

// parseEffortResponse extracts effort level from the LLM's JSON response.
func parseEffortResponse(text string) (string, error) {
	// Strip any markdown code fence wrapper
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	var resp effortClassificationResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return "", fmt.Errorf("parse effort JSON: %w (raw: %s)", err, text)
	}

	effort := strings.ToLower(resp.Effort)
	switch effort {
	case "low", "medium", "high":
		return effort, nil
	default:
		return "", fmt.Errorf("unknown effort level: %q", resp.Effort)
	}
}

// parseStructuredEffortResponse extracts effort level from Claude Code's structured JSON output.
func parseStructuredEffortResponse(jsonResponse []byte) (string, error) {
	structuredOutput, err := extractStructuredOutput(jsonResponse)
	if err != nil {
		return "", fmt.Errorf("extract structured output: %w", err)
	}

	var resp effortClassificationResponse
	if err := json.Unmarshal(structuredOutput, &resp); err != nil {
		return "", fmt.Errorf("parse structured effort: %w", err)
	}

	effort := strings.ToLower(resp.Effort)
	switch effort {
	case "low", "medium", "high":
		return effort, nil
	default:
		return "", fmt.Errorf("unknown effort level: %q", resp.Effort)
	}
}
