package azuredevops

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/alekspetrov/pilot/internal/logging"
)

// WebhookHandler handles Azure DevOps service hook webhooks
type WebhookHandler struct {
	client        *Client
	webhookSecret string
	pilotTag      string
	onWorkItem    func(context.Context, *WorkItem) error
	workItemTypes []string
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(client *Client, webhookSecret, pilotTag string) *WebhookHandler {
	return &WebhookHandler{
		client:        client,
		webhookSecret: webhookSecret,
		pilotTag:      pilotTag,
		workItemTypes: []string{"Bug", "Task", "User Story"},
	}
}

// SetWorkItemTypes sets the work item types to handle
func (h *WebhookHandler) SetWorkItemTypes(types []string) {
	h.workItemTypes = types
}

// OnWorkItem sets the callback for when a pilot-tagged work item is received
func (h *WebhookHandler) OnWorkItem(callback func(context.Context, *WorkItem) error) {
	h.onWorkItem = callback
}

// VerifySecret verifies the webhook secret using basic auth
// Azure DevOps service hooks support basic authentication
func (h *WebhookHandler) VerifySecret(providedSecret string) bool {
	if h.webhookSecret == "" {
		// No secret configured, skip verification (development mode)
		return true
	}

	// Use constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(providedSecret), []byte(h.webhookSecret)) == 1
}

// Handle processes a webhook payload
func (h *WebhookHandler) Handle(ctx context.Context, payload *WebhookPayload) error {
	logger := logging.WithComponent("azuredevops-webhook")
	logger.Debug("Azure DevOps webhook received",
		slog.String("event", payload.EventType),
		slog.String("publisher", payload.PublisherID),
	)

	// Only process work item events from the tfs publisher
	if payload.PublisherID != "tfs" {
		return nil
	}

	switch payload.EventType {
	case WebhookEventWorkItemCreated:
		return h.handleWorkItemCreated(ctx, payload)
	case WebhookEventWorkItemUpdated:
		return h.handleWorkItemUpdated(ctx, payload)
	default:
		logger.Debug("Ignoring webhook event", slog.String("event", payload.EventType))
		return nil
	}
}

// handleWorkItemCreated processes newly created work items
func (h *WebhookHandler) handleWorkItemCreated(ctx context.Context, payload *WebhookPayload) error {
	logger := logging.WithComponent("azuredevops-webhook")

	// Parse the resource as work item webhook resource
	resource, err := h.parseWorkItemResource(payload.Resource)
	if err != nil {
		return fmt.Errorf("failed to parse work item resource: %w", err)
	}

	// Check if work item has pilot tag
	if !h.hasPilotTag(resource.Fields) {
		logger.Debug("Work item does not have pilot tag, skipping",
			slog.Int("id", resource.ID))
		return nil
	}

	// Check work item type
	if !h.isAllowedWorkItemType(resource.Fields) {
		logger.Debug("Work item type not in allowed list, skipping",
			slog.Int("id", resource.ID))
		return nil
	}

	return h.processWorkItem(ctx, resource.ID)
}

// handleWorkItemUpdated processes work item updates (tag changes)
func (h *WebhookHandler) handleWorkItemUpdated(ctx context.Context, payload *WebhookPayload) error {
	logger := logging.WithComponent("azuredevops-webhook")

	// Parse the resource
	resource, err := h.parseWorkItemResource(payload.Resource)
	if err != nil {
		return fmt.Errorf("failed to parse work item resource: %w", err)
	}

	// For updates, check if the pilot tag was added
	// The revision contains the new values, we need to check if tag is now present
	currentFields := resource.Fields
	if resource.Revision != nil {
		currentFields = resource.Revision.Fields
	}

	if !h.hasPilotTag(currentFields) {
		logger.Debug("Work item does not have pilot tag after update, skipping",
			slog.Int("id", resource.ID))
		return nil
	}

	// Also check it's not already in progress or done
	if h.hasTag(currentFields, TagInProgress) || h.hasTag(currentFields, TagDone) {
		logger.Debug("Work item already processed, skipping",
			slog.Int("id", resource.ID))
		return nil
	}

	// Check work item type
	if !h.isAllowedWorkItemType(currentFields) {
		logger.Debug("Work item type not in allowed list, skipping",
			slog.Int("id", resource.ID))
		return nil
	}

	return h.processWorkItem(ctx, resource.ID)
}

// processWorkItem processes a work item that should be handled by Pilot
func (h *WebhookHandler) processWorkItem(ctx context.Context, workItemID int) error {
	logger := logging.WithComponent("azuredevops-webhook")
	logger.Info("Processing pilot work item",
		slog.Int("id", workItemID))

	// Fetch full work item details via API (webhook payload may be incomplete)
	fullWorkItem, err := h.client.GetWorkItem(ctx, workItemID)
	if err != nil {
		return fmt.Errorf("failed to fetch work item details: %w", err)
	}

	logger.Info("Fetched work item details",
		slog.Int("id", fullWorkItem.ID),
		slog.String("title", fullWorkItem.GetTitle()),
		slog.String("type", fullWorkItem.GetWorkItemType()),
	)

	// Call the callback
	if h.onWorkItem != nil {
		return h.onWorkItem(ctx, fullWorkItem)
	}

	return nil
}

// parseWorkItemResource parses the resource map into WorkItemWebhookResource
func (h *WebhookHandler) parseWorkItemResource(resource map[string]interface{}) (*WorkItemWebhookResource, error) {
	// Re-marshal and unmarshal to get proper typing
	data, err := json.Marshal(resource)
	if err != nil {
		return nil, err
	}

	var result WorkItemWebhookResource
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// hasPilotTag checks if the fields include the pilot tag
func (h *WebhookHandler) hasPilotTag(fields map[string]interface{}) bool {
	return h.hasTag(fields, h.pilotTag)
}

// hasTag checks if the fields include a specific tag
func (h *WebhookHandler) hasTag(fields map[string]interface{}, tag string) bool {
	tagsValue, ok := fields["System.Tags"]
	if !ok {
		return false
	}

	tagsStr, ok := tagsValue.(string)
	if !ok {
		return false
	}

	tags := splitTags(tagsStr)
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// isAllowedWorkItemType checks if the work item type is in the allowed list
func (h *WebhookHandler) isAllowedWorkItemType(fields map[string]interface{}) bool {
	witValue, ok := fields["System.WorkItemType"]
	if !ok {
		return false
	}

	wit, ok := witValue.(string)
	if !ok {
		return false
	}

	for _, allowedType := range h.workItemTypes {
		if wit == allowedType {
			return true
		}
	}
	return false
}
