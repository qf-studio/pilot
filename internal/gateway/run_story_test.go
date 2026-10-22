package gateway

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func decodeExecutionResponse(t *testing.T, body []byte) executionResponse {
	t.Helper()
	var got executionResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, body)
	}
	return got
}

func TestHandleExecution_RunStory(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	started := t0.Add(30 * time.Second)
	prURL := "https://github.com/qf-studio/pilot/pull/77"
	wantIssue := "https://github.com/qf-studio/pilot/issues/42"

	ev := func(stage memory.Stage, d time.Duration, detail string) *memory.Event {
		return &memory.Event{ExecutionID: "exec-1", Stage: stage, OccurredAt: t0.Add(d), Detail: detail}
	}

	tests := []struct {
		name   string
		exec   *memory.Execution
		events []*memory.Event
		prs    []*AutopilotPRState
		check  func(t *testing.T, rs runStory)
	}{
		{
			name: "merged with all sources present",
			exec: &memory.Execution{
				ID: "exec-1", TaskID: "GH-42", ProjectPath: "/tmp/proj-a", Status: "completed",
				TaskTitle: "feat: add widget", TaskSourceAdapter: "github", TaskSourceIssueID: "42",
				PRUrl: prURL, FilesChanged: 3, LinesAdded: 120, LinesRemoved: 15,
				ModelName: "claude-sonnet-5-5", CreatedAt: t0, StartedAt: &started, DurationMs: 90000,
				EstimatedCostUSD: 1.5, TokensTotal: 1000,
			},
			events: []*memory.Event{
				ev(memory.StageQueued, 0, "queued"),
				ev(memory.StageQualityGate, 60*time.Second, `{"gate":"build","duration_ms":10,"passed":false,"retry_count":0}`),
				ev(memory.StageQualityGate, 61*time.Second, `{"gate":"test","duration_ms":10,"passed":true,"retry_count":0}`),
				ev(memory.StageQualityGate, 62*time.Second, `{"total_duration_ms":20,"gate_count":2}`),
				ev(memory.StageQualityGate, 70*time.Second, `{"gate":"build","duration_ms":10,"passed":true,"retry_count":1}`),
				ev(memory.StagePRCreated, 90*time.Second, "pr created: "+prURL),
				ev(memory.StagePRCreated, 95*time.Second, "pr created again"),
				ev(memory.StageMerged, 10*time.Minute, "merged"),
			},
			prs: []*AutopilotPRState{
				{PRNumber: 1, PRURL: "https://github.com/qf-studio/pilot/pull/1", CIStatus: "failure"},
				{PRNumber: 77, PRURL: prURL, CIStatus: "success"},
			},
			check: func(t *testing.T, rs runStory) {
				if rs.Asked.Title == nil || *rs.Asked.Title != "feat: add widget" {
					t.Errorf("asked.title = %v", rs.Asked.Title)
				}
				if rs.Asked.IssueURL == nil || *rs.Asked.IssueURL != wantIssue {
					t.Errorf("asked.issue_url = %v, want %s", rs.Asked.IssueURL, wantIssue)
				}
				if rs.Changed.PRURL == nil || *rs.Changed.PRURL != prURL {
					t.Errorf("changed.pr_url = %v", rs.Changed.PRURL)
				}
				if rs.Changed.FilesChanged == nil || *rs.Changed.FilesChanged != 3 ||
					rs.Changed.LinesAdded == nil || *rs.Changed.LinesAdded != 120 ||
					rs.Changed.LinesRemoved == nil || *rs.Changed.LinesRemoved != 15 {
					t.Errorf("changed diff stats = %+v", rs.Changed)
				}
				if rs.Changed.Summary != nil {
					t.Errorf("changed.summary = %v, want null (PR body not stored)", *rs.Changed.Summary)
				}
				wantGates := []runStoryGate{{Name: "build", Passed: true}, {Name: "test", Passed: true}}
				if !reflect.DeepEqual(rs.Verified.QualityGates, wantGates) {
					t.Errorf("verified.quality_gates = %+v, want %+v (latest per gate, summary row skipped)", rs.Verified.QualityGates, wantGates)
				}
				if rs.Verified.AcceptanceEvidence != nil {
					t.Errorf("verified.acceptance_evidence = %v, want null", *rs.Verified.AcceptanceEvidence)
				}
				if rs.Verified.CIStatus == nil || *rs.Verified.CIStatus != "success" {
					t.Errorf("verified.ci_status = %v, want success", rs.Verified.CIStatus)
				}
				if rs.Timings.QueuedAt == nil || !rs.Timings.QueuedAt.Equal(t0) {
					t.Errorf("timings.queued_at = %v", rs.Timings.QueuedAt)
				}
				if rs.Timings.StartedAt == nil || !rs.Timings.StartedAt.Equal(started) {
					t.Errorf("timings.started_at = %v", rs.Timings.StartedAt)
				}
				if rs.Timings.PROpenedAt == nil || !rs.Timings.PROpenedAt.Equal(t0.Add(90*time.Second)) {
					t.Errorf("timings.pr_opened_at = %v, want earliest pr_created", rs.Timings.PROpenedAt)
				}
				if rs.Timings.MergedAt == nil || !rs.Timings.MergedAt.Equal(t0.Add(10*time.Minute)) {
					t.Errorf("timings.merged_at = %v", rs.Timings.MergedAt)
				}
				if rs.Timings.DurationMS == nil || *rs.Timings.DurationMS != 90000 {
					t.Errorf("timings.duration_ms = %v", rs.Timings.DurationMS)
				}
				if rs.Outcome == nil || *rs.Outcome != "merged" {
					t.Errorf("outcome = %v, want merged", rs.Outcome)
				}
				if rs.FailClass != nil {
					t.Errorf("failure_class = %v, want null", *rs.FailClass)
				}
				if rs.ModelName == nil || *rs.ModelName != "claude-sonnet-5-5" {
					t.Errorf("model_name = %v", rs.ModelName)
				}
			},
		},
		{
			name: "running execution is null where nothing is recorded",
			exec: &memory.Execution{
				ID: "exec-1", TaskID: "GH-43", ProjectPath: "/tmp/proj-a", Status: "running",
				CreatedAt: t0,
			},
			events: []*memory.Event{ev(memory.StageQueued, 0, "queued"), ev(memory.StageRunning, time.Second, "")},
			check: func(t *testing.T, rs runStory) {
				if rs.Outcome == nil || *rs.Outcome != "running" {
					t.Errorf("outcome = %v, want running", rs.Outcome)
				}
				if rs.Timings.QueuedAt == nil {
					t.Error("timings.queued_at = null, want created_at")
				}
				nulls := map[string]bool{
					"asked.title":                  rs.Asked.Title == nil,
					"asked.issue_url":              rs.Asked.IssueURL == nil,
					"changed.pr_url":               rs.Changed.PRURL == nil,
					"changed.files_changed":        rs.Changed.FilesChanged == nil,
					"changed.lines_added":          rs.Changed.LinesAdded == nil,
					"changed.lines_removed":        rs.Changed.LinesRemoved == nil,
					"changed.summary":              rs.Changed.Summary == nil,
					"verified.quality_gates":       rs.Verified.QualityGates == nil,
					"verified.acceptance_evidence": rs.Verified.AcceptanceEvidence == nil,
					"verified.ci_status":           rs.Verified.CIStatus == nil,
					"timings.started_at":           rs.Timings.StartedAt == nil,
					"timings.pr_opened_at":         rs.Timings.PROpenedAt == nil,
					"timings.merged_at":            rs.Timings.MergedAt == nil,
					"timings.duration_ms":          rs.Timings.DurationMS == nil,
					"failure_class":                rs.FailClass == nil,
					"model_name":                   rs.ModelName == nil,
				}
				for field, isNull := range nulls {
					if !isNull {
						t.Errorf("%s is not null for a running execution", field)
					}
				}
			},
		},
		{
			name: "failed execution with ci failure class",
			exec: &memory.Execution{
				ID: "exec-1", TaskID: "GH-44", ProjectPath: "/tmp/proj-a", Status: "failed",
				Error: "ci failed", PRUrl: prURL, CreatedAt: t0, StartedAt: &started, DurationMs: 5000,
			},
			events: []*memory.Event{
				ev(memory.StagePRCreated, time.Minute, "pr created"),
				ev(memory.StageCIFailed, 2*time.Minute, "ci failed"),
				ev(memory.StageFailed, 3*time.Minute, "failed"),
			},
			check: func(t *testing.T, rs runStory) {
				if rs.Outcome == nil || *rs.Outcome != "failed" {
					t.Errorf("outcome = %v, want failed", rs.Outcome)
				}
				if rs.FailClass == nil || *rs.FailClass != "ci" {
					t.Errorf("failure_class = %v, want ci", rs.FailClass)
				}
				if rs.Timings.MergedAt != nil {
					t.Errorf("timings.merged_at = %v, want null", rs.Timings.MergedAt)
				}
			},
		},
		{
			name: "failed execution without ci event is unknown class",
			exec: &memory.Execution{ID: "exec-1", TaskID: "GH-45", ProjectPath: "/tmp/proj-a", Status: "failed", CreatedAt: t0},
			check: func(t *testing.T, rs runStory) {
				if rs.FailClass == nil || *rs.FailClass != "unknown" {
					t.Errorf("failure_class = %v, want unknown", rs.FailClass)
				}
			},
		},
		{
			name: "completed with PR but no merge needs review",
			exec: &memory.Execution{ID: "exec-1", TaskID: "GH-46", ProjectPath: "/tmp/proj-a", Status: "completed", PRUrl: prURL, CreatedAt: t0},
			check: func(t *testing.T, rs runStory) {
				if rs.Outcome == nil || *rs.Outcome != "review_needed" {
					t.Errorf("outcome = %v, want review_needed", rs.Outcome)
				}
			},
		},
		{
			name: "status with no honest mapping has null outcome",
			exec: &memory.Execution{ID: "exec-1", TaskID: "GH-47", ProjectPath: "/tmp/proj-a", Status: "canceled", CreatedAt: t0},
			check: func(t *testing.T, rs runStory) {
				if rs.Outcome != nil {
					t.Errorf("outcome = %v, want null", *rs.Outcome)
				}
			},
		},
		{
			name: "non-github source yields no issue url",
			exec: &memory.Execution{ID: "exec-1", TaskID: "LIN-1", ProjectPath: "/tmp/proj-a", Status: "completed", TaskSourceAdapter: "linear", TaskSourceIssueID: "42", PRUrl: prURL, CreatedAt: t0},
			check: func(t *testing.T, rs runStory) {
				if rs.Asked.IssueURL != nil {
					t.Errorf("asked.issue_url = %v, want null", *rs.Asked.IssueURL)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockDashboardStore{
				execByID:            map[string]*memory.Execution{"exec-1": tt.exec},
				eventsByExecutionID: map[string][]*memory.Event{"exec-1": tt.events},
			}
			s := newTestServerWithDashboard(store)
			s.autopilotProvider = &mockAutopilotProvider{activePRs: tt.prs}

			req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1", nil, "id", "exec-1")
			w := newTestResponseRecorder()
			s.handleExecution(w, req)

			if w.status != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
			}
			got := decodeExecutionResponse(t, w.body.Bytes())
			if got.ExecutionID != "exec-1" || got.TaskID != tt.exec.TaskID || got.Status != tt.exec.Status {
				t.Errorf("envelope = %+v", got)
			}
			tt.check(t, got.RunStory)
		})
	}
}

// TestHandleExecution_RunStory_NoCostFields pins the "no cost or token fields
// in run_story" scope fence against the serialized JSON.
func TestHandleExecution_RunStory_NoCostFields(t *testing.T) {
	store := &mockDashboardStore{
		execByID: map[string]*memory.Execution{
			"exec-1": {ID: "exec-1", TaskID: "GH-1", Status: "completed", EstimatedCostUSD: 2, TokensTotal: 99, CreatedAt: time.Now()},
		},
	}
	s := newTestServerWithDashboard(store)
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1", nil, "id", "exec-1")
	w := newTestResponseRecorder()
	s.handleExecution(w, req)

	var env struct {
		RunStory map[string]json.RawMessage `json:"run_story"`
	}
	if err := json.Unmarshal(w.body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := map[string]bool{"asked": true, "changed": true, "verified": true, "timings": true, "outcome": true, "failure_class": true, "model_name": true}
	for field := range env.RunStory {
		if !want[field] {
			t.Errorf("run_story has unexpected field %q (no cost/token fields allowed)", field)
		}
	}
	for field := range want {
		if _, ok := env.RunStory[field]; !ok {
			t.Errorf("run_story missing field %q", field)
		}
	}
}

func TestHandleExecution_Errors(t *testing.T) {
	tests := []struct {
		name  string
		store DashboardStore
		scope string
		want  int
	}{
		{name: "unknown execution", store: &mockDashboardStore{}, want: http.StatusNotFound},
		{name: "store nil", store: nil, want: http.StatusServiceUnavailable},
		{
			name:  "cross-project hidden under scoped dashboard",
			store: &mockDashboardStore{execByID: map[string]*memory.Execution{"exec-1": {ID: "exec-1", ProjectPath: "/tmp/proj-b"}}},
			scope: "/tmp/proj-a",
			want:  http.StatusNotFound,
		},
		{
			name: "events read error",
			store: &mockDashboardStore{
				execByID:               map[string]*memory.Execution{"exec-1": {ID: "exec-1"}},
				listExecutionEventsErr: http.ErrAbortHandler,
			},
			want: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewServer(&Config{Host: "127.0.0.1", Port: 0})
			if tt.store != nil {
				s.dashboardStore = tt.store
			}
			s.dashboardProjectPath = tt.scope
			req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1", nil, "id", "exec-1")
			w := newTestResponseRecorder()
			s.handleExecution(w, req)
			if w.status != tt.want {
				t.Errorf("status = %d, want %d", w.status, tt.want)
			}
		})
	}
}

func TestHandleTask_RunStory_NewestExecution(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	store := &mockDashboardStore{
		execsForTask: []*memory.Execution{
			{ID: "exec-new", TaskID: "GH-9", ProjectPath: "/tmp/proj-a", Status: "running", CreatedAt: t0.Add(time.Hour)},
			{ID: "exec-old", TaskID: "GH-9", ProjectPath: "/tmp/proj-a", Status: "failed", CreatedAt: t0},
		},
	}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-9?project=/tmp/proj-a", nil, "taskId", "GH-9")
	w := newTestResponseRecorder()
	s.handleTask(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.status, w.body.String())
	}
	got := decodeExecutionResponse(t, w.body.Bytes())
	if got.ExecutionID != "exec-new" {
		t.Errorf("executionId = %q, want exec-new", got.ExecutionID)
	}
	if got.RunStory.Outcome == nil || *got.RunStory.Outcome != "running" {
		t.Errorf("outcome = %v, want running", got.RunStory.Outcome)
	}
	if want := []string{"GH-9|/tmp/proj-a"}; !reflect.DeepEqual(store.gotListExecsForTaskArgs, want) {
		t.Errorf("ListExecutionsForTask args = %v, want %v", store.gotListExecsForTaskArgs, want)
	}
}

func TestHandleTask_UnknownTask_404(t *testing.T) {
	s := newTestServerWithDashboard(&mockDashboardStore{})
	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-9", nil, "taskId", "GH-9")
	w := newTestResponseRecorder()
	s.handleTask(w, req)
	if w.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.status)
	}
}
