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

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
	"github.com/qf-studio/pilot/internal/transcription"
)

const startupAPITimeout = 10 * time.Second

// MemberResolver resolves a Telegram user to a team member ID for RBAC (GH-634).
// Decoupled from teams package to avoid import cycles.
type MemberResolver interface {
	// ResolveTelegramIdentity maps a Telegram user ID and/or email to a member ID.
	// Returns ("", nil) when no match is found (= skip RBAC).
	ResolveTelegramIdentity(telegramID int64, email string) (string, error)
}

// MemberResolverAdapter wraps a telegram.MemberResolver as comms.MemberResolver.
type MemberResolverAdapter struct {
	Inner MemberResolver
}

// ResolveIdentity maps a sender ID string to a team member ID via the inner Telegram resolver.
func (a *MemberResolverAdapter) ResolveIdentity(senderID string) (string, error) {
	id, err := strconv.ParseInt(senderID, 10, 64)
	if err != nil {
		return "", nil // not a valid Telegram ID
	}
	return a.Inner.ResolveTelegramIdentity(id, "")
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
	client           *Client
	runner           *executor.Runner
	projects         comms.ProjectSource // Project source for multi-project support
	projectPath      string              // Default/fallback project path
	allowedIDs       map[int64]bool      // Allowed user/chat IDs for security
	offset           int64               // Last processed update ID
	mu               sync.Mutex
	stopCh           chan struct{}
	wg               sync.WaitGroup
	transcriber      *transcription.Service // Voice transcription service (optional)
	transcriptionErr error                  // Error from transcription init (for guidance)
	store            *memory.Store          // Memory store for history/queue/budget (optional)
	cmdHandler       *CommandHandler        // Command handler for /commands
	plainTextMode    bool                   // Use plain text instead of Markdown
	botUsername      string                 // Bot username for mention stripping (GH-2129)
	commsHandler     *comms.Handler         // Shared message handler (GH-2143)
}

// HandlerConfig holds configuration for the Telegram handler
type HandlerConfig struct {
	BotToken       string
	ProjectPath    string                 // Default/fallback project path
	Projects       comms.ProjectSource    // Project source for multi-project support
	AllowedIDs     []int64                // User/chat IDs allowed to send tasks
	Transcription  *transcription.Config  // Voice transcription config (optional)
	Store          *memory.Store          // Memory store for history/queue/budget (optional)
	PlainTextMode  bool                   // Use plain text instead of Markdown (default: true)
	RateLimit      *comms.RateLimitConfig // Rate limiting config (optional)
	LLMClassifier  *LLMClassifierConfig   // LLM intent classification config (optional)
	MemberResolver MemberResolver         // Team member resolver for RBAC (optional, GH-634)
	CommsHandler   *comms.Handler         // Shared message handler (optional, GH-2143)
	Client         *Client                // Optional reuse of existing client
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

	// Use provided client or create a new one
	client := config.Client
	if client == nil {
		client = NewClient(config.BotToken)
	}

	h := &Handler{
		client:        client,
		runner:        runner,
		projects:      config.Projects,
		projectPath:   projectPath,
		allowedIDs:    allowedIDs,
		stopCh:        make(chan struct{}),
		store:         config.Store,
		plainTextMode: config.PlainTextMode,
		commsHandler:  config.CommsHandler,
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

	return h
}

// getActiveProjectPath returns the active project path for a chat
func (h *Handler) getActiveProjectPath(chatID string) string {
	if h.commsHandler != nil {
		_, path := h.commsHandler.GetActiveProject(chatID)
		if path != "" {
			return path
		}
	}
	return h.projectPath
}

// setActiveProject sets the active project for a chat by name
func (h *Handler) setActiveProject(chatID, projectName string) (*comms.ProjectInfo, error) {
	if h.commsHandler != nil {
		if err := h.commsHandler.SetActiveProject(chatID, projectName); err != nil {
			return nil, err
		}
	}
	if h.projects == nil {
		return nil, fmt.Errorf("no projects configured")
	}
	proj := h.projects.GetProjectByName(projectName)
	if proj == nil {
		return nil, fmt.Errorf("project '%s' not found", projectName)
	}
	return proj, nil
}

// getActiveProjectInfo returns the active project info for a chat
func (h *Handler) getActiveProjectInfo(chatID string) *comms.ProjectInfo {
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
	startupCtx, cancel := context.WithTimeout(ctx, startupAPITimeout)
	defer cancel()
	return h.client.CheckSingleton(startupCtx)
}

// StartPolling starts polling for updates in a goroutine
func (h *Handler) StartPolling(ctx context.Context) {
	// Fetch bot username for mention stripping (GH-2129)
	startupCtx, cancel := context.WithTimeout(ctx, startupAPITimeout)
	defer cancel()
	if me, err := h.client.GetMe(startupCtx); err != nil {
		logging.WithComponent("telegram").Warn("Failed to fetch bot username via getMe", slog.String("error", err.Error()))
	} else if me != nil {
		h.botUsername = me.Username
		logging.WithComponent("telegram").Debug("Bot username resolved", slog.String("username", me.Username))
	}

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

// cleanupLoop delegates cleanup to the shared commsHandler.
func (h *Handler) cleanupLoop(ctx context.Context) {
	defer h.wg.Done()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
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
	text = stripBotMention(text, h.botUsername)

	// Commands stay local
	if strings.HasPrefix(text, "/") {
		h.handleCommand(ctx, chatID, text)
		return
	}

	// Delegate all other text to shared comms.Handler
	if h.commsHandler != nil {
		senderID := ""
		senderName := ""
		if msg.From != nil {
			senderID = strconv.FormatInt(msg.From.ID, 10)
			if msg.From.FirstName != "" {
				senderName = msg.From.FirstName
			}
		}
		h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
			ContextID:  chatID,
			SenderID:   senderID,
			SenderName: senderName,
			Text:       text,
			Platform:   "telegram",
			Timestamp:  time.Now(),
		})
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

	switch {
	case data == "execute":
		if h.commsHandler != nil {
			senderID := ""
			if callback.From != nil {
				senderID = strconv.FormatInt(callback.From.ID, 10)
			}
			h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
				ContextID:  chatID,
				SenderID:   senderID,
				Platform:   "telegram",
				IsCallback: true,
				CallbackID: callback.ID,
				ActionID:   "execute",
			})
		}
	case data == "cancel":
		if h.commsHandler != nil {
			senderID := ""
			if callback.From != nil {
				senderID = strconv.FormatInt(callback.From.ID, 10)
			}
			h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
				ContextID:  chatID,
				SenderID:   senderID,
				Platform:   "telegram",
				IsCallback: true,
				CallbackID: callback.ID,
				ActionID:   "cancel",
			})
		}
	case strings.HasPrefix(data, "switch_"):
		projectName := strings.TrimPrefix(data, "switch_")
		h.cmdHandler.HandleCallbackSwitch(ctx, chatID, projectName)
	case data == "voice_check_status":
		h.sendVoiceSetupPrompt(ctx, chatID)
	}
}

// handleCommand processes bot commands
func (h *Handler) handleCommand(ctx context.Context, chatID, text string) {
	// Delegate to command handler
	h.cmdHandler.HandleCommand(ctx, chatID, text)
}

// handleRunCommand executes a task directly without confirmation
func (h *Handler) handleRunCommand(ctx context.Context, chatID, taskIDInput string) {
	// Check if already running a task
	if h.commsHandler != nil {
		if running := h.commsHandler.GetRunningTask(chatID); running != nil {
			elapsed := time.Since(running.StartedAt).Round(time.Second)
			_, _ = h.client.SendMessage(ctx, chatID,
				fmt.Sprintf("⚠️ Already running %s (%s)\n\nUse /stop to cancel it first.", running.TaskID, elapsed), "")
			return
		}
	}

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

	// Execute directly via commsHandler
	if h.commsHandler != nil {
		h.commsHandler.ExecuteDirectTask(ctx, chatID, "", taskInfo.FullID, fmt.Sprintf("## Task: %s\n\n%s", taskInfo.FullID, description), nil)
	}
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

	// Execute with image via commsHandler
	taskID := fmt.Sprintf("IMG-%d", time.Now().Unix())
	if h.commsHandler != nil {
		h.commsHandler.ExecuteDirectTask(ctx, chatID, "", taskID, prompt, &comms.DirectTaskOpts{
			ImagePath: imagePath,
		})
	}
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

	// Delegate transcribed text to commsHandler
	text := strings.TrimSpace(result.Text)
	logging.WithComponent("telegram").Debug("Processing transcribed text", slog.String("chat_id", chatID))

	if h.commsHandler != nil {
		senderID := ""
		if msg.From != nil {
			senderID = strconv.FormatInt(msg.From.ID, 10)
		}
		h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
			ContextID: chatID,
			SenderID:  senderID,
			Text:      text,
			VoiceText: text,
			Platform:  "telegram",
			Timestamp: time.Now(),
		})
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

// stripBotMention removes a leading @username mention from message text (GH-2129).
func stripBotMention(text, botUsername string) string {
	if botUsername == "" {
		return text
	}
	prefix := "@" + botUsername
	if len(text) >= len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
		text = strings.TrimSpace(text[len(prefix):])
	}
	return text
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
