package comms

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
)

const (
	// webPlatform is the IncomingMessage.Platform of the Pilot Console chat.
	webPlatform = "web"

	issueCardTTL         = 30 * time.Minute
	issueCardBodyPreview = 400
	chatPartialNote      = "(Stopped early: turn limit or timeout.)"
)

var refsHeadingRE = regexp.MustCompile(`(?m)^## Refs[ \t]*$`)

// pendingIssueCard is a drafted pilot issue awaiting the user's decision.
type pendingIssueCard struct {
	Draft       IssueDraft
	CardID      string
	ProjectPath string
	CreatedAt   time.Time
}

func (h *Handler) hasPendingIssueCard(contextID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.pendingIssueCards[contextID]
	return ok
}

// handleChatTurn runs one read-only agentic chat turn for a web conversation,
// forwarding tool progress and turning a pilot-issue block into a card.
func (h *Handler) handleChatTurn(ctx context.Context, contextID, threadID, text string) {
	if h.runner == nil {
		_ = h.messenger.SendText(ctx, contextID, threadID, "Chat is not available: no runner configured")
		return
	}

	projectPath := h.getActiveProjectPath(contextID)
	h.mu.Lock()
	resume := h.chatSessions[contextID]
	h.mu.Unlock()

	convID := strings.TrimPrefix(contextID, "web:")
	progressTask := "chat-" + convID
	result, err := h.runner.ExecuteChatTurn(ctx, executor.ChatTurn{
		ConversationID:  contextID,
		ProjectPath:     projectPath,
		Text:            text,
		ResumeSessionID: resume,
	}, func(line string) {
		_, _ = h.messenger.SendProgress(ctx, contextID, "", progressTask, "research", 0, line)
	})

	// Decision: a changed SessionID is the only reset signal on the Claude Code
	// backend (it retries without resume internally), so always adopt the
	// latest id.
	if result.SessionID != "" {
		h.mu.Lock()
		h.chatSessions[contextID] = result.SessionID
		h.mu.Unlock()
	}

	if err != nil {
		_ = h.messenger.SendText(ctx, contextID, threadID, "Chat turn failed: "+err.Error())
		return
	}

	reply, draft, found, blockErr := extractPilotIssueBlock(result.Text)
	if result.Partial {
		if reply != "" {
			reply += "\n\n"
		}
		reply += chatPartialNote
	}
	if strings.TrimSpace(reply) != "" {
		_ = h.messenger.SendChunked(ctx, contextID, threadID, reply, "")
	}

	switch {
	case !found:
	case blockErr != nil:
		_ = h.messenger.SendText(ctx, contextID, threadID, "The issue draft was not valid: "+blockErr.Error())
	default:
		h.presentIssueCard(ctx, contextID, threadID, draft, projectPath)
	}
}

// presentIssueCard stores the draft and asks the user to confirm filing it.
// Nothing is filed until handleIssueCardDecision runs with confirmed true.
func (h *Handler) presentIssueCard(ctx context.Context, contextID, threadID string, draft IssueDraft, projectPath string) {
	if h.issueCreator == nil {
		_ = h.messenger.SendText(ctx, contextID, threadID,
			"GitHub is not configured for this project, so the issue cannot be filed. Here is the draft:\n\n"+draft.Title+"\n\n"+draft.Body)
		return
	}

	var idBytes [4]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		h.log.Warn("issue card id generation failed", slog.Any("error", err))
		_ = h.messenger.SendText(ctx, contextID, threadID, "Issue card failed: "+err.Error())
		return
	}
	cardID := "issue-" + hex.EncodeToString(idBytes[:])

	h.mu.Lock()
	h.pendingIssueCards[contextID] = &pendingIssueCard{
		Draft:       draft,
		CardID:      cardID,
		ProjectPath: projectPath,
		CreatedAt:   time.Now(),
	}
	h.mu.Unlock()

	preview := draft.Body
	if rs := []rune(preview); len(rs) > issueCardBodyPreview {
		preview = string(rs[:issueCardBodyPreview])
	}
	projectName := ""
	if h.projects != nil {
		if proj := h.projects.GetProjectByPath(projectPath); proj != nil {
			projectName = proj.Name
		}
	}
	_, _ = h.messenger.SendConfirmation(ctx, contextID, threadID, cardID,
		"File Pilot issue: "+draft.Title+"\n\n"+preview, projectName)
}

// handleIssueCardDecision resolves the pending issue card: files it when
// confirmed, discards it otherwise.
func (h *Handler) handleIssueCardDecision(ctx context.Context, contextID, threadID string, confirmed bool) {
	h.mu.Lock()
	card, ok := h.pendingIssueCards[contextID]
	if ok {
		delete(h.pendingIssueCards, contextID)
	}
	h.mu.Unlock()

	switch {
	case !ok:
		_ = h.messenger.SendText(ctx, contextID, threadID, "No pending issue card.")
		return
	case !confirmed:
		_ = h.messenger.SendText(ctx, contextID, threadID, "Issue draft discarded.")
		return
	case time.Since(card.CreatedAt) > issueCardTTL:
		_ = h.messenger.SendText(ctx, contextID, threadID, "Issue card expired. Ask again to draft it.")
		return
	}

	draft := card.Draft
	draft.Body = appendChatRefs(draft.Body, strings.TrimPrefix(contextID, "web:"))
	draft.Labels = withPilotLabel(draft.Labels)

	url, err := h.issueCreator.CreateIssue(ctx, card.ProjectPath, draft)
	if err != nil {
		_ = h.messenger.SendText(ctx, contextID, threadID, "Issue filing failed: "+err.Error())
		return
	}
	_ = h.messenger.SendText(ctx, contextID, threadID, "Filed "+url)
}

// appendChatRefs adds the "Filed from Pilot Console chat" bullet at the end of
// body, creating a "## Refs" section when the body has none.
func appendChatRefs(body, conversationID string) string {
	bullet := fmt.Sprintf("- Filed from Pilot Console chat %s", conversationID)
	body = strings.TrimRight(body, " \t\r\n")
	if refsHeadingRE.MatchString(body) {
		return body + "\n" + bullet
	}
	return body + "\n\n## Refs\n" + bullet
}
