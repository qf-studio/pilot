package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockTelegramServer creates a test server that captures sent messages
type mockTelegramServer struct {
	server       *httptest.Server
	sentMessages []string
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

// TestCommandHandler_HandleHelp tests the /help command
func TestCommandHandler_HandleHelp(t *testing.T) {
	mock := newMockTelegramServer()
	defer mock.close()

	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "123", "/help")

	// Check that message was formatted (we can't check exact content due to mock server)
	// The handler will try to send but the mock server won't match URLs
	// This test primarily validates that no panic occurs
}

// TestCommandHandler_HandleStatus tests the /status command
func TestCommandHandler_HandleStatus(t *testing.T) {
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}
	cmd := NewCommandHandler(h, nil)

	tests := []struct {
		name        string
		setupFunc   func()
		chatID      string
	}{
		{
			name:   "no running tasks",
			chatID: "chat1",
			setupFunc: func() {
				// Clear all tasks
				h.pendingTasks = make(map[string]*PendingTask)
				h.runningTasks = make(map[string]*RunningTask)
			},
		},
		{
			name:   "with running task",
			chatID: "chat2",
			setupFunc: func() {
				h.runningTasks["chat2"] = &RunningTask{
					TaskID:    "TASK-01",
					ChatID:    "chat2",
					StartedAt: time.Now().Add(-5 * time.Minute),
				}
			},
		},
		{
			name:   "with pending task",
			chatID: "chat3",
			setupFunc: func() {
				h.pendingTasks["chat3"] = &PendingTask{
					TaskID:      "TASK-02",
					Description: "Test task",
					ChatID:      "chat3",
					CreatedAt:   time.Now().Add(-2 * time.Minute),
				}
			},
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupFunc()
			// This will fail to send (no real Telegram) but should not panic
			cmd.HandleCommand(ctx, tt.chatID, "/status")
		})
	}
}

// TestCommandHandler_HandleCancel tests the /cancel command
func TestCommandHandler_HandleCancel(t *testing.T) {
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
		runner:        nil, // No runner for this test
	}
	cmd := NewCommandHandler(h, nil)

	tests := []struct {
		name        string
		setupFunc   func()
		chatID      string
		wantPending int
		wantRunning int
	}{
		{
			name:   "cancel pending task",
			chatID: "chat1",
			setupFunc: func() {
				h.pendingTasks["chat1"] = &PendingTask{
					TaskID:      "TASK-01",
					Description: "Test task",
					ChatID:      "chat1",
					CreatedAt:   time.Now(),
				}
			},
			wantPending: 0,
			wantRunning: 0,
		},
		{
			name:   "cancel running task",
			chatID: "chat2",
			setupFunc: func() {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				h.runningTasks["chat2"] = &RunningTask{
					TaskID:    "TASK-02",
					ChatID:    "chat2",
					StartedAt: time.Now(),
					Cancel:    cancel,
				}
				_ = ctx // suppress unused warning
			},
			wantPending: 0,
			wantRunning: 0,
		},
		{
			name:   "nothing to cancel",
			chatID: "chat3",
			setupFunc: func() {
				// No tasks
			},
			wantPending: 0,
			wantRunning: 0,
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset state
			h.pendingTasks = make(map[string]*PendingTask)
			h.runningTasks = make(map[string]*RunningTask)

			tt.setupFunc()
			cmd.HandleCommand(ctx, tt.chatID, "/cancel")

			h.mu.Lock()
			pendingCount := len(h.pendingTasks)
			runningCount := len(h.runningTasks)
			h.mu.Unlock()

			if pendingCount != tt.wantPending {
				t.Errorf("pending count = %d, want %d", pendingCount, tt.wantPending)
			}
			if runningCount != tt.wantRunning {
				t.Errorf("running count = %d, want %d", runningCount, tt.wantRunning)
			}
		})
	}
}

// TestCommandHandler_HandleQueue tests the /queue command
func TestCommandHandler_HandleQueue(t *testing.T) {
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}

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
		projects ProjectSource
	}{
		{
			name:     "no projects configured",
			projects: nil,
		},
		{
			name: "with projects",
			projects: &MockProjectSource{
				projects: []*ProjectInfo{
					{Name: "project-a", Path: "/path/a", Navigator: true},
					{Name: "project-b", Path: "/path/b", Navigator: false},
				},
			},
		},
		{
			name: "empty project list",
			projects: &MockProjectSource{
				projects: []*ProjectInfo{},
			},
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{
				client:        NewClient("test-token"),
				pendingTasks:  make(map[string]*PendingTask),
				runningTasks:  make(map[string]*RunningTask),
				activeProject: make(map[string]string),
				projectPath:   "/default/path",
				projects:      tt.projects,
			}
			cmd := NewCommandHandler(h, nil)

			cmd.HandleCommand(ctx, "chat1", "/projects")
			// Just verify no panic
		})
	}
}

// TestCommandHandler_HandleSwitch tests the /switch command
func TestCommandHandler_HandleSwitch(t *testing.T) {
	projects := &MockProjectSource{
		projects: []*ProjectInfo{
			{Name: "project-a", Path: "/path/a"},
			{Name: "project-b", Path: "/path/b"},
		},
	}

	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/path/a",
		projects:      projects,
	}
	cmd := NewCommandHandler(h, nil)

	tests := []struct {
		name        string
		command     string
		wantPath    string
	}{
		{
			name:     "switch to existing project",
			command:  "/switch project-b",
			wantPath: "/path/b",
		},
		{
			name:     "switch to non-existent project",
			command:  "/switch unknown",
			wantPath: "/path/a", // Should remain unchanged
		},
		{
			name:     "show current project",
			command:  "/switch",
			wantPath: "/path/a", // Should show current
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h.activeProject["chat1"] = "/path/a" // Reset
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
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}

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
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "/budget")
	// Just verify no panic
}

// TestCommandHandler_HandleTasks tests the /tasks command
func TestCommandHandler_HandleTasks(t *testing.T) {
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/nonexistent/path",
	}
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "/tasks")
	// Just verify no panic
}

// TestCommandHandler_UnknownCommand tests handling of unknown commands
func TestCommandHandler_UnknownCommand(t *testing.T) {
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}
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
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
	}

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
		projects: []*ProjectInfo{
			{Name: "project-a", Path: "/path/a"},
			{Name: "project-b", Path: "/path/b"},
		},
	}

	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/path/a",
		projects:      projects,
	}
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	h.activeProject["chat1"] = "/path/a"

	cmd.HandleCallbackSwitch(ctx, "chat1", "project-b")

	path := h.getActiveProjectPath("chat1")
	if path != "/path/b" {
		t.Errorf("callback switch failed: path = %q, want %q", path, "/path/b")
	}
}

// TestCommandRouting tests that commands are routed correctly
func TestCommandRouting(t *testing.T) {
	h := &Handler{
		client:        NewClient("test-token"),
		pendingTasks:  make(map[string]*PendingTask),
		runningTasks:  make(map[string]*RunningTask),
		activeProject: make(map[string]string),
		projectPath:   "/test/path",
		projects: &MockProjectSource{
			projects: []*ProjectInfo{
				{Name: "test", Path: "/test/path"},
			},
		},
	}
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
