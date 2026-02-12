package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// BaseURL is the Asana API base URL
	BaseURL = "https://app.asana.com/api/1.0"
)

// Client is an Asana API client
type Client struct {
	baseURL     string
	accessToken string
	workspaceID string
	httpClient  *http.Client
}

// NewClient creates a new Asana client
func NewClient(accessToken, workspaceID string) *Client {
	return &Client{
		baseURL:     BaseURL,
		accessToken: accessToken,
		workspaceID: workspaceID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new Asana client with a custom base URL (for testing)
func NewClientWithBaseURL(baseURL, accessToken, workspaceID string) *Client {
	return &Client{
		baseURL:     strings.TrimSuffix(baseURL, "/"),
		accessToken: accessToken,
		workspaceID: workspaceID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// doRequest performs an HTTP request to the Asana API
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Bearer token authentication
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Accept", "application/json")
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
		// Try to parse Asana error response
		var apiResp APIResponse[interface{}]
		if err := json.Unmarshal(respBody, &apiResp); err == nil && len(apiResp.Errors) > 0 {
			return fmt.Errorf("asana API error (status %d): %s", resp.StatusCode, apiResp.Errors[0].Message)
		}
		return fmt.Errorf("asana API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
	}

	return nil
}

// GetTask fetches a task by GID
func (c *Client) GetTask(ctx context.Context, taskGID string) (*Task, error) {
	path := fmt.Sprintf("/tasks/%s", taskGID)
	var resp APIResponse[Task]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetTaskWithFields fetches a task with specific fields
func (c *Client) GetTaskWithFields(ctx context.Context, taskGID string, fields []string) (*Task, error) {
	path := fmt.Sprintf("/tasks/%s?opt_fields=%s", taskGID, strings.Join(fields, ","))
	var resp APIResponse[Task]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// UpdateTask updates task fields
func (c *Client) UpdateTask(ctx context.Context, taskGID string, data map[string]interface{}) (*Task, error) {
	path := fmt.Sprintf("/tasks/%s", taskGID)
	reqBody := map[string]interface{}{
		"data": data,
	}
	var resp APIResponse[Task]
	if err := c.doRequest(ctx, http.MethodPut, path, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// CompleteTask marks a task as completed
func (c *Client) CompleteTask(ctx context.Context, taskGID string) (*Task, error) {
	return c.UpdateTask(ctx, taskGID, map[string]interface{}{
		"completed": true,
	})
}

// AddComment adds a comment (story) to a task
func (c *Client) AddComment(ctx context.Context, taskGID, text string) (*Story, error) {
	path := fmt.Sprintf("/tasks/%s/stories", taskGID)
	reqBody := map[string]interface{}{
		"data": map[string]string{
			"text": text,
		},
	}
	var resp APIResponse[Story]
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// AddHTMLComment adds an HTML-formatted comment to a task
func (c *Client) AddHTMLComment(ctx context.Context, taskGID, htmlText string) (*Story, error) {
	path := fmt.Sprintf("/tasks/%s/stories", taskGID)
	reqBody := map[string]interface{}{
		"data": map[string]string{
			"html_text": htmlText,
		},
	}
	var resp APIResponse[Story]
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetTaskStories fetches stories (comments and activity) for a task
func (c *Client) GetTaskStories(ctx context.Context, taskGID string) ([]Story, error) {
	path := fmt.Sprintf("/tasks/%s/stories", taskGID)
	var resp PagedResponse[Story]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// AddTag adds a tag to a task
func (c *Client) AddTag(ctx context.Context, taskGID, tagGID string) error {
	path := fmt.Sprintf("/tasks/%s/addTag", taskGID)
	reqBody := map[string]interface{}{
		"data": map[string]string{
			"tag": tagGID,
		},
	}
	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// RemoveTag removes a tag from a task
func (c *Client) RemoveTag(ctx context.Context, taskGID, tagGID string) error {
	path := fmt.Sprintf("/tasks/%s/removeTag", taskGID)
	reqBody := map[string]interface{}{
		"data": map[string]string{
			"tag": tagGID,
		},
	}
	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// GetWorkspaceTags fetches all tags in the workspace
func (c *Client) GetWorkspaceTags(ctx context.Context) ([]Tag, error) {
	path := fmt.Sprintf("/workspaces/%s/tags", c.workspaceID)
	var resp PagedResponse[Tag]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// FindTagByName finds a tag by name in the workspace
func (c *Client) FindTagByName(ctx context.Context, name string) (*Tag, error) {
	tags, err := c.GetWorkspaceTags(ctx)
	if err != nil {
		return nil, err
	}
	for _, tag := range tags {
		if strings.EqualFold(tag.Name, name) {
			return &tag, nil
		}
	}
	return nil, nil
}

// CreateTag creates a new tag in the workspace
func (c *Client) CreateTag(ctx context.Context, name string) (*Tag, error) {
	path := "/tags"
	reqBody := map[string]interface{}{
		"data": map[string]string{
			"name":      name,
			"workspace": c.workspaceID,
		},
	}
	var resp APIResponse[Tag]
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// AddAttachment adds an external attachment (URL) to a task
func (c *Client) AddAttachment(ctx context.Context, taskGID, url, name string) (*Attachment, error) {
	path := fmt.Sprintf("/tasks/%s/attachments", taskGID)
	reqBody := map[string]interface{}{
		"data": map[string]interface{}{
			"resource_subtype": "external",
			"url":              url,
			"name":             name,
		},
	}
	var resp APIResponse[Attachment]
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetProject fetches project info
func (c *Client) GetProject(ctx context.Context, projectGID string) (*Project, error) {
	path := fmt.Sprintf("/projects/%s", projectGID)
	var resp APIResponse[Project]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// GetWorkspace fetches workspace info
func (c *Client) GetWorkspace(ctx context.Context) (*Workspace, error) {
	path := fmt.Sprintf("/workspaces/%s", c.workspaceID)
	var resp APIResponse[Workspace]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

// SearchTasks searches for tasks matching a query
func (c *Client) SearchTasks(ctx context.Context, query string) ([]Task, error) {
	path := fmt.Sprintf("/workspaces/%s/tasks/search?text=%s", c.workspaceID, query)
	var resp PagedResponse[Task]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// GetTasksByTag fetches tasks with a specific tag
func (c *Client) GetTasksByTag(ctx context.Context, tagGID string) ([]Task, error) {
	path := fmt.Sprintf("/tags/%s/tasks", tagGID)
	var resp PagedResponse[Task]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// GetActiveTasksByTag fetches non-completed tasks with a specific tag.
// This method queries tasks by tag and filters out completed ones.
func (c *Client) GetActiveTasksByTag(ctx context.Context, tagGID string) ([]Task, error) {
	// Get tasks by tag with additional fields for filtering
	path := fmt.Sprintf("/tags/%s/tasks?opt_fields=gid,name,notes,completed,completed_at,tags,tags.name,created_at,modified_at,permalink_url", tagGID)
	var resp PagedResponse[Task]
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}

	// Filter out completed tasks
	var activeTasks []Task
	for _, task := range resp.Data {
		if !task.Completed {
			activeTasks = append(activeTasks, task)
		}
	}

	return activeTasks, nil
}

// Ping checks if the Asana API is accessible and the token is valid
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.GetWorkspace(ctx)
	return err
}
