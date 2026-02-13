package memory

import (
	"database/sql"
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
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

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
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_memories_type ON memories(type);
		CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
		CREATE INDEX IF NOT EXISTS idx_memories_confidence ON memories(confidence DESC);
	`)
	return err
}

// AddMemory stores a new memory entry.
// Sets ID on the passed Memory struct after insertion.
func (k *KnowledgeStore) AddMemory(m *Memory) error {
	if m.Confidence == 0 {
		m.Confidence = 1.0 // Default confidence
	}
	result, err := k.db.Exec(`
		INSERT INTO memories (type, content, context, confidence, project_id)
		VALUES (?, ?, ?, ?, ?)
	`, m.Type, m.Content, m.Context, m.Confidence, m.ProjectID)
	if err != nil {
		return err
	}
	m.ID, _ = result.LastInsertId()
	return nil
}

// GetMemory retrieves a memory by ID.
// Returns sql.ErrNoRows if not found.
func (k *KnowledgeStore) GetMemory(id int64) (*Memory, error) {
	row := k.db.QueryRow(`
		SELECT id, type, content, context, confidence, project_id, created_at, updated_at
		FROM memories WHERE id = ?
	`, id)

	m := &Memory{}
	var projectID sql.NullString
	err := row.Scan(&m.ID, &m.Type, &m.Content, &m.Context,
		&m.Confidence, &projectID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if projectID.Valid {
		m.ProjectID = projectID.String
	}
	return m, nil
}

// QueryByTopic searches memories by content/context matching the topic.
// Returns memories for the specified project or global memories (NULL project_id).
// Only returns memories with confidence > 0.1.
func (k *KnowledgeStore) QueryByTopic(topic string, projectID string) ([]*Memory, error) {
	rows, err := k.db.Query(`
		SELECT id, type, content, context, confidence, project_id, created_at, updated_at
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
		SELECT id, type, content, context, confidence, project_id, created_at, updated_at
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
		SELECT id, type, content, context, confidence, project_id, created_at, updated_at
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
		m := &Memory{}
		var projectID sql.NullString
		err := rows.Scan(&m.ID, &m.Type, &m.Content, &m.Context,
			&m.Confidence, &projectID, &m.CreatedAt, &m.UpdatedAt)
		if err != nil {
			continue
		}
		if projectID.Valid {
			m.ProjectID = projectID.String
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
func (k *KnowledgeStore) SyncToFiles(agentPath string) error {
	// TODO: Implement file export for git-tracked memories
	// Format: .agent/memories/{type}/{hash}.md
	// Each file contains: type, content, context, confidence, timestamps
	return nil
}
