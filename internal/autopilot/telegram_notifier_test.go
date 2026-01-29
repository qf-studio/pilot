package autopilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/adapters/telegram"
)

func TestTelegramNotifier_NotifyMerged(t *testing.T) {
	var receivedText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegram.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		receivedText = req.Text

		resp := telegram.SendMessageResponse{OK: true}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := telegram.NewClientWithBaseURL("test-token", server.URL)
	notifier := NewTelegramNotifier(client, "123456")

	prState := &PRState{
		PRNumber: 42,
		Stage:    StageMerged,
	}

	err := notifier.NotifyMerged(context.Background(), prState)
	if err != nil {
		t.Fatalf("NotifyMerged error: %v", err)
	}

	if !strings.Contains(receivedText, "✅") {
		t.Error("expected merged notification to contain ✅")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected merged notification to contain PR number")
	}
	if !strings.Contains(receivedText, "merged") {
		t.Error("expected merged notification to contain 'merged'")
	}
}

func TestTelegramNotifier_NotifyCIFailed(t *testing.T) {
	var receivedText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegram.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		receivedText = req.Text

		resp := telegram.SendMessageResponse{OK: true}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := telegram.NewClientWithBaseURL("test-token", server.URL)
	notifier := NewTelegramNotifier(client, "123456")

	prState := &PRState{PRNumber: 42}
	failedChecks := []string{"build", "lint"}

	err := notifier.NotifyCIFailed(context.Background(), prState, failedChecks)
	if err != nil {
		t.Fatalf("NotifyCIFailed error: %v", err)
	}

	if !strings.Contains(receivedText, "❌") {
		t.Error("expected CI failed notification to contain ❌")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected CI failed notification to contain PR number")
	}
	if !strings.Contains(receivedText, "build") {
		t.Error("expected CI failed notification to contain failed check 'build'")
	}
	if !strings.Contains(receivedText, "lint") {
		t.Error("expected CI failed notification to contain failed check 'lint'")
	}
}

func TestTelegramNotifier_NotifyCIFailed_NoChecks(t *testing.T) {
	var receivedText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegram.SendMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedText = req.Text

		resp := telegram.SendMessageResponse{OK: true}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := telegram.NewClientWithBaseURL("test-token", server.URL)
	notifier := NewTelegramNotifier(client, "123456")

	prState := &PRState{PRNumber: 42}

	err := notifier.NotifyCIFailed(context.Background(), prState, nil)
	if err != nil {
		t.Fatalf("NotifyCIFailed error: %v", err)
	}

	if !strings.Contains(receivedText, "unknown") {
		t.Error("expected CI failed notification with no checks to mention 'unknown'")
	}
}

func TestTelegramNotifier_NotifyApprovalRequired(t *testing.T) {
	var receivedText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegram.SendMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedText = req.Text

		resp := telegram.SendMessageResponse{OK: true}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := telegram.NewClientWithBaseURL("test-token", server.URL)
	notifier := NewTelegramNotifier(client, "123456")

	prState := &PRState{PRNumber: 42}

	err := notifier.NotifyApprovalRequired(context.Background(), prState)
	if err != nil {
		t.Fatalf("NotifyApprovalRequired error: %v", err)
	}

	if !strings.Contains(receivedText, "⏳") {
		t.Error("expected approval notification to contain ⏳")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected approval notification to contain PR number")
	}
	if !strings.Contains(receivedText, "/approve 42") {
		t.Error("expected approval notification to contain approve command")
	}
	if !strings.Contains(receivedText, "/reject 42") {
		t.Error("expected approval notification to contain reject command")
	}
}

func TestTelegramNotifier_NotifyFixIssueCreated(t *testing.T) {
	var receivedText string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegram.SendMessageRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		receivedText = req.Text

		resp := telegram.SendMessageResponse{OK: true}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := telegram.NewClientWithBaseURL("test-token", server.URL)
	notifier := NewTelegramNotifier(client, "123456")

	prState := &PRState{PRNumber: 42}

	err := notifier.NotifyFixIssueCreated(context.Background(), prState, 100)
	if err != nil {
		t.Fatalf("NotifyFixIssueCreated error: %v", err)
	}

	if !strings.Contains(receivedText, "🔄") {
		t.Error("expected fix issue notification to contain 🔄")
	}
	if !strings.Contains(receivedText, "Issue #100") {
		t.Error("expected fix issue notification to contain issue number")
	}
	if !strings.Contains(receivedText, "PR #42") {
		t.Error("expected fix issue notification to contain PR number")
	}
}

func TestNewTelegramNotifier(t *testing.T) {
	client := telegram.NewClient("test-token")
	notifier := NewTelegramNotifier(client, "123456")

	if notifier == nil {
		t.Fatal("NewTelegramNotifier returned nil")
	}
	if notifier.client != client {
		t.Error("client not set correctly")
	}
	if notifier.chatID != "123456" {
		t.Errorf("chatID = %s, want 123456", notifier.chatID)
	}
}

func TestEscapeMarkdown(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello", "hello"},
		{"hello_world", "hello\\_world"},
		{"*bold*", "\\*bold\\*"},
		{"[link](url)", "\\[link\\]\\(url\\)"},
		{"code`block`", "code\\`block\\`"},
		{"item #1", "item \\#1"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeMarkdown(tt.input)
			if result != tt.expected {
				t.Errorf("escapeMarkdown(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
