package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// --- Construction / configuration ---

func TestNewEngine_PicksUpEnvKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-test")
	e := NewEngine(&BackendConfig{})
	if !e.IsAvailable() {
		t.Fatal("engine should be available with env key")
	}
	if e.Name() != BackendTypeOpenRouter {
		t.Fatalf("Name() = %q, want %q", e.Name(), BackendTypeOpenRouter)
	}
}

func TestNewEngine_ConfigTokenOverridesEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-env")
	e := NewEngine(&BackendConfig{APIAuthToken: "sk-or-v1-config"})
	if e.apiKey != "sk-or-v1-config" {
		t.Fatalf("apiKey = %q, want config-supplied value", e.apiKey)
	}
}

func TestNewEngine_MissingKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	e := NewEngine(&BackendConfig{})
	if e.IsAvailable() {
		t.Fatal("engine should not be available without any key")
	}
}

func TestNewEngine_CustomBaseURL(t *testing.T) {
	e := NewEngine(&BackendConfig{APIBaseURL: "http://localhost:9999/api/v1/"})
	if e.baseURL != "http://localhost:9999/api/v1" {
		t.Fatalf("baseURL = %q, want trailing slash trimmed", e.baseURL)
	}
}

// --- Factory wiring ---

func TestBackendFactory_RegistersOpenRouter(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-test")
	backend, err := NewBackend(&BackendConfig{Type: BackendTypeOpenRouter})
	if err != nil {
		t.Fatalf("NewBackend(openrouter) failed: %v", err)
	}
	if _, ok := backend.(*Engine); !ok {
		t.Fatalf("expected *Engine, got %T", backend)
	}
	if backend.Name() != BackendTypeOpenRouter {
		t.Fatalf("Name() = %q, want %q", backend.Name(), BackendTypeOpenRouter)
	}
}

// --- Retry-after extraction ---

func TestExtractRetryAfter(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 429,
			"message": "rate limited",
			"metadata": {"retry_after_seconds": 12, "provider_name": "Anthropic"}
		}
	}`)
	if got := extractRetryAfter(body); got != 12 {
		t.Fatalf("extractRetryAfter = %d, want 12", got)
	}

	// Malformed → 0
	if got := extractRetryAfter([]byte(`not json`)); got != 0 {
		t.Fatalf("extractRetryAfter(garbage) = %d, want 0", got)
	}

	// Missing field → 0
	if got := extractRetryAfter([]byte(`{"error":{"code":429}}`)); got != 0 {
		t.Fatalf("extractRetryAfter(no metadata) = %d, want 0", got)
	}
}

func TestEngineTruncate(t *testing.T) {
	if engineTruncate("short", 100) != "short" {
		t.Fatal("short string should not be truncated")
	}
	if got := engineTruncate("hello world", 5); got != "hello..." {
		t.Fatalf("engineTruncate = %q, want %q", got, "hello...")
	}
}

// --- Tool execution ---

func TestEngineExecBash_Simple(t *testing.T) {
	got := engineExecBash("echo hello", 5, t.TempDir())
	if !strings.Contains(got, "hello") {
		t.Fatalf("expected 'hello' in output, got: %q", got)
	}
}

func TestEngineExecBash_Timeout(t *testing.T) {
	got := engineExecBash("sleep 2", 1, t.TempDir())
	if !strings.Contains(got, "TIMEOUT") {
		t.Fatalf("expected TIMEOUT marker, got: %q", got)
	}
}

func TestEngineExecBash_OutputCap(t *testing.T) {
	// Generate >engineOutputCap bytes via python (portable, deterministic).
	big := engineExecBash(`python3 -c "import sys; sys.stdout.write('x'*60000)"`, 10, t.TempDir())
	if len(big) > engineOutputCap+500 { // small slack for the truncation marker
		t.Fatalf("output not capped: %d bytes", len(big))
	}
	if !strings.Contains(big, "truncated") {
		t.Fatalf("expected 'truncated' marker (output len=%d)", len(big))
	}
}

func TestEngineExecWriteReadFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.txt"
	wr := engineExecWriteFile(path, "hello\nworld\n")
	if !strings.Contains(wr, "Wrote") {
		t.Fatalf("write failed: %q", wr)
	}
	rd := engineExecReadFile(path, 0, 0)
	if !strings.Contains(rd, "hello") || !strings.Contains(rd, "world") {
		t.Fatalf("read content unexpected: %q", rd)
	}
	if !strings.Contains(rd, "   1 |") {
		t.Fatalf("expected line numbering, got: %q", rd)
	}
}

func TestEngineExecEditFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.txt"
	_ = engineExecWriteFile(path, "alpha\nbeta\ngamma\n")

	ok := engineExecEditFile(path, "beta", "BETA")
	if !strings.Contains(ok, "Edited") {
		t.Fatalf("edit failed: %q", ok)
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "BETA") {
		t.Fatalf("edit did not apply: %q", content)
	}

	notFound := engineExecEditFile(path, "zzznotpresent", "x")
	if !strings.Contains(notFound, "not found") {
		t.Fatalf("missing-string case: %q", notFound)
	}

	ambiguous := engineExecEditFile(path, "a", "X")
	if !strings.Contains(ambiguous, "times") {
		t.Fatalf("ambiguous-match case: %q", ambiguous)
	}
}

func TestEngineDispatchTool_UnknownTool(t *testing.T) {
	got := engineDispatchTool("nonexistent", map[string]interface{}{}, t.TempDir())
	if !strings.Contains(got, "Unknown tool") {
		t.Fatalf("unknown tool: %q", got)
	}
}

// --- SSE parsing ---

func TestParseSSEStream_SimpleCompletion(t *testing.T) {
	stream := strings.Join([]string{
		`: OPENROUTER PROCESSING`, // heartbeat — must be skipped
		``,
		`data: {"id":"gen-1","model":"anthropic/claude-opus-4.7","provider":"Anthropic","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello "}}]}`,
		``,
		`data: {"id":"gen-1","model":"anthropic/claude-opus-4.7","provider":"Anthropic","choices":[{"index":0,"delta":{"content":"world"}}]}`,
		``,
		`data: {"id":"gen-1","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop","native_finish_reason":"end_turn"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":0.0001}}`,
		``,
		`data: [DONE]`,
	}, "\n") + "\n"

	e := &Engine{}
	resp, err := e.parseSSEStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if resp.ID != "gen-1" {
		t.Fatalf("ID = %q, want gen-1", resp.ID)
	}
	if resp.Model != "anthropic/claude-opus-4.7" {
		t.Fatalf("Model = %q", resp.Model)
	}
	if resp.Provider != "Anthropic" {
		t.Fatalf("Provider = %q", resp.Provider)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("got %d choices", len(resp.Choices))
	}
	c := resp.Choices[0]
	if c.Message == nil || c.Message.Content != "Hello world" {
		t.Fatalf("Message.Content = %q", c.Message.Content)
	}
	if c.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q", c.FinishReason)
	}
	if c.NativeFinishReason != "end_turn" {
		t.Fatalf("NativeFinishReason = %q", c.NativeFinishReason)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 || resp.Usage.Cost != 0.0001 {
		t.Fatalf("usage mismatch: %+v", resp.Usage)
	}
}

func TestParseSSEStream_ToolCall(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"gen-2","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"toolu_01","type":"function","function":{"name":"bash"}}]}}]}`,
		``,
		`data: {"id":"gen-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\""}}]}}]}`,
		``,
		`data: {"id":"gen-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ls /tmp\"}"}}]}}]}`,
		``,
		`data: {"id":"gen-2","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls","native_finish_reason":"tool_use"}]}`,
		``,
		`data: [DONE]`,
	}, "\n") + "\n"

	e := &Engine{}
	resp, err := e.parseSSEStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message == nil {
		t.Fatalf("no message")
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool_calls", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "toolu_01" {
		t.Fatalf("ToolCall.ID = %q", tc.ID)
	}
	if tc.Function.Name != "bash" {
		t.Fatalf("Function.Name = %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"command":"ls /tmp"}` {
		t.Fatalf("Arguments = %q", tc.Function.Arguments)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q", resp.Choices[0].FinishReason)
	}
}

func TestParseSSEStream_ParallelToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"gen-3","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[` +
			`{"index":0,"id":"toolu_a","type":"function","function":{"name":"bash","arguments":"{\"command\":\"a\"}"}},` +
			`{"index":1,"id":"toolu_b","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"/b\"}"}}` +
			`]},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
	}, "\n") + "\n"

	e := &Engine{}
	resp, err := e.parseSSEStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parseSSEStream: %v", err)
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("got %d tool_calls, want 2", len(msg.ToolCalls))
	}
	names := []string{msg.ToolCalls[0].Function.Name, msg.ToolCalls[1].Function.Name}
	bothPresent := (names[0] == "bash" || names[0] == "read_file") &&
		(names[1] == "bash" || names[1] == "read_file") &&
		names[0] != names[1]
	if !bothPresent {
		t.Fatalf("unexpected tool names: %v", names)
	}
}

func TestParseSSEStream_ErrorInStream(t *testing.T) {
	stream := `data: {"id":"gen-x","error":{"code":529,"message":"upstream overloaded"}}` + "\n"
	e := &Engine{}
	_, err := e.parseSSEStream(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("expected overloaded error, got: %v", err)
	}
}

// --- End-to-end against a mock OR server ---

func TestExecute_HappyPath_NoTools(t *testing.T) {
	var seenAuth string
	var seenReferer string
	var seenTitle string
	var seenBody orRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != openRouterChatPath {
			http.NotFound(w, r)
			return
		}
		seenAuth = r.Header.Get("Authorization")
		seenReferer = r.Header.Get("HTTP-Referer")
		seenTitle = r.Header.Get("X-OpenRouter-Title")

		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seenBody)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		// Single chunk: assistant content + finish_reason: stop + usage.
		_, _ = fmt.Fprintf(w, "data: %s\n\n",
			`{"id":"gen-mock","model":"anthropic/claude-opus-4.7","provider":"Anthropic","choices":[{"index":0,"delta":{"role":"assistant","content":"PONG"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4,"cost":0.00005,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`)
		_, _ = fmt.Fprintf(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	e := NewEngine(&BackendConfig{
		APIAuthToken: "sk-or-v1-mock",
		APIBaseURL:   srv.URL,
	})

	var events []BackendEvent
	res, err := e.Execute(context.Background(), ExecuteOptions{
		Prompt:       "Reply PONG",
		ProjectPath:  t.TempDir(),
		EventHandler: func(ev BackendEvent) { events = append(events, ev) },
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !res.Success {
		t.Fatal("expected Success=true")
	}
	if res.Output != "PONG" {
		t.Fatalf("Output = %q, want PONG", res.Output)
	}
	if res.TokensInput != 3 || res.TokensOutput != 1 {
		t.Fatalf("tokens = (%d,%d), want (3,1)", res.TokensInput, res.TokensOutput)
	}

	// Headers
	if seenAuth != "Bearer sk-or-v1-mock" {
		t.Fatalf("Authorization = %q", seenAuth)
	}
	if seenReferer != openRouterDefaultReferer {
		t.Fatalf("Referer = %q", seenReferer)
	}
	if seenTitle != openRouterDefaultTitle {
		t.Fatalf("Title = %q", seenTitle)
	}

	// Request body sanity
	if !seenBody.Stream {
		t.Fatal("expected stream: true")
	}
	if seenBody.Usage == nil || !seenBody.Usage.Include {
		t.Fatal("expected usage.include: true")
	}
	if seenBody.Reasoning == nil || seenBody.Reasoning.Effort != engineEffortHigh {
		t.Fatalf("Reasoning = %+v", seenBody.Reasoning)
	}
	if seenBody.Provider != nil {
		t.Fatalf("Provider should be nil by default (preserves sticky routing), got: %+v", seenBody.Provider)
	}
	if len(seenBody.Tools) != 4 {
		t.Fatalf("got %d tools, want 4 (bash,read,write,edit)", len(seenBody.Tools))
	}
	if len(seenBody.Messages) < 2 || seenBody.Messages[0].Role != "system" {
		t.Fatal("expected system message first")
	}
	// system content should contain cache_control with ttl:1h
	if !strings.Contains(string(seenBody.Messages[0].Content), `"cache_control"`) {
		t.Fatalf("system message missing cache_control: %s", seenBody.Messages[0].Content)
	}
	if !strings.Contains(string(seenBody.Messages[0].Content), `"ttl":"1h"`) {
		t.Fatalf("system message missing ttl:1h: %s", seenBody.Messages[0].Content)
	}

	// Events
	var sawInit, sawText, sawResult bool
	for _, ev := range events {
		switch ev.Type {
		case EventTypeInit:
			sawInit = true
		case EventTypeText:
			sawText = true
		case EventTypeResult:
			sawResult = true
		}
	}
	if !sawInit || !sawText || !sawResult {
		t.Fatalf("missing events: init=%v text=%v result=%v", sawInit, sawText, sawResult)
	}
}

func TestExecute_ToolLoop(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if calls == 1 {
			// Turn 1: model wants to bash 'echo HI'
			_, _ = fmt.Fprintf(w, "data: %s\n\n",
				`{"id":"g1","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"toolu_x","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo HI\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":50,"completion_tokens":20,"cost":0.001}}`)
		} else {
			// Turn 2: model says done
			_, _ = fmt.Fprintf(w, "data: %s\n\n",
				`{"id":"g2","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":60,"completion_tokens":2,"cost":0.0005}}`)
		}
		_, _ = fmt.Fprintf(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	e := NewEngine(&BackendConfig{
		APIAuthToken: "sk-or-v1-mock",
		APIBaseURL:   srv.URL,
	})

	var toolEvents []BackendEvent
	res, err := e.Execute(context.Background(), ExecuteOptions{
		Prompt:      "do a thing",
		ProjectPath: t.TempDir(),
		EventHandler: func(ev BackendEvent) {
			if ev.Type == EventTypeToolUse || ev.Type == EventTypeToolResult {
				toolEvents = append(toolEvents, ev)
			}
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 API calls, got %d", calls)
	}
	if !res.Success {
		t.Fatal("expected Success=true")
	}
	if res.Output != "OK" {
		t.Fatalf("Output = %q, want OK", res.Output)
	}
	if res.TokensInput != 110 || res.TokensOutput != 22 {
		t.Fatalf("aggregated tokens wrong: in=%d out=%d", res.TokensInput, res.TokensOutput)
	}
	if len(toolEvents) != 2 { // one ToolUse, one ToolResult
		t.Fatalf("got %d tool events, want 2", len(toolEvents))
	}
	if toolEvents[0].ToolName != "bash" {
		t.Fatalf("ToolName = %q", toolEvents[0].ToolName)
	}
	if !strings.Contains(toolEvents[1].ToolResult, "HI") {
		t.Fatalf("tool result missing 'HI': %q", toolEvents[1].ToolResult)
	}
}

func TestExecute_RetryOn429(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			// Embed retry_after_seconds:0 so the test doesn't actually wait.
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"rate limited","metadata":{"retry_after_seconds":0}}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprintf(w, "data: %s\n\n",
			`{"id":"g","choices":[{"index":0,"delta":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		_, _ = fmt.Fprintf(w, "data: [DONE]\n")
	}))
	defer srv.Close()

	// Override backoffs to zero for the test (avoid 30s sleep).
	originalBackoffs := engineRetryBackoffs
	engineRetryBackoffs = []time.Duration{0}
	defer func() { engineRetryBackoffs = originalBackoffs }()

	e := NewEngine(&BackendConfig{
		APIAuthToken: "sk-or-v1-mock",
		APIBaseURL:   srv.URL,
	})

	res, err := e.Execute(context.Background(), ExecuteOptions{
		Prompt:      "p",
		ProjectPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls (429 + recovery), got %d", calls)
	}
	if !res.Success || res.Output != "recovered" {
		t.Fatalf("res = %+v", res)
	}
}

func TestExecute_NoAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	e := NewEngine(&BackendConfig{})
	_, err := e.Execute(context.Background(), ExecuteOptions{Prompt: "x", ProjectPath: t.TempDir()})
	if err == nil {
		t.Fatal("expected error when no key")
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error should mention OPENROUTER_API_KEY, got: %v", err)
	}
}

// --- Live smoke test, gated by env var ---
// Run with: OPENROUTER_API_KEY=... go test -run TestEngineSmoke ./internal/executor/ -v

func TestEngineSmoke(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("smoke test requires OPENROUTER_API_KEY")
	}

	e := NewEngine(&BackendConfig{})
	// Cheapest possible call: Haiku + tight max_tokens. Lets a credit-limited
	// key (a few cents) still validate the full request/response loop.
	e.maxOutputToks = 200

	res, err := e.Execute(context.Background(), ExecuteOptions{
		Prompt:      "You are a test assistant. Reply with exactly: SMOKE OK. Do not use any tools.",
		ProjectPath: t.TempDir(),
		Model:       "anthropic/claude-haiku-4.5",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		// 402 (credit cap) is a real-world result, not a code bug. The fact that
		// we got a structured error back means auth/headers/parsing all worked.
		if strings.Contains(res.Error, "402") || strings.Contains(res.Error, "credits") {
			t.Skipf("OR key out of credit (engine round-trip otherwise OK): %s", res.Error)
		}
		t.Fatalf("smoke failed: %+v", res)
	}
	t.Logf("smoke ok: output=%q tokens=(%d,%d)", res.Output, res.TokensInput, res.TokensOutput)
}
