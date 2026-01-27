package linear

// Config holds Linear adapter configuration
type Config struct {
	Enabled    bool   `yaml:"enabled"`
	APIKey     string `yaml:"api_key"`
	TeamID     string `yaml:"team_id"`
	AutoAssign bool   `yaml:"auto_assign"`
	PilotLabel string `yaml:"pilot_label"`
}

// DefaultConfig returns default Linear configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
		PilotLabel: "pilot",
		AutoAssign: true,
	}
}

// Priority levels
const (
	PriorityNone   = 0
	PriorityUrgent = 1
	PriorityHigh   = 2
	PriorityMedium = 3
	PriorityLow    = 4
)

// PriorityName returns the human-readable priority name
func PriorityName(priority int) string {
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

// StateType represents issue state types
type StateType string

const (
	StateTypeBacklog   StateType = "backlog"
	StateTypeUnstarted StateType = "unstarted"
	StateTypeStarted   StateType = "started"
	StateTypeCompleted StateType = "completed"
	StateTypeCanceled  StateType = "canceled"
)
