package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// syncMainBranch syncs the local main branch with origin after task completion.
// GH-1018: prevents local/remote divergence over time when multiple PRs are merged.
//
// Strategy:
//  1. Detect current branch first (so we can target origin/<branch>, not hardcode main)
//  2. If on a feature branch, skip (don't disrupt worktrees)
//  3. Fetch origin/<branch>
//  4. git merge --ff-only origin/<branch>  ← non-destructive
//
// GH-3018: previously used `git reset --hard origin/main`. That silently
// destroyed local commits when GitHub push-propagation lagged behind the
// immediately-following fetch+reset. `merge --ff-only` is a no-op when
// local is ahead/diverged, and a safe fast-forward when local is behind.
//
// This is opt-in via executor.sync_main_after_task config for the post-task
// call site (runner.go). The epic.go:1498 call site fires unconditionally
// during sequential epic execution — both are now safe.
func (r *Runner) syncMainBranch(ctx context.Context, repoPath string) error {
	log := r.log.With(slog.String("repo", repoPath))
	log.Debug("Syncing main branch with origin")

	// Detect current branch first so we target the correct remote ref.
	branchCmd := gitCommandIn(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	branchOutput, err := branchCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	currentBranch := strings.TrimSpace(string(branchOutput))

	// Only sync on main/master. Feature branches and worktrees are off-limits.
	if currentBranch != "main" && currentBranch != "master" {
		log.Debug("Not on main branch, skipping sync", slog.String("branch", currentBranch))
		return nil
	}

	remoteRef := "origin/" + currentBranch

	// Fetch the matching remote ref.
	fetchCmd := gitCommandIn(ctx, repoPath, "fetch", "origin", currentBranch)
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch %s: %w: %s", remoteRef, err, output)
	}

	// GH-3018: --ff-only prevents silent commit loss when local is ahead of origin.
	// Fails safely on: (a) local ahead, (b) divergence, (c) dirty working tree.
	mergeCmd := gitCommandIn(ctx, repoPath, "merge", "--ff-only", remoteRef)
	output, mergeErr := mergeCmd.CombinedOutput()
	if mergeErr != nil {
		// All failure modes are safe to skip. Raw git output lets operators
		// distinguish local-ahead vs. divergence vs. dirty tree.
		log.Warn("Skipped main sync — fast-forward not possible (non-fatal)",
			slog.String("branch", currentBranch),
			slog.String("remote_ref", remoteRef),
			slog.String("git_output", strings.TrimSpace(string(output))),
			slog.String("hint", "To sync manually: cd <repo> && git fetch && git merge --ff-only "+remoteRef),
		)
		return nil
	}

	log.Info("Synced main branch with origin", slog.String("branch", currentBranch))
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

// updateNavigatorDocsAfterTask updates the Navigator files of the workspace
// after a successful task: the index (GH-57), the archive of the task
// document (GH-1063), the feature matrix (GH-1388) and the completion marker
// (GH-1064).
//
// TASK-651: with run_as_user set it writes nothing and logs one Info line.
// The steps run after the PR is created: the session is over, the commit is
// pushed, and the agent workspace is removed after the run, so only the
// daemon would read them.
func (r *Runner) updateNavigatorDocsAfterTask(log *slog.Logger, task *Task, result *ExecutionResult, modifiedFiles []string, executionPath string) {
	if runAsSettingsNow() != nil {
		log.Info("Navigator files after the task skipped: run_as_user is set and the agent workspace is removed after the run",
			slog.String("task_id", task.ID),
			slog.String("skipped", "index sync, task doc archive, feature matrix, context marker"),
		)
		return
	}

	// Sync Navigator index (GH-57) - update DEVELOPMENT-README.md
	if syncErr := r.syncNavigatorIndex(task, "completed", executionPath); syncErr != nil {
		log.Warn("Failed to sync Navigator index", slog.Any("error", syncErr))
	}

	// GH-1063: Archive completed task documentation
	agentPath := filepath.Join(executionPath, ".agent")
	if archiveErr := ArchiveTaskDoc(agentPath, task.ID); archiveErr != nil {
		log.Warn("Failed to archive task documentation", slog.Any("error", archiveErr))
	}

	// GH-1388: Update feature matrix for feature tasks
	if strings.HasPrefix(strings.ToLower(task.Title), "feat(") {
		ver := "unknown"
		if r.config != nil && r.config.Version != "" {
			ver = r.config.Version
		}
		if fmErr := UpdateFeatureMatrix(agentPath, task, ver); fmErr != nil {
			log.Warn("Failed to update feature matrix", slog.Any("error", fmErr))
		}
	}

	// GH-1064: Create context marker for completed task
	marker := &ContextMarker{
		Name:        fmt.Sprintf("task-completed-%s", task.ID),
		Description: fmt.Sprintf("Task completed: %s", task.Title),
		TaskID:      task.ID,
		CurrentFocus: fmt.Sprintf("Completed %s. %d files changed, %d lines added, %d removed. Cost: $%.2f.",
			task.Title, result.FilesChanged, result.LinesAdded, result.LinesRemoved,
			result.EstimatedCostUSD),
	}

	// Add modified files list (GH-1388)
	if len(modifiedFiles) > 0 {
		marker.CurrentFocus += fmt.Sprintf(" Modified: %s.", strings.Join(modifiedFiles, ", "))
	}

	// Add commit SHA and PR info if available
	if result.CommitSHA != "" {
		marker.Commits = append(marker.Commits, result.CommitSHA)
	}
	if result.PRUrl != "" {
		marker.CurrentFocus += fmt.Sprintf(" PR: %s", result.PRUrl)
	}

	if createMarkerErr := CreateMarker(agentPath, marker); createMarkerErr != nil {
		log.Warn("Failed to create completion marker", slog.Any("error", createMarkerErr))
	} else {
		log.Debug("Created completion context marker", slog.String("marker_path", marker.FilePath))
	}
}

// maybeInitNavigator initializes Navigator structure if not present.
// This enables auto-init for projects that don't have .agent/ yet.
// With navigator.bootstrap=auto it then runs the one-time bootstrap session
// while the README still carries a placeholder section.
//
// executionPath is where the scaffold lives (a worktree when isolation is
// active); projectPath is the registered clone path the config knows, used for
// the per-project `navigator: false` lookup (GH-5739).
func (r *Runner) maybeInitNavigator(ctx context.Context, executionPath, projectPath string) error {
	initializer, err := NewNavigatorInitializer(r.log)
	if err != nil {
		return fmt.Errorf("failed to create navigator initializer: %w", err)
	}

	if !initializer.IsInitialized(executionPath) {
		r.log.Info("Auto-initializing Navigator for project",
			slog.String("path", executionPath),
		)
		if err := initializer.Initialize(executionPath); err != nil {
			return err
		}
	}

	r.maybeBootstrapNavigator(ctx, executionPath, projectPath)
	return nil
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
// Returns just the repo part (e.g., "pilot" from "qf-studio/pilot").
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
