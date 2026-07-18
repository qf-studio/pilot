package github

import (
	"context"
	"fmt"
	"net/http"
)

// WorkflowJob represents a single GitHub Actions job within a workflow run,
// including its per-step status/conclusion breakdown.
//
// GH-4460: the studio-sdk GitHub client (used by autopilot's CIMonitor for
// the check-runs list and whole-job log fetch) does not expose the jobs API,
// so this in-tree client supplies it. For check runs created by GitHub
// Actions, the check-run ID and the job ID are the same numeric ID — see
// ListCheckRuns/GetJobLogs, which already rely on that equivalence.
type WorkflowJob struct {
	ID         int64     `json:"id"`
	RunID      int64     `json:"run_id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`               // queued, in_progress, completed
	Conclusion string    `json:"conclusion,omitempty"` // success, failure, cancelled, timed_out, ...
	HTMLURL    string    `json:"html_url,omitempty"`
	Steps      []JobStep `json:"steps"`
}

// JobStep is one step within a WorkflowJob.
type JobStep struct {
	Name        string `json:"name"`
	Status      string `json:"status"`               // queued, in_progress, completed
	Conclusion  string `json:"conclusion,omitempty"` // success, failure, cancelled, skipped, ...
	Number      int    `json:"number"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
}

// GetWorkflowJob fetches a single GitHub Actions job by ID, including its
// per-step breakdown.
// Uses GET /repos/{owner}/{repo}/actions/jobs/{job_id}.
// GH-4460: the step list is what lets a CI-failure excerpt point at the
// actual failing step instead of the whole job's log, which starts with
// runner-provisioning preamble that preflight rejected as "only runner setup
// information" (root cause of the GH-4415 continuation-issue bounce chain).
func (c *Client) GetWorkflowJob(ctx context.Context, owner, repo string, jobID int64) (*WorkflowJob, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/jobs/%d", owner, repo, jobID)
	var job WorkflowJob
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// CheckRunAnnotation is a single annotation attached to a check run, e.g. a
// compiler error or lint failure GitHub surfaces inline against a file/line.
// GH-4460: used as a fallback signal for check runs that are not backed by a
// GitHub Actions job (e.g. third-party Checks-API integrations), where
// GetWorkflowJob has nothing to resolve.
type CheckRunAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"` // notice, warning, failure
	Message         string `json:"message"`
	Title           string `json:"title,omitempty"`
}

// GetCheckRunAnnotations fetches annotations for a check run.
// Uses GET /repos/{owner}/{repo}/check-runs/{check_run_id}/annotations.
func (c *Client) GetCheckRunAnnotations(ctx context.Context, owner, repo string, checkRunID int64) ([]CheckRunAnnotation, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs/%d/annotations?per_page=50", owner, repo, checkRunID)
	var result []CheckRunAnnotation
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}
