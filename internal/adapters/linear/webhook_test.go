package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewWebhookHandler(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	if handler == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if handler.client != client {
		t.Error("handler.client does not match provided client")
	}
	if handler.pilotLabel != "pilot" {
		t.Errorf("handler.pilotLabel = %s, want 'pilot'", handler.pilotLabel)
	}
	if handler.onIssue != nil {
		t.Error("handler.onIssue should be nil initially")
	}
}

func TestOnIssue(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	var callbackSet bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackSet = true
		return nil
	})

	if handler.onIssue == nil {
		t.Error("handler.onIssue should not be nil after OnIssue called")
	}

	// Invoke callback to verify it was set correctly
	_ = handler.onIssue(context.Background(), &Issue{})
	if !callbackSet {
		t.Error("callback was not invoked")
	}
}

func TestHandle_IssueCreated_WithPilotLabel(t *testing.T) {
	// Create mock server for fetching issue details
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"id":          "issue-123",
					"identifier":  "PROJ-42",
					"title":       "Test Issue",
					"description": "Test description",
					"priority":    2,
					"state": map[string]interface{}{
						"id":   "state-1",
						"name": "In Progress",
						"type": "started",
					},
					"labels": map[string]interface{}{
						"nodes": []interface{}{
							map[string]interface{}{"id": "label-1", "name": "pilot"},
						},
					},
					"assignee": nil,
					"project":  nil,
					"team": map[string]interface{}{
						"id":   "team-1",
						"name": "Engineering",
						"key":  "ENG",
					},
					"createdAt": "2024-01-15T10:00:00Z",
					"updatedAt": "2024-01-15T10:00:00Z",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Create a test handler with mock
	testHandler := &testWebhookHandler{
		pilotLabel: "pilot",
		serverURL:  server.URL,
	}

	var receivedIssue *Issue
	testHandler.onIssue = func(ctx context.Context, issue *Issue) error {
		receivedIssue = issue
		return nil
	}

	payload := map[string]interface{}{
		"action": "create",
		"type":   "Issue",
		"data": map[string]interface{}{
			"id": "issue-123",
			"labels": []interface{}{
				map[string]interface{}{"id": "label-1", "name": "pilot"},
			},
		},
	}

	err := testHandler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if receivedIssue == nil {
		t.Fatal("OnIssue callback was not called")
	}
	if receivedIssue.Identifier != "PROJ-42" {
		t.Errorf("issue.Identifier = %s, want PROJ-42", receivedIssue.Identifier)
	}
}

func TestHandle_IssueCreated_NoPilotLabel(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	payload := map[string]interface{}{
		"action": "create",
		"type":   "Issue",
		"data": map[string]interface{}{
			"id": "issue-123",
			"labels": []interface{}{
				map[string]interface{}{"id": "label-1", "name": "bug"},
				map[string]interface{}{"id": "label-2", "name": "enhancement"},
			},
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called for issues without pilot label")
	}
}

func TestHandle_IssueUpdated(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	// Update events should be ignored
	payload := map[string]interface{}{
		"action": "update",
		"type":   "Issue",
		"data": map[string]interface{}{
			"id": "issue-123",
			"labels": []interface{}{
				map[string]interface{}{"id": "label-1", "name": "pilot"},
			},
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called for update events")
	}
}

func TestHandle_CommentEvent(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	payload := map[string]interface{}{
		"action": "create",
		"type":   "Comment",
		"data": map[string]interface{}{
			"id":   "comment-123",
			"body": "Test comment",
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called for Comment events")
	}
}

func TestHandle_IssueDeleted(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	payload := map[string]interface{}{
		"action": "remove",
		"type":   "Issue",
		"data": map[string]interface{}{
			"id": "issue-123",
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called for remove events")
	}
}

func TestHandle_NoCallback(t *testing.T) {
	// Create mock server to return an issue
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"id":          "issue-123",
					"identifier":  "PROJ-42",
					"title":       "Test Issue",
					"description": "",
					"priority":    0,
					"state":       map[string]interface{}{"id": "s1", "name": "Todo", "type": "unstarted"},
					"labels":      map[string]interface{}{"nodes": []interface{}{}},
					"team":        map[string]interface{}{"id": "t1", "name": "Eng", "key": "ENG"},
					"createdAt":   "2024-01-15T10:00:00Z",
					"updatedAt":   "2024-01-15T10:00:00Z",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	testHandler := &testWebhookHandler{
		pilotLabel: "pilot",
		serverURL:  server.URL,
		onIssue:    nil, // No callback
	}

	payload := map[string]interface{}{
		"action": "create",
		"type":   "Issue",
		"data": map[string]interface{}{
			"id":       "issue-123",
			"labelIds": []interface{}{"label-1"}, // Has label IDs so it passes hasPilotLabel
		},
	}

	// Should not panic or error
	err := testHandler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
}

func TestHandle_CallbackError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"data": map[string]interface{}{
				"issue": map[string]interface{}{
					"id":          "issue-123",
					"identifier":  "PROJ-42",
					"title":       "Test Issue",
					"description": "",
					"priority":    0,
					"state":       map[string]interface{}{"id": "s1", "name": "Todo", "type": "unstarted"},
					"labels":      map[string]interface{}{"nodes": []interface{}{}},
					"team":        map[string]interface{}{"id": "t1", "name": "Eng", "key": "ENG"},
					"createdAt":   "2024-01-15T10:00:00Z",
					"updatedAt":   "2024-01-15T10:00:00Z",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	expectedErr := errors.New("callback error")
	testHandler := &testWebhookHandler{
		pilotLabel: "pilot",
		serverURL:  server.URL,
		onIssue: func(ctx context.Context, issue *Issue) error {
			return expectedErr
		},
	}

	payload := map[string]interface{}{
		"action": "create",
		"type":   "Issue",
		"data": map[string]interface{}{
			"id": "issue-123",
			"labels": []interface{}{
				map[string]interface{}{"id": "label-1", "name": "pilot"},
			},
		},
	}

	err := testHandler.Handle(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if err != expectedErr {
		t.Errorf("error = %v, want %v", err, expectedErr)
	}
}

func TestHandle_InvalidDataType(t *testing.T) {
	client := NewClient("test-api-key")
	handler := NewWebhookHandler(client, "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	// data is not a map
	payload := map[string]interface{}{
		"action": "create",
		"type":   "Issue",
		"data":   "invalid",
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called when data is invalid")
	}
}

func TestHandle_MissingActionOrType(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]interface{}
	}{
		{
			name: "missing action",
			payload: map[string]interface{}{
				"type": "Issue",
				"data": map[string]interface{}{"id": "123"},
			},
		},
		{
			name: "missing type",
			payload: map[string]interface{}{
				"action": "create",
				"data":   map[string]interface{}{"id": "123"},
			},
		},
		{
			name:    "empty payload",
			payload: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-api-key")
			handler := NewWebhookHandler(client, "pilot")

			var callbackCalled bool
			handler.OnIssue(func(ctx context.Context, issue *Issue) error {
				callbackCalled = true
				return nil
			})

			err := handler.Handle(context.Background(), tt.payload)
			if err != nil {
				t.Fatalf("Handle failed: %v", err)
			}

			if callbackCalled {
				t.Error("OnIssue callback should not be called for incomplete payloads")
			}
		})
	}
}

func TestHasPilotLabel_WithLabels(t *testing.T) {
	tests := []struct {
		name       string
		pilotLabel string
		data       map[string]interface{}
		want       bool
	}{
		{
			name:       "has pilot label",
			pilotLabel: "pilot",
			data: map[string]interface{}{
				"labels": []interface{}{
					map[string]interface{}{"id": "1", "name": "bug"},
					map[string]interface{}{"id": "2", "name": "pilot"},
				},
			},
			want: true,
		},
		{
			name:       "no pilot label",
			pilotLabel: "pilot",
			data: map[string]interface{}{
				"labels": []interface{}{
					map[string]interface{}{"id": "1", "name": "bug"},
					map[string]interface{}{"id": "2", "name": "enhancement"},
				},
			},
			want: false,
		},
		{
			name:       "empty labels",
			pilotLabel: "pilot",
			data: map[string]interface{}{
				"labels": []interface{}{},
			},
			want: false,
		},
		{
			name:       "custom pilot label",
			pilotLabel: "ai-task",
			data: map[string]interface{}{
				"labels": []interface{}{
					map[string]interface{}{"id": "1", "name": "ai-task"},
				},
			},
			want: true,
		},
		{
			name:       "label without name field",
			pilotLabel: "pilot",
			data: map[string]interface{}{
				"labels": []interface{}{
					map[string]interface{}{"id": "1"},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-api-key")
			handler := NewWebhookHandler(client, tt.pilotLabel)

			got := handler.hasPilotLabel(tt.data)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasPilotLabel_WithLabelIds(t *testing.T) {
	tests := []struct {
		name       string
		pilotLabel string
		data       map[string]interface{}
		want       bool
	}{
		{
			name:       "has label IDs (assumes pilot label present)",
			pilotLabel: "pilot",
			data: map[string]interface{}{
				"labelIds": []interface{}{"label-1", "label-2"},
			},
			want: true,
		},
		{
			name:       "empty label IDs",
			pilotLabel: "pilot",
			data: map[string]interface{}{
				"labelIds": []interface{}{},
			},
			want: false,
		},
		{
			name:       "no labels or labelIds",
			pilotLabel: "pilot",
			data:       map[string]interface{}{},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-api-key")
			handler := NewWebhookHandler(client, tt.pilotLabel)

			got := handler.hasPilotLabel(tt.data)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasPilotLabel_InvalidLabelFormat(t *testing.T) {
	tests := []struct {
		name string
		data map[string]interface{}
		want bool
	}{
		{
			name: "labels not an array",
			data: map[string]interface{}{
				"labels": "invalid",
			},
			want: false,
		},
		{
			name: "labels contains non-map elements",
			data: map[string]interface{}{
				"labels": []interface{}{
					"string-label",
					123,
				},
			},
			want: false,
		},
		{
			name: "labelIds not an array",
			data: map[string]interface{}{
				"labelIds": "invalid",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient("test-api-key")
			handler := NewWebhookHandler(client, "pilot")

			got := handler.hasPilotLabel(tt.data)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebhookEventTypes(t *testing.T) {
	tests := []struct {
		eventType WebhookEventType
		expected  string
	}{
		{EventIssueCreated, "Issue.create"},
		{EventIssueUpdated, "Issue.update"},
		{EventIssueDeleted, "Issue.delete"},
		{EventCommentAdded, "Comment.create"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			if string(tt.eventType) != tt.expected {
				t.Errorf("event type = %s, want %s", tt.eventType, tt.expected)
			}
		})
	}
}

func TestWebhookPayloadStructure(t *testing.T) {
	// Test that WebhookPayload can be properly unmarshaled
	jsonPayload := `{
		"action": "create",
		"type": "Issue",
		"data": {"id": "issue-123", "title": "Test"},
		"url": "https://linear.app/team/PROJ-42",
		"createdAt": "2024-01-15T10:00:00Z",
		"webhookId": "webhook-123",
		"webhookTimestamp": 1705318800000
	}`

	var payload WebhookPayload
	err := json.Unmarshal([]byte(jsonPayload), &payload)
	if err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.Action != "create" {
		t.Errorf("payload.Action = %s, want 'create'", payload.Action)
	}
	if payload.Type != "Issue" {
		t.Errorf("payload.Type = %s, want 'Issue'", payload.Type)
	}
	if payload.URL != "https://linear.app/team/PROJ-42" {
		t.Errorf("payload.URL = %s, want 'https://linear.app/team/PROJ-42'", payload.URL)
	}
	if payload.WebhookID != "webhook-123" {
		t.Errorf("payload.WebhookID = %s, want 'webhook-123'", payload.WebhookID)
	}
	if payload.WebhookTS != 1705318800000 {
		t.Errorf("payload.WebhookTS = %d, want 1705318800000", payload.WebhookTS)
	}

	// Verify data field
	if payload.Data["id"] != "issue-123" {
		t.Errorf("payload.Data[id] = %v, want 'issue-123'", payload.Data["id"])
	}
}

// testWebhookHandler is a test helper that mimics WebhookHandler behavior
// but allows injecting a test server URL for fetching issues
type testWebhookHandler struct {
	pilotLabel string
	serverURL  string
	onIssue    func(context.Context, *Issue) error
}

func (h *testWebhookHandler) Handle(ctx context.Context, payload map[string]interface{}) error {
	action, _ := payload["action"].(string)
	eventType, _ := payload["type"].(string)

	// Only process issue creation events
	if action != "create" || eventType != "Issue" {
		return nil
	}

	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Check if issue has pilot label
	if !h.hasPilotLabel(data) {
		return nil
	}

	// Fetch full issue details from mock server
	issueID, _ := data["id"].(string)
	issue, err := h.getIssue(ctx, issueID)
	if err != nil {
		return err
	}

	// Call the callback
	if h.onIssue != nil {
		return h.onIssue(ctx, issue)
	}

	return nil
}

func (h *testWebhookHandler) hasPilotLabel(data map[string]interface{}) bool {
	labels, ok := data["labels"].([]interface{})
	if !ok {
		labelIDs, ok := data["labelIds"].([]interface{})
		if !ok {
			return false
		}
		return len(labelIDs) > 0
	}

	for _, label := range labels {
		labelMap, ok := label.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := labelMap["name"].(string)
		if name == h.pilotLabel {
			return true
		}
	}

	return false
}

func (h *testWebhookHandler) getIssue(ctx context.Context, id string) (*Issue, error) {
	client := newTestableClient(h.serverURL, "test-api-key")
	return client.getIssue(ctx, id)
}
