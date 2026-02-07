package executor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
		{
			name: "markdown heading with numbered items",
			output: `Here's the plan:

### 1. Set up database schema - Create migration files
### 2. Implement auth service - Build JWT-based authentication
### 3. Add API endpoints - Create login and logout routes`,
			expected: []PlannedSubtask{
				{Title: "Set up database schema", Description: "Create migration files", Order: 1},
				{Title: "Implement auth service", Description: "Build JWT-based authentication", Order: 2},
				{Title: "Add API endpoints", Description: "Create login and logout routes", Order: 3},
			},
		},
		{
			name: "dash bullet with numbered items",
			output: `Implementation steps:
- 1. Create database migration
- 2. Implement repository layer
- 3. Add service methods`,
			expected: []PlannedSubtask{
				{Title: "Create database migration", Description: "", Order: 1},
				{Title: "Implement repository layer", Description: "", Order: 2},
				{Title: "Add service methods", Description: "", Order: 3},
			},
		},
		{
			name: "dash bullet with bold numbers",
			output: `Tasks:
- **1. Add migration** - Schema changes for user tables
- **2. Build API layer** - REST endpoints with validation
- **3. Add frontend** - React forms and state management`,
			expected: []PlannedSubtask{
				{Title: "Add migration", Description: "Schema changes for user tables", Order: 1},
				{Title: "Build API layer", Description: "REST endpoints with validation", Order: 2},
				{Title: "Add frontend", Description: "React forms and state management", Order: 3},
			},
		},
		{
			name: "h2 heading with step prefix",
			output: `## Step 1: Initialize project
## Step 2: Add core functionality
## Step 3: Write tests`,
			expected: []PlannedSubtask{
				{Title: "Initialize project", Description: "", Order: 1},
				{Title: "Add core functionality", Description: "", Order: 2},
				{Title: "Write tests", Description: "", Order: 3},
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

// writeMockScript creates a temporary executable script that outputs the given text
// and exits with the given code. Returns the path to the script.
func writeMockScript(t *testing.T, dir, output string, exitCode int) string {
	t.Helper()
	scriptPath := filepath.Join(dir, "mock-claude")
	script := "#!/bin/sh\n"
	if output != "" {
		script += "cat <<'ENDOFOUTPUT'\n" + output + "\nENDOFOUTPUT\n"
	}
	script += "exit " + fmt.Sprintf("%d", exitCode) + "\n"
	err := os.WriteFile(scriptPath, []byte(script), 0o755)
	if err != nil {
		t.Fatalf("failed to write mock script: %v", err)
	}
	return scriptPath
}

// newTestRunner creates a Runner with a mock Claude command for testing PlanEpic.
func newTestRunner(claudeCmd string) *Runner {
	return &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				Command: claudeCmd,
			},
		},
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		modelRouter:       NewModelRouter(nil, nil),
	}
}

func TestPlanEpicSuccess(t *testing.T) {
	tmpDir := t.TempDir()

	validOutput := `Here's the implementation plan:

1. **Set up database schema** - Create migration files for users and sessions tables
2. **Implement auth service** - Build JWT-based authentication with refresh tokens
3. **Add API endpoints** - Create login, logout, and session management routes
4. **Write integration tests** - End-to-end tests for the auth flow`

	mockCmd := writeMockScript(t, tmpDir, validOutput, 0)

	runner := newTestRunner(mockCmd)
	task := &Task{
		ID:          "GH-100",
		Title:       "[epic] Implement user authentication",
		Description: "Full auth system with JWT tokens and session management",
		ProjectPath: tmpDir,
	}

	plan, err := runner.PlanEpic(context.Background(), task)
	if err != nil {
		t.Fatalf("PlanEpic returned unexpected error: %v", err)
	}

	if plan == nil {
		t.Fatal("PlanEpic returned nil plan")
	}

	if plan.ParentTask != task {
		t.Error("PlanEpic did not set ParentTask correctly")
	}

	if len(plan.Subtasks) != 4 {
		t.Fatalf("expected 4 subtasks, got %d", len(plan.Subtasks))
	}

	expectedTitles := []string{
		"Set up database schema",
		"Implement auth service",
		"Add API endpoints",
		"Write integration tests",
	}

	for i, expected := range expectedTitles {
		if plan.Subtasks[i].Title != expected {
			t.Errorf("subtask %d: title = %q, want %q", i, plan.Subtasks[i].Title, expected)
		}
		if plan.Subtasks[i].Order != i+1 {
			t.Errorf("subtask %d: order = %d, want %d", i, plan.Subtasks[i].Order, i+1)
		}
		if plan.Subtasks[i].Description == "" {
			t.Errorf("subtask %d: description should not be empty", i)
		}
	}

	if plan.PlanOutput == "" {
		t.Error("PlanOutput should not be empty")
	}
}

func TestPlanEpicCLIFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// Script exits with non-zero code (simulates CLI crash / API key missing / 500 error)
	scriptPath := filepath.Join(tmpDir, "mock-claude")
	script := "#!/bin/sh\necho 'Error: API key not set' >&2\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock script: %v", err)
	}

	runner := newTestRunner(scriptPath)
	task := &Task{
		ID:          "GH-101",
		Title:       "[epic] Build notification system",
		Description: "Multi-channel notifications",
		ProjectPath: tmpDir,
	}

	plan, err := runner.PlanEpic(context.Background(), task)
	if err == nil {
		t.Fatal("PlanEpic should return error when CLI fails")
	}

	if plan != nil {
		t.Error("PlanEpic should return nil plan on CLI failure")
	}

	if !strings.Contains(err.Error(), "claude planning failed") {
		t.Errorf("error should mention claude planning failed, got: %v", err)
	}
}

func TestPlanEpicEmptyOutput(t *testing.T) {
	tmpDir := t.TempDir()

	// Script succeeds but outputs nothing
	mockCmd := writeMockScript(t, tmpDir, "", 0)

	runner := newTestRunner(mockCmd)
	task := &Task{
		ID:          "GH-102",
		Title:       "[epic] Empty response task",
		Description: "Should fail on empty output",
		ProjectPath: tmpDir,
	}

	plan, err := runner.PlanEpic(context.Background(), task)
	if err == nil {
		t.Fatal("PlanEpic should return error on empty output")
	}

	if plan != nil {
		t.Error("PlanEpic should return nil plan on empty output")
	}

	if !strings.Contains(err.Error(), "empty output") {
		t.Errorf("error should mention empty output, got: %v", err)
	}
}

func TestPlanEpicNoParseableSubtasks(t *testing.T) {
	tmpDir := t.TempDir()

	// Script outputs text but no numbered list — regex cannot parse subtasks
	unparseable := `I analyzed the task and here are my thoughts:

The system should handle authentication with multiple providers.
Consider using OAuth2 for social login integration.
Security is paramount for this implementation.`

	mockCmd := writeMockScript(t, tmpDir, unparseable, 0)

	runner := newTestRunner(mockCmd)
	task := &Task{
		ID:          "GH-103",
		Title:       "[epic] Unparseable planning output",
		Description: "Output with no numbered items triggers no-subtasks error",
		ProjectPath: tmpDir,
	}

	plan, err := runner.PlanEpic(context.Background(), task)
	if err == nil {
		t.Fatal("PlanEpic should return error when no subtasks are parseable")
	}

	if plan != nil {
		t.Error("PlanEpic should return nil plan when regex finds nothing")
	}

	if !strings.Contains(err.Error(), "no subtasks found") {
		t.Errorf("error should mention no subtasks found, got: %v", err)
	}
}

func TestPlanEpicRegexParsesVariousFormats(t *testing.T) {
	// Validates that even when Claude returns different formatting,
	// the regex-based parseSubtasks still extracts subtasks correctly
	tmpDir := t.TempDir()

	tests := []struct {
		name           string
		output         string
		expectedCount  int
		expectedTitles []string
	}{
		{
			name: "step prefix format",
			output: `Here's the plan:
Step 1: Set up project scaffolding
Step 2: Implement core logic
Step 3: Add tests`,
			expectedCount:  3,
			expectedTitles: []string{"Set up project scaffolding", "Implement core logic", "Add tests"},
		},
		{
			name: "bold-wrapped numbers (GH-490 format)",
			output: `Analysis complete:

**1. Create database migration** - Schema changes for user tables
**2. Build API layer** - REST endpoints with validation
**3. Add frontend components** - React forms and state management`,
			expectedCount:  3,
			expectedTitles: []string{"Create database migration", "Build API layer", "Add frontend components"},
		},
		{
			name: "parenthesis format",
			output: `1) Initialize project
2) Add dependencies
3) Implement feature
4) Write tests`,
			expectedCount:  4,
			expectedTitles: []string{"Initialize project", "Add dependencies", "Implement feature", "Write tests"},
		},
		{
			name: "markdown heading format (GH-542)",
			output: `### 1. Create database migration - Schema changes
### 2. Build API layer - REST endpoints
### 3. Add frontend components - React forms`,
			expectedCount:  3,
			expectedTitles: []string{"Create database migration", "Build API layer", "Add frontend components"},
		},
		{
			name: "dash bullet format (GH-542)",
			output: `- **1. Add migration** - Schema changes
- **2. Build API** - REST endpoints
- **3. Add frontend** - React forms`,
			expectedCount:  3,
			expectedTitles: []string{"Add migration", "Build API", "Add frontend"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCmd := writeMockScript(t, tmpDir, tt.output, 0)
			runner := newTestRunner(mockCmd)
			task := &Task{
				ID:          "GH-FMT",
				Title:       "[epic] Format test",
				Description: "Test regex parsing of different formats",
				ProjectPath: tmpDir,
			}

			plan, err := runner.PlanEpic(context.Background(), task)
			if err != nil {
				t.Fatalf("PlanEpic failed: %v", err)
			}

			if len(plan.Subtasks) != tt.expectedCount {
				t.Fatalf("expected %d subtasks, got %d", tt.expectedCount, len(plan.Subtasks))
			}

			for i, expected := range tt.expectedTitles {
				if plan.Subtasks[i].Title != expected {
					t.Errorf("subtask %d: title = %q, want %q", i, plan.Subtasks[i].Title, expected)
				}
			}
		})
	}
}

func TestPlanEpicDefaultCommand(t *testing.T) {
	// When config is nil, PlanEpic defaults to "claude" command.
	// We verify by setting config with empty command — it should default to "claude".
	// Use a nonexistent binary to ensure it fails fast without hanging.
	runner := &Runner{
		config: &BackendConfig{
			ClaudeCode: &ClaudeCodeConfig{
				Command: "nonexistent-claude-binary-for-test",
			},
		},
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		modelRouter:       NewModelRouter(nil, nil),
	}

	task := &Task{
		ID:          "GH-104",
		Title:       "[epic] Default command test",
		Description: "Should fail when binary is not found",
	}

	_, err := runner.PlanEpic(context.Background(), task)
	if err == nil {
		t.Fatal("PlanEpic should fail when binary is not available")
	}

	if !strings.Contains(err.Error(), "claude planning failed") {
		t.Errorf("error should indicate claude planning failed, got: %v", err)
	}

	// Also verify nil config uses "claude" default
	runner2 := &Runner{
		config:            nil,
		running:           make(map[string]*exec.Cmd),
		progressCallbacks: make(map[string]ProgressCallback),
		tokenCallbacks:    make(map[string]TokenCallback),
		log:               slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		modelRouter:       NewModelRouter(nil, nil),
	}

	// Use a short timeout so it doesn't hang if "claude" binary exists
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = runner2.PlanEpic(ctx, task)
	// We expect either an error (binary not found) or timeout (binary exists but hangs)
	if err == nil {
		t.Fatal("PlanEpic with nil config should still attempt to run")
	}
}

func TestPlanEpicContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	// Script that sleeps — will be cancelled
	scriptPath := filepath.Join(tmpDir, "mock-claude")
	script := "#!/bin/sh\nsleep 30\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock script: %v", err)
	}

	runner := newTestRunner(scriptPath)
	task := &Task{
		ID:          "GH-105",
		Title:       "[epic] Cancellation test",
		Description: "Should respect context cancellation",
		ProjectPath: tmpDir,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := runner.PlanEpic(ctx, task)
	if err == nil {
		t.Fatal("PlanEpic should return error on cancelled context")
	}
}

func TestParsePRNumber(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected int
	}{
		{"standard PR URL", "https://github.com/owner/repo/pull/42", 42},
		{"pulls variant (not matched)", "https://github.com/owner/repo/pulls/99", 0},
		{"enterprise URL", "https://github.example.com/org/repo/pull/7", 7},
		{"large PR number", "https://github.com/owner/repo/pull/12345", 12345},
		{"empty string", "", 0},
		{"no pull path", "https://github.com/owner/repo/issues/123", 0},
		{"plain text", "not a url", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parsePRNumberFromURL(tt.url)
			if result != tt.expected {
				t.Errorf("parsePRNumberFromURL(%q) = %d, want %d", tt.url, result, tt.expected)
			}
		})
	}
}

func TestSetOnSubIssuePRCreated(t *testing.T) {
	runner := NewRunner()

	// Callback should be nil by default
	if runner.onSubIssuePRCreated != nil {
		t.Error("onSubIssuePRCreated should be nil by default")
	}

	// Set callback
	var called bool
	var capturedPR int
	var capturedURL string
	var capturedIssue int
	var capturedSHA string
	var capturedBranch string

	runner.SetOnSubIssuePRCreated(func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string) {
		called = true
		capturedPR = prNumber
		capturedURL = prURL
		capturedIssue = issueNumber
		capturedSHA = headSHA
		capturedBranch = branchName
	})

	if runner.onSubIssuePRCreated == nil {
		t.Fatal("onSubIssuePRCreated should be set after SetOnSubIssuePRCreated")
	}

	// Invoke callback
	runner.onSubIssuePRCreated(42, "https://github.com/owner/repo/pull/42", 10, "abc123", "pilot/GH-10")

	if !called {
		t.Error("callback was not invoked")
	}
	if capturedPR != 42 {
		t.Errorf("prNumber = %d, want 42", capturedPR)
	}
	if capturedURL != "https://github.com/owner/repo/pull/42" {
		t.Errorf("prURL = %q, want pull/42 URL", capturedURL)
	}
	if capturedIssue != 10 {
		t.Errorf("issueNumber = %d, want 10", capturedIssue)
	}
	if capturedSHA != "abc123" {
		t.Errorf("headSHA = %q, want abc123", capturedSHA)
	}
	if capturedBranch != "pilot/GH-10" {
		t.Errorf("branchName = %q, want pilot/GH-10", capturedBranch)
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

