package gateway

import (
	"net/http"
	"sort"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// Usage row kinds.
const (
	usageKindExecution = "execution"
	usageKindChatTurn  = "chat_turn"
)

// usageTimeLayout is RFC3339 with fixed-width nanoseconds, so timestamps sort
// lexicographically in time order.
const usageTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// maxUsageRows caps one page of GET /api/v1/executions/usage.
const maxUsageRows = 1000

// usageRow is one row of GET /api/v1/executions/usage (GH-5677): an execution
// or a web chat turn, so the console ingests both with one call. The shape
// matches the per-execution usage rows of the key-source issue (GH-5676) plus
// kind. Execution rows report keySource "unknown" and reportedCostUSD null
// until executions record them (GH-5676).
type usageRow struct {
	Kind             string   `json:"kind"` // execution | chat_turn
	ExecutionID      string   `json:"executionId"`
	TaskID           string   `json:"taskId"`
	ConversationID   string   `json:"conversationId,omitempty"` // chat_turn only
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

type usageResponse struct {
	Executions []usageRow `json:"executions"`
	// NextSince is the newest completedAt in this page, or the request's since
	// when the page is empty. It is inclusive on the server side, so the
	// boundary row repeats; dedupe on kind + executionId.
	NextSince string `json:"nextSince"`
	// HasMore is true when the page hit the row cap. The page then holds the
	// OLDEST rows at or after since (listed newest first), so polling again
	// from NextSince walks forward without skipping any.
	HasMore bool `json:"hasMore"`
}

// handleUsage serves GET /api/v1/executions/usage?since=RFC3339: executions
// and chat turns completed at or after since, newest first, capped at
// maxUsageRows. Same auth and project scoping as /api/v1/metrics.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	projectPath := s.dashboardProjectPath
	s.mu.RUnlock()

	cs, ok := store.(ChatUsageStore)
	if store == nil || !ok {
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

	// maxUsageRows+1 from each source is enough to know whether the merged
	// page overflows the cap.
	execs, err := cs.ListUsageExecutionsSince(since, projectPath, maxUsageRows+1)
	if err != nil {
		http.Error(w, "failed to fetch execution usage", http.StatusInternalServerError)
		return
	}
	turns, err := cs.ListChatTurnsSince(since, projectPath, maxUsageRows+1)
	if err != nil {
		http.Error(w, "failed to fetch chat usage", http.StatusInternalServerError)
		return
	}

	rows := make([]usageRow, 0, len(execs)+len(turns))
	for _, e := range execs {
		row := usageRow{
			Kind: usageKindExecution, ExecutionID: e.ExecutionID, TaskID: e.TaskID,
			ProjectPath: e.ProjectPath, Status: e.Status, Model: e.Model,
			TokensInput: e.TokensInput, TokensOutput: e.TokensOutput,
			TokensCacheRead: e.TokensCacheRead, TokensCacheWrite: e.TokensCacheWrite,
			EstimatedCostUSD: e.EstimatedCostUSD, KeySource: "unknown",
			CompletedAt: e.CompletedAt.UTC().Format(usageTimeLayout), PRUrl: e.PRUrl,
		}
		if e.StartedAt != nil {
			st := e.StartedAt.UTC().Format(usageTimeLayout)
			row.StartedAt = &st
		}
		rows = append(rows, row)
	}
	for _, t := range turns {
		st := t.StartedAt.UTC().Format(usageTimeLayout)
		rows = append(rows, usageRow{
			Kind: usageKindChatTurn, ExecutionID: t.ID, ConversationID: t.ConversationID,
			ProjectPath: t.ProjectPath, Status: t.Status, Model: t.Model,
			TokensInput: t.TokensInput, TokensOutput: t.TokensOutput,
			TokensCacheRead: t.TokensCacheRead, TokensCacheWrite: t.TokensCacheWrite,
			EstimatedCostUSD: t.EstimatedCostUSD, ReportedCostUSD: t.ReportedCostUSD,
			KeySource: t.KeySource, StartedAt: &st,
			CompletedAt: t.CompletedAt.UTC().Format(usageTimeLayout),
		})
	}
	// Oldest first to apply the cap, then flip to newest first.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CompletedAt < rows[j].CompletedAt })
	hasMore := len(rows) > maxUsageRows
	if hasMore {
		rows = rows[:maxUsageRows]
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}

	resp := usageResponse{Executions: rows, NextSince: since.UTC().Format(usageTimeLayout), HasMore: hasMore}
	if len(rows) > 0 {
		resp.NextSince = rows[0].CompletedAt
	}
	writeJSON(w, resp)
}

var _ ChatUsageStore = (*memory.Store)(nil)
