package gitlab

import (
	"context"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestVerifyToken(t *testing.T) {
	tests := []struct {
		name      string
		secret    string
		token     string
		wantValid bool
	}{
		{
			name:      "valid token",
			secret:    testutil.FakeGitLabWebhookSecret,
			token:     testutil.FakeGitLabWebhookSecret,
			wantValid: true,
		},
		{
			name:      "invalid token",
			secret:    testutil.FakeGitLabWebhookSecret,
			token:     "wrong-token",
			wantValid: false,
		},
		{
			name:      "empty token",
			secret:    testutil.FakeGitLabWebhookSecret,
			token:     "",
			wantValid: false,
		},
		{
			name:      "no secret configured - development mode",
			secret:    "",
			token:     "any-token",
			wantValid: true,
		},
		{
			name:      "no secret configured - empty token",
			secret:    "",
			token:     "",
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(testutil.FakeGitLabToken, "namespace/project")
			handler := NewWebhookHandler(client, tt.secret, "pilot")

			got := handler.VerifyToken(tt.token)
			if got != tt.wantValid {
				t.Errorf("VerifyToken() = %v, want %v", got, tt.wantValid)
			}
		})
	}
}

func TestNewWebhookHandler(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	handler := NewWebhookHandler(client, testutil.FakeGitLabWebhookSecret, "pilot")

	if handler == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if handler.webhookSecret != testutil.FakeGitLabWebhookSecret {
		t.Errorf("handler.webhookSecret = %s, want %s", handler.webhookSecret, testutil.FakeGitLabWebhookSecret)
	}
	if handler.pilotLabel != "pilot" {
		t.Errorf("handler.pilotLabel = %s, want pilot", handler.pilotLabel)
	}
}

func TestWebhookHandler_OnIssue(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	handler := NewWebhookHandler(client, testutil.FakeGitLabWebhookSecret, "pilot")

	handler.OnIssue(func(ctx context.Context, issue *Issue, project *Project) error {
		return nil
	})

	// Verify callback was registered (handler.onIssue should be non-nil)
	if handler.onIssue == nil {
		t.Error("OnIssue callback was not registered")
	}
}

func TestWebhookHandler_HasPilotLabel(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	handler := NewWebhookHandler(client, testutil.FakeGitLabWebhookSecret, "pilot")

	tests := []struct {
		name   string
		labels []*WebhookLabel
		want   bool
	}{
		{
			name: "has pilot label",
			labels: []*WebhookLabel{
				{ID: 1, Title: "bug"},
				{ID: 2, Title: "pilot"},
			},
			want: true,
		},
		{
			name: "does not have pilot label",
			labels: []*WebhookLabel{
				{ID: 1, Title: "bug"},
				{ID: 2, Title: "enhancement"},
			},
			want: false,
		},
		{
			name:   "empty labels",
			labels: []*WebhookLabel{},
			want:   false,
		},
		{
			name:   "nil labels",
			labels: nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handler.hasPilotLabel(tt.labels)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebhookHandler_WasLabelAdded(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	handler := NewWebhookHandler(client, testutil.FakeGitLabWebhookSecret, "pilot")

	tests := []struct {
		name        string
		labelChange *LabelChange
		want        bool
	}{
		{
			name: "pilot label was added",
			labelChange: &LabelChange{
				Previous: []*WebhookLabel{
					{ID: 1, Title: "bug"},
				},
				Current: []*WebhookLabel{
					{ID: 1, Title: "bug"},
					{ID: 2, Title: "pilot"},
				},
			},
			want: true,
		},
		{
			name: "pilot label was already present",
			labelChange: &LabelChange{
				Previous: []*WebhookLabel{
					{ID: 1, Title: "pilot"},
				},
				Current: []*WebhookLabel{
					{ID: 1, Title: "pilot"},
					{ID: 2, Title: "bug"},
				},
			},
			want: false,
		},
		{
			name: "pilot label was removed",
			labelChange: &LabelChange{
				Previous: []*WebhookLabel{
					{ID: 1, Title: "pilot"},
				},
				Current: []*WebhookLabel{
					{ID: 2, Title: "bug"},
				},
			},
			want: false,
		},
		{
			name: "pilot label not present in either",
			labelChange: &LabelChange{
				Previous: []*WebhookLabel{
					{ID: 1, Title: "bug"},
				},
				Current: []*WebhookLabel{
					{ID: 1, Title: "bug"},
					{ID: 2, Title: "enhancement"},
				},
			},
			want: false,
		},
		{
			name: "empty previous labels",
			labelChange: &LabelChange{
				Previous: []*WebhookLabel{},
				Current: []*WebhookLabel{
					{ID: 1, Title: "pilot"},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := handler.wasLabelAdded(tt.labelChange)
			if got != tt.want {
				t.Errorf("wasLabelAdded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebhookEventConstants(t *testing.T) {
	tests := []struct {
		constant string
		expected string
	}{
		{WebhookEventIssue, "Issue Hook"},
		{WebhookEventMergeRequest, "Merge Request Hook"},
		{WebhookEventPipeline, "Pipeline Hook"},
		{WebhookEventNote, "Note Hook"},
	}

	for _, tt := range tests {
		if tt.constant != tt.expected {
			t.Errorf("constant = %s, want %s", tt.constant, tt.expected)
		}
	}
}
