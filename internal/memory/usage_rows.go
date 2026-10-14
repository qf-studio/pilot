package memory

import (
	"database/sql"
	"fmt"
	"time"
)

// UsageExecutionRow is the per-execution slice of the usage feed served with
// chat turns (GH-5677). Key source and CLI-reported cost are not stored on
// executions at this point, so the feed reports "unknown" and null for them.
type UsageExecutionRow struct {
	ExecutionID      string
	TaskID           string
	ProjectPath      string
	Status           string
	Model            string
	TokensInput      int64
	TokensOutput     int64
	TokensCacheRead  int64
	TokensCacheWrite int64
	EstimatedCostUSD float64
	StartedAt        *time.Time
	CompletedAt      time.Time
	PRUrl            string
}

// ListUsageExecutionsSince returns executions (canary excluded) completed at
// or after since, oldest first, optionally scoped to projectPath, capped at
// limit.
func (s *Store) ListUsageExecutionsSince(since time.Time, projectPath string, limit int) ([]UsageExecutionRow, error) {
	q := `SELECT id, task_id, project_path, status, COALESCE(model_name, ''),
			COALESCE(tokens_input, 0), COALESCE(tokens_output, 0),
			COALESCE(tokens_cache_read, 0), COALESCE(tokens_cache_write, 0),
			COALESCE(estimated_cost_usd, 0), started_at, completed_at, COALESCE(pr_url, '')
		FROM executions
		WHERE completed_at IS NOT NULL AND completed_at >= ? AND COALESCE(is_canary, 0) = 0`
	args := []interface{}{since.UTC()}
	if projectPath != "" {
		q += ` AND project_path = ?`
		args = append(args, projectPath)
	}
	q += ` ORDER BY completed_at ASC, id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list execution usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UsageExecutionRow
	for rows.Next() {
		var (
			r       UsageExecutionRow
			started sql.NullTime
		)
		if err := rows.Scan(&r.ExecutionID, &r.TaskID, &r.ProjectPath, &r.Status, &r.Model,
			&r.TokensInput, &r.TokensOutput, &r.TokensCacheRead, &r.TokensCacheWrite,
			&r.EstimatedCostUSD, &started, &r.CompletedAt, &r.PRUrl); err != nil {
			return nil, fmt.Errorf("failed to scan execution usage: %w", err)
		}
		if started.Valid {
			t := started.Time
			r.StartedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
