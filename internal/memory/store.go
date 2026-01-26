package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		`CREATE INDEX IF NOT EXISTS idx_executions_task ON executions(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_executions_project ON executions(project_path)`,
		`CREATE INDEX IF NOT EXISTS idx_patterns_project ON patterns(project_path)`,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
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
}

// SaveExecution saves an execution record
func (s *Store) SaveExecution(exec *Execution) error {
	_, err := s.db.Exec(`
		INSERT INTO executions (id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, exec.ID, exec.TaskID, exec.ProjectPath, exec.Status, exec.Output, exec.Error, exec.DurationMs, exec.PRUrl, exec.CommitSHA, exec.CompletedAt)
	return err
}

// GetExecution retrieves an execution by ID
func (s *Store) GetExecution(id string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions WHERE id = ?
	`, id)

	var exec Execution
	var completedAt sql.NullTime
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt)
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
