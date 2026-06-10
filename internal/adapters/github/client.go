package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIURL     = "https://api.github.com"
	githubGraphQLURL = "https://api.github.com/graphql"
)

// RateLimitError is returned by doRequest when GitHub signals a rate limit via
// a 403 or 429 response. It carries the parsed Retry-After duration so the
// retry loop can honor it without regexing the error string.
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

// parseRetryAfterHeader reads Retry-After and X-RateLimit-Reset headers and
// returns the delay duration. Returns 0 when neither header is present.
func parseRetryAfterHeader(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(unix, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}

// GraphQLRequest is a GitHub GraphQL API request body.
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse is a GitHub GraphQL API response envelope.
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError is a single error from a GraphQL response.
type GraphQLError struct {
	Message string `json:"message"`
}

// Client is a GitHub API client
type Client struct {
	token      string
	httpClient *http.Client
	baseURL    string       // For testing - defaults to githubAPIURL
	retryOpts  RetryOptions // Retry config for doRequest; overridable in tests
}

// NewClient creates a new GitHub client
func NewClient(token string) *Client {
	return &Client{
		token:     token,
		baseURL:   githubAPIURL,
		retryOpts: DefaultRetryOptions(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new GitHub client with a custom base URL (for testing).
// Retry is disabled by default so unit tests fail fast; set client.retryOpts to enable.
func NewClientWithBaseURL(token, baseURL string) *Client {
	return &Client{
		token:     token,
		baseURL:   baseURL,
		retryOpts: RetryOptions{MaxRetries: 0},
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Issue represents a GitHub issue
type Issue struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"` // GraphQL global node ID
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	Labels      []Label   `json:"labels"`
	Assignee    *User     `json:"assignee"`
	Assignees   []User    `json:"assignees"`
	User        User      `json:"user"` // Issue author
	HTMLURL     string    `json:"html_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request,omitempty"` // Non-nil when item is a PR (GitHub Issues API returns both)
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

// doRequest performs an HTTP request to the GitHub API with automatic retry on
// transient errors (429, 5xx, network failures). The request body is buffered
// once before the retry loop so it can be replayed on each attempt.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	return WithRetryVoid(ctx, func() error {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if bodyBytes != nil {
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
			msg := string(respBody)
			if resp.StatusCode == http.StatusTooManyRequests {
				return &RateLimitError{
					StatusCode: http.StatusTooManyRequests,
					RetryAfter: parseRetryAfterHeader(resp.Header),
					Message:    msg,
				}
			}
			if resp.StatusCode == http.StatusForbidden {
				msgLower := strings.ToLower(msg)
				isRateLimit := resp.Header.Get("X-RateLimit-Remaining") == "0" ||
					strings.Contains(msgLower, "secondary rate limit") ||
					strings.Contains(msgLower, "rate limit exceeded")
				if isRateLimit {
					return &RateLimitError{
						StatusCode: http.StatusForbidden,
						RetryAfter: parseRetryAfterHeader(resp.Header),
						Message:    msg,
					}
				}
			}
			return fmt.Errorf("API error (status %d): %s", resp.StatusCode, msg)
		}

		if result != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}
		}

		return nil
	}, c.retryOpts)
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

// ListIssueComments returns all comments on an issue or PR.
func (c *Client) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]*Comment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	var comments []*Comment
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &comments); err != nil {
		return nil, err
	}
	return comments, nil
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
	// GitHub API is case-sensitive for label names in URL path, normalize to lowercase
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, number, strings.ToLower(label))
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

// RequestReviewers requests reviewers for a pull request.
// reviewers are individual GitHub usernames, teamReviewers are team slugs.
func (c *Client) RequestReviewers(ctx context.Context, owner, repo string, number int, reviewers, teamReviewers []string) error {
	if len(reviewers) == 0 && len(teamReviewers) == 0 {
		return nil
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", owner, repo, number)
	body := map[string][]string{}
	if len(reviewers) > 0 {
		body["reviewers"] = reviewers
	}
	if len(teamReviewers) > 0 {
		body["team_reviewers"] = teamReviewers
	}
	return c.doRequest(ctx, http.MethodPost, path, body, nil)
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
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	payload := map[string]string{"state": "closed"}
	return c.doRequest(ctx, http.MethodPatch, path, payload, nil)
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
// Note: Labels are filtered case-insensitively in Go code after fetching,
// because GitHub API label queries are case-sensitive.
func (c *Client) ListIssues(ctx context.Context, owner, repo string, opts *ListIssuesOptions) ([]*Issue, error) {
	const perPage = 100
	const maxPages = 50

	// Build query parameters (pagination is added per-page below).
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
	baseQuery := strings.Join(params, "&")

	// Paginate (GitHub defaults to 30/page, caps at 100). Without this the
	// candidate set was silently truncated to the first ~30 issues, breaking the
	// oldest-first dispatch contract on repos with more open issues. TASK-346 (C6).
	var issues []*Issue
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues?per_page=%d&page=%d", owner, repo, perPage, page)
		if baseQuery != "" {
			path += "&" + baseQuery
		}
		var batch []*Issue
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		issues = append(issues, batch...)
		if len(batch) < perPage {
			break
		}
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

// IssueInput is the input for creating a new issue
type IssueInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// CreateIssue creates a new issue in a repository
func (c *Client) CreateIssue(ctx context.Context, owner, repo string, input *IssueInput) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", owner, repo)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodPost, path, input, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
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

// ListPullRequests lists pull requests for a repository.
// state can be "open", "closed", or "all".
// Results are paginated at 100 per page (GitHub default is 30). A safety cap
// of 50 pages (5 000 PRs) prevents runaway loops on very large repos.
func (c *Client) ListPullRequests(ctx context.Context, owner, repo, state string) ([]*PullRequest, error) {
	const perPage = 100
	const maxPages = 50

	var all []*PullRequest
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&per_page=%d&page=%d", owner, repo, state, perPage, page)
		var batch []*PullRequest
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// CreateRelease creates a new release
func (c *Client) CreateRelease(ctx context.Context, owner, repo string, input *ReleaseInput) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases", owner, repo)
	var result Release
	if err := c.doRequest(ctx, http.MethodPost, path, input, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateGitTag creates a lightweight git tag via the GitHub API.
// This creates only the tag ref, not a GitHub Release — letting GoReleaser
// handle the full release creation with binary assets on tag push.
func (c *Client) CreateGitTag(ctx context.Context, owner, repo, tag, sha string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
	body := map[string]string{
		"ref": "refs/tags/" + tag,
		"sha": sha,
	}
	return c.doRequest(ctx, http.MethodPost, path, body, nil)
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

// GetReleaseByTag fetches a release by its tag name.
// Returns nil, nil if no release exists for the given tag (404).
func (c *Client) GetReleaseByTag(ctx context.Context, owner, repo, tag string) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/tags/%s", owner, repo, tag)
	var result Release
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// UpdateRelease updates an existing release (e.g. to enrich the body with a summary).
func (c *Client) UpdateRelease(ctx context.Context, owner, repo string, releaseID int64, input *ReleaseInput) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/%d", owner, repo, releaseID)
	var result Release
	if err := c.doRequest(ctx, http.MethodPatch, path, input, &result); err != nil {
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
// Paginates exhaustively (100 per page, up to 50 pages = 5 000 tags) so a SHA beyond the
// first 20 newest tags is still found. Used to detect if a commit has already been tagged.
func (c *Client) GetTagForSHA(ctx context.Context, owner, repo, sha string) (string, error) {
	const perPage = 100
	const maxPages = 50
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/tags?per_page=%d&page=%d", owner, repo, perPage, page)
		var batch []*Tag
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return "", err
		}
		for _, tag := range batch {
			if tag.Commit.SHA == sha {
				return tag.Name, nil
			}
		}
		if len(batch) < perPage {
			break
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

// CompareStatus returns GitHub's relationship of head to base for base...head:
// one of "ahead", "behind", "identical", or "diverged". It is the cheapest way
// to ask "is base an ancestor of head?" — base...head is "ahead" when head
// contains base plus more commits, and "identical" when they are the same
// commit. Used to detect a commit already covered by an existing release tag.
func (c *Client) CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, base, head)
	var result struct {
		Status string `json:"status"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", err
	}
	return result.Status, nil
}

// GetJobLogs fetches the logs for a GitHub Actions job (check run).
// Uses GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs which returns
// a 302 redirect to a log download URL. Returns the raw log text.
// GH-1567: Used to include CI error logs in autopilot fix issues.
func (c *Client) GetJobLogs(ctx context.Context, owner, repo string, jobID int64) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, jobID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("API error (status %d) fetching job logs", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read log response: %w", err)
	}

	return string(body), nil
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

// UpdateRef creates or updates a branch ref to point at the given SHA.
// Uses PATCH /repos/{owner}/{repo}/git/refs/heads/{branch} with force=true.
// If the ref does not exist, falls back to creating it via POST.
func (c *Client) UpdateRef(ctx context.Context, owner, repo, branch, sha string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, url.PathEscape(branch))
	body := map[string]interface{}{
		"sha":   sha,
		"force": true,
	}
	err := c.doRequest(ctx, http.MethodPatch, path, body, nil)
	if err == nil {
		return nil
	}
	// If the ref doesn't exist yet, create it
	if isUnprocessableError(err) || isNotFoundError(err) {
		createPath := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
		createBody := map[string]string{
			"ref": "refs/heads/" + branch,
			"sha": sha,
		}
		return c.doRequest(ctx, http.MethodPost, createPath, createBody, nil)
	}
	return err
}

// DeleteBranch deletes a branch from the repository.
// GitHub API: DELETE /repos/{owner}/{repo}/git/refs/heads/{branch}
// Returns nil on success, or if the branch was already deleted (404/422).
func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, url.PathEscape(branch))
	err := c.doRequest(ctx, http.MethodDelete, path, nil, nil)
	// 404 = branch doesn't exist, 422 = branch already deleted
	// Both are success cases for cleanup
	if isNotFoundError(err) || isUnprocessableError(err) {
		return nil
	}
	return err
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

// PRFile represents a file changed in a pull request.
type PRFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"` // "added", "removed", "modified", "renamed"
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// ListPullRequestFiles returns the list of files changed in a pull request.
func (c *Client) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]*PRFile, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files", owner, repo, number)
	var result []*PRFile
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

// GetPullRequestComments returns line-level review comments on a pull request.
// These are inline code annotations, distinct from top-level review bodies returned by ListPullRequestReviews.
// Uses: GET /repos/{owner}/{repo}/pulls/{number}/comments
func (c *Client) GetPullRequestComments(ctx context.Context, owner, repo string, number int) ([]*PRReviewComment, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, number)
	var result []*PRReviewComment
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ExecuteGraphQL executes a GitHub GraphQL query or mutation.
// Posts to baseURL+"/graphql" (testable via NewClientWithBaseURL).
// result is unmarshalled from response.data if non-nil.
// Transient transport errors (5xx, network) and GraphQL-level rate limits
// (HTTP 200 + RATE_LIMITED / "was submitted too quickly") are retried via
// c.retryOpts, matching the behaviour of doRequest.
func (c *Client) ExecuteGraphQL(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	reqBody := GraphQLRequest{Query: query, Variables: variables}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal graphql request: %w", err)
	}

	endpoint := c.baseURL + "/graphql"

	return WithRetryVoid(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("create graphql request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("graphql request failed: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read graphql response: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("graphql API error (status %d): %s", resp.StatusCode, string(respBody))
		}

		var gqlResp GraphQLResponse
		if err := json.Unmarshal(respBody, &gqlResp); err != nil {
			return fmt.Errorf("parse graphql response: %w", err)
		}

		if len(gqlResp.Errors) > 0 {
			return fmt.Errorf("graphql error: %s", gqlResp.Errors[0].Message)
		}

		if result != nil && len(gqlResp.Data) > 0 {
			if err := json.Unmarshal(gqlResp.Data, result); err != nil {
				return fmt.Errorf("unmarshal graphql data: %w", err)
			}
		}

		return nil
	}, c.retryOpts)
}

// SearchPRsForIssue returns all PRs that reference the given issue number using the
// GitHub Search API. It searches for PRs in the specified repo that mention #issueNumber.
func (c *Client) SearchPRsForIssue(ctx context.Context, owner, repo string, issueNumber int) ([]*PullRequest, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr #%d", owner, repo, issueNumber)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=100", url.QueryEscape(q))

	var result struct {
		Items []struct {
			ID          int64  `json:"id"`
			Number      int    `json:"number"`
			Title       string `json:"title"`
			State       string `json:"state"`
			HTMLURL     string `json:"html_url"`
			PullRequest *struct {
				MergedAt string `json:"merged_at"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("search PRs for issue #%d: %w", issueNumber, err)
	}

	prs := make([]*PullRequest, 0, len(result.Items))
	for _, item := range result.Items {
		pr := &PullRequest{
			ID:      item.ID,
			Number:  item.Number,
			Title:   item.Title,
			State:   item.State,
			HTMLURL: item.HTMLURL,
		}
		if item.PullRequest != nil && item.PullRequest.MergedAt != "" {
			pr.MergedAt = item.PullRequest.MergedAt
			pr.Merged = true
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// SearchOpenPRsForIssue returns open PRs that reference the given issue number.
// The returned PullRequest values include the User (author) field so callers can
// distinguish Pilot-bot PRs from human recovery PRs.
func (c *Client) SearchOpenPRsForIssue(ctx context.Context, owner, repo string, issueNumber int) ([]*PullRequest, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:open #%d", owner, repo, issueNumber)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=100", url.QueryEscape(q))

	var result struct {
		Items []struct {
			ID      int64  `json:"id"`
			Number  int    `json:"number"`
			Title   string `json:"title"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
			User    *User  `json:"user"`
		} `json:"items"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("search open PRs for issue #%d: %w", issueNumber, err)
	}

	prs := make([]*PullRequest, 0, len(result.Items))
	for _, item := range result.Items {
		prs = append(prs, &PullRequest{
			ID:      item.ID,
			Number:  item.Number,
			Title:   item.Title,
			State:   item.State,
			HTMLURL: item.HTMLURL,
			User:    item.User,
		})
	}
	return prs, nil
}

// GetAuthenticatedUser returns the GitHub user associated with the current token.
func (c *Client) GetAuthenticatedUser(ctx context.Context) (*User, error) {
	var user User
	if err := c.doRequest(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return nil, fmt.Errorf("get authenticated user: %w", err)
	}
	return &user, nil
}

// SearchMergedPRsForIssue checks if any merged PRs exist that reference the given
// issue number in their title (e.g. "GH-123" pattern). Uses the GitHub Search API.
// Returns true if at least one merged PR is found.
func (c *Client) SearchMergedPRsForIssue(ctx context.Context, owner, repo string, issueNumber int) (bool, error) {
	q := fmt.Sprintf("repo:%s/%s GH-%d in:title is:pr is:merged", owner, repo, issueNumber)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=1", url.QueryEscape(q))

	var result struct {
		TotalCount int `json:"total_count"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, fmt.Errorf("search merged PRs for issue %d: %w", issueNumber, err)
	}
	return result.TotalCount > 0, nil
}

// FindMergedPRByBranch looks up PRs by head branch via the strongly-consistent REST API
// (no Search API indexing lag). Returns true if any PR on that branch is merged.
// GH-2341: bypasses Search API lag that allowed duplicate dispatch of recently-merged issues.
func (c *Client) FindMergedPRByBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	head := fmt.Sprintf("%s:%s", owner, branch)
	path := fmt.Sprintf("/repos/%s/%s/pulls?head=%s&state=closed&per_page=10",
		owner, repo, url.QueryEscape(head))

	var prs []*PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &prs); err != nil {
		return false, fmt.Errorf("list PRs by branch %s: %w", branch, err)
	}
	for _, pr := range prs {
		if pr.MergedAt != "" || pr.Merged {
			return true, nil
		}
	}
	return false, nil
}

// FindOpenPRByBranch looks up OPEN PRs by head branch via the strongly-consistent
// REST API (no Search API indexing lag). Returns true if any open PR exists on
// that branch. TASK-341: counterpart to FindMergedPRByBranch for the
// PR-created-but-not-yet-merged window, so a re-dispatch no-op can be classified
// as awaiting-merge rather than pilot-blocked.
func (c *Client) FindOpenPRByBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	head := fmt.Sprintf("%s:%s", owner, branch)
	path := fmt.Sprintf("/repos/%s/%s/pulls?head=%s&state=open&per_page=10",
		owner, repo, url.QueryEscape(head))

	var prs []*PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &prs); err != nil {
		return false, fmt.Errorf("list open PRs by branch %s: %w", branch, err)
	}
	// The server filters by head=owner:branch&state=open, so a matching PR has
	// state=="open" and head.ref==branch. Re-check both rather than trusting the
	// array length, mirroring FindMergedPRByBranch's field inspection.
	for _, pr := range prs {
		if pr.State == "open" && pr.Head.Ref == branch {
			return true, nil
		}
	}
	return false, nil
}

// SearchOpenSubIssues counts open issues in a repo whose body contains "Parent: GH-{parentNum}".
// Uses the GitHub Search API to find sub-issues referencing the given parent.
func (c *Client) SearchOpenSubIssues(ctx context.Context, owner, repo string, parentNum int) (int, error) {
	q := fmt.Sprintf(`repo:%s/%s "Parent: GH-%d" is:issue is:open`, owner, repo, parentNum)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=1", url.QueryEscape(q))

	var result struct {
		TotalCount int `json:"total_count"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return 0, fmt.Errorf("search open sub-issues for parent %d: %w", parentNum, err)
	}
	return result.TotalCount, nil
}

// UpdatePullRequestBranch updates the PR branch with the latest base branch.
// Uses GitHub API: PUT /repos/{owner}/{repo}/pulls/{number}/update-branch
// Returns nil on success, error if the branch cannot be automatically updated (true conflict).
func (c *Client) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/update-branch", owner, repo, number)
	body := map[string]interface{}{}
	return c.doRequest(ctx, http.MethodPut, path, body, nil)
}

// GetIssueNodeID fetches the GraphQL node ID for a given issue number via the REST API.
func (c *Client) GetIssueNodeID(ctx context.Context, owner, repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var issue struct {
		NodeID string `json:"node_id"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return "", fmt.Errorf("get issue node ID for %s/%s#%d: %w", owner, repo, number, err)
	}
	if issue.NodeID == "" {
		return "", fmt.Errorf("issue %s/%s#%d returned empty node_id", owner, repo, number)
	}
	return issue.NodeID, nil
}

// LinkSubIssue links a child issue to a parent issue using the addSubIssue GraphQL mutation.
// Both issue numbers are resolved to node IDs first.
func (c *Client) LinkSubIssue(ctx context.Context, owner, repo string, parentNum, childNum int) error {
	parentID, err := c.GetIssueNodeID(ctx, owner, repo, parentNum)
	if err != nil {
		return fmt.Errorf("resolve parent node ID: %w", err)
	}
	childID, err := c.GetIssueNodeID(ctx, owner, repo, childNum)
	if err != nil {
		return fmt.Errorf("resolve child node ID: %w", err)
	}

	const mutation = `mutation($parentID: ID!, $childID: ID!) {
		addSubIssue(input: {issueId: $parentID, subIssueId: $childID}) {
			issue { id }
			subIssue { id }
		}
	}`

	variables := map[string]interface{}{
		"parentID": parentID,
		"childID":  childID,
	}
	return c.ExecuteGraphQL(ctx, mutation, variables, nil)
}

// GetOpenSubIssueCount queries native GitHub sub-issues for a parent issue and returns:
//   - count: number of sub-issues in OPEN state
//   - hasNativeLinks: true when the parent has at least one native sub-issue link (totalCount > 0)
//   - error: any API or parsing error
func (c *Client) GetOpenSubIssueCount(ctx context.Context, owner, repo string, parentNum int) (count int, hasNativeLinks bool, err error) {
	parentID, err := c.GetIssueNodeID(ctx, owner, repo, parentNum)
	if err != nil {
		return 0, false, fmt.Errorf("resolve parent node ID: %w", err)
	}

	const query = `query($issueID: ID!) {
		node(id: $issueID) {
			... on Issue {
				subIssues(first: 100) {
					totalCount
					nodes {
						state
					}
				}
			}
		}
	}`

	var result struct {
		Node struct {
			SubIssues struct {
				TotalCount int `json:"totalCount"`
				Nodes      []struct {
					State string `json:"state"`
				} `json:"nodes"`
			} `json:"subIssues"`
		} `json:"node"`
	}

	if err := c.ExecuteGraphQL(ctx, query, map[string]interface{}{"issueID": parentID}, &result); err != nil {
		return 0, false, fmt.Errorf("query sub-issues for %s/%s#%d: %w", owner, repo, parentNum, err)
	}

	if result.Node.SubIssues.TotalCount == 0 {
		return 0, false, nil
	}

	openCount := 0
	for _, n := range result.Node.SubIssues.Nodes {
		if n.State == "OPEN" {
			openCount++
		}
	}
	return openCount, true, nil
}

// SearchOpenPilotIssuesWithSubIssues returns issue numbers for open issues labeled "pilot"
// in the given repo that have at least one sub-issue (subIssuesSummary.total > 0).
// limit controls the maximum number of issues fetched from the API via the GraphQL first argument.
func (c *Client) SearchOpenPilotIssuesWithSubIssues(ctx context.Context, owner, repo string, limit int) ([]int, error) {
	const query = `query($owner: String!, $repo: String!, $first: Int!) {
		repository(owner: $owner, name: $repo) {
			issues(first: $first, states: [OPEN], labels: ["pilot"]) {
				nodes {
					number
					subIssuesSummary {
						total
						completed
					}
				}
			}
		}
	}`

	var result struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					Number           int `json:"number"`
					SubIssuesSummary struct {
						Total     int `json:"total"`
						Completed int `json:"completed"`
					} `json:"subIssuesSummary"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}

	variables := map[string]interface{}{
		"owner": owner,
		"repo":  repo,
		"first": limit,
	}

	if err := c.ExecuteGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("search pilot issues with sub-issues for %s/%s: %w", owner, repo, err)
	}

	var numbers []int
	for _, node := range result.Repository.Issues.Nodes {
		if node.SubIssuesSummary.Total > 0 {
			numbers = append(numbers, node.Number)
		}
	}
	return numbers, nil
}
