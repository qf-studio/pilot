package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	return WithRetry(ctx, func() (*Comment, error) {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
		reqBody := map[string]string{"body": body}
		var comment Comment
		if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &comment); err != nil {
			return nil, err
		}
		return &comment, nil
	}, DefaultRetryOptions())
}

// AddLabels adds labels to an issue
func (c *Client) AddLabels(ctx context.Context, owner, repo string, number int, labels []string) error {
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, number)
		reqBody := map[string][]string{"labels": labels}
		return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
	}, DefaultRetryOptions())
}

// RemoveLabel removes a label from an issue
func (c *Client) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error {
	return WithRetryVoid(ctx, func() error {
		// GitHub API is case-sensitive for label names in URL path, normalize to lowercase
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, number, strings.ToLower(label))
		err := c.doRequest(ctx, http.MethodDelete, path, nil, nil)
		// 404 is OK - label might not exist
		if err != nil && err.Error() != "API error (status 404): " {
			return err
		}
		return nil
	}, DefaultRetryOptions())
}

// UpdateIssueState updates an issue's state (open/closed)
func (c *Client) UpdateIssueState(ctx context.Context, owner, repo string, number int, state string) error {
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
		reqBody := map[string]string{"state": state}
		return c.doRequest(ctx, http.MethodPatch, path, reqBody, nil)
	}, DefaultRetryOptions())
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
	return WithRetry(ctx, func() (*CommitStatus, error) {
		path := fmt.Sprintf("/repos/%s/%s/statuses/%s", owner, repo, sha)
		var result CommitStatus
		if err := c.doRequest(ctx, http.MethodPost, path, status, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}, DefaultRetryOptions())
}

// CreateCheckRun creates a check run for the GitHub Checks API
// Requires a GitHub App token with checks:write permission
func (c *Client) CreateCheckRun(ctx context.Context, owner, repo string, checkRun *CheckRun) (*CheckRun, error) {
	return WithRetry(ctx, func() (*CheckRun, error) {
		path := fmt.Sprintf("/repos/%s/%s/check-runs", owner, repo)
		var result CheckRun
		if err := c.doRequest(ctx, http.MethodPost, path, checkRun, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}, DefaultRetryOptions())
}

// UpdateCheckRun updates an existing check run
func (c *Client) UpdateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, checkRun *CheckRun) (*CheckRun, error) {
	return WithRetry(ctx, func() (*CheckRun, error) {
		path := fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, repo, checkRunID)
		var result CheckRun
		if err := c.doRequest(ctx, http.MethodPatch, path, checkRun, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}, DefaultRetryOptions())
}

// CreatePullRequest creates a new pull request
func (c *Client) CreatePullRequest(ctx context.Context, owner, repo string, input *PullRequestInput) (*PullRequest, error) {
	return WithRetry(ctx, func() (*PullRequest, error) {
		path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
		var result PullRequest
		if err := c.doRequest(ctx, http.MethodPost, path, input, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}, DefaultRetryOptions())
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

// ClosePullRequest closes a pull request without merging.
// Used by autopilot to close failed PRs so the sequential poller can unblock.
func (c *Client) ClosePullRequest(ctx context.Context, owner, repo string, number int) error {
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
		payload := map[string]string{"state": "closed"}
		return c.doRequest(ctx, http.MethodPatch, path, payload, nil)
	}, DefaultRetryOptions())
}

// AddPRComment adds a comment to a pull request (issue comment API)
// For review comments on specific lines, use CreatePRReviewComment instead
func (c *Client) AddPRComment(ctx context.Context, owner, repo string, number int, body string) (*PRComment, error) {
	return WithRetry(ctx, func() (*PRComment, error) {
		// PRs use the issues API for general comments
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
		reqBody := map[string]string{"body": body}
		var result PRComment
		if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}, DefaultRetryOptions())
}

// ListIssues lists issues for a repository with optional filters
// Note: Labels are filtered case-insensitively in Go code after fetching,
// because GitHub API label queries are case-sensitive.
func (c *Client) ListIssues(ctx context.Context, owner, repo string, opts *ListIssuesOptions) ([]*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues?", owner, repo)

	// Build query parameters
	// Note: We intentionally skip passing labels to the API because GitHub's
	// label query is case-sensitive. Instead, we filter in code after fetching.
	params := []string{}
	var filterLabels []string
	if opts != nil {
		filterLabels = opts.Labels // Save for post-fetch filtering
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

	// Filter by labels case-insensitively
	if len(filterLabels) > 0 {
		var filtered []*Issue
		for _, issue := range issues {
			hasAllLabels := true
			for _, wantLabel := range filterLabels {
				if !HasLabel(issue, wantLabel) {
					hasAllLabels = false
					break
				}
			}
			if hasAllLabels {
				filtered = append(filtered, issue)
			}
		}
		return filtered, nil
	}

	return issues, nil
}

// HasLabel checks if an issue has a specific label (case-insensitive)
func HasLabel(issue *Issue, labelName string) bool {
	for _, label := range issue.Labels {
		if strings.EqualFold(label.Name, labelName) {
			return true
		}
	}
	return false
}

// MergePullRequest merges a pull request
// method can be "merge", "squash", or "rebase" (use MergeMethod* constants)
// commitTitle is optional - if empty, GitHub uses the default
func (c *Client) MergePullRequest(ctx context.Context, owner, repo string, number int, method, commitTitle string) error {
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number)

		body := map[string]string{
			"merge_method": method,
		}
		if commitTitle != "" {
			body["commit_title"] = commitTitle
		}

		return c.doRequest(ctx, http.MethodPut, path, body, nil)
	}, DefaultRetryOptions())
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
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)

		payload := map[string]string{
			"event": ReviewEventApprove,
		}
		if body != "" {
			payload["body"] = body
		}

		return c.doRequest(ctx, http.MethodPost, path, payload, nil)
	}, DefaultRetryOptions())
}

// IssueInput is the input for creating a new issue
type IssueInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// CreateIssue creates a new issue in a repository
func (c *Client) CreateIssue(ctx context.Context, owner, repo string, input *IssueInput) (*Issue, error) {
	return WithRetry(ctx, func() (*Issue, error) {
		path := fmt.Sprintf("/repos/%s/%s/issues", owner, repo)
		var issue Issue
		if err := c.doRequest(ctx, http.MethodPost, path, input, &issue); err != nil {
			return nil, err
		}
		return &issue, nil
	}, DefaultRetryOptions())
}

// GetBranch fetches information about a branch
func (c *Client) GetBranch(ctx context.Context, owner, repo, branch string) (*Branch, error) {
	path := fmt.Sprintf("/repos/%s/%s/branches/%s", owner, repo, url.PathEscape(branch))
	var result Branch
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListPullRequests lists pull requests for a repository
// state can be "open", "closed", or "all"
func (c *Client) ListPullRequests(ctx context.Context, owner, repo, state string) ([]*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s", owner, repo, state)
	var result []*PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CreateRelease creates a new release
func (c *Client) CreateRelease(ctx context.Context, owner, repo string, input *ReleaseInput) (*Release, error) {
	return WithRetry(ctx, func() (*Release, error) {
		path := fmt.Sprintf("/repos/%s/%s/releases", owner, repo)
		var result Release
		if err := c.doRequest(ctx, http.MethodPost, path, input, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}, DefaultRetryOptions())
}

// CreateGitTag creates a lightweight git tag via the GitHub API.
// This creates only the tag ref, not a GitHub Release — letting GoReleaser
// handle the full release creation with binary assets on tag push.
func (c *Client) CreateGitTag(ctx context.Context, owner, repo, tag, sha string) error {
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
		body := map[string]string{
			"ref": "refs/tags/" + tag,
			"sha": sha,
		}
		return c.doRequest(ctx, http.MethodPost, path, body, nil)
	}, DefaultRetryOptions())
}

// GetLatestRelease gets the latest published release
// Returns nil, nil if no releases exist
func (c *Client) GetLatestRelease(ctx context.Context, owner, repo string) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/latest", owner, repo)
	var result Release
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		// 404 means no releases exist - return nil, nil
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// ListReleases lists releases for a repository (newest first)
func (c *Client) ListReleases(ctx context.Context, owner, repo string, perPage int) ([]*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases?per_page=%d", owner, repo, perPage)
	var result []*Release
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ListTags lists repository tags (newest first)
func (c *Client) ListTags(ctx context.Context, owner, repo string, perPage int) ([]*Tag, error) {
	path := fmt.Sprintf("/repos/%s/%s/tags?per_page=%d", owner, repo, perPage)
	var result []*Tag
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetTagForSHA returns the tag name if a tag exists at the given SHA, or empty string if none.
// Used to detect if a commit has already been tagged (race condition prevention).
func (c *Client) GetTagForSHA(ctx context.Context, owner, repo, sha string) (string, error) {
	tags, err := c.ListTags(ctx, owner, repo, 20)
	if err != nil {
		return "", err
	}
	for _, tag := range tags {
		if tag.Commit.SHA == sha {
			return tag.Name, nil
		}
	}
	return "", nil
}

// GetPRCommits returns all commits in a pull request
func (c *Client) GetPRCommits(ctx context.Context, owner, repo string, prNumber int) ([]*Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits?per_page=100", owner, repo, prNumber)
	var result []*Commit
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CompareCommits compares two commits and returns commits between them
func (c *Client) CompareCommits(ctx context.Context, owner, repo, base, head string) ([]*Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, base, head)
	var result struct {
		Commits []*Commit `json:"commits"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result.Commits, nil
}

// isNotFoundError checks if error is a 404 not found error
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return len(errStr) >= 21 && errStr[:21] == "API error (status 404"
}

// isUnprocessableError checks if error is a 422 unprocessable entity error
func isUnprocessableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return len(errStr) >= 21 && errStr[:21] == "API error (status 422"
}

// DeleteBranch deletes a branch from the repository.
// GitHub API: DELETE /repos/{owner}/{repo}/git/refs/heads/{branch}
// Returns nil on success, or if the branch was already deleted (404/422).
func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	return WithRetryVoid(ctx, func() error {
		path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, url.PathEscape(branch))
		err := c.doRequest(ctx, http.MethodDelete, path, nil, nil)
		// 404 = branch doesn't exist, 422 = branch already deleted
		// Both are success cases for cleanup
		if isNotFoundError(err) || isUnprocessableError(err) {
			return nil
		}
		return err
	}, DefaultRetryOptions())
}

// ListPullRequestReviews lists all reviews for a pull request
func (c *Client) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]*PullRequestReview, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	var result []*PullRequestReview
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// HasApprovalReview checks if a PR has at least one approval review.
// Returns (hasApproval, approverLogin, error).
// Only considers the latest review from each user.
func (c *Client) HasApprovalReview(ctx context.Context, owner, repo string, number int) (bool, string, error) {
	reviews, err := c.ListPullRequestReviews(ctx, owner, repo, number)
	if err != nil {
		return false, "", err
	}

	// Track latest review state per user
	latestReviews := make(map[string]string) // user login -> state
	for _, review := range reviews {
		latestReviews[review.User.Login] = review.State
	}

	// Check if any user's latest review is APPROVED
	for login, state := range latestReviews {
		if state == ReviewStateApproved {
			return true, login, nil
		}
	}

	return false, "", nil
}
