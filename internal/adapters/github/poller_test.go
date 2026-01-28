package github

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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
			client := NewClient("test-token")
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
	client := NewClient("test-token")
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
	client := NewClient("test-token")

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
	client := NewClient("test-token")

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
	client := NewClient("test-token")

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
	client := NewClient("test-token")
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
	client := NewClient("test-token")
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
	client := NewClient("test-token")
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

func TestPoller_ConcurrentAccess(t *testing.T) {
	client := NewClient("test-token")
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

			client := NewClientWithBaseURL("test-token", server.URL)

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

			if len(processedIssues) != tt.expectedProcessed {
				t.Errorf("processed %d issues, want %d", len(processedIssues), tt.expectedProcessed)
			}
		})
	}
}

func TestPoller_CheckForNewIssues_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)

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

	client := NewClientWithBaseURL("test-token", server.URL)

	callCount := 0
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			callCount++
			return errors.New("callback error")
		}),
	)

	poller.checkForNewIssues(context.Background())

	// Both issues should be attempted (callback is called for both)
	if callCount != 2 {
		t.Errorf("callback called %d times, want 2", callCount)
	}

	// Issues should NOT be marked as processed when callback fails
	if poller.IsProcessed(1) {
		t.Error("issue 1 should not be marked as processed after callback error")
	}
	if poller.IsProcessed(2) {
		t.Error("issue 2 should not be marked as processed after callback error")
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

	client := NewClientWithBaseURL("test-token", server.URL)

	// Create poller without callback
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second)

	// Should not panic
	poller.checkForNewIssues(context.Background())

	// Issue should be marked as processed even without callback
	if !poller.IsProcessed(1) {
		t.Error("issue should be marked as processed when no callback is set")
	}
}

func TestPoller_CheckForNewIssues_SkipsAlreadyProcessed(t *testing.T) {
	issues := []*Issue{
		{Number: 1, Title: "Issue 1", Labels: []Label{{Name: "pilot"}}},
		{Number: 2, Title: "Issue 2", Labels: []Label{{Name: "pilot"}}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issues)
	}))
	defer server.Close()

	client := NewClientWithBaseURL("test-token", server.URL)

	callCount := 0
	poller, _ := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			callCount++
			return nil
		}),
	)

	// Pre-mark issue 1 as processed
	poller.markProcessed(1)

	poller.checkForNewIssues(context.Background())

	// Only issue 2 should trigger callback
	if callCount != 1 {
		t.Errorf("callback called %d times, want 1 (only for unprocessed issue)", callCount)
	}
}

func TestPoller_Start_CancelsOnContextDone(t *testing.T) {
	client := NewClient("test-token")
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

	client := NewClientWithBaseURL("test-token", server.URL)

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
