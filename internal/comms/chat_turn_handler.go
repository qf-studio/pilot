package comms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

const issueCardTTL = 30 * time.Minute

var refsHeadingRE = regexp.MustCompile(`(?m)^## Refs[ \t]*$`)

// pendingIssueCard is a drafted pilot issue awaiting the user's decision.
type pendingIssueCard struct {
	Draft       IssueDraft
	CardID      string
	ProjectPath string
	CreatedAt   time.Time
}

// DailyChatLimitMessage is sent instead of running a turn once the org-wide
// daily cap is reached.
const DailyChatLimitMessage = "Daily chat limit reached. It resets at 00:00 UTC."

var chatTurnsRejected atomic.Int64

// ChatTurnsRejectedTotal returns the process-lifetime number of chat turns
// refused by the org-wide daily cap (pilot_chat_turns_rejected_total).
func ChatTurnsRejectedTotal() int64 { return chatTurnsRejected.Load() }

// reserveDailyTurn counts one started turn against the org-wide daily cap and
// reports whether the turn may run. The counter resets at the UTC day
// boundary and is not persisted. A cap of zero disables the check.
func (h *Handler) reserveDailyTurn() bool {
	if h.dailyTurnCap <= 0 {
		return true
	}
	day := h.now().UTC().Format("2006-01-02")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chatTurnDay != day {
		h.chatTurnDay = day
		h.chatTurnCount = 0
	}
	if h.chatTurnCount >= h.dailyTurnCap {
		chatTurnsRejected.Add(1)
		if h.chatCapWarnDay != day {
			h.chatCapWarnDay = day
			h.log.Warn("Daily chat turn cap reached; rejecting further turns until 00:00 UTC",
				slog.Int("cap", h.dailyTurnCap), slog.String("day", day))
		}
		return false
	}
	h.chatTurnCount++
	return true
}

// admitChatTurn runs the checks every chat turn shares: a runner exists, no
// other turn runs for the conversation, and the org-wide daily cap has room
// (reserveDailyTurn). When it refuses it has already told the user why; when it
// admits, the caller must call release when the turn ends.
func (h *Handler) admitChatTurn(ctx context.Context, contextID, threadID string) (release func(), ok bool) {
	send := func(s string) { _ = h.messenger.SendText(ctx, contextID, threadID, s) }
	if h.runner == nil {
		send("Chat is not available: no runner configured")
		return nil, false
	}
	// Two concurrent turns would resume the same session; reject the second.
	h.mu.Lock()
	if h.chatTurnBusy[contextID] {
		h.mu.Unlock()
		send("A turn is already running for this conversation. Wait for it to finish.")
		return nil, false
	}
	h.chatTurnBusy[contextID] = true
	h.mu.Unlock()
	release = func() {
		h.mu.Lock()
		delete(h.chatTurnBusy, contextID)
		h.mu.Unlock()
	}

	if !h.reserveDailyTurn() {
		release()
		send(DailyChatLimitMessage)
		return nil, false
	}
	return release, true
}

// handleChatTurn runs one read-only agentic chat turn for a web conversation.
func (h *Handler) handleChatTurn(ctx context.Context, contextID, threadID, text string) {
	send := func(s string) { _ = h.messenger.SendText(ctx, contextID, threadID, s) }
	release, ok := h.admitChatTurn(ctx, contextID, threadID)
	if !ok {
		return
	}
	defer release()

	projectPath := h.getActiveProjectPath(contextID)
	h.mu.Lock()
	resume := h.chatSessions[contextID]
	h.mu.Unlock()

	taskID := "chat-" + strings.TrimPrefix(contextID, "web:")
	startedAt := time.Now()
	turn := executor.ChatTurn{
		ConversationID: contextID, ProjectPath: projectPath, Text: text, ResumeSessionID: resume,
	}
	if ss, ok := h.messenger.(SubagentSender); ok {
		turn.OnSubagent = func(e executor.SubagentEvent) {
			_ = ss.SendSubagent(ctx, contextID, threadID, SubagentUpdate{
				TaskID: e.TaskID, Text: e.Text, Phase: e.Phase, DurationMs: e.DurationMs,
			})
		}
	}
	result, err := h.runner.ExecuteChatTurn(ctx, turn, func(line string) {
		_, _ = h.messenger.SendProgress(ctx, contextID, "", taskID, "research", 0, line)
	})

	// Only a turn reserveDailyTurn admitted gets here, so a cap-rejected turn
	// writes no row.
	h.recordChatTurn(contextID, projectPath, startedAt, result, err)

	// A changed SessionID is the only reset signal on the Claude Code backend.
	if result.SessionID != "" {
		h.mu.Lock()
		h.chatSessions[contextID] = result.SessionID
		h.mu.Unlock()
	}
	if err != nil {
		send("Chat turn failed: " + err.Error())
		return
	}
	// The context event goes out before the reply so the UI has the counts
	// when the text lands. Read-only turns create no memories.
	if cs, ok := h.messenger.(ContextSender); ok {
		_ = cs.SendContext(ctx, contextID, threadID, ContextCounts{
			DocsRead: result.DocsRead, MemoriesRead: result.MemoriesRead, MemoriesCreated: 0,
		})
	}

	reply, draft, found, blockErr := extractPilotIssueBlock(result.Text)
	if result.Partial {
		reply += "\n\n(Stopped early: turn limit or timeout.)"
	}
	if strings.TrimSpace(reply) != "" {
		_ = h.messenger.SendChunked(ctx, contextID, threadID, strings.TrimSpace(reply), "")
	}
	switch {
	case !found:
	case blockErr != nil:
		send("The issue draft was not valid: " + blockErr.Error())
	default:
		draft = h.rewriteUntrackedRefs(ctx, contextID, threadID, draft, projectPath)
		h.presentIssueCard(ctx, contextID, threadID, draft, projectPath)
	}
}

// ActionCreateTickets is the chat action that drafts tickets from a research
// report sent inline as the message text.
const ActionCreateTickets = "create_tickets"

// MaxDraftReportBytes caps the report a create_tickets action may carry.
const MaxDraftReportBytes = 64 * 1024

// ticketDraftingPrompt is the fixed instruction of a drafting turn; the report
// follows it. Body rules mirror .agent/sops/onboarding/new-project-issue-authoring.md.
const ticketDraftingPrompt = `You are drafting GitHub issues from the research report below. This is read-only: do not edit files and do not file anything. Use the read-only tools to check every file and symbol you cite against the checkout.

Reply with a short note, then ONE fenced block named pilot-drafts holding a YAML list of drafts. Each draft has the keys id, title, labels, body and deps:

- id: a short unique slug (for example d1, d2).
- title: a conventional-commit title, type(scope): description.
- labels: a list of label names (may be empty).
- body: a literal block with exactly these H2 sections, in order: "## Context", "## Acceptance", "## Implementation", "## Refs". Cite evidence as file:line inside backticks. Only backtick paths that exist in the checkout; write other paths as plain text. Under "## Acceptance" write checkbox bullets; at least one must be a backticked command from go, make, npm, npx, bun, bunx, pnpm, yarn, pytest or cargo followed by "passes; paste the output into the PR body" (one command span per bullet, no shell operators), or a mutation pin of the form "<change> -> <TestName> fails".
- deps: a list of ids of OTHER drafts in this block that must land first (ids only, never issue numbers).

One draft is one PR-sized change. If part of the report is bigger than that, still emit it as one draft; do not split it further.

` + "```pilot-drafts" + `
- id: d1
  title: "feat(scope): add the thing"
  labels: [enhancement]
  deps: []
  body: |
    ## Context
    ...
    ## Acceptance
    - [ ] ` + "`go test ./internal/x/`" + ` passes; paste the output into the PR body.
    ## Implementation
    ...
    ## Refs
    ...
` + "```" + `

The report:
`

// handleCreateTickets runs one drafting turn for the create_tickets action and
// emits a drafts event. Nothing is filed: the console owns approval and filing.
func (h *Handler) handleCreateTickets(ctx context.Context, contextID, threadID, report string) {
	send := func(s string) { _ = h.messenger.SendText(ctx, contextID, threadID, s) }
	if len(report) > MaxDraftReportBytes {
		send(fmt.Sprintf("The report is too large to draft tickets from (%d bytes, limit %d).", len(report), MaxDraftReportBytes))
		return
	}
	release, ok := h.admitChatTurn(ctx, contextID, threadID)
	if !ok {
		return
	}
	defer release()

	projectPath := h.getActiveProjectPath(contextID)
	taskID := "drafts-" + strings.TrimPrefix(contextID, "web:")
	progress := func(line string) {
		_, _ = h.messenger.SendProgress(ctx, contextID, "", taskID, "drafting", 0, line)
	}
	progress("Drafting tickets from the report")

	// A drafting turn never resumes or replaces the conversation's chat session.
	startedAt := time.Now()
	result, err := h.runner.ExecuteChatTurn(ctx, executor.ChatTurn{
		ConversationID: contextID, ProjectPath: projectPath, Text: ticketDraftingPrompt + report,
	}, progress)
	h.recordChatTurn(contextID, projectPath, startedAt, result, err)
	if err != nil {
		send("Ticket drafting failed: " + err.Error())
		return
	}

	reply, drafts, found, blockErr := extractPilotDraftsBlock(result.Text)
	switch {
	case !found:
		send("Ticket drafting failed: the reply had no pilot-drafts block.")
		return
	case blockErr != nil:
		send("Ticket drafting failed: the drafts block was not valid: " + blockErr.Error())
		return
	}
	checkDrafts(drafts, projectPath)

	if ds, ok := h.messenger.(DraftsSender); ok {
		_ = ds.SendDrafts(ctx, contextID, threadID, drafts)
	} else {
		var b strings.Builder
		for _, d := range drafts {
			fmt.Fprintf(&b, "- %s [%s]: %s\n", d.ID, d.Kind, d.Title)
		}
		send("Drafted tickets:\n" + b.String())
	}
	if strings.TrimSpace(reply) != "" {
		_ = h.messenger.SendChunked(ctx, contextID, threadID, strings.TrimSpace(reply), "")
	}
}

// recordChatTurn saves one chat_turns row for a finished turn, failed turns
// included, so chat spend is metered as its own unit (GH-5677). A save error
// is logged and never blocks the reply.
func (h *Handler) recordChatTurn(contextID, projectPath string, startedAt time.Time, result executor.ChatTurnResult, turnErr error) {
	if h.store == nil {
		return
	}
	completedAt := time.Now()
	duration := result.Duration
	if duration <= 0 {
		duration = completedAt.Sub(startedAt)
	}
	var idBytes [8]byte
	_, _ = rand.Read(idBytes[:])
	row := &memory.ChatTurn{
		ID:               "chat-" + hex.EncodeToString(idBytes[:]),
		ConversationID:   contextID,
		ProjectPath:      projectPath,
		Model:            result.Model,
		TokensInput:      result.TokensInput,
		TokensOutput:     result.TokensOutput,
		TokensCacheRead:  result.TokensCacheRead,
		TokensCacheWrite: result.TokensCacheWrite,
		EstimatedCostUSD: result.EstimatedCostUSD,
		ReportedCostUSD:  result.ReportedCostUSD,
		KeySource:        executor.KeySourceFromEnv(),
		BillingMode:      result.BillingMode, // GH-6016
		Status:           memory.ChatTurnCompleted,
		DurationMs:       duration.Milliseconds(),
		ToolCalls:        result.ToolCalls,
		Partial:          result.Partial,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
	}
	if turnErr != nil {
		row.Status = memory.ChatTurnFailed
		row.Error = turnErr.Error()
	}
	if err := h.store.SaveChatTurn(row); err != nil {
		h.log.Warn("failed to record chat turn", "conversation", contextID, "error", err)
	}
}

// rewriteUntrackedRefs turns backticked paths in the draft body that are not on
// the project's default branch into plain text. The dispatcher's base-presence
// gate holds any issue that backticks such a path (e.g. the untracked .agent
// scaffold on tenant clones), so rewriting here is cheaper than a rejected
// draft that costs another model turn and may repeat the mistake. A probe error
// fails open: the draft is returned unchanged.
func (h *Handler) rewriteUntrackedRefs(ctx context.Context, contextID, threadID string, draft IssueDraft, projectPath string) IssueDraft {
	refs := executor.ExtractReferencedPaths(draft.Body)
	if len(refs) == 0 || h.missingPathsOnBase == nil {
		return draft
	}
	missing, err := h.missingPathsOnBase(ctx, projectPath, refs)
	if err != nil {
		h.log.Warn("issue draft reference check failed; presenting draft unchanged", "project", projectPath, "error", err)
		return draft
	}
	if len(missing) == 0 {
		return draft
	}
	draft.Body = executor.UnquoteReferencedPaths(draft.Body, missing)
	_ = h.messenger.SendText(ctx, contextID, threadID,
		fmt.Sprintf("%d reference(s) not on the default branch were rewritten as plain text: %s", len(missing), strings.Join(missing, ", ")))
	return draft
}

// presentIssueCard stores the draft and asks the user to confirm filing it.
func (h *Handler) presentIssueCard(ctx context.Context, contextID, threadID string, draft IssueDraft, projectPath string) {
	if h.issueCreator == nil {
		_ = h.messenger.SendText(ctx, contextID, threadID,
			"GitHub is not configured for this project, so the issue cannot be filed. Here is the draft:\n\n"+draft.Title+"\n\n"+draft.Body)
		return
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	cardID := "issue-" + hex.EncodeToString(b[:])

	h.mu.Lock()
	h.pendingIssueCards[contextID] = &pendingIssueCard{draft, cardID, projectPath, time.Now()}
	h.mu.Unlock()

	preview := []rune(draft.Body)
	if len(preview) > 400 {
		preview = preview[:400]
	}
	project := ""
	if h.projects != nil {
		if p := h.projects.GetProjectByPath(projectPath); p != nil {
			project = p.Name
		}
	}
	_, _ = h.messenger.SendConfirmation(ctx, contextID, threadID, cardID,
		"File Pilot issue: "+draft.Title+"\n\n"+string(preview), project)
}

// handleIssueCardDecision files the pending issue card when confirmed and
// discards it otherwise.
func (h *Handler) handleIssueCardDecision(ctx context.Context, contextID, threadID string, confirmed bool) {
	h.mu.Lock()
	card, ok := h.pendingIssueCards[contextID]
	delete(h.pendingIssueCards, contextID)
	h.mu.Unlock()

	send := func(s string) { _ = h.messenger.SendText(ctx, contextID, threadID, s) }
	switch {
	case !ok:
		send("No pending issue card.")
		return
	case !confirmed:
		send("Issue draft discarded.")
		return
	case time.Since(card.CreatedAt) > issueCardTTL:
		send("Issue card expired. Ask again to draft it.")
		return
	}

	draft := card.Draft
	bullet := "- Filed from Pilot Console chat " + strings.TrimPrefix(contextID, "web:")
	draft.Body = strings.TrimRight(draft.Body, " \t\r\n")
	if refsHeadingRE.MatchString(draft.Body) {
		draft.Body += "\n" + bullet
	} else {
		draft.Body += "\n\n## Refs\n" + bullet
	}
	draft.Labels = withPilotLabel(draft.Labels)

	url, err := h.issueCreator.CreateIssue(ctx, card.ProjectPath, draft)
	if err != nil {
		send("Issue filing failed: " + err.Error())
		return
	}
	send("Filed " + url)
}
