package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/logging"
)

// Handler processes incoming Discord events and coordinates task execution.
type Handler struct {
	gatewayClient   *GatewayClient
	apiClient       *Client
	runner          *executor.Runner
	allowedGuilds   map[string]bool
	allowedChannels map[string]bool
	pendingTasks    map[string]*PendingTaskInfo
	mu              sync.Mutex
	stopCh          chan struct{}
	wg              sync.WaitGroup
	log             *slog.Logger
}

// PendingTaskInfo represents a task awaiting confirmation.
type PendingTaskInfo struct {
	TaskID      string
	Description string
	ChannelID   string
	MessageID   string
	UserID      string
	CreatedAt   time.Time
}

// HandlerConfig holds configuration for the Discord handler.
type HandlerConfig struct {
	BotToken        string
	AllowedGuilds   []string
	AllowedChannels []string
}

// NewHandler creates a new Discord event handler.
func NewHandler(config *HandlerConfig, runner *executor.Runner) *Handler {
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
		runner:          runner,
		allowedGuilds:   allowedGuilds,
		allowedChannels: allowedChannels,
		pendingTasks:    make(map[string]*PendingTaskInfo),
		stopCh:          make(chan struct{}),
		log:             logging.WithComponent("discord.handler"),
	}
}

// StartListening connects to Discord and starts listening for events.
func (h *Handler) StartListening(ctx context.Context) error {
	if err := h.gatewayClient.Connect(ctx); err != nil {
		return fmt.Errorf("connect gateway: %w", err)
	}

	events, err := h.gatewayClient.Listen(ctx)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	h.log.Info("Discord handler listening for events")

	// Start cleanup goroutine
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

// Stop gracefully stops the handler.
func (h *Handler) Stop() {
	close(h.stopCh)
	_ = h.gatewayClient.Close()
	h.wg.Wait()
}

// cleanupLoop removes expired pending tasks.
func (h *Handler) cleanupLoop(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.stopCh:
			return
		case <-ticker.C:
			h.cleanupExpiredTasks(ctx)
		}
	}
}

// cleanupExpiredTasks removes tasks pending for more than 5 minutes.
func (h *Handler) cleanupExpiredTasks(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	expiry := time.Now().Add(-5 * time.Minute)
	for channelID, task := range h.pendingTasks {
		if task.CreatedAt.Before(expiry) {
			_, _ = h.apiClient.SendMessage(ctx, task.ChannelID, "⏰ Task "+task.TaskID+" expired (no confirmation received).")
			delete(h.pendingTasks, channelID)
			h.log.Debug("Expired pending task",
				slog.String("task_id", task.TaskID),
				slog.String("channel_id", channelID))
		}
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

// handleMessageCreate processes incoming messages.
func (h *Handler) handleMessageCreate(ctx context.Context, event *GatewayEvent) {
	var msg MessageCreate
	data, _ := json.Marshal(event.D)
	if err := json.Unmarshal(data, &msg); err != nil {
		h.log.Warn("Failed to parse MESSAGE_CREATE", slog.Any("error", err))
		return
	}

	// Ignore bot messages
	if msg.Author.ID == "bot" { // In real Discord, this would be checked against bot's own ID
		return
	}

	// Check guild/channel allowlist
	if !h.isAllowed(msg.GuildID, msg.ChannelID) {
		h.log.Debug("Ignoring message from unauthorized guild/channel",
			slog.String("guild_id", msg.GuildID),
			slog.String("channel_id", msg.ChannelID))
		return
	}

	text := strings.TrimSpace(msg.Content)
	if text == "" {
		return
	}

	h.log.Debug("Message received",
		slog.String("channel_id", msg.ChannelID),
		slog.String("author_id", msg.Author.ID),
		slog.String("text", TruncateText(text, 50)))

	// For now, treat as task message
	// In a full implementation, would use intent detection
	if strings.HasPrefix(text, "/") {
		// Handle commands
		return
	}

	// Treat as task request
	h.handleTask(ctx, msg.ChannelID, msg.Author.ID, text)
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

	// Acknowledge interaction
	responseType := 4 // CHANNEL_MESSAGE_WITH_SOURCE (deferred)
	_ = h.apiClient.CreateInteractionResponse(ctx, interaction.ID, interaction.Token, responseType, "Processing...")

	// Handle button actions
	switch interaction.Data.CustomID {
	case "execute_task":
		h.handleConfirmation(ctx, interaction.ChannelID, userID, true)
	case "cancel_task":
		h.handleConfirmation(ctx, interaction.ChannelID, userID, false)
	}
}

// isAllowed checks if a guild/channel is authorized.
func (h *Handler) isAllowed(guildID, channelID string) bool {
	// If no restrictions, allow all
	if len(h.allowedGuilds) == 0 && len(h.allowedChannels) == 0 {
		return true
	}

	// Check guild allowlist
	if len(h.allowedGuilds) > 0 && h.allowedGuilds[guildID] {
		return true
	}

	// Check channel allowlist
	if len(h.allowedChannels) > 0 && h.allowedChannels[channelID] {
		return true
	}

	return false
}

// handleTask creates and sends a confirmation prompt for a task.
func (h *Handler) handleTask(ctx context.Context, channelID, userID, description string) {
	// Generate task ID
	taskID := fmt.Sprintf("DISCORD-%d", time.Now().Unix())

	// Check for existing pending task
	h.mu.Lock()
	if existing, exists := h.pendingTasks[channelID]; exists {
		h.mu.Unlock()
		_, _ = h.apiClient.SendMessage(ctx, channelID, fmt.Sprintf("⚠️ You already have a pending task: %s\n\nReply with execute or cancel.", existing.TaskID))
		return
	}

	// Create pending task
	pending := &PendingTaskInfo{
		TaskID:      taskID,
		Description: description,
		ChannelID:   channelID,
		UserID:      userID,
		CreatedAt:   time.Now(),
	}
	h.pendingTasks[channelID] = pending
	h.mu.Unlock()

	// Send confirmation
	text := FormatTaskConfirmation(taskID, description, "")
	buttons := BuildConfirmationButtons()

	msg, err := h.apiClient.SendMessageWithComponents(ctx, channelID, text, buttons)
	if err != nil {
		h.log.Warn("Failed to send confirmation", slog.Any("error", err))
		_, _ = h.apiClient.SendMessage(ctx, channelID, text+"\n\nReply with execute or cancel.")
		return
	}

	h.mu.Lock()
	if p, ok := h.pendingTasks[channelID]; ok {
		if msg != nil {
			p.MessageID = msg.ID
		}
	}
	h.mu.Unlock()
}

// handleConfirmation processes task execution confirmation.
func (h *Handler) handleConfirmation(ctx context.Context, channelID, userID string, confirmed bool) {
	h.mu.Lock()
	pending, exists := h.pendingTasks[channelID]
	if exists {
		delete(h.pendingTasks, channelID)
	}
	h.mu.Unlock()

	if !exists {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "No pending task to confirm.")
		return
	}

	if confirmed {
		h.executeTask(ctx, channelID, pending.TaskID, pending.Description)
	} else {
		_, _ = h.apiClient.SendMessage(ctx, channelID, fmt.Sprintf("❌ Task %s cancelled.", pending.TaskID))
	}
}

// executeTask executes a confirmed task.
func (h *Handler) executeTask(ctx context.Context, channelID, taskID, description string) {
	// Send execution started message
	progressMsg := FormatProgressUpdate(taskID, "Starting", 0, "Initializing...")
	msg, err := h.apiClient.SendMessage(ctx, channelID, progressMsg)
	if err != nil {
		h.log.Warn("Failed to send start message", slog.Any("error", err))
	}

	var progressMsgID string
	if msg != nil {
		progressMsgID = msg.ID
	}

	// Create task for executor
	task := &executor.Task{
		ID:          taskID,
		Title:       TruncateText(description, 50),
		Description: description,
		ProjectPath: ".", // Default to current dir
		Verbose:     false,
		Branch:      fmt.Sprintf("pilot/%s", taskID),
		BaseBranch:  "main",
		CreatePR:    true,
	}

	// Set up progress callback
	var lastPhase string
	var lastProgress int
	var lastUpdate time.Time

	if progressMsgID != "" && h.runner != nil {
		h.runner.OnProgress(func(tid string, phase string, progress int, message string) {
			if tid != taskID {
				return
			}

			now := time.Now()
			phaseChanged := phase != lastPhase
			progressChanged := progress-lastProgress >= 15
			timeElapsed := now.Sub(lastUpdate) >= 3*time.Second

			if !phaseChanged && !progressChanged && !timeElapsed {
				return
			}

			lastPhase = phase
			lastProgress = progress
			lastUpdate = now

			updateText := FormatProgressUpdate(taskID, phase, progress, message)
			_ = h.apiClient.EditMessage(ctx, channelID, progressMsgID, updateText)
		})
	}

	// Execute task
	h.log.Info("Executing task",
		slog.String("task_id", taskID),
		slog.String("channel_id", channelID))
	result, err := h.runner.Execute(ctx, task)

	// Clear progress callback
	if h.runner != nil {
		h.runner.OnProgress(nil)
	}

	// Send result
	if err != nil {
		errMsg := fmt.Sprintf("❌ Task failed\n%s\n\n%s", taskID, err.Error())
		_, _ = h.apiClient.SendMessage(ctx, channelID, errMsg)
		return
	}

	// Format and send result
	output := CleanInternalSignals(result.Output)
	prURL := result.PRUrl
	resultMsg := FormatTaskResult(output, true, prURL)

	_, _ = h.apiClient.SendMessage(ctx, channelID, resultMsg)
}
