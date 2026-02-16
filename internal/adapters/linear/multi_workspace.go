package linear

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/alekspetrov/pilot/internal/logging"
)

// MultiWorkspaceHandler manages multiple Linear workspaces and routes webhooks to the correct handler
type MultiWorkspaceHandler struct {
	workspaces map[string]*WorkspaceHandler // keyed by team_id
	mu         sync.RWMutex
}

// WorkspaceHandler handles webhooks for a single Linear workspace
type WorkspaceHandler struct {
	config   *WorkspaceConfig
	client   *Client
	webhook  *WebhookHandler
	notifier *Notifier
}

// NewMultiWorkspaceHandler creates a handler for multiple Linear workspaces
func NewMultiWorkspaceHandler(cfg *Config) (*MultiWorkspaceHandler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	h := &MultiWorkspaceHandler{
		workspaces: make(map[string]*WorkspaceHandler),
	}

	workspaces := cfg.GetWorkspaces()
	for _, ws := range workspaces {
		client := NewClient(ws.APIKey)
		pilotLabel := ws.PilotLabel
		if pilotLabel == "" {
			pilotLabel = "pilot"
		}

		handler := &WorkspaceHandler{
			config:   ws,
			client:   client,
			webhook:  NewWebhookHandler(client, pilotLabel, ws.ProjectIDs),
			notifier: NewNotifier(client),
		}

		// Index by team_id for routing
		if ws.TeamID != "" {
			h.workspaces[ws.TeamID] = handler
		}

		logging.WithComponent("linear").Info("Registered Linear workspace",
			slog.String("name", ws.Name),
			slog.String("team_id", ws.TeamID),
			slog.Int("projects", len(ws.Projects)))
	}

	return h, nil
}

// OnIssue sets the callback for when a pilot-labeled issue is received.
// The callback receives the issue and the workspace name it came from.
func (h *MultiWorkspaceHandler) OnIssue(callback func(context.Context, *Issue, string) error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, ws := range h.workspaces {
		// Capture workspace name for closure
		wsName := ws.config.Name
		ws.webhook.OnIssue(func(ctx context.Context, issue *Issue) error {
			return callback(ctx, issue, wsName)
		})
	}
}

// Handle processes a webhook payload, routing to the correct workspace handler
func (h *MultiWorkspaceHandler) Handle(ctx context.Context, payload map[string]interface{}) error {
	// Extract team info from payload
	teamID := h.extractTeamID(payload)
	if teamID == "" {
		logging.WithComponent("linear").Debug("No team ID in payload, trying all handlers")
		// If no team ID, try to find a handler that can process it
		return h.handleWithoutTeamID(ctx, payload)
	}

	h.mu.RLock()
	handler, ok := h.workspaces[teamID]
	h.mu.RUnlock()

	if !ok {
		logging.WithComponent("linear").Warn("Unknown workspace for team",
			slog.String("team_id", teamID))
		return fmt.Errorf("unknown workspace for team %s", teamID)
	}

	return handler.webhook.Handle(ctx, payload)
}

// handleWithoutTeamID attempts to route webhook without team ID (fallback for legacy configs)
func (h *MultiWorkspaceHandler) handleWithoutTeamID(ctx context.Context, payload map[string]interface{}) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// If only one workspace, use it
	if len(h.workspaces) == 1 {
		for _, handler := range h.workspaces {
			return handler.webhook.Handle(ctx, payload)
		}
	}

	// Try all handlers, return first success
	var lastErr error
	for teamID, handler := range h.workspaces {
		err := handler.webhook.Handle(ctx, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		logging.WithComponent("linear").Debug("Handler failed",
			slog.String("team_id", teamID),
			slog.Any("error", err))
	}

	if lastErr != nil {
		return lastErr
	}
	return nil
}

// extractTeamID extracts the team ID from a webhook payload
func (h *MultiWorkspaceHandler) extractTeamID(payload map[string]interface{}) string {
	// Try data.team.id first (full issue payload)
	if data, ok := payload["data"].(map[string]interface{}); ok {
		if team, ok := data["team"].(map[string]interface{}); ok {
			if id, ok := team["id"].(string); ok {
				return id
			}
		}
		// Try data.teamId (webhook compact format)
		if teamID, ok := data["teamId"].(string); ok {
			return teamID
		}
	}
	return ""
}

// GetWorkspace returns the handler for a specific workspace by name
func (h *MultiWorkspaceHandler) GetWorkspace(name string) *WorkspaceHandler {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ws := range h.workspaces {
		if ws.config.Name == name {
			return ws
		}
	}
	return nil
}

// GetWorkspaceByTeamID returns the handler for a specific workspace by team ID
func (h *MultiWorkspaceHandler) GetWorkspaceByTeamID(teamID string) *WorkspaceHandler {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.workspaces[teamID]
}

// GetNotifier returns the notifier for a specific workspace
func (h *MultiWorkspaceHandler) GetNotifier(workspaceName string) *Notifier {
	ws := h.GetWorkspace(workspaceName)
	if ws == nil {
		return nil
	}
	return ws.notifier
}

// GetClient returns the client for a specific workspace
func (h *MultiWorkspaceHandler) GetClient(workspaceName string) *Client {
	ws := h.GetWorkspace(workspaceName)
	if ws == nil {
		return nil
	}
	return ws.client
}

// WorkspaceCount returns the number of configured workspaces
func (h *MultiWorkspaceHandler) WorkspaceCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.workspaces)
}

// ListWorkspaces returns the names of all configured workspaces
func (h *MultiWorkspaceHandler) ListWorkspaces() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	names := make([]string, 0, len(h.workspaces))
	for _, ws := range h.workspaces {
		names = append(names, ws.config.Name)
	}
	return names
}

// ResolvePilotProject returns the Pilot project name for an issue in a workspace.
// Delegates to WorkspaceConfig.ResolvePilotProject for the actual resolution logic.
func (ws *WorkspaceHandler) ResolvePilotProject(issue *Issue) string {
	return ws.config.ResolvePilotProject(issue)
}

// Config returns the workspace configuration
func (ws *WorkspaceHandler) Config() *WorkspaceConfig {
	return ws.config
}

// Notifier returns the workspace notifier
func (ws *WorkspaceHandler) Notifier() *Notifier {
	return ws.notifier
}

// Client returns the workspace client
func (ws *WorkspaceHandler) Client() *Client {
	return ws.client
}
