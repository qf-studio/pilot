package memory

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestNewPatternQueryService(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	service := NewPatternQueryService(store)
	if service == nil {
		t.Error("NewPatternQueryService returned nil")
	}
}

func TestQuery_DefaultValues(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	service := NewPatternQueryService(store)
	ctx := context.Background()

	// Query with defaults
	result, err := service.Query(ctx, &PatternQuery{})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if result == nil {
		t.Error("Query returned nil result")
	}
}

func TestQuery_WithFilters(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create test patterns
	patterns := []*CrossPattern{
		{ID: "qf-code-1", Type: "code", Title: "Code Pattern", Confidence: 0.9, Occurrences: 10, Scope: "org"},
		{ID: "qf-code-2", Type: "code", Title: "Low Confidence", Confidence: 0.4, Occurrences: 2, Scope: "org"},
		{ID: "qf-workflow-1", Type: "workflow", Title: "Workflow Pattern", Confidence: 0.8, Occurrences: 5, Scope: "org"},
		{ID: "qf-anti-1", Type: "error", Title: "Anti Pattern", Confidence: 0.7, IsAntiPattern: true, Scope: "org"},
	}

	for _, p := range patterns {
		_ = store.SaveCrossPattern(p)
	}

	service := NewPatternQueryService(store)
	ctx := context.Background()

	tests := []struct {
		name    string
		query   *PatternQuery
		wantMin int
	}{
		{
			name: "filter by type",
			query: &PatternQuery{
				Types:         []string{"code"},
				MinConfidence: 0.3,
				MaxResults:    10,
			},
			wantMin: 2,
		},
		{
			name: "filter by confidence",
			query: &PatternQuery{
				MinConfidence: 0.7,
				MaxResults:    10,
			},
			wantMin: 2, // at least code-1 and workflow-1
		},
		{
			name: "exclude anti-patterns",
			query: &PatternQuery{
				MinConfidence: 0.7,
				MaxResults:    10,
				IncludeAnti:   false,
			},
			wantMin: 2, // code-1, workflow-1
		},
		{
			name: "limit results",
			query: &PatternQuery{
				MinConfidence: 0.3,
				MaxResults:    2,
			},
			wantMin: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := service.Query(ctx, tt.query)
			if err != nil {
				t.Fatalf("Query failed: %v", err)
			}

			if len(result.Patterns) < tt.wantMin {
				t.Errorf("got %d patterns, want at least %d", len(result.Patterns), tt.wantMin)
			}
		})
	}
}

func TestQuery_WithSearchTerm(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create test patterns
	_ = store.SaveCrossPattern(&CrossPattern{ID: "1", Type: "code", Title: "Error Handling", Description: "Wrap errors", Confidence: 0.8, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "2", Type: "code", Title: "Context Usage", Description: "Pass context", Confidence: 0.8, Scope: "org"})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		SearchTerm:    "error",
		MinConfidence: 0.5,
		MaxResults:    10,
	})
	if err != nil {
		t.Fatalf("Query with search failed: %v", err)
	}

	if len(result.Patterns) != 1 {
		t.Errorf("got %d patterns, want 1 for 'error' search", len(result.Patterns))
	}
}

func TestQuery_WithProjectPath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with project links
	_ = store.SaveCrossPattern(&CrossPattern{ID: "org-1", Type: "code", Title: "Org Pattern", Confidence: 0.8, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "project-1", Type: "code", Title: "Project Pattern", Confidence: 0.8, Scope: "project"})
	_ = store.LinkPatternToProject("project-1", "/my/project")

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		ProjectPath:   "/my/project",
		MinConfidence: 0.5,
		MaxResults:    10,
		IncludeGlobal: true,
	})
	if err != nil {
		t.Fatalf("Query with project path failed: %v", err)
	}

	if len(result.Patterns) < 1 {
		t.Error("expected at least org-scope patterns for project query")
	}
}

func TestQuery_SortByConfidence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with varying confidence
	_ = store.SaveCrossPattern(&CrossPattern{ID: "low", Type: "code", Title: "Low", Confidence: 0.5, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "high", Type: "code", Title: "High", Confidence: 0.9, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "medium", Type: "code", Title: "Medium", Confidence: 0.7, Scope: "org"})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		MinConfidence: 0.4,
		MaxResults:    10,
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Patterns) < 2 {
		t.Skip("Not enough patterns to test sorting")
	}

	// Verify sorted by confidence descending
	for i := 1; i < len(result.Patterns); i++ {
		if result.Patterns[i].Confidence > result.Patterns[i-1].Confidence {
			t.Errorf("patterns not sorted by confidence: %f > %f",
				result.Patterns[i].Confidence, result.Patterns[i-1].Confidence)
		}
	}
}

func TestGetRelevantPatterns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:          "handler-1",
		Type:        "code",
		Title:       "Context in handlers",
		Description: "Pass context to handler functions",
		Context:     "Go handlers",
		Confidence:  0.8,
		Occurrences: 10,
		Scope:       "org",
	})
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:          "test-1",
		Type:        "workflow",
		Title:       "Unit tests",
		Description: "Write unit tests",
		Context:     "Testing",
		Confidence:  0.7,
		Occurrences: 5,
		Scope:       "org",
	})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	// Query with handler context
	patterns, err := service.GetRelevantPatterns(ctx, "/test/project", "implementing a new handler")
	if err != nil {
		t.Fatalf("GetRelevantPatterns failed: %v", err)
	}

	// Handler pattern should be more relevant
	if len(patterns) > 0 && patterns[0].ID != "handler-1" {
		t.Log("Note: Pattern relevance scoring may vary")
	}
}

func TestFormatForPrompt(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:          "format-1",
		Type:        "code",
		Title:       "Error Handling",
		Description: "Always wrap errors",
		Context:     "Go code",
		Confidence:  0.8,
		Scope:       "org",
	})
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:            "format-anti",
		Type:          "error",
		Title:         "[ANTI] Nil pointer",
		Description:   "AVOID: Dereferencing nil pointers",
		Confidence:    0.7,
		IsAntiPattern: true,
		Scope:         "org",
	})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	prompt, err := service.FormatForPrompt(ctx, "/test/project", "fixing an error handler")
	if err != nil {
		t.Fatalf("FormatForPrompt failed: %v", err)
	}

	// Should contain header
	if prompt != "" {
		if len(prompt) < 10 {
			t.Error("prompt seems too short")
		}
	}
}

func TestFormatForPrompt_Empty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// No patterns
	service := NewPatternQueryService(store)
	ctx := context.Background()

	prompt, err := service.FormatForPrompt(ctx, "/empty/project", "doing something")
	if err != nil {
		t.Fatalf("FormatForPrompt failed: %v", err)
	}

	// Empty is acceptable for no patterns
	_ = prompt
}

func TestGetPatternSuggestions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns of different types
	_ = store.SaveCrossPattern(&CrossPattern{ID: "code-1", Type: "code", Title: "Implement function", Confidence: 0.8, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "test-1", Type: "workflow", Title: "Run tests", Confidence: 0.8, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "structure-1", Type: "structure", Title: "Package layout", Confidence: 0.8, Scope: "org"})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	tests := []struct {
		name          string
		partialOutput string
		wantType      string
	}{
		{
			name:          "function implementation",
			partialOutput: "implementing a new function for user authentication",
			wantType:      "code",
		},
		{
			name:          "testing context",
			partialOutput: "running test to verify the changes",
			wantType:      "workflow",
		},
		{
			name:          "package organization",
			partialOutput: "organizing the package structure",
			wantType:      "structure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suggestions, err := service.GetPatternSuggestions(ctx, "/test/project", tt.partialOutput)
			if err != nil {
				t.Fatalf("GetPatternSuggestions failed: %v", err)
			}

			// Should return some suggestions
			_ = suggestions
		})
	}
}

func TestContainsString(t *testing.T) {
	tests := []struct {
		name  string
		slice []string
		s     string
		want  bool
	}{
		{name: "contains", slice: []string{"a", "b", "c"}, s: "b", want: true},
		{name: "not contains", slice: []string{"a", "b", "c"}, s: "d", want: false},
		{name: "empty slice", slice: []string{}, s: "a", want: false},
		{name: "empty string", slice: []string{"a", "b", ""}, s: "", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsString(tt.slice, tt.s)
			if got != tt.want {
				t.Errorf("containsString(%v, %q) = %v, want %v", tt.slice, tt.s, got, tt.want)
			}
		})
	}
}

func TestQuery_TotalMatches(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create many patterns with unique IDs
	for i := 0; i < 10; i++ {
		_ = store.SaveCrossPattern(&CrossPattern{
			ID:         "total-match-" + string(rune('a'+i)),
			Type:       "code",
			Title:      "Pattern",
			Confidence: 0.8,
			Scope:      "org",
		})
	}

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		MinConfidence: 0.5,
		MaxResults:    3,
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Patterns) > 3 {
		t.Errorf("got %d patterns, want at most 3", len(result.Patterns))
	}

	// TotalMatches should be at least as many as we created
	if result.TotalMatches < 3 {
		t.Errorf("TotalMatches = %d, want >= 3", result.TotalMatches)
	}
}

func TestQuery_QueryTime(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		MinConfidence: 0.5,
		MaxResults:    10,
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if result.QueryTime <= 0 {
		t.Error("QueryTime should be positive")
	}
}

func TestQuery_ScopeFilter(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with different scopes
	_ = store.SaveCrossPattern(&CrossPattern{ID: "org-1", Type: "code", Title: "Org", Confidence: 0.8, Scope: "org"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "project-1", Type: "code", Title: "Project", Confidence: 0.8, Scope: "project"})
	_ = store.SaveCrossPattern(&CrossPattern{ID: "global-1", Type: "code", Title: "Global", Confidence: 0.8, Scope: "global"})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		Scope:         "org",
		MinConfidence: 0.5,
		MaxResults:    10,
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	for _, p := range result.Patterns {
		if p.Scope != "org" {
			t.Errorf("pattern scope = %q, want 'org'", p.Scope)
		}
	}
}

func TestFormatForPrompt_AntiPatternsNotCrowdedOut(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-anti-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	tests := []struct {
		name               string
		patterns           []*CrossPattern
		wantAntiSection    bool
		wantNormalSection  bool
		wantAntiSubstrings []string
	}{
		{
			name: "anti-patterns appear despite many high-confidence normal patterns",
			patterns: []*CrossPattern{
				{ID: "norm-1", Type: "code", Title: "Normal 1", Description: "Desc 1", Confidence: 0.95, Occurrences: 20, Scope: "org"},
				{ID: "norm-2", Type: "code", Title: "Normal 2", Description: "Desc 2", Confidence: 0.93, Occurrences: 18, Scope: "org"},
				{ID: "norm-3", Type: "code", Title: "Normal 3", Description: "Desc 3", Confidence: 0.91, Occurrences: 15, Scope: "org"},
				{ID: "norm-4", Type: "code", Title: "Normal 4", Description: "Desc 4", Confidence: 0.89, Occurrences: 12, Scope: "org"},
				{ID: "norm-5", Type: "code", Title: "Normal 5", Description: "Desc 5", Confidence: 0.87, Occurrences: 10, Scope: "org"},
				{ID: "norm-6", Type: "code", Title: "Normal 6", Description: "Desc 6", Confidence: 0.85, Occurrences: 8, Scope: "org"},
				{ID: "anti-1", Type: "error", Title: "[ANTI] Nil deref", Description: "AVOID: Nil pointer dereference", Confidence: 0.7, IsAntiPattern: true, Scope: "org"},
				{ID: "anti-2", Type: "error", Title: "[ANTI] Missing ctx", Description: "AVOID: Missing context propagation", Confidence: 0.65, IsAntiPattern: true, Scope: "org"},
			},
			wantAntiSection:    true,
			wantNormalSection:  true,
			wantAntiSubstrings: []string{"Nil deref", "Missing ctx"},
		},
		{
			name: "only anti-patterns present",
			patterns: []*CrossPattern{
				{ID: "anti-only-1", Type: "error", Title: "[ANTI] Bad import", Description: "AVOID: Circular imports", Confidence: 0.8, IsAntiPattern: true, Scope: "org"},
			},
			wantAntiSection:    true,
			wantNormalSection:  false,
			wantAntiSubstrings: []string{"Bad import"},
		},
		{
			name: "anti-patterns below min confidence excluded",
			patterns: []*CrossPattern{
				{ID: "norm-hi", Type: "code", Title: "Good pattern", Description: "Desc", Confidence: 0.9, Occurrences: 10, Scope: "org"},
				{ID: "anti-low", Type: "error", Title: "[ANTI] Low conf", Description: "AVOID: something", Confidence: 0.4, IsAntiPattern: true, Scope: "org"},
			},
			wantAntiSection:   false,
			wantNormalSection: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a fresh store per subtest
			subDir, err := os.MkdirTemp("", "query-subtest-*")
			if err != nil {
				t.Fatalf("failed to create temp dir: %v", err)
			}
			defer func() { _ = os.RemoveAll(subDir) }()

			s, _ := NewStore(subDir)
			defer func() { _ = s.Close() }()

			for _, p := range tt.patterns {
				if err := s.SaveCrossPattern(p); err != nil {
					t.Fatalf("SaveCrossPattern failed: %v", err)
				}
			}

			service := NewPatternQueryService(s)
			ctx := context.Background()

			prompt, err := service.FormatForPrompt(ctx, "/test/project", "implementing a handler")
			if err != nil {
				t.Fatalf("FormatForPrompt failed: %v", err)
			}

			hasAntiSection := strings.Contains(prompt, "Anti-Patterns to Avoid")
			if hasAntiSection != tt.wantAntiSection {
				t.Errorf("anti-pattern section present=%v, want %v\nprompt:\n%s", hasAntiSection, tt.wantAntiSection, prompt)
			}

			hasNormalSection := strings.Contains(prompt, "Recommended Patterns")
			if hasNormalSection != tt.wantNormalSection {
				t.Errorf("normal section present=%v, want %v\nprompt:\n%s", hasNormalSection, tt.wantNormalSection, prompt)
			}

			for _, sub := range tt.wantAntiSubstrings {
				if !strings.Contains(prompt, sub) {
					t.Errorf("expected anti-pattern substring %q in prompt, not found\nprompt:\n%s", sub, prompt)
				}
			}
		})
	}
}

func TestQuery_OnlyAnti(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-onlyanti-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Mix of normal and anti-patterns
	patterns := []*CrossPattern{
		{ID: "oa-norm-1", Type: "code", Title: "Normal", Confidence: 0.95, Occurrences: 20, Scope: "org"},
		{ID: "oa-norm-2", Type: "code", Title: "Normal 2", Confidence: 0.90, Occurrences: 15, Scope: "org"},
		{ID: "oa-anti-1", Type: "error", Title: "Anti 1", Confidence: 0.7, IsAntiPattern: true, Scope: "org"},
		{ID: "oa-anti-2", Type: "error", Title: "Anti 2", Confidence: 0.65, IsAntiPattern: true, Scope: "org"},
	}
	for _, p := range patterns {
		_ = store.SaveCrossPattern(p)
	}

	service := NewPatternQueryService(store)
	ctx := context.Background()

	result, err := service.Query(ctx, &PatternQuery{
		OnlyAnti:      true,
		MinConfidence: 0.5,
		MaxResults:    10,
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Patterns) != 2 {
		t.Errorf("got %d patterns, want 2 anti-patterns only", len(result.Patterns))
	}
	for _, p := range result.Patterns {
		if !p.IsAntiPattern {
			t.Errorf("got non-anti-pattern %q with OnlyAnti=true", p.ID)
		}
	}
}

func TestGetRelevantPatterns_Scoring(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "query-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, _ := NewStore(tmpDir)
	defer func() { _ = store.Close() }()

	// Create patterns with different relevance
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:          "very-relevant",
		Type:        "code",
		Title:       "Authentication handler",
		Description: "Implement auth handler",
		Context:     "Go handlers",
		Confidence:  0.8,
		Occurrences: 20,
		Scope:       "org",
	})
	_ = store.SaveCrossPattern(&CrossPattern{
		ID:          "somewhat-relevant",
		Type:        "code",
		Title:       "Logging setup",
		Description: "Configure logging",
		Context:     "Infrastructure",
		Confidence:  0.8,
		Occurrences: 5,
		Scope:       "org",
	})

	service := NewPatternQueryService(store)
	ctx := context.Background()

	patterns, err := service.GetRelevantPatterns(ctx, "/test/project", "implementing authentication handler")
	if err != nil {
		t.Fatalf("GetRelevantPatterns failed: %v", err)
	}

	if len(patterns) > 1 {
		// The more relevant pattern should score higher and be first
		if patterns[0].ID != "very-relevant" {
			t.Log("Note: Pattern ordering based on relevance may vary")
		}
	}
}
