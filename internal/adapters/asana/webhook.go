package asana

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"

	"github.com/alekspetrov/pilot/internal/logging"
)

// WebhookHandler handles Asana webhooks
type WebhookHandler struct {
	client        *Client
	webhookSecret string
	pilotTag      string
	onTask        func(context.Context, *Task) error
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(client *Client, webhookSecret, pilotTag string) *WebhookHandler {
	return &WebhookHandler{
		client:        client,
		webhookSecret: webhookSecret,
		pilotTag:      pilotTag,
	}
}

// OnTask sets the callback for when a pilot-tagged task is received
func (h *WebhookHandler) OnTask(callback func(context.Context, *Task) error) {
	h.onTask = callback
}

// VerifySignature verifies the Asana webhook signature using X-Hook-Secret
// Asana uses HMAC-SHA256 for webhook signature verification
func (h *WebhookHandler) VerifySignature(payload []byte, signature string) bool {
	if h.webhookSecret == "" {
		// No secret configured, skip verification (development mode)
		return true
	}

	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSig))
}

// HandleHandshake handles the initial webhook handshake from Asana
// Returns the X-Hook-Secret value that should be echoed back
func (h *WebhookHandler) HandleHandshake(hookSecret string) string {
	// Store the secret for future verification
	// In production, this would typically be saved to config
	return hookSecret
}

// Handle processes a webhook payload
func (h *WebhookHandler) Handle(ctx context.Context, payload *WebhookPayload) error {
	for _, event := range payload.Events {
		if err := h.handleEvent(ctx, event); err != nil {
			logging.WithComponent("asana").Error("Failed to handle event",
				slog.String("action", event.Action),
				slog.String("resource_type", event.Resource.ResourceType),
				slog.Any("error", err))
			// Continue processing other events
		}
	}
	return nil
}

// handleEvent processes a single webhook event
func (h *WebhookHandler) handleEvent(ctx context.Context, event WebhookEvent) error {
	logging.WithComponent("asana").Debug("Processing Asana webhook event",
		slog.String("action", event.Action),
		slog.String("resource_type", event.Resource.ResourceType),
		slog.String("resource_gid", event.Resource.GID))

	// Only process task events
	if event.Resource.ResourceType != "task" {
		logging.WithComponent("asana").Debug("Ignoring non-task event",
			slog.String("resource_type", event.Resource.ResourceType))
		return nil
	}

	switch WebhookEventType(event.Action) {
	case EventTaskAdded, EventTaskChanged:
		return h.handleTaskEvent(ctx, event)
	default:
		logging.WithComponent("asana").Debug("Ignoring event action",
			slog.String("action", event.Action))
		return nil
	}
}

// handleTaskEvent processes task create/update events
func (h *WebhookHandler) handleTaskEvent(ctx context.Context, event WebhookEvent) error {
	taskGID := event.Resource.GID

	// Check if this is a tag change event - if so, verify pilot tag was added
	if event.Change != nil && event.Change.Field == "tags" {
		if !h.wasTagAdded(event.Change) {
			logging.WithComponent("asana").Debug("Tag change but pilot tag not added, skipping",
				slog.String("task_gid", taskGID))
			return nil
		}
	}

	// Fetch full task details
	task, err := h.client.GetTaskWithFields(ctx, taskGID, []string{
		"gid", "name", "notes", "html_notes", "completed", "completed_at",
		"assignee", "projects", "tags", "workspace", "parent",
		"created_at", "modified_at", "due_on", "due_at", "start_on", "permalink_url",
	})
	if err != nil {
		return fmt.Errorf("failed to fetch task details: %w", err)
	}

	// Check if task has pilot tag
	if !h.hasPilotTag(task) {
		logging.WithComponent("asana").Debug("Task does not have pilot tag, skipping",
			slog.String("gid", taskGID),
			slog.String("name", task.Name))
		return nil
	}

	// Skip completed tasks
	if task.Completed {
		logging.WithComponent("asana").Debug("Task is already completed, skipping",
			slog.String("gid", taskGID))
		return nil
	}

	return h.processTask(ctx, task)
}

// processTask processes a task that should be handled by Pilot
func (h *WebhookHandler) processTask(ctx context.Context, task *Task) error {
	logging.WithComponent("asana").Info("Processing pilot task",
		slog.String("gid", task.GID),
		slog.String("name", task.Name))

	// Call the callback
	if h.onTask != nil {
		return h.onTask(ctx, task)
	}

	return nil
}

// hasPilotTag checks if the task has the pilot tag
func (h *WebhookHandler) hasPilotTag(task *Task) bool {
	for _, tag := range task.Tags {
		if strings.EqualFold(tag.Name, h.pilotTag) {
			return true
		}
	}
	return false
}

// wasTagAdded checks if the pilot tag was added in this change
func (h *WebhookHandler) wasTagAdded(change *WebhookChange) bool {
	if change.Action != "added" {
		return false
	}

	// AddedValue might be a map with tag info
	if addedTag, ok := change.AddedValue.(map[string]interface{}); ok {
		if name, ok := addedTag["name"].(string); ok {
			return strings.EqualFold(name, h.pilotTag)
		}
		// Check by GID if name not available
		if gid, ok := addedTag["gid"].(string); ok {
			// Would need to look up tag name, for now just log
			logging.WithComponent("asana").Debug("Tag added by GID",
				slog.String("gid", gid))
			return true // Optimistically assume it might be pilot tag
		}
	}

	return false
}

// HandleRaw processes a raw webhook payload (for use with net/http handlers)
func (h *WebhookHandler) HandleRaw(ctx context.Context, events []map[string]interface{}) error {
	for _, eventData := range events {
		event := h.parseEvent(eventData)
		if err := h.handleEvent(ctx, event); err != nil {
			logging.WithComponent("asana").Error("Failed to handle raw event",
				slog.Any("error", err))
		}
	}
	return nil
}

// parseEvent parses a raw event map into a WebhookEvent
func (h *WebhookHandler) parseEvent(data map[string]interface{}) WebhookEvent {
	event := WebhookEvent{}

	if action, ok := data["action"].(string); ok {
		event.Action = action
	}

	if resource, ok := data["resource"].(map[string]interface{}); ok {
		if gid, ok := resource["gid"].(string); ok {
			event.Resource.GID = gid
		}
		if resourceType, ok := resource["resource_type"].(string); ok {
			event.Resource.ResourceType = resourceType
		}
		if name, ok := resource["name"].(string); ok {
			event.Resource.Name = name
		}
	}

	if parent, ok := data["parent"].(map[string]interface{}); ok {
		event.Parent = &WebhookResource{}
		if gid, ok := parent["gid"].(string); ok {
			event.Parent.GID = gid
		}
		if resourceType, ok := parent["resource_type"].(string); ok {
			event.Parent.ResourceType = resourceType
		}
	}

	if change, ok := data["change"].(map[string]interface{}); ok {
		event.Change = &WebhookChange{}
		if field, ok := change["field"].(string); ok {
			event.Change.Field = field
		}
		if action, ok := change["action"].(string); ok {
			event.Change.Action = action
		}
		if addedValue, ok := change["added_value"]; ok {
			event.Change.AddedValue = addedValue
		}
		if removedValue, ok := change["removed_value"]; ok {
			event.Change.RemovedValue = removedValue
		}
		if newValue, ok := change["new_value"]; ok {
			event.Change.NewValue = newValue
		}
	}

	return event
}
