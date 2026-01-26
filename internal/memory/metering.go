package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// UsageEventType represents the type of billable event
type UsageEventType string

const (
	EventTypeTask     UsageEventType = "task"      // Task execution
	EventTypeToken    UsageEventType = "token"     // Claude API tokens
	EventTypeCompute  UsageEventType = "compute"   // Execution time (minutes)
	EventTypeStorage  UsageEventType = "storage"   // Memory/logs storage
	EventTypeAPICall  UsageEventType = "api_call"  // External API calls
)

// UsageEvent represents a single billable event
type UsageEvent struct {
	ID          string                 `json:"id"`
	Timestamp   time.Time              `json:"timestamp"`
	UserID      string                 `json:"user_id"`
	ProjectID   string                 `json:"project_id"`
	EventType   UsageEventType         `json:"event_type"`
	Quantity    int64                  `json:"quantity"`
	UnitCost    float64                `json:"unit_cost"`
	TotalCost   float64                `json:"total_cost"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	ExecutionID string                 `json:"execution_id,omitempty"` // Link to execution
}

// UsageSummary holds aggregated usage for a period
type UsageSummary struct {
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id,omitempty"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	// Task metrics
	TaskCount     int64   `json:"task_count"`
	TaskCost      float64 `json:"task_cost"`

	// Token metrics
	TokensInput   int64   `json:"tokens_input"`
	TokensOutput  int64   `json:"tokens_output"`
	TokensTotal   int64   `json:"tokens_total"`
	TokenCost     float64 `json:"token_cost"`

	// Compute metrics
	ComputeMinutes int64   `json:"compute_minutes"`
	ComputeCost    float64 `json:"compute_cost"`

	// Storage metrics
	StorageBytes   int64   `json:"storage_bytes"`
	StorageCost    float64 `json:"storage_cost"`

	// API call metrics
	APICallCount int64   `json:"api_call_count"`
	APICallCost  float64 `json:"api_call_cost"`

	// Totals
	TotalCost float64 `json:"total_cost"`
}

// UsageQuery holds parameters for querying usage
type UsageQuery struct {
	UserID    string
	ProjectID string
	Start     time.Time
	End       time.Time
	EventType UsageEventType // Empty = all types
}

// Pricing constants (can be made configurable)
const (
	// Per task flat fee
	PricePerTask = 1.00 // $1.00 per task execution

	// Token pricing with 20% margin
	TokenInputPricePerMillion  = 3.60  // $3.00 + 20%
	TokenOutputPricePerMillion = 18.00 // $15.00 + 20%

	// Compute pricing
	PricePerComputeMinute = 0.01 // $0.01 per minute

	// Storage pricing (per GB per month)
	PricePerGBMonth = 0.10

	// API call pricing
	PricePerAPICall = 0.001 // $0.001 per call
)

// CalculateTokenCost calculates cost for token usage with margin
func CalculateTokenCost(inputTokens, outputTokens int64) float64 {
	inputCost := float64(inputTokens) * TokenInputPricePerMillion / 1_000_000
	outputCost := float64(outputTokens) * TokenOutputPricePerMillion / 1_000_000
	return inputCost + outputCost
}

// CalculateComputeCost calculates cost for compute time
func CalculateComputeCost(durationMs int64) float64 {
	minutes := float64(durationMs) / 60000.0
	return minutes * PricePerComputeMinute
}

// RecordUsageEvent saves a usage event
func (s *Store) RecordUsageEvent(event *UsageEvent) error {
	metadata, _ := json.Marshal(event.Metadata)

	_, err := s.db.Exec(`
		INSERT INTO usage_events (id, timestamp, user_id, project_id, event_type, quantity, unit_cost, total_cost, metadata, execution_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.Timestamp, event.UserID, event.ProjectID, event.EventType, event.Quantity, event.UnitCost, event.TotalCost, string(metadata), event.ExecutionID)
	return err
}

// RecordTaskUsage records usage for a completed task
func (s *Store) RecordTaskUsage(executionID, userID, projectID string, durationMs, tokensInput, tokensOutput int64) error {
	now := time.Now()

	// Record task event
	taskEvent := &UsageEvent{
		ID:          fmt.Sprintf("evt_%s_task", executionID),
		Timestamp:   now,
		UserID:      userID,
		ProjectID:   projectID,
		EventType:   EventTypeTask,
		Quantity:    1,
		UnitCost:    PricePerTask,
		TotalCost:   PricePerTask,
		ExecutionID: executionID,
	}
	if err := s.RecordUsageEvent(taskEvent); err != nil {
		return fmt.Errorf("failed to record task event: %w", err)
	}

	// Record token event
	if tokensInput > 0 || tokensOutput > 0 {
		tokenCost := CalculateTokenCost(tokensInput, tokensOutput)
		tokenEvent := &UsageEvent{
			ID:          fmt.Sprintf("evt_%s_token", executionID),
			Timestamp:   now,
			UserID:      userID,
			ProjectID:   projectID,
			EventType:   EventTypeToken,
			Quantity:    tokensInput + tokensOutput,
			UnitCost:    tokenCost / float64(tokensInput+tokensOutput),
			TotalCost:   tokenCost,
			ExecutionID: executionID,
			Metadata: map[string]interface{}{
				"input_tokens":  tokensInput,
				"output_tokens": tokensOutput,
			},
		}
		if err := s.RecordUsageEvent(tokenEvent); err != nil {
			return fmt.Errorf("failed to record token event: %w", err)
		}
	}

	// Record compute event
	if durationMs > 0 {
		computeCost := CalculateComputeCost(durationMs)
		computeEvent := &UsageEvent{
			ID:          fmt.Sprintf("evt_%s_compute", executionID),
			Timestamp:   now,
			UserID:      userID,
			ProjectID:   projectID,
			EventType:   EventTypeCompute,
			Quantity:    durationMs / 60000, // minutes
			UnitCost:    PricePerComputeMinute,
			TotalCost:   computeCost,
			ExecutionID: executionID,
			Metadata: map[string]interface{}{
				"duration_ms": durationMs,
			},
		}
		if err := s.RecordUsageEvent(computeEvent); err != nil {
			return fmt.Errorf("failed to record compute event: %w", err)
		}
	}

	return nil
}

// GetUsageSummary returns aggregated usage for a period
func (s *Store) GetUsageSummary(query UsageQuery) (*UsageSummary, error) {
	summary := &UsageSummary{
		UserID:      query.UserID,
		ProjectID:   query.ProjectID,
		PeriodStart: query.Start,
		PeriodEnd:   query.End,
	}

	// Build WHERE clause
	var args []interface{}
	whereClause := "WHERE timestamp >= ? AND timestamp < ?"
	args = append(args, query.Start, query.End)

	if query.UserID != "" {
		whereClause += " AND user_id = ?"
		args = append(args, query.UserID)
	}

	if query.ProjectID != "" {
		whereClause += " AND project_id = ?"
		args = append(args, query.ProjectID)
	}

	// Get task metrics
	row := s.db.QueryRow(`
		SELECT COALESCE(COUNT(*), 0), COALESCE(SUM(total_cost), 0)
		FROM usage_events
		`+whereClause+` AND event_type = 'task'
	`, args...)
	if err := row.Scan(&summary.TaskCount, &summary.TaskCost); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get task metrics: %w", err)
	}

	// Get token metrics
	row = s.db.QueryRow(`
		SELECT
			COALESCE(SUM(CAST(json_extract(metadata, '$.input_tokens') AS INTEGER)), 0),
			COALESCE(SUM(CAST(json_extract(metadata, '$.output_tokens') AS INTEGER)), 0),
			COALESCE(SUM(quantity), 0),
			COALESCE(SUM(total_cost), 0)
		FROM usage_events
		`+whereClause+` AND event_type = 'token'
	`, args...)
	if err := row.Scan(&summary.TokensInput, &summary.TokensOutput, &summary.TokensTotal, &summary.TokenCost); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get token metrics: %w", err)
	}

	// Get compute metrics
	row = s.db.QueryRow(`
		SELECT COALESCE(SUM(quantity), 0), COALESCE(SUM(total_cost), 0)
		FROM usage_events
		`+whereClause+` AND event_type = 'compute'
	`, args...)
	if err := row.Scan(&summary.ComputeMinutes, &summary.ComputeCost); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get compute metrics: %w", err)
	}

	// Get storage metrics
	row = s.db.QueryRow(`
		SELECT COALESCE(SUM(quantity), 0), COALESCE(SUM(total_cost), 0)
		FROM usage_events
		`+whereClause+` AND event_type = 'storage'
	`, args...)
	if err := row.Scan(&summary.StorageBytes, &summary.StorageCost); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get storage metrics: %w", err)
	}

	// Get API call metrics
	row = s.db.QueryRow(`
		SELECT COALESCE(COUNT(*), 0), COALESCE(SUM(total_cost), 0)
		FROM usage_events
		`+whereClause+` AND event_type = 'api_call'
	`, args...)
	if err := row.Scan(&summary.APICallCount, &summary.APICallCost); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to get API call metrics: %w", err)
	}

	// Calculate total
	summary.TotalCost = summary.TaskCost + summary.TokenCost + summary.ComputeCost + summary.StorageCost + summary.APICallCost

	return summary, nil
}

// GetDailyUsage returns usage aggregated by day
func (s *Store) GetDailyUsage(query UsageQuery) ([]*DailyUsage, error) {
	var args []interface{}
	whereClause := "WHERE timestamp >= ? AND timestamp < ?"
	args = append(args, query.Start, query.End)

	if query.UserID != "" {
		whereClause += " AND user_id = ?"
		args = append(args, query.UserID)
	}

	if query.ProjectID != "" {
		whereClause += " AND project_id = ?"
		args = append(args, query.ProjectID)
	}

	rows, err := s.db.Query(`
		SELECT
			date(timestamp) as day,
			event_type,
			COUNT(*) as event_count,
			COALESCE(SUM(quantity), 0) as total_quantity,
			COALESCE(SUM(total_cost), 0) as total_cost
		FROM usage_events
		`+whereClause+`
		GROUP BY date(timestamp), event_type
		ORDER BY day DESC, event_type
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get daily usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// Aggregate by day
	dailyMap := make(map[string]*DailyUsage)
	for rows.Next() {
		var dateStr string
		var eventType string
		var count, quantity int64
		var cost float64

		if err := rows.Scan(&dateStr, &eventType, &count, &quantity, &cost); err != nil {
			return nil, err
		}

		if _, ok := dailyMap[dateStr]; !ok {
			date, _ := time.Parse("2006-01-02", dateStr)
			dailyMap[dateStr] = &DailyUsage{Date: date}
		}

		du := dailyMap[dateStr]
		du.TotalCost += cost

		switch UsageEventType(eventType) {
		case EventTypeTask:
			du.TaskCount = count
			du.TaskCost = cost
		case EventTypeToken:
			du.TokenCount = quantity
			du.TokenCost = cost
		case EventTypeCompute:
			du.ComputeMinutes = quantity
			du.ComputeCost = cost
		}
	}

	// Convert to slice
	var result []*DailyUsage
	for _, du := range dailyMap {
		result = append(result, du)
	}

	return result, nil
}

// DailyUsage represents usage for a single day
type DailyUsage struct {
	Date           time.Time `json:"date"`
	TaskCount      int64     `json:"task_count"`
	TaskCost       float64   `json:"task_cost"`
	TokenCount     int64     `json:"token_count"`
	TokenCost      float64   `json:"token_cost"`
	ComputeMinutes int64     `json:"compute_minutes"`
	ComputeCost    float64   `json:"compute_cost"`
	TotalCost      float64   `json:"total_cost"`
}

// GetUsageByProject returns usage aggregated by project
func (s *Store) GetUsageByProject(query UsageQuery) ([]*ProjectUsage, error) {
	var args []interface{}
	whereClause := "WHERE timestamp >= ? AND timestamp < ?"
	args = append(args, query.Start, query.End)

	if query.UserID != "" {
		whereClause += " AND user_id = ?"
		args = append(args, query.UserID)
	}

	rows, err := s.db.Query(`
		SELECT
			project_id,
			COUNT(DISTINCT CASE WHEN event_type = 'task' THEN id END) as task_count,
			COALESCE(SUM(CASE WHEN event_type = 'token' THEN quantity ELSE 0 END), 0) as tokens,
			COALESCE(SUM(CASE WHEN event_type = 'compute' THEN quantity ELSE 0 END), 0) as compute_mins,
			COALESCE(SUM(total_cost), 0) as total_cost
		FROM usage_events
		`+whereClause+`
		GROUP BY project_id
		ORDER BY total_cost DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get project usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []*ProjectUsage
	for rows.Next() {
		var pu ProjectUsage
		if err := rows.Scan(&pu.ProjectID, &pu.TaskCount, &pu.TokenCount, &pu.ComputeMinutes, &pu.TotalCost); err != nil {
			return nil, err
		}
		result = append(result, &pu)
	}

	return result, nil
}

// ProjectUsage represents usage for a single project
type ProjectUsage struct {
	ProjectID      string  `json:"project_id"`
	TaskCount      int64   `json:"task_count"`
	TokenCount     int64   `json:"token_count"`
	ComputeMinutes int64   `json:"compute_minutes"`
	TotalCost      float64 `json:"total_cost"`
}

// GetUsageEvents returns raw usage events for export/audit
func (s *Store) GetUsageEvents(query UsageQuery, limit int) ([]*UsageEvent, error) {
	var args []interface{}
	whereClause := "WHERE timestamp >= ? AND timestamp < ?"
	args = append(args, query.Start, query.End)

	if query.UserID != "" {
		whereClause += " AND user_id = ?"
		args = append(args, query.UserID)
	}

	if query.ProjectID != "" {
		whereClause += " AND project_id = ?"
		args = append(args, query.ProjectID)
	}

	if query.EventType != "" {
		whereClause += " AND event_type = ?"
		args = append(args, query.EventType)
	}

	args = append(args, limit)

	rows, err := s.db.Query(`
		SELECT id, timestamp, user_id, project_id, event_type, quantity, unit_cost, total_cost, metadata, COALESCE(execution_id, '')
		FROM usage_events
		`+whereClause+`
		ORDER BY timestamp DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to get usage events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []*UsageEvent
	for rows.Next() {
		var e UsageEvent
		var metadataStr string
		var eventType string
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.UserID, &e.ProjectID, &eventType, &e.Quantity, &e.UnitCost, &e.TotalCost, &metadataStr, &e.ExecutionID); err != nil {
			return nil, err
		}
		e.EventType = UsageEventType(eventType)
		if metadataStr != "" {
			_ = json.Unmarshal([]byte(metadataStr), &e.Metadata)
		}
		events = append(events, &e)
	}

	return events, nil
}

// UsageThreshold defines an alert threshold for usage
type UsageThreshold struct {
	UserID      string
	MetricType  string  // "cost", "tasks", "tokens"
	Threshold   float64
	Period      string  // "daily", "weekly", "monthly"
	LastAlerted time.Time
}

// CheckUsageThresholds checks if any thresholds are exceeded
func (s *Store) CheckUsageThresholds(userID string) ([]string, error) {
	// Get current month's usage
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	summary, err := s.GetUsageSummary(UsageQuery{
		UserID: userID,
		Start:  monthStart,
		End:    now,
	})
	if err != nil {
		return nil, err
	}

	var alerts []string

	// Example thresholds (would be configurable in production)
	if summary.TotalCost > 100.0 {
		alerts = append(alerts, fmt.Sprintf("Monthly cost threshold exceeded: $%.2f", summary.TotalCost))
	}

	if summary.TaskCount > 500 {
		alerts = append(alerts, fmt.Sprintf("Monthly task limit approaching: %d tasks", summary.TaskCount))
	}

	return alerts, nil
}
