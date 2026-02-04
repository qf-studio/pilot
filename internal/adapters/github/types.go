package github

import "time"

// Config holds GitHub adapter configuration
type Config struct {
	Enabled           bool                     `yaml:"enabled"`
	Token             string                   `yaml:"token"`          // Personal Access Token or GitHub App token
	WebhookSecret     string                   `yaml:"webhook_secret"` // For HMAC signature verification
	PilotLabel        string                   `yaml:"pilot_label"`
	Repo              string                   `yaml:"repo"`                // Default repo in "owner/repo" format
	ProjectPath       string                   `yaml:"project_path"`        // Required project path - must match repo (GH-386)
	Polling           *PollingConfig           `yaml:"polling"`             // Polling configuration
	StaleLabelCleanup *StaleLabelCleanupConfig `yaml:"stale_label_cleanup"` // Auto-cleanup stale labels
}

// PollingConfig holds GitHub polling settings
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

// DefaultConfig returns default GitHub configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:    false,
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
	Labels []string
	State  string // open, closed, all
	Sort   string // created, updated, comments
	Since  time.Time
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

// CommitStatus states
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
	StatusError   = "error"
)

// CommitStatus represents a GitHub commit status
type CommitStatus struct {
	ID          int64  `json:"id,omitempty"`
	State       string `json:"state"`                 // pending, success, failure, error
	TargetURL   string `json:"target_url,omitempty"`  // URL to link to from the status
	Description string `json:"description,omitempty"` // Short description (140 chars max)
	Context     string `json:"context,omitempty"`     // Unique identifier for the status
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// CheckRun conclusion values
const (
	ConclusionSuccess        = "success"
	ConclusionFailure        = "failure"
	ConclusionNeutral        = "neutral"
	ConclusionCancelled      = "cancelled"
	ConclusionTimedOut       = "timed_out"
	ConclusionActionRequired = "action_required"
	ConclusionSkipped        = "skipped"
)

// CheckRun status values
const (
	CheckRunQueued     = "queued"
	CheckRunInProgress = "in_progress"
	CheckRunCompleted  = "completed"
)

// CheckRun represents a GitHub Check Run (Checks API)
type CheckRun struct {
	ID          int64        `json:"id,omitempty"`
	HeadSHA     string       `json:"head_sha"`
	Name        string       `json:"name"`
	Status      string       `json:"status,omitempty"`      // queued, in_progress, completed
	Conclusion  string       `json:"conclusion,omitempty"`  // success, failure, neutral, cancelled, timed_out, action_required, skipped
	DetailsURL  string       `json:"details_url,omitempty"` // URL for more details
	ExternalID  string       `json:"external_id,omitempty"` // Reference for external system
	StartedAt   string       `json:"started_at,omitempty"`
	CompletedAt string       `json:"completed_at,omitempty"`
	Output      *CheckOutput `json:"output,omitempty"` // Rich output for the check
}

// CheckOutput represents the output of a check run
type CheckOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text,omitempty"`
}

// PRRef represents a branch reference with SHA (matches GitHub API response for head/base)
type PRRef struct {
	Ref string `json:"ref"` // branch name
	SHA string `json:"sha"` // commit sha
}

// PullRequest represents a GitHub pull request
type PullRequest struct {
	ID        int64  `json:"id,omitempty"`
	Number    int    `json:"number,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	State     string `json:"state,omitempty"` // open, closed
	Head      PRRef  `json:"head"`            // Head reference with branch name and SHA
	Base      PRRef  `json:"base"`            // Base reference with branch name and SHA
	HTMLURL   string `json:"html_url,omitempty"`
	Draft     bool   `json:"draft,omitempty"`
	Merged         bool   `json:"merged,omitempty"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
	Mergeable      *bool  `json:"mergeable,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	MergedAt  string `json:"merged_at,omitempty"`
}

// PullRequestInput is used for creating pull requests
type PullRequestInput struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Head  string `json:"head"` // Branch to merge from
	Base  string `json:"base"` // Branch to merge into
	Draft bool   `json:"draft,omitempty"`
}

// PRComment represents a comment on a pull request
type PRComment struct {
	ID        int64  `json:"id,omitempty"`
	Body      string `json:"body"`
	Path      string `json:"path,omitempty"`      // File path for review comments
	Position  int    `json:"position,omitempty"`  // Line position for review comments
	CommitID  string `json:"commit_id,omitempty"` // Commit SHA for review comments
	HTMLURL   string `json:"html_url,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// CombinedStatus represents the combined status for a commit SHA
// See: https://docs.github.com/en/rest/commits/statuses#get-the-combined-status-for-a-specific-reference
type CombinedStatus struct {
	State      string         `json:"state"` // pending, success, failure, error
	Statuses   []CommitStatus `json:"statuses"`
	SHA        string         `json:"sha"`
	TotalCount int            `json:"total_count"`
}

// CheckRunsResponse contains check run list from GitHub Checks API
// See: https://docs.github.com/en/rest/checks/runs#list-check-runs-for-a-git-reference
type CheckRunsResponse struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

// Merge methods for MergePullRequest
const (
	MergeMethodMerge  = "merge"
	MergeMethodSquash = "squash"
	MergeMethodRebase = "rebase"
)

// Review events for PR reviews
const (
	ReviewEventApprove        = "APPROVE"
	ReviewEventRequestChanges = "REQUEST_CHANGES"
	ReviewEventComment        = "COMMENT"
)

// Review states returned by GitHub API
const (
	ReviewStateApproved         = "APPROVED"
	ReviewStateChangesRequested = "CHANGES_REQUESTED"
	ReviewStateCommented        = "COMMENTED"
	ReviewStateDismissed        = "DISMISSED"
	ReviewStatePending          = "PENDING"
)

// PullRequestReview represents a GitHub PR review
type PullRequestReview struct {
	ID          int64  `json:"id"`
	User        User   `json:"user"`
	Body        string `json:"body,omitempty"`
	State       string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	HTMLURL     string `json:"html_url,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
}

// Branch represents a GitHub branch
type Branch struct {
	Name      string       `json:"name"`
	Commit    BranchCommit `json:"commit"`
	Protected bool         `json:"protected"`
}

// BranchCommit contains commit info for a branch
type BranchCommit struct {
	SHA string `json:"sha"`
	URL string `json:"url"`
}

// SHA returns the commit SHA for the branch
func (b *Branch) SHA() string {
	return b.Commit.SHA
}

// Release represents a GitHub release
type Release struct {
	ID              int64     `json:"id"`
	TagName         string    `json:"tag_name"`
	TargetCommitish string    `json:"target_commitish"`
	Name            string    `json:"name"`
	Body            string    `json:"body"`
	Draft           bool      `json:"draft"`
	Prerelease      bool      `json:"prerelease"`
	HTMLURL         string    `json:"html_url"`
	CreatedAt       time.Time `json:"created_at"`
	PublishedAt     time.Time `json:"published_at"`
}

// ReleaseInput is the input for creating a release
type ReleaseInput struct {
	TagName         string `json:"tag_name"`
	TargetCommitish string `json:"target_commitish,omitempty"`
	Name            string `json:"name,omitempty"`
	Body            string `json:"body,omitempty"`
	Draft           bool   `json:"draft,omitempty"`
	Prerelease      bool   `json:"prerelease,omitempty"`
	GenerateNotes   bool   `json:"generate_release_notes,omitempty"`
}

// Tag represents a GitHub tag
type Tag struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// Commit represents a GitHub commit (for PR commit listing)
type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string    `json:"name"`
			Email string    `json:"email"`
			Date  time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}
