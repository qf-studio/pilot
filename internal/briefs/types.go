package briefs

import "time"

// BriefKind says which cadence a brief belongs to. It is the `kind` marker on
// the console payload and the email (GH-5792).
type BriefKind string

// Brief kinds.
const (
	KindDaily  BriefKind = "daily"
	KindWeekly BriefKind = "weekly"
)

// Brief represents a daily or weekly summary brief
type Brief struct {
	// Kind is KindWeekly for the Monday brief; empty is treated as daily.
	Kind BriefKind
	// Weekly carries the proof numbers; set only when Kind is KindWeekly.
	Weekly      *WeeklyProof
	GeneratedAt time.Time
	Period      BriefPeriod
	Completed   []TaskSummary
	InProgress  []TaskSummary
	Blocked     []BlockedTask
	Upcoming    []TaskSummary
	Metrics     BriefMetrics
}

// BriefPeriod represents the time range for the brief
type BriefPeriod struct {
	Start time.Time
	End   time.Time
}

// TaskSummary represents a task in the brief
type TaskSummary struct {
	ID          string
	Title       string
	ProjectPath string
	Status      string
	Progress    int    // 0-100
	PRUrl       string // For completed tasks with PRs
	DurationMs  int64
	CompletedAt *time.Time
}

// BlockedTask represents a task that failed or is blocked
type BlockedTask struct {
	TaskSummary
	Error      string
	FailedAt   time.Time
	RetryCount int
}

// BriefMetrics contains aggregate metrics for the period
type BriefMetrics struct {
	TotalTasks       int
	CompletedCount   int
	FailedCount      int
	SuccessRate      float64 // 0.0-1.0
	AvgDurationMs    int64
	PRsCreated       int
	PRsMerged        int
	TotalTokensUsed  int64
	EstimatedCostUSD float64
}

// BriefConfig holds configuration for brief generation
type BriefConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Schedule string `yaml:"schedule"` // Cron syntax: "0 9 * * 1-5"
	Timezone string `yaml:"timezone"`
	// WeeklyDay is the weekday the weekly brief fires, at the schedule's
	// hour and minute. Empty means monday (GH-5792).
	WeeklyDay string          `yaml:"weekly_day"`
	Channels  []ChannelConfig `yaml:"channels"`
	Content   ContentConfig   `yaml:"content"`
	Filters   FilterConfig    `yaml:"filters"`
}

// ChannelConfig defines a delivery channel
type ChannelConfig struct {
	Type       string   `yaml:"type"`       // "slack", "email", "telegram"
	Channel    string   `yaml:"channel"`    // For Slack: "#channel-name", For Telegram: chat_id
	Recipients []string `yaml:"recipients"` // For email
}

// ContentConfig controls what content is included
type ContentConfig struct {
	IncludeMetrics     bool `yaml:"include_metrics"`
	IncludeErrors      bool `yaml:"include_errors"`
	MaxItemsPerSection int  `yaml:"max_items_per_section"`
}

// FilterConfig filters which tasks to include
type FilterConfig struct {
	Projects []string `yaml:"projects"` // Empty = all projects
}

// DeliveryResult represents the result of sending a brief
type DeliveryResult struct {
	Channel   string
	Success   bool
	Error     error
	SentAt    time.Time
	MessageID string // Platform-specific message ID
}

// StructuredBriefSchema is the version stamped on every StructuredBrief
// payload. Bump it on any breaking change to the JSON shape (GH-5791).
const StructuredBriefSchema = 1

// StructuredBrief is the one brief object the console stores and shows and
// the email rendering is built from (GH-5791). Section order is part of the
// contract: shipped, review_needed, pool, failed — the struct field order
// below is the JSON key order.
type StructuredBrief struct {
	Schema int `json:"schema"`
	// Kind is "daily" or "weekly" (GH-5792).
	Kind BriefKind `json:"kind"`
	// Timezone is the IANA name of the zone the timestamps below are
	// rendered in: daily_brief.timezone, else the box zone (GH-5814).
	Timezone     string       `json:"timezone"`
	GeneratedAt  time.Time    `json:"generated_at"`
	PeriodStart  time.Time    `json:"period_start"`
	PeriodEnd    time.Time    `json:"period_end"`
	Shipped      []ShippedRun `json:"shipped"`
	ReviewNeeded []ReviewItem `json:"review_needed"`
	// Pool is nil (JSON null) when the daemon does not know the allowance.
	Pool   *PoolUsage  `json:"pool"`
	Failed []FailedRun `json:"failed"`
	// Weekly is present only on weekly briefs (GH-5792).
	Weekly *WeeklyProof `json:"weekly,omitempty"`
}

// WeeklyProof is the weekly brief's three proof numbers plus the biggest
// shipped runs (GH-5792). An empty week is all zeros and an empty TopShipped,
// never an error.
type WeeklyProof struct {
	TicketsShipped        int          `json:"tickets_shipped"`
	MedianTimeToPRMinutes float64      `json:"median_time_to_pr_minutes"`
	MergedCount           int          `json:"merged_count"`
	TopShipped            []ShippedRun `json:"top_shipped"`
}

// ShippedRun is one delivered run. It folds in the receipts digest line:
// CostUSD is set only when the execution ran on the tenant's own key and is
// omitted on the platform key (and when the key source is unknown).
type ShippedRun struct {
	IssueRef     string   `json:"issue_ref"`
	Title        string   `json:"title"`
	PRURL        string   `json:"pr_url,omitempty"`
	DurationMs   int64    `json:"duration_ms"`
	LinesAdded   int      `json:"lines_added"`
	LinesRemoved int      `json:"lines_removed"`
	KeySource    string   `json:"key_source"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

// ReviewItem is something waiting on a human, with a one-line reason.
type ReviewItem struct {
	IssueRef string `json:"issue_ref"`
	Title    string `json:"title"`
	PRURL    string `json:"pr_url,omitempty"`
	Reason   string `json:"reason"`
	// WaitingSince is when the item started waiting; Overdue is true when
	// that was more than 24h before the brief was generated.
	WaitingSince time.Time `json:"waiting_since"`
	Overdue      bool      `json:"overdue"`
}

// PoolUsage is how much of the included allowance has been used.
type PoolUsage struct {
	PercentUsed float64 `json:"percent_used"`
}

// FailedRun is a run that failed and was not later delivered.
type FailedRun struct {
	IssueRef     string `json:"issue_ref"`
	FailureClass string `json:"failure_class"`
	Retryable    bool   `json:"retryable"`
}
