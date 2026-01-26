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

// Handler processes incoming Telegram messages and executes tasks
type Handler struct {
	client      *Client
	runner      *executor.Runner
	projectPath string
	allowedIDs  map[int64]bool // Allowed user/chat IDs for security
	offset      int64          // Last processed update ID
	mu          sync.Mutex
	stopCh      chan struct{}
	wg          sync.WaitGroup
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
		client:      NewClient(config.BotToken),
		runner:      runner,
		projectPath: config.ProjectPath,
		allowedIDs:  allowedIDs,
		stopCh:      make(chan struct{}),
	}
}

// StartPolling starts polling for updates in a goroutine
func (h *Handler) StartPolling(ctx context.Context) {
	h.wg.Add(1)
	go h.pollLoop(ctx)
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

	// Handle commands
	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, chatID, text)
		return
	}

	// Treat message as task description
	h.executeTask(ctx, chatID, text)
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

Send me a task description and I'll execute it using Claude Code.

*Commands:*
/help - Show this message
/status - Check if bot is running

*Example tasks:*
• Create a hello.py file that prints Hello World
• Add a function to parse JSON in utils.go
• Fix the typo in README.md`

		_, _ = h.client.SendMessage(ctx, chatID, helpText, "Markdown")

	case "/status":
		statusText := fmt.Sprintf("✅ Pilot bot is running\n📁 Project: `%s`", h.projectPath)
		_, _ = h.client.SendMessage(ctx, chatID, statusText, "Markdown")

	default:
		_, _ = h.client.SendMessage(ctx, chatID, "Unknown command. Use /help for available commands.", "")
	}
}

// executeTask executes a task and sends the result
func (h *Handler) executeTask(ctx context.Context, chatID, description string) {
	// Generate task ID
	taskID := fmt.Sprintf("TG-%d", time.Now().Unix())

	// Acknowledge receipt
	ackMsg := fmt.Sprintf("🚀 *Task received*\n`%s`\n\n%s", taskID, truncateDescription(description, 200))
	_, err := h.client.SendMessage(ctx, chatID, ackMsg, "Markdown")
	if err != nil {
		log.Printf("[telegram] Failed to send ack: %v", err)
	}

	// Create task for executor
	task := &executor.Task{
		ID:          taskID,
		Title:       truncateDescription(description, 50),
		Description: description,
		ProjectPath: h.projectPath,
		Verbose:     false,
	}

	// Execute task
	log.Printf("[telegram] Executing task %s: %s", taskID, truncateDescription(description, 100))
	result, err := h.runner.Execute(ctx, task)

	// Send result
	if err != nil {
		errMsg := fmt.Sprintf("❌ *Task failed*\n`%s`\n\n```\n%s\n```", taskID, err.Error())
		_, _ = h.client.SendMessage(ctx, chatID, errMsg, "Markdown")
		return
	}

	if result.Success {
		successMsg := fmt.Sprintf("✅ *Task completed*\n`%s`\n\n⏱ Duration: %s", taskID, result.Duration.Round(time.Second))
		if result.PRUrl != "" {
			successMsg += fmt.Sprintf("\n\n[View PR](%s)", result.PRUrl)
		}
		if result.Output != "" && len(result.Output) < 500 {
			successMsg += fmt.Sprintf("\n\n```\n%s\n```", result.Output)
		}
		_, _ = h.client.SendMessage(ctx, chatID, successMsg, "Markdown")
	} else {
		failMsg := fmt.Sprintf("❌ *Task failed*\n`%s`\n\n```\n%s\n```", taskID, truncateDescription(result.Error, 500))
		_, _ = h.client.SendMessage(ctx, chatID, failMsg, "Markdown")
	}
}

// truncateDescription truncates a string to maxLen
func truncateDescription(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
