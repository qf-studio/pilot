package comms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
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

// handleChatTurn runs one read-only agentic chat turn for a web conversation.
func (h *Handler) handleChatTurn(ctx context.Context, contextID, threadID, text string) {
	send := func(s string) { _ = h.messenger.SendText(ctx, contextID, threadID, s) }
	if h.runner == nil {
		send("Chat is not available: no runner configured")
		return
	}
	// Two concurrent turns would resume the same session; reject the second.
	h.mu.Lock()
	if h.chatTurnBusy[contextID] {
		h.mu.Unlock()
		send("A turn is already running for this conversation. Wait for it to finish.")
		return
	}
	h.chatTurnBusy[contextID] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.chatTurnBusy, contextID)
		h.mu.Unlock()
	}()

	projectPath := h.getActiveProjectPath(contextID)
	h.mu.Lock()
	resume := h.chatSessions[contextID]
	h.mu.Unlock()

	taskID := "chat-" + strings.TrimPrefix(contextID, "web:")
	result, err := h.runner.ExecuteChatTurn(ctx, executor.ChatTurn{
		ConversationID: contextID, ProjectPath: projectPath, Text: text, ResumeSessionID: resume,
	}, func(line string) {
		_, _ = h.messenger.SendProgress(ctx, contextID, "", taskID, "research", 0, line)
	})

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
