package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Constructor Tests ---

func TestNewAnthropicBackend_Defaults(t *testing.T) {
	b := NewAnthropicBackend(nil)
	if b == nil {
		t.Fatal("NewAnthropicBackend(nil) returned nil")
	}
	if b.apiURL != anthropicAPIURL {
		t.Errorf("apiURL = %q, want default %q", b.apiURL, anthropicAPIURL)
	}
}

func TestNewAnthropicBackend_CustomBaseURL(t *testing.T) {
	cfg := &BackendConfig{
		APIBaseURL: "https://proxy.example.com",
	}
	b := NewAnthropicBackend(cfg)
	if b.apiURL != "https://proxy.example.com/v1/messages" {
		t.Errorf("apiURL = %q, want proxy endpoint", b.apiURL)
	}
}

func TestAnthropicBackend_Name(t *testing.T) {
	b := NewAnthropicBackend(nil)
	if b.Name() != BackendTypeAnthropicAPI {
		t.Errorf("Name() = %q, want %q", b.Name(), BackendTypeAnthropicAPI)
	}
}

func TestAnthropicBackend_IsAvailable(t *testing.T) {
	noKey := NewAnthropicBackend(nil)
	if noKey.IsAvailable() {
		t.Error("IsAvailable() should be false without a key")
	}
}

// --- Helper: build a test backend pointing at a local server ---

func newTestAnthropicBackend(t *testing.T, serverURL string) *AnthropicBackend {
	t.Helper()
	b := &AnthropicBackend{
		apiKey: "test-anthropic-key",
		apiURL: serverURL + "/v1/messages",
		config: &BackendConfig{},
		// Inject zero-duration backoffs so retry tests are instant.
		retryWaits: []time.Duration{0, 0, 0, 0, 0},
	}
	return b
}

// anthropicSSEStop returns a minimal SSE stream that ends cleanly.
func anthropicSSEStop() string {
	return strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg1","type":"message","role":"assistant","model":"claude-opus-4-6","usage":{"input_tokens":10,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n\n")
}

// --- Retry on 429 ---

func TestAnthropicBackend_Retry429(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicSSEStop())
	}))
	defer srv.Close()

	b := newTestAnthropicBackend(t, srv.URL)

	req := &apiRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 100,
		Messages:  []apiMessage{{Role: "user", Content: "hi"}},
	}
	result, err := b.callAPI(context.Background(), req)
	if err != nil {
		t.Fatalf("callAPI error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}

// --- Retry on 500 ---

func TestAnthropicBackend_Retry500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, anthropicSSEStop())
	}))
	defer srv.Close()

	b := newTestAnthropicBackend(t, srv.URL)

	req := &apiRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 100,
		Messages:  []apiMessage{{Role: "user", Content: "hi"}},
	}
	result, err := b.callAPI(context.Background(), req)
	if err != nil {
		t.Fatalf("callAPI error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}

// --- Context cancellation during 429 backoff ---
//
// Verifies that when ctx is cancelled while callAPI is waiting between retries,
// the function returns ctx.Err() promptly instead of sleeping the full backoff.

func TestAnthropicBackend_CtxCancelDuring429Backoff(t *testing.T) {
	// Use a real backoff long enough that if the select falls through to
	// time.After, the test would time out (5s >> test budget).
	longWait := 5 * time.Second

	readyCh := make(chan struct{}) // server signals it sent 429
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		// Signal that the 429 has been written so the test can cancel ctx.
		select {
		case readyCh <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	b := &AnthropicBackend{
		apiKey:     "test-key",
		apiURL:     srv.URL + "/v1/messages",
		config:     &BackendConfig{},
		retryWaits: []time.Duration{longWait, longWait, longWait, longWait, longWait},
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel the context once the server has responded with 429.
	go func() {
		<-readyCh
		cancel()
	}()

	start := time.Now()
	req := &apiRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 100,
		Messages:  []apiMessage{{Role: "user", Content: "hi"}},
	}
	_, err := b.callAPI(ctx, req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	// Must return well before the 5s backoff.
	if elapsed >= longWait {
		t.Errorf("callAPI took %v, want << %v (backoff not honouring ctx)", elapsed, longWait)
	}
}

// --- Context cancellation during overloaded-error backoff ---

func TestAnthropicBackend_CtxCancelDuringOverloadedBackoff(t *testing.T) {
	longWait := 5 * time.Second

	readyCh := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Return an overloaded error event in the stream body.
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n")
		select {
		case readyCh <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	b := &AnthropicBackend{
		apiKey:     "test-key",
		apiURL:     srv.URL + "/v1/messages",
		config:     &BackendConfig{},
		retryWaits: []time.Duration{longWait, longWait, longWait, longWait, longWait},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-readyCh
		cancel()
	}()

	start := time.Now()
	req := &apiRequest{
		Model:     "claude-opus-4-6",
		MaxTokens: 100,
		Messages:  []apiMessage{{Role: "user", Content: "hi"}},
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

// --- No API key returns error ---

func TestAnthropicBackend_NoAPIKey(t *testing.T) {
	b := &AnthropicBackend{
		apiKey: "",
		apiURL: anthropicAPIURL,
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
