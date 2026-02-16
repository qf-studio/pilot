package linear

import (
	"sync"
	"testing"
)

// mockProcessedStore implements ProcessedStore for testing.
// GH-1351: Tests Linear processed issue persistence.
type mockProcessedStore struct {
	mu        sync.Mutex
	processed map[string]string // id -> result
}

func newMockProcessedStore() *mockProcessedStore {
	return &mockProcessedStore{
		processed: make(map[string]string),
	}
}

func (m *mockProcessedStore) MarkLinearIssueProcessed(issueID string, result string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed[issueID] = result
	return nil
}

func (m *mockProcessedStore) UnmarkLinearIssueProcessed(issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processed, issueID)
	return nil
}

func (m *mockProcessedStore) IsLinearIssueProcessed(issueID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.processed[issueID]
	return ok, nil
}

func (m *mockProcessedStore) LoadLinearProcessedIssues() (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]bool)
	for id := range m.processed {
		result[id] = true
	}
	return result, nil
}

// TestPoller_LoadsProcessedFromStore verifies that the poller loads
// processed issues from the store on startup (GH-1351).
func TestPoller_LoadsProcessedFromStore(t *testing.T) {
	store := newMockProcessedStore()

	// Pre-populate store with processed issues
	store.processed["linear-issue-1"] = "processed"
	store.processed["linear-issue-2"] = "processed"

	config := &WorkspaceConfig{
		TeamID:     "TEST",
		PilotLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	// Verify processed issues were loaded
	if poller.ProcessedCount() != 2 {
		t.Errorf("ProcessedCount = %d, want 2", poller.ProcessedCount())
	}

	if !poller.IsProcessed("linear-issue-1") {
		t.Error("linear-issue-1 should be processed")
	}
	if !poller.IsProcessed("linear-issue-2") {
		t.Error("linear-issue-2 should be processed")
	}
	if poller.IsProcessed("linear-issue-3") {
		t.Error("linear-issue-3 should not be processed")
	}
}

// TestPoller_MarkProcessed_PersistsToStore verifies that marking an issue
// as processed persists it to the store (GH-1351).
func TestPoller_MarkProcessed_PersistsToStore(t *testing.T) {
	store := newMockProcessedStore()
	config := &WorkspaceConfig{
		TeamID:     "TEST",
		PilotLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	// Mark an issue as processed
	poller.markProcessed("new-linear-issue")

	// Verify it's in memory
	if !poller.IsProcessed("new-linear-issue") {
		t.Error("new-linear-issue should be processed in memory")
	}

	// Verify it's persisted to store
	store.mu.Lock()
	_, exists := store.processed["new-linear-issue"]
	store.mu.Unlock()
	if !exists {
		t.Error("new-linear-issue should be persisted to store")
	}
}

// TestPoller_ClearProcessed_RemovesFromStore verifies that clearing a processed
// issue removes it from both memory and store (GH-1351).
func TestPoller_ClearProcessed_RemovesFromStore(t *testing.T) {
	store := newMockProcessedStore()
	store.processed["issue-to-clear"] = "processed"

	config := &WorkspaceConfig{
		TeamID:     "TEST",
		PilotLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	// Verify issue is loaded
	if !poller.IsProcessed("issue-to-clear") {
		t.Error("issue-to-clear should be processed initially")
	}

	// Clear the issue
	poller.ClearProcessed("issue-to-clear")

	// Verify it's removed from memory
	if poller.IsProcessed("issue-to-clear") {
		t.Error("issue-to-clear should not be processed after clearing")
	}

	// Verify it's removed from store
	store.mu.Lock()
	_, exists := store.processed["issue-to-clear"]
	store.mu.Unlock()
	if exists {
		t.Error("issue-to-clear should be removed from store")
	}
}

// TestPoller_WithoutStore_StillWorks verifies that the poller works
// without a ProcessedStore (backward compatibility).
func TestPoller_WithoutStore_StillWorks(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:     "TEST",
		PilotLabel: "pilot",
	}

	// Create poller without store
	poller := NewPoller(nil, config, 30)

	// Should not panic and work with in-memory only
	poller.markProcessed("memory-only-issue")

	if !poller.IsProcessed("memory-only-issue") {
		t.Error("memory-only-issue should be processed")
	}

	poller.ClearProcessed("memory-only-issue")

	if poller.IsProcessed("memory-only-issue") {
		t.Error("memory-only-issue should not be processed after clearing")
	}
}

// TestPoller_Reset_ClearsMemoryOnly verifies that Reset clears the in-memory
// map but doesn't affect the store.
func TestPoller_Reset_ClearsMemoryOnly(t *testing.T) {
	store := newMockProcessedStore()
	config := &WorkspaceConfig{
		TeamID:     "TEST",
		PilotLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	// Mark and persist an issue
	poller.markProcessed("persistent-issue")

	// Verify it's in store
	store.mu.Lock()
	_, exists := store.processed["persistent-issue"]
	store.mu.Unlock()
	if !exists {
		t.Fatal("persistent-issue should be in store")
	}

	// Reset clears in-memory map only
	poller.Reset()

	if poller.IsProcessed("persistent-issue") {
		t.Error("persistent-issue should not be in memory after Reset")
	}

	// Store should still have it (Reset doesn't clear store)
	store.mu.Lock()
	_, exists = store.processed["persistent-issue"]
	store.mu.Unlock()
	if !exists {
		t.Error("persistent-issue should still be in store after Reset")
	}
}
