package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// syncMainBranch syncs the local main branch with origin/main after task completion.
// GH-1018: This prevents local/remote divergence over time when multiple PRs are merged.
//
// Strategy:
// 1. Fetch origin/main to get latest remote state
// 2. If on main branch, reset --hard to origin/main
// 3. If on feature branch, skip reset (don't disrupt worktree)
//
// This is opt-in via executor.sync_main_after_task config.
func (r *Runner) syncMainBranch(ctx context.Context, repoPath string) error {
	log := r.log.With(slog.String("repo", repoPath))
	log.Debug("Syncing main branch with origin")

	// Fetch latest from origin
	fetchCmd := exec.CommandContext(ctx, "git", "fetch", "origin", "main")
	fetchCmd.Dir = repoPath
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch origin/main: %w: %s", err, output)
	}

	// Check current branch
	branchCmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	branchCmd.Dir = repoPath
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}

	currentBranch := strings.TrimSpace(string(branchOutput))

	// Only reset if on main branch (don't disrupt feature branches or worktrees)
	if currentBranch == "main" || currentBranch == "master" {
		resetCmd := exec.CommandContext(ctx, "git", "reset", "--hard", "origin/main")
		resetCmd.Dir = repoPath
		if output, err := resetCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to reset main to origin/main: %w: %s", err, output)
		}
		log.Info("Synced main branch with origin/main")
	} else {
		log.Debug("Not on main branch, skipping reset", slog.String("branch", currentBranch))
	}

	return nil
}

// syncNavigatorIndex updates DEVELOPMENT-README.md after task completion.
// It moves completed tasks from "In Progress" to "Completed" section.
// Supports both TASK-XX and GH-XX formats.
func (r *Runner) syncNavigatorIndex(task *Task, status string, executionPath string) error {
	indexPath := filepath.Join(executionPath, ".agent", "DEVELOPMENT-README.md")

	// Check if Navigator index exists
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		r.log.Debug("Navigator index not found, skipping sync",
			slog.String("task_id", task.ID),
			slog.String("path", indexPath),
		)
		return nil
	}

	// Read current index
	content, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("failed to read navigator index: %w", err)
	}

	lines := strings.Split(string(content), "\n")
	var result []string
	var taskEntry string
	var taskTitle string
	inProgressSection := false
	completedSection := false
	completedInsertIdx := -1

	// Extract task number for matching (handles GH-XX and TASK-XX)
	taskNum := extractTaskNumber(task.ID)

	for i, line := range lines {
		// Track sections
		if strings.Contains(line, "### In Progress") {
			inProgressSection = true
			completedSection = false
			result = append(result, line)
			continue
		}
		if strings.Contains(line, "### Backlog") || strings.Contains(line, "## Completed") {
			inProgressSection = false
		}
		if strings.Contains(line, "## Completed") {
			completedSection = true
			result = append(result, line)
			// Find where to insert (after the header and any date line)
			completedInsertIdx = len(result)
			continue
		}
		if completedSection && strings.HasPrefix(strings.TrimSpace(line), "| Item") {
			// After table header in completed section
			result = append(result, line)
			completedInsertIdx = len(result)
			continue
		}
		if completedSection && strings.HasPrefix(strings.TrimSpace(line), "|---") {
			result = append(result, line)
			completedInsertIdx = len(result)
			continue
		}

		// Check if this line contains our task in the In Progress table
		if inProgressSection && strings.Contains(line, "|") {
			// Table row format: | GH# | Title | Status |
			// or: | 54 | Speed Optimization ... | 🔄 Pilot executing |
			if strings.Contains(line, task.ID) || (taskNum != "" && containsTaskNumber(line, taskNum)) {
				// Extract title from the row
				parts := strings.Split(line, "|")
				if len(parts) >= 3 {
					taskTitle = strings.TrimSpace(parts[2]) // Title is second column after GH#
				}
				taskEntry = line
				// Skip this line (don't add to result) - we'll move it to completed
				continue
			}
		}

		result = append(result, line)
		_ = i // suppress unused warning
	}

	// If we found a task entry to move
	if taskEntry != "" && completedInsertIdx >= 0 {
		// Create completed entry
		completedEntry := fmt.Sprintf("| %s | %s |", task.ID, taskTitle)

		// Insert at the right position
		newResult := make([]string, 0, len(result)+1)
		newResult = append(newResult, result[:completedInsertIdx]...)
		newResult = append(newResult, completedEntry)
		newResult = append(newResult, result[completedInsertIdx:]...)
		result = newResult

		// Write updated index
		if err := os.WriteFile(indexPath, []byte(strings.Join(result, "\n")), 0644); err != nil {
			return fmt.Errorf("failed to write navigator index: %w", err)
		}

		r.log.Info("Updated Navigator index",
			slog.String("task_id", task.ID),
			slog.String("status", status),
			slog.String("moved_to", "Completed"),
		)
	} else if taskEntry != "" {
		r.log.Debug("Task found but no Completed section to move to",
			slog.String("task_id", task.ID),
		)
	} else {
		r.log.Debug("Task not found in Navigator index In Progress section",
			slog.String("task_id", task.ID),
		)
	}

	return nil
}

// maybeInitNavigator initializes Navigator structure if not present.
// This enables auto-init for projects that don't have .agent/ yet.
func (r *Runner) maybeInitNavigator(projectPath string) error {
	initializer, err := NewNavigatorInitializer(r.log)
	if err != nil {
		return fmt.Errorf("failed to create navigator initializer: %w", err)
	}

	if initializer.IsInitialized(projectPath) {
		return nil
	}

	r.log.Info("Auto-initializing Navigator for project",
		slog.String("path", projectPath),
	)

	return initializer.Initialize(projectPath)
}

// extractTaskNumber extracts the numeric part from task IDs like "GH-57" or "TASK-123"
func extractTaskNumber(taskID string) string {
	// Handle GH-XX format
	if strings.HasPrefix(taskID, "GH-") {
		return strings.TrimPrefix(taskID, "GH-")
	}
	// Handle TASK-XX format
	if strings.HasPrefix(taskID, "TASK-") {
		return strings.TrimPrefix(taskID, "TASK-")
	}
	return taskID
}

// containsTaskNumber checks if a line contains a task number in various formats
func containsTaskNumber(line, taskNum string) bool {
	// Check for "| 57 |" or "| GH-57 |" or "| TASK-57 |" patterns
	patterns := []string{
		fmt.Sprintf("| %s ", taskNum),
		fmt.Sprintf("|%s ", taskNum),
		fmt.Sprintf("| %s|", taskNum),
		fmt.Sprintf("|%s|", taskNum),
		fmt.Sprintf("GH-%s", taskNum),
		fmt.Sprintf("TASK-%s", taskNum),
	}
	for _, p := range patterns {
		if strings.Contains(line, p) {
			return true
		}
	}
	return false
}

// ExtractRepoName extracts the repository name from "owner/repo" format.
// Returns just the repo part (e.g., "pilot" from "alekspetrov/pilot").
func ExtractRepoName(repo string) string {
	parts := strings.Split(repo, "/")
	if len(parts) == 2 {
		return parts[1]
	}
	return repo
}

// ValidateRepoProjectMatch validates that a source repo matches the project path.
// This is a defense against cross-project execution (GH-386).
// Returns an error if there's a mismatch between repo name and project directory name.
func ValidateRepoProjectMatch(sourceRepo, projectPath string) error {
	if sourceRepo == "" || projectPath == "" {
		return nil // Nothing to validate
	}

	repoName := ExtractRepoName(sourceRepo)
	projectDir := filepath.Base(projectPath)

	// Normalize for comparison (case-insensitive)
	repoName = strings.ToLower(repoName)
	projectDir = strings.ToLower(projectDir)

	if repoName != projectDir {
		return fmt.Errorf(
			"repo/project mismatch: issue from '%s' but executing in '%s' (expected project directory '%s')",
			sourceRepo, projectPath, repoName,
		)
	}

	return nil
}
