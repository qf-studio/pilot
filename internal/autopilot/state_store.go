package autopilot

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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
	// modernc.org/sqlite's ":memory:" database is private per-connection, not
	// shared across the pool — a second concurrent connection sees a fresh,
	// unmigrated database ("no such table"). Test callers of this path are
	// the only ones exercising this store from more than one goroutine (e.g.
	// GH-4476's release-tick retry runs in a background goroutine), so pin
	// the pool to a single connection to keep it one logical database.
	db.SetMaxOpenConns(1)
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
		// GH-3990: durable scope-release state — one row per epic/label scope held
		// under Trigger "on_scope_close", tracking its carrier release through
		// pending -> releasing -> done/failed.
		`CREATE TABLE IF NOT EXISTS autopilot_scope_release (
			repo TEXT NOT NULL,
			scope_key TEXT NOT NULL,
			scope_title TEXT NOT NULL DEFAULT '',
			member_prs TEXT NOT NULL DEFAULT '',
			anchor_pr INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'pending',
			final_sha TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (repo, scope_key)
		)`,
		// GH-3990: scope_key namespaces a PR's release scope ("epic:<N>" |
		// "label:<name>" | "train:<RFC3339>") — persisted so a scope-release
		// carrier's identity survives a restart via LoadAllPRStates.
		`ALTER TABLE autopilot_pr_state ADD COLUMN scope_key TEXT NOT NULL DEFAULT ''`,
		// TASK-390: persist the PR title. Restored post-merge states (tag/
		// release stages) never re-fetch the PR from GitHub, so without this
		// their dashboard rows render with a blank title forever.
		`ALTER TABLE autopilot_pr_state ADD COLUMN pr_title TEXT NOT NULL DEFAULT ''`,
		// GH-4164: mirrors merge_notification_posted's crash-recovery guard —
		// tracks whether the approval-gated "🔀 Merged <sha>" Telegram follow-up
		// has been sent, so re-entry into StageMerging cannot double-fire it.
		`ALTER TABLE autopilot_pr_state ADD COLUMN merge_followup_posted INTEGER NOT NULL DEFAULT 0`,
		// GH-4307: durable dedup claim for autopilot-generated fix issues, keyed
		// on (repo, dedup_key). Without this, a re-tick, a release-scan
		// re-discovery, or a second daemon observing the same failure signal
		// each mint a fresh "fix(ci): resolve post-merge CI failure..." issue —
		// this table is checked (via ClaimSpawnedFix) before every
		// CreateFailureIssue call so only the first observation creates one.
		// GH-4331: track the main-HEAD SHA a scope-release carrier last failed
		// against, so a recovery sweep can distinguish "still the same red
		// commit" (leave terminal) from "main moved, worth retrying" without
		// re-running CI itself.
		`ALTER TABLE autopilot_scope_release ADD COLUMN last_failed_sha TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS autopilot_spawned_fixes (
			repo TEXT NOT NULL,
			dedup_key TEXT NOT NULL,
			issue_number INTEGER NOT NULL DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (repo, dedup_key)
		)`,
		// GH-4533: persist the infra-outage auto-retry budget so a daemon
		// restart between a rerun and the next CI verdict cannot silently
		// grant a fresh 2-retry budget on the same SHA.
		`ALTER TABLE autopilot_pr_state ADD COLUMN infra_rerun_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN infra_rerun_sha TEXT NOT NULL DEFAULT ''`,
		// GH-4598: persist Parked + EscalationReason. Both were in-memory-only
		// (GH-4596/#4602), which meant every daemon restart (or poller replay
		// re-registering the PR) reset Parked to false and EscalationReason to
		// "" for a PR already sitting quietly in awaiting_approval with no
		// approval channel wired. The next tick's submitAsyncApprovalRequest
		// would then treat the misconfig as brand-new: re-log the WARN, blow
		// the specific gate reason away in favor of the generic
		// environments.<env>.require_approval=true fallback, and re-invoke
		// postMisconfigComment (itself idempotent about the actual GitHub
		// comment via the marker check, but not free — a wasted
		// ListIssueComments round-trip every restart). Persisting both fields
		// lets a restored PR recognize itself as already parked on tick 1.
		`ALTER TABLE autopilot_pr_state ADD COLUMN parked INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN escalation_reason TEXT NOT NULL DEFAULT ''`,
		// GH-4610: persist the needs-manual-rebase hold flag and the re-adoption
		// counter so a daemon restart doesn't lose track of which held PRs are
		// eligible for re-adoption, or reset an already-spent re-adoption budget.
		`ALTER TABLE autopilot_pr_state ADD COLUMN rebase_hold_active INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN readopt_count INTEGER NOT NULL DEFAULT 0`,
		// GH-4792 (TASK-458 part 2): persist the platform-outage breaker hold
		// flag and its re-adoption counter, mirroring rebase_hold_active/
		// readopt_count above for the same restart-survival reason.
		`ALTER TABLE autopilot_pr_state ADD COLUMN breaker_hold_active INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN breaker_readopt_count INTEGER NOT NULL DEFAULT 0`,
		// GH-4643: a distinct, non-resettable-by-SHA-advance counter for
		// consecutive "post-merge CI timeout" carrier failures. Unlike
		// attempts (reset to 0 by recoverFailedScopeReleases whenever main
		// advances), this counter is what lets a structurally-unresolvable
		// timeout (a workflow-less repo with a required-checks allowlist
		// naming a check that will never post) get parked instead of
		// retrying forever.
		`ALTER TABLE autopilot_scope_release ADD COLUMN timeout_attempts INTEGER NOT NULL DEFAULT 0`,
		// GH-4813: persist the post-merge infra-outage auto-retry budget,
		// mirroring infra_rerun_count/infra_rerun_sha above but scoped to the
		// post-merge mainSHA rather than the PR's HeadSHA — a daemon restart
		// between a post-merge rerun and the next CI verdict must not silently
		// grant a fresh 2-retry budget on the same SHA.
		`ALTER TABLE autopilot_pr_state ADD COLUMN post_merge_infra_rerun_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE autopilot_pr_state ADD COLUMN post_merge_infra_rerun_sha TEXT NOT NULL DEFAULT ''`,
		// GH-4919: permanent-failure classification for reconcileReleaseBackfill.
		// A row whose API lookups keep erroring (deleted repo, unresolved
		// platform incident) is marked abandoned once it crosses both a
		// consecutive-failure and a minimum-wall-clock-window threshold, so
		// every future sweep can skip it without any API call. Persisted so
		// the skip survives a daemon restart; error already carries the
		// reason via the existing error column.
		`ALTER TABLE autopilot_pr_state ADD COLUMN release_backfill_abandoned INTEGER NOT NULL DEFAULT 0`,
		// GH-4999: durable idempotency guard for the Jira merge-side done leg
		// (notifyJiraDone). checkExternalMergeOrClose's merged branch has no
		// persistPRState call between detecting the merge and its terminal
		// removePR, so without a persisted flag a crash in that window would
		// leave a restart free to re-enter the merged branch and re-fire the
		// Jira completion comment.
		`ALTER TABLE autopilot_pr_state ADD COLUMN jira_done_notified INTEGER NOT NULL DEFAULT 0`,
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
			scope_key TEXT NOT NULL DEFAULT '',
			pr_title TEXT NOT NULL DEFAULT '',
			merge_followup_posted INTEGER NOT NULL DEFAULT 0,
			infra_rerun_count INTEGER NOT NULL DEFAULT 0,
			infra_rerun_sha TEXT NOT NULL DEFAULT '',
			parked INTEGER NOT NULL DEFAULT 0,
			escalation_reason TEXT NOT NULL DEFAULT '',
			rebase_hold_active INTEGER NOT NULL DEFAULT 0,
			readopt_count INTEGER NOT NULL DEFAULT 0,
			breaker_hold_active INTEGER NOT NULL DEFAULT 0,
			breaker_readopt_count INTEGER NOT NULL DEFAULT 0,
			post_merge_infra_rerun_count INTEGER NOT NULL DEFAULT 0,
			post_merge_infra_rerun_sha TEXT NOT NULL DEFAULT '',
			release_backfill_abandoned INTEGER NOT NULL DEFAULT 0,
			jira_done_notified INTEGER NOT NULL DEFAULT 0,
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
			post_merge_sha, post_merge_ci_started_at, rebase_attempts, scope_key,
			pr_title, merge_followup_posted, infra_rerun_count, infra_rerun_sha,
			parked, escalation_reason, rebase_hold_active, readopt_count,
			breaker_hold_active, breaker_readopt_count,
			post_merge_infra_rerun_count, post_merge_infra_rerun_sha,
			release_backfill_abandoned, jira_done_notified
		)
		SELECT
			pr_number, repo, pr_url, issue_number, branch_name, head_sha,
			stage, ci_status, last_checked, ci_wait_started_at,
			merge_attempts, error, created_at, updated_at,
			release_version, release_bump_type, merge_notification_posted,
			approval_request_id, approval_decision, approval_requested_at,
			post_merge_sha, post_merge_ci_started_at, rebase_attempts, scope_key,
			pr_title, merge_followup_posted, infra_rerun_count, infra_rerun_sha,
			parked, escalation_reason, rebase_hold_active, readopt_count,
			breaker_hold_active, breaker_readopt_count,
			post_merge_infra_rerun_count, post_merge_infra_rerun_sha,
			release_backfill_abandoned, jira_done_notified
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
			post_merge_sha, post_merge_ci_started_at, rebase_attempts, scope_key,
			pr_title, merge_followup_posted, infra_rerun_count, infra_rerun_sha,
			parked, escalation_reason, rebase_hold_active, readopt_count,
			breaker_hold_active, breaker_readopt_count,
			post_merge_infra_rerun_count, post_merge_infra_rerun_sha,
			release_backfill_abandoned, jira_done_notified
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			rebase_attempts = excluded.rebase_attempts,
			scope_key = excluded.scope_key,
			pr_title = excluded.pr_title,
			merge_followup_posted = excluded.merge_followup_posted,
			infra_rerun_count = excluded.infra_rerun_count,
			infra_rerun_sha = excluded.infra_rerun_sha,
			parked = excluded.parked,
			escalation_reason = excluded.escalation_reason,
			rebase_hold_active = excluded.rebase_hold_active,
			readopt_count = excluded.readopt_count,
			breaker_hold_active = excluded.breaker_hold_active,
			breaker_readopt_count = excluded.breaker_readopt_count,
			post_merge_infra_rerun_count = excluded.post_merge_infra_rerun_count,
			post_merge_infra_rerun_sha = excluded.post_merge_infra_rerun_sha,
			release_backfill_abandoned = excluded.release_backfill_abandoned,
			jira_done_notified = excluded.jira_done_notified
	`,
		pr.PRNumber, repo, pr.PRURL, pr.IssueNumber, pr.BranchName, pr.HeadSHA,
		string(pr.Stage), string(pr.CIStatus),
		nullTime(pr.LastChecked), nullTime(pr.CIWaitStartedAt),
		pr.MergeAttempts, pr.Error, nullTime(pr.CreatedAt),
		pr.ReleaseVersion, string(pr.ReleaseBumpType), pr.MergeNotificationPosted,
		pr.ApprovalRequestID, pr.ApprovalDecision, nullTime(pr.ApprovalRequestedAt),
		pr.PostMergeSHA, nullTime(pr.PostMergeCIStartedAt), pr.RebaseAttempts, pr.ScopeKey,
		pr.PRTitle, pr.MergeFollowupPosted, pr.InfraRerunCount, pr.InfraRerunSHA,
		pr.Parked, pr.EscalationReason, pr.RebaseHoldActive, pr.ReadoptCount,
		pr.BreakerHoldActive, pr.BreakerReadoptCount,
		pr.PostMergeInfraRerunCount, pr.PostMergeInfraRerunSHA,
		pr.ReleaseBackfillAbandoned, pr.JiraDoneNotified,
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
			post_merge_sha, post_merge_ci_started_at, rebase_attempts, scope_key,
			pr_title, merge_followup_posted, infra_rerun_count, infra_rerun_sha,
			parked, escalation_reason, rebase_hold_active, readopt_count,
			breaker_hold_active, breaker_readopt_count,
			post_merge_infra_rerun_count, post_merge_infra_rerun_sha,
			release_backfill_abandoned, jira_done_notified
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
			post_merge_sha, post_merge_ci_started_at, rebase_attempts, scope_key,
			pr_title, merge_followup_posted, infra_rerun_count, infra_rerun_sha,
			parked, escalation_reason, rebase_hold_active, readopt_count,
			breaker_hold_active, breaker_readopt_count,
			post_merge_infra_rerun_count, post_merge_infra_rerun_sha,
			release_backfill_abandoned, jira_done_notified
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
			&pr.PostMergeSHA, &postMergeCIStartedAt, &pr.RebaseAttempts, &pr.ScopeKey,
			&pr.PRTitle, &pr.MergeFollowupPosted, &pr.InfraRerunCount, &pr.InfraRerunSHA,
			&pr.Parked, &pr.EscalationReason, &pr.RebaseHoldActive, &pr.ReadoptCount,
			&pr.BreakerHoldActive, &pr.BreakerReadoptCount,
			&pr.PostMergeInfraRerunCount, &pr.PostMergeInfraRerunSHA,
			&pr.ReleaseBackfillAbandoned, &pr.JiraDoneNotified,
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
		&pr.PostMergeSHA, &postMergeCIStartedAt, &pr.RebaseAttempts, &pr.ScopeKey,
		&pr.PRTitle, &pr.MergeFollowupPosted, &pr.InfraRerunCount, &pr.InfraRerunSHA,
		&pr.Parked, &pr.EscalationReason, &pr.RebaseHoldActive, &pr.ReadoptCount,
		&pr.BreakerHoldActive, &pr.BreakerReadoptCount,
		&pr.PostMergeInfraRerunCount, &pr.PostMergeInfraRerunSHA,
		&pr.ReleaseBackfillAbandoned, &pr.JiraDoneNotified,
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

// ScopeRelease is one durable scope-release row: the set of merged member PRs
// held under release Trigger "on_scope_close" for a single epic/label scope,
// and the carrier release's progress through pending -> releasing ->
// done/failed (GH-3990).
type ScopeRelease struct {
	Repo       string
	ScopeKey   string
	ScopeTitle string
	MemberPRs  []int
	AnchorPR   int
	State      string
	FinalSHA   string
	Tag        string
	Attempts   int
	// LastFailedSHA is the main-HEAD SHA this scope last failed a carrier
	// against (GH-4331). Empty until the first genuine carrier failure.
	LastFailedSHA string
	// TimeoutAttempts counts consecutive "post-merge CI timeout" carrier
	// failures (GH-4643). Unlike Attempts, this is never reset by
	// recoverFailedScopeReleases's SHA-advance resurrection — it resets only
	// when a non-timeout failure reason is recorded, so a structurally-
	// unresolvable timeout accumulates toward the park threshold across
	// resurrections instead of getting a fresh budget on every main advance.
	TimeoutAttempts int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// encodeIntCSV renders a sorted []int as a comma-separated string for storage
// in a TEXT column (autopilot_scope_release.member_prs).
func encodeIntCSV(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

// decodeIntCSV parses a comma-separated int list written by encodeIntCSV.
// Malformed entries are skipped rather than erroring — a corrupt stored value
// should degrade to "fewer members" rather than fail the caller.
func decodeIntCSV(s string) []int {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	return nums
}

// EnqueueScopeRelease durably records that scopeKey's members are ready to
// release as one carrier once startPendingScopeReleases claims the row.
// Idempotent: a second call for the same (repo, scopeKey) is a no-op via
// INSERT OR IGNORE, so the epic-close reactive path and the closed-epic
// lookback sweep can both call it without duplicating or clobbering an
// in-flight/completed row. memberPRs should be pre-sorted ascending; anchor_pr
// is its highest element (GH-3990).
func (s *StateStore) EnqueueScopeRelease(repo, scopeKey, scopeTitle string, memberPRs []int) error {
	anchor := 0
	if len(memberPRs) > 0 {
		anchor = memberPRs[len(memberPRs)-1]
	}
	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO autopilot_scope_release
			(repo, scope_key, scope_title, member_prs, anchor_pr, state)
		VALUES (?, ?, ?, ?, ?, 'pending')
	`, repo, scopeKey, scopeTitle, encodeIntCSV(memberPRs), anchor)
	return err
}

// ClaimScopeRelease atomically transitions one pending scope-release row to
// 'releasing'. Returns claimed=true only when THIS call performed the
// transition (RowsAffected==1) — the same single-winner discipline
// PersistedReleasingAge/ClaimExecution-style callers rely on elsewhere, so the
// epicParentTicker sweep and a startup backstop can both attempt the same row
// without registering two carriers for it (GH-3990).
func (s *StateStore) ClaimScopeRelease(repo, scopeKey string) (bool, error) {
	result, err := s.db.Exec(`
		UPDATE autopilot_scope_release
		SET state = 'releasing', updated_at = CURRENT_TIMESTAMP
		WHERE repo = ? AND scope_key = ? AND state = 'pending'
	`, repo, scopeKey)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ClaimSpawnedFix atomically records that a fix issue is about to be created
// for (repo, dedupKey). Returns claimed=true only when THIS call performed
// the insert (single-winner discipline, mirrors ClaimScopeRelease above) — a
// re-tick, a release-scan re-discovery, or a second daemon observing the same
// failure signal all lose the claim and must skip creating a duplicate fix
// issue (GH-4307).
func (s *StateStore) ClaimSpawnedFix(repo, dedupKey string) (bool, error) {
	result, err := s.db.Exec(`
		INSERT OR IGNORE INTO autopilot_spawned_fixes (repo, dedup_key)
		VALUES (?, ?)
	`, repo, dedupKey)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RecordSpawnedFixIssue backfills the created issue number onto an
// already-claimed dedup row once CreatePilotIssue succeeds.
func (s *StateStore) RecordSpawnedFixIssue(repo, dedupKey string, issueNumber int) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_spawned_fixes SET issue_number = ? WHERE repo = ? AND dedup_key = ?
	`, issueNumber, repo, dedupKey)
	return err
}

// ReleaseSpawnedFix deletes the claim row for (repo, dedupKey) taken by
// ClaimSpawnedFix. GH-4856: call this when the create-issue call that
// followed a successful claim fails synchronously (e.g. a transient
// CreatePilotIssue error) — without it, the row survives with
// issue_number=0 forever (RecordSpawnedFixIssue is only ever reached on the
// success path), permanently poisoning the dedup key: every future attempt
// hits the dedup-hit branch and gets back (0, nil) with no way to ever
// record a real issue number. Deleting the row lets the next attempt
// re-claim and retry cleanly.
func (s *StateStore) ReleaseSpawnedFix(repo, dedupKey string) error {
	_, err := s.db.Exec(`
		DELETE FROM autopilot_spawned_fixes WHERE repo = ? AND dedup_key = ?
	`, repo, dedupKey)
	return err
}

// GetSpawnedFixIssue returns the issue number recorded for (repo, dedupKey),
// or 0 if the claim row exists but RecordSpawnedFixIssue hasn't landed yet
// (a create is still in flight) or the row doesn't exist.
func (s *StateStore) GetSpawnedFixIssue(repo, dedupKey string) (int, error) {
	var issueNumber int
	err := s.db.QueryRow(`
		SELECT issue_number FROM autopilot_spawned_fixes WHERE repo = ? AND dedup_key = ?
	`, repo, dedupKey).Scan(&issueNumber)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return issueNumber, nil
}

// HasSpawnedFixForPR reports whether a fix/review issue has already been
// durably claimed for prNumber, regardless of failure type or the exact set
// of failed checks — a prefix match on the "fix:pr<N>:" dedup-key namespace
// (spawnedFixDedupKey) rather than an exact-key lookup like GetSpawnedFixIssue.
// Returns the most recently recorded issue number, or 0 if no claim has been
// backfilled with a live issue number yet (no claim at all, or a claim still
// in flight between ClaimSpawnedFix and RecordSpawnedFixIssue).
//
// GH-4841: notifyExternalClose uses this as a durable fallback for
// prState.TerminalLabel, which is in-memory only and can be lost to a daemon
// restart landing between a CI-failure/review-feedback PR close and the
// end-of-cycle persistPRState call. The claim row this reads was written by
// CreateFailureIssue/CreateReviewIssue synchronously, before either handler
// ever closes the PR — so it survives exactly the restart window that loses
// prState.TerminalLabel.
func (s *StateStore) HasSpawnedFixForPR(repo string, prNumber int) (int, error) {
	var issueNumber int
	// GH-4852 nit: created_at is DATETIME DEFAULT CURRENT_TIMESTAMP, i.e.
	// second resolution — two claims for the same PR landing within the same
	// second (fast re-tick, or a CI-fix round immediately followed by a
	// review round on the replacement PR) would otherwise tie and fall back
	// to SQLite's undefined row order. rowid is monotonically assigned on
	// insert for this ordinary (non-WITHOUT ROWID) table, so it's a stable
	// tiebreaker for "most recently recorded".
	err := s.db.QueryRow(`
		SELECT issue_number FROM autopilot_spawned_fixes
		WHERE repo = ? AND dedup_key LIKE ? AND issue_number > 0
		ORDER BY created_at DESC, rowid DESC LIMIT 1
	`, repo, fmt.Sprintf("fix:pr%d:%%", prNumber)).Scan(&issueNumber)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return issueNumber, nil
}

// GetScopeRelease returns one scope-release row by repo+scopeKey, or nil, nil
// if not found.
func (s *StateStore) GetScopeRelease(repo, scopeKey string) (*ScopeRelease, error) {
	row := s.db.QueryRow(`
		SELECT repo, scope_key, scope_title, member_prs, anchor_pr, state, final_sha, tag, attempts, last_failed_sha, timeout_attempts, created_at, updated_at
		FROM autopilot_scope_release WHERE repo = ? AND scope_key = ?
	`, repo, scopeKey)
	sr, err := scanScopeRelease(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sr, nil
}

// ListScopeReleases returns every scope-release row for repo whose state is
// one of states.
func (s *StateStore) ListScopeReleases(repo string, states ...string) ([]*ScopeRelease, error) {
	if len(states) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(states))
	args := make([]interface{}, 0, len(states)+1)
	args = append(args, repo)
	for i, st := range states {
		placeholders[i] = "?"
		args = append(args, st)
	}
	query := fmt.Sprintf(`
		SELECT repo, scope_key, scope_title, member_prs, anchor_pr, state, final_sha, tag, attempts, last_failed_sha, timeout_attempts, created_at, updated_at
		FROM autopilot_scope_release WHERE repo = ? AND state IN (%s)
	`, strings.Join(placeholders, ", "))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*ScopeRelease
	for rows.Next() {
		sr, err := scanScopeRelease(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

// MarkScopeReleaseDone marks a scope-release row terminal-success. tag is ""
// for a no-op release (BumpNone — the scope's commits carry no releasable
// change), recorded so a no-op scope never retries. GH-4331: an empty tag
// here never blanks an already-recorded tag — a restart replay re-observing
// the same completed scope as a no-op (e.g. after CompareCommits sees
// nothing new post-tag) must not erase the tag a prior pass already wrote.
func (s *StateStore) MarkScopeReleaseDone(repo, scopeKey, tag, finalSHA string) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_scope_release
		SET state = 'done', tag = CASE WHEN ? = '' THEN tag ELSE ? END, final_sha = ?, updated_at = CURRENT_TIMESTAMP
		WHERE repo = ? AND scope_key = ?
	`, tag, tag, finalSHA, repo, scopeKey)
	return err
}

// MarkScopeReleasePending re-queues a scope-release row as 'pending' so the
// next startPendingScopeReleases sweep claims a fresh carrier for it.
// incrementAttempts distinguishes a genuine carrier failure (true — bumps the
// retry-cap counter handleScopeReleaseFailure checks, and records failedSHA as
// the main-HEAD SHA that failed) from crash recovery (false — a 'releasing'
// row left with no live carrier after a restart, which is not itself a
// failure of the release; failedSHA is ignored in that case).
//
// GH-4331: rows already terminal ('failed' or 'done') are never matched —
// without this guard, a zombie carrier rehydrated from a persisted PRState
// whose scope already resolved terminal would bounce the row
// failed->pending->failed forever every tick, inflating attempts far past
// maxScopeReleaseAttempts. Recovery for a genuinely stuck 'failed' row is
// ResetScopeReleaseForRetry, called only once main has moved past
// LastFailedSHA. GH-4643: 'parked' rows are excluded on the same terms — a
// parked scope is never resurrected by SHA-advance recovery, only by manual
// intervention, so an in-flight zombie carrier for it must not bounce it back
// to pending either.
func (s *StateStore) MarkScopeReleasePending(repo, scopeKey string, incrementAttempts bool, failedSHA string) error {
	q := `UPDATE autopilot_scope_release SET state = 'pending', updated_at = CURRENT_TIMESTAMP WHERE repo = ? AND scope_key = ? AND state NOT IN ('failed', 'done', 'parked')`
	args := []interface{}{repo, scopeKey}
	if incrementAttempts {
		q = `UPDATE autopilot_scope_release SET state = 'pending', attempts = attempts + 1, last_failed_sha = CASE WHEN ? = '' THEN last_failed_sha ELSE ? END, updated_at = CURRENT_TIMESTAMP WHERE repo = ? AND scope_key = ? AND state NOT IN ('failed', 'done', 'parked')`
		args = []interface{}{failedSHA, failedSHA, repo, scopeKey}
	}
	_, err := s.db.Exec(q, args...)
	return err
}

// MarkScopeReleaseFailed marks a scope-release row terminal-failed, once
// handleScopeReleaseFailure's attempt cap has been exceeded.
func (s *StateStore) MarkScopeReleaseFailed(repo, scopeKey string) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_scope_release SET state = 'failed', updated_at = CURRENT_TIMESTAMP
		WHERE repo = ? AND scope_key = ?
	`, repo, scopeKey)
	return err
}

// MarkScopeReleaseParked marks a scope-release row terminal-parked (GH-4643):
// distinct from 'failed' specifically so recoverFailedScopeReleases — which
// only ever lists state='failed' rows — never resurrects it. Reached after
// maxScopeReleaseTimeoutAttempts consecutive "post-merge CI timeout"
// failures, the signature of a repo with no post-merge CI configured at all
// (a required-checks allowlist naming a check that will never post), where
// resurrecting on every main-branch advance just re-times-out forever.
func (s *StateStore) MarkScopeReleaseParked(repo, scopeKey string) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_scope_release SET state = 'parked', updated_at = CURRENT_TIMESTAMP
		WHERE repo = ? AND scope_key = ?
	`, repo, scopeKey)
	return err
}

// UpdateScopeReleaseTimeoutAttempts bumps (isTimeout=true) or resets to zero
// (isTimeout=false) a scope-release row's timeout_attempts counter and
// returns the resulting value (GH-4643). Guarded by the same terminal-state
// exclusion as MarkScopeReleasePending so a row already resolved terminal
// between the caller's MarkScopeReleasePending call and this one is left
// untouched. Returns 0, nil if the row doesn't exist or is terminal (no
// change made) rather than erroring — callers treat that as "nothing to cap
// against yet".
func (s *StateStore) UpdateScopeReleaseTimeoutAttempts(repo, scopeKey string, isTimeout bool) (int, error) {
	set := "timeout_attempts = 0"
	if isTimeout {
		set = "timeout_attempts = timeout_attempts + 1"
	}
	q := fmt.Sprintf(`
		UPDATE autopilot_scope_release SET %s, updated_at = CURRENT_TIMESTAMP
		WHERE repo = ? AND scope_key = ? AND state NOT IN ('failed', 'done', 'parked')
	`, set)
	if _, err := s.db.Exec(q, repo, scopeKey); err != nil {
		return 0, err
	}

	var count int
	err := s.db.QueryRow(`
		SELECT timeout_attempts FROM autopilot_scope_release WHERE repo = ? AND scope_key = ?
	`, repo, scopeKey).Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}

// ResetScopeReleaseForRetry resurrects a terminal 'failed' scope-release row
// for a fresh attempt, resetting attempts to 0 so the new carrier gets the
// full retry budget. Callers (recoverFailedScopeReleases) must only invoke
// this once main has moved past the SHA the row last failed against — this
// method itself does not compare SHAs, it just performs the resurrection
// (GH-4331).
func (s *StateStore) ResetScopeReleaseForRetry(repo, scopeKey string) error {
	_, err := s.db.Exec(`
		UPDATE autopilot_scope_release
		SET state = 'pending', attempts = 0, updated_at = CURRENT_TIMESTAMP
		WHERE repo = ? AND scope_key = ? AND state = 'failed'
	`, repo, scopeKey)
	return err
}

// ScopeMemberPending reports whether prNumber is a member of any non-terminal
// (pending or releasing) scope-release row for repo. ScanRecentlyMergedPRs
// consults this to skip a member PR whose scope has already completed and
// closed the epic parent (heldByScope would fail open there) — otherwise the
// scanner would cut a redundant per-merge tag for it ahead of the carrier
// (GH-3990).
func (s *StateStore) ScopeMemberPending(repo string, prNumber int) (bool, error) {
	rows, err := s.db.Query(`
		SELECT member_prs FROM autopilot_scope_release
		WHERE repo = ? AND state IN ('pending', 'releasing')
	`, repo)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var memberCSV string
		if err := rows.Scan(&memberCSV); err != nil {
			return false, err
		}
		for _, n := range decodeIntCSV(memberCSV) {
			if n == prNumber {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

// scanScopeRelease scans one row into a ScopeRelease via the given scan func —
// satisfied by both *sql.Row.Scan (GetScopeRelease) and *sql.Rows.Scan
// (ListScopeReleases), so both callers share one column-order source of truth.
func scanScopeRelease(scan func(dest ...interface{}) error) (*ScopeRelease, error) {
	var sr ScopeRelease
	var memberCSV string
	var createdAt, updatedAt sql.NullTime
	err := scan(
		&sr.Repo, &sr.ScopeKey, &sr.ScopeTitle, &memberCSV, &sr.AnchorPR,
		&sr.State, &sr.FinalSHA, &sr.Tag, &sr.Attempts, &sr.LastFailedSHA, &sr.TimeoutAttempts, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	sr.MemberPRs = decodeIntCSV(memberCSV)
	if createdAt.Valid {
		sr.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		sr.UpdatedAt = updatedAt.Time
	}
	return &sr, nil
}
