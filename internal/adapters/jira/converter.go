package jira

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/qf-studio/pilot/internal/acceptance"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
)

// TaskInfo contains the extracted task information from a Jira issue
type TaskInfo struct {
	ID          string
	Title       string
	Description string
	Priority    Priority
	Labels      []string
	ProjectKey  string
	IssueKey    string
	IssueURL    string
}

// ConvertIssueToTask converts a Jira issue to a TaskInfo.
//
// All untrusted fields (Summary, Description) are run through
// text.SanitizeUntrusted to strip invisible Unicode format characters
// used for ASCII-smuggling / prompt-injection attacks.
func ConvertIssueToTask(issue *Issue, baseURL string) *TaskInfo {
	var priority Priority
	if issue.Fields.Priority != nil {
		priority = PriorityFromJira(issue.Fields.Priority.Name)
	}

	title, titleStripped := text.SanitizeUntrusted(issue.Fields.Summary)
	description, bodyStripped := text.SanitizeUntrusted(extractDescription(string(issue.Fields.Description)))

	if titleStripped+bodyStripped > 0 {
		logging.WithComponent("jira").Warn(
			"invisible_unicode_stripped",
			slog.String("source", "jira"),
			slog.String("issue", issue.Key),
			slog.Int("title_stripped", titleStripped),
			slog.Int("body_stripped", bodyStripped),
		)
	}

	task := &TaskInfo{
		ID:          fmt.Sprintf("JIRA-%s", issue.Key),
		Title:       title,
		Description: description,
		Priority:    priority,
		Labels:      filterLabels(issue.Fields.Labels),
		ProjectKey:  issue.Fields.Project.Key,
		IssueKey:    issue.Key,
		IssueURL:    fmt.Sprintf("%s/browse/%s", strings.TrimSuffix(baseURL, "/"), issue.Key),
	}

	return task
}

// extractDescription cleans and extracts the task description
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	// Remove common Jira template sections that aren't useful for tasks
	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip template sections
		if strings.HasPrefix(trimmed, "h2. Checklist") ||
			strings.HasPrefix(trimmed, "h2. Environment") ||
			strings.HasPrefix(trimmed, "*Checklist*") ||
			strings.HasPrefix(trimmed, "*Environment*") {
			skipSection = true
			continue
		}

		// Resume at next heading
		if skipSection && (strings.HasPrefix(trimmed, "h2.") || strings.HasPrefix(trimmed, "h1.") ||
			(strings.HasPrefix(trimmed, "*") && strings.HasSuffix(trimmed, "*"))) {
			skipSection = false
		}

		if !skipSection {
			filtered = append(filtered, line)
		}
	}

	return text.SanitizeUntrustedString(strings.TrimSpace(strings.Join(filtered, "\n")))
}

// filterLabels returns labels excluding pilot and priority labels
func filterLabels(labels []string) []string {
	var filtered []string
	for _, label := range labels {
		lower := strings.ToLower(label)
		// Skip pilot and priority labels
		if strings.HasPrefix(lower, "pilot") ||
			strings.HasPrefix(lower, "priority") ||
			lower == "p0" || lower == "p1" || lower == "p2" || lower == "p3" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}

// ExtractAcceptanceCriteria extracts acceptance criteria from issue body
func ExtractAcceptanceCriteria(body string) []string {
	// Jira-style patterns (wiki markup)
	jiraPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)h[23]\.\s*acceptance criteria\s*\n([\s\S]*?)(?:\nh[123]\.|\z)`),
		regexp.MustCompile(`(?i)\*acceptance criteria\*\s*\n([\s\S]*?)(?:\n\*[^*]+\*|\z)`),
	}

	// Markdown patterns (Jira Cloud supports both)
	mdPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)###?\s*acceptance criteria\s*\n([\s\S]*?)(?:\n###?|\z)`),
		regexp.MustCompile(`(?i)###?\s*criteria\s*\n([\s\S]*?)(?:\n###?|\z)`),
	}
	// House-style "## Acceptance" headings come from the shared extractor.
	mdPatterns = append(mdPatterns, acceptance.MarkdownPatterns()[3:]...)

	allPatterns := append(jiraPatterns, mdPatterns...)
	// Jira uses [] or [x] checkboxes and * or - bullets.
	return acceptance.ExtractWith(body, allPatterns, jiraCheckboxItemRe, jiraListItemRe)
}

var (
	jiraCheckboxItemRe = regexp.MustCompile(`[*-]\s*\[[ x]?\]\s*(.+)`)
	jiraListItemRe     = regexp.MustCompile(`[*-]\s+(.+)`)
)

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
