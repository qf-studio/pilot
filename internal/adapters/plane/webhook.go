package plane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/alekspetrov/pilot/internal/logging"
)

// WebhookEventType represents the type of Plane webhook event.
type WebhookEventType string

const (
	EventIssue WebhookEventType = "issue"
)

// WebhookPayload represents a Plane.so webhook payload.
type WebhookPayload struct {
	Event       string          `json:"event"`
	Action      string          `json:"action"`
	WebhookID   string          `json:"webhook_id"`
	WorkspaceID string          `json:"workspace_id"`
	Data        json.RawMessage `json:"data"`
}

// WebhookWorkItemData represents the data field of a work item webhook event.
type WebhookWorkItemData struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	StateID    string   `json:"state"`
	LabelIDs   []string `json:"labels"`
	ProjectID  string   `json:"project"`
	SequenceID int      `json:"sequence_id"`
}

// WebhookHandler handles Plane.so webhooks.
type WebhookHandler struct {
	secret     string
	pilotLabel string
	projectIDs []string
	onWorkItem func(context.Context, *WebhookWorkItemData) error
}

// NewWebhookHandler creates a new Plane webhook handler.
func NewWebhookHandler(secret, pilotLabel string, projectIDs []string) *WebhookHandler {
	return &WebhookHandler{
		secret:     secret,
		pilotLabel: pilotLabel,
		projectIDs: projectIDs,
	}
}

// OnWorkItem sets the callback for when a pilot-labeled work item event is received.
func (h *WebhookHandler) OnWorkItem(callback func(context.Context, *WebhookWorkItemData) error) {
	h.onWorkItem = callback
}

// VerifySignature verifies the Plane webhook HMAC-SHA256 signature.
// Returns true if signature is valid, or if no secret is configured (development mode).
func VerifySignature(secret string, payload []byte, signature string) bool {
	if secret == "" {
		return true
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// Handle processes a Plane webhook payload.
func (h *WebhookHandler) Handle(ctx context.Context, payload []byte, signature string) error {
	log := logging.WithComponent("plane")

	// Verify signature
	if !VerifySignature(h.secret, payload, signature) {
		return fmt.Errorf("invalid webhook signature")
	}

	var wp WebhookPayload
	if err := json.Unmarshal(payload, &wp); err != nil {
		return fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	log.Debug("Plane webhook", slog.String("event", wp.Event), slog.String("action", wp.Action))

	// Only process issue events with created/updated actions
	if wp.Event != string(EventIssue) {
		return nil
	}
	if wp.Action != "created" && wp.Action != "updated" {
		return nil
	}

	var data WebhookWorkItemData
	if err := json.Unmarshal(wp.Data, &data); err != nil {
		return fmt.Errorf("failed to parse work item data: %w", err)
	}

	// Check project filter
	if !h.isAllowedProject(data.ProjectID) {
		log.Debug("Work item not in allowed project, skipping",
			slog.String("work_item_id", data.ID),
			slog.String("project_id", data.ProjectID))
		return nil
	}

	// Check pilot label
	if !h.hasPilotLabel(data.LabelIDs) {
		log.Debug("Work item does not have pilot label, skipping",
			slog.String("work_item_id", data.ID))
		return nil
	}

	log.Info("Processing pilot work item",
		slog.String("work_item_id", data.ID),
		slog.String("name", data.Name),
		slog.String("action", wp.Action))

	if h.onWorkItem != nil {
		return h.onWorkItem(ctx, &data)
	}

	return nil
}

// isAllowedProject checks if the work item belongs to an allowed project.
func (h *WebhookHandler) isAllowedProject(projectID string) bool {
	if len(h.projectIDs) == 0 {
		return true
	}
	for _, pid := range h.projectIDs {
		if pid == projectID {
			return true
		}
	}
	return false
}

// hasPilotLabel checks if any of the label UUIDs match the configured pilot label UUID.
func (h *WebhookHandler) hasPilotLabel(labelIDs []string) bool {
	if h.pilotLabel == "" {
		return false
	}
	for _, id := range labelIDs {
		if id == h.pilotLabel {
			return true
		}
	}
	return false
}
