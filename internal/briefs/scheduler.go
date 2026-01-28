package briefs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Scheduler manages scheduled brief generation and delivery
type Scheduler struct {
	generator *Generator
	delivery  *DeliveryService
	config    *BriefConfig
	cron      *cron.Cron
	mu        sync.Mutex
	running   bool
	entryID   cron.EntryID
	logger    *slog.Logger
}

// NewScheduler creates a new brief scheduler
func NewScheduler(generator *Generator, delivery *DeliveryService, config *BriefConfig, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}

	loc, err := time.LoadLocation(config.Timezone)
	if err != nil {
		logger.Warn("invalid timezone, using UTC", "timezone", config.Timezone, "error", err)
		loc = time.UTC
	}

	return &Scheduler{
		generator: generator,
		delivery:  delivery,
		config:    config,
		cron:      cron.New(cron.WithLocation(loc)),
		logger:    logger,
	}
}

// Start begins the scheduler
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return nil
	}

	if !s.config.Enabled {
		s.logger.Info("brief scheduler disabled")
		return nil
	}

	// Add the scheduled job
	entryID, err := s.cron.AddFunc(s.config.Schedule, func() {
		s.runBrief(ctx)
	})
	if err != nil {
		return err
	}

	s.entryID = entryID
	s.cron.Start()
	s.running = true

	// Get next run without lock (we already hold it)
	nextRun := s.cron.Entry(s.entryID).Next

	s.logger.Info("brief scheduler started",
		"schedule", s.config.Schedule,
		"timezone", s.config.Timezone,
		"next_run", nextRun,
	)

	return nil
}

// Stop stops the scheduler
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	ctx := s.cron.Stop()
	<-ctx.Done()
	s.running = false
	s.logger.Info("brief scheduler stopped")
}

// NextRun returns the next scheduled run time
func (s *Scheduler) NextRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return time.Time{}
	}

	entry := s.cron.Entry(s.entryID)
	return entry.Next
}

// LastRun returns the last run time
func (s *Scheduler) LastRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return time.Time{}
	}

	entry := s.cron.Entry(s.entryID)
	return entry.Prev
}

// IsRunning returns whether the scheduler is active
func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// RunNow triggers an immediate brief generation and delivery
func (s *Scheduler) RunNow(ctx context.Context) ([]DeliveryResult, error) {
	return s.runBriefWithResults(ctx)
}

// runBrief generates and delivers a brief (called by cron)
func (s *Scheduler) runBrief(ctx context.Context) {
	results, err := s.runBriefWithResults(ctx)
	if err != nil {
		s.logger.Error("failed to generate brief", "error", err)
		return
	}

	for _, result := range results {
		if result.Success {
			s.logger.Info("brief delivered",
				"channel", result.Channel,
				"message_id", result.MessageID,
			)
		} else {
			s.logger.Error("brief delivery failed",
				"channel", result.Channel,
				"error", result.Error,
			)
		}
	}
}

// runBriefWithResults generates and delivers a brief, returning results
func (s *Scheduler) runBriefWithResults(ctx context.Context) ([]DeliveryResult, error) {
	s.logger.Info("generating daily brief")

	brief, err := s.generator.GenerateDaily()
	if err != nil {
		return nil, err
	}

	s.logger.Info("brief generated",
		"completed", len(brief.Completed),
		"in_progress", len(brief.InProgress),
		"blocked", len(brief.Blocked),
		"upcoming", len(brief.Upcoming),
	)

	results := s.delivery.DeliverAll(ctx, brief)
	return results, nil
}

// Status returns scheduler status information
func (s *Scheduler) Status() SchedulerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	status := SchedulerStatus{
		Enabled:  s.config.Enabled,
		Running:  s.running,
		Schedule: s.config.Schedule,
		Timezone: s.config.Timezone,
	}

	if s.running {
		entry := s.cron.Entry(s.entryID)
		status.NextRun = entry.Next
		status.LastRun = entry.Prev
	}

	return status
}

// SchedulerStatus holds scheduler status information
type SchedulerStatus struct {
	Enabled  bool
	Running  bool
	Schedule string
	Timezone string
	NextRun  time.Time
	LastRun  time.Time
}
