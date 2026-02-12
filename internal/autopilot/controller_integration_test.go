//go:build integration

package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/adapters/github"
)

// integrationMockNotifier captures notifications for verification
type integrationMockNotifier struct {
	mu               sync.Mutex
	mergedCalls      []*PRState
	ciFailedCalls    []*PRState
	approvalCalls    []*PRState
	fixIssueCalls    []int
	releaseCalls     []string
}

func (m *integrationMockNotifier) NotifyMerged(ctx context.Context, prState *PRState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mergedCalls = append(m.mergedCalls, prState)
	return nil
}

func (m *integrationMockNotifier) NotifyCIFailed(ctx context.Context, prState *PRState, failedChecks []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ciFailedCalls = append(m.ciFailedCalls, prState)
	return nil
}

func (m *integrationMockNotifier) NotifyApprovalRequired(ctx context.Context, prState *PRState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvalCalls = append(m.approvalCalls, prState)
	return nil
}

func (m *integrationMockNotifier) NotifyFixIssueCreated(ctx context.Context, prState *PRState, issueNumber int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fixIssueCalls = append(m.fixIssueCalls, issueNumber)
	return nil
}

func (m *integrationMockNotifier) NotifyReleased(ctx context.Context, prState *PRState, releaseURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCalls = append(m.releaseCalls, releaseURL)
	return nil
}

// setupMockGitHubServer creates a test server that simulates GitHub API
func setupMockGitHubServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(handler))
}

// TestController_Integration_PRLifecycle tests the full PR state machine
func TestController_Integration_PRLifecycle(t *testing.T) {
	// Track API calls
	var mu sync.Mutex
	apiCalls := make(map[string]int)

	server := setupMockGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		apiCalls[r.URL.Path]++
		mu.Unlock()

		switch {
		// GET /repos/owner/repo/pulls/1
		case r.URL.Path == "/repos/test/repo/pulls/1" && r.Method == "GET":
			json.NewEncoder(w).Encode(github.PullRequest{
				Number: 1,
				Head: github.PRRef{
					SHA: "abc123def456",
				},
				Mergeable: integrationBoolPtr(true),
			})

		// GET /repos/owner/repo/commits/SHA/check-runs
		case r.URL.Path == "/repos/test/repo/commits/abc123def456/check-runs" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"total_count": 1,
				"check_runs": []map[string]interface{}{
					{
						"name":       "CI",
						"status":     "completed",
						"conclusion": "success",
					},
				},
			})

		// PUT /repos/owner/repo/pulls/1/merge
		case r.URL.Path == "/repos/test/repo/pulls/1/merge" && r.Method == "PUT":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"merged":  true,
				"sha":     "mergesha123",
				"message": "Pull Request successfully merged",
			})

		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer server.Close()

	// Create controller with mock client
	ghClient := github.NewClientWithBaseURL("test-token", server.URL)
	cfg := &Config{
		Enabled:        true,
		Environment:    EnvDev,
		AutoMerge:      true,
		MergeMethod:    "squash",
		CIWaitTimeout:  5 * time.Minute,
		CIPollInterval: 100 * time.Millisecond,
		CIChecks: &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 100 * time.Millisecond,
		},
	}

	controller := NewController(cfg, ghClient, nil, "test", "repo")
	notifier := &integrationMockNotifier{}
	controller.SetNotifier(notifier)

	// Register a PR
	controller.OnPRCreated(1, "https://github.com/test/repo/pull/1", 100, "abc123def456", "pilot/GH-100")

	// Verify PR is tracked
	if len(controller.activePRs) != 1 {
		t.Fatalf("Expected 1 active PR, got %d", len(controller.activePRs))
	}

	// Process through state machine
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// First process: PRCreated -> WaitingCI
	err := controller.ProcessPR(ctx, 1)
	if err != nil {
		t.Fatalf("ProcessPR (PRCreated) failed: %v", err)
	}

	prState := controller.activePRs[1]
	if prState.Stage != StageWaitingCI {
		t.Errorf("Expected stage WaitingCI, got %s", prState.Stage)
	}

	// Second process: WaitingCI -> CIPassed (CI success response)
	err = controller.ProcessPR(ctx, 1)
	if err != nil {
		t.Fatalf("ProcessPR (WaitingCI) failed: %v", err)
	}

	if prState.Stage != StageCIPassed {
		t.Errorf("Expected stage CIPassed, got %s", prState.Stage)
	}

	// Third process: CIPassed -> Merging (dev mode, no approval needed)
	err = controller.ProcessPR(ctx, 1)
	if err != nil {
		t.Fatalf("ProcessPR (CIPassed) failed: %v", err)
	}

	if prState.Stage != StageMerging {
		t.Errorf("Expected stage Merging, got %s", prState.Stage)
	}

	// Fourth process: Merging -> Merged
	err = controller.ProcessPR(ctx, 1)
	if err != nil {
		t.Fatalf("ProcessPR (Merging) failed: %v", err)
	}

	if prState.Stage != StageMerged {
		t.Errorf("Expected stage Merged, got %s", prState.Stage)
	}

	// Verify API was called
	mu.Lock()
	defer mu.Unlock()
	if apiCalls["/repos/test/repo/pulls/1"] < 1 {
		t.Error("Expected PR fetch API call")
	}
}

// TestController_Integration_CIFailure tests CI failure handling
func TestController_Integration_CIFailure(t *testing.T) {
	ciCheckCount := 0

	server := setupMockGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test/repo/pulls/2" && r.Method == "GET":
			json.NewEncoder(w).Encode(github.PullRequest{
				Number: 2,
				Head: github.PRRef{
					SHA: "def456abc789",
				},
				Mergeable: integrationBoolPtr(true),
			})

		case r.URL.Path == "/repos/test/repo/commits/def456abc789/check-runs" && r.Method == "GET":
			ciCheckCount++
			json.NewEncoder(w).Encode(map[string]interface{}{
				"total_count": 1,
				"check_runs": []map[string]interface{}{
					{
						"name":       "CI",
						"status":     "completed",
						"conclusion": "failure",
					},
				},
			})

		case r.URL.Path == "/repos/test/repo/issues" && r.Method == "POST":
			// Fix issue creation
			json.NewEncoder(w).Encode(map[string]interface{}{
				"number": 201,
			})

		case r.URL.Path == "/repos/test/repo/pulls/2" && r.Method == "PATCH":
			// PR close
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL("test-token", server.URL)
	cfg := &Config{
		Enabled:          true,
		Environment:      EnvDev,
		AutoMerge:        true,
		CIWaitTimeout:    1 * time.Minute,
		CIPollInterval:   50 * time.Millisecond,
		AutoCreateIssues: true,
		IssueLabels:      []string{"pilot", "autopilot-fix"},
		CIChecks: &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 50 * time.Millisecond,
		},
	}

	controller := NewController(cfg, ghClient, nil, "test", "repo")
	notifier := &integrationMockNotifier{}
	controller.SetNotifier(notifier)

	controller.OnPRCreated(2, "https://github.com/test/repo/pull/2", 200, "def456abc789", "pilot/GH-200")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// PRCreated -> WaitingCI
	_ = controller.ProcessPR(ctx, 2)

	// WaitingCI -> CIFailed
	_ = controller.ProcessPR(ctx, 2)

	prState := controller.activePRs[2]
	if prState.Stage != StageCIFailed {
		t.Errorf("Expected stage CIFailed, got %s", prState.Stage)
	}

	// CIFailed -> creates fix issue
	_ = controller.ProcessPR(ctx, 2)

	// Verify CI failure notification was sent
	notifier.mu.Lock()
	ciFailedCount := len(notifier.ciFailedCalls)
	notifier.mu.Unlock()

	if ciFailedCount != 1 {
		t.Errorf("Expected 1 CI failed notification, got %d", ciFailedCount)
	}
}

// TestController_Integration_ProdApproval tests prod environment approval flow
func TestController_Integration_ProdApproval(t *testing.T) {
	server := setupMockGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test/repo/pulls/3" && r.Method == "GET":
			json.NewEncoder(w).Encode(github.PullRequest{
				Number: 3,
				Head: github.PRRef{
					SHA: "prodsha123",
				},
				Mergeable: integrationBoolPtr(true),
			})

		case r.URL.Path == "/repos/test/repo/commits/prodsha123/check-runs" && r.Method == "GET":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"total_count": 1,
				"check_runs": []map[string]interface{}{
					{
						"name":       "CI",
						"status":     "completed",
						"conclusion": "success",
					},
				},
			})

		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL("test-token", server.URL)
	cfg := &Config{
		Enabled:        true,
		Environment:    EnvProd, // Production requires approval
		AutoMerge:      true,
		CIWaitTimeout:  1 * time.Minute,
		CIPollInterval: 50 * time.Millisecond,
		CIChecks: &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 50 * time.Millisecond,
		},
	}

	controller := NewController(cfg, ghClient, nil, "test", "repo")
	notifier := &integrationMockNotifier{}
	controller.SetNotifier(notifier)

	controller.OnPRCreated(3, "https://github.com/test/repo/pull/3", 300, "prodsha123", "pilot/GH-300")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// PRCreated -> WaitingCI
	_ = controller.ProcessPR(ctx, 3)

	// WaitingCI -> CIPassed
	_ = controller.ProcessPR(ctx, 3)

	prState := controller.activePRs[3]
	if prState.Stage != StageCIPassed {
		t.Errorf("Expected stage CIPassed, got %s", prState.Stage)
	}

	// CIPassed -> AwaitApproval (prod mode)
	_ = controller.ProcessPR(ctx, 3)

	if prState.Stage != StageAwaitApproval {
		t.Errorf("Expected stage AwaitApproval in prod mode, got %s", prState.Stage)
	}

	// Verify approval notification was sent
	notifier.mu.Lock()
	approvalCount := len(notifier.approvalCalls)
	notifier.mu.Unlock()

	if approvalCount != 1 {
		t.Errorf("Expected 1 approval notification, got %d", approvalCount)
	}
}

// TestController_Integration_CircuitBreaker tests per-PR circuit breaker
func TestController_Integration_CircuitBreaker(t *testing.T) {
	mergeAttempts := 0

	server := setupMockGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test/repo/pulls/4" && r.Method == "GET":
			// Return valid PR data to progress through states
			json.NewEncoder(w).Encode(github.PullRequest{
				Number:    4,
				Head:      github.PRRef{SHA: "cbsha123"},
				Mergeable: integrationBoolPtr(true),
			})

		case r.URL.Path == "/repos/test/repo/commits/cbsha123/check-runs" && r.Method == "GET":
			// CI passes
			json.NewEncoder(w).Encode(map[string]interface{}{
				"total_count": 1,
				"check_runs": []map[string]interface{}{
					{"name": "CI", "status": "completed", "conclusion": "success"},
				},
			})

		case r.URL.Path == "/repos/test/repo/pulls/4/merge" && r.Method == "PUT":
			// Merge always fails - this triggers circuit breaker
			mergeAttempts++
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"message": "Pull Request is not mergeable",
			})

		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL("test-token", server.URL)
	cfg := &Config{
		Enabled:             true,
		Environment:         EnvDev,
		AutoMerge:           true,
		CIWaitTimeout:       1 * time.Minute,
		CIPollInterval:      50 * time.Millisecond,
		MaxFailures:         3, // Circuit breaker threshold
		FailureResetTimeout: 1 * time.Hour,
		CIChecks: &CIChecksConfig{
			Mode:                 "auto",
			DiscoveryGracePeriod: 50 * time.Millisecond,
		},
	}

	controller := NewController(cfg, ghClient, nil, "test", "repo")

	controller.OnPRCreated(4, "https://github.com/test/repo/pull/4", 400, "cbsha123", "pilot/GH-400")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Process through state machine until we hit merge failures
	// PRCreated -> WaitingCI -> CIPassed -> Merging (fails repeatedly)
	for i := 0; i < 10; i++ {
		_ = controller.ProcessPR(ctx, 4)

		// Once circuit is open, stop
		if controller.isPRCircuitOpen(4) {
			break
		}
	}

	// Check if circuit is open after merge failures
	if !controller.isPRCircuitOpen(4) {
		t.Errorf("Expected per-PR circuit breaker to be open after merge failures (attempts: %d)", mergeAttempts)
	}
}

// TestController_Integration_MultiplePRs tests concurrent PR handling
func TestController_Integration_MultiplePRs(t *testing.T) {
	server := setupMockGitHubServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test/repo/pulls/10" && r.Method == "GET":
			json.NewEncoder(w).Encode(github.PullRequest{
				Number: 10,
				Head:   github.PRRef{SHA: "sha10"},
			})
		case r.URL.Path == "/repos/test/repo/pulls/11" && r.Method == "GET":
			json.NewEncoder(w).Encode(github.PullRequest{
				Number: 11,
				Head:   github.PRRef{SHA: "sha11"},
			})
		case r.URL.Path == "/repos/test/repo/pulls/12" && r.Method == "GET":
			json.NewEncoder(w).Encode(github.PullRequest{
				Number: 12,
				Head:   github.PRRef{SHA: "sha12"},
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	defer server.Close()

	ghClient := github.NewClientWithBaseURL("test-token", server.URL)
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.CIPollInterval = 50 * time.Millisecond

	controller := NewController(cfg, ghClient, nil, "test", "repo")

	// Register multiple PRs
	controller.OnPRCreated(10, "https://github.com/test/repo/pull/10", 1000, "sha10", "pilot/GH-1000")
	controller.OnPRCreated(11, "https://github.com/test/repo/pull/11", 1001, "sha11", "pilot/GH-1001")
	controller.OnPRCreated(12, "https://github.com/test/repo/pull/12", 1002, "sha12", "pilot/GH-1002")

	// Verify all PRs are tracked
	if len(controller.activePRs) != 3 {
		t.Errorf("Expected 3 active PRs, got %d", len(controller.activePRs))
	}

	// Each PR should be independent
	for prNum, prState := range controller.activePRs {
		if prState.Stage != StagePRCreated {
			t.Errorf("PR %d: expected initial stage PRCreated, got %s", prNum, prState.Stage)
		}
	}
}

func integrationBoolPtr(b bool) *bool {
	return &b
}
