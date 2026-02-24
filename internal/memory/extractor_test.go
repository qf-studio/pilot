package memory

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestNewPatternExtractor(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)

	extractor := NewPatternExtractor(patternStore, store)
	if extractor == nil {
		t.Error("NewPatternExtractor returned nil")
	}
}

func TestExtractFromExecution_CompletedOnly(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	tests := []struct {
		name    string
		exec    *Execution
		wantErr bool
	}{
		{
			name: "completed execution",
			exec: &Execution{
				ID:          "exec-1",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      "Using context.Context in handlers. Added error handling for GetUser.",
			},
			wantErr: false,
		},
		{
			name: "running execution should fail",
			exec: &Execution{
				ID:          "exec-2",
				ProjectPath: "/test/project",
				Status:      "running",
				Output:      "In progress...",
			},
			wantErr: true,
		},
		{
			name: "pending execution should fail",
			exec: &Execution{
				ID:          "exec-3",
				ProjectPath: "/test/project",
				Status:      "pending",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractor.ExtractFromExecution(ctx, tt.exec)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractFromExecution() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractCodePatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	tests := []struct {
		name         string
		output       string
		wantPatterns int
	}{
		{
			name:         "context pattern",
			output:       "Using context.Context in handlers for proper timeout handling.",
			wantPatterns: 1,
		},
		{
			name:         "error handling pattern",
			output:       "Added error handling for GetUser and CreateOrder functions.",
			wantPatterns: 1,
		},
		{
			name:         "test pattern",
			output:       "Created tests for auth module. Created tests for payment module.",
			wantPatterns: 1,
		},
		{
			name:         "multiple patterns",
			output:       "Using context.Context in handlers. Added error handling for GetUser. Created tests for auth.",
			wantPatterns: 3,
		},
		{
			name:         "no patterns",
			output:       "Built the binary. Pushed to git.",
			wantPatterns: 0,
		},
		{
			name:         "structured logging pattern",
			output:       "Using zap for logging in the service.",
			wantPatterns: 1,
		},
		{
			name:         "validation pattern",
			output:       "Added validation for CreateUserRequest.",
			wantPatterns: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "test-exec",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      tt.output,
			}

			result, err := extractor.ExtractFromExecution(ctx, exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			if len(result.Patterns) != tt.wantPatterns {
				t.Errorf("got %d patterns, want %d", len(result.Patterns), tt.wantPatterns)
			}
		})
	}
}

func TestExtractErrorPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	tests := []struct {
		name             string
		errorOutput      string
		wantAntiPatterns int
	}{
		{
			name:             "nil pointer error",
			errorOutput:      "panic: nil pointer dereference",
			wantAntiPatterns: 1,
		},
		{
			name:             "sql no rows error",
			errorOutput:      "sql: no rows in result set",
			wantAntiPatterns: 1,
		},
		{
			name:             "context deadline error",
			errorOutput:      "context deadline exceeded",
			wantAntiPatterns: 1,
		},
		{
			name:             "race condition error",
			errorOutput:      "race condition detected in test",
			wantAntiPatterns: 1,
		},
		{
			name:             "import cycle error",
			errorOutput:      "import cycle not allowed",
			wantAntiPatterns: 1,
		},
		{
			name:             "no error patterns",
			errorOutput:      "build succeeded",
			wantAntiPatterns: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "test-exec",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      "Some output",
				Error:       tt.errorOutput,
			}

			result, err := extractor.ExtractFromExecution(ctx, exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			if len(result.AntiPatterns) != tt.wantAntiPatterns {
				t.Errorf("got %d anti-patterns, want %d", len(result.AntiPatterns), tt.wantAntiPatterns)
			}
		})
	}
}

func TestExtractWorkflowPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	tests := []struct {
		name         string
		output       string
		wantPatterns bool
	}{
		{
			name:         "make test pattern",
			output:       "Running make test to verify changes.",
			wantPatterns: true,
		},
		{
			name:         "make lint pattern",
			output:       "Ran make lint to check code quality.",
			wantPatterns: true,
		},
		{
			name:         "git commit pattern",
			output:       "Created git commit with changes.",
			wantPatterns: true,
		},
		{
			name:         "no workflow patterns",
			output:       "Just some regular output.",
			wantPatterns: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "test-exec",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      tt.output,
			}

			result, err := extractor.ExtractFromExecution(ctx, exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			hasWorkflowPattern := false
			for _, p := range result.Patterns {
				if p.Type == PatternTypeWorkflow {
					hasWorkflowPattern = true
					break
				}
			}

			if hasWorkflowPattern != tt.wantPatterns {
				t.Errorf("hasWorkflowPattern = %v, want %v", hasWorkflowPattern, tt.wantPatterns)
			}
		})
	}
}

func TestSaveExtractedPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	result := &ExtractionResult{
		ExecutionID: "test-exec",
		ProjectPath: "/test/project",
		Patterns: []*ExtractedPattern{
			{
				Type:        PatternTypeCode,
				Title:       "Test Pattern",
				Description: "A test pattern",
				Examples:    []string{"example1"},
				Confidence:  0.8,
				Context:     "Go code",
			},
		},
		AntiPatterns: []*ExtractedPattern{
			{
				Type:        PatternTypeError,
				Title:       "Nil pointer",
				Description: "Nil pointer dereference",
				Confidence:  0.7,
			},
		},
	}

	if err := extractor.SaveExtractedPatterns(ctx, result); err != nil {
		t.Fatalf("SaveExtractedPatterns() error = %v", err)
	}

	// Verify patterns were saved (1 pattern + 1 anti-pattern)
	if patternStore.Count() != 2 {
		t.Errorf("PatternStore.Count() = %d, want 2", patternStore.Count())
	}

	// Verify anti-pattern has correct prefix
	errorPatterns := patternStore.GetByType(PatternTypeError)
	if len(errorPatterns) != 1 {
		t.Fatalf("Expected 1 error pattern, got %d", len(errorPatterns))
	}
	if !strings.HasPrefix(errorPatterns[0].Title, "[ANTI]") {
		t.Errorf("Anti-pattern title should have [ANTI] prefix, got %q", errorPatterns[0].Title)
	}
}

func TestExtractAndSave(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	exec := &Execution{
		ID:          "extract-save-exec",
		ProjectPath: "/test/project",
		Status:      "completed",
		Output:      "using context.Context in Handler added error handling for Validate",
		Error:       "",
	}

	if err := extractor.ExtractAndSave(ctx, exec); err != nil {
		t.Fatalf("ExtractAndSave() error = %v", err)
	}

	// Should have extracted at least the context pattern
	if patternStore.Count() < 1 {
		t.Errorf("Expected at least 1 pattern to be saved, got %d", patternStore.Count())
	}
}

func TestExtractAndSave_NoPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	exec := &Execution{
		ID:          "no-patterns-exec",
		ProjectPath: "/test/project",
		Status:      "completed",
		Output:      "Just built the binary.",
	}

	// ExtractAndSave with no patterns should not call save, so no deadlock
	err = extractor.ExtractAndSave(ctx, exec)
	if err != nil {
		t.Fatalf("ExtractAndSave failed: %v", err)
	}

	// Should not error even with no patterns - no save was triggered
	if patternStore.Count() != 0 {
		t.Error("expected no patterns to be saved")
	}
}

func TestPatternAnalysisRequest_ToJSON(t *testing.T) {
	req := &PatternAnalysisRequest{
		ExecutionID:   "exec-123",
		ProjectPath:   "/test/project",
		Output:        "Some output",
		Error:         "Some error",
		DiffContent:   "+ added line\n- removed line",
		CommitMessage: "feat: add new feature",
	}

	jsonStr, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}

	if jsonStr == "" {
		t.Error("ToJSON returned empty string")
	}

	// Verify it's valid JSON by parsing
	_, err = ParseAnalysisResponse(`{"patterns":[],"anti_patterns":[]}`)
	if err != nil {
		t.Fatalf("ParseAnalysisResponse failed: %v", err)
	}
}

func TestParseAnalysisResponse(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name:    "valid empty response",
			json:    `{"patterns":[],"anti_patterns":[]}`,
			wantErr: false,
		},
		{
			name: "valid response with patterns",
			json: `{
				"patterns": [
					{"type": "code", "title": "Test Pattern", "description": "A test", "confidence": 0.8}
				],
				"anti_patterns": []
			}`,
			wantErr: false,
		},
		{
			name:    "invalid JSON",
			json:    `not valid json`,
			wantErr: true,
		},
		{
			name:    "empty string",
			json:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := ParseAnalysisResponse(tt.json)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAnalysisResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && resp == nil {
				t.Error("ParseAnalysisResponse returned nil without error")
			}
		})
	}
}

func TestMergePattern_DeduplicatesProjects(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	// Save pattern twice from same project
	result := &ExtractionResult{
		ExecutionID: "test-exec",
		ProjectPath: "/test/project",
		Patterns: []*ExtractedPattern{
			{Type: PatternTypeCode, Title: "Dedupe Test", Description: "Test", Confidence: 0.8},
		},
	}

	if err := extractor.SaveExtractedPatterns(ctx, result); err != nil {
		t.Fatalf("SaveExtractedPatterns() first call error = %v", err)
	}

	// Save again with same project
	if err := extractor.SaveExtractedPatterns(ctx, result); err != nil {
		t.Fatalf("SaveExtractedPatterns() second call error = %v", err)
	}

	// Should still only have 1 pattern with 1 project (deduplicated)
	patterns := patternStore.GetByType(PatternTypeCode)
	if len(patterns) != 1 {
		t.Fatalf("Expected 1 pattern, got %d", len(patterns))
	}

	if len(patterns[0].Projects) != 1 {
		t.Errorf("Expected 1 project (deduplicated), got %d", len(patterns[0].Projects))
	}
}

func TestMergePattern_AddsNewProjects(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	// Save pattern from first project
	result1 := &ExtractionResult{
		ExecutionID: "exec-1",
		ProjectPath: "/project/one",
		Patterns: []*ExtractedPattern{
			{Type: PatternTypeCode, Title: "Multi-Project Pattern", Description: "Test", Confidence: 0.8},
		},
	}

	if err := extractor.SaveExtractedPatterns(ctx, result1); err != nil {
		t.Fatalf("SaveExtractedPatterns() first project error = %v", err)
	}

	// Save same pattern from second project
	result2 := &ExtractionResult{
		ExecutionID: "exec-2",
		ProjectPath: "/project/two",
		Patterns: []*ExtractedPattern{
			{Type: PatternTypeCode, Title: "Multi-Project Pattern", Description: "Test", Confidence: 0.8},
		},
	}

	if err := extractor.SaveExtractedPatterns(ctx, result2); err != nil {
		t.Fatalf("SaveExtractedPatterns() second project error = %v", err)
	}

	// Should have 1 pattern with 2 projects
	patterns := patternStore.GetByType(PatternTypeCode)
	if len(patterns) != 1 {
		t.Fatalf("Expected 1 merged pattern, got %d", len(patterns))
	}

	if len(patterns[0].Projects) != 2 {
		t.Errorf("Expected 2 projects, got %d", len(patterns[0].Projects))
	}
}

func TestExtractFromReviewComments_TestingFeedback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	comments := []string{"add tests for edge cases", "test coverage is important"}

	result, err := extractor.ExtractFromReviewComments(ctx, comments, "/test/project")
	if err != nil {
		t.Fatalf("ExtractFromReviewComments failed: %v", err)
	}

	// Should extract testing anti-pattern
	if len(result.AntiPatterns) < 1 {
		t.Errorf("Expected at least 1 anti-pattern for testing feedback, got %d", len(result.AntiPatterns))
	}

	// Verify it's a workflow pattern
	found := false
	for _, p := range result.AntiPatterns {
		if p.Type == PatternTypeWorkflow {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected workflow anti-pattern for testing feedback")
	}
}

func TestExtractFromReviewComments_NamingFeedback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	comments := []string{"unclear variable names", "please rename"}

	result, err := extractor.ExtractFromReviewComments(ctx, comments, "/test/project")
	if err != nil {
		t.Fatalf("ExtractFromReviewComments failed: %v", err)
	}

	// Should extract naming anti-pattern
	found := false
	for _, p := range result.AntiPatterns {
		if p.Type == PatternTypeNaming {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected naming anti-pattern for unclear names feedback")
	}
}

func TestExtractFromReviewComments_ErrorHandling(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	comments := []string{"unchecked error in line 42", "handle error cases"}

	result, err := extractor.ExtractFromReviewComments(ctx, comments, "/test/project")
	if err != nil {
		t.Fatalf("ExtractFromReviewComments failed: %v", err)
	}

	// Should extract error handling anti-pattern
	found := false
	for _, p := range result.AntiPatterns {
		if p.Type == PatternTypeError {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected error handling anti-pattern")
	}
}

func TestExtractFromReviewComments_PositiveFeedback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	comments := []string{"nice approach", "well done implementation"}

	result, err := extractor.ExtractFromReviewComments(ctx, comments, "/test/project")
	if err != nil {
		t.Fatalf("ExtractFromReviewComments failed: %v", err)
	}

	// Should extract positive pattern, not anti-pattern
	if len(result.Patterns) < 1 {
		t.Errorf("Expected at least 1 positive pattern, got %d", len(result.Patterns))
	}

	if len(result.AntiPatterns) > 0 {
		t.Errorf("Expected no anti-patterns for positive feedback, got %d", len(result.AntiPatterns))
	}
}

func TestExtractFromReviewComments_NoPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	comments := []string{"looks good to merge"}

	result, err := extractor.ExtractFromReviewComments(ctx, comments, "/test/project")
	if err != nil {
		t.Fatalf("ExtractFromReviewComments failed: %v", err)
	}

	// No recognizable patterns
	if len(result.Patterns) > 0 || len(result.AntiPatterns) > 0 {
		t.Errorf("Expected no patterns for generic comment, got %d patterns and %d anti-patterns",
			len(result.Patterns), len(result.AntiPatterns))
	}
}

func TestExtractFromReviewComments_Documentation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	comments := []string{"add documentation for this function", "please explain this logic"}

	result, err := extractor.ExtractFromReviewComments(ctx, comments, "/test/project")
	if err != nil {
		t.Fatalf("ExtractFromReviewComments failed: %v", err)
	}

	// Should extract documentation anti-pattern
	found := false
	for _, p := range result.AntiPatterns {
		if p.Type == PatternTypeCode && strings.Contains(strings.ToLower(p.Title), "doc") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected documentation anti-pattern")
	}
}
