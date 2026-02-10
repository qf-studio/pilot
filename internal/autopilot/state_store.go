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
		`CREATE TABLE IF NOT EXISTS autopilot_processed (
			issue_number INTEGER PRIMARY KEY,
			processed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			result TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS autopilot_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
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
	return nil
}

// SavePRState persists a PR state to the database (upsert).
func (s *StateStore) SavePRState(pr *PRState) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_pr_state (
			pr_number, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at, updated_at,
			release_version, release_bump_type
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?)
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
			release_bump_type = excluded.release_bump_type
	`,
		pr.PRNumber, pr.PRURL, pr.IssueNumber, pr.BranchName, pr.HeadSHA,
		string(pr.Stage), string(pr.CIStatus),
		nullTime(pr.LastChecked), nullTime(pr.CIWaitStartedAt),
		pr.MergeAttempts, pr.Error, nullTime(pr.CreatedAt),
		pr.ReleaseVersion, string(pr.ReleaseBumpType),
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
			release_version, release_bump_type
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
			release_version, release_bump_type
		FROM autopilot_pr_state
	`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var states []*PRState
	for rows.Next() {
		var pr PRState
		var lastChecked, ciWaitStartedAt, createdAt sql.NullTime
		var stage, ciStatus, relBumpType string

		if err := rows.Scan(
			&pr.PRNumber, &pr.PRURL, &pr.IssueNumber, &pr.BranchName, &pr.HeadSHA,
			&stage, &ciStatus, &lastChecked, &ciWaitStartedAt,
			&pr.MergeAttempts, &pr.Error, &createdAt,
			&pr.ReleaseVersion, &relBumpType,
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
		states = append(states, &pr)
	}
	return states, nil
}

// RemovePRState deletes a PR state record.
func (s *StateStore) RemovePRState(prNumber int) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_pr_state WHERE pr_number = ?`, prNumber)
	return err
}

// MarkIssueProcessed records that an issue has been processed.
func (s *StateStore) MarkIssueProcessed(issueNumber int, result string) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_processed (issue_number, processed_at, result)
		VALUES (?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT(issue_number) DO UPDATE SET
			processed_at = CURRENT_TIMESTAMP,
			result = excluded.result
	`, issueNumber, result)
	return err
}

// IsIssueProcessed checks if an issue has been previously processed.
func (s *StateStore) IsIssueProcessed(issueNumber int) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM autopilot_processed WHERE issue_number = ?`, issueNumber).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// LoadProcessedIssues returns a map of all processed issue numbers.
func (s *StateStore) LoadProcessedIssues() (map[int]bool, error) {
	rows, err := s.db.Query(`SELECT issue_number FROM autopilot_processed`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	processed := make(map[int]bool)
	for rows.Next() {
		var num int
		if err := rows.Scan(&num); err != nil {
			return nil, err
		}
		processed[num] = true
	}
	return processed, nil
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

// PurgeOldProcessedIssues removes processed issue records older than the given duration.
func (s *StateStore) PurgeOldProcessedIssues(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := s.db.Exec(`DELETE FROM autopilot_processed WHERE processed_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
	var lastChecked, ciWaitStartedAt, createdAt sql.NullTime
	var stage, ciStatus, relBumpType string

	err := row.Scan(
		&pr.PRNumber, &pr.PRURL, &pr.IssueNumber, &pr.BranchName, &pr.HeadSHA,
		&stage, &ciStatus, &lastChecked, &ciWaitStartedAt,
		&pr.MergeAttempts, &pr.Error, &createdAt,
		&pr.ReleaseVersion, &relBumpType,
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
	return &pr, nil
}

// nullTime converts a time.Time to sql.NullTime, treating zero time as NULL.
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}
