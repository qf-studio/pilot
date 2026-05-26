package autopilot

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// StateStore persists autopilot state to SQLite for crash recovery.
// It stores PR lifecycle state and processed issue tracking so that
// autopilot can resume from the correct stage after a restart.
type StateStore struct {
	db *sql.DB
}

// NewStateStore creates a StateStore using an existing *sql.DB connection.
// It runs migrations to create the required tables if they don't exist.
func NewStateStore(db *sql.DB) (*StateStore, error) {
	s := &StateStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("autopilot state store migration failed: %w", err)
	}
	return s, nil
}

// NewStateStoreFromPath creates a StateStore by opening a new SQLite connection.
// Used primarily for testing with in-memory databases (path = ":memory:").
func NewStateStoreFromPath(path string) (*StateStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		return nil, fmt.Errorf("failed to set database pragmas: %w", err)
	}
	return NewStateStore(db)
}

func (s *StateStore) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS autopilot_pr_state (
			pr_number INTEGER PRIMARY KEY,
			pr_url TEXT NOT NULL,
			issue_number INTEGER DEFAULT 0,
			branch_name TEXT NOT NULL DEFAULT '',
			head_sha TEXT DEFAULT '',
			stage TEXT NOT NULL,
			ci_status TEXT NOT NULL DEFAULT 'pending',
			last_checked DATETIME,
			ci_wait_started_at DATETIME,
			merge_attempts INTEGER DEFAULT 0,
			error TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			release_version TEXT DEFAULT '',
			release_bump_type TEXT DEFAULT ''
		)`,
		// GH-2345: Track whether the merge-completion comment has been posted,
		// so re-entry into StageMerging (e.g. after crash recovery) does not
		// emit duplicate "PR merged" comments on the linked issue.
		`ALTER TABLE autopilot_pr_state ADD COLUMN merge_notification_posted INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS autopilot_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS autopilot_pr_failures (
			pr_number INTEGER PRIMARY KEY,
			failure_count INTEGER NOT NULL DEFAULT 0,
			last_failure_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// GH-1838: Generic adapter_processed table — replaces 7 per-adapter tables.
		// Source and repo form the namespace; repo is '' for tracker-style adapters.
		`CREATE TABLE IF NOT EXISTS adapter_processed (
			adapter TEXT NOT NULL,
			issue_id TEXT NOT NULL,
			processed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			result TEXT DEFAULT '',
			PRIMARY KEY (adapter, issue_id)
		)`,
		// GH-2685: Async approval state — persisted so crash-recovery can resume
		// the non-blocking tick handler without re-submitting the request.
		`ALTER TABLE autopilot_pr_state ADD COLUMN approval_request_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN approval_decision TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN approval_requested_at DATETIME`,
		// GH-2717: Non-blocking post-merge CI — persist SHA and start time so
		// daemon restarts resume monitoring the same commit without re-fetching.
		`ALTER TABLE autopilot_pr_state ADD COLUMN post_merge_sha TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN post_merge_ci_started_at DATETIME`,
		// TASK-298: Add repo column to adapter_processed for cross-repo dedup (TASK-288 Step 2).
		// Repo defaults to '' for tracker-style adapters (linear, jira, asana, etc.).
		`ALTER TABLE adapter_processed ADD COLUMN repo TEXT NOT NULL DEFAULT ''`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			// Ignore "duplicate column" errors from ALTER TABLE migrations
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	// TASK-298: Consolidate 7 legacy per-adapter tables into adapter_processed.
	if err := s.migrateLegacyProcessedTables(); err != nil {
		return fmt.Errorf("legacy processed tables migration failed: %w", err)
	}

	return nil
}

// migrateLegacyProcessedTables copies rows from the 7 legacy per-adapter tables
// into adapter_processed, then drops the legacy tables.
// Safe to run multiple times: checks table existence and uses INSERT OR IGNORE.
func (s *StateStore) migrateLegacyProcessedTables() error {
	type legacyTable struct {
		table   string
		adapter string
		idCol   string
		castInt bool // true when the PK column is INTEGER and must be cast to TEXT
	}
	tables := []legacyTable{
		{"autopilot_processed", "github", "issue_number", true},
		{"linear_processed", "linear", "issue_id", false},
		{"gitlab_processed", "gitlab", "issue_number", true},
		{"jira_processed", "jira", "issue_key", false},
		{"asana_processed", "asana", "task_gid", false},
		{"azuredevops_processed", "azuredevops", "work_item_id", true},
		{"plane_processed", "plane", "issue_id", false},
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, lt := range tables {
		// Skip if legacy table does not exist (fresh install or already dropped).
		var exists int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, lt.table,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check table %s: %w", lt.table, err)
		}
		if exists == 0 {
			continue
		}

		idExpr := lt.idCol
		if lt.castInt {
			idExpr = fmt.Sprintf("CAST(%s AS TEXT)", lt.idCol)
		}

		// Copy rows; OR IGNORE skips rows already present in adapter_processed.
		q := fmt.Sprintf(`
			INSERT OR IGNORE INTO adapter_processed (adapter, repo, issue_id, processed_at, result)
			SELECT ?, '', %s, processed_at, COALESCE(result, '') FROM %s
		`, idExpr, lt.table)
		if _, err := tx.Exec(q, lt.adapter); err != nil {
			return fmt.Errorf("copy %s: %w", lt.table, err)
		}

		if _, err := tx.Exec(`DROP TABLE IF EXISTS ` + lt.table); err != nil {
			return fmt.Errorf("drop %s: %w", lt.table, err)
		}
	}

	return tx.Commit()
}

// SavePRState persists a PR state to the database (upsert).
func (s *StateStore) SavePRState(pr *PRState) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_pr_state (
			pr_number, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at, updated_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(pr_number) DO UPDATE SET
			pr_url = excluded.pr_url,
			issue_number = excluded.issue_number,
			branch_name = excluded.branch_name,
			head_sha = excluded.head_sha,
			stage = excluded.stage,
			ci_status = excluded.ci_status,
			last_checked = excluded.last_checked,
			ci_wait_started_at = excluded.ci_wait_started_at,
			merge_attempts = excluded.merge_attempts,
			error = excluded.error,
			updated_at = CURRENT_TIMESTAMP,
			release_version = excluded.release_version,
			release_bump_type = excluded.release_bump_type,
			merge_notification_posted = excluded.merge_notification_posted,
			approval_request_id = excluded.approval_request_id,
			approval_decision = excluded.approval_decision,
			approval_requested_at = excluded.approval_requested_at,
			post_merge_sha = excluded.post_merge_sha,
			post_merge_ci_started_at = excluded.post_merge_ci_started_at
	`,
		pr.PRNumber, pr.PRURL, pr.IssueNumber, pr.BranchName, pr.HeadSHA,
		string(pr.Stage), string(pr.CIStatus),
		nullTime(pr.LastChecked), nullTime(pr.CIWaitStartedAt),
		pr.MergeAttempts, pr.Error, nullTime(pr.CreatedAt),
		pr.ReleaseVersion, string(pr.ReleaseBumpType), pr.MergeNotificationPosted,
		pr.ApprovalRequestID, pr.ApprovalDecision, nullTime(pr.ApprovalRequestedAt),
		pr.PostMergeSHA, nullTime(pr.PostMergeCIStartedAt),
	)
	return err
}

// GetPRState retrieves a single PR state by number.
// Returns nil, nil if not found.
func (s *StateStore) GetPRState(prNumber int) (*PRState, error) {
	row := s.db.QueryRow(`
		SELECT pr_number, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at
		FROM autopilot_pr_state WHERE pr_number = ?
	`, prNumber)

	pr, err := scanPRState(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pr, nil
}

// LoadAllPRStates retrieves all persisted PR states.
func (s *StateStore) LoadAllPRStates() ([]*PRState, error) {
	rows, err := s.db.Query(`
		SELECT pr_number, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at
		FROM autopilot_pr_state
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var states []*PRState
	for rows.Next() {
		var pr PRState
		var lastChecked, ciWaitStartedAt, createdAt, approvalRequestedAt, postMergeCIStartedAt sql.NullTime
		var stage, ciStatus, relBumpType string

		if err := rows.Scan(
			&pr.PRNumber, &pr.PRURL, &pr.IssueNumber, &pr.BranchName, &pr.HeadSHA,
			&stage, &ciStatus, &lastChecked, &ciWaitStartedAt,
			&pr.MergeAttempts, &pr.Error, &createdAt,
			&pr.ReleaseVersion, &relBumpType, &pr.MergeNotificationPosted,
			&pr.ApprovalRequestID, &pr.ApprovalDecision, &approvalRequestedAt,
			&pr.PostMergeSHA, &postMergeCIStartedAt,
		); err != nil {
			return nil, err
		}

		pr.Stage = PRStage(stage)
		pr.CIStatus = CIStatus(ciStatus)
		pr.ReleaseBumpType = BumpType(relBumpType)
		if lastChecked.Valid {
			pr.LastChecked = lastChecked.Time
		}
		if ciWaitStartedAt.Valid {
			pr.CIWaitStartedAt = ciWaitStartedAt.Time
		}
		if createdAt.Valid {
			pr.CreatedAt = createdAt.Time
		}
		if approvalRequestedAt.Valid {
			pr.ApprovalRequestedAt = approvalRequestedAt.Time
		}
		if postMergeCIStartedAt.Valid {
			pr.PostMergeCIStartedAt = postMergeCIStartedAt.Time
		}
		states = append(states, &pr)
	}
	return states, nil
}

// RemovePRState deletes a PR state record.
func (s *StateStore) RemovePRState(prNumber int) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_pr_state WHERE pr_number = ?`, prNumber)
	return err
}

// Mark records that an issue has been processed for the given source adapter and repo.
// For tracker-style adapters (linear, jira, asana, etc.) pass repo="".
// For VCS-hosted adapters (github, gitlab) pass repo as "owner/repo".
func (s *StateStore) Mark(source, repo, issueID string) error {
	_, err := s.db.Exec(`
		INSERT INTO adapter_processed (adapter, repo, issue_id, processed_at, result)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, '')
		ON CONFLICT(adapter, issue_id) DO UPDATE SET
			repo = excluded.repo,
			processed_at = CURRENT_TIMESTAMP
	`, source, repo, issueID)
	return err
}

// Unmark removes the processed record for the given source, repo, and issue.
// Used when a failed-label is removed to allow retry.
func (s *StateStore) Unmark(source, repo, issueID string) error {
	_, err := s.db.Exec(
		`DELETE FROM adapter_processed WHERE adapter = ? AND repo = ? AND issue_id = ?`,
		source, repo, issueID,
	)
	return err
}

// IsProcessed reports whether the given issue has been processed.
func (s *StateStore) IsProcessed(source, repo, issueID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM adapter_processed WHERE adapter = ? AND repo = ? AND issue_id = ?`,
		source, repo, issueID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Load returns all processed issue IDs (and their timestamps) for the given source and repo.
func (s *StateStore) Load(source, repo string) (map[string]time.Time, error) {
	rows, err := s.db.Query(
		`SELECT issue_id, processed_at FROM adapter_processed WHERE adapter = ? AND repo = ?`,
		source, repo,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	processed := make(map[string]time.Time)
	for rows.Next() {
		var id string
		var ts time.Time
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		processed[id] = ts
	}
	return processed, nil
}

// Purge removes processed records for the given source that are older than olderThan.
func (s *StateStore) Purge(source string, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.db.Exec(
		`DELETE FROM adapter_processed WHERE adapter = ? AND processed_at < ?`,
		source, cutoff,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// SaveMetadata stores a key-value pair in the metadata table.
func (s *StateStore) SaveMetadata(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_metadata (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, key, value)
	return err
}

// GetMetadata retrieves a metadata value by key.
// Returns empty string if not found.
func (s *StateStore) GetMetadata(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM autopilot_metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SavePRFailures persists the per-PR failure state.
func (s *StateStore) SavePRFailures(prNumber, failureCount int, lastFailureTime time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_pr_failures (pr_number, failure_count, last_failure_time)
		VALUES (?, ?, ?)
		ON CONFLICT(pr_number) DO UPDATE SET
			failure_count = excluded.failure_count,
			last_failure_time = excluded.last_failure_time
	`, prNumber, failureCount, lastFailureTime)
	return err
}

// RemovePRFailures removes per-PR failure state.
func (s *StateStore) RemovePRFailures(prNumber int) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_pr_failures WHERE pr_number = ?`, prNumber)
	return err
}

// LoadAllPRFailures loads all per-PR failure states.
func (s *StateStore) LoadAllPRFailures() (map[int]*prFailureState, error) {
	rows, err := s.db.Query(`
		SELECT pr_number, failure_count, last_failure_time
		FROM autopilot_pr_failures
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	failures := make(map[int]*prFailureState)
	for rows.Next() {
		var prNumber, failureCount int
		var lastFailureTime time.Time

		if err := rows.Scan(&prNumber, &failureCount, &lastFailureTime); err != nil {
			return nil, err
		}

		failures[prNumber] = &prFailureState{
			FailureCount:    failureCount,
			LastFailureTime: lastFailureTime,
		}
	}
	return failures, nil
}

// PurgeTerminalPRStates removes PR states in terminal stages (failed, merged+removed).
// This is for housekeeping — active PRs are never purged.
func (s *StateStore) PurgeTerminalPRStates(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.db.Exec(`
		DELETE FROM autopilot_pr_state
		WHERE stage IN ('failed') AND updated_at < ?
	`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// scanPRState scans a single row into a PRState.
func scanPRState(row *sql.Row) (*PRState, error) {
	var pr PRState
	var lastChecked, ciWaitStartedAt, createdAt, approvalRequestedAt, postMergeCIStartedAt sql.NullTime
	var stage, ciStatus, relBumpType string

	err := row.Scan(
		&pr.PRNumber, &pr.PRURL, &pr.IssueNumber, &pr.BranchName, &pr.HeadSHA,
		&stage, &ciStatus, &lastChecked, &ciWaitStartedAt,
		&pr.MergeAttempts, &pr.Error, &createdAt,
		&pr.ReleaseVersion, &relBumpType, &pr.MergeNotificationPosted,
		&pr.ApprovalRequestID, &pr.ApprovalDecision, &approvalRequestedAt,
		&pr.PostMergeSHA, &postMergeCIStartedAt,
	)
	if err != nil {
		return nil, err
	}

	pr.Stage = PRStage(stage)
	pr.CIStatus = CIStatus(ciStatus)
	pr.ReleaseBumpType = BumpType(relBumpType)
	if lastChecked.Valid {
		pr.LastChecked = lastChecked.Time
	}
	if ciWaitStartedAt.Valid {
		pr.CIWaitStartedAt = ciWaitStartedAt.Time
	}
	if createdAt.Valid {
		pr.CreatedAt = createdAt.Time
	}
	if approvalRequestedAt.Valid {
		pr.ApprovalRequestedAt = approvalRequestedAt.Time
	}
	if postMergeCIStartedAt.Valid {
		pr.PostMergeCIStartedAt = postMergeCIStartedAt.Time
	}
	return &pr, nil
}

// nullTime converts a time.Time to sql.NullTime, treating zero time as NULL.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
