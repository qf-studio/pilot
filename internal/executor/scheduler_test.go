package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestScheduler_StartStop(t *testing.T) {
	config := &SchedulerConfig{
		CheckInterval: 100 * time.Millisecond,
		RetryBuffer:   0,
	}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	if s.IsRunning() {
		t.Error("Scheduler should not be running before Start()")
	}

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if !s.IsRunning() {
		t.Error("Scheduler should be running after Start()")
	}

	// Starting again should be idempotent
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() again error = %v", err)
	}

	s.Stop()

	if s.IsRunning() {
		t.Error("Scheduler should not be running after Stop()")
	}
}

func TestScheduler_QueueTask(t *testing.T) {
	config := &SchedulerConfig{
		CheckInterval: 100 * time.Millisecond,
		RetryBuffer:   5 * time.Minute,
	}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	task := &Task{ID: "TASK-1", Title: "Test task"}
	rl := &RateLimitInfo{
		ResetTime: time.Now().Add(1 * time.Hour),
		Timezone:  "UTC",
		RawError:  "rate limited",
	}

	s.QueueTask(task, rl)

	if queue.Len() != 1 {
		t.Errorf("Queue length = %d, want 1", queue.Len())
	}

	// Check retry time has buffer added
	tasks := queue.List()
	expectedRetry := rl.ResetTime.Add(config.RetryBuffer)
	diff := tasks[0].RetryAfter.Sub(expectedRetry)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("RetryAfter = %v, want %v (with buffer)", tasks[0].RetryAfter, expectedRetry)
	}
}

func TestScheduler_RetryCallback(t *testing.T) {
	config := &SchedulerConfig{
		CheckInterval: 50 * time.Millisecond,
		RetryBuffer:   0,
	}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	var mu sync.Mutex
	var retriedTask *PendingTask

	s.SetRetryCallback(func(ctx context.Context, task *PendingTask) error {
		mu.Lock()
		retriedTask = task
		mu.Unlock()
		return nil
	})

	// Add a task that's ready for retry
	task := &Task{ID: "TASK-1", Title: "Test task"}
	queue.Add(task, time.Now().Add(-1*time.Minute), "was limited")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for scheduler to process
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	mu.Lock()
	if retriedTask == nil {
		t.Error("Retry callback was not called")
	} else if retriedTask.Task.ID != "TASK-1" {
		t.Errorf("Retried task ID = %s, want TASK-1", retriedTask.Task.ID)
	}
	mu.Unlock()
}

func TestScheduler_ExpiredCallback(t *testing.T) {
	config := &SchedulerConfig{
		CheckInterval: 50 * time.Millisecond,
		RetryBuffer:   0,
	}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	var mu sync.Mutex
	var expiredTask *PendingTask

	s.SetExpiredCallback(func(ctx context.Context, task *PendingTask) {
		mu.Lock()
		expiredTask = task
		mu.Unlock()
	})

	// Add a task that exceeds max retries
	task := &Task{ID: "TASK-1", Title: "Test task"}
	queue.Add(task, time.Now().Add(-1*time.Minute), "limited 1")
	queue.Add(task, time.Now().Add(-1*time.Minute), "limited 2")
	queue.Add(task, time.Now().Add(-1*time.Minute), "limited 3")
	queue.Add(task, time.Now().Add(-1*time.Minute), "limited 4") // exceeds max

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Wait for scheduler to process
	time.Sleep(200 * time.Millisecond)
	s.Stop()

	mu.Lock()
	if expiredTask == nil {
		t.Error("Expired callback was not called")
	} else if expiredTask.Task.ID != "TASK-1" {
		t.Errorf("Expired task ID = %s, want TASK-1", expiredTask.Task.ID)
	}
	mu.Unlock()
}

func TestScheduler_Status(t *testing.T) {
	config := &SchedulerConfig{
		CheckInterval: 100 * time.Millisecond,
		RetryBuffer:   0,
	}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	// Add some tasks
	task1 := &Task{ID: "TASK-1", Title: "Task 1"}
	task2 := &Task{ID: "TASK-2", Title: "Task 2"}

	later := time.Now().Add(2 * time.Hour)
	earlier := time.Now().Add(1 * time.Hour)

	queue.Add(task1, later, "limited")
	queue.Add(task2, earlier, "limited")

	status := s.Status()

	if status.Running {
		t.Error("Status.Running should be false before Start()")
	}

	if status.PendingCount != 2 {
		t.Errorf("Status.PendingCount = %d, want 2", status.PendingCount)
	}

	if len(status.PendingTasks) != 2 {
		t.Errorf("Status.PendingTasks has %d items, want 2", len(status.PendingTasks))
	}

	// NextRetry should be the earlier time
	diff := status.NextRetry.Sub(earlier)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("Status.NextRetry = %v, want approximately %v", status.NextRetry, earlier)
	}
}

func TestScheduler_ContextCancellation(t *testing.T) {
	config := &SchedulerConfig{
		CheckInterval: 50 * time.Millisecond,
		RetryBuffer:   0,
	}
	queue := NewTaskQueue()
	s := NewScheduler(config, queue)

	ctx, cancel := context.WithCancel(context.Background())

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if !s.IsRunning() {
		t.Error("Scheduler should be running")
	}

	// Cancel context
	cancel()

	// Give scheduler time to notice cancellation
	time.Sleep(100 * time.Millisecond)

	// Scheduler should still report running until Stop() is called
	// but its internal loop should have exited
	s.Stop()

	if s.IsRunning() {
		t.Error("Scheduler should not be running after Stop()")
	}
}
