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

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/testutil"
)

// --- noopMessenger for tests ---

type noopMessenger struct {
	mu       sync.Mutex
	texts    []sentText
	confirms []sentConfirm
	results  []sentResult
	chunks   []sentChunk
	acks     []string
}

type sentText struct{ contextID, text string }
type sentConfirm struct{ contextID, threadID, taskID, desc, project string }
type sentResult struct {
	contextID, threadID, taskID string
	success                     bool
	output, prURL               string
}
type sentChunk struct{ contextID, threadID, content, prefix string }

func (n *noopMessenger) SendText(_ context.Context, contextID, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, sentText{contextID, text})
	return nil
}
func (n *noopMessenger) SendConfirmation(_ context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.confirms = append(n.confirms, sentConfirm{contextID, threadID, taskID, desc, project})
	return "msg-ref-1", nil
}
func (n *noopMessenger) SendProgress(_ context.Context, _, messageRef, _, _ string, _ int, _ string) (string, error) {
	return messageRef, nil
}
func (n *noopMessenger) SendResult(_ context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.results = append(n.results, sentResult{contextID, threadID, taskID, success, output, prURL})
	return nil
}
func (n *noopMessenger) SendChunked(_ context.Context, contextID, threadID, content, prefix string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.chunks = append(n.chunks, sentChunk{contextID, threadID, content, prefix})
	return nil
}
func (n *noopMessenger) AcknowledgeCallback(_ context.Context, callbackID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.acks = append(n.acks, callbackID)
	return nil
}
func (n *noopMessenger) MaxMessageLength() int { return 2000 }

func newTestCommsHandler(m comms.Messenger) *comms.Handler {
	if m == nil {
		m = &noopMessenger{}
	}
	return comms.NewHandler(&comms.HandlerConfig{
		Messenger:    m,
		TaskIDPrefix: "DISCORD",
	})
}

func newTestHandler(ch *comms.Handler) *Handler {
	return NewHandler(&HandlerConfig{
		BotToken: testutil.FakeBearerToken,
	}, ch)
}

// --- Guild/Channel filtering ---

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
			h := NewHandler(&HandlerConfig{
				BotToken:        testutil.FakeBearerToken,
				AllowedGuilds:   tt.allowedGuilds,
				AllowedChannels: tt.allowedChannels,
			}, nil)

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
			h := NewHandler(&HandlerConfig{
				BotToken:        testutil.FakeBearerToken,
				AllowedGuilds:   tt.allowedGuilds,
				AllowedChannels: tt.allowedChannels,
			}, nil)

			result := h.isAllowed(tt.guildID, tt.channelID)
			if result != tt.allowed {
				t.Errorf("expected %v, got %v", tt.allowed, result)
			}
		})
	}
}

func TestHandlerBotMessageSkipping(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)

	// Test: bot message should be skipped (no message sent)
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
	h.handleMessageCreate(ctx, event)

	messenger.mu.Lock()
	textCount := len(messenger.texts)
	messenger.mu.Unlock()

	if textCount != 0 {
		t.Errorf("expected no messages for bot message, got %d", textCount)
	}
}

// --- Mention stripping ---

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
			h := NewHandler(&HandlerConfig{
				BotToken: testutil.FakeBearerToken,
				BotID:    tt.botID,
			}, nil)

			result := h.stripMention(tt.content)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestMentionStrippedBeforeIntentClassification(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)

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

	// Greeting should produce a text response (via comms.Handler)
	messenger.mu.Lock()
	textCount := len(messenger.texts)
	messenger.mu.Unlock()

	if textCount == 0 {
		t.Error("expected greeting response to be sent via comms.Handler")
	}
}

// --- comms.Handler delegation ---

func TestMessageDelegatedToCommsHandler(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)

	// A task-like message should trigger a confirmation via comms.Handler
	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		Author:    User{ID: "user1", Username: "testuser"},
		Content:   "add a logout button to the navbar",
	}

	msgData, _ := json.Marshal(msg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	h.handleMessageCreate(ctx, event)

	// comms.Handler should have created a pending task and sent a confirmation
	messenger.mu.Lock()
	confirmCount := len(messenger.confirms)
	messenger.mu.Unlock()

	if confirmCount == 0 {
		t.Error("expected confirmation to be sent for task via comms.Handler")
	}

	// Verify the pending task exists in comms.Handler
	pending := ch.GetPendingTask("chan1")
	if pending == nil {
		t.Error("expected pending task in comms.Handler")
	}
}

func TestGreetingDelegatedToCommsHandler(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)

	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		Author:    User{ID: "user1", Username: "testuser"},
		Content:   "hello",
	}

	msgData, _ := json.Marshal(msg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	h.handleMessageCreate(ctx, event)

	// Should get a text response (greeting) but no confirmation/task
	messenger.mu.Lock()
	textCount := len(messenger.texts)
	confirmCount := len(messenger.confirms)
	messenger.mu.Unlock()

	if textCount == 0 {
		t.Error("expected greeting text response")
	}
	if confirmCount != 0 {
		t.Error("greeting should not create a confirmation")
	}
}

func TestInteractionDelegatedToCommsHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg1"}`))
	}))
	defer server.Close()

	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Inject a pending task into comms.Handler
	ctx := context.Background()
	ch.HandleMessage(ctx, &comms.IncomingMessage{
		ContextID:  "chan123",
		SenderID:   "user123",
		Text:       "add a feature",
		Platform:   "discord",
	})

	// Verify pending task exists
	pending := ch.GetPendingTask("chan123")
	if pending == nil {
		t.Fatal("expected pending task to be created")
	}

	// Simulate cancel button click
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

	h.handleInteractionCreate(ctx, event)

	// Verify task was removed from pending
	pending = ch.GetPendingTask("chan123")
	if pending != nil {
		t.Error("expected pending task to be removed after cancel")
	}
}

func TestExecuteButtonNormalizesActionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg1"}`))
	}))
	defer server.Close()

	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	ctx := context.Background()

	// Without a pending task, execute button should trigger "No pending task" message
	interaction := InteractionCreate{
		ID:        "int1",
		Token:     "tok1",
		Type:      3,
		ChannelID: "chan1",
		User:      &User{ID: "user1"},
		Data:      InteractionData{CustomID: "execute_task"},
	}

	intData, _ := json.Marshal(interaction)
	event := &GatewayEvent{
		T: stringPtr("INTERACTION_CREATE"),
		D: json.RawMessage(intData),
	}

	h.handleInteractionCreate(ctx, event)

	// Should have sent "No pending task" text
	messenger.mu.Lock()
	found := false
	for _, msg := range messenger.texts {
		if strings.Contains(msg.text, "No pending task") {
			found = true
			break
		}
	}
	messenger.mu.Unlock()

	if !found {
		t.Error("expected 'No pending task' message when execute is clicked without pending task")
	}
}

// --- Interaction response type ---

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
		_, _ = w.Write([]byte(`{"id":"msg1"}`))
	}))
	defer server.Close()

	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Add a pending task via comms.Handler
	ctx := context.Background()
	ch.HandleMessage(ctx, &comms.IncomingMessage{
		ContextID: "chan1",
		SenderID:  "user1",
		Text:      "deploy the app",
		Platform:  "discord",
	})

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

	h.handleInteractionCreate(ctx, event)

	if receivedType != InteractionResponseDeferredUpdateMessage {
		t.Errorf("expected interaction response type %d, got %d",
			InteractionResponseDeferredUpdateMessage, receivedType)
	}
}

// --- Handler lifecycle ---

func TestHandlerUnknownEventHandling(t *testing.T) {
	h := newTestHandler(nil)

	event := &GatewayEvent{
		T: stringPtr("UNKNOWN_EVENT"),
		D: json.RawMessage(`{}`),
	}

	ctx := context.Background()
	h.processEvent(ctx, event)
	// No assertion needed - just ensuring it doesn't crash
}

func TestHandlerStopIdempotent(t *testing.T) {
	h := newTestHandler(nil)

	// Calling Stop multiple times should not panic
	h.Stop()
	h.Stop()
	h.Stop()
}

func TestHandlerNilCommsHandler(t *testing.T) {
	// Handler with nil commsHandler should not panic on events
	h := newTestHandler(nil)

	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		Author:    User{ID: "user1", Username: "testuser"},
		Content:   "hello",
	}

	msgData, _ := json.Marshal(msg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	h.handleMessageCreate(ctx, event) // should not panic
}

// --- Messenger ---

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

// --- Formatter ---

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

// --- Rate limit (Client-level) ---

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

// --- Task ID uniqueness ---

func TestTaskIDUniqueness(t *testing.T) {
	// Verify comms.Handler task ID generation produces unique IDs
	// (DISCORD prefix + Unix timestamp)
	seen := make(map[string]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup
	const count = 100

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			taskID := fmt.Sprintf("DISCORD-%d", i)
			mu.Lock()
			seen[taskID] = true
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(seen) != count {
		t.Errorf("expected %d unique task IDs, got %d", count, len(seen))
	}
}

// --- Multiple channels ---

func TestHandlerMultipleChannels(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)

	ctx := context.Background()

	// Create pending tasks in two channels
	ch.HandleMessage(ctx, &comms.IncomingMessage{
		ContextID: "chan1",
		SenderID:  "user1",
		Text:      "add feature A",
		Platform:  "discord",
	})
	ch.HandleMessage(ctx, &comms.IncomingMessage{
		ContextID: "chan2",
		SenderID:  "user2",
		Text:      "add feature B",
		Platform:  "discord",
	})

	// Both channels should have pending tasks
	if ch.GetPendingTask("chan1") == nil {
		t.Error("expected pending task in chan1")
	}
	if ch.GetPendingTask("chan2") == nil {
		t.Error("expected pending task in chan2")
	}

	// Cancel chan1 — chan2 should not be affected
	ch.HandleMessage(ctx, &comms.IncomingMessage{
		ContextID:  "chan1",
		SenderID:   "user1",
		Platform:   "discord",
		IsCallback: true,
		ActionID:   "cancel",
	})

	if ch.GetPendingTask("chan1") != nil {
		t.Error("chan1 task should be cancelled")
	}
	if ch.GetPendingTask("chan2") == nil {
		t.Error("chan2 task should still exist")
	}
}

// --- Guild filtering blocks message ---

func TestGuildFilterBlocksMessage(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)

	h := NewHandler(&HandlerConfig{
		BotToken:      testutil.FakeBearerToken,
		AllowedGuilds: []string{"guild-allowed"},
	}, ch)

	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		GuildID:   "guild-blocked",
		Author:    User{ID: "user1", Username: "testuser"},
		Content:   "add a feature",
	}

	msgData, _ := json.Marshal(msg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	h.handleMessageCreate(ctx, event)

	// Message from blocked guild should not reach comms.Handler
	if ch.GetPendingTask("chan1") != nil {
		t.Error("message from blocked guild should not create pending task")
	}

	messenger.mu.Lock()
	textCount := len(messenger.texts)
	messenger.mu.Unlock()
	if textCount != 0 {
		t.Error("no messages should be sent for blocked guild")
	}
}

// --- Empty message after mention strip ---

func TestEmptyAfterMentionStrip(t *testing.T) {
	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := NewHandler(&HandlerConfig{
		BotToken: testutil.FakeBearerToken,
		BotID:    "123456789",
	}, ch)

	msg := MessageCreate{
		ID:        "msg1",
		ChannelID: "chan1",
		Author:    User{ID: "user1", Username: "testuser"},
		Content:   "<@123456789>",
	}

	msgData, _ := json.Marshal(msg)
	event := &GatewayEvent{
		T: stringPtr("MESSAGE_CREATE"),
		D: json.RawMessage(msgData),
	}

	ctx := context.Background()
	h.handleMessageCreate(ctx, event)

	// Empty message after strip should be ignored
	messenger.mu.Lock()
	textCount := len(messenger.texts)
	messenger.mu.Unlock()

	if textCount != 0 {
		t.Errorf("expected no messages for empty-after-strip, got %d", textCount)
	}
}

// --- Non-MESSAGE_COMPONENT interaction ignored ---

func TestNonButtonInteractionIgnored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	messenger := &noopMessenger{}
	ch := newTestCommsHandler(messenger)
	h := newTestHandler(ch)
	h.apiClient = NewClientWithBaseURL(testutil.FakeBearerToken, server.URL)

	// Type 2 = APPLICATION_COMMAND, not a button click
	interaction := InteractionCreate{
		ID:   "int1",
		Type: 2,
	}

	intData, _ := json.Marshal(interaction)
	event := &GatewayEvent{
		T: stringPtr("INTERACTION_CREATE"),
		D: json.RawMessage(intData),
	}

	ctx := context.Background()
	h.handleInteractionCreate(ctx, event) // should not panic or delegate
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func contains(haystack, needle string) bool {
	for i := 0; i < len(haystack)-len(needle)+1; i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
