package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckStaleness_FreshDBNoBanner(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	store, err := NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	info, err := store.CheckStaleness(DefaultStalenessWarnAfter)
	if err != nil {
		t.Fatalf("CheckStaleness: %v", err)
	}
	if info.Stale || info.Archived {
		t.Fatalf("expected fresh DB to report healthy, got %+v", info)
	}
	if banner := info.Banner(); banner != "" {
		t.Fatalf("expected no banner for fresh DB, got %q", banner)
	}
}

func TestCheckStaleness_EmptyDBTreatedAsFreshInit(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	store, err := NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// executions table has zero rows — must never be flagged stale just
	// because there's no history yet.
	info, err := store.CheckStaleness(1 * time.Hour)
	if err != nil {
		t.Fatalf("CheckStaleness: %v", err)
	}
	if info.HasHistory {
		t.Fatal("expected HasHistory=false for an empty executions table")
	}
	if info.Stale {
		t.Fatal("expected empty DB to never be flagged stale (fresh-init)")
	}
	if banner := info.Banner(); banner != "" {
		t.Fatalf("expected no false-positive banner for empty DB, got %q", banner)
	}
}

func TestCheckStaleness_OldNewestExecutionWarns(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	store, err := NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := store.SaveExecution(&Execution{
		ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		CreatedAt: old,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	info, err := store.CheckStaleness(DefaultStalenessWarnAfter)
	if err != nil {
		t.Fatalf("CheckStaleness: %v", err)
	}
	if !info.Stale {
		t.Fatalf("expected a 10-day-old newest execution to be flagged stale (threshold %s), got %+v", DefaultStalenessWarnAfter, info)
	}
	banner := info.Banner()
	if banner == "" {
		t.Fatal("expected a non-empty banner for a stale ledger")
	}
	if !containsAll(banner, "LEDGER STALE", "days ago", store.DBFilePath()) {
		t.Fatalf("banner missing expected fragments: %q", banner)
	}
}

func TestCheckStaleness_RecentExecutionNoBanner(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	store, err := NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&Execution{
		ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	info, err := store.CheckStaleness(DefaultStalenessWarnAfter)
	if err != nil {
		t.Fatalf("CheckStaleness: %v", err)
	}
	if info.Stale {
		t.Fatalf("expected a 1-hour-old newest execution to NOT be stale, got %+v", info)
	}
	if banner := info.Banner(); banner != "" {
		t.Fatalf("expected no banner for a fresh execution, got %q", banner)
	}
}

func TestCheckStaleness_ArchiveSentinelAlwaysWarns(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "data")
	store, err := NewStore(dataPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	// Fresh execution, well within threshold — without a sentinel this
	// would not warn.
	if err := store.SaveExecution(&Execution{
		ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	sentinelMsg := "retired 2026-07-27 after S6-lite cutover — see README-MOVED.md"
	if err := os.WriteFile(store.ArchiveSentinelPath(), []byte(sentinelMsg+"\n"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	info, err := store.CheckStaleness(DefaultStalenessWarnAfter)
	if err != nil {
		t.Fatalf("CheckStaleness: %v", err)
	}
	if !info.Archived {
		t.Fatalf("expected sentinel presence to set Archived=true, got %+v", info)
	}
	banner := info.Banner()
	if banner == "" {
		t.Fatal("expected a non-empty banner when archive sentinel is present")
	}
	if !containsAll(banner, "LEDGER ARCHIVED", sentinelMsg) {
		t.Fatalf("banner missing expected fragments: %q", banner)
	}
}

func TestNewStoreGuarded_ArchiveSentinelRefusesStart(t *testing.T) {
	withIsolatedHome(t)
	dataPath := filepath.Join(t.TempDir(), "data")

	// First open (no sentinel yet) to create the DB.
	store, err := NewStoreGuarded(dataPath, false)
	if err != nil {
		t.Fatalf("NewStoreGuarded (initial): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dataPath, ArchiveSentinelFilename), []byte("archived for forensics\n"), 0644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	_, err = NewStoreGuarded(dataPath, false)
	if err == nil {
		t.Fatal("expected NewStoreGuarded to refuse starting against an archived ledger")
	}
	var archived *ErrLedgerArchived
	if !errors.As(err, &archived) {
		t.Fatalf("expected *ErrLedgerArchived, got %T: %v", err, err)
	}

	// The override flag must let it through for forensics.
	store2, err := NewStoreGuarded(dataPath, true)
	if err != nil {
		t.Fatalf("NewStoreGuarded with allowArchived=true should succeed, got: %v", err)
	}
	defer func() { _ = store2.Close() }()
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
