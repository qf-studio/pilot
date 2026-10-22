package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/briefs"
	"github.com/qf-studio/pilot/internal/memory"
)

// Run-story outcome values.
const (
	runStoryOutcomeRunning      = "running"
	runStoryOutcomeReviewNeeded = "review_needed"
	runStoryOutcomeMerged       = "merged"
	runStoryOutcomeFailed       = "failed"
)

// Run-story failure classes.
const (
	runStoryFailureCI      = "ci"
	runStoryFailureEnv     = "env"
	runStoryFailureScope   = "scope"
	runStoryFailureUnknown = "unknown"
)

// executionReadResponse is the body of GET /api/v1/executions/{id} and
// GET /api/v1/tasks/{taskId} (GH-5790): the execution's identity plus its
// run story. Cost and token fields are deliberately absent.
type executionReadResponse struct {
	ExecutionID string         `json:"executionId"`
	TaskID      string         `json:"taskId"`
	Status      string         `json:"status"`
	RunStory    runStoryObject `json:"run_story"`
}

// runStoryObject is the "Run Story" card payload. Every leaf is nullable:
// a nil pointer / slice serializes as JSON null, never as a made-up zero.
// Sources (table.column, all in the daemon's SQLite store unless noted):
//
//	asked.title             executions.task_title
//	asked.issue_url         executions.task_source_issue_id when it is already a URL; for
//	                        source_adapter=github with a numeric id, built from the repo
//	                        in executions.pr_url (/pull/N -> /issues/<id>); else null
//	changed.pr_url          executions.pr_url
//	changed.files_changed   executions.files_changed
//	changed.lines_added     executions.lines_added
//	changed.lines_removed   executions.lines_removed
//	changed.summary         Not available yet (the PR body is not stored by the daemon)
//	verified.quality_gates  execution_events stage=quality_gate, per-gate rows
//	                        (detail.gate, detail.passed); latest row per gate wins
//	verified.acceptance_evidence
//	                        Not available yet (the outcome is only rendered into the
//	                        PR body, never stored)
//	verified.ci_status      autopilot PR state ci_status (live /api/v1/autopilot rows)
//	                        matched on pr_url; once autopilot drops the row, the latest
//	                        execution_events stage ci_passed/ci_failed (success/failure)
//	timings.queued_at       executions.created_at
//	timings.started_at      executions.started_at
//	timings.pr_opened_at    autopilot PR state created_at while the row exists, else the
//	                        earliest execution_events stage=pr_created
//	timings.merged_at       earliest execution_events stage=merged (autopilot deletes the
//	                        PR state row on merge, so it carries no merge time)
//	timings.duration_ms     executions.duration_ms once the run finished
//	outcome                 executions.status + execution_events stage=merged
//	failure_class           executions.error classified by briefs.ClassifyFailure, plus
//	                        "scope" for scope-guard text; a ci_failed event with an
//	                        unclassifiable error maps to ci
//	model_name              executions.model_name
type runStoryObject struct {
	Asked    runStoryAsked    `json:"asked"`
	Changed  runStoryChanged  `json:"changed"`
	Verified runStoryVerified `json:"verified"`
	Timings  runStoryTimings  `json:"timings"`
	Outcome  *string          `json:"outcome"`
	// FailureClass is set only when Outcome is "failed".
	FailureClass *string `json:"failure_class"`
	ModelName    *string `json:"model_name"`
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
	DurationMs *int64     `json:"duration_ms"`
}

var githubPullURLRe = regexp.MustCompile(`^(https?://[^/]+/[^/]+/[^/]+)/pull/\d+`)

// buildRunStory assembles the run story for one execution from its row, its
// event timeline and (optionally) the live autopilot PR state.
func buildRunStory(exec *memory.Execution, events []*memory.Event, prs []*AutopilotPRState) runStoryObject {
	var rs runStoryObject

	rs.Asked.Title = strPtr(exec.TaskTitle)
	rs.Asked.IssueURL = strPtr(runStoryIssueURL(exec))

	rs.Changed.PRURL = strPtr(exec.PRUrl)
	if exec.PRUrl != "" {
		// Diff stats only mean something once a PR exists; before that the
		// row's zeros are "not measured", not "no change".
		rs.Changed.FilesChanged = &exec.FilesChanged
		rs.Changed.LinesAdded = &exec.LinesAdded
		rs.Changed.LinesRemoved = &exec.LinesRemoved
	}
	rs.ModelName = strPtr(exec.ModelName)

	var (
		prState           *AutopilotPRState
		prCreated, merged *time.Time
		eventCI           string
		gateOrder         []string
		gateResult        = map[string]bool{}
		sawCIFailedEvent  bool
	)
	if exec.PRUrl != "" {
		for _, p := range prs {
			if p != nil && p.PRURL == exec.PRUrl {
				prState = p
				break
			}
		}
	}
	for _, e := range events {
		at := e.OccurredAt
		switch e.Stage {
		case memory.StagePRCreated:
			if prCreated == nil {
				prCreated = &at
			}
		case memory.StageMerged:
			if merged == nil {
				merged = &at
			}
		case memory.StageCIPassed:
			eventCI, sawCIFailedEvent = "success", false
		case memory.StageCIFailed:
			eventCI, sawCIFailedEvent = "failure", true
		case memory.StageQualityGate:
			var d struct {
				Gate   string `json:"gate"`
				Passed bool   `json:"passed"`
			}
			// The trailing summary row has no "gate" key; skip it and any
			// row whose detail is not the JSON shape we recorded.
			if json.Unmarshal([]byte(e.Detail), &d) != nil || d.Gate == "" {
				continue
			}
			if _, seen := gateResult[d.Gate]; !seen {
				gateOrder = append(gateOrder, d.Gate)
			}
			gateResult[d.Gate] = d.Passed
		}
	}

	for _, name := range gateOrder {
		rs.Verified.QualityGates = append(rs.Verified.QualityGates, runStoryGate{Name: name, Passed: gateResult[name]})
	}
	switch {
	case prState != nil && prState.CIStatus != "":
		rs.Verified.CIStatus = strPtr(prState.CIStatus)
	case eventCI != "":
		rs.Verified.CIStatus = strPtr(eventCI)
	}

	if !exec.CreatedAt.IsZero() {
		t := exec.CreatedAt
		rs.Timings.QueuedAt = &t
	}
	rs.Timings.StartedAt = exec.StartedAt
	switch {
	case prState != nil && !prState.CreatedAt.IsZero():
		t := prState.CreatedAt
		rs.Timings.PROpenedAt = &t
	case prCreated != nil:
		rs.Timings.PROpenedAt = prCreated
	}
	rs.Timings.MergedAt = merged
	if exec.CompletedAt != nil || exec.DurationMs > 0 {
		d := exec.DurationMs
		rs.Timings.DurationMs = &d
	}

	outcome := runStoryOutcome(exec, merged != nil)
	rs.Outcome = strPtr(outcome)
	if outcome == runStoryOutcomeFailed {
		class := classifyRunStoryFailure(exec.Error)
		if class == runStoryFailureUnknown && sawCIFailedEvent {
			class = runStoryFailureCI
		}
		rs.FailureClass = &class
	}
	return rs
}

// runStoryOutcome maps the execution status (plus a recorded merge) to the
// card's four outcomes. Terminal non-failure statuses that never opened a PR
// (no_op, skipped, superseded, ...) have no card outcome, so nil.
func runStoryOutcome(exec *memory.Execution, merged bool) string {
	switch exec.Status {
	case "queued", "pending", "running":
		return runStoryOutcomeRunning
	case "failed":
		return runStoryOutcomeFailed
	case "completed":
		switch {
		case merged:
			return runStoryOutcomeMerged
		case exec.PRUrl != "":
			return runStoryOutcomeReviewNeeded
		}
	}
	if merged {
		return runStoryOutcomeMerged
	}
	return ""
}

// classifyRunStoryFailure folds the brief's failure vocabulary into the
// card's ci / env / scope / unknown classes.
func classifyRunStoryFailure(errText string) string {
	if strings.Contains(strings.ToLower(errText), "scope") {
		return runStoryFailureScope
	}
	class, _ := briefs.ClassifyFailure(errText)
	switch class {
	case briefs.FailureCI:
		return runStoryFailureCI
	case briefs.FailureTimeout, briefs.FailureRateLimit, briefs.FailureNetwork, briefs.FailureAuth:
		return runStoryFailureEnv
	}
	return runStoryFailureUnknown
}

func runStoryIssueURL(exec *memory.Execution) string {
	id := exec.TaskSourceIssueID
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		return id
	}
	if exec.TaskSourceAdapter != "github" || id == "" {
		return ""
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	m := githubPullURLRe.FindStringSubmatch(exec.PRUrl)
	if m == nil {
		return ""
	}
	return m[1] + "/issues/" + id
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// handleExecutionRead serves GET /api/v1/executions/{id}: the execution with
// its run story. Same project-scope posture as handleExecutionEvents.
func (s *Server) handleExecutionRead(w http.ResponseWriter, r *http.Request) {
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
	s.writeExecutionRead(w, store, exec)
}

// handleTaskRead serves GET /api/v1/tasks/{taskId}?project=<path>: the run
// story of the newest execution of (taskId, project), resolved exactly like
// handleTaskEvents.
func (s *Server) handleTaskRead(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	store := s.dashboardStore
	projectPath := s.dashboardProjectPath
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
	s.writeExecutionRead(w, store, execs[0])
}

func (s *Server) writeExecutionRead(w http.ResponseWriter, store DashboardStore, exec *memory.Execution) {
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

	writeJSON(w, executionReadResponse{
		ExecutionID: exec.ID,
		TaskID:      exec.TaskID,
		Status:      exec.Status,
		RunStory:    buildRunStory(exec, events, prs),
	})
}
