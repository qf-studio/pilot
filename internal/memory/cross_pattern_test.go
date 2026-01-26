package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCrossPatternCRUD(t *testing.T) {
	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "pilot-test-cross-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Test SaveCrossPattern
	pattern := &CrossPattern{
		ID:          "test_pattern_1",
		Type:        "code",
		Title:       "Use context.Context",
		Description: "Always pass context for cancellation",
		Context:     "Go handlers",
		Examples:    []string{"ctx, cancel := context.WithTimeout(...)"},
		Confidence:  0.75,
		Occurrences: 3,
		Scope:       "org",
	}

	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}

	// Test GetCrossPattern
	retrieved, err := store.GetCrossPattern("test_pattern_1")
	if err != nil {
		t.Fatalf("GetCrossPattern failed: %v", err)
	}

	if retrieved.Title != "Use context.Context" {
		t.Errorf("expected title 'Use context.Context', got '%s'", retrieved.Title)
	}
	if retrieved.Confidence != 0.75 {
		t.Errorf("expected confidence 0.75, got %f", retrieved.Confidence)
	}
	if len(retrieved.Examples) != 1 {
		t.Errorf("expected 1 example, got %d", len(retrieved.Examples))
	}

	// Test GetCrossPatternsByType
	patterns, err := store.GetCrossPatternsByType("code")
	if err != nil {
		t.Fatalf("GetCrossPatternsByType failed: %v", err)
	}
	if len(patterns) != 1 {
		t.Errorf("expected 1 pattern, got %d", len(patterns))
	}

	// Test SearchCrossPatterns
	searchResults, err := store.SearchCrossPatterns("context", 10)
	if err != nil {
		t.Fatalf("SearchCrossPatterns failed: %v", err)
	}
	if len(searchResults) != 1 {
		t.Errorf("expected 1 search result, got %d", len(searchResults))
	}

	// Test DeleteCrossPattern
	if err := store.DeleteCrossPattern("test_pattern_1"); err != nil {
		t.Fatalf("DeleteCrossPattern failed: %v", err)
	}

	_, err = store.GetCrossPattern("test_pattern_1")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}

	_ = ctx
}

func TestPatternProjectLink(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-link-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create a pattern
	pattern := &CrossPattern{
		ID:          "link_test_pattern",
		Type:        "workflow",
		Title:       "Run tests",
		Description: "Always run tests before commit",
		Confidence:  0.8,
		Scope:       "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}

	// Link to multiple projects
	if err := store.LinkPatternToProject("link_test_pattern", "/project/a"); err != nil {
		t.Fatalf("LinkPatternToProject failed: %v", err)
	}
	if err := store.LinkPatternToProject("link_test_pattern", "/project/b"); err != nil {
		t.Fatalf("LinkPatternToProject failed: %v", err)
	}
	// Link same project again (should increment uses)
	if err := store.LinkPatternToProject("link_test_pattern", "/project/a"); err != nil {
		t.Fatalf("LinkPatternToProject (duplicate) failed: %v", err)
	}

	// Get projects for pattern
	links, err := store.GetProjectsForPattern("link_test_pattern")
	if err != nil {
		t.Fatalf("GetProjectsForPattern failed: %v", err)
	}
	if len(links) != 2 {
		t.Errorf("expected 2 project links, got %d", len(links))
	}

	// Check that project/a has 2 uses
	for _, link := range links {
		if link.ProjectPath == "/project/a" && link.Uses != 2 {
			t.Errorf("expected project/a to have 2 uses, got %d", link.Uses)
		}
	}
}

func TestPatternFeedback(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-feedback-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create a pattern
	pattern := &CrossPattern{
		ID:         "feedback_test_pattern",
		Type:       "code",
		Title:      "Error handling",
		Confidence: 0.6,
		Scope:      "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}

	// Link to project
	if err := store.LinkPatternToProject("feedback_test_pattern", "/test/project"); err != nil {
		t.Fatalf("LinkPatternToProject failed: %v", err)
	}

	// Create execution
	exec := &Execution{
		ID:          "exec_1",
		TaskID:      "task_1",
		ProjectPath: "/test/project",
		Status:      "completed",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// Record positive feedback
	feedback := &PatternFeedback{
		PatternID:       "feedback_test_pattern",
		ExecutionID:     "exec_1",
		ProjectPath:     "/test/project",
		Outcome:         "success",
		ConfidenceDelta: 0.1,
	}
	if err := store.RecordPatternFeedback(feedback); err != nil {
		t.Fatalf("RecordPatternFeedback failed: %v", err)
	}

	// Check confidence increased
	updated, err := store.GetCrossPattern("feedback_test_pattern")
	if err != nil {
		t.Fatalf("GetCrossPattern failed: %v", err)
	}
	if updated.Confidence <= 0.6 {
		t.Errorf("expected confidence to increase, got %f", updated.Confidence)
	}
}

func TestCrossPatternStats(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-stats-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create various patterns
	patterns := []*CrossPattern{
		{ID: "p1", Type: "code", Title: "Pattern 1", Confidence: 0.8, Scope: "org"},
		{ID: "p2", Type: "code", Title: "Pattern 2", Confidence: 0.7, Scope: "org"},
		{ID: "p3", Type: "workflow", Title: "Workflow 1", Confidence: 0.9, Scope: "org"},
		{ID: "p4", Type: "error", Title: "Anti 1", Confidence: 0.75, IsAntiPattern: true, Scope: "org"},
	}

	for _, p := range patterns {
		if err := store.SaveCrossPattern(p); err != nil {
			t.Fatalf("SaveCrossPattern failed: %v", err)
		}
	}

	stats, err := store.GetCrossPatternStats()
	if err != nil {
		t.Fatalf("GetCrossPatternStats failed: %v", err)
	}

	if stats.TotalPatterns != 4 {
		t.Errorf("expected 4 total patterns, got %d", stats.TotalPatterns)
	}
	if stats.Patterns != 3 {
		t.Errorf("expected 3 regular patterns, got %d", stats.Patterns)
	}
	if stats.AntiPatterns != 1 {
		t.Errorf("expected 1 anti-pattern, got %d", stats.AntiPatterns)
	}
	if stats.ByType["code"] != 2 {
		t.Errorf("expected 2 code patterns, got %d", stats.ByType["code"])
	}
}

func TestPatternExtractor(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-extractor-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	patternStore, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create pattern store: %v", err)
	}

	extractor := NewPatternExtractor(patternStore, store)
	ctx := context.Background()

	// Create a completed execution with pattern-rich output
	exec := &Execution{
		ID:          "extract_test_1",
		TaskID:      "task_1",
		ProjectPath: "/test/project",
		Status:      "completed",
		Output:      "Using context.Context in handlers. Added error handling for GetUser. Created tests for auth module.",
	}

	result, err := extractor.ExtractFromExecution(ctx, exec)
	if err != nil {
		t.Fatalf("ExtractFromExecution failed: %v", err)
	}

	if len(result.Patterns) == 0 {
		t.Error("expected patterns to be extracted")
	}

	// Test extraction from error output
	failedExec := &Execution{
		ID:          "extract_test_2",
		TaskID:      "task_2",
		ProjectPath: "/test/project",
		Status:      "completed",
		Output:      "Build succeeded",
		Error:       "panic: nil pointer dereference",
	}

	errorResult, err := extractor.ExtractFromExecution(ctx, failedExec)
	if err != nil {
		t.Fatalf("ExtractFromExecution (error) failed: %v", err)
	}

	if len(errorResult.AntiPatterns) == 0 {
		t.Error("expected anti-patterns to be extracted from error")
	}
}

func TestPatternQueryService(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-query-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	// Create test patterns
	patterns := []*CrossPattern{
		{ID: "q1", Type: "code", Title: "High confidence", Confidence: 0.9, Occurrences: 10, Scope: "org"},
		{ID: "q2", Type: "code", Title: "Low confidence", Confidence: 0.4, Occurrences: 2, Scope: "org"},
		{ID: "q3", Type: "workflow", Title: "Medium confidence", Confidence: 0.7, Occurrences: 5, Scope: "org"},
	}

	for _, p := range patterns {
		if err := store.SaveCrossPattern(p); err != nil {
			t.Fatalf("SaveCrossPattern failed: %v", err)
		}
	}

	queryService := NewPatternQueryService(store)
	ctx := context.Background()

	// Test query with minimum confidence
	result, err := queryService.Query(ctx, &PatternQuery{
		MinConfidence: 0.6,
		MaxResults:    10,
	})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Patterns) != 2 {
		t.Errorf("expected 2 patterns with confidence >= 0.6, got %d", len(result.Patterns))
	}

	// Verify sorted by confidence
	if result.Patterns[0].Confidence < result.Patterns[1].Confidence {
		t.Error("patterns should be sorted by confidence descending")
	}

	// Test FormatForPrompt
	promptBlock, err := queryService.FormatForPrompt(ctx, "/test/project", "implementing a new handler")
	if err != nil {
		t.Fatalf("FormatForPrompt failed: %v", err)
	}

	if promptBlock == "" {
		t.Log("No patterns formatted (may be expected if filtering)")
	}
}

func TestOrgPatternStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-org-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	orgStore, err := NewOrgPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create org store: %v", err)
	}

	// Create aggregated pattern
	pattern := &AggregatedPattern{
		ID:           "org_1",
		Type:         "code",
		Title:        "Org Pattern",
		Description:  "A pattern aggregated across projects",
		Confidence:   0.85,
		Occurrences:  15,
		ProjectCount: 3,
		Projects: []ProjectMention{
			{ProjectPath: "/project/a", Uses: 5, SuccessRate: 0.8},
			{ProjectPath: "/project/b", Uses: 7, SuccessRate: 0.9},
			{ProjectPath: "/project/c", Uses: 3, SuccessRate: 0.75},
		},
		CreatedAt: time.Now(),
	}

	if err := orgStore.Update(pattern); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Retrieve
	retrieved, ok := orgStore.Get("org_1")
	if !ok {
		t.Fatal("pattern not found")
	}
	if retrieved.ProjectCount != 3 {
		t.Errorf("expected 3 projects, got %d", retrieved.ProjectCount)
	}

	// Test persistence
	orgStore2, err := NewOrgPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to reload org store: %v", err)
	}

	reloaded, ok := orgStore2.Get("org_1")
	if !ok {
		t.Fatal("pattern not found after reload")
	}
	if reloaded.Title != "Org Pattern" {
		t.Errorf("expected title 'Org Pattern', got '%s'", reloaded.Title)
	}
}

func TestLearningLoop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-learning-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	patternStore, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create pattern store: %v", err)
	}

	extractor := NewPatternExtractor(patternStore, store)
	learner := NewLearningLoop(store, extractor, nil)
	ctx := context.Background()

	// Create a pattern
	pattern := &CrossPattern{
		ID:         "learn_test_1",
		Type:       "code",
		Title:      "Test pattern",
		Confidence: 0.6,
		Scope:      "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}
	if err := store.LinkPatternToProject("learn_test_1", "/test/project"); err != nil {
		t.Fatalf("LinkPatternToProject failed: %v", err)
	}

	// Create execution
	exec := &Execution{
		ID:          "learn_exec_1",
		TaskID:      "task_1",
		ProjectPath: "/test/project",
		Status:      "completed",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution failed: %v", err)
	}

	// Record execution with applied patterns
	if err := learner.RecordExecution(ctx, exec, []string{"learn_test_1"}); err != nil {
		t.Fatalf("RecordExecution failed: %v", err)
	}

	// Check pattern performance
	perf, err := learner.GetPatternPerformance(ctx, "learn_test_1")
	if err != nil {
		t.Fatalf("GetPatternPerformance failed: %v", err)
	}

	if perf.SuccessCount != 1 {
		t.Errorf("expected 1 success count, got %d", perf.SuccessCount)
	}
}

func TestPatternSync(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pilot-test-sync-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	sync, err := NewPatternSync(store, tmpDir)
	if err != nil {
		t.Fatalf("failed to create sync: %v", err)
	}

	ctx := context.Background()

	// Create pattern and links
	pattern := &CrossPattern{
		ID:         "sync_test_1",
		Type:       "code",
		Title:      "Sync test pattern",
		Confidence: 0.7,
		Scope:      "org",
	}
	if err := store.SaveCrossPattern(pattern); err != nil {
		t.Fatalf("SaveCrossPattern failed: %v", err)
	}

	store.LinkPatternToProject("sync_test_1", "/project/a")
	store.LinkPatternToProject("sync_test_1", "/project/b")

	// Sync from project
	if err := sync.SyncFromProject(ctx, "/project/a"); err != nil {
		t.Fatalf("SyncFromProject failed: %v", err)
	}

	// Check aggregated patterns
	orgPatterns := sync.GetOrgPatterns()
	if len(orgPatterns) != 1 {
		t.Errorf("expected 1 org pattern, got %d", len(orgPatterns))
	}

	// Test export
	exportPath := filepath.Join(tmpDir, "export.json")
	if err := sync.ExportPatterns(ctx, exportPath, 0.5); err != nil {
		t.Fatalf("ExportPatterns failed: %v", err)
	}

	// Verify export file exists
	if _, err := os.Stat(exportPath); os.IsNotExist(err) {
		t.Error("export file not created")
	}
}
