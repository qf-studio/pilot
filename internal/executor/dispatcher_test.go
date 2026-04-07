package executor

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// setupTestStore creates a temporary store for testing
func setupTestStore(t *testing.T) (*memory.Store, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "pilot-dispatcher-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	store, err := memory.NewStore(tempDir)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("failed to create store: %v", err)
	}

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tempDir)
	}

	return store, cleanup
}

func TestDispatcher_QueueTask(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	// Create test task
	task := &Task{
		ID:          "TEST-001",
		Title:       "Test Task",
		Description: "Test description",
		ProjectPath: "/tmp/test-project",
		Branch:      "test-branch",
		CreatePR:    true,
	}

	// Queue the task
	execID, err := dispatcher.QueueTask(ctx, task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	if execID == "" {
		t.Error("expected execution ID, got empty string")
	}

	// Verify task is in database
	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}

	if exec.Status != "queued" && exec.Status != "running" {
		t.Errorf("expected status queued or running, got %s", exec.Status)
	}

	if exec.TaskID != task.ID {
		t.Errorf("expected task ID %s, got %s", task.ID, exec.TaskID)
	}

	if exec.TaskTitle != task.Title {
		t.Errorf("expected task title %s, got %s", task.Title, exec.TaskTitle)
	}

	if exec.TaskDescription != task.Description {
		t.Errorf("expected task description %s, got %s", task.Description, exec.TaskDescription)
	}

	if exec.TaskBranch != task.Branch {
		t.Errorf("expected task branch %s, got %s", task.Branch, exec.TaskBranch)
	}

	if exec.TaskCreatePR != task.CreatePR {
		t.Errorf("expected task create PR %v, got %v", task.CreatePR, exec.TaskCreatePR)
	}
}

func TestDispatcher_DuplicateTask(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	// Create test task
	task := &Task{
		ID:          "TEST-DUP",
		Title:       "Duplicate Test",
		Description: "Test description",
		ProjectPath: "/tmp/test-project",
	}

	// Queue first time
	_, err := dispatcher.QueueTask(ctx, task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	// Queue second time - should fail
	_, err = dispatcher.QueueTask(ctx, task)
	if err == nil {
		t.Error("expected error for duplicate task, got nil")
	}
}

func TestDispatcher_GetWorkerStatus(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	// Initially no workers
	status := dispatcher.GetWorkerStatus()
	if len(status) != 0 {
		t.Errorf("expected 0 workers initially, got %d", len(status))
	}

	// Queue a task to create a worker
	task := &Task{
		ID:          "TEST-WORKER",
		Title:       "Worker Test",
		Description: "Test description",
		ProjectPath: "/tmp/test-project-1",
	}

	_, err := dispatcher.QueueTask(ctx, task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	// Give worker time to start
	time.Sleep(100 * time.Millisecond)

	// Check worker exists
	status = dispatcher.GetWorkerStatus()
	if len(status) != 1 {
		t.Errorf("expected 1 worker, got %d", len(status))
	}

	if _, ok := status["/tmp/test-project-1"]; !ok {
		t.Error("expected worker for /tmp/test-project-1")
	}
}

func TestDispatcher_MultipleProjects(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	// Queue tasks for different projects
	// Add small delays between queuing to avoid SQLite BUSY errors under race detector
	projects := []string{"/tmp/project-a", "/tmp/project-b", "/tmp/project-c"}
	for i, proj := range projects {
		task := &Task{
			ID:          "TEST-" + proj[len("/tmp/"):],
			Title:       "Test " + proj,
			Description: "Test description",
			ProjectPath: proj,
		}

		_, err := dispatcher.QueueTask(ctx, task)
		if err != nil {
			t.Fatalf("failed to queue task %d: %v", i, err)
		}
		// Small delay to let SQLite WAL settle between rapid queue operations
		time.Sleep(50 * time.Millisecond)
	}

	// Give workers time to start
	time.Sleep(100 * time.Millisecond)

	// Check workers for each project
	status := dispatcher.GetWorkerStatus()
	if len(status) != 3 {
		t.Errorf("expected 3 workers, got %d", len(status))
	}

	for _, proj := range projects {
		if _, ok := status[proj]; !ok {
			t.Errorf("expected worker for %s", proj)
		}
	}
}

func TestStore_GetQueuedTasksForProject(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert test executions
	executions := []*memory.Execution{
		{ID: "exec-1", TaskID: "TASK-1", ProjectPath: "/project-a", Status: "queued"},
		{ID: "exec-2", TaskID: "TASK-2", ProjectPath: "/project-a", Status: "queued"},
		{ID: "exec-3", TaskID: "TASK-3", ProjectPath: "/project-b", Status: "queued"},
		{ID: "exec-4", TaskID: "TASK-4", ProjectPath: "/project-a", Status: "completed"}, // Not queued
		{ID: "exec-5", TaskID: "TASK-5", ProjectPath: "/project-a", Status: "running"},   // Not queued
	}

	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Query project-a queued tasks
	tasks, err := store.GetQueuedTasksForProject("/project-a", 10)
	if err != nil {
		t.Fatalf("failed to get queued tasks: %v", err)
	}

	if len(tasks) != 2 {
		t.Errorf("expected 2 queued tasks for project-a, got %d", len(tasks))
	}

	// Query project-b queued tasks
	tasks, err = store.GetQueuedTasksForProject("/project-b", 10)
	if err != nil {
		t.Fatalf("failed to get queued tasks: %v", err)
	}

	if len(tasks) != 1 {
		t.Errorf("expected 1 queued task for project-b, got %d", len(tasks))
	}

	// Query with limit
	tasks, err = store.GetQueuedTasksForProject("/project-a", 1)
	if err != nil {
		t.Fatalf("failed to get queued tasks: %v", err)
	}

	if len(tasks) != 1 {
		t.Errorf("expected 1 task with limit, got %d", len(tasks))
	}
}

func TestStore_UpdateExecutionStatus(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert test execution
	exec := &memory.Execution{
		ID:          "exec-status",
		TaskID:      "TASK-STATUS",
		ProjectPath: "/project",
		Status:      "queued",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Update to running
	if err := store.UpdateExecutionStatus("exec-status", "running"); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	updated, err := store.GetExecution("exec-status")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "running" {
		t.Errorf("expected status running, got %s", updated.Status)
	}

	// Update to failed with error
	if err := store.UpdateExecutionStatus("exec-status", "failed", "test error"); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	updated, err = store.GetExecution("exec-status")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Errorf("expected status failed, got %s", updated.Status)
	}
	if updated.Error != "test error" {
		t.Errorf("expected error 'test error', got %s", updated.Error)
	}
	if updated.CompletedAt == nil {
		t.Error("expected completed_at to be set for failed status")
	}
}

func TestStore_IsTaskQueued(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert test executions
	executions := []*memory.Execution{
		{ID: "exec-q1", TaskID: "TASK-QUEUED", ProjectPath: "/project", Status: "queued"},
		{ID: "exec-q2", TaskID: "TASK-RUNNING", ProjectPath: "/project", Status: "running"},
		{ID: "exec-q3", TaskID: "TASK-DONE", ProjectPath: "/project", Status: "completed"},
	}

	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Check queued task
	queued, err := store.IsTaskQueued("TASK-QUEUED")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if !queued {
		t.Error("expected TASK-QUEUED to be queued")
	}

	// Check running task
	queued, err = store.IsTaskQueued("TASK-RUNNING")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if !queued {
		t.Error("expected TASK-RUNNING to be queued (in queue = queued or running)")
	}

	// Check completed task
	queued, err = store.IsTaskQueued("TASK-DONE")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if queued {
		t.Error("expected TASK-DONE to NOT be queued")
	}

	// Check non-existent task
	queued, err = store.IsTaskQueued("TASK-NONEXISTENT")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if queued {
		t.Error("expected TASK-NONEXISTENT to NOT be queued")
	}
}

func TestStore_GetStaleRunningExecutions(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// We need to insert executions with specific created_at times
	// Since SaveExecution uses CURRENT_TIMESTAMP, we'll test with a very short duration

	exec := &memory.Execution{
		ID:          "exec-stale",
		TaskID:      "TASK-STALE",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// With 0 duration, even a just-created task is stale
	stale, err := store.GetStaleRunningExecutions(0)
	if err != nil {
		t.Fatalf("failed to get stale: %v", err)
	}
	if len(stale) != 1 {
		t.Errorf("expected 1 stale execution, got %d", len(stale))
	}

	// With very long duration, nothing is stale
	stale, err = store.GetStaleRunningExecutions(24 * time.Hour)
	if err != nil {
		t.Fatalf("failed to get stale: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("expected 0 stale executions with long duration, got %d", len(stale))
	}
}

func TestDispatcher_RecoverStaleTasks(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a "stale" running task (we use 0 duration to make it immediately stale)
	exec := &memory.Execution{
		ID:          "exec-recover",
		TaskID:      "TASK-RECOVER",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Create dispatcher with 0 stale duration (everything is stale immediately)
	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Check that the task was marked failed (not re-queued)
	updated, err := store.GetExecution("exec-recover")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}

	if updated.Status != "failed" {
		t.Errorf("expected recovered task to have status 'failed', got '%s'", updated.Status)
	}
}

func TestProjectWorker_Status(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	// Use logging.WithComponent to get a proper logger
	log := slog.Default()
	worker := NewProjectWorker("/test/project", store, runner, log)

	status := worker.Status()

	if status.ProjectPath != "/test/project" {
		t.Errorf("expected project path /test/project, got %s", status.ProjectPath)
	}

	if status.IsProcessing {
		t.Error("expected worker to not be processing initially")
	}

	if status.CurrentTaskID != "" {
		t.Errorf("expected no current task, got %s", status.CurrentTaskID)
	}
}

func TestDispatcher_ExecutionStatusPath(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	// Queue a task
	task := &Task{
		ID:          "TEST-STATUS-PATH",
		Title:       "Status Path Test",
		Description: "Test description",
		ProjectPath: filepath.Join(os.TempDir(), "test-status-path"),
	}

	execID, err := dispatcher.QueueTask(ctx, task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	// Check execution status
	exec, err := dispatcher.GetExecutionStatus(execID)
	if err != nil {
		t.Fatalf("failed to get execution status: %v", err)
	}

	// Status should be queued or running (worker might have picked it up)
	if exec.Status != "queued" && exec.Status != "running" && exec.Status != "failed" {
		t.Errorf("unexpected execution status: %s", exec.Status)
	}
}

func TestRecoverStaleTasks_QueuedAndRunning(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a stale running task and a stale queued task
	for _, exec := range []*memory.Execution{
		{ID: "stale-run-1", TaskID: "TASK-R1", ProjectPath: "/proj", Status: "running"},
		{ID: "stale-q-1", TaskID: "TASK-Q1", ProjectPath: "/proj", Status: "queued"},
		{ID: "fresh-run", TaskID: "TASK-FRESH", ProjectPath: "/proj", Status: "running"},
	} {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Use 0 threshold so everything is immediately stale
	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	// All three should be marked failed
	for _, id := range []string{"stale-run-1", "stale-q-1", "fresh-run"} {
		exec, err := store.GetExecution(id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", id, err)
		}
		if exec.Status != "failed" {
			t.Errorf("expected %s status=failed, got %s", id, exec.Status)
		}
	}
}

func TestRecoverStaleTasks_RespectsThresholds(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert executions — they'll get CURRENT_TIMESTAMP as created_at
	for _, exec := range []*memory.Execution{
		{ID: "run-1", TaskID: "TASK-R1", ProjectPath: "/proj", Status: "running"},
		{ID: "q-1", TaskID: "TASK-Q1", ProjectPath: "/proj", Status: "queued"},
	} {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Use very long thresholds — nothing should be considered stale
	config := &DispatcherConfig{
		StaleRunningThreshold: 24 * time.Hour,
		StaleQueuedThreshold:  24 * time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	// Both should still be in their original state
	for _, tc := range []struct {
		id     string
		expect string
	}{
		{"run-1", "running"},
		{"q-1", "queued"},
	} {
		exec, err := store.GetExecution(tc.id)
		if err != nil {
			t.Fatalf("GetExecution(%s): %v", tc.id, err)
		}
		if exec.Status != tc.expect {
			t.Errorf("expected %s status=%s, got %s", tc.id, tc.expect, exec.Status)
		}
	}
}

func TestRunStaleRecoveryLoop_Periodic(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: 100 * time.Millisecond, // fast ticks for test
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	// Insert a stale task AFTER start — the periodic loop should catch it
	if err := store.SaveExecution(&memory.Execution{
		ID: "late-stale", TaskID: "TASK-LATE", ProjectPath: "/proj", Status: "running",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Wait enough ticks for the loop to fire
	time.Sleep(350 * time.Millisecond)

	exec, err := store.GetExecution("late-stale")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected late-stale to be failed after periodic recovery, got %s", exec.Status)
	}
}

func TestQueueTask_AfterRecovery(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a stale task for the same task ID we'll queue later
	if err := store.SaveExecution(&memory.Execution{
		ID: "old-exec", TaskID: "TASK-REQUEUE", ProjectPath: "/proj", Status: "running",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Start dispatcher with 0 threshold — marks old task as failed
	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	// Verify old task is failed
	old, err := store.GetExecution("old-exec")
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if old.Status != "failed" {
		t.Fatalf("expected old-exec failed, got %s", old.Status)
	}

	// Now re-queue the same task ID — should succeed since old one is failed
	task := &Task{
		ID:          "TASK-REQUEUE",
		Title:       "Re-queued task",
		Description: "After recovery",
		ProjectPath: "/proj",
	}
	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("QueueTask after recovery: %v", err)
	}
	if execID == "" {
		t.Error("expected execution ID, got empty")
	}
}
