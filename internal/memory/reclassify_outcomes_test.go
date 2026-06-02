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

	// All inserted as the legacy collapsed status='failed', one row per real-world
	// error signature observed in production (TASK-358 categorization).
	seed := []Execution{
		// no-op
		{ID: "e1", TaskID: "GH-1", ProjectPath: "/p", Status: "failed", Error: "no new commit produced — post-push SHA matches base branch"},
		{ID: "e2", TaskID: "GH-2", ProjectPath: "/p", Status: "failed", Error: "no_changes: Claude completed but made no code changes after retry"},
		{ID: "e3", TaskID: "GH-3", ProjectPath: "/p", Status: "failed", Error: "no_changes: branch has no commits relative to base (PR guard)"},
		{ID: "e3b", TaskID: "GH-3b", ProjectPath: "/p", Status: "failed", Error: "Claude completed but made no code changes after retry"}, // legacy, no prefix
		// stalled
		{ID: "e4", TaskID: "GH-4", ProjectPath: "/p", Status: "failed", Error: "session stalled: no agent event for >10m0s"},
		{ID: "e5", TaskID: "GH-5", ProjectPath: "/p", Status: "failed", Error: "per-task budget limit exceeded: tokens"},
		// rate-limited
		{ID: "e8", TaskID: "GH-8", ProjectPath: "/p", Status: "failed", Error: "You've hit your limit · resets 3pm (Europe/Podgorica)"},
		// skipped
		{ID: "e9", TaskID: "GH-9", ProjectPath: "/p", Status: "failed", Error: "stale queued task recovered (no worker picked up)"},
		{ID: "e10", TaskID: "GH-10", ProjectPath: "/p", Status: "failed", Error: "failed to start Claude Code: context canceled"},
		// infra
		{ID: "e11", TaskID: "GH-11", ProjectPath: "/p", Status: "failed", Error: "oom_killed: Process killed by SIGKILL (exit code 137)"},
		{ID: "e12", TaskID: "GH-12", ProjectPath: "/p", Status: "failed", Error: "push failed: failed to push: exit status 1"},
		{ID: "e13", TaskID: "GH-13", ProjectPath: "/p", Status: "failed", Error: "PR creation failed: failed to create PR: exit status 1"},
		// genuine failures (stay failed)
		{ID: "e6", TaskID: "GH-6", ProjectPath: "/p", Status: "failed", Error: "go build: undefined: Foo"},
		{ID: "e14", TaskID: "GH-14", ProjectPath: "/p", Status: "failed", Error: "PR creation refused: title is not a conventional commit"},
		{ID: "e15", TaskID: "GH-15", ProjectPath: "/p", Status: "failed", Error: "unknown: exit status 1"},
		// untouched
		{ID: "e7", TaskID: "GH-7", ProjectPath: "/p", Status: "completed"},
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
		"e1": "no_op", "e2": "no_op", "e3": "no_op", "e3b": "no_op",
		"e4": "stalled", "e5": "stalled",
		"e8": "rate_limited",
		"e9": "skipped", "e10": "skipped",
		"e11": "infra", "e12": "infra", "e13": "infra",
		"e6": "failed", "e14": "failed", "e15": "failed", // genuine failures stay failed
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
	assertCount := func(name string, got, want int) {
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	assertCount("Failed", counts.Failed, 3) // build error, title-refused, unknown exit
	assertCount("NoOp", counts.NoOp, 4)
	assertCount("Stalled", counts.Stalled, 2)
	assertCount("RateLimited", counts.RateLimited, 1)
	assertCount("Skipped", counts.Skipped, 2)
	assertCount("Infra", counts.Infra, 3)
	assertCount("Succeeded", counts.Succeeded, 1)
	assertCount("Total", counts.Total, len(seed))
	assertCount("NonFailure", counts.NonFailure(), 12)
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
