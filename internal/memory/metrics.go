package memory

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ExecutionMetrics holds detailed metrics for a single execution
type ExecutionMetrics struct {
	ExecutionID      string
	TokensInput      int64
	TokensOutput     int64
	TokensTotal      int64
	EstimatedCostUSD float64
	FilesChanged     int
	LinesAdded       int
	LinesRemoved     int
	ModelName        string
}

// MetricsQuery holds parameters for querying metrics
type MetricsQuery struct {
	Start    time.Time
	End      time.Time
	Projects []string // Empty = all projects
}

// MetricsSummary holds aggregated execution metrics
type MetricsSummary struct {
	// Counts
	TotalExecutions int
	SuccessCount    int
	FailedCount     int
	SuccessRate     float64

	// Duration
	TotalDurationMs int64
	AvgDurationMs   int64
	MinDurationMs   int64
	MaxDurationMs   int64

	// Tokens
	TotalTokensInput  int64
	TotalTokensOutput int64
	TotalTokens       int64
	AvgTokensPerTask  int64

	// Cost
	TotalCostUSD float64
	AvgCostUSD   float64

	// Code changes
	TotalFilesChanged int
	TotalLinesAdded   int
	TotalLinesRemoved int

	// PRs
	PRsCreated int

	// Time period
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// DailyMetrics holds metrics aggregated by day
type DailyMetrics struct {
	Date            time.Time
	ExecutionCount  int
	SuccessCount    int
	FailedCount     int
	TotalDurationMs int64
	TotalTokens     int64
	TotalCostUSD    float64
	FilesChanged    int
	LinesAdded      int
	LinesRemoved    int
	PRsCreated      int
}

// ProjectMetrics holds metrics aggregated by project
type ProjectMetrics struct {
	ProjectPath     string
	ProjectName     string
	ExecutionCount  int
	SuccessCount    int
	FailedCount     int
	SuccessRate     float64
	TotalDurationMs int64
	TotalTokens     int64
	TotalCostUSD    float64
	LastExecution   time.Time
}

// FailureReason holds failure breakdown data
type FailureReason struct {
	Reason string
	Count  int
}

// Model pricing constants (USD per 1M tokens)
// Source: https://platform.claude.com/docs/en/about-claude/pricing
const (
	// Claude Sonnet 4.5/4 pricing
	SonnetInputPricePerMillion  = 3.00
	SonnetOutputPricePerMillion = 15.00

	// Claude Opus 4.6/4.5 pricing (same price)
	OpusInputPricePerMillion  = 5.00
	OpusOutputPricePerMillion = 25.00

	// Claude Opus 4.1/4.0 pricing (legacy, for historical cost tracking)
	Opus41InputPricePerMillion  = 15.00
	Opus41OutputPricePerMillion = 75.00

	// Claude Haiku 4.5 pricing
	HaikuInputPricePerMillion  = 1.00
	HaikuOutputPricePerMillion = 5.00

	// Aliases for backward compatibility
	Opus46InputPricePerMillion    = OpusInputPricePerMillion
	Opus46OutputPricePerMillion   = OpusOutputPricePerMillion
	Opus45InputPricePerMillion    = OpusInputPricePerMillion  // 4.5 same as 4.6
	Opus45OutputPricePerMillion   = OpusOutputPricePerMillion // 4.5 same as 4.6
	Sonnet35InputPricePerMillion  = SonnetInputPricePerMillion
	Sonnet35OutputPricePerMillion = SonnetOutputPricePerMillion

	// Default model
	DefaultModel = "claude-opus-4-6"
)

// EstimateCost calculates estimated cost from token usage
func EstimateCost(inputTokens, outputTokens int64, model string) float64 {
	var inputPrice, outputPrice float64

	modelLower := strings.ToLower(model)
	switch {
	case strings.Contains(modelLower, "opus-4-1") || strings.Contains(modelLower, "opus-4-0") || model == "claude-opus-4":
		// Legacy Opus 4.1/4.0 pricing
		inputPrice = Opus41InputPricePerMillion
		outputPrice = Opus41OutputPricePerMillion
	case strings.Contains(modelLower, "opus"):
		// Opus 4.6/4.5 pricing (same price: $5/$25)
		inputPrice = OpusInputPricePerMillion
		outputPrice = OpusOutputPricePerMillion
	case strings.Contains(modelLower, "haiku"):
		// Haiku 4.5 pricing
		inputPrice = HaikuInputPricePerMillion
		outputPrice = HaikuOutputPricePerMillion
	default:
		// Sonnet / unknown — default to Sonnet pricing
		inputPrice = SonnetInputPricePerMillion
		outputPrice = SonnetOutputPricePerMillion
	}

	inputCost := float64(inputTokens) * inputPrice / 1_000_000
	outputCost := float64(outputTokens) * outputPrice / 1_000_000
	return inputCost + outputCost
}

// SaveExecutionMetrics saves metrics for an execution
func (s *Store) SaveExecutionMetrics(metrics *ExecutionMetrics) error {
	_, err := s.db.Exec(`
		UPDATE executions SET
			tokens_input = ?,
			tokens_output = ?,
			tokens_total = ?,
			estimated_cost_usd = ?,
			files_changed = ?,
			lines_added = ?,
			lines_removed = ?,
			model_name = ?
		WHERE id = ?
	`, metrics.TokensInput, metrics.TokensOutput, metrics.TokensTotal,
		metrics.EstimatedCostUSD, metrics.FilesChanged, metrics.LinesAdded,
		metrics.LinesRemoved, metrics.ModelName, metrics.ExecutionID)
	return err
}

// GetMetricsSummary returns aggregated metrics for a time period
func (s *Store) GetMetricsSummary(query MetricsQuery) (*MetricsSummary, error) {
	summary := &MetricsSummary{
		PeriodStart: query.Start,
		PeriodEnd:   query.End,
	}

	var args []interface{}
	whereClause := "WHERE created_at >= ? AND created_at < ?"
	args = append(args, query.Start, query.End)

	if len(query.Projects) > 0 {
		placeholders := ""
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		whereClause += " AND project_path IN (" + placeholders + ")"
	}

	// Get aggregated metrics
	row := s.db.QueryRow(`
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(duration_ms), 0) as total_duration,
			CAST(COALESCE(AVG(CASE WHEN status = 'completed' THEN duration_ms END), 0) AS INTEGER) as avg_duration,
			COALESCE(MIN(CASE WHEN status = 'completed' THEN duration_ms END), 0) as min_duration,
			COALESCE(MAX(CASE WHEN status = 'completed' THEN duration_ms END), 0) as max_duration,
			COALESCE(SUM(tokens_input), 0) as total_input,
			COALESCE(SUM(tokens_output), 0) as total_output,
			COALESCE(SUM(tokens_total), 0) as total_tokens,
			COALESCE(SUM(estimated_cost_usd), 0) as total_cost,
			COALESCE(SUM(files_changed), 0) as files_changed,
			COALESCE(SUM(lines_added), 0) as lines_added,
			COALESCE(SUM(lines_removed), 0) as lines_removed,
			COALESCE(SUM(CASE WHEN pr_url != '' AND pr_url IS NOT NULL THEN 1 ELSE 0 END), 0) as prs
		FROM executions
	`+whereClause, args...)

	err := row.Scan(
		&summary.TotalExecutions,
		&summary.SuccessCount,
		&summary.FailedCount,
		&summary.TotalDurationMs,
		&summary.AvgDurationMs,
		&summary.MinDurationMs,
		&summary.MaxDurationMs,
		&summary.TotalTokensInput,
		&summary.TotalTokensOutput,
		&summary.TotalTokens,
		&summary.TotalCostUSD,
		&summary.TotalFilesChanged,
		&summary.TotalLinesAdded,
		&summary.TotalLinesRemoved,
		&summary.PRsCreated,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics summary: %w", err)
	}

	// Calculate derived metrics
	if summary.TotalExecutions > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(summary.TotalExecutions)
		summary.AvgTokensPerTask = summary.TotalTokens / int64(summary.TotalExecutions)
		summary.AvgCostUSD = summary.TotalCostUSD / float64(summary.TotalExecutions)
	}

	return summary, nil
}

// GetDailyMetrics returns metrics aggregated by day
func (s *Store) GetDailyMetrics(query MetricsQuery) ([]*DailyMetrics, error) {
	var args []interface{}
	whereClause := "WHERE created_at >= ? AND created_at < ?"
	args = append(args, query.Start, query.End)

	if len(query.Projects) > 0 {
		placeholders := ""
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		whereClause += " AND project_path IN (" + placeholders + ")"
	}

	rows, err := s.db.Query(`
		SELECT
			date(created_at) as day,
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(duration_ms), 0) as total_duration,
			COALESCE(SUM(tokens_total), 0) as total_tokens,
			COALESCE(SUM(estimated_cost_usd), 0) as total_cost,
			COALESCE(SUM(files_changed), 0) as files_changed,
			COALESCE(SUM(lines_added), 0) as lines_added,
			COALESCE(SUM(lines_removed), 0) as lines_removed,
			COALESCE(SUM(CASE WHEN pr_url != '' AND pr_url IS NOT NULL THEN 1 ELSE 0 END), 0) as prs
		FROM executions
		`+whereClause+`
		GROUP BY date(created_at)
		ORDER BY day DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get daily metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var metrics []*DailyMetrics
	for rows.Next() {
		var m DailyMetrics
		var dateStr string
		if err := rows.Scan(
			&dateStr,
			&m.ExecutionCount,
			&m.SuccessCount,
			&m.FailedCount,
			&m.TotalDurationMs,
			&m.TotalTokens,
			&m.TotalCostUSD,
			&m.FilesChanged,
			&m.LinesAdded,
			&m.LinesRemoved,
			&m.PRsCreated,
		); err != nil {
			return nil, err
		}
		m.Date, _ = time.Parse("2006-01-02", dateStr)
		metrics = append(metrics, &m)
	}

	return metrics, nil
}

// GetProjectMetrics returns metrics aggregated by project
func (s *Store) GetProjectMetrics(query MetricsQuery) ([]*ProjectMetrics, error) {
	var args []interface{}
	whereClause := "WHERE e.created_at >= ? AND e.created_at < ?"
	args = append(args, query.Start, query.End)

	if len(query.Projects) > 0 {
		placeholders := ""
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		whereClause += " AND e.project_path IN (" + placeholders + ")"
	}

	rows, err := s.db.Query(`
		SELECT
			e.project_path,
			COALESCE(p.name, e.project_path) as project_name,
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN e.status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN e.status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(SUM(e.duration_ms), 0) as total_duration,
			COALESCE(SUM(e.tokens_total), 0) as total_tokens,
			COALESCE(SUM(e.estimated_cost_usd), 0) as total_cost,
			MAX(e.created_at) as last_exec
		FROM executions e
		LEFT JOIN projects p ON e.project_path = p.path
		`+whereClause+`
		GROUP BY e.project_path
		ORDER BY total DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get project metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var metrics []*ProjectMetrics
	for rows.Next() {
		var m ProjectMetrics
		if err := rows.Scan(
			&m.ProjectPath,
			&m.ProjectName,
			&m.ExecutionCount,
			&m.SuccessCount,
			&m.FailedCount,
			&m.TotalDurationMs,
			&m.TotalTokens,
			&m.TotalCostUSD,
			&m.LastExecution,
		); err != nil {
			return nil, err
		}
		if m.ExecutionCount > 0 {
			m.SuccessRate = float64(m.SuccessCount) / float64(m.ExecutionCount)
		}
		metrics = append(metrics, &m)
	}

	return metrics, nil
}

// GetFailureReasons returns breakdown of failure reasons
func (s *Store) GetFailureReasons(query MetricsQuery, limit int) ([]*FailureReason, error) {
	var args []interface{}
	whereClause := "WHERE created_at >= ? AND created_at < ? AND status = 'failed' AND error != ''"
	args = append(args, query.Start, query.End)

	if len(query.Projects) > 0 {
		placeholders := ""
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		whereClause += " AND project_path IN (" + placeholders + ")"
	}

	args = append(args, limit)

	// Group by first line of error (usually the main error message)
	rows, err := s.db.Query(`
		SELECT
			SUBSTR(error, 1, INSTR(error || char(10), char(10)) - 1) as reason,
			COUNT(*) as count
		FROM executions
		`+whereClause+`
		GROUP BY reason
		ORDER BY count DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get failure reasons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var reasons []*FailureReason
	for rows.Next() {
		var r FailureReason
		if err := rows.Scan(&r.Reason, &r.Count); err != nil {
			return nil, err
		}
		reasons = append(reasons, &r)
	}

	return reasons, nil
}

// GetPeakUsageHours returns execution counts by hour of day
func (s *Store) GetPeakUsageHours(query MetricsQuery) (map[int]int, error) {
	var args []interface{}
	whereClause := "WHERE created_at >= ? AND created_at < ?"
	args = append(args, query.Start, query.End)

	if len(query.Projects) > 0 {
		placeholders := ""
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		whereClause += " AND project_path IN (" + placeholders + ")"
	}

	rows, err := s.db.Query(`
		SELECT
			CAST(strftime('%H', created_at) AS INTEGER) as hour,
			COUNT(*) as count
		FROM executions
		`+whereClause+`
		GROUP BY hour
		ORDER BY hour
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get peak usage hours: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hours := make(map[int]int)
	for rows.Next() {
		var hour, count int
		if err := rows.Scan(&hour, &count); err != nil {
			return nil, err
		}
		hours[hour] = count
	}

	return hours, nil
}

// ExportMetrics exports execution data for external analytics
func (s *Store) ExportMetrics(query MetricsQuery) ([]*ExportedExecution, error) {
	var args []interface{}
	whereClause := "WHERE created_at >= ? AND created_at < ?"
	args = append(args, query.Start, query.End)

	if len(query.Projects) > 0 {
		placeholders := ""
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		whereClause += " AND project_path IN (" + placeholders + ")"
	}

	rows, err := s.db.Query(`
		SELECT
			id, task_id, project_path, status, duration_ms,
			tokens_input, tokens_output, tokens_total, estimated_cost_usd,
			files_changed, lines_added, lines_removed, model_name,
			pr_url, commit_sha, created_at, completed_at
		FROM executions
		`+whereClause+`
		ORDER BY created_at DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to export metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var exports []*ExportedExecution
	for rows.Next() {
		var e ExportedExecution
		var completedAt sql.NullTime
		var tokensInput, tokensOutput, tokensTotal sql.NullInt64
		var cost sql.NullFloat64
		var filesChanged, linesAdded, linesRemoved sql.NullInt64
		var modelName, prURL, commitSHA sql.NullString

		if err := rows.Scan(
			&e.ID, &e.TaskID, &e.ProjectPath, &e.Status, &e.DurationMs,
			&tokensInput, &tokensOutput, &tokensTotal, &cost,
			&filesChanged, &linesAdded, &linesRemoved, &modelName,
			&prURL, &commitSHA, &e.CreatedAt, &completedAt,
		); err != nil {
			return nil, err
		}

		if tokensInput.Valid {
			e.TokensInput = tokensInput.Int64
		}
		if tokensOutput.Valid {
			e.TokensOutput = tokensOutput.Int64
		}
		if tokensTotal.Valid {
			e.TokensTotal = tokensTotal.Int64
		}
		if cost.Valid {
			e.EstimatedCostUSD = cost.Float64
		}
		if filesChanged.Valid {
			e.FilesChanged = int(filesChanged.Int64)
		}
		if linesAdded.Valid {
			e.LinesAdded = int(linesAdded.Int64)
		}
		if linesRemoved.Valid {
			e.LinesRemoved = int(linesRemoved.Int64)
		}
		if modelName.Valid {
			e.ModelName = modelName.String
		}
		if prURL.Valid {
			e.PRUrl = prURL.String
		}
		if commitSHA.Valid {
			e.CommitSHA = commitSHA.String
		}
		if completedAt.Valid {
			e.CompletedAt = &completedAt.Time
		}

		exports = append(exports, &e)
	}

	return exports, nil
}

// ExportedExecution represents an execution record for export
type ExportedExecution struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	ProjectPath      string     `json:"project_path"`
	Status           string     `json:"status"`
	DurationMs       int64      `json:"duration_ms"`
	TokensInput      int64      `json:"tokens_input"`
	TokensOutput     int64      `json:"tokens_output"`
	TokensTotal      int64      `json:"tokens_total"`
	EstimatedCostUSD float64    `json:"estimated_cost_usd"`
	FilesChanged     int        `json:"files_changed"`
	LinesAdded       int        `json:"lines_added"`
	LinesRemoved     int        `json:"lines_removed"`
	ModelName        string     `json:"model_name"`
	PRUrl            string     `json:"pr_url,omitempty"`
	CommitSHA        string     `json:"commit_sha,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}
