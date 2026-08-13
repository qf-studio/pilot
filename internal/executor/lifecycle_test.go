package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestExecutionLifecycle_Begin_CreatesRowAndThreadsID verifies Begin creates
// the executions row at the given initial status and stamps task.ExecutionID
// with the same ID it persisted (GH-4243).
func TestExecutionLifecycle_Begin_CreatesRowAndThreadsID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{
		ID:          "GH-1",
		Title:       "fix the thing",
		Description: "fix the thing",
		ProjectPath: "/tmp/project",
		Branch:      "pilot/GH-1",
		CreatePR:    true,
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID")
	}
	if task.ExecutionID != execID {
		t.Fatalf("expected task.ExecutionID %q, got %q", execID, task.ExecutionID)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load saved execution: %v", err)
	}
	if exec.Status != string(ExecStatusQueued) {
		t.Errorf("expected status %q, got %q", ExecStatusQueued, exec.Status)
	}
	if exec.TaskID != task.ID || exec.ProjectPath != task.ProjectPath || exec.TaskTitle != task.Title {
		t.Errorf("execution row fields don't match source task: %+v", exec)
	}
}

// TestExecutionLifecycle_Begin_PersistsIsCanary is a GH-4648 regression test
// for the intake-wire fix: a task built for a canary-configured project must
// produce an execution row with is_canary=1 through the ExecutionLifecycle.Begin
// chokepoint, and a non-canary project's task must land 0 — no false
// positives. This is the write-side half of the GH-4240 canary isolation
// mechanism; the read-side COALESCE(is_canary, 0) = 0 filters were dead code
// until every fresh-intake Task construction site actually set this field.
func TestExecutionLifecycle_Begin_PersistsIsCanary(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	canaryTask := &Task{ID: "GH-4648-canary", ProjectPath: "/canary-sandbox", IsCanary: true}
	canaryExecID, err := NewExecutionLifecycle(store).Begin(canaryTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin (canary) failed: %v", err)
	}
	canaryExec, err := store.GetExecution(canaryExecID)
	if err != nil {
		t.Fatalf("failed to load canary execution: %v", err)
	}
	if !canaryExec.IsCanary {
		t.Error("expected canary task's execution row to have IsCanary=true, got false")
	}

	regularTask := &Task{ID: "GH-4648-regular", ProjectPath: "/normal-project", IsCanary: false}
	regularExecID, err := NewExecutionLifecycle(store).Begin(regularTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin (regular) failed: %v", err)
	}
	regularExec, err := store.GetExecution(regularExecID)
	if err != nil {
		t.Fatalf("failed to load regular execution: %v", err)
	}
	if regularExec.IsCanary {
		t.Error("expected non-canary task's execution row to have IsCanary=false, got true (false positive)")
	}
}

// TestExecutionLifecycle_Begin_NilStore_StillThreadsID mirrors the epic
// sub-issue path's mem-026 tolerance: a nil store (or a failed save) must
// never stop task.ExecutionID from being stamped, so downstream event
// recording always has a stable UUID rather than falling back to the
// human-readable task ID.
func TestExecutionLifecycle_Begin_NilStore_StillThreadsID(t *testing.T) {
	task := &Task{ID: "GH-2", ProjectPath: "/tmp/project"}

	execID, err := NewExecutionLifecycle(nil).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("expected nil-store Begin to succeed, got: %v", err)
	}
	if execID == "" {
		t.Fatal("expected non-empty execution ID even with a nil store")
	}
	if task.ExecutionID != execID {
		t.Fatalf("expected task.ExecutionID %q, got %q", execID, task.ExecutionID)
	}
}

// TestExecutionLifecycle_Transition_QueuedToRunning verifies the non-terminal
// status move used by the dispatcher's pickup path.
func TestExecutionLifecycle_Transition_QueuedToRunning(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-3", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if err := lifecycle.Transition(execID, ExecStatusRunning); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusRunning) {
		t.Errorf("expected status %q, got %q", ExecStatusRunning, exec.Status)
	}
}

// TestExecutionLifecycle_Transition_RejectsWhenAlreadyTerminal is the GH-4423
// regression test for finding 2: Transition must not resurrect a row that
// already reached a terminal status back to a non-terminal one (e.g. a
// duplicate/late dispatch resuming a worker for a row Finish already closed
// out). The CAS guard in UpdateExecutionStatusIfNotTerminal rejects the
// write silently (no error) — the terminal status must survive.
func TestExecutionLifecycle_Transition_RejectsWhenAlreadyTerminal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4423-T1", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}
	if _, err := lifecycle.Finish(execID, &ExecutionResult{TaskID: task.ID, Success: true, CommitSHA: "abc123"}, nil, time.Second); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	// A late/duplicate Transition call racing in after Finish already closed
	// out the row must be rejected, not resurrect it to "running".
	if err := lifecycle.Transition(execID, ExecStatusRunning); err != nil {
		t.Fatalf("Transition on an already-terminal row should not error, got: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusCompleted) {
		t.Errorf("expected terminal status to survive a late Transition, got %q", exec.Status)
	}
}

// TestExecutionLifecycle_Finish_Success verifies the success path atomically
// persists status/pr_url/commit_sha and saves metrics.
func TestExecutionLifecycle_Finish_Success(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		TaskID:       task.ID,
		Success:      true,
		PRUrl:        "https://github.com/qf-studio/pilot/pull/1",
		CommitSHA:    "deadbeef",
		TokensInput:  100,
		TokensOutput: 200,
		TokensTotal:  300,
	}
	outcome, err := lifecycle.Finish(execID, result, nil, 42*time.Second)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusCompleted {
		t.Errorf("expected outcome status %q, got %q", ExecStatusCompleted, outcome.Status)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusCompleted) {
		t.Errorf("expected status %q, got %q", ExecStatusCompleted, exec.Status)
	}
	if exec.PRUrl != result.PRUrl || exec.CommitSHA != result.CommitSHA {
		t.Errorf("expected pr_url/commit_sha to match result, got %+v", exec)
	}
	if exec.TokensTotal != result.TokensTotal {
		t.Errorf("expected metrics to be persisted: tokens_total = %d, want %d", exec.TokensTotal, result.TokensTotal)
	}
}

// TestExecutionLifecycle_Finish_DuplicateCallRejectsSecondWrite is the GH-4423
// regression test for finding 2: "a duplicate Finish overwrites a terminal
// state silently". A second Finish call against an execID that already
// reached a terminal status (e.g. a retried callback finishing the same
// execution twice with a different outcome) must not overwrite the first,
// genuine terminal status — Persist's CAS guard should reject the second
// write instead.
func TestExecutionLifecycle_Finish_DuplicateCallRejectsSecondWrite(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4423-T2", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	firstResult := &ExecutionResult{
		TaskID:    task.ID,
		Success:   true,
		PRUrl:     "https://github.com/qf-studio/pilot/pull/4423",
		CommitSHA: "first-commit",
	}
	if _, err := lifecycle.Finish(execID, firstResult, nil, time.Second); err != nil {
		t.Fatalf("first Finish failed: %v", err)
	}

	// A second, later Finish call for the same execID (e.g. a retried
	// callback) reports a completely different outcome — this must not land.
	secondResult := &ExecutionResult{TaskID: task.ID, Success: false, Error: "boom"}
	if _, err := lifecycle.Finish(execID, secondResult, nil, time.Second); err != nil {
		t.Fatalf("second Finish should not error even though its write is rejected, got: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusCompleted) {
		t.Errorf("expected the FIRST Finish's terminal status to survive, got %q", exec.Status)
	}
	if exec.PRUrl != firstResult.PRUrl || exec.CommitSHA != firstResult.CommitSHA {
		t.Errorf("expected the first Finish's pr_url/commit_sha to survive a duplicate call, got %+v", exec)
	}
}

// TestExecutionLifecycle_Finish_ExecErrShortCircuits verifies an execErr
// forces StatusFailed regardless of what result would otherwise classify to.
func TestExecutionLifecycle_Finish_ExecErrShortCircuits(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-5", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	execErr := errors.New("backend crashed")
	outcome, err := lifecycle.Finish(execID, nil, execErr, time.Second)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusFailed {
		t.Errorf("expected outcome status %q, got %q", ExecStatusFailed, outcome.Status)
	}
	if outcome.Error != execErr.Error() {
		t.Errorf("expected outcome error %q, got %q", execErr.Error(), outcome.Error)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusFailed) {
		t.Errorf("expected status %q, got %q", ExecStatusFailed, exec.Status)
	}
	if exec.Error != execErr.Error() {
		t.Errorf("expected error %q, got %q", execErr.Error(), exec.Error)
	}
}

// TestExecutionLifecycle_Finish_Override reproduces epic.go's work-loss guard:
// a sub-issue that reports Success with real commits but no PR must persist
// as failed even though TerminalStatus alone would call it completed.
func TestExecutionLifecycle_Finish_Override(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-6", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		TaskID:    task.ID,
		Success:   true,
		CommitSHA: "deadbeef",
		// PRUrl intentionally empty: commits with no PR is the stranded-work case.
	}

	if classified := TerminalStatus(result); classified != "completed" {
		t.Fatalf("test setup invalid: expected TerminalStatus to classify as completed without an override, got %q", classified)
	}

	outcome, err := lifecycle.Finish(execID, result, nil, time.Second, ExecStatusFailed)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusFailed {
		t.Errorf("expected override to force status %q, got %q", ExecStatusFailed, outcome.Status)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusFailed) {
		t.Errorf("expected status %q, got %q", ExecStatusFailed, exec.Status)
	}
}

// TestExecutionLifecycle_Finish_PRExistsPromotesToCompleted is the GH-4404
// regression test: pointer GH-16/GH-15 re-picked a task whose PR was already
// open because something downstream of PR creation (an intent-judge veto
// arriving after delivery — #4407's truncated-diff false-veto is the
// incident that exposed this) classified the attempt as a non-completed
// terminal status. That write made HasTerminalCompletion disagree with
// GitHub reality (a real PR existed but the row read "not done"), so the
// poller re-picked the task and risked a duplicate PR. A PR is ground truth
// that work was delivered, so Classify/Finish must promote to completed
// whenever result.PRUrl is non-empty, regardless of what TerminalStatus
// would otherwise classify the result as.
func TestExecutionLifecycle_Finish_PRExistsPromotesToCompleted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-16", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		TaskID:  task.ID,
		Success: false, // e.g. an intent-judge veto arriving after the PR was created
		Error:   "intent judge vetoed: diff appears to be missing implementation",
		PRUrl:   "https://github.com/qf-studio/pointer/pull/18",
	}

	if classified := TerminalStatus(result); classified != "failed" {
		t.Fatalf("test setup invalid: expected TerminalStatus to classify as failed without the PR-exists guard, got %q", classified)
	}

	outcome, err := lifecycle.Finish(execID, result, nil, 11*time.Minute)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusCompleted {
		t.Errorf("expected outcome status %q, got %q", ExecStatusCompleted, outcome.Status)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.Status != string(ExecStatusCompleted) {
		t.Errorf("expected status %q, got %q", ExecStatusCompleted, exec.Status)
	}
	if exec.PRUrl != result.PRUrl {
		t.Errorf("expected pr_url to be persisted, got %+v", exec)
	}

	done, err := store.HasTerminalCompletion(task.ID, task.ProjectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if !done {
		t.Error("expected HasTerminalCompletion to count the PR-delivered row as done — a false 'not done' here is exactly what let the poller re-pick and risk a duplicate PR")
	}
}

// TestExecutionLifecycle_Finish_OverrideWinsOverPRSelfHeal verifies the
// GH-4404 PR-exists self-heal never overrides an explicit caller override —
// epic.go's stranded-work override (executeSubIssuesTracked) must still be
// able to force a non-completed status even if, hypothetically, a PRUrl were
// present, since the caller's override reflects context (e.g. the PR itself
// being invalid/abandoned) that Classify cannot see from result alone.
func TestExecutionLifecycle_Finish_OverrideWinsOverPRSelfHeal(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-17", ProjectPath: "/tmp/project"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{
		TaskID:  task.ID,
		Success: false,
		PRUrl:   "https://github.com/qf-studio/pointer/pull/19",
	}

	outcome, err := lifecycle.Finish(execID, result, nil, time.Second, ExecStatusFailed)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusFailed {
		t.Errorf("expected explicit override to win over PR self-heal, got %q", outcome.Status)
	}
}

// TestExecutionLifecycle_Begin_EmptyTitle_BackfillsViaUpdateExecutionTitle is
// the GH-4281 integration test for the Begin+UpdateExecutionTitle round trip:
// a caller that starts an execution before a title is resolvable (Begin with
// task.Title == "") can backfill task_title once it resolves, without
// re-creating the row or losing the original execution ID.
func TestExecutionLifecycle_Begin_EmptyTitle_BackfillsViaUpdateExecutionTitle(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-7", ProjectPath: "/tmp/project"} // Title intentionally blank
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to load execution: %v", err)
	}
	if exec.TaskTitle != "" {
		t.Fatalf("expected blank task_title before backfill, got %q", exec.TaskTitle)
	}

	if err := store.UpdateExecutionTitle(execID, "resolved"); err != nil {
		t.Fatalf("UpdateExecutionTitle failed: %v", err)
	}

	exec, err = store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to reload execution: %v", err)
	}
	if exec.TaskTitle != "resolved" {
		t.Errorf("expected task_title %q after backfill, got %q", "resolved", exec.TaskTitle)
	}
	// execID must be stable across the backfill — UpdateExecutionTitle updates
	// the existing row rather than creating a new one.
	if exec.ID != execID {
		t.Errorf("expected execution ID to remain %q, got %q", execID, exec.ID)
	}
}

// TestExecutionLifecycle_Begin_SecondClaimLoses is the TASK-407/GH-4349 core
// invariant: two Begin calls for the same (task_id, project_path, generation
// 0, the default) must not both create an execution row — the second loses
// the claim and gets ErrClaimLost, not a second row.
func TestExecutionLifecycle_Begin_SecondClaimLoses(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task1 := &Task{ID: "GH-8", ProjectPath: "/tmp/project"}
	execID1, err := NewExecutionLifecycle(store).Begin(task1, ExecStatusRunning)
	if err != nil {
		t.Fatalf("first Begin failed: %v", err)
	}
	if task1.ExecutionID != execID1 {
		t.Fatalf("expected task1.ExecutionID %q, got %q", execID1, task1.ExecutionID)
	}

	task2 := &Task{ID: "GH-8", ProjectPath: "/tmp/project"}
	execID2, err := NewExecutionLifecycle(store).Begin(task2, ExecStatusRunning)
	if !errors.Is(err, ErrClaimLost) {
		t.Fatalf("expected second Begin to return ErrClaimLost, got execID=%q err=%v", execID2, err)
	}
	if execID2 != "" {
		t.Errorf("expected empty execID on claim loss, got %q", execID2)
	}
	if task2.ExecutionID != "" {
		t.Errorf("expected task2.ExecutionID to remain unstamped on claim loss, got %q", task2.ExecutionID)
	}

	exec, err := store.GetExecution(execID1)
	if err != nil {
		t.Fatalf("failed to load first execution: %v", err)
	}
	if exec.ID != execID1 {
		t.Errorf("expected the winning execution row to be the first Begin's, got %q", exec.ID)
	}
}

// TestExecutionLifecycle_Begin_GenerationPlusOneClaimsAfresh verifies a
// retry that claims generation+1 does not deadlock on its own prior claim —
// generation is part of the claim key, so a new generation is a fresh row.
func TestExecutionLifecycle_Begin_GenerationPlusOneClaimsAfresh(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-9", ProjectPath: "/tmp/project"}
	execID0, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("generation 0 Begin failed: %v", err)
	}

	// A second attempt at the SAME generation must still lose.
	if _, err := NewExecutionLifecycle(store).Begin(&Task{ID: "GH-9", ProjectPath: "/tmp/project"}, ExecStatusRunning, 0); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("expected concurrent same-generation Begin to lose, got: %v", err)
	}

	// The retry decider's generation+1 claims a fresh row instead of losing.
	retryTask := &Task{ID: "GH-9", ProjectPath: "/tmp/project"}
	execID1, err := NewExecutionLifecycle(store).Begin(retryTask, ExecStatusRunning, 1)
	if err != nil {
		t.Fatalf("generation 1 Begin failed: %v", err)
	}
	if execID1 == execID0 {
		t.Errorf("expected generation 1 to claim a distinct execution ID from generation 0")
	}
	if retryTask.ExecutionID != execID1 {
		t.Errorf("expected retryTask.ExecutionID %q, got %q", execID1, retryTask.ExecutionID)
	}
}

// TestExecutionLifecycle_Begin_ConcurrentClaims_ExactlyOneWinner is the
// entry-point-inventory-adjacent concurrency proof: N goroutines racing
// Begin for the same (task_id, project_path, generation) must produce
// exactly one winner and N-1 ErrClaimLost, never two execution rows. Run
// with -race.
func TestExecutionLifecycle_Begin_ConcurrentClaims_ExactlyOneWinner(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, losses int
	barrier := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			task := &Task{ID: "GH-10", ProjectPath: "/tmp/project"}
			_, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrClaimLost):
				losses++
			default:
				t.Errorf("unexpected Begin error: %v", err)
			}
		}()
	}
	close(barrier)
	wg.Wait()

	if wins != 1 {
		t.Errorf("expected exactly 1 winner, got %d", wins)
	}
	if losses != n-1 {
		t.Errorf("expected %d losses, got %d", n-1, losses)
	}
}

// TestExecutionLifecycle_NilStore_MethodsAreNoOps verifies every method
// degrades gracefully with a nil store, mirroring the "logStore == nil"
// guards this type consolidates.
func TestExecutionLifecycle_NilStore_MethodsAreNoOps(t *testing.T) {
	lifecycle := NewExecutionLifecycle(nil)

	if err := lifecycle.Transition("some-id", ExecStatusRunning); err != nil {
		t.Errorf("expected nil-store Transition to be a no-op, got: %v", err)
	}

	outcome, err := lifecycle.Finish("some-id", &ExecutionResult{Success: true}, nil, time.Second)
	if err != nil {
		t.Errorf("expected nil-store Finish to be a no-op, got: %v", err)
	}
	if outcome.Status != "" {
		t.Errorf("expected zero-value outcome from nil-store Finish, got: %+v", outcome)
	}

	if _, err := lifecycle.Cancel("some-task", "/tmp/project", "reason"); !errors.Is(err, ErrExecutionNotFound) {
		t.Errorf("expected nil-store Cancel to report ErrExecutionNotFound, got: %v", err)
	}
}

// TestExecutionLifecycle_Cancel is the GH-4678 acceptance test for `pilot
// task cancel`'s chokepoint: a real terminal cancel verb, distinct from the
// "stalled" recovery signal a GH-4655 operator mistakenly hand-wrote instead.
func TestExecutionLifecycle_Cancel(t *testing.T) {
	t.Run("queued row: cancels cleanly, records reason, journals StageCanceled", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4678-Q", ProjectPath: "/project-q"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		exec, err := NewExecutionLifecycle(store).Cancel(task.ID, task.ProjectPath, "operator: duplicate ticket")
		if err != nil {
			t.Fatalf("Cancel failed: %v", err)
		}
		if exec.Status != string(ExecStatusCanceled) {
			t.Errorf("expected returned status %q, got %q", ExecStatusCanceled, exec.Status)
		}
		if exec.Error != "operator: duplicate ticket" {
			t.Errorf("expected returned reason recorded, got %q", exec.Error)
		}

		persisted, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if persisted.Status != string(ExecStatusCanceled) {
			t.Errorf("expected persisted status %q, got %q", ExecStatusCanceled, persisted.Status)
		}
		if persisted.Error != "operator: duplicate ticket" {
			t.Errorf("expected persisted reason, got %q", persisted.Error)
		}

		events, err := store.ListExecutionEvents(execID)
		if err != nil {
			t.Fatalf("ListExecutionEvents: %v", err)
		}
		found := false
		for _, e := range events {
			if e.Stage == memory.StageCanceled {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a StageCanceled execution_events row, got events: %+v", events)
		}

		if !IsTerminalStatus(persisted.Status) {
			t.Error("expected IsTerminalStatus(canceled) to be true")
		}
	})

	t.Run("empty reason gets a default message instead of a blank error column", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4678-DEFAULT-REASON", ProjectPath: "/project-default"}
		if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued); err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		exec, err := NewExecutionLifecycle(store).Cancel(task.ID, task.ProjectPath, "")
		if err != nil {
			t.Fatalf("Cancel failed: %v", err)
		}
		if exec.Error == "" {
			t.Error("expected a non-empty default reason when the caller supplied none")
		}
	})

	t.Run("running row: refuses, names the execution ID, does not mutate the row", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4678-RUNNING", ProjectPath: "/project-running"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		exec, err := NewExecutionLifecycle(store).Cancel(task.ID, task.ProjectPath, "trying to stop it")
		if !errors.Is(err, ErrExecutionRunning) {
			t.Fatalf("expected ErrExecutionRunning, got: %v", err)
		}
		if exec == nil || exec.ID != execID {
			t.Fatalf("expected the running execution returned so the caller can report its ID, got: %+v", exec)
		}

		persisted, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if persisted.Status != string(ExecStatusRunning) {
			t.Errorf("expected row to remain 'running' (v1 refuses rather than mutating), got %q", persisted.Status)
		}
	})

	t.Run("already-terminal row: reports ErrExecutionAlreadyTerminal, clean no-op", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4678-TERMINAL", ProjectPath: "/project-terminal"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}
		if _, err := store.MarkExecutionCompletedIfNotTerminal(execID, "https://github.com/qf-studio/pilot/pull/1", "deadbeef", 1000); err != nil {
			t.Fatalf("setup complete: %v", err)
		}

		exec, err := NewExecutionLifecycle(store).Cancel(task.ID, task.ProjectPath, "too late")
		if !errors.Is(err, ErrExecutionAlreadyTerminal) {
			t.Fatalf("expected ErrExecutionAlreadyTerminal, got: %v", err)
		}
		if exec == nil || exec.Status != "completed" {
			t.Fatalf("expected the already-terminal execution returned unchanged, got: %+v", exec)
		}
	})

	t.Run("no execution at all: clean ErrExecutionNotFound no-op", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		exec, err := NewExecutionLifecycle(store).Cancel("GH-4678-NONE", "/project-none", "n/a")
		if !errors.Is(err, ErrExecutionNotFound) {
			t.Fatalf("expected ErrExecutionNotFound, got: %v", err)
		}
		if exec != nil {
			t.Errorf("expected nil execution for a not-found cancel, got: %+v", exec)
		}
	})
}

// setupFakeGHForLifecycleTest installs a fake `gh` binary at the front of
// PATH (mirroring dispatcher_test.go's TestDispatcher_StallTaskAfterRepickHardCap_*
// pattern) that appends its argv to a log file instead of touching GitHub, and
// returns that log file's path so the test can assert on the exact CLI
// invocation.
func setupFakeGHForLifecycleTest(t *testing.T) (logFile string) {
	t.Helper()
	fakeBin := t.TempDir()
	logFile = filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" + `echo "$@" >> "` + logFile + `"` + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
	return logFile
}

// TestExecutionLifecycle_Finish_FailedGitHubTask_StripsInProgressLabel is the
// GH-4740 regression test: a failed execution against a GitHub-sourced task
// must strip pilot-in-progress from the issue through the same Persist
// chokepoint every terminal write (including a hard-killed executor's
// tripwire/Finish path) goes through — not just the happy-path teardown.
// Evidence this guards against: console#98 failed 14:41:05Z and sat with
// pilot-in-progress on the OPEN issue for 1.5h+, inert behind the still-present
// `pilot` trigger label, until an operator stripped it by hand.
func TestExecutionLifecycle_Finish_FailedGitHubTask_StripsInProgressLabel(t *testing.T) {
	logFile := setupFakeGHForLifecycleTest(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{
		ID:            "GH-4740",
		ProjectPath:   t.TempDir(),
		SourceAdapter: "github",
		SourceIssueID: "4740",
	}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{TaskID: task.ID, Success: false, Error: "unknown: exit status 1"}
	outcome, err := lifecycle.Finish(execID, result, errors.New("unknown: exit status 1"), 42*time.Second)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusFailed {
		t.Fatalf("expected outcome status %q, got %q", ExecStatusFailed, outcome.Status)
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("expected gh CLI to be invoked to strip pilot-in-progress, but log file missing: %v", err)
	}
	calls := string(logBytes)
	if !strings.Contains(calls, "issue edit 4740") {
		t.Errorf("expected a label edit on issue 4740, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "--remove-label pilot-in-progress") {
		t.Errorf("expected pilot-in-progress to be removed, got calls:\n%s", calls)
	}
}

// TestExecutionLifecycle_Finish_NonGitHubTask_SkipsInProgressStrip mirrors
// TestDispatcher_StallTaskAfterRepickHardCap_NonGitHubTaskSkipsGHCLI: a failed
// execution for a non-GitHub-sourced task (or a CLI-driven task with no
// source adapter at all) has no GitHub issue to edit, so Finish must not
// shell out to `gh`.
func TestExecutionLifecycle_Finish_NonGitHubTask_SkipsInProgressStrip(t *testing.T) {
	logFile := setupFakeGHForLifecycleTest(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GL-4740", ProjectPath: t.TempDir(), SourceAdapter: "gitlab", SourceIssueID: "4740"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	if _, err := lifecycle.Finish(execID, &ExecutionResult{TaskID: task.ID, Success: false, Error: "boom"}, errors.New("boom"), time.Second); err != nil {
		t.Fatalf("Finish failed: %v", err)
	}

	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("expected no gh CLI invocation for a non-GitHub task")
	}
}

// TestExecutionLifecycle_Finish_Completed_DoesNotStripInProgressLabel verifies
// a successful/completed execution does NOT strip pilot-in-progress: a PR
// exists at that point, and the label is deliberately kept until the PR
// merges (autopilot's post-merge RemoveLabel path), per GH-4740's acceptance
// criteria that scoped the strip to terminal outcomes leaving no deliverable
// behind.
func TestExecutionLifecycle_Finish_Completed_DoesNotStripInProgressLabel(t *testing.T) {
	logFile := setupFakeGHForLifecycleTest(t)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4740-OK", ProjectPath: t.TempDir(), SourceAdapter: "github", SourceIssueID: "9999"}
	lifecycle := NewExecutionLifecycle(store)
	execID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("Begin failed: %v", err)
	}

	result := &ExecutionResult{TaskID: task.ID, Success: true, PRUrl: "https://github.com/qf-studio/pilot/pull/1", CommitSHA: "deadbeef"}
	outcome, err := lifecycle.Finish(execID, result, nil, time.Second)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	if outcome.Status != ExecStatusCompleted {
		t.Fatalf("expected outcome status %q, got %q", ExecStatusCompleted, outcome.Status)
	}

	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("expected no gh CLI invocation for a completed execution")
	}
}

// setupFailingFakeGHForLifecycleTest mirrors setupFakeGHForLifecycleTest but
// the fake `gh` always exits non-zero, simulating the strip call itself
// failing (e.g. a revoked token or 403'd repo — the GH-4866 kill-drill's
// sandbox-repo-archived scenario).
func setupFailingFakeGHForLifecycleTest(t *testing.T) {
	t.Helper()
	fakeBin := t.TempDir()
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\necho 'gh: 403 Forbidden' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))
}

// TestExecutionLifecycle_Finish_FailedGitHubTask_RecordsLabelStripDeadManEvents
// is GH-4866's seam test for the label-strip dead-man tracker: the
// kill-drill found stripInProgressLabelOnTerminalFailure logging `failed to
// strip pilot-in-progress label` ERRORs for 57 minutes straight with zero
// dead-man signal, because nothing here ever recorded into a tracker at all
// (the same "wired to nothing" shape already fixed for self-review/
// label-lifecycle). Exercises the real seam — a real *ExecutionLifecycle
// crossing into a real fakeAlertProcessor via SetAlertProcessor, not an
// argument-discarding mock (TASK-441 L1) — for both the failure and success
// outcomes of the strip call itself.
func TestExecutionLifecycle_Finish_FailedGitHubTask_RecordsLabelStripDeadManEvents(t *testing.T) {
	t.Run("strip call fails: records attempt+failure", func(t *testing.T) {
		setupFailingFakeGHForLifecycleTest(t)

		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4866-STRIP-FAIL", ProjectPath: t.TempDir(), SourceAdapter: "github", SourceIssueID: "4866"}
		lifecycle := NewExecutionLifecycle(store)
		processor := &fakeAlertProcessor{}
		lifecycle.SetAlertProcessor(processor)
		execID, err := lifecycle.Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("Begin failed: %v", err)
		}

		result := &ExecutionResult{TaskID: task.ID, Success: false, Error: "boom"}
		if _, err := lifecycle.Finish(execID, result, errors.New("boom"), time.Second); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		var attempts, failures, successes int
		for _, event := range processor.events {
			if event.Metadata["tracker"] != LabelStripDeadManTrackerName {
				continue
			}
			switch event.Type {
			case AlertEventTypeDeadManAttempt:
				attempts++
			case AlertEventTypeDeadManFailure:
				failures++
				if event.Error == "" {
					t.Error("expected DeadManFailure event to carry the underlying gh error, got empty Error")
				}
			case AlertEventTypeDeadManSuccess:
				successes++
			}
		}
		if attempts != 1 {
			t.Errorf("expected exactly 1 DeadManAttempt event, got %d", attempts)
		}
		if failures != 1 {
			t.Errorf("expected exactly 1 DeadManFailure event, got %d", failures)
		}
		if successes != 0 {
			t.Errorf("expected 0 DeadManSuccess events, got %d", successes)
		}
	})

	t.Run("strip call succeeds: records attempt+success", func(t *testing.T) {
		setupFakeGHForLifecycleTest(t)

		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4866-STRIP-OK", ProjectPath: t.TempDir(), SourceAdapter: "github", SourceIssueID: "4867"}
		lifecycle := NewExecutionLifecycle(store)
		processor := &fakeAlertProcessor{}
		lifecycle.SetAlertProcessor(processor)
		execID, err := lifecycle.Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("Begin failed: %v", err)
		}

		result := &ExecutionResult{TaskID: task.ID, Success: false, Error: "boom"}
		if _, err := lifecycle.Finish(execID, result, errors.New("boom"), time.Second); err != nil {
			t.Fatalf("Finish failed: %v", err)
		}

		var attempts, failures, successes int
		for _, event := range processor.events {
			if event.Metadata["tracker"] != LabelStripDeadManTrackerName {
				continue
			}
			switch event.Type {
			case AlertEventTypeDeadManAttempt:
				attempts++
			case AlertEventTypeDeadManFailure:
				failures++
			case AlertEventTypeDeadManSuccess:
				successes++
			}
		}
		if attempts != 1 {
			t.Errorf("expected exactly 1 DeadManAttempt event, got %d", attempts)
		}
		if successes != 1 {
			t.Errorf("expected exactly 1 DeadManSuccess event, got %d", successes)
		}
		if failures != 0 {
			t.Errorf("expected 0 DeadManFailure events, got %d", failures)
		}
	})
}
