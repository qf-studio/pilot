package telegram

import (
	"testing"
	"time"
)

func TestNewConversationStore(t *testing.T) {
	store := NewConversationStore(10, 30*time.Minute)

	if store == nil {
		t.Fatal("NewConversationStore returned nil")
	}
	if store.maxSize != 10 {
		t.Errorf("maxSize = %d, want %d", store.maxSize, 10)
	}
	if store.ttl != 30*time.Minute {
		t.Errorf("ttl = %v, want %v", store.ttl, 30*time.Minute)
	}
	if store.history == nil {
		t.Error("history map is nil")
	}
	if store.lastSeen == nil {
		t.Error("lastSeen map is nil")
	}
}

func TestConversationStoreAdd(t *testing.T) {
	store := NewConversationStore(10, 30*time.Minute)

	store.Add("chat1", "user", "Hello")
	store.Add("chat1", "assistant", "Hi there!")

	messages := store.Get("chat1")
	if len(messages) != 2 {
		t.Errorf("messages length = %d, want 2", len(messages))
	}

	if messages[0].Role != "user" || messages[0].Content != "Hello" {
		t.Errorf("messages[0] = %+v, want {Role: user, Content: Hello}", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "Hi there!" {
		t.Errorf("messages[1] = %+v, want {Role: assistant, Content: Hi there!}", messages[1])
	}
}

func TestConversationStoreMaxSize(t *testing.T) {
	store := NewConversationStore(3, 30*time.Minute)

	// Add 5 messages, should only keep last 3
	store.Add("chat1", "user", "Message 1")
	store.Add("chat1", "assistant", "Response 1")
	store.Add("chat1", "user", "Message 2")
	store.Add("chat1", "assistant", "Response 2")
	store.Add("chat1", "user", "Message 3")

	messages := store.Get("chat1")
	if len(messages) != 3 {
		t.Errorf("messages length = %d, want 3", len(messages))
	}

	// Should have the last 3 messages
	if messages[0].Content != "Message 2" {
		t.Errorf("messages[0].Content = %q, want %q", messages[0].Content, "Message 2")
	}
	if messages[1].Content != "Response 2" {
		t.Errorf("messages[1].Content = %q, want %q", messages[1].Content, "Response 2")
	}
	if messages[2].Content != "Message 3" {
		t.Errorf("messages[2].Content = %q, want %q", messages[2].Content, "Message 3")
	}
}

func TestConversationStoreGetEmpty(t *testing.T) {
	store := NewConversationStore(10, 30*time.Minute)

	messages := store.Get("nonexistent")
	if messages != nil {
		t.Errorf("messages = %v, want nil for nonexistent chat", messages)
	}
}

func TestConversationStoreGetReturnsCopy(t *testing.T) {
	store := NewConversationStore(10, 30*time.Minute)

	store.Add("chat1", "user", "Hello")

	// Get messages and modify the returned slice
	messages := store.Get("chat1")
	messages[0].Content = "Modified"

	// Get again - original should be unchanged
	original := store.Get("chat1")
	if original[0].Content != "Hello" {
		t.Errorf("original was modified: Content = %q, want %q", original[0].Content, "Hello")
	}
}

func TestConversationStoreMultipleChats(t *testing.T) {
	store := NewConversationStore(10, 30*time.Minute)

	store.Add("chat1", "user", "Hello from chat1")
	store.Add("chat2", "user", "Hello from chat2")

	chat1 := store.Get("chat1")
	chat2 := store.Get("chat2")

	if len(chat1) != 1 || chat1[0].Content != "Hello from chat1" {
		t.Errorf("chat1 messages incorrect: %+v", chat1)
	}
	if len(chat2) != 1 || chat2[0].Content != "Hello from chat2" {
		t.Errorf("chat2 messages incorrect: %+v", chat2)
	}
}

func TestConversationStoreConcurrency(t *testing.T) {
	store := NewConversationStore(100, 30*time.Minute)

	// Spawn multiple goroutines to add and read concurrently
	done := make(chan bool)

	// Writers
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				store.Add("chat1", "user", "message")
			}
			done <- true
		}(i)
	}

	// Readers
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				_ = store.Get("chat1")
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Should not panic or race - this test mainly checks for data races
}

func TestConversationStoreLastSeenUpdated(t *testing.T) {
	store := NewConversationStore(10, 30*time.Minute)

	before := time.Now()
	store.Add("chat1", "user", "Hello")
	after := time.Now()

	store.mu.RLock()
	lastSeen := store.lastSeen["chat1"]
	store.mu.RUnlock()

	if lastSeen.Before(before) || lastSeen.After(after) {
		t.Errorf("lastSeen = %v, want between %v and %v", lastSeen, before, after)
	}
}

func TestConversationMessage(t *testing.T) {
	msg := ConversationMessage{
		Role:    "user",
		Content: "Hello world",
	}

	if msg.Role != "user" {
		t.Errorf("Role = %q, want %q", msg.Role, "user")
	}
	if msg.Content != "Hello world" {
		t.Errorf("Content = %q, want %q", msg.Content, "Hello world")
	}
}
