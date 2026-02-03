package azuredevops

import (
	"bytes"
	"context"
	"encoding/base64"
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
	defaultBaseURL    = "https://dev.azure.com"
	apiVersion        = "7.1"
	apiVersionPreview = "7.1-preview"
)

// Client is an Azure DevOps API client
type Client struct {
	pat          string
	organization string
	project      string
	repository   string
	httpClient   *http.Client
	baseURL      string
}

// NewClient creates a new Azure DevOps client
func NewClient(pat, organization, project string) *Client {
	return &Client{
		pat:          pat,
		organization: organization,
		project:      project,
		repository:   project, // Default repository name to project name
		baseURL:      defaultBaseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithConfig creates a new Azure DevOps client from config
func NewClientWithConfig(config *Config) *Client {
	c := NewClient(config.PAT, config.Organization, config.Project)
	if config.Repository != "" {
		c.repository = config.Repository
	}
	if config.BaseURL != "" {
		c.baseURL = config.BaseURL
	}
	return c
}

// NewClientWithBaseURL creates a new Azure DevOps client with a custom base URL (for testing)
func NewClientWithBaseURL(pat, organization, project, baseURL string) *Client {
	return &Client{
		pat:          pat,
		organization: organization,
		project:      project,
		repository:   project,
		baseURL:      baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetRepository sets the repository name (if different from project)
func (c *Client) SetRepository(repo string) {
	c.repository = repo
}

// doRequest performs an HTTP request to the Azure DevOps API
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	fullURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Azure DevOps uses Basic auth with empty username and PAT as password
	auth := base64.StdEncoding.EncodeToString([]byte(":" + c.pat))
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

// doRequestWithPatch performs a PATCH request with JSON Patch content type
func (c *Client) doRequestWithPatch(ctx context.Context, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	fullURL := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	auth := base64.StdEncoding.EncodeToString([]byte(":" + c.pat))
	req.Header.Set("Authorization", "Basic "+auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json-patch+json")

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

// Work Item API methods

// GetWorkItem fetches a work item by ID
func (c *Client) GetWorkItem(ctx context.Context, id int) (*WorkItem, error) {
	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		id,
		apiVersion,
	)
	var wi WorkItem
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &wi); err != nil {
		return nil, err
	}
	return &wi, nil
}

// ListWorkItemsByWIQL executes a WIQL query and returns work items
func (c *Client) ListWorkItemsByWIQL(ctx context.Context, wiql string) ([]*WorkItem, error) {
	path := fmt.Sprintf("/%s/%s/_apis/wit/wiql?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		apiVersion,
	)

	reqBody := map[string]string{"query": wiql}
	var queryResult WIQLQueryResult
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &queryResult); err != nil {
		return nil, fmt.Errorf("failed to execute WIQL query: %w", err)
	}

	if len(queryResult.WorkItems) == 0 {
		return []*WorkItem{}, nil
	}

	// Get work item IDs
	ids := make([]int, len(queryResult.WorkItems))
	for i, ref := range queryResult.WorkItems {
		ids[i] = ref.ID
	}

	// Fetch full work item details
	return c.GetWorkItems(ctx, ids)
}

// GetWorkItems fetches multiple work items by IDs
func (c *Client) GetWorkItems(ctx context.Context, ids []int) ([]*WorkItem, error) {
	if len(ids) == 0 {
		return []*WorkItem{}, nil
	}

	// Azure DevOps limits to 200 items per request
	const batchSize = 200
	var allWorkItems []*WorkItem

	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}

		batch := ids[i:end]
		idStrs := make([]string, len(batch))
		for j, id := range batch {
			idStrs[j] = strconv.Itoa(id)
		}

		path := fmt.Sprintf("/%s/%s/_apis/wit/workitems?ids=%s&api-version=%s",
			url.PathEscape(c.organization),
			url.PathEscape(c.project),
			strings.Join(idStrs, ","),
			apiVersion,
		)

		var result struct {
			Count int         `json:"count"`
			Value []*WorkItem `json:"value"`
		}
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}

		allWorkItems = append(allWorkItems, result.Value...)
	}

	return allWorkItems, nil
}

// ListWorkItems lists work items with the specified options
func (c *Client) ListWorkItems(ctx context.Context, opts *ListWorkItemsOptions) ([]*WorkItem, error) {
	// Build WIQL query
	wiql := "SELECT [System.Id] FROM WorkItems WHERE "
	conditions := []string{}

	// Filter by tags
	if opts != nil && len(opts.Tags) > 0 {
		for _, tag := range opts.Tags {
			conditions = append(conditions, fmt.Sprintf("[System.Tags] CONTAINS '%s'", tag))
		}
	}

	// Filter by states (exclude specified states)
	if opts != nil && len(opts.States) > 0 {
		stateConditions := []string{}
		for _, state := range opts.States {
			stateConditions = append(stateConditions, fmt.Sprintf("[System.State] = '%s'", state))
		}
		conditions = append(conditions, "("+strings.Join(stateConditions, " OR ")+")")
	}

	// Filter by work item types
	if opts != nil && len(opts.WorkItemTypes) > 0 {
		typeConditions := []string{}
		for _, wit := range opts.WorkItemTypes {
			typeConditions = append(typeConditions, fmt.Sprintf("[System.WorkItemType] = '%s'", wit))
		}
		conditions = append(conditions, "("+strings.Join(typeConditions, " OR ")+")")
	}

	// Filter by updated date
	if opts != nil && !opts.UpdatedAfter.IsZero() {
		conditions = append(conditions, fmt.Sprintf("[System.ChangedDate] >= '%s'", opts.UpdatedAfter.Format("2006-01-02T15:04:05Z")))
	}

	if len(conditions) == 0 {
		wiql += "1=1"
	} else {
		wiql += strings.Join(conditions, " AND ")
	}

	wiql += " ORDER BY [System.CreatedDate] ASC"

	return c.ListWorkItemsByWIQL(ctx, wiql)
}

// PatchOperation represents a JSON Patch operation
type PatchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
	From  string      `json:"from,omitempty"`
}

// UpdateWorkItem updates a work item with JSON Patch operations
func (c *Client) UpdateWorkItem(ctx context.Context, id int, operations []PatchOperation) (*WorkItem, error) {
	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		id,
		apiVersion,
	)

	var wi WorkItem
	if err := c.doRequestWithPatch(ctx, path, operations, &wi); err != nil {
		return nil, err
	}
	return &wi, nil
}

// AddWorkItemTag adds a tag to a work item
func (c *Client) AddWorkItemTag(ctx context.Context, id int, tag string) error {
	// First get current tags
	wi, err := c.GetWorkItem(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get work item: %w", err)
	}

	currentTags := ""
	if tags, ok := wi.Fields["System.Tags"].(string); ok {
		currentTags = tags
	}

	newTags := addTag(currentTags, tag)
	if newTags == currentTags {
		return nil // Tag already exists
	}

	ops := []PatchOperation{
		{
			Op:    "add",
			Path:  "/fields/System.Tags",
			Value: newTags,
		},
	}

	_, err = c.UpdateWorkItem(ctx, id, ops)
	return err
}

// RemoveWorkItemTag removes a tag from a work item
func (c *Client) RemoveWorkItemTag(ctx context.Context, id int, tag string) error {
	// First get current tags
	wi, err := c.GetWorkItem(ctx, id)
	if err != nil {
		// If work item doesn't exist, nothing to do
		return nil
	}

	currentTags := ""
	if tags, ok := wi.Fields["System.Tags"].(string); ok {
		currentTags = tags
	}

	newTags := removeTag(currentTags, tag)
	if newTags == currentTags {
		return nil // Tag wasn't there
	}

	ops := []PatchOperation{
		{
			Op:    "add",
			Path:  "/fields/System.Tags",
			Value: newTags,
		},
	}

	_, err = c.UpdateWorkItem(ctx, id, ops)
	return err
}

// AddWorkItemComment adds a comment to a work item
func (c *Client) AddWorkItemComment(ctx context.Context, id int, text string) (*Comment, error) {
	path := fmt.Sprintf("/%s/%s/_apis/wit/workitems/%d/comments?api-version=%s-preview",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		id,
		apiVersion,
	)

	reqBody := map[string]string{"text": text}
	var comment Comment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// UpdateWorkItemState updates a work item's state
func (c *Client) UpdateWorkItemState(ctx context.Context, id int, state string) error {
	ops := []PatchOperation{
		{
			Op:    "add",
			Path:  "/fields/System.State",
			Value: state,
		},
	}

	_, err := c.UpdateWorkItem(ctx, id, ops)
	return err
}

// Git/Repository API methods

// GetDefaultBranch returns the default branch of the repository
func (c *Client) GetDefaultBranch(ctx context.Context) (string, error) {
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		apiVersion,
	)

	var repo struct {
		DefaultBranch string `json:"defaultBranch"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &repo); err != nil {
		return "", err
	}

	// Default branch is returned as "refs/heads/main"
	branch := strings.TrimPrefix(repo.DefaultBranch, "refs/heads/")
	return branch, nil
}

// GetBranch gets a branch reference
func (c *Client) GetBranch(ctx context.Context, branchName string) (*GitRef, error) {
	refName := branchName
	if !strings.HasPrefix(refName, "refs/heads/") {
		refName = "refs/heads/" + branchName
	}

	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/refs?filter=%s&api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		url.QueryEscape(refName),
		apiVersion,
	)

	var result struct {
		Value []GitRef `json:"value"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}

	for _, ref := range result.Value {
		if ref.Name == refName {
			return &ref, nil
		}
	}

	return nil, fmt.Errorf("branch not found: %s", branchName)
}

// CreateBranch creates a new branch from a source ref
func (c *Client) CreateBranch(ctx context.Context, branchName, fromRef string) error {
	// Get the source ref to get the commit SHA
	sourceBranch, err := c.GetBranch(ctx, fromRef)
	if err != nil {
		return fmt.Errorf("failed to get source branch: %w", err)
	}

	newRefName := branchName
	if !strings.HasPrefix(newRefName, "refs/heads/") {
		newRefName = "refs/heads/" + branchName
	}

	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/refs?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		apiVersion,
	)

	refUpdates := []GitRefUpdate{
		{
			Name:        newRefName,
			OldObjectID: "0000000000000000000000000000000000000000", // 40 zeros for new branch
			NewObjectID: sourceBranch.ObjectID,
		},
	}

	var result struct {
		Value []GitRef `json:"value"`
	}
	if err := c.doRequest(ctx, http.MethodPost, path, refUpdates, &result); err != nil {
		return err
	}

	return nil
}

// Pull Request API methods

// CreatePullRequest creates a new pull request
func (c *Client) CreatePullRequest(ctx context.Context, input *PullRequestInput) (*PullRequest, error) {
	// Ensure refs are in full format
	sourceRef := input.SourceRefName
	if !strings.HasPrefix(sourceRef, "refs/heads/") {
		sourceRef = "refs/heads/" + sourceRef
	}
	targetRef := input.TargetRefName
	if !strings.HasPrefix(targetRef, "refs/heads/") {
		targetRef = "refs/heads/" + targetRef
	}

	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		apiVersion,
	)

	reqBody := map[string]interface{}{
		"title":         input.Title,
		"description":   input.Description,
		"sourceRefName": sourceRef,
		"targetRefName": targetRef,
	}
	if input.IsDraft {
		reqBody["isDraft"] = true
	}

	var pr PullRequest
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// GetPullRequest fetches a pull request by ID
func (c *Client) GetPullRequest(ctx context.Context, id int) (*PullRequest, error) {
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		id,
		apiVersion,
	)

	var pr PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// CompletePullRequest completes (merges) a pull request
func (c *Client) CompletePullRequest(ctx context.Context, id int, deleteSourceBranch bool) (*PullRequest, error) {
	// First get the PR to get the last merge source commit
	pr, err := c.GetPullRequest(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get pull request: %w", err)
	}

	if pr.LastMergeSourceCommit == nil {
		return nil, fmt.Errorf("pull request has no source commit")
	}

	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		id,
		apiVersion,
	)

	reqBody := map[string]interface{}{
		"status": PRStateCompleted,
		"lastMergeSourceCommit": map[string]string{
			"commitId": pr.LastMergeSourceCommit.CommitID,
		},
		"completionOptions": map[string]interface{}{
			"deleteSourceBranch": deleteSourceBranch,
			"mergeCommitMessage": pr.Title,
		},
	}

	var result PullRequest
	if err := c.doRequest(ctx, http.MethodPatch, path, reqBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AbandonPullRequest abandons (closes without merge) a pull request
func (c *Client) AbandonPullRequest(ctx context.Context, id int) (*PullRequest, error) {
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		id,
		apiVersion,
	)

	reqBody := map[string]interface{}{
		"status": PRStateAbandoned,
	}

	var pr PullRequest
	if err := c.doRequest(ctx, http.MethodPatch, path, reqBody, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// ListPullRequests lists pull requests with optional status filter
func (c *Client) ListPullRequests(ctx context.Context, status string) ([]*PullRequest, error) {
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		apiVersion,
	)

	if status != "" {
		path += "&searchCriteria.status=" + status
	}

	var result struct {
		Value []*PullRequest `json:"value"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result.Value, nil
}

// AddPRComment adds a comment to a pull request thread
func (c *Client) AddPRComment(ctx context.Context, prID int, comment string) error {
	path := fmt.Sprintf("/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads?api-version=%s",
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		prID,
		apiVersion,
	)

	reqBody := map[string]interface{}{
		"comments": []map[string]string{
			{"content": comment},
		},
	}

	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// GetWorkItemWebURL constructs the web URL for a work item
func (c *Client) GetWorkItemWebURL(id int) string {
	return fmt.Sprintf("%s/%s/%s/_workitems/edit/%d",
		c.baseURL,
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		id,
	)
}

// GetPullRequestWebURL constructs the web URL for a pull request
func (c *Client) GetPullRequestWebURL(id int) string {
	return fmt.Sprintf("%s/%s/%s/_git/%s/pullrequest/%d",
		c.baseURL,
		url.PathEscape(c.organization),
		url.PathEscape(c.project),
		url.PathEscape(c.repository),
		id,
	)
}

// HasTag checks if a work item has a specific tag
func HasTag(wi *WorkItem, tag string) bool {
	return wi.HasTag(tag)
}
