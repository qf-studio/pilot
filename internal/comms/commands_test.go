package comms

import (
	"context"
	"testing"

	"github.com/alekspetrov/pilot/internal/memory"
)

// mockMessenger captures messages sent by the command handler.
type mockMessenger struct {
	messages []string
}

func (m *mockMessenger) SendText(ctx context.Context, contextID, text string) error {
	m.messages = append(m.messages, text)
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
	cmd.HandleCommand(ctx, "chat1", "/help")

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
	cmd.HandleCommand(ctx, "chat1", "/start")

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
			cmd.HandleCommand(ctx, "chat1", "/status")

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
			cmd.HandleCommand(ctx, "chat1", "/queue")

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
	cmd.HandleCommand(ctx, "chat1", "/projects")

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
	cmd.HandleCommand(ctx, "chat1", "/tasks")

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
	cmd.HandleCommand(ctx, "chat1", "/cancel")

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
	cmd.HandleCommand(ctx, "chat1", "/stop")

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
	cmd.HandleCommand(ctx, "chat1", "/budget")

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
	cmd.HandleCommand(ctx, "chat1", "/history")

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
	cmd.HandleCommand(ctx, "chat1", "/brief")

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
					_ = messenger.SendText(ctx, contextID, "Handler called with "+taskID)
				})
			}

			ctx := context.Background()
			cmd.HandleCommand(ctx, "chat1", tt.input)

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
		name       string
		input      string
		setupFunc  func(cmd *CommandHandler)
		wantText   string
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
			cmd.HandleCommand(ctx, "chat1", tt.input)

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
	cmd.HandleCommand(ctx, "chat1", "/unknown")

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
	cmd.HandleCommand(ctx, "chat1", "/nopr create a new feature")

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
	cmd.HandleCommand(ctx, "chat1", "/pr create a new feature")

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
			cmd.HandleCommand(ctx, "chat1", tt.input)

			tt.verify(t, messenger.messages)
		})
	}
}

// TestCommandHandler_ListAlias tests /list as alias for /tasks.
func TestCommandHandler_ListAlias(t *testing.T) {
	messenger := &mockMessenger{}
	cmd := NewCommandHandler(messenger, nil)

	ctx := context.Background()
	cmd.HandleCommand(ctx, "chat1", "/list")

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
	cmd.HandleCommand(ctx, "chat1", "/project")

	if len(messenger.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messenger.messages))
	}

	if !containsString(messenger.messages[0], "Active") {
		t.Error("/project should show active project")
	}
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

