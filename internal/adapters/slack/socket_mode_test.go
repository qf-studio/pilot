package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
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

func TestSocketModeHandler_MalformedJSON(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	// Send malformed JSON — handler should log error and continue.
	if err := server.WriteMessage(websocket.TextMessage, []byte(`{not valid json!!!`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Handler should still be alive. Send a valid envelope to prove it.
	env := Envelope{
		EnvelopeID: "after-malformed",
		Type:       "events_api",
		Payload:    json.RawMessage(`{"event":{"type":"message","text":"still alive"}}`),
	}
	data, _ := json.Marshal(env)
	if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read ack for the valid envelope.
	_, ackData, err := server.ReadMessage()
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	var ack envelopeAck
	if err := json.Unmarshal(ackData, &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if ack.EnvelopeID != "after-malformed" {
		t.Errorf("ack envelope_id = %q, want %q", ack.EnvelopeID, "after-malformed")
	}

	select {
	case evt := <-events:
		if evt.EnvelopeID != "after-malformed" {
			t.Errorf("expected after-malformed, got %q", evt.EnvelopeID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event after malformed JSON")
	}
}

func TestSocketModeHandler_ServerClose(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()

	// Server closes the connection abruptly.
	_ = server.WriteMessage(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"),
	)
	_ = server.Close()

	// Events channel should close when handler exits.
	select {
	case _, ok := <-events:
		if ok {
			// May receive one last event; drain and check again.
			select {
			case _, ok2 := <-events:
				if ok2 {
					t.Error("expected events channel to close after server disconnect")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for channel close")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for events channel to close after server close")
	}
}

func TestSocketModeHandler_MultipleEnvelopes(t *testing.T) {
	client, server := newTestWSPair(t)

	handler, events := NewSocketModeHandler(client)
	handler.PongWait = 5 * time.Second
	handler.PingInterval = 2 * time.Second

	go handler.Run()
	defer handler.Close()

	envelopes := []struct {
		id       string
		envType  string
		wantType SocketEventType
	}{
		{"multi-001", "events_api", SocketEventMessage},
		{"multi-002", "interactive", SocketEventInteraction},
		{"multi-003", "slash_commands", SocketEventSlashCmd},
	}

	for _, e := range envelopes {
		env := Envelope{
			EnvelopeID: e.id,
			Type:       e.envType,
			Payload:    json.RawMessage(`{}`),
		}
		data, _ := json.Marshal(env)
		if err := server.WriteMessage(websocket.TextMessage, data); err != nil {
			t.Fatalf("write %s: %v", e.id, err)
		}

		// Read ack.
		_, ackData, err := server.ReadMessage()
		if err != nil {
			t.Fatalf("read ack for %s: %v", e.id, err)
		}
		var ack envelopeAck
		if err := json.Unmarshal(ackData, &ack); err != nil {
			t.Fatalf("unmarshal ack for %s: %v", e.id, err)
		}
		if ack.EnvelopeID != e.id {
			t.Errorf("ack envelope_id = %q, want %q", ack.EnvelopeID, e.id)
		}
	}

	// Read all emitted events and verify ordering.
	for _, e := range envelopes {
		select {
		case evt := <-events:
			if evt.EnvelopeID != e.id {
				t.Errorf("event envelope_id = %q, want %q", evt.EnvelopeID, e.id)
			}
			if evt.Type != e.wantType {
				t.Errorf("event type = %q, want %q", evt.Type, e.wantType)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %s", e.id)
		}
	}
}

// TestSocketModeIntegration exercises the full Socket Mode lifecycle:
// 1. Mock HTTP server responds to apps.connections.open with a WSS URL
// 2. Mock WebSocket server accepts the connection and sends test envelopes
// 3. Handler acknowledges envelopes and emits events on the channel
//
// This is a table-driven integration test covering the scenarios requested
// in GH-707: message event, app_mention with bot prefix stripping,
// bot self-message filtering, disconnect triggers channel close, and
// malformed envelope handling.
func TestSocketModeIntegration(t *testing.T) {
	tests := []struct {
		name       string
		envelopes  []string // raw JSON envelopes to send over WS
		wantEvents []struct {
			envID    string
			evtType  SocketEventType
			hasEvent bool // false = no event expected (acked but not emitted)
		}
		wantChannelClosed bool // true if events channel should close after all envelopes
	}{
		{
			name: "message event with ack",
			envelopes: []string{
				`{"envelope_id":"int-msg-001","type":"events_api","payload":{"token":"t","team_id":"T1","type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"hello","ts":"1.1"}}}`,
			},
			wantEvents: []struct {
				envID    string
				evtType  SocketEventType
				hasEvent bool
			}{
				{"int-msg-001", SocketEventMessage, true},
			},
		},
		{
			name: "app_mention event parsed correctly",
			envelopes: []string{
				`{"envelope_id":"int-mention-001","type":"events_api","payload":{"token":"t","team_id":"T1","type":"event_callback","event":{"type":"app_mention","channel":"C2","user":"U2","text":"<@UBOT99> deploy prod","ts":"2.2"}}}`,
			},
			wantEvents: []struct {
				envID    string
				evtType  SocketEventType
				hasEvent bool
			}{
				{"int-mention-001", SocketEventMessage, true},
			},
		},
		{
			name: "disconnect closes channel",
			envelopes: []string{
				`{"envelope_id":"int-disc-001","type":"disconnect","reason":"link_disabled"}`,
			},
			wantEvents: []struct {
				envID    string
				evtType  SocketEventType
				hasEvent bool
			}{
				{"int-disc-001", SocketEventDisconnect, true},
			},
			wantChannelClosed: true,
		},
		{
			name: "malformed envelope then valid envelope",
			envelopes: []string{
				`{totally broken json`,
				`{"envelope_id":"int-recover-001","type":"events_api","payload":{"event":{"type":"message","text":"ok"}}}`,
			},
			wantEvents: []struct {
				envID    string
				evtType  SocketEventType
				hasEvent bool
			}{
				{"int-recover-001", SocketEventMessage, true},
			},
		},
		{
			name: "bot message is emitted with payload for caller filtering",
			envelopes: []string{
				`{"envelope_id":"int-bot-001","type":"events_api","payload":{"token":"t","team_id":"T1","type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"bot msg","ts":"3.3","bot_id":"B999"}}}`,
			},
			wantEvents: []struct {
				envID    string
				evtType  SocketEventType
				hasEvent bool
			}{
				{"int-bot-001", SocketEventMessage, true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 1. Create mock WebSocket server.
			upgrader := websocket.Upgrader{}
			var serverConn *websocket.Conn
			var wsWg sync.WaitGroup
			wsWg.Add(1)

			wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var err error
				serverConn, err = upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Errorf("ws upgrade: %v", err)
					return
				}
				wsWg.Done()
			}))
			defer wsSrv.Close()

			wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

			// 2. Create mock HTTP server for apps.connections.open
			// that returns the WS server URL.
			apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/apps.connections.open" {
					t.Errorf("unexpected path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				auth := r.Header.Get("Authorization")
				if auth != "Bearer "+testutil.FakeSlackAppToken {
					t.Errorf("unexpected auth header: %s", auth)
				}
				resp := fmt.Sprintf(`{"ok":true,"url":%q}`, wsURL)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(resp))
			}))
			defer apiSrv.Close()

			// 3. Use SocketModeClient to get the WS URL (integration handshake).
			smClient := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, apiSrv.URL)
			gotURL, err := smClient.OpenConnection(context.Background())
			if err != nil {
				t.Fatalf("OpenConnection: %v", err)
			}
			if gotURL != wsURL {
				t.Fatalf("OpenConnection URL = %q, want %q", gotURL, wsURL)
			}

			// 4. Dial the WebSocket and create handler.
			clientConn, _, err := websocket.DefaultDialer.Dial(gotURL, nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = clientConn.Close() }()

			wsWg.Wait()
			defer func() { _ = serverConn.Close() }()

			handler, events := NewSocketModeHandler(clientConn)
			handler.PongWait = 5 * time.Second
			handler.PingInterval = 2 * time.Second

			go handler.Run()
			if !tt.wantChannelClosed {
				defer handler.Close()
			}

			// 5. Server sends envelopes.
			for _, raw := range tt.envelopes {
				if err := serverConn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
					t.Fatalf("write: %v", err)
				}

				// Read ack if the envelope has a valid envelope_id and valid JSON.
				var env struct {
					EnvelopeID string `json:"envelope_id"`
				}
				if json.Unmarshal([]byte(raw), &env) == nil && env.EnvelopeID != "" {
					_ = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
					_, ackData, err := serverConn.ReadMessage()
					if err != nil {
						t.Fatalf("read ack for %s: %v", env.EnvelopeID, err)
					}
					var ack envelopeAck
					if err := json.Unmarshal(ackData, &ack); err != nil {
						t.Fatalf("unmarshal ack: %v", err)
					}
					if ack.EnvelopeID != env.EnvelopeID {
						t.Errorf("ack envelope_id = %q, want %q", ack.EnvelopeID, env.EnvelopeID)
					}
					_ = serverConn.SetReadDeadline(time.Time{})
				}
			}

			// 6. Verify expected events.
			for _, want := range tt.wantEvents {
				if !want.hasEvent {
					continue
				}
				select {
				case evt := <-events:
					if evt.EnvelopeID != want.envID {
						t.Errorf("event envelope_id = %q, want %q", evt.EnvelopeID, want.envID)
					}
					if evt.Type != want.evtType {
						t.Errorf("event type = %q, want %q", evt.Type, want.evtType)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("timed out waiting for event %s", want.envID)
				}
			}

			// 7. Verify channel closure if expected.
			if tt.wantChannelClosed {
				select {
				case _, ok := <-events:
					if ok {
						t.Error("expected events channel to be closed")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for events channel close")
				}
			}
		})
	}
}
