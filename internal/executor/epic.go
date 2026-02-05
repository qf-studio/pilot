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

// CreatedIssue represents a GitHub issue created from a planned subtask.
type CreatedIssue struct {
	// Number is the GitHub issue number
	Number int

	// URL is the full GitHub issue URL
	URL string

	// Subtask is the planned subtask this issue was created from
	Subtask PlannedSubtask
}

// numberedListRegex matches numbered patterns: "1. ", "1) ", "Step 1:", "Phase 1:", "**1.", etc.
// Allows optional markdown bold markers (**) before the number.
// Used by parseSubtasks to extract numbered items from planning output.
var numberedListRegex = regexp.MustCompile(`(?mi)^(?:\s*)(?:\*{0,2})(?:step|phase|task)?\s*(\d+)[.):]\s*(.+)`)

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

	// Parse subtasks from output (Haiku API with regex fallback)
	subtasks := parseSubtasksWithFallback(r.subtaskParser, output)
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

// issueNumberRegex extracts the issue number from a GitHub issue URL.
// Matches patterns like: https://github.com/owner/repo/issues/123
var issueNumberRegex = regexp.MustCompile(`/issues/(\d+)`)

// parseIssueNumber extracts the issue number from a GitHub issue URL.
// Returns 0 if no issue number is found.
func parseIssueNumber(url string) int {
	matches := issueNumberRegex.FindStringSubmatch(url)
	if len(matches) < 2 {
		return 0
	}
	var num int
	_, _ = fmt.Sscanf(matches[1], "%d", &num)
	return num
}

// CreateSubIssues creates GitHub issues from the planned subtasks.
// Returns a slice of CreatedIssue with the issue numbers and URLs.
func (r *Runner) CreateSubIssues(ctx context.Context, plan *EpicPlan) ([]CreatedIssue, error) {
	if plan == nil || len(plan.Subtasks) == 0 {
		return nil, fmt.Errorf("plan has no subtasks to create issues from")
	}

	var created []CreatedIssue

	for _, subtask := range plan.Subtasks {
		// Build the issue body
		body := subtask.Description
		if plan.ParentTask != nil && plan.ParentTask.ID != "" {
			body = fmt.Sprintf("Parent: %s\n\n%s", plan.ParentTask.ID, body)
		}

		// Create issue using gh CLI
		args := []string{
			"issue", "create",
			"--title", subtask.Title,
			"--body", body,
			"--label", "pilot",
		}

		cmd := exec.CommandContext(ctx, "gh", args...)

		// Set working directory if parent task has a project path
		if plan.ParentTask != nil && plan.ParentTask.ProjectPath != "" {
			cmd.Dir = plan.ParentTask.ProjectPath
		}

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		r.log.Debug("Creating GitHub issue",
			"subtask_order", subtask.Order,
			"title", subtask.Title,
		)

		if err := cmd.Run(); err != nil {
			return created, fmt.Errorf("failed to create issue for subtask %d: %w (stderr: %s)",
				subtask.Order, err, stderr.String())
		}

		// gh issue create outputs the issue URL on success
		issueURL := strings.TrimSpace(stdout.String())
		issueNumber := parseIssueNumber(issueURL)

		created = append(created, CreatedIssue{
			Number:  issueNumber,
			URL:     issueURL,
			Subtask: subtask,
		})

		r.log.Info("Created GitHub issue",
			"subtask_order", subtask.Order,
			"issue_number", issueNumber,
			"url", issueURL,
		)
	}

	return created, nil
}

// UpdateIssueProgress adds a progress comment to an issue.
func (r *Runner) UpdateIssueProgress(ctx context.Context, projectPath string, issueID string, message string) error {
	args := []string{"issue", "comment", issueID, "--body", message}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if projectPath != "" {
		cmd.Dir = projectPath
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to comment on issue %s: %w (stderr: %s)", issueID, err, stderr.String())
	}
	return nil
}

// CloseIssueWithComment closes an issue with a completion comment.
func (r *Runner) CloseIssueWithComment(ctx context.Context, projectPath string, issueID string, comment string) error {
	args := []string{"issue", "close", issueID, "--comment", comment}
	cmd := exec.CommandContext(ctx, "gh", args...)
	if projectPath != "" {
		cmd.Dir = projectPath
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to close issue %s: %w (stderr: %s)", issueID, err, stderr.String())
	}
	return nil
}

// ExecuteSubIssues executes created sub-issues sequentially and tracks progress on the parent.
// Each sub-issue is executed as a separate task, and the parent issue is updated with progress.
// Returns an error if any sub-issue fails; completed sub-issues remain done.
func (r *Runner) ExecuteSubIssues(ctx context.Context, parent *Task, issues []CreatedIssue) error {
	if len(issues) == 0 {
		return fmt.Errorf("no sub-issues to execute")
	}

	total := len(issues)
	projectPath := ""
	if parent != nil {
		projectPath = parent.ProjectPath
	}

	r.log.Info("Starting sequential sub-issue execution",
		"parent_id", parent.ID,
		"total_issues", total,
	)

	// Update parent with start message
	startMsg := fmt.Sprintf("🚀 Starting sequential execution of %d sub-issues", total)
	if err := r.UpdateIssueProgress(ctx, projectPath, parent.ID, startMsg); err != nil {
		r.log.Warn("Failed to update parent progress", "error", err)
		// Non-fatal, continue execution
	}

	for i, issue := range issues {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("execution cancelled: %w", ctx.Err())
		default:
		}

		// Update parent with current progress
		progressMsg := fmt.Sprintf("⏳ Progress: %d/%d - Starting: **%s** (#%d)",
			i, total, issue.Subtask.Title, issue.Number)
		if err := r.UpdateIssueProgress(ctx, projectPath, parent.ID, progressMsg); err != nil {
			r.log.Warn("Failed to update parent progress", "error", err)
		}

		// Create task from sub-issue
		subTask := &Task{
			ID:          fmt.Sprintf("GH-%d", issue.Number),
			Title:       issue.Subtask.Title,
			Description: issue.Subtask.Description,
			ProjectPath: projectPath,
			Branch:      fmt.Sprintf("pilot/GH-%d", issue.Number),
			CreatePR:    true,
		}

		r.log.Info("Executing sub-issue",
			"parent_id", parent.ID,
			"sub_issue", issue.Number,
			"order", i+1,
			"total", total,
		)

		// Execute the sub-task
		result, err := r.Execute(ctx, subTask)
		if err != nil {
			failMsg := fmt.Sprintf("❌ Failed on %d/%d: %s - Error: %v",
				i+1, total, issue.Subtask.Title, err)
			_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, failMsg)
			return fmt.Errorf("sub-issue %d failed: %w", issue.Number, err)
		}

		if !result.Success {
			failMsg := fmt.Sprintf("❌ Failed on %d/%d: %s - %s",
				i+1, total, issue.Subtask.Title, result.Error)
			_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, failMsg)
			return fmt.Errorf("sub-issue %d failed: %s", issue.Number, result.Error)
		}

		// Close completed sub-issue
		closeComment := fmt.Sprintf("✅ Completed as part of %s", parent.ID)
		if result.PRUrl != "" {
			closeComment = fmt.Sprintf("✅ Completed as part of %s\nPR: %s", parent.ID, result.PRUrl)
		}
		if err := r.CloseIssueWithComment(ctx, projectPath, fmt.Sprintf("%d", issue.Number), closeComment); err != nil {
			r.log.Warn("Failed to close sub-issue", "issue", issue.Number, "error", err)
			// Non-fatal, continue
		}

		r.log.Info("Sub-issue completed",
			"parent_id", parent.ID,
			"sub_issue", issue.Number,
			"pr_url", result.PRUrl,
		)
	}

	// All done - update and close parent
	completeMsg := fmt.Sprintf("✅ Completed: %d/%d sub-issues done\n\nAll sub-tasks executed successfully.", total, total)
	_ = r.UpdateIssueProgress(ctx, projectPath, parent.ID, completeMsg)

	if err := r.CloseIssueWithComment(ctx, projectPath, parent.ID, "All sub-issues completed successfully."); err != nil {
		r.log.Warn("Failed to close parent issue", "error", err)
		// Non-fatal
	}

	r.log.Info("Epic execution completed",
		"parent_id", parent.ID,
		"total_completed", total,
	)

	return nil
}
