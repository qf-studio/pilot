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

// sanitizeFilename converts a human title into a path-safe filename fragment.
// GH-2377: previously only lowercased + replaced spaces, so titles containing
// "/", ":", "|" (common in REST-path or pipe-delimited titles) leaked into
// os.WriteFile and were interpreted as subdirectories, failing marker writes.
func sanitizeFilename(s string) string {
	s = strings.ToLower(s)
	unsafe := []string{"/", "\\", ":", "|", "<", ">", "?", "*", "\""}
	for _, c := range unsafe {
		s = strings.ReplaceAll(s, c, "-")
	}
	s = strings.ReplaceAll(s, " ", "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
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

	lines := strings.Split(string(content), "\n")
	featureName := extractFeatureName(task.Title)
	newRow := fmt.Sprintf("| %s | ✅ | %s | - | - | %s |", featureName, version, task.ID)

	// Strategy: find the first markdown table (## Core Execution), locate the last
	// pipe-prefixed row in that table, and insert after it. This avoids depending
	// on a specific section header like "## Intelligence" as an anchor.
	inCoreTable := false
	lastPipeIdx := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect start of Core Execution table
		if strings.HasPrefix(trimmed, "## Core Execution") {
			inCoreTable = true
			continue
		}

		// Detect end of Core Execution table (next section header)
		if inCoreTable && strings.HasPrefix(trimmed, "## ") {
			inCoreTable = false
			continue
		}

		// Track last data row (pipe-prefixed, not separator row like |---|---|)
		if inCoreTable && strings.HasPrefix(trimmed, "|") && !strings.Contains(trimmed, "---|") {
			lastPipeIdx = i
		}
	}

	if lastPipeIdx >= 0 {
		// Insert after the last data row in Core Execution table
		after := make([]string, len(lines[lastPipeIdx+1:]))
		copy(after, lines[lastPipeIdx+1:])
		result := append(lines[:lastPipeIdx+1], newRow)
		result = append(result, after...)
		return os.WriteFile(featureMatrixPath, []byte(strings.Join(result, "\n")), 0644)
	}

	// Fallback: append to end of file
	lines = append(lines, newRow)
	return os.WriteFile(featureMatrixPath, []byte(strings.Join(lines, "\n")), 0644)
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
