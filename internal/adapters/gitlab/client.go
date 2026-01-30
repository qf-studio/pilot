package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	gitlabAPIURL = "https://gitlab.com"
)

// Client is a GitLab API client
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string // For testing - defaults to gitlabAPIURL
	projectID  string // URL-encoded project path (namespace%2Fproject)
}

// NewClient creates a new GitLab client
// project should be in "namespace/project" format
func NewClient(token, project string) *Client {
	return &Client{
		token:     token,
		baseURL:   gitlabAPIURL,
		projectID: url.PathEscape(project),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new GitLab client with a custom base URL (for testing)
func NewClientWithBaseURL(token, project, baseURL string) *Client {
	return &Client{
		token:     token,
		baseURL:   baseURL,
		projectID: url.PathEscape(project),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doRequest performs an HTTP request to the GitLab API
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
	}

	return nil
}

// GetProject fetches project info
func (c *Client) GetProject(ctx context.Context) (*Project, error) {
	path := fmt.Sprintf("/api/v4/projects/%s", c.projectID)
	var project Project
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// GetIssue fetches an issue by IID (project-scoped issue number)
func (c *Client) GetIssue(ctx context.Context, iid int) (*Issue, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", c.projectID, iid)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// ListIssues lists issues for the project with optional filters
func (c *Client) ListIssues(ctx context.Context, opts *ListIssuesOptions) ([]*Issue, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/issues?", c.projectID)

	// Build query parameters
	params := []string{}
	if opts != nil {
		if len(opts.Labels) > 0 {
			for _, label := range opts.Labels {
				params = append(params, "labels="+url.QueryEscape(label))
			}
		}
		if opts.State != "" {
			params = append(params, "state="+opts.State)
		}
		if opts.Sort != "" {
			params = append(params, "sort="+opts.Sort)
		}
		if opts.OrderBy != "" {
			params = append(params, "order_by="+opts.OrderBy)
		}
		if !opts.UpdatedAt.IsZero() {
			params = append(params, "updated_after="+opts.UpdatedAt.Format(time.RFC3339))
		}
	}

	for i, p := range params {
		if i > 0 {
			path += "&"
		}
		path += p
	}

	var issues []*Issue
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// AddIssueNote adds a comment (note) to an issue
func (c *Client) AddIssueNote(ctx context.Context, iid int, body string) (*Note, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d/notes", c.projectID, iid)
	reqBody := map[string]string{"body": body}
	var note Note
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &note); err != nil {
		return nil, err
	}
	return &note, nil
}

// AddIssueLabels adds labels to an issue
func (c *Client) AddIssueLabels(ctx context.Context, iid int, labels []string) error {
	// GitLab uses PUT to update issue, and labels are comma-separated
	// First get current labels, then add new ones
	issue, err := c.GetIssue(ctx, iid)
	if err != nil {
		return fmt.Errorf("failed to get issue: %w", err)
	}

	// Merge existing and new labels
	labelSet := make(map[string]bool)
	for _, l := range issue.Labels {
		labelSet[l] = true
	}
	for _, l := range labels {
		labelSet[l] = true
	}

	// Convert back to slice
	allLabels := make([]string, 0, len(labelSet))
	for l := range labelSet {
		allLabels = append(allLabels, l)
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", c.projectID, iid)
	reqBody := map[string]interface{}{"labels": allLabels}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// RemoveIssueLabel removes a label from an issue
func (c *Client) RemoveIssueLabel(ctx context.Context, iid int, label string) error {
	// GitLab uses PUT to update issue, need to get current labels and remove the one
	issue, err := c.GetIssue(ctx, iid)
	if err != nil {
		// 404 is OK - issue might not exist
		return nil
	}

	// Remove the label from the list
	newLabels := make([]string, 0, len(issue.Labels))
	found := false
	for _, l := range issue.Labels {
		if l != label {
			newLabels = append(newLabels, l)
		} else {
			found = true
		}
	}

	// If label wasn't there, nothing to do
	if !found {
		return nil
	}

	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", c.projectID, iid)
	reqBody := map[string]interface{}{"labels": newLabels}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// CreateMergeRequest creates a new merge request
func (c *Client) CreateMergeRequest(ctx context.Context, input *MergeRequestInput) (*MergeRequest, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests", c.projectID)
	var mr MergeRequest
	if err := c.doRequest(ctx, http.MethodPost, path, input, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// GetMergeRequest fetches a merge request by IID
func (c *Client) GetMergeRequest(ctx context.Context, iid int) (*MergeRequest, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d", c.projectID, iid)
	var mr MergeRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// MergeMergeRequest merges a merge request
func (c *Client) MergeMergeRequest(ctx context.Context, iid int, squash bool) (*MergeRequest, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/merge", c.projectID, iid)
	reqBody := map[string]interface{}{
		"squash": squash,
	}
	var mr MergeRequest
	if err := c.doRequest(ctx, http.MethodPut, path, reqBody, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// ListMergeRequests lists merge requests for the project
func (c *Client) ListMergeRequests(ctx context.Context, state string) ([]*MergeRequest, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests?state=%s", c.projectID, state)
	var mrs []*MergeRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &mrs); err != nil {
		return nil, err
	}
	return mrs, nil
}

// GetPipeline fetches a pipeline by ID
func (c *Client) GetPipeline(ctx context.Context, pipelineID int) (*Pipeline, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/pipelines/%d", c.projectID, pipelineID)
	var pipeline Pipeline
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &pipeline); err != nil {
		return nil, err
	}
	return &pipeline, nil
}

// HasLabel checks if an issue has a specific label
func HasLabel(issue *Issue, labelName string) bool {
	for _, label := range issue.Labels {
		if label == labelName {
			return true
		}
	}
	return false
}

// UpdateIssueState updates an issue's state (close/reopen)
func (c *Client) UpdateIssueState(ctx context.Context, iid int, state string) error {
	path := fmt.Sprintf("/api/v4/projects/%s/issues/%d", c.projectID, iid)
	// GitLab uses state_event: "close" or "reopen"
	stateEvent := "close"
	if state == StateOpened {
		stateEvent = "reopen"
	}
	reqBody := map[string]string{"state_event": stateEvent}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// AddMergeRequestNote adds a comment to a merge request
func (c *Client) AddMergeRequestNote(ctx context.Context, iid int, body string) (*Note, error) {
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes", c.projectID, iid)
	reqBody := map[string]string{"body": body}
	var note Note
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &note); err != nil {
		return nil, err
	}
	return &note, nil
}
