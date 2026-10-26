package briefs

import (
	"fmt"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// Generator creates daily briefs from execution data
type Generator struct {
	store  *memory.Store
	config *BriefConfig
	pool   PoolSource
	// clock reports the current time; nil means time.Now. Tests set it via
	// SetClock so generated-at stamps are deterministic.
	clock func() time.Time
}

// PoolSource reports the percent of the included allowance used, and false
// when the daemon does not know it (GH-5791).
type PoolSource func() (percent float64, ok bool)

// SetPoolSource wires the allowance reader for the structured brief's pool
// section. Without one, the section is null.
func (g *Generator) SetPoolSource(p PoolSource) {
	g.pool = p
}

// SetClock overrides the generator's time source (nil restores time.Now).
// Intended for tests that assert on the generated-at date.
func (g *Generator) SetClock(clock func() time.Time) {
	g.clock = clock
}

// now returns the current time from the injected clock, else the wall clock.
func (g *Generator) now() time.Time {
	if g.clock != nil {
		return g.clock()
	}
	return time.Now()
}

// NewGenerator creates a new brief generator
func NewGenerator(store *memory.Store, config *BriefConfig) *Generator {
	if config == nil {
		config = DefaultBriefConfig()
	}
	return &Generator{
		store:  store,
		config: config,
	}
}

// DefaultBriefConfig returns default brief configuration
func DefaultBriefConfig() *BriefConfig {
	return &BriefConfig{
		Enabled:  false,
		Schedule: "0 9 * * 1-5", // 9 AM weekdays
		Timezone: "America/New_York",
		Channels: []ChannelConfig{},
		Content: ContentConfig{
			IncludeMetrics:     true,
			IncludeErrors:      true,
			MaxItemsPerSection: 10,
		},
		Filters: FilterConfig{
			Projects: []string{},
		},
	}
}

// Generate creates a brief for the specified period
func (g *Generator) Generate(period BriefPeriod) (*Brief, error) {
	query := memory.BriefQuery{
		Start:    period.Start,
		End:      period.End,
		Projects: g.config.Filters.Projects,
	}

	// Get all executions in period
	executions, err := g.store.GetExecutionsInPeriod(query)
	if err != nil {
		return nil, err
	}

	// Get active executions
	activeExecs, err := g.store.GetActiveExecutions()
	if err != nil {
		return nil, err
	}

	// Get queued tasks
	queuedExecs, err := g.store.GetQueuedTasks(g.config.Content.MaxItemsPerSection)
	if err != nil {
		return nil, err
	}

	// Get metrics
	metricsData, err := g.store.GetBriefMetrics(query)
	if err != nil {
		return nil, err
	}

	brief := &Brief{
		Kind:        KindDaily,
		GeneratedAt: g.now(),
		Period:      period,
		Completed:   []TaskSummary{},
		InProgress:  []TaskSummary{},
		Blocked:     []BlockedTask{},
		Upcoming:    []TaskSummary{},
		Metrics:     convertMetrics(metricsData),
	}

	// First pass: collect completed task IDs to filter out retried failures
	completedTaskIDs := make(map[string]bool)
	for _, exec := range executions {
		if exec.Status == "completed" {
			completedTaskIDs[exec.TaskID] = true
		}
	}

	// Second pass: categorize executions
	for _, exec := range executions {
		title := exec.TaskTitle
		if title == "" {
			title = exec.TaskID // Fallback to task ID if no title
		}
		summary := TaskSummary{
			ID:          exec.TaskID,
			Title:       title,
			ProjectPath: exec.ProjectPath,
			Status:      exec.Status,
			PRUrl:       exec.PRUrl,
			DurationMs:  exec.DurationMs,
			CompletedAt: exec.CompletedAt,
		}

		switch exec.Status {
		case "completed":
			if len(brief.Completed) < g.config.Content.MaxItemsPerSection {
				brief.Completed = append(brief.Completed, summary)
			}
		case "failed":
			// Skip failures for tasks that were later completed successfully
			if completedTaskIDs[exec.TaskID] {
				continue
			}
			if g.config.Content.IncludeErrors && len(brief.Blocked) < g.config.Content.MaxItemsPerSection {
				blocked := BlockedTask{
					TaskSummary: summary,
					Error:       exec.Error,
				}
				if exec.CompletedAt != nil {
					blocked.FailedAt = *exec.CompletedAt
				}
				brief.Blocked = append(brief.Blocked, blocked)
			}
		}
	}

	// Add active executions as in-progress
	for _, exec := range activeExecs {
		if len(brief.InProgress) >= g.config.Content.MaxItemsPerSection {
			break
		}
		brief.InProgress = append(brief.InProgress, TaskSummary{
			ID:          exec.TaskID,
			Title:       exec.TaskID,
			ProjectPath: exec.ProjectPath,
			Status:      exec.Status,
			Progress:    estimateProgress(exec),
		})
	}

	// Add queued tasks as upcoming
	for _, exec := range queuedExecs {
		if len(brief.Upcoming) >= g.config.Content.MaxItemsPerSection {
			break
		}
		brief.Upcoming = append(brief.Upcoming, TaskSummary{
			ID:          exec.TaskID,
			Title:       exec.TaskID,
			ProjectPath: exec.ProjectPath,
			Status:      exec.Status,
		})
	}

	return brief, nil
}

// GenerateDaily creates a brief for the period since the previous daily brief
// was sent, ending now (GH-5813).
func (g *Generator) GenerateDaily() (*Brief, error) {
	return g.GenerateDailyEnding(g.now())
}

// GenerateDailyEnding creates the daily brief for [previous daily brief sent,
// end], or [end - 24h, end] when none was sent. The period ends at the run
// time rather than a fixed hour, so a run completed between two slots lands in
// the next brief whatever the schedule is. Times are in the configured timezone.
func (g *Generator) GenerateDailyEnding(end time.Time) (*Brief, error) {
	loc, err := time.LoadLocation(g.config.Timezone)
	if err != nil {
		loc = time.UTC
	}
	end = end.In(loc)

	start := end.Add(-24 * time.Hour)
	last, err := g.store.GetLastBriefSentAny(string(KindDaily))
	if err != nil {
		return nil, err
	}
	// A record at or after end (clock skew, manual re-run in the same instant)
	// would give an empty or inverted window, so it keeps the 24h fallback.
	if last != nil && last.SentAt.Before(end) {
		start = last.SentAt.In(loc)
	}

	return g.Generate(BriefPeriod{Start: start, End: end})
}

// GenerateWeeklyEnding creates the weekly brief for the 7 days ending at end:
// the daily sections plus the weekly proof numbers (GH-5792).
func (g *Generator) GenerateWeeklyEnding(end time.Time) (*Brief, error) {
	period := BriefPeriod{Start: end.Add(-7 * 24 * time.Hour), End: end}
	brief, err := g.Generate(period)
	if err != nil {
		return nil, err
	}
	proof, err := g.weeklyProof(period, nil)
	if err != nil {
		return nil, err
	}
	brief.Kind = KindWeekly
	brief.Weekly = proof
	return brief, nil
}

// weeklyProof reads the window's ledger and computes the proof numbers. When
// shipped is nil the shipped runs are built from the same executions.
func (g *Generator) weeklyProof(period BriefPeriod, shipped []ShippedRun) (*WeeklyProof, error) {
	in, err := g.store.GetWeeklyProofInputs(memory.BriefQuery{
		Start:    period.Start,
		End:      period.End,
		Projects: g.config.Filters.Projects,
	})
	if err != nil {
		return nil, err
	}
	if shipped == nil {
		shipped = BuildStructured(g.now(), period, in.Shipped, nil, nil).Shipped
	}
	return BuildWeeklyProof(in, shipped), nil
}

// GenerateWeeklyStructured builds the console brief for a weekly period:
// the structured sections stamped kind=weekly with the proof numbers.
func (g *Generator) GenerateWeeklyStructured(period BriefPeriod) (*StructuredBrief, error) {
	sb, err := g.GenerateStructured(period)
	if err != nil {
		return nil, err
	}
	proof, err := g.weeklyProof(period, sb.Shipped)
	if err != nil {
		return nil, err
	}
	sb.Kind = KindWeekly
	sb.Weekly = proof
	return sb, nil
}

// GenerateWeekly creates the weekly brief for the 7 days ending now, the same
// window the scheduled weekly brief uses at fire time.
func (g *Generator) GenerateWeekly() (*Brief, error) {
	return g.GenerateWeeklyEnding(g.now())
}

// StoreStructured builds the console's structured brief for the period and
// persists its JSON payload so GET /api/v1/briefs can serve it (GH-5791).
// Weekly briefs carry kind=weekly and the proof numbers (GH-5792). It is a
// no-op without a store.
func (g *Generator) StoreStructured(period BriefPeriod, kind BriefKind) error {
	if g.store == nil {
		return nil
	}
	var sb *StructuredBrief
	var err error
	if kind == KindWeekly {
		sb, err = g.GenerateWeeklyStructured(period)
	} else {
		sb, err = g.GenerateStructured(period)
	}
	if err != nil {
		return fmt.Errorf("build structured brief: %w", err)
	}
	payload, err := sb.MarshalPayload()
	if err != nil {
		return fmt.Errorf("marshal structured brief: %w", err)
	}
	if err := g.store.SaveBriefPayload(&memory.BriefPayload{
		GeneratedAt: sb.GeneratedAt,
		Schema:      sb.Schema,
		Kind:        string(sb.Kind),
		Payload:     payload,
	}); err != nil {
		return fmt.Errorf("store structured brief: %w", err)
	}
	return nil
}

// convertMetrics converts database metrics to brief metrics
func convertMetrics(data *memory.BriefMetricsData) BriefMetrics {
	return BriefMetrics{
		TotalTasks:       data.TotalTasks,
		CompletedCount:   data.CompletedCount,
		FailedCount:      data.FailedCount,
		SuccessRate:      data.SuccessRate,
		AvgDurationMs:    data.AvgDurationMs,
		PRsCreated:       data.PRsCreated,
		TotalTokensUsed:  data.TotalTokensUsed,
		EstimatedCostUSD: data.EstimatedCostUSD,
	}
}

// estimateProgress estimates task progress based on duration
func estimateProgress(exec *memory.Execution) int {
	if exec.DurationMs == 0 {
		return 10 // Just started
	}
	// Assume average task takes 5 minutes
	avgDurationMs := int64(5 * 60 * 1000)
	progress := int((exec.DurationMs * 100) / avgDurationMs)
	if progress > 95 {
		progress = 95 // Cap at 95% until actually complete
	}
	if progress < 10 {
		progress = 10
	}
	return progress
}

// GenerateStructured builds the StructuredBrief for the period: terminal
// executions (shipped / failed), pending approvals (review_needed) and the
// pool usage when a PoolSource is wired (GH-5791).
func (g *Generator) GenerateStructured(period BriefPeriod) (*StructuredBrief, error) {
	execs, err := g.store.GetExecutionsForReceipts(memory.BriefQuery{
		Start:    period.Start,
		End:      period.End,
		Projects: g.config.Filters.Projects,
	})
	if err != nil {
		return nil, err
	}

	pending, err := g.store.LoadPendingApprovals()
	if err != nil {
		return nil, err
	}
	now := g.now()
	var reviews []ReviewInput
	for _, p := range pending {
		if p.ExpiresAt.Before(now) {
			continue // expired, not yet pruned
		}
		in := ReviewInput{
			IssueRef:     strings.TrimPrefix(p.TaskID, "GH-"),
			Title:        p.Title,
			Reason:       p.Description,
			WaitingSince: p.CreatedAt,
		}
		if strings.HasPrefix(p.TaskID, "GH-") {
			in.IssueRef = "#" + in.IssueRef
		}
		if in.Reason == "" {
			in.Reason = "Awaiting approval: " + p.Stage
		}
		// The execution join is best-effort (see Store.SetApprovalRequestID).
		if exec, err := g.store.GetExecutionByApprovalRequestID(p.ID); err == nil && exec != nil {
			in.PRURL = exec.PRUrl
		}
		reviews = append(reviews, in)
	}

	var poolPercent *float64
	if g.pool != nil {
		if pct, ok := g.pool(); ok {
			poolPercent = &pct
		}
	}

	// Render every timestamp in the tenant's zone (GH-5814).
	loc := briefLocation(g.config.Timezone)
	period = BriefPeriod{Start: period.Start.In(loc), End: period.End.In(loc)}
	return BuildStructured(now.In(loc), period, execs, reviews, poolPercent), nil
}
