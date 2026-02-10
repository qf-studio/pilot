package slack

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alekspetrov/pilot/internal/logging"
	"github.com/gorilla/websocket"
)

// SocketEventType identifies the kind of event received over Socket Mode.
type SocketEventType string

const (
	SocketEventMessage     SocketEventType = "events_api"
	SocketEventInteraction SocketEventType = "interactive"
	SocketEventSlashCmd    SocketEventType = "slash_commands"
	SocketEventDisconnect  SocketEventType = "disconnect"
)

// RawSocketEvent is emitted on the events channel for each envelope received
// from the Slack Socket Mode WebSocket connection. It carries the raw payload
// before higher-level parsing into a SocketEvent (see events.go).
type RawSocketEvent struct {
	Type       SocketEventType
	EnvelopeID string
	Payload    json.RawMessage // raw inner payload for caller to decode
}

// Envelope is the wire format Slack sends over Socket Mode.
type Envelope struct {
	EnvelopeID string          `json:"envelope_id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	// disconnect-specific fields
	Reason string `json:"reason,omitempty"`
}

// envelopeAck is written back to acknowledge receipt.
type envelopeAck struct {
	EnvelopeID string `json:"envelope_id"`
}

// SocketModeHandler manages a Slack Socket Mode WebSocket connection.
// It reads envelopes, acknowledges them, and emits RawSocketEvents on a channel.
type SocketModeHandler struct {
	conn   *websocket.Conn
	events chan RawSocketEvent
	done   chan struct{}
	once   sync.Once
	log    *slog.Logger

	// PongWait is how long we wait for a pong before considering the
	// connection dead. PingInterval is how often we send pings.
	PongWait     time.Duration
	PingInterval time.Duration
}

// NewSocketModeHandler wraps an established WebSocket connection and returns
// the handler plus the channel that will receive parsed events.
// Call Run() to start the read loop.
func NewSocketModeHandler(conn *websocket.Conn) (*SocketModeHandler, <-chan RawSocketEvent) {
	ch := make(chan RawSocketEvent, 64)
	h := &SocketModeHandler{
		conn:         conn,
		events:       ch,
		done:         make(chan struct{}),
		log:          logging.WithComponent("slack.socket_mode"),
		PongWait:     60 * time.Second,
		PingInterval: 30 * time.Second,
	}
	return h, ch
}

// Run starts the read loop and ping ticker. It blocks until the connection
// is closed or Close is called. The events channel is closed on return.
func (h *SocketModeHandler) Run() {
	defer h.cleanup()

	h.wirePongHandler()

	// Set initial read deadline based on PongWait.
	_ = h.conn.SetReadDeadline(time.Now().Add(h.PongWait))

	// Start ping ticker in a separate goroutine.
	go h.pingLoop()

	h.readLoop()
}

// Close terminates the handler gracefully.
func (h *SocketModeHandler) Close() {
	h.once.Do(func() {
		close(h.done)
		_ = h.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		_ = h.conn.Close()
	})
}

// --- internals ---

func (h *SocketModeHandler) cleanup() {
	close(h.events)
	h.Close()
}

func (h *SocketModeHandler) wirePongHandler() {
	h.conn.SetPongHandler(func(appData string) error {
		h.log.Debug("pong received")
		return h.conn.SetReadDeadline(time.Now().Add(h.PongWait))
	})
}

func (h *SocketModeHandler) pingLoop() {
	ticker := time.NewTicker(h.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			if err := h.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				h.log.Warn("ping write failed", slog.Any("error", err))
				return
			}
		}
	}
}

func (h *SocketModeHandler) readLoop() {
	for {
		select {
		case <-h.done:
			return
		default:
		}

		_, data, err := h.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseNormalClosure,
				websocket.CloseGoingAway) {
				h.log.Warn("websocket read error", slog.Any("error", err))
			}
			return
		}

		h.handleRawMessage(data)
	}
}

func (h *SocketModeHandler) handleRawMessage(data []byte) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		h.log.Error("failed to parse envelope", slog.Any("error", err))
		return
	}

	if env.EnvelopeID == "" {
		h.log.Warn("envelope missing envelope_id, skipping")
		return
	}

	// Acknowledge immediately — Slack requires this within 3 seconds.
	if err := h.acknowledge(env.EnvelopeID); err != nil {
		h.log.Error("failed to acknowledge envelope",
			slog.String("envelope_id", env.EnvelopeID),
			slog.Any("error", err))
		// Continue processing even if ack fails; Slack will redeliver.
	}

	h.log.Debug("envelope received",
		slog.String("type", env.Type),
		slog.String("envelope_id", env.EnvelopeID))

	// Handle disconnect: emit event and close.
	if env.Type == "disconnect" {
		reason := env.Reason
		if reason == "" {
			reason = "server requested disconnect"
		}
		h.log.Info("disconnect envelope received", slog.String("reason", reason))

		h.emit(RawSocketEvent{
			Type:       SocketEventDisconnect,
			EnvelopeID: env.EnvelopeID,
			Payload:    data, // full envelope so caller can inspect reason
		})
		h.Close()
		return
	}

	evtType, ok := mapEnvelopeType(env.Type)
	if !ok {
		h.log.Warn("unknown envelope type, skipping",
			slog.String("type", env.Type))
		return
	}

	h.emit(RawSocketEvent{
		Type:       evtType,
		EnvelopeID: env.EnvelopeID,
		Payload:    env.Payload,
	})
}

func (h *SocketModeHandler) acknowledge(envelopeID string) error {
	ack := envelopeAck{EnvelopeID: envelopeID}
	data, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("marshal ack: %w", err)
	}
	return h.conn.WriteMessage(websocket.TextMessage, data)
}

func (h *SocketModeHandler) emit(evt RawSocketEvent) {
	select {
	case h.events <- evt:
	default:
		h.log.Warn("event channel full, dropping event",
			slog.String("type", string(evt.Type)),
			slog.String("envelope_id", evt.EnvelopeID))
	}
}

func mapEnvelopeType(raw string) (SocketEventType, bool) {
	switch raw {
	case "events_api":
		return SocketEventMessage, true
	case "interactive":
		return SocketEventInteraction, true
	case "slash_commands":
		return SocketEventSlashCmd, true
	case "disconnect":
		return SocketEventDisconnect, true
	default:
		return "", false
	}
}
