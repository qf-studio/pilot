package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/testutil"
)

func TestNewCleaner(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	tests := []struct {
		name    string
		repo    string
		config  *StaleLabelCleanupConfig
		wantErr bool
	}{
		{
			name: "valid repo format",
			repo: "owner/repo",
			config: &StaleLabelCleanupConfig{
				Enabled:   true,
				Interval:  30 * time.Minute,
				Threshold: 1 * time.Hour,
			},
			wantErr: false,
		},
		{
			name: "invalid repo format - no slash",
			repo: "ownerrepo",
			config: &StaleLabelCleanupConfig{
				Enabled: true,
			},
			wantErr: true,
		},
		{
			name: "invalid repo format - multiple slashes",
			repo: "owner/repo/extra",
			config: &StaleLabelCleanupConfig{
				Enabled: true,
			},
			wantErr: true,
		},
		{
			name: "invalid repo format - empty",
			repo: "",
			config: &StaleLabelCleanupConfig{
				Enabled: true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testutil.FakeGitHubToken)
			cleaner, err := NewCleaner(client, store, tt.repo, tt.config)

			if (err != nil) != tt.wantErr {
				t.Errorf("NewCleaner() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if cleaner == nil {
					t.Fatal("NewCleaner returned nil")
				}
				if cleaner.client != client {
					t.Error("cleaner.client not set correctly")
				}
				if cleaner.store != store {
					t.Error("cleaner.store not set correctly")
				}
			}
		})
	}
}

func TestNewCleaner_DefaultValues(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	client := NewClient(testutil.FakeGitHubToken)

	// Test with zero values - should use defaults
	config := &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  0, // Should default to 30m
		Threshold: 0, // Should default to 1h
	}

	cleaner, err := NewCleaner(client, store, "owner/repo", config)
	if err != nil {
		t.Fatalf("NewCleaner() error = %v", err)
	}

	if cleaner.interval != 30*time.Minute {
		t.Errorf("cleaner.interval = %v, want 30m", cleaner.interval)
	}
	if cleaner.threshold != 1*time.Hour {
		t.Errorf("cleaner.threshold = %v, want 1h", cleaner.threshold)
	}
}

func TestWithCleanerLogger(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	client := NewClient(testutil.FakeGitHubToken)
	customLogger := slog.Default()

	cleaner, err := NewCleaner(client, store, "owner/repo",
		&StaleLabelCleanupConfig{Enabled: true},
		WithCleanerLogger(customLogger),
	)
	if err != nil {
		t.Fatalf("NewCleaner() error = %v", err)
	}

	if cleaner.logger != customLogger {
		t.Error("custom logger should be set")
	}
}

func TestCleaner_Cleanup_NoIssues(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  30 * time.Minute,
		Threshold: 1 * time.Hour,
	})

	err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}
}

func TestCleaner_Cleanup_StaleIssuesRemoved(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	// Create stale issue (updated 2 hours ago)
	staleTime := time.Now().Add(-2 * time.Hour)
	issues := []*Issue{
		{
			Number:    123,
			Title:     "Stale Issue",
			Labels:    []Label{{Name: LabelInProgress}},
			UpdatedAt: staleTime,
		},
	}

	var removeLabelCalled bool
	var addCommentCalled bool
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		// List issues endpoint
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}

		// Remove label endpoint
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/123/labels/"+LabelInProgress {
			removeLabelCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}

		// Add comment endpoint
		if r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/123/comments" {
			addCommentCalled = true
			_ = json.NewEncoder(w).Encode(&Comment{ID: 1, Body: "cleanup"})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  30 * time.Minute,
		Threshold: 1 * time.Hour,
	})

	err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !removeLabelCalled {
		t.Error("RemoveLabel should have been called for stale issue")
	}
	if !addCommentCalled {
		t.Error("AddComment should have been called for stale issue")
	}
}

func TestCleaner_Cleanup_RecentIssuesSkipped(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	// Create recent issue (updated 30 minutes ago - under 1h threshold)
	recentTime := time.Now().Add(-30 * time.Minute)
	issues := []*Issue{
		{
			Number:    456,
			Title:     "Recent Issue",
			Labels:    []Label{{Name: LabelInProgress}},
			UpdatedAt: recentTime,
		},
	}

	removeLabelCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// List issues endpoint
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}

		// Remove label endpoint - should not be called
		if r.Method == http.MethodDelete {
			removeLabelCalled = true
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  30 * time.Minute,
		Threshold: 1 * time.Hour,
	})

	err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}

	if removeLabelCalled {
		t.Error("RemoveLabel should NOT have been called for recent issue")
	}
}

func TestCleaner_Cleanup_ActiveExecutionsSkipped(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	// Create an active execution for issue 789
	exec := &memory.Execution{
		ID:          "exec-001",
		TaskID:      "GH-789",
		ProjectPath: "/test/project",
		Status:      "running",
	}
	if err := store.SaveExecution(exec); err != nil {
		t.Fatalf("Failed to save execution: %v", err)
	}

	// Create stale issue that has an active execution
	staleTime := time.Now().Add(-2 * time.Hour)
	issues := []*Issue{
		{
			Number:    789,
			Title:     "Issue with Active Execution",
			Labels:    []Label{{Name: LabelInProgress}},
			UpdatedAt: staleTime,
		},
	}

	removeLabelCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// List issues endpoint
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(issues)
			return
		}

		// Remove label endpoint - should not be called
		if r.Method == http.MethodDelete {
			removeLabelCalled = true
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  30 * time.Minute,
		Threshold: 1 * time.Hour,
	})

	err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}

	if removeLabelCalled {
		t.Error("RemoveLabel should NOT have been called for issue with active execution")
	}
}

func TestCleaner_Cleanup_APIError(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  30 * time.Minute,
		Threshold: 1 * time.Hour,
	})

	err := cleaner.Cleanup(context.Background())
	if err == nil {
		t.Error("Cleanup() should return error on API failure")
	}
}

func TestCleaner_StartStop(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  50 * time.Millisecond,
		Threshold: 1 * time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())

	// Start cleaner
	done := make(chan struct{})
	go func() {
		cleaner.Start(ctx)
		close(done)
	}()

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Stop via context cancellation
	cancel()

	// Should exit within reasonable time
	select {
	case <-done:
		// Good - cleaner stopped
	case <-time.After(1 * time.Second):
		t.Error("cleaner did not stop within timeout after context cancellation")
	}
}

func TestCleaner_Stop(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  50 * time.Millisecond,
		Threshold: 1 * time.Hour,
	})

	ctx := context.Background()

	// Start cleaner
	done := make(chan struct{})
	go func() {
		cleaner.Start(ctx)
		close(done)
	}()

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Stop via Stop method
	cleaner.Stop()

	// Should exit within reasonable time
	select {
	case <-done:
		// Good - cleaner stopped
	case <-time.After(1 * time.Second):
		t.Error("cleaner did not stop within timeout after Stop()")
	}
}

func TestCleaner_DoubleStart(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  100 * time.Millisecond,
		Threshold: 1 * time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cleaner twice - should not panic or block
	go cleaner.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	go cleaner.Start(ctx) // Second call should return immediately

	time.Sleep(100 * time.Millisecond)
	cleaner.Stop()
}

func TestStaleLabelCleanupConfig_InDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.StaleLabelCleanup == nil {
		t.Fatal("StaleLabelCleanup should be set in default config")
	}

	if !cfg.StaleLabelCleanup.Enabled {
		t.Error("StaleLabelCleanup.Enabled should be true by default")
	}

	if cfg.StaleLabelCleanup.Interval != 30*time.Minute {
		t.Errorf("StaleLabelCleanup.Interval = %v, want 30m", cfg.StaleLabelCleanup.Interval)
	}

	if cfg.StaleLabelCleanup.Threshold != 1*time.Hour {
		t.Errorf("StaleLabelCleanup.Threshold = %v, want 1h", cfg.StaleLabelCleanup.Threshold)
	}

	if cfg.StaleLabelCleanup.FailedThreshold != 24*time.Hour {
		t.Errorf("StaleLabelCleanup.FailedThreshold = %v, want 24h", cfg.StaleLabelCleanup.FailedThreshold)
	}
}

func TestCleaner_Cleanup_StaleFailedLabelsRemoved(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	// Create stale failed issue (updated 25 hours ago - over 24h threshold)
	staleTime := time.Now().Add(-25 * time.Hour)
	// Note: ListIssues does post-fetch filtering (client filters by label after API call)
	// So we return all issues with any label, and the client filters by label name
	allIssues := []*Issue{
		{
			Number:    456,
			Title:     "Stale Failed Issue",
			Labels:    []Label{{Name: LabelFailed}},
			UpdatedAt: staleTime,
		},
	}

	var removeLabelCalled bool
	var addCommentCalled bool
	var onFailedCleanedCalled bool
	var cleanedIssueNumber int
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		// List issues endpoint - return all issues (client filters by label)
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(allIssues)
			return
		}

		// Remove label endpoint
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/456/labels/"+LabelFailed {
			removeLabelCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}

		// Add comment endpoint
		if r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/456/comments" {
			addCommentCalled = true
			_ = json.NewEncoder(w).Encode(&Comment{ID: 1, Body: "cleanup"})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:         true,
		Interval:        30 * time.Minute,
		Threshold:       1 * time.Hour,
		FailedThreshold: 24 * time.Hour,
	}, WithOnFailedCleaned(func(issueNumber int) {
		mu.Lock()
		onFailedCleanedCalled = true
		cleanedIssueNumber = issueNumber
		mu.Unlock()
	}))

	err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !removeLabelCalled {
		t.Error("RemoveLabel should have been called for stale failed issue")
	}
	if !addCommentCalled {
		t.Error("AddComment should have been called for stale failed issue")
	}
	if !onFailedCleanedCalled {
		t.Error("OnFailedCleaned callback should have been called")
	}
	if cleanedIssueNumber != 456 {
		t.Errorf("OnFailedCleaned called with issue %d, want 456", cleanedIssueNumber)
	}
}

// GH-2402: Cleaner removes stale pilot-blocked labels and fires OnBlockedCleaned
// so the poller can clear the issue from its processed map.
func TestCleaner_Cleanup_StaleBlockedLabelsRemoved(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	staleTime := time.Now().Add(-25 * time.Hour)
	allIssues := []*Issue{
		{
			Number:    789,
			Title:     "Stale Blocked Issue",
			Labels:    []Label{{Name: LabelBlocked}},
			UpdatedAt: staleTime,
		},
	}

	var (
		mu                     sync.Mutex
		removeLabelCalled      bool
		addCommentCalled       bool
		onBlockedCleanedCalled bool
		cleanedIssueNumber     int
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(allIssues)
			return
		}
		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/789/labels/"+LabelBlocked {
			removeLabelCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/789/comments" {
			addCommentCalled = true
			_ = json.NewEncoder(w).Encode(&Comment{ID: 1, Body: "cleanup"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:         true,
		Interval:        30 * time.Minute,
		Threshold:       1 * time.Hour,
		FailedThreshold: 24 * time.Hour,
	}, WithOnBlockedCleaned(func(issueNumber int) {
		mu.Lock()
		onBlockedCleanedCalled = true
		cleanedIssueNumber = issueNumber
		mu.Unlock()
	}))

	if err := cleaner.Cleanup(context.Background()); err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !removeLabelCalled {
		t.Error("RemoveLabel should have been called for stale blocked issue")
	}
	if !addCommentCalled {
		t.Error("AddComment should have been called for stale blocked issue")
	}
	if !onBlockedCleanedCalled {
		t.Error("OnBlockedCleaned callback should have been called")
	}
	if cleanedIssueNumber != 789 {
		t.Errorf("OnBlockedCleaned called with issue %d, want 789", cleanedIssueNumber)
	}
}

func TestCleaner_Cleanup_RecentFailedIssuesSkipped(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	// Create recent failed issue (updated 12 hours ago - under 24h threshold)
	recentTime := time.Now().Add(-12 * time.Hour)
	allIssues := []*Issue{
		{
			Number:    789,
			Title:     "Recent Failed Issue",
			Labels:    []Label{{Name: LabelFailed}},
			UpdatedAt: recentTime,
		},
	}

	removeLabelCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// List issues endpoint - return all issues (client filters by label)
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			_ = json.NewEncoder(w).Encode(allIssues)
			return
		}

		// Remove label endpoint - should not be called
		if r.Method == http.MethodDelete {
			removeLabelCalled = true
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:         true,
		Interval:        30 * time.Minute,
		Threshold:       1 * time.Hour,
		FailedThreshold: 24 * time.Hour,
	})

	err := cleaner.Cleanup(context.Background())
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}

	if removeLabelCalled {
		t.Error("RemoveLabel should NOT have been called for recent failed issue")
	}
}

func TestCleaner_FailedThreshold_DefaultValue(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	client := NewClient(testutil.FakeGitHubToken)

	// Test with zero FailedThreshold - should default to 24h
	config := &StaleLabelCleanupConfig{
		Enabled:         true,
		Interval:        30 * time.Minute,
		Threshold:       1 * time.Hour,
		FailedThreshold: 0, // Should default to 24h
	}

	cleaner, err := NewCleaner(client, store, "owner/repo", config)
	if err != nil {
		t.Fatalf("NewCleaner() error = %v", err)
	}

	if cleaner.failedThreshold != 24*time.Hour {
		t.Errorf("cleaner.failedThreshold = %v, want 24h", cleaner.failedThreshold)
	}
}

// GH-2354: pilot-in-progress label on externally-closed issues should be
// cleaned up immediately, and the dashboard monitor callback should fire.
func TestCleaner_Cleanup_ClosedInProgressIssueCleaned(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	closedIssues := []*Issue{
		{
			Number:    2348,
			Title:     "Externally closed issue",
			Labels:    []Label{{Name: LabelInProgress}},
			State:     StateClosed,
			UpdatedAt: time.Now(), // recent — threshold must be ignored for closed
		},
	}

	var (
		mu              sync.Mutex
		removeLabelHit  bool
		sawStateClosed  bool
		commentOnClosed bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			state := r.URL.Query().Get("state")
			if state == "closed" {
				sawStateClosed = true
				_ = json.NewEncoder(w).Encode(closedIssues)
				return
			}
			_ = json.NewEncoder(w).Encode([]*Issue{})
			return
		}

		if r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/issues/2348/labels/"+LabelInProgress {
			removeLabelHit = true
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/2348/comments" {
			commentOnClosed = true
			_ = json.NewEncoder(w).Encode(&Comment{ID: 1})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	var callbackIssue int
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled:   true,
		Interval:  30 * time.Minute,
		Threshold: 1 * time.Hour,
	}, WithOnInProgressCleaned(func(n int) { callbackIssue = n }))

	if err := cleaner.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !sawStateClosed {
		t.Error("expected ListIssues to be called with state=closed")
	}
	if !removeLabelHit {
		t.Error("RemoveLabel should have been called for closed in-progress issue")
	}
	if commentOnClosed {
		t.Error("AddComment must NOT be called for closed-issue cleanup (silent)")
	}
	if callbackIssue != 2348 {
		t.Errorf("OnInProgressCleaned callback issue = %d, want 2348", callbackIssue)
	}
}

// GH-2354: active executions for closed issues must NOT have the label stripped
// while the task is still running in-memory.
func TestCleaner_Cleanup_ClosedInProgressWithActiveExecutionSkipped(t *testing.T) {
	store := createTestStore(t)
	defer func() { _ = store.Close() }()

	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-2351",
		TaskID:      "GH-2351",
		ProjectPath: "/test/project",
		Status:      "running",
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	closedIssues := []*Issue{
		{Number: 2351, Title: "Closed but still running", Labels: []Label{{Name: LabelInProgress}}, State: StateClosed, UpdatedAt: time.Now()},
	}

	removeLabelHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues" {
			if r.URL.Query().Get("state") == "closed" {
				_ = json.NewEncoder(w).Encode(closedIssues)
				return
			}
			_ = json.NewEncoder(w).Encode([]*Issue{})
			return
		}
		if r.Method == http.MethodDelete {
			removeLabelHit = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	callbackFired := false
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, _ := NewCleaner(client, store, "owner/repo", &StaleLabelCleanupConfig{
		Enabled: true, Interval: 30 * time.Minute, Threshold: 1 * time.Hour,
	}, WithOnInProgressCleaned(func(int) { callbackFired = true }))

	if err := cleaner.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	if removeLabelHit {
		t.Error("RemoveLabel must NOT be called for closed issue with active execution")
	}
	if callbackFired {
		t.Error("OnInProgressCleaned must NOT fire for closed issue with active execution")
	}
}

// Helper function to create a test memory store
func createTestStore(t *testing.T) *memory.Store {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "cleanup-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Cleanup on test completion
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	store, err := memory.NewStore(filepath.Join(tmpDir, "test-db"))
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	return store
}
