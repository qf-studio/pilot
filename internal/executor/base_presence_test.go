package executor

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5045 regression suite for the claim-path base-presence guard
// (base_presence.go, wired into dispatcher.go's processQueue between the
// GH-4656 issue-state revalidation and the GH-4141 merged-PR preflight).
//
// checkBasePresence is stubbed directly, mirroring stubFetchIssueState
// (gh4656_issue_state_test.go) — it's the single swappable var both the
// production call site and these tests share, so no real git remote or
// GitHub API call is required to drive the decision logic end-to-end
// through ProjectWorker.processQueue.

// stubCheckBasePresence overrides the package-level checkBasePresence var
// for the duration of the test and restores the original on cleanup.
func stubCheckBasePresence(t *testing.T, fn func(ctx context.Context, runner *Runner, task *Task, projectPath string, refs []int, paths []string) (BasePresenceHold, error)) {
	t.Helper()
	orig := checkBasePresence
	checkBasePresence = fn
	t.Cleanup(func() { checkBasePresence = orig })
}

// (a) unmet prerequisite -> task held, zero backend invocations, row stays
// queued with a StageBasePresenceHeld event; prerequisite lands -> next tick
// proceeds and the backend runs exactly once.
func TestProcessQueue_BasePresence_HeldThenReleased(t *testing.T) {
	const branch = "pilot/GH-9201"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5045-held-then-released",
		TaskID:          "GH-9201",
		ProjectPath:     dir,
		Status:          "queued",
		TaskBranch:      branch,
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #99",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	origMergedPR := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origMergedPR })

	var mu sync.Mutex
	held := true
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, task *Task, _ string, refs []int, _ []string) (BasePresenceHold, error) {
		if len(refs) != 1 || refs[0] != 99 {
			t.Fatalf("checkBasePresence called with unexpected refs %v (task %q)", refs, task.ID)
		}
		mu.Lock()
		defer mu.Unlock()
		if held {
			return BasePresenceHold{Held: true, Reason: "referenced PR #99 is still open (not merged)"}, nil
		}
		return BasePresenceHold{}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	// Tick 1: prerequisite still open -> held, no execution.
	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected zero backend invocations while held, got %d", count)
	}

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "queued" {
		t.Errorf("expected status to remain %q while held, got %q", "queued", got.Status)
	}

	events, err := store.ListExecutionEvents(exec.ID)
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one execution event after being held")
	}
	last := events[len(events)-1]
	if last.Stage != memory.StageBasePresenceHeld {
		t.Errorf("expected last event stage %q, got %q", memory.StageBasePresenceHeld, last.Stage)
	}
	if !strings.Contains(last.Detail, "referenced PR #99 is still open") {
		t.Errorf("expected event detail to name the unmet prerequisite, got %q", last.Detail)
	}

	// Prerequisite lands -> tick 2 proceeds to execution.
	mu.Lock()
	held = false
	mu.Unlock()

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count = backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Error("expected the backend to be invoked once the prerequisite landed — hold guard must not have short-circuited")
	}
}

// (b) a task held past BasePresenceHoldMaxCycles gets the pilot-needs-human
// label applied exactly once, and the hold counter resets afterward so a
// further held cycle doesn't re-escalate immediately.
func TestProcessQueue_BasePresence_EscalatesAfterMaxCycles(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// escalateBasePresenceHold shells out with cmd.Dir = task.ProjectPath, so
	// this must be a real directory (unlike the fake project-path strings
	// used elsewhere in this file, where the backend never runs and no
	// subprocess needs a working directory).
	projectPath := t.TempDir()
	exec := &memory.Execution{
		ID:              "exec-gh5045-escalate",
		TaskID:          "GH-9301",
		ProjectPath:     projectPath,
		Status:          "queued",
		TaskBranch:      "pilot/GH-9301",
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #1",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, _ *Task, _ string, _ []int, _ []string) (BasePresenceHold, error) {
		return BasePresenceHold{Held: true, Reason: "referenced PR #1 is still open (not merged)"}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner := NewRunnerWithBackend(backend)
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())
	worker.setBasePresenceHoldMaxCycles(2)

	for i := 0; i < 3; i++ {
		worker.processQueue(context.Background())
	}

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Errorf("expected zero backend invocations across all held cycles, got %d", count)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log: %v", err)
	}
	log := string(data)
	if got := strings.Count(log, "issue edit 9301"); got != 1 {
		t.Errorf("expected exactly one `gh issue edit 9301` call after 3 held cycles with max-cycles=2, got %d (log: %q)", got, log)
	}
	if !strings.Contains(log, "--add-label pilot-needs-human") {
		t.Errorf("expected the escalation call to add the pilot-needs-human label, got log: %q", log)
	}
}

// (c) a task description with no "Depends on"/"Blocked by" ref and no
// backtick-quoted path never calls checkBasePresence at all — zero probe
// calls, byte-identical to the pre-GH-5045 pickup path — and the backend
// still executes normally.
func TestProcessQueue_BasePresence_FastPathSkipsCheckWhenNothingExtracted(t *testing.T) {
	const branch = "pilot/GH-9401"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5045-fast-path",
		TaskID:          "GH-9401",
		ProjectPath:     dir,
		Status:          "queued",
		TaskBranch:      branch,
		TaskCreatePR:    true,
		TaskDescription: "Plain-prose task description with no dependency markers or file citations.",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	origMergedPR := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origMergedPR })

	var mu sync.Mutex
	calls := 0
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, _ *Task, _ string, _ []int, _ []string) (BasePresenceHold, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return BasePresenceHold{}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	worker.processQueue(context.Background())

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 0 {
		t.Errorf("expected checkBasePresence to never be called when no refs/paths are extracted, got %d calls", gotCalls)
	}

	backend.mu.Lock()
	execCount := backend.execCount
	backend.mu.Unlock()
	if execCount == 0 {
		t.Error("expected the backend to run normally when nothing is extracted")
	}
}
