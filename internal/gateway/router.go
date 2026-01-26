package gateway

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/alekspetrov/pilot/internal/logging"
)

// MessageType defines the type of control plane message
type MessageType string

const (
	MessageTypeTask     MessageType = "task"
	MessageTypeStatus   MessageType = "status"
	MessageTypeProgress MessageType = "progress"
	MessageTypePing     MessageType = "ping"
	MessageTypePong     MessageType = "pong"
)

// Message represents a control plane message
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// WebhookHandler handles incoming webhooks
type WebhookHandler func(payload map[string]interface{})

// Router routes messages and webhooks to appropriate handlers
type Router struct {
	messageHandlers map[MessageType][]func(*Session, json.RawMessage)
	webhookHandlers map[string][]WebhookHandler
	mu              sync.RWMutex
}

// NewRouter creates a new router
func NewRouter() *Router {
	r := &Router{
		messageHandlers: make(map[MessageType][]func(*Session, json.RawMessage)),
		webhookHandlers: make(map[string][]WebhookHandler),
	}

	// Register default handlers
	r.RegisterMessageHandler(MessageTypePing, r.handlePing)

	return r
}

// RegisterMessageHandler registers a handler for a message type
func (r *Router) RegisterMessageHandler(msgType MessageType, handler func(*Session, json.RawMessage)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messageHandlers[msgType] = append(r.messageHandlers[msgType], handler)
}

// RegisterWebhookHandler registers a handler for webhooks from a source
func (r *Router) RegisterWebhookHandler(source string, handler WebhookHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.webhookHandlers[source] = append(r.webhookHandlers[source], handler)
}

// HandleMessage routes a message to registered handlers
func (r *Router) HandleMessage(session *Session, data []byte) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		logging.WithComponent("router").Warn("Failed to parse message", slog.Any("error", err))
		return
	}

	r.mu.RLock()
	handlers, ok := r.messageHandlers[msg.Type]
	r.mu.RUnlock()

	if !ok {
		logging.WithComponent("router").Debug("No handler for message type", slog.String("type", string(msg.Type)))
		return
	}

	for _, handler := range handlers {
		handler(session, msg.Payload)
	}
}

// HandleWebhook routes a webhook to registered handlers
func (r *Router) HandleWebhook(source string, payload map[string]interface{}) {
	r.mu.RLock()
	handlers, ok := r.webhookHandlers[source]
	r.mu.RUnlock()

	if !ok {
		logging.WithComponent("router").Debug("No handler for webhook source", slog.String("source", source))
		return
	}

	for _, handler := range handlers {
		handler(payload)
	}
}

// handlePing responds to ping messages
func (r *Router) handlePing(session *Session, payload json.RawMessage) {
	session.UpdatePing()
	response, _ := json.Marshal(Message{
		Type:    MessageTypePong,
		Payload: payload,
	})
	_ = session.Send(response)
}
