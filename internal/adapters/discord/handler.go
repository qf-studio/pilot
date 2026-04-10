package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/logging"
)

// Handler processes incoming Discord events and coordinates task execution
// by delegating message handling to the shared comms.Handler.
type Handler struct {
	gatewayClient   *GatewayClient
	apiClient       *Client
	notifier        *Notifier       // GH-2132: task lifecycle notifications
	commsHandler    *comms.Handler  // Shared message handler for intent dispatch + task execution
	allowedGuilds   map[string]bool
	allowedChannels map[string]bool
	stopCh          chan struct{}
	stopOnce        sync.Once
	wg              sync.WaitGroup
	log             *slog.Logger
	botID           string
}

// HandlerConfig holds configuration for the Discord handler.
type HandlerConfig struct {
	BotToken        string
	BotID           string
	AllowedGuilds   []string
	AllowedChannels []string
	CommsHandler    *comms.Handler
}

// NewHandler creates a new Discord event handler.
func NewHandler(config *HandlerConfig, commsHandler *comms.Handler) *Handler {
	allowedGuilds := make(map[string]bool)
	for _, id := range config.AllowedGuilds {
		allowedGuilds[id] = true
	}

	allowedChannels := make(map[string]bool)
	for _, id := range config.AllowedChannels {
		allowedChannels[id] = true
	}

	return &Handler{
		gatewayClient:   NewGatewayClient(config.BotToken, DefaultIntents),
		apiClient:       NewClient(config.BotToken),
		commsHandler:    commsHandler,
		allowedGuilds:   allowedGuilds,
		allowedChannels: allowedChannels,
		stopCh:          make(chan struct{}),
		log:             logging.WithComponent("discord.handler"),
		botID:           config.BotID,
	}
}

// SetNotifier sets the notifier for task lifecycle messages (GH-2132).
func (h *Handler) SetNotifier(n *Notifier) {
	h.notifier = n
}

// StartListening connects to Discord and starts listening for events
// with automatic reconnection.
func (h *Handler) StartListening(ctx context.Context) error {
	events, err := h.gatewayClient.StartListening(ctx)
	if err != nil {
		return fmt.Errorf("start listening: %w", err)
	}

	// Pick up bot user ID from READY event if not configured
	if h.botID == "" {
		h.botID = h.gatewayClient.BotUserID()
	}

	h.log.Info("Discord handler listening for events")

	// Start cleanup goroutine for expired pending tasks (delegated to commsHandler)
	h.wg.Add(1)
	go h.cleanupLoop(ctx)

	// Process events
	for {
		select {
		case <-ctx.Done():
			h.log.Info("Discord listener stopping (context cancelled)")
			return ctx.Err()
		case <-h.stopCh:
			h.log.Info("Discord listener stopping (stop signal)")
			return nil
		case evt, ok := <-events:
			if !ok {
				h.log.Info("Discord event channel closed")
				return nil
			}
			h.processEvent(ctx, &evt)
		}
	}
}

// Stop gracefully stops the handler. Safe to call multiple times.
func (h *Handler) Stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})
	_ = h.gatewayClient.Close()
	h.wg.Wait()
}

// cleanupLoop delegates cleanup to the shared commsHandler.
func (h *Handler) cleanupLoop(ctx context.Context) {
	defer h.wg.Done()
	cctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-h.stopCh:
			cancel()
		case <-cctx.Done():
		}
	}()
	if h.commsHandler != nil {
		h.commsHandler.CleanupLoop(cctx)
	}
}

// processEvent handles a single Discord event.
func (h *Handler) processEvent(ctx context.Context, event *GatewayEvent) {
	if event.T == nil {
		return
	}

	switch *event.T {
	case "MESSAGE_CREATE":
		h.handleMessageCreate(ctx, event)
	case "INTERACTION_CREATE":
		h.handleInteractionCreate(ctx, event)
	}
}

// stripMention removes a leading <@BOT_ID> mention from the message content.
func (h *Handler) stripMention(content string) string {
	if h.botID != "" {
		// Remove exact bot mention: <@BOT_ID> or <@!BOT_ID>
		prefix1 := "<@" + h.botID + ">"
		prefix2 := "<@!" + h.botID + ">"
		content = strings.TrimPrefix(content, prefix1)
		content = strings.TrimPrefix(content, prefix2)
		content = strings.TrimSpace(content)
		return content
	}

	// Fallback: strip any leading <@...> or <@!...> mention when botID is unknown
	if strings.HasPrefix(content, "<@") {
		if idx := strings.Index(content, ">"); idx != -1 {
			content = strings.TrimSpace(content[idx+1:])
		}
	}
	return content
}

// handleMessageCreate processes incoming messages.
func (h *Handler) handleMessageCreate(ctx context.Context, event *GatewayEvent) {
	var msg MessageCreate
	data, _ := json.Marshal(event.D)
	if err := json.Unmarshal(data, &msg); err != nil {
		h.log.Warn("Failed to parse MESSAGE_CREATE", slog.Any("error", err))
		return
	}

	// Ignore bot messages (including our own)
	if msg.Author.Bot {
		return
	}

	// Check guild/channel allowlist
	if !h.isAllowed(msg.GuildID, msg.ChannelID) {
		h.log.Debug("Ignoring message from unauthorized guild/channel",
			slog.String("guild_id", msg.GuildID),
			slog.String("channel_id", msg.ChannelID))
		return
	}

	// Strip bot mention prefix before processing
	text := h.stripMention(strings.TrimSpace(msg.Content))
	if text == "" {
		return
	}

	h.log.Debug("Message received",
		slog.String("channel_id", msg.ChannelID),
		slog.String("author_id", msg.Author.ID),
		slog.String("text", TruncateText(text, 50)))

	// Delegate to shared comms.Handler for intent detection + dispatch
	if h.commsHandler != nil {
		h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
			ContextID:  msg.ChannelID,
			SenderID:   msg.Author.ID,
			SenderName: msg.Author.Username,
			Text:       text,
			Platform:   "discord",
			GuildID:    msg.GuildID,
		})
	}
}

// handleInteractionCreate processes button clicks and other interactions.
func (h *Handler) handleInteractionCreate(ctx context.Context, event *GatewayEvent) {
	var interaction InteractionCreate
	data, _ := json.Marshal(event.D)
	if err := json.Unmarshal(data, &interaction); err != nil {
		h.log.Warn("Failed to parse INTERACTION_CREATE", slog.Any("error", err))
		return
	}

	// Only handle MESSAGE_COMPONENT (button clicks)
	if interaction.Type != 3 {
		return
	}

	userID := ""
	if interaction.User != nil {
		userID = interaction.User.ID
	} else if interaction.Member != nil {
		userID = interaction.Member.User.ID
	}

	h.log.Debug("Interaction received",
		slog.String("channel_id", interaction.ChannelID),
		slog.String("custom_id", interaction.Data.CustomID),
		slog.String("user_id", userID))

	// Acknowledge interaction with DEFERRED_UPDATE_MESSAGE (type 6) for button clicks.
	// Type 6 acknowledges without sending a new visible message.
	_ = h.apiClient.CreateInteractionResponse(ctx, interaction.ID, interaction.Token, InteractionResponseDeferredUpdateMessage, "")

	// Normalize Discord button IDs to comms action IDs
	var normalizedAction string
	switch interaction.Data.CustomID {
	case "execute_task":
		normalizedAction = "execute"
	case "cancel_task":
		normalizedAction = "cancel"
	default:
		return
	}

	// Delegate to comms.Handler as a callback
	if h.commsHandler != nil {
		h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
			ContextID:  interaction.ChannelID,
			SenderID:   userID,
			Platform:   "discord",
			IsCallback: true,
			ActionID:   normalizedAction,
		})
	}
}

// isAllowed checks if a guild/channel is authorized.
// DMs (empty guildID) are always permitted when only guild restrictions are set.
func (h *Handler) isAllowed(guildID, channelID string) bool {
	// If no restrictions, allow all
	if len(h.allowedGuilds) == 0 && len(h.allowedChannels) == 0 {
		return true
	}

	// Check channel allowlist first (most specific)
	if len(h.allowedChannels) > 0 && h.allowedChannels[channelID] {
		return true
	}

	// Check guild allowlist
	if len(h.allowedGuilds) > 0 {
		// DMs have empty guildID — permit them when only guild restrictions are set
		if guildID == "" {
			return len(h.allowedChannels) == 0
		}
		return h.allowedGuilds[guildID]
	}

	return false
}
