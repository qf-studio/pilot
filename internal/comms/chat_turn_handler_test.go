package comms

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/pilot/internal/executor"
)

// webChatBackend records ExecuteOptions and replays scripted events. The
// session id returned on call n is sessions[n] ("sess-<n+1>" when unscripted).
type webChatBackend struct {
	mu       sync.Mutex
	calls    []executor.ExecuteOptions
	events   []executor.BackendEvent
	outputs  []string
	sessions []string
}

func (b *webChatBackend) Name() string      { return "web-chat-fake" }
func (b *webChatBackend) IsAvailable() bool { return true }

func (b *webChatBackend) Execute(_ context.Context, opts executor.ExecuteOptions) (*executor.BackendResult, error) {
	b.mu.Lock()
	b.calls = append(b.calls, opts)
	idx := len(b.calls) - 1
	b.mu.Unlock()

	for _, ev := range b.events {
		if opts.EventHandler != nil {
			opts.EventHandler(ev)
		}
	}
	out := "ok"
	if idx < len(b.outputs) {
		out = b.outputs[idx]
	} else if len(b.outputs) > 0 {
		out = b.outputs[len(b.outputs)-1]
	}
	sess := "sess-1"
	if idx < len(b.sessions) {
		sess = b.sessions[idx]
	}
	return &executor.BackendResult{Success: true, Output: out, SessionID: sess}, nil
}

func (b *webChatBackend) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *webChatBackend) call(i int) executor.ExecuteOptions {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[i]
}

type confirmCall struct{ taskID, desc, project string }

// webChatMessenger layers recording of progress, confirmations and chunked
// sends on top of mockMessenger (which records SendText/SendChunked bodies).
type webChatMessenger struct {
	mockMessenger
	mu       sync.Mutex
	chunked  []string
	progress []string
	confirms []confirmCall
	texts    []string
}

func (m *webChatMessenger) SendText(ctx context.Context, contextID, threadID, text string) error {
	m.mu.Lock()
	m.texts = append(m.texts, text)
	m.mu.Unlock()
	return m.mockMessenger.SendText(ctx, contextID, threadID, text)
}

func (m *webChatMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	m.mu.Lock()
	m.chunked = append(m.chunked, content)
	m.mu.Unlock()
	return m.mockMessenger.SendChunked(ctx, contextID, threadID, content, prefix)
}

func (m *webChatMessenger) SendProgress(_ context.Context, _, _, _, _ string, _ int, detail string) (string, error) {
	m.mu.Lock()
	m.progress = append(m.progress, detail)
	m.mu.Unlock()
	return "", nil
}

func (m *webChatMessenger) SendConfirmation(_ context.Context, _, _, taskID, desc, project string) (string, error) {
	m.mu.Lock()
	m.confirms = append(m.confirms, confirmCall{taskID, desc, project})
	m.mu.Unlock()
	return taskID, nil
}

const (
	webCtx     = "web:conv-1"
	validBlock = "```pilot-issue\ntitle: \"feat(comms): add widget\"\nlabels: [enhancement]\nbody: |\n  ## Context\n  Add a widget.\n```"
)

func newWebChatHandler(t *testing.T, b *webChatBackend, ic IssueCreator) (*Handler, *webChatMessenger) {
	t.Helper()
	msgr := &webChatMessenger{}
	cfg := &HandlerConfig{
		Messenger:   msgr,
		Runner:      executor.NewRunnerWithBackend(b),
		ProjectPath: "/work/proj",
	}
	if ic != nil {
		cfg.IssueCreator = ic
	}
	return NewHandler(cfg), msgr
}

func webMsg(platform, text string) *IncomingMessage {
	return &IncomingMessage{ContextID: webCtx, Platform: platform, Text: text, SenderID: "u1"}
}

func webCallback(action string) *IncomingMessage {
	return &IncomingMessage{ContextID: webCtx, Platform: "web", IsCallback: true, ActionID: action, SenderID: "u1"}
}

func TestWebChat_PlainTextRunsChatTurn(t *testing.T) {
	b := &webChatBackend{outputs: []string{"the router is in gateway"}}
	h, msgr := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("web", "where is the router?"))

	if b.callCount() != 1 {
		t.Fatalf("backend calls = %d, want 1", b.callCount())
	}
	if got := b.call(0).AllowedTools; !reflect.DeepEqual(got, executor.ChatReadOnlyTools) {
		t.Errorf("AllowedTools = %v, want %v", got, executor.ChatReadOnlyTools)
	}
	if len(msgr.chunked) != 1 || msgr.chunked[0] != "the router is in gateway" {
		t.Errorf("chunked = %q, want the backend output", msgr.chunked)
	}
}

func TestWebChat_NonWebPlatformUnchanged(t *testing.T) {
	b := &webChatBackend{}
	h, _ := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("telegram", "where is the router?"))

	// The old intent path may call the backend through Execute, but never as a
	// chat turn: that would carry the read-only allowlist.
	for i := 0; i < b.callCount(); i++ {
		if reflect.DeepEqual(b.call(i).AllowedTools, executor.ChatReadOnlyTools) {
			t.Fatalf("telegram text ran a chat turn (call %d)", i)
		}
	}
}

func TestWebChat_SlashCommandBypassesChatTurn(t *testing.T) {
	b := &webChatBackend{}
	h, _ := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("web", "/status"))

	if b.callCount() != 0 {
		t.Fatalf("backend calls = %d, want 0", b.callCount())
	}
}

func TestWebChat_SecondTurnResumes(t *testing.T) {
	b := &webChatBackend{}
	h, _ := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("web", "first"))
	h.HandleMessage(context.Background(), webMsg("web", "second"))

	if b.callCount() != 2 {
		t.Fatalf("backend calls = %d, want 2", b.callCount())
	}
	if got := b.call(0).ResumeSessionID; got != "" {
		t.Errorf("first ResumeSessionID = %q, want empty", got)
	}
	if got := b.call(1).ResumeSessionID; got != "sess-1" {
		t.Errorf("second ResumeSessionID = %q, want sess-1", got)
	}
}

func TestWebChat_SessionIdChangeIsStored(t *testing.T) {
	b := &webChatBackend{sessions: []string{"sess-1", "sess-2", "sess-2"}}
	h, _ := newWebChatHandler(t, b, nil)

	for _, text := range []string{"one", "two", "three"} {
		h.HandleMessage(context.Background(), webMsg("web", text))
	}

	if got := b.call(1).ResumeSessionID; got != "sess-1" {
		t.Errorf("second ResumeSessionID = %q, want sess-1", got)
	}
	if got := b.call(2).ResumeSessionID; got != "sess-2" {
		t.Errorf("third ResumeSessionID = %q, want sess-2", got)
	}
}

func TestWebChat_ProgressForwarded(t *testing.T) {
	b := &webChatBackend{events: []executor.BackendEvent{{
		Type:      executor.EventTypeToolUse,
		ToolName:  "Read",
		ToolInput: map[string]interface{}{"file_path": "internal/comms/handler.go"},
	}}}
	h, msgr := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("web", "look"))

	if len(msgr.progress) != 1 || !strings.HasPrefix(msgr.progress[0], "Read ") {
		t.Fatalf("progress = %q, want one line starting with \"Read \"", msgr.progress)
	}
}

func TestWebChat_IssueBlockPresentsCard(t *testing.T) {
	b := &webChatBackend{outputs: []string{"Here is a draft.\n\n" + validBlock}}
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/1"}
	h, msgr := newWebChatHandler(t, b, ic)

	h.HandleMessage(context.Background(), webMsg("web", "file an issue for a widget"))

	if len(msgr.chunked) != 1 || strings.Contains(msgr.chunked[0], "pilot-issue") {
		t.Fatalf("chunked = %q, want one text without the block", msgr.chunked)
	}
	if len(msgr.confirms) != 1 {
		t.Fatalf("confirmations = %d, want 1", len(msgr.confirms))
	}
	c := msgr.confirms[0]
	if !strings.HasPrefix(c.taskID, "issue-") || len(c.taskID) != len("issue-")+8 {
		t.Errorf("taskID = %q, want issue-<8 hex>", c.taskID)
	}
	if !strings.Contains(c.desc, "feat(comms): add widget") {
		t.Errorf("desc = %q, want it to contain the title", c.desc)
	}
	if len(ic.calls) != 0 {
		t.Errorf("CreateIssue called %d times before confirmation", len(ic.calls))
	}
}

func presentCard(t *testing.T, ic *mockIssueCreator) (*Handler, *webChatMessenger) {
	t.Helper()
	b := &webChatBackend{outputs: []string{"Draft:\n\n" + validBlock}}
	h, msgr := newWebChatHandler(t, b, ic)
	h.HandleMessage(context.Background(), webMsg("web", "file an issue"))
	if len(msgr.confirms) != 1 {
		t.Fatalf("setup: confirmations = %d, want 1", len(msgr.confirms))
	}
	return h, msgr
}

func TestWebChat_FileCallbackCreatesIssue(t *testing.T) {
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/42"}
	h, msgr := presentCard(t, ic)

	h.HandleMessage(context.Background(), webCallback("file"))

	if len(ic.calls) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(ic.calls))
	}
	call := ic.calls[0]
	if call.projectPath != "/work/proj" {
		t.Errorf("projectPath = %q, want /work/proj", call.projectPath)
	}
	if !strings.HasSuffix(call.draft.Body, "- Filed from Pilot Console chat conv-1") {
		t.Errorf("body = %q, want it to end with the Refs bullet", call.draft.Body)
	}
	if !strings.Contains(call.draft.Body, "\n## Refs\n") {
		t.Errorf("body = %q, want a new ## Refs section", call.draft.Body)
	}
	if !containsPilotLabel(call.draft.Labels) {
		t.Errorf("labels = %v, want pilot", call.draft.Labels)
	}
	if last := msgr.texts[len(msgr.texts)-1]; !strings.Contains(last, ic.url) {
		t.Errorf("last text = %q, want the issue URL", last)
	}
}

func containsPilotLabel(labels []string) bool {
	for _, l := range labels {
		if l == "pilot" {
			return true
		}
	}
	return false
}

func TestWebChat_DiscardCallbackCreatesNothing(t *testing.T) {
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/42"}
	h, msgr := presentCard(t, ic)

	h.HandleMessage(context.Background(), webCallback("discard"))

	if len(ic.calls) != 0 {
		t.Fatalf("CreateIssue calls = %d, want 0", len(ic.calls))
	}
	if last := msgr.texts[len(msgr.texts)-1]; last != "Issue draft discarded." {
		t.Errorf("last text = %q", last)
	}
}

func TestWebChat_YesTextConfirmsCard(t *testing.T) {
	ic := &mockIssueCreator{url: "https://github.com/o/r/issues/7"}
	h, _ := presentCard(t, ic)

	h.HandleMessage(context.Background(), webMsg("web", "yes"))

	if len(ic.calls) != 1 {
		t.Fatalf("CreateIssue calls = %d, want 1", len(ic.calls))
	}
}

func TestWebChat_FilingFailureReported(t *testing.T) {
	ic := &mockIssueCreator{err: errors.New("boom")}
	h, msgr := presentCard(t, ic)

	h.HandleMessage(context.Background(), webCallback("file"))

	if last := msgr.texts[len(msgr.texts)-1]; last != "Issue filing failed: boom" {
		t.Errorf("last text = %q", last)
	}
}

func TestWebChat_MalformedBlockSendsError(t *testing.T) {
	bad := "```pilot-issue\ntitle: add stuff\nbody: do it\n```"
	b := &webChatBackend{outputs: []string{"Draft:\n\n" + bad}}
	h, msgr := newWebChatHandler(t, b, &mockIssueCreator{})

	h.HandleMessage(context.Background(), webMsg("web", "file it"))

	if len(msgr.confirms) != 0 {
		t.Errorf("confirmations = %d, want 0", len(msgr.confirms))
	}
	if len(msgr.texts) != 1 || !strings.HasPrefix(msgr.texts[0], "The issue draft was not valid") {
		t.Errorf("texts = %q, want one starting with the validity error", msgr.texts)
	}
}

func TestWebChat_NoIssueCreatorShowsDraft(t *testing.T) {
	b := &webChatBackend{outputs: []string{"Draft:\n\n" + validBlock}}
	h, msgr := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("web", "file it"))

	if len(msgr.confirms) != 0 {
		t.Errorf("confirmations = %d, want 0", len(msgr.confirms))
	}
	if len(msgr.texts) != 1 || !strings.Contains(msgr.texts[0], "GitHub is not configured") ||
		!strings.Contains(msgr.texts[0], "feat(comms): add widget") {
		t.Errorf("texts = %q, want the not-configured note with the draft", msgr.texts)
	}
}

func TestWebChat_PartialAppendsNote(t *testing.T) {
	b := &webChatBackend{
		outputs: []string{"half an answer"},
		events:  []executor.BackendEvent{{Type: executor.EventTypeResult, Raw: `{"subtype":"error_max_turns"}`}},
	}
	h, msgr := newWebChatHandler(t, b, nil)

	h.HandleMessage(context.Background(), webMsg("web", "dig deep"))

	if len(msgr.chunked) != 1 || !strings.HasSuffix(msgr.chunked[0], "(Stopped early: turn limit or timeout.)") {
		t.Fatalf("chunked = %q, want text ending with the partial note", msgr.chunked)
	}
}

func TestWebChat_NoPendingCardYesFallsThrough(t *testing.T) {
	h, msgr := newWebChatHandler(t, &webChatBackend{}, nil)

	h.HandleMessage(context.Background(), webMsg("web", "yes"))

	if len(msgr.texts) != 1 || msgr.texts[0] != "No pending task to confirm." {
		t.Errorf("texts = %q, want the legacy no-pending-task reply", msgr.texts)
	}
}

func TestExtractPilotIssueBlock(t *testing.T) {
	tests := []struct {
		name       string
		reply      string
		wantFound  bool
		wantErr    string
		wantText   string
		wantTitle  string
		wantLabels []string
	}{
		{name: "no block", reply: "just prose", wantText: "just prose"},
		{
			name:       "valid block",
			reply:      "Draft below.\n\n" + validBlock + "\n",
			wantFound:  true,
			wantText:   "Draft below.",
			wantTitle:  "feat(comms): add widget",
			wantLabels: []string{"enhancement", "pilot"},
		},
		{
			name:      "missing body",
			reply:     "x\n```pilot-issue\ntitle: \"fix(a): b\"\n```",
			wantFound: true, wantErr: "body is empty", wantText: "x",
		},
		{
			name:      "title without type prefix",
			reply:     "```pilot-issue\ntitle: add stuff\nbody: hi\n```",
			wantFound: true, wantErr: "conventional-commit",
		},
		{
			name:       "pilot label not duplicated",
			reply:      "```pilot-issue\ntitle: \"fix: b\"\nlabels: [pilot]\nbody: hi\n```",
			wantFound:  true,
			wantTitle:  "fix: b",
			wantLabels: []string{"pilot"},
		},
		{
			name:      "invalid yaml",
			reply:     "```pilot-issue\ntitle: [unclosed\n```",
			wantFound: true, wantErr: "invalid YAML",
		},
		{
			name:      "unclosed block",
			reply:     "intro\n```pilot-issue\ntitle: \"fix: b\"\nbody: hi",
			wantFound: true, wantErr: "not closed", wantText: "intro",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, draft, found, err := extractPilotIssueBlock(tt.reply)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if tt.wantText != "" && text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
			if found && strings.Contains(text, "pilot-issue") {
				t.Errorf("text still contains the block: %q", text)
			}
			if tt.wantTitle != "" && draft.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", draft.Title, tt.wantTitle)
			}
			if tt.wantLabels != nil && !reflect.DeepEqual(draft.Labels, tt.wantLabels) {
				t.Errorf("labels = %v, want %v", draft.Labels, tt.wantLabels)
			}
		})
	}
}

func TestAppendChatRefs(t *testing.T) {
	got := appendChatRefs("## Context\nx\n\n## Refs\n- a\n", "c1")
	if got != "## Context\nx\n\n## Refs\n- a\n- Filed from Pilot Console chat c1" {
		t.Errorf("existing Refs: got %q", got)
	}
	got = appendChatRefs("body", "c1")
	if got != "body\n\n## Refs\n- Filed from Pilot Console chat c1" {
		t.Errorf("new Refs: got %q", got)
	}
}
