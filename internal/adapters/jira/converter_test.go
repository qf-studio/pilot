package jira

import (
	"strings"
	"testing"
)

func TestConvertIssueToTask(t *testing.T) {
	issue := &Issue{
		ID:  "10001",
		Key: "PROJ-42",
		Fields: Fields{
			Summary:     "Add user authentication",
			Description: "Implement OAuth login for the application.",
			Priority:    &JiraPriority{Name: "High"},
			Labels:      []string{"pilot", "enhancement"},
			Project: Project{
				Key:  "PROJ",
				Name: "My Project",
			},
		},
	}

	task := ConvertIssueToTask(issue, "https://company.atlassian.net")

	if task.ID != "JIRA-PROJ-42" {
		t.Errorf("task.ID = %s, want JIRA-PROJ-42", task.ID)
	}

	if task.Title != "Add user authentication" {
		t.Errorf("task.Title = %s, want 'Add user authentication'", task.Title)
	}

	if task.Priority != PriorityHigh {
		t.Errorf("task.Priority = %d, want %d (High)", task.Priority, PriorityHigh)
	}

	if task.ProjectKey != "PROJ" {
		t.Errorf("task.ProjectKey = %s, want 'PROJ'", task.ProjectKey)
	}

	if task.IssueKey != "PROJ-42" {
		t.Errorf("task.IssueKey = %s, want PROJ-42", task.IssueKey)
	}

	if task.IssueURL != "https://company.atlassian.net/browse/PROJ-42" {
		t.Errorf("task.IssueURL = %s, want 'https://company.atlassian.net/browse/PROJ-42'", task.IssueURL)
	}

	// Labels should exclude pilot labels
	if len(task.Labels) != 1 || task.Labels[0] != "enhancement" {
		t.Errorf("task.Labels = %v, want [enhancement]", task.Labels)
	}
}

func TestConvertIssueToTask_NoPriority(t *testing.T) {
	issue := &Issue{
		Key: "PROJ-1",
		Fields: Fields{
			Summary: "No priority issue",
		},
	}

	task := ConvertIssueToTask(issue, "https://jira.example.com")

	if task.Priority != PriorityNone {
		t.Errorf("task.Priority = %d, want %d (None)", task.Priority, PriorityNone)
	}
}

func TestFilterLabels(t *testing.T) {
	labels := []string{
		"pilot",
		"pilot-in-progress",
		"priority",
		"P0",
		"P1",
		"P2",
		"P3",
		"bug",
		"enhancement",
		"feature",
	}

	got := filterLabels(labels)

	// Should only include bug, enhancement, feature
	if len(got) != 3 {
		t.Errorf("filterLabels() returned %d labels, want 3: %v", len(got), got)
	}

	expected := map[string]bool{"bug": true, "enhancement": true, "feature": true}
	for _, name := range got {
		if !expected[name] {
			t.Errorf("unexpected label: %s", name)
		}
	}
}

func TestExtractAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "with markdown acceptance criteria",
			body: `## Description
This is a feature request.

### Acceptance Criteria
- [ ] User can login with OAuth
- [ ] User can logout
- [x] Already implemented

### Notes
Some notes here.`,
			want: []string{
				"User can login with OAuth",
				"User can logout",
				"Already implemented",
			},
		},
		{
			name: "jira wiki format h2",
			body: `h2. Description
Feature description

h2. Acceptance Criteria
* [x] First item
* [ ] Second item

h2. Notes
Some notes`,
			want: []string{"First item", "Second item"},
		},
		{
			name: "jira bold section",
			body: `*Description*
Feature description

*Acceptance Criteria*
- First item
- Second item`,
			want: []string{"First item", "Second item"},
		},
		{
			name: "plain list in criteria section",
			body: `### Acceptance Criteria
- First item
- Second item`,
			want: []string{"First item", "Second item"},
		},
		{
			name: "no acceptance criteria",
			body: "Just a simple description without criteria.",
			want: nil,
		},
		{
			name: "empty body",
			body: "",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAcceptanceCriteria(tt.body)
			if len(got) != len(tt.want) {
				t.Errorf("ExtractAcceptanceCriteria() returned %d items, want %d: %v", len(got), len(tt.want), got)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("item %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractDescription(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "removes checklist section (wiki)",
			body: `Feature description here.

h2. Checklist
* I read the docs
* I agree to terms

h2. Notes
More content here.`,
			want: "Feature description here.\n\nh2. Notes\nMore content here.",
		},
		{
			name: "removes environment section (wiki)",
			body: `Bug description.

*Environment*
- OS: Linux
- Version: 1.0`,
			want: "Bug description.",
		},
		{
			name: "preserves normal content",
			body: "Simple description without template sections.",
			want: "Simple description without template sections.",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDescription(tt.body)
			// Normalize whitespace for comparison
			got = strings.TrimSpace(got)
			want := strings.TrimSpace(tt.want)
			if got != want {
				t.Errorf("extractDescription() = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildTaskPrompt(t *testing.T) {
	task := &TaskInfo{
		ID:          "JIRA-PROJ-42",
		Title:       "Add authentication",
		Description: "Implement OAuth login.\n\n### Acceptance Criteria\n- [ ] User can login",
		Priority:    PriorityHigh,
		IssueURL:    "https://company.atlassian.net/browse/PROJ-42",
	}

	prompt := BuildTaskPrompt(task)

	// Check key elements are present
	if !strings.Contains(prompt, "# Task: Add authentication") {
		t.Error("prompt missing task title")
	}

	if !strings.Contains(prompt, "**Issue**: https://company.atlassian.net/browse/PROJ-42") {
		t.Error("prompt missing issue URL")
	}

	if !strings.Contains(prompt, "**Priority**: High") {
		t.Error("prompt missing priority")
	}

	if !strings.Contains(prompt, "Implement OAuth login") {
		t.Error("prompt missing description")
	}

	if !strings.Contains(prompt, "## Requirements") {
		t.Error("prompt missing requirements section")
	}
}

func TestPriorityName(t *testing.T) {
	tests := []struct {
		priority Priority
		want     string
	}{
		{PriorityHighest, "Highest"},
		{PriorityHigh, "High"},
		{PriorityMedium, "Medium"},
		{PriorityLow, "Low"},
		{PriorityLowest, "Lowest"},
		{PriorityNone, "No Priority"},
		{Priority(99), "No Priority"}, // Unknown
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := PriorityName(tt.priority)
			if got != tt.want {
				t.Errorf("PriorityName(%d) = %s, want %s", tt.priority, got, tt.want)
			}
		})
	}
}
