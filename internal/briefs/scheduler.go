package briefs

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
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
	// weeklyEntryID is the weekly brief's cron entry; zero when not scheduled.
	weeklyEntryID cron.EntryID
	weeklySpec    string
	logger        *slog.Logger
	store         *memory.Store // nullable for graceful degradation
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

	// The weekly brief rides the same cron (same timezone) at the daily
	// schedule's hour. A schedule it cannot derive from must not stop the
	// daily brief, so that failure only skips the weekly one (GH-5792).
	if spec, err := WeeklySchedule(s.config.Schedule, s.config.WeeklyDay); err != nil {
		s.logger.Warn("weekly brief not scheduled", "error", err)
	} else if id, err := s.cron.AddFunc(spec, func() { s.runWeekly(ctx) }); err != nil {
		s.logger.Warn("weekly brief not scheduled", "schedule", spec, "error", err)
	} else {
		s.weeklyEntryID, s.weeklySpec = id, spec
	}

	s.cron.Start()
	s.running = true

	// Get next run without lock (we already hold it)
	nextRun := s.cron.Entry(s.entryID).Next

	s.logger.Info("brief scheduler started",
		"schedule", s.config.Schedule,
		"timezone", s.config.Timezone,
		"next_run", nextRun,
	)

	if s.weeklySpec != "" {
		s.logger.Info("weekly brief scheduled",
			"schedule", s.weeklySpec,
			"next_run", s.cron.Entry(s.weeklyEntryID).Next,
		)
	}

	// Check if we missed a scheduled brief and catch up if needed
	s.maybeCatchUp(ctx)
	s.maybeCatchUpWeekly(ctx)

	return nil
}

// WeeklyNextRun returns the next scheduled weekly brief, zero when the
// scheduler is stopped or no weekly brief is scheduled.
func (s *Scheduler) WeeklyNextRun() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running || s.weeklySpec == "" {
		return time.Time{}
	}
	return s.cron.Entry(s.weeklyEntryID).Next
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

	s.logResults(results)
}

func (s *Scheduler) logResults(results []DeliveryResult) {
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
	return s.deliverBrief(ctx, brief), nil
}

// RunWeeklyNow triggers an immediate weekly brief for the 7 days ending now.
func (s *Scheduler) RunWeeklyNow(ctx context.Context) ([]DeliveryResult, error) {
	return s.runWeeklyWithResults(ctx)
}

// runWeekly generates and delivers the weekly brief (called by cron).
func (s *Scheduler) runWeekly(ctx context.Context) {
	results, err := s.runWeeklyWithResults(ctx)
	if err != nil {
		s.logger.Error("failed to generate weekly brief", "error", err)
		return
	}
	s.logResults(results)
}

func (s *Scheduler) runWeeklyWithResults(ctx context.Context) ([]DeliveryResult, error) {
	s.logger.Info("generating weekly brief")

	brief, err := s.generator.GenerateWeeklyEnding(time.Now())
	if err != nil {
		return nil, err
	}
	return s.deliverBrief(ctx, brief), nil
}

// deliverBrief stores the console payload, delivers to every channel and
// records successful deliveries under the brief's kind.
func (s *Scheduler) deliverBrief(ctx context.Context, brief *Brief) []DeliveryResult {
	s.logger.Info("brief generated",
		"kind", string(brief.Kind),
		"completed", len(brief.Completed),
		"in_progress", len(brief.InProgress),
		"blocked", len(brief.Blocked),
		"upcoming", len(brief.Upcoming),
	)

	s.storeStructured(brief.Period, brief.Kind)

	results := s.delivery.DeliverAll(ctx, brief)

	// Record successful deliveries to store
	if s.store != nil {
		for _, result := range results {
			if result.Success {
				record := &memory.BriefRecord{
					SentAt:    time.Now(),
					Channel:   result.Channel,
					BriefType: string(brief.Kind),
				}
				if err := s.store.RecordBriefSent(record); err != nil {
					s.logger.Warn("failed to record brief sent", "channel", result.Channel, "error", err)
				}
			}
		}
	}

	return results
}

// storeStructured builds the console's structured brief for the period and
// persists its JSON payload so GET /api/v1/briefs can serve it (GH-5791).
// Weekly briefs carry kind=weekly and the proof numbers (GH-5792).
// Best-effort: a failure is logged and never blocks channel delivery.
func (s *Scheduler) storeStructured(period BriefPeriod, kind BriefKind) {
	if s.store == nil {
		return
	}
	var sb *StructuredBrief
	var err error
	if kind == KindWeekly {
		sb, err = s.generator.GenerateWeeklyStructured(period)
	} else {
		sb, err = s.generator.GenerateStructured(period)
	}
	if err != nil {
		s.logger.Warn("failed to build structured brief", "error", err)
		return
	}
	payload, err := sb.MarshalPayload()
	if err != nil {
		s.logger.Warn("failed to marshal structured brief", "error", err)
		return
	}
	if err := s.store.SaveBriefPayload(&memory.BriefPayload{
		GeneratedAt: sb.GeneratedAt,
		Schema:      sb.Schema,
		Payload:     payload,
	}); err != nil {
		s.logger.Warn("failed to store structured brief", "error", err)
	}
}

// maybeCatchUp checks if a scheduled brief was missed and fires one if needed.
// A brief is considered missed if the last sent time is before the previous scheduled run time.
func (s *Scheduler) maybeCatchUp(ctx context.Context) {
	s.catchUp(s.config.Schedule, string(KindDaily), 48*time.Hour, false, func() { s.runBrief(ctx) })
}

// maybeCatchUpWeekly is maybeCatchUp for the weekly brief: a restart across
// the weekly slot still sends it, looking back just over a week. It only
// fires when a weekly brief was sent before, so enabling it never produces an
// off-day brief on the first restart.
func (s *Scheduler) maybeCatchUpWeekly(ctx context.Context) {
	if s.weeklySpec == "" {
		return
	}
	s.catchUp(s.weeklySpec, string(KindWeekly), 8*24*time.Hour, true, func() { s.runWeekly(ctx) })
}

// catchUp fires run when the last brief of briefType was sent before the most
// recent scheduled time of spec within lookback. With requirePrior, a brief
// that was never sent is not caught up.
func (s *Scheduler) catchUp(spec, briefType string, lookback time.Duration, requirePrior bool, run func()) {
	if s.store == nil {
		s.logger.Info("catch-up skipped: no store configured", "kind", briefType)
		return
	}

	// Get the most recent brief sent for any channel
	// We use "telegram" as a representative channel since it's the primary delivery mechanism
	lastRecord, err := s.store.GetLastBriefSent("telegram", briefType)
	if err != nil {
		s.logger.Warn("catch-up: failed to get last brief sent", "kind", briefType, "error", err)
		return
	}

	// Parse the cron schedule to determine the previous scheduled run
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(spec)
	if err != nil {
		s.logger.Warn("catch-up: failed to parse schedule", "schedule", spec, "error", err)
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

	// Find the previous scheduled time by checking what the next run would be from
	// lookback ago then step forward until we find the most recent past scheduled time
	checkTime := nowInTz.Add(-lookback)
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
		s.logger.Info("catch-up: no previous scheduled time found", "kind", briefType)
		return
	}
	if requirePrior && lastRecord == nil {
		s.logger.Info("catch-up: skipped, no earlier brief on record", "kind", briefType)
		return
	}

	// Check if we missed the brief
	if lastRecord == nil || lastRecord.SentAt.Before(prevScheduled) {
		lastSentStr := "never"
		if lastRecord != nil {
			lastSentStr = lastRecord.SentAt.Format(time.RFC3339)
		}
		s.logger.Info("catch-up: missed brief detected, firing now",
			"kind", briefType,
			"last_sent", lastSentStr,
			"prev_scheduled", prevScheduled.Format(time.RFC3339),
		)
		run()
	} else {
		s.logger.Info("catch-up: no missed brief",
			"kind", briefType,
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
