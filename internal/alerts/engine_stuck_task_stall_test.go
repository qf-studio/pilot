package alerts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// stuckTaskStallTestConfig mirrors TestEngine_EvaluateStuckTasks_OrphanEviction's
// config: a single enabled task_stuck rule is all evaluateStuckTasks needs to
// enter its orphan-eviction branch.
func stuckTaskStallTestConfig() *AlertConfig {
	return &AlertConfig{
		Enabled: true,
		Channels: []ChannelConfig{
			{Name: "test-channel", Type: "webhook", Enabled: true},
		},
		Rules: []AlertRule{
			{
				Name:    "task_stuck",
				Type:    AlertTypeTaskStuck,
				Enabled: true,
				Condition: RuleCondition{
					ProgressUnchangedFor: 10 * time.Minute,
				},
				Severity: SeverityWarning,
				Channels: []string{"test-channel"},
				Cooldown: 0,
			},
		},
	}
}

// TestEngine_EvaluateStuckTasks_StallsOrphanedExecutionViaLifecycle is the
// GH-4562 acceptance test (a): a task's execution row still sitting "running"
// when the stuck-task evictor drops its in-memory taskLastProgress entry must
// be transitioned to "stalled" through the ExecutionLifecycle chokepoint —
// not just silently forgotten by the alerts engine's own bookkeeping, which
// would leave the row (and its execution_claims generation, TASK-407) live
// forever with no owner left to reconcile it.
func TestEngine_EvaluateStuckTasks_StallsOrphanedExecutionViaLifecycle(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const projectPath = "/proj"
	lifecycle := executor.NewExecutionLifecycle(store)

	task := &executor.Task{ID: "GH-9001", ProjectPath: projectPath}
	execID, err := lifecycle.Begin(task, executor.ExecStatusQueued)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := lifecycle.Transition(execID, executor.ExecStatusRunning); err != nil {
		t.Fatalf("Transition(running): %v", err)
	}

	config := stuckTaskStallTestConfig()
	mockCh := newMockChannel("test-channel", "webhook")
	dispatcher := NewDispatcher(config)
	dispatcher.RegisterChannel(mockCh)

	engine := NewEngine(config, WithDispatcher(dispatcher), WithExecutionLifecycle(lifecycle))

	// Seed an orphaned taskLastProgress entry: stuck for 130 min >
	// minOrphanEvictionThreshold (120m).
	engine.mu.Lock()
	engine.taskLastProgress[task.ID] = progressState{
		Progress:  10,
		UpdatedAt: time.Now().Add(-130 * time.Minute),
	}
	engine.mu.Unlock()

	engine.evaluateStuckTasks(context.Background())

	// The in-memory tracker entry is evicted, as before this change.
	engine.mu.RLock()
	_, stillTracked := engine.taskLastProgress[task.ID]
	engine.mu.RUnlock()
	if stillTracked {
		t.Error("expected orphaned taskLastProgress entry to be evicted")
	}

	// The execution row is now "stalled" with an error naming the eviction
	// and how long the task was stuck.
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != string(executor.ExecStatusStalled) {
		t.Fatalf("execution status = %q, want %q", exec.Status, executor.ExecStatusStalled)
	}
	if !strings.Contains(exec.Error, "orphan eviction after") {
		t.Errorf("execution error = %q, want it to mention orphan eviction", exec.Error)
	}
	if !strings.Contains(exec.Error, "h") && !strings.Contains(exec.Error, "m") {
		t.Errorf("execution error = %q, want it to carry the stuck duration", exec.Error)
	}

	// Claim release, verified the same way as GH-4561/#4563 (subtask 1):
	// nextRetryGeneration (internal/executor/dispatcher.go, unexported) grants
	// a fresh generation once (1) the claimed generation's execution is
	// terminal and (2) the task is not yet "done" per HasTerminalCompletion.
	// That method isn't reachable from this package, so its two preconditions
	// are asserted directly here via the exported surface it's built from —
	// together they guarantee nextRetryGeneration would return (gen=1,
	// retry=true), i.e. the stall IS the claim release, no separate release
	// call needed.
	gen, claimExecID, found, err := store.LatestClaimGeneration(task.ID, projectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	}
	if !found || gen != 0 || claimExecID != execID {
		t.Fatalf("LatestClaimGeneration = (gen=%d, execID=%q, found=%v), want (0, %q, true)", gen, claimExecID, found, execID)
	}
	if !executor.IsTerminalStatus(exec.Status) {
		t.Errorf("expected stalled status %q to be terminal (precondition for nextRetryGeneration's retry grant)", exec.Status)
	}
	done, err := executor.HasTerminalCompletion(store, task.ID, projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if done {
		t.Error("expected HasTerminalCompletion=false for a stalled task with no PR/commit — a stalled task must remain re-dispatchable")
	}
}

// TestEngine_EvaluateStuckTasks_OrphanEvictionOnTerminalRowIsNoOp is the
// GH-4562 acceptance test (b): evicting a taskLastProgress entry whose
// execution row already reached a terminal status — either because the task
// genuinely finished around the same time the tracker went stale, or because
// a second eviction sweep re-examines a row this same mechanism already
// stalled — must be a no-op. It must not error, and it must not overwrite the
// row's already-terminal status/error with a fresh orphan-eviction reason.
func TestEngine_EvaluateStuckTasks_OrphanEvictionOnTerminalRowIsNoOp(t *testing.T) {
	tests := []struct {
		name          string
		terminalizeFn func(t *testing.T, lifecycle *executor.ExecutionLifecycle, execID string)
	}{
		{
			name: "already completed",
			terminalizeFn: func(t *testing.T, lifecycle *executor.ExecutionLifecycle, execID string) {
				t.Helper()
				if _, err := lifecycle.Finish(execID, &executor.ExecutionResult{Success: true}, nil, 0, executor.ExecStatusCompleted); err != nil {
					t.Fatalf("Finish(completed): %v", err)
				}
			},
		},
		{
			name: "already stalled by a prior eviction sweep",
			terminalizeFn: func(t *testing.T, lifecycle *executor.ExecutionLifecycle, execID string) {
				t.Helper()
				if _, err := lifecycle.Finish(execID, nil, errors.New("orphan eviction after 2h10m0s stuck"), 0, executor.ExecStatusStalled); err != nil {
					t.Fatalf("Finish(stalled): %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := memory.NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("memory.NewStore: %v", err)
			}
			defer func() { _ = store.Close() }()

			const projectPath = "/proj"
			lifecycle := executor.NewExecutionLifecycle(store)

			task := &executor.Task{ID: "GH-9002", ProjectPath: projectPath}
			execID, err := lifecycle.Begin(task, executor.ExecStatusQueued)
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			if err := lifecycle.Transition(execID, executor.ExecStatusRunning); err != nil {
				t.Fatalf("Transition(running): %v", err)
			}
			tt.terminalizeFn(t, lifecycle, execID)

			before, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution (before): %v", err)
			}

			config := stuckTaskStallTestConfig()
			mockCh := newMockChannel("test-channel", "webhook")
			dispatcher := NewDispatcher(config)
			dispatcher.RegisterChannel(mockCh)
			engine := NewEngine(config, WithDispatcher(dispatcher), WithExecutionLifecycle(lifecycle))

			engine.mu.Lock()
			engine.taskLastProgress[task.ID] = progressState{
				Progress:  10,
				UpdatedAt: time.Now().Add(-130 * time.Minute),
			}
			engine.mu.Unlock()

			// Fire the evictor twice — once via evaluateStuckTasks (the real
			// call path) and once more directly, to explicitly exercise "a
			// second eviction of an already-terminal row" per the task spec.
			engine.evaluateStuckTasks(context.Background())
			engine.stallOrphanedExecution(task.ID, 130*time.Minute)

			after, err := store.GetExecution(execID)
			if err != nil {
				t.Fatalf("GetExecution (after): %v", err)
			}
			if after.Status != before.Status {
				t.Errorf("status changed on terminal-row eviction: before=%q after=%q", before.Status, after.Status)
			}
			if after.Error != before.Error {
				t.Errorf("error text changed on terminal-row eviction: before=%q after=%q", before.Error, after.Error)
			}

			engine.mu.RLock()
			_, stillTracked := engine.taskLastProgress[task.ID]
			engine.mu.RUnlock()
			if stillTracked {
				t.Error("expected taskLastProgress entry to still be evicted regardless of execution row terminality")
			}
		})
	}
}
