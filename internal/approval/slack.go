package approval

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
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
	Type     string           `json:"type"`
	Text     *SlackTextObject `json:"text"`
	ActionID string           `json:"action_id"`
	Value    string           `json:"value,omitempty"`
	Style    string           `json:"style,omitempty"` // "primary" or "danger"
}

// SlackSectionBlock represents a section block
type SlackSectionBlock struct {
	Type string           `json:"type"`
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
	client   SlackClient
	channel  string
	pending  map[string]*slackPending // requestID -> pending state
	mu       sync.RWMutex
	log      *slog.Logger
	store    PendingApprovalStore // optional; enables restart persistence (GH-4411)
	recorder DecisionRecorder     // optional; persists decisions directly (restart-safe, GH-4411)
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

// WithStore attaches a persistence store so pending approvals survive restarts.
// Returns h to allow builder-style chaining after NewSlackHandler.
func (h *SlackHandler) WithStore(store PendingApprovalStore) *SlackHandler {
	h.store = store
	return h
}

// WithDecisionRecorder attaches a DecisionRecorder so HandleInteraction persists
// decisions directly to the PRState/executions store rather than relying solely
// on a live goroutine reading pending.ResponseCh. This is what makes a button
// click on a Rehydrate-restored request actually reach the pipeline — after a
// restart there is no waiter goroutine left to consume the channel (GH-4411,
// mirrors the Telegram fix from GH-3825).
// Returns h to allow builder-style chaining.
func (h *SlackHandler) WithDecisionRecorder(recorder DecisionRecorder) *SlackHandler {
	h.recorder = recorder
	return h
}

// Rehydrate loads persisted pending approvals from the store and re-inserts them
// into the in-memory map so that button clicks on the ORIGINAL Slack message —
// posted before a daemon restart — are still processed rather than falling
// through to "No pending task to confirm." Unlike Telegram (whose callback
// query token can expire while the daemon is down), Slack interactive
// components stay clickable indefinitely, so Rehydrate deliberately does NOT
// repost a fresh message: it re-arms the existing button's requestID in place
// (GH-4411). Expired rows are pruned. No-op when no store is attached.
func (h *SlackHandler) Rehydrate(ctx context.Context) error {
	if h.store == nil {
		return nil
	}
	rows, err := h.store.LoadPendingApprovals()
	if err != nil {
		return fmt.Errorf("rehydrate: load pending approvals: %w", err)
	}
	now := time.Now()
	rehydrated := 0
	for _, row := range rows {
		if row.ExpiresAt.Before(now) {
			_ = h.store.DeletePendingApproval(row.ID)
			continue
		}
		req := &Request{
			ID:               row.ID,
			TaskID:           row.TaskID,
			Stage:            Stage(row.Stage),
			Title:            row.Title,
			Description:      row.Description,
			Metadata:         row.Metadata,
			Approvers:        row.Approvers,
			PreferredChannel: row.PreferredChannel,
			CreatedAt:        row.CreatedAt,
			ExpiresAt:        row.ExpiresAt,
		}
		channel := h.channel
		if len(req.Approvers) > 0 && req.Approvers[0] != "" {
			channel = req.Approvers[0]
		}
		h.mu.Lock()
		if _, exists := h.pending[req.ID]; !exists {
			h.pending[req.ID] = &slackPending{
				Request:    req,
				Channel:    channel,
				ResponseCh: make(chan *Response, 1),
			}
			rehydrated++
		}
		h.mu.Unlock()
	}
	if rehydrated > 0 {
		h.log.Info("rehydrated pending approvals", slog.Int("count", rehydrated))
	}
	return nil
}

// PruneExpired scans the in-memory pending set for requests whose ExpiresAt
// has passed, updates their Slack message to show they expired, removes them
// from the pending map, and deletes their persisted row. It also sweeps the
// store directly for rows with no in-memory counterpart (e.g. left behind by
// a process that crashed before Rehydrate ran). Returns the number of
// in-memory requests pruned.
//
// A request rehydrated after a daemon restart has no waiter goroutine
// enforcing its own timeout — Manager's async dispatch loop only watches
// requests it created in the current process. Without this sweep, a
// rehydrated request that expires just sits in h.pending forever instead of
// resolving to "expired" (GH-4411, mirrors GH-3825).
func (h *SlackHandler) PruneExpired(ctx context.Context) (int, error) {
	now := time.Now()

	h.mu.Lock()
	var expired []*slackPending
	for id, p := range h.pending {
		if p.Request.ExpiresAt.Before(now) {
			expired = append(expired, p)
			delete(h.pending, id)
		}
	}
	h.mu.Unlock()

	for _, p := range expired {
		if h.store != nil {
			if err := h.store.DeletePendingApproval(p.Request.ID); err != nil {
				h.log.Warn("failed to delete expired persisted approval",
					slog.String("request_id", p.Request.ID), slog.Any("error", err))
			}
		}
		if p.TS != "" {
			blocks := h.buildExpiredBlocks(p.Request)
			text := h.formatExpiredText(p.Request)
			if err := h.client.UpdateInteractiveMessage(ctx, p.Channel, p.TS, blocks, text); err != nil {
				h.log.Warn("failed to update expired message", slog.Any("error", err))
			}
		}
		select {
		case p.ResponseCh <- &Response{RequestID: p.Request.ID, Decision: DecisionTimeout, RespondedAt: now}:
		default:
		}
		close(p.ResponseCh)
	}

	if h.store != nil {
		if _, err := h.store.PrunePendingApprovals(now); err != nil {
			return len(expired), fmt.Errorf("prune expired: sweep store: %w", err)
		}
	}

	if len(expired) > 0 {
		h.log.Info("pruned expired pending approvals", slog.Int("count", len(expired)))
	}

	return len(expired), nil
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

	// Best-effort persistence so the request survives a restart (GH-4411).
	if h.store != nil {
		row := &memory.PendingApproval{
			ID:               req.ID,
			TaskID:           req.TaskID,
			Stage:            string(req.Stage),
			Title:            req.Title,
			Description:      req.Description,
			Metadata:         req.Metadata,
			Approvers:        req.Approvers,
			PreferredChannel: req.PreferredChannel,
			CreatedAt:        req.CreatedAt,
			ExpiresAt:        req.ExpiresAt,
		}
		if err := h.store.InsertPendingApproval(row); err != nil {
			h.log.Warn("failed to persist pending approval", slog.String("request_id", req.ID), slog.Any("error", err))
		}
	}

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

	if h.store != nil {
		if err := h.store.DeletePendingApproval(requestID); err != nil {
			h.log.Warn("failed to delete persisted approval on cancel", slog.String("request_id", requestID), slog.Any("error", err))
		}
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
		h.log.Info("Approval interaction for unknown or already-processed request",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username))
		return true // Still handled, just expired
	}

	if h.store != nil {
		if err := h.store.DeletePendingApproval(requestID); err != nil {
			h.log.Warn("failed to delete persisted approval on interaction", slog.String("request_id", requestID), slog.Any("error", err))
		}
	}

	// A click can race the periodic PruneExpired sweep: the request is still
	// in h.pending but its deadline has already passed. Treat it the same as
	// the not-found case instead of recording a real decision — otherwise the
	// user sees "Approved"/"Rejected" for a request the system has already
	// decided to time out (mirrors GH-3825's Telegram handling).
	if pending.Request.ExpiresAt.Before(time.Now()) {
		h.log.Info("Approval interaction arrived after expiry",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username))
		if pending.TS != "" {
			blocks := h.buildExpiredBlocks(pending.Request)
			text := h.formatExpiredText(pending.Request)
			if err := h.client.UpdateInteractiveMessage(ctx, pending.Channel, pending.TS, blocks, text); err != nil {
				h.log.Warn("Failed to update expired message", slog.Any("error", err))
			}
		}
		select {
		case pending.ResponseCh <- &Response{RequestID: requestID, Decision: DecisionTimeout, RespondedAt: time.Now()}:
		default:
		}
		close(pending.ResponseCh)
		return true
	}

	// Persist the decision directly so it reaches the pipeline even when
	// nothing is left waiting on pending.ResponseCh — the common case after a
	// daemon restart, where Rehydrate reconstructs this entry with a fresh
	// channel but no goroutine to read it (GH-4411, mirrors GH-3825).
	if h.recorder != nil {
		if err := h.recorder.RecordDecision(ctx, requestID, decision, username); err != nil {
			h.log.Warn("failed to record approval decision", slog.String("request_id", requestID), slog.Any("error", err))
		}
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

// buildExpiredBlocks creates Slack blocks for an expired request.
func (h *SlackHandler) buildExpiredBlocks(req *Request) []interface{} {
	text := fmt.Sprintf("⏱ *EXPIRED*\n\n*Task:* `%s`\n*Title:* %s\n\n_Approval request expired — no action taken._",
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

// formatExpiredText creates fallback text for expired messages.
func (h *SlackHandler) formatExpiredText(req *Request) string {
	return fmt.Sprintf("Approval request for task %s expired", req.TaskID)
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
