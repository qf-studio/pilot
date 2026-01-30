package azuredevops

import (
	"strings"
	"testing"
)

func TestConvertWorkItemToTask(t *testing.T) {
	wi := &WorkItem{
		ID:  123,
		Rev: 1,
		Fields: map[string]interface{}{
			"System.Title":                  "Fix login bug",
			"System.Description":            "Users cannot login when...",
			"System.Tags":                   "pilot; bug; frontend",
			"Microsoft.VSTS.Common.Priority": float64(2),
		},
	}

	task := ConvertWorkItemToTask(wi, "my-org", "my-project", "my-repo", "https://dev.azure.com")

	if task.ID != "AZDO-123" {
		t.Errorf("expected ID 'AZDO-123', got '%s'", task.ID)
	}

	if task.Title != "Fix login bug" {
		t.Errorf("expected title 'Fix login bug', got '%s'", task.Title)
	}

	if task.Priority != PriorityHigh {
		t.Errorf("expected priority High, got %d", task.Priority)
	}

	if task.Organization != "my-org" {
		t.Errorf("expected organization 'my-org', got '%s'", task.Organization)
	}

	if task.Project != "my-project" {
		t.Errorf("expected project 'my-project', got '%s'", task.Project)
	}

	if task.Repository != "my-repo" {
		t.Errorf("expected repository 'my-repo', got '%s'", task.Repository)
	}

	if task.WorkItemID != 123 {
		t.Errorf("expected work item ID 123, got %d", task.WorkItemID)
	}

	if task.WorkItemURL != "https://dev.azure.com/my-org/my-project/_workitems/edit/123" {
		t.Errorf("unexpected work item URL: %s", task.WorkItemURL)
	}

	if task.CloneURL != "https://dev.azure.com/my-org/my-project/_git/my-repo" {
		t.Errorf("unexpected clone URL: %s", task.CloneURL)
	}

	// Tags should exclude pilot and priority tags
	if len(task.Tags) != 2 {
		t.Errorf("expected 2 tags (bug, frontend), got %d", len(task.Tags))
	}
}

func TestExtractDescription(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty",
			input:    "",
			expected: "",
		},
		{
			name:     "plain text",
			input:    "Simple description",
			expected: "Simple description",
		},
		{
			name:     "with template sections",
			input:    "Main description\n### Checklist\n- [ ] Done\n### Next section",
			expected: "Main description\n### Next section",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractDescription(tt.input)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestStripHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no html",
			input:    "plain text",
			expected: "plain text",
		},
		{
			name:     "simple tags",
			input:    "<p>paragraph</p>",
			expected: "paragraph",
		},
		{
			name:     "br tags",
			input:    "line1<br>line2<br/>line3",
			expected: "line1\nline2\nline3",
		},
		{
			name:     "list items",
			input:    "<ul><li>item1</li><li>item2</li></ul>",
			expected: "- item1\n- item2",
		},
		{
			name:     "html entities",
			input:    "&amp; &lt; &gt; &quot; &#39;",
			expected: "& < > \" '",
		},
		{
			name:     "script removal",
			input:    "text<script>alert('xss')</script>more",
			expected: "textmore",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripHTML(tt.input)
			if result != tt.expected {
				t.Errorf("expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestExtractTagNames(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "only pilot tags",
			input:    []string{"pilot", "pilot-in-progress"},
			expected: nil,
		},
		{
			name:     "mixed tags",
			input:    []string{"pilot", "bug", "frontend", "p1"},
			expected: []string{"bug", "frontend"},
		},
		{
			name:     "priority tags filtered",
			input:    []string{"priority-high", "p0", "p1", "feature"},
			expected: []string{"feature"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTagNames(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d tags, got %d", len(tt.expected), len(result))
				return
			}
			for i, tag := range result {
				if tag != tt.expected[i] {
					t.Errorf("tag %d: expected '%s', got '%s'", i, tt.expected[i], tag)
				}
			}
		})
	}
}

func TestExtractAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "empty",
			input:    "",
			expected: nil,
		},
		{
			name:     "no acceptance criteria",
			input:    "Just a description",
			expected: nil,
		},
		{
			name:     "with checkbox items",
			input:    "### Acceptance Criteria\n- [ ] First item\n- [x] Second item\n### Other",
			expected: []string{"First item", "Second item"},
		},
		{
			name:     "with plain list",
			input:    "### Acceptance Criteria\n- First item\n- Second item\n### Other",
			expected: []string{"First item", "Second item"},
		},
		{
			name:     "double hash heading",
			input:    "## Acceptance Criteria\n- [ ] Item one\n## Next Section",
			expected: []string{"Item one"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractAcceptanceCriteria(tt.input)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d criteria, got %d: %v", len(tt.expected), len(result), result)
				return
			}
			for i, item := range result {
				if item != tt.expected[i] {
					t.Errorf("criterion %d: expected '%s', got '%s'", i, tt.expected[i], item)
				}
			}
		})
	}
}

func TestBuildTaskPrompt(t *testing.T) {
	task := &TaskInfo{
		ID:          "AZDO-123",
		Title:       "Fix the bug",
		Description: "The bug causes issues when...\n\n### Acceptance Criteria\n- [ ] Fix the issue\n- [ ] Add tests",
		Priority:    PriorityHigh,
		WorkItemURL: "https://dev.azure.com/org/proj/_workitems/edit/123",
	}

	prompt := BuildTaskPrompt(task)

	// Check that key components are present
	if !strings.Contains(prompt, "# Task: Fix the bug") {
		t.Error("prompt should contain task title")
	}

	if !strings.Contains(prompt, "**Work Item**:") {
		t.Error("prompt should contain work item link")
	}

	if !strings.Contains(prompt, "**Priority**: High") {
		t.Error("prompt should contain priority")
	}

	if !strings.Contains(prompt, "## Description") {
		t.Error("prompt should contain description section")
	}

	if !strings.Contains(prompt, "## Acceptance Criteria") {
		t.Error("prompt should contain acceptance criteria section")
	}

	if !strings.Contains(prompt, "## Requirements") {
		t.Error("prompt should contain requirements section")
	}

	if !strings.Contains(prompt, "Write tests for new functionality") {
		t.Error("prompt should contain standard requirements")
	}
}

func TestBuildTaskPromptMinimal(t *testing.T) {
	task := &TaskInfo{
		ID:          "AZDO-1",
		Title:       "Simple task",
		Description: "",
		Priority:    PriorityNone,
		WorkItemURL: "https://example.com/1",
	}

	prompt := BuildTaskPrompt(task)

	if !strings.Contains(prompt, "# Task: Simple task") {
		t.Error("prompt should contain task title")
	}

	if !strings.Contains(prompt, "No Priority") {
		t.Error("prompt should show no priority")
	}

	// Should still have requirements even with minimal info
	if !strings.Contains(prompt, "## Requirements") {
		t.Error("prompt should contain requirements section")
	}
}
