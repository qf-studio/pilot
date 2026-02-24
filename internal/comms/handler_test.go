package comms

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alekspetrov/pilot/internal/intent"
)

// handlerMock records all Messenger calls for assertion in handler tests.
type handlerMock struct {
	mu       sync.Mutex
	texts    []hSentText
	confirms []hSentConfirm
	results  []hSentResult
	chunks   []hSentChunk
	progress []hSentProgress
	acks     []string
}

type hSentText struct {
	contextID, text string
}
type hSentConfirm struct {
	contextID, threadID, taskID, desc, project string
}
type hSentResult struct {
	contextID, threadID, taskID, output, prURL string
	success                                    bool
}
type hSentChunk struct {
	contextID, threadID, content, prefix string
}
type hSentProgress struct {
	contextID, msgRef, taskID, phase, detail string
	progress                                 int
}

func (m *handlerMock) SendText(_ context.Context, contextID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.texts = append(m.texts, hSentText{contextID, text})
	return nil
}

func (m *handlerMock) SendConfirmation(_ context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirms = append(m.confirms, hSentConfirm{contextID, threadID, taskID, desc, project})
	return "msg-ref-1", nil
}

func (m *handlerMock) SendProgress(_ context.Context, contextID, msgRef, taskID, phase string, progress int, detail string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.progress = append(m.progress, hSentProgress{contextID, msgRef, taskID, phase, detail, progress})
	return msgRef, nil
}

func (m *handlerMock) SendResult(_ context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.results = append(m.results, hSentResult{contextID, threadID, taskID, output, prURL, success})
	return nil
}

func (m *handlerMock) SendChunked(_ context.Context, contextID, threadID, content, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = append(m.chunks, hSentChunk{contextID, threadID, content, prefix})
	return nil
}

func (m *handlerMock) AcknowledgeCallback(_ context.Context, callbackID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acks = append(m.acks, callbackID)
	return nil
}

func (m *handlerMock) MaxMessageLength() int { return 4000 }

func (m *handlerMock) getTexts() []hSentText {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]hSentText, len(m.texts))
	copy(cp, m.texts)
	return cp
}

// hMockClassifier returns a fixed intent.
type hMockClassifier struct {
	result intent.Intent
	err    error
}

func (c *hMockClassifier) Classify(_ context.Context, _ []intent.ConversationMessage, _ string) (intent.Intent, error) {
	return c.result, c.err
}

// hMockMemberResolver returns a fixed member ID.
type hMockMemberResolver struct {
	memberID string
	err      error
}

func (r *hMockMemberResolver) ResolveIdentity(_ string) (string, error) {
	return r.memberID, r.err
}

func newTestHandler(m *handlerMock) *Handler {
	return NewHandler(&HandlerConfig{
		Messenger:    m,
		TaskIDPrefix: "TEST",
	})
}

func TestNewHandler(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		ProjectPath:  "/tmp/test-project",
		TaskIDPrefix: "TG",
	})

	if h.taskIDPrefix != "TG" {
		t.Errorf("expected prefix TG, got %s", h.taskIDPrefix)
	}
	if h.projectPath != "/tmp/test-project" {
		t.Errorf("expected project path /tmp/test-project, got %s", h.projectPath)
	}
	if h.rateLimit == nil {
		t.Error("expected rate limiter to be initialized")
	}
}

func TestNewHandler_DefaultPrefix(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{Messenger: m})
	if h.taskIDPrefix != "MSG" {
		t.Errorf("expected default prefix MSG, got %s", h.taskIDPrefix)
	}
}

func TestHandleMessage_RateLimited(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger: m,
		RateLimit: &RateLimitConfig{
			Enabled:           true,
			MessagesPerMinute: 1,
			BurstSize:         1,
			TasksPerHour:      1,
		},
		TaskIDPrefix: "TEST",
	})

	ctx := context.Background()
	// First message consumes the single token
	h.HandleMessage(ctx, &IncomingMessage{ContextID: "ch1", SenderID: "u1", Text: "hello"})
	// Second message should be rate limited
	h.HandleMessage(ctx, &IncomingMessage{ContextID: "ch1", SenderID: "u1", Text: "hello again"})

	texts := m.getTexts()
	found := false
	for _, st := range texts {
		if st.text == "⚠️ Rate limit exceeded. Please wait before sending more messages." {
			found = true
		}
	}
	if !found {
		t.Error("expected rate limit message")
	}
}

func TestHandleMessage_Greeting(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "ch1",
		SenderID:  "u1",
		Text:      "hello",
	})

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected at least one text message")
	}
	if texts[0].text != "👋 Hello! I'm Pilot — send me a task, question, or say /help." {
		t.Errorf("unexpected greeting: %s", texts[0].text)
	}
}

func TestHandleMessage_ConfirmationNo(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	// Seed a pending task
	h.mu.Lock()
	h.pendingTasks["ch1"] = &PendingTask{
		TaskID:      "TEST-123",
		Description: "do something",
		ContextID:   "ch1",
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "ch1",
		SenderID:  "u1",
		Text:      "no",
	})

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected cancellation message")
	}
	if texts[0].text != "❌ Task TEST-123 cancelled." {
		t.Errorf("unexpected message: %s", texts[0].text)
	}

	// Verify pending task was removed
	h.mu.Lock()
	_, exists := h.pendingTasks["ch1"]
	h.mu.Unlock()
	if exists {
		t.Error("pending task should have been removed")
	}
}

func TestHandleMessage_CallbackConfirmation(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.mu.Lock()
	h.pendingTasks["ch1"] = &PendingTask{
		TaskID:      "TEST-456",
		Description: "build feature",
		ContextID:   "ch1",
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID:  "ch1",
		SenderID:   "u1",
		IsCallback: true,
		CallbackID: "cb-1",
		ActionID:   "cancel",
	})

	if len(m.acks) == 0 {
		t.Error("expected callback acknowledgment")
	}
	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected cancellation message")
	}
	if texts[0].text != "❌ Task TEST-456 cancelled." {
		t.Errorf("unexpected: %s", texts[0].text)
	}
}

func TestHandleMessage_NoConfirmationPending(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "ch1",
		SenderID:  "u1",
		Text:      "yes",
	})

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected 'no pending task' message")
	}
	if texts[0].text != "No pending task to confirm." {
		t.Errorf("unexpected: %s", texts[0].text)
	}
}

func TestDetectIntent_Command(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	got := h.detectIntent(context.Background(), "ch1", "/help")
	if got != intent.IntentCommand {
		t.Errorf("expected command intent, got %s", got)
	}
}

func TestDetectIntent_ClearQuestion(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	got := h.detectIntent(context.Background(), "ch1", "What does the auth handler do?")
	if got != intent.IntentQuestion {
		t.Errorf("expected question intent, got %s", got)
	}
}

func TestDetectIntent_LLMClassifier(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger:     m,
		LLMClassifier: &hMockClassifier{result: intent.IntentResearch},
		TaskIDPrefix:  "TEST",
	})

	got := h.detectIntent(context.Background(), "ch1", "analyze the codebase performance")
	if got != intent.IntentResearch {
		t.Errorf("expected research intent from LLM, got %s", got)
	}
}

func TestDetectIntent_LLMFallback(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger:     m,
		LLMClassifier: &hMockClassifier{err: fmt.Errorf("timeout")},
		TaskIDPrefix:  "TEST",
	})

	// Should fall back to regex
	got := h.detectIntent(context.Background(), "ch1", "hello")
	if got != intent.IntentGreeting {
		t.Errorf("expected greeting intent from fallback, got %s", got)
	}
}

func TestHandleTask_RateLimited(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger: m,
		RateLimit: &RateLimitConfig{
			Enabled:           true,
			MessagesPerMinute: 100,
			TasksPerHour:      1,
			BurstSize:         1,
		},
		TaskIDPrefix: "TEST",
	})

	ctx := context.Background()
	// First task uses the token
	h.handleTask(ctx, "ch1", "", "create a feature", "u1")
	// Second task should be rate limited
	h.handleTask(ctx, "ch1", "", "another feature", "u1")

	texts := m.getTexts()
	found := false
	for _, st := range texts {
		if st.text == "⚠️ Task rate limit exceeded. You've submitted too many tasks recently. Please wait before submitting more." {
			found = true
		}
	}
	if !found {
		t.Error("expected task rate limit message")
	}
}

func TestHandleTask_ExistingPending(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.mu.Lock()
	h.pendingTasks["ch1"] = &PendingTask{
		TaskID:    "OLD-1",
		ContextID: "ch1",
		CreatedAt: time.Now(),
	}
	h.mu.Unlock()

	h.handleTask(context.Background(), "ch1", "", "new task", "u1")

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected warning about existing task")
	}
	if texts[0].contextID != "ch1" {
		t.Error("wrong context")
	}
}

func TestGetActiveProjectPath(t *testing.T) {
	m := &handlerMock{}
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		ProjectPath:  "/default/path",
		TaskIDPrefix: "TEST",
	})

	// Default path
	if got := h.getActiveProjectPath("ch1"); got != "/default/path" {
		t.Errorf("expected default path, got %s", got)
	}

	// Set active
	h.mu.Lock()
	h.activeProject["ch1"] = "/custom/path"
	h.mu.Unlock()

	if got := h.getActiveProjectPath("ch1"); got != "/custom/path" {
		t.Errorf("expected custom path, got %s", got)
	}
}

func TestResolveMemberID(t *testing.T) {
	tests := []struct {
		name     string
		resolver MemberResolver
		senderID string
		want     string
	}{
		{"nil resolver", nil, "u1", ""},
		{"no sender", &hMockMemberResolver{memberID: "m1"}, "", ""},
		{"resolved", &hMockMemberResolver{memberID: "m1"}, "u1", "m1"},
		{"error", &hMockMemberResolver{err: fmt.Errorf("fail")}, "u1", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &handlerMock{}
			h := NewHandler(&HandlerConfig{
				Messenger:      m,
				MemberResolver: tt.resolver,
				TaskIDPrefix:   "TEST",
			})

			if tt.senderID != "" {
				h.mu.Lock()
				h.lastSender["ch1"] = tt.senderID
				h.mu.Unlock()
			}

			got := h.resolveMemberID("ch1")
			if got != tt.want {
				t.Errorf("resolveMemberID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCancelTask_Pending(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.mu.Lock()
	h.pendingTasks["ch1"] = &PendingTask{
		TaskID:    "T-1",
		ContextID: "ch1",
		CreatedAt: time.Now(),
	}
	h.mu.Unlock()

	err := h.CancelTask(context.Background(), "ch1")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	h.mu.Lock()
	_, exists := h.pendingTasks["ch1"]
	h.mu.Unlock()
	if exists {
		t.Error("pending task should be removed")
	}
}

func TestCancelTask_None(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	err := h.CancelTask(context.Background(), "ch1")
	if err == nil {
		t.Error("expected error when no task to cancel")
	}
}

func TestCleanupExpiredTasks(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.mu.Lock()
	h.pendingTasks["ch1"] = &PendingTask{
		TaskID:    "T-OLD",
		ContextID: "ch1",
		CreatedAt: time.Now().Add(-10 * time.Minute), // expired
	}
	h.pendingTasks["ch2"] = &PendingTask{
		TaskID:    "T-NEW",
		ContextID: "ch2",
		CreatedAt: time.Now(), // fresh
	}
	h.mu.Unlock()

	h.cleanupExpiredTasks(context.Background())

	h.mu.Lock()
	_, ch1Exists := h.pendingTasks["ch1"]
	_, ch2Exists := h.pendingTasks["ch2"]
	h.mu.Unlock()

	if ch1Exists {
		t.Error("expired task should be removed")
	}
	if !ch2Exists {
		t.Error("fresh task should remain")
	}

	// Verify notification sent for expired
	texts := m.getTexts()
	found := false
	for _, txt := range texts {
		if txt.contextID == "ch1" {
			found = true
		}
	}
	if !found {
		t.Error("expected expiration notification for ch1")
	}
}

func TestSenderTracking(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "ch1",
		SenderID:  "user-42",
		Text:      "hello",
	})

	h.mu.Lock()
	sender := h.lastSender["ch1"]
	h.mu.Unlock()

	if sender != "user-42" {
		t.Errorf("expected sender user-42, got %s", sender)
	}
}
