package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// GitGraphFetcher fetches git graph state for a project path and commit limit.
// Defined as a function type to avoid importing internal/dashboard which would
// create an import cycle via dashboard → banner → config → gateway.
type GitGraphFetcher func(projectPath string, limit int) interface{}

// DashboardStore provides read access to execution and metrics data for the dashboard API.
type DashboardStore interface {
	GetLifetimeTokens(projectPath string) (*memory.LifetimeTokens, error)
	GetLifetimeTaskCounts(projectPath string) (*memory.LifetimeTaskCounts, error)
	GetWindowedStats(projectPath string, since time.Time) (memory.WindowedStats, error)
	GetDailyMetrics(query memory.MetricsQuery) ([]*memory.DailyMetrics, error)
	GetRecentExecutions(limit int, projectPath string) ([]*memory.Execution, error)
	// GetNonTerminalExecutions backs the queue endpoint's active-only filter
	// (GH-5426): every execution whose status is not terminal, regardless of
	// age, so a long-running execution older than the 50-newest window isn't
	// invisible to an idle-detection consumer.
	GetNonTerminalExecutions(projectPath string) ([]*memory.Execution, error)
	GetQueuedTasks(limit int) ([]*memory.Execution, error)
	GetActiveExecutions() ([]*memory.Execution, error)
	GetRecentLogs(limit int) ([]*memory.LogEntry, error)
	// Approval reads/cleanup (GH-4748 / C14 pilot leg). *memory.Store satisfies
	// these directly; they piggyback on DashboardStore rather than a separate
	// interface since every wiring site that has a store already sets one.
	LoadPendingApprovals() ([]*memory.PendingApproval, error)
	GetExecutionByApprovalRequestID(requestID string) (*memory.Execution, error)
	DeletePendingApproval(id string) error
	// Execution-events read surface (GH-4749 / S4 pilot leg). GetExecution and
	// ListExecutionsForTask back the two routes' execution lookups (single-row
	// and newest-of-task respectively); ListExecutionEvents returns the
	// stage-transition timeline for a resolved execution ID.
	GetExecution(id string) (*memory.Execution, error)
	ListExecutionsForTask(taskID, projectPath string) ([]*memory.Execution, error)
	ListExecutionEvents(executionID string) ([]*memory.Event, error)
	// GetUsageSince backs GET /api/v1/executions/usage: executions (GH-5676)
	// and chat turns (GH-5690) merged into one page, oldest first.
	GetUsageSince(since time.Time, projectPath string) ([]memory.UsageRow, bool, error)
}

// ChatUsageStore is the optional chat-turn read surface (GH-5677). It is a
// separate interface, asserted on the dashboard store, so stores without chat
// metering keep satisfying DashboardStore. *memory.Store implements it.
type ChatUsageStore interface {
	GetChatTotalsSince(since time.Time, projectPath string) (memory.ChatTotals, error)
	GetChatDailySince(since time.Time, projectPath string) ([]memory.ChatDay, error)
}

// chatDailyDays is the length of the chatDaily series.
const chatDailyDays = 30

// defaultDashboardStatsWindowDays mirrors config.DashboardConfig's default
// (GH-4735), used when Server.dashboardStatsWindowDays is unset (0).
const defaultDashboardStatsWindowDays = 30

// SetDashboardStore configures the store used by dashboard API endpoints.
func (s *Server) SetDashboardStore(store DashboardStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dashboardStore = store
}

// SetDashboardProjectPath scopes the dashboard metrics, queue, and history endpoints
// to a single project directory. Pass "" to aggregate across all projects (daemon default).
func (s *Server) SetDashboardProjectPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dashboardProjectPath = path
}

// SetDashboardStatsWindowDays sets the rolling window (days) used for the
// dashboard metrics endpoint's windowed cost/success numbers (GH-4735),
// sourced from config.DashboardConfig.StatsWindowDays. Pass <= 0 to fall
// back to defaultDashboardStatsWindowDays.
func (s *Server) SetDashboardStatsWindowDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dashboardStatsWindowDays = days
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
	// Window carries the GH-4735 rolling-window headline numbers. Lifetime
	// fields above are kept unchanged for desktop app back-compat.
	Window dashboardWindowResponse `json:"window"`
	// Chat usage over the same rolling window as Window (GH-5677): web chat
	// turns, their list-price estimated cost and input+output tokens.
	ChatTurns   int     `json:"chatTurns"`
	ChatCostUSD float64 `json:"chatCostUSD"`
	ChatTokens  int64   `json:"chatTokens"`
	// ChatDaily is the last 30 UTC days of chat usage, oldest first, zero-filled.
	ChatDaily []chatDayResponse `json:"chatDaily"`
}

type chatDayResponse struct {
	Date    string  `json:"date"`
	Turns   int     `json:"turns"`
	CostUSD float64 `json:"costUSD"`
	Tokens  int64   `json:"tokens"`
}

// dashboardWindowResponse is the GH-4735 rolling-window cost/success block
// (see MetricsCardData/WindowedStats for the underlying semantics: canary
// executions excluded, neutral statuses excluded from AttemptSuccessRate).
type dashboardWindowResponse struct {
	Days                int     `json:"days"`
	TotalCostUSD        float64 `json:"totalCostUsd"`
	CostPerDeliveredUSD float64 `json:"costPerDeliveredUsd"`
	IssuesAttempted     int     `json:"issuesAttempted"`
	IssuesDelivered     int     `json:"issuesDelivered"`
	DeliveryRate        float64 `json:"deliveryRate"`
	AttemptSuccessRate  float64 `json:"attemptSuccessRate"`
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
	projectPath := s.dashboardProjectPath
	windowDays := s.dashboardStatsWindowDays
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	if windowDays <= 0 {
		windowDays = defaultDashboardStatsWindowDays
	}

	lt, err := store.GetLifetimeTokens(projectPath)
	if err != nil {
		lt = &memory.LifetimeTokens{}
	}

	tc, err := store.GetLifetimeTaskCounts(projectPath)
	if err != nil {
		tc = &memory.LifetimeTaskCounts{}
	}

	since := time.Now().AddDate(0, 0, -windowDays)
	ws, err := store.GetWindowedStats(projectPath, since)
	if err != nil {
		ws = memory.WindowedStats{}
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

	chatTotals, chatDaily := chatMetrics(store, projectPath, since, now)

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
		Window: dashboardWindowResponse{
			Days:                windowDays,
			TotalCostUSD:        ws.TotalCostUSD,
			CostPerDeliveredUSD: ws.CostPerDelivered,
			IssuesAttempted:     ws.IssuesAttempted,
			IssuesDelivered:     ws.IssuesDelivered,
			DeliveryRate:        ws.DeliveryRate,
			AttemptSuccessRate:  ws.AttemptSuccessRate,
		},
		ChatTurns:   chatTotals.Turns,
		ChatCostUSD: chatTotals.CostUSD,
		ChatTokens:  chatTotals.Tokens,
		ChatDaily:   chatDaily,
	}

	writeJSON(w, resp)
}

// chatMetrics reads the window totals and the zero-filled 30-day series. A
// store without chat metering, or a read error, yields zeros: chat numbers
// never fail the metrics endpoint.
func chatMetrics(store DashboardStore, projectPath string, since, now time.Time) (memory.ChatTotals, []chatDayResponse) {
	series := make([]chatDayResponse, chatDailyDays)
	byDate := make(map[string]int, chatDailyDays)
	for i := range series {
		d := now.AddDate(0, 0, -(chatDailyDays - 1 - i)).Format("2006-01-02")
		series[i].Date = d
		byDate[d] = i
	}
	cs, ok := store.(ChatUsageStore)
	if !ok {
		return memory.ChatTotals{}, series
	}
	totals, err := cs.GetChatTotalsSince(since, projectPath)
	if err != nil {
		totals = memory.ChatTotals{}
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(chatDailyDays - 1))
	days, err := cs.GetChatDailySince(dayStart, projectPath)
	if err == nil {
		for _, d := range days {
			if i, found := byDate[d.Date]; found {
				series[i].Turns, series[i].CostUSD, series[i].Tokens = d.Turns, d.CostUSD, d.Tokens
			}
		}
	}
	return totals, series
}

func (s *Server) handleDashboardQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	projectPath := s.dashboardProjectPath
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	// GH-5426: the "active" query parameter switches the queue endpoint from
	// the default 50-newest-by-created_at window (which can hide a
	// long-running execution behind 50 newer terminal rows) to every
	// non-terminal execution regardless of age. Absent (or falsy), the
	// response is byte-identical to before this parameter existed, so older
	// consumers are unaffected.
	active, _ := strconv.ParseBool(r.URL.Query().Get("active"))

	var execs []*memory.Execution
	var err error
	if active {
		execs, err = store.GetNonTerminalExecutions(projectPath)
	} else {
		execs, err = store.GetRecentExecutions(50, projectPath)
	}
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
	projectPath := s.dashboardProjectPath
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

	execs, err := store.GetRecentExecutions(limit, projectPath)
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
		return fmt.Sprintf("https://github.com/qf-studio/pilot/issues/%s", num)
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

// executionUsageRow is one usage row of GET /api/v1/executions/usage
// (GH-5676): an execution, or since GH-5690 a web chat turn (kind says which).
// The hosted console meters tenants by reading these rows; keySource says which
// Anthropic key paid for the run.
type executionUsageRow struct {
	Kind             string   `json:"kind"` // execution | chat_turn
	ExecutionID      string   `json:"executionId"`
	TaskID           string   `json:"taskId"`
	ConversationID   string   `json:"conversationId,omitempty"` // chat_turn rows only
	ProjectPath      string   `json:"projectPath"`
	Status           string   `json:"status"`
	Model            string   `json:"model"`
	TokensInput      int64    `json:"tokensInput"`
	TokensOutput     int64    `json:"tokensOutput"`
	TokensCacheRead  int64    `json:"tokensCacheRead"`
	TokensCacheWrite int64    `json:"tokensCacheWrite"`
	EstimatedCostUSD float64  `json:"estimatedCostUSD"`
	ReportedCostUSD  *float64 `json:"reportedCostUSD"` // null when the CLI reported no cost
	KeySource        string   `json:"keySource"`       // platform | own | unknown
	StartedAt        *string  `json:"startedAt"`       // RFC3339, null if never started
	CompletedAt      string   `json:"completedAt"`     // RFC3339
	PRUrl            string   `json:"prUrl"`
}

type executionUsageResponse struct {
	Executions []executionUsageRow `json:"executions"`
	// NextSince is the cursor for the next poll: the newest completedAt in this
	// page, or the request's since when the page is empty. It is inclusive on
	// the server side, so the boundary row repeats; dedupe on executionId.
	NextSince string `json:"nextSince"`
	// HasMore is true when either source held more rows than the page. The page
	// then holds the OLDEST rows at or after since, so polling again from
	// NextSince walks forward without skipping any.
	HasMore bool `json:"hasMore"`
}

// handleExecutionsUsage serves GET /api/v1/executions/usage?since=RFC3339:
// one row per execution or chat turn completed at or after since, oldest
// first, capped at memory.MaxExecutionUsageRows in total (GH-5676, GH-5690). Same auth as /api/v1/metrics (it is
// registered on the protected apiMux) and the same dashboard project scoping.
func (s *Server) handleExecutionsUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	projectPath := s.dashboardProjectPath
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	sinceRaw := r.URL.Query().Get("since")
	if sinceRaw == "" {
		http.Error(w, "since is required (RFC3339)", http.StatusBadRequest)
		return
	}
	since, err := time.Parse(time.RFC3339Nano, sinceRaw)
	if err != nil {
		http.Error(w, "since must be an RFC3339 timestamp", http.StatusBadRequest)
		return
	}

	rows, hasMore, err := store.GetUsageSince(since, projectPath)
	if err != nil {
		http.Error(w, "failed to fetch execution usage", http.StatusInternalServerError)
		return
	}

	resp := executionUsageResponse{
		Executions: make([]executionUsageRow, 0, len(rows)),
		NextSince:  since.UTC().Format(time.RFC3339Nano),
		HasMore:    hasMore,
	}
	for _, u := range rows {
		row := executionUsageRow{
			Kind:             u.Kind,
			ConversationID:   u.ConversationID,
			ExecutionID:      u.ExecutionID,
			TaskID:           u.TaskID,
			ProjectPath:      u.ProjectPath,
			Status:           u.Status,
			Model:            u.Model,
			TokensInput:      u.TokensInput,
			TokensOutput:     u.TokensOutput,
			TokensCacheRead:  u.TokensCacheRead,
			TokensCacheWrite: u.TokensCacheWrite,
			EstimatedCostUSD: u.EstimatedCostUSD,
			ReportedCostUSD:  u.ReportedCostUSD,
			KeySource:        u.KeySource,
			CompletedAt:      u.CompletedAt.UTC().Format(time.RFC3339Nano),
			PRUrl:            u.PRUrl,
		}
		if u.StartedAt != nil {
			st := u.StartedAt.UTC().Format(time.RFC3339Nano)
			row.StartedAt = &st
		}
		resp.Executions = append(resp.Executions, row)
	}
	if n := len(resp.Executions); n > 0 { // rows are oldest first
		resp.NextSince = resp.Executions[n-1].CompletedAt
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
