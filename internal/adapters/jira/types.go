package jira

// Config holds Jira adapter configuration
type Config struct {
	Enabled       bool   `yaml:"enabled"`
	Platform      string `yaml:"platform"`       // "cloud" or "server"
	BaseURL       string `yaml:"base_url"`       // e.g., "https://company.atlassian.net"
	Username      string `yaml:"username"`       // Email for Cloud, username for Server
	APIToken      string `yaml:"api_token"`      // API token (both Cloud and Server)
	WebhookSecret string `yaml:"webhook_secret"` // For HMAC signature verification
	PilotLabel    string `yaml:"pilot_label"`
	Transitions   struct {
		InProgress string `yaml:"in_progress"` // Jira transition ID
		Done       string `yaml:"done"`        // Jira transition ID
	} `yaml:"transitions"`
}

// DefaultConfig returns default Jira configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
		Platform:   "cloud",
		PilotLabel: "pilot",
	}
}

// Platform types
const (
	PlatformCloud  = "cloud"
	PlatformServer = "server"
)

// Issue states
const (
	StatusToDo       = "To Do"
	StatusInProgress = "In Progress"
	StatusDone       = "Done"
)

// Priority mapping from Jira
type Priority int

const (
	PriorityNone    Priority = 0
	PriorityHighest Priority = 1
	PriorityHigh    Priority = 2
	PriorityMedium  Priority = 3
	PriorityLow     Priority = 4
	PriorityLowest  Priority = 5
)

// PriorityFromJira converts a Jira priority name to Priority
func PriorityFromJira(name string) Priority {
	switch name {
	case "Highest", "Blocker", "Critical":
		return PriorityHighest
	case "High", "Major":
		return PriorityHigh
	case "Medium":
		return PriorityMedium
	case "Low", "Minor":
		return PriorityLow
	case "Lowest", "Trivial":
		return PriorityLowest
	default:
		return PriorityNone
	}
}

// PriorityName returns the human-readable priority name
func PriorityName(priority Priority) string {
	switch priority {
	case PriorityHighest:
		return "Highest"
	case PriorityHigh:
		return "High"
	case PriorityMedium:
		return "Medium"
	case PriorityLow:
		return "Low"
	case PriorityLowest:
		return "Lowest"
	default:
		return "No Priority"
	}
}

// Issue represents a Jira issue
type Issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Self   string `json:"self"`
	Fields Fields `json:"fields"`
}

// Fields represents Jira issue fields
type Fields struct {
	Summary     string       `json:"summary"`
	Description string       `json:"description"`
	IssueType   IssueType    `json:"issuetype"`
	Status      Status       `json:"status"`
	Priority    *JiraPriority `json:"priority,omitempty"`
	Labels      []string     `json:"labels"`
	Assignee    *User        `json:"assignee,omitempty"`
	Reporter    *User        `json:"reporter,omitempty"`
	Project     Project      `json:"project"`
	Created     string       `json:"created"`
	Updated     string       `json:"updated"`
}

// IssueType represents a Jira issue type
type IssueType struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Status represents a Jira status
type Status struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	StatusCategory StatusCategory `json:"statusCategory"`
}

// StatusCategory represents a Jira status category
type StatusCategory struct {
	ID   int    `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// JiraPriority represents a Jira priority
type JiraPriority struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User represents a Jira user
type User struct {
	AccountID    string `json:"accountId,omitempty"`    // Cloud
	Name         string `json:"name,omitempty"`         // Server
	Key          string `json:"key,omitempty"`          // Server
	EmailAddress string `json:"emailAddress,omitempty"`
	DisplayName  string `json:"displayName"`
}

// Project represents a Jira project
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	Self string `json:"self"`
}

// Transition represents a Jira workflow transition
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   Status `json:"to"`
}

// TransitionsResponse represents the response from the transitions API
type TransitionsResponse struct {
	Transitions []Transition `json:"transitions"`
}

// Comment represents a Jira comment
type Comment struct {
	ID      string `json:"id"`
	Body    string `json:"body"`
	Author  User   `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// RemoteLink represents a Jira remote link (for PR linking)
type RemoteLink struct {
	GlobalID string           `json:"globalId,omitempty"`
	Object   RemoteLinkObject `json:"object"`
}

// RemoteLinkObject represents the object in a remote link
type RemoteLinkObject struct {
	URL     string            `json:"url"`
	Title   string            `json:"title"`
	Summary string            `json:"summary,omitempty"`
	Icon    *RemoteLinkIcon   `json:"icon,omitempty"`
	Status  *RemoteLinkStatus `json:"status,omitempty"`
}

// RemoteLinkIcon represents an icon for a remote link
type RemoteLinkIcon struct {
	URL16x16 string `json:"url16x16"`
	Title    string `json:"title"`
}

// RemoteLinkStatus represents the status of a remote link
type RemoteLinkStatus struct {
	Resolved bool             `json:"resolved"`
	Icon     *RemoteLinkIcon  `json:"icon,omitempty"`
}
