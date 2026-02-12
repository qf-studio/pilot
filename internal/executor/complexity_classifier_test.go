package executor

import (
	"context"
	"errors"
	"testing"
)

// mockClaudeRunner creates a test runner that returns canned classification JSON.
func mockClaudeRunner(complexity, reason string) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(`{"complexity":"` + complexity + `","reason":"` + reason + `"}`), nil
	}
}

// mockClaudeRunnerError creates a test runner that returns an error.
func mockClaudeRunnerError(err error) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, err
	}
}

func TestComplexityClassifier_SimpleTask(t *testing.T) {
	classifier := newComplexityClassifierWithRunner(mockClaudeRunner("SIMPLE", "Single field addition"))
	task := &Task{
		ID:          "GH-100",
		Title:       "Add email field to user struct",
		Description: "Add an email field to the user struct in models.go",
	}

	result := classifier.Classify(context.Background(), task)
	if result != ComplexitySimple {
		t.Errorf("expected SIMPLE, got %s", result)
	}
}

func TestComplexityClassifier_MediumDetailedTask(t *testing.T) {
	// This is the key test: a detailed but well-scoped issue should be MEDIUM, not COMPLEX
	classifier := newComplexityClassifierWithRunner(mockClaudeRunner("MEDIUM", "Detailed instructions but single-scope feature"))
	task := &Task{
		ID:    "GH-200",
		Title: "Add webhook endpoint with retry logic",
		Description: `Implement a webhook endpoint that:
1. Accepts POST requests at /api/webhooks
2. Validates the payload signature using HMAC-SHA256
3. Stores the event in the database
4. Implements retry logic with exponential backoff (max 3 retries)
5. Returns 200 on success, 400 on invalid signature
6. Add unit tests for all paths

Use the existing http router and database connection.
Follow the patterns in internal/api/handlers.go.`,
	}

	result := classifier.Classify(context.Background(), task)
	if result != ComplexityMedium {
		t.Errorf("expected MEDIUM (detailed but well-scoped), got %s", result)
	}
}

func TestComplexityClassifier_ComplexTask(t *testing.T) {
	classifier := newComplexityClassifierWithRunner(mockClaudeRunner("COMPLEX", "Requires architectural changes across multiple systems"))
	task := &Task{
		ID:          "GH-300",
		Title:       "Migrate authentication from sessions to JWT",
		Description: "Rewrite the entire auth system from session-based to JWT. Requires database schema changes, new middleware, updated frontend token handling, and migration of existing sessions.",
	}

	result := classifier.Classify(context.Background(), task)
	if result != ComplexityComplex {
		t.Errorf("expected COMPLEX, got %s", result)
	}
}

func TestComplexityClassifier_CachesResult(t *testing.T) {
	callCount := 0
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		callCount++
		return []byte(`{"complexity":"MEDIUM","reason":"standard work"}`), nil
	}

	classifier := newComplexityClassifierWithRunner(runner)
	task := &Task{
		ID:          "GH-400",
		Title:       "Add logging",
		Description: "Add structured logging to the API layer",
	}

	// First call hits subprocess
	result1 := classifier.Classify(context.Background(), task)
	// Second call should use cache
	result2 := classifier.Classify(context.Background(), task)

	if result1 != result2 {
		t.Errorf("cached result differs: %s vs %s", result1, result2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 subprocess call (cached), got %d", callCount)
	}
}

func TestComplexityClassifier_FallsBackOnError(t *testing.T) {
	classifier := newComplexityClassifierWithRunner(mockClaudeRunnerError(errors.New("subprocess failed")))
	task := &Task{
		ID:          "GH-500",
		Title:       "Fix typo in README",
		Description: "Fix typo in README.md",
	}

	// Should fall back to heuristic (word count < 10 → Simple; "typo" pattern → Trivial)
	result := classifier.Classify(context.Background(), task)
	if result != ComplexityTrivial {
		t.Errorf("expected fallback to heuristic (TRIVIAL), got %s", result)
	}
}

func TestComplexityClassifier_NilTask(t *testing.T) {
	classifier := newComplexityClassifierWithRunner(mockClaudeRunner("MEDIUM", "n/a"))
	result := classifier.Classify(context.Background(), nil)
	if result != ComplexityMedium {
		t.Errorf("expected MEDIUM for nil task, got %s", result)
	}
}

func TestNewComplexityClassifier(t *testing.T) {
	// No API key needed anymore - uses Claude Code subscription
	classifier := NewComplexityClassifier()
	if classifier == nil {
		t.Fatal("expected non-nil classifier")
	}
	if classifier.model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected haiku model, got %s", classifier.model)
	}
}

func TestParseClassificationResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected Complexity
		wantErr  bool
	}{
		{
			name:     "simple JSON",
			input:    `{"complexity":"MEDIUM","reason":"standard work"}`,
			expected: ComplexityMedium,
		},
		{
			name:     "markdown wrapped",
			input:    "```json\n{\"complexity\":\"COMPLEX\",\"reason\":\"arch change\"}\n```",
			expected: ComplexityComplex,
		},
		{
			name:     "lowercase",
			input:    `{"complexity":"trivial","reason":"typo"}`,
			expected: ComplexityTrivial,
		},
		{
			name:     "epic",
			input:    `{"complexity":"EPIC","reason":"multi-phase"}`,
			expected: ComplexityEpic,
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
		{
			name:    "unknown level",
			input:   `{"complexity":"MEGA","reason":"unknown"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseClassificationResponse(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestHasLabel(t *testing.T) {
	tests := []struct {
		name     string
		task     *Task
		label    string
		expected bool
	}{
		{
			name:     "nil task",
			task:     nil,
			label:    "no-decompose",
			expected: false,
		},
		{
			name:     "no labels",
			task:     &Task{Labels: nil},
			label:    "no-decompose",
			expected: false,
		},
		{
			name:     "label present",
			task:     &Task{Labels: []string{"pilot", "no-decompose", "priority:high"}},
			label:    "no-decompose",
			expected: true,
		},
		{
			name:     "case insensitive",
			task:     &Task{Labels: []string{"No-Decompose"}},
			label:    "no-decompose",
			expected: true,
		},
		{
			name:     "label absent",
			task:     &Task{Labels: []string{"pilot", "enhancement"}},
			label:    "no-decompose",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasLabel(tt.task, tt.label)
			if result != tt.expected {
				t.Errorf("HasLabel() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDecomposer_NoDecomposeLabel(t *testing.T) {
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)

	// Task that would normally decompose (complex + numbered steps)
	task := &Task{
		ID:    "GH-600",
		Title: "Refactor authentication system",
		Description: `Refactor the entire authentication system:
1. Update user model
2. Rewrite login endpoint
3. Add session middleware
4. Update frontend components`,
		Labels: []string{"pilot", "no-decompose"},
	}

	result := decomposer.Decompose(task)
	if result.Decomposed {
		t.Error("expected task with no-decompose label to NOT be decomposed")
	}
	if result.Reason != "skipped: no-decompose label" {
		t.Errorf("expected reason 'skipped: no-decompose label', got %q", result.Reason)
	}
}

func TestDecomposer_WithLLMClassifier(t *testing.T) {
	// LLM says MEDIUM → should NOT decompose (threshold is complex)
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)
	classifier := newComplexityClassifierWithRunner(mockClaudeRunner("MEDIUM", "well-scoped feature with clear instructions"))
	decomposer.SetClassifier(classifier)

	// This task has numbered steps and "refactor" keyword — heuristic would say COMPLEX
	// But LLM correctly identifies it as MEDIUM (well-scoped)
	task := &Task{
		ID:    "GH-700",
		Title: "Add retry logic to webhook handler",
		Description: `Add retry logic to the webhook handler with exponential backoff:
1. Add retry counter to webhook event struct
2. Implement exponential backoff calculation
3. Add retry queue worker
4. Update webhook handler to enqueue failed events
5. Add unit tests for retry logic

Follow existing patterns in internal/webhooks/handler.go.`,
	}

	result := decomposer.DecomposeWithContext(context.Background(), task)
	if result.Decomposed {
		t.Error("expected LLM MEDIUM classification to prevent decomposition")
	}
	if result.Reason != "complexity below threshold: medium" {
		t.Errorf("expected threshold reason, got %q", result.Reason)
	}
}

func TestDecomposer_LLMClassifierFallback(t *testing.T) {
	// LLM returns error → falls back to heuristic
	config := &DecomposeConfig{
		Enabled:             true,
		MinComplexity:       "complex",
		MaxSubtasks:         5,
		MinDescriptionWords: 10,
	}
	decomposer := NewTaskDecomposer(config)
	classifier := newComplexityClassifierWithRunner(mockClaudeRunnerError(errors.New("subprocess failed")))
	decomposer.SetClassifier(classifier)

	// This task has "refactor" keyword → heuristic says COMPLEX → should decompose
	task := &Task{
		ID:    "GH-701",
		Title: "Refactor authentication system",
		Description: `Refactor the entire auth system:
1. Update user model with new fields
2. Rewrite login endpoint for MFA
3. Add session validation middleware
4. Update frontend auth components`,
	}

	result := decomposer.DecomposeWithContext(context.Background(), task)
	// Heuristic fallback: "refactor" keyword → COMPLEX → should decompose
	if !result.Decomposed {
		t.Errorf("expected heuristic fallback to decompose (refactor = complex), reason: %s", result.Reason)
	}
}
