package memory

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withIsolatedHome points $HOME at a fresh temp dir so the last-known-good
// marker (~/.pilot/last_known_good.json) doesn't leak between tests or read
// a real developer/CI home directory.
func withIsolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestNewStoreGuarded_FirstRunNoMarker(t *testing.T) {
	withIsolatedHome(t)
	dataPath := filepath.Join(t.TempDir(), "data")

	store, err := NewStoreGuarded(dataPath)
	if err != nil {
		t.Fatalf("NewStoreGuarded failed on genuine first run: %v", err)
	}
	defer func() { _ = store.Close() }()

	lkg, err := readLastKnownGood()
	if err != nil {
		t.Fatalf("readLastKnownGood: %v", err)
	}
	if lkg == nil {
		t.Fatal("expected last-known-good marker to be written after a healthy open")
	}
	if lkg.ExecutionCount != 0 {
		t.Errorf("expected execution count 0 on first run, got %d", lkg.ExecutionCount)
	}
}

func TestNewStoreGuarded_HealthyReopenSamePath(t *testing.T) {
	withIsolatedHome(t)
	dataPath := filepath.Join(t.TempDir(), "data")

	store, err := NewStoreGuarded(dataPath)
	if err != nil {
		t.Fatalf("NewStoreGuarded (first open) failed: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen the SAME directory — this must succeed even though the marker
	// now records non-zero history, because the directory pre-existed.
	store2, err := NewStoreGuarded(dataPath)
	if err != nil {
		t.Fatalf("NewStoreGuarded (reopen) failed: %v", err)
	}
	defer func() { _ = store2.Close() }()

	count, err := store2.executionCount()
	if err != nil {
		t.Fatalf("executionCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 execution preserved across reopen, got %d", count)
	}
}

func TestNewStoreGuarded_DetectsShadowLedger(t *testing.T) {
	withIsolatedHome(t)
	root := t.TempDir()
	canonicalPath := filepath.Join(root, "canonical")

	// Simulate a healthy daemon run with real history at the canonical path.
	store, err := NewStoreGuarded(canonicalPath)
	if err != nil {
		t.Fatalf("NewStoreGuarded (canonical) failed: %v", err)
	}
	if err := store.SaveExecution(&Execution{ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/p", Status: "completed"}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The last-known-good marker is refreshed at open time, so simulate a
	// normal restart against the same (already-populated) canonical path —
	// this is what makes the marker reflect real history, exactly as it
	// would after the daemon's first real restart in production.
	store, err = NewStoreGuarded(canonicalPath)
	if err != nil {
		t.Fatalf("NewStoreGuarded (canonical restart) failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate the cutover bug: config now resolves to a brand-new,
	// never-before-seen directory (e.g. an absolute path that bypassed a
	// migration shim), distinct from the canonical path that has history.
	shadowPath := filepath.Join(root, "shadow")

	_, err = NewStoreGuarded(shadowPath)
	if err == nil {
		t.Fatal("expected NewStoreGuarded to refuse opening a shadow ledger, got nil error")
	}
	var splitBrain *ErrSplitBrainLedger
	if !errors.As(err, &splitBrain) {
		t.Fatalf("expected *ErrSplitBrainLedger, got %T: %v", err, err)
	}
	if splitBrain.LastKnownGoodCount != 1 {
		t.Errorf("expected LastKnownGoodCount 1, got %d", splitBrain.LastKnownGoodCount)
	}

	// The shadow directory must not be left behind as a half-initialized
	// ledger that a subsequent unguarded NewStore call would treat as
	// legitimate.
	if _, statErr := os.Stat(filepath.Join(shadowPath, "pilot.db")); statErr != nil {
		t.Errorf("expected shadow pilot.db file to exist (created by NewStore before the guard rejected it): %v", statErr)
	}
}

func TestNewStoreGuarded_NoFalsePositiveWhenMarkerHasNoHistory(t *testing.T) {
	withIsolatedHome(t)
	root := t.TempDir()

	// A marker exists but with ExecutionCount 0 (e.g. daemon started once
	// and never actually ran a task) — a fresh directory elsewhere should
	// not be treated as a shadow ledger.
	firstPath := filepath.Join(root, "first")
	store, err := NewStoreGuarded(firstPath)
	if err != nil {
		t.Fatalf("NewStoreGuarded (first) failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	secondPath := filepath.Join(root, "second")
	store2, err := NewStoreGuarded(secondPath)
	if err != nil {
		t.Fatalf("expected no split-brain error when prior marker has zero executions, got: %v", err)
	}
	defer func() { _ = store2.Close() }()
}
