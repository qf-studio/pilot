package comms

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/pilot/internal/intent"
)

// IssueDraft holds the LLM-drafted fields for a new GitHub issue.
type IssueDraft struct {
	Title  string
	Body   string
	Labels []string
}

// IssueCreator creates a GitHub issue for a given project.
// The concrete implementation lives in internal/adapters/github to avoid import cycles.
type IssueCreator interface {
	// CreateIssue creates an issue and returns its HTML URL.
	CreateIssue(ctx context.Context, projectPath string, d IssueDraft) (url string, err error)
}

// parseIssueDraft parses a JSON issue draft from LLM output.
// Strips markdown code fences if present; always ensures "pilot" label is included.
func parseIssueDraft(raw string) (IssueDraft, error) {
	raw = strings.TrimSpace(raw)
	// Strip markdown code fences
	if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimPrefix(raw, "```")
		if idx := strings.LastIndex(raw, "```"); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
	}

	var d struct {
		Title  string   `json:"title"`
		Body   string   `json:"body"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return IssueDraft{}, fmt.Errorf("failed to parse issue draft JSON: %w — raw: %s", err, TruncateText(raw, 200))
	}
	if d.Title == "" {
		return IssueDraft{}, fmt.Errorf("LLM returned empty issue title")
	}

	// Ensure "pilot" label is always present (locked decision: auto-execute).
	hasPilot := false
	for _, l := range d.Labels {
		if l == "pilot" {
			hasPilot = true
			break
		}
	}
	if !hasPilot {
		d.Labels = append(d.Labels, "pilot")
	}

	return IssueDraft{Title: d.Title, Body: d.Body, Labels: d.Labels}, nil
}

// draftIssueSystemPrompt returns the system prompt for DraftIssue, honoring r.persona.
func (r *Responder) draftIssueSystemPrompt() string {
	var header string
	if r.persona != "" {
		header = r.persona + "\n\n"
	} else {
		header = "You are Pilot, a developer assistant. "
	}
	return header + `Draft a GitHub issue from the user's description.

Return ONLY a JSON object — no text before or after it:
{
  "title": "<conventional-commit title: type(scope): description>",
  "body": "## Summary\n<1-2 sentence summary>\n\n## Acceptance Criteria\n- [ ] <criterion>",
  "labels": ["pilot"]
}

RULES for the title:
- Must start with a type: feat, fix, chore, refactor, test, docs, perf, build, ci, style
- Format: type(scope): description  — e.g. "feat(gateway): add rate limiting"
- scope is the affected subsystem (e.g. "gateway", "auth", "cli", "comms")
- description is lowercase, imperative, no period at end`
}

// DraftIssue uses the LLM to draft a GitHub issue from a freeform message.
// The returned IssueDraft always includes the "pilot" label.
func (r *Responder) DraftIssue(ctx context.Context, history []intent.ConversationMessage, msg string) (IssueDraft, error) {
	raw, err := r.client.Answer(ctx, r.answerModel, r.draftIssueSystemPrompt(), history, msg)
	if err != nil {
		return IssueDraft{}, fmt.Errorf("LLM draft failed: %w", err)
	}
	return parseIssueDraft(raw)
}

// handleIssueIntake drafts a GitHub issue from the user's freeform message and creates it directly.
func (h *Handler) handleIssueIntake(ctx context.Context, contextID, threadID, text string) {
	if h.responder == nil {
		_ = h.messenger.SendText(ctx, contextID, threadID, "Issue intake requires the bot module to be enabled (bot.enabled: true).")
		return
	}
	if h.issueCreator == nil {
		_ = h.messenger.SendText(ctx, contextID, threadID, "Issue creation requires GitHub to be configured (adapters.github).")
		return
	}

	// Guard: prevent parallel intakes on the same context (mirrors pendingTasks pattern).
	h.mu.Lock()
	if _, inFlight := h.pendingIssues[contextID]; inFlight {
		h.mu.Unlock()
		_ = h.messenger.SendText(ctx, contextID, threadID, "⚠️ An issue is already being drafted for this context. Please wait.")
		return
	}
	placeholder := &IssueDraft{}
	h.pendingIssues[contextID] = placeholder
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pendingIssues, contextID)
		h.mu.Unlock()
	}()

	_ = h.messenger.SendText(ctx, contextID, threadID, "🎫 Drafting issue...")

	var history []intent.ConversationMessage
	if h.convStore != nil {
		history = h.convStore.Get(contextID)
	}

	draft, err := h.responder.DraftIssue(ctx, history, text)
	if err != nil {
		h.log.Warn("DraftIssue failed", slog.Any("error", err))
		_ = h.messenger.SendText(ctx, contextID, threadID, "❌ Failed to draft issue. Try being more specific about what you'd like to track.")
		return
	}

	projectPath := h.getActiveProjectPath(contextID)
	url, err := h.issueCreator.CreateIssue(ctx, projectPath, draft)
	if err != nil {
		h.log.Warn("CreateIssue failed", slog.Any("error", err), slog.String("title", draft.Title))
		_ = h.messenger.SendText(ctx, contextID, threadID, fmt.Sprintf("❌ Failed to create issue: %s", err.Error()))
		return
	}

	reply := fmt.Sprintf("✅ Issue created and queued for Pilot:\n\n*%s*\n%s", draft.Title, url)
	_ = h.messenger.SendChunked(ctx, contextID, threadID, reply, "")
	if h.convStore != nil {
		h.convStore.Add(contextID, "assistant", TruncateText(reply, 500))
	}
}
