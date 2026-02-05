package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestHaikuParser creates a HaikuParser pointed at a test server.
func newTestHaikuParser(serverURL string) *HaikuParser {
	return &HaikuParser{
		apiKey: "test-api-key",
		apiURL: serverURL,
		model:  "claude-haiku-4-5-20251001",
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// makeHaikuResponse builds an Anthropic Messages API response JSON string.
func makeHaikuResponse(text string) string {
	resp := haikuResponse{
		Content: []struct {
			Text string `json:"text"`
		}{
			{Text: text},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

func TestHaikuParser_ParsePlanning_Success(t *testing.T) {
	tests := []struct {
		name     string
		response string // JSON array text from model
		expected []PlannedSubtask
	}{
		{
			name:     "plain JSON subtasks",
			response: `[{"order":1,"title":"Create schema","description":"Add migration for users table"},{"order":2,"title":"Add endpoints","description":"REST API for CRUD operations"},{"order":3,"title":"Write tests","description":"Unit and integration tests"}]`,
			expected: []PlannedSubtask{
				{Order: 1, Title: "Create schema", Description: "Add migration for users table"},
				{Order: 2, Title: "Add endpoints", Description: "REST API for CRUD operations"},
				{Order: 3, Title: "Write tests", Description: "Unit and integration tests"},
			},
		},
		{
			name:     "single subtask",
			response: `[{"order":1,"title":"Fix bug","description":"Patch the nil pointer in handler"}]`,
			expected: []PlannedSubtask{
				{Order: 1, Title: "Fix bug", Description: "Patch the nil pointer in handler"},
			},
		},
		{
			name:     "subtasks with empty description",
			response: `[{"order":1,"title":"Setup","description":""},{"order":2,"title":"Implement","description":""}]`,
			expected: []PlannedSubtask{
				{Order: 1, Title: "Setup", Description: ""},
				{Order: 2, Title: "Implement", Description: ""},
			},
		},
		{
			name:     "five subtasks in order",
			response: `[{"order":1,"title":"A","description":"first"},{"order":2,"title":"B","description":"second"},{"order":3,"title":"C","description":"third"},{"order":4,"title":"D","description":"fourth"},{"order":5,"title":"E","description":"fifth"}]`,
			expected: []PlannedSubtask{
				{Order: 1, Title: "A", Description: "first"},
				{Order: 2, Title: "B", Description: "second"},
				{Order: 3, Title: "C", Description: "third"},
				{Order: 4, Title: "D", Description: "fourth"},
				{Order: 5, Title: "E", Description: "fifth"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want %q", got, "application/json")
				}
				if got := r.Header.Get("x-api-key"); got != "test-api-key" {
					t.Errorf("x-api-key = %q, want %q", got, "test-api-key")
				}
				if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
					t.Errorf("anthropic-version = %q, want %q", got, "2023-06-01")
				}

				// Verify request body
				var reqBody haikuRequest
				if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
					t.Errorf("failed to decode request body: %v", err)
				}
				if reqBody.Model != "claude-haiku-4-5-20251001" {
					t.Errorf("model = %q, want %q", reqBody.Model, "claude-haiku-4-5-20251001")
				}
				if len(reqBody.Messages) != 1 {
					t.Errorf("messages length = %d, want 1", len(reqBody.Messages))
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(makeHaikuResponse(tt.response)))
			}))
			defer server.Close()

			parser := newTestHaikuParser(server.URL)
			subtasks, err := parser.ParsePlanning(context.Background(), "some planning output")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(subtasks) != len(tt.expected) {
				t.Fatalf("got %d subtasks, want %d", len(subtasks), len(tt.expected))
			}

			for i, want := range tt.expected {
				got := subtasks[i]
				if got.Order != want.Order {
					t.Errorf("subtask[%d].Order = %d, want %d", i, got.Order, want.Order)
				}
				if got.Title != want.Title {
					t.Errorf("subtask[%d].Title = %q, want %q", i, got.Title, want.Title)
				}
				if got.Description != want.Description {
					t.Errorf("subtask[%d].Description = %q, want %q", i, got.Description, want.Description)
				}
			}
		})
	}
}

func TestHaikuParser_ParsePlanning_MalformedJSON(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "invalid JSON array",
			response: `not valid json at all`,
		},
		{
			name:     "JSON object instead of array",
			response: `{"order":1,"title":"Only one","description":"Not an array"}`,
		},
		{
			name:     "truncated JSON",
			response: `[{"order":1,"title":"Incomplete`,
		},
		{
			name:     "HTML instead of JSON",
			response: `<html><body>Error</body></html>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(makeHaikuResponse(tt.response)))
			}))
			defer server.Close()

			parser := newTestHaikuParser(server.URL)
			_, err := parser.ParsePlanning(context.Background(), "some planning output")
			if err == nil {
				t.Error("expected error for malformed JSON, got nil")
			}
		})
	}
}

func TestHaikuParser_ParsePlanning_HTTPError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized},
		{name: "rate limited", statusCode: http.StatusTooManyRequests},
		{name: "server error", statusCode: http.StatusInternalServerError},
		{name: "bad gateway", statusCode: http.StatusBadGateway},
		{name: "service unavailable", statusCode: http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"error":"test error"}`))
			}))
			defer server.Close()

			parser := newTestHaikuParser(server.URL)
			_, err := parser.ParsePlanning(context.Background(), "some planning output")
			if err == nil {
				t.Errorf("expected error for HTTP %d, got nil", tt.statusCode)
			}
		})
	}
}

func TestHaikuParser_ParsePlanning_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than client timeout
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	parser := &HaikuParser{
		apiKey: "test-api-key",
		apiURL: server.URL,
		model:  "claude-haiku-4-5-20251001",
		httpClient: &http.Client{
			Timeout: 50 * time.Millisecond, // Very short timeout
		},
	}

	_, err := parser.ParsePlanning(context.Background(), "some planning output")
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestHaikuParser_ParsePlanning_EmptyInput(t *testing.T) {
	// Should fail before making any HTTP call
	parser := newTestHaikuParser("http://should-not-be-called")
	_, err := parser.ParsePlanning(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty input, got nil")
	}
}

func TestHaikuParser_ParsePlanning_EmptyAPIResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Empty content array
		_, _ = w.Write([]byte(`{"content":[]}`))
	}))
	defer server.Close()

	parser := newTestHaikuParser(server.URL)
	_, err := parser.ParsePlanning(context.Background(), "some planning output")
	if err == nil {
		t.Error("expected error for empty API response, got nil")
	}
}

func TestHaikuParser_ParsePlanning_EmptySubtaskArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeHaikuResponse(`[]`)))
	}))
	defer server.Close()

	parser := newTestHaikuParser(server.URL)
	_, err := parser.ParsePlanning(context.Background(), "some planning output")
	if err == nil {
		t.Error("expected error for empty subtask array, got nil")
	}
}

func TestHaikuParser_ParsePlanning_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	parser := newTestHaikuParser(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := parser.ParsePlanning(ctx, "some planning output")
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

func TestNewHaikuParser(t *testing.T) {
	parser := NewHaikuParser("my-api-key")
	if parser == nil {
		t.Fatal("NewHaikuParser returned nil")
	}
	if parser.apiKey != "my-api-key" {
		t.Errorf("apiKey = %q, want %q", parser.apiKey, "my-api-key")
	}
	if parser.model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want %q", parser.model, "claude-haiku-4-5-20251001")
	}
	if parser.apiURL != "https://api.anthropic.com/v1/messages" {
		t.Errorf("apiURL = %q, want default Anthropic URL", parser.apiURL)
	}
	if parser.httpClient == nil {
		t.Error("httpClient is nil")
	}
}
