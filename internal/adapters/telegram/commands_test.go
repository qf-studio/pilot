package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/testutil"
)

// mockTelegramServer creates a test server that captures sent messages
type mockTelegramServer struct {
	server        *httptest.Server
	sentMessages  []string
	sentKeyboards [][]InlineKeyboardButton
}

func newMockTelegramServer() *mockTelegramServer {
	m := &mockTelegramServer{
		sentMessages:  []string{},
		sentKeyboards: [][]InlineKeyboardButton{},
	}

	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse the request to capture sent messages
		if strings.Contains(r.URL.Path, "/sendMessage") {
			var req SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				m.sentMessages = append(m.sentMessages, req.Text)
				if req.ReplyMarkup != nil {
					m.sentKeyboards = append(m.sentKeyboards, req.ReplyMarkup.InlineKeyboard...)
				}
			}
		}

		// Return success response
		response := SendMessageResponse{
			OK: true,
			Result: &Result{
				MessageID: 123,
				ChatID:    456,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))

	return m
}

func (m *mockTelegramServer) close() {
	m.server.Close()
}

// newTestHandlerForCommands creates a Handler wired with a commsHandler for command tests.
func newTestHandlerForCommands(projects comms.ProjectSource, projectPath string) *Handler {
	ch := comms.NewHandler(&comms.HandlerConfig{
		Messenger:    &noopMessenger{},
		Projects:     projects,
		ProjectPath:  projectPath,
		TaskIDPrefix: "TG",
	})
	return &Handler{
		client:       NewClient(testutil.FakeTelegramBotToken),
		projects:     projects,
		projectPath:  projectPath,
		commsHandler: ch,
	}
}

// TestCommandHandler_HandleHelp tests the /help command
func TestCommandHandler_HandleHelp(t *testing.T) {
	mock := newMockTelegramServer()
	defer mock.close()

	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "123", "/help")

	// Check that message was formatted (we can't check exact content due to mock server)
	// The handler will try to send but the mock server won't match URLs
	// This test primarily validates that no panic occurs
}

// TestCommandHandler_HandleStatus tests the /status command
func TestCommandHandler_HandleStatus(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	tests := []struct {
		name   string
		chatID string
	}{
		{
			name:   "no running tasks",
			chatID: "chat1",
		},
		{
			name:   "different chat",
			chatID: "chat2",
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This will fail to send (no real Telegram) but should not panic
			cmd.HandleCommand(ctx, tt.chatID, "/status")
		})
	}
}

// TestCommandHandler_HandleCancel tests the /cancel command
func TestCommandHandler_HandleCancel(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	tests := []struct {
		name   string
		chatID string
	}{
		{
			name:   "nothing to cancel",
			chatID: "chat1",
		},
		{
			name:   "different chat",
			chatID: "chat2",
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd.HandleCommand(ctx, tt.chatID, "/cancel")
			// Verify no panic; cancel state managed by commsHandler
		})
	}
}

// TestCommandHandler_HandleQueue tests the /queue command
func TestCommandHandler_HandleQueue(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")

	tests := []struct {
		name     string
		store    bool
		hasQueue bool
	}{
		{
			name:     "no store",
			store:    false,
			hasQueue: false,
		},
		// Note: Testing with actual store would require database setup
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cmd *CommandHandler
			if tt.store {
				// Would need actual store
				cmd = NewCommandHandler(h, nil)
			} else {
				cmd = NewCommandHandler(h, nil)
			}

			cmd.HandleCommand(ctx, "chat1", "/queue")
			// Just verify no panic
		})
	}
}

// TestCommandHandler_HandleProjects tests the /projects command
func TestCommandHandler_HandleProjects(t *testing.T) {
	tests := []struct {
		name     string
		projects comms.ProjectSource
	}{
		{
			name:     "no projects configured",
			projects: nil,
		},
		{
			name: "with projects",
			projects: &MockProjectSource{
				projects: []*comms.ProjectInfo{
					{Name: "project-a", Path: "/path/a", Navigator: true},
					{Name: "project-b", Path: "/path/b", Navigator: false},
				},
			},
		},
		{
			name: "empty project list",
			projects: &MockProjectSource{
				projects: []*comms.ProjectInfo{},
			},
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandlerForCommands(tt.projects, "/default/path")
			cmd := NewCommandHandler(h, nil)

			cmd.HandleCommand(ctx, "chat1", "/projects")
			// Just verify no panic
		})
	}
}

// TestCommandHandler_HandleSwitch tests the /switch command
func TestCommandHandler_HandleSwitch(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*comms.ProjectInfo{
			{Name: "project-a", Path: "/path/a"},
			{Name: "project-b", Path: "/path/b"},
		},
	}

	h := newTestHandlerForCommands(projects, "/path/a")
	cmd := NewCommandHandler(h, nil)

	tests := []struct {
		name     string
		command  string
		wantPath string
	}{
		{
			name:     "switch to existing project",
			command:  "/switch project-b",
			wantPath: "/path/b",
		},
		{
			name:     "switch to non-existent project",
			command:  "/switch unknown",
			wantPath: "/path/b", // Stays at last known project (from previous subtest)
		},
		{
			name:     "show current project",
			command:  "/switch",
			wantPath: "/path/b", // Should show current
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd.HandleCommand(ctx, "chat1", tt.command)

			path := h.getActiveProjectPath("chat1")
			if path != tt.wantPath {
				t.Errorf("active project path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

// TestCommandHandler_HandleHistory tests the /history command
func TestCommandHandler_HandleHistory(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")

	tests := []struct {
		name  string
		store bool
	}{
		{
			name:  "no store",
			store: false,
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cmd *CommandHandler
			if tt.store {
				cmd = NewCommandHandler(h, nil) // Would need actual store
			} else {
				cmd = NewCommandHandler(h, nil)
			}

			cmd.HandleCommand(ctx, "chat1", "/history")
			// Just verify no panic
		})
	}
}

// TestCommandHandler_HandleBudget tests the /budget command
func TestCommandHandler_HandleBudget(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "/budget")
	// Just verify no panic
}

// TestCommandHandler_HandleTasks tests the /tasks command
func TestCommandHandler_HandleTasks(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/nonexistent/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "/tasks")
	// Just verify no panic
}

// TestCommandHandler_UnknownCommand tests handling of unknown commands
func TestCommandHandler_UnknownCommand(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "/unknown_command")
	// Just verify no panic
}

// TestFormatTimeAgo tests the time formatting helper
func TestFormatTimeAgo(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		time     time.Time
		expected string
	}{
		{
			name:     "just now",
			time:     now.Add(-30 * time.Second),
			expected: "just now",
		},
		{
			name:     "minutes ago",
			time:     now.Add(-5 * time.Minute),
			expected: "5m ago",
		},
		{
			name:     "hours ago",
			time:     now.Add(-3 * time.Hour),
			expected: "3h ago",
		},
		{
			name:     "days ago",
			time:     now.Add(-2 * 24 * time.Hour),
			expected: "2d ago",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgo(tt.time)
			if got != tt.expected {
				t.Errorf("formatTimeAgo() = %q, want %q", got, tt.expected)
			}
		})
	}
}

// TestFormatTimeAgo_OldDates tests formatting of dates older than a week
func TestFormatTimeAgo_OldDates(t *testing.T) {
	// Dates older than a week should show as "Jan 2" format
	oldDate := time.Now().Add(-14 * 24 * time.Hour)
	got := formatTimeAgo(oldDate)

	// Should be in "Jan 2" format
	if !strings.Contains(got, " ") || len(got) < 4 {
		t.Errorf("formatTimeAgo() for old date = %q, expected date format", got)
	}
}

// TestNewCommandHandler tests command handler creation
func TestNewCommandHandler(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")

	tests := []struct {
		name  string
		store bool
	}{
		{
			name:  "without store",
			store: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cmd *CommandHandler
			if tt.store {
				cmd = NewCommandHandler(h, nil)
			} else {
				cmd = NewCommandHandler(h, nil)
			}

			if cmd == nil {
				t.Fatal("NewCommandHandler returned nil")
			}
			if cmd.handler != h {
				t.Error("handler not set correctly")
			}
		})
	}
}

// TestCommandHandler_HandleCallbackSwitch tests callback-based project switching
func TestCommandHandler_HandleCallbackSwitch(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*comms.ProjectInfo{
			{Name: "project-a", Path: "/path/a"},
			{Name: "project-b", Path: "/path/b"},
		},
	}

	h := newTestHandlerForCommands(projects, "/path/a")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()

	// Set initial project
	_ = h.commsHandler.SetActiveProject("chat1", "project-a")

	cmd.HandleCallbackSwitch(ctx, "chat1", "project-b")

	path := h.getActiveProjectPath("chat1")
	if path != "/path/b" {
		t.Errorf("callback switch failed: path = %q, want %q", path, "/path/b")
	}
}

// TestCommandRouting tests that commands are routed correctly
func TestCommandRouting(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*comms.ProjectInfo{
			{Name: "test", Path: "/test/path"},
		},
	}
	h := newTestHandlerForCommands(projects, "/test/path")
	cmd := NewCommandHandler(h, nil)

	commands := []string{
		"/help",
		"/start",
		"/status",
		"/cancel",
		"/queue",
		"/projects",
		"/project",
		"/project test",
		"/switch",
		"/switch test",
		"/history",
		"/budget",
		"/tasks",
		"/list",
		"/stop",
	}

	ctx := context.Background()
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			// Should not panic
			cmd.HandleCommand(ctx, "chat1", command)
		})
	}
}
