package gateway

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

func TestRunStory_Outcomes(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	started := t0.Add(time.Minute)
	completed := t0.Add(11 * time.Minute)
	prAt := t0.Add(10 * time.Minute)
	mergedAt := t0.Add(2 * time.Hour)
	const prURL = "https://github.com/acme/widgets/pull/42"

	tests := []struct {
		name   string
		exec   *memory.Execution
		events []*memory.Event
		prs    []*AutopilotPRState
		check  func(t *testing.T, rs runStoryObject)
	}{
		{
			name: "merged with all sources present",
			exec: &memory.Execution{
				ID: "exec-m", TaskID: "GH-42", Status: "completed", PRUrl: prURL,
				TaskTitle: "Add widgets", TaskSourceAdapter: "github", TaskSourceIssueID: "42",
				FilesChanged: 3, LinesAdded: 40, LinesRemoved: 7, ModelName: "claude-sonnet-5-5",
				CreatedAt: t0, StartedAt: &started, CompletedAt: &completed, DurationMs: 600000,
			},
			events: []*memory.Event{
				{Stage: memory.StageQualityGate, Detail: `{"gate":"build","duration_ms":10,"passed":true,"retry_count":0}`},
				{Stage: memory.StageQualityGate, Detail: `{"gate":"test","duration_ms":10,"passed":false,"retry_count":0}`},
				{Stage: memory.StageQualityGate, Detail: `{"gate":"test","duration_ms":10,"passed":true,"retry_count":1}`},
				{Stage: memory.StageQualityGate, Detail: `{"total_duration_ms":30,"gate_count":3}`},
				{Stage: memory.StagePRCreated, OccurredAt: prAt},
				{Stage: memory.StageCIPassed, OccurredAt: prAt.Add(time.Minute)},
				{Stage: memory.StageMerged, OccurredAt: mergedAt},
			},
			prs: []*AutopilotPRState{{PRURL: prURL, CIStatus: "success", CreatedAt: prAt}},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "title", rs.Asked.Title, "Add widgets")
				eqStr(t, "issue_url", rs.Asked.IssueURL, "https://github.com/acme/widgets/issues/42")
				eqStr(t, "pr_url", rs.Changed.PRURL, prURL)
				if rs.Changed.FilesChanged == nil || *rs.Changed.FilesChanged != 3 ||
					*rs.Changed.LinesAdded != 40 || *rs.Changed.LinesRemoved != 7 {
					t.Errorf("diff stats = %+v", rs.Changed)
				}
				if rs.Changed.Summary != nil {
					t.Errorf("summary = %v, want nil", *rs.Changed.Summary)
				}
				wantGates := []runStoryGate{{"build", true}, {"test", true}}
				if !reflect.DeepEqual(rs.Verified.QualityGates, wantGates) {
					t.Errorf("gates = %+v, want %+v", rs.Verified.QualityGates, wantGates)
				}
				if rs.Verified.AcceptanceEvidence != nil {
					t.Errorf("acceptance_evidence = %v, want nil", *rs.Verified.AcceptanceEvidence)
				}
				eqStr(t, "ci_status", rs.Verified.CIStatus, "success")
				eqTime(t, "queued_at", rs.Timings.QueuedAt, t0)
				eqTime(t, "started_at", rs.Timings.StartedAt, started)
				eqTime(t, "pr_opened_at", rs.Timings.PROpenedAt, prAt)
				eqTime(t, "merged_at", rs.Timings.MergedAt, mergedAt)
				if rs.Timings.DurationMs == nil || *rs.Timings.DurationMs != 600000 {
					t.Errorf("duration_ms = %v", rs.Timings.DurationMs)
				}
				eqStr(t, "outcome", rs.Outcome, "merged")
				if rs.FailureClass != nil {
					t.Errorf("failure_class = %v, want nil", *rs.FailureClass)
				}
				eqStr(t, "model_name", rs.ModelName, "claude-sonnet-5-5")
			},
		},
		{
			name: "running execution is all nulls",
			exec: &memory.Execution{ID: "exec-r", TaskID: "GH-43", Status: "running", CreatedAt: t0, StartedAt: &started},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "outcome", rs.Outcome, "running")
				eqTime(t, "queued_at", rs.Timings.QueuedAt, t0)
				eqTime(t, "started_at", rs.Timings.StartedAt, started)
				if rs.Asked.Title != nil || rs.Asked.IssueURL != nil || rs.Changed.PRURL != nil ||
					rs.Changed.FilesChanged != nil || rs.Changed.LinesAdded != nil || rs.Changed.LinesRemoved != nil ||
					rs.Changed.Summary != nil || rs.Verified.QualityGates != nil ||
					rs.Verified.AcceptanceEvidence != nil || rs.Verified.CIStatus != nil ||
					rs.Timings.PROpenedAt != nil || rs.Timings.MergedAt != nil || rs.Timings.DurationMs != nil ||
					rs.FailureClass != nil || rs.ModelName != nil {
					t.Errorf("expected nulls, got %+v", rs)
				}
			},
		},
		{
			name: "completed with open PR needs review",
			exec: &memory.Execution{ID: "exec-o", Status: "completed", PRUrl: prURL, CreatedAt: t0, CompletedAt: &completed, DurationMs: 1},
			prs:  []*AutopilotPRState{{PRURL: prURL, CIStatus: "running"}},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "outcome", rs.Outcome, "review_needed")
				eqStr(t, "ci_status", rs.Verified.CIStatus, "running")
			},
		},
		{
			name: "failed with rate-limit error is env",
			exec: &memory.Execution{ID: "exec-f", Status: "failed", Error: "claude: 429 rate limit exceeded", CreatedAt: t0, CompletedAt: &completed},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "outcome", rs.Outcome, "failed")
				eqStr(t, "failure_class", rs.FailureClass, "env")
			},
		},
		{
			name: "failed with CI error is ci",
			exec: &memory.Execution{ID: "exec-c", Status: "failed", Error: "CI failed: checks failed", CreatedAt: t0},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "failure_class", rs.FailureClass, "ci")
			},
		},
		{
			name: "failed with scope text is scope",
			exec: &memory.Execution{ID: "exec-s", Status: "failed", Error: "scope guard: diff touches files outside task scope", CreatedAt: t0},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "failure_class", rs.FailureClass, "scope")
			},
		},
		{
			name:   "failed with unclassifiable error and ci_failed event is ci",
			exec:   &memory.Execution{ID: "exec-e", Status: "failed", Error: "boom", CreatedAt: t0},
			events: []*memory.Event{{Stage: memory.StageCIFailed, OccurredAt: prAt}},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "failure_class", rs.FailureClass, "ci")
				eqStr(t, "ci_status", rs.Verified.CIStatus, "failure")
			},
		},
		{
			name: "failed with unclassifiable error is unknown",
			exec: &memory.Execution{ID: "exec-u", Status: "failed", Error: "boom", CreatedAt: t0},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "failure_class", rs.FailureClass, "unknown")
			},
		},
		{
			name: "no_op without PR has no outcome",
			exec: &memory.Execution{ID: "exec-n", Status: "no_op", CreatedAt: t0},
			check: func(t *testing.T, rs runStoryObject) {
				if rs.Outcome != nil {
					t.Errorf("outcome = %v, want nil", *rs.Outcome)
				}
			},
		},
		{
			name: "issue_id already a URL is passed through",
			exec: &memory.Execution{ID: "exec-i", Status: "running", TaskSourceAdapter: "linear", TaskSourceIssueID: "https://linear.app/x/issue/X-1"},
			check: func(t *testing.T, rs runStoryObject) {
				eqStr(t, "issue_url", rs.Asked.IssueURL, "https://linear.app/x/issue/X-1")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, buildRunStory(tt.exec, tt.events, tt.prs))
		})
	}
}

func TestHandleExecutionRead(t *testing.T) {
	t0 := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	exec := &memory.Execution{ID: "exec-1", TaskID: "GH-1", ProjectPath: "/tmp/proj-a", Status: "running", CreatedAt: t0}

	tests := []struct {
		name       string
		store      *mockDashboardStore
		scope      string
		wantStatus int
	}{
		{"found", &mockDashboardStore{execByID: map[string]*memory.Execution{"exec-1": exec}}, "", http.StatusOK},
		{"unknown", &mockDashboardStore{}, "", http.StatusNotFound},
		{"cross-project hidden", &mockDashboardStore{execByID: map[string]*memory.Execution{"exec-1": exec}}, "/tmp/other", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServerWithDashboard(tt.store)
			s.dashboardProjectPath = tt.scope
			req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/executions/exec-1", nil, "id", "exec-1")
			w := newTestResponseRecorder()
			s.handleExecutionRead(w, req)
			if w.status != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", w.status, tt.wantStatus, w.body.String())
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(w.body.Bytes(), &raw); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if _, ok := raw["run_story"]; !ok {
				t.Fatalf("run_story missing: %s", w.body.String())
			}
			var story map[string]json.RawMessage
			if err := json.Unmarshal(raw["run_story"], &story); err != nil {
				t.Fatalf("unmarshal run_story: %v", err)
			}
			for _, k := range []string{"asked", "changed", "verified", "timings", "outcome", "failure_class", "model_name"} {
				if _, ok := story[k]; !ok {
					t.Errorf("run_story.%s missing", k)
				}
			}
			for _, banned := range []string{"cost", "token"} {
				if strings.Contains(strings.ToLower(string(raw["run_story"])), banned) {
					t.Errorf("run_story must not carry %q fields: %s", banned, raw["run_story"])
				}
			}
		})
	}
}

func TestHandleTaskRead(t *testing.T) {
	older := &memory.Execution{ID: "exec-old", TaskID: "GH-9", Status: "failed", Error: "boom"}
	newer := &memory.Execution{ID: "exec-new", TaskID: "GH-9", Status: "running"}
	store := &mockDashboardStore{execsForTask: []*memory.Execution{newer, older}}
	s := newTestServerWithDashboard(store)

	req := httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-9?project=/tmp/p", nil, "taskId", "GH-9")
	w := newTestResponseRecorder()
	s.handleTaskRead(w, req)
	if w.status != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", w.status, w.body.String())
	}
	var got executionReadResponse
	if err := json.Unmarshal(w.body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ExecutionID != "exec-new" {
		t.Errorf("executionId = %q, want exec-new", got.ExecutionID)
	}
	eqStr(t, "outcome", got.RunStory.Outcome, "running")
	if len(store.gotListExecsForTaskArgs) != 1 || store.gotListExecsForTaskArgs[0] != "GH-9|/tmp/p" {
		t.Errorf("list args = %v", store.gotListExecsForTaskArgs)
	}

	empty := newTestServerWithDashboard(&mockDashboardStore{})
	w = newTestResponseRecorder()
	empty.handleTaskRead(w, httpTestRequestWithPathValue(t, http.MethodGet, "/api/v1/tasks/GH-9", nil, "taskId", "GH-9"))
	if w.status != http.StatusNotFound {
		t.Errorf("unknown task status = %d, want 404", w.status)
	}
}

func eqStr(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %q", field, want)
		return
	}
	if *got != want {
		t.Errorf("%s = %q, want %q", field, *got, want)
	}
}

func eqTime(t *testing.T, field string, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %v", field, want)
		return
	}
	if !got.Equal(want) {
		t.Errorf("%s = %v, want %v", field, *got, want)
	}
}
