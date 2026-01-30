package gitlab

import (
	"fmt"
	"regexp"
	"strings"
)

// TaskInfo contains the extracted task information from a GitLab issue
type TaskInfo struct {
	ID          string
	Title       string
	Description string
	Priority    Priority
	Labels      []string
	ProjectPath string // namespace/project format
	IssueIID    int
	IssueURL    string
	CloneURL    string
}

// ConvertIssueToTask converts a GitLab issue to a TaskInfo
func ConvertIssueToTask(issue *Issue, project *Project) *TaskInfo {
	// Construct clone URL from project web URL
	cloneURL := project.WebURL + ".git"

	task := &TaskInfo{
		ID:          fmt.Sprintf("GL-%d", issue.IID),
		Title:       issue.Title,
		Description: extractDescription(issue.Description),
		Priority:    extractPriority(issue.Labels),
		Labels:      extractLabelNames(issue.Labels),
		ProjectPath: project.PathWithNamespace,
		IssueIID:    issue.IID,
		IssueURL:    issue.WebURL,
		CloneURL:    cloneURL,
	}

	return task
}

// extractDescription extracts and cleans the task description
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	// Remove common GitLab issue template sections that aren't useful for tasks
	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip template sections (GitLab uses similar patterns)
		if strings.HasPrefix(trimmed, "### Checklist") ||
			strings.HasPrefix(trimmed, "### Environment") ||
			strings.HasPrefix(trimmed, "### Bug Report") ||
			strings.HasPrefix(trimmed, "/label ") ||
			strings.HasPrefix(trimmed, "/assign ") ||
			strings.HasPrefix(trimmed, "/milestone ") {
			skipSection = true
			continue
		}

		// Resume at next heading
		if skipSection && strings.HasPrefix(trimmed, "### ") {
			skipSection = false
		}

		if !skipSection {
			filtered = append(filtered, line)
		}
	}

	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

// extractPriority determines priority from labels
func extractPriority(labels []string) Priority {
	for _, label := range labels {
		name := strings.ToLower(label)

		// GitLab scoped labels use :: as separator
		// e.g., priority::urgent, priority::high

		// Common priority label patterns
		if strings.Contains(name, "urgent") || strings.Contains(name, "critical") || name == "p0" || name == "priority::urgent" {
			return PriorityUrgent
		}
		if strings.Contains(name, "high") || name == "p1" || name == "priority::high" {
			return PriorityHigh
		}
		if strings.Contains(name, "medium") || name == "p2" || name == "priority::medium" {
			return PriorityMedium
		}
		if strings.Contains(name, "low") || name == "p3" || name == "priority::low" {
			return PriorityLow
		}
	}

	return PriorityNone
}

// extractLabelNames returns a list of label names excluding pilot/priority labels
func extractLabelNames(labels []string) []string {
	var names []string
	for _, label := range labels {
		name := strings.ToLower(label)
		// Skip pilot and priority labels
		if strings.HasPrefix(name, "pilot") ||
			strings.HasPrefix(name, "priority") ||
			strings.HasPrefix(name, "p0") || strings.HasPrefix(name, "p1") ||
			strings.HasPrefix(name, "p2") || strings.HasPrefix(name, "p3") {
			continue
		}
		names = append(names, label)
	}
	return names
}

// ExtractAcceptanceCriteria extracts acceptance criteria from issue body
func ExtractAcceptanceCriteria(body string) []string {
	var criteria []string

	// Look for common acceptance criteria patterns
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)### acceptance criteria\s*\n([\s\S]*?)(?:\n###|\z)`),
		regexp.MustCompile(`(?i)### criteria\s*\n([\s\S]*?)(?:\n###|\z)`),
		regexp.MustCompile(`(?i)## acceptance criteria\s*\n([\s\S]*?)(?:\n##|\z)`),
	}

	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(body)
		if len(matches) > 1 {
			// Extract checkbox items (GitLab uses same format)
			checkboxPattern := regexp.MustCompile(`- \[[ x]\] (.+)`)
			items := checkboxPattern.FindAllStringSubmatch(matches[1], -1)
			for _, item := range items {
				if len(item) > 1 {
					criteria = append(criteria, strings.TrimSpace(item[1]))
				}
			}
			// Also extract plain list items
			if len(criteria) == 0 {
				listPattern := regexp.MustCompile(`- (.+)`)
				items = listPattern.FindAllStringSubmatch(matches[1], -1)
				for _, item := range items {
					if len(item) > 1 {
						criteria = append(criteria, strings.TrimSpace(item[1]))
					}
				}
			}
			break
		}
	}

	return criteria
}

// BuildTaskPrompt creates a prompt for Claude Code from the task info
func BuildTaskPrompt(task *TaskInfo) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# Task: %s\n\n", task.Title))
	sb.WriteString(fmt.Sprintf("**Issue**: %s\n", task.IssueURL))
	sb.WriteString(fmt.Sprintf("**Priority**: %s\n\n", PriorityName(task.Priority)))

	if task.Description != "" {
		sb.WriteString("## Description\n\n")
		sb.WriteString(task.Description)
		sb.WriteString("\n\n")
	}

	// Extract acceptance criteria if available
	criteria := ExtractAcceptanceCriteria(task.Description)
	if len(criteria) > 0 {
		sb.WriteString("## Acceptance Criteria\n\n")
		for _, c := range criteria {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", c))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Requirements\n\n")
	sb.WriteString("1. Implement the changes described above\n")
	sb.WriteString("2. Write tests for new functionality\n")
	sb.WriteString("3. Ensure all existing tests pass\n")
	sb.WriteString("4. Follow the project's code style and conventions\n")

	return sb.String()
}
