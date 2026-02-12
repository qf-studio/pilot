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

// GracePeriod is the time to wait after context cancellation before hard killing the process.
// This allows the process to clean up gracefully if it responds to SIGTERM.
const GracePeriod = 5 * time.Second

// HeartbeatTimeout is the time to wait for any stream-json event before considering the process hung.
const HeartbeatTimeout = 5 * time.Minute

// HeartbeatCheckInterval is how often to check for heartbeat timeout.
const HeartbeatCheckInterval = 30 * time.Second

// HeartbeatCallback is a callback invoked when heartbeat timeout is detected.
// Returns true if the callback wants to handle the timeout (process will be killed).
type HeartbeatCallback func(pid int, lastEventAge time.Duration)

// ClaudeCodeBackend implements Backend for Claude Code CLI.
type ClaudeCodeBackend struct {
	config *ClaudeCodeConfig
	log    *slog.Logger
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
		config: config,
		log:    logging.WithComponent("executor.claudecode"),
	}
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
func (b *ClaudeCodeBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	// Build command arguments
	args := []string{
		"-p", opts.Prompt,
		"--verbose",
		"--output-format", "stream-json",
		"--dangerously-skip-permissions",
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
				if age > HeartbeatTimeout {
					b.log.Warn("Heartbeat timeout detected, killing hung process",
						slog.Int("pid", cmd.Process.Pid),
						slog.Duration("last_event_age", age),
						slog.Duration("timeout", HeartbeatTimeout),
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
				if event.IsError {
					result.Error = event.Message
				} else {
					result.Output = event.Message
				}
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
		result.Success = false
		if result.Error == "" {
			result.Error = err.Error()
		}
		if stderrOutput.Len() > 0 && result.Error == "" {
			result.Error = stderrOutput.String()
		}
	} else {
		result.Success = true
	}

	return result, nil
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
	}
	if streamEvent.Model != "" {
		event.Model = streamEvent.Model
	}

	return event
}
