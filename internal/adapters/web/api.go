package web

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
)

// contextIDPrefix namespaces web conversations within comms.Handler's
// per-ContextID state (rate limits, pending/running task slots), matching
// the platform-prefix convention comms uses to keep contexts from different
// adapters from colliding.
const contextIDPrefix = "web:"

// Errors returned by API.Dispatch. The gateway HTTP handler maps these to
// 400 Bad Request; anything else is a 500.
var (
	ErrMissingConversationID = errors.New("missing conversationId")
	ErrMissingText           = errors.New("missing text")
	ErrMissingCallbackFields = errors.New("callback requires callbackId and actionId")
	// ErrApprovalCallback is returned when a request looks like an approval
	// decision routed through the chat API. Approvals do not go through the
	// comms brain (pitfall GH-4411/GH-4431) — the console must call POST
	// /api/v1/approvals/{requestId}/decision (#4748) directly.
	ErrApprovalCallback = errors.New("approval decisions are not accepted on the chat API; use POST /api/v1/approvals/{requestId}/decision")
)

// DispatchRequest is the validated input to API.Dispatch, adapted from the
// POST /api/v1/chat/messages body by the gateway handler.
type DispatchRequest struct {
	ConversationID string
	Text           string
	IsCallback     bool
	CallbackID     string
	ActionID       string
	Sender         string
}

// isApprovalCallback reports whether a callback payload matches the
// approve/reject shape used by internal/approval's Telegram/Slack button
// handlers (ActionID "approve"/"reject", CallbackID/value "approve:<id>" or
// "reject:<id>" — see internal/approval/telegram.go:463-470,
// internal/approval/slack.go:400-407). The chat API must never let one of
// these through to comms.Handler, which has no approval semantics at all.
func isApprovalCallback(actionID, callbackID string) bool {
	if actionID == "approve" || actionID == "reject" {
		return true
	}
	return strings.HasPrefix(callbackID, "approve:") || strings.HasPrefix(callbackID, "reject:")
}

// API implements the chat transport's business logic behind the gateway's
// HTTP handlers (internal/gateway/chat.go). It owns one comms.Handler (built
// via comms.BuildHandler with a WebMessenger) and that same WebMessenger,
// so it can both dispatch inbound messages and read back the outbound event
// buffer for the events poll endpoint.
type API struct {
	handler   *comms.Handler
	messenger *WebMessenger
}

// NewAPI wraps a comms.Handler and the WebMessenger it was built with. The
// caller (cmd/pilot/main.go, internal/pilot/pilot.go) is responsible for
// constructing both via comms.BuildHandler(comms.HandlerDeps{Messenger:
// aWebMessenger, ...}) and passing the same messenger instance here.
func NewAPI(handler *comms.Handler, messenger *WebMessenger) *API {
	return &API{handler: handler, messenger: messenger}
}

// Dispatch validates req, then runs comms.Handler.HandleMessage on a
// goroutine using dispatchCtx — the long-lived daemon context, NOT the HTTP
// request context, since HandleMessage can block for minutes (a full task
// execution) while the HTTP request must return in milliseconds. Cancelling
// the original HTTP request (client disconnect, request-scoped timeout) has
// no effect on the dispatched work.
//
// Returns the conversation's accept-time seq (the newest seq already in the
// buffer before this message's own events are appended) so the caller can
// tell a client where to start polling from to see this message's effects.
func (a *API) Dispatch(dispatchCtx context.Context, req DispatchRequest) (seq int64, err error) {
	if strings.TrimSpace(req.ConversationID) == "" {
		return 0, ErrMissingConversationID
	}
	if req.IsCallback {
		if strings.TrimSpace(req.CallbackID) == "" || strings.TrimSpace(req.ActionID) == "" {
			return 0, ErrMissingCallbackFields
		}
		if isApprovalCallback(req.ActionID, req.CallbackID) {
			return 0, ErrApprovalCallback
		}
	} else if strings.TrimSpace(req.Text) == "" {
		return 0, ErrMissingText
	}

	contextID := contextIDPrefix + req.ConversationID
	msg := &comms.IncomingMessage{
		ContextID:  contextID,
		SenderName: req.Sender,
		Text:       req.Text,
		Platform:   "web",
		Timestamp:  time.Now(),
		IsCallback: req.IsCallback,
		CallbackID: req.CallbackID,
		ActionID:   req.ActionID,
	}

	seq = a.messenger.LatestSeq(contextID)

	go a.handler.HandleMessage(dispatchCtx, msg)

	return seq, nil
}

// Events returns events for conversationID with seq > after, in seq order.
// A conversationID with no buffer (never messaged, or expired/evicted) is
// not an error — it returns an empty slice, matching "no events yet" rather
// than "unknown conversation" (the chat API has no separate concept of
// conversation existence beyond "has it produced any events").
func (a *API) Events(conversationID string, after int64) []Event {
	contextID := contextIDPrefix + conversationID
	events, _ := a.messenger.Events(contextID, after)
	return events
}
