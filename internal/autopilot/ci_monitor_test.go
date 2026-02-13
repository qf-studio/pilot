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

func TestNewCIMonitor_DevCITimeout(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.Environment = EnvDev
	cfg.DevCITimeout = 5 * time.Minute
	cfg.CIWaitTimeout = 30 * time.Minute

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	if monitor == nil {
		t.Fatal("NewCIMonitor returned nil")
	}
	// Dev environment should use DevCITimeout
	if monitor.waitTimeout != cfg.DevCITimeout {
		t.Errorf("waitTimeout = %v, want %v (DevCITimeout for dev env)", monitor.waitTimeout, cfg.DevCITimeout)
	}
}

func TestNewCIMonitor_StageProdUseCIWaitTimeout(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)

	// Test stage environment
	cfgStage := DefaultConfig()
	cfgStage.Environment = EnvStage
	cfgStage.DevCITimeout = 5 * time.Minute
	cfgStage.CIWaitTimeout = 30 * time.Minute

	monitorStage := NewCIMonitor(ghClient, "owner", "repo", cfgStage)
	if monitorStage.waitTimeout != cfgStage.CIWaitTimeout {
		t.Errorf("stage waitTimeout = %v, want %v (CIWaitTimeout)", monitorStage.waitTimeout, cfgStage.CIWaitTimeout)
	}

	// Test prod environment
	cfgProd := DefaultConfig()
	cfgProd.Environment = EnvProd
	cfgProd.DevCITimeout = 5 * time.Minute
	cfgProd.CIWaitTimeout = 30 * time.Minute

	monitorProd := NewCIMonitor(ghClient, "owner", "repo", cfgProd)
	if monitorProd.waitTimeout != cfgProd.CIWaitTimeout {
		t.Errorf("prod waitTimeout = %v, want %v (CIWaitTimeout)", monitorProd.waitTimeout, cfgProd.CIWaitTimeout)
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
	// Use manual mode with specific required checks
	cfg.CIChecks = &CIChecksConfig{
		Mode:     "manual",
		Required: []string{"build", "test"},
	}

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
		name       string
		checkRuns  []github.CheckRun
		wantFailed []string
		wantErr    bool
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
	// Use manual mode with specific required checks
	cfg.CIChecks = &CIChecksConfig{
		Mode:     "manual",
		Required: []string{"build", "test"}, // 'test' doesn't exist
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// Should timeout because 'test' is pending (not found)
	_, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err == nil {
		t.Fatal("WaitForCI() should timeout when required check is missing")
	}
}

func TestCIMonitor_GetCIStatus(t *testing.T) {
	// Test GetCIStatus returns point-in-time status
	tests := []struct {
		name       string
		checkRuns  []github.CheckRun
		wantStatus CIStatus
	}{
		{
			name: "all checks success",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
			},
			wantStatus: CISuccess,
		},
		{
			name: "one check failing",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},
			},
			wantStatus: CIFailure,
		},
		{
			name: "one check pending",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunQueued, Conclusion: ""},
			},
			wantStatus: CIPending,
		},
		{
			name: "one check running",
			checkRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunInProgress, Conclusion: ""},
			},
			wantStatus: CIPending, // Running maps to pending in aggregate
		},
		{
			name:       "no checks",
			checkRuns:  []github.CheckRun{},
			wantStatus: CIPending,
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
			cfg.RequiredChecks = []string{} // Check all runs

			monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

			status, err := monitor.GetCIStatus(context.Background(), "abc1234")
			if err != nil {
				t.Fatalf("GetCIStatus() error = %v", err)
			}
			if status != tt.wantStatus {
				t.Errorf("GetCIStatus() status = %s, want %s", status, tt.wantStatus)
			}
		})
	}
}

func TestCIMonitor_AutoDiscovery(t *testing.T) {
	// Test auto mode discovers checks from API and returns correct status
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 3,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
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
	// Use auto mode (default when no RequiredChecks set)
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{
		Mode:                 "auto",
		DiscoveryGracePeriod: 10 * time.Millisecond,
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	status, err := monitor.CheckCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("CheckCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("CheckCI() status = %s, want %s", status, CISuccess)
	}

	// Verify discovered checks are stored
	discovered := monitor.GetDiscoveredChecks("abc1234")
	if len(discovered) != 3 {
		t.Errorf("GetDiscoveredChecks() = %v, want 3 checks", discovered)
	}
}

func TestCIMonitor_AutoDiscovery_WithExclusions(t *testing.T) {
	// Test auto mode excludes checks matching patterns
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := github.CheckRunsResponse{
			TotalCount: 4,
			CheckRuns: []github.CheckRun{
				{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "test", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				{Name: "codecov/patch", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure},     // Should be excluded
				{Name: "coverage-optional", Status: github.CheckRunCompleted, Conclusion: github.ConclusionFailure}, // Should be excluded
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
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{
		Mode:                 "auto",
		Exclude:              []string{"codecov/*", "*-optional"},
		DiscoveryGracePeriod: 10 * time.Millisecond,
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// Should succeed because the failing checks are excluded
	status, err := monitor.CheckCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("CheckCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("CheckCI() status = %s, want %s (excluded checks should be ignored)", status, CISuccess)
	}

	// Verify only non-excluded checks are discovered
	discovered := monitor.GetDiscoveredChecks("abc1234")
	if len(discovered) != 2 {
		t.Errorf("GetDiscoveredChecks() = %v, want 2 checks (build, test)", discovered)
	}
}

func TestCIMonitor_matchesExclude_GlobPatterns(t *testing.T) {
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.CIChecks = &CIChecksConfig{
		Mode: "auto",
		Exclude: []string{
			"codecov/*",       // Glob pattern
			"*-optional",      // Glob pattern
			"skip-me",         // Exact match
			"prefix-*-suffix", // Complex glob
		},
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	tests := []struct {
		name string
		want bool
	}{
		{"codecov/patch", true},
		{"codecov/project", true},
		{"codecov", false}, // No glob match
		{"test-optional", true},
		{"optional", false}, // Doesn't match *-optional
		{"skip-me", true},   // Exact match
		{"skip-me-too", false},
		{"prefix-x-suffix", true},
		{"prefix--suffix", true},
		{"build", false},
		{"test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := monitor.matchesExclude(tt.name)
			if got != tt.want {
				t.Errorf("matchesExclude(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestCIMonitor_AutoDiscovery_GracePeriod(t *testing.T) {
	// Test that auto mode waits during grace period when no checks exist
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := github.CheckRunsResponse{
			TotalCount: 0,
			CheckRuns:  []github.CheckRun{},
		}
		// After grace period (3rd call), return checks
		if callCount >= 3 {
			resp = github.CheckRunsResponse{
				TotalCount: 1,
				CheckRuns: []github.CheckRun{
					{Name: "build", Status: github.CheckRunCompleted, Conclusion: github.ConclusionSuccess},
				},
			}
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	ghClient := github.NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cfg := DefaultConfig()
	cfg.CIPollInterval = 10 * time.Millisecond
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{
		Mode:                 "auto",
		DiscoveryGracePeriod: 50 * time.Millisecond,
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// Wait for CI - should eventually succeed when checks appear
	status, err := monitor.WaitForCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("WaitForCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("WaitForCI() status = %s, want %s", status, CISuccess)
	}
	if callCount < 3 {
		t.Errorf("expected at least 3 calls (waiting for checks), got %d", callCount)
	}
}

func TestCIMonitor_AutoDiscovery_GracePeriodExpired(t *testing.T) {
	// Test that auto mode returns success if grace period expires with no checks
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
	cfg.CIWaitTimeout = 1 * time.Second
	cfg.RequiredChecks = nil
	cfg.CIChecks = &CIChecksConfig{
		Mode:                 "auto",
		DiscoveryGracePeriod: 20 * time.Millisecond, // Very short grace period
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	// First call starts grace period
	status, _ := monitor.CheckCI(context.Background(), "abc1234")
	if status != CIPending {
		t.Errorf("First CheckCI() status = %s, want %s", status, CIPending)
	}

	// Wait for grace period to expire
	time.Sleep(30 * time.Millisecond)

	// Second call should return success after grace period expired
	status, err := monitor.CheckCI(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("CheckCI() error = %v", err)
	}
	if status != CISuccess {
		t.Errorf("CheckCI() after grace period status = %s, want %s", status, CISuccess)
	}
}

func TestCIMonitor_LegacyRequiredChecksUsesManualMode(t *testing.T) {
	// Test that legacy RequiredChecks config is converted to manual mode
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.RequiredChecks = []string{"build", "test"}
	cfg.CIChecks = nil // No new-style config

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	if monitor.ciChecks == nil {
		t.Fatal("ciChecks should be set")
	}
	if monitor.ciChecks.Mode != "manual" {
		t.Errorf("ciChecks.Mode = %s, want manual", monitor.ciChecks.Mode)
	}
	if len(monitor.requiredChecks) != 2 {
		t.Errorf("requiredChecks = %v, want [build, test]", monitor.requiredChecks)
	}
}

func TestCIMonitor_NewConfigUsesManualMode(t *testing.T) {
	// Test that CIChecks with manual mode uses Required list
	ghClient := github.NewClient(testutil.FakeGitHubToken)
	cfg := DefaultConfig()
	cfg.RequiredChecks = nil // Ignored when CIChecks is set
	cfg.CIChecks = &CIChecksConfig{
		Mode:     "manual",
		Required: []string{"ci", "deploy"},
	}

	monitor := NewCIMonitor(ghClient, "owner", "repo", cfg)

	if monitor.ciChecks.Mode != "manual" {
		t.Errorf("ciChecks.Mode = %s, want manual", monitor.ciChecks.Mode)
	}
	if len(monitor.requiredChecks) != 2 {
		t.Errorf("requiredChecks = %v, want [ci, deploy]", monitor.requiredChecks)
	}
}
