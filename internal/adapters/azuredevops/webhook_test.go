package azuredevops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestWebhookHandlerVerifySecret(t *testing.T) {
	tests := []struct {
		name             string
		configuredSecret string
		providedSecret   string
		expected         bool
	}{
		{
			name:             "no secret configured",
			configuredSecret: "",
			providedSecret:   "anything",
			expected:         true,
		},
		{
			name:             "correct secret",
			configuredSecret: "my-secret",
			providedSecret:   "my-secret",
			expected:         true,
		},
		{
			name:             "wrong secret",
			configuredSecret: "my-secret",
			providedSecret:   "wrong-secret",
			expected:         false,
		},
		{
			name:             "empty provided secret",
			configuredSecret: "my-secret",
			providedSecret:   "",
			expected:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewWebhookHandler(nil, tt.configuredSecret, "pilot")
			result := handler.VerifySecret(tt.providedSecret)
			if result != tt.expected {
				t.Errorf("VerifySecret() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestWebhookHandlerHandle(t *testing.T) {
	// Create a mock server that returns work item details
	workItem := &WorkItem{
		ID:  42,
		Rev: 1,
		Fields: map[string]interface{}{
			"System.Title":        "Test Work Item",
			"System.Tags":         "pilot",
			"System.WorkItemType": "Bug",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workItem)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)

	handler := NewWebhookHandler(client, testutil.FakeAzureDevOpsWebhookSecret, "pilot")

	var processedWorkItem *WorkItem
	handler.OnWorkItem(func(ctx context.Context, wi *WorkItem) error {
		processedWorkItem = wi
		return nil
	})

	ctx := context.Background()

	// Test work item created event
	payload := &WebhookPayload{
		EventType:   WebhookEventWorkItemCreated,
		PublisherID: "tfs",
		Resource: map[string]interface{}{
			"id":  float64(42),
			"rev": float64(1),
			"fields": map[string]interface{}{
				"System.Title":        "Test Work Item",
				"System.Tags":         "pilot",
				"System.WorkItemType": "Bug",
			},
		},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if processedWorkItem == nil {
		t.Fatal("expected work item to be processed")
	}

	if processedWorkItem.ID != 42 {
		t.Errorf("expected work item ID 42, got %d", processedWorkItem.ID)
	}
}

func TestWebhookHandlerSkipsNonPilotItems(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", "http://unused")
	handler := NewWebhookHandler(client, "", "pilot")

	var processCalled bool
	handler.OnWorkItem(func(ctx context.Context, wi *WorkItem) error {
		processCalled = true
		return nil
	})

	ctx := context.Background()

	// Work item without pilot tag
	payload := &WebhookPayload{
		EventType:   WebhookEventWorkItemCreated,
		PublisherID: "tfs",
		Resource: map[string]interface{}{
			"id":  float64(42),
			"rev": float64(1),
			"fields": map[string]interface{}{
				"System.Title":        "Test Work Item",
				"System.Tags":         "other-tag",
				"System.WorkItemType": "Bug",
			},
		},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if processCalled {
		t.Error("expected work item NOT to be processed (no pilot tag)")
	}
}

func TestWebhookHandlerSkipsNonTFSPublisher(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", "http://unused")
	handler := NewWebhookHandler(client, "", "pilot")

	var processCalled bool
	handler.OnWorkItem(func(ctx context.Context, wi *WorkItem) error {
		processCalled = true
		return nil
	})

	ctx := context.Background()

	// Non-tfs publisher
	payload := &WebhookPayload{
		EventType:   WebhookEventWorkItemCreated,
		PublisherID: "other-publisher",
		Resource: map[string]interface{}{
			"id":  float64(42),
			"rev": float64(1),
			"fields": map[string]interface{}{
				"System.Title": "Test Work Item",
				"System.Tags":  "pilot",
			},
		},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if processCalled {
		t.Error("expected work item NOT to be processed (non-tfs publisher)")
	}
}

func TestWebhookHandlerSkipsNonAllowedWorkItemType(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", "http://unused")
	handler := NewWebhookHandler(client, "", "pilot")
	handler.SetWorkItemTypes([]string{"Bug", "Task"}) // Exclude "Feature"

	var processCalled bool
	handler.OnWorkItem(func(ctx context.Context, wi *WorkItem) error {
		processCalled = true
		return nil
	})

	ctx := context.Background()

	// Feature type (not in allowed list)
	payload := &WebhookPayload{
		EventType:   WebhookEventWorkItemCreated,
		PublisherID: "tfs",
		Resource: map[string]interface{}{
			"id":  float64(42),
			"rev": float64(1),
			"fields": map[string]interface{}{
				"System.Title":        "Test Feature",
				"System.Tags":         "pilot",
				"System.WorkItemType": "Feature",
			},
		},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if processCalled {
		t.Error("expected work item NOT to be processed (Feature not in allowed types)")
	}
}

func TestWebhookHandlerIgnoresUnknownEvents(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", "http://unused")
	handler := NewWebhookHandler(client, "", "pilot")

	ctx := context.Background()

	// Unknown event type
	payload := &WebhookPayload{
		EventType:   "unknown.event",
		PublisherID: "tfs",
		Resource:    map[string]interface{}{},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() should not error on unknown events: %v", err)
	}
}

func TestWebhookHandlerWorkItemUpdated(t *testing.T) {
	// Create a mock server that returns work item details
	workItem := &WorkItem{
		ID:  42,
		Rev: 2,
		Fields: map[string]interface{}{
			"System.Title":        "Updated Work Item",
			"System.Tags":         "pilot",
			"System.WorkItemType": "Task",
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workItem)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	handler := NewWebhookHandler(client, "", "pilot")

	var processedWorkItem *WorkItem
	handler.OnWorkItem(func(ctx context.Context, wi *WorkItem) error {
		processedWorkItem = wi
		return nil
	})

	ctx := context.Background()

	// Work item updated event with pilot tag added
	payload := &WebhookPayload{
		EventType:   WebhookEventWorkItemUpdated,
		PublisherID: "tfs",
		Resource: map[string]interface{}{
			"id":  float64(42),
			"rev": float64(2),
			"fields": map[string]interface{}{
				"System.Title":        "Updated Work Item",
				"System.Tags":         "pilot",
				"System.WorkItemType": "Task",
			},
		},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if processedWorkItem == nil {
		t.Fatal("expected work item to be processed")
	}
}

func TestWebhookHandlerSkipsAlreadyProcessedItems(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", "http://unused")
	handler := NewWebhookHandler(client, "", "pilot")

	var processCalled bool
	handler.OnWorkItem(func(ctx context.Context, wi *WorkItem) error {
		processCalled = true
		return nil
	})

	ctx := context.Background()

	// Work item already has pilot-in-progress tag
	payload := &WebhookPayload{
		EventType:   WebhookEventWorkItemUpdated,
		PublisherID: "tfs",
		Resource: map[string]interface{}{
			"id":  float64(42),
			"rev": float64(2),
			"fields": map[string]interface{}{
				"System.Title":        "Already Processing",
				"System.Tags":         "pilot; pilot-in-progress",
				"System.WorkItemType": "Bug",
			},
		},
	}

	err := handler.Handle(ctx, payload)
	if err != nil {
		t.Fatalf("Handle() error: %v", err)
	}

	if processCalled {
		t.Error("expected work item NOT to be processed (already in progress)")
	}
}
