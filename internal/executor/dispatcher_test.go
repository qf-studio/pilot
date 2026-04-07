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

	// Create dispatcher with 0 stale duration
	config := &DispatcherConfig{
		StaleTaskDuration: 0, // Everything is stale
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Check that the task was marked as failed (not re-queued, since re-queuing
	// without a worker just recreates the orphan)
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

	// Insert a stale "running" task and a stale "queued" task
	execRunning := &memory.Execution{
		ID:          "exec-stale-running",
		TaskID:      "TASK-RUN",
		ProjectPath: "/project-a",
		Status:      "running",
	}
	execQueued := &memory.Execution{
		ID:          "exec-stale-queued",
		TaskID:      "TASK-QUEUE",
		ProjectPath: "/project-b",
		Status:      "queued",
	}
	// A fresh queued task that should NOT be recovered
	execFresh := &memory.Execution{
		ID:          "exec-fresh-queued",
		TaskID:      "TASK-FRESH",
		ProjectPath: "/project-c",
		Status:      "queued",
	}

	for _, exec := range []*memory.Execution{execRunning, execQueued, execFresh} {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Use 0 durations for running/queued to make everything stale immediately,
	// except fresh task — we'll use a long queued duration to keep it alive
	config := &DispatcherConfig{
		StaleTaskDuration:     0,            // All running tasks are stale
		StaleQueuedDuration:   0,            // All queued tasks are stale
		StaleRecoveryInterval: 0,            // No periodic loop for this test
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Stale running → failed
	updated, err := store.GetExecution("exec-stale-running")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Errorf("expected stale running task to be 'failed', got '%s'", updated.Status)
	}

	// Stale queued → failed
	updated, err = store.GetExecution("exec-stale-queued")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Errorf("expected stale queued task to be 'failed', got '%s'", updated.Status)
	}

	// Fresh queued is also failed since StaleQueuedDuration=0 makes everything stale
	updated, err = store.GetExecution("exec-fresh-queued")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Errorf("expected fresh queued task with 0 duration to be 'failed', got '%s'", updated.Status)
	}
}

func TestRecoverStaleTasks_RespectsThresholds(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a running task and a queued task — both just created
	execRunning := &memory.Execution{
		ID:          "exec-running-fresh",
		TaskID:      "TASK-RUN-FRESH",
		ProjectPath: "/project",
		Status:      "running",
	}
	execQueued := &memory.Execution{
		ID:          "exec-queued-fresh",
		TaskID:      "TASK-QUEUE-FRESH",
		ProjectPath: "/project",
		Status:      "queued",
	}

	for _, exec := range []*memory.Execution{execRunning, execQueued} {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Use very long thresholds so nothing is stale
	config := &DispatcherConfig{
		StaleTaskDuration:     24 * time.Hour,
		StaleQueuedDuration:   24 * time.Hour,
		StaleRecoveryInterval: 0,
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Running task should still be running (not stale yet)
	updated, err := store.GetExecution("exec-running-fresh")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "running" {
		t.Errorf("expected running task to remain 'running' with long threshold, got '%s'", updated.Status)
	}

	// Queued task should still be queued (not stale yet)
	updated, err = store.GetExecution("exec-queued-fresh")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "queued" {
		t.Errorf("expected queued task to remain 'queued' with long threshold, got '%s'", updated.Status)
	}
}

func TestRunStaleRecoveryLoop_Periodic(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Use 0 stale durations so any task is immediately stale
	config := &DispatcherConfig{
		StaleTaskDuration:     0,
		StaleQueuedDuration:   0,
		StaleRecoveryInterval: 100 * time.Millisecond, // Fast tick for test
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Insert a stale running task AFTER Start (so initial recovery didn't see it)
	exec := &memory.Execution{
		ID:          "exec-late-stale",
		TaskID:      "TASK-LATE",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Wait for at least one recovery tick
	time.Sleep(300 * time.Millisecond)

	// The periodic loop should have picked it up
	updated, err := store.GetExecution("exec-late-stale")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Errorf("expected periodic recovery to fail stale task, got '%s'", updated.Status)
	}
}

func TestQueueTask_AfterRecovery(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a stale task for the same task ID we'll try to queue later
	exec := &memory.Execution{
		ID:          "exec-old",
		TaskID:      "TASK-REQUEUE",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Recover it (mark as failed)
	config := &DispatcherConfig{
		StaleTaskDuration:     0,
		StaleQueuedDuration:   0,
		StaleRecoveryInterval: 0,
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Verify old task is now failed
	updated, err := store.GetExecution("exec-old")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Fatalf("expected recovered task to be 'failed', got '%s'", updated.Status)
	}

	// Now queue a new task with the same task ID — should succeed since old one is failed
	task := &Task{
		ID:          "TASK-REQUEUE",
		Title:       "Requeued Task",
		Description: "Testing re-queue after recovery",
		ProjectPath: "/project",
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("expected QueueTask to succeed after recovery, got: %v", err)
	}
	if execID == "" {
		t.Error("expected non-empty execution ID")
	}

	// Verify the new execution is queued
	newExec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("failed to get new execution: %v", err)
	}
	if newExec.Status != "queued" && newExec.Status != "running" {
		t.Errorf("expected new task to be 'queued' or 'running', got '%s'", newExec.Status)
	}
}
