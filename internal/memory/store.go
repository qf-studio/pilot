package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Store provides persistent storage for Pilot using SQLite.
// It manages executions, patterns, projects, and cross-project learning data.
// Store handles database migrations automatically on initialization.
type Store struct {
	db   *sql.DB
	path string

	logSubMu       sync.RWMutex
	logSubscribers map[chan *LogEntry]struct{}
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

	// Enable WAL mode, busy timeout, and foreign key enforcement.
	// Foreign keys default to OFF in SQLite; ON DELETE CASCADE on pattern_projects
	// and pattern_feedback only fires once this pragma is set.
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000; PRAGMA foreign_keys=ON;"); err != nil {
		return nil, fmt.Errorf("failed to set database pragmas: %w", err)
	}

	// SQLite supports only one writer at a time. Limiting to 1 open connection
	// serializes all database access, eliminating SQLITE_BUSY contention.
	// WAL mode still allows the single connection to interleave reads and writes efficiently.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0) // Don't close idle connections

	store := &Store{
		db:             db,
		path:           dataPath,
		logSubscribers: make(map[chan *LogEntry]struct{}),
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
		`ALTER TABLE executions ADD COLUMN task_source_adapter TEXT DEFAULT ''`,
		`ALTER TABLE executions ADD COLUMN task_source_issue_id TEXT DEFAULT ''`,
		// GH-2326: persist Task.Labels across queue round-trip so no-decompose survives dispatch
		`ALTER TABLE executions ADD COLUMN task_labels TEXT DEFAULT ''`,
		// GH-2807: effort and complexity columns for cost-by-tier observability
		`ALTER TABLE executions ADD COLUMN effort_level TEXT`,
		`ALTER TABLE executions ADD COLUMN complexity_level TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_executions_status ON executions(status)`,
		`CREATE INDEX IF NOT EXISTS idx_patterns_project ON patterns(project_path)`,
		// Cross-project pattern indexes
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_type ON cross_patterns(pattern_type)`,
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_scope ON cross_patterns(scope)`,
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_confidence ON cross_patterns(confidence DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_updated ON cross_patterns(updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_cross_patterns_title ON cross_patterns(title)`,
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
		// Autopilot metrics snapshots (GH-728)
		`CREATE TABLE IF NOT EXISTS autopilot_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			snapshot_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			issues_success INTEGER DEFAULT 0,
			issues_failed INTEGER DEFAULT 0,
			issues_rate_limited INTEGER DEFAULT 0,
			prs_merged INTEGER DEFAULT 0,
			prs_failed INTEGER DEFAULT 0,
			prs_conflicting INTEGER DEFAULT 0,
			circuit_breaker_trips INTEGER DEFAULT 0,
			api_errors_total INTEGER DEFAULT 0,
			api_error_rate REAL DEFAULT 0.0,
			queue_depth INTEGER DEFAULT 0,
			failed_queue_depth INTEGER DEFAULT 0,
			active_prs INTEGER DEFAULT 0,
			success_rate REAL DEFAULT 0.0,
			avg_ci_wait_ms INTEGER DEFAULT 0,
			avg_merge_time_ms INTEGER DEFAULT 0,
			avg_execution_ms INTEGER DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_autopilot_metrics_at ON autopilot_metrics(snapshot_at)`,
		// Brief history tracking (GH-1081)
		`CREATE TABLE IF NOT EXISTS brief_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sent_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			channel TEXT NOT NULL,
			brief_type TEXT NOT NULL DEFAULT 'daily',
			recipient TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_brief_history_sent_at ON brief_history(sent_at)`,
		`CREATE INDEX IF NOT EXISTS idx_brief_history_channel ON brief_history(channel)`,
		// Execution logs table (GH-1586)
		`CREATE TABLE IF NOT EXISTS execution_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			execution_id TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			level TEXT NOT NULL DEFAULT 'info',
			message TEXT NOT NULL,
			component TEXT DEFAULT 'executor'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_execution_logs_timestamp ON execution_logs(timestamp)`,
		`CREATE TABLE IF NOT EXISTS model_outcomes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_type TEXT NOT NULL,
			model TEXT NOT NULL,
			outcome TEXT NOT NULL,
			tokens_used INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_model_outcomes_task_model ON model_outcomes(task_type, model)`,
		`CREATE INDEX IF NOT EXISTS idx_model_outcomes_created ON model_outcomes(created_at)`,
		// Pattern performance tracking (GH-2020)
		`CREATE TABLE IF NOT EXISTS pattern_performance (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			pattern_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			task_type TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			success_count INTEGER DEFAULT 0,
			failure_count INTEGER DEFAULT 0,
			last_used DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(pattern_id, project_id, task_type)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pattern_performance_pattern ON pattern_performance(pattern_id)`,
		`CREATE INDEX IF NOT EXISTS idx_pattern_performance_project ON pattern_performance(project_id)`,
		// Eval tasks table (GH-2058)
		`CREATE TABLE IF NOT EXISTS eval_tasks (
			id TEXT PRIMARY KEY,
			execution_id TEXT NOT NULL,
			issue_number INTEGER NOT NULL,
			issue_title TEXT NOT NULL,
			repo TEXT NOT NULL,
			success BOOLEAN NOT NULL,
			pass_criteria TEXT,
			files_changed TEXT,
			duration_ms INTEGER DEFAULT 0,
			project_path TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repo, issue_number)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_tasks_repo ON eval_tasks(repo)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_tasks_success ON eval_tasks(success)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_tasks_created ON eval_tasks(created_at)`,
		// Eval results table (GH-2062) — stores per-run, per-model, per-task outcomes
		`CREATE TABLE IF NOT EXISTS eval_results (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			model TEXT NOT NULL,
			passed BOOLEAN NOT NULL,
			duration_ms INTEGER DEFAULT 0,
			tokens_used INTEGER DEFAULT 0,
			cost_usd REAL DEFAULT 0.0,
			error_msg TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_results_run ON eval_results(run_id)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_results_task ON eval_results(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_results_model ON eval_results(model)`,
		`CREATE INDEX IF NOT EXISTS idx_eval_results_created ON eval_results(created_at)`,
		// Pending approval requests awaiting human decision (GH-2657)
		`CREATE TABLE IF NOT EXISTS approval_pending (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			stage TEXT NOT NULL,
			title TEXT NOT NULL,
			description TEXT DEFAULT '',
			metadata TEXT DEFAULT '',
			approvers TEXT DEFAULT '',
			preferred_channel TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_approval_pending_expires ON approval_pending(expires_at)`,
		// Approval decision columns on executions (GH-2667)
		`ALTER TABLE executions ADD COLUMN approval_request_id TEXT DEFAULT ''`,
		`ALTER TABLE executions ADD COLUMN approval_decision TEXT DEFAULT ''`,
		`ALTER TABLE executions ADD COLUMN approval_decision_at DATETIME`,
		`ALTER TABLE executions ADD COLUMN approval_decision_by TEXT DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_executions_approval_request ON executions(approval_request_id)`,
		// Per-model token/cost/execution counters on autopilot_metrics (GH-2856)
		`ALTER TABLE autopilot_metrics ADD COLUMN tokens_consumed_json TEXT DEFAULT '{}'`,
		`ALTER TABLE autopilot_metrics ADD COLUMN execution_cost_usd_json TEXT DEFAULT '{}'`,
		`ALTER TABLE autopilot_metrics ADD COLUMN executions_by_result_json TEXT DEFAULT '{}'`,
		// GH-3028: RSS telemetry — peak and final resident set size for subprocess OOM diagnostics.
		`ALTER TABLE executions ADD COLUMN peak_rss_mb INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN final_rss_mb INTEGER DEFAULT 0`,
		// GH-3615: prompt-caching token counts
		`ALTER TABLE executions ADD COLUMN tokens_cache_read INTEGER DEFAULT 0`,
		`ALTER TABLE executions ADD COLUMN tokens_cache_write INTEGER DEFAULT 0`,
		// GH-3536: project scoping for eval tasks
		`ALTER TABLE eval_tasks ADD COLUMN project_path TEXT DEFAULT ''`,
		// GH-3844 (TASK-379 C3): stage-transition ledger, durable across autopilot's
		// practice of deleting successful PR state rows.
		`CREATE TABLE IF NOT EXISTS execution_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			execution_id TEXT NOT NULL,
			stage TEXT NOT NULL,
			occurred_at DATETIME NOT NULL,
			detail TEXT DEFAULT '',
			FOREIGN KEY (execution_id) REFERENCES executions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_execution_events_execution_id ON execution_events(execution_id)`,
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

	// TASK-358: correct historically-misclassified outcomes (declined/no-op/stalled
	// that were collapsed into status='failed' before the dispatcher classified them).
	if err := s.reclassifyLegacyOutcomes(); err != nil {
		return fmt.Errorf("reclassify legacy outcomes: %w", err)
	}

	return nil
}

// reclassifyLegacyOutcomes corrects executions that the dispatcher previously
// recorded as status='failed' when they were actually non-failure terminal
// outcomes — no-op (work already on base / no edits), rate-limited, skipped
// (never ran / cancelled), stalled/budget, or infra/plumbing (resource kill,
// push/PR/worktree/branch). Before TASK-358 every !Success result collapsed into
// "failed", inflating the dashboard's QUEUE "failed" count.
//
// Each UPDATE is guarded by status='failed' and the statements run in the same
// precedence order as TerminalStatus (no-op first, infra last) so a row matching
// more than one signature lands in the most "this isn't a failure" bucket.
// Classification uses the deterministic error signatures the runner writes, so it
// only touches rows it can positively identify; genuine failures (quality gates,
// planning, unknown exit-1) carry none of these signatures and are left as
// "failed". Idempotent: after the first pass no 'failed' row matches, so running
// on every startup is a cheap, indexed no-op. Declined rows cannot be recovered
// here because the decline reason was never persisted to executions.error.
//
// Keep the LIKE patterns in sync with the signature lists in executor/runner.go.
func (s *Store) reclassifyLegacyOutcomes() error {
	stmts := []string{
		`UPDATE executions SET status = 'no_op'
		 WHERE status = 'failed' AND (
			error LIKE '%no new commit produced%' OR
			error LIKE '%no commits relative to base%' OR
			error LIKE '%no_changes%' OR
			error LIKE '%made no code changes%'
		 )`,
		`UPDATE executions SET status = 'rate_limited'
		 WHERE status = 'failed' AND (
			error LIKE '%hit your limit%' OR
			error LIKE '%rate limit%' OR
			error LIKE '%usage limit%'
		 )`,
		`UPDATE executions SET status = 'skipped'
		 WHERE status = 'failed' AND (
			error LIKE '%stale queued task recovered%' OR
			error LIKE '%context canceled%' OR
			error LIKE '%context cancelled%'
		 )`,
		`UPDATE executions SET status = 'stalled'
		 WHERE status = 'failed' AND (
			error LIKE '%session stalled%' OR
			error LIKE '%budget limit exceeded%'
		 )`,
		`UPDATE executions SET status = 'infra'
		 WHERE status = 'failed' AND (
			error LIKE '%oom_killed%' OR
			error LIKE '%exit code 137%' OR
			error LIKE '%SIGKILL%' OR
			error LIKE '%signal: killed%' OR
			error LIKE '%push failed%' OR
			error LIKE '%PR creation failed%' OR
			error LIKE '%worktree creation failed%' OR
			error LIKE '%create/switch branch%' OR
			error LIKE '%branch switch failed%'
		 )`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// DB returns the underlying *sql.DB for sharing with other packages (e.g., teams store).
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close closes the database connection and releases resources.
func (s *Store) Close() error {
	return s.db.Close()
}

// withRetry executes a database operation with exponential backoff on transient errors.
// Retries up to 5 times with 100ms, 200ms, 400ms, 800ms, 1600ms delays.
func (s *Store) withRetry(operation string, fn func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		// Only retry on SQLITE_BUSY/SQLITE_LOCKED
		errStr := strings.ToLower(err.Error())
		if !strings.Contains(errStr, "database is locked") &&
			!strings.Contains(errStr, "sqlite_busy") &&
			!strings.Contains(errStr, "sqlite_locked") {
			return err // Non-retryable error
		}
		delay := time.Duration(100<<uint(attempt)) * time.Millisecond
		slog.Warn("Database locked, retrying",
			slog.String("operation", operation),
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay),
		)
		time.Sleep(delay)
	}
	return fmt.Errorf("%s failed after 5 retries: %w", operation, err)
}

// Execution represents a task execution record stored in the database.
// It captures the complete execution history including status, output, metrics, and PR information.
type Execution struct {
	ID          string
	TaskID      string
	ProjectPath string
	// UserID identifies the user/tenant that owns this execution.
	// Empty in single-tenant deployments; populated when multi-user mode is enabled.
	// Used as the pivot for `usage_events` aggregation (GH-2429).
	UserID      string
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
	TokensCacheRead  int64
	TokensCacheWrite int64
	EstimatedCostUSD float64
	FilesChanged     int
	LinesAdded       int
	LinesRemoved     int
	ModelName        string
	// GH-2807: effort and complexity for cost-by-tier observability
	EffortLevel     string `json:"effort_level,omitempty"`
	ComplexityLevel string `json:"complexity_level,omitempty"`
	// Task queue fields (GH-46) - store task details for deferred execution
	TaskTitle         string
	TaskDescription   string
	TaskBranch        string
	TaskBaseBranch    string
	TaskCreatePR      bool
	TaskVerbose       bool
	TaskSourceAdapter string // Source adapter (e.g., "github", "gitlab", "linear")
	TaskSourceIssueID string // Issue ID in the source adapter
	// GH-2326: persisted Task.Labels so label-driven gates (no-decompose, autopilot-fix, etc.)
	// survive the dispatcher queue → worker round-trip.
	TaskLabels []string
	// Approval decision fields (GH-2667)
	ApprovalRequestID  string
	ApprovalDecision   string
	ApprovalDecisionAt *time.Time
	ApprovalDecisionBy string
	// GH-3028: RSS telemetry
	PeakRSSMB  int
	FinalRSSMB int
}

// SaveExecution saves an execution record to the database.
// The execution ID must be unique; duplicate IDs will cause an error.
func (s *Store) SaveExecution(exec *Execution) error {
	labelsJSON, err := marshalLabels(exec.TaskLabels)
	if err != nil {
		return fmt.Errorf("failed to marshal task labels: %w", err)
	}
	return s.withRetry("SaveExecution", func() error {
		_, err := s.db.Exec(`
			INSERT INTO executions (id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, completed_at,
				tokens_input, tokens_output, tokens_total, tokens_cache_read, tokens_cache_write,
				estimated_cost_usd, files_changed, lines_added, lines_removed, model_name,
				task_title, task_description, task_branch, task_base_branch, task_create_pr, task_verbose,
				task_source_adapter, task_source_issue_id, task_labels,
				approval_request_id, effort_level, complexity_level)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, exec.ID, exec.TaskID, exec.ProjectPath, exec.Status, exec.Output, exec.Error, exec.DurationMs, exec.PRUrl, exec.CommitSHA, exec.CompletedAt,
			exec.TokensInput, exec.TokensOutput, exec.TokensTotal, exec.TokensCacheRead, exec.TokensCacheWrite,
			exec.EstimatedCostUSD, exec.FilesChanged, exec.LinesAdded, exec.LinesRemoved, exec.ModelName,
			exec.TaskTitle, exec.TaskDescription, exec.TaskBranch, exec.TaskBaseBranch, exec.TaskCreatePR, exec.TaskVerbose,
			exec.TaskSourceAdapter, exec.TaskSourceIssueID, labelsJSON,
			exec.ApprovalRequestID, exec.EffortLevel, exec.ComplexityLevel)
		return err
	})
}

// marshalLabels serializes labels to JSON; returns "" when the slice is empty
// so the DB column stays compatible with pre-migration rows and default "".
func marshalLabels(labels []string) (string, error) {
	if len(labels) == 0 {
		return "", nil
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalLabels parses JSON-encoded labels; empty/whitespace → nil slice.
func unmarshalLabels(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(s), &labels); err != nil {
		// Legacy / malformed rows: return nil rather than failing the read.
		return nil
	}
	return labels
}

// executionDetailColumns is the full column set for a single Execution row,
// shared by GetExecution and GetLatestExecutionByTaskID.
const executionDetailColumns = `
	id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
	COALESCE(tokens_input, 0), COALESCE(tokens_output, 0), COALESCE(tokens_total, 0),
	COALESCE(tokens_cache_read, 0), COALESCE(tokens_cache_write, 0),
	COALESCE(estimated_cost_usd, 0), COALESCE(files_changed, 0), COALESCE(lines_added, 0),
	COALESCE(lines_removed, 0), COALESCE(model_name, ''),
	COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
	COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0),
	COALESCE(task_source_adapter, ''), COALESCE(task_source_issue_id, ''),
	COALESCE(task_labels, ''),
	COALESCE(approval_request_id, ''), COALESCE(approval_decision, ''),
	approval_decision_at,
	COALESCE(approval_decision_by, ''),
	COALESCE(effort_level, ''), COALESCE(complexity_level, '')`

// rowScanner abstracts *sql.Row and *sql.Rows so scanExecutionDetail serves both
// a single QueryRow result and a Query loop (used by ListExecutionsForTask).
type rowScanner interface {
	Scan(dest ...any) error
}

// scanExecutionDetail scans a row selected via executionDetailColumns into an Execution.
func scanExecutionDetail(row rowScanner) (*Execution, error) {
	var exec Execution
	var completedAt sql.NullTime
	var approvalDecisionAt sql.NullTime
	var labelsJSON string
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
		&exec.TokensInput, &exec.TokensOutput, &exec.TokensTotal, &exec.TokensCacheRead, &exec.TokensCacheWrite,
		&exec.EstimatedCostUSD, &exec.FilesChanged, &exec.LinesAdded, &exec.LinesRemoved, &exec.ModelName,
		&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose,
		&exec.TaskSourceAdapter, &exec.TaskSourceIssueID, &labelsJSON,
		&exec.ApprovalRequestID, &exec.ApprovalDecision, &approvalDecisionAt, &exec.ApprovalDecisionBy,
		&exec.EffortLevel, &exec.ComplexityLevel)
	if err != nil {
		return nil, err
	}
	exec.TaskLabels = unmarshalLabels(labelsJSON)
	if approvalDecisionAt.Valid {
		exec.ApprovalDecisionAt = &approvalDecisionAt.Time
	}

	if completedAt.Valid {
		exec.CompletedAt = &completedAt.Time
	}

	return &exec, nil
}

// GetExecution retrieves an execution by its unique ID.
// Returns sql.ErrNoRows if the execution is not found.
func (s *Store) GetExecution(id string) (*Execution, error) {
	row := s.db.QueryRow(`SELECT `+executionDetailColumns+` FROM executions WHERE id = ?`, id)
	return scanExecutionDetail(row)
}

// GetLatestExecutionByTaskID returns the most recent execution for a task, matched by
// exact task_id first and falling back to a substring match (e.g. "GH-15" matching
// "GH-15"). Returns sql.ErrNoRows if no execution matches.
func (s *Store) GetLatestExecutionByTaskID(taskID string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT `+executionDetailColumns+`
		FROM executions
		WHERE task_id = ? OR task_id LIKE ?
		ORDER BY (task_id = ?) DESC, created_at DESC, rowid DESC
		LIMIT 1
	`, taskID, "%"+taskID+"%", taskID)
	return scanExecutionDetail(row)
}

// ListExecutionsForTask returns every execution recorded for taskID (exact match),
// newest first. Unlike GetLatestExecutionByTaskID (single row, exact-or-substring
// match), this returns the full history so the CLI can render a per-execution
// stage timeline across retries (TASK-379 C4).
func (s *Store) ListExecutionsForTask(taskID string) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT `+executionDetailColumns+`
		FROM executions
		WHERE task_id = ?
		ORDER BY created_at DESC, rowid DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		exec, err := scanExecutionDetail(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, exec)
	}
	return executions, rows.Err()
}

// Stage identifies a discrete milestone in an execution's lifecycle. Stage events
// accumulate in execution_events to build an append-only timeline for `pilot trace`
// (TASK-379 C3/C4) — unlike executions.status, a single mutable field, this
// timeline survives autopilot's practice of deleting successful PR state rows.
// Values match the enum in GH-3840.
type Stage string

const (
	StageQueued        Stage = "queued"
	StageSpecValidated Stage = "spec_validated"
	StageRunning       Stage = "running"
	StageClaudeStarted Stage = "claude_started"
	StageDecomposed    Stage = "decomposed"
	// StageImplementationStarted marks a direct (non-epic) task handing off to
	// Claude for real implementation work. GH-3938 wires claude_started/
	// decomposed/completed on the epic-parent path only; this value is
	// reserved for a future direct-path instrumentation pass so the enum
	// matches the full lifecycle described in GH-3840 up front.
	StageImplementationStarted Stage = "implementation_started"
	StageCommit                Stage = "commit"
	StagePRCreated             Stage = "pr_created"
	StageCIPassed              Stage = "ci_passed"
	StageCIFailed              Stage = "ci_failed"
	StageAwaitingApproval      Stage = "awaiting_approval"
	StageMerged                Stage = "merged"
	StageReleased              Stage = "released"
	StageCompleted             Stage = "completed"
	StageFailed                Stage = "failed"
	StageNoOp                  Stage = "no_op"
	StageSkipped               Stage = "skipped"
	StageStalled               Stage = "stalled"
)

// Event represents a single stage-transition record for an execution.
type Event struct {
	ID          int64
	ExecutionID string
	Stage       Stage
	OccurredAt  time.Time
	Detail      string
}

// InsertExecutionEvent records a stage transition for executionID. occurred_at is
// always the write-time UTC clock, not a caller-supplied value, so the ledger
// can't be back-dated or skewed by local timezone (TASK-379 C2/C3).
func (s *Store) InsertExecutionEvent(executionID string, stage Stage, detail string) error {
	return s.withRetry("InsertExecutionEvent", func() error {
		_, err := s.db.Exec(`
			INSERT INTO execution_events (execution_id, stage, occurred_at, detail)
			VALUES (?, ?, ?, ?)
		`, executionID, string(stage), time.Now().UTC(), detail)
		return err
	})
}

// ListExecutionEvents returns the stage timeline for executionID in chronological
// (occurred_at ASC) order. Returns an empty slice, not an error, for an unknown
// execution ID.
func (s *Store) ListExecutionEvents(executionID string) ([]*Event, error) {
	rows, err := s.db.Query(`
		SELECT id, execution_id, stage, occurred_at, COALESCE(detail, '')
		FROM execution_events
		WHERE execution_id = ?
		ORDER BY occurred_at ASC, id ASC
	`, executionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var events []*Event
	for rows.Next() {
		var e Event
		var stage string
		if err := rows.Scan(&e.ID, &e.ExecutionID, &stage, &e.OccurredAt, &e.Detail); err != nil {
			return nil, err
		}
		e.Stage = Stage(stage)
		events = append(events, &e)
	}
	return events, rows.Err()
}

// HasCompletedExecution checks whether a genuine completed execution exists for the given task
// and project. "Genuine" means: status=completed, no error, AND at least one deliverable
// (commit_sha or pr_url is set). This mirrors IsTaskShipped in the executor package.
//
// Rows excluded from the count:
//   - status != "completed" (still running/queued/failed)
//   - non-empty error field (orphan recovery, GH-2315)
//   - no commit_sha AND no pr_url (epic-parent rows that produced no real work, TASK-296)
//
// The cross-site invariant — HasCompletedExecution and IsTaskShipped always agree — is enforced
// by internal/integration/task_completion_invariant_test.go.
func (s *Store) HasCompletedExecution(taskID, projectPath string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM executions
		WHERE task_id = ? AND project_path = ? AND status = 'completed'
			AND (error IS NULL OR error = '')
			AND (commit_sha != '' OR pr_url != '')
	`, taskID, projectPath).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// InvalidateCompletion deletes genuine completed execution records for the given task and
// project, allowing re-dispatch. Targets only rows that HasCompletedExecution would count
// (status='completed', no error, at least one deliverable), leaving orphan-recovered rows
// and epic-parent no-deliverable rows untouched.
func (s *Store) InvalidateCompletion(taskID, projectPath string) error {
	_, err := s.db.Exec(`
		DELETE FROM executions
		WHERE task_id = ? AND project_path = ? AND status = 'completed'
			AND (error IS NULL OR error = '')
			AND (commit_sha != '' OR pr_url != '')
	`, taskID, projectPath)
	if err != nil {
		return fmt.Errorf("invalidate completion for %s at %s: %w", taskID, projectPath, err)
	}
	return nil
}

// ReclassifyCompletionAsFailed demotes genuine completed execution records (the
// same rows HasCompletedExecution would count: status='completed', no error, at
// least one deliverable) to status='failed' with reason recorded in the error
// column. GH-3818/D10: called by autopilot the moment it observes a PR closed
// without merging, so a "completed" row can never outlive the PR it was built
// on — without this, HasCompletedExecution keeps trusting the stale row forever
// and the poller re-marks the issue pilot-done on every subsequent poll even
// though the deliverable was discarded.
//
// projectPath follows SelfHealExecutionAfterMerge's scoping convention: empty
// drops the scope and matches by task_id alone (legacy single-repo callers).
// A later merge (human recovery PR, retried issue) heals the row back to
// "completed" via SelfHealExecutionAfterMerge, so this is not a one-way trip.
func (s *Store) ReclassifyCompletionAsFailed(taskID, projectPath, reason string) error {
	return s.withRetry("ReclassifyCompletionAsFailed", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = 'failed',
				error = ?,
				completed_at = CURRENT_TIMESTAMP
			WHERE task_id = ? AND (? = '' OR project_path = ?) AND status = 'completed'
				AND (error IS NULL OR error = '')
				AND (commit_sha != '' OR pr_url != '')
		`, reason, taskID, projectPath, projectPath)
		return err
	})
}

// SetApprovalDecision records an approval decision on the execution linked to requestID.
// It sets approval_decision, approval_decision_at, and approval_decision_by on the row
// whose approval_request_id matches. Returns sql.ErrNoRows if no matching row is found.
func (s *Store) SetApprovalDecision(ctx context.Context, requestID string, decision string, by string) error {
	if requestID == "" {
		return sql.ErrNoRows
	}
	return s.withRetry("SetApprovalDecision", func() error {
		result, err := s.db.ExecContext(ctx, `
			UPDATE executions
			SET approval_decision    = ?,
			    approval_decision_at = CURRENT_TIMESTAMP,
			    approval_decision_by = ?
			WHERE approval_request_id = ?
		`, decision, by, requestID)
		if err != nil {
			return fmt.Errorf("SetApprovalDecision: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("SetApprovalDecision rows affected: %w", err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// SetApprovalRequestID records the approval request ID on the most-recent execution
// row for the given task. Must be called after SubmitApprovalRequest succeeds so
// that SetApprovalDecision's WHERE clause can later match the row.
// Returns sql.ErrNoRows when no execution row exists for taskID yet.
func (s *Store) SetApprovalRequestID(ctx context.Context, taskID, requestID string) error {
	if taskID == "" || requestID == "" {
		return nil
	}
	return s.withRetry("SetApprovalRequestID", func() error {
		result, err := s.db.ExecContext(ctx, `
			UPDATE executions
			SET approval_request_id = ?
			WHERE id = (
				SELECT id FROM executions
				WHERE task_id = ?
				ORDER BY created_at DESC
				LIMIT 1
			)
		`, requestID, taskID)
		if err != nil {
			return fmt.Errorf("SetApprovalRequestID: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("SetApprovalRequestID rows affected: %w", err)
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// GetRecentExecutions returns the most recent executions ordered by creation time.
// The limit parameter specifies the maximum number of executions to return.
// If projectPath is non-empty, only executions for that project are returned.
func (s *Store) GetRecentExecutions(limit int, projectPath string) ([]*Execution, error) {
	const base = `
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
			COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0),
			COALESCE(peak_rss_mb, 0), COALESCE(final_rss_mb, 0)
		FROM executions`
	var rows *sql.Rows
	var err error
	if projectPath != "" {
		rows, err = s.db.Query(base+` WHERE project_path = ? ORDER BY created_at DESC LIMIT ?`, projectPath, limit)
	} else {
		rows, err = s.db.Query(base+` ORDER BY created_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
			&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose,
			&exec.PeakRSSMB, &exec.FinalRSSMB); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, rows.Err()
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
		return s.withRetry("SavePattern", func() error {
			result, err := s.db.Exec(`
				INSERT INTO patterns (project_path, pattern_type, content, confidence)
				VALUES (?, ?, ?, ?)
			`, pattern.ProjectPath, pattern.Type, pattern.Content, pattern.Confidence)
			if err != nil {
				return err
			}
			id, _ := result.LastInsertId()
			pattern.ID = id
			return nil
		})
	}
	return s.withRetry("SavePattern", func() error {
		_, err := s.db.Exec(`
			UPDATE patterns SET content = ?, confidence = ?, uses = uses + 1, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, pattern.Content, pattern.Confidence, pattern.ID)
		return err
	})
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

	return patterns, rows.Err()
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
	return s.withRetry("SaveProject", func() error {
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
	})
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

	return projects, rows.Err()
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
			SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at, COALESCE(task_title, '')
			FROM executions
			WHERE created_at >= ? AND created_at < ?
			AND project_path IN (`+placeholders+`)
			ORDER BY created_at DESC
		`, args...)
	} else {
		rows, err = s.db.Query(`
			SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at, COALESCE(task_title, '')
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
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt, &exec.TaskTitle); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, rows.Err()
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

	return executions, rows.Err()
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
			COALESCE(SUM(CASE WHEN pr_url != '' THEN 1 ELSE 0 END), 0) as prs_created,
			COALESCE(SUM(tokens_total), 0) as total_tokens,
			COALESCE(SUM(estimated_cost_usd), 0) as total_cost
		FROM executions
	`+whereClause, args...)

	if err := row.Scan(&result.TotalTasks, &result.CompletedCount, &result.FailedCount, &result.AvgDurationMs, &result.PRsCreated, &result.TotalTokensUsed, &result.EstimatedCostUSD); err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	if result.TotalTasks > 0 {
		result.SuccessRate = float64(result.CompletedCount) / float64(result.TotalTasks)
	}

	return &result, nil
}

// BriefMetricsData holds aggregate metrics calculated from execution data.
type BriefMetricsData struct {
	TotalTasks       int
	CompletedCount   int
	FailedCount      int
	SuccessRate      float64
	AvgDurationMs    int64
	PRsCreated       int
	TotalTokensUsed  int64
	EstimatedCostUSD float64
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

	return executions, rows.Err()
}

// GetQueuedTasksForProject returns queued/pending tasks for a specific project.
// Results are ordered by creation time ascending (oldest first) up to the specified limit.
// This is used by the per-project worker to get the next task to execute.
func (s *Store) GetQueuedTasksForProject(projectPath string, limit int) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
			COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0),
			COALESCE(task_source_adapter, ''), COALESCE(task_source_issue_id, ''),
			COALESCE(task_labels, '')
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
		var labelsJSON string
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt,
			&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose,
			&exec.TaskSourceAdapter, &exec.TaskSourceIssueID, &labelsJSON); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		exec.TaskLabels = unmarshalLabels(labelsJSON)
		executions = append(executions, &exec)
	}

	return executions, rows.Err()
}

// UpdateExecutionStatus updates the status of an execution record.
// Optionally sets the error message if provided. Also sets completed_at for terminal states.
func (s *Store) UpdateExecutionStatus(id, status string, errorMsg ...string) error {
	var errStr *string
	if len(errorMsg) > 0 && errorMsg[0] != "" {
		errStr = &errorMsg[0]
	}

	// Set completed_at for terminal states
	if status == "completed" || status == "failed" || status == "cancelled" || status == "declined" || status == "stalled" || status == "no_op" || status == "rate_limited" || status == "infra" || status == "skipped" {
		return s.withRetry("UpdateExecutionStatus", func() error {
			_, err := s.db.Exec(`
				UPDATE executions
				SET status = ?, error = COALESCE(?, error), completed_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, status, errStr, id)
			return err
		})
	}

	return s.withRetry("UpdateExecutionStatus", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = ?, error = COALESCE(?, error)
			WHERE id = ?
		`, status, errStr, id)
		return err
	})
}

// UpdateExecutionStatusByTaskID updates the status of the most recent execution
// for a given task ID and project path. Used by autopilot to mark failed
// executions as completed when the PR is merged externally.
// The projectPath scope prevents cross-project clobbering when the same task ID
// appears in multiple repos.
//
// TASK-358: the source scope is the non-success set ('failed', 'no_op', 'stalled')
// rather than 'failed' alone, so an execution the dispatcher now classifies as a
// no-op/stalled outcome still heals to the merged status when its PR lands.
func (s *Store) UpdateExecutionStatusByTaskID(taskID, projectPath, status string) error {
	return s.withRetry("UpdateExecutionStatusByTaskID", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = ?, completed_at = CURRENT_TIMESTAMP
			WHERE task_id = ? AND project_path = ? AND status IN ('failed', 'no_op', 'stalled', 'rate_limited', 'infra', 'skipped')
		`, status, taskID, projectPath)
		return err
	})
}

// SelfHealExecutionAfterMerge promotes any non-success row ("failed", "no_op",
// "stalled" — TASK-358) for the given task ID (scoped to projectPath) to
// "completed" and stamps the PR URL so the dashboard reflects the merged outcome.
// Used when autopilot observes a merge for an issue whose previous execution row
// was recorded as a non-success (e.g. user-pushed commits, sub-issue shipped via
// parent epic, or a phantom no-op whose work was already on base). GH-2402.
//
// projectPath MUST be the same value the executor stored in executions.project_path
// — an absolute filesystem path (e.g. /Users/me/proj), NOT an owner/repo slug. The
// scope prevents cross-project clobbering when the same task ID (GH-N is only unique
// per repo) appears in multiple repos. When projectPath is empty the scope is
// dropped and rows match by task_id alone (legacy single-repo behavior); this also
// guards against a caller passing the wrong discriminator silently healing nothing.
// TASK-352.
//
// GH-3818: also clears the error column. Without this, a row healed from
// "failed" (which always carries a non-empty error — every writer of that
// status passes a message) stayed invisible to HasCompletedExecution, which
// excludes rows with a non-empty error even once status flips back to "completed".
func (s *Store) SelfHealExecutionAfterMerge(taskID, projectPath, prURL string) error {
	return s.withRetry("SelfHealExecutionAfterMerge", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = 'completed',
				error = '',
				completed_at = CURRENT_TIMESTAMP,
				pr_url = CASE WHEN ? <> '' THEN ? ELSE pr_url END
			WHERE task_id = ? AND status IN ('failed', 'no_op', 'stalled', 'rate_limited', 'infra', 'skipped') AND (? = '' OR project_path = ?)
		`, prURL, prURL, taskID, projectPath, projectPath)
		return err
	})
}

// GetExecutionStatusByTaskID returns the status of the most recent execution row
// exactly matching taskID (scoped to projectPath, mirroring SelfHealExecutionAfterMerge:
// empty projectPath drops the scope for legacy single-repo callers) — no substring
// fallback, unlike GetLatestExecutionByTaskID, so a no_op verdict can't be borrowed
// from an unrelated task or repo. Returns sql.ErrNoRows when no row matches. GH-3780.
func (s *Store) GetExecutionStatusByTaskID(taskID, projectPath string) (string, error) {
	var status string
	err := s.db.QueryRow(`
		SELECT status FROM executions
		WHERE task_id = ? AND (? = '' OR project_path = ?)
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1
	`, taskID, projectPath, projectPath).Scan(&status)
	return status, err
}

// UpdateExecutionResult updates the result fields of an execution record.
// Called when task execution completes successfully with PR/commit info.
func (s *Store) UpdateExecutionResult(id string, prURL, commitSHA string, durationMs int64) error {
	return s.withRetry("UpdateExecutionResult", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET pr_url = ?, commit_sha = ?, duration_ms = ?
			WHERE id = ?
		`, prURL, commitSHA, durationMs, id)
		return err
	})
}

// MarkExecutionCompleted atomically marks an execution completed with its result
// fields in a single UPDATE. TASK-359 Layer 1: replaces the prior two-call
// sequence (UpdateExecutionStatus("completed") then UpdateExecutionResult) whose
// non-atomic gap could leave a 'completed' row with an empty pr_url if the
// process died between the writes — a row HasCompletedExecution then accepted via
// its OR-clause, stranding the issue. A single SQLite UPDATE is atomic.
func (s *Store) MarkExecutionCompleted(id, prURL, commitSHA string, durationMs int64) error {
	return s.withRetry("MarkExecutionCompleted", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = 'completed',
				pr_url = ?,
				commit_sha = ?,
				duration_ms = ?,
				completed_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, prURL, commitSHA, durationMs, id)
		return err
	})
}

// UpdateExecutionEffort records the resolved effort and complexity levels for a completed execution.
// Called after execution finishes so cost-by-tier queries can group rows by tier.
func (s *Store) UpdateExecutionEffort(id, effortLevel, complexityLevel string) error {
	return s.withRetry("UpdateExecutionEffort", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET effort_level = ?, complexity_level = ?
			WHERE id = ?
		`, effortLevel, complexityLevel, id)
		return err
	})
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

	return executions, rows.Err()
}

// GetStaleQueuedExecutions returns executions that have been in "queued" status
// for longer than the specified duration. Used to detect stuck queue entries.
func (s *Store) GetStaleQueuedExecutions(staleDuration time.Duration) ([]*Execution, error) {
	staleTime := time.Now().Add(-staleDuration)
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions
		WHERE status = 'queued' AND created_at < ?
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

	return executions, rows.Err()
}

// GetQueuedProjectPaths returns the distinct project paths that currently
// have at least one queued or pending execution. Used by the dispatcher at
// startup to re-adopt tasks left behind when the in-memory workers map is
// lost on restart — the rows themselves survive in SQLite. GH-3732.
func (s *Store) GetQueuedProjectPaths() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT project_path
		FROM executions
		WHERE status = 'queued' OR status = 'pending'
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

// DeleteExecution removes an execution row by ID. Used to clean up orphan rows
// when the same task already has a completed execution.
func (s *Store) DeleteExecution(id string) error {
	_, err := s.db.Exec("DELETE FROM executions WHERE id = ?", id)
	return err
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

	return s.withRetry("SaveCrossPattern", func() error {
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
	})
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
	return patterns, rows.Err()
}

// LinkPatternToProject creates or updates a relationship between a pattern and a project.
// If the link exists, the usage count is incremented; otherwise a new link is created.
func (s *Store) LinkPatternToProject(patternID, projectPath string) error {
	return s.withRetry("LinkPatternToProject", func() error {
		_, err := s.db.Exec(`
			INSERT INTO pattern_projects (pattern_id, project_path, uses, last_used)
			VALUES (?, ?, 1, CURRENT_TIMESTAMP)
			ON CONFLICT(pattern_id, project_path) DO UPDATE SET
				uses = pattern_projects.uses + 1,
				last_used = CURRENT_TIMESTAMP
		`, patternID, projectPath)
		return err
	})
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
	return links, rows.Err()
}

// RecordPatternFeedback records feedback when a pattern is applied during an execution.
// Based on the outcome ("success", "failure", or "neutral"), it adjusts the pattern's
// confidence score and updates project-level success/failure counts.
// All three writes (insert feedback, update confidence, update project link) run
// in a single transaction so a partial failure cannot leave the tables inconsistent.
func (s *Store) RecordPatternFeedback(feedback *PatternFeedback) error {
	return s.withRetry("RecordPatternFeedback", func() error {
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		result, err := tx.Exec(`
			INSERT INTO pattern_feedback (pattern_id, execution_id, project_path, outcome, confidence_delta)
			VALUES (?, ?, ?, ?, ?)
		`, feedback.PatternID, feedback.ExecutionID, feedback.ProjectPath, feedback.Outcome, feedback.ConfidenceDelta)
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		feedback.ID = id

		switch feedback.Outcome {
		case "success":
			if _, err := tx.Exec(`
				UPDATE cross_patterns SET confidence = min(0.95, max(0.1, confidence + ?)) WHERE id = ?
			`, feedback.ConfidenceDelta, feedback.PatternID); err != nil {
				return err
			}
			if _, err := tx.Exec(`
				UPDATE pattern_projects SET success_count = success_count + 1 WHERE pattern_id = ? AND project_path = ?
			`, feedback.PatternID, feedback.ProjectPath); err != nil {
				return err
			}
		case "failure":
			if _, err := tx.Exec(`
				UPDATE cross_patterns SET confidence = max(0.1, min(0.95, confidence - ?)) WHERE id = ?
			`, feedback.ConfidenceDelta, feedback.PatternID); err != nil {
				return err
			}
			if _, err := tx.Exec(`
				UPDATE pattern_projects SET failure_count = failure_count + 1 WHERE pattern_id = ? AND project_path = ?
			`, feedback.PatternID, feedback.ProjectPath); err != nil {
				return err
			}
		}

		return tx.Commit()
	})
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
	return s.withRetry("DeleteCrossPattern", func() error {
		_, err := s.db.Exec(`DELETE FROM cross_patterns WHERE id = ?`, id)
		return err
	})
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
	if err := rows.Err(); err != nil {
		return nil, err
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
		err = s.withRetry("GetOrCreateDailySession", func() error {
			_, err := s.db.Exec(`
				INSERT INTO sessions (id, date, started_at)
				VALUES (?, ?, ?)
			`, session.ID, session.Date, session.StartedAt)
			return err
		})
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
	return s.withRetry("UpdateSessionTokens", func() error {
		_, err := s.db.Exec(`
			UPDATE sessions
			SET total_input_tokens = total_input_tokens + ?,
			    total_output_tokens = total_output_tokens + ?
			WHERE id = ?
		`, inputTokens, outputTokens, sessionID)
		return err
	})
}

// UpdateSessionTaskCount updates task completion/failure counts.
func (s *Store) UpdateSessionTaskCount(sessionID string, completed, failed int) error {
	return s.withRetry("UpdateSessionTaskCount", func() error {
		_, err := s.db.Exec(`
			UPDATE sessions
			SET tasks_completed = tasks_completed + ?,
			    tasks_failed = tasks_failed + ?
			WHERE id = ?
		`, completed, failed, sessionID)
		return err
	})
}

// LifetimeTokens holds cumulative token and cost totals from all executions.
type LifetimeTokens struct {
	InputTokens      int64
	OutputTokens     int64
	TotalTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalCostUSD     float64
}

// GetLifetimeTokens returns cumulative token usage and cost across all executions.
// Unlike session-scoped data, this survives restarts by querying the executions table directly.
// Rows with zero tokens (dispatcher queue rows, early-failure rows) are excluded so they
// don't dilute per-task averages.
// If projectPath is non-empty, only executions for that project are counted.
func (s *Store) GetLifetimeTokens(projectPath string) (*LifetimeTokens, error) {
	q := `
		SELECT
			COALESCE(SUM(tokens_input), 0),
			COALESCE(SUM(tokens_output), 0),
			COALESCE(SUM(tokens_total), 0),
			COALESCE(SUM(tokens_cache_read), 0),
			COALESCE(SUM(tokens_cache_write), 0),
			COALESCE(SUM(estimated_cost_usd), 0)
		FROM executions
		WHERE tokens_total > 0`
	var row *sql.Row
	if projectPath != "" {
		row = s.db.QueryRow(q+` AND project_path = ?`, projectPath)
	} else {
		row = s.db.QueryRow(q)
	}

	var lt LifetimeTokens
	if err := row.Scan(&lt.InputTokens, &lt.OutputTokens, &lt.TotalTokens,
		&lt.CacheReadTokens, &lt.CacheWriteTokens, &lt.TotalCostUSD); err != nil {
		return nil, fmt.Errorf("failed to get lifetime tokens: %w", err)
	}
	return &lt, nil
}

// LifetimeTaskCounts holds cumulative outcome counts from all executions.
// TASK-358: Failed counts genuine task failures only; non-failure terminal
// outcomes (no-op, stalled, declined, rate-limited, infra, skipped) are broken
// out separately so the dashboard does not inflate the failed count.
type LifetimeTaskCounts struct {
	Total       int
	Succeeded   int
	Failed      int
	Declined    int
	NoOp        int
	Stalled     int
	RateLimited int
	Infra       int
	Skipped     int
}

// NonFailure returns the total of all non-failure terminal outcomes (everything
// that is neither succeeded nor a genuine failure). TASK-358.
func (c LifetimeTaskCounts) NonFailure() int {
	return c.NoOp + c.Stalled + c.Declined + c.RateLimited + c.Infra + c.Skipped
}

// GetLifetimeTaskCounts returns cumulative task counts across all executions.
// Parallels GetLifetimeTokens — survives restarts by querying executions table directly.
// If projectPath is non-empty, only executions for that project are counted.
func (s *Store) GetLifetimeTaskCounts(projectPath string) (*LifetimeTaskCounts, error) {
	const cols = `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'declined' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'no_op' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'stalled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'rate_limited' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'infra' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'skipped' THEN 1 ELSE 0 END), 0)
		FROM executions`
	var row *sql.Row
	if projectPath != "" {
		row = s.db.QueryRow(cols+` WHERE project_path = ?`, projectPath)
	} else {
		row = s.db.QueryRow(cols)
	}

	var tc LifetimeTaskCounts
	if err := row.Scan(&tc.Total, &tc.Succeeded, &tc.Failed, &tc.Declined, &tc.NoOp, &tc.Stalled,
		&tc.RateLimited, &tc.Infra, &tc.Skipped); err != nil {
		return nil, fmt.Errorf("failed to get lifetime task counts: %w", err)
	}
	return &tc, nil
}

// ModelDirectionKey identifies a token bucket by model and direction, mirroring
// autopilot's internal tokenKey so lifetime baselines line up with the
// in-memory Prometheus counter they hydrate (GH-4041).
type ModelDirectionKey struct {
	Model     string
	Direction string
}

// ModelResultKey identifies an execution bucket by model and outcome, mirroring
// autopilot's internal execKey (GH-4041).
type ModelResultKey struct {
	Model  string
	Result string
}

// LifetimeCounterBaselines holds per-label lifetime totals aggregated from the
// executions table, used to restore Prometheus counter baselines on daemon
// startup so external dashboards match the store's lifetime totals across
// restarts instead of resetting to zero (GH-4041). Read-only — computed from
// existing columns, no schema changes.
type LifetimeCounterBaselines struct {
	TokensByModelDirection  map[ModelDirectionKey]int64
	CostByModel             map[string]float64
	ExecutionsByModelResult map[ModelResultKey]int64
}

// GetLifetimeCounterBaselines aggregates lifetime token, cost, and execution
// totals from the executions table, broken down the same way the live
// Prometheus counters are keyed (model+direction for tokens, model for cost,
// model+result for executions). GH-4041.
//
// The execution "result" label collapses the executions table's richer status
// vocabulary (completed/failed/declined/no_op/stalled/rate_limited/infra/
// skipped) into the three values RecordExecution ever actually receives live
// (runner.go TerminalStatus / outcomeLabel): "success" for completed,
// "stalled" for stalled, everything else terminal folds into "failed" — so a
// restart does not introduce label values the live path never produces.
func (s *Store) GetLifetimeCounterBaselines() (*LifetimeCounterBaselines, error) {
	baselines := &LifetimeCounterBaselines{
		TokensByModelDirection:  make(map[ModelDirectionKey]int64),
		CostByModel:             make(map[string]float64),
		ExecutionsByModelResult: make(map[ModelResultKey]int64),
	}

	// Rows with zero tokens (dispatcher queue rows, early-failure rows) are
	// excluded so they don't dilute the baseline, mirroring GetLifetimeTokens.
	tokRows, err := s.db.Query(`
		SELECT
			COALESCE(NULLIF(model_name, ''), 'unknown'),
			COALESCE(SUM(tokens_input), 0),
			COALESCE(SUM(tokens_output), 0),
			COALESCE(SUM(tokens_cache_write), 0),
			COALESCE(SUM(tokens_cache_read), 0),
			COALESCE(SUM(estimated_cost_usd), 0)
		FROM executions
		WHERE tokens_total > 0
		GROUP BY model_name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get lifetime token/cost baselines: %w", err)
	}
	defer func() { _ = tokRows.Close() }()

	for tokRows.Next() {
		var model string
		var input, output, cacheWrite, cacheRead int64
		var cost float64
		if err := tokRows.Scan(&model, &input, &output, &cacheWrite, &cacheRead, &cost); err != nil {
			return nil, fmt.Errorf("failed to scan lifetime token/cost baseline row: %w", err)
		}
		if input > 0 {
			baselines.TokensByModelDirection[ModelDirectionKey{Model: model, Direction: "input"}] = input
		}
		if output > 0 {
			baselines.TokensByModelDirection[ModelDirectionKey{Model: model, Direction: "output"}] = output
		}
		if cacheWrite > 0 {
			baselines.TokensByModelDirection[ModelDirectionKey{Model: model, Direction: "cache_creation"}] = cacheWrite
		}
		if cacheRead > 0 {
			baselines.TokensByModelDirection[ModelDirectionKey{Model: model, Direction: "cache_read"}] = cacheRead
		}
		if cost != 0 {
			baselines.CostByModel[model] += cost
		}
	}
	if err := tokRows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lifetime token/cost baselines: %w", err)
	}

	execRows, err := s.db.Query(`
		SELECT
			COALESCE(NULLIF(model_name, ''), 'unknown'),
			CASE
				WHEN status = 'completed' THEN 'success'
				WHEN status = 'stalled' THEN 'stalled'
				ELSE 'failed'
			END AS result,
			COUNT(*)
		FROM executions
		WHERE status NOT IN ('queued', 'pending', 'running')
		GROUP BY model_name, result
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get lifetime execution baselines: %w", err)
	}
	defer func() { _ = execRows.Close() }()

	for execRows.Next() {
		var model, result string
		var count int64
		if err := execRows.Scan(&model, &result, &count); err != nil {
			return nil, fmt.Errorf("failed to scan lifetime execution baseline row: %w", err)
		}
		baselines.ExecutionsByModelResult[ModelResultKey{Model: model, Result: result}] += count
	}
	if err := execRows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lifetime execution baselines: %w", err)
	}

	return baselines, nil
}

// EndSession marks a session as ended.
func (s *Store) EndSession(sessionID string) error {
	return s.withRetry("EndSession", func() error {
		_, err := s.db.Exec(`
			UPDATE sessions SET ended_at = CURRENT_TIMESTAMP WHERE id = ?
		`, sessionID)
		return err
	})
}

// AutopilotMetricsRow represents a persisted autopilot metrics snapshot.
type AutopilotMetricsRow struct {
	ID                  int64
	SnapshotAt          time.Time
	IssuesSuccess       int
	IssuesFailed        int
	IssuesRateLimited   int
	PRsMerged           int
	PRsFailed           int
	PRsConflicting      int
	CircuitBreakerTrips int
	APIErrorsTotal      int
	APIErrorRate        float64
	QueueDepth          int
	FailedQueueDepth    int
	ActivePRs           int
	SuccessRate         float64
	AvgCIWaitMs         int64
	AvgMergeTimeMs      int64
	AvgExecutionMs      int64
	// Per-model/direction counters added in GH-2856. Keys use "model|direction"
	// (TokensConsumed, ExecutionsByResult) or plain model string (ExecutionCostUSD).
	TokensConsumed     map[string]int64   // "model|direction" → token count
	ExecutionCostUSD   map[string]float64 // model → cumulative USD cost
	ExecutionsByResult map[string]int64   // "model|result" → execution count
}

// SaveAutopilotMetrics persists an autopilot metrics snapshot to SQLite.
func (s *Store) SaveAutopilotMetrics(row *AutopilotMetricsRow) error {
	tokensJSON := marshalMapJSON(row.TokensConsumed)
	costJSON := marshalMapJSON(row.ExecutionCostUSD)
	execsJSON := marshalMapJSON(row.ExecutionsByResult)

	return s.withRetry("SaveAutopilotMetrics", func() error {
		_, err := s.db.Exec(`
			INSERT INTO autopilot_metrics (
				snapshot_at, issues_success, issues_failed, issues_rate_limited,
				prs_merged, prs_failed, prs_conflicting, circuit_breaker_trips,
				api_errors_total, api_error_rate, queue_depth, failed_queue_depth,
				active_prs, success_rate, avg_ci_wait_ms, avg_merge_time_ms, avg_execution_ms,
				tokens_consumed_json, execution_cost_usd_json, executions_by_result_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			row.SnapshotAt,
			row.IssuesSuccess, row.IssuesFailed, row.IssuesRateLimited,
			row.PRsMerged, row.PRsFailed, row.PRsConflicting,
			row.CircuitBreakerTrips, row.APIErrorsTotal, row.APIErrorRate,
			row.QueueDepth, row.FailedQueueDepth, row.ActivePRs,
			row.SuccessRate, row.AvgCIWaitMs, row.AvgMergeTimeMs, row.AvgExecutionMs,
			tokensJSON, costJSON, execsJSON,
		)
		return err
	})
}

// GetRecentAutopilotMetrics returns the most recent metrics snapshots.
func (s *Store) GetRecentAutopilotMetrics(limit int) ([]*AutopilotMetricsRow, error) {
	rows, err := s.db.Query(`
		SELECT id, snapshot_at, issues_success, issues_failed, issues_rate_limited,
			prs_merged, prs_failed, prs_conflicting, circuit_breaker_trips,
			api_errors_total, api_error_rate, queue_depth, failed_queue_depth,
			active_prs, success_rate, avg_ci_wait_ms, avg_merge_time_ms, avg_execution_ms,
			tokens_consumed_json, execution_cost_usd_json, executions_by_result_json
		FROM autopilot_metrics
		ORDER BY snapshot_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query autopilot metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var result []*AutopilotMetricsRow
	for rows.Next() {
		r := &AutopilotMetricsRow{}
		var tokensJSON, costJSON, execsJSON sql.NullString
		if err := rows.Scan(
			&r.ID, &r.SnapshotAt, &r.IssuesSuccess, &r.IssuesFailed, &r.IssuesRateLimited,
			&r.PRsMerged, &r.PRsFailed, &r.PRsConflicting, &r.CircuitBreakerTrips,
			&r.APIErrorsTotal, &r.APIErrorRate, &r.QueueDepth, &r.FailedQueueDepth,
			&r.ActivePRs, &r.SuccessRate, &r.AvgCIWaitMs, &r.AvgMergeTimeMs, &r.AvgExecutionMs,
			&tokensJSON, &costJSON, &execsJSON,
		); err != nil {
			return nil, fmt.Errorf("failed to scan autopilot metrics: %w", err)
		}
		r.TokensConsumed = unmarshalStringIntMap(tokensJSON.String)
		r.ExecutionCostUSD = unmarshalStringFloatMap(costJSON.String)
		r.ExecutionsByResult = unmarshalStringIntMap(execsJSON.String)
		result = append(result, r)
	}
	return result, rows.Err()
}

// LatestAutopilotMetrics returns the most recent persisted snapshot, or (nil, nil) if none.
func (s *Store) LatestAutopilotMetrics() (*AutopilotMetricsRow, error) {
	row := s.db.QueryRow(`
		SELECT id, snapshot_at, issues_success, issues_failed, issues_rate_limited,
			prs_merged, prs_failed, prs_conflicting, circuit_breaker_trips,
			api_errors_total, api_error_rate, queue_depth, failed_queue_depth,
			active_prs, success_rate, avg_ci_wait_ms, avg_merge_time_ms, avg_execution_ms,
			tokens_consumed_json, execution_cost_usd_json, executions_by_result_json
		FROM autopilot_metrics
		ORDER BY snapshot_at DESC
		LIMIT 1
	`)
	r := &AutopilotMetricsRow{}
	var tokensJSON, costJSON, execsJSON sql.NullString
	err := row.Scan(
		&r.ID, &r.SnapshotAt, &r.IssuesSuccess, &r.IssuesFailed, &r.IssuesRateLimited,
		&r.PRsMerged, &r.PRsFailed, &r.PRsConflicting, &r.CircuitBreakerTrips,
		&r.APIErrorsTotal, &r.APIErrorRate, &r.QueueDepth, &r.FailedQueueDepth,
		&r.ActivePRs, &r.SuccessRate, &r.AvgCIWaitMs, &r.AvgMergeTimeMs, &r.AvgExecutionMs,
		&tokensJSON, &costJSON, &execsJSON,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan latest autopilot metrics: %w", err)
	}
	r.TokensConsumed = unmarshalStringIntMap(tokensJSON.String)
	r.ExecutionCostUSD = unmarshalStringFloatMap(costJSON.String)
	r.ExecutionsByResult = unmarshalStringIntMap(execsJSON.String)
	return r, nil
}

// PruneExecutionLogs deletes execution log entries older than the given duration.
// Returns the number of rows deleted. Runs a WAL checkpoint after a large
// prune (>1000 rows) to reclaim disk space promptly.
func (s *Store) PruneExecutionLogs(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	var result sql.Result
	err := s.withRetry("PruneExecutionLogs", func() error {
		var execErr error
		result, execErr = s.db.Exec(`DELETE FROM execution_logs WHERE timestamp < ?`, cutoff)
		return execErr
	})
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 1000 {
		_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	return n, nil
}

// PruneAutopilotMetrics deletes snapshots older than the given duration.
func (s *Store) PruneAutopilotMetrics(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	var result sql.Result
	err := s.withRetry("PruneAutopilotMetrics", func() error {
		var execErr error
		result, execErr = s.db.Exec(`DELETE FROM autopilot_metrics WHERE snapshot_at < ?`, cutoff)
		return execErr
	})
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// marshalMapJSON serializes any JSON-serializable value to a string.
// Returns "{}" on nil input, nil map, or marshal error (safe default for DB storage).
func marshalMapJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "{}"
	}
	return string(b)
}

// unmarshalStringIntMap deserializes a JSON string into map[string]int64.
// Returns an empty map on empty or invalid JSON.
func unmarshalStringIntMap(s string) map[string]int64 {
	m := make(map[string]int64)
	if s == "" || s == "{}" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// unmarshalStringFloatMap deserializes a JSON string into map[string]float64.
// Returns an empty map on empty or invalid JSON.
func unmarshalStringFloatMap(s string) map[string]float64 {
	m := make(map[string]float64)
	if s == "" || s == "{}" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// BriefRecord represents a record of a brief that was sent.
type BriefRecord struct {
	ID        int64
	SentAt    time.Time
	Channel   string // e.g., "telegram", "slack", "email"
	BriefType string // e.g., "daily", "weekly"
	Recipient string // optional recipient identifier
}

// RecordBriefSent records that a brief was sent to a channel.
func (s *Store) RecordBriefSent(record *BriefRecord) error {
	return s.withRetry("RecordBriefSent", func() error {
		result, err := s.db.Exec(`
			INSERT INTO brief_history (sent_at, channel, brief_type, recipient)
			VALUES (?, ?, ?, ?)
		`, record.SentAt, record.Channel, record.BriefType, record.Recipient)
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		record.ID = id
		return nil
	})
}

// LogEntry represents a structured execution log entry.
type LogEntry struct {
	ID          int64     `json:"id"`
	ExecutionID string    `json:"executionId,omitempty"`
	Timestamp   time.Time `json:"ts"`
	Level       string    `json:"level"`
	Message     string    `json:"message"`
	Component   string    `json:"component"`
}

// SaveLogEntry persists an execution log entry and notifies all subscribers.
func (s *Store) SaveLogEntry(entry *LogEntry) error {
	err := s.withRetry("SaveLogEntry", func() error {
		result, err := s.db.Exec(`
			INSERT INTO execution_logs (execution_id, timestamp, level, message, component)
			VALUES (?, ?, ?, ?, ?)
		`, entry.ExecutionID, entry.Timestamp, entry.Level, entry.Message, entry.Component)
		if err != nil {
			return err
		}
		id, _ := result.LastInsertId()
		entry.ID = id
		return nil
	})
	if err != nil {
		return err
	}

	// Fan out to subscribers (non-blocking)
	s.logSubMu.RLock()
	for ch := range s.logSubscribers {
		select {
		case ch <- entry:
		default:
			// Slow consumer, drop entry
		}
	}
	s.logSubMu.RUnlock()

	return nil
}

// SubscribeLogs returns a channel that receives new log entries as they are saved.
// The channel is buffered to avoid blocking the writer. Call UnsubscribeLogs to clean up.
func (s *Store) SubscribeLogs() chan *LogEntry {
	ch := make(chan *LogEntry, 64)
	s.logSubMu.Lock()
	s.logSubscribers[ch] = struct{}{}
	s.logSubMu.Unlock()
	return ch
}

// UnsubscribeLogs removes a subscriber channel and closes it.
func (s *Store) UnsubscribeLogs(ch chan *LogEntry) {
	s.logSubMu.Lock()
	delete(s.logSubscribers, ch)
	s.logSubMu.Unlock()
	close(ch)
}

// GetRecentLogs returns the most recent log entries ordered by timestamp descending.
func (s *Store) GetRecentLogs(limit int) ([]*LogEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, COALESCE(execution_id, ''), timestamp, level, message, COALESCE(component, 'executor')
		FROM execution_logs
		ORDER BY timestamp DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []*LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.ExecutionID, &e.Timestamp, &e.Level, &e.Message, &e.Component); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, rows.Err()
}

// GetLogsByExecutionID returns log entries for a specific task ID (execution_logs.execution_id
// stores the task ID, e.g. "GH-3714", not the execution row's UUID), in chronological order.
// At most limit entries are returned, keeping the most recent ones if the task has more.
func (s *Store) GetLogsByExecutionID(executionID string, limit int) ([]*LogEntry, error) {
	rows, err := s.db.Query(`
		SELECT id, COALESCE(execution_id, ''), timestamp, level, message, COALESCE(component, 'executor')
		FROM execution_logs
		WHERE execution_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, executionID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var entries []*LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.ExecutionID, &e.Timestamp, &e.Level, &e.Message, &e.Component); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to chronological (oldest first) order for readable tail output.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
}

// GetLastBriefSent returns the most recent brief record for a given channel.
// Returns nil if no brief has been sent to the channel.
func (s *Store) GetLastBriefSent(channel string) (*BriefRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, sent_at, channel, brief_type, COALESCE(recipient, '')
		FROM brief_history
		WHERE channel = ?
		ORDER BY sent_at DESC
		LIMIT 1
	`, channel)

	var record BriefRecord
	err := row.Scan(&record.ID, &record.SentAt, &record.Channel, &record.BriefType, &record.Recipient)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
