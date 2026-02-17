package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	linearAPIURL = "https://api.linear.app/graphql"
)

// Client is a Linear API client
type Client struct {
	apiKey     string
	httpClient *http.Client

	doneStateMu    sync.RWMutex
	doneStateCache map[string]string
}

// NewClient creates a new Linear client
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		doneStateCache: make(map[string]string),
	}
}

// Issue represents a Linear issue
type Issue struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Priority    int       `json:"priority"`
	State       State     `json:"state"`
	Labels      []Label   `json:"labels"`
	Assignee    *User     `json:"assignee"`
	Project     *Project  `json:"project"`
	Team        Team      `json:"team"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// State represents an issue state
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Label represents a Linear label
type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User represents a Linear user
type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Project represents a Linear project
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Team represents a Linear team
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// GraphQLRequest represents a GraphQL request
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse represents a GraphQL response
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError represents a GraphQL error
type GraphQLError struct {
	Message string `json:"message"`
}

// Execute executes a GraphQL query
func (c *Client) Execute(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	reqBody := GraphQLRequest{
		Query:     query,
		Variables: variables,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearAPIURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error: %s", string(respBody))
	}

	var gqlResp GraphQLResponse
	if err := json.Unmarshal(respBody, &gqlResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		return fmt.Errorf("GraphQL error: %s", gqlResp.Errors[0].Message)
	}

	if result != nil {
		if err := json.Unmarshal(gqlResp.Data, result); err != nil {
			return fmt.Errorf("failed to parse data: %w", err)
		}
	}

	return nil
}

// GetIssue fetches an issue by ID
func (c *Client) GetIssue(ctx context.Context, id string) (*Issue, error) {
	query := `
		query GetIssue($id: String!) {
			issue(id: $id) {
				id
				identifier
				title
				description
				priority
				state {
					id
					name
					type
				}
				labels {
					nodes {
						id
						name
					}
				}
				assignee {
					id
					name
					email
				}
				project {
					id
					name
				}
				team {
					id
					name
					key
				}
				createdAt
				updatedAt
			}
		}
	`

	var result struct {
		Issue Issue `json:"issue"`
	}

	if err := c.Execute(ctx, query, map[string]interface{}{"id": id}, &result); err != nil {
		return nil, err
	}

	return &result.Issue, nil
}

// UpdateIssueState updates an issue's state
func (c *Client) UpdateIssueState(ctx context.Context, issueID, stateID string) error {
	mutation := `
		mutation UpdateIssue($id: String!, $stateId: String!) {
			issueUpdate(id: $id, input: { stateId: $stateId }) {
				success
			}
		}
	`

	return c.Execute(ctx, mutation, map[string]interface{}{
		"id":      issueID,
		"stateId": stateID,
	}, nil)
}

// AddComment adds a comment to an issue
func (c *Client) AddComment(ctx context.Context, issueID, body string) error {
	mutation := `
		mutation CreateComment($issueId: String!, $body: String!) {
			commentCreate(input: { issueId: $issueId, body: $body }) {
				success
			}
		}
	`

	return c.Execute(ctx, mutation, map[string]interface{}{
		"issueId": issueID,
		"body":    body,
	}, nil)
}

// ListIssuesOptions configures issue listing
type ListIssuesOptions struct {
	TeamID     string
	Label      string
	ProjectIDs []string
	States     []string // e.g., ["backlog", "unstarted", "started"]
}

// ListIssues fetches issues matching the filter criteria
func (c *Client) ListIssues(ctx context.Context, opts *ListIssuesOptions) ([]*Issue, error) {
	query := `
		query ListIssues($teamId: String!, $label: String!, $states: [String!]) {
			issues(
				filter: {
					team: { key: { eq: $teamId } }
					labels: { name: { eq: $label } }
					state: { type: { in: $states } }
				}
				first: 50
				orderBy: createdAt
			) {
				nodes {
					id
					identifier
					title
					description
					priority
					state { id name type }
					labels { nodes { id name } }
					assignee { id name email }
					project { id name }
					team { id name key }
					createdAt
					updatedAt
				}
			}
		}
	`

	states := opts.States
	if len(states) == 0 {
		states = []string{"backlog", "unstarted", "started"}
	}

	variables := map[string]interface{}{
		"teamId": opts.TeamID,
		"label":  opts.Label,
		"states": states,
	}

	var result struct {
		Issues struct {
			Nodes []*issueListItem `json:"nodes"`
		} `json:"issues"`
	}

	if err := c.Execute(ctx, query, variables, &result); err != nil {
		return nil, err
	}

	// Convert responses to Issue objects
	issues := make([]*Issue, 0, len(result.Issues.Nodes))
	for _, resp := range result.Issues.Nodes {
		issue := resp.toIssue()
		// Filter by project if specified
		if len(opts.ProjectIDs) > 0 {
			if issue.Project == nil || !containsString(opts.ProjectIDs, issue.Project.ID) {
				continue
			}
		}
		issues = append(issues, issue)
	}

	return issues, nil
}

// issueListItem is the raw GraphQL response for an issue (labels have nested nodes)
type issueListItem struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	State       State  `json:"state"`
	Labels      struct {
		Nodes []Label `json:"nodes"`
	} `json:"labels"`
	Assignee  *User     `json:"assignee"`
	Project   *Project  `json:"project"`
	Team      Team      `json:"team"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// toIssue converts the response to an Issue
func (r *issueListItem) toIssue() *Issue {
	return &Issue{
		ID:          r.ID,
		Identifier:  r.Identifier,
		Title:       r.Title,
		Description: r.Description,
		Priority:    r.Priority,
		State:       r.State,
		Labels:      r.Labels.Nodes,
		Assignee:    r.Assignee,
		Project:     r.Project,
		Team:        r.Team,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// containsString checks if a slice contains a string
func containsString(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// AddLabel adds a label to an issue
func (c *Client) AddLabel(ctx context.Context, issueID, labelID string) error {
	mutation := `
		mutation AddLabel($issueId: String!, $labelId: String!) {
			issueAddLabel(id: $issueId, labelId: $labelId) {
				success
			}
		}
	`
	return c.Execute(ctx, mutation, map[string]interface{}{
		"issueId": issueID,
		"labelId": labelID,
	}, nil)
}

// RemoveLabel removes a label from an issue
func (c *Client) RemoveLabel(ctx context.Context, issueID, labelID string) error {
	mutation := `
		mutation RemoveLabel($issueId: String!, $labelId: String!) {
			issueRemoveLabel(id: $issueId, labelId: $labelId) {
				success
			}
		}
	`
	return c.Execute(ctx, mutation, map[string]interface{}{
		"issueId": issueID,
		"labelId": labelID,
	}, nil)
}

// GetLabelByName fetches a label ID by name for a team
func (c *Client) GetLabelByName(ctx context.Context, teamID, labelName string) (string, error) {
	query := `
		query GetLabel($teamId: String!, $name: String!) {
			issueLabels(filter: { team: { key: { eq: $teamId } }, name: { eq: $name } }) {
				nodes { id name }
			}
		}
	`
	var result struct {
		IssueLabels struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}

	if err := c.Execute(ctx, query, map[string]interface{}{
		"teamId": teamID,
		"name":   labelName,
	}, &result); err != nil {
		return "", err
	}

	if len(result.IssueLabels.Nodes) == 0 {
		return "", fmt.Errorf("label %q not found in team %s", labelName, teamID)
	}

	return result.IssueLabels.Nodes[0].ID, nil
}

// CreateLabel creates a new label in a team and returns its ID.
// GH-1351: Used to auto-create pilot status labels (pilot-in-progress, pilot-done, pilot-failed).
func (c *Client) CreateLabel(ctx context.Context, teamID, labelName, color string) (string, error) {
	mutation := `
		mutation CreateLabel($teamId: String!, $name: String!, $color: String!) {
			issueLabelCreate(input: { teamId: $teamId, name: $name, color: $color }) {
				success
				issueLabel {
					id
					name
				}
			}
		}
	`

	var result struct {
		IssueLabelCreate struct {
			Success    bool `json:"success"`
			IssueLabel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"issueLabel"`
		} `json:"issueLabelCreate"`
	}

	if err := c.Execute(ctx, mutation, map[string]interface{}{
		"teamId": teamID,
		"name":   labelName,
		"color":  color,
	}, &result); err != nil {
		return "", err
	}

	if !result.IssueLabelCreate.Success {
		return "", fmt.Errorf("failed to create label %q in team %s", labelName, teamID)
	}

	return result.IssueLabelCreate.IssueLabel.ID, nil
}

// GetOrCreateLabel fetches a label ID by name, creating it if it doesn't exist.
// GH-1351: Ensures pilot status labels exist for deduplication.
func (c *Client) GetOrCreateLabel(ctx context.Context, teamID, labelName, color string) (string, error) {
	id, err := c.GetLabelByName(ctx, teamID, labelName)
	if err == nil {
		return id, nil
	}

	// Label doesn't exist, create it
	return c.CreateLabel(ctx, teamID, labelName, color)
}

// CreateIssue creates a new issue in Linear with team/project context from parent issue.
// This satisfies the SubIssueCreator interface for epic decomposition.
// parentID: Linear issue ID to get team/project context from
// title: Issue title
// body: Issue description (parent reference will be prepended)
// labels: Label names to apply (will call GetOrCreateLabel for each)
// Returns: issueID (Linear identifier like APP-123), issueURL, error
func (c *Client) CreateIssue(ctx context.Context, parentID, title, body string, labels []string) (string, string, error) {
	// Fetch parent issue to get team/project context
	parent, err := c.GetIssue(ctx, parentID)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch parent issue %s: %w", parentID, err)
	}

	// Build body with parent reference
	bodyWithParent := fmt.Sprintf("Parent: %s\n\n%s", parentID, body)

	// Get or create "Pilot" label
	pilotLabelID, err := c.GetOrCreateLabel(ctx, parent.Team.Key, "Pilot", "#7ec699")
	if err != nil {
		return "", "", fmt.Errorf("failed to get/create Pilot label: %w", err)
	}

	// Collect all label IDs
	labelIDs := []string{pilotLabelID}
	for _, labelName := range labels {
		if labelName != "Pilot" { // Avoid duplicates
			labelID, err := c.GetOrCreateLabel(ctx, parent.Team.Key, labelName, "#8b949e")
			if err != nil {
				return "", "", fmt.Errorf("failed to get/create label %s: %w", labelName, err)
			}
			labelIDs = append(labelIDs, labelID)
		}
	}

	// Create issue using issueCreate mutation
	mutation := `
		mutation CreateIssue($teamId: String!, $title: String!, $description: String, $labelIds: [String!], $projectId: String) {
			issueCreate(input: {
				teamId: $teamId,
				title: $title,
				description: $description,
				labelIds: $labelIds,
				projectId: $projectId
			}) {
				success
				issue {
					id
					identifier
					url
				}
			}
		}
	`

	variables := map[string]interface{}{
		"teamId":      parent.Team.ID,
		"title":       title,
		"description": bodyWithParent,
		"labelIds":    labelIDs,
	}

	// Include project if parent has one
	if parent.Project != nil {
		variables["projectId"] = parent.Project.ID
	}

	var result struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}

	if err := c.Execute(ctx, mutation, variables, &result); err != nil {
		return "", "", fmt.Errorf("failed to create issue: %w", err)
	}

	if !result.IssueCreate.Success {
		return "", "", fmt.Errorf("issueCreate returned success=false")
	}

	return result.IssueCreate.Issue.Identifier, result.IssueCreate.Issue.URL, nil
}

// GetTeamDoneStateID returns the cached "completed" workflow state ID for a team.
// It queries the Linear API on first call and caches the result.
func (c *Client) GetTeamDoneStateID(ctx context.Context, teamKey string) (string, error) {
	// Check cache with read lock
	c.doneStateMu.RLock()
	if id, ok := c.doneStateCache[teamKey]; ok {
		c.doneStateMu.RUnlock()
		return id, nil
	}
	c.doneStateMu.RUnlock()

	// Query Linear API for completed state
	query := `
		query GetTeamDoneState($teamKey: String!) {
			workflowStates(filter: { team: { key: { eq: $teamKey } }, type: { eq: "completed" } }) {
				nodes {
					id
					name
					type
				}
			}
		}
	`

	var result struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}

	if err := c.Execute(ctx, query, map[string]interface{}{"teamKey": teamKey}, &result); err != nil {
		return "", err
	}

	if len(result.WorkflowStates.Nodes) == 0 {
		return "", fmt.Errorf("no completed state found for team %s", teamKey)
	}

	stateID := result.WorkflowStates.Nodes[0].ID

	// Store in cache with write lock
	c.doneStateMu.Lock()
	c.doneStateCache[teamKey] = stateID
	c.doneStateMu.Unlock()

	return stateID, nil
}
