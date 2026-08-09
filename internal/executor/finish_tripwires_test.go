package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// recordingAlertProcessor implements AlertEventProcessor for finish-tripwire
// unit tests — it just captures every event it receives, safe for
// concurrent use since runFinishTripwireSweep has no ordering guarantee
// callers should rely on.
type recordingAlertProcessor struct {
	mu     sync.Mutex
	events []AlertEvent
}

func (p *recordingAlertProcessor) ProcessEvent(event AlertEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

func (p *recordingAlertProcessor) countByType(t AlertEventType) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.Type == t {
			n++
		}
	}
	return n
}

func (p *recordingAlertProcessor) countByTracker(tracker string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.events {
		if e.Metadata["tracker"] == tracker {
			n++
		}
	}
	return n
}

// initGitRepo creates a real git repository at dir so checkRootClean has
// something to run `git status --porcelain` against, mirroring how a task's
// ProjectPath is a real repo in production.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit("add", "README.md")
	runGit("commit", "-q", "-m", "initial")
}

func TestCheckRootClean(t *testing.T) {
	t.Run("empty ProjectPath is a no-op pass", func(t *testing.T) {
		result := checkRootClean(&memory.Execution{ProjectPath: ""})
		if result.violated {
			t.Errorf("expected no violation for empty ProjectPath, got %+v", result)
		}
	})

	t.Run("path that is not a git repo is a no-op pass (unobservable, not a finding)", func(t *testing.T) {
		result := checkRootClean(&memory.Execution{ID: "exec-1", ProjectPath: "/nonexistent/path/does-not-exist"})
		if result.violated {
			t.Errorf("expected no violation for an unobservable path, got %+v", result)
		}
	})

	t.Run("clean git repo passes", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		result := checkRootClean(&memory.Execution{ID: "exec-2", ProjectPath: dir})
		if result.violated {
			t.Errorf("expected no violation for a clean repo, got %+v", result)
		}
	})

	t.Run("dirty git repo (staged) fails", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "phantom.go"), []byte("package x\n"), 0o644); err != nil {
			t.Fatalf("write phantom file: %v", err)
		}
		cmd := exec.Command("git", "add", "phantom.go")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add failed: %v\n%s", err, out)
		}

		result := checkRootClean(&memory.Execution{ID: "exec-3", ProjectPath: dir})
		if !result.violated {
			t.Error("expected violation for a repo with a staged diff")
		}
		if result.reason == "" {
			t.Error("expected a non-empty reason on violation")
		}
	})

	t.Run("dirty git repo (unstaged) fails", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\nmodified\n"), 0o644); err != nil {
			t.Fatalf("modify README: %v", err)
		}

		result := checkRootClean(&memory.Execution{ID: "exec-4", ProjectPath: dir})
		if !result.violated {
			t.Error("expected violation for a repo with an unstaged diff")
		}
	})
}

func TestCheckLabelLifecycle(t *testing.T) {
	t.Run("CLI-driven execution (no source adapter) is a no-op pass", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		result := checkLabelLifecycle(store, &memory.Execution{ID: "exec-cli", TaskSourceAdapter: ""})
		if result.violated {
			t.Errorf("expected no violation for a CLI-driven execution, got %+v", result)
		}
	})

	t.Run("adapter-dispatched execution with zero execution_events fails", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		exec := &memory.Execution{ID: "exec-wired-to-nothing", TaskID: "GH-9001", ProjectPath: "/proj", TaskSourceAdapter: "github", Status: "completed"}
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}

		result := checkLabelLifecycle(store, exec)
		if !result.violated {
			t.Error("expected violation for an adapter-dispatched execution with no recorded events")
		}
	})

	t.Run("adapter-dispatched execution with recorded events passes", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		exec := &memory.Execution{ID: "exec-wired-ok", TaskID: "GH-9002", ProjectPath: "/proj", TaskSourceAdapter: "github", Status: "completed"}
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}
		if err := store.InsertExecutionEvent(exec.ID, memory.StageQueued, "started"); err != nil {
			t.Fatalf("InsertExecutionEvent: %v", err)
		}

		result := checkLabelLifecycle(store, exec)
		if result.violated {
			t.Errorf("expected no violation once at least one event is recorded, got %+v", result)
		}
	})
}

func TestCheckChildrenTerminal(t *testing.T) {
	const projectPath = "/project-finish-tripwire-children"

	t.Run("task with no decomposed children is a no-op pass", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		result := checkChildrenTerminal(store, &memory.Execution{ID: "exec-no-children", TaskID: "GH-7001", ProjectPath: projectPath})
		if result.violated {
			t.Errorf("expected no violation when no children were ever decomposed, got %+v", result)
		}
	})

	t.Run("parent finishes with a non-terminal child fails", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		parentExec := &memory.Execution{ID: "exec-parent-tripwire", TaskID: "GH-7011", ProjectPath: projectPath, Status: "completed"}
		if err := store.SaveExecution(parentExec); err != nil {
			t.Fatalf("SaveExecution(parent): %v", err)
		}
		if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into 1 children: #7012"); err != nil {
			t.Fatalf("InsertExecutionEvent: %v", err)
		}
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-GH-7012", TaskID: "GH-7012", ProjectPath: projectPath, Status: "running",
		}); err != nil {
			t.Fatalf("SaveExecution(child): %v", err)
		}

		result := checkChildrenTerminal(store, parentExec)
		if !result.violated {
			t.Error("expected violation when a decomposed child is still non-terminal")
		}
	})

	t.Run("parent finishes with all children terminal passes", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		parentExec := &memory.Execution{ID: "exec-parent-tripwire-ok", TaskID: "GH-7021", ProjectPath: projectPath, Status: "completed"}
		if err := store.SaveExecution(parentExec); err != nil {
			t.Fatalf("SaveExecution(parent): %v", err)
		}
		if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into 1 children: #7022"); err != nil {
			t.Fatalf("InsertExecutionEvent: %v", err)
		}
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-GH-7022", TaskID: "GH-7022", ProjectPath: projectPath,
			Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/7022",
		}); err != nil {
			t.Fatalf("SaveExecution(child): %v", err)
		}

		result := checkChildrenTerminal(store, parentExec)
		if result.violated {
			t.Errorf("expected no violation once every decomposed child is terminal, got %+v", result)
		}
	})
}

func TestCheckWorktreePruned(t *testing.T) {
	t.Run("no orphaned worktree, no commits-without-PR: passes", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{ID: "exec-clean", TaskID: "GH-8001"})
		if result.violated {
			t.Errorf("expected no violation, got %+v", result)
		}
	})

	t.Run("orphaned worktree directory still on disk fails", func(t *testing.T) {
		taskID := "GH-8011-orphan"
		dir := filepath.Join(os.TempDir(), "pilot-worktree-"+sanitizeBranchName(taskID)+"-99999")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create fake orphaned worktree dir: %v", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()

		result := checkWorktreePruned(&memory.Execution{ID: "exec-8011", TaskID: taskID})
		if !result.violated {
			t.Error("expected violation for an orphaned worktree directory still on disk")
		}
	})

	t.Run("commits with no PR when a PR was requested fails", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{
			ID: "exec-stranded", TaskID: "GH-8021", TaskCreatePR: true, CommitSHA: "deadbeef", PRUrl: "",
		})
		if !result.violated {
			t.Error("expected violation for committed-but-undelivered work")
		}
	})

	t.Run("commits with no PR is fine for a decomposed parent (no commit of its own)", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{
			ID: "exec-decomposed", TaskID: "GH-8031", TaskCreatePR: true, CommitSHA: "deadbeef", PRUrl: "",
			Status: string(ExecStatusDecomposed),
		})
		if result.violated {
			t.Errorf("expected no violation for a decomposed parent, got %+v", result)
		}
	})

	t.Run("commits with a PR present is fine", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{
			ID: "exec-shipped", TaskID: "GH-8041", TaskCreatePR: true, CommitSHA: "deadbeef",
			PRUrl: "https://github.com/qf-studio/pilot/pull/8041",
		})
		if result.violated {
			t.Errorf("expected no violation once a PR exists, got %+v", result)
		}
	})

	// GH-4817 (TASK-459 Phase 3 Task 6): commits with no PR is fine for a
	// terminal-by-design row (superseded/canceled) — mirrors the existing
	// decomposed-parent carve-out above. The PR was deliberately not carried
	// out, not silently discarded; this tripwire must not fire the exact
	// GH-4794 shape (alert-only variant).
	t.Run("commits with no PR is fine for a superseded row", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{
			ID: "exec-superseded", TaskID: "GH-8051", TaskCreatePR: true, CommitSHA: "deadbeef", PRUrl: "",
			Status: string(ExecStatusSuperseded),
		})
		if result.violated {
			t.Errorf("expected no violation for a superseded row, got %+v", result)
		}
	})

	t.Run("commits with no PR is fine for a canceled row", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{
			ID: "exec-canceled", TaskID: "GH-8061", TaskCreatePR: true, CommitSHA: "deadbeef", PRUrl: "",
			Status: string(ExecStatusCanceled),
		})
		if result.violated {
			t.Errorf("expected no violation for a canceled row, got %+v", result)
		}
	})

	t.Run("commits with no PR still fails for a plain failed row (guard is narrow)", func(t *testing.T) {
		result := checkWorktreePruned(&memory.Execution{
			ID: "exec-failed", TaskID: "GH-8071", TaskCreatePR: true, CommitSHA: "deadbeef", PRUrl: "",
			Status: string(ExecStatusFailed),
		})
		if !result.violated {
			t.Error("expected a violation for a plain failed row — the terminal-by-design carve-out must not swallow genuine failures")
		}
	})
}

// TestRunFinishTripwireSweep_EmitsAttemptForEveryCheck verifies the sweep
// records an attempt for all four trackers regardless of pass/fail, so a
// tracker's Stale() zero-attempts signal (the "wired to nothing" class this
// whole primitive exists to catch) never fires for a sweep that actually ran.
func TestRunFinishTripwireSweep_EmitsAttemptForEveryCheck(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-sweep-attempts", TaskID: "GH-9101", ProjectPath: "", Status: "completed"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	processor := &recordingAlertProcessor{}
	runFinishTripwireSweep(store, processor, exec.ID)

	if got := processor.countByType(AlertEventTypeDeadManAttempt); got != len(FinishTripwireTrackerNames) {
		t.Errorf("expected %d attempt events (one per tracker), got %d", len(FinishTripwireTrackerNames), got)
	}
	for _, name := range FinishTripwireTrackerNames {
		if processor.countByTracker(name) == 0 {
			t.Errorf("expected at least one event for tracker %q, got none", name)
		}
	}
}

// TestRunFinishTripwireSweep_NilProcessorIsSafe verifies the sweep runs
// (and doesn't panic) with no alerts engine configured — a nil processor is
// a documented, expected state (alerts disabled), not an error.
func TestRunFinishTripwireSweep_NilProcessorIsSafe(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-sweep-nil-processor", TaskID: "GH-9102", Status: "completed"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	runFinishTripwireSweep(store, nil, exec.ID)
}

// TestRunFinishTripwireSweep_NilStoreOrEmptyExecIDIsNoOp mirrors every other
// nil-store guard in this package.
func TestRunFinishTripwireSweep_NilStoreOrEmptyExecIDIsNoOp(t *testing.T) {
	runFinishTripwireSweep(nil, &recordingAlertProcessor{}, "some-id")

	store, cleanup := setupTestStore(t)
	defer cleanup()
	runFinishTripwireSweep(store, &recordingAlertProcessor{}, "")
}

// TestRunFinishTripwireSweep_MissingExecutionRowIsNoOp: a caller passing an
// execID with no matching row (shouldn't happen — Persist just wrote it —
// but defensively) must not panic and must not emit any events.
func TestRunFinishTripwireSweep_MissingExecutionRowIsNoOp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	processor := &recordingAlertProcessor{}
	runFinishTripwireSweep(store, processor, "no-such-execution-id")

	if len(processor.events) != 0 {
		t.Errorf("expected zero events for a missing execution row, got %d", len(processor.events))
	}
}

// TestExecutionLifecycle_Persist_RunsFinishTripwireSweep is the integration
// point between Persist (the universal terminal write) and the TASK-441 L5
// sweep: a terminal Persist call must run the sweep and relay its
// attempt/success/failure signals through the lifecycle's configured
// alertProcessor.
func TestExecutionLifecycle_Persist_RunsFinishTripwireSweep(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-9201", ProjectPath: ""}
	lifecycle := NewExecutionLifecycle(store)
	processor := &recordingAlertProcessor{}
	lifecycle.SetAlertProcessor(processor)

	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if _, err := lifecycle.Finish(execID, &ExecutionResult{TaskID: task.ID, Success: true}, nil, 0); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	if got := processor.countByType(AlertEventTypeDeadManAttempt); got != len(FinishTripwireTrackerNames) {
		t.Errorf("expected Persist (via Finish) to run the sweep and emit %d attempt events, got %d", len(FinishTripwireTrackerNames), got)
	}
}

// TestExecutionLifecycle_Transition_DoesNotRunFinishTripwireSweep verifies
// the non-terminal path (queued -> running) never triggers the sweep —
// Transition never calls Persist, so this is really a regression guard
// against a future refactor accidentally routing Transition through Persist.
func TestExecutionLifecycle_Transition_DoesNotRunFinishTripwireSweep(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-9202", ProjectPath: ""}
	lifecycle := NewExecutionLifecycle(store)
	processor := &recordingAlertProcessor{}
	lifecycle.SetAlertProcessor(processor)

	execID, err := lifecycle.Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if err := lifecycle.Transition(execID, ExecStatusRunning); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(processor.events) != 0 {
		t.Errorf("expected Transition (non-terminal) to never trigger the finish-tripwire sweep, got %d events", len(processor.events))
	}
}

// TestExecutionLifecycle_Persist_SweepPanicNeverPropagates verifies a panic
// inside the sweep (simulated via a store whose GetExecution succeeds but
// whose ListExecutionEvents panics) is recovered and never surfaces through
// Persist's return value — "log-and-alert only, never block".
//
// This exercises the recover() path directly rather than trying to force a
// real panic through the production check functions (which don't have one
// today) — the guarantee under test is runFinishTripwireSweep's own defer,
// not any particular check's implementation.
func TestRunFinishTripwireSweep_PanicIsRecovered(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-panic-guard", TaskID: "GH-9203", Status: "completed"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	// panicProcessor panics on the very first ProcessEvent call (the first
	// check's attempt emission), verifying the sweep's own recover() catches
	// a panic raised from deep inside its call graph, not just from the
	// checks directly.
	panicProcessor := panicOnFirstCallProcessor{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped runFinishTripwireSweep: %v", r)
		}
	}()
	runFinishTripwireSweep(store, panicProcessor, exec.ID)
}

type panicOnFirstCallProcessor struct{}

func (panicOnFirstCallProcessor) ProcessEvent(AlertEvent) {
	panic("simulated processor panic")
}
