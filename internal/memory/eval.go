package memory

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EvalTask represents a captured evaluation task derived from a real execution.
// It stores enough context to replay the task as a benchmark or regression test.
type EvalTask struct {
	ID           string         `json:"id"`
	ExecutionID  string         `json:"execution_id"`
	IssueNumber  int            `json:"issue_number"`
	IssueTitle   string         `json:"issue_title"`
	Repo         string         `json:"repo"`
	Success      bool           `json:"success"`
	PassCriteria []PassCriteria `json:"pass_criteria,omitempty"`
	FilesChanged []string       `json:"files_changed,omitempty"`
	DurationMs   int64          `json:"duration_ms"`
	CreatedAt    time.Time      `json:"created_at"`
}

// PassCriteria defines a single criterion that must pass for an eval task to succeed.
type PassCriteria struct {
	Type    string `json:"type"`    // "build", "test", "lint", "custom"
	Command string `json:"command"` // The gate command (if known)
	Passed  bool   `json:"passed"`
}

// EvalInput carries the execution data needed to build an EvalTask.
// Callers construct this from executor.ExecutionResult to avoid an import cycle.
type EvalInput struct {
	TaskID       string
	Success      bool
	DurationMs   int64
	GateResults  []EvalGateResult
	Repo         string
	IssueNumber  int
	IssueTitle   string
	FilesChanged []string
}

// EvalGateResult is a quality gate outcome, mirroring executor.QualityGateResult.
type EvalGateResult struct {
	Name   string
	Passed bool
}

// ExtractEvalTask builds an EvalTask from execution data and issue metadata.
// It generates a deterministic ID from the repo and issue number.
func ExtractEvalTask(in EvalInput) *EvalTask {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", in.Repo, in.IssueNumber)))
	id := fmt.Sprintf("eval-%x", h[:8])

	var criteria []PassCriteria
	for _, g := range in.GateResults {
		criteria = append(criteria, PassCriteria{
			Type:   g.Name,
			Passed: g.Passed,
		})
	}

	return &EvalTask{
		ID:           id,
		ExecutionID:  in.TaskID,
		IssueNumber:  in.IssueNumber,
		IssueTitle:   in.IssueTitle,
		Repo:         in.Repo,
		Success:      in.Success,
		PassCriteria: criteria,
		FilesChanged: in.FilesChanged,
		DurationMs:   in.DurationMs,
		CreatedAt:    time.Now(),
	}
}

// SaveEvalTask persists an eval task. On duplicate (repo, issue_number) it updates the existing row.
func (s *Store) SaveEvalTask(task *EvalTask) error {
	criteriaJSON, err := json.Marshal(task.PassCriteria)
	if err != nil {
		return fmt.Errorf("marshal pass_criteria: %w", err)
	}
	filesJSON, err := json.Marshal(task.FilesChanged)
	if err != nil {
		return fmt.Errorf("marshal files_changed: %w", err)
	}

	return s.withRetry("SaveEvalTask", func() error {
		_, err := s.db.Exec(`
			INSERT INTO eval_tasks (id, execution_id, issue_number, issue_title, repo, success, pass_criteria, files_changed, duration_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo, issue_number) DO UPDATE SET
				execution_id = excluded.execution_id,
				success = excluded.success,
				pass_criteria = excluded.pass_criteria,
				files_changed = excluded.files_changed,
				duration_ms = excluded.duration_ms
		`, task.ID, task.ExecutionID, task.IssueNumber, task.IssueTitle, task.Repo, task.Success,
			string(criteriaJSON), string(filesJSON), task.DurationMs)
		return err
	})
}

// EvalResult stores the outcome of a single eval run for one task and model.
type EvalResult struct {
	ID         int64     `json:"id"`
	RunID      string    `json:"run_id"`
	TaskID     string    `json:"task_id"`
	Model      string    `json:"model"`
	Passed     bool      `json:"passed"`
	DurationMs int64     `json:"duration_ms"`
	TokensUsed int64     `json:"tokens_used"`
	CostUSD    float64   `json:"cost_usd"`
	ErrorMsg   string    `json:"error_msg,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// EvalStats aggregates eval results for a model.
type EvalStats struct {
	Model       string  `json:"model"`
	TotalTasks  int     `json:"total_tasks"`
	Passed      int     `json:"passed"`
	Failed      int     `json:"failed"`
	PassRate    float64 `json:"pass_rate"`
	AvgDuration int64   `json:"avg_duration_ms"`
	AvgTokens   int64   `json:"avg_tokens"`
	TotalCost   float64 `json:"total_cost_usd"`
}

// SaveEvalResult persists an eval result row.
func (s *Store) SaveEvalResult(r *EvalResult) error {
	return s.withRetry("SaveEvalResult", func() error {
		res, err := s.db.Exec(`
			INSERT INTO eval_results (run_id, task_id, model, passed, duration_ms, tokens_used, cost_usd, error_msg)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, r.RunID, r.TaskID, r.Model, r.Passed, r.DurationMs, r.TokensUsed, r.CostUSD, r.ErrorMsg)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		r.ID = id
		return nil
	})
}

// EvalResultFilter controls which eval results are returned by GetEvalResults.
type EvalResultFilter struct {
	RunID string
	Model string
	Limit int
}

// GetEvalResults returns eval results matching the filter, ordered by created_at DESC.
func (s *Store) GetEvalResults(filter EvalResultFilter) ([]*EvalResult, error) {
	var conditions []string
	var args []interface{}

	if filter.RunID != "" {
		conditions = append(conditions, "run_id = ?")
		args = append(args, filter.RunID)
	}
	if filter.Model != "" {
		conditions = append(conditions, "model = ?")
		args = append(args, filter.Model)
	}

	query := "SELECT id, run_id, task_id, model, passed, duration_ms, tokens_used, cost_usd, error_msg, created_at FROM eval_results"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query eval_results: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []*EvalResult
	for rows.Next() {
		var r EvalResult
		if err := rows.Scan(&r.ID, &r.RunID, &r.TaskID, &r.Model, &r.Passed,
			&r.DurationMs, &r.TokensUsed, &r.CostUSD, &r.ErrorMsg, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan eval_result: %w", err)
		}
		results = append(results, &r)
	}
	return results, rows.Err()
}

// GetEvalStats returns aggregated stats per model for a given run. If runID is empty, aggregates all runs.
func (s *Store) GetEvalStats(runID string) ([]*EvalStats, error) {
	query := `SELECT model, COUNT(*) as total, SUM(CASE WHEN passed THEN 1 ELSE 0 END) as passed,
		SUM(CASE WHEN passed THEN 0 ELSE 1 END) as failed,
		AVG(duration_ms) as avg_dur, AVG(tokens_used) as avg_tok, SUM(cost_usd) as total_cost
		FROM eval_results`
	var args []interface{}
	if runID != "" {
		query += " WHERE run_id = ?"
		args = append(args, runID)
	}
	query += " GROUP BY model ORDER BY model"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query eval_stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []*EvalStats
	for rows.Next() {
		var st EvalStats
		var avgDur, avgTok float64
		if err := rows.Scan(&st.Model, &st.TotalTasks, &st.Passed, &st.Failed,
			&avgDur, &avgTok, &st.TotalCost); err != nil {
			return nil, fmt.Errorf("scan eval_stats: %w", err)
		}
		st.AvgDuration = int64(avgDur)
		st.AvgTokens = int64(avgTok)
		if st.TotalTasks > 0 {
			st.PassRate = float64(st.Passed) / float64(st.TotalTasks) * 100
		}
		stats = append(stats, &st)
	}
	return stats, rows.Err()
}

// EvalTaskFilter controls which eval tasks are returned by ListEvalTasks.
type EvalTaskFilter struct {
	Repo        string
	ExecutionID string // Filter by execution_id (used as eval run identifier)
	SuccessOnly bool
	FailedOnly  bool
	Limit       int
}

// ListEvalTasks returns eval tasks matching the given filter, ordered by created_at DESC.
func (s *Store) ListEvalTasks(filter EvalTaskFilter) ([]*EvalTask, error) {
	var conditions []string
	var args []interface{}

	if filter.Repo != "" {
		conditions = append(conditions, "repo = ?")
		args = append(args, filter.Repo)
	}
	if filter.ExecutionID != "" {
		conditions = append(conditions, "execution_id = ?")
		args = append(args, filter.ExecutionID)
	}
	if filter.SuccessOnly {
		conditions = append(conditions, "success = 1")
	}
	if filter.FailedOnly {
		conditions = append(conditions, "success = 0")
	}

	query := "SELECT id, execution_id, issue_number, issue_title, repo, success, pass_criteria, files_changed, duration_ms, created_at FROM eval_tasks"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query eval_tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tasks []*EvalTask
	for rows.Next() {
		var t EvalTask
		var criteriaJSON, filesJSON string
		if err := rows.Scan(&t.ID, &t.ExecutionID, &t.IssueNumber, &t.IssueTitle, &t.Repo,
			&t.Success, &criteriaJSON, &filesJSON, &t.DurationMs, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan eval_task: %w", err)
		}
		if criteriaJSON != "" {
			_ = json.Unmarshal([]byte(criteriaJSON), &t.PassCriteria)
		}
		if filesJSON != "" {
			_ = json.Unmarshal([]byte(filesJSON), &t.FilesChanged)
		}
		tasks = append(tasks, &t)
	}
	return tasks, rows.Err()
}
