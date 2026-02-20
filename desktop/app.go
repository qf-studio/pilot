package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/config"
	"github.com/alekspetrov/pilot/internal/dashboard"
	"github.com/alekspetrov/pilot/internal/memory"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the Wails application struct. Its exported methods are bound to the
// frontend and callable from JavaScript/TypeScript via the generated bindings.
type App struct {
	ctx        context.Context
	store      *memory.Store
	httpClient *http.Client
	gatewayURL string // e.g. "http://127.0.0.1:9090"
}

// NewApp creates a new App instance.
func NewApp() *App {
	return &App{
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
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

	// Load config to determine gateway address
	cfg, err := config.Load(config.DefaultConfigPath())
	if err == nil && cfg.Gateway != nil {
		a.gatewayURL = fmt.Sprintf("http://%s:%d", cfg.Gateway.Host, cfg.Gateway.Port)
	} else {
		a.gatewayURL = "http://127.0.0.1:9090"
	}
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

	// Deduplicate by TaskID: keep the best execution per issue.
	// Priority: running > completed > failed; then most recent.
	best := make(map[string]QueueTask)
	for _, exec := range execs {
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

		switch exec.Status {
		case "running":
			qt.Progress = 0.5
		case "completed":
			qt.Progress = 1.0
		case "failed":
			qt.Progress = 0.0
		}

		key := exec.TaskID
		existing, exists := best[key]
		if !exists || queueTaskBetter(qt, existing) {
			best[key] = qt
		}
	}

	tasks := make([]QueueTask, 0, len(best))
	for _, qt := range best {
		tasks = append(tasks, qt)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})
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

	// Deduplicate by TaskID: keep the best execution per issue.
	// Priority: completed > failed; prefer entry with PR URL; then most recent.
	best := make(map[string]HistoryEntry)
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

		key := exec.TaskID
		existing, exists := best[key]
		if !exists || historyEntryBetter(he, existing) {
			best[key] = he
		}
	}

	entries := make([]HistoryEntry, 0, len(best))
	for _, he := range best {
		entries = append(entries, he)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CompletedAt.After(entries[j].CompletedAt)
	})
	return entries
}

// GetAutopilotStatus returns autopilot state by querying the running daemon's gateway API.
// Falls back to SQLite metrics when the daemon is not reachable.
func (a *App) GetAutopilotStatus() AutopilotStatus {
	// Try live daemon API first (GH-1585)
	if status, ok := a.fetchAutopilotFromDaemon(); ok {
		return status
	}

	// Fallback: read from SQLite metrics snapshot
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
		ActivePRs:    []ActivePR{},
	}
}

// GetLogs returns recent execution log entries for the logs panel.
func (a *App) GetLogs(limit int) []LogEntry {
	if a.store == nil {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}

	entries, err := a.store.GetRecentLogs(limit)
	if err != nil {
		return nil
	}

	result := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, LogEntry{
			Ts:        e.Timestamp.Format("15:04:05"),
			Level:     e.Level,
			Message:   e.Message,
			Component: e.Component,
		})
	}

	// Reverse so oldest is first (panel auto-scrolls to bottom)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}

// fetchAutopilotFromDaemon queries the running daemon's /api/v1/autopilot endpoint.
// Returns the parsed status and true if the daemon is reachable, or zero value and false otherwise.
func (a *App) fetchAutopilotFromDaemon() (AutopilotStatus, bool) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(a.gatewayURL + "/api/v1/autopilot")
	if err != nil {
		return AutopilotStatus{}, false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return AutopilotStatus{}, false
	}

	var data struct {
		Enabled      bool   `json:"enabled"`
		Environment  string `json:"environment"`
		AutoRelease  bool   `json:"autoRelease"`
		FailureCount int    `json:"failureCount"`
		ActivePRs    []struct {
			Number     int    `json:"number"`
			URL        string `json:"url"`
			Stage      string `json:"stage"`
			CIStatus   string `json:"ciStatus"`
			Error      string `json:"error"`
			BranchName string `json:"branchName"`
		} `json:"activePRs"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return AutopilotStatus{}, false
	}

	prs := make([]ActivePR, 0, len(data.ActivePRs))
	for _, pr := range data.ActivePRs {
		prs = append(prs, ActivePR{
			Number:     pr.Number,
			URL:        pr.URL,
			Stage:      pr.Stage,
			CIStatus:   pr.CIStatus,
			Error:      pr.Error,
			BranchName: pr.BranchName,
		})
	}

	return AutopilotStatus{
		Enabled:      data.Enabled,
		Environment:  data.Environment,
		AutoRelease:  data.AutoRelease,
		ActivePRs:    prs,
		FailureCount: data.FailureCount,
	}, true
}
// GetServerStatus checks whether the pilot daemon gateway is reachable.
// It hits the unauthenticated /health endpoint and, on success, fetches
// version info from /api/v1/status.
func (a *App) GetServerStatus() ServerStatus {
	if a.gatewayURL == "" {
		return ServerStatus{Running: false}
	}

	// Health check (unauthenticated)
	resp, err := a.httpClient.Get(a.gatewayURL + "/health")
	if err != nil {
		return ServerStatus{Running: false}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ServerStatus{Running: false}
	}

	status := ServerStatus{
		Running:    true,
		GatewayURL: a.gatewayURL,
	}

	// Try to get version from /api/v1/status (best-effort, may require auth)
	if vResp, err := a.httpClient.Get(a.gatewayURL + "/api/v1/status"); err == nil {
		defer func() { _ = vResp.Body.Close() }()
		if vResp.StatusCode == http.StatusOK {
			var body struct {
				Version string `json:"version"`
			}
			if json.NewDecoder(vResp.Body).Decode(&body) == nil && body.Version != "" {
				status.Version = body.Version
			}
		}
	}

	return status
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

// GetGitGraph returns the git commit graph for the configured project.
// limit controls how many commits to fetch; defaults to 100 when 0 is passed.
func (a *App) GetGitGraph(limit int) GitGraphData {
	if limit <= 0 {
		limit = 100
	}

	// Resolve project path from config (same pattern as startup()).
	projectPath := "."
	cfg, err := config.Load(config.DefaultConfigPath())
	if err == nil && len(cfg.Projects) > 0 {
		projectPath = cfg.Projects[0].Path
	}

	state := dashboard.FetchGitGraph(projectPath, limit)
	if state == nil {
		return GitGraphData{}
	}

	lines := make([]GitGraphLine, 0, len(state.Lines))
	for _, l := range state.Lines {
		lines = append(lines, GitGraphLine{
			GraphChars: l.GraphChars,
			Refs:       l.Refs,
			Message:    l.Message,
			Author:     l.Author,
			SHA:        l.SHA,
		})
	}

	return GitGraphData{
		Lines:       lines,
		TotalCount:  state.TotalCount,
		Error:       state.Error,
		LastRefresh: state.LastRefresh,
	}
}

// queueStatusPriority returns a numeric priority for queue task statuses.
// Higher value = better status to keep when deduplicating.
func queueStatusPriority(status string) int {
	switch status {
	case "running":
		return 3
	case "done":
		return 2
	case "queued", "pending":
		return 1
	default: // failed
		return 0
	}
}

// queueTaskBetter returns true if candidate should replace existing in dedup.
func queueTaskBetter(candidate, existing QueueTask) bool {
	cp, ep := queueStatusPriority(candidate.Status), queueStatusPriority(existing.Status)
	if cp != ep {
		return cp > ep
	}
	return candidate.CreatedAt.After(existing.CreatedAt)
}

// historyEntryBetter returns true if candidate should replace existing in dedup.
func historyEntryBetter(candidate, existing HistoryEntry) bool {
	// completed beats failed
	if candidate.Status != existing.Status {
		if candidate.Status == "completed" {
			return true
		}
		if existing.Status == "completed" {
			return false
		}
	}
	// prefer entry with PR URL
	if candidate.PRURL != "" && existing.PRURL == "" {
		return true
	}
	if candidate.PRURL == "" && existing.PRURL != "" {
		return false
	}
	// most recent wins
	return candidate.CompletedAt.After(existing.CompletedAt)
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
