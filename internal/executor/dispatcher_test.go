package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	if !errors.Is(err, ErrTaskAlreadyActive) {
		t.Errorf("expected err to wrap ErrTaskAlreadyActive, got: %v", err)
	}
}

// TestDispatcher_DuplicateTask_CrossProjectCollision is the GH-4276
// regression: task_id is not unique across projects (every freshly onboarded
// repo starts issue numbering at #1), so the same task_id already queued in
// one project must not block dispatch of the identical task_id in a
// different project.
func TestDispatcher_DuplicateTask_CrossProjectCollision(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	taskA := &Task{
		ID:          "GH-10",
		Title:       "Project A task",
		Description: "Test description",
		ProjectPath: "/tmp/project-a",
	}
	if _, err := dispatcher.QueueTask(ctx, taskA); err != nil {
		t.Fatalf("failed to queue task in project A: %v", err)
	}

	taskB := &Task{
		ID:          "GH-10",
		Title:       "Project B task",
		Description: "Test description",
		ProjectPath: "/tmp/project-b",
	}
	if _, err := dispatcher.QueueTask(ctx, taskB); err != nil {
		t.Fatalf("expected same task_id in a different project to dispatch cleanly, got error: %v", err)
	}
}

// TestDispatcher_IsActive verifies IsActive uses the same source of truth as
// QueueTask's duplicate check (GH-4008), so pollers can pre-check before
// announcing a dispatch attempt.
func TestDispatcher_IsActive(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	ctx := context.Background()

	if dispatcher.IsActive("TEST-ACTIVE", "/tmp/test-project") {
		t.Error("expected IsActive=false before task is queued")
	}

	task := &Task{
		ID:          "TEST-ACTIVE",
		Title:       "Active Test",
		Description: "Test description",
		ProjectPath: "/tmp/test-project",
	}
	if _, err := dispatcher.QueueTask(ctx, task); err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	if !dispatcher.IsActive("TEST-ACTIVE", "/tmp/test-project") {
		t.Error("expected IsActive=true once task is queued")
	}

	// GH-4276: the same task_id active in a DIFFERENT project must not
	// report as active here.
	if dispatcher.IsActive("TEST-ACTIVE", "/tmp/other-project") {
		t.Error("expected IsActive=false for a different project with the same task_id (cross-project collision)")
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

// TestDispatcher_GetRunningTaskIDs verifies the GH-4412 always-on liveness
// signal: it must report exactly the task IDs of workers currently marked
// processing, and must not report idle workers or workers with no current
// task. This is what the autopilot orphan-running sweep unions with the
// (dashboard-only) Monitor's set so a live worker is never mistaken for an
// orphan when the daemon runs headless (no --dashboard, no Monitor wired).
func TestDispatcher_GetRunningTaskIDs(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// No workers yet.
	if ids := dispatcher.GetRunningTaskIDs(); len(ids) != 0 {
		t.Errorf("expected 0 running task IDs with no workers, got %v", ids)
	}

	log := slog.Default()
	idleWorker := NewProjectWorker("/proj-idle", store, runner, log)
	dispatcher.mu.Lock()
	dispatcher.workers["/proj-idle"] = idleWorker
	dispatcher.mu.Unlock()

	// Idle worker (processing=false) must not be reported live.
	if ids := dispatcher.GetRunningTaskIDs(); len(ids) != 0 {
		t.Errorf("expected 0 running task IDs with only an idle worker, got %v", ids)
	}

	liveWorker := NewProjectWorker("/proj-live", store, runner, log)
	liveWorker.processing.Store(true)
	liveWorker.currentTaskID.Store("GH-4412")
	dispatcher.mu.Lock()
	dispatcher.workers["/proj-live"] = liveWorker
	dispatcher.mu.Unlock()

	ids := dispatcher.GetRunningTaskIDs()
	if len(ids) != 1 || ids[0] != "GH-4412" {
		t.Errorf("expected [GH-4412], got %v", ids)
	}

	// Once the worker goes idle again, it must drop out of the live set.
	liveWorker.processing.Store(false)
	if ids := dispatcher.GetRunningTaskIDs(); len(ids) != 0 {
		t.Errorf("expected 0 running task IDs after worker goes idle, got %v", ids)
	}
}

// TestDispatcher_QueuedOrRunningCount verifies the GH-4454 lane-starvation
// signal: 0 for a project with no worker at all, the raw queued count for an
// idle worker sitting on a backlog, +1 while a worker is actively processing,
// and the sum of both when a worker is processing with more still queued.
func TestDispatcher_QueuedOrRunningCount(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	// No worker for this project path at all.
	if got := dispatcher.QueuedOrRunningCount("/proj-none"); got != 0 {
		t.Errorf("expected 0 for a project with no worker, got %d", got)
	}

	log := slog.Default()

	// Idle worker, no queued tasks in the store: 0.
	idleWorker := NewProjectWorker("/proj-idle", store, runner, log)
	dispatcher.mu.Lock()
	dispatcher.workers["/proj-idle"] = idleWorker
	dispatcher.mu.Unlock()

	if got := dispatcher.QueuedOrRunningCount("/proj-idle"); got != 0 {
		t.Errorf("expected 0 for an idle worker with no queued tasks, got %d", got)
	}

	// Worker actively processing, still no queued tasks: 1.
	liveWorker := NewProjectWorker("/proj-live", store, runner, log)
	liveWorker.processing.Store(true)
	liveWorker.currentTaskID.Store("GH-4454")
	dispatcher.mu.Lock()
	dispatcher.workers["/proj-live"] = liveWorker
	dispatcher.mu.Unlock()

	if got := dispatcher.QueuedOrRunningCount("/proj-live"); got != 1 {
		t.Errorf("expected 1 for a processing worker with no queued tasks, got %d", got)
	}

	// Worker with real queued rows backing it in the store, not processing:
	// count matches the queue depth. Rows are written directly via
	// SaveExecution (status "queued") rather than QueueTask, so no worker
	// goroutine races this assertion by actually picking the task up.
	for i := 0; i < 2; i++ {
		exec := &memory.Execution{
			ID:          fmt.Sprintf("TEST-QUEUED-%d", i),
			TaskID:      fmt.Sprintf("TEST-QUEUED-%d", i),
			ProjectPath: "/proj-queued",
			Status:      "queued",
			CreatedAt:   time.Now(),
		}
		if err := store.SaveExecution(exec); err != nil {
			t.Fatalf("failed to save queued execution %d: %v", i, err)
		}
	}
	queuedWorker := NewProjectWorker("/proj-queued", store, runner, log)
	dispatcher.mu.Lock()
	dispatcher.workers["/proj-queued"] = queuedWorker
	dispatcher.mu.Unlock()

	got := dispatcher.QueuedOrRunningCount("/proj-queued")
	if got != 2 {
		t.Errorf("expected 2 for a worker with 2 queued tasks and not processing, got %d", got)
	}

	// Same worker now also marked processing: queued count + 1.
	queuedWorker.processing.Store(true)
	if got := dispatcher.QueuedOrRunningCount("/proj-queued"); got != 3 {
		t.Errorf("expected 3 for a worker with 2 queued tasks and processing, got %d", got)
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
		// GH-4240: a canary sandbox row must still be dequeued for execution
		// (metrics/dashboard exclusion, never ledger/dispatch exclusion), and
		// the flag must round-trip through the queued-task read path.
		{ID: "exec-6", TaskID: "TASK-6", ProjectPath: "/project-a", Status: "queued", IsCanary: true},
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

	if len(tasks) != 3 {
		t.Errorf("expected 3 queued tasks for project-a, got %d", len(tasks))
	}
	var sawCanary bool
	for _, task := range tasks {
		if task.ID == "exec-6" {
			sawCanary = true
			if !task.IsCanary {
				t.Error("exec-6 IsCanary = false, want true")
			}
		}
	}
	if !sawCanary {
		t.Error("expected canary row exec-6 to be included in queued tasks")
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
	queued, err := store.IsTaskQueued("TASK-QUEUED", "/project")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if !queued {
		t.Error("expected TASK-QUEUED to be queued")
	}

	// Check running task
	queued, err = store.IsTaskQueued("TASK-RUNNING", "/project")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if !queued {
		t.Error("expected TASK-RUNNING to be queued (in queue = queued or running)")
	}

	// Check completed task
	queued, err = store.IsTaskQueued("TASK-DONE", "/project")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if queued {
		t.Error("expected TASK-DONE to NOT be queued")
	}

	// Check non-existent task
	queued, err = store.IsTaskQueued("TASK-NONEXISTENT", "/project")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if queued {
		t.Error("expected TASK-NONEXISTENT to NOT be queued")
	}

	// GH-4276: same task_id queued in a DIFFERENT project must not report
	// as queued here — task_id is not unique across projects (fresh repos
	// all start numbering at #1).
	queued, err = store.IsTaskQueued("TASK-QUEUED", "/other-project")
	if err != nil {
		t.Fatalf("failed to check: %v", err)
	}
	if queued {
		t.Error("expected TASK-QUEUED in /other-project to NOT be queued (cross-project collision)")
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

// TestRecoverStaleRunningTasks_HealsToCompletedWhenBranchMerged is the GH-4092
// regression guard: a stale "running" row whose own branch already has a
// merged PR (autopilot shipped the work; only the row's own status update
// raced the reap) must heal to "completed" with the PR URL recorded — not
// "failed". Live incident: GH-4084 was marked failed 3 seconds after its PR
// #4089 merged.
func TestRecoverStaleRunningTasks_HealsToCompletedWhenBranchMerged(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-merged-run", TaskID: "GH-4092", ProjectPath: "/project-merged", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	const mergedPRURL = "https://github.com/qf-studio/pilot/pull/4089"
	origCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, projectPath, branch string) (string, error) {
		if projectPath == "/project-merged" && branch == "pilot/GH-4092" {
			return mergedPRURL, nil
		}
		return "", nil
	}
	defer func() { staleRunningMergedPRCheck = origCheck }()

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	dispatcher.recoverStaleRunningTasks()

	got, err := store.GetExecution("exec-merged-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("expected stale running task with merged branch PR to heal to 'completed', got %q (error=%q)", got.Status, got.Error)
	}
	if got.PRUrl != mergedPRURL {
		t.Errorf("expected pr_url = %q, got %q", mergedPRURL, got.PRUrl)
	}
}

// TestRecoverStaleRunningTasks_HealsUsingRecordedBranchNotTaskID is the
// GH-4409 regression guard for finding #2 in the #4403 review: a decomposed
// subtask's real work lands on its PARENT's branch (decompose.go stamps
// subtask.Branch = parent.Branch before the subtask ever runs), not a branch
// reconstructed from the subtask's own task ID. A stale "running" row for a
// subtask (e.g. GH-4393-5) whose recorded TaskBranch is the parent's branch
// (pilot/GH-4393) must probe THAT branch for a merged PR — probing the
// reconstructed "pilot/GH-4393-5" finds nothing, since nothing ever pushes
// there, so the heal is missed and the child re-runs already-shipped work.
func TestRecoverStaleRunningTasks_HealsUsingRecordedBranchNotTaskID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID: "exec-subtask-run", TaskID: "GH-4393-5", ProjectPath: "/project-epic",
		Status: "running", TaskBranch: "pilot/GH-4393",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	const mergedPRURL = "https://github.com/qf-studio/pilot/pull/4393"
	origCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, projectPath, branch string) (string, error) {
		if branch == "pilot/GH-4393-5" {
			t.Errorf("staleRunningMergedPRCheck probed the reconstructed subtask branch %q instead of the recorded parent branch", branch)
		}
		if projectPath == "/project-epic" && branch == "pilot/GH-4393" {
			return mergedPRURL, nil
		}
		return "", nil
	}
	defer func() { staleRunningMergedPRCheck = origCheck }()

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	dispatcher := NewDispatcher(store, NewRunner(), config)
	dispatcher.recoverStaleRunningTasks()

	got, err := store.GetExecution("exec-subtask-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("expected subtask row with merged parent branch to heal to 'completed', got %q (error=%q)", got.Status, got.Error)
	}
	if got.PRUrl != mergedPRURL {
		t.Errorf("expected pr_url = %q, got %q", mergedPRURL, got.PRUrl)
	}
}

// TestRecoverStaleRunningTasks_MarksFailedWhenNoMergedPR guards the negative
// case: a genuinely orphaned running row (no live worker, no merged PR on its
// branch) must still be marked "failed" — the GH-4092 healing path must not
// swallow real orphans.
func TestRecoverStaleRunningTasks_MarksFailedWhenNoMergedPR(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-orphan-run", TaskID: "GH-9999", ProjectPath: "/project-orphan", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) {
		return "", nil
	}
	defer func() { staleRunningMergedPRCheck = origCheck }()

	config := &DispatcherConfig{
		StaleRunningThreshold: 0,
		StaleQueuedThreshold:  0,
		StaleRecoveryInterval: time.Hour,
	}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, config)

	dispatcher.recoverStaleRunningTasks()

	got, err := store.GetExecution("exec-orphan-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("expected genuinely orphaned running task to be marked 'failed', got %q", got.Status)
	}
}

// TestRecoverStaleRunningTasks_SkipsWhenOpenPRExists is the GH-4423 regression
// test for finding 1's second half: an OPEN PR on the task's branch — the
// normal state for minutes-to-hours while CI/review runs — must be treated as
// liveness evidence exactly like a merged PR, not "no evidence" that gets the
// row marked failed. Before this fix, staleRunningMergedPRCheck alone
// couldn't see this and any task whose review outlasted StaleRunningThreshold
// got marked failed on the next reap tick despite already having shipped a PR.
func TestRecoverStaleRunningTasks_SkipsWhenOpenPRExists(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-open-pr-run", TaskID: "GH-4423-E", ProjectPath: "/project-open-pr", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origMergedCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { staleRunningMergedPRCheck = origMergedCheck }()

	const openPRURL = "https://github.com/qf-studio/pilot/pull/4422"
	origOpenCheck := staleRunningOpenPRCheck
	staleRunningOpenPRCheck = func(_ context.Context, projectPath, branch string) (string, error) {
		if projectPath == "/project-open-pr" && branch == "pilot/GH-4423-E" {
			return openPRURL, nil
		}
		return "", nil
	}
	defer func() { staleRunningOpenPRCheck = origOpenCheck }()

	config := &DispatcherConfig{StaleRunningThreshold: 0, StaleQueuedThreshold: 0, StaleRecoveryInterval: time.Hour}
	dispatcher := NewDispatcher(store, NewRunner(), config)

	resetCount := dispatcher.recoverStaleRunningTasks()
	if resetCount != 0 {
		t.Errorf("expected resetCount=0 (open PR is liveness, not a reap), got %d", resetCount)
	}

	got, err := store.GetExecution("exec-open-pr-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("expected row with an open PR to remain 'running' (not failed/completed), got %q", got.Status)
	}

	events, err := store.ListExecutionEvents("exec-open-pr-run")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no execution events for a skipped (still-live) row, got %d: %+v", len(events), events)
	}
}

// TestRecoverStaleRunningTasks_CASRejectsRaceWithCompletion is the GH-4423
// regression test for finding 1's TOCTOU: if the row's own worker completes
// it for real in the gap between the reaper's evidence-gathering
// (staleRunningMergedPRCheck's GitHub round trip here) and the reaper's final
// "mark failed" write, that final write must be rejected instead of
// silently stamping the completed row failed. The merged-PR-check override
// below simulates the race by completing the row as a side effect, mirroring
// a concurrent writer finishing the task mid-reap.
func TestRecoverStaleRunningTasks_CASRejectsRaceWithCompletion(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-race-run", TaskID: "GH-4423-F", ProjectPath: "/project-race", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	const racingPRURL = "https://github.com/qf-studio/pilot/pull/4424"

	origMergedCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) {
		// Simulate the real worker completing this exact row for real,
		// concurrently with this reap tick's evidence-gathering — the row is
		// now genuinely 'completed' by the time the reaper reaches its final
		// write below, but this check itself reports "no merge evidence"
		// (mirroring the check having run before the race landed).
		if err := store.MarkExecutionCompleted("exec-race-run", racingPRURL, "cafefeed", 1234); err != nil {
			t.Fatalf("failed to simulate racing completion: %v", err)
		}
		return "", nil
	}
	defer func() { staleRunningMergedPRCheck = origMergedCheck }()

	origOpenCheck := staleRunningOpenPRCheck
	staleRunningOpenPRCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { staleRunningOpenPRCheck = origOpenCheck }()

	config := &DispatcherConfig{StaleRunningThreshold: 0, StaleQueuedThreshold: 0, StaleRecoveryInterval: time.Hour}
	dispatcher := NewDispatcher(store, NewRunner(), config)

	resetCount := dispatcher.recoverStaleRunningTasks()
	if resetCount != 0 {
		t.Errorf("expected resetCount=0 — the CAS guard must reject the failed-write, got %d", resetCount)
	}

	got, err := store.GetExecution("exec-race-run")
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("expected the racing completion to survive the reap ('completed'), got %q (error=%q)", got.Status, got.Error)
	}
	if got.PRUrl != racingPRURL {
		t.Errorf("expected pr_url from the racing completion to survive, got %q", got.PRUrl)
	}

	// The reaper must not have recorded a stale_running-failed audit event
	// over the real completion — only evidence of the reap's own outcome
	// (none, since the write was rejected) may exist.
	events, err := store.ListExecutionEvents("exec-race-run")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	for _, e := range events {
		if e.Stage == memory.StageFailed {
			t.Errorf("expected no stale_running-failed event over a racing completion, got %+v", e)
		}
	}
}

// TestRecoverStaleRunningTasks_WritesExecutionEvent verifies GH-4101: marking
// a stale running task failed also writes an execution_events row, closing
// the gap where a restart/orphan-driven terminal transition was invisible in
// the audit trail (the root-causing gap in the 2026-07-08 GH-4050
// duplicate-execution incident, where execution_events for 5ce9bc2c simply
// stopped with no terminal entry).
func TestRecoverStaleRunningTasks_WritesExecutionEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-event-run", TaskID: "GH-4101-A", ProjectPath: "/project-event", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { staleRunningMergedPRCheck = origCheck }()

	config := &DispatcherConfig{StaleRunningThreshold: 0, StaleQueuedThreshold: 0, StaleRecoveryInterval: time.Hour}
	dispatcher := NewDispatcher(store, NewRunner(), config)

	dispatcher.recoverStaleRunningTasks()

	events, err := store.ListExecutionEvents("exec-event-run")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 execution event, got %d: %+v", len(events), events)
	}
	if events[0].Stage != memory.StageFailed {
		t.Errorf("expected stage %q, got %q", memory.StageFailed, events[0].Stage)
	}
	if !strings.Contains(events[0].Detail, "stale_running recovered after restart") {
		t.Errorf("expected detail to explain the stale_running recovery reason, got %q", events[0].Detail)
	}
}

// TestRecoverStaleRunningTasks_HealToCompleted_WritesExecutionEvent verifies
// the GH-4092 heal-to-completed branch (a stale "running" row whose branch PR
// already merged) also writes an execution_events row (GH-4101) — not just
// the fail branch.
func TestRecoverStaleRunningTasks_HealToCompleted_WritesExecutionEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-event-heal", TaskID: "GH-4101-B", ProjectPath: "/project-event-heal", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	const mergedPRURL = "https://github.com/qf-studio/pilot/pull/9001"
	origCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, projectPath, branch string) (string, error) {
		if projectPath == "/project-event-heal" && branch == "pilot/GH-4101-B" {
			return mergedPRURL, nil
		}
		return "", nil
	}
	defer func() { staleRunningMergedPRCheck = origCheck }()

	config := &DispatcherConfig{StaleRunningThreshold: 0, StaleQueuedThreshold: 0, StaleRecoveryInterval: time.Hour}
	dispatcher := NewDispatcher(store, NewRunner(), config)

	dispatcher.recoverStaleRunningTasks()

	events, err := store.ListExecutionEvents("exec-event-heal")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 execution event, got %d: %+v", len(events), events)
	}
	if events[0].Stage != memory.StageCompleted {
		t.Errorf("expected stage %q, got %q", memory.StageCompleted, events[0].Stage)
	}
	if !strings.Contains(events[0].Detail, mergedPRURL) {
		t.Errorf("expected detail to mention the merged PR URL, got %q", events[0].Detail)
	}
}

// TestRecoverStaleRunningTasks_FateReconstructableFromEventsAlone is the
// GH-4101 acceptance test mirroring the GH-4050 incident: reconstruct a
// restart-orphaned execution's fate using ONLY execution_events (never
// consulting executions.status), the same investigative path that was
// unavailable during the incident because the timeline simply stopped.
func TestRecoverStaleRunningTasks_FateReconstructableFromEventsAlone(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const execID = "exec-fate-run"
	exec := &memory.Execution{ID: execID, TaskID: "GH-4101-D", ProjectPath: "/project-fate", Status: "running"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origCheck := staleRunningMergedPRCheck
	staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { staleRunningMergedPRCheck = origCheck }()

	config := &DispatcherConfig{StaleRunningThreshold: 0, StaleQueuedThreshold: 0, StaleRecoveryInterval: time.Hour}
	dispatcher := NewDispatcher(store, NewRunner(), config)
	dispatcher.recoverStaleRunningTasks()

	// Reconstruct fate from execution_events alone — deliberately never call
	// store.GetExecution here.
	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("execution_events timeline is empty — fate is unrecoverable from events alone (the GH-4050 gap)")
	}
	last := events[len(events)-1]
	if last.Stage != memory.StageFailed {
		t.Fatalf("reconstructed fate from events: last stage = %q, want %q (terminal failure)", last.Stage, memory.StageFailed)
	}
	if !strings.Contains(last.Detail, "recovered after restart") {
		t.Errorf("reconstructed fate from events: detail %q does not explain the restart-driven recovery", last.Detail)
	}
}

// TestProcessQueue_MergedPRPreflight_SkipsBackend is the GH-4141 Phase 3
// regression test: a queued task whose branch already has a merged PR (e.g.
// a poller-retry duplicate of a sub-issue the epic already shipped, TASK-394)
// must complete from the pre-flight check alone — zero backend invocations —
// instead of burning a full Claude run to rediscover "no new commit" as a
// no_op.
func TestProcessQueue_MergedPRPreflight_SkipsBackend(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const projectPath = "/project-merged-preflight"
	const branch = "pilot/GH-8001"
	const mergedPRURL = "https://github.com/qf-studio/pilot/pull/8001"

	exec := &memory.Execution{
		ID:           "exec-preflight-merged",
		TaskID:       "GH-8001",
		ProjectPath:  projectPath,
		Status:       "queued",
		TaskBranch:   branch,
		TaskCreatePR: true,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, gotProjectPath, gotBranch string) (string, error) {
		if gotProjectPath == projectPath && gotBranch == branch {
			return mergedPRURL, nil
		}
		return "", nil
	}
	defer func() { mergedPRPreflightCheck = origCheck }()

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner := NewRunnerWithBackend(backend)
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Errorf("expected zero backend invocations (pre-flight short-circuit), got %d", count)
	}

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", got.Status)
	}
	if got.PRUrl != mergedPRURL {
		t.Errorf("expected pr_url %q, got %q", mergedPRURL, got.PRUrl)
	}
}

// TestProcessQueue_MergedPRPreflight_UnmergedBranchProceedsNormally is the
// GH-4141 Phase 3 negative case: a queued task whose branch has no merged PR
// (e.g. still open, or no PR at all) must proceed through the normal
// execution path unchanged — the backend is still invoked.
func TestProcessQueue_MergedPRPreflight_UnmergedBranchProceedsNormally(t *testing.T) {
	const branch = "pilot/GH-8002"
	dir := setupPRGuardRepo(t, branch, false) // no additional commits

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:           "exec-preflight-unmerged",
		TaskID:       "GH-8002",
		ProjectPath:  dir,
		Status:       "queued",
		TaskBranch:   branch,
		TaskCreatePR: true,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { mergedPRPreflightCheck = origCheck }()

	// Backend always succeeds but makes no git commits (mirrors
	// TestRunner_PRCreate_EmptyBranch_TriggersRetry's no-commit guard setup).
	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(dir, store, runner, slog.Default())

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count == 0 {
		t.Error("expected the backend to be invoked (no merged PR found) — pre-flight must not have short-circuited")
	}

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.PRUrl != "" {
		t.Errorf("expected no pr_url recorded (no merged PR short-circuit should have fired), got %q", got.PRUrl)
	}
}

// TestProcessQueue_NoCommitFailure_CardBecomesNoOp is the GH-4490 subtask 2
// regression test: a queued task whose backend succeeds but produces no git
// commits classifies to the "no_op" executions status (TerminalStatus's
// no_changes signature). Before this fix, the dispatcher's terminal-outcome
// branch only called EmitProgress — which drives phase/progress but never
// the Monitor's Status field — so the dashboard card stayed at
// running/100% until the periodic reconciler's next tick (subtask 1's
// backstop) caught up. The card must now transition to a terminal,
// non-failure state (StatusNoOp, not StatusFailed) on this same event path.
func TestProcessQueue_NoCommitFailure_CardBecomesNoOp(t *testing.T) {
	const branch = "pilot/GH-8003"
	dir := setupPRGuardRepo(t, branch, false) // no additional commits

	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{
		ID:           "exec-no-commit-card",
		TaskID:       "GH-8003",
		ProjectPath:  dir,
		Status:       "queued",
		TaskBranch:   branch,
		TaskCreatePR: true,
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { mergedPRPreflightCheck = origCheck }()

	// Backend always succeeds but makes no git commits — the same no-commit
	// setup TestRunner_PRCreate_EmptyBranch_TriggersRetry uses, which
	// classifies to TerminalStatus "no_op" via the no_changes signature.
	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "analysis complete"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}

	monitor := NewMonitor()
	monitor.Register(exec.TaskID, "no-commit test task", "")
	monitor.Queue(exec.TaskID)
	runner.SetMonitor(monitor)

	worker := NewProjectWorker(dir, store, runner, slog.Default())

	worker.processQueue(context.Background())

	got, err := store.GetExecution(exec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "no_op" {
		t.Fatalf("expected execution status 'no_op', got %q (test setup drifted from TerminalStatus classification)", got.Status)
	}

	state, ok := monitor.Get(exec.TaskID)
	if !ok {
		t.Fatal("task not found in monitor after processQueue")
	}
	if state.Status != StatusNoOp {
		t.Errorf("card Status = %s, want %s (card must not stay running or read as a generic failure)", state.Status, StatusNoOp)
	}
	if state.CompletedAt == nil {
		t.Error("expected CompletedAt to be set — card must be terminal, not stuck at running")
	}
}

// TestProcessQueue_TerminalSuccessLedger_SkipsBackend is the GH-4184
// regression test for the 17:48->18:12 race: the poller's re-arm guard
// decided "not yet completed" at poll time and let a retry queue; the
// genuine completion landed in the TASK-394 execution ledger before the
// dispatcher picked the duplicate row up, with no GitHub-side signal (no
// status labels, no merged PR yet visible) to catch it. Seed the ledger
// directly with a completed row and force the merged-PR preflight to report
// nothing found — mimicking labels/state that were mutated away between poll
// and pickup — so only the ledger guard itself can explain a skip.
func TestProcessQueue_TerminalSuccessLedger_SkipsBackend(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const projectPath = "/project-terminal-ledger"
	const taskID = "GH-9001"
	const priorPRURL = "https://github.com/qf-studio/pilot/pull/9001"

	// Seed the ledger: a prior execution row for this task already completed
	// with a real deliverable (the TASK-394 "running"->"completed" row).
	priorExec := &memory.Execution{
		ID:          "exec-terminal-success-prior",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "running",
	}
	if err := store.SaveExecution(priorExec); err != nil {
		t.Fatalf("failed to save prior execution: %v", err)
	}
	if err := store.MarkExecutionCompleted(priorExec.ID, priorPRURL, "deadbeef", 1000); err != nil {
		t.Fatalf("failed to mark prior execution completed: %v", err)
	}

	// A second, freshly queued row for the SAME task — the duplicate that
	// reached dispatcher pickup after the poller's poll-time check missed
	// the completion above.
	dupExec := &memory.Execution{
		ID:          "exec-terminal-success-dup",
		TaskID:      taskID,
		ProjectPath: projectPath,
		Status:      "queued",
		TaskBranch:  "pilot/GH-9001",
	}
	if err := store.SaveExecution(dupExec); err != nil {
		t.Fatalf("failed to save duplicate execution: %v", err)
	}

	// Force the pre-existing merged-PR preflight to report nothing — even
	// with that signal absent (mutated labels/state), the ledger guard alone
	// must refuse to dispatch.
	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { mergedPRPreflightCheck = origCheck }()

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
	runner := NewRunnerWithBackend(backend)
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 0 {
		t.Errorf("expected zero backend invocations (terminal-success ledger guard), got %d", count)
	}

	got, err := store.GetExecution(dupExec.ID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if got.Status != "completed" {
		t.Errorf("expected duplicate row status 'completed' (ledger-guarded), got %q", got.Status)
	}
	if got.PRUrl != priorPRURL {
		t.Errorf("expected duplicate row to carry prior pr_url %q, got %q", priorPRURL, got.PRUrl)
	}
}

// TestProcessQueue_CrossTaskIDGuard is the GH-4216 (Defect A, fix 3) table
// test for the cross-task-id dispatch guard: an epic parent task_id that
// recorded a StageDecomposed ledger event must not be re-dispatched as a
// fresh top-level task (re-implementing already-shipped work, the GH-4211
// repro) once every child it decomposed into has a genuine completed
// execution — but must still run normally when any child is incomplete
// (existing epic-resume behavior).
func TestProcessQueue_CrossTaskIDGuard(t *testing.T) {
	tests := []struct {
		name             string
		childStatuses    []string // one completed execution row per child, "" = no row at all
		wantBackendCalls int
		wantStatus       string
	}{
		{
			name:             "all children completed skips re-implementation",
			childStatuses:    []string{"completed", "completed"},
			wantBackendCalls: 0,
			wantStatus:       "completed",
		},
		{
			name:             "one child incomplete falls through to normal dispatch",
			childStatuses:    []string{"completed", "running"},
			wantBackendCalls: 1,
			wantStatus:       "completed", // mockFixedBackend succeeds; runner marks it completed
		},
		{
			name:             "no completed rows for either child falls through to normal dispatch",
			childStatuses:    []string{"", ""},
			wantBackendCalls: 1,
			wantStatus:       "completed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			const parentTaskID = "GH-4211"
			projectPath := setupPRGuardRepo(t, "pilot/GH-4211", false)

			parentExec := &memory.Execution{
				ID:          "exec-4211-failed",
				TaskID:      parentTaskID,
				ProjectPath: projectPath,
				Status:      "failed",
				TaskBranch:  "pilot/GH-4211",
			}
			if err := store.SaveExecution(parentExec); err != nil {
				t.Fatalf("failed to save parent execution: %v", err)
			}
			if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed,
				"decomposed into 2 children: #4212, #4213"); err != nil {
				t.Fatalf("failed to insert decomposed event: %v", err)
			}

			children := []string{"GH-4212", "GH-4213"}
			for i, status := range tc.childStatuses {
				if status == "" {
					continue
				}
				childExec := &memory.Execution{
					ID:          fmt.Sprintf("exec-%s", children[i]),
					TaskID:      children[i],
					ProjectPath: projectPath,
					Status:      status,
				}
				if status == "completed" {
					childExec.PRUrl = fmt.Sprintf("https://github.com/qf-studio/pilot/pull/%s", strings.TrimPrefix(children[i], "GH-"))
				}
				if err := store.SaveExecution(childExec); err != nil {
					t.Fatalf("failed to save child execution: %v", err)
				}
			}

			// A freshly re-queued row for the parent task_id — the GH-4211 repro's
			// re-poll re-dispatch.
			requeued := &memory.Execution{
				ID:          "exec-4211-requeued",
				TaskID:      parentTaskID,
				ProjectPath: projectPath,
				Status:      "queued",
				TaskBranch:  "pilot/GH-4211",
			}
			if err := store.SaveExecution(requeued); err != nil {
				t.Fatalf("failed to save requeued execution: %v", err)
			}

			origCheck := mergedPRPreflightCheck
			mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
			defer func() { mergedPRPreflightCheck = origCheck }()

			backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "ok"}}
			runner := NewRunnerWithBackend(backend)
			runner.skipPreflightChecks = true
			runner.config = &BackendConfig{SkipSelfReview: true}
			worker := NewProjectWorker(projectPath, store, runner, slog.Default())

			worker.processQueue(context.Background())

			backend.mu.Lock()
			count := backend.execCount
			backend.mu.Unlock()
			if count != tc.wantBackendCalls {
				t.Errorf("backend invocations = %d, want %d", count, tc.wantBackendCalls)
			}

			got, err := store.GetExecution(requeued.ID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("requeued row status = %q, want %q", got.Status, tc.wantStatus)
			}
			if tc.wantBackendCalls == 0 && got.PRUrl == "" {
				t.Error("expected the cross-task-id guard to carry a child pr_url as completion evidence")
			}
		})
	}
}

// TestDecomposedChildrenAllComplete is the GH-4227 table test for the shared
// decomposed-parent guard helper backing every dispatcher.go call site that
// consults HasCompletedExecution(taskID) for a task_id that might itself be a
// decomposed epic parent: processQueue's pickup guard, stale-running/queued
// recovery, and WaitForExecution's row-vanished resolution.
func TestDecomposedChildrenAllComplete(t *testing.T) {
	const projectPath = "/project-decomposed-guard"

	t.Run("all children complete short-circuits", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		parentExec := &memory.Execution{ID: "exec-parent-a", TaskID: "GH-5001", ProjectPath: projectPath, Status: "failed"}
		if err := store.SaveExecution(parentExec); err != nil {
			t.Fatalf("SaveExecution(parent): %v", err)
		}
		if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into 2 children: #5002, #5003"); err != nil {
			t.Fatalf("InsertExecutionEvent: %v", err)
		}
		for _, child := range []string{"GH-5002", "GH-5003"} {
			childExec := &memory.Execution{
				ID: "exec-" + child, TaskID: child, ProjectPath: projectPath,
				Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/" + strings.TrimPrefix(child, "GH-"),
			}
			if err := store.SaveExecution(childExec); err != nil {
				t.Fatalf("SaveExecution(child): %v", err)
			}
		}

		allComplete, childIDs, evidence, err := decomposedChildrenAllComplete(store, "GH-5001", projectPath, slog.Default())
		if err != nil {
			t.Fatalf("decomposedChildrenAllComplete: %v", err)
		}
		if !allComplete {
			t.Error("expected allComplete=true when every decomposed child has a genuine completed row")
		}
		if !reflect.DeepEqual(childIDs, []string{"GH-5002", "GH-5003"}) {
			t.Errorf("childIDs = %v, want [GH-5002 GH-5003]", childIDs)
		}
		if len(evidence) != 2 {
			t.Errorf("expected per-child evidence for both children, got %v", evidence)
		}
	})

	t.Run("one child incomplete falls through", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		parentExec := &memory.Execution{ID: "exec-parent-b", TaskID: "GH-5011", ProjectPath: projectPath, Status: "failed"}
		if err := store.SaveExecution(parentExec); err != nil {
			t.Fatalf("SaveExecution(parent): %v", err)
		}
		if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into 2 children: #5012, #5013"); err != nil {
			t.Fatalf("InsertExecutionEvent: %v", err)
		}
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-GH-5012", TaskID: "GH-5012", ProjectPath: projectPath,
			Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/5012",
		}); err != nil {
			t.Fatalf("SaveExecution(child1): %v", err)
		}
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-GH-5013", TaskID: "GH-5013", ProjectPath: projectPath, Status: "running",
		}); err != nil {
			t.Fatalf("SaveExecution(child2): %v", err)
		}

		allComplete, _, _, err := decomposedChildrenAllComplete(store, "GH-5011", projectPath, slog.Default())
		if err != nil {
			t.Fatalf("decomposedChildrenAllComplete: %v", err)
		}
		if allComplete {
			t.Error("expected allComplete=false when a decomposed child is still incomplete (normal epic-resume path)")
		}
	})

	t.Run("no decomposed event uses normal path", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-direct", TaskID: "GH-5021", ProjectPath: projectPath, Status: "running",
		}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}

		allComplete, childIDs, _, err := decomposedChildrenAllComplete(store, "GH-5021", projectPath, slog.Default())
		if err != nil {
			t.Fatalf("decomposedChildrenAllComplete: %v", err)
		}
		if allComplete {
			t.Error("expected allComplete=false for a task that never decomposed")
		}
		if len(childIDs) != 0 {
			t.Errorf("expected no child IDs, got %v", childIDs)
		}
	})

	t.Run("malformed detail string falls through safely with a warning log", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		parentExec := &memory.Execution{ID: "exec-parent-malformed", TaskID: "GH-5031", ProjectPath: projectPath, Status: "failed"}
		if err := store.SaveExecution(parentExec); err != nil {
			t.Fatalf("SaveExecution(parent): %v", err)
		}
		// No "#NNN" issue refs in the detail string — a malformed/legacy format
		// that decomposedChildRefRegex cannot parse into child task IDs.
		if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into subtasks"); err != nil {
			t.Fatalf("InsertExecutionEvent: %v", err)
		}

		var logBuf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&logBuf, nil))

		allComplete, childIDs, evidence, err := decomposedChildrenAllComplete(store, "GH-5031", projectPath, log)
		if err != nil {
			t.Fatalf("decomposedChildrenAllComplete: %v", err)
		}
		if allComplete {
			t.Error("expected allComplete=false for a malformed decomposed detail string")
		}
		if len(childIDs) != 0 || len(evidence) != 0 {
			t.Errorf("expected no child IDs or evidence for a malformed detail string, got childIDs=%v evidence=%v", childIDs, evidence)
		}
		if !strings.Contains(logBuf.String(), "no child refs parsed") {
			t.Errorf("expected a warning log about the unparseable decomposed detail, got: %s", logBuf.String())
		}
	})
}

// TestHasTerminalCompletion is the GH-4347 table test for the exported
// "is this task done" definition shared by the SDK poller's
// ExecutionChecker (cmd/pilot/main.go's terminalCompletionChecker) and
// dispatcher.go's own pickup guard (hasTerminalSuccessLedger). A no_op
// outcome with no error must count as terminal (matching
// childCompletionEvidence's existing "nothing to change is itself a
// legitimate completion" definition) even though it never satisfies the
// stricter Store.HasCompletedExecution.
func TestHasTerminalCompletion(t *testing.T) {
	const projectPath = "/project-terminal-completion"

	tests := []struct {
		name string
		exec *memory.Execution
		want bool
	}{
		{
			name: "genuine completed row with deliverable",
			exec: &memory.Execution{ID: "exec-htc-completed", TaskID: "GH-100", ProjectPath: projectPath, Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/100"},
			want: true,
		},
		{
			name: "no_op with no error is terminal",
			exec: &memory.Execution{ID: "exec-htc-noop", TaskID: "GH-101", ProjectPath: projectPath, Status: "no_op"},
			want: true,
		},
		{
			name: "no_op with an error is NOT terminal",
			exec: &memory.Execution{ID: "exec-htc-noop-err", TaskID: "GH-102", ProjectPath: projectPath, Status: "no_op", Error: "claude subprocess crashed"},
			want: false,
		},
		{
			name: "still running is not terminal",
			exec: &memory.Execution{ID: "exec-htc-running", TaskID: "GH-103", ProjectPath: projectPath, Status: "running"},
			want: false,
		},
		{
			name: "infra failure is not terminal (should still retry)",
			exec: &memory.Execution{ID: "exec-htc-infra", TaskID: "GH-104", ProjectPath: projectPath, Status: "infra"},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			if err := store.SaveExecution(tc.exec); err != nil {
				t.Fatalf("SaveExecution: %v", err)
			}

			got, err := HasTerminalCompletion(store, tc.exec.TaskID, projectPath)
			if err != nil {
				t.Fatalf("HasTerminalCompletion: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasTerminalCompletion(%q, %q) = %v, want %v", tc.exec.TaskID, projectPath, got, tc.want)
			}
		})
	}
}

// TestDispatcher_HasTerminalCompletion is the GH-4376 regression test for the
// exported Dispatcher method: it must delegate to the same
// package-level HasTerminalCompletion definition of "done" the poller's
// ExecutionChecker and this package's own hasTerminalSuccessLedger use, so
// admission gates outside this package (cmd/pilot/handler_common.go) agree
// with everything inside it.
func TestDispatcher_HasTerminalCompletion(t *testing.T) {
	const projectPath = "/project-dispatcher-terminal-completion"

	store, cleanup := setupTestStore(t)
	defer cleanup()

	d := NewDispatcher(store, NewRunner(), nil)

	completedExec := &memory.Execution{
		ID: "exec-d-htc-completed", TaskID: "GH-91", ProjectPath: projectPath,
		Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/91",
	}
	if err := store.SaveExecution(completedExec); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	done, err := d.HasTerminalCompletion("GH-91", projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if !done {
		t.Error("expected HasTerminalCompletion=true for a task with a genuine completed row")
	}

	done, err = d.HasTerminalCompletion("GH-92-never-dispatched", projectPath)
	if err != nil {
		t.Fatalf("HasTerminalCompletion: %v", err)
	}
	if done {
		t.Error("expected HasTerminalCompletion=false for a task with no ledger evidence")
	}
}

// TestProcessQueue_NoOpTerminalLedger_SkipsBackend is the GH-4347 regression
// for the pilot-canary-sandbox incident: GH-82 (a decomposed epic sub-issue)
// legitimately resolved to no_op ("nothing to change") and was re-dispatched
// on every subsequent poll tick — six live executions in one canary cycle —
// because neither the dispatcher's pickup guard nor the SDK poller's
// pre-dispatch check recognized a no_op row as terminal. Table-driven across
// a pilot-repo-style path and a canary-sandbox-style path (the task's
// acceptance criterion (a)) since the defect was reported as sandbox-only.
func TestProcessQueue_NoOpTerminalLedger_SkipsBackend(t *testing.T) {
	tests := []struct {
		name        string
		projectPath string
	}{
		{"pilot-repo-style path", "/Users/pilot-op/Projects/startups/pilot"},
		{"canary-sandbox-style path", "/Users/pilot-op/Projects/startups/pilot-canary-sandbox"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			const taskID = "GH-82"

			// Seed the ledger with the prior no_op outcome — GH-82's actual
			// terminal state on the canary sandbox ("greeter farewell; no
			// surviving PR").
			priorExec := &memory.Execution{
				ID:          "exec-noop-prior",
				TaskID:      taskID,
				ProjectPath: tc.projectPath,
				Status:      "no_op",
			}
			if err := store.SaveExecution(priorExec); err != nil {
				t.Fatalf("failed to save prior no_op execution: %v", err)
			}

			// A second, freshly queued row for the SAME task — the
			// re-dispatch that must be refused now that no_op is recognized
			// as terminal.
			dupExec := &memory.Execution{
				ID:          "exec-noop-dup",
				TaskID:      taskID,
				ProjectPath: tc.projectPath,
				Status:      "queued",
				TaskBranch:  "pilot/GH-82",
			}
			if err := store.SaveExecution(dupExec); err != nil {
				t.Fatalf("failed to save duplicate execution: %v", err)
			}

			origCheck := mergedPRPreflightCheck
			mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
			defer func() { mergedPRPreflightCheck = origCheck }()

			backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "should never run"}}
			runner := NewRunnerWithBackend(backend)
			worker := NewProjectWorker(tc.projectPath, store, runner, slog.Default())

			worker.processQueue(context.Background())

			backend.mu.Lock()
			count := backend.execCount
			backend.mu.Unlock()
			if count != 0 {
				t.Errorf("expected zero backend invocations (no_op terminal-ledger guard), got %d", count)
			}

			got, err := store.GetExecution(dupExec.ID)
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != "completed" {
				t.Errorf("expected duplicate row status 'completed' (ledger-guarded), got %q", got.Status)
			}
		})
	}
}

// blockingBackend blocks every Execute call until the passed-in context is
// canceled. GH-4513: TestQueueTask_ConcurrentDuplicate_DispatchesOnce used to
// run its dispatcher against NewRunner()'s real ClaudeCodeBackend, whose
// preflight check fails fast (~0.5-1s, chdir into the test's non-existent
// project path) — see that test's doc comment for why this raced the
// concurrent QueueTask calls it was supposed to be testing. Blocking here
// instead of racing a real backend keeps the winning dispatch's execution row
// pinned at status "running" for the entire test, deterministically.
type blockingBackend struct{}

func (b *blockingBackend) Name() string      { return "mock-blocking" }
func (b *blockingBackend) IsAvailable() bool { return true }
func (b *blockingBackend) Execute(ctx context.Context, _ ExecuteOptions) (*BackendResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestQueueTask_ConcurrentDuplicate_DispatchesOnce is the GH-4347 race test
// for the dispatchMu fix: QueueTask's duplicate check (IsTaskQueued) and its
// executions-row insert used to be two unlocked store calls, so concurrent
// callers racing the same task_id/project_path — e.g. the SDK poller's
// per-issue goroutines, or a poll tick landing while an epic is still
// creating sub-issues — could both observe "not queued" before either row
// landed. Table-driven across a pilot-repo-style and a sandbox-style project
// path reusing the SAME small issue number (acceptance criteria (b) and (c):
// concurrent poll ticks still dispatch once, and small-issue-number reuse
// across projects/cycles never cross-collides).
//
// GH-4513: this used to wire up NewRunner() — the REAL ClaudeCodeBackend —
// as the dispatcher's runner. The winning QueueTask call's ensureWorker()
// starts a background ProjectWorker goroutine that immediately picks up the
// freshly-queued row, transitions it to "running", and calls runner.Execute,
// which is NOT covered by dispatchMu — only QueueTask's own duplicate check +
// insert are. Against a real backend, Execute's preflight check fails fast
// (chdir into the test's non-existent /Users/pilot-op/... project path,
// observed ~0.5-1s locally, plausibly faster or slower under CI load/-race
// scheduling) and marks the row terminal ("failed"). If that terminal
// transition lands before all `concurrency` goroutines have made it through
// dispatchMu's serialized IsTaskQueued check, a "loser" goroutine that checks
// afterward no longer sees the row as queued/running (IsTaskQueued only
// matches those two statuses) and falls through to
// beginWithGenerationRetry's legitimate repick-a-dead-claim path — which
// mints a second real dispatch and a second nil-error QueueTask return,
// inflating `successes` past 1. This is exactly the class of bug the test is
// meant to catch, just misfired at the test's own instrumentation instead of
// at dispatchMu: the real backend's completion latency is not something this
// test can control, so the assertion window's length was never guaranteed to
// outlast it. Swapping in blockingBackend (which never lets Execute return
// until the test tears the dispatcher down) removes that timing dependency
// entirely — the row is now guaranteed to stay non-terminal for as long as
// the test needs it to, regardless of scheduler/CI load.
func TestQueueTask_ConcurrentDuplicate_DispatchesOnce(t *testing.T) {
	const concurrency = 8

	tests := []struct {
		name        string
		projectPath string
	}{
		{"pilot-repo-style path", "/Users/pilot-op/Projects/startups/pilot"},
		{"canary-sandbox-style path", "/Users/pilot-op/Projects/startups/pilot-canary-sandbox"},
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunnerWithBackend(&blockingBackend{})
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	dispatcher := NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const taskID = "GH-60"

			var wg sync.WaitGroup
			var mu sync.Mutex
			var successes int
			var alreadyActive int
			var otherErrs []error

			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					task := &Task{
						ID:          taskID,
						Title:       "Concurrent dispatch race",
						Description: "GH-4347 regression",
						ProjectPath: tc.projectPath,
					}
					_, err := dispatcher.QueueTask(context.Background(), task)
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						successes++
					case errors.Is(err, ErrTaskAlreadyActive):
						alreadyActive++
					default:
						otherErrs = append(otherErrs, err)
					}
				}()
			}
			wg.Wait()

			if len(otherErrs) != 0 {
				t.Fatalf("unexpected QueueTask errors: %v", otherErrs)
			}
			if successes != 1 {
				t.Errorf("expected exactly 1 successful dispatch out of %d concurrent QueueTask calls, got %d (already-active: %d)", concurrency, successes, alreadyActive)
			}
			if alreadyActive != concurrency-1 {
				t.Errorf("expected %d ErrTaskAlreadyActive rejections, got %d", concurrency-1, alreadyActive)
			}

			queued, err := store.IsTaskQueued(taskID, tc.projectPath)
			if err != nil {
				t.Fatalf("IsTaskQueued: %v", err)
			}
			if !queued {
				t.Error("expected the single successful dispatch to leave the task queued")
			}
		})
	}
}

// TestProcessQueue_CrossTaskIDGuard_MalformedDetailFallsThrough covers the
// GH-4227 case (iv) at the processQueue call site specifically: a
// StageDecomposed event whose detail string has no parseable child refs must
// not block dispatch — the task falls through to the normal epic-resume path
// (backend invoked) rather than the guard silently skipping execution.
func TestProcessQueue_CrossTaskIDGuard_MalformedDetailFallsThrough(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	const parentTaskID = "GH-5041"
	projectPath := setupPRGuardRepo(t, "pilot/GH-5041", false)

	parentExec := &memory.Execution{
		ID: "exec-5041-failed", TaskID: parentTaskID, ProjectPath: projectPath,
		Status: "failed", TaskBranch: "pilot/GH-5041",
	}
	if err := store.SaveExecution(parentExec); err != nil {
		t.Fatalf("failed to save parent execution: %v", err)
	}
	if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into subtasks"); err != nil {
		t.Fatalf("failed to insert decomposed event: %v", err)
	}

	requeued := &memory.Execution{
		ID: "exec-5041-requeued", TaskID: parentTaskID, ProjectPath: projectPath,
		Status: "queued", TaskBranch: "pilot/GH-5041",
	}
	if err := store.SaveExecution(requeued); err != nil {
		t.Fatalf("failed to save requeued execution: %v", err)
	}

	origCheck := mergedPRPreflightCheck
	mergedPRPreflightCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
	defer func() { mergedPRPreflightCheck = origCheck }()

	backend := &mockFixedBackend{result: &BackendResult{Success: true, Output: "ok"}}
	runner := NewRunnerWithBackend(backend)
	runner.skipPreflightChecks = true
	runner.config = &BackendConfig{SkipSelfReview: true}
	worker := NewProjectWorker(projectPath, store, runner, slog.Default())

	worker.processQueue(context.Background())

	backend.mu.Lock()
	count := backend.execCount
	backend.mu.Unlock()
	if count != 1 {
		t.Errorf("backend invocations = %d, want 1 (malformed decomposed detail must fall through, not block dispatch)", count)
	}
}

// TestRecoverStaleRunningTasks_DecomposedParentGuard is the GH-4227 table
// test for the decomposed-parent guard at the stale-running reap site: a
// decomposed epic parent stuck in "running" must be deleted (not marked
// failed) once every child it decomposed into has shipped, since its own row
// carries no deliverable (TASK-296) and would otherwise never satisfy
// HasCompletedExecution.
func TestRecoverStaleRunningTasks_DecomposedParentGuard(t *testing.T) {
	tests := []struct {
		name          string
		childStatuses []string // "" = no row at all
		wantDeleted   bool
		wantStatus    string // checked only when !wantDeleted
	}{
		{name: "all children completed guard fires", childStatuses: []string{"completed", "completed"}, wantDeleted: true},
		{name: "one child incomplete falls through to failed", childStatuses: []string{"completed", "running"}, wantDeleted: false, wantStatus: "failed"},
		{name: "no completed rows falls through to failed", childStatuses: []string{"", ""}, wantDeleted: false, wantStatus: "failed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			const parentTaskID = "GH-5051"
			const projectPath = "/project-decomposed-running"

			parentExec := &memory.Execution{ID: "exec-5051-running", TaskID: parentTaskID, ProjectPath: projectPath, Status: "running"}
			if err := store.SaveExecution(parentExec); err != nil {
				t.Fatalf("failed to save parent execution: %v", err)
			}
			if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into 2 children: #5052, #5053"); err != nil {
				t.Fatalf("failed to insert decomposed event: %v", err)
			}

			children := []string{"GH-5052", "GH-5053"}
			for i, status := range tc.childStatuses {
				if status == "" {
					continue
				}
				childExec := &memory.Execution{ID: "exec-" + children[i], TaskID: children[i], ProjectPath: projectPath, Status: status}
				if status == "completed" {
					childExec.PRUrl = "https://github.com/qf-studio/pilot/pull/" + strings.TrimPrefix(children[i], "GH-")
				}
				if err := store.SaveExecution(childExec); err != nil {
					t.Fatalf("failed to save child execution: %v", err)
				}
			}

			origCheck := staleRunningMergedPRCheck
			staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
			defer func() { staleRunningMergedPRCheck = origCheck }()

			config := &DispatcherConfig{StaleRunningThreshold: 0, StaleQueuedThreshold: 0, StaleRecoveryInterval: time.Hour}
			dispatcher := NewDispatcher(store, NewRunner(), config)

			dispatcher.recoverStaleRunningTasks()

			got, err := store.GetExecution(parentExec.ID)
			if tc.wantDeleted {
				if err == nil {
					t.Errorf("expected the decomposed-parent-guarded row to be deleted, but it still exists with status %q", got.Status)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}

// TestRecoverStaleQueuedTasks_DecomposedParentGuard mirrors
// TestRecoverStaleRunningTasks_DecomposedParentGuard for the stale-queued
// reap site (GH-4227).
func TestRecoverStaleQueuedTasks_DecomposedParentGuard(t *testing.T) {
	tests := []struct {
		name          string
		childStatuses []string
		wantDeleted   bool
		wantStatus    string
	}{
		{name: "all children completed guard fires", childStatuses: []string{"completed", "completed"}, wantDeleted: true},
		{name: "one child incomplete falls through to failed", childStatuses: []string{"completed", "running"}, wantDeleted: false, wantStatus: "failed"},
		{name: "no completed rows falls through to failed", childStatuses: []string{"", ""}, wantDeleted: false, wantStatus: "failed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			const parentTaskID = "GH-5061"
			const projectPath = "/project-decomposed-queued"

			parentExec := &memory.Execution{ID: "exec-5061-queued", TaskID: parentTaskID, ProjectPath: projectPath, Status: "queued"}
			if err := store.SaveExecution(parentExec); err != nil {
				t.Fatalf("failed to save parent execution: %v", err)
			}
			if err := store.InsertExecutionEvent(parentExec.ID, memory.StageDecomposed, "decomposed into 2 children: #5062, #5063"); err != nil {
				t.Fatalf("failed to insert decomposed event: %v", err)
			}

			children := []string{"GH-5062", "GH-5063"}
			for i, status := range tc.childStatuses {
				if status == "" {
					continue
				}
				childExec := &memory.Execution{ID: "exec-" + children[i], TaskID: children[i], ProjectPath: projectPath, Status: status}
				if status == "completed" {
					childExec.PRUrl = "https://github.com/qf-studio/pilot/pull/" + strings.TrimPrefix(children[i], "GH-")
				}
				if err := store.SaveExecution(childExec); err != nil {
					t.Fatalf("failed to save child execution: %v", err)
				}
			}

			config := &DispatcherConfig{StaleQueuedThreshold: 0}
			dispatcher := NewDispatcher(store, NewRunner(), config)

			dispatcher.recoverStaleQueuedTasks()

			got, err := store.GetExecution(parentExec.ID)
			if tc.wantDeleted {
				if err == nil {
					t.Errorf("expected the decomposed-parent-guarded row to be deleted, but it still exists with status %q", got.Status)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetExecution: %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}

// TestWaitForExecution_DecomposedParentGuard_ResolvesAsSuccess covers GH-4227
// at the WaitForExecution row-vanished site: when the waited-on row
// disappears (e.g. deleted by the stale-running reap's decomposed-parent
// guard branch) and its task_id is a decomposed parent whose children all
// shipped, the wait must resolve as success instead of surfacing a
// "failed to get execution" error.
func TestWaitForExecution_DecomposedParentGuard_ResolvesAsSuccess(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	const parentTaskID = "GH-5071"
	const projectPath = "/project-decomposed-wait"
	const orphanID = "exec-5071-running"

	// The original decomposed-parent row (never deleted) carries the
	// StageDecomposed ledger event, mirroring the real shape: a duplicate
	// "orphan" row for the same task_id (below) is the one actually being
	// waited on and reaped, while the decompose-time row it originated from
	// stays put — GetDecomposedChildTaskIDs's INNER JOIN needs a live
	// executions row to hang the event off of.
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-5071-decompose-origin", TaskID: parentTaskID, ProjectPath: projectPath, Status: "failed",
	}); err != nil {
		t.Fatalf("SaveExecution(decompose origin): %v", err)
	}
	if err := store.InsertExecutionEvent("exec-5071-decompose-origin", memory.StageDecomposed, "decomposed into 1 children: #5072"); err != nil {
		t.Fatalf("InsertExecutionEvent: %v", err)
	}

	if err := store.SaveExecution(&memory.Execution{
		ID: orphanID, TaskID: parentTaskID, ProjectPath: projectPath, Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(orphan): %v", err)
	}
	const childPRURL = "https://github.com/qf-studio/pilot/pull/5072"
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-GH-5072", TaskID: "GH-5072", ProjectPath: projectPath, Status: "completed", PRUrl: childPRURL,
	}); err != nil {
		t.Fatalf("SaveExecution(child): %v", err)
	}

	type result struct {
		exec *memory.Execution
		err  error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		exec, err := dispatcher.WaitForExecution(ctx, orphanID, 10*time.Millisecond)
		resultCh <- result{exec, err}
	}()

	// Let the waiter observe the "running" row at least once before it's
	// deleted out from under it, mirroring the real race (recoverStaleRunningTasks'
	// decomposed-parent guard branch deletes the row once children are seen complete).
	time.Sleep(30 * time.Millisecond)
	if err := store.DeleteExecution(orphanID); err != nil {
		t.Fatalf("DeleteExecution: %v", err)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("WaitForExecution returned error, want success: %v", res.err)
		}
		if res.exec.Status != "completed" {
			t.Errorf("Status = %q, want %q", res.exec.Status, "completed")
		}
		if res.exec.PRUrl != childPRURL {
			t.Errorf("PRUrl = %q, want %q (evidence from the last decomposed child)", res.exec.PRUrl, childPRURL)
		}
	case <-ctx.Done():
		t.Fatal("WaitForExecution did not return before timeout")
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

// GH-4021: recoverStaleRunningTasks deletes a task's orphaned "running" row
// once it observes the task actually completed under a different execution
// ID (cleanup after a redundant re-dispatch). A waiter still polling that
// exact execID must resolve the vanished row as success — not surface
// "sql: no rows" as a failure. GH-3992: this raced a false task_failed alert
// for work that had already shipped.
func TestWaitForExecution_RowDeletedAfterCompletion_ResolvesAsSuccess(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	const orphanID = "exec-orphan"
	if err := store.SaveExecution(&memory.Execution{
		ID:          orphanID,
		TaskID:      "GH-99",
		ProjectPath: "/tmp/p",
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution(orphan): %v", err)
	}

	const completedID = "exec-completed"
	if err := store.SaveExecution(&memory.Execution{
		ID:          completedID,
		TaskID:      "GH-99",
		ProjectPath: "/tmp/p",
		Status:      "completed",
		PRUrl:       "https://github.com/owner/repo/pull/1",
	}); err != nil {
		t.Fatalf("SaveExecution(completed): %v", err)
	}

	type result struct {
		exec *memory.Execution
		err  error
	}
	resultCh := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		exec, err := dispatcher.WaitForExecution(ctx, orphanID, 10*time.Millisecond)
		resultCh <- result{exec, err}
	}()

	// Let the waiter observe the "running" row at least once (capturing its
	// task/project identity) before it's deleted out from under it — this
	// mirrors the real race, where the row exists when the wait starts.
	time.Sleep(30 * time.Millisecond)
	if err := store.DeleteExecution(orphanID); err != nil {
		t.Fatalf("DeleteExecution: %v", err)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("WaitForExecution returned error, want success: %v", res.err)
		}
		if res.exec.Status != "completed" {
			t.Errorf("Status = %q, want %q", res.exec.Status, "completed")
		}
		if res.exec.ID != completedID {
			t.Errorf("resolved execution ID = %q, want %q (the genuinely completed row)", res.exec.ID, completedID)
		}
	case <-ctx.Done():
		t.Fatal("WaitForExecution did not return before timeout")
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

// TestDispatcher_ReconcileOrphanedExecutions is the GH-4392 regression suite
// for Dispatcher.Start's boot-time orphan reconciliation: a claimed
// queued/running row found before this process has created any worker can
// only have been left behind by a dead prior daemon (single-daemon
// invariant, H7/#4311) — nextRetryGeneration (GH-4372) otherwise treats such
// a row as a live owner forever, wedging the task (the TASK-409 AWS cutover
// incident this issue tracks). Mirrors the guard ordering
// recoverStaleRunningTasks already uses (decomposed-parent guard, then
// HasCompletedExecution, then the GH-4092 merged-PR heal) so a boot orphan
// whose real work already shipped heals or is deleted instead of being
// marked stalled.
func TestDispatcher_ReconcileOrphanedExecutions(t *testing.T) {
	t.Run("claimed queued row becomes stalled and journaled", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4392-Q1", ProjectPath: "/project-orphan-q"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		if reconciled := dispatcher.reconcileOrphanedExecutions(); reconciled != 1 {
			t.Fatalf("expected 1 reconciled execution, got %d", reconciled)
		}

		exec, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if exec.Status != "stalled" {
			t.Errorf("expected status 'stalled', got %q", exec.Status)
		}

		events, err := store.ListExecutionEvents(execID)
		if err != nil {
			t.Fatalf("ListExecutionEvents: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 execution event, got %d: %+v", len(events), events)
		}
		if events[0].Stage != memory.StageStalled {
			t.Errorf("expected stage %q, got %q", memory.StageStalled, events[0].Stage)
		}
		if !strings.Contains(events[0].Detail, "GH-4392") {
			t.Errorf("expected detail to reference GH-4392, got %q", events[0].Detail)
		}
	})

	t.Run("claimed running row becomes stalled when branch not merged", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4392-R1", ProjectPath: "/project-orphan-r"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		origCheck := staleRunningMergedPRCheck
		staleRunningMergedPRCheck = func(_ context.Context, _, _ string) (string, error) { return "", nil }
		defer func() { staleRunningMergedPRCheck = origCheck }()

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		if reconciled := dispatcher.reconcileOrphanedExecutions(); reconciled != 1 {
			t.Fatalf("expected 1 reconciled execution, got %d", reconciled)
		}

		exec, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if exec.Status != "stalled" {
			t.Errorf("expected status 'stalled', got %q", exec.Status)
		}
	})

	t.Run("claimed running row heals to completed when branch already merged", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4392-R2", ProjectPath: "/project-orphan-merged"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		const mergedPRURL = "https://github.com/qf-studio/pilot/pull/9101"
		origCheck := staleRunningMergedPRCheck
		staleRunningMergedPRCheck = func(_ context.Context, projectPath, branch string) (string, error) {
			if projectPath == "/project-orphan-merged" && branch == "pilot/GH-4392-R2" {
				return mergedPRURL, nil
			}
			return "", nil
		}
		defer func() { staleRunningMergedPRCheck = origCheck }()

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		dispatcher.reconcileOrphanedExecutions()

		exec, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if exec.Status != "completed" {
			t.Errorf("expected status 'completed', got %q", exec.Status)
		}
		if exec.PRUrl != mergedPRURL {
			t.Errorf("expected pr_url %q, got %q", mergedPRURL, exec.PRUrl)
		}

		events, err := store.ListExecutionEvents(execID)
		if err != nil {
			t.Fatalf("ListExecutionEvents: %v", err)
		}
		if len(events) != 1 || events[0].Stage != memory.StageCompleted {
			t.Fatalf("expected 1 StageCompleted event, got %+v", events)
		}
	})

	t.Run("claimed running row heals using recorded branch, not reconstructed task-id branch", func(t *testing.T) {
		// GH-4409: boot reconciliation's own merged-PR heal check must use the
		// same branch-derivation fix as recoverStaleRunningTasks — a claimed
		// decomposed-subtask row found at boot recorded its PARENT's branch,
		// not one reconstructed from its own task ID.
		store, cleanup := setupTestStore(t)
		defer cleanup()

		subtask := &Task{ID: "GH-4409-5", ProjectPath: "/project-epic-boot", Branch: "pilot/GH-4409"}
		execID, err := NewExecutionLifecycle(store).Begin(subtask, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		const mergedPRURL = "https://github.com/qf-studio/pilot/pull/9202"
		origCheck := staleRunningMergedPRCheck
		staleRunningMergedPRCheck = func(_ context.Context, projectPath, branch string) (string, error) {
			if branch == "pilot/GH-4409-5" {
				t.Errorf("staleRunningMergedPRCheck probed the reconstructed subtask branch %q instead of the recorded parent branch", branch)
			}
			if projectPath == "/project-epic-boot" && branch == "pilot/GH-4409" {
				return mergedPRURL, nil
			}
			return "", nil
		}
		defer func() { staleRunningMergedPRCheck = origCheck }()

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		dispatcher.reconcileOrphanedExecutions()

		exec, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if exec.Status != "completed" {
			t.Errorf("expected status 'completed', got %q", exec.Status)
		}
		if exec.PRUrl != mergedPRURL {
			t.Errorf("expected pr_url %q, got %q", mergedPRURL, exec.PRUrl)
		}
	})

	t.Run("unclaimed queued row (bare SaveExecution) is left untouched", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		// GH-3732 restart-adoption fixtures use bare SaveExecution — no
		// execution_claims row. Boot reconciliation must not touch these, or
		// TestDispatcher_AdoptQueuedProjectsOnRestart's FIFO drain breaks.
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-unclaimed", TaskID: "GH-4392-UNCLAIMED", ProjectPath: "/project-unclaimed", Status: "queued",
		}); err != nil {
			t.Fatalf("SaveExecution: %v", err)
		}

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		if reconciled := dispatcher.reconcileOrphanedExecutions(); reconciled != 0 {
			t.Fatalf("expected 0 reconciled (unclaimed row), got %d", reconciled)
		}

		exec, err := store.GetExecution("exec-unclaimed")
		if err != nil {
			t.Fatalf("GetExecution: %v", err)
		}
		if exec.Status != "queued" {
			t.Errorf("expected unclaimed row to remain 'queued', got %q", exec.Status)
		}
	})

	t.Run("claimed queued row already completed elsewhere is deleted", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		const taskID = "GH-4392-DUP"
		const projectPath = "/project-orphan-dup"

		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-dup-completed", TaskID: taskID, ProjectPath: projectPath, Status: "completed",
			PRUrl: "https://github.com/qf-studio/pilot/pull/9102",
		}); err != nil {
			t.Fatalf("SaveExecution(completed): %v", err)
		}

		task := &Task{ID: taskID, ProjectPath: projectPath}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		dispatcher.reconcileOrphanedExecutions()

		exec, err := store.GetExecution(execID)
		if err == nil && exec != nil {
			t.Errorf("expected orphaned duplicate row %s to be deleted, but it still exists with status %q", execID, exec.Status)
		}
	})
}

// TestDispatcher_ReconcileOrphanedExecutions_Idempotent verifies boot
// reconciliation only ever fires once per row (GH-4392 acceptance
// criterion): once a dead-owner row has been transitioned to 'stalled', a
// second restart's boot pass must find nothing left to reconcile —
// GetClaimedNonTerminalExecutions no longer returns a terminal row — and
// must not write a second execution_events entry for it.
func TestDispatcher_ReconcileOrphanedExecutions_Idempotent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4392-IDEMPOTENT", ProjectPath: "/project-idempotent"}
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	first := NewDispatcher(store, NewRunner(), nil).reconcileOrphanedExecutions()
	if first != 1 {
		t.Fatalf("expected 1 reconciled on first pass, got %d", first)
	}

	// Simulate a second restart: a brand new Dispatcher against the same
	// store.
	second := NewDispatcher(store, NewRunner(), nil).reconcileOrphanedExecutions()
	if second != 0 {
		t.Fatalf("expected 0 reconciled on second pass (idempotent), got %d", second)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected exactly 1 stalled event across both boot passes, got %d: %+v", len(events), events)
	}
}

// TestDispatcher_ReconcileOrphanedExecutions_ClearsRepickBackoff is the
// GH-4454 subtask 1 regression test: a daemon restart is not evidence a task
// can't succeed, so boot reconciliation stalling a dead-owner row must clear
// any repick_backoff state already accumulated for that task — otherwise the
// generation+1 retry this stall enables inherits a consecutive-drop count
// inflated by restart churn instead of genuine failures, pushing the task
// toward dispatcherRepickHardCap for reasons unrelated to the task itself.
func TestDispatcher_ReconcileOrphanedExecutions_ClearsRepickBackoff(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4454-BACKOFF", ProjectPath: "/project-boot-backoff"}
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	dispatcher := NewDispatcher(store, NewRunner(), nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Simulate consecutive drops already accumulated BEFORE the restart (e.g.
	// real repicks from a prior daemon lifetime), sitting one shy of the hard
	// cap.
	const preExistingDrops = dispatcherRepickHardCap - 1
	if err := dispatcher.SetRepickBackoffState(key, preExistingDrops, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("setup SetRepickBackoffState: %v", err)
	}

	if reconciled := dispatcher.reconcileOrphanedExecutions(); reconciled != 1 {
		t.Fatalf("expected 1 reconciled execution, got %d", reconciled)
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "stalled" {
		t.Errorf("expected status 'stalled', got %q", exec.Status)
	}

	if _, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found {
		t.Fatal("expected boot reconciliation to clear repick backoff state for the row it stalled, but state still exists")
	}
}

// TestDispatcher_BootOrphanReconciliation_EnablesGenerationRetry is the
// GH-4392 acceptance test: a dead daemon's claimed 'queued' row must not
// wedge the task forever. After Dispatcher.Start's boot reconciliation
// transitions it to 'stalled' (a terminal status), nextRetryGeneration's
// dead-owner path (GH-4372) sees the claim is dead and hands out a
// generation+1 retry on the very next dispatch attempt — closing the
// "dispatch claim lost" loop that TASK-409's AWS cutover incident hit.
func TestDispatcher_BootOrphanReconciliation_EnablesGenerationRetry(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4392-RETRY", ProjectPath: "/project-retry"}

	// Simulate the dead pre-restart daemon: it claimed generation 0 and left
	// the row 'queued' (e.g. TASK-409's AWS cutover kill).
	if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusQueued); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	// Fresh process, fresh Dispatcher — Start() runs boot reconciliation
	// before anything else, including before any worker exists.
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer dispatcher.Stop()

	gen, retry, err := dispatcher.nextRetryGeneration(task.ID, task.ProjectPath)
	if err != nil {
		t.Fatalf("nextRetryGeneration: %v", err)
	}
	if !retry {
		t.Fatalf("expected retry=true after boot reconciliation stalled the dead claim, got retry=false")
	}
	if gen != 1 {
		t.Errorf("expected generation 1, got %d", gen)
	}

	// The actual dispatch path: a fresh Task struct simulating the next
	// poller pickup for the same (task.ID, task.ProjectPath).
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}
	execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if execID == "" {
		t.Fatal("expected a fresh execID claiming generation 1, got empty (pickup dropped)")
	}

	exec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if exec.Status != "queued" {
		t.Errorf("expected fresh generation-1 execution to be 'queued', got %q", exec.Status)
	}

	genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	}
	if !found || genCheck != 1 {
		t.Errorf("expected latest claim generation 1, found=%v got=%d", found, genCheck)
	}
}

// TestDispatcher_BeginWithGenerationRetry_ArmsRepickBackoff is the GH-4394
// subtask 2 regression test: a successful terminal-claim re-pick (the
// "dispatch re-pick: prior claim was terminal but task is not done" path)
// must extend the SAME repick_backoff row the poller-originated throttle
// (#4385) reads/writes, not leave it untouched the way it did before this
// fix — which was the actual mechanism behind GH-85 re-picking 5x in ~15 min
// with no backoff growth.
func TestDispatcher_BeginWithGenerationRetry_ArmsRepickBackoff(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4394-ARM", ProjectPath: "/project-arm"}
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	dispatcher := NewDispatcher(store, NewRunner(), nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	if _, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found {
		t.Fatal("expected no repick backoff state before the first re-pick")
	}

	before := time.Now()
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID == "" {
		t.Fatal("expected the first re-pick to succeed with a fresh execID")
	}

	consecutive, nextAllowedAt, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState after re-pick: %v", err)
	}
	if !found {
		t.Fatal("expected the re-pick to persist repick backoff state")
	}
	if consecutive != 1 {
		t.Errorf("expected consecutive_drops=1 after one re-pick, got %d", consecutive)
	}
	if !nextAllowedAt.After(before) {
		t.Errorf("expected next_allowed_at (%v) to be after the re-pick (%v)", nextAllowedAt, before)
	}
}

// TestDispatcher_BeginWithGenerationRetry_ThrottledWithinBackoffWindow is the
// GH-4394 subtask 2 core regression test: once a re-pick has armed the
// backoff, a SECOND re-pick attempt for the same task within the cooldown
// window must be dropped — not silently re-armed on every poll tick the way
// GH-85 was (5 repicks in ~15 min, no growth).
func TestDispatcher_BeginWithGenerationRetry_ThrottledWithinBackoffWindow(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4394-THROTTLE", ProjectPath: "/project-throttle"}
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Simulate an already-armed backoff window from a prior re-pick, well in
	// the future so this test isn't timing-sensitive.
	if err := dispatcher.SetRepickBackoffState(key, 3, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}

	// A prior claim that IS eligible for retry (terminal, not done) —
	// otherwise nextRetryGeneration itself would short-circuit before the
	// backoff gate is ever reached, and this test wouldn't prove anything.
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	gen, retry, err := dispatcher.nextRetryGeneration(task.ID, task.ProjectPath)
	if err != nil {
		t.Fatalf("nextRetryGeneration: %v", err)
	}
	if !retry || gen != 1 {
		t.Fatalf("expected retry=true generation=1 as the precondition for this test, got retry=%v gen=%d", retry, gen)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID != "" {
		t.Fatal("expected the re-pick to be dropped while the backoff window is active, got a fresh execID")
	}

	if genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	} else if found && genCheck != 0 {
		t.Errorf("expected no generation-1 claim to have been made while throttled, latest generation=%d", genCheck)
	}

	consecutive, _, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	}
	if !found || consecutive != 3 {
		t.Errorf("expected the throttled attempt to leave backoff state untouched (consecutive_drops=3), got found=%v consecutive=%d", found, consecutive)
	}
}

// TestDispatcher_BeginWithGenerationRetry_OperatorCancelBypassesHardCap is
// the GH-4454 subtask 2 acceptance test: an operator-cancelled claim
// (status="cancelled", the manual-intervention value used to unblock a
// wedged head-of-queue task — see priorClaimWasOperatorCancelled) must not
// be treated as a failure by the hard-cap accounting. Even with the
// persisted consecutive-drop count already AT dispatcherRepickHardCap,
// beginWithGenerationRetry must still grant the retry (not stall the task
// or raise an alert) and must clear the backoff state instead of growing
// it — otherwise an operator's own cancel-to-unblock attempts permanently
// stall the very task they were trying to save.
func TestDispatcher_BeginWithGenerationRetry_OperatorCancelBypassesHardCap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4454-CANCEL", ProjectPath: "/project-cancel", Title: "Operator-cancelled task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Already at the hard cap — if this repick were treated like an ordinary
	// failure-driven retry, it would trip stallTaskAfterRepickHardCap.
	if err := dispatcher.SetRepickBackoffState(key, dispatcherRepickHardCap, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	// Operator-cancelled, not failed — the manual-intervention value that
	// must be exempted from the hard-cap counter.
	if err := store.UpdateExecutionStatus(execID, "cancelled"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as cancelled: %v", err)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID == "" {
		t.Fatal("expected the operator-cancelled repick to succeed despite the hard cap being reached")
	}

	if genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	} else if !found || genCheck != 1 {
		t.Errorf("expected a generation-1 claim after the operator-cancelled repick, found=%v generation=%d", found, genCheck)
	}

	if len(processor.events) != 0 {
		t.Fatalf("expected no hard-cap alert for an operator-cancelled repick, got %d: %+v", len(processor.events), processor.events)
	}

	if stalledExec, err := store.GetExecution(execID); err != nil {
		t.Fatalf("GetExecution: %v", err)
	} else if stalledExec.Status == "stalled" {
		t.Error("expected the operator-cancelled execution to be left alone, not marked stalled")
	}

	if _, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found {
		t.Error("expected the operator-cancelled repick to clear backoff state instead of growing it")
	}
}

// TestDispatcher_BeginWithGenerationRetry_HardCapStallsInsteadOfRetrying is
// the GH-4394 subtask 5 acceptance test: exponential backoff alone (subtask
// 2/3) never stops a doomed task from retrying — it only slows the interval
// down, capping at ~16 min forever. Once consecutive repicks reach
// dispatcherRepickHardCap, beginWithGenerationRetry must stop granting new
// generations altogether, mark the claimed execution "stalled", and raise an
// alert — instead of retrying yet again once the backoff window (already
// elapsed here) permits it.
func TestDispatcher_BeginWithGenerationRetry_HardCapStallsInsteadOfRetrying(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4394-HARDCAP", ProjectPath: "/project-hardcap", Title: "Hard cap task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Already at the hard cap, and the backoff window has already elapsed —
	// proving the hard cap itself (not the window) is what stops the retry.
	if err := dispatcher.SetRepickBackoffState(key, dispatcherRepickHardCap, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}

	// A prior claim that IS eligible for retry (terminal, not done) —
	// otherwise nextRetryGeneration itself would short-circuit before the
	// hard cap gate is ever reached.
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	gen, retry, err := dispatcher.nextRetryGeneration(task.ID, task.ProjectPath)
	if err != nil {
		t.Fatalf("nextRetryGeneration: %v", err)
	}
	if !retry || gen != 1 {
		t.Fatalf("expected retry=true generation=1 as the precondition for this test, got retry=%v gen=%d", retry, gen)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID != "" {
		t.Fatal("expected the re-pick to be dropped once the hard cap is reached, got a fresh execID")
	}

	if genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	} else if found && genCheck != 0 {
		t.Errorf("expected no generation-1 claim once the hard cap tripped, latest generation=%d", genCheck)
	}

	stalledExec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if stalledExec.Status != "stalled" {
		t.Errorf("expected the claimed execution to be marked stalled, got status=%q", stalledExec.Status)
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].TaskID != task.ID {
		t.Errorf("expected alert for task %q, got %q", task.ID, processor.events[0].TaskID)
	}
	if processor.events[0].Metadata["reason"] != "repick_hard_cap_stalled" {
		t.Errorf("expected alert metadata reason=repick_hard_cap_stalled, got %q", processor.events[0].Metadata["reason"])
	}
}

// TestDispatcher_StallTaskAfterRepickHardCap_SurfacesStalledIssue is the
// GH-4454 subtask 3 regression test: reaching the repick hard cap must label
// the task's GitHub issue pilot-blocked (dropping pilot-failed/
// pilot-in-progress) instead of leaving it eligible to keep winning
// studio-sdk's scope-overlap dispatch grouping — a stalled head issue that
// keeps winning its scope cluster silently starves every sibling issue that
// touches the same files (the "7h silent idle" in GH-4454's title).
func TestDispatcher_StallTaskAfterRepickHardCap_SurfacesStalledIssue(t *testing.T) {
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" + `echo "$@" >> "` + logFile + `"` + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	projectDir := t.TempDir()
	task := &Task{ID: "GH-9001", ProjectPath: projectDir, Title: "Wedged head issue", SourceAdapter: "github"}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	dispatcher.stallTaskAfterRepickHardCap(task, 0, dispatcherRepickHardCap)

	stalledExec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if stalledExec.Status != "stalled" {
		t.Fatalf("expected execution stalled, got %q", stalledExec.Status)
	}

	logBytes, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("expected gh CLI to be invoked, but log file missing: %v", err)
	}
	calls := string(logBytes)
	if !strings.Contains(calls, "issue comment 9001") {
		t.Errorf("expected a comment posted to issue 9001, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "issue edit 9001") {
		t.Errorf("expected a label edit on issue 9001, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "--add-label pilot-blocked") {
		t.Errorf("expected pilot-blocked to be added, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "--remove-label pilot-failed") {
		t.Errorf("expected pilot-failed to be removed, got calls:\n%s", calls)
	}
	if !strings.Contains(calls, "--remove-label pilot-in-progress") {
		t.Errorf("expected pilot-in-progress to be removed, got calls:\n%s", calls)
	}
}

// TestDispatcher_StallTaskAfterRepickHardCap_NonGitHubTaskSkipsGHCLI ensures
// the GH-4454 subtask 3 surfacing logic only shells out for GitHub-sourced
// tasks — mirrors postTitleRejectionEscalation's existing adapter guard so a
// Linear/GitLab/Jira task never triggers a `gh` CLI call it can't act on.
func TestDispatcher_StallTaskAfterRepickHardCap_NonGitHubTaskSkipsGHCLI(t *testing.T) {
	fakeBin := t.TempDir()
	logFile := filepath.Join(t.TempDir(), "gh-calls.log")
	script := filepath.Join(fakeBin, "gh")
	content := "#!/bin/sh\n" + `echo "$@" >> "` + logFile + `"` + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	origPATH := os.Getenv("PATH")
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+origPATH)

	store, cleanup := setupTestStore(t)
	defer cleanup()

	projectDir := t.TempDir()
	task := &Task{ID: "GL-42", ProjectPath: projectDir, Title: "Non-GitHub task", SourceAdapter: "gitlab"}
	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	dispatcher.stallTaskAfterRepickHardCap(task, 0, dispatcherRepickHardCap)

	if _, err := os.ReadFile(logFile); err == nil {
		t.Error("expected no gh CLI invocation for a non-GitHub task")
	}
}

// TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent covers the
// GH-4394 subtask 5 quiet-repeat requirement: once a task has been stalled by
// the hard cap, subsequent poll ticks that reach the same gate (e.g. after
// the backoff window elapses again) must not re-alert or write a duplicate
// execution event — the task stays quiet until a human re-arms it.
func TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4394-HARDCAP-IDEMPOTENT", ProjectPath: "/project-hardcap-idempotent"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	if err := dispatcher.SetRepickBackoffState(key, dispatcherRepickHardCap, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}
	for i := 0; i < 2; i++ {
		if execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued); err != nil {
			t.Fatalf("beginWithGenerationRetry call %d: %v", i, err)
		} else if execID != "" {
			t.Fatalf("beginWithGenerationRetry call %d: expected dropped retry, got execID %q", i, execID)
		}
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 alert event across both calls, got %d: %+v", len(processor.events), processor.events)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents: %v", err)
	}
	stalledEvents := 0
	for _, e := range events {
		if e.Stage == memory.StageStalled {
			stalledEvents++
		}
	}
	if stalledEvents != 1 {
		t.Errorf("expected exactly 1 stalled execution event across both calls, got %d: %+v", stalledEvents, events)
	}
}

// TestDispatcher_BeginWithGenerationRetry_ThrottlesCanaryProjectSameAsRegular
// is the GH-4394 subtask 3 regression test. One of three hypotheses filed
// against the GH-85 incident (which happened to fire against the registered
// pilot-canary-sandbox project, GH-4240/TASK-379) was that IsCanary/
// ProjectConfig.Canary might short-circuit the repick backoff the same way it
// intentionally short-circuits metrics recording (runner.go's
// `if r.metricsRecorder != nil && !task.IsCanary` guards). Investigation found
// no such branch: beginWithGenerationRetry's backoff gate (dispatcher.go
// ~L913-930) never inspects task.IsCanary, and repickBackoffKey is keyed only
// on ProjectPath+TaskID, both of which are stable, config-registered values
// for a canary project just like any other. This test pins that: a
// canary-flagged task must be throttled by an already-armed backoff window
// exactly like a non-canary task (mirrors
// TestDispatcher_BeginWithGenerationRetry_ThrottledWithinBackoffWindow above,
// with IsCanary: true added) — if a future change adds an IsCanary
// short-circuit to this gate, this test fails.
func TestDispatcher_BeginWithGenerationRetry_ThrottlesCanaryProjectSameAsRegular(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-85", ProjectPath: "/canary-sandbox", IsCanary: true}
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Simulate an already-armed backoff window from a prior re-pick, well in
	// the future so this test isn't timing-sensitive.
	if err := dispatcher.SetRepickBackoffState(key, 3, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}

	// A prior claim that IS eligible for retry (terminal, not done) —
	// otherwise nextRetryGeneration itself would short-circuit before the
	// backoff gate is ever reached, and this test wouldn't prove anything.
	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	gen, retry, err := dispatcher.nextRetryGeneration(task.ID, task.ProjectPath)
	if err != nil {
		t.Fatalf("nextRetryGeneration: %v", err)
	}
	if !retry || gen != 1 {
		t.Fatalf("expected retry=true generation=1 as the precondition for this test, got retry=%v gen=%d", retry, gen)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, IsCanary: true}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID != "" {
		t.Fatal("expected the canary task's re-pick to be dropped while the backoff window is active, got a fresh execID — IsCanary must not bypass the repick backoff gate")
	}

	consecutive, _, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	}
	if !found || consecutive != 3 {
		t.Errorf("expected the throttled canary attempt to leave backoff state untouched (consecutive_drops=3), got found=%v consecutive=%d", found, consecutive)
	}
}

// markLatestClaimedExecution sets the status of (taskID, projectPath)'s
// currently claimed execution to status — a small test helper used to
// simulate a drop of a given class (e.g. "stalled" or "failed") before
// calling beginWithGenerationRetry to observe how the dispatcher accounts
// for it (GH-4502).
func markLatestClaimedExecution(t *testing.T, store *memory.Store, taskID, projectPath, status string) string {
	t.Helper()
	_, execID, found, err := store.LatestClaimGeneration(taskID, projectPath)
	if err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	}
	if !found {
		t.Fatal("expected a claimed execution to exist")
	}
	if err := store.UpdateExecutionStatus(execID, status); err != nil {
		t.Fatalf("UpdateExecutionStatus(%q): %v", status, err)
	}
	return execID
}

// TestDispatcher_BeginWithGenerationRetry_StallDropsDoNotCountTowardHardCap
// is the GH-4502 core acceptance test: pilot-console GH-24 saw 4 consecutive
// stall-watchdog kills wedge a healthy task, because each stall-kill grew
// the same consecutiveDrops counter a genuine failure does, tripping
// dispatcherRepickHardCap (5) on stalls alone. Four stalled drops followed
// by one genuine failed drop must leave the shared consecutiveDrops counter
// at 1 (only the failure counts) — not wedge the task — while the stall
// counter independently reflects the 4 stall-kills.
func TestDispatcher_BeginWithGenerationRetry_StallDropsDoNotCountTowardHardCap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4502-STALL4", ProjectPath: "/project-stall4"}
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}

	if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	// Four consecutive stall-kills: each repick must be granted (not wedged)
	// and must NOT grow consecutive_drops.
	for i := 0; i < 4; i++ {
		markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "stalled")
		execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
		if err != nil {
			t.Fatalf("beginWithGenerationRetry (stall %d): %v", i+1, err)
		}
		if execID == "" {
			t.Fatalf("expected stall drop %d to be granted a retry, got dropped", i+1)
		}
	}

	stallDrops, found, err := dispatcher.StallDropCount(key)
	if err != nil {
		t.Fatalf("StallDropCount: %v", err)
	}
	if !found || stallDrops != 4 {
		t.Errorf("expected stall_drops=4 after 4 stall-kills, got found=%v count=%d", found, stallDrops)
	}
	if consecutive, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found && consecutive != 0 {
		t.Errorf("expected consecutive_drops=0 after only stall-kills, got found=%v consecutive=%d", found, consecutive)
	}

	// A fifth, genuine failure must grow consecutive_drops — and must still
	// be granted, since only 1 genuine failure has happened so far, nowhere
	// near dispatcherRepickHardCap.
	markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "failed")
	execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry (failed): %v", err)
	}
	if execID == "" {
		t.Fatal("expected the task to NOT be wedged after 4 stalls + 1 failure — hard cap must not have tripped")
	}

	consecutive, _, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState after failure: %v", err)
	}
	if !found || consecutive != 1 {
		t.Errorf("expected consecutive_drops=1 reflecting only the genuine failure, got found=%v consecutive=%d", found, consecutive)
	}

	if stallDrops, _, err := dispatcher.StallDropCount(key); err != nil {
		t.Fatalf("StallDropCount after failure: %v", err)
	} else if stallDrops != 4 {
		t.Errorf("expected stall_drops to remain 4 (untouched by the genuine failure), got %d", stallDrops)
	}
}

// TestDispatcher_BeginWithGenerationRetry_StallCapEscalatesWithDistinctReason
// is the GH-4502 stall-cap acceptance test: unlike the operator-cancel carve
// out, stall-kills must NOT bypass accounting entirely — until the
// silent-turn stall root cause ships, a complex-lane task could stall
// deterministically forever, and an unlimited bypass would retry it forever,
// burning tokens. Once dispatcherStallRepickCap consecutive stall-kills have
// accumulated, the next stall-killed repick must escalate/hold the task with
// a distinct, truthful reason string identifying the stall class — not the
// generic hard-cap message a genuine failure would produce.
func TestDispatcher_BeginWithGenerationRetry_StallCapEscalatesWithDistinctReason(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4502-STALLCAP", ProjectPath: "/project-stallcap", Title: "Stall-capped task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Already at the stall cap.
	if err := dispatcher.SetStallDropCount(key, dispatcherStallRepickCap); err != nil {
		t.Fatalf("SetStallDropCount: %v", err)
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "stalled"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as stalled: %v", err)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID != "" {
		t.Fatal("expected the re-pick to be dropped once the stall cap is reached, got a fresh execID")
	}

	stalledExec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if stalledExec.Status != "stalled" {
		t.Errorf("expected the claimed execution to be marked stalled, got status=%q", stalledExec.Status)
	}
	if !strings.Contains(stalledExec.Error, "stall") {
		t.Errorf("expected a stall-class reason string, got %q", stalledExec.Error)
	}
	if strings.Contains(stalledExec.Error, "consecutive failed re-picks") {
		t.Errorf("expected the stall-class reason to be distinct from the generic hard-cap message, got %q", stalledExec.Error)
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].Metadata["reason"] != "stall_repick_cap_stalled" {
		t.Errorf("expected alert metadata reason=stall_repick_cap_stalled, got %q", processor.events[0].Metadata["reason"])
	}
}

// TestDispatcher_BeginWithGenerationRetry_OperatorCancelAndStallCarveOutsAreIndependent
// is the GH-4502 regression guard: adding the stall carve-out must not
// disturb the existing operator-cancel carve-out (priorClaimWasOperatorCancelled,
// consulted at the top of beginWithGenerationRetry) — an operator-cancelled
// claim must still bypass the hard cap entirely and clear backoff state,
// exactly as TestDispatcher_BeginWithGenerationRetry_OperatorCancelBypassesHardCap
// already pins, even with a nonzero stall_drops count sitting on the same
// repick_backoff row.
func TestDispatcher_BeginWithGenerationRetry_OperatorCancelAndStallCarveOutsAreIndependent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4502-CANCEL-INDEP", ProjectPath: "/project-cancel-indep", Title: "Operator-cancelled task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Already at the hard cap AND carrying a stall-drop count on the same
	// row — proving the operator-cancel carve-out still bypasses everything.
	if err := dispatcher.SetRepickBackoffState(key, dispatcherRepickHardCap, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState: %v", err)
	}
	if err := dispatcher.SetStallDropCount(key, dispatcherStallRepickCap-1); err != nil {
		t.Fatalf("SetStallDropCount: %v", err)
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "cancelled"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as cancelled: %v", err)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID == "" {
		t.Fatal("expected the operator-cancelled repick to succeed despite the hard cap and stall-drop count")
	}

	if len(processor.events) != 0 {
		t.Fatalf("expected no alert for an operator-cancelled repick, got %d: %+v", len(processor.events), processor.events)
	}

	if _, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found {
		t.Error("expected the operator-cancelled repick to clear all backoff state (including stall_drops), instead of granting a stall carve-out")
	}
}

// TestDispatcher_BeginWithGenerationRetry_MixedStallAndFailedDropsHardCapAtFive
// is the GH-4502 mixed-sequence acceptance test: stalled drops interleaved
// with failed drops must advance independent counters, and the shared
// consecutiveDrops counter must still trip dispatcherRepickHardCap at
// exactly 5 GENUINE failures, regardless of how many stall-kills were
// interleaved between them.
func TestDispatcher_BeginWithGenerationRetry_MixedStallAndFailedDropsHardCapAtFive(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4502-MIXED", ProjectPath: "/project-mixed", Title: "Mixed drop-class task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}

	if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	// stalled, failed, stalled, failed, stalled, failed, stalled, failed —
	// 4 genuine failures and 4 stall-kills interleaved, each independently
	// granted. The hard-cap check consults the counter as it stood BEFORE
	// this call's grant, so this loop leaves consecutive_drops at 4 (not yet
	// tripped) and stall_drops at 4; a 5th genuine failure after the loop is
	// what actually trips the cap.
	sequence := []string{"stalled", "failed", "stalled", "failed", "stalled", "failed", "stalled", "failed"}
	wantConsecutive, wantStallDrops := 0, 0
	for i, status := range sequence {
		markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, status)
		execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
		if err != nil {
			t.Fatalf("beginWithGenerationRetry (drop %d, %s): %v", i+1, status, err)
		}
		if execID == "" {
			t.Fatalf("drop %d (%s): expected the retry to be granted before the hard cap trips, got dropped", i+1, status)
		}

		if status == "stalled" {
			wantStallDrops++
		} else {
			wantConsecutive++
		}

		// This test is about drop-CLASS accounting, not the exponential
		// backoff-window timing a genuine failure also arms — re-arm the
		// window into the past after every granted retry so the next
		// iteration's decision is governed only by the cap, not by whether
		// wall-clock time has crossed the (up to ~16-minute) cooldown yet.
		if consecutive, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
			t.Fatalf("RepickBackoffState (drop %d): %v", i+1, err)
		} else if found {
			if err := dispatcher.SetRepickBackoffState(key, consecutive, time.Now().Add(-time.Minute)); err != nil {
				t.Fatalf("SetRepickBackoffState (drop %d): %v", i+1, err)
			}
		}
	}

	if wantConsecutive != dispatcherRepickHardCap-1 {
		t.Fatalf("test setup error: expected the sequence to accumulate exactly %d genuine failures, got %d", dispatcherRepickHardCap-1, wantConsecutive)
	}

	consecutive, _, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	}
	if !found || consecutive != dispatcherRepickHardCap-1 {
		t.Errorf("expected consecutive_drops=%d just short of the hard cap, got found=%v consecutive=%d", dispatcherRepickHardCap-1, found, consecutive)
	}

	stallDrops, _, err := dispatcher.StallDropCount(key)
	if err != nil {
		t.Fatalf("StallDropCount: %v", err)
	}
	if stallDrops != wantStallDrops {
		t.Errorf("expected stall_drops=%d (unaffected by interleaved genuine failures), got %d", wantStallDrops, stallDrops)
	}

	// The hard-cap check consults the counter as it stood BEFORE the current
	// call, so it takes one more granted genuine failure (bringing the
	// stored count up to dispatcherRepickHardCap) before the NEXT one is the
	// first to see consecutive_drops >= cap and actually trip it.
	markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "failed")
	fifthExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry (5th failure): %v", err)
	}
	if fifthExecID == "" {
		t.Fatal("expected the 5th genuine failure to still be granted (consecutive_drops was 4 going in)")
	}
	if consecutive, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState after 5th failure: %v", err)
	} else if !found || consecutive != dispatcherRepickHardCap {
		t.Fatalf("expected consecutive_drops=%d after the 5th failure, got found=%v consecutive=%d", dispatcherRepickHardCap, found, consecutive)
	} else if err := dispatcher.SetRepickBackoffState(key, consecutive, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("SetRepickBackoffState after 5th failure: %v", err)
	}

	// The 6th genuine failure must trip the hard cap: dropped, task marked
	// stalled, exactly one hard-cap alert fired.
	markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "failed")
	finalExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry (6th failure): %v", err)
	}
	if finalExecID != "" {
		t.Fatalf("expected the 6th genuine failure to trip the hard cap and be dropped, got execID %q", finalExecID)
	}

	consecutive, _, found, err = dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState after final failure: %v", err)
	}
	if !found || consecutive != dispatcherRepickHardCap {
		t.Errorf("expected consecutive_drops=%d at the hard cap, got found=%v consecutive=%d", dispatcherRepickHardCap, found, consecutive)
	}

	if stallDrops, _, err := dispatcher.StallDropCount(key); err != nil {
		t.Fatalf("StallDropCount after final failure: %v", err)
	} else if stallDrops != wantStallDrops {
		t.Errorf("expected stall_drops to remain %d (untouched by the hard-cap trip), got %d", wantStallDrops, stallDrops)
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 hard-cap alert event, got %d: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].Metadata["reason"] != "repick_hard_cap_stalled" {
		t.Errorf("expected alert metadata reason=repick_hard_cap_stalled, got %q", processor.events[0].Metadata["reason"])
	}
}

// TestDispatcher_IsActive_TreatsDecomposedAsActive is the GH-4540/TASK-421
// regression test for the actual GH-4537 mechanism: IsTaskQueued's SQL
// allowlist was missing 'decomposed', so a decomposed epic-parent's claim was
// invisible to IsActive() even though nextRetryGeneration (via
// isTerminalExecutionStatus) correctly treats "decomposed" as a live,
// non-terminal owner. That blind spot let a stream of redundant dispatch
// attempts slip past handler_common.go's IsActive precheck straight into the
// claim-lost drop path. Seeding a decomposed-status execution and asserting
// IsActive reports true pins the fix directly at the source.
func TestDispatcher_IsActive_TreatsDecomposedAsActive(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4537-DECOMPOSED", ProjectPath: "/project-decomposed"}
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "decomposed")

	if !dispatcher.IsActive(task.ID, task.ProjectPath) {
		t.Error("expected IsActive to report true for a decomposed (non-terminal) claim, got false")
	}
}

// TestDispatcher_BeginWithGenerationRetry_InfraDropsDoNotCountTowardHardCap
// is the GH-4540/TASK-421 infra-carve-out acceptance test, mirroring
// TestDispatcher_BeginWithGenerationRetry_StallDropsDoNotCountTowardHardCap:
// incident GH-4526 wedged a healthy task at dispatcherRepickHardCap on
// environment/infra failures alone (hosted git_clean preflight deadlocks, CI
// outages) — each infra-classified drop grew the same consecutiveDrops
// counter a genuine code failure does. Four consecutive infra drops followed
// by one genuine failed drop must leave the shared consecutive_drops counter
// at 1 (only the failure counts), while infra_drops independently reflects
// the 4 infra-classified drops.
func TestDispatcher_BeginWithGenerationRetry_InfraDropsDoNotCountTowardHardCap(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4540-INFRA4", ProjectPath: "/project-infra4"}
	dispatcher := NewDispatcher(store, NewRunner(), nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}

	if _, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning); err != nil {
		t.Fatalf("setup Begin: %v", err)
	}

	// Four consecutive infra-classified drops: each repick must be granted
	// (not wedged) and must NOT grow consecutive_drops.
	for i := 0; i < 4; i++ {
		markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "infra")
		execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
		if err != nil {
			t.Fatalf("beginWithGenerationRetry (infra %d): %v", i+1, err)
		}
		if execID == "" {
			t.Fatalf("expected infra drop %d to be granted a retry, got dropped", i+1)
		}
	}

	infraDrops, found, err := dispatcher.InfraDropCount(key)
	if err != nil {
		t.Fatalf("InfraDropCount: %v", err)
	}
	if !found || infraDrops != 4 {
		t.Errorf("expected infra_drops=4 after 4 infra-classified drops, got found=%v count=%d", found, infraDrops)
	}
	if consecutive, _, found, err := dispatcher.RepickBackoffState(key); err != nil {
		t.Fatalf("RepickBackoffState: %v", err)
	} else if found && consecutive != 0 {
		t.Errorf("expected consecutive_drops=0 after only infra drops, got found=%v consecutive=%d", found, consecutive)
	}

	// A fifth, genuine failure must grow consecutive_drops — and must still
	// be granted, since only 1 genuine failure has happened so far, nowhere
	// near dispatcherRepickHardCap.
	markLatestClaimedExecution(t, store, task.ID, task.ProjectPath, "failed")
	execID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry (failed): %v", err)
	}
	if execID == "" {
		t.Fatal("expected the task to NOT be wedged after 4 infra drops + 1 failure — hard cap must not have tripped")
	}

	consecutive, _, found, err := dispatcher.RepickBackoffState(key)
	if err != nil {
		t.Fatalf("RepickBackoffState after failure: %v", err)
	}
	if !found || consecutive != 1 {
		t.Errorf("expected consecutive_drops=1 reflecting only the genuine failure, got found=%v consecutive=%d", found, consecutive)
	}

	if infraDrops, _, err := dispatcher.InfraDropCount(key); err != nil {
		t.Fatalf("InfraDropCount after failure: %v", err)
	} else if infraDrops != 4 {
		t.Errorf("expected infra_drops to remain 4 (untouched by the genuine failure), got %d", infraDrops)
	}
}

// TestDispatcher_BeginWithGenerationRetry_InfraCapEscalatesWithDistinctReason
// is the GH-4540/TASK-421 infra-cap acceptance test, mirroring
// TestDispatcher_BeginWithGenerationRetry_StallCapEscalatesWithDistinctReason:
// once dispatcherInfraRepickCap consecutive infra-classified drops have
// accumulated, the next infra-classified repick must escalate/hold the task
// with a distinct, truthful reason string identifying the infra class — not
// the generic hard-cap message a genuine code failure would produce.
func TestDispatcher_BeginWithGenerationRetry_InfraCapEscalatesWithDistinctReason(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4540-INFRACAP", ProjectPath: "/project-infracap", Title: "Infra-capped task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)
	key := repickBackoffKey(task.ProjectPath, task.ID)

	// Already at the infra cap.
	if err := dispatcher.SetInfraDropCount(key, dispatcherInfraRepickCap); err != nil {
		t.Fatalf("SetInfraDropCount: %v", err)
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if err := store.UpdateExecutionStatus(execID, "infra"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as infra: %v", err)
	}

	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID != "" {
		t.Fatal("expected the re-pick to be dropped once the infra cap is reached, got a fresh execID")
	}

	infraExec, err := store.GetExecution(execID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if infraExec.Status != "stalled" {
		t.Errorf("expected the claimed execution to be marked stalled, got status=%q", infraExec.Status)
	}
	if !strings.Contains(infraExec.Error, "infra") {
		t.Errorf("expected an infra-class reason string, got %q", infraExec.Error)
	}
	if strings.Contains(infraExec.Error, "consecutive failed re-picks") {
		t.Errorf("expected the infra-class reason to be distinct from the generic hard-cap message, got %q", infraExec.Error)
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].Metadata["reason"] != "infra_repick_cap_stalled" {
		t.Errorf("expected alert metadata reason=infra_repick_cap_stalled, got %q", processor.events[0].Metadata["reason"])
	}
}

// TestDispatcher_BeginWithGenerationRetry_DeterministicAndTransientFailures
// is the GH-4586 table-driven acceptance test covering the two prior-claim
// error-class outcomes beginWithGenerationRetry must distinguish: a
// deterministic failure (a "blocked:" hard-guard veto, or any
// IsPermanentFailure-flagged pattern) must be routed straight to the
// operator-attention path (escalateStalledTask) WITHOUT granting a fresh
// generation, while an ordinary transient failure must still be re-picked
// exactly as before this change.
func TestDispatcher_BeginWithGenerationRetry_DeterministicAndTransientFailures(t *testing.T) {
	tests := []struct {
		name           string
		priorError     string
		wantRePicked   bool
		wantAlertCount int
		wantReasonHas  string
	}{
		{
			name:           "deterministic blocked: veto is not re-picked",
			priorError:     "blocked: execution deleted memory doc(s) outside its lane: [foo.md]",
			wantRePicked:   false,
			wantAlertCount: 1,
			wantReasonHas:  "deterministic failure",
		},
		{
			name:           "deterministic IsPermanentFailure pattern is not re-picked",
			priorError:     "PR creation refused: title is not a conventional commit: 'foo'",
			wantRePicked:   false,
			wantAlertCount: 1,
			wantReasonHas:  "deterministic failure",
		},
		{
			name:           "transient failure IS re-picked",
			priorError:     "connection reset by peer while cloning repo",
			wantRePicked:   true,
			wantAlertCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, cleanup := setupTestStore(t)
			defer cleanup()

			task := &Task{ID: "GH-4586-CASE", ProjectPath: "/project-4586", Title: "GH-4586 case task"}
			runner := NewRunner()
			processor := &fakeAlertProcessor{}
			runner.SetAlertProcessor(processor)
			dispatcher := NewDispatcher(store, runner, nil)

			execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
			if err != nil {
				t.Fatalf("setup Begin: %v", err)
			}
			if err := store.UpdateExecutionStatus(execID, "failed", tt.priorError); err != nil {
				t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
			}

			freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
			retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
			if err != nil {
				t.Fatalf("beginWithGenerationRetry: %v", err)
			}

			if tt.wantRePicked && retryExecID == "" {
				t.Fatal("expected the transient failure to be re-picked with a fresh execID, got none")
			}
			if !tt.wantRePicked && retryExecID != "" {
				t.Fatalf("expected the deterministic failure NOT to be re-picked, got fresh execID %q", retryExecID)
			}

			if len(processor.events) != tt.wantAlertCount {
				t.Fatalf("expected %d alert event(s), got %d: %+v", tt.wantAlertCount, len(processor.events), processor.events)
			}

			if !tt.wantRePicked {
				stalledExec, err := store.GetExecution(execID)
				if err != nil {
					t.Fatalf("GetExecution: %v", err)
				}
				if stalledExec.Status != "stalled" {
					t.Errorf("expected the claimed execution to be marked stalled, got status=%q", stalledExec.Status)
				}
				if !strings.Contains(stalledExec.Error, tt.wantReasonHas) {
					t.Errorf("expected reason to contain %q, got %q", tt.wantReasonHas, stalledExec.Error)
				}
				if processor.events[0].Metadata["reason"] != "deterministic_failure_stalled" {
					t.Errorf("expected alert metadata reason=deterministic_failure_stalled, got %q", processor.events[0].Metadata["reason"])
				}
				if genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil {
					t.Fatalf("LatestClaimGeneration: %v", err)
				} else if !found || genCheck != 0 {
					t.Errorf("expected no generation-1 claim for a deterministic failure, found=%v generation=%d", found, genCheck)
				}
			} else {
				if genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil {
					t.Fatalf("LatestClaimGeneration: %v", err)
				} else if !found || genCheck != 1 {
					t.Errorf("expected a generation-1 claim after the transient-failure repick, found=%v generation=%d", found, genCheck)
				}
			}
		})
	}
}

// TestDispatcher_BeginWithGenerationRetry_IdenticalFailureStreakStopsRetrying
// is the GH-4586 acceptance test for the second trigger: independent of
// error class, once the last consecutiveIdenticalFailureThreshold (2)
// generations for the same (task_id, project_path) failed with the exact
// same error string, beginWithGenerationRetry must stop granting fresh
// generations and route to the operator-attention path instead — even
// though neither individual failure matches a known deterministic pattern.
func TestDispatcher_BeginWithGenerationRetry_IdenticalFailureStreakStopsRetrying(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4586-STREAK", ProjectPath: "/project-4586-streak", Title: "Identical-failure-streak task"}
	runner := NewRunner()
	processor := &fakeAlertProcessor{}
	runner.SetAlertProcessor(processor)
	dispatcher := NewDispatcher(store, runner, nil)

	const repeatedErr = "flaky compile error: undefined symbol xyz"

	// Generation 0: a transient-looking failure that does NOT match any
	// deterministic pattern on its own.
	lifecycle := NewExecutionLifecycle(store)
	gen0ExecID, err := lifecycle.Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin gen0: %v", err)
	}
	if err := store.UpdateExecutionStatus(gen0ExecID, "failed", repeatedErr); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}

	// Generation 1: claimed directly (simulating a prior successful repick),
	// failing with the EXACT SAME error string.
	gen1ExecID, err := lifecycle.Begin(task, ExecStatusRunning, 1)
	if err != nil {
		t.Fatalf("setup Begin gen1: %v", err)
	}
	if err := store.UpdateExecutionStatus(gen1ExecID, "failed", repeatedErr); err != nil {
		t.Fatalf("setup: failed to mark generation 1 as failed: %v", err)
	}

	// A third dispatch attempt loses the claim (generation 1 already
	// occupied) and falls into beginWithGenerationRetry, exactly as any
	// re-pick attempt does.
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath, Title: task.Title}
	retryExecID, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}
	if retryExecID != "" {
		t.Fatalf("expected the identical-failure streak to stop retrying, got fresh execID %q", retryExecID)
	}

	if genCheck, _, found, err := store.LatestClaimGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("LatestClaimGeneration: %v", err)
	} else if !found || genCheck != 1 {
		t.Errorf("expected no generation-2 claim once the streak fired, found=%v generation=%d", found, genCheck)
	}

	stalledExec, err := store.GetExecution(gen1ExecID)
	if err != nil {
		t.Fatalf("GetExecution: %v", err)
	}
	if stalledExec.Status != "stalled" {
		t.Errorf("expected the claimed generation-1 execution to be marked stalled, got status=%q", stalledExec.Status)
	}
	if !strings.Contains(stalledExec.Error, "consecutive identical failures") {
		t.Errorf("expected an identical-failure-streak reason string, got %q", stalledExec.Error)
	}
	if !strings.Contains(stalledExec.Error, repeatedErr) {
		t.Errorf("expected the reason to surface the repeated error text, got %q", stalledExec.Error)
	}

	if len(processor.events) != 1 {
		t.Fatalf("expected exactly 1 alert event, got %d: %+v", len(processor.events), processor.events)
	}
	if processor.events[0].Metadata["reason"] != "identical_failure_streak_stalled" {
		t.Errorf("expected alert metadata reason=identical_failure_streak_stalled, got %q", processor.events[0].Metadata["reason"])
	}

	// A subsequent poll tick re-entering beginWithGenerationRetry for the
	// same now-"stalled" claim must stay pinned to the operator-attention
	// path (priorClaimWasEscalatedForOperatorAttention) instead of falling
	// through to the ordinary stall carve-out and minting a free retry.
	retryExecID2, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued)
	if err != nil {
		t.Fatalf("beginWithGenerationRetry (second tick): %v", err)
	}
	if retryExecID2 != "" {
		t.Fatalf("expected the second poll tick to stay pinned to the operator-attention path, got fresh execID %q", retryExecID2)
	}
	if len(processor.events) != 1 {
		t.Fatalf("expected the second poll tick to be idempotent (no new alert), got %d: %+v", len(processor.events), processor.events)
	}
}

// TestRepickBackoffKey_FormatMatchesCmdPilotPackage is the GH-4394 subtask 4
// counterpart to cmd/pilot's TestRepickBackoffKey_FormatMatchesDispatcherPackage.
// cmd/pilot cannot import internal/executor's unexported repickBackoffKey (and
// internal/executor cannot import cmd/pilot without a cycle), so the format is
// duplicated by hand on both sides — see this package's repickBackoffKey doc
// comment. Both pins assert the identical literal "projectPath|taskID"
// format; a future edit to either side alone, without updating the other,
// fails whichever pin didn't move — catching a silent split of the "one
// shared per-task backoff" the poller's outer gate and this package's
// beginWithGenerationRetry both read/write.
func TestRepickBackoffKey_FormatMatchesCmdPilotPackage(t *testing.T) {
	got := repickBackoffKey("/repo/a", "GH-85")
	want := "/repo/a|GH-85"
	if got != want {
		t.Errorf("repickBackoffKey format changed: got %q, want %q — cmd/pilot's repickBackoffKey must be updated identically or the shared backoff store silently splits into two divergent keys", got, want)
	}
}

// TestDispatcher_ExecutionGeneration verifies ExecutionGeneration reports 0
// for an ordinary first attempt and the retry generation once
// beginWithGenerationRetry has claimed one — the signal cmd/pilot's
// handleIssueGeneric uses (GH-4394 subtask 2) to tell a genuine fresh
// dispatch apart from a repick before deciding whether to clear the repick
// backoff.
func TestDispatcher_ExecutionGeneration(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-4394-GEN", ProjectPath: "/project-gen"}
	dispatcher := NewDispatcher(store, NewRunner(), nil)

	if gen, err := dispatcher.ExecutionGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("ExecutionGeneration (no claim yet): %v", err)
	} else if gen != 0 {
		t.Errorf("expected generation 0 with no claim at all, got %d", gen)
	}

	execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup Begin: %v", err)
	}
	if gen, err := dispatcher.ExecutionGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("ExecutionGeneration (generation 0 claimed): %v", err)
	} else if gen != 0 {
		t.Errorf("expected generation 0 for a fresh first attempt, got %d", gen)
	}

	if err := store.UpdateExecutionStatus(execID, "failed"); err != nil {
		t.Fatalf("setup: failed to mark generation 0 as failed: %v", err)
	}
	freshTask := &Task{ID: task.ID, ProjectPath: task.ProjectPath}
	if _, err := dispatcher.beginWithGenerationRetry(freshTask, ExecStatusQueued); err != nil {
		t.Fatalf("beginWithGenerationRetry: %v", err)
	}

	if gen, err := dispatcher.ExecutionGeneration(task.ID, task.ProjectPath); err != nil {
		t.Fatalf("ExecutionGeneration (after re-pick): %v", err)
	} else if gen != 1 {
		t.Errorf("expected generation 1 after a re-pick claimed it, got %d", gen)
	}
}

// TestNextRetryGeneration_DanglingClaimFallsThroughToDoneCheck is the GH-4409
// regression guard for finding #1 in the #4403 review: nextRetryGeneration's
// GetExecution(execID) call can return sql.ErrNoRows for a claim row whose
// executions row was deleted out from under it (Begin's own save-failure
// case, or a future path that deletes a claimed row — mirroring
// deleteOrphanRunningRow — without pruning execution_claims). The old code
// treated ErrNoRows as "still owned" unconditionally, short-circuiting
// before HasTerminalCompletion ever ran — a not-done task behind such a
// dangling claim could never retry, silently, forever. The fix falls
// through to the done-check instead: retry only when the task genuinely
// isn't done yet, preserving GH-4350's "never re-arm a done task" invariant
// for the case where it IS done.
func TestNextRetryGeneration_DanglingClaimFallsThroughToDoneCheck(t *testing.T) {
	t.Run("not done: dangling claim yields a generation+1 retry instead of wedging", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4409-DANGLE-1", ProjectPath: "/project-dangle-1"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}
		// Simulate a future row-delete path (mirroring deleteOrphanRunningRow)
		// deleting the execution row without pruning its execution_claims
		// entry, for a task that is NOT actually done — unlike today's real
		// call sites, which only ever delete after confirming completion.
		if err := store.DeleteExecution(execID); err != nil {
			t.Fatalf("DeleteExecution: %v", err)
		}

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		gen, retry, err := dispatcher.nextRetryGeneration(task.ID, task.ProjectPath)
		if err != nil {
			t.Fatalf("nextRetryGeneration: %v", err)
		}
		if !retry {
			t.Fatalf("expected retry=true for a dangling claim on a not-done task, got retry=false (task permanently wedged)")
		}
		if gen != 1 {
			t.Errorf("expected generation 1, got %d", gen)
		}
	})

	t.Run("done: dangling claim must not re-arm an already-completed task", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		task := &Task{ID: "GH-4409-DANGLE-2", ProjectPath: "/project-dangle-2"}
		execID, err := NewExecutionLifecycle(store).Begin(task, ExecStatusRunning)
		if err != nil {
			t.Fatalf("setup Begin: %v", err)
		}
		// A separate execution row proves the task's real deliverable already
		// shipped (HasCompletedExecution's own-task-id definition of "done")
		// before the claimed row is deleted out from under it.
		if err := store.SaveExecution(&memory.Execution{
			ID: "exec-done-elsewhere", TaskID: task.ID, ProjectPath: task.ProjectPath,
			Status: "completed", PRUrl: "https://github.com/qf-studio/pilot/pull/1",
		}); err != nil {
			t.Fatalf("SaveExecution(done): %v", err)
		}
		if err := store.DeleteExecution(execID); err != nil {
			t.Fatalf("DeleteExecution: %v", err)
		}

		dispatcher := NewDispatcher(store, NewRunner(), nil)
		_, retry, err := dispatcher.nextRetryGeneration(task.ID, task.ProjectPath)
		if err != nil {
			t.Fatalf("nextRetryGeneration: %v", err)
		}
		if retry {
			t.Fatalf("expected retry=false — GH-4350's no_op/done invariant must not be reopened by a dangling claim, got retry=true")
		}
	})
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

// TestRecoverStaleQueuedTasks_WritesExecutionEvent verifies GH-4101: marking
// an orphaned queued task failed also writes an execution_events row, so its
// terminal transition is visible in the audit trail instead of the event
// stream simply stopping.
func TestRecoverStaleQueuedTasks_WritesExecutionEvent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	exec := &memory.Execution{ID: "exec-event-queued", TaskID: "GH-4101-C", ProjectPath: "/project-event-queued", Status: "queued"}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("failed to save execution: %v", err)
	}

	config := &DispatcherConfig{StaleQueuedThreshold: 0}
	dispatcher := NewDispatcher(store, NewRunner(), config)

	dispatcher.recoverStaleQueuedTasks()

	events, err := store.ListExecutionEvents("exec-event-queued")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 execution event, got %d: %+v", len(events), events)
	}
	if events[0].Stage != memory.StageFailed {
		t.Errorf("expected stage %q, got %q", memory.StageFailed, events[0].Stage)
	}
	if !strings.Contains(events[0].Detail, "stale_queued recovered after restart") {
		t.Errorf("expected detail to explain the stale_queued recovery reason, got %q", events[0].Detail)
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
		IsCanary:          true,
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
	// GH-4240: the canary marker must survive the queue round-trip too, or
	// it silently disappears between "task queued" and "task executed".
	if !task.IsCanary {
		t.Error("expected IsCanary to be carried over from execution")
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

// TestDispatchSuccessStage covers the dispatcher's terminal-success mapping:
// a PR produces a pr_created event, a PR-less completion (direct-commit mode)
// has no matching Stage yet and is intentionally left uninstrumented (GH-3846).
func TestDispatchSuccessStage(t *testing.T) {
	tests := []struct {
		name      string
		prURL     string
		wantStage memory.Stage
		wantOK    bool
	}{
		{"pr url present", "https://github.com/test/repo/pull/1", memory.StagePRCreated, true},
		{"no pr url", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stage, ok := dispatchSuccessStage(tt.prURL)
			if ok != tt.wantOK {
				t.Errorf("dispatchSuccessStage(%q) ok = %v, want %v", tt.prURL, ok, tt.wantOK)
			}
			if stage != tt.wantStage {
				t.Errorf("dispatchSuccessStage(%q) stage = %q, want %q", tt.prURL, stage, tt.wantStage)
			}
		})
	}
}

// TestDispatchTerminalStage covers the classified-status → execution_events
// Stage mapping used at the dispatcher's no_op/skipped/infra instrumentation
// site (GH-3846, GH-4101). Stalled is instrumented at its detection site in
// runner.go instead, and declined/rate_limited still have no Stage enum
// equivalent — both must report ok=false rather than a made-up mapping.
func TestDispatchTerminalStage(t *testing.T) {
	tests := []struct {
		status    string
		wantStage memory.Stage
		wantOK    bool
	}{
		{"no_op", memory.StageNoOp, true},
		{"skipped", memory.StageSkipped, true},
		{"stalled", "", false},
		{"declined", "", false},
		{"rate_limited", "", false},
		{"infra", memory.StageFailed, true},
		{"failed", "", false},
		{"completed", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			stage, ok := dispatchTerminalStage(tt.status)
			if ok != tt.wantOK {
				t.Errorf("dispatchTerminalStage(%q) ok = %v, want %v", tt.status, ok, tt.wantOK)
			}
			if stage != tt.wantStage {
				t.Errorf("dispatchTerminalStage(%q) stage = %q, want %q", tt.status, stage, tt.wantStage)
			}
		})
	}
}

// TestProjectWorker_recordExecutionEvent_NilStore verifies recordExecutionEvent
// is a no-op when the worker's store is nil, mirroring the Runner-side guard
// (GH-3846).
func TestProjectWorker_recordExecutionEvent_NilStore(t *testing.T) {
	w := &ProjectWorker{log: slog.New(slog.NewTextHandler(os.Stdout, nil))}
	// Should not panic with nil store
	w.recordExecutionEvent("exec-1", memory.StageRunning, "test detail")
}

// TestProjectWorker_recordExecutionEvent_UnknownExecution verifies the
// GH-4244 validate-first guard: writing an event against an execution ID that
// was never saved logs a warning and writes nothing, instead of surfacing a
// SQLite foreign-key error (FK-787).
func TestProjectWorker_recordExecutionEvent_UnknownExecution(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	w := &ProjectWorker{store: store, log: slog.New(slog.NewTextHandler(os.Stdout, nil))}
	w.recordExecutionEvent("exec-ghost", memory.StageCommit, "commit created: abc1234")

	events, err := store.ListExecutionEvents("exec-ghost")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for an execution row that was never saved, got %d", len(events))
	}
}

// TestDispatcher_recordExecutionEvent_UnknownExecution mirrors the
// ProjectWorker/Runner regression test for the Dispatcher-owned wrapper
// (GH-4244): a stale/unknown execution ID must warn-and-skip, never FK-fail.
func TestDispatcher_recordExecutionEvent_UnknownExecution(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	d := &Dispatcher{store: store, log: slog.New(slog.NewTextHandler(os.Stdout, nil))}
	d.recordExecutionEvent("exec-ghost", memory.StageFailed, "stale_queued recovered after restart")

	events, err := store.ListExecutionEvents("exec-ghost")
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events for an execution row that was never saved, got %d", len(events))
	}
}

// syntheticDispatchBackend is a minimal Backend that always succeeds, used to
// drive a full dispatcher→worker→runner pass without real git/Claude Code
// tooling (GH-3846).
type syntheticDispatchBackend struct{}

func (syntheticDispatchBackend) Name() string      { return "synthetic" }
func (syntheticDispatchBackend) IsAvailable() bool { return true }
func (syntheticDispatchBackend) Execute(_ context.Context, _ ExecuteOptions) (*BackendResult, error) {
	return &BackendResult{Success: true, Output: "synthetic success"}, nil
}

// waitForTerminalStatus polls until the execution leaves "queued"/"running",
// mirroring the polling pattern in TestDispatcher_BootWithQueuedRows_FIFODrainNoStaleReap.
func waitForTerminalStatus(t *testing.T, store *memory.Store, execID string, timeout time.Duration) *memory.Execution {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		exec, err := store.GetExecution(execID)
		if err != nil {
			t.Fatalf("failed to get execution: %v", err)
		}
		if exec.Status != "queued" && exec.Status != "running" {
			return exec
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution %s did not reach a terminal status within %v (last status: %s)", execID, timeout, exec.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestDispatcher_SyntheticDispatch_SuccessEventSequence drives a full
// synthetic dispatch (queue → worker pickup → runner execution → completion)
// through the real dispatcher and runner, and asserts the execution_events
// timeline records the expected cross-file sequence: dispatcher's
// queued→running transition, runner's spec-validated milestone, and (GH-4129)
// the direct-path claude_started/implementation_started pair emitted right
// before the backend invocation. This task has no PR (CreatePR: false), so
// the dispatcher's terminal-success write is a no-op by design (see the
// recordExecutionEvent call site in processQueue) — TestRunner_
// recordExecutionEvent_WritesEvent covers the pr_created write directly.
// GH-3846.
func TestDispatcher_SyntheticDispatch_SuccessEventSequence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunnerWithBackend(syntheticDispatchBackend{})
	runner.skipPreflightChecks = true
	runner.SetLogStore(store)
	runner.SetRecordingEnabled(false)

	dispatcher := NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-SYNTH-OK",
		Title:       "Synthetic dispatch success",
		Description: "GH-3846 synthetic dispatch coverage",
		ProjectPath: t.TempDir(),
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	exec := waitForTerminalStatus(t, store, execID, 10*time.Second)
	if exec.Status != "completed" {
		t.Fatalf("expected status completed, got %q (error: %s)", exec.Status, exec.Error)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}

	wantStages := []memory.Stage{
		memory.StageRunning,
		memory.StageSpecValidated,
		memory.StageClaudeStarted,
		memory.StageImplementationStarted,
	}
	if len(events) != len(wantStages) {
		var gotStages []memory.Stage
		for _, e := range events {
			gotStages = append(gotStages, e.Stage)
		}
		t.Fatalf("got %d events %v, want %d %v", len(events), gotStages, len(wantStages), wantStages)
	}
	for i, want := range wantStages {
		if events[i].Stage != want {
			t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, want)
		}
	}
}

// TestDispatcher_SyntheticDispatch_FailureEventSequence drives a synthetic
// dispatch that fails preflight checks (real Runner, nonexistent project
// path — same fast-fail mechanism TestDispatcher_BootWithQueuedRows_
// FIFODrainNoStaleReap relies on) and asserts the execution_events timeline
// records dispatcher's queued→running transition, runner's spec-validated
// milestone, and dispatcher's terminal-failure transition (GH-3846).
func TestDispatcher_SyntheticDispatch_FailureEventSequence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunner()
	runner.SetLogStore(store)

	dispatcher := NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-SYNTH-FAIL",
		Title:       "Synthetic dispatch failure",
		Description: "GH-3846 synthetic dispatch coverage",
		ProjectPath: "/nonexistent/synthetic-dispatch-path",
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	exec := waitForTerminalStatus(t, store, execID, 10*time.Second)
	if exec.Status != "failed" {
		t.Fatalf("expected status failed, got %q", exec.Status)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}

	wantStages := []memory.Stage{memory.StageRunning, memory.StageSpecValidated, memory.StageFailed}
	if len(events) != len(wantStages) {
		var gotStages []memory.Stage
		for _, e := range events {
			gotStages = append(gotStages, e.Stage)
		}
		t.Fatalf("got %d events %v, want %d %v", len(events), gotStages, len(wantStages), wantStages)
	}
	for i, want := range wantStages {
		if events[i].Stage != want {
			t.Errorf("event[%d].Stage = %q, want %q", i, events[i].Stage, want)
		}
	}
}

// syntheticInfraBackend always fails with an infra-classified error signature
// ("push failed" — see infraErrorSignatures in runner.go), used to drive
// TerminalStatus to classify the outcome as "infra" without a real git/push
// failure.
type syntheticInfraBackend struct{}

func (syntheticInfraBackend) Name() string      { return "synthetic-infra" }
func (syntheticInfraBackend) IsAvailable() bool { return true }
func (syntheticInfraBackend) Execute(_ context.Context, _ ExecuteOptions) (*BackendResult, error) {
	return &BackendResult{Success: false, Error: "push failed: synthetic infra glitch"}, nil
}

// TestDispatcher_SyntheticDispatch_InfraEventSequence verifies GH-4101: an
// infra-classified terminal result (TerminalStatus -> "infra") now writes a
// terminal execution_events row via dispatchTerminalStage. Before this fix,
// infra was the only classified terminal outcome besides declined/rate_limited
// with no Stage mapping, so infra-classified runs produced no terminal event —
// exactly the gap that made the GH-4050 incident's execution_events timeline
// for 5ce9bc2c simply stop with no terminal entry.
func TestDispatcher_SyntheticDispatch_InfraEventSequence(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	runner := NewRunnerWithBackend(syntheticInfraBackend{})
	runner.skipPreflightChecks = true
	runner.SetLogStore(store)
	runner.SetRecordingEnabled(false)

	dispatcher := NewDispatcher(store, runner, nil)
	if err := dispatcher.Start(context.Background()); err != nil {
		t.Fatalf("failed to start dispatcher: %v", err)
	}
	defer dispatcher.Stop()

	task := &Task{
		ID:          "GH-SYNTH-INFRA",
		Title:       "Synthetic dispatch infra failure",
		Description: "GH-4101 synthetic infra classification coverage",
		ProjectPath: t.TempDir(),
	}

	execID, err := dispatcher.QueueTask(context.Background(), task)
	if err != nil {
		t.Fatalf("failed to queue task: %v", err)
	}

	exec := waitForTerminalStatus(t, store, execID, 10*time.Second)
	if exec.Status != "infra" {
		t.Fatalf("expected status infra, got %q (error: %s)", exec.Status, exec.Error)
	}

	events, err := store.ListExecutionEvents(execID)
	if err != nil {
		t.Fatalf("ListExecutionEvents failed: %v", err)
	}

	var gotTerminal bool
	for _, e := range events {
		if e.Stage == memory.StageFailed && strings.Contains(e.Detail, "infra") {
			gotTerminal = true
		}
	}
	if !gotTerminal {
		var gotStages []memory.Stage
		for _, e := range events {
			gotStages = append(gotStages, e.Stage)
		}
		t.Fatalf("expected a terminal StageFailed event mentioning 'infra', got events %v", gotStages)
	}
}

// TestDispatcher_QueueSingleTask_ClaimLostDropsSilently is the dispatcher
// half of GH-4359 (TASK-407 follow-up): when another dispatch channel has
// already claimed (task.ID, task.ProjectPath, generation 0) — e.g. the epic
// sub-issue loop racing the normal dispatch queue for the same task_id —
// queueSingleTask must not surface ErrClaimLost as a queueing failure. It
// drops the duplicate pickup silently: nil error, empty execID, no
// executions row created here (the winning channel already owns one).
func TestDispatcher_QueueSingleTask_ClaimLostDropsSilently(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	task := &Task{ID: "GH-CLAIM-1", ProjectPath: "/project-claim-lost"}

	// Simulate another dispatch channel already winning the claim before
	// this dispatcher's queueSingleTask reaches Begin.
	winnerExecID, err := NewExecutionLifecycle(store).Begin(&Task{ID: task.ID, ProjectPath: task.ProjectPath}, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup: winning Begin failed: %v", err)
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

	execID, err := dispatcher.queueSingleTask(context.Background(), task)
	if err != nil {
		t.Fatalf("expected queueSingleTask to drop ErrClaimLost silently (nil error), got: %v", err)
	}
	if execID != "" {
		t.Errorf("expected empty execID on dropped claim, got %q", execID)
	}
	if task.ExecutionID != "" {
		t.Errorf("expected task.ExecutionID to remain unstamped on dropped claim, got %q", task.ExecutionID)
	}
	if !strings.Contains(buf.String(), "dispatch claim lost") {
		t.Errorf("expected an info log noting the dropped claim, got: %s", buf.String())
	}

	// The winning channel's row must remain the sole executions row for this
	// task — queueSingleTask must not have created a second one.
	exec, err := store.GetExecution(winnerExecID)
	if err != nil {
		t.Fatalf("failed to load winning execution: %v", err)
	}
	if exec.TaskID != task.ID {
		t.Errorf("expected winning execution to belong to %q, got %q", task.ID, exec.TaskID)
	}
}

// TestDispatcher_QueueDecomposedTask_ClaimLostDropsSilently mirrors
// TestDispatcher_QueueSingleTask_ClaimLostDropsSilently for the decomposed
// parent's own Begin call (GH-4359): losing the claim on the parent task
// must not surface as a queueing error, and must not proceed to queue any
// subtasks.
func TestDispatcher_QueueDecomposedTask_ClaimLostDropsSilently(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	parent := &Task{ID: "GH-CLAIM-PARENT", ProjectPath: "/project-claim-lost-parent"}

	winnerExecID, err := NewExecutionLifecycle(store).Begin(&Task{ID: parent.ID, ProjectPath: parent.ProjectPath}, ExecStatusRunning)
	if err != nil {
		t.Fatalf("setup: winning Begin failed: %v", err)
	}

	runner := NewRunner()
	dispatcher := NewDispatcher(store, runner, nil)

	var buf bytes.Buffer
	dispatcher.log = slog.New(slog.NewTextHandler(&buf, nil))

	subtask := &Task{ID: "GH-CLAIM-PARENT-1", ProjectPath: parent.ProjectPath}
	result := &DecomposeResult{Decomposed: true, Subtasks: []*Task{subtask}, Reason: "test"}

	execID, err := dispatcher.queueDecomposedTask(context.Background(), parent, result)
	if err != nil {
		t.Fatalf("expected queueDecomposedTask to drop ErrClaimLost silently (nil error), got: %v", err)
	}
	if execID != "" {
		t.Errorf("expected empty execID on dropped parent claim, got %q", execID)
	}
	if !strings.Contains(buf.String(), "dispatch claim lost") {
		t.Errorf("expected an info log noting the dropped parent claim, got: %s", buf.String())
	}

	// The subtask must not have been queued — its own execution row must
	// not exist.
	if _, err := store.GetExecutionStatusByTaskIDExcluding(subtask.ID, subtask.ProjectPath, ""); err == nil {
		t.Errorf("expected no execution row for subtask %q after parent claim loss, but one exists", subtask.ID)
	}

	exec, err := store.GetExecution(winnerExecID)
	if err != nil {
		t.Fatalf("failed to load winning execution: %v", err)
	}
	if exec.TaskID != parent.ID {
		t.Errorf("expected winning execution to belong to %q, got %q", parent.ID, exec.TaskID)
	}
}
