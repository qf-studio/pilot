package telegram

import (
	"testing"
	"time"
)

func TestConversationStore_AddUserMessage(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "test-chat-1"

	// Add a user message
	store.AddUserMessage(chatID, "Hello, can you help me?", IntentQuestion)

	// Verify message was stored
	history := store.GetHistory(chatID)
	if len(history) != 1 {
		t.Fatalf("expected 1 message, got %d", len(history))
	}

	msg := history[0]
	if msg.Role != "user" {
		t.Errorf("expected role 'user', got %q", msg.Role)
	}
	if msg.Content != "Hello, can you help me?" {
		t.Errorf("expected content 'Hello, can you help me?', got %q", msg.Content)
	}
	if msg.Intent != IntentQuestion {
		t.Errorf("expected intent IntentQuestion, got %v", msg.Intent)
	}
	if msg.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestConversationStore_AddAssistantMessage(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "test-chat-2"

	// Add an assistant message
	store.AddAssistantMessage(chatID, "I can help you with that.")

	// Verify message was stored
	history := store.GetHistory(chatID)
	if len(history) != 1 {
		t.Fatalf("expected 1 message, got %d", len(history))
	}

	msg := history[0]
	if msg.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", msg.Role)
	}
	if msg.Content != "I can help you with that." {
		t.Errorf("expected content 'I can help you with that.', got %q", msg.Content)
	}
	if msg.Intent != "" {
		t.Errorf("expected empty intent for assistant, got %v", msg.Intent)
	}
}

func TestConversationStore_MaxSize(t *testing.T) {
	cfg := &ConversationStoreConfig{
		MaxSize: 3,
		TTL:     30 * time.Minute,
	}
	store := NewConversationStore(cfg)
	defer store.Stop()

	chatID := "test-chat-3"

	// Add more messages than max size
	// Messages: 1, 2, 3, 4, 5 -> keep last 3: 3, 4, 5
	store.AddUserMessage(chatID, "Message 1", IntentChat)
	store.AddAssistantMessage(chatID, "Response 1")
	store.AddUserMessage(chatID, "Message 2", IntentChat)
	store.AddAssistantMessage(chatID, "Response 2")
	store.AddUserMessage(chatID, "Message 3", IntentTask)

	// Should only keep last 3
	history := store.GetHistory(chatID)
	if len(history) != 3 {
		t.Fatalf("expected 3 messages (max size), got %d", len(history))
	}

	// First kept message should be "Message 2" (index 2 in original, now 0)
	// Messages were: Msg1, Resp1, Msg2, Resp2, Msg3 -> keep: Msg2, Resp2, Msg3
	if history[0].Content != "Message 2" {
		t.Errorf("expected first message 'Message 2', got %q", history[0].Content)
	}

	// Last message should be "Message 3"
	if history[2].Content != "Message 3" {
		t.Errorf("expected last message 'Message 3', got %q", history[2].Content)
	}
}

func TestConversationStore_TTLCleanup(t *testing.T) {
	// Use very short TTL for testing
	cfg := &ConversationStoreConfig{
		MaxSize: 10,
		TTL:     50 * time.Millisecond,
	}
	store := NewConversationStore(cfg)
	defer store.Stop()

	chatID := "test-chat-4"
	store.AddUserMessage(chatID, "Old message", IntentChat)

	// Verify message exists
	history := store.GetHistory(chatID)
	if len(history) != 1 {
		t.Fatalf("expected 1 message before cleanup, got %d", len(history))
	}

	// Wait for TTL to expire and cleanup to run
	// The cleanup loop runs every minute by default, so we manually trigger it
	time.Sleep(100 * time.Millisecond)
	store.cleanup() // Manually trigger cleanup for test

	// History should be cleared
	history = store.GetHistory(chatID)
	if len(history) != 0 {
		t.Errorf("expected 0 messages after TTL cleanup, got %d", len(history))
	}
}

func TestConversationStore_GetContextSummary(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "test-chat-5"

	// Add a conversation
	store.AddUserMessage(chatID, "Add a logout button", IntentTask)
	store.AddAssistantMessage(chatID, "I'll add a logout button to the header.")
	store.AddUserMessage(chatID, "Great, also make it red", IntentTask)

	// Get summary with limit
	summary := store.GetContextSummary(chatID, 2)

	// Should only have last 2 messages
	if summary == "" {
		t.Error("expected non-empty summary")
	}

	// Should contain the assistant response and last user message
	if !containsSubstr(summary, "I'll add a logout button") {
		t.Error("summary missing assistant response")
	}
	if !containsSubstr(summary, "make it red") {
		t.Error("summary missing last user message")
	}

	// Get full summary
	fullSummary := store.GetContextSummary(chatID, 0)
	if !containsSubstr(fullSummary, "Add a logout button") {
		t.Error("full summary missing first message")
	}
}

func TestConversationStore_GetLastUserIntent(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "test-chat-6"

	// No messages - should return empty
	intent := store.GetLastUserIntent(chatID)
	if intent != "" {
		t.Errorf("expected empty intent for empty history, got %v", intent)
	}

	// Add messages
	store.AddUserMessage(chatID, "Hello", IntentGreeting)
	store.AddAssistantMessage(chatID, "Hi there!")
	store.AddUserMessage(chatID, "Add a feature", IntentTask)

	// Should return the last user intent
	intent = store.GetLastUserIntent(chatID)
	if intent != IntentTask {
		t.Errorf("expected IntentTask, got %v", intent)
	}
}

func TestConversationStore_GetLastAssistantMessage(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "test-chat-7"

	// No messages - should return empty
	lastMsg := store.GetLastAssistantMessage(chatID)
	if lastMsg != "" {
		t.Errorf("expected empty for no history, got %q", lastMsg)
	}

	// Add messages
	store.AddUserMessage(chatID, "Question 1", IntentQuestion)
	store.AddAssistantMessage(chatID, "Answer 1")
	store.AddUserMessage(chatID, "Question 2", IntentQuestion)
	store.AddAssistantMessage(chatID, "Answer 2")

	lastMsg = store.GetLastAssistantMessage(chatID)
	if lastMsg != "Answer 2" {
		t.Errorf("expected 'Answer 2', got %q", lastMsg)
	}
}

func TestConversationStore_Clear(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "test-chat-8"

	store.AddUserMessage(chatID, "Test message", IntentChat)
	store.Clear(chatID)

	history := store.GetHistory(chatID)
	if len(history) != 0 {
		t.Errorf("expected 0 messages after Clear, got %d", len(history))
	}
}

func TestConversationStore_ClearAll(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	store.AddUserMessage("chat1", "Message 1", IntentChat)
	store.AddUserMessage("chat2", "Message 2", IntentChat)

	store.ClearAll()

	if len(store.GetHistory("chat1")) != 0 {
		t.Error("expected chat1 to be cleared")
	}
	if len(store.GetHistory("chat2")) != 0 {
		t.Error("expected chat2 to be cleared")
	}
}

func TestConversationStore_DefaultConfig(t *testing.T) {
	cfg := DefaultConversationStoreConfig()

	if cfg.MaxSize != 10 {
		t.Errorf("expected default MaxSize 10, got %d", cfg.MaxSize)
	}
	if cfg.TTL != 30*time.Minute {
		t.Errorf("expected default TTL 30m, got %v", cfg.TTL)
	}
}

func TestConversationStore_ConcurrentAccess(t *testing.T) {
	store := NewConversationStore(nil)
	defer store.Stop()

	chatID := "concurrent-chat"
	done := make(chan bool, 10)

	// Concurrent writes
	for i := 0; i < 5; i++ {
		go func(n int) {
			store.AddUserMessage(chatID, "User message", IntentChat)
			done <- true
		}(i)
		go func(n int) {
			store.AddAssistantMessage(chatID, "Assistant message")
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Concurrent reads
	for i := 0; i < 5; i++ {
		go func() {
			_ = store.GetHistory(chatID)
			_ = store.GetContextSummary(chatID, 5)
			_ = store.GetLastUserIntent(chatID)
			done <- true
		}()
	}

	for i := 0; i < 5; i++ {
		<-done
	}

	// Should not panic and have stored messages
	history := store.GetHistory(chatID)
	if len(history) == 0 {
		t.Error("expected messages in history after concurrent access")
	}
}

// Note: containsSubstr helper is defined in handler_test.go
