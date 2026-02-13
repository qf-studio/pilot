package memory

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupKnowledgeTestDB(t *testing.T) (*sql.DB, *KnowledgeStore) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	ks := NewKnowledgeStore(db)
	if err := ks.InitSchema(); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	return db, ks
}

func TestKnowledgeStore_AddAndGetMemory(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	mem := &Memory{
		Type:       MemoryTypePattern,
		Content:    "JWT tokens for API auth",
		Context:    "auth/handler.go",
		Confidence: 0.9,
		ProjectID:  "pilot",
	}

	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory failed: %v", err)
	}

	if mem.ID == 0 {
		t.Error("expected ID to be set after insert")
	}

	got, err := ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory failed: %v", err)
	}

	if got.Type != MemoryTypePattern {
		t.Errorf("got type %v, want %v", got.Type, MemoryTypePattern)
	}
	if got.Content != "JWT tokens for API auth" {
		t.Errorf("got content %q, want %q", got.Content, "JWT tokens for API auth")
	}
	if got.Confidence != 0.9 {
		t.Errorf("got confidence %v, want 0.9", got.Confidence)
	}
}

func TestKnowledgeStore_QueryByTopic(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	memories := []*Memory{
		{Type: MemoryTypePattern, Content: "Use JWT for authentication", Context: "auth", ProjectID: "pilot"},
		{Type: MemoryTypePitfall, Content: "Auth tests break on CI", Context: "testing", ProjectID: "pilot"},
		{Type: MemoryTypeLearning, Content: "Database uses JWT middleware", Context: "db", ProjectID: "pilot"},
		{Type: MemoryTypeDecision, Content: "Sessions not used", Context: "auth", ProjectID: "other"},
	}

	for _, m := range memories {
		if err := ks.AddMemory(m); err != nil {
			t.Fatalf("AddMemory failed: %v", err)
		}
	}

	// Query for "JWT" in pilot project
	results, err := ks.QueryByTopic("JWT", "pilot")
	if err != nil {
		t.Fatalf("QueryByTopic failed: %v", err)
	}

	// Should find 2 memories mentioning JWT
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}

func TestKnowledgeStore_QueryByType(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	memories := []*Memory{
		{Type: MemoryTypePitfall, Content: "Don't use raw SQL", Context: "db", ProjectID: "pilot"},
		{Type: MemoryTypePitfall, Content: "Watch for race conditions", Context: "concurrency", ProjectID: "pilot"},
		{Type: MemoryTypePattern, Content: "Use prepared statements", Context: "db", ProjectID: "pilot"},
	}

	for _, m := range memories {
		if err := ks.AddMemory(m); err != nil {
			t.Fatalf("AddMemory failed: %v", err)
		}
	}

	results, err := ks.QueryByType(MemoryTypePitfall, "pilot")
	if err != nil {
		t.Fatalf("QueryByType failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("got %d pitfalls, want 2", len(results))
	}
}

func TestKnowledgeStore_DecayAndPrune(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	// Add memory with low confidence
	mem := &Memory{
		Type:       MemoryTypeLearning,
		Content:    "Old learning",
		Confidence: 0.15,
	}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory failed: %v", err)
	}

	// Decay - this should reduce confidence further
	// Set confidence to a value that will drop below threshold after decay
	_, err := db.Exec(`UPDATE memories SET confidence = 0.05 WHERE id = ?`, mem.ID)
	if err != nil {
		t.Fatalf("failed to set low confidence: %v", err)
	}

	// Prune memories below 0.1
	if err := ks.PruneStale(0.1); err != nil {
		t.Fatalf("PruneStale failed: %v", err)
	}

	// Memory should be deleted
	_, err = ks.GetMemory(mem.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected memory to be pruned, got err: %v", err)
	}
}

func TestKnowledgeStore_ReinforceMemory(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	mem := &Memory{
		Type:       MemoryTypePattern,
		Content:    "Test pattern",
		Confidence: 0.7,
	}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory failed: %v", err)
	}

	// Reinforce the memory
	if err := ks.ReinforceMemory(mem.ID, 0.2); err != nil {
		t.Fatalf("ReinforceMemory failed: %v", err)
	}

	got, err := ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory failed: %v", err)
	}

	// Use tolerance for floating point comparison
	if got.Confidence < 0.89 || got.Confidence > 0.91 {
		t.Errorf("got confidence %v, want ~0.9", got.Confidence)
	}

	// Reinforce beyond 1.0 should cap at 1.0
	if err := ks.ReinforceMemory(mem.ID, 0.5); err != nil {
		t.Fatalf("ReinforceMemory failed: %v", err)
	}

	got, err = ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory failed: %v", err)
	}

	if got.Confidence != 1.0 {
		t.Errorf("got confidence %v, want 1.0 (capped)", got.Confidence)
	}
}

func TestKnowledgeStore_UpdateMemory(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	mem := &Memory{
		Type:    MemoryTypeDecision,
		Content: "Original decision",
		Context: "planning",
	}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory failed: %v", err)
	}

	// Update the memory
	mem.Content = "Updated decision"
	mem.Confidence = 0.95
	if err := ks.UpdateMemory(mem); err != nil {
		t.Fatalf("UpdateMemory failed: %v", err)
	}

	got, err := ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory failed: %v", err)
	}

	if got.Content != "Updated decision" {
		t.Errorf("got content %q, want %q", got.Content, "Updated decision")
	}
}

func TestKnowledgeStore_GetMemoryStats(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	memories := []*Memory{
		{Type: MemoryTypePattern, Content: "Pattern 1", Confidence: 0.8},
		{Type: MemoryTypePattern, Content: "Pattern 2", Confidence: 0.6},
		{Type: MemoryTypePitfall, Content: "Pitfall 1", Confidence: 0.9},
		{Type: MemoryTypeLearning, Content: "Learning 1", Confidence: 0.7},
	}

	for _, m := range memories {
		if err := ks.AddMemory(m); err != nil {
			t.Fatalf("AddMemory failed: %v", err)
		}
	}

	stats, err := ks.GetMemoryStats()
	if err != nil {
		t.Fatalf("GetMemoryStats failed: %v", err)
	}

	if stats.Total != 4 {
		t.Errorf("got total %d, want 4", stats.Total)
	}

	if stats.ByType[MemoryTypePattern] != 2 {
		t.Errorf("got %d patterns, want 2", stats.ByType[MemoryTypePattern])
	}

	if stats.ByType[MemoryTypePitfall] != 1 {
		t.Errorf("got %d pitfalls, want 1", stats.ByType[MemoryTypePitfall])
	}
}

func TestKnowledgeStore_GetRecentMemories(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	// Add memories with slight time gaps
	for i := 1; i <= 5; i++ {
		mem := &Memory{
			Type:    MemoryTypeLearning,
			Content: "Learning " + string(rune('A'+i-1)),
		}
		if err := ks.AddMemory(mem); err != nil {
			t.Fatalf("AddMemory failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	results, err := ks.GetRecentMemories(3)
	if err != nil {
		t.Fatalf("GetRecentMemories failed: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("got %d results, want 3", len(results))
	}
}

func TestKnowledgeStore_DefaultConfidence(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	// Memory without explicit confidence should get 1.0
	mem := &Memory{
		Type:    MemoryTypePattern,
		Content: "Test with default confidence",
	}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory failed: %v", err)
	}

	got, err := ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory failed: %v", err)
	}

	if got.Confidence != 1.0 {
		t.Errorf("got confidence %v, want 1.0 (default)", got.Confidence)
	}
}

func TestKnowledgeStore_DeleteMemory(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	mem := &Memory{
		Type:    MemoryTypeDecision,
		Content: "Delete me",
	}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory failed: %v", err)
	}

	if err := ks.DeleteMemory(mem.ID); err != nil {
		t.Fatalf("DeleteMemory failed: %v", err)
	}

	_, err := ks.GetMemory(mem.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected ErrNoRows after delete, got: %v", err)
	}
}
