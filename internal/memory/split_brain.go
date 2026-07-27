package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lastKnownGoodRelPath is home-relative and deliberately independent of the
// configurable Memory.Path: its whole job is to catch the case where
// Memory.Path itself silently points somewhere new. GH-4393 — the
// 2026-07-16 cutover left an absolute path (`/Users/aleks.petrov/.pilot/data`)
// in config.yaml that the migration shim didn't cover, so the daemon
// auto-created a brand-new, empty ledger there and ran on it for three hours
// with no indication anything was wrong.
const lastKnownGoodRelPath = ".pilot/last_known_good.json"

// lastKnownGood records the state directory and execution count a prior,
// successful daemon start observed. It is refreshed on every healthy
// NewStoreGuarded call.
type lastKnownGood struct {
	DBPath         string    `json:"db_path"`
	ExecutionCount int       `json:"execution_count"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ErrSplitBrainLedger is returned by NewStoreGuarded when the state
// directory it was asked to open did not exist yet (i.e. was about to be
// silently auto-created) while a last-known-good marker shows this daemon
// previously ran with a non-empty ledger at a different resolved path. This
// is the exact shape of the GH-4393 incident: a shadow-path open that looks
// like a healthy first run but is actually silent divergence from the
// canonical ledger.
type ErrSplitBrainLedger struct {
	ConfiguredPath     string
	LastKnownGoodPath  string
	LastKnownGoodCount int
}

func (e *ErrSplitBrainLedger) Error() string {
	return fmt.Sprintf(
		"refusing to start: state directory for %q resolved to a brand-new, empty ledger, but this daemon previously ran with %d execution(s) recorded at %q — this looks like a shadow ledger (a misconfigured or unshimmed path); verify the memory.path config and any migration/cutover shims before starting",
		e.ConfiguredPath, e.LastKnownGoodCount, e.LastKnownGoodPath,
	)
}

func lastKnownGoodPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, lastKnownGoodRelPath), nil
}

// readLastKnownGood returns (nil, nil) if no marker has ever been written
// (e.g. a genuine first run), so callers can distinguish "no history" from
// an error reading the marker.
func readLastKnownGood() (*lastKnownGood, error) {
	path, err := lastKnownGoodPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lkg lastKnownGood
	if err := json.Unmarshal(data, &lkg); err != nil {
		return nil, fmt.Errorf("failed to parse last-known-good marker %s: %w", path, err)
	}
	return &lkg, nil
}

func writeLastKnownGood(dbPath string, executionCount int) error {
	path, err := lastKnownGoodPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create last-known-good marker directory: %w", err)
	}
	lkg := lastKnownGood{
		DBPath:         dbPath,
		ExecutionCount: executionCount,
		UpdatedAt:      time.Now(),
	}
	data, err := json.Marshal(lkg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// dirExists reports whether path already exists as a directory, i.e.
// whether opening it is NOT going to trigger an auto-create.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// NewStoreGuarded wraps NewStore with a split-brain guard (GH-4393): a
// daemon that has run before must never silently initialize an empty
// ledger. If the state directory did not exist before this call (NewStore
// is about to auto-create it) and a last-known-good marker shows real prior
// history at a different resolved path, this refuses to hand back the
// freshly-opened (necessarily empty) store and returns *ErrSplitBrainLedger
// instead.
//
// On a healthy open — including a genuine first run, where no marker exists
// yet — the marker is (re)written with the resolved path and current
// execution count so future starts can detect divergence.
//
// GH-4569: it also refuses to start against a ledger marked archived (an
// ArchiveSentinelFilename file next to the DB) unless allowArchived is set
// (the --i-know-this-is-an-archive escape hatch, for forensics only). The
// staleness/archive banner itself is still printed unconditionally by the
// underlying NewStore call, warn-only, regardless of allowArchived.
func NewStoreGuarded(dataPath string, allowArchived bool) (*Store, error) {
	preExisted := dirExists(dataPath)

	resolved, err := filepath.EvalSymlinks(dataPath)
	if err != nil {
		// Path doesn't exist yet (first run, or about to be auto-created by
		// NewStore below) — EvalSymlinks requires the path to exist, so fall
		// back to the unresolved path.
		resolved = dataPath
	}
	dbPath := filepath.Join(resolved, "pilot.db")

	lkg, lkgErr := readLastKnownGood()
	// A marker read failure (corrupt file, permissions) degrades the guard
	// to "no known history" rather than blocking startup on trouble
	// unrelated to the ledger itself.

	store, err := NewStore(dataPath)
	if err != nil {
		return nil, err
	}

	if archiveMsg, archived, sentinelErr := readArchiveSentinel(dataPath); sentinelErr == nil && archived && !allowArchived {
		_ = store.Close()
		return nil, &ErrLedgerArchived{
			DataPath: dataPath,
			DBPath:   dbPath,
			Message:  archiveMsg,
		}
	}

	if !preExisted && lkgErr == nil && lkg != nil && lkg.ExecutionCount > 0 && lkg.DBPath != dbPath {
		count, countErr := store.executionCount()
		if countErr == nil && count == 0 {
			_ = store.Close()
			return nil, &ErrSplitBrainLedger{
				ConfiguredPath:     dataPath,
				LastKnownGoodPath:  lkg.DBPath,
				LastKnownGoodCount: lkg.ExecutionCount,
			}
		}
	}

	if count, countErr := store.executionCount(); countErr == nil {
		// Best-effort: failing to refresh the marker must not block a
		// healthy store open.
		_ = writeLastKnownGood(dbPath, count)
	}

	return store, nil
}
