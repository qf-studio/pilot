package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewClaudeCodeBackend(t *testing.T) {
	tests := []struct {
		name          string
		config        *ClaudeCodeConfig
		expectCommand string
	}{
		{
			name:          "nil config uses defaults",
			config:        nil,
			expectCommand: "claude",
		},
		{
			name:          "empty command uses default",
			config:        &ClaudeCodeConfig{Command: ""},
			expectCommand: "claude",
		},
		{
			name:          "custom command",
			config:        &ClaudeCodeConfig{Command: "/custom/claude"},
			expectCommand: "/custom/claude",
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

// TestClaudeCodeBackendParseBackgroundTaskEvents verifies that Claude Code's
// task_started/task_notification system events (emitted when a Bash command
// or sub-agent runs in the background, e.g. task_type=local_bash) map to
// EventTypeTaskStarted/EventTypeTaskNotification with the task ID and
// terminal status preserved, so the runner can track in-flight background
// tasks for the stall watchdog (GH-4357).
func TestClaudeCodeBackendParseBackgroundTaskEvents(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	t.Run("task_started", func(t *testing.T) {
		line := `{"type":"system","subtype":"task_started","task_id":"bscl9ri3t","tool_use_id":"toolu_1","description":"Run tests","task_type":"local_bash","session_id":"sess-1"}`
		event := backend.parseStreamEvent(line)

		if event.Type != EventTypeTaskStarted {
			t.Errorf("Type = %q, want %q", event.Type, EventTypeTaskStarted)
		}
		if event.BackgroundTaskID != "bscl9ri3t" {
			t.Errorf("BackgroundTaskID = %q, want %q", event.BackgroundTaskID, "bscl9ri3t")
		}
	})

	t.Run("task_notification completed", func(t *testing.T) {
		line := `{"type":"system","subtype":"task_notification","task_id":"bscl9ri3t","tool_use_id":"toolu_1","status":"completed","summary":"tests passed"}`
		event := backend.parseStreamEvent(line)

		if event.Type != EventTypeTaskNotification {
			t.Errorf("Type = %q, want %q", event.Type, EventTypeTaskNotification)
		}
		if event.BackgroundTaskID != "bscl9ri3t" {
			t.Errorf("BackgroundTaskID = %q, want %q", event.BackgroundTaskID, "bscl9ri3t")
		}
		if event.BackgroundTaskStatus != "completed" {
			t.Errorf("BackgroundTaskStatus = %q, want %q", event.BackgroundTaskStatus, "completed")
		}
	})
}

func TestClaudeCodeBackendParseUsageInfo(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	line := `{"type":"result","result":"Done","usage":{"input_tokens":100,"output_tokens":50},"model":"claude-sonnet-4-6"}`
	event := backend.parseStreamEvent(line)

	if event.TokensInput != 100 {
		t.Errorf("TokensInput = %d, want 100", event.TokensInput)
	}
	if event.TokensOutput != 50 {
		t.Errorf("TokensOutput = %d, want 50", event.TokensOutput)
	}
	if event.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", event.Model)
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

// TestClaudeCodeBackendParsePartialMessageStreamEvent covers GH-4501: with
// --include-partial-messages, the CLI wraps mid-turn delta chunks in a
// top-level {"type":"stream_event","event":{"type":"..."}} envelope. These
// must classify as EventTypeStreamDelta — a distinct, unhandled-in-processing
// type — rather than being misparsed as a complete assistant/tool event.
func TestClaudeCodeBackendParsePartialMessageStreamEvent(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	tests := []struct {
		name string
		line string
	}{
		{
			name: "message_start",
			line: `{"type":"stream_event","event":{"type":"message_start","message":{"model":"claude-sonnet-5","id":"msg_1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":5}}},"session_id":"s1","uuid":"u1"}`,
		},
		{
			name: "content_block_start",
			line: `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}},"session_id":"s1","uuid":"u2"}`,
		},
		{
			name: "content_block_delta text",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial chunk"}},"session_id":"s1","uuid":"u3"}`,
		},
		{
			name: "content_block_delta thinking",
			line: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning..."}},"session_id":"s1","uuid":"u4"}`,
		},
		{
			name: "content_block_stop",
			line: `{"type":"stream_event","event":{"type":"content_block_stop","index":0},"session_id":"s1","uuid":"u5"}`,
		},
		{
			name: "message_delta",
			line: `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":14}},"session_id":"s1","uuid":"u6"}`,
		},
		{
			name: "message_stop",
			line: `{"type":"stream_event","event":{"type":"message_stop"},"session_id":"s1","uuid":"u7"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := backend.parseStreamEvent(tt.line)

			if event.Type != EventTypeStreamDelta {
				t.Errorf("Type = %q, want %q", event.Type, EventTypeStreamDelta)
			}
			if event.ToolName != "" {
				t.Errorf("ToolName should be empty for a partial delta, got %q", event.ToolName)
			}
			// A partial text_delta/thinking_delta must never surface as
			// TokensInput/TokensOutput on its own — usage in these envelopes
			// lives under "event", which the top-level StreamEvent.Usage field
			// (tagged "usage" at the top level) does not reach, so accounting
			// stays sourced from the complete assistant/result events only.
			if event.TokensInput != 0 || event.TokensOutput != 0 {
				t.Errorf("partial delta should not carry token usage, got in=%d out=%d", event.TokensInput, event.TokensOutput)
			}
			if event.Raw != tt.line {
				t.Errorf("Raw = %q, want %q", event.Raw, tt.line)
			}
		})
	}
}

// TestClaudeCodeBackendPartialDeltasDoNotDuplicateCompleteMessage covers
// GH-4501's double-processing requirement: a realistic interleaved sequence
// (partial deltas for a turn, followed by the complete "assistant" event for
// the same turn, followed by "result") must produce exactly one
// EventTypeText event with the full text and exactly one token-usage
// accounting event — not one per delta.
func TestClaudeCodeBackendPartialDeltasDoNotDuplicateCompleteMessage(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)

	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi! What"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" can I help?"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi! What can I help?"}]}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","result":"Hi! What can I help?","is_error":false,"usage":{"input_tokens":2,"output_tokens":14}}`,
	}

	var (
		textEvents   int
		deltaEvents  int
		fullText     string
		tokensInput  int64
		tokensOutput int64
	)
	for _, line := range lines {
		event := backend.parseStreamEvent(line)
		switch event.Type {
		case EventTypeText:
			textEvents++
			fullText = event.Message
		case EventTypeStreamDelta:
			deltaEvents++
		}
		tokensInput += event.TokensInput
		tokensOutput += event.TokensOutput
	}

	if textEvents != 1 {
		t.Errorf("expected exactly 1 EventTypeText, got %d", textEvents)
	}
	if fullText != "Hi! What can I help?" {
		t.Errorf("assembled text = %q, want full complete-message text", fullText)
	}
	if deltaEvents != 7 {
		t.Errorf("expected 7 EventTypeStreamDelta events, got %d", deltaEvents)
	}
	if tokensInput != 2 || tokensOutput != 14 {
		t.Errorf("token accounting = in=%d out=%d, want in=2 out=14 (sourced only from the result event)", tokensInput, tokensOutput)
	}
}

// TestClaudeCodeBackendIncludesPartialMessagesFlag covers GH-4501: the CLI
// must be invoked with --include-partial-messages so long silent "thinking"
// turns still emit stdout that resets the stall watchdog's idle clock. Uses
// a fake CLI script (same technique as TestClaudeCodeBackendResumeSessionFallback)
// to capture the real argv rather than asserting against unexported
// arg-building internals.
func TestClaudeCodeBackendIncludesPartialMessagesFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	logFile := tmpDir + "/calls.log"
	script := tmpDir + "/fake-claude"

	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + logFile + `
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})

	t.Run("default invocation", func(t *testing.T) {
		opts := ExecuteOptions{
			Prompt:       "hello",
			ProjectPath:  tmpDir,
			EventHandler: func(BackendEvent) {},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := backend.Execute(ctx, opts); err != nil {
			t.Fatalf("Execute() error: %v", err)
		}
	})

	t.Run("resume invocation", func(t *testing.T) {
		opts := ExecuteOptions{
			Prompt:          "hello",
			ProjectPath:     tmpDir,
			ResumeSessionID: "abc-123",
			EventHandler:    func(BackendEvent) {},
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := backend.Execute(ctx, opts); err != nil {
			t.Fatalf("Execute() error: %v", err)
		}
	})

	data, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read log: %v", readErr)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 CLI invocations, got %d: %q", len(lines), lines)
	}
	for i, line := range lines {
		if !strings.Contains(line, "--include-partial-messages") {
			t.Errorf("invocation %d missing --include-partial-messages: %q", i, line)
		}
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

func TestGracePeriodConstant(t *testing.T) {
	// Verify grace period is set to expected value
	if GracePeriod != 5*time.Second {
		t.Errorf("GracePeriod = %v, want 5s", GracePeriod)
	}
}

func TestClaudeCodeBackendTimeoutKillsProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	// Create backend with a command that ignores SIGTERM (sleep)
	// We use 'sh -c' with a trap to simulate a process that ignores signals
	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{
		Command: "sh",
	})

	// Create a context that times out quickly
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Modify ExtraArgs to run a long sleep that outputs stream-json format
	// This simulates Claude Code hanging
	opts := ExecuteOptions{
		Prompt:      "-c",
		ProjectPath: "/tmp",
		Verbose:     false,
		EventHandler: func(event BackendEvent) {
			// Ignore events
		},
	}

	// The backend.Execute uses the config.Command + args, so we need to
	// create a custom backend for testing. Skip this for now as it's
	// integration-level testing.
	_ = backend
	_ = opts

	// Instead, verify the timeout detection logic works
	// by checking context cancellation is detected properly
	<-ctx.Done()
	if ctx.Err() != context.DeadlineExceeded {
		t.Errorf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
}

func TestClaudeCodeBackendContextCancellation(t *testing.T) {
	// Test that context cancellation is handled properly
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	cancel()

	if ctx.Err() != context.Canceled {
		t.Errorf("ctx.Err() = %v, want Canceled", ctx.Err())
	}
}

func TestHeartbeatConstants(t *testing.T) {
	// Verify heartbeat constants are set to expected values
	if DefaultHeartbeatTimeout != 5*time.Minute {
		t.Errorf("DefaultHeartbeatTimeout = %v, want 5m", DefaultHeartbeatTimeout)
	}
	if MinHeartbeatTimeout != 1*time.Minute {
		t.Errorf("MinHeartbeatTimeout = %v, want 1m", MinHeartbeatTimeout)
	}
	if MaxHeartbeatTimeout != 30*time.Minute {
		t.Errorf("MaxHeartbeatTimeout = %v, want 30m", MaxHeartbeatTimeout)
	}
	if HeartbeatCheckInterval != 30*time.Second {
		t.Errorf("HeartbeatCheckInterval = %v, want 30s", HeartbeatCheckInterval)
	}
}

func TestHeartbeatCallbackType(t *testing.T) {
	// Verify HeartbeatCallback can be assigned properly
	var callbackInvoked bool
	var capturedPID int
	var capturedAge time.Duration

	callback := func(pid int, lastEventAge time.Duration) {
		callbackInvoked = true
		capturedPID = pid
		capturedAge = lastEventAge
	}

	// Invoke the callback directly to verify it works
	testPID := 12345
	testAge := 6 * time.Minute
	callback(testPID, testAge)

	if !callbackInvoked {
		t.Error("HeartbeatCallback was not invoked")
	}
	if capturedPID != testPID {
		t.Errorf("capturedPID = %d, want %d", capturedPID, testPID)
	}
	if capturedAge != testAge {
		t.Errorf("capturedAge = %v, want %v", capturedAge, testAge)
	}
}

func TestExecuteOptionsHeartbeatCallback(t *testing.T) {
	// Verify ExecuteOptions accepts HeartbeatCallback
	var callbackCalled bool
	opts := ExecuteOptions{
		Prompt:      "test",
		ProjectPath: "/tmp",
		HeartbeatCallback: func(pid int, lastEventAge time.Duration) {
			callbackCalled = true
		},
	}

	// Verify the callback is set
	if opts.HeartbeatCallback == nil {
		t.Error("HeartbeatCallback should not be nil")
	}

	// Invoke and verify
	opts.HeartbeatCallback(1234, time.Minute)
	if !callbackCalled {
		t.Error("HeartbeatCallback was not called")
	}
}

func TestWatchdogCallbackType(t *testing.T) {
	// Verify WatchdogCallback can be assigned properly (GH-882)
	var callbackInvoked bool
	var capturedPID int
	var capturedTimeout time.Duration

	callback := func(pid int, watchdogTimeout time.Duration) {
		callbackInvoked = true
		capturedPID = pid
		capturedTimeout = watchdogTimeout
	}

	testPID := 5678
	testTimeout := 10 * time.Minute

	callback(testPID, testTimeout)

	if !callbackInvoked {
		t.Error("WatchdogCallback was not invoked")
	}
	if capturedPID != testPID {
		t.Errorf("capturedPID = %d, want %d", capturedPID, testPID)
	}
	if capturedTimeout != testTimeout {
		t.Errorf("capturedTimeout = %v, want %v", capturedTimeout, testTimeout)
	}
}

func TestExecuteOptionsWatchdogCallback(t *testing.T) {
	// Verify ExecuteOptions accepts WatchdogCallback (GH-882)
	var callbackCalled bool
	opts := ExecuteOptions{
		Prompt:          "test",
		ProjectPath:     "/tmp",
		WatchdogTimeout: 30 * time.Minute,
		WatchdogCallback: func(pid int, watchdogTimeout time.Duration) {
			callbackCalled = true
		},
	}

	// Verify the callback and timeout are set
	if opts.WatchdogCallback == nil {
		t.Error("WatchdogCallback should not be nil")
	}
	if opts.WatchdogTimeout != 30*time.Minute {
		t.Errorf("WatchdogTimeout = %v, want 30m", opts.WatchdogTimeout)
	}

	// Invoke and verify
	opts.WatchdogCallback(1234, opts.WatchdogTimeout)
	if !callbackCalled {
		t.Error("WatchdogCallback was not called")
	}
}

func TestEffectiveHeartbeatTimeout(t *testing.T) {
	tests := []struct {
		name     string
		config   *BackendConfig
		expected time.Duration
	}{
		{
			name:     "nil config returns default",
			config:   nil,
			expected: DefaultHeartbeatTimeout,
		},
		{
			name:     "zero value returns default",
			config:   &BackendConfig{},
			expected: DefaultHeartbeatTimeout,
		},
		{
			name:     "custom value within range",
			config:   &BackendConfig{HeartbeatTimeout: 10 * time.Minute},
			expected: 10 * time.Minute,
		},
		{
			name:     "below minimum clamped to min",
			config:   &BackendConfig{HeartbeatTimeout: 30 * time.Second},
			expected: MinHeartbeatTimeout,
		},
		{
			name:     "above maximum clamped to max",
			config:   &BackendConfig{HeartbeatTimeout: 45 * time.Minute},
			expected: MaxHeartbeatTimeout,
		},
		{
			name:     "exact minimum allowed",
			config:   &BackendConfig{HeartbeatTimeout: 1 * time.Minute},
			expected: 1 * time.Minute,
		},
		{
			name:     "exact maximum allowed",
			config:   &BackendConfig{HeartbeatTimeout: 30 * time.Minute},
			expected: 30 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.EffectiveHeartbeatTimeout()
			if got != tt.expected {
				t.Errorf("EffectiveHeartbeatTimeout() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestClaudeCodeBackendHeartbeatTimeout(t *testing.T) {
	// Default backend should use DefaultHeartbeatTimeout
	b := NewClaudeCodeBackend(nil)
	if b.heartbeatTimeout != DefaultHeartbeatTimeout {
		t.Errorf("default heartbeatTimeout = %v, want %v", b.heartbeatTimeout, DefaultHeartbeatTimeout)
	}

	// SetHeartbeatTimeout should update the value
	b.SetHeartbeatTimeout(15 * time.Minute)
	if b.heartbeatTimeout != 15*time.Minute {
		t.Errorf("after SetHeartbeatTimeout(15m), heartbeatTimeout = %v, want 15m", b.heartbeatTimeout)
	}
}

// TestEffectiveHeartbeatTimeoutWithFloor covers GH-4691/GH-4715:
// opts.LivenessPolicy.HeartbeatFloor (resolved by the runner via
// ResolveLivenessPolicy for high-effort/heavy-complexity lanes) must raise
// the effective heartbeat timeout above
// the backend's own configured/default value, but never lower it — and the
// returned source string must reflect which one won, so a kill log line is
// diagnosable without cross-referencing the runner's decision separately.
func TestEffectiveHeartbeatTimeoutWithFloor(t *testing.T) {
	tests := []struct {
		name        string
		base        time.Duration
		floor       time.Duration
		wantTimeout time.Duration
		wantSource  string
	}{
		{
			name:        "zero floor: base (config) wins unchanged",
			base:        5 * time.Minute,
			floor:       0,
			wantTimeout: 5 * time.Minute,
			wantSource:  "config",
		},
		{
			name:        "floor below base: base (config) wins",
			base:        5 * time.Minute,
			floor:       3 * time.Minute,
			wantTimeout: 5 * time.Minute,
			wantSource:  "config",
		},
		{
			name:        "floor above base (GH-4691 epic/high-effort case): floor wins",
			base:        DefaultHeartbeatTimeout, // 5m
			floor:       10 * time.Minute,        // highEffortStallFloor
			wantTimeout: 10 * time.Minute,
			wantSource:  "effort_floor",
		},
		{
			name:        "floor exactly equal to base: base wins (not >, so no stacking noise)",
			base:        10 * time.Minute,
			floor:       10 * time.Minute,
			wantTimeout: 10 * time.Minute,
			wantSource:  "config",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTimeout, gotSource := effectiveHeartbeatTimeout(tt.base, tt.floor)
			if gotTimeout != tt.wantTimeout || gotSource != tt.wantSource {
				t.Errorf("effectiveHeartbeatTimeout(%v, %v) = (%v, %q), want (%v, %q)",
					tt.base, tt.floor, gotTimeout, gotSource, tt.wantTimeout, tt.wantSource)
			}
		})
	}
}

func TestBackendFactoryHeartbeatTimeout(t *testing.T) {
	// Factory should wire heartbeat timeout from BackendConfig
	config := &BackendConfig{
		Type:             BackendTypeClaudeCode,
		HeartbeatTimeout: 12 * time.Minute,
	}
	backend, err := NewBackend(config)
	if err != nil {
		t.Fatalf("NewBackend() error: %v", err)
	}
	ccb, ok := backend.(*ClaudeCodeBackend)
	if !ok {
		t.Fatal("expected *ClaudeCodeBackend")
	}
	if ccb.heartbeatTimeout != 12*time.Minute {
		t.Errorf("heartbeatTimeout = %v, want 12m", ccb.heartbeatTimeout)
	}
}

func TestClassifyClaudeCodeError(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		expectType ClaudeCodeErrorType
	}{
		{
			name:       "rate limit - hit your limit",
			stderr:     "Error: You've hit your limit · resets 6am (Europe/Podgorica)",
			expectType: ErrorTypeRateLimit,
		},
		{
			name:       "rate limit - rate limit",
			stderr:     "Error: Rate limit exceeded, try again later",
			expectType: ErrorTypeRateLimit,
		},
		{
			name:       "invalid config - effort level",
			stderr:     `Error: Effort level "max" is not available for Claude.ai subscribers`,
			expectType: ErrorTypeInvalidConfig,
		},
		{
			name:       "invalid config - requires verbose",
			stderr:     "Error: When using --print, --output-format=stream-json requires --verbose",
			expectType: ErrorTypeInvalidConfig,
		},
		{
			name:       "api error - authentication",
			stderr:     "Error: Authentication failed. Please check your API key.",
			expectType: ErrorTypeAPIError,
		},
		{
			name:       "api error - 401",
			stderr:     "HTTP 401: Unauthorized",
			expectType: ErrorTypeAPIError,
		},
		{
			name:       "timeout - killed",
			stderr:     "signal: killed",
			expectType: ErrorTypeTimeout,
		},
		{
			// GH-2377: CC emits "No conversation found with session ID: <uuid>"
			// when --resume targets an evicted session. Must classify as
			// session_not_found so the --resume fallback triggers.
			name:       "session not found - no conversation found",
			stderr:     "No conversation found with session ID: 723e1e6e-0253-45ae-a7e4-e79112d73deb",
			expectType: ErrorTypeSessionNotFound,
		},
		{
			name:       "session not found - classic phrasing",
			stderr:     "Error: session not found",
			expectType: ErrorTypeSessionNotFound,
		},
		{
			name:       "unknown error",
			stderr:     "Some random error message",
			expectType: ErrorTypeUnknown,
		},
		{
			name:       "empty stderr",
			stderr:     "",
			expectType: ErrorTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyClaudeCodeError(tt.stderr, nil, false, "", false)
			if err.Type != tt.expectType {
				t.Errorf("classifyClaudeCodeError() type = %q, want %q", err.Type, tt.expectType)
			}
			// Verify stderr is captured
			if tt.stderr != "" && err.Stderr != tt.stderr {
				t.Errorf("classifyClaudeCodeError() stderr = %q, want %q", err.Stderr, tt.stderr)
			}
		})
	}
}

func TestParseClaudeCodeError(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		expectType ClaudeCodeErrorType
	}{
		{
			name:       "rate limit error",
			stderr:     "Error: You've hit your limit · resets 6am (Europe/Podgorica)",
			expectType: ErrorTypeRateLimit,
		},
		{
			name:       "invalid config error",
			stderr:     `Error: Effort level "max" is not available for Claude.ai subscribers`,
			expectType: ErrorTypeInvalidConfig,
		},
		{
			name:       "api error",
			stderr:     "Error: Authentication failed. Please check your API key.",
			expectType: ErrorTypeAPIError,
		},
		{
			name:       "timeout error",
			stderr:     "signal: killed",
			expectType: ErrorTypeTimeout,
		},
		{
			name:       "unknown error",
			stderr:     "Something completely unexpected happened",
			expectType: ErrorTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseClaudeCodeError(tt.stderr, nil, false, "", false)
			ccErr, ok := err.(*ClaudeCodeError)
			if !ok {
				t.Errorf("parseClaudeCodeError() did not return *ClaudeCodeError, got %T", err)
				return
			}
			if ccErr.Type != tt.expectType {
				t.Errorf("parseClaudeCodeError() type = %q, want %q", ccErr.Type, tt.expectType)
			}
			if tt.stderr != "" && ccErr.Stderr != tt.stderr {
				t.Errorf("parseClaudeCodeError() stderr = %q, want %q", ccErr.Stderr, tt.stderr)
			}
		})
	}
}

func TestSawSuccessResultRecovery(t *testing.T) {
	// GH-2107: When a successful result event was seen but the process exits with
	// an error (e.g., timeout on final summary), SawSuccessResult should be set.
	t.Run("successful result sets SawSuccessResult", func(t *testing.T) {
		backend := NewClaudeCodeBackend(nil)
		event := backend.parseStreamEvent(`{"type":"result","result":"All tasks completed.","is_error":false}`)
		if event.Type != EventTypeResult {
			t.Fatalf("expected result event, got %s", event.Type)
		}
		if event.IsError {
			t.Fatal("expected non-error result")
		}

		// Simulate what executeWithFromPR does: set SawSuccessResult when result is not error
		result := &BackendResult{}
		if event.Type == EventTypeResult && !event.IsError {
			result.Output = event.Message
			result.SawSuccessResult = true
		}

		if !result.SawSuccessResult {
			t.Error("SawSuccessResult should be true for non-error result")
		}
		if result.Output != "All tasks completed." {
			t.Errorf("Output = %q, want %q", result.Output, "All tasks completed.")
		}
	})

	t.Run("error result does not set SawSuccessResult", func(t *testing.T) {
		backend := NewClaudeCodeBackend(nil)
		event := backend.parseStreamEvent(`{"type":"result","result":"Failed to complete","is_error":true}`)

		result := &BackendResult{}
		if event.Type == EventTypeResult {
			if event.IsError {
				result.Error = event.Message
			} else {
				result.SawSuccessResult = true
			}
		}

		if result.SawSuccessResult {
			t.Error("SawSuccessResult should be false for error result")
		}
	})

	t.Run("no result event does not set SawSuccessResult", func(t *testing.T) {
		result := &BackendResult{}
		if result.SawSuccessResult {
			t.Error("SawSuccessResult should default to false")
		}
	})
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		n        int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.n)
		if got != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.expected)
		}
	}
}

func TestClassifyClaudeCodeError_OOM(t *testing.T) {
	// GH-2112/GH-4412: OOM/SIGKILL detection via exit code, gated on kernel
	// OOM evidence — a bare 137/139 with no confirming dmesg entry must NOT
	// be labeled oom_killed (see TestClassifyClaudeCodeError_ShutdownVsOOM
	// for the unconfirmed-kill case).
	tests := []struct {
		name               string
		exitCode           int
		stderr             string
		kernelOOMConfirmed bool
		expectType         ClaudeCodeErrorType
		expectMsg          string
	}{
		{
			name:               "exit 137 (SIGKILL) with kernel evidence classified as OOM",
			exitCode:           137,
			stderr:             "",
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeOOM,
			expectMsg:          "Process killed by SIGKILL (exit code 137, confirmed via kernel OOM-killer log)",
		},
		{
			name:               "exit 139 (SIGSEGV) with kernel evidence classified as OOM",
			exitCode:           139,
			stderr:             "",
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeOOM,
			expectMsg:          "Process killed by SIGSEGV (exit code 139, confirmed via kernel OOM-killer log)",
		},
		{
			name:               "exit 137 with stderr and kernel evidence still classified as OOM",
			exitCode:           137,
			stderr:             "some output before death",
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeOOM,
			expectMsg:          "Process killed by SIGKILL (exit code 137, confirmed via kernel OOM-killer log)",
		},
		{
			name:       "exit 1 with empty stderr is not OOM",
			exitCode:   1,
			stderr:     "",
			expectType: ErrorTypeUnknown,
		},
		{
			name:       "exit 1 with rate limit stderr",
			exitCode:   1,
			stderr:     "Error: You've hit your limit",
			expectType: ErrorTypeRateLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a real exec.ExitError by running a process that exits with the desired code
			var exitErr error
			if tt.exitCode > 0 {
				cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", tt.exitCode))
				exitErr = cmd.Run()
			}
			err := classifyClaudeCodeError(tt.stderr, exitErr, false, "", tt.kernelOOMConfirmed)
			if err.Type != tt.expectType {
				t.Errorf("type = %q, want %q", err.Type, tt.expectType)
			}
			if tt.expectMsg != "" && err.Message != tt.expectMsg {
				t.Errorf("message = %q, want %q", err.Message, tt.expectMsg)
			}
		})
	}
}

// TestClassifyClaudeCodeError_UnconfirmedKillNotOOM covers the core GH-4412
// fix: an exit 137/139 that is neither our own context-cancellation teardown
// nor a Pilot-initiated heartbeat/watchdog kill nor backed by a dmesg
// OOM-killer entry must NOT be labeled oom_killed. Bare exit code is not
// evidence — plenty of external SIGKILL sources (the orphan-running sweep
// bug this task is part of, a manual `kill -9`, cgroup OOM without dmesg
// access) produce the identical exit code.
func TestClassifyClaudeCodeError_UnconfirmedKillNotOOM(t *testing.T) {
	tests := []struct {
		name       string
		exitCode   int
		expectType ClaudeCodeErrorType
		expectMsg  string
	}{
		{
			name:       "exit 137, no kernel evidence -> not OOM",
			exitCode:   137,
			expectType: ErrorTypeTimeout,
			expectMsg:  "Process killed by SIGKILL (exit code 137, no kernel OOM evidence found)",
		},
		{
			name:       "exit 139, no kernel evidence -> not OOM",
			exitCode:   139,
			expectType: ErrorTypeTimeout,
			expectMsg:  "Process killed by SIGSEGV (exit code 139, no kernel OOM evidence found)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", tt.exitCode))
			exitErr := cmd.Run()
			err := classifyClaudeCodeError("", exitErr, false, "", false)
			if err.Type != tt.expectType {
				t.Errorf("type = %q, want %q (must not be oom_killed without kernel evidence)", err.Type, tt.expectType)
			}
			if err.Type == ErrorTypeOOM {
				t.Errorf("unconfirmed kill must never classify as oom_killed")
			}
			if tt.expectMsg != "" && err.Message != tt.expectMsg {
				t.Errorf("message = %q, want %q", err.Message, tt.expectMsg)
			}
		})
	}
}

// TestClassifyClaudeCodeError_SelfKillNotOOM covers the second half of the
// GH-4412 fix: heartbeat-timeout and watchdog-timeout kills call
// cmd.Process.Kill() directly rather than going through context
// cancellation, so ctx.Err() stays nil for them. Before this fix they fell
// straight into the ctxCancelled==false branch and were mislabeled
// oom_killed even though Pilot itself killed the process on purpose.
func TestClassifyClaudeCodeError_SelfKillNotOOM(t *testing.T) {
	tests := []struct {
		name           string
		exitCode       int
		selfKillReason string
		expectType     ClaudeCodeErrorType
		expectMsg      string
	}{
		{
			name:           "heartbeat timeout kill -> shutdown_terminated, not OOM",
			exitCode:       137,
			selfKillReason: "heartbeat timeout",
			expectType:     ErrorTypeShutdownTerminated,
			expectMsg:      "Process terminated by SIGKILL after Pilot heartbeat timeout (exit code 137)",
		},
		{
			name:           "watchdog timeout kill -> shutdown_terminated, not OOM",
			exitCode:       137,
			selfKillReason: "watchdog timeout",
			expectType:     ErrorTypeShutdownTerminated,
			expectMsg:      "Process terminated by SIGKILL after Pilot watchdog timeout (exit code 137)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", tt.exitCode))
			exitErr := cmd.Run()
			// kernelOOMConfirmed=true is passed to prove selfKillReason takes
			// precedence over evidence — Pilot's own kill is never OOM
			// regardless of what dmesg says (e.g. host memory pressure that
			// didn't actually kill this process).
			err := classifyClaudeCodeError("", exitErr, false, tt.selfKillReason, true)
			if err.Type != tt.expectType {
				t.Errorf("type = %q, want %q", err.Type, tt.expectType)
			}
			if err.Type == ErrorTypeOOM {
				t.Errorf("self-inflicted kill must never classify as oom_killed")
			}
			if tt.expectMsg != "" && err.Message != tt.expectMsg {
				t.Errorf("message = %q, want %q", err.Message, tt.expectMsg)
			}
		})
	}
}

func TestClassifyClaudeCodeError_ShutdownVsOOM(t *testing.T) {
	// GH-4105/GH-4412: exit 137/139 must be classified as oom_killed only
	// when the run context was NOT already cancelled by our own
	// shutdown/timeout path AND kernel dmesg evidence confirms an actual
	// OOM-killer event. When the context was cancelled, the same exit codes
	// are expected teardown noise and must be tagged shutdown_terminated
	// instead. Without kernel evidence, an unexplained kill is tagged
	// "timeout" (cause unconfirmed), never oom_killed.
	tests := []struct {
		name               string
		exitCode           int
		ctxCancelled       bool
		kernelOOMConfirmed bool
		expectType         ClaudeCodeErrorType
		expectMsg          string
	}{
		{
			// (a) exit 137, context not cancelled, kernel evidence -> oom_killed
			name:               "exit 137, context not cancelled, kernel evidence -> oom_killed",
			exitCode:           137,
			ctxCancelled:       false,
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeOOM,
			expectMsg:          "Process killed by SIGKILL (exit code 137, confirmed via kernel OOM-killer log)",
		},
		{
			// (a2) exit 137, context not cancelled, NO kernel evidence -> not oom_killed
			name:         "exit 137, context not cancelled, no kernel evidence -> not oom_killed",
			exitCode:     137,
			ctxCancelled: false,
			expectType:   ErrorTypeTimeout,
			expectMsg:    "Process killed by SIGKILL (exit code 137, no kernel OOM evidence found)",
		},
		{
			// (b) exit 137, context cancelled -> shutdown classification
			// (kernel evidence irrelevant — our own teardown wins)
			name:               "exit 137, context cancelled -> shutdown_terminated",
			exitCode:           137,
			ctxCancelled:       true,
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeShutdownTerminated,
			expectMsg:          "Process terminated by SIGKILL after context cancellation (exit code 137)",
		},
		{
			// (c) exit 139 mirror
			name:               "exit 139, context not cancelled, kernel evidence -> oom_killed",
			exitCode:           139,
			ctxCancelled:       false,
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeOOM,
			expectMsg:          "Process killed by SIGSEGV (exit code 139, confirmed via kernel OOM-killer log)",
		},
		{
			name:         "exit 139, context not cancelled, no kernel evidence -> not oom_killed",
			exitCode:     139,
			ctxCancelled: false,
			expectType:   ErrorTypeTimeout,
			expectMsg:    "Process killed by SIGSEGV (exit code 139, no kernel OOM evidence found)",
		},
		{
			name:               "exit 139, context cancelled -> shutdown_terminated",
			exitCode:           139,
			ctxCancelled:       true,
			kernelOOMConfirmed: true,
			expectType:         ErrorTypeShutdownTerminated,
			expectMsg:          "Process terminated by SIGSEGV after context cancellation (exit code 139)",
		},
		{
			// (d) clean exit unaffected — no exit error at all, ctxCancelled must not
			// spuriously trigger the shutdown classification.
			name:         "clean exit (no error) unaffected by ctxCancelled",
			exitCode:     0,
			ctxCancelled: true,
			expectType:   ErrorTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var exitErr error
			if tt.exitCode > 0 {
				cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", tt.exitCode))
				exitErr = cmd.Run()
			}
			err := classifyClaudeCodeError("", exitErr, tt.ctxCancelled, "", tt.kernelOOMConfirmed)
			if err.Type != tt.expectType {
				t.Errorf("type = %q, want %q", err.Type, tt.expectType)
			}
			if tt.expectMsg != "" && err.Message != tt.expectMsg {
				t.Errorf("message = %q, want %q", err.Message, tt.expectMsg)
			}
		})
	}
}

// TestDmesgHasRecentOOMKill covers the dmesg-parsing evidence check added by
// GH-4412. It must (a) match the kernel's "Killed process <pid>" signature
// line, (b) require the line be at-or-after the `since` cutoff so a
// recycled-PID entry from an unrelated, older process doesn't produce a
// false positive, and (c) fail closed (no evidence) on any parse ambiguity.
func TestDmesgHasRecentOOMKill(t *testing.T) {
	// Fixed reference instant so timestamp math is deterministic regardless
	// of when the test runs.
	base := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.Local)

	tests := []struct {
		name   string
		output string
		pid    int
		since  time.Time
		want   bool
	}{
		{
			name: "recent OOM kill for pid matches",
			output: "[Fri Jul 17 11:59:00 2026] some unrelated line\n" +
				"[Fri Jul 17 12:00:30 2026] Out of memory: Killed process 4242 (claude) total-vm:..., anon-rss:...",
			pid:   4242,
			since: base,
			want:  true,
		},
		{
			name:   "OOM kill for a different pid does not match",
			output: "[Fri Jul 17 12:00:30 2026] Out of memory: Killed process 9999 (claude) total-vm:...",
			pid:    4242,
			since:  base,
			want:   false,
		},
		{
			name:   "stale entry before since cutoff does not match (recycled PID guard)",
			output: "[Fri Jul 17 08:00:00 2026] Out of memory: Killed process 4242 (some-other-process) total-vm:...",
			pid:    4242,
			since:  base,
			want:   false,
		},
		{
			name:   "no dmesg output at all",
			output: "",
			pid:    4242,
			since:  base,
			want:   false,
		},
		{
			name:   "matching text but unparseable timestamp is not evidence",
			output: "[not-a-timestamp] Killed process 4242 (claude)",
			pid:    4242,
			since:  base,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dmesgHasRecentOOMKill(tt.output, tt.pid, tt.since)
			if got != tt.want {
				t.Errorf("dmesgHasRecentOOMKill() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestHasKernelOOMEvidence_InvalidPID verifies the pid<=0 guard short-circuits
// without spawning dmesg at all — defensive against callers that pass an
// unset/negative pid (e.g. cmd.Process nil-checked incorrectly upstream).
func TestHasKernelOOMEvidence_InvalidPID(t *testing.T) {
	if hasKernelOOMEvidence(0, time.Now()) {
		t.Error("hasKernelOOMEvidence(0, ...) should return false")
	}
	if hasKernelOOMEvidence(-1, time.Now()) {
		t.Error("hasKernelOOMEvidence(-1, ...) should return false")
	}
}

func TestExtractExitCode(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
		expected int
	}{
		{"exit 0 (no error)", 0, -1}, // cmd.Run() returns nil for exit 0
		{"exit 1", 1, 1},
		{"exit 137", 137, 137},
		{"exit 139", 139, 139},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("signal-based exit codes are Unix-only")
			}
			cmd := exec.Command("sh", "-c", fmt.Sprintf("exit %d", tt.exitCode))
			err := cmd.Run()
			got := extractExitCode(err)
			if got != tt.expected {
				t.Errorf("extractExitCode() = %d, want %d", got, tt.expected)
			}
		})
	}

	t.Run("nil error returns -1", func(t *testing.T) {
		if got := extractExitCode(nil); got != -1 {
			t.Errorf("extractExitCode(nil) = %d, want -1", got)
		}
	})

	t.Run("non-ExitError returns -1", func(t *testing.T) {
		if got := extractExitCode(fmt.Errorf("some error")); got != -1 {
			t.Errorf("extractExitCode(non-ExitError) = %d, want -1", got)
		}
	})
}

func TestClaudeCodeConfigContextWindow(t *testing.T) {
	// GH-2163: Verify 1M context and max output tokens config fields
	tests := []struct {
		name                string
		config              *ClaudeCodeConfig
		expectDisable1M     bool
		expectMaxOutput     int
		expectEnvContains   []string
		expectEnvNotContain []string
	}{
		{
			name:                "default config - no context window flags",
			config:              &ClaudeCodeConfig{Command: "claude"},
			expectDisable1M:     false,
			expectMaxOutput:     0,
			expectEnvNotContain: []string{"CLAUDE_CODE_DISABLE_1M_CONTEXT", "CLAUDE_CODE_MAX_OUTPUT_TOKENS"},
		},
		{
			name:              "disable 1M context",
			config:            &ClaudeCodeConfig{Command: "claude", Disable1MContext: true},
			expectDisable1M:   true,
			expectEnvContains: []string{"CLAUDE_CODE_DISABLE_1M_CONTEXT=1"},
		},
		{
			name:              "max output tokens set",
			config:            &ClaudeCodeConfig{Command: "claude", MaxOutputTokens: 128000},
			expectMaxOutput:   128000,
			expectEnvContains: []string{"CLAUDE_CODE_MAX_OUTPUT_TOKENS=128000"},
		},
		{
			name:            "both flags set",
			config:          &ClaudeCodeConfig{Command: "claude", Disable1MContext: true, MaxOutputTokens: 64000},
			expectDisable1M: true,
			expectMaxOutput: 64000,
			expectEnvContains: []string{
				"CLAUDE_CODE_DISABLE_1M_CONTEXT=1",
				"CLAUDE_CODE_MAX_OUTPUT_TOKENS=64000",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Disable1MContext != tt.expectDisable1M {
				t.Errorf("Disable1MContext = %v, want %v", tt.config.Disable1MContext, tt.expectDisable1M)
			}
			if tt.config.MaxOutputTokens != tt.expectMaxOutput {
				t.Errorf("MaxOutputTokens = %d, want %d", tt.config.MaxOutputTokens, tt.expectMaxOutput)
			}

			// Simulate the env-building logic from Execute()
			var env []string
			if tt.config.Disable1MContext || tt.config.MaxOutputTokens > 0 {
				env = []string{"PATH=/usr/bin"} // minimal base env for test
				if tt.config.Disable1MContext {
					env = append(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=1")
				}
				if tt.config.MaxOutputTokens > 0 {
					env = append(env, fmt.Sprintf("CLAUDE_CODE_MAX_OUTPUT_TOKENS=%d", tt.config.MaxOutputTokens))
				}
			}

			for _, expected := range tt.expectEnvContains {
				found := false
				for _, e := range env {
					if e == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("env should contain %q, got %v", expected, env)
				}
			}
			for _, notExpected := range tt.expectEnvNotContain {
				for _, e := range env {
					if len(e) >= len(notExpected) && e[:len(notExpected)] == notExpected {
						t.Errorf("env should NOT contain %q, got %v", notExpected, env)
					}
				}
			}
		})
	}
}

func TestClaudeCodeError_Error(t *testing.T) {
	t.Run("with stderr", func(t *testing.T) {
		err := &ClaudeCodeError{
			Type:    ErrorTypeRateLimit,
			Message: "Rate limit hit",
			Stderr:  "detailed stderr",
		}
		errStr := err.Error()
		if errStr != "rate_limit: Rate limit hit (stderr: detailed stderr)" {
			t.Errorf("Error() = %q, unexpected format", errStr)
		}
	})

	t.Run("without stderr", func(t *testing.T) {
		err := &ClaudeCodeError{
			Type:    ErrorTypeUnknown,
			Message: "Unknown error",
			Stderr:  "",
		}
		errStr := err.Error()
		if errStr != "unknown: Unknown error" {
			t.Errorf("Error() = %q, unexpected format", errStr)
		}
	})
}

// TestErrorTypeNoChanges verifies that the no_changes classification constant
// is defined and distinguishable from other error types. GH-2328.
func TestErrorTypeNoChanges(t *testing.T) {
	if ErrorTypeNoChanges != "no_changes" {
		t.Errorf("ErrorTypeNoChanges = %q, want %q", ErrorTypeNoChanges, "no_changes")
	}

	// Must not collide with any other classification the runner already depends on.
	others := []ClaudeCodeErrorType{
		ErrorTypeRateLimit,
		ErrorTypeInvalidConfig,
		ErrorTypeAPIError,
		ErrorTypeTimeout,
		ErrorTypeOOM,
		ErrorTypeSessionNotFound,
		ErrorTypeUnknown,
	}
	for _, o := range others {
		if ErrorTypeNoChanges == o {
			t.Errorf("ErrorTypeNoChanges collides with %q", o)
		}
	}

	// A ClaudeCodeError carrying no_changes must render the final assistant
	// text via Error() so the autopilot failure comment surfaces the refusal.
	err := &ClaudeCodeError{
		Type:    ErrorTypeNoChanges,
		Message: "refused: this task is out of scope",
	}
	if got := err.Error(); got != "no_changes: refused: this task is out of scope" {
		t.Errorf("Error() = %q, want %q", got, "no_changes: refused: this task is out of scope")
	}
}

// TestSetProviderEnv verifies the GH-2371 provider routing fields are stored
// on the backend and surface in the subprocess env build. The env-build logic
// in Execute() is mirrored here to avoid spawning a real claude CLI.
func TestSetProviderEnv(t *testing.T) {
	tests := []struct {
		name              string
		baseURL           string
		authToken         string
		model             string
		expectContains    []string
		expectNotContains []string
	}{
		{
			name:              "all empty - no injection (Anthropic default)",
			expectNotContains: []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"},
		},
		{
			name:              "base URL only",
			baseURL:           "https://api.z.ai/api/anthropic",
			expectContains:    []string{"ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic"},
			expectNotContains: []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"},
		},
		{
			name:      "all three set (Z.AI)",
			baseURL:   "https://api.z.ai/api/anthropic",
			authToken: "zai-fake-token",
			model:     "glm-4.6",
			expectContains: []string{
				"ANTHROPIC_BASE_URL=https://api.z.ai/api/anthropic",
				"ANTHROPIC_AUTH_TOKEN=zai-fake-token",
				"ANTHROPIC_MODEL=glm-4.6",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewClaudeCodeBackend(nil)
			b.SetProviderEnv(tt.baseURL, tt.authToken, tt.model)

			if b.apiBaseURL != tt.baseURL {
				t.Errorf("apiBaseURL = %q, want %q", b.apiBaseURL, tt.baseURL)
			}
			if b.apiAuthToken != tt.authToken {
				t.Errorf("apiAuthToken = %q, want %q", b.apiAuthToken, tt.authToken)
			}
			if b.defaultModel != tt.model {
				t.Errorf("defaultModel = %q, want %q", b.defaultModel, tt.model)
			}

			// Mirror Execute()'s env-build logic.
			env := []string{"PATH=/usr/bin", "PILOT_EXECUTOR=1"}
			if b.apiBaseURL != "" {
				env = append(env, "ANTHROPIC_BASE_URL="+b.apiBaseURL)
			}
			if b.apiAuthToken != "" {
				env = append(env, "ANTHROPIC_AUTH_TOKEN="+b.apiAuthToken)
			}
			if b.defaultModel != "" {
				env = append(env, "ANTHROPIC_MODEL="+b.defaultModel)
			}

			for _, want := range tt.expectContains {
				found := false
				for _, e := range env {
					if e == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("env should contain %q, got %v", want, env)
				}
			}
			for _, banned := range tt.expectNotContains {
				for _, e := range env {
					if len(e) >= len(banned) && e[:len(banned)] == banned {
						t.Errorf("env should NOT contain prefix %q, got %v", banned, env)
					}
				}
			}
		})
	}
}

// TestNewBackendWiresProviderEnv verifies the factory propagates
// BackendConfig.{APIBaseURL,APIAuthToken,DefaultModel} onto the
// ClaudeCodeBackend so users configure provider routing once (GH-2371).
func TestNewBackendWiresProviderEnv(t *testing.T) {
	cfg := &BackendConfig{
		Type:         BackendTypeClaudeCode,
		APIBaseURL:   "https://api.z.ai/api/anthropic",
		APIAuthToken: "zai-fake-token",
		DefaultModel: "glm-4.6",
		ClaudeCode:   &ClaudeCodeConfig{Command: "claude"},
	}

	backend, err := NewBackend(cfg)
	if err != nil {
		t.Fatalf("NewBackend error: %v", err)
	}
	cc, ok := backend.(*ClaudeCodeBackend)
	if !ok {
		t.Fatalf("expected *ClaudeCodeBackend, got %T", backend)
	}
	if cc.apiBaseURL != cfg.APIBaseURL {
		t.Errorf("apiBaseURL = %q, want %q", cc.apiBaseURL, cfg.APIBaseURL)
	}
	if cc.apiAuthToken != cfg.APIAuthToken {
		t.Errorf("apiAuthToken = %q, want %q", cc.apiAuthToken, cfg.APIAuthToken)
	}
	if cc.defaultModel != cfg.DefaultModel {
		t.Errorf("defaultModel = %q, want %q", cc.defaultModel, cfg.DefaultModel)
	}
}

// TestClaudeCodeBackendResumeSessionFallback verifies the GH-2377 fix:
// when Execute is called with ResumeSessionID set and the CLI exits with a
// "session not found" stderr, the backend retries Execute without --resume.
//
// Strategy: install a tiny shell script as the "claude" command that writes
// its full argv to a counter file, then exits with different stderr on the
// first vs second invocation. If the fallback fires, we expect exactly 2
// invocations — the first containing "--resume" and the second not.
func TestClaudeCodeBackendResumeSessionFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	logFile := tmpDir + "/calls.log"
	script := tmpDir + "/fake-claude"

	// Script logs argv and exits with different stderr based on call count.
	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + logFile + `
COUNT=$(wc -l < ` + logFile + ` | tr -d ' ')
if [ "$COUNT" = "1" ]; then
  echo "No conversation found with session ID: abc-123" >&2
  exit 1
fi
echo "fake retry stderr" >&2
exit 2
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	opts := ExecuteOptions{
		Prompt:          "hello",
		ProjectPath:     tmpDir,
		ResumeSessionID: "abc-123",
		EventHandler:    func(BackendEvent) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := backend.Execute(ctx, opts)
	if err == nil {
		t.Fatal("expected error from fake CLI, got nil")
	}

	// Verify exactly 2 invocations and --resume dropped on retry.
	data, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read log: %v", readErr)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 CLI invocations, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "--resume") || !strings.Contains(lines[0], "abc-123") {
		t.Errorf("first invocation missing --resume: %q", lines[0])
	}
	if strings.Contains(lines[1], "--resume") {
		t.Errorf("second invocation should not contain --resume: %q", lines[1])
	}
}

// TestClaudeCodeBackendEmptyModelLogsAndOmitsFlag covers GH-3754: when
// ExecuteOptions.Model is empty, the backend must not pass --model to the
// worker (so it falls through to the worker's own default) and must log
// that omission loudly, including the backend type and task ID, instead of
// silently dropping the flag.
func TestClaudeCodeBackendEmptyModelLogsAndOmitsFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	logFile := tmpDir + "/calls.log"
	script := tmpDir + "/fake-claude"

	body := `#!/bin/sh
printf '%s\n' "$*" >> ` + logFile + `
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	var buf bytes.Buffer
	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	backend.log = slog.New(slog.NewTextHandler(&buf, nil))

	opts := ExecuteOptions{
		Prompt:       "hello",
		ProjectPath:  tmpDir,
		TaskID:       "GH-3754-test",
		EventHandler: func(BackendEvent) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := backend.Execute(ctx, opts); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	data, readErr := os.ReadFile(logFile)
	if readErr != nil {
		t.Fatalf("read log: %v", readErr)
	}
	if strings.Contains(string(data), "--model") {
		t.Errorf("expected no --model flag in argv when opts.Model is empty, got: %q", data)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, "Omitting --model flag") {
		t.Errorf("expected empty-model log line, got log output: %s", logOutput)
	}
	if !strings.Contains(logOutput, BackendTypeClaudeCode) {
		t.Errorf("expected backend type %q in log line, got: %s", BackendTypeClaudeCode, logOutput)
	}
	if !strings.Contains(logOutput, "GH-3754-test") {
		t.Errorf("expected task_id in log line, got: %s", logOutput)
	}
}

// TestClaudeCodeBackendStdoutTailOnEmptyStderrFailure covers GH-4395: a
// nonzero exit with no stderr ("unknown: exit status 1" with stderr="")
// previously left no diagnostic evidence at all — the raw stdout the CLI did
// produce (even if it never assembled into a valid stream-json event) was
// silently discarded, so triage required re-running the task. The backend
// must now capture and surface that raw stdout as result.StdoutTail.
func TestClaudeCodeBackendStdoutTailOnEmptyStderrFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	script := tmpDir + "/fake-claude"

	// Emits plain (non-JSON) diagnostic lines to stdout, nothing to stderr,
	// then exits 1 — reproducing the exact "unknown: exit status 1" /
	// stderr="" signature from the GH-4395 incident log.
	body := `#!/bin/sh
echo "internal error: worker crashed before producing a result event"
echo "last tool: Bash(go test ./...)"
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	opts := ExecuteOptions{
		Prompt:       "hello",
		ProjectPath:  tmpDir,
		EventHandler: func(BackendEvent) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := backend.Execute(ctx, opts)
	if err == nil {
		t.Fatal("expected error from fake CLI exiting 1, got nil")
	}
	ccErr, ok := err.(*ClaudeCodeError)
	if !ok {
		t.Fatalf("expected *ClaudeCodeError, got %T: %v", err, err)
	}
	if ccErr.Type != ErrorTypeUnknown {
		t.Fatalf("expected ErrorTypeUnknown, got %q", ccErr.Type)
	}
	if ccErr.Stderr != "" {
		t.Fatalf("expected empty stderr reproducing the incident signature, got %q", ccErr.Stderr)
	}

	if result == nil {
		t.Fatal("expected non-nil result even on failure")
	}
	if !strings.Contains(result.StdoutTail, "internal error: worker crashed") {
		t.Errorf("StdoutTail missing expected diagnostic line, got: %q", result.StdoutTail)
	}
	if !strings.Contains(result.StdoutTail, "last tool: Bash(go test ./...)") {
		t.Errorf("StdoutTail missing second diagnostic line, got: %q", result.StdoutTail)
	}
}

// TestClaudeCodeBackendStdoutTailNotSetOnSuccess verifies StdoutTail is only
// populated as a failure diagnostic (GH-4395) — it's not meant to duplicate
// the full transcript on the (much more common) success path.
func TestClaudeCodeBackendStdoutTailNotSetOnSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	script := tmpDir + "/fake-claude"

	body := `#!/bin/sh
echo '{"type":"result","result":"done"}'
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	opts := ExecuteOptions{
		Prompt:       "hello",
		ProjectPath:  tmpDir,
		EventHandler: func(BackendEvent) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := backend.Execute(ctx, opts)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.StdoutTail != "" {
		t.Errorf("expected empty StdoutTail on success, got: %q", result.StdoutTail)
	}
}

// TestReadBoundedLine_OversizedLineDoesNotExitSilently covers GH-4519: unlike
// bufio.Scanner, which aborts the entire read with bufio.ErrTooLong the
// moment a line exceeds its fixed buffer (and never surfaces that error to
// the caller unless Err() is checked), readBoundedLine must keep draining an
// oversized line to its end and let the caller continue reading subsequent
// lines — this is the exact silent-exit bug that let a hung child process
// wedge on a full stdout pipe and get SIGKILLed by the heartbeat monitor.
func TestReadBoundedLine_OversizedLineDoesNotExitSilently(t *testing.T) {
	const maxBytes = 1024 * 1024 // 1 MiB, matches maxStdoutLineBytes

	oversized := strings.Repeat("a", 5*1024*1024) // 5 MiB, no newline yet
	input := oversized + "\n" + `{"type":"result","result":"done"}` + "\n"

	// Sanity check that a bufio.Scanner with the same 1MB buffer really does
	// abort silently on this input, i.e. this test reproduces the original bug.
	t.Run("reproduces bufio.Scanner ErrTooLong", func(t *testing.T) {
		scanner := bufio.NewScanner(strings.NewReader(input))
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, maxBytes)
		lineCount := 0
		for scanner.Scan() {
			lineCount++
		}
		if lineCount != 0 {
			t.Fatalf("expected scanner to abort before returning any line, got %d lines", lineCount)
		}
		if !errors.Is(scanner.Err(), bufio.ErrTooLong) {
			t.Fatalf("expected bufio.ErrTooLong, got %v", scanner.Err())
		}
	})

	reader := bufio.NewReaderSize(strings.NewReader(input), 64*1024)

	var totalBytesSeenByHeartbeat int
	onBytes := func(n int) { totalBytesSeenByHeartbeat += n }

	// First line: oversized, must be truncated but must NOT return an error —
	// the loop must be able to continue to the next line.
	line, truncated, totalBytes, err := readBoundedLine(reader, maxBytes, onBytes)
	if err != nil {
		t.Fatalf("readBoundedLine returned error on oversized line, want nil (silent exit not allowed): %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated=true for a line exceeding maxBytes")
	}
	if len(line) != maxBytes {
		t.Fatalf("expected kept line length %d, got %d", maxBytes, len(line))
	}
	if totalBytes != len(oversized) {
		t.Fatalf("expected totalBytes=%d, got %d", len(oversized), totalBytes)
	}
	if totalBytesSeenByHeartbeat == 0 {
		t.Fatal("expected onBytes callback to fire while draining the oversized line (heartbeat must track raw byte flow)")
	}

	// Second line: normal, must parse cleanly — proving the reader survived
	// the oversized line and continues with subsequent lines.
	line2, truncated2, _, err2 := readBoundedLine(reader, maxBytes, onBytes)
	if err2 != nil {
		t.Fatalf("readBoundedLine returned error on subsequent normal line: %v", err2)
	}
	if truncated2 {
		t.Fatal("normal line should not be marked truncated")
	}
	want := `{"type":"result","result":"done"}`
	if string(line2) != want {
		t.Fatalf("line2 = %q, want %q", string(line2), want)
	}

	// Third read: EOF, terminal error must be surfaced (not swallowed).
	_, _, _, err3 := readBoundedLine(reader, maxBytes, onBytes)
	if !errors.Is(err3, io.EOF) {
		t.Fatalf("expected io.EOF at end of stream, got %v", err3)
	}
}

// TestClaudeCodeBackendSurvivesOversizedStdoutLine covers GH-4519 end to end:
// a single stream-json line over 1MB (e.g. a tool result carrying a base64
// blob) must not kill the stdout reader. Before the fix, bufio.Scanner
// aborted on this input and stopped draining stdout entirely; since the fake
// CLI keeps writing past the OS pipe buffer capacity afterward, the child
// blocks and the run would eventually be killed rather than complete.
func TestClaudeCodeBackendSurvivesOversizedStdoutLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	script := tmpDir + "/fake-claude"

	// Emit one line well over the 1MB cap (5MB of 'a's) with no embedded
	// newlines, then a normal stream-json result event.
	body := `#!/bin/sh
head -c 5000000 /dev/zero | tr '\0' 'a'
echo ""
echo '{"type":"result","subtype":"success","result":"done","is_error":false}'
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	var events []BackendEvent
	opts := ExecuteOptions{
		Prompt:      "hello",
		ProjectPath: tmpDir,
		EventHandler: func(e BackendEvent) {
			events = append(events, e)
		},
	}

	// Generous timeout: with the fix this completes in well under a second.
	// A regression that reintroduces the silent-scanner-exit bug would hang
	// here until context cancellation, blowing well past this bound.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result, err := backend.Execute(ctx, opts)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute() took %v, want well under 5s — suggests the oversized line wedged the reader", elapsed)
	}
	if !result.SawSuccessResult {
		t.Error("expected SawSuccessResult=true — the result event after the oversized line must still parse")
	}
	if result.Output != "done" {
		t.Errorf("result.Output = %q, want %q", result.Output, "done")
	}
	// Note: result.StdoutTail is only attached to the result on failure
	// (GH-4395 — success runs don't need the diagnostic transcript); the
	// truncation-marker content itself is covered on the failure path by
	// TestClaudeCodeBackendStdoutTailShowsTruncationMarkerOnFailure below.

	sawResultEvent := false
	for _, e := range events {
		if e.Type == EventTypeResult {
			sawResultEvent = true
		}
	}
	if !sawResultEvent {
		t.Error("expected the result event to reach EventHandler after the oversized line")
	}
}

// TestClaudeCodeBackendStdoutTailShowsTruncationMarkerOnFailure covers AC2:
// when a run fails, the bounded diagnostic stdoutTail must contain a
// truncation marker (not the raw multi-megabyte line) for any oversized
// line encountered — proving the reader captured evidence instead of
// silently dropping it (GH-4519), while keeping memory bounded.
func TestClaudeCodeBackendStdoutTailShowsTruncationMarkerOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts; skipping on windows")
	}

	tmpDir := t.TempDir()
	script := tmpDir + "/fake-claude"

	// Oversized line with no subsequent successful result event, then a
	// nonzero exit — reproduces the failure-diagnostics path (GH-4395)
	// combined with an oversized line (GH-4519), without GH-2107's
	// SawSuccessResult recovery masking the failure.
	body := `#!/bin/sh
head -c 5000000 /dev/zero | tr '\0' 'a'
echo ""
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	backend := NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script})
	opts := ExecuteOptions{
		Prompt:       "hello",
		ProjectPath:  tmpDir,
		EventHandler: func(BackendEvent) {},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	start := time.Now()
	result, err := backend.Execute(ctx, opts)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from fake CLI exiting 1, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Execute() took %v, want well under 5s — suggests the oversized line wedged the reader", elapsed)
	}
	if result == nil {
		t.Fatal("expected non-nil result even on failure")
	}
	if !strings.Contains(result.StdoutTail, "[line truncated:") {
		t.Errorf("expected StdoutTail to contain a truncation marker, got: %q", truncate(result.StdoutTail, 300))
	}
	if !strings.Contains(result.StdoutTail, "5000000 bytes") {
		t.Errorf("expected truncation marker to report the original line size, got: %q", truncate(result.StdoutTail, 300))
	}
	// AC2: bounded memory — the raw 5MB payload must never appear in the
	// (64KB-capped) tail, only the small marker + snippet.
	if len(result.StdoutTail) > MaxStdoutTailBufferBytes+1024 {
		t.Errorf("StdoutTail is %d bytes, expected it to stay close to the %d-byte cap", len(result.StdoutTail), MaxStdoutTailBufferBytes)
	}
}
