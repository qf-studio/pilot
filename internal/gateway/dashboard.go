package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
)

// GitGraphFetcher fetches git graph state for a project path and commit limit.
// Defined as a function type to avoid importing internal/dashboard which would
// create an import cycle via dashboard → banner → config → gateway.
type GitGraphFetcher func(projectPath string, limit int) interface{}

// DashboardStore provides read access to execution and metrics data for the dashboard API.
type DashboardStore interface {
	GetLifetimeTokens() (*memory.LifetimeTokens, error)
	GetLifetimeTaskCounts() (*memory.LifetimeTaskCounts, error)
	GetDailyMetrics(query memory.MetricsQuery) ([]*memory.DailyMetrics, error)
	GetRecentExecutions(limit int) ([]*memory.Execution, error)
	GetQueuedTasks(limit int) ([]*memory.Execution, error)
	GetActiveExecutions() ([]*memory.Execution, error)
	GetRecentLogs(limit int) ([]*memory.LogEntry, error)
}

// SetDashboardStore configures the store used by dashboard API endpoints.
func (s *Server) SetDashboardStore(store DashboardStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dashboardStore = store
}

// --- JSON response types (mirrors desktop/types.go) ---

type dashboardMetricsResponse struct {
	TotalTokens    int64     `json:"totalTokens"`
	InputTokens    int64     `json:"inputTokens"`
	OutputTokens   int64     `json:"outputTokens"`
	TotalCostUSD   float64   `json:"totalCostUSD"`
	TotalTasks     int       `json:"totalTasks"`
	SucceededTasks int       `json:"succeededTasks"`
	FailedTasks    int       `json:"failedTasks"`
	TokenSparkline []int64   `json:"tokenSparkline"`
	CostSparkline  []float64 `json:"costSparkline"`
	QueueSparkline []int     `json:"queueSparkline"`
}

type queueTaskResponse struct {
	ID          string    `json:"id"`
	IssueID     string    `json:"issueID"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Progress    float64   `json:"progress"`
	PRURL       string    `json:"prURL,omitempty"`
	IssueURL    string    `json:"issueURL,omitempty"`
	ProjectPath string    `json:"projectPath"`
	CreatedAt   time.Time `json:"createdAt"`
}

type historyEntryResponse struct {
	ID          string    `json:"id"`
	IssueID     string    `json:"issueID"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	PRURL       string    `json:"prURL,omitempty"`
	ProjectPath string    `json:"projectPath"`
	CompletedAt time.Time `json:"completedAt"`
	DurationMs  int64     `json:"durationMs"`
}

type logEntryResponse struct {
	Ts        string `json:"ts"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Component string `json:"component,omitempty"`
}

// --- Handlers ---

func (s *Server) handleDashboardMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	lt, err := store.GetLifetimeTokens()
	if err != nil {
		lt = &memory.LifetimeTokens{}
	}

	tc, err := store.GetLifetimeTaskCounts()
	if err != nil {
		tc = &memory.LifetimeTaskCounts{}
	}

	now := time.Now().UTC()
	weekAgo := now.AddDate(0, 0, -7)
	query := memory.MetricsQuery{Start: weekAgo, End: now.AddDate(0, 0, 1)}

	dailyMetrics, _ := store.GetDailyMetrics(query)

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

	resp := dashboardMetricsResponse{
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

	writeJSON(w, resp)
}

func (s *Server) handleDashboardQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	execs, err := store.GetRecentExecutions(50)
	if err != nil {
		http.Error(w, "failed to fetch queue", http.StatusInternalServerError)
		return
	}

	tasks := make([]queueTaskResponse, 0, len(execs))
	for _, exec := range execs {
		qt := queueTaskResponse{
			ID:          exec.ID,
			IssueID:     issueIDFromTaskID(exec.TaskID),
			Title:       exec.TaskTitle,
			Status:      normalizeDashboardStatus(exec.Status),
			PRURL:       exec.PRUrl,
			IssueURL:    dashboardIssueURL(exec.TaskID),
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
		tasks = append(tasks, qt)
	}

	writeJSON(w, tasks)
}

func (s *Server) handleDashboardHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	limit := 5
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	execs, err := store.GetRecentExecutions(limit)
	if err != nil {
		http.Error(w, "failed to fetch history", http.StatusInternalServerError)
		return
	}

	entries := make([]historyEntryResponse, 0, len(execs))
	for _, exec := range execs {
		if exec.Status != "completed" && exec.Status != "failed" {
			continue
		}
		he := historyEntryResponse{
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

	writeJSON(w, entries)
}

func (s *Server) handleGitGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	fetcher := s.gitGraphFetcher
	projectPath := s.gitGraphPath
	s.mu.RUnlock()

	if fetcher == nil {
		http.Error(w, "git graph not configured", http.StatusServiceUnavailable)
		return
	}

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	if projectPath == "" {
		projectPath = "."
	}

	state := fetcher(projectPath, limit)
	writeJSON(w, state)
}

func (s *Server) handleDashboardLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	entries, err := store.GetRecentLogs(limit)
	if err != nil {
		http.Error(w, "failed to fetch logs", http.StatusInternalServerError)
		return
	}

	result := make([]logEntryResponse, 0, len(entries))
	for _, e := range entries {
		result = append(result, logEntryResponse{
			Ts:        e.Timestamp.Format("15:04:05"),
			Level:     e.Level,
			Message:   e.Message,
			Component: e.Component,
		})
	}

	// Reverse so oldest is first (UI auto-scrolls to bottom)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	writeJSON(w, result)
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func issueIDFromTaskID(taskID string) string {
	parts := strings.SplitN(taskID, "/", 2)
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return taskID
}

func dashboardIssueURL(taskID string) string {
	id := issueIDFromTaskID(taskID)
	if strings.HasPrefix(id, "GH-") {
		num := strings.TrimPrefix(id, "GH-")
		return fmt.Sprintf("https://github.com/anthropics/pilot/issues/%s", num)
	}
	return ""
}

func normalizeDashboardStatus(status string) string {
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
