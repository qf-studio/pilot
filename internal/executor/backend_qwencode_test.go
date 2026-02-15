package executor

import (
	"testing"
)

func TestNewQwenCodeBackend(t *testing.T) {
	tests := []struct {
		name          string
		config        *QwenCodeConfig
		expectCommand string
	}{
		{
			name:          "nil config uses defaults",
			config:        nil,
			expectCommand: "qwen",
		},
		{
			name:          "empty command uses default",
			config:        &QwenCodeConfig{Command: ""},
			expectCommand: "qwen",
		},
		{
			name:          "custom command",
			config:        &QwenCodeConfig{Command: "/custom/qwen"},
			expectCommand: "/custom/qwen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewQwenCodeBackend(tt.config)
			if backend == nil {
				t.Fatal("NewQwenCodeBackend returned nil")
			}
			if backend.config.Command != tt.expectCommand {
				t.Errorf("Command = %q, want %q", backend.config.Command, tt.expectCommand)
			}
		})
	}
}

func TestQwenCodeBackendName(t *testing.T) {
	backend := NewQwenCodeBackend(nil)
	if backend.Name() != BackendTypeQwenCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeQwenCode)
	}
}

func TestQwenCodeBackendIsAvailable(t *testing.T) {
	backend := NewQwenCodeBackend(&QwenCodeConfig{
		Command: "/nonexistent/path/to/qwen",
	})

	if backend.IsAvailable() {
		t.Error("IsAvailable() should return false for non-existent command")
	}
}

func TestQwenCodeBackendParseStreamEvent(t *testing.T) {
	backend := NewQwenCodeBackend(nil)

	tests := []struct {
		name           string
		line           string
		expectType     BackendEventType
		expectTool     string
		expectError    bool
		expectSession  string
		expectMessage  string
		expectTokensIn int64
	}{
		{
			name:          "system init",
			line:          `{"type":"system","subtype":"init","session_id":"qwen-sess-123"}`,
			expectType:    EventTypeInit,
			expectSession: "qwen-sess-123",
		},
		{
			name:       "tool use read_file → Read",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"read_file","input":{"file_path":"/test.go"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Read",
		},
		{
			name:       "tool use write_file → Write",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"write_file","input":{"file_path":"/test.go","content":"hello"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Write",
		},
		{
			name:       "tool use run_shell_command → Bash",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"run_shell_command","input":{"command":"go test ./..."}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Bash",
		},
		{
			name:       "tool use grep_search → Grep",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"grep_search","input":{"pattern":"func main"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Grep",
		},
		{
			name:       "tool use glob → Glob",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"glob","input":{"pattern":"**/*.go"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Glob",
		},
		{
			name:       "tool use edit → Edit",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"edit","input":{"file_path":"/test.go"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Edit",
		},
		{
			name:       "tool use list_directory → Bash",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"list_directory","input":{"path":"."}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Bash",
		},
		{
			name:       "tool use task → Task (passthrough)",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"task","input":{"prompt":"research this"}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "Task",
		},
		{
			name:       "MCP tool passes through unchanged",
			line:       `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__github__list_issues","input":{}}]}}`,
			expectType: EventTypeToolUse,
			expectTool: "mcp__github__list_issues",
		},
		{
			name:          "text content",
			line:          `{"type":"assistant","message":{"content":[{"type":"text","text":"Analyzing the codebase..."}]}}`,
			expectType:    EventTypeText,
			expectMessage: "Analyzing the codebase...",
		},
		{
			name:       "result success",
			line:       `{"type":"result","result":"Task completed","is_error":false}`,
			expectType: EventTypeResult,
		},
		{
			name:        "result error",
			line:        `{"type":"result","result":"Failed to compile","is_error":true}`,
			expectType:  EventTypeResult,
			expectError: true,
		},
		{
			name:       "invalid json",
			line:       `not valid json at all`,
			expectType: EventTypeText,
		},
		{
			name:       "user tool_result block (Qwen-style)",
			line:       `{"type":"user","message":{"content":[{"type":"tool_result","text":"file contents here","is_error":false}]}}`,
			expectType: EventTypeToolResult,
		},
		{
			name:        "user tool_result with error",
			line:        `{"type":"user","message":{"content":[{"type":"tool_result","text":"permission denied","is_error":true}]}}`,
			expectType:  EventTypeToolResult,
			expectError: true,
		},
		{
			name:           "usage info extracted",
			line:           `{"type":"result","result":"Done","usage":{"input_tokens":5000,"output_tokens":2000},"model":"qwen3-coder-plus"}`,
			expectType:     EventTypeResult,
			expectTokensIn: 5000,
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
			if tt.expectSession != "" && event.SessionID != tt.expectSession {
				t.Errorf("SessionID = %q, want %q", event.SessionID, tt.expectSession)
			}
			if tt.expectMessage != "" && event.Message != tt.expectMessage {
				t.Errorf("Message = %q, want %q", event.Message, tt.expectMessage)
			}
			if tt.expectTokensIn > 0 && event.TokensInput != tt.expectTokensIn {
				t.Errorf("TokensInput = %d, want %d", event.TokensInput, tt.expectTokensIn)
			}
			if event.Raw != tt.line {
				t.Errorf("Raw = %q, want %q", event.Raw, tt.line)
			}
		})
	}
}

func TestQwenCodeBackendParseUsageInfo(t *testing.T) {
	backend := NewQwenCodeBackend(nil)

	line := `{"type":"result","result":"Done","usage":{"input_tokens":100,"output_tokens":50},"model":"qwen3-coder-plus"}`
	event := backend.parseStreamEvent(line)

	if event.TokensInput != 100 {
		t.Errorf("TokensInput = %d, want 100", event.TokensInput)
	}
	if event.TokensOutput != 50 {
		t.Errorf("TokensOutput = %d, want 50", event.TokensOutput)
	}
	if event.Model != "qwen3-coder-plus" {
		t.Errorf("Model = %q, want qwen3-coder-plus", event.Model)
	}
}

func TestQwenCodeBackendParseToolInput(t *testing.T) {
	backend := NewQwenCodeBackend(nil)

	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"run_shell_command","input":{"command":"go test ./..."}}]}}`
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

func TestNormalizeQwenToolName(t *testing.T) {
	tests := []struct {
		input  string
		expect string
	}{
		{"read_file", "Read"},
		{"write_file", "Write"},
		{"edit", "Edit"},
		{"run_shell_command", "Bash"},
		{"grep_search", "Grep"},
		{"glob", "Glob"},
		{"list_directory", "Bash"},
		{"web_fetch", "WebFetch"},
		{"web_search", "WebSearch"},
		{"todo_write", "TodoWrite"},
		{"save_memory", "TodoWrite"},
		{"task", "Task"},
		{"skill", "Skill"},
		{"lsp", "Bash"},
		{"exit_plan_mode", "ExitPlanMode"},
		// Unknown tools pass through
		{"unknown_tool", "unknown_tool"},
		{"custom_mcp_tool", "custom_mcp_tool"},
		// MCP tools pass through unchanged
		{"mcp__github__create_issue", "mcp__github__create_issue"},
		{"mcp__sqlite__query", "mcp__sqlite__query"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeQwenToolName(tt.input)
			if result != tt.expect {
				t.Errorf("normalizeQwenToolName(%q) = %q, want %q", tt.input, result, tt.expect)
			}
		})
	}
}

func TestQwenCodeBuildArgs(t *testing.T) {
	tests := []struct {
		name       string
		config     *QwenCodeConfig
		opts       ExecuteOptions
		expectArgs []string
		notExpect  []string
	}{
		{
			name:   "basic prompt",
			config: &QwenCodeConfig{Command: "qwen"},
			opts: ExecuteOptions{
				Prompt:      "fix the bug",
				ProjectPath: "/project",
			},
			expectArgs: []string{"-p", "fix the bug", "--output-format", "stream-json", "--yolo"},
			notExpect:  []string{"--model", "--resume", "--verbose", "--dangerously-skip-permissions", "--effort"},
		},
		{
			name:   "with model",
			config: &QwenCodeConfig{Command: "qwen"},
			opts: ExecuteOptions{
				Prompt: "fix the bug",
				Model:  "qwen3-coder-plus",
			},
			expectArgs: []string{"--model", "qwen3-coder-plus", "--yolo"},
		},
		{
			name:   "with resume",
			config: &QwenCodeConfig{Command: "qwen", UseSessionResume: true},
			opts: ExecuteOptions{
				Prompt:          "continue",
				ResumeSessionID: "sess-abc",
			},
			expectArgs: []string{"--resume", "sess-abc"},
		},
		{
			name:   "resume disabled in config",
			config: &QwenCodeConfig{Command: "qwen", UseSessionResume: false},
			opts: ExecuteOptions{
				Prompt:          "continue",
				ResumeSessionID: "sess-abc",
			},
			notExpect: []string{"--resume"},
		},
		{
			name:   "effort silently ignored",
			config: &QwenCodeConfig{Command: "qwen"},
			opts: ExecuteOptions{
				Prompt: "fix bug",
				Effort: "max",
			},
			notExpect: []string{"--effort"},
		},
		{
			name:   "from-pr silently ignored",
			config: &QwenCodeConfig{Command: "qwen"},
			opts: ExecuteOptions{
				Prompt: "fix CI",
				FromPR: 42,
			},
			notExpect: []string{"--from-pr"},
		},
		{
			name:   "extra args appended",
			config: &QwenCodeConfig{Command: "qwen", ExtraArgs: []string{"--debug"}},
			opts: ExecuteOptions{
				Prompt: "test",
			},
			expectArgs: []string{"--debug"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewQwenCodeBackend(tt.config)
			args := backend.buildArgs(tt.opts)

			for _, expected := range tt.expectArgs {
				found := false
				for _, arg := range args {
					if arg == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("args missing %q, got %v", expected, args)
				}
			}

			for _, notExpected := range tt.notExpect {
				for _, arg := range args {
					if arg == notExpected {
						t.Errorf("args should not contain %q, got %v", notExpected, args)
						break
					}
				}
			}
		})
	}
}

func TestClassifyQwenCodeError(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		expectType QwenCodeErrorType
	}{
		{
			name:       "rate limit - 429",
			stderr:     "Error: 429 Too Many Requests",
			expectType: QwenErrorTypeRateLimit,
		},
		{
			name:       "rate limit - rate limit text",
			stderr:     "Error: Rate limit exceeded, try again later",
			expectType: QwenErrorTypeRateLimit,
		},
		{
			name:       "rate limit - hit your limit",
			stderr:     "Error: You've hit your limit",
			expectType: QwenErrorTypeRateLimit,
		},
		{
			name:       "invalid config - invalid model",
			stderr:     "Error: Invalid model specified",
			expectType: QwenErrorTypeInvalidConfig,
		},
		{
			name:       "invalid config - unknown option",
			stderr:     "Error: Unknown option --foobar",
			expectType: QwenErrorTypeInvalidConfig,
		},
		{
			name:       "api error - authentication",
			stderr:     "Error: Authentication failed. Please check your API key.",
			expectType: QwenErrorTypeAPIError,
		},
		{
			name:       "api error - 401",
			stderr:     "HTTP 401: Unauthorized",
			expectType: QwenErrorTypeAPIError,
		},
		{
			name:       "timeout - killed",
			stderr:     "signal: killed",
			expectType: QwenErrorTypeTimeout,
		},
		{
			name:       "timeout - timeout",
			stderr:     "Error: Request timeout",
			expectType: QwenErrorTypeTimeout,
		},
		{
			name:       "unknown error",
			stderr:     "Some random error message",
			expectType: QwenErrorTypeUnknown,
		},
		{
			name:       "empty stderr",
			stderr:     "",
			expectType: QwenErrorTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyQwenCodeError(tt.stderr, nil)
			if err.Type != tt.expectType {
				t.Errorf("classifyQwenCodeError() type = %q, want %q", err.Type, tt.expectType)
			}
			if tt.stderr != "" && err.Stderr != tt.stderr {
				t.Errorf("classifyQwenCodeError() stderr = %q, want %q", err.Stderr, tt.stderr)
			}
		})
	}
}

func TestQwenCodeError_Error(t *testing.T) {
	t.Run("with stderr", func(t *testing.T) {
		err := &QwenCodeError{
			Type:    QwenErrorTypeRateLimit,
			Message: "Rate limit hit",
			Stderr:  "detailed stderr",
		}
		errStr := err.Error()
		if errStr != "rate_limit: Rate limit hit (stderr: detailed stderr)" {
			t.Errorf("Error() = %q, unexpected format", errStr)
		}
	})

	t.Run("without stderr", func(t *testing.T) {
		err := &QwenCodeError{
			Type:    QwenErrorTypeUnknown,
			Message: "Unknown error",
			Stderr:  "",
		}
		errStr := err.Error()
		if errStr != "unknown: Unknown error" {
			t.Errorf("Error() = %q, unexpected format", errStr)
		}
	})
}

func TestBackendFactoryQwenCode(t *testing.T) {
	config := &BackendConfig{
		Type: BackendTypeQwenCode,
		QwenCode: &QwenCodeConfig{
			Command: "qwen",
		},
	}

	backend, err := NewBackend(config)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("NewBackend() returned nil")
	}
	if backend.Name() != BackendTypeQwenCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeQwenCode)
	}
}

func TestBackendFactoryQwenCodeNilConfig(t *testing.T) {
	config := &BackendConfig{
		Type: BackendTypeQwenCode,
		// QwenCode is nil — should use defaults
	}

	backend, err := NewBackend(config)
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	if backend == nil {
		t.Fatal("NewBackend() returned nil")
	}
	if backend.Name() != BackendTypeQwenCode {
		t.Errorf("Name() = %q, want %q", backend.Name(), BackendTypeQwenCode)
	}
}
