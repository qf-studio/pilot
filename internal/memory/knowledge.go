package memory

import (
	"database/sql"
	"encoding/json"
	"time"
)

// MemoryType categorizes experiential memories.
type MemoryType string

const (
	// MemoryTypePattern represents recurring patterns discovered in the codebase.
	// Example: "We use JWT for auth"
	MemoryTypePattern MemoryType = "pattern"

	// MemoryTypePitfall represents common mistakes or issues to avoid.
	// Example: "Auth changes break tests"
	MemoryTypePitfall MemoryType = "pitfall"

	// MemoryTypeDecision represents architectural or design decisions made.
	// Example: "JWT over sessions for scaling"
	MemoryTypeDecision MemoryType = "decision"

	// MemoryTypeLearning represents insights gained from debugging or exploration.
	// Example: "This error usually means X"
	MemoryTypeLearning MemoryType = "learning"
)

// Memory represents an experiential memory entry.
// Memories capture patterns, pitfalls, decisions, and learnings
// that persist across sessions and inform future task execution.
type Memory struct {
	ID         int64
	Type       MemoryType
	Content    string  // The actual memory content
	Context    string  // Task/file where this was learned
	Confidence float64 // 0.0-1.0, decays over time
	ProjectID  string  // Optional project scope
	Source     string  // Origin of the memory, e.g. SourceConsoleReview ("" = legacy/untagged)
	Tags       []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SourceConsoleReview marks learnings written from a console reject
// (POST /api/v1/learnings or a reason on an approval decision, GH-6011).
const SourceConsoleReview = "console-review"

// memoryColumns is the SELECT list every Memory reader scans via scanMemory.
const memoryColumns = "id, type, content, context, confidence, project_id, source, tags, created_at, updated_at"

// KnowledgeStore manages experiential memories using SQLite.
// It provides storage, retrieval, and time-based decay of memories.
type KnowledgeStore struct {
	db *sql.DB
}

// NewKnowledgeStore creates a new KnowledgeStore backed by the given database.
func NewKnowledgeStore(db *sql.DB) *KnowledgeStore {
	return &KnowledgeStore{db: db}
}

// InitSchema creates the memories table and indexes.
// Safe to call multiple times (uses IF NOT EXISTS).
func (k *KnowledgeStore) InitSchema() error {
	_, err := k.db.Exec(`
		CREATE TABLE IF NOT EXISTS memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			content TEXT NOT NULL,
			context TEXT,
			confidence REAL DEFAULT 1.0,
			project_id TEXT,
			source TEXT,
			tags TEXT,
			dedup_key TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(type);
		CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
		CREATE INDEX IF NOT EXISTS idx_memories_confidence ON memories(confidence DESC);
	`)
	if err != nil {
		return err
	}
	// GH-6011: databases created before source/tags/dedup_key existed.
	for _, col := range []string{"source", "tags", "dedup_key"} {
		if err := k.ensureColumn(col); err != nil {
			return err
		}
	}
	_, err = k.db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_memories_source ON memories(source, project_id, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_memories_dedup ON memories(dedup_key);
	`)
	return err
}

// ensureColumn adds a TEXT column to memories when an older database lacks it.
func (k *KnowledgeStore) ensureColumn(name string) error {
	rows, err := k.db.Query(`SELECT name FROM pragma_table_info('memories')`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			_ = rows.Close()
			return err
		}
		if col == name {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if found {
		return nil
	}
	_, err = k.db.Exec(`ALTER TABLE memories ADD COLUMN ` + name + ` TEXT`)
	return err
}

// AddMemory stores a new memory entry.
// Sets ID on the passed Memory struct after insertion.
func (k *KnowledgeStore) AddMemory(m *Memory) error {
	if m.Confidence == 0 {
		m.Confidence = 1.0 // Default confidence
	}
	result, err := k.db.Exec(`
		INSERT INTO memories (type, content, context, confidence, project_id, source, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, m.Type, m.Content, m.Context, m.Confidence, m.ProjectID, m.Source, encodeTags(m.Tags))
	if err != nil {
		return err
	}
	m.ID, _ = result.LastInsertId()
	return nil
}

// AddMemoryOnce stores m unless a memory with the same dedupKey was created in
// the last window; the check and insert are one statement, so concurrent
// callers cannot both insert. It sets m.ID to the new or the existing row's ID
// and reports whether a row was created (GH-6011: console learnings are
// idempotent on (kind, subject, reason hash) for 24 h).
func (k *KnowledgeStore) AddMemoryOnce(m *Memory, dedupKey string, window time.Duration) (created bool, err error) {
	if m.Confidence == 0 {
		m.Confidence = 1.0
	}
	cutoff := time.Now().UTC().Add(-window).Format("2006-01-02 15:04:05")
	result, err := k.db.Exec(`
		INSERT INTO memories (type, content, context, confidence, project_id, source, tags, dedup_key)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM memories WHERE dedup_key = ? AND created_at >= ?)
	`, m.Type, m.Content, m.Context, m.Confidence, m.ProjectID, m.Source, encodeTags(m.Tags), dedupKey,
		dedupKey, cutoff)
	if err != nil {
		return false, err
	}
	if n, _ := result.RowsAffected(); n > 0 {
		m.ID, _ = result.LastInsertId()
		return true, nil
	}
	err = k.db.QueryRow(`
		SELECT id FROM memories WHERE dedup_key = ? AND created_at >= ? ORDER BY id DESC LIMIT 1
	`, dedupKey, cutoff).Scan(&m.ID)
	return false, err
}

// QueryBySource returns the newest memories of memType and source scoped to
// exactly projectID (no NULL-project fallback), newest first.
func (k *KnowledgeStore) QueryBySource(memType MemoryType, source, projectID string, limit int) ([]*Memory, error) {
	rows, err := k.db.Query(`
		SELECT `+memoryColumns+`
		FROM memories
		WHERE type = ? AND source = ? AND project_id = ?
		AND confidence > 0.1
		ORDER BY created_at DESC, id DESC
		LIMIT ?
	`, memType, source, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return k.scanMemories(rows)
}

func encodeTags(tags []string) sql.NullString {
	if len(tags) == 0 {
		return sql.NullString{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// GetMemory retrieves a memory by ID.
// Returns sql.ErrNoRows if not found.
func (k *KnowledgeStore) GetMemory(id int64) (*Memory, error) {
	row := k.db.QueryRow(`
		SELECT `+memoryColumns+`
		FROM memories WHERE id = ?
	`, id)
	return scanMemory(row)
}

// scanMemory scans one memoryColumns row.
func scanMemory(row interface{ Scan(dest ...any) error }) (*Memory, error) {
	m := &Memory{}
	var projectID, source, tags sql.NullString
	var context sql.NullString
	if err := row.Scan(&m.ID, &m.Type, &m.Content, &context,
		&m.Confidence, &projectID, &source, &tags, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Context = context.String
	m.ProjectID = projectID.String
	m.Source = source.String
	if tags.Valid && tags.String != "" {
		_ = json.Unmarshal([]byte(tags.String), &m.Tags)
	}
	return m, nil
}

// QueryByTopic searches memories by content/context matching the topic.
// Returns memories for the specified project or global memories (NULL project_id).
// Only returns memories with confidence > 0.1.
func (k *KnowledgeStore) QueryByTopic(topic string, projectID string) ([]*Memory, error) {
	rows, err := k.db.Query(`
		SELECT `+memoryColumns+`
		FROM memories
		WHERE (content LIKE ? OR context LIKE ?)
		AND (project_id = ? OR project_id IS NULL)
		AND confidence > 0.1
		ORDER BY confidence DESC, updated_at DESC
		LIMIT 10
	`, "%"+topic+"%", "%"+topic+"%", projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return k.scanMemories(rows)
}

// QueryByType retrieves memories of a specific type for a project.
func (k *KnowledgeStore) QueryByType(memType MemoryType, projectID string) ([]*Memory, error) {
	rows, err := k.db.Query(`
		SELECT `+memoryColumns+`
		FROM memories
		WHERE type = ?
		AND (project_id = ? OR project_id IS NULL)
		AND confidence > 0.1
		ORDER BY confidence DESC, updated_at DESC
		LIMIT 20
	`, memType, projectID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return k.scanMemories(rows)
}

// GetRecentMemories retrieves the most recently updated memories.
func (k *KnowledgeStore) GetRecentMemories(limit int) ([]*Memory, error) {
	rows, err := k.db.Query(`
		SELECT `+memoryColumns+`
		FROM memories
		WHERE confidence > 0.1
		ORDER BY updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return k.scanMemories(rows)
}

// scanMemories extracts Memory structs from query rows.
func (k *KnowledgeStore) scanMemories(rows *sql.Rows) ([]*Memory, error) {
	var memories []*Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			continue
		}
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// UpdateMemory updates an existing memory's content and confidence.
// Also updates the updated_at timestamp.
func (k *KnowledgeStore) UpdateMemory(m *Memory) error {
	_, err := k.db.Exec(`
		UPDATE memories
		SET content = ?, context = ?, confidence = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, m.Content, m.Context, m.Confidence, m.ID)
	return err
}

// ReinforceMemory increases confidence when a memory proves useful.
// Confidence is capped at 1.0.
func (k *KnowledgeStore) ReinforceMemory(id int64, delta float64) error {
	_, err := k.db.Exec(`
		UPDATE memories
		SET confidence = MIN(1.0, confidence + ?), updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, delta, id)
	return err
}

// DecayConfidence reduces confidence based on time since last update.
// Rate is the confidence reduction per day of inactivity.
func (k *KnowledgeStore) DecayConfidence(rate float64) error {
	_, err := k.db.Exec(`
		UPDATE memories
		SET confidence = MAX(0.0, confidence - (? * (julianday('now') - julianday(updated_at))))
		WHERE confidence > 0.1
	`, rate)
	return err
}

// PruneStale removes memories with confidence below the threshold.
// Use after DecayConfidence to clean up obsolete memories.
func (k *KnowledgeStore) PruneStale(threshold float64) error {
	_, err := k.db.Exec(`DELETE FROM memories WHERE confidence < ?`, threshold)
	return err
}

// DeleteMemory removes a specific memory by ID.
func (k *KnowledgeStore) DeleteMemory(id int64) error {
	_, err := k.db.Exec(`DELETE FROM memories WHERE id = ?`, id)
	return err
}

// CountMemories returns the total count of active memories (confidence > 0.1).
func (k *KnowledgeStore) CountMemories() (int, error) {
	var count int
	err := k.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE confidence > 0.1`).Scan(&count)
	return count, err
}

// GetMemoryStats returns aggregate statistics about stored memories.
func (k *KnowledgeStore) GetMemoryStats() (*MemoryStats, error) {
	stats := &MemoryStats{ByType: make(map[MemoryType]int)}

	// Total and average confidence
	row := k.db.QueryRow(`
		SELECT COUNT(*), COALESCE(AVG(confidence), 0)
		FROM memories WHERE confidence > 0.1
	`)
	if err := row.Scan(&stats.Total, &stats.AvgConfidence); err != nil {
		return nil, err
	}

	// Count by type
	rows, err := k.db.Query(`
		SELECT type, COUNT(*) FROM memories
		WHERE confidence > 0.1
		GROUP BY type
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var memType MemoryType
		var count int
		if err := rows.Scan(&memType, &count); err != nil {
			continue
		}
		stats.ByType[memType] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}

// MemoryStats holds aggregate statistics about stored memories.
type MemoryStats struct {
	Total         int
	AvgConfidence float64
	ByType        map[MemoryType]int
}

// SyncToFiles exports memories to markdown files in .agent/memories/ for git tracking.
// This allows memories to persist in version control alongside the codebase.
//
// Hook (GH-6011, TASK-585 D2 "SQLite now, files later"): console-review
// learnings (Source == SourceConsoleReview) live only in SQLite today; when this
// stub is implemented they are the rows to export as .agent/knowledge/memories
// learning files. Deliberately still a stub.
func (k *KnowledgeStore) SyncToFiles(agentPath string) error {
	// TODO: Implement file export for git-tracked memories
	// Format: .agent/memories/{type}/{hash}.md
	// Each file contains: type, content, context, confidence, timestamps
	return nil
}
