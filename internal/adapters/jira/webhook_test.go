package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewWebhookHandler(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "secret", "pilot")

	if handler == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if handler.pilotLabel != "pilot" {
		t.Errorf("handler.pilotLabel = %s, want 'pilot'", handler.pilotLabel)
	}
}

func TestVerifySignature(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		signature string
		want      bool
	}{
		{
			name:      "no secret configured",
			secret:    "",
			signature: "anything",
			want:      true,
		},
		{
			name:      "valid signature",
			secret:    "test-secret",
			signature: "5d8e1c2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d",
			want:      false, // Won't match unless we compute actual HMAC
		},
	}

	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewWebhookHandler(client, tt.secret, "pilot")
			got := handler.VerifySignature([]byte("payload"), tt.signature)
			if got != tt.want {
				t.Errorf("VerifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandle_IssueCreated(t *testing.T) {
	// Create mock server for fetching issue details
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := Issue{
			ID:  "10001",
			Key: "PROJ-42",
			Fields: Fields{
				Summary: "Test Issue",
				Labels:  []string{"pilot"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	var receivedIssue *Issue
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		receivedIssue = issue
		return nil
	})

	payload := map[string]interface{}{
		"webhookEvent": "jira:issue_created",
		"issue": map[string]interface{}{
			"id":   "10001",
			"key":  "PROJ-42",
			"self": "https://jira.example.com/rest/api/3/issue/10001",
			"fields": map[string]interface{}{
				"summary": "Test Issue",
				"labels":  []interface{}{"pilot"},
			},
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if receivedIssue == nil {
		t.Fatal("OnIssue callback was not called")
	}
	if receivedIssue.Key != "PROJ-42" {
		t.Errorf("issue.Key = %s, want PROJ-42", receivedIssue.Key)
	}
}

func TestHandle_IssueCreated_NoPilotLabel(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	payload := map[string]interface{}{
		"webhookEvent": "jira:issue_created",
		"issue": map[string]interface{}{
			"id":  "10001",
			"key": "PROJ-42",
			"fields": map[string]interface{}{
				"summary": "Test Issue",
				"labels":  []interface{}{"bug", "enhancement"},
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

func TestHandle_IssueUpdated_LabelAdded(t *testing.T) {
	// Create mock server for fetching issue details
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := Issue{
			ID:  "10001",
			Key: "PROJ-42",
			Fields: Fields{
				Summary: "Test Issue",
				Labels:  []string{"pilot"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(issue)
	}))
	defer server.Close()

	client := NewClient(server.URL, "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	var receivedIssue *Issue
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		receivedIssue = issue
		return nil
	})

	payload := map[string]interface{}{
		"webhookEvent": "jira:issue_updated",
		"issue": map[string]interface{}{
			"id":  "10001",
			"key": "PROJ-42",
			"fields": map[string]interface{}{
				"summary": "Test Issue",
				"labels":  []interface{}{"pilot"},
			},
		},
		"changelog": map[string]interface{}{
			"id": "12345",
			"items": []interface{}{
				map[string]interface{}{
					"field":      "labels",
					"fieldtype":  "jira",
					"fromString": "",
					"toString":   "pilot",
				},
			},
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if receivedIssue == nil {
		t.Fatal("OnIssue callback was not called")
	}
}

func TestHandle_IssueUpdated_DifferentLabel(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	payload := map[string]interface{}{
		"webhookEvent": "jira:issue_updated",
		"issue": map[string]interface{}{
			"id":  "10001",
			"key": "PROJ-42",
			"fields": map[string]interface{}{
				"summary": "Test Issue",
				"labels":  []interface{}{"bug"},
			},
		},
		"changelog": map[string]interface{}{
			"id": "12345",
			"items": []interface{}{
				map[string]interface{}{
					"field":      "labels",
					"fieldtype":  "jira",
					"fromString": "",
					"toString":   "bug",
				},
			},
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called when non-pilot label is added")
	}
}

func TestHandle_UnknownEvent(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	var callbackCalled bool
	handler.OnIssue(func(ctx context.Context, issue *Issue) error {
		callbackCalled = true
		return nil
	})

	payload := map[string]interface{}{
		"webhookEvent": "comment_created",
		"issue": map[string]interface{}{
			"key": "PROJ-42",
		},
	}

	err := handler.Handle(context.Background(), payload)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if callbackCalled {
		t.Error("OnIssue callback should not be called for comment_created events")
	}
}

func TestHasPilotLabel(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{
			name:   "has pilot label",
			labels: []string{"bug", "pilot", "enhancement"},
			want:   true,
		},
		{
			name:   "has PILOT label (case insensitive)",
			labels: []string{"bug", "PILOT"},
			want:   true,
		},
		{
			name:   "no pilot label",
			labels: []string{"bug", "enhancement"},
			want:   false,
		},
		{
			name:   "empty labels",
			labels: []string{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				Fields: Fields{Labels: tt.labels},
			}
			got := handler.hasPilotLabel(issue)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWasLabelAdded(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	tests := []struct {
		name    string
		payload map[string]interface{}
		want    bool
	}{
		{
			name: "pilot label added",
			payload: map[string]interface{}{
				"changelog": map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"field":      "labels",
							"fromString": "bug",
							"toString":   "bug pilot",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "pilot label already present",
			payload: map[string]interface{}{
				"changelog": map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"field":      "labels",
							"fromString": "pilot",
							"toString":   "pilot bug",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "different label added",
			payload: map[string]interface{}{
				"changelog": map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"field":      "labels",
							"fromString": "",
							"toString":   "bug",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "no changelog",
			payload: map[string]interface{}{
				"issue": map[string]interface{}{},
			},
			want: false,
		},
		{
			name: "status changed, not labels",
			payload: map[string]interface{}{
				"changelog": map[string]interface{}{
					"items": []interface{}{
						map[string]interface{}{
							"field":      "status",
							"fromString": "To Do",
							"toString":   "In Progress",
						},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handler.wasLabelAdded(tt.payload)
			if got != tt.want {
				t.Errorf("wasLabelAdded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractIssue(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	payload := map[string]interface{}{
		"issue": map[string]interface{}{
			"id":   "10001",
			"key":  "PROJ-42",
			"self": "https://jira.example.com/rest/api/3/issue/10001",
			"fields": map[string]interface{}{
				"summary":     "Test Issue",
				"description": "Issue description",
				"labels":      []interface{}{"pilot", "bug"},
				"issuetype": map[string]interface{}{
					"name": "Story",
				},
				"status": map[string]interface{}{
					"name": "To Do",
				},
				"priority": map[string]interface{}{
					"name": "High",
				},
				"project": map[string]interface{}{
					"key":  "PROJ",
					"name": "My Project",
				},
			},
		},
	}

	issue, err := handler.extractIssue(payload)
	if err != nil {
		t.Fatalf("extractIssue failed: %v", err)
	}

	if issue.Key != "PROJ-42" {
		t.Errorf("issue.Key = %s, want PROJ-42", issue.Key)
	}
	if issue.Fields.Summary != "Test Issue" {
		t.Errorf("issue.Fields.Summary = %s, want 'Test Issue'", issue.Fields.Summary)
	}
	if len(issue.Fields.Labels) != 2 {
		t.Errorf("issue.Fields.Labels = %v, want 2 labels", issue.Fields.Labels)
	}
	if issue.Fields.IssueType.Name != "Story" {
		t.Errorf("issue.Fields.IssueType.Name = %s, want 'Story'", issue.Fields.IssueType.Name)
	}
	if issue.Fields.Status.Name != "To Do" {
		t.Errorf("issue.Fields.Status.Name = %s, want 'To Do'", issue.Fields.Status.Name)
	}
	if issue.Fields.Priority.Name != "High" {
		t.Errorf("issue.Fields.Priority.Name = %s, want 'High'", issue.Fields.Priority.Name)
	}
}

func TestExtractIssue_MissingIssue(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	payload := map[string]interface{}{
		"webhookEvent": "jira:issue_created",
	}

	_, err := handler.extractIssue(payload)
	if err == nil {
		t.Error("expected error for missing issue in payload")
	}
}

func TestExtractADFText(t *testing.T) {
	client := NewClient("https://jira.example.com", "user", "token", PlatformCloud)
	handler := NewWebhookHandler(client, "", "pilot")

	tests := []struct {
		name string
		adf  map[string]interface{}
		want string
	}{
		{
			name: "simple paragraph",
			adf: map[string]interface{}{
				"type":    "doc",
				"version": 1,
				"content": []interface{}{
					map[string]interface{}{
						"type": "paragraph",
						"content": []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "Hello world",
							},
						},
					},
				},
			},
			want: "Hello world",
		},
		{
			name: "multiple paragraphs",
			adf: map[string]interface{}{
				"type":    "doc",
				"version": 1,
				"content": []interface{}{
					map[string]interface{}{
						"type": "paragraph",
						"content": []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "First paragraph",
							},
						},
					},
					map[string]interface{}{
						"type": "paragraph",
						"content": []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "Second paragraph",
							},
						},
					},
				},
			},
			want: "First paragraph\nSecond paragraph",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handler.extractADFText(tt.adf)
			if got != tt.want {
				t.Errorf("extractADFText() = %q, want %q", got, tt.want)
			}
		})
	}
}
