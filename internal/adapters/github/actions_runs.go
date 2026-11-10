package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	sdkgithub "github.com/qf-studio/studio-sdk/sdk/integrations/github"
)

// workflowRunsPerPage and workflowRunsMaxPages bound ListWorkflowRuns: a
// commit rarely carries more than a handful of workflow runs, so the cap only
// guards against a server that ignores the page parameter.
const (
	workflowRunsPerPage  = 100
	workflowRunsMaxPages = 10
)

// WorkflowRun is one GitHub Actions workflow run, as listed by
// GET /repos/{owner}/{repo}/actions/runs. GH-6283: a fine-grained token cannot
// read check runs but can read these (permission Actions: Read), so the CI
// monitor derives its verdict from them when the check-runs call is refused.
type WorkflowRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	WorkflowID int64  `json:"workflow_id"`
	Status     string `json:"status"`               // queued, in_progress, waiting, requested, pending, completed
	Conclusion string `json:"conclusion,omitempty"` // success, failure, cancelled, skipped, ...
	HeadSHA    string `json:"head_sha"`
	RunAttempt int    `json:"run_attempt"`
	HTMLURL    string `json:"html_url,omitempty"`
}

type workflowRunsResponse struct {
	TotalCount   int           `json:"total_count"`
	WorkflowRuns []WorkflowRun `json:"workflow_runs"`
}

type workflowJobsResponse struct {
	TotalCount int           `json:"total_count"`
	Jobs       []WorkflowJob `json:"jobs"`
}

// ListWorkflowRuns lists the workflow runs of a commit SHA.
// Uses GET /repos/{owner}/{repo}/actions/runs?head_sha={sha}&per_page=100.
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo, sha string) ([]WorkflowRun, error) {
	var runs []WorkflowRun
	for page := 1; page <= workflowRunsMaxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=%d&page=%d",
			owner, repo, url.QueryEscape(sha), workflowRunsPerPage, page)
		var result workflowRunsResponse
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}
		runs = append(runs, result.WorkflowRuns...)
		if len(result.WorkflowRuns) < workflowRunsPerPage || len(runs) >= result.TotalCount {
			break
		}
	}
	return runs, nil
}

// ListWorkflowRunJobs lists the jobs of a workflow run (latest attempt).
// Uses GET /repos/{owner}/{repo}/actions/runs/{run_id}/jobs?per_page=100.
// A job's name is the check-run name Pilot's required-checks list refers to.
func (c *Client) ListWorkflowRunJobs(ctx context.Context, owner, repo string, runID int64) ([]WorkflowJob, error) {
	var jobs []WorkflowJob
	for page := 1; page <= workflowRunsMaxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?per_page=%d&page=%d",
			owner, repo, runID, workflowRunsPerPage, page)
		var result workflowJobsResponse
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, err
		}
		jobs = append(jobs, result.Jobs...)
		if len(result.Jobs) < workflowRunsPerPage || len(jobs) >= result.TotalCount {
			break
		}
	}
	return jobs, nil
}

// IsCheckRunsForbidden reports whether err, returned by a check-runs call, is
// GitHub refusing the token (403) rather than rate limiting it. A fine-grained
// personal access token always gets a 403 there: GitHub offers it no Checks
// permission (GH-6283). Both this package's and the studio-sdk client's
// errors are recognised — the SDK client, which the CI monitor uses for
// check-runs, returns a non-rate-limit 403 as a plain "API error (status 403)"
// error and a rate-limited one as its own RateLimitError.
func IsCheckRunsForbidden(err error) bool {
	if err == nil {
		return false
	}
	var rl *RateLimitError
	var sdkRL *sdkgithub.RateLimitError
	if errors.As(err, &rl) || errors.As(err, &sdkRL) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusForbidden
	}
	return strings.Contains(err.Error(), "API error (status 403)")
}

// IsUnreadable reports whether err says the token cannot read the resource: a
// 403 that is not a rate limit, or a 404 (GitHub answers 404 for a resource a
// token may not see).
func IsUnreadable(err error) bool {
	if err == nil {
		return false
	}
	if IsCheckRunsForbidden(err) {
		return true
	}
	var rl *RateLimitError
	var sdkRL *sdkgithub.RateLimitError
	if errors.As(err, &rl) || errors.As(err, &sdkRL) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return strings.Contains(err.Error(), "API error (status 404)")
}
