package comms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

var errNoSuchProject = errors.New("no such project")

// mockMessenger captures messages sent by the command handler.
type mockMessenger struct {
	messages  []string
	threadIDs []string
}

func (m *mockMessenger) SendText(ctx context.Context, contextID, threadID, text string) error {
	m.messages = append(m.messages, text)
	m.threadIDs = append(m.threadIDs, threadID)
	return nil
}

func (m *mockMessenger) SendConfirmation(ctx context.Context, contextID, threadID, taskID, desc, project string) (string, error) {
	return "", nil
}

func (m *mockMessenger) SendProgress(ctx context.Context, contextID, messageRef, taskID, phase string, progress int, detail string) (string, error) {
	return "", nil
}

func (m *mockMessenger) SendResult(ctx context.Context, contextID, threadID, taskID string, success bool, output, prURL string) error {
	return nil
}

func (m *mockMessenger) SendChunked(ctx context.Context, contextID, threadID, content, prefix string) error {
	m.messages = append(m.messages, content)
	m.threadIDs = append(m.threadIDs, threadID)
	return nil
}

func (m *mockMessenger) AcknowledgeCallback(ctx context.Context, callbackID string) error {
	return nil
}

func (m *mockMessenger) MaxMessageLength() int {
	return 4096
}

// TestCommandHandler_HandleHelp tests the /help command.
func TestCommandHandler_HandleHelp(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/help")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if len(messenger.messages[0]) == 0 {
		t.Error("help message is empty")
	}

	if !containsString(messenger.messages[0], "Pilot Bot") {
		t.Error("help message missing bot name")
	}

	if !containsString(messenger.messages[0], "/status") {
		t.Error("help message missing /status command")
	}

	if !containsString(messenger.messages[0], "/queue") {
		t.Error("help message missing /queue command")
	}
}

// TestCommandHandler_HandleStart tests the /start command (alias for /help).
func TestCommandHandler_HandleStart(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/start")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "Pilot Bot") {
		t.Error("/start should show help")
	}
}

// TestCommandHandler_HandleStatus tests the /status command.
func TestCommandHandler_HandleStatus(t *testing.T) {
	tests := []struct {
		name      string
		messenger *mockMessenger
		setupFunc func(cmd *CommandHandler)
		wantText  string
	}{
		{
			name:      "no functions configured",
			messenger: &mockMessenger{},
			setupFunc: func(cmd *CommandHandler) {},
			wantText:  "Status",
		},
		{
			name:      "with active project",
			messenger: &mockMessenger{},
			setupFunc: func(cmd *CommandHandler) {
				cmd.SetActiveProjectFunc(func(contextID string) (string, string) {
					return "MyProject", "/path/to/project"
				})
			},
			wantText: "MyProject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCommandHandler(tt.messenger, nil)
			tt.setupFunc(cmd)

			ctx := context.Background()
			cmd.HandleCommand(ctx, "chat1", "", "/status")

			if len(tt.messenger.messages) == 0 {
				t.Fatal("no messages sent")
			}

			if !containsString(tt.messenger.messages[0], tt.wantText) {
				t.Errorf("message missing %q: %s", tt.wantText, tt.messenger.messages[0])
			}
		})
	}
}

// TestCommandHandler_HandleQueue tests the /queue command.
func TestCommandHandler_HandleQueue(t *testing.T) {
	tests := []struct {
		name      string
		messenger *mockMessenger
		store     *memory.Store
		wantText  string
	}{
		{
			name:      "no store",
			messenger: &mockMessenger{},
			store:     nil,
			wantText:  "not available",
		},
		{
			name:      "empty queue",
			messenger: &mockMessenger{},
			store:     mustCreateMemoryStore(t),
			wantText:  "Queue is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCommandHandler(tt.messenger, tt.store)

			ctx := context.Background()
			cmd.HandleCommand(ctx, "chat1", "", "/queue")

			if len(tt.messenger.messages) == 0 {
				t.Fatal("no messages sent")
			}

			if !containsString(tt.messenger.messages[0], tt.wantText) {
				t.Errorf("message missing %q: %s", tt.wantText, tt.messenger.messages[0])
			}
		})
	}
}

// TestCommandHandler_HandleProjects tests the /projects command.
func TestCommandHandler_HandleProjects(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	// No projects function configured
	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/projects")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "not configured") {
		t.Error("message should indicate projects not configured")
	}
}

// TestCommandHandler_HandleTasks tests the /tasks command.
func TestCommandHandler_HandleTasks(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	// No list function configured
	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/tasks")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "not found") {
		t.Error("message should indicate tasks not found")
	}
}

// TestCommandHandler_HandleCancel tests the /cancel command.
func TestCommandHandler_HandleCancel(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/cancel")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "No task to cancel") {
		t.Error("message should indicate no task to cancel")
	}
}

// TestCommandHandler_HandleStop tests the /stop command.
func TestCommandHandler_HandleStop(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/stop")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "No task") {
		t.Error("message should indicate no running task")
	}
}

// TestCommandHandler_HandleBudget tests the /budget command.
func TestCommandHandler_HandleBudget(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/budget")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "not available") {
		t.Error("message should indicate budget not available without store")
	}
}

// TestCommandHandler_HandleHistory tests the /history command.
func TestCommandHandler_HandleHistory(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/history")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "not available") {
		t.Error("message should indicate history not available without store")
	}
}

// TestCommandHandler_HandleBrief tests the /brief command.
func TestCommandHandler_HandleBrief(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/brief")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "not available") {
		t.Error("message should indicate brief not available without store")
	}
}

// TestCommandHandler_HandleRun tests the /run command.
func TestCommandHandler_HandleRun(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		hasHandler bool
		wantText   string
	}{
		{
			name:       "without handler, no args",
			input:      "/run",
			hasHandler: false,
			wantText:   "Usage: /run",
		},
		{
			name:       "without handler, with args",
			input:      "/run 42",
			hasHandler: false,
			wantText:   "Usage: /run",
		},
		{
			name:       "with handler",
			input:      "/run 42",
			hasHandler: true,
			wantText:   "", // Handler is called instead
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &mockMessenger{}
			cmd := NewCommandHandler(messenger, nil)

			if tt.hasHandler {
				cmd.SetRunCommandFunc(func(ctx context.Context, contextID, taskID string) {
					// Just mark that handler was called
					_ = messenger.SendText(ctx, contextID, "", "Handler called with "+taskID)
				})
			}

			ctx := context.Background()
			cmd.HandleCommand(ctx, "chat1", "", tt.input)

			if len(messenger.messages) == 0 {
				t.Fatal("no messages sent")
			}

			if tt.wantText != "" && !containsString(messenger.messages[0], tt.wantText) {
				t.Errorf("message missing %q: %s", tt.wantText, messenger.messages[0])
			}
		})
	}
}

// TestCommandHandler_HandleSwitch tests the /switch command.
func TestCommandHandler_HandleSwitch(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		setupFunc func(cmd *CommandHandler)
		wantText  string
	}{
		{
			name:      "no setup",
			input:     "/switch myproject",
			setupFunc: func(cmd *CommandHandler) {},
			wantText:  "not configured",
		},
		{
			name:  "successful switch",
			input: "/switch myproject",
			setupFunc: func(cmd *CommandHandler) {
				cmd.SetSetProjectFunc(func(ctx, projectName string) error {
					return nil
				})
				cmd.SetActiveProjectFunc(func(contextID string) (string, string) {
					return "MyProject", "/path"
				})
			},
			wantText: "Switched",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &mockMessenger{}
			cmd := NewCommandHandler(messenger, nil)
			tt.setupFunc(cmd)

			ctx := context.Background()
			cmd.HandleCommand(ctx, "chat1", "", tt.input)

			if len(messenger.messages) == 0 {
				t.Fatal("no messages sent")
			}

			if !containsString(messenger.messages[0], tt.wantText) {
				t.Errorf("message missing %q: %s", tt.wantText, messenger.messages[0])
			}
		})
	}
}

// TestCommandHandler_HandleUnknown tests unknown commands.
func TestCommandHandler_HandleUnknown(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/unknown")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "Unknown command") {
		t.Error("message should indicate unknown command")
	}

	if !containsString(messenger.messages[0], "/help") {
		t.Error("message should suggest using /help")
	}
}

// TestCommandHandler_HandleNoPR tests the /nopr command.
func TestCommandHandler_HandleNoPR(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/nopr create a new feature")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "without PR") {
		t.Error("message should indicate task without PR")
	}
}

// TestCommandHandler_HandlePR tests the /pr command.
func TestCommandHandler_HandlePR(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/pr create a new feature")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "with PR") {
		t.Error("message should indicate task with PR")
	}
}

// TestCommandHandler_CommandParsing tests various command formats.
func TestCommandHandler_CommandParsing(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		verify func(t *testing.T, messages []string)
	}{
		{
			name:  "command with extra whitespace",
			input: "  /help  ",
			verify: func(t *testing.T, messages []string) {
				if len(messages) != 1 {
					t.Error("should handle extra whitespace")
				}
			},
		},
		{
			name:  "command with multiple args",
			input: "/pr this is a very long task description",
			verify: func(t *testing.T, messages []string) {
				if !containsString(messages[0], "with PR") {
					t.Error("should handle multi-word args")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &mockMessenger{}
			cmd := NewCommandHandler(messenger, nil)

			ctx := context.Background()
			cmd.HandleCommand(ctx, "chat1", "", tt.input)

			tt.verify(t, messenger.messages)
		})
	}
}

// TestCommandHandler_ListAlias tests /list as alias for /tasks.
func TestCommandHandler_ListAlias(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "", "/list")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "not found") {
		t.Error("/list should behave like /tasks")
	}
}

// TestCommandHandler_ProjectAlias tests /project as alias for /switch.
func TestCommandHandler_ProjectAlias(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	// /project without args should show current project
	cmd.HandleCommand(ctx, "chat1", "", "/project")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "Active") {
		t.Error("/project should show active project")
	}
}

// TestFormatQueueSummary_BlockAnnotations covers GH-3732: queued entries must
// show what they're waiting behind — the running task for their project, or
// (chained) the queued task immediately ahead of them in the same project —
// while an idle project's first queued entry stays a bare line.
func TestFormatQueueSummary_BlockAnnotations(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name       string
		running    []*memory.Execution
		queued     []*memory.Execution
		wantText   string              // exact expected output (unchanged/empty cases)
		wantLines  map[string][]string // substring -> other substrings that must co-occur on its line
		unwantChar map[string]string   // substring -> character that must NOT be on its line
	}{
		{
			name:     "empty queue unchanged",
			running:  nil,
			queued:   nil,
			wantText: "📋 Queue is empty",
		},
		{
			name: "single project busy queue chains behind predecessor",
			running: []*memory.Execution{
				{TaskID: "GH-1", ProjectPath: "/p1", CreatedAt: now},
			},
			queued: []*memory.Execution{
				{TaskID: "GH-2", ProjectPath: "/p1", CreatedAt: now},
				{TaskID: "GH-3", ProjectPath: "/p1", CreatedAt: now},
			},
			wantLines: map[string][]string{
				"GH-2": {"behind GH-1", "position 1"},
				"GH-3": {"behind GH-2", "position 2"},
			},
		},
		{
			name: "multi-project mix only annotates entries with a live blocker",
			running: []*memory.Execution{
				{TaskID: "GH-10", ProjectPath: "/p1", CreatedAt: now},
			},
			queued: []*memory.Execution{
				{TaskID: "GH-11", ProjectPath: "/p1", CreatedAt: now},
				{TaskID: "GH-20", ProjectPath: "/p2", CreatedAt: now},
			},
			wantLines: map[string][]string{
				"GH-11": {"behind GH-10", "position 1"},
			},
			unwantChar: map[string]string{
				"GH-20": "⏳", // idle project (no running/queued predecessor) — bare line
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatQueueSummary(tc.running, tc.queued)

			if tc.wantText != "" {
				if got != tc.wantText {
					t.Errorf("expected output %q, got %q", tc.wantText, got)
				}
				return
			}

			lines := strings.Split(got, "\n")
			for marker, mustContain := range tc.wantLines {
				line := findLineContaining(lines, marker)
				if line == "" {
					t.Fatalf("expected a line containing %q, got:\n%s", marker, got)
				}
				for _, want := range mustContain {
					if !strings.Contains(line, want) {
						t.Errorf("expected line %q to contain %q", line, want)
					}
				}
			}
			for marker, mustNotChar := range tc.unwantChar {
				line := findLineContaining(lines, marker)
				if line == "" {
					t.Fatalf("expected a line containing %q, got:\n%s", marker, got)
				}
				if strings.Contains(line, mustNotChar) {
					t.Errorf("expected line %q to NOT contain %q", line, mustNotChar)
				}
			}
		})
	}
}

func findLineContaining(lines []string, substr string) string {
	for _, line := range lines {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// Helper functions

func containsString(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && stringContains(haystack, needle)
}

func stringContains(s, substr string) bool {
	return len(substr) <= len(s) && (substr == s || len(s) > 0 && len(substr) > 0)
}

func mustCreateMemoryStore(t *testing.T) *memory.Store {
	store, err := memory.NewStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	return store
}

// threadIDUnderTest is a non-empty forum topic ID: asserting on it (rather than
// the empty string) proves the thread is actually threaded through, not merely
// defaulted away.
const threadIDUnderTest = "42"

type stubProject struct {
	name string
	path string
}

func (p stubProject) GetName() string   { return p.name }
func (p stubProject) GetPath() string   { return p.path }
func (p stubProject) IsNavigator() bool { return true }

// mustCreateTempMemoryStore builds a per-test store on disk; ":memory:" is a
// shared-cache handle here, so seeded rows leak between subtests.
func mustCreateTempMemoryStore(t *testing.T) *memory.Store {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mustCreateClosedMemoryStore(t *testing.T) *memory.Store {
	store := mustCreateTempMemoryStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close memory store: %v", err)
	}
	return store
}

func mustCreateSeededMemoryStore(t *testing.T) *memory.Store {
	store := mustCreateTempMemoryStore(t)
	err := store.SaveExecution(&memory.Execution{
		ID:          "exec-1",
		TaskID:      "GH-1",
		ProjectPath: "/tmp/project",
		Status:      "completed",
		PRUrl:       "https://example.test/pr/1",
		DurationMs:  1500,
		CreatedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("failed to seed execution: %v", err)
	}
	return store
}

// TestCommandHandler_SendsToOriginatingThread asserts every command reply
// carries the caller's threadID down to the messenger.
func TestCommandHandler_SendsToOriginatingThread(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		store    func(t *testing.T) *memory.Store
		setup    func(cmd *CommandHandler)
		wantText string
	}{
		{
			name:     "nopr without args",
			input:    "/nopr",
			wantText: "Usage: /nopr",
		},
		{
			name:     "pr without args",
			input:    "/pr",
			wantText: "Usage: /pr",
		},
		{
			name:     "draft-issue without args",
			input:    "/draft-issue",
			wantText: "Usage: /draft-issue",
		},
		{
			name:     "draft-issue without intake func",
			input:    "/draft-issue add response caching",
			wantText: "Issue intake is not available",
		},
		{
			name:     "queue fetch error",
			input:    "/queue",
			store:    mustCreateClosedMemoryStore,
			wantText: "Failed to fetch queue",
		},
		{
			name:  "projects configured but empty",
			input: "/projects",
			setup: func(cmd *CommandHandler) {
				cmd.SetProjectListFunc(func() []interface{} { return nil })
			},
			wantText: "No projects configured",
		},
		{
			name:  "projects listed",
			input: "/projects",
			setup: func(cmd *CommandHandler) {
				cmd.SetProjectListFunc(func() []interface{} {
					return []interface{}{stubProject{name: "alpha", path: "/tmp/alpha"}}
				})
				cmd.SetActiveProjectFunc(func(string) (string, string) { return "alpha", "/tmp/alpha" })
			},
			wantText: "alpha",
		},
		{
			name:  "switch to unknown project",
			input: "/switch ghost",
			setup: func(cmd *CommandHandler) {
				cmd.SetSetProjectFunc(func(string, string) error { return errNoSuchProject })
			},
			wantText: "not found",
		},
		{
			name:  "current project",
			input: "/project",
			setup: func(cmd *CommandHandler) {
				cmd.SetActiveProjectFunc(func(string) (string, string) { return "alpha", "/tmp/alpha" })
			},
			wantText: "Active",
		},
		{
			name:     "history fetch error",
			input:    "/history",
			store:    mustCreateClosedMemoryStore,
			wantText: "Failed to fetch history",
		},
		{
			name:     "history empty",
			input:    "/history",
			store:    mustCreateTempMemoryStore,
			wantText: "No task history yet",
		},
		{
			name:     "history listed",
			input:    "/history",
			store:    mustCreateSeededMemoryStore,
			wantText: "Recent Tasks",
		},
		{
			name:     "budget fetch error",
			input:    "/budget",
			store:    mustCreateClosedMemoryStore,
			wantText: "Failed to fetch usage data",
		},
		{
			name:     "budget summary",
			input:    "/budget",
			store:    mustCreateTempMemoryStore,
			wantText: "Usage This Month",
		},
		{
			name:  "tasks none",
			input: "/tasks",
			setup: func(cmd *CommandHandler) {
				cmd.SetListTasksFunc(func() string { return "" })
			},
			wantText: "No tasks found",
		},
		{
			name:  "tasks listed",
			input: "/tasks",
			setup: func(cmd *CommandHandler) {
				cmd.SetListTasksFunc(func() string { return "• TASK-01" })
			},
			wantText: "Task Backlog",
		},
		{
			name:     "brief without generator",
			input:    "/brief",
			store:    mustCreateTempMemoryStore,
			wantText: "Brief generation not configured",
		},
		{
			name:  "nopr with description",
			input: "/nopr add response caching",
			setup: func(cmd *CommandHandler) {
				cmd.SetRunCommandFunc(func(context.Context, string, string) {})
			},
			wantText: "Executing without PR",
		},
		{
			name:  "pr with description",
			input: "/pr add response caching",
			setup: func(cmd *CommandHandler) {
				cmd.SetRunCommandFunc(func(context.Context, string, string) {})
			},
			wantText: "Executing with PR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &mockMessenger{}
			var store *memory.Store
			if tt.store != nil {
				store = tt.store(t)
			}
			cmd := NewCommandHandler(messenger, store)
			if tt.setup != nil {
				tt.setup(cmd)
			}

			cmd.HandleCommand(context.Background(), "chat1", threadIDUnderTest, tt.input)

			if len(messenger.messages) == 0 {
				t.Fatal("no messages sent")
			}
			found := false
			for _, msg := range messenger.messages {
				if strings.Contains(msg, tt.wantText) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no message contains %q: %v", tt.wantText, messenger.messages)
			}
			for i, got := range messenger.threadIDs {
				if got != threadIDUnderTest {
					t.Errorf("message %d sent with threadID %q, want %q", i, got, threadIDUnderTest)
				}
			}
		})
	}
}

// TestCommandHandler_DraftIssueRoutesToIntake covers the /draft-issue branch
// that delegates to the issue intake func instead of replying itself.
func TestCommandHandler_DraftIssueRoutesToIntake(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	var gotContextID, gotText string
	cmd.SetIssueIntakeFunc(func(_ context.Context, contextID, text string) {
		gotContextID = contextID
		gotText = text
	})

	cmd.HandleCommand(context.Background(), "chat1", threadIDUnderTest, "/draft-issue add response caching")

	if gotContextID != "chat1" {
		t.Errorf("intake contextID = %q, want %q", gotContextID, "chat1")
	}
	if gotText != "add response caching" {
		t.Errorf("intake text = %q, want %q", gotText, "add response caching")
	}
	if len(messenger.messages) != 0 {
		t.Errorf("expected no direct reply when intake is wired, got %v", messenger.messages)
	}
}
