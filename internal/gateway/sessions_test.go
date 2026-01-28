package gateway

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNewSessionManager(t *testing.T) {
	sm := NewSessionManager()

	if sm == nil {
		t.Fatal("NewSessionManager returned nil")
	}
	if sm.sessions == nil {
		t.Error("Sessions map not initialized")
	}
	if sm.Count() != 0 {
		t.Errorf("Expected 0 sessions, got %d", sm.Count())
	}
}

func TestSessionManagerCreateAndGet(t *testing.T) {
	sm := NewSessionManager()

	// Create a mock WebSocket connection using httptest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// Keep connection open briefly
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	// Connect to the test server
	wsURL := "ws" + server.URL[4:] // Convert http:// to ws://
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to test server: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Create session
	session := sm.Create(conn)

	if session == nil {
		t.Fatal("Create returned nil session")
	}
	if session.ID == "" {
		t.Error("Session ID should not be empty")
	}
	if session.Conn != conn {
		t.Error("Session connection not set correctly")
	}
	if session.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if session.LastPing.IsZero() {
		t.Error("LastPing should be set")
	}
	if sm.Count() != 1 {
		t.Errorf("Expected 1 session, got %d", sm.Count())
	}

	// Get session
	retrieved, ok := sm.Get(session.ID)
	if !ok {
		t.Error("Failed to get session by ID")
	}
	if retrieved != session {
		t.Error("Retrieved session does not match created session")
	}

	// Get non-existent session
	_, ok = sm.Get("non-existent-id")
	if ok {
		t.Error("Should not find non-existent session")
	}
}

func TestSessionManagerRemove(t *testing.T) {
	sm := NewSessionManager()

	// Create a mock WebSocket connection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to test server: %v", err)
	}

	session := sm.Create(conn)
	if sm.Count() != 1 {
		t.Errorf("Expected 1 session, got %d", sm.Count())
	}

	// Remove session
	sm.Remove(session.ID)

	if sm.Count() != 0 {
		t.Errorf("Expected 0 sessions after removal, got %d", sm.Count())
	}

	// Verify session is gone
	_, ok := sm.Get(session.ID)
	if ok {
		t.Error("Session should not exist after removal")
	}

	// Remove non-existent session - should not panic
	sm.Remove("non-existent-id")
}

func TestSessionManagerCount(t *testing.T) {
	sm := NewSessionManager()

	tests := []struct {
		name          string
		addSessions   int
		expectedCount int
	}{
		{
			name:          "zero sessions",
			addSessions:   0,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if sm.Count() != tt.expectedCount {
				t.Errorf("Expected %d sessions, got %d", tt.expectedCount, sm.Count())
			}
		})
	}
}

func TestSessionManagerConcurrentAccess(t *testing.T) {
	sm := NewSessionManager()

	// Create a test server that can handle multiple connections
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]
	numGoroutines := 10
	var wg sync.WaitGroup

	// Concurrently create sessions
	sessions := make([]*Session, numGoroutines)
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Errorf("Failed to connect: %v", err)
				return
			}
			sessions[idx] = sm.Create(conn)
		}(i)
	}

	wg.Wait()

	// Verify all sessions were created
	if sm.Count() != numGoroutines {
		t.Errorf("Expected %d sessions, got %d", numGoroutines, sm.Count())
	}

	// Concurrently read and remove sessions
	wg.Add(numGoroutines * 2)

	for i := 0; i < numGoroutines; i++ {
		// Reader goroutine
		go func(idx int) {
			defer wg.Done()
			if sessions[idx] != nil {
				_, _ = sm.Get(sessions[idx].ID)
			}
		}(i)

		// Remover goroutine
		go func(idx int) {
			defer wg.Done()
			if sessions[idx] != nil {
				sm.Remove(sessions[idx].ID)
			}
		}(i)
	}

	wg.Wait()

	// All sessions should be removed
	if sm.Count() != 0 {
		t.Errorf("Expected 0 sessions after removal, got %d", sm.Count())
	}
}

func TestSessionUpdatePing(t *testing.T) {
	sm := NewSessionManager()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	session := sm.Create(conn)
	originalPing := session.LastPing

	// Wait a bit and update ping
	time.Sleep(10 * time.Millisecond)
	session.UpdatePing()

	if !session.LastPing.After(originalPing) {
		t.Error("LastPing should be updated to a later time")
	}
}

func TestSessionSend(t *testing.T) {
	receivedMessage := make(chan []byte, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read message from client
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receivedMessage <- msg
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

	testMessage := []byte(`{"type":"test","data":"hello"}`)
	err = session.Send(testMessage)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case received := <-receivedMessage:
		if string(received) != string(testMessage) {
			t.Errorf("Expected message %s, got %s", testMessage, received)
		}
	case <-time.After(1 * time.Second):
		t.Error("Timeout waiting for message")
	}
}

func TestSessionManagerBroadcast(t *testing.T) {
	numClients := 3
	receivedMessages := make(chan []byte, numClients)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Read message from server
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		receivedMessages <- msg
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]
	sm := NewSessionManager()

	// Create multiple client connections
	var conns []*websocket.Conn
	for i := 0; i < numClients; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		conns = append(conns, conn)
		sm.Create(conn)
	}

	// Clean up connections
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()

	// Broadcast message
	testMessage := []byte(`{"type":"broadcast","data":"hello all"}`)
	sm.Broadcast(testMessage)

	// Wait for all clients to receive the message
	receivedCount := 0
	timeout := time.After(2 * time.Second)

	for receivedCount < numClients {
		select {
		case msg := <-receivedMessages:
			if string(msg) != string(testMessage) {
				t.Errorf("Expected message %s, got %s", testMessage, msg)
			}
			receivedCount++
		case <-timeout:
			t.Errorf("Timeout: only received %d/%d messages", receivedCount, numClients)
			return
		}
	}
}

func TestSessionIDUniqueness(t *testing.T) {
	sm := NewSessionManager()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[4:]
	numSessions := 100
	ids := make(map[string]bool)

	for i := 0; i < numSessions; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("Failed to connect: %v", err)
		}
		defer func() { _ = conn.Close() }()

		session := sm.Create(conn)
		if ids[session.ID] {
			t.Errorf("Duplicate session ID generated: %s", session.ID)
		}
		ids[session.ID] = true
	}

	if len(ids) != numSessions {
		t.Errorf("Expected %d unique IDs, got %d", numSessions, len(ids))
	}
}
