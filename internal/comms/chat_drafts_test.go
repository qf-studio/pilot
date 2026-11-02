package comms

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// draftsMessenger records progress phases and drafts events on top of
// webChatMessenger.
type draftsMessenger struct {
	webChatMessenger
	phases []string
	drafts [][]TicketDraft
	order  []string
}

func (m *draftsMessenger) SendProgress(ctx context.Context, c, r, taskID, phase string, p int, detail string) (string, error) {
	m.phases = append(m.phases, phase)
	m.order = append(m.order, "progress")
	return m.webChatMessenger.SendProgress(ctx, c, r, taskID, phase, p, detail)
}

func (m *draftsMessenger) SendDrafts(_ context.Context, _, _ string, d []TicketDraft) error {
	m.drafts = append(m.drafts, d)
	m.order = append(m.order, "drafts")
	return nil
}

func (m *draftsMessenger) SendChunked(ctx context.Context, c, t, content, prefix string) error {
	m.order = append(m.order, "text")
	return m.webChatMessenger.SendChunked(ctx, c, t, content, prefix)
}

const twoDraftsReply = "Two tickets.\n\n```pilot-drafts\n" +
	"- id: d1\n  title: \"fix(x): validate widget input\"\n  deps: []\n  body: |\n" +
	"    ## Context\n" +
	"    The widget in `internal/x/widget.go:10` never validates its input, which lets bad config through.\n" +
	"    ## Acceptance\n" +
	"    - [ ] `go test ./internal/x/ -run TestWidget` passes; paste the output into the PR body.\n" +
	"    ## Implementation\n    Validate it.\n    ## Refs\n    - report\n" +
	"- id: d2\n  title: \"Big rework\"\n  deps: [d1, d9]\n  body: |\n    ## Context\n    Rework everything.\n" +
	"```"

func newDraftsChat(t *testing.T, b *webChatBackend, cap int, clock *time.Time, store *memory.Store) (*Handler, *draftsMessenger) {
	t.Helper()
	msgr := &draftsMessenger{}
	cfg := &HandlerConfig{
		Messenger: msgr, Runner: executor.NewRunnerWithBackend(b), ProjectPath: draftProject(t),
		DailyTurnCap: cap, Store: store,
	}
	if clock != nil {
		cfg.Now = func() time.Time { return *clock }
	}
	return NewHandler(cfg), msgr
}

func createTickets(h *Handler, conv, report string) {
	h.HandleMessage(context.Background(), &IncomingMessage{
		ContextID: "web:" + conv, Platform: "web", Text: report, Action: ActionCreateTickets, SenderID: "u1",
	})
}

func TestCreateTickets_EmitsProgressThenDraftsThenCompletion(t *testing.T) {
	b := &webChatBackend{outputs: []string{twoDraftsReply}}
	h, msgr := newDraftsChat(t, b, 0, nil, nil)
	createTickets(h, "c1", "# Report\nThe widget is unvalidated.")

	if len(b.calls) != 1 || !isChatTurn(b.calls[0]) {
		t.Fatalf("backend calls = %d, want one read-only chat turn", len(b.calls))
	}
	if !strings.Contains(b.calls[0].Prompt, "pilot-drafts") || !strings.Contains(b.calls[0].Prompt, "The widget is unvalidated.") {
		t.Errorf("prompt lacks the fixed instruction or the report: %q", b.calls[0].Prompt)
	}
	if len(msgr.phases) == 0 || msgr.phases[0] != "drafting" {
		t.Errorf("progress phases = %v, want drafting first", msgr.phases)
	}
	if len(msgr.drafts) != 1 || len(msgr.drafts[0]) != 2 {
		t.Fatalf("drafts events = %+v, want one event with two drafts", msgr.drafts)
	}
	if got := strings.Join(msgr.order, ","); !strings.HasSuffix(got, "drafts,text") || !strings.HasPrefix(got, "progress") {
		t.Errorf("event order = %s, want progress, drafts, then the completion text", got)
	}
	d1, d2 := msgr.drafts[0][0], msgr.drafts[0][1]
	if d1.Kind != DraftKindTicket || !d1.Shippable.OK {
		t.Errorf("d1 = %+v, want a shippable ticket", d1)
	}
	if d2.Kind != DraftKindEpic || d2.Shippable.OK || len(d2.Shippable.Reasons) == 0 {
		t.Errorf("d2 = %+v, want an epic-candidate with reasons", d2)
	}
	if !hasReason(d2.Shippable.Reasons, `"d9"`) {
		t.Errorf("d2 reasons = %q, want the unknown dep named", d2.Shippable.Reasons)
	}
}

func TestCreateTickets_ParseFailureSendsTextAndNoDrafts(t *testing.T) {
	for name, out := range map[string]string{
		"no block":    "I could not find anything.",
		"bad block":   "```pilot-drafts\n- id: [oops\n```",
		"empty draft": "```pilot-drafts\n- id: a\n  title: \"\"\n  body: b\n```",
	} {
		t.Run(name, func(t *testing.T) {
			h, msgr := newDraftsChat(t, &webChatBackend{outputs: []string{out}}, 0, nil, nil)
			createTickets(h, "c1", "report")
			if len(msgr.drafts) != 0 {
				t.Fatalf("drafts events = %d, want none on a parse failure", len(msgr.drafts))
			}
			if len(msgr.messages) != 1 || !strings.HasPrefix(msgr.messages[0], "Ticket drafting failed:") {
				t.Errorf("messages = %q, want one failure text", msgr.messages)
			}
		})
	}
}

func TestCreateTickets_BackendErrorSendsText(t *testing.T) {
	h, msgr := newDraftsChat(t, &webChatBackend{err: context.DeadlineExceeded}, 0, nil, nil)
	createTickets(h, "c1", "report")
	if len(msgr.drafts) != 0 || len(msgr.messages) != 1 || !strings.Contains(msgr.messages[0], "Ticket drafting failed") {
		t.Errorf("drafts=%d messages=%q, want one failure text", len(msgr.drafts), msgr.messages)
	}
}

func TestCreateTickets_OversizeReportRefusedBeforeTurn(t *testing.T) {
	b := &webChatBackend{outputs: []string{twoDraftsReply}}
	h, msgr := newDraftsChat(t, b, 1, nil, nil)
	createTickets(h, "c1", strings.Repeat("x", MaxDraftReportBytes+1))
	if len(b.calls) != 0 || len(msgr.messages) != 1 || !strings.Contains(msgr.messages[0], "too large") {
		t.Errorf("calls=%d messages=%q, want a refusal with no turn", len(b.calls), msgr.messages)
	}
	if h.chatTurnCount != 0 {
		t.Errorf("chatTurnCount = %d, a refused report must not spend a turn", h.chatTurnCount)
	}
}

// TestCreateTickets_CallsReserveDailyTurn pins that the drafting turn counts
// against the org-wide cap: the 51st turn on a cap of 50 is refused with the
// daily-limit message and never reaches the backend.
func TestCreateTickets_CallsReserveDailyTurn(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	b := &webChatBackend{outputs: make([]string, 50)}
	for i := range b.outputs {
		b.outputs[i] = twoDraftsReply
	}
	h, msgr := newDraftsChat(t, b, 50, &clock, nil)
	before := ChatTurnsRejectedTotal()

	for i := 0; i < 50; i++ {
		// One conversation per turn: the per-conversation message rate limit is not under test.
		createTickets(h, fmt.Sprintf("c%d", i), "report")
	}
	if len(b.calls) != 50 || len(msgr.drafts) != 50 {
		t.Fatalf("calls=%d drafts=%d, want 50 admitted drafting turns", len(b.calls), len(msgr.drafts))
	}
	createTickets(h, "c51", "report")

	if len(b.calls) != 50 || len(msgr.drafts) != 50 {
		t.Errorf("calls=%d drafts=%d, the 51st turn must not run", len(b.calls), len(msgr.drafts))
	}
	if got := msgr.last(); got != DailyChatLimitMessage {
		t.Errorf("last message = %q, want %q", got, DailyChatLimitMessage)
	}
	if got := ChatTurnsRejectedTotal() - before; got != 1 {
		t.Errorf("rejected counter delta = %d, want 1", got)
	}
	// A refusal must release the busy slot so the conversation is not stuck.
	if h.chatTurnBusy["web:c51"] {
		t.Error("conversation still marked busy after a cap refusal")
	}
}

func TestCreateTickets_RecordsChatTurnRow(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h, _ := newDraftsChat(t, &webChatBackend{outputs: []string{twoDraftsReply}}, 0, nil, store)
	createTickets(h, "c1", "report")

	rows := chatRows(t, h)
	if len(rows) != 1 || rows[0].ConversationID != "web:c1" || rows[0].Status != memory.ChatTurnCompleted {
		t.Fatalf("rows = %+v, want one completed chat_turns row for web:c1", rows)
	}
}

func TestCreateTickets_DoesNotTouchChatSession(t *testing.T) {
	b := &webChatBackend{outputs: []string{"hello", twoDraftsReply}, sessions: []string{"chat-sess", "drafts-sess"}}
	h, _ := newDraftsChat(t, b, 0, nil, nil)
	sendTo(h, "c1", "where is the router?")
	createTickets(h, "c1", "report")
	if got := h.chatSessions["web:c1"]; got != "chat-sess" {
		t.Errorf("chat session = %q, want the drafting turn to leave it as chat-sess", got)
	}
	if b.calls[1].ResumeSessionID != "" {
		t.Errorf("drafting turn resumed session %q, want a fresh one", b.calls[1].ResumeSessionID)
	}
}

func TestCreateTickets_MessengerWithoutDraftsSenderGetsText(t *testing.T) {
	msgr := &webChatMessenger{}
	h := NewHandler(&HandlerConfig{
		Messenger: msgr, Runner: executor.NewRunnerWithBackend(&webChatBackend{outputs: []string{twoDraftsReply}}),
		ProjectPath: draftProject(t),
	})
	createTickets(h, "c1", "report")
	if len(msgr.messages) == 0 || !strings.Contains(msgr.messages[0], "d1 [ticket]") || !strings.Contains(msgr.messages[0], "d2 [epic]") {
		t.Errorf("messages = %q, want a text listing of both drafts", msgr.messages)
	}
}
