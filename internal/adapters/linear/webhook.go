package linear

import (
	"context"
	"log/slog"

	"github.com/alekspetrov/pilot/internal/logging"
)

// WebhookEventType represents the type of webhook event
type WebhookEventType string

const (
	EventIssueCreated WebhookEventType = "Issue.create"
	EventIssueUpdated WebhookEventType = "Issue.update"
	EventIssueDeleted WebhookEventType = "Issue.delete"
	EventCommentAdded WebhookEventType = "Comment.create"
)

// WebhookPayload represents a Linear webhook payload
type WebhookPayload struct {
	Action     string                 `json:"action"`
	Type       string                 `json:"type"`
	Data       map[string]interface{} `json:"data"`
	URL        string                 `json:"url"`
	CreatedAt  string                 `json:"createdAt"`
	WebhookID  string                 `json:"webhookId"`
	WebhookTS  int64                  `json:"webhookTimestamp"`
}

// WebhookHandler handles Linear webhooks
type WebhookHandler struct {
	client     *Client
	pilotLabel string
	onIssue    func(context.Context, *Issue) error
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(client *Client, pilotLabel string) *WebhookHandler {
	return &WebhookHandler{
		client:     client,
		pilotLabel: pilotLabel,
	}
}

// OnIssue sets the callback for when a pilot-labeled issue is received
func (h *WebhookHandler) OnIssue(callback func(context.Context, *Issue) error) {
	h.onIssue = callback
}

// Handle processes a webhook payload
func (h *WebhookHandler) Handle(ctx context.Context, payload map[string]interface{}) error {
	action, _ := payload["action"].(string)
	eventType, _ := payload["type"].(string)

	logging.WithComponent("linear").Debug("Linear webhook", slog.String("action", action), slog.String("type", eventType))

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
		logging.WithComponent("linear").Debug("Issue does not have pilot label, skipping")
		return nil
	}

	// Fetch full issue details
	issueID, _ := data["id"].(string)
	issue, err := h.client.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}

	logging.WithComponent("linear").Info("Processing pilot issue", slog.String("identifier", issue.Identifier), slog.String("title", issue.Title))

	// Call the callback
	if h.onIssue != nil {
		return h.onIssue(ctx, issue)
	}

	return nil
}

// hasPilotLabel checks if the issue has the pilot label
func (h *WebhookHandler) hasPilotLabel(data map[string]interface{}) bool {
	labels, ok := data["labels"].([]interface{})
	if !ok {
		// Check labelIds instead
		labelIDs, ok := data["labelIds"].([]interface{})
		if !ok {
			return false
		}
		// For now, return true if there are any labels
		// In production, we'd check against actual pilot label ID
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
