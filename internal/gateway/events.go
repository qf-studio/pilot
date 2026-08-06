package gateway

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// executionEventResponse is one entry in an execution's stage timeline. Stage
// is served as an opaque string deliberately — the vocabulary is 31 values as
// of 2026-08-06 and growing (internal/memory/store.go), so gateway code must
// never enumerate or validate it, just pass it through.
type executionEventResponse struct {
	Stage      string    `json:"stage"`
	OccurredAt time.Time `json:"occurredAt"`
	Detail     string    `json:"detail"`
}

// taskEventsResponse wraps the task-scoped route's event array with the
// resolved execution's id/status, so a caller that only knows a task id
// learns which execution the events belong to (GH-4749 acceptance #2).
type taskEventsResponse struct {
	ExecutionID string                   `json:"executionId"`
	Status      string                   `json:"status"`
	Events      []executionEventResponse `json:"events"`
}

// handleExecutionEvents serves GET /api/v1/executions/{id}/events: the ordered
// (occurred_at ASC) stage timeline for a single execution (GH-4749 acceptance
// #1). detail is served verbatim — no redaction/scrubbing helper exists on
// this path today (searched internal/gateway, internal/dashboard,
// internal/memory); building one is a separate S4 leg per the task's scope
// fence, noted in the PR description.
func (s *Server) handleExecutionEvents(w http.ResponseWriter, r *http.Request) {
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
		// Same posture as handleApprovals: never leak a cross-project row
		// under a scoped dashboard, not even as an explicit lookup.
		http.Error(w, "unknown execution", http.StatusNotFound)
		return
	}

	events, err := store.ListExecutionEvents(id)
	if err != nil {
		http.Error(w, "failed to fetch events", http.StatusInternalServerError)
		return
	}

	writeJSON(w, toExecutionEventResponses(events))
}

// handleTaskEvents serves GET /api/v1/tasks/{taskId}/events?project=<path>:
// the event timeline for the NEWEST execution of (taskId, project), plus an
// envelope naming which execution it resolved to (GH-4749 acceptance #2).
// "Newest" is ListExecutionsForTask's own ordering (created_at DESC, rowid
// DESC), matching the pick-newest rule the pilot-console C8 join endpoint
// uses so the two ends agree.
//
// project scoping: when this server is scoped to a single project
// (SetDashboardProjectPath), that scope is authoritative and the query
// param is not honored, mirroring the sibling dashboard routes' behavior —
// a scoped deployment never serves another project's data regardless of what
// a caller asks for. The query param exists for daemon/aggregate deployments
// (dashboardProjectPath == ""), where there's no default project and the
// caller must say which one, per GH-4352's task_id-collision lesson.
func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
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
	exec := execs[0] // newest: ListExecutionsForTask orders created_at DESC, rowid DESC

	events, err := store.ListExecutionEvents(exec.ID)
	if err != nil {
		http.Error(w, "failed to fetch events", http.StatusInternalServerError)
		return
	}

	writeJSON(w, taskEventsResponse{
		ExecutionID: exec.ID,
		Status:      exec.Status,
		Events:      toExecutionEventResponses(events),
	})
}

// toExecutionEventResponses adapts store events to the wire DTO. Handles nil
// (unknown execution with zero rows) the same as an empty slice.
func toExecutionEventResponses(events []*memory.Event) []executionEventResponse {
	out := make([]executionEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, executionEventResponse{
			Stage:      string(e.Stage),
			OccurredAt: e.OccurredAt,
			Detail:     e.Detail,
		})
	}
	return out
}
