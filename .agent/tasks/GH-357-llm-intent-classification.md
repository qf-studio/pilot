# GH-357: LLM-based Intent Classification for Telegram

**Status**: 📋 Ready for Pilot
**Created**: 2026-02-03
**Previous Attempt**: GH-356 (reverted - incomplete implementation)

---

## Context

**Problem**:
Current regex-based intent detection in `internal/adapters/telegram/intent.go` is brittle. Messages like "What do you think about adding a logout button?" get misclassified as Task instead of Chat because they contain "add".

**Goal**:
Use Claude Haiku to classify intent with conversation context. Maintain regex as fast fallback.

**Why it failed last time (GH-356)**:
1. Handler methods called but not implemented (e.g., `handleGreetingWithHistory()` didn't exist)
2. Config not passed from `cmd/pilot/main.go` to `HandlerConfig`
3. `--no-pr` flag ignored
4. Regex still used, LLM classifier never called

---

## Implementation Plan

### Phase 1: Add Anthropic API Client

**File**: `internal/adapters/telegram/anthropic.go` (NEW)

```go
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// AnthropicClient provides direct access to Claude API for fast classification
type AnthropicClient struct {
	apiKey     string
	httpClient *http.Client
	model      string
}

// NewAnthropicClient creates a new Anthropic API client
func NewAnthropicClient(apiKey string) *AnthropicClient {
	return &AnthropicClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 5 * time.Second, // Fast timeout for classification
		},
		model: "claude-3-5-haiku-20241022",
	}
}

// ClassifyRequest is the request payload for intent classification
type ClassifyRequest struct {
	Messages []ConversationMessage `json:"messages"`
}

// ConversationMessage represents a message in the conversation
type ConversationMessage struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// ClassifyResponse is the response from the classification API
type ClassifyResponse struct {
	Intent     string  `json:"intent"`
	Confidence float64 `json:"confidence"`
}

// Classify determines the intent of a message using Claude Haiku
func (c *AnthropicClient) Classify(ctx context.Context, messages []ConversationMessage, currentMessage string) (Intent, error) {
	// Build the classification prompt
	systemPrompt := `You are an intent classifier for a coding assistant bot. Classify the user's message into exactly one of these intents:

- command: Message starts with /
- greeting: Simple greeting like "hi", "hello", "hey"
- research: Requests for analysis/research (e.g., "research how X works", "analyze the auth flow")
- planning: Requests for implementation plans (e.g., "plan how to add X", "design a solution for Y")
- question: Questions about code/project (e.g., "what files handle auth?", "how does X work?")
- chat: Conversational/opinion-seeking (e.g., "what do you think about...", "should I...")
- task: Requests to make changes (e.g., "add a button", "fix the bug", "implement feature X")

IMPORTANT:
- "What do you think about adding X?" is CHAT (asking opinion), not TASK
- "Add X to the project" is TASK (direct instruction)
- Questions that don't require code changes are QUESTION
- Be conservative: when in doubt between task and chat, prefer chat

Respond with JSON only: {"intent": "...", "confidence": 0.0-1.0}`

	// Build messages array for API
	apiMessages := []map[string]string{}

	// Add conversation history (last 5 messages for context)
	historyStart := 0
	if len(messages) > 5 {
		historyStart = len(messages) - 5
	}
	for _, msg := range messages[historyStart:] {
		apiMessages = append(apiMessages, map[string]string{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// Add the current message to classify
	apiMessages = append(apiMessages, map[string]string{
		"role":    "user",
		"content": fmt.Sprintf("Classify this message: %s", currentMessage),
	})

	// Build request body
	requestBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": 100,
		"system":     systemPrompt,
		"messages":   apiMessages,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	// Make API request
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	// Parse response
	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response from API")
	}

	// Parse the JSON response
	var classifyResp ClassifyResponse
	if err := json.Unmarshal([]byte(apiResp.Content[0].Text), &classifyResp); err != nil {
		return "", fmt.Errorf("failed to parse classification: %w", err)
	}

	// Map to Intent type
	switch classifyResp.Intent {
	case "command":
		return IntentCommand, nil
	case "greeting":
		return IntentGreeting, nil
	case "research":
		return IntentResearch, nil
	case "planning":
		return IntentPlanning, nil
	case "question":
		return IntentQuestion, nil
	case "chat":
		return IntentChat, nil
	case "task":
		return IntentTask, nil
	default:
		return IntentTask, nil // Default fallback
	}
}
```

---

### Phase 2: Add Conversation History Store

**File**: `internal/adapters/telegram/conversation.go` (NEW)

```go
package telegram

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
```

---

### Phase 3: Update Config Structs

**File**: `internal/adapters/telegram/notifier.go`

Add to `Config` struct (line ~11-20):

```go
type Config struct {
	Enabled       bool                  `yaml:"enabled"`
	BotToken      string                `yaml:"bot_token"`
	ChatID        string                `yaml:"chat_id"`
	Polling       bool                  `yaml:"polling"`
	AllowedIDs    []int64               `yaml:"allowed_ids"`
	PlainTextMode bool                  `yaml:"plain_text_mode"`
	Transcription *transcription.Config `yaml:"transcription"`
	RateLimit     *RateLimitConfig      `yaml:"rate_limit"`
	// NEW: LLM intent classification config
	LLMClassifier *LLMClassifierConfig  `yaml:"llm_classifier"`
}

// LLMClassifierConfig configures LLM-based intent classification
type LLMClassifierConfig struct {
	Enabled        bool          `yaml:"enabled"`         // Enable LLM classification (default: false)
	APIKey         string        `yaml:"api_key"`         // Anthropic API key (falls back to ANTHROPIC_API_KEY env)
	TimeoutSeconds int           `yaml:"timeout_seconds"` // Timeout for classification (default: 2)
	HistorySize    int           `yaml:"history_size"`    // Messages to keep per chat (default: 10)
	HistoryTTL     time.Duration `yaml:"history_ttl"`     // TTL for conversation history (default: 30m)
}
```

---

### Phase 4: Update HandlerConfig and Handler

**File**: `internal/adapters/telegram/handler.go`

#### 4a. Update HandlerConfig struct (line ~62-72):

```go
type HandlerConfig struct {
	BotToken      string
	ProjectPath   string
	Projects      ProjectSource
	AllowedIDs    []int64
	Transcription *transcription.Config
	Store         *memory.Store
	PlainTextMode bool
	RateLimit     *RateLimitConfig
	// NEW: LLM classifier config
	LLMClassifier *LLMClassifierConfig
}
```

#### 4b. Update Handler struct (line ~40-60):

```go
type Handler struct {
	client           *Client
	runner           *executor.Runner
	projects         ProjectSource
	projectPath      string
	activeProject    map[string]string
	allowedIDs       map[int64]bool
	offset           int64
	pendingTasks     map[string]*PendingTask
	runningTasks     map[string]*RunningTask
	mu               sync.Mutex
	stopCh           chan struct{}
	wg               sync.WaitGroup
	transcriber      *transcription.Service
	transcriptionErr error
	store            *memory.Store
	cmdHandler       *CommandHandler
	plainTextMode    bool
	rateLimiter      *RateLimiter
	// NEW: LLM intent classifier
	llmClassifier    *AnthropicClient
	conversationStore *ConversationStore
}
```

#### 4c. Update NewHandler function (after line ~126):

```go
	// Initialize LLM classifier if configured
	if config.LLMClassifier != nil && config.LLMClassifier.Enabled {
		apiKey := config.LLMClassifier.APIKey
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if apiKey != "" {
			h.llmClassifier = NewAnthropicClient(apiKey)

			// Set up conversation store with defaults
			historySize := 10
			if config.LLMClassifier.HistorySize > 0 {
				historySize = config.LLMClassifier.HistorySize
			}
			historyTTL := 30 * time.Minute
			if config.LLMClassifier.HistoryTTL > 0 {
				historyTTL = config.LLMClassifier.HistoryTTL
			}
			h.conversationStore = NewConversationStore(historySize, historyTTL)

			logging.WithComponent("telegram").Info("LLM intent classifier enabled",
				slog.String("model", "claude-3-5-haiku"))
		} else {
			logging.WithComponent("telegram").Warn("LLM classifier enabled but no API key found")
		}
	}
```

#### 4d. Add detectIntentWithLLM method (new method):

```go
// detectIntentWithLLM uses LLM classification with regex fallback
func (h *Handler) detectIntentWithLLM(ctx context.Context, chatID, text string) Intent {
	// If LLM classifier not available, use regex
	if h.llmClassifier == nil {
		return DetectIntent(text)
	}

	// Fast path: commands always use regex
	if strings.HasPrefix(text, "/") {
		return IntentCommand
	}

	// Try LLM classification with timeout
	classifyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// Get conversation history
	var history []ConversationMessage
	if h.conversationStore != nil {
		history = h.conversationStore.Get(chatID)
	}

	intent, err := h.llmClassifier.Classify(classifyCtx, history, text)
	if err != nil {
		logging.WithComponent("telegram").Debug("LLM classification failed, using regex",
			slog.Any("error", err))
		return DetectIntent(text)
	}

	logging.WithComponent("telegram").Debug("LLM classified intent",
		slog.String("chat_id", chatID),
		slog.String("intent", string(intent)),
		slog.String("text", truncateDescription(text, 50)))

	return intent
}
```

#### 4e. Update processUpdate to use LLM detection (line ~349-372):

Replace:
```go
	// Detect intent
	intent := DetectIntent(text)
```

With:
```go
	// Detect intent (uses LLM if available, regex fallback)
	intent := h.detectIntentWithLLM(ctx, chatID, text)

	// Record user message in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(chatID, "user", text)
	}
```

#### 4f. Record assistant responses in conversation history:

After each handler that sends a response, add conversation tracking. Example for handleChat (around line ~731):

```go
	_, _ = h.client.SendMessage(ctx, chatID, response, "")

	// Record assistant response in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(chatID, "assistant", truncateDescription(response, 500))
	}
```

---

### Phase 5: Wire Config from main.go

**File**: `cmd/pilot/main.go`

Update HandlerConfig creation (around line ~705-712):

```go
		tgHandler = telegram.NewHandler(&telegram.HandlerConfig{
			BotToken:      cfg.Adapters.Telegram.BotToken,
			ProjectPath:   projectPath,
			Projects:      config.NewProjectSource(cfg),
			AllowedIDs:    allowedIDs,
			Transcription: cfg.Adapters.Telegram.Transcription,
			RateLimit:     cfg.Adapters.Telegram.RateLimit,
			LLMClassifier: cfg.Adapters.Telegram.LLMClassifier, // NEW
		}, runner)
```

---

### Phase 6: Update handleVoice intent detection

**File**: `internal/adapters/telegram/handler.go` (line ~1092-1107)

Replace regex-only detection with LLM-aware detection:

```go
	// Detect intent and handle (uses LLM if available)
	text := strings.TrimSpace(result.Text)
	intent := h.detectIntentWithLLM(ctx, chatID, text)

	// Record transcribed message in conversation history
	if h.conversationStore != nil {
		h.conversationStore.Add(chatID, "user", text)
	}

	switch intent {
	case IntentCommand:
		h.handleCommand(ctx, chatID, text)
	case IntentGreeting:
		h.handleGreeting(ctx, chatID, msg.From)
	case IntentQuestion:
		h.handleQuestion(ctx, chatID, text)
	case IntentResearch:
		h.handleResearch(ctx, chatID, text)
	case IntentPlanning:
		h.handlePlanning(ctx, chatID, text)
	case IntentChat:
		h.handleChat(ctx, chatID, text)
	case IntentTask:
		h.handleTask(ctx, chatID, text)
	default:
		h.handleChat(ctx, chatID, text) // Default to chat, not task
	}
```

---

## Verification

### Unit Tests

1. `anthropic_test.go` - Mock HTTP responses for classification
2. `conversation_test.go` - Test history store, TTL, max size
3. `handler_test.go` - Test LLM detection fallback to regex

### Manual Testing

```bash
# Enable in config
cat >> ~/.pilot/config.yaml << 'EOF'
adapters:
  telegram:
    llm_classifier:
      enabled: true
      # api_key from ANTHROPIC_API_KEY env
EOF

# Test messages
# "What do you think about adding dark mode?" -> should be Chat
# "Add dark mode to the settings page" -> should be Task
# "How does auth work?" -> should be Question
```

---

## Success Criteria

- [ ] LLM classifier called when enabled and API key present
- [ ] Regex fallback works when LLM unavailable or times out
- [ ] Conversation history tracked per chat
- [ ] "What do you think about X?" classified as Chat, not Task
- [ ] Config flows from yaml → main.go → HandlerConfig → Handler
- [ ] Tests pass

---

## Config Example

```yaml
adapters:
  telegram:
    enabled: true
    bot_token: "your-bot-token"
    chat_id: "your-chat-id"
    llm_classifier:
      enabled: true
      # api_key: "" # Falls back to ANTHROPIC_API_KEY env
      timeout_seconds: 2
      history_size: 10
      history_ttl: 30m
```

---

**Ready for Pilot execution**
