package gitlab

import (
	"strings"
	"testing"
)

func TestConvertIssueToTask(t *testing.T) {
	issue := &Issue{
		ID:          1001,
		IID:         42,
		Title:       "Implement feature X",
		Description: "This is the description\n\nWith multiple lines",
		State:       StateOpened,
		Labels:      []string{"pilot", "bug", "priority::high"},
		WebURL:      "https://gitlab.com/namespace/project/-/issues/42",
	}

	project := &Project{
		ID:                12345,
		Name:              "project",
		PathWithNamespace: "namespace/project",
		WebURL:            "https://gitlab.com/namespace/project",
		DefaultBranch:     "main",
	}

	task := ConvertIssueToTask(issue, project)

	if task.ID != "GL-42" {
		t.Errorf("task.ID = %s, want GL-42", task.ID)
	}

	if task.Title != "Implement feature X" {
		t.Errorf("task.Title = %s, want 'Implement feature X'", task.Title)
	}

	if task.IssueIID != 42 {
		t.Errorf("task.IssueIID = %d, want 42", task.IssueIID)
	}

	if task.ProjectPath != "namespace/project" {
		t.Errorf("task.ProjectPath = %s, want namespace/project", task.ProjectPath)
	}

	if task.IssueURL != "https://gitlab.com/namespace/project/-/issues/42" {
		t.Errorf("task.IssueURL = %s, want https://gitlab.com/namespace/project/-/issues/42", task.IssueURL)
	}

	if task.CloneURL != "https://gitlab.com/namespace/project.git" {
		t.Errorf("task.CloneURL = %s, want https://gitlab.com/namespace/project.git", task.CloneURL)
	}

	if task.Priority != PriorityHigh {
		t.Errorf("task.Priority = %d, want %d (PriorityHigh)", task.Priority, PriorityHigh)
	}
}

func TestExtractDescription(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "simple description",
			body: "This is a simple description.",
			want: "This is a simple description.",
		},
		{
			name: "removes checklist section",
			body: "Main content\n\n### Checklist\n- [ ] Item 1\n- [ ] Item 2",
			want: "Main content",
		},
		{
			name: "removes environment section",
			body: "Main content\n\n### Environment\nOS: Linux",
			want: "Main content",
		},
		{
			name: "removes GitLab quick actions",
			body: "Main content\n/label ~bug\n/assign @user",
			want: "Main content",
		},
		{
			name: "empty body",
			body: "",
			want: "",
		},
		{
			name: "whitespace only",
			body: "   \n\n   ",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDescription(tt.body)
			if got != tt.want {
				t.Errorf("extractDescription() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractPriority(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   Priority
	}{
		{
			name:   "scoped label - urgent",
			labels: []string{"priority::urgent", "bug"},
			want:   PriorityUrgent,
		},
		{
			name:   "scoped label - high",
			labels: []string{"priority::high", "enhancement"},
			want:   PriorityHigh,
		},
		{
			name:   "scoped label - medium",
			labels: []string{"bug", "priority::medium"},
			want:   PriorityMedium,
		},
		{
			name:   "scoped label - low",
			labels: []string{"priority::low"},
			want:   PriorityLow,
		},
		{
			name:   "P0 label",
			labels: []string{"P0", "bug"},
			want:   PriorityUrgent,
		},
		{
			name:   "P1 label",
			labels: []string{"P1"},
			want:   PriorityHigh,
		},
		{
			name:   "P2 label",
			labels: []string{"P2"},
			want:   PriorityMedium,
		},
		{
			name:   "P3 label",
			labels: []string{"P3"},
			want:   PriorityLow,
		},
		{
			name:   "critical keyword",
			labels: []string{"critical-issue"},
			want:   PriorityUrgent,
		},
		{
			name:   "high keyword",
			labels: []string{"high-priority"},
			want:   PriorityHigh,
		},
		{
			name:   "no priority labels",
			labels: []string{"bug", "enhancement"},
			want:   PriorityNone,
		},
		{
			name:   "empty labels",
			labels: []string{},
			want:   PriorityNone,
		},
		{
			name:   "nil labels",
			labels: nil,
			want:   PriorityNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPriority(tt.labels)
			if got != tt.want {
				t.Errorf("extractPriority() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestExtractLabelNames(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   []string
	}{
		{
			name:   "filters out pilot and priority labels",
			labels: []string{"pilot", "bug", "priority::high", "P1", "enhancement"},
			want:   []string{"bug", "enhancement"},
		},
		{
			name:   "keeps all non-filtered labels",
			labels: []string{"bug", "documentation", "feature"},
			want:   []string{"bug", "documentation", "feature"},
		},
		{
			name:   "all labels filtered",
			labels: []string{"pilot", "pilot-in-progress", "P0"},
			want:   nil,
		},
		{
			name:   "empty labels",
			labels: []string{},
			want:   nil,
		},
		{
			name:   "nil labels",
			labels: nil,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLabelNames(tt.labels)

			if len(got) != len(tt.want) {
				t.Errorf("extractLabelNames() returned %d labels, want %d", len(got), len(tt.want))
				return
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractLabelNames()[%d] = %s, want %s", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantLen   int
		wantFirst string
	}{
		{
			name: "markdown checkboxes",
			body: `## Description
Some description

### Acceptance Criteria
- [ ] First criterion
- [x] Second criterion completed
- [ ] Third criterion

### Notes`,
			wantLen:   3,
			wantFirst: "First criterion",
		},
		{
			name: "plain list items",
			body: `### Acceptance criteria
- First item
- Second item

### Other section`,
			wantLen:   2,
			wantFirst: "First item",
		},
		{
			name: "double hash section",
			body: `## Acceptance Criteria
- [ ] Criterion one
- [ ] Criterion two

## Implementation`,
			wantLen:   2,
			wantFirst: "Criterion one",
		},
		{
			name:      "no acceptance criteria section",
			body:      "Just a description without criteria",
			wantLen:   0,
			wantFirst: "",
		},
		{
			name:      "empty body",
			body:      "",
			wantLen:   0,
			wantFirst: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAcceptanceCriteria(tt.body)

			if len(got) != tt.wantLen {
				t.Errorf("ExtractAcceptanceCriteria() returned %d criteria, want %d", len(got), tt.wantLen)
				return
			}

			if tt.wantLen > 0 && got[0] != tt.wantFirst {
				t.Errorf("ExtractAcceptanceCriteria()[0] = %s, want %s", got[0], tt.wantFirst)
			}
		})
	}
}

func TestBuildTaskPrompt(t *testing.T) {
	task := &TaskInfo{
		ID:          "GL-42",
		Title:       "Implement feature X",
		Description: "This is the description\n\n### Acceptance Criteria\n- [ ] Test criterion",
		Priority:    PriorityHigh,
		Labels:      []string{"bug", "enhancement"},
		ProjectPath: "namespace/project",
		IssueIID:    42,
		IssueURL:    "https://gitlab.com/namespace/project/-/issues/42",
		CloneURL:    "https://gitlab.com/namespace/project.git",
	}

	prompt := BuildTaskPrompt(task)

	// Check required sections are present
	if !strings.Contains(prompt, "# Task: Implement feature X") {
		t.Error("prompt missing task title")
	}

	if !strings.Contains(prompt, "**Issue**: https://gitlab.com/namespace/project/-/issues/42") {
		t.Error("prompt missing issue URL")
	}

	if !strings.Contains(prompt, "**Priority**: High") {
		t.Error("prompt missing priority")
	}

	if !strings.Contains(prompt, "## Description") {
		t.Error("prompt missing description section")
	}

	if !strings.Contains(prompt, "## Acceptance Criteria") {
		t.Error("prompt missing acceptance criteria section")
	}

	if !strings.Contains(prompt, "Test criterion") {
		t.Error("prompt missing acceptance criterion")
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
		{PriorityUrgent, "Urgent"},
		{PriorityHigh, "High"},
		{PriorityMedium, "Medium"},
		{PriorityLow, "Low"},
		{PriorityNone, "No Priority"},
		{Priority(99), "No Priority"}, // Unknown priority
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
