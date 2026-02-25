package plane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is a Plane.so REST API client.
// Auth: X-API-Key header.
// Rate limit: 60 req/min (respects Retry-After header).
type Client struct {
	baseURL          string
	apiKey           string
	httpClient       *http.Client
	workspaceSlug    string // optional: used by CreateIssue (SubIssueCreator)
	defaultProjectID string // optional: fallback project for CreateIssue
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithWorkspaceSlug sets the workspace slug for SubIssueCreator operations.
func WithWorkspaceSlug(slug string) ClientOption {
	return func(c *Client) {
		c.workspaceSlug = slug
	}
}

// WithDefaultProjectID sets the default project ID for SubIssueCreator operations.
func WithDefaultProjectID(projectID string) ClientOption {
	return func(c *Client) {
		c.defaultProjectID = projectID
	}
}

// NewClient creates a new Plane API client.
// baseURL should be e.g. "https://api.plane.so" (no trailing slash).
func NewClient(baseURL, apiKey string, opts ...ClientOption) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	c := &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// doRequest performs an HTTP request to the Plane API.
// It handles JSON marshalling, auth headers, rate-limit retries, and error responses.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle rate limiting (429)
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := resp.Header.Get("Retry-After")
		secs, parseErr := strconv.Atoi(retryAfter)
		if parseErr != nil || secs <= 0 {
			secs = 5 // default backoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(secs) * time.Second):
		}
		// Retry once after waiting
		return c.doRequest(ctx, method, path, body, result)
	}

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

// workItemsBase returns the base path for work-items endpoints.
func workItemsBase(workspaceSlug, projectID string) string {
	return fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/work-items", workspaceSlug, projectID)
}

// ListWorkItems fetches work items for a project, optionally filtered by label ID.
// Pass empty labelID to list all work items.
func (c *Client) ListWorkItems(ctx context.Context, workspaceSlug, projectID, labelID string) ([]WorkItem, error) {
	path := workItemsBase(workspaceSlug, projectID) + "/"
	if labelID != "" {
		path += "?label=" + labelID
	}

	var resp paginatedResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// GetWorkItem fetches a single work item by ID.
func (c *Client) GetWorkItem(ctx context.Context, workspaceSlug, projectID, workItemID string) (*WorkItem, error) {
	path := workItemsBase(workspaceSlug, projectID) + "/" + workItemID + "/"

	var item WorkItem
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// UpdateWorkItem patches a work item with the given fields.
// Common fields: "state" (state UUID), "priority" (int), "labels" ([]string UUIDs).
func (c *Client) UpdateWorkItem(ctx context.Context, workspaceSlug, projectID, workItemID string, fields map[string]interface{}) error {
	path := workItemsBase(workspaceSlug, projectID) + "/" + workItemID + "/"
	return c.doRequest(ctx, http.MethodPatch, path, fields, nil)
}

// CreateWorkItem creates a new work item in a project.
// Required fields typically include "name". Optional: "description_html", "state", "priority", "labels".
func (c *Client) CreateWorkItem(ctx context.Context, workspaceSlug, projectID string, fields map[string]interface{}) (*WorkItem, error) {
	path := workItemsBase(workspaceSlug, projectID) + "/"

	var item WorkItem
	if err := c.doRequest(ctx, http.MethodPost, path, fields, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// ListStates fetches all workflow states for a project.
func (c *Client) ListStates(ctx context.Context, workspaceSlug, projectID string) ([]State, error) {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/states/", workspaceSlug, projectID)

	var resp statesResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// ListLabels fetches all labels for a project.
func (c *Client) ListLabels(ctx context.Context, workspaceSlug, projectID string) ([]Label, error) {
	path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/labels/", workspaceSlug, projectID)

	var resp labelsResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// AddComment adds an HTML comment to a work item.
func (c *Client) AddComment(ctx context.Context, workspaceSlug, projectID, workItemID, commentHTML string) error {
	path := workItemsBase(workspaceSlug, projectID) + "/" + workItemID + "/comments/"
	body := map[string]string{
		"comment_html": commentHTML,
	}
	return c.doRequest(ctx, http.MethodPost, path, body, nil)
}

// AddLabel adds a label UUID to a work item's labels array.
// Plane manages labels as a UUID array on the work item (no separate label-issue endpoint).
// GH-1830: PATCH label array to add/remove labels.
func (c *Client) AddLabel(ctx context.Context, workspaceSlug, projectID, workItemID, labelID string) error {
	item, err := c.GetWorkItem(ctx, workspaceSlug, projectID, workItemID)
	if err != nil {
		return fmt.Errorf("AddLabel: get work item: %w", err)
	}
	// Check if already present
	for _, id := range item.LabelIDs {
		if id == labelID {
			return nil // already has the label
		}
	}
	updated := append(item.LabelIDs, labelID)
	return c.UpdateWorkItem(ctx, workspaceSlug, projectID, workItemID, map[string]interface{}{
		"labels": updated,
	})
}

// RemoveLabel removes a label UUID from a work item's labels array.
// GH-1830: PATCH label array to remove a specific label.
func (c *Client) RemoveLabel(ctx context.Context, workspaceSlug, projectID, workItemID, labelID string) error {
	item, err := c.GetWorkItem(ctx, workspaceSlug, projectID, workItemID)
	if err != nil {
		return fmt.Errorf("RemoveLabel: get work item: %w", err)
	}
	updated := make([]string, 0, len(item.LabelIDs))
	for _, id := range item.LabelIDs {
		if id != labelID {
			updated = append(updated, id)
		}
	}
	if len(updated) == len(item.LabelIDs) {
		return nil // label not present, nothing to do
	}
	return c.UpdateWorkItem(ctx, workspaceSlug, projectID, workItemID, map[string]interface{}{
		"labels": updated,
	})
}

// HasLabelID reports whether a work item has the given label UUID.
func HasLabelID(item *WorkItem, labelID string) bool {
	for _, id := range item.LabelIDs {
		if id == labelID {
			return true
		}
	}
	return false
}

// UpdateIssueState sets the workflow state of a work item by state UUID.
// GH-1832: Plane states are per-project UUIDs; use ResolveStateByGroup to find the UUID.
func (c *Client) UpdateIssueState(ctx context.Context, workspaceSlug, projectID, workItemID, stateID string) error {
	return c.UpdateWorkItem(ctx, workspaceSlug, projectID, workItemID, map[string]interface{}{
		"state": stateID,
	})
}

// ResolveStateByGroup fetches all states for a project and returns the first state UUID
// matching the given group (e.g. StateGroupStarted, StateGroupCompleted).
// Returns empty string if no state matches.
// GH-1832: Used to resolve per-project state UUIDs by their fixed group category.
func (c *Client) ResolveStateByGroup(ctx context.Context, workspaceSlug, projectID string, group StateGroup) (string, error) {
	states, err := c.ListStates(ctx, workspaceSlug, projectID)
	if err != nil {
		return "", fmt.Errorf("ResolveStateByGroup: %w", err)
	}
	for _, s := range states {
		if s.Group == group {
			return s.ID, nil
		}
	}
	return "", nil
}

// ListComments fetches all comments for a work item.
// GH-1832: Used for dedup check before posting PR comments.
func (c *Client) ListComments(ctx context.Context, workspaceSlug, projectID, workItemID string) ([]Comment, error) {
	path := workItemsBase(workspaceSlug, projectID) + "/" + workItemID + "/comments/"
	var resp commentsResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// AddCommentWithTracking adds an HTML comment with external_source and external_id fields.
// It checks existing comments for a matching external_id to prevent duplicates on retry.
// GH-1832: Posts PR URL as comment with dedup via external_id.
func (c *Client) AddCommentWithTracking(ctx context.Context, workspaceSlug, projectID, workItemID, commentHTML, externalSource, externalID string) error {
	// Check for duplicate before posting
	existing, err := c.ListComments(ctx, workspaceSlug, projectID, workItemID)
	if err == nil {
		for _, comment := range existing {
			if comment.ExternalID == externalID && comment.ExternalSource == externalSource {
				return nil // already posted
			}
		}
	}
	// Post the comment with tracking fields
	body := map[string]string{
		"comment_html":    commentHTML,
		"access":          "INTERNAL",
		"external_source": externalSource,
		"external_id":     externalID,
	}
	return c.doRequest(ctx, http.MethodPost, workItemsBase(workspaceSlug, projectID)+"/"+workItemID+"/comments/", body, nil)
}

// CreateIssue implements executor.SubIssueCreator for epic decomposition.
// GH-1833: Creates a child work item under parentID in the configured workspace/project.
// parentID is a Plane work item UUID; the new item is created in the same project.
// Returns the new work item ID and a URL for display.
func (c *Client) CreateIssue(ctx context.Context, parentID, title, body string, labels []string) (string, string, error) {
	if c.workspaceSlug == "" {
		return "", "", fmt.Errorf("CreateIssue: workspaceSlug not configured (use WithWorkspaceSlug)")
	}
	if c.defaultProjectID == "" {
		return "", "", fmt.Errorf("CreateIssue: defaultProjectID not configured (use WithDefaultProjectID)")
	}

	fields := map[string]interface{}{
		"name":             title,
		"description_html": "<p>" + body + "</p>",
	}
	if parentID != "" {
		fields["parent"] = parentID
	}

	item, err := c.CreateWorkItem(ctx, c.workspaceSlug, c.defaultProjectID, fields)
	if err != nil {
		return "", "", fmt.Errorf("CreateIssue: %w", err)
	}

	url := fmt.Sprintf("%s/workspaces/%s/projects/%s/work-items/%s",
		c.baseURL, c.workspaceSlug, c.defaultProjectID, item.ID)
	return item.ID, url, nil
}
