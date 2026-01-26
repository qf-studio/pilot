package sandbox

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionStatus represents the current state of an execution
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusQueued    ExecutionStatus = "queued"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
	StatusTimeout   ExecutionStatus = "timeout"
)

// ExecutionPhase represents the current phase of execution
type ExecutionPhase string

const (
	PhaseStarting     ExecutionPhase = "starting"
	PhaseBranching    ExecutionPhase = "branching"
	PhaseExploring    ExecutionPhase = "exploring"
	PhaseInstalling   ExecutionPhase = "installing"
	PhaseImplementing ExecutionPhase = "implementing"
	PhaseTesting      ExecutionPhase = "testing"
	PhaseCommitting   ExecutionPhase = "committing"
	PhaseCompleted    ExecutionPhase = "completed"
)

// Execution represents a task execution in the cloud
type Execution struct {
	ID             uuid.UUID       `json:"id"`
	OrgID          uuid.UUID       `json:"org_id"`
	ProjectID      uuid.UUID       `json:"project_id"`
	ExternalTaskID string          `json:"external_task_id,omitempty"` // Linear/Jira issue ID
	Status         ExecutionStatus `json:"status"`
	Phase          ExecutionPhase  `json:"phase"`
	Progress       int             `json:"progress"` // 0-100
	Output         string          `json:"output,omitempty"`
	Error          string          `json:"error,omitempty"`
	DurationMs     int64           `json:"duration_ms"`
	PRUrl          string          `json:"pr_url,omitempty"`
	CommitSHA      string          `json:"commit_sha,omitempty"`
	TokensUsed     int64           `json:"tokens_used"`
	CostCents      int             `json:"cost_cents"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`

	// Runtime fields (not persisted)
	ContainerID string `json:"-"`
	LogStream   string `json:"-"`
}

// ExecutionRequest is the input for creating an execution
type ExecutionRequest struct {
	OrgID          uuid.UUID `json:"org_id"`
	ProjectID      uuid.UUID `json:"project_id"`
	ExternalTaskID string    `json:"external_task_id,omitempty"`
	Prompt         string    `json:"prompt"`
	Branch         string    `json:"branch,omitempty"`
	Priority       int       `json:"priority"` // 0 = normal, 1 = high
}

// ExecutionResult is the output of a completed execution
type ExecutionResult struct {
	ID         uuid.UUID       `json:"id"`
	Status     ExecutionStatus `json:"status"`
	Output     string          `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	PRUrl      string          `json:"pr_url,omitempty"`
	CommitSHA  string          `json:"commit_sha,omitempty"`
	DurationMs int64           `json:"duration_ms"`
	TokensUsed int64           `json:"tokens_used"`
}

// ContainerConfig holds configuration for execution containers
type ContainerConfig struct {
	Image         string            `json:"image"`
	Memory        string            `json:"memory"`   // e.g., "2Gi"
	CPU           string            `json:"cpu"`      // e.g., "1"
	Timeout       time.Duration     `json:"timeout"`  // Max execution time
	Env           map[string]string `json:"env"`
	NetworkPolicy string            `json:"network_policy"` // e.g., "restricted"
}

// DefaultContainerConfig returns sensible defaults
func DefaultContainerConfig() ContainerConfig {
	return ContainerConfig{
		Image:         "pilot/executor:latest",
		Memory:        "2Gi",
		CPU:           "1",
		Timeout:       10 * time.Minute,
		NetworkPolicy: "restricted",
		Env:           make(map[string]string),
	}
}

// ResourceLimits defines limits for an execution
type ResourceLimits struct {
	MaxTokens       int64         `json:"max_tokens"`
	MaxDuration     time.Duration `json:"max_duration"`
	MaxOutputSize   int           `json:"max_output_size"` // bytes
	AllowedDomains  []string      `json:"allowed_domains"` // For network egress
}

// DefaultResourceLimits returns sensible defaults
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		MaxTokens:     1_000_000,  // 1M tokens
		MaxDuration:   10 * time.Minute,
		MaxOutputSize: 10 * 1024 * 1024, // 10MB
		AllowedDomains: []string{
			"github.com",
			"api.github.com",
			"linear.app",
			"api.linear.app",
			"*.atlassian.com",
			"registry.npmjs.org",
			"pypi.org",
		},
	}
}

// LogEntry represents a log line from execution
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"` // "info", "error", "debug"
	Message   string    `json:"message"`
	Phase     string    `json:"phase,omitempty"`
}

// ProgressUpdate represents a progress event
type ProgressUpdate struct {
	ExecutionID uuid.UUID      `json:"execution_id"`
	Phase       ExecutionPhase `json:"phase"`
	Progress    int            `json:"progress"`
	Message     string         `json:"message,omitempty"`
	Timestamp   time.Time      `json:"timestamp"`
}

// QueueStats provides queue statistics
type QueueStats struct {
	Pending  int `json:"pending"`
	Running  int `json:"running"`
	Queued   int `json:"queued"`
	AvgWaitMs int64 `json:"avg_wait_ms"`
}
