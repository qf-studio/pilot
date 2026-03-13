package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/intent"
	"github.com/alekspetrov/pilot/internal/logging"
)

// Handler processes incoming Discord events and coordinates task execution.
type Handler struct {
	gatewayClient     *GatewayClient
	apiClient         *Client
	notifier          *Notifier // GH-2132: task lifecycle notifications
	runner            *executor.Runner
	allowedGuilds     map[string]bool
	allowedChannels   map[string]bool
	pendingTasks      map[string]*PendingTaskInfo
	mu                sync.Mutex
	stopCh            chan struct{}
	stopOnce          sync.Once
	wg                sync.WaitGroup
	log               *slog.Logger
	botID             string
	projectPath       string
	llmClassifier     intent.Classifier
	conversationStore *intent.ConversationStore
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
	BotID           string
	AllowedGuilds   []string
	AllowedChannels []string
	ProjectPath     string
	LLMClassifier   *LLMClassifierConfig
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

	projectPath := config.ProjectPath
	if projectPath == "" {
		projectPath = "."
	}

	h := &Handler{
		gatewayClient:   NewGatewayClient(config.BotToken, DefaultIntents),
		apiClient:       NewClient(config.BotToken),
		runner:          runner,
		allowedGuilds:   allowedGuilds,
		allowedChannels: allowedChannels,
		pendingTasks:    make(map[string]*PendingTaskInfo),
		stopCh:          make(chan struct{}),
		log:             logging.WithComponent("discord.handler"),
		botID:           config.BotID,
		projectPath:     projectPath,
	}

	// Initialize LLM classifier if configured
	if config.LLMClassifier != nil && config.LLMClassifier.Enabled {
		apiKey := config.LLMClassifier.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if apiKey != "" {
			h.llmClassifier = intent.NewAnthropicClient(apiKey)

			historySize := 10
			if config.LLMClassifier.HistorySize > 0 {
				historySize = config.LLMClassifier.HistorySize
			}
			historyTTL := 30 * time.Minute
			if config.LLMClassifier.HistoryTTL > 0 {
				historyTTL = config.LLMClassifier.HistoryTTL
			}
			h.conversationStore = intent.NewConversationStore(historySize, historyTTL)

			h.log.Info("LLM intent classifier enabled")
		} else {
			h.log.Warn("LLM classifier enabled but no API key found")
		}
	}

	return h
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

// Stop gracefully stops the handler. Safe to call multiple times.
func (h *Handler) Stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})
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
// Collects expired tasks under lock, releases lock, then sends messages.
func (h *Handler) cleanupExpiredTasks(ctx context.Context) {
	h.mu.Lock()
	expiry := time.Now().Add(-5 * time.Minute)
	var expired []*PendingTaskInfo
	var expiredKeys []string
	for channelID, task := range h.pendingTasks {
		if task.CreatedAt.Before(expiry) {
			expired = append(expired, task)
			expiredKeys = append(expiredKeys, channelID)
		}
	}
	for _, key := range expiredKeys {
		delete(h.pendingTasks, key)
	}
	h.mu.Unlock()

	// Send expiry messages outside the lock
	for _, task := range expired {
		_, _ = h.apiClient.SendMessage(ctx, task.ChannelID, "⏰ Task "+task.TaskID+" expired (no confirmation received).")
		h.log.Debug("Expired pending task",
			slog.String("task_id", task.TaskID),
			slog.String("channel_id", task.ChannelID))
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

	// Detect intent (uses LLM if available, regex fallback)
	msgIntent := h.detectIntentWithLLM(ctx, msg.ChannelID, text)
	h.log.Debug("Message received",
		slog.String("channel_id", msg.ChannelID),
		slog.String("author_id", msg.Author.ID),
		slog.String("intent", string(msgIntent)),
		slog.String("text", TruncateText(text, 50)))

	// Record user message in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(msg.ChannelID, "user", text)
	}

	switch msgIntent {
	case intent.IntentCommand:
		// Commands start with / — no handler wired yet
		return
	case intent.IntentGreeting:
		h.handleGreeting(ctx, msg.ChannelID, msg.Author.Username)
	case intent.IntentQuestion:
		h.handleQuestion(ctx, msg.ChannelID, text)
	case intent.IntentResearch:
		h.handleResearch(ctx, msg.ChannelID, text)
	case intent.IntentPlanning:
		h.handlePlanning(ctx, msg.ChannelID, text)
	case intent.IntentChat:
		h.handleChat(ctx, msg.ChannelID, text)
	case intent.IntentTask:
		h.handleTask(ctx, msg.ChannelID, msg.Author.ID, text)
	default:
		h.handleChat(ctx, msg.ChannelID, text)
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

	// Handle button actions
	switch interaction.Data.CustomID {
	case "execute_task":
		h.handleConfirmation(ctx, interaction.ChannelID, userID, true)
	case "cancel_task":
		h.handleConfirmation(ctx, interaction.ChannelID, userID, false)
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

// detectIntentWithLLM uses LLM classification with regex fallback.
func (h *Handler) detectIntentWithLLM(ctx context.Context, channelID, text string) intent.Intent {
	if h.llmClassifier == nil {
		return intent.DetectIntent(text)
	}

	// Fast path: commands always use regex
	if strings.HasPrefix(text, "/") {
		return intent.IntentCommand
	}

	// Fast path: clear question patterns skip LLM
	if intent.IsClearQuestion(text) {
		return intent.IntentQuestion
	}

	// Try LLM classification with timeout
	classifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var history []intent.ConversationMessage
	if h.conversationStore != nil {
		history = h.conversationStore.Get(channelID)
	}

	classified, err := h.llmClassifier.Classify(classifyCtx, history, text)
	if err != nil {
		h.log.Debug("LLM classification failed, using regex", slog.Any("error", err))
		return intent.DetectIntent(text)
	}

	h.log.Debug("LLM classified intent",
		slog.String("channel_id", channelID),
		slog.String("intent", string(classified)),
		slog.String("text", TruncateText(text, 50)))

	return classified
}

// handleGreeting responds to greetings without task execution.
func (h *Handler) handleGreeting(ctx context.Context, channelID, username string) {
	_, _ = h.apiClient.SendMessage(ctx, channelID, FormatGreeting())
}

// handleQuestion answers questions about the codebase using read-only execution.
func (h *Handler) handleQuestion(ctx context.Context, channelID, question string) {
	if h.runner == nil {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "❌ Executor not available.")
		return
	}
	_, _ = h.apiClient.SendMessage(ctx, channelID, "🔍 Looking into that...")

	taskID := fmt.Sprintf("Q-%d", time.Now().UnixNano())
	task := &executor.Task{
		ID:    taskID,
		Title: "Question: " + TruncateText(question, 40),
		Description: fmt.Sprintf(`Answer this question about the codebase. DO NOT make any changes, only read and analyze.

Question: %s

IMPORTANT: Be concise. Limit your exploration to 5-10 files max. Provide a brief, direct answer.`, question),
		ProjectPath: h.projectPath,
	}

	questionCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	result, err := h.runner.Execute(questionCtx, task)
	if err != nil {
		if questionCtx.Err() == context.DeadlineExceeded {
			_, _ = h.apiClient.SendMessage(ctx, channelID, "⏱ Question timed out. Try something more specific.")
		} else {
			_, _ = h.apiClient.SendMessage(ctx, channelID, "❌ Couldn't answer that question. Try rephrasing.")
		}
		return
	}

	answer := CleanInternalSignals(result.Output)
	if answer == "" {
		answer = "I couldn't find a clear answer to that question."
	}
	h.sendChunked(ctx, channelID, answer)
}

// handleResearch handles research/analysis requests with read-only execution.
func (h *Handler) handleResearch(ctx context.Context, channelID, query string) {
	if h.runner == nil {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "❌ Executor not available.")
		return
	}
	_, _ = h.apiClient.SendMessage(ctx, channelID, "🔬 Researching...")

	taskID := fmt.Sprintf("RES-%d", time.Now().UnixNano())
	task := &executor.Task{
		ID:    taskID,
		Title: "Research: " + TruncateText(query, 40),
		Description: fmt.Sprintf(`Research and analyze: %s

Provide findings in a structured format with:
- Executive summary
- Key findings
- Relevant code/files if applicable
- Recommendations

DO NOT make any code changes. This is a read-only research task.`, query),
		ProjectPath: h.projectPath,
	}

	researchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	result, err := h.runner.Execute(researchCtx, task)
	if err != nil {
		if researchCtx.Err() == context.DeadlineExceeded {
			_, _ = h.apiClient.SendMessage(ctx, channelID, "⏱ Research timed out. Try a more specific query.")
		} else {
			_, _ = h.apiClient.SendMessage(ctx, channelID, fmt.Sprintf("❌ Research failed: %s", err.Error()))
		}
		return
	}

	content := CleanInternalSignals(result.Output)
	if content == "" {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "🤷 No research findings to report.")
		return
	}
	h.sendChunked(ctx, channelID, content)
}

// handlePlanning creates an implementation plan and offers Execute/Cancel buttons.
func (h *Handler) handlePlanning(ctx context.Context, channelID, request string) {
	if h.runner == nil {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "❌ Executor not available.")
		return
	}
	_, _ = h.apiClient.SendMessage(ctx, channelID, "📐 Drafting plan...")

	taskID := fmt.Sprintf("PLAN-%d", time.Now().UnixNano())
	task := &executor.Task{
		ID:    taskID,
		Title: "Plan: " + TruncateText(request, 40),
		Description: fmt.Sprintf(`Create an implementation plan for: %s

Explore the codebase and propose a detailed plan. Include:
1. Summary of approach
2. Files to modify/create
3. Step-by-step implementation phases
4. Potential risks or considerations

DO NOT make any code changes. Only explore and plan.`, request),
		ProjectPath: h.projectPath,
	}

	planCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	result, err := h.runner.Execute(planCtx, task)
	if err != nil {
		if planCtx.Err() == context.DeadlineExceeded {
			_, _ = h.apiClient.SendMessage(ctx, channelID, "⏱ Planning timed out. Try a simpler request.")
		} else {
			_, _ = h.apiClient.SendMessage(ctx, channelID, fmt.Sprintf("❌ Planning failed: %s", err.Error()))
		}
		return
	}

	planContent := CleanInternalSignals(result.Output)
	if planContent == "" {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "🤷 The task may be too simple for planning. Try executing it directly.")
		return
	}

	// Truncate plan for Discord display
	display := planContent
	if len(display) > 1800 {
		display = display[:1800] + "\n\n_(truncated)_"
	}

	_, _ = h.apiClient.SendMessage(ctx, channelID, fmt.Sprintf("📋 **Implementation Plan**\n\n%s", display))
}

// handleChat handles conversational messages with read-only execution.
func (h *Handler) handleChat(ctx context.Context, channelID, message string) {
	if h.runner == nil {
		_, _ = h.apiClient.SendMessage(ctx, channelID, "❌ Executor not available.")
		return
	}
	_, _ = h.apiClient.SendMessage(ctx, channelID, "💬 Thinking...")

	taskID := fmt.Sprintf("CHAT-%d", time.Now().UnixNano())
	task := &executor.Task{
		ID:    taskID,
		Title: "Chat: " + TruncateText(message, 30),
		Description: fmt.Sprintf(`You are Pilot, an AI assistant for the codebase at %s.

The user wants to have a conversation (not execute a task).
Respond helpfully and conversationally. You can reference project knowledge but DO NOT make code changes.

Be concise - this is a chat conversation, not a report. Keep response under 500 words.

User message: %s`, h.projectPath, message),
		ProjectPath: h.projectPath,
	}

	chatCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	result, err := h.runner.Execute(chatCtx, task)
	if err != nil {
		if chatCtx.Err() == context.DeadlineExceeded {
			_, _ = h.apiClient.SendMessage(ctx, channelID, "⏱ Took too long to respond. Try a simpler question.")
		} else {
			_, _ = h.apiClient.SendMessage(ctx, channelID, "Sorry, I couldn't process that. Try rephrasing?")
		}
		return
	}

	response := CleanInternalSignals(result.Output)
	if response == "" {
		response = "I'm not sure how to respond to that. Could you rephrase?"
	}
	h.sendChunked(ctx, channelID, response)
}

// sendChunked sends content in Discord-safe chunks (max 2000 chars).
func (h *Handler) sendChunked(ctx context.Context, channelID, content string) {
	chunks := ChunkContent(content, 1900)
	for i, chunk := range chunks {
		msg := chunk
		if len(chunks) > 1 {
			msg = fmt.Sprintf("📄 Part %d/%d\n\n%s", i+1, len(chunks), chunk)
		}
		_, _ = h.apiClient.SendMessage(ctx, channelID, msg)
	}
}

// handleTask creates and sends a confirmation prompt for a task.
func (h *Handler) handleTask(ctx context.Context, channelID, userID, description string) {
	// Generate task ID using UnixNano to avoid collisions
	taskID := fmt.Sprintf("DISCORD-%d", time.Now().UnixNano())

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
	text := FormatTaskConfirmation(taskID, description, h.projectPath)
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
	// GH-2132: Notify task started via notifier
	if h.notifier != nil {
		if err := h.notifier.NotifyTaskStarted(ctx, channelID, taskID, description); err != nil {
			h.log.Warn("Failed to send task started notification", slog.Any("error", err))
		}
	}

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
		ProjectPath: h.projectPath,
		Verbose:     false,
		Branch:      fmt.Sprintf("pilot/%s", taskID),
		BaseBranch:  "main",
		CreatePR:    true,
	}

	// Set up per-task progress callback using named callbacks
	callbackName := "discord-" + taskID
	var lastPhase string
	var lastProgress int
	var lastUpdate time.Time

	if progressMsgID != "" && h.runner != nil {
		h.runner.AddProgressCallback(callbackName, func(tid string, phase string, progress int, message string) {
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

	// Remove per-task progress callback
	if h.runner != nil {
		h.runner.RemoveProgressCallback(callbackName)
	}

	// Send result
	if err != nil {
		errMsg := fmt.Sprintf("❌ Task failed\n%s\n\n%s", taskID, err.Error())
		_, _ = h.apiClient.SendMessage(ctx, channelID, errMsg)
		// GH-2132: Notify failure via notifier
		if h.notifier != nil {
			_ = h.notifier.NotifyTaskFailed(ctx, channelID, taskID, err.Error())
		}
		return
	}

	// Format and send result
	output := CleanInternalSignals(result.Output)
	prURL := result.PRUrl
	resultMsg := FormatTaskResult(output, true, prURL)

	_, _ = h.apiClient.SendMessage(ctx, channelID, resultMsg)

	// GH-2132: Notify completion via notifier
	if h.notifier != nil {
		_ = h.notifier.NotifyTaskCompleted(ctx, channelID, taskID, output, prURL)
	}
}
