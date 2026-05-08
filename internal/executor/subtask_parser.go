package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

const subtaskParseSystemPrompt = `Extract subtasks from this planning output as JSON. Return ONLY a JSON object with a "subtasks" array. Each subtask must have: "order" (integer), "title" (string), "description" (string).

Example response:
{"subtasks": [{"order": 1, "title": "Set up database", "description": "Create tables and migrations"}, {"order": 2, "title": "Add API endpoints", "description": "REST endpoints for CRUD operations"}]}`

const subtaskReformatSystemPrompt = `Reformat subtask titles to conventional-commits format. Use the parent task title as context for type and scope. Return ONLY a JSON object with a "subtasks" array. Each subtask must have "order" (integer) and "title" (string) in conventional-commits format (type(scope): description).`

// SubtaskParser extracts subtasks from planning output using the claude subprocess.
// Part of the epic planning pipeline: PlanEpic → parseSubtasksWithFallback → SubtaskParser.
// When the binary is unavailable or fails, parseSubtasksWithFallback falls back to
// regex-based parseSubtasks() in epic.go.
type SubtaskParser struct {
	claudeCmd string
	model     string
	timeout   time.Duration
	log       *slog.Logger
	cmdRunner func(ctx context.Context, args ...string) ([]byte, error)
}

// NewSubtaskParser creates a SubtaskParser using the claude subprocess.
// Returns nil if the claude binary is not found on PATH (caller should use regex fallback).
func NewSubtaskParser(claudeCmd string, log *slog.Logger) *SubtaskParser {
	if claudeCmd == "" {
		claudeCmd = "claude"
	}
	if _, err := exec.LookPath(claudeCmd); err != nil {
		return nil
	}
	p := &SubtaskParser{
		claudeCmd: claudeCmd,
		model:     "claude-haiku-4-5-20251001",
		timeout:   30 * time.Second,
		log:       log,
	}
	p.cmdRunner = p.defaultCmdRunner
	return p
}

func (p *SubtaskParser) defaultCmdRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.claudeCmd, args...)
	return cmd.Output()
}

// newSubtaskParserWithRunner creates a SubtaskParser with a custom command runner for testing.
func newSubtaskParserWithRunner(runner func(ctx context.Context, args ...string) ([]byte, error), log *slog.Logger) *SubtaskParser {
	return &SubtaskParser{
		claudeCmd: "claude",
		model:     "claude-haiku-4-5-20251001",
		timeout:   30 * time.Second,
		log:       log,
		cmdRunner: runner,
	}
}

// subtaskJSON is the JSON schema for subtask extraction.
type subtaskJSON struct {
	Order       int    `json:"order"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// subtasksResponse wraps the array of subtasks in the response.
type subtasksResponse struct {
	Subtasks []subtaskJSON `json:"subtasks"`
}

// stripCodeFence removes markdown code fence wrappers from LLM text output.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// Parse sends the planning output to the claude subprocess for structured extraction.
// Returns the extracted subtasks or an error if the subprocess fails or output is unparseable.
func (p *SubtaskParser) Parse(ctx context.Context, output string) ([]PlannedSubtask, error) {
	if p == nil {
		return nil, fmt.Errorf("subtask parser is nil")
	}

	prompt := fmt.Sprintf("%s\n\n---\n\n%s", subtaskParseSystemPrompt, output)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.cmdRunner(ctx, "--print", "-p", prompt, "--model", p.model, "--output-format", "text")
	if err != nil {
		return nil, fmt.Errorf("claude subprocess failed: %w", err)
	}

	text := stripCodeFence(string(raw))

	var parsed subtasksResponse
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse subtasks JSON: %w", err)
	}

	if len(parsed.Subtasks) == 0 {
		return nil, fmt.Errorf("no subtasks in response")
	}

	result := make([]PlannedSubtask, len(parsed.Subtasks))
	for i, s := range parsed.Subtasks {
		result[i] = PlannedSubtask{
			Order:       s.Order,
			Title:       s.Title,
			Description: s.Description,
		}
	}

	return result, nil
}

// ReformatTitles sends a batch of subtask titles to the claude subprocess and asks it to rewrite
// them in conventional-commits format. parentTitle provides type/scope context.
// Only the titles are updated; Order and Description are preserved from the input.
// Returns an error when the parser is nil, the subprocess fails, or the response is empty.
func (p *SubtaskParser) ReformatTitles(ctx context.Context, parentTitle string, subtasks []PlannedSubtask) ([]PlannedSubtask, error) {
	if p == nil {
		return nil, fmt.Errorf("subtask parser is nil")
	}

	var sb strings.Builder
	for _, st := range subtasks {
		fmt.Fprintf(&sb, "- Order %d: %q\n", st.Order, st.Title)
	}

	userMsg := fmt.Sprintf(
		"Parent task: %q\n\nReformat these subtask titles to conventional-commits format (type(scope): description):\n%s\n"+
			`Return ONLY JSON: {"subtasks": [{"order": 1, "title": "feat(x): do y"}, ...]}`,
		parentTitle, sb.String(),
	)

	prompt := fmt.Sprintf("%s\n\n---\n\n%s", subtaskReformatSystemPrompt, userMsg)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.cmdRunner(ctx, "--print", "-p", prompt, "--model", p.model, "--output-format", "text")
	if err != nil {
		return nil, fmt.Errorf("claude subprocess failed: %w", err)
	}

	text := stripCodeFence(string(raw))

	var parsed struct {
		Subtasks []struct {
			Order int    `json:"order"`
			Title string `json:"title"`
		} `json:"subtasks"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse reformatted titles JSON: %w", err)
	}
	if len(parsed.Subtasks) == 0 {
		return nil, fmt.Errorf("no reformatted titles in response")
	}

	// Build order→title map and apply back to the input subtasks (preserve Description/Order).
	byOrder := make(map[int]string, len(parsed.Subtasks))
	for _, s := range parsed.Subtasks {
		byOrder[s.Order] = s.Title
	}

	result := make([]PlannedSubtask, len(subtasks))
	copy(result, subtasks)
	for i := range result {
		if t, ok := byOrder[result[i].Order]; ok && t != "" {
			result[i].Title = t
		}
	}
	return result, nil
}

// parseSubtasksWithFallback is the primary entry point for subtask extraction.
// Tries claude subprocess structured extraction first (SubtaskParser.Parse), then falls back
// to regex-based parseSubtasks() in epic.go if the subprocess is unavailable or fails.
func parseSubtasksWithFallback(parser *SubtaskParser, output string) []PlannedSubtask {
	if parser != nil {
		subtasks, err := parser.Parse(context.Background(), output)
		if err == nil && len(subtasks) > 0 {
			if parser.log != nil {
				parser.log.Debug("Subtasks extracted via claude subprocess", "count", len(subtasks))
			}
			return subtasks
		}
		if parser.log != nil {
			parser.log.Warn("claude subtask extraction failed, falling back to regex", "error", err)
		}
	}

	// Fallback to regex parsing
	return parseSubtasks(output)
}
