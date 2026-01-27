package approval

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// TelegramClient defines the interface for Telegram operations
// This allows the approval handler to use the existing Telegram client
type TelegramClient interface {
	SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]InlineKeyboardButton) (*MessageResponse, error)
	EditMessage(ctx context.Context, chatID string, messageID int64, text, parseMode string) error
	AnswerCallback(ctx context.Context, callbackID, text string) error
}

// InlineKeyboardButton represents a Telegram inline keyboard button
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// MessageResponse represents a Telegram API response with message result
type MessageResponse struct {
	Result *MessageResult `json:"result"`
}

// MessageResult contains the sent message details
type MessageResult struct {
	MessageID int64 `json:"message_id"`
}

// TelegramHandler handles approval requests via Telegram
type TelegramHandler struct {
	client  TelegramClient
	chatID  string
	pending map[string]*telegramPending // requestID -> pending state
	mu      sync.RWMutex
	log     *slog.Logger
}

// telegramPending tracks a pending Telegram approval request
type telegramPending struct {
	Request    *Request
	MessageID  int64
	ResponseCh chan *Response
}

// NewTelegramHandler creates a new Telegram approval handler
func NewTelegramHandler(client TelegramClient, chatID string) *TelegramHandler {
	return &TelegramHandler{
		client:  client,
		chatID:  chatID,
		pending: make(map[string]*telegramPending),
		log:     logging.WithComponent("approval.telegram"),
	}
}

// Name returns the handler name
func (h *TelegramHandler) Name() string {
	return "telegram"
}

// SendApprovalRequest sends an approval request via Telegram
func (h *TelegramHandler) SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error) {
	responseCh := make(chan *Response, 1)

	// Format message based on stage
	text := h.formatApprovalMessage(req)

	// Create inline keyboard with approve/reject buttons
	keyboard := h.createApprovalKeyboard(req)

	// Send message
	resp, err := h.client.SendMessageWithKeyboard(ctx, h.chatID, text, "", keyboard)
	if err != nil {
		return nil, fmt.Errorf("failed to send Telegram message: %w", err)
	}

	// Track pending request
	var messageID int64
	if resp != nil && resp.Result != nil {
		messageID = resp.Result.MessageID
	}

	h.mu.Lock()
	h.pending[req.ID] = &telegramPending{
		Request:    req,
		MessageID:  messageID,
		ResponseCh: responseCh,
	}
	h.mu.Unlock()

	h.log.Debug("Sent approval request",
		slog.String("request_id", req.ID),
		slog.Int64("message_id", messageID))

	return responseCh, nil
}

// CancelRequest cancels a pending approval request
func (h *TelegramHandler) CancelRequest(ctx context.Context, requestID string) error {
	h.mu.Lock()
	pending, exists := h.pending[requestID]
	if exists {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()

	if !exists {
		return nil
	}

	// Update message to show cancelled
	if pending.MessageID != 0 {
		text := h.formatCancelledMessage(pending.Request)
		if err := h.client.EditMessage(ctx, h.chatID, pending.MessageID, text, ""); err != nil {
			h.log.Warn("Failed to edit cancelled message", slog.Any("error", err))
		}
	}

	// Close response channel
	close(pending.ResponseCh)

	return nil
}

// HandleCallback processes a Telegram callback (button press)
// This should be called by the main Telegram handler when receiving callbacks
func (h *TelegramHandler) HandleCallback(ctx context.Context, callbackID, data, userID, username string) bool {
	// Parse callback data: "approve:<requestID>" or "reject:<requestID>"
	var decision Decision
	var requestID string

	if len(data) > 8 && data[:8] == "approve:" {
		decision = DecisionApproved
		requestID = data[8:]
	} else if len(data) > 7 && data[:7] == "reject:" {
		decision = DecisionRejected
		requestID = data[7:]
	} else {
		return false // Not an approval callback
	}

	h.mu.Lock()
	pending, exists := h.pending[requestID]
	if exists {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()

	if !exists {
		_ = h.client.AnswerCallback(ctx, callbackID, "Request expired or already processed")
		return true
	}

	// Answer callback
	var answerText string
	if decision == DecisionApproved {
		answerText = "Approved!"
	} else {
		answerText = "Rejected"
	}
	_ = h.client.AnswerCallback(ctx, callbackID, answerText)

	// Update message to show result
	if pending.MessageID != 0 {
		text := h.formatResponseMessage(pending.Request, decision, username)
		if err := h.client.EditMessage(ctx, h.chatID, pending.MessageID, text, ""); err != nil {
			h.log.Warn("Failed to edit response message", slog.Any("error", err))
		}
	}

	// Send response
	response := &Response{
		RequestID:   requestID,
		Decision:    decision,
		ApprovedBy:  username,
		RespondedAt: time.Now(),
	}

	select {
	case pending.ResponseCh <- response:
	default:
	}
	close(pending.ResponseCh)

	h.log.Info("Approval callback handled",
		slog.String("request_id", requestID),
		slog.String("decision", string(decision)),
		slog.String("user", username))

	return true
}

// formatApprovalMessage formats the approval request message
func (h *TelegramHandler) formatApprovalMessage(req *Request) string {
	var icon, stageLabel string

	switch req.Stage {
	case StagePreExecution:
		icon = "🚀"
		stageLabel = "Pre-Execution Approval"
	case StagePreMerge:
		icon = "🔀"
		stageLabel = "Pre-Merge Approval"
	case StagePostFailure:
		icon = "❌"
		stageLabel = "Post-Failure Decision"
	default:
		icon = "⚠️"
		stageLabel = "Approval Required"
	}

	text := fmt.Sprintf("%s %s\n\nTask: %s\n%s", icon, stageLabel, req.TaskID, req.Title)

	if req.Description != "" {
		text += fmt.Sprintf("\n\n%s", truncateForTelegram(req.Description, 500))
	}

	// Add metadata
	if prURL, ok := req.Metadata["pr_url"].(string); ok && prURL != "" {
		text += fmt.Sprintf("\n\nPR: %s", prURL)
	}
	if errorMsg, ok := req.Metadata["error"].(string); ok && errorMsg != "" {
		text += fmt.Sprintf("\n\nError: %s", truncateForTelegram(errorMsg, 200))
	}

	// Add timeout info
	timeLeft := time.Until(req.ExpiresAt).Round(time.Minute)
	text += fmt.Sprintf("\n\nExpires in: %s", formatDuration(timeLeft))

	return text
}

// createApprovalKeyboard creates inline keyboard buttons
func (h *TelegramHandler) createApprovalKeyboard(req *Request) [][]InlineKeyboardButton {
	var approveText, rejectText string

	switch req.Stage {
	case StagePreExecution:
		approveText = "✅ Execute"
		rejectText = "❌ Cancel"
	case StagePreMerge:
		approveText = "✅ Merge"
		rejectText = "❌ Reject"
	case StagePostFailure:
		approveText = "🔄 Retry"
		rejectText = "⏹ Abort"
	default:
		approveText = "✅ Approve"
		rejectText = "❌ Reject"
	}

	return [][]InlineKeyboardButton{
		{
			{Text: approveText, CallbackData: "approve:" + req.ID},
			{Text: rejectText, CallbackData: "reject:" + req.ID},
		},
	}
}

// formatResponseMessage formats the message after a response
func (h *TelegramHandler) formatResponseMessage(req *Request, decision Decision, username string) string {
	var icon, status string

	switch decision {
	case DecisionApproved:
		icon = "✅"
		status = "APPROVED"
	case DecisionRejected:
		icon = "❌"
		status = "REJECTED"
	default:
		icon = "⏱"
		status = "TIMEOUT"
	}

	text := fmt.Sprintf("%s %s\n\nTask: %s\n%s\n\nDecision: %s", icon, status, req.TaskID, req.Title, username)

	return text
}

// formatCancelledMessage formats the message when request is cancelled
func (h *TelegramHandler) formatCancelledMessage(req *Request) string {
	return fmt.Sprintf("⏹ CANCELLED\n\nTask: %s\n%s\n\nApproval request was cancelled.", req.TaskID, req.Title)
}

// truncateForTelegram truncates text to fit Telegram message limits
func truncateForTelegram(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-3] + "..."
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "expired"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	hours := int(d.Hours())
	if hours == 1 {
		return "1 hour"
	}
	return strconv.Itoa(hours) + " hours"
}
