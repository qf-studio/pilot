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
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
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

	if b.config.Model != "" {
		payload["model"] = b.config.Model
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
		// Parse JSON response
		var msgResult struct {
			Success bool   `json:"success"`
			Output  string `json:"output"`
			Error   string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&msgResult); err != nil {
			return nil, err
		}
		result.Success = msgResult.Success
		result.Output = msgResult.Output
		result.Error = msgResult.Error
	}

	// If no error set, mark as successful
	if result.Error == "" {
		result.Success = true
	}

	return result, nil
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

			// Accumulate token usage
			result.TokensInput += event.TokensInput
			result.TokensOutput += event.TokensOutput
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
		}

	default:
		// Unknown event type, treat as progress
		event.Type = EventTypeProgress
		event.Message = data
	}

	// Extract usage if present
	if ocEvent.Usage != nil {
		event.TokensInput = ocEvent.Usage.InputTokens
		event.TokensOutput = ocEvent.Usage.OutputTokens
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

type openCodeUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
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
