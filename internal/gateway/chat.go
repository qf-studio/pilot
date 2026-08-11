package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/qf-studio/pilot/internal/adapters/web"
	"github.com/qf-studio/pilot/internal/logging"
)

// ChatAPI is the seam handleChatMessages/handleChatEvents dispatch through —
// satisfied by *web.API. Defined here (consumer side) rather than in
// internal/adapters/web so gateway keeps owning its own handler-testing
// interfaces, matching the DashboardStore/AutopilotProvider/DecisionRecorder
// convention already used in this package.
type ChatAPI interface {
	Dispatch(ctx context.Context, req web.DispatchRequest) (seq int64, err error)
	Events(conversationID string, after int64) (events []web.Event, latestSeq int64)
}

// SetChatAPI wires the web chat transport (GH-4835 / C17) backing
// POST /api/v1/chat/messages and GET /api/v1/chat/conversations/{id}/events.
// Must be called before Start(), at BOTH gateway construction sites —
// cmd/pilot/main.go (polling mode) and internal/pilot/pilot.go (gateway
// mode) — when adapters.chat.enabled is true, mirroring SetDecisionRecorder
// (see approvals.go). Left nil (the default, matching adapters.chat.enabled:
// false), the routes are not registered at all in Start() — a request to
// either path 404s like any unknown path, not 503, so "disabled" and
// "misconfigured" are told apart at the routing layer rather than inside
// the handler.
func (s *Server) SetChatAPI(api ChatAPI) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatAPI = api
}

// chatMessageRequest is the POST /api/v1/chat/messages body. Either Text is
// set (a plain message) or IsCallback+CallbackID+ActionID are set (a button
// press) — never both, matching comms.IncomingMessage's own shape. Sender is
// an optional display label only; the chat API has no per-user RBAC beyond
// the gateway's own bearer auth (GH-4835 scope fence).
type chatMessageRequest struct {
	ConversationID string `json:"conversationId"`
	Text           string `json:"text,omitempty"`
	IsCallback     bool   `json:"isCallback,omitempty"`
	CallbackID     string `json:"callbackId,omitempty"`
	ActionID       string `json:"actionId,omitempty"`
	Sender         string `json:"sender,omitempty"`
}

// chatMessageResponse is the 202 body: Seq is the conversation's newest seq
// at accept time (before this message's own events are appended), telling
// the caller where to start polling from to observe this message's effects.
type chatMessageResponse struct {
	ConversationID string `json:"conversationId"`
	Seq            int64  `json:"seq"`
}

// chatEventsResponse is the GET .../events body. LatestSeq is the newest seq
// currently known for the conversation (0 if it has no buffer yet) — the
// reset-detection signal documented on web.API.Events (GH-4843 D2): a client
// whose own `after` cursor exceeds LatestSeq has outlived the server's
// in-memory buffer (daemon restart, or 1h conversation expiry) and must
// re-poll from after=0. A gap between the returned Events' lowest seq and
// `after` (visible against LatestSeq) signals the 500-event drop-oldest cap
// truncated history instead.
type chatEventsResponse struct {
	ConversationID string      `json:"conversationId"`
	Events         []web.Event `json:"events"`
	LatestSeq      int64       `json:"latestSeq"`
}

// handleChatMessages serves POST /api/v1/chat/messages (GH-4835 acceptance
// #1-#3). Dispatches comms.Handler.HandleMessage on the daemon context (see
// Server.Start, which stashes it in s.daemonCtx) rather than the request
// context, since a task-shaped message can run for minutes while this
// handler must return in milliseconds — see web.API.Dispatch's doc comment.
func (s *Server) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	api := s.chatAPI
	dispatchCtx := s.daemonCtx
	s.mu.RUnlock()

	if api == nil {
		http.Error(w, "chat API not configured", http.StatusServiceUnavailable)
		return
	}
	if dispatchCtx == nil {
		// Start() hasn't run yet (or hasn't reached the point where it
		// stashes the daemon context). Should not happen once the server is
		// actually serving requests, but fail loudly rather than dispatch
		// on a nil context.
		http.Error(w, "gateway not ready", http.StatusServiceUnavailable)
		return
	}

	var body chatMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	seq, err := api.Dispatch(dispatchCtx, web.DispatchRequest{
		ConversationID: body.ConversationID,
		Text:           body.Text,
		IsCallback:     body.IsCallback,
		CallbackID:     body.CallbackID,
		ActionID:       body.ActionID,
		Sender:         body.Sender,
	})
	if err != nil {
		switch {
		case errors.Is(err, web.ErrMissingConversationID),
			errors.Is(err, web.ErrMissingText),
			errors.Is(err, web.ErrMissingCallbackFields),
			errors.Is(err, web.ErrApprovalCallback):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, "failed to dispatch message", http.StatusInternalServerError)
		}
		return
	}

	writeJSONStatus(w, http.StatusAccepted, chatMessageResponse{
		ConversationID: body.ConversationID,
		Seq:            seq,
	})
}

// handleChatEvents serves GET /api/v1/chat/conversations/{id}/events?after=<seq>
// (GH-4835 acceptance #1-#3). A conversation with no events yet (never
// messaged, or its buffer expired/was evicted — see web.WebMessenger) is not
// an error: it returns an empty events array, since a poll-drain client
// naturally polls before the first message it sent has produced any events.
func (s *Server) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.RLock()
	api := s.chatAPI
	s.mu.RUnlock()

	if api == nil {
		http.Error(w, "chat API not configured", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing conversation id", http.StatusBadRequest)
		return
	}

	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid after parameter", http.StatusBadRequest)
			return
		}
		after = v
	}

	events, latestSeq := api.Events(id, after)
	if events == nil {
		events = []web.Event{}
	}
	writeJSON(w, chatEventsResponse{
		ConversationID: id,
		Events:         events,
		LatestSeq:      latestSeq,
	})
}

// writeJSONStatus is writeJSON with an explicit non-200 status code.
func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logging.WithComponent("gateway").Warn("failed to encode chat response", slog.Any("error", err))
	}
}
