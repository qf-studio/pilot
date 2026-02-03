package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewAnthropicClient(t *testing.T) {
	client := NewAnthropicClient("test-api-key")

	if client == nil {
		t.Fatal("NewAnthropicClient returned nil")
	}
	if client.apiKey != "test-api-key" {
		t.Errorf("apiKey = %q, want %q", client.apiKey, "test-api-key")
	}
	if client.model != "claude-3-5-haiku-20241022" {
		t.Errorf("model = %q, want %q", client.model, "claude-3-5-haiku-20241022")
	}
	if client.httpClient == nil {
		t.Error("httpClient is nil")
	}
	if client.httpClient.Timeout != 5*time.Second {
		t.Errorf("httpClient.Timeout = %v, want %v", client.httpClient.Timeout, 5*time.Second)
	}
}

func TestAnthropicClientClassify(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		statusCode     int
		expectedIntent Intent
		expectError    bool
	}{
		{
			name:           "classify as chat",
			response:       `{"content": [{"text": "{\"intent\": \"chat\", \"confidence\": 0.95}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentChat,
			expectError:    false,
		},
		{
			name:           "classify as task",
			response:       `{"content": [{"text": "{\"intent\": \"task\", \"confidence\": 0.90}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentTask,
			expectError:    false,
		},
		{
			name:           "classify as question",
			response:       `{"content": [{"text": "{\"intent\": \"question\", \"confidence\": 0.85}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentQuestion,
			expectError:    false,
		},
		{
			name:           "classify as greeting",
			response:       `{"content": [{"text": "{\"intent\": \"greeting\", \"confidence\": 0.99}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentGreeting,
			expectError:    false,
		},
		{
			name:           "classify as research",
			response:       `{"content": [{"text": "{\"intent\": \"research\", \"confidence\": 0.88}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentResearch,
			expectError:    false,
		},
		{
			name:           "classify as planning",
			response:       `{"content": [{"text": "{\"intent\": \"planning\", \"confidence\": 0.87}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentPlanning,
			expectError:    false,
		},
		{
			name:           "classify as command",
			response:       `{"content": [{"text": "{\"intent\": \"command\", \"confidence\": 1.0}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentCommand,
			expectError:    false,
		},
		{
			name:           "unknown intent defaults to task",
			response:       `{"content": [{"text": "{\"intent\": \"unknown\", \"confidence\": 0.5}"}]}`,
			statusCode:     http.StatusOK,
			expectedIntent: IntentTask,
			expectError:    false,
		},
		{
			name:        "API error",
			response:    `{"error": "unauthorized"}`,
			statusCode:  http.StatusUnauthorized,
			expectError: true,
		},
		{
			name:        "empty response",
			response:    `{"content": []}`,
			statusCode:  http.StatusOK,
			expectError: true,
		},
		{
			name:        "invalid JSON in content",
			response:    `{"content": [{"text": "not json"}]}`,
			statusCode:  http.StatusOK,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				if r.Header.Get("x-api-key") != "test-api-key" {
					t.Errorf("x-api-key = %q, want %q", r.Header.Get("x-api-key"), "test-api-key")
				}
				if r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("anthropic-version = %q, want %q", r.Header.Get("anthropic-version"), "2023-06-01")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			// Create client that points to test server
			client := &AnthropicClient{
				apiKey:     "test-api-key",
				httpClient: server.Client(),
				model:      "claude-3-5-haiku-20241022",
			}

			// Override the API URL by using a custom transport
			originalURL := "https://api.anthropic.com/v1/messages"
			_ = originalURL // Suppress unused variable warning

			// For this test, we'll need to mock the HTTP client differently
			// Use the server URL directly
			client.httpClient = &http.Client{
				Timeout: 5 * time.Second,
				Transport: &mockTransport{
					serverURL:  server.URL,
					statusCode: tt.statusCode,
					response:   tt.response,
				},
			}

			ctx := context.Background()
			history := []ConversationMessage{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
			}

			intent, err := client.Classify(ctx, history, "What do you think about adding X?")

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if intent != tt.expectedIntent {
					t.Errorf("intent = %v, want %v", intent, tt.expectedIntent)
				}
			}
		})
	}
}

// mockTransport is a custom RoundTripper that redirects requests to test server
type mockTransport struct {
	serverURL  string
	statusCode int
	response   string
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Use a recorder to capture the response
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(t.statusCode)
	_, _ = recorder.Write([]byte(t.response))

	return recorder.Result(), nil
}

func TestAnthropicClientClassifyRequestBody(t *testing.T) {
	var receivedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content": [{"text": "{\"intent\": \"chat\", \"confidence\": 0.95}"}]}`))
	}))
	defer server.Close()

	client := &AnthropicClient{
		apiKey: "test-api-key",
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &redirectTransport{serverURL: server.URL},
		},
		model: "claude-3-5-haiku-20241022",
	}

	ctx := context.Background()
	history := []ConversationMessage{
		{Role: "user", Content: "Previous message"},
	}

	_, err := client.Classify(ctx, history, "Current message")
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	// Verify request body structure
	if receivedBody["model"] != "claude-3-5-haiku-20241022" {
		t.Errorf("model = %v, want %v", receivedBody["model"], "claude-3-5-haiku-20241022")
	}
	if receivedBody["max_tokens"] != float64(100) {
		t.Errorf("max_tokens = %v, want %v", receivedBody["max_tokens"], 100)
	}

	// Verify system prompt exists
	if receivedBody["system"] == nil || receivedBody["system"] == "" {
		t.Error("system prompt is missing")
	}

	// Verify messages array
	messages, ok := receivedBody["messages"].([]interface{})
	if !ok {
		t.Fatal("messages is not an array")
	}
	if len(messages) < 2 { // At least history + current message
		t.Errorf("messages length = %d, want at least 2", len(messages))
	}
}

// redirectTransport redirects all requests to a test server
type redirectTransport struct {
	serverURL string
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Create a new request to the test server
	newReq, err := http.NewRequest(req.Method, t.serverURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header

	return http.DefaultTransport.RoundTrip(newReq)
}

func TestAnthropicClientHistoryLimit(t *testing.T) {
	var receivedMessages []interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedMessages = body["messages"].([]interface{})

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"content": [{"text": "{\"intent\": \"chat\", \"confidence\": 0.95}"}]}`))
	}))
	defer server.Close()

	client := &AnthropicClient{
		apiKey: "test-api-key",
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &redirectTransport{serverURL: server.URL},
		},
		model: "claude-3-5-haiku-20241022",
	}

	// Create history with more than 5 messages
	history := []ConversationMessage{
		{Role: "user", Content: "Message 1"},
		{Role: "assistant", Content: "Response 1"},
		{Role: "user", Content: "Message 2"},
		{Role: "assistant", Content: "Response 2"},
		{Role: "user", Content: "Message 3"},
		{Role: "assistant", Content: "Response 3"},
		{Role: "user", Content: "Message 4"},
		{Role: "assistant", Content: "Response 4"},
	}

	ctx := context.Background()
	_, err := client.Classify(ctx, history, "Current message")
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	// Should have 5 history messages + 1 current = 6 total
	if len(receivedMessages) != 6 {
		t.Errorf("messages length = %d, want 6 (5 history + 1 current)", len(receivedMessages))
	}
}
