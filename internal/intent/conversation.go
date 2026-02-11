package intent

import (
	"sync"
	"time"
)

// ConversationStore maintains recent message history per chat
type ConversationStore struct {
	mu       sync.RWMutex
	history  map[string][]ConversationMessage // chatID -> messages
	maxSize  int
	ttl      time.Duration
	lastSeen map[string]time.Time
}

// NewConversationStore creates a new conversation history store
func NewConversationStore(maxSize int, ttl time.Duration) *ConversationStore {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  maxSize,
		ttl:      ttl,
		lastSeen: make(map[string]time.Time),
	}

	// Start cleanup goroutine
	go store.cleanupLoop()

	return store
}

// Add adds a message to the conversation history
func (s *ConversationStore) Add(chatID, role, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.history[chatID] = append(s.history[chatID], ConversationMessage{
		Role:    role,
		Content: content,
	})

	// Trim to max size
	if len(s.history[chatID]) > s.maxSize {
		s.history[chatID] = s.history[chatID][len(s.history[chatID])-s.maxSize:]
	}

	s.lastSeen[chatID] = time.Now()
}

// Get returns the conversation history for a chat
func (s *ConversationStore) Get(chatID string) []ConversationMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if msgs, ok := s.history[chatID]; ok {
		// Return a copy
		result := make([]ConversationMessage, len(msgs))
		copy(result, msgs)
		return result
	}
	return nil
}

// cleanupLoop removes stale conversations
func (s *ConversationStore) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for chatID, lastSeen := range s.lastSeen {
			if now.Sub(lastSeen) > s.ttl {
				delete(s.history, chatID)
				delete(s.lastSeen, chatID)
			}
		}
		s.mu.Unlock()
	}
}
