package briefs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
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
	store     *memory.Store // nullable for graceful degradation
}

// NewScheduler creates a new brief scheduler.
// The store parameter is optional (nullable) for graceful degradation.
func NewScheduler(generator *Generator, delivery *DeliveryService, config *BriefConfig, logger *slog.Logger, store *memory.Store) *Scheduler {
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
		store:     store,
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

	// Check if we missed a scheduled brief and catch up if needed
	s.maybeCatchUp(ctx)

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

	// Record successful deliveries to store
	if s.store != nil {
		for _, result := range results {
			if result.Success {
				record := &memory.BriefRecord{
					SentAt:    time.Now(),
					Channel:   result.Channel,
					BriefType: "daily",
				}
				if err := s.store.RecordBriefSent(record); err != nil {
					s.logger.Warn("failed to record brief sent", "channel", result.Channel, "error", err)
				}
			}
		}
	}

	return results, nil
}

// maybeCatchUp checks if a scheduled brief was missed and fires one if needed.
// A brief is considered missed if the last sent time is before the previous scheduled run time.
func (s *Scheduler) maybeCatchUp(ctx context.Context) {
	if s.store == nil {
		s.logger.Info("catch-up skipped: no store configured")
		return
	}

	// Get the most recent brief sent for any channel
	// We use "telegram" as a representative channel since it's the primary delivery mechanism
	lastRecord, err := s.store.GetLastBriefSent("telegram")
	if err != nil {
		s.logger.Warn("catch-up: failed to get last brief sent", "error", err)
		return
	}

	// Parse the cron schedule to determine the previous scheduled run
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(s.config.Schedule)
	if err != nil {
		s.logger.Warn("catch-up: failed to parse schedule", "schedule", s.config.Schedule, "error", err)
		return
	}

	// Calculate the previous scheduled run time
	// We iterate backwards from now to find the most recent scheduled time
	now := time.Now()
	loc, _ := time.LoadLocation(s.config.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	nowInTz := now.In(loc)

	// Find the previous scheduled time by checking what the next run would be from 48 hours ago
	// then step forward until we find the most recent past scheduled time
	checkTime := nowInTz.Add(-48 * time.Hour)
	var prevScheduled time.Time
	for {
		nextRun := schedule.Next(checkTime)
		if nextRun.After(nowInTz) {
			break
		}
		prevScheduled = nextRun
		checkTime = nextRun
	}

	if prevScheduled.IsZero() {
		s.logger.Info("catch-up: no previous scheduled time found")
		return
	}

	// Check if we missed the brief
	if lastRecord == nil || lastRecord.SentAt.Before(prevScheduled) {
		lastSentStr := "never"
		if lastRecord != nil {
			lastSentStr = lastRecord.SentAt.Format(time.RFC3339)
		}
		s.logger.Info("catch-up: missed brief detected, firing now",
			"last_sent", lastSentStr,
			"prev_scheduled", prevScheduled.Format(time.RFC3339),
		)
		s.runBrief(ctx)
	} else {
		s.logger.Info("catch-up: no missed brief",
			"last_sent", lastRecord.SentAt.Format(time.RFC3339),
			"prev_scheduled", prevScheduled.Format(time.RFC3339),
		)
	}
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
