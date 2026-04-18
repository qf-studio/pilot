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

func TestResolveModel(t *testing.T) {
	tests := []struct {
		name     string
		config   *BackendConfig
		explicit string
		want     string
	}{
		{
			name:     "nil_config_returns_explicit",
			config:   nil,
			explicit: "claude-haiku-4-5-20251001",
			want:     "claude-haiku-4-5-20251001",
		},
		{
			name:     "empty_default_returns_explicit",
			config:   &BackendConfig{},
			explicit: "claude-haiku-4-5-20251001",
			want:     "claude-haiku-4-5-20251001",
		},
		{
			name:     "default_model_overrides",
			config:   &BackendConfig{DefaultModel: "glm-5.1"},
			explicit: "claude-haiku-4-5-20251001",
			want:     "glm-5.1",
		},
		{
			name:     "default_model_with_empty_explicit",
			config:   &BackendConfig{DefaultModel: "glm-5.1"},
			explicit: "",
			want:     "glm-5.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.ResolveModel(tt.explicit)
			if got != tt.want {
				t.Errorf("ResolveModel(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}
}

func TestResolveAPIBaseURL(t *testing.T) {
	tests := []struct {
		name   string
		config *BackendConfig
		want   string
	}{
		{
			name:   "nil_config_returns_anthropic_default",
			config: nil,
			want:   "https://api.anthropic.com",
		},
		{
			name:   "empty_url_returns_anthropic_default",
			config: &BackendConfig{},
			want:   "https://api.anthropic.com",
		},
		{
			name:   "custom_url_overrides",
			config: &BackendConfig{APIBaseURL: "https://api.z.ai/api/anthropic"},
			want:   "https://api.z.ai/api/anthropic",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.ResolveAPIBaseURL()
			if got != tt.want {
				t.Errorf("ResolveAPIBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBackendConfigAPIAuthToken covers the GH-2371 field: default empty,
// and round-trips a plain value. Env expansion itself is done during
// config.Load (os.ExpandEnv) — tested separately in internal/config.
func TestBackendConfigAPIAuthToken(t *testing.T) {
	var zero BackendConfig
	if zero.APIAuthToken != "" {
		t.Errorf("zero-value APIAuthToken = %q, want empty", zero.APIAuthToken)
	}

	cfg := &BackendConfig{APIAuthToken: "zai-fake-token"}
	if cfg.APIAuthToken != "zai-fake-token" {
		t.Errorf("APIAuthToken = %q, want %q", cfg.APIAuthToken, "zai-fake-token")
	}
}
