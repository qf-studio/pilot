package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
)

// GracePeriod is the time to wait after context cancellation before hard killing the process.
// This allows the process to clean up gracefully if it responds to SIGTERM.
const GracePeriod = 5 * time.Second

// DefaultHeartbeatTimeout is the default time to wait for any stream-json event before considering the process hung.
const DefaultHeartbeatTimeout = 5 * time.Minute

// MinHeartbeatTimeout is the minimum allowed heartbeat timeout.
const MinHeartbeatTimeout = 1 * time.Minute

// MaxHeartbeatTimeout is the maximum allowed heartbeat timeout.
const MaxHeartbeatTimeout = 30 * time.Minute

// HeartbeatCheckInterval is how often to check for heartbeat timeout.
const HeartbeatCheckInterval = 30 * time.Second

// HeartbeatCallback is a callback invoked when heartbeat timeout is detected.
// Returns true if the callback wants to handle the timeout (process will be killed).
type HeartbeatCallback func(pid int, lastEventAge time.Duration)

// ClaudeCodeErrorType categorizes different types of Claude Code failures.
// GH-917: Better error classification enables smarter retry decisions.
type ClaudeCodeErrorType string

const (
	// ErrorTypeRateLimit indicates Claude Code hit a rate limit
	ErrorTypeRateLimit ClaudeCodeErrorType = "rate_limit"
	// ErrorTypeInvalidConfig indicates invalid configuration (e.g., --effort max)
	ErrorTypeInvalidConfig ClaudeCodeErrorType = "invalid_config"
	// ErrorTypeAPIError indicates Claude API errors (auth, server errors)
	ErrorTypeAPIError ClaudeCodeErrorType = "api_error"
	// ErrorTypeTimeout indicates the process was killed due to timeout
	ErrorTypeTimeout ClaudeCodeErrorType = "timeout"
	// ErrorTypeOOM indicates the process was OOM-killed (exit 137/139) (GH-2112)
	ErrorTypeOOM ClaudeCodeErrorType = "oom_killed"
	// ErrorTypeSessionNotFound indicates the session for --from-pr or --resume was not found (GH-1267)
	ErrorTypeSessionNotFound ClaudeCodeErrorType = "session_not_found"
	// ErrorTypeUnknown indicates an unclassified error
	ErrorTypeUnknown ClaudeCodeErrorType = "unknown"
)

// ClaudeCodeError represents a classified error from Claude Code.
type ClaudeCodeError struct {
	Type    ClaudeCodeErrorType
	Message string
	Stderr  string
}

func (e *ClaudeCodeError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %s (stderr: %s)", e.Type, e.Message, e.Stderr)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// ErrorType implements BackendError.
func (e *ClaudeCodeError) ErrorType() string { return string(e.Type) }

// ErrorMessage implements BackendError.
func (e *ClaudeCodeError) ErrorMessage() string { return e.Message }

// ErrorStderr implements BackendError.
func (e *ClaudeCodeError) ErrorStderr() string { return e.Stderr }

// classifyClaudeCodeError examines stderr output and exit code to classify the error.
func classifyClaudeCodeError(stderr string, originalErr error) *ClaudeCodeError {
	// GH-2112: Check exit code first — OOM kills (137=SIGKILL, 139=SIGSEGV) often
	// produce no stderr, so exit code is the only reliable signal.
	if exitCode := extractExitCode(originalErr); exitCode == 137 || exitCode == 139 {
		sigName := "SIGKILL"
		if exitCode == 139 {
			sigName = "SIGSEGV"
		}
		return &ClaudeCodeError{
			Type:    ErrorTypeOOM,
			Message: fmt.Sprintf("Process killed by %s (exit code %d)", sigName, exitCode),
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	stderrLower := strings.ToLower(stderr)

	// Rate limit detection
	if strings.Contains(stderrLower, "hit your limit") ||
		strings.Contains(stderrLower, "rate limit") ||
		strings.Contains(stderrLower, "resets") && strings.Contains(stderrLower, "limit") {
		return &ClaudeCodeError{
			Type:    ErrorTypeRateLimit,
			Message: "Claude Code rate limit reached",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Invalid config detection (effort level, model, etc.)
	if strings.Contains(stderrLower, "effort level") ||
		strings.Contains(stderrLower, "is not available") ||
		strings.Contains(stderrLower, "invalid model") ||
		strings.Contains(stderrLower, "requires --verbose") {
		return &ClaudeCodeError{
			Type:    ErrorTypeInvalidConfig,
			Message: "Invalid Claude Code configuration",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// API errors
	if strings.Contains(stderrLower, "api error") ||
		strings.Contains(stderrLower, "authentication") ||
		strings.Contains(stderrLower, "unauthorized") ||
		strings.Contains(stderrLower, "403") ||
		strings.Contains(stderrLower, "401") {
		return &ClaudeCodeError{
			Type:    ErrorTypeAPIError,
			Message: "Claude API error",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Session not found (GH-1267: --from-pr or --resume failed)
	if strings.Contains(stderrLower, "session not found") ||
		strings.Contains(stderrLower, "no session") ||
		strings.Contains(stderrLower, "session expired") ||
		strings.Contains(stderrLower, "could not find session") ||
		strings.Contains(stderrLower, "invalid session") {
		return &ClaudeCodeError{
			Type:    ErrorTypeSessionNotFound,
			Message: "Session not found for --from-pr or --resume",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Timeout/killed
	if strings.Contains(stderrLower, "killed") ||
		strings.Contains(stderrLower, "signal") ||
		strings.Contains(stderrLower, "timeout") {
		return &ClaudeCodeError{
			Type:    ErrorTypeTimeout,
			Message: "Process killed or timed out",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Unknown error
	msg := "Unknown error"
	if originalErr != nil {
		msg = originalErr.Error()
	}
	return &ClaudeCodeError{
		Type:    ErrorTypeUnknown,
		Message: msg,
		Stderr:  strings.TrimSpace(stderr),
	}
}

// extractExitCode returns the process exit code from an exec.ExitError, or -1 if unavailable.
func extractExitCode(err error) int {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return -1
	}
	// On Unix, check for signal-based termination (128+signal)
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	return exitErr.ExitCode()
}

// parseClaudeCodeError examines stderr output and exit code to classify the error.
// This function matches the specification in GH-917 and returns error interface.
func parseClaudeCodeError(stderr string, originalErr error) error {
	return classifyClaudeCodeError(stderr, originalErr)
}

// ClaudeCodeBackend implements Backend for Claude Code CLI.
type ClaudeCodeBackend struct {
	config           *ClaudeCodeConfig
	heartbeatTimeout time.Duration
	log              *slog.Logger
}

// NewClaudeCodeBackend creates a new Claude Code backend.
func NewClaudeCodeBackend(config *ClaudeCodeConfig) *ClaudeCodeBackend {
	if config == nil {
		config = &ClaudeCodeConfig{Command: "claude"}
	}
	if config.Command == "" {
		config.Command = "claude"
	}
	return &ClaudeCodeBackend{
		config:           config,
		heartbeatTimeout: DefaultHeartbeatTimeout,
		log:              logging.WithComponent("executor.claudecode"),
	}
}

// SetHeartbeatTimeout sets a custom heartbeat timeout for this backend.
func (b *ClaudeCodeBackend) SetHeartbeatTimeout(d time.Duration) {
	b.heartbeatTimeout = d
}

// Name returns the backend identifier.
func (b *ClaudeCodeBackend) Name() string {
	return BackendTypeClaudeCode
}

// IsAvailable checks if Claude Code CLI is installed.
func (b *ClaudeCodeBackend) IsAvailable() bool {
	_, err := exec.LookPath(b.config.Command)
	return err == nil
}

// Execute runs a prompt through Claude Code CLI.
// If --from-pr is used and fails with session not found, it falls back to executing without it.
func (b *ClaudeCodeBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	result, err := b.executeWithFromPR(ctx, opts, true)

	// GH-1267: Fallback if --from-pr fails with session not found
	if err != nil && opts.FromPR > 0 && b.config.UseFromPR {
		if ccErr, ok := err.(*ClaudeCodeError); ok && ccErr.Type == ErrorTypeSessionNotFound {
			b.log.Warn("Session not found for --from-pr, retrying without it",
				slog.Int("pr", opts.FromPR),
				slog.String("error", ccErr.Message),
			)
			// Retry without --from-pr
			return b.executeWithFromPR(ctx, opts, false)
		}
	}

	return result, err
}

// executeWithFromPR is the internal implementation that allows controlling --from-pr usage.
// When allowFromPR is false, it skips --from-pr even if opts.FromPR is set.
// This enables fallback retry without --from-pr if the session is not found.
func (b *ClaudeCodeBackend) executeWithFromPR(ctx context.Context, opts ExecuteOptions, allowFromPR bool) (*BackendResult, error) {
	// Build command arguments
	var args []string

	// GH-1267: Use --from-pr for session resumption from PR context
	// This takes precedence over --resume since it's more specific.
	if opts.FromPR > 0 && allowFromPR && b.config.UseFromPR {
		args = []string{
			"--from-pr", strconv.Itoa(opts.FromPR),
			"-p", opts.Prompt,
			"--verbose",
			"--output-format", "stream-json",
			"--dangerously-skip-permissions",
		}
		b.log.Info("Resuming session from PR context",
			slog.Int("pr", opts.FromPR),
		)
	} else if opts.ResumeSessionID != "" {
		// GH-1265: Use --resume for session continuation (e.g., self-review)
		args = []string{
			"--resume", opts.ResumeSessionID,
			"-p", opts.Prompt,
			"--verbose",
			"--output-format", "stream-json",
			"--dangerously-skip-permissions",
		}
		b.log.Info("Resuming session for context continuation",
			slog.String("session_id", opts.ResumeSessionID),
		)
	} else {
		args = []string{
			"-p", opts.Prompt,
			"--verbose",
			"--output-format", "stream-json",
			"--dangerously-skip-permissions",
		}
	}

	// Add model flag if specified (model routing GH-215)
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
		b.log.Info("Using routed model", slog.String("model", opts.Model))
	}

	// Add effort flag if specified (effort routing)
	// Note: Claude Code CLI may not support --effort yet; this is future-proofed.
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
		b.log.Info("Using routed effort", slog.String("effort", opts.Effort))
	}

	args = append(args, b.config.ExtraArgs...)

	cmd := exec.CommandContext(ctx, b.config.Command, args...)
	cmd.Dir = opts.ProjectPath

	// Pass context window and output token env vars if configured (GH-2163).
	if b.config.Disable1MContext || b.config.MaxOutputTokens > 0 {
		env := os.Environ()
		if b.config.Disable1MContext {
			env = append(env, "CLAUDE_CODE_DISABLE_1M_CONTEXT=1")
		}
		if b.config.MaxOutputTokens > 0 {
			env = append(env, fmt.Sprintf("CLAUDE_CODE_MAX_OUTPUT_TOKENS=%d", b.config.MaxOutputTokens))
		}
		cmd.Env = env
	}

	b.log.Debug("Starting Claude Code",
		slog.String("command", b.config.Command),
		slog.String("project", opts.ProjectPath),
	)

	// Create pipes for output
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Claude Code: %w", err)
	}
	b.log.Debug("Claude Code started", slog.Int("pid", cmd.Process.Pid))

	// Track results
	result := &BackendResult{}
	var stderrOutput strings.Builder
	var wg sync.WaitGroup

	// Channel to signal command completion
	cmdDone := make(chan struct{})

	// Heartbeat tracking: store last event time as Unix nano (atomic int64)
	var lastEventAt atomic.Int64
	lastEventAt.Store(time.Now().UnixNano())

	// Heartbeat monitor goroutine
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	go func() {
		ticker := time.NewTicker(HeartbeatCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-cmdDone:
				return
			case <-ticker.C:
				lastNano := lastEventAt.Load()
				lastTime := time.Unix(0, lastNano)
				age := time.Since(lastTime)
				if age > b.heartbeatTimeout {
					b.log.Warn("Heartbeat timeout detected, killing hung process",
						slog.Int("pid", cmd.Process.Pid),
						slog.Duration("last_event_age", age),
						slog.Duration("timeout", b.heartbeatTimeout),
					)

					// Invoke callback if provided
					if opts.HeartbeatCallback != nil {
						opts.HeartbeatCallback(cmd.Process.Pid, age)
					}

					// Kill the hung process
					if cmd.Process != nil {
						if err := cmd.Process.Kill(); err != nil {
							b.log.Error("Failed to kill hung process",
								slog.Int("pid", cmd.Process.Pid),
								slog.Any("error", err),
							)
						} else {
							b.log.Info("Hung process killed successfully",
								slog.Int("pid", cmd.Process.Pid),
							)
						}
					}
					return
				}
			}
		}
	}()

	// Watchdog goroutine: hard kill after absolute timeout (GH-882)
	// This is a safety net for processes that ignore context cancellation.
	if opts.WatchdogTimeout > 0 {
		go func() {
			select {
			case <-cmdDone:
				// Command completed normally, watchdog not needed
				return
			case <-time.After(opts.WatchdogTimeout):
				// Watchdog timeout expired, forcibly kill the process
				if cmd.Process == nil {
					return
				}

				b.log.Warn("Watchdog timeout expired, forcibly killing subprocess",
					slog.Int("pid", cmd.Process.Pid),
					slog.Duration("watchdog_timeout", opts.WatchdogTimeout),
				)

				// Invoke callback before killing (allows alert emission)
				if opts.WatchdogCallback != nil {
					opts.WatchdogCallback(cmd.Process.Pid, opts.WatchdogTimeout)
				}

				// Kill the process
				if err := cmd.Process.Kill(); err != nil {
					b.log.Error("Watchdog failed to kill process",
						slog.Int("pid", cmd.Process.Pid),
						slog.Any("error", err),
					)
				} else {
					b.log.Info("Watchdog killed process successfully",
						slog.Int("pid", cmd.Process.Pid),
					)
				}
			}
		}()
	}

	// Read stdout (stream-json events)
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdout)
		// Increase buffer size for large JSON events
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			// Update heartbeat timestamp on each stream event
			lastEventAt.Store(time.Now().UnixNano())

			if opts.Verbose {
				fmt.Printf("   %s\n", line)
			}

			// Parse and convert to BackendEvent
			event := b.parseStreamEvent(line)
			if opts.EventHandler != nil {
				opts.EventHandler(event)
			}

			// Track final result
			if event.Type == EventTypeResult {
				// GH-2103: Cancel heartbeat on result event.
				// On slow I/O flush, the heartbeat timer could fire and kill
				// the process after it had already produced output.
				cancelHeartbeat()

				if event.IsError {
					result.Error = event.Message
				} else {
					result.Output = event.Message
					result.SawSuccessResult = true // GH-2107: track successful result for timeout recovery
				}
				// Cancel heartbeat — process is finishing, don't kill it
				cancelHeartbeat()
			}

			// Capture session ID from init event (GH-1265)
			if event.Type == EventTypeInit && event.SessionID != "" {
				result.SessionID = event.SessionID
			}

			// Accumulate token usage
			result.TokensInput += event.TokensInput
			result.TokensOutput += event.TokensOutput
			result.CacheCreationInputTokens += event.CacheCreationInputTokens
			result.CacheReadInputTokens += event.CacheReadInputTokens
			if event.Model != "" {
				result.Model = event.Model
			}
		}
	}()

	// Read stderr
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			stderrOutput.WriteString(line + "\n")
			if opts.Verbose {
				fmt.Printf("   [err] %s\n", line)
			}
		}
	}()

	// Monitor context for timeout and handle hard kill
	go func() {
		select {
		case <-cmdDone:
			// Command completed normally, nothing to do
			return
		case <-ctx.Done():
			// Context cancelled (timeout or explicit cancellation)
			// exec.CommandContext will send SIGTERM/interrupt, wait grace period then SIGKILL
			if cmd.Process == nil {
				return
			}

			b.log.Warn("Context cancelled, waiting grace period before hard kill",
				slog.Int("pid", cmd.Process.Pid),
				slog.Duration("grace_period", GracePeriod),
			)

			// Wait for grace period or command to exit
			select {
			case <-cmdDone:
				// Process exited gracefully after signal
				b.log.Debug("Process exited gracefully after context cancellation",
					slog.Int("pid", cmd.Process.Pid),
				)
				return
			case <-time.After(GracePeriod):
				// Grace period expired, hard kill
				if cmd.Process != nil {
					b.log.Warn("Grace period expired, sending SIGKILL",
						slog.Int("pid", cmd.Process.Pid),
					)
					if err := cmd.Process.Kill(); err != nil {
						b.log.Error("Failed to kill process",
							slog.Int("pid", cmd.Process.Pid),
							slog.Any("error", err),
						)
					} else {
						b.log.Info("Process killed successfully",
							slog.Int("pid", cmd.Process.Pid),
						)
					}
				}
			}
		}
	}()

	// Wait for output readers
	wg.Wait()

	// Wait for command to complete
	err = cmd.Wait()
	close(cmdDone) // Signal that command is done

	if err != nil {
		// GH-2107: If a successful result event was seen before the process exited with
		// an error, the work was completed but Claude Code timed out on a subsequent turn
		// (e.g., writing final summary). Recover as success.
		if result.SawSuccessResult {
			b.log.Info("Recovering success: process exited with error after successful result event (GH-2107)",
				slog.String("exit_error", err.Error()),
				slog.String("output_preview", truncate(result.Output, 200)),
			)
			result.Success = true
			return result, nil
		}

		result.Success = false

		// GH-917: Classify the error for better handling
		stderr := stderrOutput.String()
		ccErr := parseClaudeCodeError(stderr, err).(*ClaudeCodeError)

		// GH-2112: Log OOM kills at error level for monitoring
		if ccErr.Type == ErrorTypeOOM {
			b.log.Error("Claude Code process OOM-killed",
				slog.String("error_type", string(ccErr.Type)),
				slog.String("message", ccErr.Message),
				slog.String("stderr", ccErr.Stderr),
			)
		} else {
			b.log.Warn("Claude Code execution failed",
				slog.String("error_type", string(ccErr.Type)),
				slog.String("message", ccErr.Message),
				slog.String("stderr", ccErr.Stderr),
			)
		}

		// Store classified error info in result
		if result.Error == "" {
			result.Error = ccErr.Error()
		}

		// Return classified error for upstream handling
		return result, ccErr
	}

	result.Success = true
	return result, nil
}

// truncate returns the first n characters of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// parseStreamEvent converts Claude Code stream-json to BackendEvent.
func (b *ClaudeCodeBackend) parseStreamEvent(line string) BackendEvent {
	event := BackendEvent{
		Raw: line,
	}

	var streamEvent StreamEvent
	if err := json.Unmarshal([]byte(line), &streamEvent); err != nil {
		// Not valid JSON, return as-is
		event.Type = EventTypeText
		event.Message = line
		return event
	}

	// Map stream event type to backend event type
	switch streamEvent.Type {
	case "system":
		if streamEvent.Subtype == "init" {
			event.Type = EventTypeInit
			event.SessionID = streamEvent.SessionID // Capture session ID for resume (GH-1265)
			event.Message = "Claude Code initialized"
		}

	case "assistant":
		if streamEvent.Message != nil {
			for _, block := range streamEvent.Message.Content {
				switch block.Type {
				case "tool_use":
					event.Type = EventTypeToolUse
					event.ToolName = block.Name
					event.ToolInput = block.Input
					event.Message = fmt.Sprintf("Using %s", block.Name)
				case "text":
					event.Type = EventTypeText
					event.Message = block.Text
				}
			}
		}

	case "user":
		// Tool results
		if streamEvent.ToolUseResult != nil {
			event.Type = EventTypeToolResult
			var toolResult ToolResultContent
			if err := json.Unmarshal(streamEvent.ToolUseResult, &toolResult); err == nil {
				event.ToolResult = toolResult.Content
				event.IsError = toolResult.IsError
			}
		}

	case "result":
		event.Type = EventTypeResult
		event.Message = streamEvent.Result
		event.IsError = streamEvent.IsError
	}

	// Capture usage info
	if streamEvent.Usage != nil {
		event.TokensInput = streamEvent.Usage.InputTokens
		event.TokensOutput = streamEvent.Usage.OutputTokens
		event.CacheCreationInputTokens = streamEvent.Usage.CacheCreationInputTokens
		event.CacheReadInputTokens = streamEvent.Usage.CacheReadInputTokens
	}
	if streamEvent.Model != "" {
		event.Model = streamEvent.Model
	}

	return event
}
