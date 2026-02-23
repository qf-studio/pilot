package telegram

import (
	"sync"
	"testing"
	"time"
)

func TestConversationStore_AddAndGet(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  10,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	store.Add("chat-1", "user", "hello")
	store.Add("chat-1", "assistant", "hi there")

	msgs := store.Get("chat-1")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("msgs[0] = %+v, want {Role:user Content:hello}", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Errorf("msgs[1] = %+v, want {Role:assistant Content:hi there}", msgs[1])
	}
}

func TestConversationStore_GetEmpty(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  10,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	msgs := store.Get("nonexistent")
	if msgs != nil {
		t.Errorf("expected nil for nonexistent chat, got %v", msgs)
	}
}

func TestConversationStore_MaxSize(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  3,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	store.Add("chat-1", "user", "msg-1")
	store.Add("chat-1", "assistant", "msg-2")
	store.Add("chat-1", "user", "msg-3")
	store.Add("chat-1", "assistant", "msg-4")
	store.Add("chat-1", "user", "msg-5")

	msgs := store.Get("chat-1")
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (maxSize), got %d", len(msgs))
	}
	// Should keep the last 3 messages
	if msgs[0].Content != "msg-3" {
		t.Errorf("msgs[0].Content = %q, want %q", msgs[0].Content, "msg-3")
	}
	if msgs[1].Content != "msg-4" {
		t.Errorf("msgs[1].Content = %q, want %q", msgs[1].Content, "msg-4")
	}
	if msgs[2].Content != "msg-5" {
		t.Errorf("msgs[2].Content = %q, want %q", msgs[2].Content, "msg-5")
	}
}

func TestConversationStore_SeparateChats(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  10,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	store.Add("chat-A", "user", "hello A")
	store.Add("chat-B", "user", "hello B")

	msgsA := store.Get("chat-A")
	msgsB := store.Get("chat-B")

	if len(msgsA) != 1 || msgsA[0].Content != "hello A" {
		t.Errorf("chat-A: got %v, want [{user hello A}]", msgsA)
	}
	if len(msgsB) != 1 || msgsB[0].Content != "hello B" {
		t.Errorf("chat-B: got %v, want [{user hello B}]", msgsB)
	}
}

func TestConversationStore_GetReturnsCopy(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  10,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	store.Add("chat-1", "user", "original")

	msgs := store.Get("chat-1")
	msgs[0].Content = "modified"

	// Original should be unchanged
	original := store.Get("chat-1")
	if original[0].Content != "original" {
		t.Errorf("Get() did not return a copy; original was modified to %q", original[0].Content)
	}
}

func TestConversationStore_ConcurrentAccess(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  100,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.Add("chat-1", "user", "concurrent msg")
		}()
		go func() {
			defer wg.Done()
			_ = store.Get("chat-1")
		}()
	}
	wg.Wait()

	msgs := store.Get("chat-1")
	if len(msgs) != 50 {
		t.Errorf("expected 50 messages after concurrent adds, got %d", len(msgs))
	}
}

func TestConversationStore_LastSeenUpdated(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  10,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	before := time.Now()
	store.Add("chat-1", "user", "hello")
	after := time.Now()

	store.mu.RLock()
	lastSeen := store.lastSeen["chat-1"]
	store.mu.RUnlock()

	if lastSeen.Before(before) || lastSeen.After(after) {
		t.Errorf("lastSeen = %v, expected between %v and %v", lastSeen, before, after)
	}
}

func TestConversationStore_MaxSizeOne(t *testing.T) {
	store := &ConversationStore{
		history:  make(map[string][]ConversationMessage),
		maxSize:  1,
		ttl:      1 * time.Hour,
		lastSeen: make(map[string]time.Time),
	}

	store.Add("chat-1", "user", "first")
	store.Add("chat-1", "user", "second")

	msgs := store.Get("chat-1")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (maxSize=1), got %d", len(msgs))
	}
	if msgs[0].Content != "second" {
		t.Errorf("expected last message %q, got %q", "second", msgs[0].Content)
	}
}
