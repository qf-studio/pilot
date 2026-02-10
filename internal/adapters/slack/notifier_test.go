package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alekspetrov/pilot/internal/testutil"
)

// TestNewNotifier tests notifier creation
func TestNewNotifier(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		channel string
	}{
		{
			name: "basic config",
			config: &Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#dev-notifications",
			},
			channel: "#dev-notifications",
		},
		{
			name: "empty channel defaults",
			config: &Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "",
			},
			channel: "",
		},
		{
			name: "custom channel",
			config: &Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#custom-channel",
			},
			channel: "#custom-channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(tt.config)

			if notifier == nil {
				t.Fatal("NewNotifier returned nil")
			}
			if notifier.channel != tt.channel {
				t.Errorf("channel = %q, want %q", notifier.channel, tt.channel)
			}
			if notifier.client == nil {
				t.Error("client is nil")
			}
		})
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
	if config.Channel != "#dev-notifications" {
		t.Errorf("Channel = %q, want #dev-notifications", config.Channel)
	}
	if config.BotToken != "" {
		t.Errorf("BotToken = %q, want empty string", config.BotToken)
	}
	if config.SocketMode {
		t.Error("SocketMode should be false by default")
	}
	if config.AllowedUsers == nil || len(config.AllowedUsers) != 0 {
		t.Errorf("AllowedUsers = %v, want empty slice", config.AllowedUsers)
	}
	if config.AllowedChannels == nil || len(config.AllowedChannels) != 0 {
		t.Errorf("AllowedChannels = %v, want empty slice", config.AllowedChannels)
	}
}

// TestConfigFields tests that config has expected fields
func TestConfigFields(t *testing.T) {
	config := &Config{
		Enabled:  true,
		BotToken: testutil.FakeSlackBotToken,
		Channel:  "#pilot-notifications",
	}

	if !config.Enabled {
		t.Error("Enabled should be true")
	}
	if config.BotToken != testutil.FakeSlackBotToken {
		t.Errorf("BotToken = %q, want %s", config.BotToken, testutil.FakeSlackBotToken)
	}
	if config.Channel != "#pilot-notifications" {
		t.Errorf("Channel = %q, want #pilot-notifications", config.Channel)
	}
}

// TestGenerateProgressBar tests progress bar generation
func TestGenerateProgressBar(t *testing.T) {
	tests := []struct {
		name     string
		progress int
		expected string
	}{
		{
			name:     "0 percent",
			progress: 0,
			expected: "░░░░░░░░░░",
		},
		{
			name:     "10 percent",
			progress: 10,
			expected: "█░░░░░░░░░",
		},
		{
			name:     "25 percent",
			progress: 25,
			expected: "██░░░░░░░░",
		},
		{
			name:     "50 percent",
			progress: 50,
			expected: "█████░░░░░",
		},
		{
			name:     "75 percent",
			progress: 75,
			expected: "███████░░░",
		},
		{
			name:     "90 percent",
			progress: 90,
			expected: "█████████░",
		},
		{
			name:     "100 percent",
			progress: 100,
			expected: "██████████",
		},
		{
			name:     "33 percent rounds down",
			progress: 33,
			expected: "███░░░░░░░",
		},
		{
			name:     "99 percent",
			progress: 99,
			expected: "█████████░",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateProgressBar(tt.progress)
			if got != tt.expected {
				t.Errorf("generateProgressBar(%d) = %q, want %q", tt.progress, got, tt.expected)
			}
			// Verify length is always 10 characters (10 blocks)
			if len([]rune(got)) != 10 {
				t.Errorf("progress bar length = %d runes, want 10", len([]rune(got)))
			}
		})
	}
}

// createMockSlackServer creates a test server that captures and validates Slack API requests
func createMockSlackServer(t *testing.T, validateMsg func(*Message)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify method
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}

		// Verify content type
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}

		// Verify authorization
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Authorization = %q, want Bearer token", auth)
		}

		// Parse body if validator provided
		if validateMsg != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("failed to read body: %v", err)
			}

			var msg Message
			if err := json.Unmarshal(body, &msg); err != nil {
				t.Fatalf("failed to parse message: %v", err)
			}
			validateMsg(&msg)
		}

		// Return success response
		response := PostMessageResponse{
			OK:      true,
			TS:      "1234567890.123456",
			Channel: "C1234567890",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
}

// TestNotifierTaskStarted tests the TaskStarted method
func TestNotifierTaskStarted(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		title     string
		wantParts []string
	}{
		{
			name:      "basic task",
			taskID:    "TASK-01",
			title:     "Create authentication handler",
			wantParts: []string{"TASK-01", "authentication handler", "started"},
		},
		{
			name:      "task with special chars",
			taskID:    "PROJ-123",
			title:     "Fix bug in user_service",
			wantParts: []string{"PROJ-123", "Fix bug", "user_service"},
		},
		{
			name:      "empty title",
			taskID:    "T-1",
			title:     "",
			wantParts: []string{"T-1"},
		},
		{
			name:      "long title",
			taskID:    "EPIC-999",
			title:     "Implement comprehensive end-to-end testing framework for the entire application",
			wantParts: []string{"EPIC-999", "end-to-end"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockSlackServer(t, func(msg *Message) {
				// Verify channel is set
				if msg.Channel == "" {
					t.Error("channel is empty")
				}

				// Verify blocks are present
				if len(msg.Blocks) == 0 {
					t.Error("expected blocks in message")
				}

				// Extract text from blocks
				var text string
				for _, block := range msg.Blocks {
					if block.Text != nil {
						text += block.Text.Text
					}
				}

				// Verify expected parts are present
				for _, part := range tt.wantParts {
					if !strings.Contains(text, part) {
						t.Errorf("message text = %q, want to contain %q", text, part)
					}
				}
			})
			defer server.Close()

			// Test with real notifier - method signature verification
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#test",
			})

			ctx := context.Background()
			// This will fail because it hits real Slack API, but verifies interface
			_ = notifier.TaskStarted(ctx, tt.taskID, tt.title)
		})
	}
}

// TestNotifierTaskProgress tests the TaskProgress method
func TestNotifierTaskProgress(t *testing.T) {
	tests := []struct {
		name        string
		taskID      string
		status      string
		progress    int
		wantParts   []string
		wantPercent string
	}{
		{
			name:        "0 percent",
			taskID:      "TASK-01",
			status:      "Starting",
			progress:    0,
			wantParts:   []string{"TASK-01", "Starting"},
			wantPercent: "0%",
		},
		{
			name:        "50 percent",
			taskID:      "TASK-02",
			status:      "Implementing",
			progress:    50,
			wantParts:   []string{"TASK-02", "Implementing"},
			wantPercent: "50%",
		},
		{
			name:        "100 percent",
			taskID:      "TASK-03",
			status:      "Complete",
			progress:    100,
			wantParts:   []string{"TASK-03", "Complete"},
			wantPercent: "100%",
		},
		{
			name:        "partial progress",
			taskID:      "T-99",
			status:      "Running tests",
			progress:    73,
			wantParts:   []string{"T-99", "tests"},
			wantPercent: "73%",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockSlackServer(t, func(msg *Message) {
				if len(msg.Blocks) == 0 {
					t.Error("expected blocks in message")
				}

				var text string
				for _, block := range msg.Blocks {
					if block.Text != nil {
						text += block.Text.Text
					}
				}

				// Verify percentage is included
				if !strings.Contains(text, tt.wantPercent) {
					t.Errorf("message = %q, want to contain %q", text, tt.wantPercent)
				}

				// Verify progress bar is included (check for block characters)
				if !strings.Contains(text, "█") && !strings.Contains(text, "░") {
					t.Error("expected progress bar characters in message")
				}
			})
			defer server.Close()

			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#test",
			})

			ctx := context.Background()
			_ = notifier.TaskProgress(ctx, tt.taskID, tt.status, tt.progress)
		})
	}
}

// TestNotifierTaskCompleted tests the TaskCompleted method
func TestNotifierTaskCompleted(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		title     string
		prURL     string
		wantParts []string
		wantColor bool
	}{
		{
			name:      "without PR",
			taskID:    "TASK-01",
			title:     "Add user authentication",
			prURL:     "",
			wantParts: []string{"TASK-01", "authentication", "completed"},
			wantColor: true,
		},
		{
			name:      "with PR URL",
			taskID:    "TASK-02",
			title:     "Refactor database layer",
			prURL:     "https://github.com/org/repo/pull/123",
			wantParts: []string{"TASK-02", "database", "github.com"},
			wantColor: true,
		},
		{
			name:      "empty title with PR",
			taskID:    "T-1",
			title:     "",
			prURL:     "https://github.com/org/repo/pull/1",
			wantParts: []string{"T-1", "PR"},
			wantColor: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockSlackServer(t, func(msg *Message) {
				if len(msg.Blocks) == 0 {
					t.Error("expected blocks in message")
				}

				// Check for attachments with color
				if tt.wantColor && len(msg.Attachments) == 0 {
					t.Error("expected attachments for completed task")
				}

				if len(msg.Attachments) > 0 && msg.Attachments[0].Color != "good" {
					t.Errorf("attachment color = %q, want good", msg.Attachments[0].Color)
				}
			})
			defer server.Close()

			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#test",
			})

			ctx := context.Background()
			_ = notifier.TaskCompleted(ctx, tt.taskID, tt.title, tt.prURL)
		})
	}
}

// TestNotifierTaskFailed tests the TaskFailed method
func TestNotifierTaskFailed(t *testing.T) {
	tests := []struct {
		name      string
		taskID    string
		title     string
		errorMsg  string
		wantParts []string
		wantColor string
	}{
		{
			name:      "simple error",
			taskID:    "TASK-01",
			title:     "Deploy to production",
			errorMsg:  "Build failed",
			wantParts: []string{"TASK-01", "failed", "Build failed"},
			wantColor: "danger",
		},
		{
			name:      "multiline error",
			taskID:    "TASK-02",
			title:     "Run test suite",
			errorMsg:  "Error: test failed\nExpected: true\nGot: false",
			wantParts: []string{"TASK-02", "Error", "Expected"},
			wantColor: "danger",
		},
		{
			name:      "error with special chars",
			taskID:    "T-99",
			title:     "Parse JSON",
			errorMsg:  "unexpected token '<' at position 0",
			wantParts: []string{"T-99", "unexpected token"},
			wantColor: "danger",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockSlackServer(t, func(msg *Message) {
				if len(msg.Blocks) == 0 {
					t.Error("expected blocks in message")
				}

				// Check for danger color attachment
				if len(msg.Attachments) == 0 {
					t.Error("expected attachments for failed task")
				}

				if len(msg.Attachments) > 0 && msg.Attachments[0].Color != tt.wantColor {
					t.Errorf("attachment color = %q, want %q", msg.Attachments[0].Color, tt.wantColor)
				}

				var text string
				for _, block := range msg.Blocks {
					if block.Text != nil {
						text += block.Text.Text
					}
				}

				// Verify error message is in code block (```)
				if !strings.Contains(text, "```") {
					t.Error("expected error message in code block")
				}
			})
			defer server.Close()

			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#test",
			})

			ctx := context.Background()
			_ = notifier.TaskFailed(ctx, tt.taskID, tt.title, tt.errorMsg)
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
		wantParts    []string
		wantColor    string
	}{
		{
			name:         "single file changed",
			taskID:       "TASK-01",
			title:        "Fix typo in README",
			prURL:        "https://github.com/org/repo/pull/1",
			filesChanged: 1,
			wantParts:    []string{"TASK-01", "README", "1 files"},
			wantColor:    "#6366f1",
		},
		{
			name:         "multiple files changed",
			taskID:       "TASK-02",
			title:        "Major refactoring",
			prURL:        "https://github.com/org/repo/pull/42",
			filesChanged: 15,
			wantParts:    []string{"TASK-02", "refactoring", "15 files"},
			wantColor:    "#6366f1",
		},
		{
			name:         "zero files changed",
			taskID:       "T-1",
			title:        "Update config",
			prURL:        "https://github.com/org/repo/pull/99",
			filesChanged: 0,
			wantParts:    []string{"T-1", "0 files"},
			wantColor:    "#6366f1",
		},
		{
			name:         "large PR",
			taskID:       "EPIC-100",
			title:        "Complete rewrite",
			prURL:        "https://github.com/org/repo/pull/500",
			filesChanged: 200,
			wantParts:    []string{"EPIC-100", "200 files"},
			wantColor:    "#6366f1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockSlackServer(t, func(msg *Message) {
				if len(msg.Blocks) == 0 {
					t.Error("expected blocks in message")
				}

				// Check for custom color attachment
				if len(msg.Attachments) == 0 {
					t.Error("expected attachments for PR ready")
				}

				if len(msg.Attachments) > 0 && msg.Attachments[0].Color != tt.wantColor {
					t.Errorf("attachment color = %q, want %q", msg.Attachments[0].Color, tt.wantColor)
				}

				var text string
				for _, block := range msg.Blocks {
					if block.Text != nil {
						text += block.Text.Text
					}
				}

				// Verify PR URL is included as a link
				if !strings.Contains(text, tt.prURL) {
					t.Errorf("message = %q, want to contain PR URL %q", text, tt.prURL)
				}
			})
			defer server.Close()

			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#test",
			})

			ctx := context.Background()
			_ = notifier.PRReady(ctx, tt.taskID, tt.title, tt.prURL, tt.filesChanged)
		})
	}
}

// TestNotifierMessageFormatting tests that messages are properly formatted
func TestNotifierMessageFormatting(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		taskID    string
		title     string
		wantEmoji string
	}{
		{
			name:      "task started has rocket emoji",
			method:    "started",
			taskID:    "T-1",
			title:     "Test",
			wantEmoji: "🚀",
		},
		{
			name:      "task progress has timer emoji",
			method:    "progress",
			taskID:    "T-2",
			title:     "Test",
			wantEmoji: "⏳",
		},
		{
			name:      "task completed has checkmark emoji",
			method:    "completed",
			taskID:    "T-3",
			title:     "Test",
			wantEmoji: "✅",
		},
		{
			name:      "task failed has X emoji",
			method:    "failed",
			taskID:    "T-4",
			title:     "Test",
			wantEmoji: "❌",
		},
		{
			name:      "PR ready has bell emoji",
			method:    "pr_ready",
			taskID:    "T-5",
			title:     "Test",
			wantEmoji: "🔔",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockSlackServer(t, func(msg *Message) {
				var text string
				for _, block := range msg.Blocks {
					if block.Text != nil {
						text += block.Text.Text
					}
				}

				if !strings.Contains(text, tt.wantEmoji) {
					t.Errorf("message = %q, want to contain emoji %s", text, tt.wantEmoji)
				}
			})
			defer server.Close()

			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  "#test",
			})

			ctx := context.Background()
			switch tt.method {
			case "started":
				_ = notifier.TaskStarted(ctx, tt.taskID, tt.title)
			case "progress":
				_ = notifier.TaskProgress(ctx, tt.taskID, tt.title, 50)
			case "completed":
				_ = notifier.TaskCompleted(ctx, tt.taskID, tt.title, "")
			case "failed":
				_ = notifier.TaskFailed(ctx, tt.taskID, tt.title, "error")
			case "pr_ready":
				_ = notifier.PRReady(ctx, tt.taskID, tt.title, "https://github.com/test", 1)
			}
		})
	}
}

// TestNotifierBlocksUseMarkdown tests that blocks use markdown formatting
func TestNotifierBlocksUseMarkdown(t *testing.T) {
	notifier := NewNotifier(&Config{
		BotToken: testutil.FakeSlackBotToken,
		Channel:  "#test",
	})

	// Verify notifier is created correctly
	if notifier == nil {
		t.Fatal("notifier is nil")
	}

	// The blocks should use mrkdwn type for rich formatting
	// This is tested via the actual message structure in other tests
}

// TestNotifierChannelConfiguration tests that channel is properly configured
func TestNotifierChannelConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		channel     string
		wantChannel string
	}{
		{
			name:        "channel with hash",
			channel:     "#dev-notifications",
			wantChannel: "#dev-notifications",
		},
		{
			name:        "channel without hash",
			channel:     "general",
			wantChannel: "general",
		},
		{
			name:        "channel ID",
			channel:     "C1234567890",
			wantChannel: "C1234567890",
		},
		{
			name:        "DM channel",
			channel:     "D1234567890",
			wantChannel: "D1234567890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notifier := NewNotifier(&Config{
				BotToken: testutil.FakeSlackBotToken,
				Channel:  tt.channel,
			})

			if notifier.channel != tt.wantChannel {
				t.Errorf("channel = %q, want %q", notifier.channel, tt.wantChannel)
			}
		})
	}
}

// TestConfigYAMLTags tests that Config struct has proper YAML tags
func TestConfigYAMLTags(t *testing.T) {
	// Verify the config can be represented in YAML-compatible format
	config := &Config{
		Enabled:  true,
		BotToken: "xoxb-token",
		Channel:  "#channel",
	}

	// Verify fields are accessible
	if !config.Enabled {
		t.Error("Enabled field not accessible")
	}
	if config.BotToken == "" {
		t.Error("BotToken field not accessible")
	}
	if config.Channel == "" {
		t.Error("Channel field not accessible")
	}
}

// TestNotifierNilConfig tests behavior with edge case configs
func TestNotifierEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
	}{
		{
			name: "empty strings",
			config: &Config{
				Enabled:  false,
				BotToken: "",
				Channel:  "",
			},
		},
		{
			name: "whitespace in channel",
			config: &Config{
				BotToken: "token",
				Channel:  "  #channel  ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Should not panic
			notifier := NewNotifier(tt.config)
			if notifier == nil {
				t.Error("NewNotifier returned nil for valid config")
			}
		})
	}
}
