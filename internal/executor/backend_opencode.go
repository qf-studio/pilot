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
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// OpenCodeBackend implements Backend for OpenCode server.
// OpenCode uses a client/server architecture where the server runs locally
// and clients communicate via HTTP/SSE.
type OpenCodeBackend struct {
	config     *OpenCodeConfig
	log        *slog.Logger
	httpClient *http.Client
	serverCmd  *exec.Cmd
	serverMu   sync.Mutex
}

// NewOpenCodeBackend creates a new OpenCode backend.
func NewOpenCodeBackend(config *OpenCodeConfig) *OpenCodeBackend {
	if config == nil {
		config = &OpenCodeConfig{
			ServerURL:       "http://127.0.0.1:4096",
			Model:           "anthropic/claude-sonnet-4",
			Provider:        "anthropic",
			AutoStartServer: true,
			ServerCommand:   "opencode serve",
		}
	}
	if config.ServerURL == "" {
		config.ServerURL = "http://127.0.0.1:4096"
	}
	if config.Model == "" {
		config.Model = "anthropic/claude-sonnet-4"
	}

	return &OpenCodeBackend{
		config: config,
		log:    logging.WithComponent("executor.opencode"),
		httpClient: &http.Client{
			Timeout: 10 * time.Minute, // Long timeout for AI operations
		},
	}
}

// Name returns the backend identifier.
func (b *OpenCodeBackend) Name() string {
	return BackendTypeOpenCode
}

// IsAvailable checks if OpenCode server is running or can be started.
func (b *OpenCodeBackend) IsAvailable() bool {
	// Check if server is already running
	if b.isServerRunning() {
		return true
	}

	// Check if opencode CLI is installed
	_, err := exec.LookPath("opencode")
	return err == nil
}

// isServerRunning checks if the OpenCode server is responding.
func (b *OpenCodeBackend) isServerRunning() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", b.config.ServerURL+"/global/health", nil)
	if err != nil {
		return false
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode == http.StatusOK
}

// startServer starts the OpenCode server if configured.
func (b *OpenCodeBackend) startServer(ctx context.Context) error {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()

	// Already running
	if b.isServerRunning() {
		return nil
	}

	if !b.config.AutoStartServer {
		return fmt.Errorf("OpenCode server not running and auto-start disabled")
	}

	b.log.Info("Starting OpenCode server", slog.String("command", b.config.ServerCommand))

	// Parse server command
	parts := strings.Fields(b.config.ServerCommand)
	if len(parts) == 0 {
		parts = []string{"opencode", "serve"}
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start OpenCode server: %w", err)
	}

	b.serverCmd = cmd

	// Wait for server to be ready
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if b.isServerRunning() {
			b.log.Info("OpenCode server ready")
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("OpenCode server failed to start within timeout")
}

// Execute runs a prompt through OpenCode server.
func (b *OpenCodeBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	// Ensure server is running
	if err := b.startServer(ctx); err != nil {
		return nil, err
	}

	// Create a new session
	sessionID, err := b.createSession(ctx, opts.ProjectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	b.log.Debug("Created OpenCode session", slog.String("session_id", sessionID))

	// Send the message and stream response
	result, err := b.sendMessage(ctx, sessionID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}

	return result, nil
}

// createSession creates a new OpenCode session.
func (b *OpenCodeBackend) createSession(ctx context.Context, projectPath string) (string, error) {
	// OpenCode session creation payload
	payload := map[string]interface{}{
		"path": projectPath,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", b.config.ServerURL+"/session", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// OpenCode attached mode resolves the project directory from the
	// `x-opencode-directory` header (or `directory` query param), not from the
	// JSON `path` field. Without it, sessions are created in the server's cwd
	// rather than the target project. GH-2415.
	if projectPath != "" {
		req.Header.Set("X-OpenCode-Directory", url.QueryEscape(projectPath))
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("session creation failed: %s", string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.ID, nil
}

// sendMessage sends a prompt to an OpenCode session and streams the response.
func (b *OpenCodeBackend) sendMessage(ctx context.Context, sessionID string, opts ExecuteOptions) (*BackendResult, error) {
	result := &BackendResult{}

	// Build message payload
	payload := map[string]interface{}{
		"parts": []map[string]interface{}{
			{
				"type": "text",
				"text": opts.Prompt,
			},
		},
	}

	// OpenCode v1.4.x's Hono+Zod validator requires `model` to be either
	// {providerID, modelID} or omitted. Sending a plain string fails with
	// HTTP 400 "invalid_type" before the handler runs (GH-2413). See
	// https://github.com/anomalyco/opencode/blob/v1.4.6/packages/opencode/src/session/prompt.ts
	if ref := b.resolveModelRef(); ref != nil {
		payload["model"] = ref
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// Use async endpoint for streaming
	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("%s/session/%s/message", b.config.ServerURL, sessionID),
		bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// See createSession: attached-mode directory resolution requires this
	// header on the message endpoint too. GH-2415.
	if opts.ProjectPath != "" {
		req.Header.Set("X-OpenCode-Directory", url.QueryEscape(opts.ProjectPath))
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("message failed: %s", string(body))
	}

	// Check if streaming response
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// Parse SSE stream
		if err := b.parseSSEStream(resp.Body, opts, result); err != nil {
			return nil, err
		}
	} else {
		// OpenCode v1.4.x POST /session/:id/message returns application/json
		// with the shape {info: AssistantMessage, parts: Part[]}, regardless of
		// the Accept: text/event-stream request header. Parse that shape and
		// project it onto BackendResult / BackendEvent. (GH-2409)
		if err := b.parseAssistantResponse(resp.Body, opts, result); err != nil {
			return nil, err
		}
	}

	// If no error set, mark as successful
	if result.Error == "" {
		result.Success = true
	}

	return result, nil
}

// ocModelRef matches OpenCode v1.4.x's PromptInput.model schema:
// {providerID, modelID}. Encoded as a JSON object.
type ocModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// resolveModelRef builds the OpenCode model reference from config.
// Returns nil when no model is configured, signalling the caller to omit
// the field so the server falls back to its default model.
//
// Resolution rules:
//   - "providerID/modelID" → split on first "/"
//   - bare "modelID" + config.Provider → use config.Provider as providerID
//   - bare "modelID" with no provider → empty providerID (server may reject;
//     we still send what we have rather than silently dropping the model)
func (b *OpenCodeBackend) resolveModelRef() *ocModelRef {
	model := strings.TrimSpace(b.config.Model)
	if model == "" {
		return nil
	}
	if i := strings.Index(model, "/"); i > 0 && i < len(model)-1 {
		return &ocModelRef{
			ProviderID: model[:i],
			ModelID:    model[i+1:],
		}
	}
	return &ocModelRef{
		ProviderID: strings.TrimSpace(b.config.Provider),
		ModelID:    model,
	}
}

// parseAssistantResponse decodes the synchronous response from
// POST /session/:id/message and populates result. The body shape comes from
// OpenCode v1.4.6 (packages/opencode/src/server/instance/session.ts) and
// looks like: {info: AssistantMessage, parts: Part[]}.
//
// Text parts are concatenated into result.Output. Non-text parts (tool, file,
// agent, etc.) are surfaced through opts.EventHandler so the runner sees the
// same event stream it would for a streaming backend. Token usage and model
// metadata come from info.
func (b *OpenCodeBackend) parseAssistantResponse(body io.Reader, opts ExecuteOptions, result *BackendResult) error {
	var resp ocAssistantResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return fmt.Errorf("decode opencode response: %w", err)
	}

	// Model metadata: prefer providerID/modelID; fall back to model field.
	if resp.Info.ModelID != "" {
		if resp.Info.ProviderID != "" {
			result.Model = resp.Info.ProviderID + "/" + resp.Info.ModelID
		} else {
			result.Model = resp.Info.ModelID
		}
	} else if resp.Info.Model != "" {
		result.Model = resp.Info.Model
	}

	// Token usage. v1.4.x exposes tokens.{input,output,reasoning,cache.{read,write}}.
	result.TokensInput += resp.Info.Tokens.Input
	result.TokensOutput += resp.Info.Tokens.Output
	result.CacheReadInputTokens += resp.Info.Tokens.Cache.Read
	result.CacheCreationInputTokens += resp.Info.Tokens.Cache.Write

	if resp.Info.SessionID != "" {
		result.SessionID = resp.Info.SessionID
	}

	// If the assistant message carries an error indicator, propagate it.
	if resp.Info.Error.Message != "" {
		result.Error = resp.Info.Error.Message
	} else if resp.Info.Error.Name != "" {
		result.Error = resp.Info.Error.Name
	}

	var output strings.Builder
	for _, part := range resp.Parts {
		switch part.Type {
		case "text":
			output.WriteString(part.Text)
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:    EventTypeText,
					Message: part.Text,
					Raw:     part.Text,
				})
			}
		case "tool":
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:      EventTypeToolUse,
					ToolName:  part.Tool,
					ToolInput: part.State.Input,
					Message:   fmt.Sprintf("Using %s", part.Tool),
				})
				if part.State.Status == "completed" || part.State.Status == "error" {
					ev := BackendEvent{
						Type:       EventTypeToolResult,
						ToolName:   part.Tool,
						ToolResult: part.State.Output,
						IsError:    part.State.Status == "error",
					}
					opts.EventHandler(ev)
				}
			}
		case "step-start", "step-finish", "reasoning", "file", "agent", "snapshot":
			if opts.EventHandler != nil {
				opts.EventHandler(BackendEvent{
					Type:    EventTypeProgress,
					Message: part.Type,
				})
			}
		}
	}

	result.Output = output.String()

	// Synthesize a final result event so downstream consumers (alerts, logging)
	// see a terminal event, mirroring the SSE path.
	if opts.EventHandler != nil {
		opts.EventHandler(BackendEvent{
			Type:         EventTypeResult,
			Message:      result.Output,
			IsError:      result.Error != "",
			TokensInput:  resp.Info.Tokens.Input,
			TokensOutput: resp.Info.Tokens.Output,
			Model:        result.Model,
		})
	}

	return nil
}

// parseSSEStream parses Server-Sent Events from OpenCode.
func (b *OpenCodeBackend) parseSSEStream(reader io.Reader, opts ExecuteOptions, result *BackendResult) error {
	scanner := bufio.NewScanner(reader)
	// Increase buffer for large events
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var eventData strings.Builder

	for scanner.Scan() {
		line := scanner.Text()

		if opts.Verbose {
			fmt.Printf("   [sse] %s\n", line)
		}

		// SSE format: "data: {...}"
		if strings.HasPrefix(line, "data: ") {
			eventData.WriteString(strings.TrimPrefix(line, "data: "))
			continue
		}

		// Empty line marks end of event
		if line == "" && eventData.Len() > 0 {
			event := b.parseOpenCodeEvent(eventData.String())
			if opts.EventHandler != nil {
				opts.EventHandler(event)
			}

			// Track final result
			if event.Type == EventTypeResult {
				if event.IsError {
					result.Error = event.Message
				} else {
					result.Output = event.Message
				}
			}

			// Accumulate token usage. GH-2428: also accumulate cache tokens so
			// the SSE path matches the synchronous parseAssistantResponse path.
			result.TokensInput += event.TokensInput
			result.TokensOutput += event.TokensOutput
			result.CacheCreationInputTokens += event.CacheCreationInputTokens
			result.CacheReadInputTokens += event.CacheReadInputTokens
			if event.Model != "" {
				result.Model = event.Model
			}

			eventData.Reset()
		}
	}

	return scanner.Err()
}

// parseOpenCodeEvent converts OpenCode SSE event to BackendEvent.
func (b *OpenCodeBackend) parseOpenCodeEvent(data string) BackendEvent {
	event := BackendEvent{
		Raw: data,
	}

	var ocEvent openCodeEvent
	if err := json.Unmarshal([]byte(data), &ocEvent); err != nil {
		event.Type = EventTypeText
		event.Message = data
		return event
	}

	// Map OpenCode event types to backend events
	switch ocEvent.Type {
	case "message.start", "session.start":
		event.Type = EventTypeInit
		event.Message = "OpenCode session started"

	case "message.delta", "content.delta":
		event.Type = EventTypeText
		if ocEvent.Delta != nil {
			event.Message = ocEvent.Delta.Text
		}

	case "tool.start", "tool_use":
		event.Type = EventTypeToolUse
		event.ToolName = ocEvent.Tool
		if ocEvent.Input != nil {
			event.ToolInput = ocEvent.Input
		}
		event.Message = fmt.Sprintf("Using %s", ocEvent.Tool)

	case "tool.end", "tool_result":
		event.Type = EventTypeToolResult
		event.ToolResult = ocEvent.Output
		event.IsError = ocEvent.IsError

	case "message.end", "done":
		event.Type = EventTypeResult
		event.Message = ocEvent.Output
		event.IsError = ocEvent.IsError

	case "error":
		event.Type = EventTypeError
		event.Message = ocEvent.Error
		event.IsError = true

	case "usage":
		event.Type = EventTypeProgress
		if ocEvent.Usage != nil {
			event.TokensInput = ocEvent.Usage.InputTokens
			event.TokensOutput = ocEvent.Usage.OutputTokens
			event.CacheCreationInputTokens = ocEvent.Usage.cacheCreate()
			event.CacheReadInputTokens = ocEvent.Usage.cacheRead()
		}

	default:
		// Unknown event type, treat as progress
		event.Type = EventTypeProgress
		event.Message = data
	}

	// Extract usage if present (covers events where the usage block lives at
	// the top level rather than under a dedicated "usage" event type).
	if ocEvent.Usage != nil {
		event.TokensInput = ocEvent.Usage.InputTokens
		event.TokensOutput = ocEvent.Usage.OutputTokens
		event.CacheCreationInputTokens = ocEvent.Usage.cacheCreate()
		event.CacheReadInputTokens = ocEvent.Usage.cacheRead()
	}
	if ocEvent.Model != "" {
		event.Model = ocEvent.Model
	}

	return event
}

// openCodeEvent represents an event from OpenCode's SSE stream.
type openCodeEvent struct {
	Type    string                 `json:"type"`
	Tool    string                 `json:"tool,omitempty"`
	Input   map[string]interface{} `json:"input,omitempty"`
	Output  string                 `json:"output,omitempty"`
	Error   string                 `json:"error,omitempty"`
	IsError bool                   `json:"is_error,omitempty"`
	Model   string                 `json:"model,omitempty"`
	Delta   *openCodeDelta         `json:"delta,omitempty"`
	Usage   *openCodeUsage         `json:"usage,omitempty"`
}

type openCodeDelta struct {
	Text string `json:"text,omitempty"`
}

// openCodeUsage matches the usage shape OpenCode v1.4.x emits in SSE events.
// Both flat (cache_*) and nested (cache.{read,write}) shapes are accepted —
// different OpenCode builds and provider passthroughs use different layouts.
// GH-2428.
type openCodeUsage struct {
	InputTokens              int64        `json:"input_tokens"`
	OutputTokens             int64        `json:"output_tokens"`
	CacheCreationInputTokens int64        `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64        `json:"cache_read_input_tokens,omitempty"`
	Cache                    ocCacheToken `json:"cache,omitempty"`
}

// cacheCreate returns cache creation tokens from either layout.
func (u openCodeUsage) cacheCreate() int64 {
	if u.CacheCreationInputTokens > 0 {
		return u.CacheCreationInputTokens
	}
	return u.Cache.Write
}

// cacheRead returns cache read tokens from either layout.
func (u openCodeUsage) cacheRead() int64 {
	if u.CacheReadInputTokens > 0 {
		return u.CacheReadInputTokens
	}
	return u.Cache.Read
}

// ocAssistantResponse mirrors the response body of
// POST /session/:sessionID/message in OpenCode v1.4.x:
//
//	{info: AssistantMessage, parts: Part[]}
//
// Field set is intentionally minimal — only what Pilot consumes. Unknown
// fields are ignored by the JSON decoder.
type ocAssistantResponse struct {
	Info  ocAssistantInfo `json:"info"`
	Parts []ocPart        `json:"parts"`
}

type ocAssistantInfo struct {
	ID         string         `json:"id,omitempty"`
	Role       string         `json:"role,omitempty"`
	SessionID  string         `json:"sessionID,omitempty"`
	ProviderID string         `json:"providerID,omitempty"`
	ModelID    string         `json:"modelID,omitempty"`
	Model      string         `json:"model,omitempty"` // legacy/fallback
	Tokens     ocTokens       `json:"tokens"`
	Cost       float64        `json:"cost,omitempty"`
	Error      ocAssistantErr `json:"error,omitempty"`
}

type ocTokens struct {
	Input     int64        `json:"input"`
	Output    int64        `json:"output"`
	Reasoning int64        `json:"reasoning,omitempty"`
	Cache     ocCacheToken `json:"cache"`
}

type ocCacheToken struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

type ocAssistantErr struct {
	Name    string `json:"name,omitempty"`
	Message string `json:"message,omitempty"`
}

// ocPart covers the relevant union variants from MessageV2.Part. Only fields
// Pilot uses are mapped; other types decode with zero values for unused fields.
type ocPart struct {
	Type  string      `json:"type"`
	Text  string      `json:"text,omitempty"`
	Tool  string      `json:"tool,omitempty"`
	State ocPartState `json:"state,omitempty"`
}

type ocPartState struct {
	Status string                 `json:"status,omitempty"`
	Input  map[string]interface{} `json:"input,omitempty"`
	Output string                 `json:"output,omitempty"`
}

// StopServer stops the managed OpenCode server if running.
func (b *OpenCodeBackend) StopServer() error {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()

	if b.serverCmd != nil && b.serverCmd.Process != nil {
		b.log.Info("Stopping OpenCode server")
		return b.serverCmd.Process.Kill()
	}
	return nil
}
