package memory

import (
	"database/sql"
	"fmt"
	"time"
)

// Chat turn statuses.
const (
	ChatTurnCompleted = "completed"
	ChatTurnFailed    = "failed"
)

// MaxChatTurnRows is the ListChatTurnsSince page size when limit <= 0.
const MaxChatTurnRows = 1000

// ChatTurn is one recorded web chat turn (GH-5677).
type ChatTurn struct {
	ID               string
	ConversationID   string
	ProjectPath      string
	Model            string
	TokensInput      int64
	TokensOutput     int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	EstimatedCostUSD float64
	// ReportedCostUSD is the CLI-reported cost; nil when none was reported.
	ReportedCostUSD *float64
	// KeySource is platform, own or unknown.
	KeySource   string
	Status      string
	Error       string
	DurationMs  int64
	ToolCalls   int
	Partial     bool
	StartedAt   time.Time
	CompletedAt time.Time
}

// TotalTokens is input plus output; cache tokens are reported separately.
func (c *ChatTurn) TotalTokens() int64 { return c.TokensInput + c.TokensOutput }

// ChatKeySourceTotals is the lifetime chat usage for one key source.
type ChatKeySourceTotals struct {
	KeySource string
	Turns     int64
	CostUSD   float64
}

// SaveChatTurn inserts one chat turn row.
func (s *Store) SaveChatTurn(t *ChatTurn) error {
	t.KeySource = NormalizeKeySource(t.KeySource)
	if t.Status == "" {
		t.Status = ChatTurnCompleted
	}
	var reported interface{}
	if t.ReportedCostUSD != nil {
		reported = *t.ReportedCostUSD
	}
	_, err := s.db.Exec(`INSERT INTO chat_turns (
			id, conversation_id, project_path, model,
			tokens_input, tokens_output, tokens_cache_read, tokens_cache_write,
			estimated_cost_usd, reported_cost_usd, key_source, status, error,
			duration_ms, tool_calls, partial, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.ConversationID, t.ProjectPath, t.Model,
		t.TokensInput, t.TokensOutput, t.TokensCacheRead, t.TokensCacheWrite,
		t.EstimatedCostUSD, reported, t.KeySource, t.Status, t.Error,
		t.DurationMs, t.ToolCalls, t.Partial, t.StartedAt.UTC(), t.CompletedAt.UTC())
	if err != nil {
		return fmt.Errorf("failed to save chat turn: %w", err)
	}
	return nil
}

// ListChatTurnsSince returns chat turns completed at or after since, oldest
// first, optionally scoped to projectPath, capped at limit (MaxChatTurnRows
// when limit <= 0).
func (s *Store) ListChatTurnsSince(since time.Time, projectPath string, limit int) ([]*ChatTurn, error) {
	if limit <= 0 {
		limit = MaxChatTurnRows
	}
	q := `SELECT id, conversation_id, project_path, model,
			tokens_input, tokens_output, tokens_cache_read, tokens_cache_write,
			estimated_cost_usd, reported_cost_usd, key_source, status, error,
			duration_ms, tool_calls, partial, started_at, completed_at
		FROM chat_turns WHERE completed_at >= ?`
	args := []interface{}{since.UTC()}
	if projectPath != "" {
		q += ` AND project_path = ?`
		args = append(args, projectPath)
	}
	q += ` ORDER BY completed_at ASC, id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list chat turns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []*ChatTurn
	for rows.Next() {
		var (
			t        ChatTurn
			reported sql.NullFloat64
		)
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.ProjectPath, &t.Model,
			&t.TokensInput, &t.TokensOutput, &t.TokensCacheRead, &t.TokensCacheWrite,
			&t.EstimatedCostUSD, &reported, &t.KeySource, &t.Status, &t.Error,
			&t.DurationMs, &t.ToolCalls, &t.Partial, &t.StartedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("failed to scan chat turn: %w", err)
		}
		if reported.Valid {
			v := reported.Float64
			t.ReportedCostUSD = &v
		}
		out = append(out, &t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate chat turns: %w", err)
	}
	return out, nil
}

// GetChatTotalsByKeySource returns lifetime chat turn counts and estimated
// cost grouped by key source. It backs the Prometheus counters, which must not
// reset when the daemon restarts.
func (s *Store) GetChatTotalsByKeySource() ([]ChatKeySourceTotals, error) {
	rows, err := s.db.Query(`SELECT key_source, COUNT(*), COALESCE(SUM(estimated_cost_usd), 0)
		FROM chat_turns GROUP BY key_source ORDER BY key_source`)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat totals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ChatKeySourceTotals
	for rows.Next() {
		var t ChatKeySourceTotals
		if err := rows.Scan(&t.KeySource, &t.Turns, &t.CostUSD); err != nil {
			return nil, fmt.Errorf("failed to scan chat totals: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ChatTotals sums chat turns over a window.
type ChatTotals struct {
	Turns   int
	CostUSD float64
	// Tokens is input plus output; cache tokens are reported separately.
	Tokens int64
}

// ChatDay is one UTC day of chat usage.
type ChatDay struct {
	Date string // YYYY-MM-DD, UTC
	ChatTotals
}

const chatAggregateCols = `COUNT(*), COALESCE(SUM(estimated_cost_usd), 0), COALESCE(SUM(tokens_input + tokens_output), 0)`

// GetChatTotalsSince sums chat turns completed at or after since, optionally
// scoped to projectPath. Cost is the list-price estimate, matching the
// executions cost the metrics API already serves.
func (s *Store) GetChatTotalsSince(since time.Time, projectPath string) (ChatTotals, error) {
	q := `SELECT ` + chatAggregateCols + ` FROM chat_turns WHERE completed_at >= ?`
	args := []interface{}{since.UTC()}
	if projectPath != "" {
		q += ` AND project_path = ?`
		args = append(args, projectPath)
	}
	var t ChatTotals
	if err := s.db.QueryRow(q, args...).Scan(&t.Turns, &t.CostUSD, &t.Tokens); err != nil {
		return ChatTotals{}, fmt.Errorf("failed to get chat totals: %w", err)
	}
	return t, nil
}

// GetChatDailySince returns per-UTC-day chat usage for turns completed at or
// after since, oldest day first. Days without turns are omitted.
func (s *Store) GetChatDailySince(since time.Time, projectPath string) ([]ChatDay, error) {
	q := `SELECT substr(completed_at, 1, 10), ` + chatAggregateCols + ` FROM chat_turns WHERE completed_at >= ?`
	args := []interface{}{since.UTC()}
	if projectPath != "" {
		q += ` AND project_path = ?`
		args = append(args, projectPath)
	}
	q += ` GROUP BY 1 ORDER BY 1`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat daily usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ChatDay
	for rows.Next() {
		var d ChatDay
		if err := rows.Scan(&d.Date, &d.Turns, &d.CostUSD, &d.Tokens); err != nil {
			return nil, fmt.Errorf("failed to scan chat daily usage: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
