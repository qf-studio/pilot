package executor

import (
	"context"
	"errors"
	"testing"
)

// mockEffortRunner creates a test runner that returns canned effort JSON.
func mockEffortRunner(effort, reason string) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(`{"effort":"` + effort + `","reason":"` + reason + `"}`), nil
	}
}

// mockEffortRunnerError creates a test runner that returns an error.
func mockEffortRunnerError(err error) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, err
	}
}

func TestEffortClassifier_LowEffort(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("low", "Simple typo fix"))
	task := &Task{
		ID:          "GH-100",
		Title:       "Fix typo in README",
		Description: "Fix typo in README.md: change 'teh' to 'the'",
	}

	result := classifier.Classify(context.Background(), task)
	if result != "low" {
		t.Errorf("expected 'low', got %q", result)
	}
}

func TestEffortClassifier_MediumEffort(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("medium", "Standard work with clear requirements"))
	task := &Task{
		ID:    "GH-200",
		Title: "Add email field to user struct",
		Description: `Add an email field to the user struct:
1. Add field to models/user.go
2. Update CreateUser function
3. Add validation for email format
4. Write unit tests`,
	}

	result := classifier.Classify(context.Background(), task)
	if result != "medium" {
		t.Errorf("expected 'medium', got %q", result)
	}
}

func TestEffortClassifier_HighEffort(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("high", "Security-sensitive with multiple considerations"))
	task := &Task{
		ID:          "GH-300",
		Title:       "Fix authentication bypass vulnerability",
		Description: "There's a subtle bug in the session validation that allows bypassing auth under certain conditions. Need to investigate the root cause and fix without breaking existing sessions.",
	}

	result := classifier.Classify(context.Background(), task)
	if result != "high" {
		t.Errorf("expected 'high', got %q", result)
	}
}

func TestEffortClassifier_CachesResult(t *testing.T) {
	callCount := 0
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		callCount++
		return []byte(`{"effort":"medium","reason":"standard work"}`), nil
	}

	classifier := newEffortClassifierWithRunner(runner)
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
		t.Errorf("cached result differs: %q vs %q", result1, result2)
	}
	if callCount != 1 {
		t.Errorf("expected 1 subprocess call (cached), got %d", callCount)
	}
}

func TestEffortClassifier_ReturnsEmptyOnError(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunnerError(errors.New("subprocess failed")))
	task := &Task{
		ID:          "GH-500",
		Title:       "Fix typo in README",
		Description: "Fix typo in README.md",
	}

	// Should return empty string on error (signals fallback)
	result := classifier.Classify(context.Background(), task)
	if result != "" {
		t.Errorf("expected empty string on error, got %q", result)
	}
}

func TestEffortClassifier_NilTask(t *testing.T) {
	classifier := newEffortClassifierWithRunner(mockEffortRunner("medium", "n/a"))
	result := classifier.Classify(context.Background(), nil)
	if result != "" {
		t.Errorf("expected empty string for nil task, got %q", result)
	}
}

func TestNewEffortClassifier(t *testing.T) {
	classifier := NewEffortClassifier()
	if classifier == nil {
		t.Fatal("expected non-nil classifier")
	}
	if classifier.model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected haiku model, got %s", classifier.model)
	}
}

func TestParseEffortResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "simple JSON low",
			input:    `{"effort":"low","reason":"typo fix"}`,
			expected: "low",
		},
		{
			name:     "simple JSON medium",
			input:    `{"effort":"medium","reason":"standard work"}`,
			expected: "medium",
		},
		{
			name:     "simple JSON high",
			input:    `{"effort":"high","reason":"security sensitive"}`,
			expected: "high",
		},
		{
			name:     "markdown wrapped",
			input:    "```json\n{\"effort\":\"high\",\"reason\":\"complex analysis\"}\n```",
			expected: "high",
		},
		{
			name:     "uppercase",
			input:    `{"effort":"HIGH","reason":"arch change"}`,
			expected: "high",
		},
		{
			name:     "mixed case",
			input:    `{"effort":"Medium","reason":"standard"}`,
			expected: "medium",
		},
		{
			name:    "invalid JSON",
			input:   "not json at all",
			wantErr: true,
		},
		{
			name:    "unknown level",
			input:   `{"effort":"max","reason":"unknown"}`,
			wantErr: true,
		},
		{
			name:    "empty effort",
			input:   `{"effort":"","reason":"missing"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseEffortResponse(tt.input)
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
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestEffortClassifier_TaskWithoutID(t *testing.T) {
	callCount := 0
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		callCount++
		return []byte(`{"effort":"low","reason":"simple"}`), nil
	}

	classifier := newEffortClassifierWithRunner(runner)
	task := &Task{
		Title:       "Quick fix",
		Description: "A quick fix",
		// No ID - should still work but not cache
	}

	result1 := classifier.Classify(context.Background(), task)
	result2 := classifier.Classify(context.Background(), task)

	if result1 != "low" || result2 != "low" {
		t.Errorf("expected 'low' for both, got %q and %q", result1, result2)
	}
	// Without ID, should call subprocess twice (no caching)
	if callCount != 2 {
		t.Errorf("expected 2 subprocess calls (no cache without ID), got %d", callCount)
	}
}
