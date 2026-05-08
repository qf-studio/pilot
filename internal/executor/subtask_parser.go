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

const subtaskExtractionSystemPrompt = `Extract subtasks from this planning output as JSON. Return ONLY a JSON object with a "subtasks" array. Each subtask must have: "order" (integer), "title" (string), "description" (string).

Example response:
{"subtasks": [{"order": 1, "title": "Set up database", "description": "Create tables and migrations"}, {"order": 2, "title": "Add API endpoints", "description": "REST endpoints for CRUD operations"}]}`

const subtaskReformatSystemPrompt = `Reformat subtask titles to conventional-commits format. Use the parent task title as context for type and scope. Return ONLY a JSON object with a "subtasks" array. Each subtask must have "order" (integer) and "title" (string) in conventional-commits format (type(scope): description).`

// SubtaskParser extracts subtasks from planning output using the claude subprocess.
// Part of the epic planning pipeline: PlanEpic → parseSubtasksWithFallback → SubtaskParser.
// When the claude binary is missing or fails, parseSubtasksWithFallback falls back to
// regex-based parseSubtasks() in epic.go.
type SubtaskParser struct {
	claudeCmd string
	model     string
	timeout   time.Duration
	log       *slog.Logger
	cmdRunner func(ctx context.Context, args ...string) ([]byte, error)
}

// NewSubtaskParser creates a SubtaskParser using the claude subprocess.
// Returns nil when the claude binary is not on PATH (caller should use regex fallback).
func NewSubtaskParser(claudeCmd string, log *slog.Logger) *SubtaskParser {
	if claudeCmd == "" {
		claudeCmd = "claude"
	}
	if _, err := exec.LookPath(claudeCmd); err != nil {
		if log != nil {
			log.Warn("claude binary not found, subtask parser disabled (regex fallback will be used)",
				"command", claudeCmd, "error", err)
		}
		return nil
	}
	p := &SubtaskParser{
		claudeCmd: claudeCmd,
		model:     "claude-haiku-4-5-20251001",
		timeout:   30 * time.Second,
		log:       log,
	}
	p.cmdRunner = p.defaultRunner
	return p
}

// newSubtaskParserWithRunner creates a SubtaskParser with an injectable runner for testing.
func newSubtaskParserWithRunner(runner func(ctx context.Context, args ...string) ([]byte, error), log *slog.Logger) *SubtaskParser {
	return &SubtaskParser{
		claudeCmd: "claude",
		model:     "claude-haiku-4-5-20251001",
		timeout:   30 * time.Second,
		log:       log,
		cmdRunner: runner,
	}
}

func (p *SubtaskParser) defaultRunner(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, p.claudeCmd, args...)
	return cmd.Output()
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

// stripJSONFences removes markdown code fence wrappers that Claude sometimes adds.
func stripJSONFences(b []byte) []byte {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return []byte(strings.TrimSpace(s))
}

// Parse sends the planning output to claude subprocess for structured extraction.
// Returns the extracted subtasks or an error if the subprocess call fails.
func (p *SubtaskParser) Parse(ctx context.Context, output string) ([]PlannedSubtask, error) {
	if p == nil {
		return nil, fmt.Errorf("subtask parser is nil")
	}

	prompt := fmt.Sprintf("%s\n\n---\n\n%s", subtaskExtractionSystemPrompt, output)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	out, err := p.cmdRunner(ctx, "--print", "-p", prompt, "--model", p.model, "--output-format", "text")
	if err != nil {
		return nil, fmt.Errorf("subtask parser subprocess: %w", err)
	}

	var parsed subtasksResponse
	if err := json.Unmarshal(stripJSONFences(out), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse subtasks JSON: %w", err)
	}

	if len(parsed.Subtasks) == 0 {
		return nil, fmt.Errorf("no subtasks in subprocess response")
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

// ReformatTitles sends a batch of subtask titles to the claude subprocess and asks it to
// rewrite them in conventional-commits format. parentTitle provides type/scope context.
// Only the titles are updated; Order and Description are preserved from the input.
func (p *SubtaskParser) ReformatTitles(ctx context.Context, parentTitle string, subtasks []PlannedSubtask) ([]PlannedSubtask, error) {
	if p == nil {
		return nil, fmt.Errorf("subtask parser is nil")
	}

	var sb strings.Builder
	for _, st := range subtasks {
		fmt.Fprintf(&sb, "- Order %d: %q\n", st.Order, st.Title)
	}

	prompt := fmt.Sprintf(
		"%s\n\n---\n\nParent task: %q\n\nReformat these subtask titles to conventional-commits format (type(scope): description):\n%s\n"+
			`Return ONLY JSON: {"subtasks": [{"order": 1, "title": "feat(x): do y"}, ...]}`,
		subtaskReformatSystemPrompt, parentTitle, sb.String(),
	)

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	out, err := p.cmdRunner(ctx, "--print", "-p", prompt, "--model", p.model, "--output-format", "text")
	if err != nil {
		return nil, fmt.Errorf("subtask reformat subprocess: %w", err)
	}

	var parsed struct {
		Subtasks []struct {
			Order int    `json:"order"`
			Title string `json:"title"`
		} `json:"subtasks"`
	}
	if err := json.Unmarshal(stripJSONFences(out), &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse reformatted titles JSON: %w", err)
	}
	if len(parsed.Subtasks) == 0 {
		return nil, fmt.Errorf("no reformatted titles in response")
	}

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
// Tries claude subprocess extraction first (SubtaskParser.Parse), then falls back
// to regex-based parseSubtasks() in epic.go if the subprocess is unavailable or fails.
func parseSubtasksWithFallback(parser *SubtaskParser, output string) []PlannedSubtask {
	if parser != nil {
		subtasks, err := parser.Parse(context.Background(), output)
		if err == nil && len(subtasks) > 0 {
			if parser.log != nil {
				parser.log.Debug("Subtasks extracted via Haiku API", "count", len(subtasks))
			}
			return subtasks
		}
		if parser.log != nil {
			parser.log.Warn("Haiku subtask extraction failed, falling back to regex", "error", err)
		}
	}

	// Fallback to regex parsing
	return parseSubtasks(output)
}
