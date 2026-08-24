package executor

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// GH-5045/GH-5052 regression suite for the claim-path base-presence guard
// (base_presence.go, wired into dispatcher.go's processQueue between the
// GH-4656 issue-state revalidation and the running-status transition).
//
// Two layers of coverage:
//
//   - basePresenceChecker.Check is driven directly through a fake
//     BasePresenceProbe (no dispatcher, no store, no gh CLI) — the fast,
//     precise layer that pins down the decision logic for both dependency-ref
//     shapes (a directly-referenced open PR, and an issue whose attached PR
//     is open-unmerged) plus the path-existence check and fail-open-on-error
//     behavior.
//   - dispatcher-level tests stub checkBasePresence wholesale (mirroring
//     stubFetchIssueState, gh4656_issue_state_test.go) and drive
//     ProjectWorker.processQueue across multiple ticks to pin down the
//     hold/escalate/park/release state machine.

// ---------------------------------------------------------------------
// Layer 1: basePresenceChecker.Check driven directly via a fake probe.
// ---------------------------------------------------------------------

// fakeBasePresenceProbe is a scripted BasePresenceProbe: each method looks
// up its answer (or forced error) from a map keyed by the number/path
// queried, and records every call it received so tests can assert on call
// counts (e.g. "probing stops at the first held ref").
type fakeBasePresenceProbe struct {
	mu sync.Mutex

	// issueOrPR maps a number to (kind, state). A number absent from this
	// map but present in issueOrPRErr returns that error instead.
	issueOrPR    map[int][2]string
	issueOrPRErr map[int]error

	// linkedPRs maps an issue number to the PR numbers attached to it. A
	// number absent from this map but present in linkedPRsErr returns that
	// error instead; otherwise it returns (nil, nil) — "no attached PR".
	linkedPRs    map[int][]int
	linkedPRsErr map[int]error

	// fileExists maps a path to its existence. A path absent from this map
	// but present in fileExistsErr returns that error instead.
	fileExists    map[string]bool
	fileExistsErr map[string]error

	issueOrPRCalls  []int
	linkedPRCalls   []int
	fileExistsCalls []string
}

func (f *fakeBasePresenceProbe) IssueOrPRState(_ context.Context, _, _ string, number int) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issueOrPRCalls = append(f.issueOrPRCalls, number)
	if err, ok := f.issueOrPRErr[number]; ok {
		return "", "", err
	}
	if kv, ok := f.issueOrPR[number]; ok {
		return kv[0], kv[1], nil
	}
	return "", "", errors.New("fakeBasePresenceProbe: no IssueOrPRState scripted for number")
}

func (f *fakeBasePresenceProbe) LinkedPRNumbers(_ context.Context, _, _ string, issueNumber int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkedPRCalls = append(f.linkedPRCalls, issueNumber)
	if err, ok := f.linkedPRsErr[issueNumber]; ok {
		return nil, err
	}
	return f.linkedPRs[issueNumber], nil
}

func (f *fakeBasePresenceProbe) FileExistsOnDefaultBranch(_ context.Context, _, _, path string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fileExistsCalls = append(f.fileExistsCalls, path)
	if err, ok := f.fileExistsErr[path]; ok {
		return false, err
	}
	return f.fileExists[path], nil
}

// TestBasePresenceChecker_Check_OpenPRRef_Holds covers ref shape (a): the
// referenced number resolves directly to an open PR.
func TestBasePresenceChecker_Check_OpenPRRef_Holds(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		issueOrPR: map[int][2]string{99: {"pr", "open"}},
	}
	checker := basePresenceChecker{probe: probe}

	got := checker.Check(context.Background(), "acme", "widgets", []int{99}, nil)
	if !got.Held {
		t.Fatalf("expected hold for an open-PR ref, got %+v", got)
	}
	if !strings.Contains(got.Reason, "#99") || !strings.Contains(got.Reason, "open") {
		t.Errorf("expected reason to name the open PR, got %q", got.Reason)
	}
}

// TestBasePresenceChecker_Check_IssueWithOpenPR_Holds covers ref shape (b) —
// the canonical incident form (GH-5021, ui#120/#124/#139 lineage): the
// referenced number is an issue, and its attached PR (found via
// LinkedPRNumbers) is open-unmerged.
func TestBasePresenceChecker_Check_IssueWithOpenPR_Holds(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		issueOrPR: map[int][2]string{
			42:  {"issue", "open"},
			101: {"pr", "open"},
		},
		linkedPRs: map[int][]int{42: {101}},
	}
	checker := basePresenceChecker{probe: probe}

	got := checker.Check(context.Background(), "acme", "widgets", []int{42}, nil)
	if !got.Held {
		t.Fatalf("expected hold for an issue whose attached PR is open, got %+v", got)
	}
	if !strings.Contains(got.Reason, "#42") || !strings.Contains(got.Reason, "#101") {
		t.Errorf("expected reason to name both the issue and its attached PR, got %q", got.Reason)
	}
}

// TestBasePresenceChecker_Check_IssueWithMergedPR_NoHold: same shape as
// above, but the attached PR already merged — nothing left to wait on.
func TestBasePresenceChecker_Check_IssueWithMergedPR_NoHold(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		issueOrPR: map[int][2]string{
			42:  {"issue", "closed"},
			101: {"pr", "merged"},
		},
		linkedPRs: map[int][]int{42: {101}},
	}
	checker := basePresenceChecker{probe: probe}

	got := checker.Check(context.Background(), "acme", "widgets", []int{42}, nil)
	if got.Held {
		t.Fatalf("expected no hold once the issue's attached PR is merged, got %+v", got)
	}
}

// TestBasePresenceChecker_Check_ClosedUnmergedPRRef_NoHold: ref shape (a),
// but the referenced PR is closed without merging — abandoned work is not a
// prerequisite to wait on.
func TestBasePresenceChecker_Check_ClosedUnmergedPRRef_NoHold(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		issueOrPR: map[int][2]string{99: {"pr", "closed"}},
	}
	checker := basePresenceChecker{probe: probe}

	got := checker.Check(context.Background(), "acme", "widgets", []int{99}, nil)
	if got.Held {
		t.Fatalf("expected no hold for a closed-unmerged PR ref, got %+v", got)
	}
}

// TestBasePresenceChecker_Check_MissingPath_Holds covers the backtick-path
// hold: FileExistsOnDefaultBranch reports absent.
func TestBasePresenceChecker_Check_MissingPath_Holds(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		fileExists: map[string]bool{"internal/foo.go": false},
	}
	checker := basePresenceChecker{probe: probe}

	got := checker.Check(context.Background(), "acme", "widgets", nil, []string{"internal/foo.go"})
	if !got.Held {
		t.Fatalf("expected hold for a missing path, got %+v", got)
	}
	if !strings.Contains(got.Reason, "internal/foo.go") {
		t.Errorf("expected reason to name the missing path, got %q", got.Reason)
	}
}

// TestBasePresenceChecker_Check_ProbeError_FailsOpenAndLogs asserts the
// fail-open contract at the Check layer itself (not just at the dispatcher
// call site): a probe error for one ref never holds the task, and is logged
// so an operator can notice a probe malfunction rather than it silently
// masquerading as "nothing blocks."
func TestBasePresenceChecker_Check_ProbeError_FailsOpenAndLogs(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		issueOrPRErr: map[int]error{99: errors.New("simulated transport failure")},
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	checker := basePresenceChecker{probe: probe, log: logger}

	got := checker.Check(context.Background(), "acme", "widgets", []int{99}, nil)
	if got.Held {
		t.Fatalf("expected fail-open (not held) on probe error, got %+v", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "simulated transport failure") {
		t.Errorf("expected the probe error to be logged, got log output: %q", logged)
	}
	if !strings.Contains(logged, "99") {
		t.Errorf("expected the log to name the ref that errored, got: %q", logged)
	}
}

// TestBasePresenceChecker_Check_NilProbe_NoHold covers the "no adapter
// wired for this repo" case: checkBasePresence's caller can construct a
// basePresenceChecker with a nil probe (defensive default) and it must
// behave as "nothing blocks", not panic.
func TestBasePresenceChecker_Check_NilProbe_NoHold(t *testing.T) {
	checker := basePresenceChecker{}
	got := checker.Check(context.Background(), "acme", "widgets", []int{99}, []string{"internal/foo.go"})
	if got.Held {
		t.Fatalf("expected no hold with a nil probe, got %+v", got)
	}
}

// TestBasePresenceChecker_Check_RefsCheckedBeforePaths verifies that a held
// ref short-circuits before any path lookup runs.
func TestBasePresenceChecker_Check_RefsCheckedBeforePaths(t *testing.T) {
	probe := &fakeBasePresenceProbe{
		issueOrPR:  map[int][2]string{99: {"pr", "open"}},
		fileExists: map[string]bool{"internal/foo.go": false},
	}
	checker := basePresenceChecker{probe: probe}

	got := checker.Check(context.Background(), "acme", "widgets", []int{99}, []string{"internal/foo.go"})
	if !got.Held {
		t.Fatalf("expected hold from the ref check, got %+v", got)
	}
	probe.mu.Lock()
	fileCalls := len(probe.fileExistsCalls)
	probe.mu.Unlock()
	if fileCalls != 0 {
		t.Errorf("expected zero path lookups once a ref already holds, got %d", fileCalls)
	}
}

// ---------------------------------------------------------------------
// Layer 2: dispatcher-level fake-tracker multi-tick tests. checkBasePresence
// is stubbed directly, mirroring stubFetchIssueState
// (gh4656_issue_state_test.go) — it's the single swappable var both the
// production call site and these tests share, so no real git remote or
// GitHub API call is required to drive the decision logic end-to-end
// through ProjectWorker.processQueue.
// ---------------------------------------------------------------------

// stubCheckBasePresence overrides the package-level checkBasePresence var
// for the duration of the test and restores the original on cleanup.
func stubCheckBasePresence(t *testing.T, fn func(ctx context.Context, runner *Runner, task *Task, projectPath string, refs []int, paths []string) (BasePresenceHold, error)) {
	t.Helper()
	orig := checkBasePresence
	checkBasePresence = fn
	t.Cleanup(func() { checkBasePresence = orig })
}

// (1) dual-shape hold-then-release: a task referencing an issue whose
// attached PR is open stays held; once the underlying probe reports the PR
// merged (simulating the PR merging), the very next tick releases the hold
// and the backend runs exactly once.
func TestProcessQueue_BasePresence_DualShapeHeldThenReleasedOnMerge(t *testing.T) {
	const branch = "pilot/GH-9201"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5052-dual-shape",
		TaskID:          "GH-9201",
		ProjectPath:     dir,
		Status:          "queued",
		TaskBranch:      branch,
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #42",
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
	prMerged := false
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, task *Task, _ string, refs []int, _ []string) (BasePresenceHold, error) {
		if len(refs) != 1 || refs[0] != 42 {
			t.Fatalf("checkBasePresence called with unexpected refs %v (task %q)", refs, task.ID)
		}
		mu.Lock()
		defer mu.Unlock()
		if prMerged {
			return BasePresenceHold{}, nil
		}
		return BasePresenceHold{Held: true, Reason: "referenced issue #42's attached PR #101 is still open (not merged)"}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	// Tick 1: attached PR still open -> held, no execution.
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

	// PR merges -> tick 2 releases the hold and executes.
	mu.Lock()
	prMerged = true
	mu.Unlock()

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count = backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Error("expected the backend to be invoked once the attached PR merged — hold guard must not have short-circuited")
	}
}

// (2) persistent-hold escalation fires exactly once (not every N cycles)
// and the queue head advances to a second queued task for the same project
// once the first is parked.
func TestProcessQueue_BasePresence_EscalatesOnceThenQueueHeadAdvances(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	// escalateBasePresenceHold shells out with cmd.Dir = task.ProjectPath, and
	// the second (unheld) task actually executes and needs a real git repo
	// checked out on its branch to create commits/PRs against.
	projectPath := setupPRGuardRepo(t, "pilot/GH-9302", false)

	held := &memory.Execution{
		ID:              "exec-gh5052-escalate-held",
		TaskID:          "GH-9301",
		ProjectPath:     projectPath,
		Status:          "queued",
		TaskBranch:      "pilot/GH-9301",
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #1",
	}
	if err := store.SaveExecution(held); err != nil {
		t.Fatalf("SaveExecution(held): %v", err)
	}
	// A second task queued after the held one for the SAME project — it
	// must become reachable once the first is parked.
	second := &memory.Execution{
		ID:              "exec-gh5052-escalate-second",
		TaskID:          "GH-9302",
		ProjectPath:     projectPath,
		Status:          "queued",
		TaskBranch:      "pilot/GH-9302",
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

	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, task *Task, _ string, refs []int, _ []string) (BasePresenceHold, error) {
		if task.ID != "GH-9301" {
			// The second task's description has no refs/paths, so this
			// stub should never be consulted for it (the fast path skips
			// the call entirely) — fail loudly if that invariant breaks.
			t.Fatalf("checkBasePresence unexpectedly called for task %q", task.ID)
		}
		return BasePresenceHold{Held: true, Reason: "referenced PR #1 is still open (not merged)"}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should only run for the second task"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())
	worker.setBasePresenceHoldMaxCycles(2)

	// Tick 1: held, not yet escalated (count reaches 1 < max=2). processQueue
	// returns after a not-yet-escalated hold to avoid busy-looping.
	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected zero backend invocations before escalation, got %d", count)
	}
	if data, err := os.ReadFile(logFile); err == nil && strings.Contains(string(data), "issue edit") {
		t.Fatalf("expected no escalation before the max-cycles threshold, got gh CLI log: %q", string(data))
	}

	// Tick 2: count reaches 2 == max, escalates, parks the row, then the SAME
	// tick (same processQueue call, via `continue` not `return`) advances to
	// the second queued task and executes it.
	worker.processQueue(context.Background())

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log: %v", err)
	}
	log := string(data)
	if got := strings.Count(log, "issue edit 9301"); got != 1 {
		t.Errorf("expected exactly one `gh issue edit 9301` escalation call, got %d (log: %q)", got, log)
	}
	if !strings.Contains(log, "--add-label pilot-needs-human") {
		t.Errorf("expected the escalation call to add the pilot-needs-human label, got log: %q", log)
	}

	heldExec, err := store.GetExecution(held.ID)
	if err != nil {
		t.Fatalf("GetExecution(held): %v", err)
	}
	if heldExec.Status != string(ExecStatusSkipped) {
		t.Errorf("expected the escalated row to be parked as %q, got %q", ExecStatusSkipped, heldExec.Status)
	}

	backend.mu.Lock()
	count = backend.execCount
	gotProjectPath := backend.gotProjectPath
	backend.mu.Unlock()
	if count == 0 {
		t.Errorf("expected the queue head to advance to the second task within the same tick and execute it, got %d backend invocations", count)
	}
	if gotProjectPath != projectPath {
		t.Errorf("expected the second task's execution to use project path %q, got %q", projectPath, gotProjectPath)
	}

	secondExec, err := store.GetExecution(second.ID)
	if err != nil {
		t.Fatalf("GetExecution(second): %v", err)
	}
	if secondExec.Status == "queued" {
		t.Errorf("expected the second task to have been picked up off the queue, got status %q", secondExec.Status)
	}

	// A further tick (nothing left queued) must not re-fire the escalation.
	worker.processQueue(context.Background())
	data, err = os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log (final): %v", err)
	}
	if got := strings.Count(string(data), "issue edit 9301"); got != 1 {
		t.Errorf("expected escalation to fire exactly once total (not re-fire), got %d calls: %q", got, string(data))
	}
}

// (2b) GH-5193 acceptance: a task whose body cites a path that can never
// land on this repo's default branch (the GH-5189/GH-5145 incident shape —
// a cross-repo path, e.g. a studio-sdk file mentioned as context, not an
// actual this-repo prerequisite) escalates within a bounded number of
// holds instead of holding forever. Mirrors
// TestProcessQueue_BasePresence_EscalatesOnceThenQueueHeadAdvances exactly,
// except the extracted prerequisite is a path (never satisfied) rather
// than a ref — pinning that the bounded-escalation cap applies uniformly
// to both dependency-ref shapes and the path shape the two live incidents
// actually hit.
func TestProcessQueue_BasePresence_NeverSatisfiablePathEscalatesWithinBoundedHolds(t *testing.T) {
	logFile := setupFakeGhCLI(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	projectPath := setupPRGuardRepo(t, "pilot/GH-9304", false)

	const phantomPath = "studio-sdk/internal/example.go"
	held := &memory.Execution{
		ID:              "exec-gh5193-path-escalate-held",
		TaskID:          "GH-9303",
		ProjectPath:     projectPath,
		Status:          "queued",
		TaskBranch:      "pilot/GH-9303",
		TaskCreatePR:    true,
		TaskDescription: "See `" + phantomPath + "` for context.",
	}
	if err := store.SaveExecution(held); err != nil {
		t.Fatalf("SaveExecution(held): %v", err)
	}
	// A second task queued after the held one for the SAME project — it
	// must become reachable once the first is parked.
	second := &memory.Execution{
		ID:              "exec-gh5193-path-escalate-second",
		TaskID:          "GH-9304",
		ProjectPath:     projectPath,
		Status:          "queued",
		TaskBranch:      "pilot/GH-9304",
		TaskCreatePR:    true,
		TaskDescription: "No dependency markers here.",
	}
	if err := store.SaveExecution(second); err != nil {
		t.Fatalf("SaveExecution(second): %v", err)
	}

	// The live body always still cites the phantom path — an operator who
	// never edits the issue (unlike the self-heal scenario covered by
	// TestProcessQueue_BasePresence_BodyEditRemovingPathClearsHoldAcrossTicks)
	// must still see the hold bounded rather than wedge forever. Scoped to
	// the held task's ID: returning held's body unconditionally for every
	// task (including the second, unrelated task) would leak the phantom
	// path into the second task's presence check too, since dispatcher.go
	// prefers a non-empty live Body over task.Description for ANY task this
	// stub is consulted for.
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, task *Task, _ string) (IssueState, error) {
		if task.ID == "GH-9303" {
			return IssueState{Closed: false, Body: held.TaskDescription}, nil
		}
		return IssueState{Closed: false}, nil
	})
	origMergedPR := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origMergedPR })

	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, task *Task, _ string, _ []int, paths []string) (BasePresenceHold, error) {
		if task.ID != "GH-9303" {
			t.Fatalf("checkBasePresence unexpectedly called for task %q", task.ID)
		}
		for _, p := range paths {
			if p == phantomPath {
				return BasePresenceHold{Held: true, Reason: "referenced path \"" + p + "\" not found on default branch"}, nil
			}
		}
		t.Fatalf("checkBasePresence called without the phantom path in paths %v", paths)
		return BasePresenceHold{}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should only run for the second task"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())
	worker.setBasePresenceHoldMaxCycles(2)

	// Tick 1: held, not yet escalated (count reaches 1 < max=2).
	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected zero backend invocations before escalation, got %d", count)
	}
	if data, err := os.ReadFile(logFile); err == nil && strings.Contains(string(data), "issue edit") {
		t.Fatalf("expected no escalation before the max-cycles threshold, got gh CLI log: %q", string(data))
	}

	// Tick 2: count reaches 2 == max, escalates, parks the row, then the SAME
	// tick advances to the second queued task and executes it — the queue
	// head must not wedge on a never-satisfiable path forever.
	worker.processQueue(context.Background())

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read gh CLI log: %v", err)
	}
	log := string(data)
	if got := strings.Count(log, "issue edit 9303"); got != 1 {
		t.Errorf("expected exactly one `gh issue edit 9303` escalation call, got %d (log: %q)", got, log)
	}
	if !strings.Contains(log, "--add-label pilot-needs-human") {
		t.Errorf("expected the escalation call to add the pilot-needs-human label, got log: %q", log)
	}

	heldExec, err := store.GetExecution(held.ID)
	if err != nil {
		t.Fatalf("GetExecution(held): %v", err)
	}
	if heldExec.Status != string(ExecStatusSkipped) {
		t.Errorf("expected the escalated row to be parked as %q, got %q", ExecStatusSkipped, heldExec.Status)
	}

	backend.mu.Lock()
	count = backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Errorf("expected the queue head to advance to the second task within the same tick and execute it, got %d backend invocations", count)
	}

	secondExec, err := store.GetExecution(second.ID)
	if err != nil {
		t.Fatalf("GetExecution(second): %v", err)
	}
	if secondExec.Status == "queued" {
		t.Errorf("expected the second task to have been picked up off the queue, got status %q", secondExec.Status)
	}
}

// (3) closing the held issue releases the hold across ticks — the GH-4656
// revalidation must run even while a row is held, not be short-circuited by
// the hold path's early return.
func TestProcessQueue_BasePresence_ClosingHeldIssueReleasesAcrossTicks(t *testing.T) {
	const branch = "pilot/GH-9203"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5052-close-releases",
		TaskID:          "GH-9203",
		ProjectPath:     dir,
		Status:          "queued",
		TaskBranch:      branch,
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #55",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	var mu sync.Mutex
	issueClosed := false
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		mu.Lock()
		defer mu.Unlock()
		return IssueState{Closed: issueClosed}, nil
	})

	checkCalls := 0
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, _ *Task, _ string, _ []int, _ []string) (BasePresenceHold, error) {
		mu.Lock()
		checkCalls++
		mu.Unlock()
		return BasePresenceHold{Held: true, Reason: "referenced PR #55 is still open (not merged)"}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner := NewRunnerWithBackend(backend)
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	// Tick 1: held (issue still open).
	worker.processQueue(context.Background())

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "queued" {
		t.Errorf("expected status to remain queued while held, got %q", got.Status)
	}

	// The issue closes out from under the hold.
	mu.Lock()
	issueClosed = true
	mu.Unlock()

	// Tick 2: the GH-4656 revalidation must fire BEFORE the hold check and
	// supersede the row — checkBasePresence must never even be consulted.
	worker.processQueue(context.Background())

	mu.Lock()
	gotCalls := checkCalls
	mu.Unlock()
	if gotCalls != 1 {
		t.Errorf("expected checkBasePresence to be called exactly once (tick 1 only) — closing the issue must short-circuit before the hold check on tick 2, got %d calls", gotCalls)
	}

	got, err = store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != string(ExecStatusSuperseded) {
		t.Errorf("expected closing the held issue to supersede the row, got status %q", got.Status)
	}

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Errorf("expected zero backend invocations — the row was held then superseded, never executed, got %d", count)
	}
}

// (4) natural-release counter reset: a task held for fewer than
// BasePresenceHoldMaxCycles cycles, then naturally released and claimed,
// resets its hold counter to zero rather than carrying it over.
func TestProcessQueue_BasePresence_NaturalReleaseResetsCounter(t *testing.T) {
	const branch = "pilot/GH-9204"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5052-natural-release",
		TaskID:          "GH-9204",
		ProjectPath:     dir,
		Status:          "queued",
		TaskBranch:      branch,
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #77",
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
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, _ *Task, _ string, _ []int, _ []string) (BasePresenceHold, error) {
		mu.Lock()
		defer mu.Unlock()
		if held {
			return BasePresenceHold{Held: true, Reason: "referenced PR #77 is still open (not merged)"}, nil
		}
		return BasePresenceHold{}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "done"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())
	worker.setBasePresenceHoldMaxCycles(20)

	// Two held ticks (well under the max-cycles bound).
	worker.processQueue(context.Background())
	worker.processQueue(context.Background())

	key := repickBackoffKey(dir, "GH-9204")
	countBeforeRelease, found, err := store.GetBasePresenceHoldCount(key)
	if err != nil {
		t.Fatalf("GetBasePresenceHoldCount: %v", err)
	}
	if !found || countBeforeRelease != 2 {
		t.Fatalf("expected hold count 2 after two held ticks, got %d (found=%v)", countBeforeRelease, found)
	}

	// Prerequisite lands -> natural release and claim.
	mu.Lock()
	held = false
	mu.Unlock()
	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Fatal("expected the backend to run once the prerequisite landed")
	}

	countAfterRelease, found, err := store.GetBasePresenceHoldCount(key)
	if err != nil {
		t.Fatalf("GetBasePresenceHoldCount (after release): %v", err)
	}
	if !found || countAfterRelease != 0 {
		t.Errorf("expected the hold count to reset to 0 on natural release/claim, got %d (found=%v)", countAfterRelease, found)
	}
}

// (5) a task description with no "Depends on"/"Blocked by" ref and no
// backtick-quoted path never calls checkBasePresence at all — zero probe
// calls, byte-identical to the pre-GH-5045 pickup path — and the backend
// still executes normally.
func TestProcessQueue_BasePresence_FastPathSkipsCheckWhenNothingExtracted(t *testing.T) {
	const branch = "pilot/GH-9401"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5052-fast-path",
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

// (6) GH-5193: editing the live issue body to remove the offending
// cross-repo/nonexistent path reference clears an active hold on the very
// next tick — no cancel/relabel cycle required. Pins the fix for the cache
// defect verified live on GH-5189: the issue body was fixed at 14:46Z but
// the tick at 14:48Z still held on the removed path, because refs/paths
// were always re-extracted from the execution row's frozen TaskDescription
// snapshot (never updated after the row is queued) rather than the live
// issue body fetchIssueState already fetches on every tick.
func TestProcessQueue_BasePresence_BodyEditRemovingPathClearsHoldAcrossTicks(t *testing.T) {
	const branch = "pilot/GH-9206"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	const phantomPath = "studio-sdk/internal/example.go"
	const staleBody = "See `" + phantomPath + "` for context."
	const fixedBody = "No file citation here anymore."

	exec := &memory.Execution{
		ID:           "exec-gh5193-body-edit-clears-hold",
		TaskID:       "GH-9206",
		ProjectPath:  dir,
		Status:       "queued",
		TaskBranch:   branch,
		TaskCreatePR: true,
		// GH-5193: the execution row's snapshot is frozen at queue time —
		// the fix must consult the live body stubbed via fetchIssueState
		// below (once available), not this field, on every tick.
		TaskDescription: staleBody,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	var mu sync.Mutex
	liveBody := staleBody
	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		mu.Lock()
		defer mu.Unlock()
		return IssueState{Closed: false, Body: liveBody}, nil
	})
	origMergedPR := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	t.Cleanup(func() { mergedPRPreflightCheck = origMergedPR })

	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, _ *Task, _ string, _ []int, paths []string) (BasePresenceHold, error) {
		for _, p := range paths {
			if p == phantomPath {
				return BasePresenceHold{Held: true, Reason: "referenced path \"" + p + "\" not found on default branch"}, nil
			}
		}
		return BasePresenceHold{}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "done"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	// Tick 1: live body still carries the cross-repo path -> held.
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
		t.Errorf("expected status to remain queued while held, got %q", got.Status)
	}

	// Operator edits the issue body to drop the phantom cross-repo path —
	// the execution row's TaskDescription is untouched, mirroring GH-5189's
	// reality: nothing rewrites the stored snapshot on a live body edit.
	mu.Lock()
	liveBody = fixedBody
	mu.Unlock()

	// Tick 2: must clear on THIS tick, without any cancel/relabel cycle.
	worker.processQueue(context.Background())

	backend.mu.Lock()
	count = backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Error("expected the backend to run once the live body no longer references the missing path — a body edit must self-heal the hold on the next tick")
	}

	key := repickBackoffKey(dir, "GH-9206")
	holdCount, found, err := store.GetBasePresenceHoldCount(key)
	if err != nil {
		t.Fatalf("GetBasePresenceHoldCount: %v", err)
	}
	if !found || holdCount != 0 {
		t.Errorf("expected the hold count to reset to 0 once the body edit released the hold, got %d (found=%v)", holdCount, found)
	}
}

// (7) ledger event + log + progress observability: a held cycle records a
// StageBasePresenceHeld execution event carrying the unmet-prerequisite
// reason.
func TestProcessQueue_BasePresence_RecordsLedgerEventOnHold(t *testing.T) {
	const branch = "pilot/GH-9205"
	dir := setupPRGuardRepo(t, branch, false)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:              "exec-gh5052-ledger-event",
		TaskID:          "GH-9205",
		ProjectPath:     dir,
		Status:          "queued",
		TaskBranch:      branch,
		TaskCreatePR:    true,
		TaskDescription: "Depends on: #88",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	stubFetchIssueState(t, func(_ context.Context, _ *Runner, _ *Task, _ string) (IssueState, error) {
		return IssueState{Closed: false}, nil
	})
	stubCheckBasePresence(t, func(_ context.Context, _ *Runner, _ *Task, _ string, _ []int, _ []string) (BasePresenceHold, error) {
		return BasePresenceHold{Held: true, Reason: "referenced PR #88 is still open (not merged)"}, nil
	})

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner := NewRunnerWithBackend(backend)
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	worker.processQueue(context.Background())

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
	if !strings.Contains(last.Detail, "referenced PR #88 is still open") {
		t.Errorf("expected event detail to name the unmet prerequisite, got %q", last.Detail)
	}

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Errorf("expected zero backend invocations while held, got %d", count)
	}
}
