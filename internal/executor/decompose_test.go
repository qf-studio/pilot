package executor

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestDecomposeConfig_Defaults(t *testing.T) {
	config := DefaultDecomposeConfig()

	if config.Enabled {
		t.Error("Expected Enabled to be false by default")
	}
	if config.MinComplexity != "complex" {
		t.Errorf("Expected MinComplexity to be 'complex', got %q", config.MinComplexity)
	}
	if config.MaxSubtasks != 5 {
		t.Errorf("Expected MaxSubtasks to be 5, got %d", config.MaxSubtasks)
	}
	if config.MinDescriptionWords != 50 {
		t.Errorf("Expected MinDescriptionWords to be 50, got %d", config.MinDescriptionWords)
	}
}

func TestTaskDecomposer_Disabled(t *testing.T) {
	config := &DecomposeConfig{
		Enabled: false,
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:          "TEST-1",
		Title:       "Complex Task",
		Description: "This is a very complex task with many steps that should be decomposed but won't be because decomposition is disabled.",
	}

	result := decomposer.Decompose(task)

	if result.Decomposed {
		t.Error("Expected Decomposed to be false when disabled")
	}
	if len(result.Subtasks) != 1 {
		t.Errorf("Expected 1 subtask (original), got %d", len(result.Subtasks))
	}
	if result.Reason != "decomposition disabled" {
		t.Errorf("Expected reason 'decomposition disabled', got %q", result.Reason)
	}
}

func TestTaskDecomposer_NilTask(t *testing.T) {
	decomposer := NewTaskDecomposer(&DecomposeConfig{Enabled: true})

	result := decomposer.Decompose(nil)

	if result.Decomposed {
		t.Error("Expected Decomposed to be false for nil task")
	}
	if result.Subtasks != nil {
		t.Error("Expected nil subtasks for nil task")
	}
	if result.Reason != "nil task" {
		t.Errorf("Expected reason 'nil task', got %q", result.Reason)
	}
}

func TestTaskDecomposer_SimpleTasks(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 50,
	}
	decomposer := NewTaskDecomposer(config)

	// Simple task - should not decompose
	task := &Task{
		ID:          "TEST-1",
		Title:       "Fix typo",
		Description: "Fix a typo in the README file",
	}

	result := decomposer.Decompose(task)

	if result.Decomposed {
		t.Error("Expected simple task to not be decomposed")
	}
	if !strings.Contains(result.Reason, "complexity below threshold") {
		t.Errorf("Expected reason about complexity, got %q", result.Reason)
	}
}

func TestTaskDecomposer_NumberedSteps(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10, // Lower for testing
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "TEST-1",
		Title: "Refactor authentication system",
		Description: `This task requires refactoring the entire authentication system with multiple changes:

1. Update the user model to include new fields for MFA
2. Refactor the login endpoint to support MFA flow
3. Add new middleware for session validation
4. Update the frontend components for MFA input
5. Add comprehensive tests for all changes

This is a complex architectural change that spans multiple files.`,
		ProjectPath: "/test/project",
		Branch:      "feature/auth-refactor",
		CreatePR:    true,
	}

	result := decomposer.Decompose(task)

	if !result.Decomposed {
		t.Errorf("Expected task to be decomposed, reason: %s", result.Reason)
		return
	}

	if len(result.Subtasks) != 5 {
		t.Errorf("Expected 5 subtasks, got %d", len(result.Subtasks))
	}

	// Verify subtask IDs
	for i, subtask := range result.Subtasks {
		expectedID := "TEST-1-" + strconv.Itoa(i+1)
		if subtask.ID != expectedID {
			t.Errorf("Subtask %d: expected ID %q, got %q", i, expectedID, subtask.ID)
		}

		// Verify project path propagation
		if subtask.ProjectPath != task.ProjectPath {
			t.Errorf("Subtask %d: ProjectPath not propagated", i)
		}

		// Verify branch propagation
		if subtask.Branch != task.Branch {
			t.Errorf("Subtask %d: Branch not propagated", i)
		}

		// Only last subtask should create PR
		if i < len(result.Subtasks)-1 && subtask.CreatePR {
			t.Errorf("Subtask %d: should not create PR", i)
		}
		if i == len(result.Subtasks)-1 && !subtask.CreatePR {
			t.Error("Last subtask should create PR")
		}
	}
}

func TestTaskDecomposer_BulletPoints(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "TEST-2",
		Title: "Database migration for new schema",
		Description: `Migrate the database to support the new multi-tenant architecture:

- Create tenant table with proper indexes
- Add tenant_id column to all user-facing tables
- Implement row-level security policies
- Update all queries to filter by tenant

This migration is critical for the multi-tenant feature.`,
		ProjectPath: "/test/project",
	}

	result := decomposer.Decompose(task)

	if !result.Decomposed {
		t.Errorf("Expected task to be decomposed, reason: %s", result.Reason)
		return
	}

	if len(result.Subtasks) != 4 {
		t.Errorf("Expected 4 subtasks, got %d", len(result.Subtasks))
	}
}

func TestTaskDecomposer_AcceptanceCriteria(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "TEST-3",
		Title: "Rewrite the API layer",
		Description: `Rewrite the API layer to use the new framework with the following criteria:

## Acceptance Criteria
- [ ] All existing endpoints migrated to new router
- [ ] OpenAPI spec generated automatically
- [ ] Request validation using new middleware
- [ ] Response serialization updated

This is a major rewrite that requires careful planning.`,
		ProjectPath: "/test/project",
	}

	result := decomposer.Decompose(task)

	if !result.Decomposed {
		t.Errorf("Expected task to be decomposed, reason: %s", result.Reason)
		return
	}

	if len(result.Subtasks) != 4 {
		t.Errorf("Expected 4 subtasks, got %d", len(result.Subtasks))
	}
}

func TestTaskDecomposer_MaxSubtasks(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         3, // Limit to 3
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "TEST-4",
		Title: "Restructure the entire codebase",
		Description: `Major restructuring with many steps:

1. Step one of the restructure
2. Step two of the restructure
3. Step three of the restructure
4. Step four of the restructure
5. Step five of the restructure
6. Step six of the restructure

This is extensive work requiring many changes.`,
		ProjectPath: "/test/project",
	}

	result := decomposer.Decompose(task)

	if !result.Decomposed {
		t.Errorf("Expected task to be decomposed, reason: %s", result.Reason)
		return
	}

	if len(result.Subtasks) != 3 {
		t.Errorf("Expected max 3 subtasks, got %d", len(result.Subtasks))
	}
}

func TestTaskDecomposer_ShortDescription(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 100, // High threshold
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "TEST-5",
		Title: "Refactor something",
		Description: `Short refactor task:
1. Do this
2. Do that`,
		ProjectPath: "/test/project",
	}

	result := decomposer.Decompose(task)

	if result.Decomposed {
		t.Error("Expected short description to not be decomposed")
	}
	if result.Reason != "description too short for decomposition (heuristic mode)" {
		t.Errorf("Expected reason about short description, got %q", result.Reason)
	}
}

func TestTaskDecomposer_NoDecompositionPoints(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)

	// Complex task but no clear structure for decomposition
	task := &Task{
		ID:    "TEST-6",
		Title: "Refactor the system architecture completely",
		Description: `This is a complex refactoring task that involves updating the system
architecture to support new requirements. The changes will touch multiple files
and modules across the codebase. We need to ensure backward compatibility while
improving the overall design. This is quite complex and requires careful consideration
of all the components involved in the system and how they interact with each other.`,
		ProjectPath: "/test/project",
	}

	result := decomposer.Decompose(task)

	if result.Decomposed {
		t.Error("Expected task without clear structure to not decompose")
	}
	if result.Reason != "no decomposition points found" {
		t.Errorf("Expected reason 'no decomposition points found', got %q", result.Reason)
	}
}

func TestExtractNumberedSteps(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name: "standard numbered list",
			text: `1. First item
2. Second item
3. Third item`,
			expected: 3,
		},
		{
			name: "parentheses format",
			text: `1) First item
2) Second item`,
			expected: 2,
		},
		{
			name: "step format",
			text: `Step 1: Do this
Step 2: Do that
Step 3: Finish up`,
			expected: 3,
		},
		{
			name:     "no numbered items",
			text:     "Just some plain text without numbers",
			expected: 0,
		},
		{
			name:     "single item",
			text:     "1. Only one item",
			expected: 0, // Need at least 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractNumberedSteps(tt.text)
			if len(result) != tt.expected {
				t.Errorf("extractNumberedSteps() returned %d items, want %d", len(result), tt.expected)
			}
		})
	}
}

func TestExtractBulletPoints(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name: "dash bullets",
			text: `- First item
- Second item
- Third item`,
			expected: 3,
		},
		{
			name: "asterisk bullets",
			text: `* First item
* Second item`,
			expected: 2,
		},
		{
			name: "skip completed checkboxes",
			text: `- [x] Completed item
- [ ] Pending item
- [ ] Another pending`,
			expected: 2, // Only uncompleted items
		},
		{
			name:     "no bullets",
			text:     "Just plain text",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBulletPoints(tt.text)
			if len(result) != tt.expected {
				t.Errorf("extractBulletPoints() returned %d items, want %d", len(result), tt.expected)
			}
		})
	}
}

func TestExtractAcceptanceCriteria(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name: "checkbox criteria",
			text: `## Acceptance Criteria
[ ] First criterion
[ ] Second criterion
[ ] Third criterion`,
			expected: 3,
		},
		{
			name: "bullet checkbox criteria",
			text: `- [ ] First
- [ ] Second`,
			expected: 2,
		},
		{
			name:     "no criteria",
			text:     "No acceptance criteria here",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractAcceptanceCriteria(tt.text)
			if len(result) != tt.expected {
				t.Errorf("extractAcceptanceCriteria() returned %d items, want %d", len(result), tt.expected)
			}
		})
	}
}

func TestExtractFileGroups(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int
	}{
		{
			name:     "go files",
			text:     "Update internal/executor/runner.go and internal/executor/backend.go",
			expected: 2,
		},
		{
			name:     "mixed files",
			text:     "Modify src/component.tsx, api/handler.go, and test.py",
			expected: 3,
		},
		{
			name:     "no files",
			text:     "Just update the documentation",
			expected: 0,
		},
		{
			name:     "single file",
			text:     "Only change main.go",
			expected: 0, // Need at least 2
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractFileGroups(tt.text)
			if len(result) != tt.expected {
				t.Errorf("extractFileGroups() returned %d items, want %d", len(result), tt.expected)
			}
		})
	}
}

func TestGenerateSubtaskID(t *testing.T) {
	tests := []struct {
		parentID string
		index    int
		expected string
	}{
		{"GH-150", 1, "GH-150-1"},
		{"GH-150", 2, "GH-150-2"},
		{"TASK-123", 10, "TASK-123-10"},
		{"TEST", 1, "TEST-1"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := generateSubtaskID(tt.parentID, tt.index)
			if result != tt.expected {
				t.Errorf("generateSubtaskID(%q, %d) = %q, want %q",
					tt.parentID, tt.index, result, tt.expected)
			}
		})
	}
}

func TestTruncateTitle(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"Short", 10, "Short"},
		{"This is a very long title", 15, "This is a ve..."},
		{"No truncation needed", 50, "No truncation needed"},
		{"Multi\nline\ntitle", 20, "Multi line title"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateTitle(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateTitle(%q, %d) = %q, want %q",
					tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

func TestShouldDecompose(t *testing.T) {
	tests := []struct {
		name     string
		task     *Task
		config   *DecomposeConfig
		expected bool
	}{
		{
			name:     "nil config",
			task:     &Task{Description: "Some long description that should be complex enough"},
			config:   nil,
			expected: false,
		},
		{
			name:     "disabled config",
			task:     &Task{Description: "Some long description that should be complex enough"},
			config:   &DecomposeConfig{Enabled: false},
			expected: false,
		},
		{
			name: "complex task meets criteria",
			task: &Task{
				Description: strings.Repeat("word ", 60) + "refactor the system",
			},
			config: &DecomposeConfig{
				Enabled:             true,
				MinComplexity:       "complex",
				MinDescriptionWords: 50,
			},
			expected: true,
		},
		{
			name: "simple task",
			task: &Task{
				Description: "Fix typo in README",
			},
			config: &DecomposeConfig{
				Enabled:             true,
				MinComplexity:       "complex",
				MinDescriptionWords: 10,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldDecompose(tt.task, tt.config)
			if result != tt.expected {
				t.Errorf("ShouldDecompose() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDecomposeForRetry_BypassesAllGates(t *testing.T) {
	// DecomposeForRetry should bypass word count and complexity gates entirely.
	// A short task with numbered steps should decompose because execution failure
	// already proved the task is too large — gate bypass is the point of this path.
	config := &DecomposeConfig{
		Enabled:             false, // Disabled for normal path — bypass is the point
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 1000, // Very high threshold — would block normal decomposition
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "GH-1716",
		Title: "Implement auth system",
		Description: `Short task with numbered steps:
1. Add user model
2. Add login endpoint
3. Add session middleware`,
		ProjectPath: "/test/project",
	}

	result := decomposer.DecomposeForRetry(nil, task)

	if !result.Decomposed {
		t.Errorf("Expected DecomposeForRetry to decompose task with numbered steps, reason: %s", result.Reason)
	}
	if len(result.Subtasks) != 3 {
		t.Errorf("Expected 3 subtasks, got %d", len(result.Subtasks))
	}
	if result.Reason != "decomposed after execution failure (retry fallback)" {
		t.Errorf("Unexpected reason: %q", result.Reason)
	}
}

func TestDecomposeForRetry_RespectsNoDecomposeLabel(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:     true,
		MaxSubtasks: 5,
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:     "GH-1716",
		Title:  "Implement auth system",
		Labels: []string{NoDecomposeLabel},
		Description: `1. Add user model
2. Add login endpoint
3. Add session middleware`,
	}

	result := decomposer.DecomposeForRetry(nil, task)

	if result.Decomposed {
		t.Error("Expected DecomposeForRetry to respect no-decompose label")
	}
	if result.Reason != "skipped: no-decompose label (even on retry)" {
		t.Errorf("Unexpected reason: %q", result.Reason)
	}
}

func TestDecomposeForRetry_NoStructuralPoints(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:     true,
		MaxSubtasks: 5,
	}
	decomposer := NewTaskDecomposer(config)

	task := &Task{
		ID:    "GH-1716",
		Title: "Refactor the system",
		Description: `This is a complex refactoring task that involves updating the system
architecture to support new requirements. The changes will touch multiple files
and modules across the codebase. We need to ensure backward compatibility while
improving the overall design.`,
	}

	result := decomposer.DecomposeForRetry(nil, task)

	if result.Decomposed {
		t.Error("Expected DecomposeForRetry to return false when no structural split points exist")
	}
	if result.Reason != "no decomposition points found (retry fallback)" {
		t.Errorf("Unexpected reason: %q", result.Reason)
	}
}

func TestHasNoDecomposePhrase(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		body     string
		expected bool
	}{
		// Each canonical phrase in body → true
		{name: "single AC list in body", body: "This is a single AC list for the task.", expected: true},
		{name: "do not decompose in body", body: "Please do not decompose this issue.", expected: true},
		{name: "do not split in body", body: "do not split this task.", expected: true},
		{name: "single Pilot issue in body", body: "This is a single Pilot issue.", expected: true},
		{name: "keep as ... single in body", body: "Keep as one single task.", expected: true},
		{name: "splitting this would in body", body: "Splitting this would fragment the changes.", expected: true},
		{name: "HTML marker in body", body: "<!-- pilot:no-decompose -->", expected: true},
		// GH-3597: standalone prose variants (the #3582 incident phrasing)
		{name: "must not be decomposed", body: "It must NOT be decomposed into sub-issues.", expected: true},
		{name: "must not be split", body: "This task must not be split.", expected: true},
		{name: "should not be decomposed", body: "The work should not be decomposed further.", expected: true},
		{name: "should not be split", body: "This should not be split across PRs.", expected: true},
		{name: "is a standalone task", body: "This is a standalone task covering one package.", expected: true},
		{name: "as a single PR", body: "Implement it as a single PR.", expected: true},
		// Each canonical phrase in title → true
		{name: "do not decompose in title", title: "do not decompose this", expected: true},
		{name: "do not split in title", title: "do not split this", expected: true},
		{name: "single Pilot issue in title", title: "single Pilot issue with multiple steps", expected: true},
		{name: "splitting this would in title", title: "splitting this would break things", expected: true},
		// Mixed-case → true
		{name: "mixed case single AC list", body: "This has a Single AC List of criteria.", expected: true},
		{name: "mixed case do not decompose", body: "DO NOT DECOMPOSE this task.", expected: true},
		{name: "mixed case HTML marker", body: "<!-- Pilot:No-Decompose -->", expected: true},
		// HTML marker with extra whitespace → true
		{name: "HTML marker with spaces", body: "<!--  pilot:no-decompose  -->", expected: true},
		// Phrase as unrelated substring → false
		{name: "single word only", body: "This is a single change to a file.", expected: false},
		{name: "split without do not", body: "We should split this into modules.", expected: false},
		{name: "standalone without task context", body: "Build a standalone binary for darwin.", expected: false},
		{name: "single PR mention without 'as a'", body: "The last single PR touched this file.", expected: false},
		{name: "pilot issue without single", body: "This is a pilot issue for adding auth.", expected: false},
		{name: "keep without single", body: "Keep as is without further changes.", expected: false},
		// Empty → false
		{name: "empty body", title: "", body: "", expected: false},
		// Genuine multi-scope body without opt-out → false
		{
			name:     "genuine multi-scope body",
			title:    "Refactor auth and payments",
			body:     "Update internal/auth/handler.go and internal/payments/service.go to use the new middleware.",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{
				Title:       tt.title,
				Description: tt.body,
			}
			got := HasNoDecomposePhrase(task)
			if got != tt.expected {
				t.Errorf("HasNoDecomposePhrase() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// gh3582BodyExcerpt reproduces the decision-relevant content of issue #3582
// verbatim (markdown/backticks stripped): the standalone-prose opener that was
// never machine-checked, a context citation of a foreign package (grouping.go),
// and work scoped entirely to internal/executor/. The daemon split this task
// into 4 sub-issues — the GH-3597 incident.
const gh3582BodyExcerpt = "This is a standalone task. It must NOT be decomposed into sub-issues. Implement it as a single PR.\n\n" +
	"## Problem\n\n" +
	"subIssueBody() (internal/executor/epic.go, ~line 1210) stamps the Parent: reference and autopilot-meta block programmatically — but the model-written subtask description it embeds is NOT sanitized. When the planner LLM emits its own Parent: GH-N line, the assembled body carries TWO parent claims:\n\n" +
	"- ParseParentIssueNumber (internal/adapters/github/grouping.go:38, regex ^Parent:\\s*(?:GH-|#)(\\d+)) can resolve the wrong one,\n" +
	"- recovery searches (queryRecentSubIssues, recoverExistingSubIssues in epic.go) match the child into a FOREIGN epic's recovery set → cross-epic contamination.\n\n" +
	"## Fix\n\n" +
	"In internal/executor/epic.go:\n\n" +
	"1. Add sanitizeSubtaskDescription(description string) string that removes parent lines and autopilot-meta blocks from model-supplied descriptions.\n" +
	"2. Call it on the description at BOTH body-assembly call sites of subIssueBody(...).\n\n" +
	"## Tests (table-driven, internal/executor/epic_test.go)\n\n" +
	"- description containing Parent: GH-201 → assembled body contains exactly ONE Parent: line\n"

// TestGH3582StandaloneBody_NeverDecomposes is the GH-3597 acceptance test:
// re-filing #3582's body (prose only — no label, no HTML marker) must classify
// as single-task execution at every gate.
func TestGH3582StandaloneBody_NeverDecomposes(t *testing.T) {
	task := &Task{
		ID:          "GH-3582",
		Title:       "Sanitize model-emitted parent refs in subtask bodies",
		Description: gh3582BodyExcerpt,
	}

	if !HasNoDecomposePhrase(task) {
		t.Fatal("expected standalone prose to match noDecomposePhrases")
	}
	if c := DetectComplexity(task); c == ComplexityEpic {
		t.Errorf("DetectComplexity() = %v, want non-epic for standalone-prose body", c)
	}

	decomposer := NewTaskDecomposer(&DecomposeConfig{
		Enabled:       true,
		MinComplexity: "complex",
		MaxSubtasks:   5,
	})
	result := decomposer.Decompose(task)
	if result.Decomposed {
		t.Errorf("Decompose() split a standalone-prose task: %s", result.Reason)
	}
	if result.Reason != "skipped: no-decompose phrase in title/description" {
		t.Errorf("Unexpected reason: %q", result.Reason)
	}
}

func TestDecomposeForRetry_RespectsNoDecomposePhrase(t *testing.T) {
	decomposer := NewTaskDecomposer(&DecomposeConfig{
		Enabled:     true,
		MaxSubtasks: 5,
	})

	task := &Task{
		ID:    "GH-3582",
		Title: "Implement auth system",
		Description: `This must not be split.
1. Add user model
2. Add login endpoint
3. Add session middleware`,
	}

	result := decomposer.DecomposeForRetry(nil, task)

	if result.Decomposed {
		t.Error("Expected DecomposeForRetry to respect no-decompose phrase")
	}
	if result.Reason != "skipped: no-decompose phrase (even on retry)" {
		t.Errorf("Unexpected reason: %q", result.Reason)
	}
}

func TestBuildSubtaskDescription(t *testing.T) {
	parent := &Task{
		ID:    "GH-150",
		Title: "Parent Task Title",
	}

	desc := buildSubtaskDescription(parent, "Do something specific", 2, 5)

	// Check required elements
	if !strings.Contains(desc, "Subtask 2 of 5") {
		t.Error("Expected subtask numbering in description")
	}
	if !strings.Contains(desc, "GH-150") {
		t.Error("Expected parent ID in description")
	}
	if !strings.Contains(desc, "Parent Task Title") {
		t.Error("Expected parent title in description")
	}
	if !strings.Contains(desc, "Do something specific") {
		t.Error("Expected objective in description")
	}

	// Check final subtask note
	finalDesc := buildSubtaskDescription(parent, "Final step", 5, 5)
	if !strings.Contains(finalDesc, "final subtask") {
		t.Error("Expected final subtask note")
	}
}

// TestDecomposeWithContext_LLMClassifierSkipsWordCountGate verifies that when an LLM
// classifier returns COMPLEX, the word count gate is bypassed (GH-1728).
func TestDecomposeWithContext_LLMClassifierSkipsWordCountGate(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 300, // High threshold — task description is well under this
	}
	decomposer := NewTaskDecomposer(config)

	// Attach LLM classifier that always returns COMPLEX
	classifier := newComplexityClassifierWithRunner(mockClaudeRunner("COMPLEX", "multiple adapter changes required"))
	decomposer.SetClassifier(classifier)

	// Short description (~30 words) with numbered steps — well under MinDescriptionWords
	task := &Task{
		ID:    "GH-1716",
		Title: "Add webhook support to three adapters",
		Description: `Add outbound webhook support to three adapters:
1. Telegram adapter
2. Slack adapter
3. GitHub adapter`,
		ProjectPath: "/test/project",
	}

	result := decomposer.DecomposeWithContext(context.Background(), task)

	if !result.Decomposed {
		t.Errorf("LLM classifier COMPLEX + short description should decompose; reason: %s", result.Reason)
	}
	if len(result.Subtasks) != 3 {
		t.Errorf("Expected 3 subtasks (one per adapter), got %d", len(result.Subtasks))
	}
}

// TestDecomposeWithContext_HeuristicEnforcesWordCountGate verifies that without an LLM
// classifier the word count gate still blocks short descriptions (GH-1728).
func TestDecomposeWithContext_HeuristicEnforcesWordCountGate(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 300, // High threshold
	}
	decomposer := NewTaskDecomposer(config)
	// No classifier set — heuristic mode

	task := &Task{
		ID:    "GH-1716",
		Title: "Refactor all three adapters completely",
		Description: `Refactor all three adapters completely:
1. Telegram adapter
2. Slack adapter
3. GitHub adapter`,
		ProjectPath: "/test/project",
	}

	result := decomposer.DecomposeWithContext(context.Background(), task)

	if result.Decomposed {
		t.Error("Heuristic mode with short description should NOT decompose")
	}
	if !strings.Contains(result.Reason, "heuristic mode") {
		t.Errorf("Expected reason to mention heuristic mode, got %q", result.Reason)
	}
}
