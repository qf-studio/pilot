package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestNewOpenCodeBackend(t *testing.T) {
	tests := []struct {
		name            string
		config          *OpenCodeConfig
		expectServerURL string
		expectModel     string
		expectTimeout   string
	}{
		{
			name:            "nil config uses defaults",
			config:          nil,
			expectServerURL: "http://127.0.0.1:4096",
			expectModel:     "anthropic/claude-sonnet-4",
			expectTimeout:   "10m0s",
		},
		{
			name: "empty server URL uses default",
			config: &OpenCodeConfig{
				ServerURL: "",
				Model:     "custom-model",
			},
			expectServerURL: "http://127.0.0.1:4096",
			expectModel:     "custom-model",
			expectTimeout:   "10m0s",
		},
		{
			name: "custom config",
			config: &OpenCodeConfig{
				ServerURL:      "http://localhost:5000",
				Model:          "anthropic/claude-opus-4",
				RequestTimeout: "20m",
			},
			expectServerURL: "http://localhost:5000",
			expectModel:     "anthropic/claude-opus-4",
			expectTimeout:   "20m0s",
		},
		{
			name: "empty model uses default",
			config: &OpenCodeConfig{
				ServerURL: "http://localhost:4096",
				Model:     "",
			},
			expectServerURL: "http://localhost:4096",
			expectModel:     "anthropic/claude-sonnet-4",
			expectTimeout:   "10m0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewOpenCodeBackend(tt.config)
			if backend == nil {
				t.Fatal("NewOpenCodeBackend returned nil")
			}
			if backend.config.ServerURL != tt.expectServerURL {
				t.Errorf("ServerURL = %q, want %q", backend.config.ServerURL, tt.expectServerURL)
			}
			if backend.config.Model != tt.expectModel {
				t.Errorf("Model = %q, want %q", backend.config.Model, tt.expectModel)
			}
			if backend.httpClient.Timeout.String() != tt.expectTimeout {
				t.Errorf("http timeout = %q, want %q", backend.httpClient.Timeout.String(), tt.expectTimeout)
			}
		})
	}
}

func TestOpenCodeBackendName(t *testing.T) {
	backend := NewOpenCodeBackend(nil)
	if backend.Name() != BackendTypeOpenCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeOpenCode)
	}
}

func TestOpenCodeBackendParseOpenCodeEvent(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	tests := []struct {
		name        string
		data        string
		expectType  BackendEventType
		expectTool  string
		expectError bool
	}{
		{
			name:       "session start",
			data:       `{"type":"session.start"}`,
			expectType: EventTypeInit,
		},
		{
			name:       "message start",
			data:       `{"type":"message.start"}`,
			expectType: EventTypeInit,
		},
		{
			name:       "content delta",
			data:       `{"type":"content.delta","delta":{"text":"Hello world"}}`,
			expectType: EventTypeText,
		},
		{
			name:       "message delta",
			data:       `{"type":"message.delta","delta":{"text":"More text"}}`,
			expectType: EventTypeText,
		},
		{
			name:       "tool start",
			data:       `{"type":"tool.start","tool":"Read","input":{"file_path":"/test.go"}}`,
			expectType: EventTypeToolUse,
			expectTool: "Read",
		},
		{
			name:       "tool use",
			data:       `{"type":"tool_use","tool":"Write","input":{"file_path":"/output.go"}}`,
			expectType: EventTypeToolUse,
			expectTool: "Write",
		},
		{
			name:       "tool end",
			data:       `{"type":"tool.end","output":"file contents"}`,
			expectType: EventTypeToolResult,
		},
		{
			name:       "tool result",
			data:       `{"type":"tool_result","output":"success"}`,
			expectType: EventTypeToolResult,
		},
		{
			name:       "message end",
			data:       `{"type":"message.end","output":"Task complete"}`,
			expectType: EventTypeResult,
		},
		{
			name:       "done",
			data:       `{"type":"done","output":"Finished"}`,
			expectType: EventTypeResult,
		},
		{
			name:        "error",
			data:        `{"type":"error","error":"Something went wrong"}`,
			expectType:  EventTypeError,
			expectError: true,
		},
		{
			name:       "usage",
			data:       `{"type":"usage","usage":{"input_tokens":100,"output_tokens":50}}`,
			expectType: EventTypeProgress,
		},
		{
			name:       "unknown type",
			data:       `{"type":"unknown_event"}`,
			expectType: EventTypeProgress,
		},
		{
			name:       "invalid json",
			data:       `not valid json`,
			expectType: EventTypeText,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := backend.parseOpenCodeEvent(tt.data)

			if event.Type != tt.expectType {
				t.Errorf("Type = %q, want %q", event.Type, tt.expectType)
			}
			if tt.expectTool != "" && event.ToolName != tt.expectTool {
				t.Errorf("ToolName = %q, want %q", event.ToolName, tt.expectTool)
			}
			if tt.expectError && !event.IsError {
				t.Error("IsError should be true")
			}
			if event.Raw != tt.data {
				t.Errorf("Raw = %q, want %q", event.Raw, tt.data)
			}
		})
	}
}

func TestOpenCodeBackendParseUsageInfo(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	data := `{"type":"usage","usage":{"input_tokens":200,"output_tokens":100},"model":"anthropic/claude-sonnet-4"}`
	event := backend.parseOpenCodeEvent(data)

	if event.TokensInput != 200 {
		t.Errorf("TokensInput = %d, want 200", event.TokensInput)
	}
	if event.TokensOutput != 100 {
		t.Errorf("TokensOutput = %d, want 100", event.TokensOutput)
	}
	if event.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("Model = %q, want anthropic/claude-sonnet-4", event.Model)
	}
}

func TestOpenCodeBackendParseToolInput(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	data := `{"type":"tool.start","tool":"Bash","input":{"command":"npm test"}}`
	event := backend.parseOpenCodeEvent(data)

	if event.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", event.ToolName)
	}
	if event.ToolInput == nil {
		t.Fatal("ToolInput should not be nil")
	}
	if cmd, ok := event.ToolInput["command"].(string); !ok || cmd != "npm test" {
		t.Errorf("ToolInput[command] = %v, want 'npm test'", event.ToolInput["command"])
	}
}

func TestOpenCodeBackendIsAvailable(t *testing.T) {
	backend := NewOpenCodeBackend(&OpenCodeConfig{
		ServerURL:       "http://127.0.0.1:59999", // Non-running server
		AutoStartServer: false,
	})

	// Server not running and auto-start disabled, but opencode CLI check
	// This will depend on whether opencode is installed
	// Just verify it doesn't panic
	_ = backend.IsAvailable()
}

func TestOpenCodeBackendStopServer(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	// Should not error when no server is managed
	err := backend.StopServer()
	if err != nil {
		t.Errorf("StopServer() error = %v, want nil", err)
	}
}

// TestOpenCodeBackendParseAssistantResponse verifies the JSON-shape parsing
// for OpenCode v1.4.x's POST /session/:id/message response. Regression for
// GH-2409 — the previous parser looked for {success,output,error}, none of
// which exist on the actual response, so result.Output came back empty even
// though the call succeeded.
func TestOpenCodeBackendParseAssistantResponse(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	body := `{
		"info": {
			"id": "msg_1",
			"role": "assistant",
			"sessionID": "ses_1",
			"providerID": "anthropic",
			"modelID": "claude-sonnet-4",
			"tokens": {
				"input": 123,
				"output": 45,
				"reasoning": 0,
				"cache": {"read": 7, "write": 8}
			}
		},
		"parts": [
			{"type": "text", "text": "Hello "},
			{"type": "tool", "tool": "Read", "state": {"status": "completed", "input": {"file_path": "/x.go"}, "output": "ok"}},
			{"type": "text", "text": "world"}
		]
	}`

	var events []BackendEvent
	opts := ExecuteOptions{
		EventHandler: func(e BackendEvent) {
			events = append(events, e)
		},
	}
	result := &BackendResult{}

	if err := backend.parseAssistantResponse(strings.NewReader(body), opts, result); err != nil {
		t.Fatalf("parseAssistantResponse error = %v", err)
	}

	if result.Output != "Hello world" {
		t.Errorf("Output = %q, want %q", result.Output, "Hello world")
	}
	if result.TokensInput != 123 {
		t.Errorf("TokensInput = %d, want 123", result.TokensInput)
	}
	if result.TokensOutput != 45 {
		t.Errorf("TokensOutput = %d, want 45", result.TokensOutput)
	}
	if result.CacheReadInputTokens != 7 {
		t.Errorf("CacheReadInputTokens = %d, want 7", result.CacheReadInputTokens)
	}
	if result.CacheCreationInputTokens != 8 {
		t.Errorf("CacheCreationInputTokens = %d, want 8", result.CacheCreationInputTokens)
	}
	if result.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("Model = %q, want anthropic/claude-sonnet-4", result.Model)
	}
	if result.SessionID != "ses_1" {
		t.Errorf("SessionID = %q, want ses_1", result.SessionID)
	}
	if result.Error != "" {
		t.Errorf("Error = %q, want empty", result.Error)
	}

	// Expect events: text, tool_use, tool_result, text, result.
	wantTypes := []BackendEventType{
		EventTypeText, EventTypeToolUse, EventTypeToolResult, EventTypeText, EventTypeResult,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d (events=%v)", len(events), len(wantTypes), events)
	}
	for i, et := range wantTypes {
		if events[i].Type != et {
			t.Errorf("event[%d].Type = %q, want %q", i, events[i].Type, et)
		}
	}

	// Tool event should carry the tool name and input.
	toolEv := events[1]
	if toolEv.ToolName != "Read" {
		t.Errorf("tool event ToolName = %q, want Read", toolEv.ToolName)
	}
	if toolEv.ToolInput["file_path"] != "/x.go" {
		t.Errorf("tool event ToolInput[file_path] = %v, want /x.go", toolEv.ToolInput["file_path"])
	}

	// Final result event should carry the concatenated output.
	finalEv := events[len(events)-1]
	if finalEv.Message != "Hello world" {
		t.Errorf("final event Message = %q, want %q", finalEv.Message, "Hello world")
	}
}

func TestOpenCodeBackendParseAssistantResponseEmpty(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	// No parts at all — output should be empty but parse must succeed.
	body := `{"info": {"id":"msg_2","tokens":{"input":1,"output":2,"cache":{"read":0,"write":0}}}, "parts": []}`
	result := &BackendResult{}
	if err := backend.parseAssistantResponse(strings.NewReader(body), ExecuteOptions{}, result); err != nil {
		t.Fatalf("parseAssistantResponse error = %v", err)
	}
	if result.Output != "" {
		t.Errorf("Output = %q, want empty", result.Output)
	}
	if result.TokensInput != 1 || result.TokensOutput != 2 {
		t.Errorf("tokens = %d/%d, want 1/2", result.TokensInput, result.TokensOutput)
	}
}

func TestOpenCodeBackendParseAssistantResponseError(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	body := `{"info": {"id":"msg_3","tokens":{"input":0,"output":0,"cache":{"read":0,"write":0}}, "error":{"name":"ProviderAuthError","message":"bad key"}}, "parts": []}`
	result := &BackendResult{}
	if err := backend.parseAssistantResponse(strings.NewReader(body), ExecuteOptions{}, result); err != nil {
		t.Fatalf("parseAssistantResponse error = %v", err)
	}
	if result.Error != "bad key" {
		t.Errorf("Error = %q, want %q", result.Error, "bad key")
	}
}

// TestOpenCodeBackendResolveModelRef verifies model resolution into the
// {providerID, modelID} shape required by OpenCode v1.4.x (GH-2413).
func TestOpenCodeBackendResolveModelRef(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		provider    string
		wantNil     bool
		wantProv    string
		wantModelID string
	}{
		{
			name:        "providerID/modelID combined",
			model:       "openai/gpt-5.4-mini",
			provider:    "",
			wantProv:    "openai",
			wantModelID: "gpt-5.4-mini",
		},
		{
			name:        "bare modelID with explicit provider",
			model:       "gpt-5.4-mini",
			provider:    "openai",
			wantProv:    "openai",
			wantModelID: "gpt-5.4-mini",
		},
		{
			name:    "empty model returns nil (server default)",
			model:   "",
			wantNil: true,
		},
		{
			name:        "anthropic/claude-sonnet-4 default",
			model:       "anthropic/claude-sonnet-4",
			wantProv:    "anthropic",
			wantModelID: "claude-sonnet-4",
		},
		{
			name:        "modelID with multiple slashes splits on first only",
			model:       "openrouter/anthropic/claude-sonnet-4",
			wantProv:    "openrouter",
			wantModelID: "anthropic/claude-sonnet-4",
		},
		{
			name:        "trailing slash treated as bare modelID",
			model:       "openai/",
			provider:    "fallback",
			wantProv:    "fallback",
			wantModelID: "openai/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &OpenCodeBackend{config: &OpenCodeConfig{
				Model:    tt.model,
				Provider: tt.provider,
			}}
			ref := b.resolveModelRef()
			if tt.wantNil {
				if ref != nil {
					t.Fatalf("resolveModelRef() = %+v, want nil", ref)
				}
				return
			}
			if ref == nil {
				t.Fatal("resolveModelRef() = nil, want non-nil")
			}
			if ref.ProviderID != tt.wantProv {
				t.Errorf("ProviderID = %q, want %q", ref.ProviderID, tt.wantProv)
			}
			if ref.ModelID != tt.wantModelID {
				t.Errorf("ModelID = %q, want %q", ref.ModelID, tt.wantModelID)
			}
		})
	}
}

// TestOpenCodeBackendSendMessageModelObject verifies that sendMessage
// serialises `model` as an object {providerID, modelID}, never a string
// (GH-2413). Also checks `model` is omitted when no model is configured.
func TestOpenCodeBackendSendMessageModelObject(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		provider  string
		wantOmit  bool
		wantProv  string
		wantModel string
	}{
		{
			name:      "combined model serialises as object",
			model:     "openai/gpt-5.4-mini",
			wantProv:  "openai",
			wantModel: "gpt-5.4-mini",
		},
		{
			name:      "bare model + provider serialises as object",
			model:     "gpt-5.4-mini",
			provider:  "openai",
			wantProv:  "openai",
			wantModel: "gpt-5.4-mini",
		},
		{
			name:     "empty model omits field",
			model:    "",
			wantOmit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured map[string]interface{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &captured)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"info":{"id":"m1","tokens":{"input":0,"output":0,"cache":{"read":0,"write":0}}},"parts":[{"type":"text","text":"ok"}]}`))
			}))
			defer srv.Close()

			b := &OpenCodeBackend{
				config: &OpenCodeConfig{
					ServerURL: srv.URL,
					Model:     tt.model,
					Provider:  tt.provider,
				},
				httpClient: &http.Client{Timeout: 5 * time.Second},
			}
			b.log = NewOpenCodeBackend(nil).log

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := b.sendMessage(ctx, "ses_1", ExecuteOptions{Prompt: "hi"}); err != nil {
				t.Fatalf("sendMessage error = %v", err)
			}

			modelField, present := captured["model"]
			if tt.wantOmit {
				if present {
					t.Fatalf("expected model field omitted, got %v", modelField)
				}
				return
			}
			obj, ok := modelField.(map[string]interface{})
			if !ok {
				t.Fatalf("model field is not an object: %T = %v", modelField, modelField)
			}
			if obj["providerID"] != tt.wantProv {
				t.Errorf("providerID = %v, want %q", obj["providerID"], tt.wantProv)
			}
			if obj["modelID"] != tt.wantModel {
				t.Errorf("modelID = %v, want %q", obj["modelID"], tt.wantModel)
			}
		})
	}
}

// TestOpenCodeBackendSendMessagePayloadShape verifies the full payload JSON
// shape matches OpenCode v1.4.x's PromptInput schema (GH-2413).
func TestOpenCodeBackendSendMessagePayloadShape(t *testing.T) {
	var raw bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&raw, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"m1","tokens":{"input":0,"output":0,"cache":{"read":0,"write":0}}},"parts":[]}`))
	}))
	defer srv.Close()

	b := &OpenCodeBackend{
		config: &OpenCodeConfig{
			ServerURL: srv.URL,
			Model:     "anthropic/claude-sonnet-4",
		},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	b.log = NewOpenCodeBackend(nil).log

	if _, err := b.sendMessage(context.Background(), "ses_1", ExecuteOptions{Prompt: "hello"}); err != nil {
		t.Fatalf("sendMessage error = %v", err)
	}

	// Reject the legacy string form.
	if strings.Contains(raw.String(), `"model":"anthropic/claude-sonnet-4"`) {
		t.Fatalf("payload still sends model as string: %s", raw.String())
	}
	// Require the object form.
	if !strings.Contains(raw.String(), `"providerID":"anthropic"`) ||
		!strings.Contains(raw.String(), `"modelID":"claude-sonnet-4"`) {
		t.Fatalf("payload missing object-form model: %s", raw.String())
	}
}

// TestOpenCodeBackendDirectoryHeader verifies that both POST /session and
// POST /session/:id/message send the X-OpenCode-Directory header so that
// attached OpenCode servers create sessions in the target project path,
// not in the server's cwd. GH-2415.
func TestOpenCodeBackendDirectoryHeader(t *testing.T) {
	const projectPath = "/tmp/some path/with spaces"
	wantHeader := url.QueryEscape(projectPath)

	var sessionHeader, messageHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			sessionHeader = r.Header.Get("X-OpenCode-Directory")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ses_1"}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/session/"):
			messageHeader = r.Header.Get("X-OpenCode-Directory")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"info":{"id":"m1","tokens":{"input":0,"output":0,"cache":{"read":0,"write":0}}},"parts":[{"type":"text","text":"ok"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := &OpenCodeBackend{
		config: &OpenCodeConfig{
			ServerURL: srv.URL,
			Model:     "anthropic/claude-sonnet-4",
		},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	b.log = NewOpenCodeBackend(nil).log

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessionID, err := b.createSession(ctx, projectPath)
	if err != nil {
		t.Fatalf("createSession error = %v", err)
	}
	if sessionHeader != wantHeader {
		t.Errorf("createSession X-OpenCode-Directory = %q, want %q", sessionHeader, wantHeader)
	}

	if _, err := b.sendMessage(ctx, sessionID, ExecuteOptions{Prompt: "hi", ProjectPath: projectPath}); err != nil {
		t.Fatalf("sendMessage error = %v", err)
	}
	if messageHeader != wantHeader {
		t.Errorf("sendMessage X-OpenCode-Directory = %q, want %q", messageHeader, wantHeader)
	}
}

func TestOpenCodeEventStructs(t *testing.T) {
	// Test openCodeEvent struct
	event := openCodeEvent{
		Type:    "tool.start",
		Tool:    "Read",
		Input:   map[string]interface{}{"file_path": "/test.go"},
		Output:  "",
		Error:   "",
		IsError: false,
		Model:   "anthropic/claude-sonnet-4",
		Delta:   &openCodeDelta{Text: "Hello"},
		Usage:   &openCodeUsage{InputTokens: 100, OutputTokens: 50},
	}

	if event.Type != "tool.start" {
		t.Errorf("Type = %q, want tool.start", event.Type)
	}
	if event.Tool != "Read" {
		t.Errorf("Tool = %q, want Read", event.Tool)
	}
	if event.Delta.Text != "Hello" {
		t.Errorf("Delta.Text = %q, want Hello", event.Delta.Text)
	}
	if event.Usage.InputTokens != 100 {
		t.Errorf("Usage.InputTokens = %d, want 100", event.Usage.InputTokens)
	}
}

// TestOpenCodeBackendParseSSEStreamCacheTokens verifies that the SSE path
// captures cache_creation/cache_read fields from a usage event, matching the
// synchronous parseAssistantResponse path. GH-2428.
func TestOpenCodeBackendParseSSEStreamCacheTokens(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	// Two SSE events: a usage event with cache fields, then a result event.
	// SSE format: each event ends with a blank line; we also need a trailing
	// blank line on the last event to trigger dispatch.
	sse := "data: {\"type\":\"usage\",\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"cache_creation_input_tokens\":3,\"cache_read_input_tokens\":4},\"model\":\"glm-5.1\"}\n\n" +
		"data: {\"type\":\"done\",\"output\":\"ok\"}\n\n"

	result := &BackendResult{}
	opts := ExecuteOptions{}
	if err := backend.parseSSEStream(strings.NewReader(sse), opts, result); err != nil {
		t.Fatalf("parseSSEStream error = %v", err)
	}

	if result.TokensInput != 10 || result.TokensOutput != 20 {
		t.Errorf("tokens = %d/%d, want 10/20", result.TokensInput, result.TokensOutput)
	}
	if result.CacheCreationInputTokens != 3 {
		t.Errorf("CacheCreationInputTokens = %d, want 3", result.CacheCreationInputTokens)
	}
	if result.CacheReadInputTokens != 4 {
		t.Errorf("CacheReadInputTokens = %d, want 4", result.CacheReadInputTokens)
	}
	if result.Model != "glm-5.1" {
		t.Errorf("Model = %q, want glm-5.1", result.Model)
	}
}

// TestOpenCodeBackendParseSSENestedCache verifies that the nested
// {cache:{read,write}} usage layout is also accepted by SSE parsing. GH-2428.
func TestOpenCodeBackendParseSSENestedCache(t *testing.T) {
	backend := NewOpenCodeBackend(nil)

	// Trailing empty line is required so bufio.Scanner yields the blank line
	// that marks the end of the SSE event.
	sse := "data: {\"type\":\"usage\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"cache\":{\"read\":5,\"write\":6}}}\n\n"

	result := &BackendResult{}
	if err := backend.parseSSEStream(strings.NewReader(sse), ExecuteOptions{}, result); err != nil {
		t.Fatalf("parseSSEStream error = %v", err)
	}
	if result.CacheCreationInputTokens != 6 {
		t.Errorf("CacheCreationInputTokens = %d, want 6 (from cache.write)", result.CacheCreationInputTokens)
	}
	if result.CacheReadInputTokens != 5 {
		t.Errorf("CacheReadInputTokens = %d, want 5 (from cache.read)", result.CacheReadInputTokens)
	}
}
