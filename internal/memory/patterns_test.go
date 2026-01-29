package memory

import (
	"context"
	"os"
	"testing"
)

func TestNewGlobalPatternStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	if store == nil {
		t.Error("NewGlobalPatternStore() returned nil")
	}

	if store.Count() != 0 {
		t.Errorf("new store Count() = %d, want 0", store.Count())
	}
}

func TestGlobalPatternStore_AddAndGet(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	pattern := &GlobalPattern{
		ID:          "test-pattern-1",
		Type:        PatternTypeCode,
		Title:       "Test Pattern",
		Description: "A test pattern",
		Confidence:  0.9,
	}

	if err := store.Add(pattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got, ok := store.Get("test-pattern-1")
	if !ok {
		t.Fatal("Get() returned ok=false, want true")
	}

	if got.Title != "Test Pattern" {
		t.Errorf("Get().Title = %q, want %q", got.Title, "Test Pattern")
	}

	if got.Uses != 1 {
		t.Errorf("Get().Uses = %d, want 1", got.Uses)
	}
}

func TestGlobalPatternStore_GetByType(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	// Add patterns of different types
	patterns := []*GlobalPattern{
		{ID: "code-1", Type: PatternTypeCode, Title: "Code Pattern 1"},
		{ID: "code-2", Type: PatternTypeCode, Title: "Code Pattern 2"},
		{ID: "naming-1", Type: PatternTypeNaming, Title: "Naming Pattern 1"},
	}

	for _, p := range patterns {
		if err := store.Add(p); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	codePatterns := store.GetByType(PatternTypeCode)
	if len(codePatterns) != 2 {
		t.Errorf("GetByType(Code) returned %d patterns, want 2", len(codePatterns))
	}

	namingPatterns := store.GetByType(PatternTypeNaming)
	if len(namingPatterns) != 1 {
		t.Errorf("GetByType(Naming) returned %d patterns, want 1", len(namingPatterns))
	}
}

func TestGlobalPatternStore_GetForProject(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	// Add global pattern (no projects)
	globalPattern := &GlobalPattern{
		ID:    "global-1",
		Type:  PatternTypeCode,
		Title: "Global Pattern",
	}

	// Add project-specific pattern
	projectPattern := &GlobalPattern{
		ID:       "project-1",
		Type:     PatternTypeCode,
		Title:    "Project Pattern",
		Projects: []string{"/path/to/project"},
	}

	if err := store.Add(globalPattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if err := store.Add(projectPattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Should get both global and project-specific
	patterns := store.GetForProject("/path/to/project")
	if len(patterns) != 2 {
		t.Errorf("GetForProject() returned %d patterns, want 2", len(patterns))
	}

	// Should only get global for different project
	patterns = store.GetForProject("/other/project")
	if len(patterns) != 1 {
		t.Errorf("GetForProject(other) returned %d patterns, want 1", len(patterns))
	}
}

func TestGlobalPatternStore_IncrementUse(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	pattern := &GlobalPattern{
		ID:    "test-increment",
		Type:  PatternTypeCode,
		Title: "Test Pattern",
	}

	if err := store.Add(pattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Initial use count should be 1
	got, _ := store.Get("test-increment")
	if got.Uses != 1 {
		t.Errorf("Initial Uses = %d, want 1", got.Uses)
	}

	// Increment
	if err := store.IncrementUse("test-increment"); err != nil {
		t.Fatalf("IncrementUse() error = %v", err)
	}

	got, _ = store.Get("test-increment")
	if got.Uses != 2 {
		t.Errorf("After increment Uses = %d, want 2", got.Uses)
	}

	// Non-existent pattern should error
	if err := store.IncrementUse("non-existent"); err == nil {
		t.Error("IncrementUse(non-existent) should return error")
	}
}

func TestGlobalPatternStore_Remove(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	pattern := &GlobalPattern{
		ID:    "test-remove",
		Type:  PatternTypeCode,
		Title: "Test Pattern",
	}

	if err := store.Add(pattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if store.Count() != 1 {
		t.Errorf("Count() after add = %d, want 1", store.Count())
	}

	if err := store.Remove("test-remove"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if store.Count() != 0 {
		t.Errorf("Count() after remove = %d, want 0", store.Count())
	}

	_, ok := store.Get("test-remove")
	if ok {
		t.Error("Get() after remove returned ok=true, want false")
	}
}

func TestGlobalPatternStore_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create store and add pattern
	store1, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	pattern := &GlobalPattern{
		ID:          "persist-test",
		Type:        PatternTypeWorkflow,
		Title:       "Persistent Pattern",
		Description: "Should survive reload",
	}

	if err := store1.Add(pattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Create new store from same path
	store2, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() reload error = %v", err)
	}

	got, ok := store2.Get("persist-test")
	if !ok {
		t.Fatal("Get() after reload returned ok=false")
	}

	if got.Title != "Persistent Pattern" {
		t.Errorf("Title after reload = %q, want %q", got.Title, "Persistent Pattern")
	}

	if got.Description != "Should survive reload" {
		t.Errorf("Description after reload = %q, want %q", got.Description, "Should survive reload")
	}
}

func TestGlobalPatternStore_UpdateExistingPattern(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	// Add initial pattern
	pattern := &GlobalPattern{
		ID:          "update-test",
		Type:        PatternTypeCode,
		Title:       "Original Title",
		Description: "Original Description",
	}

	if err := store.Add(pattern); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got, _ := store.Get("update-test")
	originalCreatedAt := got.CreatedAt

	// Update pattern
	updatedPattern := &GlobalPattern{
		ID:          "update-test",
		Type:        PatternTypeCode,
		Title:       "Updated Title",
		Description: "Updated Description",
	}

	if err := store.Add(updatedPattern); err != nil {
		t.Fatalf("Add() update error = %v", err)
	}

	got, _ = store.Get("update-test")

	if got.Title != "Updated Title" {
		t.Errorf("Title after update = %q, want %q", got.Title, "Updated Title")
	}

	if got.Uses != 2 {
		t.Errorf("Uses after update = %d, want 2", got.Uses)
	}

	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Error("CreatedAt should be preserved on update")
	}
}

func TestPatternLearner_LearnFromExecution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "patterns-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	patternStore, err := NewGlobalPatternStore(tmpDir)
	if err != nil {
		t.Fatalf("NewGlobalPatternStore() error = %v", err)
	}

	// Create memory store for executions
	memStore, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	defer func() { _ = memStore.Close() }()

	learner := NewPatternLearner(patternStore, memStore)

	// Test with non-completed execution (should skip)
	exec := &Execution{
		ID:     "test-exec-1",
		Status: "running",
	}

	if err := learner.LearnFromExecution(context.Background(), exec); err != nil {
		t.Errorf("LearnFromExecution() on running exec error = %v", err)
	}

	// Verify no patterns learned from incomplete execution
	if patternStore.Count() != 0 {
		t.Errorf("PatternStore.Count() = %d after incomplete exec, want 0", patternStore.Count())
	}
}

func TestPatternTypeConstants(t *testing.T) {
	// Verify pattern type constants are correctly defined
	types := map[PatternType]string{
		PatternTypeCode:      "code",
		PatternTypeStructure: "structure",
		PatternTypeNaming:    "naming",
		PatternTypeWorkflow:  "workflow",
		PatternTypeError:     "error",
	}

	for pType, expected := range types {
		if string(pType) != expected {
			t.Errorf("PatternType %v = %q, want %q", pType, pType, expected)
		}
	}
}
