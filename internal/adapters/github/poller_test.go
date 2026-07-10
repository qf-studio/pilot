package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/adapters/skipreason"
	"github.com/qf-studio/pilot/internal/testutil"
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

	// GH-2176: When the callback returns an error, the issue is unmarked so it
	// can be retried on the next poll cycle (after pilot-failed label is removed).
	if poller.IsProcessed(1) {
		t.Error("issue 1 should NOT be marked as processed after callback error (GH-2176: retry path)")
	}
	if poller.IsProcessed(2) {
		t.Error("issue 2 should NOT be marked as processed after callback error (GH-2176: retry path)")
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
	// Issue 1 has pilot-failed and is at max retries (pilot-failed-retry-exhausted,
	// GH-3715) — should be skipped. Issue 2 has only pilot so should be processed.
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}, {Name: "pilot-failed"}, {Name: LabelFailedRetryExhausted}}},
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

	// Only issue 2 should trigger callback (issue 1 at max retries)
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("callback called %d times, want 1 (issue at retry limit should be skipped)", got)
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
		WithRetryGracePeriod(0), // GH-2201: disable grace period for test
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
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0), // GH-2201: disable grace period for test
	)

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

// TestPoller_FindOldestUnprocessedIssue_ClosedIssueGate covers GH-4183: a
// closed issue is terminal and must never be re-armed for retry by the
// "status labels removed" path, regardless of whether it still carries a
// (non-blocking) label. Only an open, previously-processed issue should be
// picked back up.
func TestPoller_FindOldestUnprocessedIssue_ClosedIssueGate(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name         string
		state        string
		labels       []Label
		wantDispatch bool
	}{
		{
			name:         "closed issue with missing status label is skipped",
			state:        StateClosed,
			labels:       []Label{{Name: "pilot"}},
			wantDispatch: false,
		},
		{
			name:         "closed issue with present status label is skipped",
			state:        StateClosed,
			labels:       []Label{{Name: "pilot"}, {Name: "bug"}},
			wantDispatch: false,
		},
		{
			name:         "open issue with missing status label dispatches",
			state:        StateOpen,
			labels:       []Label{{Name: "pilot"}},
			wantDispatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := []*Issue{
				{Number: 1, Title: "Issue 1", State: tt.state, Labels: tt.labels, CreatedAt: now},
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(issues)
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithRetryGracePeriod(0), // GH-2201: disable grace period for test
			)

			// Simulate: issue was processed by a prior poll cycle.
			poller.markProcessed(1)

			issue, err := poller.findOldestUnprocessedIssue(context.Background())
			if err != nil {
				t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
			}

			if tt.wantDispatch {
				if issue == nil {
					t.Fatal("issue should not be nil - open issue should be re-armed for retry")
				}
				if poller.IsProcessed(1) {
					t.Error("issue #1 should no longer be marked as processed after re-arm")
				}
			} else {
				if issue != nil {
					t.Fatalf("issue should be nil - closed issue is terminal, got #%d", issue.Number)
				}
				if !poller.IsProcessed(1) {
					t.Error("issue #1 should remain marked as processed - closed issue must not be re-armed")
				}
			}
		})
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

// TestPoller_HasPendingDependencies covers the GH-3789 in-memory gate:
// hasPendingDependencies resolves Blocked-by/Depends-on references against
// the already-fetched candidate slice (fetchCandidates' result), never a
// per-blocker API call. A blocker present in that slice is open and
// blocking; absent (closed, unlabeled, or nonexistent) is not — fail-open.
func TestPoller_HasPendingDependencies(t *testing.T) {
	tests := []struct {
		name      string
		issueBody string
		fetched   []*Issue
		want      bool
	}{
		{
			name:      "no dependencies",
			issueBody: "Regular issue body",
			fetched:   nil,
			want:      false,
		},
		{
			name:      "dependency present in fetched candidates (open)",
			issueBody: "Depends on: #100",
			fetched:   []*Issue{{Number: 100}},
			want:      true,
		},
		{
			name:      "dependency absent from fetched candidates (closed/unlabeled/nonexistent)",
			issueBody: "Depends on: #100",
			fetched:   nil,
			want:      false,
		},
	}

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "http://example.invalid")
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{Number: 1, Body: tt.issueBody}
			got := poller.hasPendingDependencies(issue, tt.fetched)

			if got != tt.want {
				t.Errorf("hasPendingDependencies() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPoller_FindOldestUnprocessedIssue_SkipsPendingDependencies is the D6
// reproduction (GH-3789/GH-3759): issue #1's blocker #100 is open and
// present in the SAME fetched candidate list (list-based fixture, no
// per-issue httptest route) — #1 must be skipped and #2 dispatched instead,
// with no p.client.GetIssue call in the gating path.
func TestPoller_FindOldestUnprocessedIssue_SkipsPendingDependencies(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Oldest with dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second no dep", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
		// #100 is the open blocker — present in the same open+pilot-labeled
		// fetch (simulated here as still in-progress so it isn't itself
		// selected as the oldest dispatchable candidate).
		{Number: 100, Title: "Blocker still open", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}
		if r.URL.Path == "/repos/owner/repo/issues/100" || r.URL.Path == "/repos/owner/repo/issues/101" {
			t.Errorf("unexpected per-blocker API call to %s — gate must resolve in-memory (GH-3789)", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
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
	// Should skip #1 (blocker present in fetched candidates) and return #2
	if issue.Number != 2 {
		t.Errorf("found issue #%d, want #2 (skips issue with blocker present in fetched candidates)", issue.Number)
	}
}

// TestPoller_FindOldestUnprocessedIssue_PicksClosedDependency: blocker #100
// is absent from the fetched candidate list (closed/unlabeled/nonexistent —
// indistinguishable under the in-memory gate), so #1 proceeds (fail-open).
func TestPoller_FindOldestUnprocessedIssue_PicksClosedDependency(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Oldest with closed dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second no dep", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}
		w.WriteHeader(http.StatusNotFound)
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
	// Should pick #1 because dependency #100 is absent from the fetched candidates
	if issue.Number != 1 {
		t.Errorf("found issue #%d, want #1 (dependency absent from fetch)", issue.Number)
	}
}

// TestPoller_FindOldestUnprocessedIssue_AllDepsOpen: both blockers (#100,
// #101) are present in the fetched candidate list, so neither #1 nor #2
// can be dispatched this cycle.
func TestPoller_FindOldestUnprocessedIssue_AllDepsOpen(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Has dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Also has dep", Body: "Blocked by: #101", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 100, Title: "Blocker 1", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}, CreatedAt: now},
		{Number: 101, Title: "Blocker 2", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())

	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	// Should return nil when all issues have blockers present in the fetch
	if issue != nil {
		t.Errorf("issue should be nil when all have pending dependencies, got #%d", issue.Number)
	}
}

// TestPoller_FindOldestUnprocessedIssue_RecordsPendingDependencySkip closes
// the sequential-mode metrics gap noted in #3835: prior to GH-3789 the
// sequential path (findOldestUnprocessedIssue) skipped pending-dependency
// candidates without ever calling recordSkip, unlike the parallel path.
func TestPoller_FindOldestUnprocessedIssue_RecordsPendingDependencySkip(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 1, Title: "Oldest with dep", Body: "Depends on: #100", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "Second no dep", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 100, Title: "Blocker still open", Labels: []Label{{Name: "pilot"}, {Name: LabelInProgress}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	m := newFakePollerMetrics()
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithPollerMetrics(m))

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil || issue.Number != 2 {
		t.Fatalf("findOldestUnprocessedIssue() = %v, want #2", issue)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.skipped[skipreason.ReasonPendingDependency] != 1 {
		t.Errorf("skipped[%q] = %d, want 1", skipreason.ReasonPendingDependency, m.skipped[skipreason.ReasonPendingDependency])
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
		{
			// GH-3714: root-level files (package.json) share no directory,
			// so plain directory extraction would miss this collision.
			name: "two bodies both naming package.json group together",
			issues: []*Issue{
				{Number: 1, Body: "Add a package.json with the base dependencies", CreatedAt: now.Add(-2 * time.Hour)},
				{Number: 2, Body: "Update package.json to add the lint script", CreatedAt: now.Add(-1 * time.Hour)},
			},
			wantGroups:     1,
			wantMaxGroupSz: 2,
		},
		{
			name: "bodies naming disjoint subdirs still run parallel",
			issues: []*Issue{
				{Number: 1, Body: "Modify internal/gateway/server.go", CreatedAt: now.Add(-2 * time.Hour)},
				{Number: 2, Body: "Update internal/executor/runner.go", CreatedAt: now.Add(-1 * time.Hour)},
			},
			wantGroups:     2,
			wantMaxGroupSz: 1,
		},
		{
			// GH-3714: a scaffold/bootstrap issue naming 2+ root config files
			// (package.json + tsconfig.json) is globally overlapping — it
			// serializes with every other candidate, even ones referencing
			// unrelated subdirectories.
			name: "scaffold-flavored body groups with all candidates",
			issues: []*Issue{
				{Number: 1, Body: "Bootstrap the project: add package.json and tsconfig.json", CreatedAt: now.Add(-3 * time.Hour)},
				{Number: 2, Body: "Modify internal/gateway/server.go", CreatedAt: now.Add(-2 * time.Hour)},
				{Number: 3, Body: "Update internal/executor/runner.go", CreatedAt: now.Add(-1 * time.Hour)},
			},
			wantGroups:     1,
			wantMaxGroupSz: 3,
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

func TestPoller_HasMergedWork(t *testing.T) {
	tests := []struct {
		name           string
		searchResponse string
		wantSkip       bool
		wantLabeled    bool
	}{
		{
			name:           "merged PRs exist - skip and label",
			searchResponse: `{"total_count": 2}`,
			wantSkip:       true,
			wantLabeled:    true,
		},
		{
			name:           "no merged PRs - allow retry",
			searchResponse: `{"total_count": 0}`,
			wantSkip:       false,
			wantLabeled:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var labeled bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/search/issues":
					_, _ = w.Write([]byte(tt.searchResponse))
				case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/42/labels":
					labeled = true
					w.WriteHeader(http.StatusOK)
				default:
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

			issue := &Issue{Number: 42, Title: "Test issue"}
			got := poller.hasMergedWork(context.Background(), issue)

			if got != tt.wantSkip {
				t.Errorf("hasMergedWork() = %v, want %v", got, tt.wantSkip)
			}
			if labeled != tt.wantLabeled {
				t.Errorf("labeled = %v, want %v", labeled, tt.wantLabeled)
			}
			if tt.wantSkip && !poller.IsProcessed(42) {
				t.Error("issue should be marked as processed when skipped")
			}
		})
	}
}

func TestPoller_HasMergedWork_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message": "rate limit"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue := &Issue{Number: 42, Title: "Test issue"}
	got := poller.hasMergedWork(context.Background(), issue)

	// Should not block on API errors — allow the issue through
	if got {
		t.Error("hasMergedWork() should return false on API error")
	}
}

func TestPoller_CheckForNewIssues_SkipsRetryWithMergedPRs(t *testing.T) {
	issues := []*Issue{
		{Number: 1, Title: "GH-1 feature", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 1}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0), // GH-2201: disable grace period for test
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	// Pre-mark as processed (simulating previous failed attempt)
	poller.markProcessed(1)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Should NOT retry since merged PRs exist
	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("callback called %d times, want 0 (should skip issue with merged PRs)", got)
	}
}

func TestPoller_FindOldestUnprocessedIssue_SkipsRetryWithMergedPRs(t *testing.T) {
	issues := []*Issue{
		{
			Number:    1,
			Title:     "GH-1 feature",
			Labels:    []Label{{Name: "pilot"}},
			CreatedAt: time.Now().Add(-1 * time.Hour),
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 3}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionMode(ExecutionModeSequential),
		WithRetryGracePeriod(0), // GH-2201: disable grace period for test
	)

	// Pre-mark as processed (simulating previous failed attempt)
	poller.markProcessed(1)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue != nil {
		t.Errorf("findOldestUnprocessedIssue() returned issue %d, want nil (should skip issue with merged PRs)", issue.Number)
	}
}

func TestPoller_CheckForNewIssues_SkipsPullRequests(t *testing.T) {
	pr := &struct{}{}
	items := []*Issue{
		{Number: 1, Title: "Real issue", Labels: []Label{{Name: "pilot"}}},
		{Number: 2, Title: "A pull request", Labels: []Label{{Name: "pilot"}}, PullRequest: pr},
		{Number: 3, Title: "Another issue", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
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
		t.Fatalf("dispatched %d issues, want 2 (PRs should be skipped)", len(dispatched))
	}

	for _, num := range dispatched {
		if num == 2 {
			t.Errorf("PR (issue #2) should not have been dispatched")
		}
	}
}

func TestPoller_FindOldestUnprocessedIssue_SkipsPullRequests(t *testing.T) {
	pr := &struct{}{}
	now := time.Now()
	items := []*Issue{
		{Number: 10, Title: "A pull request", Labels: []Label{{Name: "pilot"}}, PullRequest: pr, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 20, Title: "Real issue", Labels: []Label{{Name: "pilot"}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("expected an issue, got nil")
	}
	if issue.Number != 20 {
		t.Errorf("got issue #%d, want #20 (PR #10 should be skipped)", issue.Number)
	}
}

// --- GH-2201: Retry grace period and task checker tests ---

// mockTaskChecker implements TaskChecker for testing
type mockTaskChecker struct {
	queued map[string]bool
}

func (m *mockTaskChecker) IsTaskQueued(taskID string) bool {
	return m.queued[taskID]
}

func TestPoller_SkipsRecentlyProcessed(t *testing.T) {
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
		WithRetryGracePeriod(10*time.Minute), // Long grace period
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	// Pre-mark as processed (just now — within grace period)
	poller.markProcessed(1)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Should NOT retry — still within grace period
	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("callback called %d times, want 0 (should skip recently processed issue)", got)
	}
	// Should still be in processed map
	if !poller.IsProcessed(1) {
		t.Error("issue should still be in processed map during grace period")
	}
}

func TestPoller_AllowsRetryAfterGrace(t *testing.T) {
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
		WithRetryGracePeriod(1*time.Millisecond), // Tiny grace period
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	// Pre-mark as processed, then wait for grace period to expire
	poller.markProcessed(1)
	time.Sleep(5 * time.Millisecond)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Should retry — grace period expired
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("callback called %d times, want 1 (should retry after grace period)", got)
	}
}

func TestPoller_SkipsQueuedTask(t *testing.T) {
	issues := []*Issue{
		{Number: 42, Title: "Issue 42", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	checker := &mockTaskChecker{queued: map[string]bool{"GH-42": true}}

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0), // No grace period — only task checker blocks
		WithTaskChecker(checker),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	// Pre-mark as processed
	poller.markProcessed(42)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Should NOT retry — task is still queued
	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("callback called %d times, want 0 (should skip queued task)", got)
	}
	// Should still be in processed map
	if !poller.IsProcessed(42) {
		t.Error("issue should still be in processed map when task is queued")
	}
}

func TestPoller_AllowsRetryCompletedTask(t *testing.T) {
	issues := []*Issue{
		{Number: 42, Title: "Issue 42", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	checker := &mockTaskChecker{queued: map[string]bool{}} // Not queued

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0), // No grace period
		WithTaskChecker(checker),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	// Pre-mark as processed
	poller.markProcessed(42)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// Should retry — task is not queued and grace period is 0
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("callback called %d times, want 1 (should retry completed task)", got)
	}
}

// newRetryGateIncidentServer serves the routes hit by the retry-gate checks
// (hasMergedWork, hasOpenPRAwaitingMerge, and the parallel-path fresh-label
// GetIssue refresh) with "nothing found" responses, and falls back to the
// issue list for everything else (including the GetIssue refresh, whose
// array-shaped response is tolerated as a soft failure — see
// checkForNewIssues' fresh-label-check error handling).
func newRetryGateIncidentServer(issue *Issue) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case "/repos/owner/repo/pulls":
			_, _ = w.Write([]byte(`[]`))
		default:
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
		}
	}))
}

// TestPoller_SequentialRetryBlock_TaskQueuedGate and
// TestPoller_ParallelRetryBlock_TaskQueuedGate reproduce the GH-4128 → PR
// #4133 incident timeline: an issue is marked processed at T with no
// pilot-in-progress/status labels and no PR yet, and the poller
// re-evaluates it at T+5m42s — past the 5m default retry grace period
// (GH-2201) but with the dispatched task's execution row (inserted by
// subtask 1, checked via IsTaskQueued) still running. Both the parallel
// retry block (poller.go:1394-1447) and the sequential retry block
// (poller.go:978-1028) must consult that running row before allowing a
// second dispatch of the same issue.
const retryGateIncidentIssueNumber = 4128

func retryGateIncidentIssue() *Issue {
	return &Issue{
		Number:    retryGateIncidentIssueNumber,
		State:     "open",
		Title:     "Incident repro issue",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
}

// markProcessedAtIncidentTimeline sets the processed timestamp so that
// time.Since(processedAt) reads as exactly the incident's observed
// mark-to-reevaluation gap: 5m42s, i.e. 42s past the 5m default grace period.
func markProcessedAtIncidentTimeline(p *Poller, number int) {
	p.mu.Lock()
	p.processed[number] = time.Now().Add(-(5*time.Minute + 42*time.Second))
	p.mu.Unlock()
}

func TestPoller_SequentialRetryBlock_TaskQueuedGate(t *testing.T) {
	tests := []struct {
		name        string
		taskQueued  bool
		wantSkipped bool
	}{
		{
			name:        "scenario_a_running_row_present_skips_with_task_queued",
			taskQueued:  true,
			wantSkipped: true,
		},
		{
			name:        "scenario_b_no_running_row_dispatches",
			taskQueued:  false,
			wantSkipped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := retryGateIncidentIssue()
			server := newRetryGateIncidentServer(issue)
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			taskID := fmt.Sprintf("GH-%d", retryGateIncidentIssueNumber)
			checker := &mockTaskChecker{queued: map[string]bool{taskID: tt.taskQueued}}

			var callCount int32
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithTaskChecker(checker),
				WithOnIssue(func(ctx context.Context, iss *Issue) error {
					atomic.AddInt32(&callCount, 1)
					return nil
				}),
			)

			markProcessedAtIncidentTimeline(poller, retryGateIncidentIssueNumber)

			got, err := poller.findOldestUnprocessedIssue(context.Background())
			if err != nil {
				t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
			}

			// Mirror what startSequential does with a non-nil candidate: dispatch it.
			if got != nil {
				if _, err := poller.processIssueSequential(context.Background(), got); err != nil {
					t.Fatalf("processIssueSequential() error = %v", err)
				}
			}

			if tt.wantSkipped {
				if got != nil {
					t.Errorf("findOldestUnprocessedIssue() = issue #%d, want nil (running task row must gate re-dispatch)", got.Number)
				}
				if got := atomic.LoadInt32(&callCount); got != 0 {
					t.Errorf("dispatch called %d times, want 0 (task still queued)", got)
				}
				if !poller.IsProcessed(retryGateIncidentIssueNumber) {
					t.Error("issue should remain in processed map while task is queued")
				}
			} else {
				if got == nil {
					t.Fatal("findOldestUnprocessedIssue() = nil, want issue #4128 (no running row should allow dispatch)")
				}
				if got.Number != retryGateIncidentIssueNumber {
					t.Errorf("findOldestUnprocessedIssue() = issue #%d, want #%d", got.Number, retryGateIncidentIssueNumber)
				}
				if got := atomic.LoadInt32(&callCount); got != 1 {
					t.Errorf("dispatch called %d times, want 1 (no running row should allow re-dispatch)", got)
				}
			}
		})
	}
}

func TestPoller_ParallelRetryBlock_TaskQueuedGate(t *testing.T) {
	tests := []struct {
		name         string
		taskQueued   bool
		wantDispatch int32
	}{
		{
			name:         "scenario_a_running_row_present_skips_with_task_queued",
			taskQueued:   true,
			wantDispatch: 0,
		},
		{
			name:         "scenario_b_no_running_row_dispatches",
			taskQueued:   false,
			wantDispatch: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := retryGateIncidentIssue()
			server := newRetryGateIncidentServer(issue)
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
			taskID := fmt.Sprintf("GH-%d", retryGateIncidentIssueNumber)
			checker := &mockTaskChecker{queued: map[string]bool{taskID: tt.taskQueued}}

			var callCount int32
			poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
				WithTaskChecker(checker),
				WithOnIssue(func(ctx context.Context, iss *Issue) error {
					atomic.AddInt32(&callCount, 1)
					return nil
				}),
			)

			markProcessedAtIncidentTimeline(poller, retryGateIncidentIssueNumber)

			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			if got := atomic.LoadInt32(&callCount); got != tt.wantDispatch {
				t.Errorf("checkForNewIssues dispatched %d times, want %d", got, tt.wantDispatch)
			}

			if tt.taskQueued {
				if !poller.IsProcessed(retryGateIncidentIssueNumber) {
					t.Error("issue should remain in processed map while task is queued")
				}
			}
		})
	}
}

func TestPoller_ProcessedMapStoresTimestamps(t *testing.T) {
	client := NewClient(testutil.FakeGitHubToken)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	before := time.Now()
	poller.markProcessed(1)
	after := time.Now()

	poller.mu.RLock()
	ts, ok := poller.processed[1]
	poller.mu.RUnlock()

	if !ok {
		t.Fatal("issue 1 should be in processed map")
	}
	if ts.Before(before) || ts.After(after) {
		t.Errorf("processed timestamp %v not between %v and %v", ts, before, after)
	}

	// Verify IsProcessed still works
	if !poller.IsProcessed(1) {
		t.Error("IsProcessed should return true")
	}
	if poller.IsProcessed(2) {
		t.Error("IsProcessed should return false for unprocessed issue")
	}

	// Verify Reset clears timestamps
	poller.Reset()
	if poller.IsProcessed(1) {
		t.Error("IsProcessed should return false after Reset")
	}
}

func TestPoller_AutoRetryFailedIssue_FirstFailure(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	var labelRemoved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Handle label removal
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/42/labels/"+LabelFailed {
			labelRemoved.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Handle search (hasMergedWork check) — no merged PRs
		if r.URL.Path == "/search/issues" {
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		// Handle list issues
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0),
	)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil — pilot-failed issue should be retried on first failure")
	}
	if issue.Number != 42 {
		t.Errorf("got issue #%d, want #42", issue.Number)
	}
	if !labelRemoved.Load() {
		t.Error("pilot-failed label should have been removed")
	}

	// Verify retry count incremented
	poller.mu.RLock()
	retries := poller.failedRetryCount[42]
	poller.mu.RUnlock()
	if retries != 1 {
		t.Errorf("retry count = %d, want 1", retries)
	}
}

// GH-3715: retry limit is now enforced via the pilot-failed-retry-exhausted
// label (persisted across restarts) rather than the in-memory failedRetryCount
// map. An issue already carrying the exhausted label must be skipped.
func TestPoller_AutoRetryFailedIssue_RetryLimitReached(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}, {Name: LabelFailedRetryExhausted}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available issue", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
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
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43 (should skip #42 at retry limit)", issue.Number)
	}
}

func TestPoller_AutoRetryFailedIssue_SkipsDoneIssues(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		// Has both pilot-failed AND pilot-done — should NOT be retried
		{Number: 42, State: "open", Title: "Done+Failed", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}, {Name: LabelDone}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
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
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43 (should skip #42 with pilot-done)", issue.Number)
	}
}

func TestPoller_AutoRetryFailedIssue_SkipsClosedIssues(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		// Closed issue with stale pilot-failed label — should NOT be retried (GH-2252)
		{Number: 42, State: "closed", Title: "Closed+Failed", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
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
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43 (should skip closed #42 with pilot-failed)", issue.Number)
	}
}

func TestPoller_AutoRetryFailedIssue_ParallelMode(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	var labelRemoved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/42/labels/"+LabelFailed {
			labelRemoved.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/search/issues" {
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var processedIssues []*Issue
	var mu sync.Mutex

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			processedIssues = append(processedIssues, issue)
			mu.Unlock()
			return nil
		}),
		WithRetryGracePeriod(0),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := len(processedIssues)
	mu.Unlock()

	if got != 1 {
		t.Errorf("processed %d issues, want 1", got)
	}
	if !labelRemoved.Load() {
		t.Error("pilot-failed label should have been removed in parallel mode")
	}
}

// GH-3715: retry limit is now enforced via the pilot-failed-retry-exhausted
// label rather than the in-memory failedRetryCount map, so it survives restarts.
func TestPoller_AutoRetryFailedIssue_ParallelMode_LimitReached(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Stuck issue", Labels: []Label{{Name: "pilot"}, {Name: LabelFailed}, {Name: LabelFailedRetryExhausted}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var processedCount atomic.Int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			processedCount.Add(1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if processedCount.Load() != 0 {
		t.Errorf("processed %d issues, want 0 (should skip at retry limit)", processedCount.Load())
	}
}

// GH-2768: Skip issues with pilot-needs-clarification label

func TestPoller_SkipsNeedsClarification(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		// Has pilot-needs-clarification — should be skipped until label is removed
		{Number: 42, State: "open", Title: "Declined issue", Labels: []Label{{Name: "pilot"}, {Name: LabelNeedsClarification}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available issue", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Test findOldestUnprocessedIssue skips #42
	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("findOldestUnprocessedIssue: got issue #%d, want #43 (should skip #42 with pilot-needs-clarification)", issue.Number)
	}

	// Test checkForNewIssues (parallel mode) also skips #42
	var processedIssues []*Issue
	var mu sync.Mutex
	poller2, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			mu.Lock()
			processedIssues = append(processedIssues, iss)
			mu.Unlock()
			return nil
		}),
	)
	poller2.checkForNewIssues(context.Background())
	poller2.WaitForActive()

	mu.Lock()
	got := processedIssues
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("checkForNewIssues: dispatched %d issues, want 1", len(got))
	}
	if got[0].Number != 43 {
		t.Errorf("checkForNewIssues: dispatched issue #%d, want #43", got[0].Number)
	}
}

// GH-2276: Auto-retry issues with pilot-retry-ready (PR closed without merge)

func TestPoller_AutoRetryRetryReadyIssue_FirstRetry(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready issue", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	var labelRemoved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Handle label removal
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/42/labels/"+LabelRetryReady {
			labelRemoved.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		// Handle search (hasMergedWork check) — no merged PRs
		if r.URL.Path == "/search/issues" {
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		// Handle list issues
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0),
	)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil — pilot-retry-ready issue should be retried on first attempt")
	}
	if issue.Number != 42 {
		t.Errorf("got issue #%d, want #42", issue.Number)
	}
	if !labelRemoved.Load() {
		t.Error("pilot-retry-ready label should have been removed")
	}

	// Verify retry count incremented
	poller.mu.RLock()
	retries := poller.retryReadyCount[42]
	poller.mu.RUnlock()
	if retries != 1 {
		t.Errorf("retry count = %d, want 1", retries)
	}
}

func TestPoller_AutoRetryRetryReadyIssue_RetryLimitReached(t *testing.T) {
	now := time.Now()
	// GH-2432: Retry state is now persisted via labels — issue already at
	// pilot-retry-2 means the next escalation goes to pilot-retry-exhausted
	// (no dispatch).
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready issue", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}, {Name: LabelRetry2}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available issue", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithMaxRetryReadyRetries(3),
	)

	issue, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue() error = %v", err)
	}
	if issue == nil {
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43 (should skip #42 at retry limit)", issue.Number)
	}
}

func TestPoller_AutoRetryRetryReadyIssue_SkipsDoneIssues(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		// Has both pilot-retry-ready AND pilot-done — should NOT be retried
		{Number: 42, State: "open", Title: "Done+RetryReady", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}, {Name: LabelDone}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
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
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43 (should skip #42 with pilot-done)", issue.Number)
	}
}

func TestPoller_AutoRetryRetryReadyIssue_SkipsClosedIssues(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		// Closed issue with pilot-retry-ready label — should NOT be retried
		{Number: 42, State: "closed", Title: "Closed+RetryReady", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 43, State: "open", Title: "Available", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
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
		t.Fatal("issue should not be nil — #43 should be picked")
	}
	if issue.Number != 43 {
		t.Errorf("got issue #%d, want #43 (should skip closed #42 with pilot-retry-ready)", issue.Number)
	}
}

func TestPoller_AutoRetryRetryReadyIssue_ParallelMode(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready issue", Labels: []Label{{Name: "pilot"}, {Name: LabelRetryReady}}, CreatedAt: now.Add(-1 * time.Hour)},
	}

	var labelRemoved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/42/labels/"+LabelRetryReady {
			labelRemoved.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/search/issues" {
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var processedIssues []*Issue
	var mu sync.Mutex

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			processedIssues = append(processedIssues, issue)
			mu.Unlock()
			return nil
		}),
		WithRetryGracePeriod(0),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := len(processedIssues)
	mu.Unlock()

	if got != 1 {
		t.Errorf("processed %d issues, want 1", got)
	}
	if !labelRemoved.Load() {
		t.Error("pilot-retry-ready label should have been removed in parallel mode")
	}
}

func TestPoller_WithMaxRetryReadyRetries(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "http://localhost")
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithMaxRetryReadyRetries(5),
	)
	if poller.maxRetryReadyRetries != 5 {
		t.Errorf("maxRetryReadyRetries = %d, want 5", poller.maxRetryReadyRetries)
	}

	// Negative values should be clamped to 0
	poller2, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithMaxRetryReadyRetries(-1),
	)
	if poller2.maxRetryReadyRetries != 0 {
		t.Errorf("maxRetryReadyRetries = %d, want 0 (clamped from -1)", poller2.maxRetryReadyRetries)
	}
}

// mockExecutionChecker implements ExecutionChecker for testing.
type mockExecutionChecker struct {
	completed   map[string]bool // key: "taskID:projectPath"
	invalidated []string        // keys passed to InvalidateCompletion, in order
}

func (m *mockExecutionChecker) HasCompletedExecution(taskID, projectPath string) (bool, error) {
	return m.completed[taskID+":"+projectPath], nil
}

func (m *mockExecutionChecker) InvalidateCompletion(taskID, projectPath string) error {
	key := taskID + ":" + projectPath
	m.invalidated = append(m.invalidated, key)
	delete(m.completed, key)
	return nil
}

func TestPoller_SkipsCompletedExecution(t *testing.T) {
	// GH-2242: Issue is open, no pilot-done label, but has completed execution — should NOT dispatch
	issues := []*Issue{
		{Number: 42, Title: "Issue 42", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/issues") {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	execChecker := &mockExecutionChecker{
		completed: map[string]bool{
			"GH-42:/project": true,
		},
	}

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionChecker(execChecker, "/project"),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("callback called %d times, want 0 (completed execution should skip dispatch)", got)
	}

	// Should be marked as processed
	if !poller.IsProcessed(42) {
		t.Error("completed issue should be marked as processed")
	}
}

func TestPoller_DispatchesWhenNoCompletedExecution(t *testing.T) {
	// GH-2242: Issue has no completed execution — should dispatch normally
	issues := []*Issue{
		{Number: 99, Title: "Issue 99", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/issues") {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	execChecker := &mockExecutionChecker{
		completed: map[string]bool{}, // No completed executions
	}

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionChecker(execChecker, "/project"),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("callback called %d times, want 1 (should dispatch when no completed execution)", got)
	}
}

// GH-3418: pilot-retry-ready re-dispatch must invalidate any stale completed
// execution row, otherwise HasCompletedExecution silently no-ops the re-dispatch.
func TestPoller_RetryReady_InvalidatesCompletedExecution(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready with completed row",
			Labels:    []Label{{Name: "pilot"}, {Name: LabelRetryReady}},
			CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/search/issues" {
			_, _ = w.Write([]byte(`{"total_count": 0}`))
			return
		}
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// Seed: issue has a prior completed execution row.
	execChecker := &mockExecutionChecker{
		completed: map[string]bool{
			"GH-42:/project": true,
		},
	}

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0),
		WithExecutionChecker(execChecker, "/project"),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	// InvalidateCompletion must have been called for the re-dispatched issue.
	if len(execChecker.invalidated) == 0 {
		t.Fatal("InvalidateCompletion was not called — stale completed row would block re-dispatch")
	}
	if execChecker.invalidated[0] != "GH-42:/project" {
		t.Errorf("InvalidateCompletion called with %q, want %q", execChecker.invalidated[0], "GH-42:/project")
	}

	// After invalidation the completed map is empty, so dispatch must proceed.
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("OnIssue called %d times, want 1 (retry-ready should re-dispatch after invalidation)", got)
	}
}

// GH-3992/GH-4021: pilot-retry-ready must not re-dispatch while a PR from a
// later, successful run is still open awaiting merge. hasMergedWork's DB
// completed-execution fallback is intentionally skipped under this label
// (any completed row is assumed stale from the close-without-merge event
// that set the label), so an open PR was the only reliable signal left —
// and nothing checked for it before this fix. In the incident, the
// auto-retry fired 5 minutes after a successful retry completed but 2
// minutes before its PR actually merged, dispatching a redundant third run
// whose orphan-row cleanup later raced the waiter into a false task_failed
// alert.
func TestPoller_RetryReady_SkipsWhenOpenPRAwaitingMerge(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 42, State: "open", Title: "Retry-ready with PR still open",
			Labels:    []Label{{Name: "pilot"}, {Name: LabelRetryReady}},
			CreatedAt: now.Add(-1 * time.Hour)},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case r.URL.Path == "/repos/owner/repo/pulls" && r.URL.Query().Get("state") == "closed":
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/repos/owner/repo/pulls" && r.URL.Query().Get("state") == "open":
			_, _ = w.Write([]byte(`[{"number": 4018, "state": "open", "head": {"ref": "pilot/GH-42"}}]`))
		default:
			_ = json.NewEncoder(w).Encode(issues)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(0),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&callCount, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("OnIssue called %d times, want 0 (retry-ready must skip re-dispatch while its PR is open awaiting merge)", got)
	}
}

// GH-2341: Search API may lag up to ~30s after a merge. hasMergedWork must
// supplement with a direct REST PR lookup by branch to catch that window.
func TestPoller_HasMergedWork_SearchLag_BranchFallback(t *testing.T) {
	var searchCalls, pullsCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/issues":
			atomic.AddInt32(&searchCalls, 1)
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case "/repos/owner/repo/pulls":
			atomic.AddInt32(&pullsCalls, 1)
			head := r.URL.Query().Get("head")
			if head != "owner:pilot/GH-42" {
				t.Errorf("unexpected head filter: %q", head)
			}
			_, _ = w.Write([]byte(`[{"number": 100, "merged_at": "2026-04-17T14:01:40Z", "head": {"ref": "pilot/GH-42"}}]`))
		case "/repos/owner/repo/issues/42/labels":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue := &Issue{Number: 42, Title: "Test issue"}
	got := poller.hasMergedWork(context.Background(), issue)

	if !got {
		t.Error("hasMergedWork() should return true when branch has merged PR (even with Search API empty)")
	}
	if atomic.LoadInt32(&pullsCalls) == 0 {
		t.Error("expected branch REST lookup to be called when Search API returned empty")
	}
	_ = searchCalls
}

// GH-2341: If neither Search API nor branch REST find a merged PR, allow retry.
func TestPoller_HasMergedWork_NoMerges_NoFallbackBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case "/repos/owner/repo/pulls":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	issue := &Issue{Number: 42, Title: "Test issue"}
	if poller.hasMergedWork(context.Background(), issue) {
		t.Error("hasMergedWork() should return false when no merged PRs exist on branch")
	}
}

// GH-3420 / TASK-359 Layer 3b: Search API empty + branch REST empty + completed DB row → true.
func TestPoller_HasMergedWork_DBFallback_CompletedRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case "/repos/owner/repo/pulls":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/owner/repo/issues/42/labels":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	execChecker := &mockExecutionChecker{
		completed: map[string]bool{
			"GH-42:/project": true,
		},
	}
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionChecker(execChecker, "/project"),
	)

	issue := &Issue{Number: 42, Title: "Test issue"}
	if !poller.hasMergedWork(context.Background(), issue) {
		t.Error("hasMergedWork() should return true when DB has completed row (even with Search + branch empty)")
	}
}

// GH-3420: Search empty + branch empty + no DB row → false (no regression).
func TestPoller_HasMergedWork_DBFallback_NoRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case "/repos/owner/repo/pulls":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	execChecker := &mockExecutionChecker{
		completed: map[string]bool{}, // no rows
	}
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionChecker(execChecker, "/project"),
	)

	issue := &Issue{Number: 42, Title: "Test issue"}
	if poller.hasMergedWork(context.Background(), issue) {
		t.Error("hasMergedWork() should return false when Search, branch, and DB all return empty")
	}
}

// GH-2341: Fresh label fetch before dispatch blocks re-dispatch when the
// ListIssues snapshot is stale and pilot-done was added in between.
func TestPoller_CheckForNewIssues_StaleSnapshot_RefreshesLabels(t *testing.T) {
	// Snapshot returned by ListIssues has NO pilot-done label.
	staleIssues := []*Issue{
		{Number: 42, Title: "GH-42 feature", State: "open", Labels: []Label{{Name: "pilot"}}},
	}
	// Fresh GetIssue response includes pilot-done.
	freshIssue := &Issue{
		Number: 42, Title: "GH-42 feature", State: "open",
		Labels: []Label{{Name: "pilot"}, {Name: LabelDone}},
	}

	var dispatches int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		switch r.URL.Path {
		case "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(staleIssues)
		case "/repos/owner/repo/issues/42":
			_ = json.NewEncoder(w).Encode(freshIssue)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatches); got != 0 {
		t.Errorf("dispatched %d times, want 0 (fresh GetIssue shows pilot-done)", got)
	}
}

// GH-2402: Sub-issues whose parent epic already shipped must be auto-closed
// with pilot-superseded instead of dispatched.
func TestPoller_CheckForNewIssues_SkipsSupersededByParent(t *testing.T) {
	subIssue := &Issue{
		Number: 200,
		Title:  "Sub-issue under shipped epic",
		Body:   "Parent: GH-100\n\nSome work.",
		State:  "open",
		Labels: []Label{{Name: "pilot"}},
	}
	parent := &Issue{
		Number: 100,
		Title:  "Epic",
		State:  "closed",
		Labels: []Label{{Name: "pilot"}, {Name: LabelDone}},
	}

	var (
		mu               sync.Mutex
		gotComment       string
		gotLabels        []string
		closedIssueState string
		dispatches       int32
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{subIssue})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/100":
			_ = json.NewEncoder(w).Encode(parent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/200":
			_ = json.NewEncoder(w).Encode(subIssue)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls" &&
			r.URL.Query().Get("state") == "closed":
			// Wave 2: supersession now requires positive evidence the child's own
			// slice shipped — give it a merged PR on pilot/GH-200.
			_ = json.NewEncoder(w).Encode([]*PullRequest{{
				Number: 900,
				State:  "closed",
				Merged: true,
				Head:   PRRef{Ref: "pilot/GH-200"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/200/comments":
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			gotComment = payload.Body
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":1}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/200/labels":
			var payload struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			gotLabels = append(gotLabels, payload.Labels...)
			mu.Unlock()
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/200":
			var payload struct {
				State string `json:"state"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			closedIssueState = payload.State
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatches); got != 0 {
		t.Errorf("dispatched %d times, want 0 (sub-issue should be auto-closed)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotComment, "parent epic #100") {
		t.Errorf("comment missing parent reference: %q", gotComment)
	}
	hasSuperseded := false
	for _, l := range gotLabels {
		if l == LabelSuperseded {
			hasSuperseded = true
			break
		}
	}
	if !hasSuperseded {
		t.Errorf("expected pilot-superseded label, got %v", gotLabels)
	}
	if closedIssueState != "closed" {
		t.Errorf("expected sub-issue closed, got state=%q", closedIssueState)
	}
}

// GH-2402: Sub-issues whose parent is closed but NOT marked done should NOT
// be auto-closed (e.g. user manually closed the epic before Pilot finished).
func TestPoller_CheckForNewIssues_DoesNotSupersedeWhenParentNotDone(t *testing.T) {
	subIssue := &Issue{
		Number: 201,
		Title:  "Sub-issue",
		Body:   "Parent: GH-101\n",
		State:  "open",
		Labels: []Label{{Name: "pilot"}},
	}
	parent := &Issue{
		Number: 101,
		Title:  "Epic",
		State:  "closed",
		Labels: []Label{{Name: "pilot"}}, // No LabelDone
	}

	var dispatches int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{subIssue})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/101":
			_ = json.NewEncoder(w).Encode(parent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/201":
			_ = json.NewEncoder(w).Encode(subIssue)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatches); got != 1 {
		t.Errorf("dispatched %d times, want 1 (parent closed but not done — should dispatch)", got)
	}
}

// GH-3513: A sub-issue with an OPEN pilot/GH-N PR must never be superseded,
// even when the parent is closed + pilot-done — the open PR is direct evidence
// its implementation is still in flight (the parent close may be premature).
func TestPoller_CheckForNewIssues_DoesNotSupersedeWithOpenPR(t *testing.T) {
	subIssue := &Issue{
		Number: 202,
		Title:  "Sub-issue with live PR",
		Body:   "Parent: GH-102\n\nSome work.",
		State:  "open",
		Labels: []Label{{Name: "pilot"}},
	}
	parent := &Issue{
		Number: 102,
		Title:  "Epic",
		State:  "closed",
		Labels: []Label{{Name: "pilot"}, {Name: LabelDone}},
	}
	openPR := &PullRequest{
		Number: 900,
		State:  "open",
		Head:   PRRef{Ref: "pilot/GH-202"},
	}

	var (
		dispatches  int32
		closedIssue int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{subIssue})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/102":
			_ = json.NewEncoder(w).Encode(parent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/202":
			_ = json.NewEncoder(w).Encode(subIssue)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls":
			_ = json.NewEncoder(w).Encode([]*PullRequest{openPR})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/202":
			atomic.AddInt32(&closedIssue, 1)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&closedIssue); got != 0 {
		t.Errorf("sub-issue was closed %d times, want 0 (open PR must veto supersession)", got)
	}
	if got := atomic.LoadInt32(&dispatches); got != 1 {
		t.Errorf("dispatched %d times, want 1 (open PR means work in flight — fall through to dispatch)", got)
	}
}

// GH-2402: Issues with pilot-blocked must be skipped — no dispatch and no retry
// until the user removes the label.
func TestPoller_CheckForNewIssues_SkipsBlockedIssues(t *testing.T) {
	blocked := &Issue{
		Number: 300,
		Title:  "deterministic failure",
		State:  "open",
		Labels: []Label{{Name: "pilot"}, {Name: LabelBlocked}},
	}

	var dispatches int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{blocked})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatches); got != 0 {
		t.Errorf("dispatched %d times, want 0 (pilot-blocked must skip)", got)
	}
}

// --- GH-3252: WithBoardSync write-back tests ---

// boardSyncTestServer creates an httptest.Server that handles the full set of calls
// made during a board-sync dispatch cycle:
//   - GET /repos/owner/repo/issues   → returns issues
//   - GET /repos/owner/repo/issues/N → returns freshIssue (used for label refresh and GetIssueNodeID fallback)
//   - POST /graphql                  → serves ProjectBoardSync GraphQL; counts mutations via mutationCount
func boardSyncTestServer(t *testing.T, issues []*Issue, freshIssue *Issue, mutationCount *atomic.Int32, capturedNodeID *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode(issues)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/1":
			_ = json.NewEncoder(w).Encode(freshIssue)
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var req GraphQLRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode graphql request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var resp string
			switch {
			case strings.Contains(req.Query, "organization"):
				resp = `{"data":{"organization":{"projectV2":{"id":"PVT_org1"}}}}`
			case strings.Contains(req.Query, "field(name:"):
				resp = `{"data":{"node":{"field":{"id":"PVTSSF_f1","options":[{"id":"OPT_inprog","name":"In Progress"},{"id":"OPT_done","name":"Done"}]}}}}`
			case strings.Contains(req.Query, "projectItems"):
				if capturedNodeID != nil {
					if id, ok := req.Variables["issueID"].(string); ok {
						*capturedNodeID = id
					}
				}
				resp = `{"data":{"node":{"projectItems":{"nodes":[{"id":"PVTI_item1","project":{"id":"PVT_org1"}}]}}}}`
			case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
				mutationCount.Add(1)
				resp = `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PVTI_item1"}}}}`
			default:
				t.Errorf("unexpected graphql query: %s", req.Query)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
}

// newBoardSyncForTest creates a ProjectBoardSync backed by the given client.
func newBoardSyncForTest(client *Client) *ProjectBoardSync {
	return NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "owner")
}

func TestPollerBoardSync_PickupTriggersExactlyOneCall(t *testing.T) {
	issue := &Issue{
		Number: 1, Title: "feat: board test", NodeID: "I_node1",
		Labels: []Label{{Name: "pilot"}},
	}
	var mutCount atomic.Int32
	server := boardSyncTestServer(t, []*Issue{issue}, issue, &mutCount, nil)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bs := newBoardSyncForTest(client)

	var dispatched int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithBoardSync(bs, "In Progress"),
		WithOnIssueWithResult(func(ctx context.Context, i *Issue) (*IssueResult, error) {
			atomic.AddInt32(&dispatched, 1)
			return &IssueResult{Success: true, PRNumber: 1, PRURL: "https://github.com/owner/repo/pull/1"}, nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatched); got != 1 {
		t.Fatalf("handler called %d times, want 1", got)
	}
	if got := mutCount.Load(); got != 1 {
		t.Errorf("UpdateProjectItemStatus called %d times, want exactly 1", got)
	}
}

func TestPollerBoardSync_NilBoardSync(t *testing.T) {
	issue := &Issue{
		Number: 1, Title: "no board", NodeID: "I_node1",
		Labels: []Label{{Name: "pilot"}},
	}
	var mutCount atomic.Int32
	server := boardSyncTestServer(t, []*Issue{issue}, issue, &mutCount, nil)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	// WithBoardSync not called — boardSync remains nil.
	var dispatched int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssueWithResult(func(ctx context.Context, i *Issue) (*IssueResult, error) {
			atomic.AddInt32(&dispatched, 1)
			return &IssueResult{Success: true, PRNumber: 1}, nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatched); got != 1 {
		t.Fatalf("handler called %d times, want 1", got)
	}
	if got := mutCount.Load(); got != 0 {
		t.Errorf("mutation called %d times, want 0 (boardSync is nil)", got)
	}
}

func TestPollerBoardSync_EmptyInProgressStatus(t *testing.T) {
	issue := &Issue{
		Number: 1, Title: "no status", NodeID: "I_node1",
		Labels: []Label{{Name: "pilot"}},
	}
	var mutCount atomic.Int32
	server := boardSyncTestServer(t, []*Issue{issue}, issue, &mutCount, nil)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bs := newBoardSyncForTest(client)

	var dispatched int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithBoardSync(bs, ""), // empty status — must be no-op
		WithOnIssueWithResult(func(ctx context.Context, i *Issue) (*IssueResult, error) {
			atomic.AddInt32(&dispatched, 1)
			return &IssueResult{Success: true, PRNumber: 1}, nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&dispatched); got != 1 {
		t.Fatalf("handler called %d times, want 1", got)
	}
	if got := mutCount.Load(); got != 0 {
		t.Errorf("mutation called %d times, want 0 (inProgressStatus is empty)", got)
	}
}

func TestPollerBoardSync_NodeIDFromIssue(t *testing.T) {
	// Issue already has NodeID — GetIssueNodeID fallback must NOT be called.
	issue := &Issue{
		Number: 1, Title: "has node id", NodeID: "I_direct_node",
		Labels: []Label{{Name: "pilot"}},
	}
	var capturedNodeID string
	var mutCount atomic.Int32
	server := boardSyncTestServer(t, []*Issue{issue}, issue, &mutCount, &capturedNodeID)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bs := newBoardSyncForTest(client)

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithBoardSync(bs, "In Progress"),
		WithOnIssueWithResult(func(ctx context.Context, i *Issue) (*IssueResult, error) {
			return &IssueResult{Success: true, PRNumber: 1}, nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if mutCount.Load() != 1 {
		t.Fatalf("mutation called %d times, want 1", mutCount.Load())
	}
	if capturedNodeID != "I_direct_node" {
		t.Errorf("GraphQL used nodeID %q, want %q (should use issue.NodeID directly)", capturedNodeID, "I_direct_node")
	}
}

func TestPollerBoardSync_NodeIDFallback(t *testing.T) {
	// Issue returned by ListIssues has empty NodeID — must fall back to GetIssueNodeID.
	issueNoNodeID := &Issue{
		Number: 1, Title: "no node id", NodeID: "",
		Labels: []Label{{Name: "pilot"}},
	}
	// GetIssueNodeID and fresh-label-check both use GET /repos/.../issues/1.
	// Return the same issue but WITH a node_id so GetIssueNodeID can resolve it.
	freshIssueWithNodeID := &Issue{
		Number: 1, Title: "no node id", NodeID: "I_fallback_node",
		Labels: []Label{{Name: "pilot"}},
	}
	var capturedNodeID string
	var mutCount atomic.Int32
	server := boardSyncTestServer(t, []*Issue{issueNoNodeID}, freshIssueWithNodeID, &mutCount, &capturedNodeID)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bs := newBoardSyncForTest(client)

	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithBoardSync(bs, "In Progress"),
		WithOnIssueWithResult(func(ctx context.Context, i *Issue) (*IssueResult, error) {
			return &IssueResult{Success: true, PRNumber: 1}, nil
		}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if mutCount.Load() != 1 {
		t.Fatalf("mutation called %d times, want 1", mutCount.Load())
	}
	if capturedNodeID != "I_fallback_node" {
		t.Errorf("GraphQL used nodeID %q, want %q (should use GetIssueNodeID fallback)", capturedNodeID, "I_fallback_node")
	}
}

// TestPoller_MergeDoneWindow_MarkProcessedAfterGrace verifies that calling
// MarkProcessed (as the onIssueDone callback does) re-enters the grace window
// and prevents phantom re-dispatch during the merge→pilot-done label lag.
// GH-3271 / TASK-321 PR-4.
func TestPoller_MergeDoneWindow_MarkProcessedAfterGrace(t *testing.T) {
	// Issue is open with pilot label, no pilot-done / pilot-in-progress.
	// This is the state during the merge→pilot-done window.
	issues := []*Issue{
		{Number: 77, Title: "Issue 77", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/repos/owner/repo/issues") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(issues)
		case r.URL.Path == "/search/issues":
			// Search API: no merged PR found (simulates index lag)
			_, _ = w.Write([]byte(`{"total_count": 0}`))
		case r.URL.Path == "/repos/owner/repo/pulls":
			// Branch REST lookup: no merged PR (simulates the window before GitHub propagates)
			_, _ = w.Write([]byte(`[]`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)

	var callCount atomic.Int32
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithRetryGracePeriod(5*time.Millisecond),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			callCount.Add(1)
			return nil
		}),
	)

	// Step 1: initial dispatch — markProcessed at T=0
	poller.markProcessed(77)

	// Step 2: grace period expires
	time.Sleep(15 * time.Millisecond)

	// Step 3: MarkProcessed called by onIssueDone (autopilot merge callback)
	// — refreshes the timestamp, re-entering the grace window
	poller.MarkProcessed(77)

	// Step 4: poll tick fires during merge→pilot-done window
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := callCount.Load(); got != 0 {
		t.Errorf("dispatch callback called %d times, want 0 — MarkProcessed should prevent re-dispatch in merge→done window", got)
	}
}

// GH-3513 wave 2 (#3537): a child with NO PR and NO completed execution must
// not be superseded even when the parent is closed + pilot-done — the parent
// close may be premature and the child's slice never shipped. It must fall
// through to normal dispatch.
func TestPoller_CheckForNewIssues_DoesNotSupersedeChildWithoutShippedEvidence(t *testing.T) {
	subIssue := &Issue{
		Number: 203,
		Title:  "Sub-issue with no PR yet",
		Body:   "Parent: GH-103\n\nUnshipped slice.",
		State:  "open",
		Labels: []Label{{Name: "pilot"}},
	}
	parent := &Issue{
		Number: 103,
		Title:  "Epic",
		State:  "closed",
		Labels: []Label{{Name: "pilot"}, {Name: LabelDone}},
	}

	var (
		dispatches  int32
		closedIssue int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{subIssue})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/103":
			_ = json.NewEncoder(w).Encode(parent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/203":
			_ = json.NewEncoder(w).Encode(subIssue)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls":
			// No PRs on the child's branch — open or merged.
			_ = json.NewEncoder(w).Encode([]*PullRequest{})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/203":
			atomic.AddInt32(&closedIssue, 1)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&closedIssue); got != 0 {
		t.Errorf("sub-issue closed %d times, want 0 (no shipped evidence — must not supersede)", got)
	}
	if got := atomic.LoadInt32(&dispatches); got != 1 {
		t.Errorf("dispatched %d times, want 1 (child must dispatch when its slice never shipped)", got)
	}
}

// GH-3513 wave 2: a completed execution row is valid evidence the child's
// slice shipped — supersession may proceed.
func TestPoller_CheckForNewIssues_SupersedesChildWithCompletedExecution(t *testing.T) {
	subIssue := &Issue{
		Number: 204,
		Title:  "Sub-issue shipped via execution row",
		Body:   "Parent: GH-104\n\nShipped slice.",
		State:  "open",
		Labels: []Label{{Name: "pilot"}},
	}
	parent := &Issue{
		Number: 104,
		Title:  "Epic",
		State:  "closed",
		Labels: []Label{{Name: "pilot"}, {Name: LabelDone}},
	}

	var (
		dispatches  int32
		closedIssue int32
		gotLabelsMu sync.Mutex
		gotLabels   []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{subIssue})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/104":
			_ = json.NewEncoder(w).Encode(parent)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues/204":
			_ = json.NewEncoder(w).Encode(subIssue)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls":
			_ = json.NewEncoder(w).Encode([]*PullRequest{}) // no PRs at all
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/204/labels":
			var payload struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			gotLabelsMu.Lock()
			gotLabels = append(gotLabels, payload.Labels...)
			gotLabelsMu.Unlock()
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/owner/repo/issues/204":
			atomic.AddInt32(&closedIssue, 1)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	checker := &mockExecutionChecker{completed: map[string]bool{"GH-204:/proj/p": true}}
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionChecker(checker, "/proj/p"),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			atomic.AddInt32(&dispatches, 1)
			return nil
		}),
	)
	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if got := atomic.LoadInt32(&closedIssue); got != 1 {
		t.Errorf("sub-issue closed %d times, want 1 (completed execution = shipped evidence)", got)
	}
	if got := atomic.LoadInt32(&dispatches); got != 0 {
		t.Errorf("dispatched %d times, want 0 (superseded)", got)
	}
	gotLabelsMu.Lock()
	defer gotLabelsMu.Unlock()
	found := false
	for _, l := range gotLabels {
		if l == LabelSuperseded {
			found = true
		}
	}
	if !found {
		t.Errorf("expected pilot-superseded label, got %v", gotLabels)
	}
}
