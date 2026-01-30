package azuredevops

import (
	"fmt"
	"regexp"
	"strings"
)

// TaskInfo contains the extracted task information from an Azure DevOps work item
type TaskInfo struct {
	ID           string
	Title        string
	Description  string
	Priority     Priority
	Tags         []string
	Organization string
	Project      string
	Repository   string
	WorkItemID   int
	WorkItemURL  string
	CloneURL     string
}

// ConvertWorkItemToTask converts an Azure DevOps work item to a TaskInfo
func ConvertWorkItemToTask(wi *WorkItem, organization, project, repository, baseURL string) *TaskInfo {
	// Construct clone URL
	cloneURL := fmt.Sprintf("%s/%s/%s/_git/%s",
		baseURL,
		organization,
		project,
		repository,
	)

	// Construct work item URL
	workItemURL := fmt.Sprintf("%s/%s/%s/_workitems/edit/%d",
		baseURL,
		organization,
		project,
		wi.ID,
	)

	task := &TaskInfo{
		ID:           fmt.Sprintf("AZDO-%d", wi.ID),
		Title:        wi.GetTitle(),
		Description:  extractDescription(wi.GetDescription()),
		Priority:     wi.GetPriority(),
		Tags:         extractTagNames(wi.GetTags()),
		Organization: organization,
		Project:      project,
		Repository:   repository,
		WorkItemID:   wi.ID,
		WorkItemURL:  workItemURL,
		CloneURL:     cloneURL,
	}

	return task
}

// extractDescription extracts and cleans the task description
// Azure DevOps stores descriptions as HTML
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	// Remove HTML tags
	body = stripHTML(body)

	// Remove common Azure DevOps template sections
	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip template sections
		if strings.HasPrefix(trimmed, "### Checklist") ||
			strings.HasPrefix(trimmed, "### Environment") ||
			strings.HasPrefix(trimmed, "### Repro Steps") ||
			strings.HasPrefix(trimmed, "### Expected Behavior") ||
			strings.HasPrefix(trimmed, "### Actual Behavior") {
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

// stripHTML removes HTML tags from a string
func stripHTML(s string) string {
	// Remove script and style tags with content
	scriptRe := regexp.MustCompile(`(?i)<script[^>]*>[\s\S]*?</script>`)
	s = scriptRe.ReplaceAllString(s, "")
	styleRe := regexp.MustCompile(`(?i)<style[^>]*>[\s\S]*?</style>`)
	s = styleRe.ReplaceAllString(s, "")

	// Replace common HTML elements with appropriate text
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n\n")
	s = strings.ReplaceAll(s, "</div>", "\n")
	s = strings.ReplaceAll(s, "</li>", "\n")
	s = strings.ReplaceAll(s, "<li>", "- ")

	// Remove all remaining HTML tags
	tagRe := regexp.MustCompile(`<[^>]*>`)
	s = tagRe.ReplaceAllString(s, "")

	// Decode common HTML entities
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")

	// Clean up multiple newlines
	multiNewlineRe := regexp.MustCompile(`\n{3,}`)
	s = multiNewlineRe.ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
}

// extractTagNames returns a list of tag names excluding pilot/priority tags
func extractTagNames(tags []string) []string {
	var names []string
	for _, tag := range tags {
		name := strings.ToLower(tag)
		// Skip pilot and priority tags
		if strings.HasPrefix(name, "pilot") ||
			strings.HasPrefix(name, "priority") ||
			strings.HasPrefix(name, "p0") || strings.HasPrefix(name, "p1") ||
			strings.HasPrefix(name, "p2") || strings.HasPrefix(name, "p3") {
			continue
		}
		names = append(names, tag)
	}
	return names
}

// ExtractAcceptanceCriteria extracts acceptance criteria from work item description
func ExtractAcceptanceCriteria(body string) []string {
	var criteria []string

	// First strip HTML if present
	body = stripHTML(body)

	// Look for common acceptance criteria patterns
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)### acceptance criteria\s*\n([\s\S]*?)(?:\n###|\z)`),
		regexp.MustCompile(`(?i)### criteria\s*\n([\s\S]*?)(?:\n###|\z)`),
		regexp.MustCompile(`(?i)## acceptance criteria\s*\n([\s\S]*?)(?:\n##|\z)`),
		regexp.MustCompile(`(?i)acceptance criteria:?\s*\n([\s\S]*?)(?:\n[A-Z]|\z)`),
	}

	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(body)
		if len(matches) > 1 {
			// Extract checkbox items
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
	sb.WriteString(fmt.Sprintf("**Work Item**: %s\n", task.WorkItemURL))
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
