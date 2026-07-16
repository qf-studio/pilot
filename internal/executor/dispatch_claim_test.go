package executor

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

// TestDispatchClaim_EntryPointInventory is the TASK-407/GH-4349
// entry-point-inventory acceptance test: every dispatch channel that can
// start an execution — the poller/dispatcher's queueSingleTask, the
// decomposed-parent site in queueDecomposedTask, the epic sub-issue loop
// (epic.go's previously-unguarded channel), and the CLI's
// recordCLITaskStart — funnels through the same ExecutionLifecycle.Begin
// chokepoint (internal/executor/lifecycle.go:93) rather than each hand-
// rolling its own guard. This table races N goroutines per channel against
// the identical (task, project, generation 0) key that channel's real call
// site would claim; regardless of which channel "wins" the race in
// production, exactly one Begin call may succeed and every other caller
// must observe ErrClaimLost, never a second executions row.
func TestDispatchClaim_EntryPointInventory(t *testing.T) {
	const n = 8

	channels := []struct {
		// name documents the real call site this subtest's Begin race stands
		// in for.
		name        string
		taskID      string
		projectPath string
	}{
		{
			name:        "dispatcher queueSingleTask (poller entry point, dispatcher.go:689)",
			taskID:      "GH-entry-poller",
			projectPath: "/tmp/project-poller",
		},
		{
			name:        "dispatcher queueDecomposedTask parent (dispatcher.go:631)",
			taskID:      "GH-entry-decomposed-parent",
			projectPath: "/tmp/project-decomposed",
		},
		{
			name:        "epic sub-issue loop (epic-direct entry point, epic.go:2317)",
			taskID:      "GH-entry-epic-subissue",
			projectPath: "/tmp/project-epic",
		},
		{
			name:        "CLI recordCLITaskStart (cmd/pilot/commands.go:1039)",
			taskID:      "GH-entry-cli",
			projectPath: "/tmp/project-cli",
		},
	}

	for _, ch := range channels {
		ch := ch
		t.Run(ch.name, func(t *testing.T) {
			t.Parallel()

			store, cleanup := setupTestStore(t)
			defer cleanup()

			var wg sync.WaitGroup
			var mu sync.Mutex
			var wins, losses int
			barrier := make(chan struct{})

			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-barrier
					task := &Task{ID: ch.taskID, ProjectPath: ch.projectPath}
					_, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						wins++
					case errors.Is(err, ErrClaimLost):
						losses++
					default:
						t.Errorf("%s: unexpected Begin error: %v", ch.name, err)
					}
				}()
			}
			close(barrier)
			wg.Wait()

			if wins != 1 {
				t.Errorf("%s: expected exactly 1 winner, got %d", ch.name, wins)
			}
			if losses != n-1 {
				t.Errorf("%s: expected %d losses, got %d", ch.name, n-1, losses)
			}
		})
	}
}

// TestDispatchClaim_RetryGenerationPlusOne covers the "retry deciders thread
// generation+1" half of TASK-407's acceptance criteria: a legitimate retry
// (retry-after-terminal-failure, conflict close-and-reexecute, restart-reap
// re-pickup, or an autopilot CI-fix re-dispatch) must claim generation+1
// rather than reusing the generation its own prior attempt already holds —
// reusing it would deadlock the retry against itself, since Begin's claim is
// unconditional on the caller being the same process.
func TestDispatchClaim_RetryGenerationPlusOne(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	taskID, projectPath := "GH-entry-retry", "/tmp/project-retry"

	// The initial dispatch claims generation 0.
	initial := &Task{ID: taskID, ProjectPath: projectPath}
	initialExecID, err := NewExecutionLifecycle(store).Begin(initial, ExecStatusRunning, 0)
	if err != nil {
		t.Fatalf("initial generation 0 Begin failed: %v", err)
	}

	// A retry decider that reused generation 0 (the bug this test guards
	// against) would lose to its own prior claim.
	sameGenRetry := &Task{ID: taskID, ProjectPath: projectPath}
	if _, err := NewExecutionLifecycle(store).Begin(sameGenRetry, ExecStatusRunning, 0); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("expected a same-generation retry to lose to its own prior claim, got: %v", err)
	}

	// The retry decider claims generation+1 instead, and wins a fresh row.
	retry := &Task{ID: taskID, ProjectPath: projectPath}
	retryExecID, err := NewExecutionLifecycle(store).Begin(retry, ExecStatusRunning, 1)
	if err != nil {
		t.Fatalf("generation+1 retry Begin failed: %v", err)
	}
	if retryExecID == initialExecID {
		t.Errorf("expected the generation+1 retry to claim a distinct execution ID from the initial generation 0 claim")
	}

	// Concurrent retry deciders racing the SAME generation+1 (e.g. a
	// conflict-retry and a CI-fix retry both firing for the same failed
	// task) must still only produce one winner.
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
			task := &Task{ID: taskID, ProjectPath: projectPath}
			_, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning, 2)
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
		t.Errorf("expected exactly 1 winner among concurrent generation 2 retries, got %d", wins)
	}
	if losses != n-1 {
		t.Errorf("expected %d losses among concurrent generation 2 retries, got %d", n-1, losses)
	}
}

// TestDispatchClaim_MultiProcess_TwoStoreHandlesOneFile is the AC's
// multi-process verification: two independent *memory.Store handles opened
// against the same on-disk SQLite file (standing in for two separate pilot
// processes — e.g. a daemon restart racing the still-shutting-down prior
// instance) must still serialize through execution_claims' PRIMARY KEY, so
// concurrent claims for the same (task, project, generation) produce exactly
// one winner regardless of which store handle observes it.
func TestDispatchClaim_MultiProcess_TwoStoreHandlesOneFile(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "pilot-dispatch-claim-multiprocess")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(dataDir) }()

	storeA, err := memory.NewStore(dataDir)
	if err != nil {
		t.Fatalf("failed to open first store handle: %v", err)
	}
	defer func() { _ = storeA.Close() }()

	storeB, err := memory.NewStore(dataDir)
	if err != nil {
		t.Fatalf("failed to open second store handle on the same data dir: %v", err)
	}
	defer func() { _ = storeB.Close() }()

	taskID, projectPath := "GH-entry-multiprocess", "/tmp/project-multiprocess"

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins, losses int
	barrier := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		// Alternate which store handle ("process") issues the claim so both
		// connections are contended concurrently.
		store := storeA
		if i%2 == 1 {
			store = storeB
		}
		go func(store *memory.Store) {
			defer wg.Done()
			<-barrier
			claimed, err := store.ClaimExecution(taskID, projectPath, 0, "exec-multiprocess")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("ClaimExecution failed: %v", err)
				return
			}
			if claimed {
				wins++
			} else {
				losses++
			}
		}(store)
	}
	close(barrier)
	wg.Wait()

	if wins != 1 {
		t.Errorf("expected exactly 1 winner across two store handles on one file, got %d", wins)
	}
	if losses != n-1 {
		t.Errorf("expected %d losses across two store handles on one file, got %d", n-1, losses)
	}
}
