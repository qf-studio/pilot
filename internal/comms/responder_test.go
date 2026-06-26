package comms

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/intent"
)

// mockAnswerer records calls to Answer and returns a canned reply.
type mockAnswerer struct {
	reply  string
	err    error
	calls  []mockAnswerCall
}

type mockAnswerCall struct {
	model, system, user string
	history             []intent.ConversationMessage
}

func (m *mockAnswerer) Answer(_ context.Context, model, system string, history []intent.ConversationMessage, user string) (string, error) {
	m.calls = append(m.calls, mockAnswerCall{model: model, system: system, history: history, user: user})
	return m.reply, m.err
}

func newMockResponder(reply, persona string) (*Responder, *mockAnswerer) {
	a := &mockAnswerer{reply: reply}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001", persona: persona}
	return r, a
}

// TestResponder_Chat_CallsLLM verifies Chat returns the LLM reply.
func TestResponder_Chat_CallsLLM(t *testing.T) {
	r, _ := newMockResponder("Great question!", "")
	got, err := r.Chat(context.Background(), nil, "how are you?")
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if got != "Great question!" {
		t.Errorf("Chat() = %q, want %q", got, "Great question!")
	}
}

// TestResponder_Chat_WithHistory verifies that Chat passes history to the LLM.
func TestResponder_Chat_WithHistory(t *testing.T) {
	r, a := newMockResponder("You asked about testing.", "")
	history := []intent.ConversationMessage{
		{Role: "user", Content: "tell me about testing"},
		{Role: "assistant", Content: "testing is important"},
	}
	_, err := r.Chat(context.Background(), history, "what did I ask?")
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(a.calls) == 0 {
		t.Fatal("expected at least one Answer call")
	}
	if len(a.calls[0].history) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(a.calls[0].history))
	}
}

// TestResponder_Chat_PersonaInSystemPrompt verifies the persona is included.
func TestResponder_Chat_PersonaInSystemPrompt(t *testing.T) {
	r, a := newMockResponder("ok", "You are a Go expert.")
	_, err := r.Chat(context.Background(), nil, "hello")
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if len(a.calls) == 0 {
		t.Fatal("expected an Answer call")
	}
	if !strings.Contains(a.calls[0].system, "You are a Go expert.") {
		t.Errorf("persona not in system prompt; system = %q", a.calls[0].system)
	}
}

// TestResponder_Chat_NoPersonaDefaultPrompt verifies the default system prompt.
func TestResponder_Chat_NoPersonaDefaultPrompt(t *testing.T) {
	r, a := newMockResponder("ok", "")
	_, _ = r.Chat(context.Background(), nil, "hello")
	if len(a.calls) == 0 {
		t.Fatal("expected an Answer call")
	}
	if !strings.Contains(a.calls[0].system, "Pilot") {
		t.Errorf("expected 'Pilot' in default system prompt; got %q", a.calls[0].system)
	}
}

// TestResponder_Chat_Error propagates LLM errors.
func TestResponder_Chat_Error(t *testing.T) {
	a := &mockAnswerer{err: errors.New("timeout")}
	r := &Responder{client: a, answerModel: "claude-haiku-4-5-20251001"}
	_, err := r.Chat(context.Background(), nil, "hi")
	if err == nil {
		t.Error("expected error from Chat, got nil")
	}
}

// TestResponder_Greeting_WithPersona verifies persona-formatted greeting.
func TestResponder_Greeting_WithPersona(t *testing.T) {
	r, _ := newMockResponder("", "I am your Go assistant.")
	got := r.Greeting()
	if !strings.Contains(got, "I am your Go assistant.") {
		t.Errorf("persona not in greeting: %q", got)
	}
	if !strings.Contains(got, "/help") {
		t.Errorf("greeting missing /help hint: %q", got)
	}
}

// TestResponder_Greeting_NoPersona verifies the default (static) greeting.
func TestResponder_Greeting_NoPersona(t *testing.T) {
	r, _ := newMockResponder("", "")
	got := r.Greeting()
	if got != "👋 Hello! I'm Pilot — send me a task, question, or say /help." {
		t.Errorf("unexpected default greeting: %q", got)
	}
}

// TestBuildResponder_NilCfg verifies nil config returns nil responder.
func TestBuildResponder_NilCfg(t *testing.T) {
	if got := BuildResponder(nil); got != nil {
		t.Errorf("BuildResponder(nil) = %v, want nil", got)
	}
}

// TestBuildResponder_Disabled verifies disabled config returns nil responder.
func TestBuildResponder_Disabled(t *testing.T) {
	if got := BuildResponder(&BotConfig{Enabled: false, APIKey: "key"}); got != nil {
		t.Errorf("BuildResponder(disabled) = %v, want nil", got)
	}
}

// TestBuildResponder_NoKey verifies that no key (and env not set) returns nil.
func TestBuildResponder_NoKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	got := BuildResponder(&BotConfig{Enabled: true, APIKey: ""})
	if got != nil {
		t.Errorf("BuildResponder(no key) = %v, want nil", got)
	}
}

// TestBuildResponder_EnvKeyFallback verifies env var fallback.
func TestBuildResponder_EnvKeyFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	got := BuildResponder(&BotConfig{Enabled: true, APIKey: ""})
	if got == nil {
		t.Error("BuildResponder(env key) = nil, want non-nil Responder")
	}
}

// TestBuildResponder_DefaultModel verifies that empty model defaults to Haiku.
func TestBuildResponder_DefaultModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	r := BuildResponder(&BotConfig{Enabled: true})
	if r == nil {
		t.Fatal("expected non-nil Responder")
	}
	if r.answerModel != "claude-haiku-4-5-20251001" {
		t.Errorf("answerModel = %q, want claude-haiku-4-5-20251001", r.answerModel)
	}
}

// TestBuildResponder_AnswerModelOverride verifies answer_model overrides model.
func TestBuildResponder_AnswerModelOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	r := BuildResponder(&BotConfig{
		Enabled:     true,
		Model:       "claude-haiku-4-5-20251001",
		AnswerModel: "claude-sonnet-4-6",
	})
	if r == nil {
		t.Fatal("expected non-nil Responder")
	}
	if r.answerModel != "claude-sonnet-4-6" {
		t.Errorf("answerModel = %q, want claude-sonnet-4-6", r.answerModel)
	}
}
