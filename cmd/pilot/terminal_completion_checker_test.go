package main

import (
	"os"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// newTerminalCompletionCheckerTestStore creates a real on-disk store (no
// completed execution rows seeded), matching production schema, for tests
// that need to distinguish "gated by repick backoff" from "genuinely has a
// terminal completion" — both return true from HasCompletedExecution, but for
// different reasons, and only the DB-backed path proves the distinction.
func newTerminalCompletionCheckerTestStore(t *testing.T) *memory.Store {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "pilot-test-terminal-checker-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	store, err := memory.NewStore(tmpDir)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestTerminalCompletionChecker_GatedByBackoff_SkipsWithoutStoreLookup is the
// GH-4469 deliverable-1/3 core regression test: this is the exact hook wired
// as the vendored github SDK poller's ExecutionChecker
// (studio-sdk/sdk/integrations/github/poller.go hasCompletedExecution), the
// EARLIEST checkpoint in its per-issue candidate loop — running before
// scope-grouping, the fresh-label GH API refresh, the pre-flight judge
// subprocess, markProcessed, board-sync, and the dispatch/claim-insert. A
// task with NO completed execution row would ordinarily report false here;
// once its repick backoff key is gated, HasCompletedExecution must report
// true anyway so the poller treats it exactly like an already-completed
// issue and skips the entire rest of the expensive loop.
func TestTerminalCompletionChecker_GatedByBackoff_SkipsWithoutStoreLookup(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	checker := terminalCompletionChecker{store: store}

	taskID := "GH-4391"
	projectPath := "/tmp/pilot-gh-4469-gate-test-does-not-exist"
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	// Sanity: with no backoff armed and no completed row, this must be false.
	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("expected false for a task with neither a completed row nor an armed backoff")
	}

	// Arm the backoff (simulating a prior dropped pickup) and verify the
	// checker now reports true — the poller-visible signal for "skip this
	// tick" — even though there is still no completed execution row.
	repickBackoff.recordDrop(key)

	done, err = checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error while gated: %v", err)
	}
	if !done {
		t.Fatal("expected HasCompletedExecution to report true while the repick backoff is armed, so the poller skips this candidate entirely")
	}
}

// TestTerminalCompletionChecker_GateExpiry_ResumesNormalCheck verifies that
// once the backoff window has elapsed, HasCompletedExecution falls back to
// the real terminal-completion check instead of remaining stuck reporting
// true forever.
func TestTerminalCompletionChecker_GateExpiry_ResumesNormalCheck(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	checker := terminalCompletionChecker{store: store}

	taskID := "GH-4391-EXPIRE"
	projectPath := "/tmp/pilot-gh-4469-gate-expiry-test-does-not-exist"
	key := repickBackoffKey(projectPath, taskID)
	t.Cleanup(func() { repickBackoff.recordSuccess(key) })

	repickBackoff.recordDrop(key)
	if done, _ := checker.HasCompletedExecution(taskID, projectPath); !done {
		t.Fatal("expected the task to be gated immediately after a drop")
	}

	// Force the window to have already elapsed, as if next_allowed_at passed.
	repickBackoff.mu.Lock()
	repickBackoff.entries[key].nextAllowedAt = time.Now().Add(-time.Second)
	repickBackoff.mu.Unlock()

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error after gate expiry: %v", err)
	}
	if done {
		t.Fatal("expected the gate to expire and fall back to the real (false) terminal-completion check")
	}
}

// TestTerminalCompletionChecker_GenuineCompletion_StillReportsTrue verifies
// GH-4469's new gate does not regress the pre-existing GH-4347 behavior: a
// task with a real completed execution row must still report true even with
// no backoff involved at all.
func TestTerminalCompletionChecker_GenuineCompletion_StillReportsTrue(t *testing.T) {
	store := newTerminalCompletionCheckerTestStore(t)
	checker := terminalCompletionChecker{store: store}

	taskID := "GH-4391-GENUINE"
	projectPath := "/tmp/pilot-gh-4469-genuine-completion-does-not-exist"

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-gh-4469-genuine", TaskID: taskID, ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/qf-studio/pilot-canary-sandbox/pull/1",
	}); err != nil {
		t.Fatalf("failed to seed completed execution: %v", err)
	}

	done, err := checker.HasCompletedExecution(taskID, projectPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !done {
		t.Fatal("expected a genuinely completed task to still report true")
	}
}
