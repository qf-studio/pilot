package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	githubAPIURL = "https://api.github.com"
)

// Client is a GitHub API client
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string // For testing - defaults to githubAPIURL
}

// NewClient creates a new GitHub client
func NewClient(token string) *Client {
	return &Client{
		token:   token,
		baseURL: githubAPIURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new GitHub client with a custom base URL (for testing)
func NewClientWithBaseURL(token, baseURL string) *Client {
	return &Client{
		token:   token,
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Issue represents a GitHub issue
type Issue struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Labels    []Label   `json:"labels"`
	Assignee  *User     `json:"assignee"`
	Assignees []User    `json:"assignees"`
	User      User      `json:"user"` // Issue author
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Label represents a GitHub label
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

// User represents a GitHub user
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email,omitempty"`
}

// Repository represents a GitHub repository
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    User   `json:"owner"`
	HTMLURL  string `json:"html_url"`
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
}

// Comment represents a GitHub issue comment
type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// doRequest performs an HTTP request to the GitHub API
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

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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

// GetIssue fetches an issue by owner, repo, and number
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// AddComment adds a comment to an issue
func (c *Client) AddComment(ctx context.Context, owner, repo string, number int, body string) (*Comment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	reqBody := map[string]string{"body": body}
	var comment Comment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// AddLabels adds labels to an issue
func (c *Client) AddLabels(ctx context.Context, owner, repo string, number int, labels []string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, number)
	reqBody := map[string][]string{"labels": labels}
	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// RemoveLabel removes a label from an issue
func (c *Client) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, number, label)
	err := c.doRequest(ctx, http.MethodDelete, path, nil, nil)
	// 404 is OK - label might not exist
	if err != nil && err.Error() != "API error (status 404): " {
		return err
	}
	return nil
}

// UpdateIssueState updates an issue's state (open/closed)
func (c *Client) UpdateIssueState(ctx context.Context, owner, repo string, number int, state string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	reqBody := map[string]string{"state": state}
	return c.doRequest(ctx, http.MethodPatch, path, reqBody, nil)
}

// GetRepository fetches repository info
func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	var repository Repository
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &repository); err != nil {
		return nil, err
	}
	return &repository, nil
}

// CreateCommitStatus creates a status for a specific commit SHA
// The context parameter allows multiple statuses per commit (e.g., "ci/build", "pilot/execution")
func (c *Client) CreateCommitStatus(ctx context.Context, owner, repo, sha string, status *CommitStatus) (*CommitStatus, error) {
	path := fmt.Sprintf("/repos/%s/%s/statuses/%s", owner, repo, sha)
	var result CommitStatus
	if err := c.doRequest(ctx, http.MethodPost, path, status, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateCheckRun creates a check run for the GitHub Checks API
// Requires a GitHub App token with checks:write permission
func (c *Client) CreateCheckRun(ctx context.Context, owner, repo string, checkRun *CheckRun) (*CheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs", owner, repo)
	var result CheckRun
	if err := c.doRequest(ctx, http.MethodPost, path, checkRun, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdateCheckRun updates an existing check run
func (c *Client) UpdateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, checkRun *CheckRun) (*CheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, repo, checkRunID)
	var result CheckRun
	if err := c.doRequest(ctx, http.MethodPatch, path, checkRun, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreatePullRequest creates a new pull request
func (c *Client) CreatePullRequest(ctx context.Context, owner, repo string, input *PullRequestInput) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	var result PullRequest
	if err := c.doRequest(ctx, http.MethodPost, path, input, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetPullRequest fetches a pull request by number
func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	var result PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AddPRComment adds a comment to a pull request (issue comment API)
// For review comments on specific lines, use CreatePRReviewComment instead
func (c *Client) AddPRComment(ctx context.Context, owner, repo string, number int, body string) (*PRComment, error) {
	// PRs use the issues API for general comments
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	reqBody := map[string]string{"body": body}
	var result PRComment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListIssues lists issues for a repository with optional filters
func (c *Client) ListIssues(ctx context.Context, owner, repo string, opts *ListIssuesOptions) ([]*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues?", owner, repo)

	// Build query parameters
	params := []string{}
	if opts != nil {
		if len(opts.Labels) > 0 {
			for _, label := range opts.Labels {
				params = append(params, "labels="+label)
			}
		}
		if opts.State != "" {
			params = append(params, "state="+opts.State)
		}
		if opts.Sort != "" {
			params = append(params, "sort="+opts.Sort)
		}
		if !opts.Since.IsZero() {
			params = append(params, "since="+opts.Since.Format(time.RFC3339))
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

// HasLabel checks if an issue has a specific label
func HasLabel(issue *Issue, labelName string) bool {
	for _, label := range issue.Labels {
		if label.Name == labelName {
			return true
		}
	}
	return false
}

// MergePullRequest merges a pull request
// method can be "merge", "squash", or "rebase" (use MergeMethod* constants)
// commitTitle is optional - if empty, GitHub uses the default
func (c *Client) MergePullRequest(ctx context.Context, owner, repo string, number int, method, commitTitle string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number)

	body := map[string]string{
		"merge_method": method,
	}
	if commitTitle != "" {
		body["commit_title"] = commitTitle
	}

	return c.doRequest(ctx, http.MethodPut, path, body, nil)
}

// GetCombinedStatus gets combined status for a commit SHA
// Returns the combined state of all statuses for the commit
func (c *Client) GetCombinedStatus(ctx context.Context, owner, repo, sha string) (*CombinedStatus, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/status", owner, repo, sha)

	var status CombinedStatus
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// ListCheckRuns lists check runs for a commit SHA
// Returns check runs from GitHub Actions and other check suites
func (c *Client) ListCheckRuns(ctx context.Context, owner, repo, sha string) (*CheckRunsResponse, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, sha)

	var result CheckRunsResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ApprovePullRequest creates an approval review on a PR
// body is the optional review comment
func (c *Client) ApprovePullRequest(ctx context.Context, owner, repo string, number int, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)

	payload := map[string]string{
		"event": ReviewEventApprove,
	}
	if body != "" {
		payload["body"] = body
	}

	return c.doRequest(ctx, http.MethodPost, path, payload, nil)
}
