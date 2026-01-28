package executor

import (
	"context"
)

// Backend defines the interface for AI execution backends.
// Implementations handle the specifics of invoking different AI coding agents
// (Claude Code, OpenCode, etc.) while providing a unified interface to the Runner.
type Backend interface {
	// Name returns the backend identifier (e.g., "claude-code", "opencode")
	Name() string

	// Execute runs a prompt against the backend and streams events.
	// The eventHandler is called for each event received from the backend.
	// Returns the final result or error.
	Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error)

	// IsAvailable checks if the backend is properly configured and accessible.
	IsAvailable() bool
}

// ExecuteOptions contains parameters for backend execution.
type ExecuteOptions struct {
	// Prompt is the full prompt to send to the AI backend
	Prompt string

	// ProjectPath is the working directory for execution
	ProjectPath string

	// Verbose enables detailed output logging
	Verbose bool

	// EventHandler receives streaming events during execution
	// The handler receives the raw event line from the backend
	EventHandler func(event BackendEvent)
}

// BackendEvent represents a streaming event from the backend.
// Each backend maps its native events to this common format.
type BackendEvent struct {
	// Type identifies the event category
	Type BackendEventType

	// Raw contains the original event data (JSON string)
	Raw string

	// Phase indicates the current execution phase (if detectable)
	Phase string

	// Message contains a human-readable description
	Message string

	// ToolName is set for tool_use events
	ToolName string

	// ToolInput contains tool parameters for tool_use events
	ToolInput map[string]interface{}

	// ToolResult contains the output for tool_result events
	ToolResult string

	// IsError indicates if this is an error event
	IsError bool

	// TokensInput is the input token count (if available)
	TokensInput int64

	// TokensOutput is the output token count (if available)
	TokensOutput int64

	// Model is the model name used (if available)
	Model string
}

// BackendEventType categorizes backend events.
type BackendEventType string

const (
	// EventTypeInit indicates the backend is starting
	EventTypeInit BackendEventType = "init"

	// EventTypeText indicates a text/message block
	EventTypeText BackendEventType = "text"

	// EventTypeToolUse indicates a tool is being invoked
	EventTypeToolUse BackendEventType = "tool_use"

	// EventTypeToolResult indicates a tool execution result
	EventTypeToolResult BackendEventType = "tool_result"

	// EventTypeResult indicates final execution result
	EventTypeResult BackendEventType = "result"

	// EventTypeError indicates an error occurred
	EventTypeError BackendEventType = "error"

	// EventTypeProgress indicates a progress update
	EventTypeProgress BackendEventType = "progress"
)

// BackendResult contains the outcome of a backend execution.
type BackendResult struct {
	// Success indicates whether execution completed successfully
	Success bool

	// Output contains the final output text
	Output string

	// Error contains error details if execution failed
	Error string

	// TokensInput is the total input tokens consumed
	TokensInput int64

	// TokensOutput is the total output tokens generated
	TokensOutput int64

	// Model is the model used for execution
	Model string
}

// BackendConfig contains configuration for executor backends.
type BackendConfig struct {
	// Type specifies which backend to use ("claude-code" or "opencode")
	Type string `yaml:"type"`

	// ClaudeCode contains Claude Code specific settings
	ClaudeCode *ClaudeCodeConfig `yaml:"claude_code,omitempty"`

	// OpenCode contains OpenCode specific settings
	OpenCode *OpenCodeConfig `yaml:"opencode,omitempty"`
}

// ClaudeCodeConfig contains Claude Code backend configuration.
type ClaudeCodeConfig struct {
	// Command is the path to the claude CLI (default: "claude")
	Command string `yaml:"command,omitempty"`

	// ExtraArgs are additional arguments to pass to the CLI
	ExtraArgs []string `yaml:"extra_args,omitempty"`
}

// OpenCodeConfig contains OpenCode backend configuration.
type OpenCodeConfig struct {
	// ServerURL is the OpenCode server URL (default: "http://127.0.0.1:4096")
	ServerURL string `yaml:"server_url,omitempty"`

	// Model is the model to use (e.g., "anthropic/claude-sonnet-4")
	Model string `yaml:"model,omitempty"`

	// Provider is the provider name (e.g., "anthropic")
	Provider string `yaml:"provider,omitempty"`

	// AutoStartServer starts the server if not running
	AutoStartServer bool `yaml:"auto_start_server,omitempty"`

	// ServerCommand is the command to start the server (default: "opencode serve")
	ServerCommand string `yaml:"server_command,omitempty"`
}

// DefaultBackendConfig returns default backend configuration.
func DefaultBackendConfig() *BackendConfig {
	return &BackendConfig{
		Type: "claude-code",
		ClaudeCode: &ClaudeCodeConfig{
			Command: "claude",
		},
		OpenCode: &OpenCodeConfig{
			ServerURL:       "http://127.0.0.1:4096",
			Model:           "anthropic/claude-sonnet-4",
			Provider:        "anthropic",
			AutoStartServer: true,
			ServerCommand:   "opencode serve",
		},
	}
}

// BackendType constants for configuration.
const (
	BackendTypeClaudeCode = "claude-code"
	BackendTypeOpenCode   = "opencode"
)
