package executor

import (
	"testing"
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
