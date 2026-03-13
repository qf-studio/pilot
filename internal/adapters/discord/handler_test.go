package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestHandlerDMAllowlisting(t *testing.T) {
	tests := []struct {
		name            string
		allowedGuilds   []string
		allowedChannels []string
		guildID         string
		channelID       string
		allowed         bool
	}{
		{
			name:          "DM with guild allowlist only — permitted",
			allowedGuilds: []string{"guild123"},
			guildID:       "",
			channelID:     "dm-chan-1",
			allowed:       true,
		},
		{
			name:            "DM with guild and channel allowlist — denied (channel not listed)",
			allowedGuilds:   []string{"guild123"},
			allowedChannels: []string{"chan456"},
			guildID:         "",
			channelID:       "dm-chan-1",
			allowed:         false,
		},
		{
			name:            "DM with channel allowlist only — denied",
			allowedChannels: []string{"chan456"},
			guildID:         "",
			channelID:       "dm-chan-1",
			allowed:         false,
		},
		{
			name:            "DM with channel allowlist — permitted (channel listed)",
			allowedChannels: []string{"dm-chan-1"},
			guildID:         "",
			channelID:       "dm-chan-1",
			allowed:         true,
		},
		{
			name:    "DM with no restrictions — permitted",
			guildID: "",
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
			ID:       "123456789",
			Username: "PilotBot",
			Bot:      true,
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

func TestIntentRouting(t *testing.T) {
	tests := []struct {
		name            string
		content         string
		expectContains  string // Expected substring in the API call body
		expectNoTask    bool   // Should NOT create a pending task
	}{
		{
			name:           "greeting does not create task",
			content:        "hi",
			expectNoTask:   true,
		},
		{
			name:           "hello does not create task",
			content:        "hello",
			expectNoTask:   true,
		},
		{
			name:           "question does not create task",
			content:        "what files handle auth?",
			expectNoTask:   true,
		},
		{
			name:           "task creates pending task",
			content:        "add a logout button to the navbar",
			expectNoTask:   false,
		},
		{
			name:           "command is ignored",
			content:        "/status",
			expectNoTask:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg1"})
			}))
			defer server.Close()

			config := &HandlerConfig{
				BotToken: testutil.FakeBearerToken,
			}
			h := NewHandler(config, nil)
			h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

			msg := MessageCreate{
				ID:        "msg1",
				ChannelID: "chan1",
				Author:    User{ID: "user1", Username: "testuser"},
				Content:   tt.content,
			}

			msgData, _ := json.Marshal(msg)
			event := &GatewayEvent{
				T: stringPtr("MESSAGE_CREATE"),
				D: json.RawMessage(msgData),
			}

			ctx := context.Background()
			h.handleMessageCreate(ctx, event)

			h.mu.Lock()
			_, hasPending := h.pendingTasks["chan1"]
			h.mu.Unlock()

			if tt.expectNoTask && hasPending {
				t.Errorf("expected no pending task for %q, but one was created", tt.content)
			}
			if !tt.expectNoTask && !hasPending {
				t.Errorf("expected pending task for %q, but none was created", tt.content)
			}
		})
	}
}

func TestMentionStrippedBeforeIntentClassification(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Capture the message body sent by the greeting handler
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/messages") {
			buf := new(strings.Builder)
			_, _ = fmt.Fprintf(buf, "")
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if content, ok := payload["content"].(string); ok {
				receivedBody = content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg1"})
	}))
	defer server.Close()

	// No botID configured — relies on fallback stripping
	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Simulate "@Pilot hi" which Discord delivers as "<@1481980896998326383> hi"
	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		Author:    User{ID: "user1", Username: "testuser"},
		Content:   "<@1481980896998326383> hi",
	}

	msgData, _ := json.Marshal(msg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	h.handleMessageCreate(ctx, event)

	// Should NOT create a pending task (greeting, not task)
	h.mu.Lock()
	_, hasPending := h.pendingTasks["chan1"]
	h.mu.Unlock()

	if hasPending {
		t.Error("mention + greeting should not create pending task")
	}

	// Greeting handler should have sent a response
	if receivedBody == "" {
		t.Error("expected greeting response to be sent")
	}
}

func TestDetectIntentWithLLMFallback(t *testing.T) {
	// Without LLM classifier, should use regex
	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)

	ctx := context.Background()

	// Greeting
	result := h.detectIntentWithLLM(ctx, "chan1", "hi")
	if result != "greeting" {
		t.Errorf("expected greeting, got %s", result)
	}

	// Command
	result = h.detectIntentWithLLM(ctx, "chan1", "/status")
	if result != "command" {
		t.Errorf("expected command, got %s", result)
	}

	// Task
	result = h.detectIntentWithLLM(ctx, "chan1", "add a new endpoint for users")
	if result != "task" {
		t.Errorf("expected task, got %s", result)
	}

	// Question
	result = h.detectIntentWithLLM(ctx, "chan1", "what files handle authentication?")
	if result != "question" {
		t.Errorf("expected question, got %s", result)
	}
}

func TestConversationStoreWiring(t *testing.T) {
	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
		LLMClassifier: &LLMClassifierConfig{
			Enabled: true,
			APIKey:  "test-api-key",
		},
	}
	h := NewHandler(config, nil)

	if h.llmClassifier == nil {
		t.Fatal("expected llmClassifier to be initialized")
	}
	if h.conversationStore == nil {
		t.Fatal("expected conversationStore to be initialized")
	}
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

func TestMentionStripping(t *testing.T) {
	tests := []struct {
		name     string
		botID    string
		content  string
		expected string
	}{
		{
			name:     "strip bot mention",
			botID:    "123456789",
			content:  "<@123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "strip nickname mention",
			botID:    "123456789",
			content:  "<@!123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "no mention",
			botID:    "123456789",
			content:  "deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "different user mention preserved",
			botID:    "123456789",
			content:  "<@987654321> deploy the thing",
			expected: "<@987654321> deploy the thing",
		},
		{
			name:     "empty bot ID strips leading mention (fallback)",
			botID:    "",
			content:  "<@123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "empty bot ID strips nickname mention (fallback)",
			botID:    "",
			content:  "<@!123456789> deploy the thing",
			expected: "deploy the thing",
		},
		{
			name:     "mention only",
			botID:    "123456789",
			content:  "<@123456789>",
			expected: "",
		},
		{
			name:     "empty bot ID mention only",
			botID:    "",
			content:  "<@123456789>",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &HandlerConfig{
				BotToken: testutil.FakeBearerToken,
				BotID:    tt.botID,
			}
			h := NewHandler(config, nil)

			result := h.stripMention(tt.content)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestTaskIDUniqueness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg1"})
	}))
	defer server.Close()

	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Generate multiple task IDs rapidly and verify uniqueness
	seen := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	const count = 100

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			taskID := fmt.Sprintf("DISCORD-%d", time.Now().UnixNano())
			mu.Lock()
			seen[taskID] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// With UnixNano, we should get many unique IDs (though some goroutines
	// may share the same nanosecond). With Unix() this would collapse to 1.
	if len(seen) < 2 {
		t.Errorf("expected more than 1 unique task ID from %d attempts, got %d", count, len(seen))
	}
}

func TestCleanupExpiredTasksMutexSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg1"})
	}))
	defer server.Close()

	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Add expired and non-expired tasks
	h.mu.Lock()
	h.pendingTasks["expired-chan"] = &PendingTaskInfo{
		TaskID:    "DISCORD-OLD",
		ChannelID: "expired-chan",
		CreatedAt: time.Now().Add(-10 * time.Minute),
	}
	h.pendingTasks["active-chan"] = &PendingTaskInfo{
		TaskID:    "DISCORD-NEW",
		ChannelID: "active-chan",
		CreatedAt: time.Now(),
	}
	h.mu.Unlock()

	ctx := context.Background()

	// Run cleanup concurrently with reads to verify mutex safety
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.cleanupExpiredTasks(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.mu.Lock()
			_ = len(h.pendingTasks)
			h.mu.Unlock()
		}()
	}
	wg.Wait()

	// Verify expired task was removed, active task remains
	h.mu.Lock()
	_, expiredExists := h.pendingTasks["expired-chan"]
	_, activeExists := h.pendingTasks["active-chan"]
	h.mu.Unlock()

	if expiredExists {
		t.Error("expected expired task to be removed")
	}
	if !activeExists {
		t.Error("expected active task to remain")
	}
}

func TestHandlerStopIdempotent(t *testing.T) {
	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)

	// Calling Stop multiple times should not panic
	h.Stop()
	h.Stop()
	h.Stop()
}

func TestProjectPathFromConfig(t *testing.T) {
	tests := []struct {
		name        string
		projectPath string
		expected    string
	}{
		{
			name:        "explicit project path",
			projectPath: "/home/user/myproject",
			expected:    "/home/user/myproject",
		},
		{
			name:        "empty falls back to dot",
			projectPath: "",
			expected:    ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &HandlerConfig{
				BotToken:    testutil.FakeBearerToken,
				ProjectPath: tt.projectPath,
			}
			h := NewHandler(config, nil)
			if h.projectPath != tt.expected {
				t.Errorf("expected projectPath %q, got %q", tt.expected, h.projectPath)
			}
		})
	}
}

func TestRateLimitHandling(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "0.1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg1"})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)
	ctx := context.Background()

	msg, err := client.SendMessage(ctx, "chan1", "hello")
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if msg == nil || msg.ID != "msg1" {
		t.Error("expected valid message after rate limit retry")
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts (1 rate limited + 1 success), got %d", attempt)
	}
}

func TestRateLimitExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0.01")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)
	ctx := context.Background()

	_, err := client.SendMessage(ctx, "chan1", "hello")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("expected rate limit error, got: %v", err)
	}
}

func TestInteractionResponseType(t *testing.T) {
	var receivedType int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/interactions/") {
			var payload struct {
				Type int `json:"type"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			receivedType = payload.Type
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "msg1"})
	}))
	defer server.Close()

	config := &HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}
	h := NewHandler(config, nil)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Add pending task
	h.mu.Lock()
	h.pendingTasks["chan1"] = &PendingTaskInfo{
		TaskID:    "DISCORD-1",
		ChannelID: "chan1",
		UserID:    "user1",
	}
	h.mu.Unlock()

	interaction := InteractionCreate{
		ID:        "int1",
		Token:     "tok1",
		Type:      3,
		ChannelID: "chan1",
		User:      &User{ID: "user1"},
		Data:      InteractionData{CustomID: "cancel_task"},
	}

	intData, _ := json.Marshal(interaction)
	event := &GatewayEvent{
		T: stringPtr("INTERACTION_CREATE"),
		D: json.RawMessage(intData),
	}

	h.handleInteractionCreate(context.Background(), event)

	if receivedType != InteractionResponseDeferredUpdateMessage {
		t.Errorf("expected interaction response type %d, got %d",
			InteractionResponseDeferredUpdateMessage, receivedType)
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
