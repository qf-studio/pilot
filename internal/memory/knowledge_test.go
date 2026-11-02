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
	db.SetMaxOpenConns(1) // each ":memory:" connection is its own database

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

func TestAddMemory_PersistsSourceAndTags(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	mem := &Memory{
		Type:      MemoryTypeLearning,
		Content:   "review_reject — task GH-1\nuse the existing helper",
		ProjectID: "pilot",
		Source:    SourceConsoleReview,
		Tags:      []string{"review_reject", "pilot", "GH-1"},
	}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	got, err := ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Source != SourceConsoleReview {
		t.Errorf("Source = %q, want %q", got.Source, SourceConsoleReview)
	}
	if len(got.Tags) != 3 || got.Tags[1] != "pilot" {
		t.Errorf("Tags = %v, want [review_reject pilot GH-1]", got.Tags)
	}
}

func TestAddMemory_LegacyMemoryHasNoSource(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	mem := &Memory{Type: MemoryTypePattern, Content: "plain", ProjectID: "pilot"}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	got, err := ks.GetMemory(mem.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Source != "" || len(got.Tags) != 0 {
		t.Errorf("legacy memory got Source=%q Tags=%v, want empty", got.Source, got.Tags)
	}
}

func TestAddMemory_InitSchemaMigratesOldTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE memories (
		id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT NOT NULL, content TEXT NOT NULL,
		context TEXT, confidence REAL DEFAULT 1.0, project_id TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories (type, content, project_id) VALUES ('pattern', 'old row', 'pilot')`); err != nil {
		t.Fatal(err)
	}

	ks := NewKnowledgeStore(db)
	for i := 0; i < 2; i++ { // twice: the migration must be idempotent
		if err := ks.InitSchema(); err != nil {
			t.Fatalf("InitSchema #%d: %v", i+1, err)
		}
	}
	old, err := ks.QueryByType(MemoryTypePattern, "pilot")
	if err != nil || len(old) != 1 || old[0].Content != "old row" {
		t.Fatalf("old row not readable after migration: %v %v", old, err)
	}
	mem := &Memory{Type: MemoryTypeLearning, Content: "new", ProjectID: "pilot", Source: SourceConsoleReview}
	if err := ks.AddMemory(mem); err != nil {
		t.Fatalf("AddMemory after migration: %v", err)
	}
}

func TestAddMemoryOnce_IdempotentWithinWindow(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	newMem := func() *Memory {
		return &Memory{Type: MemoryTypeLearning, Content: "c", ProjectID: "pilot", Source: SourceConsoleReview}
	}
	first := newMem()
	created, err := ks.AddMemoryOnce(first, "k1", 24*time.Hour)
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v, want created", created, err)
	}
	second := newMem()
	created, err = ks.AddMemoryOnce(second, "k1", 24*time.Hour)
	if err != nil || created {
		t.Fatalf("second: created=%v err=%v, want duplicate", created, err)
	}
	if second.ID != first.ID {
		t.Errorf("duplicate ID = %d, want existing %d", second.ID, first.ID)
	}
	other := newMem()
	if created, err = ks.AddMemoryOnce(other, "k2", 24*time.Hour); err != nil || !created {
		t.Fatalf("different key: created=%v err=%v, want created", created, err)
	}

	// A row older than the window no longer blocks the same key.
	if _, err := db.Exec(`UPDATE memories SET created_at = datetime('now', '-25 hours') WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	again := newMem()
	if created, err = ks.AddMemoryOnce(again, "k1", 24*time.Hour); err != nil || !created {
		t.Fatalf("after window: created=%v err=%v, want created", created, err)
	}
}

func TestAddMemory_QueryBySourceScopesAndOrders(t *testing.T) {
	db, ks := setupKnowledgeTestDB(t)
	defer func() { _ = db.Close() }()

	add := func(content, project, source string) {
		t.Helper()
		if err := ks.AddMemory(&Memory{Type: MemoryTypeLearning, Content: content, ProjectID: project, Source: source}); err != nil {
			t.Fatal(err)
		}
	}
	add("old", "pilot", SourceConsoleReview)
	add("new", "pilot", SourceConsoleReview)
	add("other-project", "other", SourceConsoleReview)
	add("other-source", "pilot", "")

	got, err := ks.QueryBySource(MemoryTypeLearning, SourceConsoleReview, "pilot", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "new" || got[1].Content != "old" {
		t.Fatalf("got %v, want [new old] for project pilot", got)
	}
	if limited, _ := ks.QueryBySource(MemoryTypeLearning, SourceConsoleReview, "pilot", 1); len(limited) != 1 {
		t.Errorf("limit 1 returned %d rows", len(limited))
	}
}
