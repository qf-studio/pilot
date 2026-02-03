package azuredevops

import "time"

// Config holds Azure DevOps adapter configuration
type Config struct {
	Enabled           bool                     `yaml:"enabled"`
	PAT               string                   `yaml:"pat"`             // Personal Access Token
	Organization      string                   `yaml:"organization"`    // Azure DevOps org name
	Project           string                   `yaml:"project"`         // Project name
	Repository        string                   `yaml:"repository"`      // Repository name (optional, defaults to project)
	BaseURL           string                   `yaml:"base_url"`        // Default: https://dev.azure.com
	WebhookSecret     string                   `yaml:"webhook_secret"`  // For basic auth on webhook endpoint
	PilotTag          string                   `yaml:"pilot_tag"`       // Tag to watch (Azure uses tags, not labels)
	WorkItemTypes     []string                 `yaml:"work_item_types"` // e.g., ["Bug", "Task", "User Story"]
	Polling           *PollingConfig           `yaml:"polling"`
	StaleLabelCleanup *StaleLabelCleanupConfig `yaml:"stale_label_cleanup"`
}

// PollingConfig holds Azure DevOps polling settings
type PollingConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"` // Poll interval (default 30s)
}

// StaleLabelCleanupConfig holds settings for auto-cleanup of stale pilot-in-progress tags
type StaleLabelCleanupConfig struct {
	Enabled   bool          `yaml:"enabled"`
	Interval  time.Duration `yaml:"interval"`  // How often to check for stale tags (default: 30m)
	Threshold time.Duration `yaml:"threshold"` // How long before a tag is considered stale (default: 1h)
}

// DefaultConfig returns default Azure DevOps configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:  false,
		BaseURL:  "https://dev.azure.com",
		PilotTag: "pilot",
		WorkItemTypes: []string{
			"Bug",
			"Task",
			"User Story",
		},
		Polling: &PollingConfig{
			Enabled:  false,
			Interval: 30 * time.Second,
		},
		StaleLabelCleanup: &StaleLabelCleanupConfig{
			Enabled:   true,
			Interval:  30 * time.Minute,
			Threshold: 1 * time.Hour,
		},
	}
}

// ListWorkItemsOptions holds options for listing work items via WIQL
type ListWorkItemsOptions struct {
	Tags          []string
	States        []string // New, Active, Resolved, Closed
	WorkItemTypes []string
	UpdatedAfter  time.Time
}

// Work item states
const (
	StateNew      = "New"
	StateActive   = "Active"
	StateResolved = "Resolved"
	StateClosed   = "Closed"
)

// Tag names used by Pilot
const (
	TagInProgress = "pilot-in-progress"
	TagDone       = "pilot-done"
	TagFailed     = "pilot-failed"
)

// Priority mapping from Azure DevOps priority field
type Priority int

const (
	PriorityNone   Priority = 0
	PriorityUrgent Priority = 1
	PriorityHigh   Priority = 2
	PriorityMedium Priority = 3
	PriorityLow    Priority = 4
)

// PriorityFromValue converts Azure DevOps priority value to internal Priority
// Azure DevOps uses numeric priority: 1 = highest, 4 = lowest
func PriorityFromValue(value int) Priority {
	switch value {
	case 1:
		return PriorityUrgent
	case 2:
		return PriorityHigh
	case 3:
		return PriorityMedium
	case 4:
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

// Pull request states
const (
	PRStateActive    = "active"
	PRStateCompleted = "completed"
	PRStateAbandoned = "abandoned"
)

// Merge status values
const (
	MergeStatusSucceeded = "succeeded"
	MergeStatusConflicts = "conflicts"
	MergeStatusFailure   = "failure"
	MergeStatusQueued    = "queued"
)

// WorkItem represents an Azure DevOps work item
type WorkItem struct {
	ID     int                    `json:"id"`
	Rev    int                    `json:"rev"`
	Fields map[string]interface{} `json:"fields"`
	URL    string                 `json:"url"`
}

// GetTitle returns the work item title
func (w *WorkItem) GetTitle() string {
	if title, ok := w.Fields["System.Title"].(string); ok {
		return title
	}
	return ""
}

// GetDescription returns the work item description (HTML)
func (w *WorkItem) GetDescription() string {
	// Azure DevOps stores description in System.Description for Bugs
	// and in System.Description or Microsoft.VSTS.TCM.ReproSteps for different types
	if desc, ok := w.Fields["System.Description"].(string); ok {
		return desc
	}
	if desc, ok := w.Fields["Microsoft.VSTS.TCM.ReproSteps"].(string); ok {
		return desc
	}
	return ""
}

// GetState returns the work item state
func (w *WorkItem) GetState() string {
	if state, ok := w.Fields["System.State"].(string); ok {
		return state
	}
	return ""
}

// GetWorkItemType returns the work item type (Bug, Task, User Story, etc.)
func (w *WorkItem) GetWorkItemType() string {
	if wit, ok := w.Fields["System.WorkItemType"].(string); ok {
		return wit
	}
	return ""
}

// GetTags returns the work item tags as a slice
// Azure DevOps stores tags as semicolon-separated string
func (w *WorkItem) GetTags() []string {
	if tagsStr, ok := w.Fields["System.Tags"].(string); ok && tagsStr != "" {
		return splitTags(tagsStr)
	}
	return nil
}

// HasTag checks if the work item has a specific tag
func (w *WorkItem) HasTag(tag string) bool {
	for _, t := range w.GetTags() {
		if t == tag {
			return true
		}
	}
	return false
}

// GetPriority returns the work item priority as internal Priority type
func (w *WorkItem) GetPriority() Priority {
	if priority, ok := w.Fields["Microsoft.VSTS.Common.Priority"].(float64); ok {
		return PriorityFromValue(int(priority))
	}
	return PriorityNone
}

// GetCreatedDate returns the work item creation date
func (w *WorkItem) GetCreatedDate() time.Time {
	if dateStr, ok := w.Fields["System.CreatedDate"].(string); ok {
		t, _ := time.Parse(time.RFC3339, dateStr)
		return t
	}
	return time.Time{}
}

// GetChangedDate returns the work item last changed date
func (w *WorkItem) GetChangedDate() time.Time {
	if dateStr, ok := w.Fields["System.ChangedDate"].(string); ok {
		t, _ := time.Parse(time.RFC3339, dateStr)
		return t
	}
	return time.Time{}
}

// GetWebURL constructs the web URL for the work item
func (w *WorkItem) GetWebURL(baseURL, organization, project string) string {
	return baseURL + "/" + organization + "/" + project + "/_workitems/edit/" + string(rune(w.ID))
}

// PullRequest represents an Azure DevOps pull request
type PullRequest struct {
	PullRequestID         int           `json:"pullRequestId"`
	Title                 string        `json:"title"`
	Description           string        `json:"description"`
	Status                string        `json:"status"` // active, completed, abandoned
	SourceRefName         string        `json:"sourceRefName"`
	TargetRefName         string        `json:"targetRefName"`
	MergeStatus           string        `json:"mergeStatus"`
	IsDraft               bool          `json:"isDraft"`
	CreationDate          time.Time     `json:"creationDate"`
	ClosedDate            time.Time     `json:"closedDate,omitempty"`
	URL                   string        `json:"url"`
	Repository            *GitRepo      `json:"repository,omitempty"`
	CreatedBy             *Identity     `json:"createdBy,omitempty"`
	MergeID               string        `json:"mergeId,omitempty"`
	LastMergeSourceCommit *GitCommitRef `json:"lastMergeSourceCommit,omitempty"`
}

// GitRepo represents a Git repository reference
type GitRepo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Project *Project `json:"project,omitempty"`
}

// Project represents an Azure DevOps project reference
type Project struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	State string `json:"state"`
}

// Identity represents an Azure DevOps user identity
type Identity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
	URL         string `json:"url"`
	ImageURL    string `json:"imageUrl,omitempty"`
}

// GitCommitRef represents a Git commit reference
type GitCommitRef struct {
	CommitID string `json:"commitId"`
	URL      string `json:"url"`
}

// PullRequestInput is used for creating pull requests
type PullRequestInput struct {
	Title         string `json:"title"`
	Description   string `json:"description,omitempty"`
	SourceRefName string `json:"sourceRefName"` // refs/heads/branch-name
	TargetRefName string `json:"targetRefName"` // refs/heads/main
	IsDraft       bool   `json:"isDraft,omitempty"`
}

// GitRef represents a Git reference (branch/tag)
type GitRef struct {
	Name     string `json:"name"`     // refs/heads/branch-name
	ObjectID string `json:"objectId"` // Commit SHA
	URL      string `json:"url"`
}

// GitRefUpdate is used for creating/updating branches
type GitRefUpdate struct {
	Name        string `json:"name"`        // refs/heads/branch-name
	OldObjectID string `json:"oldObjectId"` // 40 zeros for new branch
	NewObjectID string `json:"newObjectId"` // Target commit SHA
}

// Comment represents a work item comment
type Comment struct {
	ID           int       `json:"id"`
	Text         string    `json:"text"`
	CreatedBy    *Identity `json:"createdBy,omitempty"`
	CreatedDate  time.Time `json:"createdDate"`
	ModifiedDate time.Time `json:"modifiedDate,omitempty"`
}

// WIQLQueryResult represents the result of a WIQL query
type WIQLQueryResult struct {
	QueryType         string                 `json:"queryType"`
	QueryResultType   string                 `json:"queryResultType"`
	AsOf              time.Time              `json:"asOf"`
	Columns           []WIQLColumn           `json:"columns"`
	WorkItems         []WIQLWorkItemRef      `json:"workItems"`
	WorkItemRelations []WIQLWorkItemRelation `json:"workItemRelations,omitempty"`
}

// WIQLColumn represents a column in WIQL results
type WIQLColumn struct {
	ReferenceName string `json:"referenceName"`
	Name          string `json:"name"`
	URL           string `json:"url"`
}

// WIQLWorkItemRef represents a work item reference in WIQL results
type WIQLWorkItemRef struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

// WIQLWorkItemRelation represents a work item relation in WIQL results
type WIQLWorkItemRelation struct {
	Target *WIQLWorkItemRef `json:"target"`
	Rel    string           `json:"rel,omitempty"`
	Source *WIQLWorkItemRef `json:"source,omitempty"`
}

// Webhook payload types

// WebhookPayload is the base webhook payload from Azure DevOps
type WebhookPayload struct {
	SubscriptionID     string                      `json:"subscriptionId"`
	NotificationID     int                         `json:"notificationId"`
	ID                 string                      `json:"id"`
	EventType          string                      `json:"eventType"`
	PublisherID        string                      `json:"publisherId"`
	Message            *WebhookMessage             `json:"message,omitempty"`
	DetailedMessage    *WebhookMessage             `json:"detailedMessage,omitempty"`
	Resource           map[string]interface{}      `json:"resource"`
	ResourceVersion    string                      `json:"resourceVersion"`
	ResourceContainers map[string]WebhookContainer `json:"resourceContainers"`
	CreatedDate        time.Time                   `json:"createdDate"`
}

// WebhookMessage contains the message text for webhooks
type WebhookMessage struct {
	Text     string `json:"text"`
	HTML     string `json:"html,omitempty"`
	Markdown string `json:"markdown,omitempty"`
}

// WebhookContainer contains resource container info
type WebhookContainer struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
}

// WorkItemWebhookResource is the resource payload for work item webhooks
type WorkItemWebhookResource struct {
	ID          int                    `json:"id"`
	WorkItemID  int                    `json:"workItemId,omitempty"`
	Rev         int                    `json:"rev"`
	RevisedBy   *Identity              `json:"revisedBy,omitempty"`
	RevisedDate time.Time              `json:"revisedDate"`
	Fields      map[string]interface{} `json:"fields"`
	URL         string                 `json:"url"`
	Revision    *WorkItemRevision      `json:"revision,omitempty"`
}

// WorkItemRevision represents a work item revision in webhook payload
type WorkItemRevision struct {
	ID     int                    `json:"id"`
	Rev    int                    `json:"rev"`
	Fields map[string]interface{} `json:"fields"`
	URL    string                 `json:"url"`
}

// Webhook event types
const (
	WebhookEventWorkItemCreated  = "workitem.created"
	WebhookEventWorkItemUpdated  = "workitem.updated"
	WebhookEventWorkItemDeleted  = "workitem.deleted"
	WebhookEventWorkItemRestored = "workitem.restored"
	WebhookEventPRCreated        = "git.pullrequest.created"
	WebhookEventPRUpdated        = "git.pullrequest.updated"
	WebhookEventPRMerged         = "git.pullrequest.merged"
)

// Helper functions

// splitTags splits a semicolon-separated tag string into a slice
func splitTags(tags string) []string {
	if tags == "" {
		return nil
	}
	var result []string
	for _, tag := range splitAndTrim(tags, ";") {
		if tag != "" {
			result = append(result, tag)
		}
	}
	return result
}

// splitAndTrim splits a string and trims whitespace from each part
func splitAndTrim(s string, sep string) []string {
	parts := make([]string, 0)
	current := ""
	for i := 0; i < len(s); i++ {
		if i+len(sep) <= len(s) && s[i:i+len(sep)] == sep {
			trimmed := trimSpace(current)
			if trimmed != "" {
				parts = append(parts, trimmed)
			}
			current = ""
			i += len(sep) - 1
		} else {
			current += string(s[i])
		}
	}
	if trimmed := trimSpace(current); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return parts
}

// trimSpace trims leading and trailing whitespace
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// joinTags joins tags into a semicolon-separated string
func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	result := tags[0]
	for i := 1; i < len(tags); i++ {
		result += "; " + tags[i]
	}
	return result
}

// addTag adds a tag to the tags string if not already present
func addTag(tagsStr, newTag string) string {
	tags := splitTags(tagsStr)
	for _, t := range tags {
		if t == newTag {
			return tagsStr // Already has tag
		}
	}
	tags = append(tags, newTag)
	return joinTags(tags)
}

// removeTag removes a tag from the tags string
func removeTag(tagsStr, tagToRemove string) string {
	tags := splitTags(tagsStr)
	result := make([]string, 0, len(tags))
	for _, t := range tags {
		if t != tagToRemove {
			result = append(result, t)
		}
	}
	return joinTags(result)
}
