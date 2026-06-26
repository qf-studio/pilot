package comms

import (
	"context"
	"fmt"

	"github.com/qf-studio/pilot/internal/intent"
	"github.com/qf-studio/pilot/internal/llm"
)

// chatResponder is the internal interface satisfied by Responder. Handler stores
// this interface so tests can inject mocks without requiring a real Anthropic key.
type chatResponder interface {
	Chat(ctx context.Context, history []intent.ConversationMessage, msg string) (string, error)
}

// Responder answers conversational messages using the Anthropic API directly,
// bypassing the executor/worktree path for low-latency chat replies.
type Responder struct {
	client      *llm.Client
	model       string
	answerModel string // overrides model for Answer calls; empty = use model
	persona     string // system prompt; empty = default Pilot persona
}

// Chat calls the Anthropic API with the configured model and persona system prompt.
func (r *Responder) Chat(ctx context.Context, history []intent.ConversationMessage, msg string) (string, error) {
	m := r.answerModel
	if m == "" {
		m = r.model
	}
	reply, err := r.client.Answer(ctx, m, r.systemPrompt(), history, msg)
	if err != nil {
		return "", fmt.Errorf("responder: %w", err)
	}
	return reply, nil
}

func (r *Responder) systemPrompt() string {
	if r.persona != "" {
		return r.persona
	}
	return "You are Pilot, an AI assistant. Answer helpfully and concisely."
}
