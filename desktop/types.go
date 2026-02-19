package main

import "time"

// DashboardMetrics holds aggregated metrics for the desktop dashboard.
type DashboardMetrics struct {
	// Token totals
	TotalTokens  int64   `json:"totalTokens"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	TotalCostUSD float64 `json:"totalCostUSD"`

	// Task counts
	TotalTasks    int `json:"totalTasks"`
	SucceededTasks int `json:"succeededTasks"`
	FailedTasks   int `json:"failedTasks"`

	// 7-day sparkline data (oldest first)
	TokenSparkline []int64   `json:"tokenSparkline"`
	CostSparkline  []float64 `json:"costSparkline"`
	QueueSparkline []int     `json:"queueSparkline"`
}

// QueueTask represents a single task in the queue panel.
type QueueTask struct {
	ID          string    `json:"id"`
	IssueID     string    `json:"issueID"`
	Title       string    `json:"title"`
	Status      string    `json:"status"` // running, queued, pending, done, failed
	Progress    float64   `json:"progress"`
	PRURL       string    `json:"prURL,omitempty"`
	IssueURL    string    `json:"issueURL,omitempty"`
	ProjectPath string    `json:"projectPath"`
	CreatedAt   time.Time `json:"createdAt"`
}

// HistoryEntry represents a completed task in the history panel.
type HistoryEntry struct {
	ID          string    `json:"id"`
	IssueID     string    `json:"issueID"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	PRURL       string    `json:"prURL,omitempty"`
	ProjectPath string    `json:"projectPath"`
	CompletedAt time.Time `json:"completedAt"`
	DurationMs  int64     `json:"durationMs"`
	// Epic grouping
	EpicID    string         `json:"epicID,omitempty"`
	SubIssues []HistoryEntry `json:"subIssues,omitempty"`
}

// AutopilotStatus holds the autopilot state for the autopilot panel.
type AutopilotStatus struct {
	Enabled     bool        `json:"enabled"`
	Environment string      `json:"environment"`
	AutoRelease bool        `json:"autoRelease"`
	ActivePRs   []ActivePR  `json:"activePRs"`
	FailureCount int        `json:"failureCount"`
}

// ActivePR represents a PR being tracked by autopilot.
type ActivePR struct {
	Number    int    `json:"number"`
	URL       string `json:"url"`
	Stage     string `json:"stage"`
	CIStatus  string `json:"ciStatus,omitempty"`
	Error     string `json:"error,omitempty"`
	BranchName string `json:"branchName"`
}

// LogEntry represents a structured log entry for the logs panel.
type LogEntry struct {
	Ts        string `json:"ts"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Component string `json:"component,omitempty"`
}

// ServerStatus holds the connection status of the running pilot daemon.
type ServerStatus struct {
	Running   bool   `json:"running"`
	Version   string `json:"version,omitempty"`
	GatewayURL string `json:"gatewayURL,omitempty"`
}
