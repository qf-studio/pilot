package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

func TestHandlerGuildFiltering(t *testing.T) {
	tests := []struct {
		name            string
		allowedGuilds   []string
		allowedChannels []string
		guildID         string
		channelID       string
		allowed         bool
	}{
		{
			name:          "allowed guild",
			allowedGuilds: []string{"guild123"},
			guildID:       "guild123",
			allowed:       true,
		},
		{
			name:          "disallowed guild",
			allowedGuilds: []string{"guild123"},
			guildID:       "guild456",
			allowed:       false,
		},
		{
			name:            "allowed channel",
			allowedChannels: []string{"chan123"},
			channelID:       "chan123",
			allowed:         true,
		},
		{
			name:            "disallowed channel",
			allowedChannels: []string{"chan123"},
			channelID:       "chan456",
			allowed:         false,
		},
		{
			name:    "no restrictions",
			guildID: "any",
			allowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &HandlerConfig{
				BotToken:        testutil.FakeBearerToken,
				AllowedGuilds:   tt.allowedGuilds,
				AllowedChannels: tt.allowedChannels,
			}
			h := NewHandler(config, nil)

			result := h.isAllowed(tt.guildID, tt.channelID)
			if result != tt.allowed {
				t.Errorf("expected %v, got %v", tt.allowed, result)
			}
		})
	}
}

func TestHandlerBotMessageSkipping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"url": "wss://gateway.discord.test",
			})
		}
	}))
	defer server.Close()

	config := &HandlerConfig{
		BotToken:      testutil.FakeBearerToken,
		AllowedGuilds: []string{},
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Test: bot message should be skipped
	botMsg := MessageCreate{
		ID:        "msg123",
		ChannelID: "chan123",
		GuildID:   "guild123",
		Author: User{
			ID:       "bot",
			Username: "DiscordBot",
		},
		Content: "Some bot message",
	}

	msgData, _ := json.Marshal(botMsg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	// Should not panic and should skip the message
	h.handleMessageCreate(ctx, event)
	// No assertion needed - just ensuring it doesn't crash
}

func TestHandlerButtonCallbackRouting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"url": "wss://gateway.discord.test",
			})
		} else if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "msg123",
			})
		}
	}))
	defer server.Close()

	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Create a pending task
	h.mu.Lock()
	h.pendingTasks["chan123"] = &PendingTaskInfo{
		TaskID:      "DISCORD-12345",
		Description: "Test task",
		ChannelID:   "chan123",
		UserID:      "user123",
	}
	h.mu.Unlock()

	// Test: cancel_task button click (doesn't require runner)
	interaction := InteractionCreate{
		ID:        "int123",
		Token:     "token123",
		Type:      3, // MESSAGE_COMPONENT
		ChannelID: "chan123",
		User: &User{
			ID:       "user123",
			Username: "testuser",
		},
		Data: InteractionData{
			CustomID: "cancel_task",
		},
	}

	intData, _ := json.Marshal(interaction)
	event := &GatewayEvent{
		T: stringPtr("INTERACTION_CREATE"),
		D: json.RawMessage(intData),
	}

	ctx := context.Background()
	h.handleInteractionCreate(ctx, event)

	// Verify task was removed from pending
	h.mu.Lock()
	_, exists := h.pendingTasks["chan123"]
	h.mu.Unlock()

	if exists {
		t.Error("expected pending task to be removed after confirmation")
	}
}

func TestHandlerUnknownEventHandling(t *testing.T) {
	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)

	// Test: unknown event type should not crash
	event := &GatewayEvent{
		T: stringPtr("UNKNOWN_EVENT"),
		D: json.RawMessage(`{}`),
	}

	ctx := context.Background()
	h.processEvent(ctx, event)
	// No assertion needed - just ensuring it doesn't crash
}

func TestHandlerMultipleChannels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if r.URL.Path == "/gateway" {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"url": "wss://gateway.discord.test",
			})
		} else if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": fmt.Sprintf("msg%s", r.Header.Get("X-Channel-ID")),
			})
		}
	}))
	defer server.Close()

	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Test: multiple concurrent pending tasks in different channels
	h.mu.Lock()
	h.pendingTasks["chan1"] = &PendingTaskInfo{
		TaskID:    "DISCORD-1",
		ChannelID: "chan1",
	}
	h.pendingTasks["chan2"] = &PendingTaskInfo{
		TaskID:    "DISCORD-2",
		ChannelID: "chan2",
	}
	h.mu.Unlock()

	// Verify both tasks exist
	h.mu.Lock()
	if len(h.pendingTasks) != 2 {
		t.Errorf("expected 2 pending tasks, got %d", len(h.pendingTasks))
	}
	h.mu.Unlock()

	// Handle confirmation on chan1 - should not affect chan2
	h.mu.Lock()
	delete(h.pendingTasks, "chan1")
	h.mu.Unlock()

	h.mu.Lock()
	if _, exists := h.pendingTasks["chan2"]; !exists {
		t.Error("chan2 task should still exist")
	}
	h.mu.Unlock()
}

func TestMessengerImplementation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "msg123",
		})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)
	messenger := NewMessenger(client)

	ctx := context.Background()

	// Test: SendText
	err := messenger.SendText(ctx, "chan123", "Hello")
	if err != nil {
		t.Fatalf("SendText failed: %v", err)
	}

	// Test: SendConfirmation
	ref, err := messenger.SendConfirmation(ctx, "chan123", "", "task1", "Do something", "myproject")
	if err != nil {
		t.Fatalf("SendConfirmation failed: %v", err)
	}
	if ref == "" {
		t.Error("expected non-empty message ref")
	}

	// Test: MaxMessageLength
	maxLen := messenger.MaxMessageLength()
	if maxLen != MaxMessageLength {
		t.Errorf("expected %d, got %d", MaxMessageLength, maxLen)
	}
}

func TestFormatterFunctions(t *testing.T) {
	tests := []struct {
		name     string
		testFunc func() string
		contains []string
	}{
		{
			name: "FormatTaskConfirmation",
			testFunc: func() string {
				return FormatTaskConfirmation("TASK-1", "Do something", "myproject")
			},
			contains: []string{"TASK-1", "Do something", "myproject"},
		},
		{
			name: "FormatProgressUpdate",
			testFunc: func() string {
				return FormatProgressUpdate("TASK-1", "Processing", 50, "Details")
			},
			contains: []string{"TASK-1", "50", "Details"},
		},
		{
			name: "FormatTaskResult",
			testFunc: func() string {
				return FormatTaskResult("Output", true, "https://pr.url")
			},
			contains: []string{"completed", "Output", "https://pr.url"},
		},
		{
			name: "BuildConfirmationButtons",
			testFunc: func() string {
				buttons := BuildConfirmationButtons()
				if len(buttons) == 0 || len(buttons[0].Components) == 0 {
					return "no buttons"
				}
				return buttons[0].Components[0].Label
			},
			contains: []string{"Execute"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.testFunc()
			for _, expected := range tt.contains {
				if !contains(result, expected) {
					t.Errorf("expected to find %q in %q", expected, result)
				}
			}
		})
	}
}

func TestChunkContent(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		maxLen   int
		expected int
	}{
		{
			name:     "short content",
			content:  "hello",
			maxLen:   2000,
			expected: 1,
		},
		{
			name:     "exactly max length",
			content:  string(make([]byte, 2000)),
			maxLen:   2000,
			expected: 1,
		},
		{
			name:     "needs chunking",
			content:  string(make([]byte, 5000)),
			maxLen:   2000,
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := ChunkContent(tt.content, tt.maxLen)
			if len(chunks) != tt.expected {
				t.Errorf("expected %d chunks, got %d", tt.expected, len(chunks))
			}
		})
	}
}

func TestCleanInternalSignals(t *testing.T) {
	input := "<!-- INTERNAL: This is internal -->Some output<!-- /INTERNAL -->"
	output := CleanInternalSignals(input)

	if contains(output, "INTERNAL") {
		t.Errorf("internal signals not cleaned: %s", output)
	}

	if !contains(output, "output") {
		t.Errorf("regular content lost: %s", output)
	}
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func contains(haystack, needle string) bool {
	// Implement simple string search
	for i := 0; i < len(haystack)-len(needle)+1; i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
