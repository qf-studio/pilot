package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides persistent storage for Pilot using SQLite.
// It manages executions, patterns, projects, and cross-project learning data.
// Store handles database migrations automatically on initialization.
type Store struct {
	db   *sql.DB
	path string
}

// NewStore creates a new Store instance with a SQLite database at the given path.
// It creates the data directory if it does not exist and runs database migrations.
// Returns an error if the database cannot be opened or migrations fail.
func NewStore(dataPath string) (*Store, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	dbPath := filepath.Join(dataPath, "pilot.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable WAL mode and busy timeout for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		return nil, fmt.Errorf("failed to set database pragmas: %w", err)
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
		// Task queue columns for storing task details (GH-46)
		`ALTER TABLE executions ADD COLUMN task_title TEXT`,
		`ALTER TABLE executions ADD COLUMN task_description TEXT`,
		`ALTER TABLE executions ADD COLUMN task_branch TEXT`,
		`ALTER TABLE executions ADD COLUMN task_base_branch TEXT`,
		`ALTER TABLE executions ADD COLUMN task_create_pr BOOLEAN DEFAULT FALSE`,
		`ALTER TABLE executions ADD COLUMN task_verbose BOOLEAN DEFAULT FALSE`,
		`CREATE INDEX IF NOT EXISTS idx_executions_status ON executions(status)`,
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
		// Dashboard sessions table (GH-367)
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			date TEXT NOT NULL,
			started_at DATETIME NOT NULL,
			ended_at DATETIME,
			total_input_tokens INTEGER DEFAULT 0,
			total_output_tokens INTEGER DEFAULT 0,
			total_cost_cents INTEGER DEFAULT 0,
			tasks_completed INTEGER DEFAULT 0,
			tasks_failed INTEGER DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_date ON sessions(date)`,
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

// Close closes the database connection and releases resources.
func (s *Store) Close() error {
	return s.db.Close()
}

// Execution represents a task execution record stored in the database.
// It captures the complete execution history including status, output, metrics, and PR information.
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
	// Task queue fields (GH-46) - store task details for deferred execution
	TaskTitle       string
	TaskDescription string
	TaskBranch      string
	TaskBaseBranch  string
	TaskCreatePR    bool
	TaskVerbose     bool
}

// SaveExecution saves an execution record to the database.
// The execution ID must be unique; duplicate IDs will cause an error.
func (s *Store) SaveExecution(exec *Execution) error {
	_, err := s.db.Exec(`
		INSERT INTO executions (id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, completed_at,
			tokens_input, tokens_output, tokens_total, estimated_cost_usd, files_changed, lines_added, lines_removed, model_name,
			task_title, task_description, task_branch, task_base_branch, task_create_pr, task_verbose)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, exec.ID, exec.TaskID, exec.ProjectPath, exec.Status, exec.Output, exec.Error, exec.DurationMs, exec.PRUrl, exec.CommitSHA, exec.CompletedAt,
		exec.TokensInput, exec.TokensOutput, exec.TokensTotal, exec.EstimatedCostUSD, exec.FilesChanged, exec.LinesAdded, exec.LinesRemoved, exec.ModelName,
		exec.TaskTitle, exec.TaskDescription, exec.TaskBranch, exec.TaskBaseBranch, exec.TaskCreatePR, exec.TaskVerbose)
	return err
}

// GetExecution retrieves an execution by its unique ID.
// Returns sql.ErrNoRows if the execution is not found.
func (s *Store) GetExecution(id string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(tokens_input, 0), COALESCE(tokens_output, 0), COALESCE(tokens_total, 0),
			COALESCE(estimated_cost_usd, 0), COALESCE(files_changed, 0), COALESCE(lines_added, 0),
			COALESCE(lines_removed, 0), COALESCE(model_name, ''),
			COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
			COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0)
		FROM executions WHERE id = ?
	`, id)

	var exec Execution
	var completedAt sql.NullTime
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
		&exec.TokensInput, &exec.TokensOutput, &exec.TokensTotal, &exec.EstimatedCostUSD, &exec.FilesChanged, &exec.LinesAdded, &exec.LinesRemoved, &exec.ModelName,
		&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose)
	if err != nil {
		return nil, err
	}

	if completedAt.Valid {
		exec.CompletedAt = &completedAt.Time
	}

	return &exec, nil
}

// GetRecentExecutions returns the most recent executions ordered by creation time.
// The limit parameter specifies the maximum number of executions to return.
func (s *Store) GetRecentExecutions(limit int) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
			COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0)
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
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
			&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

// Pattern represents a learned pattern from project executions.
// Patterns capture recurring code structures, workflows, or solutions
// that can be applied to future similar tasks.
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

// SavePattern saves a new pattern or updates an existing one.
// If pattern.ID is zero, a new pattern is inserted; otherwise the existing pattern is updated.
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

// GetPatterns retrieves patterns applicable to a project.
// Returns both project-specific patterns and global patterns (those with no project path).
// Results are ordered by confidence and usage count descending.
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

// Project represents a registered project in Pilot.
// It stores project metadata, Navigator settings, and custom configuration.
type Project struct {
	Path             string
	Name             string
	NavigatorEnabled bool
	LastActive       time.Time
	Settings         map[string]interface{}
}

// SaveProject saves or updates a project in the database.
// If a project with the same path exists, it is updated; otherwise a new record is created.
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

// GetProject retrieves a project by its filesystem path.
// Returns sql.ErrNoRows if the project is not found.
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
		if err := json.Unmarshal([]byte(settingsStr), &p.Settings); err != nil {
			slog.Warn("failed to unmarshal project settings",
				slog.String("project_path", p.Path),
				slog.Any("error", err))
		}
	}

	return &p, nil
}

// GetAllProjects retrieves all registered projects ordered by last activity time.
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
			if err := json.Unmarshal([]byte(settingsStr), &p.Settings); err != nil {
				slog.Warn("failed to unmarshal project settings",
					slog.String("project_path", p.Path),
					slog.Any("error", err))
			}
		}
		projects = append(projects, &p)
	}

	return projects, nil
}

// BriefQuery holds parameters for querying execution data within a time period.
// Used for generating daily briefs and reports.
type BriefQuery struct {
	Start    time.Time
	End      time.Time
	Projects []string // Empty = all projects
}

// GetExecutionsInPeriod retrieves executions within the specified time range.
// If query.Projects is non-empty, results are filtered to those projects only.
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

// GetActiveExecutions retrieves all executions with status "running".
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

// GetBriefMetrics calculates aggregate metrics for a time period including
// task counts, success rates, average duration, and PR creation statistics.
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
			CAST(COALESCE(AVG(CASE WHEN status = 'completed' THEN duration_ms END), 0) AS INTEGER) as avg_duration,
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

// BriefMetricsData holds aggregate metrics calculated from execution data.
type BriefMetricsData struct {
	TotalTasks     int
	CompletedCount int
	FailedCount    int
	SuccessRate    float64
	AvgDurationMs  int64
	PRsCreated     int
}

// GetQueuedTasks returns tasks with status "queued" or "pending" waiting to be executed.
// Results are ordered by creation time ascending (oldest first) up to the specified limit.
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

// GetQueuedTasksForProject returns queued/pending tasks for a specific project.
// Results are ordered by creation time ascending (oldest first) up to the specified limit.
// This is used by the per-project worker to get the next task to execute.
func (s *Store) GetQueuedTasksForProject(projectPath string, limit int) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
			COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0)
		FROM executions
		WHERE (status = 'queued' OR status = 'pending') AND project_path = ?
		ORDER BY created_at ASC
		LIMIT ?
	`, projectPath, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
			&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, nil
}

// UpdateExecutionStatus updates the status of an execution record.
// Optionally sets the error message if provided. Also sets completed_at for terminal states.
func (s *Store) UpdateExecutionStatus(id, status string, errorMsg ...string) error {
	var errStr *string
	if len(errorMsg) > 0 && errorMsg[0] != "" {
		errStr = &errorMsg[0]
	}

	// Set completed_at for terminal states
	if status == "completed" || status == "failed" || status == "cancelled" {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = ?, error = COALESCE(?, error), completed_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, status, errStr, id)
		return err
	}

	_, err := s.db.Exec(`
		UPDATE executions
		SET status = ?, error = COALESCE(?, error)
		WHERE id = ?
	`, status, errStr, id)
	return err
}

// UpdateExecutionResult updates the result fields of an execution record.
// Called when task execution completes successfully with PR/commit info.
func (s *Store) UpdateExecutionResult(id string, prURL, commitSHA string, durationMs int64) error {
	_, err := s.db.Exec(`
		UPDATE executions
		SET pr_url = ?, commit_sha = ?, duration_ms = ?
		WHERE id = ?
	`, prURL, commitSHA, durationMs, id)
	return err
}

// GetStaleRunningExecutions returns executions that have been in "running" status
// for longer than the specified duration. Used to detect crashed workers on restart.
func (s *Store) GetStaleRunningExecutions(staleDuration time.Duration) ([]*Execution, error) {
	staleTime := time.Now().Add(-staleDuration)
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions
		WHERE status = 'running' AND created_at < ?
		ORDER BY created_at ASC
	`, staleTime)
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

// IsTaskQueued checks if a task with the given ID is already queued or running.
// Used to prevent duplicate task submissions.
func (s *Store) IsTaskQueued(taskID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM executions
		WHERE task_id = ? AND status IN ('queued', 'pending', 'running')
	`, taskID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CrossPattern represents a pattern that applies across multiple projects.
// It enables knowledge sharing between projects within an organization,
// tracking confidence based on usage outcomes.
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

// PatternProjectLink represents the relationship between a cross-project pattern and a specific project.
// It tracks usage statistics and success/failure counts for the pattern within that project.
type PatternProjectLink struct {
	PatternID    string
	ProjectPath  string
	Uses         int
	SuccessCount int
	FailureCount int
	LastUsed     time.Time
}

// PatternFeedback records the outcome when a pattern was applied during an execution.
// It is used to adjust pattern confidence based on real-world results.
type PatternFeedback struct {
	ID              int64
	PatternID       string
	ExecutionID     string
	ProjectPath     string
	Outcome         string // "success", "failure", "neutral"
	ConfidenceDelta float64
	CreatedAt       time.Time
}

// SaveCrossPattern saves a new cross-project pattern or updates an existing one.
// On conflict, the pattern is updated and its occurrence count is incremented.
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

// GetCrossPattern retrieves a cross-project pattern by its unique ID.
// Returns sql.ErrNoRows if the pattern is not found.
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
		if err := json.Unmarshal([]byte(examplesStr), &p.Examples); err != nil {
			slog.Warn("failed to unmarshal cross pattern examples",
				slog.String("pattern_id", p.ID),
				slog.Any("error", err))
		}
	}

	return &p, nil
}

// GetCrossPatternsByType retrieves all cross-project patterns of a specific type.
// Results are ordered by confidence and occurrence count descending.
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

// GetCrossPatternsForProject retrieves cross-project patterns relevant to a specific project.
// This includes patterns directly linked to the project and organization-scoped patterns.
// If includeGlobal is true, globally-scoped patterns are also included.
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

// GetTopCrossPatterns retrieves the highest-confidence cross-project patterns.
// Only patterns with confidence at or above minConfidence are returned, up to the specified limit.
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
			if err := json.Unmarshal([]byte(examplesStr), &p.Examples); err != nil {
				slog.Warn("failed to unmarshal cross pattern examples",
					slog.String("pattern_id", p.ID),
					slog.Any("error", err))
			}
		}
		patterns = append(patterns, &p)
	}
	return patterns, nil
}

// LinkPatternToProject creates or updates a relationship between a pattern and a project.
// If the link exists, the usage count is incremented; otherwise a new link is created.
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

// GetProjectsForPattern retrieves all projects that use a specific pattern.
// Results are ordered by usage count descending.
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

// RecordPatternFeedback records feedback when a pattern is applied during an execution.
// Based on the outcome ("success", "failure", or "neutral"), it adjusts the pattern's
// confidence score and updates project-level success/failure counts.
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
			UPDATE cross_patterns SET confidence = min(0.95, max(0.1, confidence + ?)) WHERE id = ?
		`, feedback.ConfidenceDelta, feedback.PatternID)
		_, _ = s.db.Exec(`
			UPDATE pattern_projects SET success_count = success_count + 1 WHERE pattern_id = ? AND project_path = ?
		`, feedback.PatternID, feedback.ProjectPath)
	case "failure":
		_, _ = s.db.Exec(`
			UPDATE cross_patterns SET confidence = max(0.1, min(0.95, confidence - ?)) WHERE id = ?
		`, feedback.ConfidenceDelta, feedback.PatternID)
		_, _ = s.db.Exec(`
			UPDATE pattern_projects SET failure_count = failure_count + 1 WHERE pattern_id = ? AND project_path = ?
		`, feedback.PatternID, feedback.ProjectPath)
	}

	return nil
}

// SearchCrossPatterns searches patterns by title, description, or context using substring matching.
// Results are ordered by confidence and occurrence count descending, up to the specified limit.
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

// DeleteCrossPattern deletes a cross-project pattern by ID.
// Related pattern_projects and pattern_feedback records are deleted via foreign key cascade.
func (s *Store) DeleteCrossPattern(id string) error {
	_, err := s.db.Exec(`DELETE FROM cross_patterns WHERE id = ?`, id)
	return err
}

// GetCrossPatternStats returns aggregate statistics about cross-project patterns
// including counts, average confidence, and breakdown by pattern type.
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

// CrossPatternStats holds aggregate statistics about cross-project patterns.
type CrossPatternStats struct {
	TotalPatterns    int
	Patterns         int
	AntiPatterns     int
	AvgConfidence    float64
	TotalOccurrences int
	ByType           map[string]int
	ProjectCount     int
}

// Session represents a dashboard session with token usage and task counts.
// Sessions are keyed by date (YYYY-MM-DD) for daily aggregation.
type Session struct {
	ID                string
	Date              string // YYYY-MM-DD format
	StartedAt         time.Time
	EndedAt           *time.Time
	TotalInputTokens  int
	TotalOutputTokens int
	TotalCostCents    int
	TasksCompleted    int
	TasksFailed       int
}

// GetOrCreateDailySession retrieves today's session or creates a new one.
// Sessions are keyed by date to aggregate daily metrics.
func (s *Store) GetOrCreateDailySession() (*Session, error) {
	today := time.Now().Format("2006-01-02")

	// Try to get existing session for today
	row := s.db.QueryRow(`
		SELECT id, date, started_at, ended_at, total_input_tokens, total_output_tokens,
		       total_cost_cents, tasks_completed, tasks_failed
		FROM sessions WHERE date = ?
	`, today)

	var session Session
	var endedAt sql.NullTime
	err := row.Scan(&session.ID, &session.Date, &session.StartedAt, &endedAt,
		&session.TotalInputTokens, &session.TotalOutputTokens,
		&session.TotalCostCents, &session.TasksCompleted, &session.TasksFailed)

	if err == sql.ErrNoRows {
		// Create new session for today
		session = Session{
			ID:        fmt.Sprintf("session-%s-%d", today, time.Now().UnixNano()),
			Date:      today,
			StartedAt: time.Now(),
		}
		_, err = s.db.Exec(`
			INSERT INTO sessions (id, date, started_at)
			VALUES (?, ?, ?)
		`, session.ID, session.Date, session.StartedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to create session: %w", err)
		}
		return &session, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}

	return &session, nil
}

// UpdateSessionTokens updates token counts for a session.
func (s *Store) UpdateSessionTokens(sessionID string, inputTokens, outputTokens int) error {
	_, err := s.db.Exec(`
		UPDATE sessions
		SET total_input_tokens = total_input_tokens + ?,
		    total_output_tokens = total_output_tokens + ?
		WHERE id = ?
	`, inputTokens, outputTokens, sessionID)
	return err
}

// UpdateSessionTaskCount updates task completion/failure counts.
func (s *Store) UpdateSessionTaskCount(sessionID string, completed, failed int) error {
	_, err := s.db.Exec(`
		UPDATE sessions
		SET tasks_completed = tasks_completed + ?,
		    tasks_failed = tasks_failed + ?
		WHERE id = ?
	`, completed, failed, sessionID)
	return err
}

// EndSession marks a session as ended.
func (s *Store) EndSession(sessionID string) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET ended_at = CURRENT_TIMESTAMP WHERE id = ?
	`, sessionID)
	return err
}
