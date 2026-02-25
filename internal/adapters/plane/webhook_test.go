package plane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func computeSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func makePayload(t *testing.T, event, action string, data WebhookWorkItemData) []byte {
	t.Helper()
	wp := map[string]interface{}{
		"event":        event,
		"action":       action,
		"webhook_id":   "wh-123",
		"workspace_id": "ws-456",
		"data":         data,
	}
	b, err := json.Marshal(wp)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}
	return b
}

func TestVerifySignature_Valid(t *testing.T) {
	secret := "test-webhook-secret"
	payload := []byte(`{"event":"issue","action":"created"}`)
	sig := computeSignature(secret, payload)

	if !VerifySignature(secret, payload, sig) {
		t.Error("expected valid signature to pass")
	}
}

func TestVerifySignature_Invalid(t *testing.T) {
	secret := "test-webhook-secret"
	payload := []byte(`{"event":"issue","action":"created"}`)

	if VerifySignature(secret, payload, "invalid-signature") {
		t.Error("expected invalid signature to fail")
	}
}

func TestVerifySignature_EmptySecret(t *testing.T) {
	payload := []byte(`{"event":"issue","action":"created"}`)
	if !VerifySignature("", payload, "anything") {
		t.Error("expected empty secret to pass (dev mode)")
	}
}

func TestVerifySignature_WrongPayload(t *testing.T) {
	secret := "test-webhook-secret"
	payload := []byte(`{"event":"issue","action":"created"}`)
	sig := computeSignature(secret, payload)

	tampered := []byte(`{"event":"issue","action":"deleted"}`)
	if VerifySignature(secret, tampered, sig) {
		t.Error("expected tampered payload to fail verification")
	}
}

func TestNewWebhookHandler(t *testing.T) {
	h := NewWebhookHandler("secret", "pilot-label-uuid", []string{"proj-1"})
	if h == nil {
		t.Fatal("NewWebhookHandler returned nil")
	}
	if h.secret != "secret" {
		t.Errorf("secret = %s, want 'secret'", h.secret)
	}
	if h.pilotLabel != "pilot-label-uuid" {
		t.Errorf("pilotLabel = %s, want 'pilot-label-uuid'", h.pilotLabel)
	}
	if len(h.projectIDs) != 1 {
		t.Errorf("projectIDs length = %d, want 1", len(h.projectIDs))
	}
	if h.onWorkItem != nil {
		t.Error("onWorkItem should be nil initially")
	}
}

func TestHandle_IssueCreated_WithPilotLabel(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	var received *WebhookWorkItemData
	h.OnWorkItem(func(_ context.Context, data *WebhookWorkItemData) error {
		received = data
		return nil
	})

	data := WebhookWorkItemData{
		ID:        "wi-1",
		Name:      "Fix login bug",
		StateID:   "state-1",
		LabelIDs:  []string{"other-label", "pilot-label-uuid"},
		ProjectID: "proj-1",
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if received == nil {
		t.Fatal("callback was not called")
	}
	if received.ID != "wi-1" {
		t.Errorf("work item ID = %s, want 'wi-1'", received.ID)
	}
	if received.Name != "Fix login bug" {
		t.Errorf("work item Name = %s, want 'Fix login bug'", received.Name)
	}
}

func TestHandle_IssueUpdated_WithPilotLabel(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{
		ID:       "wi-1",
		Name:     "Fix login bug",
		LabelIDs: []string{"pilot-label-uuid"},
	}
	payload := makePayload(t, "issue", "updated", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if !called {
		t.Error("callback should be called for updated action")
	}
}

func TestHandle_InvalidSignature(t *testing.T) {
	h := NewWebhookHandler("real-secret", "pilot-label-uuid", nil)

	payload := makePayload(t, "issue", "created", WebhookWorkItemData{
		ID:       "wi-1",
		LabelIDs: []string{"pilot-label-uuid"},
	})

	err := h.Handle(context.Background(), payload, "bad-sig")
	if err == nil {
		t.Fatal("expected error for invalid signature")
	}
	if err.Error() != "invalid webhook signature" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHandle_NoPilotLabel(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{
		ID:       "wi-1",
		LabelIDs: []string{"bug-label", "feature-label"},
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if called {
		t.Error("callback should not be called without pilot label")
	}
}

func TestHandle_WrongProject(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", []string{"allowed-project"})

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{
		ID:        "wi-1",
		LabelIDs:  []string{"pilot-label-uuid"},
		ProjectID: "other-project",
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if called {
		t.Error("callback should not be called for non-allowed project")
	}
}

func TestHandle_AllowedProject(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", []string{"proj-a", "proj-b"})

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{
		ID:        "wi-1",
		LabelIDs:  []string{"pilot-label-uuid"},
		ProjectID: "proj-b",
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if !called {
		t.Error("callback should be called for allowed project")
	}
}

func TestHandle_NoProjectFilter(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{
		ID:        "wi-1",
		LabelIDs:  []string{"pilot-label-uuid"},
		ProjectID: "any-project",
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if !called {
		t.Error("callback should be called when no project filter is set")
	}
}

func TestHandle_NonIssueEvent(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{ID: "wi-1", LabelIDs: []string{"pilot-label-uuid"}}
	payload := makePayload(t, "project", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if called {
		t.Error("callback should not be called for non-issue events")
	}
}

func TestHandle_DeletedAction(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{ID: "wi-1", LabelIDs: []string{"pilot-label-uuid"}}
	payload := makePayload(t, "issue", "deleted", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if called {
		t.Error("callback should not be called for deleted action")
	}
}

func TestHandle_CallbackError(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)

	expectedErr := errors.New("processing failed")
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		return expectedErr
	})

	data := WebhookWorkItemData{
		ID:       "wi-1",
		LabelIDs: []string{"pilot-label-uuid"},
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != expectedErr {
		t.Errorf("error = %v, want %v", err, expectedErr)
	}
}

func TestHandle_NoCallback(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "pilot-label-uuid", nil)
	// No callback set

	data := WebhookWorkItemData{
		ID:       "wi-1",
		LabelIDs: []string{"pilot-label-uuid"},
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
}

func TestHandle_InvalidJSON(t *testing.T) {
	h := NewWebhookHandler("", "pilot-label-uuid", nil)

	err := h.Handle(context.Background(), []byte(`{invalid`), "")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestHandle_EmptyPilotLabel(t *testing.T) {
	secret := "test-secret"
	h := NewWebhookHandler(secret, "", nil)

	var called bool
	h.OnWorkItem(func(_ context.Context, _ *WebhookWorkItemData) error {
		called = true
		return nil
	})

	data := WebhookWorkItemData{
		ID:       "wi-1",
		LabelIDs: []string{"some-label"},
	}
	payload := makePayload(t, "issue", "created", data)
	sig := computeSignature(secret, payload)

	err := h.Handle(context.Background(), payload, sig)
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if called {
		t.Error("callback should not be called when pilot label is empty")
	}
}

func TestIsAllowedProject(t *testing.T) {
	tests := []struct {
		name       string
		projectIDs []string
		projectID  string
		want       bool
	}{
		{"no filter allows all", nil, "any-project", true},
		{"empty filter allows all", []string{}, "any-project", true},
		{"matching project allowed", []string{"proj-1"}, "proj-1", true},
		{"non-matching project rejected", []string{"proj-1"}, "proj-2", false},
		{"multiple filters - match", []string{"proj-1", "proj-2"}, "proj-2", true},
		{"multiple filters - no match", []string{"proj-1", "proj-2"}, "proj-3", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewWebhookHandler("", "", tt.projectIDs)
			got := h.isAllowedProject(tt.projectID)
			if got != tt.want {
				t.Errorf("isAllowedProject(%s) = %v, want %v", tt.projectID, got, tt.want)
			}
		})
	}
}

func TestHasPilotLabel(t *testing.T) {
	tests := []struct {
		name       string
		pilotLabel string
		labelIDs   []string
		want       bool
	}{
		{"has pilot label", "pilot-uuid", []string{"other", "pilot-uuid"}, true},
		{"no pilot label", "pilot-uuid", []string{"bug", "feature"}, false},
		{"empty labels", "pilot-uuid", nil, false},
		{"empty pilot label config", "", []string{"some-label"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewWebhookHandler("", tt.pilotLabel, nil)
			got := h.hasPilotLabel(tt.labelIDs)
			if got != tt.want {
				t.Errorf("hasPilotLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebhookPayload_Unmarshal(t *testing.T) {
	raw := `{
		"event": "issue",
		"action": "created",
		"webhook_id": "wh-abc",
		"workspace_id": "ws-def",
		"data": {
			"id": "wi-123",
			"name": "Test item",
			"state": "state-uuid",
			"labels": ["lbl-1", "lbl-2"],
			"project": "proj-uuid",
			"sequence_id": 421
		}
	}`

	var wp WebhookPayload
	err := json.Unmarshal([]byte(raw), &wp)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if wp.Event != "issue" {
		t.Errorf("Event = %s, want 'issue'", wp.Event)
	}
	if wp.Action != "created" {
		t.Errorf("Action = %s, want 'created'", wp.Action)
	}
	if wp.WebhookID != "wh-abc" {
		t.Errorf("WebhookID = %s, want 'wh-abc'", wp.WebhookID)
	}
	if wp.WorkspaceID != "ws-def" {
		t.Errorf("WorkspaceID = %s, want 'ws-def'", wp.WorkspaceID)
	}

	var data WebhookWorkItemData
	if err := json.Unmarshal(wp.Data, &data); err != nil {
		t.Fatalf("failed to unmarshal data: %v", err)
	}
	if data.ID != "wi-123" {
		t.Errorf("data.ID = %s, want 'wi-123'", data.ID)
	}
	if data.SequenceID != 421 {
		t.Errorf("data.SequenceID = %d, want 421", data.SequenceID)
	}
	if len(data.LabelIDs) != 2 {
		t.Errorf("data.LabelIDs length = %d, want 2", len(data.LabelIDs))
	}
}
