// Package executor: engine.go — Pilot's single execution engine, targeting
// OpenRouter's OpenAI-compatible /chat/completions endpoint.
//
// Architecture (see .agent/system/openrouter-quirks.md for rationale):
//   - Single auth surface: OPENROUTER_API_KEY
//   - Multi-provider model access via slugs (anthropic/*, openai/*, google/*, ...)
//   - OpenAI-shape request/response — tools, streaming, usage, all standardized
//   - Anthropic cache_control sent on content blocks; engages with ttl:"1h" when
//     the upstream path honors it (BYOK or sticky routing)
//   - Reasoning: {effort} field instead of Anthropic's thinking.budget_tokens —
//     OR maps to upstream-specific format on the wire
//
// This replaces backend_anthropic.go (delete in TASK-315) plus every other
// backend in factory.go. The Engine type is the only Backend Pilot ships.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// BackendTypeOpenRouter is the factory key for the OpenRouter engine.
	BackendTypeOpenRouter = "openrouter"

	openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"
	openRouterChatPath       = "/chat/completions"

	// Default attribution headers (OR uses these for app leaderboards + analytics).
	openRouterDefaultReferer = "https://github.com/qf-studio/pilot"
	openRouterDefaultTitle   = "pilot"

	// Default model when neither opts.Model nor BackendConfig overrides supply one.
	openRouterDefaultModel = "anthropic/claude-opus-4.7"

	// Engine loop limits.
	engineMaxTurns       = 60
	engineBashTimeout    = 120     // seconds per bash command
	engineMaxRetries     = 5       // API retry attempts
	engineOutputCap      = 50000   // bytes, engineTruncate tool output to prevent context bloat
	engineContextPruneAt = 150000  // estimated tokens before pruning
	engineMaxOutputToksDefault = 12000 // default max_tokens cap in chat completion request
	engineCacheTTL       = "1h"    // long TTL for agent loops that idle on tool waits

	// Effort levels map to OR's `reasoning.effort` field (low|medium|high).
	engineEffortLow    = "low"
	engineEffortMedium = "medium"
	engineEffortHigh   = "high"
)

// engineRetryBackoffs is the exponential backoff schedule for 429/529/5xx.
var engineRetryBackoffs = []time.Duration{
	30 * time.Second,
	60 * time.Second,
	90 * time.Second,
	120 * time.Second,
	180 * time.Second,
}

// Engine is Pilot's single Backend implementation. It speaks to OpenRouter
// over HTTPS, streams SSE responses, runs a tool-use loop, and reports cost
// via OR's `usage.cost` field.
type Engine struct {
	apiKey         string
	baseURL        string
	referer        string
	title          string
	config         *BackendConfig
	httpClient     *http.Client
	maxOutputToks  int // override for engineMaxOutputToksDefault; 0 = use default
}

// NewEngine constructs an Engine from BackendConfig. The OPENROUTER_API_KEY
// environment variable is the primary credential source; BackendConfig.APIAuthToken
// is honored as a config-level override (useful for testing).
func NewEngine(config *BackendConfig) *Engine {
	e := &Engine{
		config:     config,
		baseURL:    openRouterDefaultBaseURL,
		referer:    openRouterDefaultReferer,
		title:      openRouterDefaultTitle,
		httpClient: &http.Client{Timeout: 0}, // No timeout — streams can run long
	}

	// Override base URL from config (e.g. testing against a mock).
	if config != nil && config.APIBaseURL != "" {
		e.baseURL = strings.TrimRight(config.APIBaseURL, "/")
	}

	// Resolve API key. Priority: explicit config token → env var.
	if config != nil && config.APIAuthToken != "" {
		e.apiKey = config.APIAuthToken
	}
	if e.apiKey == "" {
		e.apiKey = os.Getenv("OPENROUTER_API_KEY")
	}

	return e
}

// Name returns the backend type identifier.
func (e *Engine) Name() string { return BackendTypeOpenRouter }

// IsAvailable reports whether the engine has the credentials to make a call.
func (e *Engine) IsAvailable() bool { return e.apiKey != "" }

// --- OpenRouter request/response types (OpenAI /chat/completions shape) ---

// orContentBlock is a content item inside a message. OR accepts content as
// either a string (`content: "..."`) or an array of these blocks. Using the
// block form lets us attach cache_control to specific blocks.
type orContentBlock struct {
	Type         string          `json:"type"`                    // "text" | "image_url" (we use text only)
	Text         string          `json:"text,omitempty"`
	CacheControl *orCacheControl `json:"cache_control,omitempty"` // Anthropic passthrough
}

// orCacheControl is the cache breakpoint marker that flows through OR to
// Anthropic. ttl: "1h" extends the default 5m cache for agent loops.
type orCacheControl struct {
	Type string `json:"type"`          // "ephemeral"
	TTL  string `json:"ttl,omitempty"` // "1h" recommended for agent loops
}

// orMessage represents one message in the conversation. Content is
// json.RawMessage so we can emit either a string or a []orContentBlock.
type orMessage struct {
	Role       string          `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []orToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

// orToolDef is the OpenAI function-calling tool definition shape.
type orToolDef struct {
	Type     string         `json:"type"` // always "function"
	Function orFunctionSpec `json:"function"`
}

type orFunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// orToolCall is a tool invocation the model produced. ID flows through OR
// preserving Anthropic's native `toolu_*` identifier when the upstream is
// Anthropic.
type orToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`            // "function"
	Function orFunctionCall `json:"function"`
	Index    int            `json:"index,omitempty"` // streaming delta order
}

type orFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded args (OpenAI convention)
}

// orReasoning is OR's normalized reasoning/thinking field. For Anthropic
// upstreams this maps to thinking.budget_tokens on the wire.
type orReasoning struct {
	Effort string `json:"effort,omitempty"` // "low" | "medium" | "high"
}

// orProvider lets callers pin or constrain provider routing. By default we
// leave this nil — setting `provider.order` disables OR's sticky cache
// routing, so we only override when there's a specific reason.
type orProvider struct {
	Order             []string `json:"order,omitempty"`
	AllowFallbacks    *bool    `json:"allow_fallbacks,omitempty"`
	RequireParameters bool     `json:"require_parameters,omitempty"`
}

// orUsageInclude opts into detailed usage reporting in responses.
type orUsageInclude struct {
	Include bool `json:"include"`
}

// orRequest is the body of a /chat/completions call.
type orRequest struct {
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens,omitempty"`
	Messages   []orMessage     `json:"messages"`
	Tools      []orToolDef     `json:"tools,omitempty"`
	ToolChoice string          `json:"tool_choice,omitempty"`
	Stream     bool            `json:"stream"`
	Reasoning  *orReasoning    `json:"reasoning,omitempty"`
	Provider   *orProvider     `json:"provider,omitempty"`
	Usage      *orUsageInclude `json:"usage,omitempty"`
}

// orChoice is one completion option from the response.
type orChoice struct {
	Index              int                `json:"index"`
	Message            *orResponseMessage `json:"message,omitempty"` // non-streaming
	Delta              *orResponseMessage `json:"delta,omitempty"`   // streaming
	FinishReason       string             `json:"finish_reason,omitempty"`
	NativeFinishReason string             `json:"native_finish_reason,omitempty"`
}

type orResponseMessage struct {
	Role      string       `json:"role,omitempty"`
	Content   string       `json:"content,omitempty"`
	Refusal   string       `json:"refusal,omitempty"`
	Reasoning string       `json:"reasoning,omitempty"`
	ToolCalls []orToolCall `json:"tool_calls,omitempty"`
}

// orPromptTokensDetails carries cache hit/miss counters. cached_tokens > 0
// indicates the prompt cache engaged.
type orPromptTokensDetails struct {
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type orCompletionTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type orUsage struct {
	PromptTokens            int64                      `json:"prompt_tokens"`
	CompletionTokens        int64                      `json:"completion_tokens"`
	TotalTokens             int64                      `json:"total_tokens"`
	Cost                    float64                    `json:"cost"`
	IsByok                  bool                       `json:"is_byok"`
	PromptTokensDetails     *orPromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *orCompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type orErrorBody struct {
	Code     int             `json:"code"`
	Message  string          `json:"message"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// orResponse is the assembled response after non-streaming or end-of-stream.
type orResponse struct {
	ID       string       `json:"id"`
	Object   string       `json:"object"`
	Model    string       `json:"model"`
	Provider string       `json:"provider"`
	Choices  []orChoice   `json:"choices"`
	Usage    orUsage      `json:"usage"`
	Error    *orErrorBody `json:"error,omitempty"`
}

// --- Tool definitions ---

var engineTools = []orToolDef{
	{
		Type: "function",
		Function: orFunctionSpec{
			Name:        "bash",
			Description: "Execute a bash command. Returns stdout+stderr combined. Commands timeout after 120s by default. Use for running code, installing packages, checking files, running tests.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"command": {"type": "string", "description": "The bash command to execute"},
					"timeout": {"type": "integer", "description": "Timeout in seconds (default 120, max 600)"}
				},
				"required": ["command"]
			}`),
		},
	},
	{
		Type: "function",
		Function: orFunctionSpec{
			Name:        "read_file",
			Description: "Read a file's contents and return with line numbers prepended (e.g. '  42 | line content'). Supports offset/limit for partial reads.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path to the file"},
					"offset": {"type": "integer", "description": "Line number to start from (1-indexed)"},
					"limit": {"type": "integer", "description": "Max lines to read"}
				},
				"required": ["path"]
			}`),
		},
	},
	{
		Type: "function",
		Function: orFunctionSpec{
			Name:        "write_file",
			Description: "Write content to a file. Creates parent directories if needed. Overwrites existing files. Use for creating new files or complete rewrites.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path to write to"},
					"content": {"type": "string", "description": "File content"}
				},
				"required": ["path", "content"]
			}`),
		},
	},
	{
		Type: "function",
		Function: orFunctionSpec{
			Name:        "edit_file",
			Description: "Replace a specific string in a file. The old_string must appear exactly once. More precise than write_file for small changes.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path"},
					"old_string": {"type": "string", "description": "Exact text to find (must appear exactly once)"},
					"new_string": {"type": "string", "description": "Replacement text"}
				},
				"required": ["path", "old_string", "new_string"]
			}`),
		},
	},
}

// --- Tool execution ---

func engineExecBash(command string, timeout int, cwd string) string {
	if timeout <= 0 || timeout > 600 {
		timeout = engineBashTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()

	output := string(out)
	if len(output) > engineOutputCap {
		half := engineOutputCap / 2
		output = output[:half] + "\n\n... [truncated] ...\n\n" + output[len(output)-half:]
	}

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("%s\n[TIMEOUT after %ds — command killed]", output, timeout)
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Sprintf("%s\n[exit code: %d]", output, exitErr.ExitCode())
		}
		return fmt.Sprintf("%s\n[ERROR: %v]", output, err)
	}
	return output
}

func engineExecReadFile(path string, offset, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("[File not found: %s]", path)
	}
	if len(data) > 500000 {
		return fmt.Sprintf("[File too large: %d bytes. Use bash: head -100 %s]", len(data), path)
	}
	lines := strings.Split(string(data), "\n")
	if offset > 0 && offset <= len(lines) {
		lines = lines[offset-1:]
	}
	if limit > 0 && limit < len(lines) {
		lines = lines[:limit]
	}
	startLine := 1
	if offset > 0 {
		startLine = offset
	}
	var sb strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&sb, "%4d | %s\n", startLine+i, line)
	}
	return sb.String()
}

func engineExecWriteFile(path, content string) string {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Sprintf("[ERROR creating dirs: %v]", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Sprintf("[ERROR writing %s: %v]", path, err)
	}
	return fmt.Sprintf("[Wrote %d bytes to %s]", len(content), path)
}

func engineExecEditFile(path, oldStr, newStr string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("[File not found: %s]", path)
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return fmt.Sprintf("[old_string not found in %s — read the file first to get exact text]", path)
	}
	if count > 1 {
		return fmt.Sprintf("[old_string appears %d times in %s — provide more context to make it unique]", count, path)
	}
	newContent := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("[ERROR writing %s: %v]", path, err)
	}
	return fmt.Sprintf("[Edited %s: replaced %d chars with %d chars]", path, len(oldStr), len(newStr))
}

// engineDispatchTool routes a tool call to the right executor. Returns the
// string result that gets sent back as a tool_result message.
func engineDispatchTool(name string, args map[string]interface{}, cwd string) string {
	switch name {
	case "bash":
		cmd, _ := args["command"].(string)
		timeout := engineBashTimeout
		if t, ok := args["timeout"].(float64); ok {
			timeout = int(t)
		}
		return engineExecBash(cmd, timeout, cwd)
	case "read_file":
		path, _ := args["path"].(string)
		offset, _ := args["offset"].(float64)
		limit, _ := args["limit"].(float64)
		return engineExecReadFile(path, int(offset), int(limit))
	case "write_file":
		path, _ := args["path"].(string)
		content, _ := args["content"].(string)
		return engineExecWriteFile(path, content)
	case "edit_file":
		path, _ := args["path"].(string)
		oldStr, _ := args["old_string"].(string)
		newStr, _ := args["new_string"].(string)
		return engineExecEditFile(path, oldStr, newStr)
	default:
		return fmt.Sprintf("[Unknown tool: %s]", name)
	}
}

// --- HTTP call with retry + SSE parsing ---

// callAPI POSTs the request and parses the SSE stream into a unified response.
// Retries on 429/529/5xx with exponential backoff.
func (e *Engine) callAPI(ctx context.Context, req *orRequest) (*orResponse, error) {
	req.Stream = true
	if req.Usage == nil {
		req.Usage = &orUsageInclude{Include: true}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := e.baseURL + openRouterChatPath

	for attempt := 0; attempt <= engineMaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if e.referer != "" {
			httpReq.Header.Set("HTTP-Referer", e.referer)
		}
		if e.title != "" {
			httpReq.Header.Set("X-OpenRouter-Title", e.title)
		}

		resp, err := e.httpClient.Do(httpReq)
		if err != nil {
			if attempt < engineMaxRetries {
				wait := engineRetryBackoffs[min(attempt, len(engineRetryBackoffs)-1)]
				slog.Warn("engine: HTTP error, retrying",
					slog.Int("attempt", attempt+1), slog.Duration("wait", wait), slog.Any("error", err))
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("HTTP request failed after %d retries: %w", engineMaxRetries, err)
		}

		// Retry on 429/529/5xx.
		if resp.StatusCode == 429 || resp.StatusCode == 529 || resp.StatusCode >= 500 {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if attempt < engineMaxRetries {
				wait := engineRetryBackoffs[min(attempt, len(engineRetryBackoffs)-1)]
				// Honor retry_after_seconds from OR's 429 error body if present.
				if resp.StatusCode == 429 {
					if ra := extractRetryAfter(respBody); ra > 0 {
						wait = time.Duration(ra) * time.Second
					}
				}
				slog.Warn("engine: API error, retrying",
					slog.Int("status", resp.StatusCode), slog.Duration("wait", wait))
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("API returned %d after %d retries: %s", resp.StatusCode, engineMaxRetries, engineTruncate(string(respBody), 500))
		}

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, engineTruncate(string(respBody), 500))
		}

		result, err := e.parseSSEStream(resp.Body)
		_ = resp.Body.Close()

		if err != nil {
			// Some upstreams emit overloaded errors in the SSE stream body even at HTTP 200.
			if strings.Contains(strings.ToLower(err.Error()), "overloaded") && attempt < engineMaxRetries {
				wait := engineRetryBackoffs[min(attempt, len(engineRetryBackoffs)-1)]
				slog.Warn("engine: overloaded in stream, retrying", slog.Duration("wait", wait))
				time.Sleep(wait)
				continue
			}
			return nil, err
		}

		return result, nil
	}

	return nil, fmt.Errorf("exhausted %d retries", engineMaxRetries)
}

// extractRetryAfter looks for OR's `metadata.retry_after_seconds` in a 429 body.
// Returns 0 if not parseable.
func extractRetryAfter(body []byte) int {
	var envelope struct {
		Error struct {
			Metadata struct {
				RetryAfter int `json:"retry_after_seconds"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0
	}
	return envelope.Error.Metadata.RetryAfter
}

func engineTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseSSEStream reads OR's SSE event stream and accumulates the final
// orResponse. Skips `: OPENROUTER PROCESSING` heartbeats. Terminates on
// `data: [DONE]`. The final data chunk carries the usage block.
func (e *Engine) parseSSEStream(body io.Reader) (*orResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer

	result := &orResponse{}
	// Accumulate delta content per choice index.
	contentBuilders := make(map[int]*strings.Builder)
	// Accumulate tool-call argument JSON per (choice, tool-call) index.
	toolCallArgBuilders := make(map[string]*strings.Builder) // key: "choice:index"
	toolCallMeta := make(map[string]*orToolCall)             // key: "choice:index"

	for scanner.Scan() {
		line := scanner.Text()

		// Skip blank lines and OR heartbeat comments.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk orResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Some chunks (especially errors) may not parse cleanly; skip.
			continue
		}

		// Error in stream body (HTTP 200, error inside JSON).
		if chunk.Error != nil && chunk.Error.Message != "" {
			return nil, fmt.Errorf("stream error: %s", chunk.Error.Message)
		}

		// Capture top-level metadata from the first chunk that has them.
		if chunk.ID != "" && result.ID == "" {
			result.ID = chunk.ID
		}
		if chunk.Model != "" && result.Model == "" {
			result.Model = chunk.Model
		}
		if chunk.Provider != "" && result.Provider == "" {
			result.Provider = chunk.Provider
		}

		for _, choice := range chunk.Choices {
			idx := choice.Index
			// Ensure result.Choices is grown to fit this index.
			for len(result.Choices) <= idx {
				result.Choices = append(result.Choices, orChoice{Index: len(result.Choices)})
			}
			rc := &result.Choices[idx]

			if choice.FinishReason != "" {
				rc.FinishReason = choice.FinishReason
			}
			if choice.NativeFinishReason != "" {
				rc.NativeFinishReason = choice.NativeFinishReason
			}

			if choice.Delta == nil {
				continue
			}

			// Accumulate text content.
			if choice.Delta.Content != "" {
				cb, ok := contentBuilders[idx]
				if !ok {
					cb = &strings.Builder{}
					contentBuilders[idx] = cb
				}
				cb.WriteString(choice.Delta.Content)
			}

			// Accumulate tool calls.
			for _, tc := range choice.Delta.ToolCalls {
				key := fmt.Sprintf("%d:%d", idx, tc.Index)
				meta, ok := toolCallMeta[key]
				if !ok {
					meta = &orToolCall{Index: tc.Index, Type: "function"}
					toolCallMeta[key] = meta
					toolCallArgBuilders[key] = &strings.Builder{}
				}
				if tc.ID != "" {
					meta.ID = tc.ID
				}
				if tc.Type != "" {
					meta.Type = tc.Type
				}
				if tc.Function.Name != "" {
					meta.Function.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					toolCallArgBuilders[key].WriteString(tc.Function.Arguments)
				}
			}
		}

		// Final usage block comes on the last data chunk (OR convention).
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 || chunk.Usage.Cost > 0 {
			result.Usage = chunk.Usage
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	// Materialize each choice's final message from the accumulated deltas.
	for i := range result.Choices {
		msg := &orResponseMessage{Role: "assistant"}
		if cb, ok := contentBuilders[i]; ok {
			msg.Content = cb.String()
		}
		// Walk our tool-call map in index order to keep the result deterministic.
		for key, meta := range toolCallMeta {
			var ci, ti int
			if _, err := fmt.Sscanf(key, "%d:%d", &ci, &ti); err != nil || ci != i {
				continue
			}
			tc := *meta
			if ab, ok := toolCallArgBuilders[key]; ok {
				tc.Function.Arguments = ab.String()
			}
			msg.ToolCalls = append(msg.ToolCalls, tc)
		}
		result.Choices[i].Message = msg
	}

	return result, nil
}

// --- Engine Execute loop ---

// Execute runs the agent loop against OpenRouter. Streams BackendEvents to
// the caller's handler. Returns a BackendResult with final tokens, cost,
// success state.
func (e *Engine) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if e.apiKey == "" {
		return nil, fmt.Errorf("engine: no OPENROUTER_API_KEY configured")
	}

	model := opts.Model
	if model == "" {
		if e.config != nil && e.config.DefaultModel != "" {
			model = e.config.DefaultModel
		} else {
			model = openRouterDefaultModel
		}
	}

	cwd := opts.ProjectPath
	if cwd == "" {
		cwd = "/app"
	}

	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:    EventTypeInit,
			Message: "OpenRouter engine initialized",
			Model:   model,
		})
	}

	// Initial messages: system carries the prompt with cache_control; user
	// kicks off the loop. cache_control on the system block lets a properly
	// configured upstream cache the entire prompt prefix.
	systemBlocks := []orContentBlock{
		{
			Type:         "text",
			Text:         opts.Prompt,
			CacheControl: &orCacheControl{Type: "ephemeral", TTL: engineCacheTTL},
		},
	}
	systemContent, _ := json.Marshal(systemBlocks)
	userContent, _ := json.Marshal("Begin. Follow the mandatory workflow.")

	messages := []orMessage{
		{Role: "system", Content: systemContent},
		{Role: "user", Content: userContent},
	}

	var (
		totalInputTokens  int64
		totalOutputTokens int64
		totalCacheRead    int64
		totalCacheWrite   int64
		totalCost         float64
		lastOutput        string
		sawSuccess        bool
		cachedTokensZeros int // count turns with cached=0; warn after threshold
	)

	for turn := 0; turn < engineMaxTurns; turn++ {
		if ctx.Err() != nil {
			return &BackendResult{
				Success:                  sawSuccess,
				Output:                   lastOutput,
				Error:                    "context cancelled",
				TokensInput:              totalInputTokens,
				TokensOutput:             totalOutputTokens,
				CacheReadInputTokens:     totalCacheRead,
				CacheCreationInputTokens: totalCacheWrite,
				Model:                    model,
				SawSuccessResult:         sawSuccess,
			}, nil
		}

		// Context pruning by rough token estimate.
		if engineEstimateTokens(messages) > engineContextPruneAt {
			messages = enginePruneMessages(messages)
			slog.Info("engine: context pruned", slog.Int("turn", turn))
		}

		// Progressive effort: high for first 8 turns, medium after. Overridden
		// by opts.Effort.
		effort := engineEffortHigh
		if turn >= 8 {
			effort = engineEffortMedium
		}
		switch opts.Effort {
		case "low":
			effort = engineEffortLow
		case "medium":
			effort = engineEffortMedium
		case "high", "max":
			effort = engineEffortHigh
		}

		maxToks := e.maxOutputToks
		if maxToks <= 0 {
			maxToks = engineMaxOutputToksDefault
		}

		req := &orRequest{
			Model:     model,
			MaxTokens: maxToks,
			Messages:  messages,
			Tools:     engineTools,
			Reasoning: &orReasoning{Effort: effort},
		}

		slog.Info("engine: API call",
			slog.Int("turn", turn),
			slog.String("model", model),
			slog.String("effort", effort),
			slog.Int("messages", len(messages)))

		response, err := e.callAPI(ctx, req)
		if err != nil {
			slog.Error("engine: API call failed", slog.Int("turn", turn), slog.Any("error", err))
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{Type: EventTypeError, Message: err.Error(), IsError: true})
			}
			return &BackendResult{
				Success:                  sawSuccess,
				Output:                   lastOutput,
				Error:                    err.Error(),
				TokensInput:              totalInputTokens,
				TokensOutput:             totalOutputTokens,
				CacheReadInputTokens:     totalCacheRead,
				CacheCreationInputTokens: totalCacheWrite,
				Model:                    model,
				SawSuccessResult:         sawSuccess,
				ErrorType:                "api_error",
			}, nil
		}

		// Accounting.
		totalInputTokens += response.Usage.PromptTokens
		totalOutputTokens += response.Usage.CompletionTokens
		totalCost += response.Usage.Cost
		if response.Usage.PromptTokensDetails != nil {
			totalCacheRead += response.Usage.PromptTokensDetails.CachedTokens
			totalCacheWrite += response.Usage.PromptTokensDetails.CacheWriteTokens
			if response.Usage.PromptTokensDetails.CachedTokens == 0 {
				cachedTokensZeros++
			} else {
				cachedTokensZeros = 0
			}
		}

		// One-time nudge if cache never engages (suggests user hasn't set up
		// BYOK or pool path doesn't cache for them).
		if cachedTokensZeros == 5 {
			slog.Warn("engine: prompt cache has not engaged after 5 turns; consider adding an Anthropic key under OpenRouter Integrations for cache passthrough",
				slog.String("model", model))
		}

		if len(response.Choices) == 0 {
			err := fmt.Errorf("engine: empty choices in response (id=%s)", response.ID)
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{Type: EventTypeError, Message: err.Error(), IsError: true})
			}
			return &BackendResult{
				Success:          sawSuccess,
				Output:           lastOutput,
				Error:            err.Error(),
				TokensInput:      totalInputTokens,
				TokensOutput:     totalOutputTokens,
				Model:            model,
				SawSuccessResult: sawSuccess,
				ErrorType:        "api_error",
			}, nil
		}

		choice := response.Choices[0]
		msg := choice.Message
		if msg == nil {
			msg = &orResponseMessage{Role: "assistant"}
		}

		// Emit text event for the assistant's prose.
		if msg.Content != "" {
			lastOutput = msg.Content
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:                 EventTypeText,
					Message:              msg.Content,
					TokensInput:          response.Usage.PromptTokens,
					TokensOutput:         response.Usage.CompletionTokens,
					CacheReadInputTokens: cacheReadFromUsage(response.Usage),
					Model:                model,
				})
			}
		}

		// Append the assistant message to history.
		assistantMsg := orMessage{Role: "assistant"}
		if msg.Content != "" {
			c, _ := json.Marshal(msg.Content)
			assistantMsg.Content = c
		}
		if len(msg.ToolCalls) > 0 {
			assistantMsg.ToolCalls = msg.ToolCalls
		}
		messages = append(messages, assistantMsg)

		// Tool calls? Execute them and append tool messages.
		if choice.FinishReason == "tool_calls" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				var args map[string]interface{}
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				}
				if args == nil {
					args = map[string]interface{}{}
				}

				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:      EventTypeToolUse,
						ToolName:  tc.Function.Name,
						ToolInput: args,
						Model:     model,
					})
				}

				slog.Info("engine: tool exec",
					slog.Int("turn", turn),
					slog.String("tool", tc.Function.Name))

				result := engineDispatchTool(tc.Function.Name, args, cwd)

				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:       EventTypeToolResult,
						ToolName:   tc.Function.Name,
						ToolResult: result,
					})
				}

				toolContent, _ := json.Marshal(result)
				messages = append(messages, orMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    toolContent,
				})
			}
			continue // next turn
		}

		// finish_reason: "stop" (or anything other than tool_calls) → done
		sawSuccess = true
		if opts.EventHandler != nil {
			opts.EventHandler(BackendEvent{Type: EventTypeResult, Message: lastOutput})
		}
		break
	}

	slog.Info("engine: done",
		slog.Bool("success", sawSuccess),
		slog.Int64("tokens_in", totalInputTokens),
		slog.Int64("tokens_out", totalOutputTokens),
		slog.Int64("cache_read", totalCacheRead),
		slog.Int64("cache_write", totalCacheWrite),
		slog.Float64("cost_usd", totalCost),
		slog.String("model", model))

	return &BackendResult{
		Success:                  sawSuccess,
		Output:                   lastOutput,
		TokensInput:              totalInputTokens,
		TokensOutput:             totalOutputTokens,
		CacheReadInputTokens:     totalCacheRead,
		CacheCreationInputTokens: totalCacheWrite,
		Model:                    model,
		SawSuccessResult:         sawSuccess,
	}, nil
}

func cacheReadFromUsage(u orUsage) int64 {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// --- Context management ---

func engineEstimateTokens(messages []orMessage) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) / 4
			total += len(tc.Function.Name) / 4
		}
	}
	return total
}

func enginePruneMessages(messages []orMessage) []orMessage {
	keepTurns := 12
	if len(messages) <= keepTurns*2 {
		return messages
	}
	first := messages[0]
	keep := messages[len(messages)-keepTurns*2:]
	prunedMarker, _ := json.Marshal("[Earlier messages pruned to save context. Continue working on the task.]")
	pruned := []orMessage{
		first,
		{Role: "user", Content: prunedMarker},
	}
	return append(pruned, keep...)
}

// --- Error type ---

// EngineError implements BackendError for engine failures.
type EngineError struct {
	ErrType string
	Msg     string
}

func (e *EngineError) Error() string        { return e.Msg }
func (e *EngineError) ErrorType() string    { return e.ErrType }
func (e *EngineError) ErrorMessage() string { return e.Msg }
func (e *EngineError) ErrorStderr() string  { return "" }
