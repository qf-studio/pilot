package executor

import (
	"testing"
)

func TestNewClaudeCodeBackend(t *testing.T) {
	tests := []struct {
		name           string
		config         *ClaudeCodeConfig
		expectCommand  string
	}{
		{
			name:           "nil config uses defaults",
			config:         nil,
			expectCommand:  "claude",
		},
		{
			name:           "empty command uses default",
			config:         &ClaudeCodeConfig{Command: ""},
			expectCommand:  "claude",
		},
		{
			name:           "custom command",
			config:         &ClaudeCodeConfig{Command: "/custom/claude"},
			expectCommand:  "/custom/claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewClaudeCodeBackend(tt.config)
			if backend == nil {
				t.Fatal("NewClaudeCodeBackend returned nil")
			}
			if backend.config.Command != tt.expectCommand {
				t.Errorf("Command = %q, want %q", backend.config.Command, tt.expectCommand)
			}
		})
	}
}

func TestClaudeCodeBackendName(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)
	if backend.Name() != BackendTypeClaudeCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeClaudeCode)
	}
}

func TestClaudeCodeBackendParseStreamEvent(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	tests := []struct {
		name        string
		line        string
		expectType  BackendEventType
		expectTool  string
		expectError bool
	}{
		{
			name:       "system init",
			line:       `{"type":"system","subtype":"init","session_id":"abc"}`,
			expectType: EventTypeInit,
		},
		{
			name:       "tool use Read",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/test.go"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Read",
		},
		{
			name:       "tool use Write",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/test.go"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Write",
		},
		{
			name:       "text content",
			line:       `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`,
			expectType: EventTypeText,
		},
		{
			name:       "result success",
			line:       `{"type":"result","result":"Done!","is_error":false}`,
			expectType: EventTypeResult,
		},
		{
			name:        "result error",
			line:        `{"type":"result","result":"Failed","is_error":true}`,
			expectType:  EventTypeResult,
			expectError: true,
		},
		{
			name:       "invalid json",
			line:       `not valid json`,
			expectType: EventTypeText,
		},
		{
			name:       "user tool result",
			line:       `{"type":"user","tool_use_result":{"tool_use_id":"123","type":"tool_result","content":"[main abc1234] commit"}}`,
			expectType: EventTypeToolResult,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := backend.parseStreamEvent(tt.line)

			if event.Type != tt.expectType {
				t.Errorf("Type = %q, want %q", event.Type, tt.expectType)
			}
			if tt.expectTool != "" && event.ToolName != tt.expectTool {
				t.Errorf("ToolName = %q, want %q", event.ToolName, tt.expectTool)
			}
			if tt.expectError && !event.IsError {
				t.Error("IsError should be true")
			}
			if event.Raw != tt.line {
				t.Errorf("Raw = %q, want %q", event.Raw, tt.line)
			}
		})
	}
}

func TestClaudeCodeBackendParseUsageInfo(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	line := `{"type":"result","result":"Done","usage":{"input_tokens":100,"output_tokens":50},"model":"claude-sonnet-4-5"}`
	event := backend.parseStreamEvent(line)

	if event.TokensInput != 100 {
		t.Errorf("TokensInput = %d, want 100", event.TokensInput)
	}
	if event.TokensOutput != 50 {
		t.Errorf("TokensOutput = %d, want 50", event.TokensOutput)
	}
	if event.Model != "claude-sonnet-4-5" {
		t.Errorf("Model = %q, want claude-sonnet-4-5", event.Model)
	}
}

func TestClaudeCodeBackendParseToolInput(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`
	event := backend.parseStreamEvent(line)

	if event.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want Bash", event.ToolName)
	}
	if event.ToolInput == nil {
		t.Fatal("ToolInput should not be nil")
	}
	if cmd, ok := event.ToolInput["command"].(string); !ok || cmd != "go test ./..." {
		t.Errorf("ToolInput[command] = %v, want 'go test ./...'", event.ToolInput["command"])
	}
}

func TestClaudeCodeBackendIsAvailable(t *testing.T) {
	// Test with non-existent command
	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{
		Command: "/nonexistent/path/to/claude",
	})

	// Should return false for non-existent command
	if backend.IsAvailable() {
		t.Error("IsAvailable() should return false for non-existent command")
	}
}
