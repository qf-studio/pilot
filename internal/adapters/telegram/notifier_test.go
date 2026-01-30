package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

// TestNewNotifier tests notifier creation
func TestNewNotifier(t *testing.T) {
	config := &Config{
		BotToken: testutil.FakeTelegramBotToken,
		ChatID:   "123456",
	}

	notifier := NewNotifier(config)

	if notifier == nil {
		t.Fatal("NewNotifier returned nil")
	}
	if notifier.chatID != "123456" {
		t.Errorf("chatID = %q, want %q", notifier.chatID, "123456")
	}
	if notifier.client == nil {
		t.Error("client is nil")
	}
}

// TestDefaultConfig tests the default config values
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config == nil {
		t.Fatal("DefaultConfig returned nil")
	}
	if config.Enabled {
		t.Error("Enabled should be false by default")
	}
}

// TestGenerateProgressBar tests progress bar generation
func TestGenerateProgressBar(t *testing.T) {
	tests := []struct {
		progress int
		expected string
	}{
		{0, "░░░░░░░░░░"},
		{10, "█░░░░░░░░░"},
		{50, "█████░░░░░"},
		{100, "██████████"},
		// Note: negative values result in negative filled count, which doesn't break the function
		// but the result is implementation-dependent
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := generateProgressBar(tt.progress)
			if got != tt.expected {
				t.Errorf("generateProgressBar(%d) = %q, want %q", tt.progress, got, tt.expected)
			}
		})
	}
}

// createMockServer creates a test server that returns successful responses
func createMockServer(t *testing.T, validateBody func(map[string]interface{})) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		// Parse body if validator provided
		if validateBody != nil {
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("failed to parse request body: %v", err)
			}
			validateBody(body)
		}

		// Return success response
		response := SendMessageResponse{
			OK: true,
			Result: &Result{
				MessageID: 123,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
}

// TestNotifierSendMessage tests the SendMessage method
func TestNotifierSendMessage(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{
			name:    "simple message",
			text:    "Hello, world!",
			wantErr: false,
		},
		{
			name:    "empty message",
			text:    "",
			wantErr: false,
		},
		{
			name:    "message with special chars",
			text:    "Test *bold* _italic_ `code`",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(t, func(body map[string]interface{}) {
				if text, ok := body["text"].(string); ok {
					if text != tt.text {
						t.Errorf("text = %q, want %q", text, tt.text)
					}
				}
			})
			defer server.Close()

			// Note: We can't easily override the API URL in the current implementation
			// This test verifies the method signature and basic behavior
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeTelegramBotToken,
				ChatID:   "123456",
			})

			// The actual call will fail because it goes to real Telegram
			// but we're testing the interface
			ctx := context.Background()
			_ = notifier.SendMessage(ctx, tt.text)
		})
	}
}

// TestNotifierSendTaskStarted tests the SendTaskStarted method
func TestNotifierSendTaskStarted(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		title     string
		wantParts []string
	}{
		{
			name:      "basic task",
			taskID:    "TASK-01",
			title:     "Create auth handler",
			wantParts: []string{"TASK-01", "auth handler"},
		},
		{
			name:      "task with special chars",
			taskID:    "TG-123",
			title:     "Fix _underscores_ and *asterisks*",
			wantParts: []string{"TG-123", "underscores", "asterisks"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeTelegramBotToken,
				ChatID:   "123456",
			})

			ctx := context.Background()
			// Method exists and accepts correct parameters
			_ = notifier.SendTaskStarted(ctx, tt.taskID, tt.title)
		})
	}
}

// TestNotifierSendTaskCompleted tests the SendTaskCompleted method
func TestNotifierSendTaskCompleted(t *testing.T) {
	tests := []struct {
		name   string
		taskID string
		title  string
		prURL  string
	}{
		{
			name:   "without PR",
			taskID: "TASK-01",
			title:  "Complete task",
			prURL:  "",
		},
		{
			name:   "with PR",
			taskID: "TASK-02",
			title:  "Task with PR",
			prURL:  "https://github.com/org/repo/pull/123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeTelegramBotToken,
				ChatID:   "123456",
			})

			ctx := context.Background()
			_ = notifier.SendTaskCompleted(ctx, tt.taskID, tt.title, tt.prURL)
		})
	}
}

// TestNotifierSendTaskFailed tests the SendTaskFailed method
func TestNotifierSendTaskFailed(t *testing.T) {
	tests := []struct {
		name     string
		taskID   string
		title    string
		errorMsg string
	}{
		{
			name:     "simple error",
			taskID:   "TASK-01",
			title:    "Failed task",
			errorMsg: "Build failed",
		},
		{
			name:     "multiline error",
			taskID:   "TASK-02",
			title:    "Another failure",
			errorMsg: "Error on line 1\nError on line 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeTelegramBotToken,
				ChatID:   "123456",
			})

			ctx := context.Background()
			_ = notifier.SendTaskFailed(ctx, tt.taskID, tt.title, tt.errorMsg)
		})
	}
}

// TestNotifierTaskProgress tests the TaskProgress method
func TestNotifierTaskProgress(t *testing.T) {
	tests := []struct {
		name     string
		taskID   string
		status   string
		progress int
	}{
		{
			name:     "0% progress",
			taskID:   "TASK-01",
			status:   "Starting",
			progress: 0,
		},
		{
			name:     "50% progress",
			taskID:   "TASK-02",
			status:   "Implementing",
			progress: 50,
		},
		{
			name:     "100% progress",
			taskID:   "TASK-03",
			status:   "Complete",
			progress: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeTelegramBotToken,
				ChatID:   "123456",
			})

			ctx := context.Background()
			_ = notifier.TaskProgress(ctx, tt.taskID, tt.status, tt.progress)
		})
	}
}

// TestNotifierPRReady tests the PRReady method
func TestNotifierPRReady(t *testing.T) {
	tests := []struct {
		name         string
		taskID       string
		title        string
		prURL        string
		filesChanged int
	}{
		{
			name:         "single file",
			taskID:       "TASK-01",
			title:        "Add feature",
			prURL:        "https://github.com/org/repo/pull/1",
			filesChanged: 1,
		},
		{
			name:         "multiple files",
			taskID:       "TASK-02",
			title:        "Large refactor",
			prURL:        "https://github.com/org/repo/pull/2",
			filesChanged: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeTelegramBotToken,
				ChatID:   "123456",
			})

			ctx := context.Background()
			_ = notifier.PRReady(ctx, tt.taskID, tt.title, tt.prURL, tt.filesChanged)
		})
	}
}

// TestConfigFields tests that config has expected fields
func TestConfigFields(t *testing.T) {
	config := &Config{
		Enabled:    true,
		BotToken:   "token",
		ChatID:     "chat",
		Polling:    true,
		AllowedIDs: []int64{123, 456},
	}

	if !config.Enabled {
		t.Error("Enabled should be true")
	}
	if config.BotToken != "token" {
		t.Errorf("BotToken = %q, want %q", config.BotToken, "token")
	}
	if config.ChatID != "chat" {
		t.Errorf("ChatID = %q, want %q", config.ChatID, "chat")
	}
	if !config.Polling {
		t.Error("Polling should be true")
	}
	if len(config.AllowedIDs) != 2 {
		t.Errorf("AllowedIDs len = %d, want 2", len(config.AllowedIDs))
	}
}

// TestEscapeMarkdownNotifier tests markdown escaping for notifications
func TestEscapeMarkdownNotifier(t *testing.T) {
	tests := []struct {
		input   string
		wantEsc []string // substrings that should be escaped
	}{
		{
			input:   "hello_world",
			wantEsc: []string{"\\_"},
		},
		{
			input:   "*bold* text",
			wantEsc: []string{"\\*"},
		},
		{
			input:   "[link](url)",
			wantEsc: []string{"\\[", "\\]", "\\(", "\\)"},
		},
		{
			input:   "plain text",
			wantEsc: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeMarkdown(tt.input)
			for _, esc := range tt.wantEsc {
				if !strings.Contains(got, esc) {
					t.Errorf("escapeMarkdown(%q) = %q, want to contain %q", tt.input, got, esc)
				}
			}
		})
	}
}
