package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNewRouter(t *testing.T) {
	router := NewRouter()

	if router == nil {
		t.Fatal("NewRouter returned nil")
	}
	if router.messageHandlers == nil {
		t.Error("messageHandlers not initialized")
	}
	if router.webhookHandlers == nil {
		t.Error("webhookHandlers not initialized")
	}

	// Verify ping handler is registered by default
	router.mu.RLock()
	_, hasPingHandler := router.messageHandlers[MessageTypePing]
	router.mu.RUnlock()

	if !hasPingHandler {
		t.Error("Ping handler should be registered by default")
	}
}

func TestRouterRegisterMessageHandler(t *testing.T) {
	router := NewRouter()

	handler := func(session *Session, payload json.RawMessage) {
		// Handler logic
	}

	router.RegisterMessageHandler(MessageTypeTask, handler)

	router.mu.RLock()
	handlers, ok := router.messageHandlers[MessageTypeTask]
	router.mu.RUnlock()

	if !ok {
		t.Error("Handler not registered")
	}
	if len(handlers) != 1 {
		t.Errorf("Expected 1 handler, got %d", len(handlers))
	}
}

func TestRouterRegisterMultipleMessageHandlers(t *testing.T) {
	router := NewRouter()

	var callOrder []int
	var mu sync.Mutex

	handler1 := func(session *Session, payload json.RawMessage) {
		mu.Lock()
		callOrder = append(callOrder, 1)
		mu.Unlock()
	}
	handler2 := func(session *Session, payload json.RawMessage) {
		mu.Lock()
		callOrder = append(callOrder, 2)
		mu.Unlock()
	}

	router.RegisterMessageHandler(MessageTypeStatus, handler1)
	router.RegisterMessageHandler(MessageTypeStatus, handler2)

	router.mu.RLock()
	handlers := router.messageHandlers[MessageTypeStatus]
	router.mu.RUnlock()

	if len(handlers) != 2 {
		t.Errorf("Expected 2 handlers, got %d", len(handlers))
	}

	// Call handlers and verify order
	for _, h := range handlers {
		h(nil, nil)
	}

	if len(callOrder) != 2 {
		t.Errorf("Expected 2 calls, got %d", len(callOrder))
	}
	if callOrder[0] != 1 || callOrder[1] != 2 {
		t.Errorf("Handlers called in wrong order: %v", callOrder)
	}
}

func TestRouterRegisterWebhookHandler(t *testing.T) {
	router := NewRouter()

	handlerCalled := false
	handler := func(payload map[string]interface{}) {
		handlerCalled = true
	}

	router.RegisterWebhookHandler("linear", handler)

	router.mu.RLock()
	handlers, ok := router.webhookHandlers["linear"]
	router.mu.RUnlock()

	if !ok {
		t.Error("Webhook handler not registered")
	}
	if len(handlers) != 1 {
		t.Errorf("Expected 1 handler, got %d", len(handlers))
	}

	// Call handler
	handlers[0](nil)
	if !handlerCalled {
		t.Error("Handler was not called")
	}
}

func TestRouterRegisterMultipleWebhookHandlers(t *testing.T) {
	router := NewRouter()

	var callCount int32

	handler1 := func(payload map[string]interface{}) {
		atomic.AddInt32(&callCount, 1)
	}
	handler2 := func(payload map[string]interface{}) {
		atomic.AddInt32(&callCount, 1)
	}

	router.RegisterWebhookHandler("github", handler1)
	router.RegisterWebhookHandler("github", handler2)

	router.mu.RLock()
	handlers := router.webhookHandlers["github"]
	router.mu.RUnlock()

	if len(handlers) != 2 {
		t.Errorf("Expected 2 handlers, got %d", len(handlers))
	}
}

func TestRouterHandleMessage(t *testing.T) {
	tests := []struct {
		name            string
		messageType     MessageType
		payload         string
		registerHandler bool
		expectCall      bool
	}{
		{
			name:            "valid message with handler",
			messageType:     MessageTypeTask,
			payload:         `{"id":"123"}`,
			registerHandler: true,
			expectCall:      true,
		},
		{
			name:            "valid message without handler",
			messageType:     MessageTypeProgress,
			payload:         `{"progress":50}`,
			registerHandler: false,
			expectCall:      false,
		},
		{
			name:            "status message with handler",
			messageType:     MessageTypeStatus,
			payload:         `{"running":true}`,
			registerHandler: true,
			expectCall:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter()
			handlerCalled := false
			var receivedPayload json.RawMessage

			if tt.registerHandler {
				router.RegisterMessageHandler(tt.messageType, func(session *Session, payload json.RawMessage) {
					handlerCalled = true
					receivedPayload = payload
				})
			}

			msg := Message{
				Type:    tt.messageType,
				Payload: json.RawMessage(tt.payload),
			}
			data, _ := json.Marshal(msg)

			router.HandleMessage(nil, data)

			if handlerCalled != tt.expectCall {
				t.Errorf("Handler called = %v, want %v", handlerCalled, tt.expectCall)
			}

			if tt.expectCall && string(receivedPayload) != tt.payload {
				t.Errorf("Payload = %s, want %s", receivedPayload, tt.payload)
			}
		})
	}
}

func TestRouterHandleMessageInvalidJSON(t *testing.T) {
	router := NewRouter()

	handlerCalled := false
	router.RegisterMessageHandler(MessageTypeTask, func(session *Session, payload json.RawMessage) {
		handlerCalled = true
	})

	// Send invalid JSON
	router.HandleMessage(nil, []byte("not valid json"))

	if handlerCalled {
		t.Error("Handler should not be called for invalid JSON")
	}
}

func TestRouterHandleWebhook(t *testing.T) {
	tests := []struct {
		name            string
		source          string
		payload         map[string]interface{}
		registerHandler bool
		expectCall      bool
	}{
		{
			name:            "linear webhook with handler",
			source:          "linear",
			payload:         map[string]interface{}{"action": "create", "type": "Issue"},
			registerHandler: true,
			expectCall:      true,
		},
		{
			name:            "github webhook without handler",
			source:          "github",
			payload:         map[string]interface{}{"action": "opened"},
			registerHandler: false,
			expectCall:      false,
		},
		{
			name:            "jira webhook with handler",
			source:          "jira",
			payload:         map[string]interface{}{"webhookEvent": "jira:issue_created"},
			registerHandler: true,
			expectCall:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter()
			handlerCalled := false
			var receivedPayload map[string]interface{}

			if tt.registerHandler {
				router.RegisterWebhookHandler(tt.source, func(payload map[string]interface{}) {
					handlerCalled = true
					receivedPayload = payload
				})
			}

			router.HandleWebhook(tt.source, tt.payload)

			if handlerCalled != tt.expectCall {
				t.Errorf("Handler called = %v, want %v", handlerCalled, tt.expectCall)
			}

			if tt.expectCall {
				for k, v := range tt.payload {
					if receivedPayload[k] != v {
						t.Errorf("Payload[%s] = %v, want %v", k, receivedPayload[k], v)
					}
				}
			}
		})
	}
}

func TestRouterHandlePing(t *testing.T) {
	router := NewRouter()

	// Create a test WebSocket server
	pongReceived := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read the pong response
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		pongReceived <- msg
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	sm := NewSessionManager()
	session := sm.Create(conn)
	originalPing := session.LastPing

	// Wait a bit to ensure time difference
	time.Sleep(10 * time.Millisecond)

	// Create ping message
	pingPayload := json.RawMessage(`{"timestamp":123456}`)
	pingMsg := Message{
		Type:    MessageTypePing,
		Payload: pingPayload,
	}
	data, _ := json.Marshal(pingMsg)

	// Handle ping
	router.HandleMessage(session, data)

	// Verify session LastPing was updated
	if !session.LastPing.After(originalPing) {
		t.Error("Session LastPing should be updated after ping")
	}

	// Verify pong was sent
	select {
	case pongData := <-pongReceived:
		var pongMsg Message
		if err := json.Unmarshal(pongData, &pongMsg); err != nil {
			t.Fatalf("Failed to unmarshal pong: %v", err)
		}
		if pongMsg.Type != MessageTypePong {
			t.Errorf("Expected pong type, got %s", pongMsg.Type)
		}
		if string(pongMsg.Payload) != string(pingPayload) {
			t.Errorf("Pong payload = %s, want %s", pongMsg.Payload, pingPayload)
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for pong message")
	}
}

func TestRouterConcurrentAccess(t *testing.T) {
	router := NewRouter()

	var callCount int32
	numGoroutines := 50
	var wg sync.WaitGroup

	// Register handlers concurrently
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			router.RegisterMessageHandler(MessageTypeTask, func(session *Session, payload json.RawMessage) {
				atomic.AddInt32(&callCount, 1)
			})
			router.RegisterWebhookHandler("linear", func(payload map[string]interface{}) {
				atomic.AddInt32(&callCount, 1)
			})
		}(i)
	}
	wg.Wait()

	// Verify handlers were registered
	router.mu.RLock()
	msgHandlers := len(router.messageHandlers[MessageTypeTask])
	webhookHandlers := len(router.webhookHandlers["linear"])
	router.mu.RUnlock()

	if msgHandlers != numGoroutines {
		t.Errorf("Expected %d message handlers, got %d", numGoroutines, msgHandlers)
	}
	if webhookHandlers != numGoroutines {
		t.Errorf("Expected %d webhook handlers, got %d", numGoroutines, webhookHandlers)
	}

	// Handle messages concurrently
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			msg := Message{Type: MessageTypeTask, Payload: json.RawMessage(`{}`)}
			data, _ := json.Marshal(msg)
			router.HandleMessage(nil, data)

			router.HandleWebhook("linear", map[string]interface{}{})
		}()
	}
	wg.Wait()

	// Each goroutine calls all handlers (numGoroutines handlers each)
	expectedCalls := int32(numGoroutines * numGoroutines * 2)
	if callCount != expectedCalls {
		t.Errorf("Expected %d calls, got %d", expectedCalls, callCount)
	}
}

func TestMessageTypes(t *testing.T) {
	tests := []struct {
		name     string
		msgType  MessageType
		expected string
	}{
		{"task type", MessageTypeTask, "task"},
		{"status type", MessageTypeStatus, "status"},
		{"progress type", MessageTypeProgress, "progress"},
		{"ping type", MessageTypePing, "ping"},
		{"pong type", MessageTypePong, "pong"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.msgType) != tt.expected {
				t.Errorf("MessageType = %s, want %s", tt.msgType, tt.expected)
			}
		})
	}
}

func TestMessageSerialization(t *testing.T) {
	tests := []struct {
		name    string
		message Message
	}{
		{
			name: "task message",
			message: Message{
				Type:    MessageTypeTask,
				Payload: json.RawMessage(`{"id":"task-123","title":"Test Task"}`),
			},
		},
		{
			name: "status message",
			message: Message{
				Type:    MessageTypeStatus,
				Payload: json.RawMessage(`{"running":true,"sessions":5}`),
			},
		},
		{
			name: "progress message",
			message: Message{
				Type:    MessageTypeProgress,
				Payload: json.RawMessage(`{"percent":75,"message":"Processing..."}`),
			},
		},
		{
			name: "ping message with timestamp",
			message: Message{
				Type:    MessageTypePing,
				Payload: json.RawMessage(`{"timestamp":1234567890}`),
			},
		},
		{
			name: "message with empty payload",
			message: Message{
				Type:    MessageTypePong,
				Payload: json.RawMessage(`{}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Serialize
			data, err := json.Marshal(tt.message)
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}

			// Deserialize
			var decoded Message
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}

			if decoded.Type != tt.message.Type {
				t.Errorf("Type = %s, want %s", decoded.Type, tt.message.Type)
			}
			if string(decoded.Payload) != string(tt.message.Payload) {
				t.Errorf("Payload = %s, want %s", decoded.Payload, tt.message.Payload)
			}
		})
	}
}

func TestRouterWebhookHandlerModifiesPayload(t *testing.T) {
	router := NewRouter()

	// Handler that modifies the payload
	router.RegisterWebhookHandler("test", func(payload map[string]interface{}) {
		payload["modified"] = true
	})

	payload := map[string]interface{}{"original": "value"}
	router.HandleWebhook("test", payload)

	// Verify payload was modified
	if payload["modified"] != true {
		t.Error("Handler should be able to modify payload")
	}
}

func TestRouterNoHandlerForSource(t *testing.T) {
	router := NewRouter()

	// Register handler for one source
	router.RegisterWebhookHandler("linear", func(payload map[string]interface{}) {})

	// Call webhook for different source - should not panic
	router.HandleWebhook("github", map[string]interface{}{"action": "opened"})
	router.HandleWebhook("jira", map[string]interface{}{"event": "created"})
	router.HandleWebhook("unknown", nil)
}
