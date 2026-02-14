package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
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
// Allows optional markdown bold markers (**) before the number (GH-490 fix).
// Also handles markdown heading prefixes (### 1.), dash/asterisk bullets (- 1., * 1.),
// and combinations like "- **1. Title**" or "### Step 1: Title" (GH-542 fix).
// Used by parseSubtasks as the regex fallback in the parsing pipeline:
//
//	PlanEpic → parseSubtasksWithFallback → SubtaskParser (Haiku API) → parseSubtasks (regex)
var numberedListRegex = regexp.MustCompile(`(?mi)^(?:\s*)(?:#{1,6}\s+)?(?:[-*]\s+)?(?:\*{0,2})(?:step|phase|task)?\s*(\d+)[.):]\s*(.+)`)

// PlanEpic runs Claude Code in planning mode to break an epic into subtasks.
// Returns an EpicPlan with 3-5 sequential subtasks.
// executionPath may differ from task.ProjectPath when using worktree isolation (GH-968).
func (r *Runner) PlanEpic(ctx context.Context, task *Task, executionPath string) (*EpicPlan, error) {
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

	// Set working directory - use executionPath which respects worktree isolation
	if executionPath != "" {
		cmd.Dir = executionPath
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

	// Parse subtasks: tries Haiku structured extraction first, falls back to regex.
	// See parseSubtasksWithFallback in subtask_parser.go for the fallback chain.
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

	sb.WriteString("## CRITICAL: Avoid Single-Package Splits\n\n")
	sb.WriteString("If all the work lives in one package or directory (e.g., all files in `cmd/pilot/`),\n")
	sb.WriteString("DO NOT split into separate subtasks. Instead, return a SINGLE subtask with the full scope.\n")
	sb.WriteString("Splitting work within the same package causes merge conflicts when subtasks execute in parallel.\n")
	sb.WriteString("Only split when subtasks genuinely touch DIFFERENT packages or directories.\n\n")

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
	sb.WriteString("- NEVER split work that belongs to the same Go package or directory into separate subtasks\n")

	return sb.String()
}

// consolidateEpicPlan merges the original task description with the planned subtasks
// into a single description for non-decomposed execution. The executor gets the full
// implementation plan but executes it as one unit on one branch.
func consolidateEpicPlan(originalDesc string, subtasks []PlannedSubtask) string {
	var sb strings.Builder
	sb.WriteString(originalDesc)
	sb.WriteString("\n\n## Planned Steps (execute all in sequence)\n\n")
	for _, st := range subtasks {
		sb.WriteString(fmt.Sprintf("%d. **%s**", st.Order, st.Title))
		if st.Description != "" {
			sb.WriteString(" — ")
			sb.WriteString(st.Description)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// isSinglePackageScope checks whether all planned subtasks reference files within
// the same Go package or directory. When true, creating separate GitHub issues
// would cause merge conflicts because each sub-issue branches from main independently.
//
// Detection strategy:
// 1. Extract all file paths mentioned across subtask titles and descriptions
// 2. Compute unique parent directories
// 3. If only 1 directory (or 0 files found), consider it single-package scope
//
// GH-1265: This prevents the "serial conflict cascade" bug where N sub-issues
// all touching cmd/pilot/ create N branches from main, each redeclaring shared types.
func isSinglePackageScope(subtasks []PlannedSubtask, taskDescription string) bool {
	// Collect all text to scan for file references
	var allText strings.Builder
	allText.WriteString(taskDescription)
	allText.WriteString("\n")
	for _, st := range subtasks {
		allText.WriteString(st.Title)
		allText.WriteString("\n")
		allText.WriteString(st.Description)
		allText.WriteString("\n")
	}

	dirs := extractUniqueDirectories(allText.String())

	// If we found file references and they all point to 1 directory → single package
	if len(dirs) == 1 {
		return true
	}

	// If no file references found, use heuristic: check if subtask titles suggest
	// the same component (e.g., all mention "onboard", "dashboard", "config")
	if len(dirs) == 0 {
		return detectSameComponentFromTitles(subtasks)
	}

	return false
}

// extractUniqueDirectories finds file paths in text and returns their unique parent directories.
func extractUniqueDirectories(text string) map[string]bool {
	// Reuse the existing file path pattern from the decomposer
	filePattern := regexp.MustCompile(`\b((?:[\w\-]+/)+[\w\-]+\.(?:go|py|ts|tsx|js|jsx|rs|java|rb))\b`)
	matches := filePattern.FindAllStringSubmatch(text, -1)

	dirs := make(map[string]bool)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		filePath := m[1]
		// Extract directory: "cmd/pilot/onboard.go" → "cmd/pilot"
		lastSlash := strings.LastIndex(filePath, "/")
		if lastSlash > 0 {
			dirs[filePath[:lastSlash]] = true
		}
	}
	return dirs
}

// detectSameComponentFromTitles checks if subtask titles all reference the same component.
// Uses a simple heuristic: extract the most common significant word from titles.
// If one word appears in >80% of subtask titles, it's likely single-scope.
func detectSameComponentFromTitles(subtasks []PlannedSubtask) bool {
	if len(subtasks) < 2 {
		return false
	}

	// Count word frequency across titles
	wordCounts := make(map[string]int)
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "for": true,
		"to": true, "in": true, "of": true, "with": true, "from": true, "by": true,
		"add": true, "create": true, "implement": true, "update": true, "fix": true,
		"setup": true, "set": true, "up": true, "new": true, "test": true, "tests": true,
	}

	for _, st := range subtasks {
		words := strings.Fields(strings.ToLower(st.Title))
		seen := make(map[string]bool) // dedupe within a single title
		for _, w := range words {
			w = strings.Trim(w, ".,:-()[]\"'`*")
			if len(w) < 3 || stopWords[w] {
				continue
			}
			if !seen[w] {
				wordCounts[w]++
				seen[w] = true
			}
		}
	}

	// Check if any significant word appears in >80% of titles
	threshold := int(float64(len(subtasks)) * 0.8)
	for _, count := range wordCounts {
		if count >= threshold {
			return true
		}
	}

	return false
}

// parseSubtasks extracts subtasks from Claude's planning output using regex.
// This is the fallback parser when Haiku API is unavailable (see subtask_parser.go).
// Looks for numbered patterns: "1. Title - Description", "Step 1: Title", "**1. Title**"
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

	// Try common separators (em-dash first since Claude often uses it)
	separators := []string{" — ", " - ", ": ", " – "}
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

// parsePRNumberFromURL extracts a PR number from a GitHub PR URL.
// Returns 0 if the URL doesn't contain a valid PR number.
func parsePRNumberFromURL(url string) int {
	// Match /pull/123 at the end of the URL
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return 0
	}
	numStr := strings.TrimSpace(url[idx+len("/pull/"):])
	// Strip any trailing path segments
	if slashIdx := strings.Index(numStr, "/"); slashIdx >= 0 {
		numStr = numStr[:slashIdx]
	}
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0
	}
	return n
}

// CreateSubIssues creates GitHub issues from the planned subtasks.
// Returns a slice of CreatedIssue with the issue numbers and URLs.
// executionPath may differ from task.ProjectPath when using worktree isolation (GH-968).
func (r *Runner) CreateSubIssues(ctx context.Context, plan *EpicPlan, executionPath string) ([]CreatedIssue, error) {
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

		// Truncate title to max 80 chars for GitHub issue limits (GH-1133)
		title := truncateTitle(subtask.Title, 80)

		// Create issue using gh CLI
		args := []string{
			"issue", "create",
			"--title", title,
			"--body", body,
			"--label", "pilot",
		}

		cmd := exec.CommandContext(ctx, "gh", args...)

		// Set working directory - use executionPath which respects worktree isolation
		if executionPath != "" {
			cmd.Dir = executionPath
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
// executionPath may differ from task.ProjectPath when using worktree isolation (GH-968).
func (r *Runner) ExecuteSubIssues(ctx context.Context, parent *Task, issues []CreatedIssue, executionPath string) error {
	if len(issues) == 0 {
		return fmt.Errorf("no sub-issues to execute")
	}

	total := len(issues)
	// Use executionPath for gh CLI commands (respects worktree isolation)
	projectPath := executionPath
	if projectPath == "" && parent != nil {
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

		// Execute the sub-task (use override if set, for testing)
		// GH-948: Use executeWithOptions to prevent recursive worktree creation
		var result *ExecutionResult
		var err error
		if r.executeFunc != nil {
			// Use test override function if set
			result, err = r.executeFunc(ctx, subTask)
		} else {
			// Use internal method to prevent recursive worktree creation
			result, err = r.executeWithOptions(ctx, subTask, false)
		}
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

		// Register sub-issue PR with autopilot controller (GH-596)
		if result.PRUrl != "" && r.onSubIssuePRCreated != nil {
			if prNum := parsePRNumberFromURL(result.PRUrl); prNum > 0 {
				r.onSubIssuePRCreated(prNum, result.PRUrl, issue.Number, result.CommitSHA, subTask.Branch)
			} else {
				r.log.Warn("Failed to extract PR number from sub-issue PR URL",
					"pr_url", result.PRUrl)
			}
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
