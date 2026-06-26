package comms

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
}

func newResponder(client *llm.Client, answerModel, persona string) *Responder {
	return &Responder{
		client:      client,
		answerModel: answerModel,
		persona:     persona,
	}
}

// Chat answers a conversational message using the direct LLM path.
// History provides multi-turn context; persona is injected into the system prompt.
func (r *Responder) Chat(ctx context.Context, history []intent.ConversationMessage, msg string) (string, error) {
	return r.client.Answer(ctx, r.answerModel, r.systemPrompt(), history, msg)
}

// Greeting returns a persona-aware greeting without an LLM call.
func (r *Responder) Greeting() string {
	if r.persona != "" {
		return fmt.Sprintf("👋 Hello! %s Send me a task, question, or say /help.", r.persona)
	}
	return "👋 Hello! I'm Pilot — send me a task, question, or say /help."
}

// DraftIssue asks the LLM to produce a structured GitHub issue from the user's description.
// The title always follows the conventional-commits format (type(scope): description).
// Labels defaults to ["pilot"] when autoLabelPilot is true.
func (r *Responder) DraftIssue(ctx context.Context, history []intent.ConversationMessage, msg string, autoLabelPilot bool) (IssueDraft, error) {
	system := `You are a GitHub issue drafter for a software project. Given a description, produce a JSON object with these fields:
- "title": a conventional-commit style title in format "type(scope): description" (types: feat, fix, chore, refactor, test, docs, perf, build, ci, style)
- "body": a clear, concise issue body in Markdown explaining the problem or feature request
- "labels": an array of label strings

Rules:
- Title MUST match: type(scope): description
- Keep the title under 72 characters
- Body should be 2-5 sentences
- Always include "pilot" in labels

Respond with ONLY valid JSON, no markdown fences.`

	raw, err := r.client.Answer(ctx, r.answerModel, system, history, msg)
	if err != nil {
		return IssueDraft{}, fmt.Errorf("DraftIssue LLM call: %w", err)
	}

	raw = strings.TrimSpace(raw)
	// Strip markdown code fences if the model wraps the JSON anyway.
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var draft struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		return IssueDraft{}, fmt.Errorf("DraftIssue parse JSON: %w (raw=%q)", err, raw)
	}

	labels := draft.Labels
	if autoLabelPilot && !containsLabel(labels, "pilot") {
		labels = append(labels, "pilot")
	}

	return IssueDraft{
		Title:  draft.Title,
		Body:   draft.Body,
		Labels: labels,
	}, nil
}

func containsLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

func (r *Responder) systemPrompt() string {
	base := "Be concise and conversational. Keep responses under 400 words. Do not make code changes — chat only."
	if r.persona != "" {
		return r.persona + "\n\n" + base
	}
	return "You are Pilot, a helpful AI development assistant. " + base
}
