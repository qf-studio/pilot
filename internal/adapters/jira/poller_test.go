package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewPoller(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{
		PilotLabel: "pilot",
		ProjectKey: "TEST",
	}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.pilotLabel != "pilot" {
		t.Errorf("expected pilotLabel 'pilot', got '%s'", poller.pilotLabel)
	}

	if poller.interval != 30*time.Second {
		t.Errorf("expected interval 30s, got %v", poller.interval)
	}

	if len(poller.processed) != 0 {
		t.Error("expected empty processed map")
	}
}

func TestNewPoller_DefaultLabel(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{
		PilotLabel: "", // Empty label should default to "pilot"
	}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.pilotLabel != "pilot" {
		t.Errorf("expected default pilotLabel 'pilot', got '%s'", poller.pilotLabel)
	}
}

func TestPollerWithOptions(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	var callbackCalled bool
	handler := func(ctx context.Context, issue *Issue) (*IssueResult, error) {
		callbackCalled = true
		return &IssueResult{Success: true}, nil
	}

	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(handler),
	)

	if poller.onIssue == nil {
		t.Error("expected onIssue handler to be set")
	}

	// Call the handler to verify it's wired correctly
	_, _ = poller.onIssue(context.Background(), &Issue{})
	if !callbackCalled {
		t.Error("expected callback to be called")
	}
}

func TestPollerMarkProcessed(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 NOT to be processed initially")
	}

	poller.markProcessed("TEST-123")

	if !poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 to be processed after marking")
	}

	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1, got %d", poller.ProcessedCount())
	}

	poller.Reset()

	if poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 NOT to be processed after reset")
	}

	if poller.ProcessedCount() != 0 {
		t.Errorf("expected processed count 0 after reset, got %d", poller.ProcessedCount())
	}
}

func TestPollerClearProcessed(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	poller.markProcessed("TEST-123")
	poller.markProcessed("TEST-456")

	if poller.ProcessedCount() != 2 {
		t.Errorf("expected processed count 2, got %d", poller.ProcessedCount())
	}

	poller.ClearProcessed("TEST-123")

	if poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 NOT to be processed after clearing")
	}
	if !poller.IsProcessed("TEST-456") {
		t.Error("expected TEST-456 to still be processed")
	}
	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1 after clearing one, got %d", poller.ProcessedCount())
	}
}

func TestPollerConcurrentAccess(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "TEST-" + string(rune('0'+n%10))
			poller.markProcessed(key)
			_ = poller.IsProcessed(key)
			_ = poller.ProcessedCount()
		}(i)
	}
	wg.Wait()

	// No race condition should occur
	count := poller.ProcessedCount()
	if count == 0 {
		t.Error("expected some processed items")
	}
}

func TestPollerBuildJQL(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)

	tests := []struct {
		name    string
		config  *Config
		wantJQL string
	}{
		{
			name: "label only",
			config: &Config{
				PilotLabel: "pilot",
			},
			wantJQL: `labels = "pilot" AND statusCategory != Done ORDER BY created ASC`,
		},
		{
			name: "label and project",
			config: &Config{
				PilotLabel: "pilot",
				ProjectKey: "TEST",
			},
			wantJQL: `labels = "pilot" AND project = "TEST" AND statusCategory != Done ORDER BY created ASC`,
		},
		{
			name: "custom label",
			config: &Config{
				PilotLabel: "autopilot",
				ProjectKey: "MYPROJ",
			},
			wantJQL: `labels = "autopilot" AND project = "MYPROJ" AND statusCategory != Done ORDER BY created ASC`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poller := NewPoller(client, tt.config, 30*time.Second)
			got := poller.buildJQL()
			if got != tt.wantJQL {
				t.Errorf("buildJQL() = %q, want %q", got, tt.wantJQL)
			}
		})
	}
}

func TestPollerCheckForNewIssues(t *testing.T) {
	var requestCount int
	var processedIssue *Issue

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		// Handle search request
		if r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/search" {
			resp := SearchResponse{
				Issues: []*Issue{
					{
						Key: "TEST-1",
						Fields: Fields{
							Summary:     "First issue",
							Description: "Test description",
							Created:     "2024-01-01T10:00:00.000+0000",
							Labels:      []string{"pilot"},
						},
					},
					{
						Key: "TEST-2",
						Fields: Fields{
							Summary:     "Second issue (in progress)",
							Description: "Already being worked on",
							Created:     "2024-01-02T10:00:00.000+0000",
							Labels:      []string{"pilot", "pilot-in-progress"},
						},
					},
				},
				Total:      2,
				MaxResults: 50,
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		// Handle label add/remove
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			processedIssue = issue
			return &IssueResult{Success: true}, nil
		}),
	)

	ctx := context.Background()
	poller.checkForNewIssues(ctx)
	// GH-1357: Wait for parallel execution to complete
	poller.WaitForActive()

	// Should process TEST-1 but skip TEST-2 (has in-progress label)
	if processedIssue == nil {
		t.Fatal("expected an issue to be processed")
	}

	if processedIssue.Key != "TEST-1" {
		t.Errorf("expected TEST-1 to be processed, got %s", processedIssue.Key)
	}

	// TEST-1 should be marked as processed
	if !poller.IsProcessed("TEST-1") {
		t.Error("expected TEST-1 to be marked as processed")
	}
}

func TestPollerCheckForNewIssues_SkipsAlreadyProcessed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/search" {
			resp := SearchResponse{
				Issues: []*Issue{
					{
						Key: "TEST-1",
						Fields: Fields{
							Summary: "Already processed",
							Labels:  []string{"pilot"},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	var callCount int
	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			callCount++
			return &IssueResult{Success: true}, nil
		}),
	)

	// Mark as already processed
	poller.markProcessed("TEST-1")

	ctx := context.Background()
	poller.checkForNewIssues(ctx)

	if callCount != 0 {
		t.Errorf("expected callback not to be called for already processed issue, got %d calls", callCount)
	}
}

func TestPollerStart_CancelsOnContextDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := SearchResponse{Issues: []*Issue{}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}
	poller := NewPoller(client, config, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- poller.Start(ctx)
	}()

	// Cancel after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("poller did not stop after context cancellation")
	}
}

func TestPollerHasStatusLabel(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", []string{}, false},
		{"pilot only", []string{"pilot"}, false},
		{"in-progress", []string{"pilot", "pilot-in-progress"}, true},
		{"done", []string{"pilot", "pilot-done"}, true},
		{"failed", []string{"pilot", "pilot-failed"}, true},
		{"case insensitive", []string{"PILOT-IN-PROGRESS"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Fields: Fields{Labels: tt.labels},
			}
			got := poller.hasStatusLabel(issue)
			if got != tt.want {
				t.Errorf("hasStatusLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClientHasLabel(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)

	tests := []struct {
		name   string
		labels []string
		search string
		want   bool
	}{
		{"exact match", []string{"pilot"}, "pilot", true},
		{"case insensitive", []string{"Pilot"}, "pilot", true},
		{"not found", []string{"pilot"}, "autopilot", false},
		{"empty labels", []string{}, "pilot", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Fields: Fields{Labels: tt.labels},
			}
			got := client.HasLabel(issue, tt.search)
			if got != tt.want {
				t.Errorf("HasLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

// GH-1357: Tests for parallel execution pattern and ProcessedStore

// mockProcessedStore implements ProcessedStore for testing.
type mockProcessedStore struct {
	mu        sync.Mutex
	processed map[string]string // key -> result
}

func newMockProcessedStore() *mockProcessedStore {
	return &mockProcessedStore{
		processed: make(map[string]string),
	}
}

func (m *mockProcessedStore) MarkJiraIssueProcessed(issueKey string, result string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed[issueKey] = result
	return nil
}

func (m *mockProcessedStore) UnmarkJiraIssueProcessed(issueKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processed, issueKey)
	return nil
}

func (m *mockProcessedStore) IsJiraIssueProcessed(issueKey string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.processed[issueKey]
	return ok, nil
}

func (m *mockProcessedStore) LoadJiraProcessedIssues() (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]bool)
	for key := range m.processed {
		result[key] = true
	}
	return result, nil
}

// TestPoller_LoadsProcessedFromStore verifies that the poller loads
// processed issues from the store on startup (GH-1357).
func TestPoller_LoadsProcessedFromStore(t *testing.T) {
	store := newMockProcessedStore()

	// Pre-populate store with processed issues
	store.processed["TEST-1"] = "processed"
	store.processed["TEST-2"] = "processed"

	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second, WithProcessedStore(store))

	// Verify processed issues were loaded
	if poller.ProcessedCount() != 2 {
		t.Errorf("ProcessedCount = %d, want 2", poller.ProcessedCount())
	}

	if !poller.IsProcessed("TEST-1") {
		t.Error("TEST-1 should be processed")
	}
	if !poller.IsProcessed("TEST-2") {
		t.Error("TEST-2 should be processed")
	}
	if poller.IsProcessed("TEST-3") {
		t.Error("TEST-3 should not be processed")
	}
}

// TestPoller_MarkProcessed_PersistsToStore verifies that marking an issue
// as processed persists it to the store (GH-1357).
func TestPoller_MarkProcessed_PersistsToStore(t *testing.T) {
	store := newMockProcessedStore()
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second, WithProcessedStore(store))

	// Mark an issue as processed
	poller.markProcessed("TEST-NEW")

	// Verify it's in memory
	if !poller.IsProcessed("TEST-NEW") {
		t.Error("TEST-NEW should be processed in memory")
	}

	// Verify it's persisted to store
	store.mu.Lock()
	_, exists := store.processed["TEST-NEW"]
	store.mu.Unlock()
	if !exists {
		t.Error("TEST-NEW should be persisted to store")
	}
}

// TestPoller_ClearProcessed_RemovesFromStore verifies that clearing a processed
// issue removes it from both memory and store (GH-1357).
func TestPoller_ClearProcessed_RemovesFromStore(t *testing.T) {
	store := newMockProcessedStore()
	store.processed["TEST-CLEAR"] = "processed"

	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second, WithProcessedStore(store))

	// Verify issue is loaded
	if !poller.IsProcessed("TEST-CLEAR") {
		t.Error("TEST-CLEAR should be processed initially")
	}

	// Clear the issue
	poller.ClearProcessed("TEST-CLEAR")

	// Verify it's removed from memory
	if poller.IsProcessed("TEST-CLEAR") {
		t.Error("TEST-CLEAR should not be processed after clearing")
	}

	// Verify it's removed from store
	store.mu.Lock()
	_, exists := store.processed["TEST-CLEAR"]
	store.mu.Unlock()
	if exists {
		t.Error("TEST-CLEAR should be removed from store")
	}
}

// TestPoller_WithMaxConcurrent verifies that the max concurrent option is set.
func TestPoller_WithMaxConcurrent(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second, WithMaxConcurrent(5))

	if poller.maxConcurrent != 5 {
		t.Errorf("maxConcurrent = %d, want 5", poller.maxConcurrent)
	}

	if cap(poller.semaphore) != 5 {
		t.Errorf("semaphore capacity = %d, want 5", cap(poller.semaphore))
	}
}

// TestPoller_WithMaxConcurrent_DefaultsToTwo verifies that max concurrent defaults to 2.
func TestPoller_WithMaxConcurrent_DefaultsToTwo(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second)

	if poller.maxConcurrent != 2 {
		t.Errorf("default maxConcurrent = %d, want 2", poller.maxConcurrent)
	}
}

// TestPoller_WithMaxConcurrent_MinimumOne verifies that max concurrent cannot go below 1.
func TestPoller_WithMaxConcurrent_MinimumOne(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second, WithMaxConcurrent(0))

	if poller.maxConcurrent != 1 {
		t.Errorf("maxConcurrent with 0 = %d, want 1 (minimum)", poller.maxConcurrent)
	}

	poller2 := NewPoller(client, config, 30*time.Second, WithMaxConcurrent(-5))

	if poller2.maxConcurrent != 1 {
		t.Errorf("maxConcurrent with -5 = %d, want 1 (minimum)", poller2.maxConcurrent)
	}
}

// TestPoller_Drain verifies that Drain stops accepting new issues and waits for active.
func TestPoller_Drain(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second)

	// Simulate an active task
	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)

	drainDone := make(chan struct{})
	go func() {
		poller.Drain()
		close(drainDone)
	}()

	// Give Drain time to set stopping flag
	time.Sleep(10 * time.Millisecond)

	if !poller.stopping.Load() {
		t.Error("stopping should be true after Drain called")
	}

	// Complete the active task
	<-poller.semaphore
	poller.activeWg.Done()

	// Drain should complete
	select {
	case <-drainDone:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Error("Drain should complete after active tasks finish")
	}
}

// TestPoller_WaitForActive verifies that WaitForActive waits for goroutines.
func TestPoller_WaitForActive(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{PilotLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second)

	// Simulate an active task
	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)

	waitDone := make(chan struct{})
	go func() {
		poller.WaitForActive()
		close(waitDone)
	}()

	// Give WaitForActive time to set stopping flag
	time.Sleep(10 * time.Millisecond)

	if !poller.stopping.Load() {
		t.Error("stopping should be true after WaitForActive called")
	}

	// Complete the active task
	<-poller.semaphore
	poller.activeWg.Done()

	// WaitForActive should complete
	select {
	case <-waitDone:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Error("WaitForActive should complete after active tasks finish")
	}
}
