package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	// _time_format=sqlite makes modernc.org/sqlite write bound time.Time
	// parameters using a format SQLite's own date()/datetime()/strftime()
	// recognize (one of https://www.sqlite.org/lang_datefunc.html's formats).
	// Without it the driver falls back to Go's time.Time.String() ("2006-01-02
	// 15:04:05.999999999 -0700 MST"), which those functions can't parse (they
	// silently return NULL) and which also sorts inconsistently against
	// CURRENT_TIMESTAMP-populated columns in plain string comparisons
	// (GH-4332).
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
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

	// GH-4569: warn (stderr only, never stdout) on every open if the ledger
	// looks stale or is marked archived — every NewStore caller (CLI
	// queries, dashboard, pilot start) routes through here, so this is the
	// one place that reliably covers "any ledger-reading entry point".
	store.warnIfStale()

	return store, nil
}

// executionCount returns the number of rows in the executions table. Used by
// NewStoreGuarded (GH-4393) to tell a freshly-initialized, empty ledger
// apart from one with real history.
func (s *Store) executionCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM executions").Scan(&count)
	return count, err
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
		// GH-3764(c): no literal default — a guessed model name is worse than an
		// honest NULL. Existing rows already backfilled with the old
		// 'claude-sonnet-4-5' default are left untouched (this is an additive
		// ALTER TABLE; SQLite can't rewrite a column's default in place).
		// Readers COALESCE(NULLIF(model_name, ''), 'unknown') instead of guessing.
		`ALTER TABLE executions ADD COLUMN model_name TEXT`,
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
		// GH-4033: separate "when the runner actually started this execution" from
		// created_at ("when it was queued"). A decomposed subtask's row is created at
		// decomposition time but can sit queued behind a sibling for a while before the
		// worker picks it up — the stuck-monitor must time it from started_at, not
		// created_at, or a legitimately-running subtask gets evicted as stale.
		`ALTER TABLE executions ADD COLUMN started_at DATETIME`,
		// GH-4240: marks executions from a synthetic canary sandbox project
		// (ProjectConfig.Canary) so they can be excluded from success-rate/
		// throughput metrics, the metrics hydrator, and dashboard history
		// without touching the ledger itself.
		`ALTER TABLE executions ADD COLUMN is_canary BOOLEAN DEFAULT FALSE`,
		// TASK-407/GH-4349: the atomic dispatch-admission claim. A row here
		// means one caller has already won the right to begin execution for
		// (task_id, project_path, generation); every other Begin caller for
		// the same key loses (ClaimExecution's INSERT OR IGNORE +
		// RowsAffected()==1, mirroring the ClaimSpawnedFix idiom at
		// internal/autopilot/state_store.go:1062). Rows are permanent per
		// generation — a legitimate retry claims generation+1 rather than
		// deleting/reusing this row, so the claim survives crash windows.
		`CREATE TABLE IF NOT EXISTS execution_claims (
			task_id TEXT NOT NULL,
			project_path TEXT NOT NULL,
			generation INTEGER NOT NULL,
			execution_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (task_id, project_path, generation)
		)`,
		// GH-4394: repick backoff (#4385) was tracked purely in an
		// in-process map (cmd/pilot/repick_backoff.go) that reset to empty on
		// every daemon restart AND diverged per-process whenever a shadow DB
		// split-brain put two pilot processes on different SQLite files
		// (#4393) — evidenced by GH-85 re-picking 5x in ~15 minutes with no
		// backoff growth across a daemon restart mid-storm. Persisting the
		// cooldown to the canonical ledger (same file execution_claims lives
		// in) means whichever process instance loads this row next continues
		// the SAME growing backoff instead of starting over at zero. Keyed by
		// the same opaque "project_path|task_id" string the tracker already
		// uses (repickBackoffKey) rather than split columns, so the
		// persistence layer stays a pure key/value mirror of the in-memory
		// entries with no re-derivation logic on either side.
		`CREATE TABLE IF NOT EXISTS repick_backoff (
			key TEXT PRIMARY KEY,
			consecutive_drops INTEGER NOT NULL DEFAULT 0,
			next_allowed_at DATETIME NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		// GH-4502: a stall-watchdog kill (execution status "stalled") is not
		// evidence a task's code is broken — it's a healthy session sitting in
		// a long silent model turn that got killed defensively — so it must
		// not grow the same consecutive_drops counter a genuine failure does
		// (incident: pilot-console GH-24, 4 stall-kills wedged a healthy task
		// at dispatcherRepickHardCap). Tracked in its own column on the same
		// row (same key, same ledger) rather than a separate table, so it
		// rides along with the existing repick_backoff persistence/migration
		// path instead of duplicating it.
		`ALTER TABLE repick_backoff ADD COLUMN stall_drops INTEGER NOT NULL DEFAULT 0`,
		// GH-4540/TASK-421: a dispatch attempt refused because the task is
		// already queued/running, or already terminally done, is not
		// evidence of a genuine failure any more than a stall-kill is (same
		// reasoning as stall_drops above) — but unlike a stall-kill, this
		// class of drop is detected at the cmd/pilot chokepoint
		// (handleIssueGeneric), not inside beginWithGenerationRetry, so it
		// needs its own column reachable from that package too. See
		// cmd/pilot/repick_backoff.go's recordClaimLostDrop.
		`ALTER TABLE repick_backoff ADD COLUMN claim_lost_drops INTEGER NOT NULL DEFAULT 0`,
		// GH-4540/TASK-421: an infra-classified failure (status "infra" —
		// e.g. a hosted git_clean preflight deadlock or a CI outage) is not
		// evidence the task's own code is broken, mirroring stall_drops'
		// reasoning exactly but for a different prior-claim status. See
		// Dispatcher.priorClaimWasInfra.
		`ALTER TABLE repick_backoff ADD COLUMN infra_drops INTEGER NOT NULL DEFAULT 0`,
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
			error LIKE '%context cancelled%' OR
			error LIKE '%parent task is already done%'
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
	UserID     string
	Status     string
	Output     string
	Error      string
	DurationMs int64
	PRUrl      string
	CommitSHA  string
	CreatedAt  time.Time
	// StartedAt is when the row actually transitioned to "running" (the runner
	// began execution), as opposed to CreatedAt (when it was queued/decomposed).
	// Nil until the worker picks it up. GH-4033.
	StartedAt   *time.Time
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
	// IsCanary marks this execution as belonging to a synthetic canary
	// sandbox project (GH-4240). Excluded from success-rate/throughput
	// metrics, the metrics hydrator, and dashboard history; the ledger row
	// itself is written identically regardless of this flag.
	IsCanary bool
}

// SaveExecution saves an execution record to the database.
// The execution ID must be unique; duplicate IDs will cause an error.
func (s *Store) SaveExecution(exec *Execution) error {
	labelsJSON, err := marshalLabels(exec.TaskLabels)
	if err != nil {
		return fmt.Errorf("failed to marshal task labels: %w", err)
	}
	// createdAt is stamped in Go (rather than left to the column's
	// DEFAULT CURRENT_TIMESTAMP) so callers that pre-set exec.CreatedAt
	// (tests seeding a fixed time window) get exactly that value persisted,
	// instead of the real insertion wall-clock time silently overriding it.
	// The period queries (GetExecutionsInPeriod, GetBriefMetrics) filter on
	// this column against Go time.Time bounds — letting SQLite pick the
	// timestamp raced the caller's bounds under load (GH-4332).
	createdAt := exec.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return s.withRetry("SaveExecution", func() error {
		_, err := s.db.Exec(`
			INSERT INTO executions (id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
				tokens_input, tokens_output, tokens_total, tokens_cache_read, tokens_cache_write,
				estimated_cost_usd, files_changed, lines_added, lines_removed, model_name,
				task_title, task_description, task_branch, task_base_branch, task_create_pr, task_verbose,
				task_source_adapter, task_source_issue_id, task_labels,
				approval_request_id, effort_level, complexity_level, is_canary)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, exec.ID, exec.TaskID, exec.ProjectPath, exec.Status, exec.Output, exec.Error, exec.DurationMs, exec.PRUrl, exec.CommitSHA, createdAt, exec.CompletedAt,
			exec.TokensInput, exec.TokensOutput, exec.TokensTotal, exec.TokensCacheRead, exec.TokensCacheWrite,
			exec.EstimatedCostUSD, exec.FilesChanged, exec.LinesAdded, exec.LinesRemoved, exec.ModelName,
			exec.TaskTitle, exec.TaskDescription, exec.TaskBranch, exec.TaskBaseBranch, exec.TaskCreatePR, exec.TaskVerbose,
			exec.TaskSourceAdapter, exec.TaskSourceIssueID, labelsJSON,
			exec.ApprovalRequestID, exec.EffortLevel, exec.ComplexityLevel, exec.IsCanary)
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
	id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, started_at, completed_at,
	COALESCE(tokens_input, 0), COALESCE(tokens_output, 0), COALESCE(tokens_total, 0),
	COALESCE(tokens_cache_read, 0), COALESCE(tokens_cache_write, 0),
	COALESCE(estimated_cost_usd, 0), COALESCE(files_changed, 0), COALESCE(lines_added, 0),
	COALESCE(lines_removed, 0), COALESCE(NULLIF(model_name, ''), 'unknown'),
	COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
	COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0),
	COALESCE(task_source_adapter, ''), COALESCE(task_source_issue_id, ''),
	COALESCE(task_labels, ''),
	COALESCE(approval_request_id, ''), COALESCE(approval_decision, ''),
	approval_decision_at,
	COALESCE(approval_decision_by, ''),
	COALESCE(effort_level, ''), COALESCE(complexity_level, ''),
	COALESCE(is_canary, 0)`

// rowScanner abstracts *sql.Row and *sql.Rows so scanExecutionDetail serves both
// a single QueryRow result and a Query loop (used by ListExecutionsForTask).
type rowScanner interface {
	Scan(dest ...any) error
}

// scanExecutionDetail scans a row selected via executionDetailColumns into an Execution.
func scanExecutionDetail(row rowScanner) (*Execution, error) {
	var exec Execution
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var approvalDecisionAt sql.NullTime
	var labelsJSON string
	err := row.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &startedAt, &completedAt,
		&exec.TokensInput, &exec.TokensOutput, &exec.TokensTotal, &exec.TokensCacheRead, &exec.TokensCacheWrite,
		&exec.EstimatedCostUSD, &exec.FilesChanged, &exec.LinesAdded, &exec.LinesRemoved, &exec.ModelName,
		&exec.TaskTitle, &exec.TaskDescription, &exec.TaskBranch, &exec.TaskBaseBranch, &exec.TaskCreatePR, &exec.TaskVerbose,
		&exec.TaskSourceAdapter, &exec.TaskSourceIssueID, &labelsJSON,
		&exec.ApprovalRequestID, &exec.ApprovalDecision, &approvalDecisionAt, &exec.ApprovalDecisionBy,
		&exec.EffortLevel, &exec.ComplexityLevel, &exec.IsCanary)
	if err != nil {
		return nil, err
	}
	exec.TaskLabels = unmarshalLabels(labelsJSON)
	if startedAt.Valid {
		exec.StartedAt = &startedAt.Time
	}
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
// "GH-15"), scoped to projectPath the same way GetExecutionStatusByTaskID is (empty
// projectPath skips the filter, preserving pre-GH-4352 behavior for callers with no
// project context, e.g. the CLI). Returns sql.ErrNoRows if no execution matches.
func (s *Store) GetLatestExecutionByTaskID(taskID, projectPath string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT `+executionDetailColumns+`
		FROM executions
		WHERE (task_id = ? OR task_id LIKE ?) AND (? = '' OR project_path = ?)
		ORDER BY (task_id = ?) DESC, created_at DESC, rowid DESC
		LIMIT 1
	`, taskID, "%"+taskID+"%", projectPath, projectPath, taskID)
	return scanExecutionDetail(row)
}

// GetLatestExecutionByTaskIDExcluding mirrors GetLatestExecutionByTaskID but
// ignores the row identified by excludeID. GH-4141: an epic sub-issue now
// carries its own "running" executions row for the full run duration
// (internal/executor/epic.go finalizeSubIssueExecution), so a caller
// reconciling against a genuinely separate, concurrently-tracked row for the
// same task (GH-3786) must look past its own row to find it.
//
// GH-4352: projectPath scopes the same way GetExecutionStatusByTaskIDExcluding
// does — without it, a task_id collision across projects (e.g. sandbox canary
// reusing a low GH-N also live in another repo) let this adopt the wrong
// project's PR/commit as reconciliation evidence.
func (s *Store) GetLatestExecutionByTaskIDExcluding(taskID, projectPath, excludeID string) (*Execution, error) {
	row := s.db.QueryRow(`
		SELECT `+executionDetailColumns+`
		FROM executions
		WHERE (task_id = ? OR task_id LIKE ?) AND (? = '' OR project_path = ?) AND id != ?
		ORDER BY (task_id = ?) DESC, created_at DESC, rowid DESC
		LIMIT 1
	`, taskID, "%"+taskID+"%", projectPath, projectPath, excludeID, taskID)
	return scanExecutionDetail(row)
}

// ListExecutionsByTaskIDExcluding returns every execution row for taskID
// (exact match, scoped to projectPath), excluding excludeID, newest first —
// the full history, unlike GetLatestExecutionByTaskIDExcluding's single
// "latest by created_at" row.
//
// GH-4381: a caller reconciling a child's outcome must scan every row for a
// terminal one, not just the most recent — a fresh "queued" duplicate row
// can be written after an older terminal "no_op" row and would sort first,
// hiding it from any check that only looks at the latest row (the same
// latest-row-ordering trap HasTerminalCompletion was built to avoid for
// admission gates, GH-4347).
func (s *Store) ListExecutionsByTaskIDExcluding(taskID, projectPath, excludeID string) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT `+executionDetailColumns+`
		FROM executions
		WHERE task_id = ? AND (? = '' OR project_path = ?) AND id != ?
		ORDER BY created_at DESC, rowid DESC
	`, taskID, projectPath, projectPath, excludeID)
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

// ListExecutionsForTask returns every execution recorded for taskID (exact match),
// newest first. Unlike GetLatestExecutionByTaskID (single row, exact-or-substring
// match), this returns the full history so the CLI can render a per-execution
// stage timeline across retries (TASK-379 C4).
//
// GH-4378: task_id is not unique across projects (every freshly onboarded
// repo starts issue numbering at #1 — same collision class as GH-4276), so
// an unscoped lookup interleaves unrelated repos' executions into one
// timeline. projectPath scopes the same way GetLatestExecutionByTaskID does:
// empty projectPath skips the filter (cross-project, for callers that have
// already resolved ambiguity another way, e.g. via ListProjectsForTask).
func (s *Store) ListExecutionsForTask(taskID, projectPath string) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT `+executionDetailColumns+`
		FROM executions
		WHERE task_id = ? AND (? = '' OR project_path = ?)
		ORDER BY created_at DESC, rowid DESC
	`, taskID, projectPath, projectPath)
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

// TaskProjectSummary identifies one project's most recent execution for a
// given task_id. Used by `pilot trace` to detect when a task_id collides
// across multiple projects (GH-4378) so it can list candidates instead of
// silently merging their executions into one timeline.
type TaskProjectSummary struct {
	ProjectPath string
	LatestAt    time.Time
}

// ListProjectsForTask returns, for every distinct project_path with a
// recorded execution for taskID, that project's most recent execution
// timestamp — newest first. Scans every row rather than GROUP BY
// MAX(created_at) because the sqlite driver can't map an aggregate
// expression back to time.Time (only declared-typed columns get that
// treatment) — reading created_at directly newest-first and keeping the
// first occurrence per project_path sidesteps that.
func (s *Store) ListProjectsForTask(taskID string) ([]TaskProjectSummary, error) {
	rows, err := s.db.Query(`
		SELECT project_path, created_at
		FROM executions
		WHERE task_id = ?
		ORDER BY created_at DESC, rowid DESC
	`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	seen := make(map[string]bool)
	var summaries []TaskProjectSummary
	for rows.Next() {
		var projectPath string
		var createdAt time.Time
		if err := rows.Scan(&projectPath, &createdAt); err != nil {
			return nil, err
		}
		if seen[projectPath] {
			continue
		}
		seen[projectPath] = true
		summaries = append(summaries, TaskProjectSummary{ProjectPath: projectPath, LatestAt: createdAt})
	}
	return summaries, rows.Err()
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
	// Claude for real implementation work. GH-3938 wired claude_started/
	// decomposed/completed on the epic-parent path only; GH-4129 wires this
	// value on the direct path so the enum matches the full lifecycle
	// described in GH-3840.
	StageImplementationStarted Stage = "implementation_started"
	StageCommit                Stage = "commit"
	StagePRCreated             Stage = "pr_created"
	// StageWaitingCI mirrors autopilot.StageWaitingCI (types.go) — the PR is
	// waiting for CI checks to complete. GH-4128.
	StageWaitingCI        Stage = "waiting_ci"
	StageCIPassed         Stage = "ci_passed"
	StageCIFailed         Stage = "ci_failed"
	StageAwaitingApproval Stage = "awaiting_approval"
	StageMerged           Stage = "merged"
	StageReleased         Stage = "released"
	StageCompleted        Stage = "completed"
	StageFailed           Stage = "failed"
	StageNoOp             Stage = "no_op"
	StageSkipped          Stage = "skipped"
	StageStalled          Stage = "stalled"
	// StageSuperseded records that an execution was finalized without running
	// (pickup-time) or without opening a PR (pre-PR-create) because its
	// GitHub issue was found closed — GH-4656, closing the 2026-07-31
	// GH-4649 incident window (a retry finished after its issue had already
	// been closed as superseded by a sibling/parent run and opened PR#4653
	// against the closed issue anyway). Detail carries the observed issue
	// state and whether a pilot-superseded label was present.
	StageSuperseded Stage = "superseded"
	// StageResearchPhase records the parallel-research phase's duration and
	// token spend once per direct-path execution (GH-4129). Detail is a JSON
	// object: {"duration_ms","total_tokens","findings"}.
	StageResearchPhase Stage = "research_phase"
	// StageQualityGate records quality.CheckResults timing: one event per
	// gate (detail: {"gate","duration_ms","passed","retry_count"}) plus a
	// trailing summary event (detail: {"total_duration_ms","gate_count"})
	// (GH-4129).
	StageQualityGate Stage = "quality_gate"
	// StageRetryAttempt tags a retried backend invocation with the loop that
	// triggered it (smart_retry, quality_gate_retry, intent_judge_retry) so
	// retry wall-clock share is queryable (GH-4129). Detail is a JSON object:
	// {"loop","attempt"}.
	StageRetryAttempt Stage = "retry_attempt"
	// StageDecompositionSkipped records that a task classified epic (or at/
	// above decompose.min_complexity) did NOT enter decomposition — the gate
	// and concrete threshold/observed values are carried in Detail as a
	// single-line machine-readable message (GH-4271). Distinct from
	// StageSkipped/StageNoOp, which classify the whole execution's terminal
	// outcome: this stage fires mid-execution while the task still proceeds
	// to direct execution, closing the silent-epic-bypass gap that made a
	// canary run (sandbox#6, 275 words vs min 300) indistinguishable from the
	// TASK-401 defect class without hand-querying execution_events.
	StageDecompositionSkipped Stage = "decomposition_skipped"
	// StageSubIssuesIncomplete records a sub-issue creation coverage gap
	// (GH-4300): fewer sub-issues exist for a decomposed epic than its plan
	// called for, even after retrying transient creation failures. Detail
	// carries "planned=N created=M missing=<titles>". A gap fires this stage
	// instead of StageFailed because the epic parent is deliberately left
	// open (labeled pilot-needs-clarification) rather than terminated —
	// incident 2026-07-14 (pilot-console#1) closed a parent with 1 of 2
	// planned subtasks never dispatched after a transient sub-issue-creation
	// failure went unnoticed.
	StageSubIssuesIncomplete Stage = "sub_issues_incomplete"
	// StageDispatchClaimLost records that a Begin call site (epic sub-issue
	// loop, dispatcher queue path, CLI) lost the execution_claims race for
	// (task_id, project_path, generation) to another dispatch channel —
	// evidence that the atomic claim (TASK-407/GH-4349) did its job instead
	// of a second execution starting silently. Detail carries a short
	// human-readable note naming which sub-issue/task lost the claim.
	StageDispatchClaimLost Stage = "dispatch_claim_lost"
	// StageMemoryGuardRestore records that the GH-4387 protected-memory guard
	// fail-safe-restored a .agent/knowledge/memories/**.md file the execution
	// had deleted while it was still referenced by a .agent/knowledge/graph.json
	// node (GH-4398). One event per restored file; Detail is a JSON object:
	// {"path","node_id"}. Without this, a guard that silently rewrites the
	// branch would be invisible to anyone reviewing the PR or `pilot trace`.
	StageMemoryGuardRestore Stage = "memory_guard_restore"
	// StageWorkPreserved records the GH-4517 harvester backstop firing: the
	// worktree was about to be classified no_op (and deleted by the runner's
	// deferred worktree cleanup) but still had uncommitted changes, so the
	// executor auto-committed them to the task branch as a wip(<task-id>)
	// commit and pushed instead of silently discarding the work. Detail
	// carries the same human-readable message as result.Error (short SHA,
	// branch, "needs manual review" note). Distinct from StageCommit (a
	// normal, agent-authored commit) and StageNoOp (a genuine no-op with
	// nothing to preserve) — this is a rescue path for a session that did
	// real work but never ran `git commit` itself. Incident: pilot-console#26
	// (B8), 2026-07-23 — 44 minutes of completed, test-passing work destroyed
	// before this backstop existed.
	StageWorkPreserved Stage = "work_preserved"
	// StageGithubSideEffect records a GH-4670 post-run audit hit: a GitHub
	// issue in the task's own repo was closed or reopened during the run
	// window OTHER than the issue the session was dispatched to fix — the
	// GH-4649 incident class (an executor session improvised `gh issue
	// close` + a label on a SIBLING issue mid-run). Detail is a JSON object:
	// {"repo","number","title","state","url","task_issue","run_start_at"}.
	// Detective only — no auto-revert; paired with an alert-engine warning
	// (executor/sideeffect_audit.go) for a human operator to judge.
	StageGithubSideEffect Stage = "executor.github_sideeffect"
	// StageCanceled records that an operator ran `pilot task cancel`
	// (GH-4678) against this execution — a deliberate terminal decision,
	// distinct from StageStalled (dead-owner recovery signal, retry-worthy)
	// and StageFailed (an unplanned outcome). Detail carries the operator's
	// reason string. See executor.ExecutionLifecycle.Cancel.
	StageCanceled Stage = "canceled"
	// StageGhGuardDenied records a GH-4671 gh-guard shim denial: the
	// executor session attempted a `gh` CLI call (via its Bash tool) that
	// the policy in executor/ghguard rejected before it reached GitHub —
	// the preventive counterpart to StageGithubSideEffect, which can only
	// see a bad call after the fact. Detail is a JSON object:
	// {"args","reason","task_issue","task_repo"}. Paired with an
	// alert-engine warning (AlertEventTypeGhGuardDenied) for a human
	// operator to judge whether the attempted call indicates a prompt/task
	// problem worth investigating.
	StageGhGuardDenied Stage = "executor.gh_guard_denied"
)

// Event represents a single stage-transition record for an execution.
type Event struct {
	ID          int64
	ExecutionID string
	Stage       Stage
	OccurredAt  time.Time
	Detail      string
}

// ErrExecutionNotFound is returned by RecordExecutionEvent when executionID
// has no matching executions row. Callers treat it as a warn-and-skip signal,
// never as a reason to fail the caller's own operation (GH-4244).
var ErrExecutionNotFound = errors.New("execution not found")

// RecordExecutionEvent is the validate-first entry point for writing to the
// execution_events audit trail (GH-4244). It replaces the pattern of calling
// InsertExecutionEvent directly and letting a missing parent row surface as a
// SQLite foreign-key constraint error (FK-787) — instead it confirms the
// executions row exists first and returns ErrExecutionNotFound so the caller
// can log a clean warning and skip the write. This is now the single place
// that guards the executions/execution_events FK; every recordExecutionEvent
// wrapper across the executor and autopilot packages delegates here.
func (s *Store) RecordExecutionEvent(executionID string, stage Stage, detail string) error {
	return recordExecutionEventOn(s.db, executionID, stage, detail)
}

// HasExecutionEventStage reports whether executionID's execution_events
// ledger already carries an entry for stage. Used by heal/backfill passes
// (e.g. GH-4370's release-tag-ancestry reconciliation) so a repeat sweep — or
// a crash between writing the event and draining the row it healed — never
// double-stamps the ladder.
func (s *Store) HasExecutionEventStage(executionID string, stage Stage) (bool, error) {
	var exists bool
	err := s.db.QueryRow(`
		SELECT EXISTS(SELECT 1 FROM execution_events WHERE execution_id = ? AND stage = ?)
	`, executionID, string(stage)).Scan(&exists)
	return exists, err
}

// dbExecer is satisfied by both *sql.DB and *sql.Tx. recordExecutionEventOn is
// generalized over it so the GH-4292 heal paths can run the same validate-first
// check and insert inside their own transaction, atomically with the status
// UPDATE they commit alongside it, instead of hand-rolling a second INSERT.
type dbExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

// recordExecutionEventOn is RecordExecutionEvent's validate-first logic,
// generalized to run against any dbExecer rather than always the Store's own
// s.db connection — see dbExecer's doc comment.
func recordExecutionEventOn(db dbExecer, executionID string, stage Stage, detail string) error {
	row := db.QueryRow(`SELECT `+executionDetailColumns+` FROM executions WHERE id = ?`, executionID)
	if _, err := scanExecutionDetail(row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: execution_id=%s", ErrExecutionNotFound, executionID)
		}
		return fmt.Errorf("failed to verify execution %s exists: %w", executionID, err)
	}
	_, err := db.Exec(`
		INSERT INTO execution_events (execution_id, stage, occurred_at, detail)
		VALUES (?, ?, ?, ?)
	`, executionID, string(stage), time.Now().UTC(), detail)
	return err
}

// InsertExecutionEvent records a stage transition for executionID. occurred_at is
// always the write-time UTC clock, not a caller-supplied value, so the ledger
// can't be back-dated or skewed by local timezone (TASK-379 C2/C3). Most
// callers should use RecordExecutionEvent instead, which validates the parent
// row exists first; this method is exposed for call sites (e.g. tests,
// RecordExecutionEvent itself) that already hold a known-good execution ID.
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

// HasTerminalCompletion reports whether taskID has ANY row in projectPath
// that is terminal in the sense that no further dispatch is warranted:
// either a genuine HasCompletedExecution row (completed with a commit/PR
// deliverable), a no_op row with no error ("nothing to change" is itself
// a legitimate completion — the same definition childCompletionEvidence
// uses in internal/executor/dispatcher.go for decomposed-child evidence),
// or a canceled row (GH-4678: an operator ran `pilot task cancel` — that is
// a deliberate, permanent "stop dispatching this" decision, not a failure to
// retry; unlike the no_op branch this one does NOT require an empty error,
// since Cancel always records the operator's reason in the error column).
//
// GH-4347: deliberately an ANY-row check, unlike childCompletionEvidence's
// no_op fallback (which inspects only GetLatestExecutionByTaskID's most
// recent row — correct for that call site, where "latest" is the child's
// only prior attempt). Here the caller is specifically re-checking
// admission for a task_id that may already have a fresh "queued" duplicate
// row alongside an earlier no_op row — the fresh row would be "latest" and
// would wrongly hide the terminal no_op if this used the same latest-only
// definition. Scanning every row for the task avoids that ordering trap.
func (s *Store) HasTerminalCompletion(taskID, projectPath string) (bool, error) {
	completed, err := s.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		return false, err
	}
	if completed {
		return true, nil
	}

	var count int
	err = s.db.QueryRow(`
		SELECT COUNT(*) FROM executions
		WHERE task_id = ? AND project_path = ? AND (
			(status = 'no_op' AND (error IS NULL OR error = ''))
			OR status = 'canceled'
		)
	`, taskID, projectPath).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// decomposedChildRefRegex extracts "#123"-style issue references from a
// StageDecomposed execution_events detail string, e.g. "decomposed into 2
// children: #4212, #4213" (see executor.formatDecomposedChildrenSummary).
var decomposedChildRefRegex = regexp.MustCompile(`#(\d+)`)

// GetDecomposedChildTaskIDs returns the child task IDs (formatted "GH-<n>",
// matching the task_id convention sub-issue dispatch uses) parsed from the
// most recent StageDecomposed execution_events entry recorded for
// taskID/projectPath, and whether any decomposed event was found at all.
//
// GH-4216 (Defect A, fix 3): backs the cross-task-id dispatch guard, which
// tells a genuinely-fresh task apart from an epic parent whose children
// already shipped. HasCompletedExecution(taskID, ...) alone can never see
// this — an epic parent that produced no direct deliverable of its own
// (TASK-296, epic-parent no-deliverable rows excluded there) never satisfies
// it, no matter how done its children are.
func (s *Store) GetDecomposedChildTaskIDs(taskID, projectPath string) ([]string, bool, error) {
	var detail string
	err := s.db.QueryRow(`
		SELECT COALESCE(e.detail, '')
		FROM execution_events e
		JOIN executions x ON x.id = e.execution_id
		WHERE x.task_id = ? AND x.project_path = ? AND e.stage = ?
		ORDER BY e.occurred_at DESC, e.id DESC
		LIMIT 1
	`, taskID, projectPath, string(StageDecomposed)).Scan(&detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	matches := decomposedChildRefRegex.FindAllStringSubmatch(detail, -1)
	if len(matches) == 0 {
		return nil, true, nil
	}
	seen := make(map[string]bool, len(matches))
	childIDs := make([]string, 0, len(matches))
	for _, m := range matches {
		id := "GH-" + m[1]
		if !seen[id] {
			seen[id] = true
			childIDs = append(childIDs, id)
		}
	}
	return childIDs, true, nil
}

// decomposedDetailRegex matches the well-formed "decomposed into N children:
// #a, #b, #c" detail string emitted by executor.formatDecomposedChildrenSummary
// (internal/executor/epic.go). Group 1 captures the raw child-ref list.
var decomposedDetailRegex = regexp.MustCompile(`^decomposed into \d+ children:\s*(.*)$`)

// childIssueNumberRegex matches a single "#123" child reference within the
// list captured by decomposedDetailRegex.
var childIssueNumberRegex = regexp.MustCompile(`^#(\d+)$`)

// GetDecomposedChildren returns the child issue numbers parsed from the most
// recent StageDecomposed execution_events entry recorded for taskID (across
// all projects — unlike GetDecomposedChildTaskIDs, this is not project-path
// scoped), and whether a well-formed decomposed event was found.
//
// Returns (childIssueNumbers, true) when the latest StageDecomposed detail
// matches the "decomposed into N children: #a, #b, #c" format. Returns
// (nil, false) when no StageDecomposed event exists for taskID, and
// (nil, false) with a warning log when a StageDecomposed event exists but
// its detail string is malformed (missing colon, non-numeric child ref, or
// an empty child list) — a shape the trusted emitter should never produce,
// but one that must not be silently treated as "absent".
func (s *Store) GetDecomposedChildren(taskID string) ([]string, bool) {
	var detail string
	err := s.db.QueryRow(`
		SELECT COALESCE(e.detail, '')
		FROM execution_events e
		JOIN executions x ON x.id = e.execution_id
		WHERE x.task_id = ? AND e.stage = ?
		ORDER BY e.occurred_at DESC, e.id DESC
		LIMIT 1
	`, taskID, string(StageDecomposed)).Scan(&detail)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		slog.Warn("GetDecomposedChildren: query failed", "task_id", taskID, "error", err)
		return nil, false
	}

	m := decomposedDetailRegex.FindStringSubmatch(detail)
	if m == nil {
		slog.Warn("GetDecomposedChildren: malformed decomposed detail string", "task_id", taskID, "detail", detail)
		return nil, false
	}

	childList := strings.TrimSpace(m[1])
	if childList == "" {
		slog.Warn("GetDecomposedChildren: decomposed detail has empty child list", "task_id", taskID, "detail", detail)
		return nil, false
	}

	tokens := strings.Split(childList, ",")
	children := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		cm := childIssueNumberRegex.FindStringSubmatch(tok)
		if cm == nil {
			slog.Warn("GetDecomposedChildren: malformed child reference in decomposed detail", "task_id", taskID, "detail", detail, "token", tok)
			return nil, false
		}
		children = append(children, cm[1])
	}

	return children, true
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

// ReclassifyCompletionAsSuperseded is ReclassifyCompletionAsFailed's sibling
// for the GH-4701 case: a completed execution row whose PR was closed
// because an operator deliberately closed the issue as not-planned (or a
// prior stage already tagged the close pilot-superseded — e.g. a
// sibling/duplicate execution delivered the same scope first), not because
// anything actually failed. Demoting to "failed" here would render the row
// as a pipeline failure in HISTORY (the 2026-08-03 #4655 cluster: 6 of 43
// rows misread that way); "superseded" is a muted, distinct outcome instead
// (see internal/dashboard/stage_strip.go's mutedOutcomes).
func (s *Store) ReclassifyCompletionAsSuperseded(taskID, projectPath, reason string) error {
	return s.withRetry("ReclassifyCompletionAsSuperseded", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = 'superseded',
				error = ?,
				completed_at = CURRENT_TIMESTAMP
			WHERE task_id = ? AND (? = '' OR project_path = ?) AND status = 'completed'
				AND (error IS NULL OR error = '')
				AND (commit_sha != '' OR pr_url != '')
		`, reason, taskID, projectPath, projectPath)
		return err
	})
}

// TerminateNonTerminalExecution flips the latest execution row for taskID
// (same created_at DESC, rowid DESC selection as GetExecutionStatusByTaskID)
// to status='failed' when it is still queued/pending/running. GH-4499: covers
// the gap ReclassifyCompletionAsFailed intentionally leaves open — that method
// only ever demotes a genuine "completed" row, so a PR closed externally while
// its execution row was still non-terminal (e.g. the poller never got to mark
// it completed, or it was killed mid-flight) left the row stuck forever. On
// the next daemon restart HydrateFromStore re-seeds that row into the Monitor
// as a running card, and Monitor.ReconcileWithStore (GH-4490) can't rescue it
// because the reconciler trusts the executions row as source of truth.
//
// Only the latest row is eligible — selecting it via the subquery before
// applying the status filter means a newer row from a live retry (which
// would sort first) shields any older non-terminal row from being touched.
//
// projectPath follows ReclassifyCompletionAsFailed's scoping convention:
// empty drops the scope and matches by task_id alone.
func (s *Store) TerminateNonTerminalExecution(taskID, projectPath, reason string) error {
	return s.withRetry("TerminateNonTerminalExecution", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = 'failed',
				error = ?,
				completed_at = CURRENT_TIMESTAMP
			WHERE id = (
				SELECT id FROM executions
				WHERE task_id = ? AND (? = '' OR project_path = ?)
				ORDER BY created_at DESC, rowid DESC
				LIMIT 1
			) AND status IN ('queued', 'pending', 'running')
		`, reason, taskID, projectPath, projectPath)
		return err
	})
}

// TerminateNonTerminalExecutionAsSuperseded is TerminateNonTerminalExecution's
// sibling for the GH-4701 case: see ReclassifyCompletionAsSuperseded's doc
// comment for when this applies over the "failed" variant.
func (s *Store) TerminateNonTerminalExecutionAsSuperseded(taskID, projectPath, reason string) error {
	return s.withRetry("TerminateNonTerminalExecutionAsSuperseded", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET status = 'superseded',
				error = ?,
				completed_at = CURRENT_TIMESTAMP
			WHERE id = (
				SELECT id FROM executions
				WHERE task_id = ? AND (? = '' OR project_path = ?)
				ORDER BY created_at DESC, rowid DESC
				LIMIT 1
			) AND status IN ('queued', 'pending', 'running')
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

// GetExecutionByApprovalRequestID returns the execution row whose approval_request_id
// matches requestID (populated by SetApprovalRequestID after SubmitApprovalRequest
// succeeds). Used by the gateway approvals API (GH-4748) to join a pending
// approval_pending row to its execution/PR context. Returns sql.ErrNoRows if
// requestID is empty (the column defaults to ” for every non-approval execution,
// so an empty requestID must never be allowed to match) or no execution matches.
func (s *Store) GetExecutionByApprovalRequestID(requestID string) (*Execution, error) {
	if requestID == "" {
		return nil, sql.ErrNoRows
	}
	row := s.db.QueryRow(`SELECT `+executionDetailColumns+` FROM executions WHERE approval_request_id = ? ORDER BY created_at DESC LIMIT 1`, requestID)
	return scanExecutionDetail(row)
}

// GetRecentExecutions returns the most recent executions ordered by creation time.
// The limit parameter specifies the maximum number of executions to return.
// If projectPath is non-empty, only executions for that project are returned.
func (s *Store) GetRecentExecutions(limit int, projectPath string) ([]*Execution, error) {
	// GH-4240: is_canary = 0 excludes the synthetic canary sandbox from
	// dashboard queue/history — its executions are still fully persisted,
	// just not surfaced here.
	const base = `
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at,
			COALESCE(task_title, ''), COALESCE(task_description, ''), COALESCE(task_branch, ''),
			COALESCE(task_base_branch, ''), COALESCE(task_create_pr, 0), COALESCE(task_verbose, 0),
			COALESCE(peak_rss_mb, 0), COALESCE(final_rss_mb, 0)
		FROM executions
		WHERE COALESCE(is_canary, 0) = 0`
	var rows *sql.Rows
	var err error
	if projectPath != "" {
		rows, err = s.db.Query(base+` AND project_path = ? ORDER BY created_at DESC LIMIT ?`, projectPath, limit)
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

// FindOrphanedRunningExecutions returns status='running' rows whose task_id is
// NOT in excludeTaskIDs — candidates for the autopilot orphan-running sweep
// (TASK-399/GH-4209). excludeTaskIDs is the caller's in-flight set (e.g. the
// live executor.Monitor's running/queued task IDs), so a genuinely executing
// task is never returned as a candidate for reconciliation.
func (s *Store) FindOrphanedRunningExecutions(excludeTaskIDs []string) ([]*Execution, error) {
	query := `
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at, COALESCE(task_branch, '')
		FROM executions
		WHERE status = 'running'`
	args := make([]interface{}, 0, len(excludeTaskIDs))
	if len(excludeTaskIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(excludeTaskIDs)), ",")
		query += fmt.Sprintf(" AND task_id NOT IN (%s)", placeholders)
		for _, id := range excludeTaskIDs {
			args = append(args, id)
		}
	}
	query += " ORDER BY created_at ASC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &completedAt, &exec.TaskBranch); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}

	return executions, rows.Err()
}

// orphanedRunningNoEvidenceError is the error stamped on an orphaned running
// row that resolves to 'failed' (no merged PR found). TASK-399/GH-4209.
const orphanedRunningNoEvidenceError = "orphaned running execution: no live process and no merged PR found on pr_url/branch"

// ResolveOrphanedRunningExecution flips one orphan-running candidate (a row
// the caller has already confirmed is not in flight — see
// FindOrphanedRunningExecutions) to a terminal status. prURL non-empty means
// the caller found a merged PR on this row's pr_url or branch, so the row
// heals to 'completed' and prURL is stamped; empty means no merge evidence
// exists, so the row is marked 'failed' — which keeps it eligible for the
// ordinary SelfHealExecutionAfterMerge path if a merge surfaces later, since
// 'failed' is already in that method's non-success IN(...) set.
//
// Guarded by `AND status = 'running'`: a no-op (and therefore idempotent) once
// the row has transitioned through the normal completion path, and safe to
// call repeatedly across sweep ticks. TASK-399/GH-4209.
//
// GH-4292: the status UPDATE and its terminal execution_events row (StageMerged
// when prURL is known, else StageFailed) commit in one transaction — either
// both land or neither does — via recordExecutionEventOn, never a hand-rolled
// INSERT. The UPDATE's `AND status = 'running'` guard makes RowsAffected == 0
// on a second call against an already-healed row, so the event write is skipped
// on repeat calls (idempotent, matching the doc comment above).
func (s *Store) ResolveOrphanedRunningExecution(id, prURL string) error {
	return s.withRetry("ResolveOrphanedRunningExecution", func() error {
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		var result sql.Result
		if prURL != "" {
			result, err = tx.Exec(`
				UPDATE executions
				SET status = 'completed', error = '', completed_at = CURRENT_TIMESTAMP, pr_url = ?
				WHERE id = ? AND status = 'running'
			`, prURL, id)
		} else {
			result, err = tx.Exec(`
				UPDATE executions
				SET status = 'failed', error = ?, completed_at = CURRENT_TIMESTAMP
				WHERE id = ? AND status = 'running'
			`, orphanedRunningNoEvidenceError, id)
		}
		if err != nil {
			return err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return tx.Commit()
		}

		stage, detail := StageFailed, orphanedRunningNoEvidenceError
		if prURL != "" {
			stage, detail = StageMerged, "orphaned running execution resolved: merged "+prURL
		}
		if err := recordExecutionEventOn(tx, id, stage, detail); err != nil {
			return err
		}

		return tx.Commit()
	})
}

// GetBriefMetrics calculates aggregate metrics for a time period including
// task counts, success rates, average duration, and PR creation statistics.
//
// GH-4742: the canary tenant is excluded from every aggregate below (see
// GetWindowedStats/GetLifetimeTaskCounts for the same COALESCE(is_canary, 0)
// = 0 predicate), and SuccessRate is computed over completed+failed only —
// queued/running rows and neutral terminals (no_op, skipped, declined,
// stalled, rate_limited, infra, superseded, decomposed, canceled) are
// excluded from the rate denominator. TotalTasks remains COUNT(*) as a
// volume stat and is NOT the SuccessRate denominator.
func (s *Store) GetBriefMetrics(query BriefQuery) (*BriefMetricsData, error) {
	var result BriefMetricsData

	var args []interface{}
	whereClause := "WHERE created_at >= ? AND created_at < ? AND COALESCE(is_canary, 0) = 0"
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

	if attempted := result.CompletedCount + result.FailedCount; attempted > 0 {
		result.SuccessRate = float64(result.CompletedCount) / float64(attempted)
	}

	return &result, nil
}

// BriefMetricsData holds aggregate metrics calculated from execution data.
type BriefMetricsData struct {
	// TotalTasks is COUNT(*) over the canary-excluded period population — a
	// volume stat. It includes queued/running rows and neutral terminals and
	// is NOT the SuccessRate denominator (see SuccessRate).
	TotalTasks     int
	CompletedCount int
	FailedCount    int
	// SuccessRate is CompletedCount / (CompletedCount + FailedCount): the
	// GH-4735 attempt-success rate. In-flight (queued/running) rows and
	// neutral terminals (no_op, skipped, declined, stalled, rate_limited,
	// infra, superseded, decomposed, canceled) are excluded from both the
	// numerator and denominator.
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
			COALESCE(task_labels, ''), COALESCE(is_canary, 0)
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
			&exec.TaskSourceAdapter, &exec.TaskSourceIssueID, &labelsJSON, &exec.IsCanary); err != nil {
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

// terminalExecutionStatuses are the statuses that make an executions row's
// outcome final. Shared by UpdateExecutionStatus (which stamps completed_at
// for any of these) and the CAS-guarded writes below (GH-4423): a guarded
// write is rejected once the row's CURRENT status is already one of these,
// so a terminal row can never be silently clobbered by a second terminal
// write racing in after the first one landed.
//
// GH-4243 dead-API audit: "cancelled" (double-L) is confirmed dead as a
// write value — no production call site ever passes it to
// UpdateExecutionStatus, MarkExecutionCompleted, or Begin/Transition/Finish
// (executor.Status has no ExecStatusCancelled constant). It's kept in this
// terminal-state list only defensively — matching monitor.go's separate
// in-memory TaskStatus enum, which does have a StatusCancelled, and matching
// dispatcher.go's WaitForExecution terminal-status switch, which reads
// "cancelled" as a possible historical/manually-written value. Not removed
// since dropping it would silently stop setting completed_at on that
// historical value.
//
// GH-4678: "canceled" (single-L) is the LIVE operator-cancel value written
// by executor.ExecutionLifecycle.Cancel (pilot task cancel). Deliberately
// spelled differently from the dead "cancelled" above so the two never
// collide — "canceled" means "operator terminated this on purpose, never
// re-pick", the opposite intent of "cancelled"'s historical retry-worthy
// connotation.
var terminalExecutionStatuses = []string{
	"completed", "failed", "cancelled", "canceled", "declined", "stalled", "no_op", "rate_limited", "infra", "skipped",
}

func isTerminalExecutionStatus(status string) bool {
	for _, t := range terminalExecutionStatuses {
		if status == t {
			return true
		}
	}
	return false
}

// notTerminalClause returns a "status NOT IN (?, ?, ...)" SQL fragment sized
// to terminalExecutionStatuses, plus its matching bind args — the CAS guard
// every UpdateExecutionStatusIfNotTerminal / MarkExecutionCompletedIfNotTerminal
// write appends to its WHERE clause (GH-4423).
func notTerminalClause() (string, []interface{}) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(terminalExecutionStatuses)), ",")
	args := make([]interface{}, len(terminalExecutionStatuses))
	for i, st := range terminalExecutionStatuses {
		args[i] = st
	}
	return placeholders, args
}

// UpdateExecutionStatus updates the status of an execution record.
// Optionally sets the error message if provided. Also sets completed_at for terminal states.
func (s *Store) UpdateExecutionStatus(id, status string, errorMsg ...string) error {
	var errStr *string
	if len(errorMsg) > 0 && errorMsg[0] != "" {
		errStr = &errorMsg[0]
	}

	// Set completed_at for terminal states.
	if isTerminalExecutionStatus(status) {
		return s.withRetry("UpdateExecutionStatus", func() error {
			_, err := s.db.Exec(`
				UPDATE executions
				SET status = ?, error = COALESCE(?, error), completed_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, status, errStr, id)
			return err
		})
	}

	// GH-4033: stamp started_at when the worker actually begins running this
	// execution, so the stuck-monitor's clock reference isn't the row's
	// created_at (queue/decomposition time) — a decomposed subtask can sit
	// queued behind a sibling for a while before this fires.
	if status == "running" {
		return s.withRetry("UpdateExecutionStatus", func() error {
			_, err := s.db.Exec(`
				UPDATE executions
				SET status = ?, error = COALESCE(?, error), started_at = CURRENT_TIMESTAMP
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

// UpdateExecutionStatusIfNotTerminal is UpdateExecutionStatus's CAS-guarded
// counterpart (GH-4423). It appends "AND status NOT IN (<terminalExecutionStatuses>)"
// to the WHERE clause, so the write only lands while the row is still
// non-terminal — once a row reaches a terminal status, this method can never
// silently overwrite it with another write.
//
// This closes the TOCTOU in the stale-running/stale-queued reapers
// (dispatcher.go): both gather evidence across several steps
// (HasCompletedExecution, live-worker check, merged-PR check for the running
// reap) and then used to write UpdateExecutionStatus(id, "failed", ...)
// blind — if the row reached a terminal status (e.g. completed) in the gap
// between evidence-gathering and that write, the completed row silently got
// stamped failed. Routed through this guard instead, that write is now
// rejected — logged at ERROR with both the attempted and actual status, so
// the #4404/#4457-class debugging trail has evidence instead of a vanished
// completion.
//
// Returns applied=false with err=nil when the guard rejected the write: this
// is not a failure for the caller to retry or escalate — the row already
// reached its true terminal state through another writer, and the rejection
// itself has already been logged here.
func (s *Store) UpdateExecutionStatusIfNotTerminal(id, status string, errorMsg ...string) (applied bool, err error) {
	var errStr *string
	if len(errorMsg) > 0 && errorMsg[0] != "" {
		errStr = &errorMsg[0]
	}

	var setClause string
	switch {
	case isTerminalExecutionStatus(status):
		setClause = "status = ?, error = COALESCE(?, error), completed_at = CURRENT_TIMESTAMP"
	case status == "running":
		setClause = "status = ?, error = COALESCE(?, error), started_at = CURRENT_TIMESTAMP"
	default:
		setClause = "status = ?, error = COALESCE(?, error)"
	}

	notTerminal, notTerminalArgs := notTerminalClause()

	err = s.withRetry("UpdateExecutionStatusIfNotTerminal", func() error {
		args := append([]interface{}{status, errStr, id}, notTerminalArgs...)
		result, execErr := s.db.Exec(`
			UPDATE executions
			SET `+setClause+`
			WHERE id = ? AND status NOT IN (`+notTerminal+`)
		`, args...)
		if execErr != nil {
			return execErr
		}
		affected, raErr := result.RowsAffected()
		if raErr != nil {
			return raErr
		}
		applied = affected > 0
		return nil
	})
	if err == nil && !applied {
		s.logRejectedTerminalWrite("UpdateExecutionStatusIfNotTerminal", id, status)
	}
	return applied, err
}

// logRejectedTerminalWrite logs the ERROR evidence trail for a CAS write
// rejected by UpdateExecutionStatusIfNotTerminal / MarkExecutionCompletedIfNotTerminal
// (GH-4423): both the attempted status and the row's actual current status,
// so a terminal->terminal collision leaves a trace instead of disappearing
// silently.
func (s *Store) logRejectedTerminalWrite(method, id, attemptedStatus string) {
	var current string
	if scanErr := s.db.QueryRow(`SELECT status FROM executions WHERE id = ?`, id).Scan(&current); scanErr != nil {
		slog.Error("CAS-guarded write rejected but failed to read current status for evidence (GH-4423)",
			slog.String("method", method), slog.String("execution_id", id),
			slog.String("attempted_status", attemptedStatus), slog.Any("error", scanErr))
		return
	}
	slog.Error("CAS-guarded write rejected: row already terminal (GH-4423)",
		slog.String("method", method), slog.String("execution_id", id),
		slog.String("attempted_status", attemptedStatus), slog.String("current_status", current))
}

// UpdateExecutionStatusByTaskID updates the status of the most recent execution
// for a given task ID and project path.
//
// GH-4243 dead-API audit: confirmed zero production call sites. It predates
// SelfHealExecutionAfterMerge, which replaced it as autopilot's merge-heal
// path (see internal/autopilot/controller.go's TestSelfHeal* suite, which
// asserts zero calls through the EvalStore.UpdateExecutionStatusByTaskID mock
// on the modern path). Kept — not removed — only because it's still part of
// the EvalStore interface contract; if that interface method is ever dropped,
// this can go with it.
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
//
// GH-4292: the status UPDATE and a terminal execution_events row per healed
// row (StageMerged when prURL is known, else StageFailed) commit in one
// transaction — candidates are selected first, then each is updated and
// logged via recordExecutionEventOn (never a hand-rolled INSERT) before the
// commit. The WHERE clause's status IN(...) set already excludes rows this
// method previously healed, so a repeat call against an already-healed row
// selects zero candidates and writes zero events (idempotent).
//
// GH-4292/GH-4277 backfill: also picks up rows already sitting at "completed"
// (healed by the pre-GH-4292 code, which flipped status but never wrote an
// event) whose execution_events ledger has no terminal entry — these render
// with a frozen HISTORY label (e.g. "running", "ci_passed") because the
// dashboard derives that text from the ledger, not executions.status. Such a
// row gets only the terminal event appended (its status/pr_url/completed_at
// are already correct and are left untouched); the "already terminal-status
// AND already has a terminal event" case remains excluded, so this is still
// idempotent across the periodic/startup catch-up sweeps that call this
// method unconditionally for every recently-merged PR.
func (s *Store) SelfHealExecutionAfterMerge(taskID, projectPath, prURL string) error {
	return s.withRetry("SelfHealExecutionAfterMerge", func() error {
		return healAndBackfillRows(s.db, `task_id = ? AND (? = '' OR project_path = ?) AND `+healOrBackfillCandidateClause,
			[]interface{}{taskID, projectPath, projectPath}, prURL,
			"self-heal after merge: "+orDefault(prURL, "no PR URL known"))
	})
}

// SelfHealExecutionByPRURL is the pr_url-keyed fallback for selfHealForPR
// (TASK-399/GH-4209): used when a merged PR's issue number can't be resolved
// from its branch name or body markers ('Closes #N' / 'Parent: GH-N'). It
// promotes any non-success row (same set as SelfHealExecutionAfterMerge)
// whose own already-stamped pr_url column equals prURL — covering a PR on a
// non-standard branch whose execution row still carries the PR URL from
// UpdateExecutionResult at creation time. No-op when prURL is empty.
//
// GH-4292: see SelfHealExecutionAfterMerge's doc comment — same transactional
// status UPDATE + terminal execution_events write, same completed-row ledger
// backfill, same idempotency argument.
func (s *Store) SelfHealExecutionByPRURL(prURL string) error {
	if prURL == "" {
		return nil
	}
	return s.withRetry("SelfHealExecutionByPRURL", func() error {
		return healAndBackfillRows(s.db, `pr_url = ? AND `+healOrBackfillCandidateClause,
			[]interface{}{prURL}, prURL, "self-heal by PR URL: "+prURL)
	})
}

// orDefault returns s unless it's empty, in which case it returns def — used
// to build a human-readable execution_events detail string for the no-PR-URL
// branch without a multi-line if/else at each call site.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// terminalEventStages is the set of execution_events stages that count as a
// "terminal" ledger entry for GH-4292's completed-row backfill check below —
// any of these already tells the dashboard the row reached an end state, so
// no backfill event is needed on top of it.
const terminalEventStages = `'merged', 'completed', 'failed', 'no_op', 'skipped', 'stalled'`

// healOrBackfillCandidateClause is shared by SelfHealExecutionAfterMerge and
// SelfHealExecutionByPRURL's WHERE clauses (GH-4292). It selects two disjoint
// groups, both scoped by the caller's own task_id/pr_url predicate:
//
//   - the original non-success set ("failed", "no_op", "stalled",
//     "rate_limited", "infra", "skipped") — genuine heal candidates, handled
//     unconditionally (unchanged from the pre-GH-4292 behavior).
//   - status = 'completed' rows with no terminal execution_events row yet —
//     the GH-4277 backfill-only case: status/pr_url are already correct, only
//     the ledger is missing its terminal entry.
//
// Deliberately excludes 'running'/'queued'/'pending' (in-flight — must never
// be force-completed here; see TestSelfHealExecutionAfterMerge_ExcludesRunningQueuedPending)
// and 'declined'/'cancelled' (an intentional non-outcome, not something a
// coincidental later merge should silently resolve).
const healOrBackfillCandidateClause = `(
	status IN ('failed', 'no_op', 'stalled', 'rate_limited', 'infra', 'skipped')
	OR (
		status = 'completed'
		AND NOT EXISTS (
			SELECT 1 FROM execution_events ee
			WHERE ee.execution_id = executions.id AND ee.stage IN (` + terminalEventStages + `)
		)
	)
)`

// healAndBackfillRows is the shared transaction body for SelfHealExecutionAfterMerge
// and SelfHealExecutionByPRURL (GH-4292/GH-4277): it selects every candidate execution
// row matching whereClause/args (healOrBackfillCandidateClause above), promotes each
// non-success row to "completed" (stamping pr_url when prURL is non-empty, matching
// the pre-GH-4292 UPDATE) — leaving already-'completed' rows' status/pr_url/completed_at
// untouched — and, in the same transaction, records one terminal execution_events row
// per row via recordExecutionEventOn: StageMerged when prURL is known, else StageFailed.
// Selecting candidates first (rather than a single bulk UPDATE) is what makes a per-row
// event possible.
func healAndBackfillRows(db *sql.DB, whereClause string, whereArgs []interface{}, prURL, detail string) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`SELECT id, status FROM executions WHERE `+whereClause, whereArgs...)
	if err != nil {
		return err
	}
	type candidate struct {
		id     string
		status string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.status); err != nil {
			_ = rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	stage := StageFailed
	if prURL != "" {
		stage = StageMerged
	}

	for _, c := range candidates {
		switch {
		case c.status != "completed":
			if _, err := tx.Exec(`
				UPDATE executions
				SET status = 'completed',
					error = '',
					completed_at = CURRENT_TIMESTAMP,
					pr_url = CASE WHEN ? <> '' THEN ? ELSE pr_url END
				WHERE id = ?
			`, prURL, prURL, c.id); err != nil {
				return err
			}
		case prURL != "":
			// GH-4511: the row is already 'completed' (GH-4277 backfill-only
			// case — only the ledger's terminal event is missing), but if its
			// own pr_url column is still empty, backfill it from the caller's
			// known prURL now rather than leaving it permanently blank. A
			// blank pr_url here excludes the row from
			// GetLifetimePRCountersFromExecutions's non-empty-pr_url filter
			// forever, silently desyncing the lifetime PR-merged baseline
			// from the live session counter that already counted this merge.
			if _, err := tx.Exec(`
				UPDATE executions
				SET pr_url = ?
				WHERE id = ? AND pr_url = ''
			`, prURL, c.id); err != nil {
				return err
			}
		}
		if err := recordExecutionEventOn(tx, c.id, stage, detail); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// HealFrozenHistoryLadders is GH-4368's one-shot dashboard-hydration heal. It
// scans the whole executions table once for every row already sitting at
// status='completed' whose execution_events ledger has no terminal entry —
// the same target as GH-4277's per-task backfill inside
// SelfHealExecutionAfterMerge/SelfHealExecutionByPRURL, except those two are
// keyed to one task_id or pr_url at a time and only ever get called for PRs
// still inside autopilot's bounded merged_pr_scan_window catch-up sweep. A row
// whose event stream died mid-incident (daemon killed before the terminal
// event was recorded) long before that window opened is otherwise
// permanently unreachable, so its HISTORY row stays frozen forever.
//
// Each candidate is stamped using its OWN already-known pr_url column
// (StageMerged) or, when none is set, StageCompleted — deliberately never
// StageFailed. Every candidate here is already a genuine 'completed' success;
// healAndBackfillRows' StageFailed-when-no-prURL branch exists for a
// different case (a non-success row with no merge evidence), and reusing it
// here would mislabel a real success red in buildStageInfo's reducer even
// though the row's icon (driven independently by executions.status via
// displayStatus) would still show ✓.
//
// completed_at/status/pr_url are left untouched, matching GH-4277. Idempotent:
// once a row has a terminal execution_events entry it drops out of the
// NOT EXISTS clause, so a second call selects zero candidates.
func (s *Store) HealFrozenHistoryLadders() (int, error) {
	var healed int
	err := s.withRetry("HealFrozenHistoryLadders", func() error {
		healed = 0
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		rows, err := tx.Query(`
			SELECT id, pr_url FROM executions
			WHERE status = 'completed' AND NOT EXISTS (
				SELECT 1 FROM execution_events ee
				WHERE ee.execution_id = executions.id AND ee.stage IN (` + terminalEventStages + `)
			)
		`)
		if err != nil {
			return err
		}
		type candidate struct{ id, prURL string }
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if err := rows.Scan(&c.id, &c.prURL); err != nil {
				_ = rows.Close()
				return err
			}
			candidates = append(candidates, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		for _, c := range candidates {
			stage, detail := StageCompleted, "historical archaeology heal (GH-4368): no PR URL on record"
			if c.prURL != "" {
				stage, detail = StageMerged, "historical archaeology heal (GH-4368): merged "+c.prURL
			}
			if err := recordExecutionEventOn(tx, c.id, stage, detail); err != nil {
				return err
			}
			healed++
		}

		return tx.Commit()
	})
	return healed, err
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

// GetExecutionStatusByTaskIDExcluding mirrors GetExecutionStatusByTaskID but
// ignores the row identified by excludeID (GH-4141, see
// GetLatestExecutionByTaskIDExcluding).
func (s *Store) GetExecutionStatusByTaskIDExcluding(taskID, projectPath, excludeID string) (string, error) {
	var status string
	err := s.db.QueryRow(`
		SELECT status FROM executions
		WHERE task_id = ? AND (? = '' OR project_path = ?) AND id != ?
		ORDER BY created_at DESC, rowid DESC
		LIMIT 1
	`, taskID, projectPath, projectPath, excludeID).Scan(&status)
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

// MarkExecutionCompletedIfNotTerminal is MarkExecutionCompleted's CAS-guarded
// counterpart (GH-4423), scoped to the same "AND status NOT IN
// (<terminalExecutionStatuses>)" guard as UpdateExecutionStatusIfNotTerminal.
// ExecutionLifecycle.Persist's success branch routes through this so a
// duplicate Finish call on an execution that already reached a terminal
// status (e.g. a racing writer already recorded "failed") cannot silently
// resurrect and overwrite it as "completed". Returns applied=false with
// err=nil when the guard rejected the write — the rejection is already
// logged (both attempted and actual status) by the time this returns.
func (s *Store) MarkExecutionCompletedIfNotTerminal(id, prURL, commitSHA string, durationMs int64) (applied bool, err error) {
	notTerminal, notTerminalArgs := notTerminalClause()

	err = s.withRetry("MarkExecutionCompletedIfNotTerminal", func() error {
		args := append([]interface{}{prURL, commitSHA, durationMs, id}, notTerminalArgs...)
		result, execErr := s.db.Exec(`
			UPDATE executions
			SET status = 'completed',
				pr_url = ?,
				commit_sha = ?,
				duration_ms = ?,
				completed_at = CURRENT_TIMESTAMP
			WHERE id = ? AND status NOT IN (`+notTerminal+`)
		`, args...)
		if execErr != nil {
			return execErr
		}
		affected, raErr := result.RowsAffected()
		if raErr != nil {
			return raErr
		}
		applied = affected > 0
		return nil
	})
	if err == nil && !applied {
		s.logRejectedTerminalWrite("MarkExecutionCompletedIfNotTerminal", id, "completed")
	}
	return applied, err
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

// UpdateExecutionTitle backfills task_title for an execution row (GH-4280,
// mirroring the pr_title persistence pattern from GH-4080). No-op when title
// is empty so a caller that hasn't resolved a title yet can't clobber one
// already persisted by an earlier write.
func (s *Store) UpdateExecutionTitle(executionID, title string) error {
	if title == "" {
		return nil
	}
	return s.withRetry("UpdateExecutionTitle", func() error {
		_, err := s.db.Exec(`
			UPDATE executions
			SET task_title = ?
			WHERE id = ?
		`, title, executionID)
		return err
	})
}

// GetStaleRunningExecutions returns executions that have been in "running" status
// for longer than the specified duration. Used to detect crashed workers on restart.
//
// GH-4033: staleness is measured from started_at (when the worker actually began
// running this execution), NOT created_at (when the row was queued/decomposed).
// A decomposed subtask's row is created at decomposition time but can legitimately
// sit queued behind a sibling for a while before the worker picks it up; timing
// from created_at evicted such subtasks as "stuck" while they were still actively
// running. COALESCE falls back to created_at for rows written before this column
// existed (started_at is NULL) or rows inserted directly with status='running'
// (bypassing UpdateExecutionStatus, e.g. in tests).
func (s *Store) GetStaleRunningExecutions(staleDuration time.Duration) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, started_at, completed_at, COALESCE(task_branch, '')
		FROM executions
		WHERE status = 'running'
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var startedAt sql.NullTime
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &startedAt, &completedAt, &exec.TaskBranch); err != nil {
			return nil, err
		}
		if startedAt.Valid {
			exec.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return filterAndSortStale(executions, staleDuration), nil
}

// GetStaleQueuedExecutions returns executions that have been in "queued" status
// for longer than the specified duration. Used to detect stuck queue entries.
func (s *Store) GetStaleQueuedExecutions(staleDuration time.Duration) ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT id, task_id, project_path, status, output, error, duration_ms, pr_url, commit_sha, created_at, completed_at
		FROM executions
		WHERE status = 'queued'
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return filterAndSortStale(executions, staleDuration), nil
}

// staleReference returns the timestamp GetStaleRunningExecutions and
// GetStaleQueuedExecutions measure staleness from: started_at for a running
// row (GH-4033 — a decomposed subtask can sit queued behind a sibling
// legitimately before a worker picks it up), falling back to created_at when
// unset (queued rows, or running rows written before started_at existed).
func staleReference(exec *Execution) time.Time {
	if exec.StartedAt != nil {
		return *exec.StartedAt
	}
	return exec.CreatedAt
}

// filterAndSortStale filters execs to those older than staleDuration and
// sorts the result oldest-first, matching the callers' prior `ORDER BY ...
// ASC` SQL clause.
//
// GH-4392: staleness is decided by comparing driver-parsed time.Time values
// in Go, not by a `created_at < ?` SQL string-range predicate. Every row's
// created_at is scanned into a time.Time above regardless of the on-disk
// text layout, but rows written before the DSN's `_time_format=sqlite` fix
// (GH-4332/#4345) can carry a different text layout than rows written after
// it. Lexicographic SQL comparison across those two layouts is not
// guaranteed to match chronological order, so a `WHERE created_at < ?`
// predicate can silently drop legacy-format rows from the result set (no
// error — they just never come back). That is exactly how the stale
// recovery sweep logged "reset 0 tasks" straight through the GH-4392
// incident while 5 dead-owner queued rows sat unrecovered. Comparing already
// -parsed time.Time values sidesteps the on-disk format entirely.
func filterAndSortStale(execs []*Execution, staleDuration time.Duration) []*Execution {
	cutoff := time.Now().Add(-staleDuration)
	stale := make([]*Execution, 0, len(execs))
	for _, exec := range execs {
		if staleReference(exec).Before(cutoff) {
			stale = append(stale, exec)
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		return staleReference(stale[i]).Before(staleReference(stale[j]))
	})
	return stale
}

// GetClaimedNonTerminalExecutions returns every execution currently in a
// non-terminal, in-flight status ("queued" or "running") that also holds at
// least one execution_claims row naming it — i.e. every row created through
// the real ExecutionLifecycle.Begin/ClaimExecution path, as opposed to a
// direct executions-table insert. No time filter applies.
//
// Used exclusively by the dispatcher's boot-time orphan reconciliation
// (GH-4392): Dispatcher.Start calls this before any worker exists for this
// process, so under the single-daemon invariant (H7/#4311) every row it
// returns necessarily predates — and was left behind by — a prior, now-dead
// daemon process. No duration threshold applies because none is needed: at
// boot, "non-terminal and claimed" already implies "orphaned, holding a dead
// claim."
//
// Scoped to claimed rows specifically (the JOIN) rather than every
// non-terminal row: nextRetryGeneration's dead-claim blind spot is what
// GH-4392 fixes, and only a row with an execution_claims entry can be
// blocking a future dispatch attempt that way. A non-terminal row with no
// claim was never inserted through the claim path — GH-3732's queued-project
// restart adoption still owns getting it a worker, unchanged by this fix.
func (s *Store) GetClaimedNonTerminalExecutions() ([]*Execution, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT e.id, e.task_id, e.project_path, e.status, e.output, e.error, e.duration_ms, e.pr_url, e.commit_sha, e.created_at, e.started_at, e.completed_at, COALESCE(e.task_branch, '')
		FROM executions e
		JOIN execution_claims c ON c.execution_id = e.id
		WHERE e.status IN ('queued', 'running')
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var executions []*Execution
	for rows.Next() {
		var exec Execution
		var startedAt sql.NullTime
		var completedAt sql.NullTime
		if err := rows.Scan(&exec.ID, &exec.TaskID, &exec.ProjectPath, &exec.Status, &exec.Output, &exec.Error, &exec.DurationMs, &exec.PRUrl, &exec.CommitSHA, &exec.CreatedAt, &startedAt, &completedAt, &exec.TaskBranch); err != nil {
			return nil, err
		}
		if startedAt.Valid {
			exec.StartedAt = &startedAt.Time
		}
		if completedAt.Valid {
			exec.CompletedAt = &completedAt.Time
		}
		executions = append(executions, &exec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort in Go (not SQL ORDER BY created_at) for the same reason
	// filterAndSortStale does — legacy-format rows must not be misordered by
	// a raw string comparison.
	sort.Slice(executions, func(i, j int) bool {
		return staleReference(executions[i]).Before(staleReference(executions[j]))
	})

	return executions, nil
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

// HydrationTask is a minimal projection of a non-terminal executions row,
// just enough to reconstruct executor.Monitor state after a daemon restart
// (GH-4246). See GetTasksForMonitorHydration.
type HydrationTask struct {
	TaskID    string
	Status    string // "queued" | "pending" | "running"
	Title     string
	IssueURL  string
	StartedAt *time.Time
}

// GetTasksForMonitorHydration returns queued/pending/running executions
// across all projects, ordered oldest-first. The executor.Monitor is
// otherwise populated only at dispatch-intake time (the sole Register
// caller is cmd/pilot/handler_common.go), so a daemon restart wipes it even
// though these rows are still active in the DB — this backs the startup
// hydration that rebuilds the monitor from persisted state (GH-4246).
func (s *Store) GetTasksForMonitorHydration() ([]*HydrationTask, error) {
	rows, err := s.db.Query(`
		SELECT task_id, status, COALESCE(task_title, ''), COALESCE(task_source_issue_id, ''), started_at
		FROM executions
		WHERE status IN ('queued', 'pending', 'running')
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var tasks []*HydrationTask
	for rows.Next() {
		var t HydrationTask
		var startedAt sql.NullTime
		if err := rows.Scan(&t.TaskID, &t.Status, &t.Title, &t.IssueURL, &startedAt); err != nil {
			return nil, err
		}
		if startedAt.Valid {
			t.StartedAt = &startedAt.Time
		}
		tasks = append(tasks, &t)
	}
	return tasks, rows.Err()
}

// CountQueuedTasks returns the number of queued/pending execution rows — the
// DB-backed source for the pilot_queue_depth gauge (GH-4246). A cheap COUNT
// query, safe to call from a frequent (e.g. 2s) dashboard refresh loop.
func (s *Store) CountQueuedTasks() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM executions WHERE status = 'queued' OR status = 'pending'`).Scan(&n)
	return n, err
}

// DeleteExecution removes an execution row by ID. Used to clean up orphan rows
// when the same task already has a completed execution.
func (s *Store) DeleteExecution(id string) error {
	_, err := s.db.Exec("DELETE FROM executions WHERE id = ?", id)
	return err
}

// IsTaskQueued checks if a task with the given ID is already queued or running
// in projectPath. Used to prevent duplicate task submissions.
//
// GH-4276: scoped by project_path — task_id is not unique across projects
// (e.g. every freshly onboarded repo starts issue numbering at #1), so an
// unscoped lookup could see a same-numbered task actively queued/running in
// a different project and wrongly treat this project's dispatch as a
// duplicate.
func (s *Store) IsTaskQueued(taskID, projectPath string) (bool, error) {
	var count int
	// GH-4540/TASK-421: 'decomposed' was missing from this allowlist even
	// though it is a non-terminal status (see executor.terminalExecutionStatuses)
	// — an epic parent sitting "decomposed" was invisible to this check (and
	// thus to Dispatcher.IsActive's pre-dispatch admission gate) while still
	// being a live, non-terminal claim owner from nextRetryGeneration's point
	// of view, letting the poller re-offer it on every tick and generate an
	// unbounded run of claim-lost drops.
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM executions
		WHERE task_id = ? AND project_path = ? AND status IN ('queued', 'pending', 'running', 'decomposed')
	`, taskID, canonicalizeProjectPath(projectPath)).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// canonicalizeProjectPath normalizes projectPath for use as a discriminator
// key (execution_claims, IsTaskQueued's active-task lookup), resolving
// symlinks and collapsing a trailing separator / "."/".." segments so two
// textually-different-but-equivalent paths for the same project
// (mem-019/GH-4297's discriminator-mismatch class) resolve to the same key
// instead of silently splitting it. filepath.Clean runs first so
// EvalSymlinks sees a normalized input; if EvalSymlinks fails (path doesn't
// exist yet, e.g. a project directory not yet checked out), it falls back to
// the cleaned form rather than erroring, mirroring worktree.go's
// resolvedRepoPath tolerance for the same OS-level symlink quirk (macOS
// /var vs /private/var).
func canonicalizeProjectPath(projectPath string) string {
	if projectPath == "" {
		return projectPath
	}
	cleaned := filepath.Clean(projectPath)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

// ClaimExecution atomically claims (task_id, project_path, generation) for
// executionID via INSERT OR IGNORE + RowsAffected()==1 — the ClaimSpawnedFix
// idiom (internal/autopilot/state_store.go:1062) generalized to dispatch
// admission (TASK-407/GH-4349). Returns claimed=true only when THIS call
// performed the insert; a second caller racing the same key (any goroutine,
// any process — SQLite serializes it) loses and must not begin its own
// execution. Rows are permanent per generation: a legitimate retry claims
// generation+1 rather than reusing this one.
func (s *Store) ClaimExecution(taskID, projectPath string, generation int, executionID string) (bool, error) {
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO execution_claims (task_id, project_path, generation, execution_id)
		VALUES (?, ?, ?, ?)
	`, taskID, canonicalizeProjectPath(projectPath), generation, executionID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// LatestClaimGeneration returns the highest generation currently claimed for
// (taskID, projectPath) in execution_claims, and the execution_id that
// generation claimed. found is false when no claim row exists at all — the
// normal state for a task that has never been dispatched.
//
// GH-4372: the poller's re-pick path uses this to distinguish a claim held by
// a still-live execution (queued/running — the ErrClaimLost duplicate-pickup
// case, must keep dropping silently) from one held by a dead, terminal
// execution (failed/stalled/etc. — a legitimate retry candidate that should
// claim generation+1) instead of retrying generation 0 forever.
func (s *Store) LatestClaimGeneration(taskID, projectPath string) (generation int, executionID string, found bool, err error) {
	err = s.db.QueryRow(`
		SELECT generation, execution_id FROM execution_claims
		WHERE task_id = ? AND project_path = ?
		ORDER BY generation DESC
		LIMIT 1
	`, taskID, canonicalizeProjectPath(projectPath)).Scan(&generation, &executionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", false, nil
		}
		return 0, "", false, err
	}
	return generation, executionID, true, nil
}

// GetRepickBackoff returns the persisted repick-backoff cooldown state for
// key (a "project_path|task_id" string minted by cmd/pilot's
// repickBackoffKey). found is false when no drop has ever been recorded for
// key, or its state was cleared by ClearRepickBackoff after a successful
// dispatch — the normal "not throttled" case (GH-4394).
func (s *Store) GetRepickBackoff(key string) (consecutiveDrops int, nextAllowedAt time.Time, found bool, err error) {
	err = s.db.QueryRow(`
		SELECT consecutive_drops, next_allowed_at FROM repick_backoff WHERE key = ?
	`, key).Scan(&consecutiveDrops, &nextAllowedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, time.Time{}, false, nil
		}
		return 0, time.Time{}, false, err
	}
	return consecutiveDrops, nextAllowedAt, true, nil
}

// SetRepickBackoff persists a consecutive-drop count and cooldown deadline
// for key, replacing whatever was stored before (GH-4394). The caller (the
// in-process repickBackoffTracker) owns the exponential-growth policy; this
// is a plain upsert of whatever it computed.
func (s *Store) SetRepickBackoff(key string, consecutiveDrops int, nextAllowedAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO repick_backoff (key, consecutive_drops, next_allowed_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE SET
			consecutive_drops = excluded.consecutive_drops,
			next_allowed_at = excluded.next_allowed_at,
			updated_at = CURRENT_TIMESTAMP
	`, key, consecutiveDrops, nextAllowedAt)
	return err
}

// ClearRepickBackoff removes any cooldown state for key (GH-4394) — called
// once a dispatch for the task actually proceeds, so the next drop, if any,
// starts a fresh backoff sequence rather than continuing to escalate.
func (s *Store) ClearRepickBackoff(key string) error {
	_, err := s.db.Exec(`DELETE FROM repick_backoff WHERE key = ?`, key)
	return err
}

// GetStallDropCount returns the persisted count of consecutive stall-kill
// drops for key (same "project_path|task_id" string repickBackoffKey mints).
// found is false when no stall drop has ever been recorded for key — the
// normal case for a task that has never had a stall-watchdog kill (GH-4502).
// Distinct from consecutive_drops on the same row: that counter tracks
// genuine (non-stall) drops toward dispatcherRepickHardCap, this one tracks
// stall-kills toward the separate, higher dispatcherStallRepickCap.
func (s *Store) GetStallDropCount(key string) (count int, found bool, err error) {
	err = s.db.QueryRow(`
		SELECT stall_drops FROM repick_backoff WHERE key = ?
	`, key).Scan(&count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return count, true, nil
}

// SetStallDropCount persists count as the stall-kill drop count for key
// (GH-4502), creating the repick_backoff row if key has no prior state.
// consecutive_drops/next_allowed_at are only supplied for a brand-new row
// (both effectively "never throttled") — an existing row's genuine-failure
// counter and backoff window are left untouched by the ON CONFLICT clause,
// since a stall-kill must not perturb genuine-failure accounting.
func (s *Store) SetStallDropCount(key string, count int) error {
	_, err := s.db.Exec(`
		INSERT INTO repick_backoff (key, consecutive_drops, next_allowed_at, stall_drops, updated_at)
		VALUES (?, 0, CURRENT_TIMESTAMP, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE SET
			stall_drops = excluded.stall_drops,
			updated_at = CURRENT_TIMESTAMP
	`, key, count)
	return err
}

// GetInfraDropCount returns the persisted count of consecutive
// infra-classified repick drops for key (GH-4540/TASK-421) — the same
// "project_path|task_id" string repickBackoffKey mints. found is false when
// no infra drop has ever been recorded for key. Distinct from
// consecutive_drops on the same row: that counter tracks genuine (non-infra,
// non-stall) drops toward dispatcherRepickHardCap; this one tracks
// infra-classified repicks toward the separate dispatcherInfraRepickCap.
func (s *Store) GetInfraDropCount(key string) (count int, found bool, err error) {
	err = s.db.QueryRow(`
		SELECT infra_drops FROM repick_backoff WHERE key = ?
	`, key).Scan(&count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return count, true, nil
}

// SetInfraDropCount persists count as the infra-classified drop count for
// key (GH-4540/TASK-421), creating the repick_backoff row if key has no
// prior state. consecutive_drops/next_allowed_at are only supplied for a
// brand-new row — an existing row's genuine-failure counter and backoff
// window are left untouched by the ON CONFLICT clause, since an
// infra-classified repick must not perturb genuine-failure accounting.
func (s *Store) SetInfraDropCount(key string, count int) error {
	_, err := s.db.Exec(`
		INSERT INTO repick_backoff (key, consecutive_drops, next_allowed_at, infra_drops, updated_at)
		VALUES (?, 0, CURRENT_TIMESTAMP, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE SET
			infra_drops = excluded.infra_drops,
			updated_at = CURRENT_TIMESTAMP
	`, key, count)
	return err
}

// GetClaimLostDropCount returns the persisted count of consecutive
// claim-lost/already-done drops for key (GH-4540/TASK-421) — dropped pickups
// that cmd/pilot's handleIssueGeneric chokepoint refused because the task
// was already active or already terminally done, not because anything
// failed. Distinct from consecutive_drops: that counter only grows for a
// genuine failed re-pick (see Dispatcher.beginWithGenerationRetry); this one
// exists purely for backoff-window growth and observability (WARN
// escalation, loop-breaker alert) so a task queued behind another task
// indefinitely is never pushed toward dispatcherRepickHardCap.
func (s *Store) GetClaimLostDropCount(key string) (count int, found bool, err error) {
	err = s.db.QueryRow(`
		SELECT claim_lost_drops FROM repick_backoff WHERE key = ?
	`, key).Scan(&count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return count, true, nil
}

// SetClaimLostBackoff persists claimLostDrops and the backoff cooldown
// deadline for key (GH-4540/TASK-421), creating the repick_backoff row if
// key has no prior state. Unlike SetRepickBackoff, this intentionally never
// touches consecutive_drops — growing the shared cooldown window (so a
// repeatedly-re-offered task still gets throttled the way TASK-413 intends)
// must not also push the task toward the genuine-failure hard cap.
func (s *Store) SetClaimLostBackoff(key string, claimLostDrops int, nextAllowedAt time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO repick_backoff (key, consecutive_drops, next_allowed_at, claim_lost_drops, updated_at)
		VALUES (?, 0, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE SET
			next_allowed_at = excluded.next_allowed_at,
			claim_lost_drops = excluded.claim_lost_drops,
			updated_at = CURRENT_TIMESTAMP
	`, key, nextAllowedAt, claimLostDrops)
	return err
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
// GH-4735: canary sandbox executions are excluded, matching every other
// lifetime aggregate (GetLifetimeTaskCounts, GetIssueLevelCounts, etc.) —
// this was the only lifetime aggregate the GH-4240/TASK-436 canary-filter
// wave missed.
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
		WHERE tokens_total > 0 AND COALESCE(is_canary, 0) = 0`
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
	// GH-4240: canary sandbox executions never count toward these lifetime
	// baselines, project-scoped or not.
	if projectPath != "" {
		row = s.db.QueryRow(cols+` WHERE COALESCE(is_canary, 0) = 0 AND project_path = ?`, projectPath)
	} else {
		row = s.db.QueryRow(cols + ` WHERE COALESCE(is_canary, 0) = 0`)
	}

	var tc LifetimeTaskCounts
	if err := row.Scan(&tc.Total, &tc.Succeeded, &tc.Failed, &tc.Declined, &tc.NoOp, &tc.Stalled,
		&tc.RateLimited, &tc.Infra, &tc.Skipped); err != nil {
		return nil, fmt.Errorf("failed to get lifetime task counts: %w", err)
	}
	return &tc, nil
}

// WindowedStats holds cost/success stats over a rolling time window
// (GH-4735), replacing the lifetime-flavored headline numbers that blend
// model eras. All fields are computed from a single population — every
// execution row with created_at >= since, COALESCE(is_canary, 0) = 0, and
// (if projectPath is non-empty) project_path = projectPath — so unlike
// GetLifetimeTokens/GetLifetimeTaskCounts (mismatched populations: one
// filters tokens_total, the other doesn't) there is no cross-aggregate
// population drift.
//
// "Issue" below means one distinct (task_id, project_path) pair, deduped
// across retry attempts (mirrors IssueLevelCounts). IssuesAttempted counts
// distinct issues having >= 1 execution with status IN ('completed',
// 'failed') in the window — this is a deliberate simplification: an issue
// whose only window activity is a neutral terminal status (no_op, infra,
// skipped, declined, stalled, rate_limited) counts nowhere (not attempted,
// not delivered), since none of those outcomes represent a real attempt at
// shipping. TotalCostUSD sums estimated_cost_usd across ALL executions in
// the window regardless of status — a failed retry that burned tokens is
// real spend and must not be dropped from the cost total.
type WindowedStats struct {
	WindowDays int

	TotalCostUSD     float64 // SUM(estimated_cost_usd), all executions in window (canary-excluded)
	IssuesAttempted  int     // distinct issues with >= 1 completed-or-failed execution in window
	IssuesDelivered  int     // distinct issues with >= 1 completed execution in window
	CostPerDelivered float64 // TotalCostUSD / IssuesDelivered, 0 when IssuesDelivered == 0

	AttemptCompleted   int     // executions with status = 'completed' in window
	AttemptFailed      int     // executions with status = 'failed' in window
	AttemptSuccessRate float64 // AttemptCompleted / (AttemptCompleted + AttemptFailed), 0 when both are 0
	DeliveryRate       float64 // IssuesDelivered / IssuesAttempted, 0 when IssuesAttempted == 0

	// AttemptTotal and the neutral-status buckets below give the TUI queue
	// card's windowed 9-way breakdown (mirrors LifetimeTaskCounts), computed
	// in the same query/population as everything above. These are per-attempt
	// row counts, not deduped-by-issue like IssuesAttempted/IssuesDelivered.
	AttemptTotal       int // all executions in window (canary-excluded), any status
	AttemptDeclined    int
	AttemptNoOp        int
	AttemptStalled     int
	AttemptRateLimited int
	AttemptInfra       int
	AttemptSkipped     int
}

// GetWindowedStats returns cost/success stats for executions with
// created_at >= since. If projectPath is non-empty, only executions for
// that project are counted. See WindowedStats for the exact population and
// neutral-status handling. GH-4735: replaces lifetime headline numbers,
// which blend model eras and mismatch aggregate populations.
func (s *Store) GetWindowedStats(projectPath string, since time.Time) (WindowedStats, error) {
	const cols = `
		SELECT
			COALESCE(SUM(estimated_cost_usd), 0),
			COUNT(DISTINCT CASE WHEN status IN ('completed', 'failed') THEN task_id || '|' || project_path END),
			COUNT(DISTINCT CASE WHEN status = 'completed' THEN task_id || '|' || project_path END),
			COUNT(*),
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'declined' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'no_op' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'stalled' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'rate_limited' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'infra' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'skipped' THEN 1 ELSE 0 END), 0)
		FROM executions
		WHERE created_at >= ? AND COALESCE(is_canary, 0) = 0`

	var row *sql.Row
	if projectPath != "" {
		row = s.db.QueryRow(cols+` AND project_path = ?`, since, projectPath)
	} else {
		row = s.db.QueryRow(cols, since)
	}

	var ws WindowedStats
	if err := row.Scan(&ws.TotalCostUSD, &ws.IssuesAttempted, &ws.IssuesDelivered, &ws.AttemptTotal,
		&ws.AttemptCompleted, &ws.AttemptFailed, &ws.AttemptDeclined, &ws.AttemptNoOp,
		&ws.AttemptStalled, &ws.AttemptRateLimited, &ws.AttemptInfra, &ws.AttemptSkipped); err != nil {
		return WindowedStats{}, fmt.Errorf("failed to get windowed stats: %w", err)
	}

	if ws.IssuesDelivered > 0 {
		ws.CostPerDelivered = ws.TotalCostUSD / float64(ws.IssuesDelivered)
	}
	if ws.IssuesAttempted > 0 {
		ws.DeliveryRate = float64(ws.IssuesDelivered) / float64(ws.IssuesAttempted)
	}
	if attemptTotal := ws.AttemptCompleted + ws.AttemptFailed; attemptTotal > 0 {
		ws.AttemptSuccessRate = float64(ws.AttemptCompleted) / float64(attemptTotal)
	}

	return ws, nil
}

// IssueLevelCounts holds unique-issue outcome counts, deduped by task_id
// across retry attempts. Contrast with LifetimeTaskCounts, which counts every
// executions row (one row per attempt) — a task retried twice before
// shipping contributes 1 to IssueLevelCounts.Shipped but 3 rows (2 failed +
// 1 completed) to LifetimeTaskCounts. TASK-392: this is what "did the issue
// eventually ship" should be measured against, not per-attempt totals.
type IssueLevelCounts struct {
	Attempted int // distinct task_id with at least one execution row
	Shipped   int // distinct task_id with at least one 'completed' execution row
}

// GetIssueLevelCounts returns unique-issue attempt/ship counts, deduped by
// task_id. If projectPath is non-empty, only executions for that project are
// counted. TASK-392.
func (s *Store) GetIssueLevelCounts(projectPath string) (*IssueLevelCounts, error) {
	const cols = `
		SELECT
			COUNT(DISTINCT task_id),
			COUNT(DISTINCT CASE WHEN status = 'completed' THEN task_id END)
		FROM executions`
	var row *sql.Row
	// GH-4240: canary sandbox executions never count toward issue-level
	// attempted/shipped baselines, project-scoped or not.
	if projectPath != "" {
		row = s.db.QueryRow(cols+` WHERE COALESCE(is_canary, 0) = 0 AND project_path = ?`, projectPath)
	} else {
		row = s.db.QueryRow(cols + ` WHERE COALESCE(is_canary, 0) = 0`)
	}

	var c IssueLevelCounts
	if err := row.Scan(&c.Attempted, &c.Shipped); err != nil {
		return nil, fmt.Errorf("failed to get issue-level counts: %w", err)
	}
	return &c, nil
}

// IssueLevelModelCounts holds unique-issue attempt/ship counts for one model,
// deduped by task_id within that model — the per-model counterpart to
// IssueLevelCounts. GH-4483: the attempt-level pilot_executions_total{model,
// result} counter charges every retry/rate-limit death to its model, so a
// task that failed twice on claude-sonnet-5 before shipping on the third
// attempt reads as 1 success / 2 failures (33%) even though the issue
// eventually shipped. This pairs with GetIssueLevelCounts to answer "did
// issues on this model eventually ship" instead of "how many attempts on
// this model succeeded".
type IssueLevelModelCounts struct {
	Model     string
	Attempted int // distinct task_id with at least one execution row on this model
	Shipped   int // distinct task_id with at least one 'completed' execution row on this model
}

// GetIssueLevelCountsByModel returns unique-issue attempt/ship counts broken
// out by model_name, deduped by task_id within each model bucket. If
// projectPath is non-empty, only executions for that project are counted.
// Rows with an empty model_name are excluded rather than bucketed under
// "unknown" — see GetLifetimeCounterBaselines for why. GH-4483.
func (s *Store) GetIssueLevelCountsByModel(projectPath string) ([]IssueLevelModelCounts, error) {
	const cols = `
		SELECT
			model_name,
			COUNT(DISTINCT task_id),
			COUNT(DISTINCT CASE WHEN status = 'completed' THEN task_id END)
		FROM executions
		WHERE COALESCE(is_canary, 0) = 0 AND model_name IS NOT NULL AND model_name != ''`

	var rows *sql.Rows
	var err error
	// GH-4240: canary sandbox executions never count toward issue-level
	// attempted/shipped baselines, project-scoped or not.
	if projectPath != "" {
		rows, err = s.db.Query(cols+` AND project_path = ? GROUP BY model_name`, projectPath)
	} else {
		rows, err = s.db.Query(cols + ` GROUP BY model_name`)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get issue-level counts by model: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []IssueLevelModelCounts
	for rows.Next() {
		var c IssueLevelModelCounts
		if err := rows.Scan(&c.Model, &c.Attempted, &c.Shipped); err != nil {
			return nil, fmt.Errorf("failed to scan issue-level model count row: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate issue-level model counts: %w", err)
	}
	return out, nil
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
// GH-4483: the execution "result" label used to collapse the executions
// table's richer status vocabulary (completed/failed/declined/no_op/stalled/
// rate_limited/infra/skipped) down to just "success"/"stalled"/"failed" —
// mirroring what the live RecordExecution() call sites happened to emit at
// the time. That made every non-failure terminal outcome (declined, no_op,
// rate_limited, infra, skipped) read as a genuine "failed" in the per-model
// panel, the same defect TASK-392/#4070 fixed for the headline
// pilot_success_rate. This now preserves the full taxonomy instead, mirroring
// that fix: each status gets its own result label. The live per-event
// counter still only ever emits a narrower set of labels between restarts
// (see runner.go), but the store-hydrated baseline — which is what dashboards
// and this daemon's own restarts read — is now accurate, and the label set
// widens over time as the daemon restarts and re-hydrates.
//
// Rows with an empty model_name are excluded entirely rather than bucketed
// under "unknown": they are pre-GH-4041 rows or executions that died before
// invoking Claude, and carry zero tokens (verified: SUM(tokens_input+
// tokens_output)=0 for every such row), so they are not a real "model" and
// only pollute the per-model panel.
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
		WHERE tokens_total > 0 AND COALESCE(is_canary, 0) = 0
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
			model_name,
			CASE
				WHEN status = 'completed' THEN 'success'
				WHEN status = 'declined' THEN 'declined'
				WHEN status = 'no_op' THEN 'no_op'
				WHEN status = 'stalled' THEN 'stalled'
				WHEN status = 'rate_limited' THEN 'rate_limited'
				WHEN status = 'infra' THEN 'infra'
				WHEN status = 'skipped' THEN 'skipped'
				ELSE 'failed'
			END AS result,
			COUNT(*)
		FROM executions
		WHERE status NOT IN ('queued', 'pending', 'running')
			AND COALESCE(is_canary, 0) = 0
			AND model_name IS NOT NULL AND model_name != ''
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

// LifetimePRCounters holds lifetime PR-family counter baselines derived from
// the durable execution_events ledger (GH-4093, follow-up to GH-4029/GH-4041).
// PR #4043 deliberately skipped hydrating pilot_prs_merged_total/
// pilot_prs_failed_total on the premise that no persisted state survives a
// restart — that premise was wrong for these two: autopilot_pr_state rows are
// deleted on PR completion (controller.go persistRemovePR), but every
// merged/failed transition is also durably logged to execution_events
// (GH-3844/TASK-379 C3), which is append-only and never pruned.
type LifetimePRCounters struct {
	Merged int64
	Failed int64
}

// GetLifetimePRCounters queries execution_events for lifetime pilot_prs_merged_total
// / pilot_prs_failed_total baselines, deduped per execution_id (COUNT(DISTINCT),
// not COUNT(*)) so a stage logged more than once for the same execution is not
// double counted.
//
// GH-4121: no longer the metrics_hydrator source for these two counters — the
// ledger only goes back to its TASK-379/GH-3844 introduction and undercounts
// against every other lifetime counter, which all hydrate all-time from the
// executions table. See GetLifetimePRCountersFromExecutions, the current
// hydration source. Kept here (still covered by TestGetLifetimePRCounters)
// as the more precise event-level source, available if a future caller needs
// per-execution rather than per-task granularity.
//
// "failed" is ambiguous in the raw ledger: the executor writes stage='failed'
// for executions that never produced a PR (Claude Code failed before any PR
// existed — internal/executor/runner.go, dispatcher.go), while the autopilot
// controller writes stage='failed' for PRs that failed after being created
// (CI failure, merge failure, release failure — controller.go
// executionEventStageFor). Both share the same execution_events.stage value,
// so counting all 'failed' rows would conflate "task never shipped a PR" with
// "PR-family failure" and wildly overcount pilot_prs_failed_total (most
// executions fail during coding, not PR review). Restricting to execution_ids
// that also have a 'pr_created' row scopes both merged and failed to genuine
// PR outcomes, matching what RecordPRMerged/RecordPRFailed count live.
func (s *Store) GetLifetimePRCounters() (*LifetimePRCounters, error) {
	counters := &LifetimePRCounters{}

	rows, err := s.db.Query(`
		SELECT stage, COUNT(DISTINCT execution_id)
		FROM execution_events
		WHERE stage IN (?, ?)
			AND execution_id IN (
				SELECT execution_id FROM execution_events WHERE stage = ?
			)
		GROUP BY stage
	`, string(StageMerged), string(StageFailed), string(StagePRCreated))
	if err != nil {
		return nil, fmt.Errorf("failed to get lifetime PR counters: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var stage string
		var count int64
		if err := rows.Scan(&stage, &count); err != nil {
			return nil, fmt.Errorf("failed to scan lifetime PR counter row: %w", err)
		}
		switch Stage(stage) {
		case StageMerged:
			counters.Merged = count
		case StageFailed:
			counters.Failed = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lifetime PR counters: %w", err)
	}

	return counters, nil
}

// GetLifetimePRCountersFromExecutions computes lifetime pilot_prs_merged_total /
// pilot_prs_failed_total baselines from the executions table instead of the
// execution_events ledger (GH-4121, follow-up to GH-4093). The ledger only
// goes back to its TASK-379/GH-3844 introduction, so it undercounts merged/
// failed PRs by ~20x against every other lifetime counter — all of which
// (pilot_issues_shipped_total, pilot_execution_cost_usd_total, token totals)
// hydrate all-time from executions. This gives the PR-outcome counters the
// same all-time population.
//
// Both counts dedupe by task_id (a task retried across several execution rows
// contributes at most once), mirroring GetIssueLevelCounts.Shipped:
//   - Merged: distinct task_id with a 'completed' row carrying a PR URL — the
//     same "shipped-with-PR" population issues_shipped already treats as
//     honest.
//   - Failed: distinct task_id with a 'failed' row carrying a PR URL (a
//     genuine PR-family failure — CI/merge/release failed after a PR
//     existed), EXCLUDING any task_id that also has a merged row. Without
//     that exclusion a task that failed once and then shipped on retry would
//     count in both buckets, double counting the same task_id across the two
//     metrics.
//
// If projectPath is non-empty, only executions for that project are counted
// (matches GetIssueLevelCounts).
func (s *Store) GetLifetimePRCountersFromExecutions(projectPath string) (*LifetimePRCounters, error) {
	// GH-4240: is_canary = 0 excludes the canary sandbox from both the outer
	// count and the "already merged elsewhere" exclusion subquery — otherwise
	// a canary task_id colliding with the subquery's scope could leak in.
	const cols = `
		SELECT
			COUNT(DISTINCT CASE WHEN status = 'completed' AND pr_url <> '' THEN task_id END),
			COUNT(DISTINCT CASE
				WHEN status = 'failed' AND pr_url <> '' AND task_id NOT IN (
					SELECT task_id FROM executions
					WHERE status = 'completed' AND pr_url <> '' AND COALESCE(is_canary, 0) = 0 AND (? = '' OR project_path = ?)
				) THEN task_id
			END)
		FROM executions
		WHERE COALESCE(is_canary, 0) = 0 AND (? = '' OR project_path = ?)`

	row := s.db.QueryRow(cols, projectPath, projectPath, projectPath, projectPath)

	counters := &LifetimePRCounters{}
	if err := row.Scan(&counters.Merged, &counters.Failed); err != nil {
		return nil, fmt.Errorf("failed to get lifetime PR counters from executions: %w", err)
	}
	return counters, nil
}

// LifetimeCIRunCounters holds lifetime pilot_ci_runs_total{result} baselines
// derived from the durable execution_events ledger (GH-4134). Unlike
// pilot_prs_merged_total/pilot_prs_failed_total, CI pass/fail verdicts have
// no pre-ledger equivalent in the executions table — the executor never
// persisted a per-verdict CI outcome before TASK-379/GH-3844 introduced this
// ledger, so this is genuinely the only historical source (reset-on-restart
// for any verdict older than the ledger, documented in FEATURE-MATRIX.md).
type LifetimeCIRunCounters struct {
	Pass int64
	Fail int64
}

// GetLifetimeCIRunCounters queries execution_events for lifetime
// pilot_ci_runs_total{result} baselines, deduped per execution_id (COUNT(
// DISTINCT), not COUNT(*)) so a stage logged more than once for the same
// execution is not double counted — mirrors GetLifetimePRCounters. Live
// recording (controller.go handleWaitingCI/handleCIFailed) only ever
// transitions a given execution to ci_passed or ci_failed once (both are
// terminal for that stage), so this dedupe is defensive, not load-bearing.
func (s *Store) GetLifetimeCIRunCounters() (*LifetimeCIRunCounters, error) {
	counters := &LifetimeCIRunCounters{}

	// GH-4240: join back to executions so a canary sandbox run's CI verdicts
	// don't inflate this lifetime baseline (execution_events has no
	// project/canary info of its own).
	rows, err := s.db.Query(`
		SELECT ee.stage, COUNT(DISTINCT ee.execution_id)
		FROM execution_events ee
		JOIN executions e ON e.id = ee.execution_id
		WHERE ee.stage IN (?, ?) AND COALESCE(e.is_canary, 0) = 0
		GROUP BY ee.stage
	`, string(StageCIPassed), string(StageCIFailed))
	if err != nil {
		return nil, fmt.Errorf("failed to get lifetime CI run counters: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var stage string
		var count int64
		if err := rows.Scan(&stage, &count); err != nil {
			return nil, fmt.Errorf("failed to scan lifetime CI run counter row: %w", err)
		}
		switch Stage(stage) {
		case StageCIPassed:
			counters.Pass = count
		case StageCIFailed:
			counters.Fail = count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate lifetime CI run counters: %w", err)
	}

	return counters, nil
}

// earliestStageOccurrences returns, per execution_id, the earliest occurred_at
// for the given stage. Selects the raw column directly (not through a
// GROUP BY/MIN() aggregate) — the sqlite driver only recovers the DATETIME
// declared-type for direct column reads; scanning an aggregate result into
// time.Time fails, so ORDER BY + first-write-wins in Go stands in for MIN().
// GH-4240: joins back to executions to drop canary-sandbox rows — every
// caller feeds a throughput histogram that must stay canary-blind.
func (s *Store) earliestStageOccurrences(stage Stage) (map[string]time.Time, error) {
	rows, err := s.db.Query(`
		SELECT ee.execution_id, ee.occurred_at
		FROM execution_events ee
		JOIN executions e ON e.id = ee.execution_id
		WHERE ee.stage = ? AND COALESCE(e.is_canary, 0) = 0
		ORDER BY ee.occurred_at ASC, ee.id ASC
	`, string(stage))
	if err != nil {
		return nil, fmt.Errorf("failed to query %s stage occurrences: %w", stage, err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]time.Time)
	for rows.Next() {
		var execID string
		var occurredAt time.Time
		if err := rows.Scan(&execID, &occurredAt); err != nil {
			return nil, fmt.Errorf("failed to scan %s stage occurrence: %w", stage, err)
		}
		if _, exists := out[execID]; !exists {
			out[execID] = occurredAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate %s stage occurrences: %w", stage, err)
	}
	return out, nil
}

// GetLifetimePRTimeToMerge derives pilot_pr_time_to_merge_seconds histogram
// samples from execution_events timestamps: for every execution_id that has
// both a 'pr_created' and a 'merged' row, the sample is the earliest 'merged'
// occurred_at minus the earliest 'pr_created' occurred_at (GH-4093). Samples
// with a non-positive duration are dropped defensively — they should not
// occur (occurred_at is a monotonic write-time clock per InsertExecutionEvent)
// but a negative bucket observation would corrupt the histogram.
func (s *Store) GetLifetimePRTimeToMerge() ([]time.Duration, error) {
	created, err := s.earliestStageOccurrences(StagePRCreated)
	if err != nil {
		return nil, err
	}
	merged, err := s.earliestStageOccurrences(StageMerged)
	if err != nil {
		return nil, err
	}

	var samples []time.Duration
	for execID, mergedAt := range merged {
		createdAt, ok := created[execID]
		if !ok {
			continue
		}
		if d := mergedAt.Sub(createdAt); d > 0 {
			samples = append(samples, d)
		}
	}
	return samples, nil
}

// timedSample pairs a histogram observation with the wall-clock time it
// occurred, so callers can sort chronologically before applying a
// most-recent-N cap (Metrics.maxSamples) — execution_events queries return
// results keyed by a Go map, which has no stable iteration order.
type timedSample struct {
	at time.Time
	d  time.Duration
}

func sortedDurations(samples []timedSample) []time.Duration {
	sort.Slice(samples, func(i, j int) bool { return samples[i].at.Before(samples[j].at) })
	out := make([]time.Duration, len(samples))
	for i, sm := range samples {
		out[i] = sm.d
	}
	return out
}

// GetLifetimeTimeToPR derives pilot_time_to_pr_seconds histogram samples from
// execution_events timestamps: for every execution_id that has both a
// 'running' and a 'pr_created' row, the sample is the earliest 'pr_created'
// occurred_at minus the earliest 'running' occurred_at (GH-4211, mirrors
// GetLifetimePRTimeToMerge). Samples are returned oldest-first so a caller
// capping to the most recent N keeps the right tail.
func (s *Store) GetLifetimeTimeToPR() ([]time.Duration, error) {
	running, err := s.earliestStageOccurrences(StageRunning)
	if err != nil {
		return nil, err
	}
	created, err := s.earliestStageOccurrences(StagePRCreated)
	if err != nil {
		return nil, err
	}

	var samples []timedSample
	for execID, createdAt := range created {
		startedAt, ok := running[execID]
		if !ok {
			continue
		}
		if d := createdAt.Sub(startedAt); d > 0 {
			samples = append(samples, timedSample{at: createdAt, d: d})
		}
	}
	return sortedDurations(samples), nil
}

// GetLifetimeQueueWait derives pilot_queue_wait_seconds histogram samples
// directly from the executions table: for every row with a non-null
// started_at, the sample is started_at minus created_at (GH-4211). Unlike
// GetLifetimeTimeToPR/GetLifetimePRTimeToMerge this reads executions, not
// execution_events — queue wait is a property of the execution row itself,
// not a ledger transition. Samples are returned oldest-first.
func (s *Store) GetLifetimeQueueWait() ([]time.Duration, error) {
	rows, err := s.db.Query(`
		SELECT created_at, started_at
		FROM executions
		WHERE started_at IS NOT NULL AND COALESCE(is_canary, 0) = 0
		ORDER BY started_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query queue wait samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var samples []time.Duration
	for rows.Next() {
		var createdAt, startedAt time.Time
		if err := rows.Scan(&createdAt, &startedAt); err != nil {
			return nil, fmt.Errorf("failed to scan queue wait row: %w", err)
		}
		if d := startedAt.Sub(createdAt); d > 0 {
			samples = append(samples, d)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate queue wait rows: %w", err)
	}
	return samples, nil
}

// GetLifetimeApprovalWait derives pilot_approval_wait_seconds histogram
// samples from execution_events timestamps: for every execution_id that has
// both an 'awaiting_approval' and a 'merged' row, the sample is the earliest
// 'merged' occurred_at minus the earliest 'awaiting_approval' occurred_at
// (GH-4211). The live path (Controller.applyApprovalDecision) also observes
// a sample when approval is rejected/times out, but that decision itself is
// never durably persisted as a distinct ledger stage — only the terminal
// 'merged' transition is — so a rejected/timed-out approval has no
// ledger-derivable pair and is not represented here (reset-on-restart for
// that subset, same as the operational counters documented below). Samples
// are returned oldest-first.
func (s *Store) GetLifetimeApprovalWait() ([]time.Duration, error) {
	awaiting, err := s.earliestStageOccurrences(StageAwaitingApproval)
	if err != nil {
		return nil, err
	}
	merged, err := s.earliestStageOccurrences(StageMerged)
	if err != nil {
		return nil, err
	}

	var samples []timedSample
	for execID, mergedAt := range merged {
		awaitingAt, ok := awaiting[execID]
		if !ok {
			continue
		}
		if d := mergedAt.Sub(awaitingAt); d > 0 {
			samples = append(samples, timedSample{at: mergedAt, d: d})
		}
	}
	return sortedDurations(samples), nil
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

// GetLogsByExecutionID returns log entries matching executionID against execution_logs.execution_id.
// Since GH-3764-2, that column is written via Task.LogExecutionID(), which prefers the
// dispatcher-assigned executions.id UUID over the human-readable task ID (e.g. "GH-3714") —
// callers should pass the UUID for dispatcher-executed tasks and fall back to the task ID only
// for executions that never got a dispatcher-assigned ID. Returned in chronological order
// (oldest first), keeping the most recent limit entries if there are more matches than limit.
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
