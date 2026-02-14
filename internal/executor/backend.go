package executor

import (
	"context"
	"time"
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

	// Effort specifies the effort level for execution (e.g., "low", "medium", "high", "max").
	// If empty, the backend's default effort is used (high).
	// Maps to Claude API output_config.effort or Claude Code --effort flag.
	Effort string

	// EventHandler receives streaming events during execution
	// The handler receives the raw event line from the backend
	EventHandler func(event BackendEvent)

	// HeartbeatCallback is invoked when subprocess heartbeat timeout is detected.
	// The callback receives the process PID and the time since the last event.
	// After callback invocation, the process will be killed.
	HeartbeatCallback func(pid int, lastEventAge time.Duration)

	// WatchdogTimeout is the absolute time limit after which the subprocess will be
	// forcibly killed. This is a safety net for processes that ignore context cancellation.
	// When set (> 0), a watchdog goroutine will kill the process after this duration.
	WatchdogTimeout time.Duration

	// WatchdogCallback is invoked when the watchdog kills a subprocess.
	// The callback receives the process PID and the watchdog timeout duration.
	// Called BEFORE the process is killed, allowing for alert emission.
	WatchdogCallback func(pid int, watchdogTimeout time.Duration)
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

	// SkipSelfReview disables the self-review phase before PR creation.
	// When false (default), Pilot runs a self-review phase after quality gates pass
	// to catch issues like unwired config, undefined methods, or incomplete implementations.
	SkipSelfReview bool `yaml:"skip_self_review,omitempty"`

	// ClaudeCode contains Claude Code specific settings
	ClaudeCode *ClaudeCodeConfig `yaml:"claude_code,omitempty"`

	// OpenCode contains OpenCode specific settings
	OpenCode *OpenCodeConfig `yaml:"opencode,omitempty"`

	// ModelRouting contains model selection based on task complexity
	ModelRouting *ModelRoutingConfig `yaml:"model_routing,omitempty"`

	// Timeout contains execution timeout settings
	Timeout *TimeoutConfig `yaml:"timeout,omitempty"`

	// EffortRouting contains effort level selection based on task complexity
	EffortRouting *EffortRoutingConfig `yaml:"effort_routing,omitempty"`

	// EffortClassifier contains LLM-based effort classification settings (GH-727)
	EffortClassifier *EffortClassifierConfig `yaml:"effort_classifier,omitempty"`

	// Decompose contains auto-decomposition settings for complex tasks
	Decompose *DecomposeConfig `yaml:"decompose,omitempty"`

	// IntentJudge contains intent alignment settings for diff-vs-ticket verification
	IntentJudge *IntentJudgeConfig `yaml:"intent_judge,omitempty"`

	// Navigator contains Navigator auto-init settings
	Navigator *NavigatorConfig `yaml:"navigator,omitempty"`

	// UseWorktree enables git worktree isolation for execution.
	// When true, Pilot creates a temporary worktree for each task, allowing
	// execution even when the user has uncommitted changes in their working directory.
	// Default: false (opt-in feature)
	UseWorktree bool `yaml:"use_worktree,omitempty"`

	// WorktreePoolSize sets the number of pre-created worktrees to pool.
	// When > 0, worktrees are reused across tasks in sequential mode, saving 500ms-2s per task.
	// Pool paths: /tmp/pilot-worktree-pool-N/
	// Set to 0 to disable pooling (current behavior).
	// Default: 0 (disabled)
	WorktreePoolSize int `yaml:"worktree_pool_size,omitempty"`

	// SyncMainAfterTask enables syncing the local main branch with origin after task completion.
	// When true, Pilot fetches origin/main and resets local main to match after each task.
	// This prevents local/remote divergence over time.
	// Default: false (opt-in feature)
	// GH-1018: Added to prevent local/remote main branch divergence
	SyncMainAfterTask bool `yaml:"sync_main_after_task,omitempty"`

	// Retry contains error-type-specific retry strategies (GH-920)
	Retry *RetryConfig `yaml:"retry,omitempty"`

	// Stagnation contains stagnation detection settings (GH-925)
	Stagnation *StagnationConfig `yaml:"stagnation,omitempty"`

	// Simplification contains code simplification settings (GH-995)
	// When enabled, Pilot auto-simplifies code after implementation for clarity.
	Simplification *SimplifyConfig `yaml:"simplification,omitempty"`
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

// EffortRoutingConfig controls the effort level based on task complexity.
// Effort controls how many tokens Claude uses when responding — trading off
// between thoroughness and efficiency. Works with Claude API output_config.effort.
//
// Example YAML configuration:
//
//	executor:
//	  effort_routing:
//	    enabled: true
//	    trivial: "low"     # Fast, minimal token spend
//	    simple: "medium"   # Balanced
//	    medium: "high"     # Standard (default behavior)
//	    complex: "max"     # Deepest reasoning
type EffortRoutingConfig struct {
	// Enabled controls whether effort routing is active.
	// When false (default), effort is not set (uses model default of "high").
	Enabled bool `yaml:"enabled"`

	// Trivial effort for trivial tasks. Default: "low"
	Trivial string `yaml:"trivial"`

	// Simple effort for simple tasks. Default: "medium"
	Simple string `yaml:"simple"`

	// Medium effort for standard tasks. Default: "high"
	Medium string `yaml:"medium"`

	// Complex effort for architectural work. Default: "max"
	Complex string `yaml:"complex"`
}

// EffortClassifierConfig configures the LLM-based effort classifier that analyzes
// task content to recommend the appropriate effort level before execution.
// Falls back to static complexity→effort mapping on failure.
//
// GH-727: Smarter effort selection via LLM analysis.
// Cost: ~$0.0002 per classification (negligible vs execution savings).
//
// Example YAML configuration:
//
//	executor:
//	  effort_classifier:
//	    enabled: true
//	    model: "claude-haiku-4-5-20251001"
//	    timeout: 30s
type EffortClassifierConfig struct {
	// Enabled controls whether LLM effort classification is active.
	// When false (default), static complexity→effort mapping is used.
	Enabled bool `yaml:"enabled"`

	// Model is the model to use for effort classification.
	// Default: "claude-haiku-4-5-20251001"
	Model string `yaml:"model,omitempty"`

	// Timeout is the maximum time to wait for LLM response.
	// Default: "30s"
	Timeout string `yaml:"timeout,omitempty"`
}

// DefaultEffortClassifierConfig returns default effort classifier configuration.
func DefaultEffortClassifierConfig() *EffortClassifierConfig {
	return &EffortClassifierConfig{
		Enabled: true,
		Model:   "claude-haiku-4-5-20251001",
		Timeout: "30s",
	}
}

// IntentJudgeConfig configures the LLM intent judge that compares diffs against
// the original issue to catch scope creep and missing requirements.
//
// Example YAML configuration:
//
//	executor:
//	  intent_judge:
//	    enabled: true
//	    model: "claude-haiku-4-5-20251001"
//	    max_diff_chars: 8000
type IntentJudgeConfig struct {
	// Enabled controls whether the intent judge runs after execution.
	// Default: true (when config block is present).
	Enabled *bool `yaml:"enabled,omitempty"`

	// Model is the model to use for intent evaluation. Default: "claude-haiku-4-5-20251001"
	Model string `yaml:"model,omitempty"`

	// MaxDiffChars is the maximum diff size in characters before truncation.
	// Default: 8000.
	MaxDiffChars int `yaml:"max_diff_chars,omitempty"`
}

// DefaultIntentJudgeConfig returns default intent judge configuration.
func DefaultIntentJudgeConfig() *IntentJudgeConfig {
	enabled := true
	return &IntentJudgeConfig{
		Enabled:      &enabled,
		Model:        "claude-haiku-4-5-20251001",
		MaxDiffChars: 8000,
	}
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
			Model:           "anthropic/claude-sonnet-4-5",
			Provider:        "anthropic",
			AutoStartServer: true,
			ServerCommand:   "opencode serve",
		},
		ModelRouting:     DefaultModelRoutingConfig(),
		Timeout:          DefaultTimeoutConfig(),
		EffortRouting:    DefaultEffortRoutingConfig(),
		EffortClassifier: DefaultEffortClassifierConfig(),
		Decompose:        DefaultDecomposeConfig(),
		IntentJudge:      DefaultIntentJudgeConfig(),
		Navigator:        DefaultNavigatorConfig(),
		Retry:            DefaultRetryConfig(),
		Stagnation:       DefaultStagnationConfig(),
		Simplification:   DefaultSimplifyConfig(),
	}
}

// DefaultModelRoutingConfig returns default model routing configuration.
// Model routing is disabled by default; when enabled, uses Haiku for trivial
// tasks (speed) and Opus 4.5 for simple/medium/complex tasks (highest
// capability).
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
		Simple:  "claude-opus-4-6",
		Medium:  "claude-opus-4-6",
		Complex: "claude-opus-4-6",
	}
}

// DefaultEffortRoutingConfig returns default effort routing configuration.
// Effort routing is disabled by default; when enabled, maps task complexity
// to Claude API effort levels for optimal cost/quality trade-off.
func DefaultEffortRoutingConfig() *EffortRoutingConfig {
	return &EffortRoutingConfig{
		Enabled: false,
		Trivial: "low",
		Simple:  "medium",
		Medium:  "high",
		Complex: "max",
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

// StagnationConfig controls stagnation detection and recovery (GH-925).
// Detects when tasks are stuck in loops, making no progress, or spinning.
//
// Example YAML configuration:
//
//	executor:
//	  stagnation:
//	    enabled: true
//	    warn_timeout: 10m
//	    pause_timeout: 20m
//	    abort_timeout: 30m
//	    warn_at_iteration: 8
//	    abort_at_iteration: 15
//	    commit_partial_work: true
type StagnationConfig struct {
	// Enabled controls whether stagnation detection is active.
	// Default: false (disabled by default).
	Enabled bool `yaml:"enabled"`

	// Timeout thresholds - absolute time since task start
	WarnTimeout  time.Duration `yaml:"warn_timeout"`
	PauseTimeout time.Duration `yaml:"pause_timeout"`
	AbortTimeout time.Duration `yaml:"abort_timeout"`

	// Iteration limits - Claude Code turn count
	WarnAtIteration  int `yaml:"warn_at_iteration"`
	PauseAtIteration int `yaml:"pause_at_iteration"`
	AbortAtIteration int `yaml:"abort_at_iteration"`

	// Loop detection - detect identical states
	StateHistorySize         int `yaml:"state_history_size"`
	IdenticalStatesThreshold int `yaml:"identical_states_threshold"`

	// Recovery settings
	GracePeriod       time.Duration `yaml:"grace_period"`
	CommitPartialWork bool          `yaml:"commit_partial_work"`
}

// DefaultStagnationConfig returns default stagnation detection settings.
func DefaultStagnationConfig() *StagnationConfig {
	return &StagnationConfig{
		Enabled:                  false, // Disabled by default
		WarnTimeout:              10 * time.Minute,
		PauseTimeout:             20 * time.Minute,
		AbortTimeout:             30 * time.Minute,
		WarnAtIteration:          8,
		PauseAtIteration:         12,
		AbortAtIteration:         15,
		StateHistorySize:         5,
		IdenticalStatesThreshold: 3,
		GracePeriod:              30 * time.Second,
		CommitPartialWork:        true,
	}
}

// BackendType constants for configuration.
const (
	BackendTypeClaudeCode = "claude-code"
	BackendTypeOpenCode   = "opencode"
)
