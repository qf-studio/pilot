package memory

import (
	"os"
	"testing"
	"time"
)

func TestNewKnowledgeGraph(t *testing.T) {
	t.Run("create new graph in empty directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "kg-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()

		kg, err := NewKnowledgeGraph(tmpDir)
		if err != nil {
			t.Errorf("NewKnowledgeGraph() error = %v", err)
			return
		}

		if kg == nil {
			t.Error("NewKnowledgeGraph() returned nil without error")
		}
	})

	t.Run("load existing graph", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "kg-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()

		// Create and populate a graph
		kg1, err := NewKnowledgeGraph(tmpDir)
		if err != nil {
			t.Fatalf("failed to create first graph: %v", err)
		}

		node := &GraphNode{
			ID:      "test-node",
			Type:    "pattern",
			Title:   "Test Node",
			Content: "Test content",
		}
		if err := kg1.Add(node); err != nil {
			t.Fatalf("Add() error = %v", err)
		}

		// Load a new graph from the same path
		kg2, err := NewKnowledgeGraph(tmpDir)
		if err != nil {
			t.Fatalf("failed to load existing graph: %v", err)
		}

		if kg2.Count() != 1 {
			t.Errorf("loaded graph Count() = %d, want 1", kg2.Count())
		}
	})
}

func TestKnowledgeGraph_AddAndGet(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	node := &GraphNode{
		ID:      "test-add",
		Type:    "learning",
		Title:   "Test Learning",
		Content: "This is a test learning node",
	}

	if err := kg.Add(node); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got, ok := kg.Get("test-add")
	if !ok {
		t.Fatal("Get() returned ok=false, want true")
	}

	if got.Title != "Test Learning" {
		t.Errorf("Get().Title = %q, want %q", got.Title, "Test Learning")
	}

	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestKnowledgeGraph_Search(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	nodes := []*GraphNode{
		{ID: "n1", Type: "pattern", Title: "Error Handling", Content: "How to handle errors"},
		{ID: "n2", Type: "pattern", Title: "Logging Best Practices", Content: "Structured logging"},
		{ID: "n3", Type: "learning", Title: "Testing Strategies", Content: "Error handling in tests"},
	}

	for _, n := range nodes {
		if err := kg.Add(n); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	// Search by title
	results := kg.Search("Error")
	if len(results) != 2 {
		t.Errorf("Search('Error') returned %d results, want 2", len(results))
	}

	// Search by content
	results = kg.Search("structured")
	if len(results) != 1 {
		t.Errorf("Search('structured') returned %d results, want 1", len(results))
	}

	// Search by type
	results = kg.Search("learning")
	if len(results) != 1 {
		t.Errorf("Search('learning') returned %d results, want 1", len(results))
	}
}

func TestKnowledgeGraph_GetByType(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	nodes := []*GraphNode{
		{ID: "p1", Type: "pattern", Title: "Pattern 1"},
		{ID: "p2", Type: "pattern", Title: "Pattern 2"},
		{ID: "l1", Type: "learning", Title: "Learning 1"},
	}

	for _, n := range nodes {
		if err := kg.Add(n); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	patterns := kg.GetByType("pattern")
	if len(patterns) != 2 {
		t.Errorf("GetByType('pattern') returned %d results, want 2", len(patterns))
	}

	learnings := kg.GetByType("learning")
	if len(learnings) != 1 {
		t.Errorf("GetByType('learning') returned %d results, want 1", len(learnings))
	}
}

func TestKnowledgeGraph_GetRelated(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	// Add nodes with relations
	nodeA := &GraphNode{
		ID:        "node-a",
		Type:      "pattern",
		Title:     "Node A",
		Relations: []string{"node-b", "node-c"},
	}
	nodeB := &GraphNode{ID: "node-b", Type: "pattern", Title: "Node B"}
	nodeC := &GraphNode{ID: "node-c", Type: "pattern", Title: "Node C"}

	for _, n := range []*GraphNode{nodeA, nodeB, nodeC} {
		if err := kg.Add(n); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	related := kg.GetRelated("node-a")
	if len(related) != 2 {
		t.Errorf("GetRelated('node-a') returned %d results, want 2", len(related))
	}

	// Non-existent node returns nil
	related = kg.GetRelated("non-existent")
	if related != nil {
		t.Errorf("GetRelated('non-existent') returned %v, want nil", related)
	}
}

func TestKnowledgeGraph_Remove(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	node := &GraphNode{ID: "to-remove", Type: "pattern", Title: "Remove Me"}
	if err := kg.Add(node); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	if kg.Count() != 1 {
		t.Errorf("Count() after add = %d, want 1", kg.Count())
	}

	if err := kg.Remove("to-remove"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	if kg.Count() != 0 {
		t.Errorf("Count() after remove = %d, want 0", kg.Count())
	}

	_, ok := kg.Get("to-remove")
	if ok {
		t.Error("Get() after remove returned ok=true, want false")
	}
}

func TestKnowledgeGraph_Count(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("failed to create knowledge graph: %v", err)
	}

	if count := kg.Count(); count != 0 {
		t.Errorf("Count() = %d, want 0 for empty graph", count)
	}

	// Add nodes and verify count
	for i := 0; i < 3; i++ {
		node := &GraphNode{ID: string(rune('a' + i)), Type: "test", Title: "Test"}
		if err := kg.Add(node); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	if count := kg.Count(); count != 3 {
		t.Errorf("Count() = %d, want 3", count)
	}
}

func TestKnowledgeGraph_AddPattern(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	metadata := map[string]interface{}{"language": "go"}
	if err := kg.AddPattern("error_handling", "Always check errors", metadata); err != nil {
		t.Fatalf("AddPattern() error = %v", err)
	}

	patterns := kg.GetPatterns()
	if len(patterns) != 1 {
		t.Fatalf("GetPatterns() returned %d, want 1", len(patterns))
	}

	if patterns[0].Title != "error_handling" {
		t.Errorf("Pattern Title = %q, want 'error_handling'", patterns[0].Title)
	}

	if patterns[0].Content != "Always check errors" {
		t.Errorf("Pattern Content = %q, want 'Always check errors'", patterns[0].Content)
	}
}

func TestKnowledgeGraph_AddLearning(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	metadata := map[string]interface{}{"project": "pilot"}
	if err := kg.AddLearning("Context cancellation", "Always propagate context", metadata); err != nil {
		t.Fatalf("AddLearning() error = %v", err)
	}

	learnings := kg.GetLearnings()
	if len(learnings) != 1 {
		t.Fatalf("GetLearnings() returned %d, want 1", len(learnings))
	}

	if learnings[0].Title != "Context cancellation" {
		t.Errorf("Learning Title = %q, want 'Context cancellation'", learnings[0].Title)
	}
}

func TestKnowledgeGraph_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create and populate graph
	kg1, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	node := &GraphNode{
		ID:      "persist-test",
		Type:    "learning",
		Title:   "Persistent Node",
		Content: "Should survive reload",
	}
	if err := kg1.Add(node); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	// Load new graph from same path
	kg2, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() reload error = %v", err)
	}

	got, ok := kg2.Get("persist-test")
	if !ok {
		t.Fatal("Get() after reload returned ok=false")
	}

	if got.Title != "Persistent Node" {
		t.Errorf("Title after reload = %q, want %q", got.Title, "Persistent Node")
	}

	if got.Content != "Should survive reload" {
		t.Errorf("Content after reload = %q, want %q", got.Content, "Should survive reload")
	}
}

func TestKnowledgeGraph_UpdateExistingNode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	// Add initial node
	node := &GraphNode{
		ID:      "update-test",
		Type:    "pattern",
		Title:   "Original Title",
		Content: "Original Content",
	}
	if err := kg.Add(node); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	got, _ := kg.Get("update-test")
	originalCreatedAt := got.CreatedAt

	// Update node
	updatedNode := &GraphNode{
		ID:      "update-test",
		Type:    "pattern",
		Title:   "Updated Title",
		Content: "Updated Content",
	}
	if err := kg.Add(updatedNode); err != nil {
		t.Fatalf("Add() update error = %v", err)
	}

	got, _ = kg.Get("update-test")

	if got.Title != "Updated Title" {
		t.Errorf("Title after update = %q, want %q", got.Title, "Updated Title")
	}

	if !got.CreatedAt.Equal(originalCreatedAt) {
		t.Error("CreatedAt should be preserved on update")
	}

	if !got.UpdatedAt.After(originalCreatedAt) {
		t.Error("UpdatedAt should be after CreatedAt after update")
	}
}

func TestKnowledgeGraph_AddExecutionLearning(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	files := []string{"internal/executor/runner.go", "internal/executor/monitor.go"}
	patterns := []string{"error-handling", "retry-logic"}
	outcome := "success"

	if err := kg.AddExecutionLearning("Fix timeout bug", "Added retry with backoff", files, patterns, outcome); err != nil {
		t.Fatalf("AddExecutionLearning() error = %v", err)
	}

	// Should have: 1 learning + 2 file nodes + 2 pattern nodes + 1 outcome = 6
	if count := kg.Count(); count != 6 {
		t.Errorf("Count() = %d, want 6", count)
	}

	// Verify the learning node has relations
	learnings := kg.GetByType("execution_learning")
	if len(learnings) != 1 {
		t.Fatalf("GetByType('execution_learning') returned %d, want 1", len(learnings))
	}

	learning := learnings[0]
	if learning.Title != "Fix timeout bug" {
		t.Errorf("Title = %q, want %q", learning.Title, "Fix timeout bug")
	}
	if learning.Content != "Added retry with backoff" {
		t.Errorf("Content = %q, want %q", learning.Content, "Added retry with backoff")
	}
	// 2 files + 2 patterns + 1 outcome = 5 relations
	if len(learning.Relations) != 5 {
		t.Errorf("Relations count = %d, want 5", len(learning.Relations))
	}

	// Verify related nodes are retrievable
	related := kg.GetRelated(learning.ID)
	if len(related) != 5 {
		t.Errorf("GetRelated() returned %d, want 5", len(related))
	}

	// Verify metadata
	if learning.Metadata["outcome"] != "success" {
		t.Errorf("Metadata[outcome] = %v, want %q", learning.Metadata["outcome"], "success")
	}

	// Verify file nodes exist
	fileNodes := kg.GetByType("file")
	if len(fileNodes) != 2 {
		t.Errorf("GetByType('file') returned %d, want 2", len(fileNodes))
	}

	// Verify outcome node exists
	outcomeNodes := kg.GetByType("outcome")
	if len(outcomeNodes) != 1 {
		t.Errorf("GetByType('outcome') returned %d, want 1", len(outcomeNodes))
	}
	if outcomeNodes[0].Title != "success" {
		t.Errorf("Outcome Title = %q, want %q", outcomeNodes[0].Title, "success")
	}
}

func TestKnowledgeGraph_AddExecutionLearning_EmptyInputs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	// Empty files and patterns — should still create learning + outcome
	if err := kg.AddExecutionLearning("Minimal learning", "No files changed", nil, nil, "success"); err != nil {
		t.Fatalf("AddExecutionLearning() error = %v", err)
	}

	// 1 learning + 1 outcome = 2
	if count := kg.Count(); count != 2 {
		t.Errorf("Count() = %d, want 2", count)
	}

	learnings := kg.GetByType("execution_learning")
	if len(learnings) != 1 {
		t.Fatalf("GetByType('execution_learning') returned %d, want 1", len(learnings))
	}
	// Only the outcome relation
	if len(learnings[0].Relations) != 1 {
		t.Errorf("Relations count = %d, want 1", len(learnings[0].Relations))
	}
}

func TestKnowledgeGraph_GetRelatedByKeywords(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	kg, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	// Add execution learnings with a small time gap to ensure ordering
	files1 := []string{"internal/executor/runner.go"}
	if err := kg.AddExecutionLearning("Fix timeout bug", "Added retry with exponential backoff", files1, []string{"retry"}, "success"); err != nil {
		t.Fatalf("AddExecutionLearning() error = %v", err)
	}

	// Small delay to ensure different timestamps
	time.Sleep(10 * time.Millisecond)

	files2 := []string{"internal/gateway/server.go"}
	if err := kg.AddExecutionLearning("Add WebSocket auth", "Token validation for WebSocket connections", files2, []string{"auth"}, "success"); err != nil {
		t.Fatalf("AddExecutionLearning() error = %v", err)
	}

	t.Run("search by title keyword", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"timeout"})
		if len(results) == 0 {
			t.Fatal("expected at least 1 result for keyword 'timeout'")
		}
		found := false
		for _, r := range results {
			if r.Title == "Fix timeout bug" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected to find 'Fix timeout bug' in results")
		}
	})

	t.Run("search by content keyword", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"backoff"})
		if len(results) == 0 {
			t.Fatal("expected at least 1 result for keyword 'backoff'")
		}
	})

	t.Run("search by metadata keyword", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"runner.go"})
		if len(results) == 0 {
			t.Fatal("expected at least 1 result for keyword 'runner.go'")
		}
	})

	t.Run("case insensitive search", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"WEBSOCKET"})
		if len(results) == 0 {
			t.Fatal("expected at least 1 result for case-insensitive 'WEBSOCKET'")
		}
	})

	t.Run("multiple keywords match union", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"timeout", "WebSocket"})
		// Should find nodes from both learnings
		if len(results) < 2 {
			t.Errorf("expected at least 2 results for multiple keywords, got %d", len(results))
		}
	})

	t.Run("sorted by recency", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"success"})
		if len(results) < 2 {
			t.Fatalf("expected at least 2 results, got %d", len(results))
		}
		// First result should be more recent than second
		if results[0].UpdatedAt.Before(results[1].UpdatedAt) {
			t.Error("results not sorted by recency (newest first)")
		}
	})

	t.Run("empty keywords returns nil", func(t *testing.T) {
		results := kg.GetRelatedByKeywords(nil)
		if results != nil {
			t.Errorf("expected nil for empty keywords, got %d results", len(results))
		}

		results = kg.GetRelatedByKeywords([]string{})
		if results != nil {
			t.Errorf("expected nil for empty slice, got %d results", len(results))
		}
	})

	t.Run("no matches returns empty", func(t *testing.T) {
		results := kg.GetRelatedByKeywords([]string{"nonexistent-keyword-xyz"})
		if len(results) != 0 {
			t.Errorf("expected 0 results for non-matching keyword, got %d", len(results))
		}
	})
}

func TestKnowledgeGraph_AddExecutionLearning_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kg-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create graph and add execution learning
	kg1, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() error = %v", err)
	}

	if err := kg1.AddExecutionLearning("Persistent learning", "Should survive reload", []string{"file.go"}, []string{"pattern-a"}, "success"); err != nil {
		t.Fatalf("AddExecutionLearning() error = %v", err)
	}

	// Reload from disk
	kg2, err := NewKnowledgeGraph(tmpDir)
	if err != nil {
		t.Fatalf("NewKnowledgeGraph() reload error = %v", err)
	}

	learnings := kg2.GetByType("execution_learning")
	if len(learnings) != 1 {
		t.Fatalf("after reload: GetByType('execution_learning') returned %d, want 1", len(learnings))
	}

	if learnings[0].Title != "Persistent learning" {
		t.Errorf("after reload: Title = %q, want %q", learnings[0].Title, "Persistent learning")
	}

	// Relations should persist
	if len(learnings[0].Relations) != 3 {
		t.Errorf("after reload: Relations count = %d, want 3", len(learnings[0].Relations))
	}

	// Related nodes should be retrievable after reload
	related := kg2.GetRelated(learnings[0].ID)
	if len(related) != 3 {
		t.Errorf("after reload: GetRelated() returned %d, want 3", len(related))
	}

	// Keywords search should work after reload
	results := kg2.GetRelatedByKeywords([]string{"Persistent"})
	if len(results) == 0 {
		t.Error("after reload: GetRelatedByKeywords found no results")
	}
}
