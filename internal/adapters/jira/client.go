package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a Jira API client
type Client struct {
	baseURL    string
	username   string
	apiToken   string
	platform   string
	httpClient *http.Client
}

// NewClient creates a new Jira client
func NewClient(baseURL, username, apiToken, platform string) *Client {
	// Ensure baseURL doesn't have trailing slash
	baseURL = strings.TrimSuffix(baseURL, "/")

	return &Client{
		baseURL:  baseURL,
		username: username,
		apiToken: apiToken,
		platform: platform,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// apiPath returns the correct API path based on platform
func (c *Client) apiPath() string {
	if c.platform == PlatformCloud {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

// doRequest performs an HTTP request to the Jira API
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	url := c.baseURL + c.apiPath() + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set authentication header (Basic Auth with email:api_token for Cloud, or username:token for Server)
	auth := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.apiToken))
	req.Header.Set("Authorization", "Basic "+auth)
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
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
	}

	return nil
}

// GetIssue fetches an issue by key (e.g., "PROJ-42")
func (c *Client) GetIssue(ctx context.Context, issueKey string) (*Issue, error) {
	path := fmt.Sprintf("/issue/%s", issueKey)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// AddComment adds a comment to an issue
func (c *Client) AddComment(ctx context.Context, issueKey, body string) (*Comment, error) {
	path := fmt.Sprintf("/issue/%s/comment", issueKey)

	// Jira Cloud uses ADF (Atlassian Document Format), Server uses plain text
	var reqBody interface{}
	if c.platform == PlatformCloud {
		reqBody = map[string]interface{}{
			"body": map[string]interface{}{
				"type":    "doc",
				"version": 1,
				"content": []map[string]interface{}{
					{
						"type": "paragraph",
						"content": []map[string]interface{}{
							{
								"type": "text",
								"text": body,
							},
						},
					},
				},
			},
		}
	} else {
		reqBody = map[string]string{"body": body}
	}

	var comment Comment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// GetTransitions fetches available transitions for an issue
func (c *Client) GetTransitions(ctx context.Context, issueKey string) ([]Transition, error) {
	path := fmt.Sprintf("/issue/%s/transitions", issueKey)
	var resp TransitionsResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Transitions, nil
}

// TransitionIssue performs a workflow transition on an issue
func (c *Client) TransitionIssue(ctx context.Context, issueKey, transitionID string) error {
	path := fmt.Sprintf("/issue/%s/transitions", issueKey)
	reqBody := map[string]interface{}{
		"transition": map[string]string{
			"id": transitionID,
		},
	}
	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// TransitionIssueTo finds and performs a transition to the specified status
func (c *Client) TransitionIssueTo(ctx context.Context, issueKey, statusName string) error {
	transitions, err := c.GetTransitions(ctx, issueKey)
	if err != nil {
		return fmt.Errorf("failed to get transitions: %w", err)
	}

	for _, t := range transitions {
		if strings.EqualFold(t.To.Name, statusName) || strings.EqualFold(t.Name, statusName) {
			return c.TransitionIssue(ctx, issueKey, t.ID)
		}
	}

	return fmt.Errorf("no transition found to status: %s", statusName)
}

// AddRemoteLink adds a remote link to an issue (for PR linking)
func (c *Client) AddRemoteLink(ctx context.Context, issueKey string, link *RemoteLink) error {
	path := fmt.Sprintf("/issue/%s/remotelink", issueKey)
	return c.doRequest(ctx, http.MethodPost, path, link, nil)
}

// AddPRLink adds a GitHub PR link to an issue
func (c *Client) AddPRLink(ctx context.Context, issueKey, prURL, prTitle string) error {
	link := &RemoteLink{
		GlobalID: fmt.Sprintf("github-pr-%s", prURL),
		Object: RemoteLinkObject{
			URL:     prURL,
			Title:   prTitle,
			Summary: "Pull Request created by Pilot",
			Icon: &RemoteLinkIcon{
				URL16x16: "https://github.githubassets.com/favicon.ico",
				Title:    "GitHub",
			},
		},
	}
	return c.AddRemoteLink(ctx, issueKey, link)
}

// GetProject fetches project info
func (c *Client) GetProject(ctx context.Context, projectKey string) (*Project, error) {
	path := fmt.Sprintf("/project/%s", projectKey)
	var project Project
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// SearchResponse represents the response from the search API
type SearchResponse struct {
	Issues     []*Issue `json:"issues"`
	Total      int      `json:"total"`
	StartAt    int      `json:"startAt"`
	MaxResults int      `json:"maxResults"`
}

// SearchIssues searches for issues using JQL
func (c *Client) SearchIssues(ctx context.Context, jql string, maxResults int) ([]*Issue, error) {
	if maxResults <= 0 {
		maxResults = 50
	}

	path := fmt.Sprintf("/search?jql=%s&maxResults=%d", strings.ReplaceAll(jql, " ", "+"), maxResults)
	var resp SearchResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Issues, nil
}

// AddLabel adds a label to an issue
func (c *Client) AddLabel(ctx context.Context, issueKey, label string) error {
	path := fmt.Sprintf("/issue/%s", issueKey)
	reqBody := map[string]interface{}{
		"update": map[string]interface{}{
			"labels": []map[string]interface{}{
				{"add": label},
			},
		},
	}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// RemoveLabel removes a label from an issue
func (c *Client) RemoveLabel(ctx context.Context, issueKey, label string) error {
	path := fmt.Sprintf("/issue/%s", issueKey)
	reqBody := map[string]interface{}{
		"update": map[string]interface{}{
			"labels": []map[string]interface{}{
				{"remove": label},
			},
		},
	}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// HasLabel checks if an issue has a specific label (case-insensitive)
func (c *Client) HasLabel(issue *Issue, label string) bool {
	for _, l := range issue.Fields.Labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}
