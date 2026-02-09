package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// newTestWSPair creates a connected client/server websocket pair for testing.
func newTestWSPair(t *testing.T) (client *websocket.Conn, server *websocket.Conn) {
	t.Helper()

	upgrader := websocket.Upgrader{}
	var serverConn *websocket.Conn
	var wg sync.WaitGroup
	wg.Add(1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		serverConn, err = upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		wg.Done()
	}))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })

	wg.Wait()
	t.Cleanup(func() { _ = serverConn.Close() })

	return clientConn, serverConn
}

func TestSocketModeHandler_EventsAPI(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	// Server sends an events_api envelope.
	env := Envelope{
		EnvelopeID: "evt-123",
		Type:       "events_api",
		Payload:    json.RawMessage(`{"event":{"type":"message","text":"hello"}}`),
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read the ack that the handler writes back.
	_, ackData, err := server.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var ack envelopeAck
	if err := json.Unmarshal(ackData, &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if ack.EnvelopeID != "evt-123" {
		t.Errorf("ack envelope_id = %q, want %q", ack.EnvelopeID, "evt-123")
	}

	// Read the emitted event.
	select {
	case evt := <-events:
		if evt.Type != SocketEventMessage {
			t.Errorf("event type = %q, want %q", evt.Type, SocketEventMessage)
		}
		if evt.EnvelopeID != "evt-123" {
			t.Errorf("event envelope_id = %q, want %q", evt.EnvelopeID, "evt-123")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSocketModeHandler_InteractiveEnvelope(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	env := Envelope{
		EnvelopeID: "int-456",
		Type:       "interactive",
		Payload:    json.RawMessage(`{"type":"block_actions"}`),
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Consume ack.
	_, _, _ = server.ReadMessage()

	select {
	case evt := <-events:
		if evt.Type != SocketEventInteraction {
			t.Errorf("event type = %q, want %q", evt.Type, SocketEventInteraction)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSocketModeHandler_SlashCommand(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	env := Envelope{
		EnvelopeID: "cmd-789",
		Type:       "slash_commands",
		Payload:    json.RawMessage(`{"command":"/pilot"}`),
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, _ = server.ReadMessage()

	select {
	case evt := <-events:
		if evt.Type != SocketEventSlashCmd {
			t.Errorf("event type = %q, want %q", evt.Type, SocketEventSlashCmd)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestSocketModeHandler_Disconnect(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()

	env := Envelope{
		EnvelopeID: "disc-001",
		Type:       "disconnect",
		Reason:     "link_disabled",
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Should receive disconnect event.
	select {
	case evt := <-events:
		if evt.Type != SocketEventDisconnect {
			t.Errorf("event type = %q, want %q", evt.Type, SocketEventDisconnect)
		}
		if evt.EnvelopeID != "disc-001" {
			t.Errorf("envelope_id = %q, want %q", evt.EnvelopeID, "disc-001")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for disconnect event")
	}

	// Channel should close after disconnect.
	select {
	case _, ok := <-events:
		if ok {
			t.Error("expected events channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events channel close")
	}
}

func TestSocketModeHandler_UnknownType(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	// Send unknown type — should be acked but not emitted.
	env := Envelope{
		EnvelopeID: "unk-001",
		Type:       "unknown_type",
		Payload:    json.RawMessage(`{}`),
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Ack should still be sent.
	_, ackData, err := server.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var ack envelopeAck
	if err := json.Unmarshal(ackData, &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if ack.EnvelopeID != "unk-001" {
		t.Errorf("ack envelope_id = %q, want %q", ack.EnvelopeID, "unk-001")
	}

	// No event should be emitted. Send a known event to flush.
	env2 := Envelope{
		EnvelopeID: "evt-flush",
		Type:       "events_api",
		Payload:    json.RawMessage(`{}`),
	}
	data2, _ := json.Marshal(env2)
	if err := server.WriteMessage(websocket.TextMessage, data2); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, _ = server.ReadMessage() // ack for flush

	select {
	case evt := <-events:
		if evt.EnvelopeID == "unk-001" {
			t.Error("unknown envelope type should not emit an event")
		}
		// Should be the flush event.
		if evt.EnvelopeID != "evt-flush" {
			t.Errorf("expected flush event, got %q", evt.EnvelopeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for flush event")
	}
}

func TestSocketModeHandler_MissingEnvelopeID(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	// Send envelope without envelope_id — should be skipped.
	if err := server.WriteMessage(websocket.TextMessage, []byte(`{"type":"events_api","payload":{}}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Send a valid one to verify the handler is still alive.
	env := Envelope{
		EnvelopeID: "valid-001",
		Type:       "events_api",
		Payload:    json.RawMessage(`{}`),
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, _ = server.ReadMessage() // ack

	select {
	case evt := <-events:
		if evt.EnvelopeID != "valid-001" {
			t.Errorf("expected valid-001, got %q", evt.EnvelopeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestMapEnvelopeType(t *testing.T) {
	tests := []struct {
		input string
		want  SocketEventType
		ok    bool
	}{
		{"events_api", SocketEventMessage, true},
		{"interactive", SocketEventInteraction, true},
		{"slash_commands", SocketEventSlashCmd, true},
		{"disconnect", SocketEventDisconnect, true},
		{"unknown", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		got, ok := mapEnvelopeType(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("mapEnvelopeType(%q) = (%q, %v), want (%q, %v)",
				tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
