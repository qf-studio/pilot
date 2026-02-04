package briefs

import (
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// Generator creates daily briefs from execution data
type Generator struct {
	store  *memory.Store
	config *BriefConfig
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
		GeneratedAt: time.Now(),
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

// GenerateDaily creates a brief for the previous 24 hours
func (g *Generator) GenerateDaily() (*Brief, error) {
	loc, err := time.LoadLocation(g.config.Timezone)
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	// Brief covers yesterday 9 AM to today 9 AM (or configured time)
	end := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, loc)
	start := end.Add(-24 * time.Hour)

	return g.Generate(BriefPeriod{Start: start, End: end})
}

// GenerateWeekly creates a brief for the previous week
func (g *Generator) GenerateWeekly() (*Brief, error) {
	loc, err := time.LoadLocation(g.config.Timezone)
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	// Find last Monday
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday
	}
	daysToMonday := weekday - 1
	end := time.Date(now.Year(), now.Month(), now.Day()-daysToMonday, 9, 0, 0, 0, loc)
	start := end.Add(-7 * 24 * time.Hour)

	return g.Generate(BriefPeriod{Start: start, End: end})
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
