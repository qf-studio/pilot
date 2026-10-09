package executor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// chatFakeBackend records every ExecuteOptions it receives and replays
// scripted events. errs are returned in order per call (nil = success).
type chatFakeBackend struct {
	mu     sync.Mutex
	calls  []ExecuteOptions
	events []BackendEvent
	errs   []error
	block  bool
	output string
}

func (b *chatFakeBackend) Name() string      { return "chat-fake" }
func (b *chatFakeBackend) IsAvailable() bool { return true }

func (b *chatFakeBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	b.mu.Lock()
	b.calls = append(b.calls, opts)
	idx := len(b.calls) - 1
	var err error
	if idx < len(b.errs) {
		err = b.errs[idx]
	}
	b.mu.Unlock()

	if b.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	for _, ev := range b.events {
		if opts.EventHandler != nil {
			opts.EventHandler(ev)
		}
	}
	return &BackendResult{Success: true, Output: b.output, SessionID: "sess-1"}, nil
}

func (b *chatFakeBackend) call(i int) ExecuteOptions {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[i]
}

func newChatRunner(b Backend) *Runner { return NewRunnerWithBackend(b) }

func toolUse(name string, input map[string]interface{}) BackendEvent {
	return BackendEvent{Type: EventTypeToolUse, ToolName: name, ToolInput: input}
}

func disableChatProgressThrottle(t *testing.T) {
	t.Helper()
	old := chatProgressInterval
	chatProgressInterval = 0
	t.Cleanup(func() { chatProgressInterval = old })
}

func TestChatTurn_AllowlistExcludesWriteTools(t *testing.T) {
	b := &chatFakeBackend{output: "ok"}
	_, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	got := b.call(0).AllowedTools
	if !reflect.DeepEqual(got, ChatReadOnlyTools) {
		t.Fatalf("AllowedTools = %v, want %v", got, ChatReadOnlyTools)
	}
	for _, tool := range got {
		switch tool {
		case "Edit", "Write", "MultiEdit", "NotebookEdit", "Bash":
			t.Errorf("allowlist contains forbidden tool %q", tool)
		}
	}
	if len(ChatReadOnlyTools) != 12 {
		t.Errorf("len(ChatReadOnlyTools) = %d, want 12", len(ChatReadOnlyTools))
	}
}

func TestChatTurn_ResumePassedToBackend(t *testing.T) {
	b := &chatFakeBackend{output: "ok"}
	r := newChatRunner(b)
	dir := t.TempDir()

	first, err := r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: dir, Text: "first"}, nil)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: dir, Text: "second", ResumeSessionID: first.SessionID}, nil); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	got := b.call(1)
	if got.ResumeSessionID != "sess-1" {
		t.Errorf("ResumeSessionID = %q, want sess-1", got.ResumeSessionID)
	}
	if got.Prompt != "second" {
		t.Errorf("Prompt = %q, want user text only", got.Prompt)
	}
}

func TestChatTurn_FirstTurnUsesChatPrompt(t *testing.T) {
	b := &chatFakeBackend{output: "ok"}
	_, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "where is the router?"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	got := b.call(0)
	if !strings.Contains(got.Prompt, chatPersonaLine) {
		t.Errorf("first-turn prompt missing persona line")
	}
	if !strings.Contains(got.Prompt, "where is the router?") {
		t.Errorf("first-turn prompt missing user text")
	}
	if got.MaxTurns != 40 {
		t.Errorf("MaxTurns = %d, want default 40", got.MaxTurns)
	}
}

func TestChatTurn_ProgressForwardsToolUse(t *testing.T) {
	disableChatProgressThrottle(t)
	b := &chatFakeBackend{
		output: "done",
		events: []BackendEvent{
			toolUse("Read", map[string]interface{}{"file_path": "internal/comms/handler.go"}),
			toolUse("Grep", map[string]interface{}{"pattern": "IssueCreator"}),
			toolUse("Grep", map[string]interface{}{"pattern": "IssueCreator"}),
			toolUse("Bash", map[string]interface{}{"command": "git log --oneline -5"}),
		},
	}
	var lines []string
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "q"}, func(m string) { lines = append(lines, m) })
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	want := []string{"Read internal/comms/handler.go", "Grep IssueCreator", "Bash git log --oneline -5"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("progress lines = %q, want %q", lines, want)
	}
	if res.ToolCalls != 4 {
		t.Errorf("ToolCalls = %d, want 4", res.ToolCalls)
	}
}

func TestChatTurn_ProgressThrottleKeepsLastPending(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	p := newChatProgressThrottle(func(m string) {
		mu.Lock()
		lines = append(lines, m)
		mu.Unlock()
	}, 50*time.Millisecond)
	p.Offer("a")
	p.Offer("b")
	p.Offer("c")
	time.Sleep(150 * time.Millisecond)
	p.Close()
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"a", "c"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}

func TestChatProgressLine(t *testing.T) {
	long := strings.Repeat("x", 120)
	tests := []struct {
		ev   BackendEvent
		want string
	}{
		{toolUse("LS", map[string]interface{}{"path": "internal"}), "LS internal"},
		{toolUse("Glob", map[string]interface{}{"pattern": "**/*.go"}), "Glob **/*.go"},
		{toolUse("Task", map[string]interface{}{"description": "survey"}), "Task survey"},
		{toolUse("Bash", map[string]interface{}{"command": long}), "Bash " + long[:80]},
		{toolUse("WebFetch", map[string]interface{}{"url": "x"}), "WebFetch"},
	}
	for _, tt := range tests {
		if got := chatProgressLine(tt.ev); got != tt.want {
			t.Errorf("chatProgressLine(%s) = %q, want %q", tt.ev.ToolName, got, tt.want)
		}
	}
}

func TestChatTurn_SessionNotFoundResets(t *testing.T) {
	b := &chatFakeBackend{
		output: "ok",
		errs:   []error{&ClaudeCodeError{Type: ErrorTypeSessionNotFound, Message: "gone"}},
	}
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(),
		ChatTurn{ProjectPath: t.TempDir(), Text: "again", ResumeSessionID: "stale"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if !res.SessionReset {
		t.Error("SessionReset = false, want true")
	}
	if got := b.call(1); got.ResumeSessionID != "" || !strings.Contains(got.Prompt, chatPersonaLine) {
		t.Errorf("retry should run fresh with the chat prompt, got resume=%q", got.ResumeSessionID)
	}
}

func TestChatTurn_TimeoutReturnsPartial(t *testing.T) {
	b := &chatFakeBackend{block: true}
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(),
		ChatTurn{ProjectPath: t.TempDir(), Text: "slow", Timeout: 50 * time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !res.Partial {
		t.Error("Partial = false, want true")
	}
}

func TestChatTurn_MaxTurnsReturnsPartial(t *testing.T) {
	b := &chatFakeBackend{
		events: []BackendEvent{
			{Type: EventTypeText, Message: "so far"},
			{Type: EventTypeResult, IsError: true, Raw: `{"type":"result","subtype":"error_max_turns"}`},
		},
	}
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "q"}, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !res.Partial || res.Text != "so far" {
		t.Errorf("got Partial=%v Text=%q, want partial with collected text", res.Partial, res.Text)
	}
}

func TestChatTurn_NoTaskSideEffects(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agent", "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []string {
		var out []string
		_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
			if err == nil {
				out = append(out, p)
			}
			return nil
		})
		return out
	}
	before := snapshot()

	b := &chatFakeBackend{output: "ok", events: []BackendEvent{toolUse("Read", map[string]interface{}{"file_path": "a.go"})}}
	if _, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: dir, Text: "q"}, nil); err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Errorf("project dir changed: before=%v after=%v", before, after)
	}
}

func TestChatPrompt_ContainsNavigatorMethod(t *testing.T) {
	p := BuildChatPrompt("/repo", "hello")
	for _, want := range []string{".agent/DEVELOPMENT-README.md", "graph.json", "path:line", chatReadOnlyStatement, "--- User message ---\nhello"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if !strings.HasPrefix(p, chatPersonaLine) {
		t.Error("prompt must open with the persona line")
	}
	if strings.Index(p, ".agent/DEVELOPMENT-README.md") > strings.Index(p, "pilot-issue") {
		t.Error("research method must precede the issue contract")
	}
}

func TestChatPrompt_ExcludesExecutorPersona(t *testing.T) {
	p := BuildChatPrompt("/repo", "hello")
	for _, banned := range []string{"Phase 2: IMPLEMENT", "write code NOW", "do not ask to create a GitHub issue"} {
		if strings.Contains(p, banned) {
			t.Errorf("prompt contains executor phrase %q", banned)
		}
	}
}

func TestChatPrompt_IssueContract(t *testing.T) {
	p := BuildChatPrompt("/repo", "file an issue")
	for _, want := range []string{"pilot-issue", "Blocked by: #N", "never in backticks"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
