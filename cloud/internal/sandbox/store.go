package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

// Store provides execution storage
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a new execution store
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Project holds minimal project info needed for execution
type Project struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	RepoURL       string
	DefaultBranch string
	Settings      ProjectSettings
}

// ProjectSettings for execution
type ProjectSettings struct {
	NavigatorEnabled bool `json:"navigator_enabled"`
}

// CreateExecution creates a new execution record
func (s *Store) CreateExecution(ctx context.Context, exec *Execution) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO executions (id, org_id, project_id, external_task_id, status, phase, progress, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, exec.ID, exec.OrgID, exec.ProjectID, exec.ExternalTaskID, exec.Status, exec.Phase, exec.Progress, exec.CreatedAt)

	if err != nil {
		return fmt.Errorf("failed to create execution: %w", err)
	}
	return nil
}

// GetExecution retrieves an execution by ID
func (s *Store) GetExecution(ctx context.Context, id uuid.UUID) (*Execution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, project_id, external_task_id, status, phase, progress, output, error,
		       duration_ms, pr_url, commit_sha, tokens_used, cost_cents, created_at, started_at, completed_at
		FROM executions WHERE id = $1
	`, id)

	return s.scanExecution(row)
}

func (s *Store) scanExecution(row pgx.Row) (*Execution, error) {
	var exec Execution
	var externalTaskID *string
	var output, errMsg, prURL, commitSHA *string
	var startedAt, completedAt *time.Time

	err := row.Scan(&exec.ID, &exec.OrgID, &exec.ProjectID, &externalTaskID, &exec.Status,
		&exec.Phase, &exec.Progress, &output, &errMsg, &exec.DurationMs, &prURL, &commitSHA,
		&exec.TokensUsed, &exec.CostCents, &exec.CreatedAt, &startedAt, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if externalTaskID != nil {
		exec.ExternalTaskID = *externalTaskID
	}
	if output != nil {
		exec.Output = *output
	}
	if errMsg != nil {
		exec.Error = *errMsg
	}
	if prURL != nil {
		exec.PRUrl = *prURL
	}
	if commitSHA != nil {
		exec.CommitSHA = *commitSHA
	}
	exec.StartedAt = startedAt
	exec.CompletedAt = completedAt

	return &exec, nil
}

// UpdateExecution updates an execution record
func (s *Store) UpdateExecution(ctx context.Context, exec *Execution) error {
	result, err := s.pool.Exec(ctx, `
		UPDATE executions SET
			status = $2, phase = $3, progress = $4, output = $5, error = $6,
			duration_ms = $7, pr_url = $8, commit_sha = $9, tokens_used = $10,
			cost_cents = $11, started_at = $12, completed_at = $13
		WHERE id = $1
	`, exec.ID, exec.Status, exec.Phase, exec.Progress, exec.Output, exec.Error,
		exec.DurationMs, exec.PRUrl, exec.CommitSHA, exec.TokensUsed,
		exec.CostCents, exec.StartedAt, exec.CompletedAt)

	if err != nil {
		return fmt.Errorf("failed to update execution: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListExecutions returns executions for an organization
func (s *Store) ListExecutions(ctx context.Context, orgID uuid.UUID, limit, offset int) ([]*Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, project_id, external_task_id, status, phase, progress, output, error,
		       duration_ms, pr_url, commit_sha, tokens_used, cost_cents, created_at, started_at, completed_at
		FROM executions WHERE org_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, orgID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var externalTaskID *string
		var output, errMsg, prURL, commitSHA *string
		var startedAt, completedAt *time.Time

		if err := rows.Scan(&exec.ID, &exec.OrgID, &exec.ProjectID, &externalTaskID, &exec.Status,
			&exec.Phase, &exec.Progress, &output, &errMsg, &exec.DurationMs, &prURL, &commitSHA,
			&exec.TokensUsed, &exec.CostCents, &exec.CreatedAt, &startedAt, &completedAt); err != nil {
			return nil, err
		}

		if externalTaskID != nil {
			exec.ExternalTaskID = *externalTaskID
		}
		if output != nil {
			exec.Output = *output
		}
		if errMsg != nil {
			exec.Error = *errMsg
		}
		if prURL != nil {
			exec.PRUrl = *prURL
		}
		if commitSHA != nil {
			exec.CommitSHA = *commitSHA
		}
		exec.StartedAt = startedAt
		exec.CompletedAt = completedAt
		executions = append(executions, &exec)
	}

	return executions, nil
}

// ListExecutionsByProject returns executions for a project
func (s *Store) ListExecutionsByProject(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]*Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, project_id, external_task_id, status, phase, progress, output, error,
		       duration_ms, pr_url, commit_sha, tokens_used, cost_cents, created_at, started_at, completed_at
		FROM executions WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var externalTaskID *string
		var output, errMsg, prURL, commitSHA *string
		var startedAt, completedAt *time.Time

		if err := rows.Scan(&exec.ID, &exec.OrgID, &exec.ProjectID, &externalTaskID, &exec.Status,
			&exec.Phase, &exec.Progress, &output, &errMsg, &exec.DurationMs, &prURL, &commitSHA,
			&exec.TokensUsed, &exec.CostCents, &exec.CreatedAt, &startedAt, &completedAt); err != nil {
			return nil, err
		}

		if externalTaskID != nil {
			exec.ExternalTaskID = *externalTaskID
		}
		if output != nil {
			exec.Output = *output
		}
		if errMsg != nil {
			exec.Error = *errMsg
		}
		if prURL != nil {
			exec.PRUrl = *prURL
		}
		if commitSHA != nil {
			exec.CommitSHA = *commitSHA
		}
		exec.StartedAt = startedAt
		exec.CompletedAt = completedAt
		executions = append(executions, &exec)
	}

	return executions, nil
}

// CountExecutionsByStatus counts executions with a given status
func (s *Store) CountExecutionsByStatus(ctx context.Context, status ExecutionStatus) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM executions WHERE status = $1
	`, status).Scan(&count)
	return count, err
}

// GetProject retrieves a project by ID
func (s *Store) GetProject(ctx context.Context, id uuid.UUID) (*Project, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, org_id, repo_url, default_branch, settings
		FROM projects WHERE id = $1
	`, id)

	var p Project
	var settingsJSON []byte

	err := row.Scan(&p.ID, &p.OrgID, &p.RepoURL, &p.DefaultBranch, &settingsJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if settingsJSON != nil {
		_ = json.Unmarshal(settingsJSON, &p.Settings)
	}

	return &p, nil
}

// GetQueuedExecutions returns executions ready to be processed
func (s *Store) GetQueuedExecutions(ctx context.Context, limit int) ([]*Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, project_id, external_task_id, status, phase, progress, output, error,
		       duration_ms, pr_url, commit_sha, tokens_used, cost_cents, created_at, started_at, completed_at
		FROM executions
		WHERE status = $1
		ORDER BY created_at ASC
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, StatusQueued, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var externalTaskID *string
		var output, errMsg, prURL, commitSHA *string
		var startedAt, completedAt *time.Time

		if err := rows.Scan(&exec.ID, &exec.OrgID, &exec.ProjectID, &externalTaskID, &exec.Status,
			&exec.Phase, &exec.Progress, &output, &errMsg, &exec.DurationMs, &prURL, &commitSHA,
			&exec.TokensUsed, &exec.CostCents, &exec.CreatedAt, &startedAt, &completedAt); err != nil {
			return nil, err
		}

		if externalTaskID != nil {
			exec.ExternalTaskID = *externalTaskID
		}
		if output != nil {
			exec.Output = *output
		}
		if errMsg != nil {
			exec.Error = *errMsg
		}
		if prURL != nil {
			exec.PRUrl = *prURL
		}
		if commitSHA != nil {
			exec.CommitSHA = *commitSHA
		}
		exec.StartedAt = startedAt
		exec.CompletedAt = completedAt
		executions = append(executions, &exec)
	}

	return executions, nil
}

// GetExecutionMetrics returns aggregate metrics for an org
func (s *Store) GetExecutionMetrics(ctx context.Context, orgID uuid.UUID, start, end time.Time) (*ExecutionMetrics, error) {
	var metrics ExecutionMetrics

	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(AVG(CASE WHEN status = 'completed' THEN duration_ms END), 0) as avg_duration,
			COALESCE(SUM(tokens_used), 0) as total_tokens,
			COALESCE(SUM(cost_cents), 0) as total_cost
		FROM executions
		WHERE org_id = $1 AND created_at >= $2 AND created_at < $3
	`, orgID, start, end).Scan(&metrics.TotalExecutions, &metrics.CompletedCount, &metrics.FailedCount,
		&metrics.AvgDurationMs, &metrics.TotalTokens, &metrics.TotalCostCents)

	if err != nil {
		return nil, err
	}

	if metrics.TotalExecutions > 0 {
		metrics.SuccessRate = float64(metrics.CompletedCount) / float64(metrics.TotalExecutions)
	}

	return &metrics, nil
}

// ExecutionMetrics holds aggregate metrics
type ExecutionMetrics struct {
	TotalExecutions int
	CompletedCount  int
	FailedCount     int
	SuccessRate     float64
	AvgDurationMs   int64
	TotalTokens     int64
	TotalCostCents  int
}
