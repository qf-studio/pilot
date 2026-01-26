package telegram

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
)

// PendingTask represents a task awaiting confirmation
type PendingTask struct {
	TaskID      string
	Description string
	ChatID      string
	MessageID   int64
	CreatedAt   time.Time
}

// Handler processes incoming Telegram messages and executes tasks
type Handler struct {
	client       *Client
	runner       *executor.Runner
	projectPath  string
	allowedIDs   map[int64]bool      // Allowed user/chat IDs for security
	offset       int64               // Last processed update ID
	pendingTasks map[string]*PendingTask // ChatID -> pending task
	mu           sync.Mutex
	stopCh       chan struct{}
	wg           sync.WaitGroup
}

// HandlerConfig holds configuration for the Telegram handler
type HandlerConfig struct {
	BotToken    string
	ProjectPath string
	AllowedIDs  []int64 // User/chat IDs allowed to send tasks
}

// NewHandler creates a new Telegram message handler
func NewHandler(config *HandlerConfig, runner *executor.Runner) *Handler {
	allowedIDs := make(map[int64]bool)
	for _, id := range config.AllowedIDs {
		allowedIDs[id] = true
	}

	return &Handler{
		client:       NewClient(config.BotToken),
		runner:       runner,
		projectPath:  config.ProjectPath,
		allowedIDs:   allowedIDs,
		pendingTasks: make(map[string]*PendingTask),
		stopCh:       make(chan struct{}),
	}
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

	log.Println("[telegram] Starting poll loop...")

	for {
		select {
		case <-ctx.Done():
			log.Println("[telegram] Context cancelled, stopping poll loop")
			return
		case <-h.stopCh:
			log.Println("[telegram] Stop signal received, stopping poll loop")
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
				fmt.Sprintf("⏰ Task `%s` expired (no confirmation received).", task.TaskID), "Markdown")
			delete(h.pendingTasks, chatID)
			log.Printf("[telegram] Expired pending task %s for chat %s", task.TaskID, chatID)
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
			log.Printf("[telegram] Error fetching updates: %v", err)
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

	if update.Message == nil || update.Message.Text == "" {
		return
	}

	msg := update.Message
	chatID := strconv.FormatInt(msg.Chat.ID, 10)

	// Security check: only process messages from allowed users/chats
	if len(h.allowedIDs) > 0 {
		senderID := int64(0)
		if msg.From != nil {
			senderID = msg.From.ID
		}

		if !h.allowedIDs[msg.Chat.ID] && !h.allowedIDs[senderID] {
			log.Printf("[telegram] Ignoring message from unauthorized chat/user: %d/%d", msg.Chat.ID, senderID)
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

	// Detect intent
	intent := DetectIntent(text)
	log.Printf("[telegram] Message from %s: %q -> Intent: %s", chatID, truncateDescription(text, 50), intent)

	switch intent {
	case IntentCommand:
		h.handleCommand(ctx, chatID, text)
	case IntentGreeting:
		h.handleGreeting(ctx, chatID, msg.From)
	case IntentQuestion:
		h.handleQuestion(ctx, chatID, text)
	case IntentTask:
		h.handleTask(ctx, chatID, text)
	default:
		// Fallback to task
		h.handleTask(ctx, chatID, text)
	}
}

// handleCallback processes callback queries from inline keyboards
func (h *Handler) handleCallback(ctx context.Context, callback *CallbackQuery) {
	if callback.Message == nil {
		return
	}

	chatID := strconv.FormatInt(callback.Message.Chat.ID, 10)
	data := callback.Data

	// Answer callback to remove loading state
	_ = h.client.AnswerCallback(ctx, callback.ID, "")

	switch data {
	case "execute":
		h.handleConfirmation(ctx, chatID, true)
	case "cancel":
		h.handleConfirmation(ctx, chatID, false)
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
			fmt.Sprintf("❌ Task `%s` cancelled.", pending.TaskID), "Markdown")
	}
}

// handleGreeting responds to greetings
func (h *Handler) handleGreeting(ctx context.Context, chatID string, from *User) {
	username := ""
	if from != nil {
		username = from.FirstName
	}
	_, _ = h.client.SendMessage(ctx, chatID, FormatGreeting(username), "Markdown")
}

// handleQuestion handles questions about the codebase
func (h *Handler) handleQuestion(ctx context.Context, chatID, question string) {
	// Send acknowledgment
	_, _ = h.client.SendMessage(ctx, chatID, FormatQuestionAck(), "Markdown")

	// Create a read-only prompt for Claude
	prompt := fmt.Sprintf(`Answer this question about the codebase. DO NOT make any changes, only read and analyze.

Question: %s

Provide a concise answer. If you need to show file paths or code, use markdown formatting.`, question)

	// Create a read-only task (no branch, no PR)
	taskID := fmt.Sprintf("Q-%d", time.Now().Unix())
	task := &executor.Task{
		ID:          taskID,
		Title:       "Question: " + truncateDescription(question, 40),
		Description: prompt,
		ProjectPath: h.projectPath,
		Verbose:     false,
	}

	// Execute
	log.Printf("[telegram] Answering question %s: %s", taskID, truncateDescription(question, 50))
	result, err := h.runner.Execute(ctx, task)

	if err != nil {
		_, _ = h.client.SendMessage(ctx, chatID,
			"❌ Sorry, I couldn't answer that question. Try rephrasing it.", "")
		return
	}

	// Format and send answer
	answer := FormatQuestionAnswer(result.Output)
	if answer == "" {
		answer = "I couldn't find a clear answer to that question."
	}

	_, _ = h.client.SendMessage(ctx, chatID, answer, "Markdown")
}

// handleTask handles task requests with confirmation
func (h *Handler) handleTask(ctx context.Context, chatID, description string) {
	// Check if there's already a pending task
	h.mu.Lock()
	if existing, exists := h.pendingTasks[chatID]; exists {
		h.mu.Unlock()
		_, _ = h.client.SendMessage(ctx, chatID,
			fmt.Sprintf("⚠️ You already have a pending task: `%s`\n\nReply *yes* to execute or *no* to cancel.",
				existing.TaskID), "Markdown")
		return
	}

	// Generate task ID
	taskID := fmt.Sprintf("TG-%d", time.Now().Unix())

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
	confirmMsg := FormatTaskConfirmation(taskID, description, h.projectPath)

	msgResp, err := h.client.SendMessageWithKeyboard(ctx, chatID, confirmMsg, "Markdown",
		[][]InlineKeyboardButton{
			{
				{Text: "✅ Execute", CallbackData: "execute"},
				{Text: "❌ Cancel", CallbackData: "cancel"},
			},
		})

	if err != nil {
		log.Printf("[telegram] Failed to send confirmation: %v", err)
		// Fallback to text-based confirmation
		_, _ = h.client.SendMessage(ctx, chatID,
			confirmMsg+"\n\n_Reply *yes* to execute or *no* to cancel._", "Markdown")
	} else if msgResp != nil && msgResp.Result != nil {
		h.mu.Lock()
		if p, ok := h.pendingTasks[chatID]; ok {
			p.MessageID = msgResp.Result.MessageID
		}
		h.mu.Unlock()
	}
}

// executeTask executes a confirmed task
func (h *Handler) executeTask(ctx context.Context, chatID, taskID, description string) {
	// Send execution started message (this will be updated with progress)
	resp, err := h.client.SendMessage(ctx, chatID, FormatProgressUpdate(taskID, "Starting", 0, "Initializing..."), "Markdown")
	if err != nil {
		log.Printf("[telegram] Failed to send start message: %v", err)
		// Fallback to simple message
		_, _ = h.client.SendMessage(ctx, chatID, FormatTaskStarted(taskID, description), "Markdown")
	}

	// Track progress message ID for updates
	var progressMsgID int64
	if resp != nil && resp.Result != nil {
		progressMsgID = resp.Result.MessageID
	}

	// Create task for executor
	task := &executor.Task{
		ID:          taskID,
		Title:       truncateDescription(description, 50),
		Description: description,
		ProjectPath: h.projectPath,
		Verbose:     false,
	}

	// Set up progress callback with throttling
	var lastPhase string
	var lastProgress int
	var lastUpdate time.Time

	if progressMsgID != 0 {
		h.runner.OnProgress(func(tid string, phase string, progress int, message string) {
			// Only update for our task
			if tid != taskID {
				return
			}

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
			if err := h.client.EditMessage(ctx, chatID, progressMsgID, updateText, "Markdown"); err != nil {
				log.Printf("[telegram] Failed to edit progress message: %v", err)
			}
		})
	}

	// Execute task
	log.Printf("[telegram] Executing task %s: %s", taskID, truncateDescription(description, 100))
	result, err := h.runner.Execute(ctx, task)

	// Clear progress callback
	h.runner.OnProgress(nil)

	// Send result with clean formatting
	if err != nil {
		errMsg := fmt.Sprintf("❌ *Task failed*\n`%s`\n\n```\n%s\n```", taskID, err.Error())
		_, _ = h.client.SendMessage(ctx, chatID, errMsg, "Markdown")
		return
	}

	result.TaskID = taskID // Ensure TaskID is set
	_, _ = h.client.SendMessage(ctx, chatID, FormatTaskResult(result), "Markdown")
}

// handleCommand processes bot commands
func (h *Handler) handleCommand(ctx context.Context, chatID, text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/start", "/help":
		helpText := `🤖 *Pilot Bot*

I can help you with your codebase!

*What I understand:*
• *Tasks:* "Create a file...", "Add a function...", "Fix the bug..."
• *Questions:* "What files handle auth?", "How does X work?"
• *Greetings:* "Hi", "Hello" - I'll greet you back

*Commands:*
/help - Show this message
/status - Check bot status
/cancel - Cancel pending task

*Task flow:*
1. You describe a task
2. I ask for confirmation
3. You confirm or cancel
4. I execute and report results`

		_, _ = h.client.SendMessage(ctx, chatID, helpText, "Markdown")

	case "/status":
		h.mu.Lock()
		pending := h.pendingTasks[chatID]
		h.mu.Unlock()

		statusText := fmt.Sprintf("✅ Pilot bot is running\n📁 Project: `%s`", h.projectPath)
		if pending != nil {
			statusText += fmt.Sprintf("\n\n⏳ Pending task: `%s`", pending.TaskID)
		}
		_, _ = h.client.SendMessage(ctx, chatID, statusText, "Markdown")

	case "/cancel":
		h.mu.Lock()
		if pending, exists := h.pendingTasks[chatID]; exists {
			delete(h.pendingTasks, chatID)
			h.mu.Unlock()
			_, _ = h.client.SendMessage(ctx, chatID,
				fmt.Sprintf("❌ Task `%s` cancelled.", pending.TaskID), "Markdown")
		} else {
			h.mu.Unlock()
			_, _ = h.client.SendMessage(ctx, chatID, "No pending task to cancel.", "")
		}

	default:
		_, _ = h.client.SendMessage(ctx, chatID, "Unknown command. Use /help for available commands.", "")
	}
}
