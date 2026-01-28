package orchestrator

import (
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/linear"
)

func TestExtractLabelNames(t *testing.T) {
	tests := []struct {
		name   string
		labels []linear.Label
		want   []string
	}{
		{
			name:   "empty labels",
			labels: []linear.Label{},
			want:   []string{},
		},
		{
			name:   "nil labels",
			labels: nil,
			want:   []string{},
		},
		{
			name: "single label",
			labels: []linear.Label{
				{ID: "1", Name: "bug"},
			},
			want: []string{"bug"},
		},
		{
			name: "multiple labels",
			labels: []linear.Label{
				{ID: "1", Name: "bug"},
				{ID: "2", Name: "priority-high"},
				{ID: "3", Name: "backend"},
			},
			want: []string{"bug", "priority-high", "backend"},
		},
		{
			name: "labels with empty name",
			labels: []linear.Label{
				{ID: "1", Name: ""},
				{ID: "2", Name: "feature"},
			},
			want: []string{"", "feature"},
		},
		{
			name: "labels with special characters",
			labels: []linear.Label{
				{ID: "1", Name: "pilot:active"},
				{ID: "2", Name: "tech-debt"},
				{ID: "3", Name: "enhancement (v2)"},
			},
			want: []string{"pilot:active", "tech-debt", "enhancement (v2)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLabelNames(tt.labels)

			if len(got) != len(tt.want) {
				t.Errorf("extractLabelNames() returned %d items, want %d", len(got), len(tt.want))
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

func TestTaskStruct(t *testing.T) {
	tests := []struct {
		name string
		task *Task
		want struct {
			id          string
			projectPath string
			branch      string
			priority    float64
		}
	}{
		{
			name: "basic task creation",
			task: &Task{
				ID:          "TASK-001",
				ProjectPath: "/home/user/project",
				Branch:      "pilot/TASK-001",
				Priority:    1.0,
			},
			want: struct {
				id          string
				projectPath string
				branch      string
				priority    float64
			}{
				id:          "TASK-001",
				projectPath: "/home/user/project",
				branch:      "pilot/TASK-001",
				priority:    1.0,
			},
		},
		{
			name: "task with zero priority",
			task: &Task{
				ID:          "TASK-002",
				ProjectPath: "/var/projects/app",
				Branch:      "pilot/ABC-123",
				Priority:    0.0,
			},
			want: struct {
				id          string
				projectPath string
				branch      string
				priority    float64
			}{
				id:          "TASK-002",
				projectPath: "/var/projects/app",
				branch:      "pilot/ABC-123",
				priority:    0.0,
			},
		},
		{
			name: "task with high priority",
			task: &Task{
				ID:          "URGENT-001",
				ProjectPath: "/projects/critical",
				Branch:      "pilot/URGENT-001",
				Priority:    10.5,
			},
			want: struct {
				id          string
				projectPath string
				branch      string
				priority    float64
			}{
				id:          "URGENT-001",
				projectPath: "/projects/critical",
				branch:      "pilot/URGENT-001",
				priority:    10.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.task.ID != tt.want.id {
				t.Errorf("Task.ID = %s, want %s", tt.task.ID, tt.want.id)
			}
			if tt.task.ProjectPath != tt.want.projectPath {
				t.Errorf("Task.ProjectPath = %s, want %s", tt.task.ProjectPath, tt.want.projectPath)
			}
			if tt.task.Branch != tt.want.branch {
				t.Errorf("Task.Branch = %s, want %s", tt.task.Branch, tt.want.branch)
			}
			if tt.task.Priority != tt.want.priority {
				t.Errorf("Task.Priority = %f, want %f", tt.task.Priority, tt.want.priority)
			}
		})
	}
}

func TestTaskWithLinearIssue(t *testing.T) {
	issue := &linear.Issue{
		ID:          "issue-uuid-123",
		Identifier:  "ABC-42",
		Title:       "Implement feature X",
		Description: "Detailed description of feature X",
		Priority:    2,
		Labels: []linear.Label{
			{ID: "l1", Name: "enhancement"},
			{ID: "l2", Name: "backend"},
		},
	}

	doc := &TaskDocument{
		ID:       "TASK-ABC-42",
		Title:    "Implement feature X",
		Markdown: "# Task: Implement feature X\n\nDescription here.",
	}

	task := &Task{
		ID:          doc.ID,
		Ticket:      issue,
		Document:    doc,
		ProjectPath: "/path/to/project",
		Branch:      "pilot/ABC-42",
		Priority:    float64(issue.Priority),
	}

	if task.ID != "TASK-ABC-42" {
		t.Errorf("Task.ID = %s, want TASK-ABC-42", task.ID)
	}

	if task.Ticket.Title != "Implement feature X" {
		t.Errorf("Task.Ticket.Title = %s, want 'Implement feature X'", task.Ticket.Title)
	}

	if task.Document.Markdown != "# Task: Implement feature X\n\nDescription here." {
		t.Errorf("Task.Document.Markdown mismatch")
	}

	if task.Branch != "pilot/ABC-42" {
		t.Errorf("Task.Branch = %s, want pilot/ABC-42", task.Branch)
	}
}

func TestConfigDefaults(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		want   struct {
			model         string
			maxConcurrent int
		}
	}{
		{
			name:   "empty config",
			config: &Config{},
			want: struct {
				model         string
				maxConcurrent int
			}{
				model:         "",
				maxConcurrent: 0,
			},
		},
		{
			name: "config with values",
			config: &Config{
				Model:         "claude-3",
				MaxConcurrent: 4,
			},
			want: struct {
				model         string
				maxConcurrent int
			}{
				model:         "claude-3",
				maxConcurrent: 4,
			},
		},
		{
			name: "config with only model",
			config: &Config{
				Model: "gpt-4",
			},
			want: struct {
				model         string
				maxConcurrent int
			}{
				model:         "gpt-4",
				maxConcurrent: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Model != tt.want.model {
				t.Errorf("Config.Model = %s, want %s", tt.config.Model, tt.want.model)
			}
			if tt.config.MaxConcurrent != tt.want.maxConcurrent {
				t.Errorf("Config.MaxConcurrent = %d, want %d", tt.config.MaxConcurrent, tt.want.maxConcurrent)
			}
		})
	}
}

func TestTicketDataConstruction(t *testing.T) {
	tests := []struct {
		name   string
		ticket *TicketData
		want   struct {
			id          string
			identifier  string
			title       string
			description string
			priority    int
			labelCount  int
		}
	}{
		{
			name: "basic ticket",
			ticket: &TicketData{
				ID:          "uuid-123",
				Identifier:  "PROJ-1",
				Title:       "Fix bug",
				Description: "Bug description",
				Priority:    3,
				Labels:      []string{"bug"},
			},
			want: struct {
				id          string
				identifier  string
				title       string
				description string
				priority    int
				labelCount  int
			}{
				id:          "uuid-123",
				identifier:  "PROJ-1",
				title:       "Fix bug",
				description: "Bug description",
				priority:    3,
				labelCount:  1,
			},
		},
		{
			name: "ticket without labels",
			ticket: &TicketData{
				ID:          "uuid-456",
				Identifier:  "PROJ-2",
				Title:       "Add feature",
				Description: "Feature description",
				Priority:    1,
				Labels:      []string{},
			},
			want: struct {
				id          string
				identifier  string
				title       string
				description string
				priority    int
				labelCount  int
			}{
				id:          "uuid-456",
				identifier:  "PROJ-2",
				title:       "Add feature",
				description: "Feature description",
				priority:    1,
				labelCount:  0,
			},
		},
		{
			name: "ticket with multiple labels",
			ticket: &TicketData{
				ID:          "uuid-789",
				Identifier:  "PROJ-3",
				Title:       "Refactor code",
				Description: "Cleanup and refactoring",
				Priority:    4,
				Labels:      []string{"refactor", "tech-debt", "backend", "low-priority"},
			},
			want: struct {
				id          string
				identifier  string
				title       string
				description string
				priority    int
				labelCount  int
			}{
				id:          "uuid-789",
				identifier:  "PROJ-3",
				title:       "Refactor code",
				description: "Cleanup and refactoring",
				priority:    4,
				labelCount:  4,
			},
		},
		{
			name: "ticket with optional fields",
			ticket: &TicketData{
				ID:          "uuid-abc",
				Identifier:  "PROJ-4",
				Title:       "Project task",
				Description: "With project and assignee",
				Priority:    2,
				Labels:      []string{"feature"},
				Project:     "Main Project",
				Assignee:    "developer@example.com",
			},
			want: struct {
				id          string
				identifier  string
				title       string
				description string
				priority    int
				labelCount  int
			}{
				id:          "uuid-abc",
				identifier:  "PROJ-4",
				title:       "Project task",
				description: "With project and assignee",
				priority:    2,
				labelCount:  1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ticket.ID != tt.want.id {
				t.Errorf("TicketData.ID = %s, want %s", tt.ticket.ID, tt.want.id)
			}
			if tt.ticket.Identifier != tt.want.identifier {
				t.Errorf("TicketData.Identifier = %s, want %s", tt.ticket.Identifier, tt.want.identifier)
			}
			if tt.ticket.Title != tt.want.title {
				t.Errorf("TicketData.Title = %s, want %s", tt.ticket.Title, tt.want.title)
			}
			if tt.ticket.Description != tt.want.description {
				t.Errorf("TicketData.Description = %s, want %s", tt.ticket.Description, tt.want.description)
			}
			if tt.ticket.Priority != tt.want.priority {
				t.Errorf("TicketData.Priority = %d, want %d", tt.ticket.Priority, tt.want.priority)
			}
			if len(tt.ticket.Labels) != tt.want.labelCount {
				t.Errorf("len(TicketData.Labels) = %d, want %d", len(tt.ticket.Labels), tt.want.labelCount)
			}
		})
	}
}

func TestTaskDocumentConstruction(t *testing.T) {
	tests := []struct {
		name string
		doc  *TaskDocument
		want struct {
			id       string
			title    string
			markdown string
		}
	}{
		{
			name: "basic document",
			doc: &TaskDocument{
				ID:       "TASK-001",
				Title:    "Implement feature",
				Markdown: "# Task\n\n## Description\n\nImplement feature.",
			},
			want: struct {
				id       string
				title    string
				markdown string
			}{
				id:       "TASK-001",
				title:    "Implement feature",
				markdown: "# Task\n\n## Description\n\nImplement feature.",
			},
		},
		{
			name: "document with empty markdown",
			doc: &TaskDocument{
				ID:       "TASK-002",
				Title:    "Empty task",
				Markdown: "",
			},
			want: struct {
				id       string
				title    string
				markdown string
			}{
				id:       "TASK-002",
				title:    "Empty task",
				markdown: "",
			},
		},
		{
			name: "document with complex markdown",
			doc: &TaskDocument{
				ID:    "TASK-003",
				Title: "Complex task",
				Markdown: `# Task: Complex task

## Overview
This is a complex task with multiple sections.

## Implementation Steps
1. Step one
2. Step two
3. Step three

## Acceptance Criteria
- [ ] Criterion 1
- [ ] Criterion 2

## Notes
Additional notes here.`,
			},
			want: struct {
				id       string
				title    string
				markdown string
			}{
				id:    "TASK-003",
				title: "Complex task",
				markdown: `# Task: Complex task

## Overview
This is a complex task with multiple sections.

## Implementation Steps
1. Step one
2. Step two
3. Step three

## Acceptance Criteria
- [ ] Criterion 1
- [ ] Criterion 2

## Notes
Additional notes here.`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.doc.ID != tt.want.id {
				t.Errorf("TaskDocument.ID = %s, want %s", tt.doc.ID, tt.want.id)
			}
			if tt.doc.Title != tt.want.title {
				t.Errorf("TaskDocument.Title = %s, want %s", tt.doc.Title, tt.want.title)
			}
			if tt.doc.Markdown != tt.want.markdown {
				t.Errorf("TaskDocument.Markdown mismatch:\ngot:\n%s\nwant:\n%s", tt.doc.Markdown, tt.want.markdown)
			}
		})
	}
}

func TestScoredTaskConstruction(t *testing.T) {
	tests := []struct {
		name string
		task ScoredTask
		want struct {
			taskID      string
			title       string
			rawPriority int
			score       float64
			factorCount int
		}
	}{
		{
			name: "basic scored task",
			task: ScoredTask{
				TaskID:      "TASK-001",
				Title:       "High priority task",
				RawPriority: 1,
				Score:       95.5,
				Factors: map[string]float64{
					"priority": 50.0,
					"urgency":  45.5,
				},
			},
			want: struct {
				taskID      string
				title       string
				rawPriority int
				score       float64
				factorCount int
			}{
				taskID:      "TASK-001",
				title:       "High priority task",
				rawPriority: 1,
				score:       95.5,
				factorCount: 2,
			},
		},
		{
			name: "task with no factors",
			task: ScoredTask{
				TaskID:      "TASK-002",
				Title:       "Low priority task",
				RawPriority: 4,
				Score:       10.0,
				Factors:     map[string]float64{},
			},
			want: struct {
				taskID      string
				title       string
				rawPriority int
				score       float64
				factorCount int
			}{
				taskID:      "TASK-002",
				title:       "Low priority task",
				rawPriority: 4,
				score:       10.0,
				factorCount: 0,
			},
		},
		{
			name: "task with multiple factors",
			task: ScoredTask{
				TaskID:      "TASK-003",
				Title:       "Complex scoring",
				RawPriority: 2,
				Score:       75.25,
				Factors: map[string]float64{
					"priority":   30.0,
					"complexity": 20.0,
					"impact":     15.25,
					"deadline":   10.0,
				},
			},
			want: struct {
				taskID      string
				title       string
				rawPriority int
				score       float64
				factorCount int
			}{
				taskID:      "TASK-003",
				title:       "Complex scoring",
				rawPriority: 2,
				score:       75.25,
				factorCount: 4,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.task.TaskID != tt.want.taskID {
				t.Errorf("ScoredTask.TaskID = %s, want %s", tt.task.TaskID, tt.want.taskID)
			}
			if tt.task.Title != tt.want.title {
				t.Errorf("ScoredTask.Title = %s, want %s", tt.task.Title, tt.want.title)
			}
			if tt.task.RawPriority != tt.want.rawPriority {
				t.Errorf("ScoredTask.RawPriority = %d, want %d", tt.task.RawPriority, tt.want.rawPriority)
			}
			if tt.task.Score != tt.want.score {
				t.Errorf("ScoredTask.Score = %f, want %f", tt.task.Score, tt.want.score)
			}
			if len(tt.task.Factors) != tt.want.factorCount {
				t.Errorf("len(ScoredTask.Factors) = %d, want %d", len(tt.task.Factors), tt.want.factorCount)
			}
		})
	}
}

func TestExtractLabelNamesOrderPreservation(t *testing.T) {
	// Test that label order is preserved
	labels := []linear.Label{
		{ID: "1", Name: "first"},
		{ID: "2", Name: "second"},
		{ID: "3", Name: "third"},
		{ID: "4", Name: "fourth"},
		{ID: "5", Name: "fifth"},
	}

	got := extractLabelNames(labels)

	expected := []string{"first", "second", "third", "fourth", "fifth"}
	for i, name := range got {
		if name != expected[i] {
			t.Errorf("Label order not preserved at index %d: got %q, want %q", i, name, expected[i])
		}
	}
}

func TestExtractLabelNamesLargeInput(t *testing.T) {
	// Test with a larger number of labels
	labelCount := 100
	labels := make([]linear.Label, labelCount)
	for i := 0; i < labelCount; i++ {
		labels[i] = linear.Label{
			ID:   "id-" + string(rune('a'+i%26)),
			Name: "label-" + string(rune('a'+i%26)),
		}
	}

	got := extractLabelNames(labels)

	if len(got) != labelCount {
		t.Errorf("extractLabelNames() returned %d items, want %d", len(got), labelCount)
	}

	// Verify first and last elements
	if got[0] != "label-a" {
		t.Errorf("First label = %s, want label-a", got[0])
	}
	if got[labelCount-1] != "label-"+string(rune('a'+(labelCount-1)%26)) {
		t.Errorf("Last label mismatch")
	}
}
