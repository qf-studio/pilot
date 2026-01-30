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

	// Model specifies the model to use for execution (e.g., "claude-haiku", "claude-opus").
	// If empty, the backend's default model is used.
	Model string

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

	// AutoCreatePR controls whether PRs are created by default after successful execution.
	// Default: true. Use --no-pr flag to disable for individual tasks.
	AutoCreatePR *bool `yaml:"auto_create_pr,omitempty"`

	// DirectCommit enables committing directly to main without branches or PRs.
	// DANGER: Requires BOTH this config option AND --direct-commit CLI flag.
	// Intended for users who rely on manual QA instead of code review.
	DirectCommit bool `yaml:"direct_commit,omitempty"`

	// DetectEphemeral enables automatic detection of ephemeral tasks (serve, run, etc.)
	// that shouldn't create PRs. When true (default), commands like "serve the app"
	// or "run dev server" will execute without creating a PR.
	DetectEphemeral *bool `yaml:"detect_ephemeral,omitempty"`

	// ClaudeCode contains Claude Code specific settings
	ClaudeCode *ClaudeCodeConfig `yaml:"claude_code,omitempty"`

	// OpenCode contains OpenCode specific settings
	OpenCode *OpenCodeConfig `yaml:"opencode,omitempty"`

	// ModelRouting contains model selection based on task complexity
	ModelRouting *ModelRoutingConfig `yaml:"model_routing,omitempty"`

	// Timeout contains execution timeout settings
	Timeout *TimeoutConfig `yaml:"timeout,omitempty"`

	// Decompose contains auto-decomposition settings for complex tasks
	Decompose *DecomposeConfig `yaml:"decompose,omitempty"`
}

// ModelRoutingConfig controls which model to use based on task complexity.
// Enables cost optimization by using cheaper models for simple tasks.
//
// Example YAML configuration:
//
//	executor:
//	  model_routing:
//	    enabled: true
//	    trivial: "claude-haiku"    # Typos, log additions, renames
//	    simple: "claude-sonnet"    # Small fixes, add fields
//	    medium: "claude-sonnet"    # Standard feature work
//	    complex: "claude-opus"     # Refactors, migrations
//
// Task complexity is auto-detected from the issue description and labels.
// When enabled, reduces costs by ~40% while maintaining quality for complex tasks.
type ModelRoutingConfig struct {
	// Enabled controls whether model routing is active.
	// When false (default), the orchestrator's model setting is used for all tasks.
	Enabled bool `yaml:"enabled"`

	// Trivial is the model for trivial tasks (typos, log additions, renames).
	// Default: "claude-haiku"
	Trivial string `yaml:"trivial"`

	// Simple is the model for simple tasks (small fixes, add field, update config).
	// Default: "claude-sonnet"
	Simple string `yaml:"simple"`

	// Medium is the model for standard feature work (new endpoints, components).
	// Default: "claude-sonnet"
	Medium string `yaml:"medium"`

	// Complex is the model for architectural work (refactors, migrations, new systems).
	// Default: "claude-opus"
	Complex string `yaml:"complex"`
}

// TimeoutConfig controls execution timeouts to prevent stuck tasks.
type TimeoutConfig struct {
	// Default is the default timeout for all tasks
	Default string `yaml:"default"`

	// Trivial is the timeout for trivial tasks (shorter)
	Trivial string `yaml:"trivial"`

	// Simple is the timeout for simple tasks
	Simple string `yaml:"simple"`

	// Medium is the timeout for medium tasks
	Medium string `yaml:"medium"`

	// Complex is the timeout for complex tasks (longer)
	Complex string `yaml:"complex"`
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
	autoCreatePR := true
	detectEphemeral := true
	return &BackendConfig{
		Type:            "claude-code",
		AutoCreatePR:    &autoCreatePR,
		DetectEphemeral: &detectEphemeral,
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
		ModelRouting: DefaultModelRoutingConfig(),
		Timeout:      DefaultTimeoutConfig(),
		Decompose:    DefaultDecomposeConfig(),
	}
}

// DefaultModelRoutingConfig returns default model routing configuration.
// Model routing is disabled by default; when enabled, uses Haiku for trivial,
// Sonnet for simple/medium, and Opus for complex tasks.
//
// Complexity detection criteria:
//   - Trivial: Single-file changes, typos, logging, renames
//   - Simple: Small fixes, add/remove fields, config updates
//   - Medium: New endpoints, components, moderate refactoring
//   - Complex: Architecture changes, multi-file refactors, migrations
func DefaultModelRoutingConfig() *ModelRoutingConfig {
	return &ModelRoutingConfig{
		Enabled: false,
		Trivial: "claude-haiku",
		Simple:  "claude-sonnet",
		Medium:  "claude-sonnet",
		Complex: "claude-opus",
	}
}

// DefaultTimeoutConfig returns default timeout configuration.
// Timeouts are calibrated to prevent stuck tasks while allowing complex work.
func DefaultTimeoutConfig() *TimeoutConfig {
	return &TimeoutConfig{
		Default: "30m",
		Trivial: "5m",
		Simple:  "10m",
		Medium:  "30m",
		Complex: "60m",
	}
}

// BackendType constants for configuration.
const (
	BackendTypeClaudeCode = "claude-code"
	BackendTypeOpenCode   = "opencode"
)
