// Package comms provides shared communication handler logic for adapter implementations.
package comms

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/executor"
	"github.com/alekspetrov/pilot/internal/intent"
	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/alekspetrov/pilot/internal/memory"
)

// MemberResolver resolves a platform user to a team member ID for RBAC.
// Each adapter provides a concrete implementation.
type MemberResolver interface {
	// ResolveIdentity maps a sender ID to a team member ID.
	// Returns ("", nil) when no match is found (= skip RBAC).
	ResolveIdentity(senderID string) (string, error)
}

// HandlerConfig holds configuration for creating a shared Handler.
type HandlerConfig struct {
	Messenger      Messenger
	Runner         *executor.Runner
	Projects       ProjectSource
	ProjectPath    string
	RateLimit      *RateLimitConfig
	LLMClassifier  intent.Classifier
	ConvStore      *intent.ConversationStore
	MemberResolver MemberResolver
	Store          *memory.Store
	// TaskIDPrefix is the adapter-specific prefix for task IDs (e.g., "TG", "SLACK").
	TaskIDPrefix string
	Log          *slog.Logger
}

// Handler implements platform-agnostic message handling with intent dispatch,
// rate limiting, task lifecycle, and progress tracking.
type Handler struct {
	messenger      Messenger
	runner         *executor.Runner
	projects       ProjectSource
	projectPath    string
	rateLimit      *RateLimiter
	llmClassifier  intent.Classifier
	convStore      *intent.ConversationStore
	memberResolver MemberResolver
	store          *memory.Store
	taskIDPrefix   string
	log            *slog.Logger

	activeProject map[string]string       // contextID -> projectPath
	pendingTasks  map[string]*PendingTask // contextID -> pending task
	runningTasks  map[string]*RunningTask // contextID -> running task
	lastSender    map[string]string       // contextID -> senderID
	mu            sync.Mutex
}

// NewHandler creates a shared Handler from the given config.
func NewHandler(cfg *HandlerConfig) *Handler {
	var rl *RateLimiter
	if cfg.RateLimit != nil {
		rl = NewRateLimiter(cfg.RateLimit)
	} else {
		rl = NewRateLimiter(DefaultRateLimitConfig())
	}

	prefix := cfg.TaskIDPrefix
	if prefix == "" {
		prefix = "MSG"
	}

	lg := cfg.Log
	if lg == nil {
		lg = logging.WithComponent("comms.handler")
	}

	return &Handler{
		messenger:      cfg.Messenger,
		runner:         cfg.Runner,
		projects:       cfg.Projects,
		projectPath:    cfg.ProjectPath,
		rateLimit:      rl,
		llmClassifier:  cfg.LLMClassifier,
		convStore:      cfg.ConvStore,
		memberResolver: cfg.MemberResolver,
		store:          cfg.Store,
		taskIDPrefix:   prefix,
		log:            lg,
		activeProject:  make(map[string]string),
		pendingTasks:   make(map[string]*PendingTask),
		runningTasks:   make(map[string]*RunningTask),
		lastSender:     make(map[string]string),
	}
}

// HandleMessage is the main entry point for processing an incoming message.
// It performs rate limiting, intent detection, and dispatches to the appropriate handler.
func (h *Handler) HandleMessage(ctx context.Context, msg *IncomingMessage) {
	contextID := msg.ContextID
	text := msg.Text

	// Track sender for RBAC
	if msg.SenderID != "" {
		h.mu.Lock()
		h.lastSender[contextID] = msg.SenderID
		h.mu.Unlock()
	}

	// Rate limit check
	if !h.rateLimit.AllowMessage(contextID) {
		h.log.Warn("Message rate limit exceeded", slog.String("context_id", contextID))
		_ = h.messenger.SendText(ctx, contextID, "⚠️ Rate limit exceeded. Please wait before sending more messages.")
		return
	}

	// Handle callback (button press) — check pending confirmation
	if msg.IsCallback {
		_ = h.messenger.AcknowledgeCallback(ctx, msg.CallbackID)
		confirmed := msg.ActionID == "execute" || msg.ActionID == "confirm" || msg.ActionID == "yes"
		h.handleConfirmation(ctx, contextID, msg.ThreadID, confirmed)
		return
	}

	// Text-based confirmation shortcuts
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "yes" || lower == "y" || lower == "confirm" || lower == "ok" {
		h.handleConfirmation(ctx, contextID, msg.ThreadID, true)
		return
	}
	if lower == "no" || lower == "n" || lower == "cancel" || lower == "nope" {
		h.handleConfirmation(ctx, contextID, msg.ThreadID, false)
		return
	}

	// Detect intent
	detected := h.detectIntent(ctx, contextID, text)

	// Record user message in conversation history
	if h.convStore != nil {
		h.convStore.Add(contextID, "user", TruncateText(text, 500))
	}

	// Dispatch
	switch detected {
	case intent.IntentGreeting:
		h.handleGreeting(ctx, contextID)
	case intent.IntentQuestion:
		h.handleQuestion(ctx, contextID, msg.ThreadID, text)
	case intent.IntentResearch:
		h.handleResearch(ctx, contextID, msg.ThreadID, text)
	case intent.IntentPlanning:
		h.handlePlanning(ctx, contextID, msg.ThreadID, text)
	case intent.IntentChat:
		h.handleChat(ctx, contextID, msg.ThreadID, text)
	case intent.IntentTask:
		h.handleTask(ctx, contextID, msg.ThreadID, text, msg.SenderID)
	default:
		// Fallback: treat as task
		h.handleTask(ctx, contextID, msg.ThreadID, text, msg.SenderID)
	}
}

// ---------- intent detection ----------

func (h *Handler) detectIntent(ctx context.Context, contextID, text string) intent.Intent {
	// Fast path: commands
	if strings.HasPrefix(text, "/") {
		return intent.IntentCommand
	}

	// Fast path: clear questions
	if intent.IsClearQuestion(text) {
		return intent.IntentQuestion
	}

	// LLM classification if available
	if h.llmClassifier != nil {
		classifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		var history []intent.ConversationMessage
		if h.convStore != nil {
			history = h.convStore.Get(contextID)
		}

		detected, err := h.llmClassifier.Classify(classifyCtx, history, text)
		if err != nil {
			h.log.Debug("LLM classification failed, using regex", slog.Any("error", err))
			return intent.DetectIntent(text)
		}

		h.log.Debug("LLM classified intent",
			slog.String("context_id", contextID),
			slog.String("intent", string(detected)),
			slog.String("text", TruncateText(text, 50)))
		return detected
	}

	return intent.DetectIntent(text)
}

// ---------- intent handlers ----------

func (h *Handler) handleGreeting(ctx context.Context, contextID string) {
	_ = h.messenger.SendText(ctx, contextID, "👋 Hello! I'm Pilot — send me a task, question, or say /help.")
}

func (h *Handler) handleQuestion(ctx context.Context, contextID, threadID, question string) {
	_ = h.messenger.SendText(ctx, contextID, "🔍 Looking into that...")

	questionCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	prompt := fmt.Sprintf(`Answer this question about the codebase. DO NOT make any changes, only read and analyze.

Question: %s

IMPORTANT: Be concise. Limit your exploration to 5-10 files max. Provide a brief, direct answer.
If the question is too broad, ask for clarification instead of exploring everything.`, question)

	taskID := fmt.Sprintf("Q-%d", time.Now().Unix())
	task := &executor.Task{
		ID:          taskID,
		Title:       "Question: " + TruncateText(question, 40),
		Description: prompt,
		ProjectPath: h.getActiveProjectPath(contextID),
		Verbose:     false,
	}

	h.log.Debug("Answering question", slog.String("task_id", taskID), slog.String("context_id", contextID))
	result, err := h.runner.Execute(questionCtx, task)

	if err != nil {
		if questionCtx.Err() == context.DeadlineExceeded {
			_ = h.messenger.SendText(ctx, contextID, "⏱ Question timed out. Try asking something more specific.")
		} else {
			_ = h.messenger.SendText(ctx, contextID, "❌ Sorry, I couldn't answer that question. Try rephrasing it.")
		}
		return
	}

	answer := CleanInternalSignals(result.Output)
	if answer == "" {
		answer = "I couldn't find a clear answer to that question."
	}

	_ = h.messenger.SendChunked(ctx, contextID, threadID, answer, "")
}

func (h *Handler) handleResearch(ctx context.Context, contextID, threadID, query string) {
	_ = h.messenger.SendText(ctx, contextID, "🔬 Researching...")

	taskID := fmt.Sprintf("RES-%d", time.Now().Unix())
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
		ProjectPath: h.getActiveProjectPath(contextID),
		CreatePR:    false,
	}

	researchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	h.log.Info("Executing research", slog.String("task_id", taskID), slog.String("context_id", contextID))
	result, err := h.runner.Execute(researchCtx, task)

	if err != nil {
		if researchCtx.Err() == context.DeadlineExceeded {
			_ = h.messenger.SendText(ctx, contextID, "⏱ Research timed out. Try a more specific query.")
		} else {
			_ = h.messenger.SendText(ctx, contextID, fmt.Sprintf("❌ Research failed: %s", err.Error()))
		}
		return
	}

	content := CleanInternalSignals(result.Output)
	if content == "" {
		_ = h.messenger.SendText(ctx, contextID, "Research completed but produced no output.")
		return
	}

	_ = h.messenger.SendChunked(ctx, contextID, threadID, content, "")
}

func (h *Handler) handlePlanning(ctx context.Context, contextID, threadID, request string) {
	_ = h.messenger.SendText(ctx, contextID, "📐 Drafting plan...")

	taskID := fmt.Sprintf("PLAN-%d", time.Now().Unix())
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
		ProjectPath: h.getActiveProjectPath(contextID),
		CreatePR:    false,
	}

	planTimeout := 2 * time.Minute
	if h.runner.Config() != nil && h.runner.Config().PlanningTimeout > 0 {
		planTimeout = h.runner.Config().PlanningTimeout
	}
	planCtx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()

	h.log.Info("Creating plan", slog.String("task_id", taskID), slog.String("context_id", contextID))
	result, err := h.runner.Execute(planCtx, task)

	if err != nil {
		if planCtx.Err() == context.DeadlineExceeded {
			_ = h.messenger.SendText(ctx, contextID, "⏱ Planning timed out. Try a simpler request.")
		} else {
			_ = h.messenger.SendText(ctx, contextID, fmt.Sprintf("❌ Planning failed: %s", err.Error()))
		}
		return
	}

	planContent := CleanInternalSignals(result.Output)
	if planContent == "" {
		_ = h.messenger.SendText(ctx, contextID, "Planning completed but produced no output.")
		return
	}

	// Store plan as pending task for execution
	h.mu.Lock()
	h.pendingTasks[contextID] = &PendingTask{
		TaskID:      taskID,
		Description: fmt.Sprintf("## Implementation Plan\n\n%s\n\n## Original Request\n\n%s", planContent, request),
		ContextID:   contextID,
		ThreadID:    threadID,
		SenderID:    h.lastSender[contextID],
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	// Send plan with confirmation prompt
	summary := TruncateText(planContent, h.messenger.MaxMessageLength()-100)
	_, err = h.messenger.SendConfirmation(ctx, contextID, threadID, taskID, summary, h.getActiveProjectPath(contextID))
	if err != nil {
		h.log.Warn("Failed to send plan confirmation, falling back to text", slog.Any("error", err))
		_ = h.messenger.SendChunked(ctx, contextID, threadID, planContent, "📋 Implementation Plan")
		_ = h.messenger.SendText(ctx, contextID, "Reply yes to execute or no to cancel.")
	}
}

func (h *Handler) handleChat(ctx context.Context, contextID, threadID, message string) {
	_ = h.messenger.SendText(ctx, contextID, "💬 Thinking...")

	taskID := fmt.Sprintf("CHAT-%d", time.Now().Unix())
	task := &executor.Task{
		ID:    taskID,
		Title: "Chat: " + TruncateText(message, 30),
		Description: fmt.Sprintf(`You are Pilot, an AI assistant for the codebase at %s.

The user wants to have a conversation (not execute a task).
Respond helpfully and conversationally. You can reference project knowledge but DO NOT make code changes.

Be concise - this is a chat conversation, not a report. Keep response under 500 words.

User message: %s`, h.getActiveProjectPath(contextID), message),
		ProjectPath: h.getActiveProjectPath(contextID),
		CreatePR:    false,
	}

	chatCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	h.log.Debug("Chat response", slog.String("task_id", taskID), slog.String("context_id", contextID))
	result, err := h.runner.Execute(chatCtx, task)

	if err != nil {
		if chatCtx.Err() == context.DeadlineExceeded {
			_ = h.messenger.SendText(ctx, contextID, "⏱ Took too long to respond. Try a simpler question.")
		} else {
			_ = h.messenger.SendText(ctx, contextID, "Sorry, I couldn't process that. Try rephrasing?")
		}
		return
	}

	response := CleanInternalSignals(result.Output)
	if response == "" {
		response = "I'm not sure how to respond to that. Could you rephrase?"
	}

	// Truncate to fit platform limit
	maxLen := h.messenger.MaxMessageLength()
	if maxLen > 0 && len(response) > maxLen {
		response = response[:maxLen-3] + "..."
	}

	_ = h.messenger.SendText(ctx, contextID, response)

	// Record in conversation history
	if h.convStore != nil {
		h.convStore.Add(contextID, "assistant", TruncateText(response, 500))
	}
}

func (h *Handler) handleTask(ctx context.Context, contextID, threadID, description, senderID string) {
	// Task rate limit
	if !h.rateLimit.AllowTask(contextID) {
		h.log.Warn("Task rate limit exceeded", slog.String("context_id", contextID))
		_ = h.messenger.SendText(ctx, contextID,
			"⚠️ Task rate limit exceeded. You've submitted too many tasks recently. Please wait before submitting more.")
		return
	}

	// Check for existing pending task
	h.mu.Lock()
	if existing, exists := h.pendingTasks[contextID]; exists {
		h.mu.Unlock()
		_ = h.messenger.SendText(ctx, contextID,
			fmt.Sprintf("⚠️ You already have a pending task: %s\n\nReply yes to execute or no to cancel.", existing.TaskID))
		return
	}
	h.mu.Unlock()

	taskID := fmt.Sprintf("%s-%d", h.taskIDPrefix, time.Now().Unix())

	h.mu.Lock()
	h.pendingTasks[contextID] = &PendingTask{
		TaskID:      taskID,
		Description: description,
		ContextID:   contextID,
		ThreadID:    threadID,
		SenderID:    senderID,
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	_, err := h.messenger.SendConfirmation(ctx, contextID, threadID, taskID, description, h.getActiveProjectPath(contextID))
	if err != nil {
		h.log.Warn("Failed to send task confirmation", slog.Any("error", err))
		_ = h.messenger.SendText(ctx, contextID,
			fmt.Sprintf("📋 Task %s\n\n%s\n\nReply yes to execute or no to cancel.",
				taskID, TruncateText(description, 500)))
	}
}

// ---------- confirmation & execution ----------

func (h *Handler) handleConfirmation(ctx context.Context, contextID, threadID string, confirmed bool) {
	h.mu.Lock()
	pending, exists := h.pendingTasks[contextID]
	if exists {
		delete(h.pendingTasks, contextID)
	}
	h.mu.Unlock()

	if !exists {
		_ = h.messenger.SendText(ctx, contextID, "No pending task to confirm.")
		return
	}

	if !confirmed {
		_ = h.messenger.SendText(ctx, contextID, fmt.Sprintf("❌ Task %s cancelled.", pending.TaskID))
		return
	}

	h.executeTask(ctx, contextID, threadID, pending.TaskID, pending.Description)
}

func (h *Handler) executeTask(ctx context.Context, contextID, threadID, taskID, description string) {
	// Detect ephemeral tasks
	createPR := true
	detectEphemeral := true
	if h.runner.Config() != nil && h.runner.Config().DetectEphemeral != nil {
		detectEphemeral = *h.runner.Config().DetectEphemeral
	}
	if detectEphemeral && intent.IsEphemeralTask(description) {
		createPR = false
		h.log.Debug("Ephemeral task detected - skipping PR creation",
			slog.String("task_id", taskID))
	}

	// Send starting message
	prNote := ""
	if !createPR {
		prNote = " (no PR)"
	}
	detail := fmt.Sprintf("🚀 Starting %s%s...", taskID, prNote)
	msgRef, err := h.messenger.SendProgress(ctx, contextID, "", taskID, "Starting"+prNote, 0, "Initializing...")
	if err != nil {
		h.log.Warn("Failed to send progress start", slog.Any("error", err))
		_ = h.messenger.SendText(ctx, contextID, detail)
	}

	// Track running task
	taskCtx, taskCancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.runningTasks[contextID] = &RunningTask{
		TaskID:    taskID,
		ContextID: contextID,
		StartedAt: time.Now(),
		Cancel:    taskCancel,
	}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.runningTasks, contextID)
		h.mu.Unlock()
		taskCancel()
	}()

	// Build executor task
	branch := ""
	baseBranch := ""
	if createPR {
		branch = fmt.Sprintf("pilot/%s", taskID)
		baseBranch = "main"
	}

	memberID := h.resolveMemberID(contextID)

	task := &executor.Task{
		ID:          taskID,
		Title:       TruncateText(description, 50),
		Description: description,
		ProjectPath: h.getActiveProjectPath(contextID),
		Verbose:     false,
		Branch:      branch,
		BaseBranch:  baseBranch,
		CreatePR:    createPR,
		MemberID:    memberID,
	}

	// Progress callback with throttling
	if msgRef != "" && h.runner != nil {
		var lastPhase string
		var lastProgress int
		var lastUpdate time.Time

		h.runner.OnProgress(func(tid, phase string, progress int, message string) {
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

			newRef, _ := h.messenger.SendProgress(ctx, contextID, msgRef, taskID, phase, progress, message)
			if newRef != "" {
				msgRef = newRef
			}
		})
	}

	// Execute
	h.log.Info("Executing task", slog.String("task_id", taskID), slog.String("context_id", contextID))
	result, err := h.runner.Execute(taskCtx, task)

	// Clear progress callback
	if h.runner != nil {
		h.runner.OnProgress(nil)
	}

	if err != nil {
		_ = h.messenger.SendResult(ctx, contextID, threadID, taskID, false, err.Error(), "")
		return
	}

	output := CleanInternalSignals(result.Output)
	_ = h.messenger.SendResult(ctx, contextID, threadID, taskID, result.Success, output, result.PRUrl)
}

// ---------- project management ----------

func (h *Handler) getActiveProjectPath(contextID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if path, ok := h.activeProject[contextID]; ok {
		return path
	}
	return h.projectPath
}

// SetActiveProject sets the active project for a context by name.
func (h *Handler) SetActiveProject(contextID, projectName string) error {
	if h.projects == nil {
		return fmt.Errorf("no projects configured")
	}
	proj := h.projects.GetProjectByName(projectName)
	if proj == nil {
		return fmt.Errorf("project '%s' not found", projectName)
	}
	h.mu.Lock()
	h.activeProject[contextID] = proj.Path
	h.mu.Unlock()
	return nil
}

// GetActiveProject returns (name, path) for the active project in a given context.
func (h *Handler) GetActiveProject(contextID string) (string, string) {
	path := h.getActiveProjectPath(contextID)
	if h.projects != nil {
		if proj := h.projects.GetProjectByPath(path); proj != nil {
			return proj.Name, proj.Path
		}
	}
	return "", path
}

// ---------- RBAC ----------

func (h *Handler) resolveMemberID(contextID string) string {
	if h.memberResolver == nil {
		return ""
	}

	h.mu.Lock()
	senderID := h.lastSender[contextID]
	h.mu.Unlock()

	if senderID == "" {
		return ""
	}

	memberID, err := h.memberResolver.ResolveIdentity(senderID)
	if err != nil {
		h.log.Warn("failed to resolve identity",
			slog.String("sender_id", senderID),
			slog.Any("error", err))
		return ""
	}
	return memberID
}

// ---------- state accessors (for CommandHandler wiring) ----------

// GetPendingTask returns the pending task for a context, if any.
func (h *Handler) GetPendingTask(contextID string) *PendingTask {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pendingTasks[contextID]
}

// GetRunningTask returns the running task for a context, if any.
func (h *Handler) GetRunningTask(contextID string) *RunningTask {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runningTasks[contextID]
}

// CancelTask cancels any pending or running task for a context.
func (h *Handler) CancelTask(ctx context.Context, contextID string) error {
	h.mu.Lock()
	pending, hasPending := h.pendingTasks[contextID]
	if hasPending {
		delete(h.pendingTasks, contextID)
	}
	running, hasRunning := h.runningTasks[contextID]
	if hasRunning {
		running.Cancel()
		delete(h.runningTasks, contextID)
	}
	h.mu.Unlock()

	if hasPending {
		_ = h.messenger.SendText(ctx, contextID, fmt.Sprintf("❌ Cancelled pending task %s", pending.TaskID))
		return nil
	}
	if hasRunning {
		_ = h.messenger.SendText(ctx, contextID, fmt.Sprintf("🛑 Stopping task %s", running.TaskID))
		return nil
	}
	return fmt.Errorf("no task to cancel")
}

// ---------- cleanup ----------

// CleanupLoop runs a background goroutine that removes expired pending tasks.
// Call with go h.CleanupLoop(ctx) and track with a WaitGroup externally.
func (h *Handler) CleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.cleanupExpiredTasks(ctx)
		}
	}
}

func (h *Handler) cleanupExpiredTasks(ctx context.Context) {
	h.mu.Lock()
	var expired []string
	for id, task := range h.pendingTasks {
		if time.Since(task.CreatedAt) > 5*time.Minute {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		delete(h.pendingTasks, id)
	}
	h.mu.Unlock()

	for _, id := range expired {
		_ = h.messenger.SendText(ctx, id, "⏰ Pending task expired (5 min timeout). Send a new request.")
	}
}
