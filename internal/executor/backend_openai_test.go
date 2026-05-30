package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseChunk formats a single SSE data line.
func sseChunk(v interface{}) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

func sseDone() string { return "data: [DONE]\n\n" }

// --- Constructor Tests ---

func TestNewOpenAIBackend_Defaults(t *testing.T) {
	b := NewOpenAIBackend(nil)
	if b == nil {
		t.Fatal("NewOpenAIBackend(nil) returned nil")
	}
	if b.apiURL != openaiDefaultBaseURL+"/chat/completions" {
		t.Errorf("apiURL = %q, want default", b.apiURL)
	}
	if b.model != openaiDefaultModel {
		t.Errorf("model = %q, want %q", b.model, openaiDefaultModel)
	}
}

func TestNewOpenAIBackend_CustomConfig(t *testing.T) {
	cfg := &BackendConfig{
		OpenAI: &OpenAIConfig{
			APIKey:  "test-openai-key",
			BaseURL: "https://openrouter.ai/api/v1",
			Model:   "anthropic/claude-sonnet-4",
		},
	}
	b := NewOpenAIBackend(cfg)
	if b.apiKey != "test-openai-key" {
		t.Errorf("apiKey = %q, want test-openai-key", b.apiKey)
	}
	if b.apiURL != "https://openrouter.ai/api/v1/chat/completions" {
		t.Errorf("apiURL = %q, want openrouter endpoint", b.apiURL)
	}
	if b.model != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %q", b.model)
	}
}

func TestNewOpenAIBackend_DefaultModelFallback(t *testing.T) {
	cfg := &BackendConfig{
		DefaultModel: "gpt-4o-mini",
		OpenAI:       &OpenAIConfig{APIKey: "k"},
	}
	b := NewOpenAIBackend(cfg)
	if b.model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini from DefaultModel fallback", b.model)
	}
}

func TestOpenAIBackend_Name(t *testing.T) {
	b := NewOpenAIBackend(nil)
	if b.Name() != BackendTypeOpenAIAPI {
		t.Errorf("Name() = %q, want %q", b.Name(), BackendTypeOpenAIAPI)
	}
}

func TestOpenAIBackend_IsAvailable(t *testing.T) {
	noKey := NewOpenAIBackend(nil)
	if noKey.IsAvailable() {
		t.Error("IsAvailable() should be false without a key")
	}

	withKey := NewOpenAIBackend(&BackendConfig{OpenAI: &OpenAIConfig{APIKey: "test-key"}})
	if !withKey.IsAvailable() {
		t.Error("IsAvailable() should be true with a key set")
	}
}

// --- Helper: make a backend pointing at a test server ---

func newTestOpenAIBackend(t *testing.T, serverURL string) *OpenAIBackend {
	t.Helper()
	cfg := &BackendConfig{
		OpenAI: &OpenAIConfig{
			APIKey:  "test-api-key",
			BaseURL: serverURL,
			Model:   "test-model",
		},
	}
	b := NewOpenAIBackend(cfg)
	// Inject zero-duration backoffs so retry tests are instant.
	b.retryWaits = []time.Duration{0, 0, 0, 0, 0}
	return b
}

// --- Text-Only Streaming ---

func TestOpenAIBackend_TextOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []map[string]interface{}{
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "Hello"}, "finish_reason": nil},
			}},
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"content": " world"}, "finish_reason": nil},
			}},
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
			}, "usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}},
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, sseChunk(c))
		}
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)

	var events []BackendEvent
	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test prompt",
		ProjectPath: t.TempDir(),
		EventHandler: func(e BackendEvent) {
			events = append(events, e)
		},
	})

	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	if result.Output != "Hello world" {
		t.Errorf("Output = %q, want 'Hello world'", result.Output)
	}
	if result.TokensInput != 10 {
		t.Errorf("TokensInput = %d, want 10", result.TokensInput)
	}
	if result.TokensOutput != 5 {
		t.Errorf("TokensOutput = %d, want 5", result.TokensOutput)
	}

	// Check event sequence: init → text → result
	types := make([]BackendEventType, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	if len(types) < 3 {
		t.Fatalf("expected at least 3 events, got %d: %v", len(types), types)
	}
	if types[0] != EventTypeInit {
		t.Errorf("events[0] = %q, want init", types[0])
	}
	if types[1] != EventTypeText {
		t.Errorf("events[1] = %q, want text", types[1])
	}
	if types[len(types)-1] != EventTypeResult {
		t.Errorf("last event = %q, want result", types[len(types)-1])
	}
}

// --- Tool Call with Argument Fragmentation ---

func TestOpenAIBackend_ToolCallFragmentation(t *testing.T) {
	// Arguments for bash split across three delta chunks
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		chunks := []map[string]interface{}{
			// First chunk: tool call starts with id and name, empty arguments
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []map[string]interface{}{
						{"index": 0, "id": "call_abc", "type": "function", "function": map[string]interface{}{"name": "bash", "arguments": ""}},
					},
				}, "finish_reason": nil},
			}},
			// Second chunk: argument fragment 1
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{"index": 0, "function": map[string]interface{}{"arguments": `{"com`}},
					},
				}, "finish_reason": nil},
			}},
			// Third chunk: argument fragment 2
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{"index": 0, "function": map[string]interface{}{"arguments": `mand":"ls"}`}},
					},
				}, "finish_reason": nil},
			}},
			// Final chunk: finish_reason = tool_calls
			{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "tool_calls"},
			}, "usage": map[string]interface{}{"prompt_tokens": 20, "completion_tokens": 10}},
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, sseChunk(c))
		}
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)

	var toolUseEvents []BackendEvent
	var toolResultEvents []BackendEvent

	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test prompt",
		ProjectPath: t.TempDir(),
		EventHandler: func(e BackendEvent) {
			if e.Type == EventTypeToolUse {
				toolUseEvents = append(toolUseEvents, e)
			}
			if e.Type == EventTypeToolResult {
				toolResultEvents = append(toolResultEvents, e)
			}
		},
	})

	// The backend will execute bash "ls" — the tool runs but we just check that
	// arguments were parsed correctly from the fragments.
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if len(toolUseEvents) == 0 {
		t.Fatal("expected at least one tool_use event")
	}
	if toolUseEvents[0].ToolName != "bash" {
		t.Errorf("ToolName = %q, want bash", toolUseEvents[0].ToolName)
	}
	cmd, ok := toolUseEvents[0].ToolInput["command"].(string)
	if !ok || cmd != "ls" {
		t.Errorf("tool input command = %q, want ls", cmd)
	}
	if len(toolResultEvents) == 0 {
		t.Error("expected at least one tool_result event")
	}

	// After one tool call with no follow-up, success depends on whether second
	// call (with tool result) returns stop. result.Success may be false here
	// because the server returned no second turn. That's OK — we're testing
	// fragment accumulation, not end-to-end success.
	_ = result
}

// --- Multi-Turn: tool call → result → final text ---

func TestOpenAIBackend_MultiTurn(t *testing.T) {
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		callCount++

		if callCount == 1 {
			// First turn: tool call
			chunks := []map[string]interface{}{
				{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]interface{}{
						"role": "assistant",
						"tool_calls": []map[string]interface{}{
							{"index": 0, "id": "call_001", "type": "function",
								"function": map[string]interface{}{"name": "bash", "arguments": `{"command":"echo hello"}`}},
						},
					}, "finish_reason": nil},
				}},
				{"id": "c1", "model": "test-model", "choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "tool_calls"},
				}, "usage": map[string]interface{}{"prompt_tokens": 30, "completion_tokens": 15}},
			}
			for _, c := range chunks {
				_, _ = io.WriteString(w, sseChunk(c))
			}
		} else {
			// Second turn: final text after seeing tool result
			chunks := []map[string]interface{}{
				{"id": "c2", "model": "test-model", "choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "Done!"}, "finish_reason": nil},
				}},
				{"id": "c2", "model": "test-model", "choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
				}, "usage": map[string]interface{}{"prompt_tokens": 50, "completion_tokens": 5}},
			}
			for _, c := range chunks {
				_, _ = io.WriteString(w, sseChunk(c))
			}
		}
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)

	var eventTypes []BackendEventType
	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test prompt",
		ProjectPath: t.TempDir(),
		EventHandler: func(e BackendEvent) {
			eventTypes = append(eventTypes, e.Type)
		},
	})

	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("server called %d times, want 2", callCount)
	}
	if !result.Success {
		t.Error("expected Success=true")
	}
	if result.Output != "Done!" {
		t.Errorf("Output = %q, want 'Done!'", result.Output)
	}
	// Token sums: 30+50=80 prompt, 15+5=20 completion
	if result.TokensInput != 80 {
		t.Errorf("TokensInput = %d, want 80", result.TokensInput)
	}
	if result.TokensOutput != 20 {
		t.Errorf("TokensOutput = %d, want 20", result.TokensOutput)
	}

	// Verify event sequence contains tool_use and tool_result
	hasToolUse := false
	hasToolResult := false
	for _, et := range eventTypes {
		if et == EventTypeToolUse {
			hasToolUse = true
		}
		if et == EventTypeToolResult {
			hasToolResult = true
		}
	}
	if !hasToolUse {
		t.Error("expected tool_use event")
	}
	if !hasToolResult {
		t.Error("expected tool_result event")
	}
}

// --- Retry on 429 ---

func TestOpenAIBackend_Retry429(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Second attempt succeeds
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunk := map[string]interface{}{
			"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "ok"}, "finish_reason": nil},
			},
		}
		_, _ = io.WriteString(w, sseChunk(chunk))
		stop := map[string]interface{}{
			"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
			},
		}
		_, _ = io.WriteString(w, sseChunk(stop))
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)
	// Inject zero-duration backoffs so the retry is instant.
	b.retryWaits = []time.Duration{0, 0, 0, 0, 0}

	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test",
		ProjectPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Error("expected success after retry")
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}

// --- Context cancellation during 429 backoff ---
//
// Verifies that cancelling ctx while callAPI is waiting between retries returns
// ctx.Err() promptly — not after the full backoff duration.

func TestOpenAIBackend_CtxCancelDuring429Backoff(t *testing.T) {
	longWait := 5 * time.Second

	readyCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		select {
		case readyCh <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	b := &OpenAIBackend{
		apiKey:     "test-key",
		model:      "test-model",
		apiURL:     srv.URL + "/chat/completions",
		config:     &BackendConfig{},
		retryWaits: []time.Duration{longWait, longWait, longWait, longWait, longWait},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-readyCh
		cancel()
	}()

	start := time.Now()
	req := &openaiRequest{
		Model:    "test-model",
		Messages: []openaiMsg{{Role: "user", Content: strPtr("hi")}},
	}
	_, err := b.callAPI(ctx, req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if elapsed >= longWait {
		t.Errorf("callAPI took %v, want << %v (backoff not honouring ctx)", elapsed, longWait)
	}
}

// strPtr returns a pointer to s; used in test message construction.
func strPtr(s string) *string { return &s }

// --- Retry on 500 ---

func TestOpenAIBackend_Retry500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunk := map[string]interface{}{
			"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "recovered"}, "finish_reason": nil},
			},
		}
		_, _ = io.WriteString(w, sseChunk(chunk))
		stop := map[string]interface{}{
			"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
			},
		}
		_, _ = io.WriteString(w, sseChunk(stop))
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)
	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test",
		ProjectPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Error("expected success after 500 retry")
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}

// --- Malformed SSE handling ---

func TestOpenAIBackend_MalformedSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Mix of valid lines, garbage, and empty lines
		lines := []string{
			"data: not-valid-json\n\n",
			":comment line\n\n",
			"\n",
			sseChunk(map[string]interface{}{
				"id": "c1", "model": "m",
				"choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "valid"}, "finish_reason": nil},
				},
			}),
			"data: {broken json\n\n",
			sseChunk(map[string]interface{}{
				"id": "c1", "model": "m",
				"choices": []map[string]interface{}{
					{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
				},
			}),
			sseDone(),
		}
		for _, l := range lines {
			_, _ = io.WriteString(w, l)
		}
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)
	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test",
		ProjectPath: t.TempDir(),
	})

	// Malformed lines should be skipped; valid content should accumulate
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.Output != "valid" {
		t.Errorf("Output = %q, want 'valid'", result.Output)
	}
}

// --- No API key returns error ---

func TestOpenAIBackend_NoAPIKey(t *testing.T) {
	b := &OpenAIBackend{
		apiKey: "",
		model:  openaiDefaultModel,
		apiURL: "https://api.openai.com/v1/chat/completions",
		config: &BackendConfig{},
	}
	_, err := b.Execute(context.Background(), ExecuteOptions{Prompt: "test"})
	if err == nil {
		t.Error("expected error when no API key configured")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Errorf("error = %q, want API key mention", err.Error())
	}
}

// --- Token accounting ---

func TestOpenAIBackend_TokenAccounting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunks := []map[string]interface{}{
			{"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "answer"}, "finish_reason": nil},
			}},
			{"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
			}, "usage": map[string]interface{}{"prompt_tokens": 42, "completion_tokens": 17, "total_tokens": 59}},
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, sseChunk(c))
		}
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)
	result, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test",
		ProjectPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.TokensInput != 42 {
		t.Errorf("TokensInput = %d, want 42", result.TokensInput)
	}
	if result.TokensOutput != 17 {
		t.Errorf("TokensOutput = %d, want 17", result.TokensOutput)
	}
	// Cache tokens are always 0 for OpenAI — verify result fields exist and are 0
	if result.CacheCreationInputTokens != 0 {
		t.Errorf("CacheCreationInputTokens = %d, want 0", result.CacheCreationInputTokens)
	}
	if result.CacheReadInputTokens != 0 {
		t.Errorf("CacheReadInputTokens = %d, want 0", result.CacheReadInputTokens)
	}
}

// --- Request format validation ---

func TestOpenAIBackend_RequestFormat(t *testing.T) {
	var captured []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = body

		// Verify no Anthropic headers
		if r.Header.Get("anthropic-version") != "" {
			t.Error("anthropic-version header should not be set for OpenAI backend")
		}
		if r.Header.Get("x-api-key") != "" {
			t.Error("x-api-key header should not be set for OpenAI backend")
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Authorization = %q, want 'Bearer ...'", auth)
		}

		// Return minimal stop response
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		chunk := map[string]interface{}{
			"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"role": "assistant", "content": "hi"}, "finish_reason": nil},
			},
		}
		stop := map[string]interface{}{
			"id": "c1", "model": "m", "choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
			},
		}
		_, _ = io.WriteString(w, sseChunk(chunk))
		_, _ = io.WriteString(w, sseChunk(stop))
		_, _ = io.WriteString(w, sseDone())
	}))
	defer srv.Close()

	b := newTestOpenAIBackend(t, srv.URL)
	_, err := b.Execute(context.Background(), ExecuteOptions{
		Prompt:      "test",
		ProjectPath: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(captured, &req); err != nil {
		t.Fatalf("captured body is not valid JSON: %v", err)
	}

	// Should have "messages" and "tools" and "stream"
	if _, ok := req["messages"]; !ok {
		t.Error("request missing 'messages' field")
	}
	if _, ok := req["tools"]; !ok {
		t.Error("request missing 'tools' field")
	}
	streamVal, _ := req["stream"].(bool)
	if !streamVal {
		t.Error("request 'stream' should be true")
	}

	// Should NOT have Anthropic-specific fields
	if _, ok := req["thinking"]; ok {
		t.Error("request should not have 'thinking' field")
	}
	if _, ok := req["system"]; ok {
		t.Error("request should not have 'system' field (use system role message instead)")
	}

	// Verify tools use OpenAI function-call format
	tools, _ := req["tools"].([]interface{})
	if len(tools) == 0 {
		t.Fatal("tools array is empty")
	}
	firstTool, _ := tools[0].(map[string]interface{})
	if firstTool["type"] != "function" {
		t.Errorf("tool type = %q, want 'function'", firstTool["type"])
	}
	funcDef, _ := firstTool["function"].(map[string]interface{})
	if _, ok := funcDef["parameters"]; !ok {
		t.Error("tool function missing 'parameters' (not 'input_schema')")
	}
	if _, ok := funcDef["input_schema"]; ok {
		t.Error("tool function should use 'parameters', not 'input_schema'")
	}

	// System prompt must be a system-role message, not a top-level "system" field
	messages, _ := req["messages"].([]interface{})
	if len(messages) == 0 {
		t.Fatal("messages array is empty")
	}
	firstMsg, _ := messages[0].(map[string]interface{})
	if firstMsg["role"] != "system" {
		t.Errorf("first message role = %q, want 'system'", firstMsg["role"])
	}
}

// --- Factory registration ---

func TestNewBackend_OpenAIAPI(t *testing.T) {
	cfg := &BackendConfig{
		Type:   BackendTypeOpenAIAPI,
		OpenAI: &OpenAIConfig{APIKey: "test-key"},
	}
	b, err := NewBackend(cfg)
	if err != nil {
		t.Fatalf("NewBackend error: %v", err)
	}
	if b.Name() != BackendTypeOpenAIAPI {
		t.Errorf("Name() = %q, want %q", b.Name(), BackendTypeOpenAIAPI)
	}
	_, ok := b.(*OpenAIBackend)
	if !ok {
		t.Error("backend should be *OpenAIBackend")
	}
}

// --- SSE parser unit tests ---

func TestParseOpenAISSEStream_TextOnly(t *testing.T) {
	b := &OpenAIBackend{}
	sse := fmt.Sprintf(
		"data: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
		mustJSON(map[string]interface{}{
			"id": "c1", "model": "gpt-4o",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"content": "Hello"}, "finish_reason": nil},
			},
		}),
		mustJSON(map[string]interface{}{
			"id": "c1", "model": "gpt-4o",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
			},
			"usage": map[string]interface{}{"prompt_tokens": 5, "completion_tokens": 3},
		}),
	)

	result, err := b.parseSSEStream(strings.NewReader(sse))
	if err != nil {
		t.Fatalf("parseSSEStream error: %v", err)
	}
	if result.Content != "Hello" {
		t.Errorf("Content = %q, want 'Hello'", result.Content)
	}
	if result.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want 'stop'", result.FinishReason)
	}
	if result.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d, want 5", result.PromptTokens)
	}
}

func TestParseOpenAISSEStream_ToolCallFragments(t *testing.T) {
	b := &OpenAIBackend{}

	chunks := []interface{}{
		map[string]interface{}{
			"id": "c1", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{"index": 0, "id": "call_xyz", "type": "function",
							"function": map[string]interface{}{"name": "read_file", "arguments": ""}},
					},
				}, "finish_reason": nil},
			},
		},
		map[string]interface{}{
			"id": "c1", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{"index": 0, "function": map[string]interface{}{"arguments": `{"pa`}},
					},
				}, "finish_reason": nil},
			},
		},
		map[string]interface{}{
			"id": "c1", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{
						{"index": 0, "function": map[string]interface{}{"arguments": `th":"/tmp/x"}`}},
					},
				}, "finish_reason": nil},
			},
		},
		map[string]interface{}{
			"id": "c1", "model": "m",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "tool_calls"},
			},
			"usage": map[string]interface{}{"prompt_tokens": 8, "completion_tokens": 4},
		},
	}

	var sb strings.Builder
	for _, c := range chunks {
		sb.WriteString(sseChunk(c))
	}
	sb.WriteString(sseDone())

	result, err := b.parseSSEStream(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatalf("parseSSEStream error: %v", err)
	}
	if result.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want 'tool_calls'", result.FinishReason)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}
	tc := result.ToolCalls[0]
	if tc.ID != "call_xyz" {
		t.Errorf("ToolCall.ID = %q, want call_xyz", tc.ID)
	}
	if tc.Function.Name != "read_file" {
		t.Errorf("ToolCall.Name = %q, want read_file", tc.Function.Name)
	}
	// Arguments should be the concatenated fragments
	want := `{"path":"/tmp/x"}`
	if tc.Function.Arguments != want {
		t.Errorf("Arguments = %q, want %q", tc.Function.Arguments, want)
	}
}

// mustJSON marshals v to JSON or panics.
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
