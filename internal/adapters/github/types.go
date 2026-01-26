package github

// Config holds GitHub adapter configuration
type Config struct {
	Enabled       bool   `yaml:"enabled"`
	Token         string `yaml:"token"`          // Personal Access Token or GitHub App token
	WebhookSecret string `yaml:"webhook_secret"` // For HMAC signature verification
	PilotLabel    string `yaml:"pilot_label"`
}

// DefaultConfig returns default GitHub configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
		PilotLabel: "pilot",
	}
}

// Issue states
const (
	StateOpen   = "open"
	StateClosed = "closed"
)

// Label names used by Pilot
const (
	LabelInProgress = "pilot-in-progress"
	LabelDone       = "pilot-done"
	LabelFailed     = "pilot-failed"
)

// Priority mapping from GitHub labels
type Priority int

const (
	PriorityNone   Priority = 0
	PriorityUrgent Priority = 1
	PriorityHigh   Priority = 2
	PriorityMedium Priority = 3
	PriorityLow    Priority = 4
)

// PriorityFromLabel converts a GitHub label to priority
func PriorityFromLabel(label string) Priority {
	switch label {
	case "priority:urgent", "P0":
		return PriorityUrgent
	case "priority:high", "P1":
		return PriorityHigh
	case "priority:medium", "P2":
		return PriorityMedium
	case "priority:low", "P3":
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
