package linear

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestNewWebhookHandler(t *testing.T) {
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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

func TestNewWebhookHandler_WithProjectIDs(t *testing.T) {
	client := NewClient(testutil.FakeLinearAPIKey)
	projectIDs := []string{"proj-1", "proj-2"}
	handler := NewWebhookHandler(client, "pilot", projectIDs)

	if handler == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if len(handler.projectIDs) != 2 {
		t.Errorf("handler.projectIDs length = %d, want 2", len(handler.projectIDs))
	}
	if handler.projectIDs[0] != "proj-1" {
		t.Errorf("handler.projectIDs[0] = %s, want 'proj-1'", handler.projectIDs[0])
	}
}

func TestOnIssue(t *testing.T) {
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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
	client := NewClient(testutil.FakeLinearAPIKey)
	handler := NewWebhookHandler(client, "pilot", nil)

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
			client := NewClient(testutil.FakeLinearAPIKey)
			handler := NewWebhookHandler(client, "pilot", nil)

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
			client := NewClient(testutil.FakeLinearAPIKey)
			handler := NewWebhookHandler(client, tt.pilotLabel, nil)

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
			client := NewClient(testutil.FakeLinearAPIKey)
			handler := NewWebhookHandler(client, tt.pilotLabel, nil)

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
			client := NewClient(testutil.FakeLinearAPIKey)
			handler := NewWebhookHandler(client, "pilot", nil)

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

func TestIsAllowedProject(t *testing.T) {
	tests := []struct {
		name       string
		projectIDs []string
		issue      *Issue
		want       bool
	}{
		{
			name:       "no filter allows all",
			projectIDs: nil,
			issue:      &Issue{ID: "issue-1"},
			want:       true,
		},
		{
			name:       "empty filter allows all",
			projectIDs: []string{},
			issue:      &Issue{ID: "issue-1"},
			want:       true,
		},
		{
			name:       "filter rejects nil project",
			projectIDs: []string{"proj-1"},
			issue:      &Issue{ID: "issue-1", Project: nil},
			want:       false,
		},
		{
			name:       "filter allows matching project",
			projectIDs: []string{"proj-1"},
			issue:      &Issue{ID: "issue-1", Project: &Project{ID: "proj-1", Name: "Project 1"}},
			want:       true,
		},
		{
			name:       "filter rejects non-matching project",
			projectIDs: []string{"proj-1"},
			issue:      &Issue{ID: "issue-1", Project: &Project{ID: "proj-2", Name: "Project 2"}},
			want:       false,
		},
		{
			name:       "multiple filters allow any matching",
			projectIDs: []string{"proj-1", "proj-2", "proj-3"},
			issue:      &Issue{ID: "issue-1", Project: &Project{ID: "proj-2", Name: "Project 2"}},
			want:       true,
		},
		{
			name:       "multiple filters reject non-matching",
			projectIDs: []string{"proj-1", "proj-2"},
			issue:      &Issue{ID: "issue-1", Project: &Project{ID: "proj-3", Name: "Project 3"}},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testutil.FakeLinearAPIKey)
			handler := NewWebhookHandler(client, "pilot", tt.projectIDs)

			got := handler.isAllowedProject(tt.issue)
			if got != tt.want {
				t.Errorf("isAllowedProject() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandle_IssueCreated_ProjectFilter(t *testing.T) {
	// Create mock server that returns issue with project
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
					"project": map[string]interface{}{
						"id":   "allowed-project-id",
						"name": "Allowed Project",
					},
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

	testHandler := &testWebhookHandler{
		pilotLabel: "pilot",
		serverURL:  server.URL,
		projectIDs: []string{"allowed-project-id"},
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
		t.Fatal("OnIssue callback was not called for allowed project")
	}
	if receivedIssue.Project == nil || receivedIssue.Project.ID != "allowed-project-id" {
		t.Error("issue should have the allowed project")
	}
}

func TestHandle_IssueCreated_ProjectFilter_Rejected(t *testing.T) {
	// Create mock server that returns issue with different project
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
					"project": map[string]interface{}{
						"id":   "other-project-id",
						"name": "Other Project",
					},
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

	testHandler := &testWebhookHandler{
		pilotLabel: "pilot",
		serverURL:  server.URL,
		projectIDs: []string{"allowed-project-id"},
	}

	var callbackCalled bool
	testHandler.onIssue = func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
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

	if callbackCalled {
		t.Error("OnIssue callback should not be called for non-allowed project")
	}
}

// testWebhookHandler is a test helper that mimics WebhookHandler behavior
// but allows injecting a test server URL for fetching issues
type testWebhookHandler struct {
	pilotLabel string
	serverURL  string
	projectIDs []string
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

	// Check project filter
	if !h.isAllowedProject(issue) {
		return nil
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
	client := newTestableClient(h.serverURL, testutil.FakeLinearAPIKey)
	return client.getIssue(ctx, id)
}

func (h *testWebhookHandler) isAllowedProject(issue *Issue) bool {
	if len(h.projectIDs) == 0 {
		return true
	}
	if issue.Project == nil {
		return false
	}
	for _, pid := range h.projectIDs {
		if issue.Project.ID == pid {
			return true
		}
	}
	return false
}
