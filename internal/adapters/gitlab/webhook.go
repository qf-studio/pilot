package gitlab

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"

	"github.com/alekspetrov/pilot/internal/logging"
)

// WebhookHandler handles GitLab webhooks
type WebhookHandler struct {
	client        *Client
	webhookSecret string
	pilotLabel    string
	onIssue       func(context.Context, *Issue, *Project) error
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(client *Client, webhookSecret, pilotLabel string) *WebhookHandler {
	return &WebhookHandler{
		client:        client,
		webhookSecret: webhookSecret,
		pilotLabel:    pilotLabel,
	}
}

// OnIssue sets the callback for when a pilot-labeled issue is received
func (h *WebhookHandler) OnIssue(callback func(context.Context, *Issue, *Project) error) {
	h.onIssue = callback
}

// VerifyToken verifies the GitLab webhook token
// GitLab uses simple token comparison via X-Gitlab-Token header (not HMAC)
func (h *WebhookHandler) VerifyToken(token string) bool {
	if h.webhookSecret == "" {
		// No secret configured, skip verification (development mode)
		return true
	}

	// Use constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.webhookSecret)) == 1
}

// Handle processes a webhook payload
func (h *WebhookHandler) Handle(ctx context.Context, eventType string, payload *IssueWebhookPayload) error {
	if payload.ObjectAttributes == nil {
		return nil
	}

	action := payload.ObjectAttributes.Action
	logging.WithComponent("gitlab").Debug("GitLab webhook",
		slog.String("event", eventType),
		slog.String("action", action))

	// Only process issue events
	if eventType != WebhookEventIssue {
		return nil
	}

	// Process open/update events
	switch action {
	case "open":
		return h.handleIssueOpened(ctx, payload)
	case "update":
		return h.handleIssueUpdated(ctx, payload)
	default:
		return nil
	}
}

// handleIssueOpened processes newly created issues
func (h *WebhookHandler) handleIssueOpened(ctx context.Context, payload *IssueWebhookPayload) error {
	// Check if issue has pilot label
	if !h.hasPilotLabel(payload.Labels) {
		logging.WithComponent("gitlab").Debug("Issue does not have pilot label, skipping",
			slog.Int("iid", payload.ObjectAttributes.IID))
		return nil
	}

	return h.processIssue(ctx, payload)
}

// handleIssueUpdated processes issue updates (label changes)
func (h *WebhookHandler) handleIssueUpdated(ctx context.Context, payload *IssueWebhookPayload) error {
	// Check if pilot label was added
	if payload.Changes == nil || payload.Changes.Labels == nil {
		return nil
	}

	labelAdded := h.wasLabelAdded(payload.Changes.Labels)
	if !labelAdded {
		logging.WithComponent("gitlab").Debug("Pilot label was not added, skipping",
			slog.Int("iid", payload.ObjectAttributes.IID))
		return nil
	}

	return h.processIssue(ctx, payload)
}

// processIssue processes an issue that should be handled by Pilot
func (h *WebhookHandler) processIssue(ctx context.Context, payload *IssueWebhookPayload) error {
	logging.WithComponent("gitlab").Info("Processing pilot issue",
		slog.String("project", payload.Project.PathWithNamespace),
		slog.Int("iid", payload.ObjectAttributes.IID),
		slog.String("title", payload.ObjectAttributes.Title))

	// Fetch full issue details via API (webhook payload may be incomplete)
	fullIssue, err := h.client.GetIssue(ctx, payload.ObjectAttributes.IID)
	if err != nil {
		return fmt.Errorf("failed to fetch issue details: %w", err)
	}

	// Fetch project info
	project := &Project{
		ID:                payload.Project.ID,
		Name:              payload.Project.Name,
		PathWithNamespace: payload.Project.PathWithNamespace,
		WebURL:            payload.Project.WebURL,
		DefaultBranch:     payload.Project.DefaultBranch,
	}

	// Call the callback
	if h.onIssue != nil {
		return h.onIssue(ctx, fullIssue, project)
	}

	return nil
}

// hasPilotLabel checks if the labels include the pilot label
func (h *WebhookHandler) hasPilotLabel(labels []*WebhookLabel) bool {
	for _, label := range labels {
		if label.Title == h.pilotLabel {
			return true
		}
	}
	return false
}

// wasLabelAdded checks if the pilot label was added in this update
func (h *WebhookHandler) wasLabelAdded(labelChange *LabelChange) bool {
	// Check if pilot label is in current but not in previous
	hasCurrent := false
	for _, label := range labelChange.Current {
		if label.Title == h.pilotLabel {
			hasCurrent = true
			break
		}
	}

	if !hasCurrent {
		return false
	}

	// Check if it was in previous (if it was, it's not a new addition)
	for _, label := range labelChange.Previous {
		if label.Title == h.pilotLabel {
			return false
		}
	}

	return true
}
