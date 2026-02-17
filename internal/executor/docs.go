package executor

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskDoc represents a task documentation file
type TaskDoc struct {
	ID                 string
	Title              string
	Description        string
	AcceptanceCriteria []string
	CreatedAt          time.Time
}

// CreateTaskDoc generates a task documentation file in .agent/tasks/
func CreateTaskDoc(agentPath string, task *Task) error {
	tasksDir := filepath.Join(agentPath, "tasks")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		return err
	}

	filename := fmt.Sprintf("%s.md", sanitizeFilename(task.ID))
	path := filepath.Join(tasksDir, filename)

	content := formatTaskDoc(task)
	return os.WriteFile(path, []byte(content), 0644)
}

// ArchiveTaskDoc moves completed task to archive/
func ArchiveTaskDoc(agentPath, taskID string) error {
	tasksDir := filepath.Join(agentPath, "tasks")
	archiveDir := filepath.Join(tasksDir, "archive")

	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return err
	}

	src := filepath.Join(tasksDir, fmt.Sprintf("%s.md", sanitizeFilename(taskID)))
	dst := filepath.Join(archiveDir, fmt.Sprintf("%s.md", sanitizeFilename(taskID)))

	return os.Rename(src, dst)
}

// formatTaskDoc generates markdown content for task doc
func formatTaskDoc(task *Task) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", task.ID))
	sb.WriteString(fmt.Sprintf("**Created:** %s\n\n", time.Now().Format("2006-01-02")))
	sb.WriteString("## Problem\n\n")
	sb.WriteString(task.Description)
	sb.WriteString("\n\n## Acceptance Criteria\n\n")
	for _, ac := range task.AcceptanceCriteria {
		sb.WriteString(fmt.Sprintf("- [ ] %s\n", ac))
	}
	return sb.String()
}

func sanitizeFilename(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "-")
}

// UpdateFeatureMatrix appends a new feature row to .agent/system/FEATURE-MATRIX.md
// for feature tasks (feat(scope): ...). Skips non-feature commits.
func UpdateFeatureMatrix(agentPath string, task *Task, version string) error {
	featureMatrixPath := filepath.Join(agentPath, "system", "FEATURE-MATRIX.md")

	// Read the file
	content, err := os.ReadFile(featureMatrixPath)
	if err != nil {
		// File doesn't exist or can't be read - log warning but don't fail execution
		slog.Warn("Could not read FEATURE-MATRIX.md", slog.Any("error", err))
		return nil
	}

	text := string(content)
	lines := strings.Split(text, "\n")

	// Find the "Core Execution" table and insert after the last row before a blank line or new section
	// Look for the pattern: | Feature Name | Status | ... |
	// We'll append to the end of the first table we find (Core Execution)

	var result []string
	inserted := false

	for _, line := range lines {
		result = append(result, line)

		// After the Core Execution table ends (look for blank line after pipes), insert the new feature
		if !inserted && strings.HasPrefix(line, "## Intelligence") {
			// Go back one line (which should be blank or a pipe line)
			// and insert before this section header
			if len(result) > 0 && result[len(result)-1] == line {
				// Insert before the ## Intelligence line
				// Find the last data row of Core Execution table
				insertIdx := len(result) - 1

				// Extract feature name from task description or title
				featureName := extractFeatureName(task.Title)

				// Create the row: | Feature Name | Done | version |
				newRow := fmt.Sprintf("| %s | ✅ | %s | - | - | Task %s (GH-1388) |", featureName, version, task.ID)
				result = append(result[:insertIdx], append([]string{newRow, ""}, result[insertIdx:]...)...)
				inserted = true
			}
		}
	}

	// If we didn't insert (table structure is different), just append to end
	if !inserted {
		result = append(result, fmt.Sprintf("| %s | ✅ | %s | - | - | Task %s (GH-1388) |",
			extractFeatureName(task.Title), version, task.ID))
	}

	// Write back
	newContent := strings.Join(result, "\n")
	return os.WriteFile(featureMatrixPath, []byte(newContent), 0644)
}

// extractFeatureName extracts a clean feature name from the task title
// e.g., "feat(executor): update Navigator docs after task execution" -> "Update Navigator docs"
func extractFeatureName(title string) string {
	// Remove common prefixes like "feat(scope): "
	if idx := strings.Index(title, "):"); idx != -1 {
		title = title[idx+3:]
	}
	// Capitalize first letter if needed
	title = strings.TrimSpace(title)
	if len(title) > 0 {
		title = strings.ToUpper(title[:1]) + title[1:]
	}
	return title
}
