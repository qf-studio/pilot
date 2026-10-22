package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// Run-story outcome vocabulary (GH-5790).
const (
	runStoryOutcomeRunning      = "running"
	runStoryOutcomeReviewNeeded = "review_needed"
	runStoryOutcomeMerged       = "merged"
	runStoryOutcomeFailed       = "failed"

	runStoryFailureCI      = "ci"
	runStoryFailureUnknown = "unknown"
)

// executionResponse is the body of GET /api/v1/executions/{id} and
// GET /api/v1/tasks/{taskId} (newest execution of the task). RunStory is the
// per-ticket card the console renders (GH-5790).
type executionResponse struct {
	ExecutionID string   `json:"executionId"`
	TaskID      string   `json:"taskId"`
	Status      string   `json:"status"`
	RunStory    runStory `json:"run_story"`
}

// runStory answers "what was asked, what changed, what was verified, how long
// did it take, what happened" from data the daemon already stores. Every leaf
// is nullable: null means "no stored source recorded it for this run", never
// a guess. Cost and token fields are deliberately absent. Sources:
//
//	asked.title                    executions.task_title
//	asked.issue_url                executions.task_source_adapter ("github") +
//	                               task_source_issue_id, repo taken from
//	                               executions.pr_url; null without a PR URL
//	changed.pr_url                 executions.pr_url
//	changed.files_changed          executions.files_changed (null when all
//	changed.lines_added            three diff columns are 0)
//	changed.lines_removed          executions.lines_added / lines_removed
//	changed.summary                Not available yet (PR body is not stored)
//	verified.quality_gates         execution_events stage=quality_gate, the
//	                               latest per-gate row {gate, passed}
//	verified.acceptance_evidence   Not available yet (outcome is only written
//	                               into the PR body, no event or column)
//	verified.ci_status             autopilot active-PR state for pr_url;
//	                               null once autopilot drops the row
//	timings.queued_at              executions.created_at
//	timings.started_at             executions.started_at
//	timings.pr_opened_at           execution_events stage=pr_created (earliest)
//	timings.merged_at              execution_events stage=merged (earliest)
//	timings.duration_ms            executions.duration_ms (null when 0)
//	outcome                        executions.status + stage=merged event
//	failure_class                  outcome=failed only: "ci" when a
//	                               stage=ci_failed event exists, else
//	                               "unknown". "env" and "scope" are reserved:
//	                               no stored error classification feeds them
//	                               today
//	model_name                     executions.model_name
type runStory struct {
	Asked     runStoryAsked    `json:"asked"`
	Changed   runStoryChanged  `json:"changed"`
	Verified  runStoryVerified `json:"verified"`
	Timings   runStoryTimings  `json:"timings"`
	Outcome   *string          `json:"outcome"`
	FailClass *string          `json:"failure_class"`
	ModelName *string          `json:"model_name"`
}

type runStoryAsked struct {
	Title    *string `json:"title"`
	IssueURL *string `json:"issue_url"`
}

type runStoryChanged struct {
	PRURL        *string `json:"pr_url"`
	FilesChanged *int    `json:"files_changed"`
	LinesAdded   *int    `json:"lines_added"`
	LinesRemoved *int    `json:"lines_removed"`
	Summary      *string `json:"summary"`
}

type runStoryGate struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

type runStoryVerified struct {
	QualityGates       []runStoryGate `json:"quality_gates"`
	AcceptanceEvidence *string        `json:"acceptance_evidence"`
	CIStatus           *string        `json:"ci_status"`
}

type runStoryTimings struct {
	QueuedAt   *time.Time `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at"`
	PROpenedAt *time.Time `json:"pr_opened_at"`
	MergedAt   *time.Time `json:"merged_at"`
	DurationMS *int64     `json:"duration_ms"`
}

// handleExecution serves GET /api/v1/executions/{id}: the execution's identity
// plus its run story (GH-5790). Scoping matches handleExecutionEvents.
func (s *Server) handleExecution(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	projectPath := s.dashboardProjectPath
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing execution id", http.StatusBadRequest)
		return
	}

	exec, err := store.GetExecution(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "unknown execution", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to fetch execution", http.StatusInternalServerError)
		return
	}
	if projectPath != "" && exec.ProjectPath != projectPath {
		http.Error(w, "unknown execution", http.StatusNotFound)
		return
	}

	s.writeExecution(w, store, exec)
}

// handleTask serves GET /api/v1/tasks/{taskId}?project=<path>: the same body
// as handleExecution for the NEWEST execution of (taskId, project), with the
// project scoping rules of handleTaskEvents (GH-5790).
func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	store := s.dashboardStore
	serverScope := s.dashboardProjectPath
	s.mu.RUnlock()

	if store == nil {
		http.Error(w, "dashboard store not configured", http.StatusServiceUnavailable)
		return
	}

	taskID := r.PathValue("taskId")
	if taskID == "" {
		http.Error(w, "missing task id", http.StatusBadRequest)
		return
	}

	projectPath := serverScope
	if projectPath == "" {
		projectPath = r.URL.Query().Get("project")
	}

	execs, err := store.ListExecutionsForTask(taskID, projectPath)
	if err != nil {
		http.Error(w, "failed to fetch executions", http.StatusInternalServerError)
		return
	}
	if len(execs) == 0 {
		http.Error(w, "unknown task", http.StatusNotFound)
		return
	}

	s.writeExecution(w, store, execs[0]) // newest: created_at DESC, rowid DESC
}

func (s *Server) writeExecution(w http.ResponseWriter, store DashboardStore, exec *memory.Execution) {
	events, err := store.ListExecutionEvents(exec.ID)
	if err != nil {
		http.Error(w, "failed to fetch events", http.StatusInternalServerError)
		return
	}

	s.mu.RLock()
	provider := s.autopilotProvider
	s.mu.RUnlock()
	var prs []*AutopilotPRState
	if provider != nil {
		prs = provider.GetActivePRs()
	}

	writeJSON(w, executionResponse{
		ExecutionID: exec.ID,
		TaskID:      exec.TaskID,
		Status:      exec.Status,
		RunStory:    buildRunStory(exec, events, prs),
	})
}

// buildRunStory assembles the run story from the execution row, its stage
// events and the autopilot's active-PR snapshot. Pure: no I/O.
func buildRunStory(exec *memory.Execution, events []*memory.Event, prs []*AutopilotPRState) runStory {
	story := runStory{
		Asked: runStoryAsked{
			Title:    strPtr(exec.TaskTitle),
			IssueURL: strPtr(issueURLFromExecution(exec)),
		},
		Changed:   runStoryChanged{PRURL: strPtr(exec.PRUrl)},
		ModelName: strPtr(exec.ModelName),
	}
	if exec.FilesChanged != 0 || exec.LinesAdded != 0 || exec.LinesRemoved != 0 {
		story.Changed.FilesChanged = &exec.FilesChanged
		story.Changed.LinesAdded = &exec.LinesAdded
		story.Changed.LinesRemoved = &exec.LinesRemoved
	}

	story.Timings.StartedAt = exec.StartedAt
	if !exec.CreatedAt.IsZero() {
		createdAt := exec.CreatedAt
		story.Timings.QueuedAt = &createdAt
	}
	if exec.DurationMs > 0 {
		d := exec.DurationMs
		story.Timings.DurationMS = &d
	}

	var ciFailed bool
	gateIdx := map[string]int{}
	for _, e := range events {
		switch e.Stage {
		case memory.StagePRCreated:
			if story.Timings.PROpenedAt == nil {
				at := e.OccurredAt
				story.Timings.PROpenedAt = &at
			}
		case memory.StageMerged:
			if story.Timings.MergedAt == nil {
				at := e.OccurredAt
				story.Timings.MergedAt = &at
			}
		case memory.StageCIFailed:
			ciFailed = true
		case memory.StageQualityGate:
			var d struct {
				Gate   string `json:"gate"`
				Passed bool   `json:"passed"`
			}
			// The trailing summary row has no "gate" key; skip it and any
			// malformed detail rather than inventing a gate name.
			if json.Unmarshal([]byte(e.Detail), &d) != nil || d.Gate == "" {
				continue
			}
			// A retried quality loop re-records every gate: keep the latest
			// verdict per gate, in first-seen order.
			if i, ok := gateIdx[d.Gate]; ok {
				story.Verified.QualityGates[i].Passed = d.Passed
				continue
			}
			gateIdx[d.Gate] = len(story.Verified.QualityGates)
			story.Verified.QualityGates = append(story.Verified.QualityGates, runStoryGate{Name: d.Gate, Passed: d.Passed})
		}
	}

	if exec.PRUrl != "" {
		for _, pr := range prs {
			if pr != nil && pr.PRURL == exec.PRUrl {
				story.Verified.CIStatus = strPtr(pr.CIStatus)
				break
			}
		}
	}

	story.Outcome = strPtr(runStoryOutcome(exec.Status, story.Timings.MergedAt != nil))
	if story.Outcome != nil && *story.Outcome == runStoryOutcomeFailed {
		class := runStoryFailureUnknown
		if ciFailed {
			class = runStoryFailureCI
		}
		story.FailClass = &class
	}
	return story
}

// runStoryOutcome maps an executions.status (plus whether a merged event
// exists) onto the four-value card vocabulary. Statuses with no honest
// mapping (canceled, skipped, no_op, superseded, ...) return "" → null.
func runStoryOutcome(status string, merged bool) string {
	switch status {
	case "queued", "pending", "running":
		return runStoryOutcomeRunning
	case "failed":
		return runStoryOutcomeFailed
	}
	if merged {
		return runStoryOutcomeMerged
	}
	switch status {
	case "completed", "needs_human":
		return runStoryOutcomeReviewNeeded
	}
	return ""
}

// issueURLFromExecution rebuilds the GitHub issue URL from the stored issue
// number and the PR URL's host/owner/repo (a pilot PR lives in the same repo
// as its issue). Returns "" for non-GitHub sources, non-numeric ids, or rows
// without a parsable PR URL.
func issueURLFromExecution(exec *memory.Execution) string {
	if exec.TaskSourceAdapter != "github" || exec.PRUrl == "" {
		return ""
	}
	if n, err := strconv.Atoi(exec.TaskSourceIssueID); err != nil || n <= 0 {
		return ""
	}
	u, err := url.Parse(exec.PRUrl)
	if err != nil || u.Host == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return ""
	}
	return fmt.Sprintf("%s://%s/%s/%s/issues/%s", u.Scheme, u.Host, parts[0], parts[1], exec.TaskSourceIssueID)
}

// strPtr returns nil for "" so absent values serialize as JSON null.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
