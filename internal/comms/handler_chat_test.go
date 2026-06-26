package comms

// Adapter→comms seam tests (per mem-036 — test at the boundary, not in isolation).
//
// These tests assert:
//   (a) When responder != nil, handleChat/handleGreeting bypass runner.Execute and
//       any worktree creation entirely.
//   (b) When responder == nil (bot disabled), the existing executor path is taken
//       with zero regression.

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/intent"
)

// newHandlerWithResponder builds a Handler with a mock responder wired in.
// runner is intentionally nil so any runner.Execute call would panic — proving
// the responder path never touches the executor.
func newHandlerWithResponder(m *handlerMock, reply, persona string) (*Handler, *mockAnswerer) {
	a := &mockAnswerer{reply: reply}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001", persona: persona}
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		Responder:    r,
		TaskIDPrefix: "TEST",
	})
	return h, a
}

// ---------------------------------------------------------------------------
// handleChat seam tests
// ---------------------------------------------------------------------------

// TestHandleChat_ResponderPath_SkipsRunner verifies that when a responder is
// configured the chat reply arrives WITHOUT invoking runner.Execute (which would
// panic because runner is nil here).
func TestHandleChat_ResponderPath_SkipsRunner(t *testing.T) {
	m := &handlerMock{}
	h, _ := newHandlerWithResponder(m, "Hello from LLM", "")

	h.handleChat(context.Background(), "ch1", "", "how are you?")

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected at least one text reply")
	}
	got := texts[0].text
	if got != "Hello from LLM" {
		t.Errorf("handleChat responder path: got %q, want %q", got, "Hello from LLM")
	}
}

// TestHandleChat_ResponderPath_RecordsToConvStore verifies the reply is stored
// in the conversation store for multi-turn context.
func TestHandleChat_ResponderPath_RecordsToConvStore(t *testing.T) {
	m := &handlerMock{}
	a := &mockAnswerer{reply: "LLM reply"}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	convStore := intent.NewConversationStore(10, 0)
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		Responder:    r,
		ConvStore:    convStore,
		TaskIDPrefix: "TEST",
	})

	h.handleChat(context.Background(), "ch1", "", "hi")

	msgs := convStore.Get("ch1")
	found := false
	for _, msg := range msgs {
		if msg.Role == "assistant" && strings.Contains(msg.Content, "LLM reply") {
			found = true
		}
	}
	if !found {
		t.Errorf("responder reply not recorded in convStore; got: %v", msgs)
	}
}

// TestHandleChat_ResponderPath_PassesHistory verifies that existing conversation
// history is forwarded to the responder's Chat call.
func TestHandleChat_ResponderPath_PassesHistory(t *testing.T) {
	m := &handlerMock{}
	a := &mockAnswerer{reply: "ok"}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	convStore := intent.NewConversationStore(10, 0)
	convStore.Add("ch1", "user", "earlier message")
	convStore.Add("ch1", "assistant", "earlier reply")
	h := NewHandler(&HandlerConfig{
		Messenger:    m,
		Responder:    r,
		ConvStore:    convStore,
		TaskIDPrefix: "TEST",
	})

	h.handleChat(context.Background(), "ch1", "", "follow up")

	if len(a.calls) == 0 {
		t.Fatal("expected at least one Answer call")
	}
	if len(a.calls[0].history) < 2 {
		t.Errorf("expected >= 2 history entries forwarded to LLM, got %d", len(a.calls[0].history))
	}
}

// TestHandleChat_NilResponder_ExecutorPath verifies that with no responder the
// handler falls through to the "💬 Thinking..." executor path (runner nil → it
// returns an error, but the key check is the "Thinking" message was sent first).
func TestHandleChat_NilResponder_ExecutorPath(t *testing.T) {
	m := &handlerMock{}
	// No responder, no runner — falls into executor path; runner is nil so
	// Execute will panic unless we guard. We only check the first message.
	h := newTestHandler(m)

	// We can't call runner.Execute here (nil runner). Recover the panic that
	// would occur to verify we DO reach the executor path (the "Thinking" message
	// is sent before Execute is called).
	func() {
		defer func() { recover() }() //nolint:errcheck
		h.handleChat(context.Background(), "ch1", "", "chat with no bot")
	}()

	texts := m.getTexts()
	found := false
	for _, st := range texts {
		if strings.Contains(st.text, "Thinking") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("nil-responder path did not send 'Thinking' preamble; texts: %v", texts)
	}
}

// ---------------------------------------------------------------------------
// handleGreeting seam tests
// ---------------------------------------------------------------------------

// TestHandleGreeting_ResponderPath_UsesPersona verifies persona is returned when
// responder is configured.
func TestHandleGreeting_ResponderPath_UsesPersona(t *testing.T) {
	m := &handlerMock{}
	h, _ := newHandlerWithResponder(m, "", "I am your Go assistant.")

	h.handleGreeting(context.Background(), "ch1")

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected a greeting reply")
	}
	if !strings.Contains(texts[0].text, "I am your Go assistant.") {
		t.Errorf("persona not in greeting: %q", texts[0].text)
	}
}

// TestHandleGreeting_NilResponder_StaticText verifies the static fallback greeting.
func TestHandleGreeting_NilResponder_StaticText(t *testing.T) {
	m := &handlerMock{}
	h := newTestHandler(m)

	h.handleGreeting(context.Background(), "ch1")

	texts := m.getTexts()
	if len(texts) == 0 {
		t.Fatal("expected a greeting reply")
	}
	if texts[0].text != "👋 Hello! I'm Pilot — send me a task, question, or say /help." {
		t.Errorf("unexpected static greeting: %q", texts[0].text)
	}
}

// ---------------------------------------------------------------------------
// Full HandleMessage dispatch seam tests
// ---------------------------------------------------------------------------

// TestHandleMessage_Chat_ResponderPath_SkipsRunner exercises the full
// HandleMessage→handleChat path when the responder is wired, confirming no
// task confirmation and no runner invocation.
func TestHandleMessage_Chat_ResponderPath_SkipsRunner(t *testing.T) {
	m := &handlerMock{}
	h, _ := newHandlerWithResponder(m, "I think about this...", "")

	// Force IntentChat via the LLM classifier mock.
	h.llmClassifier = &hMockClassifier{result: intent.IntentChat}

	// Use text with no /prefix, no greeting, no clear-question prefix, no
	// operational keywords — so it falls through to the LLM classifier mock.
	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "ch1",
		SenderID:  "u1",
		Text:      "discuss generics with me please",
	})

	// Must NOT have created a pending task.
	h.mu.Lock()
	_, hasPending := h.pendingTasks["ch1"]
	h.mu.Unlock()
	if hasPending {
		t.Error("responder chat path created a PendingTask — executor path should be bypassed")
	}

	texts := m.getTexts()
	found := false
	for _, st := range texts {
		if strings.Contains(st.text, "I think about this") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected responder reply in texts; got: %v", texts)
	}
}
