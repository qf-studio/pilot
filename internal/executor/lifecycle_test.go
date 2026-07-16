package executor

import (
	"errors"
	"sync"
	"testing"
	"time"
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
}
