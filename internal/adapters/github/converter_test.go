package github

import (
	"strings"
	"testing"
)

func TestConvertIssueToTask(t *testing.T) {
	issue := &Issue{
		Number:  42,
		Title:   "Add user authentication",
		Body:    "Implement OAuth login for the application.",
		State:   "open",
		HTMLURL: "https://github.com/org/repo/issues/42",
		Labels: []Label{
			{Name: "pilot"},
			{Name: "priority:high"},
			{Name: "enhancement"},
		},
	}

	repo := &Repository{
		Name:     "repo",
		FullName: "org/repo",
		CloneURL: "https://github.com/org/repo.git",
		Owner:    User{Login: "org"},
	}

	task := ConvertIssueToTask(issue, repo)

	if task.ID != "GH-42" {
		t.Errorf("task.ID = %s, want GH-42", task.ID)
	}

	if task.Title != "Add user authentication" {
		t.Errorf("task.Title = %s, want 'Add user authentication'", task.Title)
	}

	if task.Priority != PriorityHigh {
		t.Errorf("task.Priority = %d, want %d (High)", task.Priority, PriorityHigh)
	}

	if task.RepoOwner != "org" {
		t.Errorf("task.RepoOwner = %s, want 'org'", task.RepoOwner)
	}

	if task.IssueNumber != 42 {
		t.Errorf("task.IssueNumber = %d, want 42", task.IssueNumber)
	}

	// Labels should exclude pilot and priority labels
	if len(task.Labels) != 1 || task.Labels[0] != "enhancement" {
		t.Errorf("task.Labels = %v, want [enhancement]", task.Labels)
	}
}

func TestExtractPriority(t *testing.T) {
	tests := []struct {
		name   string
		labels []Label
		want   Priority
	}{
		{
			name:   "urgent priority",
			labels: []Label{{Name: "priority:urgent"}},
			want:   PriorityUrgent,
		},
		{
			name:   "P0 label",
			labels: []Label{{Name: "P0"}},
			want:   PriorityUrgent,
		},
		{
			name:   "high priority",
			labels: []Label{{Name: "priority:high"}},
			want:   PriorityHigh,
		},
		{
			name:   "P1 label",
			labels: []Label{{Name: "P1"}},
			want:   PriorityHigh,
		},
		{
			name:   "medium priority",
			labels: []Label{{Name: "priority:medium"}},
			want:   PriorityMedium,
		},
		{
			name:   "low priority",
			labels: []Label{{Name: "P3"}},
			want:   PriorityLow,
		},
		{
			name:   "no priority labels",
			labels: []Label{{Name: "bug"}, {Name: "enhancement"}},
			want:   PriorityNone,
		},
		{
			name:   "empty labels",
			labels: []Label{},
			want:   PriorityNone,
		},
		{
			name:   "critical maps to urgent",
			labels: []Label{{Name: "critical"}},
			want:   PriorityUrgent,
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

func TestExtractAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "with acceptance criteria section",
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
				t.Errorf("ExtractAcceptanceCriteria() returned %d items, want %d", len(got), len(tt.want))
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

func TestExtractLabelNames(t *testing.T) {
	labels := []Label{
		{Name: "pilot"},
		{Name: "pilot-in-progress"},
		{Name: "priority:high"},
		{Name: "P1"},
		{Name: "bug"},
		{Name: "enhancement"},
	}

	got := extractLabelNames(labels)

	// Should only include bug and enhancement
	if len(got) != 2 {
		t.Errorf("extractLabelNames() returned %d labels, want 2", len(got))
	}

	for _, name := range got {
		if name != "bug" && name != "enhancement" {
			t.Errorf("unexpected label: %s", name)
		}
	}
}

func TestBuildTaskPrompt(t *testing.T) {
	task := &TaskInfo{
		ID:          "GH-42",
		Title:       "Add authentication",
		Description: "Implement OAuth login.\n\n### Acceptance Criteria\n- [ ] User can login",
		Priority:    PriorityHigh,
		IssueURL:    "https://github.com/org/repo/issues/42",
	}

	prompt := BuildTaskPrompt(task)

	// Check key elements are present
	if !strings.Contains(prompt, "# Task: Add authentication") {
		t.Error("prompt missing task title")
	}

	if !strings.Contains(prompt, "**Issue**: https://github.com/org/repo/issues/42") {
		t.Error("prompt missing issue URL")
	}

	if !strings.Contains(prompt, "**Priority**: High") {
		t.Error("prompt missing priority")
	}

	if !strings.Contains(prompt, "Implement OAuth login") {
		t.Error("prompt missing description")
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

func TestExtractDescription(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "removes checklist section",
			body: `Feature description here.

### Checklist
- [ ] I read the docs
- [ ] I agree to terms

### Notes
More content here.`,
			want: "Feature description here.\n\n### Notes\nMore content here.",
		},
		{
			name: "removes environment section",
			body: `Bug description.

### Environment
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
