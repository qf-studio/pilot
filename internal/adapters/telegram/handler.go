package telegram

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/alekspetrov/pilot/internal/transcription"
)

// MemberResolver resolves a Telegram user to a team member ID for RBAC (GH-634).
// Decoupled from teams package to avoid import cycles.
type MemberResolver interface {
	// ResolveTelegramIdentity maps a Telegram user ID and/or email to a member ID.
	// Returns ("", nil) when no match is found (= skip RBAC).
	ResolveTelegramIdentity(telegramID int64, email string) (string, error)
}

// PendingTask represents a task awaiting confirmation
type PendingTask struct {
	TaskID      string
	Description string
	ChatID      string
	MessageID   int64
	SenderID    int64 // Telegram user ID of the sender for RBAC (GH-634)
	CreatedAt   time.Time
}

// RunningTask represents a task currently being executed
type RunningTask struct {
	TaskID    string
	ChatID    string
	StartedAt time.Time
	Cancel    context.CancelFunc
}

// Handler processes incoming Telegram messages and executes tasks
type Handler struct {
	client            *Client
	runner            *executor.Runner
	projects          ProjectSource           // Project source for multi-project support
	projectPath       string                  // Default/fallback project path
	activeProject     map[string]string       // chatID -> projectPath (active project per chat)
	allowedIDs        map[int64]bool          // Allowed user/chat IDs for security
	offset            int64                   // Last processed update ID
	pendingTasks      map[string]*PendingTask // ChatID -> pending task
	runningTasks      map[string]*RunningTask // ChatID -> running task
	mu                sync.Mutex
	stopCh            chan struct{}
	wg                sync.WaitGroup
	transcriber       *transcription.Service // Voice transcription service (optional)
	transcriptionErr  error                  // Error from transcription init (for guidance)
	store             *memory.Store          // Memory store for history/queue/budget (optional)
	cmdHandler        *CommandHandler        // Command handler for /commands
	plainTextMode     bool                   // Use plain text instead of Markdown
	rateLimiter       *RateLimiter           // Rate limiter for DoS protection
	llmClassifier     *AnthropicClient       // LLM intent classifier (optional)
	conversationStore *ConversationStore     // Conversation history per chat (optional)
	memberResolver    MemberResolver         // Team member resolver for RBAC (optional, GH-634)
	lastSender        map[string]int64       // chatID -> last sender Telegram user ID (GH-634)
}

// HandlerConfig holds configuration for the Telegram handler
type HandlerConfig struct {
	BotToken      string
	ProjectPath   string                // Default/fallback project path
	Projects      ProjectSource         // Project source for multi-project support
	AllowedIDs    []int64               // User/chat IDs allowed to send tasks
	Transcription *transcription.Config // Voice transcription config (optional)
	Store         *memory.Store         // Memory store for history/queue/budget (optional)
	PlainTextMode bool                  // Use plain text instead of Markdown (default: true)
	RateLimit     *RateLimitConfig      // Rate limiting config (optional)
	LLMClassifier  *LLMClassifierConfig  // LLM intent classification config (optional)
	MemberResolver MemberResolver        // Team member resolver for RBAC (optional, GH-634)
}

// NewHandler creates a new Telegram message handler
func NewHandler(config *HandlerConfig, runner *executor.Runner) *Handler {
	allowedIDs := make(map[int64]bool)
	for _, id := range config.AllowedIDs {
		allowedIDs[id] = true
	}

	// Determine default project path
	projectPath := config.ProjectPath
	if projectPath == "" && config.Projects != nil {
		if defaultProj := config.Projects.GetDefaultProject(); defaultProj != nil {
			projectPath = defaultProj.Path
		}
	}

	// Initialize rate limiter
	var rateLimiter *RateLimiter
	if config.RateLimit != nil {
		rateLimiter = NewRateLimiter(config.RateLimit)
	} else {
		// Use defaults if not configured
		rateLimiter = NewRateLimiter(DefaultRateLimitConfig())
	}

	h := &Handler{
		client:        NewClient(config.BotToken),
		runner:        runner,
		projects:      config.Projects,
		projectPath:   projectPath,
		activeProject: make(map[string]string),
		allowedIDs:    allowedIDs,
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		stopCh:        make(chan struct{}),
		store:          config.Store,
		plainTextMode:  config.PlainTextMode,
		rateLimiter:    rateLimiter,
		memberResolver: config.MemberResolver,
		lastSender:     make(map[string]int64),
	}

	// Initialize command handler
	h.cmdHandler = NewCommandHandler(h, config.Store)

	// Initialize transcription service if configured
	if config.Transcription != nil {
		svc, err := transcription.NewService(config.Transcription)
		if err != nil {
			h.transcriptionErr = err
			logging.WithComponent("telegram").Warn("Transcription not available", slog.Any("error", err))
		} else {
			h.transcriber = svc
			logging.WithComponent("telegram").Debug("Voice transcription enabled", slog.String("backend", svc.BackendName()))
		}
	}

	// Initialize LLM classifier if configured
	if config.LLMClassifier != nil && config.LLMClassifier.Enabled {
		apiKey := config.LLMClassifier.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if apiKey != "" {
			h.llmClassifier = NewAnthropicClient(apiKey)

			// Set up conversation store with defaults
			historySize := 10
			if config.LLMClassifier.HistorySize > 0 {
				historySize = config.LLMClassifier.HistorySize
			}
			historyTTL := 30 * time.Minute
			if config.LLMClassifier.HistoryTTL > 0 {
				historyTTL = config.LLMClassifier.HistoryTTL
			}
			h.conversationStore = NewConversationStore(historySize, historyTTL)

			logging.WithComponent("telegram").Info("LLM intent classifier enabled",
				slog.String("model", "claude-3-5-haiku"))
		} else {
			logging.WithComponent("telegram").Warn("LLM classifier enabled but no API key found")
		}
	}

	return h
}

// getActiveProjectPath returns the active project path for a chat
func (h *Handler) getActiveProjectPath(chatID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()

	if path, ok := h.activeProject[chatID]; ok {
		return path
	}
	return h.projectPath
}

// setActiveProject sets the active project for a chat by name
func (h *Handler) setActiveProject(chatID, projectName string) (*ProjectInfo, error) {
	if h.projects == nil {
		return nil, fmt.Errorf("no projects configured")
	}

	proj := h.projects.GetProjectByName(projectName)
	if proj == nil {
		return nil, fmt.Errorf("project '%s' not found", projectName)
	}

	h.mu.Lock()
	h.activeProject[chatID] = proj.Path
	h.mu.Unlock()

	return proj, nil
}

// getActiveProjectInfo returns the active project info for a chat
func (h *Handler) getActiveProjectInfo(chatID string) *ProjectInfo {
	if h.projects == nil {
		return nil
	}

	path := h.getActiveProjectPath(chatID)
	return h.projects.GetProjectByPath(path)
}

// getParseMode returns the parse mode based on plainTextMode setting.
// Returns empty string for plain text, "Markdown" for markdown mode.
func (h *Handler) getParseMode() string {
	if h.plainTextMode {
		return ""
	}
	return "Markdown"
}

// CheckSingleton verifies no other bot instance is already running.
// Returns ErrConflict if another instance is detected.
func (h *Handler) CheckSingleton(ctx context.Context) error {
	return h.client.CheckSingleton(ctx)
}

// StartPolling starts polling for updates in a goroutine
func (h *Handler) StartPolling(ctx context.Context) {
	h.wg.Add(1)
	go h.pollLoop(ctx)

	// Start cleanup goroutine for expired pending tasks
	h.wg.Add(1)
	go h.cleanupLoop(ctx)
}

// Stop gracefully stops the polling loop
func (h *Handler) Stop() {
	close(h.stopCh)
	h.wg.Wait()
}

// pollLoop continuously polls for updates
func (h *Handler) pollLoop(ctx context.Context) {
	defer h.wg.Done()

	logging.WithComponent("telegram").Debug("Starting poll loop")

	for {
		select {
		case <-ctx.Done():
			logging.WithComponent("telegram").Debug("Poll loop stopped")
			return
		case <-h.stopCh:
			logging.WithComponent("telegram").Debug("Poll loop stopped")
			return
		default:
			h.fetchAndProcess(ctx)
		}
	}
}

// cleanupLoop removes expired pending tasks
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

// cleanupExpiredTasks removes tasks pending for more than 5 minutes
func (h *Handler) cleanupExpiredTasks(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	expiry := time.Now().Add(-5 * time.Minute)
	for chatID, task := range h.pendingTasks {
		if task.CreatedAt.Before(expiry) {
			// Notify user that task expired
			_, _ = h.client.SendMessage(ctx, chatID,
				fmt.Sprintf("⏰ Task %s expired (no confirmation received).", task.TaskID), "")
			delete(h.pendingTasks, chatID)
			logging.WithComponent("telegram").Debug("Expired pending task", slog.String("task_id", task.TaskID), slog.String("chat_id", chatID))
		}
	}
}

// fetchAndProcess fetches updates and processes them
func (h *Handler) fetchAndProcess(ctx context.Context) {
	// Use long polling with 30 second timeout
	updates, err := h.client.GetUpdates(ctx, h.offset, 30)
	if err != nil {
		// Don't spam logs on context cancellation
		if ctx.Err() == nil {
			logging.WithComponent("telegram").Warn("Error fetching updates", slog.Any("error", err))
		}
		// Brief pause before retry on error
		time.Sleep(time.Second)
		return
	}

	for _, update := range updates {
		h.processUpdate(ctx, update)
		// Update offset to acknowledge this update
		h.mu.Lock()
		if update.UpdateID >= h.offset {
			h.offset = update.UpdateID + 1
		}
		h.mu.Unlock()
	}
}

// processUpdate handles a single update
func (h *Handler) processUpdate(ctx context.Context, update *Update) {
	// Handle callback queries (button clicks)
	if update.CallbackQuery != nil {
		h.handleCallback(ctx, update.CallbackQuery)
		return
	}

	if update.Message == nil {
		return
	}

	msg := update.Message
	chatID := strconv.FormatInt(msg.Chat.ID, 10)

	// Track sender for RBAC resolution (GH-634)
	if msg.From != nil && msg.From.ID != 0 {
		h.mu.Lock()
		h.lastSender[chatID] = msg.From.ID
		h.mu.Unlock()
	}

	// Rate limiting check for all message types
	if h.rateLimiter != nil && !h.rateLimiter.AllowMessage(chatID) {
		logging.WithComponent("telegram").Warn("Rate limit exceeded",
			slog.String("chat_id", chatID), slog.String("type", "message"))
		_, _ = h.client.SendMessage(ctx, chatID,
			"⚠️ Rate limit exceeded. Please wait a moment before sending more messages.", "")
		return
	}

	// Handle photo messages
	if len(msg.Photo) > 0 {
		h.handlePhoto(ctx, chatID, msg)
		return
	}

	// Handle voice messages
	if msg.Voice != nil {
		h.handleVoice(ctx, chatID, msg)
		return
	}

	// Skip if no text
	if msg.Text == "" {
		return
	}

	// Security check: only process messages from allowed users/chats
	if len(h.allowedIDs) > 0 {
		senderID := int64(0)
		if msg.From != nil {
			senderID = msg.From.ID
		}

		if !h.allowedIDs[msg.Chat.ID] && !h.allowedIDs[senderID] {
			logging.WithComponent("telegram").Debug("Ignoring message from unauthorized chat/user",
				slog.Int64("chat_id", msg.Chat.ID), slog.Int64("sender_id", senderID))
			return
		}
	}

	text := strings.TrimSpace(msg.Text)

	// Check for confirmation responses
	textLower := strings.ToLower(text)
	if textLower == "yes" || textLower == "y" || textLower == "execute" || textLower == "confirm" {
		h.handleConfirmation(ctx, chatID, true)
		return
	}
	if textLower == "no" || textLower == "n" || textLower == "cancel" || textLower == "abort" {
		h.handleConfirmation(ctx, chatID, false)
		return
	}

	// Detect intent (uses LLM if available, regex fallback)
	intent := h.detectIntentWithLLM(ctx, chatID, text)
	logging.WithComponent("telegram").Debug("Message received",
		slog.String("chat_id", chatID), slog.String("intent", string(intent)))

	// Record user message in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(chatID, "user", text)
	}

	switch intent {
	case IntentCommand:
		h.handleCommand(ctx, chatID, text)
	case IntentGreeting:
		h.handleGreeting(ctx, chatID, msg.From)
	case IntentQuestion:
		h.handleQuestion(ctx, chatID, text)
	case IntentResearch:
		h.handleResearch(ctx, chatID, text)
	case IntentPlanning:
		h.handlePlanning(ctx, chatID, text)
	case IntentChat:
		h.handleChat(ctx, chatID, text)
	case IntentTask:
		h.handleTask(ctx, chatID, text)
	default:
		// Fallback to chat for conversational messages
		h.handleChat(ctx, chatID, text)
	}
}

// detectIntentWithLLM uses LLM classification with regex fallback
func (h *Handler) detectIntentWithLLM(ctx context.Context, chatID, text string) Intent {
	// If LLM classifier not available, use regex
	if h.llmClassifier == nil {
		return DetectIntent(text)
	}

	// Fast path: commands always use regex
	if strings.HasPrefix(text, "/") {
		return IntentCommand
	}

	// Fast path: clear question patterns don't need LLM verification
	// Prevents Haiku from misclassifying "What's in roadmap" as Task
	if isClearQuestion(text) {
		return IntentQuestion
	}

	// Try LLM classification with timeout
	classifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Get conversation history
	var history []ConversationMessage
	if h.conversationStore != nil {
		history = h.conversationStore.Get(chatID)
	}

	intent, err := h.llmClassifier.Classify(classifyCtx, history, text)
	if err != nil {
		logging.WithComponent("telegram").Debug("LLM classification failed, using regex",
			slog.Any("error", err))
		return DetectIntent(text)
	}

	logging.WithComponent("telegram").Debug("LLM classified intent",
		slog.String("chat_id", chatID),
		slog.String("intent", string(intent)),
		slog.String("text", truncateDescription(text, 50)))

	return intent
}

// handleCallback processes callback queries from inline keyboards
func (h *Handler) handleCallback(ctx context.Context, callback *CallbackQuery) {
	if callback.Message == nil {
		return
	}

	chatID := strconv.FormatInt(callback.Message.Chat.ID, 10)
	data := callback.Data

	// Track sender from callback for RBAC resolution (GH-634)
	if callback.From != nil && callback.From.ID != 0 {
		h.mu.Lock()
		h.lastSender[chatID] = callback.From.ID
		h.mu.Unlock()
	}

	// Answer callback to remove loading state
	_ = h.client.AnswerCallback(ctx, callback.ID, "")

	switch {
	case data == "execute":
		h.handleConfirmation(ctx, chatID, true)
	case data == "cancel":
		h.handleConfirmation(ctx, chatID, false)
	case strings.HasPrefix(data, "switch_"):
		projectName := strings.TrimPrefix(data, "switch_")
		h.cmdHandler.HandleCallbackSwitch(ctx, chatID, projectName)
	case data == "voice_check_status":
		h.sendVoiceSetupPrompt(ctx, chatID)
	}
}

// handleConfirmation handles task confirmation or cancellation
func (h *Handler) handleConfirmation(ctx context.Context, chatID string, confirmed bool) {
	h.mu.Lock()
	pending, exists := h.pendingTasks[chatID]
	if exists {
		delete(h.pendingTasks, chatID)
	}
	h.mu.Unlock()

	if !exists {
		_, _ = h.client.SendMessage(ctx, chatID, "No pending task to confirm.", "")
		return
	}

	if confirmed {
		h.executeTask(ctx, chatID, pending.TaskID, pending.Description)
	} else {
		_, _ = h.client.SendMessage(ctx, chatID,
			fmt.Sprintf("❌ Task %s cancelled.", pending.TaskID), "")
	}
}

// handleGreeting responds to greetings
func (h *Handler) handleGreeting(ctx context.Context, chatID string, from *User) {
	username := ""
	if from != nil {
		username = from.FirstName
	}
	_, _ = h.client.SendMessage(ctx, chatID, FormatGreeting(username), "")
}

// handleQuestion handles questions about the codebase
func (h *Handler) handleQuestion(ctx context.Context, chatID, question string) {
	// Try fast path first for common questions
	if answer := h.tryFastAnswer(question); answer != "" {
		logging.WithComponent("telegram").Debug("Fast answer used", slog.String("chat_id", chatID))
		_, _ = h.client.SendMessage(ctx, chatID, answer, "")
		return
	}

	// Send acknowledgment for slow path
	_, _ = h.client.SendMessage(ctx, chatID, FormatQuestionAck(), "")

	// Create a timeout context for questions (90 seconds max)
	questionCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	// Create a read-only prompt for Claude
	// Be explicit about being concise to avoid extensive exploration
	prompt := fmt.Sprintf(`Answer this question about the codebase. DO NOT make any changes, only read and analyze.

Question: %s

IMPORTANT: Be concise. Limit your exploration to 5-10 files max. Provide a brief, direct answer.
If the question is too broad, ask for clarification instead of exploring everything.`, question)

	// Create a read-only task (no branch, no PR)
	taskID := fmt.Sprintf("Q-%d", time.Now().Unix())
	task := &executor.Task{
		ID:          taskID,
		Title:       "Question: " + truncateDescription(question, 40),
		Description: prompt,
		ProjectPath: h.projectPath,
		Verbose:     false,
	}

	// Execute with timeout context
	logging.WithTask(taskID).Debug("Answering question", slog.String("chat_id", chatID))
	result, err := h.runner.Execute(questionCtx, task)

	if err != nil {
		if questionCtx.Err() == context.DeadlineExceeded {
			_, _ = h.client.SendMessage(ctx, chatID,
				"⏱ Question timed out. Try asking something more specific.", "")
		} else {
			_, _ = h.client.SendMessage(ctx, chatID,
				"❌ Sorry, I couldn't answer that question. Try rephrasing it.", "")
		}
		return
	}

	// Format and send answer
	answer := FormatQuestionAnswer(result.Output)
	if answer == "" {
		answer = "I couldn't find a clear answer to that question."
	}

	_, _ = h.client.SendMessage(ctx, chatID, answer, "")
}

// handleResearch handles research/analysis requests
// Executes Claude Code in read-only mode and returns analysis directly to chat
func (h *Handler) handleResearch(ctx context.Context, chatID, query string) {
	// Send acknowledgment
	_, _ = h.client.SendMessage(ctx, chatID, "🔬 Researching...", "")

	// Create research task (no branch, no PR)
	taskID := fmt.Sprintf("RES-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Research: " + truncateDescription(query, 40),
		Description: fmt.Sprintf(`Research and analyze: %s

Provide findings in a structured format with:
- Executive summary
- Key findings
- Relevant code/files if applicable
- Recommendations

DO NOT make any code changes. This is a read-only research task.`, query),
		ProjectPath: h.getActiveProjectPath(chatID),
		CreatePR:    false,
	}

	// Execute with timeout (3 minutes for research)
	researchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	logging.WithTask(taskID).Info("Executing research", slog.String("chat_id", chatID), slog.String("query", truncateDescription(query, 50)))
	result, err := h.runner.Execute(researchCtx, task)

	if err != nil {
		if researchCtx.Err() == context.DeadlineExceeded {
			_, _ = h.client.SendMessage(ctx, chatID, "⏱ Research timed out. Try a more specific query.", "")
		} else {
			_, _ = h.client.SendMessage(ctx, chatID, fmt.Sprintf("❌ Research failed: %s", err.Error()), "")
		}
		return
	}

	// Send content to Telegram (chunked if long)
	h.sendResearchOutput(ctx, chatID, query, result)
}

// sendResearchOutput sends research findings to Telegram, chunking if necessary
func (h *Handler) sendResearchOutput(ctx context.Context, chatID, query string, result *executor.ExecutionResult) {
	content := cleanInternalSignals(result.Output)
	if content == "" {
		_, _ = h.client.SendMessage(ctx, chatID, "🤷 No research findings to report.", "")
		return
	}

	// Chunk content for Telegram (4096 char limit, use 3800 for safety)
	chunks := chunkContent(content, 3800)

	for i, chunk := range chunks {
		msg := chunk
		if len(chunks) > 1 {
			msg = fmt.Sprintf("📄 Part %d/%d\n\n%s", i+1, len(chunks), chunk)
		}
		_, _ = h.client.SendMessage(ctx, chatID, msg, "")

		// Small delay between chunks to avoid rate limiting
		if i < len(chunks)-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Optionally save to file
	h.saveResearchFile(query, content)
}

// saveResearchFile saves research output to .agent/research/ directory
func (h *Handler) saveResearchFile(query, content string) {
	// Create .agent/research/ directory if it doesn't exist
	researchDir := filepath.Join(h.projectPath, ".agent", "research")
	if err := os.MkdirAll(researchDir, 0755); err != nil {
		return // Silent fail for file save
	}

	// Generate filename from query
	slug := strings.ToLower(query)
	slug = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	filename := fmt.Sprintf("%s-%d.md", slug, time.Now().Unix())

	// Save file
	filePath := filepath.Join(researchDir, filename)
	_ = os.WriteFile(filePath, []byte(content), 0644)

	logging.WithComponent("telegram").Debug("Saved research file", slog.String("path", filePath))
}

// handlePlanning handles planning requests
// Explores codebase and creates implementation plan, then offers Execute/Cancel
func (h *Handler) handlePlanning(ctx context.Context, chatID, request string) {
	// Send acknowledgment
	_, _ = h.client.SendMessage(ctx, chatID, "📐 Drafting plan...", "")

	// Create planning task (read-only exploration)
	taskID := fmt.Sprintf("PLAN-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Plan: " + truncateDescription(request, 40),
		Description: fmt.Sprintf(`Create an implementation plan for: %s

Explore the codebase and propose a detailed plan. Include:
1. Summary of approach
2. Files to modify/create
3. Step-by-step implementation phases
4. Potential risks or considerations

DO NOT make any code changes. Only explore and plan.`, request),
		ProjectPath: h.getActiveProjectPath(chatID),
		CreatePR:    false,
	}

	// Execute with timeout (2 minutes for planning)
	planCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	logging.WithTask(taskID).Info("Creating plan", slog.String("chat_id", chatID))
	result, err := h.runner.Execute(planCtx, task)

	if err != nil {
		if planCtx.Err() == context.DeadlineExceeded {
			_, _ = h.client.SendMessage(ctx, chatID, "⏱ Planning timed out. Try a simpler request.", "")
		} else {
			_, _ = h.client.SendMessage(ctx, chatID, fmt.Sprintf("❌ Planning failed: %s", err.Error()), "")
		}
		return
	}

	planContent := cleanInternalSignals(result.Output)
	if planContent == "" {
		_, _ = h.client.SendMessage(ctx, chatID, "🤷 Could not generate a plan. Try being more specific.", "")
		return
	}

	// Store the plan as a pending task for execution
	h.mu.Lock()
	h.pendingTasks[chatID] = &PendingTask{
		TaskID:      taskID,
		Description: fmt.Sprintf("## Implementation Plan\n\n%s\n\n## Original Request\n\n%s", planContent, request),
		ChatID:      chatID,
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	// Send plan summary with execute button
	summary := extractPlanSummary(planContent)
	_, _ = h.client.SendMessageWithKeyboard(ctx, chatID,
		fmt.Sprintf("📋 Implementation Plan\n\n%s", summary), "",
		[][]InlineKeyboardButton{
			{
				{Text: "✅ Execute", CallbackData: "execute"},
				{Text: "❌ Cancel", CallbackData: "cancel"},
			},
		})
}

// extractPlanSummary extracts key points from a plan for display
func extractPlanSummary(plan string) string {
	lines := strings.Split(plan, "\n")
	var summary []string
	lineCount := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		summary = append(summary, trimmed)
		lineCount++

		if lineCount >= 20 {
			break
		}
	}

	result := strings.Join(summary, "\n")
	if len(result) > 2000 {
		result = result[:2000] + "\n\n_(truncated)_"
	}

	return result
}

// handleChat handles conversational messages
// Responds conversationally without code execution or PR creation
func (h *Handler) handleChat(ctx context.Context, chatID, message string) {
	// Send typing indicator
	_, _ = h.client.SendMessage(ctx, chatID, "💬 Thinking...", "")

	// Create chat task (read-only, conversational)
	taskID := fmt.Sprintf("CHAT-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Chat: " + truncateDescription(message, 30),
		Description: fmt.Sprintf(`You are Pilot, an AI assistant for the codebase at %s.

The user wants to have a conversation (not execute a task).
Respond helpfully and conversationally. You can reference project knowledge but DO NOT make code changes.

Be concise - this is a chat conversation, not a report. Keep response under 500 words.

User message: %s`, h.getActiveProjectPath(chatID), message),
		ProjectPath: h.getActiveProjectPath(chatID),
		CreatePR:    false,
	}

	// Execute with short timeout (60 seconds for chat)
	chatCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	logging.WithTask(taskID).Debug("Chat response", slog.String("chat_id", chatID))
	result, err := h.runner.Execute(chatCtx, task)

	if err != nil {
		if chatCtx.Err() == context.DeadlineExceeded {
			_, _ = h.client.SendMessage(ctx, chatID, "⏱ Took too long to respond. Try a simpler question.", "")
		} else {
			_, _ = h.client.SendMessage(ctx, chatID, "Sorry, I couldn't process that. Try rephrasing?", "")
		}
		return
	}

	// Clean and send response
	response := cleanInternalSignals(result.Output)
	if response == "" {
		response = "I'm not sure how to respond to that. Could you rephrase?"
	}

	// Truncate if too long
	if len(response) > 4000 {
		response = response[:3997] + "..."
	}

	_, _ = h.client.SendMessage(ctx, chatID, response, "")

	// Record assistant response in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(chatID, "assistant", truncateDescription(response, 500))
	}
}

// handleTask handles task requests with confirmation
func (h *Handler) handleTask(ctx context.Context, chatID, description string) {
	// Check task rate limit
	if h.rateLimiter != nil && !h.rateLimiter.AllowTask(chatID) {
		remaining := h.rateLimiter.GetRemainingTasks(chatID)
		logging.WithComponent("telegram").Warn("Task rate limit exceeded",
			slog.String("chat_id", chatID), slog.Int("remaining", remaining))
		_, _ = h.client.SendMessage(ctx, chatID,
			"⚠️ Task rate limit exceeded. You've submitted too many tasks recently. Please wait before submitting more.", "")
		return
	}

	// Check if there's already a pending task
	h.mu.Lock()
	if existing, exists := h.pendingTasks[chatID]; exists {
		h.mu.Unlock()
		_, _ = h.client.SendMessage(ctx, chatID,
			fmt.Sprintf("⚠️ You already have a pending task: %s\n\nReply yes to execute or no to cancel.",
				existing.TaskID), "")
		return
	}
	h.mu.Unlock()

	// Try to resolve task ID from description (e.g., "Start task 07" → TASK-07)
	taskID := ""
	displayDesc := description
	if taskInfo := h.resolveTaskFromDescription(description); taskInfo != nil {
		taskID = taskInfo.FullID
		displayDesc = fmt.Sprintf("%s: %s", taskInfo.FullID, taskInfo.Title)
		// Load full task description for execution
		if fullDesc := h.loadTaskDescription(taskInfo); fullDesc != "" {
			description = fullDesc
		}
	} else {
		// Fallback to generated ID for free-form tasks
		taskID = fmt.Sprintf("TG-%d", time.Now().Unix())
	}

	h.mu.Lock()

	// Create pending task
	pending := &PendingTask{
		TaskID:      taskID,
		Description: description,
		ChatID:      chatID,
		CreatedAt:   time.Now(),
	}
	h.pendingTasks[chatID] = pending
	h.mu.Unlock()

	// Send confirmation message with inline keyboard
	// Use displayDesc for user-friendly display, description is kept for execution
	confirmMsg := FormatTaskConfirmation(taskID, displayDesc, h.projectPath)

	msgResp, err := h.client.SendMessageWithKeyboard(ctx, chatID, confirmMsg, "",
		[][]InlineKeyboardButton{
			{
				{Text: "✅ Execute", CallbackData: "execute"},
				{Text: "❌ Cancel", CallbackData: "cancel"},
			},
		})

	if err != nil {
		logging.WithComponent("telegram").Warn("Failed to send confirmation", slog.Any("error", err))
		// Fallback to text-based confirmation
		_, _ = h.client.SendMessage(ctx, chatID,
			confirmMsg+"\n\n_Reply yes to execute or no to cancel._", "")
	} else if msgResp != nil && msgResp.Result != nil {
		h.mu.Lock()
		if p, ok := h.pendingTasks[chatID]; ok {
			p.MessageID = msgResp.Result.MessageID
		}
		h.mu.Unlock()
	}
}

// executeTask executes a confirmed task with automatic ephemeral detection
func (h *Handler) executeTask(ctx context.Context, chatID, taskID, description string) {
	// Determine if this is an ephemeral task (shouldn't create PR)
	createPR := true

	// Check if ephemeral detection is enabled (default: true)
	detectEphemeral := true
	if h.runner != nil && h.runner.Config() != nil && h.runner.Config().DetectEphemeral != nil {
		detectEphemeral = *h.runner.Config().DetectEphemeral
	}

	if detectEphemeral && IsEphemeralTask(description) {
		createPR = false
		logging.WithTask(taskID).Debug("Ephemeral task detected - skipping PR creation",
			slog.String("description", truncateDescription(description, 50)))
	}

	h.executeTaskWithOptions(ctx, chatID, taskID, description, createPR)
}

// resolveMemberID resolves the current Telegram sender to a team member ID (GH-634).
// Returns "" if no resolver is configured or no match is found.
func (h *Handler) resolveMemberID(chatID string) string {
	if h.memberResolver == nil {
		return ""
	}

	h.mu.Lock()
	senderID := h.lastSender[chatID]
	h.mu.Unlock()

	if senderID == 0 {
		return ""
	}

	memberID, err := h.memberResolver.ResolveTelegramIdentity(senderID, "")
	if err != nil {
		logging.WithComponent("telegram").Warn("failed to resolve Telegram identity",
			slog.Int64("telegram_id", senderID),
			slog.Any("error", err))
		return ""
	}

	if memberID != "" {
		logging.WithComponent("telegram").Debug("resolved Telegram user to team member",
			slog.Int64("telegram_id", senderID),
			slog.String("member_id", memberID))
	}

	return memberID
}

// executeTaskWithOptions executes a task with explicit PR creation control
func (h *Handler) executeTaskWithOptions(ctx context.Context, chatID, taskID, description string, createPR bool) {
	// Send execution started message (this will be updated with progress)
	// NOTE: Using plain text (no parse mode) to avoid markdown escaping issues.
	// See SOP: .agent/sops/telegram-bot-development.md
	prNote := ""
	if !createPR {
		prNote = " (no PR)"
	}
	resp, err := h.client.SendMessage(ctx, chatID, FormatProgressUpdate(taskID, "Starting"+prNote, 0, "Initializing..."), "")
	if err != nil {
		logging.WithTask(taskID).Warn("Failed to send start message", slog.Any("error", err))
		// Fallback to simple message
		_, _ = h.client.SendMessage(ctx, chatID, FormatTaskStarted(taskID, description), "")
	}

	// Track progress message ID for updates
	var progressMsgID int64
	if resp != nil && resp.Result != nil {
		progressMsgID = resp.Result.MessageID
		logging.WithTask(taskID).Debug("Progress message ready", slog.Int64("message_id", progressMsgID))
	} else {
		logging.WithTask(taskID).Warn("No progress message ID - progress updates disabled")
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
		Title:       truncateDescription(description, 50),
		Description: description,
		ProjectPath: h.projectPath,
		Verbose:     false,
		Branch:      branch,
		BaseBranch:  baseBranch,
		CreatePR:    createPR,
		MemberID:    h.resolveMemberID(chatID), // GH-634: RBAC lookup
	}

	// Set up progress callback with throttling
	var lastPhase string
	var lastProgress int
	var lastUpdate time.Time

	if progressMsgID != 0 {
		logging.WithTask(taskID).Debug("Setting up progress callback")
		h.runner.OnProgress(func(tid string, phase string, progress int, message string) {
			// Only update for our task
			if tid != taskID {
				return
			}

			logging.WithTask(taskID).Debug("Progress update",
				slog.String("phase", phase), slog.Int("progress", progress))

			// Throttle updates: phase change OR progress change >= 15% OR 3 seconds elapsed
			now := time.Now()
			phaseChanged := phase != lastPhase
			progressChanged := progress-lastProgress >= 15
			timeElapsed := now.Sub(lastUpdate) >= 3*time.Second

			if !phaseChanged && !progressChanged && !timeElapsed {
				return
			}

			// Update tracking state
			lastPhase = phase
			lastProgress = progress
			lastUpdate = now

			// Send update
			updateText := FormatProgressUpdate(taskID, phase, progress, message)
			if err := h.client.EditMessage(ctx, chatID, progressMsgID, updateText, ""); err != nil {
				logging.WithTask(taskID).Warn("Failed to edit progress message", slog.Any("error", err))
			}
		})
	} else {
		logging.WithTask(taskID).Warn("Progress callback NOT set (no message ID)")
	}

	// Execute task
	logging.WithTask(taskID).Info("Executing task", slog.String("chat_id", chatID))
	result, err := h.runner.Execute(ctx, task)

	// Clear progress callback
	h.runner.OnProgress(nil)

	// Send result with clean formatting
	if err != nil {
		errMsg := fmt.Sprintf("❌ Task failed\n%s\n\n%s", taskID, err.Error())
		_, _ = h.client.SendMessage(ctx, chatID, errMsg, "")
		return
	}

	result.TaskID = taskID // Ensure TaskID is set
	_, _ = h.client.SendMessage(ctx, chatID, FormatTaskResult(result), "")
}

// handleCommand processes bot commands
func (h *Handler) handleCommand(ctx context.Context, chatID, text string) {
	// Delegate to command handler
	h.cmdHandler.HandleCommand(ctx, chatID, text)
}

// handleRunCommand executes a task directly without confirmation
func (h *Handler) handleRunCommand(ctx context.Context, chatID, taskIDInput string) {
	// Check if already running a task
	h.mu.Lock()
	if running := h.runningTasks[chatID]; running != nil {
		h.mu.Unlock()
		elapsed := time.Since(running.StartedAt).Round(time.Second)
		_, _ = h.client.SendMessage(ctx, chatID,
			fmt.Sprintf("⚠️ Already running %s (%s)\n\nUse /stop to cancel it first.", running.TaskID, elapsed), "")
		return
	}
	h.mu.Unlock()

	// Resolve task ID
	taskInfo := h.resolveTaskID(taskIDInput)
	if taskInfo == nil {
		_, _ = h.client.SendMessage(ctx, chatID,
			fmt.Sprintf("❌ Task %s not found\n\nUse /tasks to see available tasks.", taskIDInput), "")
		return
	}

	// Load task description
	description := h.loadTaskDescription(taskInfo)
	if description == "" {
		_, _ = h.client.SendMessage(ctx, chatID,
			fmt.Sprintf("❌ Could not load task %s", taskInfo.FullID), "")
		return
	}

	// Notify user
	_, _ = h.client.SendMessage(ctx, chatID,
		fmt.Sprintf("🚀 Starting task\n\n%s: %s", taskInfo.FullID, taskInfo.Title), "")

	// Execute directly
	h.executeTask(ctx, chatID, taskInfo.FullID, fmt.Sprintf("## Task: %s\n\n%s", taskInfo.FullID, description))
}

// handlePhoto processes photo messages
func (h *Handler) handlePhoto(ctx context.Context, chatID string, msg *Message) {
	// Security check: only process from allowed users/chats
	if len(h.allowedIDs) > 0 {
		senderID := int64(0)
		if msg.From != nil {
			senderID = msg.From.ID
		}
		if !h.allowedIDs[msg.Chat.ID] && !h.allowedIDs[senderID] {
			logging.WithComponent("telegram").Debug("Ignoring photo from unauthorized chat/user",
				slog.Int64("chat_id", msg.Chat.ID), slog.Int64("sender_id", senderID))
			return
		}
	}

	// Get the largest photo size (last in array)
	photo := msg.Photo[len(msg.Photo)-1]
	logging.WithComponent("telegram").Debug("Received photo",
		slog.String("chat_id", chatID), slog.Int("width", photo.Width), slog.Int("height", photo.Height))

	// Send acknowledgment
	_, _ = h.client.SendMessage(ctx, chatID, "📷 Processing image...", "")

	// Download the image
	imagePath, err := h.downloadImage(ctx, photo.FileID)
	if err != nil {
		logging.WithComponent("telegram").Warn("Failed to download image", slog.Any("error", err))
		_, _ = h.client.SendMessage(ctx, chatID, "❌ Failed to download image. Please try again.", "")
		return
	}
	defer func() {
		// Cleanup temp file after processing
		_ = os.Remove(imagePath)
	}()

	// Build prompt with image context
	prompt := msg.Caption
	if prompt == "" {
		prompt = "Analyze this image and describe what you see."
	}

	// Execute with image
	h.executeImageTask(ctx, chatID, imagePath, prompt)
}

// handleVoice processes voice messages
func (h *Handler) handleVoice(ctx context.Context, chatID string, msg *Message) {
	// Security check: only process from allowed users/chats
	if len(h.allowedIDs) > 0 {
		senderID := int64(0)
		if msg.From != nil {
			senderID = msg.From.ID
		}
		if !h.allowedIDs[msg.Chat.ID] && !h.allowedIDs[senderID] {
			logging.WithComponent("telegram").Debug("Ignoring voice from unauthorized chat/user",
				slog.Int64("chat_id", msg.Chat.ID), slog.Int64("sender_id", senderID))
			return
		}
	}

	// Check if transcription is available
	if h.transcriber == nil {
		logging.WithComponent("telegram").Debug("Voice message received but transcription not configured")
		msg := h.voiceNotAvailableMessage()
		_, _ = h.client.SendMessage(ctx, chatID, msg, "")
		return
	}

	voice := msg.Voice
	logging.WithComponent("telegram").Debug("Received voice",
		slog.String("chat_id", chatID), slog.Int("duration", voice.Duration))

	// Send acknowledgment
	_, _ = h.client.SendMessage(ctx, chatID, "🎤 Transcribing voice message...", "")

	// Download the voice file
	audioPath, err := h.downloadAudio(ctx, voice.FileID)
	if err != nil {
		logging.WithComponent("telegram").Warn("Failed to download voice", slog.Any("error", err))
		_, _ = h.client.SendMessage(ctx, chatID, "❌ Failed to download voice message. Please try again.", "")
		return
	}
	defer func() {
		_ = os.Remove(audioPath)
	}()

	// Transcribe the audio
	transcribeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	result, err := h.transcriber.Transcribe(transcribeCtx, audioPath)
	if err != nil {
		logging.WithComponent("telegram").Warn("Transcription failed", slog.Any("error", err))
		_, _ = h.client.SendMessage(ctx, chatID,
			"❌ Failed to transcribe voice message. Please try again or send as text.", "")
		return
	}

	if result.Text == "" {
		_, _ = h.client.SendMessage(ctx, chatID,
			"🤷 Couldn't understand the voice message. Please try again or send as text.", "")
		return
	}

	// Show the transcription to the user
	langInfo := ""
	if result.Language != "" && result.Language != "unknown" {
		langInfo = fmt.Sprintf(" (%s)", result.Language)
	}

	transcriptMsg := fmt.Sprintf("🎤 Transcribed%s:\n%s", langInfo, result.Text)
	_, _ = h.client.SendMessage(ctx, chatID, transcriptMsg, "")

	// Process the transcribed text as if it was typed
	logging.WithComponent("telegram").Debug("Processing transcribed text", slog.String("chat_id", chatID))

	// Detect intent and handle (uses LLM if available)
	text := strings.TrimSpace(result.Text)
	intent := h.detectIntentWithLLM(ctx, chatID, text)

	// Record transcribed message in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(chatID, "user", text)
	}

	switch intent {
	case IntentCommand:
		h.handleCommand(ctx, chatID, text)
	case IntentGreeting:
		h.handleGreeting(ctx, chatID, msg.From)
	case IntentQuestion:
		h.handleQuestion(ctx, chatID, text)
	case IntentResearch:
		h.handleResearch(ctx, chatID, text)
	case IntentPlanning:
		h.handlePlanning(ctx, chatID, text)
	case IntentChat:
		h.handleChat(ctx, chatID, text)
	case IntentTask:
		h.handleTask(ctx, chatID, text)
	default:
		h.handleChat(ctx, chatID, text) // Default to chat, not task
	}
}

// downloadAudio downloads a voice file from Telegram and saves to temp file
func (h *Handler) downloadAudio(ctx context.Context, fileID string) (string, error) {
	// Get file path from Telegram
	file, err := h.client.GetFile(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("getFile failed: %w", err)
	}

	if file.FilePath == "" {
		return "", fmt.Errorf("file path not available")
	}

	// Download file data
	data, err := h.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	// Determine extension from file path (usually .oga for voice)
	ext := filepath.Ext(file.FilePath)
	if ext == "" {
		ext = ".oga" // Default to oga for voice messages
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "pilot-voice-*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = tmpFile.Close() }()

	// Write data
	if _, err := tmpFile.Write(data); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// voiceNotAvailableMessage returns an actionable error message for voice transcription
func (h *Handler) voiceNotAvailableMessage() string {
	var sb strings.Builder
	sb.WriteString("❌ Voice transcription not available\n\n")

	// Check what's missing based on the error
	if h.transcriptionErr != nil {
		errStr := h.transcriptionErr.Error()
		if strings.Contains(errStr, "no backend") || strings.Contains(errStr, "API key") {
			sb.WriteString("Missing: OpenAI API key\n\n")
			sb.WriteString("Set openai_api_key in ~/.pilot/config.yaml\n")
			sb.WriteString("Then restart bot.")
			return sb.String()
		}
	}

	// Generic guidance
	sb.WriteString("To enable voice:\n")
	sb.WriteString("1. Set openai_api_key in ~/.pilot/config.yaml\n")
	sb.WriteString("2. Restart bot\n\n")
	sb.WriteString("Run 'pilot doctor' to check setup.")
	return sb.String()
}

// downloadImage downloads an image from Telegram and saves to temp file
func (h *Handler) downloadImage(ctx context.Context, fileID string) (string, error) {
	// Get file path from Telegram
	file, err := h.client.GetFile(ctx, fileID)
	if err != nil {
		return "", fmt.Errorf("getFile failed: %w", err)
	}

	if file.FilePath == "" {
		return "", fmt.Errorf("file path not available")
	}

	// Download file data
	data, err := h.client.DownloadFile(ctx, file.FilePath)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	// Determine extension from file path
	ext := filepath.Ext(file.FilePath)
	if ext == "" {
		ext = ".jpg" // Default to jpg for photos
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "pilot-image-*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() { _ = tmpFile.Close() }()

	// Write data
	if _, err := tmpFile.Write(data); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}

	return tmpFile.Name(), nil
}

// executeImageTask executes a task with an image attachment
func (h *Handler) executeImageTask(ctx context.Context, chatID, imagePath, prompt string) {
	// Generate task ID
	taskID := fmt.Sprintf("IMG-%d", time.Now().Unix())

	// Send progress message
	resp, err := h.client.SendMessage(ctx, chatID, FormatProgressUpdate(taskID, "Analyzing", 10, "Processing image..."), "")
	var progressMsgID int64
	if err == nil && resp != nil && resp.Result != nil {
		progressMsgID = resp.Result.MessageID
	}

	// Create task with image
	task := &executor.Task{
		ID:          taskID,
		Title:       "Image analysis",
		Description: prompt,
		ProjectPath: h.projectPath,
		Verbose:     false,
		ImagePath:   imagePath,
		Branch:      fmt.Sprintf("pilot/%s", taskID),
		BaseBranch:  "main",
		CreatePR:    true,
	}

	// Set up progress callback
	var lastPhase string
	var lastProgress int
	var lastUpdate time.Time

	if progressMsgID != 0 {
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
			_ = h.client.EditMessage(ctx, chatID, progressMsgID, updateText, "")
		})
	}

	// Execute task (90 second timeout for image analysis)
	taskCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	logging.WithTask(taskID).Info("Executing image task", slog.String("chat_id", chatID))
	result, err := h.runner.Execute(taskCtx, task)

	// Clear progress callback
	h.runner.OnProgress(nil)

	if err != nil {
		if taskCtx.Err() == context.DeadlineExceeded {
			_, _ = h.client.SendMessage(ctx, chatID, "⏱ Image analysis timed out. Try a simpler request.", "")
		} else {
			_, _ = h.client.SendMessage(ctx, chatID, fmt.Sprintf("❌ Analysis failed: %s", err.Error()), "")
		}
		return
	}

	// Format and send result
	answer := result.Output
	if answer == "" {
		answer = "Could not analyze the image."
	}

	// Truncate if too long for Telegram
	if len(answer) > 4000 {
		answer = answer[:3997] + "..."
	}

	_, _ = h.client.SendMessage(ctx, chatID, answer, "")
}

// ============================================================================
// Task Resolution Helpers
// ============================================================================

// TaskInfo holds resolved task information from .agent/tasks/
type TaskInfo struct {
	ID       string // e.g., "07"
	FullID   string // e.g., "TASK-07"
	Title    string // e.g., "Telegram Voice Support"
	Status   string // e.g., "backlog", "complete"
	FilePath string // Full path to task file
}

// resolveTaskID looks up a task number and returns task info
// Input can be "07", "7", "TASK-07", "task 7", etc.
func (h *Handler) resolveTaskID(input string) *TaskInfo {
	// Normalize input - extract just the number
	input = strings.ToLower(strings.TrimSpace(input))
	input = strings.TrimPrefix(input, "task-")
	input = strings.TrimPrefix(input, "task ")
	input = strings.TrimPrefix(input, "#")

	// Try to parse as number
	num, err := strconv.Atoi(input)
	if err != nil {
		return nil
	}

	// Format as two-digit for file lookup
	taskNum := fmt.Sprintf("%02d", num)

	// Search for matching task file
	tasksDir := filepath.Join(h.projectPath, ".agent", "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		name := strings.ToUpper(entry.Name())
		// Match TASK-07-*.md or TASK-7-*.md
		if strings.HasPrefix(name, fmt.Sprintf("TASK-%s-", taskNum)) ||
			strings.HasPrefix(name, fmt.Sprintf("TASK-%d-", num)) {

			filePath := filepath.Join(tasksDir, entry.Name())
			status, title := parseTaskFile(filePath)

			return &TaskInfo{
				ID:       taskNum,
				FullID:   fmt.Sprintf("TASK-%s", taskNum),
				Title:    title,
				Status:   status,
				FilePath: filePath,
			}
		}
	}

	return nil
}

// loadTaskDescription reads the full task description from the file
func (h *Handler) loadTaskDescription(taskInfo *TaskInfo) string {
	if taskInfo == nil || taskInfo.FilePath == "" {
		return ""
	}

	data, err := os.ReadFile(taskInfo.FilePath)
	if err != nil {
		return ""
	}

	return string(data)
}

// resolveTaskFromDescription extracts task ID from descriptions like:
// "Start task 07", "task 7", "07", "run 25", "execute task-07"
func (h *Handler) resolveTaskFromDescription(description string) *TaskInfo {
	desc := strings.ToLower(strings.TrimSpace(description))

	// Patterns to extract task number
	patterns := []string{
		`(?i)(?:start|run|execute|do)\s+(?:task[- ]?)?(\d+)`,
		`(?i)task[- ]?(\d+)`,
		`^(\d+)$`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(desc); len(matches) > 1 {
			return h.resolveTaskID(matches[1])
		}
	}

	return nil
}

// ============================================================================
// Fast Path Handlers
// ============================================================================

// tryFastAnswer attempts to answer common questions without spawning Claude Code
// Returns empty string if question needs full Claude processing
func (h *Handler) tryFastAnswer(question string) string {
	q := strings.ToLower(question)

	switch {
	case containsAny(q, "issues", "tasks", "backlog", "todo list", "what to do"):
		return h.fastListTasks()
	case containsAny(q, "status", "progress", "current state"):
		return h.fastReadStatus()
	case containsAny(q, "todos", "fixmes", "todo", "fixme"):
		return h.fastGrepTodos()
	}

	return "" // Fall back to Claude
}

// containsAny returns true if s contains any of the substrings
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// fastListTasks lists tasks from .agent/tasks/ directory
func (h *Handler) fastListTasks() string {
	tasksDir := filepath.Join(h.projectPath, ".agent", "tasks")

	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return "" // Fall back to Claude
	}

	type taskInfo struct {
		num   string
		title string
	}
	var pending, inProgress, completed []taskInfo

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		filePath := filepath.Join(tasksDir, entry.Name())
		status, title := parseTaskFile(filePath)

		// Extract task number (e.g., "07" from "TASK-07-telegram-voice.md")
		taskNum := extractTaskNumber(entry.Name())
		if taskNum == "" {
			continue
		}

		info := taskInfo{num: taskNum, title: title}

		switch {
		case strings.Contains(status, "complete") || strings.Contains(status, "done") || strings.Contains(status, "✅"):
			completed = append(completed, info)
		case strings.Contains(status, "progress") || strings.Contains(status, "🚧") || strings.Contains(status, "wip"):
			inProgress = append(inProgress, info)
		default:
			pending = append(pending, info)
		}
	}

	if len(pending)+len(inProgress)+len(completed) == 0 {
		return "" // No tasks found, fall back to Claude
	}

	var sb strings.Builder

	// In Progress
	if len(inProgress) > 0 {
		sb.WriteString("In Progress\n")
		for _, t := range inProgress {
			sb.WriteString(fmt.Sprintf("%s: %s\n", t.num, t.title))
		}
		sb.WriteString("\n")
	}

	// Backlog - show first 5
	if len(pending) > 0 {
		sb.WriteString("Backlog\n")
		showCount := min(5, len(pending))
		for i := 0; i < showCount; i++ {
			sb.WriteString(fmt.Sprintf("%s: %s\n", pending[i].num, pending[i].title))
		}
		if len(pending) > 5 {
			sb.WriteString(fmt.Sprintf("_+%d more planned_\n", len(pending)-5))
		}
		sb.WriteString("\n")
	}

	// Recently done - show last 2
	if len(completed) > 0 {
		sb.WriteString("Recently done\n")
		showCount := min(2, len(completed))
		start := len(completed) - showCount
		for i := start; i < len(completed); i++ {
			sb.WriteString(fmt.Sprintf("%s: %s\n", completed[i].num, completed[i].title))
		}
		sb.WriteString("\n")
	}

	// Progress bar
	total := len(pending) + len(inProgress) + len(completed)
	doneCount := len(completed)
	percent := 0
	if total > 0 {
		percent = (doneCount * 100) / total
	}
	sb.WriteString(fmt.Sprintf("Progress: %s %d%%", makeProgressBar(percent), percent))

	return sb.String()
}

// extractTaskNumber gets "07" from "TASK-07-name.md"
func extractTaskNumber(filename string) string {
	// Remove .md
	name := strings.TrimSuffix(filename, ".md")

	// Handle TASK-XX format
	if strings.HasPrefix(strings.ToUpper(name), "TASK-") {
		rest := name[5:] // After "TASK-"
		// Find end of number
		numEnd := 0
		for i, c := range rest {
			if c >= '0' && c <= '9' {
				numEnd = i + 1
			} else {
				break
			}
		}
		if numEnd > 0 {
			return rest[:numEnd]
		}
	}
	return ""
}

// makeProgressBar creates a text progress bar
func makeProgressBar(percent int) string {
	filled := percent / 5 // 20 chars total
	empty := 20 - filled
	return strings.Repeat("█", filled) + strings.Repeat("░", empty)
}

// parseTaskFile reads a task file and extracts status and title
func parseTaskFile(path string) (status, title string) {
	file, err := os.Open(path)
	if err != nil {
		return "pending", ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	lineCount := 0

	for scanner.Scan() && lineCount < 15 {
		line := scanner.Text()
		lineCount++

		// Extract title from "# TASK-XX: Title" or first heading
		if strings.HasPrefix(line, "# ") && title == "" {
			title = strings.TrimPrefix(line, "# ")
			// Remove task ID prefix if present
			if idx := strings.Index(title, ":"); idx != -1 && idx < 20 {
				title = strings.TrimSpace(title[idx+1:])
			}
		}

		// Extract status from "**Status**: X" or "Status: X"
		lineLower := strings.ToLower(line)
		if strings.Contains(lineLower, "status") {
			if idx := strings.Index(line, ":"); idx != -1 {
				status = strings.ToLower(strings.TrimSpace(line[idx+1:]))
				// Clean up status markers
				status = strings.Trim(status, "*_` ")
				status = strings.ToLower(status)
			}
		}
	}

	if status == "" {
		status = "pending"
	}

	return status, truncateDescription(title, 50)
}

// fastReadStatus reads project status from DEVELOPMENT-README.md
func (h *Handler) fastReadStatus() string {
	readmePath := filepath.Join(h.projectPath, ".agent", "DEVELOPMENT-README.md")

	data, err := os.ReadFile(readmePath)
	if err != nil {
		return "" // Fall back to Claude
	}

	content := string(data)

	// Extract key sections
	var sb strings.Builder
	sb.WriteString("📊 *Project Status*\n\n")

	// Find "Current State" or "Implementation Status" section
	lines := strings.Split(content, "\n")
	inSection := false
	lineCount := 0

	for _, line := range lines {
		lineLower := strings.ToLower(line)

		// Start capturing at relevant sections
		if strings.Contains(lineLower, "current state") ||
			strings.Contains(lineLower, "implementation status") ||
			strings.Contains(lineLower, "active tasks") {
			inSection = true
			sb.WriteString("*" + strings.TrimPrefix(line, "## ") + "*\n")
			continue
		}

		// Stop at next major section
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}

		if inSection {
			// Convert table rows to list items
			if strings.HasPrefix(strings.TrimSpace(line), "|") {
				cells := strings.Split(line, "|")
				if len(cells) >= 3 {
					cell1 := strings.TrimSpace(cells[1])
					cell2 := strings.TrimSpace(cells[2])
					if cell1 != "" && !strings.Contains(cell1, "---") {
						sb.WriteString(fmt.Sprintf("• %s: %s\n", cell1, cell2))
						lineCount++
					}
				}
			} else if strings.TrimSpace(line) != "" {
				sb.WriteString(line + "\n")
				lineCount++
			}

			if lineCount > 20 {
				sb.WriteString("\n_(truncated)_")
				break
			}
		}
	}

	if lineCount == 0 {
		return "" // Nothing found, fall back to Claude
	}

	return sb.String()
}

// fastGrepTodos searches for TODO/FIXME comments in the codebase
func (h *Handler) fastGrepTodos() string {
	var todos []string

	// Walk common source directories
	dirs := []string{"cmd", "internal", "pkg", "src", "orchestrator"}

	for _, dir := range dirs {
		dirPath := filepath.Join(h.projectPath, dir)
		if _, err := os.Stat(dirPath); os.IsNotExist(err) {
			continue
		}

		_ = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}

			// Only scan Go and Python files
			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".py" {
				return nil
			}

			file, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer func() { _ = file.Close() }()

			scanner := bufio.NewScanner(file)
			lineNum := 0

			for scanner.Scan() {
				lineNum++
				line := scanner.Text()
				lineLower := strings.ToLower(line)

				if strings.Contains(lineLower, "todo") || strings.Contains(lineLower, "fixme") {
					relPath, _ := filepath.Rel(h.projectPath, path)
					// Clean up the line
					comment := strings.TrimSpace(line)
					comment = strings.TrimPrefix(comment, "//")
					comment = strings.TrimPrefix(comment, "#")
					comment = strings.TrimSpace(comment)

					todos = append(todos, fmt.Sprintf("• %s:%d %s", relPath, lineNum, truncateDescription(comment, 60)))

					if len(todos) >= 15 {
						return filepath.SkipAll
					}
				}
			}
			return nil
		})

		if len(todos) >= 15 {
			break
		}
	}

	if len(todos) == 0 {
		return "✨ No TODOs or FIXMEs found in the codebase!"
	}

	// Sort by path for readability
	sort.Strings(todos)

	var sb strings.Builder
	sb.WriteString("📝 TODOs & FIXMEs\n\n")
	for _, todo := range todos {
		sb.WriteString(todo + "\n")
	}

	if len(todos) >= 15 {
		sb.WriteString("\n_(showing first 15)_")
	}

	return sb.String()
}

// sendVoiceSetupPrompt sends an interactive voice setup message with install options
func (h *Handler) sendVoiceSetupPrompt(ctx context.Context, chatID string) {
	status := transcription.CheckSetup(nil)

	var sb strings.Builder

	if status.OpenAIKeySet {
		sb.WriteString("✅ Voice transcription is ready!\n")
		sb.WriteString("Backend: Whisper API")
		_, _ = h.client.SendMessage(ctx, chatID, sb.String(), "")
		return
	}

	sb.WriteString("🎤 Voice transcription not available\n\n")
	sb.WriteString("Missing: OpenAI API key for Whisper\n")
	sb.WriteString("Set openai_api_key in ~/.pilot/config.yaml\n")

	var buttons [][]InlineKeyboardButton
	buttons = append(buttons, []InlineKeyboardButton{
		{Text: "🔍 Check Status", CallbackData: "voice_check_status"},
	})

	_, _ = h.client.SendMessageWithKeyboard(ctx, chatID, sb.String(), "", buttons)
}
