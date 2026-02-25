package jira

import (
	"testing"
)

// mockGenericStore implements adapters.ProcessedStore for testing the shim.
type mockGenericStore struct {
	marked   map[string]map[string]string // adapter -> issueID -> result
	unmarked []string
}

func newMockGenericStore() *mockGenericStore {
	return &mockGenericStore{
		marked: make(map[string]map[string]string),
	}
}

func (m *mockGenericStore) MarkAdapterProcessed(adapter, issueID, result string) error {
	if m.marked[adapter] == nil {
		m.marked[adapter] = make(map[string]string)
	}
	m.marked[adapter][issueID] = result
	return nil
}

func (m *mockGenericStore) UnmarkAdapterProcessed(adapter, issueID string) error {
	delete(m.marked[adapter], issueID)
	m.unmarked = append(m.unmarked, adapter+":"+issueID)
	return nil
}

func (m *mockGenericStore) IsAdapterProcessed(adapter, issueID string) (bool, error) {
	if m.marked[adapter] == nil {
		return false, nil
	}
	_, ok := m.marked[adapter][issueID]
	return ok, nil
}

func (m *mockGenericStore) LoadAdapterProcessed(adapter string) (map[string]bool, error) {
	out := make(map[string]bool)
	for k := range m.marked[adapter] {
		out[k] = true
	}
	return out, nil
}

func TestGenericStoreShim_MarkAndLoad(t *testing.T) {
	mock := newMockGenericStore()
	shim := &genericStoreShim{store: mock, adapter: "jira"}

	// Mark via Jira-specific interface
	if err := shim.MarkJiraIssueProcessed("PROJ-1", "success"); err != nil {
		t.Fatalf("MarkJiraIssueProcessed failed: %v", err)
	}
	if err := shim.MarkJiraIssueProcessed("PROJ-2", "failed"); err != nil {
		t.Fatalf("MarkJiraIssueProcessed failed: %v", err)
	}

	// Verify stored under "jira" adapter namespace
	if mock.marked["jira"]["PROJ-1"] != "success" {
		t.Error("PROJ-1 not stored correctly via shim")
	}

	// Load via Jira-specific interface
	loaded, err := shim.LoadJiraProcessedIssues()
	if err != nil {
		t.Fatalf("LoadJiraProcessedIssues failed: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("loaded %d issues, want 2", len(loaded))
	}

	// IsProcessed
	ok, err := shim.IsJiraIssueProcessed("PROJ-1")
	if err != nil {
		t.Fatalf("IsJiraIssueProcessed failed: %v", err)
	}
	if !ok {
		t.Error("PROJ-1 should be processed")
	}

	ok, _ = shim.IsJiraIssueProcessed("PROJ-999")
	if ok {
		t.Error("PROJ-999 should not be processed")
	}

	// Unmark
	if err := shim.UnmarkJiraIssueProcessed("PROJ-1"); err != nil {
		t.Fatalf("UnmarkJiraIssueProcessed failed: %v", err)
	}
	ok, _ = shim.IsJiraIssueProcessed("PROJ-1")
	if ok {
		t.Error("PROJ-1 should be unprocessed after unmark")
	}
}

func TestNewAdapter(t *testing.T) {
	cfg := &Config{
		Enabled:  true,
		BaseURL:  "https://jira.example.com",
		Username: "user",
		APIToken: "fake-token",
		Platform: "cloud",
		Polling: &PollingConfig{
			Enabled: true,
		},
	}
	a := NewAdapter(cfg)
	if a.Name() != "jira" {
		t.Errorf("Name() = %q, want %q", a.Name(), "jira")
	}
	if a.Client() == nil {
		t.Error("Client() returned nil")
	}
	if !a.PollingEnabled() {
		t.Error("PollingEnabled() should return true")
	}
}
