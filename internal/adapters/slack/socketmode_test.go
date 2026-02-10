package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/testutil"
	"github.com/gorilla/websocket"
)

func TestOpenConnection(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		response   string
		wantURL    string
		wantErr    error
		wantErrMsg string
	}{
		{
			name:       "successful connection returns WSS URL",
			statusCode: http.StatusOK,
			response:   `{"ok":true,"url":"wss://wss-primary.slack.com/link/?ticket=abc123"}`,
			wantURL:    "wss://wss-primary.slack.com/link/?ticket=abc123",
		},
		{
			name:       "invalid auth error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"invalid_auth"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "invalid_auth",
		},
		{
			name:       "not authed error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"not_authed"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "not_authed",
		},
		{
			name:       "token revoked error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"token_revoked"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "token_revoked",
		},
		{
			name:       "account inactive error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"account_inactive"}`,
			wantErr:    ErrAuthFailure,
			wantErrMsg: "account_inactive",
		},
		{
			name:       "non-auth API error",
			statusCode: http.StatusOK,
			response:   `{"ok":false,"error":"too_many_websockets"}`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "too_many_websockets",
		},
		{
			name:       "HTTP 500 error",
			statusCode: http.StatusInternalServerError,
			response:   `Internal Server Error`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "HTTP 500",
		},
		{
			name:       "empty URL in response",
			statusCode: http.StatusOK,
			response:   `{"ok":true,"url":""}`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "empty WebSocket URL",
		},
		{
			name:       "malformed JSON response",
			statusCode: http.StatusOK,
			response:   `{not json`,
			wantErr:    ErrConnectionOpen,
			wantErrMsg: "failed to parse response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request method and path
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/apps.connections.open" {
					t.Errorf("expected /apps.connections.open, got %s", r.URL.Path)
				}

				// Verify auth header
				auth := r.Header.Get("Authorization")
				if auth != "Bearer "+testutil.FakeSlackAppToken {
					t.Errorf("expected Bearer %s, got %s", testutil.FakeSlackAppToken, auth)
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, server.URL)
			url, err := client.OpenConnection(context.Background())

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("expected error wrapping %v, got: %v", tt.wantErr, err)
				}
				if tt.wantErrMsg != "" {
					if got := err.Error(); !contains(got, tt.wantErrMsg) {
						t.Errorf("error %q does not contain %q", got, tt.wantErrMsg)
					}
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if url != tt.wantURL {
				t.Errorf("expected URL %q, got %q", tt.wantURL, url)
			}
		})
	}
}

func TestOpenConnectionCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"url":"wss://example.com"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, server.URL)
	_, err := client.OpenConnection(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, ErrConnectionOpen) {
		t.Errorf("expected ErrConnectionOpen, got: %v", err)
	}
}

func TestNewSocketModeClient(t *testing.T) {
	client := NewSocketModeClient(testutil.FakeSlackAppToken)
	if client.appToken != testutil.FakeSlackAppToken {
		t.Errorf("expected appToken %q, got %q", testutil.FakeSlackAppToken, client.appToken)
	}
	if client.apiURL != slackAPIURL {
		t.Errorf("expected apiURL %q, got %q", slackAPIURL, client.apiURL)
	}
	if client.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
}

// contains and searchString helpers are in events_test.go

// --- Listen tests ---

// setupListenTestServer creates a mock HTTP API server that returns a WS URL,
// and a mock WebSocket server that accepts connections and invokes onConn.
// Returns the SocketModeClient configured to use these servers.
func setupListenTestServer(t *testing.T, onConn func(conn *websocket.Conn)) *SocketModeClient {
	t.Helper()

	upgrader := websocket.Upgrader{}
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		onConn(conn)
	}))
	t.Cleanup(wsSrv.Close)

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := fmt.Sprintf(`{"ok":true,"url":%q}`, wsURL)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(apiSrv.Close)

	return NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, apiSrv.URL)
}

func TestListen_ReceivesMessageEvents(t *testing.T) {
	client := setupListenTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()

		// Send a message event.
		env := map[string]interface{}{
			"envelope_id": "listen-msg-001",
			"type":        "events_api",
			"payload": map[string]interface{}{
				"token":   "t",
				"team_id": "T1",
				"type":    "event_callback",
				"event": map[string]interface{}{
					"type":    "message",
					"channel": "C123",
					"user":    "U456",
					"text":    "hello from listen",
					"ts":      "1.1",
				},
			},
		}
		data, _ := json.Marshal(env)
		_ = conn.WriteMessage(websocket.TextMessage, data)

		// Read ack.
		_, _, _ = conn.ReadMessage()

		// Keep connection open briefly for event to be forwarded.
		time.Sleep(100 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	select {
	case evt, ok := <-events:
		if !ok {
			t.Fatal("events channel closed before receiving event")
		}
		if evt.Type != EventTypeMessage {
			t.Errorf("event type = %q, want %q", evt.Type, EventTypeMessage)
		}
		if evt.ChannelID != "C123" {
			t.Errorf("channel = %q, want %q", evt.ChannelID, "C123")
		}
		if evt.Text != "hello from listen" {
			t.Errorf("text = %q, want %q", evt.Text, "hello from listen")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	cancel()
	// Channel should close after cancel.
	for range events {
	}
}

func TestListen_AppMentionStripsBot(t *testing.T) {
	client := setupListenTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()

		env := map[string]interface{}{
			"envelope_id": "listen-mention-001",
			"type":        "events_api",
			"payload": map[string]interface{}{
				"token":   "t",
				"team_id": "T1",
				"type":    "event_callback",
				"event": map[string]interface{}{
					"type":    "app_mention",
					"channel": "C123",
					"user":    "U456",
					"text":    "<@UBOT99> deploy prod",
					"ts":      "2.2",
				},
			},
		}
		data, _ := json.Marshal(env)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		_, _, _ = conn.ReadMessage() // ack
		time.Sleep(100 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	select {
	case evt := <-events:
		if evt.Text != "deploy prod" {
			t.Errorf("text = %q, want %q (bot mention should be stripped)", evt.Text, "deploy prod")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	cancel()
	for range events {
	}
}

func TestListen_DisconnectTriggersReconnect(t *testing.T) {
	var connCount atomic.Int32

	upgrader := websocket.Upgrader{}
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		n := connCount.Add(1)

		if n == 1 {
			// First connection: send disconnect.
			env := map[string]interface{}{
				"envelope_id": "disc-001",
				"type":        "disconnect",
				"reason":      "link_disabled",
			}
			data, _ := json.Marshal(env)
			_ = conn.WriteMessage(websocket.TextMessage, data)
			// Read ack.
			_, _, _ = conn.ReadMessage()
			return
		}

		// Second connection: send a message then stay alive.
		env := map[string]interface{}{
			"envelope_id": "reconnect-msg-001",
			"type":        "events_api",
			"payload": map[string]interface{}{
				"token":   "t",
				"team_id": "T1",
				"type":    "event_callback",
				"event": map[string]interface{}{
					"type":    "message",
					"channel": "C999",
					"user":    "U111",
					"text":    "after reconnect",
					"ts":      "3.3",
				},
			},
		}
		data, _ := json.Marshal(env)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		_, _, _ = conn.ReadMessage() // ack
		// Keep alive long enough for event to be forwarded.
		time.Sleep(500 * time.Millisecond)
	}))
	defer wsSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := fmt.Sprintf(`{"ok":true,"url":%q}`, wsURL)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer apiSrv.Close()

	client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, apiSrv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Should receive the message from the second connection.
	select {
	case evt := <-events:
		if evt.Text != "after reconnect" {
			t.Errorf("text = %q, want %q", evt.Text, "after reconnect")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for event after reconnect")
	}

	if got := connCount.Load(); got < 2 {
		t.Errorf("expected at least 2 connections (reconnect), got %d", got)
	}

	cancel()
	for range events {
	}
}

func TestListen_AuthFailure(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
	}))
	defer apiSrv.Close()

	client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, apiSrv.URL)

	_, err := client.Listen(context.Background())
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
	if !errors.Is(err, ErrAuthFailure) {
		t.Errorf("expected ErrAuthFailure, got: %v", err)
	}
}

func TestListen_ContextCancelClosesChannel(t *testing.T) {
	client := setupListenTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()
		// Keep connection alive until test cancels.
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Cancel immediately.
	cancel()

	// Channel should close.
	select {
	case _, ok := <-events:
		if ok {
			// Might get a straggler event; drain and check again.
			for range events {
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for events channel to close")
	}
}

func TestListen_MultipleEvents(t *testing.T) {
	client := setupListenTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()

		for i := 0; i < 3; i++ {
			env := map[string]interface{}{
				"envelope_id": fmt.Sprintf("multi-%d", i),
				"type":        "events_api",
				"payload": map[string]interface{}{
					"token":   "t",
					"team_id": "T1",
					"type":    "event_callback",
					"event": map[string]interface{}{
						"type":    "message",
						"channel": "C123",
						"user":    "U456",
						"text":    fmt.Sprintf("msg-%d", i),
						"ts":      fmt.Sprintf("%d.%d", i, i),
					},
				},
			}
			data, _ := json.Marshal(env)
			_ = conn.WriteMessage(websocket.TextMessage, data)
			_, _, _ = conn.ReadMessage() // ack
		}
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	var received []string
	for i := 0; i < 3; i++ {
		select {
		case evt := <-events:
			received = append(received, evt.Text)
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}

	for i, want := range []string{"msg-0", "msg-1", "msg-2"} {
		if received[i] != want {
			t.Errorf("event[%d].Text = %q, want %q", i, received[i], want)
		}
	}

	cancel()
	for range events {
	}
}

func TestListen_SkipsNonEventsAPITypes(t *testing.T) {
	client := setupListenTestServer(t, func(conn *websocket.Conn) {
		defer func() { _ = conn.Close() }()

		// Send interactive envelope — should be skipped (not mapped to SocketEvent).
		interactive := map[string]interface{}{
			"envelope_id": "int-001",
			"type":        "interactive",
			"payload":     map[string]interface{}{"type": "block_actions"},
		}
		data, _ := json.Marshal(interactive)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		_, _, _ = conn.ReadMessage() // ack

		// Then send a real message event.
		msg := map[string]interface{}{
			"envelope_id": "msg-001",
			"type":        "events_api",
			"payload": map[string]interface{}{
				"token":   "t",
				"team_id": "T1",
				"type":    "event_callback",
				"event": map[string]interface{}{
					"type":    "message",
					"channel": "C123",
					"user":    "U456",
					"text":    "the real message",
					"ts":      "5.5",
				},
			},
		}
		data, _ = json.Marshal(msg)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		_, _, _ = conn.ReadMessage() // ack
		time.Sleep(200 * time.Millisecond)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Should only receive the message event, not the interactive one.
	select {
	case evt := <-events:
		if evt.Text != "the real message" {
			t.Errorf("text = %q, want %q", evt.Text, "the real message")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}

	cancel()
	for range events {
	}
}

func TestParseRawEvent_NonEventsAPI(t *testing.T) {
	client := NewSocketModeClient(testutil.FakeSlackAppToken)

	tests := []struct {
		name    string
		rawType SocketEventType
	}{
		{"interactive", SocketEventInteraction},
		{"slash_commands", SocketEventSlashCmd},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt, err := client.parseRawEvent(RawSocketEvent{
				Type:       tt.rawType,
				EnvelopeID: "test-001",
				Payload:    json.RawMessage(`{}`),
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if evt != nil {
				t.Errorf("expected nil event for %s type, got %+v", tt.rawType, evt)
			}
		})
	}
}

func TestListen_ServerDropReconnects(t *testing.T) {
	var connCount atomic.Int32
	var mu sync.Mutex
	var conns []*websocket.Conn

	upgrader := websocket.Upgrader{}
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		mu.Lock()
		conns = append(conns, conn)
		mu.Unlock()

		n := connCount.Add(1)

		if n == 1 {
			// First connection: close abruptly.
			_ = conn.Close()
			return
		}

		// Second connection: send message.
		env := map[string]interface{}{
			"envelope_id": "drop-msg-001",
			"type":        "events_api",
			"payload": map[string]interface{}{
				"token":   "t",
				"team_id": "T1",
				"type":    "event_callback",
				"event": map[string]interface{}{
					"type":    "message",
					"channel": "C123",
					"user":    "U456",
					"text":    "after drop",
					"ts":      "7.7",
				},
			},
		}
		data, _ := json.Marshal(env)
		_ = conn.WriteMessage(websocket.TextMessage, data)
		_, _, _ = conn.ReadMessage() // ack
		time.Sleep(500 * time.Millisecond)
		_ = conn.Close()
	}))
	defer wsSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := fmt.Sprintf(`{"ok":true,"url":%q}`, wsURL)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	}))
	defer apiSrv.Close()

	client := NewSocketModeClientWithBaseURL(testutil.FakeSlackAppToken, apiSrv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events, err := client.Listen(ctx)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	select {
	case evt := <-events:
		if evt.Text != "after drop" {
			t.Errorf("text = %q, want %q", evt.Text, "after drop")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for event after server drop")
	}

	cancel()
	for range events {
	}
}
