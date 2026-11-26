package plane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/text"
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

// LabelResolver returns the UUID of the label with the given name in a project.
// It returns "" (or an error) when the project has no label with that name.
type LabelResolver func(ctx context.Context, projectID, labelName string) (string, error)

// labelCacheTTL is how long a resolved label UUID (and a resolve-failure warning)
// is kept for a project.
const labelCacheTTL = 10 * time.Minute

type cachedLabel struct {
	id string
	at time.Time
}

// WebhookHandler handles Plane.so webhooks.
type WebhookHandler struct {
	secret     string
	pilotLabel string
	projectIDs []string
	onWorkItem func(context.Context, *WebhookWorkItemData) error

	// GH-6705: pilotLabel is usually a label NAME, while events carry label
	// UUIDs. The resolver maps the name to the UUID for each project.
	resolveLabel LabelResolver
	labelMu      sync.Mutex
	labelCache   map[string]cachedLabel // projectID → resolved pilot label
	labelWarned  map[string]time.Time   // projectID → time of last resolve-failure warning
	now          func() time.Time
	logger       *slog.Logger // nil → logging.WithComponent("plane")
}

// NewWebhookHandler creates a new Plane webhook handler.
func NewWebhookHandler(secret, pilotLabel string, projectIDs []string) *WebhookHandler {
	return &WebhookHandler{
		secret:     secret,
		pilotLabel: pilotLabel,
		projectIDs: projectIDs,
	}
}

// SetLabelResolver sets the resolver used to map the configured pilot label
// name to the label UUID of the event's project. Without a resolver the
// configured value must be the label UUID itself.
func (h *WebhookHandler) SetLabelResolver(resolve LabelResolver) {
	h.labelMu.Lock()
	defer h.labelMu.Unlock()
	h.resolveLabel = resolve
	h.labelCache = make(map[string]cachedLabel)
	h.labelWarned = make(map[string]time.Time)
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
	log := h.log()

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
	if !h.hasPilotLabel(ctx, data.ProjectID, data.LabelIDs) {
		log.Debug("Work item does not have pilot label, skipping",
			slog.String("work_item_id", data.ID))
		return nil
	}

	// Strip invisible Unicode from the untrusted Name field before the
	// log line and before handing data to the pilot callback.
	cleanName, stripped := text.SanitizeUntrusted(data.Name)
	if stripped > 0 {
		log.Warn("invisible_unicode_stripped",
			slog.String("source", "plane-webhook"),
			slog.String("workitem", data.ID),
			slog.Int("name_stripped", stripped),
		)
	}
	data.Name = cleanName

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

// hasPilotLabel checks if any of the label UUIDs match the configured pilot label.
// A configured UUID matches directly; otherwise, when a resolver is set, the
// configured name is resolved to the UUID of the label in the event's project.
func (h *WebhookHandler) hasPilotLabel(ctx context.Context, projectID string, labelIDs []string) bool {
	if h.pilotLabel == "" {
		return false
	}
	if containsLabel(labelIDs, h.pilotLabel) {
		return true
	}
	if h.resolveLabel == nil || len(labelIDs) == 0 {
		return false
	}
	id, ok := h.resolvePilotLabel(ctx, projectID)
	return ok && containsLabel(labelIDs, id)
}

// resolvePilotLabel returns the UUID of the pilot label in the project, cached
// for labelCacheTTL. A failed lookup is not cached and warns at most once per
// project per labelCacheTTL.
func (h *WebhookHandler) resolvePilotLabel(ctx context.Context, projectID string) (string, bool) {
	now := h.clock()

	h.labelMu.Lock()
	if c, ok := h.labelCache[projectID]; ok && now.Sub(c.at) < labelCacheTTL {
		h.labelMu.Unlock()
		return c.id, true
	}
	resolve := h.resolveLabel
	h.labelMu.Unlock()

	id, err := resolve(ctx, projectID, h.pilotLabel)

	h.labelMu.Lock()
	defer h.labelMu.Unlock()
	if err == nil && id != "" {
		h.labelCache[projectID] = cachedLabel{id: id, at: now}
		return id, true
	}
	if last, warned := h.labelWarned[projectID]; !warned || now.Sub(last) >= labelCacheTTL {
		h.labelWarned[projectID] = now
		attrs := []any{
			slog.String("project_id", projectID),
			slog.String("label", h.pilotLabel),
		}
		if err != nil {
			attrs = append(attrs, slog.Any("error", err))
		}
		h.log().Warn("Plane pilot label not resolved for project; webhook events are not accepted", attrs...)
	}
	return "", false
}

func containsLabel(labelIDs []string, want string) bool {
	for _, id := range labelIDs {
		if id == want {
			return true
		}
	}
	return false
}

func (h *WebhookHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

func (h *WebhookHandler) log() *slog.Logger {
	if h.logger != nil {
		return h.logger
	}
	return logging.WithComponent("plane")
}
