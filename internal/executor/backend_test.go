package executor

import (
	"testing"
)

func TestBackendEventTypes(t *testing.T) {
	types := []BackendEventType{
		EventTypeInit,
		EventTypeText,
		EventTypeToolUse,
		EventTypeToolResult,
		EventTypeResult,
		EventTypeError,
		EventTypeProgress,
	}

	for _, eventType := range types {
		if eventType == "" {
			t.Error("event type should not be empty")
		}
	}
}

func TestBackendEvent(t *testing.T) {
	event := BackendEvent{
		Type:         EventTypeToolUse,
		Raw:          `{"type":"tool_use","name":"Read"}`,
		Phase:        "Exploring",
		Message:      "Reading files",
		ToolName:     "Read",
		ToolInput:    map[string]interface{}{"file_path": "/test.go"},
		TokensInput:  100,
		TokensOutput: 50,
		Model:        "claude-sonnet-4-6",
	}

	if event.Type != EventTypeToolUse {
		t.Errorf("Type = %q, want tool_use", event.Type)
	}
	if event.ToolName != "Read" {
		t.Errorf("ToolName = %q, want Read", event.ToolName)
	}
	if event.TokensInput != 100 {
		t.Errorf("TokensInput = %d, want 100", event.TokensInput)
	}
}

func TestBackendResult(t *testing.T) {
	result := &BackendResult{
		Success:      true,
		Output:       "Task completed",
		TokensInput:  1000,
		TokensOutput: 500,
		Model:        "claude-sonnet-4-6",
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if result.TokensInput+result.TokensOutput != 1500 {
		t.Errorf("Total tokens = %d, want 1500", result.TokensInput+result.TokensOutput)
	}
}

func TestDefaultBackendConfig(t *testing.T) {
	config := DefaultBackendConfig()

	if config == nil {
		t.Fatal("DefaultBackendConfig returned nil")
	}
	if config.Type != BackendTypeClaudeCode {
		t.Errorf("Type = %q, want %q", config.Type, BackendTypeClaudeCode)
	}
	if config.ClaudeCode == nil {
		t.Error("ClaudeCode config should not be nil")
	}
	if config.ClaudeCode.Command != "claude" {
		t.Errorf("ClaudeCode.Command = %q, want claude", config.ClaudeCode.Command)
	}
	if config.OpenCode == nil {
		t.Error("OpenCode config should not be nil")
	}
	if config.OpenCode.ServerURL != "http://127.0.0.1:4096" {
		t.Errorf("OpenCode.ServerURL = %q, want http://127.0.0.1:4096", config.OpenCode.ServerURL)
	}
}

func TestBackendConfigTypes(t *testing.T) {
	if BackendTypeClaudeCode != "claude-code" {
		t.Errorf("BackendTypeClaudeCode = %q, want claude-code", BackendTypeClaudeCode)
	}
	if BackendTypeOpenCode != "opencode" {
		t.Errorf("BackendTypeOpenCode = %q, want opencode", BackendTypeOpenCode)
	}
}

func TestClaudeCodeConfig(t *testing.T) {
	config := &ClaudeCodeConfig{
		Command:   "claude-custom",
		ExtraArgs: []string{"--flag1", "--flag2"},
	}

	if config.Command != "claude-custom" {
		t.Errorf("Command = %q, want claude-custom", config.Command)
	}
	if len(config.ExtraArgs) != 2 {
		t.Errorf("ExtraArgs length = %d, want 2", len(config.ExtraArgs))
	}
}

func TestOpenCodeConfig(t *testing.T) {
	config := &OpenCodeConfig{
		ServerURL:       "http://localhost:5000",
		Model:           "anthropic/claude-opus-4",
		Provider:        "anthropic",
		AutoStartServer: true,
		ServerCommand:   "opencode serve --port 5000",
	}

	if config.ServerURL != "http://localhost:5000" {
		t.Errorf("ServerURL = %q, want http://localhost:5000", config.ServerURL)
	}
	if config.Model != "anthropic/claude-opus-4" {
		t.Errorf("Model = %q, want anthropic/claude-opus-4", config.Model)
	}
	if !config.AutoStartServer {
		t.Error("AutoStartServer should be true")
	}
}
