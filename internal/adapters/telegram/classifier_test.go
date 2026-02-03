package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// mockAnthropicResponse creates a valid Anthropic API response
func mockAnthropicResponse(intent, reasoning string, confidence float64, taskSummary string) string {
	response := map[string]interface{}{
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": toJSON(map[string]interface{}{
					"intent":       intent,
					"confidence":   confidence,
					"reasoning":    reasoning,
					"task_summary": taskSummary,
				}),
			},
		},
	}
	b, _ := json.Marshal(response)
	return string(b)
}

func toJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestLLMClassifier_CasualCommit_ClassifiesAsChat(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockAnthropicResponse("chat", "Casual reaction to discussion, not an actionable task", 0.9, "")))
	}))
	defer server.Close()

	// Create classifier with test config
	cfg := &LLMClassifierConfig{
		Enabled: true,
		Model:   "claude-3-haiku-20240307",
		Timeout: 5 * time.Second,
	}

	// Set test API key
	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	classifier := NewLLMClassifier(cfg)

	// Override httpClient to use test server
	classifier.httpClient = &http.Client{
		Transport: &testTransport{server: server},
	}

	// Test message: "Wow, let's commit changes first" should be chat, not task
	result, err := classifier.Classify(context.Background(), "Wow, let's commit changes first", nil)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if result.Intent != IntentChat {
		t.Errorf("expected IntentChat for casual commit mention, got %v", result.Intent)
	}
}

func TestLLMClassifier_CheckQuestion_ClassifiesAsQuestion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockAnthropicResponse("question", "Asking about status/execution, not requesting action", 0.95, "")))
	}))
	defer server.Close()

	cfg := &LLMClassifierConfig{
		Enabled: true,
		Model:   "claude-3-haiku-20240307",
		Timeout: 5 * time.Second,
	}

	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	classifier := NewLLMClassifier(cfg)
	classifier.httpClient = &http.Client{
		Transport: &testTransport{server: server},
	}

	// "Check if it was executed" should be question, not task
	result, err := classifier.Classify(context.Background(), "Check if it was executed", nil)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if result.Intent != IntentQuestion {
		t.Errorf("expected IntentQuestion for status check, got %v", result.Intent)
	}
}

func TestLLMClassifier_ClearTask_ClassifiesAsTask(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockAnthropicResponse("task", "Clear directive to implement UI element", 0.98, "Add a logout button to the user interface")))
	}))
	defer server.Close()

	cfg := &LLMClassifierConfig{
		Enabled: true,
		Model:   "claude-3-haiku-20240307",
		Timeout: 5 * time.Second,
	}

	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	classifier := NewLLMClassifier(cfg)
	classifier.httpClient = &http.Client{
		Transport: &testTransport{server: server},
	}

	// "Add a logout button" is a clear task
	result, err := classifier.Classify(context.Background(), "Add a logout button", nil)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	if result.Intent != IntentTask {
		t.Errorf("expected IntentTask for clear directive, got %v", result.Intent)
	}
	if result.TaskSummary == "" {
		t.Error("expected non-empty TaskSummary for task intent")
	}
}

func TestLLMClassifier_APIFailure_ReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"type": "server_error", "message": "Internal server error"}}`))
	}))
	defer server.Close()

	cfg := &LLMClassifierConfig{
		Enabled: true,
		Model:   "claude-3-haiku-20240307",
		Timeout: 5 * time.Second,
	}

	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	classifier := NewLLMClassifier(cfg)
	classifier.httpClient = &http.Client{
		Transport: &testTransport{server: server},
	}

	// API failure should return error (caller should fallback to regex)
	_, err := classifier.Classify(context.Background(), "Add a button", nil)
	if err == nil {
		t.Error("expected error on API failure, got nil")
	}
}

func TestLLMClassifier_Disabled_ReturnsError(t *testing.T) {
	cfg := &LLMClassifierConfig{
		Enabled: false,
	}

	classifier := NewLLMClassifier(cfg)

	_, err := classifier.Classify(context.Background(), "Test message", nil)
	if err == nil {
		t.Error("expected error when classifier disabled")
	}
}

func TestLLMClassifier_NoAPIKey_DisabledAutomatically(t *testing.T) {
	// Ensure no API key is set
	originalKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_API_KEY")
	defer func() {
		if originalKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalKey)
		}
	}()

	cfg := &LLMClassifierConfig{
		Enabled: true,
		Model:   "claude-3-haiku-20240307",
		Timeout: 5 * time.Second,
	}

	classifier := NewLLMClassifier(cfg)

	if classifier.IsEnabled() {
		t.Error("classifier should be disabled when ANTHROPIC_API_KEY is not set")
	}
}

func TestLLMClassifier_WithConversationHistory(t *testing.T) {
	var receivedPrompt string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the prompt to verify history is included
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) > 0 {
			receivedPrompt = req.Messages[0].Content
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockAnthropicResponse("task", "Confirmation after seeing plan", 0.85, "Execute the previously discussed plan")))
	}))
	defer server.Close()

	cfg := &LLMClassifierConfig{
		Enabled: true,
		Model:   "claude-3-haiku-20240307",
		Timeout: 5 * time.Second,
	}

	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	classifier := NewLLMClassifier(cfg)
	classifier.httpClient = &http.Client{
		Transport: &testTransport{server: server},
	}

	// Create conversation history
	history := []ConversationMessage{
		{Role: "user", Content: "Add a logout button", Intent: IntentTask, Timestamp: time.Now()},
		{Role: "assistant", Content: "I'll add a logout button to the header", Timestamp: time.Now()},
	}

	// "yes" with context should be interpreted based on history
	_, err := classifier.Classify(context.Background(), "yes, proceed", history)
	if err != nil {
		t.Fatalf("Classify failed: %v", err)
	}

	// Verify history was included in prompt
	if receivedPrompt == "" {
		t.Error("expected prompt to be sent")
	}
	if !containsStr(receivedPrompt, "Add a logout button") {
		t.Error("expected conversation history to be included in prompt")
	}
}

func TestMapStringToIntent(t *testing.T) {
	tests := []struct {
		input    string
		expected Intent
	}{
		{"command", IntentCommand},
		{"COMMAND", IntentCommand},
		{"greeting", IntentGreeting},
		{"question", IntentQuestion},
		{"research", IntentResearch},
		{"planning", IntentPlanning},
		{"chat", IntentChat},
		{"task", IntentTask},
		{"unknown", IntentChat}, // Default to chat for safety
		{"", IntentChat},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := mapStringToIntent(tt.input)
			if result != tt.expected {
				t.Errorf("mapStringToIntent(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestDefaultLLMClassifierConfig(t *testing.T) {
	cfg := DefaultLLMClassifierConfig()

	if cfg.Enabled != false {
		t.Error("default should have Enabled=false")
	}
	if cfg.Model != "claude-3-haiku-20240307" {
		t.Errorf("unexpected default model: %s", cfg.Model)
	}
	if cfg.Timeout != 2*time.Second {
		t.Errorf("unexpected default timeout: %v", cfg.Timeout)
	}
	if cfg.MaxHistory != 10 {
		t.Errorf("unexpected default MaxHistory: %d", cfg.MaxHistory)
	}
	if cfg.HistoryTTL != 30*time.Minute {
		t.Errorf("unexpected default HistoryTTL: %v", cfg.HistoryTTL)
	}
}

func TestLLMClassifier_ParseResponse_InvalidJSON(t *testing.T) {
	cfg := &LLMClassifierConfig{
		Enabled: true,
	}
	os.Setenv("ANTHROPIC_API_KEY", "test-api-key")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	classifier := NewLLMClassifier(cfg)

	// Test with invalid JSON
	_, err := classifier.parseResponse("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	// Test with markdown-wrapped JSON (should be handled)
	validResponse := `{"intent": "task", "confidence": 0.9, "reasoning": "test", "task_summary": ""}`
	result, err := classifier.parseResponse("```json\n" + validResponse + "\n```")
	if err != nil {
		t.Errorf("failed to parse markdown-wrapped JSON: %v", err)
	}
	if result.Intent != IntentTask {
		t.Errorf("expected IntentTask, got %v", result.Intent)
	}
}

// testTransport redirects requests to test server
type testTransport struct {
	server *httptest.Server
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect to test server
	req.URL.Scheme = "http"
	req.URL.Host = t.server.Listener.Addr().String()
	return http.DefaultTransport.RoundTrip(req)
}

// Helper function
func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
