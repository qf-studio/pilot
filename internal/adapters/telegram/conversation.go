package telegram

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ConversationMessage represents a single message in conversation history
type ConversationMessage struct {
	Role      string    // "user" or "assistant"
	Content   string    // Message content
	Intent    Intent    // Detected intent (for user messages)
	Timestamp time.Time // When the message was sent
}

// ConversationStore manages conversation histories per chat
type ConversationStore struct {
	mu        sync.RWMutex
	histories map[string][]ConversationMessage // chatID -> messages
	maxSize   int                              // Max messages per chat (default: 10)
	ttl       time.Duration                    // TTL for conversations (default: 30m)
	stopCh    chan struct{}                    // Stop channel for cleanup goroutine
}

// ConversationStoreConfig holds configuration for the conversation store
type ConversationStoreConfig struct {
	MaxSize int           // Max messages to keep per chat
	TTL     time.Duration // Time-to-live for conversation history
}

// DefaultConversationStoreConfig returns sensible defaults
func DefaultConversationStoreConfig() *ConversationStoreConfig {
	return &ConversationStoreConfig{
		MaxSize: 10,
		TTL:     30 * time.Minute,
	}
}

// NewConversationStore creates a new conversation store with the given config
func NewConversationStore(cfg *ConversationStoreConfig) *ConversationStore {
	if cfg == nil {
		cfg = DefaultConversationStoreConfig()
	}

	maxSize := cfg.MaxSize
	if maxSize <= 0 {
		maxSize = 10
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}

	store := &ConversationStore{
		histories: make(map[string][]ConversationMessage),
		maxSize:   maxSize,
		ttl:       ttl,
		stopCh:    make(chan struct{}),
	}

	// Start cleanup goroutine
	go store.cleanupLoop()

	return store
}

// AddUserMessage adds a user message to the conversation history
func (s *ConversationStore) AddUserMessage(chatID, content string, intent Intent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	msg := ConversationMessage{
		Role:      "user",
		Content:   content,
		Intent:    intent,
		Timestamp: time.Now(),
	}

	s.addMessage(chatID, msg)
}

// AddAssistantMessage adds an assistant message to the conversation history
func (s *ConversationStore) AddAssistantMessage(chatID, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	msg := ConversationMessage{
		Role:      "assistant",
		Content:   content,
		Intent:    "", // Assistant messages don't have intent
		Timestamp: time.Now(),
	}

	s.addMessage(chatID, msg)
}

// addMessage adds a message to history, maintaining max size
// Must be called with lock held
func (s *ConversationStore) addMessage(chatID string, msg ConversationMessage) {
	history := s.histories[chatID]
	history = append(history, msg)

	// Trim to max size
	if len(history) > s.maxSize {
		history = history[len(history)-s.maxSize:]
	}

	s.histories[chatID] = history
}

// GetHistory returns the conversation history for a chat
func (s *ConversationStore) GetHistory(chatID string) []ConversationMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.histories[chatID]
	if history == nil {
		return nil
	}

	// Return a copy to avoid race conditions
	result := make([]ConversationMessage, len(history))
	copy(result, history)
	return result
}

// GetContextSummary returns a formatted summary of recent conversation for LLM context
func (s *ConversationStore) GetContextSummary(chatID string, limit int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.histories[chatID]
	if len(history) == 0 {
		return ""
	}

	// Get last N messages
	start := 0
	if limit > 0 && len(history) > limit {
		start = len(history) - limit
	}
	messages := history[start:]

	var sb strings.Builder
	for _, msg := range messages {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", role, truncateForContext(msg.Content, 200)))
	}

	return sb.String()
}

// GetLastUserIntent returns the intent of the last user message (useful for "yes" confirmation handling)
func (s *ConversationStore) GetLastUserIntent(chatID string) Intent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.histories[chatID]
	// Find last user message with a task-related intent
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" && history[i].Intent != "" {
			return history[i].Intent
		}
	}
	return ""
}

// GetLastAssistantMessage returns the last assistant response
func (s *ConversationStore) GetLastAssistantMessage(chatID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	history := s.histories[chatID]
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			return history[i].Content
		}
	}
	return ""
}

// Clear removes conversation history for a chat
func (s *ConversationStore) Clear(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.histories, chatID)
}

// ClearAll removes all conversation histories
func (s *ConversationStore) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.histories = make(map[string][]ConversationMessage)
}

// Stop stops the cleanup goroutine
func (s *ConversationStore) Stop() {
	close(s.stopCh)
}

// cleanupLoop periodically removes expired conversations
func (s *ConversationStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

// cleanup removes conversations with no recent activity
func (s *ConversationStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-s.ttl)

	for chatID, history := range s.histories {
		if len(history) == 0 {
			delete(s.histories, chatID)
			continue
		}

		// Check last message timestamp
		lastMsg := history[len(history)-1]
		if lastMsg.Timestamp.Before(cutoff) {
			delete(s.histories, chatID)
		}
	}
}

// truncateForContext truncates content for context summary
func truncateForContext(content string, maxLen int) string {
	// Replace newlines with spaces for summary
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.TrimSpace(content)

	if len(content) <= maxLen {
		return content
	}
	return content[:maxLen-3] + "..."
}
