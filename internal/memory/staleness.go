package memory

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// GH-4569: a stale ledger answers every query successfully with wrong
// data — the worst failure shape. After the S6-lite cutover (TASK-409) a
// laptop's ~/.pilot/data/pilot.db sat frozen for 11 days while looking
// perfectly healthy; a session reading it confidently misdiagnosed
// merged/healthy tasks as failed. This file gives every ledger-reading
// entry point a way to notice and say so loudly, and gives operators a way
// to permanently retire a DB so the next copy of it (backup, restored
// snapshot, second machine) can't spring the same trap.

// DefaultStalenessWarnAfter is the default age (measured from the newest
// execution row's created_at to now) after which a ledger is considered
// stale enough to warn about. Configurable via config.Ledger.StalenessWarnAfter
// (ledger.staleness_warn_after in YAML), wired through SetStalenessThreshold.
const DefaultStalenessWarnAfter = 72 * time.Hour

// ArchiveSentinelFilename is the marker file operators drop next to a
// retired ledger DB (sibling of pilot.db, i.e. directly inside the
// configured data directory) to mark it as permanently archived. Its
// presence unconditionally triggers the staleness banner on every
// ledger-reading command, and makes NewStoreGuarded (pilot start) refuse to
// run against it outright.
const ArchiveSentinelFilename = "LEDGER-ARCHIVED"

var (
	stalenessMu        sync.RWMutex
	stalenessWarnAfter = DefaultStalenessWarnAfter
)

// SetStalenessThreshold overrides the default staleness age threshold
// (config.Ledger.StalenessWarnAfter / ledger.staleness_warn_after). A
// non-positive duration is ignored — it must not be possible to silently
// disable the banner via a zero-value config field left over from a
// partially-filled YAML block.
func SetStalenessThreshold(d time.Duration) {
	if d <= 0 {
		return
	}
	stalenessMu.Lock()
	defer stalenessMu.Unlock()
	stalenessWarnAfter = d
}

// StalenessThreshold returns the currently configured staleness age
// threshold.
func StalenessThreshold() time.Duration {
	stalenessMu.RLock()
	defer stalenessMu.RUnlock()
	return stalenessWarnAfter
}

// Path returns the data directory this Store was opened with (the
// directory containing pilot.db, not the db file itself).
func (s *Store) Path() string {
	return s.path
}

// DBFilePath returns the path to the pilot.db file itself.
func (s *Store) DBFilePath() string {
	return filepath.Join(s.path, "pilot.db")
}

// ArchiveSentinelPath returns the path checked for the archive marker file.
func (s *Store) ArchiveSentinelPath() string {
	return filepath.Join(s.path, ArchiveSentinelFilename)
}

// newestExecutionCreatedAt returns the created_at of the most recent
// execution row, and false if the executions table is empty (a genuine
// fresh-init ledger, which must never be flagged stale).
//
// Deliberately uses ORDER BY created_at DESC LIMIT 1 rather than
// SELECT MAX(created_at): the sqlite driver only maps declared-typed
// columns back to time.Time, not aggregate expressions (see the identical
// note on ListProjectsForTask in store.go).
func (s *Store) newestExecutionCreatedAt() (time.Time, bool, error) {
	var createdAt time.Time
	err := s.db.QueryRow(`SELECT created_at FROM executions ORDER BY created_at DESC LIMIT 1`).Scan(&createdAt)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return createdAt, true, nil
}

// readArchiveSentinel reports whether an archive sentinel file exists in
// dataPath, returning its first line (trimmed) as a human-authored note.
func readArchiveSentinel(dataPath string) (firstLine string, exists bool, err error) {
	f, err := os.Open(filepath.Join(dataPath, ArchiveSentinelFilename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	if scanner.Scan() {
		firstLine = strings.TrimSpace(scanner.Text())
	}
	return firstLine, true, nil
}

// StalenessInfo describes what CheckStaleness found.
type StalenessInfo struct {
	// Stale is true when the newest execution row is older than the
	// configured threshold.
	Stale bool
	// Archived is true when an ArchiveSentinelFilename marker sits next to
	// the DB.
	Archived       bool
	ArchiveMessage string
	// HasHistory is false for a genuinely empty (fresh-init) ledger.
	HasHistory      bool
	NewestExecution time.Time
	Age             time.Duration
	DBPath          string
}

// Banner formats the loud stderr/dashboard warning for a StalenessInfo. It
// returns "" if there is nothing to warn about (fresh or healthy ledger).
func (info *StalenessInfo) Banner() string {
	if info == nil || (!info.Stale && !info.Archived) {
		return ""
	}
	if info.Archived {
		msg := fmt.Sprintf("\u26a0 LEDGER ARCHIVED — %s", info.DBPath)
		if info.ArchiveMessage != "" {
			msg += fmt.Sprintf(" — %s", info.ArchiveMessage)
		} else {
			msg += fmt.Sprintf(" (see %s)", ArchiveSentinelFilename)
		}
		return msg
	}
	days := int(info.Age.Hours() / 24)
	return fmt.Sprintf("\u26a0 LEDGER STALE — newest execution %s (%d days ago). Are you reading an archive? %s",
		info.NewestExecution.Format(time.RFC3339), days, info.DBPath)
}

// CheckStaleness inspects the store for both age-based staleness (newest
// execution older than threshold) and an archive sentinel file. A
// genuinely empty ledger (no execution rows yet, no sentinel) is always
// reported as fresh — never stale.
func (s *Store) CheckStaleness(threshold time.Duration) (*StalenessInfo, error) {
	info := &StalenessInfo{DBPath: s.DBFilePath()}

	firstLine, archived, err := readArchiveSentinel(s.path)
	if err != nil {
		return nil, err
	}
	info.Archived = archived
	info.ArchiveMessage = firstLine

	newest, ok, err := s.newestExecutionCreatedAt()
	if err != nil {
		return nil, err
	}
	if ok {
		info.HasHistory = true
		info.NewestExecution = newest
		info.Age = time.Since(newest)
		if threshold > 0 && info.Age > threshold {
			info.Stale = true
		}
	}

	return info, nil
}

// warnIfStale runs CheckStaleness with the configured threshold and prints
// the banner to stderr (never stdout, so it can't corrupt machine-readable
// command output) if warranted. Best-effort: a staleness-check failure
// (e.g. a transient read error) must never block a store open.
func (s *Store) warnIfStale() {
	info, err := s.CheckStaleness(StalenessThreshold())
	if err != nil || info == nil {
		return
	}
	if banner := info.Banner(); banner != "" {
		fmt.Fprintln(os.Stderr, banner)
	}
}

// ErrLedgerArchived is returned by NewStoreGuarded when an
// ArchiveSentinelFilename marker is present and the caller did not pass
// allowArchived (the --i-know-this-is-an-archive escape hatch). Retiring a
// DB with a sentinel is an explicit operator action; pilot start must not
// quietly run against it as if it were live.
type ErrLedgerArchived struct {
	DataPath string
	DBPath   string
	Message  string
}

func (e *ErrLedgerArchived) Error() string {
	msg := fmt.Sprintf("refusing to start: %s is marked archived (%s found in %s)",
		e.DBPath, ArchiveSentinelFilename, e.DataPath)
	if e.Message != "" {
		msg += fmt.Sprintf(" — %q", e.Message)
	}
	return msg + " — pass --i-know-this-is-an-archive to override for forensics"
}
