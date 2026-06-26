package comms

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/intent"
)

// --- BuildClassifier tests ---

func TestBuildClassifier_NilConfig(t *testing.T) {
	classifier, store := BuildClassifier(nil, nil)
	if classifier != nil {
		t.Error("expected nil classifier for nil config")
	}
	if store != nil {
		t.Error("expected nil store for nil config")
	}
}

func TestBuildClassifier_Disabled(t *testing.T) {
	cfg := &ClassifierConfig{Enabled: false, APIKey: "some-key"}
	classifier, store := BuildClassifier(cfg, nil)
	if classifier != nil {
		t.Error("expected nil classifier when disabled")
	}
	if store != nil {
		t.Error("expected nil store when disabled")
	}
}

func TestBuildClassifier_NoAPIKey(t *testing.T) {
	// Enabled but no key and no ANTHROPIC_API_KEY env var (test env has none).
	cfg := &ClassifierConfig{Enabled: true, APIKey: ""}
	// Do not set env var — ensure the function returns nil rather than panicking.
	classifier, store := BuildClassifier(cfg, nil)
	// Result depends on whether ANTHROPIC_API_KEY is set in the test environment.
	// We only assert that if both are nil no panic occurred.
	if (classifier == nil) != (store == nil) {
		t.Error("classifier and store must both be nil or both non-nil")
	}
}

func TestBuildClassifier_Enabled(t *testing.T) {
	cfg := &ClassifierConfig{
		Enabled:     true,
		APIKey:      "test-anthropic-key",
		HistorySize: 5,
		HistoryTTL:  10 * time.Minute,
	}
	classifier, store := BuildClassifier(cfg, nil)
	if classifier == nil {
		t.Fatal("expected non-nil classifier when enabled with API key")
	}
	if store == nil {
		t.Fatal("expected non-nil store when enabled with API key")
	}
}

func TestBuildClassifier_DefaultHistorySizeAndTTL(t *testing.T) {
	cfg := &ClassifierConfig{
		Enabled: true,
		APIKey:  "test-key",
		// HistorySize and HistoryTTL left at zero → factory uses defaults
	}
	classifier, store := BuildClassifier(cfg, nil)
	if classifier == nil {
		t.Fatal("expected non-nil classifier")
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	// Verify the store works (add + get a message)
	store.Add("chan1", "user", "hello")
	msgs := store.Get("chan1")
	if len(msgs) != 1 {
		t.Errorf("expected 1 message in store, got %d", len(msgs))
	}
}

// --- BuildHandler tests ---

func TestBuildHandler_AllFields(t *testing.T) {
	m := &handlerMock{}
	classifier := &hMockClassifier{result: intent.IntentQuestion}
	resolver := &hMockMemberResolver{memberID: "member-1"}

	h := BuildHandler(HandlerDeps{
		Messenger: m,
		Classifier: &ClassifierConfig{
			Enabled: true,
			APIKey:  "test-key",
		},
		MemberResolver: resolver,
		TaskIDPrefix:   "SLACK",
	})

	if h == nil {
		t.Fatal("expected non-nil Handler")
	}
	if h.taskIDPrefix != "SLACK" {
		t.Errorf("expected prefix SLACK, got %s", h.taskIDPrefix)
	}
	if h.llmClassifier == nil {
		t.Error("expected llmClassifier to be wired")
	}
	if h.convStore == nil {
		t.Error("expected convStore to be wired when classifier is enabled")
	}
	if h.memberResolver == nil {
		t.Error("expected memberResolver to be wired")
	}
	if h.rateLimit == nil {
		t.Error("expected rateLimit to be initialized (default)")
	}
	_ = classifier // referenced in test setup only
}

func TestBuildHandler_DisabledClassifier(t *testing.T) {
	m := &handlerMock{}
	h := BuildHandler(HandlerDeps{
		Messenger:    m,
		TaskIDPrefix: "TG",
		Classifier:   &ClassifierConfig{Enabled: false},
	})

	if h.llmClassifier != nil {
		t.Error("expected nil llmClassifier when classifier disabled")
	}
	if h.convStore != nil {
		t.Error("expected nil convStore when classifier disabled")
	}
}

func TestBuildHandler_NilClassifierConfig(t *testing.T) {
	m := &handlerMock{}
	h := BuildHandler(HandlerDeps{
		Messenger:    m,
		TaskIDPrefix: "DISCORD",
		Classifier:   nil,
	})
	if h.llmClassifier != nil {
		t.Error("expected nil llmClassifier when ClassifierConfig is nil")
	}
}

// TestBuildHandler_AdapterParity verifies all five adapter configurations
// are exercised via BuildHandler and produce consistent fields.
func TestBuildHandler_AdapterParity(t *testing.T) {
	cases := []struct {
		name         string
		prefix       string
		hasClassifier bool
	}{
		{"telegram-main", "TG", true},
		{"slack-main", "SLACK", true},
		{"discord", "DISCORD", true},
		{"telegram-gateway", "TG", false},
		{"slack-gateway", "SLACK", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var classifierCfg *ClassifierConfig
			if tc.hasClassifier {
				classifierCfg = &ClassifierConfig{
					Enabled: true,
					APIKey:  "test-key",
				}
			}

			m := &handlerMock{}
			h := BuildHandler(HandlerDeps{
				Messenger:    m,
				TaskIDPrefix: tc.prefix,
				Classifier:   classifierCfg,
			})

			if h == nil {
				t.Fatal("BuildHandler returned nil")
			}
			if h.taskIDPrefix != tc.prefix {
				t.Errorf("expected prefix %s, got %s", tc.prefix, h.taskIDPrefix)
			}
			if h.rateLimit == nil {
				t.Error("rateLimit must never be nil — factory should apply default")
			}
			if tc.hasClassifier && h.llmClassifier == nil {
				t.Error("llmClassifier should be wired when classifier config is enabled")
			}
			if tc.hasClassifier && h.convStore == nil {
				t.Error("convStore should be wired when classifier config is enabled")
			}
		})
	}
}

// --- BuildResponder tests ---

func TestBuildResponder_NilConfig(t *testing.T) {
	r := BuildResponder(nil)
	if r != nil {
		t.Error("expected nil responder for nil config")
	}
}

func TestBuildResponder_Disabled(t *testing.T) {
	r := BuildResponder(&BotConfig{Enabled: false, APIKey: "test-key"})
	if r != nil {
		t.Error("expected nil responder when disabled")
	}
}

func TestBuildResponder_NoAPIKey(t *testing.T) {
	// Enabled but no key — result depends on ANTHROPIC_API_KEY env; just verify no panic.
	r := BuildResponder(&BotConfig{Enabled: true, APIKey: ""})
	_ = r
}

func TestBuildResponder_Enabled(t *testing.T) {
	r := BuildResponder(&BotConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		Model:   "claude-haiku-4-5-20251001",
	})
	if r == nil {
		t.Fatal("expected non-nil responder when enabled with API key")
	}
}

func TestBuildResponder_DefaultModel(t *testing.T) {
	r := BuildResponder(&BotConfig{Enabled: true, APIKey: "test-key"})
	if r == nil {
		t.Fatal("expected non-nil responder")
	}
	if r.model != "claude-haiku-4-5-20251001" {
		t.Errorf("expected default model claude-haiku-4-5-20251001, got %s", r.model)
	}
}

func TestBuildResponder_PersonaAndAnswerModel(t *testing.T) {
	r := BuildResponder(&BotConfig{
		Enabled:     true,
		APIKey:      "test-key",
		Model:       "claude-haiku-4-5-20251001",
		AnswerModel: "claude-sonnet-4-6",
		Persona:     "You are a helpful DevOps assistant.",
	})
	if r == nil {
		t.Fatal("expected non-nil responder")
	}
	if r.answerModel != "claude-sonnet-4-6" {
		t.Errorf("expected answerModel claude-sonnet-4-6, got %s", r.answerModel)
	}
	if r.persona != "You are a helpful DevOps assistant." {
		t.Errorf("unexpected persona: %s", r.persona)
	}
}

func TestBuildResponder_SystemPrompt_DefaultWhenNoPersona(t *testing.T) {
	r := BuildResponder(&BotConfig{Enabled: true, APIKey: "test-key"})
	if r == nil {
		t.Fatal("expected non-nil responder")
	}
	sp := r.systemPrompt()
	if sp == "" {
		t.Error("expected a non-empty default system prompt")
	}
}

func TestBuildResponder_SystemPrompt_UsesPersona(t *testing.T) {
	r := BuildResponder(&BotConfig{
		Enabled: true,
		APIKey:  "test-key",
		Persona: "You are specialized in Go.",
	})
	if r == nil {
		t.Fatal("expected non-nil responder")
	}
	if r.systemPrompt() != "You are specialized in Go." {
		t.Errorf("expected persona as system prompt, got: %s", r.systemPrompt())
	}
}

// --- BuildHandler bot wiring tests ---

func TestBuildHandler_WithBot_WiresResponder(t *testing.T) {
	m := &handlerMock{}
	h := BuildHandler(HandlerDeps{
		Messenger:    m,
		TaskIDPrefix: "SLACK",
		Bot: &BotConfig{
			Enabled: true,
			APIKey:  "test-bot-key",
			Model:   "claude-haiku-4-5-20251001",
			Persona: "You are TestBot.",
		},
	})
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	if h.responder == nil {
		t.Error("expected responder to be wired when bot config is enabled")
	}
}

func TestBuildHandler_NilBot_NilResponder(t *testing.T) {
	m := &handlerMock{}
	h := BuildHandler(HandlerDeps{
		Messenger:    m,
		TaskIDPrefix: "SLACK",
		Bot:          nil,
	})
	if h.responder != nil {
		t.Error("expected nil responder when bot config is nil")
	}
}

func TestBuildHandler_DisabledBot_NilResponder(t *testing.T) {
	m := &handlerMock{}
	h := BuildHandler(HandlerDeps{
		Messenger:    m,
		TaskIDPrefix: "TG",
		Bot:          &BotConfig{Enabled: false, APIKey: "test-key"},
	})
	if h.responder != nil {
		t.Error("expected nil responder when bot is disabled")
	}
}

// TestBuildHandler_ClassifierRoutedToHandler verifies that when a mock classifier
// is wired, detectIntent uses it for messages that bypass the regex fast paths.
// Slack regression: GH-3645 — "check the pilot queue" was classified as IntentTask
// because the LLM classifier was never wired into Slack's comms.Handler.
func TestBuildHandler_ClassifierRoutedToHandler(t *testing.T) {
	// Mock classifier returns IntentGreeting so the handler calls handleGreeting
	// (no runner required), letting us observe the send without a nil-runner panic.
	mockClassifier := &hMockClassifier{result: intent.IntentGreeting}
	m := &handlerMock{}

	h := NewHandler(&HandlerConfig{
		Messenger:     m,
		LLMClassifier: mockClassifier,
		TaskIDPrefix:  "SLACK",
	})

	ctx := context.Background()
	// "check the pilot queue" — does not start with a greeting, is not a clear
	// question, so it reaches the LLM classifier gate in detectIntent.
	h.HandleMessage(ctx, &IncomingMessage{
		ContextID: "C123",
		SenderID:  "U1",
		Text:      "check the pilot queue",
	})

	texts := m.getTexts()
	for _, st := range texts {
		if st.contextID == "C123" && st.text != "" {
			// Any non-empty response means the message was handled — confirm it
			// did NOT go to the task confirmation path (which has no text send).
			// If classifier wasn't called, the message would be classified as
			// IntentTask and sent to SendConfirmation, not SendText.
			for _, c := range m.confirms {
				if c.contextID == "C123" {
					t.Error("message routed to task confirmation — LLM classifier not called")
				}
			}
			return
		}
	}
	t.Error("no response sent — LLM classifier not routed to handler")
}
