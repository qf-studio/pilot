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
	BackendTypeAnthropicAPI = "anthropic-api"

	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"

	// Progressive thinking budget
	thinkingHighTurns  = 8
	thinkingHighBudget = 10000
	thinkingLowBudget  = 3000
	maxOutputTokens    = 12000

	// Limits
	apiMaxTurns       = 60
	apiBashTimeout    = 120 // seconds per bash command
	apiMaxRetries     = 5
	apiOutputCap      = 50000 // bytes, cap tool output to prevent context bloat
	apiContextPruneAt = 150000 // estimated tokens before pruning
)

// AnthropicBackend implements Backend using direct Anthropic Messages API calls.
// Replaces Claude Code CLI subprocess with HTTP streaming, giving full control
// over thinking budgets, tool dispatch, and retry logic.
type AnthropicBackend struct {
	apiKey string
	model  string
	apiURL string
	config *BackendConfig
}

// NewAnthropicBackend creates a new direct API backend.
func NewAnthropicBackend(config *BackendConfig) *AnthropicBackend {
	b := &AnthropicBackend{config: config, apiURL: anthropicAPIURL}

	if config != nil && config.APIBaseURL != "" {
		b.apiURL = config.ResolveAPIBaseURL() + "/v1/messages"
	}

	// Resolve API key (same priority as effort_classifier.go:84-95)
	for _, key := range []string{"ANTHROPIC_API_KEY", "PILOT_ENGINE_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if v := os.Getenv(key); v != "" {
			b.apiKey = v
			break
		}
	}

	return b
}

func (b *AnthropicBackend) Name() string { return BackendTypeAnthropicAPI }

func (b *AnthropicBackend) IsAvailable() bool { return b.apiKey != "" }

// --- Anthropic API Types ---

type apiMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []apiContentBlock
}

type apiContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type apiToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []apiMessage    `json:"messages"`
	Tools     []apiToolDef    `json:"tools,omitempty"`
	Stream    bool            `json:"stream"`
	Thinking  *apiThinking    `json:"thinking,omitempty"`
}

type apiThinking struct {
	Type        string `json:"type"`
	BudgetTokens int   `json:"budget_tokens"`
}

type apiResponse struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Content      []apiContentBlock `json:"content"`
	Model        string            `json:"model"`
	StopReason   string            `json:"stop_reason"`
	Usage        apiUsage          `json:"usage"`
	Error        *apiError         `json:"error,omitempty"`
}

type apiUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// SSE event for streaming
type sseEvent struct {
	Type string `json:"type"`
	// Various data fields depending on type
	Index        int             `json:"index,omitempty"`
	ContentBlock *apiContentBlock `json:"content_block,omitempty"`
	Delta        *sseDelta       `json:"delta,omitempty"`
	Message      *apiResponse    `json:"message,omitempty"`
	Usage        *apiUsage       `json:"usage,omitempty"`
}

type sseDelta struct {
	Type         string `json:"type,omitempty"`
	Text         string `json:"text,omitempty"`
	PartialJSON  string `json:"partial_json,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
}

// --- Tool Definitions ---

var apiTools = []apiToolDef{
	{
		Name:        "bash",
		Description: "Execute a bash command. Returns stdout+stderr. Commands timeout after 120s.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "The bash command to execute"},
				"timeout": {"type": "integer", "description": "Timeout in seconds (default 120, max 600)"}
			},
			"required": ["command"]
		}`),
	},
	{
		Name:        "read_file",
		Description: "Read a file's contents with line numbers.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Absolute path to the file"},
				"offset": {"type": "integer", "description": "Line number to start from (1-indexed)"},
				"limit": {"type": "integer", "description": "Max lines to read"}
			},
			"required": ["path"]
		}`),
	},
	{
		Name:        "write_file",
		Description: "Write content to a file. Creates parent directories. Overwrites existing.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Absolute path to write to"},
				"content": {"type": "string", "description": "File content"}
			},
			"required": ["path", "content"]
		}`),
	},
	{
		Name:        "edit_file",
		Description: "Replace a specific string in a file. old_string must appear exactly once.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Absolute path"},
				"old_string": {"type": "string", "description": "Exact text to find"},
				"new_string": {"type": "string", "description": "Replacement text"}
			},
			"required": ["path", "old_string", "new_string"]
		}`),
	},
}

// --- Tool Execution ---

func execBash(command string, timeout int, cwd string) string {
	if timeout <= 0 || timeout > 600 {
		timeout = apiBashTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()

	output := string(out)
	if len(output) > apiOutputCap {
		output = output[:apiOutputCap/2] + "\n\n... [truncated] ...\n\n" + output[len(output)-apiOutputCap/2:]
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

func execReadFile(path string, offset, limit int) string {
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

func execWriteFile(path, content string) string {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Sprintf("[ERROR creating dirs: %v]", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Sprintf("[ERROR writing %s: %v]", path, err)
	}
	return fmt.Sprintf("[Wrote %d bytes to %s]", len(content), path)
}

func execEditFile(path, oldStr, newStr string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("[File not found: %s]", path)
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return fmt.Sprintf("[old_string not found in %s]", path)
	}
	if count > 1 {
		return fmt.Sprintf("[old_string appears %d times — provide more context]", count)
	}
	newContent := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("[ERROR writing %s: %v]", path, err)
	}
	return fmt.Sprintf("[Edited %s]", path)
}

func executeTool(name string, input map[string]interface{}, cwd string) string {
	switch name {
	case "bash":
		cmd, _ := input["command"].(string)
		timeout := apiBashTimeout
		if t, ok := input["timeout"].(float64); ok {
			timeout = int(t)
		}
		return execBash(cmd, timeout, cwd)
	case "read_file":
		path, _ := input["path"].(string)
		offset, _ := input["offset"].(float64)
		limit, _ := input["limit"].(float64)
		return execReadFile(path, int(offset), int(limit))
	case "write_file":
		path, _ := input["path"].(string)
		content, _ := input["content"].(string)
		return execWriteFile(path, content)
	case "edit_file":
		path, _ := input["path"].(string)
		oldStr, _ := input["old_string"].(string)
		newStr, _ := input["new_string"].(string)
		return execEditFile(path, oldStr, newStr)
	default:
		return fmt.Sprintf("[Unknown tool: %s]", name)
	}
}

// --- API Call with Streaming ---

func (b *AnthropicBackend) callAPI(ctx context.Context, req *apiRequest) (*apiResponse, error) {
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	backoffs := []time.Duration{30 * time.Second, 60 * time.Second, 90 * time.Second, 120 * time.Second, 180 * time.Second}

	for attempt := 0; attempt <= apiMaxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", b.apiURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
		httpReq.Header.Set("Accept", "text/event-stream")

		// All sk-ant-* tokens (API keys AND OAuth) use x-api-key header.
		// This matches effort_classifier.go behavior — OAuth tokens work
		// with x-api-key but NOT with Authorization: Bearer.
		if strings.HasPrefix(b.apiKey, "sk-ant-") {
			httpReq.Header.Set("x-api-key", b.apiKey)
		} else {
			httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)
		}

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			if attempt < apiMaxRetries {
				slog.Warn("HTTP error, retrying", slog.Int("attempt", attempt+1), slog.Any("error", err))
				time.Sleep(backoffs[min(attempt, len(backoffs)-1)])
				continue
			}
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}

		// Handle non-200 responses
		if resp.StatusCode == 429 || resp.StatusCode == 529 || resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			if attempt < apiMaxRetries {
				wait := backoffs[min(attempt, len(backoffs)-1)]
				slog.Warn("API error, retrying", slog.Int("status", resp.StatusCode), slog.Duration("wait", wait))
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("API returned %d after %d retries", resp.StatusCode, apiMaxRetries)
		}

		if resp.StatusCode != 200 {
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 500)]))
		}

		// Parse SSE stream → accumulate into final response
		result, err := b.parseSSEStream(resp.Body)
		_ = resp.Body.Close()

		if err != nil {
			// Retry on overloaded errors in response body
			if strings.Contains(err.Error(), "overloaded") && attempt < apiMaxRetries {
				wait := backoffs[min(attempt, len(backoffs)-1)]
				slog.Warn("Overloaded in response, retrying", slog.Duration("wait", wait))
				time.Sleep(wait)
				continue
			}
			return nil, err
		}

		return result, nil
	}

	return nil, fmt.Errorf("exhausted %d retries", apiMaxRetries)
}

// parseSSEStream reads SSE events and accumulates the final message.
func (b *AnthropicBackend) parseSSEStream(body io.Reader) (*apiResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for large responses

	var result apiResponse
	var currentBlocks []apiContentBlock
	currentBlockTexts := make(map[int]strings.Builder)
	currentBlockInputs := make(map[int]strings.Builder)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				result.ID = event.Message.ID
				result.Model = event.Message.Model
				result.Role = event.Message.Role
				result.Usage.InputTokens += event.Message.Usage.InputTokens
			}

		case "content_block_start":
			if event.ContentBlock != nil {
				// Grow blocks slice to fit
				for len(currentBlocks) <= event.Index {
					currentBlocks = append(currentBlocks, apiContentBlock{})
				}
				currentBlocks[event.Index] = *event.ContentBlock
			}

		case "content_block_delta":
			if event.Delta != nil {
				switch event.Delta.Type {
				case "text_delta":
					if _, ok := currentBlockTexts[event.Index]; !ok {
						currentBlockTexts[event.Index] = strings.Builder{}
					}
					b := currentBlockTexts[event.Index]
					b.WriteString(event.Delta.Text)
					currentBlockTexts[event.Index] = b
				case "input_json_delta":
					if _, ok := currentBlockInputs[event.Index]; !ok {
						currentBlockInputs[event.Index] = strings.Builder{}
					}
					b := currentBlockInputs[event.Index]
					b.WriteString(event.Delta.PartialJSON)
					currentBlockInputs[event.Index] = b
				case "thinking_delta":
					// Thinking content — we don't need to store it
				}
			}

		case "content_block_stop":
			// Finalize block text/input
			if sb, ok := currentBlockTexts[event.Index]; ok && event.Index < len(currentBlocks) {
				currentBlocks[event.Index].Text = sb.String()
			}
			if sb, ok := currentBlockInputs[event.Index]; ok && event.Index < len(currentBlocks) {
				currentBlocks[event.Index].Input = json.RawMessage(sb.String())
			}

		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				result.StopReason = event.Delta.StopReason
			}
			if event.Usage != nil {
				result.Usage.OutputTokens += event.Usage.OutputTokens
			}

		case "message_stop":
			// Final event

		case "error":
			// Error in stream
			errData, _ := json.Marshal(event)
			return nil, fmt.Errorf("stream error: %s", string(errData))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read error: %w", err)
	}

	result.Content = currentBlocks

	// Check for error in response
	if result.Type == "error" || (result.Error != nil && result.Error.Type != "") {
		errMsg := "unknown error"
		if result.Error != nil {
			errMsg = result.Error.Message
		}
		if strings.Contains(strings.ToLower(errMsg), "overloaded") {
			return nil, fmt.Errorf("overloaded: %s", errMsg)
		}
		return nil, fmt.Errorf("API error: %s", errMsg)
	}

	return &result, nil
}

// --- Main Execute Loop ---

func (b *AnthropicBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	if b.apiKey == "" {
		return nil, fmt.Errorf("no API key configured for anthropic-api backend")
	}

	model := opts.Model
	if model == "" {
		model = b.config.ResolveModel("claude-opus-4-6")
	}

	cwd := opts.ProjectPath
	if cwd == "" {
		cwd = "/app"
	}

	// Emit init event
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:    EventTypeInit,
			Message: "Anthropic API backend initialized",
			Model:   model,
		})
	}

	messages := []apiMessage{
		{Role: "user", Content: opts.Prompt},
	}

	var totalInputTokens, totalOutputTokens int64
	var lastOutput string
	var sawSuccess bool

	for turn := 0; turn < apiMaxTurns; turn++ {
		// Check context
		if ctx.Err() != nil {
			return &BackendResult{
				Success:     sawSuccess,
				Output:      lastOutput,
				Error:       "context cancelled",
				TokensInput: totalInputTokens,
				TokensOutput: totalOutputTokens,
				Model:       model,
				SawSuccessResult: sawSuccess,
			}, nil
		}

		// Progressive thinking budget
		thinkingBudget := thinkingLowBudget
		if turn < thinkingHighTurns {
			thinkingBudget = thinkingHighBudget
		}

		// Map effort to thinking budget override
		switch opts.Effort {
		case "low":
			thinkingBudget = 1000
		case "medium":
			thinkingBudget = 3000
		case "max":
			thinkingBudget = 15000
		}

		// Disable thinking for non-Opus models (Haiku/Sonnet don't benefit as much,
		// and it avoids compatibility issues with older model versions)
		useThinking := strings.Contains(model, "opus")

		req := &apiRequest{
			Model:     model,
			MaxTokens: maxOutputTokens,
			System:    opts.Prompt,
			Messages:  messages,
			Tools:     apiTools,
		}
		if useThinking {
			req.MaxTokens = thinkingBudget + maxOutputTokens
			req.Thinking = &apiThinking{Type: "enabled", BudgetTokens: thinkingBudget}
		}

		// First message has prompt as system, subsequent don't repeat it
		if turn == 0 {
			req.System = opts.Prompt
			req.Messages = []apiMessage{
				{Role: "user", Content: "Begin. Follow the mandatory workflow."},
			}
			messages = req.Messages
		} else {
			req.System = opts.Prompt
		}

		slog.Info("API call", slog.Int("turn", turn), slog.String("model", model),
			slog.Bool("thinking", useThinking), slog.Int("budget", thinkingBudget),
			slog.String("effort", opts.Effort))

		response, err := b.callAPI(ctx, req)
		if err != nil {
			slog.Error("API call failed", slog.Int("turn", turn), slog.Any("error", err))

			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:    EventTypeError,
					Message: err.Error(),
					IsError: true,
				})
			}

			return &BackendResult{
				Success:     sawSuccess,
				Output:      lastOutput,
				Error:       err.Error(),
				TokensInput: totalInputTokens,
				TokensOutput: totalOutputTokens,
				Model:       model,
				SawSuccessResult: sawSuccess,
			}, nil
		}

		totalInputTokens += response.Usage.InputTokens
		totalOutputTokens += response.Usage.OutputTokens

		// Process response content → extract tool calls and text
		var assistantBlocks []apiContentBlock
		var toolCalls []apiContentBlock

		for _, block := range response.Content {
			switch block.Type {
			case "text":
				lastOutput = block.Text
				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:         EventTypeText,
						Message:      block.Text,
						TokensInput:  response.Usage.InputTokens,
						TokensOutput: response.Usage.OutputTokens,
						Model:        model,
					})
				}
				assistantBlocks = append(assistantBlocks, block)

			case "tool_use":
				var toolInput map[string]interface{}
				if err := json.Unmarshal(block.Input, &toolInput); err != nil {
					toolInput = map[string]interface{}{"error": "failed to parse input"}
				}

				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:      EventTypeToolUse,
						ToolName:  block.Name,
						ToolInput: toolInput,
						Model:     model,
					})
				}

				toolCalls = append(toolCalls, block)
				assistantBlocks = append(assistantBlocks, block)

			case "thinking":
				// Skip thinking blocks in message history
				assistantBlocks = append(assistantBlocks, block)
			}
		}

		// Add assistant message to conversation
		messages = append(messages, apiMessage{Role: "assistant", Content: assistantBlocks})

		// Process tool calls
		if response.StopReason == "tool_use" && len(toolCalls) > 0 {
			var toolResults []apiContentBlock

			for _, tc := range toolCalls {
				var toolInput map[string]interface{}
				_ = json.Unmarshal(tc.Input, &toolInput)

				slog.Info("Tool exec", slog.Int("turn", turn), slog.String("tool", tc.Name))
				result := executeTool(tc.Name, toolInput, cwd)

				if opts.EventHandler != nil {
					opts.EventHandler(BackendEvent{
						Type:       EventTypeToolResult,
						ToolName:   tc.Name,
						ToolResult: result,
					})
				}

				toolResults = append(toolResults, apiContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					Content:   result,
				})
			}

			messages = append(messages, apiMessage{Role: "user", Content: toolResults})

		} else if response.StopReason == "end_turn" {
			// Model finished
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
		if estimateContextTokens(messages) > apiContextPruneAt {
			messages = pruneAPIMessages(messages)
			slog.Info("Context pruned", slog.Int("turn", turn))
		}
	}

	return &BackendResult{
		Success:          sawSuccess,
		Output:           lastOutput,
		TokensInput:      totalInputTokens,
		TokensOutput:     totalOutputTokens,
		Model:            model,
		SawSuccessResult: sawSuccess,
	}, nil
}

// --- Context Management ---

func estimateContextTokens(messages []apiMessage) int {
	total := 0
	for _, msg := range messages {
		switch c := msg.Content.(type) {
		case string:
			total += len(c) / 4
		case []apiContentBlock:
			for _, b := range c {
				total += len(b.Text) / 4
				total += len(b.Content) / 4
				total += len(b.Input) / 4
			}
		default:
			data, _ := json.Marshal(c)
			total += len(data) / 4
		}
	}
	return total
}

func pruneAPIMessages(messages []apiMessage) []apiMessage {
	keepTurns := 12
	if len(messages) <= keepTurns*2 {
		return messages
	}
	first := messages[0]
	keep := messages[len(messages)-keepTurns*2:]
	pruned := []apiMessage{
		first,
		{Role: "user", Content: "[Earlier messages pruned to save context. Continue working on the task.]"},
	}
	return append(pruned, keep...)
}

// --- Error Type ---

// AnthropicAPIError implements BackendError for API errors.
type AnthropicAPIError struct {
	ErrType string
	Msg     string
}

func (e *AnthropicAPIError) Error() string       { return e.Msg }
func (e *AnthropicAPIError) ErrorType() string    { return e.ErrType }
func (e *AnthropicAPIError) ErrorMessage() string { return e.Msg }
func (e *AnthropicAPIError) ErrorStderr() string  { return "" }
