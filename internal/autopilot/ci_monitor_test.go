package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewCIMonitor(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	if monitor == nil {
		t.Fatal("NewCIMonitor returned nil")
	}
	if monitor.owner != "owner" {
		t.Errorf("owner = %s, want owner", monitor.owner)
	}
	if monitor.repo != "repo" {
		t.Errorf("repo = %s, want repo", monitor.repo)
	}
	if monitor.pollInterval != cfg.CIPollInterval {
		t.Errorf("pollInterval = %v, want %v", monitor.pollInterval, cfg.CIPollInterval)
	}
	if monitor.waitTimeout != cfg.CIWaitTimeout {
		t.Errorf("waitTimeout = %v, want %v", monitor.waitTimeout, cfg.CIWaitTimeout)
	}
}

func TestCIMonitor_WaitForCI_Success(t *testing.T) {
	// Mock GitHub client returning success after 2 polls
	pollCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		resp := github.CheckRunsResponse{
			TotalCount: 3,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "lint", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
		}
		// First poll: pending, subsequent polls: success
		if pollCount == 1 {
			resp.CheckRuns[0].Status = github.CheckRunInProgress
			resp.CheckRuns[0].Conclusion = ""
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("WaitForCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("WaitForCI() status = %s, want %s", status, CISuccess)
	}
	if pollCount < 2 {
		t.Errorf("expected at least 2 polls, got %d", pollCount)
	}
}

func TestCIMonitor_WaitForCI_Failure(t *testing.T) {
	// Mock GitHub client returning failure
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 3,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "lint", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test", "lint"}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("WaitForCI() error = %v", err)
	}
	if status != CIFailure {
		t.Errorf("WaitForCI() status = %s, want %s", status, CIFailure)
	}
}

func TestCIMonitor_WaitForCI_Timeout(t *testing.T) {
	// Mock GitHub client always returning pending
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 1,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunInProgress, Conclusion: ""},
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 50 * time.Millisecond
	cfg.RequiredChecks = []string{"build"}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err == nil {
		t.Fatal("WaitForCI() should return timeout error")
	}
	if status != CIPending {
		t.Errorf("WaitForCI() status = %s, want %s", status, CIPending)
	}
}

func TestCIMonitor_WaitForCI_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 1,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunInProgress, Conclusion: ""},
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 100 * time.Millisecond
	cfg.CIWaitTimeout = 10 * time.Second

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	status, err := monitor.WaitForCI(ctx, "abc1234")
	if err == nil {
		t.Fatal("WaitForCI() should return error on context cancellation")
	}
	if status != CIPending {
		t.Errorf("WaitForCI() status = %s, want %s", status, CIPending)
	}
}

func TestCIMonitor_RequiredChecksOnly(t *testing.T) {
	// Verify only configured checks are monitored
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 4,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "lint", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure}, // Fails but not required
				{Name: "coverage", Status: github.CheckRunInProgress, Conclusion: ""},                  // Still running but not required
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build", "test"} // Only build and test are required

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("WaitForCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("WaitForCI() status = %s, want %s (unrequired checks should be ignored)", status, CISuccess)
	}
}

func TestCIMonitor_NoRequiredChecks(t *testing.T) {
	// When no required checks are configured, all checks are monitored
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 2,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{} // No required checks

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("WaitForCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("WaitForCI() status = %s, want %s", status, CISuccess)
	}
}

func TestCIMonitor_NoChecks(t *testing.T) {
	// When no checks exist at all, return pending
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 0,
			CheckRuns:  []github.CheckRun{},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 50 * time.Millisecond
	cfg.RequiredChecks = []string{} // No required checks, monitor all

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	_, err := monitor.WaitForCI(context.Background(), "abc1234")
	// Should timeout because no checks exist and status stays pending
	if err == nil {
		t.Fatal("WaitForCI() should timeout when no checks exist")
	}
}

func TestCIMonitor_GetFailedChecks(t *testing.T) {
	tests := []struct {
		name          string
		checkRuns     []github.CheckRun
		wantFailed    []string
		wantErr       bool
	}{
		{
			name: "multiple failures",
			checkRuns: []github.CheckRun{
				{Name: "build", Conclusion: github.ConclusionFailure},
				{Name: "test", Conclusion: github.ConclusionSuccess},
				{Name: "lint", Conclusion: github.ConclusionFailure},
			},
			wantFailed: []string{"build", "lint"},
			wantErr:    false,
		},
		{
			name: "no failures",
			checkRuns: []github.CheckRun{
				{Name: "build", Conclusion: github.ConclusionSuccess},
				{Name: "test", Conclusion: github.ConclusionSuccess},
			},
			wantFailed: nil,
			wantErr:    false,
		},
		{
			name: "all failures",
			checkRuns: []github.CheckRun{
				{Name: "build", Conclusion: github.ConclusionFailure},
				{Name: "test", Conclusion: github.ConclusionFailure},
			},
			wantFailed: []string{"build", "test"},
			wantErr:    false,
		},
		{
			name:       "empty check runs",
			checkRuns:  []github.CheckRun{},
			wantFailed: nil,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := github.CheckRunsResponse{
					TotalCount: len(tt.checkRuns),
					CheckRuns:  tt.checkRuns,
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()

			monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

			failed, err := monitor.GetFailedChecks(context.Background(), "abc1234")
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetFailedChecks() error = %v, wantErr %v", err, tt.wantErr)
			}

			if len(failed) != len(tt.wantFailed) {
				t.Errorf("GetFailedChecks() = %v, want %v", failed, tt.wantFailed)
			}

			for i, name := range failed {
				if i < len(tt.wantFailed) && name != tt.wantFailed[i] {
					t.Errorf("GetFailedChecks()[%d] = %s, want %s", i, name, tt.wantFailed[i])
				}
			}
		})
	}
}

func TestCIMonitor_GetFailedChecks_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	_, err := monitor.GetFailedChecks(context.Background(), "abc1234")
	if err == nil {
		t.Error("GetFailedChecks() should return error on API failure")
	}
}

func TestCIMonitor_GetCheckStatus(t *testing.T) {
	tests := []struct {
		name       string
		checkName  string
		checkRuns  []github.CheckRun
		wantStatus CIStatus
		wantErr    bool
	}{
		{
			name:      "check found - success",
			checkName: "build",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
			wantStatus: CISuccess,
			wantErr:    false,
		},
		{
			name:      "check found - failure",
			checkName: "build",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},
			},
			wantStatus: CIFailure,
			wantErr:    false,
		},
		{
			name:      "check found - in progress",
			checkName: "build",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunInProgress, Conclusion: ""},
			},
			wantStatus: CIRunning,
			wantErr:    false,
		},
		{
			name:      "check not found",
			checkName: "nonexistent",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
			wantStatus: CIPending,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := github.CheckRunsResponse{
					TotalCount: len(tt.checkRuns),
					CheckRuns:  tt.checkRuns,
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			cfg := DefaultConfig()

			monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

			status, err := monitor.GetCheckStatus(context.Background(), "abc1234", tt.checkName)
			if (err != nil) != tt.wantErr {
				t.Fatalf("GetCheckStatus() error = %v, wantErr %v", err, tt.wantErr)
			}
			if status != tt.wantStatus {
				t.Errorf("GetCheckStatus() = %s, want %s", status, tt.wantStatus)
			}
		})
	}
}

func TestCIMonitor_MapCheckStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     string
		conclusion string
		want       CIStatus
	}{
		{"queued", github.CheckRunQueued, "", CIRunning},
		{"in_progress", github.CheckRunInProgress, "", CIRunning},
		{"completed success", github.CheckRunCompleted, github.ConclusionSuccess, CISuccess},
		{"completed failure", github.CheckRunCompleted, github.ConclusionFailure, CIFailure},
		{"completed cancelled", github.CheckRunCompleted, github.ConclusionCancelled, CIFailure},
		{"completed timed_out", github.CheckRunCompleted, github.ConclusionTimedOut, CIFailure},
		{"completed skipped", github.CheckRunCompleted, github.ConclusionSkipped, CISuccess},
		{"completed neutral", github.CheckRunCompleted, github.ConclusionNeutral, CISuccess},
		{"completed unknown", github.CheckRunCompleted, "unknown", CIPending},
		{"unknown status", "unknown", "", CIPending},
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := monitor.mapCheckStatus(tt.status, tt.conclusion)
			if got != tt.want {
				t.Errorf("mapCheckStatus(%s, %s) = %s, want %s", tt.status, tt.conclusion, got, tt.want)
			}
		})
	}
}

func TestCIMonitor_AggregateStatus(t *testing.T) {
	tests := []struct {
		name     string
		statuses map[string]CIStatus
		want     CIStatus
	}{
		{
			name:     "all success",
			statuses: map[string]CIStatus{"build": CISuccess, "test": CISuccess},
			want:     CISuccess,
		},
		{
			name:     "one failure",
			statuses: map[string]CIStatus{"build": CISuccess, "test": CIFailure},
			want:     CIFailure,
		},
		{
			name:     "one pending",
			statuses: map[string]CIStatus{"build": CISuccess, "test": CIPending},
			want:     CIPending,
		},
		{
			name:     "one running",
			statuses: map[string]CIStatus{"build": CISuccess, "test": CIRunning},
			want:     CIPending,
		},
		{
			name:     "failure takes precedence over pending",
			statuses: map[string]CIStatus{"build": CIFailure, "test": CIPending},
			want:     CIFailure,
		},
		{
			name:     "empty statuses",
			statuses: map[string]CIStatus{},
			want:     CISuccess,
		},
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := monitor.aggregateStatus(tt.statuses)
			if got != tt.want {
				t.Errorf("aggregateStatus() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCIMonitor_CheckAllRuns(t *testing.T) {
	tests := []struct {
		name      string
		checkRuns *github.CheckRunsResponse
		want      CIStatus
	}{
		{
			name: "all success",
			checkRuns: &github.CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []github.CheckRun{
					{Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			},
			want: CISuccess,
		},
		{
			name: "one failure",
			checkRuns: &github.CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []github.CheckRun{
					{Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},
				},
			},
			want: CIFailure,
		},
		{
			name: "one pending",
			checkRuns: &github.CheckRunsResponse{
				TotalCount: 2,
				CheckRuns: []github.CheckRun{
					{Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
					{Status: github.CheckRunInProgress, Conclusion: ""},
				},
			},
			want: CIPending,
		},
		{
			name: "no checks",
			checkRuns: &github.CheckRunsResponse{
				TotalCount: 0,
				CheckRuns:  []github.CheckRun{},
			},
			want: CIPending,
		},
	}

	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := monitor.checkAllRuns(tt.checkRuns)
			if got != tt.want {
				t.Errorf("checkAllRuns() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCIMonitor_WaitForCI_APIErrorContinues(t *testing.T) {
	// Test that API errors during polling are logged but don't fail the wait
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call fails
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Subsequent calls succeed
		resp := github.CheckRunsResponse{
			TotalCount: 1,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = []string{"build"}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("WaitForCI() should recover from API errors: %v", err)
	}
	if status != CISuccess {
		t.Errorf("WaitForCI() status = %s, want %s", status, CISuccess)
	}
	if callCount < 2 {
		t.Errorf("expected at least 2 calls, got %d", callCount)
	}
}

func TestCIMonitor_WaitForCI_RequiredCheckNotFound(t *testing.T) {
	// Test behavior when a required check doesn't exist in the response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 1,
			CheckRuns: []github.CheckRun{
				// Only 'build' exists, but 'test' is required
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 50 * time.Millisecond
	cfg.RequiredChecks = []string{"build", "test"} // 'test' doesn't exist

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// Should timeout because 'test' is pending (not found)
	_, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err == nil {
		t.Fatal("WaitForCI() should timeout when required check is missing")
	}
}
