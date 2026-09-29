package github

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/pilot/internal/acceptance"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
)

// TaskInfo contains the extracted task information from a GitHub issue
type TaskInfo struct {
	ID          string
	Title       string
	Description string
	Priority    Priority
	Labels      []string
	RepoOwner   string
	RepoName    string
	IssueNumber int
	IssueURL    string
	CloneURL    string
}

// ConvertIssueToTask converts a GitHub issue to a TaskInfo.
//
// All untrusted fields (Title, Body) are run through
// text.SanitizeUntrusted to strip invisible Unicode format characters
// used for ASCII-smuggling / prompt-injection attacks. A slog.Warn is
// emitted when any runes are stripped — that is the attack-in-progress
// signal.
func ConvertIssueToTask(issue *Issue, repo *Repository) *TaskInfo {
	title, titleStripped := text.SanitizeUntrusted(issue.Title)
	description, bodyStripped := text.SanitizeUntrusted(extractDescription(issue.Body))

	if titleStripped+bodyStripped > 0 {
		logging.WithComponent("github").Warn(
			"invisible_unicode_stripped",
			slog.String("source", "github"),
			slog.Int("issue", issue.Number),
			slog.Int("title_stripped", titleStripped),
			slog.Int("body_stripped", bodyStripped),
		)
	}

	task := &TaskInfo{
		ID:          fmt.Sprintf("GH-%d", issue.Number),
		Title:       title,
		Description: description,
		Priority:    extractPriority(issue.Labels),
		Labels:      extractLabelNames(issue.Labels),
		RepoOwner:   repo.Owner.Login,
		RepoName:    repo.Name,
		IssueNumber: issue.Number,
		IssueURL:    issue.HTMLURL,
		CloneURL:    repo.CloneURL,
	}

	return task
}

// extractDescription extracts and cleans the task description
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	// Remove common GitHub issue template sections that aren't useful for tasks
	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip template sections
		if strings.HasPrefix(trimmed, "### Checklist") ||
			strings.HasPrefix(trimmed, "### Environment") ||
			strings.HasPrefix(trimmed, "### Bug Report") {
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

	return text.SanitizeUntrustedString(strings.TrimSpace(strings.Join(filtered, "\n")))
}

// extractPriority determines priority from labels
func extractPriority(labels []Label) Priority {
	for _, label := range labels {
		name := strings.ToLower(label.Name)

		// Common priority label patterns
		if strings.Contains(name, "urgent") || strings.Contains(name, "critical") || name == "p0" {
			return PriorityUrgent
		}
		if strings.Contains(name, "high") || name == "p1" {
			return PriorityHigh
		}
		if strings.Contains(name, "medium") || name == "p2" {
			return PriorityMedium
		}
		if strings.Contains(name, "low") || name == "p3" {
			return PriorityLow
		}
	}

	return PriorityNone
}

// extractLabelNames returns a list of label names excluding pilot/priority labels
func extractLabelNames(labels []Label) []string {
	var names []string
	for _, label := range labels {
		name := strings.ToLower(label.Name)
		// Skip pilot and priority labels
		if strings.HasPrefix(name, "pilot") ||
			strings.HasPrefix(name, "priority") ||
			strings.HasPrefix(name, "p0") || strings.HasPrefix(name, "p1") ||
			strings.HasPrefix(name, "p2") || strings.HasPrefix(name, "p3") {
			continue
		}
		names = append(names, label.Name)
	}
	return names
}

// ExtractAcceptanceCriteria extracts acceptance criteria from issue body.
// The extractor lives in internal/acceptance so the executor can share it.
func ExtractAcceptanceCriteria(body string) []string {
	return acceptance.Extract(body)
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
