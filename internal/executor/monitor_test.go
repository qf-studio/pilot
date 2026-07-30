package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func TestNewMonitor(t *testing.T) {
	monitor := NewMonitor()

	if monitor == nil {
		t.Fatal("NewMonitor returned nil")
	}
	if monitor.tasks == nil {
		t.Error("tasks map not initialized")
	}
}

func TestMonitorRegister(t *testing.T) {
	monitor := NewMonitor()

	monitor.Register("task-1", "Test Task", "")

	state, ok := monitor.Get("task-1")
	if !ok {
		t.Fatal("Failed to get registered task")
	}
	if state.ID != "task-1" {
		t.Errorf("Expected ID 'task-1', got '%s'", state.ID)
	}
	if state.Title != "Test Task" {
		t.Errorf("Expected title 'Test Task', got '%s'", state.Title)
	}
	if state.Status != StatusPending {
		t.Errorf("Expected status pending, got %s", state.Status)
	}
}

func TestMonitorQueue(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")

	monitor.Queue("task-1")

	state, _ := monitor.Get("task-1")
	if state.Status != StatusQueued {
		t.Errorf("Expected status queued, got %s", state.Status)
	}
	if state.Phase != "Queued" {
		t.Errorf("Expected phase 'Queued', got '%s'", state.Phase)
	}
}

func TestMonitorQueueThenStart(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")

	monitor.Queue("task-1")
	state, _ := monitor.Get("task-1")
	if state.Status != StatusQueued {
		t.Errorf("Expected status queued, got %s", state.Status)
	}

	monitor.Start("task-1")
	state, _ = monitor.Get("task-1")
	if state.Status != StatusRunning {
		t.Errorf("Expected status running after start, got %s", state.Status)
	}
	if state.StartedAt == nil {
		t.Error("StartedAt not set after start")
	}
}

func TestMonitorStart(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")

	monitor.Start("task-1")

	state, _ := monitor.Get("task-1")
	if state.Status != StatusRunning {
		t.Errorf("Expected status running, got %s", state.Status)
	}
	if state.StartedAt == nil {
		t.Error("StartedAt not set")
	}
}

func TestMonitorUpdateProgress(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.Start("task-1")

	monitor.UpdateProgress("task-1", "IMPL", 50, "Working...")

	state, _ := monitor.Get("task-1")
	if state.Phase != "IMPL" {
		t.Errorf("Expected phase 'IMPL', got '%s'", state.Phase)
	}
	if state.Progress != 50 {
		t.Errorf("Expected progress 50, got %d", state.Progress)
	}
	if state.Message != "Working..." {
		t.Errorf("Expected message 'Working...', got '%s'", state.Message)
	}
}

func TestMonitorComplete(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.Start("task-1")

	monitor.Complete("task-1", "https://github.com/org/repo/pull/1")

	state, _ := monitor.Get("task-1")
	if state.Status != StatusCompleted {
		t.Errorf("Expected status completed, got %s", state.Status)
	}
	if state.PRUrl != "https://github.com/org/repo/pull/1" {
		t.Errorf("Expected PR URL, got '%s'", state.PRUrl)
	}
	if state.CompletedAt == nil {
		t.Error("CompletedAt not set")
	}
}

func TestMonitorFail(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.Start("task-1")

	monitor.Fail("task-1", "Something went wrong")

	state, _ := monitor.Get("task-1")
	if state.Status != StatusFailed {
		t.Errorf("Expected status failed, got %s", state.Status)
	}
	if state.Error != "Something went wrong" {
		t.Errorf("Expected error message, got '%s'", state.Error)
	}
}

// TestMonitorNoOp verifies NoOp lands the card on StatusNoOp — distinct from
// Fail's StatusFailed — since a no-commit run is a non-failure terminal
// outcome (GH-4490 subtask 2).
func TestMonitorNoOp(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.Start("task-1")

	monitor.NoOp("task-1", "no new commit produced — worktree HEAD matches base branch parent")

	state, _ := monitor.Get("task-1")
	if state.Status != StatusNoOp {
		t.Errorf("Expected status no_op, got %s", state.Status)
	}
	if state.Error != "no new commit produced — worktree HEAD matches base branch parent" {
		t.Errorf("Expected error message, got '%s'", state.Error)
	}
	if state.CompletedAt == nil {
		t.Error("CompletedAt not set")
	}
}

func TestMonitorGetAll(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Register("task-2", "Task 2", "")
	monitor.Register("task-3", "Task 3", "")

	all := monitor.GetAll()
	if len(all) != 3 {
		t.Errorf("Expected 3 tasks, got %d", len(all))
	}
}

func TestMonitorGetRunning(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Register("task-2", "Task 2", "")
	monitor.Start("task-1")

	running := monitor.GetRunning()
	if len(running) != 1 {
		t.Errorf("Expected 1 running task, got %d", len(running))
	}
	if running[0].ID != "task-1" {
		t.Errorf("Expected task-1, got %s", running[0].ID)
	}
}

func TestMonitorCount(t *testing.T) {
	monitor := NewMonitor()

	if monitor.Count() != 0 {
		t.Error("Expected count 0 for empty monitor")
	}

	monitor.Register("task-1", "Task 1", "")
	monitor.Register("task-2", "Task 2", "")

	if monitor.Count() != 2 {
		t.Errorf("Expected count 2, got %d", monitor.Count())
	}
}

func TestMonitorRemove(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")

	monitor.Remove("task-1")

	_, ok := monitor.Get("task-1")
	if ok {
		t.Error("Task should have been removed")
	}
}

func TestMonitorCancel(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.Start("task-1")

	monitor.Cancel("task-1")

	state, ok := monitor.Get("task-1")
	if !ok {
		t.Fatal("Task should still exist after cancel")
	}
	if state.Status != StatusCancelled {
		t.Errorf("Expected status cancelled, got %s", state.Status)
	}
	if state.Phase != "Cancelled" {
		t.Errorf("Expected phase 'Cancelled', got '%s'", state.Phase)
	}
	if state.CompletedAt == nil {
		t.Error("CompletedAt should be set on cancel")
	}
}

func TestMonitorGetNonexistent(t *testing.T) {
	monitor := NewMonitor()

	state, ok := monitor.Get("nonexistent")
	if ok {
		t.Error("Should not find nonexistent task")
	}
	if state != nil {
		t.Error("State should be nil for nonexistent task")
	}
}

func TestMonitorOperationsOnNonexistent(t *testing.T) {
	monitor := NewMonitor()

	// These should not panic on nonexistent tasks, and stay no-ops (unlike
	// UpdateProgress, see TestMonitorUpdateProgressCreatesUnknownTask).
	monitor.Start("nonexistent")
	monitor.Complete("nonexistent", "url")
	monitor.Fail("nonexistent", "error")
	monitor.Cancel("nonexistent")
	monitor.Remove("nonexistent")

	// Count should still be 0
	if monitor.Count() != 0 {
		t.Errorf("Count should be 0, got %d", monitor.Count())
	}
}

// GH-4246: a progress event for a taskID the monitor has never seen (e.g. a
// hydration race, or a task whose Register call was lost across a restart)
// must create the entry rather than silently drop the event — this was the
// live-view half of the "queue panel blind after restart" defect.
func TestMonitorUpdateProgressCreatesUnknownTask(t *testing.T) {
	monitor := NewMonitor()

	monitor.UpdateProgress("unknown-task", "IMPL", 42, "Working...")

	state, ok := monitor.Get("unknown-task")
	if !ok {
		t.Fatal("UpdateProgress should create an entry for an unknown taskID")
	}
	if state.Status != StatusRunning {
		t.Errorf("Expected status running for auto-created entry, got %s", state.Status)
	}
	if state.Phase != "IMPL" {
		t.Errorf("Expected phase 'IMPL', got '%s'", state.Phase)
	}
	if state.Progress != 42 {
		t.Errorf("Expected progress 42, got %d", state.Progress)
	}
	if state.Message != "Working..." {
		t.Errorf("Expected message 'Working...', got '%s'", state.Message)
	}
	if monitor.Count() != 1 {
		t.Errorf("Expected count 1 after auto-create, got %d", monitor.Count())
	}
}

func TestMonitorUpdateProgressEmptyMessage(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.UpdateProgress("task-1", "Phase1", 25, "Initial message")

	// Update with empty message should not overwrite existing message
	monitor.UpdateProgress("task-1", "Phase2", 50, "")

	state, _ := monitor.Get("task-1")
	if state.Phase != "Phase2" {
		t.Errorf("Phase should update to Phase2, got %s", state.Phase)
	}
	if state.Progress != 50 {
		t.Errorf("Progress should update to 50, got %d", state.Progress)
	}
	if state.Message != "Initial message" {
		t.Errorf("Empty message should not overwrite, got '%s'", state.Message)
	}
}

func TestMonitorGetReturnsCopy(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Test Task", "")
	monitor.Start("task-1")

	state1, _ := monitor.Get("task-1")
	state1.Progress = 999 // Modify the copy

	state2, _ := monitor.Get("task-1")
	if state2.Progress == 999 {
		t.Error("Get should return a copy, not the original")
	}
}

func TestMonitorGetRunningMultiple(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Register("task-2", "Task 2", "")
	monitor.Register("task-3", "Task 3", "")

	monitor.Start("task-1")
	monitor.Start("task-2")
	// task-3 remains pending

	running := monitor.GetRunning()
	if len(running) != 2 {
		t.Errorf("Expected 2 running tasks, got %d", len(running))
	}

	// Verify both running tasks are included
	ids := map[string]bool{}
	for _, s := range running {
		ids[s.ID] = true
	}
	if !ids["task-1"] || !ids["task-2"] {
		t.Error("GetRunning should include task-1 and task-2")
	}
	if ids["task-3"] {
		t.Error("GetRunning should not include pending task-3")
	}
}

func TestMonitorGetRunningNoRunning(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Register("task-2", "Task 2", "")

	running := monitor.GetRunning()
	if len(running) != 0 {
		t.Errorf("Expected 0 running tasks, got %d", len(running))
	}
}

func TestMonitorGetRunningTaskIDs(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Register("task-2", "Task 2", "")
	monitor.Register("task-3", "Task 3", "")

	monitor.Queue("task-1")
	monitor.Start("task-2")
	// task-3 stays pending

	ids := monitor.GetRunningTaskIDs()
	if len(ids) != 2 {
		t.Fatalf("Expected 2 active tasks (queued+running), got %d", len(ids))
	}
	// Should include both queued and running
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found["task-1"] {
		t.Error("Expected queued task-1 in active list")
	}
	if !found["task-2"] {
		t.Error("Expected running task-2 in active list")
	}
	if found["task-3"] {
		t.Error("Pending task-3 should not be in active list")
	}
}

func TestMonitorWaitForTasks(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Start("task-1")

	// Complete task in background after 100ms
	go func() {
		time.Sleep(100 * time.Millisecond)
		monitor.Complete("task-1", "")
	}()

	ctx := context.Background()
	err := monitor.WaitForTasks(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitForTasks should succeed, got: %v", err)
	}
}

func TestMonitorWaitForTasksTimeout(t *testing.T) {
	monitor := NewMonitor()
	monitor.Register("task-1", "Task 1", "")
	monitor.Start("task-1")
	// Never complete task-1

	ctx := context.Background()
	err := monitor.WaitForTasks(ctx, 100*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForTasks should timeout")
	}
	// GH-4609: the self-upgrade alert path (cmd/pilot's drainTimeoutAlertGate)
	// needs to distinguish a drain-timeout from any other TaskChecker
	// failure via errors.Is, not by parsing the message.
	if !errors.Is(err, ErrDrainTimeout) {
		t.Errorf("WaitForTasks timeout error should wrap ErrDrainTimeout, got: %v", err)
	}
}

// fakeLiveWorkerChecker is a test double for LiveWorkerChecker (GH-4609).
type fakeLiveWorkerChecker struct {
	ids []string
}

func (f *fakeLiveWorkerChecker) GetRunningTaskIDs() []string {
	return f.ids
}

// pastStart backdates taskID's StartedAt beyond deadOwnerGracePeriod so
// ReconcileDeadOwners tests aren't gated by the just-started grace window.
// White-box (package executor): reaches into Monitor's internal map directly.
func pastStart(t *testing.T, m *Monitor, taskID string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	past := time.Now().Add(-time.Hour)
	if state, ok := m.tasks[taskID]; ok {
		state.StartedAt = &past
	}
}

// GH-4609 acceptance: a dead-owner active-registry entry (no live executor
// process, execution row not progressing) does not block self-upgrade drain
// forever — it is finalized as failed and excluded from the drain-blocking
// set. Reproduces the GH-72 stalled-retry incident: the in-memory registry
// kept counting a task whose worker process was long gone.
func TestMonitorReconcileDeadOwners_ExcludedFromDrain(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-72", "Zombie task", "")
	m.Start("GH-72")
	m.SetLiveWorkerChecker(&fakeLiveWorkerChecker{}) // no live worker holds anything
	pastStart(t, m, "GH-72")

	ids := m.GetRunningTaskIDs()
	if len(ids) != 0 {
		t.Fatalf("Expected dead-owner entry excluded from drain-blocking set, got %v", ids)
	}

	state, ok := m.Get("GH-72")
	if !ok {
		t.Fatal("GH-72 missing after reconciliation")
	}
	if state.Status != StatusFailed {
		t.Errorf("Status = %s, want %s (dead-owner entry must be finalized as failed)", state.Status, StatusFailed)
	}
}

// A task a live worker is currently holding must never be reconciled away,
// regardless of how long ago it started.
func TestMonitorReconcileDeadOwners_LiveWorkerNotFinalized(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-73", "Live task", "")
	m.Start("GH-73")
	m.SetLiveWorkerChecker(&fakeLiveWorkerChecker{ids: []string{"GH-73"}})
	pastStart(t, m, "GH-73")

	ids := m.GetRunningTaskIDs()
	if len(ids) != 1 || ids[0] != "GH-73" {
		t.Fatalf("Expected live-worker task to remain drain-blocking, got %v", ids)
	}
	state, _ := m.Get("GH-73")
	if state.Status != StatusRunning {
		t.Errorf("Status = %s, want %s (live worker must not be reconciled away)", state.Status, StatusRunning)
	}
}

// deadOwnerGracePeriod protects a task Monitor.Start just marked running from
// being reconciled before its worker has had a chance to register with the
// live-worker checker.
func TestMonitorReconcileDeadOwners_GracePeriodProtectsRecentStart(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-74", "Freshly started task", "")
	m.Start("GH-74") // StartedAt = now, well inside deadOwnerGracePeriod
	m.SetLiveWorkerChecker(&fakeLiveWorkerChecker{})

	ids := m.GetRunningTaskIDs()
	if len(ids) != 1 || ids[0] != "GH-74" {
		t.Fatalf("Expected freshly started task protected by grace period, got %v", ids)
	}
}

// A recent execution_event heartbeat protects a no-live-worker candidate from
// finalization — the fallback signal for the narrow race between
// Monitor.Start and the live-worker checker registering the task.
func TestMonitorReconcileDeadOwners_RecentHeartbeatProtectsCandidate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-75", ProjectPath: "/p", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	if err := store.RecordExecutionEvent("e1", memory.StageRunning, "heartbeat"); err != nil {
		t.Fatalf("RecordExecutionEvent: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-75", "Task 75", "")
	m.SetProjectInfo("GH-75", "/p", "proj")
	m.Start("GH-75")
	m.SetLiveWorkerChecker(&fakeLiveWorkerChecker{}) // no live worker
	m.SetExecutionStore(store)
	pastStart(t, m, "GH-75")

	ids := m.GetRunningTaskIDs()
	if len(ids) != 1 || ids[0] != "GH-75" {
		t.Fatalf("Expected recent heartbeat to protect candidate from finalization, got %v", ids)
	}
	state, _ := m.Get("GH-75")
	if state.Status != StatusRunning {
		t.Errorf("Status = %s, want %s (recent heartbeat must protect from dead-owner finalization)", state.Status, StatusRunning)
	}
}

// No heartbeat at all (and no live worker) is exactly the GH-72 zombie
// shape — the candidate must be finalized as failed.
func TestMonitorReconcileDeadOwners_NoHeartbeatFinalizes(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-76", ProjectPath: "/p", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}
	// No execution_events recorded -- "not progressing".

	m := NewMonitor()
	m.Register("GH-76", "Task 76", "")
	m.SetProjectInfo("GH-76", "/p", "proj")
	m.Start("GH-76")
	m.SetLiveWorkerChecker(&fakeLiveWorkerChecker{})
	m.SetExecutionStore(store)
	pastStart(t, m, "GH-76")

	ids := m.GetRunningTaskIDs()
	if len(ids) != 0 {
		t.Fatalf("Expected dead-owner with no heartbeat excluded from drain, got %v", ids)
	}
	state, _ := m.Get("GH-76")
	if state.Status != StatusFailed {
		t.Errorf("Status = %s, want %s", state.Status, StatusFailed)
	}
}

// End-to-end acceptance: WaitForTasks is the method upgrade.TaskChecker calls
// to block self-upgrade drain (GracefulUpgrader.PerformUpgrade /
// HotUpgrader.PerformHotUpgrade). A dead-owner entry must let it resolve
// promptly instead of timing out — the literal GH-72 incident symptom
// ("drain timeout: 1 tasks still active: [GH-72]" looping every 5 minutes).
func TestMonitorWaitForTasks_DeadOwnerDoesNotBlockDrain(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-72", "Zombie task", "")
	m.Start("GH-72")
	m.SetLiveWorkerChecker(&fakeLiveWorkerChecker{})
	pastStart(t, m, "GH-72")

	ctx := context.Background()
	if err := m.WaitForTasks(ctx, 5*time.Second); err != nil {
		t.Fatalf("WaitForTasks should resolve promptly once the dead-owner entry is reconciled, got: %v", err)
	}
}

// TestMonitorReleasePriorAttempt_FinalizesNonTerminalEntry is the GH-4609
// subtask 2 Monitor-level acceptance test: a still-Running (or Queued/
// Pending) entry must be finalized to Stalled so a granted stall-retry never
// leaves the prior attempt's active-registry entry looking like it's still
// in flight.
func TestMonitorReleasePriorAttempt_FinalizesNonTerminalEntry(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-72", "Zombie task", "")
	m.Start("GH-72")

	m.ReleasePriorAttempt("GH-72", "stall-retry: superseded by generation 1")

	state, ok := m.Get("GH-72")
	if !ok {
		t.Fatal("GH-72 missing after ReleasePriorAttempt")
	}
	if state.Status != StatusStalled {
		t.Errorf("Status = %s, want %s", state.Status, StatusStalled)
	}
	if state.Error != "stall-retry: superseded by generation 1" {
		t.Errorf("Error = %q, want the release reason recorded", state.Error)
	}
	if state.CompletedAt == nil {
		t.Error("expected CompletedAt to be set once the entry is finalized")
	}
}

// A terminal entry (the common case: runner.go's own Stall() already ran)
// must be left untouched — ReleasePriorAttempt only forces the transition
// when it hasn't already happened.
func TestMonitorReleasePriorAttempt_TerminalEntryUntouched(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-73", "Already stalled task", "")
	m.Start("GH-73")
	m.Stall("GH-73", "session stalled: no agent event for >10m")

	m.ReleasePriorAttempt("GH-73", "stall-retry: superseded by generation 1")

	state, _ := m.Get("GH-73")
	if state.Error != "session stalled: no agent event for >10m" {
		t.Errorf("expected the original Stall() reason preserved, got %q", state.Error)
	}
}

// A completed entry must never be reclassified as stalled by a later
// stall-retry decision for a different, unrelated generation.
func TestMonitorReleasePriorAttempt_CompletedEntryUntouched(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-74", "Completed task", "")
	m.Start("GH-74")
	m.Complete("GH-74", "https://github.com/org/repo/pull/1")

	m.ReleasePriorAttempt("GH-74", "stall-retry: superseded by generation 1")

	state, _ := m.Get("GH-74")
	if state.Status != StatusCompleted {
		t.Errorf("Status = %s, want %s (completed entry must not be overwritten)", state.Status, StatusCompleted)
	}
}

// An unregistered task ID is a no-op — nothing to release.
func TestMonitorReleasePriorAttempt_UnknownTaskIsNoOp(t *testing.T) {
	m := NewMonitor()
	m.ReleasePriorAttempt("GH-does-not-exist", "reason")

	if _, ok := m.Get("GH-does-not-exist"); ok {
		t.Fatal("expected no entry to be created for an unknown task ID")
	}
}

func TestTaskStatusConstants(t *testing.T) {
	tests := []struct {
		status   TaskStatus
		expected string
	}{
		{StatusPending, "pending"},
		{StatusQueued, "queued"},
		{StatusRunning, "running"},
		{StatusCompleted, "completed"},
		{StatusFailed, "failed"},
		{StatusCancelled, "cancelled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if string(tt.status) != tt.expected {
				t.Errorf("status = %q, want %q", tt.status, tt.expected)
			}
		})
	}
}

func TestTaskStateFields(t *testing.T) {
	state := &TaskState{
		ID:       "TASK-123",
		Title:    "Test Task",
		Status:   StatusRunning,
		Phase:    "Implementing",
		Progress: 50,
		Message:  "Working on it",
		Error:    "",
		PRUrl:    "",
	}

	if state.ID != "TASK-123" {
		t.Errorf("ID = %q, want TASK-123", state.ID)
	}
	if state.Title != "Test Task" {
		t.Errorf("Title = %q, want Test Task", state.Title)
	}
	if state.Status != StatusRunning {
		t.Errorf("Status = %v, want running", state.Status)
	}
	if state.Phase != "Implementing" {
		t.Errorf("Phase = %q, want Implementing", state.Phase)
	}
	if state.Progress != 50 {
		t.Errorf("Progress = %d, want 50", state.Progress)
	}
	if state.Message != "Working on it" {
		t.Errorf("Message = %q, want Working on it", state.Message)
	}
}

// GH-2167: SetProjectInfo attaches project path and name to a registered task.
func TestMonitorSetProjectInfo(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-1", "Test task", "https://example.com/1")
	m.SetProjectInfo("GH-1", "/home/user/pilot", "pilot")

	state, ok := m.Get("GH-1")
	if !ok {
		t.Fatal("task not found")
	}
	if state.ProjectPath != "/home/user/pilot" {
		t.Errorf("ProjectPath = %q, want /home/user/pilot", state.ProjectPath)
	}
	if state.ProjectName != "pilot" {
		t.Errorf("ProjectName = %q, want pilot", state.ProjectName)
	}
}

func TestMonitorSetProjectInfo_NonExistent(t *testing.T) {
	m := NewMonitor()
	// Should not panic on non-existent task
	m.SetProjectInfo("GH-999", "/tmp/foo", "foo")
}

// GH-4246: Hydrate reconstructs a task's state directly (bypassing
// Register→Queue/Start), preserving StartedAt for running tasks so
// duration display survives a restart.
func TestMonitorHydrate(t *testing.T) {
	startedAt := time.Now().Add(-5 * time.Minute)

	m := NewMonitor()
	m.Hydrate("GH-1", "Queued task", "1", StatusQueued, nil)
	m.Hydrate("GH-2", "Running task", "2", StatusRunning, &startedAt)

	queued, ok := m.Get("GH-1")
	if !ok {
		t.Fatal("GH-1 not found after hydrate")
	}
	if queued.Status != StatusQueued {
		t.Errorf("Expected status queued, got %s", queued.Status)
	}
	if queued.Phase != "Queued" {
		t.Errorf("Expected phase 'Queued', got '%s'", queued.Phase)
	}

	running, ok := m.Get("GH-2")
	if !ok {
		t.Fatal("GH-2 not found after hydrate")
	}
	if running.Status != StatusRunning {
		t.Errorf("Expected status running, got %s", running.Status)
	}
	if running.StartedAt == nil || !running.StartedAt.Equal(startedAt) {
		t.Errorf("Expected StartedAt %v, got %v", startedAt, running.StartedAt)
	}
}

// GH-4246: HydrateFromStore seeds the monitor from queued/pending/running DB
// rows in one call — a daemon restart with active work in the DB must
// produce a monitor whose GetAll() reflects it within one refresh tick,
// without waiting for each task's own lifecycle to re-touch the monitor.
func TestMonitorHydrateFromStore(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	execs := []*memory.Execution{
		{ID: "e1", TaskID: "GH-10", ProjectPath: "/p", Status: "queued", TaskTitle: "Queued task", TaskSourceIssueID: "10"},
		{ID: "e2", TaskID: "GH-11", ProjectPath: "/p", Status: "running", TaskTitle: "Running task", TaskSourceIssueID: "11"},
		{ID: "e3", TaskID: "GH-12", ProjectPath: "/p", Status: "completed", TaskTitle: "Done task"},
	}
	for _, e := range execs {
		if err := store.SaveExecution(e); err != nil {
			t.Fatalf("SaveExecution(%s): %v", e.ID, err)
		}
	}
	if err := store.UpdateExecutionStatus("e2", "running"); err != nil {
		t.Fatalf("UpdateExecutionStatus: %v", err)
	}

	m := NewMonitor()
	if err := m.HydrateFromStore(store); err != nil {
		t.Fatalf("HydrateFromStore failed: %v", err)
	}

	all := m.GetAll()
	if len(all) != 2 {
		t.Fatalf("Expected 2 hydrated tasks (queued+running), got %d", len(all))
	}

	queued, ok := m.Get("GH-10")
	if !ok {
		t.Fatal("GH-10 not hydrated")
	}
	if queued.Status != StatusQueued || queued.Title != "Queued task" {
		t.Errorf("Unexpected GH-10 state: %+v", queued)
	}

	running, ok := m.Get("GH-11")
	if !ok {
		t.Fatal("GH-11 not hydrated")
	}
	if running.Status != StatusRunning {
		t.Errorf("Expected GH-11 status running, got %s", running.Status)
	}
	if running.StartedAt == nil {
		t.Error("Expected GH-11 StartedAt to be set")
	}

	if _, ok := m.Get("GH-12"); ok {
		t.Error("Completed task GH-12 should not be hydrated")
	}
}

func TestMonitorHydrateFromStoreNilStore(t *testing.T) {
	m := NewMonitor()
	if err := m.HydrateFromStore(nil); err != nil {
		t.Errorf("HydrateFromStore(nil) should be a no-op, got error: %v", err)
	}
	if m.Count() != 0 {
		t.Errorf("Expected count 0, got %d", m.Count())
	}
}

// GH-4490: ReconcileWithStore is the periodic backstop that catches a card
// left at running/100% when the normal event path never fires (no-commit
// failure, externally closed PR) — it must pull the terminal outcome from
// the executions table (source of truth) rather than trust stale in-memory
// state.
func TestMonitorReconcileWithStore_RunningBecomesCompleted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-20", ProjectPath: "/p", Status: "completed", PRUrl: "https://example.com/pr/1",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-20", "Task 20", "")
	m.SetProjectInfo("GH-20", "/p", "proj")
	m.Start("GH-20")
	m.UpdateProgress("GH-20", "Implementing", 60, "working")

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok := m.Get("GH-20")
	if !ok {
		t.Fatal("GH-20 not found after reconcile")
	}
	if state.Status != StatusCompleted {
		t.Errorf("Status = %s, want %s", state.Status, StatusCompleted)
	}
	if state.Progress != 100 {
		t.Errorf("Progress = %d, want 100", state.Progress)
	}
	if state.CompletedAt == nil {
		t.Error("expected CompletedAt to be set")
	}
}

// A no-commit failure records a terminal, non-failure status (no_op) on the
// executions row without ever calling Monitor.Fail or Monitor.NoOp directly —
// the reconciler must still stop the card from displaying "running", and must
// land on StatusNoOp (not StatusFailed) so the card doesn't misreport a
// no-op as a genuine failure (GH-4490 subtask 2).
func TestMonitorReconcileWithStore_QueuedBecomesNoOp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-21", ProjectPath: "/p", Status: "no_op",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-21", "Task 21", "")
	m.SetProjectInfo("GH-21", "/p", "proj")
	m.Queue("GH-21")

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok := m.Get("GH-21")
	if !ok {
		t.Fatal("GH-21 not found after reconcile")
	}
	if state.Status != StatusNoOp {
		t.Errorf("Status = %s, want %s", state.Status, StatusNoOp)
	}
	if state.Error == "" {
		t.Error("expected a reconciliation error message to be set")
	}
}

// If the store row is itself still non-terminal, the running card must be
// left untouched.
func TestMonitorReconcileWithStore_LeavesRunningTaskAlone(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-22", ProjectPath: "/p", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-22", "Task 22", "")
	m.SetProjectInfo("GH-22", "/p", "proj")
	m.Start("GH-22")
	m.UpdateProgress("GH-22", "Implementing", 42, "working")

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok := m.Get("GH-22")
	if !ok {
		t.Fatal("GH-22 not found after reconcile")
	}
	if state.Status != StatusRunning {
		t.Errorf("Status = %s, want %s", state.Status, StatusRunning)
	}
	if state.Progress != 42 {
		t.Errorf("Progress = %d, want 42 (should be untouched)", state.Progress)
	}
}

// A task with no matching executions row yet (registered but not dispatched)
// must not error and must be left alone.
func TestMonitorReconcileWithStore_NoExecutionRow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	m := NewMonitor()
	m.Register("GH-23", "Task 23", "")

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok := m.Get("GH-23")
	if !ok {
		t.Fatal("GH-23 not found after reconcile")
	}
	if state.Status != StatusPending {
		t.Errorf("Status = %s, want %s", state.Status, StatusPending)
	}
}

// A task already terminal in-memory (e.g. Complete() already fired) must
// not be re-touched by a later reconcile pass, even if it's still present
// in the monitor.
func TestMonitorReconcileWithStore_AlreadyTerminalUntouched(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-24", ProjectPath: "/p", Status: "failed",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-24", "Task 24", "")
	m.SetProjectInfo("GH-24", "/p", "proj")
	m.Start("GH-24")
	m.Complete("GH-24", "https://example.com/pr/2")

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok := m.Get("GH-24")
	if !ok {
		t.Fatal("GH-24 not found after reconcile")
	}
	if state.Status != StatusCompleted {
		t.Errorf("Status = %s, want %s (already-terminal state must not be overwritten by a stale store row)", state.Status, StatusCompleted)
	}
}

// TASK-420/GH-4537: a Monitor entry that reached a terminal state prematurely
// (e.g. via a claim-loss race or duplicate-dispatch bug elsewhere in the
// pipeline) must self-heal back to running once the ledger — the
// authoritative source — still says the execution is running. Before this
// fix, ReconcileWithStore only ever corrected non-terminal -> terminal
// (GH-4490's Case 1); a Monitor card stuck at "done" while the ledger still
// says "running" would never recover, even across the periodic 2s reconcile
// ticker, producing the exact QUEUE false-complete captured in GH-4536
// (2026-07-24 22:17:07Z, "3 live claude/node procs" while the card read
// "done").
func TestMonitorReconcileWithStore_TerminalRevertsToRunning(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-30", ProjectPath: "/p", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-30", "Task 30", "")
	m.SetProjectInfo("GH-30", "/p", "proj")
	m.Start("GH-30")
	// Simulate the premature-terminal race: the in-memory card already
	// believes the task is done, even though the ledger still says running.
	m.Complete("GH-30", "https://example.com/pr/3")

	state, ok := m.Get("GH-30")
	if !ok {
		t.Fatal("GH-30 not found before reconcile")
	}
	if state.Status != StatusCompleted {
		t.Fatalf("precondition failed: Status = %s, want %s", state.Status, StatusCompleted)
	}

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok = m.Get("GH-30")
	if !ok {
		t.Fatal("GH-30 not found after reconcile")
	}
	if state.Status != StatusRunning {
		t.Errorf("Status = %s, want %s (a running ledger row must always win over a stale terminal Monitor entry)", state.Status, StatusRunning)
	}
	if state.CompletedAt != nil {
		t.Error("expected CompletedAt to be cleared when reverting to running")
	}
}

// A Monitor entry that is genuinely queued/pending (never terminal) must be
// left alone by the new running-reversion branch — it should keep whatever
// non-terminal status it already has rather than being forced to
// StatusRunning.
func TestMonitorReconcileWithStore_QueuedStaysQueuedWhenStoreRunning(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-31", ProjectPath: "/p", Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	m := NewMonitor()
	m.Register("GH-31", "Task 31", "")
	m.SetProjectInfo("GH-31", "/p", "proj")
	m.Queue("GH-31")

	if err := m.ReconcileWithStore(store); err != nil {
		t.Fatalf("ReconcileWithStore: %v", err)
	}

	state, ok := m.Get("GH-31")
	if !ok {
		t.Fatal("GH-31 not found after reconcile")
	}
	if state.Status != StatusQueued {
		t.Errorf("Status = %s, want %s (non-terminal statuses other than running must not be forced to running)", state.Status, StatusQueued)
	}
}

func TestMonitorReconcileWithStoreNilStore(t *testing.T) {
	m := NewMonitor()
	m.Register("GH-25", "Task 25", "")
	if err := m.ReconcileWithStore(nil); err != nil {
		t.Errorf("ReconcileWithStore(nil) should be a no-op, got error: %v", err)
	}
}

func TestTerminalMonitorStatus(t *testing.T) {
	tests := []struct {
		dbStatus string
		want     TaskStatus
		terminal bool
	}{
		{"", "", false},
		{"queued", "", false},
		{"pending", "", false},
		{"running", "", false},
		{"completed", StatusCompleted, true},
		{"cancelled", StatusCancelled, true},
		{"stalled", StatusStalled, true},
		{"failed", StatusFailed, true},
		{"no_op", StatusNoOp, true},
		{"declined", StatusFailed, true},
		{"declined-preflight", StatusFailed, true},
		{"rate_limited", StatusFailed, true},
		{"infra", StatusFailed, true},
		{"skipped", StatusFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.dbStatus, func(t *testing.T) {
			got, terminal := terminalMonitorStatus(tt.dbStatus)
			if got != tt.want || terminal != tt.terminal {
				t.Errorf("terminalMonitorStatus(%q) = (%q, %v), want (%q, %v)", tt.dbStatus, got, terminal, tt.want, tt.terminal)
			}
		})
	}
}
