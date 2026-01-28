package executor

import (
	"testing"
	"time"
)

func TestTaskQueue_Add(t *testing.T) {
	q := NewTaskQueue()

	task := &Task{ID: "TASK-1", Title: "Test task"}
	retryTime := time.Now().Add(1 * time.Hour)

	q.Add(task, retryTime, "rate limited")

	if q.Len() != 1 {
		t.Errorf("Len() = %d, want 1", q.Len())
	}

	// Adding same task should update, not duplicate
	q.Add(task, retryTime.Add(30*time.Minute), "rate limited again")

	if q.Len() != 1 {
		t.Errorf("Len() after duplicate add = %d, want 1", q.Len())
	}

	// Check attempts incremented
	tasks := q.List()
	if tasks[0].Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", tasks[0].Attempts)
	}
}

func TestTaskQueue_GetReady(t *testing.T) {
	q := NewTaskQueue()

	// Add task ready now
	readyTask := &Task{ID: "TASK-1", Title: "Ready task"}
	q.Add(readyTask, time.Now().Add(-1*time.Minute), "was limited")

	// Add task not ready yet
	notReadyTask := &Task{ID: "TASK-2", Title: "Not ready task"}
	q.Add(notReadyTask, time.Now().Add(1*time.Hour), "limited")

	ready := q.GetReady()

	if len(ready) != 1 {
		t.Errorf("GetReady() returned %d tasks, want 1", len(ready))
	}

	if ready[0].Task.ID != "TASK-1" {
		t.Errorf("Ready task ID = %s, want TASK-1", ready[0].Task.ID)
	}

	// Queue should have only the not-ready task
	if q.Len() != 1 {
		t.Errorf("Queue length after GetReady = %d, want 1", q.Len())
	}
}

func TestTaskQueue_GetExpired(t *testing.T) {
	q := NewTaskQueue()

	// Add task with max retries exceeded
	expiredTask := &Task{ID: "TASK-1", Title: "Expired task"}
	q.Add(expiredTask, time.Now().Add(-1*time.Minute), "limited")
	q.Add(expiredTask, time.Now().Add(-1*time.Minute), "limited again")
	q.Add(expiredTask, time.Now().Add(-1*time.Minute), "limited third time")
	q.Add(expiredTask, time.Now().Add(-1*time.Minute), "exceeded") // 4th attempt

	// Add normal task
	normalTask := &Task{ID: "TASK-2", Title: "Normal task"}
	q.Add(normalTask, time.Now().Add(1*time.Hour), "limited")

	expired := q.GetExpired()

	if len(expired) != 1 {
		t.Errorf("GetExpired() returned %d tasks, want 1", len(expired))
	}

	if expired[0].Task.ID != "TASK-1" {
		t.Errorf("Expired task ID = %s, want TASK-1", expired[0].Task.ID)
	}

	// Queue should have only the normal task
	if q.Len() != 1 {
		t.Errorf("Queue length after GetExpired = %d, want 1", q.Len())
	}
}

func TestTaskQueue_Remove(t *testing.T) {
	q := NewTaskQueue()

	task1 := &Task{ID: "TASK-1", Title: "Task 1"}
	task2 := &Task{ID: "TASK-2", Title: "Task 2"}

	q.Add(task1, time.Now().Add(1*time.Hour), "limited")
	q.Add(task2, time.Now().Add(1*time.Hour), "limited")

	removed := q.Remove("TASK-1")
	if !removed {
		t.Error("Remove(TASK-1) returned false, want true")
	}

	if q.Len() != 1 {
		t.Errorf("Queue length after Remove = %d, want 1", q.Len())
	}

	// Try to remove non-existent task
	removed = q.Remove("TASK-999")
	if removed {
		t.Error("Remove(TASK-999) returned true, want false")
	}
}

func TestTaskQueue_NextRetryTime(t *testing.T) {
	q := NewTaskQueue()

	// Empty queue
	nextTime := q.NextRetryTime()
	if !nextTime.IsZero() {
		t.Error("NextRetryTime() on empty queue should return zero time")
	}

	// Add tasks with different retry times
	later := time.Now().Add(2 * time.Hour)
	earlier := time.Now().Add(1 * time.Hour)

	task1 := &Task{ID: "TASK-1", Title: "Later task"}
	task2 := &Task{ID: "TASK-2", Title: "Earlier task"}

	q.Add(task1, later, "limited")
	q.Add(task2, earlier, "limited")

	nextTime = q.NextRetryTime()

	// Should return the earlier time (within 1 second margin)
	diff := nextTime.Sub(earlier)
	if diff < -time.Second || diff > time.Second {
		t.Errorf("NextRetryTime() = %v, want approximately %v", nextTime, earlier)
	}
}

func TestTaskQueue_List(t *testing.T) {
	q := NewTaskQueue()

	task1 := &Task{ID: "TASK-1", Title: "Task 1"}
	task2 := &Task{ID: "TASK-2", Title: "Task 2"}

	q.Add(task1, time.Now().Add(1*time.Hour), "limited")
	q.Add(task2, time.Now().Add(2*time.Hour), "limited")

	list := q.List()

	if len(list) != 2 {
		t.Errorf("List() returned %d items, want 2", len(list))
	}

	// Verify it's a copy (modifying shouldn't affect queue)
	list[0].Attempts = 999
	queueList := q.List()
	if queueList[0].Attempts == 999 {
		t.Error("List() should return a copy, not the original")
	}
}
