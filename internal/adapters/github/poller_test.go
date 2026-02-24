package github

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewPoller(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		label    string
		interval time.Duration
		wantErr  bool
	}{
		{
			name:     "valid repo format",
			repo:     "owner/repo",
			label:    "pilot",
			interval: 30 * time.Second,
			wantErr:  false,
		},
		{
			name:     "invalid repo format - no slash",
			repo:     "ownerrepo",
			label:    "pilot",
			interval: 30 * time.Second,
			wantErr:  true,
		},
		{
			name:     "invalid repo format - multiple slashes",
			repo:     "owner/repo/extra",
			label:    "pilot",
			interval: 30 * time.Second,
			wantErr:  true,
		},
		{
			name:     "invalid repo format - empty",
			repo:     "",
			label:    "pilot",
			interval: 30 * time.Second,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testutil.FakeGitHubToken)
			poller, err := NewPoller(client, tt.repo, tt.label, tt.interval)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewPoller() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if poller == nil {
					t.Fatal("NewPoller returned nil")
				}
				if poller.client != client {
					t.Error("poller.client not set correctly")
				}
				if poller.label != tt.label {
					t.Errorf("poller.label = %s, want %s", poller.label, tt.label)
				}
				if poller.interval != tt.interval {
					t.Errorf("poller.interval = %v, want %v", poller.interval, tt.interval)
				}
			}
		})
	}
}

func TestNewPoller_ParsesOwnerAndRepo(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, err := NewPoller(client, "myorg/myrepo", "pilot", 30*time.Second)

	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	if poller.owner != "myorg" {
		t.Errorf("poller.owner = %s, want 'myorg'", poller.owner)
	}
	if poller.repo != "myrepo" {
		t.Errorf("poller.repo = %s, want 'myrepo'", poller.repo)
	}
}

func TestWithPollerLogger(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	// Create a custom logger
	customLogger := slog.Default()

	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithPollerLogger(customLogger),
	)
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	if poller.logger != customLogger {
		t.Error("custom logger should be set")
	}
}

func TestWithPollerLogger_DefaultLogger(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	// Without custom logger, should use default
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	if poller.logger == nil {
		t.Error("default logger should be set")
	}
}

func TestWithOnIssue(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	called := false
	callback := func(ctx context.Context, issue *Issue) error {
		called = true
		return nil
	}

	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithOnIssue(callback))
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	if poller.onIssue == nil {
		t.Error("onIssue callback not set")
	}

	// Call the callback to verify it was set correctly
	_ = poller.onIssue(context.Background(), &Issue{})
	if !called {
		t.Error("callback was not called")
	}
}

func TestPoller_IsProcessed(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Initially should not be processed
	if poller.IsProcessed(42) {
		t.Error("issue should not be processed initially")
	}

	// Mark as processed
	poller.markProcessed(42)

	// Now should be processed
	if !poller.IsProcessed(42) {
		t.Error("issue should be processed after marking")
	}

	// Another issue should not be processed
	if poller.IsProcessed(43) {
		t.Error("other issues should not be processed")
	}
}

func TestPoller_ProcessedCount(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	if poller.ProcessedCount() != 0 {
		t.Errorf("ProcessedCount() = %d, want 0", poller.ProcessedCount())
	}

	poller.markProcessed(1)
	if poller.ProcessedCount() != 1 {
		t.Errorf("ProcessedCount() = %d, want 1", poller.ProcessedCount())
	}

	poller.markProcessed(2)
	poller.markProcessed(3)
	if poller.ProcessedCount() != 3 {
		t.Errorf("ProcessedCount() = %d, want 3", poller.ProcessedCount())
	}

	// Re-marking same issue shouldn't increase count
	poller.markProcessed(1)
	if poller.ProcessedCount() != 3 {
		t.Errorf("ProcessedCount() = %d, want 3 after re-marking", poller.ProcessedCount())
	}
}

func TestPoller_Reset(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Mark some issues
	poller.markProcessed(1)
	poller.markProcessed(2)
	poller.markProcessed(3)

	if poller.ProcessedCount() != 3 {
		t.Errorf("ProcessedCount() = %d, want 3", poller.ProcessedCount())
	}

	// Reset
	poller.Reset()

	if poller.ProcessedCount() != 0 {
		t.Errorf("ProcessedCount() after reset = %d, want 0", poller.ProcessedCount())
	}

	if poller.IsProcessed(1) {
		t.Error("issue 1 should not be processed after reset")
	}
}

func TestPoller_ClearProcessed(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Mark some issues as processed
	poller.markProcessed(1)
	poller.markProcessed(2)
	poller.markProcessed(3)

	if poller.ProcessedCount() != 3 {
		t.Errorf("ProcessedCount() = %d, want 3", poller.ProcessedCount())
	}

	// Clear single issue
	poller.ClearProcessed(2)

	if poller.ProcessedCount() != 2 {
		t.Errorf("ProcessedCount() after clear = %d, want 2", poller.ProcessedCount())
	}

	if poller.IsProcessed(2) {
		t.Error("issue 2 should not be processed after ClearProcessed")
	}
	if !poller.IsProcessed(1) {
		t.Error("issue 1 should still be processed")
	}
	if !poller.IsProcessed(3) {
		t.Error("issue 3 should still be processed")
	}
}

func TestPoller_ConcurrentAccess(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	var wg sync.WaitGroup
	const numGoroutines = 10
	const numOpsPerGoroutine = 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				poller.markProcessed(base*numOpsPerGoroutine + j)
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				_ = poller.IsProcessed(j)
				_ = poller.ProcessedCount()
			}
		}()
	}

	wg.Wait()

	// Should have all unique issues marked
	expectedCount := numGoroutines * numOpsPerGoroutine
	if poller.ProcessedCount() != expectedCount {
		t.Errorf("ProcessedCount() = %d, want %d", poller.ProcessedCount(), expectedCount)
	}
}

func TestPoller_CheckForNewIssues(t *testing.T) {
	tests := []struct {
		name               string
		issues             []*Issue
		expectedProcessed  int
		callbackShouldFail bool
	}{
		{
			name: "processes new pilot issues",
			issues: []*Issue{
				{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}}},
				{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}},
			},
			expectedProcessed:  2,
			callbackShouldFail: false,
		},
		{
			name: "skips in-progress issues",
			issues: []*Issue{
				{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}},
				{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}},
			},
			expectedProcessed:  1,
			callbackShouldFail: false,
		},
		{
			name: "skips done issues",
			issues: []*Issue{
				{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}, {Name: LabelDone}}},
				{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}},
			},
			expectedProcessed:  1,
			callbackShouldFail: false,
		},
		{
			name:               "handles empty response",
			issues:             []*Issue{},
			expectedProcessed:  0,
			callbackShouldFail: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tt.issues)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

			processedIssues := []*Issue{}
			var mu sync.Mutex

			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithOnIssue(func(ctx context.Context, issue *Issue) error {
					if tt.callbackShouldFail {
						return errors.New("callback error")
					}
					mu.Lock()
					processedIssues = append(processedIssues, issue)
					mu.Unlock()
					return nil
				}),
			)

			// Call checkForNewIssues directly
			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			mu.Lock()
			got := len(processedIssues)
			mu.Unlock()
			if got != tt.expectedProcessed {
				t.Errorf("processed %d issues, want %d", got, tt.expectedProcessed)
			}
		})
	}
}

func TestPoller_CheckForNewIssues_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	callbackCalled := false
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			callbackCalled = true
			return nil
		}),
	)

	// Should not panic and should not call callback
	poller.checkForNewIssues(context.Background())

	if callbackCalled {
		t.Error("callback should not be called on API error")
	}
}

func TestPoller_CheckForNewIssues_CallbackError(t *testing.T) {
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}}},
		{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return errors.New("callback error")
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Both issues should be attempted (callback is called for both)
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("callback called %d times, want 2", got)
	}

	// In parallel mode, issues are pre-marked to prevent duplicate dispatch
	if !poller.IsProcessed(1) {
		t.Error("issue 1 should be marked as processed (pre-marked in parallel mode)")
	}
	if !poller.IsProcessed(2) {
		t.Error("issue 2 should be marked as processed (pre-marked in parallel mode)")
	}
}

func TestPoller_CheckForNewIssues_NoCallback(t *testing.T) {
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Create poller without callback
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Should not panic
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Issue should be marked as processed even without callback
	if !poller.IsProcessed(1) {
		t.Error("issue should be marked as processed when no callback is set")
	}
}

func TestPoller_CheckForNewIssues_SkipsAlreadyProcessed(t *testing.T) {
	// Issue 1 has pilot-failed so should be skipped
	// Issue 2 has only pilot so should be processed
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}, {Name: "pilot-failed"}}},
		{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Only issue 2 should trigger callback (issue 1 has pilot-failed)
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("callback called %d times, want 1 (issue with status labels should be skipped)", got)
	}
}

func TestPoller_CheckForNewIssues_AllowsRetryWhenLabelsRemoved(t *testing.T) {
	// Issue was processed before but pilot-failed was removed
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	// Pre-mark as processed (simulating previous failed attempt)
	poller.markProcessed(1)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Should retry since labels were removed
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("callback called %d times, want 1 (should retry after labels removed)", got)
	}
}

func TestPoller_Start_CancelsOnContextDone(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Should exit within reasonable time
	select {
	case <-done:
		// Good - poller stopped
	case <-time.After(1 * time.Second):
		t.Error("poller did not stop within timeout after context cancellation")
	}
}

func TestPoller_Start_InitialCheck(t *testing.T) {
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}}},
	}

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	callbackCalled := make(chan struct{}, 1)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 1*time.Hour, // Long interval
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			select {
			case callbackCalled <- struct{}{}:
			default:
			}
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go poller.Start(ctx)

	// Should get initial check quickly
	select {
	case <-callbackCalled:
		// Good - initial check happened
	case <-time.After(500 * time.Millisecond):
		t.Error("initial check did not happen quickly")
	}

	cancel()
}

// Tests for sequential execution mode

func TestWithExecutionMode(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	t.Run("sequential mode", func(t *testing.T) {
		poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
			WithExecutionMode(ExecutionModeSequential),
		)
		if err != nil {
			t.Fatalf("NewPoller() error = %v", err)
		}
		if poller.executionMode != ExecutionModeSequential {
			t.Errorf("executionMode = %v, want %v", poller.executionMode, ExecutionModeSequential)
		}
	})

	t.Run("parallel mode", func(t *testing.T) {
		poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
			WithExecutionMode(ExecutionModeParallel),
		)
		if err != nil {
			t.Fatalf("NewPoller() error = %v", err)
		}
		if poller.executionMode != ExecutionModeParallel {
			t.Errorf("executionMode = %v, want %v", poller.executionMode, ExecutionModeParallel)
		}
	})

	t.Run("auto mode", func(t *testing.T) {
		poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
			WithExecutionMode(ExecutionModeAuto),
		)
		if err != nil {
			t.Fatalf("NewPoller() error = %v", err)
		}
		if poller.executionMode != ExecutionModeAuto {
			t.Errorf("executionMode = %v, want %v", poller.executionMode, ExecutionModeAuto)
		}
	})

	t.Run("default is auto matching config default", func(t *testing.T) {
		poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
		if err != nil {
			t.Fatalf("NewPoller() error = %v", err)
		}
		if poller.executionMode != ExecutionModeAuto {
			t.Errorf("default executionMode = %v, want %v", poller.executionMode, ExecutionModeAuto)
		}
	})
}

func TestWithSequentialConfig(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionMode(ExecutionModeSequential),
		WithSequentialConfig(true, 15*time.Second, 2*time.Hour),
	)
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	if !poller.waitForMerge {
		t.Error("waitForMerge should be true")
	}
	if poller.prPollInterval != 15*time.Second {
		t.Errorf("prPollInterval = %v, want 15s", poller.prPollInterval)
	}
	if poller.prTimeout != 2*time.Hour {
		t.Errorf("prTimeout = %v, want 2h", poller.prTimeout)
	}
	if poller.mergeWaiter == nil {
		t.Error("mergeWaiter should be created in sequential mode with waitForMerge")
	}
}

func TestWithOnIssueWithResult(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	called := false
	callback := func(ctx context.Context, issue *Issue) (*IssueResult, error) {
		called = true
		return &IssueResult{
			Success:  true,
			PRNumber: 42,
			PRURL:    "https://github.com/owner/repo/pull/42",
		}, nil
	}

	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssueWithResult(callback),
	)
	if err != nil {
		t.Fatalf("NewPoller() error = %v", err)
	}

	if poller.onIssueWithResult == nil {
		t.Error("onIssueWithResult callback not set")
	}

	// Call the callback to verify it was set correctly
	result, _ := poller.onIssueWithResult(context.Background(), &Issue{})
	if !called {
		t.Error("callback was not called")
	}
	if result.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", result.PRNumber)
	}
}

func TestPoller_FindOldestUnprocessedIssue(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 3, Title: "Newest", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
		{Number: 1, Title: "Oldest", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Middle", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil")
	}
	if issue.Number != 1 {
		t.Errorf("found issue #%d, want #1 (oldest)", issue.Number)
	}
}

func TestPoller_FindOldestUnprocessedIssue_SkipsProcessedWithDoneLabel(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Oldest Done", Labels: []Label{{Name: "pilot"}, {Name: LabelDone}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil")
	}
	if issue.Number != 2 {
		t.Errorf("found issue #%d, want #2 (oldest without status label)", issue.Number)
	}
}

func TestPoller_FindOldestUnprocessedIssue_AllowsRetryWhenFailedLabelRemoved(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Was Failed", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Simulate: issue was processed (failed) but pilot-failed label was removed
	poller.markProcessed(1)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil - should allow retry")
	}
	// Should return #1 because pilot-failed was removed, allowing retry
	if issue.Number != 1 {
		t.Errorf("found issue #%d, want #1 (should retry after pilot-failed removed)", issue.Number)
	}
	// Verify it was removed from processed map
	if poller.IsProcessed(1) {
		t.Error("issue #1 should no longer be marked as processed")
	}
}

func TestPoller_FindOldestUnprocessedIssue_SkipsInProgress(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "In Progress", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil")
	}
	if issue.Number != 2 {
		t.Errorf("found issue #%d, want #2 (skips in-progress)", issue.Number)
	}
}

func TestPoller_FindOldestUnprocessedIssue_ReturnsNilWhenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue != nil {
		t.Error("issue should be nil when no unprocessed issues")
	}
}

func TestPoller_ProcessIssueSequential_UsesResultCallback(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	expectedResult := &IssueResult{
		Success:  true,
		PRNumber: 99,
		PRURL:    "https://github.com/owner/repo/pull/99",
	}

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return expectedResult, nil
		}),
	)

	result, err := poller.processIssueSequential(context.Background(), &Issue{Number: 1})

	if err != nil {
		t.Fatalf("processIssueSequential() error = %v", err)
	}
	if result.PRNumber != 99 {
		t.Errorf("PRNumber = %d, want 99", result.PRNumber)
	}
}

func TestPoller_ProcessIssueSequential_FallsBackToLegacyCallback(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)

	legacyCalled := false
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			legacyCalled = true
			return nil
		}),
	)

	result, err := poller.processIssueSequential(context.Background(), &Issue{Number: 1})

	if err != nil {
		t.Fatalf("processIssueSequential() error = %v", err)
	}
	if !legacyCalled {
		t.Error("legacy callback should be called")
	}
	if !result.Success {
		t.Error("result.Success should be true")
	}
	// No PR info from legacy callback
	if result.PRNumber != 0 {
		t.Errorf("PRNumber = %d, want 0 (legacy callback doesn't return PR)", result.PRNumber)
	}
}

func TestPoller_StartSequential_ProcessesOneAtATime(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "First", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 2, Title: "Second", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	processedOrder := []int{}
	var mu sync.Mutex

	poller, _ := NewPoller(client, "owner/repo", "pilot", 10*time.Millisecond,
		WithExecutionMode(ExecutionModeSequential),
		WithSequentialConfig(false, 10*time.Millisecond, 100*time.Millisecond), // No merge waiting
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			mu.Lock()
			processedOrder = append(processedOrder, issue.Number)
			mu.Unlock()
			return &IssueResult{Success: true}, nil
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go poller.Start(ctx)

	// Wait for processing
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should process oldest first (issue #1)
	if len(processedOrder) < 1 {
		t.Fatal("should have processed at least one issue")
	}
	if processedOrder[0] != 1 {
		t.Errorf("first processed issue = %d, want 1 (oldest)", processedOrder[0])
	}
}

func TestParseDependencies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []int
	}{
		{
			name: "depends on with colon",
			body: "This issue depends on: #123",
			want: []int{123},
		},
		{
			name: "depends on without colon",
			body: "Depends on #456",
			want: []int{456},
		},
		{
			name: "blocked by with colon",
			body: "Blocked by: #789",
			want: []int{789},
		},
		{
			name: "blocked by without colon",
			body: "blocked by #101",
			want: []int{101},
		},
		{
			name: "requires with colon",
			body: "Requires: #202",
			want: []int{202},
		},
		{
			name: "requires without colon",
			body: "requires #303",
			want: []int{303},
		},
		{
			name: "multiple dependencies",
			body: "Depends on: #1\nBlocked by: #2\nRequires: #3",
			want: []int{1, 2, 3},
		},
		{
			name: "duplicate dependencies",
			body: "Depends on: #100\nAlso depends on #100",
			want: []int{100},
		},
		{
			name: "markdown header format",
			body: "## Depends on: #555",
			want: []int{555},
		},
		{
			name: "case insensitive",
			body: "DEPENDS ON: #111\nBLOCKED BY: #222",
			want: []int{111, 222},
		},
		{
			name: "no dependencies",
			body: "Just a regular issue body with #123 mentioned but not as dependency",
			want: nil,
		},
		{
			name: "empty body",
			body: "",
			want: nil,
		},
		{
			name: "mixed content",
			body: "## Task\nImplement feature X\n\nDepends on: #42\n\nSee also #99 (not a dependency)",
			want: []int{42},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDependencies(tt.body)

			if len(got) != len(tt.want) {
				t.Errorf("ParseDependencies() returned %d deps, want %d", len(got), len(tt.want))
				t.Errorf("got: %v, want: %v", got, tt.want)
				return
			}

			// Check each dependency is present (order may vary due to regex matching)
			gotMap := make(map[int]bool)
			for _, d := range got {
				gotMap[d] = true
			}
			for _, w := range tt.want {
				if !gotMap[w] {
					t.Errorf("ParseDependencies() missing dependency #%d", w)
				}
			}
		})
	}
}

func TestPoller_HasPendingDependencies(t *testing.T) {
	tests := []struct {
		name      string
		issueBody string
		depState  string // "open" or "closed"
		want      bool
	}{
		{
			name:      "no dependencies",
			issueBody: "Regular issue body",
			depState:  "",
			want:      false,
		},
		{
			name:      "dependency is open",
			issueBody: "Depends on: #100",
			depState:  "open",
			want:      true,
		},
		{
			name:      "dependency is closed",
			issueBody: "Depends on: #100",
			depState:  "closed",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/repos/owner/repo/issues/100" {
					issue := &Issue{Number: 100, State: tt.depState}
					_ = json.NewEncoder(w).Encode(issue)
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

			issue := &Issue{Number: 1, Body: tt.issueBody}
			got := poller.hasPendingDependencies(context.Background(), issue)

			if got != tt.want {
				t.Errorf("hasPendingDependencies() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPoller_HasPendingDependencies_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue := &Issue{Number: 1, Body: "Depends on: #100"}
	got := poller.hasPendingDependencies(context.Background(), issue)

	// Should return true (has pending) when API fails - be safe and don't execute
	if !got {
		t.Error("hasPendingDependencies() should return true on API error")
	}
}

func TestPoller_FindOldestUnprocessedIssue_SkipsPendingDependencies(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Oldest with dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second no dep", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case "/repos/owner/repo/issues/100":
			// Dependency is still open
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: "open"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil")
	}
	// Should skip #1 (has open dependency) and return #2
	if issue.Number != 2 {
		t.Errorf("found issue #%d, want #2 (skips issue with open dependency)", issue.Number)
	}
}

func TestPoller_FindOldestUnprocessedIssue_PicksClosedDependency(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Oldest with closed dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second no dep", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case "/repos/owner/repo/issues/100":
			// Dependency is closed
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: "closed"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil")
	}
	// Should pick #1 because dependency is closed
	if issue.Number != 1 {
		t.Errorf("found issue #%d, want #1 (dependency is closed)", issue.Number)
	}
}

func TestPoller_FindOldestUnprocessedIssue_AllDepsOpen(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Has dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Also has dep", Body: "Blocked by: #101", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case "/repos/owner/repo/issues/100", "/repos/owner/repo/issues/101":
			// Both dependencies are open
			_ = json.NewEncoder(w).Encode(&Issue{Number: 100, State: "open"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	// Should return nil when all issues have open dependencies
	if issue != nil {
		t.Errorf("issue should be nil when all have open dependencies, got #%d", issue.Number)
	}
}

// GH-1355: Test recovery of orphaned in-progress issues
func TestPoller_RecoverOrphanedIssues(t *testing.T) {
	// Mock issues that have both pilot and pilot-in-progress labels
	orphanedIssues := []*Issue{
		{Number: 123, Title: "Orphaned Issue 1", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}},
		{Number: 456, Title: "Orphaned Issue 2", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}},
	}

	removedLabels := []int{}
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/repos/owner/repo/issues" {
				_ = json.NewEncoder(w).Encode(orphanedIssues)
			}
		case http.MethodDelete:
			// Track label removal calls
			switch r.URL.Path {
			case "/repos/owner/repo/issues/123/labels/pilot-in-progress":
				mu.Lock()
				removedLabels = append(removedLabels, 123)
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			case "/repos/owner/repo/issues/456/labels/pilot-in-progress":
				mu.Lock()
				removedLabels = append(removedLabels, 456)
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Call recovery directly
	poller.recoverOrphanedIssues(context.Background())

	mu.Lock()
	defer mu.Unlock()

	// Should have removed labels from both orphaned issues
	if len(removedLabels) != 2 {
		t.Errorf("expected 2 labels removed, got %d", len(removedLabels))
	}
}

// GH-1355: Test recovery handles empty result
func TestPoller_RecoverOrphanedIssues_NoOrphanedIssues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return empty array - no orphaned issues
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Should not panic or error with empty result
	poller.recoverOrphanedIssues(context.Background())
}

// GH-1355: Test recovery handles API error gracefully
func TestPoller_RecoverOrphanedIssues_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Should not panic - errors are logged but not returned
	poller.recoverOrphanedIssues(context.Background())
}

func TestGroupByOverlappingScope(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name           string
		issues         []*Issue
		wantGroups     int
		wantMaxGroupSz int
	}{
		{
			name:       "empty input",
			issues:     nil,
			wantGroups: 0,
		},
		{
			name: "single issue",
			issues: []*Issue{
				{Number: 1, Body: "Modify internal/comms/handler.go"},
			},
			wantGroups:     1,
			wantMaxGroupSz: 1,
		},
		{
			name: "three issues same directory form one group",
			issues: []*Issue{
				{Number: 1, Body: "Modify internal/comms/handler.go", CreatedAt: now.Add(-3 * time.Hour)},
				{Number: 2, Body: "Update internal/comms/router.go", CreatedAt: now.Add(-2 * time.Hour)},
				{Number: 3, Body: "Refactor internal/comms/types.go", CreatedAt: now.Add(-1 * time.Hour)},
			},
			wantGroups:     1,
			wantMaxGroupSz: 3,
		},
		{
			name: "three issues different directories form three groups",
			issues: []*Issue{
				{Number: 1, Body: "Modify internal/gateway/server.go", CreatedAt: now.Add(-3 * time.Hour)},
				{Number: 2, Body: "Update internal/executor/runner.go", CreatedAt: now.Add(-2 * time.Hour)},
				{Number: 3, Body: "Refactor internal/adapters/slack.go", CreatedAt: now.Add(-1 * time.Hour)},
			},
			wantGroups:     3,
			wantMaxGroupSz: 1,
		},
		{
			name: "mixed overlap: two overlap plus one independent",
			issues: []*Issue{
				{Number: 1, Body: "Modify internal/comms/handler.go", CreatedAt: now.Add(-3 * time.Hour)},
				{Number: 2, Body: "Update internal/comms/router.go", CreatedAt: now.Add(-2 * time.Hour)},
				{Number: 3, Body: "Refactor internal/gateway/server.go", CreatedAt: now.Add(-1 * time.Hour)},
			},
			wantGroups:     2,
			wantMaxGroupSz: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := groupByOverlappingScope(tt.issues)
			if len(groups) != tt.wantGroups {
				t.Errorf("got %d groups, want %d", len(groups), tt.wantGroups)
			}
			maxSz := 0
			for _, g := range groups {
				if len(g) > maxSz {
					maxSz = len(g)
				}
			}
			if tt.wantGroups > 0 && maxSz != tt.wantMaxGroupSz {
				t.Errorf("max group size = %d, want %d", maxSz, tt.wantMaxGroupSz)
			}
		})
	}
}

// GH-1806: 3 issues referencing internal/comms/ → only 1 dispatched
func TestPoller_OverlapGrouping_AllOverlap(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 3, Title: "Newest comms", Body: "Change internal/comms/types.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 1, Title: "Oldest comms", Body: "Change internal/comms/handler.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, Title: "Middle comms", Body: "Change internal/comms/router.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var dispatched []int
	var mu sync.Mutex
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			dispatched = append(dispatched, issue.Number)
			mu.Unlock()
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 1 {
		t.Fatalf("dispatched %d issues, want 1; got %v", len(dispatched), dispatched)
	}
	if dispatched[0] != 1 {
		t.Errorf("dispatched issue %d, want 1 (oldest)", dispatched[0])
	}
}

// GH-1806: 3 issues referencing different directories → all 3 dispatched
func TestPoller_OverlapGrouping_NoOverlap(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Gateway", Body: "Change internal/gateway/server.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, Title: "Executor", Body: "Change internal/executor/runner.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 3, Title: "Adapters", Body: "Change internal/adapters/slack.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&callCount); got != 3 {
		t.Errorf("dispatched %d issues, want 3", got)
	}
}

// GH-1806: Mixed overlap groups → correct subset dispatched
func TestPoller_OverlapGrouping_MixedGroups(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		// Group 1: internal/comms overlap (issues 1, 2)
		{Number: 1, Title: "Comms A", Body: "Change internal/comms/handler.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, Title: "Comms B", Body: "Change internal/comms/router.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		// Group 2: independent (issue 3)
		{Number: 3, Title: "Gateway", Body: "Change internal/gateway/server.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var dispatched []int
	var mu sync.Mutex
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			dispatched = append(dispatched, issue.Number)
			mu.Unlock()
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	if len(dispatched) != 2 {
		t.Fatalf("dispatched %d issues, want 2; got %v", len(dispatched), dispatched)
	}

	// Should dispatch issue 1 (oldest in comms group) and issue 3 (standalone)
	dispatchedSet := map[int]bool{}
	for _, n := range dispatched {
		dispatchedSet[n] = true
	}
	if !dispatchedSet[1] {
		t.Error("expected issue 1 (oldest in comms group) to be dispatched")
	}
	if !dispatchedSet[3] {
		t.Error("expected issue 3 (standalone gateway) to be dispatched")
	}
	if dispatchedSet[2] {
		t.Error("issue 2 should be deferred (overlaps with older issue 1)")
	}
}

// GH-1799: auto mode — non-overlapping issues dispatched in parallel
func TestPoller_AutoMode_NonOverlapping(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Gateway", Body: "Change internal/gateway/server.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, Title: "Executor", Body: "Change internal/executor/runner.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 3, Title: "Adapters", Body: "Change internal/adapters/slack.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionMode(ExecutionModeAuto),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// All 3 non-overlapping issues should be dispatched in parallel
	if got := atomic.LoadInt32(&callCount); got != 3 {
		t.Errorf("auto mode dispatched %d issues, want 3 (non-overlapping should all run)", got)
	}
}

// GH-1799: auto mode — overlapping issues dispatch only the oldest
func TestPoller_AutoMode_OverlappingDispatchesOldestOnly(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 3, Title: "Newest comms", Body: "Change internal/comms/types.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 1, Title: "Oldest comms", Body: "Change internal/comms/handler.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, Title: "Middle comms", Body: "Change internal/comms/router.go", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var dispatched []int
	var mu sync.Mutex
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionMode(ExecutionModeAuto),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			dispatched = append(dispatched, issue.Number)
			mu.Unlock()
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	// Only the oldest (issue 1) should be dispatched; 2 and 3 deferred
	if len(dispatched) != 1 {
		t.Fatalf("auto mode dispatched %d issues, want 1; got %v", len(dispatched), dispatched)
	}
	if dispatched[0] != 1 {
		t.Errorf("auto mode dispatched issue %d, want 1 (oldest)", dispatched[0])
	}
}
