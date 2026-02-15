package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// qwenToolNameMap normalizes Qwen Code tool names (snake_case) to the PascalCase
// names expected by Runner's handleToolUse() for progress phase detection.
var qwenToolNameMap = map[string]string{
	"read_file":         "Read",
	"write_file":        "Write",
	"edit":              "Edit",
	"run_shell_command": "Bash",
	"grep_search":       "Grep",
	"glob":              "Glob",
	"list_directory":    "Bash",
	"web_fetch":         "WebFetch",
	"web_search":        "WebSearch",
	"todo_write":        "TodoWrite",
	"save_memory":       "TodoWrite",
	"task":              "Task",
	"skill":             "Skill",
	"lsp":               "Bash",
	"exit_plan_mode":    "ExitPlanMode",
}

// normalizeQwenToolName maps Qwen Code snake_case tool names to Runner-compatible
// PascalCase names. MCP tools (prefixed with "mcp__") pass through unchanged.
func normalizeQwenToolName(name string) string {
	if mapped, ok := qwenToolNameMap[name]; ok {
		return mapped
	}
	return name
}

// QwenCodeErrorType categorizes different types of Qwen Code failures.
type QwenCodeErrorType string

const (
	QwenErrorTypeRateLimit        QwenCodeErrorType = "rate_limit"
	QwenErrorTypeAPIError         QwenCodeErrorType = "api_error"
	QwenErrorTypeTimeout          QwenCodeErrorType = "timeout"
	QwenErrorTypeInvalidConfig    QwenCodeErrorType = "invalid_config"
	QwenErrorTypeSessionNotFound  QwenCodeErrorType = "session_not_found"
	QwenErrorTypeUnknown          QwenCodeErrorType = "unknown"
)

// QwenCodeError represents a classified error from Qwen Code.
type QwenCodeError struct {
	Type    QwenCodeErrorType
	Message string
	Stderr  string
}

func (e *QwenCodeError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %s (stderr: %s)", e.Type, e.Message, e.Stderr)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// ErrorType implements BackendError.
func (e *QwenCodeError) ErrorType() string { return string(e.Type) }

// ErrorMessage implements BackendError.
func (e *QwenCodeError) ErrorMessage() string { return e.Message }

// ErrorStderr implements BackendError.
func (e *QwenCodeError) ErrorStderr() string { return e.Stderr }

// classifyQwenCodeError examines stderr output to classify the error.
func classifyQwenCodeError(stderr string, originalErr error) *QwenCodeError {
	stderrLower := strings.ToLower(stderr)

	// Rate limit detection
	if strings.Contains(stderrLower, "rate limit") ||
		strings.Contains(stderrLower, "hit your limit") ||
		strings.Contains(stderrLower, "too many requests") ||
		strings.Contains(stderrLower, "429") {
		return &QwenCodeError{
			Type:    QwenErrorTypeRateLimit,
			Message: "Qwen Code rate limit reached",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Invalid config detection
	if strings.Contains(stderrLower, "invalid model") ||
		strings.Contains(stderrLower, "not available") ||
		strings.Contains(stderrLower, "unknown option") ||
		strings.Contains(stderrLower, "unrecognized") {
		return &QwenCodeError{
			Type:    QwenErrorTypeInvalidConfig,
			Message: "Invalid Qwen Code configuration",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// API errors
	if strings.Contains(stderrLower, "api error") ||
		strings.Contains(stderrLower, "authentication") ||
		strings.Contains(stderrLower, "unauthorized") ||
		strings.Contains(stderrLower, "403") ||
		strings.Contains(stderrLower, "401") {
		return &QwenCodeError{
			Type:    QwenErrorTypeAPIError,
			Message: "Qwen API error",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Session not found/expired
	if strings.Contains(stderrLower, "session not found") ||
		strings.Contains(stderrLower, "session expired") ||
		strings.Contains(stderrLower, "invalid session") {
		return &QwenCodeError{
			Type:    QwenErrorTypeSessionNotFound,
			Message: "Qwen session not found",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Timeout/killed
	if strings.Contains(stderrLower, "killed") ||
		strings.Contains(stderrLower, "signal") ||
		strings.Contains(stderrLower, "timeout") {
		return &QwenCodeError{
			Type:    QwenErrorTypeTimeout,
			Message: "Process killed or timed out",
			Stderr:  strings.TrimSpace(stderr),
		}
	}

	// Unknown error
	msg := "Unknown error"
	if originalErr != nil {
		msg = originalErr.Error()
	}
	return &QwenCodeError{
		Type:    QwenErrorTypeUnknown,
		Message: msg,
		Stderr:  strings.TrimSpace(stderr),
	}
}

// QwenCodeBackend implements Backend for Qwen Code CLI.
// Qwen Code is a Gemini CLI fork that supports --output-format stream-json
// with a nearly identical event structure to Claude Code.
type QwenCodeBackend struct {
	config *QwenCodeConfig
	log    *slog.Logger
}

// NewQwenCodeBackend creates a new Qwen Code backend.
func NewQwenCodeBackend(config *QwenCodeConfig) *QwenCodeBackend {
	if config == nil {
		config = &QwenCodeConfig{Command: "qwen"}
	}
	if config.Command == "" {
		config.Command = "qwen"
	}
	return &QwenCodeBackend{
		config: config,
		log:    logging.WithComponent("executor.qwencode"),
	}
}

// Name returns the backend identifier.
func (b *QwenCodeBackend) Name() string {
	return BackendTypeQwenCode
}

// IsAvailable checks if Qwen Code CLI is installed.
func (b *QwenCodeBackend) IsAvailable() bool {
	path, err := exec.LookPath(b.config.Command)
	if err != nil {
		return false
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		b.log.Warn("qwen-code: could not determine version", "error", err)
		return true
	}
	version := strings.TrimSpace(string(out))
	b.log.Info("qwen-code: detected version", "version", version)
	return true
}

// buildArgs constructs the CLI arguments for Qwen Code execution.
func (b *QwenCodeBackend) buildArgs(opts ExecuteOptions) []string {
	var args []string

	// Session resume support
	if opts.ResumeSessionID != "" && b.config.UseSessionResume {
		args = append(args, "--resume", opts.ResumeSessionID)
		b.log.Info("Resuming Qwen Code session",
			slog.String("session_id", opts.ResumeSessionID),
		)
	}

	// Core flags
	args = append(args,
		"-p", opts.Prompt,
		"--output-format", "stream-json",
		"--yolo", // Qwen's equivalent of --dangerously-skip-permissions
	)

	// Model flag
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
		b.log.Info("Using routed model", slog.String("model", opts.Model))
	}

	// Note: --effort and --from-pr are not supported by Qwen Code — skip silently

	// Extra args from config
	args = append(args, b.config.ExtraArgs...)

	return args
}

// Execute runs a prompt through Qwen Code CLI.
func (b *QwenCodeBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	args := b.buildArgs(opts)

	cmd := exec.CommandContext(ctx, b.config.Command, args...)
	cmd.Dir = opts.ProjectPath

	b.log.Debug("Starting Qwen Code",
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
		return nil, fmt.Errorf("failed to start Qwen Code: %w", err)
	}
	b.log.Debug("Qwen Code started", slog.Int("pid", cmd.Process.Pid))

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
				if age > HeartbeatTimeout {
					b.log.Warn("Heartbeat timeout detected, killing hung process",
						slog.Int("pid", cmd.Process.Pid),
						slog.Duration("last_event_age", age),
						slog.Duration("timeout", HeartbeatTimeout),
					)

					if opts.HeartbeatCallback != nil {
						opts.HeartbeatCallback(cmd.Process.Pid, age)
					}

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

	// Watchdog goroutine: hard kill after absolute timeout
	if opts.WatchdogTimeout > 0 {
		go func() {
			select {
			case <-cmdDone:
				return
			case <-time.After(opts.WatchdogTimeout):
				if cmd.Process == nil {
					return
				}

				b.log.Warn("Watchdog timeout expired, forcibly killing subprocess",
					slog.Int("pid", cmd.Process.Pid),
					slog.Duration("watchdog_timeout", opts.WatchdogTimeout),
				)

				if opts.WatchdogCallback != nil {
					opts.WatchdogCallback(cmd.Process.Pid, opts.WatchdogTimeout)
				}

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
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			// Update heartbeat timestamp
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
				if event.IsError {
					result.Error = event.Message
				} else {
					result.Output = event.Message
				}
			}

			// Capture session ID from init event
			if event.Type == EventTypeInit && event.SessionID != "" {
				result.SessionID = event.SessionID
			}

			// Accumulate token usage
			result.TokensInput += event.TokensInput
			result.TokensOutput += event.TokensOutput
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
			return
		case <-ctx.Done():
			if cmd.Process == nil {
				return
			}

			b.log.Warn("Context cancelled, waiting grace period before hard kill",
				slog.Int("pid", cmd.Process.Pid),
				slog.Duration("grace_period", GracePeriod),
			)

			select {
			case <-cmdDone:
				b.log.Debug("Process exited gracefully after context cancellation",
					slog.Int("pid", cmd.Process.Pid),
				)
				return
			case <-time.After(GracePeriod):
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
	close(cmdDone)

	if err != nil {
		result.Success = false

		stderrStr := stderrOutput.String()
		qcErr := classifyQwenCodeError(stderrStr, err)

		b.log.Warn("Qwen Code execution failed",
			slog.String("error_type", string(qcErr.Type)),
			slog.String("message", qcErr.Message),
			slog.String("stderr", qcErr.Stderr),
		)

		// Fallback if --resume fails with session not found
		if qcErr.Type == QwenErrorTypeSessionNotFound && opts.ResumeSessionID != "" {
			b.log.Warn("qwen-code: session not found, retrying without --resume",
				"session_id", opts.ResumeSessionID)
			opts.ResumeSessionID = ""
			return b.Execute(ctx, opts)
		}

		if result.Error == "" {
			result.Error = qcErr.Error()
		}

		return result, qcErr
	}

	result.Success = true
	return result, nil
}

// parseStreamEvent converts Qwen Code stream-json to BackendEvent.
// Qwen Code's stream-json format is nearly identical to Claude Code's:
// same top-level types (system, assistant, user, result), same content block
// structure, same usage fields. Key differences:
// 1. Tool names are snake_case (read_file vs Read) — normalized via qwenToolNameMap
// 2. User messages contain tool_result blocks in message.content[] instead of flat tool_use_result
func (b *QwenCodeBackend) parseStreamEvent(line string) BackendEvent {
	event := BackendEvent{
		Raw: line,
	}

	var streamEvent StreamEvent
	if err := json.Unmarshal([]byte(line), &streamEvent); err != nil {
		event.Type = EventTypeText
		event.Message = line
		return event
	}

	switch streamEvent.Type {
	case "system":
		if streamEvent.Subtype == "init" {
			event.Type = EventTypeInit
			event.SessionID = streamEvent.SessionID
			event.Message = "Qwen Code initialized"
		}

	case "assistant":
		if streamEvent.Message != nil {
			for _, block := range streamEvent.Message.Content {
				switch block.Type {
				case "tool_use":
					event.Type = EventTypeToolUse
					// Normalize snake_case tool names to PascalCase for Runner compatibility
					event.ToolName = normalizeQwenToolName(block.Name)
					event.ToolInput = block.Input
					event.Message = fmt.Sprintf("Using %s", event.ToolName)
				case "text":
					event.Type = EventTypeText
					event.Message = block.Text
				}
			}
		}

	case "user":
		// Qwen Code sends tool results in message.content[] as tool_result blocks.
		// The result text may be in "content" or "text" field depending on version.
		if streamEvent.Message != nil {
			for _, block := range streamEvent.Message.Content {
				if block.Type == "tool_result" {
					event.Type = EventTypeToolResult
					// Prefer "content" field (Qwen standard), fall back to "text"
					if block.Content != "" {
						event.ToolResult = block.Content
					} else {
						event.ToolResult = block.Text
					}
					event.IsError = block.IsError
				}
			}
		}
		// Also handle Claude-style flat tool_use_result for forward compatibility
		if event.Type == "" && streamEvent.ToolUseResult != nil {
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
	}
	if streamEvent.Model != "" {
		event.Model = streamEvent.Model
	}

	return event
}
