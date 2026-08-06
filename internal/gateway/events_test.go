package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

// --- handleExecutionEvents unit tests --------------------------------------

func TestHandleExecutionEvents_Success_Ordered(t *testing.T) {
	t0 := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	store := &mockDashboardStore{
		execByID: map[string]*memory.Execution{
			"exec-1": {ID: "exec-1", TaskID: "GH-1", ProjectPath: "/tmp/proj-a", Status: "completed"},
		},
		eventsByExecutionID: map[string][]*memory.Event{
			"exec-1": {
				{ExecutionID: "exec-1", Stage: memory.StageQueued, OccurredAt: t0, Detail: "queued"},
				{ExecutionID: "exec-1", Stage: memory.StageRunning, OccurredAt: t0.Add(time.Minute), Detail: "started"},
				{ExecutionID: "exec-1", Stage: memory.StageMerged, OccurredAt: t0.Add(2 * time.Minute), Detail: "merged"},
			},
		},
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1/events", nil, "id", "exec-1")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var got []executionEventResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.body.String())
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	wantStages := []string{string(memory.StageQueued), string(memory.StageRunning), string(memory.StageMerged)}
	for i, w := range wantStages {
		if got[i].Stage != w {
			t.Errorf("event[%d].Stage = %q, want %q", i, got[i].Stage, w)
		}
	}
	if !got[0].OccurredAt.Equal(t0) {
		t.Errorf("event[0].OccurredAt = %v, want %v", got[0].OccurredAt, t0)
	}
	if got[2].Detail != "merged" {
		t.Errorf("event[2].Detail = %q, want merged", got[2].Detail)
	}
}

func TestHandleExecutionEvents_UnknownExecution_404(t *testing.T) {
	store := &mockDashboardStore{}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-ghost/events", nil, "id", "exec-ghost")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.status)
	}
}

func TestHandleExecutionEvents_ProjectScoped_ExcludesMismatch(t *testing.T) {
	store := &mockDashboardStore{
		execByID: map[string]*memory.Execution{
			"exec-b": {ID: "exec-b", TaskID: "GH-2", ProjectPath: "/tmp/proj-b", Status: "completed"},
		},
	}
	s := newTestServerWithDashboard(store)
	s.dashboardProjectPath = "/tmp/proj-a"

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-b/events", nil, "id", "exec-b")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (cross-project execution must not be visible under a scoped dashboard)", w.status)
	}
}

func TestHandleExecutionEvents_StoreNil_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1/events", nil, "id", "exec-1")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleExecutionEvents_GetExecutionError_500(t *testing.T) {
	store := &mockDashboardStore{getExecutionErr: errors.New("boom")}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1/events", nil, "id", "exec-1")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
}

func TestHandleExecutionEvents_ListEventsError_500(t *testing.T) {
	store := &mockDashboardStore{
		execByID: map[string]*memory.Execution{
			"exec-1": {ID: "exec-1", TaskID: "GH-1", ProjectPath: "/tmp/proj-a", Status: "completed"},
		},
		listExecutionEventsErr: errors.New("boom"),
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1/events", nil, "id", "exec-1")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
}

func TestHandleExecutionEvents_MethodNotAllowed(t *testing.T) {
	store := &mockDashboardStore{}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/executions/exec-1/events", nil, "id", "exec-1")
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.status)
	}
}

func TestHandleExecutionEvents_MissingID_400(t *testing.T) {
	store := &mockDashboardStore{}
	s := newTestServerWithDashboard(store)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/executions//events", nil)
	w := newTestResponseRecorder()
	s.handleExecutionEvents(w, req)

	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

// --- handleTaskEvents unit tests --------------------------------------------

func TestHandleTaskEvents_Success_NewestExecution_Envelope(t *testing.T) {
	t0 := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	store := &mockDashboardStore{
		// ListExecutionsForTask is expected to already return newest-first
		// (that's the store's own contract) -- the handler just takes [0].
		execsForTask: []*memory.Execution{
			{ID: "exec-newest", TaskID: "GH-1", ProjectPath: "/tmp/proj-a", Status: "running"},
			{ID: "exec-older", TaskID: "GH-1", ProjectPath: "/tmp/proj-a", Status: "completed"},
		},
		eventsByExecutionID: map[string][]*memory.Event{
			"exec-newest": {
				{ExecutionID: "exec-newest", Stage: memory.StageQueued, OccurredAt: t0, Detail: "queued"},
			},
		},
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-1/events?project=/tmp/proj-a", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	var got taskEventsResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.body.String())
	}
	if got.ExecutionID != "exec-newest" {
		t.Errorf("ExecutionID = %q, want exec-newest", got.ExecutionID)
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want running", got.Status)
	}
	if len(got.Events) != 1 || got.Events[0].Detail != "queued" {
		t.Errorf("Events = %+v, want single queued event", got.Events)
	}
	if len(store.gotListExecsForTaskArgs) != 1 || store.gotListExecsForTaskArgs[0] != "GH-1|/tmp/proj-a" {
		t.Errorf("ListExecutionsForTask args = %v, want [GH-1|/tmp/proj-a]", store.gotListExecsForTaskArgs)
	}
}

func TestHandleTaskEvents_UnknownTask_404(t *testing.T) {
	store := &mockDashboardStore{}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-999/events", nil, "taskId", "GH-999")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.status)
	}
}

func TestHandleTaskEvents_QueryProjectUsedWhenServerUnscoped(t *testing.T) {
	store := &mockDashboardStore{execsForTask: []*memory.Execution{{ID: "e1", Status: "queued"}}}
	s := newTestServerWithDashboard(store) // no SetDashboardProjectPath -> aggregate/daemon mode

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-1/events?project=/tmp/proj-x", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if len(store.gotListExecsForTaskArgs) != 1 || store.gotListExecsForTaskArgs[0] != "GH-1|/tmp/proj-x" {
		t.Errorf("ListExecutionsForTask args = %v, want [GH-1|/tmp/proj-x]", store.gotListExecsForTaskArgs)
	}
}

func TestHandleTaskEvents_ServerScopeAuthoritative_IgnoresQueryProject(t *testing.T) {
	store := &mockDashboardStore{execsForTask: []*memory.Execution{{ID: "e1", Status: "queued"}}}
	s := newTestServerWithDashboard(store)
	s.dashboardProjectPath = "/tmp/proj-a" // single-project deployment

	// Caller asks for a different project via the query param -- must not
	// escape the server's own scope.
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-1/events?project=/tmp/proj-evil", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if len(store.gotListExecsForTaskArgs) != 1 || store.gotListExecsForTaskArgs[0] != "GH-1|/tmp/proj-a" {
		t.Errorf("ListExecutionsForTask args = %v, want [GH-1|/tmp/proj-a] (server scope must win)", store.gotListExecsForTaskArgs)
	}
}

func TestHandleTaskEvents_StoreNil_503(t *testing.T) {
	s := NewServer(&Config{Host: "127.0.0.1", Port: 0})

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-1/events", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.status)
	}
}

func TestHandleTaskEvents_ListExecutionsError_500(t *testing.T) {
	store := &mockDashboardStore{listExecsForTaskErr: errors.New("boom")}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-1/events", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
}

func TestHandleTaskEvents_ListEventsError_500(t *testing.T) {
	store := &mockDashboardStore{
		execsForTask:           []*memory.Execution{{ID: "e1", Status: "queued"}},
		listExecutionEventsErr: errors.New("boom"),
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-1/events", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.status)
	}
}

func TestHandleTaskEvents_MethodNotAllowed(t *testing.T) {
	store := &mockDashboardStore{}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodPost, "/api/v1/tasks/GH-1/events", nil, "taskId", "GH-1")
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.status)
	}
}

func TestHandleTaskEvents_MissingTaskID_400(t *testing.T) {
	store := &mockDashboardStore{}
	s := newTestServerWithDashboard(store)

	req := httpTestRequest(t, http.MethodGet, "/api/v1/tasks//events", nil)
	w := newTestResponseRecorder()
	s.handleTaskEvents(w, req)

	if w.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.status)
	}
}

// --- composed / integration test: real store + real mux + real routing ----

// TestExecutionEventsAPI_Composed seeds a real *memory.Store through the
// full HTTP mux (auth middleware, Go 1.22 {id}/{taskId} path patterns) and
// exercises both routes end to end, per the task's explicit "seeded store"
// test requirement (store_test.go's CreatedAt/occurred_at seeding pattern).
func TestExecutionEventsAPI_Composed(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	const projectPath = "/tmp/composed-events-proj"
	const taskID = "GH-4749"

	// Older execution for the same task -- must NOT be the one the
	// task-scoped route resolves to.
	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-older", TaskID: taskID, ProjectPath: projectPath, Status: "completed",
	}); err != nil {
		t.Fatalf("SaveExecution(exec-older): %v", err)
	}
	time.Sleep(2 * time.Millisecond) // created_at resolution guard (see store_test.go convention)

	if err := store.SaveExecution(&memory.Execution{
		ID: "exec-newest", TaskID: taskID, ProjectPath: projectPath, Status: "running",
	}); err != nil {
		t.Fatalf("SaveExecution(exec-newest): %v", err)
	}

	for _, seed := range []struct {
		stage  memory.Stage
		detail string
	}{
		{memory.StageQueued, "queued"},
		{memory.StageRunning, "started"},
		{memory.StagePRCreated, "opened PR #1"},
	} {
		if err := store.InsertExecutionEvent("exec-newest", seed.stage, seed.detail); err != nil {
			t.Fatalf("InsertExecutionEvent(%s): %v", seed.stage, err)
		}
		time.Sleep(time.Millisecond) // distinct occurred_at values, same convention as execution_events_test.go
	}

	config := &Config{Host: "127.0.0.1", Port: 19095}
	authConfig := &AuthConfig{Type: AuthTypeAPIToken, Token: "composed-events-secret"}
	server := NewServerWithAuth(config, authConfig)
	server.SetDashboardStore(store)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = server.Start(ctx) }()
	time.Sleep(100 * time.Millisecond)

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://127.0.0.1:19095"

	// 1. Auth rejected without a bearer token, on both endpoints.
	getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/executions/exec-newest/events", nil)
	getResp, err := client.Do(getReq)
	if err != nil {
		t.Fatalf("GET executions (no auth): %v", err)
	}
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET executions no-auth status = %d, want 401", getResp.StatusCode)
	}

	getReq2, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/tasks/"+taskID+"/events", nil)
	getResp2, err := client.Do(getReq2)
	if err != nil {
		t.Fatalf("GET tasks (no auth): %v", err)
	}
	_ = getResp2.Body.Close()
	if getResp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET tasks no-auth status = %d, want 401", getResp2.StatusCode)
	}

	// 2. GET /executions/{id}/events with a valid token returns the ordered timeline.
	execReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/executions/exec-newest/events", nil)
	execReq.Header.Set("Authorization", "Bearer composed-events-secret")
	execResp, err := client.Do(execReq)
	if err != nil {
		t.Fatalf("GET executions (auth): %v", err)
	}
	defer func() { _ = execResp.Body.Close() }()
	if execResp.StatusCode != http.StatusOK {
		t.Fatalf("GET executions auth status = %d, want 200", execResp.StatusCode)
	}
	var events []executionEventResponse
	if err := json.NewDecoder(execResp.Body).Decode(&events); err != nil {
		t.Fatalf("decode executions body: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}
	wantOrder := []string{"queued", "started", "opened PR #1"}
	for i, want := range wantOrder {
		if events[i].Detail != want {
			t.Errorf("events[%d].Detail = %q, want %q", i, events[i].Detail, want)
		}
	}
	for i := 1; i < len(events); i++ {
		if events[i].OccurredAt.Before(events[i-1].OccurredAt) {
			t.Errorf("events not in ascending occurred_at order: events[%d]=%v before events[%d]=%v",
				i, events[i].OccurredAt, i-1, events[i-1].OccurredAt)
		}
	}

	// 3. 404 for an unknown execution id.
	unknownReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/executions/exec-does-not-exist/events", nil)
	unknownReq.Header.Set("Authorization", "Bearer composed-events-secret")
	unknownResp, err := client.Do(unknownReq)
	if err != nil {
		t.Fatalf("GET unknown execution: %v", err)
	}
	_ = unknownResp.Body.Close()
	if unknownResp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown execution status = %d, want 404", unknownResp.StatusCode)
	}

	// 4. GET /tasks/{taskId}/events?project=<path> resolves to the NEWEST
	// execution (exec-newest, not exec-older) and returns the envelope.
	taskReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/tasks/"+taskID+"/events?project="+projectPath, nil)
	taskReq.Header.Set("Authorization", "Bearer composed-events-secret")
	taskResp, err := client.Do(taskReq)
	if err != nil {
		t.Fatalf("GET tasks (auth): %v", err)
	}
	defer func() { _ = taskResp.Body.Close() }()
	if taskResp.StatusCode != http.StatusOK {
		t.Fatalf("GET tasks auth status = %d, want 200", taskResp.StatusCode)
	}
	var taskEvents taskEventsResponse
	if err := json.NewDecoder(taskResp.Body).Decode(&taskEvents); err != nil {
		t.Fatalf("decode tasks body: %v", err)
	}
	if taskEvents.ExecutionID != "exec-newest" {
		t.Errorf("ExecutionID = %q, want exec-newest", taskEvents.ExecutionID)
	}
	if taskEvents.Status != "running" {
		t.Errorf("Status = %q, want running", taskEvents.Status)
	}
	if len(taskEvents.Events) != 3 {
		t.Errorf("len(Events) = %d, want 3", len(taskEvents.Events))
	}

	// 5. 404 for an unknown task id.
	unknownTaskReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/tasks/GH-does-not-exist/events", nil)
	unknownTaskReq.Header.Set("Authorization", "Bearer composed-events-secret")
	unknownTaskResp, err := client.Do(unknownTaskReq)
	if err != nil {
		t.Fatalf("GET unknown task: %v", err)
	}
	_ = unknownTaskResp.Body.Close()
	if unknownTaskResp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown task status = %d, want 404", unknownTaskResp.StatusCode)
	}
}
