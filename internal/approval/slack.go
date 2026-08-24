package approval

import (
	"context"
	"errors"
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

	// allowedUsers is a handler-scoped fallback allowlist consulted by
	// isAuthorizedApprover when a request's own Request.Approvers is empty
	// (GH-5157, mirrors TelegramHandler.allowedUsers from GH-5155). Set via
	// WithAllowedIDs. Nil/empty means unrestricted — any clicker may decide
	// a request that carries no configured approvers.
	allowedUsers []string
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

// resolveChannel picks the Slack destination for req: the handler's own
// configured channel when one is set, falling back to the first approver
// (a DM) only when no channel is configured. This is channel-first —
// before GH-4808, resolveChannel preferred Approvers[0] unconditionally, so
// any request with an approver landed in an unwatched Pilot-bot DM even
// though `adapters.slack.approval.channel` was configured, making that
// config dead and the ask effectively invisible to the operator (incident:
// PR#4806 sat in awaiting_approval ~50 minutes while the operator searched
// the wrong places). This is the single place that rule lives —
// SendApprovalRequest and Rehydrate both call it, so the first send and any
// post-restart rehydrate never disagree on where a given request's message
// lives (GH-4772).
func (h *SlackHandler) resolveChannel(req *Request) string {
	if h.channel != "" {
		return h.channel
	}
	if len(req.Approvers) > 0 && req.Approvers[0] != "" {
		return req.Approvers[0]
	}
	return h.channel
}

// destinationIsChannel reports whether resolveChannel(req) will land on the
// handler's configured channel (as opposed to a DM fallback to
// Approvers[0]) — i.e. the channel-first branch of resolveChannel fired.
func (h *SlackHandler) destinationIsChannel() bool {
	return h.channel != ""
}

// mentionPrefix returns a Slack `<@U…> ` mention prefix when the message is
// headed to a channel (not a DM) and an approver is configured — so the ask
// notifies the intended approver instead of scrolling by unseen in a
// channel they don't watch closely (GH-4808 acceptance #2). Returns "" when
// the destination is already a DM to the approver (redundant self-mention)
// or when no approver is set.
func (h *SlackHandler) mentionPrefix(req *Request) string {
	if !h.destinationIsChannel() {
		return ""
	}
	if len(req.Approvers) == 0 || req.Approvers[0] == "" {
		return ""
	}
	return fmt.Sprintf("<@%s> ", req.Approvers[0])
}

// ResolvedDestination returns a human-readable description of where
// resolveChannel(req) sends this request — the channel name/ID, or
// "dm:<target>" when it falls back to a DM. Used only for logging (the
// "async approval request submitted" line), so "where did the ask go" is
// answerable from daemon.log alone without Slack-side message search
// (GH-4808 acceptance #4). Implements the approval package's optional
// destinationDescriber interface consumed by Manager.
func (h *SlackHandler) ResolvedDestination(req *Request) string {
	dest := h.resolveChannel(req)
	if dest == "" {
		return ""
	}
	if h.destinationIsChannel() {
		return dest
	}
	return "dm:" + dest
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

// WithAllowedIDs attaches a handler-scoped allowlist of user IDs permitted
// to decide approval requests whose own Request.Approvers is empty
// (GH-5157, mirrors TelegramHandler.WithAllowedUsers from GH-5155).
// Requests that do carry Approvers are gated by that list instead — this
// fallback only applies when Approvers is unset. Returns h to allow
// builder-style chaining.
func (h *SlackHandler) WithAllowedIDs(ids []string) *SlackHandler {
	h.allowedUsers = ids
	return h
}

// isAuthorizedApprover reports whether userID may decide a request via this
// handler (GH-5157, mirrors TelegramHandler.isAuthorizedApprover). When
// approvers (the request's own Request.Approvers) is non-empty it is the
// authoritative allowlist — userID must exactly match one of its entries via
// plain string equality, since Approvers already carries whatever identity
// format the operator configured for this channel (e.g. Slack user ids).
// When approvers is empty, control falls back to the handler-scoped
// allowedUsers set (see WithAllowedIDs) so a channel can still restrict who
// may decide requests that didn't specify their own approver list. Both
// empty means unrestricted — any user may decide, preserving behavior for
// callers that never configured either.
func (h *SlackHandler) isAuthorizedApprover(userID string, approvers []string) bool {
	if len(approvers) > 0 {
		for _, a := range approvers {
			if a == userID {
				return true
			}
		}
		return false
	}
	if len(h.allowedUsers) == 0 {
		return true
	}
	for _, u := range h.allowedUsers {
		if u == userID {
			return true
		}
	}
	return false
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
		// GH-4772: only rehydrate rows this handler actually owns — a row
		// originally dispatched to Telegram (or any other channel) must not
		// be re-armed here, and this handler must not delete an expired row
		// it doesn't own out from under its owning handler's own sweep. See
		// ownsChannel/DefaultChannelName.
		if !ownsChannel(h.Name(), row.PreferredChannel) {
			continue
		}
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
			Project:          row.Project,
			CreatedAt:        row.CreatedAt,
			ExpiresAt:        row.ExpiresAt,
		}
		channel := h.resolveChannel(req)
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
		// GH-4772: scope the store-level sweep to rows this handler owns
		// (see TelegramHandler.PruneExpired's matching comment). Slack is
		// never the default channel, so it never runs the orphan fallback.
		if _, err := h.store.PrunePendingApprovals(now, ownedChannels(h.Name())); err != nil {
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
		Channel: h.resolveChannel(req),
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
			Project:          req.Project,
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
	// GH-5157: decide authorization inside the same critical section as the
	// lookup, and only delete from h.pending when authorized — an
	// unauthorized click must not mutate any state (pending map, store,
	// resolved decision), mirroring TelegramHandler.HandleCallback (GH-5155).
	authorized := true
	if exists {
		authorized = h.isAuthorizedApprover(userID, pending.Request.Approvers)
		if authorized {
			delete(h.pending, requestID)
		}
	}
	h.mu.Unlock()

	if !exists {
		h.log.Info("Approval interaction for unknown or already-processed request",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username))
		return true // Still handled, just expired
	}

	if !authorized {
		h.log.Info("Approval interaction from unauthorized user",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username),
			slog.String("user_id", userID))
		return true
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
	//
	// GH-4777: a click can lose the race to a concurrent HTTP POST (or the
	// other channel) — RecordDecision then returns memory.ErrApprovalAlreadyDecided.
	// The clicker must see "already decided", not a success card claiming
	// their click won when it didn't (PR#4767 review).
	raceLost := false
	if h.recorder != nil {
		if err := h.recorder.RecordDecision(ctx, requestID, decision, username); err != nil {
			if errors.Is(err, memory.ErrApprovalAlreadyDecided) {
				raceLost = true
				h.log.Info("approval decision race lost — another decider already recorded",
					slog.String("request_id", requestID), slog.String("user", username))
			} else {
				h.log.Warn("failed to record approval decision", slog.String("request_id", requestID), slog.Any("error", err))
			}
		}
	}

	// Update message to show result
	if pending.TS != "" {
		var blocks []interface{}
		var text string
		if raceLost {
			blocks = h.buildAlreadyDecidedBlocks(pending.Request)
			text = h.formatAlreadyDecidedText(pending.Request)
		} else {
			blocks = h.buildResponseBlocks(pending.Request, decision, username)
			text = h.formatResponseText(pending.Request, decision, username)
		}
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
	headerText := fmt.Sprintf("%s%s *%s*\n\n*Task:* `%s`\n*Title:* %s",
		h.mentionPrefix(req), icon, stageLabel, req.TaskID, req.Title)

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

// buildAlreadyDecidedBlocks creates Slack blocks for a click that lost the
// decision race (GH-4777) — another decider (a concurrent HTTP POST, or the
// same request answered through a different channel) already recorded the
// outcome first.
func (h *SlackHandler) buildAlreadyDecidedBlocks(req *Request) []interface{} {
	text := fmt.Sprintf("⚠️ *ALREADY DECIDED*\n\n*Task:* `%s`\n*Title:* %s\n\n_Someone else already recorded a decision for this request._",
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

// formatAlreadyDecidedText creates fallback text for the already-decided race-loss message.
func (h *SlackHandler) formatAlreadyDecidedText(req *Request) string {
	return fmt.Sprintf("Task %s already decided by someone else", req.TaskID)
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
	return fmt.Sprintf("%sApproval required for task %s: %s", h.mentionPrefix(req), req.TaskID, req.Title)
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
