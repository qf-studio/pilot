package executor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
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

	// Check that the task was marked failed (not re-queued — re-queuing without
	// a worker just recreates the orphan).
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

	// Insert a stale running task and a stale queued task for the same
	// project — mirrors the GH-3714/3715/3716 restart incident: a crashed
	// worker leaves a "running" orphan while its FIFO siblings sit "queued".
	executions := []*memory.Execution{
		{ID: "exec-stale-run", TaskID: "TASK-RUN", ProjectPath: "/project", Status: "running"},
		{ID: "exec-stale-q", TaskID: "TASK-Q", ProjectPath: "/project", Status: "queued"},
		{ID: "exec-ok", TaskID: "TASK-OK", ProjectPath: "/project", Status: "completed"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Use 0 thresholds so everything is stale immediately.
	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour, // won't tick in this test
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// The orphaned RUNNING task (crashed worker) is still reaped — unaffected
	// by GH-3732, since recoverStaleRunningTasks runs before queue adoption
	// creates any workers.
	exec, err := store.GetExecution("exec-stale-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected exec-stale-run to be 'failed', got '%s'", exec.Status)
	}

	// GH-3732: the queued sibling must NOT be reaped as an orphan — its
	// project gets re-adopted at Start, so a real worker should pick it up
	// instead of the stale-queued reap wrongly failing it.
	exec, err = store.GetExecution("exec-stale-q")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status == "failed" && exec.Error == "queued task orphaned by restart; project no longer configured" {
		t.Errorf("expected exec-stale-q to be adopted, not reaped as an orphan (error=%q)", exec.Error)
	}

	status := dispatcher.GetWorkerStatus()
	if _, ok := status["/project"]; !ok {
		t.Errorf("expected a re-adopted worker for /project, got workers: %v", status)
	}

	// Completed task should be untouched.
	exec, err = store.GetExecution("exec-ok")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected completed task to remain 'completed', got '%s'", exec.Status)
	}
}

func TestRecoverStaleTasks_RunningSkipsWhenLiveWorker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-live-run", TaskID: "TASK-LR", ProjectPath: "/project-live", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	// Inject a live worker for the project so the reaper should skip it.
	dispatcher.mu.Lock()
	dispatcher.workers["/project-live"] = &ProjectWorker{projectPath: "/project-live"}
	dispatcher.mu.Unlock()

	dispatcher.recoverStaleTasks()

	got, err := store.GetExecution("exec-live-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("expected running task with live worker to remain 'running', got '%s'", got.Status)
	}
}

func TestRecoverStaleTasks_QueuedSkipsWhenLiveWorker(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-live-q", TaskID: "TASK-LQ", ProjectPath: "/project-live", Status: "queued"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	dispatcher.mu.Lock()
	dispatcher.workers["/project-live"] = &ProjectWorker{projectPath: "/project-live"}
	dispatcher.mu.Unlock()

	dispatcher.recoverStaleTasks()

	got, err := store.GetExecution("exec-live-q")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "queued" {
		t.Errorf("expected queued task with live worker to remain 'queued', got '%s'", got.Status)
	}
}

func TestRecoverStaleTasks_RespectsThresholds(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert running and queued tasks that were just created.
	executions := []*memory.Execution{
		{ID: "exec-fresh-run", TaskID: "TASK-FR", ProjectPath: "/project", Status: "running"},
		{ID: "exec-fresh-q", TaskID: "TASK-FQ", ProjectPath: "/project", Status: "queued"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Use very long thresholds so nothing is "stale" by age.
	config := &DispatcherConfig{
		StaleRunningThreshold: 24 * time.Hour,
		StaleQueuedThreshold:  24 * time.Hour,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// The running task's threshold is respected — it isn't old enough to be
	// considered a crash orphan, so it's left untouched.
	exec, err := store.GetExecution("exec-fresh-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status != "running" {
		t.Errorf("expected exec-fresh-run to remain 'running', got '%s'", exec.Status)
	}

	// GH-3732: restart adoption is NOT threshold-gated — every project with a
	// queued row gets a worker at Start regardless of how fresh the row is.
	status := dispatcher.GetWorkerStatus()
	if _, ok := status["/project"]; !ok {
		t.Errorf("expected exec-fresh-q's project to be adopted with a worker regardless of threshold, got workers: %v", status)
	}
}

func TestRunStaleRecoveryLoop_Periodic(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Use a very short interval so the loop ticks quickly.
	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: 50 * time.Millisecond,
	}
	runner := NewRunner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcher := NewDispatcher(store, runner, config)
	if err := dispatcher.Start(ctx); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Insert a stale task AFTER Start() (so the initial pass doesn't see it).
	time.Sleep(20 * time.Millisecond)
	exec := &memory.Execution{
		ID:          "exec-periodic",
		TaskID:      "TASK-PERIODIC",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Wait for the loop to tick and recover it.
	time.Sleep(200 * time.Millisecond)

	updated, err := store.GetExecution("exec-periodic")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updated.Status != "failed" {
		t.Errorf("expected periodic recovery to mark task 'failed', got '%s'", updated.Status)
	}
}

func TestRecoverStaleTasks_DeletesOrphanWhenCompleted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Scenario: same TaskID has a completed row AND an orphan running/queued row.
	executions := []*memory.Execution{
		{ID: "exec-completed", TaskID: "TASK-ORPHAN", ProjectPath: "/project", Status: "completed", CommitSHA: "abc"},
		{ID: "exec-orphan-run", TaskID: "TASK-ORPHAN", ProjectPath: "/project", Status: "running"},
		{ID: "exec-orphan-q", TaskID: "TASK-ORPHAN", ProjectPath: "/project", Status: "queued"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Orphan rows should be deleted, not marked failed.
	for _, id := range []string{"exec-orphan-run", "exec-orphan-q"} {
		exec, err := store.GetExecution(id)
		if err == nil && exec != nil {
			t.Errorf("expected orphan %s to be deleted, but it still exists with status '%s'", id, exec.Status)
		}
	}

	// Completed row should remain untouched.
	exec, err := store.GetExecution("exec-completed")
	if err != nil {
		t.Fatalf("failed to get completed execution: %v", err)
	}
	if exec.Status != "completed" {
		t.Errorf("expected completed execution to remain 'completed', got '%s'", exec.Status)
	}
}

func TestRecoverStaleTasks_MarksFailedWhenNoCompleted(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Scenario: orphan rows with no completed execution for the same TaskID.
	executions := []*memory.Execution{
		{ID: "exec-only-run", TaskID: "TASK-NOCOMPLETE", ProjectPath: "/project", Status: "running"},
		{ID: "exec-only-q", TaskID: "TASK-NOCOMPLETE-Q", ProjectPath: "/project", Status: "queued"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// The running orphan has no worker (recoverStaleRunningTasks runs before
	// adoption) and no completed sibling, so it's genuinely reaped.
	exec, err := store.GetExecution("exec-only-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected exec-only-run to be 'failed', got '%s'", exec.Status)
	}

	// GH-3732: the queued task's project gets re-adopted at Start, so it must
	// NOT be reaped via the stale-queued orphan path — it may still end up
	// "failed" if the real worker attempts (and fails) execution against a
	// nonexistent project path, but that's a distinct, legitimate outcome
	// from the orphan-reap message.
	exec, err = store.GetExecution("exec-only-q")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status == "failed" && exec.Error == "queued task orphaned by restart; project no longer configured" {
		t.Errorf("expected exec-only-q to be adopted, not reaped as an orphan (error=%q)", exec.Error)
	}
}

func TestRecoverStaleTasks_DifferentProjectPath(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Scenario: completed execution exists for a DIFFERENT project path.
	// The orphan should still be marked failed (HasCompletedExecution checks both fields).
	executions := []*memory.Execution{
		{ID: "exec-diff-completed", TaskID: "TASK-DIFF", ProjectPath: "/project-a", Status: "completed"},
		{ID: "exec-diff-orphan", TaskID: "TASK-DIFF", ProjectPath: "/project-b", Status: "running"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Different project path → no match → should be marked failed, not deleted.
	exec, err := store.GetExecution("exec-diff-orphan")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if exec.Status != "failed" {
		t.Errorf("expected orphan with different project to be 'failed', got '%s'", exec.Status)
	}
}

func TestStore_HasCompletedExecution(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	executions := []*memory.Execution{
		{ID: "exec-hce-1", TaskID: "TASK-HCE", ProjectPath: "/project-a", Status: "completed", CommitSHA: "abc"},
		{ID: "exec-hce-2", TaskID: "TASK-HCE", ProjectPath: "/project-b", Status: "running"},
		{ID: "exec-hce-3", TaskID: "TASK-HCE-NONE", ProjectPath: "/project-a", Status: "failed"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	tests := []struct {
		name        string
		taskID      string
		projectPath string
		want        bool
	}{
		{"completed exists", "TASK-HCE", "/project-a", true},
		{"different project", "TASK-HCE", "/project-b", false},
		{"only failed", "TASK-HCE-NONE", "/project-a", false},
		{"nonexistent task", "TASK-NOPE", "/project-a", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.HasCompletedExecution(tc.taskID, tc.projectPath)
			if err != nil {
				t.Fatalf("HasCompletedExecution error: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasCompletedExecution(%q, %q) = %v, want %v", tc.taskID, tc.projectPath, got, tc.want)
			}
		})
	}
}

func TestStore_DeleteExecution(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:          "exec-del",
		TaskID:      "TASK-DEL",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	if err := store.DeleteExecution("exec-del"); err != nil {
		t.Fatalf("DeleteExecution error: %v", err)
	}

	got, err := store.GetExecution("exec-del")
	if err == nil && got != nil {
		t.Errorf("expected execution to be deleted, but found status '%s'", got.Status)
	}
}

func TestQueueTask_AfterRecovery(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Insert a stale task for the same task ID we'll try to queue.
	exec := &memory.Execution{
		ID:          "exec-old",
		TaskID:      "TASK-REQUEUE",
		ProjectPath: "/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Start dispatcher with 0 threshold so it recovers immediately.
	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// The old task should now be failed, so re-queuing the same task ID should succeed.
	task := &Task{
		ID:          "TASK-REQUEUE",
		Title:       "Re-queued after recovery",
		Description: "Should succeed since old execution is failed",
		ProjectPath: "/project",
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("expected re-queue to succeed after recovery, got error: %v", err)
	}
	if execID == "" {
		t.Error("expected non-empty execution ID")
	}
}

// GH-3513 wave 2: every TASK-358 classified worker outcome must be terminal for
// WaitForExecution. Treating them as in-flight left the handler hanging until a
// later self-heal mutated the row — in the GH-3530 incident a child PR merge
// promoted the PARENT's row to completed with the child's PR URL, and the woken
// handler reported a false "✅ Pilot completed!".
func TestWaitForExecution_ClassifiedOutcomesAreTerminal(t *testing.T) {
	statuses := []string{"completed", "failed", "cancelled", "declined", "no_op", "rate_limited", "skipped", "stalled", "infra"}

	store, cleanup := setupTestStore(t)
	defer cleanup()
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			execID := "exec-" + status
			if err := store.SaveExecution(&memory.Execution{
				ID:          execID,
				TaskID:      "GH-1",
				ProjectPath: "/tmp/p",
				Status:      status,
			}); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			exec, err := dispatcher.WaitForExecution(ctx, execID, 10*time.Millisecond)
			if err != nil {
				t.Fatalf("WaitForExecution(%s) returned error (hang→timeout?): %v", status, err)
			}
			if exec.Status != status {
				t.Errorf("WaitForExecution(%s) returned status %q", status, exec.Status)
			}
		})
	}
}

// GH-3732: restart adoption. A fresh Dispatcher (empty in-memory workers map)
// must recreate a worker for every project that still has queued rows in
// SQLite, so a daemon restart resumes FIFO processing instead of stranding
// tasks that look idle from the outside.
func TestDispatcher_AdoptQueuedProjectsOnRestart(t *testing.T) {
	tests := []struct {
		name     string
		projects []string
	}{
		{name: "single project", projects: []string{"/project-adopt-a"}},
		{name: "multiple projects", projects: []string{"/project-adopt-b", "/project-adopt-c", "/project-adopt-d"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			// Simulate tasks left queued from before a restart: rows exist in
			// SQLite, but this is a fresh Dispatcher with an empty workers map.
			for i, proj := range tc.projects {
				exec := &memory.Execution{
					ID:          fmt.Sprintf("exec-adopt-%s-%d", tc.name, i),
					TaskID:      fmt.Sprintf("TASK-ADOPT-%s-%d", tc.name, i),
					ProjectPath: proj,
					Status:      "queued",
				}
				if err := store.SaveExecution(exec); err != nil {
					t.Fatalf("failed to save execution: %v", err)
				}
			}

			runner := NewRunner()
			dispatcher := NewDispatcher(store, runner, nil)

			if len(dispatcher.GetWorkerStatus()) != 0 {
				t.Fatalf("expected empty workers map before Start")
			}

			if err := dispatcher.Start(context.Background()); err != nil {
				t.Fatalf("failed to start dispatcher: %v", err)
			}
			defer dispatcher.Stop()

			// Give the adoption + worker goroutines time to spin up.
			time.Sleep(150 * time.Millisecond)

			status := dispatcher.GetWorkerStatus()
			for _, proj := range tc.projects {
				if _, ok := status[proj]; !ok {
					t.Errorf("expected re-adopted worker for %s, got workers: %v", proj, status)
				}
			}
		})
	}
}

// TestStore_GetQueuedProjectPaths verifies the distinct-project query backing
// restart adoption: only queued/pending rows count, duplicates collapse, and
// completed/running rows are excluded. GH-3732.
func TestStore_GetQueuedProjectPaths(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	executions := []*memory.Execution{
		{ID: "exec-gp-1", TaskID: "TASK-GP-1", ProjectPath: "/project-gp-a", Status: "queued"},
		{ID: "exec-gp-2", TaskID: "TASK-GP-2", ProjectPath: "/project-gp-a", Status: "queued"}, // duplicate project
		{ID: "exec-gp-3", TaskID: "TASK-GP-3", ProjectPath: "/project-gp-b", Status: "pending"},
		{ID: "exec-gp-4", TaskID: "TASK-GP-4", ProjectPath: "/project-gp-c", Status: "completed"}, // not queued
		{ID: "exec-gp-5", TaskID: "TASK-GP-5", ProjectPath: "/project-gp-d", Status: "running"},   // not queued
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	paths, err := store.GetQueuedProjectPaths()
	if err != nil {
		t.Fatalf("GetQueuedProjectPaths error: %v", err)
	}

	got := make(map[string]bool, len(paths))
	for _, p := range paths {
		got[p] = true
	}

	for _, want := range []string{"/project-gp-a", "/project-gp-b"} {
		if !got[want] {
			t.Errorf("expected %s in queued project paths, got %v", want, paths)
		}
	}
	for _, notWant := range []string{"/project-gp-c", "/project-gp-d"} {
		if got[notWant] {
			t.Errorf("did not expect %s in queued project paths, got %v", notWant, paths)
		}
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 distinct queued project paths (dedup), got %d: %v", len(paths), paths)
	}
}

// GH-3732: queueing a task behind a busy worker must log the blocking task ID
// and the new task's FIFO position, instead of leaving it invisible until its
// turn comes up (the GH-3725 incident: queued 70+ minutes with no signal why).
func TestDispatcher_QueueSingleTask_BlockLogging(t *testing.T) {
	tests := []struct {
		name          string
		busy          bool
		blockedTaskID string
	}{
		{name: "idle_project_no_block", busy: false},
		{name: "busy_project_logs_blocker_and_position", busy: true, blockedTaskID: "GH-1000"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()
			runner := NewRunner()
			dispatcher := NewDispatcher(store, runner, nil)

			projectPath := "/project-block-" + tc.name
			if tc.busy {
				// Manually register a "busy" worker without starting its Run()
				// goroutine, so the busy-check is deterministic instead of
				// racing a real worker.
				worker := NewProjectWorker(projectPath, store, runner, dispatcher.log)
				worker.processing.Store(true)
				worker.currentTaskID.Store(tc.blockedTaskID)
				dispatcher.mu.Lock()
				dispatcher.workers[projectPath] = worker
				dispatcher.mu.Unlock()
			}

			var buf bytes.Buffer
			dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

			task := &Task{ID: "GH-NEW", ProjectPath: projectPath}
			if _, err := dispatcher.queueSingleTask(context.Background(), task); err != nil {
				t.Fatalf("queueSingleTask error: %v", err)
			}

			logOutput := buf.String()
			if !tc.busy {
				if strings.Contains(logOutput, "blocked_by") {
					t.Errorf("expected no blocked_by annotation for idle project, got: %s", logOutput)
				}
				return
			}

			if !strings.Contains(logOutput, "blocked_by="+tc.blockedTaskID) {
				t.Errorf("expected blocked_by=%s in log, got: %s", tc.blockedTaskID, logOutput)
			}
			if !strings.Contains(logOutput, "position=1") {
				t.Errorf("expected position=1 in log, got: %s", logOutput)
			}
			if !strings.Contains(logOutput, tc.blockedTaskID) {
				t.Errorf("expected log message to name the blocking task %s, got: %s", tc.blockedTaskID, logOutput)
			}
		})
	}
}

// TestRecoverStaleQueuedTasks_MessageAccuracy verifies the reworded orphan
// message only fires for genuine orphans (no live worker), and that a
// project with a live worker is left untouched. GH-3732.
func TestRecoverStaleQueuedTasks_MessageAccuracy(t *testing.T) {
	tests := []struct {
		name          string
		injectWorker  bool
		wantStatus    string
		wantErrSubstr string
	}{
		{
			name:          "genuine orphan gets reworded message",
			injectWorker:  false,
			wantStatus:    "failed",
			wantErrSubstr: "queued task orphaned by restart; project no longer configured",
		},
		{
			name:         "live worker protects queued row from reap",
			injectWorker: true,
			wantStatus:   "queued",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			exec := &memory.Execution{ID: "exec-msg", TaskID: "TASK-MSG", ProjectPath: "/project-msg", Status: "queued"}
			if err := store.SaveExecution(exec); err != nil {
				t.Fatalf("failed to save execution: %v", err)
			}

			config := &DispatcherConfig{StaleQueuedThreshold: 0}
			dispatcher := NewDispatcher(store, NewRunner(), config)

			if tc.injectWorker {
				dispatcher.mu.Lock()
				dispatcher.workers["/project-msg"] = &ProjectWorker{projectPath: "/project-msg"}
				dispatcher.mu.Unlock()
			}

			// Call the queued-reap directly (no Start(), no adoption) to
			// exercise the message logic in isolation.
			dispatcher.recoverStaleQueuedTasks()

			got, err := store.GetExecution("exec-msg")
			if err != nil {
				t.Fatalf("failed to get execution: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("expected status %q, got %q", tc.wantStatus, got.Status)
			}
			if tc.wantErrSubstr != "" && got.Error != tc.wantErrSubstr {
				t.Errorf("expected error %q, got %q", tc.wantErrSubstr, got.Error)
			}
		})
	}
}

// TestBuildTaskFromExecution_ThreadsExecutionUUID verifies GH-3764: the Task
// handed to the runner carries the execution row's UUID (exec.ID) separately
// from the human-readable task ID (exec.TaskID), so downstream log/diagnostic
// writes can join against executions.id while WS live-tail filters (which key
// on task.ID) keep working unchanged.
func TestBuildTaskFromExecution_ThreadsExecutionUUID(t *testing.T) {
	exec := &memory.Execution{
		ID:                "11111111-1111-1111-1111-111111111111",
		TaskID:            "GH-3764",
		ProjectPath:       "/tmp/project",
		TaskTitle:         "Test title",
		TaskDescription:   "Test description",
		TaskBranch:        "pilot/GH-3764",
		TaskBaseBranch:    "main",
		TaskCreatePR:      true,
		TaskVerbose:       true,
		TaskSourceAdapter: "github",
		TaskSourceIssueID: "3764",
		TaskLabels:        []string{"pilot"},
	}

	task := buildTaskFromExecution(exec)

	if task.ExecutionID != exec.ID {
		t.Errorf("expected ExecutionID %q, got %q", exec.ID, task.ExecutionID)
	}
	if task.ID != exec.TaskID {
		t.Errorf("expected ID (task label) %q, got %q", exec.TaskID, task.ID)
	}
	if task.ExecutionID == task.ID {
		t.Errorf("ExecutionID and ID must stay distinct fields, both were %q", task.ID)
	}
	if task.Title != exec.TaskTitle || task.Description != exec.TaskDescription {
		t.Errorf("task title/description not carried over from execution")
	}
	if task.ProjectPath != exec.ProjectPath || task.Branch != exec.TaskBranch || task.BaseBranch != exec.TaskBaseBranch {
		t.Errorf("task project/branch fields not carried over from execution")
	}
	if task.CreatePR != exec.TaskCreatePR || task.Verbose != exec.TaskVerbose {
		t.Errorf("task CreatePR/Verbose flags not carried over from execution")
	}
	if task.SourceAdapter != exec.TaskSourceAdapter || task.SourceIssueID != exec.TaskSourceIssueID {
		t.Errorf("task source adapter/issue ID not carried over from execution")
	}
	if len(task.Labels) != 1 || task.Labels[0] != "pilot" {
		t.Errorf("expected labels [pilot], got %v", task.Labels)
	}
}

// TestDispatcher_BootWithQueuedRows_FIFODrainNoStaleReap simulates the
// GH-3788 incident: a daemon restart finds N queued rows, already older
// than StaleQueuedThreshold (as any row left over from real downtime would
// be), spread across multiple projects. Start()'s adoption pass must give
// every one of those projects a worker before recoverStaleQueuedTasks runs,
// so none of them are reaped as "queued task orphaned by restart" — they
// should instead drain FIFO through the real worker.
func TestDispatcher_BootWithQueuedRows_FIFODrainNoStaleReap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Two rows on one project (to exercise FIFO ordering) plus one row each
	// on two more projects — mirrors the four queued tasks (GH-3759/3764/
	// 3765/3726) reaped in the incident.
	executions := []*memory.Execution{
		{ID: "exec-boot-1", TaskID: "GH-BOOT-1", ProjectPath: "/project-boot-a", Status: "queued"},
		{ID: "exec-boot-2", TaskID: "GH-BOOT-2", ProjectPath: "/project-boot-a", Status: "queued"},
		{ID: "exec-boot-3", TaskID: "GH-BOOT-3", ProjectPath: "/project-boot-b", Status: "queued"},
		{ID: "exec-boot-4", TaskID: "GH-BOOT-4", ProjectPath: "/project-boot-c", Status: "queued"},
	}
	for _, exec := range executions {
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save execution: %v", err)
		}
	}

	// Zero threshold: every row above is immediately "stale" by age, exactly
	// like rows that sat queued through real daemon downtime.
	config := &DispatcherConfig{
		StaleQueuedThreshold:  0,
		StaleRunningThreshold: 0,
		StaleRecoveryInterval: time.Hour, // won't tick during this test
	}
	dispatcher := NewDispatcher(store, NewRunner(), config)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// Every project with a queued row must have been adopted at Start.
	status := dispatcher.GetWorkerStatus()
	for _, proj := range []string{"/project-boot-a", "/project-boot-b", "/project-boot-c"} {
		if _, ok := status[proj]; !ok {
			t.Errorf("expected %s to be adopted with a worker, got workers: %v", proj, status)
		}
	}

	// Give the adopted workers time to drain the queue (their preflight
	// checks fail fast since these project paths don't exist on disk).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, exec := range executions {
			got, err := store.GetExecution(exec.ID)
			if err != nil {
				t.Fatalf("failed to get execution: %v", err)
			}
			if got.Status == "queued" {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, exec := range executions {
		got, err := store.GetExecution(exec.ID)
		if err != nil {
			t.Fatalf("failed to get execution: %v", err)
		}
		if got.Status == "queued" {
			t.Errorf("expected %s to be drained by its adopted worker, still queued", exec.ID)
		}
		if got.Error == "queued task orphaned by restart; project no longer configured" {
			t.Errorf("expected %s to be adopted and drained, but stale-queued reap fired instead (error=%q)", exec.ID, got.Error)
		}
	}
}

// TestDispatcher_AdoptQueuedProjects_ReportsFailureWithoutAdopting covers the
// other way GH-3788's mass-reap can happen: adoptQueuedProjects can't tell
// "no queued projects" apart from "failed to ask the store" unless it
// reports its own success/failure to the caller. Start() relies on that
// signal to skip the boot-time stale-queued reap when adoption couldn't run
// — otherwise every queued row would look orphaned since no project got
// adopted, reproducing the exact "no worker picked up" mass-reap this issue
// tracks. This test pins the signal itself: on a store error, adoption must
// report false and must not claim any workers.
func TestDispatcher_AdoptQueuedProjects_ReportsFailureWithoutAdopting(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-adopt-fail", TaskID: "GH-ADOPTFAIL", ProjectPath: "/project-adopt-fail", Status: "queued"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	// Close the underlying DB so GetQueuedProjectPaths fails, simulating a
	// store that isn't ready yet at boot.
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	config := DefaultDispatcherConfig()
	dispatcher := NewDispatcher(store, NewRunner(), config)

	if ok := dispatcher.adoptQueuedProjects(); ok {
		t.Error("expected adoptQueuedProjects to report failure when the store query errors")
	}
	if status := dispatcher.GetWorkerStatus(); len(status) != 0 {
		t.Errorf("expected no workers adopted when the store query fails, got: %v", status)
	}
}
