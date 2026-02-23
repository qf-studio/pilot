package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAnthropicClient_Classify(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		statusCode int
		message    string
		wantIntent Intent
		wantErr    bool
	}{
		{
			name:       "classify task intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"task\",\"confidence\":0.95}"}]}`,
			message:    "add a logout button",
			wantIntent: IntentTask,
		},
		{
			name:       "classify greeting intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"greeting\",\"confidence\":0.99}"}]}`,
			message:    "hello",
			wantIntent: IntentGreeting,
		},
		{
			name:       "classify command intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"command\",\"confidence\":0.98}"}]}`,
			message:    "/start",
			wantIntent: IntentCommand,
		},
		{
			name:       "classify question intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"question\",\"confidence\":0.90}"}]}`,
			message:    "how does auth work?",
			wantIntent: IntentQuestion,
		},
		{
			name:       "classify chat intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"chat\",\"confidence\":0.85}"}]}`,
			message:    "what do you think about adding caching?",
			wantIntent: IntentChat,
		},
		{
			name:       "classify research intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"research\",\"confidence\":0.88}"}]}`,
			message:    "research how the auth flow works",
			wantIntent: IntentResearch,
		},
		{
			name:       "classify planning intent",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"planning\",\"confidence\":0.92}"}]}`,
			message:    "plan how to implement rate limiting",
			wantIntent: IntentPlanning,
		},
		{
			name:       "unknown intent defaults to task",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"{\"intent\":\"unknown_thing\",\"confidence\":0.50}"}]}`,
			message:    "something ambiguous",
			wantIntent: IntentTask,
		},
		{
			name:       "API returns error status",
			statusCode: http.StatusInternalServerError,
			response:   `{"error":"internal"}`,
			message:    "hello",
			wantErr:    true,
		},
		{
			name:       "empty content array",
			statusCode: http.StatusOK,
			response:   `{"content":[]}`,
			message:    "hello",
			wantErr:    true,
		},
		{
			name:       "malformed classification JSON",
			statusCode: http.StatusOK,
			response:   `{"content":[{"text":"not json at all"}]}`,
			message:    "hello",
			wantErr:    true,
		},
		{
			name:       "malformed API response",
			statusCode: http.StatusOK,
			response:   `{broken json`,
			message:    "hello",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request structure
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
				}
				if r.Header.Get("x-api-key") != "test-anthropic-key" {
					t.Errorf("expected x-api-key test-anthropic-key, got %s", r.Header.Get("x-api-key"))
				}
				if r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("expected anthropic-version 2023-06-01, got %s", r.Header.Get("anthropic-version"))
				}

				// Verify request body structure
				var reqBody map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					t.Errorf("failed to decode request body: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if reqBody["model"] == nil {
					t.Error("expected model field in request body")
				}
				if reqBody["system"] == nil {
					t.Error("expected system field in request body")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			client := &AnthropicClient{
				apiKey: "test-anthropic-key",
				httpClient: &http.Client{
					Timeout: 5 * time.Second,
				},
				model: "claude-haiku-4-5-20251001",
			}

			// Override the API URL by using a custom transport
			// Instead, we'll create a client that targets our test server
			// We need to modify the Classify method's URL target
			// Since the URL is hardcoded, we'll use a custom HTTP client with a redirect
			client.httpClient.Transport = &rewriteTransport{
				targetURL: server.URL + "/v1/messages",
			}

			history := []ConversationMessage{
				{Role: "user", Content: "previous message"},
				{Role: "assistant", Content: "previous response"},
			}

			intent, err := client.Classify(context.Background(), history, tt.message)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if intent != tt.wantIntent {
				t.Errorf("intent = %q, want %q", intent, tt.wantIntent)
			}
		})
	}
}

func TestAnthropicClient_ClassifyWithLongHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Check that messages are limited (history capped at 5 + 1 current)
		messages, ok := reqBody["messages"].([]interface{})
		if !ok {
			t.Error("expected messages array in request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// 5 history messages + 1 classify message = 6
		if len(messages) > 6 {
			t.Errorf("expected at most 6 messages (5 history + 1 current), got %d", len(messages))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"text":"{\"intent\":\"task\",\"confidence\":0.9}"}]}`))
	}))
	defer server.Close()

	client := &AnthropicClient{
		apiKey: "test-anthropic-key",
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &rewriteTransport{targetURL: server.URL + "/v1/messages"},
		},
		model: "claude-haiku-4-5-20251001",
	}

	// Create 10 messages of history
	history := make([]ConversationMessage, 10)
	for i := 0; i < 10; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		history[i] = ConversationMessage{Role: role, Content: "message"}
	}

	_, err := client.Classify(context.Background(), history, "do something")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnthropicClient_ClassifyEmptyHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		messages, _ := reqBody["messages"].([]interface{})
		// Should have just the classify message
		if len(messages) != 1 {
			t.Errorf("expected 1 message (no history), got %d", len(messages))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"text":"{\"intent\":\"greeting\",\"confidence\":0.99}"}]}`))
	}))
	defer server.Close()

	client := &AnthropicClient{
		apiKey: "test-anthropic-key",
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &rewriteTransport{targetURL: server.URL + "/v1/messages"},
		},
		model: "claude-haiku-4-5-20251001",
	}

	intent, err := client.Classify(context.Background(), nil, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if intent != IntentGreeting {
		t.Errorf("intent = %q, want %q", intent, IntentGreeting)
	}
}

func TestAnthropicClient_ClassifyCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content":[{"text":"{\"intent\":\"task\",\"confidence\":0.9}"}]}`))
	}))
	defer server.Close()

	client := &AnthropicClient{
		apiKey: "test-anthropic-key",
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &rewriteTransport{targetURL: server.URL + "/v1/messages"},
		},
		model: "claude-haiku-4-5-20251001",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Classify(ctx, nil, "hello")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestNewAnthropicClient(t *testing.T) {
	client := NewAnthropicClient("test-key")
	if client.apiKey != "test-key" {
		t.Errorf("apiKey = %q, want %q", client.apiKey, "test-key")
	}
	if client.model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want %q", client.model, "claude-haiku-4-5-20251001")
	}
	if client.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
	if client.httpClient.Timeout != 5*time.Second {
		t.Errorf("timeout = %v, want %v", client.httpClient.Timeout, 5*time.Second)
	}
}

// rewriteTransport rewrites all request URLs to a target URL for testing.
// This allows testing code that has hardcoded API URLs.
type rewriteTransport struct {
	targetURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Rewrite the URL to the test server
	newReq := req.Clone(req.Context())
	parsed, err := req.URL.Parse(t.targetURL)
	if err != nil {
		return nil, err
	}
	newReq.URL = parsed
	return http.DefaultTransport.RoundTrip(newReq)
}
