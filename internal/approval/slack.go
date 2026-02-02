package approval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
)

// SlackClient defines the interface for Slack operations
// This allows the approval handler to use the existing Slack client
type SlackClient interface {
	PostInteractiveMessage(ctx context.Context, msg *SlackInteractiveMessage) (*SlackPostMessageResponse, error)
	UpdateInteractiveMessage(ctx context.Context, channel, ts string, blocks []interface{}, text string) error
}

// SlackInteractiveMessage represents a Slack message with interactive buttons
type SlackInteractiveMessage struct {
	Channel string        `json:"channel"`
	Text    string        `json:"text,omitempty"`
	Blocks  []interface{} `json:"blocks,omitempty"`
}

// SlackPostMessageResponse represents the response from posting a message
type SlackPostMessageResponse struct {
	OK      bool   `json:"ok"`
	TS      string `json:"ts"`
	Channel string `json:"channel"`
	Error   string `json:"error,omitempty"`
}

// SlackTextObject represents text in a Slack block
type SlackTextObject struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// SlackButtonElement represents an interactive button in Slack
type SlackButtonElement struct {
	Type     string          `json:"type"`
	Text     *SlackTextObject `json:"text"`
	ActionID string          `json:"action_id"`
	Value    string          `json:"value,omitempty"`
	Style    string          `json:"style,omitempty"` // "primary" or "danger"
}

// SlackSectionBlock represents a section block
type SlackSectionBlock struct {
	Type string          `json:"type"`
	Text *SlackTextObject `json:"text,omitempty"`
}

// SlackActionsBlock represents an actions block containing buttons
type SlackActionsBlock struct {
	Type     string               `json:"type"`
	BlockID  string               `json:"block_id,omitempty"`
	Elements []SlackButtonElement `json:"elements"`
}

// SlackHandler handles approval requests via Slack
type SlackHandler struct {
	client  SlackClient
	channel string
	pending map[string]*slackPending // requestID -> pending state
	mu      sync.RWMutex
	log     *slog.Logger
}

// slackPending tracks a pending Slack approval request
type slackPending struct {
	Request    *Request
	TS         string // Slack message timestamp (used as message ID)
	Channel    string
	ResponseCh chan *Response
}

// NewSlackHandler creates a new Slack approval handler
func NewSlackHandler(client SlackClient, channel string) *SlackHandler {
	return &SlackHandler{
		client:  client,
		channel: channel,
		pending: make(map[string]*slackPending),
		log:     logging.WithComponent("approval.slack"),
	}
}

// Name returns the handler name
func (h *SlackHandler) Name() string {
	return "slack"
}

// SendApprovalRequest sends an approval request via Slack
func (h *SlackHandler) SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error) {
	responseCh := make(chan *Response, 1)

	// Build message blocks
	blocks := h.buildApprovalBlocks(req)

	// Create interactive message
	msg := &SlackInteractiveMessage{
		Channel: h.channel,
		Text:    h.formatFallbackText(req), // Fallback for notifications
		Blocks:  blocks,
	}

	// Send message
	resp, err := h.client.PostInteractiveMessage(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("failed to send Slack message: %w", err)
	}

	// Track pending request
	h.mu.Lock()
	h.pending[req.ID] = &slackPending{
		Request:    req,
		TS:         resp.TS,
		Channel:    resp.Channel,
		ResponseCh: responseCh,
	}
	h.mu.Unlock()

	h.log.Debug("Sent approval request",
		slog.String("request_id", req.ID),
		slog.String("ts", resp.TS))

	return responseCh, nil
}

// CancelRequest cancels a pending approval request
func (h *SlackHandler) CancelRequest(ctx context.Context, requestID string) error {
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
	if pending.TS != "" {
		blocks := h.buildCancelledBlocks(pending.Request)
		text := h.formatCancelledText(pending.Request)
		if err := h.client.UpdateInteractiveMessage(ctx, pending.Channel, pending.TS, blocks, text); err != nil {
			h.log.Warn("Failed to update cancelled message", slog.Any("error", err))
		}
	}

	// Close response channel
	close(pending.ResponseCh)

	return nil
}

// HandleInteraction processes a Slack interaction (button press)
// This should be called by the Slack webhook handler when receiving interactions
func (h *SlackHandler) HandleInteraction(ctx context.Context, actionID, value, userID, username, responseURL string) bool {
	// Parse value: "approve:<requestID>" or "reject:<requestID>"
	var decision Decision
	var requestID string

	if len(value) > 8 && value[:8] == "approve:" {
		decision = DecisionApproved
		requestID = value[8:]
	} else if len(value) > 7 && value[:7] == "reject:" {
		decision = DecisionRejected
		requestID = value[7:]
	} else {
		return false // Not an approval action
	}

	h.mu.Lock()
	pending, exists := h.pending[requestID]
	if exists {
		delete(h.pending, requestID)
	}
	h.mu.Unlock()

	if !exists {
		h.log.Debug("Approval request not found or already processed",
			slog.String("request_id", requestID))
		return true // Still handled, just expired
	}

	// Update message to show result
	if pending.TS != "" {
		blocks := h.buildResponseBlocks(pending.Request, decision, username)
		text := h.formatResponseText(pending.Request, decision, username)
		if err := h.client.UpdateInteractiveMessage(ctx, pending.Channel, pending.TS, blocks, text); err != nil {
			h.log.Warn("Failed to update response message", slog.Any("error", err))
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

	h.log.Info("Approval interaction handled",
		slog.String("request_id", requestID),
		slog.String("decision", string(decision)),
		slog.String("user", username))

	return true
}

// buildApprovalBlocks creates Slack blocks for an approval request
func (h *SlackHandler) buildApprovalBlocks(req *Request) []interface{} {
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

	// Header section
	headerText := fmt.Sprintf("%s *%s*\n\n*Task:* `%s`\n*Title:* %s",
		icon, stageLabel, req.TaskID, req.Title)

	if req.Description != "" {
		headerText += fmt.Sprintf("\n\n%s", truncateForSlack(req.Description, 500))
	}

	// Add metadata
	if prURL, ok := req.Metadata["pr_url"].(string); ok && prURL != "" {
		headerText += fmt.Sprintf("\n\n*PR:* <%s|View Pull Request>", prURL)
	}
	if errorMsg, ok := req.Metadata["error"].(string); ok && errorMsg != "" {
		headerText += fmt.Sprintf("\n\n*Error:* ```%s```", truncateForSlack(errorMsg, 200))
	}

	// Add timeout info
	timeLeft := time.Until(req.ExpiresAt).Round(time.Minute)
	headerText += fmt.Sprintf("\n\n_Expires in: %s_", formatDuration(timeLeft))

	blocks := []interface{}{
		SlackSectionBlock{
			Type: "section",
			Text: &SlackTextObject{
				Type: "mrkdwn",
				Text: headerText,
			},
		},
	}

	// Add approval buttons
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

	actionsBlock := SlackActionsBlock{
		Type:    "actions",
		BlockID: "approval_actions",
		Elements: []SlackButtonElement{
			{
				Type: "button",
				Text: &SlackTextObject{
					Type:  "plain_text",
					Text:  approveText,
					Emoji: true,
				},
				ActionID: "approve",
				Value:    "approve:" + req.ID,
				Style:    "primary",
			},
			{
				Type: "button",
				Text: &SlackTextObject{
					Type:  "plain_text",
					Text:  rejectText,
					Emoji: true,
				},
				ActionID: "reject",
				Value:    "reject:" + req.ID,
				Style:    "danger",
			},
		},
	}

	blocks = append(blocks, actionsBlock)
	return blocks
}

// buildResponseBlocks creates Slack blocks for a response message (no buttons)
func (h *SlackHandler) buildResponseBlocks(req *Request, decision Decision, username string) []interface{} {
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

	text := fmt.Sprintf("%s *%s*\n\n*Task:* `%s`\n*Title:* %s\n\n*Decision by:* %s",
		icon, status, req.TaskID, req.Title, username)

	return []interface{}{
		SlackSectionBlock{
			Type: "section",
			Text: &SlackTextObject{
				Type: "mrkdwn",
				Text: text,
			},
		},
	}
}

// buildCancelledBlocks creates Slack blocks for a cancelled request
func (h *SlackHandler) buildCancelledBlocks(req *Request) []interface{} {
	text := fmt.Sprintf("⏹ *CANCELLED*\n\n*Task:* `%s`\n*Title:* %s\n\n_Approval request was cancelled._",
		req.TaskID, req.Title)

	return []interface{}{
		SlackSectionBlock{
			Type: "section",
			Text: &SlackTextObject{
				Type: "mrkdwn",
				Text: text,
			},
		},
	}
}

// formatFallbackText creates fallback text for notifications
func (h *SlackHandler) formatFallbackText(req *Request) string {
	return fmt.Sprintf("Approval required for task %s: %s", req.TaskID, req.Title)
}

// formatResponseText creates fallback text for response messages
func (h *SlackHandler) formatResponseText(req *Request, decision Decision, username string) string {
	return fmt.Sprintf("Task %s %s by %s", req.TaskID, string(decision), username)
}

// formatCancelledText creates fallback text for cancelled messages
func (h *SlackHandler) formatCancelledText(req *Request) string {
	return fmt.Sprintf("Approval request for task %s was cancelled", req.TaskID)
}

// truncateForSlack truncates text to fit Slack message limits
func truncateForSlack(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen-3] + "..."
}
