package gateway

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Session represents a connected client session
type Session struct {
	ID        string
	Conn      *websocket.Conn
	CreatedAt time.Time
	LastPing  time.Time
	mu        sync.Mutex
}

// SessionManager manages active sessions
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

// NewSessionManager creates a new session manager
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

// Create creates a new session for a WebSocket connection
func (m *SessionManager) Create(conn *websocket.Conn) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	session := &Session{
		ID:        uuid.New().String(),
		Conn:      conn,
		CreatedAt: time.Now(),
		LastPing:  time.Now(),
	}

	m.sessions[session.ID] = session
	return session
}

// Get retrieves a session by ID
func (m *SessionManager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, ok := m.sessions[id]
	return session, ok
}

// Remove removes a session
func (m *SessionManager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[id]; ok {
		_ = session.Conn.Close()
		delete(m.sessions, id)
	}
}

// Count returns the number of active sessions
func (m *SessionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// Broadcast sends a message to all sessions
func (m *SessionManager) Broadcast(message []byte) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, session := range m.sessions {
		_ = session.Send(message)
	}
}

// Send sends a message to this session
func (s *Session) Send(message []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Conn.WriteMessage(websocket.TextMessage, message)
}

// UpdatePing updates the last ping time
func (s *Session) UpdatePing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastPing = time.Now()
}
