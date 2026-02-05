package executor

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSubtasks(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected []PlannedSubtask
	}{
		{
			name: "numbered list with dash separator",
			output: `Here's the plan:

1. **Set up database schema** - Create the initial tables for users and sessions
2. **Implement authentication service** - Build the core auth logic with JWT tokens
3. **Add API endpoints** - Create REST endpoints for login and logout`,
			expected: []PlannedSubtask{
				{Title: "Set up database schema", Description: "Create the initial tables for users and sessions", Order: 1},
				{Title: "Implement authentication service", Description: "Build the core auth logic with JWT tokens", Order: 2},
				{Title: "Add API endpoints", Description: "Create REST endpoints for login and logout", Order: 3},
			},
		},
		{
			name: "numbered list with colon separator",
			output: `Plan:
1. Setup infrastructure: Install dependencies and configure environment
2. Create models: Define data structures for the feature
3. Write tests: Add unit tests for the new functionality`,
			expected: []PlannedSubtask{
				{Title: "Setup infrastructure", Description: "Install dependencies and configure environment", Order: 1},
				{Title: "Create models", Description: "Define data structures for the feature", Order: 2},
				{Title: "Write tests", Description: "Add unit tests for the new functionality", Order: 3},
			},
		},
		{
			name: "step prefix pattern",
			output: `Breaking this down:
Step 1: Initialize project structure
Step 2: Add core functionality
Step 3: Integrate with existing system`,
			expected: []PlannedSubtask{
				{Title: "Initialize project structure", Description: "", Order: 1},
				{Title: "Add core functionality", Description: "", Order: 2},
				{Title: "Integrate with existing system", Description: "", Order: 3},
			},
		},
		{
			name: "parenthesis numbered list",
			output: `Implementation plan:
1) Create the database migration
2) Implement repository layer
3) Add service methods`,
			expected: []PlannedSubtask{
				{Title: "Create the database migration", Description: "", Order: 1},
				{Title: "Implement repository layer", Description: "", Order: 2},
				{Title: "Add service methods", Description: "", Order: 3},
			},
		},
		{
			name: "multiline descriptions",
			output: `1. **First task** - Initial description
   Additional context for first task
2. **Second task** - Main work
   More details about second task
   Even more details`,
			expected: []PlannedSubtask{
				{Title: "First task", Description: "Initial description\nAdditional context for first task", Order: 1},
				{Title: "Second task", Description: "Main work\nMore details about second task\nEven more details", Order: 2},
			},
		},
		{
			name:     "empty output",
			output:   "",
			expected: nil,
		},
		{
			name: "no numbered items",
			output: `Some random text
without any numbered items
just plain paragraphs`,
			expected: nil,
		},
		{
			name: "single item",
			output: `1. The only task - Do everything in one go`,
			expected: []PlannedSubtask{
				{Title: "The only task", Description: "Do everything in one go", Order: 1},
			},
		},
		{
			name: "bold-wrapped numbers from Claude output",
			output: `Based on the codebase analysis, here are the subtasks:

**1. Add parent_task_id to database schema** - Create migration and update store
**2. Wire parent context through dispatcher** - Pass parent ID to sub-issues
**3. Update dashboard rendering** - Group sub-issues under parent in history`,
			expected: []PlannedSubtask{
				{Title: "Add parent_task_id to database schema", Description: "Create migration and update store", Order: 1},
				{Title: "Wire parent context through dispatcher", Description: "Pass parent ID to sub-issues", Order: 2},
				{Title: "Update dashboard rendering", Description: "Group sub-issues under parent in history", Order: 3},
			},
		},
		{
			name: "duplicate order numbers filtered",
			output: `1. First task - Description
1. Duplicate first - Should be ignored
2. Second task - Description`,
			expected: []PlannedSubtask{
				{Title: "First task", Description: "Description", Order: 1},
				{Title: "Second task", Description: "Description", Order: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseSubtasks(tt.output)

			if tt.expected == nil {
				if len(result) != 0 {
					t.Errorf("expected empty result, got %d subtasks", len(result))
				}
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("expected %d subtasks, got %d", len(tt.expected), len(result))
				for i, s := range result {
					t.Logf("  subtask %d: %+v", i, s)
				}
				return
			}

			for i, expected := range tt.expected {
				actual := result[i]
				if actual.Title != expected.Title {
					t.Errorf("subtask %d: title = %q, want %q", i, actual.Title, expected.Title)
				}
				if actual.Description != expected.Description {
					t.Errorf("subtask %d: description = %q, want %q", i, actual.Description, expected.Description)
				}
				if actual.Order != expected.Order {
					t.Errorf("subtask %d: order = %d, want %d", i, actual.Order, expected.Order)
				}
			}
		})
	}
}

func TestSplitTitleDescription(t *testing.T) {
	tests := []struct {
		input       string
		wantTitle   string
		wantDesc    string
	}{
		{"**Title** - Description", "Title", "Description"},
		{"Title - Description", "Title", "Description"},
		{"Title: Description", "Title", "Description"},
		{"Title – Description", "Title", "Description"}, // em dash
		{"Just a title", "Just a title", ""},
		{"**Bold title**", "Bold title", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			title, desc := splitTitleDescription(tt.input)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
			if desc != tt.wantDesc {
				t.Errorf("description = %q, want %q", desc, tt.wantDesc)
			}
		})
	}
}

func TestBuildPlanningPrompt(t *testing.T) {
	task := &Task{
		ID:          "TASK-123",
		Title:       "Implement user authentication",
		Description: "Add login, logout, and session management",
	}

	prompt := buildPlanningPrompt(task)

	// Check required elements are present
	required := []string{
		"software architect",
		"3-5 sequential subtasks",
		"Implement user authentication",
		"Add login, logout, and session management",
		"Output Format",
	}

	for _, r := range required {
		if !strings.Contains(prompt, r) {
			t.Errorf("prompt missing required element: %q", r)
		}
	}
}

func TestEpicPlanTypes(t *testing.T) {
	// Verify types are properly constructed
	task := &Task{ID: "TASK-1", Title: "Epic task"}
	subtasks := []PlannedSubtask{
		{Title: "First", Description: "Do first thing", Order: 1},
		{Title: "Second", Description: "Do second thing", Order: 2, DependsOn: []int{1}},
	}

	plan := &EpicPlan{
		ParentTask:  task,
		Subtasks:    subtasks,
		TotalEffort: "2 days",
		PlanOutput:  "raw output",
	}

	if plan.ParentTask.ID != "TASK-1" {
		t.Errorf("ParentTask.ID = %q, want %q", plan.ParentTask.ID, "TASK-1")
	}
	if len(plan.Subtasks) != 2 {
		t.Errorf("len(Subtasks) = %d, want 2", len(plan.Subtasks))
	}
	if !reflect.DeepEqual(plan.Subtasks[1].DependsOn, []int{1}) {
		t.Errorf("Subtasks[1].DependsOn = %v, want [1]", plan.Subtasks[1].DependsOn)
	}
}

func TestExecuteEpicTriggersPlanningMode(t *testing.T) {
	// Test that epic complexity triggers planning mode
	task := &Task{
		ID:          "TASK-EPIC",
		Title:       "[epic] Major refactoring",
		Description: "This is a large epic task with multiple phases",
	}

	complexity := DetectComplexity(task)
	if !complexity.IsEpic() {
		t.Error("expected epic complexity to be detected")
	}
}

func TestParseIssueNumber(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected int
	}{
		{
			name:     "standard github issue url",
			url:      "https://github.com/anthropics/pilot/issues/123",
			expected: 123,
		},
		{
			name:     "github enterprise url",
			url:      "https://github.example.com/org/repo/issues/456",
			expected: 456,
		},
		{
			name:     "url with trailing newline",
			url:      "https://github.com/owner/repo/issues/789\n",
			expected: 789,
		},
		{
			name:     "large issue number",
			url:      "https://github.com/owner/repo/issues/99999",
			expected: 99999,
		},
		{
			name:     "empty string",
			url:      "",
			expected: 0,
		},
		{
			name:     "invalid url - no issues path",
			url:      "https://github.com/owner/repo/pull/123",
			expected: 0,
		},
		{
			name:     "invalid url - no number",
			url:      "https://github.com/owner/repo/issues/",
			expected: 0,
		},
		{
			name:     "plain text",
			url:      "not a url at all",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseIssueNumber(tt.url)
			if result != tt.expected {
				t.Errorf("parseIssueNumber(%q) = %d, want %d", tt.url, result, tt.expected)
			}
		})
	}
}

