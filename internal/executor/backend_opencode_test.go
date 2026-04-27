package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewOpenCodeBackend(t *testing.T) {
	tests := []struct {
		name            string
		config          *OpenCodeConfig
		expectServerURL string
		expectModel     string
	}{
		{
			name:            "nil config uses defaults",
			config:          nil,
			expectServerURL: "http://127.0.0.1:4096",
			expectModel:     "anthropic/claude-sonnet-4",
		},
		{
			name: "empty server URL uses default",
			config: &OpenCodeConfig{
				ServerURL: "",
				Model:     "custom-model",
			},
			expectServerURL: "http://127.0.0.1:4096",
			expectModel:     "custom-model",
		},
		{
			name: "custom config",
			config: &OpenCodeConfig{
				ServerURL: "http://localhost:5000",
				Model:     "anthropic/claude-opus-4",
			},
			expectServerURL: "http://localhost:5000",
			expectModel:     "anthropic/claude-opus-4",
		},
		{
			name: "empty model uses default",
			config: &OpenCodeConfig{
				ServerURL: "http://localhost:4096",
				Model:     "",
			},
			expectServerURL: "http://localhost:4096",
			expectModel:     "anthropic/claude-sonnet-4",
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

func TestOpenCodeBackendSendsProjectDirectoryHeader(t *testing.T) {
	var sessionHeader string
	var messageHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/global/health":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			sessionHeader = r.Header.Get("X-OpenCode-Directory")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"sess-1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session/sess-1/message":
			messageHeader = r.Header.Get("X-OpenCode-Directory")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"info":{"id":"msg_1","role":"assistant","sessionID":"sess-1","providerID":"anthropic","modelID":"claude-sonnet-4","tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"type":"text","text":"ok"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	const projectPath = "/config/Desktop/projects/linkedinopenclaw"
	backend := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: server.URL, Model: "anthropic/claude-sonnet-4", Provider: "anthropic", AutoStartServer: false})
	result, err := backend.Execute(context.Background(), ExecuteOptions{
		Prompt:      "hello",
		ProjectPath: projectPath,
	})
	if err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !result.Success || result.Output != "ok" {
		t.Fatalf("unexpected result: %+v", result)
	}
	const want = "%2Fconfig%2FDesktop%2Fprojects%2Flinkedinopenclaw"
	if sessionHeader != want {
		t.Fatalf("session header = %q, want %q", sessionHeader, want)
	}
	if messageHeader != want {
		t.Fatalf("message header = %q, want %q", messageHeader, want)
	}
}

func TestOpenCodeBackendCreateSessionUsesDirectoryQuery(t *testing.T) {
	projectPath := "/tmp/project"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			t.Fatalf("path = %s, want /session", r.URL.Path)
		}
		if got := r.URL.Query().Get("directory"); got != projectPath {
			t.Fatalf("directory query = %q, want %q", got, projectPath)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if strings.TrimSpace(string(body)) != "{}" {
			t.Fatalf("body = %s, want {}", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"sess-query"}`))
	}))
	defer server.Close()

	backend := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: server.URL})
	id, err := backend.createSession(context.Background(), projectPath)
	if err != nil {
		t.Fatalf("createSession error = %v", err)
	}
	if id != "sess-query" {
		t.Fatalf("id = %q, want sess-query", id)
	}
}

func TestOpenCodeBackendCreateSessionFallsBackToLegacyPayload(t *testing.T) {
	projectPath := "/tmp/project"
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requestCount.Add(1) {
		case 1:
			if got := r.URL.Query().Get("directory"); got != projectPath {
				t.Fatalf("directory query = %q, want %q", got, projectPath)
			}
			http.Error(w, "query api unsupported", http.StatusBadRequest)
		case 2:
			if got := r.URL.Query().Get("directory"); got != "" {
				t.Fatalf("unexpected fallback directory query = %q", got)
			}
			payload := decodePayloadMap(t, mustReadBody(t, r))
			if payload["path"] != projectPath {
				t.Fatalf("payload path = %#v, want %q", payload["path"], projectPath)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"sess-legacy"}`))
		default:
			t.Fatalf("unexpected extra request %d", requestCount.Load())
		}
	}))
	defer server.Close()

	backend := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: server.URL})
	id, err := backend.createSession(context.Background(), projectPath)
	if err != nil {
		t.Fatalf("createSession error = %v", err)
	}
	if id != "sess-legacy" {
		t.Fatalf("id = %q, want sess-legacy", id)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("requestCount = %d, want 2", got)
	}
}

func TestOpenCodeBackendSendMessageModernPromptAsync(t *testing.T) {
	projectPath := "/tmp/project"
	promptCalled := make(chan struct{})
	eventSubscribed := make(chan struct{})
	var messageCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/event":
			if got := r.URL.Query().Get("directory"); got != projectPath {
				t.Fatalf("event directory = %q, want %q", got, projectPath)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("response writer is not a flusher")
			}
			close(eventSubscribed)
			<-promptCalled
			writeSSE(t, w, flusher, fmt.Sprintf(`{"type":"message.updated","properties":{"sessionID":"sess-1","info":{"id":"msg-1","sessionID":"sess-1","role":"assistant","providerID":"dokproxy","modelID":"gpt-5.4"}}}`))
			writeSSE(t, w, flusher, fmt.Sprintf(`{"type":"message.part.updated","properties":{"sessionID":"sess-1","part":{"id":"part-1","sessionID":"sess-1","messageID":"msg-1","type":"text","text":"OK"}}}`))
			writeSSE(t, w, flusher, fmt.Sprintf(`{"type":"message.part.updated","properties":{"sessionID":"sess-1","part":{"id":"part-2","sessionID":"sess-1","messageID":"msg-1","type":"step-finish","reason":"stop","tokens":{"input":10,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}}}`))
		case "/session/sess-1/prompt_async":
			if got := r.URL.Query().Get("directory"); got != projectPath {
				t.Fatalf("prompt_async directory = %q, want %q", got, projectPath)
			}
			payload := decodePayloadMap(t, mustReadBody(t, r))
			parts, ok := payload["parts"].([]interface{})
			if !ok || len(parts) != 1 {
				t.Fatalf("parts = %#v, want single-element slice", payload["parts"])
			}
			obj, ok := payload["model"].(map[string]interface{})
			if !ok || obj["providerID"] != "dokproxy" || obj["modelID"] != "gpt-5.4" {
				t.Fatalf("model = %#v, want object dokproxy/gpt-5.4", payload["model"])
			}
			close(promptCalled)
			w.WriteHeader(http.StatusNoContent)
		case "/session/sess-1/message":
			messageCalls.Add(1)
			http.Error(w, "legacy path should not be called", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	backend := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: server.URL, Model: "dokproxy/gpt-5.4", Provider: "dokproxy"})
	result, err := backend.sendMessage(context.Background(), "sess-1", ExecuteOptions{Prompt: "hello", ProjectPath: projectPath})
	if err != nil {
		t.Fatalf("sendMessage error = %v", err)
	}
	if !result.Success {
		t.Fatalf("result.Success = false, error = %q", result.Error)
	}
	if result.Output != "OK" {
		t.Fatalf("result.Output = %q, want OK", result.Output)
	}
	if result.Model != "dokproxy/gpt-5.4" {
		t.Fatalf("result.Model = %q, want dokproxy/gpt-5.4", result.Model)
	}
	if result.TokensInput != 10 || result.TokensOutput != 2 {
		t.Fatalf("tokens = %d/%d, want 10/2", result.TokensInput, result.TokensOutput)
	}
	if messageCalls.Load() != 0 {
		t.Fatalf("legacy message path called %d times", messageCalls.Load())
	}
	select {
	case <-eventSubscribed:
	default:
		t.Fatal("event stream was not subscribed")
	}
}

func TestOpenCodeBackendParseGlobalEventAutoApprovesProjectPermission(t *testing.T) {
	projectPath := "/tmp/project"
	var permissionReplyCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/permission/per-1/reply" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("directory"); got != projectPath {
			t.Fatalf("directory query = %q, want %q", got, projectPath)
		}
		permissionReplyCount.Add(1)
		body := decodePayloadMap(t, mustReadBody(t, r))
		if body["reply"] != "always" {
			t.Fatalf("reply = %#v, want always", body["reply"])
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`true`))
	}))
	defer server.Close()

	backend := NewOpenCodeBackend(&OpenCodeConfig{ServerURL: server.URL})
	events, done, err := backend.parseGlobalEvent(fmt.Sprintf(`{"directory":%q,"payload":{"type":"permission.asked","properties":{"id":"per-1","sessionID":"sess-1","permission":"external_directory","patterns":[%q]}}}`, projectPath, projectPath+"/*"), projectPath, "sess-1")
	if err != nil {
		t.Fatalf("parseGlobalEvent error = %v", err)
	}
	if done {
		t.Fatal("done = true, want false")
	}
	if permissionReplyCount.Load() != 1 {
		t.Fatalf("permissionReplyCount = %d, want 1", permissionReplyCount.Load())
	}
	if len(events) != 1 || events[0].Type != EventTypeProgress {
		t.Fatalf("events = %+v, want single progress event", events)
	}
}

func TestOpenCodeBackendParseGlobalEventBareMessagePartUpdated(t *testing.T) {
	backend := NewOpenCodeBackend(nil)
	events, done, err := backend.parseGlobalEvent(`{"type":"message.part.updated","properties":{"sessionID":"sess-1","part":{"id":"part-1","sessionID":"sess-1","messageID":"msg-1","type":"text","text":"OK"}}}`, "/tmp/project", "sess-1")
	if err != nil {
		t.Fatalf("parseGlobalEvent error = %v", err)
	}
	if done {
		t.Fatal("done = true, want false")
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Type != EventTypeText || events[0].Message != "OK" {
		t.Fatalf("event = %+v, want text OK", events[0])
	}
}

func mustReadBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

func decodePayloadMap(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v\nbody=%s", err, string(body))
	}
	return payload
}

func writeSSE(t *testing.T, w http.ResponseWriter, flusher http.Flusher, payload string) {
	t.Helper()
	if _, err := io.WriteString(w, "data: "+payload+"\n\n"); err != nil {
		t.Fatalf("write SSE: %v", err)
	}
	flusher.Flush()
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
