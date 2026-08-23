package executor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5133 (Defect 2): before wakeHeldWorkers existed, ProjectWorker.processQueue
// only ever ran again in response to a NEW dispatch (QueueTask/ensureWorker's
// own Signal call) — a base-presence-held queue head that never received
// another fresh dispatch could sit forever: its hold_count never advanced,
// the escalation/park path (BasePresenceHoldMaxCycles -> pilot-needs-human +
// ExecStatusSkipped) was unreachable, and every genuinely innocent task
// queued behind it starved right along with it. This is the exact 3-hour
// incident wedge: two held docs issues in front of a clean third one, no
// further dispatches for hours.
//
// This test drives a REAL *Dispatcher (not a bare ProjectWorker driven by
// manual processQueue() calls, as the pre-existing GH-5052 suite does) so
// the periodic runStaleRecoveryLoop -> wakeHeldWorkers path introduced by
// this fix is what actually re-evaluates the held head — the test only
// dispatches once, at Start (queue adoption), then goes quiet, exactly
// mirroring the incident's "no new dispatches" condition.
func TestDispatcher_HeldHeadInQuietQueue_EscalatesAndQueueAdvancesViaPeriodicWake(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// escalateBasePresenceHold shells out with cmd.Dir = task.ProjectPath, and
	// the second (unheld) task actually executes and needs a real git repo
	// checked out on its branch to create commits/PRs against.
	const secondBranch = "pilot/GH-9402"
	projectPath := setupPRGuardRepo(t, secondBranch, false)

	held := &memory.Execution{
		ID:           "exec-gh5133-held",
		TaskID:       "GH-9401",
		ProjectPath:  projectPath,
		Status:       "queued",
		TaskBranch:   "pilot/GH-9401",
		TaskCreatePR: true,
		// A real (non-glob) path reference, so ExtractReferencedPaths (fixed by
		// Defect 1 to exclude globs) still yields a non-empty path and the
		// dispatcher's `len(refs) > 0 || len(paths) > 0` gate is entered,
		// letting the stubbed checkBasePresence below actually run. This test
		// pins the QUEUE mechanics of a genuine hold (Defect 2); the glob
		// exclusion itself is pinned separately by TestExtractReferencedPaths.
		TaskDescription: "See `internal/executor/definitely_missing_file.go` for the affected files.",
	}
	if err := store.SaveExecution(held); err != nil {
		t.Fatalf("SaveExecution(held): %v", err)
	}
	// A second, genuinely clean task queued behind the held one for the SAME
	// project — must remain starved until the head is parked, then execute.
	second := &memory.Execution{
		ID:              "exec-gh5133-second",
		TaskID:          "GH-9402",
		ProjectPath:     projectPath,
		Status:          "queued",
		TaskBranch:      secondBranch,
		TaskCreatePR:    true,
		TaskDescription: "No dependency markers here.",
	}
	if err := store.SaveExecution(second); err != nil {
		t.Fatalf("SaveExecution(second): %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	origMergedPR := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origMergedPR })

	// The stubbed probe forces the hold deterministically (rather than relying
	// on a real GitHub lookup) — this test pins the QUEUE mechanics (Defect 2),
	// not the extractor itself (Defect 1, covered by TestExtractReferencedPaths).
	// Force a hold for the first task only, as if a genuinely-missing path
	// were cited.
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, task *Task, _ string, _ []int, _ []string) (BasePresenceHold, error) {
		if task.ID != "GH-9401" {
			t.Fatalf("checkBasePresence unexpectedly called for task %q", task.ID)
		}
		return BasePresenceHold{Held: true, Reason: `referenced path "internal/executor/definitely_missing_file.go" not found on default branch`}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should only run for the second task"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	config := &DispatcherConfig{
		// Long enough that neither stale-recovery pass ever reaps these rows
		// by age, short enough the test doesn't wait long in wall-clock time.
		StaleRunningThreshold:     time.Hour,
		StaleQueuedThreshold:      time.Hour,
		StaleRecoveryInterval:     20 * time.Millisecond,
		BasePresenceHoldMaxCycles: 3,
	}
	dispatcher := NewDispatcher(store, runner, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start's own queue adoption is the ONLY fresh dispatch this test ever
	// produces — after this, nothing calls QueueTask/ensureWorker/Signal
	// again. Every further re-evaluation of the held head must come from
	// the periodic wakeHeldWorkers path alone.
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("dispatcher.Start: %v", err)
	}
	defer dispatcher.Stop()

	deadline := time.Now().Add(5 * time.Second)
	var heldExec *memory.Execution
	for time.Now().Before(deadline) {
		var err error
		heldExec, err = store.GetExecution(held.ID)
		if err != nil {
			t.Fatalf("GetExecution(held): %v", err)
		}
		if heldExec.Status == string(ExecStatusSkipped) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if heldExec.Status != string(ExecStatusSkipped) {
		t.Fatalf("expected the held task to escalate and park as %q via periodic wakes alone, got %q", ExecStatusSkipped, heldExec.Status)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log: %v", err)
	}
	log := string(data)
	if got := strings.Count(log, "issue edit 9401"); got != 1 {
		t.Errorf("expected exactly one `gh issue edit 9401` escalation call, got %d (log: %q)", got, log)
	}
	if !strings.Contains(log, "--add-label pilot-needs-human") {
		t.Errorf("expected the escalation call to add the pilot-needs-human label, got log: %q", log)
	}

	// The second, innocent task must have been unblocked and executed —
	// this is the "starved tasks behind it then execute" half of the
	// acceptance criterion. Poll backend.execCount (not the execution's
	// status) since Dispatcher.Start runs the worker in its own goroutine:
	// the row flips out of "queued" the instant it's picked up, well before
	// the async backend invocation (branch checkout, commit, etc.) actually
	// completes — waiting on status alone raced the still-in-flight
	// execution against this test's deadline/cancel, canceling it mid-run.
	deadline = time.Now().Add(5 * time.Second)
	var count int
	var gotProjectPath string
	for time.Now().Before(deadline) {
		backend.mu.Lock()
		count = backend.execCount
		gotProjectPath = backend.gotProjectPath
		backend.mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count == 0 {
		t.Error("expected the queue head to advance to the second task and execute it once the first was parked")
	}
	if gotProjectPath != projectPath {
		t.Errorf("expected the second task's execution to use project path %q, got %q", projectPath, gotProjectPath)
	}

	secondExec, err := store.GetExecution(second.ID)
	if err != nil {
		t.Fatalf("GetExecution(second): %v", err)
	}
	if secondExec.Status == "queued" {
		t.Errorf("expected the second task to have been picked up off the queue once the head was parked, got status %q", secondExec.Status)
	}
}
