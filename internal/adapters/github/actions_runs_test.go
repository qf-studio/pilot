package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestListWorkflowRuns(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"total_count": 1,
			"workflow_runs": []map[string]interface{}{
				{"id": 7, "name": "CI", "workflow_id": 3, "status": "completed", "conclusion": "success", "head_sha": "abc", "run_attempt": 2},
			},
		})
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	runs, err := c.ListWorkflowRuns(context.Background(), "o", "r", "abc")
	if err != nil {
		t.Fatalf("ListWorkflowRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != 7 || runs[0].WorkflowID != 3 || runs[0].RunAttempt != 2 || runs[0].Conclusion != "success" {
		t.Errorf("runs = %+v", runs)
	}
	if gotQuery != "head_sha=abc&per_page=100&page=1" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestListWorkflowRunJobs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/7/jobs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"total_count": 1,
			"jobs":        []map[string]interface{}{{"id": 70, "run_id": 7, "name": "test", "status": "completed", "conclusion": "success"}},
		})
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	jobs, err := c.ListWorkflowRunJobs(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatalf("ListWorkflowRunJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Name != "test" || jobs[0].ID != 70 {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestIsCheckRunsForbidden_And_IsUnreadable(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		forbidden  bool
		unreadable bool
	}{
		{"nil", nil, false, false},
		{"403", &APIError{StatusCode: 403, Message: "x"}, true, true},
		{"404", &APIError{StatusCode: 404, Message: "x"}, false, true},
		{"403 rate limit", &RateLimitError{StatusCode: 403, Message: "x"}, false, false},
		{"500", &APIError{StatusCode: 500, Message: "x"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCheckRunsForbidden(tt.err); got != tt.forbidden {
				t.Errorf("IsCheckRunsForbidden = %v, want %v", got, tt.forbidden)
			}
			if got := IsUnreadable(tt.err); got != tt.unreadable {
				t.Errorf("IsUnreadable = %v, want %v", got, tt.unreadable)
			}
		})
	}
}
