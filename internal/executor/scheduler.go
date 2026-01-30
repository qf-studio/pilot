package executor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// SchedulerConfig configures retry scheduler behavior
type SchedulerConfig struct {
	// CheckInterval is how often to check for ready tasks
	CheckInterval time.Duration
	// RetryBuffer adds extra time before retry to ensure limit has reset
	RetryBuffer time.Duration
}

// DefaultSchedulerConfig returns default scheduler settings
func DefaultSchedulerConfig() *SchedulerConfig {
	return &SchedulerConfig{
		CheckInterval: 1 * time.Minute,
		RetryBuffer:   5 * time.Minute, // Add 5 min buffer after stated reset
	}
}

// RetryCallback is called when a task is ready for retry
type RetryCallback func(ctx context.Context, task *PendingTask) error

// ExpiredCallback is called when a task has exceeded max retries
type ExpiredCallback func(ctx context.Context, task *PendingTask)

// Scheduler manages background retry of rate-limited tasks
type Scheduler struct {
	config    *SchedulerConfig
	queue     *TaskQueue
	onRetry   RetryCallback
	onExpired ExpiredCallback
	log       *slog.Logger

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// NewScheduler creates a new retry scheduler
func NewScheduler(config *SchedulerConfig, queue *TaskQueue) *Scheduler {
	if config == nil {
		config = DefaultSchedulerConfig()
	}
	if queue == nil {
		queue = NewTaskQueue()
	}

	return &Scheduler{
		config: config,
		queue:  queue,
		log:    logging.WithComponent("scheduler"),
	}
}

// SetRetryCallback sets the callback for when tasks are ready to retry
func (s *Scheduler) SetRetryCallback(cb RetryCallback) {
	s.onRetry = cb
}

// SetExpiredCallback sets the callback for when tasks exceed max retries
func (s *Scheduler) SetExpiredCallback(cb ExpiredCallback) {
	s.onExpired = cb
}

// Queue returns the underlying task queue
func (s *Scheduler) Queue() *TaskQueue {
	return s.queue
}

// Start begins the scheduler loop
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.mu.Unlock()

	s.log.Info("Scheduler started",
		slog.Duration("check_interval", s.config.CheckInterval),
		slog.Duration("retry_buffer", s.config.RetryBuffer),
	)

	go s.run(ctx)
	return nil
}

// Stop gracefully stops the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	s.mu.Unlock()

	// Wait for run loop to finish
	<-s.doneCh
	s.log.Info("Scheduler stopped")
}

// IsRunning returns whether the scheduler is active
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// run is the main scheduler loop
func (s *Scheduler) run(ctx context.Context) {
	defer close(s.doneCh)

	ticker := time.NewTicker(s.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkTasks(ctx)
		}
	}
}

// checkTasks processes ready and expired tasks
func (s *Scheduler) checkTasks(ctx context.Context) {
	// Handle expired tasks first
	expired := s.queue.GetExpired()
	for _, task := range expired {
		s.log.Warn("Task exceeded max retries",
			slog.String("task_id", task.Task.ID),
			slog.Int("attempts", task.Attempts),
		)
		if s.onExpired != nil {
			s.onExpired(ctx, task)
		}
	}

	// Handle ready tasks
	ready := s.queue.GetReady()
	for _, task := range ready {
		s.log.Info("Retrying rate-limited task",
			slog.String("task_id", task.Task.ID),
			slog.Int("attempt", task.Attempts),
			slog.Duration("wait_time", time.Since(task.QueuedAt)),
		)

		if s.onRetry != nil {
			if err := s.onRetry(ctx, task); err != nil {
				s.log.Error("Retry failed",
					slog.String("task_id", task.Task.ID),
					slog.Any("error", err),
				)
				// Re-queue if rate limited again will be handled by the retry callback
			}
		}
	}
}

// QueueTask adds a task to the retry queue with rate limit info
func (s *Scheduler) QueueTask(task *Task, rl *RateLimitInfo) {
	// Add buffer to retry time
	retryAt := rl.ResetTime.Add(s.config.RetryBuffer)

	s.queue.Add(task, retryAt, rl.RawError)

	s.log.Info("Task queued for retry",
		slog.String("task_id", task.ID),
		slog.Time("reset_time", rl.ResetTime),
		slog.Time("retry_at", retryAt),
		slog.String("timezone", rl.Timezone),
	)
}

// Status returns a summary of the scheduler state
func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()

	pending := s.queue.List()
	nextRetry := s.queue.NextRetryTime()

	return SchedulerStatus{
		Running:      running,
		PendingCount: len(pending),
		PendingTasks: pending,
		NextRetry:    nextRetry,
	}
}

// SchedulerStatus provides scheduler state information
type SchedulerStatus struct {
	Running      bool
	PendingCount int
	PendingTasks []PendingTask
	NextRetry    time.Time
}
