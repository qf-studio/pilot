package executor

import (
	"testing"
)

func TestDetectComplexity(t *testing.T) {
	tests := []struct {
		name     string
		task     *Task
		expected Complexity
	}{
		// Trivial cases
		{
			name:     "typo fix",
			task:     &Task{Description: "Fix typo in README.md"},
			expected: ComplexityTrivial,
		},
		{
			name:     "add logging",
			task:     &Task{Description: "Add log statement to debug connection"},
			expected: ComplexityTrivial,
		},
		{
			name:     "rename variable",
			task:     &Task{Description: "Rename variable from x to count"},
			expected: ComplexityTrivial,
		},
		{
			name:     "update comment",
			task:     &Task{Description: "Update comment to reflect new behavior"},
			expected: ComplexityTrivial,
		},
		{
			name:     "remove unused import",
			task:     &Task{Description: "Remove unused import statements"},
			expected: ComplexityTrivial,
		},

		// Simple cases
		{
			name:     "add field",
			task:     &Task{Description: "Add field to user struct"},
			expected: ComplexitySimple,
		},
		{
			name:     "add parameter",
			task:     &Task{Description: "Add parameter to function"},
			expected: ComplexitySimple,
		},
		{
			name:     "quick fix",
			task:     &Task{Description: "Quick fix for null check"},
			expected: ComplexitySimple,
		},
		{
			name:     "short description heuristic",
			task:     &Task{Description: "Update the button color"},
			expected: ComplexitySimple,
		},

		// Complex cases
		{
			name:     "refactor",
			task:     &Task{Description: "Refactor the authentication system"},
			expected: ComplexityComplex,
		},
		{
			name:     "migration",
			task:     &Task{Description: "Database migration for new schema"},
			expected: ComplexityComplex,
		},
		{
			name:     "architecture",
			task:     &Task{Description: "Update system architecture for microservices"},
			expected: ComplexityComplex,
		},
		{
			name:     "rewrite",
			task:     &Task{Description: "Rewrite the parser from scratch"},
			expected: ComplexityComplex,
		},

		// Medium cases (default)
		{
			name:     "medium length description",
			task:     &Task{Description: "Implement a new endpoint that fetches user data from the database and returns it formatted as JSON with proper error handling"},
			expected: ComplexityMedium,
		},
		{
			name:     "feature without keywords",
			task:     &Task{Description: "Create new component for displaying charts with proper styling and responsive design"},
			expected: ComplexityMedium,
		},

		// Edge cases
		{
			name:     "nil task",
			task:     nil,
			expected: ComplexityMedium,
		},
		{
			name:     "empty description",
			task:     &Task{Description: ""},
			expected: ComplexitySimple,
		},
		{
			name:     "title contains pattern",
			task:     &Task{Title: "Fix typo", Description: "Some description"},
			expected: ComplexityTrivial,
		},

		// Long description triggers complex
		{
			name: "very long description",
			task: &Task{Description: `This task requires implementing a comprehensive solution that spans multiple files and components.
				We need to update the data layer, add new API endpoints, modify the frontend components, update tests,
				and ensure backward compatibility. The implementation should follow our coding standards and include
				proper documentation. We also need to consider performance implications and add appropriate caching
				where necessary. The feature should support both authenticated and anonymous users with different
				permission levels.`},
			expected: ComplexityComplex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectComplexity(tt.task)
			if got != tt.expected {
				t.Errorf("DetectComplexity() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestComplexity_Methods(t *testing.T) {
	tests := []struct {
		complexity          Complexity
		isTrivial           bool
		isSimple            bool
		shouldSkipNavigator bool
	}{
		{ComplexityTrivial, true, true, true},
		{ComplexitySimple, false, true, false},
		{ComplexityMedium, false, false, false},
		{ComplexityComplex, false, false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.complexity), func(t *testing.T) {
			if got := tt.complexity.IsTrivial(); got != tt.isTrivial {
				t.Errorf("IsTrivial() = %v, want %v", got, tt.isTrivial)
			}
			if got := tt.complexity.IsSimple(); got != tt.isSimple {
				t.Errorf("IsSimple() = %v, want %v", got, tt.isSimple)
			}
			if got := tt.complexity.ShouldSkipNavigator(); got != tt.shouldSkipNavigator {
				t.Errorf("ShouldSkipNavigator() = %v, want %v", got, tt.shouldSkipNavigator)
			}
		})
	}
}

func TestComplexity_String(t *testing.T) {
	tests := []struct {
		complexity Complexity
		expected   string
	}{
		{ComplexityTrivial, "trivial"},
		{ComplexitySimple, "simple"},
		{ComplexityMedium, "medium"},
		{ComplexityComplex, "complex"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.complexity.String(); got != tt.expected {
				t.Errorf("String() = %v, want %v", got, tt.expected)
			}
		})
	}
}
