package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestGetWorkflowRunIDForJob covers the happy path (run ID extracted from the
// job payload) and the 404 case (missing job surfaces an error, not a zero
// value silently swallowed).
func TestGetWorkflowRunIDForJob(t *testing.T) {
	tests := []struct {
		name       string
		jobID      int64
		statusCode int
		response   interface{}
		wantRunID  int64
		wantErr    bool
	}{
		{
			name:       "success",
			jobID:      555,
			statusCode: http.StatusOK,
			response: WorkflowJob{
				ID:     555,
				RunID:  999,
				Name:   "build",
				Status: "completed",
			},
			wantRunID: 999,
			wantErr:   false,
		},
		{
			name:       "not found",
			jobID:      404404,
			statusCode: http.StatusNotFound,
			response:   map[string]string{"message": "Not Found"},
			wantRunID:  0,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/repos/owner/repo/actions/jobs/" + itoa(tt.jobID)
				if r.URL.Path != wantPath {
					t.Errorf("unexpected path: %s, want %s", r.URL.Path, wantPath)
				}
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
					t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			runID, err := client.GetWorkflowRunIDForJob(context.Background(), "owner", "repo", tt.jobID)

			if (err != nil) != tt.wantErr {
				t.Fatalf("GetWorkflowRunIDForJob() error = %v, wantErr %v", err, tt.wantErr)
			}
			if runID != tt.wantRunID {
				t.Errorf("runID = %d, want %d", runID, tt.wantRunID)
			}
		})
	}
}

// TestGetWorkflowRunIDForJob_RetriesOnServerError verifies retry semantics
// match the rest of the client: a transient 502 is retried via doRequest's
// shared WithRetryVoid path, and the run ID is returned once the retry
// succeeds.
func TestGetWorkflowRunIDForJob_RetriesOnServerError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(WorkflowJob{ID: 1, RunID: 42})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = fastRetryOpts()

	runID, err := client.GetWorkflowRunIDForJob(context.Background(), "owner", "repo", 1)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if runID != 42 {
		t.Errorf("runID = %d, want 42", runID)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 retry), got %d", calls.Load())
	}
}

// TestRerunFailedJobs covers the happy path (201 Created, empty body) and a
// non-2xx response surfacing an error.
func TestRerunFailedJobs(t *testing.T) {
	tests := []struct {
		name       string
		runID      int64
		statusCode int
		wantErr    bool
	}{
		{
			name:       "success",
			runID:      123,
			statusCode: http.StatusCreated,
			wantErr:    false,
		},
		{
			name:       "non-2xx surfaces error",
			runID:      456,
			statusCode: http.StatusForbidden,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/repos/owner/repo/actions/runs/" + itoa(tt.runID) + "/rerun-failed-jobs"
				if r.URL.Path != wantPath {
					t.Errorf("unexpected path: %s, want %s", r.URL.Path, wantPath)
				}
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.Header.Get("Authorization") != "Bearer "+testutil.FakeGitHubToken {
					t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
				}

				w.WriteHeader(tt.statusCode)
				if tt.statusCode >= 400 {
					_, _ = w.Write([]byte(`{"message":"error"}`))
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			err := client.RerunFailedJobs(context.Background(), "owner", "repo", tt.runID)

			if (err != nil) != tt.wantErr {
				t.Errorf("RerunFailedJobs() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestRerunFailedJobs_RetriesOnServerError verifies RerunFailedJobs shares
// the same retry semantics as the rest of the client (transient 503 retried
// via doRequest's WithRetryVoid, eventually succeeding).
func TestRerunFailedJobs_RetriesOnServerError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = fastRetryOpts()

	err := client.RerunFailedJobs(context.Background(), "owner", "repo", 1)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 retry), got %d", calls.Load())
	}
}

// TestRerunFailedJobs_ContextCancellation verifies context cancellation
// aborts the retry loop without waiting for the full retry budget, matching
// the existing WithRetry/ExecuteGraphQL cancellation contract.
func TestRerunFailedJobs_ContextCancellation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	client.retryOpts = RetryOptions{
		MaxRetries: 10,
		BaseDelay:  50 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
	}

	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	err := client.RerunFailedJobs(ctx, "owner", "repo", 1)
	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	if calls.Load() > 2 {
		t.Errorf("expected ≤2 calls before cancellation, got %d", calls.Load())
	}
}

// itoa avoids pulling in strconv just for path building in test assertions.
func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}
