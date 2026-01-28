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

	"github.com/alekspetrov/pilot/internal/logging"
)

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

	// Wait for output readers
	wg.Wait()

	// Wait for command to complete
	err = cmd.Wait()
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
