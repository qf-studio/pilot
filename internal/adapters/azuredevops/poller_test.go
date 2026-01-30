package azuredevops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewPoller(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	if poller.tag != "pilot" {
		t.Errorf("expected tag 'pilot', got '%s'", poller.tag)
	}

	if poller.interval != 30*time.Second {
		t.Errorf("expected interval 30s, got %v", poller.interval)
	}

	if poller.executionMode != ExecutionModeParallel {
		t.Errorf("expected default mode parallel, got %s", poller.executionMode)
	}

	if !poller.waitForMerge {
		t.Error("expected waitForMerge to be true by default")
	}
}

func TestPollerWithOptions(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")

	handler := func(ctx context.Context, wi *WorkItem) error {
		return nil
	}

	poller := NewPoller(client, "pilot", 30*time.Second,
		WithOnWorkItem(handler),
		WithExecutionMode(ExecutionModeSequential),
		WithSequentialConfig(false, 10*time.Second, 30*time.Minute),
		WithWorkItemTypes([]string{"Bug", "Task"}),
	)

	if poller.executionMode != ExecutionModeSequential {
		t.Errorf("expected mode sequential, got %s", poller.executionMode)
	}

	if poller.waitForMerge {
		t.Error("expected waitForMerge to be false")
	}

	if poller.prPollInterval != 10*time.Second {
		t.Errorf("expected prPollInterval 10s, got %v", poller.prPollInterval)
	}

	if poller.prTimeout != 30*time.Minute {
		t.Errorf("expected prTimeout 30m, got %v", poller.prTimeout)
	}

	if len(poller.workItemTypes) != 2 {
		t.Errorf("expected 2 work item types, got %d", len(poller.workItemTypes))
	}

	// Test handler is set
	if poller.onWorkItem == nil {
		t.Error("expected onWorkItem handler to be set")
	}
}

func TestPollerMarkProcessed(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	if poller.IsProcessed(123) {
		t.Error("expected 123 NOT to be processed initially")
	}

	poller.markProcessed(123)

	if !poller.IsProcessed(123) {
		t.Error("expected 123 to be processed after marking")
	}

	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1, got %d", poller.ProcessedCount())
	}

	poller.Reset()

	if poller.IsProcessed(123) {
		t.Error("expected 123 NOT to be processed after reset")
	}

	if poller.ProcessedCount() != 0 {
		t.Errorf("expected processed count 0 after reset, got %d", poller.ProcessedCount())
	}
}

func TestPollerFindOldestUnprocessedWorkItem(t *testing.T) {
	// Create mock server
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// WIQL query
			wiqlResult := WIQLQueryResult{
				WorkItems: []WIQLWorkItemRef{
					{ID: 1},
					{ID: 2},
					{ID: 3},
				},
			}
			_ = json.NewEncoder(w).Encode(wiqlResult)
		} else {
			// Work items batch
			workItems := struct {
				Count int         `json:"count"`
				Value []*WorkItem `json:"value"`
			}{
				Count: 3,
				Value: []*WorkItem{
					{
						ID:     1,
						Fields: map[string]interface{}{
							"System.Title":       "First (oldest)",
							"System.CreatedDate": "2024-01-01T10:00:00Z",
							"System.Tags":        "pilot",
						},
					},
					{
						ID:     2,
						Fields: map[string]interface{}{
							"System.Title":       "Second",
							"System.CreatedDate": "2024-01-02T10:00:00Z",
							"System.Tags":        "pilot; pilot-in-progress", // Already in progress
						},
					},
					{
						ID:     3,
						Fields: map[string]interface{}{
							"System.Title":       "Third",
							"System.CreatedDate": "2024-01-03T10:00:00Z",
							"System.Tags":        "pilot",
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(workItems)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	poller := NewPoller(client, "pilot", 30*time.Second)

	ctx := context.Background()
	wi, err := poller.findOldestUnprocessedWorkItem(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wi == nil {
		t.Fatal("expected to find a work item")
	}

	// Should find ID 1 (oldest that's not in progress)
	if wi.ID != 1 {
		t.Errorf("expected oldest unprocessed work item ID 1, got %d", wi.ID)
	}
}

func TestPollerFindOldestUnprocessedWorkItemNone(t *testing.T) {
	// Create mock server with no work items
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// Empty WIQL result
			wiqlResult := WIQLQueryResult{
				WorkItems: []WIQLWorkItemRef{},
			}
			_ = json.NewEncoder(w).Encode(wiqlResult)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	poller := NewPoller(client, "pilot", 30*time.Second)

	ctx := context.Background()
	wi, err := poller.findOldestUnprocessedWorkItem(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if wi != nil {
		t.Errorf("expected nil when no work items, got ID %d", wi.ID)
	}
}

func TestExtractPRNumber(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected int
		wantErr  bool
	}{
		{
			name:     "valid PR URL",
			url:      "https://dev.azure.com/org/project/_git/repo/pullrequest/123",
			expected: 123,
			wantErr:  false,
		},
		{
			name:     "PR URL with trailing slash",
			url:      "https://dev.azure.com/org/project/_git/repo/pullrequest/456/",
			expected: 456,
			wantErr:  false,
		},
		{
			name:     "empty URL",
			url:      "",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "URL without PR number",
			url:      "https://dev.azure.com/org/project/_git/repo",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid URL format",
			url:      "not a url",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ExtractPRNumber(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractPRNumber() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("ExtractPRNumber() = %d, expected %d", result, tt.expected)
			}
		})
	}
}
