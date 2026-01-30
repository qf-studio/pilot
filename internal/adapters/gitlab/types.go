package gitlab

import "time"

// Config holds GitLab adapter configuration
type Config struct {
	Enabled           bool                     `yaml:"enabled"`
	Token             string                   `yaml:"token"`          // Personal Access Token or Project Access Token
	BaseURL           string                   `yaml:"base_url"`       // Default: https://gitlab.com
	WebhookSecret     string                   `yaml:"webhook_secret"` // Simple token for X-Gitlab-Token header
	PilotLabel        string                   `yaml:"pilot_label"`
	Project           string                   `yaml:"project"` // Project path in "namespace/project" format
	Polling           *PollingConfig           `yaml:"polling"`
	StaleLabelCleanup *StaleLabelCleanupConfig `yaml:"stale_label_cleanup"`
}

// PollingConfig holds GitLab polling settings
type PollingConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"` // Poll interval (default 30s)
	Label    string        `yaml:"label"`    // Label to watch for (default: pilot)
}

// StaleLabelCleanupConfig holds settings for auto-cleanup of stale pilot-in-progress labels
type StaleLabelCleanupConfig struct {
	Enabled   bool          `yaml:"enabled"`
	Interval  time.Duration `yaml:"interval"`  // How often to check for stale labels (default: 30m)
	Threshold time.Duration `yaml:"threshold"` // How long before a label is considered stale (default: 1h)
}

// DefaultConfig returns default GitLab configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
		BaseURL:    "https://gitlab.com",
		PilotLabel: "pilot",
		Polling: &PollingConfig{
			Enabled:  false,
			Interval: 30 * time.Second,
			Label:    "pilot",
		},
		StaleLabelCleanup: &StaleLabelCleanupConfig{
			Enabled:   true,
			Interval:  30 * time.Minute,
			Threshold: 1 * time.Hour,
		},
	}
}

// ListIssuesOptions holds options for listing issues
type ListIssuesOptions struct {
	Labels    []string
	State     string // opened, closed, all
	Sort      string // created_at, updated_at
	OrderBy   string // asc, desc
	UpdatedAt time.Time
}

// Issue states (GitLab uses different terminology than GitHub)
const (
	StateOpened = "opened"
	StateClosed = "closed"
)

// Label names used by Pilot
const (
	LabelInProgress = "pilot-in-progress"
	LabelDone       = "pilot-done"
	LabelFailed     = "pilot-failed"
)

// Priority mapping from GitLab labels
type Priority int

const (
	PriorityNone   Priority = 0
	PriorityUrgent Priority = 1
	PriorityHigh   Priority = 2
	PriorityMedium Priority = 3
	PriorityLow    Priority = 4
)

// PriorityFromLabel converts a GitLab label to priority
func PriorityFromLabel(label string) Priority {
	switch label {
	case "priority::urgent", "P0":
		return PriorityUrgent
	case "priority::high", "P1":
		return PriorityHigh
	case "priority::medium", "P2":
		return PriorityMedium
	case "priority::low", "P3":
		return PriorityLow
	default:
		return PriorityNone
	}
}

// PriorityName returns the human-readable priority name
func PriorityName(priority Priority) string {
	switch priority {
	case PriorityUrgent:
		return "Urgent"
	case PriorityHigh:
		return "High"
	case PriorityMedium:
		return "Medium"
	case PriorityLow:
		return "Low"
	default:
		return "No Priority"
	}
}

// Merge request states
const (
	MRStateOpened = "opened"
	MRStateClosed = "closed"
	MRStateMerged = "merged"
)

// Pipeline statuses
const (
	PipelinePending  = "pending"
	PipelineRunning  = "running"
	PipelineSuccess  = "success"
	PipelineFailed   = "failed"
	PipelineCanceled = "canceled"
	PipelineSkipped  = "skipped"
	PipelineManual   = "manual"
)

// Issue represents a GitLab issue
type Issue struct {
	ID          int       `json:"id"`
	IID         int       `json:"iid"` // Project-scoped ID
	ProjectID   int       `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"` // opened, closed
	Labels      []string  `json:"labels"`
	WebURL      string    `json:"web_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ClosedAt    time.Time `json:"closed_at,omitempty"`
	Author      *User     `json:"author,omitempty"`
	Assignees   []*User   `json:"assignees,omitempty"`
}

// User represents a GitLab user
type User struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
	WebURL    string `json:"web_url"`
}

// Project represents a GitLab project
type Project struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	NameWithNamespace string `json:"name_with_namespace"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
}

// MergeRequest represents a GitLab merge request
type MergeRequest struct {
	ID             int       `json:"id"`
	IID            int       `json:"iid"` // Project-scoped ID
	ProjectID      int       `json:"project_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	State          string    `json:"state"` // opened, closed, merged
	SourceBranch   string    `json:"source_branch"`
	TargetBranch   string    `json:"target_branch"`
	WebURL         string    `json:"web_url"`
	MergeStatus    string    `json:"merge_status"` // can_be_merged, cannot_be_merged, checking, etc.
	HasConflicts   bool      `json:"has_conflicts"`
	SHA            string    `json:"sha"` // Head commit SHA
	MergedAt       time.Time `json:"merged_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Author         *User     `json:"author,omitempty"`
	Labels         []string  `json:"labels"`
	Draft          bool      `json:"draft"`
	HeadPipeline   *Pipeline `json:"head_pipeline,omitempty"`
	DetailedStatus *Status   `json:"detailed_merge_status,omitempty"`
}

// MergeRequestInput is used for creating merge requests
type MergeRequestInput struct {
	Title              string `json:"title"`
	Description        string `json:"description,omitempty"`
	SourceBranch       string `json:"source_branch"`
	TargetBranch       string `json:"target_branch"`
	RemoveSourceBranch bool   `json:"remove_source_branch,omitempty"`
	Squash             bool   `json:"squash,omitempty"`
}

// Pipeline represents a GitLab CI/CD pipeline
type Pipeline struct {
	ID        int       `json:"id"`
	IID       int       `json:"iid"`
	ProjectID int       `json:"project_id"`
	SHA       string    `json:"sha"`
	Ref       string    `json:"ref"`
	Status    string    `json:"status"` // pending, running, success, failed, canceled, skipped, manual
	WebURL    string    `json:"web_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Status represents a detailed merge status
type Status struct {
	Icon        string `json:"icon"`
	Text        string `json:"text"`
	Label       string `json:"label"`
	Group       string `json:"group"`
	HasDetails  bool   `json:"has_details"`
	DetailsPath string `json:"details_path"`
}

// Note represents a GitLab comment (note in GitLab terminology)
type Note struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	Author    *User     `json:"author,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	System    bool      `json:"system"` // True for system-generated notes
}

// WebhookEvent types
const (
	WebhookEventIssue        = "Issue Hook"
	WebhookEventMergeRequest = "Merge Request Hook"
	WebhookEventPipeline     = "Pipeline Hook"
	WebhookEventNote         = "Note Hook"
)

// IssueWebhookPayload represents a GitLab issue webhook event
type IssueWebhookPayload struct {
	ObjectKind       string             `json:"object_kind"` // "issue"
	EventType        string             `json:"event_type"`  // "issue"
	User             *User              `json:"user"`
	Project          *WebhookProject    `json:"project"`
	ObjectAttributes *IssueAttributes   `json:"object_attributes"`
	Labels           []*WebhookLabel    `json:"labels"`
	Changes          *IssueChanges      `json:"changes,omitempty"`
	Assignees        []*User            `json:"assignees,omitempty"`
}

// WebhookProject contains project info in webhook payloads
type WebhookProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
	DefaultBranch     string `json:"default_branch"`
}

// IssueAttributes contains issue details in webhook payloads
type IssueAttributes struct {
	ID          int       `json:"id"`
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"`
	Action      string    `json:"action"` // open, close, reopen, update
	URL         string    `json:"url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WebhookLabel represents a label in webhook payloads
type WebhookLabel struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// IssueChanges tracks what changed in an issue update webhook
type IssueChanges struct {
	Labels *LabelChange `json:"labels,omitempty"`
	State  *StateChange `json:"state,omitempty"`
}

// LabelChange represents label additions/removals
type LabelChange struct {
	Previous []*WebhookLabel `json:"previous"`
	Current  []*WebhookLabel `json:"current"`
}

// StateChange represents state transitions
type StateChange struct {
	Previous string `json:"previous"`
	Current  string `json:"current"`
}
