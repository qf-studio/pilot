package comms

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
)

// webChatBackend records calls; call n returns outputs[n] and sessions[n]
// (defaults "ok" / "sess-1") after replaying events.
type webChatBackend struct {
	calls    []executor.ExecuteOptions
	events   []executor.BackendEvent
	outputs  []string
	sessions []string
}

func (b *webChatBackend) Name() string      { return "web-chat-fake" }
func (b *webChatBackend) IsAvailable() bool { return true }

func (b *webChatBackend) Execute(_ context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	b.calls = append(b.calls, opts)
	idx := len(b.calls) - 1
	for _, ev := range b.events {
		opts.EventHandler(ev)
	}
	res := &executor.BackendResult{Success: true, Output: "ok", SessionID: "sess-1"}
	if idx < len(b.outputs) {
		res.Output = b.outputs[idx]
	}
	if idx < len(b.sessions) {
		res.SessionID = b.sessions[idx]
	}
	return res, nil
}

type webChatMessenger struct {
	mockMessenger // messages records SendText and SendChunked bodies
	progress      []string
	confirmIDs    []string
	confirmDescs  []string
}

func (m *webChatMessenger) SendProgress(_ context.Context, _, _, _, _ string, _ int, detail string) (string, error) {
	m.progress = append(m.progress, detail)
	return "", nil
}

func (m *webChatMessenger) SendConfirmation(_ context.Context, _, _, taskID, desc, _ string) (string, error) {
	m.confirmIDs = append(m.confirmIDs, taskID)
	m.confirmDescs = append(m.confirmDescs, desc)
	return taskID, nil
}

func (m *webChatMessenger) last() string { return m.messages[len(m.messages)-1] }

const validBlock = "```pilot-issue\ntitle: \"feat(comms): add widget\"\nlabels: [enhancement]\nbody: |\n  ## Context\n  Add a widget.\n```"

func newWebChat(b *webChatBackend, ic IssueCreator) (*Handler, *webChatMessenger) {
	msgr := &webChatMessenger{}
	cfg := &HandlerConfig{Messenger: msgr, Runner: executor.NewRunnerWithBackend(b), ProjectPath: "/work/proj"}
	if ic != nil {
		cfg.IssueCreator = ic
	}
	return NewHandler(cfg), msgr
}

func send(h *Handler, platform, text string) {
	h.HandleMessage(context.Background(), &IncomingMessage{ContextID: "web:conv-1", Platform: platform, Text: text, SenderID: "u1"})
}

func callback(h *Handler, action string) {
	h.HandleMessage(context.Background(), &IncomingMessage{ContextID: "web:conv-1", Platform: "web", IsCallback: true, ActionID: action, SenderID: "u1"})
}

func isChatTurn(o executor.ExecuteOptions) bool {
	return reflect.DeepEqual(o.AllowedTools, executor.ChatReadOnlyTools)
}

// cardHandler returns a handler with an issue card pending.
func cardHandler(t *testing.T, ic *mockIssueCreator) (*Handler, *webChatMessenger) {
	t.Helper()
	h, msgr := newWebChat(&webChatBackend{outputs: []string{"Draft:\n\n" + validBlock}}, ic)
	send(h, "web", "file an issue")
	if len(msgr.confirmIDs) != 1 {
		t.Fatalf("setup: confirmations = %d, want 1", len(msgr.confirmIDs))
	}
	return h, msgr
}

func TestWebChat_PlainTextRunsChatTurn(t *testing.T) {
	b := &webChatBackend{outputs: []string{"the router is in gateway"}}
	h, msgr := newWebChat(b, nil)
	send(h, "web", "where is the router?")
	if len(b.calls) != 1 || !isChatTurn(b.calls[0]) {
		t.Fatalf("backend calls = %d, want 1 chat turn", len(b.calls))
	}
	if len(msgr.messages) != 1 || msgr.messages[0] != "the router is in gateway" {
		t.Errorf("messages = %q, want the backend output", msgr.messages)
	}
}

func TestWebChat_NonWebPlatformUnchanged(t *testing.T) {
	b := &webChatBackend{}
	h, _ := newWebChat(b, nil)
	send(h, "telegram", "where is the router?")
	for i, c := range b.calls {
		if isChatTurn(c) {
			t.Fatalf("telegram text ran a chat turn (call %d)", i)
		}
	}
}

func TestWebChat_OperationalQueryKeepsOldPath(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b := &webChatBackend{}
	msgr := &webChatMessenger{}
	h := NewHandler(&HandlerConfig{Messenger: msgr, Runner: executor.NewRunnerWithBackend(b), ProjectPath: "/work/proj", Store: store})
	send(h, "web", "queue status")
	for i, c := range b.calls {
		if isChatTurn(c) {
			t.Fatalf("operational query ran a chat turn (call %d)", i)
		}
	}
	if len(msgr.messages) != 1 || msgr.messages[0] != "📋 Queue is empty" {
		t.Errorf("messages = %q, want the operational queue reply", msgr.messages)
	}
}

func TestWebChat_SlashCommandBypassesChatTurn(t *testing.T) {
	b := &webChatBackend{}
	h, _ := newWebChat(b, nil)
	send(h, "web", "/status")
	if len(b.calls) != 0 {
		t.Fatalf("backend calls = %d, want 0", len(b.calls))
	}
}

func TestWebChat_SecondTurnResumes(t *testing.T) {
	b := &webChatBackend{}
	h, _ := newWebChat(b, nil)
	send(h, "web", "first")
	send(h, "web", "second")
	if len(b.calls) != 2 || b.calls[0].ResumeSessionID != "" || b.calls[1].ResumeSessionID != "sess-1" {
		t.Fatalf("resume ids wrong: %d calls", len(b.calls))
	}
}

func TestWebChat_SessionIdChangeIsStored(t *testing.T) {
	b := &webChatBackend{sessions: []string{"sess-1", "sess-2", "sess-2"}}
	h, _ := newWebChat(b, nil)
	for _, text := range []string{"one", "two", "three"} {
		send(h, "web", text)
	}
	if b.calls[1].ResumeSessionID != "sess-1" || b.calls[2].ResumeSessionID != "sess-2" {
		t.Errorf("resume ids = %q, %q; want sess-1, sess-2", b.calls[1].ResumeSessionID, b.calls[2].ResumeSessionID)
	}
}

func TestWebChat_ProgressForwarded(t *testing.T) {
	b := &webChatBackend{events: []executor.BackendEvent{{
		Type: executor.EventTypeToolUse, ToolName: "Read", ToolInput: map[string]interface{}{"file_path": "internal/comms/handler.go"},
	}}}
	h, msgr := newWebChat(b, nil)
	send(h, "web", "look")
	if len(msgr.progress) != 1 || !strings.HasPrefix(msgr.progress[0], "Read ") {
		t.Fatalf("progress = %q, want one line starting with \"Read \"", msgr.progress)
	}
}

func TestWebChat_IssueBlockPresentsCard(t *testing.T) {
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/1"}
	_, msgr := cardHandler(t, ic)
	if len(msgr.messages) != 1 || strings.Contains(msgr.messages[0], "pilot-issue") {
		t.Fatalf("messages = %q, want one text without the block", msgr.messages)
	}
	if id := msgr.confirmIDs[0]; !strings.HasPrefix(id, "issue-") || len(id) != len("issue-")+8 {
		t.Errorf("taskID = %q, want issue-<8 hex>", id)
	}
	if !strings.Contains(msgr.confirmDescs[0], "feat(comms): add widget") || len(ic.calls) != 0 {
		t.Errorf("desc = %q, CreateIssue calls = %d", msgr.confirmDescs[0], len(ic.calls))
	}
}

func TestWebChat_FileCallbackCreatesIssue(t *testing.T) {
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/42"}
	h, msgr := cardHandler(t, ic)
	callback(h, "file")
	if len(ic.calls) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(ic.calls))
	}
	c := ic.calls[0]
	if c.projectPath != "/work/proj" || !strings.HasSuffix(c.draft.Body, "\n\n## Refs\n- Filed from Pilot Console chat conv-1") {
		t.Errorf("projectPath = %q, body = %q", c.projectPath, c.draft.Body)
	}
	if !reflect.DeepEqual(c.draft.Labels, []string{"enhancement", "pilot"}) {
		t.Errorf("labels = %v", c.draft.Labels)
	}
	if !strings.Contains(msgr.last(), ic.url) {
		t.Errorf("last message = %q, want the URL", msgr.last())
	}
}

func TestWebChat_DiscardCallbackCreatesNothing(t *testing.T) {
	ic := &mockIssueCreator{}
	h, msgr := cardHandler(t, ic)
	callback(h, "discard")
	if len(ic.calls) != 0 || msgr.last() != "Issue draft discarded." {
		t.Errorf("calls = %d, last = %q", len(ic.calls), msgr.last())
	}
}

func TestWebChat_YesTextConfirmsCard(t *testing.T) {
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/7"}
	h, _ := cardHandler(t, ic)
	send(h, "web", "yes")
	if len(ic.calls) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(ic.calls))
	}
}

func TestWebChat_MalformedBlockSendsError(t *testing.T) {
	bad := "```pilot-issue\ntitle: add stuff\nbody: do it\n```"
	h, msgr := newWebChat(&webChatBackend{outputs: []string{bad}}, &mockIssueCreator{})
	send(h, "web", "file it")
	if len(msgr.confirmIDs) != 0 || len(msgr.messages) != 1 || !strings.HasPrefix(msgr.messages[0], "The issue draft was not valid") {
		t.Errorf("confirms = %d, messages = %q", len(msgr.confirmIDs), msgr.messages)
	}
}

func TestWebChat_NoIssueCreatorShowsDraft(t *testing.T) {
	h, msgr := newWebChat(&webChatBackend{outputs: []string{"Draft:\n\n" + validBlock}}, nil)
	send(h, "web", "file it")
	if len(msgr.confirmIDs) != 0 || !strings.Contains(msgr.last(), "GitHub is not configured") {
		t.Errorf("confirms = %d, last = %q", len(msgr.confirmIDs), msgr.last())
	}
}

func TestWebChat_PartialAppendsNote(t *testing.T) {
	b := &webChatBackend{
		outputs: []string{"half an answer"},
		events:  []executor.BackendEvent{{Type: executor.EventTypeResult, Raw: `{"subtype":"error_max_turns"}`}},
	}
	h, msgr := newWebChat(b, nil)
	send(h, "web", "dig deep")
	if len(msgr.messages) != 1 || !strings.HasSuffix(msgr.messages[0], "(Stopped early: turn limit or timeout.)") {
		t.Fatalf("messages = %q, want text ending with the partial note", msgr.messages)
	}
}

func TestExtractPilotIssueBlock(t *testing.T) {
	tests := []struct {
		name, reply, wantErr, wantText string
		wantFound                      bool
		wantLabels                     []string
	}{
		{name: "no block", reply: "just prose", wantText: "just prose"},
		{name: "valid block", reply: "Draft.\n\n" + validBlock + "\n", wantFound: true, wantText: "Draft.", wantLabels: []string{"enhancement", "pilot"}},
		{name: "missing body", reply: "```pilot-issue\ntitle: \"fix(a): b\"\n```", wantFound: true, wantErr: "body is empty"},
		{name: "title without type", reply: "```pilot-issue\ntitle: add stuff\nbody: hi\n```", wantFound: true, wantErr: "conventional-commit"},
		{name: "pilot label kept once", reply: "```pilot-issue\ntitle: \"fix: b\"\nlabels: [pilot]\nbody: hi\n```", wantFound: true, wantLabels: []string{"pilot"}},
		{name: "invalid yaml", reply: "```pilot-issue\ntitle: [unclosed\n```", wantFound: true, wantErr: "invalid YAML"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, draft, found, err := extractPilotIssueBlock(tt.reply)
			if found != tt.wantFound || (err == nil) != (tt.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("found = %v, err = %v; want found %v, err %q", found, err, tt.wantFound, tt.wantErr)
			}
			if tt.wantText != "" && text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
			if tt.wantLabels != nil && !reflect.DeepEqual(draft.Labels, tt.wantLabels) {
				t.Errorf("labels = %v, want %v", draft.Labels, tt.wantLabels)
			}
		})
	}
}
