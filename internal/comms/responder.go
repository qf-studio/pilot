package comms

import (
	"context"
	"fmt"

	"github.com/qf-studio/pilot/internal/intent"
	"github.com/qf-studio/pilot/internal/llm"
)

// llmAnswerer is the narrow interface that Responder needs from llm.Client.
// Using an interface keeps Responder testable without a live HTTP server.
type llmAnswerer interface {
	Answer(ctx context.Context, model, system string, history []intent.ConversationMessage, user string) (string, error)
}

// Responder answers conversational messages via direct Anthropic API calls,
// bypassing the Claude Code executor for low-latency chat (~1–2s vs 15–30s).
type Responder struct {
	client      llmAnswerer
	answerModel string
	persona     string
	retrieval   RetrievalConfig
}

func newResponder(client *llm.Client, answerModel, persona string, retrieval RetrievalConfig) *Responder {
	return &Responder{
		client:      client,
		answerModel: answerModel,
		persona:     persona,
		retrieval:   retrieval,
	}
}

// Chat answers a conversational message using the direct LLM path.
// History provides multi-turn context; persona is injected into the system prompt.
func (r *Responder) Chat(ctx context.Context, history []intent.ConversationMessage, msg string) (string, error) {
	return r.client.Answer(ctx, r.answerModel, r.systemPrompt(), history, msg)
}

// Answer answers a codebase question using bounded file retrieval + direct LLM.
// Returns (answer, false, nil) on success.
// Returns ("", true, nil) when the question is too broad — the caller should
// fall back to the executor path.
// Returns ("", false, err) when the LLM call itself fails.
func (r *Responder) Answer(ctx context.Context, history []intent.ConversationMessage, question, projectPath string) (string, bool, error) {
	contextBlock, tooBroad := retrieve(projectPath, question, r.retrieval)
	if tooBroad {
		return "", true, nil
	}
	system := r.answerSystemPrompt(contextBlock)
	answer, err := r.client.Answer(ctx, r.answerModel, system, history, question)
	if err != nil {
		return "", false, err
	}
	return answer, false, nil
}

// Greeting returns a persona-aware greeting without an LLM call.
func (r *Responder) Greeting() string {
	if r.persona != "" {
		return fmt.Sprintf("👋 Hello! %s Send me a task, question, or say /help.", r.persona)
	}
	return "👋 Hello! I'm Pilot — send me a task, question, or say /help."
}

func (r *Responder) systemPrompt() string {
	base := "Be concise and conversational. Keep responses under 400 words. Do not make code changes — chat only."
	if r.persona != "" {
		return r.persona + "\n\n" + base
	}
	return "You are Pilot, a helpful AI development assistant. " + base
}

func (r *Responder) answerSystemPrompt(contextBlock string) string {
	var base string
	if r.persona != "" {
		base = r.persona + "\n\n"
	} else {
		base = "You are Pilot, a helpful AI development assistant.\n\n"
	}
	return base + fmt.Sprintf(`Answer the user's codebase question using ONLY the file excerpts below.
Cite the file paths (e.g. "in internal/comms/handler.go") when referencing code.
Be concise. If the excerpts do not contain enough information to answer fully, say so.

<codebase_context>
%s
</codebase_context>`, contextBlock)
}
