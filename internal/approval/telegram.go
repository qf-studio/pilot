package approval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/pilot/internal/logging"
	"github.com/qf-studio/pilot/internal/memory"
)

// PendingApprovalStore persists pending approval requests across restarts.
// *memory.Store satisfies this interface directly.
type PendingApprovalStore interface {
	InsertPendingApproval(*memory.PendingApproval) error
	DeletePendingApproval(id string) error
	LoadPendingApprovals() ([]*memory.PendingApproval, error)
	// PrunePendingApprovals channel-scopes the expiry sweep to `channels`
	// (GH-4772) — see ownedChannels.
	PrunePendingApprovals(cutoff time.Time, channels []string) (int64, error)
	// PrunePendingApprovalsOutside sweeps rows whose channel matches none of
	// `knownChannels` — the orphan fallback (GH-4772). Only the default
	// channel handler should call this (see DefaultChannelName).
	PrunePendingApprovalsOutside(cutoff time.Time, knownChannels []string) (int64, error)
}

// TelegramClient defines the interface for Telegram operations
// This allows the approval handler to use the existing Telegram client
type TelegramClient interface {
	SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]InlineKeyboardButton, messageThreadID int64) (*MessageResponse, error)
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
	client          TelegramClient
	chatID          string
	messageThreadID int64
	pending         map[string]*telegramPending  // requestID -> pending state
	resolved        map[string]*telegramResolved // requestID -> approved decision (for a later merge follow-up)
	mu              sync.RWMutex
	log             *slog.Logger
	store           PendingApprovalStore // optional; enables restart persistence
	recorder        DecisionRecorder     // optional; persists decisions directly (restart-safe)

	// warnedInvalidDest dedupes the "Approvers[0] is not a valid Telegram
	// destination" warning per bad value (GH-4380), so a persistently
	// misconfigured Approvers entry logs once instead of once per tick.
	warnedInvalidDest map[string]bool

	// allowedUsers is a handler-scoped fallback allowlist consulted by
	// isAuthorizedApprover when a request's own Request.Approvers is empty
	// (GH-5155). Set via WithAllowedUsers. Nil/empty means unrestricted —
	// any tapper may decide a request that carries no configured approvers.
	allowedUsers []string
}

// telegramPending tracks a pending Telegram approval request
type telegramPending struct {
	Request    *Request
	MessageID  int64
	ChatID     string // resolved destination — may differ from h.chatID when approvers are set
	ResponseCh chan *Response
}

// telegramResolved tracks an approved decision's chat/message so a later
// NotifyMerged call can post the merge follow-up in the same chat. Only
// approved decisions are tracked here — rejected/timeout requests never
// reach a merge (GH-4164).
type telegramResolved struct {
	Request   *Request
	ChatID    string
	MessageID int64
	DecidedAt time.Time
}

// resolvedRetention bounds how long an approved decision's chat/message is
// kept in TelegramHandler.resolved awaiting a merge follow-up. A merge
// normally follows approval within minutes; this is just a leak guard for
// approved requests whose PR never actually merges (closed, cascade failure,
// etc.) so the map doesn't grow unbounded (GH-4164).
const resolvedRetention = 24 * time.Hour

// NewTelegramHandler creates a new Telegram approval handler
func NewTelegramHandler(client TelegramClient, chatID string, messageThreadID int64) *TelegramHandler {
	return &TelegramHandler{
		client:            client,
		chatID:            chatID,
		messageThreadID:   messageThreadID,
		pending:           make(map[string]*telegramPending),
		resolved:          make(map[string]*telegramResolved),
		log:               logging.WithComponent("approval.telegram"),
		warnedInvalidDest: make(map[string]bool),
	}
}

// isValidTelegramChatID reports whether s looks like a destination the
// Telegram Bot API will accept: a numeric chat id (negative for groups/
// channels) or an "@username" public channel/group handle. Approvers is a
// channel-agnostic config list ("user IDs/handles who can approve") shared
// across whichever handler ends up serving a request — when the operator
// intends a different channel's approver identity (e.g. a Slack channel name)
// and it lands here anyway (see resolveDestChatID), sending to it verbatim
// produces a Telegram "chat not found" 400 on every retry instead of a single
// diagnosable warning (GH-4380).
func isValidTelegramChatID(s string) bool {
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "@") {
		return len(s) > 1
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// resolveDestChatID picks the Telegram destination for req: the first
// approver when it's a plausible Telegram chat id, otherwise the handler's
// own configured chatID. An invalid override is logged once (deduped by
// value) rather than retried against the Telegram API every tick (GH-4380).
func (h *TelegramHandler) resolveDestChatID(req *Request) string {
	if len(req.Approvers) == 0 {
		return h.chatID
	}
	if isValidTelegramChatID(req.Approvers[0]) {
		return req.Approvers[0]
	}
	h.warnInvalidDestOnce(req.ID, req.Approvers[0])
	return h.chatID
}

func (h *TelegramHandler) threadFor(dest string) int64 {
	if dest != h.chatID {
		return 0
	}
	return h.messageThreadID
}

// warnInvalidDestOnce logs the first time a given invalid Approvers[0] value
// is seen, then suppresses repeats of the same value (GH-4380).
func (h *TelegramHandler) warnInvalidDestOnce(requestID, badDest string) {
	h.mu.Lock()
	if h.warnedInvalidDest == nil {
		h.warnedInvalidDest = make(map[string]bool)
	}
	if h.warnedInvalidDest[badDest] {
		h.mu.Unlock()
		return
	}
	h.warnedInvalidDest[badDest] = true
	h.mu.Unlock()

	h.log.Warn("approval request Approvers[0] is not a valid Telegram destination, falling back to configured chat_id",
		slog.String("request_id", requestID), slog.String("invalid_approver", badDest))
}

// Name returns the handler name
func (h *TelegramHandler) Name() string {
	return "telegram"
}

// WithStore attaches a persistence store so pending approvals survive restarts.
// Returns h to allow builder-style chaining after NewTelegramHandler.
func (h *TelegramHandler) WithStore(store PendingApprovalStore) *TelegramHandler {
	h.store = store
	return h
}

// WithDecisionRecorder attaches a DecisionRecorder so HandleCallback persists
// decisions directly to the PRState/executions store rather than relying
// solely on a live goroutine reading pending.ResponseCh. This is what makes a
// button tap on a Rehydrate-restored request actually reach the pipeline —
// after a restart there is no waiter goroutine left to consume the channel.
// Returns h to allow builder-style chaining.
func (h *TelegramHandler) WithDecisionRecorder(recorder DecisionRecorder) *TelegramHandler {
	h.recorder = recorder
	return h
}

// WithAllowedUsers attaches a handler-scoped allowlist of user IDs permitted
// to decide approval requests whose own Request.Approvers is empty
// (GH-5155). Requests that do carry Approvers are gated by that list
// instead — this fallback only applies when Approvers is unset. Returns h
// to allow builder-style chaining.
func (h *TelegramHandler) WithAllowedUsers(users []string) *TelegramHandler {
	h.allowedUsers = users
	return h
}

// isAuthorizedApprover reports whether userID may decide a request via this
// handler (GH-5155). When approvers (the request's own Request.Approvers)
// is non-empty it is the authoritative allowlist — userID must exactly
// match one of its entries via plain string equality, since Approvers
// already carries whatever identity format the operator configured for
// this channel (e.g. Telegram numeric user ids). When approvers is empty,
// control falls back to the handler-scoped allowedUsers set (see
// WithAllowedUsers) so a channel can still restrict who may decide requests
// that didn't specify their own approver list. Both empty means
// unrestricted — any user may decide, preserving behavior for callers that
// never configured either.
func (h *TelegramHandler) isAuthorizedApprover(userID string, approvers []string) bool {
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
// into the in-memory map so that button taps that arrive after a restart are
// processed rather than answered with "expired". Expired rows are pruned.
// For each newly-rehydrated request it also re-sends a fresh approval prompt
// (same request_id/buttons) so the user has a live, tappable message —
// restart churn routinely expires the callback query on the original message
// before the user can tap it. Only requests inserted during THIS call are
// re-notified, so calling Rehydrate again (e.g. a second startup pass) does
// not re-send prompts for requests already tracked in h.pending. No-op when
// no store is attached.
func (h *TelegramHandler) Rehydrate(ctx context.Context) error {
	if h.store == nil {
		return nil
	}
	rows, err := h.store.LoadPendingApprovals()
	if err != nil {
		return fmt.Errorf("rehydrate: load pending approvals: %w", err)
	}
	now := time.Now()
	var newlyRehydrated []*telegramPending
	for _, row := range rows {
		// GH-4772: only rehydrate rows this handler actually owns — a row
		// originally dispatched to Slack (or any other channel) must not
		// get a duplicate Telegram prompt, and this handler must not delete
		// an expired row it doesn't own out from under its owning handler's
		// own sweep. See ownsChannel/DefaultChannelName.
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
		destChatID := h.resolveDestChatID(req)
		responseCh := make(chan *Response, 1)
		h.mu.Lock()
		if _, exists := h.pending[req.ID]; !exists {
			p := &telegramPending{
				Request:    req,
				ChatID:     destChatID,
				ResponseCh: responseCh,
			}
			h.pending[req.ID] = p
			newlyRehydrated = append(newlyRehydrated, p)
		}
		h.mu.Unlock()
	}
	if len(newlyRehydrated) > 0 {
		h.log.Info("rehydrated pending approvals", slog.Int("count", len(newlyRehydrated)))
		for _, p := range newlyRehydrated {
			h.resendRehydratedPrompt(ctx, p)
		}
	}
	return nil
}

// resendRehydratedPrompt sends a fresh, tappable approval prompt for a
// request restored by Rehydrate. The original message (sent by the
// pre-restart process) may have gone stale — its callback query can have
// expired during daemon-down time — so this sends a NEW message carrying the
// same request_id/buttons rather than editing the old one, and records the
// new message ID so later edits (HandleCallback, PruneExpired) target it.
// Best-effort: a send failure is logged and does not fail Rehydrate.
func (h *TelegramHandler) resendRehydratedPrompt(ctx context.Context, p *telegramPending) {
	text := h.formatRehydratedMessage(p.Request)
	keyboard := h.createApprovalKeyboard(p.Request)

	resp, err := h.client.SendMessageWithKeyboard(ctx, p.ChatID, text, "", keyboard, h.threadFor(p.ChatID))
	if err != nil {
		h.log.Warn("failed to resend rehydrated approval prompt",
			slog.String("request_id", p.Request.ID), slog.Any("error", err))
		return
	}
	if resp != nil && resp.Result != nil {
		h.mu.Lock()
		p.MessageID = resp.Result.MessageID
		h.mu.Unlock()
	}
}

// PruneExpired scans the in-memory pending set for requests whose ExpiresAt
// has passed, edits their Telegram message to show they expired, removes
// them from the pending map, and deletes their persisted row. It also sweeps
// the store directly via PrunePendingApproval for rows with no in-memory
// counterpart (e.g. left behind by a process that crashed before Rehydrate
// ran). Returns the number of in-memory requests pruned.
//
// A request rehydrated after a daemon restart has no waiter goroutine
// enforcing its own timeout — Manager's async dispatch loop only watches
// requests it created in the current process. Without this sweep, a
// rehydrated request that expires just sits in h.pending forever instead of
// resolving to "expired" (GH-3825).
func (h *TelegramHandler) PruneExpired(ctx context.Context) (int, error) {
	now := time.Now()

	h.mu.Lock()
	var expired []*telegramPending
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
		if p.MessageID != 0 {
			text := h.formatExpiredMessage(p.Request)
			if err := h.client.EditMessage(ctx, p.ChatID, p.MessageID, text, ""); err != nil {
				h.log.Warn("failed to edit expired message",
					slog.String("request_id", p.Request.ID), slog.Any("error", err))
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
		// (its own channel, plus legacy empty-channel rows when this is the
		// default claimant) so it never deletes/decides another channel's
		// expired row before that channel's own sweep runs.
		if _, err := h.store.PrunePendingApprovals(now, ownedChannels(h.Name())); err != nil {
			return len(expired), fmt.Errorf("prune expired: sweep store: %w", err)
		}
		// The default channel also sweeps orphaned rows — a preferred_channel
		// that matches no handler this package knows about (removed from
		// config, typo'd, etc.) — so such a row is never permanently
		// unprunable. A full Manager-level orphan sweep is roadmap leg B4;
		// this just keeps rows collectible until then.
		if h.Name() == DefaultChannelName {
			if _, err := h.store.PrunePendingApprovalsOutside(now, knownChannelNames); err != nil {
				return len(expired), fmt.Errorf("prune expired: sweep orphaned-channel rows: %w", err)
			}
		}
	}

	if len(expired) > 0 {
		h.log.Info("pruned expired pending approvals", slog.Int("count", len(expired)))
	}

	// Sweep stale resolved decisions (approved requests whose PR never
	// reached NotifyMerged within resolvedRetention) so the map stays bounded.
	h.mu.Lock()
	for id, r := range h.resolved {
		if now.Sub(r.DecidedAt) > resolvedRetention {
			delete(h.resolved, id)
		}
	}
	h.mu.Unlock()

	return len(expired), nil
}

// SendApprovalRequest sends an approval request via Telegram
func (h *TelegramHandler) SendApprovalRequest(ctx context.Context, req *Request) (<-chan *Response, error) {
	responseCh := make(chan *Response, 1)

	// Format message based on stage
	text := h.formatApprovalMessage(req)

	// Create inline keyboard with approve/reject buttons
	keyboard := h.createApprovalKeyboard(req)

	// Resolve destination: use first approver's chat_id when it's a valid
	// Telegram destination, otherwise fall back to the configured chat_id
	// (GH-4380).
	destChatID := h.resolveDestChatID(req)

	// Send message
	resp, err := h.client.SendMessageWithKeyboard(ctx, destChatID, text, "", keyboard, h.threadFor(destChatID))
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
		ChatID:     destChatID,
		ResponseCh: responseCh,
	}
	h.mu.Unlock()

	// Best-effort persistence so the request survives a restart.
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
		slog.String("chat_id", destChatID),
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

	if h.store != nil {
		if err := h.store.DeletePendingApproval(requestID); err != nil {
			h.log.Warn("failed to delete persisted approval on cancel", slog.String("request_id", requestID), slog.Any("error", err))
		}
	}

	// Update message to show cancelled
	if pending.MessageID != 0 {
		text := h.formatCancelledMessage(pending.Request)
		if err := h.client.EditMessage(ctx, pending.ChatID, pending.MessageID, text, ""); err != nil {
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
	// GH-5155: decide authorization inside the same critical section as the
	// lookup, and only delete from h.pending when authorized — an
	// unauthorized tap must not mutate any state (pending map, store,
	// resolved decision), just like the not-found/expired paths below.
	authorized := true
	if exists {
		authorized = h.isAuthorizedApprover(userID, pending.Request.Approvers)
		if authorized {
			delete(h.pending, requestID)
		}
	}
	h.mu.Unlock()

	if !exists {
		// No log line existed here previously — a tap landing after a daemon
		// restart but before Rehydrate repopulates h.pending (or against any
		// other state mismatch) was completely invisible to operators (GH-4159).
		h.log.Info("Approval callback for unknown or already-processed request",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username))
		if err := h.client.AnswerCallback(ctx, callbackID, "Request expired or already processed"); err != nil {
			h.log.Warn("failed to answer unknown-request approval callback",
				slog.String("request_id", requestID), slog.Any("error", err))
		}
		return true
	}

	if !authorized {
		h.log.Info("Approval callback from unauthorized user",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username),
			slog.String("user_id", userID))
		if err := h.client.AnswerCallback(ctx, callbackID, "You are not authorized to decide this request"); err != nil {
			h.log.Warn("failed to answer unauthorized approval callback",
				slog.String("request_id", requestID), slog.Any("error", err))
		}
		return true
	}

	if h.store != nil {
		if err := h.store.DeletePendingApproval(requestID); err != nil {
			h.log.Warn("failed to delete persisted approval on callback", slog.String("request_id", requestID), slog.Any("error", err))
		}
	}

	// A tap can race the periodic PruneExpired sweep: the request is still in
	// h.pending but its deadline has already passed. Treat it the same as the
	// not-found case instead of recording a real decision — otherwise the user
	// sees "Approved!"/"Rejected" for a request the system has already decided
	// to time out (GH-3825).
	if pending.Request.ExpiresAt.Before(time.Now()) {
		h.log.Info("Approval callback arrived after expiry",
			slog.String("request_id", requestID),
			slog.String("decision", string(decision)),
			slog.String("user", username))
		if err := h.client.AnswerCallback(ctx, callbackID, "Request expired or already processed"); err != nil {
			h.log.Warn("failed to answer expired approval callback",
				slog.String("request_id", requestID), slog.Any("error", err))
		}
		if pending.MessageID != 0 {
			text := h.formatExpiredMessage(pending.Request)
			if err := h.client.EditMessage(ctx, pending.ChatID, pending.MessageID, text, ""); err != nil {
				h.log.Warn("Failed to edit expired message", slog.Any("error", err))
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
	// channel but no goroutine to read it (GH-3825).
	//
	// GH-4777: a tap can lose the race to a concurrent HTTP POST (or the
	// other channel) — RecordDecision then returns memory.ErrApprovalAlreadyDecided.
	// The tapper must see "already decided", not a success card claiming
	// their tap won when it didn't (PR#4767 review).
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

	// Answer callback
	var answerText string
	switch {
	case raceLost:
		answerText = "Already decided"
	case decision == DecisionApproved:
		answerText = "Approved!"
	default:
		answerText = "Rejected"
	}
	if err := h.client.AnswerCallback(ctx, callbackID, answerText); err != nil {
		h.log.Warn("failed to answer approval callback",
			slog.String("request_id", requestID), slog.Any("error", err))
	}

	// Update message to show result — falls back to a brand-new message if
	// the edit fails (or there is no live message to edit), so a recorded
	// decision is never left with zero user feedback (GH-4164).
	text := h.formatResponseMessage(pending.Request, decision, username)
	if raceLost {
		text = h.formatAlreadyDecidedMessage(pending.Request)
	}
	h.deliverResponseCard(ctx, pending.ChatID, pending.MessageID, text)

	// Track the approved decision's chat/message so a later merge (autopilot
	// calling NotifyMerged) can post a follow-up in the same chat. Only
	// approved decisions that actually won the race ever reach a merge.
	if decision == DecisionApproved && !raceLost {
		h.mu.Lock()
		h.resolved[requestID] = &telegramResolved{
			Request:   pending.Request,
			ChatID:    pending.ChatID,
			MessageID: pending.MessageID,
			DecidedAt: time.Now(),
		}
		h.mu.Unlock()
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

// deliverResponseCard edits the original approval message in place to show
// the decision. If there is no live message to edit (messageID == 0) or the
// edit fails, it falls back to sending a brand-new message with identical
// content, so a recorded decision is never left with zero user feedback
// (GH-4164). Best-effort: a failure on the fallback send is logged, not
// returned — callers of HandleCallback must not fail on delivery issues.
func (h *TelegramHandler) deliverResponseCard(ctx context.Context, chatID string, messageID int64, text string) {
	if messageID != 0 {
		err := h.client.EditMessage(ctx, chatID, messageID, text, "")
		if err == nil {
			return
		}
		h.log.Warn("failed to edit response message, falling back to a new message",
			slog.String("chat_id", chatID), slog.Int64("message_id", messageID), slog.Any("error", err))
	}
	if _, err := h.client.SendMessageWithKeyboard(ctx, chatID, text, "", nil, h.threadFor(chatID)); err != nil {
		h.log.Warn("failed to send fallback response message",
			slog.String("chat_id", chatID), slog.Any("error", err))
	}
}

// NotifyMerged posts a short merge-completion follow-up in the same chat as
// the original approval decision, once the PR that decision gated has
// actually merged. Called by autopilot's merge-transition handler
// (stage -> merged) for PRs that went through human approval. A requestID
// with no matching resolved decision (never approved via Telegram, already
// notified, or past resolvedRetention) is a silent no-op — the caller
// doesn't need to know which channel (if any) gated the merge (GH-4164).
func (h *TelegramHandler) NotifyMerged(ctx context.Context, requestID, shortSHA string) error {
	h.mu.Lock()
	r, exists := h.resolved[requestID]
	if exists {
		delete(h.resolved, requestID)
	}
	h.mu.Unlock()

	if !exists {
		return nil
	}

	text := fmt.Sprintf("🔀 Merged %s", shortSHA)
	if _, err := h.client.SendMessageWithKeyboard(ctx, r.ChatID, text, "", nil, h.threadFor(r.ChatID)); err != nil {
		return fmt.Errorf("notify merged: %w", err)
	}
	return nil
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

// formatResponseMessage formats the message after a response. An approved
// decision carrying a ReleasePlan (set by autopilot at request-creation time
// — see Request.ReleasePlan) renders a release-aware next-step line instead
// of the generic "APPROVED" card, so the user sees what happens next without
// having to check the pipeline separately (GH-4164). Rejected/timeout
// decisions, and approvals with no release plan (e.g. non-merge-gating
// stages), fall through to the original generic card.
func (h *TelegramHandler) formatResponseMessage(req *Request, decision Decision, username string) string {
	if decision == DecisionApproved && req.ReleasePlan != "" {
		return fmt.Sprintf("✅ Approved by %s — merging. %s\n\nTask: %s\n%s",
			username, req.ReleasePlan, req.TaskID, req.Title)
	}

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

// formatRehydratedMessage formats the fresh prompt sent after Rehydrate
// restores a pending request post-restart.
func (h *TelegramHandler) formatRehydratedMessage(req *Request) string {
	return fmt.Sprintf("⏳ Still pending (rehydrated after restart)\n\n%s", h.formatApprovalMessage(req))
}

// formatCancelledMessage formats the message when request is cancelled
func (h *TelegramHandler) formatCancelledMessage(req *Request) string {
	return fmt.Sprintf("⏹ CANCELLED\n\nTask: %s\n%s\n\nApproval request was cancelled.", req.TaskID, req.Title)
}

// formatAlreadyDecidedMessage formats the message shown to a tapper who lost
// the decision race (GH-4777) — another decider (a concurrent HTTP POST, or
// the same request answered through a different channel) already recorded
// the outcome first, so this tap must not claim success.
func (h *TelegramHandler) formatAlreadyDecidedMessage(req *Request) string {
	return fmt.Sprintf("⚠️ ALREADY DECIDED\n\nTask: %s\n%s\n\nSomeone else already recorded a decision for this request.", req.TaskID, req.Title)
}

// formatExpiredMessage formats the message when a request expires unanswered.
func (h *TelegramHandler) formatExpiredMessage(req *Request) string {
	return fmt.Sprintf("⏱ EXPIRED\n\nTask: %s\n%s\n\nApproval request expired — no action taken.", req.TaskID, req.Title)
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
