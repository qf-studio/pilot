package memory

import (
	"context"
	"fmt"
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

func TestExtractFromSelfReview(t *testing.T) {
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
		name              string
		selfReviewOutput  string
		wantAntiPatterns  int
		wantPatternTypes  []PatternType // expected types in anti-patterns (order-independent)
	}{
		{
			name: "multiple finding types",
			selfReviewOutput: `REVIEW_FIXED: corrected nil check in handler.go
Found missing error handling in database query at store.go:42
PARITY_GAP: interface method added but not implemented in mock
test coverage gap for new validateInput function
SUSPICIOUS_VALUE detected in pricing constant`,
			wantAntiPatterns: 5,
			wantPatternTypes: []PatternType{
				PatternTypeCode,      // REVIEW_FIXED
				PatternTypeError,     // missing error handling
				PatternTypeStructure, // PARITY_GAP
				PatternTypeWorkflow,  // test coverage gap
				PatternTypeCode,      // SUSPICIOUS_VALUE
			},
		},
		{
			name:             "empty output",
			selfReviewOutput: "",
			wantAntiPatterns: 0,
			wantPatternTypes: nil,
		},
		{
			name:             "whitespace only output",
			selfReviewOutput: "   \n\t  \n  ",
			wantAntiPatterns: 0,
			wantPatternTypes: nil,
		},
		{
			name:             "unparseable output with no markers",
			selfReviewOutput: "All checks passed. Code looks clean. No issues found.",
			wantAntiPatterns: 0,
			wantPatternTypes: nil,
		},
		{
			name: "mixed findings with clean output",
			selfReviewOutput: `Self-review complete.
Files checked: 5
REVIEW_FIXED: corrected nil check in handler
Everything else looks good.
INCOMPLETE: TODO items remain in auth module`,
			wantAntiPatterns: 2,
			wantPatternTypes: []PatternType{
				PatternTypeCode,     // REVIEW_FIXED
				PatternTypeWorkflow, // INCOMPLETE
			},
		},
		{
			name:             "build failure only",
			selfReviewOutput: "build verification failure: missing return statement in Calculate()",
			wantAntiPatterns: 1,
			wantPatternTypes: []PatternType{PatternTypeError},
		},
		{
			name:             "dead code detection",
			selfReviewOutput: "Found unused import: fmt in utils.go\nFound dead code in legacy handler",
			wantAntiPatterns: 1, // single regex matches both
			wantPatternTypes: []PatternType{PatternTypeCode},
		},
		{
			name:             "struct field unwired",
			selfReviewOutput: "config field not wired: MaxRetries defined in Config but never read",
			wantAntiPatterns: 1,
			wantPatternTypes: []PatternType{PatternTypeStructure},
		},
		{
			name:             "lint violations",
			selfReviewOutput: "lint violation: exported function missing doc comment\nlinter error on line 55",
			wantAntiPatterns: 1,
			wantPatternTypes: []PatternType{PatternTypeWorkflow},
		},
		{
			name: "cross-file parity",
			selfReviewOutput: `cross-file parity issue: AddUser added to service.go but not to service_test.go
parity mismatch between interface and implementation`,
			wantAntiPatterns: 1, // single regex matches both variants
			wantPatternTypes: []PatternType{PatternTypeStructure},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractor.ExtractFromSelfReview(ctx, tt.selfReviewOutput, "/test/project")
			if err != nil {
				t.Fatalf("ExtractFromSelfReview() error = %v", err)
			}

			if result == nil {
				t.Fatal("ExtractFromSelfReview() returned nil result")
			}

			if len(result.AntiPatterns) != tt.wantAntiPatterns {
				types := make([]PatternType, len(result.AntiPatterns))
				for i, ap := range result.AntiPatterns {
					types[i] = ap.Type
				}
				t.Errorf("got %d anti-patterns %v, want %d", len(result.AntiPatterns), types, tt.wantAntiPatterns)
				return
			}

			// Verify pattern types match (order-independent)
			if tt.wantPatternTypes != nil {
				gotTypes := make(map[PatternType]int)
				for _, ap := range result.AntiPatterns {
					gotTypes[ap.Type]++
				}

				wantTypes := make(map[PatternType]int)
				for _, pt := range tt.wantPatternTypes {
					wantTypes[pt]++
				}

				for pt, count := range wantTypes {
					if gotTypes[pt] != count {
						t.Errorf("pattern type %s: got %d, want %d", pt, gotTypes[pt], count)
					}
				}
			}

			// Verify all anti-patterns have confidence 0.5
			for _, ap := range result.AntiPatterns {
				if ap.Confidence != 0.5 {
					t.Errorf("anti-pattern %q has confidence %f, want 0.5", ap.Title, ap.Confidence)
				}
				if ap.Context != "Self-review" {
					t.Errorf("anti-pattern %q has context %q, want 'Self-review'", ap.Title, ap.Context)
				}
			}

			// Verify project path is set
			if result.ProjectPath != "/test/project" {
				t.Errorf("ProjectPath = %q, want %q", result.ProjectPath, "/test/project")
			}

			// Verify no positive patterns (self-review findings are all anti-patterns)
			if len(result.Patterns) != 0 {
				t.Errorf("got %d positive patterns, want 0", len(result.Patterns))
			}
		})
	}
}

func TestExtractErrorPatterns_CICompilationErrors(t *testing.T) {
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
		wantTitle        string
	}{
		{
			name:             "undefined identifier",
			errorOutput:      "./main.go:15:2: undefined: myFunc",
			wantAntiPatterns: 1,
			wantTitle:        "Undefined identifier",
		},
		{
			name:             "unused variable",
			errorOutput:      "./handler.go:10:2: x declared and not used",
			wantAntiPatterns: 1,
			wantTitle:        "Unused variable or import",
		},
		{
			name:             "type mismatch",
			errorOutput:      "cannot use val (variable of type string) as int in argument to process",
			wantAntiPatterns: 1,
			wantTitle:        "Type mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "test-ci-compile",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      "output",
				Error:       tt.errorOutput,
			}

			result, err := extractor.ExtractFromExecution(ctx, exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			if len(result.AntiPatterns) < tt.wantAntiPatterns {
				t.Errorf("got %d anti-patterns, want at least %d", len(result.AntiPatterns), tt.wantAntiPatterns)
			}

			found := false
			for _, ap := range result.AntiPatterns {
				if ap.Title == tt.wantTitle {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected anti-pattern with title %q not found", tt.wantTitle)
			}
		})
	}
}

func TestExtractErrorPatterns_CITestFailures(t *testing.T) {
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
		wantTitle        string
	}{
		{
			name:             "test FAIL line",
			errorOutput:      "--- FAIL: TestHandler (0.01s)\n    handler_test.go:42: expected 200, got 500",
			wantAntiPatterns: 1,
			wantTitle:        "Test failure",
		},
		{
			name:             "test timeout panic",
			errorOutput:      "panic: test timed out after 30s",
			wantAntiPatterns: 1,
			wantTitle:        "Test timeout",
		},
		{
			name:             "runtime panic in test",
			errorOutput:      "panic: runtime error: index out of range [5] with length 3",
			wantAntiPatterns: 1,
			wantTitle:        "Runtime panic in test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "test-ci-test",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      "output",
				Error:       tt.errorOutput,
			}

			result, err := extractor.ExtractFromExecution(ctx, exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			if len(result.AntiPatterns) < tt.wantAntiPatterns {
				t.Errorf("got %d anti-patterns, want at least %d", len(result.AntiPatterns), tt.wantAntiPatterns)
			}

			found := false
			for _, ap := range result.AntiPatterns {
				if ap.Title == tt.wantTitle {
					found = true
					break
				}
			}
			if !found {
				titles := make([]string, len(result.AntiPatterns))
				for i, ap := range result.AntiPatterns {
					titles[i] = ap.Title
				}
				t.Errorf("expected anti-pattern %q not found, got: %v", tt.wantTitle, titles)
			}
		})
	}
}

func TestExtractErrorPatterns_CILintAndBuildErrors(t *testing.T) {
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
		wantTitle        string
	}{
		{
			name:             "golangci-lint error",
			errorOutput:      "golangci-lint: error at handler.go:55 (errcheck)",
			wantAntiPatterns: 1,
			wantTitle:        "Lint violation",
		},
		{
			name:             "staticcheck error",
			errorOutput:      "staticcheck: SA1019 deprecated function used",
			wantAntiPatterns: 1,
			wantTitle:        "Lint violation",
		},
		{
			name:             "missing module",
			errorOutput:      "missing go.sum entry for module providing package github.com/foo/bar",
			wantAntiPatterns: 1,
			wantTitle:        "Missing module",
		},
		{
			name:             "version conflict",
			errorOutput:      `require github.com/foo/bar: version "v1.2.3" invalid`,
			wantAntiPatterns: 1,
			wantTitle:        "Module version conflict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := &Execution{
				ID:          "test-ci-lint",
				ProjectPath: "/test/project",
				Status:      "completed",
				Output:      "output",
				Error:       tt.errorOutput,
			}

			result, err := extractor.ExtractFromExecution(ctx, exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			if len(result.AntiPatterns) < tt.wantAntiPatterns {
				t.Errorf("got %d anti-patterns, want at least %d", len(result.AntiPatterns), tt.wantAntiPatterns)
			}

			found := false
			for _, ap := range result.AntiPatterns {
				if ap.Title == tt.wantTitle {
					found = true
					break
				}
			}
			if !found {
				titles := make([]string, len(result.AntiPatterns))
				for i, ap := range result.AntiPatterns {
					titles[i] = ap.Title
				}
				t.Errorf("expected anti-pattern %q not found, got: %v", tt.wantTitle, titles)
			}
		})
	}
}

func TestExtractCIErrorPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)

	tests := []struct {
		name           string
		ciLogs         string
		checkNames     []string
		wantMinCount   int // minimum expected patterns
		wantCategory   string
		wantConfidence float64
		wantCIContext  bool
		wantCheckTag   string
	}{
		// --- Compilation category ---
		{
			name:           "compilation: undefined identifier",
			ciLogs:         "undefined: processItem",
			wantMinCount:   1,
			wantCategory:   "compilation",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "compilation: type mismatch",
			ciLogs:         "cannot use myString as int in argument to Foo",
			wantMinCount:   1,
			wantCategory:   "compilation",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "compilation: unused variable",
			ciLogs:         "x declared and not used",
			wantMinCount:   1,
			wantCategory:   "compilation",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "compilation: missing return",
			ciLogs:         "missing return at end of function",
			wantMinCount:   1,
			wantCategory:   "compilation",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		// --- Test failure category ---
		{
			name:           "test: FAIL line",
			ciLogs:         "--- FAIL: TestProcess (0.05s)\n    process_test.go:42: expected nil, got error",
			wantMinCount:   1,
			wantCategory:   "test",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "test: panic test timed out",
			ciLogs:         "panic: test timed out after 30s",
			wantMinCount:   1,
			wantCategory:   "test",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "test: assertion failure",
			ciLogs:         "expected 42 but got 0",
			wantMinCount:   1,
			wantCategory:   "test",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "test: runtime panic",
			ciLogs:         "panic: runtime error: index out of range [5] with length 3",
			wantMinCount:   1,
			wantCategory:   "test",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		// --- Lint category ---
		{
			name:           "lint: golangci-lint",
			ciLogs:         "golangci-lint: error in file main.go",
			wantMinCount:   1,
			wantCategory:   "lint",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "lint: staticcheck",
			ciLogs:         "staticcheck: SA1006 found in handler.go",
			wantMinCount:   1,
			wantCategory:   "lint",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "lint: errcheck",
			ciLogs:         "errcheck: unchecked error in server.go",
			wantMinCount:   1,
			wantCategory:   "lint",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		// --- Build/module category ---
		{
			name:           "build: missing go.sum entry",
			ciLogs:         "missing go.sum entry for module github.com/foo/bar",
			wantMinCount:   1,
			wantCategory:   "build",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "build: import cycle",
			ciLogs:         "import cycle not allowed: pkg/a -> pkg/b -> pkg/a",
			wantMinCount:   1,
			wantCategory:   "build",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		{
			name:           "build: missing dependency",
			ciLogs:         "cannot find package \"github.com/missing/pkg\" in any of:",
			wantMinCount:   1,
			wantCategory:   "build",
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
		// --- Check name tagging ---
		{
			name:           "check name included in context",
			ciLogs:         "undefined: processItem",
			checkNames:     []string{"go-build", "go-vet"},
			wantMinCount:   1,
			wantCategory:   "compilation",
			wantConfidence: 0.5,
			wantCIContext:  true,
			wantCheckTag:   "check:go-build,go-vet",
		},
		// --- Edge cases ---
		{
			name:         "no matching patterns",
			ciLogs:       "Build succeeded. All tests passed.",
			wantMinCount: 0,
		},
		{
			name:         "empty logs",
			ciLogs:       "",
			wantMinCount: 0,
		},
		{
			name:         "whitespace-only logs",
			ciLogs:       "   \n\t  ",
			wantMinCount: 0,
		},
		// --- Multiple categories in one log ---
		{
			name:           "mixed: compilation + test failure",
			ciLogs:         "--- FAIL: TestProcess (0.05s)\npanic: runtime error: nil pointer\nundefined: handler",
			wantMinCount:   3, // FAIL + runtime panic + undefined
			wantCategory:   "", // mixed, skip category check
			wantConfidence: 0.5,
			wantCIContext:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var patterns []*ExtractedPattern
			if len(tt.checkNames) > 0 {
				patterns = extractor.extractCIErrorPatterns(tt.ciLogs, tt.checkNames...)
			} else {
				patterns = extractor.extractCIErrorPatterns(tt.ciLogs)
			}

			if len(patterns) < tt.wantMinCount {
				t.Errorf("got %d patterns, want at least %d", len(patterns), tt.wantMinCount)
				for i, p := range patterns {
					t.Logf("  pattern[%d]: %q category context=%q", i, p.Title, p.Context)
				}
				return
			}

			for _, p := range patterns {
				if p.Confidence != tt.wantConfidence {
					t.Errorf("pattern %q confidence = %f, want %f", p.Title, p.Confidence, tt.wantConfidence)
				}
				if tt.wantCIContext && !strings.HasPrefix(p.Context, "source:ci") {
					t.Errorf("pattern %q context = %q, want source:ci prefix", p.Title, p.Context)
				}
				if tt.wantCategory != "" && !strings.Contains(p.Context, "category:"+tt.wantCategory) {
					t.Errorf("pattern %q context = %q, want category:%s", p.Title, p.Context, tt.wantCategory)
				}
				if tt.wantCheckTag != "" && !strings.Contains(p.Context, tt.wantCheckTag) {
					t.Errorf("pattern %q context = %q, want %s", p.Title, p.Context, tt.wantCheckTag)
				}
			}
		})
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

func TestSaveExtractedPatterns_CIRecurrenceBoost(t *testing.T) {
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

	// Simulate first CI failure → extract CI patterns
	ciLogs := "undefined: processItem"
	ciPatterns := extractor.extractCIErrorPatterns(ciLogs)
	if len(ciPatterns) == 0 {
		t.Fatal("expected at least 1 CI error pattern")
	}

	result1 := &ExtractionResult{
		ExecutionID:  "ci-run-1",
		ProjectPath:  "/test/project",
		AntiPatterns: ciPatterns,
		Patterns:     make([]*ExtractedPattern, 0),
	}

	if err := extractor.SaveExtractedPatterns(ctx, result1); err != nil {
		t.Fatalf("first SaveExtractedPatterns failed: %v", err)
	}

	// Record initial confidence
	allPatterns := patternStore.GetByType(ciPatterns[0].Type)
	if len(allPatterns) == 0 {
		t.Fatal("expected pattern to be saved")
	}
	initialConfidence := allPatterns[0].Confidence

	// Simulate same CI failure recurring → extract again
	ciPatterns2 := extractor.extractCIErrorPatterns(ciLogs)
	result2 := &ExtractionResult{
		ExecutionID:  "ci-run-2",
		ProjectPath:  "/test/project",
		AntiPatterns: ciPatterns2,
		Patterns:     make([]*ExtractedPattern, 0),
	}

	if err := extractor.SaveExtractedPatterns(ctx, result2); err != nil {
		t.Fatalf("second SaveExtractedPatterns failed: %v", err)
	}

	// Verify confidence was boosted by 1.5x
	boostedPatterns := patternStore.GetByType(ciPatterns[0].Type)
	var found *GlobalPattern
	for _, p := range boostedPatterns {
		if strings.Contains(p.Title, ciPatterns[0].Title) {
			found = p
			break
		}
	}

	if found == nil {
		t.Fatal("boosted pattern not found")
	}

	expectedConfidence := min(0.95, initialConfidence*1.5)
	if found.Confidence != expectedConfidence {
		t.Errorf("confidence = %f, want %f (initial %f * 1.5)", found.Confidence, expectedConfidence, initialConfidence)
	}

	// Verify no duplicate was created
	count := 0
	for _, p := range boostedPatterns {
		if strings.Contains(p.Title, ciPatterns[0].Title) {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 pattern (deduped), got %d", count)
	}
}

func TestSaveExtractedPatterns_CIRecurrenceBoost_CappedAt095(t *testing.T) {
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

	ciLogs := "undefined: processItem"

	// Save repeatedly to push confidence toward cap
	for i := 0; i < 10; i++ {
		ciPatterns := extractor.extractCIErrorPatterns(ciLogs)
		result := &ExtractionResult{
			ExecutionID:  fmt.Sprintf("ci-run-%d", i),
			ProjectPath:  "/test/project",
			AntiPatterns: ciPatterns,
			Patterns:     make([]*ExtractedPattern, 0),
		}
		if err := extractor.SaveExtractedPatterns(ctx, result); err != nil {
			t.Fatalf("SaveExtractedPatterns iteration %d failed: %v", i, err)
		}
	}

	// Verify confidence is capped at 0.95
	allPatterns := patternStore.GetByType(PatternTypeError)
	for _, p := range allPatterns {
		if p.Confidence > 0.95 {
			t.Errorf("confidence %f exceeds cap of 0.95 for pattern %q", p.Confidence, p.Title)
		}
	}
}

// TestCIPatternRecurrence_EndToEnd is an integration test that validates the full loop:
// CI failure → pattern extracted → same failure recurs → confidence boosted →
// pattern appears in PatternContext.InjectPatterns() output.
func TestCIPatternRecurrence_EndToEnd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-e2e-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	projectPath := "/test/ci-project"
	ciLogs := "--- FAIL: TestProcess (0.05s)\n    process_test.go:42: expected nil, got error"

	// Step 1: First CI failure → extract patterns
	ciPatterns := extractor.extractCIErrorPatterns(ciLogs)
	if len(ciPatterns) == 0 {
		t.Fatal("expected CI error patterns from FAIL log line")
	}
	result1 := &ExtractionResult{
		ExecutionID:  "ci-run-1",
		ProjectPath:  projectPath,
		AntiPatterns: ciPatterns,
		Patterns:     make([]*ExtractedPattern, 0),
	}
	if err := extractor.SaveExtractedPatterns(ctx, result1); err != nil {
		t.Fatalf("first save failed: %v", err)
	}

	// Verify CrossPattern was created in SQLite
	crossPatterns, err := store.GetCrossPatternsForProject(projectPath, true)
	if err != nil {
		t.Fatalf("GetCrossPatternsForProject failed: %v", err)
	}
	if len(crossPatterns) == 0 {
		t.Fatal("expected CrossPattern to be saved to SQLite for CI pattern")
	}
	initialCrossConfidence := crossPatterns[0].Confidence

	// Step 2: Same CI failure recurs → extract and save again
	ciPatterns2 := extractor.extractCIErrorPatterns(ciLogs)
	result2 := &ExtractionResult{
		ExecutionID:  "ci-run-2",
		ProjectPath:  projectPath,
		AntiPatterns: ciPatterns2,
		Patterns:     make([]*ExtractedPattern, 0),
	}
	if err := extractor.SaveExtractedPatterns(ctx, result2); err != nil {
		t.Fatalf("second save failed: %v", err)
	}

	// Step 3: Verify confidence was boosted in CrossPattern
	crossPatternsAfter, err := store.GetCrossPatternsForProject(projectPath, true)
	if err != nil {
		t.Fatalf("GetCrossPatternsForProject after boost failed: %v", err)
	}
	if len(crossPatternsAfter) == 0 {
		t.Fatal("expected CrossPattern after boost")
	}

	// Find the matching pattern
	var boostedCross *CrossPattern
	for _, cp := range crossPatternsAfter {
		if cp.IsAntiPattern {
			boostedCross = cp
			break
		}
	}
	if boostedCross == nil {
		t.Fatal("no anti-pattern CrossPattern found after boost")
	}

	expectedBoostedConfidence := min(0.95, initialCrossConfidence*1.5)
	if boostedCross.Confidence != expectedBoostedConfidence {
		t.Errorf("CrossPattern confidence = %f, want %f (boosted from %f)",
			boostedCross.Confidence, expectedBoostedConfidence, initialCrossConfidence)
	}

	// Step 4: Verify pattern appears in FormatForPrompt output
	queryService := NewPatternQueryService(store)
	promptBlock, err := queryService.FormatForPrompt(ctx, projectPath, "CI test failure fix")
	if err != nil {
		t.Fatalf("FormatForPrompt failed: %v", err)
	}

	if promptBlock == "" {
		t.Fatal("FormatForPrompt returned empty string; expected CI pattern to be injected")
	}

	if !strings.Contains(promptBlock, "Anti-Patterns to Avoid") {
		t.Error("expected 'Anti-Patterns to Avoid' section in prompt block")
	}

	// Verify the specific CI pattern title appears
	if !strings.Contains(promptBlock, "Test failure") {
		t.Errorf("expected 'Test failure' pattern in prompt block, got:\n%s", promptBlock)
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

// TestCodePatternMatcherCount verifies exactly 11 categories are registered.
func TestCodePatternMatcherCount(t *testing.T) {
	if got := len(codePatternMatchers); got != 11 {
		t.Errorf("len(codePatternMatchers) = %d, want 11", got)
	}
}

// TestNewCodePatternCategories verifies the 6 new matcher categories (API Design,
// Concurrency, Config Wiring, Test Patterns, Performance, Security) each fire on
// matching input and stay silent on unrelated input.
func TestNewCodePatternCategories(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "extractor-new-cats-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	patternStore, _ := NewGlobalPatternStore(tmpDir)
	extractor := NewPatternExtractor(patternStore, store)

	tests := []struct {
		name        string
		output      string
		wantTitle   string // substring of expected pattern title
		wantMatch   bool
	}{
		// --- API Design ---
		{
			name:      "api design: http.Handler match",
			output:    "implemented http.Handler interface for the webhook route",
			wantTitle: "API design",
			wantMatch: true,
		},
		{
			name:      "api design: REST endpoint match",
			output:    "added REST endpoint with middleware for rate limiting",
			wantTitle: "API design",
			wantMatch: true,
		},
		{
			name:      "api design: no match",
			output:    "wrote a helper function to format timestamps",
			wantTitle: "API design",
			wantMatch: false,
		},
		// --- Concurrency ---
		{
			name:      "concurrency: sync.Mutex match",
			output:    "protected shared counter using sync.Mutex to avoid data races",
			wantTitle: "Concurrency",
			wantMatch: true,
		},
		{
			name:      "concurrency: go func match",
			output:    "launched background worker via go func with sync.WaitGroup",
			wantTitle: "Concurrency",
			wantMatch: true,
		},
		{
			name:      "concurrency: no match",
			output:    "added a simple sequential loop to process items",
			wantTitle: "Concurrency",
			wantMatch: false,
		},
		// --- Config Wiring ---
		{
			name:      "config wiring: yaml tag match",
			output:    `added yaml:"timeout" struct tag and os.Getenv fallback`,
			wantTitle: "Config wiring",
			wantMatch: true,
		},
		{
			name:      "config wiring: viper match",
			output:    "loading settings via viper.GetString and mapstructure decode",
			wantTitle: "Config wiring",
			wantMatch: true,
		},
		{
			name:      "config wiring: no match",
			output:    "refactored the retry loop to use exponential backoff",
			wantTitle: "Config wiring",
			wantMatch: false,
		},
		// --- Test Patterns ---
		{
			name:      "test patterns: t.Run match",
			output:    "added table-driven tests using t.Run( for each scenario",
			wantTitle: "Test pattern",
			wantMatch: true,
		},
		{
			name:      "test patterns: httptest match",
			output:    "wrote handler test with httptest.NewRecorder and assert.Equal",
			wantTitle: "Test pattern",
			wantMatch: true,
		},
		{
			name:      "test patterns: no match",
			output:    "updated the deployment manifest",
			wantTitle: "Test pattern",
			wantMatch: false,
		},
		// --- Performance ---
		{
			name:      "performance: SetMaxOpenConns match",
			output:    "tuned database pool with SetMaxOpenConns to reduce contention",
			wantTitle: "Performance",
			wantMatch: true,
		},
		{
			name:      "performance: cache match",
			output:    "added in-memory cache layer to avoid redundant database queries",
			wantTitle: "Performance",
			wantMatch: true,
		},
		{
			name:      "performance: no match",
			output:    "formatted the codebase with gofmt",
			wantTitle: "Performance",
			wantMatch: false,
		},
		// --- Security ---
		{
			name:      "security: authentication match",
			output:    "added authentication middleware with token validation",
			wantTitle: "Security",
			wantMatch: true,
		},
		{
			name:      "security: hmac match",
			output:    "signing webhook payloads with hmac and checking permission on every call",
			wantTitle: "Security",
			wantMatch: true,
		},
		{
			name:      "security: no match",
			output:    "added structured logging to the service startup path",
			wantTitle: "Security",
			wantMatch: false,
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

			result, err := extractor.ExtractFromExecution(context.Background(), exec)
			if err != nil {
				t.Fatalf("ExtractFromExecution failed: %v", err)
			}

			found := false
			for _, p := range result.Patterns {
				if strings.Contains(p.Title, tt.wantTitle) {
					found = true
					break
				}
			}

			if found != tt.wantMatch {
				if tt.wantMatch {
					t.Errorf("expected pattern with title containing %q, got none (patterns: %v)",
						tt.wantTitle, patternTitles(result.Patterns))
				} else {
					t.Errorf("expected no pattern with title containing %q, but found one",
						tt.wantTitle)
				}
			}
		})
	}
}

// patternTitles returns the titles of extracted patterns for test diagnostics.
func patternTitles(patterns []*ExtractedPattern) []string {
	titles := make([]string, len(patterns))
	for i, p := range patterns {
		titles[i] = p.Title
	}
	return titles
}
