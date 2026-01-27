package replay

import (
	"time"
)

// Recording represents a complete execution recording
type Recording struct {
	ID           string         `json:"id"`
	TaskID       string         `json:"task_id"`
	ProjectPath  string         `json:"project_path"`
	StartTime    time.Time      `json:"start_time"`
	EndTime      time.Time      `json:"end_time"`
	Duration     time.Duration  `json:"duration"`
	Status       string         `json:"status"` // "completed", "failed", "cancelled"
	EventCount   int            `json:"event_count"`
	StreamPath   string         `json:"stream_path"`   // Path to .jsonl file
	DiffsPath    string         `json:"diffs_path"`    // Path to diffs directory
	SummaryPath  string         `json:"summary_path"`  // Path to summary.md
	Metadata     *Metadata      `json:"metadata"`
	TokenUsage   *TokenUsage    `json:"token_usage,omitempty"`
	PhaseTimings []PhaseTiming  `json:"phase_timings,omitempty"`
}

// Metadata holds task metadata for the recording
type Metadata struct {
	Branch       string            `json:"branch,omitempty"`
	BaseBranch   string            `json:"base_branch,omitempty"`
	CommitSHA    string            `json:"commit_sha,omitempty"`
	PRUrl        string            `json:"pr_url,omitempty"`
	HasNavigator bool              `json:"has_navigator"`
	ModelName    string            `json:"model_name,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// TokenUsage tracks token consumption
type TokenUsage struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// PhaseTiming records time spent in each execution phase
type PhaseTiming struct {
	Phase    string        `json:"phase"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	Duration time.Duration `json:"duration"`
}

// StreamEvent is a timestamped event from the execution stream
type StreamEvent struct {
	Timestamp time.Time       `json:"timestamp"`
	Sequence  int             `json:"sequence"`
	Raw       string          `json:"raw"`  // Original JSON line
	Type      string          `json:"type"` // Parsed event type
	Parsed    *ParsedEvent    `json:"parsed,omitempty"`
}

// ParsedEvent contains structured data from stream events
type ParsedEvent struct {
	Type          string            `json:"type"`
	Subtype       string            `json:"subtype,omitempty"`
	ToolName      string            `json:"tool_name,omitempty"`
	ToolInput     map[string]any    `json:"tool_input,omitempty"`
	Text          string            `json:"text,omitempty"`
	Result        string            `json:"result,omitempty"`
	IsError       bool              `json:"is_error,omitempty"`
	InputTokens   int64             `json:"input_tokens,omitempty"`
	OutputTokens  int64             `json:"output_tokens,omitempty"`
	FilePath      string            `json:"file_path,omitempty"` // For file operations
	FileOperation string            `json:"file_operation,omitempty"` // read, write, edit
}

// FileDiff represents a file change during execution
type FileDiff struct {
	Timestamp time.Time `json:"timestamp"`
	FilePath  string    `json:"file_path"`
	Operation string    `json:"operation"` // "create", "modify", "delete"
	Before    string    `json:"before,omitempty"`
	After     string    `json:"after,omitempty"`
	Diff      string    `json:"diff,omitempty"` // Unified diff format
}

// RecordingFilter specifies criteria for listing recordings
type RecordingFilter struct {
	ProjectPath string
	Status      string
	Since       time.Time
	Limit       int
}

// RecordingSummary is a brief summary for listing
type RecordingSummary struct {
	ID          string        `json:"id"`
	TaskID      string        `json:"task_id"`
	ProjectPath string        `json:"project_path"`
	Status      string        `json:"status"`
	StartTime   time.Time     `json:"start_time"`
	Duration    time.Duration `json:"duration"`
	EventCount  int           `json:"event_count"`
}

// AnalysisReport provides detailed analysis of a recording
type AnalysisReport struct {
	Recording      *Recording           `json:"recording"`
	TokenBreakdown TokenBreakdown       `json:"token_breakdown"`
	PhaseAnalysis  []PhaseAnalysis      `json:"phase_analysis"`
	ToolUsage      []ToolUsageStats     `json:"tool_usage"`
	Errors         []ErrorEvent         `json:"errors"`
	DecisionPoints []DecisionPoint      `json:"decision_points"`
}

// TokenBreakdown shows token usage by category
type TokenBreakdown struct {
	ByPhase  map[string]TokenUsage `json:"by_phase"`
	ByTool   map[string]TokenUsage `json:"by_tool"`
	Overhead TokenUsage            `json:"overhead"` // System/init tokens
}

// PhaseAnalysis provides insights into each phase
type PhaseAnalysis struct {
	Phase        string          `json:"phase"`
	Duration     time.Duration   `json:"duration"`
	Percentage   float64         `json:"percentage"` // Of total time
	EventCount   int             `json:"event_count"`
	ToolsUsed    []string        `json:"tools_used"`
	FilesChanged int             `json:"files_changed"`
}

// ToolUsageStats tracks usage of individual tools
type ToolUsageStats struct {
	Tool         string        `json:"tool"`
	Count        int           `json:"count"`
	TotalTime    time.Duration `json:"total_time"`
	AvgTime      time.Duration `json:"avg_time"`
	ErrorCount   int           `json:"error_count"`
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
}

// ErrorEvent captures an error during execution
type ErrorEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Phase     string    `json:"phase"`
	Tool      string    `json:"tool,omitempty"`
	Message   string    `json:"message"`
	Sequence  int       `json:"sequence"`
}

// DecisionPoint marks significant decisions during execution
type DecisionPoint struct {
	Timestamp   time.Time `json:"timestamp"`
	Sequence    int       `json:"sequence"`
	Description string    `json:"description"`
	Context     string    `json:"context,omitempty"`
}

// ReplayOptions configures replay behavior
type ReplayOptions struct {
	StartAt     int           // Start from this event sequence
	StopAt      int           // Stop at this event sequence (0 = end)
	Speed       float64       // Playback speed multiplier (1.0 = real-time)
	ShowTools   bool          // Show tool calls
	ShowText    bool          // Show assistant text
	ShowResults bool          // Show tool results
	Verbose     bool          // Show all event details
}

// DefaultReplayOptions returns default replay options
func DefaultReplayOptions() *ReplayOptions {
	return &ReplayOptions{
		StartAt:     0,
		StopAt:      0,
		Speed:       0, // As fast as possible (non-real-time)
		ShowTools:   true,
		ShowText:    true,
		ShowResults: false,
		Verbose:     false,
	}
}

// ExportFormat specifies output format for export
type ExportFormat string

const (
	ExportFormatHTML     ExportFormat = "html"
	ExportFormatMarkdown ExportFormat = "markdown"
	ExportFormatJSON     ExportFormat = "json"
)
