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
	"strings"
	"time"
)

const (
	BackendTypeOpenAIAPI = "openai-api"
	openaiDefaultBaseURL = "https://api.openai.com/v1"
	openaiDefaultModel   = "gpt-4o"
)

// OpenAIBackend implements Backend using direct OpenAI-compatible /v1/chat/completions API.
// Supports OpenAI, OpenRouter, Groq, Together, Synthetic, vLLM, Ollama, and any provider
// exposing an OpenAI-compatible endpoint — without a CLI wrapper.
type OpenAIBackend struct {
	apiKey     string
	model      string
	apiURL     string // full endpoint URL
	config     *BackendConfig
	retryWaits []time.Duration // nil = use hardcoded defaults; overridden in tests
}

// NewOpenAIBackend creates a new OpenAI-compatible direct HTTP backend.
func NewOpenAIBackend(config *BackendConfig) *OpenAIBackend {
	b := &OpenAIBackend{config: config}

	// Resolve base URL
	baseURL := openaiDefaultBaseURL
	if config != nil && config.OpenAI != nil && config.OpenAI.BaseURL != "" {
		baseURL = strings.TrimRight(config.OpenAI.BaseURL, "/")
	}
	b.apiURL = baseURL + "/chat/completions"

	// Resolve model
	b.model = openaiDefaultModel
	if config != nil && config.OpenAI != nil && config.OpenAI.Model != "" {
		b.model = config.OpenAI.Model
	} else if config != nil && config.DefaultModel != "" {
		b.model = config.DefaultModel
	}

	// Resolve API key: config takes priority over env vars
	if config != nil && config.OpenAI != nil && config.OpenAI.APIKey != "" {
		b.apiKey = config.OpenAI.APIKey
	} else if config != nil && config.APIAuthToken != "" {
		b.apiKey = config.APIAuthToken
	} else {
		for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "PILOT_ENGINE_API_KEY"} {
			if v := os.Getenv(key); v != "" {
				b.apiKey = v
				break
			}
		}
	}

	return b
}

func (b *OpenAIBackend) Name() string      { return BackendTypeOpenAIAPI }
func (b *OpenAIBackend) IsAvailable() bool { return b.apiKey != "" }

// --- OpenAI API Types ---

// openaiMsg is the wire format for a single conversation turn.
type openaiMsg struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"` // nil serialises as null (for tool-call-only turns)
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"` // "function"
	Function openaiCallFunc `json:"function"`
}

// openaiCallFunc carries the resolved call (name + argument JSON string).
type openaiCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // complete JSON string
}

// openaiToolDef describes a tool in the request.
type openaiToolDef struct {
	Type     string        `json:"type"` // "function"
	Function openaiFuncDef `json:"function"`
}

type openaiFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openaiRequest struct {
	Model    string          `json:"model"`
	Messages []openaiMsg     `json:"messages"`
	Tools    []openaiToolDef `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}

// SSE streaming types

type openaiChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []openaiChoice `json:"choices"`
	Usage   *openaiUsage   `json:"usage,omitempty"`
}

type openaiChoice struct {
	Index        int         `json:"index"`
	Delta        openaiDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"` // pointer — null vs "stop"/"tool_calls"
}

type openaiDelta struct {
	Role      string                `json:"role,omitempty"`
	Content   string                `json:"content,omitempty"`
	ToolCalls []openaiDeltaToolCall `json:"tool_calls,omitempty"`
}

type openaiDeltaToolCall struct {
	Index    int             `json:"index"`
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type,omitempty"`
	Function openaiDeltaFunc `json:"function"`
}

type openaiDeltaFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // fragment
}

type openaiUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// openaiAccum collects the fully parsed result of one SSE stream.
type openaiAccum struct {
	Model            string
	Content          string
	ToolCalls        []openaiToolCall
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64
}

// pendingOAIToolCall accumulates fragmented streaming tool-call data.
type pendingOAIToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

// --- Tool Definitions (OpenAI function-call format) ---

var openaiTools = []openaiToolDef{
	{
		Type: "function",
		Function: openaiFuncDef{
			Name:        "bash",
			Description: "Execute a bash command. Returns stdout+stderr. Commands timeout after 120s.",
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
		Function: openaiFuncDef{
			Name:        "read_file",
			Description: "Read a file's contents with line numbers.",
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
		Function: openaiFuncDef{
			Name:        "write_file",
			Description: "Write content to a file. Creates parent directories. Overwrites existing.",
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
		Function: openaiFuncDef{
			Name:        "edit_file",
			Description: "Replace a specific string in a file. old_string must appear exactly once.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Absolute path"},
					"old_string": {"type": "string", "description": "Exact text to find"},
					"new_string": {"type": "string", "description": "Replacement text"}
				},
				"required": ["path", "old_string", "new_string"]
			}`),
		},
	},
}

// --- API Call with Streaming ---

func (b *OpenAIBackend) callAPI(ctx context.Context, req *openaiRequest) (*openaiAccum, error) {
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	backoffs := b.retryWaits
	if backoffs == nil {
		backoffs = []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second, 120 * time.Second, 180 * time.Second}
	}

	for attempt := 0; attempt <= apiMaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", b.apiURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			if attempt < apiMaxRetries {
				slog.Warn("HTTP error, retrying", slog.Int("attempt", attempt+1), slog.Any("error", err))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoffs[min(attempt, len(backoffs)-1)]):
				}
				continue
			}
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			if attempt < apiMaxRetries {
				wait := backoffs[min(attempt, len(backoffs)-1)]
				slog.Warn("API error, retrying", slog.Int("status", resp.StatusCode), slog.Duration("wait", wait))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return nil, fmt.Errorf("API returned %d after %d retries", resp.StatusCode, apiMaxRetries)
		}

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
		}

		result, err := b.parseSSEStream(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	return nil, fmt.Errorf("exhausted %d retries", apiMaxRetries)
}

// parseSSEStream reads an OpenAI SSE stream and accumulates the full response.
// Tool call argument fragments are joined by index before JSON-parsing.
func (b *OpenAIBackend) parseSSEStream(body io.Reader) (*openaiAccum, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var result openaiAccum
	var contentBuf strings.Builder
	pending := make(map[int]*pendingOAIToolCall)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk openaiChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if result.Model == "" && chunk.Model != "" {
			result.Model = chunk.Model
		}

		if chunk.Usage != nil {
			result.PromptTokens = chunk.Usage.PromptTokens
			result.CompletionTokens = chunk.Usage.CompletionTokens
		}

		for _, choice := range chunk.Choices {
			// Accumulate text
			if choice.Delta.Content != "" {
				contentBuf.WriteString(choice.Delta.Content)
			}

			// Accumulate tool call fragments by index
			for _, dtc := range choice.Delta.ToolCalls {
				p, ok := pending[dtc.Index]
				if !ok {
					p = &pendingOAIToolCall{}
					pending[dtc.Index] = p
				}
				if dtc.ID != "" {
					p.id = dtc.ID
				}
				if dtc.Function.Name != "" {
					p.name = dtc.Function.Name
				}
				p.arguments.WriteString(dtc.Function.Arguments)
			}

			if choice.FinishReason != nil && *choice.FinishReason != "" {
				result.FinishReason = *choice.FinishReason
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = contentBuf.String()

	// Finalize tool calls in index order
	for i := 0; i < len(pending); i++ {
		p, ok := pending[i]
		if !ok {
			break
		}
		result.ToolCalls = append(result.ToolCalls, openaiToolCall{
			ID:   p.id,
			Type: "function",
			Function: openaiCallFunc{
				Name:      p.name,
				Arguments: p.arguments.String(),
			},
		})
	}

	return &result, nil
}

// --- Main Execute Loop ---

func (b *OpenAIBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if b.apiKey == "" {
		return nil, fmt.Errorf("no API key configured for openai-api backend (set OPENAI_API_KEY or executor.openai.api_key)")
	}

	model := opts.Model
	if model == "" {
		model = b.model
	}

	cwd := opts.ProjectPath
	if cwd == "" {
		cwd = "/app"
	}

	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:    EventTypeInit,
			Message: "OpenAI-compatible API backend initialized",
			Model:   model,
		})
	}

	// Build conversation: system prompt + initial user kickoff
	content := opts.Prompt
	beginContent := "Begin. Follow the mandatory workflow."
	messages := []openaiMsg{
		{Role: "system", Content: &content},
		{Role: "user", Content: &beginContent},
	}

	var totalPromptTokens, totalCompletionTokens int64
	var lastOutput string
	var sawSuccess bool

	for turn := 0; turn < apiMaxTurns; turn++ {
		if ctx.Err() != nil {
			return &BackendResult{
				Success:          sawSuccess,
				Output:           lastOutput,
				Error:            "context cancelled",
				TokensInput:      totalPromptTokens,
				TokensOutput:     totalCompletionTokens,
				Model:            model,
				SawSuccessResult: sawSuccess,
			}, nil
		}

		req := &openaiRequest{
			Model:    model,
			Messages: messages,
			Tools:    openaiTools,
		}

		slog.Info("OpenAI API call", slog.Int("turn", turn), slog.String("model", model),
			slog.Int("messages", len(messages)))

		accum, err := b.callAPI(ctx, req)
		if err != nil {
			slog.Error("OpenAI API call failed", slog.Int("turn", turn), slog.Any("error", err))

			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:    EventTypeError,
					Message: err.Error(),
					IsError: true,
				})
			}

			return &BackendResult{
				Success:          sawSuccess,
				Output:           lastOutput,
				Error:            err.Error(),
				TokensInput:      totalPromptTokens,
				TokensOutput:     totalCompletionTokens,
				Model:            model,
				SawSuccessResult: sawSuccess,
			}, nil
		}

		totalPromptTokens += accum.PromptTokens
		totalCompletionTokens += accum.CompletionTokens

		// Emit text event
		if accum.Content != "" {
			lastOutput = accum.Content
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:         EventTypeText,
					Message:      accum.Content,
					TokensInput:  accum.PromptTokens,
					TokensOutput: accum.CompletionTokens,
					Model:        model,
				})
			}
		}

		// Build assistant message for conversation history
		assistantMsg := openaiMsg{Role: "assistant"}
		if accum.Content != "" {
			c := accum.Content
			assistantMsg.Content = &c
		}
		// nil Content serialises as null — required for tool-call-only turns
		if len(accum.ToolCalls) > 0 {
			assistantMsg.ToolCalls = accum.ToolCalls
		}
		messages = append(messages, assistantMsg)

		// Process tool calls
		if accum.FinishReason == "tool_calls" && len(accum.ToolCalls) > 0 {
			for _, tc := range accum.ToolCalls {
				var toolInput map[string]interface{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &toolInput); err != nil {
					toolInput = map[string]interface{}{"error": "failed to parse arguments: " + err.Error()}
				}

				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:      EventTypeToolUse,
						ToolName:  tc.Function.Name,
						ToolInput: toolInput,
						Model:     model,
					})
				}

				slog.Info("OpenAI tool exec", slog.Int("turn", turn), slog.String("tool", tc.Function.Name))
				toolResult := executeTool(tc.Function.Name, toolInput, cwd)

				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:       EventTypeToolResult,
						ToolName:   tc.Function.Name,
						ToolResult: toolResult,
					})
				}

				// OpenAI tool result message format
				toolResultContent := toolResult
				messages = append(messages, openaiMsg{
					Role:       "tool",
					Content:    &toolResultContent,
					ToolCallID: tc.ID,
				})
			}

		} else if accum.FinishReason == "stop" || accum.FinishReason == "" {
			// "stop" = normal completion; empty = some providers omit it
			sawSuccess = true
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:    EventTypeResult,
					Message: lastOutput,
				})
			}
			break
		}

		// Context pruning: rough estimate 4 chars = 1 token
		if estimateOpenAIContextTokens(messages) > apiContextPruneAt {
			messages = pruneOpenAIMessages(messages)
			slog.Info("OpenAI context pruned", slog.Int("turn", turn))
		}
	}

	return &BackendResult{
		Success:          sawSuccess,
		Output:           lastOutput,
		TokensInput:      totalPromptTokens,
		TokensOutput:     totalCompletionTokens,
		Model:            model,
		SawSuccessResult: sawSuccess,
	}, nil
}

// --- Context Management ---

func estimateOpenAIContextTokens(messages []openaiMsg) int {
	total := 0
	for _, msg := range messages {
		if msg.Content != nil {
			total += len(*msg.Content) / 4
		}
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

func pruneOpenAIMessages(messages []openaiMsg) []openaiMsg {
	keepTurns := 12
	// messages[0] is always the system prompt; +1 accounts for it
	if len(messages) <= keepTurns*2+1 {
		return messages
	}
	system := messages[0]
	keep := messages[len(messages)-keepTurns*2:]
	notice := "[Earlier messages pruned to save context. Continue working on the task.]"
	pruned := []openaiMsg{
		system,
		{Role: "user", Content: &notice},
	}
	return append(pruned, keep...)
}

// --- Error Type ---

// OpenAIAPIError implements BackendError for OpenAI-compatible API errors.
type OpenAIAPIError struct {
	ErrType string
	Msg     string
}

func (e *OpenAIAPIError) Error() string        { return e.Msg }
func (e *OpenAIAPIError) ErrorType() string    { return e.ErrType }
func (e *OpenAIAPIError) ErrorMessage() string { return e.Msg }
func (e *OpenAIAPIError) ErrorStderr() string  { return "" }
