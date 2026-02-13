package executor

import (
	"fmt"
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
