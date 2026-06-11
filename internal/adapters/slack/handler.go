package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/studio-sdk/sdk/core"
)

// commsHandlerIface is the subset of *comms.Handler that Handler calls.
// Using an interface allows test doubles without a full comms stack.
type commsHandlerIface interface {
	HandleMessage(ctx context.Context, msg *comms.IncomingMessage)
	CleanupLoop(ctx context.Context)
}

// MemberResolver resolves a Slack user to a team member ID for RBAC (GH-786).
// Decoupled from teams package to avoid import cycles.
type MemberResolver interface {
	// ResolveSlackIdentity maps a Slack user ID and/or email to a member ID.
	// Returns ("", nil) when no match is found (= skip RBAC).
	ResolveSlackIdentity(slackUserID, email string) (string, error)
}

// MemberResolverAdapter wraps a slack.MemberResolver as comms.MemberResolver.
type MemberResolverAdapter struct {
	Inner MemberResolver
}

// ResolveIdentity implements comms.MemberResolver by delegating to ResolveSlackIdentity.
func (a *MemberResolverAdapter) ResolveIdentity(senderID string) (string, error) {
	return a.Inner.ResolveSlackIdentity(senderID, "")
}

// Handler processes incoming Slack events and coordinates task execution.
// Delegates intent detection and task lifecycle to the shared comms.Handler (GH-2143).
type Handler struct {
	socketClient    *SocketModeClient
	apiClient       *Client          // Kept for client access; Messenger wraps this
	commsHandler    commsHandlerIface // Shared message handler for intent dispatch + task execution
	allowedChannels map[string]bool   // Allowed channel IDs for security
	allowedUsers    map[string]bool   // Allowed user IDs for security
	stopCh          chan struct{}
	wg              sync.WaitGroup
	log             *slog.Logger
}

// HandlerConfig holds configuration for the Slack handler.
type HandlerConfig struct {
	AppToken        string         // Slack app-level token (xapp-...)
	BotToken        string         // Slack bot token (xoxb-...)
	Client          *Client        // Optional: reuse existing API client
	CommsHandler    *comms.Handler // Shared handler for intent dispatch + task lifecycle
	AllowedChannels []string       // Channel IDs allowed to send tasks
	AllowedUsers    []string       // User IDs allowed to send tasks
}

// LLMClassifierConfig holds configuration for the LLM classifier.
// Retained for config compatibility; now handled by comms.Handler.
type LLMClassifierConfig struct {
	Enabled     bool
	APIKey      string
	HistorySize int
	HistoryTTL  time.Duration
}

// NewHandler creates a new Slack event handler.
func NewHandler(config *HandlerConfig) *Handler {
	allowedChannels := make(map[string]bool)
	for _, id := range config.AllowedChannels {
		allowedChannels[id] = true
	}

	allowedUsers := make(map[string]bool)
	for _, id := range config.AllowedUsers {
		allowedUsers[id] = true
	}

	apiClient := config.Client
	if apiClient == nil {
		apiClient = NewClient(config.BotToken)
	}

	// Store as interface only when non-nil to avoid a non-nil interface holding a nil pointer.
	var commsH commsHandlerIface
	if config.CommsHandler != nil {
		commsH = config.CommsHandler
	}

	return &Handler{
		socketClient:    NewSocketModeClient(config.AppToken),
		apiClient:       apiClient,
		commsHandler:    commsH,
		allowedChannels: allowedChannels,
		allowedUsers:    allowedUsers,
		stopCh:          make(chan struct{}),
		log:             logging.WithComponent("slack.handler"),
	}
}

// StartListening starts listening for Slack events via Socket Mode.
// It blocks until ctx is cancelled or Stop() is called.
func (h *Handler) StartListening(ctx context.Context) error {
	events, err := h.socketClient.Listen(ctx)
	if err != nil {
		return fmt.Errorf("failed to start Socket Mode listener: %w", err)
	}

	h.log.Info("Slack Socket Mode listener started")

	// Start cleanup goroutine for expired pending tasks (delegated to commsHandler)
	h.wg.Add(1)
	go h.cleanupLoop(ctx)

	// Process events
	for {
		select {
		case <-ctx.Done():
			h.log.Info("Slack listener stopping (context cancelled)")
			return ctx.Err()
		case <-h.stopCh:
			h.log.Info("Slack listener stopping (stop signal)")
			return nil
		case evt, ok := <-events:
			if !ok {
				h.log.Info("Slack event channel closed")
				return nil
			}
			h.processEvent(ctx, &evt)
		}
	}
}

// Stop gracefully stops the handler.
func (h *Handler) Stop() {
	close(h.stopCh)
	h.wg.Wait()
}

// cleanupLoop delegates pending task cleanup to the shared comms.Handler.
func (h *Handler) cleanupLoop(ctx context.Context) {
	defer h.wg.Done()
	// Create a context that's also cancelled by stopCh
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	logging.SafeGo("slack-handler", func() {
		select {
		case <-h.stopCh:
			cancel()
		case <-cctx.Done():
		}
	})
	if h.commsHandler != nil {
		h.commsHandler.CleanupLoop(cctx)
	}
}

// processEvent handles a single Slack event by delegating to comms.Handler.
func (h *Handler) processEvent(ctx context.Context, event *SocketEvent) {
	// Ignore bot messages to avoid feedback loops
	if event.IsBotMessage() {
		return
	}

	channelID := event.ChannelID
	userID := event.UserID
	text := strings.TrimSpace(event.Text)

	// Security check: only process from allowed channels/users
	if !h.isAllowed(channelID, userID) {
		h.log.Debug("Ignoring message from unauthorized channel/user",
			slog.String("channel_id", channelID),
			slog.String("user_id", userID))
		return
	}

	// Skip if no text
	if text == "" {
		return
	}

	// Delegate to shared comms.Handler for intent detection + dispatch
	if h.commsHandler != nil {
		h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
			ContextID: channelID,
			SenderID:  userID,
			Text:      text,
			ThreadID:  event.ThreadTS,
			Platform:  "slack",
			Timestamp: time.Now(),
		})
	}
}

// isAllowed checks if a channel/user is authorized.
func (h *Handler) isAllowed(channelID, userID string) bool {
	// If no restrictions configured, allow all
	if len(h.allowedChannels) == 0 && len(h.allowedUsers) == 0 {
		return true
	}

	// Check channel allowlist
	if len(h.allowedChannels) > 0 && h.allowedChannels[channelID] {
		return true
	}

	// Check user allowlist
	if len(h.allowedUsers) > 0 && h.allowedUsers[userID] {
		return true
	}

	return false
}

// HandleMessage implements core.MessageHandler. It converts the normalized SDK
// event to comms.IncomingMessage and delegates to the shared comms.Handler.
// Slack sender IDs are already strings so no conversion is needed.
//
// The conversion mirrors sdkshim.MessageEventToIncomingMessage; it is inlined
// here to avoid the sdkshim → config → slack → sdkshim import cycle.
func (h *Handler) HandleMessage(ctx context.Context, ev core.MessageEvent) error {
	msg := &comms.IncomingMessage{
		ContextID:  ev.ChannelID,
		SenderID:   ev.Sender.UserID,
		SenderName: ev.Sender.DisplayName,
		Text:       ev.Text,
		ThreadID:   ev.ThreadID,
		Platform:   "slack",
		RawEvent:   &ev,
	}
	if ev.Action == "callback" {
		msg.IsCallback = true
		msg.CallbackID = ev.CallbackID
		// ev.Data carries the button Data payload; bridgeMessenger.SendConfirmation
		// sets Data=="execute"/"cancel" so comms.Handler can route directly.
		msg.ActionID = ev.Data
	}
	if h.commsHandler != nil {
		h.commsHandler.HandleMessage(ctx, msg)
	}
	return nil
}

// SetCommsHandler wires the shared comms handler after construction. Used
// when the bridge messenger must be created before the comms handler, avoiding
// the chicken-and-egg dependency between the bridge and its Messenger.
func (h *Handler) SetCommsHandler(ch *comms.Handler) {
	h.commsHandler = ch
}

// HandleCallback processes button clicks from interactive messages.
// Normalizes Slack action IDs and delegates to comms.Handler.
func (h *Handler) HandleCallback(ctx context.Context, channelID, userID, actionID, messageTS string) {
	if h.commsHandler == nil {
		return
	}

	// Normalize Slack-specific action IDs to comms convention
	normalizedAction := actionID
	switch actionID {
	case "execute_task":
		normalizedAction = "execute"
	case "cancel_task":
		normalizedAction = "cancel"
	}

	h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
		ContextID:  channelID,
		SenderID:   userID,
		Platform:   "slack",
		IsCallback: true,
		ActionID:   normalizedAction,
		Timestamp:  time.Now(),
	})
}
