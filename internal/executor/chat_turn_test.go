package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	// usage, when set, is copied into the BackendResult (tokens, cost, model).
	usage *BackendResult
	// callEvents, when set, replaces events for the call with the same index
	// and is emitted even when that call then returns an error.
	callEvents [][]BackendEvent
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
	events := b.events
	if idx < len(b.callEvents) {
		events = b.callEvents[idx]
		for _, ev := range events {
			if opts.EventHandler != nil {
				opts.EventHandler(ev)
			}
		}
		events = nil
	}
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if opts.EventHandler != nil {
			opts.EventHandler(ev)
		}
	}
	res := &BackendResult{Success: true, Output: b.output, SessionID: "sess-1"}
	if u := b.usage; u != nil {
		res.TokensInput, res.TokensOutput = u.TokensInput, u.TokensOutput
		res.CacheReadInputTokens, res.CacheCreationInputTokens = u.CacheReadInputTokens, u.CacheCreationInputTokens
		res.ReportedCostUSD, res.Model = u.ReportedCostUSD, u.Model
	}
	return res, nil
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
	if len(ChatReadOnlyTools) != 13 {
		t.Errorf("len(ChatReadOnlyTools) = %d, want 13", len(ChatReadOnlyTools))
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

// mutation pin: BackendRan is set outside the backend-invoked path (true on the no-backend return, or dropped from the finished turn) -> this test fails
func TestChatTurn_BackendRanReflectsWhetherBackendWasInvoked(t *testing.T) {
	noBackend := newChatRunner(&chatFakeBackend{})
	noBackend.SetBackend(nil)
	tests := []struct {
		name    string
		runner  *Runner
		wantErr bool
		want    bool
	}{
		{"no backend configured", noBackend, true, false},
		{"backend ran and succeeded", newChatRunner(&chatFakeBackend{output: "ok"}), false, true},
		{"backend ran and failed", newChatRunner(&chatFakeBackend{errs: []error{errors.New("boom")}}), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.runner.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "q"}, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if res.BackendRan != tt.want {
				t.Errorf("BackendRan = %v, want %v", res.BackendRan, tt.want)
			}
		})
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

func TestChatPrompt_ForbidsFencesInBody(t *testing.T) {
	p := BuildChatPrompt("/repo", "file an issue")
	if !strings.Contains(p, "never use triple-backtick fences") {
		t.Error("prompt must forbid triple-backtick fences inside the issue body")
	}
}

func TestChatPrompt_ForbidsAgentDirRefs(t *testing.T) {
	p := BuildChatPrompt("/repo", "file an issue")
	if !strings.Contains(p, "Never backtick anything under .agent/") {
		t.Error("prompt must forbid backticking paths under .agent/ in the issue body")
	}
}

// GH-5688: ChatReadOnlyTools is an auto-approve list and does nothing under
// --dangerously-skip-permissions; the deny list and the write guard hold.
func TestChatTurn_DeniesWriteTools(t *testing.T) {
	b := &chatFakeBackend{output: "ok"}
	if _, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil); err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	got := b.call(0)
	if want := []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}; !reflect.DeepEqual(got.DisallowedTools, want) {
		t.Errorf("DisallowedTools = %v, want %v", got.DisallowedTools, want)
	}
	if got.WriteBoundary != WriteBoundaryDenyAll {
		t.Errorf("WriteBoundary = %q, want the deny-all boundary", got.WriteBoundary)
	}
}

// The chat turn must reach the CLI as --disallowedTools with all four tools.
func TestChatTurn_DeniesWriteToolsOnTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-CLI test relies on shell scripts")
	}
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args")
	script := filepath.Join(tmp, "fake-claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > "+argsFile+"\nexit 0\n"), 0o755); err != nil { //nolint:gosec // fake CLI must be executable
		t.Fatal(err)
	}
	r := NewRunnerWithBackend(NewClaudeCodeBackend(&ClaudeCodeConfig{Command: script}))
	_, _ = r.ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("CLI not invoked: %v", err)
	}
	for _, want := range []string{"--disallowedTools Write,Edit,MultiEdit,NotebookEdit", "--settings "} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("args %q lack %q", raw, want)
		}
	}
}

func TestChatTurn_ResultCarriesUsageAndCost(t *testing.T) {
	reported := 0.42
	b := &chatFakeBackend{output: "ok", usage: &BackendResult{
		TokensInput: 1000, TokensOutput: 500, CacheReadInputTokens: 2000, CacheCreationInputTokens: 300,
		ReportedCostUSD: &reported, Model: "claude-sonnet-4-6",
	}}
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if res.TokensInput != 1000 || res.TokensOutput != 500 || res.TokensCacheRead != 2000 || res.TokensCacheWrite != 300 {
		t.Errorf("tokens = in %d out %d cacheRead %d cacheWrite %d, want 1000/500/2000/300",
			res.TokensInput, res.TokensOutput, res.TokensCacheRead, res.TokensCacheWrite)
	}
	if res.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", res.Model)
	}
	if res.ReportedCostUSD == nil || *res.ReportedCostUSD != reported {
		t.Errorf("ReportedCostUSD = %v, want %v", res.ReportedCostUSD, reported)
	}
	want := estimateCostWithCache(1000, 500, 300, 2000, "claude-sonnet-4-6")
	if want <= 0 || res.EstimatedCostUSD != want {
		t.Errorf("EstimatedCostUSD = %v, want %v (> 0)", res.EstimatedCostUSD, want)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", res.Duration)
	}
}

func TestChatTurn_NoReportedCostStaysNil(t *testing.T) {
	b := &chatFakeBackend{output: "ok", usage: &BackendResult{TokensInput: 10, TokensOutput: 5}}
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: t.TempDir(), Text: "hi"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if res.ReportedCostUSD != nil {
		t.Errorf("ReportedCostUSD = %v, want nil", *res.ReportedCostUSD)
	}
	if res.EstimatedCostUSD <= 0 {
		t.Errorf("EstimatedCostUSD = %v, want > 0", res.EstimatedCostUSD)
	}
}

func TestClaudeCodeParseResultEvent_CarriesReportedCost(t *testing.T) {
	backend := NewClaudeCodeBackend(nil)
	ev := backend.parseStreamEvent(`{"type":"result","subtype":"success","result":"done","total_cost_usd":0.0731,"usage":{"input_tokens":10,"output_tokens":5}}`)
	if ev.Type != EventTypeResult || ev.ReportedCostUSD == nil || *ev.ReportedCostUSD != 0.0731 {
		t.Fatalf("result event = type %v cost %v, want result with 0.0731", ev.Type, ev.ReportedCostUSD)
	}
	ev = backend.parseStreamEvent(`{"type":"result","subtype":"success","result":"done"}`)
	if ev.ReportedCostUSD != nil {
		t.Fatalf("absent total_cost_usd gave %v, want nil", *ev.ReportedCostUSD)
	}
}

// agentToolUse builds an Agent/Task tool_use event carrying a tool_use id.
func agentToolUse(name, id string, input map[string]interface{}) BackendEvent {
	ev := toolUse(name, input)
	ev.ToolUseID = id
	return ev
}

func toolResultFor(id string) BackendEvent {
	return BackendEvent{Type: EventTypeToolResult, ToolUseID: id}
}

func runSubagentTurn(t *testing.T, events []BackendEvent) []SubagentEvent {
	t.Helper()
	disableChatProgressThrottle(t)
	b := &chatFakeBackend{output: "ok", events: events}
	var got []SubagentEvent
	_, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{
		ProjectPath: t.TempDir(),
		Text:        "q",
		OnSubagent:  func(e SubagentEvent) { got = append(got, e) },
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	return got
}

func TestChatTurn_SubagentRunningThenDone(t *testing.T) {
	for _, tool := range []string{"Task", "Agent"} {
		t.Run(tool, func(t *testing.T) {
			got := runSubagentTurn(t, []BackendEvent{
				agentToolUse(tool, "toolu_1", map[string]interface{}{"description": "map the executor"}),
				toolResultFor("toolu_1"),
			})
			if len(got) != 2 {
				t.Fatalf("got %d subagent events, want 2: %+v", len(got), got)
			}
			if got[0].Phase != SubagentPhaseRunning || got[1].Phase != SubagentPhaseDone {
				t.Errorf("phases = %q,%q, want running,done", got[0].Phase, got[1].Phase)
			}
			for _, e := range got {
				if e.TaskID != "toolu_1" || e.Text != "map the executor" {
					t.Errorf("event = %+v, want TaskID toolu_1 Text %q", e, "map the executor")
				}
			}
			if got[1].DurationMs < 0 {
				t.Errorf("DurationMs = %d, want >= 0", got[1].DurationMs)
			}
		})
	}
}

func TestChatTurn_SubagentTextFallback(t *testing.T) {
	long := strings.Repeat("x", 120)
	got := runSubagentTurn(t, []BackendEvent{
		agentToolUse("Agent", "toolu_p", map[string]interface{}{"prompt": long}),
		agentToolUse("Task", "toolu_n", nil),
	})
	byID := map[string]string{}
	for _, e := range got {
		if e.Phase == SubagentPhaseRunning {
			byID[e.TaskID] = e.Text
		}
	}
	if byID["toolu_p"] != strings.Repeat("x", 80) {
		t.Errorf("prompt fallback = %q, want first 80 runes", byID["toolu_p"])
	}
	if byID["toolu_n"] != "Task" {
		t.Errorf("name fallback = %q, want Task", byID["toolu_n"])
	}
}

func TestChatTurn_SubagentWithoutResultClosesAtTurnEnd(t *testing.T) {
	got := runSubagentTurn(t, []BackendEvent{
		agentToolUse("Task", "toolu_1", map[string]interface{}{"description": "never finishes"}),
	})
	if len(got) != 2 || got[0].Phase != SubagentPhaseRunning || got[1].Phase != SubagentPhaseDone {
		t.Fatalf("events = %+v, want running then one done", got)
	}
}

func TestChatTurn_SubagentOwnToolUseOpensNoRow(t *testing.T) {
	nested := agentToolUse("Task", "toolu_child", map[string]interface{}{"description": "nested"})
	nested.ParentToolUseID = "toolu_1"
	got := runSubagentTurn(t, []BackendEvent{
		agentToolUse("Task", "toolu_1", map[string]interface{}{"description": "outer"}),
		nested,
		toolResultFor("toolu_1"),
	})
	for _, e := range got {
		if e.TaskID == "toolu_child" {
			t.Errorf("nested tool_use opened a row: %+v", e)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %d events, want 2: %+v", len(got), got)
	}
}

func TestChatTurn_SubagentTimeoutClosesRows(t *testing.T) {
	disableChatProgressThrottle(t)
	var mu sync.Mutex
	var got []SubagentEvent
	waiting := &chatWaitBackend{events: []BackendEvent{
		agentToolUse("Task", "toolu_1", map[string]interface{}{"description": "slow"}),
	}}
	res, err := newChatRunner(waiting).ExecuteChatTurn(context.Background(), ChatTurn{
		ProjectPath: t.TempDir(),
		Text:        "slow",
		Timeout:     100 * time.Millisecond,
		OnSubagent: func(e SubagentEvent) {
			mu.Lock()
			got = append(got, e)
			mu.Unlock()
		},
	}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !res.Partial {
		t.Error("Partial = false, want true")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Phase != SubagentPhaseRunning || got[1].Phase != SubagentPhaseDone {
		t.Fatalf("events = %+v, want running then done", got)
	}
}

// chatWaitBackend emits events then blocks until the context ends.
type chatWaitBackend struct {
	events []BackendEvent
}

func (b *chatWaitBackend) Name() string      { return "chat-wait" }
func (b *chatWaitBackend) IsAvailable() bool { return true }
func (b *chatWaitBackend) Execute(ctx context.Context, opts ExecuteOptions) (*BackendResult, error) {
	for _, ev := range b.events {
		opts.EventHandler(ev)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestChatTurn_ReadCountsDistinctDocsAndMemories(t *testing.T) {
	disableChatProgressThrottle(t)
	read := func(p string) BackendEvent { return toolUse("Read", map[string]interface{}{"file_path": p}) }
	b := &chatFakeBackend{output: "ok", events: []BackendEvent{
		read("/repo/.agent/DEVELOPMENT-README.md"),
		read(".agent/tasks/TASK-1.md"),
		read(".agent/knowledge/memories/pitfalls/x.md"),
		read("/repo/.agent/knowledge/memories/pitfalls/x.md"),
		read(".agent/knowledge/memories/pitfalls/x.md"),
		read("src/main.go"),
		toolUse("Grep", map[string]interface{}{"pattern": "x", "path": ".agent/"}),
	}}
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{ProjectPath: "/repo", Text: "q"}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if res.DocsRead != 2 || res.MemoriesRead != 1 {
		t.Errorf("DocsRead=%d MemoriesRead=%d, want 2 and 1", res.DocsRead, res.MemoriesRead)
	}
}

func TestChatTurn_SessionResetRestartsCounts(t *testing.T) {
	disableChatProgressThrottle(t)
	read := func(p string) BackendEvent { return toolUse("Read", map[string]interface{}{"file_path": p}) }
	b := &chatFakeBackend{
		output: "ok",
		errs:   []error{&ClaudeCodeError{Type: ErrorTypeSessionNotFound, Message: "gone"}},
		callEvents: [][]BackendEvent{
			{read(".agent/a.md"), read(".agent/b.md"), agentToolUse("Task", "toolu_old", nil)},
			{read(".agent/c.md")},
		},
	}
	var got []SubagentEvent
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{
		ProjectPath: t.TempDir(), Text: "q", ResumeSessionID: "stale",
		OnSubagent: func(e SubagentEvent) { got = append(got, e) },
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if res.DocsRead != 1 || res.ToolCalls != 1 {
		t.Errorf("DocsRead=%d ToolCalls=%d, want counts of the retry only (1, 1)", res.DocsRead, res.ToolCalls)
	}
	if len(got) != 2 || got[1].Phase != SubagentPhaseDone {
		t.Errorf("failed attempt's row not closed: %+v", got)
	}
}

// TestChatTurn_StreamLinesDriveSubagentAndReads feeds real stream-json lines
// through parseStreamEvents into the chat handler (parser-to-handler wiring).
func TestChatTurn_StreamLinesDriveSubagentAndReads(t *testing.T) {
	disableChatProgressThrottle(t)
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Task","input":{"description":"survey docs"}},{"type":"tool_use","id":"toolu_2","name":"Read","input":{"file_path":".agent/DEVELOPMENT-README.md"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_2","content":"readme"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"done"}]}]}}`,
	}
	parser := NewClaudeCodeBackend(nil)
	var events []BackendEvent
	for _, l := range lines {
		events = append(events, parser.parseStreamEvents(l)...)
	}
	b := &chatFakeBackend{output: "ok", events: events}
	var got []SubagentEvent
	res, err := newChatRunner(b).ExecuteChatTurn(context.Background(), ChatTurn{
		ProjectPath: t.TempDir(), Text: "q",
		OnSubagent: func(e SubagentEvent) { got = append(got, e) },
	}, nil)
	if err != nil {
		t.Fatalf("ExecuteChatTurn: %v", err)
	}
	if len(got) != 2 || got[0].Phase != SubagentPhaseRunning || got[1].Phase != SubagentPhaseDone || got[1].TaskID != "toolu_1" {
		t.Errorf("subagent events = %+v, want running/done for toolu_1", got)
	}
	if res.DocsRead != 1 || res.ToolCalls != 2 {
		t.Errorf("DocsRead=%d ToolCalls=%d, want 1 and 2", res.DocsRead, res.ToolCalls)
	}
}
