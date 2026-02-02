package telegram

import (
	"testing"
	"time"
)

func TestRateLimiter_AllowMessage(t *testing.T) {
	config := &RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 10,
		TasksPerHour:      5,
		BurstSize:         3,
	}
	limiter := NewRateLimiter(config)

	chatID := "test-chat-123"

	// First 3 messages should be allowed (burst)
	for i := 0; i < 3; i++ {
		if !limiter.AllowMessage(chatID) {
			t.Errorf("Message %d should be allowed (burst)", i+1)
		}
	}

	// 4th message should be blocked (burst exhausted)
	if limiter.AllowMessage(chatID) {
		t.Error("Message 4 should be blocked (burst exhausted)")
	}
}

func TestRateLimiter_AllowTask(t *testing.T) {
	config := &RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 20,
		TasksPerHour:      5,
		BurstSize:         2,
	}
	limiter := NewRateLimiter(config)

	chatID := "test-chat-456"

	// First 2 tasks should be allowed (burst)
	for i := 0; i < 2; i++ {
		if !limiter.AllowTask(chatID) {
			t.Errorf("Task %d should be allowed (burst)", i+1)
		}
	}

	// 3rd task should be blocked
	if limiter.AllowTask(chatID) {
		t.Error("Task 3 should be blocked (burst exhausted)")
	}
}

func TestRateLimiter_Disabled(t *testing.T) {
	config := &RateLimitConfig{
		Enabled:           false,
		MessagesPerMinute: 1,
		TasksPerHour:      1,
		BurstSize:         1,
	}
	limiter := NewRateLimiter(config)

	chatID := "test-chat-789"

	// All requests should be allowed when disabled
	for i := 0; i < 100; i++ {
		if !limiter.AllowMessage(chatID) {
			t.Error("Message should be allowed when rate limiting is disabled")
		}
		if !limiter.AllowTask(chatID) {
			t.Error("Task should be allowed when rate limiting is disabled")
		}
	}
}

func TestRateLimiter_TokenRefill(t *testing.T) {
	config := &RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 60, // 1 per second
		TasksPerHour:      60,
		BurstSize:         1,
	}
	limiter := NewRateLimiter(config)

	chatID := "test-chat-refill"

	// Use burst
	if !limiter.AllowMessage(chatID) {
		t.Error("First message should be allowed")
	}

	// Second message should be blocked
	if limiter.AllowMessage(chatID) {
		t.Error("Second message should be blocked (burst exhausted)")
	}

	// Wait for token to refill (1 second = 1 token at 60/min)
	time.Sleep(1100 * time.Millisecond)

	// Should have ~1 token now
	if !limiter.AllowMessage(chatID) {
		t.Error("Message should be allowed after refill")
	}
}

func TestRateLimiter_PerUserIsolation(t *testing.T) {
	config := &RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 10,
		TasksPerHour:      5,
		BurstSize:         2,
	}
	limiter := NewRateLimiter(config)

	chatID1 := "user-1"
	chatID2 := "user-2"

	// Use up user1's burst
	limiter.AllowMessage(chatID1)
	limiter.AllowMessage(chatID1)

	// User1 should be blocked
	if limiter.AllowMessage(chatID1) {
		t.Error("User1 should be blocked")
	}

	// User2 should still have full burst
	if !limiter.AllowMessage(chatID2) {
		t.Error("User2 should have full burst")
	}
	if !limiter.AllowMessage(chatID2) {
		t.Error("User2 should have full burst")
	}
}

func TestRateLimiter_GetRemaining(t *testing.T) {
	config := &RateLimitConfig{
		Enabled:           true,
		MessagesPerMinute: 20,
		TasksPerHour:      10,
		BurstSize:         5,
	}
	limiter := NewRateLimiter(config)

	chatID := "test-remaining"

	// Initial should be burst size
	if remaining := limiter.GetRemainingMessages(chatID); remaining != 5 {
		t.Errorf("Expected 5 remaining messages, got %d", remaining)
	}

	// Use one
	limiter.AllowMessage(chatID)

	if remaining := limiter.GetRemainingMessages(chatID); remaining != 4 {
		t.Errorf("Expected 4 remaining messages, got %d", remaining)
	}
}

func TestRateLimiter_Cleanup(t *testing.T) {
	config := DefaultRateLimitConfig()
	limiter := NewRateLimiter(config)

	// Create some buckets
	limiter.AllowMessage("chat-1")
	limiter.AllowMessage("chat-2")

	// Verify buckets exist
	limiter.mu.Lock()
	initialCount := len(limiter.buckets)
	limiter.mu.Unlock()

	if initialCount != 2 {
		t.Errorf("Expected 2 buckets, got %d", initialCount)
	}

	// Cleanup with max age of 0 should remove all buckets
	limiter.Cleanup(0)

	limiter.mu.Lock()
	finalCount := len(limiter.buckets)
	limiter.mu.Unlock()

	if finalCount != 0 {
		t.Errorf("Expected 0 buckets after cleanup, got %d", finalCount)
	}
}
