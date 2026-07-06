package autopilot

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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
		// GH-3819: repo is part of the PRIMARY KEY from the start on fresh installs.
		// (Older DBs had repo added later via ALTER TABLE, which cannot extend a
		// PRIMARY KEY — see migrateAdapterProcessedPrimaryKey below.)
		`CREATE TABLE IF NOT EXISTS adapter_processed (
			adapter TEXT NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
			issue_id TEXT NOT NULL,
			processed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			result TEXT DEFAULT '',
			PRIMARY KEY (adapter, repo, issue_id)
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
		// GH-3715: Persist the auto-rebase cycle counter so the cap on successful
		// rebases (conflict -> rebase-success -> CI -> conflict oscillation)
		// survives daemon restarts.
		`ALTER TABLE autopilot_pr_state ADD COLUMN rebase_attempts INTEGER NOT NULL DEFAULT 0`,
		// GH-3903: Add repo ahead of the primary-key rebuild in
		// migratePRStateRepoScoping below — pr_number alone is not unique when
		// multiple project-scoped controllers share one SQLite DB.
		`ALTER TABLE autopilot_pr_state ADD COLUMN repo TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE autopilot_pr_failures ADD COLUMN repo TEXT NOT NULL DEFAULT ''`,
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

	// GH-3819: Rebuild adapter_processed with repo in the PRIMARY KEY if an
	// older DB still has the pre-GH-1838-fixup schema. Must run before the
	// legacy-table migration below so legacy rows land in the corrected table.
	if err := s.migrateAdapterProcessedPrimaryKey(); err != nil {
		return fmt.Errorf("adapter_processed primary key migration failed: %w", err)
	}

	// TASK-298: Consolidate 7 legacy per-adapter tables into adapter_processed.
	// Must run before the empty-repo prune below: these legacy tables predate
	// the repo column and are always consolidated with repo='', so pruning
	// first would miss the very rows this step creates until a second restart.
	if err := s.migrateLegacyProcessedTables(); err != nil {
		return fmt.Errorf("legacy processed tables migration failed: %w", err)
	}

	// GH-3819: Purge orphaned empty-repo rows for repo-scoped adapters. These
	// can only have been written before the repo column existed, by the
	// GH-3819 collision bug clobbering a row down to its zero value, or by
	// the legacy-table consolidation above; a repo-scoped poller always calls
	// Mark/IsProcessed/Load with a non-empty "owner/repo" key, so such rows
	// can never be matched again and would otherwise sit as dead weight.
	if err := s.pruneOrphanedEmptyRepoRows(); err != nil {
		return fmt.Errorf("adapter_processed empty-repo cleanup failed: %w", err)
	}

	// GH-3903: rebuild autopilot_pr_state/autopilot_pr_failures so repo joins
	// the primary key, fixing cross-repo pr_number collisions between
	// project-scoped controllers sharing one SQLite DB.
	if err := s.migratePRStateRepoScoping(); err != nil {
		return fmt.Errorf("autopilot_pr_state repo-scoping migration failed: %w", err)
	}

	return nil
}

// migrateAdapterProcessedPrimaryKey rebuilds adapter_processed so repo is part
// of the PRIMARY KEY. The original table (GH-1838) keyed on (adapter, issue_id)
// only; repo was bolted on afterward via ALTER TABLE ADD COLUMN, which SQLite
// cannot use to extend a PRIMARY KEY. As a result, two different repos
// processing the same issue_id (e.g. issue #5 in two separate projects) shared
// one row: Mark()'s ON CONFLICT upsert silently overwrote that row's repo
// column with whichever repo processed the colliding ID most recently,
// corrupting cross-project dedup state (GH-3819).
//
// No-op if repo is already part of the primary key (fresh install, or already
// migrated).
func (s *StateStore) migrateAdapterProcessedPrimaryKey() error {
	rows, err := s.db.Query(`PRAGMA table_info(adapter_processed)`)
	if err != nil {
		return fmt.Errorf("inspect adapter_processed schema: %w", err)
	}
	repoInPK := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan adapter_processed schema: %w", err)
		}
		if name == "repo" && pk > 0 {
			repoInPK = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate adapter_processed schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close adapter_processed schema query: %w", err)
	}
	if repoInPK {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		CREATE TABLE adapter_processed_gh3819 (
			adapter TEXT NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
			issue_id TEXT NOT NULL,
			processed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			result TEXT DEFAULT '',
			PRIMARY KEY (adapter, repo, issue_id)
		)
	`); err != nil {
		return fmt.Errorf("create adapter_processed_gh3819: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO adapter_processed_gh3819 (adapter, repo, issue_id, processed_at, result)
		SELECT adapter, repo, issue_id, processed_at, result FROM adapter_processed
	`); err != nil {
		return fmt.Errorf("copy adapter_processed rows: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE adapter_processed`); err != nil {
		return fmt.Errorf("drop old adapter_processed: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE adapter_processed_gh3819 RENAME TO adapter_processed`); err != nil {
		return fmt.Errorf("rename adapter_processed_gh3819: %w", err)
	}
	return tx.Commit()
}

// prURLRepoRe extracts "owner/repo" from a GitHub PR URL of the form
// "https://github.com/{owner}/{repo}/pull/{n}".
var prURLRepoRe = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+)/pull/\d+$`)

// repoFromPRURL extracts "owner/repo" from a GitHub PR URL, or "" if prURL
// doesn't match the expected shape (empty/malformed legacy row).
func repoFromPRURL(prURL string) string {
	m := prURLRepoRe.FindStringSubmatch(prURL)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// tableHasColumnInPK reports whether column is part of table's PRIMARY KEY,
// per PRAGMA table_info. Used to make a PK-rebuild migration idempotent: skip
// it on every startup after the first successful run (or on a fresh install
// where the table is already created with the column in its key).
func (s *StateStore) tableHasColumnInPK(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column && pk > 0 {
			return true, nil
		}
	}
	return false, rows.Err()
}

// migratePRStateRepoScoping rebuilds autopilot_pr_state and
// autopilot_pr_failures so repo is part of the primary key, fixing GH-3903:
// with pr_number alone as the key, every project-scoped controller sharing
// one SQLite DB restores and processes every other controller's rows too — a
// PR closed in one repo could apply labels, close issues, and delete branches
// in a completely unrelated repo that merely happens to have a PR with the
// same number.
//
// repo is backfilled for existing autopilot_pr_state rows by parsing pr_url.
// autopilot_pr_failures has no URL of its own; its rows are matched to their
// PR's resolved repo via pr_number, and dropped if no match is found — these
// are ephemeral retry counters, safe to lose (a real failure recurs and
// re-persists on the very next tick).
//
// No-op if repo is already part of the primary key (fresh install, or a
// prior run already migrated this DB).
func (s *StateStore) migratePRStateRepoScoping() error {
	already, err := s.tableHasColumnInPK("autopilot_pr_state", "repo")
	if err != nil {
		return fmt.Errorf("inspect autopilot_pr_state schema: %w", err)
	}
	if already {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Backfill repo for pr_state rows that don't have one yet, parsed from pr_url.
	rows, err := tx.Query(`SELECT pr_number, pr_url FROM autopilot_pr_state WHERE repo = ''`)
	if err != nil {
		return fmt.Errorf("query autopilot_pr_state for backfill: %w", err)
	}
	type prRepoPair struct {
		prNumber int
		repo     string
	}
	var backfill []prRepoPair
	for rows.Next() {
		var prNumber int
		var prURL string
		if err := rows.Scan(&prNumber, &prURL); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan autopilot_pr_state row: %w", err)
		}
		if repo := repoFromPRURL(prURL); repo != "" {
			backfill = append(backfill, prRepoPair{prNumber, repo})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate autopilot_pr_state rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close autopilot_pr_state rows: %w", err)
	}
	for _, pair := range backfill {
		if _, err := tx.Exec(
			`UPDATE autopilot_pr_state SET repo = ? WHERE pr_number = ? AND repo = ''`,
			pair.repo, pair.prNumber,
		); err != nil {
			return fmt.Errorf("backfill repo for pr %d: %w", pair.prNumber, err)
		}
	}

	// Rebuild autopilot_pr_state with repo in the primary key.
	if _, err := tx.Exec(`
		CREATE TABLE autopilot_pr_state_gh3903 (
			pr_number INTEGER NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
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
			release_bump_type TEXT DEFAULT '',
			merge_notification_posted INTEGER NOT NULL DEFAULT 0,
			approval_request_id TEXT NOT NULL DEFAULT '',
			approval_decision TEXT NOT NULL DEFAULT '',
			approval_requested_at DATETIME,
			post_merge_sha TEXT NOT NULL DEFAULT '',
			post_merge_ci_started_at DATETIME,
			rebase_attempts INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (repo, pr_number)
		)
	`); err != nil {
		return fmt.Errorf("create autopilot_pr_state_gh3903: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO autopilot_pr_state_gh3903 (
			pr_number, repo, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at, updated_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at, rebase_attempts
		)
		SELECT
			pr_number, repo, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at, updated_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at, rebase_attempts
		FROM autopilot_pr_state
	`); err != nil {
		return fmt.Errorf("copy autopilot_pr_state rows: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE autopilot_pr_state`); err != nil {
		return fmt.Errorf("drop old autopilot_pr_state: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE autopilot_pr_state_gh3903 RENAME TO autopilot_pr_state`); err != nil {
		return fmt.Errorf("rename autopilot_pr_state_gh3903: %w", err)
	}

	// Rebuild autopilot_pr_failures, matching each row to its PR's resolved
	// repo via pr_number (against the now-scoped autopilot_pr_state above).
	// Rows that cannot be matched to any repo are dropped.
	if _, err := tx.Exec(`
		CREATE TABLE autopilot_pr_failures_gh3903 (
			pr_number INTEGER NOT NULL,
			repo TEXT NOT NULL DEFAULT '',
			failure_count INTEGER NOT NULL DEFAULT 0,
			last_failure_time DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (repo, pr_number)
		)
	`); err != nil {
		return fmt.Errorf("create autopilot_pr_failures_gh3903: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO autopilot_pr_failures_gh3903 (pr_number, repo, failure_count, last_failure_time)
		SELECT f.pr_number, s.repo, f.failure_count, f.last_failure_time
		FROM autopilot_pr_failures f
		JOIN autopilot_pr_state s ON s.pr_number = f.pr_number
		WHERE s.repo != ''
	`); err != nil {
		return fmt.Errorf("copy autopilot_pr_failures rows: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE autopilot_pr_failures`); err != nil {
		return fmt.Errorf("drop old autopilot_pr_failures: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE autopilot_pr_failures_gh3903 RENAME TO autopilot_pr_failures`); err != nil {
		return fmt.Errorf("rename autopilot_pr_failures_gh3903: %w", err)
	}

	return tx.Commit()
}

// repoScopedAdapters lists adapter names whose pollers always dedup within a
// specific repo (as opposed to tracker-style adapters — linear, jira, asana,
// plane — which pass repo="" by design, see Mark's doc comment).
var repoScopedAdapters = []string{"github", "gitlab", "azuredevops"}

// pruneOrphanedEmptyRepoRows deletes adapter_processed rows for repo-scoped
// adapters that have an empty repo. Such rows cannot correspond to any real
// lookup (repoKey() is never empty for these adapters) and are either
// pre-TASK-298 leftovers from before the repo column existed, or residue from
// the GH-3819 collision bug. Safe to run on every startup: idempotent, and
// never touches tracker-style adapters, which legitimately use repo="".
func (s *StateStore) pruneOrphanedEmptyRepoRows() error {
	placeholders := make([]string, len(repoScopedAdapters))
	args := make([]interface{}, len(repoScopedAdapters))
	for i, a := range repoScopedAdapters {
		placeholders[i] = "?"
		args[i] = a
	}
	query := fmt.Sprintf(
		`DELETE FROM adapter_processed WHERE repo = '' AND adapter IN (%s)`,
		strings.Join(placeholders, ", "),
	)
	_, err := s.db.Exec(query, args...)
	return err
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

// SavePRState persists a PR state to the database (upsert), scoped to repo
// ("owner/repo"). GH-3903: repo is part of the primary key alongside
// pr_number so project-scoped controllers sharing one SQLite DB cannot
// collide on the same PR number from different repos.
func (s *StateStore) SavePRState(repo string, pr *PRState) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_pr_state (
			pr_number, repo, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at, updated_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at, rebase_attempts
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo, pr_number) DO UPDATE SET
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
			post_merge_ci_started_at = excluded.post_merge_ci_started_at,
			rebase_attempts = excluded.rebase_attempts
	`,
		pr.PRNumber, repo, pr.PRURL, pr.IssueNumber, pr.BranchName, pr.HeadSHA,
		string(pr.Stage), string(pr.CIStatus),
		nullTime(pr.LastChecked), nullTime(pr.CIWaitStartedAt),
		pr.MergeAttempts, pr.Error, nullTime(pr.CreatedAt),
		pr.ReleaseVersion, string(pr.ReleaseBumpType), pr.MergeNotificationPosted,
		pr.ApprovalRequestID, pr.ApprovalDecision, nullTime(pr.ApprovalRequestedAt),
		pr.PostMergeSHA, nullTime(pr.PostMergeCIStartedAt), pr.RebaseAttempts,
	)
	return err
}

// GetPRState retrieves a single PR state by repo and number.
// Returns nil, nil if not found.
func (s *StateStore) GetPRState(repo string, prNumber int) (*PRState, error) {
	row := s.db.QueryRow(`
		SELECT pr_number, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at, rebase_attempts
		FROM autopilot_pr_state WHERE repo = ? AND pr_number = ?
	`, repo, prNumber)

	pr, err := scanPRState(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pr, nil
}

// LoadAllPRStates retrieves all persisted PR states for the given repo.
// GH-3903: scoped so one controller can never restore another repo's rows.
func (s *StateStore) LoadAllPRStates(repo string) ([]*PRState, error) {
	rows, err := s.db.Query(`
		SELECT pr_number, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at, rebase_attempts
		FROM autopilot_pr_state WHERE repo = ?
	`, repo)
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
			&pr.PostMergeSHA, &postMergeCIStartedAt, &pr.RebaseAttempts,
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

// RemovePRState deletes a PR state record, scoped to repo.
func (s *StateStore) RemovePRState(repo string, prNumber int) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_pr_state WHERE repo = ? AND pr_number = ?`, repo, prNumber)
	return err
}

// Mark records that an issue has been processed for the given source adapter and repo.
// For tracker-style adapters (linear, jira, asana, etc.) pass repo="".
// For VCS-hosted adapters (github, gitlab) pass repo as "owner/repo".
func (s *StateStore) Mark(source, repo, issueID string) error {
	_, err := s.db.Exec(`
		INSERT INTO adapter_processed (adapter, repo, issue_id, processed_at, result)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, '')
		ON CONFLICT(adapter, repo, issue_id) DO UPDATE SET
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

// SavePRFailures persists the per-PR failure state, scoped to repo.
func (s *StateStore) SavePRFailures(repo string, prNumber, failureCount int, lastFailureTime time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO autopilot_pr_failures (pr_number, repo, failure_count, last_failure_time)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(repo, pr_number) DO UPDATE SET
			failure_count = excluded.failure_count,
			last_failure_time = excluded.last_failure_time
	`, prNumber, repo, failureCount, lastFailureTime)
	return err
}

// RemovePRFailures removes per-PR failure state, scoped to repo.
func (s *StateStore) RemovePRFailures(repo string, prNumber int) error {
	_, err := s.db.Exec(`DELETE FROM autopilot_pr_failures WHERE repo = ? AND pr_number = ?`, repo, prNumber)
	return err
}

// LoadAllPRFailures loads all per-PR failure states for the given repo.
func (s *StateStore) LoadAllPRFailures(repo string) (map[int]*prFailureState, error) {
	rows, err := s.db.Query(`
		SELECT pr_number, failure_count, last_failure_time
		FROM autopilot_pr_failures WHERE repo = ?
	`, repo)
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

// releasingStaleThreshold bounds how long a PR row may sit at stage='releasing'
// before it is treated as wedged. 'releasing' is not a terminal stage, but a row
// stuck past this threshold indicates a release that never completed (B4/TASK-309).
// Shared by PurgeTerminalPRStates (B4 housekeeping purge) and the scanner skip
// gate (B3, PersistedReleasingAge) so both agree on what "stale" means.
const releasingStaleThreshold = 30 * time.Minute

// PurgeTerminalPRStates removes housekeeping-eligible PR state rows for the
// given repo: terminal 'failed' rows older than olderThan, plus 'releasing'
// rows untouched for longer than releasingStaleThreshold. A 'releasing' row
// is not strictly terminal, but one stuck past the threshold is a wedged
// release (B4/TASK-309) — purging it is a safety net so the row cannot live
// forever and suppress re-discovery by ScanRecentlyMergedPRs. Active PRs
// (fresh rows, other stages) are never purged. GH-3903: scoped by repo so a
// housekeeping purge from one controller can never delete another repo's
// terminal rows.
func (s *StateStore) PurgeTerminalPRStates(repo string, olderThan time.Duration) (int64, error) {
	// updated_at is written as CURRENT_TIMESTAMP (SQLite UTC), so the cutoffs must
	// be evaluated against SQLite's own UTC clock — binding a Go (local) time.Time
	// here mis-compares by the host's tz offset. <= keeps the olderThan=0 degenerate
	// case ("purge all terminal rows now") reaping same-second rows.
	result, err := s.db.Exec(`
		DELETE FROM autopilot_pr_state
		WHERE repo = ?
		  AND ((stage = 'failed'    AND updated_at <= datetime('now', ?))
		   OR  (stage = 'releasing' AND updated_at <= datetime('now', ?)))
	`,
		repo,
		fmt.Sprintf("-%d seconds", int64(olderThan.Seconds())),
		fmt.Sprintf("-%d seconds", int64(releasingStaleThreshold.Seconds())),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PersistedReleasingAge reports the age (time since last update) of a persisted PR
// row at stage='releasing', scoped to repo. found is false when no row exists for
// repo/prNumber or the row is in a different stage. The scanner uses this
// (B3/TASK-309) to skip re-registering a release that is already in flight in the
// state store but absent from the in-memory activePRs map (e.g. after a daemon
// restart), without relying on the in-memory map alone. Returning the age (rather
// than a bool) lets the caller ignore genuinely wedged rows so they can be
// re-driven. GH-3903: scoped by repo so one controller can never read another
// repo's colliding pr_number and wrongly skip (or fail to skip) a release.
func (s *StateStore) PersistedReleasingAge(repo string, prNumber int) (age time.Duration, found bool, err error) {
	var stage string
	var updatedAt sql.NullTime
	row := s.db.QueryRow(`SELECT stage, updated_at FROM autopilot_pr_state WHERE repo = ? AND pr_number = ?`, repo, prNumber)
	if scanErr := row.Scan(&stage, &updatedAt); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, scanErr
	}
	if PRStage(stage) != StageReleasing {
		return 0, false, nil
	}
	if !updatedAt.Valid {
		// Row exists at 'releasing' but has no timestamp (should not happen given
		// the CURRENT_TIMESTAMP default); treat as wedged so it is not skipped.
		return releasingStaleThreshold, true, nil
	}
	return time.Since(updatedAt.Time), true, nil
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
		&pr.PostMergeSHA, &postMergeCIStartedAt, &pr.RebaseAttempts,
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
