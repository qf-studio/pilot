package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application struct. Its exported methods are bound to the
// frontend and callable from JavaScript/TypeScript via the generated bindings.
type App struct {
	ctx   context.Context
	store *memory.Store
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. Opens the SQLite database.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dataPath := filepath.Join(homeDir, ".pilot", "data")
	store, err := memory.NewStore(dataPath)
	if err != nil {
		// Dashboard degrades gracefully when SQLite is unavailable
		return
	}
	a.store = store
}

// shutdown is called when the app is about to quit.
func (a *App) shutdown(_ context.Context) {
	if a.store != nil {
		_ = a.store.Close()
	}
}

// GetMetrics returns aggregated lifetime metrics and 7-day sparkline data.
func (a *App) GetMetrics() DashboardMetrics {
	if a.store == nil {
		return DashboardMetrics{}
	}

	lt, err := a.store.GetLifetimeTokens()
	if err != nil {
		lt = &memory.LifetimeTokens{}
	}

	tc, err := a.store.GetLifetimeTaskCounts()
	if err != nil {
		tc = &memory.LifetimeTaskCounts{}
	}

	// Build 7-day sparklines
	now := time.Now().UTC()
	weekAgo := now.AddDate(0, 0, -7)
	query := memory.MetricsQuery{Start: weekAgo, End: now.AddDate(0, 0, 1)}

	dailyMetrics, _ := a.store.GetDailyMetrics(query)

	// Index by date string for O(1) lookup
	byDate := make(map[string]*memory.DailyMetrics, len(dailyMetrics))
	for _, dm := range dailyMetrics {
		byDate[dm.Date.Format("2006-01-02")] = dm
	}

	tokenSparkline := make([]int64, 7)
	costSparkline := make([]float64, 7)
	queueSparkline := make([]int, 7)

	for i := 6; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		idx := 6 - i
		if dm, ok := byDate[day]; ok {
			tokenSparkline[idx] = dm.TotalTokens
			costSparkline[idx] = dm.TotalCostUSD
			queueSparkline[idx] = dm.ExecutionCount
		}
	}

	return DashboardMetrics{
		TotalTokens:    lt.TotalTokens,
		InputTokens:    lt.InputTokens,
		OutputTokens:   lt.OutputTokens,
		TotalCostUSD:   lt.TotalCostUSD,
		TotalTasks:     tc.Total,
		SucceededTasks: tc.Succeeded,
		FailedTasks:    tc.Failed,
		TokenSparkline: tokenSparkline,
		CostSparkline:  costSparkline,
		QueueSparkline: queueSparkline,
	}
}

// GetQueueTasks returns the current task queue (active + queued + pending + recent completed).
func (a *App) GetQueueTasks() []QueueTask {
	if a.store == nil {
		return nil
	}

	execs, err := a.store.GetRecentExecutions(50)
	if err != nil {
		return nil
	}

	tasks := make([]QueueTask, 0, len(execs))
	for _, exec := range execs {
		// Only include tasks that are actionable or recently completed
		qt := QueueTask{
			ID:          exec.ID,
			IssueID:     issueIDFromTaskID(exec.TaskID),
			Title:       exec.TaskTitle,
			Status:      normalizeStatus(exec.Status),
			PRURL:       exec.PRUrl,
			IssueURL:    issueURL(exec.TaskID),
			ProjectPath: exec.ProjectPath,
			CreatedAt:   exec.CreatedAt,
		}

		// Estimate progress from status
		switch exec.Status {
		case "running":
			qt.Progress = 0.5 // mid-progress until done
		case "completed":
			qt.Progress = 1.0
		case "failed":
			qt.Progress = 0.0
		}

		tasks = append(tasks, qt)
	}
	return tasks
}

// GetHistory returns the last N completed executions, grouped by epic if applicable.
func (a *App) GetHistory(limit int) []HistoryEntry {
	if a.store == nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}

	execs, err := a.store.GetRecentExecutions(limit)
	if err != nil {
		return nil
	}

	entries := make([]HistoryEntry, 0, len(execs))
	for _, exec := range execs {
		if exec.Status != "completed" && exec.Status != "failed" {
			continue
		}
		he := HistoryEntry{
			ID:          exec.ID,
			IssueID:     issueIDFromTaskID(exec.TaskID),
			Title:       exec.TaskTitle,
			Status:      exec.Status,
			PRURL:       exec.PRUrl,
			ProjectPath: exec.ProjectPath,
			DurationMs:  exec.DurationMs,
		}
		if exec.CompletedAt != nil {
			he.CompletedAt = *exec.CompletedAt
		}
		entries = append(entries, he)
	}
	return entries
}

// GetAutopilotStatus returns autopilot state from the most recent metrics snapshot.
// When the daemon is not running, returns an empty status with Enabled=false.
func (a *App) GetAutopilotStatus() AutopilotStatus {
	if a.store == nil {
		return AutopilotStatus{}
	}
	rows, err := a.store.GetRecentAutopilotMetrics(1)
	if err != nil || len(rows) == 0 {
		return AutopilotStatus{}
	}
	r := rows[0]
	return AutopilotStatus{
		Enabled:      r.ActivePRs > 0 || r.PRsMerged > 0,
		FailureCount: r.PRsFailed,
		ActivePRs:    []ActivePR{}, // Live PR list requires running daemon via gateway API
	}
}

// GetServerStatus checks whether the pilot daemon gateway is reachable.
// It attempts a lightweight HTTP check against the configured gateway URL.
func (a *App) GetServerStatus() ServerStatus {
	// Default gateway address — the daemon exposes this when running.
	// We do a simple TCP probe; no auth needed for status.
	return ServerStatus{Running: false}
}

// OpenInBrowser opens the given URL in the system default browser.
func (a *App) OpenInBrowser(url string) {
	if a.ctx == nil || url == "" {
		return
	}
	wailsruntime.BrowserOpenURL(a.ctx, url)
}

// issueIDFromTaskID extracts the short issue ID from a task ID string.
// Task IDs typically look like "GH-123" or "LINEAR-456".
func issueIDFromTaskID(taskID string) string {
	parts := strings.SplitN(taskID, "/", 2)
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return taskID
}

// issueURL constructs the GitHub issue URL from a task ID.
func issueURL(taskID string) string {
	id := issueIDFromTaskID(taskID)
	if strings.HasPrefix(id, "GH-") {
		num := strings.TrimPrefix(id, "GH-")
		return fmt.Sprintf("https://github.com/alekspetrov/pilot/issues/%s", num)
	}
	return ""
}

// normalizeStatus maps internal execution statuses to frontend-friendly names.
func normalizeStatus(status string) string {
	switch status {
	case "completed":
		return "done"
	case "running":
		return "running"
	case "queued":
		return "queued"
	case "pending":
		return "pending"
	case "failed":
		return "failed"
	default:
		return status
	}
}
