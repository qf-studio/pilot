package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/intent"
)

func makeServer(t *testing.T, statusCode int, body interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify required headers are present.
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, "missing x-api-key", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") == "" {
			http.Error(w, "missing anthropic-version", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write(mustJSON(t, body))
	}))
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("mustJSON: %v", err)
	}
	return b
}

func newTestClient(url string) *Client {
	c := NewClient("test-api-key")
	c.apiURL = url
	return c
}

func TestAnswer_SingleBlock(t *testing.T) {
	srv := makeServer(t, http.StatusOK, map[string]interface{}{
		"content": []map[string]string{{"text": "hello world"}},
	})
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.Answer(context.Background(), "claude-haiku-4-5-20251001", "sys", nil, "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestAnswer_MultipleBlocks(t *testing.T) {
	srv := makeServer(t, http.StatusOK, map[string]interface{}{
		"content": []map[string]string{
			{"text": "foo"},
			{"text": "bar"},
		},
	})
	defer srv.Close()

	c := newTestClient(srv.URL)
	got, err := c.Answer(context.Background(), "claude-haiku-4-5-20251001", "sys", nil, "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "foobar" {
		t.Errorf("got %q, want %q", got, "foobar")
	}
}

func TestAnswer_WithHistory(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		resp, _ := json.Marshal(map[string]interface{}{
			"content": []map[string]string{{"text": "answer"}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	history := []intent.ConversationMessage{
		{Role: "user", Content: "previous question"},
		{Role: "assistant", Content: "previous answer"},
	}

	c := newTestClient(srv.URL)
	_, err := c.Answer(context.Background(), "claude-haiku-4-5-20251001", "system prompt", history, "new question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs, ok := capturedBody["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages not an array")
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages (2 history + 1 user), got %d", len(msgs))
	}

	// max_tokens is REQUIRED by the Anthropic Messages API — omitting it 400s.
	mt, ok := capturedBody["max_tokens"].(float64)
	if !ok || mt <= 0 {
		t.Errorf("expected positive max_tokens in request body, got %v", capturedBody["max_tokens"])
	}
	// output_config is non-standard and must not be sent.
	if _, present := capturedBody["output_config"]; present {
		t.Errorf("output_config must not be in request body")
	}
}

func TestAnswer_Non200(t *testing.T) {
	srv := makeServer(t, http.StatusInternalServerError, map[string]interface{}{
		"error": "server error",
	})
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.Answer(context.Background(), "claude-haiku-4-5-20251001", "sys", nil, "hi")
	if err == nil {
		t.Fatal("expected error on non-200 response")
	}
}

func TestAnswer_EmptyContent(t *testing.T) {
	srv := makeServer(t, http.StatusOK, map[string]interface{}{
		"content": []map[string]string{},
	})
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.Answer(context.Background(), "claude-haiku-4-5-20251001", "sys", nil, "hi")
	if err == nil {
		t.Fatal("expected error on empty content")
	}
}

func TestAnswer_RequestShape(t *testing.T) {
	var capturedBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		resp, _ := json.Marshal(map[string]interface{}{
			"content": []map[string]string{{"text": "ok"}},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	_, err := c.Answer(context.Background(), "my-model", "my-system", nil, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedBody["model"] != "my-model" {
		t.Errorf("model: got %v, want my-model", capturedBody["model"])
	}
	if capturedBody["system"] != "my-system" {
		t.Errorf("system: got %v, want my-system", capturedBody["system"])
	}
	// max_tokens is REQUIRED by the Anthropic Messages API; omitting it 400s.
	mt, ok := capturedBody["max_tokens"].(float64)
	if !ok || mt <= 0 {
		t.Errorf("max_tokens: got %v, want positive int", capturedBody["max_tokens"])
	}
	// output_config is non-standard and must not be sent (it was the cause of the
	// 400 "max_tokens: Field required" regression — see fix/llm-max-tokens).
	if _, present := capturedBody["output_config"]; present {
		t.Error("output_config must not be in request body")
	}
}
