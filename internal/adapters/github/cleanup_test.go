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

	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/testutil"
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
