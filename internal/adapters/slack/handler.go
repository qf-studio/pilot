package slack

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/comms"
	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/intent"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
)

// MemberResolver resolves a Slack user to a team member ID for RBAC (GH-786).
// Decoupled from teams package to avoid import cycles.
type MemberResolver interface {
	// ResolveSlackIdentity maps a Slack user ID and/or email to a member ID.
	// Returns ("", nil) when no match is found (= skip RBAC).
	ResolveSlackIdentity(slackUserID, email string) (string, error)
}

// PendingTask represents a task awaiting confirmation.
type PendingTask struct {
	TaskID      string
	Description string
	ChannelID   string
	ThreadTS    string
	MessageTS   string
	UserID      string // Slack user ID of the sender for RBAC
	CreatedAt   time.Time
}

// Handler processes incoming Slack events and coordinates task execution.
// Mirrors Telegram handler's RBAC support with memberResolver and lastSender tracking.
type Handler struct {
	socketClient      *SocketModeClient
	apiClient         *Client
	memberResolver    MemberResolver            // Team member resolver for RBAC (optional, GH-786)
	lastSender        map[string]string         // channelID -> last sender Slack user ID
	runner            *executor.Runner          // Task executor
	projects          comms.ProjectSource       // Project source for multi-project support
	projectPath       string                    // Default/fallback project path
	activeProject     map[string]string         // channelID -> projectPath (active project per channel)
	pendingTasks      map[string]*PendingTask   // channelID -> pending task
	allowedChannels   map[string]bool           // Allowed channel IDs for security
	allowedUsers      map[string]bool           // Allowed user IDs for security
	conversationStore *intent.ConversationStore // Conversation history per channel
	rateLimiter       *comms.RateLimiter        // Rate limiter for DoS protection
	llmClassifier     intent.Classifier         // LLM intent classifier (optional)
	mu                sync.Mutex
	stopCh            chan struct{}
	wg                sync.WaitGroup
	store             *memory.Store             // Memory store for history/queue/budget (optional)
	log               *slog.Logger
}

// HandlerConfig holds configuration for the Slack handler.
type HandlerConfig struct {
	AppToken        string               // Slack app-level token (xapp-...)
	BotToken        string               // Slack bot token (xoxb-...)
	MemberResolver  MemberResolver       // Team member resolver for RBAC (optional, GH-786)
	ProjectPath     string               // Default/fallback project path
	Projects        comms.ProjectSource  // Project source for multi-project support
	AllowedChannels []string             // Channel IDs allowed to send tasks
	AllowedUsers    []string             // User IDs allowed to send tasks
	RateLimit       *comms.RateLimitConfig // Rate limiting config (optional)
	LLMClassifier   *LLMClassifierConfig // LLM intent classification config (optional)
	Store           *memory.Store          // Memory store for history/queue/budget (optional)
}

// LLMClassifierConfig holds configuration for the LLM classifier.
type LLMClassifierConfig struct {
	Enabled     bool
	APIKey      string
	HistorySize int
	HistoryTTL  time.Duration
}

// NewHandler creates a new Slack event handler.
func NewHandler(config *HandlerConfig, runner *executor.Runner) *Handler {
	allowedChannels := make(map[string]bool)
	for _, id := range config.AllowedChannels {
		allowedChannels[id] = true
	}

	allowedUsers := make(map[string]bool)
	for _, id := range config.AllowedUsers {
		allowedUsers[id] = true
	}

	// Determine default project path
	projectPath := config.ProjectPath
	if projectPath == "" && config.Projects != nil {
		if defaultProj := config.Projects.GetDefaultProject(); defaultProj != nil {
			projectPath = defaultProj.Path
		}
	}

	// Initialize rate limiter
	var rateLimiter *comms.RateLimiter
	if config.RateLimit != nil {
		rateLimiter = comms.NewRateLimiter(config.RateLimit)
	} else {
		rateLimiter = comms.NewRateLimiter(comms.DefaultRateLimitConfig())
	}

	h := &Handler{
		socketClient:    NewSocketModeClient(config.AppToken),
		apiClient:       NewClient(config.BotToken),
		memberResolver:  config.MemberResolver,
		lastSender:      make(map[string]string),
		runner:          runner,
		projects:        config.Projects,
		projectPath:     projectPath,
		activeProject:   make(map[string]string),
		pendingTasks:    make(map[string]*PendingTask),
		allowedChannels: allowedChannels,
		allowedUsers:    allowedUsers,
		rateLimiter:     rateLimiter,
		store:           config.Store,
		stopCh:          make(chan struct{}),
		log:             logging.WithComponent("slack.handler"),
	}

	// Initialize LLM classifier if configured
	if config.LLMClassifier != nil && config.LLMClassifier.Enabled {
		apiKey := config.LLMClassifier.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if apiKey != "" {
			h.llmClassifier = intent.NewAnthropicClient(apiKey)

			// Set up conversation store with defaults
			historySize := 10
			if config.LLMClassifier.HistorySize > 0 {
				historySize = config.LLMClassifier.HistorySize
			}
			historyTTL := 30 * time.Minute
			if config.LLMClassifier.HistoryTTL > 0 {
				historyTTL = config.LLMClassifier.HistoryTTL
			}
			h.conversationStore = intent.NewConversationStore(historySize, historyTTL)

			h.log.Info("LLM intent classifier enabled", slog.String("model", "claude-3-5-haiku"))
		} else {
			h.log.Warn("LLM classifier enabled but no API key found")
		}
	}

	return h
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

// StartListening starts listening for Slack events via Socket Mode.
// It blocks until ctx is cancelled or Stop() is called.
func (h *Handler) StartListening(ctx context.Context) error {
	events, err := h.socketClient.Listen(ctx)
	if err != nil {
		return fmt.Errorf("failed to start Socket Mode listener: %w", err)
	}

	h.log.Info("Slack Socket Mode listener started")

	// Start cleanup goroutine for expired pending tasks
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
			// Notify user that task expired
			_, _ = h.apiClient.PostMessage(ctx, &Message{
				Channel:  task.ChannelID,
				Text:     fmt.Sprintf("⏰ Task %s expired (no confirmation received).", task.TaskID),
				ThreadTS: task.ThreadTS,
			})
			delete(h.pendingTasks, channelID)
			h.log.Debug("Expired pending task",
				slog.String("task_id", task.TaskID),
				slog.String("channel_id", channelID))
		}
	}
}

// processEvent handles a single Slack event.
func (h *Handler) processEvent(ctx context.Context, event *SocketEvent) {
	// Ignore bot messages to avoid feedback loops
	if event.IsBotMessage() {
		return
	}

	channelID := event.ChannelID
	userID := event.UserID
	text := strings.TrimSpace(event.Text)

	// Track sender for RBAC resolution
	h.TrackSender(channelID, userID)

	// Security check: only process from allowed channels/users
	if !h.isAllowed(channelID, userID) {
		h.log.Debug("Ignoring message from unauthorized channel/user",
			slog.String("channel_id", channelID),
			slog.String("user_id", userID))
		return
	}

	// Rate limiting check
	if h.rateLimiter != nil && !h.rateLimiter.AllowMessage(channelID) {
		h.log.Warn("Rate limit exceeded",
			slog.String("channel_id", channelID),
			slog.String("type", "message"))
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     "⚠️ Rate limit exceeded. Please wait a moment before sending more messages.",
			ThreadTS: event.ThreadTS,
		})
		return
	}

	// Skip if no text
	if text == "" {
		return
	}

	// Check for confirmation responses
	textLower := strings.ToLower(text)
	if textLower == "yes" || textLower == "y" || textLower == "execute" || textLower == "confirm" {
		h.handleConfirmation(ctx, channelID, event.ThreadTS, true)
		return
	}
	if textLower == "no" || textLower == "n" || textLower == "cancel" || textLower == "abort" {
		h.handleConfirmation(ctx, channelID, event.ThreadTS, false)
		return
	}

	// Detect intent
	detectedIntent := h.detectIntent(ctx, channelID, text)
	h.log.Debug("Message received",
		slog.String("channel_id", channelID),
		slog.String("intent", string(detectedIntent)))

	// Record user message in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(channelID, "user", text)
	}

	switch detectedIntent {
	case intent.IntentGreeting:
		h.handleGreeting(ctx, channelID, event.ThreadTS)
	case intent.IntentQuestion:
		h.handleQuestion(ctx, channelID, event.ThreadTS, text)
	case intent.IntentResearch:
		h.handleResearch(ctx, channelID, event.ThreadTS, text)
	case intent.IntentPlanning:
		h.handlePlanning(ctx, channelID, event.ThreadTS, text)
	case intent.IntentChat:
		h.handleChat(ctx, channelID, event.ThreadTS, text)
	case intent.IntentTask:
		h.handleTask(ctx, channelID, event.ThreadTS, text, userID)
	default:
		// Fallback to chat for conversational messages
		h.handleChat(ctx, channelID, event.ThreadTS, text)
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

// detectIntent uses LLM classification with regex fallback.
func (h *Handler) detectIntent(ctx context.Context, channelID, text string) intent.Intent {
	// Fast path: commands always use regex
	if strings.HasPrefix(text, "/") {
		return intent.IntentCommand
	}

	// Fast path: clear question patterns don't need LLM verification
	if intent.IsClearQuestion(text) {
		return intent.IntentQuestion
	}

	// If LLM classifier not available, use regex
	if h.llmClassifier == nil {
		return intent.DetectIntent(text)
	}

	// Try LLM classification with timeout
	classifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Get conversation history
	var history []intent.ConversationMessage
	if h.conversationStore != nil {
		history = h.conversationStore.Get(channelID)
	}

	detectedIntent, err := h.llmClassifier.Classify(classifyCtx, history, text)
	if err != nil {
		h.log.Debug("LLM classification failed, using regex",
			slog.Any("error", err))
		return intent.DetectIntent(text)
	}

	h.log.Debug("LLM classified intent",
		slog.String("channel_id", channelID),
		slog.String("intent", string(detectedIntent)),
		slog.String("text", truncateText(text, 50)))

	return detectedIntent
}

// getActiveProjectPath returns the active project path for a channel.
func (h *Handler) getActiveProjectPath(channelID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if path, ok := h.activeProject[channelID]; ok {
		return path
	}
	return h.projectPath
}

// handleGreeting responds to greetings.
func (h *Handler) handleGreeting(ctx context.Context, channelID, threadTS string) {
	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     FormatGreeting(""),
		ThreadTS: threadTS,
	})
}

// handleQuestion handles questions about the codebase.
func (h *Handler) handleQuestion(ctx context.Context, channelID, threadTS, question string) {
	// Send acknowledgment
	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     FormatQuestionAck(),
		ThreadTS: threadTS,
	})

	// Create a timeout context for questions (90 seconds max)
	questionCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	// Create a read-only prompt for Claude
	prompt := fmt.Sprintf(`Answer this question about the codebase. DO NOT make any changes, only read and analyze.

Question: %s

IMPORTANT: Be concise. Limit your exploration to 5-10 files max. Provide a brief, direct answer.
If the question is too broad, ask for clarification instead of exploring everything.`, question)

	// Create a read-only task (no branch, no PR)
	taskID := fmt.Sprintf("Q-%d", time.Now().Unix())
	task := &executor.Task{
		ID:          taskID,
		Title:       "Question: " + truncateText(question, 40),
		Description: prompt,
		ProjectPath: h.getActiveProjectPath(channelID),
		Verbose:     false,
	}

	// Execute with timeout context
	h.log.Debug("Answering question",
		slog.String("task_id", taskID),
		slog.String("channel_id", channelID))
	result, err := h.runner.Execute(questionCtx, task)

	if err != nil {
		var errMsg string
		if questionCtx.Err() == context.DeadlineExceeded {
			errMsg = "⏱ Question timed out. Try asking something more specific."
		} else {
			errMsg = "❌ Sorry, I couldn't answer that question. Try rephrasing it."
		}
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     errMsg,
			ThreadTS: threadTS,
		})
		return
	}

	// Format and send answer
	answer := CleanInternalSignals(result.Output)
	if answer == "" {
		answer = "I couldn't find a clear answer to that question."
	}

	// Send chunks if answer is long
	chunks := ChunkContent(answer, 3800)
	for _, chunk := range chunks {
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     chunk,
			ThreadTS: threadTS,
		})
		time.Sleep(200 * time.Millisecond) // Small delay between chunks
	}
}

// handleResearch handles research/analysis requests.
func (h *Handler) handleResearch(ctx context.Context, channelID, threadTS, query string) {
	// Send acknowledgment
	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     "🔬 Researching...",
		ThreadTS: threadTS,
	})

	// Create research task (no branch, no PR)
	taskID := fmt.Sprintf("RES-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Research: " + truncateText(query, 40),
		Description: fmt.Sprintf(`Research and analyze: %s

Provide findings in a structured format with:
- Executive summary
- Key findings
- Relevant code/files if applicable
- Recommendations

DO NOT make any code changes. This is a read-only research task.`, query),
		ProjectPath: h.getActiveProjectPath(channelID),
		CreatePR:    false,
	}

	// Execute with timeout (3 minutes for research)
	researchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	h.log.Info("Executing research",
		slog.String("task_id", taskID),
		slog.String("channel_id", channelID),
		slog.String("query", truncateText(query, 50)))
	result, err := h.runner.Execute(researchCtx, task)

	if err != nil {
		var errMsg string
		if researchCtx.Err() == context.DeadlineExceeded {
			errMsg = "⏱ Research timed out. Try a more specific query."
		} else {
			errMsg = fmt.Sprintf("❌ Research failed: %s", err.Error())
		}
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     errMsg,
			ThreadTS: threadTS,
		})
		return
	}

	// Send content to Slack (chunked if long)
	content := CleanInternalSignals(result.Output)
	if content == "" {
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     FormatResearchOutput(""),
			ThreadTS: threadTS,
		})
		return
	}

	chunks := ChunkContent(content, 3800)
	for i, chunk := range chunks {
		msg := chunk
		if len(chunks) > 1 {
			msg = fmt.Sprintf("📄 Part %d/%d\n\n%s", i+1, len(chunks), chunk)
		}
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     msg,
			ThreadTS: threadTS,
		})
		time.Sleep(300 * time.Millisecond) // Small delay between chunks
	}
}

// handlePlanning handles planning requests.
func (h *Handler) handlePlanning(ctx context.Context, channelID, threadTS, request string) {
	// Send acknowledgment
	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     "📐 Drafting plan...",
		ThreadTS: threadTS,
	})

	// Create planning task (read-only exploration)
	taskID := fmt.Sprintf("PLAN-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Plan: " + truncateText(request, 40),
		Description: fmt.Sprintf(`Create an implementation plan for: %s

Explore the codebase and propose a detailed plan. Include:
1. Summary of approach
2. Files to modify/create
3. Step-by-step implementation phases
4. Potential risks or considerations

DO NOT make any code changes. Only explore and plan.`, request),
		ProjectPath: h.getActiveProjectPath(channelID),
		CreatePR:    false,
	}

	// Execute with timeout from config (default 2 minutes for planning)
	planTimeout := 2 * time.Minute
	if h.runner.Config() != nil && h.runner.Config().PlanningTimeout > 0 {
		planTimeout = h.runner.Config().PlanningTimeout
	}
	planCtx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()

	h.log.Info("Creating plan",
		slog.String("task_id", taskID),
		slog.String("channel_id", channelID))
	result, err := h.runner.Execute(planCtx, task)

	if err != nil {
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     planningErrorMessage(err, planCtx.Err()),
			ThreadTS: threadTS,
		})
		return
	}

	planContent := CleanInternalSignals(result.Output)
	if planContent == "" {
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     planEmptyMessage(result.Error, result.Success),
			ThreadTS: threadTS,
		})
		return
	}

	// Store the plan as a pending task for execution
	h.mu.Lock()
	h.pendingTasks[channelID] = &PendingTask{
		TaskID:      taskID,
		Description: fmt.Sprintf("## Implementation Plan\n\n%s\n\n## Original Request\n\n%s", planContent, request),
		ChannelID:   channelID,
		ThreadTS:    threadTS,
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	// Send plan summary with execute buttons
	summary := FormatPlanSummary(planContent)
	blocks := BuildConfirmationBlocks(taskID, summary)

	resp, err := h.apiClient.PostInteractiveMessage(ctx, &InteractiveMessage{
		Channel: channelID,
		Text:    fmt.Sprintf("📋 Implementation Plan\n\n%s", summary),
		Blocks:  blocks,
	})
	if err != nil {
		h.log.Warn("Failed to send confirmation with buttons, falling back to text",
			slog.Any("error", err))
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     fmt.Sprintf("📋 Implementation Plan\n\n%s\n\n_Reply yes to execute or no to cancel._", summary),
			ThreadTS: threadTS,
		})
	} else if resp != nil {
		h.mu.Lock()
		if p, ok := h.pendingTasks[channelID]; ok {
			p.MessageTS = resp.TS
		}
		h.mu.Unlock()
	}
}

// handleChat handles conversational messages.
func (h *Handler) handleChat(ctx context.Context, channelID, threadTS, message string) {
	// Send typing indicator
	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     "💬 Thinking...",
		ThreadTS: threadTS,
	})

	// Create chat task (read-only, conversational)
	taskID := fmt.Sprintf("CHAT-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Chat: " + truncateText(message, 30),
		Description: fmt.Sprintf(`You are Pilot, an AI assistant for the codebase at %s.

The user wants to have a conversation (not execute a task).
Respond helpfully and conversationally. You can reference project knowledge but DO NOT make code changes.

Be concise - this is a chat conversation, not a report. Keep response under 500 words.

User message: %s`, h.getActiveProjectPath(channelID), message),
		ProjectPath: h.getActiveProjectPath(channelID),
		CreatePR:    false,
	}

	// Execute with short timeout (60 seconds for chat)
	chatCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	h.log.Debug("Chat response",
		slog.String("task_id", taskID),
		slog.String("channel_id", channelID))
	result, err := h.runner.Execute(chatCtx, task)

	if err != nil {
		var errMsg string
		if chatCtx.Err() == context.DeadlineExceeded {
			errMsg = "⏱ Took too long to respond. Try a simpler question."
		} else {
			errMsg = "Sorry, I couldn't process that. Try rephrasing?"
		}
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     errMsg,
			ThreadTS: threadTS,
		})
		return
	}

	// Clean and send response
	response := CleanInternalSignals(result.Output)
	if response == "" {
		response = "I'm not sure how to respond to that. Could you rephrase?"
	}

	// Truncate if too long
	if len(response) > 3800 {
		response = response[:3797] + "..."
	}

	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     response,
		ThreadTS: threadTS,
	})

	// Record assistant response in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(channelID, "assistant", truncateText(response, 500))
	}
}

// handleTask handles task requests with confirmation.
func (h *Handler) handleTask(ctx context.Context, channelID, threadTS, description, userID string) {
	// Check task rate limit
	if h.rateLimiter != nil && !h.rateLimiter.AllowTask(channelID) {
		remaining := h.rateLimiter.GetRemainingTasks(channelID)
		h.log.Warn("Task rate limit exceeded",
			slog.String("channel_id", channelID),
			slog.Int("remaining", remaining))
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     "⚠️ Task rate limit exceeded. You've submitted too many tasks recently. Please wait before submitting more.",
			ThreadTS: threadTS,
		})
		return
	}

	// Check if there's already a pending task
	h.mu.Lock()
	if existing, exists := h.pendingTasks[channelID]; exists {
		h.mu.Unlock()
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     fmt.Sprintf("⚠️ You already have a pending task: %s\n\nReply yes to execute or no to cancel.", existing.TaskID),
			ThreadTS: threadTS,
		})
		return
	}
	h.mu.Unlock()

	// Generate task ID
	taskID := fmt.Sprintf("SLACK-%d", time.Now().Unix())

	h.mu.Lock()
	// Create pending task
	pending := &PendingTask{
		TaskID:      taskID,
		Description: description,
		ChannelID:   channelID,
		ThreadTS:    threadTS,
		UserID:      userID,
		CreatedAt:   time.Now(),
	}
	h.pendingTasks[channelID] = pending
	h.mu.Unlock()

	// Send confirmation message with buttons
	confirmMsg := FormatTaskConfirmation(taskID, description, h.getActiveProjectPath(channelID))
	blocks := BuildConfirmationBlocks(taskID, truncateText(description, 500))

	resp, err := h.apiClient.PostInteractiveMessage(ctx, &InteractiveMessage{
		Channel: channelID,
		Text:    confirmMsg,
		Blocks:  blocks,
	})

	if err != nil {
		h.log.Warn("Failed to send confirmation with buttons, falling back to text",
			slog.Any("error", err))
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     confirmMsg + "\n\n_Reply yes to execute or no to cancel._",
			ThreadTS: threadTS,
		})
	} else if resp != nil {
		h.mu.Lock()
		if p, ok := h.pendingTasks[channelID]; ok {
			p.MessageTS = resp.TS
		}
		h.mu.Unlock()
	}
}

// handleConfirmation handles task confirmation or cancellation.
func (h *Handler) handleConfirmation(ctx context.Context, channelID, threadTS string, confirmed bool) {
	h.mu.Lock()
	pending, exists := h.pendingTasks[channelID]
	if exists {
		delete(h.pendingTasks, channelID)
	}
	h.mu.Unlock()

	if !exists {
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     "No pending task to confirm.",
			ThreadTS: threadTS,
		})
		return
	}

	if confirmed {
		h.executeTask(ctx, channelID, pending.ThreadTS, pending.TaskID, pending.Description)
	} else {
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     fmt.Sprintf("❌ Task %s cancelled.", pending.TaskID),
			ThreadTS: pending.ThreadTS,
		})
	}
}

// handleCallback processes button clicks from interactive messages.
func (h *Handler) HandleCallback(ctx context.Context, channelID, userID, actionID, messageTS string) {
	// Track sender for RBAC resolution
	h.TrackSender(channelID, userID)

	switch actionID {
	case "execute_task":
		h.handleConfirmation(ctx, channelID, "", true)
	case "cancel_task":
		h.handleConfirmation(ctx, channelID, "", false)
	}
}

// executeTask executes a confirmed task.
func (h *Handler) executeTask(ctx context.Context, channelID, threadTS, taskID, description string) {
	// Determine if this is an ephemeral task (shouldn't create PR)
	createPR := true
	if intent.IsEphemeralTask(description) {
		createPR = false
		h.log.Debug("Ephemeral task detected - skipping PR creation",
			slog.String("task_id", taskID),
			slog.String("description", truncateText(description, 50)))
	}

	// Send execution started message
	prNote := ""
	if !createPR {
		prNote = " (no PR)"
	}
	progressMsg := FormatProgressUpdate(taskID, "Starting"+prNote, 0, "Initializing...")
	resp, err := h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     progressMsg,
		ThreadTS: threadTS,
	})
	if err != nil {
		h.log.Warn("Failed to send start message", slog.Any("error", err))
	}

	var progressMsgTS string
	if resp != nil {
		progressMsgTS = resp.TS
	}

	// Create task for executor
	branch := ""
	baseBranch := ""
	if createPR {
		branch = fmt.Sprintf("pilot/%s", taskID)
		baseBranch = "main"
	}

	task := &executor.Task{
		ID:          taskID,
		Title:       truncateText(description, 50),
		Description: description,
		ProjectPath: h.getActiveProjectPath(channelID),
		Verbose:     false,
		Branch:      branch,
		BaseBranch:  baseBranch,
		CreatePR:    createPR,
		MemberID:    h.resolveMemberID(channelID), // RBAC lookup
	}

	// Set up progress callback with throttling
	var lastPhase string
	var lastProgress int
	var lastUpdate time.Time

	if progressMsgTS != "" && h.runner != nil {
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
			_ = h.apiClient.UpdateMessage(ctx, channelID, progressMsgTS, &Message{
				Channel: channelID,
				Text:    updateText,
			})
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
		_, _ = h.apiClient.PostMessage(ctx, &Message{
			Channel:  channelID,
			Text:     errMsg,
			ThreadTS: threadTS,
		})
		return
	}

	// Format and send result
	output := CleanInternalSignals(result.Output)
	prURL := result.PRUrl
	resultMsg := FormatTaskResult(output, true, prURL)

	_, _ = h.apiClient.PostMessage(ctx, &Message{
		Channel:  channelID,
		Text:     resultMsg,
		ThreadTS: threadTS,
	})
}
