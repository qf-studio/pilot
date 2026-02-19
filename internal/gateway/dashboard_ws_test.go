package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/memory"
	"github.com/gorilla/websocket"
)

// mockLogStreamStore implements LogStreamStore for testing.
type mockLogStreamStore struct {
	mu         sync.Mutex
	logEntries []*memory.LogEntry
	subs       []chan *memory.LogEntry
}

func (m *mockLogStreamStore) GetRecentLogs(_ int) ([]*memory.LogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logEntries, nil
}

func (m *mockLogStreamStore) SubscribeLogs() chan *memory.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan *memory.LogEntry, 64)
	m.subs = append(m.subs, ch)
	return ch
}

func (m *mockLogStreamStore) UnsubscribeLogs(ch chan *memory.LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, sub := range m.subs {
		if sub == ch {
			m.subs = append(m.subs[:i], m.subs[i+1:]...)
			break
		}
	}
	close(ch)
}

// publish sends a log entry to all current subscribers.
func (m *mockLogStreamStore) publish(entry *memory.LogEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

// subCount returns the number of active subscribers (thread-safe).
func (m *mockLogStreamStore) subCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

func TestDashboardWebSocket_InitialLogs(t *testing.T) {
	now := time.Now()
	store := &mockLogStreamStore{
		logEntries: []*memory.LogEntry{
			{ID: 2, Timestamp: now, Level: "info", Message: "second", Component: "test"},
			{ID: 1, Timestamp: now.Add(-time.Second), Level: "warn", Message: "first", Component: "test"},
		},
	}

	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	srv.logStreamStore = store

	ts := httptest.NewServer(http.HandlerFunc(srv.handleDashboardWebSocket))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read initial batch
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	var initial []logEntryResponse
	if err := json.Unmarshal(msg, &initial); err != nil {
		t.Fatalf("Unmarshal initial logs failed: %v", err)
	}

	if len(initial) != 2 {
		t.Fatalf("Expected 2 initial entries, got %d", len(initial))
	}

	// Should be reversed to chronological order (oldest first)
	if initial[0].Message != "first" {
		t.Errorf("Expected first entry 'first', got %q", initial[0].Message)
	}
	if initial[1].Message != "second" {
		t.Errorf("Expected second entry 'second', got %q", initial[1].Message)
	}
}

func TestDashboardWebSocket_StreamEntry(t *testing.T) {
	store := &mockLogStreamStore{}

	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	srv.logStreamStore = store

	ts := httptest.NewServer(http.HandlerFunc(srv.handleDashboardWebSocket))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read empty initial batch
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	var initial []logEntryResponse
	if err := json.Unmarshal(msg, &initial); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(initial) != 0 {
		t.Fatalf("Expected 0 initial entries, got %d", len(initial))
	}

	// Publish a new entry
	store.publish(&memory.LogEntry{
		ID:        3,
		Timestamp: time.Now(),
		Level:     "error",
		Message:   "streamed entry",
		Component: "executor",
	})

	// Read streamed entry
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err = conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage for streamed entry failed: %v", err)
	}

	var entry logEntryResponse
	if err := json.Unmarshal(msg, &entry); err != nil {
		t.Fatalf("Unmarshal streamed entry failed: %v", err)
	}

	if entry.Message != "streamed entry" {
		t.Errorf("Expected 'streamed entry', got %q", entry.Message)
	}
	if entry.Level != "error" {
		t.Errorf("Expected level 'error', got %q", entry.Level)
	}
	if entry.Component != "executor" {
		t.Errorf("Expected component 'executor', got %q", entry.Component)
	}
}

func TestDashboardWebSocket_NoStore(t *testing.T) {
	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	// logStreamStore is nil

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/dashboard", nil)
	srv.handleDashboardWebSocket(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected 503, got %d", rr.Code)
	}
}

func TestDashboardWebSocket_ClientDisconnect(t *testing.T) {
	store := &mockLogStreamStore{}

	srv := NewServer(&Config{Host: "127.0.0.1", Port: 0})
	srv.logStreamStore = store

	ts := httptest.NewServer(http.HandlerFunc(srv.handleDashboardWebSocket))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}

	// Read initial batch
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	// Verify subscriber was added
	if n := store.subCount(); n != 1 {
		t.Fatalf("Expected 1 subscriber, got %d", n)
	}

	// Close client connection
	_ = conn.Close()

	// Give server time to clean up
	time.Sleep(100 * time.Millisecond)

	// After disconnect, subscriber should be cleaned up
	if n := store.subCount(); n != 0 {
		t.Errorf("Expected 0 subscribers after disconnect, got %d", n)
	}
}
