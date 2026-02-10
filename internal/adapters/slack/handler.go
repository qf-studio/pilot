package slack

import (
	"log/slog"
	"sync"

	"github.com/alekspetrov/pilot/internal/logging"
)

// MemberResolver resolves a Slack user to a team member ID for RBAC (GH-786).
// Decoupled from teams package to avoid import cycles.
type MemberResolver interface {
	// ResolveSlackIdentity maps a Slack user ID and/or email to a member ID.
	// Returns ("", nil) when no match is found (= skip RBAC).
	ResolveSlackIdentity(slackUserID, email string) (string, error)
}

// Handler processes incoming Slack events and coordinates task execution.
// Mirrors Telegram handler's RBAC support with memberResolver and lastSender tracking.
type Handler struct {
	client         *SocketModeClient
	memberResolver MemberResolver       // Team member resolver for RBAC (optional, GH-786)
	lastSender     map[string]string    // channelID -> last sender Slack user ID
	mu             sync.Mutex
	log            *slog.Logger
}

// HandlerConfig holds configuration for the Slack handler.
type HandlerConfig struct {
	AppToken       string         // Slack app-level token (xapp-...)
	MemberResolver MemberResolver // Team member resolver for RBAC (optional, GH-786)
}

// NewHandler creates a new Slack event handler.
func NewHandler(config *HandlerConfig) *Handler {
	return &Handler{
		client:         NewSocketModeClient(config.AppToken),
		memberResolver: config.MemberResolver,
		lastSender:     make(map[string]string),
		log:            logging.WithComponent("slack.handler"),
	}
}

// TrackSender records the user ID of the last sender in a channel.
// Called during event processing to track who sent messages for RBAC.
func (h *Handler) TrackSender(channelID, userID string) {
	if channelID == "" || userID == "" {
		return
	}
	h.mu.Lock()
	h.lastSender[channelID] = userID
	h.mu.Unlock()
}

// resolveMemberID resolves the current Slack sender to a team member ID (GH-786).
// Returns "" if no resolver is configured or no match is found.
func (h *Handler) resolveMemberID(channelID string) string {
	if h.memberResolver == nil {
		return ""
	}

	h.mu.Lock()
	senderID := h.lastSender[channelID]
	h.mu.Unlock()

	if senderID == "" {
		return ""
	}

	memberID, err := h.memberResolver.ResolveSlackIdentity(senderID, "")
	if err != nil {
		h.log.Warn("failed to resolve Slack identity",
			slog.String("slack_user_id", senderID),
			slog.Any("error", err))
		return ""
	}

	if memberID != "" {
		h.log.Debug("resolved Slack user to team member",
			slog.String("slack_user_id", senderID),
			slog.String("member_id", memberID))
	}

	return memberID
}

// GetLastSender returns the last sender user ID for a channel.
// Useful for testing and debugging.
func (h *Handler) GetLastSender(channelID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSender[channelID]
}
