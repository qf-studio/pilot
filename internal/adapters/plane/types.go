package plane

import "time"

// Config holds Plane.so adapter configuration.
type Config struct {
	Enabled       bool           `yaml:"enabled"`
	BaseURL       string         `yaml:"base_url"`       // https://api.plane.so (or self-hosted)
	APIKey        string         `yaml:"api_key"`        // X-API-Key header value
	WebhookSecret string         `yaml:"webhook_secret"` // For HMAC signature verification
	WorkspaceSlug string         `yaml:"workspace_slug"`
	ProjectIDs    []string       `yaml:"project_ids"`
	PilotLabel    string         `yaml:"pilot_label"` // default: "pilot"
	Polling       *PollingConfig `yaml:"polling,omitempty"`
}

// PollingConfig holds polling configuration for the Plane adapter.
type PollingConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

// DefaultConfig returns default Plane configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
		BaseURL:    "https://api.plane.so",
		PilotLabel: "pilot",
	}
}

// StateGroup represents the fixed state group categories in Plane.
// Every state belongs to exactly one of these five groups.
type StateGroup string

const (
	StateGroupBacklog   StateGroup = "backlog"
	StateGroupUnstarted StateGroup = "unstarted"
	StateGroupStarted   StateGroup = "started"
	StateGroupCompleted StateGroup = "completed"
	StateGroupCancelled StateGroup = "cancelled"
)

// Priority represents a Plane work item priority (0=none, 1=urgent, 2=high, 3=medium, 4=low).
type Priority int

const (
	PriorityNone   Priority = 0
	PriorityUrgent Priority = 1
	PriorityHigh   Priority = 2
	PriorityMedium Priority = 3
	PriorityLow    Priority = 4
)

// PriorityName returns the human-readable priority name.
func PriorityName(p Priority) string {
	switch p {
	case PriorityUrgent:
		return "Urgent"
	case PriorityHigh:
		return "High"
	case PriorityMedium:
		return "Medium"
	case PriorityLow:
		return "Low"
	default:
		return "None"
	}
}

// WorkItem represents a Plane work item (formerly "issue").
// Uses /work-items/ API endpoints (NOT deprecated /issues/).
type WorkItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description_html,omitempty"`
	StateID     string    `json:"state"`
	Priority    Priority  `json:"priority"`
	LabelIDs    []string  `json:"labels"`
	AssigneeIDs []string  `json:"assignees"`
	ProjectID   string    `json:"project"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// State represents a Plane workflow state.
type State struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Group StateGroup `json:"group"`
	Color string     `json:"color"`
}

// Label represents a Plane label.
type Label struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Comment represents a Plane work item comment.
type Comment struct {
	ID             string    `json:"id"`
	CommentHTML    string    `json:"comment_html"`
	ExternalSource string    `json:"external_source,omitempty"`
	ExternalID     string    `json:"external_id,omitempty"`
	Access         string    `json:"access,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// paginatedResponse wraps paginated API responses from Plane.
type paginatedResponse struct {
	Results    []WorkItem `json:"results"`
	TotalCount int        `json:"total_count"`
}

// statesResponse wraps the states list API response.
type statesResponse struct {
	Results []State `json:"results"`
}

// labelsResponse wraps the labels list API response.
type labelsResponse struct {
	Results []Label `json:"results"`
}

// commentsResponse wraps the comments list API response.
type commentsResponse struct {
	Results []Comment `json:"results"`
}
