package memory

import (
	"os"
	"testing"
)

// TestReclassifyLegacyOutcomes verifies the one-time backfill (TASK-358) moves
// historically-misclassified 'failed' rows into their true terminal outcome
// (no_op / stalled) while leaving genuine failures untouched, and that the fix
// is reflected in GetLifetimeTaskCounts.
func TestReclassifyLegacyOutcomes(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-reclassify-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	// All inserted as the legacy collapsed status='failed'.
	seed := []Execution{
		{ID: "e1", TaskID: "GH-1", ProjectPath: "/p", Status: "failed", Error: "no new commit produced — post-push SHA matches base branch"},
		{ID: "e2", TaskID: "GH-2", ProjectPath: "/p", Status: "failed", Error: "no_changes: Claude completed but made no code changes after retry"},
		{ID: "e3", TaskID: "GH-3", ProjectPath: "/p", Status: "failed", Error: "no_changes: branch has no commits relative to base (PR guard)"},
		{ID: "e4", TaskID: "GH-4", ProjectPath: "/p", Status: "failed", Error: "session stalled: no agent event for >10m0s"},
		{ID: "e5", TaskID: "GH-5", ProjectPath: "/p", Status: "failed", Error: "per-task budget limit exceeded: tokens"},
		{ID: "e6", TaskID: "GH-6", ProjectPath: "/p", Status: "failed", Error: "go build: undefined: Foo"}, // genuine failure
		{ID: "e7", TaskID: "GH-7", ProjectPath: "/p", Status: "completed"},                                 // untouched
	}
	for i := range seed {
		if err := store.SaveExecution(&seed[i]); err != nil {
			t.Fatalf("SaveExecution(%s) failed: %v", seed[i].ID, err)
		}
	}

	if err := store.reclassifyLegacyOutcomes(); err != nil {
		t.Fatalf("reclassifyLegacyOutcomes failed: %v", err)
	}

	wantStatus := map[string]string{
		"e1": "no_op",
		"e2": "no_op",
		"e3": "no_op",
		"e4": "stalled",
		"e5": "stalled",
		"e6": "failed",    // genuine failure stays failed
		"e7": "completed", // untouched
	}
	for id, want := range wantStatus {
		got, err := store.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", id, err)
		}
		if got.Status != want {
			t.Errorf("%s status = %q, want %q", id, got.Status, want)
		}
	}

	counts, err := store.GetLifetimeTaskCounts()
	if err != nil {
		t.Fatalf("GetLifetimeTaskCounts: %v", err)
	}
	if counts.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (only the genuine build failure)", counts.Failed)
	}
	if counts.NoOp != 3 {
		t.Errorf("NoOp = %d, want 3", counts.NoOp)
	}
	if counts.Stalled != 2 {
		t.Errorf("Stalled = %d, want 2", counts.Stalled)
	}
	if counts.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", counts.Succeeded)
	}
	if counts.Total != len(seed) {
		t.Errorf("Total = %d, want %d", counts.Total, len(seed))
	}
}

// TestReclassifyLegacyOutcomesIdempotent verifies re-running the backfill makes
// no further changes (it runs on every startup via migrate()).
func TestReclassifyLegacyOutcomesIdempotent(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-reclassify-idem-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "e1", TaskID: "GH-1", ProjectPath: "/p", Status: "failed", Error: "no new commit produced"})

	for i := 0; i < 3; i++ {
		if err := store.reclassifyLegacyOutcomes(); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	got, _ := store.GetExecution("e1")
	if got.Status != "no_op" {
		t.Errorf("status = %q, want no_op after repeated runs", got.Status)
	}
}

// TestSelfHealPromotesNoOp verifies that a row the dispatcher now classifies as a
// no-op (rather than failed) still heals to completed when its PR is observed
// merged — the heal scope was broadened in TASK-358.
func TestSelfHealPromotesNoOp(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "pilot-heal-noop-*")
	defer func() { _ = os.RemoveAll(tmpDir) }()

	store, err := NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	_ = store.SaveExecution(&Execution{ID: "e1", TaskID: "GH-9", ProjectPath: "/p", Status: "no_op", Error: "no new commit produced"})

	if err := store.SelfHealExecutionAfterMerge("GH-9", "/p", "https://github.com/o/r/pull/9"); err != nil {
		t.Fatalf("SelfHealExecutionAfterMerge: %v", err)
	}
	got, _ := store.GetExecution("e1")
	if got.Status != "completed" {
		t.Errorf("status = %q, want completed", got.Status)
	}
	if got.PRUrl != "https://github.com/o/r/pull/9" {
		t.Errorf("PRUrl = %q, want the merged PR url", got.PRUrl)
	}
}
