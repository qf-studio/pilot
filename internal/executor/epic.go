package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// EpicPlan represents the result of planning an epic task.
// Contains the parent task and the subtasks derived from Claude Code's planning output.
type EpicPlan struct {
	// ParentTask is the original epic task that was planned
	ParentTask *Task

	// Subtasks are the sequential subtasks derived from the planning phase
	Subtasks []PlannedSubtask

	// TotalEffort is the estimated total effort (if provided by the planner)
	TotalEffort string

	// PlanOutput is the raw Claude Code output for reference
	PlanOutput string
}

// PlannedSubtask represents a single subtask derived from epic planning.
type PlannedSubtask struct {
	// Title is the short title of the subtask
	Title string

	// Description is the detailed description of what needs to be done
	Description string

	// Order is the execution order (1-indexed)
	Order int

	// DependsOn contains the orders of subtasks this depends on
	DependsOn []int
}

// numberedListRegex matches numbered patterns: "1. ", "1) ", "Step 1:", "Phase 1:", etc.
var numberedListRegex = regexp.MustCompile(`(?mi)^(?:\s*)(?:step|phase|task)?\s*(\d+)[.):]\s*(.+)`)

// PlanEpic runs Claude Code in planning mode to break an epic into subtasks.
// Returns an EpicPlan with 3-5 sequential subtasks.
func (r *Runner) PlanEpic(ctx context.Context, task *Task) (*EpicPlan, error) {
	// Build planning prompt
	prompt := buildPlanningPrompt(task)

	// Get claude command from config or use default
	claudeCmd := "claude"
	if r.config != nil && r.config.ClaudeCode != nil && r.config.ClaudeCode.Command != "" {
		claudeCmd = r.config.ClaudeCode.Command
	}

	// Run Claude Code with --print flag for planning
	args := []string{"--print", "-p", prompt}

	cmd := exec.CommandContext(ctx, claudeCmd, args...)

	// Set working directory if specified
	if task.ProjectPath != "" {
		cmd.Dir = task.ProjectPath
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	r.log.Debug("Running Claude Code planning",
		"task_id", task.ID,
		"command", claudeCmd,
		"args", args,
	)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude planning failed: %w (stderr: %s)", err, stderr.String())
	}

	output := stdout.String()
	if output == "" {
		return nil, fmt.Errorf("claude planning returned empty output")
	}

	// Parse subtasks from output
	subtasks := parseSubtasks(output)
	if len(subtasks) == 0 {
		return nil, fmt.Errorf("no subtasks found in planning output")
	}

	return &EpicPlan{
		ParentTask: task,
		Subtasks:   subtasks,
		PlanOutput: output,
	}, nil
}

// buildPlanningPrompt creates the prompt for epic planning.
func buildPlanningPrompt(task *Task) string {
	var sb strings.Builder

	sb.WriteString("You are a software architect planning an implementation.\n\n")
	sb.WriteString("Break down this epic task into 3-5 sequential subtasks that can each be completed independently.\n")
	sb.WriteString("Each subtask should be a concrete, implementable unit of work.\n\n")

	sb.WriteString("## Task to Plan\n\n")
	sb.WriteString(fmt.Sprintf("**Title:** %s\n\n", task.Title))
	if task.Description != "" {
		sb.WriteString(fmt.Sprintf("**Description:**\n%s\n\n", task.Description))
	}

	sb.WriteString("## Output Format\n\n")
	sb.WriteString("List each subtask with a number, title, and description:\n\n")
	sb.WriteString("1. **Subtask title** - Description of what needs to be done\n")
	sb.WriteString("2. **Next subtask** - Its description\n")
	sb.WriteString("...\n\n")

	sb.WriteString("Focus on:\n")
	sb.WriteString("- Clear boundaries between subtasks\n")
	sb.WriteString("- Logical ordering (dependencies flow naturally)\n")
	sb.WriteString("- Each subtask should be testable/verifiable\n")
	sb.WriteString("- Include any setup/infrastructure subtasks first\n")

	return sb.String()
}

// parseSubtasks extracts subtasks from Claude's planning output.
// Looks for numbered patterns: "1. Title - Description" or "Step 1: Title"
func parseSubtasks(output string) []PlannedSubtask {
	var subtasks []PlannedSubtask
	seenOrders := make(map[int]bool)

	scanner := bufio.NewScanner(strings.NewReader(output))
	var currentSubtask *PlannedSubtask
	var descriptionLines []string

	for scanner.Scan() {
		line := scanner.Text()

		// Try to match numbered list patterns
		matches := numberedListRegex.FindStringSubmatch(line)
		if len(matches) >= 3 {
			// Save previous subtask if exists
			if currentSubtask != nil {
				finalizeSubtask(currentSubtask, descriptionLines)
				if currentSubtask.Title != "" && !seenOrders[currentSubtask.Order] {
					subtasks = append(subtasks, *currentSubtask)
					seenOrders[currentSubtask.Order] = true
				}
			}

			order := 0
			_, _ = fmt.Sscanf(matches[1], "%d", &order)

			// Extract title and possibly inline description
			titleAndDesc := strings.TrimSpace(matches[2])
			title, desc := splitTitleDescription(titleAndDesc)

			currentSubtask = &PlannedSubtask{
				Title:       title,
				Description: desc,
				Order:       order,
			}
			descriptionLines = nil
			continue
		}

		// Accumulate description lines for current subtask
		if currentSubtask != nil && strings.TrimSpace(line) != "" {
			// Skip markdown headers that might be formatting
			if !strings.HasPrefix(strings.TrimSpace(line), "#") {
				descriptionLines = append(descriptionLines, strings.TrimSpace(line))
			}
		}
	}

	// Save last subtask
	if currentSubtask != nil {
		finalizeSubtask(currentSubtask, descriptionLines)
		if currentSubtask.Title != "" && !seenOrders[currentSubtask.Order] {
			subtasks = append(subtasks, *currentSubtask)
		}
	}

	return subtasks
}

// splitTitleDescription splits "**Title** - Description" or "Title: Description" patterns.
func splitTitleDescription(s string) (title, description string) {
	// Remove markdown bold markers
	s = strings.ReplaceAll(s, "**", "")

	// Try common separators
	separators := []string{" - ", ": ", " – "}
	for _, sep := range separators {
		if idx := strings.Index(s, sep); idx > 0 {
			return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+len(sep):])
		}
	}

	// No separator found, entire string is title
	return strings.TrimSpace(s), ""
}

// finalizeSubtask combines inline description with accumulated description lines.
func finalizeSubtask(subtask *PlannedSubtask, lines []string) {
	if len(lines) == 0 {
		return
	}

	accumulated := strings.TrimSpace(strings.Join(lines, "\n"))
	if subtask.Description == "" {
		subtask.Description = accumulated
	} else {
		// Prepend inline description to accumulated lines
		subtask.Description = subtask.Description + "\n" + accumulated
	}
}
