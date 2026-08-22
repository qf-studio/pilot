package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/comms"
	"github.com/qf-studio/pilot/internal/executor"
	"github.com/qf-studio/pilot/internal/memory"
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
	cmd.HandleCommand(ctx, "123", "", "/help")

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
			cmd.HandleCommand(ctx, tt.chatID, "", "/status")
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
			cmd.HandleCommand(ctx, tt.chatID, "", "/cancel")
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

			cmd.HandleCommand(ctx, "chat1", "", "/queue")
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

			cmd.HandleCommand(ctx, "chat1", "", "/projects")
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
			cmd.HandleCommand(ctx, "chat1", "", tt.command)

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

			cmd.HandleCommand(ctx, "chat1", "", "/history")
			// Just verify no panic
		})
	}
}

// TestCommandHandler_HandleBudget tests the /budget command
func TestCommandHandler_HandleBudget(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/budget")
	// Just verify no panic
}

// TestCommandHandler_HandleTasks tests the /tasks command
func TestCommandHandler_HandleTasks(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/nonexistent/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/tasks")
	// Just verify no panic
}

// TestCommandHandler_UnknownCommand tests handling of unknown commands
func TestCommandHandler_UnknownCommand(t *testing.T) {
	h := newTestHandlerForCommands(nil, "/test/path")
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/unknown_command")
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

	cmd.HandleCallbackSwitch(ctx, "chat1", "", "project-b")

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
			cmd.HandleCommand(ctx, "chat1", "", command)
		})
	}
}

func TestSplitCommandMention(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		botUsername   string
		wantCmd       string
		wantAddressed bool
	}{
		{name: "bare command", token: "/status", botUsername: "pilotbot", wantCmd: "/status", wantAddressed: true},
		{name: "suffixed with our name", token: "/status@pilotbot", botUsername: "pilotbot", wantCmd: "/status", wantAddressed: true},
		{name: "suffix case-insensitive", token: "/Status@PilotBot", botUsername: "pilotbot", wantCmd: "/status", wantAddressed: true},
		{name: "addressed to another bot", token: "/status@otherbot", botUsername: "pilotbot", wantCmd: "/status", wantAddressed: false},
		{name: "unknown bot username accepts any suffix", token: "/status@pilotbot", botUsername: "", wantCmd: "/status", wantAddressed: true},
		{name: "uppercase bare command", token: "/STATUS", botUsername: "pilotbot", wantCmd: "/status", wantAddressed: true},
		{name: "empty mention", token: "/status@", botUsername: "pilotbot", wantCmd: "/status", wantAddressed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, addressed := splitCommandMention(tt.token, tt.botUsername)
			if cmd != tt.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, tt.wantCmd)
			}
			if addressed != tt.wantAddressed {
				t.Errorf("addressed = %v, want %v", addressed, tt.wantAddressed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Forum-topic threading: every command reply must carry message_thread_id.
// ---------------------------------------------------------------------------

// threadIDUnderTest is a non-empty forum topic ID: asserting on it (rather than
// the empty string) proves the thread is actually threaded through, not merely
// defaulted away.
const threadIDUnderTest = "42"

type capturedSend struct {
	text     string
	threadID int64
}

// threadCapturingServer records the text and message_thread_id of every
// sendMessage call.
type threadCapturingServer struct {
	server *httptest.Server
	mu     sync.Mutex
	sends  []capturedSend
}

func newThreadCapturingServer(t *testing.T) *threadCapturingServer {
	t.Helper()
	s := &threadCapturingServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/sendMessage") {
			var req SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
				s.mu.Lock()
				s.sends = append(s.sends, capturedSend{text: req.Text, threadID: req.MessageThreadID})
				s.mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 123, ChatID: 456}})
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *threadCapturingServer) captured() []capturedSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedSend, len(s.sends))
	copy(out, s.sends)
	return out
}

func (s *threadCapturingServer) assertAllSentToThread(t *testing.T, wantText string) {
	t.Helper()
	sends := s.captured()
	if len(sends) == 0 {
		t.Fatal("no messages sent")
	}
	found := false
	for _, send := range sends {
		if strings.Contains(send.text, wantText) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no message contains %q: %v", wantText, sends)
	}
	want := parseThreadID(threadIDUnderTest)
	for i, send := range sends {
		if send.threadID != want {
			t.Errorf("message %d sent with message_thread_id %d, want %d", i, send.threadID, want)
		}
	}
}

// mustCreateTelegramStore builds a per-test store on disk; ":memory:" is a
// shared-cache handle here, so seeded rows leak between subtests.
func mustCreateTelegramStore(t *testing.T) *memory.Store {
	t.Helper()
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustCreateClosedTelegramStore(t *testing.T) *memory.Store {
	t.Helper()
	store := mustCreateTelegramStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close memory store: %v", err)
	}
	return store
}

func mustCreateQueuedTelegramStore(t *testing.T) *memory.Store {
	t.Helper()
	store := mustCreateTelegramStore(t)
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-queued",
		TaskID:      "GH-1",
		ProjectPath: "/tmp/project",
		Status:      "queued",
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("failed to seed queued execution: %v", err)
	}
	return store
}

func mustCreateHistoryTelegramStore(t *testing.T) *memory.Store {
	t.Helper()
	store := mustCreateTelegramStore(t)
	if err := store.SaveExecution(&memory.Execution{
		ID:          "exec-done",
		TaskID:      "GH-2",
		ProjectPath: "/tmp/project",
		Status:      "completed",
		PRUrl:       "https://example.test/pr/2",
		DurationMs:  1500,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("failed to seed completed execution: %v", err)
	}
	return store
}

func mustCreateTasksDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	tasksDir := filepath.Join(root, ".agent", "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "TASK-01-demo.md"), []byte("# TASK-01: Demo task\n"), 0o644); err != nil {
		t.Fatalf("write task file: %v", err)
	}
	return root
}

// TestCommandHandler_SendsToOriginatingTopic asserts every Telegram command
// reply carries the forum topic the command arrived in.
func TestCommandHandler_SendsToOriginatingTopic(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		store       func(t *testing.T) *memory.Store
		projectPath func(t *testing.T) string
		noComms     bool
		seedPending bool
		wantText    string
	}{
		{name: "run without args", input: "/run", wantText: "Usage: /run"},
		{name: "nopr without args", input: "/nopr", wantText: "Usage: /nopr"},
		{name: "pr without args", input: "/pr", wantText: "Usage: /pr"},
		{name: "unknown command", input: "/frobnicate", wantText: "Unknown command"},
		{name: "nopr with description", input: "/nopr add response caching", noComms: true, wantText: "Executing without PR"},
		{name: "pr with description", input: "/pr add response caching", noComms: true, wantText: "Executing with PR"},
		{name: "cancel with nothing pending", input: "/cancel", wantText: "No task to cancel."},
		{name: "cancel without comms handler", input: "/cancel", noComms: true, wantText: "No task to cancel."},
		{name: "stop with nothing running", input: "/stop", wantText: "No task is currently running."},
		{name: "stop without comms handler", input: "/stop", noComms: true, wantText: "No task is currently running."},
		{name: "status", input: "/status", wantText: "Status"},
		{name: "brief without store", input: "/brief", wantText: "Brief not available"},
		{name: "brief generated", input: "/brief", store: mustCreateTelegramStore, wantText: "Generating brief"},
		{name: "queue fetch error", input: "/queue", store: mustCreateClosedTelegramStore, wantText: "Failed to fetch queue"},
		{name: "queue empty", input: "/queue", store: mustCreateTelegramStore, wantText: "Queue is empty"},
		{name: "queue empty with pending confirmation", input: "/queue", store: mustCreateTelegramStore, seedPending: true, wantText: "pending confirmation"},
		{name: "queue listed", input: "/queue", store: mustCreateQueuedTelegramStore, wantText: "Task Queue"},
		{name: "history fetch error", input: "/history", store: mustCreateClosedTelegramStore, wantText: "Failed to fetch history"},
		{name: "history empty", input: "/history", store: mustCreateTelegramStore, wantText: "No task history yet"},
		{name: "history listed", input: "/history", store: mustCreateHistoryTelegramStore, wantText: "Recent Tasks"},
		{name: "budget fetch error", input: "/budget", store: mustCreateClosedTelegramStore, wantText: "Failed to fetch usage data"},
		{name: "budget summary", input: "/budget", store: mustCreateTelegramStore, wantText: "Usage This Month"},
		{name: "tasks none", input: "/tasks", wantText: "No tasks found"},
		{name: "tasks listed", input: "/tasks", projectPath: mustCreateTasksDir, wantText: "Task Backlog"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newThreadCapturingServer(t)

			projectPath := "/test/path"
			if tt.projectPath != nil {
				projectPath = tt.projectPath(t)
			}

			h := &Handler{
				client:      NewClientWithBaseURL(testutil.FakeTelegramBotToken, srv.server.URL),
				projectPath: projectPath,
			}
			if !tt.noComms {
				h.commsHandler = comms.NewHandler(&comms.HandlerConfig{
					Messenger:    &noopMessenger{},
					ProjectPath:  projectPath,
					TaskIDPrefix: "TG",
				})
			}

			var store *memory.Store
			if tt.store != nil {
				store = tt.store(t)
			}
			cmd := NewCommandHandler(h, store)

			ctx := context.Background()
			if tt.seedPending {
				h.commsHandler.HandleMessage(ctx, &comms.IncomingMessage{
					ContextID: "123",
					SenderID:  "u1",
					Text:      "Add a rate limiter to the gateway",
				})
				if h.commsHandler.GetPendingTask("123") == nil {
					t.Fatal("failed to seed a pending task")
				}
			}

			cmd.HandleCommand(ctx, "123", threadIDUnderTest, tt.input)

			srv.assertAllSentToThread(t, tt.wantText)
		})
	}
}

// blockingBackend parks until the execution context is cancelled, so a test can
// observe a genuinely in-flight task.
type blockingBackend struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingBackend) Name() string      { return "blocking" }
func (b *blockingBackend) IsAvailable() bool { return true }

func (b *blockingBackend) Execute(ctx context.Context, _ executor.ExecuteOptions) (*executor.BackendResult, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return &executor.BackendResult{Success: false, Error: "cancelled"}, ctx.Err()
}

// TestCommandHandler_StopRunningTaskSendsToOriginatingTopic covers the /stop
// branch that reports a task it actually stopped.
func TestCommandHandler_StopRunningTaskSendsToOriginatingTopic(t *testing.T) {
	srv := newThreadCapturingServer(t)

	backend := &blockingBackend{started: make(chan struct{})}
	runner := executor.NewRunnerWithBackend(backend)
	runner.SetSkipPreflightChecks(true)
	runner.SetRecordingEnabled(false)

	projectPath := t.TempDir()
	h := &Handler{
		client:      NewClientWithBaseURL(testutil.FakeTelegramBotToken, srv.server.URL),
		projectPath: projectPath,
		commsHandler: comms.NewHandler(&comms.HandlerConfig{
			Messenger:    &noopMessenger{},
			Runner:       runner,
			ProjectPath:  projectPath,
			TaskIDPrefix: "TG",
		}),
	}
	cmd := NewCommandHandler(h, nil)

	ctx := context.Background()
	forcePR := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.commsHandler.ExecuteDirectTask(ctx, "123", threadIDUnderTest, "TG-stop-me", "serve the docs site",
			&comms.DirectTaskOpts{ForcePR: &forcePR})
	}()

	select {
	case <-backend.started:
	case <-time.After(30 * time.Second):
		t.Fatal("backend never started")
	}

	cmd.HandleCommand(ctx, "123", threadIDUnderTest, "/stop")

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("task did not stop")
	}

	srv.assertAllSentToThread(t, "Stopped task")
}
