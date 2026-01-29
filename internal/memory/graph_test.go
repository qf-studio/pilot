package memory

import (
	"os"
	"testing"
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
