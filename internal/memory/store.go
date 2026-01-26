package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store provides persistent storage for Pilot
type Store struct {
	db   *sql.DB
	path string
}

// NewStore creates a new memory store
func NewStore(dataPath string) (*Store, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(dataPath, "pilot.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &Store{
		db:   db,
		path: dataPath,
	}

	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

// migrate creates necessary tables
func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS executions (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			project_path TEXT NOT NULL,
			status TEXT NOT NULL,
			output TEXT,
			error TEXT,
			duration_ms INTEGER,
			pr_url TEXT,
			commit_sha TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS patterns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_path TEXT,
			pattern_type TEXT NOT NULL,
			content TEXT NOT NULL,
			confidence REAL DEFAULT 1.0,
			uses INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			path TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			navigator_enabled BOOLEAN DEFAULT TRUE,
			last_active DATETIME DEFAULT CURRENT_TIMESTAMP,
			settings TEXT
		)`,
		// Cross-project pattern tables (TASK-11)
		`CREATE TABLE IF NOT EXISTS cross_patterns (
			id TEXT PRIMARY KEY,
			pattern_type TEXT NOT NULL,
			title TEXT NOT NULL,
			description TEXT NOT NULL,
			context TEXT,
			examples TEXT,
			confidence REAL DEFAULT 0.5,
			occurrences INTEGER DEFAULT 1,
			is_anti_pattern BOOLEAN DEFAULT FALSE,
			scope TEXT DEFAULT 'org',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS pattern_projects (
			pattern_id TEXT NOT NULL,
			project_path TEXT NOT NULL,
			uses INTEGER DEFAULT 1,
			success_count INTEGER DEFAULT 0,
			failure_count INTEGER DEFAULT 0,
			last_used DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (pattern_id, project_path),
			FOREIGN KEY (pattern_id) REFERENCES cross_patterns(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS pattern_feedback (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pattern_id TEXT NOT NULL,
			execution_id TEXT NOT NULL,
			project_path TEXT NOT NULL,
			outcome TEXT NOT NULL,
			confidence_delta REAL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (pattern_id) REFERENCES cross_patterns(id) ON DELETE CASCADE,
			FOREIGN KEY (execution_id) REFERENCES executions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_executions_task ON executions(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_executions_project ON executions(project_path)`,
		`CREATE INDEX IF NOT EXISTS idx_executions_created ON executions(created_at)`,
		// Metrics columns (TASK-13)
		`ALTER TABLE executions ADD COLUMN tokens_input INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN tokens_output INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN tokens_total INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN estimated_cost_usd REAL DEFAULT 0.0`,
		`ALTER TABLE executions ADD COLUMN files_changed INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN lines_added INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN lines_removed INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN model_name TEXT DEFAULT 'claude-sonnet-4-5'`,
		`CREATE INDEX IF NOT EXISTS idx_patterns_project ON patterns(project_path)`,
		// Cross-project pattern indexes
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_type ON cross_patterns(pattern_type)`,
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_scope ON cross_patterns(scope)`,
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_confidence ON cross_patterns(confidence DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_pattern_projects_project ON pattern_projects(project_path)`,
		`CREATE INDEX IF NOT EXISTS idx_pattern_feedback_pattern ON pattern_feedback(pattern_id)`,
		// Usage metering tables (TASK-16)
		`CREATE TABLE IF NOT EXISTS usage_events (
			id TEXT PRIMARY KEY,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			user_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			quantity INTEGER DEFAULT 0,
			unit_cost REAL DEFAULT 0.0,
			total_cost REAL DEFAULT 0.0,
			metadata TEXT,
			execution_id TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_user ON usage_events(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_project ON usage_events(project_id)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp ON usage_events(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_type ON usage_events(event_type)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_events_execution ON usage_events(execution_id)`,
	}

	for _, migration := range migrations {
		_, err := s.db.Exec(migration)
		if err != nil {
			// Ignore "duplicate column" errors from ALTER TABLE migrations
			// SQLite returns "duplicate column name" when column already exists
			errStr := err.Error()
			if strings.Contains(errStr, "duplicate column") {
				continue
			}
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// Execution represents a task execution record
type Execution struct {
	ID          string
	TaskID      string
	ProjectPath string
	Status      string
	Output      string
	Error       string
	DurationMs  int64
	PRUrl       string
	CommitSHA   string
	CreatedAt   time.Time
	CompletedAt *time.Time
	// Metrics fields (TASK-13)
	TokensInput      int64
	TokensOutput     int64
	TokensTotal      int64
	EstimatedCostUSD float64
	FilesChanged     int
	LinesAdded       int
	LinesRemoved     int
	ModelName        string
}

// SaveExecution saves an execution record
func (s *Store) SaveExecution(exec *Execution) error {
	_, err := s.db.Exec(`
		INSERT INTO executions (id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, completed_at,
			tokens_input, tokens_output, tokens_total, estimated_cost_usd, files_changed, lines_added, lines_removed, model_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, exec.ID, exec.TaskID, exec.ProjectPath, exec.Status, exec.Output, exec.Error, exec.DurationMs, exec.PRUrl, exec.CommitSHA, exec.CompletedAt,
		exec.TokensInput, exec.TokensOutput, exec.TokensTotal, exec.EstimatedCostUSD, exec.FilesChanged, exec.LinesAdded, exec.LinesRemoved, exec.ModelName)
	return err
}

// GetExecution retrieves an execution by ID
func (s *Store) GetExecution(id string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(tokens_input, 0), COALESCE(tokens_output, 0), COALESCE(tokens_total, 0),
			COALESCE(estimated_cost_usd, 0), COALESCE(files_changed, 0), COALESCE(lines_added, 0),
			COALESCE(lines_removed, 0), COALESCE(model_name, '')
		FROM executions WHERE id = ?
	`, id)

	var exec Execution
	var completedAt sql.NullTime
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
		&exec.TokensInput, &exec.TokensOutput, &exec.TokensTotal, &exec.EstimatedCostUSD, &exec.FilesChanged, &exec.LinesAdded, &exec.LinesRemoved, &exec.ModelName)
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		exec.CompletedAt = &completedAt.Time
	}

	return &exec, nil
}

// GetRecentExecutions returns recent executions
func (s *Store) GetRecentExecutions(limit int) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

// Pattern represents a learned pattern
type Pattern struct {
	ID          int64
	ProjectPath string
	Type        string
	Content     string
	Confidence  float64
	Uses        int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SavePattern saves or updates a pattern
func (s *Store) SavePattern(pattern *Pattern) error {
	if pattern.ID == 0 {
		result, err := s.db.Exec(`
			INSERT INTO patterns (project_path, pattern_type, content, confidence)
			VALUES (?, ?, ?, ?)
		`, pattern.ProjectPath, pattern.Type, pattern.Content, pattern.Confidence)
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		pattern.ID = id
	} else {
		_, err := s.db.Exec(`
			UPDATE patterns SET content = ?, confidence = ?, uses = uses + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, pattern.Content, pattern.Confidence, pattern.ID)
		return err
	}
	return nil
}

// GetPatterns retrieves patterns for a project
func (s *Store) GetPatterns(projectPath string) ([]*Pattern, error) {
	rows, err := s.db.Query(`
		SELECT id, project_path, pattern_type, content, confidence, uses, created_at, updated_at
		FROM patterns WHERE project_path = ? OR project_path IS NULL
		ORDER BY confidence DESC, uses DESC
	`, projectPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var patterns []*Pattern
	for rows.Next() {
		var p Pattern
		var projectPath sql.NullString
		if err := rows.Scan(&p.ID, &projectPath, &p.Type, &p.Content, &p.Confidence, &p.Uses, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if projectPath.Valid {
			p.ProjectPath = projectPath.String
		}
		patterns = append(patterns, &p)
	}

	return patterns, nil
}

// Project represents a registered project
type Project struct {
	Path             string
	Name             string
	NavigatorEnabled bool
	LastActive       time.Time
	Settings         map[string]interface{}
}

// SaveProject saves or updates a project
func (s *Store) SaveProject(project *Project) error {
	settings, _ := json.Marshal(project.Settings)
	_, err := s.db.Exec(`
		INSERT INTO projects (path, name, navigator_enabled, settings)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			name = excluded.name,
			navigator_enabled = excluded.navigator_enabled,
			last_active = CURRENT_TIMESTAMP,
			settings = excluded.settings
	`, project.Path, project.Name, project.NavigatorEnabled, string(settings))
	return err
}

// GetProject retrieves a project by path
func (s *Store) GetProject(path string) (*Project, error) {
	row := s.db.QueryRow(`
		SELECT path, name, navigator_enabled, last_active, settings
		FROM projects WHERE path = ?
	`, path)

	var p Project
	var settingsStr string
	if err := row.Scan(&p.Path, &p.Name, &p.NavigatorEnabled, &p.LastActive, &settingsStr); err != nil {
		return nil, err
	}

	if settingsStr != "" {
		_ = json.Unmarshal([]byte(settingsStr), &p.Settings)
	}

	return &p, nil
}

// GetAllProjects retrieves all projects
func (s *Store) GetAllProjects() ([]*Project, error) {
	rows, err := s.db.Query(`
		SELECT path, name, navigator_enabled, last_active, settings
		FROM projects ORDER BY last_active DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var projects []*Project
	for rows.Next() {
		var p Project
		var settingsStr string
		if err := rows.Scan(&p.Path, &p.Name, &p.NavigatorEnabled, &p.LastActive, &settingsStr); err != nil {
			return nil, err
		}
		if settingsStr != "" {
			_ = json.Unmarshal([]byte(settingsStr), &p.Settings)
		}
		projects = append(projects, &p)
	}

	return projects, nil
}

// BriefQuery holds parameters for querying brief data
type BriefQuery struct {
	Start    time.Time
	End      time.Time
	Projects []string // Empty = all projects
}

// GetExecutionsInPeriod retrieves executions within a time period
func (s *Store) GetExecutionsInPeriod(query BriefQuery) ([]*Execution, error) {
	var rows *sql.Rows
	var err error

	if len(query.Projects) > 0 {
		// Build placeholders for IN clause
		placeholders := ""
		args := make([]interface{}, 0, len(query.Projects)+2)
		args = append(args, query.Start, query.End)
		for i, p := range query.Projects {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, p)
		}
		rows, err = s.db.Query(`
			SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
			FROM executions
			WHERE created_at >= ? AND created_at < ?
			AND project_path IN (`+placeholders+`)
			ORDER BY created_at DESC
		`, args...)
	} else {
		rows, err = s.db.Query(`
			SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
			FROM executions
			WHERE created_at >= ? AND created_at < ?
			ORDER BY created_at DESC
		`, query.Start, query.End)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

// GetActiveExecutions retrieves currently running executions
func (s *Store) GetActiveExecutions() ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions
		WHERE status = 'running'
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

// GetBriefMetrics calculates aggregate metrics for a time period
func (s *Store) GetBriefMetrics(query BriefQuery) (*BriefMetricsData, error) {
	var result BriefMetricsData

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

	// Get counts and averages
	row := s.db.QueryRow(`
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed,
			COALESCE(AVG(CASE WHEN status = 'completed' THEN duration_ms END), 0) as avg_duration,
			COALESCE(SUM(CASE WHEN pr_url != '' THEN 1 ELSE 0 END), 0) as prs_created
		FROM executions
	`+whereClause, args...)

	if err := row.Scan(&result.TotalTasks, &result.CompletedCount, &result.FailedCount, &result.AvgDurationMs, &result.PRsCreated); err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	if result.TotalTasks > 0 {
		result.SuccessRate = float64(result.CompletedCount) / float64(result.TotalTasks)
	}

	return &result, nil
}

// BriefMetricsData holds raw metrics from the database
type BriefMetricsData struct {
	TotalTasks     int
	CompletedCount int
	FailedCount    int
	SuccessRate    float64
	AvgDurationMs  int64
	PRsCreated     int
}

// GetQueuedTasks returns tasks waiting to be executed
func (s *Store) GetQueuedTasks(limit int) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions
		WHERE status = 'queued' OR status = 'pending'
		ORDER BY created_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

// CrossPattern represents a pattern that applies across projects (TASK-11)
type CrossPattern struct {
	ID            string
	Type          string
	Title         string
	Description   string
	Context       string
	Examples      []string
	Confidence    float64
	Occurrences   int
	IsAntiPattern bool
	Scope         string // "project", "org", "global"
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PatternProjectLink represents the relationship between a pattern and a project
type PatternProjectLink struct {
	PatternID    string
	ProjectPath  string
	Uses         int
	SuccessCount int
	FailureCount int
	LastUsed     time.Time
}

// PatternFeedback records outcome when a pattern was applied
type PatternFeedback struct {
	ID              int64
	PatternID       string
	ExecutionID     string
	ProjectPath     string
	Outcome         string // "success", "failure", "neutral"
	ConfidenceDelta float64
	CreatedAt       time.Time
}

// SaveCrossPattern saves or updates a cross-project pattern
func (s *Store) SaveCrossPattern(pattern *CrossPattern) error {
	examples, _ := json.Marshal(pattern.Examples)

	_, err := s.db.Exec(`
		INSERT INTO cross_patterns (id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			context = excluded.context,
			examples = excluded.examples,
			confidence = excluded.confidence,
			occurrences = cross_patterns.occurrences + 1,
			updated_at = CURRENT_TIMESTAMP
	`, pattern.ID, pattern.Type, pattern.Title, pattern.Description, pattern.Context, string(examples), pattern.Confidence, pattern.Occurrences, pattern.IsAntiPattern, pattern.Scope)
	return err
}

// GetCrossPattern retrieves a cross-project pattern by ID
func (s *Store) GetCrossPattern(id string) (*CrossPattern, error) {
	row := s.db.QueryRow(`
		SELECT id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope, created_at, updated_at
		FROM cross_patterns WHERE id = ?
	`, id)

	var p CrossPattern
	var examplesStr string
	if err := row.Scan(&p.ID, &p.Type, &p.Title, &p.Description, &p.Context, &examplesStr, &p.Confidence, &p.Occurrences, &p.IsAntiPattern, &p.Scope, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}

	if examplesStr != "" {
		_ = json.Unmarshal([]byte(examplesStr), &p.Examples)
	}

	return &p, nil
}

// GetCrossPatternsByType retrieves cross-project patterns by type
func (s *Store) GetCrossPatternsByType(patternType string) ([]*CrossPattern, error) {
	rows, err := s.db.Query(`
		SELECT id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope, created_at, updated_at
		FROM cross_patterns
		WHERE pattern_type = ?
		ORDER BY confidence DESC, occurrences DESC
	`, patternType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanCrossPatterns(rows)
}

// GetCrossPatternsForProject retrieves patterns relevant to a specific project
func (s *Store) GetCrossPatternsForProject(projectPath string, includeGlobal bool) ([]*CrossPattern, error) {
	query := `
		SELECT DISTINCT cp.id, cp.pattern_type, cp.title, cp.description, cp.context, cp.examples,
		       cp.confidence, cp.occurrences, cp.is_anti_pattern, cp.scope, cp.created_at, cp.updated_at
		FROM cross_patterns cp
		LEFT JOIN pattern_projects pp ON cp.id = pp.pattern_id
		WHERE pp.project_path = ?
		   OR cp.scope = 'org'
	`
	if includeGlobal {
		query += ` OR cp.scope = 'global'`
	}
	query += ` ORDER BY cp.confidence DESC, cp.occurrences DESC`

	rows, err := s.db.Query(query, projectPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanCrossPatterns(rows)
}

// GetTopCrossPatterns retrieves the most confident patterns
func (s *Store) GetTopCrossPatterns(limit int, minConfidence float64) ([]*CrossPattern, error) {
	rows, err := s.db.Query(`
		SELECT id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope, created_at, updated_at
		FROM cross_patterns
		WHERE confidence >= ?
		ORDER BY confidence DESC, occurrences DESC
		LIMIT ?
	`, minConfidence, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanCrossPatterns(rows)
}

// scanCrossPatterns scans rows into CrossPattern slice
func (s *Store) scanCrossPatterns(rows *sql.Rows) ([]*CrossPattern, error) {
	var patterns []*CrossPattern
	for rows.Next() {
		var p CrossPattern
		var examplesStr string
		if err := rows.Scan(&p.ID, &p.Type, &p.Title, &p.Description, &p.Context, &examplesStr, &p.Confidence, &p.Occurrences, &p.IsAntiPattern, &p.Scope, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if examplesStr != "" {
			_ = json.Unmarshal([]byte(examplesStr), &p.Examples)
		}
		patterns = append(patterns, &p)
	}
	return patterns, nil
}

// LinkPatternToProject creates or updates a pattern-project relationship
func (s *Store) LinkPatternToProject(patternID, projectPath string) error {
	_, err := s.db.Exec(`
		INSERT INTO pattern_projects (pattern_id, project_path, uses, last_used)
		VALUES (?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(pattern_id, project_path) DO UPDATE SET
			uses = pattern_projects.uses + 1,
			last_used = CURRENT_TIMESTAMP
	`, patternID, projectPath)
	return err
}

// GetProjectsForPattern retrieves all projects using a pattern
func (s *Store) GetProjectsForPattern(patternID string) ([]*PatternProjectLink, error) {
	rows, err := s.db.Query(`
		SELECT pattern_id, project_path, uses, success_count, failure_count, last_used
		FROM pattern_projects
		WHERE pattern_id = ?
		ORDER BY uses DESC
	`, patternID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var links []*PatternProjectLink
	for rows.Next() {
		var link PatternProjectLink
		if err := rows.Scan(&link.PatternID, &link.ProjectPath, &link.Uses, &link.SuccessCount, &link.FailureCount, &link.LastUsed); err != nil {
			return nil, err
		}
		links = append(links, &link)
	}
	return links, nil
}

// RecordPatternFeedback records feedback when a pattern is applied
func (s *Store) RecordPatternFeedback(feedback *PatternFeedback) error {
	result, err := s.db.Exec(`
		INSERT INTO pattern_feedback (pattern_id, execution_id, project_path, outcome, confidence_delta)
		VALUES (?, ?, ?, ?, ?)
	`, feedback.PatternID, feedback.ExecutionID, feedback.ProjectPath, feedback.Outcome, feedback.ConfidenceDelta)
	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	feedback.ID = id

	// Update pattern confidence and project link based on outcome
	switch feedback.Outcome {
	case "success":
		_, _ = s.db.Exec(`
			UPDATE cross_patterns SET confidence = MIN(0.95, confidence + ?) WHERE id = ?
		`, feedback.ConfidenceDelta, feedback.PatternID)
		_, _ = s.db.Exec(`
			UPDATE pattern_projects SET success_count = success_count + 1 WHERE pattern_id = ? AND project_path = ?
		`, feedback.PatternID, feedback.ProjectPath)
	case "failure":
		_, _ = s.db.Exec(`
			UPDATE cross_patterns SET confidence = MAX(0.1, confidence - ?) WHERE id = ?
		`, feedback.ConfidenceDelta, feedback.PatternID)
		_, _ = s.db.Exec(`
			UPDATE pattern_projects SET failure_count = failure_count + 1 WHERE pattern_id = ? AND project_path = ?
		`, feedback.PatternID, feedback.ProjectPath)
	}

	return nil
}

// SearchCrossPatterns searches patterns by title or description
func (s *Store) SearchCrossPatterns(query string, limit int) ([]*CrossPattern, error) {
	searchTerm := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT id, pattern_type, title, description, context, examples, confidence, occurrences, is_anti_pattern, scope, created_at, updated_at
		FROM cross_patterns
		WHERE title LIKE ? OR description LIKE ? OR context LIKE ?
		ORDER BY confidence DESC, occurrences DESC
		LIMIT ?
	`, searchTerm, searchTerm, searchTerm, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return s.scanCrossPatterns(rows)
}

// DeleteCrossPattern deletes a cross-project pattern
func (s *Store) DeleteCrossPattern(id string) error {
	_, err := s.db.Exec(`DELETE FROM cross_patterns WHERE id = ?`, id)
	return err
}

// GetCrossPatternStats returns statistics about cross-project patterns
func (s *Store) GetCrossPatternStats() (*CrossPatternStats, error) {
	var stats CrossPatternStats

	// Get total counts
	row := s.db.QueryRow(`
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN is_anti_pattern = 0 THEN 1 ELSE 0 END), 0) as patterns,
			COALESCE(SUM(CASE WHEN is_anti_pattern = 1 THEN 1 ELSE 0 END), 0) as anti_patterns,
			COALESCE(AVG(confidence), 0) as avg_confidence,
			COALESCE(SUM(occurrences), 0) as total_occurrences
		FROM cross_patterns
	`)
	if err := row.Scan(&stats.TotalPatterns, &stats.Patterns, &stats.AntiPatterns, &stats.AvgConfidence, &stats.TotalOccurrences); err != nil {
		return nil, err
	}

	// Get type breakdown
	rows, err := s.db.Query(`
		SELECT pattern_type, COUNT(*) as count
		FROM cross_patterns
		GROUP BY pattern_type
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	stats.ByType = make(map[string]int)
	for rows.Next() {
		var pType string
		var count int
		if err := rows.Scan(&pType, &count); err != nil {
			return nil, err
		}
		stats.ByType[pType] = count
	}

	// Get project count
	row = s.db.QueryRow(`SELECT COUNT(DISTINCT project_path) FROM pattern_projects`)
	_ = row.Scan(&stats.ProjectCount)

	return &stats, nil
}

// CrossPatternStats holds statistics about cross-project patterns
type CrossPatternStats struct {
	TotalPatterns    int
	Patterns         int
	AntiPatterns     int
	AvgConfidence    float64
	TotalOccurrences int
	ByType           map[string]int
	ProjectCount     int
}
