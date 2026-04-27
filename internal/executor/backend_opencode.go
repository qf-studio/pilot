package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
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

type openCodeGlobalEvent struct {
	Directory string               `json:"directory"`
	Payload   openCodeEventPayload `json:"payload"`
}

type openCodeEventPayload struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	AggregateID string            `json:"aggregateID"`
	Properties  openCodeEventData `json:"properties"`
	Data        openCodeEventData `json:"data"`
}

type openCodeEventData struct {
	ID         string                    `json:"id,omitempty"`
	SessionID  string                    `json:"sessionID,omitempty"`
	MessageID  string                    `json:"messageID,omitempty"`
	PartID     string                    `json:"partID,omitempty"`
	Field      string                    `json:"field,omitempty"`
	Delta      string                    `json:"delta,omitempty"`
	Info       *openCodeEventMessageInfo `json:"info,omitempty"`
	Part       *openCodeResponsePart     `json:"part,omitempty"`
	Permission string                    `json:"permission,omitempty"`
	Patterns   []string                  `json:"patterns,omitempty"`
	Status     *struct {
		Type    string `json:"type"`
		Attempt int    `json:"attempt,omitempty"`
		Message string `json:"message,omitempty"`
		Next    int64  `json:"next,omitempty"`
	} `json:"status,omitempty"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error,omitempty"`
}

type openCodeEventMessageInfo struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Role       string `json:"role"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Finish     string `json:"finish"`
	Error      *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error,omitempty"`
}

type openCodeResponsePart struct {
	ID        string       `json:"id,omitempty"`
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	Tool      string       `json:"tool,omitempty"`
	CallID    string       `json:"callID,omitempty"`
	Output    string       `json:"output,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	SessionID string       `json:"sessionID,omitempty"`
	MessageID string       `json:"messageID,omitempty"`
	State     *ocPartState `json:"state,omitempty"`
	Tokens    *ocTokens    `json:"tokens,omitempty"`
}

type openCodePermissionRequest struct {
	ID         string   `json:"id"`
	SessionID  string   `json:"sessionID"`
	Permission string   `json:"permission"`
	Patterns   []string `json:"patterns"`
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
	baseURL := strings.TrimRight(b.config.ServerURL, "/") + "/session"
	if projectPath != "" {
		q := url.Values{}
		q.Set("directory", projectPath)
		baseURL += "?" + q.Encode()
	}

	result, err := b.doCreateSessionRequest(ctx, baseURL, map[string]interface{}{}, projectPath)
	if err == nil {
		return result.ID, nil
	}

	legacyResult, legacyErr := b.doCreateSessionRequest(ctx, strings.TrimRight(b.config.ServerURL, "/")+"/session", map[string]interface{}{"path": projectPath}, projectPath)
	if legacyErr == nil {
		b.log.Warn("OpenCode session create fell back to legacy payload API")
		return legacyResult.ID, nil
	}

	return "", err
}

func (b *OpenCodeBackend) doCreateSessionRequest(ctx context.Context, requestURL string, payload map[string]interface{}, projectPath string) (*struct {
	ID string `json:"id"`
}, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if projectPath != "" {
		req.Header.Set("X-OpenCode-Directory", url.QueryEscape(projectPath))
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("session creation failed: %s", string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// sendMessage sends a prompt to an OpenCode session and streams the response.
func (b *OpenCodeBackend) sendMessage(ctx context.Context, sessionID string, opts ExecuteOptions) (*BackendResult, error) {
	result := &BackendResult{}

	payload := b.buildPromptPayload(opts)

	if modernResult, modernErr := b.sendMessageModern(ctx, sessionID, opts, payload); modernErr == nil {
		return modernResult, nil
	} else {
		b.log.Warn("OpenCode modern prompt API failed, falling back to legacy message API", slog.Any("error", modernErr))
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

func (b *OpenCodeBackend) buildPromptPayload(opts ExecuteOptions) map[string]interface{} {
	payload := map[string]interface{}{
		"parts": []map[string]interface{}{{
			"type": "text",
			"text": opts.Prompt,
		}},
	}
	if ref := b.resolveModelRef(); ref != nil {
		payload["model"] = ref
	}
	return payload
}

func (b *OpenCodeBackend) sendMessageModern(ctx context.Context, sessionID string, opts ExecuteOptions, payload map[string]interface{}) (*BackendResult, error) {
	result := &BackendResult{}

	eventCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events := make(chan BackendEvent, 32)
	eventErrCh := make(chan error, 1)
	go b.consumeEventStream(eventCtx, opts.ProjectPath, sessionID, events, eventErrCh)

	if err := b.doPromptAsyncRequest(ctx, sessionID, opts.ProjectPath, payload); err != nil {
		return nil, err
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-eventErrCh:
			if err != nil {
				return nil, err
			}
		case event, ok := <-events:
			if !ok {
				if result.Success || result.Error != "" || result.Output != "" {
					return result, nil
				}
				return nil, fmt.Errorf("event stream closed before completion")
			}

			if opts.EventHandler != nil {
				opts.EventHandler(event)
			}

			result.TokensInput += event.TokensInput
			result.TokensOutput += event.TokensOutput
			if event.Model != "" {
				result.Model = event.Model
			}
			switch event.Type {
			case EventTypeText:
				if event.Message != "" {
					result.Output += event.Message
				}
			case EventTypeToolResult:
				if event.ToolResult != "" {
					result.LastAssistantText = event.ToolResult
				}
			case EventTypeError:
				result.Error = event.Message
				return result, nil
			case EventTypeResult:
				if event.Message != "" && result.Output == "" {
					result.Output = event.Message
				}
				result.Success = !event.IsError
				if event.IsError {
					result.Error = event.Message
				}
				return result, nil
			}
		}
	}
}

func (b *OpenCodeBackend) doPromptAsyncRequest(ctx context.Context, sessionID, projectPath string, payload map[string]interface{}) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	requestURL := fmt.Sprintf("%s/session/%s/prompt_async", strings.TrimRight(b.config.ServerURL, "/"), sessionID)
	if projectPath != "" {
		q := url.Values{}
		q.Set("directory", projectPath)
		requestURL += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "POST", requestURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if projectPath != "" {
		req.Header.Set("X-OpenCode-Directory", url.QueryEscape(projectPath))
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("prompt_async failed: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func (b *OpenCodeBackend) consumeEventStream(ctx context.Context, projectPath, sessionID string, events chan<- BackendEvent, errCh chan<- error) {
	defer close(events)

	requestURL := strings.TrimRight(b.config.ServerURL, "/") + "/event"
	if projectPath != "" {
		q := url.Values{}
		q.Set("directory", projectPath)
		requestURL += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", requestURL, nil)
	if err != nil {
		errCh <- err
		return
	}
	if projectPath != "" {
		req.Header.Set("X-OpenCode-Directory", url.QueryEscape(projectPath))
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		errCh <- err
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errCh <- fmt.Errorf("event subscribe failed: %s", strings.TrimSpace(string(body)))
		return
	}

	if err := b.parseGlobalEventStream(resp.Body, projectPath, sessionID, events); err != nil && !errors.Is(err, context.Canceled) {
		errCh <- err
		return
	}
	errCh <- nil
}

func (b *OpenCodeBackend) parseGlobalEventStream(reader io.Reader, projectPath, sessionID string, events chan<- BackendEvent) error {
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var eventData strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			eventData.WriteString(strings.TrimPrefix(line, "data: "))
			continue
		}
		if line != "" || eventData.Len() == 0 {
			continue
		}
		mapped, done, err := b.parseGlobalEvent(eventData.String(), projectPath, sessionID)
		eventData.Reset()
		if err != nil {
			return err
		}
		for _, event := range mapped {
			events <- event
		}
		if done {
			return nil
		}
	}
	return scanner.Err()
}

func (b *OpenCodeBackend) parseGlobalEvent(data, projectPath, sessionID string) ([]BackendEvent, bool, error) {
	directory, payload, err := decodeOpenCodeStreamEvent(data)
	if err != nil {
		return nil, false, err
	}
	if projectPath != "" && directory != "" && !samePath(directory, projectPath) {
		return nil, false, nil
	}
	if sid := firstNonEmpty(payload.Properties.SessionID, payload.Data.SessionID); sid != "" && sid != sessionID {
		return nil, false, nil
	}

	switch payload.Type {
	case "message.updated":
		if payload.Properties.Info != nil {
			return []BackendEvent{{
				Type:      EventTypeInit,
				Raw:       data,
				Message:   "OpenCode session started",
				SessionID: payload.Properties.Info.SessionID,
				Model:     joinModel(payload.Properties.Info.ProviderID, payload.Properties.Info.ModelID),
			}}, false, nil
		}
	case "message.part.updated":
		part := payload.Properties.Part
		if part == nil {
			part = payload.Data.Part
		}
		if part == nil {
			return nil, false, nil
		}
		return b.mapResponsePart(*part, data), false, nil
	case "message.part.delta":
		delta := firstNonEmpty(payload.Properties.Delta, payload.Data.Delta)
		if delta == "" {
			return nil, false, nil
		}
		return []BackendEvent{{Type: EventTypeText, Raw: data, Message: delta}}, false, nil
	case "session.error":
		msg := "OpenCode session error"
		if payload.Properties.Error != nil && payload.Properties.Error.Data.Message != "" {
			msg = payload.Properties.Error.Data.Message
		}
		return []BackendEvent{{Type: EventTypeError, Raw: data, Message: msg, IsError: true}}, true, nil
	case "session.idle":
		return []BackendEvent{{Type: EventTypeResult, Raw: data}}, true, nil
	case "permission.asked":
		approved, err := b.handlePermissionRequest(projectPath, openCodePermissionRequest{
			ID:         payload.Properties.ID,
			SessionID:  payload.Properties.SessionID,
			Permission: payload.Properties.Permission,
			Patterns:   payload.Properties.Patterns,
		})
		if err != nil {
			return nil, false, err
		}
		if approved {
			return []BackendEvent{{Type: EventTypeProgress, Raw: data, Message: "Auto-approved external directory permission"}}, false, nil
		}
		return []BackendEvent{{Type: EventTypeError, Raw: data, Message: "OpenCode requested unsupported permission", IsError: true}}, true, nil
	case "question.asked":
		return []BackendEvent{{Type: EventTypeError, Raw: data, Message: "OpenCode asked interactive question; unattended mode unsupported", IsError: true}}, true, nil
	}

	if payload.Type == "sync" {
		switch payload.Name {
		case "message.updated.1":
			if payload.Data.Info != nil {
				return []BackendEvent{{
					Type:      EventTypeInit,
					Raw:       data,
					Message:   "OpenCode session started",
					SessionID: payload.Data.Info.SessionID,
					Model:     joinModel(payload.Data.Info.ProviderID, payload.Data.Info.ModelID),
				}}, false, nil
			}
		case "message.part.updated.1":
			if payload.Data.Part != nil {
				return b.mapResponsePart(*payload.Data.Part, data), false, nil
			}
		}
	}

	return nil, false, nil
}

func (b *OpenCodeBackend) mapResponsePart(part openCodeResponsePart, raw string) []BackendEvent {
	switch part.Type {
	case "text":
		if part.Text == "" {
			return nil
		}
		return []BackendEvent{{Type: EventTypeText, Raw: raw, Message: part.Text}}
	case "tool":
		if part.State == nil {
			return nil
		}
		switch part.State.Status {
		case "pending", "running":
			return []BackendEvent{{Type: EventTypeToolUse, Raw: raw, ToolName: part.Tool, ToolInput: part.State.Input, Message: fmt.Sprintf("Using %s", part.Tool)}}
		case "completed":
			return []BackendEvent{{Type: EventTypeToolResult, Raw: raw, ToolName: part.Tool, ToolResult: part.State.Output}}
		case "error":
			return []BackendEvent{{Type: EventTypeError, Raw: raw, ToolName: part.Tool, Message: part.State.Output, IsError: true}}
		}
	case "step-finish":
		event := BackendEvent{Type: EventTypeResult, Raw: raw}
		if part.Tokens != nil {
			event.TokensInput = part.Tokens.Input
			event.TokensOutput = part.Tokens.Output
		}
		if part.Reason == "error" {
			event.IsError = true
			event.Message = "OpenCode step failed"
		}
		return []BackendEvent{event}
	}
	return nil
}

func (b *OpenCodeBackend) handlePermissionRequest(projectPath string, req openCodePermissionRequest) (bool, error) {
	if req.Permission != "external_directory" {
		return false, nil
	}
	if !permissionMatchesProject(req.Patterns, projectPath) {
		return false, nil
	}

	requestURL := fmt.Sprintf("%s/permission/%s/reply", strings.TrimRight(b.config.ServerURL, "/"), req.ID)
	if projectPath != "" {
		q := url.Values{}
		q.Set("directory", projectPath)
		requestURL += "?" + q.Encode()
	}
	body, err := json.Marshal(map[string]string{"reply": "always"})
	if err != nil {
		return false, err
	}
	reqHTTP, err := http.NewRequestWithContext(context.Background(), "POST", requestURL, bytes.NewBuffer(body))
	if err != nil {
		return false, err
	}
	reqHTTP.Header.Set("Content-Type", "application/json")
	if projectPath != "" {
		reqHTTP.Header.Set("X-OpenCode-Directory", url.QueryEscape(projectPath))
	}
	resp, err := b.httpClient.Do(reqHTTP)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("permission reply failed: %s", strings.TrimSpace(string(data)))
	}
	return true, nil
}

func permissionMatchesProject(patterns []string, projectPath string) bool {
	if projectPath == "" {
		return false
	}
	cleanProject := filepath.Clean(projectPath)
	for _, pattern := range patterns {
		trimmed := strings.TrimSuffix(filepath.Clean(pattern), string(filepath.Separator)+"*")
		if strings.HasPrefix(cleanProject, trimmed) || strings.HasPrefix(trimmed, cleanProject) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func joinModel(providerID, modelID string) string {
	if providerID == "" {
		return modelID
	}
	if modelID == "" {
		return providerID
	}
	return providerID + "/" + modelID
}

func decodeOpenCodeStreamEvent(data string) (string, openCodeEventPayload, error) {
	var wrapped openCodeGlobalEvent
	if err := json.Unmarshal([]byte(data), &wrapped); err == nil {
		if wrapped.Payload.Type != "" || wrapped.Payload.Name != "" || wrapped.Directory != "" {
			return wrapped.Directory, wrapped.Payload, nil
		}
	}

	var payload openCodeEventPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return "", openCodeEventPayload{}, err
	}
	return "", payload, nil
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
